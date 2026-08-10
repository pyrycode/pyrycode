//go:build e2e_realclaude

package realclaude

// The three instants a staged probe run rests on were ordered BY CONSTRUCTION,
// or they were not and which premise is missing is named — so that any later
// claim resting on that ordering rests on a checked premise rather than on an
// assumption.
//
// Everything here runs offline: no live claude, no credentials, no daemon, no
// env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestTrailOrder' -v ./internal/e2e/realclaude/
//
// # The ordering, and the three premises it rests on
//
// A staged probe run holds its command un-finishable — blocked on a FIFO nobody
// writes to — for the whole of the wait on pyry's exit. Three instants are then
// ordered by construction: the trailer is sighted on pyry's stdout, then pyry
// exits, then the rig re-reads a pid it pinned earlier. That ordering is what
// lets a later reading of the pid say something about the earlier sighting.
//
// The ordering is only BY CONSTRUCTION if three things actually held, and each
// can fail independently:
//
//  1. THE TRAILER WAS SIGHTED. With no sighting there is no earlier instant for
//     anything to be about.
//  2. PYRY EXITED WITHIN ITS DEADLINE. Without a completed exit the later read
//     is not later than anything, and the "then" in the argument is unearned.
//  3. THE HOLD WAS STILL HELD FOR THE WHOLE OF THAT WAIT. This one is
//     load-bearing in a way the other two are not. "A process can only die once"
//     is a fact about a PROCESS; a pid is only a stable name for one while it
//     cannot exit. Held on a FIFO nobody writes to, the command cannot finish,
//     so the pinned pid still names the same process at the later read. Release
//     the hold and the pid could have been retired and reissued between the
//     sighting and the read, at which point the later reading is about some
//     other process entirely. So the hold underwrites the POSITIVE direction of
//     the argument and not only the negative one, which is why it is a premise
//     and not a guard.
//
// # Every failed premise is a void and never a negative
//
// Each of the three failures is the RIG's own, not pyry's, and nothing was
// measured when one fired — so no verdict may be read from it. Reporting a soft
// negative out of the rig's own breakage is the collapse this family already
// refuses in code: trailOutcomeVoidLivenessInstrument's doc
// (trail_run_outcome_test.go:180-184) says that folding an instrument failure
// into "no row matched" "would manufacture a clean negative out of the
// instrument's breakage". The same reasoning applies one layer up, to the
// premises themselves.
//
// # This file publishes no verdict about any process
//
// trailCertifyOrdering says only that the three instants were ordered, or that
// they were not and which premise is missing. That is why it stands alone and is
// sound alone: it cannot publish a wrong verdict about a process, because it
// publishes none. No value here claims anything about a command's liveness or
// about the turn having been declared finished, and the Detail content rule on
// the predicate states that as a rule rather than leaving it to habit.
//
// # Reused, not rebuilt
//
// holdProbeFIFO (background_trigger_probe_test.go:663) supplies the hold and
// trailWaitForTrailer (result_trailer_observation_test.go:267) supplies the
// sighting. This predicate consumes FACTS ABOUT those, as three plain booleans,
// and never the objects themselves — see the parameter list's own note for why
// the narrow type is the enforcement rather than a convention. trailDetail
// (trailer_admissibility_test.go:352) formats and caps every Detail, and
// TestTrailAdmissibilityConstantsAreClosed (trailer_admissibility_test.go:1213)
// is where these four values are checked against every other value this tree
// ships.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// --- the ordering's value space ------------------------------------------------

