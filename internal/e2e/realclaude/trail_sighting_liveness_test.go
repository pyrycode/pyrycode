//go:build e2e_realclaude

package realclaude

// Whether a command was ALIVE WHEN ITS RUN'S TRAILER WAS SIGHTED, decided from a
// certified ordering plus a pid re-read after pyry exited — so that the headless
// stream path, which writes no reap log at all on a clean exit, has an evidence
// route rather than none.
//
// Everything here runs offline: no live claude, no credentials, no daemon, no
// env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestTrailSighting' -v ./internal/e2e/realclaude/
//
// # Why an ordering argument at all: the top-ranked evidence is EMPTY here
//
// trailClassifyRun ranks an admissible reap-log attribution second, above every
// point-in-time reading (trail_run_outcome_test.go:606-607), and argues at the
// point of use (:999-1003) that those readings "are expected to be late — the reap
// completes in the time of one ps exec while the observation of the trailer trails
// the write by up to a poll interval — so resting a verdict on them manufactures a
// systematic false negative". :1344-1346 makes that executable.
//
// That route is structurally unavailable on PYRY_USE_STREAMJSON=1. The two runners
// reap in different places:
//
//   - ptyrunner reaps in a defer that fires on EVERY teardown reaching it —
//     "watchdog-fire, normal end-of-turn, and any post-Spawn early return"
//     (internal/agentrun/ptyrunner/runner.go:387-398). Three reap call sites.
//   - streamrunner reaps inside cmd.Cancel only — its single reap call site
//     (internal/agentrun/streamrunner/runner.go:207-208) — and its own comment
//     states: "It never fires on a clean exit: cancelChild there runs only after
//     cmd.Wait returns, by which point os/exec has stopped its ctx watcher"
//     (:203-205).
//
// So on a healthy stream-path run pyry writes NO REAP LOG AT ALL. The attribution
// leg is not late here — it is empty, and lateness is the wrong objection to raise
// against a route that produces nothing to be late.
//
// # The argument this file makes instead
//
// Given #1439's certified ordering — the trailer was sighted, THEN pyry exited,
// THEN the pinned pid was re-read, all three under a hold held for the whole wait
// — a process can only die once, so a pid found ALIVE after pyry's exit was
// necessarily alive at the earlier sighting. Lateness, the standing objection to
// every point-in-time reading in this family, does not apply: it only STRENGTHENS
// this direction, because a later "alive" implies an earlier "alive".
//
// The argument keys on a pid pinned while the command was still reachable and
// re-checks THAT PID ALONE. It never asks whether the command is still a
// descendant of anything — the reading that goes blind once claude exits and the
// group re-parents to init (process_pin_liveness_test.go:268-271).
//
// # What this predicate claims, and what it must never claim
//
// It establishes ALIVE WHEN THE TRAILER LINE WAS SIGHTED ON PYRY'S STDOUT. It does
// NOT establish "alive when pyry declared the turn finished": on this path no
// terminal reason is certified, so no such instant exists at all, and the shipped
// classifier says exactly that at each of its void arms
// (trail_run_outcome_test.go:834, :969-974, :979-988).
//
// Every value here is named after the SIGHTING, every Detail ends with
// trailSightingInstantClause, and TestTrailSightingVoidsNeverReadAsNegative
// asserts no Detail claims the other instant. That is the prose half of the naming
// rule made executable rather than left to habit.
//
// # The naming collision is this ticket's central risk
//
// trailOutcomeRunningAtTrailer = "run-running-at-trailer"
// (trail_run_outcome_test.go:114-118) already ships, and its doc reads: "an
// admissible attribution proves the process group was alive when the trailer was
// written. THE FINDING, and the only path to one." That is the SAME ENGLISH
// SENTENCE this predicate establishes, from the REAP-LOG evidence class this file
// exists because the stream path lacks. A value here named for
// aliveness-at-the-trailer is one word from it, and a consumer that confused the
// two would read reap-log-backed and ordering-backed evidence as one finding.
//
// So the positive value is sighting-alive-by-ordering: a different prefix, a
// different noun, and a -by-ordering suffix that names the evidence class the
// collision is about. The second, subtler pair is handled the same way:
// sighting-reason-pid-read-failed rather than …-liveness-instrument-failed,
// because the shipped trailOutcomeVoidLivenessInstrument
// (trail_run_outcome_test.go:180-184) is "run-void-liveness-instrument-failed" and
// a reason ending in the same four words would be distinct to a map and confusable
// to a reader. Naming it after THE PID READ keeps the run space's phrasing out of
// this one.
//
// Prose cannot enforce either. A per-space membership predicate is scoped to one
// space per call and CANNOT SEE A PAIR, so all eight values below join the union
// map in TestTrailAdmissibilityConstantsAreClosed
// (`TestTrailAdmissibilityConstantsAreClosed`) — the sixth space to do so, and #1439's
// precedent for the same reason one layer down.
//
// # THERE IS DELIBERATELY NO sighting-out-of-contract VALUE
//
// Same decision #1439 made, and for a checkable reason rather than by imitation:
// every off-space input already has a reachable, correct home. An unreadable
// verdict and a failed read are the same statement — NOTHING WAS MEASURED — so the
// switch's default carries both, and two fixtures reach it by different routes. An
// unreachable named value would make this space's count a lie about what the
// predicate can answer.
//
// # Scope, and one observation recorded rather than chased
//
// This file ships the value spaces, the record, the predicate, its fixtures, its
// tests and its own no-captured-bytes sweep, plus the join to the shared union
// map. It changed trailClassifyRun not at all, grew no run-level outcome, and did
// not touch the step-1 gate switch: that switch ANSWERED trailGateAbsentOwesNone
// with an unconditional return, so a route wired below it would not have fired on
// the one gate value it exists to serve. #1446 reached it — by consulting this
// predicate INSIDE that arm (trail_run_outcome_test.go:842-974), which is the only
// placement that does not award a scan-side answer to a record the gate says
// certifies nothing, and by publishing the finding under an evidence route of its
// own. Nothing here changed for it: the predicate still stands alone, is still
// pure over its input, and is still driven from fixtures.
//
// THE OBSERVATION: the stream path leaves a backgrounded group unreaped where
// ptyrunner kills it. That is worth recording and is NOT chased here — what
// survives teardown is #1231's question.
//
// # Reused, not rebuilt
//
// pinReadState supplies the per-pid verdicts,
// trailCertifyOrdering the ordering,
// trailDetail the capped formatting, and
// trailNeedle the sweep's needle. No
// second descendant walk, no second FIFO-hold helper, no second liveness reader,
// no second trailer parser.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// --- the sighting's value space -------------------------------------------------

