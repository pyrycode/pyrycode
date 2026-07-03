package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/pyrycode/pyrycode/internal/acp"
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

// newFakeClaudePool builds a trimmed ACP pool (bootstrap evicted, service-mode
// bridge) backed by the argv-recording fake claude — the standup runACP performs
// minus the trust-mark. It returns the pool (not yet Run), the argv file the fake
// appends one line to per spawn, the shared logger (a single slog handler, whose
// internal mutex makes concurrent pool+transport writes to stderr race-free), and
// the stderr buffer those diagnostics land in.
func newFakeClaudePool(t *testing.T) (pool *sessions.Pool, argvFile string, logger *slog.Logger, stderr *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	argvFile = filepath.Join(tmp, "argv.txt")
	scriptPath := fakeClaudeScript(t, tmp, argvFile)
	stderr = &bytes.Buffer{}
	logger = testLogger(stderr)
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
	return pool, argvFile, logger, stderr
}

// acpReply is the decode-by-shape view of one JSON-RPC frame the transport writes
// back to the host: id + either a session result or an error object.
type acpReply struct {
	ID     json.RawMessage   `json:"id"`
	Result *newSessionResult `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// acpHarness drives serveACPWithPool over in-memory pipes against a fake-claude
// pool: write a frame with send, read the next reply with read, tear down with
// shutdown. Extends the TestACP_SessionNew harness for the load/cancel tests.
type acpHarness struct {
	t        *testing.T
	writer   *io.PipeWriter
	reader   *bufio.Reader
	served   chan error
	argvFile string
	stderr   *bytes.Buffer
}

func newACPHarness(t *testing.T) *acpHarness {
	t.Helper()
	pool, argvFile, logger, stderr := newFakeClaudePool(t)

	hostToAgentR, hostToAgentW := io.Pipe()
	agentToHostR, agentToHostW := io.Pipe()
	t.Cleanup(func() { _ = hostToAgentW.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	// A cancelled ctx unblocks the serve loop and pool teardown even when a test
	// t.Fatals before shutdown; the goroutine sends to the buffered channel and
	// exits, so nothing leaks.
	t.Cleanup(cancel)

	served := make(chan error, 1)
	go func() { served <- serveACPWithPool(ctx, pool, hostToAgentR, agentToHostW, logger) }()

	return &acpHarness{
		t:        t,
		writer:   hostToAgentW,
		reader:   bufio.NewReader(agentToHostR),
		served:   served,
		argvFile: argvFile,
		stderr:   stderr,
	}
}

// send writes one JSON-RPC frame (a trailing newline is appended) to the agent.
func (h *acpHarness) send(frame string) {
	h.t.Helper()
	if _, err := io.WriteString(h.writer, frame+"\n"); err != nil {
		h.t.Fatalf("write frame: %v", err)
	}
}

// read blocks for the next reply frame and decodes it. A missing frame surfaces
// as the test's own timeout, matching the existing harness's blocking read.
func (h *acpHarness) read() (acpReply, []byte) {
	h.t.Helper()
	line, err := h.reader.ReadBytes('\n')
	if err != nil {
		h.t.Fatalf("read frame: %v", err)
	}
	var r acpReply
	if err := json.Unmarshal(line, &r); err != nil {
		h.t.Fatalf("unmarshal frame %q: %v", line, err)
	}
	return r, line
}

// shutdown closes host stdin (EOF path → clean return) and joins the serve+pool
// goroutines, so a subsequent read of the stderr buffer is race-free.
func (h *acpHarness) shutdown() {
	h.t.Helper()
	if err := h.writer.Close(); err != nil {
		h.t.Fatalf("close host stdin: %v", err)
	}
	select {
	case err := <-h.served:
		if err != nil {
			h.t.Fatalf("serveACPWithPool: want nil on host EOF, got %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Fatal("serveACPWithPool did not return after host EOF")
	}
}

// waitOneClaudeArgv polls argvFile until the fake claude has recorded exactly one
// spawn and returns that spawn's argv fields. It fails if the file ever shows more
// than one line (a second claude — divergence-6 violation) or if nothing lands
// within the deadline. Same argv-line-count proof as the session/new test.
func waitOneClaudeArgv(t *testing.T, argvFile string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(argvFile)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) == 1 && lines[0] != "" {
				return strings.Fields(lines[0])
			}
			if len(lines) > 1 {
				t.Fatalf("claude spawned %d times, want exactly 1 (divergence 6): %q", len(lines), lines)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake claude never recorded its argv")
	return nil
}

// assertNoClaudeSpawned fails if the fake claude recorded any spawn. Sound as a
// synchronous check only on paths that provably never reach Activate (an unknown
// or malformed id errors before the spawn path).
func assertNoClaudeSpawned(t *testing.T, argvFile string) {
	t.Helper()
	data, err := os.ReadFile(argvFile)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read argv file: %v", err)
	}
	if s := strings.TrimSpace(string(data)); s != "" {
		t.Fatalf("want no claude spawned, argv file has: %q", s)
	}
}

// sessionIDParams builds a {"sessionId":id} params object.
func sessionIDParams(id string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"sessionId":%q}`, id))
}

