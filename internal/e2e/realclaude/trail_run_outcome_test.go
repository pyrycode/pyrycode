//go:build e2e_realclaude

package realclaude

// One probe run's observations resolve to EXACTLY ONE outcome from a closed,
// named set, so that a run which measured nothing is recorded as having measured
// nothing rather than falling through to a finding.
//
// This file reaches the run-level verdict and takes no measurement. Everything
// here runs offline: no live claude, no credentials, no daemon, no env gate, no
// t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
//
// # The question, and the three facts that shape every decision below
//
// The probe asks: WAS A BACKGROUNDED COMMAND STILL RUNNING WHEN PYRY DECLARED
// THE TURN FINISHED? Almost every way such a run can go wrong produces an
// observation that LOOKS LIKE A NEGATIVE ANSWER, and this file is the piece that
// refuses that collapse.
//
//  1. THE ATTRIBUTION IS ASYMMETRIC AND IS THE ONLY THING THAT CAN PROVE YES.
//     ptyrunner's pinned teardown order (runner.go:479-485) writes the trailer
//     before the reap defer (:398) SIGKILLs claude's descendant groups, so a
//     group named in pyry's reap log was alive strictly after the trailer was
//     written. A HIT PROVES ALIVENESS; A MISS PROVES NOTHING. #1270 owns deciding
//     when a hit is admissible; this file owns never inverting that asymmetry.
//  2. THE POINT-IN-TIME READS ARE EXPECTED TO BE LATE. The reap completes in the
//     time of one ps exec (reap.go:76) while the observation of the trailer
//     trails the write by up to a poll interval, so a liveness read taken at
//     observation time will usually find the group already reaped. Treated as the
//     answer it manufactures a SYSTEMATIC FALSE NEGATIVE. The claude-still-alive
//     reading is worse than useless as a timeliness witness: the reap runs
//     BETWEEN the trailer and claude's SIGTERM, so in exactly the window the
//     probe exists to catch, it reports "not late". Both stay in the record as
//     corroboration; neither certifies anything.
//  3. EVERY WAY OF MEASURING NOTHING NEEDS ITS OWN NAME. tdnDecideAfter
//     (teardown_liveness_probe_test.go:645) is the counter-example: nine distinct
//     nothing-was-measured conditions in that one function — twenty across its
//     file — all return tdnDispositionSkipped, separated only by a prose
//     DispositionDetail. Prose is decorative; the enum value is what a consumer
//     branches on. This classifier neither reuses tdnDispositionSkipped nor
//     unifies with tdnDecideAfter: the two share their inputs but not their
//     staging path or their outcome set, and merging outcome sets whose
//     separation is the point would undo the ticket.
//
// # The one doctrine: discriminators and publishable records, never captured bytes
//
// trailRunReadings carries, for each observation, the LEANEST value that decides
// — and the outcome record carries counts, never rows, and no command string at
// all. That is what makes the no-captured-bytes property true BY CONSTRUCTION
// rather than by an ordering discipline a later edit can silently break:
//
//   - the trailer's and the attribution's admissibility enter as #1270's two
//     results WHOLE, because both are documented trap-free by construction
//     (trailer_admissibility_test.go:281-296, :336-339);
//   - the trailer's lateness enters as the BoundFrom discriminator alone and
//     never as a trailObservation, which EMBEDS trailScanResult
//     (result_trailer_observation_test.go:141-142) and would therefore promote
//     the trailer pointer straight back into reach. The dotted selector for that
//     field is spelled nowhere in this file, in code OR in prose, so the census
//     recipe — a fixed-string grep, which cannot tell a dereference from a
//     comment — means what it says;
//   - the argv scan enters as two counts and an errored bool. pinScan.Matches
//     holds verbatim argv, which is why pinStateColumns (process_pin_liveness_test.go:232)
//     refuses a `command` column at all; a record whose whole value is that it can
//     be published unreviewed must not inherit the operator-review-before-paste
//     obligation by copying one in. The scan's ERROR is taken as a discriminator
//     rather than as its text for the same reason;
//   - the per-pid liveness enters as []pinStateOutcome, which carries no command
//     column by construction, and leaves only through tdnVerdictSummary, a
//     renderer that emits `pid=N verdict` and can emit nothing else.
//
// A consequence worth stating: Staleness IS NOT AN INPUT AT ALL. Corroboration
// must discriminate on BoundFrom and never on Staleness != 0, and the strongest
// form of that rule is a classifier that could not read a staleness if it wanted
// to.
//
// # Reused, not rebuilt
//
// trailGate / trailAdmitAttribution / trailGateResult / trailAdmitResult /
// trailIsGateValue / trailIsAdmitValue / trailDetail / trailGateCases
// (trailer_admissibility_test.go, #1270) are the two admissibility inputs and
// their membership predicates — the gate is not re-derived, the allowlist is not
// re-derived, and the trailer pointer inside trailScanResult is not read here.
// trailBoundFromMiss / trailBoundFromStart / trailBoundNone and trailNeedle
// (result_trailer_observation_test.go, #1266) are the lateness discriminator and
// the needle. pinStateOutcome / pinIsVerdict / pinScan's two counts
// (process_pin_liveness_test.go, #1235) are the liveness inputs.
// tdnVerdictSummary (teardown_liveness_probe_test.go:858) is the corroboration
// renderer.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// --- the run's value space ----------------------------------------------------

// What one probe run can conclude, as a POSITIVE ALLOWLIST of thirteen: three
// answers and ten named voids. NOTHING is the catch-all for "everything else"
// — trailOutcomeOutOfContract is a named answer about the CALLER's record,
// reached by a guard at the top of trailClassifyRun, never by exhausting a
// switch.
//
// Every value carries a `run-` prefix, and the third sub-namespace is
// load-bearing rather than cosmetic. trailAbsent / trailAborted are the scan's
// INPUT STATES; trailGateNoTrailer / trailGateScanAborted are the GATE's view of
// them; these are the RUN's view. Three spaces now mean nearly the same words, so
// a copy-paste between them must be a visible mistake rather than a plausible
// line.
const (
	// trailOutcomeRunningAtTrailer: an admissible attribution proves the process
	// group was alive when the trailer was written. THE FINDING, and the only
	// path to one.
	trailOutcomeRunningAtTrailer = "run-running-at-trailer"
	// trailOutcomeMatchedUnattributed: a row matched the argv scan and nothing
	// was attributed. Says a row matched and the attribution is silent — no more.
	// Never "it was leaked", and never "it exited".
	trailOutcomeMatchedUnattributed = "run-matched-not-attributed"
	// trailOutcomeNoRowMatched: the scan ran over well-formed rows and none
	// matched. A statement about THE SCAN, not about the command having exited —
	// the reap is expected to have already run by observation time, so a clean
	// negative here is the predicted reading on a healthy run and on a leaking one
	// alike. There is deliberately no "exited normally" value in this space for it
	// to decay into.
	trailOutcomeNoRowMatched = "run-scan-matched-no-row"
	// trailOutcomeVoidBudgetFired: the trailer reports a budget-fired run. The
	// Terminate hook reaped INSIDE the hook (runner.go:492-503), BEFORE the
	// trailer, so no attribution on that path could prove aliveness-at-trailer.
	// Not a negative: reporting a scan-side answer here would imply a better
	// instrument could have proved something.
	trailOutcomeVoidBudgetFired = "run-void-budget-fired"
	// trailOutcomeVoidNoTrailer: no trailer line was written, so there is no
	// "when the turn was declared finished" instant to speak of.
	trailOutcomeVoidNoTrailer = "run-void-no-trailer"
	// trailOutcomeVoidTrailerScanAborted: the trailer scan aborted — the
	// instrument's own breakage, never an answer about pyry. Kept apart from
	// trailOutcomeVoidNoTrailer because a line past bufio.Scanner's 64 KiB
	// default and a genuine absence are otherwise indistinguishable, which is
	// #1266's whole reason for existing.
	trailOutcomeVoidTrailerScanAborted = "run-void-trailer-scan-aborted"
	// trailOutcomeVoidPyryDidNotExit: pyry did not exit within its deadline, so
	// every staged reading is about a live pyry.
	trailOutcomeVoidPyryDidNotExit = "run-void-pyry-did-not-exit"
	// trailOutcomeVoidArgvScanErrored: the argv scan errored as an instrument. A
	// failed scan must neither relabel a genuine match nor suppress a genuine
	// "the scan ran and no row matched".
	trailOutcomeVoidArgvScanErrored = "run-void-argv-scan-errored"
	// trailOutcomeVoidNoRowsParsed: the scan ran and parsed no well-formed rows
	// at all. Distinct from the value above AND from trailOutcomeNoRowMatched:
	// pinScanArgv returns the ZERO pinScan on error (process_pin_liveness_test.go:191-196),
	// so RowsScanned == 0 cannot by itself separate "the scan never ran" from
	// "the scan ran and parsed nothing", and all three would otherwise read as an
	// empty match set.
	trailOutcomeVoidNoRowsParsed = "run-void-no-rows-parsed"
	// trailOutcomeVoidLivenessInstrument: a per-pid read failed as an instrument
	// rather than answering. A half-run instrument publishing an absence is a
	// measured defect (#1230 PR #1232), and collapsing it into "no row matched"
	// would manufacture a clean negative out of the instrument's breakage.
	trailOutcomeVoidLivenessInstrument = "run-void-liveness-instrument-failed"
	// trailOutcomeVoidPathOwesNoReason: the trailer carries no terminal_reason
	// and the observed runner path owes none, so the gate read
	// trailGateAbsentOwesNone. A READING of the trailer rather than a defect in
	// it — and still a void, because nothing was certified: with no terminal
	// reason there is no "when the turn was declared finished" instant for a
	// claim about aliveness-at-trailer to be about.
	//
	// Deliberately NOT trailOutcomeVoidNoTrailer. A trailer WAS written on this
	// path — it is claude's own result line — and that value's doc reads "no
	// trailer line was written, so there is no 'when the turn was declared
	// finished' instant to speak of". Collapsing the two would report a line that
	// exists as one that does not, which is the class of collapse this space's
	// third sub-namespace exists to make visible.
	//
	// Distinct from trailOutcomeOutOfContract for the reason #1417 exists: that
	// value says the CALLER's record is not a reading, and reporting a healthy
	// headless stream run as one files a genuine measurement as an instrument
	// defect.
	trailOutcomeVoidPathOwesNoReason = "run-void-path-owes-no-reason"
	// trailOutcomeVoidReasonNotOwedByPath: the trailer CARRIES a terminal_reason
	// and the observed runner path owes none, so the gate read
	// trailGatePresentOwesNone. A READING of the trailer rather than a defect in
	// it — streamrunner.Run passes claude's bytes through unchanged
	// (internal/agentrun/streamrunner/runner.go:177-179), so the line is genuinely
	// what the run produced — and still a void, because nothing was certified:
	// with no certified terminal reason there is no "when the turn was declared
	// finished" instant for a claim about aliveness-at-trailer to be about.
	//
	// It says the line is not that path's documented healthy shape and NEVER that
	// pyry wrote it, inheriting the claim limit the gate value's own doc states
	// (trailer_admissibility_test.go, trailGatePresentOwesNone) and never widening
	// it: a run-level value claiming authorship would let claude's own output name
	// pyry as its author, one layer further from the bytes than the gate.
	//
	// Deliberately NOT trailOutcomeVoidNoTrailer. A trailer WAS written — it is
	// claude's own result line — and that value's doc reads "no trailer line was
	// written, so there is no 'when the turn was declared finished' instant to
	// speak of". Collapsing the two would report a line that exists as one that
	// does not.
	//
	// Deliberately NOT trailOutcomeVoidPathOwesNoReason, its sibling. That one is
	// the trailer carrying NO terminal_reason where the path owes none, which is
	// that path's documented healthy shape; this one is a reason PRESENT where the
	// path owes none, which is not. Same void-ness, two different readings, and a
	// published record that could not separate them would report the healthy shape
	// and the unexpected one under one name.
	//
	// Distinct from trailOutcomeOutOfContract for the reason #1434 exists: that
	// value says the CALLER's record is not a reading, and reporting a genuine
	// measurement as one files it as an instrument defect.
	trailOutcomeVoidReasonNotOwedByPath = "run-void-reason-not-owed-by-path"
	// trailOutcomeOutOfContract: an input value outside the closed set its own
	// producer documents, or a pair no correct composition can produce. A void by
	// default here would let a caller's bug read as a MEASURED void — the same
	// defect #1270's two out-of-contract values exist to prevent, one layer up.
	trailOutcomeOutOfContract = "run-out-of-contract"
)

