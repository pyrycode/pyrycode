package msgqueue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// errFake is the forced delivery failure the lossless-retry scenario injects.
var errFake = errors.New("fake deliver failure")

// fakeDeliver is a test double for DeliverFunc. It records the order and
// payloads of successful deliveries, enforces that no conversation ever has more
// than one in-flight delivery (the serial-drain invariant), and can simulate
// "claude busy mid-turn" (a per-conversation gate the test releases) and "claude
// unavailable" (a per-conversation forced-failure count). It signals delivery
// start on entered and successful completion on completed so tests synchronise
// without sleeps.
type fakeDeliver struct {
	mu          sync.Mutex
	order       []string       // texts delivered successfully, in order
	inflight    map[string]int // per-conv in-flight count
	maxInflight int            // max in-flight observed across all conversations
	failTimes   map[string]int // per-conv remaining forced failures
	gates       map[string]chan struct{}

	entered   chan string // text, sent at the top of every deliver call
	completed chan string // text, sent after every successful delivery
}

func newFakeDeliver() *fakeDeliver {
	return &fakeDeliver{
		inflight:  map[string]int{},
		failTimes: map[string]int{},
		gates:     map[string]chan struct{}{},
		entered:   make(chan string, 64),
		completed: make(chan string, 64),
	}
}

func (f *fakeDeliver) deliver(ctx context.Context, convID string, payload []byte) error {
	text := string(payload)

	f.mu.Lock()
	f.inflight[convID]++
	if f.inflight[convID] > f.maxInflight {
		f.maxInflight = f.inflight[convID]
	}
	gate := f.gates[convID]
	f.mu.Unlock()

	f.entered <- text

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			f.mu.Lock()
			f.inflight[convID]--
			f.mu.Unlock()
			return ctx.Err()
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight[convID]--
	if f.failTimes[convID] > 0 {
		f.failTimes[convID]--
		return errFake
	}
	f.order = append(f.order, text)
	f.completed <- text
	return nil
}

func (f *fakeDeliver) deliveredOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

func (f *fakeDeliver) maxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInflight
}

