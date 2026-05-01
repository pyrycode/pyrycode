package sessions

import (
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// helperPool builds a Pool with a benign bootstrap config. None of the pool
// or session tests that use this call Run, so /bin/sleep is never spawned —
// it is only there to satisfy supervisor.New's exec.LookPath check.
//
// withBridge controls whether the bootstrap session has an attached bridge.
// The Attach-related session tests need one; the rest do not care.
func helperPool(t *testing.T, withBridge bool) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin: "/bin/sleep",
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if withBridge {
		cfg.Bootstrap.Bridge = supervisor.NewBridge(cfg.Logger)
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// helperPoolWithSleepArgs builds a Pool whose bootstrap session, when Run, will
// spawn `/bin/sleep 3600` — a long-lived benign child the test can tear down
// via context cancellation. Backoff is shortened so any unexpected restart
// path triggers quickly rather than hiding behind the 500ms default.
func helperPoolWithSleepArgs(t *testing.T) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// TestPool_New_BootstrapInstalled covers the constructor path: New must
// install exactly one bootstrap entry, reachable via Default(), with a valid
// canonical UUID id.
func TestPool_New_BootstrapInstalled(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	def := pool.Default()
	if def == nil {
		t.Fatal("pool.Default() returned nil")
	}
	id := string(def.ID())
	if len(id) != 36 {
		t.Errorf("len(default id) = %d, want 36 (id = %q)", len(id), id)
	}
	if !uuidPattern.MatchString(id) {
		t.Errorf("default id %q does not match canonical UUID pattern", id)
	}
}

// TestPool_LookupEmpty_ResolvesToDefault is the consumer-path AC: an empty
// SessionID resolves to the bootstrap entry. Phase 1.1's wire protocol will
// rely on this — Phase 1.0 doesn't call it from production code yet, but the
// behaviour must be in place for Child B.
func TestPool_LookupEmpty_ResolvesToDefault(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	got, err := pool.Lookup("")
	if err != nil {
		t.Fatalf("Lookup(\"\"): %v", err)
	}
	if got != pool.Default() {
		t.Errorf("Lookup(\"\") returned %p, want %p (Default)", got, pool.Default())
	}
}

// TestPool_LookupByID_ReturnsBootstrap verifies that a Lookup with the
// bootstrap session's own id returns it.
func TestPool_LookupByID_ReturnsBootstrap(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	def := pool.Default()
	got, err := pool.Lookup(def.ID())
	if err != nil {
		t.Fatalf("Lookup(%q): %v", def.ID(), err)
	}
	if got != def {
		t.Errorf("Lookup(%q) returned %p, want %p (Default)", def.ID(), got, def)
	}
}

// TestPool_LookupUnknown_ReturnsErrSessionNotFound verifies that a non-empty
// but unknown id returns ErrSessionNotFound, matchable via errors.Is.
func TestPool_LookupUnknown_ReturnsErrSessionNotFound(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	// Fabricate a syntactically-plausible id that is not the bootstrap.
	fake := SessionID("00000000-0000-4000-8000-000000000000")
	got, err := pool.Lookup(fake)
	if got != nil {
		t.Errorf("Lookup(unknown) returned non-nil session %p, want nil", got)
	}
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(unknown) err = %v, want ErrSessionNotFound", err)
	}
}
