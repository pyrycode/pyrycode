//go:build linux && e2e_install

// install_linux_test.go drives `pyry install-service` through a real
// systemd --user round-trip on Linux: write the unit file via
// install.Install, daemon-reload, start, assert active, exercise
// `pyry status` against the running daemon, stop, and clean up.
//
// Run with: go test -tags=e2e_install ./internal/e2e/...
//
// Separate from the `e2e` tag so default e2e CI runs don't require a
// running systemd --user session. Tests skip with a clear message when
// `systemctl --user is-system-running` reports an unusable state.
//
// Requires `systemctl --user is-system-running` to report a usable state;
// if running as a service account without an interactive session, may
// require `loginctl enable-linger <user>` once on the host.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/install"
)

const (
	systemctlTimeout = 10 * time.Second
	activePollGap    = 100 * time.Millisecond
	activeDeadline   = 10 * time.Second
	inactiveDeadline = 5 * time.Second

	// fatalNameEnv carries the unique unit name from the parent re-exec
	// test into the inner child. fatalOutEnv names the file the child
	// writes its observed (name, unitPath) state to before t.Fatal.
	fatalNameEnv = "PYRY_E2E_INSTALL_FATAL_NAME"
	fatalOutEnv  = "PYRY_E2E_INSTALL_FATAL_OUT"
)

// uniqueName returns a collision-resistant unit name. PID + nanosecond clock
// guards back-to-back invocations on the same host; the test process is
// single-tenant per run so cross-process collisions are not in scope.
func uniqueName() string {
	return fmt.Sprintf("pyry-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
}

// skipIfNoUserSystemd skips the test when the host has no usable user
// systemd session — common on CI runners (`ubuntu-latest` has no D-Bus
// session for the runner user) and in containers without --user dbus.
//
// `is-system-running` exits non-zero on degraded/maintenance/etc but those
// states are still usable for our purposes. The unusable states are
// "offline" (no manager running) and "unknown" (no D-Bus session).
func skipIfNoUserSystemd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("systemctl not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "is-system-running").CombinedOutput()
	state := strings.TrimSpace(string(out))
	if err != nil && (state == "offline" || state == "unknown" || state == "") {
		t.Skipf("user systemd unusable: state=%q err=%v", state, err)
	}
}

// runSystemctl runs `systemctl --user <args...>` with a bounded timeout.
// Returns combined output for diagnosis. Errors are returned, not
// t.Fatal'd — cleanup paths swallow them.
func runSystemctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	full := append([]string{"--user"}, args...)
	return exec.CommandContext(ctx, "systemctl", full...).CombinedOutput()
}

// waitForActive polls `systemctl --user is-active <name>` until it reports
// "active" or the deadline expires. Failure dumps `systemctl status` for
// post-mortem diagnosis.
func waitForActive(t *testing.T, name string) {
	t.Helper()
	end := time.Now().Add(activeDeadline)
	for time.Now().Before(end) {
		out, _ := exec.Command("systemctl", "--user", "is-active", name).Output()
		if strings.TrimSpace(string(out)) == "active" {
			return
		}
		time.Sleep(activePollGap)
	}
	status, _ := runSystemctl("status", "--no-pager", name)
	t.Fatalf("service %s did not reach active within %s\n%s", name, activeDeadline, status)
}

// waitForInactive polls until `systemctl --user is-active <name>` reports
// anything other than "active". After `systemctl stop` returns, the unit
// has been asked to stop but may still be terminating; this gates the
// subsequent assertions.
func waitForInactive(t *testing.T, name string) {
	t.Helper()
	end := time.Now().Add(inactiveDeadline)
	for time.Now().Before(end) {
		out, _ := exec.Command("systemctl", "--user", "is-active", name).Output()
		if strings.TrimSpace(string(out)) != "active" {
			return
		}
		time.Sleep(activePollGap)
	}
	t.Fatalf("service %s still active after %s", name, inactiveDeadline)
}

// cleanupSystemdUnit is idempotent best-effort teardown. Each step ignores
// errors but logs combined output via t.Logf so a failing test still
// surfaces useful diagnostics. Also wipes ~/.pyry/<name> and the socket
// file the daemon created at runtime.
func cleanupSystemdUnit(t *testing.T, name, unitPath, homeDir string) {
	t.Helper()
	if out, err := runSystemctl("stop", name); err != nil {
		t.Logf("cleanup: systemctl stop %s: %v\n%s", name, err, out)
	}
	if out, err := runSystemctl("disable", name); err != nil {
		t.Logf("cleanup: systemctl disable %s: %v\n%s", name, err, out)
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		t.Logf("cleanup: remove %s: %v", unitPath, err)
	}
	if out, err := runSystemctl("daemon-reload"); err != nil {
		t.Logf("cleanup: systemctl daemon-reload: %v\n%s", err, out)
	}
	// Best-effort runtime artefact cleanup: registry dir + socket file.
	_ = os.RemoveAll(filepath.Join(homeDir, ".pyry", name))
	_ = os.Remove(filepath.Join(homeDir, ".pyry", name+".sock"))
}