// Whether the three instants were ordered by construction, as a POSITIVE
// ALLOWLIST of four: one certification and three named voids, one per premise.
//
// Every value carries an `order-` prefix, and the fourth sub-namespace is
// load-bearing rather than cosmetic. order-void-pyry-did-not-exit and the
// shipped run-void-pyry-did-not-exit (trail_run_outcome_test.go:168) mean nearly
// the same words one layer apart, so a copy-paste between them must be a visible
// mistake in a published record rather than a plausible line — the same argument
// trail_run_outcome_test.go:108-113 makes for `run-`.
//
// The Go identifiers deliberately avoid the trailOutcomeVoid… shape for the same
// reason at the other tier: trailOrderVoidNoExit rather than
// trailOrderVoidPyryDidNotExit, because the latter differs from the shipped
// trailOutcomeVoidPyryDidNotExit only in the middle, both autocomplete from
// `trailO`, and both are untyped strings, so the compiler catches nothing.
// Naming each void after the MISSING PREMISE rather than after the event removes
// the pair.
//
// THERE IS DELIBERATELY NO order-out-of-contract VALUE, and its absence is a
// decision rather than an omission. Every sibling space has one, reached by a
// guard at the top of its predicate. Here the parameter list is three bools, so
// all eight inputs are readings by construction and there is nothing such a value
// could be about. An unreachable named value is worse than none: no fixture could
// reach it, a row asserting it would be unreachable-red, and its presence would
// make this space's count a lie about what the predicate can answer.
const (
	// trailOrderCertified: all three premises held, so the three instants were
	// ordered by construction. Says NOTHING about any command's liveness — there
	// is no such claim in this space to be made.
	trailOrderCertified = "order-certified"
	// trailOrderVoidUnsighted: the trailer was not sighted, so there is no
	// earlier instant for anything to be about. The rig's own failure, so an
	// unmeasured premise and never a negative.
	trailOrderVoidUnsighted = "order-void-trailer-unsighted"
	// trailOrderVoidNoExit: pyry did not exit within its deadline, so the later
	// read is not later than anything and the "then" in the ordering argument is
	// unearned. Deliberately NOT trailOutcomeVoidPyryDidNotExit
	// (trail_run_outcome_test.go:168), which is the RUN's view of the same event
	// one layer up; this one says only that an ordering premise is missing.
	trailOrderVoidNoExit = "order-void-pyry-did-not-exit"
	// trailOrderVoidUnheld: the hold was not held for the whole of the wait, so
	// the pinned pid may no longer name the same process at the later read. The
	// premise whose failure removes the SUBJECT rather than one endpoint of the
	// ordering, which is why it outranks the other two — see
	// trailCertifyOrdering's precedence argument.
	trailOrderVoidUnheld = "order-void-hold-released"
)

// trailOrderPremiseClause is the fixed tail every Detail below ends with. It is
// a shared constant rather than four hand-written copies so that the four arms
// cannot DRIFT from one another in how they render the premises.
//
// Sharing the constant is not on its own what makes "every Detail carries all
// three premises" true: an arm can still omit the tail from its own format
// string, and deleting it from one arm leaves every other test here green. What
// makes the rule checkable is TestTrailOrderAllEightPremiseCombinations, which
// renders this clause from each row's own three booleans and requires the
// Detail to contain it — so an arm that reports its decision and quietly drops
// the co-failures that did not decide reddens, as does one that fills the
// clause in the wrong order.
//
// It costs ~65 bytes of the 512-byte cap and leaks nothing: three booleans the
// caller already holds.
const trailOrderPremiseClause = "premises: trailer-sighted=%t pyry-exited=%t hold-held=%t"

// --- the record ----------------------------------------------------------------

// trailOrderResult is what the ordering predicate produces, in trailAdmitResult's
// exact shape (trailer_admissibility_test.go:340-343). No third field: there is
// no reason to certify here, and the three input booleans reach a reader through
// the Detail's fixed clause rather than as fields.
//
// TRAP-FREE BY CONSTRUCTION, and trivially so — no pointer, no embedded type,
// and the function that fills it can see no captured byte at all, because its
// whole input is three bools. TestTrailOrderResultCarriesNoCapturedBytes is what
// keeps a future field from quietly adding a command-shaped key.
type trailOrderResult struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

// --- the predicate --------------------------------------------------------------

