//go:build e2e_realclaude

package realclaude

// The publishable record for one probe run: its process evidence, its liveness
// verdicts, its reap-log attribution, and the runner path it actually took.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence. Everything here runs offline: no live
// claude, no credentials, no network, no daemon, no subject process, no
// process-table read, no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinRecord' -v ./internal/e2e/realclaude/
//
// # Nothing on this path may exec, and the check for that has to name symbols
//
// A grep for the os/exec package selector is NOT sufficient on its own: every
// route off this offline path runs through a SHIPPED HELPER that execs
// internally rather than through a visible call to that package. The version
// probe (`probeClaudeVersion`)
// execs the binary, which is why the version here is a caller-supplied string;
// the claude-binary resolver (`resolveClaudeBin`) skips when claude is
// absent, and a skip that exits 0 reads as a pass under `make e2e-realclaude`;
// the worktree credentials gate (`WithWorktreeAuthenticated`) is AC5's "no credentials"; the
// per-pid state read (`pinReadState`) execs `ps` at :293; the
// exit-1 borrow (:1088) execs `false` to obtain an *os.ProcessState Go cannot
// synthesize; and the argv scan (:191), the process snapshot
// (`probeProcessSnapshot`), the teardown scan
// (`tdnScan`) and the FIFO hold
// (`holdProbeFIFO`) each reach a process or the table.
//
// EVERY ONE OF THEM IS REFERENCED ABOVE BY FILE AND LINE RATHER THAN BY NAME, so
// that the forbidden-symbol grep reports on this file's CODE and cannot be
// defeated by this file's own prose. A check that cannot report clean is as
// useless as one that cannot fail — #1290's spec wrote a bare `t.Skip` grep that
// matched that file's own header sentence and so could never come back empty.
//
// Pure over bytes and therefore admissible if ever needed, though this design
// needs none of them: pinMatchArgvExcluding (:173), probeDescendantsFromPS
// (`probeDescendantsFromPS`), pinClassifyState (:332).
//
// # Two properties, and only one of them is structural
//
// THE RECORD IS TRAP-FREE BY CONSTRUCTION. No field can hold an argv —
// finRecordProc is three ints — and trailScanResult.Trailer is unreachable
// because finRecordInputs carries neither a trailObservation
// (`trailObservation`) nor a trailScanResult (:98). That is
// the property trailRunReadings.BoundFrom's comment states as its own reason for
// taking a plain value (trail_run_outcome_test.go:425-431): taking the
// observation "would promote that pointer back into reach". It is the STRONGER
// property #1290 could not buy; #1320 bought it — finTrailerBuild's input is now a
// finSighting carrying neither .Line nor the pointer, as finTrailerBuild's doc says.
//
// THE BUILDER IS NOT. in.Rows[i].Command and in.ClaudeCommand are verbatim argv,
// in reach inside finRecordBuild. The no-captured-bytes property across the
// CONVERSION is held by the Detail content rule plus
// TestFinRecordCarriesNoCapturedBytes — NOT finTrailerBuild's posture now, and
// it is said in both places so that neither claim is read as covering the other.
//
// # Reused, not rebuilt
//
// finTrailerBuild / finTrailerRecord (#1290, finding_trailer_evidence_test.go)
// and finAttributeFanOut / finAttributeRecord (#1280) are embedded WHOLE: the
// trailer observation is never re-read, the outcome union is never re-derived,
// and pyry's stderr is never re-parsed — tdnClassifyReapLog
// (`tdnClassifyReapLog`) owns that read and the fan-out is its
// consumer. tdnRunnerFromArgv is the argv
// read, with tdnFixturePtyArgv (:897) and tdnFixtureStreamArgv (:902) its
// shipped fixtures; reachRunnerPathFromEnv
// is the env read. trailReapLine (`trailAdmitAttribution`) renders the
// synthetic reap stderr, trailNeedle is
// the plant, and reachMaxCommandBytes / reachCapCommand
// (`reachEnableEnv`, :945) are the single-sourced cap.
//
// trailDetail (`trailGateInput`) is reused rather than given a
// finDetail twin, for the reason merged code has settled twice
// (finding_attribution_fanout_test.go:37-44, finding_staging_gate_test.go:73-81):
// it carries no decision — fmt.Sprintf plus reachCapCommand's 512-byte cap — and
// a twin would only fork the cap.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- the row type ---------------------------------------------------------------

// finRecordProc is one matched row's identity: three integers and NOTHING ELSE.
// No command string, no needle list, no reachProc.
//
// A field typed []reachProc would pass every test in this file while keeping
// argv one edit away — reachProc carries Command and Needles alongside the three
// integers (background_reach_probe_test.go:162-168), and a ps command column is
// how an operator's CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY reaches an
// artifact destined for a public issue. finAttributeEntry
// (finding_attribution_fanout_test.go:80-88) is the precedent: it holds only
// what it may publish, so its no-captured-bytes property is true BY CONSTRUCTION
// rather than by an ordering discipline a later edit can break.
//
// The three integers are not a compromise reached by giving something up. A live
// run of this shape matches TWO rows — the shell wrapper claude runs Bash
// through, and the command itself — and they share a pgid with one the other's
// parent, which is exactly what distinguishes wrapper from command. A reader of
// the issue can check that from the integers; they could not check it from the
// bare count 2, which is all trailRunOutcome.MatchCount publishes
// (trail_run_outcome_test.go:521-524). Widening this type to the argv would put
// model-chosen text into the artifact and contradict the rule that comment and
// trailRunReadings.MatchCount (:411-414) both state.
type finRecordProc struct {
	PID  int `json:"pid"`
	PPID int `json:"ppid"`
	PGID int `json:"pgid"`
}

// --- the record ------------------------------------------------------------------