// A UUIDv4-shaped id that no test ever creates, for the unknown-id paths.
const unknownSessionID = "11111111-1111-4111-8111-111111111111"

// TestDecodeSessionID pins the shared param decode both verbs use: a valid
// sessionId round-trips; an absent, empty, non-string, or unparseable params maps
// to CodeInvalidParams. The empty-string case is the Lookup("") bootstrap-trap
// guard.
func TestDecodeSessionID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		params  string
		wantID  sessions.SessionID
		wantErr bool
	}{
		{"valid", `{"sessionId":"abc"}`, "abc", false},
		{"missing key", `{}`, "", true},
		{"empty string", `{"sessionId":""}`, "", true},
		{"non-string", `{"sessionId":5}`, "", true},
		{"null params", `null`, "", true},
		{"garbage", `not json`, "", true},
		{"absent params", ``, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, aerr := decodeSessionID(json.RawMessage(tt.params))
			if tt.wantErr {
				if aerr == nil {
					t.Fatalf("decodeSessionID(%q): want error, got id %q", tt.params, id)
				}
				if aerr.Code != acp.CodeInvalidParams {
					t.Fatalf("decodeSessionID(%q): code = %d, want CodeInvalidParams (%d)", tt.params, aerr.Code, acp.CodeInvalidParams)
				}
				return
			}
			if aerr != nil {
				t.Fatalf("decodeSessionID(%q): unexpected error %v", tt.params, aerr)
			}
			if id != tt.wantID {
				t.Fatalf("decodeSessionID(%q): id = %q, want %q", tt.params, id, tt.wantID)
			}
		})
	}
}

