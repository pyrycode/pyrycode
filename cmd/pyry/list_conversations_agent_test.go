package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// TestListConversations_AgentFromRealPool drives the wired list_conversations
// handler over a real pool holding a live Claude bootstrap and a dormant Codex
// session (#2643), one conversation bound to each. A client that negotiated
// multi_agent reads both, tagged; a client that did not reads the Claude row
// alone, with no agent key.
func TestListConversations_AgentFromRealPool(t *testing.T) {
	t.Parallel()
	pool, _ := newDormantWritePool(t, `"harness":"codex",`)

	reg := &conversations.Registry{}
	ts := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-claude", Cwd: "/w", CurrentSessionID: dormantWriteBootID, LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-codex", Cwd: "/w", CurrentSessionID: dormantWriteTargetID, LastUsedAt: ts.Add(time.Second)})
	h := handlers.ListConversationsWithAgents(reg, sessionHarness(pool), emptyAgentListHistory{})

	list := func(multiAgent bool) []map[string]json.RawMessage {
		t.Helper()
		out := make(chan protocol.RoutingEnvelope, 1)
		c := dispatch.NewTestConn("conn-agent", out, nil)
		c.SetMultiAgent(multiAgent)
		req := protocol.Envelope{ID: 1, Type: protocol.TypeListConversations, TS: ts, Payload: json.RawMessage(`{}`)}
		if err := h(context.Background(), c, req); err != nil {
			t.Fatalf("handler: %v", err)
		}
		var inner protocol.Envelope
		if err := json.Unmarshal((<-out).Frame, &inner); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		var payload struct {
			Conversations []map[string]json.RawMessage `json:"conversations"`
		}
		if err := json.Unmarshal(inner.Payload, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		return payload.Conversations
	}

	capable := list(true)
	if len(capable) != 2 {
		t.Fatalf("capable client got %d rows, want 2", len(capable))
	}
	for i, want := range []string{`"claude"`, `"codex"`} {
		if got := string(capable[i]["agent"]); got != want {
			t.Errorf("capable row %d agent = %s, want %s", i, got, want)
		}
	}

	incapable := list(false)
	if len(incapable) != 1 || string(incapable[0]["id"]) != `"conv-claude"` {
		t.Fatalf("incapable client rows = %v, want conv-claude alone", incapable)
	}
	if _, ok := incapable[0]["agent"]; ok {
		t.Error("incapable client's row carries an agent key")
	}
}

// emptyAgentListHistory isolates agent wiring from durable history lookup.
type emptyAgentListHistory struct{}

func (emptyAgentListHistory) LatestEntryID(conversations.ConversationID) (uint64, error) {
	return 0, nil
}