// finRecordRun is one probe run's publishable record.
//
// # omitempty, where it is and where it must not be
//
// The two slices carry it, in finAttributeRecord's shape
// (finding_attribution_fanout_test.go:98-105). THE SCALARS DO NOT, and ExitCode
// is the load-bearing one: 0 is a REAL SUCCESSFUL EXIT, so under omitempty a
// clean run would render byte-identically to a run whose exit was never
// observed. A caller with no observed exit hands pinExitStatusUnknown
// (`pinExitStatusUnknown`) instead — a documented caller obligation,
// not a validated one, because no builder in this family validates its inputs
// and no such miswrite has been observed. If a live run ever publishes
// exit_code: 0 for a pyry that did not exit, the fix is a PyryExited bool beside
// it, mirroring trailRunReadings.PyryExited (trail_run_outcome_test.go:421-424)
// whose zero value points the safe way — not validation inside the builder.
//
// # What is carried whole, and why that is safe
//
// Attribution and Trailer are both embedded whole and both are documented
// trap-free by their own enforcing tests (TestFinAttributeRecordCarriesNoCapturedBytes,
// TestFinTrailerRecordCarriesNoCapturedBytes). Neither is re-derived and neither
// is re-read. Liveness is []pinStateOutcome carried whole for
// trailRunReadings.Liveness' stated reason (:417-420): "pinStateOutcome carries
// no command column by construction" — pinStateColumns is `pid=,ppid=,stat=` and
// carries an explicit never-add-command/args/comm prohibition with an enforcing
// test (process_pin_liveness_test.go:221-232). Its PID and PPID are what tie
// each verdict to its row, so AC1's "which row it belongs to" needs no new
// field; do not add a row-index and do not widen the column set. ToolStderr
// stays on the carried outcome: #1281 settled that a forbidden-key sweep meeting
// that key defuses by EXACT-KEY EXEMPTION, never a prefix rule, and that sweep
// is #1286's, not a reason to strip the field here.
//
// # The Detail's content rule, pinned rather than left to judgement
//
// In trailRunOutcome.Detail's shape (trail_run_outcome_test.go:465-475) and
// finAttributeRecord.Detail's (finding_attribution_fanout_test.go:116-124), it
// MAY name: the exit code, the row and liveness COUNTS, the three runner
// readings and their agreement verdict, the attribution's selected admissibility
// value, and the carried outcome. It may NEVER quote: an argv; a
// pinStateOutcome's Detail or ToolStderr; an entry's Admit.Detail; the embedded
// finTrailerRecord.Detail or finAttributeRecord.Detail; or pyry's stderr.
//
// QUOTING A SUB-RECORD'S DETAIL IS THE LIKELIEST SLIP HERE. #1280 names it for
// one sub-record; this record embeds two, it reads as helpful context, and it
// both duplicates a string the record already carries and spends the 512-byte
// cap on it.
//
// THE CONCRETE MECHANISM THE ARGV PROHIBITION GUARDS AGAINST IS A %v VERB
// APPLIED TO AN INPUT. trailDetail("... rows: %v", in.Rows) renders []reachProc
// including every Command — the whole verbatim argv of every matched row,
// straight into the published string, from a line that reads as ordinary debug
// formatting. Same for %v on `in` itself or on in.Liveness. Format the DERIVED
// SCALARS and never an input struct or an input slice.
//
// NAME COUNTS, NOT EVERY PID. The record already publishes every row's three
// integers as fields, so enumerating them in the Detail adds nothing a reader
// cannot already see and makes the Detail's length grow with the input — which
// is what would turn the headroom below from a structural property into a
// property of how many rows happened to be handed in.
//
// Every Detail must leave len(trailNeedle) bytes of headroom under
// reachMaxCommandBytes — UNDER 470 BYTES ON EVERY ROW. That is not stylistic: a
// Detail that had wrongly interpolated an argv would be truncated before the
// needle if the surrounding prose left no room, and AC4's containment checks
// would then pass against a leaking implementation. That is the defect #1284
// shipped and had to fix. The long-form argument belongs in a comment, which no
// cap applies to.
type finRecordRun struct {
	ExitCode        int                `json:"exit_code"`
	Rows            []finRecordProc    `json:"matched_rows,omitempty"`
	Liveness        []pinStateOutcome  `json:"liveness,omitempty"`
	Attribution     finAttributeRecord `json:"attribution"`
	Trailer         finTrailerRecord   `json:"trailer"`
	RunnerFromEnv   string             `json:"runner_from_env"`
	RunnerFromArgv  string             `json:"runner_from_argv"`
	RunnerAgreement string             `json:"runner_agreement"`
	ClaudeVersion   string             `json:"claude_version"`
	Detail          string             `json:"detail"`
}

// --- the builder's input ---------------------------------------------------------

// finRecordInputs is one run's readings.
//
// NAMED FIELDS RATHER THAN POSITIONAL PARAMETERS, which departs from
// finAttributeFanOut and finTrailerBuild for one reason: four of the eight
// inputs are strings, and two of them — RunnerFromEnv and ClaudeCommand — are
// adjacent, same-typed, and on OPPOSITE SIDES OF THE ARGV PROHIBITION.
// Transposing them positionally would write verbatim claude argv into
// runner_from_env and publish it, with nothing going red. Named fields make that
// transposition a compile error. Same doctrine as finRecordProc: prefer the
// shape that cannot be got wrong over the discipline that must not be.
//
// THERE IS NO trailObservation FIELD AND NO trailScanResult FIELD, and that
// absence is the whole of AC2's structural half — it is what puts
// trailScanResult.Trailer out of reach of this record. That pointer is nil
// unless State == trailSeen and a consumer dereferencing it without checking
// panics loudly (result_trailer_observation_test.go:108-118), so a record able
// to reach it would inherit an obligation it has no way to discharge.
// TestFinRecordEmbedsTrailerRecordWhole walks this type's fields so a later edit
// cannot add one silently.
type finRecordInputs struct {
	ExitCode int
	// Rows is the argv-bearing scan output, reduced to three integers per row at
	// the single conversion point in finRecordBuild. Command and Needles are
	// dropped there and reach no field of the built record.
	Rows        []reachProc
	Liveness    []pinStateOutcome
	Attribution finAttributeRecord
	Trailer     finTrailerRecord
	// RunnerFromEnv is what the rig itself set, so it can only ever report the
	// rig's own INTENT. Recorded as documentation, not as corroboration — see
	// finRecordBuild.
	RunnerFromEnv string
	// ClaudeCommand is the claude child's own argv. READ, reduced to one of
	// tdnRunnerFromArgv's three constant answers, and NEVER RETAINED anywhere in
	// the record.
	ClaudeCommand string
	// ClaudeVersion is caller-supplied and capped on the way in — see
	// finRecordBuild.
	ClaudeVersion string
}

// --- the runner-path verdict -----------------------------------------------------

// The three-valued agreement verdict. indeterminate is a THIRD ANSWER and not a
// disagreement: it says the argv was not read, not that the run took the other
// path. Collapsing it into disagree would publish a claim about the runner that
// no reading supports.
const (
	finRecordRunnerAgrees        = "agree"
	finRecordRunnerDisagrees     = "disagree"
	finRecordRunnerIndeterminate = "indeterminate"
)

// finRecordRunnerLabel returns the leading token before the parenthesised
// reason.
//
// THE DEGENERATE PATH FAILS TOWARD disagree. Given a reading with no " (", the
// whole string comes back. Both shipped producers always emit a parenthesised
// reason — TestTdnRunnerFromArgv:957 asserts it — so this path is defensive
// only, and the safe direction is that two whole strings compare unequal
// (disagree) rather than collapsing to a false agreement.
//
// (The spec cites that assertion as :957 and the prefix match below as :951;
// both are one line off in merged code — they are at :956 and :952.)
func finRecordRunnerLabel(reading string) string {
	if i := strings.Index(reading, " ("); i >= 0 {
		return reading[:i]
	}
	return reading
}

