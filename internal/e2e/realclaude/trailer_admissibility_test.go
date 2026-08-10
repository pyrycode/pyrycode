//go:build e2e_realclaude

package realclaude

// Two pure predicates that decide whether a probe run's trailer and reap-log
// evidence is ADMISSIBLE, so that the run-level classifier (#1271) never has to.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence. Everything here runs offline: no live
// claude, no credentials, no daemon, no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
//
// # The claim, and why it is admissible on some paths only
//
// The probe's strongest claim rests on pyry's own reap log. ptyrunner's teardown
// order is pinned in its own comment (runner.go:479-485):
//
//	cancel() -> wg.Wait() -> counter.Stop() -> emitter.Close() -> cancel() -> [reap] -> sess.Close()
//
// emitter.Close() writes the trailer; the reap defer (runner.go:398) SIGKILLs
// claude's descendant groups AFTER it. So on that path a process group named in
// pyry's reap log was alive strictly after the trailer was written, and
// therefore alive when it was written — a deterministic proof where a
// point-in-time ps offers a guess.
//
// The asymmetry is the whole point: an attribution HIT PROVES ALIVENESS, an
// attribution MISS PROVES NOTHING. Every way of failing to attribute therefore
// gets its own name, and none of them may read as "the group had exited".
//
// But the order is path-conditional. The budget's Terminate hook reaps INSIDE
// the hook (runner.go:492-503), BEFORE the trailer, and a third reap runs on
// operator-cancel (runner.go:313-316). The reap line lands on stderr and the
// trailer on stdout — separate pipes, separate copier goroutines — so the
// captured bytes carry no ordering between them. The one signal that tells the
// paths apart is the trailer's own terminal_reason: a budget-fired run renders
// max_turns, and on such a run the attribution is VOID, NOT NEGATIVE.
//
// # The shape both functions share: validate, then decide
//
// Each opens with a CONTRACT BLOCK that rejects any input its documented
// producer cannot emit, and only then decides. That is what makes every later
// arm's precondition true by construction — no arm has to defend against an
// impossible input, because the impossible inputs were already named and
// returned. It is also how "no value is the catch-all" is satisfied: the
// out-of-contract value is a GUARD AT THE TOP, never a fall-through at the
// bottom of a switch. A fall-through catch-all is exactly the collapse these two
// functions exist to refuse.
//
// # Reused, not rebuilt
//
// trailScan / trailScanResult (result_trailer_observation_test.go:180, :98) is
// the trailer input, shipped by #1266, including its aborted state and its
// fixtures. tdnClassifyReapLog / tdnReapOutcome / tdnIsReapVerdict
// (teardown_liveness_test.go:144, :116, :1176) is the attribution input, shipped
// by #1253; pyry's stderr is NOT re-parsed here. resultTrailer
// (tool_loop_test.go:194) is the decode, reachCapCommand
// (background_reach_probe_test.go:945) the cap.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// --- the wire literal the budget path renders --------------------------------

// trailBudgetTerminalReason is emitter.go:429-431's terminal_reason for
// ExitReasonMaxTurns, as a STRING LITERAL and deliberately not a reference to
// anything streamjson defines — wireFields is unexported, so no test in this
// package could reference it even if that were wanted.
//
// The failure direction is the bad one, and it is named here rather than papered
// over. If someone renames the wire value, this gate stops recognising
// budget-fired runs and reports them as trailGateUsable, so a structural void
// becomes a FALSE PROOF with nothing going red. Pinning it would need either an
// exported mapping (a production change, out of scope for a probe-family ticket)
// or a trailer captured from a real budget-fired run (no such fixture exists;
// trailPaddedTrailer is hand-built). Recorded as a known limit on what an
// admissible-vs-void answer rests on. Same discipline as tdnReapMessage
// (teardown_liveness_test.go:81-89): a rename in production must not be silently
// followed.
const trailBudgetTerminalReason = "max_turns"

// --- the gate's value space --------------------------------------------------

// What a trailer scan result can support, as a POSITIVE ALLOWLIST of seven.
// NOTHING is the catch-all for "everything else": trailGateOutOfContract is a
// named answer about the CALLER's record, reached by a guard at the top of the
// function, and never a fall-through that would let an unrecognised input read
// as an answer about pyry.
//
// Every value carries a `gate-` prefix, and that prefix is load-bearing rather
// than cosmetic. trailAbsent and trailAborted are the scan's INPUT STATES, while
// this space holds RESULTS that mean "no trailer line was written" and "the
// trailer scan aborted". A predicate whose whole purpose is refusing to collapse
// distinct things must not ship a namespace in which an input state and a result
// are one tab-completion apart, so a copy-paste between the two spaces is a
// visible mistake here rather than a plausible line.
const (
	// trailGateUsable: the trailer is usable and carries a non-empty terminal
	// reason. On this path emitter.Close() wrote the trailer before the reap
	// defer, so a reap-log attribution can be proof.
	trailGateUsable = "gate-trailer-usable"
	// trailGateNoTrailer: no trailer line was written. There is no terminal
	// reason to certify, so nothing downstream may rest on this run's trailer.
	trailGateNoTrailer = "gate-no-trailer"
	// trailGateScanAborted: the trailer scan aborted — the instrument could not
	// read the bytes. Never an answer about pyry, and kept distinct from
	// trailGateNoTrailer because a bufio.Scanner overflow and a genuinely absent
	// trailer are otherwise indistinguishable. That distinction is #1266's whole
	// reason for existing and collapsing it here would spend it.
	trailGateScanAborted = "gate-scan-aborted"
	// trailGateBudgetFired: the trailer reports a budget-fired run. NOT usable:
	// the Terminate hook reaped before the trailer, so the attribution is void
	// rather than negative. It still certifies its reason, because the
	// admissibility predicate needs that reason to name the void.
	trailGateBudgetFired = "gate-budget-fired"
	// trailGateAbsentOwesNone: the trailer carries NO terminal_reason key at all,
	// and the observed runner path owes none. A READING rather than a caller's
	// bug: streamrunner.Run tees claude's stdout for the watchdog and passes the
	// bytes through unchanged (internal/agentrun/streamrunner/runner.go:177-179),
	// synthesising a trailer of its own only when the idle-stall watchdog fired
	// AND claude emitted no result (:250-253). So on every healthy run of that
	// path the trailer is claude's own result line, and absence is what that path
	// constructs.
	//
	// It CERTIFIES NOTHING — Reason stays empty, which is what keeps
	// trailClassifyRun's C2 (trail_run_outcome_test.go:553-564) green unamended —
	// and it says no more than what was read. Not that any process was alive:
	// there is no certified instant here for such a claim to be about, which is
	// why the run-level arm it reaches is a named void. Not anything about a
	// trailer that DOES carry a reason on that path — that sibling shape has an arm
	// of its own since #1433 and, since #1434, a value of its own,
	// trailGatePresentOwesNone, which it answers under a streamrunner reading; it
	// reaches trailGateUsable only under a ptyrunner or an indeterminate one. (It
	// was #1369's until that issue was closed NOT_PLANNED and re-filed as #1427,
	// which split into those two.) And not that this gate decides
	// against the path a LIVE run took: both shipped gathers fill RunnerPath with
	// trailRunnerUnread() (finding_run_gather_test.go:552, :789;
	// trail_run_rig_test.go:162), so over a live run the reading names no runner
	// and this value is unreachable. The gate is correct about absence when the
	// path is known, and the shipped gathers do not know it.
	trailGateAbsentOwesNone = "gate-absent-reason-owes-none"
	// trailGatePresentOwesNone: the trailer carries a terminal_reason ON the line,
	// and the observed runner path owes none. A READING rather than a caller's bug,
	// for the mirror of the sibling above's reason: streamrunner.Run tees claude's
	// stdout for the watchdog and passes the bytes through unchanged
	// (internal/agentrun/streamrunner/runner.go:177-179), so the line is genuinely
	// what the run produced. Nothing about it is malformed and no caller did
	// anything wrong.
	//
	// What it says is that the line is NOT that path's documented healthy shape —
	// absence is — and no more than that. It CERTIFIES NOTHING: Reason stays empty,
	// which is what keeps trailClassifyRun's C2 (trail_run_outcome_test.go:553-564)
	// green unamended and lets C4 (:578-586) force Admit empty.
	//
	// NEVER THAT PYRY WROTE IT, and the passthrough cite above is the REASON rather
	// than a decoration. Pyry's own synthesis on that path is unconditional WHEN IT
	// HAPPENS — writeIdleStallResult sets the field
	// (internal/agentrun/streamrunner/watchdog.go:280) on one carrying no omitempty
	// (:253), and Run reaches it only when the idle-stall watchdog fired AND claude
	// emitted no result (runner.go:250-253) — but that is a statement about what
	// pyry writes and never about what claude cannot. Because the passthrough puts
	// claude's own bytes on the same line, a value claiming authorship would let
	// claude's output name pyry as its author.
	//
	// Not that any process was alive, either: this is a statement about WHAT THE
	// TRAILER CARRIED, and nothing is certified here, so there is no
	// declared-finished instant for such a claim to be about — which is why the
	// run-level arm it reaches is a named void. And not that this gate decides
	// against the path a LIVE run took: both shipped gathers fill RunnerPath with
	// trailRunnerUnread() (finding_run_gather_test.go:552, :789;
	// trail_run_rig_test.go:162), so over a live run the reading names no runner and
	// this value is unreachable. The gate is correct about presence when the path is
	// known, and the shipped gathers do not know it.
	trailGatePresentOwesNone = "gate-present-reason-owes-none"
	// trailGateOutOfContract: the input is not a reading. Reporting any of its
	// sub-cases as absent would file a caller's bug under "pyry never finished
	// the turn". Counted off the arms rather than adjusted by one: a state
	// outside the three trailScan documents, a nil Trailer under a seen state, an
	// absent terminal_reason on a path that OWES one, an absent one under a
	// reading that names NO runner, and a present-and-blank one — FIVE, and every
	// one of them is a record whose terminal_reason is missing, blank or moot. This
	// value carries no case in which the key is on the line NAMING a reason. #1419
	// had split the empty terminal_reason into the key being ABSENT from the line
	// and the key being PRESENT AND BLANK — one decoded "" and two different
	// records — #1420 split the absent one three ways again by what the observed
	// runner path owes, #1417 took the owes-none absence out of this value entirely
	// because absence there is a reading, #1433 added the present-and-named case on
	// a path owing none, and #1434 took that one out again for the same reason:
	// presence there is a reading too. Same value, same uncertified reason for all
	// five — only the Detail says which.
	trailGateOutOfContract = "gate-out-of-contract"
)

// --- the predicate's value space ---------------------------------------------

// Whether pyry's reap-log attribution is admissible as proof, as a POSITIVE
// ALLOWLIST of seven. Exactly ONE is admissible; every way of failing to
// attribute has its own name, because a void reported as a negative inverts the
// asymmetry the claim rests on.
const (
	// trailAdmitProof: the reaper named the held group on exactly one line and
	// the run was not budget-fired, so the group was alive strictly after the
	// trailer was written. THE ONLY ADMISSIBLE VALUE.
	trailAdmitProof = "admit-proof"
	// trailAdmitVoidBudgetFired: the run was budget-fired. STRUCTURAL, which is
	// why it outranks every reap-side void below — see trailAdmitAttribution's
	// ordering argument.
	trailAdmitVoidBudgetFired = "admit-void-budget-fired"
	// trailAdmitVoidInstrument: the reap line could not be read. A broken
	// instrument is never a statement about pyry.
	trailAdmitVoidInstrument = "admit-void-instrument-failed"
	// trailAdmitVoidNoLine: no reap line at all. AMBIGUOUS by construction
	// (reap.go:64 guards the emit on len(reaped) > 0), so a void — never
	// evidence the group had exited.
	trailAdmitVoidNoLine = "admit-void-no-reap-line"
	// trailAdmitVoidGroupUnnamed: the reaper ran and did not name the group. A
	// GROUP NOT NAMED IS NEVER EVIDENCE IT HAD EXITED — the miss is consistent
	// with the group exiting AND with the reaper never reaching it.
	trailAdmitVoidGroupUnnamed = "admit-void-group-unnamed"
	// trailAdmitVoidNotOneReapLine: the group was named, but more than one reap
	// line was seen, so it is unestablished WHICH reap named it and the ordering
	// argument does not close. Not the same as no line at all.
	trailAdmitVoidNotOneReapLine = "admit-void-not-one-reap-line"
	// trailAdmitOutOfContract: the input is not a reading. A void by default
	// would let a caller's bug read as a measured void.
	trailAdmitOutOfContract = "admit-out-of-contract"
)

// --- the records -------------------------------------------------------------

// trailGateInput is what the gate decides over: trailScan's output, plus the
// runner path the run was observed to take. terminal_reason is pyry's invention
// and the two runner paths owe it differently, so the same trailer shape is a
// healthy run on one path and a broken record on the other; deciding which needs
// the path.
//
// # The reading is kept OFF trailScanResult
//
// That type is trailScan's output over bytes alone, and a runner path is not a
// property of the line. It is also pinned unreachable from the published record
// (finding_run_record_test.go:780-812), so widening it would put that pin up for
// renegotiation for no gain here.
//
// # RunnerPath holds the REDUCED answer and never the argv
//
// RunnerPath is tdnRunnerFromArgv's OUTPUT — one of its five constant answers —
// and never its input. That is the rule finRecordInputs.ClaudeCommand states at
// its own tier (finding_run_record_test.go:235-238): the argv is READ, reduced to
// one of tdnRunnerFromArgv's constant answers, and NEVER RETAINED anywhere in the
// record. Nothing on this type gives verbatim argv a place to land, and trailGate
// itself calls no argv reader at all — not tdnClaudeCommand, not reachProc.Command,
// not pinScan.Matches, and not reachRunnerPathFromArgv, which keys on
// --append-system-prompt-file and would label a correctly-wired stream run
// ptyrunner (teardown_liveness_probe_test.go:759-766).
//
// # Two arms read it
//
// #1373 carried the reading to the gate and left every arm as it was. #1420 is
// where a decision consults it: the ABSENCE arm calls trailReasonAgainstPath
// (trailer_terminal_reason_test.go:222) to say which of the three absence cases
// fired, which is where that function stopped having only its own tests for
// callers. #1433 added the second reader, on the PRESENCE side, calling the same
// reduction and diverting on one of its answers. Every other arm ignores the
// field today, which is an inference from today's row set: what
// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt proves is the row-scoped
// property that an undeclared row is invariant across the readings, and one
// companion sub-test per declaring row proves that row DOES vary, so the sweep is
// not silent about what it exempts.
type trailGateInput struct {
	Scan       trailScanResult
	RunnerPath string
}

// trailGateResult is what the gate produces. It is TRAP-FREE BY CONSTRUCTION: no
// *resultTrailer is reachable from it, directly or through an embedded field, so
// a consumer that branches on this value holds nothing to dereference.
//
// That is the property worth pinning, and it is the one that is true. The gate
// cannot make itself the pointer trap's LAST consumer: trailObservation embeds
// trailScanResult (result_trailer_observation_test.go:142), so anything holding
// an observation reaches .Trailer by field promotion, and shipped code already
// does exactly that (:588, :638). What the gate can guarantee is its own output.
//
// It carries neither trailScanResult.Line nor any quote of it. That string is
// verbatim model output and marked OPERATOR-REVIEW-BEFORE-PASTE; copying it
// would propagate that obligation onto a record whose whole value is that it can
// be published unreviewed. A future field added here must not be a copy of an
// input's captured bytes — TestTrailAdmissibilityRecordsCarryNoCapturedBytes is
// the enforcing test.
type trailGateResult struct {
	Value string `json:"value"`
	// Reason is the CERTIFIED terminal reason: a plain, non-empty string exactly
	// on the two arms that certify (usable and budget-fired), empty on the five
	// that do not — counted off the space above rather than adjusted by one, since
	// #1417 and #1434 each grew it. A certification that could certify "" would
	// reintroduce, one layer up, the exact defect the nil Trailer pointer was
	// chosen to prevent.
	Reason string `json:"terminal_reason,omitempty"`
	Detail string `json:"detail"`
	// RunnerPath is the reading the gate was HANDED, copied onto the result. The
	// ABSENCE arm consults it since #1420 and the PRESENCE arm since #1433; no
	// other arm does, and no Detail interpolates it anywhere. It is on the
	// result so that the reading which reached a PURE function is observable from
	// outside it, which is the only channel a composition test can assert against.
	//
	// It ANSWERS the rule above rather than outgrowing it. The value is one of
	// tdnRunnerFromArgv's five constant answers — source-authored prose reduced
	// from argv, READ and NEVER RETAINED, exactly as finRecordInputs.ClaudeCommand
	// states it (finding_run_record_test.go:235-238) — so it is not a copy of an
	// input's captured bytes, and TestTrailAdmissibilityRecordsCarryNoCapturedBytes
	// keeps enforcing that over the marshalled record.
	//
	// AN UNFILLED READING READS AS "" HERE, which no shipped producer emits:
	// tdnRunnerFromArgv returns a non-empty string on every branch, including for
	// "". What "" MEANS at a decision was deferred until an arm depended on it,
	// and #1420 is that arm, so the answer belongs here rather than in a pointer:
	// finRecordRunnerLabel("") returns "" (finding_run_record_test.go:267-272),
	// which matches neither runner label, so an unfilled reading ROUTES — to
	// trailReasonPathUnnamed, whose meaning IS "the reading names no runner".
	//
	// That retires the safety argument this block used to make. "An unfilled one
	// cannot misroute a decision because no arm reads the field" is no longer why
	// it is safe, because an arm reads it. It is safe because the arm that reads
	// it has a true answer for "" rather than a guess, and because the value is
	// still absent from the published record, which reads as "not recorded".
	RunnerPath string `json:"runner_path,omitempty"`
}

