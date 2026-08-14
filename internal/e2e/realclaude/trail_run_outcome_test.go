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
//     (`tdnDecideAfter`) is the counter-example: nine distinct
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
//     (`trailGateResult`, :345-348);
//   - the trailer's lateness enters as the BoundFrom discriminator alone and
//     never as a trailObservation, which EMBEDS trailScanResult
//     (result_trailer_observation_test.go:141-142) and would therefore promote
//     the trailer pointer straight back into reach. The dotted selector for that
//     field is spelled nowhere in this file, in code OR in prose, so the census
//     recipe — a fixed-string grep, which cannot tell a dereference from a
//     comment — means what it says;
//   - the argv scan enters as two counts and an errored bool. pinScan.Matches
//     holds verbatim argv, which is why pinStateColumns
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
// tdnVerdictSummary is the corroboration
// renderer.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// --- the run's value space ----------------------------------------------------

// What one probe run can conclude, as a POSITIVE ALLOWLIST of sixteen: four
// answers and twelve named voids. NOTHING is the catch-all for "everything else"
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
	// trailOutcomeAliveAtSightingByOrdering: the gate read an absent
	// terminal_reason from a path that owes none, and #1440's pinned-pid sighting
	// route (`trailEstablishSighting`) established the command was alive
	// when the trailer was SIGHTED on pyry's stdout. THE SECOND ANSWER, and the only
	// one resting on an evidence class other than the reap log — which is why the
	// published record names its route in a field rather than leaving a reader to
	// infer the class from the value.
	//
	// A DIFFERENT INSTANT from the value above it, and that is the whole reason the
	// two are separate. This path certifies no terminal reason, so the instant a turn
	// was declared finished does not exist on it at all; what the ordering carries is
	// the trailer's sighting. Naming it for the sighting is what keeps a reader from
	// reading an ordering-backed finding as the reap-log-backed one.
	//
	// Deliberately NOT a `run-`-prefixed transform of #1440's own
	// trailSightingEstablished: that value is "sighting-alive-by-ordering", and
	// "run-sighting-alive-by-ordering" would CONTAIN it. The union map in
	// TestTrailAdmissibilityConstantsAreClosed compares for equality and is blind to
	// containment, which this family has already been bitten by once — hence the
	// hand-written both-ways guard at TestTrailRunComposesUnderANamedReasonOnAPathThatOwesNone.
	trailOutcomeAliveAtSightingByOrdering = "run-alive-at-sighting-by-ordering"
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
	// trailer, so no attribution on that path could prove aliveness-at-trailer,
	// at either instant. Not a negative: reporting a scan-side answer here would
	// imply a better instrument could have proved something.
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
	// reason there is no "when the turn was declared finished" instant for an
	// aliveness-at-declared-finish claim to be about.
	//
	// Reached only after the pinned-pid sighting route has been STAGED, consulted,
	// and has reported it could measure nothing. Since #1446 this value is what
	// remains of that arm once the route has answered, never an answer handed out
	// before any evidence was examined; since #1447 it is no longer the blanket over
	// everything the route failed to establish, because a route that DID measure a
	// pinned pid and did not establish has trailOutcomeVoidPinnedPidDidNotEstablish
	// below; and since #1448 it no longer covers a route that was never staged at
	// all, which has trailOutcomeVoidSightingRouteNotStaged below. A route that could
	// not read is not a route that was not there.
	//
	// TWO cases still share it — a pid read that did not answer, and an ordering that
	// WAS measured and whose premise failed — and they are separated in the published
	// record by the RouteReason field rather than by a value each, because the route
	// returns one value for both and only its reason tells them apart. A reason is a
	// field for exactly that purpose (trail_sighting_liveness_test.go:171-177), so
	// the separation lives in the published bytes rather than in the Detail's prose.
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
	// trailOutcomeVoidPinnedPidDidNotEstablish: the gate read
	// trailGateAbsentOwesNone, and #1440's pinned-pid sighting route MEASURED a
	// pinned pid — the ordering was certified and the pid was re-read after pyry
	// exited — and did not establish that the command was alive when the trailer was
	// SIGHTED. A READING, and still a void: nothing was certified, so there is no
	// "when the turn was declared finished" instant for a claim to be about.
	//
	// NEVER AN IT-EXITED VERDICT, and the claim limit is inherited VERBATIM from the
	// route's own value rather than restated loosely. trailSightingUnestablished
	// (trail_sighting_liveness_test.go, the sighting space's second value) is "a
	// statement about THIS EVIDENCE ROUTE, not about the command having been dead at
	// the sighting — the read is late by construction, so it cannot rule the earlier
	// instant out. There is deliberately no 'exited before the sighting' value in
	// this space for it to decay into." Neither is there one here, and the run-level
	// space already keeps that discipline: trailOutcomeNoRowMatched above is a
	// statement about THE SCAN and explicitly not about the command having exited.
	// This value joins that group.
	//
	// Deliberately NOT trailOutcomeVoidPathOwesNoReason, the sibling it forks from.
	// That one is the route having measured NOTHING; this one is a pid that WAS
	// read. Same void-ness, two different readings — and publishing a MEASURED
	// refutation of this route under a value whose Detail says the route was
	// consulted and answered would file a measurement under the name of one that
	// never happened.
	//
	// Deliberately NOT a `run-`-prefixed transform of trailSightingUnestablished,
	// for the reason trailOutcomeAliveAtSightingByOrdering states above:
	// "run-sighting-not-established" would CONTAIN the route's own value, and the
	// union map in TestTrailAdmissibilityConstantsAreClosed compares for EQUALITY
	// and is blind to containment — distinct to the map and a duplicate to a reader.
	//
	// Deliberately NOT trailOutcomeVoidLivenessInstrument, which is the argv scan's
	// per-pid read failing AS AN INSTRUMENT. Here the read ANSWERED; it simply did
	// not establish. Landing a working instrument's answer on that value would
	// report it as a broken one, the mirror of the collapse it exists to prevent.
	trailOutcomeVoidPinnedPidDidNotEstablish = "run-void-pinned-pid-did-not-establish"
	// trailOutcomeVoidSightingRouteNotStaged: the gate read trailGateAbsentOwesNone
	// and #1440's pinned-pid sighting route was NEVER STAGED — the ordering input
	// carries "", the zero of trailOrderResult and not a member of its own closed
	// space (trail_ordering_premises_test.go:158-161). No instrument was there to
	// answer, which is the shape EVERY run reaching this arm produces today.
	//
	// SINCE #1458 THAT IS TRUE OF THE ORDERING INPUT ALONE. The finding gather now
	// carries a PinnedPid its caller stages, so what a live run brings here is a
	// HALF-STAGED pair — pin filled, ordering unfilled — rather than an empty one,
	// and this value is still the answer: the arm's guard reads the ordering and
	// answers before the route is consulted, so a route missing the half it cannot
	// run without was never staged. No shipped gather fills the ordering (#1457 is
	// the ticket that stages it), and until one does, every run reaching this arm
	// lands here.
	//
	// THE ABSENCE OF AN INSTRUMENT, never a reading one produced. Still a void, for
	// its neighbours' reason — nothing was certified, so there is no "when the turn
	// was declared finished" instant for a claim to be about — but it is the one
	// value in this space saying the measurement never happened rather than that a
	// measurement came back empty.
	//
	// Deliberately NOT trailOutcomeVoidPathOwesNoReason, the sibling it forks from.
	// That one is the route STAGED and reporting it could measure nothing; this one
	// is a route that had nothing to read. Publishing an unstaged pair under the
	// sibling would file the absence of an instrument as a reading it produced — and
	// it would do so under a reason that asserts a measurement, since the route
	// answers an uncertified ordering with trailSightingReasonOrderingUncertified,
	// whose doc names only #1439's three trailOrderVoid* values. "" is not one of
	// them.
	//
	// Deliberately NOT trailOutcomeOutOfContract, for the reason #1417 exists. An
	// input no shipped gather stages — since #1458 the ordering rather than both —
	// is a routine reading rather than the caller's bug that value names, and no
	// contract check fires on it: the no-C10 note inside trailClassifyRun carries
	// that argument in full, and this value is what lets the note stay true.
	//
	// Deliberately NOT a `run-`-prefixed transform of any value in the sighting
	// space, for the containment reason trailOutcomeAliveAtSightingByOrdering's doc
	// states above: the union map in TestTrailAdmissibilityConstantsAreClosed
	// compares for EQUALITY and is blind to containment.
	trailOutcomeVoidSightingRouteNotStaged = "run-void-sighting-route-not-staged"
	// trailOutcomeVoidReasonNotOwedByPath: the trailer CARRIES a terminal_reason
	// and the observed runner path owes none, so the gate read
	// trailGatePresentOwesNone. A READING of the trailer rather than a defect in
	// it — streamrunner.Run passes claude's bytes through unchanged
	// (internal/agentrun/streamrunner/runner.go:177-179), so the line is genuinely
	// what the run produced — and still a void, because nothing was certified:
	// with no certified terminal reason there is no "when the turn was declared
	// finished" instant for an aliveness-at-declared-finish claim to be about.
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