// trailCertifyOrdering certifies that a probe run's three instants were ordered
// by construction, or refuses with the name of the premise that is missing.
//
// Pure over its input: no exec, no clock, no filesystem. That is what lets every
// arm be driven with no live turn and no credentials. It takes no *testing.T and
// never fails a test — an instrument failure observed mid-turn is a datum to
// publish, not a reason to abort the turn, the same contract as trailGate,
// trailAdmitAttribution, trailScan, tdnClassifyReapLog, pinReadState and
// fifoLiveRead.
//
// # Three booleans, and the parameter type IS the enforcement
//
// finSighting (finding_run_gather_test.go:372-384) is the nearest existing
// reduction of a sighting and is the WRONG input here: it carries TerminalReason,
// StopReason, Subtype and KeyNames — trailer values that have no business in this
// record. What this predicate needs from the sighting is THAT IT HAPPENED, not
// what it said.
//
// The family's forbidden-key walk would not catch that on its own: those fields
// marshal as terminal_reason / stop_reason / subtype / trailer_keys, none of
// which is `command`-shaped. Narrowing the parameter is what forecloses it;
// widening a denylist could not. With the input pinned to three bools this
// function can see no captured byte at all.
//
// # The premises are supplied, not recovered
//
// No shipped gather records "the hold was still held for the whole of the wait":
// finStageSubject (finding_stage_held_group_test.go:175-188) carries needles,
// pinned pids, a group and a row count, and trailRunReadings
// (trail_run_outcome_test.go:394-459) has no such field either. So the fact is a
// parameter a caller hands in — and it is nonetheless CHECKABLE rather than
// assumed: holdProbeFIFO (background_trigger_probe_test.go:663) keeps the write
// end, hands the caller a receive-only channel with no release path of its own,
// and closes its release channel only in the t.Cleanup it registers itself, which
// by construction runs after the subtest body. A wait taken in that body is
// therefore held for its whole duration by construction, and a caller passing
// holdHeld=true is asserting something it can know.
//
// # Precedence: hold → sighting → exit, chosen and then checked
//
// THE HOLD OUTRANKS BOTH because its failure removes the SUBJECT, not merely an
// instant. Held on a FIFO nobody writes to, the command cannot finish, so the
// pinned pid still names the same process at the later read; release the hold and
// the pid could have been retired and reissued between the sighting and the read,
// at which point the later reading is about some other process entirely. The
// other two failures leave the subject intact and remove one endpoint of the
// ordering. Same structural-outranks-situational rule trailAdmitAttribution
// applies when it puts trailAdmitVoidBudgetFired above every reap-side void
// (trailer_admissibility_test.go:212-215).
//
// SIGHTING OUTRANKS EXIT because it is the earlier instant: with no earlier
// instant, whether the later one completed is moot.
//
// The precedence is CHOSEN here and CHECKED by
// TestTrailOrderAllEightPremiseCombinations, which enumerates all eight inputs
// rather than one representative pair — three double-failure pairs plus the
// triple, so the precedence is pinned everywhere it is consulted.
//
// # Body shape: three guards then a fall-through
//
// Four return sites, and the CERTIFIED arm is the fall-through, so no arm is a
// defensive default that no fixture reaches.
//
// # The Detail content rule, pinned rather than left to judgement
//
// Each Detail MAY name the premise that decided, this space's value names, and —
// when it outranked another failure — the precedence argument for why. It MAY
// NEVER claim anything about a command's liveness, or about the turn having been
// declared finished: neither instant is established here, and a Detail claiming
// either would say one layer up exactly what the value space refuses to say. It
// MUST end with trailOrderPremiseClause, which is what carries the co-failures on
// the four multi-failure inputs without a second field or a per-row judgement
// call. All four go through trailDetail, so all inherit the 512-byte cap
// (background_reach_probe_test.go:123), which truncates SILENTLY — and
// TestTrailOrderAllEightPremiseCombinations asserts the truncation marker is
// absent from every row, so a Detail whose argument would be cut off reddens here
// rather than reaching an operator's artifact.
func trailCertifyOrdering(trailerSighted, pyryExited, holdHeld bool) trailOrderResult {
	decide := func(value, format string, args ...any) trailOrderResult {
		return trailOrderResult{Value: value, Detail: trailDetail(format, args...)}
	}

	// The hold first: its failure removes the subject rather than an endpoint.
	if !holdHeld {
		return decide(trailOrderVoidUnheld,
			"the hold was released before the wait on pyry's exit completed, so the pinned pid "+
				"is no longer a stable name: it could have been retired and reissued between "+
				"the sighting and the later read. Outranks %s and %s: their failures remove one "+
				"endpoint, this one removes the subject. An unmeasured premise, never a "+
				"negative about any command. "+trailOrderPremiseClause,
			trailOrderVoidUnsighted, trailOrderVoidNoExit, trailerSighted, pyryExited, holdHeld)
	}

	// Then the earlier instant: with none, the later one is moot.
	if !trailerSighted {
		return decide(trailOrderVoidUnsighted,
			"the trailer was not sighted, so there is no earlier instant for a later reading of "+
				"the pinned pid to be about. Outranks %s: with no earlier instant, whether the "+
				"later one completed is moot. An unmeasured premise, never a negative about any "+
				"command. "+trailOrderPremiseClause,
			trailOrderVoidNoExit, trailerSighted, pyryExited, holdHeld)
	}

	if !pyryExited {
		return decide(trailOrderVoidNoExit,
			"pyry did not exit within its deadline, so the later read is not later than "+
				"anything and the \"then\" in the ordering argument is unearned. Reported only "+
				"when both other premises held, since each of them outranks it. An unmeasured "+
				"premise, never a negative about any command. "+trailOrderPremiseClause,
			trailerSighted, pyryExited, holdHeld)
	}

	return decide(trailOrderCertified,
		"all three premises held, so the three instants were ordered by construction: the "+
			"trailer was sighted, pyry then exited, and the pinned pid was re-read after that "+
			"exit under a hold held for the whole of the wait. Certifies the ORDERING alone — "+
			"nothing about any command's liveness, and nothing about the turn having been "+
			"declared finished. "+trailOrderPremiseClause,
		trailerSighted, pyryExited, holdHeld)
}

