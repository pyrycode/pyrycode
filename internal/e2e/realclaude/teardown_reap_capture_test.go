//go:build e2e_realclaude

package realclaude

// Drives #1250's reap-log classifier with bytes captured from a REAL
// agentrun.ReapDescendantGroups call over a process tree this file builds.
//
// #1250's fixtures are hand-written string constants headed "measured
// 2026-07-30 on Darwin 25.5 against Go's log/slog" — tdnFixtureTextOne and its
// siblings through tdnFixtureOtherLines in teardown_liveness_test.go. Someone
// observed the rendering and typed it into a const; nothing executing asserts
// that the real reaper still emits those bytes. That matters because of the
// shape of the drift: a matcher that finds nothing answers no-reap-line, a
// consumer reads "the reaper never fired", and nothing goes red. Converting the
// transcription into a capture is what makes that class of drift loud.
//
// Everything here is offline: no live claude, no credentials, no daemon, no env
// gate, no t.Skip.
//
//	go test -tags e2e_realclaude -run '^TestTdn' -v ./internal/e2e/realclaude/
//
// # Reused, not rebuilt
//
// tdnClassifyReapLog, tdnReapMessage, the four verdicts, tdnIsReapVerdict and
// tdnEqualInts are #1250's (teardown_liveness_test.go). This file CALLS them
// and edits none of them: why the anchor is the bare message text and why
// membership is decided over parsed integers are argued in that file's header
// and deliberately not restated here.
//
// A separate file rather than a section of that one, because its header
// declares its contents "pure over bytes: no exec, no file read, no clock" and
// "takes no measurement". This file spawns real process trees, redirects a
// process-global writer and issues real SIGKILLs — filing it there would
// falsify that header.
//
// # Why the tree is two levels deep
//
// descendantPGIDs walks from rootPid's CHILDREN downward, and
// ReapDescendantGroups excludes pgid == rootPid, so a group that actually gets
// killed must sit two levels below the test:
//
//	test process         never the walk root — tdnCaptureReap refuses it
//	└── parent P         Setpgid → leads its own group, pgid == P == rootPid
//	    ├── leaf F       Setpgid → own group, pgid == F     → REAPED
//	    └── leaf S       no Setpgid → pgid == P == rootPid  → SPARED
//
// The intermediate has to place its own children in fresh groups, which rules
// out a shell — setsid(1) is absent on macOS, and a background job in a
// non-interactive sh stays in the shell's own group — so it is this test binary,
// re-exec'd into TestTdnReapTreeHelperProcess. internal/agentrun/reap_test.go
// reached the same conclusion for the same reason; its fixture is unexported in
// package agentrun and not importable from here, so the role is rebuilt in
// reduced form rather than shared.
//
// # No t.Parallel anywhere in this file
//
// log.SetOutput redirects a PROCESS-GLOBAL writer and the redirect is not
// reentrant, so two concurrent captures would lose one another's bytes. The
// reaps are also real SIGKILLs against real trees.
//
// # Redaction
//
// No `ps` invocation of its own — and never one through -E, -e with an
// environment column, or BSD eww, all of which print each process's full
// environment, which on an operator machine means CLAUDE_CODE_OAUTH_TOKEN and
// ANTHROPIC_API_KEY in a public issue. pinStateColumns
// (`pinStateColumns`) records that prohibition as a constant with
// an enforcing test; it is inherited here, not weakened. Liveness in this file
// is signal-zero only. Nothing here writes an artifact.
//
// No failure message prints the raw capture buffer either: log.SetOutput
// redirects a process-global writer, so for the duration of a capture that
// buffer collects whatever else the process logs through the standard logger,
// not only the reap line. Failures report outcome.Line / outcome.Detail — both
// anchored on tdnReapMessage and both already capped by reachCapCommand — never
// string(captured). Length is not the concern; provenance is.

