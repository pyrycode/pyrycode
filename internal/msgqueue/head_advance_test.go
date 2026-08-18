package msgqueue

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// errWedged is the forced delivery failure the give-up rows inject: a claude
// session that never commits a turn, so the drain walks its retry loop out to
// the give-up bound. A sentinel of its own so this file shares no state with
// queue_test.go's fakeDeliver (the #935 coexistence rule).
var errWedged = errors.New("wedged delivery failure")

// giveUpWarnMsg is the message Queue.giveUp logs when it abandons a head. The
// give-up rows assert this line never appears for a head the user dequeued.
// They deliberately do NOT assert on the retry warning ("delivery failed, will
// retry"), which also names the head id: it fires before the drain can know the
// head was dequeued, and is out of scope here.
const giveUpWarnMsg = "giving up on head after persistent delivery failure"

// headDeliver is the DeliverFunc double these rows share. It records every
// payload it accepts, signals each attempt and each success on a channel, and
// can be switched into (and out of) permanent failure. onDeliver runs inside the
// seam — where q.mu is released — so a row can call back into the queue from
// there: that is how the success rows dequeue their own in-flight head, exactly
// as a phone's dequeue_message does while a delivery is in flight.
type headDeliver struct {
	// onDeliver runs at the top of every delivery. Set before Run starts the
	// drain and never mutated afterwards, so the drain goroutine reads it safely.
	onDeliver func(convID string)

	entered chan string // text, sent at the top of every delivery attempt
	done    chan string // text, sent after every successful delivery

	mu       sync.Mutex
	failWith error
	order    []string
}

func newHeadDeliver(failWith error) *headDeliver {
	return &headDeliver{
		entered:  make(chan string, 16),
		done:     make(chan string, 16),
		failWith: failWith,
	}
}

// setFail switches the forced failure on (non-nil) or off (nil).
func (d *headDeliver) setFail(err error) {
	d.mu.Lock()
	d.failWith = err
	d.mu.Unlock()
}

func (d *headDeliver) deliver(_ context.Context, convID string, payload []byte) error {
	text := string(payload)
	d.entered <- text
	if d.onDeliver != nil {
		d.onDeliver(convID)
	}

	d.mu.Lock()
	err := d.failWith
	if err == nil {
		d.order = append(d.order, text)
	}
	d.mu.Unlock()

	if err != nil {
		return err
	}
	d.done <- text
	return nil
}

func (d *headDeliver) deliveredOrder() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.order...)
}

