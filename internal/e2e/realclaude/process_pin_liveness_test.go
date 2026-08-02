//go:build e2e_realclaude

package realclaude

// The process-identification and liveness instrument the exit-path probes
// (#1236, then #1237) depend on, plus the offline self-checks that prove it.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence. Everything here runs offline: no live
// claude, no credentials, no daemon, no env gate, no t.Skip.
//
//	go test -tags e2e_realclaude -run '^TestPin' -v ./internal/e2e/realclaude/
//
// # The three defects it closes
//
//  1. IDENTIFICATION BY POSITION AND LEAF NAME. probeAnnotateCommands (:930)
//     stores filepath.Base(fields[1]), so a held `cat <fifo>` records as `cat`
//     and matches any unrelated `cat` on the machine. #1230 already solved this
//     with reachMatchArgvRows' full-argv content match, so that matcher is
//     CALLED here, never rebuilt. What is genuinely missing is the caller's
//     exclusion set with recorded reasons — see pinPartition.
//
//  2. THE ONLY PROCESS VIEW IS A SUBTREE WALK ROOTED AT PYRY. On one of the two
//     staged paths claude exits before the observation window and the detached
//     Bash group re-parents to init, so probeDescendantsFromPS (:891) reports a
//     live, FIFO-held command as ABSENT — indistinguishable from the probes'
//     negative verdict. pinReadState is a direct `ps -p <pid>` lookup: no root,
//     no walk, no descendant requirement.
//
//  3. A STATE READ THAT IS NOT FOUR-VALUED COLLAPSES TWO DIFFERENT FINDINGS.
//     `ps` lists a SIGKILLed-but-unreaped process as a row (measured in #1224),
//     so "alive" and "exited but not yet reaped" are separate values; and a
//     half-run instrument must never publish an absence. See § The trap.
//
// # The trap: exit status alone cannot separate "gone" from "broken"
//
// Measured on this machine (darwin 25.5, 2026-07-30) — all three arrive as
// *exec.ExitError:
//
//	invocation                       exit  stdout                stderr
//	ps -p <dead> -o pid=,ppid=,stat=    1  (empty)               (empty)
//	ps -p 1 -o nosuchcol=               1  the KEYWORD LIST      ps: nosuchcol: keyword not found
//	ps -Z9q (illegal option)            1  (empty)               ps: illegal option -- Z + usage
//
// Only stderr separates "the pid is gone" from "this instrument is broken", and
// the middle row is the trap: a bad column name prints `%cpu %mem acflag …` on
// STDOUT, so code that parses whatever stdout it got alongside an error reads
// the valid-keyword list as process rows. pinClassifyState therefore checks
// stderr AND stdout before the no-such-process arm is reachable at all.
//
// A fourth signature was measured during implementation and is NOT in the
// ticket's table: a ps killed by its own CommandContext timeout surfaces as an
// *exec.ExitError carrying `signal: killed`, ExitCode() == -1, empty stdout and
// empty stderr — byte-identical to the dead-pid signature on every field the
// table names. A loaded machine is the expected condition for this family
// (#1230's round-1 MUST FIX turned on exactly that), so the sign of the exit
// status is load-bearing: a normal exit is the only thing allowed to reach
// no-such-process. See pinClassifyState's branch 3.
//
// # Redaction — inherited from #1230's ruleset, and one rule strengthened
//
// background_reach_probe_test.go:57-92 governs this family. Rule 1 binds the
// new per-pid lookup too: NEVER `ps -E`, `ps -e` with an environment column, or
// the BSD-syntax `ps eww`. Those print each process's full ENVIRONMENT, which
// here means the operator's CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY, into
// an artifact destined for a public issue. This instrument needs no environment
// read at all, so the safe design is to NOT HAVE THE CAPABILITY: the whole
// column set is pinStateColumns, three kernel-generated integers-and-flags, and
// TestPinStateColumns_ReadsNoEnvironment is the deterministic tripwire that
// fires the moment someone widens it to "improve the record".
//
// Rule 3 is STRENGTHENED here. #1230 relied on needles that HAPPEN not to
// collide with the instrument's own processes; pinPartition withholds
// instrument-owned pids explicitly and records the reason, so a withheld row is
// visible in the record rather than absent from it.
//
// # What is deliberately NOT touched
//
// probeProcessSnapshot (:867) and probeDescendantsFromPS (:891) are unchanged,
// and the per-pid read uses its own narrow `ps -p` rather than widening their
// snapshot. probeDescendantsFromPS skips any line where len(fields) != 3
// (:896-898), so adding a state column to `ps -axo pid=,ppid=,pgid=` would make
// EVERY line fail that guard and the walk return empty — reading as "the
// command is absent", the exact false negative this instrument exists to
// prevent. reachMatchArgvRows and TestReachMatchArgvRows are untouched too:
// exclusion is a strictly downstream filter, so the existing caller's behaviour
// is unchanged by construction rather than by promise.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- the exclusion-aware argv scan -------------------------------------------

// pinExclusionReasonUnstated stands in when a caller supplies an empty reason.
// A blank reason in a published record is the same defect as an unexplained
// verdict: a reader can see that something was withheld but not why, which is
// indistinguishable from a lost finding.
const pinExclusionReasonUnstated = "(caller gave no reason)"

// pinExclusion is one row the caller asked to be kept out of the matches. It
// carries the reason AND the row's own command and needles, so the record shows
// the withheld row was genuinely the instrument's own rather than a finding that
// quietly went missing.
type pinExclusion struct {
	PID     int      `json:"pid"`
	Reason  string   `json:"reason"`
	Command string   `json:"command,omitempty"`
	Needles []string `json:"matched_needles,omitempty"`
}

// pinScan is the whole outcome of one content-first scan.
//
// Matches is a SLICE and MatchCount an int: nothing here ever resolves to "the"
// pid, so a run that matched more than one row is visible as such in the record
// rather than silently resolved to the first — the defect
// probeWaitForBashToolUse (:762) still carries.
//
// RowsScanned is reachMatchArgvRows' total, carried through unaltered. It is
// what separates "the scan ran and nothing matched" from "the scan parsed no
// well-formed rows at all"; an empty Matches says neither.
type pinScan struct {
	Matches     []reachProc    `json:"matches"`
	Exclusions  []pinExclusion `json:"exclusions,omitempty"`
	RowsScanned int            `json:"rows_scanned"`
	MatchCount  int            `json:"match_count"`
}