import (
	"bytes"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// --- the reap-tree helper role ------------------------------------------------

// The reap-tree role protocol: set by tdnStartTree in the test, read by
// TestTdnReapTreeHelperProcess in the re-exec'd child.
const (
	tdnRoleEnv   = "TDN_REAP_TREE_ROLE"
	tdnFreshEnv  = "TDN_REAP_TREE_FRESH"
	tdnSameEnv   = "TDN_REAP_TREE_SAME"
	tdnReportEnv = "TDN_REAP_TREE_REPORT"

	// tdnRoleParent is the only role. Leaves need no role of their own: whether
	// a leaf leads a fresh group is decided by its SPAWNER's SysProcAttr, not by
	// the leaf, so a leaf needs no logic at all and routes through the package's
	// existing fake-pyry sleep mode (`runFakePyry`).
	tdnRoleParent = "parent"

	// tdnRoleFilter admits exactly one test in the re-exec'd child, which plays
	// its role and never returns to the framework — so the recursion is bounded
	// by construction. Without it the child reaches m.Run() and runs the suite:
	// the 2026-05-16 fork bomb TestMain's own comment records
	// (`TestMain` in fixtures_test.go).
	tdnRoleFilter = "-test.run=^TestTdnReapTreeHelperProcess$"

	// The two line kinds in the report file.
	tdnReportKindFresh = "fresh"
	tdnReportKindSame  = "same"
)

// tdnTreeBlock is how long the parent role blocks before self-terminating. A
// backstop for a leaked helper, never a timing dependency: every helper is
// SIGKILLed by the reaper under test or by t.Cleanup long before it fires. The
// leaves carry their own equivalent backstop inside runFakePyry's sleep mode.
const tdnTreeBlock = 30 * time.Second

// TestTdnReapTreeHelperProcess plays a reap-tree role when TDN_REAP_TREE_ROLE is
// set, and returns immediately otherwise.
//
// Returning, deliberately not t.Skip: this package's zero-SKIP property across
// ^TestTdn is load-bearing (#1250), so the no-role path must be a no-op PASS.
// When it does play a role it never returns to the test framework.
func TestTdnReapTreeHelperProcess(t *testing.T) {
	role := os.Getenv(tdnRoleEnv)
	if role == "" {
		return
	}
	switch role {
	case tdnRoleParent:
		tdnRunParentRole()
	default:
		fmt.Fprintf(os.Stderr, "reap-tree helper: unknown %s: %q\n", tdnRoleEnv, role)
		os.Exit(97)
	}
}

// tdnRunParentRole builds one level of the tree and blocks. It never returns.
//
// It starts TDN_REAP_TREE_FRESH leaves each leading its own process group and
// TDN_REAP_TREE_SAME leaves sharing this process's group, writes one
// "<kind> <pid>" line per leaf to TDN_REAP_TREE_REPORT in a single 0600 write
// (`spawnGrandchildAndBlock`), then blocks. Report first, block second: every child pid
// must be in the process table before ReapDescendantGroups takes its ps
// snapshot, and the test blocks on the report to know that it is.
func tdnRunParentRole() {
	reportPath := os.Getenv(tdnReportEnv)
	if reportPath == "" {
		fmt.Fprintf(os.Stderr, "reap-tree parent: %s is required\n", tdnReportEnv)
		os.Exit(96)
	}
	fresh, ferr := strconv.Atoi(os.Getenv(tdnFreshEnv))
	same, serr := strconv.Atoi(os.Getenv(tdnSameEnv))
	if ferr != nil || serr != nil || fresh < 0 || same < 0 {
		fmt.Fprintf(os.Stderr, "reap-tree parent: bad counts %s=%q %s=%q\n",
			tdnFreshEnv, os.Getenv(tdnFreshEnv), tdnSameEnv, os.Getenv(tdnSameEnv))
		os.Exit(95)
	}

	var report strings.Builder
	for _, spec := range []struct {
		kind     string
		count    int
		ownGroup bool
	}{
		{tdnReportKindFresh, fresh, true},
		{tdnReportKindSame, same, false},
	} {
		for range spec.count {
			pid, err := tdnSpawnLeaf(spec.ownGroup)
			if err != nil {
				fmt.Fprintf(os.Stderr, "reap-tree parent: start %s leaf: %v\n", spec.kind, err)
				os.Exit(94)
			}
			fmt.Fprintf(&report, "%s %d\n", spec.kind, pid)
		}
	}

	if err := os.WriteFile(reportPath, []byte(report.String()), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "reap-tree parent: write report: %v\n", err)
		os.Exit(93)
	}
	time.Sleep(tdnTreeBlock)
	os.Exit(0)
}

