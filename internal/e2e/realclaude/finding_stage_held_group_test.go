//go:build e2e_realclaude

package realclaude

// The parameterised run-outcome gather (#1281) FILLED FROM A REAL HELD COMMAND
// in a process group of its own, plus a red-on-regression trap on the two
// hardcodings that gather replaces.
//
// Everything here runs offline: a real FIFO, a real `sh`, a real `cat`, a
// synthetic stdout buffer and a synthetic reap line — no live claude, no
// credentials, no daemon, no turn, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinStage' -v ./internal/e2e/realclaude/
//
// # What this closes that #1281's rows structurally cannot
//
// finGatherReadings (finding_run_gather_test.go:298) takes pyry's reap-log
// stderr and the pinned process-group set as PARAMETERS, and proves that both
// the finding and a genuine negative come out of its own composition. Every row
// that proves it is driven from a synthetic stdout, a synthetic stderr and
// HAND-PASSED PGID INTEGERS. Hand-passed integers prove the composition; they do
// not prove the parameters can be FILLED. A live probe fills pinned from a real
// pinScanArgv (process_pin_liveness_test.go:191) over a real process table,
// taking each matched row's .PGID (reachProc, background_reach_probe_test.go:162)
// as the join key into pyry's reap log. This file is that conversion site, and
// the obligation finGatherReadings' own doc (:279-283) names as #1282's.
//
// The one reading that differs is MatchCount, and it moves the NEGATIVE arm one
// step earlier. #1281's rows all run at MatchCount == 0 — its negative needle is
// a t.TempDir() path nothing is ever staged at — so its negative falls to Step 8
// (trailOutcomeNoRowMatched). Here a real command carries the needle, so
// MatchCount is at least 2 and the same composition lands on Step 7
// (trailOutcomeMatchedUnattributed). That move is the increment, not an
// inconsistency with the blocker. The FINDING arm is unchanged, because Step 2
// (trail_run_outcome_test.go:612) outranks Step 7.
//
// # The staged command must lead a process group of its own
//
// The rig offers two subject stagings and only one is usable here. The flip
// test's subject (trail_run_rig_test.go:277) sets no SysProcAttr, so it inherits
// the test process's group — and the pinned group would then BE
// syscall.Getpgrp(), which is exactly what trailRigHeldPGID() (:119) returns. A
// synthetic stderr naming the pinned group would classify as
// tdnReapHeldPGIDKilled -> trailAdmitProof for the real arm AND for the trap's
// own-group arm, so the trap would go red asserting the opposite of its claim
// and #1268's hardcoding would look like it reaches the finding. The wrapper
// staging (:532-534) is the one copied here.
//
// The live shape agrees that a group of its own is what a real held command has:
// internal/agentrun/reap.go:52 skips `pgid <= 1 || pgid == self || pgid ==
// rootPid`, so a command sharing pyry's own group is one the reaper can NEVER
// report.
//
// # The distinctness guard reads the pgid THE SCAN REPORTS
//
// The guard exists so no arm below can be vacuous, and it is only worth writing
// if it can go red. cmd.Process.Pid != syscall.Getpgrp() CANNOT: a freshly
// forked child's pid is never the test process's group id, so that comparison
// holds whether or not Setpgid took effect, and dropping SysProcAttr — the exact
// regression the guard is for — would leave it silently green. The guard is
// therefore on the scanned .PGID, which is the same value the arms hand the
// gather; without Setpgid that value IS syscall.Getpgrp() and the guard reddens
// on the line that matters. cmd.Process.Pid stays the right operand for the
// Kill(-pgid) defer and is unfit only as the subject of the guard.
//
// # The trap asserts at the Admit layer, never at the outcome layer
//
// The word "void" names two different things one layer apart. Both of
// trailRigGather's hardcodings produce an Admit VOID: the nil stderr literal
// (trail_run_rig_test.go:159-171) reaches tdnReapNoLine -> trailAdmitVoidNoLine,
// and a pgid the reap log does not name reaches tdnReapHeldPGIDAbsent ->
// trailAdmitVoidGroupUnnamed. Neither produces a VOID OUTCOME: with a certifying
// gate, PyryExited true, a clean scan and MatchCount > 0, trailClassifyRun falls
// past Steps 3-6 to Step 7, an ANSWER in the three-answer set
// (trail_run_outcome_test.go:118-129). Reaching for one of the eight run-void-*
// outcomes to make the trap "assert a void" would require breaking a DIFFERENT
// input and would vary two dimensions at once.
//
// The trap is a test rather than a comment because the failure it guards has NO
// SYMPTOM IN THE ANSWER: a gather wired to the un-passable stderr still
// composes, still classifies and still returns a plausible outcome — a clean
// negative on every run, forever.
//
// # Liveness is filled for the first time, and Step 6 sits above Step 7
//
// readings.Liveness holds one pinReadState per MATCHED pid. In every prior fin*
// file the match set is empty, so Liveness is empty and Step 6
// (trailOutcomeVoidLivenessInstrument) is unreachable. Here it is non-empty for
// the first time, and Step 6 is consulted BEFORE Step 7 — so the finding arm is
// immune (Step 2 returns first) while every other arm would be diverted to a
// run-void-* by a single pinStateInstrumentFailed. finStageAssertLiveness
// therefore runs on every arm.
//
// # Failure messages
//
// A t.Fatalf here MAY name counts, pids, pgids, verdicts, outcome values, gate
// and admit values, BoundFrom, finAttribute* condition names, the reduced
// distinct-pgid slice, and the Detail of a trailRunOutcome, a trailAdmitResult
// or a pinStateOutcome. It MAY NEVER name pinScan.Matches or any reachProc
// (reachProc.Command is verbatim argv read off the ambient process table), a
// trailObservation's Line, a trailScanResult's trailer, or a pinExclusion's
// Command — the same rule trail_run_rig_test.go:49-57 states for the same
// reason. THE RULE STOPS BEING PRECAUTIONARY HERE: this is the first fin* file
// whose scan matches LIVE ROWS, so pinScan.Matches is non-empty for the first
// time in the family and .Command on those rows is verbatim argv off the
// operator's own process table.
//
// It may also never print a WHOLE trailRunReadings or a whole
// finAttributeRecord — %v or %+v on either value. Name scalar fields. This
// deliberately diverges from the neighbouring file, which licenses the whole
// struct (finding_run_gather_test.go:105-113) "and only because
// TestFinGatherReturnsNoCapturedBytes proves it": THAT PROOF DOES NOT COVER THIS
// FILE'S VALUES. Every row in the blocker's file runs at MatchCount == 0, so
// readings.Liveness is empty on every value it marshals, and this file is the
// first to fill it — introducing pinStateOutcome's three string fields (Detail,
// StateColumn, ToolStderr, process_pin_liveness_test.go:245-253) the proof never
// examined. Those fields are in fact safe, because pinStateArgs is
// `-p <pid> -o pid=,ppid=,stat=` with an enforcing never-add-`command`
// prohibition (:221-232) — but that is an argument from the SHIPPED COLUMN LIST
// and not from the cited test, which is exactly why the licence is not inherited
// wholesale. Naming scalars costs nothing here: every assertion in this file is
// about a .Value, a count or a pgid. Closing finGatherExemptKeys' pre-placed
// tool_stderr exemption against a FILLED Liveness needs a staged-subject row in
// the blocker's own no-captured-bytes test, which this ticket may not edit;
// filed as a follow-up.
//
// Enforced structurally rather than by discipline: finStageSubject carries []int
// and needle paths, so scan.Matches never escapes finStageHeldGroup, and inside
// it the only fields read off a row are .PGID and .PID. This file adds no ps
// read of its own, and must not: reachScanArgv's `ps -axww -o
// pid=,ppid=,pgid=,command=` (never -E/-Eww) and pinStateColumns are both
// inherited unchanged.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const (
	// finStageFIFOName is the FIFO's basename inside t.TempDir(). The full PATH
	// is the needle: unique per run, and no process's command line carries it
	// before the subject execs — including the test binary's own, whose argv is
	// `-test.run=...` and nothing more.
	finStageFIFOName = "fin-stage-held-subject"
	// finStageRendezvousWait bounds the wait for the staged `cat` to open the
	// FIFO's read end. Matches fifoLiveRendezvousWait.
	finStageRendezvousWait = 10 * time.Second
	// finStageShellCommand is the staged command, a COMPILE-TIME CONSTANT with no
	// interpolation site: both operands arrive as positional parameters, so no
	// shell metacharacter in either path is ever interpreted. The obvious
	// alternative — fmt.Sprintf("cat %s; exit 0", path) — is a shell-injection
	// shape even though today's only input is t.TempDir(). The `exit 0` after the
	// command is what defeats the exec optimisation: sh, dash, bash and zsh all
	// replace themselves with a SINGLE simple command, which would leave one
	// process and one matching row.
	finStageShellCommand = `"$1" "$2"; exit 0`
	// finStageTerminalReason is trailFixtureTrailer's own terminal reason, spelled
	// as this package spells it everywhere else. Pinned rather than merely checked
	// for non-emptiness so a seed edit that fired the budget arm reddens here
	// instead of quietly diverting Step 1 to trailOutcomeVoidBudgetFired.
	finStageTerminalReason = "completed"
)

