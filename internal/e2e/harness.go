//go:build e2e || e2e_install

// Package e2e provides a test harness that spawns pyry as a real daemon in
// an isolated temp HOME, blocks until the control socket is dialable, and
// tears it down reliably on test cleanup.
//
// The package is build-tag isolated; default `go test ./...` does not
// compile it. Invoke with:
//
//	go test -tags=e2e ./internal/e2e/...
//
// Set PYRY_E2E_BIN to a pre-built pyry binary to skip the per-test-process
// `go build`.
//
// Typical usage — spawn a daemon and drive a CLI verb against it:
//
//	func TestStatusReportsRunning(t *testing.T) {
//	    h := e2e.Start(t)
//
//	    r := h.Run(t, "status")
//	    if r.ExitCode != 0 {
//	        t.Fatalf("pyry status exit=%d stderr=%s", r.ExitCode, r.Stderr)
//	    }
//	    if !bytes.Contains(r.Stdout, []byte("Phase:")) {
//	        t.Fatalf("status output missing Phase: line: %s", r.Stdout)
//	    }
//	}
//
// h.Run auto-injects -pyry-socket=<h.SocketPath> after the verb so callers
// don't thread it through. Exit code, stdout, and stderr are all available
// on the returned RunResult regardless of success.
//
// To prove an on-disk invariant survives daemon restart, pre-populate HOME
// before the first Start, Stop the first daemon, and StartIn a second
// daemon against the same HOME:
//
//	home := t.TempDir()
//	if err := os.MkdirAll(filepath.Join(home, ".pyry", "test"), 0o700); err != nil {
//	    t.Fatal(err)
//	}
//	if err := os.WriteFile(filepath.Join(home, ".pyry", "test", "sessions.json"),
//	    []byte(registryJSON), 0o600); err != nil {
//	    t.Fatal(err)
//	}
//
//	h1 := e2e.StartIn(t, home)
//	h1.Stop(t)
//
//	h2 := e2e.StartIn(t, home)
//	// h2.HomeDir == home; assert on the registry file directly.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	readyDeadline = 5 * time.Second
	readyPollGap  = 50 * time.Millisecond
	termGrace     = 3 * time.Second
	killGrace     = 1 * time.Second
	runTimeout    = 10 * time.Second
)

// Harness owns one running pyry daemon. Returned by Start; cleanup is
// registered via t.Cleanup at construction.
type Harness struct {
	// SocketPath is the Unix socket the daemon listens on. Tests can dial
	// it directly (e.g. via internal/control client helpers).
	SocketPath string

	// HomeDir is the temp dir the daemon sees as $HOME. Registry, claude
	// sessions dir, and any other ~-relative paths live underneath.
	HomeDir string

	// PID of the running pyry process. Captured at spawn so failure-injection
	// tests can verify it is gone after cleanup runs.
	PID int

	// Stdout / Stderr accumulate the child's output. Safe to read after the
	// process has exited (cleanup waits for the wait goroutine).
	Stdout *bytes.Buffer
	Stderr *bytes.Buffer

	cmd         *exec.Cmd
	doneCh      chan struct{}
	cleanupOnce sync.Once
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// ensurePyryBuilt builds pyry once per test process. PYRY_E2E_BIN, when set,
// short-circuits to a pre-built binary on disk.
func ensurePyryBuilt(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		if env := os.Getenv("PYRY_E2E_BIN"); env != "" {
			binPath = env
			return
		}
		dir, err := os.MkdirTemp("", "pyry-e2e-*")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "pyry")
		cmd := exec.Command("go", "build", "-o", binPath, "github.com/pyrycode/pyrycode/cmd/pyry")
		out, err := cmd.CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("go build pyry: %w\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatalf("e2e: %v", binErr)
	}
	return binPath
}

// Start builds pyry once per test process, spawns it in an isolated temp
// HOME with a custom socket path, blocks until the control socket is
// dialable, and registers teardown via t.Cleanup. Fails the test on any
// error before returning a usable Harness.
//
// The supervised "claude" is /bin/sleep infinity — exists on Linux and
// macOS, survives until SIGTERM, and the readiness gate doesn't depend on
// the child being a real claude. Idle eviction is disabled
// (-pyry-idle-timeout=0) so the smoke path isn't racing the timer.
func Start(t *testing.T) *Harness {
	t.Helper()
	return StartIn(t, t.TempDir())
}

// StartIn behaves like Start but uses the caller-supplied home directory
// instead of allocating a fresh t.TempDir(). The directory must already
// exist; pre-populate it (e.g. <home>/.pyry/test/sessions.json) before
// calling StartIn to drive a daemon against a chosen on-disk state. The
// caller still owns the directory's lifecycle — StartIn does not register
// it with t.Cleanup. Use Start(t) for the common case.
func StartIn(t *testing.T, home string) *Harness {
	t.Helper()
	bin := ensurePyryBuilt(t)
	socket := filepath.Join(home, "pyry.sock")

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := exec.Command(bin,
		"-pyry-socket="+socket,
		"-pyry-name=test",
		"-pyry-claude=/bin/sleep",
		"-pyry-idle-timeout=0",
		"--", "infinity",
	)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = childEnv(home)

	if err := cmd.Start(); err != nil {
		t.Fatalf("e2e: pyry start: %v", err)
	}

	h := &Harness{
		SocketPath: socket,
		HomeDir:    home,
		PID:        cmd.Process.Pid,
		Stdout:     stdout,
		Stderr:     stderr,
		cmd:        cmd,
		doneCh:     make(chan struct{}),
	}

	go func() {
		_ = cmd.Wait()
		close(h.doneCh)
	}()

	t.Cleanup(func() { h.teardown(t) })

	if err := h.waitForReady(); err != nil {
		t.Fatalf("e2e: %v", err)
	}
	return h
}

