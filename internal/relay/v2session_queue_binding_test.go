package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestQueueCommandItemBinding(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{protocol.TypeDequeueMessage, protocol.TypeSendQueuedNow} {
		for _, value := range []string{"", "null", "0", `""`, `"untrusted-binding-marker"`, "-1", "1.0", "1e0", "true", "[]", "{}", "18446744073709551616", `"` + strings.Repeat("x", 1000) + `"`, "100", "205", "9007199254740991", "9007199254740992", "18446744073709551615"} {
			t.Run(typ+"/"+value[:min(30, len(value))], func(t *testing.T) {
				logs := &lockedBuffer{}
				changes, writes := 0, 0
				q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil },
					OnChange: func(string) { changes++ }, SendNow: func(context.Context, string, uint64, []byte) error { writes++; return nil }})
				if err != nil {
					t.Fatal(err)
				}
				id := q.Enqueue("conv", "queued")
				itemID := uint64(205)
				if parsed, err := strconv.ParseUint(value, 10, 64); err == nil && parsed > 205 {
					itemID = parsed
				}
				q.BindAcceptedItem("conv", id, itemID)
				mgr := &V2SessionManager{cfg: V2SessionConfig{QueueRemover: q, QueueSender: q, Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}}
				payload := `{"conversation_id":"conv","queued_msg_id":1`
				if value != "" {
					payload += `,"accepted_item_id":` + value
				}
				env := protocol.Envelope{Type: typ, Payload: json.RawMessage(payload + `}`)}
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
				if value == "" || value == "205" || value == "9007199254740991" {
					want = 0
				}
				if len(q.Snapshot("conv")) != want || changes != 2-want {
					t.Fatal("binding validation mutated wrong entry")
				}
				wantWrites := 0
				if want == 0 && typ == protocol.TypeSendQueuedNow {
					wantWrites = 1
				}
				if writes != wantWrites {
					t.Fatalf("writes = %d, want %d", writes, wantWrites)
				}
				if strings.Contains(logs.String(), "accepted_item_id") || strings.Contains(logs.String(), "untrusted-binding-marker") {
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
	env := protocol.Envelope{Payload: json.RawMessage(`{"conversation_id":"conv","queued_msg_id":1,"accepted_item_id":100}`)}
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
	for _, typ := range []string{protocol.TypeDequeueMessage, protocol.TypeSendQueuedNow} {
		for _, raw := range []string{
			`{"conversation_id":"conv","queued_msg_id":1,"accepted_item_id":100`,
			`{"conversation_id":"conv","queued_msg_id":1,"accepted_item_id":"unterminated}`,
			`{"conversation_id":false,"queued_msg_id":1,"accepted_item_id":100}`,
			`{"conversation_id":"conv","queued_msg_id":"1","accepted_item_id":100}`,
			`{"conversation_id":"conv","queued_msg_id":1,"queued_msg_id":"bad"}`,
			`{"conversation_id":"conv","conversation_id":false,"queued_msg_id":1}`,
		} {
			t.Run(typ+"/"+raw, func(t *testing.T) {
				writes, changes, terminal := 0, 0, 0
				q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil },
					SendNow:    func(context.Context, string, uint64, []byte) error { writes++; return nil },
					OnChange:   func(string) { changes++ },
					OnTerminal: func(string, msgqueue.QueuedMessage, msgqueue.TerminalOutcome) { terminal++ }})
				if err != nil {
					t.Fatal(err)
				}
				q.BindAcceptedItem("conv", q.Enqueue("conv", "queued"), 100)
				changes = 0
				logs := &lockedBuffer{}
				mgr := &V2SessionManager{cfg: V2SessionConfig{QueueRemover: q, QueueSender: q, Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}}
				session := &V2Session{interactive: true}
				if typ == protocol.TypeDequeueMessage {
					mgr.handleDequeueMessage(session, protocol.Envelope{Payload: json.RawMessage(raw)})
				} else {
					mgr.handleSendQueuedNow(session, []byte(`{"payload":`+raw+`}`))
				}
				if len(q.Snapshot("conv")) != 1 || writes != 0 || changes != 0 || terminal != 0 {
					t.Fatalf("malformed payload acted: writes=%d changes=%d terminal=%d", writes, changes, terminal)
				}
				if logs.String() != "" {
					t.Fatal("malformed payload logged")
				}
			})
		}
	}
}

func TestQueueItemBindingRange(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		raw   string
		id    uint64
		valid bool
	}{
		{"", 0, true},
		{"1", 1, true},
		{"9007199254740991", 9007199254740991, true},
		{"9007199254740992", 0, false},
		{"18446744073709551615", 0, false},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			id, valid := queueItemBinding(json.RawMessage(tt.raw))
			if id != tt.id || valid != tt.valid {
				t.Fatalf("binding = (%d, %t), want (%d, %t)", id, valid, tt.id, tt.valid)
			}
		})
	}
}