// pinPartition splits already-matched rows by the caller's exclusion set.
//
// It is a PURE post-filter over reachMatchArgvRows' output: it parses nothing,
// execs nothing and matches nothing. That is the design, not an accident.
// reachMatchArgvRows is this package's one full-argv matcher and #1235 must not
// grow a second, so exclusion is applied strictly downstream of it — which is
// also why the existing caller's behaviour is unchanged by construction rather
// than by promise.
//
// A pid in exclude that matched nothing produces no entry: an exclusion is
// recorded only when it actually fired, so the record never claims a
// withholding that never happened.
func pinPartition(matches []reachProc, total int, exclude map[int]string) pinScan {
	out := pinScan{RowsScanned: total}
	for _, m := range matches {
		reason, excluded := exclude[m.PID]
		if !excluded {
			out.Matches = append(out.Matches, m)
			continue
		}
		if strings.TrimSpace(reason) == "" {
			reason = pinExclusionReasonUnstated
		}
		out.Exclusions = append(out.Exclusions, pinExclusion{
			PID:     m.PID,
			Reason:  reason,
			Command: m.Command,
			Needles: m.Needles,
		})
	}
	out.MatchCount = len(out.Matches)
	return out
}

// pinMatchArgvExcluding is the byte-pure surface: #1230's matcher, then the
// exclusion partition. This is the whole of the new matching logic.
func pinMatchArgvExcluding(table []byte, needles []string, exclude map[int]string) pinScan {
	matches, total := reachMatchArgvRows(table, needles)
	return pinPartition(matches, total, exclude)
}

// pinScanArgv is the live wrapper: reachScanArgv for the exec — the same
// `ps -axww -o pid=,ppid=,pgid=,command=` read, with the same -ww and no-`-E`
// disciplines — then the exclusion partition.
//
// It returns the ZERO pinScan on error, mirroring reachScanArgv's
// discard-on-error contract (probeProcessSnapshot returns partial output
// alongside its error; these two ps call sites genuinely warrant different
// error gates — #1230 round-1 MUST FIX). A consumer's gate belongs AFTER its
// match outcome is decided, so a failed lookup neither relabels a genuine match
// nor suppresses a genuine "scan fired, no row matched"; this file ships no
// consumer, so the obligation is discharged by keeping the two reads
// independent — this error and pinStateOutcome's verdict are separate values
// with no cross-assignment.
func pinScanArgv(needles []string, exclude map[int]string) (pinScan, error) {
	matches, total, err := reachScanArgv(needles)
	if err != nil {
		return pinScan{}, fmt.Errorf("pin argv scan: %w", err)
	}
	return pinPartition(matches, total, exclude), nil
}

// --- the four-valued per-pid state read --------------------------------------

// The four values. Mutually exclusive, and never collapsed: the last two are
// the pair this family has twice collapsed at review time, and the first two
// are the pair `ps` itself collapses by listing zombies as rows.
const (
	// pinStateRunning: a row was read and its state column does not mark a
	// zombie.
	pinStateRunning = "running"
	// pinStateExitedNotReaped: a row was read and its state column marks a
	// zombie. `ps` LISTS a SIGKILLed-but-unreaped process (measured in #1224),
	// so without this value every zombie reads as running.
	pinStateExitedNotReaped = "exited-but-not-yet-reaped"
	// pinStateNoSuchProcess: exit 1 with empty stdout AND empty stderr. This is
	// the ONLY input in the whole instrument that produces this verdict.
	pinStateNoSuchProcess = "no-such-process"
	// pinStateInstrumentFailed: the read could not be taken. Never a statement
	// about the process — a half-run instrument publishing an absence is the
	// measured defect this value exists to prevent (#1230 PR #1232, MUST FIX).
	pinStateInstrumentFailed = "instrument-failed"
)

// pinStateColumns is the ENTIRE column set of the per-pid lookup, named as a
// constant so TestPinStateColumns_ReadsNoEnvironment can assert on the thing a
// future edit changes.
//
// NEVER add `command`, `args`, `comm`, or any environment column, and never
// reach this ps through `-E`, `-e` with an environment column, or the
// BSD-syntax `eww`. Those print each process's full ENVIRONMENT, which here
// means the operator's CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY, into an
// artifact destined for a public issue. This read needs no environment and no
// argv at all — pinScanArgv already owns the content match — so the safe design
// is to not have the capability (file header, § Redaction).
const pinStateColumns = "pid=,ppid=,stat="

// pinExitStatusUnknown marks an outcome whose ps never exited normally, or ran
// at all. Distinct from 0, which is a real successful exit.
const pinExitStatusUnknown = -1

// pinStateOutcome is one per-pid read. Consumers record it verbatim into
// published evidence, so every field is self-describing: Detail says which arm
// fired and why, and StateColumn carries the classifier's own input so a reader
// of the evidence sees what was classified, not merely the verdict.
//
// PID is the pid the read was ABOUT — the operand handed to ps — not a value
// parsed back out of its output.
type pinStateOutcome struct {
	Verdict     string `json:"verdict"`
	Detail      string `json:"detail"`
	PID         int    `json:"pid"`
	PPID        int    `json:"ppid,omitempty"`
	StateColumn string `json:"state_column,omitempty"`
	ExitStatus  int    `json:"exit_status"`
	ToolStderr  string `json:"tool_stderr,omitempty"`
}

// pinStateArgs is the exact argument list of the per-pid lookup. A function
// rather than a literal at the call site so the redaction tripwire asserts on
// the real list rather than on a copy of it.
//
// `-p <pid>` and nothing wider: the re-check is a lookup for ONE known pid and
// is not a reason to read, let alone persist, a second full process table.
func pinStateArgs(pid int) []string {
	return []string{"-p", strconv.Itoa(pid), "-o", pinStateColumns}
}

// pinReadState reads the state of one pid.
//
// It is a DIRECT lookup: no root, no walk, no requirement that the pid still be
// a descendant of anything. That is the whole point — probeDescendantsFromPS
// (:891) is a subtree walk rooted at pyry's pid, so a command whose group has
// re-parented to init reads as ABSENT there, which is indistinguishable from the
// consuming probes' negative verdict.
//
// It takes no *testing.T and never fails a test: an instrument failure observed
// mid-turn is a datum to publish, not a reason to abort the turn.
func pinReadState(pid int) pinStateOutcome {
	if pid <= 0 {
		// Not a reading — the instrument being called wrongly. It matters
		// because strconv.Itoa(-1) renders as `-1`, which ps would consume as a
		// FLAG rather than as an operand: the one shape in this instrument where
		// an integer reaches an exec argument position and could be read as
		// something other than the pid. Rejecting before the exec removes the
		// question.
		return pinStateOutcome{
			Verdict: pinStateInstrumentFailed,
			Detail: fmt.Sprintf("non-positive pid %d: no lookup was attempted, because ps "+
				"would read that operand as a flag rather than as a process id", pid),
			PID:        pid,
			ExitStatus: pinExitStatusUnknown,
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), reachPSTimeout)
	defer cancel()
	stdout, err := exec.CommandContext(ctx, "ps", pinStateArgs(pid)...).Output()
	return pinClassifyState(pid, stdout, err)
}

