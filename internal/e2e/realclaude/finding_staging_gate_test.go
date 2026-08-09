//go:build e2e_realclaude

package realclaude

// The staging-gate outcome tier: the conditions under which a probe run never
// staged its subject, and the rule that such a run is never handed to the run
// classifier.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence. Everything here runs offline: no live
// claude, no credentials, no daemon, no subject process, no FIFO, no
// process-table read, no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinOutcome' -v ./internal/e2e/realclaude/
//
// # Why an unstaged run must never reach the classifier
//
// trailClassifyRun (trail_run_outcome_test.go:363) owns one probe run's outcome
// over a closed set of twelve values, and it ASSUMES THE RUN STAGED: that a Bash
// call was issued, that it was the rig's hold command, that the rendezvous
// completed. Two conditions this probe can hit have no value among the twelve and
// no field in its input record trailRunReadings (:196) — the model never issued
// the Bash call, and the trigger did not fire.
//
// Handing such a run over is not a neutral act. On an unstaged run the argv scan
// still runs over a healthy process table, parses rows and matches nothing, so
// the classifier falls past Steps 1-7 to trailOutcomeNoRowMatched (:129) — one of
// its three ANSWERS, whose own comment calls it "a statement about THE SCAN, not
// about the command having exited" and notes there is "deliberately no 'exited
// normally' value in this space for it to decay into". Both statements are true
// of a run where a command existed. Published about a run where none ever did,
// the answer is a false negative wearing an answer's label — the instrument built
// to prevent a false negative becomes the thing that publishes one.
//
// So two tiers, kept apart: rig-side outcomes decided BEFORE the classifier is
// consulted, and the classifier's twelve consumed as returned, neither
// re-derived nor renamed. This file is the lower tier and the gate that enforces
// the separation.
//
// # The structural closure, and why it is the signature rather than a comment
//
// finOutcomeStagingGate neither takes nor returns a trailRunReadings, and
// finOutcomeResult has no field one is reachable from. A failure arm therefore
// holds nothing a trailClassifyRun call could be made from: the forbidden call is
// not merely discouraged inside the gate, it is UNWRITABLE there — the shape the
// next consumer cannot quietly undo. Because the violation is unwritable, and a
// test that cannot express its own violation proves nothing,
// TestFinOutcomeFailuresAreNotRunOutcomes pins the OBSERVABLE consequence
// instead: each of the six failure values is rejected by trailIsRunOutcome.
//
// # The gate reads model output, so its Details are a redaction surface
//
// One of the seven conditions — the Bash call was not the staged hold command —
// is decided by comparing what claude actually issued against what the rig
// staged, so the gate reads a string the model chose. BOTH OPERANDS COUNT, not
// only the issued one: the staged command looks rig-authored and therefore safe,
// but the rig builds it around a t.TempDir() path and a binary resolved by
// exec.LookPath (trail_run_rig_test.go:506-556), so on a live run it embeds an
// operator filesystem path — the same leak class pinStateColumns
// (process_pin_liveness_test.go:232) refuses a command column for. These records
// are pasted into public issues, so no Detail here quotes either one, and
// TestFinOutcomeResultCarriesNoCapturedBytes plants the needle in both precisely
// so that a developer reading the rule as covering only the model's string cannot
// resolve the disagreement by dropping an operand from the plant set.
//
// # Reused, not rebuilt
//
// trailIsRunOutcome (trail_run_outcome_test.go:277) and trailRunOutcomeValues
// (:1386) are the twelve and their membership predicate — called, never
// re-derived or hand-copied. trailNeedle (result_trailer_observation_test.go:325)
// is the shipped needle.
//
// trailDetail (trailer_admissibility_test.go:233) is reused rather than given a
// finDetail twin, for the reason #1280 already settled in merged code
// (finding_attribution_fanout_test.go:37-44): trailDetail's own "the trail*
// family stays out of the tdn* teardown classifier's reach" argument does not
// transfer, because trailDetail carries no decision (it is fmt.Sprintf plus
// reachCapCommand's 512-byte cap) and this file is BY DESIGN inside the trail
// family's reach — it calls trailIsRunOutcome and lives one tier below
// trailClassifyRun. Reusing it keeps the cap single-sourced.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// --- the staging tier's value space -------------------------------------------

