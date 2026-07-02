package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// discardLogger builds a logger writing to w so tests can assert diagnostics
// land there (never on the stdout frame stream).
func testLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestServeACP_EOFReturnsNil pins AC#3: when stdin is already at EOF the serve
// loop returns nil and writes nothing to the frame stream.
func TestServeACP_EOFReturnsNil(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	err := serveACP(ctx, strings.NewReader(""), &stdout, testLogger(&stderr), nil)
	if err != nil {
		t.Fatalf("serveACP at EOF: want nil, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("no handlers registered, want empty stdout, got %q", stdout.String())
	}
}

// TestServeACP_BlankLinesThenEOF pins AC#3: whitespace-only lines are skipped
// by the transport and EOF still returns nil with nothing on the frame stream.
func TestServeACP_BlankLinesThenEOF(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	err := serveACP(ctx, strings.NewReader("\n  \n\t\n"), &stdout, testLogger(&stderr), nil)
	if err != nil {
		t.Fatalf("serveACP with blank lines: want nil, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("blank lines produce no frames, want empty stdout, got %q", stdout.String())
	}
}

// TestServeACP_BlockedReadUnblocksOnCancel is the load-bearing test (AC#4): a
// stdin read blocked on a quiet host must be unblocked when the context is
// cancelled, so the serve loop returns promptly instead of hanging until the
// next line arrives. Regression guard for the #78 blocked-read leak.
func TestServeACP_BlockedReadUnblocksOnCancel(t *testing.T) {
	t.Parallel()

	// The read end of an io.Pipe with nothing written blocks forever — a quiet
	// host. Closing the write end in cleanup drains the bridge goroutine so the
	// test leaks nothing.
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- serveACP(ctx, stdinR, &stdout, testLogger(&stderr), nil) }()

	// The read genuinely blocks: without cancel/EOF, serveACP cannot return.
	select {
	case err := <-done:
		t.Fatalf("serveACP returned before cancel with %v; the read did not block", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("signal-driven shutdown: want nil (clean exit), got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveACP did not return within 2s of cancel — blocked read was not unblocked (#78 regression)")
	}
	if stdout.Len() != 0 {
		t.Fatalf("shutdown must not emit frames, got %q", stdout.String())
	}
}

// TestServeACP_StreamBreakReturnsError pins AC#5: a genuine stream break while
// not shutting down maps to a wrapped error (exit non-zero), with the detail on
// the diagnostics stream, never the frame stream.
func TestServeACP_StreamBreakReturnsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A single line longer than the transport's maxLineBytes (16 MiB) with no
	// newline is structurally broken: the scanner surfaces bufio.ErrTooLong.
	overlong := strings.Repeat("a", 16<<20+1)

	var stdout, stderr bytes.Buffer
	err := serveACP(ctx, strings.NewReader(overlong), &stdout, testLogger(&stderr), nil)
	if err == nil {
		t.Fatal("over-long line: want a wrapped error, got nil")
	}
	if !strings.Contains(err.Error(), "acp:") {
		t.Fatalf("want error wrapped with 'acp:', got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stream break must not emit frames, got %q", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("stream break detail should be logged to the diagnostics stream")
	}
}

// TestRunACP_RejectsArgs pins AC#1: the subcommand takes no arguments and the
// guard fires before any stdin side effect (so this never blocks).
func TestRunACP_RejectsArgs(t *testing.T) {
	t.Parallel()
	err := runACP([]string{"unexpected"})
	if err == nil {
		t.Fatal("runACP with an argument: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("want the error to name the unexpected argument, got %v", err)
	}
}

// fakeClaudeScript writes an executable shell "claude" that appends its argv
// (one line per spawn) to argvFile, emits a screen-output marker to its PTY
// slave, then blocks until killed. The argvFile path is baked in so no env-var
// plumbing is needed; the marker proves claude's PTY output never reaches the
// JSON-RPC frame stream. A plain shell wrapper (not a TestHelperProcess re-exec)
// sidesteps the flag-parse rejection the Go test binary hits on the
// --session-id <uuid> Pool.Create appends (docs/lessons.md).
func fakeClaudeScript(t *testing.T, dir, argvFile string) string {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	path := filepath.Join(dir, "fake-claude.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nprintf 'CLAUDE_SCREEN_OUTPUT\\n'\nexec sleep 3600\n", argvFile)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

// TestACP_SessionNew_SpawnsOneInteractiveClaude drives session/new end-to-end
// over the transport against a fake-claude pool and pins AC-1..AC-4: exactly one
// interactive claude (--session-id <uuid>, no -p/--print) spawns per session/new,
// the response carries that valid UUID, and claude's screen output stays off the
// frame stream.
func TestACP_SessionNew_SpawnsOneInteractiveClaude(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	argvFile := filepath.Join(tmp, "argv.txt")
	scriptPath := fakeClaudeScript(t, tmp, argvFile)

	var stderr bytes.Buffer
	logger := testLogger(&stderr)
	pool, err := sessions.New(sessions.Config{
		Logger:           logger,
		BootstrapEvicted: true,
		Bootstrap: sessions.SessionConfig{
			ClaudeBin: scriptPath,
			WorkDir:   tmp,
			Bridge:    supervisor.NewBridge(logger),
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}

	// Paired pipes: host writes requests to hostToAgentW; the agent's replies
	// arrive on agentToHostR.
	hostToAgentR, hostToAgentW := io.Pipe()
	agentToHostR, agentToHostW := io.Pipe()
	t.Cleanup(func() { _ = hostToAgentW.Close() }) // safety net for early t.Fatal

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	served := make(chan error, 1)
	go func() { served <- serveACPWithPool(ctx, pool, hostToAgentR, agentToHostW, logger) }()

	if _, err := io.WriteString(hostToAgentW, `{"jsonrpc":"2.0","id":1,"method":"session/new"}`+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	line, err := bufio.NewReader(agentToHostR).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	// AC-1: the frame stream carries only JSON-RPC — never claude screen bytes.
	if bytes.Contains(line, []byte("CLAUDE_SCREEN_OUTPUT")) {
		t.Fatalf("claude screen output leaked onto the frame stream: %q", line)
	}
	var resp struct {
		Result *newSessionResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	if resp.Error != nil {
		t.Fatalf("session/new returned error: %+v", *resp.Error)
	}
	// AC-2: a valid session id the host can address in later calls.
	if resp.Result == nil || !sessions.ValidID(resp.Result.SessionID) {
		t.Fatalf("response = %q, want result.sessionId to be a valid UUID", line)
	}

	// AC-3/AC-4: exactly one spawn, on the interactive path. The child records
	// argv after exec, so poll until the line lands.
	var fields []string
	ok := func() bool {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(argvFile)
			if err == nil {
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) == 1 && lines[0] != "" {
					fields = strings.Fields(lines[0])
					return true
				}
				if len(lines) > 1 {
					t.Fatalf("claude spawned %d times, want exactly 1 (divergence 6): %q", len(lines), lines)
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}()
	if !ok {
		t.Fatal("fake claude never recorded its argv")
	}
	// Exactly `--session-id <uuid>` — asserts the interactive path (a -p/--print
	// argv would fail this equality) and the returned id round-trips to the spawn.
	want := []string{"--session-id", resp.Result.SessionID}
	if !slices.Equal(fields, want) {
		t.Fatalf("claude argv = %v, want %v (interactive path, no -p/--print)", fields, want)
	}

	// Shutdown: host EOF → clean return, pool tears the claude down.
	if err := hostToAgentW.Close(); err != nil {
		t.Fatalf("close host stdin: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serveACPWithPool: want nil on host EOF, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveACPWithPool did not return after host EOF")
	}
}