// pinClassifyState maps one ps result onto exactly one of the four values. It
// is pure over (pid, stdout, err) and is the entire testable surface: splitting
// it from the exec is what lets the keyword-not-found and illegal-option arms be
// covered without provoking a genuinely broken ps, whose text differs across
// platforms.
//
// The BRANCH ORDER is the contract, not an implementation detail — it is what
// keeps the stdout-alongside-error trap unreachable:
//
//  0. pid <= 0                                  -> instrument-failed (in pinReadState, before any exec)
//  1. err != nil, stderr non-empty              -> instrument-failed, naming exit status + stderr
//  2. err != nil, not an *exec.ExitError        -> instrument-failed (exec never ran, or no status)
//  3. err != nil, exit status is not a normal exit -> instrument-failed (killed by a signal)
//  4. err != nil, stderr empty, stdout non-empty   -> instrument-failed; stdout is NEVER parsed
//  5. err != nil, stderr empty, stdout empty, normal exit -> no-such-process
//  6. err == nil, no usable row                 -> instrument-failed; an absence is not a reading
//  7. err == nil, row is about a different pid  -> instrument-failed
//  8. err == nil, row parsed, zombie state      -> exited-but-not-yet-reaped
//  9. err == nil, row parsed, otherwise         -> running
//
// Branches 1, 3 and 4 all precede branch 5: stderr, the exit status's sign, and
// stdout are each checked before the no-such-process arm is reachable at all,
// which makes branch 5 the only input in the instrument that can produce it.
//
// Branch 3 is not in #1235's measured table and was added from a measurement
// taken during implementation: exec.CommandContext kills its child on timeout
// and Output() then returns an *exec.ExitError carrying `signal: killed`,
// ExitCode() == -1, empty stdout and empty stderr — identical to the dead-pid
// signature on every other field. A loaded machine is this family's expected
// condition, not its exotic one, so without branch 3 a timed-out ps publishes
// "the command had already exited".
//
// Every ambiguous input lands on instrument-failed and never on
// no-such-process: the classifier fails SAFE in the one direction that matters,
// because no-such-process is the verdict a consumer would read as a finding.
func pinClassifyState(pid int, stdout []byte, err error) pinStateOutcome {
	out := pinStateOutcome{PID: pid, ExitStatus: pinExitStatusUnknown}
	hasStdout := strings.TrimSpace(string(stdout)) != ""

	if err != nil {
		var exitErr *exec.ExitError
		normalExit := false
		if errors.As(err, &exitErr) {
			out.ExitStatus = exitErr.ExitCode()
			out.ToolStderr = reachCapCommand(strings.TrimSpace(string(exitErr.Stderr)))
			normalExit = out.ExitStatus >= 0
		}

		out.Verdict = pinStateInstrumentFailed
		switch {
		case out.ToolStderr != "":
			out.Detail = pinDetail("ps exited %d and wrote to stderr, so it never reported on "+
				"pid %d: %s", out.ExitStatus, pid, out.ToolStderr)
		case exitErr == nil:
			out.Detail = pinDetail("ps for pid %d did not exit with a status (%T), so no "+
				"reading was taken: %v", pid, err, err)
		case !normalExit:
			out.Detail = pinDetail("ps for pid %d ended on a signal rather than a normal exit "+
				"(%v), so no reading was taken; this is what a CommandContext timeout looks "+
				"like and it is otherwise identical to a dead pid", pid, err)
		case hasStdout:
			out.Detail = pinDetail("ps exited %d for pid %d with silent stderr but %d bytes on "+
				"stdout; stdout alongside an error is NEVER parsed as process rows, because a "+
				"bad column name prints the valid-keyword list there",
				out.ExitStatus, pid, len(stdout))
		default:
			out.Verdict = pinStateNoSuchProcess
			out.Detail = pinDetail("ps exited %d with empty stdout and empty stderr: pid %d is "+
				"not in the process table", out.ExitStatus, pid)
		}
		return out
	}

	out.ExitStatus = 0
	rowPID, ppid, state, ok := pinStateRow(stdout)
	if !ok {
		out.Verdict = pinStateInstrumentFailed
		out.Detail = pinDetail("ps exited 0 for pid %d but no well-formed `%s` row could be "+
			"parsed out of %d bytes of stdout; an absence is not a reading",
			pid, pinStateColumns, len(stdout))
		return out
	}
	if rowPID != pid {
		// The lookup names one pid on its command line, so a row about another
		// one is not an answer to the question that was asked. Attribution is
		// this instrument's whole job.
		out.Verdict = pinStateInstrumentFailed
		out.Detail = pinDetail("ps exited 0 for pid %d but returned a row about pid %d; the "+
			"read is not about the process it was asked for", pid, rowPID)
		return out
	}

	out.PPID = ppid
	out.StateColumn = state
	if pinIsZombie(state) {
		out.Verdict = pinStateExitedNotReaped
		out.Detail = pinDetail("pid %d is in the table with state column %q: it has exited and "+
			"has not yet been reaped, so a row here is NOT evidence that it is running",
			pid, state)
		return out
	}
	out.Verdict = pinStateRunning
	out.Detail = pinDetail("pid %d is in the table with state column %q and ppid %d",
		pid, state, ppid)
	return out
}

