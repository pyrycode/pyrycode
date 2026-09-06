package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
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

// TestRotateForNewSession covers the DIRECT (daemon-driven) new_session
// rotation: it mints a fresh id, re-keys the pool entry, registers the new id in
// the allocated skip-set (so the fresh --session-id spawn's <newID>.jsonl CREATE
// does not double-rotate the watcher), rebinds the owning conversation, and fires
// exactly one ReasonClear transition — the same observable a /clear produces, but
// minted and driven directly rather than observed. An unknown oldID is inert.
func TestRotateForNewSession(t *testing.T) {
	t.Parallel()

	t.Run("bound conversation rotates to a fresh minted id", func(t *testing.T) {
		pool := helperPool(t, false)
		orig := pool.Default()
		oldID := orig.ID()

		const convID conversations.ConversationID = "11111111-2222-4333-8444-555555555555"
		reg, _ := seedBoundConvRegistry(t, pool, convID, oldID)

		rec := &transitionRecorder{}
		pool.SetTransitionObserver(rec.observe)

		newID, err := pool.RotateForNewSession(oldID)
		if err != nil {
			t.Fatalf("RotateForNewSession: %v", err)
		}
		if newID == "" || !ValidID(string(newID)) {
			t.Fatalf("newID = %q, want a fresh valid UUID", newID)
		}
		if newID == oldID {
			t.Fatalf("newID == oldID (%q); want a distinct minted id", newID)
		}

		// Re-key: oldID gone, newID resolves to the SAME session pointer.
		if _, err := pool.Lookup(oldID); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Lookup(oldID) err = %v, want ErrSessionNotFound (entry should have moved)", err)
		}
		got, err := pool.Lookup(newID)
		if err != nil {
			t.Fatalf("Lookup(newID): %v", err)
		}
		if got != orig {
			t.Errorf("Lookup(newID) returned a different session; want the same rotated entry")
		}

		// Owning conversation rebound to the new id.
		conv, ok := reg.Get(convID)
		if !ok {
			t.Fatal("Get(conv): not found")
		}
		if conv.CurrentSessionID != string(newID) {
			t.Errorf("CurrentSessionID = %q, want %q (rebound)", conv.CurrentSessionID, newID)
		}

		// Skip-set primed before the fresh spawn. IsAllocated consumes on hit, so
		// assert exactly once.
		if !pool.IsAllocated(newID) {
			t.Errorf("IsAllocated(newID) = false, want true — the minted id must be registered before the --session-id spawn or the watcher double-rotates")
		}

		// Exactly one ReasonClear transition carrying old→new.
		signals := rec.snapshot()
		if len(signals) != 1 {
			t.Fatalf("observer fired %d times, want 1: %+v", len(signals), signals)
		}
		if tr := signals[0]; tr.Reason != ReasonClear || tr.PreviousID != oldID || tr.NewID != newID {
			t.Errorf("signal = %+v, want {ReasonClear, %q, %q}", tr, oldID, newID)
		}
	})

	t.Run("unknown oldID is inert", func(t *testing.T) {
		pool := helperPool(t, false)
		oldID := pool.Default().ID()

		const convID conversations.ConversationID = "11111111-2222-4333-8444-555555555555"
		reg, path := seedBoundConvRegistry(t, pool, convID, oldID)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read pre-rotate file: %v", err)
		}

		rec := &transitionRecorder{}
		pool.SetTransitionObserver(rec.observe)

		unknown := SessionID("ffffffff-ffff-4fff-8fff-ffffffffffff")
		newID, err := pool.RotateForNewSession(unknown)
		if !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("RotateForNewSession(unknown) err = %v, want ErrSessionNotFound", err)
		}
		if newID != "" {
			t.Errorf("newID = %q, want empty on the not-found path", newID)
		}

		// No mutation: entry still at oldID, binding intact, no signal, file
		// byte-identical.
		if _, err := pool.Lookup(oldID); err != nil {
			t.Errorf("Lookup(oldID) err = %v, want nil (unchanged)", err)
		}
		conv, _ := reg.Get(convID)
		if conv.CurrentSessionID != string(oldID) {
			t.Errorf("CurrentSessionID = %q, want %q (unchanged)", conv.CurrentSessionID, oldID)
		}
		if n := rec.len(); n != 0 {
			t.Errorf("observer fired %d times on unknown-id rotation, want 0", n)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read post-rotate file: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Error("conversations.json rewritten on an unknown-id rotation; want untouched")
		}
	})
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

