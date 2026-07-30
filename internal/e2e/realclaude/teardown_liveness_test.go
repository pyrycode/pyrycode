//go:build e2e_realclaude

package realclaude

// The teardown-liveness instrument #1251 measures with: a classifier over the
// reaper's own log line, a real-bytes proof of the per-pid liveness read's
// fail-safe premise, and the record that carries all three readings.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence. Everything here runs offline: no live
// claude, no credentials, no daemon, no env gate, no t.Skip.
//
//	go test -tags e2e_realclaude -run '^TestTdn' -v ./internal/e2e/realclaude/
//
// # Reused, not rebuilt
//
// The per-pid liveness read is #1235's (pinReadState / pinClassifyState /
// pinScanArgv) and the FIFO reader-presence read is #1239's (fifoLiveRead).
// Both carry full design treatments in their own file headers — the four-valued
// read, redaction rule 1, and "an instrument failure is a datum, not an abort"
// are argued there and deliberately not restated here. This file CALLS them and
// edits neither.
//
// # The reaper line, and the two ways a matcher over it inverts
//
// reap.go:65 is the only line classified here. Measured 2026-07-30 (Darwin
// 25.5), both renderings on one machine:
//
//	time=… level=INFO msg="agentrun: reaped claude descendant process groups" count=2 pgids="[4242 77]"
//	2026/07/30 23:18:29 INFO agentrun: reaped claude descendant process groups count=2 pgids="[4242 77]"
//
// The first is slog.NewTextHandler (cmd/pyry/main.go:743). The second is
// slog.Default(), and it is the one that matters: runAgentRunPty
// (cmd/pyry/agent_run.go:299) sets no Logger on ptyrunner.Config, so
// ptyrunner.Run falls back to slog.Default() (runner.go:289-292), and every
// probe in this package spawns `pyry agent-run` and captures its stderr. A
// matcher anchored on `msg="agentrun: reaped…"` finds nothing on the live path
// and answers "no line" — read as "the reaper never fired" — with nothing going
// red. So the anchor is the BARE message text, a string literal in this file,
// never a level token, a timestamp, or a reference to what reap.go defines.
//
// Membership is over PARSED INTEGERS, never a substring, because the matcher
// inverts in both directions. A `pgids=[<held>]` substring probe is correct for
// the single-group line and fails the moment a second group is reaped, since
// slog quotes the value as soon as it contains a space — reporting "the reaper
// ran and did not kill our group", which is precisely the regression #1251
// exists to catch. Its dual is worse and is a false POSITIVE: held pgid 77
// matches the text of `pgids=[7788]`, and that is the arm a consumer reads as
// "no leak".
//
// # The premise the whole liveness read fails safe on
//
// pinClassifyState's branch 5 — non-zero exit, empty stdout AND empty stderr —
// is the only input that yields pinStateNoSuchProcess, the one verdict #1251
// reports as its result. Branch 1 keeps every broken invocation away from it,
// which is entirely load-bearing on real `ps` writing to stderr — and
// TestPinClassifyState builds its errors with pinExit1 / pinSignaled, so
// nothing asserted it. TestTdnRealPSMisinvocationsFailSafe executes real
// mis-invocations and asserts that premise on the bytes the classifier actually
// consumes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// --- self-checks: the reaper-log classifier ----------------------------------

// The fixture renderings, measured 2026-07-30 on Darwin 25.5 against Go's
// log/slog. The message text is a STRING LITERAL in every one of them and is
// never read back out of reap.go: a renamed message must break this test rather
// than silently follow it.
const (
	tdnFixtureTextOne = `time=2026-07-30T23:18:29.220+03:00 level=INFO ` +
		`msg="agentrun: reaped claude descendant process groups" count=1 pgids=[89355]`

	tdnFixtureTextTwo = `time=2026-07-30T23:18:29.220+03:00 level=INFO ` +
		`msg="agentrun: reaped claude descendant process groups" count=2 pgids="[89355 4242]"`

	tdnFixtureDefaultOne = `2026/07/30 23:18:29 INFO ` +
		`agentrun: reaped claude descendant process groups count=1 pgids=[89355]`

	tdnFixtureDefaultTwo = `2026/07/30 23:18:29 INFO ` +
		`agentrun: reaped claude descendant process groups count=2 pgids="[89355 4242]"`

	// tdnFixtureOtherLines carries reap.go:59's Warn, whose attribute is `pgid=`
	// (SINGULAR) and whose pgid is the held one. Nothing in it is the Info line,
	// so the held pgid appearing in the bytes must not produce an answer.
	tdnFixtureOtherLines = `2026/07/30 23:18:28 INFO pyry: agent-run starting workdir=/tmp/wd
2026/07/30 23:18:29 WARN agentrun: descendant reap: kill group failed pgid=89355 err="operation not permitted"
2026/07/30 23:18:31 INFO pyry: claude exited status=0`
)

