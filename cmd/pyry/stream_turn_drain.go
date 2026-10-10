package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
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
	live *liveStreamCapture
	// incarnation identifies a Runner.Run activation, independently of the
	// routing ID reactivation reuses. It never crosses history or wire seams.
	incarnation uint64
	sourceEpoch uint64
	occurredAt  time.Time
	sessionID   string
	// source is captured from the runner's kind and this event's routing tag.
	// Its zero value keeps callers without a known producer untagged.
	source history.SessionProvenance
	ev     turnevent.Event
	// exit marks a child-exit signal for sessionID; ev is unset and never read.
	exit bool
	// exitEpoch is this exit's position on the fan-in's exit lane, stamped by
	// exitForTag at push and left zero on an event envelope, where it is
	// meaningless and never read.
	//
	// It answers the one question the FIFO argument above cannot (#1483). That
	// argument covers every mark placed FROM this channel, because the drain places
	// those in envelope order — but openForDelivery writes a mark straight into
	// turnBusyTracker from the msgqueue drain goroutine, bypassing the fan-in
	// entirely, so an exit still queued behind a busy conversation's event burst can
	// be drained AFTER that mark. Comparing this stamp against the position the mark
	// recorded is what tells a dying child's already-stale exit from the respawned
	// child's own. The session id cannot: RestartFresh rotates sessionID and fires
	// OnSessionRotate BEFORE it cancels, so the dying child's exit carries the NEW id
	// and resolves to the same conversation.
	exitEpoch uint64
	// queued is the position of a successfully enqueued envelope.
	queued uint64
}

type confirmedStreamStop struct {
	env   streamTurnEnvelope
	after uint64
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
	liveCaptureReady  func()           // optional scheduling seam, called outside locks
	live              *daemonLiveState // installed before producers start
	shadowProcessed   atomic.Uint64    // last fully published accepted output
	runtimeReplayRing *eventring.Ring  // installed before workers; boundaries publish on the drain

	runtimeNextProducer uint64                       // guarded by offerMu
	runtimeProducers    map[string]uint64            // latest activation per routing ID, guarded by offerMu
	runtimeProducerTags map[string]*streamSessionTag // registered routing owners, guarded by offerMu
	runtimeEnabled      atomic.Bool
	runtimeHolds        map[string]int               // guarded by offerMu, including in-flight publication
	runtimeLastQueued   map[streamProducerKey]uint64 // accepted output positions per source
	runtimeStopSeen     map[streamProducerKey]uint64 // consumed producer exits, guarded by offerMu
	runtimePending      []runtimeBoundary            // guarded by offerMu
	runtimeWake         chan struct{}
	ch                  chan streamTurnEnvelope
	// offerMu orders successful enqueues against confirmed runner stops. It is
	// a leaf lock: no I/O, publication or tracker operation runs underneath it.
	offerMu     sync.Mutex
	queued      uint64
	stopped     map[streamProducerKey]confirmedStreamStop
	stoppedWake chan struct{}
	// droppableCap is the high-water mark the droppable class may not cross,
	// leaving cap(ch) - droppableCap slots that only a closing-class envelope can
	// take. Computed once at construction so the hot path is one integer compare.
	droppableCap int
	logger       *slog.Logger
	// exits counts the child-exit signals offered to this fan-in and is the sole
	// source of every envelope's exitEpoch (#1483), including confirmed stops.
	// EXITS ONLY, never events: only
	// exit stamps are ever compared, so leaving the event path untouched keeps
	// claude's stdout forwarder free of it and shrinks the invariant a reader has to
	// hold to "how many exits has this fan-in accepted".
	//
	// Atomic rather than mutex-guarded, for the reason streamSessionTag gives on this
	// same pair of goroutines: the writer is the runner's supervision goroutine and
	// the reader sits on turnBusyTracker's lock path, and a single atomic word has no
	// ordering to state where a mutex would have one to keep answered.
	//
	// A monotone counter and nothing else. It is fully predictable by construction,
	// so it is never a nonce, a token, or a uniqueness source for anything
	// security-relevant. Wraparound is not defended against: uint64 needs ~1.8e19
	// child exits, ~5.8e8 years at a sustained 1000 crashes per second.
	exits atomic.Uint64

	// lifecycleClosePending is the non-dropping hand-off from pool teardown to
	// the drain's single emitter goroutine. The wake channel is only a level
	// trigger; the map owns the requests, so a full wake channel coalesces signals
	// without losing a conversation close.
	lifecycleCloseMu      sync.Mutex
	lifecycleClosePending map[string]struct{}
	lifecycleCloseWake    chan struct{}

	// crashLoop is the session_error producer every runner's crash-episode hook
	// reaches through crashLoopForTag (#2724). Late-bound because the pool, and with
	// it the bootstrap runner, is built before the hand-off channel it sends into;
	// loaded at fire time, and atomic so a set racing a fire needs no ordering
	// argument. Nil — every test sink, and a daemon before main sets it — drops the
	// signal.
	crashLoop atomic.Pointer[func(sessionID string)]

	// echo is the queued-message placement every claude echo reaches, handed
	// over by the drain rather than by the parser so it runs after the drain has
	// handled every event claude emitted before the echo. Late-bound and atomic
	// for crashLoop's reasons; nil drops the echo, which the placement's idle
	// fallback covers.
	echo              atomic.Pointer[func(sessionID string, ev turnevent.UserEcho)]
	placementIdle     atomic.Pointer[func(string)]
	operatorPublisher atomic.Pointer[func(operatorMessage)]
	placementCommands chan func()
}

