//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestHarness_Smoke verifies the spawn + ready + clean-shutdown path.
func TestHarness_Smoke(t *testing.T) {
	h := Start(t)

	if h.SocketPath == "" {
		t.Fatal("empty SocketPath")
	}
	if h.PID <= 0 {
		t.Fatalf("invalid PID: %d", h.PID)
	}
	if h.HomeDir == "" {
		t.Fatal("empty HomeDir")
	}

	c, err := net.Dial("unix", h.SocketPath)
	if err != nil {
		t.Fatalf("dial after Start: %v", err)
	}
	_ = c.Close()
}

// innerFatalEnv toggles TestInnerFatalChild into "run as the failing
// child" mode and names the file the child writes its observed
// (pid, socket) state to before calling t.Fatal. Used by
// TestHarness_NoLeakOnFatal's subprocess re-exec.
const innerFatalEnv = "PYRY_E2E_INNER_FATAL_OUT"

// TestInnerFatalChild is run in a subprocess by TestHarness_NoLeakOnFatal.
// In normal test runs the env var is unset and the test is a no-op; in
// the re-exec it spawns pyry, writes the (pid, socket) state out, then
// fails — exercising the harness's cleanup-on-fatal path in isolation.
func TestInnerFatalChild(t *testing.T) {
	out := os.Getenv(innerFatalEnv)
	if out == "" {
		t.Skip("inner-fatal child only runs under TestHarness_NoLeakOnFatal")
	}
	h := Start(t)
	state := fmt.Sprintf("%d\n%s\n", h.PID, h.SocketPath)
	if err := os.WriteFile(out, []byte(state), 0o600); err != nil {
		t.Fatalf("write child state: %v", err)
	}
	t.Fatal("inject failure")
}

// TestHarness_NoLeakOnFatal asserts that a t.Fatal mid-test does not leak
// a pyry process or socket file. We can't t.Fatal in a same-process
// subtest — Go's testing framework propagates the failure to the parent.
// Instead, re-exec the test binary with PYRY_E2E_INNER_FATAL_OUT set so
// TestInnerFatalChild runs the failing path in a fresh process; we then
// observe externally that the daemon is gone.
func TestHarness_NoLeakOnFatal(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "child-state")

	cmd := exec.Command(os.Args[0],
		"-test.run=^TestInnerFatalChild$",
		"-test.count=1",
	)
	cmd.Env = append(os.Environ(), innerFatalEnv+"="+outFile)
	combined, _ := cmd.CombinedOutput()

	state, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("child did not write state file: %v\nchild output:\n%s", err, combined)
	}
	parts := strings.SplitN(strings.TrimSpace(string(state)), "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed child state %q", state)
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid <= 0 {
		t.Fatalf("invalid pid %q: %v", parts[0], err)
	}
	sock := parts[1]

	if processAlive(pid) {
		t.Errorf("pyry pid=%d still alive after child cleanup\nchild output:\n%s", pid, combined)
	}
	// The socket's parent dir is the child's t.TempDir — its removal
	// runs after the harness cleanup (LIFO), so observing fs.ErrNotExist
	// here covers both "harness removed the socket file" and "harness
	// failed but t.TempDir nuked the parent dir." Either way the
	// AC's "no socket file leaks" property holds.
	if _, err := os.Stat(sock); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("socket %s not removed: %v", sock, err)
	}
}

// TestStatus_E2E spawns the daemon and exercises the pyry status verb
// end-to-end against its control socket. Asserts on a stable substring
// rather than exact whitespace so a future status-formatting change
// doesn't break the test.
func TestStatus_E2E(t *testing.T) {
	h := Start(t)

	r := h.Run(t, "status")
	if r.ExitCode != 0 {
		t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	if !bytes.Contains(r.Stdout, []byte("Phase:")) {
		t.Errorf("status stdout missing %q line:\n%s", "Phase:", r.Stdout)
	}
}

// processAlive reports whether the given pid is currently running. Uses
// the POSIX zero-signal probe — no side effect, returns ESRCH if gone.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
