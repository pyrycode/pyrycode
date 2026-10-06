package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	msgSwitchMalformed   = "malformed switch_agent request"
	msgSwitchUnsupported = "agent switch unsupported"
	msgSwitchUnavailable = "agent switch unavailable"
	msgSwitchNotFound    = "unknown conversation"
	msgSwitchBusy        = "agent switch already running"
)

// handleSwitchAgent validates wire shape on Run, then leaves all switch work to
// a worker. Pointer fields preserve required model presence and effort clearing.
func (m *V2SessionManager) handleSwitchAgent(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if !s.multiAgent {
		m.switchAgentReplyError(ctx, s, env.ID, protocol.CodeProtocolUnsupported, msgSwitchUnsupported, false)
		return
	}
	if !s.interactive {
		return
	}
	var p struct {
		ConversationID *string `json:"conversation_id"`
		Agent          *string `json:"agent"`
		Model          *string `json:"model"`
		Effort         *string `json:"effort"`
	}
	body := bytes.TrimSpace(env.Payload)
	if len(body) == 0 || body[0] != '{' || json.Unmarshal(body, &p) != nil {
		m.switchAgentReplyError(ctx, s, env.ID, protocol.CodeProtocolMalformed, msgSwitchMalformed, false)
		return
	}
	if p.Agent == nil || (*p.Agent != "claude" && *p.Agent != "codex") {
		m.switchAgentReplyError(ctx, s, env.ID, protocol.CodeProtocolUnsupported, msgSwitchUnsupported, false)
		return
	}
	if p.ConversationID == nil || *p.ConversationID == "" || p.Model == nil || !validModel(*p.Model) || (p.Effort != nil && !validEffort(*p.Effort)) {
		m.switchAgentReplyError(ctx, s, env.ID, protocol.CodeProtocolMalformed, msgSwitchMalformed, false)
		return
	}
	if m.cfg.AgentSwitcher == nil {
		m.switchAgentReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSwitchUnavailable, true)
		return
	}
	request := protocol.SwitchAgentPayload{ConversationID: *p.ConversationID, Agent: *p.Agent, Model: *p.Model, Effort: p.Effort}
	// ctx is Run's context, not the requester's lifetime. The worker reads no
	// mutable session state and never performs crypto or logs untrusted values.
	go func() {
		result := switchAgentResult{s: s, inReplyTo: env.ID, outcome: m.cfg.AgentSwitcher.SwitchAgent(ctx, request)}
		m.deferSwitchAgentOutcome(ctx, result)
	}()
}

type switchAgentResult struct {
	s         *V2Session
	inReplyTo uint64
	outcome   AgentSwitchOutcome
}

func (m *V2SessionManager) deferSwitchAgentOutcome(ctx context.Context, result switchAgentResult) {
	select {
	case m.switchAgentDone <- result:
	case <-result.s.done:
	case <-ctx.Done():
	}
}

// handleSwitchAgentDone alone reads session state and seals a completion reply.
func (m *V2SessionManager) handleSwitchAgentDone(ctx context.Context, result switchAgentResult) {
	s := result.s
	if s.state != V2StateOpen || m.sessions[s.connID] != s {
		return
	}
	if result.outcome.State != AgentSwitchCommitted {
		code, message, retry := protocol.CodeServerBinaryOffline, msgSwitchUnavailable, true
		switch result.outcome.Failure {
		case AgentSwitchConversationNotFound:
			code, message, retry = protocol.CodeConversationNotFound, msgSwitchNotFound, false
		case AgentSwitchInvalidRequest:
			code, message, retry = protocol.CodeProtocolMalformed, msgSwitchMalformed, false
		case AgentSwitchModelNotOffered:
			code, message, retry = protocol.CodeProtocolMalformed, MsgSettingsModelNotOffered, false
		case AgentSwitchEffortNotOffered:
			code, message, retry = protocol.CodeProtocolMalformed, msgSettingsMalformed, false
		case AgentSwitchVocabularyUnavailable:
			code, message, retry = protocol.CodeModelListUnavailable, MsgModelListUnavailable, true
		case AgentSwitchBusy:
			code, message = protocol.CodeServerBinaryBusy, msgSwitchBusy
		case AgentSwitchWorkspaceRejected:
			code, message, retry = protocol.CodeProtocolUnsupported, msgSwitchUnsupported, false
		}
		m.switchAgentReplyError(ctx, s, result.inReplyTo, code, message, retry)
	}
	m.cfg.Logger.Debug("relay: v2 switch_agent completed", "event", "v2.switch_agent.completed", "conn_id", s.connID)
}

func (m *V2SessionManager) switchAgentReplyError(ctx context.Context, s *V2Session, id uint64, code, message string, retry bool) {
	payload, err := json.Marshal(protocol.ErrorPayload{Code: code, Message: message, Retryable: retry})
	if err != nil {
		// Closed scalar payload cannot fail to marshal.
		return
	}
	if m.dropInlineReplyIfDown(s, "v2.switch_agent.reply_dropped_transport_down") {
		return
	}
	reply := protocol.Envelope{ID: 1, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: payload, InReplyTo: &id}
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 switch_agent reply dropped", "event", "v2.switch_agent.reply_dropped", "conn_id", s.connID)
	}
}
