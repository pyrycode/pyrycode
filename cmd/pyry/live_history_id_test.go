package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestLiveProducers_HistoryEntryID(t *testing.T) {
	t.Parallel()
	for _, producer := range []string{"interactive", "transition"} {
		for _, scenario := range []string{"recipients", "no recipients", "absent store", "failed append"} {
			t.Run(producer+"/"+scenario, func(t *testing.T) {
				t.Parallel()
				const text = "ZZ2861-PRIVATE-CONTENT-ZZ"
				ctx := context.Background()
				var logs bytes.Buffer
				logger := bufLogger(&logs)
				var store *history.Store
				if scenario != "absent store" {
					dir := t.TempDir()
					if scenario == "failed append" {
						// A file where the instance directory belongs forces real I/O failure.
						dir = filepath.Join(dir, "blocked")
						if err := os.WriteFile(dir, []byte("blocked"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					store = history.New(dir)
				}
				stored := scenario != "absent store" && scenario != "failed append"
				if stored {
					for range 7 {
						if _, err := store.Append(conversations.ConversationID(testConvID), protocol.TypeMessage, json.RawMessage(`{}`), historyTS); err != nil {
							t.Fatal(err)
						}
					}
				}
				bcast := &fakeInteractiveBcast{}
				if scenario != "no recipients" {
					bcast.snapshots = [][]relay.ActiveConn{{
						{ConnID: "i1", Interactive: true},
						{ConnID: "legacy"},
						{ConnID: "i2", Interactive: true},
						{ConnID: "i3", Interactive: true},
					}}
				}
				var nextID *uint64
				wantType := protocol.TypeMessage
				switch producer {
				case "interactive":
					e := newInteractiveTurnEmitterV2(&stubCursor{}, bcast, logger)
					e.hist, e.nextID = store, 100
					nextID = &e.nextID
					wantType = protocol.TypeToolProgress
					e.emit(ctx, testConvID, wantType, map[string]string{"conversation_id": testConvID, "text": text})
				case "transition":
					e := newSessionTransitionEmitterV2(bcast, constResolver(testConvID, true), logger)
					e.hist = store
					counter := uint64(100)
					nextID = &counter
					wantType = protocol.TypeSessionTransition
					e.broadcast(ctx, sessions.SessionTransition{PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred})
				}
				wantPushes := 3
				if scenario == "no recipients" || producer == "transition" {
					wantPushes = 0
				}
				if len(bcast.pushes) != wantPushes || *nextID != 100+uint64(wantPushes) {
					t.Fatalf("pushes=%d counter=%d, want %d and %d", len(bcast.pushes), *nextID, wantPushes, 100+wantPushes)
				}
				var entry history.Entry
				if stored {
					entries := historyEntries(t, store, testConvID)
					if len(entries) != 8 {
						t.Fatalf("entries=%d, want seven seeds plus one logical event", len(entries))
					}
					entry = entries[7]
					if entry.ID != 8 || entry.Type != wantType || entry.TS.IsZero() {
						t.Fatalf("appended entry = %+v", entry)
					}
				}
				for i, p := range bcast.pushes {
					env := p.env
					if env.EventID != nil {
						t.Fatal("live state carries a retired ring event_id")
					}
					if p.connID != []string{"i1", "i2", "i3"}[i] || env.ID != 101+uint64(i) || env.Type != wantType {
						t.Fatalf("unexpected recipient/envelope: %+v", p)
					}
					if stored {
						if env.HistoryEntryID == nil || *env.HistoryEntryID != entry.ID {
							t.Fatalf("history_entry_id=%v, want stored id %d", env.HistoryEntryID, entry.ID)
						}
						if !bytes.Equal(env.Payload, entry.Payload) || !env.TS.Equal(entry.TS) {
							t.Fatal("live payload/timestamp differs from stored entry")
						}
					} else {
						wire, err := json.Marshal(env)
						if err != nil {
							t.Fatal(err)
						}
						var keys map[string]json.RawMessage
						if err := json.Unmarshal(wire, &keys); err != nil {
							t.Fatal(err)
						}
						if _, present := keys["history_entry_id"]; present {
							t.Fatalf("history_entry_id must be omitted: %s", wire)
						}
					}

				}
				if scenario == "failed append" {
					if !strings.Contains(logs.String(), "history_append_err") || !strings.Contains(logs.String(), "reason=write") {
						t.Fatalf("missing append warning: %s", &logs)
					}
					if strings.Contains(logs.String(), text) || strings.Contains(logs.String(), "/blocked") {
						t.Fatalf("append warning leaked content/path: %s", &logs)
					}
				} else if logs.Len() != 0 {
					t.Fatalf("unexpected log: %s", &logs)
				}
			})
		}
	}
}
