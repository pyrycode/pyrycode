//go:build darwin && e2e_install

// install_darwin_test.go drives `pyry install-service` through a real
// launchd round-trip on macOS: write the plist via install.Install,
// `launchctl bootstrap gui/<uid>`, poll for liveness (bootstrap is async),
// exercise `pyry status` against the running daemon, then `launchctl
// bootout` and remove the plist.
//
// Run with: go test -tags=e2e_install ./internal/e2e/...
//
// Same tag as the Linux sibling (install_linux_test.go) so a single
// `go test -tags=e2e_install` invocation covers both platforms in CI.
// Tests skip with a clear message when launchctl is missing or when
// running as root (system-domain bootstrap is not what we ship).
//
// gui/<uid> requires a logged-in GUI session for the running uid; on
// headless CI without one, the bootstrap step will fail loudly.
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
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/install"
)

const (
	launchctlTimeout = 10 * time.Second
	runningPollGap   = 100 * time.Millisecond
	runningDeadline  = 10 * time.Second
	unloadedDeadline = 5 * time.Second

	// fatalNameEnv carries the unique service name from the parent re-exec
	// test into the inner child. fatalOutEnv names the file the child
	// writes its observed (name, plistPath) state to before t.Fatal.
	fatalNameEnv = "PYRY_E2E_INSTALL_FATAL_NAME"
	fatalOutEnv  = "PYRY_E2E_INSTALL_FATAL_OUT"
)

// uniqueName returns a collision-resistant service name. PID + nanosecond
// clock guards back-to-back invocations on the same host; the test process
// is single-tenant per run so cross-process collisions are not in scope.
func uniqueName() string {
	return fmt.Sprintf("pyry-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
}

// skipIfNoLaunchctl skips the test when launchctl isn't on PATH. Defensive
// — launchctl is part of macOS's base system so this should never trigger
// in practice.
func skipIfNoLaunchctl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("launchctl"); err != nil {
		t.Skip("launchctl not on PATH")
	}
}

// skipIfRoot skips the test when running as root. The gui/<uid> domain is
// for non-root users; system-domain bootstrap is out of scope for what
// pyry ships.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("gui/<uid> domain is for non-root users; system-domain is out of scope")
	}
}

// runLaunchctl runs `launchctl <args...>` with a bounded timeout. Returns
// combined output for diagnosis. Errors are returned, not t.Fatal'd —
// cleanup paths swallow them.
func runLaunchctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), launchctlTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
}

// waitForRunning polls `launchctl print gui/<uid>/<label>` until its output
// contains `state = running` or the deadline expires. Failure dumps the
// last `print` output for post-mortem diagnosis.
//
// `launchctl print` is a debug command whose output format is technically
// unstable across macOS releases — `state = running` has been stable since
// the modern launchd CLI shipped, but if Apple ever reformats it, this
// test breaks loudly with a clear diagnostic.
func waitForRunning(t *testing.T, uid int, label string) {
	t.Helper()
	target := fmt.Sprintf("gui/%d/%s", uid, label)
	end := time.Now().Add(runningDeadline)
	for time.Now().Before(end) {
		out, err := exec.Command("launchctl", "print", target).CombinedOutput()
		if err == nil && bytes.Contains(out, []byte("state = running")) {
			return
		}
		time.Sleep(runningPollGap)
	}
	out, _ := exec.Command("launchctl", "print", target).CombinedOutput()
	t.Fatalf("service %s did not reach running within %s\n%s", label, runningDeadline, out)
}

// waitForSocketReady polls until the daemon's control socket is dialable
// or the deadline expires. `launchctl print` reports `state = running` as
// soon as launchd has spawned the program — pyry still needs a few ms to
// bind its socket. Mirroring harness.go's waitForReady, dial-success is
// the real "daemon is responsive" gate.
func waitForSocketReady(t *testing.T, socketPath string) {
	t.Helper()
	end := time.Now().Add(runningDeadline)
	for time.Now().Before(end) {
		if _, err := os.Stat(socketPath); err == nil {
			c, err := net.Dial("unix", socketPath)
			if err == nil {
				_ = c.Close()
				return
			}
		}
		time.Sleep(runningPollGap)
	}
	t.Fatalf("socket %s not dialable within %s", socketPath, runningDeadline)
}