// --- the records --------------------------------------------------------------

// trailRunReadings is one probe run's observations: DISCRIMINATORS AND
// ALREADY-PUBLISHABLE RECORDS ONLY, per the file header's doctrine. Nothing here
// is a value from which trailScanResult's trailer pointer is reachable, directly
// or through struct embedding, and nothing here carries a command string.
type trailRunReadings struct {
	// Gate is #1270's trailer-side admissibility result, taken WHOLE because it
	// is documented trap-free by construction.
	Gate trailGateResult
	// Admit is #1270's attribution-side admissibility result, taken whole for the
	// same reason. Its ZERO VALUE means "not classified", which is what a correct
	// consumer leaves behind when the gate certified no reason to hand the
	// predicate — see contract checks C3 and C4.
	Admit trailAdmitResult
	// ArgvScanErrored records THAT the argv scan failed, not what it said. Its
	// zero value points in the unsafe direction ("the scan is trustworthy"), which
	// is worth naming: pinScanArgv returns the ZERO pinScan on error, so a
	// forgotten flag arrives with both counts at zero and lands on the
	// no-rows-parsed VOID rather than on an answer. Void-to-misnamed-void is the
	// whole exposure, and C9 catches the hand-built inconsistency that would
	// escape even that.
	ArgvScanErrored bool
	// MatchCount and RowsScanned are pinScan's two counts, carried instead of its
	// Matches slice, which holds verbatim argv. A MatchCount above 1 is an
	// ORDINARY INPUT: the scan deliberately refuses to resolve "the" pid, because
	// a live run has matched more than one row for a single held command.
	MatchCount  int
	RowsScanned int
	// Liveness is the per-pid read set. pinStateOutcome carries no command column
	// by construction (pinStateColumns is `pid=,ppid=,stat=`), which is what makes
	// it admissible as an input here.
	Liveness []pinStateOutcome
	// PyryExited records whether pyry exited within its deadline. Its zero value
	// points the SAFE way: an unfilled field reads as "did not exit", which is a
	// void.
	PyryExited bool
	// BoundFrom is #1266's lateness discriminator, taken as a PLAIN VALUE and
	// never as the trailObservation that carries it — the observation embeds
	// trailScanResult, so taking it would promote that pointer back into reach.
	// Staleness is deliberately absent: trailBoundFromStart carries a real
	// duration that bounds NOTHING, so a classifier that could read a staleness
	// could be tempted to discriminate on it.
	BoundFrom string
	// ClaudeState is the claude-still-alive reading: a pinIsVerdict value, or ""
	// for "not read". CORROBORATION ONLY — it is blind to the reap, so in exactly
	// the window the probe exists to catch it reports "not late".
	ClaudeState string
}

// trailRunOutcome is the answer plus the provenance a reader needs to interpret
// it. COUNTS, NEVER ROWS; no command string; nothing from which the trailer
// pointer is reachable.
//
// The content rule for Detail is pinned rather than left to judgement: it MAY
// name outcome values, gate and admit values, counts, pids, verdicts and
// BoundFrom; it may NEVER quote Gate.Detail, Admit.Detail, a pinStateOutcome's
// Detail or its ToolStderr. That is the rule most easily broken by copying
// tdnDecideAfter, which does quote out.Detail (teardown_liveness_probe_test.go:678)
// — legitimately, because its record is not this one.
// TestTrailRunOutcomeCarriesNoCapturedBytes is the enforcing test, and it is why
// the needle goes into four inputs rather than one.
type trailRunOutcome struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
	Gate   string `json:"gate_value"`
	Admit  string `json:"admit_value,omitempty"`
	// MatchCount and RowsScanned are the argv scan's provenance. Counts only: the
	// matched rows themselves stay out of every published record.
	MatchCount  int `json:"match_count"`
	RowsScanned int `json:"rows_scanned"`
	// Bounded is BoundFrom == trailBoundFromMiss and nothing else.
	// trailBoundFromStart carries a real duration that bounds nothing and
	// trailBoundNone is the honest no-bound, so a record deriving this from
	// Staleness != 0 would publish a non-bound wearing a bound's label.
	Bounded         bool   `json:"lateness_bounded"`
	BoundFrom       string `json:"lateness_bound_from"`
	LivenessSummary string `json:"liveness,omitempty"`
	ClaudeState     string `json:"claude_state,omitempty"`
}

// --- membership helpers -------------------------------------------------------

// trailIsRunOutcome reports whether v is one of the thirteen recorded outcomes. It
// mirrors trailIsGateValue / trailIsAdmitValue / pinIsVerdict / tdnIsReapVerdict
// and exists for the same reason: a value a reader of the published record
// cannot look up is a verdict they cannot interpret.
func trailIsRunOutcome(v string) bool {
	switch v {
	case trailOutcomeRunningAtTrailer, trailOutcomeMatchedUnattributed, trailOutcomeNoRowMatched,
		trailOutcomeVoidBudgetFired, trailOutcomeVoidNoTrailer, trailOutcomeVoidTrailerScanAborted,
		trailOutcomeVoidPyryDidNotExit, trailOutcomeVoidArgvScanErrored,
		trailOutcomeVoidNoRowsParsed, trailOutcomeVoidLivenessInstrument,
		trailOutcomeVoidPathOwesNoReason, trailOutcomeVoidReasonNotOwedByPath,
		trailOutcomeOutOfContract:
		return true
	}
	return false
}

// trailIsBoundFrom reports whether v is one of #1266's three recorded lateness
// discriminators. "" is an unfilled field and not one of them — trailBoundNone
// is the honest report for "no bound was measured".
func trailIsBoundFrom(v string) bool {
	switch v {
	case trailBoundFromMiss, trailBoundFromStart, trailBoundNone:
		return true
	}
	return false
}

// --- the classifier -----------------------------------------------------------

