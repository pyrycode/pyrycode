package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// sessionTransitionQueueSize bounds the buffered hand-off between the pool's
// off-lock transition signal (Enqueue) and the emitter's Run goroutine.
// Transitions are rare and human-paced (a /clear rotation or an idle/cap
// eviction); 16 absorbs a burst, and drop-on-full bounds memory while never
// wedging the goroutines that drive a transition — the pool's lifecycle goroutine
// on an eviction, the runner's parse goroutine or a control-plane goroutine on a
// rotation (#659's MUST-NOT-BLOCK rule).
const sessionTransitionQueueSize = 16

// transitionObserverSink is the narrow *sessions.Pool surface
// startSessionTransitionStreamV2 needs: install the transition observer (which
// must happen before Pool.Run). *sessions.Pool satisfies it. Declared at the
// consumer (CODING-STYLE) so relay.go threads the value through without
// importing internal/sessions.
type transitionObserverSink interface {
	SetTransitionObserver(sessions.TransitionObserver)
}

type switchTransitionPublisherSink interface {
	SetSwitchTransitionPublisher(func(sessions.SessionTransition))
}

type switchTransitionPublication struct {
	transition sessions.SessionTransition
	done       chan struct{}
}

// sessionTransitionEmitterV2 fans legacy boundaries to open interactive conns.
// A bounded queue decouples ordinary off-lock pool callbacks from fanout; a
// dedicated publication lane retains committed switches through sealing.
//
// SECURITY: emitter logs carry only event/reason/conn_id discriminants and Push's
// transport sentinel. Payloads, ownership and agent/session facts are never
// logged. Ownership comes from captured facts or the exact-session registry
// resolver; an unresolved event is dropped without guessing a routing key.
type sessionTransitionEmitterV2 struct {
	bcast  interactiveBroadcaster
	logger *slog.Logger

	// resolveConv supplies ownership only when the transition has none captured.
	resolveConv func(string) (string, bool)
	// resolveAgent is an exact-session lookup used only at handoff, never drain.
	resolveAgent func(string) (string, bool)

	// hist is the durable conversation log (#2114). This producer deliberately
	// skips the #647 replay ring, so the log is the ONLY place a session
	// boundary is retained — it is what makes a /clear routed through pyry
	// visible in served history. nil means no durable log and broadcast behaves
	// exactly as it did before this field existed. Concrete pointer for the same
	// reason busy is one, one guard down in startSessionTransitionStreamV2: a
	// typed-nil inside an interface is non-nil at the interface level.
	hist *history.Store

	// switched publishes the committed row after the transition fanout.
	switched func(string)

	in chan sessions.SessionTransition
	// switches is a reliable, unbuffered lane. Each sending daemon worker holds
	// its conversation's reset exclusion until publication and sealing finish.
	switches chan switchTransitionPublication

	// nextID is the per-conn envelope-ID counter (mirrors assistantTurnEmitterV2).
	// Read/written only on the single Run goroutine (broadcast is serial) — no
	// atomic needed. EventID is left nil: this producer does not append to the
	// #647 replay ring (no replay AC).
	nextID uint64
}

// newSessionTransitionEmitterV2 constructs an emitter wired to bcast and the
// session→conversation resolveConv closure (#741). Run must be called once on a
// goroutine before Enqueue takes effect.
func newSessionTransitionEmitterV2(bcast interactiveBroadcaster, resolveConv func(string) (string, bool), logger *slog.Logger) *sessionTransitionEmitterV2 {
	return &sessionTransitionEmitterV2{
		bcast:       bcast,
		logger:      logger,
		resolveConv: resolveConv,
		in:          make(chan sessions.SessionTransition, sessionTransitionQueueSize),
		switches:    make(chan switchTransitionPublication),
	}
}

