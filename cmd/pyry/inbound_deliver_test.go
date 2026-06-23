package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// gatingWriter is a handlers.TurnWriter that models "claude busy mid-turn": its
// WriteUserTurn blocks on the release channel until the test lets one turn
// commit, then records the delivered text in order. It signals delivery start on
// entered and commit on completed so tests synchronise without sleeps. Activate
// is a no-op (the session is already live on the unit path). Closing release up
// front makes every delivery commit immediately (claude idle).
type gatingWriter struct {
	mu        sync.Mutex
	delivered []string
	release   chan struct{}
	entered   chan string
	completed chan string
}

func newGatingWriter() *gatingWriter {
	return &gatingWriter{
		release:   make(chan struct{}),
		entered:   make(chan string, 16),
		completed: make(chan string, 16),
	}
}

func (w *gatingWriter) Activate(ctx context.Context) error { return nil }

func (w *gatingWriter) WriteUserTurn(ctx context.Context, convID string, payload []byte) error {
	text := string(payload)
	w.entered <- text
	select {
	case <-w.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	w.mu.Lock()
	w.delivered = append(w.delivered, text)
	w.mu.Unlock()
	w.completed <- text
	return nil
}

func (w *gatingWriter) deliveredOrder() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.delivered...)
}

func inboundTestLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func recvStringWithin(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

// TestInboundDeliver_EnqueueWhileBusy_OrderedDrain is the live-wiring proof: a
// real msgqueue.Queue wired through newInboundDeliver to a gating writer. Three
// messages enqueued while a turn is in flight are held behind the busy head and
// drain in enqueue order, one at a time (never more than one in flight) as each
// turn is released — the #704 behaviour, now live (AC#2/AC#4).
func TestInboundDeliver_EnqueueWhileBusy_OrderedDrain(t *testing.T) {
	t.Parallel()
	w := newGatingWriter()
	resolve := func(string) (handlers.TurnWriter, error) { return w, nil }

	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(resolve),
		Logger:  inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- q.Run(ctx) }()

	// Enqueue three to one conversation. The gate is closed, so the head enters
	// delivery and blocks; the other two stay queued behind it.
	q.Enqueue("conv", "m1")
	q.Enqueue("conv", "m2")
	q.Enqueue("conv", "m3")

	if got := recvStringWithin(t, w.entered, "first delivery entry"); got != "m1" {
		t.Fatalf("first entered = %q, want m1", got)
	}
	// Single-in-flight: nothing else enters delivery while the head is gated.
	select {
	case got := <-w.entered:
		t.Fatalf("a second delivery entered before the gate opened: %q", got)
	case <-time.After(50 * time.Millisecond):
	}

	// Release one turn at a time → in-order commit, one at a time.
	for _, want := range []string{"m1", "m2", "m3"} {
		w.release <- struct{}{}
		if got := recvStringWithin(t, w.completed, "commit"); got != want {
			t.Fatalf("commit = %q, want %q", got, want)
		}
	}

	if got := w.deliveredOrder(); len(got) != 3 || got[0] != "m1" || got[1] != "m2" || got[2] != "m3" {
		t.Errorf("delivered order = %v, want [m1 m2 m3]", got)
	}

	cancel()
	if err := recvErrWithin(t, runDone, "queue Run exit"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// TestInboundDeliver_Idle_DrainsPromptly covers the idle path: with the gate
// open (claude idle, every delivery commits immediately), a single enqueue
// drains without any further signal.
func TestInboundDeliver_Idle_DrainsPromptly(t *testing.T) {
	t.Parallel()
	w := newGatingWriter()
	close(w.release) // gate open: deliveries commit immediately
	resolve := func(string) (handlers.TurnWriter, error) { return w, nil }

	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(resolve),
		Logger:  inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- q.Run(ctx) }()

	q.Enqueue("conv", "only")
	if got := recvStringWithin(t, w.completed, "commit"); got != "only" {
		t.Fatalf("commit = %q, want only", got)
	}

	cancel()
	if err := recvErrWithin(t, runDone, "queue Run exit"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

func recvErrWithin(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}
