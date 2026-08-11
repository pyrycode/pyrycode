//go:build e2e_realclaude

package realclaude

// The end-to-end containment proof for #1363's published trailer key NAMES: no
// value from the trailer line reaches the artifact an operator pastes, checked
// over the FILES ACTUALLY WRITTEN rather than over the record in memory.
//
// This file reaches no verdict about pyry and takes no measurement. Everything
// here runs offline: no live claude, no credentials, no network, no daemon, no
// exec, no clock, no goroutine, no env gate, no t.Skip. The only writes are the
// artifact writer's own two files, under a per-test t.TempDir().
//
//	go test -race -tags e2e_realclaude -run '^TestFinContain' -v ./internal/e2e/realclaude/
//
// # What was already proved, and what was not
//
// #1363 published the names on finSighting and finTrailerRecord, bounded by
// finBoundKeyNames, reaching the written file
// through finRecordRun.Trailer. #1364 proved both bounds bite.
// TestTrailKeyNamesCarryNoValues proves no value
// reaches the names AT THE READER'S OWN RETURN.
//
// Nothing checked that the TWO BUILDERS between that return and the file, and the
// writer after them, kept it that way IN THE RENDERED FILES — which is the only
// artefact an operator ever reads. That gap is what this file closes. The
// mis-implementation it is shaped against is concrete: a reader or a builder
// carrying the decoded map's VALUES rather than its key names would put the
// model's whole `result` text into a file the writer hands an operator to paste
// into a public issue.
//
// # A containment check is a PAIR — where the needle is planted, and which field
// is swept — and neither half alone identifies it
//
// That pairing is what makes four checks the right number rather than one. The
// artifact carries four of the trailer's own values VERBATIM BY DESIGN —
// subtype, is_error, terminal_reason and stop_reason — and
// finding_artifact_write_test.go:282-297 states the rule: plant only where the
// pipeline reduces. A single artifact-wide sweep planting in every value position
// the line carries is therefore RED AGAINST A CORRECT BUILD. So the artifact-wide
// sweep excludes those four (AC1), and the sweep that plants in all five
// string-valued positions looks at the NAMES FIELD ALONE (AC2). The two differ in
// both halves of the pair, and neither subsumes the other.
//
// # The mutant matrix: what each check is the sole red for
//
// Built rather than asserted, because "no one of them subsumes another" is the
// whole argument for shipping four checks. Each row names a mis-implementation
// and the single check that catches it.
//
//	#   mis-implementation                                caught by
//	M1  a reader/builder carrying the decoded map's        AC1 — session_id's value is the
//	    VALUES into the names field                        needle whole, so it survives the
//	                                                       64-byte per-name bound
//	M2  the same, itself bounded to 64 bytes per entry     AC1 — `result`'s value truncates to
//	                                                       padding with no needle in it and
//	                                                       session_id's does not, which is why
//	                                                       the plant list needs BOTH
//	M3  a record re-admitting the CAPPED                   shipped, finding_artifact_write_test.go:1149-1152
//	    trailScanResult.Line                               — needs an IN-CAP needle, which AC1's
//	                                                       past-the-cap pad cannot see
//	M4  a record re-admitting the FULL, uncapped line      AC1 — needs a PAST-THE-CAP needle,
//	                                                       which the shipped in-cap row cannot see
//	M5  a builder copying a value into the names field     AC2 — AC1's plant list excludes those
//	    from one of the four PUBLISHED positions           four by design, so AC1 is green here
//	                                                       against a leaking build
//	M6  a Detail interpolating the key NAMES               AC4 — the mutant Detail is 424 bytes
//	                                                       with 88 of room, so the shipped
//	                                                       per-row headroom check passes it
//	M7  a Detail interpolating the key COUNT               NOTHING. No instrument exists; said
//	                                                       plainly at AC4's row rather than
//	                                                       implied discharged
//	M8  widening a carrier to reach                        AC3 — a type-level reachability
//	    map[string]json.RawMessage                         property no fixture can exercise
//
// M3 and M4 are why AC1 is a SIBLING of the shipped in-cap row and never a
// precondition bolted onto it: the two need opposite pad positions and each is
// blind to the other's mutant. That row's own comment says it "MUST NOT ACQUIRE A
// COPY OF ONE".
//
// # Truncation is how a leak turns a containment sweep green
//
// Both of #1363's bounds truncate, reachCapCommand caps the retained line at 512
// bytes, and trailDetail caps a Detail at the same figure. A needle truncated
// away before it is looked for passes over a record that leaked — the defect
// #1284 shipped and had to fix. Every fixture here therefore states its
// non-vacuity as an ASSERTED PRECONDITION, fatal and before the thing it guards,
// never as a comment and never as a t.Errorf that lets the vacuous sweep below it
// run and print a green-looking result beside the failure.
//
// # The failure-message rule for this whole file
//
// Name the FILE, the JSON PATH, a BYTE LENGTH, an OFFSET, a COUNT or a
// rig-chosen FIELD NAME. Never a file's contents, never a Detail's string, never
// scan.Line, never a needle-bearing value. That is finWriteSorted's own rule
// (finding_artifact_write_test.go:638-646), inherited whole: printing what a file
// or a detail HOLDS after it just failed a leak check writes the leak into CI
// logs. The one knowing residual is that the non-vacuity Fatalfs print key NAMES,
// which under M1 would be values — but they are fixture constants on a
// fixture-only path, and the shipped precedent is TestTrailKeyNamesCarryNoValues'
// own non-vacuity report (trailer_key_names_test.go:389-392).

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// --- the drive helper ------------------------------------------------------------