// TestE2EInstall_RoundTrip_Linux drives the full install → start → query
// → stop → uninstall round-trip against the operator's real user systemd.
// Cleanup is registered before any state-changing step so an early failure
// still tears down.
func TestE2EInstall_RoundTrip_Linux(t *testing.T) {
	skipIfNoUserSystemd(t)

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		t.Skipf("os.UserHomeDir: %v", err)
	}

	bin := ensurePyryBuilt(t)
	name := uniqueName()
	wantUnitPath := filepath.Join(homeDir, ".config/systemd/user", name+".service")

	// Register cleanup BEFORE Install so a failure between here and the end
	// of the test still removes the unit file and stops the service.
	t.Cleanup(func() { cleanupSystemdUnit(t, name, wantUnitPath, homeDir) })

	// `install.Install` is called directly rather than via the CLI binary:
	// the CLI mapping (runInstallService → install.Options) is mechanical
	// and already covered by install_test.go; the e2e value here is in the
	// systemd round-trip, not in re-testing flag parsing.
	//
	// `-pyry-claude` and `-pyry-idle-timeout` are pyry flags, not claude
	// flags — but install.Install just appends ClaudeArgs verbatim to
	// ExecArgs. When systemd starts the unit, pyry's flag.Parse consumes
	// them; the `--` separator hands `infinity` to claude (= /bin/sleep).
	unitPath, plat, err := install.Install(install.Options{
		Platform: install.PlatformSystemd,
		Name:     name,
		Binary:   bin,
		HomeDir:  homeDir,
		ClaudeArgs: []string{
			"-pyry-claude=/bin/sleep",
			"-pyry-idle-timeout=0",
			"--", "infinity",
		},
	})
	if err != nil {
		t.Fatalf("install.Install: %v", err)
	}
	if plat != install.PlatformSystemd {
		t.Fatalf("plat = %v, want systemd", plat)
	}
	if unitPath != wantUnitPath {
		t.Fatalf("unitPath = %q, want %q", unitPath, wantUnitPath)
	}

	info, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat unit file: %v", err)
	}
	if info.Mode().IsRegular() == false {
		t.Fatalf("unit file is not a regular file: %v", info.Mode())
	}

	body, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit file: %v", err)
	}
	if !bytes.Contains(body, []byte(`Environment="PATH=`)) {
		t.Fatalf("unit file missing PATH environment line:\n%s", body)
	}

	if out, err := runSystemctl("daemon-reload"); err != nil {
		t.Fatalf("systemctl daemon-reload: %v\n%s", err, out)
	}
	if out, err := runSystemctl("start", name); err != nil {
		status, _ := runSystemctl("status", "--no-pager", name)
		t.Fatalf("systemctl start %s: %v\n%s\n--- status ---\n%s", name, err, out, status)
	}

	waitForActive(t, name)

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

	if out, err := runSystemctl("stop", name); err != nil {
		t.Fatalf("systemctl stop %s: %v\n%s", name, err, out)
	}
	waitForInactive(t, name)
}

// TestE2EInstall_PathInheritance_Linux is the regression guard for bug
// #19: the generator must emit Environment="PATH=..." that mirrors the
// install-time process's effective PATH, with $HOME/ → %h/ substitution
// for systemd portability. No real systemd is required; we read back the
// rendered file and assert on its contents.
func TestE2EInstall_PathInheritance_Linux(t *testing.T) {
	envPath := os.Getenv("PATH")
	if envPath == "" {
		t.Skip("$PATH is empty; nothing to assert against")
	}
	homeDir := t.TempDir()

	unitPath, _, err := install.Install(install.Options{
		Platform: install.PlatformSystemd,
		Name:     uniqueName(),
		Binary:   "/usr/bin/true",
		HomeDir:  homeDir,
		EnvPath:  envPath,
	})
	if err != nil {
		t.Fatalf("install.Install: %v", err)
	}

	body, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit file: %v", err)
	}

	// Find the PATH line: `Environment="PATH=<value>"`.
	const prefix = `Environment="PATH=`
	const suffix = `"`
	var pathLine string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, prefix) {
			pathLine = strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
			break
		}
	}
	if pathLine == "" {
		t.Fatalf("unit file missing %q line:\n%s", prefix, body)
	}

	rendered := strings.Split(pathLine, ":")
	have := make(map[string]bool, len(rendered))
	for _, e := range rendered {
		have[e] = true
	}

	homePrefix := homeDir
	if !strings.HasSuffix(homePrefix, "/") {
		homePrefix += "/"
	}

	for _, entry := range strings.Split(envPath, ":") {
		if entry == "" {
			continue
		}
		want := entry
		// Mirrors derivePathEnv: $HOME/foo → %h/foo for systemd.
		if homePrefix != "/" && strings.HasPrefix(entry, homePrefix) {
			want = "%h/" + strings.TrimPrefix(entry, homePrefix)
		}
		if !have[want] {
			t.Errorf("rendered PATH missing entry %q (from $PATH entry %q)\nrendered: %s",
				want, entry, pathLine)
		}
	}
}