// finStageSubject is one staged held command's readings, carried as INTEGERS and
// needle paths and never as pinScan.Matches: reachProc.Command is verbatim argv
// read off the ambient process table, and a ps column is how an operator's
// CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY reaches an artifact destined for
// a public issue. The type is the enforcement, in finAttributeFanOut's own
// []int shape (finding_attribution_fanout_test.go:195-202): nothing here is a
// value from which a command string is reachable.
type finStageSubject struct {
	// Needles is this run's argv needle set — one t.TempDir()-derived FIFO path.
	Needles []string
	// Pinned is each matched row's .PGID, in scan order, DUPLICATES INTACT.
	// len(Pinned) IS the staging scan's MatchCount by construction, one entry per
	// matched row, so no separate count field is needed. Keeping the duplicates
	// is what makes finAttributeFanOut's step 1 dedupe the thing under test
	// rather than a reduction this file performed first.
	Pinned []int
	// Group is the one distinct process group those rows share.
	Group int
	// Rows is pinScan.RowsScanned at staging time.
	Rows int
}

// finStageHeldGroup stages a command held un-finishable on a real FIFO in a
// process group of its own, pins that group off a real argv scan, asserts the
// group is distinct from the test process's own, and calls body with the result.
//
// # Why a callback and not a return value
//
// The SIGKILL(-pgid) must fire AFTER the arms have run and BEFORE the test
// function returns, and it must be a defer rather than a t.Cleanup (see the
// ordering note on the defer itself). A helper that RETURNED would fire its own
// defer at return, killing the subject before any arm ran. Passing the body in
// puts the defer in one place ahead of every arm, and makes "no arm runs before
// the guard" structural instead of a rule each test remembers.
func finStageHeldGroup(t *testing.T, body func(finStageSubject)) {
	t.Helper()

	fifoPath := filepath.Join(t.TempDir(), finStageFIFOName)
	needles := []string{fifoPath}

	// Creates the FIFO and holds its WRITE end for the whole test, so the
	// subject's block is attributable to the subject. The hold is also what makes
	// the scan-then-gather gap safe: the pinned set is read once here and every
	// arm re-scans the table, and between them the subject cannot exit.
	rendezvous := holdProbeFIFO(t, fifoPath)

	// Resolved in the PARENT, before the environment is scrubbed below.
	catPath, err := exec.LookPath(fifoLiveReaderCommand)
	if err != nil {
		t.Fatalf("resolve %s in the parent's PATH: %v", fifoLiveReaderCommand, err)
	}

	cmd := exec.Command("sh", "-c", finStageShellCommand, "sh", catPath, fifoPath)
	// The subject inherits nothing. Under `make e2e-realclaude` this process may
	// carry CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY, and exec.Command passes
	// the parent environment through by default. It costs nothing: `cat` needs no
	// environment, and both binaries are already resolved from the parent's PATH.
	cmd.Env = []string{}
	// The load-bearing line. Without it the scanned PGID is syscall.Getpgrp() and
	// the guard below reddens.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the shell wrapper on the staged FIFO: %v", err)
	}

	// A defer rather than a t.Cleanup, and the ordering against the FIFO release
	// is load-bearing: holdProbeFIFO registers its release as a t.Cleanup, which
	// runs AFTER the test function returns. This defer runs when body returns —
	// strictly INSIDE the test body and therefore strictly BEFORE that release —
	// so the group dies by SIGNAL, not by EOF, and the release then closes a
	// write end nothing is waiting on. A t.Cleanup registered here would instead
	// run LIFO before the release and block forever waiting for an EOF only that
	// later cleanup can deliver.
	//
	// THE SECOND KILL IS NOT REDUNDANT. Killing the GROUP is what reaches the
	// grandchild `cat` — cmd.Process.Kill() on the shell does not. But t.Fatalf
	// runs deferred functions, so this teardown also fires on the guard's own red
	// path, and on that path Setpgid was dropped: pgid names no group, the group
	// kill returns ESRCH, and cmd.Wait() would block forever on a live `sh`
	// waiting on a live `cat` waiting for an EOF only holdProbeFIFO's t.Cleanup
	// can send — which cannot run until this goroutine finishes its Goexit. The
	// direct kill is what makes the guard fail in seconds instead of hanging
	// until the `go test` timeout; a guard that hangs instead of failing corrupts
	// the verification it exists for. The orphaned `cat` then takes its EOF from
	// the later cleanup, outliving the test body by microseconds on a path that
	// is already failing.
	pgid := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	select {
	case <-rendezvous:
		// Fires when the `cat` opens the read end, which is also what guarantees
		// it has execed and carries the needle in its argv.
	case <-time.After(finStageRendezvousWait):
		t.Fatalf("the rendezvous never fired within %s although the shell wrapper was started; "+
			"the subject is not known to be blocked on the FIFO, so no reading taken here would "+
			"be about a live subject", finStageRendezvousWait)
	}

	// The rendezvous proves a reader ARRIVED; this proves one is STILL holding
	// the read end at scan time.
	if live := fifoLiveRead(fifoPath); live.Verdict != fifoLiveReaderPresent {
		t.Fatalf("the FIFO reads %q at scan time; want %q — the subject is not holding the read "+
			"end, so the pin taken below would not be about a live subject",
			live.Verdict, fifoLiveReaderPresent)
	}

	scan, err := pinScanArgv(needles, nil)
	if err != nil {
		// The error is safe to render and the reason is not obvious: it wraps
		// reachScanArgv's, whose ps runs through exec.CommandContext(...).Output().
		// That discards the table on error and stores ps's own stderr on
		// *exec.ExitError.Stderr, which ExitError.Error() does NOT render — so %v
		// prints the argument list and an exit status, and no scanned row.
		t.Fatalf("the staging argv scan errored as an instrument: %v. There is no match set to "+
			"pin a group off, so every arm below would be driven from a group this test did not "+
			"stage", err)
	}
	if scan.RowsScanned <= 0 {
		t.Fatalf("the staging argv scan parsed %d well-formed row(s); want more than 0 — with "+
			"none, nothing was measured and the pin below would be an absence dressed as a "+
			"reading", scan.RowsScanned)
	}
	// AC4's first half, and the premise for the wrapper/child pair: the shell
	// forks rather than exec-replacing itself, so BOTH it and the `cat` carry the
	// needle in their argv and both sit in the one Setpgid group.
	if scan.MatchCount <= 1 {
		t.Fatalf("the staging argv scan matched %d of %d row(s) with a shell wrapper and its "+
			"forked child both carrying the needle; want more than 1 — with a single row the "+
			"count of groups and the count of rows are indistinguishable and the fan-out's dedupe "+
			"is never exercised", scan.MatchCount, scan.RowsScanned)
	}

	// The conversion this ticket exists to perform: .PGID and nothing else. The
	// rows themselves stop here.
	pinned := make([]int, 0, scan.MatchCount)
	for _, match := range scan.Matches {
		pinned = append(pinned, match.PGID)
	}

	seen := make(map[int]bool, len(pinned))
	distinct := make([]int, 0, len(pinned))
	for _, group := range pinned {
		if seen[group] {
			continue
		}
		seen[group] = true
		distinct = append(distinct, group)
	}
	// The matched rows must share ONE group, and this check has a second job: it
	// is the fail-closed gate against a THIRD-PARTY row carrying the needle. Any
	// local process can put arbitrary bytes in its own argv, so the scan reads an
	// ambient, untrusted table; an unrelated row matching the needle would land
	// in a different group, and the staging fails rather than pinning a group it
	// did not stage. The message names the REDUCED slice and never the rows.
	if len(distinct) != 1 {
		t.Fatalf("the %d matched row(s) reduce to %d distinct process group(s) (%v); want exactly "+
			"1 — the staged wrapper and its child share one group, so a second group means a row "+
			"this test did not stage carries the needle and the pin would be about that row",
			scan.MatchCount, len(distinct), distinct)
	}
	// AC1's guard. The subject of the comparison is the whole point: this is the
	// SCANNED pgid, the same value the arms hand the gather, so dropping Setpgid
	// reddens here. cmd.Process.Pid would be green under that same regression.
	if distinct[0] == syscall.Getpgrp() {
		t.Fatalf("the scan reports the staged rows in process group %d, which is this test "+
			"process's own group; want a group of their own — a command sharing the test's group "+
			"is one internal/agentrun/reap.go:52 can never report, and keying the attribution on "+
			"it would make %q reachable from the very hardcoding this file traps",
			distinct[0], trailAdmitProof)
	}

	body(finStageSubject{
		Needles: needles,
		Pinned:  pinned,
		Group:   distinct[0],
		Rows:    scan.RowsScanned,
	})
}

