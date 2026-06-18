// Package turnbridge drains the supervised claude session's unified tui-driver
// Events() stream and maps each event into the neutral internal turn-event
// model (internal/turnevent, #606). It is the producer half of the
// structured-event bridge (EPIC #596, ADR 025 § Phase 2 structured streaming);
// the consumer half (#616) attaches an OnEvent callback and fans the mapped
// stream out to capability-advertising phones. This slice ships the producer
// with unit tests, deliberately unwired to a live fan-out.
package turnbridge

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// subscribeRetryDelay caps the spin when a post-WaitForPTY resolution step
// (JSONL not yet present, Events open error) fails transiently. It fires only
// on the abnormal path; the normal path blocks in WaitForPTY then Events.
const subscribeRetryDelay = 500 * time.Millisecond

// Subscriber yields a live session's tui-driver event stream. The returned
// channel closes when that session ends (supervisor restart) or ctx is done.
// Returns a non-nil error ONLY on ctx cancellation; transient resolution
// failures are retried internally, so the channel-or-ctx-done contract holds.
type Subscriber func(ctx context.Context) (<-chan tuidriver.Event, error)

// SessionHost is the supervisor seam the production Subscriber drives.
// *supervisor.Supervisor satisfies it structurally.
type SessionHost interface {
	Session() *tuidriver.Session
	WaitForPTY(ctx context.Context) error
}

// Config configures a Producer.
type Config struct {
	// Subscribe yields the live event stream; required.
	Subscribe Subscriber
	// OnEvent receives each mapped internal event. nil ⇒ the producer drains
	// the source and does nothing else (AC 4's "no-op beyond draining").
	OnEvent func(turnevent.Event)
	// FlushSignal, if non-nil, is selected in the drain loop; each receive
	// invokes OnFlush on the single Run goroutine. The owning consumer drives
	// the timer behind this channel (arm/reset/stop), so a "reset on flush"
	// policy stays single-goroutine-safe. nil ⇒ no periodic-flush arm (#609).
	FlushSignal <-chan time.Time
	// OnFlush, if non-nil and FlushSignal fires, runs on the Run goroutine — the
	// same goroutine as OnEvent — so a consumer may flush state it mutates across
	// OnEvent calls without a lock. nil ⇒ ignored.
	OnFlush func()
	// Logger; nil ⇒ slog.Default().
	Logger *slog.Logger
}

// Producer drains a tui-driver event stream into the neutral turn-event model.
// The OnEvent callback runs on the single Run goroutine, so the consumer's
// callback (#616) must not block it indefinitely — its own queue owns
// backpressure (ADR 025 § Backpressure).
type Producer struct {
	subscribe   Subscriber
	onEvent     func(turnevent.Event)
	flushSignal <-chan time.Time
	onFlush     func()
	log         *slog.Logger
}