// What one certified-ordering-plus-pinned-pid reading can conclude, as a POSITIVE
// ALLOWLIST of three: established, unestablished, and one void.
//
// Every value carries a `sighting-` prefix, and the prefix is load-bearing rather
// than cosmetic — see the file header's collision argument. The instant is in the
// prefix on purpose: the ONLY instant any value here is about is the trailer's
// sighting on pyry's stdout, never the instant a turn was declared finished, which
// this path never certifies.
const (
	// trailSightingEstablished: the ordering was certified and the pinned pid was
	// still running when re-read after pyry's exit, so it was necessarily alive at
	// the earlier sighting. The finding, and the only path to one HERE.
	//
	// Deliberately NOT anything containing "running-at-trailer": the shipped
	// trailOutcomeRunningAtTrailer (trail_run_outcome_test.go:118) states the same
	// English sentence from the REAP-LOG evidence class, and the whole reason this
	// file exists is that the stream path has no reap log. The -by-ordering suffix
	// names the evidence class, which is the axis the two differ on.
	trailSightingEstablished = "sighting-alive-by-ordering"
	// trailSightingUnestablished: the ordering was certified and the pinned pid was
	// not alive at the later read. A statement about THIS EVIDENCE ROUTE, not about
	// the command having been dead at the sighting — the read is late by
	// construction, so it cannot rule the earlier instant out. There is deliberately
	// no "exited before the sighting" value in this space for it to decay into.
	trailSightingUnestablished = "sighting-not-established"
	// trailSightingVoid: nothing was measured. Reached by a failed ordering premise
	// and by a pid read that did not answer, and NEVER a negative — see the two
	// invariants on trailEstablishSighting.
	trailSightingVoid = "sighting-void"
)

// The reason space: one per per-pid verdict, plus the ordering pass-through.
//
// A reason is a FIELD on the record rather than prose in the Detail, in
// trailGateResult's Value+Reason+Detail shape (trailer_admissibility_test.go:305),
// so a consumer tells an exited-but-not-yet-reaped from a no-such-process, and
// either from an instrument failure, WITHOUT PARSING PROSE. Two of the three land
// on the same value, and the value alone therefore cannot separate them.
const (
	// trailSightingReasonPidRunning: certified ordering, pinStateRunning. The one
	// reason that establishes.
	trailSightingReasonPidRunning = "sighting-reason-pid-running"
	// trailSightingReasonPidReapedPending: certified ordering,
	// pinStateExitedNotReaped. Kept apart from …PidGone because `ps` LISTS a
	// SIGKILLed-but-unreaped process as a row (measured in #1224), so the two are
	// different observations of the process table, not one.
	trailSightingReasonPidReapedPending = "sighting-reason-pid-reaped-pending"
	// trailSightingReasonPidGone: certified ordering, pinStateNoSuchProcess.
	trailSightingReasonPidGone = "sighting-reason-pid-gone"
	// trailSightingReasonPidReadFailed: certified ordering, and the read did not
	// answer — pinStateInstrumentFailed, or a verdict outside pinIsVerdict's space
	// including the zero "" of an unfilled pinStateOutcome. Both say only that
	// nothing was measured.
	//
	// Deliberately NOT …-liveness-instrument-failed: the shipped
	// trailOutcomeVoidLivenessInstrument is "run-void-liveness-instrument-failed"
	// (trail_run_outcome_test.go:184), and a reason ending in those four words would
	// be distinct to the union map and confusable to a reader. This one is named
	// after THE PID READ.
	trailSightingReasonPidReadFailed = "sighting-reason-pid-read-failed"
	// trailSightingReasonOrderingUncertified: the ordering result was any of
	// #1439's three trailOrderVoid* values. Passed through, never re-decided, and
	// reached REGARDLESS of the pinned pid's verdict.
	trailSightingReasonOrderingUncertified = "sighting-reason-ordering-uncertified"
)

// trailSightingInstantClause is the fixed tail every Detail below ends with. It
// names the instant the verdict is about, which is the one thing every arm here
// must say and the one thing the collision with the run space turns on.
//
// A shared constant rather than five hand-written copies so the arms cannot DRIFT
// in how they name the instant. Sharing it is not on its own what makes "every
// Detail names the sighting" true — an arm can still omit the tail from its own
// format string. What makes the rule checkable is
// TestTrailSightingAllSixteenCombinations, which requires every Detail to contain
// this clause, so an arm that drops it reddens.
//
// It costs 76 bytes of the 512-byte cap and leaks nothing: it interpolates no
// input at all.
const trailSightingInstantClause = "the instant this verdict is about is the trailer's SIGHTING on pyry's stdout"

// --- the record ------------------------------------------------------------------