// --- the evidence route's value space -------------------------------------------

// WHICH EVIDENCE CLASS PRODUCED A VERDICT, as a closed set of two, so that a
// reader never has to infer it from the outcome value. A verdict rather than a
// finding since #1447: the sighting route's own MEASURED NON-ESTABLISHMENT reaches
// a void that names this route too, and a reader who could not tell that
// refutation from the reap-log route's would be back to decoding the value string.
//
// The two answers this space names state NEARLY THE SAME ENGLISH SENTENCE from
// evidence the other path does not have: the reap log proves the group was alive
// when the trailer was WRITTEN, and #1440's ordering argument establishes the
// command was alive when the trailer was SIGHTED. #1440's header calls that
// collision its central risk (trail_sighting_liveness_test.go:67-86). Suffixing
// the outcome values is what keeps them distinct to a MAP; a field is what keeps
// them distinct to a CONSUMER, which is the half a naming convention cannot do.
//
// The values carry a `run-route-` prefix for the reason every value in this file
// carries `run-`: three spaces here now mean nearly the same words, so a
// copy-paste between them must be a visible mistake rather than a plausible line.
const (
	// trailRouteReapLog: pyry's own reap log named the held group on exactly one
	// anchored line. Set on trailOutcomeRunningAtTrailer alone.
	trailRouteReapLog = "run-route-reap-log"
	// trailRouteSighting: a certified ordering plus a pid pinned while the command
	// was still reachable and re-read after pyry exited. Set on
	// trailOutcomeAliveAtSightingByOrdering, where that read established aliveness,
	// and since #1447 on trailOutcomeVoidPinnedPidDidNotEstablish, where the same
	// read MEASURED the pid and did not. One route, two verdicts — which is the
	// whole reason the class lives in a field rather than in the value.
	trailRouteSighting = "run-route-pinned-pid-sighting"
)

// trailRunRouteValues is the closed set as data, and its one consumer is the
// predicate below — which is the whole point; see that predicate's own doc.
func trailRunRouteValues() []string {
	return []string{trailRouteReapLog, trailRouteSighting}
}

// trailIsRunRoute reports whether v is one of the two recorded evidence routes,
// for trailIsRunOutcome's reason: a value a reader of the published record cannot
// look up is a verdict they cannot interpret.
//
// DERIVED from the list above rather than switching over the constants a second
// time, which is where it departs from its four siblings in this family — and the
// departure is the point. Those predicates were written before the lists beside
// them and each needs an agreement test to keep the two from drifting
// (TestTrailRunOutcomeValuesAgreeWithThePredicate is this file's). A two-value
// space built in one commit has no such history to preserve, so deriving removes
// the drift class outright instead of shipping a test to guard a duplication
// nothing forced.
func trailIsRunRoute(v string) bool {
	for _, route := range trailRunRouteValues() {
		if route == v {
			return true
		}
	}
	return false
}

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
	// Ordering is #1439's certified-ordering result, taken WHOLE for the reason
	// Gate is: it is documented trap-free by construction
	// (`trailOrderResult`), and the predicate that consumes
	// it reads ordering.Value and nothing else. Its zero Value is "" — not a
	// member of its own space, and deliberately NOT a contract violation here; see
	// the note above C7.
	Ordering trailOrderResult
	// PinnedPid is the re-read, after pyry exited, of a pid pinned WHILE THE
	// COMMAND WAS STILL REACHABLE. It is not a member of Liveness and must never
	// be folded into it: Liveness is the argv scan's per-pid set, it feeds
	// tdnVerdictSummary into the published record and step 6's instrument-failure
	// void, and it goes blind once claude exits and the group re-parents to init
	// (`pinReadState`). A pinned pid landing in either would
	// change answers on runs that have nothing to do with this route.
	//
	// Taken WHOLE for the opposite reason to Ordering's: its Detail, StateColumn
	// and ToolStderr ARE string-bearing, and ToolStderr takes raw ps stderr
	// verbatim (`pinClassifyState`). Narrowing it to a bare verdict
	// string would leave no route for a needle to travel, which would make
	// TestTrailRunOutcomeCarriesNoCapturedBytes' sighting block unbuildable — and
	// a sweep that cannot fail measures nothing. The parameter shape is what makes
	// the control possible; § the Detail content rule is what it enforces.
	PinnedPid pinStateOutcome
}

// trailRunOutcome is the answer plus the provenance a reader needs to interpret
// it. COUNTS, NEVER ROWS; no command string; nothing from which the trailer
// pointer is reachable.
//
// The content rule for Detail is pinned rather than left to judgement: it MAY
// name outcome values, gate and admit values, route values, the sighting route's
// own value and reason, counts, pids, verdicts and BoundFrom; it may NEVER quote
// Gate.Detail, Admit.Detail, Ordering.Detail, a pinStateOutcome's Detail, its
// StateColumn or its ToolStderr, or the sighting result's Detail — that last one
// is swept at its own producer and is a budget and a duplication risk rather than
// a leak. That is the rule most easily broken by copying
// tdnDecideAfter, which does quote out.Detail
// — legitimately, because its record is not this one.
// TestTrailRunOutcomeCarriesNoCapturedBytes is the enforcing test, and it is why
// the needle goes into four inputs rather than one.
type trailRunOutcome struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
	Gate   string `json:"gate_value"`
	Admit  string `json:"admit_value,omitempty"`
	// Route names the EVIDENCE CLASS that produced the verdict, so a reader never
	// infers it from the outcome value — the two ANSWERS that carry a route state
	// nearly the same English sentence from evidence the other path lacks.
	//
	// Published exactly where THE ROUTE'S OWN MEASUREMENT DECIDED THE VALUE, which
	// since #1447 is the rule rather than "wherever a route was consulted": the
	// gate-absent-owes-none arm consults the sighting route on every run it reaches,
	// and on trailOutcomeVoidPinnedPidDidNotEstablish a pinned-pid read is what the
	// verdict rests on, so the route is named there.
	//
	// Since #1448 the fall-through beside it names the route too, which discharges
	// the delegation this doc used to leave open rather than passing it on again.
	// Once the never-staged case has a value of its own, that fall-through MEANS "the
	// route was staged, was consulted, and reported it could measure nothing" — and
	// the route's own answer is what separates it from that new neighbour, so the
	// route's measurement is what decided it. "" moves to where no route was staged
	// at all: on trailOutcomeVoidSightingRouteNotStaged the ABSENCE of an evidence
	// class produced the verdict, and naming one there would report an instrument as
	// having run. Same shape as Admit, which is "" where the predicate was owed no
	// call.
	Route string `json:"evidence_route,omitempty"`
	// RouteReason is the route's OWN reason, carried through WHOLE, so a reader tells
	// two verdicts of one route apart without parsing prose. #1440 made the reason a
	// field for exactly this — "a FIELD on the record rather than prose in the
	// Detail, in trailGateResult's Value+Reason+Detail shape, so a consumer tells an
	// exited-but-not-yet-reaped from a no-such-process, and either from an instrument
	// failure, WITHOUT PARSING PROSE" (trail_sighting_liveness_test.go:171-177) — and
	// one layer up the need is the same one: trailOutcomeVoidPathOwesNoReason is
	// reached both by a pid read that did not answer and by a MEASURED ordering whose
	// premise failed, and the route returns one value for both. Without this field
	// that separation would exist only in the Detail's sentences.
	//
	// A BICONDITIONAL rather than a best effort: non-empty exactly where Route is
	// trailRouteSighting, and then always a member of trailIsSightingReason. The
	// other direction holds because trailAdmitResult has no reason space at all — it
	// is a two-field Value+Detail record — so trailRouteReapLog publishes none and
	// never can. A reader seeing evidence_route with no evidence_route_reason on
	// trailOutcomeRunningAtTrailer is reading that fact rather than a dropped field.
	// TestTrailClassifyRun asserts both halves on every row.
	RouteReason string `json:"evidence_route_reason,omitempty"`
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