// --- membership helpers ----------------------------------------------------------

// trailOrderValues is the closed set as data, so a coverage loop can assert over
// it. It is derived from trailIsOrderValue's own space by construction: a value
// listed here that the predicate rejects, or vice versa, goes red in
// TestTrailOrderValuesAgreeWithThePredicate below.
func trailOrderValues() []string {
	return []string{
		trailOrderCertified,
		trailOrderVoidUnsighted,
		trailOrderVoidNoExit,
		trailOrderVoidUnheld,
	}
}

// trailIsOrderValue reports whether v is one of the four recorded ordering
// values. It mirrors trailIsGateValue (trailer_admissibility_test.go:906) and
// trailIsRunOutcome (trail_run_outcome_test.go:541) and exists for the same
// reason: a value a reader of the published record cannot look up is a verdict
// they cannot interpret.
func trailIsOrderValue(v string) bool {
	switch v {
	case trailOrderCertified, trailOrderVoidUnsighted, trailOrderVoidNoExit,
		trailOrderVoidUnheld:
		return true
	}
	return false
}

// --- fixtures ---------------------------------------------------------------------

// trailOrderPremises is one input's three facts, named. A test-only type: it
// keeps the eight rows below readable and stops a mislabelled row from passing
// silently, which three positional bools in a table would not.
type trailOrderPremises struct{ Sighted, Exited, Held bool }

// certify passes the three facts POSITIONALLY, which is what makes the
// single-premise tests an anti-swap check: swapping any two parameters at
// trailCertifyOrdering's definition reddens at least two of them.
func (p trailOrderPremises) certify() trailOrderResult {
	return trailCertifyOrdering(p.Sighted, p.Exited, p.Held)
}

// trailOrderCertifiedPremises is the all-three-held fixture the single-premise
// tests start from before removing exactly one premise.
func trailOrderCertifiedPremises() trailOrderPremises {
	return trailOrderPremises{Sighted: true, Exited: true, Held: true}
}

// --- tests ------------------------------------------------------------------------