// TestE2EInstall_CleanupOnFatal_Linux verifies AC #4: cleanup runs on
// failure paths. The same-process `t.Fatal` shortcut doesn't exercise the
// real failure-cleanup contract (see docs/lessons.md "E2E harness:
// same-process t.Fatal..."), so we re-exec the test binary into
// TestInstallFatalChild, let it install + start + then t.Fatal, and
// inspect the post-state externally.
func TestE2EInstall_CleanupOnFatal_Linux(t *testing.T) {
	skipIfNoUserSystemd(t)

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		t.Skipf("os.UserHomeDir: %v", err)
	}

	name := uniqueName()
	statePath := filepath.Join(t.TempDir(), "state")
	unitPath := filepath.Join(homeDir, ".config/systemd/user", name+".service")

	// Belt-and-suspenders: if the child's own cleanup ever fails, this
	// parent-side cleanup catches the leak and keeps subsequent test runs
	// from inheriting a stale unit.
	t.Cleanup(func() { cleanupSystemdUnit(t, name, unitPath, homeDir) })

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
	gotName, gotUnitPath := parts[0], parts[1]
	if gotName != name {
		t.Fatalf("child name = %q, want %q", gotName, name)
	}
	if gotUnitPath != unitPath {
		t.Fatalf("child unitPath = %q, want %q", gotUnitPath, unitPath)
	}

	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Errorf("unit file %s not removed after fatal: stat err=%v\nchild output:\n%s",
			unitPath, err, combined)
	}
	out, _ := exec.Command("systemctl", "--user", "is-active", name).Output()
	if state := strings.TrimSpace(string(out)); state == "active" {
		t.Errorf("service %s still active after child cleanup\nchild output:\n%s", name, combined)
	}
}

// TestInstallFatalChild is the inner half of TestE2EInstall_CleanupOnFatal_Linux.
// In normal test runs the env vars are unset and the test is a no-op;
// under re-exec it installs + starts a real unit, records the observed
// state, and t.Fatals so the parent can verify cleanup ran.
func TestInstallFatalChild(t *testing.T) {
	name := os.Getenv(fatalNameEnv)
	out := os.Getenv(fatalOutEnv)
	if name == "" || out == "" {
		t.Skip("fatal-child only runs under TestE2EInstall_CleanupOnFatal_Linux")
	}
	skipIfNoUserSystemd(t)

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		t.Skipf("os.UserHomeDir: %v", err)
	}

	bin := ensurePyryBuilt(t)
	unitPath := filepath.Join(homeDir, ".config/systemd/user", name+".service")

	t.Cleanup(func() { cleanupSystemdUnit(t, name, unitPath, homeDir) })

	if _, _, err := install.Install(install.Options{
		Platform: install.PlatformSystemd,
		Name:     name,
		Binary:   bin,
		HomeDir:  homeDir,
		ClaudeArgs: []string{
			"-pyry-claude=/bin/sleep",
			"-pyry-idle-timeout=0",
			"--", "infinity",
		},
	}); err != nil {
		t.Fatalf("install.Install: %v", err)
	}
	if reloadOut, err := runSystemctl("daemon-reload"); err != nil {
		t.Fatalf("systemctl daemon-reload: %v\n%s", err, reloadOut)
	}
	if startOut, err := runSystemctl("start", name); err != nil {
		t.Fatalf("systemctl start %s: %v\n%s", name, err, startOut)
	}
	waitForActive(t, name)

	state := fmt.Sprintf("%s\n%s\n", name, unitPath)
	if err := os.WriteFile(out, []byte(state), 0o600); err != nil {
		t.Fatalf("write child state: %v", err)
	}
	t.Fatal("inject failure to exercise cleanup-on-fatal")
}
