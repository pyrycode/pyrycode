package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Static error messages for request_snapshot replies. Deliberately generic:
// the wire reply NEVER echoes the JSON decode error or the (attacker-
// controlled) raw conversation_id, only one of these constants.
const (
	msgSnapshotConvNotFound = "unknown or foreign conversation_id"
	msgSnapshotOffline      = "no live claude session"
)

// handleRequestSnapshot answers an inbound request_snapshot with a
// deterministic error reply addressed to s. It is the inbound-control handler for
// TypeRequestSnapshot — intercepted in dispatchAppFrame before dispatch.Route,
// exactly like handleRekeyRequest — and runs on the manager's single Run dispatch
// goroutine. Every branch pushes exactly one reply and returns: it never panics,
// hangs, or silently drops the request.
//
// The daemon renders no screen (#2540): an unknown or foreign conversation_id
// answers conversation.not_found (not retryable), and a known one answers
// server.binary_offline (retryable). The membership gate runs FIRST, so an
// unknown id is never reported as merely offline. Clients read the run
// configuration from request_session_settings instead.
//
// SECURITY: error replies carry only a static message constant; the
// conversation_id and any decode error are never echoed or logged.
func (m *V2SessionManager) handleRequestSnapshot(ctx context.Context, s *V2Session, env protocol.Envelope) {
	var payload protocol.RequestSnapshotPayload
	// A decode failure is tolerated: it leaves ConversationID == "", which the
	// KnownConversation check below rejects as not-found. The decode error is
	// never echoed back to the phone.
	_ = json.Unmarshal(env.Payload, &payload)

	// Reject an unknown/foreign conversation_id first. A nil KnownConversation
	// (optional seam) rejects everything as not-found.
	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(payload.ConversationID) {
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeConversationNotFound, msgSnapshotConvNotFound, false)
		return
	}
	m.snapshotReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSnapshotOffline, true)
}

// snapshotReplyError pushes a single TypeError reply to s, correlated to
// inReplyTo, via the m.forwardEnvelope seal-and-forward path (no parallel send
// path). message MUST be a static constant — never attacker-controlled bytes.
func (m *V2SessionManager) snapshotReplyError(ctx context.Context, s *V2Session, inReplyTo uint64, code, message string, retryable bool) {
	errPayload, err := json.Marshal(protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 snapshot error reply marshal failed",
			"event", "v2.snapshot.err_marshal",
			"conn_id", s.connID,
			"code", code)
		return
	}
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	if m.dropInlineReplyIfDown(s, "v2.snapshot.err_dropped_transport_down") {
		return
	}
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 snapshot error reply push dropped",
			"event", "v2.snapshot.err_push",
			"conn_id", s.connID,
			"code", code,
			"err", err)
	}
}

// dropInlineReplyIfDown drops an undeliverable inline reply before sealing.
// Run owns the send cipher; callers must probe before consuming its next nonce.
// The event slug, connection ID and reason are the only logged fields.
func (m *V2SessionManager) dropInlineReplyIfDown(s *V2Session, event string) bool {
	if !m.transportDown() {
		return false
	}
	m.cfg.Logger.Debug("relay: v2 inline reply dropped; transport down",
		"event", event,
		"conn_id", s.connID,
		"reason", "transport_down")
	return true
}
