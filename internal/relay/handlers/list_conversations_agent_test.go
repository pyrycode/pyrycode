package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// agentFixture is a registry holding one conversation of each shape the agent
// projection distinguishes, and the harness read the pool would answer for it:
// a Claude session, a Codex session, a conversation with no bound session, and
// one bound to a session the pool does not hold.
func agentFixture(t *testing.T, withCodex bool) (*conversations.Registry, SessionHarnessFunc) {
	t.Helper()
	reg := &conversations.Registry{}
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	add := func(id, sessionID string, offset time.Duration) {
		reg.Create(conversations.Conversation{
			ID:               conversations.ConversationID(id),
			Cwd:              "/work/" + id,
			CurrentSessionID: sessionID,
			LastUsedAt:       base.Add(offset),
		})
	}
	add("conv-claude", "sess-claude", 0)
	if withCodex {
		add("conv-codex", "sess-codex", time.Second)
	}
	add("conv-unbound", "", 2*time.Second)
	add("conv-gone", "sess-gone", 3*time.Second)

	harness := map[string]string{"sess-claude": "claude", "sess-codex": "codex"}
	return reg, func(sessionID string) (string, bool) {
		h, ok := harness[sessionID]
		return h, ok
	}
}

func listWithAgents(t *testing.T, reg *conversations.Registry, harnessFor SessionHarnessFunc, multiAgent bool) (json.RawMessage, protocol.ConversationsPayload) {
	t.Helper()
	c, recv := newListConvConn(t)
	c.SetMultiAgent(multiAgent)
	if err := ListConversationsWithAgents(reg, harnessFor)(context.Background(), c, makeListConversationsRequest(t, 1)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	inner, payload := decodeConversationsResponse(t, recv())
	return inner.Payload, payload
}

// A client that negotiated multi_agent reads every conversation, each tagged
// with its bound session's agent. No bound session, or one the pool does not
// hold, reads claude rather than dropping the row.
func TestListConversationsWithAgents_CapableClientReadsEveryAgent(t *testing.T) {
	t.Parallel()
	reg, harnessFor := agentFixture(t, true)
	_, payload := listWithAgents(t, reg, harnessFor, true)

	want := []struct{ id, agent string }{
		{"conv-claude", protocol.AgentClaude},
		{"conv-codex", protocol.AgentCodex},
		{"conv-unbound", protocol.AgentClaude},
		{"conv-gone", protocol.AgentClaude},
	}
	if len(payload.Conversations) != len(want) {
		t.Fatalf("len(Conversations) = %d, want %d: %+v", len(payload.Conversations), len(want), payload.Conversations)
	}
	for i, w := range want {
		got := payload.Conversations[i]
		if got.ID != w.id || got.Agent != w.agent {
			t.Errorf("row %d = (%q, agent %q), want (%q, agent %q)", i, got.ID, got.Agent, w.id, w.agent)
		}
	}
}

// A client that did not negotiate multi_agent is not sent the Codex
// conversation, and every row it is sent is byte-identical to the reply a
// daemon with no agent read at all gives for the same Claude-only registry —
// which is today's reply, with no agent key.
func TestListConversationsWithAgents_IncapableClientSeesTodaysList(t *testing.T) {
	t.Parallel()
	reg, harnessFor := agentFixture(t, true)
	got, payload := listWithAgents(t, reg, harnessFor, false)

	for _, row := range payload.Conversations {
		if row.ID == "conv-codex" {
			t.Fatalf("Codex conversation sent to a client without multi_agent: %s", got)
		}
	}

	var wrapper struct {
		Conversations []map[string]json.RawMessage `json:"conversations"`
	}
	if err := json.Unmarshal(got, &wrapper); err != nil {
		t.Fatalf("decode raw payload: %v", err)
	}
	for i, row := range wrapper.Conversations {
		if _, ok := row["agent"]; ok {
			t.Errorf("row %d carries an agent key for a client without multi_agent", i)
		}
	}

	claudeOnly, _ := agentFixture(t, false)
	c, recv := newListConvConn(t)
	if err := ListConversations(claudeOnly)(context.Background(), c, makeListConversationsRequest(t, 1)); err != nil {
		t.Fatalf("agent-blind handler: %v", err)
	}
	inner, _ := decodeConversationsResponse(t, recv())
	if string(got) != string(inner.Payload) {
		t.Errorf("payload differs from today's reply:\n got %s\nwant %s", got, inner.Payload)
	}
}

// The agent-blind form reads no harness, so it neither tags nor filters: a
// capable client is still told every row is claude, which is true of every
// conversation a daemon without a harness read can run.
func TestListConversations_AgentBlindReadsClaude(t *testing.T) {
	t.Parallel()
	reg, _ := agentFixture(t, true)
	_, payload := listWithAgents(t, reg, nil, true)
	if len(payload.Conversations) != 4 {
		t.Fatalf("len(Conversations) = %d, want 4", len(payload.Conversations))
	}
	for _, row := range payload.Conversations {
		if row.Agent != protocol.AgentClaude {
			t.Errorf("row %q agent = %q, want %q", row.ID, row.Agent, protocol.AgentClaude)
		}
	}
}