// setCrashLoopNotify installs the producer crashLoopForTag's closures call.
func (s *streamTurnSink) setCrashLoopNotify(fn func(sessionID string)) {
	s.crashLoop.Store(&fn)
}

// setEchoObserver installs the consumer the drain hands each UserEcho to.
func (s *streamTurnSink) setEchoObserver(fn func(sessionID string, ev turnevent.UserEcho)) {
	s.echo.Store(&fn)
}

// observeEcho hands one echo to the installed consumer, if any.
func (s *streamTurnSink) observeEcho(sessionID string, ev turnevent.UserEcho) {
	if fn := s.echo.Load(); fn != nil {
		(*fn)(sessionID, ev)
	}
}

// crashLoopForTag returns the streamsup.Config.OnCrashLoop hook for the runner
// whose live session id tag reads. It reads the tag at fire time for exitForTag's
// reason: RestartFresh rotates the id, and the conversation lookup downstream
// matches the live one. It does not block on its own; the installed producer owns
// that half of OnCrashLoop's contract.
func (s *streamTurnSink) crashLoopForTag(tag func() string) func() {
	return func() {
		if fn := s.crashLoop.Load(); fn != nil {
			(*fn)(tag())
		}
	}
}

// newStreamTurnSink constructs the fan-in. buf <= 0 falls back to
// streamTurnSinkBuf; logger backs only the content-free drop diagnostic, nil
// falling back to slog.Default.
//
// The reserve is clamped to buf/2 so a small test buffer stays workable: at
// buf == 1 it degenerates to 0 and every slot is droppable again, which is what
// keeps the buffer-of-1 drop fixtures (`TestStreamTurnSink_ExitRetainedWhenFull`)
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
		runtimeWake:           make(chan struct{}, 1),
		ch:                    make(chan streamTurnEnvelope, buf),
		placementCommands:     make(chan func(), operatorMessageQueueSize),
		stopped:               make(map[streamProducerKey]confirmedStreamStop),
		stoppedWake:           make(chan struct{}, 1),
		droppableCap:          buf - min(streamTurnSinkCloseReserve, buf/2),
		logger:                logger,
		lifecycleClosePending: make(map[string]struct{}),
		lifecycleCloseWake:    make(chan struct{}, 1),
	}
}