// finContainRender drives one fixture trailer line through the ASSEMBLED pipeline
// — the reader, the carrier fill, the trailer builder, the record builder and the
// artifact writer — and returns every file that landed in the directory beside
// the names the reader read.
//
// Only .Trailer is substituted on finWriteInputs, and the other three trailNeedle
// plants stay live: a matched row's argv, ClaudeCommand and the reap stderr, all
// of which the pipeline reduces or drops. Keeping them makes AC1's sweep strictly
// stronger at zero cost, and nothing but the trailer line then differs from what
// finWriteRender renders.
//
// ONE CONSTRUCTION SITE for both callers, because they render the same pipeline
// over different fixture lines and two copies could disagree about which hands
// ran.
//
// # The second return is []string and never the trailScanResult
//
// Handing the scan back would promote .Line — up to 512 bytes of model-chosen
// text, marked OPERATOR-REVIEW-BEFORE-PASTE
// (result_trailer_observation_test.go:100-107) — and the *resultTrailer into the
// reach of every test in this file, where a later %+v in a failure message prints
// it. That is the reach finGatherReadings deliberately refuses to hand back and
// that finSighting exists to sever. Both
// callers' preconditions are about THE NAMES THE READER READ; neither needs Line,
// Trailer or Detail, so the narrow return costs nothing and closes the door by
// SHAPE rather than by a prose rule that must not be got wrong — this family's own
// doctrine (finding_artifact_write_test.go:28-32).
//
// THE FATAL PRECONDITION IS THE 64 KiB CEILING DEFENCE, in finPublishedKeyNames'
// words (finding_key_name_bounds_test.go:169-174): past bufio.Scanner's default a
// line ABORTS the scan rather than truncating it, KeyNames comes back nil, the
// published field renders null and every assertion downstream is vacuously green.
// It reports the scan's own Detail — this instrument's prose, carrying no byte of
// the line — and never scan.Line.
func finContainRender(t *testing.T, line string) (files map[string][]byte, read []string) {
	t.Helper()

	scan := trailScan([]byte(line + "\n"))
	if scan.State != trailSeen || scan.Trailer == nil {
		t.Fatalf("fixture: %d bytes scanned to state %q carrying a decode %t (%s); want %q carrying "+
			"one. A fixture past bufio.Scanner's 64 KiB default ABORTS the scan, which renders the "+
			"names field null and makes every assertion below vacuously green",
			len(line), scan.State, scan.Trailer != nil, scan.Detail, trailSeen)
	}

	in := finWriteInputs()
	in.Trailer = finTrailerBuild(trailOutcomeVoidBudgetFired,
		finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss))

	dir := t.TempDir()
	finWriteArtifacts(t, dir, finRecordBuild(in))
	return finWriteReadDir(t, dir), scan.KeyNames
}

// --- AC1's fixture ---------------------------------------------------------------