// seedBoundConvRegistry wires a freshly-saved conversations.json into pool,
// containing one conversation (convID) bound to boundSession. Returns the
// registry, the on-disk path, and convID for assertions.
func seedBoundConvRegistry(t *testing.T, pool *Pool, convID conversations.ConversationID, boundSession SessionID) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               convID,
		Cwd:              "/owner",
		CurrentSessionID: string(boundSession),
	})
	if err := reg.Save(path); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	pool.convReg = reg
	pool.convRegistryPath = path
	return reg, path
}

// TestPool_OnRotate_RebindsOwningConversation covers AC#1 + AC#3: a /clear
// rotation re-points the owning conversation's binding in memory, that rebind
// survives a reload from disk, and the observer fan-out still fires exactly once
// with the clear signal.
func TestPool_OnRotate_RebindsOwningConversation(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	oldID := pool.Default().ID()
	newID := SessionID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")

	const convID conversations.ConversationID = "11111111-2222-4333-8444-555555555555"
	reg, path := seedBoundConvRegistry(t, pool, convID, oldID)

	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	if err := pool.onRotate(oldID, newID); err != nil {
		t.Fatalf("onRotate: %v", err)
	}

	// In-memory rebind (AC#1).
	got, ok := reg.Get(convID)
	if !ok {
		t.Fatal("Get(conv): not found")
	}
	if got.CurrentSessionID != string(newID) {
		t.Errorf("CurrentSessionID = %q, want %q", got.CurrentSessionID, newID)
	}
	if len(got.SessionHistory) != 1 || got.SessionHistory[0] != string(oldID) {
		t.Errorf("SessionHistory = %v, want [%q]", got.SessionHistory, oldID)
	}

	// On-disk reload survives (AC#3).
	back, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	reloaded, ok := back.Get(convID)
	if !ok {
		t.Fatal("Get(conv) after reload: not found")
	}
	if reloaded.CurrentSessionID != string(newID) {
		t.Errorf("reloaded CurrentSessionID = %q, want %q", reloaded.CurrentSessionID, newID)
	}
	if len(reloaded.SessionHistory) != 1 || reloaded.SessionHistory[0] != string(oldID) {
		t.Errorf("reloaded SessionHistory = %v, want [%q]", reloaded.SessionHistory, oldID)
	}

	// Observer fan-out unchanged: still fires exactly once with the clear signal.
	signals := rec.snapshot()
	if len(signals) != 1 {
		t.Fatalf("observer fired %d times, want 1: %+v", len(signals), signals)
	}
	if tr := signals[0]; tr.Reason != ReasonClear || tr.PreviousID != oldID || tr.NewID != newID {
		t.Errorf("signal = %+v, want {ReasonClear, %q, %q}", tr, oldID, newID)
	}
}

// TestPool_OnRotate_NoOwnerNoOp covers AC#4: rotating a session no conversation
// owns mutates nothing, writes nothing (the file bytes are untouched, so Save
// was skipped), returns nil, and still fires the observer once.
func TestPool_OnRotate_NoOwnerNoOp(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	oldID := pool.Default().ID()
	newID := SessionID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")

	const (
		convID    = "11111111-2222-4333-8444-555555555555"
		otherSess = "99999999-9999-4999-8999-999999999999"
	)
	// Conversation bound to a DIFFERENT session than the one being rotated.
	reg, path := seedBoundConvRegistry(t, pool, convID, SessionID(otherSess))

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pre-rotation file: %v", err)
	}

	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	if err := pool.onRotate(oldID, newID); err != nil {
		t.Fatalf("onRotate: %v", err)
	}

	// No conversation mutated.
	got, _ := reg.Get(convID)
	if got.CurrentSessionID != otherSess {
		t.Errorf("CurrentSessionID = %q, want %q (unchanged)", got.CurrentSessionID, otherSess)
	}
	if len(got.SessionHistory) != 0 {
		t.Errorf("SessionHistory = %v, want empty (no-op)", got.SessionHistory)
	}

	// No write: Save is skipped on a miss, so the file is byte-identical.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read post-rotation file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("conversations.json was rewritten on a no-owner rotation; want untouched")
	}

	// Observer fan-out unchanged.
	if n := rec.len(); n != 1 {
		t.Errorf("observer fired %d times, want 1", n)
	}
}

