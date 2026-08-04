//go:build e2e_realclaude

package realclaude

// The probe run's verdict and the trailer evidence behind it, projected onto one
// published record from behind #1266's discriminated optional.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence. Everything here runs offline: no live
// claude, no credentials, no daemon, no subject process, no process-table read,
// no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinTrailer' -v ./internal/e2e/realclaude/
//
// # The four trailer fields sit behind a discriminated optional
//
// subtype, terminal_reason, is_error and stop_reason live on resultTrailer
// (tool_loop_test.go:194-203), reachable only through trailScanResult.Trailer —
// a pointer that is nil unless State == trailSeen, and deliberately so: a
// consumer that dereferences it without checking State panics loudly
// (result_trailer_observation_test.go:108-118), which was chosen over a value
// type handing back TerminalReason == "" and letting an empty terminal reason
// pass as a real one. So this record reads the four fields from the observation
// directly, under a guard whose FIRST operand is the State — a run that wrote no
// trailer carries its void rather than panicking.
//
// # The fields come from Trailer, never from Line
//
// Line is the matched line as scanned but CAPPED: trailScan records
// `Line: reachCapCommand(string(scanner.Bytes()))` (:182), 512 bytes. On the
// emitter's pinned wire order (emitter.go:456-468) terminal_reason is LAST and
// result sixth, so any truncation takes terminal_reason first — the exact defect
// #1266's decode-then-cap design exists to prevent. Trailer is the decode of the
// FULL line.
//
// # The record carries no trailer line at all
//
// resultTrailer has no `result` member, so the decode structurally cannot carry
// the assistant payload — a property to preserve, not an omission to fix. Line
// does carry it: the trailer's `result` field is the last assistant message
// (emitter.go:212-225), roughly 415 of the retained 512 bytes being text the
// model chose, and its own doc marks it OPERATOR-REVIEW-BEFORE-PASTE
// (result_trailer_observation_test.go:100-107). Every published record in this
// family already excludes a captured line for that reason: trailRunOutcome is
// "COUNTS, NEVER ROWS" (trail_run_outcome_test.go:222), finAttributeEntry states
// the exclusion as its own construction (finding_attribution_fanout_test.go:80-88),
// and finOutcomeResult is a value and a detail and nothing else
// (finding_staging_gate_test.go:198-201). This record's trailer evidence is its
// State and the four decoded fields. Never its bytes.
//
// # Reused, not rebuilt
//
// trailScan (result_trailer_observation_test.go:164) and its fixtures
// trailFixtureTrailer (:282), trailFixtureNoTrailer (:291), trailNeedle (:300),
// trailPaddedTrailer (:306) and trailOverlongPad (:317) are the shipped scan and
// the shipped plants — a second scanner over the same bytes that disagreed would
// be worse than either. The three scan states (:57-71) and the three lateness
// discriminators (:75-90) are shipped closed spaces, called and never restated.
// trailIsRunOutcome (trail_run_outcome_test.go:258) and trailRunOutcomeValues
// (:1194) are the eleven; finOutcomeIsValue (finding_staging_gate_test.go:210)
// and finOutcomeValues (:223) the staging tier's seven — called, never
// re-derived. reachMaxCommandBytes and reachCapCommand
// (background_reach_probe_test.go:123, :945) are the single-sourced cap.
//
// trailDetail (trailer_admissibility_test.go:206) is reused rather than given a
// finDetail twin, for the reason merged code has settled twice
// (finding_attribution_fanout_test.go:37-44, finding_staging_gate_test.go:73-81):
// it carries no decision — fmt.Sprintf plus reachCapCommand's 512-byte cap — and
// a file that calls trailIsRunOutcome is BY DESIGN inside the trail* family's
// reach. A twin would only fork the cap.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// --- the record ----------------------------------------------------------------