// waitForUnloaded polls until `launchctl print gui/<uid>/<label>` exits
// non-zero (job no longer registered).
func waitForUnloaded(t *testing.T, uid int, label string) {
	t.Helper()
	target := fmt.Sprintf("gui/%d/%s", uid, label)
	end := time.Now().Add(unloadedDeadline)
	for time.Now().Before(end) {
		if err := exec.Command("launchctl", "print", target).Run(); err != nil {
			return
		}
		time.Sleep(runningPollGap)
	}
	t.Fatalf("service %s still registered after %s", label, unloadedDeadline)
}

// cleanupLaunchdJob is idempotent best-effort teardown. Each step ignores
// errors but logs combined output via t.Logf so a failing test still
// surfaces useful diagnostics. Also wipes ~/.pyry/<name>, the socket file,
// and the launchd /tmp log files the template hardcodes.
func cleanupLaunchdJob(t *testing.T, uid int, label, plistPath, homeDir, name string) {
	t.Helper()
	if out, err := runLaunchctl("bootout", fmt.Sprintf("gui/%d/%s", uid, label)); err != nil {
		t.Logf("cleanup: launchctl bootout %s: %v\n%s", label, err, out)
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		t.Logf("cleanup: remove %s: %v", plistPath, err)
	}
	// Best-effort runtime artefact cleanup: registry dir, socket, log files.
	_ = os.RemoveAll(filepath.Join(homeDir, ".pyry", name))
	_ = os.Remove(filepath.Join(homeDir, ".pyry", name+".sock"))
	_ = os.Remove(fmt.Sprintf("/tmp/pyry.%s.out.log", name))
	_ = os.Remove(fmt.Sprintf("/tmp/pyry.%s.err.log", name))
}

// TestE2EInstall_RoundTrip_macOS drives the full install → bootstrap →
// query → bootout → uninstall round-trip against the operator's real
// launchd. Cleanup is registered before any state-changing step so an
// early failure still tears down.
//
// Cannot isolate $HOME: launchctl bootstrap gui/<uid> runs services in
// the real user GUI domain regardless of the test process's $HOME, so the
// test uses the operator's ~/Library/LaunchAgents/ and cleans up rigorously.
func TestE2EInstall_RoundTrip_macOS(t *testing.T) {
	skipIfNoLaunchctl(t)
	skipIfRoot(t)

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		t.Skipf("os.UserHomeDir: %v", err)
	}

	bin := ensurePyryBuilt(t)
	name := uniqueName()
	label := "dev.pyrycode." + name
	wantPlistPath := filepath.Join(homeDir, "Library/LaunchAgents", label+".plist")
	uid := os.Getuid()

	// Register cleanup BEFORE Install so a failure between here and the end
	// of the test still removes the plist and unloads the service.
	t.Cleanup(func() { cleanupLaunchdJob(t, uid, label, wantPlistPath, homeDir, name) })

	// `install.Install` is called directly rather than via the CLI binary:
	// the CLI mapping (runInstallService → install.Options) is mechanical
	// and already covered by install_test.go; the e2e value here is in the
	// launchd round-trip, not in re-testing flag parsing.
	//
	// `-pyry-claude` and `-pyry-idle-timeout` are pyry flags, not claude
	// flags — but install.Install just appends ClaudeArgs verbatim to
	// ExecArgs. When launchd starts the job, pyry's flag.Parse consumes
	// them; the `--` separator hands `infinity` to claude (= /bin/sleep).
	// WorkDir is set explicitly to homeDir (always exists). launchd's
	// WorkingDirectory is fatal if the directory is missing — chdir
	// failure before exec surfaces as EX_CONFIG and the job never reaches
	// `state = running`. The default `~/pyry-workspace` is what the
	// operator is expected to mkdir before starting their service; here
	// we sidestep that to keep the test about the round-trip itself, not
	// about workspace setup.
	plistPath, plat, err := install.Install(install.Options{
		Platform: install.PlatformLaunchd,
		Name:     name,
		Binary:   bin,
		HomeDir:  homeDir,
		WorkDir:  homeDir,
		ClaudeArgs: []string{
			"-pyry-claude=/bin/sleep",
			"-pyry-idle-timeout=0",
			"--", "infinity",
		},
	})
	if err != nil {
		t.Fatalf("install.Install: %v", err)
	}
	if plat != install.PlatformLaunchd {
		t.Fatalf("plat = %v, want launchd", plat)
	}
	if plistPath != wantPlistPath {
		t.Fatalf("plistPath = %q, want %q", plistPath, wantPlistPath)
	}

	info, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("stat plist: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("plist is not a regular file: %v", info.Mode())
	}

	body, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	if !bytes.Contains(body, []byte("<key>EnvironmentVariables</key>")) {
		t.Fatalf("plist missing EnvironmentVariables key:\n%s", body)
	}
	if !bytes.Contains(body, []byte("<key>PATH</key>")) {
		t.Fatalf("plist missing PATH key:\n%s", body)
	}

	if out, err := runLaunchctl("bootstrap", fmt.Sprintf("gui/%d", uid), plistPath); err != nil {
		t.Fatalf("launchctl bootstrap gui/%d %s: %v\n%s", uid, plistPath, err, out)
	}

	waitForRunning(t, uid, label)
	waitForSocketReady(t, filepath.Join(homeDir, ".pyry", name+".sock"))

	// `pyry status` is invoked directly (not via h.Run) because the
	// harness auto-injects -pyry-socket=<h.SocketPath>; here the daemon's
	// socket is at ~/.pyry/<name>.sock, derived by resolveSocketPath from
	// -pyry-name. Same machinery the operator uses.
	statusCtx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	statusCmd := exec.CommandContext(statusCtx, bin, "status", "-pyry-name="+name)
	var statusOut, statusErr bytes.Buffer
	statusCmd.Stdout = &statusOut
	statusCmd.Stderr = &statusErr
	if err := statusCmd.Run(); err != nil {
		t.Fatalf("pyry status: %v\nstdout:\n%s\nstderr:\n%s", err, statusOut.String(), statusErr.String())
	}
	if !bytes.Contains(statusOut.Bytes(), []byte("Phase:")) {
		t.Fatalf("pyry status missing %q line:\n%s", "Phase:", statusOut.String())
	}

	if out, err := runLaunchctl("bootout", fmt.Sprintf("gui/%d/%s", uid, label)); err != nil {
		t.Fatalf("launchctl bootout %s: %v\n%s", label, err, out)
	}
	waitForUnloaded(t, uid, label)
}