// trailIsRunOutcome reports whether v is one of the sixteen recorded outcomes. It
// mirrors trailIsGateValue / trailIsAdmitValue / pinIsVerdict / tdnIsReapVerdict
// and exists for the same reason: a value a reader of the published record
// cannot look up is a verdict they cannot interpret.
func trailIsRunOutcome(v string) bool {
	switch v {
	case trailOutcomeRunningAtTrailer, trailOutcomeAliveAtSightingByOrdering,
		trailOutcomeMatchedUnattributed, trailOutcomeNoRowMatched,
		trailOutcomeVoidBudgetFired, trailOutcomeVoidNoTrailer, trailOutcomeVoidTrailerScanAborted,
		trailOutcomeVoidPyryDidNotExit, trailOutcomeVoidArgvScanErrored,
		trailOutcomeVoidNoRowsParsed, trailOutcomeVoidLivenessInstrument,
		trailOutcomeVoidPathOwesNoReason, trailOutcomeVoidPinnedPidDidNotEstablish,
		trailOutcomeVoidSightingRouteNotStaged, trailOutcomeVoidReasonNotOwedByPath,
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
// There is deliberately no check over Ordering or PinnedPid either, and that one
// is load-bearing rather than economical — the argument is stated in place, above
// where a tenth check would go.
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
//     than chosen: without a usable trailer there is no certified
//     declared-finished instant for any claim to be about, and C5 has already
//     made a proof unreachable from every one of those values. One of those arms
//     CONSULTS AN EVIDENCE ROUTE before answering — #1446 wired
//     trailGateAbsentOwesNone to #1440's pinned-pid sighting route, which is the
//     only class available where the stream path writes no reap log — and it does
//     so inside the arm, never by falling through to the steps below.
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

	// THERE IS DELIBERATELY NO C10 OVER Ordering OR PinnedPid, and this note is
	// here because the instinct to add one is exactly right in shape and exactly
	// wrong in effect. C7 above validates every Liveness verdict against
	// pinIsVerdict and answers trailOutcomeOutOfContract on a miss, so validating
	// PinnedPid the same way — or Ordering against trailIsOrderValue — reads as
	// the obvious next line.
	//
	// OVER Ordering it would answer run-out-of-contract ON EVERY RUN THAT EXISTS
	// TODAY. No shipped gather stages it: trailRigGather assembles neither input,
	// and finGatherInputs still carries no field the ordering could arrive on. A
	// zero trailOrderResult carries Value "", not a member of its own closed space
	// — and an UNSTAGED INPUT IS A ROUTINE READING, not a caller's bug. Filing it
	// as one is the collapse #1417 exists to prevent, one value along.
	//
	// ON THE PIN SIDE THE PREMISE HAS MOVED AND THE CONCLUSION HAS NOT. #1458 gave
	// the finding gather a PinnedPid its caller stages, so an unfilled pin is no
	// longer what every run produces and the every-run argument is no longer
	// available on that side — the same way #1452 took the runner-path premise this
	// paragraph once argued from. Neither was ever what the conclusion rested on.
	// The consumed predicate already decides the pin safely and says so:
	// trailSightingReasonPidReadFailed's own doc names "the zero \"\" of an unfilled
	// pinStateOutcome" among the shapes it answers for, so an unfilled pin has an
	// argued home INSIDE the route and no guard is owed here. What a C10 over the
	// pin would now reject is narrower and worse, and it is TWO shapes rather than
	// one: every trailRigGather run, which stages no pin, and the EMPTY-SET case the
	// caller answers with the zero — a pin scan that found nothing to read. Both are
	// readings, and the route already carries a reason for each.
	//
	// The consequence is stated plainly rather than left implicit: PinnedPid is THE
	// ONE CLASSIFIER INPUT WITH NO CONTRACT CHECK OVER IT, so nothing downstream
	// catches a reading that arrives malformed. What stands in place of a check is
	// the producer rule on finGatherInputs.PinnedPid — pinReadState and nothing
	// else — and the two offline tests that pin the pass-through's shape,
	// TestFinGatherPinnedPidDoesNotReachTheLiveness and
	// TestFinGatherHalfStagedRouteMovesNoOutcome.
	//
	// The ORDERING side is not safe, and since #1448 it is decided in the step-1 arm
	// rather than here. trailEstablishSighting answers on a non-certified ordering
	// REGARDLESS of the verdict, so an unfilled pair reaches that function's own
	// first guard (trail_sighting_liveness_test.go:358-367) and comes back under
	// trailSightingReasonOrderingUncertified — a reason whose doc names only #1439's
	// three trailOrderVoid* values. "" is not one of them, so a record published
	// straight off that answer would claim a premise was MEASURED and failed on a run
	// where nothing was staged. The arm therefore tests Ordering.Value == "" BEFORE
	// consulting the route and answers trailOutcomeVoidSightingRouteNotStaged, which
	// is a READING and not a contract violation — the distinction this whole note is
	// about, and the reason the separation is a guard in the arm rather than a C10
	// here. trailRunAbsentOwesNoneReadings() leaves both zero and is the row that
	// proves the unstaged pair lands there rather than on run-out-of-contract.

	// --- the decision ---

	// Step 1: the trailer-side answers. Structural, so they outrank everything —
	// without a usable trailer there is no certified declared-finished instant for
	// a claim to be about. No default arm: C1 proved membership, so these seven
	// cases are total, and a NEW gate value registered in trailIsGateValue but not
	// handled here would not be caught by a bottom-of-function catch-all — the
	// shape this file exists to refuse. #1417 is what turned that open question
	// into a landed change, and it also corrects the shipped shorthand for where
	// such a value FALLS: not step 2. For any value that certifies nothing, C4 has
	// already rejected an admissibility value arriving beside it, so Admit is
	// forced empty and step 2's trailAdmitProof test cannot fire. An unhandled arm
	// falls through to steps 3-8 and awards a SCAN-SIDE ANSWER ABOUT PYRY from a
	// record the gate says certifies nothing — the same hazard class, one step
	// further down. Every value in TestTrailAdmissibilityConstantsAreClosed's union
	// map has an arm here, and that map catches a colliding value rather than an
	// unhandled one, so this comment is the only place the obligation is written.
	switch readings.Gate.Value {
	case trailGateUsable:
		// The one value that answers nothing on its own: a usable trailer
		// certifies the instant the rest of the decision is about.
	case trailGateBudgetFired:
		return decide(trailOutcomeVoidBudgetFired, "the trailer certifies terminal reason %q, so "+
			"the Terminate hook reaped INSIDE the hook (runner.go:492-503) BEFORE the trailer was "+
			"written and no attribution on that path could prove aliveness-at-trailer, at either "+
			"instant. A STRUCTURAL void, not a negative: reporting a scan-side answer here would "+
			"imply a better instrument could have proved something", readings.Gate.Reason)
	case trailGateNoTrailer:
		return decide(trailOutcomeVoidNoTrailer, "no trailer line was written. "+
			trailDeclaredFinishInstantClause+" Kept apart from %s: this is a statement about "+
			"the bytes, that one is the instrument reporting it could not read them",
			trailOutcomeVoidTrailerScanAborted)
	case trailGateScanAborted:
		return decide(trailOutcomeVoidTrailerScanAborted, "the trailer scan aborted, so the bytes "+
			"were unreadable. The instrument's own breakage and never an answer about pyry, which "+
			"is why it is kept apart from %s — a line past bufio.Scanner's 64 KiB default and a "+
			"genuine absence are otherwise indistinguishable", trailOutcomeVoidNoTrailer)
	case trailGateAbsentOwesNone:
		// #1446's arm, and the only one in this switch that CONSULTS AN EVIDENCE
		// ROUTE rather than answering from the gate value alone. It consults it
		// INSIDE the arm and never by falling through: this switch's own note above
		// explains that a record the gate says certifies nothing must not be
		// awarded a scan-side answer about pyry, and steps 3-8 are exactly that.
		//
		// #1440's route is the only evidence class available on this path — the
		// stream path writes no reap log at all on a clean exit
		// (trail_sighting_liveness_test.go:25-38) — and it is CONSUMED WHOLE,
		// never re-decided: this arm reads sighting.Value and sighting.Reason and
		// nothing else from the result.
		//
		// #1448's guard is the ONE decision this arm makes about its own inputs, and
		// it is made BEFORE the route is consulted rather than by re-reading what the
		// route returned — re-deriving a premise the predicate did not measure would
		// be reading the ordering result as something other than whole
		// (trail_sighting_liveness_test.go:305-316). An unstaged ordering is not an
		// uncertified premise, and the route cannot say so: its own first guard fires
		// on anything that is not trailOrderCertified and answers under a reason whose
		// doc names only #1439's three trailOrderVoid* values, so "" — the zero of
		// trailOrderResult, and the shape every run reaching here produces today —
		// would be published as a premise that was MEASURED and failed.
		//
		// The test is on the ORDERING ALONE, and that asymmetry is read off the
		// route's own docs rather than chosen. An unfilled pin already has an argued
		// home inside the route, whose pid-read-failed reason names the zero "" of an
		// unfilled pinStateOutcome outright, so no guard is owed on that side.
		// Testing both fields with && would be worse than redundant: on a half-staged
		// pair — ordering unfilled, pin filled — an && guard falls through and
		// publishes the exact false claim this guard exists to remove, one shape
		// along. SINCE #1458 THAT PAIR IS NOT A SHAPE REASONED ABOUT. It is what
		// every live run through finGatherReadings produces, because that gather
		// stages the pin and #1457 has not yet staged the ordering — so this guard
		// is doing live work rather than standing by for a hypothetical.
		// TestFinGatherHalfStagedRouteMovesNoOutcome drives the pair through the
		// shipped gather and this classifier, which is what keeps it that way.
		//
		// Narrower than !trailIsOrderValue(readings.Ordering.Value) on purpose. "" is
		// what an UNFILLED field carries and is the only shape any producer emits; a
		// non-empty value nobody defined is a caller's bug, and filing one as "the
		// route was never staged" would be the #1417 collapse mirrored. That shape
		// keeps today's answer, which the no-C10 note above records as deliberate.
		if readings.Ordering.Value == "" {
			return decide(trailOutcomeVoidSightingRouteNotStaged, "the gate read %s, and the "+
				"pinned-pid sighting route was NEVER STAGED: its ordering input is unfilled, so "+
				"no instrument was there to answer. The ABSENCE of an evidence class, never a "+
				"reading one produced, so no route is named. "+trailDeclaredFinishInstantClause+
				" Kept apart from %s, where a staged route measured nothing",
				trailGateAbsentOwesNone, trailOutcomeVoidPathOwesNoReason)
		}
		sighting := trailEstablishSighting(readings.Ordering, readings.PinnedPid)
		if sighting.Value == trailSightingEstablished {
			// The route establishes a DIFFERENT INSTANT from step 2's, which is
			// why it may not share step 2's value: this path certifies no terminal
			// reason, so the instant a turn was declared finished does not exist on
			// it, and what the ordering carries is the trailer's sighting.
			out.Route = trailRouteSighting
			out.RouteReason = sighting.Reason
			return decide(trailOutcomeAliveAtSightingByOrdering, "the sighting route answered "+
				"%s (%s), so the command was alive when the trailer was SIGHTED on pyry's "+
				"stdout: the pinned pid was still running when re-read after pyry exited. "+
				trailDeclaredFinishInstantClause+" An answer about the SIGHTING alone. The "+
				"evidence route field, not this value, is what keeps it apart from %s",
				sighting.Value, sighting.Reason, trailOutcomeRunningAtTrailer)
		}
		if sighting.Value == trailSightingUnestablished {
			// #1447's arm, and the second verdict this one route produces. The
			// route MEASURED a pinned pid — the ordering certified and the pid was
			// re-read after pyry exited — and did not establish aliveness at the
			// sighting. A reading, and still a void; the route is named because the
			// route's own measurement is what decided the value.
			//
			// The Detail may NOT say, or imply, that the command had exited before
			// the sighting: the re-read is late BY CONSTRUCTION, so it rules the
			// earlier instant neither in nor out. That limit is the route value's
			// own and is inherited rather than re-derived here.
			//
			// BUDGETED AGAINST THE LONGER REASON, and measured on the shipped format
			// string rather than on a draft of it. Two reasons reach this arm, and
			// sighting-reason-pid-reaped-pending is 10 bytes longer than
			// sighting-reason-pid-gone, so the worst case is the only figure worth
			// keeping: 492 of trailDetail's 512 bytes, against 482 on the shorter
			// one. reachCapCommand truncates SILENTLY, and what it would cut is the
			// tail — where both claim limits live — so the prose is written as tight
			// as it is deliberately. An earlier draft phrased the same five
			// obligations at 536 bytes and lost its closing clause to the cap; the
			// row beside this arm's is what turned that into a red build rather than
			// a quiet truncation.
			out.Route = trailRouteSighting
			out.RouteReason = sighting.Reason
			return decide(trailOutcomeVoidPinnedPidDidNotEstablish, "the gate read %s, and the "+
				"pinned-pid sighting route MEASURED a pid, answering %s (%s): no establishment "+
				"of aliveness at the trailer's sighting, and NEVER an it-exited verdict — the "+
				"re-read is late by construction. "+trailDeclaredFinishInstantClause+" Kept "+
				"apart from %s, where the route measured nothing", trailGateAbsentOwesNone,
				sighting.Value, sighting.Reason, trailOutcomeVoidPathOwesNoReason)
		}
		// trailSightingVoid, reached as the FALL-THROUGH rather than as a third
		// explicit case: trailEstablishSighting returns one of exactly three values,
		// so the two tests above leave this arm total with no return site a fixture
		// cannot reach. An explicit case plus a defensive tail would add an
		// unreachable return and break that totality — the same reason the switch
		// around this arm has no default.
		//
		// A reading, and still a void. The gate read an absent terminal_reason from a
		// path that owes none, so nothing was certified and there is no
		// declared-finished instant for an aliveness-at-declared-finish claim — but
		// the trailer was written and the record IS a reading, which is why this is
		// neither trailOutcomeVoidNoTrailer nor trailOutcomeOutOfContract. Since
		// #1447 the route having been consulted is no longer what this value means,
		// and since #1448 neither is "the route measured nothing" on its own: the
		// guard above has already taken the never-staged pair away, so what reaches
		// here is a route that WAS staged, was consulted, and reported it could
		// measure nothing. That is why the route is named here — its own answer is
		// what separates this value from the neighbour the guard sends away.
		//
		// The two cases left under this value — a pid read that did not answer, and a
		// MEASURED ordering whose premise failed — are separated in the RouteReason
		// FIELD and not in this prose, which is the whole reason #1440 built the
		// reason as a field. That is also what pays for the closing clause: the arm
		// used to spend its tail arguing which nothing this was, and now spends it
		// naming the neighbour a reader would otherwise confuse it with. An EXCHANGE
		// rather than an append — the arm rendered 461 of trailDetail's 512 bytes
		// before the split and must not grow, because reachCapCommand truncates
		// SILENTLY and what it cuts is the tail where the arguments live. Splitting
		// one arm into two IS the exchange: each says one thing where this said two.
		// The format string still interpolates sighting.Value, which is now always
		// trailSightingVoid and is carried rather than spelled so a future sighting
		// value cannot be mislabelled here.
		out.Route = trailRouteSighting
		out.RouteReason = sighting.Reason
		return decide(trailOutcomeVoidPathOwesNoReason, "the trailer carries no terminal_reason "+
			"and the OBSERVED runner path owes none, so the gate read %s: a reading, not a "+
			"defect in it. The pinned-pid sighting route was STAGED and answered %s; the reason "+
			"field names WHICH nothing. "+trailDeclaredFinishInstantClause+" Kept apart from "+
			"%s, where no route was staged", trailGateAbsentOwesNone, sighting.Value,
			trailOutcomeVoidSightingRouteNotStaged)
	case trailGatePresentOwesNone:
		// #1434's arm, and the mirror of the one above it. The gate read a
		// terminal_reason that IS on the line from a path owing none, so the record
		// is a reading, not a caller's bug — but nothing was certified, so there is
		// no declared-finished instant for an aliveness-at-declared-finish claim.
		// The two readings are kept apart because one is that path's documented
		// healthy shape and the other is not, and a record collapsing them would
		// publish the expected shape and the unexpected one under one name.
		return decide(trailOutcomeVoidReasonNotOwedByPath, "the trailer CARRIES a terminal_reason "+
			"and the runner path OBSERVED for this run owes none, so the gate read %s: a READING "+
			"of the trailer, and never a claim that pyry wrote the line. "+
			trailDeclaredFinishInstantClause+" Kept apart from %s, that path with NO reason and "+
			"its healthy shape, and from %s, which would file a measurement as the caller's "+
			"bug", trailGatePresentOwesNone, trailOutcomeVoidPathOwesNoReason,
			trailOutcomeOutOfContract)
	case trailGateOutOfContract:
		// Not a collapse: the gate already said "this record is not a reading",
		// and the run-level answer is that same sentence. What may never share a
		// value is two MEASURED nothings, and none of the twelve voids does.
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
		// The other evidence class. Its Detail is left EXACTLY as it was: naming
		// the route in prose here would spend the 68 bytes
		// TestTrailComposesUnderAPtyrunnerReading requires this arm to keep free,
		// and the field says it without costing any.
		out.Route = trailRouteReapLog
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
	// wantReason is the route reason the record must PUBLISH, as a per-row column
	// rather than as an invariant keyed on the value the way wantRoute is. The claim
	// needs the column: since #1448 two rows reach trailOutcomeVoidPathOwesNoReason
	// under two DIFFERENT reasons, which is the whole separation this ticket exists
	// for, and a map[value]reason cannot express it.
	//
	// Every row that is not on the sighting route leaves it at Go's zero "", which is
	// also the answer required there, so the column costs one line on the five rows
	// that carry a reason and nothing anywhere else. The drift a per-row want invites
	// is bounded by two invariants asserted beside it on EVERY row: the biconditional
	// against Route, and membership in trailIsSightingReason.
	wantReason string
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
// SINCE #1448 THIS IS THE NEVER-STAGED FIXTURE, and the name it inherited from
// #1417 no longer says the whole of what it is. Ordering and PinnedPid are both
// left ZERO, which is not an omission: the route is not merely unhelpful here, it
// is absent. It therefore reaches trailOutcomeVoidSightingRouteNotStaged rather
// than the fall-through beside it, and the two fixtures below are what drive that
// fall-through. A row here retargeted at either of them is a mistake rather than a
// refinement.
//
// SINCE #1458 IT IS NO LONGER THE SHAPE A LIVE RUN PRODUCES, and it is kept as it
// is deliberately. The finding gather stages a PinnedPid its caller fills, so a
// live run reaches this value HALF-STAGED — pin filled, ordering unfilled — and
// reaches it for the same reason, since the arm's guard reads the ordering alone.
// What this fixture pins is that an absent ROUTE is a reading rather than a
// caller's bug; the half-staged pair is driven through the shipped gather by
// TestFinGatherHalfStagedRouteMovesNoOutcome, which is where it belongs, because
// only a gather can show what a gather produces.
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

// trailRunSightingEstablishedReadings is #1417's gate answer with #1446's two
// inputs STAGED, so the pinned-pid sighting route establishes aliveness and the arm
// reports a finding instead of the void its unstaged sibling reaches.
//
// The ordering comes from trailCertifyOrdering rather than a
// trailOrderResult{Value: trailOrderCertified} literal, for the reason its
// neighbours drive the real gate: a hand-built certification would let this row go
// on passing against an ordering predicate that certifies nothing. The pin comes
// from #1440's trailSightingPin rather than a second fixture of this file's own —
// one helper is what keeps a row from quietly differing from its neighbours in a
// field the predicate does not read.
//
// Admit stays ZERO, inherited from the fixture this builds on and required by C4
// for the same reason: a gate certifying nothing was owed no call to the
// attribution predicate. No shipped gather stages the ORDERING — #1458 gave the
// finding gather the pin half and #1457 owes the other — so this shape is still a
// fixture rather than a reading any run produces today, which is the whole reason
// the arm is safe to land alone.
//
// A function rather than a package-level var, for trailRunWellFormed()'s reason:
// Liveness is a slice and go test -race runs this package's tests in parallel.
func trailRunSightingEstablishedReadings() trailRunReadings {
	in := trailRunAbsentOwesNoneReadings()
	in.Ordering = trailCertifyOrdering(true, true, true)
	in.PinnedPid = trailSightingPin(pinStateRunning)
	return in
}

// trailRunSightingOrderingUncertifiedReadings is the route STAGED and its ORDERING
// PREMISE REFUSED: trailCertifyOrdering with the trailer unsighted, which reaches
// trailOrderVoidUnsighted. The route measures the ordering, answers trailSightingVoid
// under trailSightingReasonOrderingUncertified, and the arm reaches
// trailOutcomeVoidPathOwesNoReason — the first of the two cases #1448 leaves sharing
// that value.
//
// The pin is deliberately RUNNING. It is the verdict that would ESTABLISH had the
// premise held, so a build that keyed on the pin rather than on the route's answer
// reports a finding here; and it is what makes the row a statement that the ordering
// OUTRANKS the verdict, which is the route's own precedence rule rather than this
// arm's.
//
// The ordering comes from trailCertifyOrdering rather than a hand-built
// trailOrderResult, for trailRunSightingEstablishedReadings()' reason one value
// along: a hand-typed void would let this row go on passing against an ordering
// predicate that no longer refuses anything. The difference from the never-staged
// fixture it builds on is the whole point and is one field wide — Value is a MEMBER
// of the ordering space here, where there it is "".
//
// A function rather than a package-level var, for trailRunWellFormed()'s reason:
// Liveness is a slice and go test -race runs this package's tests in parallel.
func trailRunSightingOrderingUncertifiedReadings() trailRunReadings {
	in := trailRunAbsentOwesNoneReadings()
	in.Ordering = trailCertifyOrdering(false, true, true)
	in.PinnedPid = trailSightingPin(pinStateRunning)
	return in
}

// trailRunSightingPidReadFailedReadings is the route STAGED, the ordering CERTIFIED,
// and the PID READ NOT ANSWERING: pinStateInstrumentFailed. The route answers
// trailSightingVoid under trailSightingReasonPidReadFailed, and the arm reaches
// trailOutcomeVoidPathOwesNoReason under the OTHER of the two reasons still sharing
// it.
//
// The pair with the fixture above is what makes the separation non-vacuous: both
// reach one run-level value through one sighting value, and only the published
// reason tells them apart, so a build that hard-coded either reason on the arm
// passes with one of the two rows and fails with both.
//
// A function rather than a package-level var, for trailRunWellFormed()'s reason:
// Liveness is a slice and go test -race runs this package's tests in parallel.
func trailRunSightingPidReadFailedReadings() trailRunReadings {
	in := trailRunAbsentOwesNoneReadings()
	in.Ordering = trailCertifyOrdering(true, true, true)
	in.PinnedPid = trailSightingPin(pinStateInstrumentFailed)
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

	// #1446's route CONSULTED AND REFUTED, which is what keeps its arm from
	// becoming a blanket answer once the two inputs are staged. Without this row a
	// build that ignored sighting.Value and reported the finding whenever the
	// ordering certified would pass every other row in this table.
	sightingRefuted := trailRunSightingEstablishedReadings()
	sightingRefuted.PinnedPid = trailSightingPin(pinStateNoSuchProcess)
	// The SECOND refuting verdict, which reaches the same run-level value through
	// the same sighting value under a DIFFERENT reason. Two things come from it and
	// neither is available without a row: the truncation-marker check in
	// TestTrailClassifyRun runs on sighting-reason-pid-reaped-pending, ten bytes
	// longer than its sibling's reason and therefore the arm's Detail worst case;
	// and the arm is pinned to key on sighting.Value rather than on the pin verdict,
	// which a build reading the verdict directly would pass on the row above alone.
	sightingRefutedPending := trailRunSightingEstablishedReadings()
	sightingRefutedPending.PinnedPid = trailSightingPin(pinStateExitedNotReaped)

	// #1448's pair, and the two rows are one claim rather than two: both reach
	// trailOutcomeVoidPathOwesNoReason through trailSightingVoid, and only the
	// PUBLISHED REASON separates them. One row alone would pass against a build that
	// hard-coded either reason onto the arm, and neither row alone can show that the
	// value survives a change of reason. They are also mandatory in the plain sense —
	// with the never-staged row retargeted below, no other row in this table reaches
	// the fall-through, and the coverage loop errors on a value no row reaches.
	orderingRefused := trailRunSightingOrderingUncertifiedReadings()
	pidReadFailed := trailRunSightingPidReadFailedReadings()

	return []trailRunCase{
		// --- the four answers ---
		{
			name: "an admissible attribution built by the real producers proves aliveness at the trailer",
			in:   fromProducers,
			want: trailOutcomeRunningAtTrailer,
		},
		{
			// #1446's arm, and mandatory for its two predecessors' reason: the
			// coverage loop in TestTrailClassifyRun errors on any value of
			// trailRunOutcomeValues() no row reaches. The only row in this table
			// that stages either of the two new readings.
			name:       "a certified ordering and a pinned pid still running establish aliveness at the sighting",
			in:         trailRunSightingEstablishedReadings(),
			want:       trailOutcomeAliveAtSightingByOrdering,
			wantReason: trailSightingReasonPidRunning,
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

		// --- the twelve voids ---
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
			// #1417's arm with #1448's answer: both of the route's inputs are
			// unfilled, so the guard answers before the route is consulted. The row is
			// what proves an unstaged pair is a reading rather than a caller's bug —
			// it must reach a named void and never trailOutcomeOutOfContract — and
			// what proves it never reports a premise as having been measured and
			// failed, since its wantReason is "" and the biconditional below forbids a
			// reason beside an unnamed route. Since #1458 this pair is no longer the
			// only shape a shipped gather produces — the finding gather stages the pin
			// — and the row deliberately keeps the empty pair, which is the one that
			// separates an absent route from a staged one.
			name: "an unstaged sighting route is the absence of an instrument, not a reading it produced",
			in:   trailRunAbsentOwesNoneReadings(),
			want: trailOutcomeVoidSightingRouteNotStaged,
		},
		{
			// The first of the two cases still sharing the fall-through. The ordering
			// was MEASURED and its premise refused, with a pin that is RUNNING — so
			// the row also pins the route's own precedence, which a build reading the
			// verdict first would invert into a finding.
			name:       "a measured ordering whose premise failed is a staged route that measured nothing",
			in:         orderingRefused,
			want:       trailOutcomeVoidPathOwesNoReason,
			wantReason: trailSightingReasonOrderingUncertified,
		},
		{
			// The second, and the row that makes the separation non-vacuous: same
			// value, same sighting value, DIFFERENT published reason. A build that
			// hard-coded either reason onto the arm passes with one of these two rows
			// and fails with both.
			name:       "a pid read that did not answer reaches the same value under the other reason",
			in:         pidReadFailed,
			want:       trailOutcomeVoidPathOwesNoReason,
			wantReason: trailSightingReasonPidReadFailed,
		},
		{
			// The same arm with #1446's route CONSULTED AND REFUTED, which the row
			// above cannot show: there both inputs are unstaged, so the route's own
			// ordering guard answers and the verdict is never reached. This row is
			// the one that separates "the arm consulted the route" from "the arm
			// answers whenever the ordering certifies" — and since #1447 what it
			// separates is finer: the route's MEASURED refusal from the arm's
			// unmeasured one, which the row above now holds alone.
			name:       "a certified ordering and a pinned pid that is gone measure a refusal of their own",
			in:         sightingRefuted,
			want:       trailOutcomeVoidPinnedPidDidNotEstablish,
			wantReason: trailSightingReasonPidGone,
		},
		{
			// The other verdict that refutes. Both reach the same run-level value
			// through the same sighting value, so the arm cannot be keying on the
			// pin verdict; and this one renders the LONGER of the two reasons,
			// which is what makes the Detail's worst case fail the build rather
			// than truncate quietly.
			name:       "a pinned pid awaiting its reap refutes under the arm's longer reason",
			in:         sightingRefutedPending,
			want:       trailOutcomeVoidPinnedPidDidNotEstablish,
			wantReason: trailSightingReasonPidReapedPending,
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

	// The third provenance field, as an INVARIANT over the classifier rather than a
	// second want column that could drift from it: a route is published on exactly
	// the values a route's own measurement decided, and names that route. The zero
	// value of a map lookup is "", which is also the answer required of every other
	// row, so one comparison carries both halves of the claim.
	//
	// Since #1448 the fall-through beside #1447's arm is a MEMBER rather than a
	// carrier of that "": what reaches it was staged, was consulted, and reported it
	// could measure nothing, so the route's own answer decided it. The "" is carried
	// by trailOutcomeVoidSightingRouteNotStaged, where no route was staged to decide
	// anything — which is the one row in this table where a named route would report
	// an instrument that never ran.
	wantRoute := map[string]string{
		trailOutcomeRunningAtTrailer:             trailRouteReapLog,
		trailOutcomeAliveAtSightingByOrdering:    trailRouteSighting,
		trailOutcomeVoidPinnedPidDidNotEstablish: trailRouteSighting,
		trailOutcomeVoidPathOwesNoReason:         trailRouteSighting,
	}

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
			if want := wantRoute[got.Value]; got.Route != want {
				t.Errorf("route provenance: %s published route %q, want %q — a finding must name "+
					"the evidence class it rests on, and every other arm must publish none rather "+
					"than a route it never consulted", got.Value, got.Route, want)
			}
			if got.Route != "" && !trailIsRunRoute(got.Route) {
				t.Errorf("route provenance: %q is outside the recorded route space, so a reader "+
					"cannot look up which evidence produced this verdict", got.Route)
			}
			// #1448's separation, in the published bytes rather than in the Detail's
			// prose. The want is per-row because two rows reach one value under two
			// reasons; the two invariants beside it are what keep a per-row want from
			// drifting into whatever the arm happens to emit.
			if got.RouteReason != tc.wantReason {
				t.Errorf("route reason: %s published reason %q, want %q — the route returns ONE "+
					"value for a pid read that did not answer and for a measured ordering whose "+
					"premise failed, so the reason is where a reader tells them apart", got.Value,
					got.RouteReason, tc.wantReason)
			}
			if (got.RouteReason != "") != (got.Route == trailRouteSighting) {
				t.Errorf("route reason: %s published reason %q beside route %q — a reason is "+
					"published exactly where the sighting route decided, and nowhere else. The "+
					"other direction holds because %s has no reason space at all: trailAdmitResult "+
					"is a two-field record, so that route publishes none and never can",
					got.Value, got.RouteReason, got.Route, trailRouteReapLog)
			}
			if got.RouteReason != "" && !trailIsSightingReason(got.RouteReason) {
				t.Errorf("route reason: %q is outside the recorded sighting-reason space, so a "+
					"reader cannot look it up — and the natural substitution, a Detail string "+
					"chosen for a better message, is not a member of it", got.RouteReason)
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
// real classifier — reaching trailGateAbsentOwesNone and then, since #1448,
// trailOutcomeVoidSightingRouteNotStaged: the fixture leaves the sighting route's
// two inputs unstaged, so the arm answers before consulting the route. Since #1458
// that pair is no longer what a live run produces — the finding gather stages the
// pin half — but the arm's answer is unchanged, because the guard reads the
// ordering alone. The FIXTURE is unchanged and deliberately so — this test's
// subject is the GATE reading, which neither #1448 nor #1458 touched, and swapping
// in a staged pair would make it a test of the route instead.
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
	if got.Value != trailOutcomeVoidSightingRouteNotStaged {
		t.Fatalf("composed to %q (%s), want %q", got.Value, got.Detail,
			trailOutcomeVoidSightingRouteNotStaged)
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
			if got.Value != trailOutcomeVoidSightingRouteNotStaged {
				t.Errorf("MatchCount %d: got %q (%s), want %q — step 1 answers before any "+
					"scan-side step, so the tail must not reach the decision", tail, got.Value,
					got.Detail, trailOutcomeVoidSightingRouteNotStaged)
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
		if absent.Value != trailOutcomeVoidSightingRouteNotStaged {
			t.Fatalf("the absent-key sibling composed to %q (%s), want %q", absent.Value,
				absent.Detail, trailOutcomeVoidSightingRouteNotStaged)
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
// (`TestTrailGateThenAdmit`) and reusing the shipped trailNeedle.
//
// The needle goes into EVERY string-bearing input the classifier can see —
// Gate.Detail, Admit.Detail, a pinStateOutcome's Detail and ToolStderr, and since
// #1446 Ordering.Detail and the pinned pid's Detail and ToolStderr — because the
// rule being enforced is not "do not copy the one field the ticket named" but "no
// Detail here quotes any input's captured string".
//
// # Five blocks, because the unit a fixture reaches is an OUTCOME, not an arm
//
// A record is classified once, but the arm that reads #1446's two inputs has FOUR
// reachable outcomes since #1448 and the fixture decides which — so covering the arm
// once covers a quarter of it. The proof block reaches run-running-at-trailer and can
// never travel the new inputs; the sighting block reaches
// run-alive-at-sighting-by-ordering; the refuted block reaches
// run-void-pinned-pid-did-not-establish FROM THE SAME ARM, reading the same two
// fields and rendering a different Detail from them; and #1448's two reach the arm's
// remaining outcomes — the never-staged value, answered by the guard BEFORE the route
// is consulted, and the fall-through the guard leaves behind.
//
// The third block is here because its absence was measured rather than argued. On the
// two-block tree, substituting this arm's sighting.Value operand for a needle-bearing
// input left the SUITE GREEN for PinnedPid.ToolStderr — the field the trust boundary
// runs through — and green for PinnedPid.Detail. Only Ordering.Detail reddened, and
// not on the needle: it overruns trailDetail's cap, so the truncation-marker check at
// TestTrailClassifyRun:1521-1524 caught it. A BUDGET kill, not a leak kill, and
// different fabric — the sweep below is this arm's only check that fails on the needle
// itself. Each block premise-asserts first, so none passes on garbage.
//
// # What the two new-input blocks ARE, stated rather than overclaimed
//
// NO LIVE LEAK ROUTE REACHES A PUBLISHED RECORD THROUGH THE NEW INPUTS, on either
// outcome. The arm reads sighting.Value and sighting.Reason and nothing else, and
// trailClassifyRun renders Liveness through tdnVerdictSummary but never renders
// PinnedPid at all. The plant is a discipline against a FUTURE edit that
// interpolates pin.Detail or pin.ToolStderr for a better failure message — the
// natural mistake, and the same framing #1440 used for its own trailOrderResult
// plant.
//
// WHAT #1458 MOVED IS THE CLAIM'S BASIS AND NOT THE CLAIM. Until that ticket the
// sentence was free: no shipped gather filled either input, so no live ps bytes
// could reach them whatever this classifier did. Now the finding gather carries a
// PinnedPid its caller fills from pinReadState, whose instrument-failed branch puts
// RAW ps STDERR into ToolStderr and folds it into Detail — so on a live run those
// bytes genuinely enter trailRunReadings, and the only thing standing between them
// and a published record is the non-rendering above. THE ROUTE IS NOW SWEPT AT THE
// GATHER TIER TOO, by TestFinGatherPinnedPidCarriesNoCapturedBytes, so this block
// is the classifier-tier half of a two-tier fabric rather than the whole of the
// coverage, one tier above where the channel opens.
func TestTrailRunOutcomeCarriesNoCapturedBytes(t *testing.T) {
	// The needle-planted per-pid read both blocks use. Shared as a function rather
	// than a value for trailRunWellFormed()'s reason.
	plantedPin := func() pinStateOutcome {
		return pinStateOutcome{
			Verdict:    pinStateNoSuchProcess,
			PID:        4242,
			Detail:     "a per-pid detail that also carries " + trailNeedle,
			ToolStderr: "ps wrote " + trailNeedle,
		}
	}

	// sweep is the two halves every block runs: the needle sweep over the
	// marshalled record, and the structural key walk that a future field does not
	// quietly add a command column. pinStateColumns refuses a `command` column at
	// the source for the same reason — those columns route the operator's
	// CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY into an artifact destined for a
	// public issue.
	sweep := func(t *testing.T, got trailRunOutcome) {
		t.Helper()

		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshalling the run outcome: %v", err)
		}
		if bytes.Contains(encoded, []byte(trailNeedle)) {
			t.Errorf("the marshalled run outcome carries captured bytes: %s", encoded)
		}

		var keyed map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &keyed); err != nil {
			t.Fatalf("decoding the marshalled run outcome: %v", err)
		}
		for _, forbidden := range []string{"command", "args", "comm", "argv"} {
			for key := range keyed {
				if strings.Contains(key, forbidden) {
					t.Errorf("the run outcome carries key %q, which is %q-shaped: this record's "+
						"whole value is that it can be published unreviewed, and a command column "+
						"would inherit the operator-review-before-paste obligation onto it", key,
						forbidden)
				}
			}
		}
	}

	t.Run("the reap-log route republishes none of its inputs", func(t *testing.T) {
		in := trailRunProofReadings()
		in.Gate = trailGateResult{Value: trailGateUsable, Reason: "completed",
			Detail: "a gate detail that also carries " + trailNeedle}
		in.Admit = trailAdmitResult{Value: trailAdmitProof,
			Detail: "an admissibility detail that also carries " + trailNeedle}
		in.Liveness = []pinStateOutcome{plantedPin()}

		got := trailClassifyRun(in)
		// The premise first, so the test cannot pass by classifying garbage.
		if got.Value != trailOutcomeRunningAtTrailer {
			t.Fatalf("value: got %q (%s), want %q — the premise is a well-formed record whose "+
				"captured strings carry the needle", got.Value, got.Detail,
				trailOutcomeRunningAtTrailer)
		}
		sweep(t, got)
	})

	t.Run("the pinned-pid sighting route republishes none of its inputs", func(t *testing.T) {
		in := trailRunSightingEstablishedReadings()
		in.Gate.Detail = "a gate detail that also carries " + trailNeedle
		in.Liveness = []pinStateOutcome{plantedPin()}
		// #1446's two inputs, which no other fixture in this file can carry a
		// needle through. The ordering keeps its certified VALUE — the arm is
		// unreachable without it — and only its Detail is planted.
		in.Ordering.Detail = "an ordering detail that also carries " + trailNeedle
		in.PinnedPid.Detail = "a pinned-pid detail that also carries " + trailNeedle
		in.PinnedPid.ToolStderr = "ps wrote " + trailNeedle

		got := trailClassifyRun(in)
		if got.Value != trailOutcomeAliveAtSightingByOrdering {
			t.Fatalf("value: got %q (%s), want %q — the premise is the one arm that consults the "+
				"two inputs this block plants the needle in, and any other arm makes the sweep a "+
				"statement about something else", got.Value, got.Detail,
				trailOutcomeAliveAtSightingByOrdering)
		}
		if got.Route != trailRouteSighting {
			t.Fatalf("route: got %q, want %q — the key walk below is over this record's published "+
				"key set, and the route key is the one this ticket adds to it", got.Route,
				trailRouteSighting)
		}
		sweep(t, got)
	})

	t.Run("the same route consulted and REFUTED republishes none of its inputs", func(t *testing.T) {
		in := trailRunSightingEstablishedReadings()
		in.PinnedPid = trailSightingPin(pinStateNoSuchProcess)
		in.Gate.Detail = "a gate detail that also carries " + trailNeedle
		in.Liveness = []pinStateOutcome{plantedPin()}
		// The same three plants as the block above, on the SAME arm's other
		// outcome. The ordering keeps its certified value here too, and that is
		// what makes this the void the route REACHED rather than the void it was
		// never consulted for: without a certified ordering trailEstablishSighting
		// answers from its first guard (trail_sighting_liveness_test.go:358-367)
		// and never reads the pin at all, so the refusal has to come from the pid.
		in.Ordering.Detail = "an ordering detail that also carries " + trailNeedle
		in.PinnedPid.Detail = "a pinned-pid detail that also carries " + trailNeedle
		in.PinnedPid.ToolStderr = "ps wrote " + trailNeedle

		got := trailClassifyRun(in)
		if got.Value != trailOutcomeVoidPinnedPidDidNotEstablish {
			t.Fatalf("value: got %q (%s), want %q — the premise is the OTHER outcome of the arm "+
				"the block above covers, which reads the same two inputs and renders a different "+
				"Detail from them", got.Value, got.Detail,
				trailOutcomeVoidPinnedPidDidNotEstablish)
		}
		// RE-POINTED at #1447 rather than deleted, and the distinction is
		// load-bearing. This assertion is not decoration: it is what guarantees the
		// key walk below runs over the record's ROUTE-BEARING key set, so an arm
		// that later dropped the route would shrink the swept set without reddening
		// anything. Deleting it — the tempting move once "" stopped being the
		// expected value — is the shape of hole #1446 was reworked for.
		//
		// The route-ABSENT key set kept its coverage when #1447 filled Route here,
		// checked rather than waved through: Route is the only field that differs
		// between the pre- and post-#1447 refuted records and it is omitempty, so
		// the set walked here is a strict SUPERSET of the one it replaced. #1448's
		// never-staged block below walks a route-absent record but INHERITS that
		// coverage rather than supplying it: its own set is a strict SUBSET.
		if got.Route != trailRouteSighting {
			t.Fatalf("route: got %q, want %q — this arm's own MEASUREMENT decided the value, so "+
				"the record names the route and the key walk below runs over the published key "+
				"set that has the route key in it", got.Route, trailRouteSighting)
		}
		if got.RouteReason != trailSightingReasonPidGone {
			t.Fatalf("route reason: got %q, want %q — same obligation as the route premise above, "+
				"on the field #1448 adds to the same key set", got.RouteReason,
				trailSightingReasonPidGone)
		}
		sweep(t, got)
	})

	t.Run("an unstaged route republishes none of the inputs it never read", func(t *testing.T) {
		in := trailRunAbsentOwesNoneReadings()
		in.Gate.Detail = "a gate detail that also carries " + trailNeedle
		in.Liveness = []pinStateOutcome{plantedPin()}
		// The ordering keeps its UNFILLED Value — that is what makes this the
		// never-staged arm — and only its Detail is planted, which is the one shape
		// no other block in this test can produce.
		in.Ordering.Detail = "an ordering detail that also carries " + trailNeedle
		in.PinnedPid.Detail = "a pinned-pid detail that also carries " + trailNeedle
		in.PinnedPid.ToolStderr = "ps wrote " + trailNeedle
		// AND IN THE VERDICT ITSELF, the deterministic half of a prohibition that
		// would otherwise be prose over prose. The guard fires on Ordering.Value
		// REGARDLESS of the verdict, so this arm is reachable carrying an arbitrary
		// verdict string; PinnedPid is the one input the classifier has no contract
		// check over (the no-C10 note says so deliberately, and C7 validates only
		// Liveness); and trailRunOutcome's content rule PERMITS naming "verdicts",
		// which was written when pinReadState's four constants were the only
		// producer. The natural sentence to write here with bytes to spare — "the
		// pair is unstaged, the pinned pid read %q" — would make this the first
		// run-level arm to publish an unvalidated string. This plant is what reddens
		// if someone writes it, and the needle is short enough that it fails on the
		// needle rather than on trailDetail's cap — a budget kill, different fabric.
		// The fall-through below carries the same plant, for a reason of its own.
		in.PinnedPid.Verdict = trailNeedle

		got := trailClassifyRun(in)
		if got.Value != trailOutcomeVoidSightingRouteNotStaged {
			t.Fatalf("value: got %q (%s), want %q — the premise is the arm reached BEFORE the "+
				"route is consulted, which is the only one whose verdict field is free",
				got.Value, got.Detail, trailOutcomeVoidSightingRouteNotStaged)
		}
		// Both premises assert the RECORD SHAPE here and do not anchor the walk, and
		// the distinction is written down so nobody later deletes block 5's premises
		// believing this block replaces them. Route and RouteReason are omitempty and
		// both empty on this arm, so the key set walked below is a strict SUBSET of
		// the one the route-bearing blocks walk. That is sound — the superset is
		// walked three times over — but a field-shaped hole could only be caught
		// there.
		if got.Route != "" || got.RouteReason != "" {
			t.Fatalf("route provenance: got %q/%q, want both empty — no route was staged to "+
				"decide anything here, and naming one would report an instrument that never ran",
				got.Route, got.RouteReason)
		}
		sweep(t, got)
	})

	t.Run("a staged route that measured nothing republishes none of its inputs", func(t *testing.T) {
		in := trailRunSightingOrderingUncertifiedReadings()
		in.Gate.Detail = "a gate detail that also carries " + trailNeedle
		in.Liveness = []pinStateOutcome{plantedPin()}
		// The ordering keeps its VOID value — a member of the ordering space, which is
		// the one field separating this arm from the block above — and only its Detail
		// is planted. The route reads that Detail and folds nothing of it into what
		// this arm publishes, which is the claim.
		in.Ordering.Detail = "an ordering detail that also carries " + trailNeedle
		in.PinnedPid.Detail = "a pinned-pid detail that also carries " + trailNeedle
		in.PinnedPid.ToolStderr = "ps wrote " + trailNeedle
		// AND IN THE VERDICT ITSELF, which is sharper here than on the block above
		// rather than a copy of it. There the classifier answered BEFORE any route
		// ran, so no sighting Detail existed to leak from; here the route DID run and
		// folded the verdict VERBATIM into its own Detail
		// (trail_sighting_liveness_test.go:358-367), so this is the one arm where the
		// captured string is already sitting one dereference away. The ordering guard
		// fires whatever the pin reads, so the arm is reachable carrying an arbitrary
		// verdict string, and this plant is what reddens if a later edit interpolates
		// sighting.Detail for a better failure message.
		in.PinnedPid.Verdict = trailNeedle

		got := trailClassifyRun(in)
		if got.Value != trailOutcomeVoidPathOwesNoReason {
			t.Fatalf("value: got %q (%s), want %q — the premise is the arm's fall-through, whose "+
				"route WAS staged and reported it could measure nothing", got.Value, got.Detail,
				trailOutcomeVoidPathOwesNoReason)
		}
		if got.Route != trailRouteSighting {
			t.Fatalf("route: got %q, want %q — the key walk below runs over the record's "+
				"ROUTE-BEARING key set", got.Route, trailRouteSighting)
		}
		// Load-bearing for the reason the route premise above is: it anchors the walk
		// to the FIELD-BEARING key set, so an arm that later stopped publishing the
		// reason would shrink the swept set without reddening anything here.
		if got.RouteReason != trailSightingReasonOrderingUncertified {
			t.Fatalf("route reason: got %q, want %q — this arm's two cases differ ONLY in this "+
				"field, so a block that did not pin it could be sweeping either", got.RouteReason,
				trailSightingReasonOrderingUncertified)
		}
		sweep(t, got)
	})
}

// trailRunOutcomeValues is the closed set as data, so a coverage loop can assert
// over it. It is derived from trailIsRunOutcome's own space by construction: a
// value listed here that the predicate rejects, or vice versa, goes red in
// TestTrailRunOutcomeValuesAgreeWithThePredicate below.
func trailRunOutcomeValues() []string {
	return []string{
		trailOutcomeRunningAtTrailer,
		trailOutcomeAliveAtSightingByOrdering,
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
		trailOutcomeVoidPinnedPidDidNotEstablish,
		trailOutcomeVoidSightingRouteNotStaged,
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
	if len(values) != 16 {
		t.Errorf("the closed set holds %d value(s), want 16 — the count is the ticket's own "+
			"enumeration and a change to it is a change to what the run can conclude. Sixteen "+
			"rather than fifteen since #1448 gave %s a value of its own: an ordering nothing "+
			"staged is not a premise that was measured and failed, and publishing the absence of "+
			"an instrument under %s reported it as a reading that instrument produced",
			len(values), trailOutcomeVoidSightingRouteNotStaged, trailOutcomeVoidPathOwesNoReason)
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

// --- the shared clause ----------------------------------------------------------

// trailDeclaredFinishInstantClause is the fixed sentence every step-1 arm that
// forecloses a claim on CERTIFIES-NOTHING grounds makes that argument with. It
// names the instant that does not exist on those arms, and naming it is the whole
// point: a SECOND instant now exists in this family. trailEstablishSighting
// (`trailEstablishSighting`) establishes aliveness at the trailer's
// SIGHTING on pyry's stdout from a certified ordering and a pinned pid, on exactly
// the paths these six arms answer. A Detail foreclosing "a claim" unqualified
// would forbid that finding from a file that cannot see it — and since #1446 one
// of the six carriers IS that finding, which is the sharpest form of the same
// argument: the arm most easily misread as an aliveness-at-declared-finish claim is
// the one that must say in these words that it is not one. #1447's carrier is that
// same route's other verdict, and it must say it for the mirror reason: an arm that
// forecloses a claim on certifies-nothing grounds while ALSO refusing to claim the
// command had exited has two limits to state, and only one of them is checkable.
//
// A shared constant rather than six hand-written copies so the arms cannot DRIFT
// in how they name the instant — trailSightingInstantClause's shape
// (`trailSightingInstantClause`), and its doctrine too: sharing it is not
// on its own what makes "every
// certifies-nothing arm names the instant" true, because an arm can still omit it
// from its own format string. What makes the rule checkable is
// TestTrailRunCertifiesNothingArmsNameTheInstant, which requires exactly these
// six arms' Details to carry it and every other row's not to.
//
// Deliberately NOT trailSightingInstantClause, which names the OTHER instant: that
// one says a verdict IS about the sighting, this one says no verdict here is about
// the declared finish. It is likewise NOT a member of any value space and so does
// not join TestTrailAdmissibilityConstantsAreClosed's union map — it is prose, and
// a clause in that map would assert a membership no consumer can hold.
//
// It costs 117 bytes of trailDetail's 512-byte cap and leaks nothing: it
// interpolates no input at all. #1443's three arms SPLICE it in place of the
// sentence each wrote by hand rather than APPENDING it, and that is arithmetic
// rather than taste — trailGatePresentOwesNone renders 472 bytes today, so
// appending 118 (the clause plus its separating space) overflows the cap, and
// reachCapCommand truncates SILENTLY, cutting off the closing argument the Detail
// exists to make. #1446's fourth carrier is a NEW format string and so pays no
// exchange, but it inherits the same ceiling: it renders 469 bytes with the clause
// in place, and the void arm beside it had to give up a clause to afford one.
//
// This declaration sits at the END of the file rather than beside the arms that use
// it, and that placement is deliberate rather than careless: sixteen other files in
// this package carry a hundred line-number cites into this one, the highest at
// `TestTrailRunOutcomeValuesAgreeWithThePredicate`, and a declaration inserted anywhere above that
// displaces every cite below it. The filename is spelled there rather than left as
// a bare `:NNN` on #1434's evidence: a bare ref inherits the LAST-NAMED FILE, which
// two paragraphs up is trail_sighting_liveness_test.go, and it reads clean under
// every filename-anchored sweep while pointing at the wrong file. The identifier is
// one grep away in the same file; a hundred stale cites are not.
const trailDeclaredFinishInstantClause = "Nothing is certified, so there is no declared-finished instant for an aliveness-at-declared-finish claim to be about."
