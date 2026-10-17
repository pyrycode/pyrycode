package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestConnSenderReply(t *testing.T) {
	want := errors.New("transport refused")
	called := false
	c := NewConnWithSender("peer", func(ctx context.Context, wire protocol.RoutingEnvelope) error {
		called = true
		var env protocol.Envelope
		if err := json.Unmarshal(wire.Frame, &env); err != nil {
			t.Fatal(err)
		}
		if wire.ConnID != "peer" || env.ID != 1 || env.Type != protocol.TypeAck || env.InReplyTo == nil || *env.InReplyTo != 42 || env.TS.IsZero() || string(env.Payload) != `{"ok":true}` {
			t.Fatalf("reply: %+v / %+v", wire, env)
		}
		return want
	}, nil)
	err := c.Reply(t.Context(), protocol.Envelope{ID: 42}, protocol.TypeAck, json.RawMessage(`{"ok":true}`))
	if !called || !errors.Is(err, want) {
		t.Fatalf("sender completion: called=%v err=%v", called, err)
	}
}
