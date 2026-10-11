package transport

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type testSendContextLog struct {
	slog.Handler
	accepted chan struct{}
}

func (h testSendContextLog) Enabled(context.Context, slog.Level) bool { return true }
func (h testSendContextLog) Handle(ctx context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "event" && a.Value.String() == "transport.send.accepted" {
			h.accepted <- struct{}{}
		}
		return true
	})
	return h.Handler.Handle(ctx, r)
}

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

func TestClientSendContextPumpShutdown(t *testing.T) {
	r := newTestRelay(t)
	accepted := make(chan struct{}, 1)
	logger := slog.New(testSendContextLog{Handler: testLogger(t).Handler(), accepted: accepted})
	c := New(Config{URL: r.URL(), Logger: logger, WriteTimeout: 10 * time.Second})
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
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	writer, err := conn.Writer(t.Context(), websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	returned := make(chan error, 1)
	// The caller stays live; stopping the pump must cancel its accepted write.
	go func() { returned <- c.SendContext(t.Context(), []byte("retired")) }()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("pump did not accept the request")
	}
	cancel()
	select {
	case err := <-returned:
		// The receive pump may close the socket before writer acquisition
		// observes its cancelled context. Both paths must end before release.
		if err == nil {
			t.Fatalf("pump shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted request survived pump shutdown and writer retention")
	}
}
