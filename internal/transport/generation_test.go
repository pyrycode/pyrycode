package transport

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type testAdmissionContext struct {
	context.Context
	once     sync.Once
	observed chan struct{}
}

func (c *testAdmissionContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}
func testGenerationTake[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("generation barrier timeout")
		var zero T
		return zero
	}
}
func TestGenerationAdmission(t *testing.T) {
	r := newTestRelay(t)
	accepted := make(chan struct{}, 8)
	c := New(Config{URL: r.URL(), Logger: slog.New(testSendContextLog{Handler: testLogger(t).Handler(), accepted: accepted})})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()
	t.Cleanup(func() { cancel(); _ = c.Close(); testGenerationTake(t, done) })
	testGenerationTake(t, c.Connected())
	g := c.Generation()
	if g == nil {
		t.Fatal("missing generation")
	}
	if err := New(Config{Logger: testLogger(t)}).SendGeneration(ctx, g, nil); !errors.Is(err, ErrNotConnected) {
		t.Fatal("foreign generation accepted")
	}
	writer, err := g.conn.Writer(ctx, websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	written := make(chan error, 1)
	go func() { written <- c.SendGeneration(ctx, g, []byte("blocked")) }()
	testGenerationTake(t, accepted)
	admission := &testAdmissionContext{Context: ctx, observed: make(chan struct{})}
	req := sendRequest{frame: []byte("must-not-migrate"), ctx: ctx, generation: g, done: make(chan error, 1)}
	queued := make(chan error, 1)
	go func() { queued <- c.enqueueSend(admission, req) }()
	testGenerationTake(t, admission.observed)
	c.DropConn()
	if err := testGenerationTake(t, written); err == nil {
		t.Fatal("retired write succeeded")
	}
	if err := testGenerationTake(t, queued); err == nil {
		if err := testGenerationTake(t, req.done); err == nil {
			t.Fatal("old request migrated")
		}
	}
	testGenerationTake(t, c.Connected())
	if err := c.SendGeneration(ctx, g, []byte("old")); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("stale send: %v", err)
	}
	g2 := c.Generation()
	if g2 == nil || g2 == g {
		t.Fatal("generation reused")
	}
	if err := c.SendGeneration(ctx, g2, []byte("new")); err != nil {
		t.Fatal(err)
	}
	recvCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	raw, owner, err := c.ReceiveGeneration(recvCtx)
	if err != nil || string(raw) != "new" || owner != g2 {
		t.Fatal("receive identity/order or migration failure")
	}
	c.DropConn()
	testGenerationTake(t, c.Connected())
	if c.Generation() == g2 {
		t.Fatal("second replacement reused generation")
	}
}

func TestGenerationReceiveOwnership(t *testing.T) {
	r := newTestRelay(t)
	c := New(Config{URL: r.URL(), Logger: testLogger(t)})
	// A buffered receive seam holds actual pump output across replacement.
	c.recvCh = make(chan receivedFrame, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()
	t.Cleanup(func() { cancel(); _ = c.Close(); testGenerationTake(t, done) })
	testGenerationTake(t, c.Connected())
	g := c.Generation()
	if err := c.SendGeneration(ctx, g, []byte(`{"push_wake_result":{"request_id":"wake-1","code":"invalid_token"}}`)); err != nil {
		t.Fatal(err)
	}
	held := testGenerationTake(t, c.recvCh)
	if held.generation != g {
		t.Fatal("pump did not stamp original generation")
	}
	c.DropConn()
	testGenerationTake(t, c.Connected())
	c.recvCh <- held
	recvCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	raw, owner, err := c.ReceiveGeneration(recvCtx)
	if err != nil || owner != g || owner == c.Generation() || string(raw) != string(held.data) {
		t.Fatal("receive reassigned old socket ownership")
	}
	select {
	case <-owner.Context().Done():
	default:
		t.Fatal("old owner still live")
	}
}
