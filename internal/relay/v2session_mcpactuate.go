package relay

// The two MCP actuation verbs (#2419): mcp_reconnect and mcp_toggle. Both are
// intercepted in dispatchAppFrame before dispatch.Route and run on the addressed
// connection's appFrameWorker rather than inline on Run, because MCPActuator waits on
// a child round trip.
//
// THE SEAM IS NIL IN EVERY SHIPPED CONSTRUCTION AND THAT IS THE POINT. Nothing in this
// file applies an authorization check and nothing in it may grow one — the per-device
// gate and its audit record are #2420's, below MCPActuator. So what makes these two
// verbs fail-safe today is structural rather than procedural: dispatchAppFrame's nil
// check means no byte of either payload is parsed and no actuation exists to
// authorize. See the seam's own block for the written ordering obligation.
//
// It reuses forwardMCPStatusReply from v2session_mcpstatus.go verbatim rather than
// growing a second copy of the requester-only reply lane. One consequence is worth
// knowing before reading a log: that helper's diagnostics are named for the status
// path (v2.mcp_status.*), because that is where the lane lives, so a marshal failure
// on an ACTUATION reply surfaces under a status-shaped event name. Its only such
// record fires on an unreachable arm — re-marshalling an envelope whose payload is
// already valid json.RawMessage — and every reachable record on these two verbs is
// emitted from this file under a v2.mcp_actuation.* name.

import (
	"context"
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	msgMCPActuationMalformed           = "malformed MCP actuation request"
	msgMCPActuationConversationMissing = "conversation not found"
	// msgMCPActuationRefused answers every refusal alike and names none of them. It
	// must stay this generic: see protocol.CodeMCPActuationRefused for why an
	// unauthorized device and an unknown server are deliberately indistinguishable.
	msgMCPActuationRefused = "MCP actuation refused"
)

var (
	rejectMCPActuationMalformed = attachmentReject{
		code: protocol.CodeProtocolMalformed, message: msgMCPActuationMalformed, retryable: false,
	}
	rejectMCPActuationConversationMissing = attachmentReject{
		code: protocol.CodeConversationNotFound, message: msgMCPActuationConversationMissing, retryable: false,
	}
	// rejectMCPActuationRefused is NOT retryable, and the flag carries as much of the
	// merge as the code does: a retryable refusal would read as "transient, therefore
	// not the gate", re-splitting on one bit what one code deliberately joined.
	rejectMCPActuationRefused = attachmentReject{
		code: protocol.CodeMCPActuationRefused, message: msgMCPActuationRefused, retryable: false,
	}
)

// mcpActuationVerb labels one of the two verbs in this file's log records. Its values
// are the wire type constants, which are daemon-authored compile-time strings — the
// only thing on this path that is safe to log besides conn_id and a mapped code.
type mcpActuationVerb string

const (
	mcpVerbReconnect mcpActuationVerb = protocol.TypeMCPReconnect
	mcpVerbToggle    mcpActuationVerb = protocol.TypeMCPToggle
)

// handleMCPReconnect runs one inbound mcp_reconnect on the addressed connection's
// appFrameWorker. dispatchAppFrame has already enforced the nil-seam and interactive
// gates on Run.
//
// ORDER IS THE DESIGN, and the ordering rather than the mere presence of the steps is
// what carries the security property — handleMCPStatusRequest's rule, and each step
// exists to stop the next one from learning something it should not:
//
//  1. Decode the payload. A failure is REJECTED, not tolerated, and nothing about it
//     is echoed or logged: encoding/json quotes the offending input into its error
//     string and those bytes are remote-authored.
//  2. Membership. An unhosted conversation is refused before the seam can see the id,
//     so an actuator never learns an id this daemon does not hold.
//  3. The seam. It owns authorization, the actuation, the audit record and the
//     post-acknowledgement status read. This handler adds no check of its own.
//
// Steps 1 and 2 answer with DISTINGUISHABLE codes while every step-3 outcome answers
// with one merged code. That asymmetry is intended: a malformed frame and an unhosted
// conversation are facts about the REQUEST, which the asker already holds, where the
// step-3 reasons are facts about the daemon and about the asker's own privilege.
func (m *V2SessionManager) handleMCPReconnect(ctx context.Context, s *V2Session, plaintext []byte) {
	env, ok := m.decodeMCPActuationEnvelope(s, plaintext, mcpVerbReconnect)
	if !ok {
		return
	}

	var req protocol.MCPReconnectPayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		// Step 1. NEVER echo err or any payload byte.
		m.rejectMCPActuation(ctx, s, env.ID, mcpVerbReconnect, rejectMCPActuationMalformed, "payload did not decode")
		return
	}
	if !m.mcpActuationConversationHosted(ctx, s, env.ID, mcpVerbReconnect, req.ConversationID) {
		return // Step 2.
	}

	// Step 3. req crosses whole, with the conn's authenticated device; s.device is
	// nil for a conn that authenticated none, which the gate below must treat as
	// unprivileged. Reading it here follows handleMintPairing's precedent for reading
	// a Run-owned field from this worker.
	payload, accepted := m.cfg.MCPActuator.Reconnect(ctx, req, s.device)
	m.finishMCPActuation(ctx, s, env.ID, mcpVerbReconnect, payload, accepted)
}

