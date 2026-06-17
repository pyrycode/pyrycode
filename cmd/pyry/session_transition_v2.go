package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// sessionTransitionQueueSize bounds the buffered hand-off between the pool's
// off-lock transition signal (Enqueue) and the emitter's Run goroutine.
// Transitions are rare and human-paced (a /clear rotation or an idle/cap
// eviction); 16 absorbs a burst, and drop-on-full bounds memory while never
// wedging the pool's lifecycle/watcher goroutines (#659's MUST-NOT-BLOCK rule).
const sessionTransitionQueueSize = 16

// transitionObserverSink is the narrow *sessions.Pool surface
// startSessionTransitionStreamV2 needs: install the transition observer (which
// must happen before Pool.Run). *sessions.Pool satisfies it. Declared at the
// consumer (CODING-STYLE) so relay.go threads the value through without
// importing internal/sessions.
type transitionObserverSink interface {
	SetTransitionObserver(sessions.TransitionObserver)
}

// sessionTransitionEmitterV2 fans a session_transition v2 envelope to every open
// INTERACTIVE conn when a session transitions (#659's /clear rotation or idle/cap
// eviction). It mirrors assistantTurnEmitterV2: a buffered `in` channel decouples
// the pool's off-lock observer callback (Enqueue, non-blocking per #659's
// MUST-NOT-BLOCK contract) from the Run goroutine that performs the blocking
// ActiveConns snapshot and the per-conn Push. The interactive-only capability
// filter is the delivery gate — a phone that never negotiated interactive
// receives only the coarse v1 fan-out, never this event.
//
// SECURITY: the only fields logged at any level are content-free discriminants —
// `event`, `reason`, `conn_id`, and Push's transport-sentinel `err`. The
// marshaled payload is NEVER logged (no payloadJSON, no err.Error() on the
// marshal path). The payload carries no application content (session ids + reason
// + timestamp only), so there is nothing sensitive to leak — but the discipline
// is kept verbatim so a future field addition can't quietly start leaking through
// a log line. Session ids are non-secret routing identifiers (session_id is
// already a standard log field across internal/sessions).
type sessionTransitionEmitterV2 struct {
	bcast  interactiveBroadcaster
	logger *slog.Logger

	in chan sessions.SessionTransition

	// nextID is the per-conn envelope-ID counter (mirrors assistantTurnEmitterV2).
	// Read/written only on the single Run goroutine (broadcast is serial) — no
	// atomic needed. EventID is left nil: this producer does not append to the
	// #647 replay ring (no replay AC).
	nextID uint64
}

// newSessionTransitionEmitterV2 constructs an emitter wired to bcast. Run must be
// called once on a goroutine before Enqueue takes effect.
func newSessionTransitionEmitterV2(bcast interactiveBroadcaster, logger *slog.Logger) *sessionTransitionEmitterV2 {
	return &sessionTransitionEmitterV2{
		bcast:  bcast,
		logger: logger,
		in:     make(chan sessions.SessionTransition, sessionTransitionQueueSize),
	}
}

// Enqueue is the sessions.TransitionObserver callback — the #659-mandated "hand
// the signal off to a buffered channel and return." Invoked synchronously from
// the pool's lifecycle/watcher goroutine with no lock held; a non-blocking
// buffered send (drop-on-full) keeps that goroutine moving so a wedged fan-out
// can never stall the pool.
func (e *sessionTransitionEmitterV2) Enqueue(t sessions.SessionTransition) {
	select {
	case e.in <- t:
	default:
		e.logger.Warn("relay: session-transition queue full; dropping signal",
			"event", "session_transition.queue_full",
			"reason", string(t.Reason))
	}
}

// Run drains the queue until ctx is cancelled or in is closed. Mirrors
// assistantTurnEmitterV2.Run.
func (e *sessionTransitionEmitterV2) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-e.in:
			if !ok {
				return
			}
			e.broadcast(ctx, t)
		}
	}
}

// broadcast maps one transition to a wire payload, marshals it once, then fans a
// session_transition envelope to every currently-open INTERACTIVE conn. An
// unknown reason is dropped (toWirePayload ok=false) so a future #659 reason can
// never emit a malformed envelope. A per-conn Push error is logged at DEBUG and
// the loop continues — a dropped conn must not abort the others (AC#2);
// ctx-cancel mid-fan-out returns early.
func (e *sessionTransitionEmitterV2) broadcast(ctx context.Context, t sessions.SessionTransition) {
	payload, ok := toWirePayload(t)
	if !ok {
		e.logger.Debug("relay: session-transition drop; unknown reason",
			"event", "session_transition.unknown_reason",
			"reason", string(t.Reason))
		return
	}
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

	// Fresh snapshot per transition: a phone that opened its session since the
	// last event is included here; one that dropped is absent, or surfaces as a
	// Push error below.
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate — non-interactive conns never see the structured stream
		}
		e.nextID++
		env := protocol.Envelope{
			ID:      e.nextID,
			Type:    protocol.TypeSessionTransition,
			TS:      time.Now().UTC(),
			Payload: payloadJSON,
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
			PreviousSessionID: string(t.PreviousID),
			NewSessionID:      string(t.NewID),
			Reason:            "clear",
			OccurredAt:        t.OccurredAt,
			WorkspaceCwd:      nil,
		}, true
	case sessions.ReasonEviction:
		return protocol.SessionTransitionPayload{
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

// startSessionTransitionStreamV2 installs the emitter as the pool's transition
// observer and starts its Run goroutine. Returns a cleanup that waits for Run to
// exit on ctx-cancel. Mirrors startAssistantTurnBridgeV2.
//
// SetTransitionObserver MUST run before Pool.Run; the call site (startRelayV2 ←
// startRelay at main.go:489) is strictly before pool.Run (main.go:514), so the
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
	logger *slog.Logger,
) func() {
	emitter := newSessionTransitionEmitterV2(bcast, logger)
	sink.SetTransitionObserver(emitter.Enqueue)

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