// What the rig can conclude about whether a run STAGED, as a positive allowlist
// of seven: six failures and one pass-through.
//
// Every value carries a `stage-` prefix, and the sub-namespace is load-bearing
// rather than cosmetic, for the reason trail_run_outcome_test.go:108-113 gives
// for its own third namespace: several spaces now mean nearly the same words, so
// a copy-paste between them must read as a visible mistake rather than a
// plausible line.
//
// THE PASS-THROUGH IS DELIBERATELY NOT THE ZERO VALUE. Six of the seven are
// failures; if the pass-through were "", a finOutcomeResult nobody filled would
// read as "this run staged fine, go classify it" — the unsafe direction, and
// precisely the collapse this tier exists to prevent. The family already argues
// the point twice: TestTrailConstantsAreClosed
// (result_trailer_observation_test.go:345) fails any closed-space value that is
// the empty string because "a zero-valued field reads as it", and
// trailRunReadings.PyryExited documents its own zero as pointing "the SAFE way"
// (trail_run_outcome_test.go:223-226).
//
// There is no out-of-contract value here and none is to be added. Seven is
// seven: the two inputs that could otherwise want one are closed by the two
// guard conditions inside finOutcomeStagingGate.
const (
	// finOutcomeNoBashCall: the model never issued the Bash call, so no command
	// was staged and nothing downstream is meaningful.
	finOutcomeNoBashCall = "stage-no-bash-call"
	// finOutcomeCommandNotStaged: a Bash call was issued and it was not the staged
	// hold command, so every reading downstream would be about some other process.
	finOutcomeCommandNotStaged = "stage-command-not-staged"
	// finOutcomeTriggerDidNotFire: the trigger did not fire.
	finOutcomeTriggerDidNotFire = "stage-trigger-did-not-fire"
	// finOutcomeRendezvousIncomplete: the rendezvous never completed, so the
	// subject is not known to have reached the state the pin scan is timed against.
	finOutcomeRendezvousIncomplete = "stage-rendezvous-incomplete"
	// finOutcomePinScanErrored: the during-turn pin scan errored as an instrument.
	// The instrument's own breakage, never an answer about the staging.
	finOutcomePinScanErrored = "stage-pin-scan-errored"
	// finOutcomePinCountUnexpected: the during-turn pin matched a count other than
	// the one expected.
	finOutcomePinCountUnexpected = "stage-pin-count-unexpected"
	// finOutcomeReadyToClassify: THE PASS-THROUGH. The run staged and its readings
	// are to be classified. The only value on which trailClassifyRun is consulted.
	finOutcomeReadyToClassify = "stage-ready-to-classify"
)

// --- the records --------------------------------------------------------------

// finOutcomeStaging is what the rig knows about whether the run staged: pure
// conditions — bools, counts, and the two command strings the identity check
// compares. No transcript, no process handle, no *testing.T.
//
// INPUT ONLY — NEVER PUBLISHED. It carries the two captured strings, and it is
// deliberately the one record in this file with NO json tags: it must not be
// embedded in, marshalled into, or quoted by any published record. Only
// finOutcomeResult crosses into publishable space. The asymmetry is load-bearing
// rather than incidental — every sibling record in this family states its content
// rule at the type (trailRunReadings:192-195, trailRunOutcome:240-251,
// trailGateResult:238-253) and this one states the converse for the same reason.
// Adding tags here "for symmetry" is the first step toward publishing two
// captured strings into a public issue.
//
// THE TWO COMMANDS ARE COMPARED AS OPAQUE BYTES. They are never parsed, split on
// whitespace, shell-lexed, path-resolved or executed. The gate answers one
// question about them — are they the same string — and the purity contract's
// no-exec clause is what keeps a later "let me just check the staged binary still
// exists" from turning an identity check into a filesystem read or a subprocess
// spawn.
type finOutcomeStaging struct {
	// BashIssued records THAT a Bash call was issued. findBashToolUse
	// (sigterm_mid_tool_use_test.go:1488-1500) returns a tool_use ID and an event
	// index and no command — the decoder behind it, contentBlock
	// (tool_loop_test.go:160-168), has no `input` member — so "was a call issued"
	// and "what was it" are genuinely two separate readings.
	BashIssued bool
	// IssuedCommand is what claude actually issued: VERBATIM MODEL OUTPUT, supplied
	// by the caller because the shipped tool_use path cannot yield it.
	IssuedCommand string
	// StagedCommand is the hold command the rig staged. Rig-authored, and a
	// captured string all the same: it is built around a t.TempDir() path and an
	// exec.LookPath result, so on a live run it embeds an operator filesystem path.
	StagedCommand string
	// TriggerFired and RendezvousDone are the two behavioural conditions. Both
	// zero-value to false, which is a failure arm — the safe direction.
	TriggerFired   bool
	RendezvousDone bool
	// PinScanErrored records THAT the during-turn pin scan failed, never what it
	// said, mirroring trailRunReadings.ArgvScanErrored
	// (trail_run_outcome_test.go:205-212). ps stderr is a captured string on the
	// same footing as argv.
	PinScanErrored bool
	// PinMatchCount and PinWantCount are the scan's match count and the count the
	// staging expected. Counts only: pinScan.Matches holds verbatim argv and stays
	// out of this record entirely.
	PinMatchCount int
	PinWantCount  int
}

