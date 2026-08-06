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
	// three sub-cases as absent would file a caller's bug under "pyry never
	// finished the turn".
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
func trailGate(res trailScanResult) trailGateResult {
	// Contract, first: a State outside the three trailScan documents is not a
	// reading. Catches the zero trailScanResult, whose State is "".
	if res.State != trailSeen && res.State != trailAbsent && res.State != trailAborted {
		return trailGateResult{
			Value: trailGateOutOfContract,
			Detail: trailDetail("state %q is not one of the three trailScan documents (%s / %s / "+
				"%s), so this record is not a reading. Reported out of contract rather than as an "+
				"absence, which would file a caller's bug under \"pyry never finished the turn\"",
				res.State, trailSeen, trailAbsent, trailAborted),
		}
	}

	// The two states that carry no trailer. Neither touches Trailer.
	switch res.State {
	case trailAbsent:
		return trailGateResult{
			Value: trailGateNoTrailer,
			Detail: trailDetail("the scan read every line cleanly and none was a trailer, so no "+
				"terminal reason exists to certify and no claim may rest on this run's trailer. "+
				"Distinct from %s: this is a statement about the bytes, that one is the "+
				"instrument reporting it could not read them", trailGateScanAborted),
		}
	case trailAborted:
		return trailGateResult{
			Value: trailGateScanAborted,
			Detail: trailDetail("the trailer scan aborted, so the bytes were unreadable. This is "+
				"the instrument's own breakage and never an answer about pyry, which is why it is "+
				"kept apart from %s — a line past bufio.Scanner's 64 KiB default and a genuine "+
				"absence are otherwise indistinguishable", trailGateNoTrailer),
		}
	}

	// trailSeen from here. Contract again, before anything reads through the
	// pointer: trailScan sets Trailer on its trailSeen return and on no other
	// (result_trailer_observation_test.go:180-233), so a nil here is a
	// hand-built record, not something the producer can emit.
	if res.Trailer == nil {
		return trailGateResult{
			Value: trailGateOutOfContract,
			Detail: trailDetail("state %s carries a nil trailer, a record trailScan cannot emit. "+
				"Reported out of contract because both alternatives are worse: calling it usable "+
				"would dereference nil, and calling it absent would contradict the state the "+
				"record itself claims", trailSeen),
		}
	}

	reason := res.Trailer.TerminalReason
	if reason == "" {
		return trailGateResult{
			Value: trailGateOutOfContract,
			Detail: trailDetail("state %s carries a trailer whose terminal_reason is empty. "+
				"Certifying it would reintroduce, one layer up, the defect the nil Trailer "+
				"pointer was chosen to prevent. Today's pyry cannot render a blank one — "+
				"emitter.go:383-391 is a chokepoint substituting the recorded detail or "+
				"\"unclassified\" before marshalling — so this is a contract check on a "+
				"hand-built input and NO LIVE REPRO EXISTS", trailSeen),
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
		}
	}

	return trailGateResult{
		Value:  trailGateUsable,
		Reason: reason,
		Detail: trailDetail("the trailer is usable and carries terminal_reason %q, which is not "+
			"%q — so emitter.Close() wrote the trailer before the reap defer on this path "+
			"(runner.go:479-485, :398) and a reap-log attribution can be proof", reason,
			trailBudgetTerminalReason),
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
	in   trailScanResult
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

// trailGateCases returns every gate input under test. The four rows that can be
// produced by the real scan go through trailScan rather than a hand-built
// record, so the reachable arms stay pinned to what the shipped producer
// actually emits; the four that trailScan CANNOT emit are hand-built, because
// that is precisely what the contract checks exist for.
func trailGateCases() []trailGateCase {
	return []trailGateCase{
		{
			name:   "an ordinary trailer is usable and certifies its reason",
			in:     trailScan([]byte(trailFixtureTrailer + "\n")),
			want:   trailGateUsable,
			reason: "completed",
		},
		{
			name:   "a max_turns trailer is budget-fired and still certifies its reason",
			in:     trailScan([]byte(trailPaddedTrailer(2000) + "\n")),
			want:   trailGateBudgetFired,
			reason: trailBudgetTerminalReason,
		},
		{
			name: "ordinary stream-json with no trailer certifies nothing",
			in:   trailScan([]byte(trailFixtureNoTrailer)),
			want: trailGateNoTrailer,
		},
		{
			name: "a line past bufio.Scanner's default aborts the scan and certifies nothing",
			in:   trailScan([]byte(trailPaddedTrailer(trailOverlongPad) + "\n")),
			want: trailGateScanAborted,
		},
		{
			name: "a state nobody defined is out of contract, never absent",
			in:   trailScanResult{State: "some-state-nobody-defined"},
			want: trailGateOutOfContract,
		},
		{
			// Named apart from the row above because the zero value is the
			// realistic accident, not an invented string.
			name: "the zero scan result is out of contract",
			in:   trailScanResult{},
			want: trailGateOutOfContract,
		},
		{
			// AC2's headline: the pointer trap's first consumer, handed the trap.
			// Reaching a value here at all is the assertion — a panic fails the
			// test by escaping the subtest.
			name: "a seen state carrying a nil trailer is out of contract and does not panic",
			in:   trailScanResult{State: trailSeen, Trailer: nil},
			want: trailGateOutOfContract,
		},
		{
			name: "a seen state whose terminal_reason is empty is out of contract",
			in:   trailScanResult{State: trailSeen, Trailer: &resultTrailer{Type: "result"}},
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
// third. That is a comment and not a check: this test catches a COLLIDING value,
// never an UNHANDLED one, so a sixth gate or admit value added here and to its
// membership predicate would pass trailClassifyRun's contract block and then find
// no arm. Recorded in #1271's spec § Open questions Q2; if the value spaces ever
// grow, this is the first thing to revisit.
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
		// Three inputs reach one value, so without this the three arms are
		// indistinguishable in a published record and a reader cannot tell a
		// nil pointer from an unfilled reason.
		nilTrailer := trailGate(trailScanResult{State: trailSeen})
		if !strings.Contains(nilTrailer.Detail, "nil trailer") {
			t.Errorf("nil-trailer detail: got %q, want it to name the nil trailer",
				nilTrailer.Detail)
		}
		emptyReason := trailGate(trailScanResult{State: trailSeen, Trailer: &resultTrailer{}})
		if !strings.Contains(emptyReason.Detail, "terminal_reason is empty") {
			t.Errorf("empty-reason detail: got %q, want it to name the empty terminal reason",
				emptyReason.Detail)
		}
		if !strings.Contains(emptyReason.Detail, "NO LIVE REPRO EXISTS") {
			t.Errorf("empty-reason detail: got %q, want it to say no live repro exists — pyry "+
				"cannot render a blank terminal_reason today, and a detail implying it can "+
				"would send a reader hunting for a run that does not exist", emptyReason.Detail)
		}
		unknown := trailGate(trailScanResult{State: "some-state-nobody-defined"})
		if !strings.Contains(unknown.Detail, "some-state-nobody-defined") {
			t.Errorf("unknown-state detail: got %q, want it to quote the state it rejected",
				unknown.Detail)
		}
	})
}

func TestTrailAdmitAttribution(t *testing.T) {
	// One anchored line naming the held group, classified by the real producer
	// rather than hand-built — the same reason TestTrailGate routes four rows
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
func TestTrailAdmissibilityRecordsCarryNoCapturedBytes(t *testing.T) {
	t.Run("the gate result carries nothing from the scanned line", func(t *testing.T) {
		got := trailGate(trailScanResult{
			State:   trailSeen,
			Line:    `{"type":"result","result":"` + trailNeedle + `"}`,
			Trailer: &resultTrailer{Type: "result", TerminalReason: "completed"},
			Detail:  "a scan detail that also carries " + trailNeedle,
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
}