// tdnSpawnLeaf starts one childless blocking process — in its own process group
// when ownGroup is set — and returns its pid.
//
// The leaf is this test binary in the package's existing fake-pyry sleep mode:
// TestMain's GO_TEST_HELPER_PROCESS branch takes it before m.Run(), so it
// spawns nothing and the re-exec depth is 2 and terminal.
//
// os/exec dedups env keeping the LAST occurrence, so the role vars are appended
// AFTER os.Environ() and an operator's pre-set PYRY_E2E_FAKE_MODE cannot
// redirect a leaf into runFakePyry's argv mode — which would echo the inherited
// environment, tokens included. spawnGrandchildAndBlock in
// internal/agentrun/reap_test.go records the same ordering rule. Clearing
// TDN_REAP_TREE_ROLE is belt and braces: a leaf never reaches m.Run() to read
// it.
//
// The background Wait is the zombie guard, not hygiene: a SIGKILLed child with
// no Wait lingers in the process table with its pgid intact, and both
// kill(pid, 0) and kill(-pgid, 0) still succeed on a corpse — which would make a
// reaped-group check pass on one, and make a killed spared leaf read as still
// alive. `startReapHelper` records the same rule in its own package.
func tdnSpawnLeaf(ownGroup bool) (int, error) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"GO_TEST_HELPER_PROCESS=1",
		"PYRY_E2E_FAKE_MODE=sleep",
		tdnRoleEnv+"=",
	)
	if ownGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// --- building and tearing down a tree the test owns ---------------------------

// tdnTree is one live process tree this test owns. RootPID is what the reap walk
// is rooted at — never the test process. Fresh are the group leaders the reaper
// must kill; Same are the descendants sharing RootPID's group, which
// ReapDescendantGroups must spare.
type tdnTree struct {
	RootPID int
	Fresh   []int
	Same    []int
}

// tdnStartTree starts a parent with `fresh` fresh-group leaves and `same`
// same-group leaves, blocks until every child pid is reported, and registers
// cleanups that SIGKILL each group and pid.
func tdnStartTree(t *testing.T, fresh, same int) tdnTree {
	t.Helper()

	reportPath := filepath.Join(t.TempDir(), "reap-tree.pids")
	cmd := exec.Command(os.Args[0], tdnRoleFilter)
	cmd.Env = append(os.Environ(),
		// GO_TEST_HELPER_PROCESS cannot already be "1" here — TestMain would
		// have run this binary as fake pyry instead of running this suite — but
		// pinning it keeps the parent's dispatch independent of the ambient
		// environment, the same reason the role vars are appended last.
		"GO_TEST_HELPER_PROCESS=",
		tdnRoleEnv+"="+tdnRoleParent,
		tdnFreshEnv+"="+strconv.Itoa(fresh),
		tdnSameEnv+"="+strconv.Itoa(same),
		tdnReportEnv+"="+reportPath,
	)
	// Setpgid makes the parent its own group leader, so its pgid == its pid ==
	// the walk root: exactly the shape ReapDescendantGroups's rootPid exclusion
	// is written for, and what puts the same-group leaves into the spared group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start reap-tree parent (fresh=%d same=%d): %v", fresh, same, err)
	}
	rootPID := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	// Registered BEFORE the leaves' cleanups so LIFO kills the leaves first: a
	// parent killed first orphans them to init, where a fresh-group leaf outlives
	// the test until its own backstop. startReapHelper in
	// internal/agentrun/reap_test.go is the pattern.
	t.Cleanup(func() { tdnKillTree(rootPID) })

	tree := tdnWaitTreeReport(t, reportPath, fresh, same, 10*time.Second)
	tree.RootPID = rootPID
	for _, pid := range slices.Concat(tree.Fresh, tree.Same) {
		t.Cleanup(func() { tdnKillTree(pid) })
	}
	return tree
}

// tdnStartLeaf starts one childless blocking process in its own group and
// returns its pid.
//
// It is the walk root for the nothing-to-reap arm: rooted at a process with no
// children at all, descendantPGIDs enumerates nothing, so
// ReapDescendantGroups's len(reaped) > 0 guard suppresses the line entirely. A
// fresh childless root rather than a second reap of an existing tree, which
// would race the just-killed leaf's zombie window — ps lists zombies, so a
// not-yet-reaped corpse's pgid still enumerates and the arm would be
// timing-dependent.
func tdnStartLeaf(t *testing.T) int {
	t.Helper()
	pid, err := tdnSpawnLeaf(true)
	if err != nil {
		t.Fatalf("start childless reap-walk root: %v", err)
	}
	t.Cleanup(func() { tdnKillTree(pid) })
	return pid
}