// finOutcomeResult is the staging tier's decision: flat, two scalar fields,
// neither of them command-shaped and neither of them a field from which either
// command is reachable.
//
// The content rule for Detail is pinned rather than left to judgement. It MAY
// name outcome values, the booleans as booleans, PinMatchCount and PinWantCount.
// It may NEVER interpolate IssuedCommand or StagedCommand IN ANY FORM, including
// a length or a prefix. TestFinOutcomeResultCarriesNoCapturedBytes is the
// enforcing test, and it is why the needle goes into both operands rather than
// into the model's one.
type finOutcomeResult struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

// --- membership helper ---------------------------------------------------------

// finOutcomeIsValue reports whether v is one of the seven staging outcomes. It
// mirrors trailIsRunOutcome (trail_run_outcome_test.go:277) and its siblings
// trailIsGateValue, trailIsAdmitValue, pinIsVerdict and tdnIsReapVerdict, and
// exists for the same reason: a value a reader of a published record cannot look
// up is a verdict they cannot interpret.
func finOutcomeIsValue(v string) bool {
	switch v {
	case finOutcomeNoBashCall, finOutcomeCommandNotStaged, finOutcomeTriggerDidNotFire,
		finOutcomeRendezvousIncomplete, finOutcomePinScanErrored, finOutcomePinCountUnexpected,
		finOutcomeReadyToClassify:
		return true
	}
	return false
}

// finOutcomeValues is the closed set as data, so a coverage loop can range over
// it. A value listed here that the predicate rejects, or vice versa, goes red in
// TestFinOutcomeValuesAgreeWithThePredicate.
func finOutcomeValues() []string {
	return []string{
		finOutcomeNoBashCall,
		finOutcomeCommandNotStaged,
		finOutcomeTriggerDidNotFire,
		finOutcomeRendezvousIncomplete,
		finOutcomePinScanErrored,
		finOutcomePinCountUnexpected,
		finOutcomeReadyToClassify,
	}
}

// --- the gate -------------------------------------------------------------------

