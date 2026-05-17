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
