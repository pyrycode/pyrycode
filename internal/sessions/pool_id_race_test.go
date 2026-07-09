package sessions

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestPool_RotateID_IDRace drives Pool.RotateID (the sole post-construction
// writer of Session.id) concurrently with a lifecycle-goroutine id read on the
// SAME live session. It is the -race regression guard for #866: before the fix,
// RotateID wrote sess.id under Pool.mu only while the eviction-transition notify
// (session.go PreviousID: s.id) and the idle-eviction warn log (session.go
// "session_id") read it with no lock, a torn-read data race on a two-word
// string header. Both those reader sites now route through s.currentID(), so
// hammering ID()->currentID() here exercises the exact lcMu guard they depend on.
//
// The test asserts synchronisation, not a value: the id legitimately flip-flops
// as the writer ping-pongs, so there is nothing to compare. Its subject is the
// absence of a -race report.
//
// Non-vacuity (AC #1): revert EITHER production guard and re-run -race to see the
// report — (a) move `sess.id = newID` back outside RotateID's lcMu section, or
// (b) make currentID()/ID() a bare `return s.id`. Reverting either side alone
// fires a data race on Session.id, proving the two-lock write and the lcMu read
// are jointly load-bearing.
func TestPool_RotateID_IDRace(t *testing.T) {
	t.Parallel()
	pool := helperPoolPersistent(t, filepath.Join(t.TempDir(), "sessions.json"))
	sess := pool.Default()
	if sess == nil {
		t.Fatal("Default() = nil")
	}

	id0 := sess.ID()
	id1, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if id1 == id0 {
		t.Fatalf("NewID collided with bootstrap id %q", id0)
	}

	// RotateID re-keys the map in place, so `sess` stays the live *Session
	// pointer across every rotation; the writer is the sole rotator, so the
	// oldID it passes is always the current live id.
	const iterations = 300

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		a, b := id0, id1
		for i := 0; i < iterations; i++ {
			if err := pool.RotateID(a, b); err != nil {
				t.Errorf("RotateID(%q, %q): %v", a, b, err)
				return
			}
			a, b = b, a
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = sess.ID()
		}
	}()

	wg.Wait()
}
