package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// mustEncode marshals env or fails the test.
func mustEncode(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// decodeError parses out as protocol.Envelope + ErrorPayload.
func decodeError(t *testing.T, out protocol.RoutingEnvelope) (protocol.Envelope, protocol.ErrorPayload) {
	t.Helper()
	var inner protocol.Envelope
	if err := json.Unmarshal(out.Frame, &inner); err != nil {
		t.Fatalf("decode inner envelope: %v", err)
	}
	if inner.Type != protocol.TypeError {
		t.Fatalf("inner.Type: got %q, want %q", inner.Type, protocol.TypeError)
	}
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(inner.Payload, &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	return inner, payload
}

func equalIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMalformedInnerFrame(t *testing.T) {
	t.Parallel()

	outbound := make(chan protocol.RoutingEnvelope, 1)
	conn := NewConn("c", outbound, nil)
	Route(context.Background(), testLogger(), conn, nil, json.RawMessage("not json"))

	select {
	case out := <-outbound:
		inner, payload := decodeError(t, out)
		if payload.Code != protocol.CodeProtocolMalformed {
			t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeProtocolMalformed)
		}
		if inner.InReplyTo != nil {
			t.Errorf("InReplyTo: got %v, want nil (no request id available)", inner.InReplyTo)
		}
	case <-time.After(time.Second):
		t.Fatal("no outbound frame within 1s")
	}
}

func TestUnknownType(t *testing.T) {
	t.Parallel()

	outbound := make(chan protocol.RoutingEnvelope, 1)
	conn := NewConn("c", outbound, nil)
	frame := mustEncode(t, protocol.Envelope{ID: 3, Type: "bogus", TS: time.Now().UTC()})
	Route(context.Background(), testLogger(), conn, nil, frame)

	select {
	case out := <-outbound:
		_, payload := decodeError(t, out)
		if payload.Code != protocol.CodeProtocolUnknownType {
			t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeProtocolUnknownType)
		}
	case <-time.After(time.Second):
		t.Fatal("no outbound frame within 1s")
	}
}

func TestEncryptedRefusal(t *testing.T) {
	t.Parallel()

	outbound := make(chan protocol.RoutingEnvelope, 1)
	conn := NewConn("c", outbound, nil)
	frame := mustEncode(t, protocol.Envelope{
		ID:               5,
		Type:             protocol.TypeSendMessage,
		TS:               time.Now().UTC(),
		PayloadEncrypted: true,
	})
	Route(context.Background(), testLogger(), conn, nil, frame)

	select {
	case out := <-outbound:
		inner, payload := decodeError(t, out)
		if payload.Code != protocol.CodeProtocolUnsupported {
			t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeProtocolUnsupported)
		}
		if inner.InReplyTo == nil || *inner.InReplyTo != 5 {
			t.Errorf("InReplyTo: got %v, want pointer to 5", inner.InReplyTo)
		}
	case <-time.After(time.Second):
		t.Fatal("no outbound frame within 1s")
	}
}

// TestIDCounter_MonotonicPerConn pins per-conn NextID monotonicity and
// per-conn isolation: each Conn owns an independent counter, so a handler
// calling NextID four times on two separate conns sees [1,2,3,4] on each.
func TestIDCounter_MonotonicPerConn(t *testing.T) {
	t.Parallel()

	var seenAID, seenBID []uint64
	handlers := map[string]Handler{
		protocol.TypeSendMessage: func(ctx context.Context, c *Conn, env protocol.Envelope) error {
			ids := []uint64{c.NextID(), c.NextID(), c.NextID(), c.NextID()}
			switch c.ConnID() {
			case "conn-a":
				seenAID = ids
			case "conn-b":
				seenBID = ids
			}
			return nil
		},
	}

	connA := NewConn("conn-a", make(chan protocol.RoutingEnvelope, 1), nil)
	connB := NewConn("conn-b", make(chan protocol.RoutingEnvelope, 1), nil)

	frame := mustEncode(t, protocol.Envelope{ID: 1, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	Route(context.Background(), testLogger(), connA, handlers, frame)
	Route(context.Background(), testLogger(), connB, handlers, frame)

	want := []uint64{1, 2, 3, 4}
	if !equalIDs(seenAID, want) {
		t.Errorf("conn-a ids: got %v, want %v", seenAID, want)
	}
	if !equalIDs(seenBID, want) {
		t.Errorf("conn-b ids: got %v, want %v (per-conn counter)", seenBID, want)
	}
}

// TestRoute_StandaloneInvocation exercises the externally-exposed Route
// + NewConn surfaces used by callers that own their own per-conn
// goroutine (the v2 session manager). Registers a handler that replies
// via c.Reply, calls Route directly, and asserts the outbound channel
// receives the reply with InReplyTo set to the request id.
func TestRoute_StandaloneInvocation(t *testing.T) {
	t.Parallel()

	outbound := make(chan protocol.RoutingEnvelope, 4)
	conn := NewConn("conn-route", outbound, nil)

	const reqID uint64 = 42
	replied := make(chan struct{})
	handlers := map[string]Handler{
		protocol.TypeSendMessage: func(ctx context.Context, c *Conn, env protocol.Envelope) error {
			defer close(replied)
			return c.Reply(ctx, env, protocol.TypeMessage, mustEncode(t, map[string]string{"hi": "there"}))
		},
	}

	frame := mustEncode(t, protocol.Envelope{ID: reqID, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	Route(context.Background(), testLogger(), conn, handlers, frame)
	select {
	case <-replied:
	case <-time.After(time.Second):
		t.Fatal("handler did not run")
	}

	select {
	case out := <-outbound:
		var inner protocol.Envelope
		if err := json.Unmarshal(out.Frame, &inner); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		if inner.Type != protocol.TypeMessage {
			t.Errorf("inner.Type = %q, want %q", inner.Type, protocol.TypeMessage)
		}
		if inner.InReplyTo == nil || *inner.InReplyTo != reqID {
			t.Errorf("InReplyTo = %v, want pointer to %d", inner.InReplyTo, reqID)
		}
	case <-time.After(time.Second):
		t.Fatal("no reply enqueued on outbound")
	}
}

// TestRoute_NoHandler_UnsupportedReply verifies the no-handler branch
// emits a sealed protocol.unsupported error envelope and does not panic
// on a nil handlers map.
func TestRoute_NoHandler_UnsupportedReply(t *testing.T) {
	t.Parallel()

	outbound := make(chan protocol.RoutingEnvelope, 1)
	conn := NewConn("c", outbound, nil)
	frame := mustEncode(t, protocol.Envelope{ID: 5, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	Route(context.Background(), testLogger(), conn, nil, frame)

	select {
	case out := <-outbound:
		inner, payload := decodeError(t, out)
		if payload.Code != protocol.CodeProtocolUnsupported {
			t.Errorf("code = %q, want %q", payload.Code, protocol.CodeProtocolUnsupported)
		}
		if inner.InReplyTo == nil || *inner.InReplyTo != 5 {
			t.Errorf("InReplyTo = %v, want pointer to 5", inner.InReplyTo)
		}
	case <-time.After(time.Second):
		t.Fatal("no error reply enqueued")
	}
}