// finRecordRunnerAgreement compares two runner readings BY LABEL ALONE.
//
// # Why not the whole strings
//
// The two readings do not share a vocabulary, and that decides how they are
// compared. The env read answers "ptyrunner (interactive TUI, the agent-run
// default)" (`reachRunnerPathFromEnv`) while an AGREEING argv read
// answers "ptyrunner (claude argv carries --session-id)"
// (`tdnRunnerFromArgv`). The two full strings are therefore
// NEVER EQUAL, not even when both name the same runner — so a record comparing
// them whole would report a disagreement on every run, and AC3's disagreement
// row would pass while discriminating nothing.
//
// # The indeterminate arm is checked FIRST
//
// Ordering it first is what makes "a third answer, never a disagreement"
// structural rather than incidental. Only the ARGV side can be indeterminate:
// reachRunnerPathFromEnv returns exactly two values by construction (:1109-1112),
// so there is deliberately no dead arm for an indeterminate env reading.
//
// # Exact equality, not a prefix match
//
// TestTdnRunnerFromArgv:952 uses strings.HasPrefix against a KNOWN-EXPECTED
// label, which is correct there. Here both operands are unknown at compile time.
// Over the closed space {ptyrunner, streamrunner, indeterminate} the two happen
// to agree, but prefix-matching two unknowns is the wrong primitive for the
// claim and should not be copied across.
func finRecordRunnerAgreement(fromEnv, fromArgv string) string {
	// The argv label and the verdict share one constant deliberately:
	// tdnRunnerFromArgv's own third answer IS "indeterminate" (:774, :781, :788),
	// so a second spelling of the same word would be a fork waiting to drift.
	argv := finRecordRunnerLabel(fromArgv)
	if argv == finRecordRunnerIndeterminate {
		return finRecordRunnerIndeterminate
	}
	if argv == finRecordRunnerLabel(fromEnv) {
		return finRecordRunnerAgrees
	}
	return finRecordRunnerDisagrees
}

// --- the builder -----------------------------------------------------------------

// finRecordBuild projects one run's readings onto the record the probe
// publishes.
//
// Pure over its inputs: no exec, no clock, no filesystem, no *testing.T, and it
// never fails a test — the same contract as trailScan, trailGate,
// trailAdmitAttribution, trailClassifyRun, finOutcomeStagingGate,
// finTrailerBuild, finAttributeFanOut, tdnClassifyReapLog and the per-pid state
// read (process_pin_liveness_test.go:265-275), because an instrument failure
// observed mid-turn is a datum to publish, not a reason to abort the turn.
//
// # The runner path is recorded AS OBSERVED, not as intended
//
// reachRunnerPathFromEnv reads the env the rig itself set, so it can only ever
// report the rig's own intent. It is carried into the RECORD as documentation
// rather than as corroboration — stated in the artifact and not only in a
// comment, exactly as #1230's record does (background_reach_probe_test.go:346-349)
// — so a reader is not misled into counting two agreeing reads.
//
// The evidential read is tdnRunnerFromArgv and never reachRunnerPathFromArgv
// (:1118): the latter keys on --append-system-prompt-file and calls it "the
// ptyrunner-shape marker", but buildStreamRunnerClaudeArgs
// (cmd/pyry/`buildStreamRunnerClaudeArgs`) emits the identical flag alongside ptyrunner's
// buildArgs (internal/agentrun/ptyrunner/runner.go:621), so it answers
// "ptyrunner" on BOTH paths and a silent switch to the other runner reads as a
// correct label with nothing going red.
//
// # ClaudeVersion is capped on the way in
//
// The family's rule is that every retained operator-visible string is capped
// (trailer_admissibility_test.go:262-265), and this is the one such string the
// record would otherwise retain uncapped. The live caller is the version probe,
// whose error path returns fmt.Sprintf("<unavailable: %v>", err)
// (`probeClaudeVersion`) — an exec error interpolating the
// RESOLVED BINARY PATH, i.e. an operator's home directory, into a record
// destined for a public issue. Capping is one call and costs no API.
//
// # What is copied and what is not
//
// Rows is converted into a fresh []finRecordProc, which drops Command and
// Needles at the single conversion point. Liveness carries the caller's slice
// HEADER, matching trailRunReadings.Liveness' shipped precedent — that is fine,
// and it is deliberately not described as a defensive copy, because it is not
// one.
func finRecordBuild(in finRecordInputs) finRecordRun {
	rec := finRecordRun{
		ExitCode:      in.ExitCode,
		Liveness:      in.Liveness,
		Attribution:   in.Attribution,
		Trailer:       in.Trailer,
		RunnerFromEnv: in.RunnerFromEnv,
		ClaudeVersion: reachCapCommand(in.ClaudeVersion),
	}

	// THE FIRST OF THE TWO PLACES ARGV ENTERS, and its only outbound edge carries
	// three integers.
	for _, row := range in.Rows {
		rec.Rows = append(rec.Rows, finRecordProc{PID: row.PID, PPID: row.PPID, PGID: row.PGID})
	}

	// THE SECOND. Every arm of tdnRunnerFromArgv returns a CONSTANT
	// (teardown_liveness_probe_test.go:773-789); no input byte reaches its return,
	// so the argv is reduced here and retained nowhere.
	rec.RunnerFromArgv = tdnRunnerFromArgv(in.ClaudeCommand)
	rec.RunnerAgreement = finRecordRunnerAgreement(rec.RunnerFromEnv, rec.RunnerFromArgv)

	// Counts, labels and closed-set values only — never a row, never a pid list,
	// never an input struct under %v, and never an embedded sub-record's Detail.
	// The LABELS rather than the full readings, because the record already
	// publishes both readings whole and the reasons would only spend the cap.
	rec.Detail = trailDetail("pyry exited %d; %d matched row(s) and %d liveness read(s); runner "+
		"%s by env and %s by claude argv, which %s; attribution selected %s; trailer outcome %s",
		rec.ExitCode, len(rec.Rows), len(rec.Liveness),
		finRecordRunnerLabel(rec.RunnerFromEnv), finRecordRunnerLabel(rec.RunnerFromArgv),
		rec.RunnerAgreement, rec.Attribution.Selected.Value, rec.Trailer.Outcome)
	return rec
}

// --- fixtures ----------------------------------------------------------------------

// The two rows a live run of this shape matches, and the claude row above them.
// The wrapper is claude's child, the command is the wrapper's, and both share the
// wrapper's pgid — the relationship the three published integers exist to let a
// reader of the issue check.
const (
	finRecordClaudePID  = 7700
	finRecordWrapperPID = 7788
	finRecordCommandPID = 7791
	finRecordSharedPGID = 7788
)

// finRecordMatchedRows returns those two rows with suffix appended to each
// Command, so AC1 can drive clean argv and AC4 can drive a planted one through
// the same shape.
//
// Command is populated because reachProc's content-matched rows carry it
// (background_reach_probe_test.go:159-168). It is exactly what the conversion
// drops.
//
// A function rather than a package-level var, for trailRunWellFormed's stated
// reason (trail_run_outcome_test.go:1111-1113): a shared backing array is
// reachable from every test in this package, and `go test -race` runs them in
// parallel.
func finRecordMatchedRows(suffix string) []reachProc {
	return []reachProc{
		{
			PID: finRecordWrapperPID, PPID: finRecordClaudePID, PGID: finRecordSharedPGID,
			Command: "/bin/zsh -c sleep 90" + suffix,
		},
		{
			PID: finRecordCommandPID, PPID: finRecordWrapperPID, PGID: finRecordSharedPGID,
			Command: "sleep 90" + suffix,
		},
	}
}

// finRecordEnvDelta names PYRY_USE_STREAMJSON EXPLICITLY rather than relying on
// it being unset. reachRunnerPathFromEnv reads the ambient os.Getenv FIRST and
// only then lets the delta override it (background_reach_probe_test.go:1103-1108),
// so an empty delta would make every env-side reading below a reading of the
// OPERATOR'S SHELL rather than of this test.
func finRecordEnvDelta() []string {
	return []string{"PYRY_USE_STREAMJSON=0"}
}

