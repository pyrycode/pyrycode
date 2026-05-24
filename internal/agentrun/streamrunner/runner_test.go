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
