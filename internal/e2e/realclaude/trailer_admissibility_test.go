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

// What a trailer scan result can support, as a POSITIVE ALLOWLIST of five.
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
	// trailGateOutOfContract: the input is not a reading. Reporting any of its
	// four sub-cases as absent would file a caller's bug under "pyry never
	// finished the turn". Four rather than three since #1419: the empty
	// terminal_reason split into the key being ABSENT from the line and the key
	// being PRESENT AND BLANK, which are one decoded "" and two different
	// records. Same value, same uncertified reason — only the Detail says which.
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
// # No arm reads it, in this slice
//
// #1373 carries the reading to the gate and changes no arm; the decision that
// consults it lands in #1374, where trailReasonAgainstPath
// (trailer_terminal_reason_test.go:215) stops having only its own tests for
// callers. TestTrailGateIgnoresTheRunnerPath is what makes "no arm reads it" a
// proof rather than a claim.
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
	// on the two arms that certify (usable and budget-fired), empty on the other
	// three. A certification that could certify "" would reintroduce, one layer
	// up, the exact defect the nil Trailer pointer was chosen to prevent.
	Reason string `json:"terminal_reason,omitempty"`
	Detail string `json:"detail"`
	// RunnerPath is the reading the gate was HANDED, copied out unread. No arm
	// consults it; it is here so that the reading which reached a PURE function is
	// observable from outside it, which is the only channel a later ticket's
	// composition test can assert against.
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
	// "". Because no arm reads the field, an unfilled one cannot misroute a
	// decision and the worst case is a published record whose runner_path is
	// absent, which reads as "not recorded". What "" MEANS at a decision is
	// #1374's question — that is the ticket where an arm first depends on it, and
	// it inherits the question from here rather than rediscovering it.
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
// # The runner path is CARRIED, never read
//
// in.RunnerPath reaches every return site and no branch. Not one of the eight
// arms consults it, and not one Detail interpolates it: every Detail here is
// fixed prose over this file's own constants and the scan's own state, which is
// the doctrine trailReasonAgainstPath states for itself at
// trailer_terminal_reason_test.go:201-214. Where that function holds the
// guarantee by review of its source, this one holds it by
// TestTrailGateIgnoresTheRunnerPath, which drives every fixture row under each of
// tdnRunnerFromArgv's five distinct answers and requires a BYTE-IDENTICAL Detail
// across all five.
//
// The reading is echoed onto the result unread, so the arriving value stays
// observable from outside a pure function. #1374 is where an arm first reads it.
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
		//     landed to prevent (trailer_terminal_reason_test.go:196-199), and
		//     inside this block it is always false — so it would route every input
		//     to the absence arm SILENTLY.
		//   - NEVER len(KeyNames) > 0. A scan-produced absence carries the six
		//     names the line did have, merely missing this one, so cardinality
		//     reads it as PRESENT.
		//
		// Both arms answer trailGateOutOfContract and certify nothing, so no closed
		// set grows and no consumer gains an arm; what is added is WHICH SHAPE
		// ARRIVED. Neither reads in.RunnerPath, and neither Detail interpolates a
		// key name — see the no-echo argument at trailGateResult and the check at
		// TestTrailAdmissibilityRecordsCarryNoCapturedBytes.
		if !slices.Contains(in.Scan.KeyNames, trailReasonKeyName) {
			return trailGateResult{
				Value: trailGateOutOfContract,
				Detail: trailDetail("state %s carries a trailer with NO terminal_reason key on the "+
					"line. Nothing is certified. Unlike a present-and-empty one this shape HAS a "+
					"live repro: on PYRY_USE_STREAMJSON=1 streamrunner passes claude's bytes "+
					"through unchanged (runner.go:177-179) and synthesises one only when the "+
					"idle-stall watchdog fired and claude emitted no result (:250-253), so a "+
					"healthy run's trailer is claude's own result line. WHICH absence — owed or "+
					"not owed — is NOT DECIDED AT THIS ARM", trailSeen),
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
// force this function to answer for the three gate values that certify nothing
// and grow an eighth outcome, which is the exact collapse the ticket refuses.
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
		trailGateBudgetFired, trailGateOutOfContract:
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
	// by #1373, and it is the shape #1374 needs, since the decision it adds
	// distinguishes rows BY path.
	in   trailGateInput
	want string
	// reason is the terminal reason the gate must certify: non-empty exactly on
	// trailGateUsable and trailGateBudgetFired.
	reason string
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
// drift apart. #1374 replaces the gathers' use of it with the reading derived
// from each gather's own argv scan; the fixture rows keep it.
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

// trailGateCases returns every gate input under test. The six rows that can be
// produced by the real scan go through trailScan rather than a hand-built
// record, so the reachable arms stay pinned to what the shipped producer
// actually emits; the three that trailScan CANNOT emit are hand-built, because
// that is precisely what the contract checks exist for.
//
// Every row carries the same runner path, because #1373 adds no arm that reads
// one: a row varying it would suggest a distinction the gate does not make. The
// sweep that VARIES it is TestTrailGateIgnoresTheRunnerPath, which drives these
// same nine rows under all five readings.
func trailGateCases() []trailGateCase {
	return []trailGateCase{
		{
			name: "an ordinary trailer is usable and certifies its reason",
			in: trailGateInput{Scan: trailScan([]byte(trailFixtureTrailer + "\n")),
				RunnerPath: trailRunnerUnread()},
			want:   trailGateUsable,
			reason: "completed",
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
			// TestTrailGateIgnoresTheRunnerPath TOTAL: after the repair above no
			// other row is absence-shaped, so without this one the absence arm is
			// the single return site no fixture reaches and an arm that forgot
			// RunnerPath: in.RunnerPath there could hide behind the seven that
			// carried it. It carries trailRunnerUnread() like every other row,
			// which is what keeps trail_ptyrunner_composition_test.go:38's
			// totality claim true and untouched.
			name: "a seen state whose terminal_reason is absent from the line is out of contract",
			in: trailGateInput{Scan: trailGateAbsentReasonScan(),
				RunnerPath: trailRunnerUnread()},
			want: trailGateOutOfContract,
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
// mean. One union map over all twenty-nine constants covers within-space,
// cross-space and against-shipped distinctness in a single loop.
//
// #1271's eleven run-level outcomes joined the map rather than starting a third
// closure test, for the same reason: three spaces now mean nearly the same words
// (an input state, the gate's view of it, and the run's view of it), and only a
// union can see a copy-paste across them.
//
// EVERY VALUE IN THIS MAP HAS AN ARM IN ITS CONSUMER — trailGate and
// trailAdmitAttribution for the first two spaces, trailClassifyRun for the
// third, trailReasonAgainstPath (trailer_terminal_reason_test.go:215) for the
// fourth. That is a comment and not a check: this test catches a COLLIDING
// value, never an UNHANDLED one, so a sixth gate or admit value added here and
// to its membership predicate would pass trailClassifyRun's contract block and
// then find no arm. Recorded in #1271's spec § Open questions Q2; if the value
// spaces ever grow, this is the first thing to revisit. #1366's fourth space
// closes that gap for itself rather than here: TestTrailReasonAgainstPath
// asserts the set of values its nine rows REACH is exactly the six, which is the
// unhandled-value check this map cannot make.
func TestTrailAdmissibilityConstantsAreClosed(t *testing.T) {
	all := map[string]string{
		// This ticket's gate values.
		"trailGateUsable":        trailGateUsable,
		"trailGateNoTrailer":     trailGateNoTrailer,
		"trailGateScanAborted":   trailGateScanAborted,
		"trailGateBudgetFired":   trailGateBudgetFired,
		"trailGateOutOfContract": trailGateOutOfContract,
		// This ticket's admissibility values.
		"trailAdmitProof":              trailAdmitProof,
		"trailAdmitVoidBudgetFired":    trailAdmitVoidBudgetFired,
		"trailAdmitVoidInstrument":     trailAdmitVoidInstrument,
		"trailAdmitVoidNoLine":         trailAdmitVoidNoLine,
		"trailAdmitVoidGroupUnnamed":   trailAdmitVoidGroupUnnamed,
		"trailAdmitVoidNotOneReapLine": trailAdmitVoidNotOneReapLine,
		"trailAdmitOutOfContract":      trailAdmitOutOfContract,
		// #1271's run-level outcomes: three answers and eight named voids.
		"trailOutcomeRunningAtTrailer":       trailOutcomeRunningAtTrailer,
		"trailOutcomeMatchedUnattributed":    trailOutcomeMatchedUnattributed,
		"trailOutcomeNoRowMatched":           trailOutcomeNoRowMatched,
		"trailOutcomeVoidBudgetFired":        trailOutcomeVoidBudgetFired,
		"trailOutcomeVoidNoTrailer":          trailOutcomeVoidNoTrailer,
		"trailOutcomeVoidTrailerScanAborted": trailOutcomeVoidTrailerScanAborted,
		"trailOutcomeVoidPyryDidNotExit":     trailOutcomeVoidPyryDidNotExit,
		"trailOutcomeVoidArgvScanErrored":    trailOutcomeVoidArgvScanErrored,
		"trailOutcomeVoidNoRowsParsed":       trailOutcomeVoidNoRowsParsed,
		"trailOutcomeVoidLivenessInstrument": trailOutcomeVoidLivenessInstrument,
		"trailOutcomeOutOfContract":          trailOutcomeOutOfContract,
		// #1366's terminal-reason-against-path values.
		"trailReasonAbsentOwesNone":  trailReasonAbsentOwesNone,
		"trailReasonPresentOwesNone": trailReasonPresentOwesNone,
		"trailReasonAbsentOwesOne":   trailReasonAbsentOwesOne,
		"trailReasonBlankOwesOne":    trailReasonBlankOwesOne,
		"trailReasonNamedOwesOne":    trailReasonNamedOwesOne,
		"trailReasonPathUnnamed":     trailReasonPathUnnamed,
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
	}
	if zeroGate.Reason != "" {
		t.Errorf("the zero trailGateResult certifies %q — an uncertified record must never read "+
			"as certified", zeroGate.Reason)
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
		// Four inputs reach one value, so without this the four arms are
		// indistinguishable in a published record: a reader cannot tell a nil
		// pointer from a state nobody defined, nor — since #1419 — a
		// terminal_reason that is ON the line and blank from one that is not on
		// the line at all. The last two decode identically to "", so the Detail
		// is the ONLY channel that says which record arrived.
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
		if strings.Contains(emptyReason.Detail, "NOT DECIDED AT THIS ARM") {
			t.Errorf("empty-reason detail: got %q, want it NOT to carry the absence arm's marker "+
				"— this input's terminal_reason IS on the line, and reaching the absence arm "+
				"would publish the wrong record about it", emptyReason.Detail)
		}
		absentReason := trailGate(trailGateInput{Scan: trailGateAbsentReasonScan(),
			RunnerPath: trailRunnerUnread()})
		if !strings.Contains(absentReason.Detail, "NO terminal_reason key on the line") {
			t.Errorf("absent-reason detail: got %q, want it to name the key as off the line",
				absentReason.Detail)
		}
		// The tail phrase of that Detail, so an overlong reword goes red HERE
		// rather than shipping a record reachCapCommand silently truncated.
		if !strings.Contains(absentReason.Detail, "NOT DECIDED AT THIS ARM") {
			t.Errorf("absent-reason detail: got %q, want it to say WHICH absence is not decided "+
				"here — a path that owes a terminal_reason and one that owes none are opposite "+
				"readings, and this arm sees only the scan", absentReason.Detail)
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

// TestTrailGateIgnoresTheRunnerPath is AC2 made deterministic: #1373 carries the
// runner path to the gate and NO ARM READS IT. The decision — value, certified
// reason and Detail — must be identical across every reading, so that #1374's
// red, when it lands, is a statement about the gate's arms rather than about a
// signature.
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
//     thing the ticket had to correct from the shipped "three".
//   - The arriving reading is READ BACK from outside the gate. A sweep that built
//     its input without filling the path would otherwise pass forty-five identical
//     comparisons while proving nothing about carriage. This assertion is TOTAL
//     over the gate's eight return sites — the nine rows reach all eight — so an
//     arm that forgot to carry the field cannot hide behind an arm that did.
//
// Totality is restored by COVERAGE rather than narrowed. #1419 split the empty
// terminal_reason into an absent-key arm and a present-and-empty one, and after
// its fixture repair no other row is absence-shaped — trailFixtureTrailer and
// trailPaddedTrailer both carry a named terminal_reason and the other five rows
// return before the reason check. The absence arm would therefore have been the
// one return site no row reached, so #1419 added the row that reaches it rather
// than softening this clause to "reach most of".
//
// # The copies alias one *resultTrailer, deliberately not mutated
//
// Varying the reading copies each row's trailGateInput, and a struct copy copies
// the POINTER: all five inputs for a row share one resultTrailer, the same
// aliasing trailRunWellFormed's doc warns about (trail_run_outcome_test.go:610).
// Only RunnerPath is ever assigned and nothing is ever written through the
// pointer, which is what keeps that sharing race-free — and it is what would
// have to hold load-bearingly if these subtests ever took t.Parallel().
func TestTrailGateIgnoresTheRunnerPath(t *testing.T) {
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

				if got.Value != base.Value || got.Reason != base.Reason {
					t.Errorf("the decision differs across readings: under %q the gate reads %q "+
						"certifying %q, under %q it reads %q certifying %q — no arm may consult "+
						"the runner path in this slice", readings[0], base.Value, base.Reason,
						reading, got.Value, got.Reason)
				}
				if !bytes.Equal([]byte(got.Detail), []byte(base.Detail)) {
					t.Errorf("the Detail differs across readings, byte for byte:\n under %q: %s\n"+
						" under %q: %s\na Detail that acquired the reading would pass a value "+
						"check while changing what the published record says",
						readings[0], base.Detail, reading, got.Detail)
				}
			}
		})
	}
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
				// The three values that certify nothing must never reach the
				// predicate: there would be no certified reason to hand it, and
				// passing "" would let an uncertified run be judged as if the
				// gate had approved it.
				switch gate.Value {
				case trailGateNoTrailer, trailGateScanAborted, trailGateOutOfContract:
				default:
					t.Fatalf("value %s certifies no reason, but it is not one of %s / %s / %s",
						gate.Value, trailGateNoTrailer, trailGateScanAborted,
						trailGateOutOfContract)
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
		// is handed. The reachability of the two arms is proven from the
		// scan-produced fixtures in TestTrailGate.
		tests := []struct {
			name     string
			keyNames []string
			want     string
			marker   string
		}{
			{
				name: "the absent-key arm",
				// No trailReasonKeyName, so the arm decides ABSENT.
				keyNames: []string{"result", trailNeedle, "type"},
				want:     trailGateOutOfContract,
				marker:   "NO terminal_reason key on the line",
			},
			{
				name:     "the present-and-empty arm",
				keyNames: []string{"result", trailNeedle, trailReasonKeyName, "type"},
				want:     trailGateOutOfContract,
				marker:   "terminal_reason is empty",
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := trailGate(trailGateInput{
					Scan: trailScanResult{
						State:    trailSeen,
						Line:     `{"type":"result","` + trailNeedle + `":1}`,
						Trailer:  &resultTrailer{Type: "result"},
						KeyNames: tc.keyNames,
						Detail:   "a scan detail that also carries " + trailNeedle,
					},
					RunnerPath: trailRunnerUnread(),
				})

				// THE NON-VACUITY PRECONDITION, in two parts. The value alone is
				// not enough: four arms answer trailGateOutOfContract, so a row
				// that reached a different one would sweep the wrong Detail and
				// report clean about an arm it never ran.
				if got.Value != tc.want {
					t.Fatalf("value: got %q (%s), want %q", got.Value, got.Detail, tc.want)
				}
				if !strings.Contains(got.Detail, tc.marker) {
					t.Fatalf("detail: got %q, want it to carry %q — this row must reach the arm it "+
						"is named for, or the sweep below says nothing about that arm",
						got.Detail, tc.marker)
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