// finTrailerRecord is one run's verdict together with the trailer evidence
// behind it. Ten scalars: no pointer, no slice, no embedded struct, and nothing
// from which trailScanResult.Line or the trailer pointer is reachable.
//
// THE TYPE IS TRAP-FREE BY CONSTRUCTION, in finAttributeEntry's sense
// (finding_attribution_fanout_test.go:80-88): copying it costs nothing and
// aliases nothing, so the ticket that embeds it whole needs no ordering
// discipline a later edit can break. THE BUILDER IS NOT — its input does carry
// the capped line — which is said again at finTrailerBuild so that neither claim
// is read as covering the other.
//
// The four fields are copied BY VALUE at build time, which severs the pointer:
// two records built from one observation cannot alias a shared *resultTrailer.
//
// # Field order, and the absence of omitempty
//
// Staleness sits immediately after BoundFrom so that "the duration is never
// published without its discriminator beside it" is true POSITIONALLY in the
// rendered JSON rather than by convention.
//
// NO FIELD CARRIES omitempty, and that is a decision. wireFields' default arm
// (emitter.go:434-435) returns terminal_reason == "" on a genuinely SEEN
// trailer; under omitempty such a record would render byte-identically to a
// trailAbsent record's zero, collapsing "the trailer was read and its terminal
// reason is empty" into "there was no trailer" — precisely the collapse the nil
// pointer was chosen to prevent one tier down. The same argument kills omitempty
// on IsError (drops false) and on Staleness (drops the honest zero that pairs
// with trailBoundNone). State is the discriminator a reader consults, and every
// field is always present beside it.
//
// # What the four trailer fields are worth
//
// Not visible from the names, so the type says it. wireFields
// (emitter.go:428-437) derives Subtype, IsError and TerminalReason from a SINGLE
// ExitReason, so their agreement is one value rendered three ways and not three
// corroborating reads. TerminalReason is pyry's OWN SYNTHESIS on this path —
// claude never emitted it. Only StopReason is independently sourced, forwarded
// from the model's last message unvalidated (`e.lastStopReason =
// entry.Message.StopReason`, emitter.go:210): it is the one model-influenced
// field crossing into this record uncapped, bounded by the wire's own shape and
// by nothing this record does. Capping it is out of scope and AC2 requires it be
// carried whole; naming it here is what keeps #1286's multi-input sweep from
// later planting a needle in a field this record must carry verbatim.
//
// # The Detail's content rule, pinned rather than left to judgement
//
// In trailRunOutcome.Detail's shape (trail_run_outcome_test.go:225-232), it MAY
// name the outcome value, the scan state, BoundFrom, Bounded as a boolean and
// the four decoded fields — permitted because the record already publishes them
// as fields, so the exposure decision is this type's and the Detail adds nothing
// to it. It may NEVER quote trailScanResult.Line or interpolate any part of it,
// INCLUDING a length, a byte count derived from it, a prefix or a hash.
//
// Every Detail must also leave len(trailNeedle) bytes of headroom under
// reachMaxCommandBytes — under 470 bytes on every row. That is not stylistic:
// trailDetail caps at 512, so a house-style multi-sentence Detail can eat the
// budget and let a leak be truncated away, turning the sweep below green against
// a record that did leak. That is the defect #1284 shipped and then had to fix.
// TestFinTrailerRecordCarriesNoCapturedBytes is the enforcing test and asserts
// the headroom PER ROW.
type finTrailerRecord struct {
	Outcome   string        `json:"outcome"`
	State     string        `json:"trailer_state"`
	Bounded   bool          `json:"lateness_bounded"`
	BoundFrom string        `json:"lateness_bound_from"`
	Staleness time.Duration `json:"staleness_ns"`

	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	TerminalReason string `json:"terminal_reason"`
	StopReason     string `json:"stop_reason"`

	Detail string `json:"detail"`
}

// --- the builder ----------------------------------------------------------------