// finContainValuePlantedTrailer is trailPaddedTrailer's wire order and its four
// PUBLISHED values unchanged, with trailNeedle planted in two positions:
//
//   - inside `result`, behind pad bytes of padding, so the cap cuts it MID-VALUE
//     rather than dropping it whole;
//   - AS session_id's whole value. trailPaddedTrailer hard-codes that field to a
//     UUID (`trailPaddedTrailer`), which is why this is a
//     separate renderer and never a repad of the shipped fixture. session_id sits
//     AFTER `result` on the wire, so substituting it does not move `result`'s
//     needle and the shipped pad measurement still holds.
//
// A function and not a package-level var, for trailRunWellFormed's stated reason
// (trail_run_outcome_test.go:1203-1205): `go test -race` runs this package's tests
// in parallel.
//
// # The plant list, and why it stops where it does
//
// EXCLUDED — subtype, is_error, terminal_reason and stop_reason. The record
// carries all four VERBATIM BY DESIGN: finTrailerSighting copies them off the
// decode and finTrailerRecord publishes them as fields, so a needle in any of them
// appears in the artifact CORRECTLY and a sweep planting there would be red
// against a correct build. That is the plant-only-where-the-pipeline-reduces rule
// (finding_artifact_write_test.go:282-297), and stop_reason is named a second time
// at finding_trailer_evidence_test.go:130-137 as the one model-influenced field
// crossing this record uncapped. The check those four need is AC2's, which sweeps
// the names field alone.
//
// WHAT THE LIST DOES CATCH — a reader or a builder that carried the decoded map's
// VALUES rather than its key names (M1), the same mis-implementation bounded to 64
// bytes per entry (M2, which session_id's plant is the whole reason for), and a
// record re-admitting the FULL, uncapped line (M4). The values keep their shipped
// wire values so nothing here drifts what
// TestFinWriteArtifactPublishesNoVerbatimModelOutput pins.
func finContainValuePlantedTrailer(pad int) string {
	return `{"type":"result","subtype":"error_max_turns","is_error":true,` +
		`"duration_ms":9002,"num_turns":6,"result":"` + strings.Repeat("x", pad) + trailNeedle + `",` +
		`"stop_reason":"end_turn","session_id":"` + trailNeedle + `",` +
		`"total_cost_usd":0.42,"usage":{"input_tokens":120,"output_tokens":45,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0},` +
		`"terminal_reason":"max_turns"}`
}

// finContainValuePad puts `result`'s needle comfortably past reachCapCommand's
// 512-byte cap while leaving the whole line around a kilobyte — four orders of
// magnitude below the scanner ceiling the drive helper's Fatalf defends. The
// offset is a property of the PAD rather than an invariant of the needle, so the
// test asserts it rather than trusting this comment.
const finContainValuePad = 600

// --- AC1 --------------------------------------------------------------------------

