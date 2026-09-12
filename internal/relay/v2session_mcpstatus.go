package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	msgMCPStatusMalformed           = "malformed MCP status request"
	msgMCPStatusConversationMissing = "conversation not found"
	msgMCPStatusUnavailable         = "MCP status is unavailable"
)

var (
	rejectMCPStatusMalformed = attachmentReject{
		code: protocol.CodeProtocolMalformed, message: msgMCPStatusMalformed, retryable: false,
	}
	rejectMCPStatusConversationMissing = attachmentReject{
		code: protocol.CodeConversationNotFound, message: msgMCPStatusConversationMissing, retryable: false,
	}
	rejectMCPStatusUnavailable = attachmentReject{
		code: protocol.CodeMCPStatusUnavailable, message: msgMCPStatusUnavailable, retryable: true,
	}
)

// handleMCPStatusRequest runs on the addressed connection's appFrameWorker.
// dispatchAppFrame has already enforced the nil-resolver and interactive gates
// on Run. Decode must precede membership, and membership must precede the resolver:
// each earlier rejection prevents the later dependency from learning an invalid
// or unhosted id. Every reply returns through forwardToRun for Run-owned sealing.
func (m *V2SessionManager) handleMCPStatusRequest(ctx context.Context, s *V2Session, plaintext []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		// Unreachable after dispatchAppFrame matched these bytes. There is no
		// trustworthy envelope id for correlation, and the decoder error can quote
		// remote-authored bytes, so log neither.
		m.cfg.Logger.Warn("relay: v2 mcp_status_request envelope did not decode",
			"event", "v2.mcp_status.request.envelope_err",
			"conn_id", s.connID)
		return
	}

	var req protocol.MCPStatusRequestPayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		m.rejectMCPStatusRequest(ctx, s, env.ID, rejectMCPStatusMalformed, "payload did not decode")
		return
	}
	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(req.ConversationID) {
		m.rejectMCPStatusRequest(ctx, s, env.ID, rejectMCPStatusConversationMissing, "conversation is not hosted")
		return
	}

	payload, ok := m.cfg.MCPStatusFor(ctx, req.ConversationID)
	if !ok {
		m.rejectMCPStatusRequest(ctx, s, env.ID, rejectMCPStatusUnavailable, "resolver has no current status")
		return
	}
	m.emitMCPStatusReply(ctx, s, env.ID, payload)
}

func (m *V2SessionManager) emitMCPStatusReply(ctx context.Context, s *V2Session, inReplyTo uint64, payload protocol.MCPStatusPayload) {
	body, err := json.Marshal(payload)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 mcp_status reply marshal failed",
			"event", "v2.mcp_status.request.marshal_err",
			"conn_id", s.connID)
		return
	}
	if !m.forwardMCPStatusReply(ctx, s, inReplyTo, protocol.TypeMCPStatus, body) {
		m.cfg.Logger.Debug("relay: v2 mcp_status reply dropped; session tearing down",
			"event", "v2.mcp_status.request.reply_dropped",
			"conn_id", s.connID)
	}
}

func (m *V2SessionManager) rejectMCPStatusRequest(ctx context.Context, s *V2Session, inReplyTo uint64, reject attachmentReject, reason string) {
	m.cfg.Logger.Warn("relay: v2 mcp_status_request refused",
		"event", "v2.mcp_status.request.refused",
		"conn_id", s.connID,
		"code", reject.code,
		"reason", reason)

	body, err := json.Marshal(protocol.ErrorPayload{
		Code: reject.code, Message: reject.message, Retryable: reject.retryable,
	})
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 mcp_status error reply marshal failed",
			"event", "v2.mcp_status.request.err_marshal",
			"conn_id", s.connID,
			"code", reject.code)
		return
	}
	if !m.forwardMCPStatusReply(ctx, s, inReplyTo, protocol.TypeError, body) {
		m.cfg.Logger.Debug("relay: v2 mcp_status reject dropped; session tearing down",
			"event", "v2.mcp_status.request.reply_dropped",
			"conn_id", s.connID,
			"code", reject.code)
	}
}

func (m *V2SessionManager) forwardMCPStatusReply(ctx context.Context, s *V2Session, inReplyTo uint64, typ string, payload json.RawMessage) bool {
	reply := protocol.Envelope{
		ID:        1,
		Type:      typ,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &inReplyTo,
	}
	frame, err := json.Marshal(reply)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 mcp_status reply envelope marshal failed",
			"event", "v2.mcp_status.request.envelope_marshal",
			"conn_id", s.connID,
			"reply_type", typ)
		return false
	}
	return m.forwardToRun(ctx, s, protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
}