// offer preserves the bounded, non-blocking event policy while assigning FIFO
// positions under the same short lock as runnerStopped.
func (s *streamTurnSink) offer(env streamTurnEnvelope, closing bool) bool {
	s.offerMu.Lock()
	defer s.offerMu.Unlock()
	if !closing && len(s.ch) >= s.droppableCap {
		return false
	}
	env.queued = s.queued + 1
	if !env.exit {
		env.sourceEpoch = s.exits.Load()
	}
	select {
	case s.ch <- env:
		s.queued++
		if s.runtimeLastQueued == nil {
			s.runtimeLastQueued = make(map[streamProducerKey]uint64)
		}
		if !env.exit {
			s.runtimeLastQueued[streamProducerKey{env.sessionID, env.incarnation}] = env.queued
			if env.incarnation != 0 {
				if s.runtimeProducers == nil {
					s.runtimeProducers = make(map[string]uint64)
				}
				s.runtimeProducers[env.sessionID] = max(s.runtimeProducers[env.sessionID], env.incarnation)
			}
		}
		return true
	default:
		if env.exit {
			s.retainExitLocked(env)
		}
		return false
	}
}

// retainExitLocked requires offerMu and keeps the newest stamp per incarnation.
// A child exit may acquire the lock after a later confirmed stop; that older
// exit must never replace the stronger producer boundary.
func (s *streamTurnSink) retainExitLocked(env streamTurnEnvelope) {
	key := streamProducerKey{env.sessionID, env.incarnation}
	if old, ok := s.stopped[key]; ok && old.env.exitEpoch >= env.exitEpoch {
		return
	}
	s.stopped[key] = confirmedStreamStop{env: env, after: s.queued}
	select {
	case s.stoppedWake <- struct{}{}:
	default:
	}
}

// runnerStopped runs only after Runner.Run joins its producer. It retains
// one latest boundary per producer incarnation without relying on queue capacity, ordered
// behind every envelope queued before that join. Coalescing repeated stops for
// one incarnation moves the boundary later, after all of that producer's old tails.
// The wake is a level trigger, never the owner of the notification.
func (s *streamTurnSink) runnerStopped(sessionID string) {
	s.offerMu.Lock()
	env := streamTurnEnvelope{sessionID: sessionID, incarnation: s.runtimeProducers[sessionID], exit: true, exitEpoch: s.exits.Add(1), occurredAt: runtimeExitTime()}
	if s.live != nil {
		src := s.live.capture(sessionID, env.incarnation, history.SessionProvenance{SessionID: sessionID}, false)
		env.live = s.live.closeProducer(src)
	}
	s.retainExitLocked(env)
	s.offerMu.Unlock()
}

func (s *streamTurnSink) takeStopped(processed uint64) []streamTurnEnvelope {
	s.offerMu.Lock()
	defer s.offerMu.Unlock()
	var ready []streamTurnEnvelope
	for id, stop := range s.stopped {
		if stop.after <= processed {
			ready = append(ready, stop.env)
			delete(s.stopped, id)
		}
	}
	return ready
}

// requestLifecycleClose records a conversation whose published turn lifecycle
// must return to idle after pool teardown. It never blocks the pool transition
// observer and never drops: the buffered wake may coalesce, while the protected
// set remains until the drain consumes it.
func (s *streamTurnSink) requestLifecycleClose(conversationID string) {
	if conversationID == "" {
		return
	}
	s.lifecycleCloseMu.Lock()
	s.lifecycleClosePending[conversationID] = struct{}{}
	s.lifecycleCloseMu.Unlock()
	select {
	case s.lifecycleCloseWake <- struct{}{}:
	default:
	}
}

