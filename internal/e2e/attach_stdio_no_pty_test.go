//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestE2E_AttachStdio_NoPTYInProcessTree asserts that the
// `pyry attach --stdio` child process holds no PTY device fds
// (/dev/ptmx, /dev/pts/*, /dev/ttys* on darwin) while attached to a
// supervised session via plain os.Pipe()s. The unit-level guarantee
// — that internal/control/attach_stdio_client.go imports no PTY
// machinery — is supplemented here at the binary boundary so a future
// refactor that wraps stdio in a pty inside cmd/pyry/runAttach's
// --stdio branch fails CI instead of shipping.
//
// Independent of TestE2E_AttachStdio_BytesRoundTrip: that test
// asserts bytes flow (positive); this one asserts no PTY allocation
// (negative). A regression that allocates a useless PTY but still
// passes bytes would fail only here.
func TestE2E_AttachStdio_NoPTYInProcessTree(t *testing.T) {
	// Same #167 gate as the byte-flow test; remove together when #167
	// lands.
	t.Skip("blocked on #167 — pyry attach --stdio rejected by parseClientFlags")

	c := startStdioAttach(t, "stdio-no-pty")

	pid := c.attachCmd.Process.Pid
	hits, err := openPTYDeviceTargets(pid)
	if err != nil {
		t.Skipf("e2e: fd inspection unavailable: %v", err)
	}
	if len(hits) > 0 {
		t.Fatalf("attach client (pid=%d) holds PTY device fd(s): %v\nattach stderr:\n%s",
			pid, hits, c.Stderr.String())
	}
}

// openPTYDeviceTargets returns the PTY device paths that pid has
// open. Empty slice + nil error means "no PTY devices held" (the
// success case). Non-nil error means the inspection mechanism itself
// is not available; the caller should t.Skip.
func openPTYDeviceTargets(pid int) ([]string, error) {
	switch runtime.GOOS {
	case "linux":
		return openPTYDeviceTargetsLinux(pid)
	case "darwin":
		return openPTYDeviceTargetsDarwin(pid)
	default:
		return nil, fmt.Errorf("unsupported GOOS %q", runtime.GOOS)
	}
}

func openPTYDeviceTargetsLinux(pid int) ([]string, error) {
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fdDir, err)
	}
	var hits []string
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue
		}
		if isPTYDevicePath(target) {
			hits = append(hits, target)
		}
	}
	return hits, nil
}

func openPTYDeviceTargetsDarwin(pid int) ([]string, error) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return nil, fmt.Errorf("lsof not found: %w", err)
	}
	out, err := exec.Command("lsof", "-p", fmt.Sprintf("%d", pid), "-Fn").Output()
	if err != nil {
		return nil, fmt.Errorf("lsof -p %d: %w", pid, err)
	}
	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		name := strings.TrimPrefix(line, "n")
		if isPTYDevicePath(name) {
			hits = append(hits, name)
		}
	}
	return hits, nil
}

// isPTYDevicePath returns true for paths the kernel exposes as PTY
// devices on linux or darwin. /dev/tty (controlling-terminal device)
// is included because a --stdio attach client with stdio wired to
// pipes should never have reason to open it.
func isPTYDevicePath(p string) bool {
	switch p {
	case "/dev/ptmx", "/dev/tty":
		return true
	}
	return strings.HasPrefix(p, "/dev/pts/") ||
		strings.HasPrefix(p, "/dev/ttys")
}
