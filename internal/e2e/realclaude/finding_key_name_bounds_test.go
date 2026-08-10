//go:build e2e_realclaude

package realclaude

// The proof that #1363's published trailer key-name bounds BITE, driven over
// hostile fixtures that fail on their own preconditions rather than passing
// through them.
//
// This file reaches no verdict about pyry and takes no measurement. Everything
// here runs offline: no live claude, no credentials, no daemon, no exec, no
// clock, no goroutine, no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinPublishedKeyNames|^TestFinBoundKeyNames' -v ./internal/e2e/realclaude/
//
// # What is pinned, and where each clause lands
//
// finBoundKeyNames (finding_run_gather_test.go:455) states FIVE clauses in its
// doc comment and shipped with none of them pinned — so "bounded" in the
// artifact's standing safety sentence (finWriteSafetyClaim,
// finding_artifact_write_test.go:124) was a claim rather than a checked property.
// Every clause is now some row's sole red:
//
//	clause                                          pinned by
//	nil for a nil or empty input, never []string{}   …ReturnsNilForEmptyInput
//	at most finTrailerMaxKeyNames, in input order    …BoundTheNameCount
//	each entry bounded INDIVIDUALLY, never joined    …BoundAnOverLongName, assertion A
//	an over-long entry TRUNCATED AND MARKED          …BoundAnOverLongName, assertion B
//	its own backing array on EVERY path              …AllocatesItsOwnBackingArray
//
// The third clause is held by the SHAPE of assertion A rather than by a row of
// its own: it measures each published element separately and never their joined
// size. A joined cap would let a leak in a late name be truncated away and turn a
// containment sweep green over a record that leaked — the defect #1284 shipped
// and then had to fix.
//
// # The bound is on what is KEPT FROM THE LINE, not on the published string
//
// Clause 4 truncates an over-long name to finTrailerMaxKeyNameBytes and then
// appends reachTruncationMarker (background_reach_probe_test.go:124, 29 bytes),
// so a published over-long name is 93 bytes and not 64. `len(name) <=
// finTrailerMaxKeyNameBytes` is therefore RED AGAINST A CORRECT BUILD and is not
// the assertion here; the marker is this rig's own bytes, added on top of the
// bounded ones.
//
// # Why both hostile fixtures are a few hundred bytes
//
// This is the one thing here that would ship green while proving nothing.
// trailScan's bufio.Scanner buffer is deliberately not raised past the 64 KiB
// default (result_trailer_observation_test.go:177-179), and a line at or past the
// limit ABORTS the scan rather than truncating it: KeyNames comes back nil and
// CarriesTrailer is false, so the published field renders null — and "the
// rendered field is bounded" is trivially true over a fixture that produced not
// one name. A naive reading of "hostile" walks straight into that. Measured on
// this tree, both fixtures sit two orders of magnitude below the ceiling:
//
//	fixture                       line bytes  scan state     reader names  published
//	finOverlongKeyNameTrailer()          457  trailer-seen             12  12, the over-long one at 93 bytes
//	finManyKeyNamesTrailer(32)           273  trailer-seen             33  32, `type` cut as the alphabetic tail
//
// Nothing rests on those figures: finPublishedKeyNames asserts trailer-seen as a
// FATAL precondition, and each row separately asserts that the UNBOUNDED reader
// output genuinely went over the bound it engages. A fixture that later grows
// past the ceiling fails loudly rather than passing vacuously.
//
// # The reference to finBoundKeyNames is ONE-DIRECTIONAL, deliberately
//
// This family's convention is to name the enforcing test at the contract it
// enforces, as finTrailerRecord does at finding_trailer_evidence_test.go:214-215.
// It is NOT followed here, and this paragraph exists so a later editor does not
// "fix" that. finBoundKeyNames sits at finding_run_gather_test.go:455 with its
// doc comment above it and inbound line-number cites pointing at and past it from
// four files; a sentence added to that comment shifts every one of them and
// re-opens the three-form cite sweep (filename-anchored, bare `(:NNN)` inheriting
// the last-named file, and symbol-anchored `<Type>:NNN`) across this package. So
// this file cites the helper and the helper says nothing about this file.

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// --- fixtures -----------------------------------------------------------------

