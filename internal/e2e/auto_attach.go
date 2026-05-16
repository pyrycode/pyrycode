//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// ForegroundAutoAttachClient is a programmatic peer for
// `pyry --session-id <uuid>` invoked as the foreground binary while
// a daemon already hosts the UUID. Wired via plain os.Pipe() — no
// PTY, no terminal, no raw mode. Mirrors StdioAttachClient's surface
// so tests share the Write / ReadUntil / Close contract.
//
// The crucial difference from StdioAttachClient: the spawned process
// is `pyry --session-id <uuid> …` (no `attach` verb), exercising the
// auto-attach gate in tryAutoAttach. control.AttachStdio is called
// in-process by the foreground pyry, not via the `pyry attach --stdio`
// verb (which is blocked on #167).
type ForegroundAutoAttachClient struct {
	// SessionID is the id of the session this client is attached to,
	// as returned by control.SessionsNew.
	SessionID string

	// SocketPath / HomeDir mirror the daemon harness fields.
	SocketPath string
	HomeDir    string

	// Stderr captures the foreground pyry's stderr. Empty in steady
	// state — auto-attach inherits --stdio's no-affordance behaviour —
	// so any content here is a failure diagnostic. Mutex-protected so
	// the test goroutine can safely snapshot it via StderrString while
	// os/exec's I/O copier goroutine may still be writing.
	Stderr *safeBuffer

	// Pid is the foreground pyry process pid. Exposed so the test
	// (and #164's siblings) can call pgrepChildren(Pid) for the
	// no-claude-child assertion.
	Pid int

	inputW  *os.File // parent's write end of foreground pyry's stdin
	outputR *os.File // parent's read end of foreground pyry's stdout

	daemonCmd  *exec.Cmd
	daemonDone chan struct{}
	daemonErr  *bytes.Buffer

	foregroundCmd  *exec.Cmd
	foregroundDone chan struct{}

	cleanupOnce sync.Once
}

// safeBuffer is a bytes.Buffer guarded by a sync.Mutex so concurrent
// Write (from os/exec's stderr-copy goroutine) and read access (from
// the test goroutine, e.g. inside a t.Fatalf diagnostic) don't race.
// Same shape as the unexported lockedBuffer in net/http/httptest, kept
// local to this file to avoid a cross-cutting refactor of
// StdioAttachClient.Stderr (which has the same latent race but is
// only reached from a t.Skip'd test today).
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns a snapshot of the bytes written so far.
func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Bytes returns a snapshot of the bytes written so far. The returned
// slice is a copy and is safe to retain past the next Write call.
func (b *safeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.buf.Bytes()
	out := make([]byte, len(src))
	copy(out, src)
	return out
}

// echoClaudeScript is a /bin/sh wrapper around the e2e test binary
// running TestHelperProcess in echo mode. The script ignores its own
// argv, so Pool.Create's `--session-id <uuid>` suffix (appended on
// every non-bootstrap session) doesn't reach the Go test binary's
// flag parser, which would otherwise reject `-session-id` and exit 2
// before TestHelperProcess runs. Mirrors writeSleepClaude's pattern
// from cap_test.go for the same reason; this one preserves the echo
// helper instead of a sleep stand-in.
//
// E2E_HELPER_BIN must be exported in the daemon's environment so
// supervisor.runOnce flows it through to the wrapper. Helper-mode
// env vars (GO_TEST_HELPER_PROCESS, GO_TEST_HELPER_MODE) are
// preserved across exec by the shell.
const echoClaudeScript = `#!/bin/sh
exec "$E2E_HELPER_BIN" -test.run=TestHelperProcess
`

func writeEchoClaude(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, "echo-claude.sh")
	if err := os.WriteFile(path, []byte(echoClaudeScript), 0o755); err != nil {
		t.Fatalf("write echo-claude script: %v", err)
	}
	return path
}

