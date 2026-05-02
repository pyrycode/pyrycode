package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// helperPoolCreate builds a Pool whose bootstrap and Pool.Create-spawned
// children both run a long sleep wrapped through /bin/sh so the
// --session-id <uuid> Pool.Create appends becomes a positional arg the
// inner `exec sleep 3600` ignores. Plain /bin/sleep (on macOS especially)
// rejects extra arguments as invalid time intervals — using sh -c with --
// gives us a "tolerates trailing args" sleep without writing a test
// helper binary.
//
// Bridge mode is enabled so each supervisor pumps its own pipes instead of
// fighting over os.Stdin (the cap-test rationale applies here too).
func helperPoolCreate(t *testing.T, registryPath string, activeCap int) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     []string{"-c", "exec sleep 3600", "--"},
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
			Bridge:         supervisor.NewBridge(logger),
		},
		Logger:       logger,
		RegistryPath: registryPath,
		ActiveCap:    activeCap,
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// runPoolInBackground launches pool.Run on a goroutine, blocks until the
// pool's runGroup handle has been wired (so a subsequent supervise call
// won't observe ErrPoolNotRunning), and returns a cancel function the test
// should call (via t.Cleanup) so Run exits and the goroutine joins before
// the test returns.
func runPoolInBackground(t *testing.T, pool *Pool) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("pool.Run did not exit within 15s after cancel")
		}
	})
	if !pollUntil(t, 2*time.Second, func() bool {
		pool.mu.RLock()
		ready := pool.runGroup != nil
		pool.mu.RUnlock()
		return ready
	}) {
		t.Fatal("pool.Run did not wire runGroup within 2s")
	}
	return ctx, cancel
}

// TestPool_Create_HappyPath: Pool.Create returns a valid UUID, persists the
// new entry, makes it Lookup-able, and brings up a real /bin/sleep child
// under supervision.
func TestPool_Create_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	// Wait for bootstrap to be active before Create — keeps the test focused
	// on the new-session lifecycle, not on bootstrap startup races.
	if !pollUntil(t, 2*time.Second, func() bool {
		return pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never spawned")
	}

	id, err := pool.Create(ctx, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(id) != 36 || !uuidPattern.MatchString(string(id)) {
		t.Errorf("Create id = %q, want canonical UUID", id)
	}

	got, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(new id): %v", err)
	}
	if got == nil || got.ID() != id {
		t.Errorf("Lookup returned %v, want session with id %q", got, id)
	}

	if !pollUntil(t, 2*time.Second, func() bool {
		return got.State().ChildPID > 0
	}) {
		t.Fatalf("new session never spawned a child; state=%+v", got.State())
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 2 {
		t.Fatalf("registry sessions = %+v, want 2", reg)
	}
	var newEntry *registryEntry
	for i := range reg.Sessions {
		if reg.Sessions[i].ID == id {
			newEntry = &reg.Sessions[i]
		}
	}
	if newEntry == nil {
		t.Fatalf("new entry %q missing from registry", id)
	}
	if newEntry.Bootstrap {
		t.Errorf("new entry has bootstrap=true, want false")
	}
	if newEntry.Label != "" {
		t.Errorf("new entry label = %q, want empty", newEntry.Label)
	}
}

// TestPool_Create_LabelRoundTrip: the label argument is written verbatim to
// the registry — empty preserved as empty, non-empty preserved verbatim.
func TestPool_Create_LabelRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		label string
	}{
		{"empty", ""},
		{"alpha", "alpha"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			regPath := filepath.Join(dir, "sessions.json")
			pool := helperPoolCreate(t, regPath, 0)
			ctx, _ := runPoolInBackground(t, pool)

			id, err := pool.Create(ctx, tc.label)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			data, err := os.ReadFile(regPath)
			if err != nil {
				t.Fatalf("read registry: %v", err)
			}
			var reg registryFile
			if err := json.Unmarshal(data, &reg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			var found bool
			for _, e := range reg.Sessions {
				if e.ID == id {
					found = true
					if e.Label != tc.label {
						t.Errorf("on-disk label = %q, want %q", e.Label, tc.label)
					}
				}
			}
			if !found {
				t.Errorf("entry %q not found in registry", id)
			}
		})
	}
}

// TestPool_Create_PersistFails_NoEntry_NoSpawn: when saveLocked fails (the
// registry path is under a non-directory), Create returns an empty id and
// the in-memory map is rolled back. No claude process is spawned.
func TestPool_Create_PersistFails_NoEntry_NoSpawn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	if !pollUntil(t, 2*time.Second, func() bool {
		return pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never spawned")
	}

	// Point persistence at a path under a non-directory; saveLocked's
	// MkdirAll fails. /dev/null exists on Linux+macOS and is not a
	// directory, so MkdirAll("/dev/null/cant") errors out.
	pool.mu.Lock()
	pool.registryPath = "/dev/null/cant/sessions.json"
	pool.mu.Unlock()

	id, err := pool.Create(ctx, "")
	if err == nil {
		t.Fatalf("Create returned id=%q, err=nil; want error from persist failure", id)
	}
	if id != "" {
		t.Errorf("Create id = %q, want empty after persist failure", id)
	}

	snap := pool.Snapshot()
	if len(snap) != 1 {
		t.Errorf("len(Snapshot) = %d, want 1 (bootstrap only)", len(snap))
	}
	if snap[0].ID != pool.Default().ID() {
		t.Errorf("snap[0].ID = %q, want bootstrap %q", snap[0].ID, pool.Default().ID())
	}
}