// finOutcomeStagingGate decides, from the staging conditions alone, one of the
// six failure outcomes or the pass-through.
//
// Pure over its input: no exec, no clock, no filesystem, the same contract as
// trailGate, trailClassifyRun, trailAdmitAttribution and tdnClassifyReapLog. It
// takes no *testing.T and never fails a test — an instrument failure observed
// mid-turn is a datum to publish, not a reason to abort the turn. That purity is
// what lets all seven arms be driven offline from synthetic inputs.
//
// # Arm order
//
// Most-upstream-first, so every later arm's precondition holds by construction.
// The order is load-bearing at two points and both are pinned by
// TestFinOutcomeStagingGate:
//
//   - IDENTITY BEFORE BEHAVIOUR (arm 2 before arms 3 and 4). The trigger and
//     rendezvous conditions are claims ABOUT THE STAGED COMMAND. If claude issued
//     something else, reporting "the trigger did not fire" is true but files "the
//     model ran the wrong thing" under "our trigger is broken".
//   - INSTRUMENT FAILURE BEFORE ITS RESULT (arm 5 before arm 6). pinScanArgv
//     returns the ZERO pinScan on error (process_pin_liveness_test.go:191-196), so
//     an errored scan arrives with PinMatchCount == 0. Checking the count first
//     would report "matched an unexpected count" about a scan that never ran — the
//     same defect trailOutcomeVoidArgvScanErrored is kept distinct from
//     trailOutcomeVoidNoRowsParsed to avoid, one tier up.
func finOutcomeStagingGate(s finOutcomeStaging) finOutcomeResult {
	if !s.BashIssued {
		return finOutcomeResult{
			Value: finOutcomeNoBashCall,
			Detail: trailDetail("the model never issued the Bash call, so no command was ever "+
				"staged and there is nothing for the run classifier to measure. Reported here "+
				"rather than handed on: the argv scan on such a run reads a healthy process "+
				"table, parses rows and matches nothing, which the run classifier would report "+
				"as %s — an answer about the scan, published about a run where no command ever "+
				"existed", trailOutcomeNoRowMatched),
		}
	}

	// The identity check, and its guard. NEITHER OPERAND IS NAMED IN THE DETAIL
	// BELOW and the sentence has no interpolation site for one — the only verbs it
	// carries take outcome values. The issued command is verbatim model output; the
	// staged one embeds a t.TempDir() path and an exec.LookPath result
	// (trail_run_rig_test.go:506-556), which is the same operator-filesystem-path
	// leak class pinStateColumns refuses a command column for. The rule covers both
	// operands, so a Detail naming "which operand differed" is also out — it is one
	// edit away from naming its bytes.
	//
	// The Detail is also kept SHORT, and that is a redaction property rather than a
	// style preference: trailDetail caps the formatted string at
	// reachMaxCommandBytes, so a Detail with no room left for a planted command
	// would have that command's needle TRUNCATED AWAY and
	// TestFinOutcomeResultCarriesNoCapturedBytes would pass against a leaking
	// implementation. The long-form argument for each arm lives in these comments,
	// which no cap applies to. The test pins the headroom per row.
	//
	// The `|| StagedCommand == ""` clause is not decoration. Without it a caller who
	// fills the bools but leaves both command fields empty gets "" == "" → equal →
	// THE PASS-THROUGH. An empty staged command means the rig staged no hold
	// command, so whatever was issued certainly was not it. The clause also absorbs
	// BashIssued == true with an empty IssuedCommand, which is reachable precisely
	// because findBashToolUse answers WHETHER a Bash call was issued without
	// yielding its command.
	if s.IssuedCommand != s.StagedCommand || s.StagedCommand == "" {
		return finOutcomeResult{
			Value: finOutcomeCommandNotStaged,
			Detail: trailDetail("a Bash call was issued and it is not the staged hold command, so "+
				"every reading downstream would be about some other process. Neither operand is "+
				"quoted: both are captured strings. An empty staged command reaches this arm "+
				"too — the rig staged no hold command, so whatever was issued was not it, and "+
				"the alternative is that an unfilled pair compares equal and reaches %s",
				finOutcomeReadyToClassify),
		}
	}

	if !s.TriggerFired {
		return finOutcomeResult{
			Value: finOutcomeTriggerDidNotFire,
			Detail: trailDetail("the trigger did not fire, so the staged hold command was never "+
				"reached and no window exists to measure. Ranked below %s deliberately: this is "+
				"a claim ABOUT the staged command, so reporting it while claude had issued "+
				"something else would file \"the model ran the wrong thing\" under \"our trigger "+
				"is broken\"", finOutcomeCommandNotStaged),
		}
	}

	if !s.RendezvousDone {
		return finOutcomeResult{
			Value: finOutcomeRendezvousIncomplete,
			Detail: trailDetail("the rendezvous never completed, so the subject is not known to "+
				"have reached the state the during-turn pin is timed against. Ranked below %s "+
				"for the same reason %s is: it is a claim about the staged command",
				finOutcomeCommandNotStaged, finOutcomeTriggerDidNotFire),
		}
	}

	if s.PinScanErrored {
		return finOutcomeResult{
			Value: finOutcomePinScanErrored,
			Detail: trailDetail("the during-turn pin scan errored as an instrument, so it answered "+
				"nothing and its match count is the zero pinScan's rather than a reading. Its "+
				"text is not carried: ps stderr is a captured string on the same footing as "+
				"argv. Ranked above %s so a count of 0 the error itself produced is never "+
				"reported as a count that was measured", finOutcomePinCountUnexpected),
		}
	}

	// The count check, and its guard. Without `|| PinWantCount < 1`, an unfilled
	// PinWantCount matches an unfilled PinMatchCount at zero and reaches THE
	// PASS-THROUGH. This probe never stages an expectation of zero matches, so a
	// want below 1 is an unfilled field and must fail rather than pass.
	if s.PinMatchCount != s.PinWantCount || s.PinWantCount < 1 {
		return finOutcomeResult{
			Value: finOutcomePinCountUnexpected,
			Detail: trailDetail("the during-turn pin matched %d row(s) against an expectation of "+
				"%d, so the staging did not produce the process set this run would be measured "+
				"against. A want below 1 reaches this arm too: no staging here expects zero "+
				"matches, so a want of 0 is an unfilled field, and the alternative is that it "+
				"agrees with an unfilled match count and reaches %s", s.PinMatchCount,
				s.PinWantCount, finOutcomeReadyToClassify),
		}
	}

	return finOutcomeResult{
		Value: finOutcomeReadyToClassify,
		Detail: trailDetail("the run staged: a Bash call was issued, it is the staged hold "+
			"command, the trigger fired, the rendezvous completed and the during-turn pin "+
			"matched the %d row(s) expected. THE ONLY value on which the readings are handed "+
			"to the run classifier", s.PinWantCount),
	}
}

// --- fixtures ------------------------------------------------------------------

