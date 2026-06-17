package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// transitionRecorder is a TransitionObserver that appends every observed
// SessionTransition under its own mutex. Fires arrive from different
// goroutines (lifecycle goroutine for eviction, caller/watcher goroutine for
// clear), so the mutex is load-bearing under -race.
type transitionRecorder struct {
	mu  sync.Mutex
	got []SessionTransition
}

func (r *transitionRecorder) observe(tr SessionTransition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, tr)
}

func (r *transitionRecorder) snapshot() []SessionTransition {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SessionTransition, len(r.got))
	copy(out, r.got)
	return out
}

func (r *transitionRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

// TestPool_TransitionObserver_ClearFiresOnRotate: a /clear rotation routed
// through onRotate fires one ReasonClear signal carrying the old/new ids and a
// non-zero occurred-at, and the underlying RotateID actually rotates the entry.
func TestPool_TransitionObserver_ClearFiresOnRotate(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	oldID := pool.Default().ID()
	newID := SessionID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")

	before := time.Now().UTC()
	if err := pool.onRotate(oldID, newID); err != nil {
		t.Fatalf("onRotate: %v", err)
	}

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("observer fired %d times, want 1: %+v", len(got), got)
	}
	tr := got[0]
	if tr.Reason != ReasonClear {
		t.Errorf("Reason = %q, want %q", tr.Reason, ReasonClear)
	}
	if tr.PreviousID != oldID {
		t.Errorf("PreviousID = %q, want %q", tr.PreviousID, oldID)
	}
	if tr.NewID != newID {
		t.Errorf("NewID = %q, want %q", tr.NewID, newID)
	}
	if tr.OccurredAt.Before(before) || tr.OccurredAt.IsZero() {
		t.Errorf("OccurredAt = %v, want >= %v and non-zero", tr.OccurredAt, before)
	}

	// RotateID's existing behaviour is unchanged: the entry moved from oldID
	// to newID in the registry/in-memory map.
	if _, err := pool.Lookup(oldID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(oldID) err = %v, want ErrSessionNotFound (entry should have moved)", err)
	}
	if _, err := pool.Lookup(newID); err != nil {
		t.Errorf("Lookup(newID) err = %v, want nil (entry should be present)", err)
	}
}

// TestPool_OnRotate_UnknownIDNoSignal: onRotate against an unknown id returns
// ErrSessionNotFound and fires no signal (a failed rotation emits nothing).
func TestPool_OnRotate_UnknownIDNoSignal(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	unknown := SessionID("ffffffff-ffff-4fff-8fff-ffffffffffff")
	err := pool.onRotate(unknown, SessionID("11111111-1111-4111-8111-111111111111"))
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("onRotate(unknown) err = %v, want ErrSessionNotFound", err)
	}
	if n := rec.len(); n != 0 {
		t.Errorf("observer fired %d times on unknown-id rotation, want 0", n)
	}
}

// TestPool_TransitionObserver_IdleEvictionFires: an idle eviction fires one
// ReasonEviction signal carrying the evicted id and an empty NewID; the
// subsequent ctx-cancel shutdown fires nothing more.
func TestPool_TransitionObserver_IdleEvictionFires(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 100*time.Millisecond)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	sess := pool.Default()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatalf("session did not evict within 2s; state=%v", sess.LifecycleState())
	}

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("observer fired %d times, want 1: %+v", len(got), got)
	}
	tr := got[0]
	if tr.Reason != ReasonEviction {
		t.Errorf("Reason = %q, want %q", tr.Reason, ReasonEviction)
	}
	if tr.PreviousID != sess.ID() {
		t.Errorf("PreviousID = %q, want %q", tr.PreviousID, sess.ID())
	}
	if tr.NewID != "" {
		t.Errorf("NewID = %q, want empty (eviction has no successor)", tr.NewID)
	}
	if tr.OccurredAt.IsZero() {
		t.Errorf("OccurredAt is zero, want non-zero")
	}

	// Shutting the session down (ctx cancel from runEvicted) returns ctx.Err()
	// from Run and must not fire a second signal.
	cancel()
	time.Sleep(50 * time.Millisecond)
	if n := rec.len(); n != 1 {
		t.Errorf("observer fired %d times total, want 1 (shutdown must not signal)", n)
	}
}