// TestPool_Create_SuperviseFails_EntryOnDisk: when Pool.Run is not active,
// Create returns ErrPoolNotRunning *and* the new id, with the entry
// persisted to disk. No lifecycle goroutine runs, so the new session has
// ChildPID == 0.
func TestPool_Create_SuperviseFails_EntryOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	// Deliberately do NOT call Run.

	id, err := pool.Create(context.Background(), "")
	if !errors.Is(err, ErrPoolNotRunning) {
		t.Fatalf("Create err = %v, want ErrPoolNotRunning", err)
	}
	if id == "" {
		t.Fatalf("Create id is empty; want non-empty so caller can recover the entry")
	}

	got, lookupErr := pool.Lookup(id)
	if lookupErr != nil {
		t.Errorf("Lookup(%q) err = %v, want nil (entry should be in-memory)", id, lookupErr)
	}
	if got == nil {
		t.Errorf("Lookup(%q) returned nil session", id)
	} else if got.State().ChildPID != 0 {
		t.Errorf("ChildPID = %d, want 0 (no lifecycle goroutine)", got.State().ChildPID)
	}

	reg, regErr := loadRegistry(regPath)
	if regErr != nil {
		t.Fatalf("loadRegistry: %v", regErr)
	}
	if reg == nil || len(reg.Sessions) != 2 {
		t.Fatalf("registry = %+v, want 2 entries (bootstrap + new)", reg)
	}
	var found bool
	for _, e := range reg.Sessions {
		if e.ID == id {
			found = true
			if e.Bootstrap {
				t.Errorf("new entry on disk has bootstrap=true")
			}
		}
	}
	if !found {
		t.Errorf("new entry %q missing from registry", id)
	}
}

// TestPool_Create_BootstrapUnchanged: after Create, Default() returns the
// same *Session pointer and same id; the bootstrap entry on disk still has
// bootstrap=true.
func TestPool_Create_BootstrapUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	beforeSess := pool.Default()
	beforeID := beforeSess.ID()

	if _, err := pool.Create(ctx, "child"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	afterSess := pool.Default()
	if afterSess != beforeSess {
		t.Errorf("Default() pointer changed after Create: %p → %p", beforeSess, afterSess)
	}
	if afterSess.ID() != beforeID {
		t.Errorf("bootstrap id changed: %q → %q", beforeID, afterSess.ID())
	}

	got, err := pool.Lookup("")
	if err != nil {
		t.Fatalf("Lookup(\"\"): %v", err)
	}
	if got != beforeSess {
		t.Errorf("Lookup(\"\") returned %p, want bootstrap %p", got, beforeSess)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	var bootstrapEntry *registryEntry
	for i := range reg.Sessions {
		if reg.Sessions[i].ID == beforeID {
			bootstrapEntry = &reg.Sessions[i]
		}
	}
	if bootstrapEntry == nil {
		t.Fatalf("bootstrap entry %q missing from registry", beforeID)
	}
	if !bootstrapEntry.Bootstrap {
		t.Errorf("bootstrap entry lost bootstrap=true after Create")
	}
}

// TestPool_Create_CapPassthrough_EvictsLRU: ActiveCap=1 with the bootstrap
// active. Create activates a new session through Pool.Activate's cap path —
// the bootstrap is the only LRU peer and must be evicted before the new
// session reaches active. Verifies Pool.Create reuses the cap-aware spawn
// path with no duplicated cap logic.
func TestPool_Create_CapPassthrough_EvictsLRU(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 1)
	ctx, _ := runPoolInBackground(t, pool)

	// Wait for the bootstrap to settle into stateActive — the cap path's
	// LRU iteration only sees the bootstrap once it's accounted for.
	// 5s budget accommodates parallel-test contention on pty.Start.
	if !pollUntil(t, 5*time.Second, func() bool {
		return pool.Default().LifecycleState() == stateActive &&
			pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never reached active")
	}

	id, err := pool.Create(ctx, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newSess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(new): %v", err)
	}

	if !pollUntil(t, 5*time.Second, func() bool {
		return pool.Default().LifecycleState() == stateEvicted &&
			newSess.LifecycleState() == stateActive &&
			newSess.State().ChildPID > 0 &&
			pool.Default().State().ChildPID == 0
	}) {
		t.Fatalf("cap=1 transitions did not settle: bootstrap=%v(pid=%d) new=%v(pid=%d)",
			pool.Default().LifecycleState(), pool.Default().State().ChildPID,
			newSess.LifecycleState(), newSess.State().ChildPID)
	}
}
