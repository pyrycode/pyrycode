package ptyrunner

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestReapDescendantGroups is the CI-runnable load-bearing net for the #565
// reaper. It builds real process trees with the reap-helper modes
// (helper_test.go) and asserts the reaper kills detached descendant groups
// while sparing the three guarded groups: the caller's own group, rootPid's
// own group, and init.
//
// Subtests run sequentially (no t.Parallel): two of them reap at os.Getpid(),
// which sweeps every fresh-group descendant of the whole test process, so a
// concurrent subtest's fresh-group helper would be collateral. Each subtest's
// helpers are killed and reaped by their own t.Cleanup before the next starts.
func TestReapDescendantGroups(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Reaps a descendant group, spares the caller's own group (the suicide
	// guard). A fresh-group child is a descendant of the test in its own
	// group → reaped. A same-group sibling shares the test's group → skipped,
	// proving the reaper never SIGKILLs its own group (which contains pyry).
	t.Run("ReapsDescendantGroupSparesCaller", func(t *testing.T) {
		fresh := startReapHelper(t, reapHelperOpts{role: "leaf", setpgid: true})
		sibling := startReapHelper(t, reapHelperOpts{role: "leaf", setpgid: false})

		reapDescendantGroups(os.Getpid(), discard)

		// fresh is a group leader (Setpgid), so its pgid == its pid.
		if !waitGroupGone(fresh.pid, 2*time.Second) {
			t.Fatalf("fresh-group descendant (pgid=%d) still alive after reap — not reaped", fresh.pid)
		}
		if !processAlive(sibling.pid) {
			t.Fatalf("same-group sibling (pid=%d) killed by reap — suicide guard failed", sibling.pid)
		}
		if !processAlive(os.Getpid()) {
			t.Fatal("caller process killed by reap — suicide guard failed")
		}
	})

	// No-op when rootPid has no descendants: the reaper finds nothing and
	// kills nothing; the leaf (the root itself, never a descendant) survives.
	t.Run("NoDescendantsIsNoOp", func(t *testing.T) {
		leaf := startReapHelper(t, reapHelperOpts{role: "leaf", setpgid: true})

		reapDescendantGroups(leaf.pid, discard)

		if !processAlive(leaf.pid) {
			t.Fatalf("root leaf (pid=%d) killed by a no-descendant reap", leaf.pid)
		}
	})

	// rootPid's own group is excluded: a grandchild left in the SAME group as
	// rootPid (the parent is its group leader, so the grandchild's pgid ==
	// rootPid) is spared by the pgid==rootPid guard. This is the guard that
	// stops the reaper from killing claude's own group instead of leaving it
	// to sess.Close.
	t.Run("ExcludesRootOwnGroup", func(t *testing.T) {
		parent := startReapHelper(t, reapHelperOpts{role: "parent_same", setpgid: true, wantReport: true})
		grandchild := parent.report

		reapDescendantGroups(parent.pid, discard)

		if !processAlive(grandchild) {
			t.Fatalf("same-group grandchild (pid=%d, in rootPid's own group) killed by reap", grandchild)
		}
		if !processAlive(parent.pid) {
			t.Fatalf("rootPid (pid=%d) killed by reap", parent.pid)
		}
	})

	// Faithful two-level mirror of production (pyry → claude → zsh+tail):
	// rootPid is a "claude" with a grandchild in a fresh detached group. The
	// reaper kills the grandchild's group but spares rootPid (its own group),
	// exactly as the real reap leaves claude for sess.Close while killing the
	// detached Bash group.
	t.Run("ReapsGrandchildGroupSparesRoot", func(t *testing.T) {
		parent := startReapHelper(t, reapHelperOpts{role: "parent_fresh", setpgid: true, wantReport: true})
		grandchild := parent.report // group leader of the fresh group → pgid == pid

		reapDescendantGroups(parent.pid, discard)

		if !waitGroupGone(grandchild, 2*time.Second) {
			t.Fatalf("fresh-group grandchild (pgid=%d) still alive after reap — not reaped", grandchild)
		}
		if !processAlive(parent.pid) {
			t.Fatalf("rootPid (pid=%d) killed by reap — only its descendant group should die", parent.pid)
		}
	})
}

type reapHelperOpts struct {
	role       string // "leaf" | "parent_fresh" | "parent_same"
	setpgid    bool   // place the helper in its own process group (group leader)
	wantReport bool   // read the grandchild pid the parent_* roles report
}

type reapHelper struct {
	pid    int // the helper's pid (== its pgid when setpgid)
	report int // grandchild pid reported by parent_* roles; 0 otherwise
}

// startReapHelper re-execs the test binary as a reap-tree fixture (see
// runReapHelper), registers a t.Cleanup that SIGKILLs its group and pid, and
// returns its pid (plus the reported grandchild pid for parent_* roles). A
// background Wait reaps the helper when it dies — killed by the reaper under
// test or by cleanup — so a SIGKILL'd direct child does not linger as a zombie
// (a zombie still answers kill(-pgid, 0), which would defeat waitGroupGone).
func startReapHelper(t *testing.T, opts reapHelperOpts) reapHelper {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	env := append(os.Environ(), "GO_PTYRUNNER_HELPER=1", "GO_PTYRUNNER_REAP_MODE="+opts.role)

	var reportPath string
	if opts.wantReport {
		reportPath = filepath.Join(t.TempDir(), "grandchild.pid")
		env = append(env, "GO_PTYRUNNER_REAP_REPORT="+reportPath)
	}
	cmd.Env = env
	if opts.setpgid {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start reap helper (role=%s): %v", opts.role, err)
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL) // group (covers a fresh-group grandchild it leads)
		_ = syscall.Kill(pid, syscall.SIGKILL)  // the pid itself (same-group helpers)
	})

	h := reapHelper{pid: pid}
	if opts.wantReport {
		h.report = waitReport(t, reportPath, 5*time.Second)
		// A reported grandchild may live in its own group (parent_fresh) and
		// thus survive the parent-group cleanup above — reap it explicitly so
		// a regressed reaper that fails to kill it does not leak past the test.
		gc := h.report
		t.Cleanup(func() {
			_ = syscall.Kill(-gc, syscall.SIGKILL)
			_ = syscall.Kill(gc, syscall.SIGKILL)
		})
	}
	return h
}

// waitReport polls the report file the parent_* helpers write until it holds a
// parseable pid. The helper writes the file in one shot then blocks, so a
// short poll (tolerating a transient empty/partial read) is enough.
func waitReport(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(b))); cerr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild pid not reported at %s within %s", path, timeout)
	return 0
}

// processAlive reports whether pid is alive. Signal 0 delivers nothing and
// returns ESRCH once the process is gone.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitGroupGone returns true once no process in group pgid is alive, polling up
// to timeout. kill(-pgid, 0) probes the whole group without delivering a
// signal: a non-nil error (ESRCH) means every member is reaped.
func waitGroupGone(pgid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if syscall.Kill(-pgid, 0) != nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
