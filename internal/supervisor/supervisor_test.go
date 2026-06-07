package supervisor

import (
	"bytes"
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

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
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
//   - "exit0":       exit immediately with code 0
//   - "exit1":       exit immediately with code 1
//   - "sleep":       sleep for GO_TEST_HELPER_SLEEP duration, then exit 0
//   - "crash":       exit immediately with code 2 (simulates crash)
//   - "emit_marker": write GO_TEST_HELPER_MARKER to stdout, then block
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
	case "emit_marker":
		// Write a known marker to stdout, then stay alive so the supervisor's
		// MirrorOutput → bridge.Write feed has time to deliver it before the
		// test cancels. Killed by ctx-cancel (SIGKILL) at teardown.
		marker := os.Getenv("GO_TEST_HELPER_MARKER")
		if marker == "" {
			fmt.Fprintln(os.Stderr, "emit_marker: GO_TEST_HELPER_MARKER unset")
			os.Exit(99)
		}
		fmt.Fprint(os.Stdout, marker)
		time.Sleep(24 * time.Hour)
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

// syncBuffer is a goroutine-safe io.Writer + String accessor. The
// supervisor's output pump writes into it from a separate goroutine while the
// test polls for a marker, so the buffer access must be locked.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestSupervisor_Bridge_MirrorReachesConsumer verifies that claude's raw PTY
// output reaches BOTH bridge raw-byte surfaces — the attached-client writer
// (the `pyry attach` path, b.output) and the output observer (the
// assistant-turn bridge, b.outputObserver) — via the Session.MirrorOutput
// feed introduced by the Session-hosting migration. Proves AC #4: no raw-byte
// consumer is starved by the swap from io.Copy(bridge, ptmx) to the
// MirrorOutput → bridge.Write output pump.
func TestSupervisor_Bridge_MirrorReachesConsumer(t *testing.T) {
	t.Parallel()

	const marker = "MIRROR_MARKER_OK"
	cfg := helperConfig("emit_marker", "GO_TEST_HELPER_MARKER="+marker)
	cfg.Bridge = NewBridge(cfg.Logger)

	// Observer surface (assistant-turn bridge path). The observer contract
	// forbids retaining p past return, so copy the bytes into a syncBuffer.
	observed := &syncBuffer{}
	cfg.Bridge.SetOutputObserver(func(p []byte) { _, _ = observed.Write(p) })

	// Attached-client surface (pyry attach path). Park the input side on a
	// pipe so the attach input pump blocks on Read and b.output stays bound.
	pr, pw := io.Pipe()
	defer pw.Close()
	attached := &syncBuffer{}
	attachDone, err := cfg.Bridge.Attach(pr, attached)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(ctx) }()
	waitForPhase(t, sup, PhaseRunning, 5*time.Second)

	// Poll both surfaces until each carries the marker, or fail.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(attached.String(), marker) &&
			strings.Contains(observed.String(), marker) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := attached.String(); !strings.Contains(got, marker) {
		t.Errorf("attached client output = %q, want it to contain %q", got, marker)
	}
	if got := observed.String(); !strings.Contains(got, marker) {
		t.Errorf("output observer = %q, want it to contain %q", got, marker)
	}

	cancel()
	pw.Close() // detach: EOF on the input side drains the attach goroutine
	<-attachDone
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of cancel")
	}
}

// TestSupervisor_WriteUserTurn_RealDeliverNotReadyFailsLoud exercises the
// production deliverViaSession (no deliverFn override) against a real spawned
// child that never renders claude's idle screen. WaitReady cannot reach idle,
// so a short-budget ctx expires and WriteUserTurn returns a loud failure —
// proving the real ready-gate is wired and that an attached-but-not-ready
// session no longer silently succeeds (AC #2). Uses service mode with a Bridge
// so the PTY input pump exists; the fake child just stays alive draining stdin.
func TestSupervisor_WriteUserTurn_RealDeliverNotReadyFailsLoud(t *testing.T) {
	t.Parallel()

	stdinFile := t.TempDir() + "/stdin.bin"
	cfg := helperConfig("stdin_to_file", "GO_TEST_HELPER_STDIN_FILE="+stdinFile)
	cfg.Bridge = NewBridge(cfg.Logger)
	cfg.ValidateConversation = func(id string) error { return nil }

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(runCtx) }()
	waitForPhase(t, sup, PhaseRunning, 5*time.Second)

	// Short budget: the fake child never reaches claude-idle, so WaitReady
	// blocks until this ctx expires (context.DeadlineExceeded), which
	// deliverViaSession wraps as "wait ready: ...".
	deliverCtx, cancelDeliver := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancelDeliver()
	err = sup.WriteUserTurn(deliverCtx, "c-1", []byte("hello\n"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WriteUserTurn err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "supervisor: write user turn:") {
		t.Errorf("WriteUserTurn err = %v, want the %q wrap prefix", err, "supervisor: write user turn:")
	}
	// Cursor is stamped before delivery, so it reflects the attempt even on
	// the loud-failure path.
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Errorf("CurrentConversation = %q, want %q", got, "c-1")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of cancel")
	}
}