// finTrailerBuild projects one run's verdict and its trailer evidence onto a
// published record.
//
// Pure over its inputs: no exec, no clock, no filesystem, no *testing.T, and it
// never fails a test — the same contract as trailScan, trailGate,
// trailAdmitAttribution, trailClassifyRun, finOutcomeStagingGate,
// tdnClassifyReapLog and pinReadState, because an instrument failure observed
// mid-turn is a datum to publish, not a reason to abort the turn.
//
// UNLIKE THE RECORD IT RETURNS, THIS FUNCTION IS NOT TRAP-FREE BY CONSTRUCTION:
// obs.trailScanResult.Line is in reach here, so the no-captured-bytes property
// of this builder is held by the Detail content rule plus
// TestFinTrailerRecordCarriesNoCapturedBytes — not by the shape of the input.
//
// # The outcome is consumed, never decided
//
// The value comes from one of two closed sets built elsewhere: trailClassifyRun's
// eleven (trail_run_outcome_test.go:344) and the staging-gate tier's seven
// (finding_staging_gate_test.go). Both are caller-supplied and carried AS
// RETURNED — not validated, not renamed, not re-derived, and never cross-checked
// against State. "A no-trailer run records trailOutcomeVoidNoTrailer" is a
// statement about what the CALLER hands in; trailClassifyRun already returns it
// for such a run. Deriving it here from State would give one field two sources,
// which this family's doctrine forbids. A disagreement between the outcome and
// the State beside it has no value in either closed set, and inventing one is
// out of scope.
//
// Nor is the field asked to reject a non-member: no builder in this family
// validates its value — trailRunOutcome (:233) and finOutcomeResult (:198) are
// plain structs — because membership lives in the reader-facing predicates,
// whose job is that "a value a reader of a published record cannot look up is a
// verdict they cannot interpret".
//
// # The State is consulted before the pointer is dereferenced
//
// carriesTrailer puts the State operand FIRST and Go's && short-circuits left to
// right, so that ordering is a property of the source rather than of this
// comment. The nil operand is there as well because trailGate, this trap's first
// consumer, already answers the State/nil PAIR before reading through the
// pointer: the inconsistency is a hand-built fixture's, and hand-built fixtures
// are all this file ever sees. Unlike trailGate this record has no
// out-of-contract value to report it with, so the honest behaviour is to publish
// the zero fields under whatever State was handed and let the Detail name which
// arm fired. One if, two Detail shapes; no third branch, no reject arm.
func finTrailerBuild(outcome string, obs trailObservation) finTrailerRecord {
	rec := finTrailerRecord{
		Outcome:   outcome,
		State:     obs.State,
		BoundFrom: obs.BoundFrom,
		Staleness: obs.Staleness,
		// Bounded is BoundFrom == trailBoundFromMiss AND NOTHING ELSE.
		// trailBoundFromStart carries a real duration that bounds nothing (the
		// first poll already matched, so the trailer may have been visible before
		// the loop began) and trailBoundNone is the honest no-bound, so a record
		// deriving this from Staleness != 0 would publish a non-bound wearing a
		// bound's label — trailRunOutcome:242-246's rule, unchanged.
		Bounded: obs.BoundFrom == trailBoundFromMiss,
	}

	if carriesTrailer := obs.State == trailSeen && obs.Trailer != nil; !carriesTrailer {
		rec.Detail = trailDetail("outcome %s over a %s scan carrying no decoded trailer, so the "+
			"four trailer fields hold their zero values; lateness %s, bounded=%t",
			outcome, obs.State, obs.BoundFrom, rec.Bounded)
		return rec
	}

	rec.Subtype = obs.Trailer.Subtype
	rec.IsError = obs.Trailer.IsError
	rec.TerminalReason = obs.Trailer.TerminalReason
	rec.StopReason = obs.Trailer.StopReason
	rec.Detail = trailDetail("outcome %s over a %s scan; subtype=%s is_error=%t "+
		"terminal_reason=%s stop_reason=%s, read from the decode of the full line; "+
		"lateness %s, bounded=%t", outcome, obs.State, rec.Subtype, rec.IsError,
		rec.TerminalReason, rec.StopReason, obs.BoundFrom, rec.Bounded)
	return rec
}

// --- tests -----------------------------------------------------------------------