func equalStrings(a, b []string) bool {
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

// recvWithin receives one value from ch or fails after a generous deadline so a
// hung drain fails loudly instead of blocking the suite.
func recvWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// AC #2, #3, #5: a message enqueued while a turn is in flight is held; the
// backlog drains one at a time, in enqueue order, never more than one in flight.
func TestQueue_EnqueueDuringInFlight_DrainsOrderedOneAtATime(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{}) // gate every delivery: simulate an in-flight turn

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	if id := q.Enqueue("c", "m1"); id != 1 {
		t.Fatalf("Enqueue m1 id = %d, want 1", id)
	}
	if id := q.Enqueue("c", "m2"); id != 2 {
		t.Fatalf("Enqueue m2 id = %d, want 2", id)
	}
	if id := q.Enqueue("c", "m3"); id != 3 {
		t.Fatalf("Enqueue m3 id = %d, want 3", id)
	}

	for _, want := range []string{"m1", "m2", "m3"} {
		if got := recvWithin(t, f.entered, "deliver("+want+")"); got != want {
			t.Fatalf("deliver entered for %q, want %q", got, want)
		}
		// While this delivery is gated (in flight), no later delivery may start.
		select {
		case got := <-f.entered:
			t.Fatalf("delivery for %q started while %q in flight", got, want)
		default:
		}
		f.gates["c"] <- struct{}{} // turn ends → this delivery commits
	}

	for range []string{"m1", "m2", "m3"} {
		recvWithin(t, f.completed, "completion")
	}
	if got := f.deliveredOrder(); !equalStrings(got, []string{"m1", "m2", "m3"}) {
		t.Fatalf("delivery order = %v, want [m1 m2 m3]", got)
	}
	if n := f.maxConcurrent(); n != 1 {
		t.Fatalf("max in-flight per conversation = %d, want 1", n)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #4: an empty queue drains as a no-op — Run does nothing and returns cleanly
// on cancel, with zero deliveries.
func TestQueue_EmptyQueue_NoOp(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if got := f.deliveredOrder(); len(got) != 0 {
		t.Fatalf("deliveries = %v, want none", got)
	}
}

// AC #1: queues for different conversations are independent — a conversation
// whose delivery is blocked never blocks or reorders another's.
func TestQueue_PerConversationIndependence(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["a"] = make(chan struct{}) // conv a stays blocked for the whole test

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("a", "a1") // blocked on its gate forever
	q.Enqueue("b", "b1")
	q.Enqueue("b", "b2")
	q.Enqueue("b", "b3")

	var bOrder []string
	for len(bOrder) < 3 {
		got := recvWithin(t, f.completed, "conv b completion")
		if got == "a1" {
			t.Fatalf("conv a delivered %q while gated — conversations not independent", got)
		}
		bOrder = append(bOrder, got)
	}
	if !equalStrings(bOrder, []string{"b1", "b2", "b3"}) {
		t.Fatalf("conv b delivery order = %v, want [b1 b2 b3]", bOrder)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #3: a message enqueued while claude is already idle drains promptly, with no
// external turn-end signal.
func TestQueue_IdleDrainsPromptly(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver() // no gates: claude is idle
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "only")
	if got := recvWithin(t, f.completed, "delivery"); got != "only" {
		t.Fatalf("delivered %q, want only", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #4 + restart pin: a delivery that keeps failing (claude unavailable during a
// child respawn) is retried at the FIFO head until it succeeds — delivered
// exactly once, never dropped.
func TestQueue_LosslessRetry_SurvivesRespawn(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.failTimes["c"] = 3 // first 3 attempts fail, 4th succeeds

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m")
	if got := recvWithin(t, f.completed, "eventual delivery"); got != "m" {
		t.Fatalf("delivered %q, want m", got)
	}
	if got := f.deliveredOrder(); !equalStrings(got, []string{"m"}) {
		t.Fatalf("delivery order = %v, want [m] (exactly once)", got)
	}

	// Each attempt (3 failures + 1 success) sent on entered; all are buffered by
	// the time the success completion arrives.
	attempts := 0
	for {
		select {
		case <-f.entered:
			attempts++
			continue
		default:
		}
		break
	}
	if attempts != 4 {
		t.Fatalf("deliver attempts = %d, want 4 (3 retries + success)", attempts)
	}
	if n := f.maxConcurrent(); n != 1 {
		t.Fatalf("max in-flight = %d, want 1 (retries are serial)", n)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #1: ids are stable, monotonic from 1 within a conversation, and independent
// across conversations. Enqueue assigns ids without Run.
func TestQueue_StableIndependentIDs(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if id := q.Enqueue("a", "x"); id != 1 {
		t.Fatalf("a/x id = %d, want 1", id)
	}
	if id := q.Enqueue("a", "y"); id != 2 {
		t.Fatalf("a/y id = %d, want 2", id)
	}
	if id := q.Enqueue("b", "z"); id != 1 {
		t.Fatalf("b/z id = %d, want 1 (independent counter)", id)
	}
	if id := q.Enqueue("a", "w"); id != 3 {
		t.Fatalf("a/w id = %d, want 3", id)
	}
	if id := q.Enqueue("b", "q"); id != 2 {
		t.Fatalf("b/q id = %d, want 2", id)
	}
}

// Clean shutdown: cancelling ctx while a drain is blocked in deliver makes Run
// return — wg.Wait() unblocking is the proof that every drain goroutine exited.
func TestQueue_CleanShutdown_NoLeak(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{}) // never released: the drain blocks in deliver

	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m")
	recvWithin(t, f.entered, "drain to enter deliver") // ensure the drain is blocked in deliver

	cancel()
	if err := recvWithin(t, runErr, "Run to return after cancel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// New rejects a nil delivery seam — it is caller-supplied wiring, surfaced as an
// error rather than a panic.
func TestNew_RejectsNilDeliver(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with nil Deliver returned nil error, want non-nil")
	}
}