// TestTrailOrderAllEightPremiseCombinations is AC2 made executable: one
// assignment rule covering all eight combinations of the three facts, with none
// left to judgement.
//
// ALL EIGHT rather than a representative sample, because three premises give
// three double-failure pairs plus one triple-failure case, and a single
// two-failure fixture would pin one of those four and leave the other three to
// judgement. Enumerating them is what pins the precedence EVERYWHERE it is
// consulted.
//
// Row names state the rule the row pins rather than its booleans, in
// finding_staging_gate_test.go:500-508's style; the booleans are in the fixture
// beside them.
//
// Each row also asserts the Detail is non-empty, carries no truncation marker,
// and contains trailOrderPremiseClause rendered from that row's own booleans.
//
// The MARKER and not a length against 512: reachCapCommand returns its input
// unchanged at exactly reachMaxCommandBytes and appends the marker only past it
// (background_reach_probe_test.go:945-950), so a len < 512 check both
// false-fails at the boundary and pins a literal that drifts when the constant
// moves. The marker test is the property itself.
//
// The CLAUSE assertion is what makes the Detail content rule's MUST checkable
// rather than a convention the arms happen to follow. Sharing one constant
// across the four format strings stops them drifting from one another, but an
// arm can still omit the tail entirely, and nothing else here would notice.
func TestTrailOrderAllEightPremiseCombinations(t *testing.T) {
	cases := []struct {
		name string
		in   trailOrderPremises
		want string
	}{
		{
			name: "certified: all three premises held",
			in:   trailOrderPremises{Sighted: true, Exited: true, Held: true},
			want: trailOrderCertified,
		},
		{
			name: "single: an unsighted trailer leaves no earlier instant",
			in:   trailOrderPremises{Sighted: false, Exited: true, Held: true},
			want: trailOrderVoidUnsighted,
		},
		{
			name: "single: pyry not exiting leaves the later read later than nothing",
			in:   trailOrderPremises{Sighted: true, Exited: false, Held: true},
			want: trailOrderVoidNoExit,
		},
		{
			name: "single: a released hold retires the pinned pid's stability",
			in:   trailOrderPremises{Sighted: true, Exited: true, Held: false},
			want: trailOrderVoidUnheld,
		},
		{
			name: "order: an unsighted trailer outranks a missing exit",
			in:   trailOrderPremises{Sighted: false, Exited: false, Held: true},
			want: trailOrderVoidUnsighted,
		},
		{
			name: "order: a released hold outranks an unsighted trailer",
			in:   trailOrderPremises{Sighted: false, Exited: true, Held: false},
			want: trailOrderVoidUnheld,
		},
		{
			name: "order: a released hold outranks a missing exit",
			in:   trailOrderPremises{Sighted: true, Exited: false, Held: false},
			want: trailOrderVoidUnheld,
		},
		{
			name: "order: a released hold outranks both other failures",
			in:   trailOrderPremises{Sighted: false, Exited: false, Held: false},
			want: trailOrderVoidUnheld,
		},
	}
	// The DISTINCT triples and not len(cases): three booleans admit exactly eight
	// inputs, so eight distinct ones is the same statement as "every combination
	// appears". A cardinality check alone passes a table that duplicates one row
	// and drops another, which silently unpins whichever rule the dropped row
	// carried.
	seen := make(map[trailOrderPremises]bool, len(cases))
	for _, tc := range cases {
		seen[tc.in] = true
	}
	if len(seen) != 8 {
		t.Fatalf("the table's %d row(s) cover %d distinct input(s), want all 8 — three booleans "+
			"admit exactly eight inputs and a set short of that leaves the precedence pinned "+
			"somewhere and assumed elsewhere", len(cases), len(seen))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.certify()
			if got.Value != tc.want {
				t.Errorf("value: got %q (%s), want %q for sighted=%t exited=%t held=%t",
					got.Value, got.Detail, tc.want, tc.in.Sighted, tc.in.Exited, tc.in.Held)
			}
			if got.Detail == "" {
				t.Error("the Detail is empty, so the record names no premise and a reader has " +
					"only the value to interpret")
			}
			if strings.Contains(got.Detail, reachTruncationMarker) {
				t.Errorf("the Detail was truncated at the %d-byte cap, so its argument reaches an "+
					"operator cut off: %q", reachMaxCommandBytes, got.Detail)
			}
			// The clause carries the co-failures that did NOT decide, which is the
			// design's substitute for a second field on the four multi-failure
			// rows. Rendered from this row's own booleans, so an arm that drops the
			// clause and one that fills it in the wrong order both redden here.
			wantClause := fmt.Sprintf(trailOrderPremiseClause, tc.in.Sighted, tc.in.Exited, tc.in.Held)
			if !strings.Contains(got.Detail, wantClause) {
				t.Errorf("the Detail omits %q, so it reports the premise that decided while "+
					"dropping the co-failures that did not: %q", wantClause, got.Detail)
			}
		})
	}
}