// childEnv returns the parent env with HOME replaced and PYRY_NAME stripped
// so the operator's shell alias can't leak into a test daemon.
func childEnv(home string) []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+1)
	for _, kv := range src {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "PYRY_NAME=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HOME="+home)
	return out
}

// waitForReady polls the socket with a 5-second deadline. Once Dial
// succeeds, the control server is in Serve and the daemon is responsive.
// Short-circuits if the daemon exits before ready (e.g. flag parse error).
func (h *Harness) waitForReady() error {
	deadline := time.Now().Add(readyDeadline)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(h.SocketPath); err == nil {
			c, err := net.Dial("unix", h.SocketPath)
			if err == nil {
				_ = c.Close()
				return nil
			}
		}
		select {
		case <-h.doneCh:
			return fmt.Errorf("pyry exited before ready: %s", h.Stderr.String())
		case <-time.After(readyPollGap):
		}
	}
	return fmt.Errorf("pyry not ready within %s", readyDeadline)
}

// RunResult is the outcome of a CLI invocation against the harness's daemon.
// All three fields are populated regardless of exit code; an erroring command
// still has its captured Stdout/Stderr available for assertion.
type RunResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Run invokes the cached pyry binary with `<verb> -pyry-socket=<h.SocketPath> <args...>`,
// waits for it to exit, and returns its captured streams. The harness's
// socket path is auto-injected so callers don't thread it through.
//
// The verb is positional because pyry dispatches subcommands on os.Args[1];
// flags must come after the verb.
//
// Fails the test (t.Fatalf) on exec failure (binary not found, fork error)
// or if the command runs longer than runTimeout. A non-zero exit code is
// not a test failure — the caller asserts on RunResult.ExitCode.
func (h *Harness) Run(t *testing.T, verb string, args ...string) RunResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	full := append([]string{verb, "-pyry-socket=" + h.SocketPath}, args...)
	cmd := exec.CommandContext(ctx, binPath, full...)
	cmd.Env = childEnv(h.HomeDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("e2e: pyry %s timed out after %s\nstdout:\n%s\nstderr:\n%s",
			verb, runTimeout, stdout.String(), stderr.String())
	}

	var exitCode int
	switch e := err.(type) {
	case nil:
		exitCode = 0
	case *exec.ExitError:
		exitCode = e.ExitCode()
	default:
		t.Fatalf("e2e: pyry %s exec failed: %v", verb, err)
	}

	return RunResult{ExitCode: exitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

// RunBare invokes the cached pyry binary with args verbatim — no daemon
// spawn, no auto-injected -pyry-socket. For verbs that don't touch the
// control socket (e.g. `version`) or for negative tests where the caller
// wants to drive a verb against a deliberately-bogus socket path. Reuses
// the same binary cache (ensurePyryBuilt) and the same exit-code /
// timeout / capture machinery as Harness.Run.
//
// Unlike Harness.Run, RunBare uses the test process env unchanged — no
// HOME isolation. The bare verbs we drive (version, status against a
// bogus socket) don't read $HOME, and adding HOME isolation we don't
// use is dead weight.
func RunBare(t *testing.T, args ...string) RunResult {
	t.Helper()
	bin := ensurePyryBuilt(t)
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("e2e: pyry %v timed out after %s\nstdout:\n%s\nstderr:\n%s",
			args, runTimeout, stdout.String(), stderr.String())
	}

	var exitCode int
	switch e := err.(type) {
	case nil:
		exitCode = 0
	case *exec.ExitError:
		exitCode = e.ExitCode()
	default:
		t.Fatalf("e2e: pyry %v exec failed: %v", args, err)
	}

	return RunResult{ExitCode: exitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

// Stop gracefully terminates the daemon (SIGTERM, grace, escalate to
// SIGKILL matching t.Cleanup teardown), waits for the process to exit,
// and removes the socket file. HomeDir is left intact on disk so the
// same directory can be passed to a subsequent StartIn for a
// restart-shaped test.
//
// Idempotent with the t.Cleanup teardown registered by Start/StartIn:
// whichever path fires first wins; the other is a no-op (sync.Once).
func (h *Harness) Stop(t *testing.T) {
	t.Helper()
	h.teardown(t)
}

// teardown sends SIGTERM, escalates to SIGKILL after a short grace, then
// removes the socket file. The temp HomeDir is cleaned up by t.TempDir.
// Wrapped in sync.Once so a manual Stop() and t.Cleanup don't double-fire.
func (h *Harness) teardown(t *testing.T) {
	h.cleanupOnce.Do(func() {
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-h.doneCh:
			case <-time.After(termGrace):
				_ = h.cmd.Process.Signal(syscall.SIGKILL)
				select {
				case <-h.doneCh:
				case <-time.After(killGrace):
					t.Logf("e2e: pyry pid=%d did not exit after SIGKILL+%s", h.PID, killGrace)
				}
			}
		}
		// Defensive: SIGTERM lets pyry remove the socket itself; SIGKILL
		// doesn't, so do it here best-effort.
		_ = os.Remove(h.SocketPath)
	})
}
