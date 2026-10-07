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
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestLiveProducers_HistoryEntryID(t *testing.T) {
	t.Parallel()
	for _, producer := range []string{"interactive", "transition", "operator"} {
		for _, scenario := range []string{"recipients", "no recipients", "absent store", "failed append", "no ring"} {
			if scenario == "no ring" && producer != "operator" {
				continue
			}
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
				var ring *eventring.Ring
				if producer != "transition" && scenario != "no ring" {
					ring = eventring.New(16)
					for range 3 {
						ring.Append(testConvID, protocol.TypeMessage, json.RawMessage(`{}`), historyTS)
					}
				}
				var nextID *uint64
				wantType := protocol.TypeMessage
				switch producer {
				case "interactive":
					e := newInteractiveTurnEmitterV2(&stubCursor{}, bcast, logger)
					e.hist, e.ring, e.nextID = store, ring, 100
					nextID = &e.nextID
					wantType = protocol.TypeAssistantDelta
					e.emit(ctx, testConvID, wantType, protocol.AssistantDeltaPayload{ConversationID: testConvID, Text: text})
				case "transition":
					e := newSessionTransitionEmitterV2(bcast, constResolver(testConvID, true), logger)
					e.hist, e.nextID = store, 100
					nextID = &e.nextID
					wantType = protocol.TypeSessionTransition
					e.broadcast(ctx, sessions.SessionTransition{PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred})
				case "operator":
					ch := make(chan operatorMessage, 1)
					e := newOperatorMessageEmitterV2(ch, logger)
					e.nextID = 100
					nextID = &e.nextID
					newOperatorMessageHistory(store, operatorMessageNotify(ch, logger), nil, logger)(testConvID, msgqueue.QueuedMessage{ID: 42, MessageID: opMsgID, Text: text})
					select {
					case m := <-ch:
						e.broadcast(ctx, bcast, ring, m)
					default:
						t.Fatal("history commit did not hand off the operator message")
					}
				}
				wantPushes := 3
				if scenario == "no recipients" {
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
				var event eventring.Event
				if ring != nil {
					events, gap := ring.After(testConvID, 3)
					if gap || len(events) != 1 {
						t.Fatalf("ring events=%d gap=%v, want one logical event", len(events), gap)
					}
					event = events[0]
					if event.ID != 4 || event.Type != wantType {
						t.Fatalf("ring event=%+v", event)
					}
					if event.HistoryEntryID != entry.ID {
						t.Fatalf("ring history id=%d, want %d", event.HistoryEntryID, entry.ID)
					}
					if stored {
						assertLogMatchesRing(t, []history.Entry{entry}, events)
					}
				}
				for i, p := range bcast.pushes {
					env := p.env
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
					if ring == nil {
						if env.EventID != nil {
							t.Fatal("event_id present without ring")
						}
					} else if env.EventID == nil || *env.EventID != event.ID || !bytes.Equal(env.Payload, event.Payload) || !env.TS.Equal(event.TS) {
						t.Fatal("live envelope does not match ring event")
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