// TestResolveCancelTarget pins AC-4's core: a known-id session/cancel resolves
// onto that session's own supervisor (pointer equality), while an unknown or
// malformed id returns an error and no supervisor. This proves the cancel routing
// T9 (#753) consumes without any abort logic.
func TestResolveCancelTarget(t *testing.T) {
	t.Parallel()
	pool, _, _, _ := newFakeClaudePool(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- pool.Run(ctx) }()
	select {
	case <-pool.Ready():
	case err := <-runErr:
		t.Fatalf("pool.Run returned before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("pool did not become ready")
	}

	id, err := pool.Create(ctx, "")
	if err != nil {
		t.Fatalf("pool.Create: %v", err)
	}

	sup, err := resolveCancelTarget(pool, sessionIDParams(string(id)))
	if err != nil {
		t.Fatalf("resolveCancelTarget known id: %v", err)
	}
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if sup == nil || sup != sess.Supervisor() {
		t.Fatalf("resolveCancelTarget resolved supervisor %p, want the session's %p", sup, sess.Supervisor())
	}

	if _, err := resolveCancelTarget(pool, sessionIDParams(unknownSessionID)); err == nil {
		t.Fatal("resolveCancelTarget unknown id: want error, got nil")
	}
	for _, params := range []string{`{"sessionId":""}`, `{}`, `{"sessionId":5}`, `null`} {
		_, err := resolveCancelTarget(pool, json.RawMessage(params))
		var aerr *acp.Error
		if !errors.As(err, &aerr) || aerr.Code != acp.CodeInvalidParams {
			t.Fatalf("resolveCancelTarget(%s): want *acp.Error CodeInvalidParams, got %v", params, err)
		}
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		t.Fatal("pool.Run did not return after cancel")
	}
}

// TestACP_SessionLoad_ResumesExistingClaude pins AC-1 and AC-3: session/load of a
// known id resumes its supervised claude and returns {"sessionId":id}, and a
// second load of the same id spawns no second claude (divergence 6) — the
// argv-line count stays exactly one across both loads.
func TestACP_SessionLoad_ResumesExistingClaude(t *testing.T) {
	t.Parallel()
	h := newACPHarness(t)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"session/new"}`)
	newReply, line := h.read()
	if newReply.Error != nil {
		t.Fatalf("session/new error: %+v", *newReply.Error)
	}
	if newReply.Result == nil || !sessions.ValidID(newReply.Result.SessionID) {
		t.Fatalf("session/new result = %q, want a valid session id", line)
	}
	id := newReply.Result.SessionID

	// AC-1: known id resumes and echoes the id back.
	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":%q}}`, id))
	loadReply, line := h.read()
	if loadReply.Error != nil {
		t.Fatalf("session/load error: %+v", *loadReply.Error)
	}
	if loadReply.Result == nil || loadReply.Result.SessionID != id {
		t.Fatalf("session/load result = %q, want sessionId %q", line, id)
	}

	// AC-3 / divergence 6: a second load of the same id resumes the existing
	// claude, never a second.
	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":%q}}`, id))
	loadReply2, line := h.read()
	if loadReply2.Error != nil {
		t.Fatalf("second session/load error: %+v", *loadReply2.Error)
	}
	if loadReply2.Result == nil || loadReply2.Result.SessionID != id {
		t.Fatalf("second session/load result = %q, want sessionId %q", line, id)
	}

	fields := waitOneClaudeArgv(t, h.argvFile)
	want := []string{"--session-id", id}
	if !slices.Equal(fields, want) {
		t.Fatalf("claude argv = %v, want %v (one interactive claude for that id)", fields, want)
	}

	h.shutdown()
}

// TestACP_SessionLoad_UnknownID_NoSpawn pins AC-2: session/load of an unknown id
// returns CodeInvalidParams and spawns no claude (Lookup fails before Activate,
// and the bootstrap is evicted).
func TestACP_SessionLoad_UnknownID_NoSpawn(t *testing.T) {
	t.Parallel()
	h := newACPHarness(t)

	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"session/load","params":{"sessionId":%q}}`, unknownSessionID))
	reply, line := h.read()
	if reply.Error == nil {
		t.Fatalf("session/load unknown id: want error, got %q", line)
	}
	if reply.Error.Code != acp.CodeInvalidParams {
		t.Fatalf("session/load unknown id: code = %d, want CodeInvalidParams (%d)", reply.Error.Code, acp.CodeInvalidParams)
	}

	assertNoClaudeSpawned(t, h.argvFile)
	h.shutdown()
}

// TestACP_SessionLoad_MalformedParams pins the decode boundary on the wire: a
// missing, empty, or absent sessionId maps to CodeInvalidParams and spawns
// nothing (each errors before the Activate spawn path).
func TestACP_SessionLoad_MalformedParams(t *testing.T) {
	t.Parallel()
	h := newACPHarness(t)

	frames := []struct {
		name  string
		frame string
	}{
		{"missing sessionId", `{"jsonrpc":"2.0","id":1,"method":"session/load","params":{}}`},
		{"empty sessionId", `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":""}}`},
		{"absent params", `{"jsonrpc":"2.0","id":3,"method":"session/load"}`},
	}
	for _, f := range frames {
		h.send(f.frame)
		reply, line := h.read()
		if reply.Error == nil {
			t.Fatalf("%s: want error, got %q", f.name, line)
		}
		if reply.Error.Code != acp.CodeInvalidParams {
			t.Fatalf("%s: code = %d, want CodeInvalidParams (%d)", f.name, reply.Error.Code, acp.CodeInvalidParams)
		}
	}

	assertNoClaudeSpawned(t, h.argvFile)
	h.shutdown()
}