// Enqueue is the sessions.TransitionObserver callback — the #659-mandated "hand
// the signal off to a buffered channel and return." Invoked synchronously from
// whichever goroutine drove the transition (see TransitionObserver's doc for the
// list) with no lock held; a non-blocking
// buffered send (drop-on-full) keeps that goroutine moving so a wedged fan-out
// can never stall the pool.
func (e *sessionTransitionEmitterV2) Enqueue(t sessions.SessionTransition) {
	if t.AgentSwitch {
		return // the dedicated publisher owns the single switch outcome
	}
	t = e.capture(t)
	select {
	case e.in <- t:
	default:
		e.logger.Warn("relay: session-transition queue full; dropping signal",
			"event", "session_transition.queue_full",
			"reason", string(t.Reason))
	}
}

// publishSwitch waits off Run through consumer delay and queue pressure. The
// daemon context, never a requesting connection, owns both waits.
func (e *sessionTransitionEmitterV2) publishSwitch(ctx context.Context, t sessions.SessionTransition) {
	req := switchTransitionPublication{transition: e.capture(t), done: make(chan struct{})}
	select {
	case e.switches <- req:
	case <-ctx.Done():
		return
	}
	select {
	case <-req.done:
	case <-ctx.Done():
	}
}

// capture retains exact-session facts before either publication lane can delay.
// A nonempty captured fact is authoritative, including an unsupported agent:
// unavailable provenance must not be replaced with a later binding or default.
func (e *sessionTransitionEmitterV2) capture(t sessions.SessionTransition) sessions.SessionTransition {
	p, ok := toWirePayload(t)
	if !ok || p.NewSessionID == "" {
		return t
	}
	if t.ConversationID == "" && e.resolveConv != nil {
		if id, found := e.resolveConv(p.NewSessionID); found {
			t.ConversationID = id
		}
	}
	agent := &t.NextAgent
	if t.Reason == sessions.ReasonEviction {
		agent = &t.PreviousAgent
	}
	if *agent == "" && e.resolveAgent != nil {
		if kind, found := e.resolveAgent(p.NewSessionID); found {
			*agent = kind
		}
	}
	return t
}

// Run drains the queue until ctx is cancelled or in is closed. Mirrors
// assistantTurnEmitterV2.Run.
func (e *sessionTransitionEmitterV2) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-e.switches:
			e.broadcast(ctx, req.transition)
			// Push only enqueues. Wait until the manager has applied its agent
			// gate/tag and sealed all preceding status/transition/row frames.
			if b, ok := e.bcast.(interface{ FlushPushes(context.Context) error }); ok {
				if err := b.FlushPushes(ctx); err != nil {
					close(req.done)
					return // daemon cancellation
				}
			}
			close(req.done)
		case t, ok := <-e.in:
			if !ok {
				return
			}
			e.broadcast(ctx, t)
		}
	}
}

// broadcast appends and fans out a legacy boundary using captured ownership and
// agent facts. Unknown reasons and unresolved ownership drop; individual Push
// failures do not abort other recipients unless the daemon context is cancelled.
func (e *sessionTransitionEmitterV2) broadcast(ctx context.Context, t sessions.SessionTransition) {
	payload, ok := toWirePayload(t)
	if !ok {
		e.logger.Debug("relay: session-transition drop; unknown reason",
			"event", "session_transition.unknown_reason",
			"reason", string(t.Reason))
		return
	}
	convID := payload.ConversationID
	if convID == "" && e.resolveConv != nil {
		if id, found := e.resolveConv(payload.NewSessionID); found {
			convID = id
		}
	}
	if convID == "" {
		e.logger.Debug("relay: session-transition drop; unresolvable conversation",
			"event", "session_transition.unresolved_conversation",
			"reason", payload.Reason)
		return
	}
	payload.ConversationID = convID

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		// SessionTransitionPayload is a closed struct of strings/time/*string and
		// cannot fail to marshal in practice. Defensive — never echo the payload
		// or err.Error().
		e.logger.Debug("relay: session-transition drop; payload marshal",
			"event", "session_transition.marshal_err",
			"reason", payload.Reason)
		return
	}

	// ONE timestamp per logical transition, hoisted out of the per-conn loop and
	// shared by the durable log entry and every envelope (#2114). It was minted
	// inside the loop until then, which gave N conns N timestamps for one
	// transition and left no single value a log entry could carry. UTC matches
	// what the interactive chokepoint stamps, so entries from the two producers
	// are orderable by the field the log stores.
	ts := time.Now().UTC()
	// Appended once, after the drops above: an unknown reason and an
	// unresolvable conversation both return before this point, so the log
	// records what was fanned out and never what was refused. This producer
	// skips the #647 replay ring, so the log is the ONLY place a session
	// boundary is retained.
	historyEntryID := appendConversationHistory(e.hist, e.logger, "session_transition.history_append_err",
		convID, protocol.TypeSessionTransition, payloadJSON, ts, transitionProvenance(t))

	// Fresh snapshot per transition: a phone that opened its session since the
	// last event is included here; one that dropped is absent, or surfaces as a
	// Push error below.
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate — non-interactive conns never see the structured stream
		}
		e.nextID++
		env := protocol.Envelope{
			ID:             e.nextID,
			Type:           protocol.TypeSessionTransition,
			TS:             ts,
			Payload:        payloadJSON,
			HistoryEntryID: historyEntryID,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: session-transition push dropped",
				"event", "session_transition.push_err",
				"conn_id", c.ConnID,
				"reason", payload.Reason,
				"err", err)
		}
	}
	if t.AgentSwitch && e.switched != nil {
		e.switched(convID)
	}
}

