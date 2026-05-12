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
	"net/http"
	"net/http/httptest"
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
	e2eUpdReadyDeadline = 5 * time.Second
	e2eUpdReadyPollGap  = 50 * time.Millisecond
	e2eUpdTermGrace     = 3 * time.Second
	e2eUpdKillGrace     = 1 * time.Second
	e2eUpdRunTimeout    = 10 * time.Second
	e2eUpdPhaseDeadline = 3 * time.Second
	e2eUpdPhasePollGap  = 50 * time.Millisecond
)

type runResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// TestUpdate_HappyPath_E2E is the release-acceptance gate for the
// fetch → verify → atomic-replace → restart → smoke chain. It is also
// the structural smoke check for the supervisor-startup-with-stdin-closed
// path (the v0.10.1-class shape, though the specific evicted-bootstrap
// reproducer is exercised by internal/e2e/bootstrap_warm_start_test.go).
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
	cmd1, stdout1, stderr1, done1 := spawnDaemonE2E(t, targetPath, home, socket)
	cmd1Stopped := false
	t.Cleanup(func() {
		if cmd1Stopped {
			return
		}
		_ = stopDaemonE2E(cmd1, done1, socket)
	})
	if err := waitForSocketE2E(socket, done1, e2eUpdReadyDeadline); err != nil {
		t.Fatalf("daemon 1 not ready: %v\nstdout:\n%s\nstderr:\n%s", err, stdout1.String(), stderr1.String())
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
		stdout2 *bytes.Buffer
		stderr2 *bytes.Buffer
		done2   chan struct{}
	)
	runRestart := func(_ context.Context, _ []string) error {
		if err := stopDaemonE2E(cmd1, done1, socket); err != nil {
			return fmt.Errorf("stop daemon 1: %w", err)
		}
		cmd1Stopped = true
		cmd2, stdout2, stderr2, done2 = spawnDaemonE2E(t, targetPath, home, socket)
		if err := waitForSocketE2E(socket, done2, e2eUpdReadyDeadline); err != nil {
			return fmt.Errorf("daemon 2 not ready: %w (stdout: %s, stderr: %s)", err, stdout2.String(), stderr2.String())
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
	// can be dialable a few ms before that. A v0.10.1-shaped startup
	// hang would never clear within e2eUpdPhaseDeadline.
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
func spawnDaemonE2E(t *testing.T, bin, home, socket string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer, chan struct{}) {
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
	return cmd, stdout, stderr, doneCh
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
// (a v0.10.1-shaped failure signature: socket dialable but supervisor
// never advances out of the starting phase).
func waitForPhasePastStartingE2E(t *testing.T, bin, home, socket string) runResult {
	t.Helper()
	deadline := time.Now().Add(e2eUpdPhaseDeadline)
	var last runResult
	for {
		last = runVerbE2E(t, bin, home, socket, "status")
		if last.ExitCode != 0 {
			return last
		}
		phase, ok := parsePhaseE2E(last.Stdout)
		if !ok {
			t.Fatalf("pyry status missing Phase: line:\n%s", last.Stdout)
		}
		if phase != "starting" {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("post-update daemon stuck at Phase: starting after %s (v0.10.1-shaped startup hang):\n%s",
				e2eUpdPhaseDeadline, last.Stdout)
		}
		time.Sleep(e2eUpdPhasePollGap)
	}
}

// parsePhaseE2E pulls the phase value from a `pyry status` stdout block.
// The bool is false when no `Phase:` line is present; an empty value on a
// present `Phase:` line returns ("", true).
func parsePhaseE2E(stdout []byte) (string, bool) {
	for _, line := range strings.Split(string(stdout), "\n") {
		if !strings.HasPrefix(line, "Phase:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return "", true
		}
		return fields[1], true
	}
	return "", false
}

// preUpdateState bundles the pre-update daemon's identity. Each failure-path
// test asserts at least two of these fields didn't change.
type preUpdateState struct {
	targetPath  string
	home        string
	socket      string
	inodeBefore uint64
	pidBefore   int
	cmd1        *exec.Cmd
	done1       chan struct{}
	stdout1     *bytes.Buffer
	stderr1     *bytes.Buffer
}

// installPreUpdateDaemonE2E installs the freshly-built pyry → <home>/bin/pyry,
// spawns it, waits for the socket, and captures the pre-update inode + PID.
// Caller is responsible for stopping cmd1 — either directly via
// stopDaemonE2E (broken-binary test, where runRestart kills it mid-test) or
// via t.Cleanup (fetch/verify failures, where daemon 1 outlives doUpdate).
func installPreUpdateDaemonE2E(t *testing.T) *preUpdateState {
	t.Helper()
	srcBin := buildPyryBinE2E(t)

	home, err := os.MkdirTemp("", "pyry-up-")
	if err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	targetPath := filepath.Join(home, "bin", "pyry")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	copyFileE2E(t, srcBin, targetPath, 0o755)
	inodeBefore := inodeOfE2E(t, targetPath)

	socket := filepath.Join(home, "pyry.sock")
	cmd1, stdout1, stderr1, done1 := spawnDaemonE2E(t, targetPath, home, socket)
	if err := waitForSocketE2E(socket, done1, e2eUpdReadyDeadline); err != nil {
		t.Fatalf("daemon 1 not ready: %v\nstdout:\n%s\nstderr:\n%s", err, stdout1.String(), stderr1.String())
	}

	return &preUpdateState{
		targetPath:  targetPath,
		home:        home,
		socket:      socket,
		inodeBefore: inodeBefore,
		pidBefore:   cmd1.Process.Pid,
		cmd1:        cmd1,
		done1:       done1,
		stdout1:     stdout1,
		stderr1:     stderr1,
	}
}

// buildBrokenPyryBinE2E builds the deliberately-broken pyry stand-in into a
// per-test temp dir. PYRY_E2E_BROKEN_BIN, when set, short-circuits to a
// pre-built binary — same shape and CI-prebuild contract as buildPyryBinE2E.
func buildBrokenPyryBinE2E(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("PYRY_E2E_BROKEN_BIN"); env != "" {
		return env
	}
	dir, err := os.MkdirTemp("", "pyry-e2eupd-broken-")
	if err != nil {
		t.Fatalf("mkdir brokenpyry build dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	binPath := filepath.Join(dir, "brokenpyry")
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/pyrycode/pyrycode/internal/brokenpyry")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build brokenpyry: %v\n%s", err, out)
	}
	return binPath
}

// newFetchFailReleaseServer hosts the latest-release endpoint successfully
// but returns HTTP 500 on the asset download URL. Parallel to
// newFakeReleaseServer; growing newFakeReleaseServer a failure-injection
// knob for a single caller is not worth the API churn against four
// happy-path callers in update_test.go.
func newFetchFailReleaseServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	asset, err := update.AssetName(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/pyrycode/pyrycode/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q}`, version)
	})
	mux.HandleFunc("/releases/download/"+version+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "simulated upstream failure", http.StatusInternalServerError)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func assertBinaryUnchangedE2E(t *testing.T, s *preUpdateState) {
	t.Helper()
	if inodeAfter := inodeOfE2E(t, s.targetPath); inodeAfter != s.inodeBefore {
		t.Errorf("on-disk binary inode changed: before=%d after=%d (AtomicReplace must not run on this path)",
			s.inodeBefore, inodeAfter)
	}
}

// assertDaemonAliveE2E checks that the pre-update daemon's PID is unchanged,
// the process is still findable, and `pyry status` against its socket
// returns exit 0. The PID and signal checks are documentation /
// localization aids — the structural assertion is the status round-trip.
func assertDaemonAliveE2E(t *testing.T, s *preUpdateState) {
	t.Helper()
	if s.cmd1.Process.Pid != s.pidBefore {
		t.Errorf("daemon 1 PID mutated: before=%d now=%d", s.pidBefore, s.cmd1.Process.Pid)
	}
	proc, err := os.FindProcess(s.pidBefore)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", s.pidBefore, err)
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("daemon 1 (pid %d) no longer alive: %v", s.pidBefore, err)
	}
	res := runVerbE2E(t, s.targetPath, s.home, s.socket, "status")
	if res.ExitCode != 0 {
		t.Errorf("pyry status against original daemon exit=%d\nstdout:\n%s\nstderr:\n%s",
			res.ExitCode, res.Stdout, res.Stderr)
	}
}

// assertNoStragglersE2E asserts that the directory holding targetPath
// contains only the pyry binary itself — no `.pyry.*.tmp` files left
// behind by a partial AtomicReplace. Trivially true today on the
// fetch-failure path (AtomicReplace never runs); the assertion exists to
// catch regressions where a future change leaks files before the rename.
func assertNoStragglersE2E(t *testing.T, s *preUpdateState) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(s.targetPath))
	if err != nil {
		t.Fatalf("read bin dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "pyry" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only 'pyry' in %s, got: %v", filepath.Dir(s.targetPath), names)
	}
}

func assertNoSuccessLineE2E(t *testing.T, out *bytes.Buffer, version string) {
	t.Helper()
	line := "==> Updated to " + version + "."
	if strings.Contains(out.String(), line) {
		t.Errorf("success line %q must NOT print on failure path; output:\n%s", line, out.String())
	}
}

// TestUpdate_FetchFailure_E2E exercises the release-asset download failure
// path: doUpdate must return an error before AtomicReplace runs, the
// on-disk binary must be untouched, the pre-update daemon must keep
// answering, and no temp files must be left behind.
func TestUpdate_FetchFailure_E2E(t *testing.T) {
	s := installPreUpdateDaemonE2E(t)
	t.Cleanup(func() { _ = stopDaemonE2E(s.cmd1, s.done1, s.socket) })

	srv := newFetchFailReleaseServer(t, "v999.0.0")

	var out bytes.Buffer
	err := doUpdate(t.Context(), updateOptions{
		currentVersion: "0.0.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return s.targetPath },
		replace:        update.AtomicReplace,
		out:            &out,
		probeRestart:   func() update.RestartProbe { return update.RestartProbe{} },
		runRestart: func(context.Context, []string) error {
			t.Fatalf("runRestart must not fire on fetch-failure path")
			return nil
		},
	})
	if err == nil {
		t.Fatalf("doUpdate: expected error, got nil; output:\n%s", out.String())
	}

	assertBinaryUnchangedE2E(t, s)
	assertDaemonAliveE2E(t, s)
	assertNoStragglersE2E(t, s)
	assertNoSuccessLineE2E(t, &out, "v999.0.0")
}

// TestUpdate_VerifyFailure_E2E exercises the checksum-mismatch path: the
// tarball downloads cleanly but the published digest doesn't match. As
// with the fetch-failure case, doUpdate must return before AtomicReplace
// runs and the daemon must be untouched.
func TestUpdate_VerifyFailure_E2E(t *testing.T) {
	s := installPreUpdateDaemonE2E(t)
	t.Cleanup(func() { _ = stopDaemonE2E(s.cmd1, s.done1, s.socket) })

	// fakeRelease produces a correctly-keyed checksums body; swap it for
	// a 64-zeros digest line keyed to the same asset. The server still
	// hands out the (genuine) tarball, so VerifySHA256 returns
	// ErrChecksumMismatch — surfaced as "update: verify checksum: …".
	newBytes := []byte("\x7fELF...does-not-matter...")
	asset, tgz, _ := fakeRelease(t, "v999.0.0", runtime.GOOS, runtime.GOARCH, newBytes)
	bogusSums := fmt.Sprintf("%s  %s\n", strings.Repeat("0", 64), asset)
	srv := newFakeReleaseServer(t, "v999.0.0", asset, tgz, []byte(bogusSums))

	var out bytes.Buffer
	err := doUpdate(t.Context(), updateOptions{
		currentVersion: "0.0.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return s.targetPath },
		replace:        update.AtomicReplace,
		out:            &out,
		probeRestart:   func() update.RestartProbe { return update.RestartProbe{} },
		runRestart: func(context.Context, []string) error {
			t.Fatalf("runRestart must not fire on verify-failure path")
			return nil
		},
	})
	if err == nil {
		t.Fatalf("doUpdate: expected error, got nil; output:\n%s", out.String())
	}

	assertBinaryUnchangedE2E(t, s)
	assertDaemonAliveE2E(t, s)
	assertNoSuccessLineE2E(t, &out, "v999.0.0")
	// No-stragglers check omitted: verify-failure exits doUpdate before
	// AtomicReplace runs, structurally identical to the fetch-failure
	// path that already covers it.
}

// TestUpdate_BrokenNewBinary_E2E asserts the currently-designed contract:
// pyry update has NO rollback. Once AtomicReplace swaps in the new bytes,
// the old binary is gone — if the new binary is broken, the operator must
// intervene. See docs/knowledge/features/pyry-update-command.md and
// docs/specs/architecture/187-update-atomic-replace.md.
//
// This e2e case mirrors the error contract pinned by the unit test
// TestUpdate_RestartFailure at cmd/pyry/update_test.go:438-460, end-to-end
// against a real spawned-and-immediately-dead child process.
func TestUpdate_BrokenNewBinary_E2E(t *testing.T) {
	s := installPreUpdateDaemonE2E(t)

	brokenBin := buildBrokenPyryBinE2E(t)
	brokenBytes, err := os.ReadFile(brokenBin)
	if err != nil {
		t.Fatalf("read brokenBin: %v", err)
	}
	asset, tgz, sums := fakeRelease(t, "v999.0.0", runtime.GOOS, runtime.GOARCH, brokenBytes)
	srv := newFakeReleaseServer(t, "v999.0.0", asset, tgz, []byte(sums))

	var (
		cmd2    *exec.Cmd
		stderr2 *bytes.Buffer
		done2   chan struct{}
	)
	cmd1Stopped := false
	runRestart := func(_ context.Context, _ []string) error {
		if err := stopDaemonE2E(s.cmd1, s.done1, s.socket); err != nil {
			return fmt.Errorf("stop daemon 1: %w", err)
		}
		cmd1Stopped = true
		cmd2, _, stderr2, done2 = spawnDaemonE2E(t, s.targetPath, s.home, s.socket)
		// The broken binary writes BROKEN_PYRY_TOKEN to stderr then
		// os.Exit(1). waitForSocketE2E's doneCh short-circuit picks up
		// the early exit and returns "daemon exited before ready",
		// which propagates as the "daemon restart failed" half of the
		// asserted doUpdate error message.
		return waitForSocketE2E(s.socket, done2, e2eUpdReadyDeadline)
	}

	var out bytes.Buffer
	err = doUpdate(t.Context(), updateOptions{
		currentVersion: "0.0.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return s.targetPath },
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

	t.Cleanup(func() {
		if !cmd1Stopped {
			_ = stopDaemonE2E(s.cmd1, s.done1, s.socket)
		}
		if cmd2 != nil {
			_ = stopDaemonE2E(cmd2, done2, s.socket)
		}
	})

	if err == nil {
		t.Fatalf("doUpdate: expected error, got nil; output:\n%s", out.String())
	}
	msg := err.Error()
	if !strings.Contains(msg, "binary replaced to ") {
		t.Errorf("error must mention 'binary replaced to ': %v", err)
	}
	if !strings.Contains(msg, "daemon restart failed") {
		t.Errorf("error must mention 'daemon restart failed': %v", err)
	}

	// AtomicReplace ran: inode changed AND the on-disk bytes are the
	// broken bytes. No rollback by design.
	inodeAfter := inodeOfE2E(t, s.targetPath)
	if inodeAfter == s.inodeBefore {
		t.Errorf("inode unchanged: AtomicReplace did not run; inode=%d", s.inodeBefore)
	}
	got, readErr := os.ReadFile(s.targetPath)
	if readErr != nil {
		t.Fatalf("read targetPath: %v", readErr)
	}
	if !bytes.Equal(got, brokenBytes) {
		t.Errorf("on-disk binary is not the broken bytes (size got=%d want=%d)", len(got), len(brokenBytes))
	}

	// Diagnostic guard: the broken helper's stderr must contain its
	// token. If a future change spawns something else, this assertion
	// localizes the failure cleanly instead of leaving the developer
	// chasing a generic "daemon exited before ready".
	var stderr2Bytes []byte
	if stderr2 != nil {
		stderr2Bytes = stderr2.Bytes()
	}
	if !bytes.Contains(stderr2Bytes, []byte("BROKEN_PYRY_TOKEN")) {
		t.Errorf("broken pyry stderr missing BROKEN_PYRY_TOKEN; got: %q", stderr2Bytes)
	}

	assertNoSuccessLineE2E(t, &out, "v999.0.0")
}