// trailClassifyRun maps one run's observations onto exactly one outcome.
//
// Pure over its input: no exec, no clock, no filesystem, no *testing.T, and it
// never fails a test — the same contract as trailScan, trailGate,
// trailAdmitAttribution, tdnClassifyReapLog, pinReadState and fifoLiveRead,
// because an instrument failure observed mid-turn is a datum to publish, not a
// reason to abort the turn. That purity is also what lets every arm be driven
// offline from fixtures.
//
// # Contract block first, so no arm is a fall-through
//
// Nine checks, each returning trailOutcomeOutOfContract with its own Detail.
// Same structural idea as trailGate and trailAdmitAttribution: the
// out-of-contract value is a GUARD AT THE TOP, so every later arm's precondition
// is true by construction. C1 and C3 CALL #1270's shipped membership predicates
// rather than re-deriving them; C7 calls pinIsVerdict.
//
// There is deliberately NO alignment check between len(Liveness) and MatchCount.
// tdnDecideAfter needs one because it indexes liveness against a pinned-pid list;
// this classifier never indexes, so a length mismatch degrades the corroboration
// summary and cannot mislabel a decision. No such failure has been observed, and
// a defence for an unobserved failure mode is a cost with no evidence behind it.
//
// # Then decide, in this order
//
//  1. A GATE THAT IS NOT USABLE ANSWERS FIRST, and that ordering is forced rather
//     than chosen: without a usable trailer there is no certified instant for any
//     claim to be about, and C5 has already made a proof unreachable from every
//     one of those values.
//  2. AN ADMISSIBLE ATTRIBUTION IS THE FINDING, consulted BEFORE any
//     point-in-time reading, because those readings are expected to be late and
//     must never be what the verdict rests on.
//  3. Pyry not exiting voids everything below it, because every reading below is
//     staged after teardown and means nothing before it.
//  4. The argv scan's own failure, then
//  5. its having parsed no rows at all, then
//  6. a liveness read that failed as an instrument rather than answering, then
//  7. a match with nothing attributed, and
//  8. otherwise: the scan ran and no row matched.
//
// # Why the proof outranks the pyry-exit void, against the neighbouring rig
//
// tdnDecideAfter's rig skips the run when pyry misses its exit grace
// (teardown_liveness_probe_test.go:430-437, "any after-reading would be about a
// live pyry") — and it is right to, because ITS verdict rests on the
// after-snapshot. This classifier's proof does not: it rests on an ordering
// argument internal to the run's own logs, that the trailer was written and then
// the reaper named the group. Voiding it for a staging failure would SUPPRESS A
// FINDING THE RUN GENUINELY ESTABLISHED. The proof arm's own preconditions rule
// out the confusion a still-running pyry could introduce — a second agent run's
// reap would push LineCount to 2, which trailAdmitAttribution answers with
// trailAdmitVoidNotOneReapLine, never proof.
//
// # Why the liveness instrument void is a void and the liveness verdicts are not
//
// An instrument failure is the ABSENCE of a reading, so it is a named
// nothing-was-measured. A VERDICT — running, zombie, no-such-process — is
// corroboration and never moves the outcome, which is why step 6 tests one value
// and ignores the other three. ClaudeState is not tested at all, even when it is
// pinStateInstrumentFailed: that reading certifies nothing on any value it can
// take, so its failure removes nothing from the answer.
func trailClassifyRun(readings trailRunReadings) trailRunOutcome {
	// The provenance every arm carries, built once. Counts, discriminators and
	// the rendered per-pid summary — no Detail from any input, no command string,
	// nothing from which the trailer pointer is reachable.
	out := trailRunOutcome{
		Gate:            readings.Gate.Value,
		Admit:           readings.Admit.Value,
		MatchCount:      readings.MatchCount,
		RowsScanned:     readings.RowsScanned,
		Bounded:         readings.BoundFrom == trailBoundFromMiss,
		BoundFrom:       readings.BoundFrom,
		LivenessSummary: tdnVerdictSummary(readings.Liveness),
		ClaudeState:     readings.ClaudeState,
	}
	decide := func(value, format string, args ...any) trailRunOutcome {
		out.Value = value
		out.Detail = trailDetail(format, args...)
		return out
	}

	// --- the contract block ---

	// C1. A CALL, not a re-derivation: trailIsGateValue is #1270's shipped
	// membership predicate over its own value space — seven of them since #1434 —
	// and this layer is not entitled to a second opinion about them. Catches the
	// zero trailGateResult.
	if !trailIsGateValue(readings.Gate.Value) {
		return decide(trailOutcomeOutOfContract, "gate value %q is not one of the seven trailGate "+
			"documents (%s / %s / %s / %s / %s / %s / %s), so this record is not a reading. "+
			"Reported out of contract rather than as a void, which would let a caller's bug read "+
			"as a measured void", readings.Gate.Value, trailGateUsable, trailGateNoTrailer,
			trailGateScanAborted, trailGateBudgetFired, trailGateAbsentOwesNone,
			trailGatePresentOwesNone, trailGateOutOfContract)
	}

	// C2. #1270 pins this invariant on its own output, so re-checking it here is
	// a check on the FIXTURE — which is exactly what a hand-built
	// trailGateResult{Value: trailGateUsable} with no reason is.
	certifies := readings.Gate.Value == trailGateUsable ||
		readings.Gate.Value == trailGateBudgetFired
	if certifies != (readings.Gate.Reason != "") {
		return decide(trailOutcomeOutOfContract, "gate value %s carries certified reason %q: a "+
			"reason is non-empty exactly on %s and %s, so this pair is one trailGate cannot emit. "+
			"Certifying an empty reason would reintroduce, two layers up, the defect the nil "+
			"Trailer pointer was chosen to prevent", readings.Gate.Value, readings.Gate.Reason,
			trailGateUsable, trailGateBudgetFired)
	}

	// C3. A certifying gate is exactly the condition under which a correct
	// consumer calls the predicate, so an unclassified attribution here is
	// unproducible: tdnClassifyReapLog is pure over already-captured bytes and
	// ALWAYS returns a record, instrument-failed included.
	if certifies && !trailIsAdmitValue(readings.Admit.Value) {
		return decide(trailOutcomeOutOfContract, "gate value %s certified reason %q, so the "+
			"attribution predicate was owed a call, but the admissibility value is %q rather than "+
			"one of the seven trailAdmitAttribution documents. There is no run condition under "+
			"which a certifying gate arrives with an unclassified attribution",
			readings.Gate.Value, readings.Gate.Reason, readings.Admit.Value)
	}

	// C4. The other half of the same composition, and the one that carries
	// weight: this is TestTrailGateThenAdmit's test-level obligation made a
	// CHECKED CONTRACT at the layer that holds both values.
	if !certifies && readings.Admit.Value != "" {
		return decide(trailOutcomeOutOfContract, "gate value %s certifies no reason, so the "+
			"attribution predicate had nothing to be handed, yet an admissibility value %q is "+
			"present. Passing an uncertified run to that predicate would judge it as if the gate "+
			"had approved it", readings.Gate.Value, readings.Admit.Value)
	}

	// C5. Proof is producible only from a usable gate: a budget-fired gate
	// certifies max_turns, which trailAdmitAttribution answers with its budget
	// void. Together with C4 this closes the false-proof path in both
	// directions.
	if readings.Admit.Value == trailAdmitProof && readings.Gate.Value != trailGateUsable {
		return decide(trailOutcomeOutOfContract, "the attribution reads %s under gate value %s, a "+
			"pair no correct composition can produce: only %s can certify a reason that is not "+
			"%q, and every other certified reason reaches %s instead", trailAdmitProof,
			readings.Gate.Value, trailGateUsable, trailBudgetTerminalReason,
			trailAdmitVoidBudgetFired)
	}

	// C6. "" is an unfilled field, not a reading: trailBoundNone is the honest
	// report for "no bound was measured".
	if !trailIsBoundFrom(readings.BoundFrom) {
		return decide(trailOutcomeOutOfContract, "lateness discriminator %q is not one of the "+
			"three trailWaitForTrailer documents (%s / %s / %s), so whether anything bounds this "+
			"run's lateness is unknown rather than reported. An unfilled field is not %s",
			readings.BoundFrom, trailBoundFromMiss, trailBoundFromStart, trailBoundNone,
			trailBoundNone)
	}

	// C7. pinIsVerdict is #1235's shipped membership predicate over the four
	// per-pid states — called rather than re-switched, for C1's reason.
	for _, read := range readings.Liveness {
		if !pinIsVerdict(read.Verdict) {
			return decide(trailOutcomeOutOfContract, "the per-pid read for pid %d carries verdict "+
				"%q, which is not one of the four pinReadState documents (%s / %s / %s / %s), so "+
				"this record is not a reading", read.PID, read.Verdict, pinStateRunning,
				pinStateExitedNotReaped, pinStateNoSuchProcess, pinStateInstrumentFailed)
		}
	}
	if readings.ClaudeState != "" && !pinIsVerdict(readings.ClaudeState) {
		return decide(trailOutcomeOutOfContract, "the claude-still-alive reading carries verdict "+
			"%q, which is not one of the four pinReadState documents and is not \"\" for "+
			"not-read. Corroboration a reader cannot look up is corroboration they cannot "+
			"interpret", readings.ClaudeState)
	}

	// C8. pinPartition can emit none of these three. {MatchCount: 1} with
	// RowsScanned unfilled is the likeliest hand-typed fixture, and without this
	// check it reaches an ANSWER rather than a void.
	if readings.MatchCount < 0 || readings.RowsScanned < 0 ||
		readings.MatchCount > readings.RowsScanned {
		return decide(trailOutcomeOutOfContract, "the argv scan reports %d match(es) across %d "+
			"row(s) scanned, a triple pinPartition cannot emit: neither count is ever negative "+
			"and MatchCount is len(Matches) over the rows that were scanned. Reported out of "+
			"contract rather than as an answer about the match set", readings.MatchCount,
			readings.RowsScanned)
	}

	// C9. pinScanArgv returns the ZERO pinScan on error
	// (process_pin_liveness_test.go:191-196), so an errored scan reporting counts
	// is a hand-built inconsistency — the one that would escape the safe
	// void-to-void landing an unfilled ArgvScanErrored produces.
	if readings.ArgvScanErrored && (readings.MatchCount != 0 || readings.RowsScanned != 0) {
		return decide(trailOutcomeOutOfContract, "the argv scan is recorded as errored yet "+
			"reports %d match(es) across %d row(s), a pair pinScanArgv cannot emit: it discards "+
			"its partial output and returns the zero pinScan on error", readings.MatchCount,
			readings.RowsScanned)
	}

	// --- the decision ---

	// Step 1: the trailer-side answers. Structural, so they outrank everything —
	// without a usable trailer there is no certified instant for a claim to be
	// about. No default arm: C1 proved membership, so these seven cases are total,
	// and a NEW gate value registered in trailIsGateValue but not handled here
	// would not be caught by a bottom-of-function catch-all — the shape this file
	// exists to refuse. #1417 is what turned that open question into a landed
	// change, and it also corrects the shipped shorthand for where such a value
	// FALLS: not step 2. For any value that certifies nothing, C4 has already
	// rejected an admissibility value arriving beside it, so Admit is forced empty
	// and step 2's trailAdmitProof test cannot fire. An unhandled arm falls
	// through to steps 3-8 and awards a SCAN-SIDE ANSWER ABOUT PYRY from a record
	// the gate says certifies nothing — the same hazard class, one step further
	// down. Every value in TestTrailAdmissibilityConstantsAreClosed's union map
	// has an arm here, and that map catches a colliding value rather than an
	// unhandled one, so this comment is the only place the obligation is written.
	switch readings.Gate.Value {
	case trailGateUsable:
		// The one value that answers nothing on its own: a usable trailer
		// certifies the instant the rest of the decision is about.
	case trailGateBudgetFired:
		return decide(trailOutcomeVoidBudgetFired, "the trailer certifies terminal reason %q, so "+
			"the Terminate hook reaped INSIDE the hook (runner.go:492-503) BEFORE the trailer was "+
			"written and no attribution on that path could prove aliveness-at-trailer. A "+
			"STRUCTURAL void, not a negative: reporting a scan-side answer here would imply a "+
			"better instrument could have proved something", readings.Gate.Reason)
	case trailGateNoTrailer:
		return decide(trailOutcomeVoidNoTrailer, "no trailer line was written, so there is no "+
			"instant at which pyry declared the turn finished for this run to be about. Kept "+
			"apart from %s: this is a statement about the bytes, that one is the instrument "+
			"reporting it could not read them", trailOutcomeVoidTrailerScanAborted)
	case trailGateScanAborted:
		return decide(trailOutcomeVoidTrailerScanAborted, "the trailer scan aborted, so the bytes "+
			"were unreadable. The instrument's own breakage and never an answer about pyry, which "+
			"is why it is kept apart from %s — a line past bufio.Scanner's 64 KiB default and a "+
			"genuine absence are otherwise indistinguishable", trailOutcomeVoidNoTrailer)
	case trailGateAbsentOwesNone:
		// A reading, and still a void. The gate read an absent terminal_reason
		// from a path that owes none, so nothing was certified and there is no
		// declared-finished instant for a claim to be about — but the trailer was
		// written and the record IS a reading, which is why this is neither
		// trailOutcomeVoidNoTrailer nor trailOutcomeOutOfContract.
		return decide(trailOutcomeVoidPathOwesNoReason, "the trailer carries no terminal_reason "+
			"and the runner path this run was OBSERVED to take owes none, so the gate read %s: "+
			"a reading of the trailer rather than a defect in it. Nothing is certified, so "+
			"there is no declared-finished instant for this run to be about. Kept apart from "+
			"%s, which reports that no trailer line was written at all, and from %s, which "+
			"would file a measurement as the caller's bug", trailGateAbsentOwesNone,
			trailOutcomeVoidNoTrailer, trailOutcomeOutOfContract)
	case trailGatePresentOwesNone:
		// #1434's arm, and the mirror of the one above it. The gate read a
		// terminal_reason that IS on the line from a path owing none, so the
		// record is a reading and not a caller's bug — but nothing was certified,
		// so there is no declared-finished instant for a claim to be about. The
		// two readings are kept apart because one is that path's documented
		// healthy shape and the other is not, and a record collapsing them would
		// publish the expected shape and the unexpected one under one name.
		return decide(trailOutcomeVoidReasonNotOwedByPath, "the trailer CARRIES a terminal_reason "+
			"and the runner path OBSERVED for this run owes none, so the gate read %s: a READING "+
			"of the trailer, and never a claim that pyry wrote the line. Nothing is certified, so "+
			"there is no declared-finished instant for this run to be about. Kept apart from %s, "+
			"that path with NO reason and its healthy shape, and from %s, which would file a "+
			"measurement as the caller's bug", trailGatePresentOwesNone,
			trailOutcomeVoidPathOwesNoReason, trailOutcomeOutOfContract)
	case trailGateOutOfContract:
		// Not a collapse: the gate already said "this record is not a reading",
		// and the run-level answer is that same sentence. What may never share a
		// value is two MEASURED nothings, and none of the ten voids does.
		return decide(trailOutcomeOutOfContract, "the trailer gate itself reported %s, so the "+
			"input it was handed is not a reading and no instant is certified. The gate's own "+
			"Detail names which of its sub-cases fired", trailGateOutOfContract)
	}

	// Step 2: the deterministic proof, consulted BEFORE any point-in-time
	// reading. Those readings are expected to be late — the reap completes in the
	// time of one ps exec while the observation of the trailer trails the write
	// by up to a poll interval — so resting a verdict on them manufactures a
	// systematic false negative.
	//
	// Every Detail below names only what DECIDED — the record's own
	// LivenessSummary and ClaudeState fields carry the per-pid readings once, so
	// restating them here would spend cap on a duplicate and, at the cap, cut off
	// the argument the Detail exists to make.
	if readings.Admit.Value == trailAdmitProof {
		return decide(trailOutcomeRunningAtTrailer, "pyry's own reap log names the held group on "+
			"exactly one anchored line under certified terminal reason %q, and emitter.Close() "+
			"wrote the trailer (runner.go:479-485) before the reap defer (:398) SIGKILLed it. The "+
			"group was alive strictly AFTER the trailer was written, and therefore alive when it "+
			"was written. The point-in-time readings recorded alongside this answer are "+
			"corroboration and are EXPECTED to be late, so they do not move it",
			readings.Gate.Reason)
	}

	// Step 3: every reading below is staged after teardown and means nothing
	// before it. Ranked BELOW the proof deliberately, against the neighbouring
	// rig's ordering — see the doc comment.
	if !readings.PyryExited {
		return decide(trailOutcomeVoidPyryDidNotExit, "pyry did not exit within its deadline, so "+
			"every reading staged after teardown is about a LIVE pyry and none of them answers "+
			"the question. Recorded as a staging fault rather than as anything about the "+
			"command; the attribution read %q, which is not %s", readings.Admit.Value,
			trailAdmitProof)
	}

	// Step 4. Its counts are zero by pinScanArgv's contract, which is exactly why
	// this cannot share a value with step 5.
	if readings.ArgvScanErrored {
		return decide(trailOutcomeVoidArgvScanErrored, "the argv scan errored as an instrument, "+
			"so no match set exists to report. A failed scan must neither relabel a genuine match "+
			"nor suppress a genuine \"the scan ran and no row matched\", so it is its own void "+
			"and never %s or %s", trailOutcomeNoRowMatched, trailOutcomeVoidNoRowsParsed)
	}

	// Step 5. Reachable only with the scan not errored, which is AC4's
	// separation: pinScanArgv's zero-on-error means RowsScanned == 0 cannot by
	// itself tell these two apart.
	if readings.RowsScanned == 0 {
		return decide(trailOutcomeVoidNoRowsParsed, "the argv scan ran without error and parsed "+
			"no well-formed rows at all, so it read nothing to match against. Distinct from %s, "+
			"which reports the same zero counts because the scan discards its output on error, "+
			"and from %s, which is a statement about rows that WERE scanned",
			trailOutcomeVoidArgvScanErrored, trailOutcomeNoRowMatched)
	}

	// Step 6. An instrument failure is the ABSENCE of a reading, so it is a named
	// nothing-was-measured; the other three verdicts are corroboration and never
	// reach here.
	for _, read := range readings.Liveness {
		if read.Verdict == pinStateInstrumentFailed {
			return decide(trailOutcomeVoidLivenessInstrument, "the per-pid read for pid %d failed "+
				"as an instrument rather than answering; every verdict is in this record's "+
				"liveness field. A half-run instrument publishing an absence is a measured "+
				"defect, and collapsing this into %s would manufacture a clean negative out of "+
				"the instrument's own breakage", read.PID, trailOutcomeNoRowMatched)
		}
	}

	// Step 7.
	if readings.MatchCount > 0 {
		return decide(trailOutcomeMatchedUnattributed, "the argv scan matched %d of %d row(s) "+
			"scanned and the attribution read %q, which is not %s. This says a row matched and "+
			"the attribution is silent — no more. An inadmissible attribution is NEVER EVIDENCE "+
			"THAT THE GROUP HAD EXITED, and it is not a leak finding either: a miss is consistent "+
			"with the group exiting and with the reaper never reaching it", readings.MatchCount,
			readings.RowsScanned, readings.Admit.Value, trailAdmitProof)
	}

	// Step 8.
	return decide(trailOutcomeNoRowMatched, "the argv scan read %d well-formed row(s) and none "+
		"matched. A statement about THE SCAN and NOT A STATEMENT THAT THE COMMAND HAD EXITED: the "+
		"reap completes in the time of one ps exec after the trailer is written, so a clean "+
		"negative at observation time is the PREDICTED reading on a healthy run and on a leaking "+
		"one alike. The per-pid readings recorded alongside it certify nothing",
		readings.RowsScanned)
}