// takeLifecycleCloses atomically removes every pending pool-teardown close. Only
// the drain calls it, keeping emitter lifecycle mutation on that one goroutine.
func (s *streamTurnSink) takeLifecycleCloses() []string {
	s.lifecycleCloseMu.Lock()
	defer s.lifecycleCloseMu.Unlock()
	if len(s.lifecycleClosePending) == 0 {
		return nil
	}
	conversationIDs := make([]string, 0, len(s.lifecycleClosePending))
	for conversationID := range s.lifecycleClosePending {
		conversationIDs = append(conversationIDs, conversationID)
		delete(s.lifecycleClosePending, conversationID)
	}
	return conversationIDs
}

// streamSessionTag carries a runner's live routing ID across daemon rotations
// and announced clears. Event and exit readers use atomic snapshots. Once bound
// to a sink, tag writes take its short acceptance lock so boundaries capture the
// registered incarnation even before the new routing ID produces output. Empty
// IDs are refused because they cannot resolve to a conversation.
type streamSessionTag struct {
	runtimeSink    atomic.Pointer[streamTurnSink]
	incarnation    atomic.Uint64
	id             atomic.Pointer[string]
	lastSource     atomic.Pointer[history.SessionProvenance]
	retiringSource atomic.Pointer[string]
}

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
	s := t.runtimeSink.Load()
	if s != nil {
		s.offerMu.Lock()
		defer s.offerMu.Unlock()
	}
	if oldID := t.ID(); oldID != newID {
		t.retiringSource.CompareAndSwap(nil, &oldID)
	}
	t.id.Store(&newID)
	if s != nil {
		s.registerRuntimeProducerLocked(t, newID)
	}
}