// TestACP_SessionCancel_NotificationEmitsNoResponse pins AC-4: session/cancel is
// accepted as a notification that emits no JSON-RPC response (proven by the next
// frame being a later request's reply, not a cancel reply), and its resolution is
// logged only on failure. A known-id cancel logs nothing; an unknown-id cancel
// logs one handler error — and neither writes a frame.
func TestACP_SessionCancel_NotificationEmitsNoResponse(t *testing.T) {
	t.Parallel()
	h := newACPHarness(t)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"session/new"}`)
	newReply, line := h.read()
	if newReply.Result == nil {
		t.Fatalf("session/new result = %q, want a session id", line)
	}
	id := newReply.Result.SessionID

	// A known-id cancel notification (no id): routing succeeds, logs nothing.
	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":%q}}`, id))
	// An unknown-id cancel notification: resolution fails and is logged, still no
	// response frame.
	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":%q}}`, unknownSessionID))
	// A follow-up request that DOES reply. Its reply must be the very next frame,
	// proving neither cancel emitted a response.
	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":%q}}`, id))

	reply, line := h.read()
	var gotID uint64
	if err := json.Unmarshal(reply.ID, &gotID); err != nil {
		t.Fatalf("reply id unmarshal %q: %v", line, err)
	}
	if gotID != 2 {
		t.Fatalf("next frame after the cancels has id %d, want 2 — a cancel emitted a response: %q", gotID, line)
	}
	if reply.Error != nil {
		t.Fatalf("follow-up session/load error: %+v", *reply.Error)
	}

	h.shutdown()

	// After shutdown the log buffer is race-free: exactly one notification-handler
	// error — from the unknown-id cancel; the known-id cancel logged none.
	if got := strings.Count(h.stderr.String(), "acp: notification handler error"); got != 1 {
		t.Fatalf("notification handler error count = %d, want 1 (unknown-id cancel only)\nstderr:\n%s", got, h.stderr.String())
	}
}

// handshakeReply is the decode-by-shape view of the single frame the handshake
// handlers write back: id + either a raw result or an error object. Result is
// raw so each test unmarshals it into the concrete result type or inspects it as
// bytes (AC-2b).
type handshakeReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// driveHandshake feeds one JSON-RPC request through serveACP with ONLY the
// handshake handlers registered (registerHandshake, no pool) and runs to EOF.
// Registering no pool is the structural proof of AC-1's "holds no session state,
// succeeds before any session exists": these handlers need nothing else to serve.
// It returns the parsed reply and the raw response line.
func driveHandshake(t *testing.T, request string) (handshakeReply, []byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	if err := serveACP(ctx, strings.NewReader(request+"\n"), &stdout, testLogger(&stderr), registerHandshake); err != nil {
		t.Fatalf("serveACP: %v", err)
	}
	line := bytes.TrimRight(stdout.Bytes(), "\n")
	if len(line) == 0 {
		t.Fatalf("no response frame written for request %q", request)
	}
	var r handshakeReply
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	return r, line
}

