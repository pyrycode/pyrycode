package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
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

// frame builds a RoutingEnvelope with the given conn_id and inner envelope.
func frame(t *testing.T, connID string, env protocol.Envelope) protocol.RoutingEnvelope {
	t.Helper()
	return protocol.RoutingEnvelope{ConnID: connID, Frame: mustEncode(t, env)}
}

// runDispatcher starts d.Run in a goroutine and returns a function that
// cancels & waits for clean exit (assertion-failing if shutdown takes
// longer than 2s).
func runDispatcher(t *testing.T, d *Dispatcher) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("Run did not return within 2s after cancel")
		}
	}
	return ctx, stop
}

func TestEmptyTable_UnsupportedType(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := New(Config{Frames: in, Logger: testLogger()})
	_, stop := runDispatcher(t, d)
	defer stop()

	req := protocol.Envelope{ID: 7, Type: protocol.TypeSendMessage, TS: time.Now().UTC()}
	in <- frame(t, "conn-a", req)

	select {
	case out := <-d.Outbound():
		if out.ConnID != "conn-a" {
			t.Errorf("ConnID: got %q, want conn-a", out.ConnID)
		}
		inner, payload := decodeError(t, out)
		if payload.Code != protocol.CodeProtocolUnsupported {
			t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeProtocolUnsupported)
		}
		if inner.InReplyTo == nil || *inner.InReplyTo != 7 {
			t.Errorf("InReplyTo: got %v, want pointer to 7", inner.InReplyTo)
		}
		if inner.ID != 1 {
			t.Errorf("inner.ID: got %d, want 1", inner.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("no outbound frame within 1s")
	}
}

func TestUnknownType(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := New(Config{Frames: in, Logger: testLogger()})
	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "c", protocol.Envelope{ID: 3, Type: "bogus", TS: time.Now().UTC()})
	out := <-d.Outbound()
	_, payload := decodeError(t, out)
	if payload.Code != protocol.CodeProtocolUnknownType {
		t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeProtocolUnknownType)
	}
}

func TestEncryptedRefusal(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := New(Config{Frames: in, Logger: testLogger()})
	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "c", protocol.Envelope{
		ID:               5,
		Type:             protocol.TypeSendMessage,
		TS:               time.Now().UTC(),
		PayloadEncrypted: true,
	})
	out := <-d.Outbound()
	inner, payload := decodeError(t, out)
	if payload.Code != protocol.CodeProtocolUnsupported {
		t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeProtocolUnsupported)
	}
	if inner.InReplyTo == nil || *inner.InReplyTo != 5 {
		t.Errorf("InReplyTo: got %v, want pointer to 5", inner.InReplyTo)
	}
}

func TestMalformedInnerFrame(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := New(Config{Frames: in, Logger: testLogger()})
	_, stop := runDispatcher(t, d)
	defer stop()

	in <- protocol.RoutingEnvelope{ConnID: "c", Frame: json.RawMessage("not json")}
	out := <-d.Outbound()
	inner, payload := decodeError(t, out)
	if payload.Code != protocol.CodeProtocolMalformed {
		t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeProtocolMalformed)
	}
	if inner.InReplyTo != nil {
		t.Errorf("InReplyTo: got %v, want nil (no request id available)", inner.InReplyTo)
	}
}

func TestIDCounter_MonotonicPerConn(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 2)
	d := New(Config{Frames: in, Logger: testLogger()})

	var (
		mu      sync.Mutex
		seenAID []uint64
		seenBID []uint64
		hWait   sync.WaitGroup
	)
	hWait.Add(2)

	d.Register(protocol.TypeSendMessage, func(ctx context.Context, c *Conn, env protocol.Envelope) error {
		defer hWait.Done()
		ids := []uint64{c.NextID(), c.NextID(), c.NextID(), c.NextID()}
		mu.Lock()
		switch c.ConnID() {
		case "conn-a":
			seenAID = ids
		case "conn-b":
			seenBID = ids
		}
		mu.Unlock()
		return nil
	})

	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "conn-a", protocol.Envelope{ID: 1, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	in <- frame(t, "conn-b", protocol.Envelope{ID: 1, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})

	waitOrFail(t, &hWait, time.Second, "handlers did not run")

	want := []uint64{1, 2, 3, 4}
	if !equalIDs(seenAID, want) {
		t.Errorf("conn-a ids: got %v, want %v", seenAID, want)
	}
	if !equalIDs(seenBID, want) {
		t.Errorf("conn-b ids: got %v, want %v (per-conn counter)", seenBID, want)
	}
}