// tdnFixtureHeldPGID is the pgid the fixtures above report as reaped.
const tdnFixtureHeldPGID = 89355

func TestTdnClassifyReapLog(t *testing.T) {
	tests := []struct {
		name         string
		stderr       string
		held         int
		wantVerdict  string
		wantPGIDs    []int
		wantLines    int
		wantCount    int
		wantDetailIn []string
	}{
		{
			name:        "TextHandler, one pgid, held present",
			stderr:      tdnFixtureTextOne,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355},
			wantLines:   1,
			wantCount:   1,
		},
		{
			// The case a single-pgid matcher gets wrong. slog quotes the value
			// the moment it contains a space, so `pgids=[89355]` becomes
			// `pgids="[89355 4242]"` and a substring probe stops matching —
			// inverting the reading on exactly the multi-group regression #1251
			// exists to catch.
			name:        "TextHandler, slog's quoted multi-pgid rendering, held present",
			stderr:      tdnFixtureTextTwo,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355, 4242},
			wantLines:   1,
			wantCount:   2,
		},
		{
			// The rendering the LIVE path produces: `pyry agent-run` passes no
			// Logger, so ptyrunner falls back to slog.Default().
			name:        "slog.Default rendering, one pgid, held present",
			stderr:      tdnFixtureDefaultOne,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355},
			wantLines:   1,
			wantCount:   1,
		},
		{
			name:        "slog.Default rendering, quoted multi-pgid, held present",
			stderr:      tdnFixtureDefaultTwo,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355, 4242},
			wantLines:   1,
			wantCount:   2,
		},
		{
			name:         "a reaped line that does not carry the held pgid",
			stderr:       tdnFixtureTextTwo,
			held:         777,
			wantVerdict:  tdnReapHeldPGIDAbsent,
			wantPGIDs:    []int{89355, 4242},
			wantLines:    1,
			wantCount:    2,
			wantDetailIn: []string{"777"},
		},
		{
			// The false POSITIVE a substring matcher produces: 77 is inside the
			// text of 7788, and this is the arm a consumer reads as "no leak".
			name: "a held pgid that is a substring of a reaped one is not a member",
			stderr: `2026/07/30 23:18:29 INFO ` +
				`agentrun: reaped claude descendant process groups count=1 pgids=[7788]`,
			held:        77,
			wantVerdict: tdnReapHeldPGIDAbsent,
			wantPGIDs:   []int{7788},
			wantLines:   1,
			wantCount:   1,
		},
		{
			// reap.go:59's Warn carries `pgid=` (singular) and the held pgid, so
			// bytes alone are not an answer: the anchor is the message.
			name:        "other pyry lines, including the singular-pgid Warn, are not the reap line",
			stderr:      tdnFixtureOtherLines,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapNoLine,
			wantLines:   0,
			// reap.go:64 guards the emit on len(reaped) > 0, so silence has TWO
			// readings and collapsing them into either one is the defect.
			wantDetailIn: []string{"reaped nothing", "never fired"},
		},
		{
			name:         "empty input",
			stderr:       "",
			held:         tdnFixtureHeldPGID,
			wantVerdict:  tdnReapNoLine,
			wantLines:    0,
			wantDetailIn: []string{"reaped nothing", "never fired"},
		},
		{
			name: "an anchored line with no pgids attribute is a broken instrument",
			stderr: `2026/07/30 23:18:29 INFO ` +
				`agentrun: reaped claude descendant process groups count=1`,
			held:         tdnFixtureHeldPGID,
			wantVerdict:  tdnReapInstrumentFailed,
			wantLines:    1,
			wantDetailIn: []string{"pgids="},
		},
		{
			name: "an anchored line with an unterminated pgid list is a broken instrument",
			stderr: `2026/07/30 23:18:29 INFO ` +
				`agentrun: reaped claude descendant process groups count=2 pgids="[89355 4242`,
			held:         tdnFixtureHeldPGID,
			wantVerdict:  tdnReapInstrumentFailed,
			wantLines:    1,
			wantDetailIn: []string{"unterminated"},
		},
		{
			name: "an anchored line whose list holds a non-integer is a broken instrument",
			stderr: `2026/07/30 23:18:29 INFO ` +
				`agentrun: reaped claude descendant process groups count=2 pgids="[89355 abc]"`,
			held:         tdnFixtureHeldPGID,
			wantVerdict:  tdnReapInstrumentFailed,
			wantLines:    1,
			wantDetailIn: []string{"abc"},
		},
		{
			// The union across lines, not the first match. #1235's own
			// anti-first-match discipline (pinScan.Matches is a slice precisely
			// because nothing here resolves to "the" one) applied to lines.
			name:        "two reap lines, the held pgid only in the second",
			stderr:      tdnFixtureDefaultTwo + "\n" + tdnFixtureTextOne,
			held:        4242,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355, 4242},
			wantLines:   2,
			wantCount:   3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tdnClassifyReapLog([]byte(tc.stderr), tc.held)

			if got.Verdict != tc.wantVerdict {
				t.Fatalf("verdict: got %q (%s), want %q", got.Verdict, got.Detail, tc.wantVerdict)
			}
			if !tdnIsReapVerdict(got.Verdict) {
				t.Errorf("verdict %q is not one of the recorded values", got.Verdict)
			}
			if got.HeldPGID != tc.held {
				t.Errorf("held pgid: got %d, want %d — the outcome names the pgid it was asked "+
					"about", got.HeldPGID, tc.held)
			}
			if !tdnEqualInts(got.PGIDs, tc.wantPGIDs) {
				t.Errorf("pgids: got %v, want %v — the record shows what membership was decided "+
					"against, not merely the verdict", got.PGIDs, tc.wantPGIDs)
			}
			if got.LineCount != tc.wantLines {
				t.Errorf("reap lines seen: got %d, want %d", got.LineCount, tc.wantLines)
			}
			if got.Count != tc.wantCount {
				t.Errorf("count attr: got %d, want %d — reap.go's own count= is carried for "+
					"cross-check against the parsed list", got.Count, tc.wantCount)
			}
			if got.Detail == "" {
				t.Errorf("empty detail: an outcome that cannot say which arm fired and why is " +
					"indistinguishable from a reading")
			}
			for _, want := range tc.wantDetailIn {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("detail: got %q, want it to contain %q", got.Detail, want)
				}
			}
		})
	}

	t.Run("a pgid the reaper could never report is rejected rather than answered", func(t *testing.T) {
		// reap.go:52 skips pgid <= 1 before it kills anything, so no line can
		// ever carry one. Answering "absent" for such a caller would manufacture
		// a leak finding out of a consumer that failed to capture its pgid —
		// the exact fail-safe rule this instrument is built on.
		for _, held := range []int{0, -1, 1} {
			got := tdnClassifyReapLog([]byte(tdnFixtureDefaultOne), held)
			if got.Verdict != tdnReapInstrumentFailed {
				t.Errorf("tdnClassifyReapLog(held=%d) = %q (%s); want %q", held, got.Verdict,
					got.Detail, tdnReapInstrumentFailed)
			}
		}
	})
}

