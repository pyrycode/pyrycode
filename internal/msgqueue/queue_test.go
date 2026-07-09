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

// #869 AC #1, #2, #5: Enqueue caps the per-conversation backlog. With the cap's
// worth already queued, a further Enqueue returns 0 without appending; the
// existing backlog (ids + order) is untouched and the rejected message never
// appears in Snapshot.
func TestQueue_CapEnforcedAtEnqueue_RejectNeverDrops(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver, MaxQueuedPerConversation: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Below the cap Enqueue behaves as today: appends, returns a dense id >= 1.
	if id := q.Enqueue("a", "m1"); id != 1 {
		t.Fatalf("Enqueue m1 id = %d, want 1", id)
	}
	if id := q.Enqueue("a", "m2"); id != 2 {
		t.Fatalf("Enqueue m2 id = %d, want 2", id)
	}
	// At the cap the third Enqueue is rejected: returns 0, appends nothing.
	if id := q.Enqueue("a", "m3"); id != 0 {
		t.Fatalf("Enqueue m3 at cap id = %d, want 0 (rejected)", id)
	}

	// Reject, never drop: the backlog still holds exactly the two accepted
	// messages in enqueue order; the rejected one never entered it, and the
	// oldest was not evicted to make room.
	snap := q.Snapshot("a")
	if len(snap) != 2 {
		t.Fatalf("Snapshot len = %d, want 2 (backlog untouched by reject)", len(snap))
	}
	if snap[0].ID != 1 || snap[0].Text != "m1" || snap[1].ID != 2 || snap[1].Text != "m2" {
		t.Fatalf("Snapshot = %+v, want ids [1 2] texts [m1 m2] in order", snap)
	}
}

// #869 AC #2, #5: after a rejected Enqueue, the two accepted messages still
// drain in enqueue order and the rejected one is never delivered. Filling the
// backlog before Run keeps it full while we fill (nothing drains yet).
func TestQueue_RejectedMessage_NeverDrains(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver, MaxQueuedPerConversation: 2, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("a", "m1")
	q.Enqueue("a", "m2")
	if id := q.Enqueue("a", "m3"); id != 0 {
		t.Fatalf("Enqueue m3 at cap id = %d, want 0 (rejected)", id)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	for _, want := range []string{"m1", "m2"} {
		if got := recvWithin(t, f.completed, "delivery of "+want); got != want {
			t.Fatalf("delivered %q, want %q", got, want)
		}
	}
	if got := f.deliveredOrder(); !equalStrings(got, []string{"m1", "m2"}) {
		t.Fatalf("delivery order = %v, want [m1 m2] (rejected m3 never delivered)", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #869 AC #4: the cap is per conversation. Filling A to the cap so it rejects
// does not affect B — B enqueues normally from its own independent counter.
func TestQueue_CapIsPerConversation(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver, MaxQueuedPerConversation: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("a", "a1")
	q.Enqueue("a", "a2")
	if id := q.Enqueue("a", "a3"); id != 0 {
		t.Fatalf("A at cap id = %d, want 0 (rejected)", id)
	}

	if id := q.Enqueue("b", "b1"); id != 1 {
		t.Fatalf("B enqueue id = %d, want 1 (independent of a filled A)", id)
	}
	if id := q.Enqueue("b", "b2"); id != 2 {
		t.Fatalf("B enqueue id = %d, want 2", id)
	}
}

// #869: a rejected Enqueue consumes no id. After a reject, freeing a slot with
// Remove lets the next Enqueue succeed and continue the DENSE id sequence — the
// rejected attempt left no gap.
func TestQueue_RejectConsumesNoID_RemoveFreesSlot(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver, MaxQueuedPerConversation: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("a", "m1") // id 1
	q.Enqueue("a", "m2") // id 2
	if id := q.Enqueue("a", "m3"); id != 0 {
		t.Fatalf("Enqueue m3 at cap id = %d, want 0 (rejected)", id)
	}

	// No drain runs (Run not started), so id 2 is a non-head, non-in-flight
	// message and is removable — this frees a backlog slot.
	if !q.Remove("a", 2) {
		t.Fatal("Remove(a, 2) = false, want true (non-head, not in flight)")
	}

	// The next Enqueue succeeds and its id is 3, not 4: the rejected m3 consumed
	// no id, so the accepted sequence stays dense.
	if id := q.Enqueue("a", "m4"); id != 3 {
		t.Fatalf("Enqueue m4 id = %d, want 3 (reject consumed no id)", id)
	}
}

// #869: an unset MaxQueuedPerConversation resolves to the default cap in New,
// and that default fires at exactly defaultMaxQueuedPerConversation — small
// backlogs (every existing scenario) never reject. This is the regression guard
// that the ~45 existing Enqueue call sites keep passing unchanged.
func TestQueue_DefaultCap_ResolvesAndFires(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver}) // MaxQueuedPerConversation unset ⇒ default
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 1; i <= defaultMaxQueuedPerConversation; i++ {
		if id := q.Enqueue("a", "m"); id != uint64(i) {
			t.Fatalf("Enqueue #%d id = %d, want %d (accepted under default cap)", i, id, i)
		}
	}
	if id := q.Enqueue("a", "over"); id != 0 {
		t.Fatalf("Enqueue past default cap id = %d, want 0 (rejected)", id)
	}
}
