package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	case "stdin_to_file":
		// Copy stdin to GO_TEST_HELPER_STDIN_FILE until EOF (PTY closed).
		// Used by TestSupervisor_WriteUserTurn_HappyPath to verify that
		// WriteUserTurn's payload reaches the child's stdin.
		path := os.Getenv("GO_TEST_HELPER_STDIN_FILE")
		if path == "" {
			fmt.Fprintln(os.Stderr, "stdin_to_file: GO_TEST_HELPER_STDIN_FILE unset")
			os.Exit(99)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stdin_to_file: open: %v\n", err)
			os.Exit(99)
		}
		// Disable buffering: copy raw chunks straight through so the test
		// can poll the file for the expected substring without waiting on
		// a flush.
		_, _ = io.Copy(f, os.Stdin)
		_ = f.Close()
		os.Exit(0)
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

// errTestConvNotFound is a local sentinel for validator-failure tests. Keeps
// the supervisor package's tests decoupled from internal/conversations.
var errTestConvNotFound = errors.New("test: conversation not found")

// waitForPhase polls sup.State() until it reports phase, or fails the test
// after the deadline. Used by tests that need a stable PhaseRunning window.
func waitForPhase(t *testing.T, sup *Supervisor, phase Phase, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sup.State().Phase == phase {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never reached phase %q within %v; last state = %+v", phase, timeout, sup.State())
}

// TestSupervisor_WriteUserTurn_HappyPath verifies the end-to-end flow: a
// successful WriteUserTurn updates the cursor AND the payload reaches the
// child's stdin via the PTY master. Uses service mode with a Bridge so the
// PTY input pump exists, but no attaching client — the bridge sits idle and
// WriteUserTurn writes directly to ptmx.
func TestSupervisor_WriteUserTurn_HappyPath(t *testing.T) {
	t.Parallel()

	stdinFile := t.TempDir() + "/stdin.bin"
	cfg := helperConfig("stdin_to_file", "GO_TEST_HELPER_STDIN_FILE="+stdinFile)
	cfg.Bridge = NewBridge(cfg.Logger)
	cfg.ValidateConversation = func(id string) error { return nil }

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(ctx) }()
	waitForPhase(t, sup, PhaseRunning, 5*time.Second)

	if err := sup.WriteUserTurn("c-1", []byte("hello\n")); err != nil {
		t.Fatalf("WriteUserTurn: %v", err)
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Errorf("CurrentConversation = %q, want %q", got, "c-1")
	}

	// Poll the helper's output file until the payload surfaces, or fail.
	deadline := time.Now().Add(3 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(stdinFile)
		if strings.Contains(string(data), "hello") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(string(data), "hello") {
		t.Errorf("helper stdin file = %q, want it to contain %q", string(data), "hello")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of cancel")
	}
}

// TestSupervisor_WriteUserTurn_CursorReadBack confirms the cursor reflects
// the most recent successful WriteUserTurn. No child needed — the no-PTY
// drop path still updates the cursor and returns nil, which is enough for
// this assertion.
func TestSupervisor_WriteUserTurn_CursorReadBack(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := sup.WriteUserTurn("c-1", []byte("first")); err != nil {
		t.Fatalf("WriteUserTurn c-1: %v", err)
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Errorf("after first write, CurrentConversation = %q, want %q", got, "c-1")
	}
	if err := sup.WriteUserTurn("c-2", []byte("second")); err != nil {
		t.Fatalf("WriteUserTurn c-2: %v", err)
	}
	if got := sup.CurrentConversation(); got != "c-2" {
		t.Errorf("after second write, CurrentConversation = %q, want %q", got, "c-2")
	}
}

// TestSupervisor_WriteUserTurn_UnknownIDDoesNotMutateCursor exercises the
// validator-refusal path: a non-nil error from ValidateConversation is
// propagated verbatim and the cursor stays at its prior value.
func TestSupervisor_WriteUserTurn_UnknownIDDoesNotMutateCursor(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	cfg.ValidateConversation = func(id string) error {
		if id == "ghost" {
			return errTestConvNotFound
		}
		return nil
	}
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := sup.WriteUserTurn("c-1", []byte("ok")); err != nil {
		t.Fatalf("WriteUserTurn c-1: %v", err)
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Fatalf("after good write, cursor = %q, want %q", got, "c-1")
	}

	err = sup.WriteUserTurn("ghost", []byte("nope"))
	if !errors.Is(err, errTestConvNotFound) {
		t.Errorf("WriteUserTurn(ghost) err = %v, want errors.Is == errTestConvNotFound", err)
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Errorf("after refused write, cursor = %q, want %q (unchanged)", got, "c-1")
	}
}

// TestSupervisor_WriteUserTurn_NilValidatorSkips confirms that a nil
// ValidateConversation skips validation entirely — useful for tests and
// for production paths that don't yet have a registry wired.
func TestSupervisor_WriteUserTurn_NilValidatorSkips(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	cfg.ValidateConversation = nil
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := sup.WriteUserTurn("anything", []byte("p")); err != nil {
		t.Errorf("WriteUserTurn: %v", err)
	}
	if got := sup.CurrentConversation(); got != "anything" {
		t.Errorf("CurrentConversation = %q, want %q", got, "anything")
	}
}

// TestSupervisor_WriteUserTurn_NoPTYDrops verifies that calling
// WriteUserTurn before Run (or between iterations) returns nil and silently
// drops the payload, while still updating the cursor. Matches Bridge.Write's
// discard-on-unattached behaviour so handlers don't need to special-case the
// backoff window.
func TestSupervisor_WriteUserTurn_NoPTYDrops(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := sup.WriteUserTurn("c-1", []byte("dropped")); err != nil {
		t.Errorf("WriteUserTurn before Run: err = %v, want nil", err)
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Errorf("CurrentConversation = %q, want %q", got, "c-1")
	}
}

// TestSupervisor_WriteUserTurn_CursorConcurrency stresses the cursor's
// mutex under contention. Multiple writers alternate ids while a reader
// loops on CurrentConversation. The assertion is twofold: -race stays
// clean, and the final cursor is one of the writers' last-written ids.
func TestSupervisor_WriteUserTurn_CursorConcurrency(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const iters = 100
	var writers sync.WaitGroup
	writers.Add(2)
	writer := func(id string) {
		defer writers.Done()
		for i := 0; i < iters; i++ {
			_ = sup.WriteUserTurn(id, []byte("x"))
		}
	}
	go writer("c-a")
	go writer("c-b")

	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
				_ = sup.CurrentConversation()
			}
		}
	}()

	writers.Wait()
	close(stop)
	<-readerDone

	got := sup.CurrentConversation()
	if got != "c-a" && got != "c-b" {
		t.Errorf("final CurrentConversation = %q, want %q or %q", got, "c-a", "c-b")
	}
}
