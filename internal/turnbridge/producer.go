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

// Target is everything one (re)subscription needs, resolved fresh per
// subscription by a TargetResolver: which session host to wait on + capture the
// live session from, which JSONL to tail (and from what offset), and an optional
// channel that forces a re-subscribe when it fires. It is the follow-active
// generalisation (#679) of the single fixed host + resolver the subscriber used
// to bake in — turnbridge stays neutral about how a Target is chosen.
type Target struct {
	// Host is the supervisor seam to WaitForPTY on and capture Session() from.
	Host SessionHost
	// Resolve yields which JSONL to tail and from what offset, re-evaluated per
	// transient retry within a subscription so a not-yet-present file is gated
	// then picked up (the #671 cold-start rule). Same contract as the resolver
	// the single-host subscriber took.
	Resolve func(ctx context.Context) (path string, startOffset int64, err error)
	// Switch, if non-nil, tears the subscription down when it fires so
	// Producer.Run re-subscribes onto the now-current Target (follow-active). A
	// nil Switch means session-end and parent-ctx cancel are the only teardown
	// triggers — the pre-#679 single-host behavior.
	Switch <-chan struct{}
}

// TargetResolver yields the current Target. Called once at the top of each
// (re)subscription. A non-nil error is retried (subscribeRetryDelay backoff)
// unless ctx is done — the existing transient-resolve backoff, now also covering
// the rare "active conversation names an unresolvable bound session" case.
type TargetResolver func(ctx context.Context) (Target, error)

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

// NewTargetSubscriber builds the production Subscriber for the follow-active
// producer lifecycle (#679). resolve yields the current Target — which session
// host to stream over, which JSONL to tail, and the optional switch channel that
// forces a re-subscribe when the active conversation changes. tr is required by
// Session.Events (it drives only the dropped stall arm); pass
// tuidriver.NewTracker(tuidriver.TrackerOpts{}).
//
// Structure: an OUTER loop calls resolve once per (re)subscription to snapshot a
// Target, and an INNER loop runs the WaitForPTY → resolve-JSONL → Events sequence
// for that Target. The inner loop's transient retries reuse target.Resolve so
// its per-subscription cold/warm offset state (resolvedOnce/sawEmpty) persists
// across a not-yet-present JSONL — the #671 cold-start gate, now per bound
// session. A switch (target.Switch fires) cancels the per-subscription ctx,
// which aborts any in-flight pre-stream wait and breaks back to the outer loop
// to re-snapshot the now-active Target; this switch-abortable wait is the one new
// invariant beyond the single-host body and is load-bearing for AC3 — without it
// the producer can wedge forever in WaitForPTY of a stale evicted session after
// the operator switches conversations.
//
// Like the single-host subscriber it owns a per-subscription ctx cancelled by
// sess.Wait() (session end → the merge loop closes the Events channel so
// Producer.Run re-subscribes) — "no leaked goroutine across a session restart".
// The switch watcher exits on the same ctx, so it never outlives its
// subscription; both watchers call the idempotent cancel().
func NewTargetSubscriber(
	resolve TargetResolver,
	tr *tuidriver.Tracker,
	log *slog.Logger,
) Subscriber {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context) (<-chan tuidriver.Event, error) {
	resubscribe:
		for {
			// Snapshot the current Target once per (re)subscription. A transient
			// error (e.g. the active conversation names a not-yet-resolvable bound
			// session) backs off and re-snapshots — never falls through to a wrong
			// host, that policy lives in the resolver.
			target, err := resolve(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				log.Warn("turnbridge: resolve target, retrying", "error", err)
				if !sleepCtx(ctx, subscribeRetryDelay) {
					return nil, ctx.Err()
				}
				continue
			}

			// Per-subscription ctx: cancelled by the switch watcher, the
			// session-end watcher, or parent-ctx cancel (context cancel is
			// idempotent). The switch watcher exits on subCtx.Done(), so it
			// cannot leak past this subscription.
			subCtx, cancel := context.WithCancel(ctx)
			if target.Switch != nil {
				go func() {
					select {
					case <-target.Switch:
						cancel()
					case <-subCtx.Done():
					}
				}()
			}

			for {
				// Block until a session is live; abort on a switch. WaitForPTY
				// returns only nil or its ctx's error, so a non-nil err means
				// subCtx is done — either parent cancel (return) or a switch
				// (re-snapshot the now-active Target).
				if err := target.Host.WaitForPTY(subCtx); err != nil {
					cancel()
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					continue resubscribe
				}
				// Capture the session; reuse this subCtx on a transient miss
				// (torn down between wait and capture — WaitForPTY blocks for the
				// next session, so no spin).
				sess := target.Host.Session()
				if sess == nil {
					continue
				}
				// Resolve which JSONL to tail and gate on the file existing.
				path, off, err := target.Resolve(subCtx)
				if err == nil {
					err = tuidriver.WaitForSessionJSONL(subCtx, path)
				}
				if err != nil {
					// Discriminate BEFORE cancel(): calling cancel() makes
					// subCtx.Err() non-nil unconditionally, which would mask a
					// transient resolve error (file not present yet) as a switch.
					switch {
					case ctx.Err() != nil:
						cancel()
						return nil, ctx.Err()
					case subCtx.Err() != nil:
						cancel()
						continue resubscribe // switch fired during the wait
					default:
						log.Warn("turnbridge: resolve session jsonl, retrying", "error", err)
						if !sleepCtx(ctx, subscribeRetryDelay) {
							cancel()
							return nil, ctx.Err()
						}
						continue // transient — retry, reusing target.Resolve state
					}
				}
				// Open the unified event stream under the per-subscription ctx.
				ch, err := sess.Events(subCtx, path, off, tr)
				if err != nil {
					switch {
					case ctx.Err() != nil:
						cancel()
						return nil, ctx.Err()
					case subCtx.Err() != nil:
						cancel()
						continue resubscribe // switch fired during the open
					default:
						log.Warn("turnbridge: open events stream, retrying", "error", err)
						if !sleepCtx(ctx, subscribeRetryDelay) {
							cancel()
							return nil, ctx.Err()
						}
						continue
					}
				}
				// Watch the session: when it ends (restart/idle-evict) sess.Wait()
				// returns, cancelling subCtx so the merge loop closes ch. Spawned
				// only on the success path, so no watcher leaks on a retry. The
				// watcher unblocks on the supervisor's guaranteed sess.Close() at
				// every runOnce exit (including root-ctx cancel), so it never leaks.
				go func() {
					_ = sess.Wait()
					cancel()
				}()
				// Diagnostic for the #671 cold-start fix: records the tailed file
				// and the chosen offset so the operator can confirm the
				// resolve-retry path fired and the subscribe landed at offset 0
				// (offset 0 ⇒ a fresh cold-start session tailed from its start).
				// Logs only the path (a UUID filename under the trusted dir), the
				// offset, and the derived cold_start bool — never any JSONL bytes
				// (substrate seal).
				log.Debug("turnbridge: subscribed to session jsonl",
					"path", path, "offset", off, "cold_start", off == 0)
				return ch, nil
			}
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