// finTrailerSeenScan, finTrailerAbsentScan and finTrailerAbortedScan are the
// three scan states, each produced by the SHIPPED scanner over a shipped fixture
// rather than hand-assembled, so a row can never assert against a state trailScan
// would not actually return for those bytes.
func finTrailerSeenScan() trailScanResult {
	return trailScan([]byte(trailFixtureTrailer + "\n"))
}

func finTrailerAbsentScan() trailScanResult {
	return trailScan([]byte(trailFixtureNoTrailer))
}

func finTrailerAbortedScan() trailScanResult {
	return trailScan([]byte(trailPaddedTrailer(trailOverlongPad) + "\n"))
}

// TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator drives all three scan
// states and all three lateness discriminators, and pins that the bounded
// boolean is true on trailBoundFromMiss ALONE — even on the two rows that carry
// a non-zero Staleness beside a discriminator that bounds nothing. Those rows
// are the ones that bite: a builder deriving Bounded from Staleness != 0 agrees
// with this one everywhere else.
//
// The observations are hand-filled — a trailScan result with Staleness and
// BoundFrom set by hand — which needs no clock, no poll and no live turn.
func TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator(t *testing.T) {
	tests := []struct {
		name      string
		scan      trailScanResult
		staleness time.Duration
		boundFrom string
		wantState string
		// synthetic records that no run produces this pairing, so the row's own
		// claim is a contract check rather than a reading.
		synthetic string
	}{
		{
			name:      "a miss-bounded seen trailer is the only bounded row",
			scan:      finTrailerSeenScan(),
			staleness: 250 * time.Millisecond,
			boundFrom: trailBoundFromMiss,
			wantState: trailSeen,
		},
		{
			// LIVE pairing, not synthetic: trailWaitForTrailer:257 sets
			// obs.Staleness = now.Sub(start) on the first-poll-matched path, so a
			// real run hands a real duration beside a discriminator that bounds
			// nothing.
			name:      "a start-measured duration is a real duration that bounds nothing",
			scan:      finTrailerSeenScan(),
			staleness: 250 * time.Millisecond,
			boundFrom: trailBoundFromStart,
			wantState: trailSeen,
		},
		{
			name:      "an absent trailer carries the honest no-bound and a zero duration",
			scan:      finTrailerAbsentScan(),
			staleness: 0,
			boundFrom: trailBoundNone,
			wantState: trailAbsent,
		},
		{
			name:      "an aborted scan is not bounded even when handed a non-zero duration",
			scan:      finTrailerAbortedScan(),
			staleness: 250 * time.Millisecond,
			boundFrom: trailBoundNone,
			wantState: trailAborted,
			synthetic: "both trailBoundNone return sites — the aborted arm at " +
				"result_trailer_observation_test.go:264-265 and the deadline arm at :270 — leave " +
				"Staleness at zero, so NO RUN PRODUCES THIS PAIRING. The row is kept as a " +
				"contract check on a builder pure over its inputs, and it is what kills a " +
				"Bounded derived from Staleness != 0 on the no-bound discriminator",
		},
	}

	seenStates := map[string]bool{}
	seenBounds := map[string]bool{}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The premise first: a row asserting about a state the shipped scanner
			// did not return would be asserting about a fixture, not about a state.
			if tc.scan.State != tc.wantState {
				t.Fatalf("fixture state: got %q (%s), want %q", tc.scan.State, tc.scan.Detail,
					tc.wantState)
			}
			if !trailIsBoundFrom(tc.boundFrom) {
				t.Fatalf("bound origin %q is not one of #1266's three", tc.boundFrom)
			}

			obs := trailObservation{
				trailScanResult: tc.scan,
				Staleness:       tc.staleness,
				BoundFrom:       tc.boundFrom,
			}
			rec := finTrailerBuild(trailOutcomeVoidNoTrailer, obs)

			if rec.State != tc.wantState {
				t.Errorf("state: got %q, want %q — the scan's own answer, carried", rec.State,
					tc.wantState)
			}
			if rec.BoundFrom != tc.boundFrom {
				t.Errorf("bound origin: got %q, want %q — the discriminator is carried verbatim, "+
					"and the duration is never published without it", rec.BoundFrom, tc.boundFrom)
			}
			if rec.Staleness != tc.staleness {
				t.Errorf("staleness: got %v, want %v", rec.Staleness, tc.staleness)
			}

			wantBounded := tc.boundFrom == trailBoundFromMiss
			if rec.Bounded != wantBounded {
				t.Errorf("bounded: got %t, want %t on %q with a %v staleness — Bounded is "+
					"BoundFrom == %s and nothing else; deriving it from a non-zero Staleness "+
					"would publish a non-bound wearing a bound's label",
					rec.Bounded, wantBounded, tc.boundFrom, tc.staleness, trailBoundFromMiss)
			}
			if rec.Detail == "" {
				t.Error("empty detail: a record that cannot say which arm fired and why is " +
					"indistinguishable from a reading")
			}

			seenStates[tc.wantState] = true
			seenBounds[tc.boundFrom] = true
		})
	}

	// AC1's coverage claim, made executable rather than eyeballed off the row
	// names: a later edit that drops a row stops covering a state or a
	// discriminator, and that must be a failure here rather than a quieter test.
	for _, state := range []string{trailSeen, trailAbsent, trailAborted} {
		if !seenStates[state] {
			t.Errorf("no row drove the %q scan state", state)
		}
	}
	for _, bound := range []string{trailBoundFromMiss, trailBoundFromStart, trailBoundNone} {
		if !seenBounds[bound] {
			t.Errorf("no row drove the %q lateness discriminator", bound)
		}
	}
}

