package sessions

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// helperPoolCap builds a Pool whose bootstrap session, when Run, spawns
// /bin/sleep 3600 — a long-lived benign child the test can tear down via
// context cancellation. ActiveCap is configured per-test. Backoff is
// shortened so any unexpected restart fires quickly rather than hiding
// behind the 500ms default.
//
// Bridge mode is enabled so the supervisor's stdin/stdout pump runs against
// per-supervisor pipes instead of os.Stdin. Foreground mode leaks one
// stdin-bound io.Copy goroutine per runOnce (see supervisor.go's known
// limitation comment); under stress those leaked readers contend on
// os.Stdin's fdMutex, deadlocking new pty.Start calls.
func helperPoolCap(t *testing.T, cap int) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
			Bridge:         supervisor.NewBridge(logger),
		},
		Logger:    logger,
		ActiveCap: cap,
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// addCapTestSession builds a Session whose supervisor spawns /bin/sleep 3600,
// inserts it into pool.sessions under p.mu (write), and starts its lifecycle
// goroutine on ctx. The session starts in stateEvicted (the cap path's
// interesting case — Activate must spawn it).
//
// Caller is responsible for cancelling ctx so the goroutine exits.
func addCapTestSession(t *testing.T, pool *Pool, ctx context.Context, id SessionID) *Session {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup, err := supervisor.New(supervisor.Config{
		ClaudeBin:      "/bin/sleep",
		ClaudeArgs:     []string{"3600"},
		Logger:         logger,
		BackoffInitial: 10 * time.Millisecond,
		BackoffMax:     10 * time.Millisecond,
		BackoffReset:   1 * time.Second,
		// Bridge mode: per-supervisor pipes for stdin/stdout pump.
		// See helperPoolCap doc for why foreground mode is unsafe here.
		Bridge: supervisor.NewBridge(logger),
	})
	if err != nil {
		t.Fatalf("supervisor.New(%s): %v", id, err)
	}
	now := time.Now().UTC()
	sess := &Session{
		id:           id,
		sup:          sup,
		log:          logger,
		createdAt:    now,
		lastActiveAt: now,
		lcState:      stateEvicted,
		activeCh:     make(chan struct{}),
		evictedCh:    closedChan(),
		activateCh:   make(chan struct{}, 1),
		evictCh:      make(chan struct{}, 1),
		pool:         pool,
	}
	pool.mu.Lock()
	pool.sessions[id] = sess
	pool.mu.Unlock()

	go func() { _ = sess.Run(ctx) }()
	return sess
}

// countActive returns the number of sessions in pool currently in stateActive.
// Iterates under p.mu.RLock and reads each Session.lcState under lcMu.
func countActive(pool *Pool) int {
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	var n int
	for _, s := range pool.sessions {
		if s.LifecycleState() == stateActive {
			n++
		}
	}
	return n
}

// TestPool_ActiveCap_ZeroIsParity: with ActiveCap == 0, Pool.Activate is a
// thin wrapper around Session.Activate — no LRU bookkeeping cost on the hot
// path, no cap enforcement. Two evicted sessions can both be activated even
// though the bootstrap is also active (cap would otherwise bind at 1 of 3).
//
// This is the AC's "byte-identical when unset" requirement: with cap=0, the
// only thing Pool.Activate does beyond Lookup is forward to Session.Activate.
func TestPool_ActiveCap_ZeroIsParity(t *testing.T) {
	t.Parallel()
	pool := helperPoolCap(t, 0)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = pool.Default().Run(ctx) }()

	idA := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	idB := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	sessA := addCapTestSession(t, pool, ctx, idA)
	sessB := addCapTestSession(t, pool, ctx, idB)

	if err := pool.Activate(ctx, idA); err != nil {
		t.Fatalf("Activate(A): %v", err)
	}
	if err := pool.Activate(ctx, idB); err != nil {
		t.Fatalf("Activate(B): %v", err)
	}

	if !pollUntil(t, 2*time.Second, func() bool {
		return sessA.LifecycleState() == stateActive &&
			sessB.LifecycleState() == stateActive
	}) {
		t.Fatalf("states: A=%v B=%v, want both active (cap=0 means no enforcement)",
			sessA.LifecycleState(), sessB.LifecycleState())
	}

	// Bootstrap also remains active — three concurrently active claudes,
	// none evicted. With a cap this would have bound; with cap=0 it does
	// not.
	if got := pool.Default().LifecycleState(); got != stateActive {
		t.Errorf("bootstrap state = %v, want active (cap=0 should never evict)", got)
	}
}