// --- fixtures -----------------------------------------------------------------

// trailRunCase is one set of readings and the outcome it must reach.
type trailRunCase struct {
	name string
	in   trailRunReadings
	want string
}

// trailRunWellFormed is a set of readings that violates no contract check and
// reaches trailOutcomeNoRowMatched. Rows below vary ONE thing from it, so a row
// that goes red names the input it varied rather than the whole record.
//
// It is a function rather than a package-level value because Liveness is a slice:
// a shared backing array would let one row's mutation reach another's.
func trailRunWellFormed() trailRunReadings {
	return trailRunReadings{
		Gate:        trailGateResult{Value: trailGateUsable, Reason: "completed", Detail: "usable"},
		Admit:       trailAdmitResult{Value: trailAdmitVoidGroupUnnamed, Detail: "a void"},
		MatchCount:  0,
		RowsScanned: 12,
		Liveness:    []pinStateOutcome{{Verdict: pinStateNoSuchProcess, PID: 4242}},
		PyryExited:  true,
		BoundFrom:   trailBoundFromMiss,
		ClaudeState: pinStateNoSuchProcess,
	}
}

// trailRunProofReadings is the same record with an admissible attribution, so it
// reaches trailOutcomeRunningAtTrailer.
func trailRunProofReadings() trailRunReadings {
	in := trailRunWellFormed()
	in.Admit = trailAdmitResult{Value: trailAdmitProof, Detail: "the proof"}
	in.MatchCount = 1
	return in
}

// trailRunAbsentOwesNoneReadings is the same record with #1417's gate answer,
// built by the REAL producers rather than hand-typed: trailGate over
// trailGateAbsentReasonScan() under a reading tdnRunnerFromArgv reduces to
// streamrunner. That is the only shape that reaches trailGateAbsentOwesNone, and
// driving the shipped gate is what keeps this fixture from surviving a change to
// which reading the arm answers on.
//
// Admit is left ZERO deliberately, and that is not a shortcut: it is exactly what
// a correct consumer leaves behind when the gate certified no reason to hand the
// predicate, and C4 rejects anything else. Which is also why no test of this arm
// may hand the classifier trailAdmitProof beside it — C4 and C5 would answer
// first and the test would re-prove the contract block instead of the arm.
//
// A function rather than a package-level var, for trailRunWellFormed()'s reason:
// Liveness is a slice and go test -race runs this package's tests in parallel.
func trailRunAbsentOwesNoneReadings() trailRunReadings {
	in := trailRunWellFormed()
	in.Gate = trailGate(trailGateInput{Scan: trailGateAbsentReasonScan(),
		RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv)})
	in.Admit = trailAdmitResult{}
	in.MatchCount = 1
	return in
}