// TestFinContainArtifactCarriesNoTrailerValue is AC1: a byte sweep over EVERY FILE
// the artifact writer wrote proves no captured value reaches it.
//
// run.md included and not run.json alone — the note embeds the same marshalled
// bytes inside a fence, and an operator pastes whichever of the two is to hand. It
// sweeps the RENDERED FILES and never the in-memory struct, which is the
// difference between measuring the artifact and measuring the record.
func TestFinContainArtifactCarriesNoTrailerValue(t *testing.T) {
	line := finContainValuePlantedTrailer(finContainValuePad)

	// OFFSET PRECONDITION A (M4), fatal: `result`'s needle — the FIRST occurrence,
	// session_id's being later on the wire — must sit PAST the cap. An in-cap plant
	// here would duplicate the shipped row at finding_artifact_write_test.go:1149-1152
	// instead of complementing it, and this check would then be blind to the mutant
	// that row cannot see.
	at := strings.Index(line, trailNeedle)
	if at <= reachMaxCommandBytes {
		t.Fatalf("`result`'s needle sits at offset %d of %d bytes, inside the %d-byte cap: this "+
			"check's plant must land PAST it, or it catches only what the shipped in-cap row at "+
			"finding_artifact_write_test.go:1149 already catches and nothing re-admitting the FULL "+
			"line", at, len(line), reachMaxCommandBytes)
	}

	// OFFSET PRECONDITION B (M2), fatal: the needle must begin exactly at
	// session_id's value start AND fit inside the per-name bound. A needle further
	// into its own value than that bound would be truncated away by a
	// values-carrying mis-implementation, and the sweep below would pass over M2.
	const marker = `"session_id":"`
	start := strings.Index(line, marker)
	if start < 0 {
		t.Fatalf("the fixture carries no %s field, so the second plant is absent and only the "+
			"past-the-cap one is under test", strings.TrimSuffix(marker, `":"`))
	}
	if valueAt := start + len(marker); !strings.HasPrefix(line[valueAt:], trailNeedle) {
		t.Fatalf("session_id's value does not START with the needle at offset %d of %d bytes: the "+
			"plant must be the whole value, or a values-carrying build truncates it at the %d-byte "+
			"per-name bound and this check passes over the leak",
			valueAt, len(line), finTrailerMaxKeyNameBytes)
	}
	if len(trailNeedle) > finTrailerMaxKeyNameBytes {
		t.Fatalf("the %d-byte needle exceeds the %d-byte per-name bound: a values-carrying build "+
			"would truncate it away and the sweep below would report clean over the leak",
			len(trailNeedle), finTrailerMaxKeyNameBytes)
	}

	files, read := finContainRender(t, line)

	// NON-VACUITY OF THE FIELD UNDER SWEEP, fatal: the reader read the line's whole
	// envelope, both bounds inert. With no names published there is nothing for a
	// values-carrying build to have leaked INTO, and the sweep proves nothing.
	if want := trailExpectedKeyNames(); !reflect.DeepEqual(read, want) {
		t.Fatalf("the reader read %d name(s) %q, want the eleven envelope names %q: a build "+
			"publishing no names leaks none, and every assertion below is then green about nothing",
			len(read), read, want)
	}

	// NON-VACUITY OF THE DIRECTORY, fatal: the claim is run.md INCLUDED and not
	// run.json alone.
	for _, want := range []string{finWriteRecordFile, finWriteNoteFile} {
		if _, ok := files[want]; !ok {
			t.Fatalf("no %s in the artifact directory, which holds %v — the claim is over every "+
				"file the writer wrote and not over the record alone", want, finWriteSorted(files))
		}
	}

	// THE DETAIL HEADROOM, PER PATH and asserted on what was actually WRITTEN. Not a
	// duplicate of the shipped walk in TestFinWriteArtifactsCarryNoCapturedBytes:
	// that one runs over finWriteInputs' OWN trailer fixture and never over this
	// one, and #1284's observed defect is precisely a sweep going green because a
	// Detail ate its own budget and truncated the leak away eight lines above where
	// it was looked for. Path and byte length only, never the string.
	details := make(map[string]string)
	if err := finWriteObservedDetails(files[finWriteRecordFile], details); err != nil {
		t.Fatalf("walking %s for details: %v", finWriteRecordFile, err)
	}
	// The shipped floor: the record's Detail, the attribution's, the one entry's
	// Admit, the selected Admit, the trailer's and the one liveness outcome's. A
	// walk that found fewer has stopped descending and the headroom claim would hold
	// over a subset.
	if len(details) < 6 {
		t.Fatalf("the artifact walk found %d detail(s) at %v, want at least 6 — the headroom claim "+
			"below would otherwise hold over a subset of the strings the artifact publishes",
			len(details), finWriteSorted(details))
	}
	for _, path := range finWriteSorted(details) {
		if room := reachMaxCommandBytes - len(details[path]); room < len(trailNeedle) {
			t.Errorf("the detail at %s is %d bytes, leaving %d of the %d-byte cap against a %d-byte "+
				"needle: a detail that leaked a trailer value would be truncated before the needle "+
				"and the sweep below would pass against it. Shorten it — the long-form argument "+
				"belongs in a comment, which no cap applies to", path, len(details[path]), room,
				reachMaxCommandBytes, len(trailNeedle))
		}
	}

	// THE SWEEP, over every file the directory held rather than over the two names
	// the writer wrote. The failure names the FILE and the listing and never any
	// file's contents.
	for _, name := range finWriteSorted(files) {
		if bytes.Contains(files[name], []byte(trailNeedle)) {
			t.Errorf("%s carries the needle. It sits INSIDE `result` past the %d-byte cap and AS "+
				"session_id's whole value, and the pipeline reduces both to nothing: the reader "+
				"returns key NAMES, the carrier fill bounds them and the builder copies them. One "+
				"occurrence therefore means a VALUE crossed into the names field or a line was "+
				"re-admitted whole. The four positions this record publishes by design carry no "+
				"needle, so no correct build can be red here. The artifact directory holds %v",
				name, reachMaxCommandBytes, finWriteSorted(files))
		}
	}
}