// trailSightingResult is what the predicate produces, in trailGateResult's
// Value+Reason+Detail shape rather than trailAdmitResult's two-field one: the
// named reason is a FIELD here because two verdicts share one value and a consumer
// must tell them apart without parsing the Detail.
//
// TRAP-FREE BY CONSTRUCTION: three strings, no pointer, no embedded type, and
// NO PID FIELD — the pid reaches a reader through the Detail as %d, which keeps
// the marshalled surface at three keys and the sweep's forbidden-key walk over the
// smallest possible set.
//
// Unlike trailOrderResult, this record's producer CAN see captured bytes: a
// pinStateOutcome carries subprocess stderr in ToolStderr
// (`pinClassifyState`) and folds it into Detail (:348-349). That is
// why TestTrailSightingResultCarriesNoCapturedBytes ships both halves — the needle
// sweep AND the structural key walk — where #1439 could honestly ship only the
// second (trail_ordering_premises_test.go:577-583).
type trailSightingResult struct {
	Value  string `json:"value"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// --- the predicate -----------------------------------------------------------------

// trailEstablishSighting decides whether a command was alive when its run's
// trailer was sighted, from a certified ordering and a pid re-read after pyry
// exited.
//
// Pure over its input: no exec, no clock, no filesystem. That is what lets every
// arm be driven with no live turn and no credentials. It takes no *testing.T and
// never fails a test — an instrument failure observed mid-turn is a datum to
// publish, not a reason to abort the turn, the same contract as trailGate
// (trailer_admissibility_test.go:362-363), trailCertifyOrdering, trailScan,
// tdnClassifyReapLog, pinReadState and fifoLiveRead. It consults no parentage and
// requires no reap line.
//
// # Two whole records, and the parameter list IS the enforcement
//
// Both parameters are the SHIPPED RECORDS rather than their Value/Verdict strings,
// and the two reasons pull in opposite directions from #1439's:
//
//   - pinStateOutcome must arrive whole because its Detail, StateColumn and
//     ToolStderr are string-bearing and ToolStderr takes raw ps stderr verbatim
//     (`pinClassifyState`). Narrowing this parameter to a bare
//     verdict string would leave no route for a needle to travel, which would make
//     the sweep below UNBUILDABLE AS SPECIFIED — and a sweep that cannot fail
//     measures nothing. #1439's three bools were the enforcement precisely BECAUSE
//     no byte could reach them; here the opposite shape is the honest one, and the
//     sweep is the control.
//   - trailOrderResult must arrive whole because it is documented trap-free by
//     construction (trail_ordering_premises_test.go:154-157), which is the same
//     reason trailClassifyRun takes trailGateResult whole. It is CONSUMED, never
//     re-derived: this function reads ordering.Value and nothing else from it.
//
// finSighting (finding_run_gather_test.go:372-384) is the nearest existing
// reduction of a sighting and is the WRONG input here: it carries TerminalReason,
// StopReason, Subtype and KeyNames — exactly the trailer values this record keeps
// out. The family's forbidden-key walk would not catch that on its own, since those
// marshal as terminal_reason / stop_reason / subtype / trailer_keys, none of which
// is `command`-shaped. THE PARAMETER LIST IS WHAT FORECLOSES IT; widening a
// denylist could not.
//
// This function reads exactly ordering.Value, pin.Verdict and pin.PID. It reads no
// other field of either record, and § The Detail content rule forbids quoting one.
//
// # The assignment rule: 4 × 4, with no combination left to judgement
//
//	ordering \ verdict | Running       | ExitedNotReaped | NoSuchProcess | InstrumentFailed
//	trailOrderCertified| ESTABLISHED   | unestablished   | unestablished | VOID
//	trailOrderVoid*    | void          | void            | void          | void
//
// TWO INVARIANTS THE RULE ENCODES, and neither is a matter of taste:
//
//  1. A PREMISE FAILURE NEVER READS AS EVIDENCE THAT THE COMMAND HAD EXITED. Void,
//     never unestablished. Nothing was measured when a premise failed, so no
//     negative may be read from it.
//  2. AN INSTRUMENT FAILURE NEVER READS AS A CLEAN NEGATIVE. Void, never
//     unestablished — the same collapse trailOutcomeVoidLivenessInstrument's doc
//     (trail_run_outcome_test.go:180-184) refuses one layer up: folding it into a
//     soft negative "would manufacture a clean negative out of the instrument's
//     breakage".
//
// # One precedence decision, stated and then checked
//
// ON A NON-CERTIFIED ORDERING THE VOID IS REACHED REGARDLESS OF THE VERDICT, so
// sighting-reason-ordering-uncertified OUTRANKS sighting-reason-pid-read-failed on
// the (void ordering × instrument-failed) cell. A premise failure is not evidence
// about the command, and re-deciding a premise this predicate did not measure would
// be reading the ordering result as something other than whole. The ordering result
// is PASSED THROUGH — never re-derived from its Detail, never re-litigated.
//
// TestTrailSightingAllSixteenCombinations pins that cell along with the other
// fifteen, so the precedence is checked everywhere it is consulted rather than at
// one representative pair.
//
// # Body shape: one guard, then a switch whose default is REACHABLE
//
// Four return sites. The default arm is deliberate and is NOT a defensive branch no
// fixture reaches: it is the home of pinStateInstrumentFailed AND of any off-space
// verdict string, including the zero "" of an unfilled pinStateOutcome. Two
// fixtures reach it by different routes and the coverage table carries both.
// Folding them together is correct rather than lazy — an unreadable verdict and a
// failed read are the same statement, NOTHING WAS MEASURED, and both must fail safe
// to void.
//
// # The Detail content rule, pinned rather than left to judgement
//
// Each Detail MAY name the verdict that decided, this space's value and reason
// names, the pid, and — on the pass-through — which ordering value arrived. Those
// are values from this family's own closed spaces and an integer, not captured
// bytes.
//
// It MUST end with trailSightingInstantClause, so every arm names the sighting as
// the instant its verdict is about.
//
// It MUST NEVER claim the turn was declared finished: no terminal reason is
// certified on this path, so that instant does not exist, and a Detail claiming it
// would report ordering-backed evidence as the reap-log-backed finding.
//
// It MUST NEVER quote pin.Detail, pin.ToolStderr, pin.StateColumn or
// ordering.Detail. That last prohibition is what
// TestTrailSightingResultCarriesNoCapturedBytes exists to enforce, and the natural
// mistake — interpolating pin.Detail for a better failure message — is exactly what
// it catches.
//
// All five arms go through trailDetail, so all inherit the 512-byte cap
// (`reachEnableEnv`), which truncates SILENTLY — and the coverage
// test asserts the truncation marker is absent from every row, so a Detail whose
// argument would be cut off reddens here rather than reaching an operator's
// artifact.
func trailEstablishSighting(ordering trailOrderResult, pin pinStateOutcome) trailSightingResult {
	decide := func(value, reason, format string, args ...any) trailSightingResult {
		return trailSightingResult{Value: value, Reason: reason, Detail: trailDetail(format, args...)}
	}

	// The ordering first, and regardless of the verdict: a missing premise is not
	// evidence about any command.
	if ordering.Value != trailOrderCertified {
		return decide(trailSightingVoid, trailSightingReasonOrderingUncertified,
			"the ordering arrived as %s rather than %s, so an ordering premise is missing. It is "+
				"passed through WHOLE and never re-decided here, and this arm is reached whatever "+
				"the pinned pid read (%q): a premise failure is not evidence that the command had "+
				"exited. Nothing was measured, so never a negative. "+trailSightingInstantClause,
			ordering.Value, trailOrderCertified, pin.Verdict)
	}

	switch pin.Verdict {
	case pinStateRunning:
		return decide(trailSightingEstablished, trailSightingReasonPidRunning,
			"the pinned pid %d was still running when it was re-read AFTER pyry's exit, and the "+
				"ordering is %s, so it was necessarily alive at the earlier sighting: a process "+
				"can only die once, and a later reading only strengthens that direction. "+
				trailSightingInstantClause,
			pin.PID, trailOrderCertified)

	case pinStateExitedNotReaped:
		return decide(trailSightingUnestablished, trailSightingReasonPidReapedPending,
			"the ordering is %s and the pinned pid %d read as %s after pyry's exit: it had exited "+
				"and was awaiting a reap, so this route does not establish aliveness at the "+
				"earlier sighting. NOT a claim that it was already gone then — only that this "+
				"evidence does not carry the earlier instant. "+trailSightingInstantClause,
			trailOrderCertified, pin.PID, pinStateExitedNotReaped)

	case pinStateNoSuchProcess:
		return decide(trailSightingUnestablished, trailSightingReasonPidGone,
			"the ordering is %s and the pinned pid %d read as %s after pyry's exit, so this route "+
				"does not establish aliveness at the earlier sighting. The read is late BY "+
				"CONSTRUCTION, so it rules nothing out about the earlier instant either. "+
				trailSightingInstantClause,
			trailOrderCertified, pin.PID, pinStateNoSuchProcess)
	}

	// Reachable by two different routes: pinStateInstrumentFailed, and any verdict
	// outside pinIsVerdict's space. pinIsVerdict lets the Detail say which was seen
	// without the two needing separate reasons — they are the same statement.
	return decide(trailSightingVoid, trailSightingReasonPidReadFailed,
		"the ordering is %s, but the pinned pid %d's read answered nothing: verdict %q, a "+
			"recorded liveness verdict: %t. An instrument failure must never read as a clean "+
			"negative, and an off-space verdict is no reading either — both say only that "+
			"nothing was measured. "+trailSightingInstantClause,
		trailOrderCertified, pin.PID, pin.Verdict, pinIsVerdict(pin.Verdict))
}

// --- membership helpers ------------------------------------------------------------

// trailSightingValues is the closed outcome set as data, so a coverage loop can
// assert over it. It is derived from trailIsSightingValue's own space by
// construction: a value listed here that the predicate rejects, or vice versa, goes
// red in TestTrailSightingValuesAgreeWithThePredicate below.
func trailSightingValues() []string {
	return []string{
		trailSightingEstablished,
		trailSightingUnestablished,
		trailSightingVoid,
	}
}

// trailIsSightingValue reports whether v is one of the three recorded outcomes. It
// mirrors trailIsRunOutcome and trailIsOrderValue
// and exists for the same reason: a value a reader of the published record cannot
// look up is a verdict they cannot interpret.
func trailIsSightingValue(v string) bool {
	switch v {
	case trailSightingEstablished, trailSightingUnestablished, trailSightingVoid:
		return true
	}
	return false
}

// trailSightingReasons is the closed reason set as data.
func trailSightingReasons() []string {
	return []string{
		trailSightingReasonPidRunning,
		trailSightingReasonPidReapedPending,
		trailSightingReasonPidGone,
		trailSightingReasonPidReadFailed,
		trailSightingReasonOrderingUncertified,
	}
}

// trailIsSightingReason reports whether v is one of the five recorded reasons.
//
// TWO SPACES AND TWO PREDICATES rather than one merged list: a consumer asks "is
// this an outcome I can look up?" and "is this a reason I can look up?" as
// different questions, and merging them would let a reason pass where an outcome is
// expected. TestTrailSightingValuesAgreeWithThePredicate checks each space rejects
// the other's values, which is the direction that keeps the split honest.
func trailIsSightingReason(v string) bool {
	switch v {
	case trailSightingReasonPidRunning, trailSightingReasonPidReapedPending,
		trailSightingReasonPidGone, trailSightingReasonPidReadFailed,
		trailSightingReasonOrderingUncertified:
		return true
	}
	return false
}

// --- fixtures -------------------------------------------------------------------------

// trailSightingPID is the pid every fixture pins. Plausible and constant, so a
// failure message names the same number the Detail does.
const trailSightingPID = 4242

// trailSightingPin is a well-formed pinStateOutcome for one verdict. One helper
// rather than four literals, so a row cannot quietly differ from its neighbours in
// a field the predicate does not read.
//
// It sets no StateColumn and no ToolStderr: those are what pinClassifyState fills
// on the arms that HAVE them, and a fixture that filled them for every verdict
// would be describing a read that never happens. The AC5 sweep plants them
// explicitly, which is where they belong.
func trailSightingPin(verdict string) pinStateOutcome {
	return pinStateOutcome{
		Verdict: verdict,
		PID:     trailSightingPID,
		Detail:  "a fixture per-pid read carrying verdict " + verdict,
	}
}

// trailSightingVerdicts is the four per-pid verdicts, spelled. There is no
// pinVerdicts() list shipped and adding one would grow the edit to
// process_pin_liveness_test.go for no gain here — pinIsVerdict
// (`pinIsVerdict`) is the space's own membership predicate, and
// TestTrailSightingValuesAgreeWithThePredicate feeds all four through it.
func trailSightingVerdicts() []string {
	return []string{
		pinStateRunning,
		pinStateExitedNotReaped,
		pinStateNoSuchProcess,
		pinStateInstrumentFailed,
	}
}

// trailSightingPremisesFor returns the premises that certify to orderValue, so
// every ordering fed to the predicate comes from #1439'S OWN PRODUCER rather than
// from a struct literal. A hand-built trailOrderResult{Value: trailOrderCertified}
// would let this file's tests pass against an ordering predicate that certifies
// nothing, which is the bypass AC3 exists to close.
//
// trailOrderVoidUnheld is THE FALL-THROUGH rather than a fourth case with a
// defensive default beneath it, so no arm here is unreachable. A fifth ordering
// value would land there and produce trailOrderVoidUnheld, which every caller's own
// "the fixture produced what it was asked for" guard rejects.
func trailSightingPremisesFor(orderValue string) trailOrderPremises {
	p := trailOrderCertifiedPremises()
	switch orderValue {
	case trailOrderCertified:
		return p
	case trailOrderVoidUnsighted:
		p.Sighted = false
		return p
	case trailOrderVoidNoExit:
		p.Exited = false
		return p
	}
	p.Held = false
	return p
}

// trailSightingExpectation is one cell of the 4×4: the value and the reason a pair
// must produce. Named fields rather than two positional strings, so a swapped
// expectation is a compile-readable mistake rather than a silently passing row.
type trailSightingExpectation struct{ Value, Reason string }

// trailSightingCertifiedRow is the CERTIFIED row of the 4×4, as data keyed by
// verdict.
//
// Data and not a second copy of the predicate's control flow: the predicate is a
// guard followed by a switch, this is a map lookup, and the coverage test's other
// three rows are a single constant pair. An expectation computed by re-running the
// predicate's own branching would be vacuous — this one reddens on any change to
// which verdict lands where.
func trailSightingCertifiedRow() map[string]trailSightingExpectation {
	return map[string]trailSightingExpectation{
		pinStateRunning:          {trailSightingEstablished, trailSightingReasonPidRunning},
		pinStateExitedNotReaped:  {trailSightingUnestablished, trailSightingReasonPidReapedPending},
		pinStateNoSuchProcess:    {trailSightingUnestablished, trailSightingReasonPidGone},
		pinStateInstrumentFailed: {trailSightingVoid, trailSightingReasonPidReadFailed},
	}
}

// trailSightingCheck asserts one result against its expectation and against every
// property the Detail content rule states. Factored so the sixteen product rows and
// the two off-space rows are held to the SAME bar rather than to two lists that can
// drift.
func trailSightingCheck(t *testing.T, got trailSightingResult, want trailSightingExpectation) {
	t.Helper()

	if got.Value != want.Value {
		t.Errorf("value: got %q (%s), want %q", got.Value, got.Detail, want.Value)
	}
	if got.Reason != want.Reason {
		t.Errorf("reason: got %q (%s), want %q — the reason is a FIELD here because two verdicts "+
			"share one value, so a consumer that cannot read it cannot tell them apart",
			got.Reason, got.Detail, want.Reason)
	}
	if !trailIsSightingValue(got.Value) {
		t.Errorf("the predicate emitted %q, which trailIsSightingValue rejects: a value a reader "+
			"cannot look up is a verdict they cannot interpret", got.Value)
	}
	if !trailIsSightingReason(got.Reason) {
		t.Errorf("the predicate emitted reason %q, which trailIsSightingReason rejects", got.Reason)
	}
	if got.Detail == "" {
		t.Error("the Detail is empty, so the record argues nothing and a reader has only the " +
			"value and reason to interpret")
	}
	// The MARKER and not a length against 512: reachCapCommand returns its input
	// unchanged AT exactly reachMaxCommandBytes and appends the marker only past it
	// (background_reach_probe_test.go:945-950), so a len < 512 check both
	// false-fails at the boundary and pins a literal that drifts when the constant
	// moves. The marker test is the property itself.
	if strings.Contains(got.Detail, reachTruncationMarker) {
		t.Errorf("the Detail was truncated at the %d-byte cap, so its argument reaches an "+
			"operator cut off: %q", reachMaxCommandBytes, got.Detail)
	}
	// The clause is what makes "every reason names the sighting as the instant its
	// verdict is about" checkable rather than a convention the arms happen to
	// follow. Sharing one constant across the five format strings stops them
	// drifting, but an arm can still omit the tail entirely and nothing else here
	// would notice.
	if !strings.Contains(got.Detail, trailSightingInstantClause) {
		t.Errorf("the Detail omits %q, so it publishes a verdict without naming the instant it is "+
			"about — and the instant is the whole axis this space differs from %q on: %q",
			trailSightingInstantClause, trailOutcomeRunningAtTrailer, got.Detail)
	}
}

// --- tests --------------------------------------------------------------------------------

// TestTrailSightingAllSixteenCombinations is AC2 made executable: one assignment
// rule covering every combination of #1439's four ordering values and the four
// per-pid verdicts, with none left to judgement.
//
// The product is GENERATED from trailOrderValues() × trailSightingVerdicts() rather
// than hand-written as sixteen literal rows. That makes "no combination left to
// judgement" structural instead of a count maintained by hand, and it is what keeps
// the coverage honest when a fifth ordering value lands: the table grows itself and
// the missing expectation reddens rather than going unnoticed.
//
// The DISTINCT-pairs guard carries #1439's discipline onto the two source lists: a
// duplicate in either would silently shrink the product while len() of neither list
// changed.
//
// Two rows sit OUTSIDE the product, for the two off-space verdicts the switch's
// default also serves — the zero "" of an unfilled pinStateOutcome and a junk
// string. Both expect void / pid-read-failed. Without them the default arm would be
// covered by one route only, and its "an unreadable verdict and a failed read are
// the same statement" claim would be asserted rather than checked.
func TestTrailSightingAllSixteenCombinations(t *testing.T) {
	orderings := trailOrderValues()
	verdicts := trailSightingVerdicts()
	certified := trailSightingCertifiedRow()

	type pair struct{ ordering, verdict string }
	seen := make(map[pair]bool, len(orderings)*len(verdicts))
	for _, o := range orderings {
		for _, v := range verdicts {
			seen[pair{o, v}] = true
		}
	}
	if len(seen) != 16 {
		t.Fatalf("the product of %d ordering value(s) and %d verdict(s) covers %d distinct "+
			"pair(s), want all 16 — a duplicate in either list shrinks the coverage while both "+
			"lengths look right", len(orderings), len(verdicts), len(seen))
	}
	if len(certified) != len(verdicts) {
		t.Fatalf("the certified row holds %d expectation(s) for %d verdict(s): every verdict needs "+
			"one, and a missing entry is a combination left to judgement", len(certified),
			len(verdicts))
	}

	for _, orderValue := range orderings {
		for _, verdict := range verdicts {
			t.Run(orderValue+" x "+verdict, func(t *testing.T) {
				// Every non-certified ordering reaches ONE cell regardless of the
				// verdict: that is the precedence decision, checked on all twelve of
				// its cells rather than at one representative pair.
				want := trailSightingExpectation{trailSightingVoid, trailSightingReasonOrderingUncertified}
				if orderValue == trailOrderCertified {
					got, ok := certified[verdict]
					if !ok {
						t.Fatalf("no expectation for verdict %q on the certified row", verdict)
					}
					want = got
				}

				ordering := trailSightingPremisesFor(orderValue).certify()
				// The fixture produced what it was asked for, first: without this a
				// premise-removal bug in the fixture would silently retarget the row.
				if ordering.Value != orderValue {
					t.Fatalf("the fixture certified %q (%s), want %q", ordering.Value,
						ordering.Detail, orderValue)
				}

				trailSightingCheck(t, trailEstablishSighting(ordering, trailSightingPin(verdict)), want)
			})
		}
	}

	// The default arm's second route. A certified ordering, so the guard above is
	// not what decides these.
	offSpace := trailSightingExpectation{trailSightingVoid, trailSightingReasonPidReadFailed}
	for _, verdict := range []string{"", "not-a-recorded-verdict"} {
		t.Run("off-space verdict "+trailSightingVerdictName(verdict), func(t *testing.T) {
			if pinIsVerdict(verdict) {
				t.Fatalf("%q is a recorded liveness verdict, so this row is not the off-space "+
					"route it claims to be", verdict)
			}
			ordering := trailSightingPremisesFor(trailOrderCertified).certify()
			if ordering.Value != trailOrderCertified {
				t.Fatalf("the fixture certified %q (%s), want %q", ordering.Value, ordering.Detail,
					trailOrderCertified)
			}
			trailSightingCheck(t, trailEstablishSighting(ordering, trailSightingPin(verdict)), offSpace)
		})
	}
}

// trailSightingVerdictName renders a subtest name for a verdict that may be empty,
// so the zero-verdict row is not an unnamed subtest.
func trailSightingVerdictName(v string) string {
	if v == "" {
		return "(empty)"
	}
	return v
}

// TestTrailSightingPremiseRemovalNeverEstablishes is AC3 made executable: an
// ordering result with one premise removed reaches void and NEVER established.
//
// Driven from #1439's own producer rather than a hand-built value —
// trailOrderCertifiedPremises(), one field cleared, then .certify() — which is the
// premise-removal shape TestTrailOrderEachPremiseHasItsOwnVoid
// (`TestTrailOrderEachPremiseHasItsOwnVoid`) already ships. THIS IS WHAT KEEPS THE
// HOLD-AS-PRECONDITION ARGUMENT FROM BEING BYPASSED by a caller that fabricates a
// certification: a struct literal carrying trailOrderCertified would satisfy this
// predicate while resting on nothing.
//
// Every row pins a pinStateRunning pid, and that is the point of the test rather
// than an incidental fixture choice: it is the ONE verdict that would otherwise
// establish, so a predicate that consulted the verdict before the ordering would
// redden on all three rows.
//
// Each subtest asserts the base fixture BOTH certifies AND establishes before
// flipping anything. The first premise stops a predicate that certified nothing
// from passing; the second stops one that established nothing from passing — and
// "never establishes" is a claim only worth checking against a predicate that can.
func TestTrailSightingPremiseRemovalNeverEstablishes(t *testing.T) {
	cases := []struct {
		name   string
		remove func(*trailOrderPremises)
	}{
		{name: "removing the sighting", remove: func(p *trailOrderPremises) { p.Sighted = false }},
		{name: "removing pyry's exit", remove: func(p *trailOrderPremises) { p.Exited = false }},
		{name: "removing the hold", remove: func(p *trailOrderPremises) { p.Held = false }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pin := trailSightingPin(pinStateRunning)

			in := trailOrderCertifiedPremises()
			base := in.certify()
			if base.Value != trailOrderCertified {
				t.Fatalf("the base fixture's ordering reads %q (%s), want %q — every row here "+
					"removes one premise from an OTHERWISE-CERTIFIED input", base.Value,
					base.Detail, trailOrderCertified)
			}
			if established := trailEstablishSighting(base, pin); established.Value != trailSightingEstablished {
				t.Fatalf("the base fixture establishes %q (%s), want %q — without this premise "+
					"a predicate that established NOTHING would pass every row below",
					established.Value, established.Detail, trailSightingEstablished)
			}

			tc.remove(&in)
			got := trailEstablishSighting(in.certify(), pin)
			if got.Value == trailSightingEstablished {
				t.Errorf("%s still establishes aliveness at the sighting: with a premise missing "+
					"the ordering is unearned, so the later reading rests on nothing and a "+
					"running pid proves nothing about the earlier instant (%s)", tc.name,
					got.Detail)
			}
			if got.Value != trailSightingVoid {
				t.Errorf("value: got %q (%s), want %q — a missing premise means nothing was "+
					"measured, and a negative read out of that is the collapse this space refuses",
					got.Value, got.Detail, trailSightingVoid)
			}
			if got.Reason != trailSightingReasonOrderingUncertified {
				t.Errorf("reason: got %q (%s), want %q — the void must name the ORDERING as what "+
					"failed, not the pid, which read as %s", got.Reason, got.Detail,
					trailSightingReasonOrderingUncertified, pinStateRunning)
			}
		})
	}
}

// TestTrailSightingValuesAgreeWithThePredicate is AC4 made executable, in
// TestTrailOrderValuesAgreeWithThePredicate's shape
// (`TestTrailOrderValuesAgreeWithThePredicate`): each list and its predicate agree in BOTH
// directions, each count is asserted against this ticket's own enumeration, and
// each predicate rejects the values of the adjacent spaces.
//
// The rejection direction is the one that earns its place. It leaves the naming
// rule reviewable against a fixed enumerated list rather than against prose, and
// two entries on that list are the collisions this space's naming exists to defeat:
// trailOutcomeRunningAtTrailer, which states the same English sentence from the
// reap-log evidence class, and trailOutcomeVoidLivenessInstrument, whose last four
// words a …-liveness-instrument-failed reason would have shared.
//
// EACH SPACE ALSO REJECTS THE OTHER'S VALUES. That is what keeps a reason from
// passing where an outcome is expected, which is the failure mode two predicates
// over one file invite and one merged list would guarantee.
func TestTrailSightingValuesAgreeWithThePredicate(t *testing.T) {
	values := trailSightingValues()
	if len(values) != 3 {
		t.Errorf("the closed outcome set holds %d value(s), want 3 — the count is this ticket's "+
			"own enumeration (established, unestablished, one void) and a change to it is a "+
			"change to what this predicate can answer", len(values))
	}
	for _, v := range values {
		if !trailIsSightingValue(v) {
			t.Errorf("%q is listed as a sighting outcome but trailIsSightingValue rejects it", v)
		}
	}

	reasons := trailSightingReasons()
	if len(reasons) != 5 {
		t.Errorf("the closed reason set holds %d value(s), want 5 — the count is this ticket's own "+
			"enumeration (one per per-pid verdict, plus the ordering pass-through) and a change "+
			"to it is a change to what a consumer can tell apart without parsing prose",
			len(reasons))
	}
	for _, v := range reasons {
		if !trailIsSightingReason(v) {
			t.Errorf("%q is listed as a sighting reason but trailIsSightingReason rejects it", v)
		}
	}

	// The other direction, over every value of the adjacent spaces rather than a
	// representative pair. The adjacent values are SPELLED rather than looped
	// through a helper: no trailGateValues() / trailAdmitValues() ships, and adding
	// one would grow the edit to trailer_admissibility_test.go for no gain here.
	adjacent := []string{
		"",
		// #1439's four ordering values: this predicate CONSUMES them, so absorbing
		// one into its own space is the realistic copy-paste.
		trailOrderCertified, trailOrderVoidUnsighted, trailOrderVoidNoExit, trailOrderVoidUnheld,
		// The four per-pid verdicts, likewise consumed.
		pinStateRunning, pinStateExitedNotReaped, pinStateNoSuchProcess, pinStateInstrumentFailed,
		// THE TWO COLLISIONS THIS NAMING DEFEATS. run-running-at-trailer states the
		// same English sentence from the reap-log evidence class, and
		// run-void-liveness-instrument-failed is the four words the pid-read-failed
		// reason was named away from.
		trailOutcomeRunningAtTrailer, trailOutcomeVoidLivenessInstrument,
		// One value from each remaining shipped space, and the scan's three input
		// states, which are one tab-completion from a result.
		trailGateUsable, trailAdmitProof, trailSeen, trailAbsent, trailAborted,
	}
	for _, v := range adjacent {
		if trailIsSightingValue(v) {
			t.Errorf("trailIsSightingValue accepts %q, which belongs to another space", v)
		}
		if trailIsSightingReason(v) {
			t.Errorf("trailIsSightingReason accepts %q, which belongs to another space", v)
		}
	}

	// The direction that earns its place: the two spaces this file ships must not
	// absorb each other.
	for _, v := range reasons {
		if trailIsSightingValue(v) {
			t.Errorf("trailIsSightingValue accepts the reason %q, so a reason would pass where an "+
				"outcome is expected", v)
		}
	}
	for _, v := range values {
		if trailIsSightingReason(v) {
			t.Errorf("trailIsSightingReason accepts the outcome %q, so an outcome would pass where "+
				"a reason is expected", v)
		}
	}
}

// TestTrailSightingResultCarriesNoCapturedBytes is AC5 made executable, in
// TestTrailRunOutcomeCarriesNoCapturedBytes's shape
// (trail_run_outcome_test.go:2207-2473) and reusing the shipped trailNeedle.
//
// THIS SWEEP IS LOAD-BEARING HERE IN A WAY IT COULD NOT BE FOR #1439, and the
// family's existing sweeps prove nothing about this record: they are
// PER-RECORD-TYPE — TestTrailRunOutcomeCarriesNoCapturedBytes marshals only
// trailClassifyRun's output — so a new record is invisible to every one of them.
//
// The two LIVE capture routes are pinStateOutcome's Detail and ToolStderr:
// pinClassifyState's branch 1 is "err != nil, stderr non-empty -> instrument-failed,
// naming exit status + stderr", so raw ps stderr reaches ToolStderr verbatim
// (`pinClassifyState`) and is folded into Detail (:348-349).
// StateColumn is string-bearing too and costs one line to include.
//
// ordering.Detail is planted as well, and it is worth naming what that plant IS AND
// IS NOT: #1439 pinned its predicate's inputs to three booleans and builds its
// Detail through trailOrderPremiseClause, so
// no captured byte can reach it THROUGH ITS PRODUCER — which is why the plant there
// has to be a struct literal. It is a discipline against a future field rather than
// a live route, and it must not be allowed to stand in for the two that are.
func TestTrailSightingResultCarriesNoCapturedBytes(t *testing.T) {
	// A struct literal, deliberately and only here: #1439's producer cannot be made
	// to emit a needle, so the plant has nowhere else to go.
	ordering := trailOrderResult{
		Value:  trailOrderCertified,
		Detail: "an ordering detail that also carries " + trailNeedle,
	}
	pin := pinStateOutcome{
		Verdict:     pinStateRunning,
		PID:         trailSightingPID,
		Detail:      "a per-pid detail that also carries " + trailNeedle,
		StateColumn: "S " + trailNeedle,
		ToolStderr:  "ps wrote " + trailNeedle,
	}

	got := trailEstablishSighting(ordering, pin)
	// The premise first, so the test cannot pass by classifying garbage: a record
	// that voided would exercise a different arm than the one under test.
	if got.Value != trailSightingEstablished {
		t.Fatalf("value: got %q (%s), want %q — the premise is a well-formed established record "+
			"whose every captured string carries the needle", got.Value, got.Detail,
			trailSightingEstablished)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the sighting result: %v", err)
	}
	if bytes.Contains(encoded, []byte(trailNeedle)) {
		t.Errorf("the marshalled sighting result carries captured bytes: %s", encoded)
	}

	// The structural half: the record has no field for a command string today, and
	// this is the check that a future field does not quietly add one. pinStateColumns
	// (`pinStateColumns`) refuses a `command` column at the source for
	// the same reason — those columns route the operator's CLAUDE_CODE_OAUTH_TOKEN /
	// ANTHROPIC_API_KEY into an artifact destined for a public issue.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keyed); err != nil {
		t.Fatalf("decoding the marshalled sighting result: %v", err)
	}
	for _, forbidden := range trailSightingForbiddenKeys() {
		for key := range keyed {
			if strings.Contains(key, forbidden) {
				t.Errorf("the sighting result carries key %q, which is %q-shaped: this record's "+
					"whole value is that it can be published unreviewed, and a command column "+
					"would inherit the operator-review-before-paste obligation onto it",
					key, forbidden)
			}
		}
	}

	// The same claim read off the TYPE rather than off one marshalled instance,
	// and it is not a duplicate of the walk above. MEASURED while building this
	// test: adding `Argv string \`json:"argv,omitempty"\`` to the record leaves the
	// walk above GREEN, because an omitempty field that is empty in this fixture
	// emits no key at all. A field is added to a struct BEFORE anything populates
	// it, so the marshalled walk alone starts refusing a command-shaped key only
	// once one already carries a value — which is the wrong end of the window for
	// a control whose whole job is that the record stays pasteable unreviewed.
	//
	// Walking the struct's own json tags catches the field the moment it lands. The
	// shipped per-record sweeps do not do this, and their records are not this
	// one's to change; here it costs a dozen lines.
	recordType := reflect.TypeOf(trailSightingResult{})
	for i := 0; i < recordType.NumField(); i++ {
		field := recordType.Field(i)
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if key == "" {
			key = field.Name
		}
		for _, forbidden := range trailSightingForbiddenKeys() {
			if strings.Contains(strings.ToLower(key), forbidden) {
				t.Errorf("trailSightingResult declares field %s as json key %q, which is "+
					"%q-shaped: the field is refused at the moment it is DECLARED, because a "+
					"marshalled record cannot show an omitempty key nothing has filled yet",
					field.Name, key, forbidden)
			}
		}
	}
}