// trailRunPresentOwesNoneReadings is the PRESENCE side of the same fixture, and
// #1434's: trailGate over trailGateUsableScan() — a trailer whose terminal_reason
// IS on the line — under a reading tdnRunnerFromArgv reduces to streamrunner. That
// is the only shape that reaches trailGatePresentOwesNone, and driving the shipped
// gate rather than hand-typing the value is what keeps this fixture from surviving
// a change to which reading the arm answers on.
//
// The SAME scan reaches trailGateUsable under a ptyrunner or an indeterminate
// reading, which is why the reading is the whole of what separates this helper from
// a usable one — and why it is built here rather than by mutating a value.
//
// Admit is left ZERO for trailRunAbsentOwesNoneReadings()' reason, which applies
// unchanged: it is what a correct consumer leaves behind when the gate certified no
// reason to hand the predicate, and C4 rejects anything else. No test of this arm
// may hand the classifier trailAdmitProof beside it.
//
// A function rather than a package-level var, for trailRunWellFormed()'s reason:
// Liveness is a slice and go test -race runs this package's tests in parallel.
func trailRunPresentOwesNoneReadings() trailRunReadings {
	in := trailRunWellFormed()
	in.Gate = trailGate(trailGateInput{Scan: trailGateUsableScan(),
		RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv)})
	in.Admit = trailAdmitResult{}
	in.MatchCount = 1
	return in
}

// trailRunCases returns every input under test. Answers, voids, the two ordering
// regressions, the late-read row, AC4's separation pair, and one row per contract
// check C1-C9.
func trailRunCases() []trailRunCase {
	// The one row built end to end by the REAL producers, so the happy path stays
	// pinned to what the shipped functions emit rather than to a hand-typed
	// approximation of them — the same reason TestTrailGate routes four rows
	// through trailScan.
	realGate := trailGate(trailGateInput{Scan: trailScan([]byte(trailFixtureTrailer + "\n")),
		RunnerPath: trailRunnerUnread()})
	realAdmit := trailAdmitAttribution(
		tdnClassifyReapLog([]byte(trailReapLine(1, "[7788]")+"\n"), 7788), realGate.Reason)
	fromProducers := trailRunWellFormed()
	fromProducers.Gate = realGate
	fromProducers.Admit = realAdmit
	fromProducers.MatchCount = 1

	matched := trailRunWellFormed()
	// A match set above one is an ORDINARY INPUT, not an error: pinScan.Matches is
	// a slice precisely because a live run has matched more than one row for a
	// single held command.
	matched.MatchCount = 3
	matched.RowsScanned = 40

	budget := trailRunWellFormed()
	budget.Gate = trailGateResult{Value: trailGateBudgetFired, Reason: trailBudgetTerminalReason,
		Detail: "budget-fired"}
	budget.Admit = trailAdmitResult{Value: trailAdmitVoidBudgetFired, Detail: "a structural void"}
	budget.MatchCount = 1

	noTrailer := trailRunWellFormed()
	noTrailer.Gate = trailGateResult{Value: trailGateNoTrailer, Detail: "no trailer line"}
	noTrailer.Admit = trailAdmitResult{}
	noTrailer.MatchCount = 1

	aborted := trailRunWellFormed()
	aborted.Gate = trailGateResult{Value: trailGateScanAborted, Detail: "the scan aborted"}
	aborted.Admit = trailAdmitResult{}
	aborted.MatchCount = 1

	pyryLive := trailRunWellFormed()
	pyryLive.PyryExited = false
	pyryLive.MatchCount = 1

	// AC4's separation pair. BOTH carry RowsScanned == 0, which is exactly why the
	// errored flag has to exist: pinScanArgv returns the ZERO pinScan on error, so
	// the counts alone cannot tell "the scan never ran" from "the scan ran and
	// parsed no well-formed rows".
	scanErrored := trailRunWellFormed()
	scanErrored.ArgvScanErrored = true
	scanErrored.RowsScanned = 0
	noRows := trailRunWellFormed()
	noRows.ArgvScanErrored = false
	noRows.RowsScanned = 0

	livenessBroken := trailRunWellFormed()
	livenessBroken.Liveness = []pinStateOutcome{
		{Verdict: pinStateNoSuchProcess, PID: 4242},
		{Verdict: pinStateInstrumentFailed, PID: 4243},
	}

	// The ordering regressions. Without these two rows, steps 2, 3 and 4 are
	// indistinguishable from any other permutation of themselves.
	proofPyryLive := trailRunProofReadings()
	proofPyryLive.PyryExited = false
	proofBrokenLiveness := trailRunProofReadings()
	proofBrokenLiveness.Liveness = []pinStateOutcome{{Verdict: pinStateInstrumentFailed, PID: 4243}}
	pyryLiveScanErrored := trailRunWellFormed()
	pyryLiveScanErrored.PyryExited = false
	pyryLiveScanErrored.ArgvScanErrored = true
	pyryLiveScanErrored.RowsScanned = 0

	// The systematic false negative, made executable: every point-in-time reading
	// says the group is gone and claude is up, and the deterministic proof still
	// wins.
	lateReads := trailRunProofReadings()
	lateReads.Liveness = []pinStateOutcome{
		{Verdict: pinStateNoSuchProcess, PID: 4242},
		{Verdict: pinStateNoSuchProcess, PID: 4243},
	}
	lateReads.ClaudeState = pinStateRunning

	// --- the contract rows, each named for the fixture mistake it catches ---

	zeroGate := trailRunWellFormed()
	zeroGate.Gate = trailGateResult{}
	zeroGate.Admit = trailAdmitResult{}

	unknownGate := trailRunWellFormed()
	unknownGate.Gate = trailGateResult{Value: "some-gate-value-nobody-defined", Reason: "completed"}

	usableNoReason := trailRunWellFormed()
	usableNoReason.Gate = trailGateResult{Value: trailGateUsable, Detail: "usable, unfilled reason"}

	abortedWithReason := trailRunWellFormed()
	abortedWithReason.Gate = trailGateResult{Value: trailGateScanAborted, Reason: "completed"}
	abortedWithReason.Admit = trailAdmitResult{}

	unclassifiedAdmit := trailRunWellFormed()
	unclassifiedAdmit.Admit = trailAdmitResult{}

	uncertifiedWithAdmit := trailRunWellFormed()
	uncertifiedWithAdmit.Gate = trailGateResult{Value: trailGateNoTrailer, Detail: "no trailer"}
	uncertifiedWithAdmit.Admit = trailAdmitResult{Value: trailAdmitVoidNoLine, Detail: "a void"}

	budgetWithProof := trailRunWellFormed()
	budgetWithProof.Gate = trailGateResult{Value: trailGateBudgetFired,
		Reason: trailBudgetTerminalReason}
	budgetWithProof.Admit = trailAdmitResult{Value: trailAdmitProof, Detail: "an impossible proof"}

	unfilledBound := trailRunWellFormed()
	unfilledBound.BoundFrom = ""

	inventedVerdict := trailRunWellFormed()
	inventedVerdict.Liveness = []pinStateOutcome{{Verdict: "some-verdict-nobody-defined", PID: 4242}}

	inventedClaudeState := trailRunWellFormed()
	inventedClaudeState.ClaudeState = "some-verdict-nobody-defined"

	unfilledRows := trailRunWellFormed()
	unfilledRows.MatchCount = 1
	unfilledRows.RowsScanned = 0

	negativeRows := trailRunWellFormed()
	negativeRows.RowsScanned = -1

	erroredWithRows := trailRunWellFormed()
	erroredWithRows.ArgvScanErrored = true
	erroredWithRows.RowsScanned = 12

	return []trailRunCase{
		// --- the three answers ---
		{
			name: "an admissible attribution built by the real producers proves aliveness at the trailer",
			in:   fromProducers,
			want: trailOutcomeRunningAtTrailer,
		},
		{
			name: "a row matched and nothing was attributed, which is never an it-exited verdict",
			in:   matched,
			want: trailOutcomeMatchedUnattributed,
		},
		{
			name: "the scan ran over well-formed rows and none matched",
			in:   trailRunWellFormed(),
			want: trailOutcomeNoRowMatched,
		},

		// --- the ten voids ---
		{
			name: "a budget-fired trailer voids the run structurally, never negatively",
			in:   budget,
			want: trailOutcomeVoidBudgetFired,
		},
		{
			name: "no trailer line means there is no declared-finished instant to speak of",
			in:   noTrailer,
			want: trailOutcomeVoidNoTrailer,
		},
		{
			name: "an aborted trailer scan is the instrument's breakage, kept apart from an absence",
			in:   aborted,
			want: trailOutcomeVoidTrailerScanAborted,
		},
		{
			name: "pyry missing its exit deadline voids every staged reading",
			in:   pyryLive,
			want: trailOutcomeVoidPyryDidNotExit,
		},
		{
			name: "an argv scan that errored neither relabels a match nor suppresses a clean negative",
			in:   scanErrored,
			want: trailOutcomeVoidArgvScanErrored,
		},
		{
			name: "a scan that ran and parsed no well-formed rows is not the same as one that errored",
			in:   noRows,
			want: trailOutcomeVoidNoRowsParsed,
		},
		{
			name: "a per-pid read that failed as an instrument is a void, never a clean negative",
			in:   livenessBroken,
			want: trailOutcomeVoidLivenessInstrument,
		},
		{
			// #1417's arm. Not named by the ticket and mandatory anyway: the
			// coverage loop in TestTrailClassifyRun errors on any value of
			// trailRunOutcomeValues() no row reaches, so #1417's own outcome
			// without a row is red there.
			name: "an absent terminal_reason from a path that owes none is a reading and a void",
			in:   trailRunAbsentOwesNoneReadings(),
			want: trailOutcomeVoidPathOwesNoReason,
		},
		{
			// #1434's arm, and its sibling above is why the row is worth having
			// twice over: the two differ only in whether the key is on the line,
			// and a table holding one of them would let a collapse of the pair go
			// unnoticed here. Mandatory for the row above's reason as well — the
			// coverage loop errors on any value of trailRunOutcomeValues() no row
			// reaches — and unlike #1417's, this one IS named by its ticket.
			name: "a named terminal_reason from a path that owes none is a reading and a void",
			in:   trailRunPresentOwesNoneReadings(),
			want: trailOutcomeVoidReasonNotOwedByPath,
		},

		// --- the ordering regressions ---
		{
			name: "the proof outranks the pyry-exit void, which the neighbouring rig orders the other way",
			in:   proofPyryLive,
			want: trailOutcomeRunningAtTrailer,
		},
		{
			name: "the proof outranks a broken per-pid instrument, which it never rested on",
			in:   proofBrokenLiveness,
			want: trailOutcomeRunningAtTrailer,
		},
		{
			name: "the staging fault outranks the instrument fault when neither is a proof",
			in:   pyryLiveScanErrored,
			want: trailOutcomeVoidPyryDidNotExit,
		},
		{
			name: "every point-in-time read says gone and claude says up, and the proof still holds",
			in:   lateReads,
			want: trailOutcomeRunningAtTrailer,
		},

		// --- the contract block, one row per check ---
		{
			name: "C1: the zero gate result is out of contract",
			in:   zeroGate,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C1: a gate value nobody defined is out of contract",
			in:   unknownGate,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C2: a usable gate carrying no certified reason is out of contract",
			in:   usableNoReason,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C2: a gate value that certifies nothing carrying a reason is out of contract",
			in:   abortedWithReason,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C3: a certifying gate arriving with an unclassified attribution is out of contract",
			in:   unclassifiedAdmit,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C4: a gate that certified nothing arriving with an admit value is out of contract",
			in:   uncertifiedWithAdmit,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C5: a budget-fired gate arriving with a proof is out of contract",
			in:   budgetWithProof,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C6: an unfilled lateness discriminator is out of contract",
			in:   unfilledBound,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C7: a liveness verdict nobody defined is out of contract",
			in:   inventedVerdict,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C7: a claude-state verdict nobody defined is out of contract",
			in:   inventedClaudeState,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C8: a match count with an unfilled rows-scanned is out of contract",
			in:   unfilledRows,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C8: a negative rows-scanned is out of contract",
			in:   negativeRows,
			want: trailOutcomeOutOfContract,
		},
		{
			name: "C9: an errored scan reporting rows is out of contract",
			in:   erroredWithRows,
			want: trailOutcomeOutOfContract,
		},
	}
}