// --- self-checks: the liveness read's fail-safe premise, against real ps -----

// TestTdnRealPSMisinvocationsFailSafe executes real `ps` mis-invocations and
// proves, on the bytes the classifier actually consumes, that a broken
// instrument can never publish an absence.
//
// The load-bearing assertion in every arm is that STDERR IS NON-EMPTY. That is
// what keeps pinClassifyState's branch 5 — the only input that yields
// pinStateNoSuchProcess — unreachable from a broken instrument. A verdict-only
// assertion would still pass on a platform that had gone silent on stderr,
// which is precisely the scenario in which the instrument would publish "the
// process is gone" out of its own breakage.
//
// # Which of these shapes can occur through pinReadState in production
//
//   - A non-numeric pid: UNREACHABLE. pinReadState takes an int.
//   - A non-positive pid: UNREACHABLE past the guard at :276, and already
//     covered by TestPinClassifyState's final subtest (:919). Not duplicated.
//   - Arms A, B and C: UNREACHABLE without an edit to pinStateColumns or
//     pinStateArgs, which TestPinStateColumns_ReadsNoEnvironment (:950) already
//     catches. They are proven here as PLATFORM facts, not as reachable paths.
//   - Arm D: structurally REACHABLE. pinReadState bounds the pid below and
//     never above, so any caller holding a garbage-but-positive pid reaches it.
//     No current caller does — pids come from pinScanArgv matches and
//     cmd.Process.Pid — but this is the one arm whose premise protects a live
//     path rather than an edit-only one.
func TestTdnRealPSMisinvocationsFailSafe(t *testing.T) {
	arms := []struct {
		name string
		// args is built by construction, never by pinStateArgs: deviating from
		// the production argument list is what makes these mis-invocations.
		args  []string
		pid   int
		extra func(t *testing.T, stdout []byte, err error)
	}{
		{
			// The strongest fixture here, and deliberately FOUR columns rather
			// than the three in the ticket body. `ps` drops the unknown column
			// and prints the rest, so a four-column request comes back as a
			// three-field row that pinStateRow parses SUCCESSFULLY — see extra.
			name:  "A. a bad column carrying the = suffix",
			args:  []string{"-p", "1", "-o", "pid=,nosuchcolumn=,ppid=,stat="},
			pid:   1,
			extra: tdnAssertPartialRowIsStillNotAReading,
		},
		{
			name: "B. a bare bad column with no = suffix",
			args: []string{"-p", "1", "-o", "nosuchcolumn"},
			pid:  1,
		},
		{
			name: "C. an illegal option",
			args: []string{"-Q", "-p", "1"},
			pid:  1,
		},
	}

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			stdout, err := tdnRunPS(arm.args)
			tdnAssertFailsSafe(t, arm.args, arm.pid, stdout, err)
			if arm.extra != nil {
				arm.extra(t, stdout, err)
			}
		})
	}

	t.Run("D. an out-of-range pid, confirmed rejected rather than assumed", func(t *testing.T) {
		// The threshold is PLATFORM-DEPENDENT and a hard-coded constant is a
		// portability trap: macOS caps pids at 99999 (measured — `ps -p 99999`
		// is a clean empty/empty absence and 100000 is the first rejection),
		// while Linux's default pid_max is 4194304, where 100000 is an ordinary
		// unused pid and this arm would silently become an absence test —
		// asserting instrument-failed against real bytes that correctly say
		// no-such-process. So the ladder escalates until ps actually rejects an
		// operand, and the rejection is confirmed rather than assumed.
		var tried []string
		for _, candidate := range tdnOutOfRangeLadder {
			args, classifyPID := tdnCandidateArgs(candidate)
			stdout, err := tdnRunPS(args)
			exitErr, ok := tdnExitError(t, args, err)
			if !ok {
				// Exit 0: the operand names a live process. Keep escalating.
				tried = append(tried, fmt.Sprintf("%s: exit 0, %d stdout bytes (a live pid)",
					candidate.operand, len(stdout)))
				continue
			}
			tried = append(tried, fmt.Sprintf("%s: exit %d, %d stdout bytes, %d stderr bytes",
				candidate.operand, exitErr.ExitCode(), len(stdout), len(exitErr.Stderr)))
			if len(exitErr.Stderr) == 0 {
				// Accepted as an ordinary unused pid: exit 1 with empty stdout
				// and empty stderr is a LEGITIMATE no-such-process on this
				// platform, not a rejection. Keep escalating.
				continue
			}
			t.Logf("#1250 arm D fired on candidate %s: %s", candidate.operand, tried[len(tried)-1])
			tdnAssertFailsSafe(t, args, classifyPID, stdout, err)
			return
		}
		t.Fatalf("no candidate in the ladder was rejected by ps, so this platform's "+
			"out-of-range arm was never exercised; every candidate reported:\n  %s",
			strings.Join(tried, "\n  "))
	})
}