// trailAdmitResult is what the admissibility predicate produces, under the same
// discipline as trailGateResult: no pointer into either input, and no quote of
// tdnReapOutcome.Line, which is pyry's own stderr and carries the same
// operator-review obligation.
type trailAdmitResult struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

// trailDetail formats a Detail and caps it with #1230's existing helper.
//
// Deliberately NOT a call to tdnDetail: the trail* family stays out of the tdn*
// teardown classifier's reach, which is the point of a distinct prefix. The cap
// applies even though no Detail here quotes captured bytes — tdnReapOutcome's
// PGIDs list is unbounded, and the family's rule is that every retained
// operator-visible string is capped.
func trailDetail(format string, args ...any) string {
	return reachCapCommand(fmt.Sprintf(format, args...))
}

// --- the gate ----------------------------------------------------------------

// trailGate decides whether a trailer scan result can support a claim, and
// certifies the terminal reason when it can.
//
// Pure over its input: no exec, no clock, no filesystem. That is what lets every
// arm be driven with no live turn and no credentials. It takes no *testing.T and
// never fails a test — an instrument failure observed mid-turn is a datum to
// publish, not a reason to abort the turn, the same contract as trailScan,
// tdnClassifyReapLog, pinReadState and fifoLiveRead.
//
// # Nil-safety
//
// trailScanResult.Trailer is nil unless State == trailSeen, deliberately
// (result_trailer_observation_test.go:108-119): a consumer that dereferences it
// without checking State panics loudly, which was chosen over a value type that
// would hand back TerminalReason == "" and let an empty terminal reason pass as
// a real one. This function is that trap's first consumer, and it dereferences
// no nil pointer on ANY input — including one whose State claims trailSeen but
// whose Trailer is nil, which the contract check below answers before anything
// reads through the pointer.
//
// The seen arm DOES read TerminalReason, because the budget arm has to be keyed
// on that field. What is guaranteed is that a nil Trailer is answered first and
// never reaches it.
//
// # The budget arm keys on terminal_reason alone
//
// A max_turns run also renders subtype "error_max_turns" and is_error true
// (emitter.go:428-437). Consulting all three would introduce a fourth question —
// what to do when they disagree — for no gain. terminal_reason is the field the
// teardown path is documented against: one field, one decision.
//
// # The runner path is CARRIED to all eleven return sites and READ at four
//
// in.RunnerPath reaches every return site. SEVEN of the eleven are decided
// without consulting it at all, and that seven did NOT move when #1433 added a
// site: the site it added is a fourth that IS decided by the path. The four are
// the three the ABSENCE arm answers with, which is #1420's whole change, and the
// PRESENCE arm #1433 added. The DETAIL is decided by the reading at exactly those
// four.
//
// The VALUE is decided by the reading for TWO INPUT SHAPES, across three of those
// four sites — stated as shapes rather than as a bare numeral, because a numeral
// here has gone stale twice already. On the ABSENCE shape, #1417 made one of the
// three sites answer trailGateAbsentOwesNone where its two siblings answer
// trailGateOutOfContract. On the PRESENT-AND-NAMED shape, #1433 made the reading
// pick between the presence site and the usable one, and #1434 gave that site a
// value of its own, trailGatePresentOwesNone, so the reading there now picks
// between two DIFFERENT values rather than between two sites answering one. And
// THE SITE COUNT ITSELF MOVED: it was ten from #1419 through #1417, and #1433 is
// the ticket that changed it — #1434 changed what a shipped site answers and added
// none.
//
// Not one Detail interpolates the reading even there: the absence sites embed
// trailReasonAgainstPath's answer, the presence site cites that function's case
// constant rather than embedding its Detail, and every arm of that function is
// fixed prose over its own file's constants and file cites
// (trailer_terminal_reason_test.go:208-221). Every Detail here stays fixed prose
// over this file's own constants, the scan's own state, and that function's
// answer or the name of it.
//
// Where that function holds its guarantee by review of its source, this one holds
// it by TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt, which drives every
// fixture row under each of tdnRunnerFromArgv's five distinct answers and requires
// a BYTE-IDENTICAL Detail across all five on every row that does not declare
// pathVaries — and the variance on the rows that DO declare it is then proven
// positively, by one companion sub-test per declaring arm.
//
// The reading is echoed onto the result, so the arriving value stays observable
// from outside a pure function.
func trailGate(in trailGateInput) trailGateResult {
	// Contract, first: a State outside the three trailScan documents is not a
	// reading. Catches the zero trailScanResult, whose State is "".
	if in.Scan.State != trailSeen && in.Scan.State != trailAbsent && in.Scan.State != trailAborted {
		return trailGateResult{
			Value: trailGateOutOfContract,
			Detail: trailDetail("state %q is not one of the three trailScan documents (%s / %s / "+
				"%s), so this record is not a reading. Reported out of contract rather than as an "+
				"absence, which would file a caller's bug under \"pyry never finished the turn\"",
				in.Scan.State, trailSeen, trailAbsent, trailAborted),
			RunnerPath: in.RunnerPath,
		}
	}

	// The two states that carry no trailer. Neither touches Trailer.
	switch in.Scan.State {
	case trailAbsent:
		return trailGateResult{
			Value: trailGateNoTrailer,
			Detail: trailDetail("the scan read every line cleanly and none was a trailer, so no "+
				"terminal reason exists to certify and no claim may rest on this run's trailer. "+
				"Distinct from %s: this is a statement about the bytes, that one is the "+
				"instrument reporting it could not read them", trailGateScanAborted),
			RunnerPath: in.RunnerPath,
		}
	case trailAborted:
		return trailGateResult{
			Value: trailGateScanAborted,
			Detail: trailDetail("the trailer scan aborted, so the bytes were unreadable. This is "+
				"the instrument's own breakage and never an answer about pyry, which is why it is "+
				"kept apart from %s — a line past bufio.Scanner's 64 KiB default and a genuine "+
				"absence are otherwise indistinguishable", trailGateNoTrailer),
			RunnerPath: in.RunnerPath,
		}
	}

	// trailSeen from here. Contract again, before anything reads through the
	// pointer: trailScan sets Trailer on its trailSeen return and on no other
	// (result_trailer_observation_test.go:180-233), so a nil here is a
	// hand-built record, not something the producer can emit.
	if in.Scan.Trailer == nil {
		return trailGateResult{
			Value: trailGateOutOfContract,
			Detail: trailDetail("state %s carries a nil trailer, a record trailScan cannot emit. "+
				"Reported out of contract because both alternatives are worse: calling it usable "+
				"would dereference nil, and calling it absent would contradict the state the "+
				"record itself claims", trailSeen),
			RunnerPath: in.RunnerPath,
		}
	}

	reason := in.Scan.Trailer.TerminalReason
	if reason == "" {
		// The decode collapses two shapes here and the key names separate them
		// (trailer_key_names_test.go:14-24). PRESENCE IS MEMBERSHIP, and the two
		// readings it is not are both live mistakes rather than invented ones:
		//
		//   - NEVER decodedReason != "". That is the collapse #1357's reading was
		//     landed to prevent (trailer_terminal_reason_test.go:203-206), and
		//     inside this block it is always false — so it would route every input
		//     to the absence arm SILENTLY.
		//   - NEVER len(KeyNames) > 0. A scan-produced absence carries the six
		//     names the line did have, merely missing this one, so cardinality
		//     reads it as PRESENT.
		//
		// Not every arm below answers trailGateOutOfContract any more. #1417 took
		// the owes-none absence out of it — that shape is a reading rather than a
		// caller's bug — so both closed sets grew and every consumer switching over
		// them gained an arm, and #1434 did the same for the PRESENCE arm past this
		// branch. THREE of this branch's four sites still answer it, counted off the
		// arms below: they certify nothing and say WHICH SHAPE ARRIVED. No Detail
		// here interpolates a key name — see the no-echo argument at
		// trailGateResult and the check at
		// TestTrailAdmissibilityRecordsCarryNoCapturedBytes. The present-and-empty
		// arm is #1419's and stays path-invariant; the absence branch is where
		// #1420 reads in.RunnerPath, and since #1433 the non-empty path below reads
		// it too — those TWO branches in this function, and no others.
		if !slices.Contains(in.Scan.KeyNames, trailReasonKeyName) {
			// WHICH absence comes from the shipped reduction, CALLED rather than
			// re-switched: trailReasonAgainstPath (trailer_terminal_reason_test.go:222)
			// already closes over the six meanings a terminal_reason has against a
			// path, and its three ABSENCE answers are exactly the three cases here.
			// Embedding its Detail is safe because every arm of it is fixed prose
			// over its own file's constants and file cites — never the reading,
			// never a key name, never the decoded scalar — which that function's
			// doc states structurally at :208-221.
			//
			// PRESENCE IS STILL THE GATE'S OWN READ, from the membership test
			// above, exactly as #1419 landed it. It is NOT taken from the answer
			// below: that function computes presence at :223 and then falls through
			// to trailReasonPathUnnamed on any label naming neither runner WITHOUT
			// CONSULTING IT (:272-278), so on an indeterminate reading an absent key
			// and a present-and-empty one reach one value — and an indeterminate
			// reading is precisely what both shipped gathers supply. Sourcing
			// presence there could not tell those two apart on the only reading
			// live code produces.
			//
			// Three return sites, and NO default guard. presence is false by the
			// enclosing branch, so only the three absence values are reachable and
			// trailReasonPathUnnamed is the FALL-THROUGH rather than a catch-all: a
			// fourth site for "some value outside the three" would be a return site
			// no fixture row can reach, and clause B of
			// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt would have to
			// weaken from "the nine rows reach all of them" to "reach most of".
			//
			// THE COUNT IS ELEVEN SINCE #1433 AND THE ARGUMENT IS UNCHANGED, because
			// it was never an argument about the total. That ticket added a return
			// site below and clause B stayed TOTAL, since trailGateCases()' usable
			// row reaches the new site under readings[1]. The rule is "every site a
			// fixture row reaches", not "ten sites": a site a row reaches pays for
			// itself, and a fourth site HERE still would not — which is why this
			// switch still has no default guard.
			//
			// One site interpolating against.Detail would compile and pass, and is
			// refused: the sweep's totality argument rests on this arm being three
			// sites that each echo RunnerPath, and each case needs its own byte
			// budget. trailDetail caps at reachMaxCommandBytes (512) and
			// reachCapCommand TRUNCATES AND MARKS rather than failing
			// (background_reach_probe_test.go:945-950), so prose that outgrew the cap
			// would be cut PAST its embedded value marker and publish a severed
			// sentence that still satisfies a marker assertion. Measured at #1417 by
			// driving these three sites: the composed Details are 461 / 464 / 468 B
			// against the 512, of which the embedded one is 264 / 269 / 286 — so each
			// case's own prose is under 200 B and is REWRITTEN to fit rather than
			// appended to. The owes-none case carries the tightest ceiling of the
			// three, 470 B, because its leak row asserts the 42 B trailNeedle would
			// still have fitted (TestTrailAdmissibilityRecordsCarryNoCapturedBytes).
			// The embedded Detail is passed as a fmt ARGUMENT — never concatenated
			// into the format string, where a % in it would be interpreted.
			//
			// Each case says what its absence means against the path. #1417 is the
			// decision the three cases were left open for, and it flipped exactly
			// ONE: absence on a path that owes none is that path's documented healthy
			// shape, so it is a reading and answers trailGateAbsentOwesNone. The
			// other two still answer out of contract — absence where a reason is
			// owed is a departure from what that path constructs, and absence under a
			// reading naming no runner is a shape nothing here can judge. The sibling
			// shape, a terminal_reason that IS on the line from a path owing none,
			// has an arm of its own below since #1433 and a value of its own,
			// trailGatePresentOwesNone, since #1434; these arms stay silent about it
			// rather than half-answering it, which is still the right shape now that
			// it is answered elsewhere. It was #1369's until that issue was closed
			// NOT_PLANNED and re-filed as #1427, which split into those two.
			against := trailReasonAgainstPath(in.RunnerPath, in.Scan.KeyNames, reason)
			switch against.Value {
			case trailReasonAbsentOwesOne:
				return trailGateResult{
					Value: trailGateOutOfContract,
					Detail: trailDetail("state %s carries a trailer with NO terminal_reason key on "+
						"the line, so nothing is certified. WHICH absence: the observed path owes "+
						"one, and absence there departs from what it constructs. %s",
						trailSeen, against.Detail),
					RunnerPath: in.RunnerPath,
				}
			case trailReasonAbsentOwesNone:
				return trailGateResult{
					Value: trailGateAbsentOwesNone,
					Detail: trailDetail("state %s carries a trailer with NO terminal_reason key on "+
						"the line, so nothing is certified. The observed path owes none, so this is "+
						"its documented healthy shape, never a caller's bug. %s",
						trailSeen, against.Detail),
					RunnerPath: in.RunnerPath,
				}
			}
			return trailGateResult{
				Value: trailGateOutOfContract,
				Detail: trailDetail("state %s carries a trailer with NO terminal_reason key on the "+
					"line, so nothing is certified. WHICH absence: the reading names no runner, so "+
					"neither path statement applies. %s", trailSeen, against.Detail),
				RunnerPath: in.RunnerPath,
			}
		}
		return trailGateResult{
			Value: trailGateOutOfContract,
			Detail: trailDetail("state %s carries a trailer whose terminal_reason is empty — the "+
				"key IS on the line and its value is blank. Certifying it would reintroduce, one "+
				"layer up, the defect the nil Trailer pointer was chosen to prevent. Today's pyry "+
				"cannot render a blank one — emitter.go:383-391 is a chokepoint substituting the "+
				"recorded detail or \"unclassified\" before marshalling — so for this shape, and "+
				"not for an absent key, NO LIVE REPRO EXISTS", trailSeen),
			RunnerPath: in.RunnerPath,
		}
	}

	if reason == trailBudgetTerminalReason {
		return trailGateResult{
			Value:  trailGateBudgetFired,
			Reason: reason,
			Detail: trailDetail("terminal_reason is %q, so the run was budget-fired and the "+
				"Terminate hook reaped INSIDE the hook (runner.go:492-503), BEFORE the trailer "+
				"was written. A reap-log attribution on this path is void, not negative. The "+
				"reason is certified anyway, because the predicate needs it to name that void",
				reason),
			RunnerPath: in.RunnerPath,
		}
	}

	// The PRESENCE side of the reading #1420 wired in at the absence branch:
	// terminal_reason IS on the line, and the observed path owes none. Same
	// reduction, CALLED rather than re-switched — trailReasonAgainstPath
	// (trailer_terminal_reason_test.go:222) owns the six meanings a terminal_reason
	// has against a path, and this arm consults exactly one of its answers. An `if`
	// and not a `switch` because exactly one answer diverts: the other two
	// reachable here — trailReasonNamedOwesOne under a ptyrunner reading and
	// trailReasonPathUnnamed under a reading naming no runner — fall through to the
	// usable return below, which stays the FALL-THROUGH rather than becoming a
	// default arm. trailReasonBlankOwesOne cannot be reached from here at all,
	// since reason != "" by the enclosing flow, so no site is spent on it.
	//
	// It answers trailGatePresentOwesNone and CERTIFIES NOTHING. #1434 took this
	// shape out of trailGateOutOfContract in exactly the move #1417 made for the
	// owes-none absence, and for the same reason: the record IS a reading, so
	// answering "the input is not a reading" filed a measurement as a caller's bug.
	// Certifying nothing is what keeps trailClassifyRun's C2
	// (trail_run_outcome_test.go:553-564) green unamended and lets C4 (:578-586)
	// force Admit empty, so the run-level answer is decided at step 1 alone — by
	// trailOutcomeVoidReasonNotOwedByPath, the arm added to that switch IN THE SAME
	// COMMIT, because a gate value registered in trailIsGateValue with no arm there
	// does not fall to step 2 but to steps 3-8, awarding a scan-side answer about
	// pyry from a record the gate says certifies nothing.
	//
	// What the arm may say is bounded by what was read, and that bound is written
	// onto trailGatePresentOwesNone's own doc rather than restated here: the line is
	// not that path's documented healthy shape, no verdict rests on it, NEVER that
	// pyry wrote it — cited to the passthrough, which is the reason and not a
	// decoration — and it is about WHAT THE TRAILER CARRIED, never whether a process
	// was alive. The Detail below carries the three phrases that state it, and
	// TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone's P1 row asserts all
	// three, so the bound is enforced rather than merely described.
	//
	// # Two orderings, both decided here rather than left to return order
	//
	// THE BUDGET ARM WINS, which is why this site sits after it. A max_turns trailer
	// from a path that owes none has two candidate arms — the budget arm keys on
	// terminal_reason alone, deliberately, for the reason this function's own
	// "# The budget arm keys on terminal_reason alone" heading gives — and the
	// reduction answers trailReasonPresentOwesNone for it. The budget void is
	// STRUCTURAL and outranks every reap-side void (trailAdmitVoidBudgetFired's own
	// comment, and trailAdmitAttribution's ordering argument), so it keeps its
	// trailer. The other order is not merely unmotivated, it is more expensive:
	// measured both ways, placing this site FIRST makes trailGateCases()' max_turns
	// row vary by the reading too, so a THIRD row would have to declare pathVaries
	// and the sweep would stop comparing what the budget arm certifies.
	//
	// THE PRESENT-AND-BLANK ARM KEEPS WINNING, and that divergence is deliberate
	// rather than an accident of return order. The reason == "" branch above returns
	// before this site is reached, so a blank terminal_reason from a path owing none
	// is answered PATH-INVARIANTLY as out of contract. The reduction disagrees:
	// trailReasonPresentOwesNone absorbs both — "Empty or named, both land here"
	// (trailer_terminal_reason_test.go:105-108) — and its Detail is byte-identical
	// for the named and the blank input. #1419's arm is kept winning because it
	// publishes what the one absorbing value cannot: the NO LIVE REPRO EXISTS claim,
	// which is TRUE of a blank key and FALSE of an absent one.
	//
	// # The constant is CITED, and the reduction's Detail is NOT embedded
	//
	// The three absence sites embed against.Detail because their three cases are
	// otherwise indistinguishable in prose. Here that neither fits nor is needed.
	// trailReasonPresentOwesNone's Detail is 395 B, which would leave 75 B of own
	// prose under the 470 B ceiling the leak row imposes — the 512 B cap less the
	// 42 B trailNeedle it asserts would still have fitted. And the marker assertions
	// key on the CONSTANT: trailGateAbsenceCaseMarkers() returns the constants
	// themselves and every assertion is a strings.Contains against one, so citing
	// the 32 B constant preserves all of them, needs no trim to the shipped
	// reduction, and creates no severed-sentence hazard. Measured at 427 B, 43 B
	// spare, and TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone asserts that
	// headroom ON THE OUTPUT so a reword that overflows is red here rather than
	// shipping a Detail cut past its own marker.
	//
	// The decoded reason is deliberately NOT interpolated, unlike the two arms that
	// certify it. This one certifies nothing, so putting the scalar into its Detail
	// would put a value from the trailer into a record that certifies none, and
	// would make the byte budget a function of untrusted input.
	//
	// PRESENCE IS THE REDUCTION'S OWN KEY-NAME READ (trailer_terminal_reason_test.go:223)
	// and never decodedReason != "", which inside this block is constantly true. A
	// hand-built record whose decoded reason is non-empty while KeyNames lacks the
	// key therefore reduces to an ABSENCE answer and reaches the usable return
	// below. That record is one trailScan cannot emit — it derives both from the
	// same line — and it is outside this arm's stated shape, so giving it a site
	// would spend a return site no fixture row reaches. Recorded, not defended: the
	// same argument the absence switch makes about its missing default.
	against := trailReasonAgainstPath(in.RunnerPath, in.Scan.KeyNames, reason)
	if against.Value == trailReasonPresentOwesNone {
		return trailGateResult{
			Value: trailGatePresentOwesNone,
			Detail: trailDetail("%s: terminal_reason is on the line and the path owes none, so the "+
				"line is not that path's healthy shape and nothing is certified. NEVER that pyry "+
				"wrote it — the path passes claude's bytes through unchanged "+
				"(streamrunner/runner.go:177-179), so claude produces the same reading. About what "+
				"the trailer CARRIED, never whether a process was alive. A READING of the trailer "+
				"rather than a defect in it", trailReasonPresentOwesNone),
			RunnerPath: in.RunnerPath,
		}
	}

	return trailGateResult{
		Value:  trailGateUsable,
		Reason: reason,
		Detail: trailDetail("the trailer is usable and carries terminal_reason %q, which is not "+
			"%q — so emitter.Close() wrote the trailer before the reap defer on this path "+
			"(runner.go:479-485, :398) and a reap-log attribution can be proof", reason,
			trailBudgetTerminalReason),
		RunnerPath: in.RunnerPath,
	}
}