// --- tests --------------------------------------------------------------------

// TestTrailClassifyRun is AC1's, AC4's and AC5's claim made executable: every
// outcome in the closed set — including the out-of-contract one — is reached by a
// table row, and no row reaches a value outside the space.
func TestTrailClassifyRun(t *testing.T) {
	reached := make(map[string]bool)

	for _, tc := range trailRunCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := trailClassifyRun(tc.in)

			if got.Value != tc.want {
				t.Fatalf("value: got %q (%s), want %q", got.Value, got.Detail, tc.want)
			}
			if !trailIsRunOutcome(got.Value) {
				t.Errorf("value: %q is outside the recorded outcome space", got.Value)
			}
			if got.Detail == "" {
				t.Error("empty detail: an outcome that cannot say which arm fired and why is " +
					"indistinguishable from a reading")
			}
			// Every Detail goes through trailDetail and is therefore capped by
			// reachCapCommand. The cap exists to bound CALLER-SCALED content —
			// a liveness summary over many pids — and a Detail whose own prose
			// spends the whole budget loses the argument it exists to make,
			// silently, at the end where the arguments live. These rows read at
			// most two pids, so a truncation here is this file's prose being too
			// long rather than the cap doing its job.
			if strings.Contains(got.Detail, reachTruncationMarker) {
				t.Errorf("detail is truncated at %d bytes, so its closing argument was cut: %s",
					reachMaxCommandBytes, got.Detail)
			}

			// The provenance a reader needs to interpret the value, asserted on
			// every row so no arm can quietly drop it.
			if got.Gate != tc.in.Gate.Value {
				t.Errorf("gate provenance: got %q, want %q", got.Gate, tc.in.Gate.Value)
			}
			if got.Admit != tc.in.Admit.Value {
				t.Errorf("admit provenance: got %q, want %q", got.Admit, tc.in.Admit.Value)
			}
			if got.MatchCount != tc.in.MatchCount || got.RowsScanned != tc.in.RowsScanned {
				t.Errorf("scan provenance: got %d of %d, want %d of %d", got.MatchCount,
					got.RowsScanned, tc.in.MatchCount, tc.in.RowsScanned)
			}
			// AC3's discriminator: bounded exactly on trailBoundFromMiss, and
			// never on a staleness the record cannot even see.
			if want := tc.in.BoundFrom == trailBoundFromMiss; got.Bounded != want {
				t.Errorf("bounded: got %t for BoundFrom %q, want %t — only %s bounds anything",
					got.Bounded, tc.in.BoundFrom, want, trailBoundFromMiss)
			}
			if got.BoundFrom != tc.in.BoundFrom {
				t.Errorf("bound-from provenance: got %q, want %q", got.BoundFrom, tc.in.BoundFrom)
			}
		})
		reached[tc.want] = true
	}

	// AC5's "every outcome is reached by a table-driven test", asserted rather
	// than assumed: a row deleted from the table above must go red here.
	for _, value := range trailRunOutcomeValues() {
		if !reached[value] {
			t.Errorf("no table row expects %q, so that outcome is unproven", value)
		}
	}
}

// TestTrailRunAnswersNameWhatWasObserved pins the two content rules a published
// verdict rests on. Both are about what a Detail must NOT let a reader conclude.
func TestTrailRunAnswersNameWhatWasObserved(t *testing.T) {
	t.Run("a clean negative says the scan matched no row, never that the command exited", func(t *testing.T) {
		got := trailClassifyRun(trailRunWellFormed())
		if got.Value != trailOutcomeNoRowMatched {
			t.Fatalf("value: got %q (%s), want %q", got.Value, got.Detail, trailOutcomeNoRowMatched)
		}
		if !strings.Contains(got.Detail, "NOT A STATEMENT THAT THE COMMAND HAD EXITED") {
			t.Errorf("detail: got %q, want it to say in those words that this is not an "+
				"it-exited verdict — the reap is expected to have already run by observation "+
				"time, so a clean negative here is the PREDICTED reading on a healthy run and "+
				"on a leaking one alike", got.Detail)
		}
	})

	t.Run("an unattributed match says nothing was attributed, never that the group exited", func(t *testing.T) {
		in := trailRunWellFormed()
		in.MatchCount = 3
		in.RowsScanned = 40
		got := trailClassifyRun(in)
		if got.Value != trailOutcomeMatchedUnattributed {
			t.Fatalf("value: got %q (%s), want %q", got.Value, got.Detail,
				trailOutcomeMatchedUnattributed)
		}
		if !strings.Contains(got.Detail, "NEVER EVIDENCE THAT THE GROUP HAD EXITED") {
			t.Errorf("detail: got %q, want it to say an inadmissible attribution is never "+
				"evidence the group had exited — a miss reported as a negative inverts the "+
				"asymmetry the whole claim rests on", got.Detail)
		}
	})
}

// TestTrailRunCorroborationNeverFlips is AC3's second half, executable: the
// point-in-time liveness reads and the claude-still-alive reading are recorded as
// corroboration and MAY DISAGREE WITH THE ATTRIBUTION WITHOUT CHANGING THE
// OUTCOME.
//
// The sweep excludes pinStateInstrumentFailed from the Liveness verdicts
// deliberately, and the exclusion is the point rather than a gap: an instrument
// failure is the ABSENCE of a reading, not a disagreeing one, so it is a named
// void (trailOutcomeVoidLivenessInstrument) and TestTrailClassifyRun owns it.
// ClaudeState carries no such exclusion — it certifies nothing on ANY value it
// can take, so its own instrument failure removes nothing from the answer.
func TestTrailRunCorroborationNeverFlips(t *testing.T) {
	decisive := []struct {
		name string
		in   trailRunReadings
		want string
	}{
		{"a proven finding", trailRunProofReadings(), trailOutcomeRunningAtTrailer},
		{"a clean negative", trailRunWellFormed(), trailOutcomeNoRowMatched},
	}
	claudeStates := []string{pinStateRunning, pinStateExitedNotReaped, pinStateNoSuchProcess,
		pinStateInstrumentFailed, ""}
	bounds := []string{trailBoundFromMiss, trailBoundFromStart, trailBoundNone}
	verdicts := []string{pinStateRunning, pinStateExitedNotReaped, pinStateNoSuchProcess}

	for _, base := range decisive {
		t.Run(base.name, func(t *testing.T) {
			for _, claude := range claudeStates {
				for _, bound := range bounds {
					for _, verdict := range verdicts {
						in := base.in
						in.ClaudeState = claude
						in.BoundFrom = bound
						in.Liveness = []pinStateOutcome{{Verdict: verdict, PID: 4242}}

						got := trailClassifyRun(in)
						if got.Value != base.want {
							t.Errorf("claude=%q bound=%s liveness=%s: got %q (%s), want %q — "+
								"corroboration may disagree with the attribution and must not "+
								"change the outcome",
								claude, bound, verdict, got.Value, got.Detail, base.want)
						}
						if want := bound == trailBoundFromMiss; got.Bounded != want {
							t.Errorf("claude=%q bound=%s: bounded is %t, want %t — %s carries a "+
								"real duration that bounds NOTHING and %s is the honest no-bound",
								claude, bound, got.Bounded, want, trailBoundFromStart,
								trailBoundNone)
						}
					}
				}
			}
		})
	}
}

