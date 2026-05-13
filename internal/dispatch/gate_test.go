package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// helloAckResponse builds a routing envelope shaped like the relay's
// hello_ack response for a given conn_id/hello id (id=1, in_reply_to set).
func helloAckResponse(t *testing.T, connID string, helloID uint64) protocol.RoutingEnvelope {
	t.Helper()
	payload := mustEncode(t, protocol.HelloAckPayload{
		ProtocolVersion: "v1",
		ServerID:        "test-server",
		ConnID:          connID,
	})
	inner := protocol.Envelope{
		ID:        1,
		Type:      protocol.TypeHelloAck,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &helloID,
	}
	return protocol.RoutingEnvelope{ConnID: connID, Frame: mustEncode(t, inner)}
}

// authErrorResponse builds the routing envelope shaped like the relay's
// reject response (TypeError, code auth.invalid_token).
func authErrorResponse(t *testing.T, connID string, helloID uint64) protocol.RoutingEnvelope {
	t.Helper()
	payload := mustEncode(t, protocol.ErrorPayload{
		Code:    protocol.CodeAuthInvalidToken,
		Message: "device token not recognised",
	})
	inner := protocol.Envelope{
		ID:        1,
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &helloID,
	}
	return protocol.RoutingEnvelope{ConnID: connID, Frame: mustEncode(t, inner)}
}

func TestFirstFrameGate_Accept(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 2)
	var gateCalls atomic.Int32
	gate := func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome {
		gateCalls.Add(1)
		return FirstFrameOutcome{Response: helloAckResponse(t, env.ConnID, 1)}
	}
	d := New(Config{Frames: in, Logger: testLogger(), FirstFrame: gate})
	_, stop := runDispatcher(t, d)
	defer stop()

	// Frame 1: hello — gate runs; ack returned.
	in <- frame(t, "c-1", protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC()})
	select {
	case out := <-d.Outbound():
		var inner protocol.Envelope
		if err := json.Unmarshal(out.Frame, &inner); err != nil {
			t.Fatalf("decode inner: %v", err)
		}
		if inner.Type != protocol.TypeHelloAck {
			t.Errorf("Type: got %q, want %q", inner.Type, protocol.TypeHelloAck)
		}
		if inner.ID != 1 {
			t.Errorf("inner.ID: got %d, want 1", inner.ID)
		}
		if inner.InReplyTo == nil || *inner.InReplyTo != 1 {
			t.Errorf("InReplyTo: got %v, want pointer to 1", inner.InReplyTo)
		}
		if out.CloseCode != 0 {
			t.Errorf("CloseCode: got %d, want 0 (accept)", out.CloseCode)
		}
	case <-time.After(time.Second):
		t.Fatal("no outbound hello_ack within 1s")
	}

	// Frame 2: send_message — gate must NOT run; falls through to the
	// empty handler table and returns protocol.unsupported.
	in <- frame(t, "c-1", protocol.Envelope{ID: 7, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	select {
	case out := <-d.Outbound():
		_, payload := decodeError(t, out)
		if payload.Code != protocol.CodeProtocolUnsupported {
			t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeProtocolUnsupported)
		}
	case <-time.After(time.Second):
		t.Fatal("no second outbound within 1s")
	}

	if got := gateCalls.Load(); got != 1 {
		t.Errorf("gate calls: got %d, want 1 (gate must run once per conn)", got)
	}
}

func TestFirstFrameGate_Reject(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 2)
	var gateCalls atomic.Int32
	gate := func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome {
		gateCalls.Add(1)
		return FirstFrameOutcome{
			Response:  authErrorResponse(t, env.ConnID, 1),
			CloseConn: true,
			Code:      4401,
		}
	}
	d := New(Config{Frames: in, Logger: testLogger(), FirstFrame: gate})
	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "c-1", protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC()})

	select {
	case out := <-d.Outbound():
		if out.CloseCode != 4401 {
			t.Errorf("CloseCode: got %d, want 4401", out.CloseCode)
		}
		_, payload := decodeError(t, out)
		if payload.Code != protocol.CodeAuthInvalidToken {
			t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeAuthInvalidToken)
		}
	case <-time.After(time.Second):
		t.Fatal("no reject outbound within 1s")
	}

	// Second frame for the same conn: must be dropped silently. No
	// additional outbound, no gate re-entry.
	in <- frame(t, "c-1", protocol.Envelope{ID: 2, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	select {
	case out := <-d.Outbound():
		t.Errorf("unexpected outbound after reject: %+v", out)
	case <-time.After(100 * time.Millisecond):
		// expected
	}

	if got := gateCalls.Load(); got != 1 {
		t.Errorf("gate calls: got %d, want 1 (gate must not re-run on dropped second frame)", got)
	}
}

func TestFirstFrameGate_RejectDoesNotAffectOtherConns(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 2)
	gate := func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome {
		switch env.ConnID {
		case "conn-a":
			return FirstFrameOutcome{
				Response:  authErrorResponse(t, env.ConnID, 1),
				CloseConn: true,
				Code:      4401,
			}
		default:
			return FirstFrameOutcome{Response: helloAckResponse(t, env.ConnID, 1)}
		}
	}
	d := New(Config{Frames: in, Logger: testLogger(), FirstFrame: gate})
	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "conn-a", protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC()})
	in <- frame(t, "conn-b", protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC()})

	seen := map[string]protocol.RoutingEnvelope{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case out := <-d.Outbound():
			seen[out.ConnID] = out
		case <-deadline:
			t.Fatalf("only saw %d outbound frames, want 2 (seen=%v)", len(seen), seen)
		}
	}
	if got := seen["conn-a"].CloseCode; got != 4401 {
		t.Errorf("conn-a CloseCode: got %d, want 4401", got)
	}
	if got := seen["conn-b"].CloseCode; got != 0 {
		t.Errorf("conn-b CloseCode: got %d, want 0 (accept)", got)
	}
}

