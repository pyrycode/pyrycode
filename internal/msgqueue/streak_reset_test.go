package msgqueue

import (
	"context"
	"errors"
	"testing"
	"time"
)

// #1485: the give-up streak belongs to one head. With backlog [A,B], delivery
// always failing, and A dequeued between two of its retries after it has failed
// for a third of the bound, B must get its own full GiveUpAfter window. On main
// the streak survives the head change, so B is abandoned about 150ms into its
// own retries, measured against A's failure window.
//
// The removal fires from Config.Pending, which runs after the drain's `dropped`
// read and before its retry sleep, with no delivery in flight: exactly the state
// of a Remove landing during the sleep, reached without any timing assumption. A
// Remove landing during the delivery would instead be caught by `dropped`, which
// resets the streak on main too.
func TestQueue_GiveUp_HeadChangedBetweenAttempts_FreshStreak(t *testing.T) {
	t.Parallel()
	const (
		giveUpAfter = 300 * time.Millisecond
		retry       = 50 * time.Millisecond // A fails 3 times, ~100ms, before it is dequeued
	)
	d := newHeadDeliver(errWedged)
	// Every attempt sends on entered and nothing reads it here; widen the buffer
	// past the ~10 attempts this row makes so the double never blocks the drain.
	d.entered = make(chan string, 64)

	var (
		q          *Queue // captured by Pending below; set before Run spawns the drain.
		calls      int    // Pending runs only on the drain goroutine: no synchronisation.
		idA        uint64
		bFirstFail time.Time // written by the drain before OnGiveUp; read after Run joins it.
	)
	gaveUp := make(chan time.Time, 4)
	q, err := New(Config{
		Deliver:       d.deliver,
		RetryInterval: retry,
		GiveUpAfter:   giveUpAfter,
		OnGiveUp:      func(string, string) { gaveUp <- time.Now() },
		Pending: func(error) bool {
			calls++
			switch calls {
			case 3:
				if !q.Remove("c", idA) {
					t.Errorf("Remove(A) = false; a head between attempts must be droppable")
				}
			case 4:
				// B's first failure. Recorded before the drain starts B's streak, so
				// it is a lower bound on the streak start.
				bFirstFail = time.Now()
			}
			return false // never a legitimate hold: every failure counts toward the bound
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idA = q.Enqueue("c", "A")
	q.Enqueue("c", "B")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	var gaveUpAt time.Time
	select {
	case gaveUpAt = <-gaveUp:
	case <-time.After(5 * time.Second):
		t.Fatal("OnGiveUp never fired for B")
	}
	// giveUp clears draining before firing the seam, so the abandoned drain has
	// already released the FIFO.
	waitSnapshotEmpty(t, q, "c")

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if calls < 4 || bFirstFail.IsZero() {
		t.Fatalf("give-up fired after %d failures, before B ever failed; A must not be abandoned once dequeued", calls)
	}
	if own := gaveUpAt.Sub(bFirstFail); own < giveUpAfter {
		t.Fatalf("B abandoned %v after its own first failure, want >= GiveUpAfter %v (the streak must restart when the head changes)", own, giveUpAfter)
	}
	select {
	case <-gaveUp:
		t.Fatal("OnGiveUp fired more than once")
	default:
	}
	if order := d.deliveredOrder(); len(order) != 0 {
		t.Fatalf("delivered = %v, want none (delivery never succeeds)", order)
	}
}
