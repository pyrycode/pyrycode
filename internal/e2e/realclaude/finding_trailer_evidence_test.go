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
// pass as a real one. Since #1320 this record reads the four fields from a
// finSighting (finding_run_gather_test.go:372), which answers the State/nil PAIR
// as CarriesTrailer one tier up and hands the four on as scalars — so the
// optional is discriminated where the pointer still exists, and a run that wrote
// no trailer carries its void down here with nothing left to dereference.
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
// "COUNTS, NEVER ROWS" (trail_run_outcome_test.go:462), finAttributeEntry states
// the exclusion as its own construction (finding_attribution_fanout_test.go:80-88),
// and finOutcomeResult is a value and a detail and nothing else
// (finding_staging_gate_test.go:198-201). This record's trailer evidence is its
// State and the four decoded fields. Never its bytes.
//
// # Reused, not rebuilt
//
// trailScan (result_trailer_observation_test.go:180) and its fixtures
// trailFixtureTrailer (:307), trailFixtureNoTrailer (:316), trailNeedle (:325),
// trailPaddedTrailer (:331) and trailOverlongPad (:342) are the shipped scan and
// the shipped plants — a second scanner over the same bytes that disagreed would
// be worse than either. The three scan states (:57-71) and the three lateness
// discriminators (:75-90) are shipped closed spaces, called and never restated.
// trailIsRunOutcome (trail_run_outcome_test.go:541) and trailRunOutcomeValues
// (:2468) are the sixteen; finOutcomeIsValue (finding_staging_gate_test.go:210)
// and finOutcomeValues (:223) the staging tier's seven — called, never
// re-derived. reachMaxCommandBytes and reachCapCommand
// (background_reach_probe_test.go:123, :945) are the single-sourced cap.
//
// trailDetail (trailer_admissibility_test.go:276) is reused rather than given a
// finDetail twin, for the reason merged code has settled twice
// (finding_attribution_fanout_test.go:37-44, finding_staging_gate_test.go:73-81):
// it carries no decision — fmt.Sprintf plus reachCapCommand's 512-byte cap — and
// a file that calls trailIsRunOutcome is BY DESIGN inside the trail* family's
// reach. A twin would only fork the cap.