// pinStateRow parses the first well-formed row out of the lookup's stdout,
// using this package's Fields / exactly-three-fields / Atoi discipline
// (probeDescendantsFromPS:894-903, reachIndexFromPS:970-980).
//
// Exactly three fields, not "at least three": the state column is a short flag
// string from a small kernel-generated alphabet (S, Ss, Us, Z, ZN, Z+) and never
// contains a space, so a line with a fourth field is not this lookup's output
// and must not be read as one.
func pinStateRow(stdout []byte) (pid, ppid int, state string, ok bool) {
	for _, line := range strings.Split(string(stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		p, perr := strconv.Atoi(fields[0])
		pp, pperr := strconv.Atoi(fields[1])
		if perr != nil || pperr != nil {
			continue
		}
		return p, pp, fields[2], true
	}
	return 0, 0, "", false
}

// pinIsZombie reports whether a state column marks a process that has exited
// and has not yet been reaped.
//
// FIRST RUNE, never equality. Measured on darwin 25.5 (2026-07-30): a `sleep`
// killed and not waited for reads `Z`, and a nice-adjusted one reads `ZN`;
// Linux emits `Z` and `Z+`. `state == "Z"` misses two of those three shapes,
// and the miss is silent — it falls through to `running`, which is the one
// direction this instrument must never fail in.
func pinIsZombie(state string) bool {
	return len(state) > 0 && state[0] == 'Z'
}

// pinDetail formats a Detail line and caps it with #1230's existing helper. The
// cap is not cosmetic: Go's exec caps captured stderr at 32 KB, and the measured
// illegal-option case emits multi-line usage text, which is far too much to
// carry into an artifact an operator pastes into a public issue. The truncation
// marker names #1230 because the cap IS #1230's, deliberately not duplicated
// under a second name.
func pinDetail(format string, args ...any) string {
	return reachCapCommand(fmt.Sprintf(format, args...))
}

// --- self-checks: the exclusion-aware argv scan ------------------------------

// pinArgvFixture is a synthetic four-column `ps -axww -o
// pid=,ppid=,pgid=,command=` table. It is deliberately NOT reachArgvFixture:
// this table needs a row the caller EXCLUDES whose argv nevertheless carries the
// needle (600, the instrument's own test binary), which #1230's fixture has no
// reason to grow.
//
// Well-formed rows: 1, 100, 200, 300, 400, 500, 600. `garbage` has one field,
// `700 700` has two, and the `x` row's pid is not an integer.
const pinArgvFixture = `
    1     0     1 /sbin/launchd
  100     1   100 /usr/local/bin/pyry agent-run --prompt-file=/tmp/run-p/prompt.txt
  200   100   200 /opt/node/bin/node /opt/claude/cli.js --session-id 11111111-2222-3333-4444-555555555555 --settings /tmp/s.json
  300   200   300 /bin/zsh -c cat /tmp/run-p/pin-hold
  400   300   300 cat /tmp/run-p/pin-hold
  500     1   500 /usr/bin/pin-hold --an-unrelated-daemon
  600     1   600 /tmp/build/realclaude.test -test.run=TestPin /tmp/run-p/pin-hold
garbage
  700   700
    x     1     1 /bin/false
`

const (
	// pinFixtureNeedle is the run-unique FIFO path. Row 500's argv carries the
	// same BASE NAME and must not match — that collision is the whole reason
	// identification is content-first.
	pinFixtureNeedle = "/tmp/run-p/pin-hold"
	// pinFixtureOwnPID is the fixture's instrument-owned row: its argv DOES
	// contain the needle, which is what makes it AC2's demonstration.
	pinFixtureOwnPID = 600
	// pinFixtureRows is the number of well-formed rows in pinArgvFixture.
	pinFixtureRows = 7
)

func TestPinMatchArgvExcluding(t *testing.T) {
	const ownReason = "the instrument's own test binary"

	t.Run("an empty exclusion set leaves the existing matcher's output alone", func(t *testing.T) {
		// AC2's "applying exclusions does not change the behaviour of the
		// existing matcher for its existing caller", asserted against the reused
		// matcher itself rather than against a hardcoded list — a hardcoded list
		// would keep passing if reachMatchArgvRows changed underneath it.
		want, wantTotal := reachMatchArgvRows([]byte(pinArgvFixture), []string{pinFixtureNeedle})
		got := pinMatchArgvExcluding([]byte(pinArgvFixture), []string{pinFixtureNeedle}, nil)

		if got.RowsScanned != wantTotal {
			t.Fatalf("rows scanned: got %d, want %d", got.RowsScanned, wantTotal)
		}
		if got.MatchCount != len(want) || len(got.Matches) != len(want) {
			t.Fatalf("matches: got %d (MatchCount %d), want %d", len(got.Matches), got.MatchCount, len(want))
		}
		for i := range want {
			pinAssertSameRow(t, i, got.Matches[i], want[i])
		}
		if len(got.Exclusions) != 0 {
			t.Fatalf("exclusions: got %+v, want none — nothing was excluded", got.Exclusions)
		}
	})

	t.Run("an excluded pid whose argv contains the needle is withheld with its reason", func(t *testing.T) {
		// AC2's named fixture. The point is that the excluded row is a genuine
		// content match: an exclusion set that only ever withholds non-matching
		// rows would demonstrate nothing.
		got := pinMatchArgvExcluding([]byte(pinArgvFixture), []string{pinFixtureNeedle},
			map[int]string{pinFixtureOwnPID: ownReason})

		if len(got.Exclusions) != 1 {
			t.Fatalf("exclusions: got %+v, want exactly pid %d", got.Exclusions, pinFixtureOwnPID)
		}
		ex := got.Exclusions[0]
		if ex.PID != pinFixtureOwnPID {
			t.Fatalf("excluded pid: got %d, want %d", ex.PID, pinFixtureOwnPID)
		}
		if ex.Reason != ownReason {
			t.Errorf("exclusion reason: got %q, want %q — a withholding no reader can "+
				"account for is the same defect as an unexplained verdict", ex.Reason, ownReason)
		}
		if !strings.Contains(ex.Command, pinFixtureNeedle) {
			t.Errorf("exclusion command: got %q, want it to carry the needle %q so the record "+
				"shows the withheld row was genuinely the instrument's own and not a lost "+
				"finding", ex.Command, pinFixtureNeedle)
		}
		if !pinExclusionMatchedNeedle(ex, pinFixtureNeedle) {
			t.Errorf("exclusion needles: got %v, want %q recorded", ex.Needles, pinFixtureNeedle)
		}
		for _, m := range got.Matches {
			if m.PID == pinFixtureOwnPID {
				t.Fatalf("pid %d appears in Matches although it was excluded: %+v",
					pinFixtureOwnPID, m)
			}
		}
	})

	t.Run("every matching row is kept and none is resolved to the first", func(t *testing.T) {
		// AC1. 300 and 400 both carry the needle in their argv tail; 500's base
		// name collides but its argv does not.
		got := pinMatchArgvExcluding([]byte(pinArgvFixture), []string{pinFixtureNeedle},
			map[int]string{pinFixtureOwnPID: ownReason})

		wantPIDs := []int{300, 400}
		if got.MatchCount != len(wantPIDs) {
			t.Fatalf("MatchCount: got %d, want %d — a run that matched more than one row must "+
				"be visible as such in the record", got.MatchCount, len(wantPIDs))
		}
		if len(got.Matches) != len(wantPIDs) {
			t.Fatalf("matches: got %+v, want pids %v", got.Matches, wantPIDs)
		}
		for i, want := range wantPIDs {
			if got.Matches[i].PID != want {
				t.Fatalf("match[%d]: got pid %d, want %d (table order, not first-wins)",
					i, got.Matches[i].PID, want)
			}
			if !strings.Contains(got.Matches[i].Command, pinFixtureNeedle) {
				t.Errorf("match[%d] command %q does not carry the needle it matched in",
					i, got.Matches[i].Command)
			}
			if !reachMatchedNeedle(got.Matches[i], pinFixtureNeedle) {
				t.Errorf("match[%d]: needle %q not recorded in %v — membership is read from "+
					"the recorded list, never by re-scanning the capped Command",
					i, pinFixtureNeedle, got.Matches[i].Needles)
			}
		}
	})

	t.Run("an exclusion with no reason is still recorded with something a reader can act on", func(t *testing.T) {
		// A blank reason field is the same defect as an unexplained verdict: the
		// record shows a row was withheld and gives no way to tell an
		// instrument-owned process from a lost finding.
		got := pinMatchArgvExcluding([]byte(pinArgvFixture), []string{pinFixtureNeedle},
			map[int]string{pinFixtureOwnPID: "   "})
		if len(got.Exclusions) != 1 {
			t.Fatalf("exclusions: got %+v, want exactly pid %d", got.Exclusions, pinFixtureOwnPID)
		}
		if got.Exclusions[0].Reason != pinExclusionReasonUnstated {
			t.Errorf("exclusion reason: got %q, want %q", got.Exclusions[0].Reason,
				pinExclusionReasonUnstated)
		}
	})

	t.Run("an exclusion that matched nothing is not recorded", func(t *testing.T) {
		got := pinMatchArgvExcluding([]byte(pinArgvFixture), []string{pinFixtureNeedle},
			map[int]string{999999: "a pid that is not in the table"})
		if len(got.Exclusions) != 0 {
			t.Fatalf("exclusions: got %+v, want none — an exclusion is recorded only when it "+
				"actually fired, otherwise the record claims a withholding that never happened",
				got.Exclusions)
		}
		if got.MatchCount != 3 {
			t.Fatalf("MatchCount: got %d, want 3 (300, 400, 600 all match)", got.MatchCount)
		}
	})

	t.Run("excluding does not disturb the matcher's own output", func(t *testing.T) {
		// The partition builds a new slice; it must not alias or truncate the
		// matcher's. A second call over the same bytes still sees all three.
		_ = pinMatchArgvExcluding([]byte(pinArgvFixture), []string{pinFixtureNeedle},
			map[int]string{pinFixtureOwnPID: ownReason, 300: "also withheld"})
		again, _ := reachMatchArgvRows([]byte(pinArgvFixture), []string{pinFixtureNeedle})
		if len(again) != 3 {
			t.Fatalf("reachMatchArgvRows after a partition: got %d rows, want 3 — the partition "+
				"must be a strictly downstream filter", len(again))
		}
	})

	t.Run("malformed, short and empty input reach a recorded outcome", func(t *testing.T) {
		// AC4. The distinction that matters is between "the scan ran and nothing
		// matched" and "the scan saw no well-formed rows at all": an empty
		// Matches says neither, RowsScanned says which.
		tests := []struct {
			name     string
			table    string
			wantRows int
		}{
			{name: "empty input", table: "", wantRows: 0},
			{name: "only malformed lines", table: "garbage\n  700   700\n  x 1 1 /bin/false\n", wantRows: 0},
			{name: "short line with no command column", table: "  100     1   100\n", wantRows: 0},
			{name: "well-formed rows, no needle match", table: pinArgvFixture, wantRows: pinFixtureRows},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := pinMatchArgvExcluding([]byte(tc.table), []string{"/no/such/needle"}, nil)
				if got.RowsScanned != tc.wantRows {
					t.Fatalf("RowsScanned: got %d, want %d — the row count is what separates a "+
						"scan that found nothing from a scan that parsed nothing",
						got.RowsScanned, tc.wantRows)
				}
				if got.MatchCount != 0 || len(got.Matches) != 0 {
					t.Fatalf("matches: got %+v, want none", got.Matches)
				}
				if len(got.Exclusions) != 0 {
					t.Fatalf("exclusions: got %+v, want none", got.Exclusions)
				}
			})
		}
	})
}

