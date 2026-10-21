package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadListShownAuthority(t *testing.T) {
	_, h, store, shadow, certificate, _ := testThreadWatermark(t, false)
	zero := conversations.ConversationID(testConvIDB)
	unknown := conversations.ConversationID("33333333-3333-4333-8333-333333333333")
	shadow.registry.Create(conversations.Conversation{ID: zero, ReadUpTo: 9})
	shadow.registry.Create(conversations.Conversation{ID: unknown, ReadUpTo: 7})
	if _, err := h.EnsureLogDir(zero); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(t.Context(), zero); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(zero).State == thread.StateUsable })
	if err := store.Unload(zero); err != nil {
		t.Fatal(err)
	}
	segments, err := filepath.Glob(filepath.Join(filepath.Dir(certificate), "*.jsonl"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("missing source fixture: %v", err)
	}
	for _, segment := range segments {
		if err := os.WriteFile(segment, []byte("unreadable legacy history\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := testDaemonThreadQueryWire(t, shadow, func(c *relay.V2SessionConfig) {
		c.Handlers = map[string]dispatch.Handler{
			protocol.TypeListConversations: handlers.ListConversationsWithAgents(shadow.registry, nil),
		}
	})
	w.open("phone", false, protocol.CapabilityThread)
	w.open("legacy", false)
	for _, peer := range []string{"phone", "legacy", "phone"} {
		testDaemonThreadRequest(t, w, peer, 40, protocol.TypeListConversations, struct{}{})
		gotPeer, env := w.read()
		var p struct {
			Conversations []map[string]json.RawMessage `json:"conversations"`
		}
		if json.Unmarshal(env.Payload, &p) != nil || gotPeer != peer || env.Type != protocol.TypeConversations || env.InReplyTo == nil || *env.InReplyTo != 40 || len(p.Conversations) != 3 {
			t.Fatalf("list response: %s %+v", gotPeer, env)
		}
		for i, id := range []string{testConvID, testConvIDB, string(unknown)} {
			row := p.Conversations[i]
			if string(row["id"]) != `"`+id+`"` || row["latest_entry_id"] != nil {
				t.Fatalf("row identity/retired evidence: %s", env.Payload)
			}
			wantShown := []string{"1", "0", ""}[i]
			if peer == "legacy" {
				wantShown = ""
			}
			if string(row["last_shown_version"]) != wantShown || string(row["read_up_to"]) != []string{"0", "9", "7"}[i] {
				t.Fatalf("shown/read evidence: %s", env.Payload)
			}
			if store.Status(conversations.ConversationID(id)).State != thread.StateNotLoaded {
				t.Fatal("list started a thread worker")
			}
		}
	}
	if _, known, err := h.CachedLatestEntryID(unknown); known || err != nil {
		t.Fatalf("list warmed unknown raw progress: known=%t err=%v", known, err)
	}
}