// finRecordFixtureNeitherArgv carries --append-system-prompt-file and NEITHER
// discriminating marker. Both runners emit that flag
// (teardown_liveness_probe_test.go:891-895), which is why it cannot name a
// runner — and it is what makes the indeterminate row below bite: against
// reachRunnerPathFromArgv this argv answers "ptyrunner", which would agree with
// the env reading and publish a runner claim no reading supports.
const finRecordFixtureNeitherArgv = `/opt/node/bin/node /opt/claude/cli.js ` +
	`--append-system-prompt-file /tmp/wd/system.txt --model claude-haiku-4-5`

// finRecordProofAttribution and finRecordVoidAttribution are two fan-outs
// PRODUCED by the shipped #1280 fan-out over a synthetic reap line rather than
// hand-assembled, so no row asserts against a sub-record finAttributeFanOut
// would not actually return. The first names the pinned group, the second does
// not.
func finRecordProofAttribution() finAttributeRecord {
	return finAttributeFanOut([]byte(trailReapLine(1, "[7788]")+"\n"),
		[]int{finRecordSharedPGID}, "completed")
}

func finRecordVoidAttribution() finAttributeRecord {
	return finAttributeFanOut([]byte(trailReapLine(1, "[7788]")+"\n"), []int{4242}, "completed")
}

// finRecordSeenTrailer and finRecordAbsentTrailer are two sub-records built by
// the shipped #1290 builder over the shipped scan, for the same reason.
func finRecordSeenTrailer() finTrailerRecord {
	return finTrailerBuild(trailOutcomeRunningAtTrailer,
		finTrailerSighting(trailScan([]byte(trailFixtureTrailer+"\n")), 250*time.Millisecond,
			trailBoundFromMiss))
}

func finRecordAbsentTrailer() finTrailerRecord {
	return finTrailerBuild(trailOutcomeVoidNoTrailer,
		finTrailerSighting(trailScan([]byte(trailFixtureNoTrailer)), 0, trailBoundNone))
}

// finRecordLivenessValues returns the four pinState* values by NAMING THE
// SHIPPED CONSTANTS (process_pin_liveness_test.go:204-219) rather than restating
// their strings, so a renamed constant is a compile error rather than a silently
// stale literal.
//
// A function rather than a package-level var, for the reason above.
func finRecordLivenessValues() []string {
	return []string{
		pinStateRunning,
		pinStateExitedNotReaped,
		pinStateNoSuchProcess,
		pinStateInstrumentFailed,
	}
}

// --- tests -------------------------------------------------------------------------

// TestFinRecordCarriesEveryMatchedRow drives the two rows a live run of this
// shape matches and pins that BOTH survive into the record with their three
// integers, that the relationship between them survives, and that every liveness
// verdict lands inside the published row set.
func TestFinRecordCarriesEveryMatchedRow(t *testing.T) {
	rows := finRecordMatchedRows("")
	in := finRecordInputs{
		ExitCode: 0,
		Rows:     rows,
		Liveness: []pinStateOutcome{
			{
				Verdict: pinStateRunning, PID: finRecordWrapperPID, PPID: finRecordClaudePID,
				StateColumn: "Ss", ExitStatus: 0, Detail: "hand-built: a live wrapper",
			},
			{
				Verdict: pinStateRunning, PID: finRecordCommandPID, PPID: finRecordWrapperPID,
				StateColumn: "S", ExitStatus: 0, Detail: "hand-built: a live command",
			},
		},
		Attribution:   finRecordProofAttribution(),
		Trailer:       finRecordSeenTrailer(),
		RunnerFromEnv: reachRunnerPathFromEnv(finRecordEnvDelta()),
		ClaudeCommand: tdnFixturePtyArgv,
		ClaudeVersion: "2.1.220 (Claude Code)",
	}

	rec := finRecordBuild(in)

	// The count #1268 mutation-tested: dropping the wrapper cut it 2 -> 1.
	if len(rec.Rows) != len(rows) {
		t.Fatalf("matched rows: got %d, want %d — a run of this shape matches the shell wrapper "+
			"AND the command it launched, and a record publishing only one of them cannot be "+
			"checked by a reader of the issue", len(rec.Rows), len(rows))
	}

	byPID := make(map[int]finRecordProc, len(rec.Rows))
	for _, row := range rec.Rows {
		byPID[row.PID] = row
	}
	for _, want := range rows {
		got, ok := byPID[want.PID]
		if !ok {
			t.Fatalf("no published row for pid %d: every matched row is carried, not a "+
				"representative one", want.PID)
		}
		if got.PPID != want.PPID || got.PGID != want.PGID {
			t.Errorf("row pid=%d: got ppid=%d pgid=%d, want ppid=%d pgid=%d — the three integers "+
				"carry the whole claim and are transferred unaltered",
				got.PID, got.PPID, got.PGID, want.PPID, want.PGID)
		}
	}

	// THE RELATIONSHIP, not merely the values. That the two rows share a pgid and
	// that one is the other's parent is what distinguishes wrapper from command,
	// and it is the reason the record publishes integers rather than the count.
	wrapper, command := byPID[finRecordWrapperPID], byPID[finRecordCommandPID]
	if wrapper.PGID != command.PGID {
		t.Errorf("pgid: wrapper %d, command %d — the two rows of a still-running Bash command "+
			"share a group, and that is half of what tells them apart from an unrelated match",
			wrapper.PGID, command.PGID)
	}
	if command.PPID != wrapper.PID {
		t.Errorf("parentage: command ppid=%d, wrapper pid=%d — the other half; without it a "+
			"reader cannot tell which row is the wrapper", command.PPID, wrapper.PID)
	}

	// THE CROSS-REFERENCE, NOT THE ECHO. Checking only that the handed-in outcomes
	// come back is a tautology over a pass-through field; checking that each lands
	// inside the published row set is the claim AC1 makes.
	published := make(map[int]bool, len(rec.Rows))
	for _, row := range rec.Rows {
		published[row.PID] = true
	}
	if len(rec.Liveness) != len(in.Liveness) {
		t.Fatalf("liveness reads: got %d, want %d", len(rec.Liveness), len(in.Liveness))
	}
	for i, out := range rec.Liveness {
		if !published[out.PID] {
			t.Errorf("liveness[%d] is about pid %d, which appears in no published row: a verdict "+
				"a reader cannot tie to a row states nothing. pinStateOutcome already carries PID "+
				"and PPID (process_pin_liveness_test.go:245-253), so the tie needs no new field — "+
				"but only while the pids land inside the row set", i, out.PID)
		}
	}

	if rec.ExitCode != in.ExitCode {
		t.Errorf("exit code: got %d, want %d", rec.ExitCode, in.ExitCode)
	}
	if rec.ClaudeVersion != in.ClaudeVersion {
		t.Errorf("claude version: got %q, want %q — caller-supplied and carried",
			rec.ClaudeVersion, in.ClaudeVersion)
	}

	// THE VERSION'S CAP, which the assertion above cannot reach with a short
	// string. The live caller's error path returns fmt.Sprintf("<unavailable:
	// %v>", err) (`probeClaudeVersion`) — an exec error
	// interpolating the RESOLVED BINARY PATH, i.e. an operator's home directory,
	// into a record destined for a public issue. It is the one retained
	// operator-visible string this record would otherwise hold uncapped, against
	// the family's rule that every one of them is capped
	// (trailer_admissibility_test.go:262-265).
	overlong := "<unavailable: exec: " + strings.Repeat("/home/operator/a/long/path", 40) + ">"
	if len(overlong) <= reachMaxCommandBytes {
		t.Fatalf("the overlong version fixture is %d bytes, inside the %d-byte cap, so the check "+
			"below would pass against a builder that dropped the cap entirely", len(overlong),
			reachMaxCommandBytes)
	}
	capped := finRecordBuild(finRecordInputs{ClaudeVersion: overlong}).ClaudeVersion
	if capped == overlong || !strings.HasSuffix(capped, reachTruncationMarker) {
		t.Errorf("claude version: %d bytes carried out of %d handed in, ending %q — the caller's "+
			"string is capped on the way in, because an unbounded one is how a resolved binary "+
			"path reaches a published record", len(capped), len(overlong), capped)
	}
	if rec.Attribution.Selected.Value != in.Attribution.Selected.Value {
		t.Errorf("attribution: got %q, want %q — the fan-out's record is carried whole, never "+
			"rebuilt", rec.Attribution.Selected.Value, in.Attribution.Selected.Value)
	}
	if rec.Trailer.Outcome != in.Trailer.Outcome {
		t.Errorf("trailer outcome: got %q, want %q", rec.Trailer.Outcome, in.Trailer.Outcome)
	}
	if rec.Detail == "" {
		t.Error("empty detail: a record that cannot say what it recorded is indistinguishable " +
			"from a reading that never happened")
	}

	// AC1's "and never its argv", made STRUCTURAL rather than argued: the row type
	// is three ints and nothing else. A []reachProc field would satisfy every
	// assertion above while keeping Command one edit away.
	rowType := reflect.TypeOf(finRecordProc{})
	if rowType.NumField() != 3 {
		t.Errorf("finRecordProc has %d field(s), want 3", rowType.NumField())
	}
	for i := 0; i < rowType.NumField(); i++ {
		if f := rowType.Field(i); f.Type.Kind() != reflect.Int {
			t.Errorf("finRecordProc.%s is %s: the row type is three integers, which is what makes "+
				"the argv prohibition true by construction rather than by an ordering discipline "+
				"a later edit can break", f.Name, f.Type)
		}
	}
}