// finOutcomeHoldCommand stands in for the rig's staged hold command. It is a
// STAND-IN and not the real one: the gate compares its two operands as opaque
// bytes and never parses, splits, shell-lexes, path-resolves or executes either,
// so no fixture here needs a real path — and a fixture carrying one would put an
// operator filesystem path into a test file for nothing.
const finOutcomeHoldCommand = "sh -c <hold-command> <rendezvous-fifo>; exit 0"

// finOutcomeGateCase is one staging input and the value it must reach. The cases
// are shared between TestFinOutcomeStagingGate, which asserts the mapping,
// TestFinOutcomeFailuresAreNotRunOutcomes, which sweeps the same fixtures through
// trailIsRunOutcome, and TestFinOutcomeResultCarriesNoCapturedBytes, which sweeps
// them again with the needle planted — so all three are driven over the gate's
// real value space rather than over three hand-kept lists that could drift.
type finOutcomeGateCase struct {
	name string
	in   finOutcomeStaging
	want string
}

// finOutcomeStagedBase is the fully-staged input each case varies ONE thing from,
// mirroring trailRunWellFormed (trail_run_outcome_test.go:652): a row that
// changes two conditions at once proves nothing about which one its arm keyed on.
// The four rows that must vary two — the two guard holes and the two order
// checks — say so in their own names, because varying two is exactly their point.
//
// PinMatchCount is 2 rather than 1 because a live run matches more than one row
// for a single held command — a shell wrapper and its forked cat
// (TestTrailRigCarriesMoreThanOneMatchedRow, trail_run_rig_test.go:506).
func finOutcomeStagedBase() finOutcomeStaging {
	return finOutcomeStaging{
		BashIssued:     true,
		IssuedCommand:  finOutcomeHoldCommand,
		StagedCommand:  finOutcomeHoldCommand,
		TriggerFired:   true,
		RendezvousDone: true,
		PinScanErrored: false,
		PinMatchCount:  2,
		PinWantCount:   2,
	}
}

func finOutcomeGateCases() []finOutcomeGateCase {
	noBash := finOutcomeStagedBase()
	noBash.BashIssued = false

	wrongCommand := finOutcomeStagedBase()
	wrongCommand.IssuedCommand = "echo something the rig never staged"

	// Guard hole 1: every bool set, both command fields left empty. Without the
	// `|| StagedCommand == ""` clause this compares equal and reaches the
	// pass-through.
	bothCommandsEmpty := finOutcomeStagedBase()
	bothCommandsEmpty.IssuedCommand = ""
	bothCommandsEmpty.StagedCommand = ""

	noTrigger := finOutcomeStagedBase()
	noTrigger.TriggerFired = false

	noRendezvous := finOutcomeStagedBase()
	noRendezvous.RendezvousDone = false

	scanErrored := finOutcomeStagedBase()
	scanErrored.PinScanErrored = true
	// pinScanArgv returns the ZERO pinScan on error, so an errored scan really does
	// arrive with a match count of 0. The fixture says so rather than leaving the
	// base's 2 behind, which no producer could emit alongside the error.
	scanErrored.PinMatchCount = 0

	wrongCount := finOutcomeStagedBase()
	wrongCount.PinMatchCount = 1

	// Guard hole 2: an unfilled want agreeing with an unfilled match count at zero.
	// Without the `|| PinWantCount < 1` clause this reaches the pass-through.
	zeroCounts := finOutcomeStagedBase()
	zeroCounts.PinMatchCount = 0
	zeroCounts.PinWantCount = 0

	// Order row: arm 5 above arm 6. An errored scan AND a count that disagrees.
	erroredAndMiscounted := finOutcomeStagedBase()
	erroredAndMiscounted.PinScanErrored = true
	erroredAndMiscounted.PinMatchCount = 0

	// Order row: arm 2 above arm 3. A wrong issued command AND a trigger that never
	// fired — which is what a run that launched the wrong thing actually looks like.
	wrongCommandAndNoTrigger := finOutcomeStagedBase()
	wrongCommandAndNoTrigger.IssuedCommand = "echo something the rig never staged"
	wrongCommandAndNoTrigger.TriggerFired = false

	return []finOutcomeGateCase{
		{
			name: "no Bash call was ever issued",
			in:   noBash,
			want: finOutcomeNoBashCall,
		},
		{
			name: "a Bash call was issued and it is not the staged hold command",
			in:   wrongCommand,
			want: finOutcomeCommandNotStaged,
		},
		{
			name: "guard: both commands empty with every condition otherwise staged",
			in:   bothCommandsEmpty,
			want: finOutcomeCommandNotStaged,
		},
		{
			name: "the trigger did not fire",
			in:   noTrigger,
			want: finOutcomeTriggerDidNotFire,
		},
		{
			name: "the rendezvous never completed",
			in:   noRendezvous,
			want: finOutcomeRendezvousIncomplete,
		},
		{
			name: "the during-turn pin scan errored as an instrument",
			in:   scanErrored,
			want: finOutcomePinScanErrored,
		},
		{
			name: "the during-turn pin matched a count other than the one expected",
			in:   wrongCount,
			want: finOutcomePinCountUnexpected,
		},
		{
			name: "guard: an unfilled want agreeing with an unfilled match count at zero",
			in:   zeroCounts,
			want: finOutcomePinCountUnexpected,
		},
		{
			name: "order: an errored scan outranks the count its error zeroed",
			in:   erroredAndMiscounted,
			want: finOutcomePinScanErrored,
		},
		{
			name: "order: a wrong issued command outranks a trigger that never fired",
			in:   wrongCommandAndNoTrigger,
			want: finOutcomeCommandNotStaged,
		},
		{
			name: "the run staged and is to be classified",
			in:   finOutcomeStagedBase(),
			want: finOutcomeReadyToClassify,
		},
	}
}