// tdnAssertPartialRowIsStillNotAReading is arm A's addition: on a platform that
// prints the surviving columns, the stdout of a BROKEN ps parses as a
// well-formed row about pid 1 that pinIsZombie reads as running. Branch order
// alone stands between that and a fabricated `running` verdict.
//
// The parse is CONDITIONAL because the shape is Darwin-specific — Linux procps
// rejects an unknown -o specifier with empty stdout — so the portable
// assertions are the invariants in tdnAssertFailsSafe and this one records what
// the platform produced either way.
func tdnAssertPartialRowIsStillNotAReading(t *testing.T, stdout []byte, err error) {
	t.Helper()
	pid, ppid, state, ok := pinStateRow(stdout)
	if !ok {
		t.Logf("#1250 arm A: this platform printed %d bytes on stdout that parse to no "+
			"well-formed row; the partial-row shape is Darwin-specific", len(stdout))
		return
	}
	t.Logf("#1250 arm A: a BROKEN ps printed a row that parses as pid=%d ppid=%d state=%q "+
		"(zombie=%t) — a live-looking reading about pid 1, produced entirely by the "+
		"instrument's own breakage", pid, ppid, state, pinIsZombie(state))
	got := pinClassifyState(1, stdout, err)
	if got.Verdict != pinStateInstrumentFailed {
		t.Fatalf("a parseable row on the stdout of a failed ps classified as %q (%s); want %q — "+
			"stdout alongside an error is never parsed as process rows",
			got.Verdict, got.Detail, pinStateInstrumentFailed)
	}
	if got.StateColumn != "" {
		t.Errorf("instrument-failed outcome recorded state column %q; nothing was read",
			got.StateColumn)
	}
}