// --- the admissibility predicate ---------------------------------------------

// trailAdmitAttribution decides whether pyry's reap-log attribution is
// admissible as proof that the held process group was alive when the trailer was
// written.
//
// Pure over its two inputs, same contract as trailGate: no exec, no clock, no
// filesystem, no *testing.T, never fails a test.
//
// certified is a terminal reason trailGate has CERTIFIED — a plain non-empty
// string from a trailGateUsable or trailGateBudgetFired result. It takes the
// string rather than a trailGateResult deliberately: taking the result would
// force this function to answer for every gate value that certifies nothing —
// trailGateNoTrailer, trailGateScanAborted, trailGateAbsentOwesNone,
// trailGatePresentOwesNone and trailGateOutOfContract, FIVE since #1434 added the
// fourth of them — and grow an eighth outcome, which is the exact collapse the
// ticket refuses.
// The composition is therefore a test-level obligation, and
// TestTrailGateThenAdmit is where it is discharged.
//
// # Two orderings, each carrying an argument
//
// The BUDGET VOID BEATS EVERY REAP-SIDE VOID. It is STRUCTURAL: on a
// budget-fired run the reap ran inside the Terminate hook BEFORE the trailer was
// written, so no reap line on that path could ever prove aliveness-at-trailer —
// the reap record's contents are irrelevant, including whether they parsed. The
// reap-side voids are INCIDENTAL: had the instrument worked, or had a second
// line not appeared, the answer might have been proof. Reporting
// trailAdmitVoidInstrument for a budget-fired run would imply that fixing the
// instrument would yield proof. It would not.
//
// The CONTRACT BLOCK BEATS THE BUDGET VOID. A caller handing this function a
// record its producer cannot emit has a bug that must surface regardless of
// which path the run took — the same rule as trailGate's first check, applied to
// the same class of defect.
func trailAdmitAttribution(reap tdnReapOutcome, certified string) trailAdmitResult {
	// Contract, first. The three checks below are exactly what
	// tdnClassifyReapLog (teardown_liveness_test.go:144-219) can emit: it
	// reaches tdnReapNoLine only with LineCount == 0, and reaches
	// tdnReapHeldPGIDKilled / tdnReapHeldPGIDAbsent only after incrementing
	// LineCount for an anchored line. tdnReapInstrumentFailed carries no such
	// pairing — its held-pgid guard (:147) returns before any line is counted
	// while its parse-failure arm (:171) returns after — so no LineCount check
	// applies to it.
	//
	// Only the first check is mandated by the AC; the other two are that rule
	// applied symmetrically, and they earn their four lines for the same reason
	// the gate's nil-Trailer arm does: these tests are fixture-driven, and a
	// hand-built tdnReapOutcome with a plausible Verdict and an unfilled
	// LineCount is exactly what a developer types. Without them such a fixture
	// reports trailAdmitVoidNoLine for a record that saw lines, or
	// trailAdmitVoidGroupUnnamed for one that saw none — a misreport dressed as
	// a reading.
	if !tdnIsReapVerdict(reap.Verdict) {
		return trailAdmitResult{
			Value: trailAdmitOutOfContract,
			Detail: trailDetail("verdict %q is not one of the four tdnClassifyReapLog documents "+
				"(%s / %s / %s / %s), so this record is not a reading. A void by default here "+
				"would let a caller's bug read as a measured void", reap.Verdict,
				tdnReapHeldPGIDKilled, tdnReapHeldPGIDAbsent, tdnReapNoLine,
				tdnReapInstrumentFailed),
		}
	}
	if reap.Verdict == tdnReapNoLine && reap.LineCount != 0 {
		return trailAdmitResult{
			Value: trailAdmitOutOfContract,
			Detail: trailDetail("verdict %s reports %d reap line(s), a pair tdnClassifyReapLog "+
				"cannot emit: it reaches that verdict only when no anchored line was counted. "+
				"Reported out of contract rather than as a void that would describe a line count "+
				"the record cannot have", reap.Verdict, reap.LineCount),
		}
	}
	if (reap.Verdict == tdnReapHeldPGIDKilled || reap.Verdict == tdnReapHeldPGIDAbsent) &&
		reap.LineCount < 1 {
		return trailAdmitResult{
			Value: trailAdmitOutOfContract,
			Detail: trailDetail("verdict %s reports %d reap line(s), a pair tdnClassifyReapLog "+
				"cannot emit: it reaches that verdict only after incrementing LineCount for an "+
				"anchored line. Reported out of contract rather than as a void describing lines "+
				"it never saw", reap.Verdict, reap.LineCount),
		}
	}
	// The SECOND parameter, contracted at #1271's request. #1270's review found
	// this file's own doctrine — "each function opens with a contract block
	// rejecting what its producer cannot emit" — checked per-function rather than
	// per-parameter: reap got three checks and certified got none, so certified
	// == "" reached trailAdmitProof. That is a FALSE PROOF from a run the gate
	// never certified, the same failure direction as the nil-Trailer trap and the
	// empty-reason gate arm this file exists to close. trailGate certifies a
	// non-empty reason on exactly two values, so "" is a record its producer
	// cannot emit. No existing row changes value: every one passes a non-empty
	// reason.
	if certified == "" {
		return trailAdmitResult{
			Value: trailAdmitOutOfContract,
			Detail: trailDetail("no terminal reason was certified, a value trailGate does not "+
				"emit: it fills Reason on exactly %s and %s. Without a certified reason the "+
				"budget arm below cannot fire, so verdict %s would reach %s — a proof about a "+
				"run whose trailer nothing approved", trailGateUsable, trailGateBudgetFired,
				reap.Verdict, trailAdmitProof),
		}
	}

	// The structural void, ahead of every incidental one.
	if certified == trailBudgetTerminalReason {
		return trailAdmitResult{
			Value: trailAdmitVoidBudgetFired,
			Detail: trailDetail("the certified terminal reason is %q, so the reap ran inside the "+
				"Terminate hook (runner.go:492-503) BEFORE the trailer was written. No reap line "+
				"on that path could prove aliveness-at-trailer, whatever the record's verdict %s "+
				"says. This void is STRUCTURAL and outranks the reap-side ones, which are "+
				"incidental: reporting one of those here would imply that fixing the instrument "+
				"would yield proof", certified, reap.Verdict),
		}
	}

	switch reap.Verdict {
	case tdnReapInstrumentFailed:
		return trailAdmitResult{
			Value: trailAdmitVoidInstrument,
			Detail: trailDetail("the reap line could not be read, so this is the instrument's own "+
				"breakage and never a statement about pyry. Collapsing it into %s would "+
				"manufacture a leak finding out of a broken reader", trailAdmitVoidGroupUnnamed),
		}
	case tdnReapNoLine:
		return trailAdmitResult{
			Value: trailAdmitVoidNoLine,
			Detail: trailDetail("no anchored reap line appears at all. AMBIGUOUS by construction "+
				"— reap.go:64 guards the emit on len(reaped) > 0, so silence means the reaper ran "+
				"and reaped nothing OR that it never fired — which makes this a void and NEVER "+
				"evidence that group %d had exited", reap.HeldPGID),
		}
	case tdnReapHeldPGIDAbsent:
		return trailAdmitResult{
			Value: trailAdmitVoidGroupUnnamed,
			Detail: trailDetail("the reaper emitted %d line(s) and group %d was not among the %d "+
				"it named (%v). A GROUP NOT NAMED IS NEVER EVIDENCE IT HAD EXITED: the miss is "+
				"consistent with the group exiting and with the reaper never reaching it. A hit "+
				"proves aliveness; a miss proves nothing", reap.LineCount, reap.HeldPGID,
				len(reap.PGIDs), reap.PGIDs),
		}
	}

	// tdnReapHeldPGIDKilled, with LineCount >= 1 guaranteed by the contract
	// block above.
	if reap.LineCount == 1 {
		return trailAdmitResult{
			Value: trailAdmitProof,
			Detail: trailDetail("the reaper named group %d on exactly one anchored line (%v) and "+
				"the certified terminal reason %q is not %q. emitter.Close() wrote the trailer "+
				"(runner.go:479-485) before the reap defer (:398) SIGKILLed the group, so the "+
				"group was alive strictly AFTER the trailer was written — and therefore alive "+
				"when it was written", reap.HeldPGID, reap.PGIDs, certified,
				trailBudgetTerminalReason),
		}
	}
	return trailAdmitResult{
		Value: trailAdmitVoidNotOneReapLine,
		Detail: trailDetail("the reaper named group %d, but across %d anchored lines (%v). With "+
			"more than one line it is unestablished WHICH reap named the group, so the ordering "+
			"argument that makes a hit a proof does not close. Distinct from %s: lines were "+
			"seen, and the group was on one of them", reap.HeldPGID, reap.LineCount, reap.PGIDs,
			trailAdmitVoidNoLine),
	}
}

// --- membership helpers ------------------------------------------------------

// trailIsGateValue reports whether v is one of the recorded gate values. It
// mirrors tdnIsReapVerdict (teardown_liveness_test.go:1176) and exists for the
// same reason: a value a reader of the published record cannot look up is a
// verdict they cannot interpret.
func trailIsGateValue(v string) bool {
	switch v {
	case trailGateUsable, trailGateNoTrailer, trailGateScanAborted,
		trailGateBudgetFired, trailGateAbsentOwesNone, trailGatePresentOwesNone,
		trailGateOutOfContract:
		return true
	}
	return false
}

// trailIsAdmitValue reports whether v is one of the recorded admissibility
// values.
func trailIsAdmitValue(v string) bool {
	switch v {
	case trailAdmitProof, trailAdmitVoidBudgetFired, trailAdmitVoidInstrument,
		trailAdmitVoidNoLine, trailAdmitVoidGroupUnnamed, trailAdmitVoidNotOneReapLine,
		trailAdmitOutOfContract:
		return true
	}
	return false
}

// --- fixtures ----------------------------------------------------------------

// trailGateCase is one gate input and the value it must reach. The cases are
// shared between TestTrailGate, which asserts the mapping, and
// TestTrailGateThenAdmit, which sweeps the same fixtures through the composition
// — so the composition is driven over the gate's real value space rather than
// over a second, hand-kept list that could drift out of agreement with it.
type trailGateCase struct {
	name string
	// in carries the runner path PER ROW rather than the sweep supplying one
	// beside the call. That is what leaves TestTrailGate's, TestTrailGateThenAdmit's
	// and TestTrailRunComposesWithGateCases' `trailGate(tc.in)` textually untouched
	// by #1373, and it is the shape #1420's absence-case decision needs, since that
	// decision distinguishes inputs BY path.
	in   trailGateInput
	want string
	// reason is the terminal reason the gate must certify: non-empty exactly on
	// trailGateUsable and trailGateBudgetFired.
	reason string
	// pathVaries declares that this row's own arm reads the runner path, so its
	// VALUE, its CERTIFIED REASON and its DETAIL may all differ across the
	// readings. The certified reason joined that list at #1433, whose arm is the
	// first in the tree whose CERTIFICATION moves with the reading: every
	// path-varying arm before it certified nothing on every reading. It is what
	// scopes TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt's comparisons,
	// and it is DECLARED rather than detected: re-deriving the gate's branch condition
	// inside the sweep would restate the thing under test, and detecting it from
	// the Details differing would exempt precisely the rows that fail. A later
	// absence-shaped row that forgets to declare it goes red, which is the
	// direction that matters.
	pathVaries bool
}

// trailReapLine renders one anchored reap line in reap.go:65's slog shape, for
// the rows that build their tdnReapOutcome through the real tdnClassifyReapLog
// rather than by hand.
func trailReapLine(count int, pgids string) string {
	return fmt.Sprintf(`time=2026-08-03T09:00:00.000Z level=INFO msg=%q count=%d pgids=%s`,
		tdnReapMessage, count, pgids)
}

// trailRunnerUnread is the honest reading for "the runner was not read from the
// process table": tdnRunnerFromArgv's own answer to an empty command, obtained by
// CALLING the shipped reader rather than by re-typing its prose as a literal.
//
// An empty argv is admissible here and is never a staging failure.
// tdnClaudeCommand returns "" when zero OR SEVERAL rows carry the claude needle
// (teardown_liveness_probe_test.go:557-575), so emptiness is ambiguity about
// which row was claude's — never a claim that the run took the other path, which
// is exactly what tdnRunnerFromArgv answers with its own indeterminate string.
//
// Both gathers and all nine fixture rows go through this one function, so the
// two sides of C2's whole-struct equality (finding_run_gather_test.go:778) cannot
// drift apart.
//
// The gathers' use of it is CONSTANT by construction and FORBIDDEN to close:
// neither gather's needle set carries tdnClaudeNeedle, and
// finding_exit_path_probe_test.go:264-272 forbids adding it to the finding
// gather's scan. So a live run reads no runner and the gate's absence arm
// reaches only its path-unnamed case, while #1420 reads it at the gate with no
// live run needed. Full reason: trail_ptyrunner_composition_test.go:19-26.
//
// A function rather than a package-level var, matching trailRigHeldPGID()'s shape
// in this family (trail_run_rig_test.go:119).
func trailRunnerUnread() string { return tdnRunnerFromArgv("") }