// handleMCPToggle runs one inbound mcp_toggle. Identical in shape, ordering and
// obligations to handleMCPReconnect above, against MCPTogglePayload and SetEnabled;
// see that block, which this one does not restate.
//
// req.Enabled is passed through UNEXAMINED in both directions. This handler does not
// read it, does not compare it against any current state, and does not treat false
// specially — an absent key having decoded as false is a wire-shape property that
// protocol.MCPTogglePayload owns, and deciding what the value means is the gate's.
func (m *V2SessionManager) handleMCPToggle(ctx context.Context, s *V2Session, plaintext []byte) {
	env, ok := m.decodeMCPActuationEnvelope(s, plaintext, mcpVerbToggle)
	if !ok {
		return
	}

	var req protocol.MCPTogglePayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		m.rejectMCPActuation(ctx, s, env.ID, mcpVerbToggle, rejectMCPActuationMalformed, "payload did not decode")
		return
	}
	if !m.mcpActuationConversationHosted(ctx, s, env.ID, mcpVerbToggle, req.ConversationID) {
		return
	}

	payload, accepted := m.cfg.MCPActuator.SetEnabled(ctx, req, s.device)
	m.finishMCPActuation(ctx, s, env.ID, mcpVerbToggle, payload, accepted)
}

// decodeMCPActuationEnvelope decodes the frame for the second and last time — the
// worker receives bytes, not the envelope dispatchAppFrame already probed.
//
// The failure arm is unreachable: dispatchAppFrame decoded these same bytes to match
// the type before enqueuing them. It replies nothing because there is no trustworthy
// envelope id to correlate a reply to, and it logs no decoder error because that
// error can quote remote-authored bytes.
func (m *V2SessionManager) decodeMCPActuationEnvelope(s *V2Session, plaintext []byte, verb mcpActuationVerb) (protocol.Envelope, bool) {
	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		m.cfg.Logger.Warn("relay: v2 MCP actuation envelope did not decode",
			"event", "v2.mcp_actuation.envelope_err",
			"conn_id", s.connID,
			"verb", string(verb))
		return protocol.Envelope{}, false
	}
	return env, true
}

// mcpActuationConversationHosted reports whether this daemon hosts conversationID,
// answering one correlated conversation.not_found when it does not. The id is a
// lookup key only: it is neither logged nor returned.
func (m *V2SessionManager) mcpActuationConversationHosted(ctx context.Context, s *V2Session, inReplyTo uint64, verb mcpActuationVerb, conversationID string) bool {
	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(conversationID) {
		m.rejectMCPActuation(ctx, s, inReplyTo, verb, rejectMCPActuationConversationMissing, "conversation is not hosted")
		return false
	}
	return true
}

// finishMCPActuation answers one actuation the seam has ruled on: the payload it
// handed back on accept, the single merged reject otherwise.
//
// payload IS NOT READ when accepted is false, per MCPActuator's comma-ok contract —
// a refusing implementation is free to return anything, so treating its bytes as an
// answer would publish whatever a poisoned or careless one put there.
func (m *V2SessionManager) finishMCPActuation(ctx context.Context, s *V2Session, inReplyTo uint64, verb mcpActuationVerb, payload protocol.MCPStatusPayload, accepted bool) {
	if !accepted {
		m.rejectMCPActuation(ctx, s, inReplyTo, verb, rejectMCPActuationRefused, "seam refused the actuation")
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		// The payload is claude-authored, so the error is not logged either.
		m.cfg.Logger.Warn("relay: v2 MCP actuation reply marshal failed",
			"event", "v2.mcp_actuation.marshal_err",
			"conn_id", s.connID,
			"verb", string(verb))
		return
	}
	if !m.forwardMCPStatusReply(ctx, s, inReplyTo, protocol.TypeMCPStatus, body) {
		m.cfg.Logger.Debug("relay: v2 MCP actuation reply dropped; session tearing down",
			"event", "v2.mcp_actuation.reply_dropped",
			"conn_id", s.connID,
			"verb", string(verb))
	}
}

// rejectMCPActuation answers one coded error to the requesting connection only.
//
// reason is a DAEMON-AUTHORED STATIC STRING and reaches the local log record only;
// the wire always carries reject.message, which is a compile-time constant. That
// split is what lets a host operator tell the three rejects apart in a log while the
// asking device cannot tell the step-3 ones apart on the wire.
func (m *V2SessionManager) rejectMCPActuation(ctx context.Context, s *V2Session, inReplyTo uint64, verb mcpActuationVerb, reject attachmentReject, reason string) {
	m.cfg.Logger.Warn("relay: v2 MCP actuation refused",
		"event", "v2.mcp_actuation.refused",
		"conn_id", s.connID,
		"verb", string(verb),
		"code", reject.code,
		"reason", reason)

	body, err := json.Marshal(protocol.ErrorPayload{
		Code: reject.code, Message: reject.message, Retryable: reject.retryable,
	})
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 MCP actuation error reply marshal failed",
			"event", "v2.mcp_actuation.err_marshal",
			"conn_id", s.connID,
			"verb", string(verb),
			"code", reject.code)
		return
	}
	if !m.forwardMCPStatusReply(ctx, s, inReplyTo, protocol.TypeError, body) {
		m.cfg.Logger.Debug("relay: v2 MCP actuation reject dropped; session tearing down",
			"event", "v2.mcp_actuation.reply_dropped",
			"conn_id", s.connID,
			"code", reject.code)
	}
}