// TestPinScanArgv_ExcludesTheInstrumentsOwnProcess takes the free in-rig flip.
//
// Synthetic bytes prove the partition; they do NOT prove the exec wiring. A
// pinScanArgv wired to the wrong needle, or an exclusion set consulted against
// the wrong pid, passes every table case above and fails in production. This
// test drives the whole path against the real process table with the one pid it
// can name with certainty — its own — and proves BOTH arms on ONE subject:
// present in Matches without the exclusion, present in Exclusions and absent
// from Matches with it.
//
// It asserts membership per pid rather than a match count: the invoking shell's
// argv also carries this binary's path (measured — two rows matched), so a
// count assertion would be testing the harness, not the instrument.
func TestPinScanArgv_ExcludesTheInstrumentsOwnProcess(t *testing.T) {
	self := os.Args[0]
	if self == "" {
		t.Fatalf("os.Args[0] is empty; this test needs the test binary's own path as its needle")
	}
	const reason = "the instrument's own test binary"
	mypid := os.Getpid()

	without, err := pinScanArgv([]string{self}, nil)
	if err != nil {
		t.Fatalf("pinScanArgv without exclusions: %v", err)
	}
	if without.RowsScanned == 0 {
		t.Fatalf("RowsScanned is 0 although the scan reported no error; the live `ps` read is "+
			"not wired up (matches: %+v)", without.Matches)
	}
	if !pinScanHasPID(without.Matches, mypid) {
		t.Fatalf("own pid %d is not among the %d rows matching %q; the content match is not "+
			"reaching the live table, so the exclusion arm below would prove nothing",
			mypid, without.MatchCount, self)
	}

	with, err := pinScanArgv([]string{self}, map[int]string{mypid: reason})
	if err != nil {
		t.Fatalf("pinScanArgv with exclusions: %v", err)
	}
	if pinScanHasPID(with.Matches, mypid) {
		t.Fatalf("own pid %d is still in Matches although it was excluded: %+v", mypid, with.Matches)
	}
	var found bool
	for _, ex := range with.Exclusions {
		if ex.PID != mypid {
			continue
		}
		found = true
		if ex.Reason != reason {
			t.Errorf("exclusion reason: got %q, want %q", ex.Reason, reason)
		}
		if !strings.Contains(ex.Command, self) {
			t.Errorf("exclusion command %q does not carry the needle %q", ex.Command, self)
		}
	}
	if !found {
		t.Fatalf("own pid %d was withheld from Matches but recorded in no exclusion (%+v); a "+
			"silently dropped row is exactly the lost finding the reason field exists to rule out",
			mypid, with.Exclusions)
	}
}