// tdnAssertFailsSafe asserts one arm's observed triple and then its verdict.
func tdnAssertFailsSafe(t *testing.T, args []string, pid int, stdout []byte, err error) {
	t.Helper()
	exitErr, ok := tdnExitError(t, args, err)
	if !ok {
		t.Fatalf("ps %s exited 0 with %d bytes on stdout; a mis-invocation must fail",
			strings.Join(args, " "), len(stdout))
	}
	if exitErr.ExitCode() <= 0 {
		t.Fatalf("ps %s reported exit code %d; want a normal non-zero exit (a negative code is a "+
			"signal, which is a different arm of the classifier)",
			strings.Join(args, " "), exitErr.ExitCode())
	}

	// THE load-bearing assertion. pinClassifyState's branch 1 is what keeps
	// branch 5 — the only producer of no-such-process — unreachable from a
	// broken instrument, and branch 1 fires on stderr and nothing else.
	if len(exitErr.Stderr) == 0 {
		t.Fatalf("ps %s exited %d with SILENT STDERR. Every branch that keeps a broken "+
			"instrument away from %q depends on ps writing there, so on this platform a "+
			"mis-invoked ps is byte-identical to a dead pid and the liveness read would "+
			"publish an absence out of its own breakage",
			strings.Join(args, " "), exitErr.ExitCode(), pinStateNoSuchProcess)
	}
	t.Logf("#1250 ps %s -> exit %d, %d stdout bytes, %d stderr bytes: %s",
		strings.Join(args, " "), exitErr.ExitCode(), len(stdout), len(exitErr.Stderr),
		reachCapCommand(strings.TrimSpace(string(exitErr.Stderr))))

	got := pinClassifyState(pid, stdout, err)
	if got.Verdict != pinStateInstrumentFailed {
		t.Fatalf("real bytes from `ps %s` classified as %q (%s); want %q, and NEVER %q or %q — "+
			"a half-run instrument must not publish a statement about the process",
			strings.Join(args, " "), got.Verdict, got.Detail, pinStateInstrumentFailed,
			pinStateNoSuchProcess, pinStateRunning)
	}
	if got.ToolStderr == "" {
		t.Errorf("outcome recorded no tool stderr although ps wrote %d bytes there; the "+
			"classifier reads a different channel from the one asserted above",
			len(exitErr.Stderr))
	}
	if !strings.Contains(got.Detail, "stderr") {
		t.Errorf("detail %q does not name stderr; branch 1 is the arm that must fire here, "+
			"because it is the one that precedes every parse", got.Detail)
	}
}