// TestFinRecordLivenessIsConsumedAsHanded drives ALL FOUR pinState* values
// through the builder in ONE call and pins each carried out unchanged and
// distinct.
//
// At the value level this is a pass-through. What it bites is at the TYPE level:
// a record collapsing the read to a boolean maps exited-but-not-yet-reaped and
// instrument-failed onto the same "not alive" and cannot return both, so driving
// all four in a single record is what makes the test fail to even express itself
// against a bool field. The distinctness assertion is what keeps "alive"
// separable from "exited but not yet reaped", and an instrument failure from
// both.
//
// # The inputs are hand-built, and that is the correct answer rather than a concession
//
// AC5's no-exec rule closes both shipped producers: the per-pid state read execs
// `ps` (`pinReadState`), and pinClassifyState (:332) is pure
// but its pinStateNoSuchProcess arm needs an err that is a real ExitError from
// os/exec carrying a normal-exit status, which Go cannot synthesize — the
// shipped helper that borrows one (:1088) execs `false`.
//
// Hand-building costs nothing here because this record CONSUMES the verdicts and
// never derives them, so a hand-built row can assert nothing the classifier
// would have refused. It is the position finTrailerOutcomeValues()
// (finding_trailer_evidence_test.go:550) occupies for #1290's outcome, and the
// rule trailGateCases states: the shipped producer for what it can emit,
// hand-built for what it cannot (trailer_admissibility_test.go:758-762).
func TestFinRecordLivenessIsConsumedAsHanded(t *testing.T) {
	values := finRecordLivenessValues()
	if len(values) != 4 {
		t.Fatalf("the pinState* space holds %d value(s), want 4 — this test's whole claim is "+
			"that the record can return all of them at once", len(values))
	}

	in := finRecordInputs{
		ExitCode:      pinExitStatusUnknown,
		Rows:          finRecordMatchedRows(""),
		Attribution:   finRecordVoidAttribution(),
		Trailer:       finRecordAbsentTrailer(),
		RunnerFromEnv: reachRunnerPathFromEnv(finRecordEnvDelta()),
		ClaudeCommand: tdnFixturePtyArgv,
		ClaudeVersion: "2.1.220 (Claude Code)",
	}
	for i, v := range values {
		in.Liveness = append(in.Liveness, pinStateOutcome{
			Verdict:     v,
			PID:         finRecordWrapperPID + i,
			PPID:        finRecordClaudePID,
			StateColumn: "S",
			ExitStatus:  pinExitStatusUnknown,
			Detail:      "hand-built: this record consumes the verdict and never derives it",
		})
	}

	rec := finRecordBuild(in)

	if len(rec.Liveness) != len(in.Liveness) {
		t.Fatalf("liveness reads: got %d, want %d — all four values ride in ONE record, because "+
			"a field that could not hold them all is the collapse this test exists to catch",
			len(rec.Liveness), len(in.Liveness))
	}
	carried := make(map[string]bool, len(rec.Liveness))
	for i := range rec.Liveness {
		if !reflect.DeepEqual(rec.Liveness[i], in.Liveness[i]) {
			t.Errorf("liveness[%d]: got %+v, want %+v — the outcome is consumed as handed, never "+
				"re-derived, renamed or normalised", i, rec.Liveness[i], in.Liveness[i])
		}
		if rec.Liveness[i].Verdict != values[i] {
			t.Errorf("liveness[%d] verdict: got %q, want %q", i, rec.Liveness[i].Verdict, values[i])
		}
		carried[rec.Liveness[i].Verdict] = true
	}
	if len(carried) != len(values) {
		t.Errorf("the record carried %d distinct verdict(s) out of %d handed in: a read collapsed "+
			"to a boolean maps %q and %q onto the same \"not alive\" and cannot return both, so "+
			"%q would stop being distinguishable from %q", len(carried), len(values),
			pinStateExitedNotReaped, pinStateInstrumentFailed, pinStateRunning,
			pinStateExitedNotReaped)
	}
	for _, v := range values {
		if !carried[v] {
			t.Errorf("no liveness read carried %q out of the builder", v)
		}
	}
}

// finRecordInputReaches reports whether target is reachable from typ, walking
// struct fields, slice and array elements, pointers and map keys and values.
//
// A SHALLOW field scan would report clean against a later edit that added a
// struct field carrying the observation one level down, and the claim AC2 makes
// is about REACHABILITY rather than about the top level. `seen` closes the walk
// against a self-referential type; none exists in this family today.
func finRecordInputReaches(typ, target reflect.Type, seen map[reflect.Type]bool) bool {
	if typ == target {
		return true
	}
	if seen[typ] {
		return false
	}
	seen[typ] = true

	switch typ.Kind() {
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			if finRecordInputReaches(typ.Field(i).Type, target, seen) {
				return true
			}
		}
	case reflect.Map:
		if finRecordInputReaches(typ.Key(), target, seen) {
			return true
		}
		return finRecordInputReaches(typ.Elem(), target, seen)
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return finRecordInputReaches(typ.Elem(), target, seen)
	}
	return false
}