// --- AC2 --------------------------------------------------------------------------

// finContainNeedlePad is 0, and it is the DELIBERATE INVERSION of the pad the
// shipped reader-tier check uses.
//
// TestTrailKeyNamesCarryNoValues calls
// trailKeyNamesNeedledTrailer(600) and asserts `result`'s needle lands PAST the
// 512-byte cap, because its claim is about which COPY of the line the reader read.
// This check's claim is about which BYTES a builder copied into a bounded field,
// so the cap is irrelevant here and the per-name bound is everything: at pad 600 a
// values-carrying builder truncates `result`'s value at 64 bytes of padding and
// the needle is never looked at. Pad 0 puts every one of the five needles inside
// the first finTrailerMaxKeyNameBytes bytes of its own value, and the row asserts
// that per needle rather than trusting this comment.
const finContainNeedlePad = 0

// TestFinContainPublishedNamesCarryNoValue is AC2: a distinct needle in EVERY
// string-valued position the line carries — the four the record publishes
// included — and the NAMES FIELD ALONE carries none of them.
//
// The check AC1 cannot make. AC1's plant list excludes those four by design, so
// AC1 is green against a builder copying one of them into the names field (M5);
// this sweep is scoped to the field where any of the five would be a leak.
//
// Distinct from TestTrailKeyNamesCarryNoValues because it runs over the
// PUBLISHED, BOUNDED names after two builders have copied them, rather than over
// the reader's own return. Its fixture is shipped and reused rather than
// re-rendered: trailKeyNamesNeedledTrailer keeps the wire order and the eleven
// top-level names of trailPaddedTrailer intact, so what is swept is the real
// envelope's name set.
func TestFinContainPublishedNamesCarryNoValue(t *testing.T) {
	needles := trailKeyNamesNeedles()
	if len(needles) != 5 {
		t.Fatalf("the plant list holds %d needle(s), want 5 — one per string-valued position",
			len(needles))
	}
	seen := make(map[string]string, len(needles))
	for _, field := range finWriteSorted(needles) {
		if other, dup := seen[needles[field]]; dup {
			t.Fatalf("%s and %s carry the same needle: a shared needle cannot say WHICH position "+
				"leaked, which is this check's whole diagnostic value", field, other)
		}
		seen[needles[field]] = field
	}

	line := trailKeyNamesNeedledTrailer(finContainNeedlePad)

	// THE DETECTABILITY PRECONDITION, fatal and per needle: each must sit inside the
	// per-name bound MEASURED FROM THE START OF ITS OWN VALUE. This is the pad
	// argument above asserted rather than trusted — a needle further in than the
	// bound is truncated away by the very mis-implementation this row exists to
	// catch, and the sweep below then reports clean over the leak.
	for _, field := range finWriteSorted(needles) {
		marker := `"` + field + `":"`
		start := strings.Index(line, marker)
		if start < 0 {
			t.Fatalf("the fixture carries no %s field, so that position is not under test", field)
		}
		off := strings.Index(line[start+len(marker):], needles[field])
		if off < 0 {
			t.Fatalf("%s's value does not carry its needle: the plant is absent and this position "+
				"is swept for nothing", field)
		}
		if end := off + len(needles[field]); end > finTrailerMaxKeyNameBytes {
			t.Fatalf("%s's needle ends %d bytes into its own value, past the %d-byte per-name "+
				"bound: a values-carrying builder truncates it away and this row passes over the "+
				"leak. Lower the pad", field, end, finTrailerMaxKeyNameBytes)
		}
	}

	// The two builders the AC names, through the shipped drive helper: trailScan →
	// finTrailerSighting → finTrailerBuild. Reusing it rather than re-driving the
	// chain is what keeps this check about the COPY and not about the scan, and it
	// carries the same 64 KiB ceiling Fatalf.
	read, published := finPublishedKeyNames(t, line)

	// THE NON-VACUITY PRECONDITIONS, fatal, in both halves: the reader saw the whole
	// envelope, and BOTH BOUNDS ARE INERT so the sweep looks at the whole set rather
	// than at a truncated prefix of it.
	if want := trailExpectedKeyNames(); !reflect.DeepEqual(read, want) {
		t.Fatalf("the reader read %d name(s) %q, want the eleven envelope names %q: the sweep "+
			"below is vacuous against a reader that recorded no names at all", len(read), read, want)
	}
	// BOTH BOUNDS INERT, asserted as the two bounds' own preconditions and
	// deliberately NOT as published == read. A builder that ADDS an entry is exactly
	// M5 — the mis-implementation this row is the sole red for — and an equality here
	// would fatal on it before the sweep could say WHICH position leaked, turning
	// this row's diagnostic value into a slice diff.
	if len(read) > finTrailerMaxKeyNames {
		t.Fatalf("the reader read %d name(s) against the %d-name bound: the count bound would BITE "+
			"and leave this row sweeping an alphabetic prefix", len(read), finTrailerMaxKeyNames)
	}
	for _, name := range read {
		if len(name) > finTrailerMaxKeyNameBytes {
			t.Fatalf("the reader read a %d-byte name against the %d-byte per-name bound: a leak in "+
				"a truncated name would pass unseen", len(name), finTrailerMaxKeyNameBytes)
		}
	}
	dropped := 0
	for _, name := range read {
		if !slices.Contains(published, name) {
			dropped++
		}
	}
	if dropped > 0 {
		t.Fatalf("%d of the %d name(s) the reader read are absent from the %d published: the sweep "+
			"below would then run over a subset of what the record carries", dropped, len(read),
			len(published))
	}

	// THE SWEEP, over the names field and nothing else. The failure names the FIELD
	// WHOSE VALUE LEAKED — the needle map's key, a name this rig chose rather than a
	// byte from the line — and never the needle or the published name.
	for _, name := range published {
		for _, field := range finWriteSorted(needles) {
			if strings.Contains(name, needles[field]) {
				t.Errorf("a published trailer key name carries %s's VALUE. Four of the five planted "+
					"positions are published verbatim by design, so the artifact-wide sweep is green "+
					"against this by construction and this row is the only thing that sees it: the "+
					"names field is names and never values, at the reader AND after both builders "+
					"have copied them", field)
			}
		}
	}
}

