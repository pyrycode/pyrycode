package relay

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// withheldFromConn reports whether env, about to be sealed for s, is a pushed
// frame about a Codex conversation that s must not receive because it did not
// negotiate protocol.CapabilityMultiAgent (#2644). It is the single choke point
// for that rule: every pushed frame — the push-queue drain, reconnect replay, the
// resync marker — reaches the wire through forwardEnvelope, which calls this, so
// no emitter can skip it. Handler replies take forwardAppReply and are replies by
// construction.
//
// Every uncertain case delivers, as before #2644: a capable conn, an unwired
// seam, a reply (InReplyTo set), a frame about no conversation, and a
// conversation the seam does not place under Codex. Run-goroutine only, like its
// caller: s.multiAgent is Run-owned.
//
// SECURITY: the withheld-frame log line carries the event slug, conn id and
// frame type only — never payload bytes, which can hold claude-authored text
// (#833), and never the conversation id.
func (m *V2SessionManager) withheldFromConn(s *V2Session, env protocol.Envelope) bool {
	if s.multiAgent || m.cfg.CodexConversation == nil || env.InReplyTo != nil {
		return false
	}
	id := pushedConversationID(env)
	if id == "" || !m.cfg.CodexConversation(id) {
		return false
	}
	m.cfg.Logger.Debug("relay: v2 push withheld; conn lacks multi_agent",
		"event", "v2.push.withheld_multi_agent",
		"conn_id", s.connID,
		"type", env.Type)
	return true
}

// pushedConversationID is the conversation a pushed frame is about: the
// payload's top-level id for conversation_updated, whose row names its
// conversation that way, and its top-level conversation_id for every other type.
// "" means the frame is about no conversation — including a payload that does
// not decode as an object — and is delivered unchanged.
//
// Pushed types with no conversation key are modal_dismissed and
// question_dismissed (opaque modal / batch ids; the *_shown frame that introduced
// the id carried the conversation and was withheld) and workspace_updated (a
// workspace, not a conversation).
func pushedConversationID(env protocol.Envelope) string {
	var p struct {
		ID             string `json:"id"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return ""
	}
	if env.Type == protocol.TypeConversationUpdated {
		return p.ID
	}
	return p.ConversationID
}