// startForegroundAutoAttach brings up a pyry daemon (helper-as-claude
// echo mode via a shell wrapper that ignores Pool.Create's appended
// --session-id), creates a session via control.SessionsNew with
// `label`, then spawns a SECOND pyry process invoked as the foreground
// binary (`pyry -pyry-socket=<sock> -- --session-id <uuid>
//        --input-format stream-json --output-format stream-json`)
// with stdin/stdout wired to plain os.Pipe()s.
//
// Returns once the foreground process has been alive past a 500ms
// settle window without exiting (mirrors startStdioAttach's
// early-death detector).
//
// Skips on os.Pipe() failure (heavily-sandboxed CI). Fatals on any
// other startup error.
func startForegroundAutoAttach(t *testing.T, label string) *ForegroundAutoAttachClient {
	t.Helper()

	inputR, inputW, err := os.Pipe()
	if err != nil {
		t.Skipf("e2e: os.Pipe unavailable: %v", err)
	}
	outputR, outputW, err := os.Pipe()
	if err != nil {
		_ = inputR.Close()
		_ = inputW.Close()
		t.Skipf("e2e: os.Pipe unavailable: %v", err)
	}

	// Short prefix keeps the socket path under macOS's 104-byte
	// sun_path limit; t.TempDir() embeds the (long) test name and
	// risks overflow.
	home, err := os.MkdirTemp("", "pyry-aa-*")
	if err != nil {
		_ = inputR.Close()
		_ = inputW.Close()
		_ = outputR.Close()
		_ = outputW.Close()
		t.Fatalf("e2e: mkdtemp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	socket, daemonCmd, daemonErr, daemonDone := spawnAutoAttachDaemon(t, home)

	c := &ForegroundAutoAttachClient{
		SocketPath: socket,
		HomeDir:    home,
		Stderr:     &safeBuffer{},
		inputW:     inputW,
		outputR:    outputR,
		daemonCmd:  daemonCmd,
		daemonDone: daemonDone,
		daemonErr:  daemonErr,
	}
	t.Cleanup(func() { c.teardown(t) })

	if err := waitDaemonReady(socket, daemonDone, daemonErr); err != nil {
		_ = inputR.Close()
		_ = outputW.Close()
		t.Fatalf("e2e: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := control.SessionsNew(ctx, socket, label)
	if err != nil {
		_ = inputR.Close()
		_ = outputW.Close()
		t.Fatalf("e2e: sessions.new: %v", err)
	}
	c.SessionID = id

	bin := ensurePyryBuilt(t)
	// No `attach` verb: this is the *foreground binary* invocation
	// shape (Claudian's "claude binary path" pointing at pyry). The
	// `--` separator is defensive — splitArgs already tips into
	// claudeArgs at the first non-pyry flag, but `--` pins intent
	// and is forward-compatible.
	foregroundCmd := exec.Command(bin,
		"-pyry-socket="+socket,
		"--",
		"--session-id", id,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
	)
	foregroundCmd.Stdin = inputR
	foregroundCmd.Stdout = outputW
	foregroundCmd.Stderr = c.Stderr
	foregroundCmd.Env = childEnv(home)

	if err := foregroundCmd.Start(); err != nil {
		_ = inputR.Close()
		_ = outputW.Close()
		t.Fatalf("e2e: foreground pyry start: %v", err)
	}

	// The child holds its own dups of inputR and outputW. Close the
	// parent's copies so EOF semantics are precise.
	_ = inputR.Close()
	_ = outputW.Close()

	foregroundDone := make(chan struct{})
	go func() {
		_ = foregroundCmd.Wait()
		close(foregroundDone)
	}()
	c.foregroundCmd = foregroundCmd
	c.foregroundDone = foregroundDone
	c.Pid = foregroundCmd.Process.Pid

	// If the foreground pyry dies in handshake, surface that early
	// instead of letting the test wait out its read deadline.
	select {
	case <-foregroundDone:
		exit := -1
		if foregroundCmd.ProcessState != nil {
			exit = foregroundCmd.ProcessState.ExitCode()
		}
		t.Fatalf("e2e: foreground pyry exited before round-trip (exit=%d)\nforeground stderr:\n%s\ndaemon stderr:\n%s",
			exit, c.Stderr.String(), daemonErr.String())
	case <-time.After(500 * time.Millisecond):
	}

	return c
}

// spawnAutoAttachDaemon spawns pyry in bridge mode supervising the
// echo-claude shell wrapper (echoClaudeScript). Variant of
// spawnAttachableDaemon that swaps `-pyry-claude=os.Args[0]` for the
// wrapper script so SessionsNew's appended `--session-id <uuid>`
// doesn't crash the supervised Go test binary's flag parser.
//
// The wrapper's exec target is the e2e test binary, passed via
// E2E_HELPER_BIN so the script can locate it without embedded
// quoting. supervisor.runOnce passes the daemon's env through to the
// supervised process unchanged, so the helper-mode env vars and
// E2E_HELPER_BIN both reach the supervised wrapper, then the wrapper's
// exec preserves them for TestHelperProcess.
func spawnAutoAttachDaemon(t *testing.T, home string) (string, *exec.Cmd, *bytes.Buffer, chan struct{}) {
	t.Helper()
	bin := ensurePyryBuilt(t)
	socket := filepath.Join(home, "pyry.sock")
	claudeBin := writeEchoClaude(t, home)

	stderr := &bytes.Buffer{}

	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=" + claudeBin,
		"-pyry-idle-timeout=0",
		// ResumeLast prepends --continue on respawn; the wrapper
		// ignores its argv anyway, but disable for parity with
		// spawnAttachableDaemon.
		"-pyry-resume=false",
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = stderr
	cmd.Env = append(childEnv(home),
		"GO_TEST_HELPER_PROCESS=1",
		"GO_TEST_HELPER_MODE=echo",
		"E2E_HELPER_BIN="+os.Args[0],
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("e2e: pyry auto-attach daemon start: %v", err)
	}

	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	return socket, cmd, stderr, doneCh
}

// Write writes b to the foreground pyry's stdin pipe.
func (c *ForegroundAutoAttachClient) Write(b []byte) (int, error) {
	return c.inputW.Write(b)
}

// ReadUntil reads from the foreground pyry's stdout pipe until needle
// appears in the accumulated buffer or the overall deadline elapses.
// Mirrors StdioAttachClient.ReadUntil.
func (c *ForegroundAutoAttachClient) ReadUntil(needle []byte, total time.Duration) ([]byte, error) {
	type readResult struct {
		buf []byte
		err error
	}
	ch := make(chan readResult, 1)
	var seen bytes.Buffer

	read := func() {
		buf := make([]byte, 4096)
		n, err := c.outputR.Read(buf)
		ch <- readResult{buf: buf[:n], err: err}
	}

	deadline := time.Now().Add(total)
	go read()
	for {
		select {
		case res := <-ch:
			if len(res.buf) > 0 {
				seen.Write(res.buf)
				if bytes.Contains(seen.Bytes(), needle) {
					return seen.Bytes(), nil
				}
			}
			if res.err != nil {
				return seen.Bytes(), fmt.Errorf("read: %w (seen %q)", res.err, seen.Bytes())
			}
			go read()
		case <-time.After(time.Until(deadline)):
			return seen.Bytes(), fmt.Errorf("timeout after %s; seen %d bytes: %q", total, seen.Len(), seen.Bytes())
		}
	}
}

// Close closes the foreground pyry's stdin pipe and tears the harness
// down. Idempotent.
func (c *ForegroundAutoAttachClient) Close(t *testing.T) int {
	t.Helper()
	c.teardown(t)
	if c.foregroundCmd != nil && c.foregroundCmd.ProcessState != nil {
		return c.foregroundCmd.ProcessState.ExitCode()
	}
	return -1
}

// teardown ordering mirrors StdioAttachClient.teardown:
//  1. inputW.Close()              — EOF to foreground stdin → clean detach
//  2. wait foregroundDone (≤ 2s) → killSpawned on timeout
//  3. outputR.Close()             — unblocks any in-flight ReadUntil
//  4. killSpawned(daemonCmd)
//  5. os.Remove(socketPath)
func (c *ForegroundAutoAttachClient) teardown(t *testing.T) {
	t.Helper()
	c.cleanupOnce.Do(func() {
		if c.inputW != nil {
			_ = c.inputW.Close()
		}
		if c.foregroundCmd != nil && c.foregroundCmd.Process != nil {
			select {
			case <-c.foregroundDone:
			case <-time.After(2 * time.Second):
				killSpawned(t, c.foregroundCmd, c.foregroundDone)
			}
		}
		if c.outputR != nil {
			_ = c.outputR.Close()
		}
		if c.daemonCmd != nil && c.daemonCmd.Process != nil {
			killSpawned(t, c.daemonCmd, c.daemonDone)
		}
		_ = os.Remove(c.SocketPath)
	})
}

// ForegroundSupervisedClient is a programmatic peer for a foreground
// pyry binary configured to fall through auto-attach into supervisor
// mode. Unlike ForegroundAutoAttachClient (which observes a successful
// auto-attach via stdio round-trip), this client observes only the
// process tree — fallback tests don't need bytes to flow through stdio.
type ForegroundSupervisedClient struct {
	// Pid is the foreground pyry process pid. Pass to pgrepChildren
	// for the supervisor-mode assertion.
	Pid int

	// Stderr captures the foreground pyry's stderr (mutex-protected so
	// concurrent writer/reader doesn't race). Mostly used for failure
	// diagnostics — supervisor mode emits log lines here in steady
	// state, so non-empty stderr is expected.
	Stderr *safeBuffer

	// SocketPath / HomeDir are the foreground's *own* socket and home,
	// distinct from any daemon spawned by the test. Exposed for
	// diagnostics.
	SocketPath string
	HomeDir    string

	cmd         *exec.Cmd
	done        chan struct{}
	stdinSink   *os.File
	cleanupOnce sync.Once
}

// startForegroundSupervised spawns a foreground pyry binary expected
// to fall through auto-attach (its own socket doesn't exist before
// runSupervisor's Listen runs) and enter supervisor mode with the
// sleep-claude stand-in.
//
// `sessionID` is appended to the foreground's claudeArgs as
// `--session-id <id>` so extractSessionID returns non-empty (mirrors
// real-world Claudian invocations, which always supply --session-id).
//
// `extraEnv` is appended verbatim to childEnv(home) before exec, so
// the test can inject PYRY_NO_AUTO_ATTACH=1 without polluting any
// other process's env. Pass nil when no extra env is needed.
//
// Returns once the foreground has both:
//  1. Survived a 500ms settle window without exiting (early-death
//     detector mirrors startForegroundAutoAttach's pattern), AND
//  2. Acquired at least one direct child PID (polled via
//     pgrepChildren with a 5s deadline).
//
// Skips on pgrep unavailability (sandboxed CI).
func startForegroundSupervised(t *testing.T, sessionID string, extraEnv []string) *ForegroundSupervisedClient {
	t.Helper()

	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skipf("e2e: pgrep unavailable: %v", err)
	}

	// Short prefix keeps the socket path under macOS's 104-byte
	// sun_path limit; t.TempDir() embeds the long test name.
	home, err := os.MkdirTemp("", "pyry-fb-*")
	if err != nil {
		t.Fatalf("e2e: mkdtemp foreground home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	socketPath := filepath.Join(home, "pyry.sock")
	claudeBin := writeSleepClaude(t, home)
	bin := ensurePyryBuilt(t)

	stdinSink, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("e2e: open /dev/null: %v", err)
	}

	stderr := &safeBuffer{}
	cmd := exec.Command(bin,
		"-pyry-socket="+socketPath,
		"-pyry-claude="+claudeBin,
		"-pyry-idle-timeout=0",
		"-pyry-resume=false",
		"--",
		"--session-id", sessionID,
	)
	cmd.Stdin = stdinSink
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	cmd.Env = append(childEnv(home), extraEnv...)

	if err := cmd.Start(); err != nil {
		_ = stdinSink.Close()
		t.Fatalf("e2e: foreground pyry start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	c := &ForegroundSupervisedClient{
		Pid:        cmd.Process.Pid,
		Stderr:     stderr,
		SocketPath: socketPath,
		HomeDir:    home,
		cmd:        cmd,
		done:       done,
		stdinSink:  stdinSink,
	}
	t.Cleanup(func() { c.teardown(t) })

	select {
	case <-done:
		exit := -1
		if cmd.ProcessState != nil {
			exit = cmd.ProcessState.ExitCode()
		}
		t.Fatalf("e2e: foreground pyry exited before settle (exit=%d)\nstderr:\n%s",
			exit, stderr.String())
	case <-time.After(500 * time.Millisecond):
	}

	if err := awaitClaudeChild(c.Pid, 5*time.Second); err != nil {
		t.Fatalf("e2e: foreground pyry pid=%d did not spawn claude child: %v\nstderr:\n%s",
			c.Pid, err, stderr.String())
	}

	return c
}

// teardown SIGTERMs the foreground, waits up to 2s, escalates to
// SIGKILL via killSpawned, then removes the socket file. Idempotent.
func (c *ForegroundSupervisedClient) teardown(t *testing.T) {
	t.Helper()
	c.cleanupOnce.Do(func() {
		if c.stdinSink != nil {
			_ = c.stdinSink.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			killSpawned(t, c.cmd, c.done)
		}
		_ = os.Remove(c.SocketPath)
	})
}

// awaitClaudeChild polls pgrepChildren(pid) every 50ms until at least
// one child appears or `total` elapses. The settle window in
// startForegroundSupervised closes the early-exit race; this poll
// closes the supervisor-spawn race (pgrep can outrun the first
// os.StartProcess inside Pool.Run).
func awaitClaudeChild(pid int, total time.Duration) error {
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		children, err := pgrepChildren(pid)
		if err != nil {
			return err
		}
		if len(children) > 0 {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("no children after %s", total)
}

// spawnDaemonWithRegisteredSession spawns an auto-attach daemon at a
// home/socket path *separate from* any foreground binary's socket
// (see #164's spec, "Why separate sockets"). Registers `label` via
// SessionsNew and returns the daemon's socket path plus the
// registered session id, so tests can later call
// control.SessionsList(socket) to assert the registry is unchanged.
//
// The daemon is auto-cleaned up via t.Cleanup.
func spawnDaemonWithRegisteredSession(t *testing.T, label string) (socket, sessionID string) {
	t.Helper()

	home, err := os.MkdirTemp("", "pyry-fbd-*")
	if err != nil {
		t.Fatalf("e2e: mkdtemp daemon home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	socket, daemonCmd, daemonErr, daemonDone := spawnAutoAttachDaemon(t, home)
	t.Cleanup(func() {
		killSpawned(t, daemonCmd, daemonDone)
		_ = os.Remove(socket)
	})

	if err := waitDaemonReady(socket, daemonDone, daemonErr); err != nil {
		t.Fatalf("e2e: daemon ready: %v\nstderr:\n%s", err, daemonErr.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := control.SessionsNew(ctx, socket, label)
	if err != nil {
		t.Fatalf("e2e: sessions.new: %v", err)
	}
	return socket, id
}

// pgrepChildren returns the PIDs whose direct parent is pid, or an
// error if the inspection mechanism is unavailable on this platform
// (caller should t.Skip).
//
// Uses `pgrep -P <pid>`, which works identically on macOS and Linux
// and is the simplest cross-platform parent-child query. pgrep exits
// 1 with empty output when no children exist; that case returns
// (nil, nil) — the success path for #163's assertion.
func pgrepChildren(pid int) ([]int, error) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		return nil, fmt.Errorf("pgrep not on PATH: %w", err)
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		// pgrep exits 1 when no matches. Distinguish "no children"
		// (success) from real errors (e.g. exec failure) via
		// ExitError + empty stdout.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && len(out) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("pgrep -P %d: %w", pid, err)
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n, parseErr := strconv.Atoi(line)
		if parseErr != nil {
			return nil, fmt.Errorf("pgrep -P %d: parse %q: %w", pid, line, parseErr)
		}
		pids = append(pids, n)
	}
	return pids, nil
}