// TestPool_Eviction_BindingNeutral covers AC#2: an idle eviction leaves the
// conversation binding exactly as-is — CurrentSessionID still points at the
// (still-resolvable) evicted id and no colliding history entry is appended.
// Proves the reason-branch skips eviction end-to-end.
func TestPool_Eviction_BindingNeutral(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 100*time.Millisecond)
	sess := pool.Default()
	boundID := sess.ID()

	const convID conversations.ConversationID = "11111111-2222-4333-8444-555555555555"
	reg, _ := seedBoundConvRegistry(t, pool, convID, boundID)

	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatalf("session did not evict within 2s; state=%v", sess.LifecycleState())
	}
	// Wait for the eviction signal to actually reach notifyTransition — only then
	// has the reason-branch had its chance to (wrongly) rebind, were it buggy.
	if !pollUntil(t, 1*time.Second, func() bool { return rec.len() >= 1 }) {
		t.Fatal("no eviction signal observed")
	}

	got, _ := reg.Get(convID)
	if got.CurrentSessionID != string(boundID) {
		t.Errorf("CurrentSessionID = %q, want %q (eviction must be binding-neutral)", got.CurrentSessionID, boundID)
	}
	if len(got.SessionHistory) != 0 {
		t.Errorf("SessionHistory = %v, want empty (eviction must not append a colliding id)", got.SessionHistory)
	}
}

// TestPool_OnRotate_RebindRaceConcurrentSave mirrors
// TestPool_TransitionObserver_RaceConcurrentFires but wires a registry whose
// conversations are each bound to one rotating id. Under -race it exercises the
// new edge — concurrent RebindSession + atomic Save on the same registry/path —
// alongside the lock-free observer read and the lifecycle eviction fire.
func TestPool_OnRotate_RebindRaceConcurrentSave(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 80*time.Millisecond)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const N = 6
	oldIDs := make([]SessionID, N)
	newIDs := make([]SessionID, N)
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg := &conversations.Registry{}
	for i := 0; i < N; i++ {
		id := SessionID(fmt.Sprintf("%08x-0000-4000-8000-%012x", i+1, i+1))
		oldIDs[i] = addCapTestSession(t, pool, ctx, id).ID()
		newIDs[i] = SessionID(fmt.Sprintf("%08x-1111-4111-8111-%012x", i+1, i+1))
		reg.Create(conversations.Conversation{
			ID:               conversations.ConversationID(fmt.Sprintf("%08x-2222-4222-8222-%012x", i+1, i+1)),
			Cwd:              fmt.Sprintf("/conv-%d", i),
			CurrentSessionID: string(oldIDs[i]),
		})
	}
	if err := reg.Save(path); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	pool.convReg = reg
	pool.convRegistryPath = path

	// Bootstrap lifecycle goroutine — fires one eviction signal once it idles.
	go func() { _ = pool.Default().Run(ctx) }()

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

	// Every conversation rebound to its new id; each history trail is exactly
	// [oldID].
	for i := 0; i < N; i++ {
		convID := conversations.ConversationID(fmt.Sprintf("%08x-2222-4222-8222-%012x", i+1, i+1))
		got, ok := reg.Get(convID)
		if !ok {
			t.Fatalf("Get(conv %d): not found", i)
		}
		if got.CurrentSessionID != string(newIDs[i]) {
			t.Errorf("conv %d CurrentSessionID = %q, want %q", i, got.CurrentSessionID, newIDs[i])
		}
		if len(got.SessionHistory) != 1 || got.SessionHistory[0] != string(oldIDs[i]) {
			t.Errorf("conv %d SessionHistory = %v, want [%q]", i, got.SessionHistory, oldIDs[i])
		}
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

// TestPool_AdoptAnnouncedID_RekeysAndFiresOneClear pins #2135's AC 1 pool half: an
// announced reset to a different id re-keys the entry and fires exactly one
// ReasonClear transition naming both ids.
func TestPool_AdoptAnnouncedID_RekeysAndFiresOneClear(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	oldID := pool.Default().ID()
	newID := SessionID("0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0")

	if err := pool.AdoptAnnouncedID(oldID, newID); err != nil {
		t.Fatalf("AdoptAnnouncedID: %v", err)
	}
	if got := pool.Default().ID(); got != newID {
		t.Errorf("session id = %q after adoption, want the announced %q", got, newID)
	}
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("fired %d transitions, want exactly 1: %+v", len(got), got)
	}
	if got[0].Reason != ReasonClear || got[0].PreviousID != oldID || got[0].NewID != newID {
		t.Errorf("transition = %+v, want {%q → %q, %q}", got[0], oldID, newID, ReasonClear)
	}
	if got[0].OccurredAt.IsZero() {
		t.Errorf("transition OccurredAt is zero, want a stamp")
	}
}