// --- tests ----------------------------------------------------------------------

// TestFinOutcomeConstantsAreClosed is AC1's structural claim made executable, in
// TestTrailConstantsAreClosed's shape (result_trailer_observation_test.go:345).
func TestFinOutcomeConstantsAreClosed(t *testing.T) {
	values := map[string]string{
		"finOutcomeNoBashCall":           finOutcomeNoBashCall,
		"finOutcomeCommandNotStaged":     finOutcomeCommandNotStaged,
		"finOutcomeTriggerDidNotFire":    finOutcomeTriggerDidNotFire,
		"finOutcomeRendezvousIncomplete": finOutcomeRendezvousIncomplete,
		"finOutcomePinScanErrored":       finOutcomePinScanErrored,
		"finOutcomePinCountUnexpected":   finOutcomePinCountUnexpected,
		"finOutcomeReadyToClassify":      finOutcomeReadyToClassify,
	}
	if len(values) != 7 {
		t.Errorf("the closed space holds %d value(s), want 7 — the count is the ticket's own "+
			"enumeration and a change to it is a change to what the staging tier can conclude",
			len(values))
	}

	byValue := make(map[string]string, len(values))
	for name, value := range values {
		if value == "" {
			t.Errorf("%s is the empty string, so a zero-valued field reads as it — the defect the "+
				"closed space exists to prevent", name)
			continue
		}
		if prev, dup := byValue[value]; dup {
			t.Errorf("%s and %s both carry %q, so the two are indistinguishable in a record",
				prev, name, value)
			continue
		}
		byValue[value] = name
	}

	// The pass-through separately, because its direction is the whole argument and
	// not just an instance of the rule above. Six of the seven are failures, so a
	// zero meaning "staged, go classify" would point the tier's failure the unsafe
	// way: an unfilled result would wave an unstaged run straight through to the
	// run classifier, which is the collapse this tier exists to prevent.
	if finOutcomeReadyToClassify == "" {
		t.Error("finOutcomeReadyToClassify is the zero value, so a finOutcomeResult nobody " +
			"filled reads as \"this run staged fine, go classify it\" — the one direction this " +
			"tier must never fail in")
	}

	// The same claim read off the record itself: a result nobody filled must be no
	// outcome at all, never one of the seven.
	var zero finOutcomeResult
	for name, value := range values {
		if zero.Value == value {
			t.Errorf("the zero finOutcomeResult reads as %s (%q)", name, value)
		}
	}
	if finOutcomeIsValue(zero.Value) {
		t.Errorf("finOutcomeIsValue accepts the zero finOutcomeResult's value %q", zero.Value)
	}
}

// TestFinOutcomeValuesAgreeWithThePredicate keeps finOutcomeValues from drifting
// away from the predicate a published record is read through, in
// TestTrailRunOutcomeValuesAgreeWithThePredicate's shape
// (trail_run_outcome_test.go:1407). Without it, an eighth outcome added to the
// predicate but not to the list would make the coverage loop in
// TestFinOutcomeStagingGate silently stop covering it.
func TestFinOutcomeValuesAgreeWithThePredicate(t *testing.T) {
	values := finOutcomeValues()
	if len(values) != 7 {
		t.Errorf("the closed set holds %d value(s), want 7 — the count is the ticket's own "+
			"enumeration and a change to it is a change to what the staging tier can conclude",
			len(values))
	}
	for _, v := range values {
		if !finOutcomeIsValue(v) {
			t.Errorf("%q is listed as a staging outcome but finOutcomeIsValue rejects it", v)
		}
	}
	if finOutcomeIsValue("") {
		t.Error("finOutcomeIsValue accepts \"\", so an unfilled field would look up as a verdict")
	}
}