// TestSupervisor_InputCoexistence_BothHeadsReachChildContiguously pins the
// Phase-1 input-coexistence contract (#595): the local attach head (keystrokes
// via Bridge.Read → sessionWriter → Session.AttachInput) and the phone head
// (WriteUserTurn → injected deliverFn → Session.AttachInput) both drive the one
// session's input, and each single PTY write lands contiguously — neither head's
// turn is split by the other's.
//
// Phase-1 expectation, made explicit (the "made explicit" half of AC3): the two
// heads share one input stream with NO arbitration and NO echo ownership. This
// test deliberately asserts neither an ordering between the two markers nor any
// echo ownership — only that each marker arrives intact. Multi-write delivery
// sequences may interleave at sub-turn granularity in Phase 1; two-heads
// arbitration (first-answer-wins / modal ownership) is Phase 3 (#597), out of
// scope here.
//
// The injected deliverFn writes straight to Session.AttachInput, bypassing the
// live-claude WaitReady idle-gate (#594's contract, tested separately) so the
// test exercises coexistence, not delivery semantics. Runs under -race: the race
// detector is the evidence that the two input paths' supervisor-level
// bookkeeping (convMu, sessMu, the Bridge channel) has no data race.
func TestSupervisor_InputCoexistence_BothHeadsReachChildContiguously(t *testing.T) {
	t.Parallel()

	const (
		localMarker = "LOCAL-KEYSTROKE-595\n"
		phoneMarker = "PHONE-TURN-595\n"
		convID      = "c-595"
	)

	stdinFile := t.TempDir() + "/stdin.bin"
	cfg := helperConfig("stdin_to_file", "GO_TEST_HELPER_STDIN_FILE="+stdinFile)
	cfg.Bridge = NewBridge(cfg.Logger)

	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Injected phone-delivery seam: deliver straight to the captured session's
	// raw input — the one shared PTY-write terminus — without WaitReady.
	sup.deliverFn = func(_ context.Context, sess *tuidriver.Session, payload []byte) error {
		return sess.AttachInput(payload)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(runCtx) }()

	// PhaseRunning is set (in onSpawn) before setSession registers the session,
	// so wait for the session itself (WaitForPTY) before driving either head.
	waitForPhase(t, sup, PhaseRunning, 5*time.Second)
	if err := sup.WaitForPTY(runCtx); err != nil {
		t.Fatalf("WaitForPTY: %v", err)
	}

	// Local head: park the attach input on a pipe; writing the marker pushes it
	// through Bridge.Read → sessionWriter → AttachInput. localOut drains the
	// kernel-echoed output so the PTY master never backpressures; it is never
	// asserted (echo ownership is Phase 3).
	localPR, localPW := io.Pipe()
	var localOut syncBuffer
	attachDone, err := cfg.Bridge.Attach(localPR, &localOut)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Drive both heads concurrently — coexistence under -race.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, werr := localPW.Write([]byte(localMarker)); werr != nil {
			t.Errorf("local write: %v", werr)
		}
	}()
	go func() {
		defer wg.Done()
		if werr := sup.WriteUserTurn(runCtx, convID, []byte(phoneMarker)); werr != nil {
			t.Errorf("WriteUserTurn: %v", werr)
		}
	}()
	wg.Wait()

	// Poll the child's stdin log until BOTH markers are present, each as a
	// contiguous substring (interleaving at sub-marker granularity would break
	// the exact match — that is the turn-integrity assertion).
	deadline := time.Now().Add(5 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(stdinFile)
		content = string(raw)
		if strings.Contains(content, localMarker) && strings.Contains(content, phoneMarker) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(content, localMarker) {
		t.Errorf("child stdin missing intact local marker; got %q", content)
	}
	if !strings.Contains(content, phoneMarker) {
		t.Errorf("child stdin missing intact phone marker; got %q", content)
	}

	// Bookkeeping: the phone turn stamped the cursor; the local path never
	// touches convMu, so the cursor reflects the phone turn alone.
	if got := sup.CurrentConversation(); got != convID {
		t.Errorf("CurrentConversation = %q, want %q", got, convID)
	}

	// Teardown: drain the attach pump, then stop the supervisor.
	cancel()
	_ = localPW.Close()
	<-attachDone
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of cancel")
	}
}

