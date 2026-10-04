package handlers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type emptyListHistory struct{}

func (emptyListHistory) LatestEntryID(conversations.ConversationID) (uint64, error) { return 0, nil }

const listReadID conversations.ConversationID = "11111111-1111-4111-8111-111111111111"
const listOtherID conversations.ConversationID = "22222222-2222-4222-8222-222222222222"

func TestListConversationsDurableReadState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reg.Create(conversations.Conversation{ID: listReadID, Cwd: "/w", ReadUpTo: 1, LastUsedAt: time.Now().UTC()})
	reg.Create(conversations.Conversation{ID: listOtherID, Cwd: "/empty"})
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	hist := history.New(root)
	appendEntry := func() uint64 {
		t.Helper()
		id, err := hist.Append(listReadID, "turn_end", json.RawMessage(`{}`), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	appendEntry()
	latest := appendEntry()
	for range 2 {
		reg, err = conversations.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		hist = history.New(root)
		c, recv := newListConvConn(t)
		if err := ListConversations(reg, hist)(context.Background(), c, makeListConversationsRequest(t, 19)); err != nil {
			t.Fatal(err)
		}
		env, payload := decodeConversationsResponse(t, recv())
		if len(payload.Conversations) != 2 {
			t.Fatalf("rows = %+v", payload)
		}
		zero, row := payload.Conversations[0], payload.Conversations[1]
		if zero.ReadUpTo != 0 || zero.LatestEntryID != 0 || row.ReadUpTo != 1 || row.LatestEntryID != latest {
			t.Fatalf("rows = %+v", payload.Conversations)
		}
		var raw struct {
			Conversations []map[string]json.RawMessage `json:"conversations"`
		}
		if err := json.Unmarshal(env.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		for _, r := range raw.Conversations {
			for _, key := range []string{"read_up_to", "latest_entry_id"} {
				if _, ok := r[key]; !ok {
					t.Fatalf("missing %s: %s", key, env.Payload)
				}
			}
		}
		latest = appendEntry()
	}
}

func TestListConversationsHistoryFailure(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"nil", "typed nil", "nil empty", "typed nil empty", "later row corrupt", "filtered corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: listReadID})
			reg.Create(conversations.Conversation{ID: listOtherID, CurrentSessionID: "codex", LastUsedAt: time.Now()})
			var hist historyLatestReader = history.New(root)
			if scenario == "nil" || scenario == "nil empty" {
				hist = nil
			}
			if scenario == "typed nil" || scenario == "typed nil empty" {
				hist = (*history.Store)(nil)
			}
			if scenario == "later row corrupt" || scenario == "filtered corrupt" {
				dir := filepath.Join(root, "conversations", string(listOtherID), "history")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "segment-00000000000000000001.jsonl"), []byte("garbage\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "nil empty" || scenario == "typed nil empty" {
				reg = &conversations.Registry{}
			}
			out := make(chan protocol.RoutingEnvelope, 4)
			c := dispatch.NewTestConn(listConvConnID, out, nil)
			c.SetMultiAgent(scenario != "filtered corrupt")
			harness := func(string) (string, bool) { return protocol.AgentCodex, true }
			if err := ListConversationsWithAgents(reg, harness, hist)(context.Background(), c, makeListConversationsRequest(t, 27)); err != nil {
				t.Fatal(err)
			}
			var env protocol.Envelope
			if err := json.Unmarshal((<-out).Frame, &env); err != nil {
				t.Fatal(err)
			}
			if scenario == "filtered corrupt" {
				if env.Type != protocol.TypeConversations {
					t.Fatalf("filtered: %s", env.Payload)
				}
				return
			}
			var payload protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if env.Type != protocol.TypeError || env.InReplyTo == nil || *env.InReplyTo != 27 || payload.Code != protocol.CodeHistoryUnavailable || !payload.Retryable || payload.Message != "conversation history is unavailable" {
				t.Fatalf("error: %+v, %+v", env, payload)
			}
			if len(out) != 0 {
				t.Fatal("partial list reply sent")
			}
		})
	}
}