// tdnExitError extracts the *exec.ExitError from one ps result, failing the
// test on an error that is not one (ps missing, or never started).
func tdnExitError(t *testing.T, args []string, err error) (*exec.ExitError, bool) {
	t.Helper()
	if err == nil {
		return nil, false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("ps %s returned %T (%v), not an *exec.ExitError; ps never ran, so no premise "+
			"about its output could be tested", strings.Join(args, " "), err, err)
	}
	return exitErr, true
}

// tdnPIDCandidate is one rung of the out-of-range ladder. pid is 0 when the
// operand does not fit an int64, and the operand is carried as a string so a
// value past every integer type can still be handed to ps.
type tdnPIDCandidate struct {
	operand string
	pid     int64
}

// tdnOutOfRangeLadder escalates past both platforms' pid ceilings. Measured
// rejections on Darwin 25.5: 100000 (the first), 4194305, 2147483647,
// 4294967296 — all `ps: process id too large`. 99999 is NOT here: it is a clean
// empty/empty absence on macOS, which is a legitimate no-such-process.
var tdnOutOfRangeLadder = []tdnPIDCandidate{
	{operand: "100000", pid: 100000},
	{operand: "4194305", pid: 4194305},
	{operand: "2147483648", pid: 2147483648},
	{operand: "4294967296", pid: 4294967296},
	{operand: "9223372036854775807", pid: 9223372036854775807},
	{operand: "99999999999999999999"},
}

// tdnCandidateArgs builds one rung's argument list, using the PRODUCTION list
// wherever the candidate fits an int so this arm also exercises pinStateArgs
// verbatim. The `int64(int(pid)) == pid` round-trip keeps the ladder portable
// to a 32-bit int, where the upper rungs would otherwise not be representable.
func tdnCandidateArgs(c tdnPIDCandidate) (args []string, classifyPID int) {
	if c.pid > 0 && int64(int(c.pid)) == c.pid {
		return pinStateArgs(int(c.pid)), int(c.pid)
	}
	return []string{"-p", c.operand, "-o", pinStateColumns}, 0
}

// tdnRunPS execs one ps and returns exactly what pinReadState's own call
// returns (process_pin_liveness_test.go:293).
//
// .Output() and nothing else. It populates *exec.ExitError.Stderr ONLY because
// it owns cmd.Stderr; a helper that set cmd.Stderr = &buf to "capture stderr
// for the assertion" would leave ExitError.Stderr empty, and the classifier
// would then see exit 1 with empty stdout and empty stderr — branch 5, the very
// arm this test exists to prove unreachable. The premise has to be asserted on
// the bytes the consumer consumes, or the assertion is about a different
// channel.
func tdnRunPS(args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), reachPSTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "ps", args...).Output()
}

// --- self-checks: the record and its redaction-safe writer -------------------

