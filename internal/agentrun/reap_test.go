package agentrun

import (
	"fmt"
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
// (runReapHelper, below) and asserts the reaper kills detached descendant
// groups while sparing the three guarded groups: the caller's own group,
// rootPid's own group, and init.
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

		ReapDescendantGroups(os.Getpid(), discard)

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

		ReapDescendantGroups(leaf.pid, discard)

		if !processAlive(leaf.pid) {
			t.Fatalf("root leaf (pid=%d) killed by a no-descendant reap", leaf.pid)
		}
	})

	// rootPid's own group is excluded: a grandchild left in the SAME group as
	// rootPid (the parent is its group leader, so the grandchild's pgid ==
	// rootPid) is spared by the pgid==rootPid guard. Production claude shares
	// pyry's group and is spared by the self guard; this pins the rootPid
	// guard kept for any future spawn that makes claude a group leader.
	t.Run("ExcludesRootOwnGroup", func(t *testing.T) {
		parent := startReapHelper(t, reapHelperOpts{role: "parent_same", setpgid: true, wantReport: true})
		grandchild := parent.report

		ReapDescendantGroups(parent.pid, discard)

		if !processAlive(grandchild) {
			t.Fatalf("same-group grandchild (pid=%d, in rootPid's own group) killed by reap", grandchild)
		}
		if !processAlive(parent.pid) {
			t.Fatalf("rootPid (pid=%d) killed by reap", parent.pid)
		}
	})

	// Two-level mirror of production (pyry → claude → zsh+tail): rootPid is a
	// "claude" with a grandchild in a fresh detached group. The reaper kills
	// the grandchild's group but spares rootPid, as the real reap spares
	// claude while killing the detached Bash group. One difference: this
	// "claude" leads its own group (spared by the rootPid guard), whereas
	// production claude shares pyry's group (spared by the self guard).
	t.Run("ReapsGrandchildGroupSparesRoot", func(t *testing.T) {
		parent := startReapHelper(t, reapHelperOpts{role: "parent_fresh", setpgid: true, wantReport: true})
		grandchild := parent.report // group leader of the fresh group → pgid == pid

		ReapDescendantGroups(parent.pid, discard)

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
	env := append(os.Environ(), "GO_AGENTRUN_REAP_MODE="+opts.role)

	var reportPath string
	if opts.wantReport {
		reportPath = filepath.Join(t.TempDir(), "grandchild.pid")
		env = append(env, "GO_AGENTRUN_REAP_REPORT="+reportPath)
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

// runReapHelper is the process-tree fixture for TestReapDescendantGroups,
// dispatched by GO_AGENTRUN_REAP_MODE (see the TestMain branch in
// exitclass_test.go). It never stands in for claude; it just shapes a tree the
// reaper walks:
//
//   - "leaf":         block (no children). Used as a fresh-group descendant,
//                     a same-group sibling, or a no-descendant root.
//   - "parent_fresh": spawn one "leaf" grandchild in a FRESH process group
//                     (Setpgid), report its pid via GO_AGENTRUN_REAP_REPORT,
//                     then block. Mirrors claude → zsh+tail (the reaped group).
//   - "parent_same":  spawn one "leaf" grandchild in the SAME group (no
//                     Setpgid), report its pid, then block. Exercises the
//                     "rootPid's own group is excluded" guard.
//
// The reaper kills with SIGKILL (uncatchable), so no mode needs a signal
// handler; the 30s block is a backstop so a leaked helper self-terminates.
func runReapHelper(role string) {
	switch role {
	case "leaf":
		blockUntilKilled()
	case "parent_fresh":
		spawnGrandchildAndBlock(true)
	case "parent_same":
		spawnGrandchildAndBlock(false)
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_AGENTRUN_REAP_MODE: %q\n", role)
		os.Exit(97)
	}
}

// blockUntilKilled blocks for a generous backstop window, then exits. The
// reaper (and the test's cleanup) kill via SIGKILL, which needs no handler;
// the timer only bounds a helper the test forgot to kill.
func blockUntilKilled() {
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// spawnGrandchildAndBlock re-execs this binary as a "leaf" grandchild — in a
// fresh process group when freshGroup is set — writes the grandchild's pid to
// GO_AGENTRUN_REAP_REPORT so the parent test can target its assertions, then
// blocks. It reaps the grandchild in the background so a SIGKILL'd group truly
// empties: a zombie still answers kill(-pgid, 0), which would otherwise defeat
// the test's group-gone probe.
func spawnGrandchildAndBlock(freshGroup bool) {
	reportPath := os.Getenv("GO_AGENTRUN_REAP_REPORT")
	if reportPath == "" {
		fmt.Fprintln(os.Stderr, "parent reap helper requires GO_AGENTRUN_REAP_REPORT")
		os.Exit(96)
	}
	gc := exec.Command(os.Args[0])
	// os/exec dedups env keeping the LAST occurrence, so appending
	// GO_AGENTRUN_REAP_MODE=leaf after os.Environ() (which carries the parent's
	// parent_* mode) makes the grandchild run as a leaf. Do not reorder.
	gc.Env = append(os.Environ(), "GO_AGENTRUN_REAP_MODE=leaf")
	if freshGroup {
		gc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := gc.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "parent reap helper: start grandchild: %v\n", err)
		os.Exit(95)
	}
	go func() { _ = gc.Wait() }()
	if err := os.WriteFile(reportPath, []byte(strconv.Itoa(gc.Process.Pid)), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "parent reap helper: write report: %v\n", err)
		os.Exit(94)
	}
	blockUntilKilled()
}
