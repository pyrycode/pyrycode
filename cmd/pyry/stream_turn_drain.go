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
//
// exit discriminates the SECOND thing the fan-in carries (#1209): a
// "this runner's child exited" signal for sessionID, which the drain turns into a
// turn-busy clear. It rides this channel rather than a lane of its own precisely
// so it is ordered BEHIND the events the dead child already pushed — the fan-in is
// FIFO with a single reader, so a clear on it cannot be overtaken, whereas one
// delivered on any other lane could land before the drain has processed openers
// the crashed child already emitted and be spent before it was needed.
//
// It is an explicit field, never a nil ev used as a sentinel. A nil sentinel would
// have to be re-checked at every consumer and would make eventKind(nil) reachable
// — that returns "unknown" rather than failing (interactive_turn_v2.go:419-421),
// so a missed check would be silent. More to the point, a nil sentinel IS a value
// of the type interactiveTurnEmitterV2.Handle accepts; with a separate field
// "Handle cannot receive a non-event" stays a type-level fact rather than a
// runtime branch.
type streamTurnEnvelope struct {
	sessionID string
	ev        turnevent.Event
	// exit marks a child-exit signal for sessionID; ev is unset and never read.
	exit bool
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

// exitFor returns the per-runner child-exit closure for the runner constructed
// with sessionID. Its func() type is exactly that of streamsup's child-exit seam
// (internal/streamsup/runner.go:105-143), so the eventual wiring binds it at the
// same construction point as sinkFor and the two lanes carry identical session
// tags by construction. Nothing in production installs it yet — #1210 is the
// wiring slice; this slice's caller is the unit test, the precedent
// startStreamTurnDrainV2 itself set in #1098.
//
// The seam is named here by location rather than by symbol on purpose: the
// unfiredness gate for this slice is a grep for that symbol across non-test
// cmd/pyry code, and a comment mentioning it would make that check report wiring
// where there is none.
//
// Same NON-BLOCKING send as sinkFor, for the same reason: it runs on the runner's
// supervision goroutine and must not be wedged by a stalled drain, so a full
// channel drops the newest.
//
// The drop is logged at Warn where sinkFor's is Debug, and that asymmetry is the
// whole diagnostic value of this branch. A dropped event is a lost delta; a
// dropped exit is a conversation that stays busy forever once a producer is wired,
// which is degraded operation and must be visible at the daemon's default
// LevelInfo (Debug is not — main.go:734-737).
//
// The select is deliberately NOT factored into a helper shared with sinkFor: the
// common part is one statement while the divergent part is the entire diagnostic
// (level, message, field set), so parameterising the divergence would cost more
// than it saves and would obscure exactly the asymmetry above.
func (s *streamTurnSink) exitFor(sessionID string) func() {
	return func() {
		select {
		case s.ch <- streamTurnEnvelope{sessionID: sessionID, exit: true}:
		default:
			// SECURITY: content-free, and no "kind" — there is no event to name.
			// The resolved conversation id is absent because this closure holds no
			// resolver and structurally cannot name one; the conversation-id
			// discipline lives on the clear path (stream_turn_busy.go:213-219).
			s.logger.Warn("relay: stream-turn exit drop; sink full",
				"event", "stream_turn.exit_sink_full",
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
//   - sink.ch: an exit envelope (#1209) clears the producing session's turn and
//     is done; otherwise feed the per-conversation turn-busy tracker, then resolve
//     activeSession() and forward the event to emitter.Handle only when the
//     producing session is the active conversation's bound session (AC2). Any
//     other session's event is dropped here, BEFORE Handle, so a background
//     conversation's conn never receives it. Gating at Handle time (not in the
//     sink) keeps the stamp consistent with the cursor the emitter reads inside
//     Handle.
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
//
// busy is the #1201 per-conversation turn-busy tracker, fed from this same fan-in
// and keyed by the producing session's conversation. It may be nil (observe is a
// nil-receiver no-op), which is what lets the pre-#1201 drain tests keep their
// exact wiring. It stays the concrete pointer rather than an interface so a
// typed-nil can never slip past that guard.
func startStreamTurnDrainV2(
	ctx context.Context,
	sink *streamTurnSink,
	emitter *interactiveTurnEmitterV2,
	activeSession func() (sessionID string, ok bool),
	busy *turnBusyTracker,
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
				if env.exit {
					// FIRST statement of the arm, and each thing it precedes matters.
					// Before observe: an exit carries no event, and routing a non-event
					// through the event path is the confusion the explicit field exists
					// to prevent. Before the active-session gate: the gate drops every
					// event whose producing session is not the ACTIVE conversation's, so
					// an exit filtered there would never clear a BACKGROUND conversation
					// — the common case for a crash. Before Handle: combined with the
					// explicit field, that keeps Handle structurally unable to receive a
					// non-event.
					//
					// Called synchronously on this goroutine, never handed to another:
					// a deferred clear could land after a turn opened by the RESPAWNED
					// child and report a live turn idle. clearForSession is reused as-is
					// — no second session→conversation resolution and no second copy of
					// the membership-mutation protocol (stream_turn_busy.go:229-232) —
					// and it is a nil-receiver no-op, so a drain with no tracker is
					// unaffected.
					busy.clearForSession(env.sessionID)
					continue
				}

				// BEFORE the gate, and the ordering IS the contract: the gate drops
				// every event whose producing session is not the ACTIVE conversation's,
				// so a tracker fed after it would report a background conversation idle
				// because it never heard about it, not because it is idle.
				busy.observe(env.sessionID, env.ev)

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