// finOverlongKeyName is one top-level key name exactly one byte over the
// per-name bound, SIZED OFF THE CONSTANT so it moves with it rather than pinning
// a literal that drifts.
//
// The stem is descriptive rather than a bare run of padding because the published
// form keeps the first finTrailerMaxKeyNameBytes bytes: a reader of a failure
// message can then see WHAT was truncated instead of a wall of x's.
func finOverlongKeyName() string {
	const stem = "pyry_probe_overlong_top_level_key_name_"
	return stem + strings.Repeat("x", finTrailerMaxKeyNameBytes+1-len(stem))
}

// finOverlongKeyNameTrailer is trailPaddedTrailer(0) with finOverlongKeyName()
// spliced in as ONE extra top-level key.
//
// Splicing onto the shipped renderer rather than writing a fresh literal is what
// makes the other eleven names the REAL ENVELOPE NAMES, so trailExpectedKeyNames()
// is a valid expectation for "the line's short names are still published
// unchanged". A hand-written literal would pin this fixture's own key set instead
// and the row would then prove nothing about the shape a producer emits.
//
// It inherits trailNeedle inside `result` from trailPaddedTrailer, which is inert
// here: no row in this file renders the artifact, sweeps for the needle or reads
// trailScanResult.Line. The artifact-wide sweeps are #1362's.
func finOverlongKeyNameTrailer() string {
	return strings.TrimSuffix(trailPaddedTrailer(0), "}") +
		`,"` + finOverlongKeyName() + `":"v"}`
}

// finManyShortNames returns n distinct names, every one far under the per-name
// bound, so a fixture built from them engages the COUNT bound alone.
//
// A function rather than a package-level var: it returns a slice, go test -race
// runs this package's tests in parallel, and a shared backing array would let one
// row's mutation reach another's (trail_run_outcome_test.go:1111-1113).
func finManyShortNames(n int) []string {
	names := make([]string, 0, n)
	for i := 0; i < n; i++ {
		names = append(names, fmt.Sprintf("k%02d", i))
	}
	return names
}

// finManyKeyNamesTrailer renders a trailer carrying extra synthetic short keys
// beside the mandatory one.
//
// "type":"result" is not decoration: without it trailScan never matches, and a
// test over the result would compare two empty reads and pass. It also sorts LAST
// of the set finManyShortNames produces, which is what makes the count bound's
// alphabetic-prefix consequence observable — the cut name is `type` itself.
//
// Called with finTrailerMaxKeyNames so the fixture sizes itself off the constant;
// the row then asserts the resulting count against the bound rather than trusting
// that arithmetic.
func finManyKeyNamesTrailer(extra int) string {
	var b strings.Builder
	b.WriteString(`{"type":"result"`)
	for _, name := range finManyShortNames(extra) {
		b.WriteString(`,"` + name + `":0`)
	}
	b.WriteString(`}`)
	return b.String()
}

// --- the drive helper -----------------------------------------------------------

// finPublishedKeyNames drives line through the shipped chain and returns the
// UNBOUNDED reader output beside the PUBLISHED names, so a row can assert both
// halves — what the line really carried and what the record published — entirely
// from shipped code. That the bounding is applied at the fill and not at the
// reader is what makes these proofs measurable rather than tautological.
//
// It stops at finTrailerBuild deliberately. The remaining hands are already
// pinned: TestFinRecordEmbedsTrailerRecordWhole (finding_run_record_test.go:757)
// pins that finRecordRun embeds the sub-record whole, and
// TestFinRecordPublishesTheTrailerKeyNamesTheReaderRead
// (finding_run_record_test.go:1099) pins the carriage through finRecordBuild. A
// fourth hand adds cost and no discrimination.
//
// THE FATAL PRECONDITION IS THE CEILING DEFENCE, and it names the state it is
// defending against: past 65535 bytes the scan aborts, CarriesTrailer is false,
// the field renders null, and every bound assertion below it passes having proved
// nothing. It reports Detail — this instrument's own prose, carrying no bytes from
// the line — and never scan.Line, which a -realclaude run could otherwise carry
// into a failure message pasted into a public issue.
func finPublishedKeyNames(t *testing.T, line string) (read, published []string) {
	t.Helper()

	scan := trailScan([]byte(line + "\n"))
	if scan.State != trailSeen || scan.Trailer == nil {
		t.Fatalf("fixture: %d bytes scanned to state %q carrying a decode %t (%s); want %q "+
			"carrying one. A fixture past bufio.Scanner's 64 KiB default ABORTS the scan, which "+
			"renders the names field null and makes every bound assertion below vacuously green",
			len(line), scan.State, scan.Trailer != nil, scan.Detail, trailSeen)
	}

	rec := finTrailerBuild(trailOutcomeVoidBudgetFired,
		finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss))
	return scan.KeyNames, rec.KeyNames
}