// TestFinRecordEmbedsTrailerRecordWhole pins AC2's two halves: the sub-record is
// carried through unchanged, and the observation behind it is not reachable from
// this record at all.
func TestFinRecordEmbedsTrailerRecordWhole(t *testing.T) {
	t.Run("the sub-record is carried through unchanged", func(t *testing.T) {
		// Built by #1290's shipped builder, so this row cannot assert against a
		// sub-record finTrailerBuild would not return.
		sub := finRecordSeenTrailer()
		rec := finRecordBuild(finRecordInputs{
			ExitCode:      0,
			Rows:          finRecordMatchedRows(""),
			Attribution:   finRecordProofAttribution(),
			Trailer:       sub,
			RunnerFromEnv: reachRunnerPathFromEnv(finRecordEnvDelta()),
			ClaudeCommand: tdnFixturePtyArgv,
		})

		// DeepEqual over ten scalars and a name list: the whole sub-record, not a
		// field of it, because "embedded whole" is the claim and picking fields
		// would restate #1290's own tests instead of pinning this record's carriage.
		if !reflect.DeepEqual(rec.Trailer, sub) {
			t.Errorf("trailer sub-record: got %+v, want %+v — it is embedded whole, never "+
				"re-derived and never re-read from the observation", rec.Trailer, sub)
		}
	})

	t.Run("the observation is not reachable from the record's inputs", func(t *testing.T) {
		// THE STRUCTURAL HALF, and a comment alone would not serve it: the point is
		// that a LATER EDIT cannot add such a field silently. trailScanResult.Trailer
		// is nil unless State == trailSeen and a consumer dereferencing it without
		// checking panics loudly (result_trailer_observation_test.go:108-118); this
		// record's builder never has the chance, which is the stronger property
		// trailRunReadings.BoundFrom's comment describes (trail_run_outcome_test.go:425-431).
		inputs := reflect.TypeOf(finRecordInputs{})
		for _, forbidden := range []reflect.Type{
			reflect.TypeOf(trailObservation{}),
			reflect.TypeOf(trailScanResult{}),
		} {
			if finRecordInputReaches(inputs, forbidden, map[reflect.Type]bool{}) {
				t.Errorf("%s is reachable from finRecordInputs: taking it would promote "+
					"trailScanResult.Trailer back into this record's reach, and with it the "+
					"panic-on-unchecked-deref obligation the discriminated optional imposes. "+
					"The already-built finTrailerRecord is what this record takes instead",
					forbidden)
			}
		}

		// The same walk over the RECORD, since a field added there would reach the
		// pointer just as surely as one added to the inputs.
		record := reflect.TypeOf(finRecordRun{})
		for _, forbidden := range []reflect.Type{
			reflect.TypeOf(trailObservation{}),
			reflect.TypeOf(trailScanResult{}),
		} {
			if finRecordInputReaches(record, forbidden, map[reflect.Type]bool{}) {
				t.Errorf("%s is reachable from finRecordRun", forbidden)
			}
		}
	})
}

// TestFinRecordRunnerAgreement drives all three verdicts END TO END through
// finRecordBuild rather than through the comparison helper alone — the record is
// what AC3 is about.
func TestFinRecordRunnerAgreement(t *testing.T) {
	fromEnv := reachRunnerPathFromEnv(finRecordEnvDelta())
	// The premise: an empty delta would make this a reading of the operator's
	// shell, and every row below would then be measuring the environment rather
	// than the record.
	if got := finRecordRunnerLabel(fromEnv); got != "ptyrunner" {
		t.Fatalf("env reading: got %q from delta %v, want a ptyrunner label — the delta names "+
			"PYRY_USE_STREAMJSON explicitly precisely so this cannot depend on the ambient "+
			"environment", fromEnv, finRecordEnvDelta())
	}

	tests := []struct {
		name    string
		argv    string
		want    string
		wantVia string
	}{
		{
			name:    "a streamrunner argv against a ptyrunner env shows the disagreement",
			argv:    tdnFixtureStreamArgv,
			want:    finRecordRunnerDisagrees,
			wantVia: "streamrunner",
		},
		{
			// THE ROW THAT FAILS AGAINST A WHOLE-STRING COMPARISON. Both readings
			// name ptyrunner and the two strings still differ, because each appends
			// its own parenthesised reason.
			name:    "a ptyrunner argv against a ptyrunner env agrees on the label",
			argv:    tdnFixturePtyArgv,
			want:    finRecordRunnerAgrees,
			wantVia: "ptyrunner",
		},
		{
			// indeterminate is a THIRD ANSWER. Against reachRunnerPathFromArgv this
			// same argv answers "ptyrunner" — which would agree with the env and
			// publish a runner claim no reading supports — so this row is also the
			// pin on which argv read the record uses.
			name:    "an argv carrying neither marker is indeterminate, not a disagreement",
			argv:    finRecordFixtureNeitherArgv,
			want:    finRecordRunnerIndeterminate,
			wantVia: finRecordRunnerIndeterminate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := finRecordBuild(finRecordInputs{
				ExitCode:      0,
				Rows:          finRecordMatchedRows(""),
				Attribution:   finRecordProofAttribution(),
				Trailer:       finRecordSeenTrailer(),
				RunnerFromEnv: fromEnv,
				ClaudeCommand: tc.argv,
			})

			if rec.RunnerAgreement != tc.want {
				t.Errorf("agreement: got %q, want %q from env %q and argv reading %q",
					rec.RunnerAgreement, tc.want, rec.RunnerFromEnv, rec.RunnerFromArgv)
			}
			if got := finRecordRunnerLabel(rec.RunnerFromArgv); got != tc.wantVia {
				t.Errorf("argv label: got %q, want %q", got, tc.wantVia)
			}
			// The record shows BOTH readings rather than a single reduced label:
			// #1230's rule is that the env read is documentation and the argv read
			// carries the evidential signal, and a reader can only tell them apart
			// while both are present.
			if rec.RunnerFromEnv == "" || rec.RunnerFromArgv == "" {
				t.Errorf("runner readings: env %q, argv %q — a record publishing one label "+
					"cannot show a disagreement at all", rec.RunnerFromEnv, rec.RunnerFromArgv)
			}
			// The argv read is one of tdnRunnerFromArgv's three answers and carries
			// its reason (:1195), so a reader sees what the label was read off.
			if !strings.Contains(rec.RunnerFromArgv, "(") {
				t.Errorf("argv reading %q has no parenthesised reason", rec.RunnerFromArgv)
			}
			// THE REDUCTION IS THE POINT: the argv is read and never retained.
			if strings.Contains(rec.RunnerFromArgv, tc.argv) ||
				strings.Contains(rec.RunnerFromEnv, tc.argv) {
				t.Errorf("a runner field carries the argv verbatim: env %q, argv %q",
					rec.RunnerFromEnv, rec.RunnerFromArgv)
			}

			if tc.want == finRecordRunnerAgrees {
				// Stated as an assertion rather than in prose: this is WHY the
				// comparison is on the label alone. A record comparing the two full
				// strings would report a disagreement here, and on every real run.
				if rec.RunnerFromEnv == rec.RunnerFromArgv {
					t.Fatalf("the two readings are byte-equal (%q), so this row no longer "+
						"discriminates between a label comparison and a whole-string one — the "+
						"two producers append their own parenthesised reasons and are never "+
						"expected to agree verbatim", rec.RunnerFromEnv)
				}
			}
		})
	}
}

