//go:build (darwin || linux) && e2e_update

// update_e2e_test.go drives the full `pyry update` happy-path against an
// in-process fake release server: spawn a daemon from <home>/bin/pyry,
// fetch + verify + AtomicReplace + restart, then assert the binary inode
// changed, the daemon PID changed, and `pyry status` / `pyry sessions
// list` return successfully against the post-update daemon.
//
// Run with: go test -tags=e2e_update ./cmd/pyry/...
//
// PYRY_E2E_BIN, when set, short-circuits the per-test go build.
//
// fakeRelease and newFakeReleaseServer are reused from update_test.go
// (same package, no build tag).

package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/update"
)

const (
	e2eUpdReadyDeadline   = 5 * time.Second
	e2eUpdReadyPollGap    = 50 * time.Millisecond
	e2eUpdTermGrace       = 3 * time.Second
	e2eUpdKillGrace       = 1 * time.Second
	e2eUpdRunTimeout      = 10 * time.Second
	e2eUpdPhaseDeadline   = 3 * time.Second
	e2eUpdPhasePollGap    = 50 * time.Millisecond
)

type runResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// TestUpdate_HappyPath_E2E is the release-acceptance gate for the
// fetch → verify → atomic-replace → restart → smoke chain. It is the
// regression guard for v0.10.1 (supervisor stuck at Phase: starting after
// auto-restart with non-TTY stdin).
func TestUpdate_HappyPath_E2E(t *testing.T) {
	srcBin := buildPyryBinE2E(t)

	// Sun_path-safe temp HOME (mkdtemp under /tmp, not t.TempDir() —
	// macOS APFS's 104-byte sun_path limit forbids long t.TempDir()
	// names extended with /pyry.sock; see lessons.md).
	home, err := os.MkdirTemp("", "pyry-up-")
	if err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	// AtomicReplace requires the parent dir to exist and be writable.
	targetPath := filepath.Join(home, "bin", "pyry")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	copyFileE2E(t, srcBin, targetPath, 0o755)
	inodeBefore := inodeOfE2E(t, targetPath)

	// Spawn the pre-update daemon. Stdin defaults to nil → Go's exec
	// wires /dev/null, satisfying AC#2's "stdin closed" requirement.
	socket := filepath.Join(home, "pyry.sock")
	cmd1, stderr1, done1 := spawnDaemonE2E(t, targetPath, home, socket)
	cmd1Stopped := false
	t.Cleanup(func() {
		if cmd1Stopped {
			return
		}
		_ = stopDaemonE2E(cmd1, done1, socket)
	})
	if err := waitForSocketE2E(socket, done1, e2eUpdReadyDeadline); err != nil {
		t.Fatalf("daemon 1 not ready: %v\nstderr:\n%s", err, stderr1.String())
	}
	pidBefore := cmd1.Process.Pid

	// Build fake-release artefacts. The "new" binary is the same srcBin
	// re-tarred; the happy path needs a different inode and a working
	// binary that responds to status/sessions list, not a different
	// Version string.
	newBytes, err := os.ReadFile(srcBin)
	if err != nil {
		t.Fatalf("read srcBin: %v", err)
	}
	asset, tgz, sums := fakeRelease(t, "v999.0.0", runtime.GOOS, runtime.GOARCH, newBytes)
	srv := newFakeReleaseServer(t, "v999.0.0", asset, tgz, []byte(sums))

	// runRestart's role here: stand in for what supervisor-restart would
	// do — kill daemon 1, respawn from targetPath (now holding the new
	// bytes), block until the new socket is dialable. The argv passed in
	// (launchctl/systemctl ...) is ignored; we don't touch the
	// operator's real launchd/systemd.
	var (
		cmd2    *exec.Cmd
		stderr2 *bytes.Buffer
		done2   chan struct{}
	)
	runRestart := func(_ context.Context, _ []string) error {
		if err := stopDaemonE2E(cmd1, done1, socket); err != nil {
			return fmt.Errorf("stop daemon 1: %w", err)
		}
		cmd1Stopped = true
		cmd2, stderr2, done2 = spawnDaemonE2E(t, targetPath, home, socket)
		if err := waitForSocketE2E(socket, done2, e2eUpdReadyDeadline); err != nil {
			return fmt.Errorf("daemon 2 not ready: %w (stderr: %s)", err, stderr2.String())
		}
		return nil
	}

	// Drive the full update flow. probeRestart returns a probe whose
	// platform-discriminant flag produces non-nil argv from
	// DetectRestartCommand, so the wiring actually fires runRestart.
	var out bytes.Buffer
	err = doUpdate(t.Context(), updateOptions{
		currentVersion: "0.0.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return targetPath },
		replace:        update.AtomicReplace,
		out:            &out,
		probeRestart: func() update.RestartProbe {
			return update.RestartProbe{
				LaunchdPlistExists: runtime.GOOS == "darwin",
				SystemdUnitExists:  runtime.GOOS == "linux",
				UID:                strconv.Itoa(os.Getuid()),
			}
		},
		runRestart: runRestart,
	})
	if err != nil {
		t.Fatalf("doUpdate: %v\n--- output ---\n%s", err, out.String())
	}
	t.Cleanup(func() {
		if cmd2 == nil {
			return
		}
		_ = stopDaemonE2E(cmd2, done2, socket)
	})

	if cmd2 == nil {
		t.Fatalf("runRestart did not fire (cmd2 nil); doUpdate output:\n%s", out.String())
	}

	// AC#1 — atomic replace happened: inode changed.
	inodeAfter := inodeOfE2E(t, targetPath)
	if inodeAfter == inodeBefore {
		t.Errorf("inode unchanged after AtomicReplace: %d", inodeBefore)
	}

	// AC#1 — daemon was restarted: PID changed.
	pidAfter := cmd2.Process.Pid
	if pidAfter == pidBefore {
		t.Errorf("daemon PID unchanged after restart: %d", pidBefore)
	}

	// AC#1 / AC#2 — pyry status succeeds against the new binary, and
	// Phase advances past `starting` within a bounded window. Polling
	// rather than single-shot because supervisor.Run sets Phase to
	// Running asynchronously after onSpawn fires; the control socket
	// can be dialable a few ms before that. v0.10.1's regression would
	// never clear within e2eUpdPhaseDeadline.
	statusRes := waitForPhasePastStartingE2E(t, targetPath, home, socket)
	if statusRes.ExitCode != 0 {
		t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
			statusRes.ExitCode, statusRes.Stdout, statusRes.Stderr)
	}

	// AC#1 / AC#2 — pyry sessions list succeeds. The exact registry
	// content is not asserted (the daemon's bootstrap is implementation
	// detail); the assertion is "exit 0 within the harness timeout".
	listRes := runVerbE2E(t, targetPath, home, socket, "sessions", "list")
	if listRes.ExitCode != 0 {
		t.Fatalf("pyry sessions list exit=%d\nstdout:\n%s\nstderr:\n%s",
			listRes.ExitCode, listRes.Stdout, listRes.Stderr)
	}
}