// TestTrailOrderEachPremiseHasItsOwnVoid is AC3 made executable: each premise
// removed from an otherwise-certified fixture leaves a result that is no longer
// certified and carries THAT PREMISE's own void.
//
// Each subtest asserts the all-true base certifies BEFORE flipping anything —
// the premise assertion is what stops the test passing by classifying garbage,
// the discipline trail_run_outcome_test.go:2302-2307 states.
//
// These three are also the anti-swap check: any pairwise swap of the three
// parameters at trailCertifyOrdering's definition reddens at least two of them,
// because each single flip maps to a distinct void.
func TestTrailOrderEachPremiseHasItsOwnVoid(t *testing.T) {
	cases := []struct {
		name   string
		remove func(*trailOrderPremises)
		want   string
	}{
		{
			name:   "removing the sighting",
			remove: func(p *trailOrderPremises) { p.Sighted = false },
			want:   trailOrderVoidUnsighted,
		},
		{
			name:   "removing pyry's exit",
			remove: func(p *trailOrderPremises) { p.Exited = false },
			want:   trailOrderVoidNoExit,
		},
		{
			name:   "removing the hold",
			remove: func(p *trailOrderPremises) { p.Held = false },
			want:   trailOrderVoidUnheld,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := trailOrderCertifiedPremises()
			// The premise, first: without it a predicate that certified nothing at
			// all would pass all three subtests.
			if base := in.certify(); base.Value != trailOrderCertified {
				t.Fatalf("the base fixture reads %q (%s), want %q — every row here removes one "+
					"premise from an OTHERWISE-CERTIFIED input", base.Value, base.Detail,
					trailOrderCertified)
			}

			tc.remove(&in)
			got := in.certify()
			if got.Value == trailOrderCertified {
				t.Errorf("%s still certifies the ordering: a missing premise means nothing was "+
					"measured, so the ordering is unearned rather than established", tc.name)
			}
			if got.Value != tc.want {
				t.Errorf("value: got %q (%s), want %q — every void names the premise it is about, "+
					"so a reader need not parse the Detail to tell the three apart",
					got.Value, got.Detail, tc.want)
			}
		})
	}
}