import (
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
// discipline a later edit can break. SO IS THE BUILDER since #1320 moved it onto
// finSighting: the two claims now hold at two tiers rather than one of them
// covering for the other, and finTrailerBuild says so on its own side so neither
// is read as covering the other.
//
// TRAP-FREE IS A CLAIM ABOUT THREE THINGS AND NOT ABOUT ALL BYTES: no
// trailScanResult.Line, no *resultTrailer and no PermissionDenials in reach. It
// does not say that nothing model-influenced crosses — StopReason does, uncapped
// and by design, which is what the section below is for.
//
// The pointer is severed ONE TIER UP, at the carrier fill, which is where it
// still exists. finSighting has no pointer field at all, so no two records built
// from one carrier can alias a shared *resultTrailer: the property is structural
// now rather than bought at build time, and it is CHECKED by
// TestFinSightingReachesNoScanType (finding_run_gather_test.go:2043) — which
// walks the carrier's type for all three scan types, so a later field carrying
// one of them a level down fails there — rather than asserted in prose.
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
// # The key names, and why they are not a fifth scalar
//
// KeyNames comes from a DIFFERENT READER than the four above: trailKeyNames over
// the full line (trailer_key_names_test.go:87), not resultTrailer's fixed decode.
// It is the fifth trailer FIELD and deliberately not a fifth decoded scalar,
// which is what leaves this file's "the four decoded scalars" sentences true.
//
// It exists because that fixed decode cannot answer one question. terminal_reason
// is pyry's own invention — wireFields (emitter.go:428-437) and the idle-stall
// trailer write it and nothing else does — so claude's own result line, the
// healthy shape on the headless path, carries no such KEY at all. After the decode
// an ABSENT terminal_reason and one emitted as "" are the same value, so the NAME
// is the only thing separating "pyry wrote this trailer, therefore the watchdog
// fired" from "claude wrote it, therefore the run was healthy". A record
// publishing a verdict about such a run while withholding that discriminator
// cannot be audited from the filed artifact alone.
//
// Names and never values, structurally rather than by discipline: trailKeyNames
// returns []string and discards its map[string]json.RawMessage internally, and
// TestTrailKeyNamesCarryNoValues (trailer_key_names_test.go:310) plants a distinct
// needle in every string-valued position of a trailer line and asserts none
// reaches them. They still come from CLAUDE, which is why the artifact's standing
// safety sentence names this field rather than leaving it to be discovered
// (finWriteSafetyClaim, finding_artifact_write_test.go:124).
//
// NO OMITEMPTY, for the blanket reason above — and the collapse that rule defends
// against is unreachable on this field anyway, which is stated here rather than
// left incidental. trailScan's match return is past tr.Type == "result" and so
// reachable only from a line that already decoded as a JSON object, so the map
// decode always succeeds and always carries at least `type`
// (result_trailer_observation_test.go:199-205). A SEEN trailer therefore cannot
// produce an empty name set; "no names" is reachable only from the not-seen arm.
// Measured on this tree rather than reasoned, the three shapes stay distinct: nil
// renders {"trailer_keys":null}, empty renders {"trailer_keys":[]}, filled renders
// {"trailer_keys":["type"]}. trailScanResult's own tier uses omitempty (:135) and
// is not a precedent here.
//
// # What the bound does to a reader, stated so it cannot mislead
//
// finBoundKeyNames (finding_run_gather_test.go) caps the count and each name's
// length, so a list at exactly finTrailerMaxKeyNames entries MAY BE AN ALPHABETIC
// PREFIX — and a name sorting late, terminal_reason among them, could then be
// absent from a line that carried it. That is unreachable from a real run: no
// producer emits a result line with more than eleven top-level keys, the keys
// being the CLI's envelope rather than the model's text, so the bound is a cap on
// the artifact rather than a live defence. NO NAME IS SPECIAL-CASED to survive the
// cut, because a hard-coded keep-list would be a second source of truth for what
// the line carried. Two names sharing a finTrailerMaxKeyNameBytes-byte prefix
// likewise collapse to one string — both carrying reachTruncationMarker, so the
// duplication is visibly an artefact rather than a reading.
//
// # The Detail's content rule, pinned rather than left to judgement
//
// In trailRunOutcome.Detail's shape (trail_run_outcome_test.go:465-475), it MAY
// name the outcome value, the scan state, BoundFrom, Bounded as a boolean and
// the four decoded fields — permitted because the record already publishes them
// as fields, so the exposure decision is this type's and the Detail adds nothing
// to it. It may NEVER quote trailScanResult.Line or interpolate any part of it,
// INCLUDING a length, a byte count derived from it, a prefix or a hash.
//
// THE KEY NAMES ARE NOT ON THE PERMITTED LIST, and that is its own decision rather
// than a consequence of the Line rule: the Detail may name no key and interpolate
// no COUNT of them, both being derived from the line. The shipped row below cannot
// detect either: its fixture's eleven short names fit inside the headroom even if
// the Detail interpolated them. The NAMES half is made red by #1362's needle sweep,
// which plants a needle AS a top-level key name and asserts no Detail in the
// artifact carries it. The COUNT half has no instrument and stays unproven.
//
// Every Detail must also leave len(trailNeedle) bytes of headroom under
// reachMaxCommandBytes — under 470 bytes on every row. That is not stylistic:
// trailDetail caps at 512, so a house-style multi-sentence Detail can eat the
// budget and let a leak be truncated away, turning the byte sweeps that RECURSE
// INTO THIS RECORD green against a record that did leak — TestFinRecordCarriesNoCapturedBytes'
// marshal sweep and the artifact writer's Detail walk, both of which reach this
// Detail because finRecordRun embeds the record whole. That is the defect #1284
// shipped and then had to fix.
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

	KeyNames []string `json:"trailer_keys"`

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
// LIKE THE RECORD IT RETURNS, THIS FUNCTION IS TRAP-FREE BY CONSTRUCTION. Its
// input is a finSighting (finding_run_gather_test.go:372), which carries neither
// trailScanResult.Line nor the *resultTrailer, so the no-captured-bytes property
// of this builder is held BY THE SHAPE OF THE INPUT — and that shape is checked
// rather than asserted: TestFinSightingReachesNoScanType
// (finding_run_gather_test.go:2043) walks the carrier for all three scan types.
// The Detail content rule stated on finTrailerRecord keeps shut the one channel
// a scalar-only input still leaves open, which is what the Detail may SAY.
//
// Trap-free is not a claim that nothing model-influenced crosses. StopReason is
// forwarded from the model's last message uncapped and BY DESIGN, and this
// record's own doc says so — a sweep author planting a needle in it would be
// planting in a field the record must carry verbatim.
//
// # The outcome is consumed, never decided
//
// The value comes from one of two closed sets built elsewhere: trailClassifyRun's
// sixteen (trail_run_outcome_test.go:638) and the staging-gate tier's seven
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
// validates its value — trailRunOutcome:476 and finOutcomeResult (:198) are
// plain structs — because membership lives in the reader-facing predicates,
// whose job is that "a value a reader of a published record cannot look up is a
// verdict they cannot interpret".
//
// # The guard reads one bool, and the drop is a DECISION
//
// There is no pointer here and nothing to dereference: the carrier already
// answered the State/nil PAIR as CarriesTrailer, and the ordering argument — the
// State operand FIRST, Go's && short-circuiting left to right — lives wherever
// that pair is computed. That is finGatherReadings' fill on the live path
// (finding_run_gather_test.go:557-582) and finTrailerSighting on the fixture
// side, and the two are required to agree.
//
// On the false arm the four AND THE KEY NAMES are ZEROED rather than copied
// through, and under the carrier that is a choice rather than a consequence: they
// are separately settable, so a CarriesTrailer: false beside a non-zero Subtype or
// a filled name list is a reachable input and is the only shape separating a
// builder that drops from one that copies through. THE ARGUMENT IS IN THE ARM'S
// OWN DETAIL, which formats "the four trailer fields and the key names hold their
// zero values" — a builder copying them through would publish a record
// contradicting itself in the same breath.
// That wording is load-bearing for this reason, and
// TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer drives both
// arms over one carrier and its one flipped bit.
//
// What is unchanged: the zeroes are published under WHATEVER State was handed —
// this record has no out-of-contract value to report an inconsistent carrier
// with, so the honest behaviour is to carry the void rather than collapse it —
// and the Detail names which arm fired. One if, two Detail shapes; no third
// branch, no reject arm.
func finTrailerBuild(outcome string, sighting finSighting) finTrailerRecord {
	rec := finTrailerRecord{
		Outcome:   outcome,
		State:     sighting.State,
		BoundFrom: sighting.BoundFrom,
		Staleness: sighting.Staleness,
		// Bounded is BoundFrom == trailBoundFromMiss AND NOTHING ELSE.
		// trailBoundFromStart carries a real duration that bounds nothing (the
		// first poll already matched, so the trailer may have been visible before
		// the loop began) and trailBoundNone is the honest no-bound, so a record
		// deriving this from Staleness != 0 would publish a non-bound wearing a
		// bound's label — trailRunOutcome:459-463's rule, unchanged.
		Bounded: sighting.BoundFrom == trailBoundFromMiss,
	}

	if !sighting.CarriesTrailer {
		rec.Detail = trailDetail("outcome %s over a %s scan carrying no decoded trailer, so the "+
			"four trailer fields and the key names hold their zero values; lateness %s, "+
			"bounded=%t", outcome, sighting.State, sighting.BoundFrom, rec.Bounded)
		return rec
	}

	rec.Subtype = sighting.Subtype
	rec.IsError = sighting.IsError
	rec.TerminalReason = sighting.TerminalReason
	rec.StopReason = sighting.StopReason
	// A PLAIN COPY, and deliberately not a second bounding call: the carrier's fill
	// already applied finBoundKeyNames, and a cap written twice is a cap that stops
	// agreeing with itself. That the assignment shares no backing array with the
	// scan's own slice is finBoundKeyNames' clause, held at the producer.
	rec.KeyNames = sighting.KeyNames
	rec.Detail = trailDetail("outcome %s over a %s scan; subtype=%s is_error=%t "+
		"terminal_reason=%s stop_reason=%s, read from the decode of the full line; "+
		"lateness %s, bounded=%t", outcome, sighting.State, rec.Subtype, rec.IsError,
		rec.TerminalReason, rec.StopReason, sighting.BoundFrom, rec.Bounded)
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

// finTrailerSighting is the fixture-side stand-in for finGatherReadings' carrier
// fill, and is a COPY of it (finding_run_gather_test.go:557-582): the same State
// operand first, the same four scalars filled only behind the pair.
//
// THE AGREEMENT OBLIGATION LANDS HERE. That fill is commented as deliberately
// identical to the builder's guard, because the builder was going to move onto
// the carrier; now that #1320 has moved it, the builder computes nothing and
// this is the second computation of the pair. The two are REQUIRED TO AGREE, and
// no in-file pin is available for it: finGatherReadings reaches ps through
// pinScanArgv and pinReadState, and this file forbids exec by its own header.
// The obligation is held by this comment and by review, exactly as it is on the
// gather's side.
//
// #1363 PUT A SECOND COMPUTATION UNDER THAT SAME OBLIGATION: the key-name fill,
// which routes through finBoundKeyNames on both sides so the cap exists in one
// copy rather than two. Its asymmetry runs the dangerous way — every offline test
// drives THIS side, so bounding here and forgetting the gather's would ship an
// unbounded field on every live probe run with nothing red.
//
// It takes the SHIPPED SCAN rather than a trailObservation, for two reasons. No
// fixture in this family constructs an observation for the builder again — the
// builder's whole point after #1320 is that the type it accepts is not one a
// scan produces. And the four scalars and the pair are DERIVED from bytes
// trailScan actually read, so a row can never assert against a pairing the
// scanner would not have returned for those bytes; the one pairing no scanner
// can return is built by flipping a bit off a derived carrier and named as
// synthetic where it is used.
//
// A function rather than a package-level var, for trailRunWellFormed's stated
// reason (trail_run_outcome_test.go:1111-1113): a shared backing value is
// reachable from every test in this package and `go test -race` runs them in
// parallel.
func finTrailerSighting(scan trailScanResult, staleness time.Duration, boundFrom string) finSighting {
	sighting := finSighting{State: scan.State, BoundFrom: boundFrom, Staleness: staleness}
	sighting.CarriesTrailer = scan.State == trailSeen && scan.Trailer != nil
	if sighting.CarriesTrailer {
		sighting.Subtype = scan.Trailer.Subtype
		sighting.IsError = scan.Trailer.IsError
		sighting.TerminalReason = scan.Trailer.TerminalReason
		sighting.StopReason = scan.Trailer.StopReason
		sighting.KeyNames = finBoundKeyNames(scan.KeyNames)
	}
	return sighting
}

// TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator drives all three scan
// states and all three lateness discriminators, and pins that the bounded
// boolean is true on trailBoundFromMiss ALONE — even on the two rows that carry
// a non-zero Staleness beside a discriminator that bounds nothing. Those rows
// are the ones that bite: a builder deriving Bounded from Staleness != 0 agrees
// with this one everywhere else.
//
// The carriers come from finTrailerSighting over a shipped scan, with Staleness
// and BoundFrom handed in — which needs no clock, no poll and no live turn.
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
			// LIVE pairing, not synthetic: trailWaitForTrailer:297 sets
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
				"result_trailer_observation_test.go:289-290 and the deadline arm at :295 — leave " +
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

			rec := finTrailerBuild(trailOutcomeVoidNoTrailer,
				finTrailerSighting(tc.scan, tc.staleness, tc.boundFrom))

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

// RETIRED BY #1325: the shell TestFinTrailerRecordReadsTheDecodedTrailer and its
// "a no-trailer observation returns rather than panicking" row, which #1320
// migrated onto the carrier and marked. Under the carrier that row's assertions
// are TAUTOLOGICAL — an absent scan gives finTrailerSighting a false
// CarriesTrailer and four zero scalars, so the record publishes zeroes whether
// the builder guards or not, and there is no pointer left for an unguarded build
// to panic on. Keeping it for its assertions would keep a second test of nothing.
//
// Each claim it made is held elsewhere, BY SYMBOL:
//
//   - the panic-on-unchecked-deref obligation, by TestFinSightingReachesNoScanType
//     (finding_run_gather_test.go), and STRONGER: it walks the carrier's TYPE for
//     all three scan types, so a later field carrying one a level down fails there,
//     where the retired row asserted over a single instance. Its failure message
//     names the obligation.
//   - the four zero fields, by
//     TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer, and
//     STRONGER: it drives both arms off one carrier and its one flipped bit, so
//     neither an always-zero nor an always-copy builder survives, where the retired
//     assertions pass against a builder with no guard at all.
//   - rec.State == trailAbsent, by
//     TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator's absent row — the
//     SAME property, under an executable coverage claim that goes red if that row
//     is ever dropped.
//
// The outcome is not a survival candidate: the retired row never asserted it, and
// TestFinTrailerRecordOutcomeIsConsumedAsHanded owns it, including "every value
// the field can carry comes back exactly as handed".
//
// The shell's other row survives as the test below, promoted to top-level: with
// one child left a t.Run is ceremony, and the shell's name pointed at a record
// where the surviving property is finTrailerSighting's.

// TestFinTrailerSightingScalarsComeFromTheFullLineDecode proves finTrailerSighting
// fills the four scalars from the decode of the FULL trailer line and never from a
// re-read of the capped copy, MEASURED on a fixture where the two reads give
// different answers rather than trusted because trailScanResult.Trailer's doc says
// which one it is.
//
// # The name says which half of an agreement obligation this pins
//
// It mirrors TestFinGatherSightingScalarsComeFromTheFullLineDecode
// (finding_run_gather_test.go) with the prefix naming WHICH of the two functions
// the obligation binds. finTrailerSighting is a copy of finGatherReadings' carrier
// fill and the two are REQUIRED TO AGREE; a grep for
// ScalarsComeFromTheFullLineDecode returns both halves, which says that without
// prose.
//
// # Why this row cannot be retired against either sibling
//
// Not against the gather's: that pins finGatherReadings' fill, a DIFFERENT
// function, and the two are required to agree precisely because neither proves the
// other. That obligation is otherwise held only by comment and by review —
// finGatherReadings reaches ps through pinScanArgv and pinReadState, and this file
// forbids exec by its own header — so this row is the ONLY executable pin on the
// helper's half of it, for the decode-vs-capped question specifically.
//
// Not against TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer
// either. Its precondition pins that the helper fills the four AT ALL; it cannot
// tell which source they came from, because its fixture is trailPaddedTrailer(0),
// wholly inside the cap, where the two candidate reads agree.
//
// # The subject is the carrier, not a built record
//
// Since #1320 the builder reads no line, so a record asserted here would prove
// nothing about which read the four came from — it copies whatever the carrier
// holds, and that carry-through is pinned by the Fills test over a fixture whose
// pad is irrelevant to it. The over-cap dimension was only ever about the HELPER's
// read, so that is what the row asserts on.
//
// # The expectations stay literals
//
// Deliberately not the sibling's `want := *scan.Trailer` form. One tier down that
// is literal-free because the gather runs its OWN scan over the same bytes, so
// want and the subject are two computations. Here the helper is handed the very
// scan this test built, so a derived expectation would compare a value to itself
// across a two-line assignment. The literals are what tie the assertion to the
// full-line decode.
//
// Failure messages name len(scan.Line) and never render it, the sibling's rule
// (finding_run_gather_test.go's "# Failure messages"): at this pad the retained
// copy carries padded stand-in payload, and no sweep covers this row's fixture.
func TestFinTrailerSightingScalarsComeFromTheFullLineDecode(t *testing.T) {
	// Pad 2000 is the plant this family already uses at
	// result_trailer_observation_test.go:507. Any pad from 142 up satisfies the
	// precondition; it is ASSERTED below rather than assumed, so a later fixture
	// change surfaces as a failed precondition instead of as a silently weaker
	// test.
	scan := trailScan([]byte(trailPaddedTrailer(2000) + "\n"))
	if scan.State != trailSeen {
		t.Fatalf("fixture state: got %q (%s), want %q", scan.State, scan.Detail, trailSeen)
	}
	if strings.Contains(scan.Line, "terminal_reason") {
		t.Fatalf("precondition: the capped line still contains terminal_reason, so this row "+
			"would pass even against a helper that read the four fields off the CAPPED copy. "+
			"Line is %d bytes", len(scan.Line))
	}

	sighting := finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss)

	// The pair, mirroring the same precondition in
	// TestFinGatherSightingScalarsComeFromTheFullLineDecode — and honestly
	// labelled: at THIS tier a false pair leaves the four zero and the checks below
	// go RED rather than vacuous, so this is a DIAGNOSIS guard (one Fatalf naming
	// the pair beats four value mismatches) and NOT a non-vacuity guard. Written as
	// the latter it would be a false claim.
	if sighting.State != trailSeen || !sighting.CarriesTrailer {
		t.Fatalf("the carrier reports scan state %q carrying a decoded trailer %t; want %q "+
			"carrying one — the four scalars are filled only behind that pair, so a failure here "+
			"explains the four below rather than adding to them", sighting.State,
			sighting.CarriesTrailer, trailSeen)
	}

	// Pinned ON THE CARRIER. The scan result's own survival of the cap is already
	// pinned at result_trailer_observation_test.go:502-530 and is not restated
	// here; what is new is that the HELPER reads Trailer and not Line.
	for _, f := range []struct{ name, got, want string }{
		{"subtype", sighting.Subtype, "error_max_turns"},
		{"terminal_reason", sighting.TerminalReason, "max_turns"},
		{"stop_reason", sighting.StopReason, "end_turn"},
	} {
		if f.got != f.want {
			t.Errorf("%s: got %q, want %q — terminal_reason is LAST on the pinned wire order "+
				"(emitter.go:456-468) and ~2 KiB past the cap here, so a carrier filled from the "+
				"capped Line could not have recovered it", f.name, f.got, f.want)
		}
	}
	if !sighting.IsError {
		t.Error("is_error: got false, want true — the budget-fired reading must cross too")
	}
}

// TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer drives the
// guard in BOTH directions, which no single arm can: a builder that ignores
// CarriesTrailer and always ZEROES passes every false arm in this file, and one
// that always COPIES THROUGH passes every true one.
//
// The row the false arm needs — CarriesTrailer false beside four non-zero
// scalars — is a pairing NO SCAN PRODUCES, and it is kept in the same register
// as the aborted row's synthetic string above: a contract check on a builder
// pure over its inputs, not a reading. Under the observation it was not even
// constructible, because !carriesTrailer implied there was no pointer to read
// and the zeroes were a consequence. Under the carrier the scalars are
// separately settable, so the drop is a DECISION — and its argument is the arm's
// own Detail, which formats "the four trailer fields and the key names hold
// their zero values". A builder copying them through publishes a record
// contradicting itself in the same breath.
//
// The row is DERIVED from the true arm by flipping the single impossible bit
// rather than typed in: everything a scan could produce still comes from the
// scan, and the two carriers differ in exactly one field, so nothing else can
// explain a difference between the two records.
func TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer(t *testing.T) {
	// trailPaddedTrailer and NOT finTrailerSeenScan: trailFixtureTrailer renders
	// is_error FALSE, and against it the false arm's is_error check compares false
	// to false and passes on a builder that copied the field through. All four
	// must be non-zero on the true arm or a quarter of the contrast is theatre.
	// The pad is irrelevant here — the carrier holds no line for a needle to sit
	// in — so it is the shortest one this family plants with.
	scan := trailScan([]byte(trailPaddedTrailer(0) + "\n"))
	if scan.State != trailSeen || scan.Trailer == nil {
		t.Fatalf("fixture: got state %q carrying a decoded trailer %t (%s), want %q carrying one — "+
			"with no decode there is nothing for either arm to be about", scan.State,
			scan.Trailer != nil, scan.Detail, trailSeen)
	}

	seen := finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss)
	// The synthetic carrier, and the only hand-set field in this test.
	dropped := seen
	dropped.CarriesTrailer = false

	// THE NON-VACUITY PRECONDITION, first and fatal. Without it a helper that
	// filled nothing would leave both arms comparing zero against zero.
	if !seen.CarriesTrailer {
		t.Fatalf("the carrier reports CarriesTrailer false over a %q scan carrying a decode: the "+
			"true arm below would then assert the very zero values it exists to separate from",
			scan.State)
	}
	if seen.Subtype == "" || seen.TerminalReason == "" || seen.StopReason == "" || !seen.IsError ||
		len(seen.KeyNames) == 0 {
		t.Fatalf("the carrier holds subtype=%q is_error=%t terminal_reason=%q stop_reason=%q and %d "+
			"key names: every one must be non-zero, or the false arm's check on the zero-valued one "+
			"passes against a builder that copied it through", seen.Subtype, seen.IsError,
			seen.TerminalReason, seen.StopReason, len(seen.KeyNames))
	}

	filled := finTrailerBuild(trailOutcomeVoidBudgetFired, seen)
	zeroed := finTrailerBuild(trailOutcomeVoidBudgetFired, dropped)

	t.Run("the four cross behind a carrier that says it holds them", func(t *testing.T) {
		for _, f := range []struct{ name, got, want string }{
			{"subtype", filled.Subtype, seen.Subtype},
			{"terminal_reason", filled.TerminalReason, seen.TerminalReason},
			{"stop_reason", filled.StopReason, seen.StopReason},
		} {
			if f.got != f.want {
				t.Errorf("%s: got %q, want %q — the carrier's own value, carried", f.name, f.got,
					f.want)
			}
		}
		if filled.IsError != seen.IsError {
			t.Errorf("is_error: got %t, want %t — the carrier's own value, carried", filled.IsError,
				seen.IsError)
		}
	})

	t.Run("the four are dropped behind a carrier that says it holds none", func(t *testing.T) {
		if zeroed.IsError {
			t.Errorf("is_error: got true, want false — CarriesTrailer is false, so the field has no "+
				"decode behind it and must claim nothing; detail %q", zeroed.Detail)
		}
		for _, f := range []struct{ name, got string }{
			{"subtype", zeroed.Subtype},
			{"terminal_reason", zeroed.TerminalReason},
			{"stop_reason", zeroed.StopReason},
		} {
			if f.got != "" {
				t.Errorf("%s: got %q, want empty — the arm's own Detail says the four hold their "+
					"zero values, so a builder copying this carrier's scalars through publishes a "+
					"record contradicting itself in the same breath", f.name, f.got)
			}
		}
		if len(zeroed.KeyNames) != 0 {
			t.Errorf("trailer_keys: got %q, want none — the key names are dropped with the four and "+
				"the arm's own Detail says so; a builder copying them through publishes claude's own "+
				"key names beside a record that says it carries no trailer", zeroed.KeyNames)
		}
	})

	t.Run("the rest of the carrier crosses either way", func(t *testing.T) {
		// The void is carried UNDER THE STATE THAT SAYS SO rather than collapsed
		// into "there was no trailer": the drop is the four scalars' and the key
		// names', and reaches nothing else on the record.
		if zeroed.State != seen.State || zeroed.BoundFrom != seen.BoundFrom ||
			zeroed.Staleness != seen.Staleness {
			t.Errorf("state %q, bound %q, staleness %v; want %q, %q, %v — only the four are dropped",
				zeroed.State, zeroed.BoundFrom, zeroed.Staleness, seen.State, seen.BoundFrom,
				seen.Staleness)
		}
		if !zeroed.Bounded {
			t.Errorf("bounded: got false, want true on %q — Bounded is BoundFrom == %s and nothing "+
				"else, and CarriesTrailer is not one of its inputs", zeroed.BoundFrom,
				trailBoundFromMiss)
		}
	})

	// That the guard BRANCHED, in one line and deliberately not as a string match
	// on the wording: what enforces the Detail's claim is the four assertions
	// above, and a match here would only couple this test to its phrasing.
	if filled.Detail == zeroed.Detail {
		t.Errorf("both arms published the same detail %q: a record that cannot say which arm fired "+
			"is indistinguishable from a reading", filled.Detail)
	}
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
				rec := finTrailerBuild(tc.outcome,
					finTrailerSighting(tc.scan, 0, trailBoundNone))
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
		if len(distinct) != 23 {
			t.Errorf("the union holds %d distinct value(s), want 23 — sixteen run outcomes and "+
				"seven staging values, and it is their disjointness that makes ONE field safe "+
				"for TWO sources. Sixteen rather than fifteen since #1448 separated the arm that "+
				"reads an absent terminal_reason on a path that owes none once more: a run whose "+
				"pinned-pid sighting route was NEVER STAGED reports the absence of an instrument, "+
				"which is not the nothing a staged route measured", len(distinct))
		}
	})

	t.Run("every value the field can carry comes back exactly as handed", func(t *testing.T) {
		// The coverage loop, so no member of either set is silently unexercised.
		// One fixed carrier: the outcome is the only thing varying.
		sighting := finTrailerSighting(finTrailerSeenScan(), 250*time.Millisecond,
			trailBoundFromMiss)
		for _, v := range finTrailerOutcomeValues() {
			rec := finTrailerBuild(v, sighting)
			if rec.Outcome != v {
				t.Errorf("outcome: got %q, want %q", rec.Outcome, v)
			}
			// The field is deliberately NOT asked to reject a non-member: no
			// builder in this family validates its value (trailRunOutcome:436,
			// finOutcomeResult:198), because membership lives in the two
			// reader-facing predicates called above.
			if !trailIsRunOutcome(rec.Outcome) && !finOutcomeIsValue(rec.Outcome) {
				t.Errorf("outcome %q is in neither closed set: a value a reader of a published "+
					"record cannot look up is a verdict they cannot interpret", rec.Outcome)
			}
		}
	})
}