// TestPool_AdoptAnnouncedID_EqualIDChangesNothing pins AC 3's pool half, and it is
// the one assertion here that onRotate would FAIL: RotateID checks membership before
// it no-ops on equal ids, so onRotate's unconditional notify draws a spurious
// delimiter for a session announcing the id it already has. This entry point must
// not repeat that.
func TestPool_AdoptAnnouncedID_EqualIDChangesNothing(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	id := pool.Default().ID()
	if err := pool.AdoptAnnouncedID(id, id); err != nil {
		t.Fatalf("AdoptAnnouncedID(x, x) = %v, want nil", err)
	}
	if got := pool.Default().ID(); got != id {
		t.Errorf("session id = %q, want the unchanged %q", got, id)
	}
	if n := rec.len(); n != 0 {
		t.Errorf("fired %d transitions on an equal-id announcement, want 0 — a spurious delimiter", n)
	}
}

// TestPool_AdoptAnnouncedID_UnknownOldID is the rotation watcher's path when it wins
// the race to the same rotation: it already re-keyed, so oldID is gone. Refusing here
// with no transition is what keeps "exactly one session_transition per reset"
// structural rather than merely likely.
func TestPool_AdoptAnnouncedID_UnknownOldID(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	absent := SessionID("99999999-9999-4999-8999-999999999999")
	newID := SessionID("0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0")
	if err := pool.AdoptAnnouncedID(absent, newID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("AdoptAnnouncedID on an absent old id = %v, want ErrSessionNotFound", err)
	}
	if n := rec.len(); n != 0 {
		t.Errorf("fired %d transitions on a refused adoption, want 0", n)
	}
}

// TestPool_AdoptAnnouncedID_RefusesATakenID is the security assertion. newID crosses
// a trust boundary this package did not have before #2135: one line on the supervised
// child's stdout now reaches rekeyLocked, which moves a map entry WITHOUT checking
// what is already at the destination. Adopting an id that names another live session
// would overwrite that session's entry and silently swallow it, so the destination is
// refused — inside the same p.mu hold as the mutation, because a check outside it is
// a TOCTOU.
func TestPool_AdoptAnnouncedID_RefusesATakenID(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)

	oldID := pool.Default().ID()
	taken := SessionID("0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0")
	// Occupy the destination key. The value is immaterial to a map-collision refusal
	// — what is asserted below is that neither key moved.
	pool.mu.Lock()
	pool.sessions[taken] = pool.sessions[oldID]
	pool.mu.Unlock()

	if err := pool.AdoptAnnouncedID(oldID, taken); !errors.Is(err, ErrSessionIDTaken) {
		t.Fatalf("AdoptAnnouncedID onto a live id = %v, want ErrSessionIDTaken", err)
	}
	pool.mu.Lock()
	_, oldStillThere := pool.sessions[oldID]
	_, takenStillThere := pool.sessions[taken]
	pool.mu.Unlock()
	if !oldStillThere || !takenStillThere {
		t.Errorf("after the refusal old present = %v, taken present = %v; want both true — "+
			"a refused adoption must not have swallowed either entry", oldStillThere, takenStillThere)
	}
	if n := rec.len(); n != 0 {
		t.Errorf("fired %d transitions on a refused adoption, want 0", n)
	}
}

// TestPool_AdoptAnnouncedID_RebindsTheOwningConversation pins the mechanism behind
// AC 4: the conversation-keyed run-configuration reply resolves the context window
// from the conversation's CURRENT bound session, so the re-key is only useful if the
// binding follows it. notifyTransition drives the rebind ahead of the observer
// fan-out; this asserts that the announced-reset path reaches it.
func TestPool_AdoptAnnouncedID_RebindsTheOwningConversation(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)

	oldID := pool.Default().ID()
	newID := SessionID("0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0")

	const convID conversations.ConversationID = "11111111-2222-4333-8444-555555555555"
	reg, _ := seedBoundConvRegistry(t, pool, convID, oldID)

	if err := pool.AdoptAnnouncedID(oldID, newID); err != nil {
		t.Fatalf("AdoptAnnouncedID: %v", err)
	}
	got, ok := reg.Get(convID)
	if !ok {
		t.Fatalf("conversation %q vanished from the registry", convID)
	}
	if got.CurrentSessionID != string(newID) {
		t.Errorf("conversation bound to %q after the announced reset, want the announced %q",
			got.CurrentSessionID, newID)
	}
}
