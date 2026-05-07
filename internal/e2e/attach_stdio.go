//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// StdioAttachClient is a programmatic peer for `pyry attach --stdio`,
// wired via plain os.Pipe() — no PTY, no terminal, no raw mode. Tests
// drive the supervised session by writing to the client's stdin pipe
// (Write) and reading from its stdout pipe (ReadUntil). Close closes
// the stdin pipe, which propagates EOF through the attach client and
// ends the attach cleanly without destroying the session.
//
// Returned by startStdioAttach. Cleanup is registered via t.Cleanup
// at construction.
type StdioAttachClient struct {
	// SessionID is the id of the session this client is attached to,
	// as returned by control.SessionsNew. Exposed for diagnostics and
	// for tests that want to drive other CLI verbs against the same
	// session.
	SessionID string

	// SocketPath / HomeDir mirror the daemon harness fields.
	SocketPath string
	HomeDir    string

	// Stderr captures the attach client's stderr. Empty in steady
	// state — `--stdio` mode suppresses pyry's own stderr noise — so
	// any content here is a failure diagnostic.
	Stderr *bytes.Buffer

	inputW  *os.File // parent's write end of attach client's stdin
	outputR *os.File // parent's read end of attach client's stdout

	daemonCmd  *exec.Cmd
	daemonDone chan struct{}
	daemonErr  *bytes.Buffer

	attachCmd  *exec.Cmd
	attachDone chan struct{}

	cleanupOnce sync.Once
}

// startStdioAttach brings up a pyry daemon in bridge mode (helper-as-
// claude in echo mode), creates a fresh session via control.SessionsNew
// with the given label, and spawns `pyry attach --stdio <id>` whose
// stdin and stdout are wired to plain os.Pipe()s. Returns a
// StdioAttachClient the test uses to write/read bytes.
//
// label is the human-facing session label passed to sessions.new. Pass
// "" for unlabeled (the server treats nil and empty-Label identically).
//
// Skips the test (t.Skip) if os.Pipe() fails — extremely rare, only
// observed in heavily sandboxed containers without /dev/null-style fd
// allocation. Fails the test on any other startup error (daemon spawn,
// readiness timeout, sessions.new wire error, attach client spawn,
// attach client early exit).
func startStdioAttach(t *testing.T, label string) *StdioAttachClient {
	t.Helper()

	// Probe pipe availability up front. AC#3 — same gating shape as the
	// PTY harness's pty.Open skip.
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

	// Short prefix keeps the socket path under macOS's 104-byte sun_path
	// limit; t.TempDir() embeds the (long) test name and risks overflow.
	home, err := os.MkdirTemp("", "pyry-as-*")
	if err != nil {
		_ = inputR.Close()
		_ = inputW.Close()
		_ = outputR.Close()
		_ = outputW.Close()
		t.Fatalf("e2e: mkdtemp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	socket, daemonCmd, _, daemonErr, daemonDone := spawnAttachableDaemon(t, home)

	c := &StdioAttachClient{
		SocketPath: socket,
		HomeDir:    home,
		Stderr:     &bytes.Buffer{},
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
	attachCmd := exec.Command(bin, "attach", "-pyry-socket="+socket, "--stdio", id)
	attachCmd.Stdin = inputR
	attachCmd.Stdout = outputW
	attachCmd.Stderr = c.Stderr
	attachCmd.Env = childEnv(home)

	if err := attachCmd.Start(); err != nil {
		_ = inputR.Close()
		_ = outputW.Close()
		t.Fatalf("e2e: pyry attach --stdio start: %v", err)
	}

	// The child holds its own dups of inputR and outputW. Close the
	// parent's copies so EOF on outputR corresponds to child-stdout-close
	// (not a reference held by the parent), and so closing inputW from
	// the test propagates EOF to the child's stdin.
	_ = inputR.Close()
	_ = outputW.Close()

	attachDone := make(chan struct{})
	go func() {
		_ = attachCmd.Wait()
		close(attachDone)
	}()
	c.attachCmd = attachCmd
	c.attachDone = attachDone

	// If the attach client dies in handshake, surface that early instead
	// of letting the test wait out its read deadline.
	select {
	case <-attachDone:
		exit := -1
		if attachCmd.ProcessState != nil {
			exit = attachCmd.ProcessState.ExitCode()
		}
		t.Fatalf("e2e: pyry attach --stdio exited before round-trip (exit=%d)\nattach stderr:\n%s\ndaemon stderr:\n%s",
			exit, c.Stderr.String(), daemonErr.String())
	case <-time.After(500 * time.Millisecond):
	}

	return c
}

// Write writes b to the attach client's stdin pipe. Returns the
// underlying os.File.Write result; callers typically expect len(b),
// nil on a healthy attach.
func (c *StdioAttachClient) Write(b []byte) (int, error) {
	return c.inputW.Write(b)
}

// ReadUntil reads from the attach client's stdout pipe in a loop in a
// background goroutine, accumulating bytes, until needle appears in the
// accumulated buffer or the overall deadline elapses. The accumulated
// bytes (including any pre-needle banner emitted by the helper) are
// returned regardless of outcome.
//
// Mirrors readUntilContains in attach_pty_test.go: os.Pipe ends share
// the no-SetReadDeadline trait with PTY masters on darwin, so the
// timeout is enforced by the caller rather than per-read.
func (c *StdioAttachClient) ReadUntil(needle []byte, total time.Duration) ([]byte, error) {
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

// Close closes the attach client's stdin pipe (delivering EOF to the
// child's AttachStdio input loop), waits up to ~2s for the child to
// exit cleanly, and returns its exit code. Subsequent calls return
// the same exit code from the cached ProcessState.
//
// Idempotent. Also called from t.Cleanup; whichever fires first wins.
func (c *StdioAttachClient) Close(t *testing.T) int {
	t.Helper()
	c.teardown(t)
	if c.attachCmd != nil && c.attachCmd.ProcessState != nil {
		return c.attachCmd.ProcessState.ExitCode()
	}
	return -1
}

// teardown is the sync.Once-wrapped cleanup body. Ordering:
//  1. inputW.Close()       — sends EOF to child stdin → clean detach
//  2. wait attachDone (≤ ~2s) → killSpawned(attachCmd) on timeout
//  3. outputR.Close()      — unblocks any in-flight ReadUntil
//  4. killSpawned(daemonCmd)
//  5. os.Remove(socketPath)
func (c *StdioAttachClient) teardown(t *testing.T) {
	t.Helper()
	c.cleanupOnce.Do(func() {
		if c.inputW != nil {
			_ = c.inputW.Close()
		}
		if c.attachCmd != nil && c.attachCmd.Process != nil {
			select {
			case <-c.attachDone:
			case <-time.After(2 * time.Second):
				killSpawned(t, c.attachCmd, c.attachDone)
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