// waitDrainExited polls until convID's drain has cleared draining — its clean
// empty-exit. It is the synchronisation point for the two rows that leave the
// FIFO empty: those rows have no delivery left to observe, and OnGiveUp is the
// very event they assert never fires, so the flag the drain clears on its way
// out is the only signal that happens-after the advance under test. The caller
// must first observe a delivery attempt: draining is also false before Run has
// spawned the drain at all.
func waitDrainExited(t *testing.T, q *Queue, convID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		c := q.convs[convID]
		exited := c == nil || !c.draining
		q.mu.Unlock()
		if exited {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("conversation %q drain never exited", convID)
}

// assertNoGiveUp fails if the give-up seam fired or the give-up warning was
// logged. The caller must have joined the drains (Run returned) first, so both
// reads see everything the drain goroutine wrote.
func assertNoGiveUp(t *testing.T, gaveUp <-chan string, logs string) {
	t.Helper()
	select {
	case reason := <-gaveUp:
		t.Fatalf("OnGiveUp fired for a head the user dequeued (reason %q); a cancelled head must not be reported as abandoned", reason)
	default:
	}
	if strings.Contains(logs, giveUpWarnMsg) {
		t.Fatalf("give-up warning logged for a head the user dequeued: %q", logs)
	}
}

// #1484 AC-1: a delivery that succeeds after its own message was dequeued must
// not splice the FIFO. With backlog [A,B] and A removed during A's in-flight
// delivery, the advance is a no-op and B — the message queued behind it — is
// still delivered. On main advanceLocked drops index 0 unconditionally, so B is
// spliced away, never attempted and never reported.
//
// This row is the sole detector of a half-guard that tests only that the FIFO is
// non-empty: with items == [B] the length test passes and B is dropped exactly
// as on main, while the single-message row below goes green. The two rows must
// stay separate.
func TestQueue_SuccessAdvance_HeadDequeuedMidDelivery_KeepsNext(t *testing.T) {
	t.Parallel()
	d := newHeadDeliver(nil)

	var q *Queue // captured by the Deliver hook below; set before Run spawns the drain.
	q, err := New(Config{Deliver: d.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Enqueue before Run so the ids exist before any drain does: the hook reads
	// idA from the drain goroutine.
	idA := q.Enqueue("c", "A")
	q.Enqueue("c", "B")
	var once sync.Once
	d.onDeliver = func(convID string) {
		// The phone cancels A while A's own delivery is in flight — the seam holds
		// no lock, so the removal lands — and the delivery still reports success.
		once.Do(func() {
			if !q.Remove(convID, idA) {
				t.Errorf("Remove(A) = false; a head that is only waiting for claude must be droppable (#487)")
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	if got := recvWithin(t, d.done, "delivery of A"); got != "A" {
		t.Fatalf("first delivery = %q, want A", got)
	}
	if got := recvWithin(t, d.done, "delivery of B"); got != "B" {
		t.Fatalf("second delivery = %q, want B (the message behind a dequeued in-flight head must still be delivered)", got)
	}
	waitSnapshotEmpty(t, q, "c")
	if order := d.deliveredOrder(); !equalStrings(order, []string{"A", "B"}) {
		t.Fatalf("delivered = %v, want [A B]", order)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #1484 AC-2: the same success-path situation with a single queued message must
// not panic. On main shrinkLocked has already set items = nil when the removal
// emptied the FIFO, so the unconditional items[1:] is out of range and the whole
// daemon dies. The drain must instead reach its clean empty-exit, and a message
// enqueued afterwards must still be delivered — the positive signal that the
// drain (and the process) is alive, which the absence of a panic alone is not.
//
// This row is the sole detector of a half-guard that tests only the head id:
// with items == nil the comparison itself panics before it can return false,
// while the [A,B] row above goes green.
func TestQueue_SuccessAdvance_HeadDequeuedMidDelivery_LastMessageNoPanic(t *testing.T) {
	t.Parallel()
	d := newHeadDeliver(nil)

	var q *Queue // captured by the Deliver hook below; set before Run spawns the drain.
	q, err := New(Config{Deliver: d.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	idA := q.Enqueue("c", "A")
	var once sync.Once
	d.onDeliver = func(convID string) {
		once.Do(func() {
			if !q.Remove(convID, idA) {
				t.Errorf("Remove(A) = false; a head that is only waiting for claude must be droppable (#487)")
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	if got := recvWithin(t, d.done, "delivery of A"); got != "A" {
		t.Fatalf("first delivery = %q, want A", got)
	}
	// The advance runs after that delivery returns, on an empty FIFO. Waiting for
	// the exit keeps the follow-up Enqueue strictly after it, so the advance
	// really does face items == nil.
	waitDrainExited(t, q, "c")

	q.Enqueue("c", "after")
	if got := recvWithin(t, d.done, "delivery after the cancelled head"); got != "after" {
		t.Fatalf("delivered %q, want after (the drain must survive a dequeued last head)", got)
	}
	waitSnapshotEmpty(t, q, "c")
	if order := d.deliveredOrder(); !equalStrings(order, []string{"A", "after"}) {
		t.Fatalf("delivered = %v, want [A after]", order)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// giveUpRow builds a queue whose delivery is wedged and whose Pending predicate
// dequeues the head from inside the give-up window. Config.Pending is the only
// test-controllable code between the drain's post-delivery `dropped` read and
// giveUp's advance, so it is where both give-up rows drive the interleaving —
// no sleeps, no goroutine-timing assumptions.
//
// The removal fires on the SECOND failure. The first only starts the give-up
// clock (elapsed is ~zero, far inside the bound), and RetryInterval is
// deliberately wider than GiveUpAfter, so the second failure is the one whose
// elapsed window crosses the bound and reaches giveUp.
func giveUpRow(t *testing.T, buf *bytes.Buffer, gaveUp chan<- string) (*Queue, *headDeliver) {
	t.Helper()
	const (
		giveUpAfter = 50 * time.Millisecond
		retry       = 250 * time.Millisecond // > giveUpAfter: the second failure crosses the bound
	)
	d := newHeadDeliver(errWedged)

	var q *Queue // captured by Pending below; set before Run spawns the drain.
	pendingCalls := 0
	q, err := New(Config{
		Deliver:       d.deliver,
		RetryInterval: retry,
		GiveUpAfter:   giveUpAfter,
		Logger:        slog.New(slog.NewTextHandler(buf, nil)),
		OnGiveUp:      func(convID, reason string) { gaveUp <- reason },
		Pending: func(error) bool {
			// Called only from the drain goroutine, so pendingCalls needs no
			// synchronisation.
			pendingCalls++
			if pendingCalls == 2 {
				if !q.Remove("c", 1) { // id 1 is the first message this conversation minted
					t.Errorf("Remove(head) = false; a head that is only waiting for claude must be droppable (#487)")
				}
				d.setFail(nil) // the wedge clears, so whatever survives can drain
			}
			return false // never a legitimate hold: every failure counts toward the bound
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return q, d
}

// #1484 AC-3, single-message row: a head dequeued inside the give-up window is
// cancelled, not abandoned. With backlog [A] the drain must not panic (on main
// advanceLocked runs items[1:] on the nil slice the removal left behind), must
// report no give-up — neither the seam nor the warning — and must still deliver
// a message enqueued afterwards.
func TestQueue_GiveUpAdvance_HeadDequeuedInWindow_LastMessageNoPanicNoReport(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	gaveUp := make(chan string, 4)
	q, d := giveUpRow(t, &buf, gaveUp)

	q.Enqueue("c", "A")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	recvWithin(t, d.entered, "first delivery attempt")
	// The give-up advance faces an empty FIFO only if nothing is enqueued before
	// it runs; the drain's exit is what happens-after it.
	waitDrainExited(t, q, "c")

	q.Enqueue("c", "after")
	if got := recvWithin(t, d.done, "delivery after the cancelled head"); got != "after" {
		t.Fatalf("delivered %q, want after (the drain must survive a head dequeued in the give-up window)", got)
	}

	// Joining the drains before reading the recorder and the log buffer makes both
	// reads race-clean and complete, the same happens-before
	// TestQueue_GiveUp_NoUntrustedContentLeak relies on.
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	assertNoGiveUp(t, gaveUp, buf.String())
	if order := d.deliveredOrder(); !equalStrings(order, []string{"after"}) {
		t.Fatalf("delivered = %v, want [after] (the cancelled head is never delivered)", order)
	}
}

// #1484 AC-3, two-message row: the give-up advance must not splice a FIFO it no
// longer owns. With backlog [A,B] and A dequeued inside the give-up window, B
// stays queued and drains, and no give-up is reported for A. On main giveUp
// drops B instead and abandons it silently, then exits the drain.
func TestQueue_GiveUpAdvance_HeadDequeuedInWindow_KeepsNext(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	gaveUp := make(chan string, 4)
	q, d := giveUpRow(t, &buf, gaveUp)

	q.Enqueue("c", "A")
	q.Enqueue("c", "B")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	// B's delivery happens-after the give-up advance on the same drain goroutine:
	// the drain reaches B only by continuing past a give-up that abandoned
	// nothing.
	if got := recvWithin(t, d.done, "delivery of B"); got != "B" {
		t.Fatalf("delivered %q, want B (the message behind a dequeued head must stay queued and drain)", got)
	}
	waitSnapshotEmpty(t, q, "c")

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	assertNoGiveUp(t, gaveUp, buf.String())
	if order := d.deliveredOrder(); !equalStrings(order, []string{"B"}) {
		t.Fatalf("delivered = %v, want [B] (the cancelled head is never delivered)", order)
	}
}