// finStageReapLine renders the synthetic reap log naming exactly one group, in
// reap.go:65's slog shape via the shipped trailReapLine.
//
// The count is 1 because trailAdmitAttribution answers a named group across more
// than one anchored line with trailAdmitVoidNotOneReapLine
// (trailer_admissibility_test.go:595-602) rather than with trailAdmitProof.
func finStageReapLine(pgid int) []byte {
	return []byte(trailReapLine(1, fmt.Sprintf("[%d]", pgid)) + "\n")
}

// finStageAssertLiveness pins the per-matched-pid reads Step 6 makes
// load-bearing, mirroring trail_run_rig_test.go:572-586.
//
// It runs on EVERY arm. Step 6 (trailOutcomeVoidLivenessInstrument) is consulted
// before Step 7, so a single pinStateInstrumentFailed diverts every arm except
// the finding to a run-void-* — precisely what AC2 forbids. The finding arm is
// immune because Step 2 returns on trailAdmitProof first, which is exactly why
// the check is not left to the arms that would happen to notice.
func finStageAssertLiveness(t *testing.T, readings trailRunReadings) {
	t.Helper()

	// The classifier deliberately has no alignment check between len(Liveness)
	// and MatchCount — it never indexes one against the other — so the alignment
	// is pinned here.
	if len(readings.Liveness) != readings.MatchCount {
		t.Fatalf("the readings carry %d per-pid read(s) for %d matched row(s); want one read per "+
			"matched pid", len(readings.Liveness), readings.MatchCount)
	}
	for _, read := range readings.Liveness {
		if !pinIsVerdict(read.Verdict) {
			t.Fatalf("the per-pid read for pid %d carries verdict %q, which is not one of the four "+
				"pinReadState documents", read.PID, read.Verdict)
		}
		if read.Verdict == pinStateInstrumentFailed {
			t.Fatalf("the per-pid read for pid %d failed as an instrument: %s. Every matched pid is "+
				"a live process this test staged, so an instrument failure here means the READ DID "+
				"NOT RUN rather than that the process was gone — and Step 6 sits above Step 7, so "+
				"the alternative reading turns a broken instrument into a %s that looks like a "+
				"measured answer", read.PID, read.Detail, trailOutcomeVoidLivenessInstrument)
		}
	}
}