// TestPool_TransitionObserver_CapEvictionFires: a cap-policy LRU eviction fires
// one ReasonEviction signal for the evicted peer. Mirrors
// TestPool_ActiveCap_BindsAndEvictsLRU's setup.
func TestPool_TransitionObserver_CapEvictionFires(t *testing.T) {
	t.Parallel()
	pool := helperPoolCap(t, 2)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = pool.Default().Run(ctx) }()

	sessA := pool.Default()
	idB := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	idC := SessionID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	sessB := addCapTestSession(t, pool, ctx, idB)
	sessC := addCapTestSession(t, pool, ctx, idC)

	// Touch A first so it is the LRU peer once B is also active.
	if err := pool.Activate(ctx, sessA.ID()); err != nil {
		t.Fatalf("Activate(A): %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := pool.Activate(ctx, idB); err != nil {
		t.Fatalf("Activate(B): %v", err)
	}
	if !pollUntil(t, 2*time.Second, func() bool {
		return sessB.LifecycleState() == stateActive
	}) {
		t.Fatalf("B did not become active; state=%v", sessB.LifecycleState())
	}
	time.Sleep(10 * time.Millisecond)

	// Activate C — exceeds cap=2; A (LRU) is evicted.
	if err := pool.Activate(ctx, idC); err != nil {
		t.Fatalf("Activate(C): %v", err)
	}
	if !pollUntil(t, 2*time.Second, func() bool {
		return sessA.LifecycleState() == stateEvicted &&
			sessC.LifecycleState() == stateActive
	}) {
		t.Fatalf("after Activate(C): A=%v C=%v, want A=evicted C=active",
			sessA.LifecycleState(), sessC.LifecycleState())
	}

	// Exactly one eviction signal, for A.
	if !pollUntil(t, 1*time.Second, func() bool { return rec.len() >= 1 }) {
		t.Fatal("no eviction signal observed")
	}
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("observer fired %d times, want 1: %+v", len(got), got)
	}
	tr := got[0]
	if tr.Reason != ReasonEviction {
		t.Errorf("Reason = %q, want %q", tr.Reason, ReasonEviction)
	}
	if tr.PreviousID != sessA.ID() {
		t.Errorf("PreviousID = %q, want %q (evicted peer A)", tr.PreviousID, sessA.ID())
	}
	if tr.NewID != "" {
		t.Errorf("NewID = %q, want empty", tr.NewID)
	}
}

// TestPool_TransitionObserver_NilIsNoOp: with no observer wired, rotation and
// idle eviction behave exactly as today — no panic, the rotation moves the
// entry, and the session still evicts.
func TestPool_TransitionObserver_NilIsNoOp(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 100*time.Millisecond)
	// Deliberately no SetTransitionObserver call.

	// Rotation still rotates with a nil observer.
	oldID := pool.Default().ID()
	newID := SessionID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	if err := pool.onRotate(oldID, newID); err != nil {
		t.Fatalf("onRotate (nil observer): %v", err)
	}
	if _, err := pool.Lookup(newID); err != nil {
		t.Errorf("Lookup(newID) after nil-observer rotate: %v, want nil", err)
	}

	// Idle eviction still fires with a nil observer.
	sess := pool.Default()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatalf("nil observer altered idle eviction; state=%v", sess.LifecycleState())
	}
}

// TestPool_TransitionObserver_RaceConcurrentFires drives the lifecycle fire
// site (an idle eviction of the bootstrap) simultaneously with N watcher-style
// onRotate fires from separate goroutines. Run under -race, this proves the
// lock-free transitionObserver read is safe under concurrent reads and the
// recorder's own mutex keeps it race-free under concurrent writes.
func TestPool_TransitionObserver_RaceConcurrentFires(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 80*time.Millisecond)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const N = 6
	oldIDs := make([]SessionID, N)
	newIDs := make([]SessionID, N)
	for i := 0; i < N; i++ {
		id := SessionID(fmt.Sprintf("%08x-0000-4000-8000-%012x", i+1, i+1))
		oldIDs[i] = addCapTestSession(t, pool, ctx, id).ID()
		newIDs[i] = SessionID(fmt.Sprintf("%08x-1111-4111-8111-%012x", i+1, i+1))
	}

	// Bootstrap lifecycle goroutine — fires one eviction signal once it idles.
	go func() { _ = pool.Default().Run(ctx) }()

	// N concurrent rotations — each fires one clear signal.
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := pool.onRotate(oldIDs[i], newIDs[i]); err != nil {
				t.Errorf("onRotate(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if !pollUntil(t, 2*time.Second, func() bool {
		return pool.Default().LifecycleState() == stateEvicted
	}) {
		t.Fatalf("bootstrap did not idle-evict; state=%v", pool.Default().LifecycleState())
	}

	if !pollUntil(t, 2*time.Second, func() bool { return rec.len() >= N+1 }) {
		t.Fatalf("observed %d signals, want %d (N clears + 1 eviction)", rec.len(), N+1)
	}

	var clears, evictions int
	for _, tr := range rec.snapshot() {
		switch tr.Reason {
		case ReasonClear:
			clears++
		case ReasonEviction:
			evictions++
		default:
			t.Errorf("unexpected reason %q", tr.Reason)
		}
	}
	if clears != N {
		t.Errorf("clear signals = %d, want %d", clears, N)
	}
	if evictions != 1 {
		t.Errorf("eviction signals = %d, want 1", evictions)
	}
}
