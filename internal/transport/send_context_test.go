package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestClientSendContext(t *testing.T) {
	c := New(Config{Logger: testLogger(t)})
	if err := c.SendContext(t.Context(), nil); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("before connect: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.SendContext(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.SendContext(t.Context(), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestClientSendContextDelivery(t *testing.T) {
	r := newTestRelay(t)
	c := New(Config{URL: r.URL(), Logger: testLogger(t)})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
		<-done
	})
	select {
	case <-c.Connected():
	case <-time.After(3 * time.Second):
		t.Fatal("connection did not establish")
	}
	if err := c.SendContext(t.Context(), []byte("context delivery")); err != nil {
		t.Fatal(err)
	}
	receiveCtx, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	if frame, err := c.Receive(receiveCtx); err != nil || string(frame) != "context delivery" {
		t.Fatalf("delivery: %q %v", frame, err)
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	writer, err := conn.Writer(t.Context(), websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	// Owning the socket writer blocks acquisition without filling OS buffers.
	// SendContext must finish while this writer remains held.
	blockedCtx, stopBlocked := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer stopBlocked()
	returned := make(chan error, 1)
	go func() { returned <- c.SendContext(blockedCtx, []byte("retired")) }()
	select {
	case err := <-returned:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked writer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("send did not retire before writer release")
	}
}