// TestTrailOrderValuesAgreeWithThePredicate is AC4 made executable, in
// TestTrailRunOutcomeValuesAgreeWithThePredicate's shape
// (trail_run_outcome_test.go:2504): the list and the predicate agree in BOTH
// directions, the count is asserted against the ticket's own enumeration, and the
// predicate rejects the values of the adjacent spaces.
//
// The rejection direction is the one that earns its place here. It keeps this
// space from silently absorbing a value that DOES claim something about a
// process — which is why the four pinIsVerdict liveness verdicts are on the list
// alongside the gate and admit values — and it leaves the naming rule reviewable
// against a fixed enumerated list rather than against prose.
//
// The adjacent values are SPELLED rather than looped through a helper: no
// trailGateValues() / trailAdmitValues() ships, and adding one would grow the
// edit to trailer_admissibility_test.go for no gain here.
func TestTrailOrderValuesAgreeWithThePredicate(t *testing.T) {
	values := trailOrderValues()
	if len(values) != 4 {
		t.Errorf("the closed set holds %d value(s), want 4 — the count is the ticket's own "+
			"enumeration (one certification plus one void per premise) and a change to it is a "+
			"change to what this predicate can answer", len(values))
	}
	for _, v := range values {
		if !trailIsOrderValue(v) {
			t.Errorf("%q is listed as an ordering value but trailIsOrderValue rejects it", v)
		}
	}

	// The other direction, over every value of the adjacent spaces rather than a
	// representative pair. trailOrderVoidNoExit sits one word from
	// trailOutcomeVoidPyryDidNotExit, which is the near-collision the `order-`
	// prefix exists to make visible, so the run space's is on the list too.
	rejected := []string{
		"",
		// All seven gate values.
		trailGateUsable, trailGateNoTrailer, trailGateScanAborted, trailGateBudgetFired,
		trailGateAbsentOwesNone, trailGatePresentOwesNone, trailGateOutOfContract,
		// All seven admissibility values.
		trailAdmitProof, trailAdmitVoidBudgetFired, trailAdmitVoidInstrument,
		trailAdmitVoidNoLine, trailAdmitVoidGroupUnnamed, trailAdmitVoidNotOneReapLine,
		trailAdmitOutOfContract,
		// The four liveness verdicts. THE DIRECTION THAT EARNS ITS PLACE: each of
		// these DOES claim something about a process, and this space claims nothing
		// about any.
		pinStateRunning, pinStateExitedNotReaped, pinStateNoSuchProcess,
		pinStateInstrumentFailed,
		// The scan's three input states, one tab-completion from a result.
		trailSeen, trailAbsent, trailAborted,
		// The run space's near-collision and its neighbours.
		trailOutcomeVoidPyryDidNotExit, trailOutcomeRunningAtTrailer, trailOutcomeOutOfContract,
	}
	for _, v := range rejected {
		if trailIsOrderValue(v) {
			t.Errorf("trailIsOrderValue accepts %q, which belongs to another space", v)
		}
	}
}

// TestTrailOrderResultCarriesNoCapturedBytes is AC5's structural half, in
// TestTrailRunOutcomeCarriesNoCapturedBytes's shape
// (trail_run_outcome_test.go:2269-2290): marshal the record, decode to
// map[string]json.RawMessage, and refuse any command/args/comm/argv-shaped key.
//
// THE trailNeedle BYTE-SWEEP IS GENUINELY ABSENT HERE, NOT DEFERRED BY
// CONVENIENCE. With the input pinned to three booleans, trailCertifyOrdering can
// see no captured byte at all, so a needle planted anywhere upstream has no route
// into this record. Planting one anyway would make a sweep that goes green
// against a CORRECT build and against a broken one alike, which measures nothing.
// #1440's record is the first one a captured byte can enter, and that is the
// ticket that ships the sweep.
//
// What remains checkable, and is checked here, is the structural claim: the
// record has no field for a command string today, and a future field must not
// quietly add one. pinStateColumns (process_pin_liveness_test.go:232) refuses a
// `command` column at the source for the same reason — those columns route the
// operator's CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY into an artifact
// destined for a public issue.
func TestTrailOrderResultCarriesNoCapturedBytes(t *testing.T) {
	got := trailOrderCertifiedPremises().certify()
	// The premise first, so the test cannot pass by marshalling garbage.
	if got.Value != trailOrderCertified {
		t.Fatalf("value: got %q (%s), want %q — the premise is a well-formed certified record",
			got.Value, got.Detail, trailOrderCertified)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the ordering result: %v", err)
	}

	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keyed); err != nil {
		t.Fatalf("decoding the marshalled ordering result: %v", err)
	}
	for _, forbidden := range []string{"command", "args", "comm", "argv"} {
		for key := range keyed {
			if strings.Contains(key, forbidden) {
				t.Errorf("the ordering result carries key %q, which is %q-shaped: this record's "+
					"whole value is that it can be published unreviewed, and a command column "+
					"would inherit the operator-review-before-paste obligation onto it",
					key, forbidden)
			}
		}
	}
}
