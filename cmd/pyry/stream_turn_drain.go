package main

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// streamTurnSinkBuf is the fan-in channel's buffer, matching the interactive
// push-queue precedent (v2session_modal.go's pushQueueCap = 256): post-#609
// coalescing makes turnevents arrive per-message / ~250ms, so 256 slots absorb a
// burst without engaging the drop path under normal load. Past it the sink drops
// the newest event rather than block — wedging claude's stdout forwarder is worse
// than losing a delta the emitter is explicitly not obliged to queue.
//
// That drop is CLASS-AWARE since #1496: only the droppable class is refused at
// the watermark streamTurnSinkCloseReserve leaves, so a turn-closing envelope
// still finds room. A dropped delta costs one event of transcript fidelity; a
// dropped closer wedges the conversation busy forever.
const streamTurnSinkBuf = 256

// streamTurnSinkCloseReserve is how many of streamTurnSinkBuf's slots the
// droppable class may never take, so a turn-closing envelope — a
// turnevent.TurnEnd, or the exit signal — is not crowded out by the very burst
// that ends with it. ADR 025 § Backpressure already requires that control events
// never drop; pushQueue.enqueue implements it downstream and, until #1496, the
// fan-in did not.
//
// WHICH LEG OF THE TRILEMMA THIS YIELDS. bounded ∧ never-drop-control ∧
// never-block-producer is unsatisfiable here exactly as it is for pushQueue, and
// #911 established the saturated state is reachable rather than theoretical. The
// two queues resolve it in OPPOSITE directions on purpose. pushQueue yields
// strictly-bounded — it soft-overflows control past nominal cap — which it can
// afford because #911's per-conn in-flight gate bounds the excursion to one
// bundle's chunks. The fan-in has no analogue of that gate: its producer is
// claude's stdout, an unrate-limited source, so soft overflow here would be an
// unbounded-growth vector driven by a runaway or hostile child. So the fan-in
// yields never-drop-control instead, past this reserve, and keeps its memory
// bound absolute. The reserve PARTITIONS existing capacity; it adds none.
//
// Loss is therefore narrowed rather than made impossible, and every remaining
// closing-class drop is logged at Warn (`sinkFor`, `exitFor`) where a droppable
// drop stays Debug.
//
// SIZING. The reserve must cover, per live stream runner, at most two unprocessed
// closing envelopes (its TurnEnd and its exit) plus at most one droppable slipped
// in by the check-then-send race sinkFor documents. Both terms scale with the
// number of live runners, so 32 covers ~10 concurrently-live runners under
// maximally adversarial interleaving, against a realistic pool of 1–5. It is
// argued rather than measured, and deliberately not config-driven: the Warn
// record is the signal that it needs turning.
const streamTurnSinkCloseReserve = 32

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
// — that returns "unknown" rather than failing (the emitter's `emit`),
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
	ch chan streamTurnEnvelope
	// droppableCap is the high-water mark the droppable class may not cross,
	// leaving cap(ch) - droppableCap slots that only a closing-class envelope can
	// take. Computed once at construction so the hot path is one integer compare.
	droppableCap int
	logger       *slog.Logger
}