func TestFirstFrameGate_Err(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 2)
	gate := func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome {
		return FirstFrameOutcome{Err: errors.New("test malformed")}
	}
	d := New(Config{Frames: in, Logger: testLogger(), FirstFrame: gate})

	var hWait sync.WaitGroup
	hWait.Add(1)
	d.Register(protocol.TypeSendMessage, func(ctx context.Context, c *Conn, env protocol.Envelope) error {
		defer hWait.Done()
		return c.Reply(ctx, env, protocol.TypeAck, mustEncode(t, protocol.AckPayload{}))
	})

	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "c-1", protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC()})

	// First outbound: protocol.malformed with InReplyTo nil.
	select {
	case out := <-d.Outbound():
		inner, payload := decodeError(t, out)
		if payload.Code != protocol.CodeProtocolMalformed {
			t.Errorf("first outbound code: got %q, want %q", payload.Code, protocol.CodeProtocolMalformed)
		}
		if inner.InReplyTo != nil {
			t.Errorf("first outbound InReplyTo: got %v, want nil", inner.InReplyTo)
		}
	case <-time.After(time.Second):
		t.Fatal("no malformed reply within 1s")
	}

	// Second frame goes to the handler table (gate is consumed).
	in <- frame(t, "c-1", protocol.Envelope{ID: 9, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	waitOrFail(t, &hWait, time.Second, "registered handler did not run after Err verdict")

	select {
	case out := <-d.Outbound():
		var inner protocol.Envelope
		if err := json.Unmarshal(out.Frame, &inner); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if inner.Type != protocol.TypeAck {
			t.Errorf("second outbound type: got %q, want %q", inner.Type, protocol.TypeAck)
		}
	case <-time.After(time.Second):
		t.Fatal("no ack reply within 1s")
	}
}

func TestFirstFrameGate_NilDisablesGate(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := New(Config{Frames: in, Logger: testLogger()}) // FirstFrame nil
	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "c", protocol.Envelope{ID: 7, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	select {
	case out := <-d.Outbound():
		_, payload := decodeError(t, out)
		if payload.Code != protocol.CodeProtocolUnsupported {
			t.Errorf("code: got %q, want %q (gate-nil must fall straight to handler table)",
				payload.Code, protocol.CodeProtocolUnsupported)
		}
	case <-time.After(time.Second):
		t.Fatal("no outbound within 1s")
	}
}

// Threat-model pin: a compromised relay may set CloseCode on a
// phone→binary routing envelope. The dispatcher MUST ignore it and
// still invoke the gate on the inner frame.
func TestFirstFrameGate_IgnoresInboundCloseCode(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	gateCalls := make(chan protocol.RoutingEnvelope, 1)
	gate := func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome {
		gateCalls <- env
		return FirstFrameOutcome{Response: helloAckResponse(t, env.ConnID, 1)}
	}
	d := New(Config{Frames: in, Logger: testLogger(), FirstFrame: gate})
	_, stop := runDispatcher(t, d)
	defer stop()

	hello := mustEncode(t, protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC()})
	in <- protocol.RoutingEnvelope{ConnID: "c-1", Frame: hello, CloseCode: 4401}

	select {
	case env := <-gateCalls:
		if env.ConnID != "c-1" {
			t.Errorf("gate ConnID: got %q, want c-1", env.ConnID)
		}
	case <-time.After(time.Second):
		t.Fatal("gate not invoked on inbound frame with CloseCode set")
	}
	select {
	case <-d.Outbound():
	case <-time.After(time.Second):
		t.Fatal("no outbound hello_ack after gate accept")
	}
}

func TestFirstFrameGate_ConcurrentConns(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 10)
	gate := func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome {
		return FirstFrameOutcome{Response: helloAckResponse(t, env.ConnID, 1)}
	}
	d := New(Config{Frames: in, Logger: testLogger(), FirstFrame: gate, OutboundBuffer: 16})
	_, stop := runDispatcher(t, d)
	defer stop()

	for i := 0; i < 10; i++ {
		connID := fmt.Sprintf("c-%d", i)
		in <- frame(t, connID, protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC()})
	}

	seen := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(seen) < 10 {
		select {
		case out := <-d.Outbound():
			seen[out.ConnID] = true
		case <-deadline:
			t.Fatalf("only %d/10 outbound seen: %v", len(seen), seen)
		}
	}
	for i := 0; i < 10; i++ {
		want := fmt.Sprintf("c-%d", i)
		if !seen[want] {
			t.Errorf("missing outbound for %s", want)
		}
	}
}
