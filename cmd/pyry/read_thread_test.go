package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestMarkConversationReadThreadRestart(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			cfg, _, _, shadow, certificate, _ := testThreadWatermark(t, empty)
			path := filepath.Join(t.TempDir(), "conversations.json")
			shadow.registry.Create(conversations.Conversation{ID: testConvIDB, ReadUpTo: 77})
			if err := shadow.registry.Save(path); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			reg, err := conversations.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(certificate))))
			h := history.New(root)
			store := thread.NewStore(h)
			t.Cleanup(func() { _ = store.Shutdown() })
			shadow.registry, shadow.store = reg, store
			installDaemonThreadRelay(&cfg, shadow)
			pushes := 0
			mark := handlers.MarkConversationRead(reg, cfg.ThreadLastShownVersion, path, func(p protocol.ConversationUpdatedPayload) {
				pushes++
				if p.ReadUpTo != 1 {
					t.Errorf("announcement = %+v", p)
				}
			}, discardLogger())
			request := func(id string, available bool, want uint64) {
				t.Helper()
				out := make(chan protocol.RoutingEnvelope, 1)
				c := dispatch.NewTestConn("phone", out, nil)
				env := protocol.Envelope{ID: 91, Type: protocol.TypeMarkConversationRead, Payload: json.RawMessage(fmt.Sprintf(`{"conversation_id":%q,"up_to":999}`, id))}
				if err := mark(t.Context(), c, env); err != nil {
					t.Fatal(err)
				}
				var reply protocol.Envelope
				if err := json.Unmarshal((<-out).Frame, &reply); err != nil {
					t.Fatal(err)
				}
				if reply.InReplyTo == nil || *reply.InReplyTo != 91 {
					t.Fatal("uncorrelated reply")
				}
				if available {
					var p protocol.ConversationUpdatedPayload
					if json.Unmarshal(reply.Payload, &p) != nil || reply.Type != protocol.TypeConversationUpdated || p.ReadUpTo != want {
						t.Fatalf("read reply = %+v", reply)
					}
				} else {
					var p protocol.ErrorPayload
					if json.Unmarshal(reply.Payload, &p) != nil || reply.Type != protocol.TypeError || p.Code != protocol.CodeHistoryUnavailable || !p.Retryable {
						t.Fatalf("refusal = %+v", reply)
					}
				}
				cv, _ := reg.Get(conversations.ConversationID(id))
				if cv.ReadUpTo != want {
					t.Fatalf("stored mark = %d, want %d", cv.ReadUpTo, want)
				}
			}
			// A clean certificate alone does not establish raw progress after restart.
			request(testConvID, false, 0)
			request(testConvID, false, 0)
			request(testConvIDB, false, 77)
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || pushes != 0 {
				t.Fatal("refusal persisted or announced a mark")
			}
			if _, err := h.LatestEntryID(testConvID); err != nil {
				t.Fatal(err)
			}
			want := uint64(1)
			if empty {
				want = 0
			}
			request(testConvID, true, want)
			request(testConvID, true, want)
			request(testConvIDB, false, 77)
			if pushes != int(want) {
				t.Fatalf("announcements = %d, want %d", pushes, want)
			}
			if store.Status(testConvID).State != thread.StateNotLoaded || store.Status(testConvIDB).State != thread.StateNotLoaded {
				t.Fatal("read lookup loaded a worker")
			}
			persisted, err := conversations.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			cv, _ := persisted.Get(testConvID)
			other, _ := persisted.Get(testConvIDB)
			if cv.ReadUpTo != want || other.ReadUpTo != 77 {
				t.Fatalf("durable marks = %d/%d", cv.ReadUpTo, other.ReadUpTo)
			}
		})
	}
}