// TestFinRecordCarriesNoCapturedBytes is the record's own construction claim made
// checkable rather than advisory: no argv in any form, in a field or quoted into
// the detail.
//
// The needle is planted in the Command of EVERY input row and in the claude row
// the runner-path read consumes. Since finRecordProc cannot hold an argv at all,
// what this test actually covers is THE CONVERSION AND THE DETAIL — the two
// places argv is in the builder's reach. The rendered artifact's full
// multi-input sweep across every input is #1286's and is not restated here.
//
// # What is copied from #1290's sweep, and what is deliberately not
//
// Copied: the non-vacuity precondition, the per-row headroom assertion on the
// BUILT record, and the per-channel naming so a failure says which one leaked.
//
// NOT copied: its top-level forbidden-key scan. Its own closing comment says why
// (finding_trailer_evidence_test.go:766-772) — that scan is valid BECAUSE
// finTrailerRecord is flat, and "lifted onto a record with a struct-valued field
// it would never examine the inner keys". finRecordRun has four struct- or
// slice-valued fields, so the same loop here would inspect ten top-level keys,
// miss every nested one, and read as a structural guarantee it is not providing.
// Vacuous coverage is worse than none. The structural guarantee here comes from
// finRecordProc being three ints: there is no argv-shaped key to find, at any
// depth. The marshal sweep below IS recursive — it walks the embedded
// sub-records, the row slice and the liveness slice — so the nested surface is
// covered by bytes rather than by key names.
func TestFinRecordCarriesNoCapturedBytes(t *testing.T) {
	tests := []struct {
		name        string
		exitCode    int
		attribution finAttributeRecord
		trailer     finTrailerRecord
		argv        string
	}{
		{
			name:        "an agreeing runner reading over a proof attribution",
			exitCode:    0,
			attribution: finRecordProofAttribution(),
			trailer:     finRecordSeenTrailer(),
			argv:        tdnFixturePtyArgv,
		},
		{
			name:        "a disagreeing runner reading over a void attribution",
			exitCode:    pinExitStatusUnknown,
			attribution: finRecordVoidAttribution(),
			trailer:     finRecordAbsentTrailer(),
			argv:        tdnFixtureStreamArgv,
		},
		{
			name:        "an indeterminate runner reading",
			exitCode:    1,
			attribution: finRecordVoidAttribution(),
			trailer:     finRecordAbsentTrailer(),
			argv:        finRecordFixtureNeitherArgv,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := finRecordMatchedRows(" " + trailNeedle)
			claudeCommand := tc.argv + " " + trailNeedle

			// THE NON-VACUITY PRECONDITION. There is no inbound cap to defeat here —
			// the plant is raw input — but a fixture that lost the needle makes the
			// whole sweep theatre, and the precondition is what says so at the point
			// of failure rather than three assertions later.
			for i, row := range rows {
				if !strings.Contains(row.Command, trailNeedle) {
					t.Fatalf("input row %d carries no needle in its %d-byte command, so the sweep "+
						"below would pass against a record that leaked every argv it was handed",
						i, len(row.Command))
				}
			}
			if !strings.Contains(claudeCommand, trailNeedle) {
				t.Fatalf("the planted claude command carries no needle")
			}

			// The two embedded sub-records are asserted CLEAN before the build, so a
			// red sweep below names the argv conversion rather than a fixture that
			// smuggled the needle in through a field AC2 requires be carried whole.
			for _, sub := range []struct {
				name string
				val  any
			}{
				{"attribution", tc.attribution},
				{"trailer", tc.trailer},
			} {
				encoded, err := json.Marshal(sub.val)
				if err != nil {
					t.Fatalf("marshalling the %s sub-record: %v", sub.name, err)
				}
				if bytes.Contains(encoded, []byte(trailNeedle)) {
					t.Fatalf("the %s sub-record already carries the needle, so a red sweep below "+
						"would name the wrong channel: %s", sub.name, encoded)
				}
			}

			rec := finRecordBuild(finRecordInputs{
				ExitCode:      tc.exitCode,
				Rows:          rows,
				Liveness:      []pinStateOutcome{{Verdict: pinStateRunning, PID: finRecordWrapperPID}},
				Attribution:   tc.attribution,
				Trailer:       tc.trailer,
				RunnerFromEnv: reachRunnerPathFromEnv(finRecordEnvDelta()),
				ClaudeCommand: claudeCommand,
				ClaudeVersion: "2.1.220 (Claude Code)",
			})

			// THE HEADROOM, ASSERTED PER ROW ON THE BUILT RECORD rather than argued
			// in prose. trailDetail caps at reachMaxCommandBytes, so a Detail that
			// had wrongly interpolated an argv would be truncated before the needle
			// if the surrounding prose left no room — and the containment checks
			// below would then pass against a leaking implementation. That is the
			// defect #1284 shipped. Pinning the room keeps it impossible, and keeps
			// it impossible after a later edit lengthens the Detail: the failure
			// lands here, naming the record, rather than silently disarming the
			// sweep.
			if room := reachMaxCommandBytes - len(rec.Detail); room < len(trailNeedle) {
				t.Errorf("the detail is %d bytes, leaving %d of trailDetail's %d-byte cap against "+
					"a %d-byte needle: a detail that leaked an argv would be truncated before the "+
					"needle and the checks below would pass against it. Shorten the detail — the "+
					"long-form argument belongs in a comment, which no cap applies to",
					len(rec.Detail), room, reachMaxCommandBytes, len(trailNeedle))
			}

			// Named separately from the marshal sweep so the failure says WHICH
			// channel leaked. A %v verb applied to in.Rows or to in itself is the
			// realistic slip, and it lands here.
			if strings.Contains(rec.Detail, trailNeedle) {
				t.Errorf("the detail quotes an argv: %q", rec.Detail)
			}

			encoded, err := json.Marshal(rec)
			if err != nil {
				t.Fatalf("marshalling the run record: %v", err)
			}
			if bytes.Contains(encoded, []byte(trailNeedle)) {
				t.Errorf("the marshalled record carries verbatim argv read off the ambient process "+
					"table, in a record destined for a public issue: %s", encoded)
			}
		})
	}
}