// TestE2EInstall_PathInheritance_macOS is the regression guard for bug
// #19: the generator must emit an EnvironmentVariables.PATH that mirrors
// the install-time process's effective PATH. No real launchd is required;
// we read back the rendered plist via plutil and assert on its contents.
//
// Unlike systemd, derivePathEnv does NO $HOME/ → %h/ substitution for
// launchd — the literal absolute paths land in the plist verbatim.
func TestE2EInstall_PathInheritance_macOS(t *testing.T) {
	envPath := os.Getenv("PATH")
	if envPath == "" {
		t.Skip("$PATH is empty; nothing to assert against")
	}
	tempHome := t.TempDir()

	plistPath, _, err := install.Install(install.Options{
		Platform: install.PlatformLaunchd,
		Name:     uniqueName(),
		Binary:   "/usr/bin/true",
		HomeDir:  tempHome,
		EnvPath:  envPath,
	})
	if err != nil {
		t.Fatalf("install.Install: %v", err)
	}

	// plutil is part of macOS's base system; absence is a broken host, not
	// a skip condition. `-extract <keypath> raw -o -` prints just the
	// scalar value, no XML wrapping. Apple-blessed parsing avoids
	// reinventing an XML decoder for the dict alternation.
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx,
		"plutil", "-extract", "EnvironmentVariables.PATH", "raw", "-o", "-", plistPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil -extract EnvironmentVariables.PATH: %v\n%s", err, out)
	}
	renderedPath := strings.TrimSpace(string(out))
	if renderedPath == "" {
		t.Fatalf("plutil returned empty PATH")
	}

	have := make(map[string]bool)
	for _, e := range strings.Split(renderedPath, ":") {
		have[e] = true
	}

	for _, entry := range strings.Split(envPath, ":") {
		if entry == "" {
			continue
		}
		if !have[entry] {
			t.Errorf("rendered PATH missing entry %q\nrendered: %s", entry, renderedPath)
		}
	}
}