func TestTdnRecordWriter(t *testing.T) {
	rec := tdnFixtureRecord()
	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}

	t.Run("the marshalled record carries no environment read", func(t *testing.T) {
		// Asserted over the MARSHALLED RECORD, never over this file's source:
		// arms B and C above legitimately contain the strings `nosuchcolumn` and
		// `-Q`, so a source-level grep tripwire would be checking the wrong
		// artifact.
		for _, forbidden := range []string{"environ", "ps -E", "-eww", "eww"} {
			if strings.Contains(string(blob), forbidden) {
				t.Errorf("the record carries %q; `ps -E` / `ps -e <env column>` / BSD `ps eww` "+
					"print each process's full ENVIRONMENT, which on an operator machine means "+
					"CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY, into an artifact destined for "+
					"a public issue", forbidden)
			}
		}
	})

	t.Run("a command string reaches the record only from the argv scan", func(t *testing.T) {
		// The positive half first: the allowed source genuinely publishes one,
		// so the negative assertions below are not vacuous.
		scan, err := json.Marshal(rec.ArgvScan)
		if err != nil {
			t.Fatalf("marshal argv scan: %v", err)
		}
		if !strings.Contains(string(scan), `"command"`) {
			t.Fatalf("the argv scan published no command at all, so the checks below prove "+
				"nothing: %s", scan)
		}

		// The narrow per-pid read's column set is pinned by
		// TestPinStateColumns_ReadsNoEnvironment (:950) and is not restated
		// here. This is the record-level tripwire against a FUTURE field: it
		// holds structurally today because none of the three types has one.
		for _, part := range []struct {
			name string
			v    any
		}{
			{name: "liveness (the narrow per-pid read)", v: rec.Liveness},
			{name: "fifo", v: rec.FIFO},
			{name: "reap", v: rec.Reap},
		} {
			b, err := json.Marshal(part.v)
			if err != nil {
				t.Fatalf("marshal %s: %v", part.name, err)
			}
			if strings.Contains(string(b), `"command"`) {
				t.Errorf("%s carries a command key: %s\ncommand may reach the record only from "+
					"the argv scan that already publishes matched rows, never from the narrow "+
					"per-pid read", part.name, b)
			}
		}
	})

	t.Run("the writer emits exactly one file, mode 0600", func(t *testing.T) {
		dir := t.TempDir()
		writeTdnArtifacts(t, dir, rec)

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read artifact dir: %v", err)
		}
		// "Exactly one file" IS the redaction assertion. writeReachArtifacts
		// writes a second file holding a verbatim three-integer ps snapshot;
		// this ticket takes no wide integer snapshot at all, and its one wide
		// read never lets its raw table out of reachScanArgv's frame, so no
		// verbatim ps output is persisted anywhere.
		if len(entries) != 1 {
			t.Fatalf("artifact dir holds %d entries (%v); want exactly one — no verbatim ps "+
				"output is persisted by this writer", len(entries), entries)
		}
		if entries[0].Name() != tdnArtifactName {
			t.Errorf("artifact name: got %q, want %q", entries[0].Name(), tdnArtifactName)
		}

		info, err := os.Stat(filepath.Join(dir, entries[0].Name()))
		if err != nil {
			t.Fatalf("stat artifact: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("artifact mode: got %04o, want 0600", perm)
		}

		written, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		if err != nil {
			t.Fatalf("read artifact: %v", err)
		}
		var round tdnRecord
		if err := json.Unmarshal(written, &round); err != nil {
			t.Fatalf("the artifact is not valid JSON: %v\n%s", err, written)
		}
		if round.Reap.Verdict != rec.Reap.Verdict || round.Liveness.Verdict != rec.Liveness.Verdict {
			t.Errorf("round-tripped record lost its verdicts: reap %q/%q liveness %q/%q",
				round.Reap.Verdict, rec.Reap.Verdict, round.Liveness.Verdict, rec.Liveness.Verdict)
		}
	})
}

// tdnFixtureRecord builds a record with every field non-zero, composed from the
// real classifiers rather than from literals: a redaction check over a record
// nobody populated proves nothing.
func tdnFixtureRecord() *tdnRecord {
	fifo := fifoLiveClassifyOpenErr(syscall.ENXIO)
	fifo.Path = "/tmp/pyry-1250-fixture/teardown-hold"
	fifo.Mode = os.ModeNamedPipe.String()

	rec := &tdnRecord{
		Ticket:   tdnTicket,
		HeldPGID: tdnFixtureHeldPGID,
		HeldPID:  pinFixtureOwnPID,
		ArgvScan: pinMatchArgvExcluding([]byte(pinArgvFixture), []string{pinFixtureNeedle},
			map[int]string{pinFixtureOwnPID: "the instrument's own test binary"}),
		Liveness: pinClassifyState(4242, []byte("4242 1 S\n"), nil),
		FIFO:     fifo,
		Reap:     tdnClassifyReapLog([]byte(tdnFixtureDefaultTwo), tdnFixtureHeldPGID),
	}
	rec.note("fixture record for %s's redaction self-check; no live turn was taken", tdnTicket)
	return rec
}

// --- test helpers ------------------------------------------------------------

// tdnIsReapVerdict reports whether v is one of the recorded values.
func tdnIsReapVerdict(v string) bool {
	switch v {
	case tdnReapHeldPGIDKilled, tdnReapHeldPGIDAbsent, tdnReapNoLine, tdnReapInstrumentFailed:
		return true
	}
	return false
}

func tdnEqualInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