// TestFinOutcomeSpacesAreDisjoint pins the two outcome spaces apart in BOTH
// directions. Neither may absorb a value from the other through a later rename —
// the whole point of the separate `stage-` sub-namespace is that a copy-paste
// between spaces whose words nearly agree reads as a visible mistake, and this is
// the test that makes it one.
func TestFinOutcomeSpacesAreDisjoint(t *testing.T) {
	for _, v := range finOutcomeValues() {
		if trailIsRunOutcome(v) {
			t.Errorf("trailIsRunOutcome accepts the staging value %q — the run classifier's "+
				"twelve have absorbed a value that means \"this run never staged\"", v)
		}
	}
	// The shipped list, not a hand-copy: re-deriving the twelve here is exactly the
	// drift this test exists to catch.
	for _, v := range trailRunOutcomeValues() {
		if finOutcomeIsValue(v) {
			t.Errorf("finOutcomeIsValue accepts the run outcome %q — the staging tier has "+
				"absorbed a value that means the run was measured", v)
		}
	}
}

// TestFinOutcomeStagingGate drives all seven arms and both guard conditions from
// synthetic inputs.
func TestFinOutcomeStagingGate(t *testing.T) {
	reached := make(map[string]bool, len(finOutcomeValues()))
	for _, tc := range finOutcomeGateCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := finOutcomeStagingGate(tc.in)
			if got.Value != tc.want {
				t.Errorf("value: got %q, want %q (detail: %s)", got.Value, tc.want, got.Detail)
			}
			if got.Detail == "" {
				t.Errorf("value %q carries no detail, so a reader of the record has the verdict "+
					"and none of the reasoning", got.Value)
			}
			if !finOutcomeIsValue(got.Value) {
				t.Errorf("the gate returned %q, which finOutcomeIsValue rejects", got.Value)
			}
		})
		reached[finOutcomeStagingGate(tc.in).Value] = true
	}
	for _, v := range finOutcomeValues() {
		if !reached[v] {
			t.Errorf("no fixture produced %s, so its arm is unproven — a row silently retargeted "+
				"by an edit leaves an arm untested rather than red", v)
		}
	}
}

// TestFinOutcomeFailuresAreNotRunOutcomes is AC4's observable half.
//
// The structural half is the signature: finOutcomeStagingGate neither takes nor
// returns a trailRunReadings and finOutcomeResult has no field one is reachable
// from, so a failure arm holds nothing a trailClassifyRun call could be made
// from — the call is unwritable there rather than merely discouraged. A test that
// cannot express its own violation proves nothing, so this one pins the
// consequence instead.
func TestFinOutcomeFailuresAreNotRunOutcomes(t *testing.T) {
	for _, tc := range finOutcomeGateCases() {
		got := finOutcomeStagingGate(tc.in)
		if trailIsRunOutcome(got.Value) {
			t.Errorf("%s: the staging gate returned %q, which trailIsRunOutcome accepts — a run "+
				"that never staged would then be published carrying one of the classifier's "+
				"twelve, so \"not observed\" could arrive at %s and an unpinnable or unreadable "+
				"command could arrive at a value meaning the command had exited",
				tc.name, got.Value, trailOutcomeNoRowMatched)
		}
	}
	// The pass-through explicitly, and not only as one row of the sweep above: it
	// is a STAGING value, not a run outcome, and this is what stops a later edit
	// from aliasing it onto one of the twelve.
	if trailIsRunOutcome(finOutcomeReadyToClassify) {
		t.Errorf("trailIsRunOutcome accepts %q — the signal to CONSULT the classifier has become "+
			"one of the answers the classifier returns", finOutcomeReadyToClassify)
	}
}

// The two needle-bearing command strings AC5 plants. Both are short on purpose
// and their length is asserted below: trailDetail caps the FORMATTED detail at
// reachMaxCommandBytes, so a Detail that did wrongly interpolate a command would
// be truncated before a needle sitting past the cap, and this test would pass
// against a leaking implementation. trailNeedle is placed past the cap
// deliberately in trailPaddedTrailer (result_trailer_observation_test.go:327-338)
// for the opposite kind of test; here that placement would be the defect. These
// mirror the ~73-byte planted strings at trail_run_outcome_test.go:1335-1343.
const (
	finOutcomePlantedStaged = "a staged hold command that also carries " + trailNeedle
	finOutcomePlantedIssued = "a different issued command that also carries " + trailNeedle
)