// --- self-checks: the four-valued per-pid state read -------------------------

func TestPinClassifyState(t *testing.T) {
	const keywordList = "%cpu %mem acflag acflg args blocked comm command cpu cputime etime f"
	const usage = "ps: illegal option -- Z\nusage: ps [-AaCcEefhjlMmrSTvwXx] [-O fmt | -o fmt]"

	tests := []struct {
		name         string
		pid          int
		stdout       string
		err          func(t *testing.T) error
		wantVerdict  string
		wantState    string
		wantPPID     int
		wantExit     int
		wantStderr   string
		wantDetailIn string
	}{
		{
			// The measured dead-pid signature, and the ONLY input in the whole
			// instrument that is allowed to produce this verdict.
			name:        "exit 1, empty stdout, empty stderr is the pid being gone",
			pid:         4242,
			err:         func(t *testing.T) error { return pinExit1(t, "") },
			wantVerdict: pinStateNoSuchProcess,
			wantExit:    1,
		},
		{
			// THE TRAP: a bad column name prints the valid-keyword list on
			// STDOUT. Parsing it would read `%cpu %mem acflag` as a process row.
			name:         "exit 1 with keyword-not-found stderr is a broken instrument, and its stdout is not parsed",
			pid:          4242,
			stdout:       keywordList,
			err:          func(t *testing.T) error { return pinExit1(t, "ps: nosuchcol: keyword not found") },
			wantVerdict:  pinStateInstrumentFailed,
			wantState:    "",
			wantExit:     1,
			wantStderr:   "keyword not found",
			wantDetailIn: "stderr",
		},
		{
			name:         "exit 1 with illegal-option usage text is a broken instrument",
			pid:          4242,
			err:          func(t *testing.T) error { return pinExit1(t, usage) },
			wantVerdict:  pinStateInstrumentFailed,
			wantExit:     1,
			wantStderr:   "illegal option",
			wantDetailIn: "stderr",
		},
		{
			// Defensive: stdout is never parsed alongside an error, whatever it
			// holds. Without this arm the keyword-list row above is the only
			// thing standing between a broken ps and a fabricated reading.
			name:         "exit 1 with a plausible row on stdout and silent stderr is still a broken instrument",
			pid:          4242,
			stdout:       "4242 1 S",
			err:          func(t *testing.T) error { return pinExit1(t, "") },
			wantVerdict:  pinStateInstrumentFailed,
			wantState:    "",
			wantExit:     1,
			wantDetailIn: "stdout",
		},
		{
			// MEASURED during implementation, and not in the ticket's table: a
			// CommandContext timeout kills ps and surfaces as an ExitError with
			// ExitCode() == -1, empty stdout and empty stderr — byte-identical
			// to the dead-pid signature on every field the table names. The sign
			// of the exit status is the only discriminator.
			name:         "an exit error carrying a signal, not a status, is a broken instrument",
			pid:          4242,
			err:          func(t *testing.T) error { return pinSignaled(t) },
			wantVerdict:  pinStateInstrumentFailed,
			wantExit:     -1,
			wantDetailIn: "signal",
		},
		{
			name:         "an error carrying no exit status at all is a broken instrument",
			pid:          4242,
			stdout:       "4242 1 S",
			err:          func(t *testing.T) error { return fmt.Errorf("exec ps: %w", os.ErrNotExist) },
			wantVerdict:  pinStateInstrumentFailed,
			wantState:    "",
			wantExit:     pinExitStatusUnknown,
			wantDetailIn: "did not exit",
		},
		{
			// The measured macOS shape. `ps` LISTS zombies, so this row is the
			// reason the read is four-valued and not a boolean.
			name:        "a zombie row on macOS reads as exited-but-not-yet-reaped",
			pid:         15302,
			stdout:      "15302 15287 ZN\n",
			wantVerdict: pinStateExitedNotReaped,
			wantState:   "ZN",
			wantPPID:    15287,
		},
		{
			name:        "the Linux zombie shape reads the same",
			pid:         15302,
			stdout:      "15302 15287 Z+\n",
			wantVerdict: pinStateExitedNotReaped,
			wantState:   "Z+",
			wantPPID:    15287,
		},
		{
			name:        "a bare Z reads the same",
			pid:         32318,
			stdout:      "32318 32308 Z   \n",
			wantVerdict: pinStateExitedNotReaped,
			wantState:   "Z",
			wantPPID:    32308,
		},
		{
			// AC3's re-parented fixture: ppid 1. The lookup is direct, so a
			// target that has re-parented to init reads exactly like any other —
			// this is the case probeDescendantsFromPS structurally cannot see.
			name:        "a target re-parented to pid 1 reads as running",
			pid:         4242,
			stdout:      "4242 1 S\n",
			wantVerdict: pinStateRunning,
			wantState:   "S",
			wantPPID:    1,
		},
		{
			name:        "init itself reads as running",
			pid:         1,
			stdout:      "    1     0 Ss\n",
			wantVerdict: pinStateRunning,
			wantState:   "Ss",
			wantPPID:    0,
		},
		{
			name:         "exit 0 with empty stdout is a broken instrument, not an absence",
			pid:          4242,
			wantVerdict:  pinStateInstrumentFailed,
			wantExit:     0,
			wantDetailIn: "no well-formed",
		},
		{
			name:         "exit 0 with unparseable stdout is a broken instrument",
			pid:          4242,
			stdout:       "garbage\n",
			wantVerdict:  pinStateInstrumentFailed,
			wantExit:     0,
			wantDetailIn: "no well-formed",
		},
		{
			name:         "exit 0 with a short row is a broken instrument",
			pid:          4242,
			stdout:       "4242 1\n",
			wantVerdict:  pinStateInstrumentFailed,
			wantExit:     0,
			wantDetailIn: "no well-formed",
		},
		{
			// The lookup names one pid on the command line, so a row about a
			// different one is not an answer to the question that was asked.
			name:         "a row about a different pid is a broken instrument",
			pid:          4242,
			stdout:       "9999 1 S\n",
			wantVerdict:  pinStateInstrumentFailed,
			wantExit:     0,
			wantDetailIn: "9999",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.err != nil {
				err = tc.err(t)
			}
			got := pinClassifyState(tc.pid, []byte(tc.stdout), err)

			if got.Verdict != tc.wantVerdict {
				t.Fatalf("verdict: got %q (%s), want %q", got.Verdict, got.Detail, tc.wantVerdict)
			}
			if got.PID != tc.pid {
				t.Errorf("pid: got %d, want %d — the outcome names the pid the read was about",
					got.PID, tc.pid)
			}
			if got.StateColumn != tc.wantState {
				t.Errorf("state column: got %q, want %q — the gate's own input must appear in "+
					"the recorded output, not only in the classifier's logic",
					got.StateColumn, tc.wantState)
			}
			if got.PPID != tc.wantPPID {
				t.Errorf("ppid: got %d, want %d", got.PPID, tc.wantPPID)
			}
			if got.ExitStatus != tc.wantExit {
				t.Errorf("exit status: got %d, want %d", got.ExitStatus, tc.wantExit)
			}
			if tc.wantStderr != "" && !strings.Contains(got.ToolStderr, tc.wantStderr) {
				t.Errorf("tool stderr: got %q, want it to contain %q — an instrument-failed "+
					"value must name the failure", got.ToolStderr, tc.wantStderr)
			}
			if tc.wantDetailIn != "" && !strings.Contains(got.Detail, tc.wantDetailIn) {
				t.Errorf("detail: got %q, want it to contain %q so the record names which arm "+
					"fired and why", got.Detail, tc.wantDetailIn)
			}
			if got.Verdict == pinStateInstrumentFailed && got.Detail == "" {
				t.Errorf("instrument-failed with an empty detail: a half-run instrument that " +
					"cannot say what failed is indistinguishable from a reading")
			}
			if !pinIsVerdict(got.Verdict) {
				t.Errorf("verdict %q is not one of the four recorded values", got.Verdict)
			}
		})
	}

	t.Run("a non-positive pid is rejected before any lookup is attempted", func(t *testing.T) {
		// strconv.Itoa(-1) renders as `-1`, which ps consumes as a FLAG rather
		// than as an operand — the one shape in this instrument where an integer
		// could reach an exec argument position and be read as something other
		// than the pid. Rejecting before the exec removes the question.
		for _, pid := range []int{0, -1} {
			got := pinReadState(pid)
			if got.Verdict != pinStateInstrumentFailed {
				t.Fatalf("pinReadState(%d) = %q (%s); want %q", pid, got.Verdict, got.Detail,
					pinStateInstrumentFailed)
			}
			if got.ToolStderr != "" {
				t.Errorf("pinReadState(%d) recorded ps stderr %q; no ps may be run at all",
					pid, got.ToolStderr)
			}
			if !strings.Contains(got.Detail, "non-positive") {
				t.Errorf("pinReadState(%d) detail = %q; want it to name the guard", pid, got.Detail)
			}
		}
	})
}