// --- AC3 --------------------------------------------------------------------------

// TestFinContainCarriersReachNoRawMessageMap is AC3: resultTrailer is not widened
// and no arbitrary-value capture is introduced, PROVED with the shipped
// reachability walk rather than asserted.
//
// No fixture, no scan, no artifact — a type-level property no fixture can
// exercise, in TestTrailScanResultReachesNoRawMessageMap's shape
// (`TestTrailScanResultReachesNoRawMessageMap`) and TestFinSightingReachesNoScanType's
// (`TestFinSightingReachesNoScanType`).
//
// # The control is what makes the negative a property of the record
//
// Both carriers reach only scalars and the one new string slice today, so the ban
// is green against a walk that could be finding nothing at all. #1357 had a
// natural control — json.RawMessage itself was already reachable from
// trailScanResult through PermissionDenials — and no such control exists here, so
// the key-name field's own type is the one asserted reachable. KeyNames is its
// SOLE ROUTE from either carrier; a later editor adding a second []string should
// revise this sentence rather than the assertion.
//
// BAN AND CONTROL PER CARRIER, never once over the pair. Sweeping a rule over one
// input and calling it covered is the shape that let a two-parameter contract be
// checked three times on one parameter and not once on the other.
func TestFinContainCarriersReachNoRawMessageMap(t *testing.T) {
	forbidden := reflect.TypeOf(map[string]json.RawMessage{})
	control := reflect.TypeOf([]string{})

	for _, carrier := range []struct {
		name string
		typ  reflect.Type
	}{
		{"finSighting", reflect.TypeOf(finSighting{})},
		{"finTrailerRecord", reflect.TypeOf(finTrailerRecord{})},
	} {
		// The walk descends into struct fields, map keys AND values, slices, arrays
		// and pointers (finding_run_record_test.go:781-817), so "no type transitively
		// holding one" is proved by this single call rather than aspirational.
		if finRecordInputReaches(carrier.typ, forbidden, map[reflect.Type]bool{}) {
			t.Errorf("%s is reachable from %s: its values are the RAW BYTES of the line, so a %%v on "+
				"the map — or on any struct transitively holding it, as this package already does at "+
				"finding_run_record_test.go:840 — prints the whole assistant `result` field into a "+
				"failure message. trailKeyNames discards that map inside itself and returns []string, "+
				"which is what keeps this true by construction", forbidden, carrier.name)
		}

		if !finRecordInputReaches(carrier.typ, control, map[reflect.Type]bool{}) {
			t.Errorf("%s is NOT reachable from %s: it should be, through KeyNames and through "+
				"nothing else. Either that field moved or this walk stopped walking — and if it "+
				"stopped walking, the ban above proves nothing", control, carrier.name)
		}
	}
}

