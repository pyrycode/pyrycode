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

// maxMCPStatusAsksPerConn caps how many asks one conn may have waiting in the seam at
// once (#2702). An ask that finds every slot taken is refused on the spot with the
// retryable mcp_status.unavailable rather than queued, since queuing it on the worker
// would park the worker again. Four for maxContextUsageAsksPerConn's reasons; the cap
// is this verb's own so neither verb can take the other's slots.
const maxMCPStatusAsksPerConn = 4

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
//
// The resolver's wait runs on a goroutine of the ask's own (#2702), the shape
// handleRequestContextUsage took in #2563: a live child that never answers would
// otherwise hold this conn's later frames, its next send_message included, until
// teardown. The cost is ordering — this reply can arrive after replies to frames
// sent later, and clients correlate on in_reply_to. ctx is the conn-scoped context
// appFrameWorker derives, and asks is that worker's semaphore of waiting-ask slots.
func (m *V2SessionManager) handleMCPStatusRequest(ctx context.Context, s *V2Session, asks chan struct{}, plaintext []byte) {
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

	// A slot is taken first, non-blocking, so one conn holds at most
	// maxMCPStatusAsksPerConn waiting asks.
	select {
	case asks <- struct{}{}:
	default:
		m.rejectMCPStatusRequest(ctx, s, env.ID, rejectMCPStatusUnavailable, "too many MCP status requests waiting on this connection")
		return
	}
	go func() {
		defer func() { <-asks }()
		m.resolveMCPStatusRequest(ctx, s, env.ID, req.ConversationID)
	}()
}

// resolveMCPStatusRequest calls the resolver on the ask's own goroutine. ctx is the
// conn's, ended by the worker's return, so both conn teardown and manager shutdown end
// the wait. A resolver that returns after ctx ended is answered with NOTHING, whatever
// it returned: the conn is gone, and a refusal logged for it would misreport a
// teardown as unavailable status.
func (m *V2SessionManager) resolveMCPStatusRequest(ctx context.Context, s *V2Session, inReplyTo uint64, conversationID string) {
	payload, ok := m.cfg.MCPStatusFor(ctx, conversationID)
	if ctx.Err() != nil {
		m.cfg.Logger.Debug("relay: v2 mcp_status request abandoned; session tearing down",
			"event", "v2.mcp_status.request.abandoned",
			"conn_id", s.connID)
		return
	}
	if !ok {
		m.rejectMCPStatusRequest(ctx, s, inReplyTo, rejectMCPStatusUnavailable, "resolver has no current status")
		return
	}
	m.emitMCPStatusReply(ctx, s, inReplyTo, payload)
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