// TestPinStateColumns_ReadsNoEnvironment is the deterministic tripwire behind
// redaction rule 1.
//
// The rule itself is a doc comment, which is advisory. This asserts on the
// CONSTANT and on the argument list actually handed to exec, because the
// constant is what a future edit changes: the realistic regression is someone
// widening the column set to "improve the record", and one flag is the
// difference between this instrument and pasting the operator's live
// CLAUDE_CODE_OAUTH_TOKEN into a public issue.
func TestPinStateColumns_ReadsNoEnvironment(t *testing.T) {
	for _, forbidden := range []string{"command", "args", "comm", "environ", "env"} {
		if strings.Contains(pinStateColumns, forbidden) {
			t.Errorf("pinStateColumns = %q contains %q; the per-pid read is three "+
				"kernel-generated columns and must never grow one that carries argv or the "+
				"environment", pinStateColumns, forbidden)
		}
	}

	args := pinStateArgs(4242)
	for _, arg := range args {
		for _, forbidden := range []string{"-E", "-e", "-Eww", "-eww", "eww", "e"} {
			if arg == forbidden {
				t.Errorf("pinStateArgs = %v contains %q; `ps -E` / `ps -e <env column>` / BSD "+
					"`ps eww` print each process's full ENVIRONMENT", args, forbidden)
			}
		}
		if strings.Contains(arg, "environ") {
			t.Errorf("pinStateArgs = %v contains an environment column", args)
		}
	}

	// "Keep the reads proportionate": the re-check is a lookup for one known
	// pid, not a second full process table.
	if !pinArgsContain(args, "-p") {
		t.Errorf("pinStateArgs = %v does not name a single pid with -p", args)
	}
	for _, wide := range []string{"-a", "-A", "-ax", "-axww", "-x"} {
		if pinArgsContain(args, wide) {
			t.Errorf("pinStateArgs = %v carries %q; the per-pid re-check must not read a second "+
				"full process table", args, wide)
		}
	}
}