// RETIRED BY #1325: the two in-cap preconditions, the detail-quotes-the-line
// check and the marshalled-record containment check. Since #1320 the builder's
// input is a finSighting, which has no .Line for a plant to sit in, so each of
// those asserted the absence of a needle its input structurally cannot carry —
// not a weakened test but a test of nothing, and a green proof of a false claim
// is worse than no proof.
//
// THE CLAIM THEY MADE STILL HOLDS, at the step that now drops the line and more
// strongly than it ever could here. TestFinGatherReturnsNoCapturedBytes
// (finding_run_gather_test.go) sweeps the carrier as the gather's THIRD return
// and asserts the same IN-CAP precondition IN CODE — that the needle survived the
// 512-byte cap in the retained copy — so what it plants is something a leaking
// value would actually leak. That in-cap strength is the whole point and is why
// the strong claim is what travels: the pads this family usually plants with put
// the needle PAST the cap, where only a value recording the line IN FULL leaks
// it, so a record carrying the CAPPED Line publishes ~415 bytes of model-chosen
// text while the sweep passes green. This tier could only ever assert the absence
// of what its input cannot carry; that one measures.
//
// What remains is not a byte claim about the line at all:
//
// TestFinTrailerRecordCarriesNoCapturedBytes pins the record's two channel-
// independent construction rules — the Detail's HEADROOM under trailDetail's cap,
// asserted per row, and the structural absence of a line-shaped KEY.
//
// # Line was the only channel swept, and that half of the argument is not lost
//
// resultTrailer has no `result` member, so no plant placed in the trailer's
// `result` field could reach this record through the decode. The four decoded
// fields are a different matter: they cross into the record VERBATIM BY DESIGN,
// so a needle planted in them would pin AGAINST this record's construction rather
// than for it. That is stated on finTrailerRecord above ("# What the four trailer
// fields are worth"), on finTrailerBuild, and where the plant now lives
// (TestFinGatherReturnsNoCapturedBytes' "# Both plants, and the one that is
// excluded"). Deleting it here loses no unique content. The full multi-input
// sweep across every artifact input is #1286's and is not restated here.
func TestFinTrailerRecordCarriesNoCapturedBytes(t *testing.T) {
	// The same fixture and the same argument as
	// TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer's, above: the
	// pad is irrelevant now that the carrier holds no line for a needle to sit in,
	// so it is the shortest one this family plants with. NOT REPADDED past the cap
	// — nothing here reads the line, and a longer pad would only dress the fixture
	// in a reach it no longer has.
	scan := trailScan([]byte(trailPaddedTrailer(0) + "\n"))

	// The fixture puts the build on the FILLED arm, which is the one the headroom
	// must be measured against: trailScan attaches the decode in the same return
	// that answers trailSeen, so finTrailerSighting reports CarriesTrailer and
	// finTrailerBuild takes the longer of its two Detail shapes — the one
	// interpolating all four scalars. Measuring the shorter arm would measure the
	// easier case.
	if scan.State != trailSeen {
		t.Fatalf("fixture state: got %q (%s), want %q", scan.State, scan.Detail, trailSeen)
	}

	rec := finTrailerBuild(trailOutcomeVoidBudgetFired,
		finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss))

	// THE HEADROOM, ASSERTED PER ROW RATHER THAN ARGUED IN PROSE — and it is
	// finTrailerRecord's OWN TYPE-LEVEL RULE rather than this test's local
	// precaution: every Detail leaves len(trailNeedle) bytes under
	// reachMaxCommandBytes, under 470 on every row, because trailDetail caps the
	// formatted detail at reachMaxCommandBytes and a house-style multi-sentence
	// Detail can eat that budget on its own. That is the defect #1284 shipped and
	// then had to fix, and this assertion is that fix.
	//
	// WHAT THE ROOM KEEPS NON-VACUOUS SITS ONE AND TWO TIERS UP, not four lines
	// below (#1325 retired the containment checks that used to). THE RECORD TRAVELS
	// WHOLE: finRecordSeenTrailer and finRecordAbsentTrailer build it with this
	// shipped builder, finRecordRun embeds it as Trailer with nothing re-derived
	// (TestFinRecordEmbedsTrailerRecordWhole pins that), and the artifact carries it
	// from there. Both byte sweeps up there DESCEND INTO IT —
	// TestFinRecordCarriesNoCapturedBytes' marshal sweep walks the embedded
	// sub-records, and the artifact writer's Detail walk collects every Detail by
	// JSON path, asserts it found at least six with the trailer's among them, and
	// applies this identical room < len(trailNeedle) test per path. A Detail that
	// has eaten its own budget truncates a leak away and turns both of them green.
	//
	// PROSPECTIVE, NOT A LIVE PLANT: after #1320 the builder reaches no line, so
	// nothing leaks into this Detail today. This is a guard against a FUTURE Detail
	// edit or field, in the same register as the key scan below. What it buys is
	// WHERE the failure lands — here, naming the record, rather than as a JSON path
	// in an artifact two tiers up with the reader hunting for which Detail grew.
	if room := reachMaxCommandBytes - len(rec.Detail); room < len(trailNeedle) {
		t.Errorf("the detail is %d bytes, leaving %d of trailDetail's %d-byte cap against the "+
			"%d-byte yardstick: finTrailerRecord's rule is that every row keeps that much room, "+
			"because this record is embedded WHOLE into finRecordRun and into the artifact and the "+
			"byte sweeps up there recurse into it — a Detail that has eaten its own budget "+
			"truncates a future leak away and passes both. Shorten the detail — the long-form "+
			"argument belongs in a comment, which no cap applies to",
			len(rec.Detail), room, reachMaxCommandBytes, len(trailNeedle))
	}

	// Marshalled once, for the key scan below.
	encoded, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling the trailer record: %v", err)
	}

	// The structural half: the record has no field for a line today, and this is
	// the check that a future field does not quietly add one. The scan is valid
	// because finTrailerRecord is FLAT — ten scalars and a string slice, which
	// adds no nested keys — so it examines every key the record has. Lifted onto
	// a record with a struct-valued field it would never examine the inner keys.
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