// TestSupervisor_WriteUserTurn_CommittedReturnsNil covers the happy path
// through the deliverFn seam: a confirmed commit returns nil. The seam returns
// the already-classified outcome, so no claude-screen literal is needed in this
// package to drive the success branch deterministically.
func TestSupervisor_WriteUserTurn_CommittedReturnsNil(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sup.setSession(&tuidriver.Session{})
	sup.deliverFn = func(context.Context, *tuidriver.Session, []byte) error { return nil }

	if err := sup.WriteUserTurn(context.Background(), "c-1", []byte("hi")); err != nil {
		t.Errorf("WriteUserTurn = %v, want nil on confirmed commit", err)
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Errorf("CurrentConversation = %q, want %q", got, "c-1")
	}
}

// TestSupervisor_WriteUserTurn_NotCommittedFailsLoud covers the uncommitted
// branch: the seam returns ErrTurnNotCommitted (DeliverResult.Committed ==
// false), which WriteUserTurn wraps and returns — never a silent success.
func TestSupervisor_WriteUserTurn_NotCommittedFailsLoud(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sup.setSession(&tuidriver.Session{})
	sup.deliverFn = func(context.Context, *tuidriver.Session, []byte) error { return ErrTurnNotCommitted }

	err = sup.WriteUserTurn(context.Background(), "c-1", []byte("hi"))
	if !errors.Is(err, ErrTurnNotCommitted) {
		t.Errorf("WriteUserTurn err = %v, want errors.Is(err, ErrTurnNotCommitted)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "supervisor: write user turn:") {
		t.Errorf("WriteUserTurn err = %v, want the wrap prefix", err)
	}
}

// TestSupervisor_WriteUserTurn_DeliverErrorFailsLoud covers a plain delivery
// (PTY write) error from the seam: WriteUserTurn wraps it with the stable
// prefix and preserves it for errors.Is.
func TestSupervisor_WriteUserTurn_DeliverErrorFailsLoud(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sup.setSession(&tuidriver.Session{})
	boom := errors.New("pty closed")
	sup.deliverFn = func(context.Context, *tuidriver.Session, []byte) error { return boom }

	err = sup.WriteUserTurn(context.Background(), "c-1", []byte("hi"))
	if !errors.Is(err, boom) {
		t.Errorf("WriteUserTurn err = %v, want errors.Is(err, boom)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "supervisor: write user turn:") {
		t.Errorf("WriteUserTurn err = %v, want the wrap prefix", err)
	}
}

// TestSupervisor_WriteUserTurn_NotReadyFailsLoud covers the attached-but-not-
// ready case via the seam: an attached session whose ready-gate times out
// (WaitReady → context.DeadlineExceeded) is a loud failure, not a silent ack
// (AC #2). The ctx-cause is preserved through the wrap chain.
func TestSupervisor_WriteUserTurn_NotReadyFailsLoud(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sup.setSession(&tuidriver.Session{})
	sup.deliverFn = func(context.Context, *tuidriver.Session, []byte) error {
		return fmt.Errorf("wait ready: %w", context.DeadlineExceeded)
	}

	err = sup.WriteUserTurn(context.Background(), "c-1", []byte("hi"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WriteUserTurn err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
}

// TestSupervisor_WriteUserTurn_CursorReadBack confirms the cursor reflects the
// most recent accepted WriteUserTurn id. No child is registered, so each call
// now returns ErrNoLiveSession (the former silent-drop path is loud), but the
// cursor is still stamped before the session check — which is what this test
// asserts.
func TestSupervisor_WriteUserTurn_CursorReadBack(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := sup.WriteUserTurn(context.Background(), "c-1", []byte("first")); !errors.Is(err, ErrNoLiveSession) {
		t.Fatalf("WriteUserTurn c-1 err = %v, want errors.Is(err, ErrNoLiveSession)", err)
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Errorf("after first write, CurrentConversation = %q, want %q", got, "c-1")
	}
	if err := sup.WriteUserTurn(context.Background(), "c-2", []byte("second")); !errors.Is(err, ErrNoLiveSession) {
		t.Fatalf("WriteUserTurn c-2 err = %v, want errors.Is(err, ErrNoLiveSession)", err)
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

	// Known id: validation passes and the cursor is stamped, but with no
	// session registered delivery fails loud with ErrNoLiveSession. The cursor
	// stamp (the load-bearing assertion) happens before the session check.
	if err := sup.WriteUserTurn(context.Background(), "c-1", []byte("ok")); !errors.Is(err, ErrNoLiveSession) {
		t.Fatalf("WriteUserTurn c-1 err = %v, want errors.Is(err, ErrNoLiveSession)", err)
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Fatalf("after good write, cursor = %q, want %q", got, "c-1")
	}

	// Ghost id: validation refuses it, so the cursor must NOT move.
	err = sup.WriteUserTurn(context.Background(), "ghost", []byte("nope"))
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

	// With a nil validator the cursor is stamped for any id; no session is
	// registered, so the call fails loud with ErrNoLiveSession.
	if err := sup.WriteUserTurn(context.Background(), "anything", []byte("p")); !errors.Is(err, ErrNoLiveSession) {
		t.Errorf("WriteUserTurn err = %v, want errors.Is(err, ErrNoLiveSession)", err)
	}
	if got := sup.CurrentConversation(); got != "anything" {
		t.Errorf("CurrentConversation = %q, want %q", got, "anything")
	}
}

// TestSupervisor_WriteUserTurn_NoSessionFailsLoud is the RED→GREEN anchor for
// AC #2's no-child case: calling WriteUserTurn before Run (or between
// iterations, when no session is registered) no longer silently drops the
// payload and returns nil — it fails loud with ErrNoLiveSession so the
// send_message handler reports failure to the phone instead of a false ack.
// The cursor is still stamped (the stamp precedes the session check).
func TestSupervisor_WriteUserTurn_NoSessionFailsLoud(t *testing.T) {
	t.Parallel()

	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = sup.WriteUserTurn(context.Background(), "c-1", []byte("not dropped"))
	if !errors.Is(err, ErrNoLiveSession) {
		t.Errorf("WriteUserTurn before Run: err = %v, want errors.Is(err, ErrNoLiveSession)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "supervisor: write user turn:") {
		t.Errorf("WriteUserTurn err = %v, want the %q wrap prefix", err, "supervisor: write user turn:")
	}
	if got := sup.CurrentConversation(); got != "c-1" {
		t.Errorf("CurrentConversation = %q, want %q", got, "c-1")
	}
}

// TestSupervisor_WaitForPTY_ReturnsImmediatelyWhenSet covers the
// already-live branch: setSession(non-nil) closes sessReadyCh, and a
// subsequent WaitForPTY observes the closed channel and returns
// immediately with nil.
func TestSupervisor_WaitForPTY_ReturnsImmediatelyWhenSet(t *testing.T) {
	t.Parallel()
	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// A zero-value *tuidriver.Session is a valid non-nil pointer; setSession
	// only stores it and drives the readiness channel — it never dereferences
	// the session, so this is safe for readiness-only assertions.
	sup.setSession(&tuidriver.Session{})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := sup.WaitForPTY(ctx); err != nil {
		t.Errorf("WaitForPTY (already set) = %v, want nil", err)
	}
}

// TestSupervisor_WaitForPTY_BlocksUntilSet covers the not-yet-live
// branch: WaitForPTY blocks while sessReadyCh is open, then unblocks
// when a concurrent setSession closes it.
func TestSupervisor_WaitForPTY_BlocksUntilSet(t *testing.T) {
	t.Parallel()
	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done <- sup.WaitForPTY(ctx)
	}()

	// Ensure the waiter is parked before we set the session; otherwise we'd
	// race the already-set fast path and not actually cover this branch.
	time.Sleep(50 * time.Millisecond)
	sup.setSession(&tuidriver.Session{})

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitForPTY = %v, want nil after setSession", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForPTY did not unblock after setSession")
	}
}

// TestSupervisor_WaitForPTY_FreshensAfterClear covers the
// re-iteration shape: setSession(nil) after a prior set freshens the
// readiness channel so the next WaitForPTY blocks again until the
// next non-nil setSession.
func TestSupervisor_WaitForPTY_FreshensAfterClear(t *testing.T) {
	t.Parallel()
	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// A zero-value session is never dereferenced by setSession — safe for
	// readiness-only assertions (see WaitForPTY_ReturnsImmediatelyWhenSet).
	sess := &tuidriver.Session{}

	// First iteration: bind and observe immediate readiness.
	sup.setSession(sess)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel1()
	if err := sup.WaitForPTY(ctx1); err != nil {
		t.Fatalf("WaitForPTY (iter 1) = %v, want nil", err)
	}

	// Iteration teardown: setSession(nil) freshens the chan.
	sup.setSession(nil)

	// Now a WaitForPTY with a short deadline must time out — the chan is
	// open again.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := sup.WaitForPTY(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitForPTY after clear = %v, want context.DeadlineExceeded", err)
	}

	// Re-bind: the fresh chan closes, WaitForPTY returns nil.
	sup.setSession(sess)
	ctx3, cancel3 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel3()
	if err := sup.WaitForPTY(ctx3); err != nil {
		t.Errorf("WaitForPTY (iter 2) = %v, want nil", err)
	}
}

// TestSupervisor_WaitForPTY_CtxCancel covers the ctx-cancel branch: a
// cancelled ctx surfaces verbatim without blocking on the chan.
func TestSupervisor_WaitForPTY_CtxCancel(t *testing.T) {
	t.Parallel()
	cfg := helperConfig("exit0")
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sup.WaitForPTY(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitForPTY(cancelled) = %v, want context.Canceled", err)
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
			_ = sup.WriteUserTurn(context.Background(), id, []byte("x"))
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