// trailSightingForbiddenKeys is the family's command-shaped key denylist, shared by
// the two halves above so they cannot drift into refusing different things.
//
// A denylist is the weaker of this ticket's two controls and is not what keeps a
// trailer value out of the record — the PARAMETER LIST does that. finSighting's
// fields would marshal as terminal_reason / stop_reason / subtype / trailer_keys
// and none is on this list, which is precisely why trailEstablishSighting does not
// take one (see its § Two whole records).
func trailSightingForbiddenKeys() []string {
	return []string{"command", "args", "comm", "argv"}
}

// TestTrailSightingVoidsNeverReadAsNegative makes the two doctrines executable
// rather than merely stated in a doc comment.
//
// FIRST, over every row of the 4×4 plus the two off-space verdicts: no Detail
// claims the turn was declared finished. On this path no terminal reason is
// certified, so that instant does not exist — and a Detail claiming it would report
// ordering-backed evidence as the reap-log-backed finding
// trailOutcomeRunningAtTrailer names. This is the only check that the PROSE half of
// the naming rule holds; the value half is checked by the union closure map.
//
// SECOND, over every row that must land on void: none reads as unestablished.
// "Nothing was measured" and "the pid was not alive" are different findings, and
// publishing the first as the second manufactures a clean negative out of a failed
// premise or a broken instrument.
//
// The expected-void set is computed from the INPUTS rather than from the
// predicate's answer, so a predicate that answered unestablished everywhere would
// redden here rather than agreeing with itself.
func TestTrailSightingVoidsNeverReadAsNegative(t *testing.T) {
	answering := map[string]bool{
		pinStateRunning:         true,
		pinStateExitedNotReaped: true,
		pinStateNoSuchProcess:   true,
	}
	verdicts := append(trailSightingVerdicts(), "", "not-a-recorded-verdict")

	for _, orderValue := range trailOrderValues() {
		for _, verdict := range verdicts {
			ordering := trailSightingPremisesFor(orderValue).certify()
			if ordering.Value != orderValue {
				t.Fatalf("the fixture certified %q (%s), want %q", ordering.Value, ordering.Detail,
					orderValue)
			}
			got := trailEstablishSighting(ordering, trailSightingPin(verdict))

			for _, forbidden := range []string{"declared finished", "declared the turn finished"} {
				if strings.Contains(got.Detail, forbidden) {
					t.Errorf("the Detail for ordering %q x verdict %q claims %q: no terminal "+
						"reason is certified on this path, so that instant does not exist and "+
						"naming it reports this evidence class as %s. Detail: %q",
						orderValue, verdict, forbidden, trailOutcomeRunningAtTrailer, got.Detail)
				}
			}

			if orderValue == trailOrderCertified && answering[verdict] {
				continue
			}
			if got.Value == trailSightingUnestablished {
				t.Errorf("ordering %q x verdict %q reads as %q: nothing was measured on this "+
					"input, so a negative read out of it is manufactured rather than observed "+
					"(%s)", orderValue, verdict, trailSightingUnestablished, got.Detail)
			}
			if got.Value != trailSightingVoid {
				t.Errorf("ordering %q x verdict %q reads as %q, want %q", orderValue, verdict,
					got.Value, trailSightingVoid)
			}
		}
	}
}