// --- tests ----------------------------------------------------------------------

// TestFinPublishedKeyNamesBoundAnOverLongName is AC1: a line carrying a name over
// the per-name bound is published bounded, and its short names survive untouched.
//
// Both halves matter and neither is optional. A bound implemented as a wholesale
// drop of the set satisfies "every name is within the bound" exactly the way the
// aborted arm does, so assertion A alone would be green over a record publishing
// nothing at all; assertion B is what says the set is still there.
func TestFinPublishedKeyNamesBoundAnOverLongName(t *testing.T) {
	read, published := finPublishedKeyNames(t, finOverlongKeyNameTrailer())

	// THE NON-VACUITY PRECONDITION, fatal: the UNBOUNDED reader output must
	// genuinely carry a name the bound has to act on. With nothing over it there
	// is nothing to truncate and every assertion below is green against an
	// implementation that bounds nothing whatsoever.
	overlong := 0
	for _, name := range read {
		if len(name) > finTrailerMaxKeyNameBytes {
			overlong++
		}
	}
	if overlong == 0 {
		t.Fatalf("the reader read %d name(s) and none exceeds the %d-byte bound: this row engages "+
			"the per-name bound, and over a fixture that never trips it the assertions below hold "+
			"against a build that applies no bound at all", len(read), finTrailerMaxKeyNameBytes)
	}

	// ASSERTION A — the headline, PER NAME and marker-aware. Per name rather than
	// over their joined size, because a joined cap lets a leak in a late name be
	// truncated away. Marker-aware because clause 4 appends reachTruncationMarker
	// on top of the bounded bytes: the bound is on what is KEPT FROM THE LINE, so
	// `len(name) <= finTrailerMaxKeyNameBytes` is red against a correct build.
	for _, name := range published {
		kept := len(strings.TrimSuffix(name, reachTruncationMarker))
		if kept > finTrailerMaxKeyNameBytes {
			t.Errorf("the published name %q keeps %d byte(s) of the line against the %d-byte bound. "+
				"The %d-byte marker is this rig's own bytes and is discounted here; what is over is "+
				"claude-authored", name, kept, finTrailerMaxKeyNameBytes, len(reachTruncationMarker))
		}
	}

	// ASSERTION B — one comparison covering both halves the AC calls non-optional.
	// It is red against a wholesale drop of the set, red against a drop of the
	// over-long entry alone, red against truncation without the marker, and red
	// against any short name being altered.
	//
	// sort.Strings on the expectation is valid because truncation keeps the first
	// finTrailerMaxKeyNameBytes bytes, so the marked entry sorts exactly where the
	// whole name did.
	want := append(trailExpectedKeyNames(),
		finOverlongKeyName()[:finTrailerMaxKeyNameBytes]+reachTruncationMarker)
	sort.Strings(want)
	if !reflect.DeepEqual(published, want) {
		t.Errorf("published names: got %q, want %q — the line's eleven envelope names unchanged "+
			"plus the over-long one PRESENT, truncated and carrying %q. An over-long entry is "+
			"truncated and marked and never dropped: dropping removes evidence silently, "+
			"truncating announces itself", published, want, reachTruncationMarker)
	}
}