// --- AC4 --------------------------------------------------------------------------

// finContainNeedleKeyName is planted AS a top-level key NAME rather than as a
// value, so it reaches trailer_keys LEGITIMATELY and its absence from every Detail
// is the claim.
//
// DISTINCT from trailNeedle and from all five of trailKeyNamesNeedles(), and the
// distinctness is load-bearing rather than tidy: this needle is CARRIED BY DESIGN
// into the published field, so sharing a constant with a value-position plant
// would leave AC1's sweep unable to tell a value leak from the by-design name
// carriage and red against a correct build.
//
// It leads with ASCII uppercase so it sorts FIRST of the set — trailKeyNames sorts
// and uppercase precedes lowercase — which means even a Detail truncating its name
// list would still carry it, and the count bound's alphabetic-prefix cut can never
// reach it. Its length is asserted against finTrailerMaxKeyNameBytes in code
// rather than pinned as a literal.
//
// A const and not a var: the parallel-tests rule
// (trail_run_outcome_test.go:1203-1205) is about shared BACKING ARRAYS, and a string
// constant has none.
const finContainNeedleKeyName = "PYRY-PROBE-KEY-NAME-NEEDLE-MUST-NOT-REACH-A-DETAIL"

// finContainNeedleKeyTrailer splices that name onto trailFixtureTrailer as ONE
// extra top-level key, in finOverlongKeyNameTrailer's shape
// (finding_key_name_bounds_test.go:113-116). Splicing onto a shipped renderer is
// what makes the other eleven names the REAL ENVELOPE NAMES rather than this
// fixture's own invention.
//
// trailFixtureTrailer and deliberately not trailPaddedTrailer(0): it carries no
// trailNeedle, so a red here names this plant and nothing else.
func finContainNeedleKeyTrailer() string {
	return strings.TrimSuffix(trailFixtureTrailer, "}") +
		`,"` + finContainNeedleKeyName + `":"v"}`
}

