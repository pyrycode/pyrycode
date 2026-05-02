//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStop_E2E spawns the daemon, runs `pyry stop`, and observes externally
// that the daemon process has exited and the socket file is gone.
func TestStop_E2E(t *testing.T) {
	h := Start(t)
	pid := h.PID
	sock := h.SocketPath

	r := h.Run(t, "stop")
	if r.ExitCode != 0 {
		t.Fatalf("pyry stop exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	if !bytes.Contains(r.Stdout, []byte("stop requested")) {
		t.Errorf("stop stdout missing %q fragment:\n%s", "stop requested", r.Stdout)
	}

	// Bounded poll: stop returns once the server acknowledges, but the
	// daemon may still be unwinding. Wait for both the process to be
	// gone AND the socket file to be removed (the supervisor's deferred
	// cleanup runs after Wait returns).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		alive := processAlive(pid)
		_, statErr := os.Stat(sock)
		if !alive && errors.Is(statErr, fs.ErrNotExist) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	alive := processAlive(pid)
	_, statErr := os.Stat(sock)
	t.Fatalf("daemon did not fully shut down: alive=%v sock_stat_err=%v", alive, statErr)
}

// TestStatus_E2E_Stopped exercises the "no daemon reachable" branch of
// `pyry status`: point it at a fresh non-existent socket path and assert
// it exits non-zero with a clean error on stderr — no panic, no stack
// trace.
func TestStatus_E2E_Stopped(t *testing.T) {
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	r := RunBare(t, "status", "-pyry-socket="+bogusSock)
	if r.ExitCode == 0 {
		t.Fatalf("pyry status against bogus socket unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
			r.Stdout, r.Stderr)
	}
	if len(bytes.TrimSpace(r.Stderr)) == 0 {
		t.Errorf("expected non-empty stderr, got empty")
	}
	for _, bad := range [][]byte{
		[]byte("panic"),
		[]byte("goroutine "),
		[]byte("runtime/"),
	} {
		if bytes.Contains(r.Stderr, bad) {
			t.Errorf("stderr contains %q — expected clean error, not crash:\n%s", bad, r.Stderr)
		}
	}
}

// TestLogs_E2E spawns the daemon and runs `pyry logs`. The supervisor
// writes startup lines into the in-memory ring buffer, so any healthy
// daemon's log buffer is non-empty by the time Start(t) returns. Asserts
// only on exit code and non-empty output — specific log wording is
// internal and free to evolve.
func TestLogs_E2E(t *testing.T) {
	h := Start(t)

	r := h.Run(t, "logs")
	if r.ExitCode != 0 {
		t.Fatalf("pyry logs exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	if len(bytes.TrimSpace(r.Stdout)) == 0 {
		t.Errorf("expected non-empty logs stdout, got empty\nstderr:\n%s", r.Stderr)
	}
}

// TestVersion_E2E runs `pyry version`. The verb short-circuits in main
// before any flag parsing, so it doesn't need a daemon — drive it via
// RunBare. Asserts the output begins with the literal "pyry " prefix
// followed by a non-empty version token.
func TestVersion_E2E(t *testing.T) {
	r := RunBare(t, "version")
	if r.ExitCode != 0 {
		t.Fatalf("pyry version exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	out := bytes.TrimSpace(r.Stdout)
	prefix := []byte("pyry ")
	if !bytes.HasPrefix(out, prefix) {
		t.Fatalf("version stdout missing %q prefix:\n%s", string(prefix), out)
	}
	token := bytes.TrimSpace(bytes.TrimPrefix(out, prefix))
	if len(token) == 0 {
		t.Errorf("version token is empty:\n%s", out)
	}
}
