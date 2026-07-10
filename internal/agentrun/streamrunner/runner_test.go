package streamrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncWriter serialises Write calls for the slog test handler; slog handlers
// may write concurrently from goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}

// helperRunCfg returns a Config wired to TestStreamRunnerHelperProcess.
// Tests override PromptBytes / Env / writers as needed.
func helperRunCfg(t *testing.T, mode string, stdout, stderr *bytes.Buffer, extraEnv ...string) Config {
	t.Helper()
	env := append([]string{
		"GO_STREAMRUNNER_HELPER=1",
		"GO_STREAMRUNNER_HELPER_MODE=" + mode,
	}, extraEnv...)
	return Config{
		ClaudeBin: os.Args[0],
		WorkDir:   t.TempDir(),
		Args: []string{
			"-test.run=TestStreamRunnerHelperProcess",
			"--",
		},
		Stdout: stdout,
		Stderr: stderr,
		Env:    env,
	}
}

func TestRun_CleanExit(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "clean", &stdout, &stderr)
	cfg.PromptBytes = []byte("hi")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{`"type":"system"`, `"type":"assistant"`, `"type":"result"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\nfull stdout:\n%s", want, out)
		}
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr non-empty: %q", stderr.String())
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "exit1", &stdout, &stderr)
	cfg.PromptBytes = []byte("noop")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Run(ctx, cfg)
	if err == nil {
		t.Fatal("Run: got nil, want non-nil from exit-1 child")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run: err = %v (%T), want *exec.ExitError", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("ExitCode = %d, want 1", exitErr.ExitCode())
	}
}

func TestRun_CtxCancelMidRun(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "sleep", &stdout, &stderr)
	cfg.PromptBytes = []byte("noop")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run after ctx cancel: %v, want nil", err)
	}
	elapsed := time.Since(start)
	if elapsed > 6*time.Second {
		t.Errorf("Run took %v, want < 6s (SIGTERM grace likely fell through to SIGKILL)", elapsed)
	}
	if !strings.Contains(stderr.String(), "got SIGTERM") {
		t.Errorf("stderr missing %q (SIGTERM may not have reached child)\nstderr: %q", "got SIGTERM", stderr.String())
	}
}

// reapRecorder is a mutex-guarded double for reapDescendantGroupsFn: it records
// the rootPid of every reap call so the ctx-cancel test can assert the wiring
// fired with a plausible pid — without standing up a real descendant tree (that
// is agentrun.TestReapDescendantGroups' job). Mutex-guarded because cmd.Cancel
// fires from the os/exec ctx-watcher goroutine, not the test goroutine.
type reapRecorder struct {
	mu   sync.Mutex
	pids []int
}

func (r *reapRecorder) record(rootPid int, _ *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pids = append(r.pids, rootPid)
}

func (r *reapRecorder) calls() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.pids...)
}

// swapReapSeam points reapDescendantGroupsFn at rec.record for the test and
// restores the real reaper via t.Cleanup. Callers MUST be non-parallel: they
// mutate a package var. Go runs non-parallel tests — and their t.Cleanup
// restore — to completion before parked t.Parallel() tests resume, so the swap
// window never overlaps the parallel TestRun_* siblings.
func swapReapSeam(t *testing.T, rec *reapRecorder) {
	t.Helper()
	orig := reapDescendantGroupsFn
	reapDescendantGroupsFn = rec.record
	t.Cleanup(func() { reapDescendantGroupsFn = orig })
}

// assertReapedLivePid asserts the reap seam captured at least one call with a
// plausible pid (> 1, matching reap.go's pgid<=1 guard boundary). The test
// can't know the fake-claude's real pid, so it asserts > 1.
func assertReapedLivePid(t *testing.T, rec *reapRecorder) {
	t.Helper()
	calls := rec.calls()
	if len(calls) == 0 {
		t.Fatal("reap seam never fired on the teardown path — reap wiring missing")
	}
	for _, pid := range calls {
		if pid > 1 {
			return
		}
	}
	t.Fatalf("reap seam fired %d time(s) but no call carried a plausible pid > 1: %v", len(calls), calls)
}

// TestRun_CtxCancel_ReapsDescendantGroups asserts the operator-SIGTERM teardown
// reaps claude's detached descendant process groups. Clones TestRun_CtxCancelMidRun
// (sleep helper mode, cancel the parent ctx ~100ms in) and adds the seam swap +
// assertion on top: the reap wired into cmd.Cancel must fire with the live
// fake-claude pid before the SIGTERM. Because cmd.Cancel fires whenever childCtx
// is done — via both the parent ctx (operator SIGTERM/SIGINT) and the watchdog's
// cancelChild — exercising the closure once proves the wiring for both teardown
// paths. Non-parallel: it swaps the reapDescendantGroupsFn package var (see
// swapReapSeam); do NOT add t.Parallel().
func TestRun_CtxCancel_ReapsDescendantGroups(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "sleep", &stdout, &stderr)
	cfg.PromptBytes = []byte("noop")

	rec := &reapRecorder{}
	swapReapSeam(t, rec)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run after ctx cancel: %v, want nil", err)
	}

	assertReapedLivePid(t, rec)
}

func TestRun_EarlyExitChild_NoBenignStdinCloseWarn(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "exit0_no_read", &stdout, &stderr)
	cfg.PromptBytes = []byte("hi")

	logBuf := &syncWriter{}
	cfg.Logger = slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run completes when the child exits 0 — non-zero exit would return a
	// *exec.ExitError, but exit0_no_read exits clean.
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := logBuf.String()
	if strings.Contains(out, "stdin close failed") {
		t.Errorf("captured WARN includes %q (benign EPIPE / ErrClosed misclassified):\n%s",
			"stdin close failed", out)
	}
}

func TestRun_StdinEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	capturePath := t.TempDir() + "/captured"
	cfg := helperRunCfg(t, "echo_stdin", &stdout, &stderr,
		"GO_STREAMRUNNER_HELPER_STDIN_FILE="+capturePath,
	)
	// Deliberately tricky prompt: embedded double-quote, newline, backslash,
	// and a U+0001 control char. JSON-encoded (not shell-escaped), all of
	// these must survive the round-trip.
	prompt := []byte("hello \"world\"\n\\backslash\x01end")
	cfg.PromptBytes = prompt

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	// Envelope must be newline-terminated (matches the probe's `echo '…' |`).
	if !bytes.HasSuffix(captured, []byte{'\n'}) {
		t.Errorf("envelope not newline-terminated: %q", captured)
	}

	var got userTurn
	if err := json.Unmarshal(bytes.TrimRight(captured, "\n"), &got); err != nil {
		t.Fatalf("unmarshal envelope: %v\nraw: %q", err, captured)
	}
	if got.Type != "user" {
		t.Errorf("Type = %q, want %q", got.Type, "user")
	}
	if got.Message.Role != "user" {
		t.Errorf("Message.Role = %q, want %q", got.Message.Role, "user")
	}
	if len(got.Message.Content) != 1 {
		t.Fatalf("len(Message.Content) = %d, want 1", len(got.Message.Content))
	}
	if got.Message.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want %q", got.Message.Content[0].Type, "text")
	}
	if got.Message.Content[0].Text != string(prompt) {
		t.Errorf("Content[0].Text round-trip mismatch:\n got  = %q\n want = %q",
			got.Message.Content[0].Text, string(prompt))
	}
}
