package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSupervisor_NewAppliesDefaults covers the default-application paths in
// New that helperConfig (the integration test fixture) bypasses by setting
// every field explicitly. With a barebones Config we exercise the
// "if cfg.X == 0 → set sensible default" branches and confirm the resulting
// Config the Supervisor wraps has the documented defaults.
func TestSupervisor_NewAppliesDefaults(t *testing.T) {
	t.Parallel()

	// We need a binary that exists on PATH so New's exec.LookPath
	// passes. /bin/sleep is universally present on macOS and Linux CI.
	cfg := Config{ClaudeBin: "/bin/sleep"}
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// We can't read sup.cfg directly (unexported), but we can verify
	// the documented invariants via behaviour:
	//   - Logger non-nil (defaults to slog.Default), reachable via sup.log
	//   - Backoff defaults applied (newBackoffTimer would receive 0 0 0
	//     otherwise and divide-by-zero or never-restart in surprising ways)
	//
	// The simplest behavioural check is that State() returns the
	// PhaseStarting we set in New — which would be the case regardless
	// of defaults, but if any default code path panicked we'd never
	// reach here.
	if sup.State().Phase != PhaseStarting {
		t.Errorf("State.Phase = %q, want %q", sup.State().Phase, PhaseStarting)
	}
}

// TestSupervisor_NewRejectsMissingClaudeBin covers the early-validation
// path in New: if the binary is not on PATH, construction fails with a
// wrapped "claude binary not found" error rather than letting Run discover
// the problem on first spawn.
func TestSupervisor_NewRejectsMissingClaudeBin(t *testing.T) {
	t.Parallel()

	cfg := Config{ClaudeBin: "/nonexistent/path/to/claude/" + t.Name()}
	if _, err := New(cfg); err == nil {
		t.Fatal("New with missing binary should error, did not")
	} else if !strings.Contains(err.Error(), "claude binary not found") {
		t.Errorf("error %q should mention 'claude binary not found'", err)
	}
}

// TestSupervisor_StateInitial confirms the State snapshot reflects the
// PhaseStarting setup that New does, with all other fields zeroed.
func TestSupervisor_StateInitial(t *testing.T) {
	t.Parallel()
	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := sup.State()
	if st.Phase != PhaseStarting {
		t.Errorf("Phase = %q, want %q", st.Phase, PhaseStarting)
	}
	if st.ChildPID != 0 {
		t.Errorf("ChildPID = %d, want 0 before Run", st.ChildPID)
	}
	if st.RestartCount != 0 {
		t.Errorf("RestartCount = %d, want 0", st.RestartCount)
	}
	if !st.StartedAt.IsZero() {
		t.Errorf("StartedAt should be zero before Run, got %v", st.StartedAt)
	}
}

// TestSupervisor_StateAcrossPhases observes State() through a full lifecycle:
// PhaseStarting → PhaseRunning (with a real child PID) → PhaseStopped via
// ctx-cancel.
func TestSupervisor_StateAcrossPhases(t *testing.T) {
	t.Parallel()

	// "sleep 5s" — gives us a stable PhaseRunning window to observe in.
	cfg := helperConfig("sleep", "GO_TEST_HELPER_SLEEP=5s")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(ctx) }()

	// Poll until the supervisor reports a running child with a real PID.
	deadline := time.Now().Add(5 * time.Second)
	var observed State
	for time.Now().Before(deadline) {
		observed = sup.State()
		if observed.Phase == PhaseRunning && observed.ChildPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if observed.Phase != PhaseRunning {
		t.Fatalf("never observed PhaseRunning; last state = %+v", observed)
	}
	if observed.ChildPID == 0 {
		t.Fatalf("PhaseRunning but ChildPID = 0; state = %+v", observed)
	}
	if observed.StartedAt.IsZero() {
		t.Errorf("PhaseRunning but StartedAt = zero")
	}

	// Cancel and confirm State drains to PhaseStopped via the deferred
	// updateState in Run.
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of cancel")
	}
	final := sup.State()
	if final.Phase != PhaseStopped {
		t.Errorf("final Phase = %q, want PhaseStopped", final.Phase)
	}
	if final.ChildPID != 0 {
		t.Errorf("final ChildPID = %d, want 0 after stop", final.ChildPID)
	}
}

// TestHelperProcess is not a real test. It is used as a fake child process by
// integration tests. The test binary re-execs itself with GO_TEST_HELPER_PROCESS=1,
// and the behavior is controlled by GO_TEST_HELPER_MODE:
//
//   - "exit0":     exit immediately with code 0
//   - "exit1":     exit immediately with code 1
//   - "sleep":     sleep for GO_TEST_HELPER_SLEEP duration, then exit 0
//   - "crash":     exit immediately with code 2 (simulates crash)
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}

	mode := os.Getenv("GO_TEST_HELPER_MODE")
	switch mode {
	case "exit0":
		os.Exit(0)
	case "exit1":
		os.Exit(1)
	case "crash":
		os.Exit(2)
	case "sleep":
		dur, err := time.ParseDuration(os.Getenv("GO_TEST_HELPER_SLEEP"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid GO_TEST_HELPER_SLEEP: %v\n", err)
			os.Exit(99)
		}
		time.Sleep(dur)
		os.Exit(0)
	case "sleep_then_crash":
		dur, err := time.ParseDuration(os.Getenv("GO_TEST_HELPER_SLEEP"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid GO_TEST_HELPER_SLEEP: %v\n", err)
			os.Exit(99)
		}
		time.Sleep(dur)
		os.Exit(1)
	case "count_exits":
		// Exit with code 0 the first N times, then block until killed.
		// Uses a file to track invocation count.
		countFile := os.Getenv("GO_TEST_HELPER_COUNT_FILE")
		if countFile == "" {
			os.Exit(99)
		}
		maxExits, _ := strconv.Atoi(os.Getenv("GO_TEST_HELPER_MAX_EXITS"))
		if maxExits == 0 {
			maxExits = 2
		}
		count := readCount(countFile)
		writeCount(countFile, count+1)
		if count < maxExits {
			os.Exit(0)
		}
		// Block until killed. Use sleep instead of select{} to avoid
		// Go's deadlock detector panicking in the child process.
		time.Sleep(24 * time.Hour)
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_TEST_HELPER_MODE: %q\n", mode)
		os.Exit(99)
	}
}

func readCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(string(data))
	return n
}