// trailGateAbsentReasonScan is the ABSENCE shape the gate's #1419 arm decides:
// trailScan over a line carrying "type":"result" and no terminal_reason key at
// all — claude's own result line, the healthy trailer on the headless
// PYRY_USE_STREAMJSON=1 path.
//
// # Going through trailScan is a REQUIREMENT, not a preference
//
// The scan yields KeyNames of the SIX names the line did carry, merely missing
// terminal_reason. A hand-built trailScanResult with KeyNames nil is the natural
// copy-paste from the sibling below and looks equivalent, and it is not: an
// implementation reading presence as `len(KeyNames) > 0` reads nil as absent
// too, answers such a row CORRECTLY, and is left red nowhere. That mutation is
// the second of the two #1419 enumerates, and this fixture is the only reason it
// can fire at all.
//
// A function rather than a package-level var, for trailExpectedKeyNames()' own
// reason (trailer_key_names_test.go:115-117): the value holds a []string and a
// *resultTrailer, go test -race runs this package's tests in parallel, and a
// shared backing array would let one row's mutation reach another's.
func trailGateAbsentReasonScan() trailScanResult {
	return trailScan([]byte(trailKeyNamesNoTerminalReason() + "\n"))
}

// trailGateEmptyReasonScan is the PRESENT-AND-EMPTY shape: the same line with
// "terminal_reason":"" on it. It differs from the fixture above in exactly one
// key (trailer_key_names_test.go:158-161), and the two decode to an identical
// "" — which is the premise TestTrailKeyNamesSeparatesAbsenceFromZeroValue
// already asserts and this file inherits rather than re-derives.
//
// It replaces the hand-built trailScanResult{State: trailSeen, Trailer:
// &resultTrailer{Type: "result"}} this row used to carry. Under a key-name
// reading that record was ABSENT, not present-and-empty, so left unrepaired the
// present-and-empty arm would be unreachable from this suite and both of the
// sub-test's Detail assertions would pass against the absence arm instead.
func trailGateEmptyReasonScan() trailScanResult {
	return trailScan([]byte(trailKeyNamesEmptyTerminalReason() + "\n"))
}

// trailGateUsableScan is the PRESENT-AND-NAMED shape: one ordinary trailer whose
// terminal_reason is on the line and names a reason other than max_turns. Under a
// ptyrunner or an indeterminate reading it reaches the usable arm; under a
// streamrunner one it reaches the presence arm this ticket added, which is the
// whole of that arm's divergence.
//
// It exists so the usable row of trailGateCases(),
// TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone and the presence companion
// in TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt drive ONE fixture, and
// so that companion drives a helper rather than indexing the row slice — the same
// discipline the absence companion already states.
//
// A function rather than a package-level var, for trailGateAbsentReasonScan()'s
// own reason: the value holds a *resultTrailer and a []string, go test -race runs
// this package's tests in parallel, and a shared backing array would let one row's
// mutation reach another's.
func trailGateUsableScan() trailScanResult {
	return trailScan([]byte(trailFixtureTrailer + "\n"))
}

// trailGateCases returns every gate input under test. The six rows that can be
// produced by the real scan go through trailScan rather than a hand-built
// record, so the reachable arms stay pinned to what the shipped producer
// actually emits; the three that trailScan CANNOT emit are hand-built, because
// that is precisely what the contract checks exist for.
//
// Every row carries the same runner path, and since #1420 that is a CHOICE
// rather than a consequence — the gate's absence arm has read the path since
// then, and its presence arm since #1433. A row varying it would land inside
// TestTrailGate, TestTrailGateThenAdmit and TestTrailRunComposesWithGateCases at
// once — three of this slice's four consumers, and the three that do NOT vary the
// path themselves — so holding the path fixed is what keeps those three
// comparable with one another. The per-case proof therefore lives in a driver of
// its own PER ARM (TestTrailGateNamesWhichAbsenceCaseFired and
// TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone), and the sweep that
// VARIES the path is
// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt, which drives these same
// nine rows under all five readings.
func trailGateCases() []trailGateCase {
	return []trailGateCase{
		{
			// Declares pathVaries since this ticket: its arm is the one the presence
			// arm diverts from, so under a streamrunner reading this same input
			// reaches trailGateOutOfContract instead. It is the FIRST row in the tree
			// whose CERTIFIED REASON moves with the reading — "completed" under the
			// baseline reading, nothing under the streamrunner one — which is what
			// widened the sweep's exemption beyond the value and the Detail.
			name:       "an ordinary trailer is usable and certifies its reason",
			in:         trailGateInput{Scan: trailGateUsableScan(), RunnerPath: trailRunnerUnread()},
			want:       trailGateUsable,
			reason:     "completed",
			pathVaries: true,
		},
		{
			name: "a max_turns trailer is budget-fired and still certifies its reason",
			in: trailGateInput{Scan: trailScan([]byte(trailPaddedTrailer(2000) + "\n")),
				RunnerPath: trailRunnerUnread()},
			want:   trailGateBudgetFired,
			reason: trailBudgetTerminalReason,
		},
		{
			name: "ordinary stream-json with no trailer certifies nothing",
			in: trailGateInput{Scan: trailScan([]byte(trailFixtureNoTrailer)),
				RunnerPath: trailRunnerUnread()},
			want: trailGateNoTrailer,
		},
		{
			name: "a line past bufio.Scanner's default aborts the scan and certifies nothing",
			in: trailGateInput{Scan: trailScan([]byte(trailPaddedTrailer(trailOverlongPad) + "\n")),
				RunnerPath: trailRunnerUnread()},
			want: trailGateScanAborted,
		},
		{
			name: "a state nobody defined is out of contract, never absent",
			in: trailGateInput{Scan: trailScanResult{State: "some-state-nobody-defined"},
				RunnerPath: trailRunnerUnread()},
			want: trailGateOutOfContract,
		},
		{
			// Named apart from the row above because the zero value is the
			// realistic accident, not an invented string.
			name: "the zero scan result is out of contract",
			in: trailGateInput{Scan: trailScanResult{},
				RunnerPath: trailRunnerUnread()},
			want: trailGateOutOfContract,
		},
		{
			// AC2's headline: the pointer trap's first consumer, handed the trap.
			// Reaching a value here at all is the assertion — a panic fails the
			// test by escaping the subtest.
			name: "a seen state carrying a nil trailer is out of contract and does not panic",
			in: trailGateInput{Scan: trailScanResult{State: trailSeen, Trailer: nil},
				RunnerPath: trailRunnerUnread()},
			want: trailGateOutOfContract,
		},
		{
			name: "a seen state whose terminal_reason is present and empty is out of contract",
			in: trailGateInput{Scan: trailGateEmptyReasonScan(),
				RunnerPath: trailRunnerUnread()},
			want: trailGateOutOfContract,
		},
		{
			// #1419's added row, and the one that keeps clause B of
			// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt TOTAL: no other
			// row is absence-shaped, so without this one the absence arm's THREE
			// return sites are the ones no fixture reaches, and an arm that forgot
			// RunnerPath: in.RunnerPath there could hide behind the sites that
			// carried it. Three of eleven rather than one of eight since #1420 split
			// the arm, which is why the exemption below is scoped BY DECLARATION and
			// clause B keeps running on every reading. What the exemption covers is
			// not what this comment used to say: it was scoped to the byte comparison
			// until #1417 added the value to it, and #1433 added the certified reason.
			//
			// It is ONE OF TWO rows whose Detail depends on the reading — the usable
			// row above is the other, since #1433 — and the sweep drives it under all
			// five: it reaches trailReasonAbsentOwesOne, trailReasonAbsentOwesNone
			// and trailReasonPathUnnamed across them, so this single row is also the
			// sweep's positive proof that its own exempted arm DOES vary. Its own
			// RunnerPath stays trailRunnerUnread() like every other row's, which is
			// what keeps the slice's uniform-path premise — and with it the three
			// consumers that do not vary the path — unchanged by either split.
			name: "a seen state whose terminal_reason is absent from the line is out of contract",
			in: trailGateInput{Scan: trailGateAbsentReasonScan(),
				RunnerPath: trailRunnerUnread()},
			want:       trailGateOutOfContract,
			pathVaries: true,
		},
	}
}

// --- tests -------------------------------------------------------------------

