package handlers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

const listReadID conversations.ConversationID = "11111111-1111-4111-8111-111111111111"
const listOtherID conversations.ConversationID = "22222222-2222-4222-8222-222222222222"

func TestListConversationsWithoutLegacyHistory(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{true, false} {
		reg := &conversations.Registry{}
		if !empty {
			reg.Create(conversations.Conversation{ID: listReadID, ReadUpTo: 7})
		}
		c, recv := newListConvConn(t)
		if err := ListConversations(reg)(t.Context(), c, makeListConversationsRequest(t, 29)); err != nil {
			t.Fatal(err)
		}
		env, rows := decodeConversationsResponse(t, recv())
		if env.InReplyTo == nil || *env.InReplyTo != 29 || empty && len(rows.Conversations) != 0 || !empty && len(rows.Conversations) != 1 {
			t.Fatalf("list without history: %+v %+v", env, rows)
		}
		var raw struct {
			Conversations []map[string]json.RawMessage `json:"conversations"`
		}
		if err := json.Unmarshal(env.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		for _, row := range raw.Conversations {
			if _, exists := row["latest_entry_id"]; exists {
				t.Fatalf("legacy watermark on wire: %s", env.Payload)
			}
			if _, exists := row["last_shown_version"]; exists || string(row["read_up_to"]) != "7" {
				t.Fatalf("unknown shown evidence or durable mark changed: %s", env.Payload)
			}
		}
	}
}

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
	for range 2 {
		reg, err = conversations.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		c, recv := newListConvConn(t)
		if err := ListConversations(reg)(context.Background(), c, makeListConversationsRequest(t, 19)); err != nil {
			t.Fatal(err)
		}
		env, payload := decodeConversationsResponse(t, recv())
		if len(payload.Conversations) != 2 {
			t.Fatalf("rows = %+v", payload)
		}
		zero, row := payload.Conversations[0], payload.Conversations[1]
		if zero.ReadUpTo != 0 || row.ReadUpTo != 1 {
			t.Fatalf("rows = %+v", payload.Conversations)
		}
		var raw struct {
			Conversations []map[string]json.RawMessage `json:"conversations"`
		}
		if err := json.Unmarshal(env.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		for _, r := range raw.Conversations {
			if _, ok := r["read_up_to"]; !ok {
				t.Fatalf("missing read_up_to: %s", env.Payload)
			}
		}
	}
}