// TestFinTrailerRecordReadsTheDecodedTrailer pins the two properties that make
// the four trailer fields safe to publish: the build survives a run that wrote
// no trailer, and the fields come from the decode of the FULL line rather than
// from the capped copy.
func TestFinTrailerRecordReadsTheDecodedTrailer(t *testing.T) {
	t.Run("a no-trailer observation returns rather than panicking", func(t *testing.T) {
		scan := finTrailerAbsentScan()
		if scan.State != trailAbsent {
			t.Fatalf("fixture state: got %q (%s), want %q", scan.State, scan.Detail, trailAbsent)
		}
		if scan.Trailer != nil {
			t.Fatalf("fixture trailer: got %+v, want nil — only %s carries a decode", scan.Trailer,
				trailSeen)
		}

		// REACHING THE ASSERTIONS BELOW IS ITSELF THE ASSERTION. A build that
		// dereferenced Trailer without a guard panics on this input, which fails
		// the test before any comparison runs. What the guard cannot distinguish
		// is WHICH operand held: dropping the State operand alone leaves this row
		// green, because the fixture's pointer is nil either way. The ordering —
		// State first, && short-circuiting left to right — is therefore a property
		// of the source, documented at finTrailerBuild, and this row pins that a
		// guard exists at all rather than claiming to pin its shape.
		rec := finTrailerBuild(trailOutcomeVoidNoTrailer, trailObservation{
			trailScanResult: scan,
			BoundFrom:       trailBoundNone,
		})

		// The load-bearing assertions of this test, the outcome being an input
		// rather than a derivation: with no decode to read, the four fields hold
		// their zero values and claim nothing.
		if rec.IsError {
			t.Errorf("is_error: got true, want false — a bool (tool_loop_test.go:200) with no "+
				"decode behind it; detail %q", rec.Detail)
		}
		for _, f := range []struct{ name, got string }{
			{"subtype", rec.Subtype},
			{"terminal_reason", rec.TerminalReason},
			{"stop_reason", rec.StopReason},
		} {
			if f.got != "" {
				t.Errorf("%s: got %q, want empty — there was no trailer to read it from",
					f.name, f.got)
			}
		}
		if rec.State != trailAbsent {
			t.Errorf("state: got %q, want %q — the void is carried, not collapsed", rec.State,
				trailAbsent)
		}
	})

	t.Run("the four fields survive a cap that destroys terminal_reason in the line", func(t *testing.T) {
		// Pad 2000 is the plant this family already uses at
		// result_trailer_observation_test.go:482. Any pad from 142 up satisfies the
		// precondition; it is ASSERTED below rather than assumed, so a later
		// fixture change surfaces as a failed precondition instead of as a
		// silently weaker test.
		scan := trailScan([]byte(trailPaddedTrailer(2000) + "\n"))
		if scan.State != trailSeen {
			t.Fatalf("fixture state: got %q (%s), want %q", scan.State, scan.Detail, trailSeen)
		}
		if strings.Contains(scan.Line, "terminal_reason") {
			t.Fatalf("precondition: the capped line still contains terminal_reason, so this row "+
				"would pass even against a record that read the four fields off the CAPPED copy. "+
				"Line is %d bytes: %q", len(scan.Line), scan.Line)
		}

		rec := finTrailerBuild(trailOutcomeVoidBudgetFired, trailObservation{
			trailScanResult: scan,
			Staleness:       250 * time.Millisecond,
			BoundFrom:       trailBoundFromMiss,
		})

		// Pinned ON THE BUILT RECORD. The scan result's own survival of the cap is
		// already pinned at result_trailer_observation_test.go:477-505 and is not
		// restated here; what is new is that the projection reads Trailer and not
		// Line.
		for _, f := range []struct{ name, got, want string }{
			{"subtype", rec.Subtype, "error_max_turns"},
			{"terminal_reason", rec.TerminalReason, "max_turns"},
			{"stop_reason", rec.StopReason, "end_turn"},
		} {
			if f.got != f.want {
				t.Errorf("%s: got %q, want %q — terminal_reason is LAST on the pinned wire order "+
					"(emitter.go:456-468) and ~2 KiB past the cap here, so a record reading the "+
					"capped Line could not have recovered it", f.name, f.got, f.want)
			}
		}
		if !rec.IsError {
			t.Error("is_error: got false, want true — the budget-fired reading must cross too")
		}
	})
}

