package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestQueueCommandItemBinding(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{protocol.TypeDequeueMessage, protocol.TypeSendQueuedNow} {
		for _, value := range []string{"null", "0", `""`, `"untrusted-binding-marker"`, "-1", "1.0", "1e0", "true", "[]", "{}", "18446744073709551616", `"` + strings.Repeat("x", 1000) + `"`, "101", "100"} {
			t.Run(typ+"/"+value[:min(30, len(value))], func(t *testing.T) {
				logs := &lockedBuffer{}
				changes, writes := 0, 0
				q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil },
					OnChange: func(string) { changes++ }, SendNow: func(context.Context, string, uint64, []byte) error { writes++; return nil }})
				if err != nil {
					t.Fatal(err)
				}
				id := q.Enqueue("conv", "queued")
				q.BindAcceptedItem("conv", id, 100)
				mgr := &V2SessionManager{cfg: V2SessionConfig{QueueRemover: q, QueueSender: q, Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}}
				env := protocol.Envelope{Type: typ, Payload: json.RawMessage(`{"conversation_id":"conv","queued_msg_id":1,"item_id":` + value + `}`)}
				s := &V2Session{interactive: true}
				if typ == protocol.TypeDequeueMessage {
					mgr.handleDequeueMessage(s, env)
				} else {
					raw, err := json.Marshal(env)
					if err != nil {
						t.Fatal(err)
					}
					mgr.handleSendQueuedNow(s, raw)
				}
				want := 1
				if value == "100" {
					want = 0
				}
				if len(q.Snapshot("conv")) != want || changes != 2-want {
					t.Fatal("binding validation mutated wrong entry")
				}
				if writes != 0 && !(value == "100" && typ == protocol.TypeSendQueuedNow) {
					t.Fatal("invalid binding wrote")
				}
				if strings.Contains(logs.String(), "item_id") || strings.Contains(logs.String(), "untrusted-binding-marker") {
					t.Fatal("binding leaked to log")
				}
			})
		}
	}
}

func TestQueueBindingUnsupportedIsInert(t *testing.T) {
	t.Parallel()
	r, s := &fakeQueueRemover{removed: true}, &fakeQueueSender{sent: true}
	mgr := &V2SessionManager{cfg: V2SessionConfig{QueueRemover: r, QueueSender: s, Logger: slog.Default()}}
	env := protocol.Envelope{Payload: json.RawMessage(`{"conversation_id":"conv","queued_msg_id":1,"item_id":100}`)}
	session := &V2Session{interactive: true}
	mgr.handleDequeueMessage(session, env)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	mgr.handleSendQueuedNow(session, raw)
	if len(r.snapshot()) != 0 || len(s.snapshot()) != 0 {
		t.Fatal("bound request fell back to legacy operation")
	}
}

func TestQueueBindingDecodeFailure(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"conversation_id":"conv","queued_msg_id":1,"item_id":100`,
		`{"conversation_id":"conv","queued_msg_id":1,"item_id":"unterminated}`,
		`{"conversation_id":false,"queued_msg_id":1,"item_id":100}`,
		`{"conversation_id":"conv","queued_msg_id":"1","item_id":100}`,
	} {
		writes := 0
		q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil },
			SendNow: func(context.Context, string, uint64, []byte) error { writes++; return nil }})
		if err != nil {
			t.Fatal(err)
		}
		q.BindAcceptedItem("conv", q.Enqueue("conv", "queued"), 100)
		logs := &lockedBuffer{}
		mgr := &V2SessionManager{cfg: V2SessionConfig{QueueRemover: q, QueueSender: q, Logger: slog.New(slog.NewJSONHandler(logs, nil))}}
		session := &V2Session{interactive: true}
		mgr.handleDequeueMessage(session, protocol.Envelope{Payload: json.RawMessage(raw)})
		mgr.handleSendQueuedNow(session, []byte(`{"payload":`+raw+`}`))
		if len(q.Snapshot("conv")) != 1 || writes != 0 {
			t.Fatal("malformed bound payload acted")
		}
		if strings.Contains(logs.String(), "item_id") {
			t.Fatal("malformed binding logged")
		}
	}
}