// TestE2EInstall_CleanupOnFatal_macOS verifies AC #4: cleanup runs on
// failure paths. The same-process `t.Fatal` shortcut doesn't exercise the
// real failure-cleanup contract (see docs/lessons.md "E2E harness:
// same-process t.Fatal..."), so we re-exec the test binary into
// TestInstallFatalChild, let it install + bootstrap + then t.Fatal, and
// inspect the post-state externally.
func TestE2EInstall_CleanupOnFatal_macOS(t *testing.T) {
	skipIfNoLaunchctl(t)
	skipIfRoot(t)

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		t.Skipf("os.UserHomeDir: %v", err)
	}

	name := uniqueName()
	label := "dev.pyrycode." + name
	plistPath := filepath.Join(homeDir, "Library/LaunchAgents", label+".plist")
	statePath := filepath.Join(t.TempDir(), "state")
	uid := os.Getuid()

	// Belt-and-suspenders: if the child's own cleanup ever fails, this
	// parent-side cleanup catches the leak and keeps subsequent test runs
	// from inheriting a stale plist or registered job.
	t.Cleanup(func() { cleanupLaunchdJob(t, uid, label, plistPath, homeDir, name) })

	cmd := exec.Command(os.Args[0],
		"-test.run=^TestInstallFatalChild$",
		"-test.count=1",
	)
	cmd.Env = append(os.Environ(),
		fatalNameEnv+"="+name,
		fatalOutEnv+"="+statePath,
	)
	combined, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("child re-exec expected to fail (t.Fatal injected) but exited 0\n%s", combined)
	}

	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("child did not write state file: %v\nchild output:\n%s", err, combined)
	}
	parts := strings.SplitN(strings.TrimSpace(string(state)), "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed child state %q\nchild output:\n%s", state, combined)
	}
	gotName, gotPlistPath := parts[0], parts[1]
	if gotName != name {
		t.Fatalf("child name = %q, want %q", gotName, name)
	}
	if gotPlistPath != plistPath {
		t.Fatalf("child plistPath = %q, want %q", gotPlistPath, plistPath)
	}

	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Errorf("plist %s not removed after fatal: stat err=%v\nchild output:\n%s",
			plistPath, err, combined)
	}
	if err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", uid, label)).Run(); err == nil {
		t.Errorf("service %s still registered after child cleanup\nchild output:\n%s", label, combined)
	}
}

// TestInstallFatalChild is the inner half of TestE2EInstall_CleanupOnFatal_macOS.
// In normal test runs the env vars are unset and the test is a no-op;
// under re-exec it installs + bootstraps a real launchd job, records the
// observed state, and t.Fatals so the parent can verify cleanup ran.
func TestInstallFatalChild(t *testing.T) {
	name := os.Getenv(fatalNameEnv)
	out := os.Getenv(fatalOutEnv)
	if name == "" || out == "" {
		t.Skip("fatal-child only runs under TestE2EInstall_CleanupOnFatal_macOS")
	}
	skipIfNoLaunchctl(t)
	skipIfRoot(t)

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		t.Skipf("os.UserHomeDir: %v", err)
	}

	bin := ensurePyryBuilt(t)
	label := "dev.pyrycode." + name
	plistPath := filepath.Join(homeDir, "Library/LaunchAgents", label+".plist")
	uid := os.Getuid()

	t.Cleanup(func() { cleanupLaunchdJob(t, uid, label, plistPath, homeDir, name) })

	if _, _, err := install.Install(install.Options{
		Platform: install.PlatformLaunchd,
		Name:     name,
		Binary:   bin,
		HomeDir:  homeDir,
		WorkDir:  homeDir,
		ClaudeArgs: []string{
			"-pyry-claude=/bin/sleep",
			"-pyry-idle-timeout=0",
			"--", "infinity",
		},
	}); err != nil {
		t.Fatalf("install.Install: %v", err)
	}
	if bootOut, err := runLaunchctl("bootstrap", fmt.Sprintf("gui/%d", uid), plistPath); err != nil {
		t.Fatalf("launchctl bootstrap gui/%d %s: %v\n%s", uid, plistPath, err, bootOut)
	}
	waitForRunning(t, uid, label)
	waitForSocketReady(t, filepath.Join(homeDir, ".pyry", name+".sock"))

	state := fmt.Sprintf("%s\n%s\n", name, plistPath)
	if err := os.WriteFile(out, []byte(state), 0o600); err != nil {
		t.Fatalf("write child state: %v", err)
	}
	t.Fatal("inject failure to exercise cleanup-on-fatal")
}