// newStreamTurnSink constructs the fan-in. buf <= 0 falls back to
// streamTurnSinkBuf; logger backs only the content-free drop diagnostic, nil
// falling back to slog.Default.
//
// The reserve is clamped to buf/2 so a small test buffer stays workable: at
// buf == 1 it degenerates to 0 and every slot is droppable again, which is what
// keeps the buffer-of-1 drop fixtures (`TestStreamTurnSink_ExitDropWhenFull`)
// meaningful. The clamp also keeps droppableCap >= ceil(buf/2) >= 1 for every
// buf >= 1, so no buffer size can starve the droppable class outright.
func newStreamTurnSink(buf int, logger *slog.Logger) *streamTurnSink {
	if buf <= 0 {
		buf = streamTurnSinkBuf
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &streamTurnSink{
		ch:           make(chan streamTurnEnvelope, buf),
		droppableCap: buf - min(streamTurnSinkCloseReserve, buf/2),
		logger:       logger,
	}
}

// streamSessionTag is the LIVE session tag one stream runner's two fan-in lanes
// read (#1133). It exists because the two ends of that tag have different
// lifetimes: the Parser and the child-exit callback are bound once, at runner
// construction, while the pool session the runner serves rotates under them on
// every stream-mode new_session. Before this type the tag was the runner's
// construction-time id captured in a closure, so a rotation left every later event
// tagged with an id the conversation was no longer bound to and the drain's
// active-session gate dropped all of them until the daemon restarted.
//
// It is written through streamsup.Config.OnSessionRotate, which the runner fires
// from RestartFresh, and read once per event by sinkForTag / exitForTag.
//
// ATOMIC, NOT A MUTEX, and that is a design decision rather than a micro-
// optimisation. The reader is claude's stdout forwarder goroutine on the per-event
// path; the writer is the daemon's new_session dispatch, inside a runner method
// whose own mutex is a documented LEAF (nothing under it may take another lock).
// A mutex here would put a lock on both of those paths and create an ordering
// question to keep answered; a single atomic word has no ordering to state.
//
// The tag can never hold "": an empty tag matches no bound session — the gate
// compares against boundSessionIDForActive, which reports ok == false for an empty
// CurrentSessionID, and no non-empty active can equal it — so an emptied tag would
// black-hole that conversation's stream for the life of the runner. Rotate refuses
// it here because the invariant belongs to this value; RestartFresh's own empty-id
// refusal means production never reaches the guard.
type streamSessionTag struct{ id atomic.Pointer[string] }

// newStreamSessionTag returns a tag seeded with the runner's construction-time
// session id — the value that used to be captured directly by the two lane
// closures, so a tag that is never rotated behaves exactly as the frozen tag did.
func newStreamSessionTag(sessionID string) *streamSessionTag {
	t := &streamSessionTag{}
	t.id.Store(&sessionID)
	return t
}

// ID returns the session id envelopes produced right now must carry. Safe on any
// goroutine; one atomic load, no allocation.
func (t *streamSessionTag) ID() string { return *t.id.Load() }

// Rotate moves the tag onto newID, ignoring "" per the type's invariant. It is the
// method the factory installs as streamsup.Config.OnSessionRotate, so its
// signature is that seam's — see that field for when the runner fires it and for
// the two windows around the rotation.
func (t *streamSessionTag) Rotate(newID string) {
	if newID == "" {
		return
	}
	t.id.Store(&newID)
}

// sinkFor returns the per-Parser sink closure for a runner whose session id never
// changes — the frozen-tag form, kept for the tests and any future caller that has
// a plain id rather than a live one. Production goes through sinkForTag: a stream
// runner's session DOES rotate (#1133).
//
// It delegates rather than duplicating, so there is exactly one implementation of
// the class-aware drop policy documented on sinkForTag.
func (s *streamTurnSink) sinkFor(sessionID string) func(turnevent.Event) {
	return s.sinkForTag(func() string { return sessionID })
}

// exitFor is exitForTag's frozen-tag form, standing to it exactly as sinkFor
// stands to sinkForTag and for the same reason.
func (s *streamTurnSink) exitFor(sessionID string) func() {
	return s.exitForTag(func() string { return sessionID })
}

// sinkForTag returns the per-Parser sink closure the factory hands to
// streamsup.NewParser, tagging each envelope with whatever tag reports AT THE
// MOMENT THE EVENT ARRIVES. Every path out of it is a NON-BLOCKING send or an
// early return: the Parser runs on claude's
// stdout forwarder goroutine, so a blocking send on a full channel would wedge
// the child. The channel IS the queue and drops rather than blocks, mirroring the
// emitter's owns-no-queue principle. It holds no lock (channel send only).
//
// WHAT it drops is class-aware (#1496), split by what LOSING one costs rather
// than by wire type. A droppable event is refused at the droppableCap watermark:
// one event of transcript fidelity, self-healing on the next event. A
// closing-class event — turnMarkClose, i.e. turnevent.TurnEnd — skips the
// watermark and sends against the FULL capacity, because losing one leaves
// turnBusyTracker's mark open with nothing that could ever clear it: no TurnStart
// exists, so every later send_message parks until streamTurnHoldTimeout and
// msgqueue gives up with a session_error.
//
// Reserving for closers ALONE is exactly sufficient, and that follows from the
// tracker's asymmetry rather than from optimism: `setBusy` is idempotent, so
// losing some openers changes nothing and losing all of them means the turn never
// opens and the arriving TurnEnd is a no-op delete. Only open-without-close
// wedges.
//
// The class split deliberately NARROWS ADR 025's wire-level never-drop set, which
// counts tool_* as control. That set is classified on protocol.Envelope.Type at
// the outbound pushQueue; here the fan-in carries turnevent.Event one layer
// upstream, and applying the wire set literally would reserve for ToolStart /
// ToolUpdate — leaving the droppable class empty exactly in the filed repro, a
// tool-heavy burst. tool_* keeps its never-drop status downstream in pushQueue,
// untouched; this reserve is additive protection at a second queue.
//
// The len(s.ch) read races other producers by construction, and the race is
// BOUNDED rather than handled: each producer is a single goroutine (one
// streamsup.Parser per live runner, on os/exec's stdout forwarder), so it has at
// most one check-then-send in flight, and the send stays non-blocking. A slipped
// droppable therefore takes at most one reserve slot per live runner — the term
// streamTurnSinkCloseReserve is sized for — and can never block or admit
// unboundedly.
//
// The tag is read ONCE, at the top, and that read is reused for the envelope and
// for any drop record below it. A rotation racing this closure therefore moves the
// whole event from one id to the other and can never split one event across two —
// which is what keeps a drop diagnostic attributable to the envelope it describes.
func (s *streamTurnSink) sinkForTag(tag func() string) func(turnevent.Event) {
	return func(ev turnevent.Event) {
		sessionID := tag()
		if turnMarkFor(ev) == turnMarkClose {
			select {
			case s.ch <- streamTurnEnvelope{sessionID: sessionID, ev: ev}:
			default:
				// Past the reserve a closer can still be lost, so the residual must be
				// VISIBLE: Warn, not Debug, because the daemon's default level is
				// LevelInfo (see the level selection in `runSupervisor`) — the same
				// argument exitFor's own drop makes.
				//
				// SECURITY: content-free, and this carries "kind" where exitFor's drop
				// does not. That is the same discipline, not a departure from it —
				// discriminant and session id only, never assistant / thought / tool
				// content — applied to a record that HAS a discriminant to name, which
				// an exit envelope does not. eventKind returns the variant name alone
				// and never Unrecognized.Kind, so nothing claude authored reaches here.
				s.logger.Warn("relay: stream-turn close drop; sink full",
					"event", "stream_turn.close_sink_full",
					"kind", eventKind(ev),
					"session_id", sessionID)
			}
			return
		}

		if len(s.ch) < s.droppableCap {
			select {
			case s.ch <- streamTurnEnvelope{sessionID: sessionID, ev: ev}:
				return
			default:
				// The watermark read was stale and the channel filled underneath it.
				// Falls through to the same drop as crossing the watermark: the event is
				// lost either way, and reporting one loss two ways would only invite a
				// reader to think they differ.
			}
		}

		// SECURITY: content-free — the discriminant and session id only, never
		// the event's assistant / thought / tool content.
		s.logger.Debug("relay: stream-turn drop; sink full",
			"event", "stream_turn.sink_full",
			"kind", eventKind(ev),
			"session_id", sessionID)
	}
}

// exitForTag returns the per-runner child-exit closure, tagging its envelope with
// whatever tag reports at the moment the child exits — the same live tag
// sinkForTag reads, bound from the same value one line above it in the factory, so
// the two lanes cannot rotate apart. Its func() type is exactly that of streamsup's child-exit seam
// (a callback field on streamsup.Config), so the wiring binds it at the same
// construction point as sinkFor — newStreamRunnerFactory (streamsup_runner.go,
// #1210), one line below the sinkFor install — and the two lanes carry identical
// session tags by construction. Production installs it on every stream runner
// built there; the unit tests call it directly as well, the precedent
// startStreamTurnDrainV2 itself set in #1098.
//
// The seam is named here by its CONTAINING TYPE rather than by its own name on
// purpose: the unfiredness gate for this slice is a grep for that field name
// across non-test cmd/pyry code, and a comment spelling it would make that check
// report wiring where there is none. That is also why no line number stands in
// for it — the name is what must not appear, not the address.
//
// NON-BLOCKING for sinkFor's reason: it runs on the runner's supervision
// goroutine and must not be wedged by a stalled drain, so a full channel drops
// the newest.
//
// It sends UNWATERMARKED, against the channel's full capacity, where sinkFor
// gates the droppable class at droppableCap. That is not a divergence but the
// same policy: an exit envelope is closing-class (#1496), being the signal that
// clears the mark for a child that died mid-turn, so it takes the reserved tail
// slots exactly as a TurnEnd does. No code change was needed here to get that —
// this closure was already written the way the closing class now requires.
//
// The drop is logged at Warn where sinkFor's DROPPABLE branch is Debug, and that
// asymmetry is the whole diagnostic value of this branch. A dropped event is a
// lost delta; a dropped exit is a conversation that stays busy forever once a
// producer is wired, which is degraded operation and must be visible at the
// daemon's default LevelInfo (Debug is not — see the level selection in
// `runSupervisor`). Since #1496 sinkFor's own closing-class branch reasons the
// same way and is likewise Warn, so the split now runs along the CLASS rather
// than along the two closures.
//
// The select is deliberately NOT factored into a helper shared with sinkFor: the
// common part is one statement while the divergent part is the entire diagnostic
// (level, message, field set), so parameterising the divergence would cost more
// than it saves and would obscure exactly the asymmetry above. #1496's closing
// class does not change that — the two records still differ in message and field
// set, this one omitting "kind" because there is no event to name.
func (s *streamTurnSink) exitForTag(tag func() string) func() {
	return func() {
		sessionID := tag()
		select {
		case s.ch <- streamTurnEnvelope{sessionID: sessionID, exit: true}:
		default:
			// SECURITY: content-free, and no "kind" — there is no event to name.
			// The resolved conversation id is absent because this closure holds no
			// resolver and structurally cannot name one; the conversation-id
			// discipline lives on the clear path (`clearForSession`).
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
					// the membership-mutation protocol (`setBusy`) —
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