// TestFinPublishedKeyNamesBoundTheNameCount is AC2: a line carrying more names
// than the bound admits is published at the bound, in the reader's order.
//
// Its fixture's names are all short and asserted so, which is what keeps this row
// and the one above independent: a failure here names the count bound and never
// the per-name one.
func TestFinPublishedKeyNamesBoundTheNameCount(t *testing.T) {
	read, published := finPublishedKeyNames(t, finManyKeyNamesTrailer(finTrailerMaxKeyNames))

	// THE NON-VACUITY PRECONDITION, fatal, in both halves.
	if len(read) <= finTrailerMaxKeyNames {
		t.Fatalf("the reader read %d name(s) against the %d-name bound: the bound cannot bite over "+
			"a fixture that stays under it, and the assertions below would hold against a build "+
			"applying no count bound at all", len(read), finTrailerMaxKeyNames)
	}
	for _, name := range read {
		if len(name) > finTrailerMaxKeyNameBytes {
			t.Fatalf("the reader read the %d-byte name %q, over the %d-byte per-name bound: this "+
				"row must engage the COUNT bound alone, or a red below cannot say which of the two "+
				"bounds it is about", len(name), name, finTrailerMaxKeyNameBytes)
		}
	}

	// EXACTLY the bound, not at-most it. "No more than N" is trivially true of a
	// field carrying none, which is the same green-by-breakage shape the aborted
	// scan produces.
	if len(published) != finTrailerMaxKeyNames {
		t.Errorf("published %d name(s), want exactly %d from a line carrying %d. A count bound "+
			"implemented as a wholesale drop satisfies \"no more than %d\" and publishes nothing",
			len(published), finTrailerMaxKeyNames, len(read), finTrailerMaxKeyNames)
	}

	// The kept set is the reader's ALPHABETIC PREFIX, in its order. What this
	// fixture makes observable is the consequence finTrailerRecord's comment warns
	// a reader about (finding_trailer_evidence_test.go:178-183): `type` sorts last
	// of the set, so `type` is the name the cut removes.
	if want := read[:finTrailerMaxKeyNames]; !reflect.DeepEqual(published, want) {
		t.Errorf("published names: got %q, want %q — the reader's own first %d names, in the order "+
			"it returned them. The bound keeps the alphabetic prefix and special-cases no name to "+
			"survive the cut", published, want, finTrailerMaxKeyNames)
	}
}

// TestFinBoundKeyNamesReturnsNilForEmptyInput is AC3's clause 1, by direct call:
// no fixture line, no scan, no builder.
//
// The empty-input path is unreachable from both shipped fill sites — they sit
// behind the CarriesTrailer guard and a seen trailer always carries at least
// `type` — so the clause has no live caller and nothing else would notice it
// breaking. That is precisely why it needs a pin rather than why it does not.
func TestFinBoundKeyNamesReturnsNilForEmptyInput(t *testing.T) {
	tests := []struct {
		name string
		in   []string
	}{
		{"a nil input", nil},
		{"a non-nil empty input", []string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// len(got) == 0 is NOT this assertion — it is exactly the mutant.
			if got := finBoundKeyNames(tc.in); got != nil {
				t.Errorf("got %#v, want nil. A non-nil empty slice marshals to [] where nil "+
					"marshals to null, so this is the difference between an artifact telling an "+
					"operator the record carried NO names and one telling them the scan read a "+
					"trailer with none — and only the second is a shape trailScan can produce", got)
			}
		})
	}
}

// TestFinBoundKeyNamesAllocatesItsOwnBackingArray is AC3's clause 5, by direct
// call, over every path the helper has.
//
// The under-both-bounds row is the AC's named one and the only shape the clause
// forbids: an `if there is nothing to do, return names` fast path is invisible to
// every other row here. The over-the-count row exists for a second mutant no
// other assertion in this file sees — `return names[:kept]` is content-identical
// to the correct answer and differs only in the array it sits on.
func TestFinBoundKeyNamesAllocatesItsOwnBackingArray(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		// want is set on the FAST-PATH ROW ALONE. It is the one row whose contents
		// the helper must leave entirely untouched; the other two rows' contents are
		// pinned one tier up, over real fixtures and through the shipped builders, by
		// the two tests above. Repeating them here would make a count-bound mutant
		// red in two places and neither message the clearer for it.
		want []string
	}{
		{
			// THE FAST PATH. Nothing to truncate, nothing to cut.
			name: "an input already under both bounds",
			in:   []string{"a", "b", "c"},
			want: []string{"a", "b", "c"},
		},
		{
			name: "an input over the count bound",
			in:   finManyShortNames(finTrailerMaxKeyNames + 1),
		},
		{
			name: "an input over the per-name bound",
			in:   []string{"a", finOverlongKeyName()},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := slices.Clone(tc.in)
			got := finBoundKeyNames(in)

			// Without this the address comparison below indexes nothing and is
			// vacuously true.
			if len(got) == 0 {
				t.Fatalf("got %d name(s) from an input of %d: the aliasing check below reads got[0]",
					len(got), len(in))
			}
			if &got[0] == &in[0] {
				t.Errorf("the result shares its backing array with the input. finTrailerBuild copies " +
					"this field by PLAIN SLICE ASSIGNMENT (finding_trailer_evidence_test.go:333) and " +
					"says in its own comment that the assignment is safe only because this clause " +
					"holds — so a pass-through puts two carriers on one array while `go test -race` " +
					"runs this package's tests in parallel")
			}
			if tc.want != nil && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q — a fast path is the only thing this row forbids, and "+
					"copying is not licence to alter", got, tc.want)
			}
		})
	}
}
