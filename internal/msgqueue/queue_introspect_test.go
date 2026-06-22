package msgqueue

import (
	"context"
	"errors"
	"testing"
	"time"
)

// snapshotIDs projects a snapshot to its ids for terse order assertions.
func snapshotIDs(s []QueuedMessage) []uint64 {
	out := make([]uint64, len(s))
	for i := range s {
		out[i] = s[i].ID
	}
	return out
}

func equalUint64s(a, b []uint64) bool {
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

// AC #1: the snapshot is the conversation's not-yet-confirmed-delivered FIFO in
// enqueue order; an unknown conversation is an empty snapshot, not an error.
func TestQueue_Snapshot_ReflectsEnqueuesInOrder(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// No Run: nothing drains, so all three sit queued.
	q.Enqueue("c", "m1")
	q.Enqueue("c", "m2")
	q.Enqueue("c", "m3")

	snap := q.Snapshot("c")
	if !equalUint64s(snapshotIDs(snap), []uint64{1, 2, 3}) {
		t.Fatalf("snapshot ids = %v, want [1 2 3]", snapshotIDs(snap))
	}
	for i, want := range []string{"m1", "m2", "m3"} {
		if snap[i].Text != want {
			t.Fatalf("snapshot[%d].Text = %q, want %q", i, snap[i].Text, want)
		}
		if snap[i].TS.IsZero() {
			t.Fatalf("snapshot[%d].TS is zero, want the enqueue timestamp", i)
		}
	}

	if got := q.Snapshot("unknown"); len(got) != 0 {
		t.Fatalf("snapshot of unknown conversation = %v, want empty", got)
	}
}

// AC #1: the returned slice is a copy — mutating it cannot reach engine state.
func TestQueue_Snapshot_IsACopy(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("c", "m1")
	q.Enqueue("c", "m2")

	snap := q.Snapshot("c")
	snap[0] = QueuedMessage{ID: 999, Text: "hacked", TS: time.Now()}

	again := q.Snapshot("c")
	if !equalUint64s(snapshotIDs(again), []uint64{1, 2}) {
		t.Fatalf("second snapshot ids = %v, want [1 2] (engine state mutated through the copy)", snapshotIDs(again))
	}
	if again[0].Text != "m1" {
		t.Fatalf("second snapshot[0].Text = %q, want m1", again[0].Text)
	}
}

// AC #2: removing a queued, not-in-flight message drops it and preserves the
// surviving order, even while the head is in flight.
func TestQueue_Remove_DropsNonHeadEntry_OrderPreserved(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{}) // hold the head in flight

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m1") // becomes the in-flight head
	id2 := q.Enqueue("c", "m2")
	q.Enqueue("c", "m3")
	recvWithin(t, f.entered, "m1 to enter deliver") // head now in flight (draining)

	if !q.Remove("c", id2) {
		t.Fatal("Remove(m2) = false, want true (non-head, removable)")
	}
	if got := snapshotIDs(q.Snapshot("c")); !equalUint64s(got, []uint64{1, 3}) {
		t.Fatalf("snapshot after Remove(m2) = %v, want [1 3]", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #2: the in-flight (draining) head is non-removable; the removal no-ops and
// the head still delivers exactly once (advanceLocked is not corrupted).
func TestQueue_Remove_NoOpsOnInFlightHead(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{})

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	id1 := q.Enqueue("c", "m1") // in-flight head
	q.Enqueue("c", "m2")
	recvWithin(t, f.entered, "m1 to enter deliver")

	if q.Remove("c", id1) {
		t.Fatal("Remove(in-flight head) = true, want false (no-op)")
	}
	if got := snapshotIDs(q.Snapshot("c")); !equalUint64s(got, []uint64{1, 2}) {
		t.Fatalf("snapshot after no-op Remove = %v, want [1 2] (head still present)", got)
	}

	// Release the gate: m1 then m2 deliver, each exactly once, in order.
	f.gates["c"] <- struct{}{}
	if got := recvWithin(t, f.completed, "m1 completion"); got != "m1" {
		t.Fatalf("first completion = %q, want m1", got)
	}
	recvWithin(t, f.entered, "m2 to enter deliver")
	f.gates["c"] <- struct{}{}
	if got := recvWithin(t, f.completed, "m2 completion"); got != "m2" {
		t.Fatalf("second completion = %q, want m2", got)
	}
	if got := f.deliveredOrder(); !equalStrings(got, []string{"m1", "m2"}) {
		t.Fatalf("delivery order = %v, want [m1 m2] (exactly once each)", got)
	}
	if n := f.maxConcurrent(); n != 1 {
		t.Fatalf("max in-flight = %d, want 1", n)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #2: removing an unknown id or an unknown conversation is a safe no-op that
// leaves the backlog unchanged.
func TestQueue_Remove_NoOpsOnUnknownIDAndConv(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("c", "m1")
	q.Enqueue("c", "m2")

	if q.Remove("c", 999) {
		t.Fatal("Remove(unknown id) = true, want false")
	}
	if q.Remove("nope", 1) {
		t.Fatal("Remove(unknown conversation) = true, want false")
	}
	if got := snapshotIDs(q.Snapshot("c")); !equalUint64s(got, []uint64{1, 2}) {
		t.Fatalf("snapshot after no-op Removes = %v, want [1 2] (unchanged)", got)
	}
}

// AC #2: removing an already-delivered id is a no-op (it is gone from the FIFO).
func TestQueue_Remove_NoOpsOnAlreadyDelivered(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{})

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	id1 := q.Enqueue("c", "m1")
	q.Enqueue("c", "m2")
	recvWithin(t, f.entered, "m1 to enter deliver")

	// Let m1 commit; the drain advances past it and peeks m2, which proves m1 is
	// no longer in the FIFO.
	f.gates["c"] <- struct{}{}
	if got := recvWithin(t, f.completed, "m1 completion"); got != "m1" {
		t.Fatalf("completion = %q, want m1", got)
	}
	recvWithin(t, f.entered, "m2 to enter deliver") // m1 advanced out before this peek

	if q.Remove("c", id1) {
		t.Fatal("Remove(already-delivered id) = true, want false")
	}
	if got := snapshotIDs(q.Snapshot("c")); !equalUint64s(got, []uint64{2}) {
		t.Fatalf("snapshot = %v, want [2] (only the in-flight m2 remains)", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #3: the change seam fires on enqueue, on delivery-advance, and on a
// successful removal, each carrying the affected conversation id; a no-op Remove
// fires nothing.
func TestQueue_OnChange_FiresOnEnqueueAdvanceRemove(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{})
	notes := make(chan string, 64)

	q, err := New(Config{
		Deliver:       f.deliver,
		RetryInterval: time.Millisecond,
		OnChange:      func(id string) { notes <- id },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m1")
	if got := recvWithin(t, notes, "enqueue m1 notification"); got != "c" {
		t.Fatalf("enqueue notification = %q, want c", got)
	}
	recvWithin(t, f.entered, "m1 to enter deliver") // head in flight

	q.Enqueue("c", "m2")
	if got := recvWithin(t, notes, "enqueue m2 notification"); got != "c" {
		t.Fatalf("enqueue notification = %q, want c", got)
	}
	id3 := q.Enqueue("c", "m3")
	if got := recvWithin(t, notes, "enqueue m3 notification"); got != "c" {
		t.Fatalf("enqueue notification = %q, want c", got)
	}

	if !q.Remove("c", id3) {
		t.Fatal("Remove(m3) = false, want true")
	}
	if got := recvWithin(t, notes, "remove notification"); got != "c" {
		t.Fatalf("remove notification = %q, want c", got)
	}

	// No-op removals fire nothing.
	if q.Remove("c", 999) || q.Remove("nope", 1) {
		t.Fatal("a no-op Remove returned true")
	}
	select {
	case got := <-notes:
		t.Fatalf("no-op Remove fired a notification %q, want none", got)
	default:
	}

	// Delivery-advance fires on the confirmed drop of the head.
	f.gates["c"] <- struct{}{}
	recvWithin(t, f.completed, "m1 completion")
	if got := recvWithin(t, notes, "delivery-advance notification"); got != "c" {
		t.Fatalf("delivery-advance notification = %q, want c", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #3: the notification fires after the lock is released — an OnChange that
// re-enters Snapshot/Remove completes without deadlocking against the lock.
func TestQueue_OnChange_FiresWithoutHoldingLock(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	notes := make(chan string, 64)

	var q *Queue
	built, err := New(Config{
		Deliver: f.deliver,
		OnChange: func(id string) {
			// Re-enter the lock-taking API from inside the callback. If notify
			// fired under q.mu, these would deadlock (sync.Mutex is not reentrant).
			_ = q.Snapshot(id)
			_ = q.Remove(id, 1<<62) // unknown id ⇒ no-op, no re-notify/recursion
			notes <- id
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q = built

	// No Run needed: Enqueue fires the change seam on the caller's goroutine.
	q.Enqueue("c", "x")
	if got := recvWithin(t, notes, "re-entrant OnChange to complete"); got != "c" {
		t.Fatalf("notification = %q, want c", got)
	}
}

// AC #4: #704's invariants hold with the new API present — interleaving Enqueue,
// Snapshot, and Remove preserves ordered, lossless, one-at-a-time drain.
func TestQueue_NewAPI_PreservesInvariants(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{})

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m1") // in-flight head
	id2 := q.Enqueue("c", "m2")
	q.Enqueue("c", "m3")
	q.Enqueue("c", "m4")
	recvWithin(t, f.entered, "m1 to enter deliver")

	// Observe via Snapshot, then drop a middle message; the rest must still drain
	// in order, one at a time.
	if got := snapshotIDs(q.Snapshot("c")); !equalUint64s(got, []uint64{1, 2, 3, 4}) {
		t.Fatalf("snapshot = %v, want [1 2 3 4]", got)
	}
	if !q.Remove("c", id2) {
		t.Fatal("Remove(m2) = false, want true")
	}

	// Drain the survivors: m1 (head), then m3, m4 — m2 never delivers.
	for _, want := range []string{"m1", "m3", "m4"} {
		f.gates["c"] <- struct{}{}
		if got := recvWithin(t, f.completed, "completion of "+want); got != want {
			t.Fatalf("completion = %q, want %q", got, want)
		}
		// Pull the next peek (except after the last) so the loop stays in step.
		if want != "m4" {
			recvWithin(t, f.entered, "next deliver")
		}
	}
	if got := f.deliveredOrder(); !equalStrings(got, []string{"m1", "m3", "m4"}) {
		t.Fatalf("delivery order = %v, want [m1 m3 m4] (m2 removed)", got)
	}
	if n := f.maxConcurrent(); n != 1 {
		t.Fatalf("max in-flight = %d, want 1", n)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}