// finStageRun drives one arm through the shipped gather and the shipped
// classifier, after the checks every arm shares.
//
// Every arm goes through here so that an arm differs from its control in
// EXACTLY the argument it varies — a negative fixture differing in more than the
// intended dimension asserts its own construction. Nothing here hand-builds a
// trailRunReadings: the classifier is fed finGatherReadings' own return value.
//
// The shared checks are what keep every arm off a void it did not intend. The
// gate check closes Step 1; MatchCount > 1 closes Steps 4 and 5, because
// pinScanArgv returns the ZERO pinScan on error and a matched row is a scanned
// row; finStageAssertLiveness closes Step 6; and this function PASSES
// PyryExited true, which closes Step 3. What is left is Step 2 against Step 7,
// which is the dimension every arm below actually varies.
func finStageRun(t *testing.T, subject finStageSubject, stderr []byte,
	pinned []int) (trailRunReadings, finAttributeRecord, trailRunOutcome) {
	t.Helper()

	// A FRESH buffer per arm, seeded before the gather so the trailer poll's
	// first iteration hits and no arm waits out finGatherTrailerWait.
	// trailFixtureTrailer is the right seed precisely because it is ORDINARY: a
	// budget-fired terminal reason would divert Step 1 to
	// trailOutcomeVoidBudgetFired.
	var stdout probeSyncBuffer
	if _, err := stdout.Write([]byte(trailFixtureTrailer + "\n")); err != nil {
		t.Fatalf("seed the arm's stdout buffer with the trailer fixture: %v", err)
	}

	readings, record, _ := finGatherReadings(finGatherInputs{
		Stdout:  &stdout,
		Needles: subject.Needles,
		Stderr:  stderr,
		Pinned:  pinned,
		// #1302 promoted this reading out of the gather's body; the original
		// justification is unchanged and now lives at the call site that makes
		// the claim. No pyry runs in this file, so there is none to fail to
		// exit. finStageRun does not take it as a parameter: all five of its
		// callers want this value, and threading one through them would be
		// noise.
		PyryExited: true,
		// ClaudeState omitted for the same reason one field over: no claude runs
		// here, and "" is C7's shipped "not read".
	})

	if readings.Gate.Value != trailGateUsable || readings.Gate.Reason != finStageTerminalReason {
		t.Fatalf("the gate reads %q certifying reason %q; want %q certifying %q — without a usable "+
			"trailer there is no certified instant for this arm to be about, and Step 1 answers it "+
			"before the attribution is consulted at all", readings.Gate.Value, readings.Gate.Reason,
			trailGateUsable, finStageTerminalReason)
	}
	if readings.MatchCount <= 1 {
		t.Fatalf("the arm's argv scan matched %d of %d row(s) against %d row(s) scanned at staging "+
			"time, while the subject is held on the FIFO; want more than 1 — below that the arm "+
			"falls to Step 8 or a void and stops being about the staged group at all",
			readings.MatchCount, readings.RowsScanned, subject.Rows)
	}
	finStageAssertLiveness(t, readings)

	return readings, record, trailClassifyRun(readings)
}

