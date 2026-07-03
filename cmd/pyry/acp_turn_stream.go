package main

import (
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/acpbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// sessionUpdateParams is the ACP session/update notification params wrapper. It
// belongs to this consumer, not to acpbridge's pure mapper (acpbridge/outbound.go
// doc): SessionID addresses the session and Update is the MapUpdate payload —
// the sessionUpdate variant discriminant rides INSIDE Update as its own field,
// so there is no separate discriminant here.
type sessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

// acpTurnStream is the outbound streaming adapter: the turnbridge OnEvent sink
// for one ACP session. It turns each neutral turnevent.Event into a JSON-RPC
// session/update notification on the ACP transport (mapping via
// acpbridge.MapUpdate), signals turn-end out-of-band to the session/prompt
// owner (#751), and drops Stall to stderr (ADR 027 divergences 1 and 3).
//
// It is stateless — no per-event state, no goroutine, no clock read. turnbridge
// invokes Handle serially on its single Run goroutine, so the adapter needs no
// synchronisation. It is the ACP analogue of interactiveTurnEmitterV2 but far
// thinner: ACP's host owns the spinner and turn pacing, so none of the mobile
// emitter's lifecycle / coalescing / capability-fan-out machinery applies.
//
// SECURITY: application content — assistant text, thought text, tool
// titles/inputs/results — is NEVER logged at any level. Logs carry only the
// content-free variant discriminant (eventKind), the session id, and, on a
// write failure, the transport error sentinel — never params or model output.
type acpTurnStream struct {
	transport *acp.Transport
	sessionID string
	onTurnEnd func(reason string) // T7 (#751) seam; nil-tolerant
	logger    *slog.Logger
}

// newACPTurnStream constructs the adapter emitting for sessionID over transport.
// onTurnEnd is the #751 seam invoked on TurnEnd with the ACP stopReason; it is
// nil-tolerant so the adapter can be built for pure emit tests. logger is
// required (the caller passes the ACP subcommand's stderr logger).
func newACPTurnStream(transport *acp.Transport, sessionID string, onTurnEnd func(string), logger *slog.Logger) *acpTurnStream {
	return &acpTurnStream{
		transport: transport,
		sessionID: sessionID,
		onTurnEnd: onTurnEnd,
		logger:    logger,
	}
}

// Handle is the turnbridge OnEvent sink: one event maps to at most one
// session/update notification. TurnEnd and Stall are matched explicitly BEFORE
// MapUpdate, because MapUpdate collapses both to ok==false and the adapter must
// distinguish them (a turn-end signal vs a stderr drop). MapUpdate is therefore
// only ever reached for the four emit-able variants, where ok is always true;
// the !ok guard below is defensive against a future sealed-Event variant.
//
// Not safe for concurrent use — designed for the producer's single Run goroutine.
func (a *acpTurnStream) Handle(ev turnevent.Event) {
	switch e := ev.(type) {
	case turnevent.TurnEnd:
		// Divergence 1: end-of-turn is the session/prompt stopReason RETURN, not a
		// session/update. The ACP stopReason IS string(Reason) by identity
		// (turnevent.TurnEndReason values are the ACP strings). Signal #751 to
		// resolve the held call; emit nothing here.
		if a.onTurnEnd != nil {
			a.onTurnEnd(string(e.Reason))
			return
		}
		a.logger.Debug("acp: turn-end with no resolver",
			"event", "acp_turn.turn_end_no_resolver",
			"session_id", a.sessionID)
		return
	case turnevent.Stall:
		// Divergence 3: ACP has no home for a stall; surface it on stderr, never on
		// the notification stream. Content-free — onset marker only.
		a.logger.Warn("acp: session stalled",
			"event", "acp_turn.stall",
			"session_id", a.sessionID)
		return
	}

	// The msgID return is intentionally discarded: ACP's agent_message_chunk
	// carries content only (no per-message wire delimiter), so chunks sharing a
	// message id stream as separate agent_message_chunk notifications in arrival
	// order and the host concatenates them. The adapter is stateless w.r.t.
	// MessageID and does not coalesce (contrast the mobile emitter's
	// MessageID-keyed delta coalescing, #609, which ACP neither needs nor supports).
	update, _, ok := acpbridge.MapUpdate(ev)
	if !ok {
		// Defensive: unreachable for the four emit-able variants (TurnEnd/Stall are
		// handled above). A future sealed-Event variant surfaces as a visible drop
		// rather than silently vanishing.
		a.logger.Debug("acp: no session/update mapping; dropped",
			"event", "acp_turn.unmapped",
			"kind", eventKind(ev))
		return
	}
	if err := a.transport.Notify(acpbridge.MethodSessionUpdate, sessionUpdateParams{
		SessionID: a.sessionID,
		Update:    update,
	}); err != nil {
		// Content-free: never log params or model output. The turn is ending anyway
		// and the transport surfaces stream breaks on its own read side.
		a.logger.Debug("acp: session/update notify failed",
			"event", "acp_turn.notify_err",
			"session_id", a.sessionID,
			"kind", eventKind(ev),
			"err", err.Error())
	}
}
