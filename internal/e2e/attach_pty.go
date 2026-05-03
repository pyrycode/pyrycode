//go:build e2e || e2e_install

package e2e

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// AttachHarness owns one running pyry daemon configured for interactive
// attach (bridge mode, the e2e test binary as claude in echo mode), the
// master side of a creack/pty pair, and the running `pyry attach`
// subprocess whose stdio is bound to the slave.
//
// Returned by StartAttach; cleanup is registered via t.Cleanup. Tests
// interact with Master to write input bytes and read output bytes back.
type AttachHarness struct {
	// Master is the PTY master. Tests write input bytes here and read
	// output bytes back. Closed by cleanup.
	Master *os.File

	// SocketPath is the daemon's control socket. Exposed so follow-up
	// tickets can drive additional verbs against the same daemon
	// (e.g. a second `pyry attach` to assert ErrBridgeBusy).
	SocketPath string

	// HomeDir is the daemon's $HOME (a fresh t.TempDir).
	HomeDir string

	daemonCmd  *exec.Cmd
	daemonDone chan struct{}
	daemonOut  *bytes.Buffer
	daemonErr  *bytes.Buffer

	attachCmd  *exec.Cmd
	attachDone chan struct{}

	slave    *os.File
	origTerm *term.State

	cleanupOnce sync.Once
}

// StartAttach spawns a pyry daemon in bridge mode whose supervised claude
// is the e2e test binary running TestHelperProcess in echo mode, opens a
// creack/pty pair, and spawns `pyry attach` with the slave on stdio. The
// returned harness exposes Master for the test to write/read.
//
// sessionID selects the session to attach to. Empty means "the bootstrap
// session", which is the only session this harness creates.
//
// Skips the test (t.Skip) if pty.Open fails — some hosts (sandboxed CI,
// minimal containers) lack a usable /dev/ptmx. Fails the test on any
// other startup error.
func StartAttach(t *testing.T, sessionID string) *AttachHarness {
	t.Helper()

	// Probe PTY availability up front. A clean t.Skip is faster than
	// spawning pyry, racing readiness, then tearing down. AC#5.
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("e2e: pty.Open unavailable: %v", err)
	}

	// Snapshot the parent test process's stdin terminal state defensively.
	// No code path in this harness should modify it, but AC#4 calls for a
	// restore-on-cleanup guarantee.
	var origTerm *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		st, err := term.GetState(int(os.Stdin.Fd()))
		if err == nil {
			origTerm = st
		}
	}

	home := t.TempDir()
	socket, daemonCmd, daemonOut, daemonErr, daemonDone := spawnAttachableDaemon(t, home)

	a := &AttachHarness{
		Master:     master,
		SocketPath: socket,
		HomeDir:    home,
		daemonCmd:  daemonCmd,
		daemonDone: daemonDone,
		daemonOut:  daemonOut,
		daemonErr:  daemonErr,
		slave:      slave,
		origTerm:   origTerm,
	}
	t.Cleanup(func() { a.teardown(t) })

	if err := waitDaemonReady(socket, daemonDone, daemonErr); err != nil {
		t.Fatalf("e2e: %v", err)
	}

	bin := ensurePyryBuilt(t)
	attachCmd := exec.Command(bin, "attach", "-pyry-socket="+socket)
	attachCmd.Stdin = slave
	attachCmd.Stdout = slave
	attachCmd.Stderr = slave
	// Make the slave the controlling terminal of the attach client so
	// IsTerminal(stdin) returns true and term.MakeRaw kills ECHO/ICANON.
	attachCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	attachCmd.Env = childEnv(home)

	if err := attachCmd.Start(); err != nil {
		t.Fatalf("e2e: pyry attach start: %v", err)
	}
	attachDone := make(chan struct{})
	go func() {
		_ = attachCmd.Wait()
		close(attachDone)
	}()
	a.attachCmd = attachCmd
	a.attachDone = attachDone

	// If the attach client dies in handshake, surface that early instead
	// of letting the test wait out its read deadline.
	select {
	case <-attachDone:
		exit := -1
		if attachCmd.ProcessState != nil {
			exit = attachCmd.ProcessState.ExitCode()
		}
		t.Fatalf("e2e: pyry attach exited before round-trip (exit=%d)\ndaemon stderr:\n%s",
			exit, daemonErr.String())
	case <-time.After(500 * time.Millisecond):
	}

	return a
}

// spawnAttachableDaemon mirrors harness.spawn but with a custom claude
// (the e2e test binary running TestHelperProcess), helper-env injection
// (GO_TEST_HELPER_PROCESS=1, GO_TEST_HELPER_MODE=echo), and no sleep
// sentinel. The daemon's stdin is left at its default (/dev/null) so
// IsTerminal returns false and pyry runs in bridge mode.
func spawnAttachableDaemon(t *testing.T, home string) (string, *exec.Cmd, *bytes.Buffer, *bytes.Buffer, chan struct{}) {
	t.Helper()
	bin := ensurePyryBuilt(t)
	socket := filepath.Join(home, "pyry.sock")

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=" + os.Args[0],
		"-pyry-idle-timeout=0",
		"--",
		"-test.run=TestHelperProcess",
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// supervisor.runOnce does cmd.Env = append(os.Environ(), helperEnv...),
	// so env set on the daemon flows through to the supervised helper.
	cmd.Env = append(childEnv(home),
		"GO_TEST_HELPER_PROCESS=1",
		"GO_TEST_HELPER_MODE=echo",
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("e2e: pyry attachable daemon start: %v", err)
	}

	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	return socket, cmd, stdout, stderr, doneCh
}

// waitDaemonReady polls the socket like Harness.waitForReady. Short-
// circuits if the daemon exits before ready (e.g. flag parse error).
func waitDaemonReady(socket string, doneCh chan struct{}, errBuf *bytes.Buffer) error {
	deadline := time.Now().Add(readyDeadline)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			c, err := net.Dial("unix", socket)
			if err == nil {
				_ = c.Close()
				return nil
			}
		}
		select {
		case <-doneCh:
			return fmt.Errorf("pyry exited before ready: %s", errBuf.String())
		case <-time.After(readyPollGap):
		}
	}
	return fmt.Errorf("pyry not ready within %s", readyDeadline)
}

// teardown closes master and slave (delivering SIGHUP/EOF to the attach
// client), then SIGTERM-grace-SIGKILLs both the attach client and the
// daemon, then removes the socket and restores the parent's terminal
// state. Idempotent via sync.Once.
func (a *AttachHarness) teardown(t *testing.T) {
	a.cleanupOnce.Do(func() {
		if a.Master != nil {
			_ = a.Master.Close()
		}
		if a.slave != nil {
			_ = a.slave.Close()
		}
		if a.attachCmd != nil && a.attachCmd.Process != nil {
			killSpawned(t, a.attachCmd, a.attachDone)
		}
		if a.daemonCmd != nil && a.daemonCmd.Process != nil {
			killSpawned(t, a.daemonCmd, a.daemonDone)
		}
		_ = os.Remove(a.SocketPath)
		if a.origTerm != nil {
			_ = term.Restore(int(os.Stdin.Fd()), a.origTerm)
		}
	})
}