func transitionProvenance(t sessions.SessionTransition) history.SessionProvenance {
	p, ok := toWirePayload(t)
	kind := t.NextAgent
	if t.Reason == sessions.ReasonEviction {
		kind = t.PreviousAgent
	}
	if !ok || p.NewSessionID == "" || (kind != "claude" && kind != "codex") {
		return history.SessionProvenance{}
	}
	return history.SessionProvenance{Kind: kind, SessionID: p.NewSessionID}
}

// toWirePayload maps a #659 session-side transition onto the protocol wire
// payload. It is the pure, unit-testable seam: internal/sessions must not import
// internal/protocol (import cycle), so the TransitionReason → wire reason mapping
// lives here, cmd-side.
//
// Eviction (idle or cap — collapsed by #659) maps to the wire "idle_evict" and
// has no successor id (t.NewID == ""); per mobile #336 the evicted id is mirrored
// onto BOTH wire id fields (never an empty new_session_id). An unknown reason
// returns ok=false so the caller drops it rather than emit a malformed envelope.
// WorkspaceCwd is always nil (literal JSON null) — workspace_change has no
// server-side source and is out of scope for this producer (#657).
func toWirePayload(t sessions.SessionTransition) (protocol.SessionTransitionPayload, bool) {
	switch t.Reason {
	case sessions.ReasonClear:
		return protocol.SessionTransitionPayload{
			ConversationID:    t.ConversationID,
			PreviousSessionID: string(t.PreviousID),
			NewSessionID:      string(t.NewID),
			Reason:            "clear",
			OccurredAt:        t.OccurredAt,
			WorkspaceCwd:      nil,
		}, true
	case sessions.ReasonEviction:
		return protocol.SessionTransitionPayload{
			ConversationID:    t.ConversationID,
			PreviousSessionID: string(t.PreviousID),
			NewSessionID:      string(t.PreviousID), // no successor; mirror the evicted id (#336)
			Reason:            "idle_evict",
			OccurredAt:        t.OccurredAt,
			WorkspaceCwd:      nil,
		}, true
	default:
		return protocol.SessionTransitionPayload{}, false
	}
}

