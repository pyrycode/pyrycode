package sessions

import (
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// helperPoolPersistentIdle builds a Pool with persistence enabled, a long-
// running /bin/sleep bootstrap, and a configurable idle timeout. Backoff
// timings are short so supervisor state transitions resolve quickly.
//
// Used by the persist-before-wake regression tests: the lifecycle goroutine
// must actually run (so transitionTo fires), and persistence must be on (so
// the test can assert the on-disk state immediately after Evict/Activate).
func helperPoolPersistentIdle(t *testing.T, registryPath string, idle time.Duration) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	// Bridge mode: callers run the pool via runPoolInBackground, which spawns
	// the bootstrap supervisor. Foreground mode in a Run-reaching fixture is
	// the deadlock surface #41 surfaced.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			Bridge:         supervisor.NewBridge(logger),
			IdleTimeout:    idle,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger:       logger,
		RegistryPath: registryPath,
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// TestSession_EvictBlocksUntilPersisted: after Session.Evict returns,
// loadRegistry must show the post-evict state. Asserts that persist
// completes before Evict's wake — no poll on the disk read, because the
// fix's contract is "Evict returns only after persist".
func TestSession_EvictBlocksUntilPersisted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistentIdle(t, regPath, 0) // idle eviction disabled
	ctx, _ := runPoolInBackground(t, pool)

	sess := pool.Default()
	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateActive
	}) {
		t.Fatalf("session never reached stateActive; state=%v", sess.LifecycleState())
	}

	if err := sess.Evict(ctx); err != nil {
		t.Fatalf("Evict: %v", err)
	}

	// Immediate disk read — no poll. The contract under test is that
	// Evict's wake follows the persist; a poll here would mask the race.
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	var found bool
	for _, e := range reg.Sessions {
		if e.ID == sess.ID() {
			found = true
			if e.LifecycleState != "evicted" {
				t.Errorf("on-disk lifecycleState = %q, want %q", e.LifecycleState, "evicted")
			}
		}
	}
	if !found {
		t.Errorf("entry %q missing from registry", sess.ID())
	}
}

// TestSession_ActivateBlocksUntilPersisted: drive a session into stateEvicted
// (via Evict), call Activate, then immediately read the registry — the
// on-disk lifecycleState must be empty (the omitempty encoding for
// stateActive). The contract under test is that Activate's wake follows the
// persist; an immediate disk read with no poll would otherwise race.
//
// Note: the bootstrap is loaded as stateActive on warm-start regardless of
// disk (see #202 / Pool.New). This test reaches stateEvicted by calling
// Evict first rather than via a seeded warm-start fixture.
func TestSession_ActivateBlocksUntilPersisted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")

	pool := helperPoolPersistentIdle(t, regPath, 0) // idle eviction disabled
	ctx, _ := runPoolInBackground(t, pool)

	sess := pool.Default()
	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateActive
	}) {
		t.Fatalf("session never reached stateActive; state=%v", sess.LifecycleState())
	}

	if err := sess.Evict(ctx); err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if got := sess.LifecycleState(); got != stateEvicted {
		t.Fatalf("post-Evict lcState = %v, want stateEvicted", got)
	}

	if err := sess.Activate(ctx); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	var found bool
	for _, e := range reg.Sessions {
		if e.ID == sess.ID() {
			found = true
			// stateActive encodes as omitempty (empty string on disk).
			if e.LifecycleState != "" {
				t.Errorf("on-disk lifecycleState = %q, want empty (active)", e.LifecycleState)
			}
		}
	}
	if !found {
		t.Errorf("entry %q missing from registry", sess.ID())
	}
}