func TestReply_InReplyToMatchesRequest(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := New(Config{Frames: in, Logger: testLogger()})

	d.Register(protocol.TypeSendMessage, func(ctx context.Context, c *Conn, env protocol.Envelope) error {
		return c.Reply(ctx, env, protocol.TypeMessage, mustEncode(t, map[string]string{"hi": "there"}))
	})

	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "c", protocol.Envelope{ID: 42, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})

	select {
	case out := <-d.Outbound():
		var inner protocol.Envelope
		if err := json.Unmarshal(out.Frame, &inner); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if inner.Type != protocol.TypeMessage {
			t.Errorf("Type: got %q, want %q", inner.Type, protocol.TypeMessage)
		}
		if inner.InReplyTo == nil || *inner.InReplyTo != 42 {
			t.Errorf("InReplyTo: got %v, want pointer to 42", inner.InReplyTo)
		}
		if inner.ID != 1 {
			t.Errorf("inner.ID: got %d, want 1", inner.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("no reply within 1s")
	}
}

func TestCtxCancel_Teardown(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := New(Config{Frames: in, Logger: testLogger()})

	var entered sync.WaitGroup
	entered.Add(1)
	enterOnce := sync.Once{}

	d.Register(protocol.TypeSendMessage, func(ctx context.Context, c *Conn, env protocol.Envelope) error {
		enterOnce.Do(entered.Done)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- d.Run(ctx) }()

	// Drain Outbound concurrently to avoid blocking the handler.
	outDrained := make(chan struct{})
	go func() {
		defer close(outDrained)
		for range d.Outbound() {
		}
	}()

	in <- frame(t, "conn-x", protocol.Envelope{ID: 1, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	waitOrFail(t, &entered, time.Second, "handler did not run before cancel")

	cancel()
	select {
	case err := <-runDone:
		if err == nil {
			t.Errorf("Run returned nil on ctx cancel, want ctx.Err")
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancel")
	}
	select {
	case <-outDrained:
	case <-time.After(time.Second):
		t.Fatal("Outbound did not close within 1s after Run return")
	}
}

func TestFramesClose_Teardown(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := New(Config{Frames: in, Logger: testLogger()})

	runDone := make(chan error, 1)
	go func() { runDone <- d.Run(context.Background()) }()

	outDrained := make(chan struct{})
	go func() {
		defer close(outDrained)
		for range d.Outbound() {
		}
	}()

	close(in)
	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run on Frames close: got %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of Frames close")
	}
	select {
	case <-outDrained:
	case <-time.After(time.Second):
		t.Fatal("Outbound did not close within 1s")
	}
}

func TestTwoConns_ArrivalOrderPreservedPerConn(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 4)
	d := New(Config{Frames: in, Logger: testLogger()})

	var (
		mu     sync.Mutex
		seenA  []uint64
		seenB  []uint64
		hWait  sync.WaitGroup
	)
	hWait.Add(4)

	d.Register(protocol.TypeSendMessage, func(ctx context.Context, c *Conn, env protocol.Envelope) error {
		defer hWait.Done()
		mu.Lock()
		switch c.ConnID() {
		case "conn-a":
			seenA = append(seenA, env.ID)
		case "conn-b":
			seenB = append(seenB, env.ID)
		}
		mu.Unlock()
		return nil
	})

	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "conn-a", protocol.Envelope{ID: 10, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	in <- frame(t, "conn-b", protocol.Envelope{ID: 20, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	in <- frame(t, "conn-a", protocol.Envelope{ID: 11, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	in <- frame(t, "conn-b", protocol.Envelope{ID: 21, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})

	waitOrFail(t, &hWait, time.Second, "not all handlers ran")

	if !equalIDs(seenA, []uint64{10, 11}) {
		t.Errorf("conn-a order: got %v, want [10 11]", seenA)
	}
	if !equalIDs(seenB, []uint64{20, 21}) {
		t.Errorf("conn-b order: got %v, want [20 21]", seenB)
	}
}

func TestDispatcher_ActiveConns_Snapshot(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 4)
	d := New(Config{Frames: in, Logger: testLogger()})

	var hWait sync.WaitGroup
	hWait.Add(2)
	d.Register(protocol.TypeSendMessage, func(ctx context.Context, c *Conn, env protocol.Envelope) error {
		hWait.Done()
		return nil
	})

	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "conn-a", protocol.Envelope{ID: 10, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})
	in <- frame(t, "conn-b", protocol.Envelope{ID: 20, Type: protocol.TypeSendMessage, TS: time.Now().UTC()})

	// Wait until both handlers ran. The no-gate path sets
	// gateCompleted=true under d.mu before invoking the handler, so by
	// the time hWait fires the broadcast-eligibility flag is published
	// and visible to ActiveConns.
	waitOrFail(t, &hWait, time.Second, "handlers did not both run")

	conns := d.ActiveConns()
	if len(conns) != 2 {
		t.Fatalf("ActiveConns len: got %d, want 2 (got=%v)", len(conns), connIDs(conns))
	}
	got := map[string]bool{}
	for _, c := range conns {
		got[c.ConnID()] = true
	}
	if !got["conn-a"] || !got["conn-b"] {
		t.Errorf("ActiveConns ids: got %v, want both conn-a and conn-b", got)
	}
}

// TestDispatcher_ActiveConns_ExcludesPreGateConn is the regression test
// for the broadcast/hello_ack race the gateStarted/gateCompleted split
// closes. It configures a FirstFrame gate that blocks until the test
// signals release. While the gate is blocked, the per-conn goroutine
// has entered runGate and set gateStarted=true, but gateCompleted is
// still false (it is set ONLY after runGate's accept-path NextID()
// advance returns). ActiveConns must exclude the conn in that window
// — otherwise a broadcast would call c.NextID() before the gate, claim
// id=1, and collide on the wire with the gate's literal-id=1 hello_ack.
//
// A single-flag implementation that set the flag at the TOP of the
// gate path (gateRun=true before runGate) would fail this test by
// returning the conn in the first assertion.
func TestDispatcher_ActiveConns_ExcludesPreGateConn(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope, 1)
	gateEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	gate := func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome {
		close(gateEntered)
		<-releaseGate
		return FirstFrameOutcome{Response: helloAckResponse(t, env.ConnID, 1)}
	}
	d := New(Config{Frames: in, Logger: testLogger(), FirstFrame: gate})
	_, stop := runDispatcher(t, d)
	defer stop()

	in <- frame(t, "c-pre", protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC()})

	// Wait until the per-conn goroutine has entered runGate. At this
	// point d.conns has an entry for "c-pre" with gateStarted=true and
	// gateCompleted=false. ActiveConns must NOT surface this conn.
	select {
	case <-gateEntered:
	case <-time.After(time.Second):
		t.Fatal("gate did not run within 1s")
	}
	if conns := d.ActiveConns(); len(conns) != 0 {
		t.Fatalf("ActiveConns during in-flight gate: got %d, want 0 (ids=%v)",
			len(conns), connIDs(conns))
	}

	// Release the gate. After runGate publishes hello_ack and advances
	// the per-conn id counter, runConn sets gateCompleted=true under
	// d.mu; ActiveConns then surfaces the conn.
	close(releaseGate)
	select {
	case out := <-d.Outbound():
		var inner protocol.Envelope
		if err := json.Unmarshal(out.Frame, &inner); err != nil {
			t.Fatalf("decode hello_ack: %v", err)
		}
		if inner.Type != protocol.TypeHelloAck {
			t.Fatalf("Type: got %q, want %q", inner.Type, protocol.TypeHelloAck)
		}
	case <-time.After(time.Second):
		t.Fatal("no hello_ack within 1s after gate release")
	}

	// runConn sets gateCompleted=true after runGate returns; this is
	// racy with the Outbound observation above (the channel send happens
	// inside runGate, the flag write happens in the caller). Poll
	// briefly for the publish to land.
	deadline := time.Now().Add(time.Second)
	var conns []*Conn
	for time.Now().Before(deadline) {
		conns = d.ActiveConns()
		if len(conns) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(conns) != 1 || conns[0].ConnID() != "c-pre" {
		t.Fatalf("ActiveConns after gate accept: got %v, want [c-pre]", connIDs(conns))
	}
}

func connIDs(cs []*Conn) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ConnID())
	}
	return out
}

func TestRegister_AfterRunPanics(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope)
	d := New(Config{Frames: in, Logger: testLogger()})
	_, stop := runDispatcher(t, d)
	defer stop()

	// Run is started in a goroutine; wait until it has flipped started
	// before exercising Register. Otherwise the test races with the
	// goroutine scheduler.
	deadline := time.Now().Add(time.Second)
	for !d.started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Run did not flip started within 1s")
		}
		time.Sleep(time.Millisecond)
	}

	// Register must panic to prevent a data race on the handlers map
	// (read lock-free in the dispatch path).
	defer func() {
		if r := recover(); r == nil {
			t.Error("Register: expected panic when called after Run, got none")
		}
	}()
	d.Register(protocol.TypeSendMessage, func(context.Context, *Conn, protocol.Envelope) error { return nil })
}

func TestRegister_DuplicatePanics(t *testing.T) {
	t.Parallel()
	in := make(chan protocol.RoutingEnvelope)
	d := New(Config{Frames: in, Logger: testLogger()})
	d.Register(protocol.TypeSendMessage, func(context.Context, *Conn, protocol.Envelope) error { return nil })
	defer func() {
		if r := recover(); r == nil {
			t.Error("Register: expected panic on duplicate type, got none")
		}
	}()
	d.Register(protocol.TypeSendMessage, func(context.Context, *Conn, protocol.Envelope) error { return nil })
}

func TestNew_NilFramesPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("New: expected panic on nil Frames")
		}
	}()
	New(Config{Logger: testLogger()})
}

func TestNew_NilLoggerPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("New: expected panic on nil Logger")
		}
	}()
	New(Config{Frames: make(chan protocol.RoutingEnvelope)})
}

// --- helpers ---

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

func waitOrFail(t *testing.T, wg *sync.WaitGroup, d time.Duration, msg string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s (waited %s)", msg, d)
	}
}