// finTrailerOutcomeValues is the union of the two closed sets a carried outcome
// may come from, built by CALLING the two shipped lists rather than by restating
// either.
func finTrailerOutcomeValues() []string {
	return append(trailRunOutcomeValues(), finOutcomeValues()...)
}

// TestFinTrailerRecordOutcomeIsConsumedAsHanded pins that the outcome field is a
// projection: whatever the caller hands in comes back, and every value it can
// carry resolves to exactly one of the two closed sets a reader can look up.
func TestFinTrailerRecordOutcomeIsConsumedAsHanded(t *testing.T) {
	t.Run("an outcome disagreeing with the state beside it is carried untouched", func(t *testing.T) {
		// THE ONLY ROWS THAT BITE. The outcome is an INPUT, so a row where it
		// agrees with the State would pin nothing — a builder deriving the outcome
		// from State returns the same value there. It changes the value on
		// precisely these two rows and on no others.
		//
		// Both are CONTRACT CHECKS ON A PROJECTION PURE OVER ITS INPUTS, not
		// claims that such a run occurs: a real staging failure never reaches the
		// run classifier at all (finding_staging_gate_test.go:16-38), and
		// trailClassifyRun returns trailOutcomeVoidNoTrailer only for a run whose
		// trailer was absent.
		tests := []struct {
			name    string
			outcome string
			scan    trailScanResult
		}{
			{
				name:    "a staging value beside a trailAbsent scan",
				outcome: finOutcomeReadyToClassify,
				scan:    finTrailerAbsentScan(),
			},
			{
				name:    "the no-trailer void beside a trailSeen scan",
				outcome: trailOutcomeVoidNoTrailer,
				scan:    finTrailerSeenScan(),
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				rec := finTrailerBuild(tc.outcome, trailObservation{
					trailScanResult: tc.scan,
					BoundFrom:       trailBoundNone,
				})
				if rec.Outcome != tc.outcome {
					t.Errorf("outcome: got %q, want %q — the value is consumed as returned, never "+
						"re-derived from the %q state beside it and never renamed",
						rec.Outcome, tc.outcome, rec.State)
				}
				if rec.State != tc.scan.State {
					t.Errorf("state: got %q, want %q — neither field is adjusted to agree with "+
						"the other; a disagreement has no value in either closed set and "+
						"inventing one is out of scope", rec.State, tc.scan.State)
				}
			})
		}
	})

	t.Run("the two closed sets are disjoint, so a carried value resolves to one", func(t *testing.T) {
		// Called through the two shipped predicates over the two shipped lists.
		// Restating either set here would fork it, and the fork would be invisible
		// until a reader could not look a published value up.
		for _, v := range trailRunOutcomeValues() {
			if !trailIsRunOutcome(v) {
				t.Errorf("%q is listed among the run outcomes but trailIsRunOutcome rejects it", v)
			}
			if finOutcomeIsValue(v) {
				t.Errorf("%q is a run outcome AND a staging value: the two sets must be disjoint, "+
					"or a carried value resolves to two different meanings", v)
			}
		}
		for _, v := range finOutcomeValues() {
			if !finOutcomeIsValue(v) {
				t.Errorf("%q is listed among the staging values but finOutcomeIsValue rejects it",
					v)
			}
			if trailIsRunOutcome(v) {
				t.Errorf("%q is a staging value AND a run outcome: the two sets must be disjoint",
					v)
			}
		}

		distinct := map[string]bool{}
		for _, v := range finTrailerOutcomeValues() {
			distinct[v] = true
		}
		if len(distinct) != 18 {
			t.Errorf("the union holds %d distinct value(s), want 18 — eleven run outcomes and "+
				"seven staging values, and it is their disjointness that makes ONE field safe "+
				"for TWO sources", len(distinct))
		}
	})

	t.Run("every value the field can carry comes back exactly as handed", func(t *testing.T) {
		// The coverage loop, so no member of either set is silently unexercised.
		// One fixed observation: the outcome is the only thing varying.
		obs := trailObservation{
			trailScanResult: finTrailerSeenScan(),
			Staleness:       250 * time.Millisecond,
			BoundFrom:       trailBoundFromMiss,
		}
		for _, v := range finTrailerOutcomeValues() {
			rec := finTrailerBuild(v, obs)
			if rec.Outcome != v {
				t.Errorf("outcome: got %q, want %q", rec.Outcome, v)
			}
			// The field is deliberately NOT asked to reject a non-member: no
			// builder in this family validates its value (trailRunOutcome:233,
			// finOutcomeResult:198), because membership lives in the two
			// reader-facing predicates called above.
			if !trailIsRunOutcome(rec.Outcome) && !finOutcomeIsValue(rec.Outcome) {
				t.Errorf("outcome %q is in neither closed set: a value a reader of a published "+
					"record cannot look up is a verdict they cannot interpret", rec.Outcome)
			}
		}
	})
}

