package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// createConvAgent runs one create_conversation carrying agent (nil = absent) on
// a conn whose multi_agent decision is multiAgent, and returns the reply's
// envelope with the creator it drove.
func createConvAgent(t *testing.T, multiAgent bool, agent *string, wantType string) (protocol.Envelope, *stubSessionCreator, int) {
	t.Helper()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	c.SetMultiAgent(multiAgent)
	creator := &stubSessionCreator{}
	h := CreateConversation(reg, creator, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, createConvRequest(t, protocol.CreateConversationPayload{Agent: agent})); err != nil {
		t.Fatalf("handler: %v", err)
	}
	return assertCreateConvEnvelopeShape(t, recv(), wantType), creator, len(reg.List())
}

// TestCreateConversation_Agent_Created (#2647): an accepted create mints the
// resolved agent's session, and the reply names the agent only to a client that
// negotiated multi_agent. An older client's reply carries no agent key at all,
// the byte-identical frame it read before agents existed.
func TestCreateConversation_Agent_Created(t *testing.T) {
	t.Parallel()
	codex, claude := protocol.AgentCodex, protocol.AgentClaude
	cases := []struct {
		name       string
		multiAgent bool
		agent      *string
		wantMinted string
		wantReply  string // "" = no agent key
	}{
		{"capable codex", true, &codex, protocol.AgentCodex, protocol.AgentCodex},
		{"capable claude", true, &claude, protocol.AgentClaude, protocol.AgentClaude},
		{"capable absent", true, nil, protocol.AgentClaude, protocol.AgentClaude},
		{"older absent", false, nil, protocol.AgentClaude, ""},
		{"older claude", false, &claude, protocol.AgentClaude, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, creator, rows := createConvAgent(t, tc.multiAgent, tc.agent, protocol.TypeConversationCreated)
			if len(creator.agents) != 1 || creator.agents[0] != tc.wantMinted {
				t.Errorf("minted agents = %v, want [%s]", creator.agents, tc.wantMinted)
			}
			if rows != 1 {
				t.Errorf("registry rows = %d, want 1", rows)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(env.Payload, &raw); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			got, present := raw["agent"]
			switch {
			case tc.wantReply == "" && present:
				t.Errorf("reply carries agent %s, want no agent key for a client without multi_agent", got)
			case tc.wantReply != "" && string(got) != `"`+tc.wantReply+`"`:
				t.Errorf("reply agent = %s (present %v), want %q", got, present, tc.wantReply)
			}
		})
	}
}

// TestCreateConversation_Agent_Refused (#2647): a codex request from a client
// without multi_agent, and any unknown agent, is refused non-retryably with
// protocol.unsupported before the mint — nothing is created, and the requested
// value is never echoed.
func TestCreateConversation_Agent_Refused(t *testing.T) {
	t.Parallel()
	codex, gemini, empty := protocol.AgentCodex, "gemini", ""
	cases := []struct {
		name       string
		multiAgent bool
		agent      *string
		wantMsg    string
	}{
		{"older codex", false, &codex, msgCreateConversationAgentUnsupported},
		{"capable unknown", true, &gemini, msgCreateConversationAgentUnknown},
		{"capable empty", true, &empty, msgCreateConversationAgentUnknown},
		{"older unknown", false, &gemini, msgCreateConversationAgentUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, creator, rows := createConvAgent(t, tc.multiAgent, tc.agent, protocol.TypeError)
			var payload protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Fatalf("unmarshal error payload: %v", err)
			}
			if payload.Code != protocol.CodeProtocolUnsupported {
				t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeProtocolUnsupported)
			}
			if payload.Retryable {
				t.Error("Retryable = true, want false")
			}
			if payload.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", payload.Message, tc.wantMsg)
			}
			if creator.calls != 0 {
				t.Errorf("creator.Create called %d times, want 0 (a refusal must not reach the pool)", creator.calls)
			}
			if rows != 0 {
				t.Errorf("registry rows = %d, want 0", rows)
			}
		})
	}
}