// TestTrailAdmissibilityConstantsAreClosed is AC5's structural claim made
// executable, following TestTrailConstantsAreClosed's shape
// (result_trailer_observation_test.go:351) and EXTENDED TO CHECK ACROSS SPACES.
//
// #1266's helper is scoped to one space per call, so it cannot see a new value
// colliding with a shipped one — and that collision is the realistic mistake
// here, because this file's results mean nearly what the scan's input states
// mean. One union map covers within-space, cross-space and against-shipped
// distinctness in a single loop. Its size is READ OFF THE MAP rather than
// printed here: the shipped comment said twenty-nine while the map already held
// thirty-five, having gone stale when #1366 added six reason values; #1417's two
// made thirty-seven, #1434's two made thirty-nine, #1439's four made forty-three,
// #1440's eight made fifty-one, and #1446's three make fifty-four. A number kept
// by hand beside a set is a number that drifts, so the loop below counts.
//
// #1271's run-level outcomes joined the map rather than starting a third closure
// test, #1439's ordering-premise values joined it rather than starting a fifth,
// #1440's sighting values joined it rather than starting a sixth, and #1446's
// route values joined it rather than starting a seventh, for the same reason: five
// spaces now mean nearly the same words (an input state, the gate's view of it,
// the run's view of it, the ordering premises' view of the same events one layer
// under the run, and the sighting's view of them from a different evidence class),
// and only a union can see a copy-paste across them. Two near-collisions make the
// point, and the second is the sharper one: order-void-pyry-did-not-exit sits one
// word from run-void-pyry-did-not-exit — one word apart in a VOID value — while
// #1440's sighting-alive-by-ordering states the same English sentence as
// run-running-at-trailer, which is the same claim in the POSITIVE value, differing
// only in the evidence class it rests on. #1446 made that pair BOTH run-level
// answers, which is what its route values name apart.
//
// EVERY VALUE IN THIS MAP HAS AN ARM IN ITS CONSUMER — trailGate and
// trailAdmitAttribution for the first two spaces, trailClassifyRun for the
// third, trailReasonAgainstPath (trailer_terminal_reason_test.go:222) for the
// fourth, trailCertifyOrdering (trail_ordering_premises_test.go) for the fifth,
// and trailEstablishSighting (trail_sighting_liveness_test.go) for the sixth.
// #1446's two evidence-route values are the one exception in shape rather than in
// substance: they are not a predicate's answers but a FIELD trailClassifyRun sets
// beside them, so what they have in that function is an assignment rather than an
// arm — one per run-level answer, and TestTrailClassifyRun's per-row check is
// where their coverage lives.
// That is a comment and not a check: this test catches a COLLIDING
// value, never an UNHANDLED one, so a NEW gate or admit value added here and to
// its membership predicate would pass trailClassifyRun's contract block and then
// find no arm. That is not hypothetical — #1417 added trailGateAbsentOwesNone,
// and the arm it needed in trailClassifyRun's step-1 switch was landed in the
// same commit precisely because nothing here would have caught its absence.
// Recorded in #1271's spec § Open questions Q2. #1366's fourth space
// closes that gap for itself rather than here: TestTrailReasonAgainstPath
// asserts the set of values its nine rows REACH is exactly the six, which is the
// unhandled-value check this map cannot make.
func TestTrailAdmissibilityConstantsAreClosed(t *testing.T) {
	all := map[string]string{
		// This ticket's gate values.
		"trailGateUsable":          trailGateUsable,
		"trailGateNoTrailer":       trailGateNoTrailer,
		"trailGateScanAborted":     trailGateScanAborted,
		"trailGateBudgetFired":     trailGateBudgetFired,
		"trailGateAbsentOwesNone":  trailGateAbsentOwesNone,
		"trailGatePresentOwesNone": trailGatePresentOwesNone,
		"trailGateOutOfContract":   trailGateOutOfContract,
		// This ticket's admissibility values.
		"trailAdmitProof":              trailAdmitProof,
		"trailAdmitVoidBudgetFired":    trailAdmitVoidBudgetFired,
		"trailAdmitVoidInstrument":     trailAdmitVoidInstrument,
		"trailAdmitVoidNoLine":         trailAdmitVoidNoLine,
		"trailAdmitVoidGroupUnnamed":   trailAdmitVoidGroupUnnamed,
		"trailAdmitVoidNotOneReapLine": trailAdmitVoidNotOneReapLine,
		"trailAdmitOutOfContract":      trailAdmitOutOfContract,
		// #1271's run-level outcomes: four answers and eleven named voids since
		// #1447 took the MEASURED refutation off the blanket #1446 left it under —
		// #1446 gave the arm that reaches trailOutcomeVoidPathOwesNoReason a finding
		// of its own, beside #1434's trailOutcomeVoidReasonNotOwedByPath and #1417's
		// trailOutcomeVoidPathOwesNoReason. Its two evidence-route values follow,
		// in the same map for this map's whole reason: the two run-level ANSWERS
		// state nearly the same English sentence, so the strings that separate
		// their evidence classes must be as distinct as the answers are.
		"trailOutcomeRunningAtTrailer":             trailOutcomeRunningAtTrailer,
		"trailOutcomeAliveAtSightingByOrdering":    trailOutcomeAliveAtSightingByOrdering,
		"trailRouteReapLog":                        trailRouteReapLog,
		"trailRouteSighting":                       trailRouteSighting,
		"trailOutcomeMatchedUnattributed":          trailOutcomeMatchedUnattributed,
		"trailOutcomeNoRowMatched":                 trailOutcomeNoRowMatched,
		"trailOutcomeVoidBudgetFired":              trailOutcomeVoidBudgetFired,
		"trailOutcomeVoidNoTrailer":                trailOutcomeVoidNoTrailer,
		"trailOutcomeVoidTrailerScanAborted":       trailOutcomeVoidTrailerScanAborted,
		"trailOutcomeVoidPyryDidNotExit":           trailOutcomeVoidPyryDidNotExit,
		"trailOutcomeVoidArgvScanErrored":          trailOutcomeVoidArgvScanErrored,
		"trailOutcomeVoidNoRowsParsed":             trailOutcomeVoidNoRowsParsed,
		"trailOutcomeVoidLivenessInstrument":       trailOutcomeVoidLivenessInstrument,
		"trailOutcomeVoidPathOwesNoReason":         trailOutcomeVoidPathOwesNoReason,
		"trailOutcomeVoidPinnedPidDidNotEstablish": trailOutcomeVoidPinnedPidDidNotEstablish,
		"trailOutcomeVoidReasonNotOwedByPath":      trailOutcomeVoidReasonNotOwedByPath,
		"trailOutcomeOutOfContract":                trailOutcomeOutOfContract,
		// #1366's terminal-reason-against-path values.
		"trailReasonAbsentOwesNone":  trailReasonAbsentOwesNone,
		"trailReasonPresentOwesNone": trailReasonPresentOwesNone,
		"trailReasonAbsentOwesOne":   trailReasonAbsentOwesOne,
		"trailReasonBlankOwesOne":    trailReasonBlankOwesOne,
		"trailReasonNamedOwesOne":    trailReasonNamedOwesOne,
		"trailReasonPathUnnamed":     trailReasonPathUnnamed,
		// #1439's ordering-premise values: one certification and one void per
		// premise. order-void-pyry-did-not-exit is the reason they are here rather
		// than in a closure test of their own — it sits one word from #1271's
		// trailOutcomeVoidPyryDidNotExit, and a per-space helper is scoped to one
		// space per call and could not see the pair.
		"trailOrderCertified":     trailOrderCertified,
		"trailOrderVoidUnsighted": trailOrderVoidUnsighted,
		"trailOrderVoidNoExit":    trailOrderVoidNoExit,
		"trailOrderVoidUnheld":    trailOrderVoidUnheld,
		// #1440's sighting values: three outcomes and five reasons.
		// run-running-at-trailer is the reason they are here rather than in a
		// closure test of their own — trailOutcomeRunningAtTrailer's doc says an
		// admissible attribution "proves the process group was alive when the trailer
		// was written", which is the same English sentence trailSightingEstablished
		// states from the ORDERING evidence class, and a per-space helper is scoped
		// to one space per call and could not see the pair. Its reasons are in the
		// map too, on #1366's precedent: a reason value collides exactly as an
		// outcome value does.
		"trailSightingEstablished":               trailSightingEstablished,
		"trailSightingUnestablished":             trailSightingUnestablished,
		"trailSightingVoid":                      trailSightingVoid,
		"trailSightingReasonPidRunning":          trailSightingReasonPidRunning,
		"trailSightingReasonPidReapedPending":    trailSightingReasonPidReapedPending,
		"trailSightingReasonPidGone":             trailSightingReasonPidGone,
		"trailSightingReasonPidReadFailed":       trailSightingReasonPidReadFailed,
		"trailSightingReasonOrderingUncertified": trailSightingReasonOrderingUncertified,
		// #1266's shipped spaces, in the same map on purpose: a gate result that
		// collided with a scan state would be a result and an input wearing one
		// string, which is the confusion the gate- prefix exists to prevent.
		"trailSeen":           trailSeen,
		"trailAbsent":         trailAbsent,
		"trailAborted":        trailAborted,
		"trailBoundFromMiss":  trailBoundFromMiss,
		"trailBoundFromStart": trailBoundFromStart,
		"trailBoundNone":      trailBoundNone,
	}

	byValue := make(map[string]string, len(all))
	for name, value := range all {
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

	// The same claim read off the records themselves. The failure mode is an
	// unfilled field reading as a filled one, not two constants colliding, so
	// the zero value is checked against EVERY constant and not only its own
	// space's.
	var zeroGate trailGateResult
	var zeroAdmit trailAdmitResult
	var zeroRun trailRunOutcome
	var zeroOrder trailOrderResult
	var zeroSighting trailSightingResult
	for name, value := range all {
		if zeroGate.Value == value {
			t.Errorf("the zero trailGateResult reads as %s (%q)", name, value)
		}
		if zeroAdmit.Value == value {
			t.Errorf("the zero trailAdmitResult reads as %s (%q)", name, value)
		}
		if zeroRun.Value == value {
			t.Errorf("the zero trailRunOutcome reads as %s (%q)", name, value)
		}
		if zeroOrder.Value == value {
			t.Errorf("the zero trailOrderResult reads as %s (%q)", name, value)
		}
		if zeroSighting.Value == value {
			t.Errorf("the zero trailSightingResult reads as %s (%q)", name, value)
		}
		if zeroSighting.Reason == value {
			t.Errorf("the zero trailSightingResult's reason reads as %s (%q)", name, value)
		}
	}
	if zeroGate.Reason != "" {
		t.Errorf("the zero trailGateResult certifies %q — an uncertified record must never read "+
			"as certified", zeroGate.Reason)
	}
	// The same failure mode on #1440's record, which is the second in the family to
	// carry a Reason: an unfilled reason reading as a filled one would let a
	// consumer that branches on the reason — the whole point of the field, since two
	// verdicts share one value — branch on nothing.
	if zeroSighting.Reason != "" {
		t.Errorf("the zero trailSightingResult names reason %q — an unfilled reason must never "+
			"read as one a consumer can branch on", zeroSighting.Reason)
	}
	// The same failure mode on #1271's record: an unfilled field reading as a
	// filled one. trailBoundFromStart carries a real duration that bounds
	// NOTHING, so an unbounded record that read as bounded would publish a
	// non-bound wearing a bound's label.
	if zeroRun.Bounded || zeroRun.BoundFrom != "" {
		t.Errorf("the zero trailRunOutcome reports bounded=%t from %q — a record carrying no "+
			"bound must never read as one bounded by a non-matching poll", zeroRun.Bounded,
			zeroRun.BoundFrom)
	}

	// trailBudgetTerminalReason is a wire literal rather than an outcome, so it
	// is not in the map above; it still must be non-empty, because an empty one
	// would make trailGateBudgetFired unreachable (the empty-reason arm fires
	// first) and turn every budget-fired run into a false proof. See the
	// constant's own comment for the rename risk this cannot cover.
	if trailBudgetTerminalReason == "" {
		t.Error("trailBudgetTerminalReason is empty, so no run can ever be recognised as " +
			"budget-fired and every one of them would report as usable")
	}
}

func TestTrailGate(t *testing.T) {
	for _, tc := range trailGateCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := trailGate(tc.in)

			if got.Value != tc.want {
				t.Fatalf("value: got %q (%s), want %q", got.Value, got.Detail, tc.want)
			}
			if !trailIsGateValue(got.Value) {
				t.Errorf("value: %q is outside the recorded gate space", got.Value)
			}
			if got.Reason != tc.reason {
				t.Errorf("certified reason: got %q, want %q", got.Reason, tc.reason)
			}
			// The certification invariant, asserted independently of the row's
			// expectation so a wrong row cannot make it vacuous: a reason is
			// present exactly on the two arms that certify.
			certifies := got.Value == trailGateUsable || got.Value == trailGateBudgetFired
			if certifies != (got.Reason != "") {
				t.Errorf("value %s carries reason %q: a reason must be non-empty iff the value is "+
					"%s or %s", got.Value, got.Reason, trailGateUsable, trailGateBudgetFired)
			}
			if got.Detail == "" {
				t.Error("empty detail: a result that cannot say which arm fired and why is " +
					"indistinguishable from a reading")
			}
		})
	}

	t.Run("the out-of-contract details name their own sub-case", func(t *testing.T) {
		// Four inputs reach one value, and FIVE arms answer it — a state outside
		// the three trailScan documents, a nil Trailer, an absent terminal_reason
		// on a path that owes one, an absent one under a reading naming no runner,
		// and a present-and-blank one. Without this they are indistinguishable in a
		// published record: a reader cannot tell a nil pointer from a state nobody
		// defined, nor a terminal_reason that is ON the line and blank from one
		// that is not on the line at all. The last two decode identically to "", so
		// the Detail is the ONLY channel that says which record arrived.
		//
		// Four of the five are covered here; the ONE that is not needs a reading
		// that names a runner, while every input in this sub-test carries
		// trailRunnerUnread(). That is the OWES-ONE absence, which needs a ptyrunner
		// reading and is covered by TestTrailGateNamesWhichAbsenceCaseFired, driving
		// its own. The count moved three times: #1420 left six, #1417 moved the
		// owes-none absence out to trailGateAbsentOwesNone leaving five — the same
		// driver proves that arm — #1433 added the present-and-named case making six
		// again, and #1434 moved that one out to trailGatePresentOwesNone, whose own
		// driver is TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone.
		//
		// Each of the two #1419 arms asserts its OWN markers and the OTHER's
		// ABSENCE. That is what makes a row red on its own when its own case
		// misroutes, rather than merely detecting that the two Details differ —
		// which a swap of the two arms would leave green.
		nilTrailer := trailGate(trailGateInput{Scan: trailScanResult{State: trailSeen},
			RunnerPath: trailRunnerUnread()})
		if !strings.Contains(nilTrailer.Detail, "nil trailer") {
			t.Errorf("nil-trailer detail: got %q, want it to name the nil trailer",
				nilTrailer.Detail)
		}
		emptyReason := trailGate(trailGateInput{Scan: trailGateEmptyReasonScan(),
			RunnerPath: trailRunnerUnread()})
		if !strings.Contains(emptyReason.Detail, "terminal_reason is empty") {
			t.Errorf("empty-reason detail: got %q, want it to name the empty terminal reason",
				emptyReason.Detail)
		}
		if !strings.Contains(emptyReason.Detail, "not for an absent key, NO LIVE REPRO EXISTS") {
			t.Errorf("empty-reason detail: got %q, want the no-live-repro claim SCOPED to this "+
				"shape — emitter.go:383-391 is a chokepoint so pyry cannot render a blank "+
				"terminal_reason, but that says nothing about an ABSENT one, which every "+
				"healthy PYRY_USE_STREAMJSON=1 run produces. Unscoped, the claim sends a "+
				"reader hunting for a run that does not exist while mis-describing the one "+
				"that does", emptyReason.Detail)
		}
		// Re-pointed by #1420 from the retired NOT DECIDED AT THIS ARM phrase onto
		// the three case markers that replaced it. Left keyed on the retired
		// phrase this would be GREEN AND VACUOUS the moment that phrase left the
		// file, and the mutual-exclusion check it exists to make would be gone:
		// this input's terminal_reason IS on the line, so no absence case applies
		// to it and the present-and-empty arm must name none of them.
		for _, marker := range trailGateAbsenceCaseMarkers() {
			if strings.Contains(emptyReason.Detail, marker) {
				t.Errorf("empty-reason detail: got %q, want it NOT to carry the absence arm's case "+
					"marker %q — this input's terminal_reason IS on the line, and reaching an "+
					"absence arm would publish the wrong record about it", emptyReason.Detail,
					marker)
			}
		}
		absentReason := trailGate(trailGateInput{Scan: trailGateAbsentReasonScan(),
			RunnerPath: trailRunnerUnread()})
		if !strings.Contains(absentReason.Detail, "NO terminal_reason key on the line") {
			t.Errorf("absent-reason detail: got %q, want it to name the key as off the line",
				absentReason.Detail)
		}
		// Re-pointed by #1420: this arm now DECIDES which absence fired, so the
		// phrase saying it does not is retired and the assertion becomes the one
		// that names the case. The input carries trailRunnerUnread(), which
		// reduces to indeterminate, so the case is the path-unnamed one.
		//
		// The retired phrase was a TAIL phrase and this marker is NOT: it sits
		// where the embedded Detail begins, with that Detail's own explanation
		// after it. So this assertion no longer doubles as the cap tripwire it
		// used to be — a truncated Detail would keep the marker and lose the
		// clauses explaining it, and pass here. What guards that is the headroom
		// assertion on the OUTPUT in TestTrailGateNamesWhichAbsenceCaseFired,
		// whose R3 row is this same input.
		if !strings.Contains(absentReason.Detail, trailReasonPathUnnamed) {
			t.Errorf("absent-reason detail: got %q, want it to name case %s — this input's "+
				"reading names no runner, and a path that owes a terminal_reason and one that "+
				"owes none are opposite readings of the same absent key",
				absentReason.Detail, trailReasonPathUnnamed)
		}
		if strings.Contains(absentReason.Detail, "terminal_reason is empty") {
			t.Errorf("absent-reason detail: got %q, want it NOT to describe the key as empty — "+
				"the key is not on the line at all, and \"empty\" is the other arm's record",
				absentReason.Detail)
		}
		if strings.Contains(absentReason.Detail, "NO LIVE REPRO EXISTS") {
			t.Errorf("absent-reason detail: got %q, want it NOT to claim no live repro exists — "+
				"that claim is TRUE of a present-and-empty terminal_reason and FALSE of an "+
				"absent one, whose live repro is every healthy PYRY_USE_STREAMJSON=1 run "+
				"(streamrunner passes claude's bytes through unchanged, runner.go:177-179). "+
				"This is the assertion that catches the old prose copy-pasted onto the new arm",
				absentReason.Detail)
		}
		unknown := trailGate(trailGateInput{Scan: trailScanResult{State: "some-state-nobody-defined"},
			RunnerPath: trailRunnerUnread()})
		if !strings.Contains(unknown.Detail, "some-state-nobody-defined") {
			t.Errorf("unknown-state detail: got %q, want it to quote the state it rejected",
				unknown.Detail)
		}
	})
}

// trailGateAbsenceCaseMarkers is the three cases the absence arm now splits into,
// as the constants trailReasonAgainstPath answers with — the markers a composed
// Detail must carry exactly one of.
//
// A function rather than a package-level var, this family's idiom
// (trailExpectedKeyNames(), trailer_key_names_test.go:115-117): the value holds a
// []string, go test -race runs this package's tests in parallel, and a shared
// backing array would let one caller's mutation reach another's.
func trailGateAbsenceCaseMarkers() []string {
	return []string{trailReasonAbsentOwesOne, trailReasonAbsentOwesNone, trailReasonPathUnnamed}
}

// TestTrailGateNamesWhichAbsenceCaseFired is AC1 made deterministic: the absence
// arm splits three ways and EACH CASE IS PROVABLE ON ITS OWN, rather than the
// three merely being distinguishable from one another.
//
// # The value is a precondition on three rows and THE DISCRIMINATOR on one
//
// Five arms answer trailGateOutOfContract since #1434, so on R1, R3 and R4
// got.Value says nothing about WHICH one ran. It is asserted there anyway, as the non-vacuity
// precondition — a row that reached a different arm would sweep the wrong Detail
// and report clean about an arm it never ran — and the case marker is what names
// the arm. On R2 it is the assertion itself: #1417 gave the owes-none absence a
// value of its own, trailGateAbsentOwesNone, so that row's value IS the answer.
// Every row keeps its marker assertions regardless, which is strictly stronger
// and is what the companion sweep below needs the markers present for.
//
// The certified reason stays empty on all four rows: naming which case fired,
// and taking one of them out of the out-of-contract value, still certifies
// nothing — so trailClassifyRun's C1 and C2 (trail_run_outcome_test.go:540-551,
// :553-564) stay green with C1's enumeration widened by one and C2 unamended.
//
// # Markers are matched WHOLE, never as fragments
//
// "one-path" IS a substring of "none-path", so a fragment assertion passes in
// silence across a swap of the two owes- arms. The three whole constants are not
// substrings of one another — checked below before they are used — which is what
// makes "carries mine and neither of the others" a real mutual exclusion rather
// than a coincidence of wording.
//
// # The mutant x row matrix, each demonstrated under `go test -overlay`
//
// The mutations fall in TWO places, and the shipped claim that they are all of
// one place went false at #1417. M1-M4 mutate trailReasonAgainstPath's LABEL
// SWITCH — which case the reading reduces to. M5-M9 mutate the gate's own switch
// over against.Value — which gate value each case is awarded. Each row below is
// the SOLE RED for at least one mutant, and M7 is recorded precisely because it
// is the sole red for none:
//
//	M1  an indeterminate reading treated as streamrunner   sole red R3, wrong value trailReasonAbsentOwesNone
//	M2  an indeterminate reading treated as ptyrunner      sole red R3, wrong value trailReasonAbsentOwesOne
//	M3  a ptyrunner reading treated as streamrunner        sole red R1, wrong value trailReasonAbsentOwesNone
//	M4  a streamrunner reading treated as ptyrunner        sole red R2, wrong value trailReasonAbsentOwesOne
//	M5  the new value awarded on the path-unnamed answer   sole red R3, wrong value trailGateAbsentOwesNone
//	M6  the new value awarded on the owes-one answer       sole red R1, wrong value trailGateAbsentOwesNone
//	M7  the new value awarded on EVERY absence answer      red R1 AND R3 — sole red for neither
//	M8  the new arm keyed on decodedReason, not the keys   sole red R4, which routes into the absence switch
//	M9  the new arm certifying a reason                    sole red R2, on the Reason assertion
//
// M1 and M2 SHARE R3 and no row separates them. They are told apart by the WRONG
// VALUE each produces, which is why the failure message names got-vs-want and
// never "is / is not the path-unnamed case" — a coarser assertion loses that
// distinction in the output. That is TestTrailReasonAgainstPath's own rule
// (trailer_terminal_reason_test.go:337-344), inherited here because these rows
// are the first decision-path consumer of that function. M5 shares R3 with them
// and is told apart the same way, by the GATE value R3 reaches rather than the
// case marker it names.
//
// M8 is the collapse the key-name reading exists to prevent, made a mutant:
// inside the enclosing reason == "" block a decoded test is constantly true, so
// present-and-empty falls into the absence switch and R4 — whose Detail must name
// no case at all — is where it surfaces. M9 has a second, independent red outside
// this file: trailClassifyRun's C2 rejects a non-certifying gate value carrying a
// reason.
//
// # Headroom is asserted ON THE OUTPUT, per row
//
// trailDetail caps at reachMaxCommandBytes and reachCapCommand TRUNCATES AND
// MARKS rather than failing (background_reach_probe_test.go:945-950). The shipped
// single absence Detail was 480 B of the 512 and the embedded Details are
// 264-286 B, so a Detail that appended rather than rewrote is truncated 1-5 bytes
// PAST its embedded value marker — the marker survives and every clause
// explaining it is amputated, which false-greens the marker assertion above. The
// truncation marker is therefore checked directly, and the length is reported
// with its remaining margin so a later reword that overflows is red HERE rather
// than shipping a severed sentence.
func TestTrailGateNamesWhichAbsenceCaseFired(t *testing.T) {
	markers := trailGateAbsenceCaseMarkers()
	for i, a := range markers {
		for j, b := range markers {
			if i == j {
				continue
			}
			if strings.Contains(a, b) {
				t.Fatalf("case marker %q contains %q: the mutual-exclusion assertions below would "+
					"report the wrong case as present, so a swap of two arms could pass", a, b)
			}
		}
	}

	tests := []struct {
		name string
		scan trailScanResult
		// reading is the runner path the gate is handed, always tdnRunnerFromArgv's
		// own output over an argv rather than a re-typed literal.
		reading string
		// wantValue is the gate value the row must reach. It is DECLARED PER ROW
		// rather than shared, which is what #1417 changed: three rows still reach
		// trailGateOutOfContract and assert it as their non-vacuity precondition,
		// and R2 reaches trailGateAbsentOwesNone, where the same field is the
		// discriminator the ticket landed.
		wantValue string
		// want is the case marker the Detail must carry, and "" means it must carry
		// NONE of the three — the present-and-empty arm, which does not decide a
		// case at all.
		want string
		// alsoCarries are phrases this row's Detail must keep, for the row whose
		// arm this ticket does not reopen.
		alsoCarries []string
	}{
		{
			name:      "R1 absent from a path that owes one",
			scan:      trailGateAbsentReasonScan(),
			reading:   tdnRunnerFromArgv(tdnFixturePtyArgv),
			wantValue: trailGateOutOfContract,
			want:      trailReasonAbsentOwesOne,
		},
		{
			// #1417's row: the ONE absence case that is a reading rather than a
			// caller's bug, so the value is what says so. The marker is asserted
			// too — strictly stronger, and the companion sweep in
			// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt needs it present.
			name:      "R2 absent from a path that owes none",
			scan:      trailGateAbsentReasonScan(),
			reading:   tdnRunnerFromArgv(tdnFixtureStreamArgv),
			wantValue: trailGateAbsentOwesNone,
			want:      trailReasonAbsentOwesNone,
		},
		{
			// trailRunnerUnread() is the reading BOTH shipped gathers supply
			// (finding_run_gather_test.go:552, :789; trail_run_rig_test.go:162), so
			// this row is the only one of the three a live run reaches today — and
			// with it the only absence answer a live run can reach, which is why
			// #1417's value is unreachable from a live gather and no comment here
			// claims otherwise.
			name:      "R3 absent from a path naming no runner",
			scan:      trailGateAbsentReasonScan(),
			reading:   trailRunnerUnread(),
			wantValue: trailGateOutOfContract,
			want:      trailReasonPathUnnamed,
		},
		{
			// #1419's arm, unamended and path-invariant. It is here so that a case
			// marker leaking onto it — the natural way to break the split — is red,
			// and since #1417 it is also where a new arm keyed on the decoded reason
			// rather than the key names surfaces (M8).
			name:        "R4 present and empty decides no case at all",
			scan:        trailGateEmptyReasonScan(),
			reading:     trailRunnerUnread(),
			wantValue:   trailGateOutOfContract,
			want:        "",
			alsoCarries: []string{"terminal_reason is empty", "NO LIVE REPRO EXISTS"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := trailGate(trailGateInput{Scan: tc.scan, RunnerPath: tc.reading})

			if got.Value != tc.wantValue {
				t.Fatalf("value: got %q (%s), want %q. On the rows wanting %s this is the "+
					"non-vacuity precondition — five arms answer it, so a row reaching a "+
					"different one would sweep the wrong Detail and report clean about an arm it "+
					"never ran. On the row wanting %s it is the ASSERTION: absence on a path that "+
					"owes none is that path's documented healthy shape, and #1417 gave it a value "+
					"of its own so a healthy stream run stops reading as the caller having a bug",
					got.Value, got.Detail, tc.wantValue, trailGateOutOfContract,
					trailGateAbsentOwesNone)
			}
			if !trailIsGateValue(got.Value) {
				t.Errorf("value: %q is outside the recorded gate space", got.Value)
			}
			if got.Reason != "" {
				t.Errorf("certified reason: got %q, want empty — naming which absence fired, and "+
					"taking one of the three out of the out-of-contract value, must not start "+
					"certifying one. An absence certifies nothing on every path", got.Reason)
			}

			var named []string
			for _, m := range markers {
				if strings.Contains(got.Detail, m) {
					named = append(named, m)
				}
			}
			switch {
			case tc.want == "" && len(named) != 0:
				t.Errorf("the Detail names case(s) %q, want none of the three: this input's "+
					"terminal_reason IS on the line, so no absence case applies to it. Detail: %s",
					named, got.Detail)
			case tc.want != "" && (len(named) != 1 || named[0] != tc.want):
				t.Errorf("the Detail names case(s) %q, want exactly [%s]: the reading was %q. A "+
					"wrong single name is an arm deciding the wrong case; two names are a Detail "+
					"that cannot be read as one answer. Detail: %s", named, tc.want, tc.reading,
					got.Detail)
			}

			for _, phrase := range tc.alsoCarries {
				if !strings.Contains(got.Detail, phrase) {
					t.Errorf("the Detail dropped %q, which #1419 landed on this arm and this "+
						"ticket does not reopen. Detail: %s", phrase, got.Detail)
				}
			}

			// Headroom, on the output. The truncation marker is the direct
			// tripwire and the length is the margin a later reword spends.
			if strings.Contains(got.Detail, reachTruncationMarker) {
				t.Errorf("the Detail was TRUNCATED at %d bytes and marked: the case marker asserted "+
					"above can survive the cut while every clause explaining it is amputated, so "+
					"this row would otherwise pass against a severed sentence. Detail: %s",
					reachMaxCommandBytes, got.Detail)
			}
			if n := len(got.Detail); n > reachMaxCommandBytes {
				t.Errorf("the Detail is %d bytes against a %d byte cap: %d bytes over, with no "+
					"margin left. Rewrite this case's prose rather than growing it — the embedded "+
					"Detail is fixed and the gate's own prose is the only budget there is", n,
					reachMaxCommandBytes, n-reachMaxCommandBytes)
			}
		})
	}
}

// TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone is this ticket's AC1 and
// AC3 made deterministic, in TestTrailGateNamesWhichAbsenceCaseFired's shape: the
// PRESENCE side of the same reading, plus the two ordering decisions the arm
// records, each driven by a row rather than left to return order.
//
// # The value is THE DISCRIMINATOR on P1, the assertion on P2 and a precondition on P3
//
// #1434 gave this arm a value of its own, trailGatePresentOwesNone, and exactly ONE
// of trailGate's eleven return sites answers it — so on P1 got.Value IS the answer
// rather than the premise for reading a Detail. That is the same inversion #1417
// made on the absence side, and TestTrailGateNamesWhichAbsenceCaseFired's own
// heading records it there. P1 keeps its marker assertion regardless: it is
// strictly stronger, and the presence companion in
// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt needs the marker present.
// On P2 the value is the assertion for the older reason — a swap of the budget arm
// and this one moves the row off gate-budget-fired.
//
// P3 is the one row where the value is a precondition rather than the answer. Five
// arms answer trailGateOutOfContract since #1434, so a P3 that reached a different
// one would sweep the wrong Detail; what the row is FOR is the marker's ABSENCE.
// That absence used to be the ONLY detector of a swallowed present-and-blank,
// because #1433's arm and #1419's answered the same value and a value assertion
// stayed green across the swallow. Since #1434 the two values differ and P3's value
// assertion catches it too — the marker assertion stays as a second detector of
// different fabric, reading the Detail where the other reads the value.
//
// # The mutant x row matrix, each demonstrated under `go test -overlay`
//
// Every row is the SOLE RED AMONG THESE ROWS for at least one mutant. Reds
// elsewhere in the suite are named beside it rather than omitted — a mutant caught
// in two places is caught in two places, and a table that hid the second would
// misdescribe what this driver is worth:
//
//	P1  the arm still answering out of contract  sole row here (value); also the presence companion
//	P2  the arm placed BEFORE the budget arm     sole row here; also the sweep's max_turns row
//	P3  the arm swallowing present-and-empty     sole row here (value AND marker); also the sweep's row
//	P1  the arm certifying a reason              sole row here (Reason); also the presence companion and both classifier reds
//	P1  the arm's Detail outgrowing the ceiling  sole row here (headroom); also the leak sweep's presence row
//
// P1 carries three mutants because its three assertions fail independently: an arm
// left answering the shipped value moves neither the reason nor the length, a
// certifying arm leaves the length alone, and an overgrown Detail leaves Reason
// alone. trailClassifyRun's C2 (trail_run_outcome_test.go:553-564) rejects the
// certifying pair a layer up, and since #1434 trailRunCases()' new row
// (trail_run_outcome_test.go:1203-1213) drives this arm into that classifier WITHOUT
// pre-asserting, so C2's own Detail is quoted verbatim in a red TestTrailClassifyRun.
// Measured, this mutant reddens four tests rather than two.
//
// The remaining mutant — the new answer awarded on a PTYRUNNER reading — reddens
// NO row here, because every row is driven at a streamrunner reading and the
// mutant only moves the ptyrunner one. It belongs to the presence companion in
// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt, at reading 0, which is
// also where rows carrying trailRunnerUnread() are covered; measured, it reddens
// that companion, that sweep's usable-row premise, and
// TestTrailComposesUnderAPtyrunnerReading.
//
// Deferred to #1428, the reason x reading matrix ticket this one blocks, named
// here so the gap is stated rather than implied: discriminating on the literal
// "idle_stall" rather than on the path, and the two indeterminate-reading
// confusions.
//
// # Headroom is asserted ON THE OUTPUT, per row
//
// Same reason as the absence driver's: reachCapCommand TRUNCATES AND MARKS rather
// than failing, so a Detail that outgrew the cap keeps its case marker and loses
// every clause explaining it. The arm's real ceiling is 470 B and not 512 — the
// leak row asserts the 42 B trailNeedle would still have fitted — so the length
// check adds the needle rather than comparing against the cap alone. Measured, the
// three rows publish 427 / 298 / 442 B.
func TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone(t *testing.T) {
	// The premise the marker assertions rest on, in the shape the absence driver
	// already uses, and asserted rather than inherited from the ticket that
	// measured it. The presence constant shares the whole `on-owes-none-path`
	// suffix with one absence constant, so containment is a live risk here rather
	// than a formality — and it is checked in BOTH directions, since a marker
	// contained BY another is as bad as one containing it.
	for _, absence := range trailGateAbsenceCaseMarkers() {
		if strings.Contains(trailReasonPresentOwesNone, absence) {
			t.Fatalf("the presence marker %q contains the absence marker %q: every assertion that "+
				"an absence Detail names no presence case, or the reverse, would report the wrong "+
				"case as present", trailReasonPresentOwesNone, absence)
		}
		if strings.Contains(absence, trailReasonPresentOwesNone) {
			t.Fatalf("the absence marker %q contains the presence marker %q: the sub-test asserting "+
				"an empty-reason Detail carries no absence marker would then also have to answer "+
				"for this one, and the two spaces would stop being separable in a record",
				absence, trailReasonPresentOwesNone)
		}
	}

	// Every row is driven under a reading that reduces to streamrunner, because
	// that is the reading the arm is about. What separates them is the trailer.
	streamrunner := tdnRunnerFromArgv(tdnFixtureStreamArgv)

	tests := []struct {
		name string
		scan trailScanResult
		// wantValue is the gate value the row must reach.
		wantValue string
		// wantReason is what the row must certify: empty on both rows that certify
		// NOTHING — which since #1434 is one out-of-contract row and one reaching
		// this ticket's own value — and the budget literal on the row the budget arm
		// keeps.
		wantReason string
		// wantMarker is the presence case marker the Detail must carry, and ""
		// means it must NOT carry it.
		wantMarker string
		// alsoCarries are phrases this row's Detail must keep.
		alsoCarries []string
	}{
		{
			// The headline: the shape that was certified usable under ptyrunner's
			// justification until #1433, filed as the caller's bug until #1434, and
			// the only shape a live watchdog-killed stream run puts on the line
			// (streamrunner/watchdog.go:280, reached from
			// streamrunner/runner.go:250-253).
			name:       "P1 a named terminal_reason from a path that owes none",
			scan:       trailGateUsableScan(),
			wantValue:  trailGatePresentOwesNone,
			wantReason: "",
			wantMarker: trailReasonPresentOwesNone,
			alsoCarries: []string{
				// The claim limit, and the cite that is the REASON for it: claude's
				// own output reaches the same reading, so the arm may never say pyry
				// wrote the line.
				"NEVER that pyry wrote it",
				"streamrunner/runner.go:177-179",
				// What the record is about, stated so a reader cannot take it for a
				// statement about a live process.
				"never whether a process was alive",
			},
		},
		{
			// ORDERING 1, driven rather than argued: max_turns has two candidate
			// arms and the budget arm wins, because the budget void is STRUCTURAL
			// and outranks every reap-side void. A swap of the two arms moves this
			// row's value to trailGateOutOfContract and its reason to "", so the
			// row is red on both.
			name:       "P2 a max_turns terminal_reason reaches the budget arm, not this one",
			scan:       trailScan([]byte(trailPaddedTrailer(2000) + "\n")),
			wantValue:  trailGateBudgetFired,
			wantReason: trailBudgetTerminalReason,
			wantMarker: "",
		},
		{
			// ORDERING 2: present-and-blank keeps reaching #1419's path-invariant
			// arm, even though the reduction absorbs it into the presence answer.
			// The alsoCarries phrases are the record that answer publishes and this
			// one cannot — NO LIVE REPRO EXISTS is true of a blank key and false of
			// an absent one — so they are what a swallowed case loses.
			name:        "P3 a present-and-blank terminal_reason keeps reaching the path-invariant arm",
			scan:        trailGateEmptyReasonScan(),
			wantValue:   trailGateOutOfContract,
			wantReason:  "",
			wantMarker:  "",
			alsoCarries: []string{"terminal_reason is empty", "NO LIVE REPRO EXISTS"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := trailGate(trailGateInput{Scan: tc.scan, RunnerPath: streamrunner})

			if got.Value != tc.wantValue {
				t.Fatalf("value: got %q (%s), want %q. Three cases, and the field means something "+
					"different in each. On the row wanting %s it IS the assertion, and the one "+
					"#1434 landed: a terminal_reason on a path that owes none is a READING, so it "+
					"reaches a value of its own rather than being filed as the caller's bug. On "+
					"the row wanting %s it is the assertion for the older reason — the budget arm "+
					"keys on terminal_reason alone and wins, because a budget void is structural "+
					"and outranks every reap-side void. On the row wanting %s it is the "+
					"non-vacuity precondition: five arms answer it since #1434, so a row reaching "+
					"a different one would sweep the wrong Detail and report clean about an arm it "+
					"never ran", got.Value, got.Detail, tc.wantValue, trailGatePresentOwesNone,
					trailGateBudgetFired, trailGateOutOfContract)
			}
			if !trailIsGateValue(got.Value) {
				t.Errorf("value: %q is outside the recorded gate space", got.Value)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("certified reason: got %q, want %q — the presence arm certifies NOTHING, "+
					"which is what keeps trailClassifyRun's C2 green unamended and lets C4 force "+
					"an empty Admit; the budget arm keeps certifying, because the predicate needs "+
					"that reason to name the void", got.Reason, tc.wantReason)
			}

			if named := strings.Contains(got.Detail, trailReasonPresentOwesNone); named !=
				(tc.wantMarker != "") {
				t.Errorf("the Detail names case %s = %t, want %t. On the row wanting it present "+
					"this is strictly weaker than the value assertion above and kept anyway, "+
					"because the presence companion in "+
					"TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt needs the marker to be "+
					"there. On the rows wanting it absent it is a SECOND detector of different "+
					"fabric: it reads the Detail where the value assertion reads %s, so this arm's "+
					"prose leaking onto a neighbour without moving its value is caught here and "+
					"nowhere else. Detail: %s", trailReasonPresentOwesNone, named,
					tc.wantMarker != "", trailGatePresentOwesNone, got.Detail)
			}
			// The presence case is not an absence case, so no absence marker may
			// appear on any of these rows — the mirror of the assertion
			// trailGateAbsenceCaseMarkers() already carries on the empty-reason
			// Detail, and what keeps the new constant OUT of that set honest.
			for _, absence := range trailGateAbsenceCaseMarkers() {
				if strings.Contains(got.Detail, absence) {
					t.Errorf("the Detail carries the absence marker %q: every input here has its "+
						"terminal_reason ON the line, so no absence case applies to any of them. "+
						"Detail: %s", absence, got.Detail)
				}
			}

			for _, phrase := range tc.alsoCarries {
				if !strings.Contains(got.Detail, phrase) {
					t.Errorf("the Detail dropped %q. Detail: %s", phrase, got.Detail)
				}
			}

			// Headroom, on the output, against the arm's real 470 B ceiling rather
			// than the 512 B cap.
			if strings.Contains(got.Detail, reachTruncationMarker) {
				t.Errorf("the Detail was TRUNCATED at %d bytes and marked: the case marker "+
					"asserted above survives the cut while every clause explaining it is "+
					"amputated, so this row would otherwise pass against a severed sentence. "+
					"Detail: %s", reachMaxCommandBytes, got.Detail)
			}
			if n := len(got.Detail); n+len(trailNeedle) > reachMaxCommandBytes {
				t.Errorf("the Detail is %d bytes and the needle is %d, so the two do not fit "+
					"inside the %d byte cap: had this arm leaked the needle it would have been "+
					"truncated away and the leak sweep's row for it would be VACUOUS. The arm "+
					"cites the %d byte case constant rather than embedding the reduction's 395 B "+
					"Detail precisely to keep this margin — rewrite its prose rather than growing "+
					"it", n, len(trailNeedle), reachMaxCommandBytes, len(trailReasonPresentOwesNone))
			}
		})
	}
}

// trailGateRunnerReadings returns tdnRunnerFromArgv's five distinct answers, each
// obtained by DRIVING THE SHIPPED READER over an argv rather than by re-typing
// its prose as a literal. A re-typed copy would go on passing after the reader
// reworded itself, which is the vacuity the sweep below exists to avoid.
//
// Five and not three. The shipped "three" that finRecordInputs.ClaudeCommand's
// doc names (finding_run_record_test.go:235-238) counts the LEADING TOKENS
// finRecordRunnerLabel reduces to — ptyrunner / streamrunner / indeterminate —
// not the strings the function returns, and TestTdnRunnerFromArgv asserts by
// strings.HasPrefix against those three tokens over six rows, two of which reach
// one answer. The function returns five, and the invariance claim is about the
// five.
//
// The argvs are the shipped fixtures where shipped ones exist
// (teardown_liveness_probe_test.go:897, :902) and mirror TestTdnRunnerFromArgv's
// own rows otherwise. They are inputs to the READER and reach the gate never.
func trailGateRunnerReadings() []string {
	return []string{
		tdnRunnerFromArgv(tdnFixturePtyArgv),
		tdnRunnerFromArgv(tdnFixtureStreamArgv),
		tdnRunnerFromArgv(tdnFixturePtyArgv + " --input-format stream-json"),
		tdnRunnerFromArgv(`/opt/node/bin/node /opt/claude/cli.js ` +
			`--append-system-prompt-file /tmp/wd/system.txt`),
		tdnRunnerFromArgv(""),
	}
}

// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt is AC2 made
// deterministic, under the claim #1420 landed and #1433 exercised — a second arm
// reads the path now, and the claim held across that without weakening: the runner
// path reaches every return site, and a row's DECISION — value, certified reason
// and Detail — may move with the reading only where that row declares pathVaries,
// and nowhere else.
//
// # The Detail is compared AS BYTES
//
// A Detail that silently acquired the path would pass a value check while
// changing what the published record says, and Detail is what a reader of the
// artifact actually reads. Comparing bytes is what makes "the decision did not
// move" a claim about the published record rather than about the enum alone.
//
// # Two clauses keep the sweep from passing vacuously
//
//   - The readings are pairwise distinct. Two collapsed answers would silently
//     shrink forty-five comparisons to fewer, and the count is precisely the
//     thing the ticket had to correct from the shipped "three". It is also what
//     keeps the companion below from proving variance across fewer labels than it
//     claims.
//   - The arriving reading is READ BACK from outside the gate. A sweep that built
//     its input without filling the path would otherwise pass forty-five identical
//     comparisons while proving nothing about carriage. This assertion is TOTAL
//     over the gate's ELEVEN return sites — the nine rows reach all eleven, but
//     only when driven across the five readings. At any single reading they still
//     reach EIGHT, which is derived rather than moved with the total: "a state
//     nobody defined" and "the zero scan result" share one site, the absence row
//     reaches exactly one of the three absence sites, and since #1433 the usable
//     row reaches exactly one of the presence site and the usable site. Three
//     sites go unreached at any one reading and eleven minus three is eight — so
//     an arm that forgot to carry the field cannot hide behind an arm that did.
//
// # The exemption covers the whole DECISION, on two rows, and both are EXERCISED
//
// #1420 split the absence arm three ways, so row nine's Detail varies across the
// readings and all four of its byte comparisons against the baseline go red —
// under readings 1 through 4 it names a different case. #1417 then took one of
// those three cases out of trailGateOutOfContract, so row nine's VALUE varies
// too: trailGateAbsentOwesNone under reading 1, trailGateOutOfContract under the
// other four. #1433 then gave the usable row an arm to divert to, so its Detail
// and its value vary as well — and, for the first time in the tree, so does what
// it CERTIFIES: "completed" under readings 0 and 2 through 4, nothing under
// reading 1. Each of the three would go red against a CORRECT build, so the
// exemption covers value, certified reason and Detail together, on exactly the
// rows that declare pathVaries and nowhere else. What it must not become is the
// whole point of how it is scoped:
//
//   - Clause B runs UNCONDITIONALLY, on every row under every reading. Skipping
//     the row wholesale — the natural way to fix a red row — would withdraw clause
//     B from three return sites, each of which must echo RunnerPath, and row nine
//     is the only row that reaches any of them.
//   - The CERTIFIED-REASON comparison used to keep running unconditionally, the
//     exempted row included, on the argument that all three absence cases certify
//     nothing whatever the reading and that NOTHING IN THE TREE FORCED a narrower
//     scope. #1433's arm is the first thing in the tree that forces it: every
//     path-varying arm before it certified nothing on every reading, and that one
//     certifies "completed" under a ptyrunner reading and nothing under a
//     streamrunner one. So the comparison now sits BELOW the guard, and what the
//     sweep no longer covers on a declaring row is precisely "this arm's
//     certification does not move with the reading". That is paid for, per
//     declaring row, in the companions below — positively, and per reading, which
//     is a stronger statement than the invariance it replaced. It has a second
//     detector of different fabric one layer up: trailClassifyRun's C2
//     (trail_run_outcome_test.go:553-564) rejects a non-certifying gate value that
//     carries a reason.
//
// A green sweep that had simply stopped covering an arm would be silent about the
// arms it no longer covers, so ONE COMPANION PER DECLARING ROW drives its arm
// POSITIVELY, over that row's own fixture helper under all five readings:
//
//   - row nine's, asserting per reading the gate value it reaches and the case it
//     names — trailReasonAbsentOwesOne, trailReasonAbsentOwesNone and
//     trailReasonPathUnnamed — with the distinct-Detail count pinned at exactly
//     three so a degenerate arm cannot satisfy the markers vacuously.
//   - the usable row's, asserting per reading the value, the CERTIFIED REASON and
//     whether the Detail names trailReasonPresentOwesNone, with the distinct-Detail
//     count pinned at exactly two. That pin is what replaces the byte comparison
//     the exemption withdrew: it proves the usable arm still publishes ONE fixed
//     string across the four readings that reach it.
//
// # The copies alias one *resultTrailer, deliberately not mutated
//
// Varying the reading copies each row's trailGateInput, and a struct copy copies
// the POINTER: all five inputs for a row share one resultTrailer, the same
// aliasing trailRunWellFormed's doc warns about (trail_run_outcome_test.go:883).
// Only RunnerPath is ever assigned and nothing is ever written through the
// pointer — which holds for the companion sub-test too — and that is what keeps
// the sharing race-free, and what would have to hold load-bearingly if these
// subtests ever took t.Parallel().
func TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt(t *testing.T) {
	readings := trailGateRunnerReadings()

	// Clause A.
	for i := range readings {
		for j := i + 1; j < len(readings); j++ {
			if readings[i] == readings[j] {
				t.Fatalf("readings %d and %d are both %q: the sweep claims to drive %d DISTINCT "+
					"answers, and two that collapsed would make it weaker than it says",
					i, j, readings[i], len(readings))
			}
		}
	}

	// "every other field is compared" has to stay true under a later edit, so the
	// field count is pinned rather than trusted — this family's own idiom
	// (finding_run_record_test.go:780-782).
	if n := reflect.TypeOf(trailGateResult{}).NumField(); n != 4 {
		t.Fatalf("trailGateResult has %d fields, want 4: a fifth must either join the compared "+
			"set below or state its own exemption, and until it does this sweep no longer "+
			"proves what its name says", n)
	}

	for _, tc := range trailGateCases() {
		t.Run(tc.name, func(t *testing.T) {
			var base trailGateResult

			for i, reading := range readings {
				in := tc.in
				in.RunnerPath = reading

				got := trailGate(in)

				// Clause B, on every reading rather than on the baseline alone.
				if got.RunnerPath != reading {
					t.Fatalf("the gate was handed reading %d (%q) and carried out %q: the reading "+
						"that arrived is not the reading that was driven, so nothing below is a "+
						"statement about this input", i, reading, got.RunnerPath)
				}

				if i == 0 {
					base = got
					// The premise, shared with TestTrailGate and asserted before the
					// claim: a gate broken into returning one value for everything
					// would satisfy the invariance vacuously.
					if base.Value != tc.want || base.Reason != tc.reason {
						t.Fatalf("premise: the gate reads %q certifying %q (%s), want %q certifying "+
							"%q — the invariance below says the decision does not move, not that "+
							"it is right", base.Value, base.Reason, base.Detail, tc.want, tc.reason)
					}
					continue
				}

				if tc.pathVaries {
					// The whole DECISION is exempt here and only here: the value, the
					// certified reason and the bytes together. The certified reason
					// joined them in this ticket, because its arm is the first in the
					// tree whose certification moves with the reading — "completed"
					// under the baseline reading and nothing under the streamrunner
					// one — so an unconditional comparison now goes red against a
					// CORRECT build. Every declaring row's variance is proven
					// positively per reading by a companion below, value and certified
					// reason both, so an exempted arm is proven rather than merely
					// uncovered.
					continue
				}
				if got.Reason != base.Reason {
					t.Errorf("the certified reason differs across readings: under %q the gate "+
						"certifies %q, under %q it certifies %q — this row does not declare "+
						"pathVaries, so its arm ignores the path entirely and cannot vary what it "+
						"certifies", readings[0], base.Reason, reading, got.Reason)
				}
				if got.Value != base.Value {
					t.Errorf("the value differs across readings: under %q the gate reads %q, "+
						"under %q it reads %q — this row does not declare pathVaries, so its arm "+
						"is one of the ones that ignore the path entirely", readings[0], base.Value,
						reading, got.Value)
				}
				if !bytes.Equal([]byte(got.Detail), []byte(base.Detail)) {
					t.Errorf("the Detail differs across readings, byte for byte:\n under %q: %s\n"+
						" under %q: %s\na Detail that acquired the reading would pass a value "+
						"check while changing what the published record says. This row does not "+
						"declare pathVaries, so its arm is one of the ones that ignore the path",
						readings[0], base.Detail, reading, got.Detail)
				}
			}
		})
	}

	// The companion: the exempted arm DOES vary, proven positively rather than
	// left as the one arm the sweep above no longer covers.
	//
	// It drives row nine's own fixture helper rather than indexing the slice, so a
	// reordering of the rows cannot silently point it at a different arm. The scan
	// is taken ONCE and only RunnerPath is assigned per reading, which is the
	// aliasing discipline the doc above states.
	t.Run("the absence arm names a different case under each reading", func(t *testing.T) {
		// Per reading, the PAIR: the gate value reached and the case named. The
		// value is part of the expectation since #1417 — reading 1 is the one
		// absence a run does not owe, so it answers a value of its own and the
		// other four keep answering out of contract.
		want := map[int]struct {
			value  string
			marker string
		}{
			0: {trailGateOutOfContract, trailReasonAbsentOwesOne},
			1: {trailGateAbsentOwesNone, trailReasonAbsentOwesNone},
			2: {trailGateOutOfContract, trailReasonPathUnnamed},
			3: {trailGateOutOfContract, trailReasonPathUnnamed},
			4: {trailGateOutOfContract, trailReasonPathUnnamed},
		}
		if len(want) != len(readings) {
			t.Fatalf("the companion expects %d readings and the sweep drives %d: a reading added "+
				"to trailGateRunnerReadings() must state which value and which case it reaches",
				len(want), len(readings))
		}

		markers := trailGateAbsenceCaseMarkers()
		scan := trailGateAbsentReasonScan()
		details := make(map[string]struct{}, len(readings))

		for i, reading := range readings {
			got := trailGate(trailGateInput{Scan: scan, RunnerPath: reading})

			if got.Value != want[i].value {
				t.Errorf("reading %d (%q): the gate reads %q, want %q — this is the arm the "+
					"sweep above exempts from its value comparison, so the exemption is paid "+
					"for here, per reading. Detail: %s", i, reading, got.Value, want[i].value,
					got.Detail)
			}
			if got.Reason != "" {
				t.Errorf("reading %d (%q): the gate certifies %q, want nothing — naming which "+
					"absence fired, and giving one of them a value of its own, certifies no "+
					"reason on any reading", i, reading, got.Reason)
			}

			var named []string
			for _, m := range markers {
				if strings.Contains(got.Detail, m) {
					named = append(named, m)
				}
			}
			if len(named) != 1 || named[0] != want[i].marker {
				t.Errorf("reading %d (%q): the Detail names case(s) %q, want exactly [%s]. The "+
					"got-vs-want is the assertion: two mis-implementations reach this row and "+
					"only the wrong value each produces tells them apart. Detail: %s", i, reading,
					named, want[i].marker, got.Detail)
			}
			details[got.Detail] = struct{}{}
		}

		// Exactly three, so a degenerate arm answering one case for everything
		// cannot satisfy the markers vacuously, and a fourth distinct Detail means
		// the arm split further than the three cases it is documented to have.
		if n := len(details); n != 3 {
			t.Errorf("the absence arm produced %d distinct Details across %d readings, want 3 — "+
				"one per case, with the three indeterminate readings sharing one", n,
				len(readings))
		}
	})

	// The second companion, and the one this ticket's exemption is paid for by:
	// the usable arm DIVERTS under exactly one reading, proven positively rather
	// than left as an arm the sweep above stopped comparing.
	//
	// It carries the certified reason as a per-reading EXPECTATION, which is what
	// replaces the invariance the widened exemption withdrew — and it is the
	// stronger of the two, since it says what each reading certifies rather than
	// only that the five agree. trailClassifyRun's C2
	// (trail_run_outcome_test.go:553-564) rejects a non-certifying gate value
	// carrying a reason at a different layer again, so the property has two
	// detectors of different fabric rather than one.
	//
	// Same discipline as its neighbour: it drives the row's own fixture helper
	// rather than indexing the slice, takes the scan ONCE, and assigns only
	// RunnerPath per reading, never writing through the aliased pointer.
	t.Run("the usable arm diverts to the presence case under one reading", func(t *testing.T) {
		// Per reading, the TRIPLE: the value reached, the reason certified, and the
		// case the Detail names — "" meaning the presence marker must be ABSENT.
		//
		// Derived from the reduction rather than copied: trailFixtureTrailer carries
		// a present, non-empty terminal_reason, so trailReasonAgainstPath answers
		// trailReasonNamedOwesOne under reading 0, trailReasonPresentOwesNone under
		// reading 1 and trailReasonPathUnnamed under 2 through 4. Only the second
		// matches the presence arm's key, so only reading 1 diverts.
		want := map[int]struct {
			value  string
			reason string
			marker string
		}{
			0: {trailGateUsable, "completed", ""},
			1: {trailGatePresentOwesNone, "", trailReasonPresentOwesNone},
			2: {trailGateUsable, "completed", ""},
			3: {trailGateUsable, "completed", ""},
			4: {trailGateUsable, "completed", ""},
		}
		if len(want) != len(readings) {
			t.Fatalf("the companion expects %d readings and the sweep drives %d: a reading added "+
				"to trailGateRunnerReadings() must state which value it reaches, what it "+
				"certifies and whether it names the presence case", len(want), len(readings))
		}

		scan := trailGateUsableScan()
		details := make(map[string]struct{}, len(readings))

		for i, reading := range readings {
			got := trailGate(trailGateInput{Scan: scan, RunnerPath: reading})

			if got.Value != want[i].value {
				t.Errorf("reading %d (%q): the gate reads %q, want %q — this is the arm the sweep "+
					"above exempts, so the exemption is paid for here, per reading. Detail: %s",
					i, reading, got.Value, want[i].value, got.Detail)
			}
			if got.Reason != want[i].reason {
				t.Errorf("reading %d (%q): the gate certifies %q, want %q. This assertion is what "+
					"the widened exemption bought: the sweep above no longer compares the "+
					"certified reason on a declaring row, so a path-varying arm that quietly "+
					"started to certify — or stopped — is caught HERE and nowhere else in this "+
					"test", i, reading, got.Reason, want[i].reason)
			}
			if named := strings.Contains(got.Detail, trailReasonPresentOwesNone); named !=
				(want[i].marker != "") {
				t.Errorf("reading %d (%q): the Detail names case %s = %t, want %t. Since #1434 the "+
					"value above discriminates on its own — the diverting reading reaches %s, and "+
					"exactly one return site answers it — so this is a SECOND detector of "+
					"different fabric rather than the only one: it reads the Detail, and it is "+
					"what catches an arm that moved the value while publishing another arm's "+
					"prose. Detail: %s", i, reading, trailReasonPresentOwesNone, named,
					want[i].marker != "", trailGatePresentOwesNone, got.Detail)
			}
			details[got.Detail] = struct{}{}
		}

		// Exactly two, and this pin is what replaces the byte comparison the
		// exemption withdrew: one Detail shared by the four readings that reach the
		// usable arm — so that arm is still proven to publish ONE fixed string
		// across them — and one for the reading that diverts. A third would be an
		// arm that had started to vary where nothing says it may.
		if n := len(details); n != 2 {
			t.Errorf("the usable fixture produced %d distinct Details across %d readings, want 2 — "+
				"one for the four readings that reach the usable arm and one for the reading "+
				"that diverts to the presence arm", n, len(readings))
		}
	})
}