// buildPyryBinE2E builds pyry into a per-test temp dir. PYRY_E2E_BIN, when
// set, short-circuits to a pre-built binary on disk — same shape as
// internal/e2e/harness.go's ensurePyryBuilt.
func buildPyryBinE2E(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("PYRY_E2E_BIN"); env != "" {
		return env
	}
	dir, err := os.MkdirTemp("", "pyry-e2eupd-")
	if err != nil {
		t.Fatalf("mkdir pyry build dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	binPath := filepath.Join(dir, "pyry")
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/pyrycode/pyrycode/cmd/pyry")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build pyry: %v\n%s", err, out)
	}
	return binPath
}

func copyFileE2E(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// inodeOfE2E returns the inode number for path. Linux + macOS only, matching
// the build tag — Stat_t.Ino is unix-shape on both.
func inodeOfE2E(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: Sys() not a *syscall.Stat_t", path)
	}
	return uint64(st.Ino)
}

// childEnvE2E returns the parent env with HOME replaced and PYRY_NAME
// stripped so the operator's shell alias can't leak into the test daemon.
// Mirrors internal/e2e/harness.go:childEnv.
func childEnvE2E(home string) []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+1)
	for _, kv := range src {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "PYRY_NAME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}

// spawnDaemonE2E forks pyry from bin with the standard test flag set
// (sleep-as-claude, idle eviction off, -pyry-name=test). Stdin is left nil
// — Go's exec wires /dev/null. Caller is responsible for stop/cleanup.
func spawnDaemonE2E(t *testing.T, bin, home, socket string) (*exec.Cmd, *bytes.Buffer, chan struct{}) {
	t.Helper()
	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=/bin/sleep",
		"-pyry-idle-timeout=0",
		"--", "99999",
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = childEnvE2E(home)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn pyry: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()
	return cmd, stderr, doneCh
}

// waitForSocketE2E polls until the daemon's control socket is dialable,
// short-circuits on doneCh (daemon exited before ready). Mirrors
// Harness.waitForReady.
func waitForSocketE2E(socket string, doneCh <-chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
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
			return fmt.Errorf("daemon exited before ready")
		case <-time.After(e2eUpdReadyPollGap):
		}
	}
	return fmt.Errorf("daemon not ready within %s", timeout)
}

