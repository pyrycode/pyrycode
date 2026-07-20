package main

import (
	"context"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// streamTurnSinkBuf is the fan-in channel's buffer, matching the interactive
// push-queue precedent (v2session_modal.go's pushQueueCap = 256): post-#609
// coalescing makes turnevents arrive per-message / ~250ms, so 256 slots absorb a
// burst without engaging the drop path under normal load. Past it the sink drops
// the newest event rather than block — wedging claude's stdout forwarder is worse
// than losing a delta the emitter is explicitly not obliged to queue.
const streamTurnSinkBuf = 256

// streamTurnEnvelope is one fan-in element: a neutral turnevent.Event tagged with
// the pool session id of the runner that produced it. The tag is what the drain
// gate compares against the active conversation's bound session (AC2 scoping) —
// the Parser carries no conversation identity, only its construction-time session
// id.
type streamTurnEnvelope struct {
	sessionID string
	ev        turnevent.Event
}

// streamTurnSink is the late-bound, daemon-singleton fan-in that lines up two
// disjoint lifetimes: N stream-json Parsers (one per session, each fixed at
// runner construction, where no emitter handle exists) and the one emitter built
// later on the relay leg. Every Parser's sink pushes {sessionID, ev} onto the one
// buffered channel; the drain goroutine (startStreamTurnDrainV2) is the sole
// reader. This is fan-IN to a single point, not shared fan-out — no session-keyed
// registry and no subscribe/unsubscribe; the per-conn fan-out stays inside the
// unchanged emitter.
//
// The channel is NEVER closed: a Parser runs on os/exec's stdout forwarder
// goroutine and may outlive the drain during shutdown, so a send-on-closed panic
// must be structurally impossible. The drain stops on ctx, not on close; any
// post-shutdown send lands in the non-blocking drop path.
type streamTurnSink struct {
	ch     chan streamTurnEnvelope
	logger *slog.Logger
}

// newStreamTurnSink constructs the fan-in. buf <= 0 falls back to
// streamTurnSinkBuf; logger backs only the content-free drop diagnostic, nil
// falling back to slog.Default.
func newStreamTurnSink(buf int, logger *slog.Logger) *streamTurnSink {
	if buf <= 0 {
		buf = streamTurnSinkBuf
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &streamTurnSink{
		ch:     make(chan streamTurnEnvelope, buf),
		logger: logger,
	}
}

// sinkFor returns the per-Parser sink closure the factory hands to
// streamsup.NewParser for the runner constructed with sessionID. The closure does
// a NON-BLOCKING send of {sessionID, ev}: the Parser runs on claude's stdout
// forwarder goroutine, so a blocking send on a full channel would wedge the
// child. On a full channel it drops the newest event — the channel IS the queue
// and drops rather than blocks, mirroring the emitter's owns-no-queue principle.
// It holds no lock (channel send only).
func (s *streamTurnSink) sinkFor(sessionID string) func(turnevent.Event) {
	return func(ev turnevent.Event) {
		select {
		case s.ch <- streamTurnEnvelope{sessionID: sessionID, ev: ev}:
		default:
			// SECURITY: content-free — the discriminant and session id only, never
			// the event's assistant / thought / tool content.
			s.logger.Debug("relay: stream-turn drop; sink full",
				"event", "stream_turn.sink_full",
				"kind", eventKind(ev),
				"session_id", sessionID)
		}
	}
}

// startStreamTurnDrainV2 spawns the single drain goroutine that feeds the
// unchanged interactiveTurnEmitterV2 from the fan-in sink — the stream-json
// analogue of the PTY path's OnEvent / FlushSignal / OnFlush triple
// (startInteractiveTurnStreamV2), without turnbridge: the Parser already emits
// turnevent.Event, so there is nothing to un-map back to a tuidriver.Event.
//
// The goroutine selects over three cases:
//   - sink.ch: resolve activeSession(); forward the event to emitter.Handle only
//     when the producing session is the active conversation's bound session
//     (AC2). Any other session's event is dropped here, BEFORE Handle, so a
//     background conversation's conn never receives it. Gating at Handle time
//     (not in the sink) keeps the stamp consistent with the cursor the emitter
//     reads inside Handle.
//   - emitter.flushC(): the ~250ms coalescing timer fired — route it back into
//     flushDelta on THIS goroutine. The emitter arms the timer inside Handle and
//     needs a driver to select it; the drain is that driver, so both Handle and
//     flushDelta run on the one goroutine (no cross-goroutine timer race).
//   - ctx.Done(): stop.
//
// Single-writer invariant: only this goroutine ever calls Handle / flushDelta, so
// the emitter's unguarded lifecycle / coalescing fields stay race-free — the same
// single-Run-goroutine assumption the PTY producer relies on. The returned
// cleanup blocks until the goroutine exits, mirroring startInteractiveTurnStreamV2.
//
// The emitter is passed in (not built here) so the caller owns its construction
// and replay wiring; #1081 composes activeSession from the active-conversation
// cursor + the bound-session lookup and wires SetReplaySource. This ticket's
// caller is the unit test.
func startStreamTurnDrainV2(
	ctx context.Context,
	sink *streamTurnSink,
	emitter *interactiveTurnEmitterV2,
	activeSession func() (sessionID string, ok bool),
	logger *slog.Logger,
) (cleanup func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case env := <-sink.ch:
				active, ok := activeSession()
				if !ok || env.sessionID != active {
					// SECURITY: content-free — discriminant + session id only.
					logger.Debug("relay: stream-turn drop; not active session",
						"event", "stream_turn.not_active",
						"kind", eventKind(env.ev),
						"session_id", env.sessionID)
					continue
				}
				emitter.Handle(ctx, env.ev)
			case <-emitter.flushC():
				emitter.flushDelta(ctx)
			}
		}
	}()
	return func() { <-done }
}