// TestTrailRunComposesWithGateCases drives the composition over #1270's whole
// shipped gate value space rather than over a second, hand-kept list that could
// drift out of agreement with it — the same reason TestTrailGateThenAdmit sweeps
// trailGateCases().
//
// A CORRECT CONSUMER is modelled exactly: it calls the admissibility predicate
// when and only when the gate certified a reason, and leaves Admit zero
// otherwise. The property is that no gate fixture then trips C3 or C4, so every
// row lands on the outcome its gate value names.
func TestTrailRunComposesWithGateCases(t *testing.T) {
	// A record that is proof on its own, so any non-proof outcome below is
	// attributable to the gate and to nothing else.
	admissible := tdnReapOutcome{Verdict: tdnReapHeldPGIDKilled, HeldPGID: 7788,
		PGIDs: []int{7788}, Count: 1, LineCount: 1}

	want := map[string]string{
		trailGateUsable:        trailOutcomeRunningAtTrailer,
		trailGateBudgetFired:   trailOutcomeVoidBudgetFired,
		trailGateNoTrailer:     trailOutcomeVoidNoTrailer,
		trailGateScanAborted:   trailOutcomeVoidTrailerScanAborted,
		trailGateOutOfContract: trailOutcomeOutOfContract,
	}
	reached := make(map[string]bool)
	predicateCalls := 0

	for _, tc := range trailGateCases() {
		t.Run(tc.name, func(t *testing.T) {
			gate := trailGate(tc.in)

			in := trailRunWellFormed()
			in.Gate = gate
			in.Admit = trailAdmitResult{}
			in.MatchCount = 1
			if gate.Reason != "" {
				predicateCalls++
				in.Admit = trailAdmitAttribution(admissible, gate.Reason)
			}

			got := trailClassifyRun(in)
			if got.Value != want[gate.Value] {
				t.Fatalf("gate %s composed to %q (%s), want %q", gate.Value, got.Value, got.Detail,
					want[gate.Value])
			}
			if gate.Value == trailGateOutOfContract &&
				!strings.Contains(got.Detail, "the trailer gate itself reported") {
				// Both the gate arm and contract checks C3/C4 produce
				// trailOutcomeOutOfContract, so without this the row cannot tell a
				// gate that said "this is not a reading" from a composition the
				// classifier rejected.
				t.Errorf("detail: got %q, want it to attribute the out-of-contract answer to the "+
					"gate rather than to a composition this classifier rejected", got.Detail)
			}
		})
		reached[trailGate(tc.in).Value] = true
	}

	if predicateCalls != 2 {
		t.Errorf("the admissibility predicate was invoked %d time(s) across the sweep, want 2 — "+
			"exactly %s and %s certify a reason", predicateCalls, trailGateUsable,
			trailGateBudgetFired)
	}
	for value := range want {
		if !reached[value] {
			t.Errorf("no gate fixture produced %s, so its composition arm is unproven", value)
		}
	}
}

// TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone drives #1417 end to
// end: a real trailScan over a line with no terminal_reason key, a real
// tdnRunnerFromArgv reading that reduces to streamrunner, the real gate, and the
// real classifier — reaching trailGateAbsentOwesNone and then
// trailOutcomeVoidPathOwesNoReason.
//
// # Why it is not a row of trailGateCases()
//
// Every row of that slice carries one runner path and is swept under all five
// readings by TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt, which
// checks each row's declared want at reading 0 — ptyrunner. An absence-shaped row
// wanting the new value would fail that premise there, where it correctly reaches
// trailGateOutOfContract. trailGateCase.want is ONE value and the new value is
// reachable at ONE of the five readings, so the two cannot both hold. Hence this
// driver, and hence TestTrailRunComposesWithGateCases' want map gains no entry —
// its coverage loop goes red on an entry no fixture produces.
//
// # The vacuity guard is a CONTROLLED EXPERIMENT, not a doc claim
//
// "The arm fired" is worth nothing unless something else would have happened
// otherwise, so the sub-test below establishes first that the two tails it uses
// genuinely DISAGREE — under a usable gate, MatchCount 1 reaches
// trailOutcomeMatchedUnattributed and MatchCount 0 reaches
// trailOutcomeNoRowMatched — and only then swaps in the new gate answer and
// requires both to reach the new outcome. Invariance across tails that provably
// differ is what shows step 1 decided.
//
// Demonstrated under `go test -overlay` rather than asserted: with the
// trailGateAbsentOwesNone arm deleted from step 1's switch, this test goes red at
// its first assertion reading run-matched-not-attributed — a SCAN-SIDE ANSWER
// ABOUT PYRY awarded from a record the gate says certifies nothing, which is
// exactly the hazard the note at that switch describes, and one step past the
// "falls through to step 2" shorthand the shipped note used to carry (C4 forces
// Admit empty, so step 2 cannot fire). The tail experiment below is what shows
// the arm is what suppresses it: those same two tails reach two DIFFERENT
// scan-side outcomes whenever step 1 does not answer first.
//
// # It must not hand the classifier a proof
//
// C4 and C5 reject a non-certifying gate value arriving with any admissibility
// value, upstream of step 1. A test pairing trailAdmitProof with the new value
// would answer at the contract block and say nothing about the arm, which is why
// trailRunAbsentOwesNoneReadings() leaves Admit zero — the shape a correct
// consumer produces.
func TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone(t *testing.T) {
	gate := trailGate(trailGateInput{Scan: trailGateAbsentReasonScan(),
		RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv)})
	if gate.Value != trailGateAbsentOwesNone {
		t.Fatalf("gate: got %q (%s), want %q — the composition below is about the arm this "+
			"reading reaches, and a different arm makes every assertion after it a statement "+
			"about something else", gate.Value, gate.Detail, trailGateAbsentOwesNone)
	}
	if gate.Reason != "" {
		t.Errorf("gate certified %q, want nothing: an absent terminal_reason certifies no reason "+
			"on any path, and a reason here would trip C2 rather than reach step 1", gate.Reason)
	}

	got := trailClassifyRun(trailRunAbsentOwesNoneReadings())
	if got.Value != trailOutcomeVoidPathOwesNoReason {
		t.Fatalf("composed to %q (%s), want %q", got.Value, got.Detail,
			trailOutcomeVoidPathOwesNoReason)
	}
	if got.Gate != trailGateAbsentOwesNone {
		t.Errorf("gate provenance: got %q, want %q — a reader of the published record cannot "+
			"interpret this void without the gate answer that produced it", got.Gate,
			trailGateAbsentOwesNone)
	}
	if got.Admit != "" {
		t.Errorf("admit provenance: got %q, want empty — the gate certified nothing, so the "+
			"attribution predicate was never owed a call", got.Admit)
	}
	if !strings.Contains(got.Detail, trailGateAbsentOwesNone) {
		t.Errorf("detail: got %q, want it to name the gate answer that decided", got.Detail)
	}
	if strings.Contains(got.Detail, reachTruncationMarker) {
		t.Errorf("detail is truncated at %d bytes, so its closing argument was cut: %s",
			reachMaxCommandBytes, got.Detail)
	}

	t.Run("the step-1 arm decided, across two tails that provably disagree", func(t *testing.T) {
		// The control: the same two tails under a USABLE gate, which reach two
		// different outcomes. Without this the invariance below would be
		// consistent with the tails never having mattered at all.
		control := map[int]string{
			1: trailOutcomeMatchedUnattributed,
			0: trailOutcomeNoRowMatched,
		}
		for tail, want := range control {
			in := trailRunWellFormed()
			in.MatchCount = tail
			if got := trailClassifyRun(in); got.Value != want {
				t.Fatalf("control, MatchCount %d: got %q (%s), want %q — the experiment below "+
					"rests on these two tails reaching DIFFERENT outcomes", tail, got.Value,
					got.Detail, want)
			}
		}

		// The experiment: the same two tails with only the gate answer swapped,
		// and Admit zeroed because that is what C4 requires of a correct consumer
		// beside a value certifying nothing.
		for tail := range control {
			in := trailRunAbsentOwesNoneReadings()
			in.MatchCount = tail
			got := trailClassifyRun(in)
			if got.Value != trailOutcomeVoidPathOwesNoReason {
				t.Errorf("MatchCount %d: got %q (%s), want %q — step 1 answers before any "+
					"scan-side step, so the tail must not reach the decision", tail, got.Value,
					got.Detail, trailOutcomeVoidPathOwesNoReason)
			}
			if got.MatchCount != tail {
				t.Errorf("MatchCount %d: the record reports %d — the tail is provenance the "+
					"reader still needs, and only the DECISION is invariant", tail,
					got.MatchCount)
			}
		}
	})
}