// stopDaemonE2E sends SIGTERM, escalates to SIGKILL after a grace, then
// removes the socket file defensively. Idempotent: re-entry on an already
// exited child is a no-op.
func stopDaemonE2E(cmd *exec.Cmd, doneCh chan struct{}, socket string) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-doneCh:
		_ = os.Remove(socket)
		return nil
	default:
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-doneCh:
	case <-time.After(e2eUpdTermGrace):
		_ = cmd.Process.Signal(syscall.SIGKILL)
		select {
		case <-doneCh:
		case <-time.After(e2eUpdKillGrace):
			return fmt.Errorf("pid %d did not exit after SIGKILL+%s", cmd.Process.Pid, e2eUpdKillGrace)
		}
	}
	_ = os.Remove(socket)
	return nil
}

// runVerbE2E invokes pyry against socket with the verb, auto-injecting
// -pyry-socket=. Mirrors internal/e2e.runVerb's shape; bounded by
// e2eUpdRunTimeout via context.WithTimeout(context.Background(), ...) so
// timeouts are predictable across t.Run boundaries.
func runVerbE2E(t *testing.T, bin, home, socket, verb string, args ...string) runResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eUpdRunTimeout)
	defer cancel()

	full := append([]string{verb, "-pyry-socket=" + socket}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Env = childEnvE2E(home)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("pyry %s timed out after %s\nstdout:\n%s\nstderr:\n%s",
			verb, e2eUpdRunTimeout, stdout.String(), stderr.String())
	}

	var exitCode int
	switch e := err.(type) {
	case nil:
		exitCode = 0
	case *exec.ExitError:
		exitCode = e.ExitCode()
	default:
		t.Fatalf("pyry %s exec failed: %v", verb, err)
	}

	return runResult{ExitCode: exitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

// waitForPhasePastStartingE2E polls `pyry status` until the reported phase
// is anything other than `starting`, returning the last successful result.
// Fails the test if the phase remains `starting` past e2eUpdPhaseDeadline
// (the v0.10.1 regression signature: socket dialable but supervisor never
// advances out of the starting phase).
func waitForPhasePastStartingE2E(t *testing.T, bin, home, socket string) runResult {
	t.Helper()
	deadline := time.Now().Add(e2eUpdPhaseDeadline)
	var last runResult
	for {
		last = runVerbE2E(t, bin, home, socket, "status")
		if last.ExitCode != 0 {
			return last
		}
		phase := parsePhaseE2E(last.Stdout)
		if phase == "" {
			t.Fatalf("pyry status missing Phase: line:\n%s", last.Stdout)
		}
		if phase != "starting" {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("post-update daemon stuck at Phase: starting after %s (v0.10.1 regression):\n%s",
				e2eUpdPhaseDeadline, last.Stdout)
		}
		time.Sleep(e2eUpdPhasePollGap)
	}
}

// parsePhaseE2E pulls the phase value from a `pyry status` stdout block.
// Returns the empty string when no `Phase:` line is present.
func parsePhaseE2E(stdout []byte) string {
	for _, line := range strings.Split(string(stdout), "\n") {
		if !strings.HasPrefix(line, "Phase:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return ""
		}
		return fields[1]
	}
	return ""
}