// TestSession_EvictActivateStress loops 20 evict↔activate transitions and
// asserts that the on-disk lifecycleState matches the in-memory state
// immediately after every Evict and Activate returns. Catches any window
// where the wake fires before the persist completes.
func TestSession_EvictActivateStress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistentIdle(t, regPath, 0) // idle eviction disabled
	ctx, _ := runPoolInBackground(t, pool)

	sess := pool.Default()
	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateActive
	}) {
		t.Fatalf("session never reached stateActive; state=%v", sess.LifecycleState())
	}

	checkDisk := func(iter int, want string) {
		t.Helper()
		reg, err := loadRegistry(regPath)
		if err != nil {
			t.Fatalf("iter %d: loadRegistry: %v", iter, err)
		}
		for _, e := range reg.Sessions {
			if e.ID == sess.ID() {
				if e.LifecycleState != want {
					t.Fatalf("iter %d: on-disk lifecycleState = %q, want %q", iter, e.LifecycleState, want)
				}
				return
			}
		}
		t.Fatalf("iter %d: entry %q missing from registry", iter, sess.ID())
	}

	for i := 0; i < 20; i++ {
		if err := sess.Evict(ctx); err != nil {
			t.Fatalf("iter %d: Evict: %v", i, err)
		}
		checkDisk(i, "evicted")
		if err := sess.Activate(ctx); err != nil {
			t.Fatalf("iter %d: Activate: %v", i, err)
		}
		checkDisk(i, "")
	}
}

// setRegistryPath swaps the pool's registry path under the pool lock so a
// concurrent lifecycle persist reads a consistent value (race-detector safe).
func setRegistryPath(p *Pool, path string) {
	p.mu.Lock()
	p.registryPath = path
	p.mu.Unlock()
}

// TestSession_PersistFailure_IsNonFatal proves a registry-persist failure on a
// lifecycle transition is NON-FATAL: the in-memory transition still completes,
// the lifecycle goroutine survives, and a later transition (with persistence
// restored) succeeds and re-persists (self-heals). Before the fix the persist
// error propagated out of the Run loop and, via the pool's shared error group,
// tore down every live session over one disk hiccup during a routine eviction.
func TestSession_PersistFailure_IsNonFatal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistentIdle(t, regPath, 0) // manual evict/activate
	ctx, _ := runPoolInBackground(t, pool)

	sess := pool.Default()
	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateActive
	}) {
		t.Fatalf("session never reached stateActive; state=%v", sess.LifecycleState())
	}

	// Break persistence: point the registry at a path under a non-directory so
	// saveLocked fails on the next transition (same trick as pool_create_test).
	setRegistryPath(pool, "/dev/null/cant/sessions.json")

	// Evict: the in-memory transition must still succeed despite the persist
	// failure — memory is authoritative — and the lifecycle goroutine must NOT
	// return the error.
	if err := sess.Evict(ctx); err != nil {
		t.Fatalf("Evict returned err on persist failure, want nil (non-fatal): %v", err)
	}
	if got := sess.LifecycleState(); got != stateEvicted {
		t.Fatalf("post-Evict lcState = %v, want stateEvicted (memory authoritative)", got)
	}

	// Restore persistence and Activate. If the lifecycle goroutine had died on
	// the persist failure (the pre-fix behaviour), this Activate would never
	// wake and would fail/time out.
	setRegistryPath(pool, regPath)
	if err := sess.Activate(ctx); err != nil {
		t.Fatalf("Activate after restored persistence: %v (lifecycle goroutine likely died on the persist failure)", err)
	}
	if got := sess.LifecycleState(); got != stateActive {
		t.Fatalf("post-Activate lcState = %v, want stateActive", got)
	}

	// The restored transition re-persisted: on-disk state self-healed to active
	// (encoded as omitempty / empty string).
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	var found bool
	for _, e := range reg.Sessions {
		if e.ID == sess.ID() {
			found = true
			if e.LifecycleState != "" {
				t.Errorf("on-disk lifecycleState = %q, want empty (active) after self-heal", e.LifecycleState)
			}
		}
	}
	if !found {
		t.Errorf("entry %q missing from registry after self-heal", sess.ID())
	}
}