// transitionClearsTurn reports whether t tears a session down under its
// conversation and, if so, the session id that is that conversation's LIVE
// binding at observer time — the key #1202's turn-busy clear resolves on.
//
// It delegates to toWirePayload so this file carries ONE closed reason switch
// rather than two: an unknown or future reason returns ok=false and clears
// nothing, inheriting the whitelist drop toWirePayload already forces on the wire
// path. Today the two questions ("does this reach the wire" / "does this close a
// turn") have the same answer for every reason the pool produces; a future reason
// that needs one without the other is a SPLIT of this switch, not a special case
// inside it.
//
// NewSessionID rather than PreviousID is deliberate. It is the conversation's
// CurrentSessionID for both reasons (the rotated id post-#739-rebind for clear;
// the mirrored evicted id for the binding-neutral eviction), which is
// conversationForSession's PRIMARY match. PreviousID would also
// resolve today — via RebindSession's SessionHistory append — but only for as
// long as notifyTransition keeps driving the rebind ahead of the observer fan-out.
func transitionClearsTurn(t sessions.SessionTransition) (sessionID string, ok bool) {
	p, ok := toWirePayload(t)
	if !ok {
		return "", false
	}
	return p.NewSessionID, true
}

// startSessionTransitionStreamV2 installs the pool's transition observer and
// starts the emitter's Run goroutine. Returns a cleanup that waits for Run to
// exit on ctx-cancel. Mirrors startAssistantTurnBridgeV2.
//
// The observer slot is single-valued (SetTransitionObserver is a plain
// assignment), so the two consumers are COMPOSED here rather than each installing
// their own: the wire emitter, and #1202's turn-busy clear.
//
// SetTransitionObserver MUST run before Pool.Run; the call site (startRelayV2 ←
// startRelay at `runSupervisor`) is strictly before pool.Run (`runSupervisor`), so the
// observer field is installed once and read-only thereafter (#659's
// install-before-Run contract, race-free).
//
// Cleanup does NOT close `in` and does NOT clear the observer. The observer is
// read-only after Pool.Run (cannot be cleared); a late Enqueue racing teardown is
// panic-safe precisely because `in` is never closed — a non-blocking send to an
// open-but-full channel just drops. Same rationale as startAssistantTurnBridgeV2:
// rely on ctx cancellation to drain Run.
func startSessionTransitionStreamV2(
	ctx context.Context,
	sink transitionObserverSink,
	bcast interactiveBroadcaster,
	resolveConv func(string) (string, bool),
	busy *turnBusyTracker,
	hist *history.Store,
	logger *slog.Logger,
	switched ...func(string),
) func() {
	return startSessionTransitionStreamV2WithHarness(ctx, sink, bcast, resolveConv, nil, busy, hist, logger, switched...)
}

// startSessionTransitionStreamV2WithHarness adds exact-session capture while
// retaining the original wrapper for callers without an agent lookup.
func startSessionTransitionStreamV2WithHarness(
	ctx context.Context,
	sink transitionObserverSink,
	bcast interactiveBroadcaster,
	resolveConv func(string) (string, bool),
	resolveAgent func(string) (string, bool),
	busy *turnBusyTracker,
	hist *history.Store,
	logger *slog.Logger,
	switched ...func(string),
) func() {
	emitter := newSessionTransitionEmitterV2(bcast, resolveConv, logger)
	emitter.resolveAgent = resolveAgent
	emitter.hist = hist
	if len(switched) > 0 {
		emitter.switched = switched[0]
	}
	if publisher, ok := sink.(switchTransitionPublisherSink); ok {
		publisher.SetSwitchTransitionPublisher(func(t sessions.SessionTransition) {
			emitter.publishSwitch(ctx, t)
		})
	}
	sink.SetTransitionObserver(func(t sessions.SessionTransition) {
		// Capture and enqueue before the turn-busy clear. Queue pressure never
		// waits on fanout, and committed switches use the dedicated publisher.
		emitter.Enqueue(t)
		if sid, ok := transitionClearsTurn(t); ok {
			// busy may be nil: the tracker is constructed only on the stream path
			// (relay.go) while this install is unconditional, so PTY mode reaches here
			// with nothing. clearForSession is a nil-receiver no-op. Concrete pointer,
			// never an interface — a typed-nil inside an interface is non-nil at the
			// interface level and would route straight past that guard.
			if busy != nil && busy.posts != nil && t.Reason == sessions.ReasonEviction {
				busy.holdForTeardown(sid)
			} else {
				busy.clearForSession(sid)
			}
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		emitter.Run(ctx)
	}()

	var cleanedUp bool
	return func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		<-done
	}
}