// TestPinReadState_FlipsAcrossOneProcessLifetime proves the read FLIPS across
// all three of its non-failure values, on ONE subject across ONE lifetime.
//
// Synthetic bytes prove the classifier; they do not prove the exec wiring. A
// wrong column order or a missing flag passes every table case above and fails
// in production. And two differently constructed subjects would not prove the
// flip: an instrument that can only ever answer `running` is the dual failure —
// it makes every killed command read as alive, which is unfalsifiable.
//
// Wait() is the synchronisation point, not Kill(): the zombie arm is only
// observable between them, and it is the arm that reads `running` under an
// equality check against "Z" and is invisible to a lookup with no state column
// at all.
func TestPinReadState_FlipsAcrossOneProcessLifetime(t *testing.T) {
	cmd := exec.Command(pinFlipCommand, pinFlipDuration)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %s: %v", pinFlipCommand, pinFlipDuration, err)
	}
	pid := cmd.Process.Pid
	reaped := false
	// Registered immediately after Start: any t.Fatalf below would otherwise
	// leave a live child owned by a dead test process.
	t.Cleanup(func() {
		if reaped {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	running := pinReadState(pid)
	if running.Verdict != pinStateRunning {
		t.Fatalf("read of a live child (pid %d) = %q (%s); want %q — an instrument that cannot "+
			"see a process it is looking straight at reports every live command as gone",
			pid, running.Verdict, running.Detail, pinStateRunning)
	}
	if running.StateColumn == "" {
		t.Errorf("running outcome recorded no state column; the classifier's own input must " +
			"appear in the evidence")
	}
	if pinIsZombie(running.StateColumn) {
		t.Errorf("running outcome recorded state %q, which begins with Z", running.StateColumn)
	}
	if running.PPID != os.Getpid() {
		t.Errorf("ppid: got %d, want this test process %d — the row must be about the child "+
			"that was started here", running.PPID, os.Getpid())
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child %d: %v", pid, err)
	}
	// Bounded poll rather than a fixed sleep: the alternative under load is a
	// weaker assertion, not a faster test. Nothing else can reap this child —
	// this process is its parent and has not called Wait — so the zombie window
	// is owned by this test and does not close on its own.
	zombie := pinPollState(pid, pinStateExitedNotReaped, pinFlipWindow)
	if zombie.Verdict != pinStateExitedNotReaped {
		t.Fatalf("read of pid %d after Kill and before Wait = %q (%s); want %q — `ps` lists a "+
			"killed-but-unreaped process as a row, so collapsing this into %q makes every "+
			"zombie read as alive", pid, zombie.Verdict, zombie.Detail, pinStateExitedNotReaped,
			pinStateRunning)
	}
	if !pinIsZombie(zombie.StateColumn) {
		t.Errorf("zombie outcome recorded state %q, which does not begin with Z", zombie.StateColumn)
	}

	// A killed process yields *exec.ExitError; that is expected here, so nothing
	// is asserted about it.
	_ = cmd.Wait()
	reaped = true

	gone := pinPollState(pid, pinStateNoSuchProcess, pinFlipWindow)
	if gone.Verdict != pinStateNoSuchProcess {
		t.Fatalf("read of pid %d after Wait reaped it = %q (%s); want %q — the read does not "+
			"flip, so a gone process is indistinguishable from a live one",
			pid, gone.Verdict, gone.Detail, pinStateNoSuchProcess)
	}
	if gone.ExitStatus != 1 || gone.ToolStderr != "" {
		t.Errorf("no-such-process outcome recorded exit %d and stderr %q; want exit 1 with "+
			"silent stderr — that signature is the ONLY input allowed to produce %q",
			gone.ExitStatus, gone.ToolStderr, pinStateNoSuchProcess)
	}
	if gone.StateColumn != "" {
		t.Errorf("no-such-process outcome recorded state %q; there was no row to read",
			gone.StateColumn)
	}
}

// --- test helpers ------------------------------------------------------------

const (
	pinFlipCommand      = "sleep"
	pinFlipDuration     = "30"
	pinFlipWindow       = 10 * time.Second
	pinFlipPollInterval = 20 * time.Millisecond
)

// pinExit1 returns an *exec.ExitError carrying a REAL normal-exit-1
// ProcessState with synthetic stderr attached. os.ProcessState's fields are
// unexported, so an exit status cannot be forged; `false` is the POSIX way to
// obtain one. Driving the classifier synthetically is what lets the
// keyword-not-found and illegal-option arms be covered without provoking a
// genuinely broken `ps`, whose text differs across platforms.
func pinExit1(t *testing.T, stderr string) error {
	t.Helper()
	err := exec.Command("false").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf(`exec.Command("false").Run() = %v; want an *exec.ExitError to borrow a real `+
			`exit-1 ProcessState from`, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf(`exec.Command("false").Run() exited %d; want 1`, exitErr.ExitCode())
	}
	return &exec.ExitError{ProcessState: exitErr.ProcessState, Stderr: []byte(stderr)}
}

// pinSignaled returns an *exec.ExitError from a process killed by a signal —
// ExitCode() == -1, empty stderr. This is the shape a CommandContext timeout
// produces (measured: `exec.CommandContext(ctx, "sleep", "5")` past its
// deadline returns `signal: killed`, NOT context.DeadlineExceeded), and it is
// byte-identical to the dead-pid signature on stdout, stderr and error type.
func pinSignaled(t *testing.T) error {
	t.Helper()
	cmd := exec.Command(pinFlipCommand, pinFlipDuration)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", pinFlipCommand, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill %s: %v", pinFlipCommand, err)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("wait on a killed %s = %v; want an *exec.ExitError", pinFlipCommand, err)
	}
	if exitErr.ExitCode() != -1 {
		t.Fatalf("a killed %s reported exit code %d; want -1 (signalled, not a normal exit)",
			pinFlipCommand, exitErr.ExitCode())
	}
	return exitErr
}

// pinPollState polls until the read reaches want or the window closes,
// returning the last outcome either way.
func pinPollState(pid int, want string, window time.Duration) pinStateOutcome {
	deadline := time.Now().Add(window)
	for {
		got := pinReadState(pid)
		if got.Verdict == want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(pinFlipPollInterval)
	}
}

// pinIsVerdict reports whether v is one of the four recorded values.
func pinIsVerdict(v string) bool {
	switch v {
	case pinStateRunning, pinStateExitedNotReaped, pinStateNoSuchProcess, pinStateInstrumentFailed:
		return true
	}
	return false
}

func pinScanHasPID(rows []reachProc, pid int) bool {
	for _, r := range rows {
		if r.PID == pid {
			return true
		}
	}
	return false
}

func pinExclusionMatchedNeedle(ex pinExclusion, needle string) bool {
	for _, n := range ex.Needles {
		if n == needle {
			return true
		}
	}
	return false
}

func pinArgsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// pinAssertSameRow compares two reachProc rows field by field. reachProc
// carries a slice, so it is not ==-comparable, and a %+v comparison would
// silently start comparing the 512-byte-capped Command
// (docs/knowledge/codebase/1230.md § Lessons learned).
func pinAssertSameRow(t *testing.T, i int, got, want reachProc) {
	t.Helper()
	if got.PID != want.PID || got.PPID != want.PPID || got.PGID != want.PGID {
		t.Fatalf("row[%d]: got pid=%d ppid=%d pgid=%d, want pid=%d ppid=%d pgid=%d",
			i, got.PID, got.PPID, got.PGID, want.PID, want.PPID, want.PGID)
	}
	if got.Command != want.Command {
		t.Fatalf("row[%d] command: got %q, want %q", i, got.Command, want.Command)
	}
	if len(got.Needles) != len(want.Needles) {
		t.Fatalf("row[%d] needles: got %v, want %v", i, got.Needles, want.Needles)
	}
	for j := range want.Needles {
		if got.Needles[j] != want.Needles[j] {
			t.Fatalf("row[%d] needle[%d]: got %q, want %q", i, j, got.Needles[j], want.Needles[j])
		}
	}
}