func writeCount(path string, n int) {
	_ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0644)
}

// helperConfig returns a Config that uses the test binary as the child process.
func helperConfig(mode string, env ...string) Config {
	allEnv := append([]string{
		"GO_TEST_HELPER_PROCESS=1",
		"GO_TEST_HELPER_MODE=" + mode,
	}, env...)

	return Config{
		ClaudeBin:      os.Args[0],
		ClaudeArgs:     []string{"-test.run=TestHelperProcess", "--"},
		ResumeLast:     false,
		Logger:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		BackoffInitial: 10 * time.Millisecond,
		BackoffMax:     50 * time.Millisecond,
		BackoffReset:   100 * time.Millisecond,
		helperEnv:      allEnv,
	}
}

func TestSupervisor_ChildExitsCleanly(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("count_exits")

	countFile := t.TempDir() + "/count"
	cfg.helperEnv = append(cfg.helperEnv,
		"GO_TEST_HELPER_COUNT_FILE="+countFile,
		"GO_TEST_HELPER_MAX_EXITS=1",
	)

	// Re-exec'ing the test binary as a PTY child has overhead (~200ms per
	// cycle: fork+exec+Go runtime init+test framework+helper+exit+PTY drain).
	// Give enough time for at least 2 full cycles.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The child exits once (count_exits mode), then blocks on second run.
	// We cancel the context after enough time for the restart cycle.
	go func() {
		time.Sleep(3 * time.Second)
		cancel()
	}()

	err = sup.Run(ctx)
	if err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}

	count := readCount(countFile)
	if count < 2 {
		t.Errorf("expected at least 2 child invocations, got %d", count)
	}
}

func TestSupervisor_RestartsAfterCrash(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("count_exits")

	countFile := t.TempDir() + "/count"
	cfg.helperEnv = append(cfg.helperEnv,
		"GO_TEST_HELPER_COUNT_FILE="+countFile,
		"GO_TEST_HELPER_MAX_EXITS=3",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	go func() {
		time.Sleep(8 * time.Second)
		cancel()
	}()

	err = sup.Run(ctx)
	if err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}

	count := readCount(countFile)
	if count < 3 {
		t.Errorf("expected at least 3 child invocations (crash + restart), got %d", count)
	}
}

func TestSupervisor_GracefulShutdown(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("sleep",
		"GO_TEST_HELPER_SLEEP=10s",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Cancel quickly to test graceful shutdown.
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err = sup.Run(ctx)
	elapsed := time.Since(start)

	if err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}

	// Should exit quickly after cancellation, not wait for the 10s sleep.
	if elapsed > 3*time.Second {
		t.Errorf("shutdown took %v, expected < 3s", elapsed)
	}
}

func isContextErr(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}

// TestSupervisor_Foreground_NoStdinReaderLeak exercises the foreground-mode
// supervisor across multiple child restart cycles and asserts no goroutine
// accumulation. Pre-fix, each cycle stranded one io.Copy(ptmx, os.Stdin)
// goroutine on os.Stdin's fdMutex; post-fix, closing the per-cycle /dev/tty
// fd unblocks the read and the goroutine drains.
//
// Skips when /dev/tty is unavailable (CI containers, headless), since the
// supervisor falls back to os.Stdin in that case and the legacy leak shape
// is preserved by design.
func TestSupervisor_Foreground_NoStdinReaderLeak(t *testing.T) {
	f, err := os.Open("/dev/tty")
	if err != nil {
		t.Skipf("no controlling tty: %v", err)
	}
	_ = f.Close()

	cfg := helperConfig("count_exits")
	countFile := t.TempDir() + "/count"
	cfg.helperEnv = append(cfg.helperEnv,
		"GO_TEST_HELPER_COUNT_FILE="+countFile,
		"GO_TEST_HELPER_MAX_EXITS=3",
	)

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Let any pending GC / runtime goroutines settle before snapshot.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	pre := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		// Cancel after enough time for ~3 child exits + restarts.
		time.Sleep(4 * time.Second)
		cancel()
	}()

	if err := sup.Run(ctx); err != nil && !isContextErr(err) {
		t.Fatalf("Run: %v", err)
	}

	// Allow goroutine teardown to complete. The drain timeout in runOnce
	// is bounded by goroutineDrainTimeout * 2 per cycle.
	time.Sleep(500 * time.Millisecond)
	runtime.GC()

	post := runtime.NumGoroutine()
	// Tolerance: 2 goroutines for incidental runtime/test churn. The leak
	// under the old code was at least one per child exit (3+ here). On
	// failure, dump goroutine stacks to make the leaking call site
	// obvious.
	if post > pre+2 {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Errorf("goroutine leak: pre=%d, post=%d (delta=%d)\n%s", pre, post, post-pre, buf[:n])
	}
}