// tdnKillTree SIGKILLs a process group and then the pid itself. Every kill in
// this file goes through it.
//
// The pid <= 1 guard is the load-bearing one: syscall.Kill(-0, …) is
// kill(0, …), which delivers to the CALLER's process group — the test binary,
// go test, and whatever else shares that group. A single zero, from a field that
// failed to parse into a variable left at its zero value, reaches it. The report
// parse guards the same value; this guards the kill site, which is where the
// damage is.
func tdnKillTree(pid int) {
	if pid <= 1 || pid == os.Getpid() || pid == syscall.Getpgrp() {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL) // the group it leads, if it leads one
	_ = syscall.Kill(pid, syscall.SIGKILL)  // the pid itself, for a same-group leaf
}

// tdnWaitTreeReport polls the report file until it holds exactly fresh+same
// well-formed "<kind> <pid>" lines, and fails the run if it never does.
//
// The parse is ALL-OR-NOTHING on purpose. Every pid in this file becomes a
// syscall.Kill target and one becomes a reap walk root, and a short read is
// syntactically valid but incomplete: accepting it yields a tdnTree with a
// missing Fresh entry — an index panic at best, a subtest silently asserting
// about the wrong process at worst. waitReport can treat a
// partial read as "not ready yet" because it reads exactly one pid; with N lines
// that is no longer a safe reading.
func tdnWaitTreeReport(t *testing.T, path string, fresh, same int, timeout time.Duration) tdnTree {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if tree, ok := tdnParseTreeReport(t, path, fresh, same); ok {
			return tree
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("reap-tree report %s did not hold %d well-formed <kind> <pid> line(s) "+
				"within %s", path, fresh+same, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tdnParseTreeReport reads one attempt at the report, returning ok only when
// every expected line is present and every pid is one this test could have
// owned.
//
// An incomplete file is not an error — the writer may still be running — so a
// missing trailing newline, a short line or an unparseable pid all mean "poll
// again". A pid this test must never signal is different in kind: no correct
// helper can report it, so it fails the run rather than the attempt.
func tdnParseTreeReport(t *testing.T, path string, fresh, same int) (tdnTree, bool) {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		return tdnTree{}, false
	}
	// The writer terminates every line, so a body that does not end in a newline
	// is still being written — and a truncated final line can otherwise parse
	// cleanly into a pid that is a prefix of the real one.
	if !strings.HasSuffix(string(blob), "\n") {
		return tdnTree{}, false
	}

	var tree tdnTree
	for _, line := range strings.Split(string(blob), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return tdnTree{}, false
		}
		pid, cerr := strconv.Atoi(fields[1])
		if cerr != nil {
			return tdnTree{}, false
		}
		if pid <= 1 {
			t.Fatalf("reap-tree report %s names pid %d, which no correct helper can report and "+
				"which as a kill target would deliver to this test's own process group", path, pid)
		}
		if pid == os.Getpid() || pid == syscall.Getpgrp() {
			t.Fatalf("reap-tree report %s names this test process (pid %d, pgid %d): refusing to "+
				"use it as a kill target or a reap walk root", path, os.Getpid(), syscall.Getpgrp())
		}
		switch fields[0] {
		case tdnReportKindFresh:
			tree.Fresh = append(tree.Fresh, pid)
		case tdnReportKindSame:
			tree.Same = append(tree.Same, pid)
		default:
			t.Fatalf("reap-tree report %s holds an unknown line kind %q", path, fields[0])
		}
	}
	if len(tree.Fresh) != fresh || len(tree.Same) != same {
		return tdnTree{}, false
	}
	return tree, true
}

// --- capturing the bytes a real reap emits ------------------------------------

// tdnCaptureReap runs a real ReapDescendantGroups rooted at rootPID and returns
// the bytes slog.Default() emitted while it ran.
//
// Why log.SetOutput captures it: runAgentRunStreamRunner sets no Logger on
// streamrunner.Config, so streamrunner.Run falls back to slog.Default() and no
// non-test code calls slog.SetDefault — so every `pyry agent-run`, which is
// what this package's probes spawn, renders ReapDescendantGroups's reap line
// through Go's BUILT-IN default handler, which writes through the log package.
// slog.Default() is therefore passed explicitly rather than a handler
// constructed here: constructing one would prove nothing about the live path.
//
// The rootPID guard is code rather than a comment because the blast radius is
// not proportionate to how unlikely the mistake is: a reap rooted at the test
// process sweeps every fresh-group descendant of the whole binary, and in this
// package sibling specs spawn real `pyry agent-run` and claude.
//
// A fresh buffer per call, so a silent arm can never inherit a positive arm's
// bytes; the redirect is restored unconditionally and is not reentrant (file
// header, § No t.Parallel).
func tdnCaptureReap(t *testing.T, rootPID int) []byte {
	t.Helper()
	if rootPID <= 1 || rootPID == os.Getpid() || rootPID == syscall.Getpgrp() {
		t.Fatalf("refusing to root a reap walk at pid %d: this test is pid %d in pgid %d, and a "+
			"walk rooted there sweeps every fresh-group descendant of the whole test binary",
			rootPID, os.Getpid(), syscall.Getpgrp())
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	agentrun.ReapDescendantGroups(rootPID, slog.Default())
	return append([]byte(nil), buf.Bytes()...)
}

// tdnAlive reports whether pid is alive. Signal 0 delivers nothing and fails
// with ESRCH once the process is gone. Signal-zero only: this file takes no ps
// read of its own (file header, § Redaction).
func tdnAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// tdnGroupGone reports whether process group pgid has emptied, polling up to
// timeout. kill(-pgid, 0) probes the whole group without delivering anything.
func tdnGroupGone(pgid int, timeout time.Duration) bool {
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

// tdnAliveThroughout fails the test unless pid stays alive for the whole window.
//
// Not a single kill(pid, 0): that succeeds on a ZOMBIE, so a spared leaf that
// had in fact been SIGKILLed could read as alive inside the window before its
// parent's background Wait reaps it. The reaper's syscall.Kill returns before
// ReapDescendantGroups does, so by capture time any kill has already been
// DELIVERED — a bounded settle in which the pid stays live is therefore
// sufficient, and it is asserted continuously across the window rather than at
// its two ends.
func tdnAliveThroughout(t *testing.T, pid int, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for {
		if !tdnAlive(pid) {
			t.Fatalf("spared process %d died within %s of the reap: its group's absence from the "+
				"reap line is evidence of the reap.go:52 exclusion only while the process is still "+
				"there to be excluded", pid, window)
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// --- the capture, driven end to end -------------------------------------------

// tdnSettleWindow is how long a spared process must stay alive after a reap for
// its absence from the line to be structural. Generous against a background Wait
// on a machine under load, against a kill already delivered before the capture
// returned.
const tdnSettleWindow = 250 * time.Millisecond

// tdnGroupGoneTimeout bounds the wait for a reaped group to empty.
const tdnGroupGoneTimeout = 2 * time.Second

// TestTdnRealReapCapture drives tdnClassifyReapLog with bytes captured from real
// reaps — no constant anywhere in the path — and asserts the capture FLIPS
// within the same harness.
//
// The flip is what makes the positive worth anything. A positive alone would
// read identically if the capture were wired to a stream the reaper never
// writes; the silent arm rules out its dual, a capture that carries the line
// unconditionally. Same helper, same capture path, same process, opposite
// answers.
//
// Subtests run sequentially and share the two byte slices, so the
// rendering-difference arm compares captures taken from two different real
// reaps. No t.Parallel (file header).
func TestTdnRealReapCapture(t *testing.T) {
	var (
		singleBytes, multiBytes []byte
		singleLine, multiLine   string
	)

	// AC1's positive half, AC2's single-group rendering, and all of AC3.
	t.Run("one group killed, a same-group sibling spared", func(t *testing.T) {
		tree := tdnStartTree(t, 1, 1)
		held, spared := tree.Fresh[0], tree.Same[0]

		captured := tdnCaptureReap(t, tree.RootPID)

		// First, and time-sensitive: the spared leaf is still there to be
		// excluded. Without it, reap-line-without-held-pgid and "that group had
		// already exited" are indistinguishable to a consumer — the unearned
		// negative this instrument exists to refuse.
		tdnAliveThroughout(t, spared, tdnSettleWindow)

		got := tdnClassifyReapLog(captured, held)
		if got.Verdict != tdnReapHeldPGIDKilled {
			t.Fatalf("verdict for the killed group (pgid %d): got %q (%s), want %q — the capture "+
				"carries %d bytes and %d anchored line(s)",
				held, got.Verdict, got.Detail, tdnReapHeldPGIDKilled, len(captured), got.LineCount)
		}
		if !tdnIsReapVerdict(got.Verdict) {
			t.Errorf("verdict %q is not one of the recorded values", got.Verdict)
		}
		if !tdnEqualInts(got.PGIDs, []int{held}) {
			t.Errorf("pgids: got %v, want %v — one group was reapable, so the line names exactly it",
				got.PGIDs, []int{held})
		}
		if got.LineCount != 1 || got.Count != 1 {
			t.Errorf("reap lines seen = %d, count attr = %d; want 1 and 1 (line: %s)",
				got.LineCount, got.Count, got.Line)
		}
		if !tdnGroupGone(held, tdnGroupGoneTimeout) {
			t.Errorf("group %d is still alive %s after the reap reported killing it — the line is "+
				"a claim about a SIGKILL, not the SIGKILL itself", held, tdnGroupGoneTimeout)
		}

		// AC3's other half. The spared leaf's pgid IS RootPID — it never called
		// setpgid and the parent leads its own group — so this verdict is about
		// exactly the group whose member was just proven alive.
		absent := tdnClassifyReapLog(captured, tree.RootPID)
		if absent.Verdict != tdnReapHeldPGIDAbsent {
			t.Fatalf("verdict for the spared group (pgid %d): got %q (%s), want %q",
				tree.RootPID, absent.Verdict, absent.Detail, tdnReapHeldPGIDAbsent)
		}
		if slices.Contains(absent.PGIDs, tree.RootPID) {
			t.Errorf("the reap line names the spared group %d among %v — reap.go:52 excludes "+
				"pgid == rootPid before it kills anything", tree.RootPID, absent.PGIDs)
		}

		// AC2's single-group rendering, on the bytes themselves. slog quotes the
		// value only once it contains a space, so one group renders unquoted.
		if !bytes.Contains(captured, fmt.Appendf(nil, "pgids=[%d]", held)) {
			t.Errorf("the capture does not carry the unquoted pgids=[%d] rendering (line: %s)",
				held, got.Line)
		}
		if bytes.Contains(captured, []byte(`pgids="[`)) {
			t.Errorf("the capture carries the QUOTED pgids=\"[ rendering for a single group "+
				"(line: %s)", got.Line)
		}

		singleBytes, singleLine = captured, got.Line
	})

	// AC1's silent half — and the half that proves the capture flips rather than
	// reporting the same answer whatever it is pointed at.
	t.Run("nothing to reap emits no line at all", func(t *testing.T) {
		root := tdnStartLeaf(t)

		captured := tdnCaptureReap(t, root)

		got := tdnClassifyReapLog(captured, root)
		if got.Verdict != tdnReapNoLine {
			t.Fatalf("verdict for a reap with nothing to kill: got %q (%s), want %q",
				got.Verdict, got.Detail, tdnReapNoLine)
		}
		if got.LineCount != 0 {
			t.Errorf("reap lines seen: got %d, want 0 (line: %s)", got.LineCount, got.Line)
		}
		if bytes.Contains(captured, []byte(tdnReapMessage)) {
			t.Errorf("a reap over a childless root emitted the reaped-groups line: %s", got.Line)
		}
		if !tdnAlive(root) {
			t.Errorf("the childless walk root %d was killed by a reap that had nothing to reap",
				root)
		}
	})

	// AC2's multi-group half. No existing fixture reaps more than one group, so
	// this tree shape is new.
	t.Run("several groups killed render as a quoted list", func(t *testing.T) {
		tree := tdnStartTree(t, 2, 0)

		captured := tdnCaptureReap(t, tree.RootPID)

		var first tdnReapOutcome
		for i, held := range tree.Fresh {
			got := tdnClassifyReapLog(captured, held)
			if got.Verdict != tdnReapHeldPGIDKilled {
				t.Fatalf("verdict for killed group %d of %d (pgid %d): got %q (%s), want %q",
					i+1, len(tree.Fresh), held, got.Verdict, got.Detail, tdnReapHeldPGIDKilled)
			}
			// Membership as a SET, never an ordered slice or an exact rendered
			// string: ReapDescendantGroups builds reaped by ranging a map, so
			// the order inside pgids=[A B] is not deterministic.
			if len(got.PGIDs) != len(tree.Fresh) {
				t.Errorf("pgids: got %v, want the %d reaped groups %v",
					got.PGIDs, len(tree.Fresh), tree.Fresh)
			}
			for _, want := range tree.Fresh {
				if !slices.Contains(got.PGIDs, want) {
					t.Errorf("pgid %d is missing from the reaped set %v", want, got.PGIDs)
				}
			}
			if got.LineCount != 1 || got.Count != len(tree.Fresh) {
				t.Errorf("reap lines seen = %d, count attr = %d; want 1 and %d (line: %s)",
					got.LineCount, got.Count, len(tree.Fresh), got.Line)
			}
			first = got
		}
		for _, held := range tree.Fresh {
			if !tdnGroupGone(held, tdnGroupGoneTimeout) {
				t.Errorf("group %d is still alive %s after the reap reported killing it",
					held, tdnGroupGoneTimeout)
			}
		}

		multiBytes, multiLine = captured, first.Line
	})

	// AC2's difference, which is itself the regression surface: a matcher proven
	// only on the single-group form inverts to a false negative on a multi-group
	// teardown, the ordinary case.
	t.Run("the one-group and many-group renderings differ", func(t *testing.T) {
		if len(singleBytes) == 0 || len(multiBytes) == 0 {
			t.Fatalf("nothing to compare (single: %d bytes, multi: %d bytes) — a preceding subtest "+
				"failed before it captured, and this arm must not pass vacuously",
				len(singleBytes), len(multiBytes))
		}
		if bytes.Contains(singleBytes, []byte(`pgids="[`)) {
			t.Errorf("the single-group capture carries the quoted rendering (line: %s)", singleLine)
		}
		if !bytes.Contains(multiBytes, []byte(`pgids="[`)) {
			t.Errorf("the multi-group capture does not carry the quoted pgids=\"[ rendering, so "+
				"the two renderings this instrument must both read are not distinguished by this "+
				"run (single: %s | multi: %s)", singleLine, multiLine)
		}
	})
}

// TestTdnClassifyReapLogSubstringMembership closes the untested direction of the
// substring hazard, and carries its already-covered twin so both halves of the
// hazard read together.
//
// Held 77 against pgids=[7788] is the PREFIX direction, already covered by
// TestTdnClassifyReapLog's "a held pgid that is a substring of a reaped one is
// not a member" case and duplicated here for six lines so neither half can be
// dropped without the other being findable. Held 88 is the SUFFIX direction and
// is the new coverage: it is a substring of the same rendered list in the other
// direction, and a substring matcher satisfies every other criterion in this
// file while inverting its answer here.
//
// The line is a literal rather than a capture because this arm is about
// membership arithmetic over a rendering, not about the rendering — that the
// real reaper emits exactly this shape is what TestTdnRealReapCapture proves.
func TestTdnClassifyReapLogSubstringMembership(t *testing.T) {
	const line = `2026/07/31 01:21:30 INFO ` +
		`agentrun: reaped claude descendant process groups count=1 pgids=[7788]`

	for _, tc := range []struct {
		name string
		held int
	}{
		{"a prefix of the reaped pgid is not a member", 77},
		{"a suffix of the reaped pgid is not a member", 88},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tdnClassifyReapLog([]byte(line), tc.held)

			if got.Verdict != tdnReapHeldPGIDAbsent {
				t.Fatalf("tdnClassifyReapLog(held=%d) = %q (%s); want %q — %d is a substring of "+
					"7788, and membership is decided over parsed integers",
					tc.held, got.Verdict, got.Detail, tdnReapHeldPGIDAbsent, tc.held)
			}
			if !tdnIsReapVerdict(got.Verdict) {
				t.Errorf("verdict %q is not one of the recorded values", got.Verdict)
			}
			if !tdnEqualInts(got.PGIDs, []int{7788}) {
				t.Errorf("pgids: got %v, want [7788] — the record shows what membership was "+
					"decided against, not merely the verdict", got.PGIDs)
			}
			if got.LineCount != 1 {
				t.Errorf("reap lines seen: got %d, want 1", got.LineCount)
			}
		})
	}
}