// TestTrailRunComposesUnderANamedReasonOnAPathThatOwesNone drives #1434 end to
// end, in its sibling above's shape: a real trailScan over a line that DOES carry a
// terminal_reason, a real tdnRunnerFromArgv reading that reduces to streamrunner,
// the real gate, and the real classifier — reaching trailGatePresentOwesNone and
// then trailOutcomeVoidReasonNotOwedByPath.
//
// # Why it is not a row of trailGateCases()
//
// The sibling's reason, unchanged: every row of that slice carries one runner path
// and is swept under all five readings by
// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt, which checks each row's
// declared want at reading 0 — ptyrunner. A row wanting the new value would fail
// that premise there, where this same fixture correctly reaches trailGateUsable.
// trailGateCase.want is ONE value and the new value is reachable at ONE of the five
// readings, so the two cannot both hold. Hence this driver, and hence
// TestTrailRunComposesWithGateCases' want map gains no entry — its coverage loop
// goes red on an entry no fixture produces.
//
// # The vacuity guard is a CONTROLLED EXPERIMENT, not a doc claim
//
// "The arm fired" is worth nothing unless something else would have happened
// otherwise, so the sub-test below establishes first that the two tails it uses
// genuinely DISAGREE — under a usable gate, MatchCount 1 reaches
// trailOutcomeMatchedUnattributed and MatchCount 0 reaches trailOutcomeNoRowMatched
// — and only then swaps in the new gate answer and requires both to reach the new
// outcome. Invariance across tails that provably differ is what shows step 1
// decided.
//
// Demonstrated under `go test -overlay` rather than asserted: with the
// trailGatePresentOwesNone arm deleted from step 1's switch, this test goes red at
// its first assertion reading run-matched-not-attributed — a SCAN-SIDE ANSWER ABOUT
// PYRY awarded from a record the gate says certifies nothing. That is one step past
// the "falls through to step 2" shorthand, since C4 forces Admit empty and step 2
// cannot fire; the tail experiment is what shows the arm is what suppresses it,
// because those same two tails reach two DIFFERENT scan-side outcomes whenever step
// 1 does not answer first.
//
// # Two shapes are refused
//
// It must NOT hand the classifier a proof. C4 (:456-464) and C5 (:466-476) reject a
// non-certifying gate value arriving with any admissibility value, upstream of step
// 1, so a test pairing trailAdmitProof with the new value would answer at the
// contract block and say nothing about the arm — which is why
// trailRunPresentOwesNoneReadings() leaves Admit zero.
//
// And it asserts no "the value cannot reach step 2". That is unreachable for EVERY
// non-certifying value, by C4 alone, so such an assertion would pass against a
// build with no arm in step 1 at all — the one build this test exists to fail.
//
// # The containment guard earns its line
//
// The Detail assertion below is a strings.Contains against trailGatePresentOwesNone
// while the same Detail deliberately names two other values. A rename that put the
// gate value into a substring relationship with either would make that assertion
// report the wrong name as present, exactly as the marker guard in
// TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone protects against — so it is
// checked here in both directions rather than left to the reader of the constants.
// TestTrailAdmissibilityConstantsAreClosed compares for EQUALITY and cannot see it.
func TestTrailRunComposesUnderANamedReasonOnAPathThatOwesNone(t *testing.T) {
	for _, other := range []string{trailOutcomeVoidPathOwesNoReason, trailOutcomeOutOfContract,
		trailOutcomeVoidReasonNotOwedByPath} {
		if strings.Contains(other, trailGatePresentOwesNone) ||
			strings.Contains(trailGatePresentOwesNone, other) {
			t.Fatalf("the gate value %q and %q contain one another: the Detail assertion below "+
				"names all of them, so it would report the wrong value as present",
				trailGatePresentOwesNone, other)
		}
	}

	gate := trailGate(trailGateInput{Scan: trailGateUsableScan(),
		RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv)})
	if gate.Value != trailGatePresentOwesNone {
		t.Fatalf("gate: got %q (%s), want %q — the composition below is about the arm this "+
			"reading reaches, and a different arm makes every assertion after it a statement "+
			"about something else", gate.Value, gate.Detail, trailGatePresentOwesNone)
	}
	if gate.Reason != "" {
		t.Errorf("gate certified %q, want nothing: a terminal_reason from a path that owes none "+
			"is a reading of the line and never a certification of it, and a reason here would "+
			"trip C2 rather than reach step 1", gate.Reason)
	}

	got := trailClassifyRun(trailRunPresentOwesNoneReadings())
	if got.Value != trailOutcomeVoidReasonNotOwedByPath {
		t.Fatalf("composed to %q (%s), want %q", got.Value, got.Detail,
			trailOutcomeVoidReasonNotOwedByPath)
	}
	if got.Gate != trailGatePresentOwesNone {
		t.Errorf("gate provenance: got %q, want %q — a reader of the published record cannot "+
			"interpret this void without the gate answer that produced it", got.Gate,
			trailGatePresentOwesNone)
	}
	if got.Admit != "" {
		t.Errorf("admit provenance: got %q, want empty — the gate certified nothing, so the "+
			"attribution predicate was never owed a call", got.Admit)
	}
	if !strings.Contains(got.Detail, trailGatePresentOwesNone) {
		t.Errorf("detail: got %q, want it to name the gate answer that decided", got.Detail)
	}
	if strings.Contains(got.Detail, reachTruncationMarker) {
		t.Errorf("detail is truncated at %d bytes, so its closing argument was cut: %s",
			reachMaxCommandBytes, got.Detail)
	}

	t.Run("the step-1 arm decided, across two tails that provably disagree", func(t *testing.T) {
		// The control: the same two tails under a USABLE gate, which reach two
		// different outcomes. Without this the invariance below would be
		// consistent with the tails never having mattered at all.
		control := map[int]string{
			1: trailOutcomeMatchedUnattributed,
			0: trailOutcomeNoRowMatched,
		}
		for tail, want := range control {
			in := trailRunWellFormed()
			in.MatchCount = tail
			if got := trailClassifyRun(in); got.Value != want {
				t.Fatalf("control, MatchCount %d: got %q (%s), want %q — the experiment below "+
					"rests on these two tails reaching DIFFERENT outcomes", tail, got.Value,
					got.Detail, want)
			}
		}

		// The experiment: the same two tails with only the gate answer swapped,
		// and Admit zeroed because that is what C4 requires of a correct consumer
		// beside a value certifying nothing.
		for tail := range control {
			in := trailRunPresentOwesNoneReadings()
			in.MatchCount = tail
			got := trailClassifyRun(in)
			if got.Value != trailOutcomeVoidReasonNotOwedByPath {
				t.Errorf("MatchCount %d: got %q (%s), want %q — step 1 answers before any "+
					"scan-side step, so the tail must not reach the decision", tail, got.Value,
					got.Detail, trailOutcomeVoidReasonNotOwedByPath)
			}
			if got.MatchCount != tail {
				t.Errorf("MatchCount %d: the record reports %d — the tail is provenance the "+
					"reader still needs, and only the DECISION is invariant", tail,
					got.MatchCount)
			}
		}
	})

	// The sibling reading, driven here rather than only in trailRunCases(), because
	// the pair is what the new value is FOR: the same path, the same classifier, and
	// the only difference is whether the key is on the line. A build that collapsed
	// the two would pass every assertion above.
	t.Run("the absent reading on the same path stays a different outcome", func(t *testing.T) {
		absent := trailClassifyRun(trailRunAbsentOwesNoneReadings())
		if absent.Value != trailOutcomeVoidPathOwesNoReason {
			t.Fatalf("the absent-key sibling composed to %q (%s), want %q", absent.Value,
				absent.Detail, trailOutcomeVoidPathOwesNoReason)
		}
		if absent.Value == got.Value {
			t.Errorf("both readings compose to %q: one is that path's documented healthy shape "+
				"and the other is not, and a published record that cannot separate them reports "+
				"the two under one name", got.Value)
		}
	})
}

// TestTrailRunOutcomeCarriesNoCapturedBytes makes AC2's
// operator-review-before-paste obligation checkable rather than advisory, in
// TestTrailAdmissibilityRecordsCarryNoCapturedBytes's shape
// (trailer_admissibility_test.go:2523) and reusing the shipped trailNeedle.
//
// The needle goes into EVERY string-bearing input the classifier can see —
// Gate.Detail, Admit.Detail, and a pinStateOutcome's Detail and ToolStderr —
// because the rule being enforced is not "do not copy the one field the ticket
// named" but "no Detail here quotes any input's captured string".
func TestTrailRunOutcomeCarriesNoCapturedBytes(t *testing.T) {
	in := trailRunProofReadings()
	in.Gate = trailGateResult{Value: trailGateUsable, Reason: "completed",
		Detail: "a gate detail that also carries " + trailNeedle}
	in.Admit = trailAdmitResult{Value: trailAdmitProof,
		Detail: "an admissibility detail that also carries " + trailNeedle}
	in.Liveness = []pinStateOutcome{{
		Verdict:    pinStateNoSuchProcess,
		PID:        4242,
		Detail:     "a per-pid detail that also carries " + trailNeedle,
		ToolStderr: "ps wrote " + trailNeedle,
	}}

	got := trailClassifyRun(in)
	// The premise first, so the test cannot pass by classifying garbage.
	if got.Value != trailOutcomeRunningAtTrailer {
		t.Fatalf("value: got %q (%s), want %q — the premise is a well-formed record whose "+
			"captured strings carry the needle", got.Value, got.Detail,
			trailOutcomeRunningAtTrailer)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the run outcome: %v", err)
	}
	if bytes.Contains(encoded, []byte(trailNeedle)) {
		t.Errorf("the marshalled run outcome carries captured bytes: %s", encoded)
	}

	// The structural half: the record has no field for a command string today,
	// and this is the check that a future field does not quietly add one.
	// pinStateColumns refuses a `command` column at the source for the same
	// reason — those columns route the operator's CLAUDE_CODE_OAUTH_TOKEN /
	// ANTHROPIC_API_KEY into an artifact destined for a public issue.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keyed); err != nil {
		t.Fatalf("decoding the marshalled run outcome: %v", err)
	}
	for _, forbidden := range []string{"command", "args", "comm", "argv"} {
		for key := range keyed {
			if strings.Contains(key, forbidden) {
				t.Errorf("the run outcome carries key %q, which is %q-shaped: this record's whole "+
					"value is that it can be published unreviewed, and a command column would "+
					"inherit the operator-review-before-paste obligation onto it", key, forbidden)
			}
		}
	}
}

// trailRunOutcomeValues is the closed set as data, so a coverage loop can assert
// over it. It is derived from trailIsRunOutcome's own space by construction: a
// value listed here that the predicate rejects, or vice versa, goes red in
// TestTrailRunOutcomeValuesAgreeWithThePredicate below.
func trailRunOutcomeValues() []string {
	return []string{
		trailOutcomeRunningAtTrailer,
		trailOutcomeMatchedUnattributed,
		trailOutcomeNoRowMatched,
		trailOutcomeVoidBudgetFired,
		trailOutcomeVoidNoTrailer,
		trailOutcomeVoidTrailerScanAborted,
		trailOutcomeVoidPyryDidNotExit,
		trailOutcomeVoidArgvScanErrored,
		trailOutcomeVoidNoRowsParsed,
		trailOutcomeVoidLivenessInstrument,
		trailOutcomeVoidPathOwesNoReason,
		trailOutcomeVoidReasonNotOwedByPath,
		trailOutcomeOutOfContract,
	}
}

// TestTrailRunOutcomeValuesAgreeWithThePredicate keeps the list above from
// drifting away from the predicate the published record is read through. Without
// it, an outcome added to trailIsRunOutcome but not to the list would make
// TestTrailClassifyRun's coverage loop silently stop covering it.
func TestTrailRunOutcomeValuesAgreeWithThePredicate(t *testing.T) {
	values := trailRunOutcomeValues()
	if len(values) != 13 {
		t.Errorf("the closed set holds %d value(s), want 13 — the count is the ticket's own "+
			"enumeration and a change to it is a change to what the run can conclude. Thirteen "+
			"rather than twelve since #1434 added %s beside #1417's %s, because a gate value "+
			"with no arm in step 1 falls through to the scan-side steps and answers about pyry "+
			"from a record that certifies nothing", len(values),
			trailOutcomeVoidReasonNotOwedByPath, trailOutcomeVoidPathOwesNoReason)
	}
	for _, v := range values {
		if !trailIsRunOutcome(v) {
			t.Errorf("%q is listed as an outcome but trailIsRunOutcome rejects it", v)
		}
	}
	// The other direction, over the values the predicate is most likely to gain
	// by a copy-paste from the two adjacent spaces.
	for _, v := range []string{"", trailGateUsable, trailGateOutOfContract, trailAdmitProof,
		trailAdmitOutOfContract, trailSeen, trailAbsent, trailAborted} {
		if trailIsRunOutcome(v) {
			t.Errorf("trailIsRunOutcome accepts %q, which belongs to another space", v)
		}
	}
}