// TestFinRecordPublishesTheTrailerKeyNamesTheReaderRead is #1363's AC1: the key
// names trailScan read off the line reach the PUBLISHED record, and a record
// built from a scan carrying no trailer renders that field a way a seen one
// cannot.
//
// # Why it drives the whole chain rather than one tier
//
// The names cross four hands — trailScan, finTrailerSighting, finTrailerBuild,
// finRecordBuild — and after the bounding call every one of them is a plain copy,
// so a test at any single tier passes against a build where the NEXT one dropped
// the field. What makes the assertion worth its length is that the expectation is
// the READER'S OWN OUTPUT over the same bytes (trailKeyNames,
// `trailKeyNames`) rather than a hand-written list. A literal
// expectation would pin the fixture's key set — which trailExpectedKeyNames
// already does one tier down — and would pass against a carrier that got its
// names from anywhere other than the line.
//
// That indirection is also how the containment already proven reaches the
// artifact. TestTrailKeyNamesCarryNoValues plants
// a distinct needle in every string-valued position of a trailer line and asserts
// none reaches trailScanResult.KeyNames; equality with that proven-clean output
// is what stops the proof from ending one tier short of the file an operator
// pastes.
//
// # The bounds precondition, and what it buys
//
// finBoundKeyNames caps the count and each name's length, so a fixture over
// either bound would make the published list a PREFIX or a truncation of the
// reader's and the equality would be asserting the cap rather than the carriage.
// Asserting the fixture under both bounds first is what makes "the reader's
// names" and "the published names" the same list.
//
// # The not-seen arm is the distinction this family exists to keep
//
// It is asserted on the MARSHALLED bytes rather than on the Go value, because the
// claim is about the published artifact. A nil slice renders null and a filled
// one an array; an omitempty tag would render NEITHER, dropping the key — and a
// seen trailer whose names were empty would then be byte-identical to a record
// that never had a trailer at all. The key's presence on BOTH records is what
// catches a later editor adding that tag.
func TestFinRecordPublishesTheTrailerKeyNamesTheReaderRead(t *testing.T) {
	// The SHIPPED scanner over a shipped fixture, so this test can never assert
	// against a scan state trailScan would not return for those bytes. The pad is
	// the shortest this family plants with: nothing here reads a line.
	line := trailPaddedTrailer(0)
	scan := trailScan([]byte(line + "\n"))

	// THE NON-VACUITY PRECONDITION, first and fatal. Against a reader that
	// recorded no names the equality below compares nil to nil and passes on a
	// carrier that publishes nothing at all.
	if scan.State != trailSeen || scan.Trailer == nil || len(scan.KeyNames) == 0 {
		t.Fatalf("fixture: got state %q carrying a decode %t and %d name(s) (%s); want %q "+
			"carrying one and at least one name — with no names read there is nothing for the "+
			"carriage below to be about", scan.State, scan.Trailer != nil, len(scan.KeyNames),
			scan.Detail, trailSeen)
	}

	// THE BOUNDS PRECONDITION, which AC1 names explicitly. Over either bound the
	// published list is a prefix or a truncation of the reader's, and the equality
	// below would then be asserting finBoundKeyNames' cap rather than the carriage.
	if len(scan.KeyNames) > finTrailerMaxKeyNames {
		t.Fatalf("the fixture carries %d name(s) against the %d-name bound: the published list "+
			"would be an alphabetic PREFIX of the reader's, and the equality below would "+
			"compare a capped list against a whole one", len(scan.KeyNames), finTrailerMaxKeyNames)
	}
	for _, name := range scan.KeyNames {
		if len(name) > finTrailerMaxKeyNameBytes {
			t.Fatalf("the fixture carries the %d-byte name %q against the %d-byte bound: it would "+
				"be published truncated and marked, and the equality below would compare that "+
				"marking against the whole name", len(name), name, finTrailerMaxKeyNameBytes)
		}
	}

	// THE DISCRIMINATOR, asserted by name rather than left to the count.
	// terminal_reason is the key this whole field exists to carry: pyry invents it
	// and claude's own result line has no such key, so once the fixed decode has
	// collapsed an absent key and an emitted "" into the same value, the NAME is
	// the only thing separating "the watchdog fired" from "the run was healthy".
	discriminator := false
	for _, name := range scan.KeyNames {
		if name == "terminal_reason" {
			discriminator = true
		}
	}
	if !discriminator {
		t.Fatalf("the reader's names %q do not include terminal_reason: a fixture without it "+
			"leaves the carriage asserted over names no reader of the artifact needs",
			scan.KeyNames)
	}

	// End to end through the SHIPPED builders, with nothing hand-assembled between
	// the scan and the record.
	seen := finRecordBuild(finRecordInputs{
		ExitCode:    0,
		Rows:        finRecordMatchedRows(""),
		Attribution: finRecordProofAttribution(),
		Trailer: finTrailerBuild(trailOutcomeVoidBudgetFired,
			finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss)),
		RunnerFromEnv: reachRunnerPathFromEnv(finRecordEnvDelta()),
		ClaudeCommand: tdnFixturePtyArgv,
	})

	if want := trailKeyNames([]byte(line)); !reflect.DeepEqual(seen.Trailer.KeyNames, want) {
		t.Errorf("published names: got %q, want %q — the reader's own names for the same line. "+
			"Every hand between the two is a copy, so a mismatch names the tier that dropped, "+
			"reordered or re-derived them", seen.Trailer.KeyNames, want)
	}

	absent := finRecordBuild(finRecordInputs{
		ExitCode:    0,
		Rows:        finRecordMatchedRows(""),
		Attribution: finRecordProofAttribution(),
		Trailer: finTrailerBuild(trailOutcomeVoidNoTrailer,
			finTrailerSighting(finTrailerAbsentScan(), 0, trailBoundNone)),
		RunnerFromEnv: reachRunnerPathFromEnv(finRecordEnvDelta()),
		ClaudeCommand: tdnFixturePtyArgv,
	})

	// Read off the MARSHALLED sub-record, because the claim is about the published
	// artifact rather than about an in-memory struct. The json name is spelled here
	// rather than derived: a renamed tag makes the presence check below RED, so the
	// literal defends itself.
	trailerKeys := func(arm string, rec finTrailerRecord) json.RawMessage {
		t.Helper()
		encoded, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshalling %s's trailer sub-record: %v", arm, err)
		}
		var keyed map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &keyed); err != nil {
			t.Fatalf("decoding %s's marshalled trailer sub-record: %v", arm, err)
		}
		raw, ok := keyed["trailer_keys"]
		if !ok {
			t.Fatalf("%s renders no trailer_keys key at all, only %d other(s): an omitempty tag "+
				"drops the key on the empty value, which would render a seen trailer carrying no "+
				"names byte-identically to a record that never had a trailer", arm, len(keyed))
		}
		return raw
	}

	seenKeys := trailerKeys("the seen record", seen.Trailer)
	absentKeys := trailerKeys("the no-trailer record", absent.Trailer)

	if string(absentKeys) != "null" {
		t.Errorf("the no-trailer record renders trailer_keys as %s, want null: no names were read "+
			"off a scan that found no trailer, and a record rendering an empty ARRAY there claims "+
			"a reading it never took", absentKeys)
	}
	var published []string
	if err := json.Unmarshal(seenKeys, &published); err != nil {
		t.Fatalf("decoding the seen record's trailer_keys %s: %v", seenKeys, err)
	}
	if len(published) == 0 {
		t.Errorf("the seen record renders trailer_keys as %s: the match return is past "+
			"tr.Type == \"result\" and so reachable only from a line that already decoded as a "+
			"JSON object, which is why an empty name set is unreachable from this arm", seenKeys)
	}
	if string(seenKeys) == string(absentKeys) {
		t.Errorf("both records render trailer_keys as %s, so a reader of the artifact cannot tell "+
			"a trailer that was READ from one that was never there — the distinction this family "+
			"exists to keep, and the one an omitempty-shaped collapse destroys", seenKeys)
	}
}