func TestTrailAdmitAttribution(t *testing.T) {
	// One anchored line naming the held group, classified by the real producer
	// rather than hand-built — the same reason TestTrailGate routes six rows
	// through trailScan. It pins the proof arm to a record tdnClassifyReapLog
	// actually emits.
	const heldPGID = 7788
	classified := tdnClassifyReapLog([]byte(trailReapLine(1, "[7788]")+"\n"), heldPGID)
	if classified.Verdict != tdnReapHeldPGIDKilled || classified.LineCount != 1 {
		t.Fatalf("fixture: tdnClassifyReapLog gave %s / %d line(s) (%s), want %s / 1 — the "+
			"proof row's premise is that the real producer emits this record",
			classified.Verdict, classified.LineCount, classified.Detail, tdnReapHeldPGIDKilled)
	}

	tests := []struct {
		name      string
		reap      tdnReapOutcome
		certified string
		want      string
	}{
		{
			name:      "the reaper named the group on exactly one line, and the run completed",
			reap:      classified,
			certified: "completed",
			want:      trailAdmitProof,
		},
		{
			// The ordering's regression guard. The same record that is proof
			// above must be a budget void here, or step 2 and step 3 are
			// indistinguishable.
			name:      "a budget-fired run voids a record that would otherwise be proof",
			reap:      classified,
			certified: trailBudgetTerminalReason,
			want:      trailAdmitVoidBudgetFired,
		},
		{
			// The other half of the same guard: the budget void must outrank an
			// instrument failure too, because it is structural where that one is
			// incidental.
			name: "a budget-fired run voids a broken instrument as budget-fired, not as instrument",
			reap: tdnReapOutcome{Verdict: tdnReapInstrumentFailed, HeldPGID: heldPGID,
				LineCount: 1},
			certified: trailBudgetTerminalReason,
			want:      trailAdmitVoidBudgetFired,
		},
		{
			name: "an unreadable reap line is the instrument's breakage",
			reap: tdnReapOutcome{Verdict: tdnReapInstrumentFailed, HeldPGID: heldPGID,
				LineCount: 1},
			certified: "completed",
			want:      trailAdmitVoidInstrument,
		},
		{
			name:      "no reap line at all is a void, never evidence the group had exited",
			reap:      tdnReapOutcome{Verdict: tdnReapNoLine, HeldPGID: heldPGID},
			certified: "completed",
			want:      trailAdmitVoidNoLine,
		},
		{
			name: "the reaper ran without naming the group is a void, never a negative",
			reap: tdnReapOutcome{Verdict: tdnReapHeldPGIDAbsent, HeldPGID: heldPGID,
				PGIDs: []int{4242}, Count: 1, LineCount: 1},
			certified: "completed",
			want:      trailAdmitVoidGroupUnnamed,
		},
		{
			name: "two reap lines leave it unestablished which reap named the group",
			reap: tdnReapOutcome{Verdict: tdnReapHeldPGIDKilled, HeldPGID: heldPGID,
				PGIDs: []int{heldPGID, 4242}, Count: 2, LineCount: 2},
			certified: "completed",
			want:      trailAdmitVoidNotOneReapLine,
		},
		{
			name:      "the zero outcome is out of contract",
			reap:      tdnReapOutcome{},
			certified: "completed",
			want:      trailAdmitOutOfContract,
		},
		{
			// The second parameter's contract check, in the direction that
			// matters: the same record that is proof at the top of this table
			// must not be proof when nothing certified the run.
			name:      "a record that would otherwise be proof is out of contract with no certified reason",
			reap:      classified,
			certified: "",
			want:      trailAdmitOutOfContract,
		},
		{
			name:      "a verdict nobody defined is out of contract",
			reap:      tdnReapOutcome{Verdict: "some-verdict-nobody-defined", LineCount: 1},
			certified: "completed",
			want:      trailAdmitOutOfContract,
		},
		{
			// A pair the producer cannot emit: no-line is reached only with a
			// zero count. Without the check it would report a void describing
			// lines it never saw.
			name:      "no-reap-line carrying a line count is out of contract",
			reap:      tdnReapOutcome{Verdict: tdnReapNoLine, HeldPGID: heldPGID, LineCount: 3},
			certified: "completed",
			want:      trailAdmitOutOfContract,
		},
		{
			// The mirror image: killed is reached only after a line is counted.
			name: "a killed verdict carrying no line count is out of contract",
			reap: tdnReapOutcome{Verdict: tdnReapHeldPGIDKilled, HeldPGID: heldPGID,
				PGIDs: []int{heldPGID}},
			certified: "completed",
			want:      trailAdmitOutOfContract,
		},
		{
			name: "an absent verdict carrying no line count is out of contract",
			reap: tdnReapOutcome{Verdict: tdnReapHeldPGIDAbsent, HeldPGID: heldPGID,
				PGIDs: []int{4242}},
			certified: "completed",
			want:      trailAdmitOutOfContract,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := trailAdmitAttribution(tc.reap, tc.certified)

			if got.Value != tc.want {
				t.Fatalf("value: got %q (%s), want %q", got.Value, got.Detail, tc.want)
			}
			if !trailIsAdmitValue(got.Value) {
				t.Errorf("value: %q is outside the recorded admissibility space", got.Value)
			}
			if got.Detail == "" {
				t.Error("empty detail: a void that cannot say which arm fired is indistinguishable " +
					"from a reading")
			}
		})
	}

	t.Run("a group not named says so in those words", func(t *testing.T) {
		// The asymmetry is the claim's whole load, so the void that is most
		// easily misread as a negative has to say what it is not.
		got := trailAdmitAttribution(tdnReapOutcome{Verdict: tdnReapHeldPGIDAbsent,
			HeldPGID: 7788, PGIDs: []int{4242}, Count: 1, LineCount: 1}, "completed")
		if !strings.Contains(got.Detail, "NEVER EVIDENCE IT HAD EXITED") {
			t.Errorf("detail: got %q, want it to say a group not named is never evidence it had "+
				"exited — a miss reported as a negative inverts the asymmetry the claim rests on",
				got.Detail)
		}
	})
}

// TestTrailGateThenAdmit is AC4's composition: the predicate is reachable ONLY
// through a terminal reason the gate certified, and the budget arm is reached
// through a real gate result rather than a hand-typed string.
func TestTrailGateThenAdmit(t *testing.T) {
	// A record that is proof on its own, so any non-proof outcome below is
	// attributable to the certified reason and to nothing else.
	admissible := tdnReapOutcome{Verdict: tdnReapHeldPGIDKilled, HeldPGID: 7788,
		PGIDs: []int{7788}, Count: 1, LineCount: 1}

	calls := 0
	reached := make(map[string]string)

	for _, tc := range trailGateCases() {
		t.Run(tc.name, func(t *testing.T) {
			gate := trailGate(tc.in)

			if gate.Reason == "" {
				// The values that certify nothing must never reach the predicate:
				// there would be no certified reason to hand it, and passing ""
				// would let an uncertified run be judged as if the gate had
				// approved it. FIVE since #1434 added trailGatePresentOwesNone
				// beside #1417's trailGateAbsentOwesNone.
				//
				// No row of trailGateCases() reaches EITHER of those two — every row
				// carries trailRunnerUnread(), which names no runner, and both are
				// keyed on a reading that does — so those two arms are DEFENSIVE
				// rather than exercised. They are here because the day a row does
				// reach one, this sweep must widen rather than fatal on a value the
				// gate legitimately emits.
				switch gate.Value {
				case trailGateNoTrailer, trailGateScanAborted, trailGateAbsentOwesNone,
					trailGatePresentOwesNone, trailGateOutOfContract:
				default:
					t.Fatalf("value %s certifies no reason, but it is not one of %s / %s / %s / "+
						"%s / %s", gate.Value, trailGateNoTrailer, trailGateScanAborted,
						trailGateAbsentOwesNone, trailGatePresentOwesNone, trailGateOutOfContract)
				}
				return
			}

			calls++
			admit := trailAdmitAttribution(admissible, gate.Reason)
			reached[gate.Value] = admit.Value
		})
	}

	if calls != 2 {
		t.Errorf("the predicate was invoked %d time(s) across the sweep, want 2 — exactly %s and "+
			"%s certify a reason", calls, trailGateUsable, trailGateBudgetFired)
	}
	if got := reached[trailGateUsable]; got != trailAdmitProof {
		t.Errorf("%s composed to %q, want %q", trailGateUsable, got, trailAdmitProof)
	}
	// AC4's point: this arm is LIVE rather than unreachable-by-construction,
	// because the string driving it came out of a real gate result over a real
	// scan of a max_turns trailer.
	if got := reached[trailGateBudgetFired]; got != trailAdmitVoidBudgetFired {
		t.Errorf("%s composed to %q, want %q — the budget void must be reached through a gate "+
			"result, not a hand-typed reason", trailGateBudgetFired, got, trailAdmitVoidBudgetFired)
	}
}

// TestTrailAdmissibilityRecordsCarryNoCapturedBytes makes AC5's
// operator-review-before-paste obligation checkable rather than advisory.
//
// trailScanResult.Line is verbatim model output and tdnReapOutcome.Line is
// pyry's own stderr; both are marked operator-review-before-paste, and copying
// either would propagate that obligation onto records whose whole value is that
// they can be published unreviewed. Both assertions are deterministic: the
// needle can only reach either record by a field copy or by a Detail quoting the
// input's captured string, and neither record does either.
//
// # The key NAMES are a third surface, and the first two rungs cannot see it
//
// #1419's arms decide on trailScanResult.KeyNames, so the most natural Detail
// for the absence arm is one saying which keys the line DID carry. Those names
// come from claude, they are deliberately unbounded at their own tier
// (trailer_key_names_test.go:55-59 defers the per-name cap to #1363 BECAUSE
// trailScanResult is published by nothing), and trailGateResult IS published.
//
// Two separate things stop the rung above from covering it. Its input leaves
// KeyNames nil and carries TerminalReason "completed", so it reaches
// trailGateUsable and neither #1419 arm — a Detail interpolating a name would
// pass it untouched. And the shipped rule "no VALUE from the trailer enters the
// record" does not forbid it either: A KEY NAME IS NOT A VALUE, which is exactly
// the distinction trailKeyNames was built on. The third sub-test is therefore a
// rung of its own rather than a widening of the first, whose premise is a
// well-formed input reaching the usable arm.
func TestTrailAdmissibilityRecordsCarryNoCapturedBytes(t *testing.T) {
	t.Run("the gate result carries nothing from the scanned line", func(t *testing.T) {
		got := trailGate(trailGateInput{
			Scan: trailScanResult{
				State:   trailSeen,
				Line:    `{"type":"result","result":"` + trailNeedle + `"}`,
				Trailer: &resultTrailer{Type: "result", TerminalReason: "completed"},
				Detail:  "a scan detail that also carries " + trailNeedle,
			},
			// One of tdnRunnerFromArgv's constant answers, never argv. The
			// marshalled result now carries runner_path, and this row is what keeps
			// the needle sweep honest over it.
			RunnerPath: trailRunnerUnread(),
		})

		if got.Value != trailGateUsable {
			t.Fatalf("value: got %q (%s), want %q — the premise is a well-formed input whose "+
				"captured strings carry the needle", got.Value, got.Detail, trailGateUsable)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshalling the gate result: %v", err)
		}
		if bytes.Contains(encoded, []byte(trailNeedle)) {
			t.Errorf("the marshalled gate result carries verbatim model output: %s", encoded)
		}
	})

	t.Run("the admissibility result carries nothing from the reap line", func(t *testing.T) {
		got := trailAdmitAttribution(tdnReapOutcome{
			Verdict:   tdnReapHeldPGIDKilled,
			Detail:    "a classifier detail that also carries " + trailNeedle,
			HeldPGID:  7788,
			PGIDs:     []int{7788},
			Count:     1,
			LineCount: 1,
			Line:      "reaped groups, and " + trailNeedle,
		}, "completed")

		if got.Value != trailAdmitProof {
			t.Fatalf("value: got %q (%s), want %q — the premise is an admissible input whose "+
				"captured strings carry the needle", got.Value, got.Detail, trailAdmitProof)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshalling the admissibility result: %v", err)
		}
		if bytes.Contains(encoded, []byte(trailNeedle)) {
			t.Errorf("the marshalled admissibility result carries pyry's captured stderr: %s",
				encoded)
		}
	})

	t.Run("the gate result carries no key name from the line", func(t *testing.T) {
		// Both rows are hand-built rather than scan-produced, deliberately: the
		// needle has to BE a key name, and trailScan reads the names off real
		// JSON. That is admissible here because the rows assert nothing about
		// what the producer emits — only about what the gate renders from what it
		// is handed. The reachability of the arms is proven from the
		// scan-produced fixtures in TestTrailGate.
		//
		// # The reading is declared PER ROW, and the third row is why
		//
		// The two shipped rows both drive trailRunnerUnread(), so the absent-key
		// one reaches the PATH-UNNAMED fall-through and would go on passing
		// without ever running the arm #1417 added. The third row plants the same
		// needle at a reading that reduces to streamrunner, where the new arm is
		// the only site that can fire, and its value is the unique precondition
		// for it.
		//
		// # The DECODED REASON is declared per row too, and the fourth row is why
		//
		// The three rows above all hand a trailer whose TerminalReason is "", so
		// every one of them routes into the empty-reason branch and NO row could
		// reach the presence arm this ticket added, whatever its reading. The
		// fourth row carries a decoded reason and the key name that makes the
		// reduction read PRESENT, at a streamrunner reading, which is the only
		// combination that site fires on.
		//
		// That reason is a plain source literal and DELIBERATELY NOT trailNeedle.
		// The needle's job on these rows is the KEY-NAME channel; planted in the
		// decoded reason instead it would exercise a channel the shipped usable
		// arm legitimately publishes — trailGateResult.Reason — so a mutant that
		// made the presence arm certify would redden this sweep rather than the
		// Reason assertion in
		// TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone, and lose its sole
		// red.
		//
		// Because reachCapCommand TRUNCATES AND MARKS rather than failing, the two
		// OUTPUT assertions below are load-bearing rather than belt-and-braces: a
		// Detail cut at the cap could have lost the needle in the cut rather than
		// by never quoting it, and the sweep would read clean for a reason it does
		// not claim. The margin is thin by construction — #1417's arm publishes
		// 464 B and this ticket's presence arm 427 B against the 512 B cap, with
		// trailNeedle at 42 B — so a reword of either goes red HERE rather than
		// shipping a vacuous sweep.
		tests := []struct {
			name     string
			keyNames []string
			// reason is the decoded terminal_reason the hand-built trailer carries,
			// declared per row for the fourth row's sake: "" routes into the
			// empty-reason branch, and only a non-empty one can reach the arm past
			// it. Never trailNeedle — see the paragraph above.
			reason string
			// reading is declared per row rather than shared, which is what makes
			// the third row reach a different arm from the first on an identical
			// key-name plant.
			reading string
			want    string
			marker  string
		}{
			{
				name: "the absent-key arm under a reading that names no runner",
				// No trailReasonKeyName, so the arm decides ABSENT.
				keyNames: []string{"result", trailNeedle, "type"},
				reading:  trailRunnerUnread(),
				want:     trailGateOutOfContract,
				marker:   "NO terminal_reason key on the line",
			},
			{
				name:     "the present-and-empty arm",
				keyNames: []string{"result", trailNeedle, trailReasonKeyName, "type"},
				reading:  trailRunnerUnread(),
				want:     trailGateOutOfContract,
				marker:   "terminal_reason is empty",
			},
			{
				// #1417's arm, reached on the same plant as the first row and
				// separated from it by the reading alone.
				name:     "the absent-key arm on a path that owes no reason",
				keyNames: []string{"result", trailNeedle, "type"},
				reading:  tdnRunnerFromArgv(tdnFixtureStreamArgv),
				want:     trailGateAbsentOwesNone,
				marker:   trailReasonAbsentOwesNone,
			},
			{
				// #1433's arm, whose value is #1434's. The key-name plant is the
				// same needle again, joined by trailReasonKeyName so the reduction
				// reads PRESENT, and the decoded reason is a source literal that is
				// not the needle.
				name:     "the presence arm on a path that owes no reason",
				keyNames: []string{"result", trailNeedle, trailReasonKeyName, "type"},
				reason:   "completed",
				reading:  tdnRunnerFromArgv(tdnFixtureStreamArgv),
				want:     trailGatePresentOwesNone,
				marker:   trailReasonPresentOwesNone,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := trailGate(trailGateInput{
					Scan: trailScanResult{
						State:    trailSeen,
						Line:     `{"type":"result","` + trailNeedle + `":1}`,
						Trailer:  &resultTrailer{Type: "result", TerminalReason: tc.reason},
						KeyNames: tc.keyNames,
						Detail:   "a scan detail that also carries " + trailNeedle,
					},
					RunnerPath: tc.reading,
				})

				// THE NON-VACUITY PRECONDITION, in two parts. The value alone is
				// not enough on the two out-of-contract rows: five of trailGate's
				// eleven return sites answer it, so a row that reached a different
				// one would sweep the wrong Detail and report clean about an arm it
				// never ran. Both counts moved at #1434, which took the presence arm
				// out of that value — the fourth row's want is this ticket's own
				// value, and its value assertion is exact.
				if got.Value != tc.want {
					t.Fatalf("value: got %q (%s), want %q", got.Value, got.Detail, tc.want)
				}
				if !strings.Contains(got.Detail, tc.marker) {
					t.Fatalf("detail: got %q, want it to carry %q — this row must reach the arm it "+
						"is named for, or the sweep below says nothing about that arm",
						got.Detail, tc.marker)
				}
				// ON THE OUTPUT, because the cap truncates rather than fails.
				if strings.Contains(got.Detail, reachTruncationMarker) {
					t.Fatalf("the Detail was TRUNCATED at %d bytes, so the sweep below cannot tell "+
						"a needle this arm never quoted from one the cap removed: %s",
						reachMaxCommandBytes, got.Detail)
				}
				if n := len(got.Detail); n+len(trailNeedle) > reachMaxCommandBytes {
					t.Fatalf("the Detail is %d bytes and the needle is %d, so the two do not fit "+
						"inside the %d byte cap: had this arm leaked the needle it would have been "+
						"truncated away and the sweep below would be VACUOUS. Rewrite this arm's "+
						"prose rather than growing it", n, len(trailNeedle), reachMaxCommandBytes)
				}

				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatalf("marshalling the gate result: %v", err)
				}
				if bytes.Contains(encoded, []byte(trailNeedle)) {
					t.Errorf("the marshalled gate result carries a key name read off claude's "+
						"line: %s — the names are attacker-influenced in principle and unbounded "+
						"at this tier, and every arm's Detail must stay fixed prose over this "+
						"file's own constants and file cites", encoded)
				}
			})
		}
	})
}