// TestACP_Initialize_Result pins AC-1 and AC-2: an initialize request driven
// through the transport returns a well-formed result declaring pyry's protocol
// version and its minimal agent capabilities (AC-2a), and that result requests no
// host fs/terminal capability (AC-2b). The host here OFFERS fs+terminal — pyry
// tolerates and ignores them (divergence 5).
func TestACP_Initialize_Result(t *testing.T) {
	t.Parallel()
	reply, line := driveHandshake(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true}}}`)

	if reply.Error != nil {
		t.Fatalf("initialize returned error: %+v", *reply.Error)
	}
	var gotID uint64
	if err := json.Unmarshal(reply.ID, &gotID); err != nil {
		t.Fatalf("reply id unmarshal %q: %v", line, err)
	}
	if gotID != 1 {
		t.Fatalf("reply id = %d, want 1 (echoed request id)", gotID)
	}

	// AC-2a: the declared capability set.
	var res initializeResult
	if err := json.Unmarshal(reply.Result, &res); err != nil {
		t.Fatalf("unmarshal result %q: %v", reply.Result, err)
	}
	if res.ProtocolVersion != SupportedProtocolVersion {
		t.Fatalf("protocolVersion = %d, want %d", res.ProtocolVersion, SupportedProtocolVersion)
	}
	if res.AgentCapabilities.LoadSession {
		t.Fatal("agentCapabilities.loadSession = true, want false (session/load resume not advertised yet)")
	}
	if pc := res.AgentCapabilities.PromptCapabilities; pc.Image || pc.Audio || pc.EmbeddedContext {
		t.Fatalf("promptCapabilities = %+v, want all false", pc)
	}

	// authMethods must marshal as [], not null.
	if !bytes.Contains(reply.Result, []byte(`"authMethods":[]`)) {
		t.Fatalf("result %q must contain authMethods:[] (empty array, not null)", reply.Result)
	}

	// AC-2b: pyry's result requests no host filesystem or terminal capability —
	// none of these substrings appear in the marshalled result.
	for _, sub := range []string{`"fs"`, `"terminal"`, `"readTextFile"`, `"writeTextFile"`} {
		if bytes.Contains(reply.Result, []byte(sub)) {
			t.Fatalf("result %q requests host capability %s (divergence 5 violation)", reply.Result, sub)
		}
	}
}

// TestACP_Initialize_ParamsTolerance pins that initialize succeeds with the same
// capability set whether the host sends no params, an empty object, or an object
// carrying only unmodelled capabilities — the divergence-5 "accept and ignore"
// path — while a params that is not an object is rejected with CodeInvalidParams.
func TestACP_Initialize_ParamsTolerance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		frame     string
		wantError bool
	}{
		{"absent params", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, false},
		{"empty object", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, false},
		{"only client capabilities", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientCapabilities":{"terminal":true}}}`, false},
		{"array params", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":[1,2,3]}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reply, line := driveHandshake(t, tt.frame)
			if tt.wantError {
				if reply.Error == nil {
					t.Fatalf("want error, got result %q", line)
				}
				if reply.Error.Code != acp.CodeInvalidParams {
					t.Fatalf("code = %d, want CodeInvalidParams (%d)", reply.Error.Code, acp.CodeInvalidParams)
				}
				return
			}
			if reply.Error != nil {
				t.Fatalf("want success, got error %+v", *reply.Error)
			}
			var res initializeResult
			if err := json.Unmarshal(reply.Result, &res); err != nil {
				t.Fatalf("unmarshal result %q: %v", reply.Result, err)
			}
			if res.ProtocolVersion != SupportedProtocolVersion {
				t.Fatalf("protocolVersion = %d, want %d", res.ProtocolVersion, SupportedProtocolVersion)
			}
		})
	}
}

// TestACP_Authenticate_Success pins AC-3: authenticate returns success without
// any real authentication, echoing the request id, with an empty-object result.
func TestACP_Authenticate_Success(t *testing.T) {
	t.Parallel()
	reply, line := driveHandshake(t, `{"jsonrpc":"2.0","id":2,"method":"authenticate","params":{"methodId":"whatever"}}`)

	if reply.Error != nil {
		t.Fatalf("authenticate returned error: %+v", *reply.Error)
	}
	var gotID uint64
	if err := json.Unmarshal(reply.ID, &gotID); err != nil {
		t.Fatalf("reply id unmarshal %q: %v", line, err)
	}
	if gotID != 2 {
		t.Fatalf("reply id = %d, want 2 (echoed request id)", gotID)
	}
	if got := string(bytes.TrimSpace(reply.Result)); got != "{}" {
		t.Fatalf("authenticate result = %q, want {} (empty object)", got)
	}
}