// New constructs a Producer. Returns an error if cfg.Subscribe is nil.
func New(cfg Config) (*Producer, error) {
	if cfg.Subscribe == nil {
		return nil, errors.New("turnbridge: Config.Subscribe is required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Producer{
		subscribe:   cfg.Subscribe,
		onEvent:     cfg.OnEvent,
		flushSignal: cfg.FlushSignal,
		onFlush:     cfg.OnFlush,
		log:         log,
	}, nil
}

// Run drives the outer re-subscribe loop until ctx is cancelled. Each iteration
// subscribes (blocking until a live stream exists), drains it until the channel
// closes (session restart) or ctx is done, then re-subscribes. Re-subscribing
// after a channel close is "no leaked goroutine across a session restart": the
// prior session's merge goroutine already closed its channel. Returns ctx.Err()
// on cancellation — the only error Subscribe yields per its contract.
func (p *Producer) Run(ctx context.Context) error {
	for {
		ch, err := p.subscribe(ctx)
		if err != nil {
			return err
		}
		p.drain(ctx, ch)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// drain reads events off ch until ch closes (session restart) or ctx is done,
// satisfying AC 2's clean exit on both. With a nil OnEvent it drains the source
// and does nothing else (AC 4). Otherwise it maps each event, invokes OnEvent on
// a mapped result, and drops + debug-logs the unrepresentable ones (AC 3). A
// non-nil FlushSignal is a third select arm; routing its fire to OnFlush here
// keeps a consumer's timer-driven flush on this single goroutine (#609). A nil
// flushSignal channel is never ready, so the disabled path is one branch.
func (p *Producer) drain(ctx context.Context, ch <-chan tuidriver.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.flushSignal:
			if p.onFlush != nil {
				p.onFlush()
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if p.onEvent == nil {
				continue
			}
			te, ok := mapEvent(ev)
			if !ok {
				p.log.Debug("turnbridge: dropping unrepresentable event", "kind", ev.Kind)
				continue
			}
			p.onEvent(te)
		}
	}
}

// NewSessionSubscriber builds the production Subscriber. resolve yields which
// session JSONL to tail and from what offset (supplied by #616 wiring — the
// resolver re-evaluates per subscription so a /clear rotation picks up the new
// file, and should return the current file size as startOffset so a
// (re)subscription streams only new events). tr is required by Session.Events
// (it drives only the dropped stall arm); pass
// tuidriver.NewTracker(tuidriver.TrackerOpts{}).
//
// The returned closure is the linchpin of "no leaked goroutine across a session
// restart": it owns a per-session ctx cancelled by sess.Wait(), which closes
// the Events channel when the supervised process exits (the merge loop's own
// TailJSONL never closes — the JSONL file persists on disk after claude exits).
func NewSessionSubscriber(
	host SessionHost,
	resolve func(ctx context.Context) (path string, startOffset int64, err error),
	tr *tuidriver.Tracker,
	log *slog.Logger,
) Subscriber {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context) (<-chan tuidriver.Event, error) {
		for {
			// Block until a session is live.
			if err := host.WaitForPTY(ctx); err != nil {
				return nil, err
			}
			// Capture it; retry if torn down between WaitForPTY and capture
			// (WaitForPTY blocks for the next session, so no spin).
			sess := host.Session()
			if sess == nil {
				continue
			}
			// Resolve which JSONL to tail and gate on the file existing.
			path, off, err := resolve(ctx)
			if err == nil {
				err = tuidriver.WaitForSessionJSONL(ctx, path)
			}
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				log.Warn("turnbridge: resolve session jsonl, retrying", "error", err)
				if !sleepCtx(ctx, subscribeRetryDelay) {
					return nil, ctx.Err()
				}
				continue
			}
			// Open the unified event stream under a per-session ctx.
			sessCtx, cancel := context.WithCancel(ctx)
			ch, err := sess.Events(sessCtx, path, off, tr)
			if err != nil {
				cancel()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				log.Warn("turnbridge: open events stream, retrying", "error", err)
				if !sleepCtx(ctx, subscribeRetryDelay) {
					return nil, ctx.Err()
				}
				continue
			}
			// Watch the session: when it ends (restart/idle-evict) sess.Wait()
			// returns, cancelling sessCtx so the merge loop closes ch. Spawned
			// only on the success path, so no watcher leaks on a retry. The
			// watcher unblocks on the supervisor's guaranteed sess.Close() at
			// every runOnce exit (including root-ctx cancel), so it never leaks.
			go func() {
				_ = sess.Wait()
				cancel()
			}()
			// Diagnostic for the #671 cold-start fix: records the tailed file and
			// the chosen offset so the operator can confirm during the live
			// mobile#421 run that the resolve-retry path fired and the subscribe
			// landed at offset 0 (offset 0 ⇒ a fresh cold-start session tailed
			// from its start). Logs only the path (a UUID filename under the
			// trusted dir), the offset, and the derived cold_start bool — never
			// any JSONL bytes (substrate seal).
			log.Debug("turnbridge: subscribed to session jsonl",
				"path", path, "offset", off, "cold_start", off == 0)
			return ch, nil
		}
	}
}

// sleepCtx blocks for d or until ctx is done. Returns true if the full delay
// elapsed, false if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