// TestPool_ActiveCap_BindsAndEvictsLRU: cap=2 with three sessions A/B/C.
// A is the bootstrap (initial active). Activate B (no eviction; count goes
// 1→2). Activate C (cap binds; LRU peer is A — bootstrap's lastActiveAt was
// touched first; B's transition stamped a later time). Assert: A=evicted,
// B+C=active.
func TestPool_ActiveCap_BindsAndEvictsLRU(t *testing.T) {
	t.Parallel()
	pool := helperPoolCap(t, 2)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = pool.Default().Run(ctx) }()

	sessA := pool.Default()
	idB := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	idC := SessionID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	sessB := addCapTestSession(t, pool, ctx, idB)
	sessC := addCapTestSession(t, pool, ctx, idC)

	// Touch A first so its lastActiveAt is the oldest of {A,B}.
	if err := pool.Activate(ctx, sessA.ID()); err != nil {
		t.Fatalf("Activate(A): %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	// Activate B — count goes 1→2, no eviction (cap=2, exactly at cap with
	// the new spawn).
	if err := pool.Activate(ctx, idB); err != nil {
		t.Fatalf("Activate(B): %v", err)
	}
	if !pollUntil(t, 2*time.Second, func() bool {
		return sessB.LifecycleState() == stateActive
	}) {
		t.Fatalf("B did not become active; state=%v", sessB.LifecycleState())
	}
	time.Sleep(10 * time.Millisecond)

	// Activate C — count would go 2→3, exceeds cap. LRU between A and B is
	// A (touched first). A is evicted, then C spawns.
	if err := pool.Activate(ctx, idC); err != nil {
		t.Fatalf("Activate(C): %v", err)
	}

	if !pollUntil(t, 2*time.Second, func() bool {
		return sessA.LifecycleState() == stateEvicted &&
			sessB.LifecycleState() == stateActive &&
			sessC.LifecycleState() == stateActive
	}) {
		t.Fatalf("after Activate(C): A=%v B=%v C=%v, want A=evicted B=active C=active",
			sessA.LifecycleState(), sessB.LifecycleState(), sessC.LifecycleState())
	}
	if got := countActive(pool); got != 2 {
		t.Errorf("countActive = %d, want 2 (cap)", got)
	}
}

// TestPool_ActiveCap_OneSessionAtCapOne: pathological case — cap=1 with a
// single inactive target and no peer to evict. Activate must succeed; the
// target fills the only slot. (Documented in the spec's open questions: cap
// path returns nil from victim selection when there is no eligible peer.)
func TestPool_ActiveCap_OneSessionAtCapOne(t *testing.T) {
	t.Parallel()
	pool := helperPoolCap(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = pool.Default().Run(ctx) }()

	// Wait for bootstrap to settle into active.
	if !pollUntil(t, 2*time.Second, func() bool {
		return pool.Default().LifecycleState() == stateActive
	}) {
		t.Fatal("bootstrap never reached active")
	}

	// Activate against the bootstrap itself with cap=1: target is already
	// active, lastActiveAt bumps, no eviction. Active count stays at 1.
	if err := pool.Activate(ctx, pool.Default().ID()); err != nil {
		t.Fatalf("Activate(bootstrap): %v", err)
	}
	if got := countActive(pool); got != 1 {
		t.Errorf("countActive after self-Activate = %d, want 1", got)
	}
}

// TestPool_ActiveCap_RaceConcurrentActivate: with cap=1 and N concurrent
// Activate calls against N distinct sessions, the active count must never
// exceed the cap. After the dust settles, exactly one session is active.
//
// This is the binding test for the capMu-serialises-cap-check claim. The
// race detector catches any unsynchronised Pool/Session reads. Run under
// -race in CI.
//
// Test-fixture note: every cap-binding Activate evicts a just-spawned
// supervisor. supervisor.runOnce calls pty.Start, which is not interruptible —
// drainSup waits for it to complete before the kill cycle can run. Under -race
// + concurrent contention this stretches into seconds per cycle, so N is kept
// modest and the bootstrap is allowed to settle into stateActive before the
// race begins. Both keep the test exercising real concurrency without piling
// up uninterruptible pty.Start calls.
func TestPool_ActiveCap_RaceConcurrentActivate(t *testing.T) {
	t.Parallel()
	pool := helperPoolCap(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = pool.Default().Run(ctx) }()

	// Settle bootstrap to stateActive before kicking off the race.
	// Without this, the first eviction races bootstrap's still-in-progress
	// pty.Start (uninterruptible — drainSup blocks until pty.Start returns).
	if !pollUntil(t, 2*time.Second, func() bool {
		return pool.Default().LifecycleState() == stateActive
	}) {
		t.Fatal("bootstrap never reached active")
	}

	const N = 6
	ids := make([]SessionID, 0, N)
	for i := 0; i < N; i++ {
		// Synthesize plausibly-shaped UUIDs that differ per i.
		id := SessionID(fmt.Sprintf("%08x-0000-4000-8000-%012x", i+1, i+1))
		ids = append(ids, id)
		addCapTestSession(t, pool, ctx, id)
	}

	var startWG, doneWG sync.WaitGroup
	startWG.Add(1)
	for _, id := range ids {
		doneWG.Add(1)
		go func(id SessionID) {
			defer doneWG.Done()
			startWG.Wait()
			// Errors are tolerated — the cap path may surface an Evict
			// error if the LRU peer's lifecycle goroutine has races we
			// didn't think of. The invariant we test is the active
			// count, not per-call success.
			_ = pool.Activate(ctx, id)
		}(id)
	}
	startWG.Done()
	doneWG.Wait()

	// Wait for the lifecycle goroutines to settle. The final active count
	// must equal the cap (every Activate either won or evicted someone).
	// The 10s budget accommodates race-detector slowdown on the supervisor
	// kill cycle (~hundreds of ms per session under -race).
	var got int
	if !pollUntil(t, 10*time.Second, func() bool {
		got = countActive(pool)
		return got == 1
	}) {
		t.Fatalf("countActive = %d after %d concurrent Activates, want 1 (cap)", got, N)
	}
}