// TestFinOutcomeResultCarriesNoCapturedBytes makes the redaction rule checkable
// rather than advisory, in TestTrailRunOutcomeCarriesNoCapturedBytes's shape
// (trail_run_outcome_test.go:1333) and reusing the shipped trailNeedle.
//
// The needle goes into BOTH string-bearing inputs the gate can see — the command
// claude issued and the staged hold command it is compared against — because the
// rule being enforced is not "do not quote the model's string" but "no detail
// here quotes any input's captured string". Scoping the plant to the issued
// operand alone would leave the rule and the recipe disagreeing about the staged
// one, and a developer resolving that disagreement by dropping an operand would
// weaken the test rather than the leak.
//
// The comparison deliberately stays a string comparison inside the gate. Reducing
// it to a caller-supplied bool would leave this test green by giving it nothing to
// plant into — a weaker instrument, not a safer gate.
func TestFinOutcomeResultCarriesNoCapturedBytes(t *testing.T) {
	// The planted strings must stay well inside the cap trailDetail applies, or a
	// leaking Detail would be truncated before the needle and this whole test would
	// be vacuous. Asserted rather than eyeballed so a later edit cannot lengthen a
	// fixture into a false green.
	for name, planted := range map[string]string{
		"finOutcomePlantedStaged": finOutcomePlantedStaged,
		"finOutcomePlantedIssued": finOutcomePlantedIssued,
	} {
		if len(planted) >= reachMaxCommandBytes {
			t.Fatalf("%s is %d bytes, which is not below trailDetail's %d-byte cap: a leaking "+
				"detail would be truncated before the needle and this test would pass against "+
				"it", name, len(planted), reachMaxCommandBytes)
		}
	}

	for _, tc := range finOutcomeGateCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Arms 3 to 7 are only reachable when the two commands are EQUAL and
			// non-empty, so a needle-bearing pair must stay equal for those rows; arm 2
			// is the mismatch arm and needs two DIFFERENT needle-bearing strings. Arm 1
			// ignores the commands entirely — plant the pair there anyway, because the
			// rule is about what the gate can see and not about what it happens to read.
			in := tc.in
			in.StagedCommand = finOutcomePlantedStaged
			in.IssuedCommand = finOutcomePlantedStaged
			if tc.want == finOutcomeCommandNotStaged {
				in.IssuedCommand = finOutcomePlantedIssued
			}

			got := finOutcomeStagingGate(in)
			// The premise first, so the test cannot pass by decaying every row onto one
			// arm once the commands are rewritten.
			if got.Value != tc.want {
				t.Fatalf("value: got %q (%s), want %q — planting the needle must preserve the "+
					"arm under test, or this row proves nothing about it",
					got.Value, got.Detail, tc.want)
			}

			// THE NON-VACUITY CHECK, and the reason this test is an instrument rather
			// than decoration. trailDetail caps the FORMATTED detail at
			// reachMaxCommandBytes, so a Detail that had wrongly interpolated a command
			// would be truncated before the needle if the surrounding prose left no room
			// — and the containment check below would then pass against a leaking
			// implementation. Pinning the headroom per row keeps that impossible, and
			// keeps it impossible after a later edit lengthens a Detail: the failure
			// lands here, naming the arm, rather than silently disarming the sweep.
			if room := reachMaxCommandBytes - len(got.Detail); room < len(finOutcomePlantedIssued) {
				t.Errorf("the %s detail is %d bytes, leaving %d of trailDetail's %d-byte cap "+
					"against a %d-byte planted command: a detail that leaked one would be "+
					"truncated before the needle and the check below would pass against it. "+
					"Shorten the detail — the long-form argument belongs in the arm's comment, "+
					"which no cap applies to", got.Value, len(got.Detail), room,
					reachMaxCommandBytes, len(finOutcomePlantedIssued))
			}

			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshalling the staging outcome: %v", err)
			}
			if bytes.Contains(encoded, []byte(trailNeedle)) {
				t.Errorf("the marshalled staging outcome carries captured bytes: %s", encoded)
			}

			// The structural half: the record has no field for a command string today,
			// and this is the check that a future field does not quietly add one. The
			// scan is valid on this record because finOutcomeResult is FLAT — two scalar
			// fields — so a top-level key scan examines every key it has. Lifted onto a
			// record with a slice-of-struct field it would never examine the inner keys.
			var keyed map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &keyed); err != nil {
				t.Fatalf("decoding the marshalled staging outcome: %v", err)
			}
			for _, forbidden := range []string{"command", "args", "comm", "argv"} {
				for key := range keyed {
					if strings.Contains(key, forbidden) {
						t.Errorf("the staging outcome carries key %q, which is %q-shaped: this "+
							"record's whole value is that it can be published unreviewed, and a "+
							"command column would inherit the operator-review-before-paste "+
							"obligation onto it", key, forbidden)
					}
				}
			}
		})
	}
}
