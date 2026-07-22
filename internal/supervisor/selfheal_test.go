package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestSupervisor_SelfHeal_FastCrashesTriggerOneRotation (AC1/AC5): when the
// child exits non-zero within FastCrashWindow of spawn FastCrashThreshold
// consecutive times, the supervisor calls SelfHeal exactly once; the fresh id
// (here: the injected SelfHeal writing a marker) then comes up clean and stays
// up. The injected SelfHeal stands in for Pool.RotateBootstrapForSelfHeal — this
// surface only proves the supervisor's detect-and-signal half.
func TestSupervisor_SelfHeal_FastCrashesTriggerOneRotation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, "healed")

	cfg := helperConfig("fast_crash_until_healed",
		"GO_TEST_HELPER_MARKER="+marker)
	// Generous window: an instant exit's uptime is dominated by the ~200ms
	// re-exec overhead, still far below 5s → every crash counts as fast.
	cfg.FastCrashWindow = 5 * time.Second
	cfg.FastCrashThreshold = 3

	var healCalls atomic.Int32
	cfg.SelfHeal = func() error {
		healCalls.Add(1)
		// Stand in for a fresh-id mint: write the marker so the next spawn
		// comes up healthy instead of fast-crashing.
		return os.WriteFile(marker, []byte("healed"), 0o600)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- sup.Run(ctx) }()

	// Self-heal must fire once the fast-crash streak reaches the threshold.
	deadline := time.Now().Add(25 * time.Second)
	for healCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := healCalls.Load(); got != 1 {
		t.Fatalf("SelfHeal fired %d times, want exactly 1 (threshold reached once)", got)
	}

	// The fresh id must come up clean and STAY up. After self-heal the marker
	// exists, so no further spawn crashes. Wait for a stable healthy child: the
	// same non-zero pid observed twice ~500ms apart while PhaseRunning. The
	// double-read skips the just-crashed spawn's transient PhaseRunning (its
	// State has not yet advanced to PhaseBackoff at the instant self-heal fires).
	var pid int
	stableDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(stableDeadline) {
		st := sup.State()
		if st.Phase == PhaseRunning && st.ChildPID != 0 {
			first := st.ChildPID
			time.Sleep(500 * time.Millisecond)
			if st2 := sup.State(); st2.Phase == PhaseRunning && st2.ChildPID == first {
				pid = first
				break
			}
			continue
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("no stable healthy child came up after self-heal; last state = %+v", sup.State())
	}
	if got := healCalls.Load(); got != 1 {
		t.Errorf("SelfHeal fired %d times total, want exactly 1 (one rotation → clean spawn)", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !isContextErr(err) {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of cancel")
	}
}

// TestSupervisor_SelfHeal_HealthyExitDoesNotTrigger (AC2/AC3): a child that
// exits non-zero only after a healthy uptime (past FastCrashWindow) never trips
// self-heal — the streak resets on each healthy run, so even a low threshold is
// never reached. This is the ordinary backoff-and-restart path, unchanged.
func TestSupervisor_SelfHeal_HealthyExitDoesNotTrigger(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("sleep_then_crash",
		"GO_TEST_HELPER_SLEEP=800ms")
	// The child's uptime (~800ms sleep + overhead) sits clearly above the
	// window, so no exit is a "fast crash"; threshold=2 would trip immediately
	// if the streak failed to reset.
	cfg.FastCrashWindow = 200 * time.Millisecond
	cfg.FastCrashThreshold = 2

	var healCalls atomic.Int32
	cfg.SelfHeal = func() error { healCalls.Add(1); return nil }

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Allow several slow-crash cycles (each ~800ms uptime + short backoff).
	go func() { time.Sleep(5 * time.Second); cancel() }()
	if err := sup.Run(ctx); err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}
	if got := healCalls.Load(); got != 0 {
		t.Errorf("SelfHeal fired %d times on healthy/slow exits, want 0 (streak must reset on each healthy run)", got)
	}
}

// TestSupervisor_SelfHeal_NilKeepsRetrying (AC3, disabled path): with
// SelfHeal == nil the entire self-heal path is inert — a deterministic
// fast-crash loop just keeps retrying forever (pre-#1165 behaviour) without
// panicking, even well past the threshold.
func TestSupervisor_SelfHeal_NilKeepsRetrying(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, "healed") // never created: SelfHeal is nil
	countFile := filepath.Join(dir, "count")

	cfg := helperConfig("fast_crash_until_healed",
		"GO_TEST_HELPER_MARKER="+marker,
		"GO_TEST_HELPER_COUNT_FILE="+countFile)
	cfg.FastCrashWindow = 5 * time.Second
	cfg.FastCrashThreshold = 2
	// cfg.SelfHeal deliberately nil → self-heal disabled, retry-forever.

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { time.Sleep(3 * time.Second); cancel() }()
	if err := sup.Run(ctx); err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}
	// Fast crashes far exceed the threshold, but with SelfHeal nil nothing is
	// called and the supervisor just keeps retrying.
	if got := readCount(countFile); got < 3 {
		t.Errorf("SelfHeal nil: want the supervisor to keep retrying past the threshold (>=3 spawns), got %d", got)
	}
}