// CompareAndSwap follows an announced clear only while the tag still holds
// oldID. A competing daemon rotation remains authoritative. Successful swaps
// register the routing alias without marking a retiring child. Empty new IDs
// are refused. Pointer comparison also refuses an equal-string replacement
// observed after the snapshot; every tag write allocates a fresh pointer.
func (t *streamSessionTag) CompareAndSwap(oldID, newID string) bool {
	if newID == "" {
		return false
	}
	s := t.runtimeSink.Load()
	if s != nil {
		s.offerMu.Lock()
		defer s.offerMu.Unlock()
	}
	p := t.id.Load()
	if *p != oldID {
		return false
	}
	if !t.id.CompareAndSwap(p, &newID) {
		return false
	}
	if s != nil {
		s.registerRuntimeProducerLocked(t, newID)
	}
	return true
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

// exitEpoch reports the fan-in's exit-lane position right now — the stamp the most
// recently offered exit envelope carried, or 0 before the first one. It is what
// runSupervisor binds into the turn-busy tracker (withExitEpoch), which reads it
// while holding its own mutex, so it must stay exactly what it is: one atomic load,
// no lock taken, no allocation, nothing that can block.
func (s *streamTurnSink) exitEpoch() uint64 { return s.exits.Load() }

// exitFor is exitForTag's frozen-tag form, standing to it exactly as sinkFor
// stands to sinkForTag and for the same reason.
func (s *streamTurnSink) exitFor(sessionID string) func() {
	return s.exitForTag(func() string { return sessionID })
}

// sinkForTag captures the daemon routing tag once per arriving event. A
// production factory supplies its fixed producer kind; omitted kind leaves
// provenance absent. Neither subprocess output nor the current conversation
// supplies these source facts.
//
// The callback never blocks the child's stdout forwarder. offer serializes its
// capacity check and send under a short leaf lock. Droppable events are refused
// at droppableCap, reserving the remaining capacity for TurnEnd. A closing event
// can still exhaust that reserve and is logged at Warn; other drops are Debug.
// Logs reuse the captured tag and include only content-free discriminants.
func (s *streamTurnSink) sinkForTag(tag func() string, kind ...string) func(turnevent.Event) {
	producerKind := ""
	if len(kind) > 0 {
		producerKind = kind[0]
	}
	return s.sinkForProducer(tag, producerKind, nil)
}

func (s *streamTurnSink) sinkForProducer(tag func() string, producerKind string, incarnation func() uint64) func(turnevent.Event) {
	return func(ev turnevent.Event) {
		if s.live != nil {
			s.offerMu.Lock()
		}
		sessionID := tag()
		env := streamTurnEnvelope{sessionID: sessionID, ev: ev}
		if incarnation != nil {
			env.incarnation = incarnation()
		}
		if producerKind != "" {
			env.source = history.SessionProvenance{Kind: producerKind, SessionID: sessionID}
		}
		if s.live != nil {
			src := s.live.capture(sessionID, env.incarnation, env.source, false)
			s.offerMu.Unlock()
			if s.liveCaptureReady != nil {
				s.liveCaptureReady()
			}
			env.live = s.live.acceptEvent(src, ev)
		}
		s.forwardEvent(env)
	}
}

// forwardEvent offers an already captured event without consulting its producer.
func (s *streamTurnSink) forwardEvent(env streamTurnEnvelope) {
	ev, sessionID := env.ev, env.sessionID
	if turnMarkFor(ev) == turnMarkClose {
		if !s.offer(env, true) {
			// A closer dropped beyond the reserve remains visible at the default
			// log level. Only its fixed variant and session ID are logged.
			s.logger.Warn("relay: stream-turn close drop; sink full",
				"event", "stream_turn.close_sink_full",
				"kind", eventKind(ev),
				"session_id", sessionID)
		}
		return
	}

	if s.offer(env, false) {
		return
	}

	// SECURITY: content-free — the discriminant and session id only, never
	// the event's assistant / thought / tool content.
	s.logger.Debug("relay: stream-turn drop; sink full",
		"event", "stream_turn.sink_full",
		"kind", eventKind(ev),
		"session_id", sessionID)
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
// Never waits for channel capacity. Exits use the closing reserve; if even that
// is full, offer retains the stamped boundary behind its queued predecessors.
// The content-free Warn keeps the existing event/session field contract while
// identifying this degraded transport path. No event content or queue position
// is logged.
func (s *streamTurnSink) exitForTag(tag func() string) func() {
	return func() {
		sessionID := tag()
		// Stamped BEFORE the send and never after (#1483). The guard's correctness
		// rests on an exit offered ahead of a mark carrying a stamp that mark's own
		// read cannot miss, and sync/atomic is sequentially consistent: a Load issued
		// after this Add returned observes at least this value. A DROPPED exit still
		// consumes a stamp, which only inflates later ones — and inflation moves the
		// guard toward declining, the safe direction (`clearForExit`).
		epoch := s.exits.Add(1)
		if !s.offer(streamTurnEnvelope{sessionID: sessionID, exit: true, exitEpoch: epoch}, true) {
			// SECURITY: content-free, and no "kind" — there is no event to name.
			// The resolved conversation id is absent because this closure holds no
			// resolver and structurally cannot name one; the conversation-id
			// discipline lives on the clear path (`clearForSession`).
			s.logger.Warn("relay: stream-turn exit retained; sink full",
				"event", "stream_turn.exit_sink_full",
				"session_id", sessionID)
		}
	}
}

// startStreamTurnDrainV2 spawns the single drain goroutine that feeds the
// interactiveTurnEmitterV2 from the fan-in sink. The Parser already emits
// turnevent.Event, so the events reach Handle as they are, with no mapping step.
//
// The goroutine selects over five cases:
//   - sink.stoppedWake: consume retained confirmed stops only after their queued
//     predecessors have been handled, using the same exit-epoch guards as exits.
//   - sink.ch: an exit envelope (#1209) clears the producing session's turn and
//     its matching published lifecycle; otherwise feed the per-conversation
//     turn-busy tracker, then resolve the producing session to the conversation
//     that owns it and hand the event to emitter.HandleFor under THAT
//     conversation's id (#2739). Every conversation's events reach its own
//     history, ring and clients whichever conversation is active; only an event
//     whose session resolves to no conversation is dropped, BEFORE HandleFor.
//   - sink.lifecycleCloseWake: a pool teardown recorded one or more conversation
//     lifecycle closes. The protected set, not the wake token, owns those requests,
//     so coalescing cannot drop one; the drain applies them on this goroutine before
//     handling a later child event.
//   - emitter.flushC(): the ~250ms coalescing timer fired — route it back into
//     flushAll on THIS goroutine. The emitter arms the timer inside Handle and
//     needs a driver to select it; the drain is that driver, so both Handle and
//     flushDelta run on the one goroutine (no cross-goroutine timer race).
//   - ctx.Done(): stop.
//
// Single-writer invariant: only this goroutine ever calls HandleFor / flushAll, so
// the emitter's unguarded lifecycle / coalescing fields stay race-free — the
// single-goroutine assumption interactiveTurnEmitterV2 documents. The returned
// cleanup blocks until the goroutine exits.
//
// The emitter is passed in (not built here) so the caller owns its construction
// and replay wiring. conversationFor resolves a producing session id to its
// conversation; production passes conversationForSession over the registry, the
// same resolution turnBusyTracker uses, so a tail from a just-rotated session
// (in SessionHistory) is attributed to its own conversation rather than dropped.
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
	conversationFor func(sessionID string) (conversationID string, ok bool),
	busy *turnBusyTracker,
	logger *slog.Logger,
) (cleanup func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		closePendingLifecycles := func() {
			if emitter.runtimeFacts {
				sink.takeLifecycleCloses() // runtime facts own source-specific cleanup
				return
			}
			// Bound trackers never enqueue early pool-transition closes. Their
			// confirmed producer stop is retained behind the parsed tail and closes it below.
			if busy != nil && busy.posts != nil {
				return
			}
			for _, conversationID := range sink.takeLifecycleCloses() {
				unlock := busy.lockPostBoundary()
				emitter.closeForConversation(ctx, conversationID)
				busy.publishPostBoundary(conversationID, false)
				unlock()
			}
		}
		var processed uint64
		handleEnvelope := func(env streamTurnEnvelope) {
			emitter.liveCapture = env.live
			defer func() { emitter.liveCapture = nil }()
			resolve := conversationFor
			if env.live != nil {
				resolve = func(string) (string, bool) {
					return env.live.source.ConversationID, env.live.source.ConversationID != ""
				}
			}
			unlock := busy.lockPostBoundary()
			defer unlock()
			if env.exit {
				if emitter.runtimeFacts {
					if sink.noteRuntimeStop(env) {
						return
					}
					id, ok := resolve(env.sessionID)
					if ok && id != "" {
						sourceID := env.source.SessionID
						if sourceID == "" {
							sourceID = env.sessionID
						}
						at := env.occurredAt
						if at.IsZero() {
							at = runtimeExitTime()
						}
						emitter.closeRuntimeSource(ctx, id, sourceID, "child_exit", at, false, env.exitEpoch, env.incarnation)
						if !emitter.hasRuntimeTurn(id) {
							if _, cleared := busy.clearForExit(env.sessionID, env.exitEpoch); !cleared && busy != nil {
								return
							}
							sink.observePlacementIdle(env.sessionID)
							busy.publishPostBoundary(id, false)
							if busy != nil && busy.posts != nil {
								busy.posts.teardown.Delete(id)
							}
						}
					}
					return
				}
				var teardownEpoch any
				if busy != nil && busy.posts != nil {
					if id, ok := resolve(env.sessionID); ok {
						teardownEpoch, _ = busy.posts.teardown.Load(id)
						if teardownEpoch != nil && env.exitEpoch <= teardownEpoch.(uint64) {
							return // exit offered before this eviction, even if TurnEnd cleared busy
						}
					}
				}
				// Retain exit-epoch protection; flush and close publication before
				// releasing posts, only when the producing exit was accepted.
				if conversationID, cleared := busy.clearForExit(env.sessionID, env.exitEpoch); cleared {
					sink.observePlacementIdle(env.sessionID)
					emitter.closeForConversation(ctx, conversationID)
					busy.publishPostBoundary(conversationID, false)
					if teardownEpoch != nil {
						// A new early transition can arrive during publication;
						// retire only the hold this exit actually accepted.
						busy.posts.teardown.CompareAndDelete(conversationID, teardownEpoch)
					}
				}
				return
			}
			if emitter.runtimeFacts {
				if id, ok := resolve(env.sessionID); ok && emitter.runtimeSealed[runtimeSourceKey(id, env.source.SessionID, env.incarnation)] {
					if echo, ok := env.ev.(turnevent.UserEcho); ok {
						sink.observeEcho(env.sessionID, echo)
					} else {
						emitter.handleForSource(ctx, id, env.ev, env.source, env.incarnation)
					}
					return // sealed predecessors cannot change successor busy/placement state
				}
			}

			// BEFORE the resolution below, and the ordering IS the contract: an
			// event that resolves to no conversation is dropped there, and the
			// tracker keeps its own resolution and its own unbound-session record.
			if turnMarkFor(env.ev) == turnMarkClose {
				sink.observePlacementIdle(env.sessionID)
			}
			busy.observe(env.sessionID, env.ev)

			// claude's echo of a user message (#2730) builds no frame. It goes to
			// queued-message placement HERE, on this goroutine and before the
			// conversation resolution: every event claude emitted ahead of it —
			// the tool result the message followed — has already been handled,
			// so the operator-message push it may commit lands after it, and a
			// background conversation's echo is placed too.
			if echo, ok := env.ev.(turnevent.UserEcho); ok {
				sink.observeEcho(env.sessionID, echo)
				return
			}

			// Attribution by the event's OWN session (#2739), never by the active
			// conversation: the emitter is the only writer of history, ring, client
			// frames and the turn-end wake, so an event dropped here is lost for
			// good.
			conversationID, ok := resolve(env.sessionID)
			if !ok || conversationID == "" {
				// SECURITY: content-free — discriminant + session id only.
				logger.Debug("relay: stream-turn drop; no conversation for session",
					"event", "stream_turn.no_conversation",
					"kind", eventKind(env.ev),
					"session_id", env.sessionID)
				return
			}
			if turnMarkFor(env.ev) == turnMarkOpen {
				busy.publishPostBoundary(conversationID, true)
			}
			emitter.handleForSource(ctx, conversationID, env.ev, env.source, env.incarnation)
			if emitter.runtimeFacts {
				emitter.runtimeEpoch = env.sourceEpoch
			}
			if turnMarkFor(env.ev) == turnMarkClose {
				busy.publishPostBoundary(conversationID, false)
			}
		}
		handleStops := func() {
			for _, env := range sink.takeStopped(processed) {
				handleEnvelope(env)
			}
		}
		for {
			sink.publishRuntimeBoundaries(ctx, emitter, busy, processed)
			handleStops()
			closePendingLifecycles()
			select {
			case <-ctx.Done():
				return
			case <-sink.stoppedWake:
				handleStops()
			case <-sink.lifecycleCloseWake:
				closePendingLifecycles()
			case <-sink.runtimeWake:
				sink.publishRuntimeBoundaries(ctx, emitter, busy, processed)
			case commit := <-sink.placementCommands:
				commit()
			case env := <-sink.ch:
				if busy == nil || busy.posts == nil {
					closePendingLifecycles()
				}
				handleStops()
				handleEnvelope(env)
				if env.queued != 0 {
					processed = env.queued
					sink.shadowProcessed.Store(processed)
				}
			case <-emitter.flushC():
				closePendingLifecycles()
				emitter.flushAll(ctx)
			}
		}
	}()
	return func() { <-done }
}