// TestFinStageRealHeldGroupFillsTheGather is this file's actual claim: the
// gather's two parameters, filled from a real held command in a group of its
// own, reach the finding — and reach an honest answer when the reap log names a
// different group.
//
// The two arms differ in ONE dimension, the group the synthetic reap line names.
// Everything else — the staging, the needles, the pinned set, the seed — is byte
// for byte identical.
func TestFinStageRealHeldGroupFillsTheGather(t *testing.T) {
	finStageHeldGroup(t, func(subject finStageSubject) {
		// --- the reap log names the real staged group ---

		readings, record, outcome := finStageRun(t, subject,
			finStageReapLine(subject.Group), subject.Pinned)

		if readings.Admit.Value != trailAdmitProof {
			t.Fatalf("the attribution over the real staged group %d reads %q; want %q — pyry's reap "+
				"log names that group on exactly one anchored line, which is what the proof rests "+
				"on: %s", subject.Group, readings.Admit.Value, trailAdmitProof, readings.Admit.Detail)
		}
		if outcome.Value != trailOutcomeRunningAtTrailer {
			t.Fatalf("the readings gathered over a real held command classify as %q (%s); want %q — "+
				"the pinned set came off a real scan and the reap line names its group, so the one "+
				"outcome that is a finding must be reachable from readings a live probe can "+
				"actually fill", outcome.Value, outcome.Detail, trailOutcomeRunningAtTrailer)
		}

		// AC4: the count of groups is not the count of rows, proven against a real
		// process table rather than a hand-passed slice.
		if len(subject.Pinned) <= 1 {
			t.Fatalf("the staging pinned %d process group id(s) off its matched rows; want more "+
				"than 1 — with one row there is no plurality for the fan-out's dedupe to reduce",
				len(subject.Pinned))
		}
		if len(record.Entries) != 1 {
			t.Fatalf("the fan-out over %d pinned pgid(s) produced %d attribution entr(ies); want "+
				"exactly 1 — the matched rows share one group, and the count of entries is the "+
				"count of DISTINCT groups and never the count of rows",
				len(subject.Pinned), len(record.Entries))
		}
		if record.Entries[0].PGID != subject.Group {
			t.Fatalf("the one attribution entry is for group %d; want %d — the entry must be for "+
				"the group the scan actually reported for the staged rows",
				record.Entries[0].PGID, subject.Group)
		}

		// --- the reap log names a different group ---

		// subject.Group + 1 rather than a typed constant: a literal could collide
		// with the real staged group on a busy machine and silently turn this into a
		// second finding arm. Group + 1 is unequal to Group by construction and is
		// at least 3, so finAttributeFanOut's step 2 does not mark it unreportable
		// and tdnClassifyReapLog's heldPGID <= 1 guard does not fire. The value
		// never leaves the synthetic stderr; nothing looks it up.
		unnamed, _, unnamedOutcome := finStageRun(t, subject,
			finStageReapLine(subject.Group+1), subject.Pinned)

		if unnamed.Admit.Value != trailAdmitVoidGroupUnnamed {
			t.Fatalf("the attribution over a reap log naming a group other than the pinned %d reads "+
				"%q; want %q — a group not named is never evidence it had exited: %s",
				subject.Group, unnamed.Admit.Value, trailAdmitVoidGroupUnnamed, unnamed.Admit.Detail)
		}
		// An ANSWER, never a run-void-*. The negative arm lands one step earlier
		// than #1281's, which falls to trailOutcomeNoRowMatched because its needle
		// matches nothing; here a real command carries the needle, so MatchCount is
		// above 0 and Step 7 answers first.
		if unnamedOutcome.Value != trailOutcomeMatchedUnattributed {
			t.Fatalf("the negative arm classifies as %q (%s); want %q — a match with a silent "+
				"attribution is the honest answer, and any run-void-* here would mean this arm "+
				"varied a second dimension and is asserting its own construction",
				unnamedOutcome.Value, unnamedOutcome.Detail, trailOutcomeMatchedUnattributed)
		}
		if outcome.Value == unnamedOutcome.Value {
			t.Fatalf("both arms classify as %q although only one reap log names the pinned group; "+
				"the outcome does not move with the one dimension they vary, so neither arm is "+
				"demonstrably about the reap line it was handed", outcome.Value)
		}
	})
}