// TestFinTrailerRecordCarriesNoCapturedBytes is the record's own construction
// claim made checkable rather than advisory: no trailer line in any form, capped
// or otherwise, in a field or quoted into the detail.
//
// # Why the plant lands INSIDE the cap, and why the family's usual plant would not
//
// trailNeedle sits at a fixed offset inside trailPaddedTrailer's `result` field,
// so its position in the rendered line is 104 + pad. The pads this family plants
// with put it well PAST the 512-byte cap — 2000 at
// result_trailer_observation_test.go:482 and trailer_admissibility_test.go:553,
// and trailOverlongPad — which is precisely the point there: only a record
// recording the line IN FULL leaks it. That makes it the wrong plant for THIS
// claim. A record carrying the CAPPED Line publishes ~415 bytes of model-chosen
// text while a past-the-cap needle sweep passes — a green proof of a false
// claim, which is worse than no proof.
//
// trailPaddedTrailer(0) renders 385 bytes against the 512-byte cap with the
// needle at offset 104-146, so it survives into Line intact and a record that
// kept the line is caught. The offset is a property of the PAD rather than an
// invariant of the needle, so it is asserted on the scan result below rather
// than trusted.
//
// # Line is the only channel swept
//
// resultTrailer has no `result` member, so no plant placed in the trailer's
// `result` field can reach this record through the decode — it can only arrive
// by way of Line. The four decoded fields are a different matter: they cross
// into the record VERBATIM BY DESIGN, which is what AC2 asks for, so a needle
// planted in them would be pinning against AC2 rather than for it. The full
// multi-input sweep across every artifact input is #1286's and is not restated
// here.
func TestFinTrailerRecordCarriesNoCapturedBytes(t *testing.T) {
	line := trailPaddedTrailer(0)
	scan := trailScan([]byte(line + "\n"))

	if scan.State != trailSeen {
		t.Fatalf("fixture state: got %q (%s), want %q", scan.State, scan.Detail, trailSeen)
	}
	// THE NON-VACUITY PRECONDITION. Without it the whole test is theatre: a
	// needle the cap already ate is absent from a leaking record too.
	if end := strings.Index(line, trailNeedle) + len(trailNeedle); end > reachMaxCommandBytes {
		t.Fatalf("the needle ends at byte %d of the %d-byte fixture line, past the %d-byte cap: "+
			"a record that kept the capped line would pass this test", end, len(line),
			reachMaxCommandBytes)
	}
	if !strings.Contains(scan.Line, trailNeedle) {
		t.Fatalf("precondition: the capped line does not carry the needle, so the sweep below "+
			"would pass against a record that kept it verbatim. Line is %d bytes", len(scan.Line))
	}

	rec := finTrailerBuild(trailOutcomeVoidBudgetFired, trailObservation{
		trailScanResult: scan,
		Staleness:       250 * time.Millisecond,
		BoundFrom:       trailBoundFromMiss,
	})

	// THE HEADROOM, ASSERTED PER ROW RATHER THAN ARGUED IN PROSE. trailDetail
	// caps the formatted detail at reachMaxCommandBytes, so a Detail that had
	// wrongly interpolated the line would be truncated before the needle if the
	// surrounding prose left no room — and the containment checks below would
	// then pass against a leaking implementation. That is the defect #1284
	// shipped. Pinning the room keeps it impossible, and keeps it impossible
	// after a later edit lengthens a Detail: the failure lands here, naming the
	// record, rather than silently disarming the sweep.
	if room := reachMaxCommandBytes - len(rec.Detail); room < len(trailNeedle) {
		t.Errorf("the detail is %d bytes, leaving %d of trailDetail's %d-byte cap against a "+
			"%d-byte needle: a detail that leaked the line would be truncated before the needle "+
			"and the checks below would pass against it. Shorten the detail — the long-form "+
			"argument belongs in a comment, which no cap applies to",
			len(rec.Detail), room, reachMaxCommandBytes, len(trailNeedle))
	}

	// Named separately from the marshal sweep so the failure message says WHICH
	// channel leaked.
	if strings.Contains(rec.Detail, trailNeedle) {
		t.Errorf("the detail quotes the trailer line: %q", rec.Detail)
	}

	encoded, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling the trailer record: %v", err)
	}
	if bytes.Contains(encoded, []byte(trailNeedle)) {
		t.Errorf("the marshalled record carries verbatim model output from inside the cap: %s",
			encoded)
	}

	// The structural half: the record has no field for a line today, and this is
	// the check that a future field does not quietly add one. The scan is valid
	// because finTrailerRecord is FLAT — ten scalars — so a top-level key scan
	// examines every key it has. Lifted onto a record with a struct-valued field
	// it would never examine the inner keys.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keyed); err != nil {
		t.Fatalf("decoding the marshalled trailer record: %v", err)
	}
	for _, forbidden := range []string{"line", "trailer_line", "result", "raw"} {
		for key := range keyed {
			if strings.Contains(key, forbidden) {
				t.Errorf("the record carries key %q, which is %q-shaped: this record's whole "+
					"value is that it can be published unreviewed, and a line column would "+
					"inherit the operator-review-before-paste obligation onto it", key, forbidden)
			}
		}
	}
}