// TestFinContainNoDetailNamesAKeyName is AC4: a needle planted as a top-level key
// NAME reaches the published trailer_keys field and NO Detail anywhere in the
// artifact.
//
// This discharges the NAMES half of finTrailerRecord's prohibition that the Detail
// "may name no key and interpolate no COUNT of them"
// (finding_trailer_evidence_test.go:199-201), inherited from #1363 via #1364.
//
// # Why a needle sweep and not a second headroom row
//
// Measured on this tree through the shipped chain, over this fixture: the shipped
// trailer Detail is 225 bytes, and a Detail interpolating sighting.KeyNames INSIDE
// the trailDetail format is 426 — leaving 86 bytes of the 512-byte cap against the
// 42-byte trailNeedle yardstick, so it PASSES the shipped per-row headroom check
// (finding_trailer_evidence_test.go:1045-1053), observed green under exactly that
// mutation while this row went red. #1364's best hostile-name fixture put its own
// mutant at 444 bytes against a 470-byte budget, 27 short of red, which is why
// that ticket measured it, cut it and handed the obligation here. A needle has no
// dependence on a byte budget.
//
// # THE COUNT HALF HAS NO INSTRUMENT AND STAYS UNPROVEN
//
// Said plainly at the row rather than left to be assumed. finTrailerMaxKeyNames is
// 32, so interpolating the count costs two digits: no fixture makes that exceed a
// byte budget, no needle can be a count, and no finTrailerRecord.Detail is pinned
// by exact equality anywhere — every assertion over it is `!= ""`, a headroom
// measurement or a difference between two arms. This test discharges the NAMES
// half only.
func TestFinContainNoDetailNamesAKeyName(t *testing.T) {
	if len(finContainNeedleKeyName) > finTrailerMaxKeyNameBytes {
		t.Fatalf("the %d-byte needle key name exceeds the %d-byte per-name bound: it would be "+
			"published truncated and marked, and the sweep below would look for a string the "+
			"artifact never carried", len(finContainNeedleKeyName), finTrailerMaxKeyNameBytes)
	}

	files, read := finContainRender(t, finContainNeedleKeyTrailer())

	// NON-VACUITY, HALF ONE — THE READER, fatal. The reader must have read the thing
	// the sweep looks for, UNBOUNDED and UNTRUNCATED: twelve names, both bounds
	// inert, the needle among them WHOLE by element equality rather than by
	// substring.
	if want := len(trailExpectedKeyNames()) + 1; len(read) != want {
		t.Fatalf("the reader read %d name(s) %q, want %d — the envelope's eleven plus the spliced "+
			"one", len(read), read, want)
	}
	if len(read) > finTrailerMaxKeyNames {
		t.Fatalf("the reader read %d name(s) against the %d-name bound: a bound that BIT here would "+
			"leave the sweep looking at a prefix", len(read), finTrailerMaxKeyNames)
	}
	for _, name := range read {
		if len(name) > finTrailerMaxKeyNameBytes {
			t.Fatalf("the reader read a %d-byte name against the %d-byte per-name bound: this row "+
				"needs every name published whole", len(name), finTrailerMaxKeyNameBytes)
		}
	}
	if !slices.Contains(read, finContainNeedleKeyName) {
		t.Fatalf("the reader's %d name(s) %q do not include the spliced one: the splice did not "+
			"take, and a Detail interpolating the names could not carry it either", len(read), read)
	}

	// NON-VACUITY, HALF TWO — THE ARTIFACT, fatal and read off the WRITTEN FILE's
	// own keys as an operator would, rather than off the in-memory record. Element 0
	// because the needle leads with uppercase and trailKeyNames sorts.
	var artifact struct {
		Trailer struct {
			KeyNames []string `json:"trailer_keys"`
		} `json:"trailer"`
	}
	if err := json.Unmarshal(files[finWriteRecordFile], &artifact); err != nil {
		t.Fatalf("decoding %s: %v", finWriteRecordFile, err)
	}
	if got := artifact.Trailer.KeyNames; len(got) == 0 || got[0] != finContainNeedleKeyName {
		t.Fatalf("the artifact's trailer_keys hold %d name(s) %q, want the spliced one FIRST: the "+
			"claim below is that no Detail carries a name the record publishes, and over an "+
			"artifact not publishing it there is nothing to have leaked", len(got), got)
	}

	// THE CLAIM. Walked over run.json alone deliberately: run.md embeds the same
	// marshalled bytes inside a fence, so its Details are byte-identical and
	// finWriteObservedDetails takes a json.RawMessage. AC1's sweep is the one that
	// covers the note.
	details := make(map[string]string)
	if err := finWriteObservedDetails(files[finWriteRecordFile], details); err != nil {
		t.Fatalf("walking %s for details: %v", finWriteRecordFile, err)
	}
	if len(details) < 6 {
		t.Fatalf("the artifact walk found %d detail(s) at %v, want at least 6 — a walk that stopped "+
			"descending would leave the claim below holding over a subset of the artifact's Details",
			len(details), finWriteSorted(details))
	}
	// PATH AND BYTE LENGTH ONLY, never the Detail's string: finWriteSorted's rule
	// (finding_artifact_write_test.go:638-646). The needle is a rig-chosen constant
	// and naming the path is what makes the failure diagnosable.
	for _, path := range finWriteSorted(details) {
		if strings.Contains(details[path], finContainNeedleKeyName) {
			t.Errorf("the detail at %s names a top-level key the line carried. The key names are NOT "+
				"on finTrailerRecord's permitted Detail list, and the shipped per-row headroom check "+
				"cannot see this: at %d bytes it leaves %d of the %d-byte cap, well clear of the "+
				"%d-byte yardstick that check measures against", path, len(details[path]),
				reachMaxCommandBytes-len(details[path]), reachMaxCommandBytes, len(trailNeedle))
		}
	}
}