// TestFinStageRigHardcodingsCannotReachTheFinding pins that #1268's two
// hardcodings cannot reach the finding, ASSERTED ON THE Admit VALUE.
//
// Each trap arm's control is the finding arm, re-run in this test's own staging
// and asserted FIRST: without it a staging that could not reach the finding at
// all would make both traps pass while proving nothing. The nil-stderr arm
// varies only the stderr; the own-group arm varies only the pinned pgid, keeping
// the control's group-naming stderr byte for byte — it is the same expression.
//
// THE TWO TRAP ARMS ARE NEVER EACH OTHER'S CONTROL. They differ in two
// dimensions, and a run varying both would assert its own construction.
//
// The assertion is at the Admit layer because the outcome layer cannot carry the
// claim: both hardcodings produce an Admit void and NEITHER produces a void
// outcome — under a certifying gate with a clean scan and MatchCount > 0 both
// fall past Steps 3-6 to Step 7, an answer. So each arm asserts its Admit value
// and, as the claim itself, that the finding is not reached.
func TestFinStageRigHardcodingsCannotReachTheFinding(t *testing.T) {
	finStageHeldGroup(t, func(subject finStageSubject) {
		// The one stderr expression, shared by the control and the own-group arm.
		namesTheGroup := finStageReapLine(subject.Group)

		// --- the control: AC2's finding arm, in this test's own staging ---

		control, _, controlOutcome := finStageRun(t, subject, namesTheGroup, subject.Pinned)

		if control.Admit.Value != trailAdmitProof || controlOutcome.Value != trailOutcomeRunningAtTrailer {
			t.Fatalf("the control arm reads attribution %q and classifies as %q (%s); want %q and "+
				"%q — the two traps below are only meaningful against a staging that DOES reach the "+
				"finding, and without that a broken staging would make both of them pass while "+
				"proving nothing", control.Admit.Value, controlOutcome.Value, controlOutcome.Detail,
				trailAdmitProof, trailOutcomeRunningAtTrailer)
		}

		// --- #1268's nil stderr, varying the stderr and nothing else ---

		noLine, _, noLineOutcome := finStageRun(t, subject, nil, subject.Pinned)

		if noLine.Admit.Value != trailAdmitVoidNoLine {
			t.Fatalf("the attribution over trailRigGather's nil stderr literal "+
				"(trail_run_rig_test.go:159-171) reads %q; want %q — tdnClassifyReapLog over nil "+
				"can only reach tdnReapNoLine, and a gather wired to that un-passable input reports "+
				"a clean negative on every run, forever, with no symptom in the answer: %s",
				noLine.Admit.Value, trailAdmitVoidNoLine, noLine.Admit.Detail)
		}
		if noLineOutcome.Value == trailOutcomeRunningAtTrailer {
			t.Fatalf("the nil-stderr arm classifies as %q although its control is the only arm "+
				"entitled to that outcome; the hardcoded stderr reaches the finding and the trap's "+
				"whole claim is void", noLineOutcome.Value)
		}
		// NOT the claim — the guard that keeps the claim from being satisfiable by
		// a void. Measured rather than assumed: with finStageAssertLiveness removed
		// and one pinStateInstrumentFailed forced into Liveness, Step 6 diverts this
		// arm to a run-void-* which satisfies BOTH assertions above, and the trap
		// passes while publishing a void it never intended. A second fabric for the
		// same failure, and deterministic where the liveness helper is a read.
		if noLineOutcome.Value != trailOutcomeMatchedUnattributed {
			t.Fatalf("the nil-stderr arm classifies as %q (%s); want %q — the arm varies only the "+
				"stderr, so it must fall past Steps 3-6 to Step 7 and land on an ANSWER. Any "+
				"run-void-* here means a second dimension moved and the trap is asserting its own "+
				"construction", noLineOutcome.Value, noLineOutcome.Detail,
				trailOutcomeMatchedUnattributed)
		}

		// --- #1268's own-group key, varying the pinned pgid and nothing else ---

		// syscall.Getpgrp() is what trailRigHeldPGID() returns
		// (trail_run_rig_test.go:119). It is always above 1, so tdnClassifyReapLog's
		// heldPGID <= 1 guard does not fire and finAttributeFanOut's step 2 does not
		// mark it unreportable: it reaches tdnReapHeldPGIDAbsent honestly, and
		// trailAdmitVoidInstrument would be a different claim.
		ownGroup, _, ownGroupOutcome := finStageRun(t, subject, namesTheGroup,
			[]int{syscall.Getpgrp()})

		if ownGroup.Admit.Value != trailAdmitVoidGroupUnnamed {
			t.Fatalf("the attribution keyed on the test process's own group %d — what "+
				"trailRigHeldPGID() returns — reads %q; want %q against a reap log that names the "+
				"staged group %d: %s", syscall.Getpgrp(), ownGroup.Admit.Value,
				trailAdmitVoidGroupUnnamed, subject.Group, ownGroup.Admit.Detail)
		}
		if ownGroupOutcome.Value == trailOutcomeRunningAtTrailer {
			t.Fatalf("the own-group arm classifies as %q over the control's own stderr; the "+
				"hardcoded group reaches the finding, which would mean the staged command does not "+
				"lead a group of its own after all", ownGroupOutcome.Value)
		}
		// The same anti-vacuity guard as the nil-stderr arm, for the same measured
		// reason.
		if ownGroupOutcome.Value != trailOutcomeMatchedUnattributed {
			t.Fatalf("the own-group arm classifies as %q (%s); want %q — the arm varies only the "+
				"pinned pgid, so it must fall past Steps 3-6 to Step 7 and land on an ANSWER. Any "+
				"run-void-* here means a second dimension moved and the trap is asserting its own "+
				"construction", ownGroupOutcome.Value, ownGroupOutcome.Detail,
				trailOutcomeMatchedUnattributed)
		}
	})
}
