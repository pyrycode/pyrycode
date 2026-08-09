//go:build e2e_realclaude

package realclaude

// The key-name reading #1357 ships: the NAMES a matched result-trailer line
// carried, read off the FULL line before the cap, and never a value from it.
//
// This file reaches no verdict about pyry and takes no measurement. Everything
// here runs offline: no live claude, no credentials, no daemon, no exec, no
// clock, no goroutine, no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
//
// # What the fixed decode cannot answer
//
// resultTrailer (tool_loop_test.go:194) is eight fixed fields, and
// TerminalReason is a plain string with omitempty — so an ABSENT
// terminal_reason and one emitted as "" both decode to "". That distinction is
// not academic: terminal_reason is a pyry invention (streamjson/emitter.go:428-437,
// streamrunner/watchdog.go:253), so on the headless PYRY_USE_STREAMJSON=1 path
// the trailer on a healthy run is claude's OWN result line and carries no
// terminal_reason at all. Telling claude's line from pyry's synthesised one is
// therefore a question about WHICH KEYS the line carried, not about any field's
// value — which is why the reading is a name set rather than a widened decode.
//
// # The ordering is the whole difficulty
//
// trailScanResult.Line is not the line: it is reachCapCommand of it
// (result_trailer_observation_test.go:107), 512 bytes plus a marker. A capped
// realistic trailer is TRUNCATED JSON, because `result` is sixth on the wire and
// the cap lands inside it — so a reader fed Line answers correctly on every short
// fixture and silently answers NOTHING on every realistic one. Measured on this
// tree:
//
//	fixture                     line bytes  capped  keys off the capped copy
//	trailFixtureTrailer                342     342  11
//	trailPaddedTrailer(0)              385     385  11
//	trailPaddedTrailer(2000)          2385     541   0
//
// The reader is therefore invoked INSIDE trailScan at the match return, where
// scanner.Bytes() is still whole — the same reason the decode runs there. The
// padded row of TestTrailKeyNamesReadsTheFullLine is what makes getting that
// wrong go red; a comment alone would not.
//
// # Names only, and by construction rather than by discipline
//
// A key scan that returned map[string]json.RawMessage and then got rendered would
// dump the whole assistant `result` field into a public issue — the exact leak the
// fixed decode exists to prevent, re-admitted by a different door. The map is
// discarded inside trailKeyNames and the signature carries []string, so no value
// can cross the boundary. The hazard is live rather than hypothetical: this
// package already formats a whole trailer sub-record with %+v
// (finding_run_record_test.go:775), and json.RawMessage values are the raw bytes.
//
// Bounding the names themselves is deliberately NOT here. They are
// attacker-influenced in principle — they arrive from claude's output — but
// trailScanResult is published by nothing (pinned at finding_run_record_test.go:787-812
// and finding_run_gather_test.go:2043-2059), so there is no rendering surface at
// this tier to bound. The per-name cap belongs at the tier that publishes: #1363.

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// --- the reader ---------------------------------------------------------------

// trailKeyNames returns the sorted names of line's top-level JSON keys, and
// nothing else. It returns nil when line does not decode as a JSON object.
//
// Three properties of the contract, each load-bearing:
//
//   - NO VALUE CROSSES IT. The map[string]json.RawMessage is discarded here; it
//     is never stored, returned, formatted or reachable from the returned value.
//     The signature is what makes that structural.
//   - TOP LEVEL ONLY, which is what the map decode gives and what the caller
//     wants: `usage` contributes `usage` and never its four sub-keys.
//   - SORTED, because a map decode offers no other stable order.
//
// Two consequences worth stating rather than discovering: duplicate top-level
// keys collapse to one name, and the input is already bounded by bufio.Scanner's
// 64 KiB default in trailScan, so no additional size cap is introduced here.
func trailKeyNames(line []byte) []string {
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(line, &keyed); err != nil {
		// The error is DROPPED rather than returned, wrapped or rendered, and
		// that is a containment decision rather than a shortcut: json.SyntaxError
		// carries a byte offset into its own input and json.UnmarshalTypeError
		// names the offending value. Both bytes are chosen by the line rather
		// than by the caller, so no fixture needle could catch that capture — a
		// planted needle only reaches an error that happens to name it. Returning
		// []string alone is what keeps "no value crosses this boundary" true by
		// construction instead of by review.
		return nil
	}
	names := make([]string, 0, len(keyed))
	for name := range keyed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- fixtures -----------------------------------------------------------------

// trailExpectedKeyNames is the eleven top-level names BOTH trailFixtureTrailer
// and trailPaddedTrailer(pad) carry, sorted. One expected slice therefore serves
// the short fixture and the past-the-cap one, which is what makes the two rows
// comparable at all.
//
// A function rather than a package-level var: it returns a slice, go test -race
// runs this package's tests in parallel, and a shared backing array would let one
// row's mutation reach another's (trail_run_outcome_test.go:608-652).
func trailExpectedKeyNames() []string {
	return []string{
		"duration_ms", "is_error", "num_turns", "result", "session_id",
		"stop_reason", "subtype", "terminal_reason", "total_cost_usd", "type", "usage",
	}
}

// trailKeyNamesNeedles is one DISTINCT needle per string-valued position of the
// fixture below. trailNeedle does not serve here: it is a single shared constant
// spliced into `result` alone, so a sweep using it could not tell a leak of
// `result` from a leak of `subtype`.
func trailKeyNamesNeedles() map[string]string {
	return map[string]string{
		"result":          "TRAIL-KEYS-RESULT-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE",
		"session_id":      "TRAIL-KEYS-SESSION-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE",
		"subtype":         "TRAIL-KEYS-SUBTYPE-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE",
		"stop_reason":     "TRAIL-KEYS-STOP-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE",
		"terminal_reason": "TRAIL-KEYS-TERMINAL-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE",
	}
}

// trailKeyNamesNeedledTrailer renders a trailer carrying a distinct needle in
// each of its five string-valued positions, with the wire order and the eleven
// top-level names of trailPaddedTrailer intact.
//
// `result`'s needle sits behind pad bytes of padding so it lands PAST the
// 512-byte cap, which is trailNeedle's own rule for a plant
// (result_trailer_observation_test.go:322-325): a hit on that needle could then
// only have come from the full line and never from the recorded copy. The test
// asserts the offset rather than trusting this comment.
func trailKeyNamesNeedledTrailer(pad int) string {
	n := trailKeyNamesNeedles()
	return `{"type":"result","subtype":"` + n["subtype"] + `","is_error":true,` +
		`"duration_ms":9001,"num_turns":6,"result":"` + strings.Repeat("x", pad) + n["result"] + `",` +
		`"stop_reason":"` + n["stop_reason"] + `","session_id":"` + n["session_id"] + `",` +
		`"total_cost_usd":0.42,"usage":{"input_tokens":120,"output_tokens":45,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0},` +
		`"terminal_reason":"` + n["terminal_reason"] + `"}`
}

// trailKeyNamesNoTerminalReason and trailKeyNamesEmptyTerminalReason are the same
// trailer differing in ONE thing: whether terminal_reason is on the line at all.
// Both carry "type":"result" — without it trailScan never matches and a test over
// them would compare two empty reads and pass.
func trailKeyNamesNoTerminalReason() string {
	return `{"type":"result","subtype":"success","is_error":false,"num_turns":3,` +
		`"stop_reason":"end_turn","result":"done"}`
}

func trailKeyNamesEmptyTerminalReason() string {
	return `{"type":"result","subtype":"success","is_error":false,"num_turns":3,` +
		`"stop_reason":"end_turn","result":"done","terminal_reason":""}`
}

// --- tests ---------------------------------------------------------------------

// TestTrailKeyNamesReadsTheFullLine pins the ordering: the reader runs against
// the whole matched line, before the cap.
//
// The two table rows carry the SAME expected slice, and that is the point. The
// short fixture is green under either implementation; the padded one — whose
// `result` field pushes the line ~1.9 KiB past the cap — is what discriminates,
// because a reader fed trailScanResult.Line yields zero names there. The third
// sub-test states the same fact from the other side by calling the reader on that
// scan's own recorded copy.
//
// One DeepEqual against trailExpectedKeyNames() covers exactness, sortedness and
// non-descent in a single assertion: `usage` contributes `usage`, and its four
// sub-keys are absent because the map decode reads one level.
func TestTrailKeyNamesReadsTheFullLine(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{
			// Green under either implementation — 342 bytes, the cap never fires.
			// Present so the discriminating row below is read as a difference in
			// the LINE rather than in the reader.
			name: "a short trailer the cap leaves intact",
			line: trailFixtureTrailer,
		},
		{
			// THE RED. Capped at 541 bytes, the truncation lands inside `result`
			// and leaves the JSON unterminated, so a reader fed Line returns
			// nothing here and this row is the only thing that says so.
			name: "a trailer whose result field pushes the line past the cap",
			line: trailPaddedTrailer(2000),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := trailScan([]byte(tc.line + "\n"))

			if got.State != trailSeen {
				t.Fatalf("state: got %q (%s), want %q", got.State, got.Detail, trailSeen)
			}
			if want := trailExpectedKeyNames(); !reflect.DeepEqual(got.KeyNames, want) {
				t.Errorf("key names: got %q, want %q — read off the FULL %d-byte line, sorted, "+
					"top level only", got.KeyNames, want, len(tc.line))
			}
		})
	}

	t.Run("the same reader over the recorded capped copy yields none", func(t *testing.T) {
		line := trailPaddedTrailer(2000)
		got := trailScan([]byte(line + "\n"))

		if got.State != trailSeen {
			t.Fatalf("state: got %q (%s), want %q", got.State, got.Detail, trailSeen)
		}
		// Without this the row could pass against a copy the cap never touched,
		// and it would then prove nothing about the ordering.
		if !strings.HasSuffix(got.Line, reachTruncationMarker) {
			t.Fatalf("recorded line: %d bytes not ending in %q — this row needs a copy the cap "+
				"actually truncated", len(got.Line), reachTruncationMarker)
		}

		if names := trailKeyNames([]byte(got.Line)); len(names) != 0 {
			t.Errorf("key names off the capped copy: got %q, want none — the cap lands inside "+
				"`result` and leaves the JSON unterminated", names)
		}
	})
}

// TestTrailKeyNamesSeparatesAbsenceFromZeroValue is the reading's whole reason to
// exist: an absent terminal_reason and one emitted as "" are the same value after
// the decode, and different name sets before it.
//
// The second half — that both lines decode to the SAME TerminalReason — is
// asserted rather than assumed. It is what establishes that the existing decode
// could not have answered this, which is the claim the new field rests on.
func TestTrailKeyNamesSeparatesAbsenceFromZeroValue(t *testing.T) {
	absent := trailScan([]byte(trailKeyNamesNoTerminalReason() + "\n"))
	empty := trailScan([]byte(trailKeyNamesEmptyTerminalReason() + "\n"))

	for _, got := range []struct {
		name string
		res  trailScanResult
	}{{"absent", absent}, {"present-and-empty", empty}} {
		if got.res.State != trailSeen {
			t.Fatalf("%s: state got %q (%s), want %q — both fixtures carry \"type\":\"result\", "+
				"and a miss here would leave this test comparing two empty reads",
				got.name, got.res.State, got.res.Detail, trailSeen)
		}
		if got.res.Trailer == nil {
			t.Fatalf("%s: trailer got nil, want a decode of the full line", got.name)
		}
	}

	if reflect.DeepEqual(absent.KeyNames, empty.KeyNames) {
		t.Errorf("key names: both reads are %q — the two lines differ in whether terminal_reason "+
			"is on them at all, and a reading that cannot see that answers nothing the decode "+
			"below does not already answer", absent.KeyNames)
	}
	if slices.Contains(absent.KeyNames, "terminal_reason") {
		t.Errorf("key names of the line with no terminal_reason: got %q, want it absent",
			absent.KeyNames)
	}
	if !slices.Contains(empty.KeyNames, "terminal_reason") {
		t.Errorf("key names of the line carrying \"terminal_reason\":\"\": got %q, want it present",
			empty.KeyNames)
	}

	if absent.Trailer.TerminalReason != empty.Trailer.TerminalReason {
		t.Fatalf("terminal_reason after the decode: got %q and %q — this test's premise is that "+
			"the fixed decode COLLAPSES the two, and if it did not the reading would be "+
			"unnecessary", absent.Trailer.TerminalReason, empty.Trailer.TerminalReason)
	}
	if absent.Trailer.TerminalReason != "" {
		t.Errorf("terminal_reason after the decode: got %q, want %q from both lines",
			absent.Trailer.TerminalReason, "")
	}
}

// TestTrailKeyNamesCarryNoValues is AC3: no value from the line reaches the names.
//
// # The assertion is scoped to KeyNames alone, deliberately
//
// The obvious idiom is forty lines above the field this ticket adds:
// TestTrailScan's padded sub-test marshals the WHOLE trailScanResult and sweeps
// the bytes (result_trailer_observation_test.go:542-549). That is correct there,
// because trailPaddedTrailer plants only in `result` — a field resultTrailer does
// not decode. Copied here it goes RED AGAINST A CORRECT BUILD: resultTrailer
// decodes Subtype, StopReason and TerminalReason, so three of the five needles
// below are carried through Trailer BY DESIGN and published verbatim downstream.
// The artifact-wide sweep that would catch a leak in those is #1362's, with its
// own narrower plant list (finding_artifact_write_test.go:274-289 states the
// plant-only-where-the-pipeline-reduces rule).
//
// So this sweeps the names and nothing else. Widening it to the enclosing record
// would be weakening a correct containment guard to satisfy a mis-scoped test.
func TestTrailKeyNamesCarryNoValues(t *testing.T) {
	needles := trailKeyNamesNeedles()
	if len(needles) != 5 {
		t.Fatalf("the plant list holds %d needle(s), want 5 — one per string-valued position",
			len(needles))
	}
	seen := make(map[string]string, len(needles))
	for field, needle := range needles {
		if other, dup := seen[needle]; dup {
			t.Fatalf("%s and %s carry the same needle %q: a shared needle cannot say WHICH "+
				"position leaked", field, other, needle)
		}
		seen[needle] = field
	}

	line := trailKeyNamesNeedledTrailer(600)
	// The plant-past-the-cap rule, asserted rather than trusted: `result`'s needle
	// must sit past reachMaxCommandBytes, so a hit on it could only have come from
	// the full line and never from the recorded copy.
	if at := strings.Index(line, needles["result"]); at <= reachMaxCommandBytes {
		t.Fatalf("`result`'s needle sits at offset %d of %d, inside the %d-byte cap — the plant "+
			"must land past it or a hit proves nothing about which copy was read",
			at, len(line), reachMaxCommandBytes)
	}

	got := trailScan([]byte(line + "\n"))

	// THE NON-VACUITY PRECONDITION, asserted before the sweep: a reader that
	// returned nothing would leak nothing and pass everything below.
	if got.State != trailSeen {
		t.Fatalf("state: got %q (%s), want %q", got.State, got.Detail, trailSeen)
	}
	if want := trailExpectedKeyNames(); !reflect.DeepEqual(got.KeyNames, want) {
		t.Fatalf("key names: got %q, want %q — the sweep below is vacuous against a reader that "+
			"records no names at all", got.KeyNames, want)
	}

	for _, name := range got.KeyNames {
		for field, needle := range needles {
			if strings.Contains(name, needle) {
				t.Errorf("the recorded name %q carries %s's value: the reading is names only, and "+
					"a value reaching it re-admits the leak resultTrailer's fixed field set "+
					"exists to prevent", name, field)
			}
		}
	}
}

// TestTrailResultTrailerFieldSetIsPinned is AC5's first half. resultTrailer has
// no `result` member, and that omission is what makes the 512-byte cap safe to
// apply to Line alone: the assistant payload structurally cannot reach the
// decoded value. #1357 answers its question with a name set precisely so it does
// not have to widen this struct, and this test is what makes a later widening go
// red with the reason attached.
//
// Types are compared against reflect.TypeOf values rather than string literals:
// Type.String() renders a named type package-qualified, which makes a string
// comparison brittle for no gain.
func TestTrailResultTrailerFieldSetIsPinned(t *testing.T) {
	want := []struct {
		name string
		tag  string
		typ  reflect.Type
	}{
		{"Type", "type", reflect.TypeOf("")},
		{"Subtype", "subtype", reflect.TypeOf("")},
		{"StopReason", "stop_reason", reflect.TypeOf("")},
		{"NumTurns", "num_turns", reflect.TypeOf(0)},
		{"PermissionDenials", "permission_denials,omitempty", reflect.TypeOf((*[]json.RawMessage)(nil))},
		{"IsError", "is_error,omitempty", reflect.TypeOf(false)},
		{"TerminalReason", "terminal_reason,omitempty", reflect.TypeOf("")},
		{"Usage", "usage,omitempty", reflect.TypeOf(resultTrailerUsage{})},
	}

	typ := reflect.TypeOf(resultTrailer{})
	if typ.NumField() != len(want) {
		t.Fatalf("resultTrailer has %d field(s), want %d — it carries no `result` member, and "+
			"that is what makes the cap safe to apply to trailScanResult.Line alone. #1357 reads "+
			"the line's KEY NAMES rather than widening this struct, so a new field here is a "+
			"decision to re-open the leak this shape closes", typ.NumField(), len(want))
	}
	for i, w := range want {
		got := typ.Field(i)
		if got.Name != w.name || got.Tag.Get("json") != w.tag || got.Type != w.typ {
			t.Errorf("field %d: got %s %s `json:%q`, want %s %s `json:%q`",
				i, got.Name, got.Type, got.Tag.Get("json"), w.name, w.typ, w.tag)
		}
	}
}

// TestTrailScanResultReachesNoRawMessageMap is AC5's second half, in the shape
// TestFinSightingReachesNoScanType uses (finding_run_gather_test.go:2043).
//
// # The ban names the MAP, never its element
//
// Re-derived on this tree with the same walk: trailScanResult ALREADY reaches
// json.RawMessage, through Trailer *resultTrailer → PermissionDenials
// *[]json.RawMessage (tool_loop_test.go:199). A ban naming the element type would
// therefore be red against correct shipped code on the day it was written. The
// map type is green today and is a live guard against the field that would change
// that — which is exactly the field trailKeyNames was tempted to return.
//
// The walk is transitive, so this one call also covers "no type transitively
// holding one".
func TestTrailScanResultReachesNoRawMessageMap(t *testing.T) {
	carrier := reflect.TypeOf(trailScanResult{})
	forbidden := reflect.TypeOf(map[string]json.RawMessage{})

	if finRecordInputReaches(carrier, forbidden, map[reflect.Type]bool{}) {
		t.Errorf("%s is reachable from trailScanResult: its values are the RAW BYTES of the "+
			"line, so a %%v on the map — or on any struct transitively holding it, as this "+
			"package already does at finding_run_record_test.go:775 — prints the whole assistant "+
			"`result` field. trailKeyNames discards the map inside itself and returns []string, "+
			"which is what keeps this true by construction", forbidden)
	}

	// The control: the walk finds what is actually there, so the green above is a
	// property of the record rather than of a walk that reports clean on
	// everything. json.RawMessage IS reachable, via PermissionDenials.
	if !finRecordInputReaches(carrier, reflect.TypeOf(json.RawMessage{}), map[reflect.Type]bool{}) {
		t.Error("json.RawMessage is NOT reachable from trailScanResult: it should be, via " +
			"Trailer *resultTrailer → PermissionDenials *[]json.RawMessage. Either that field " +
			"moved or this walk stopped walking — and if it stopped walking, the ban above " +
			"proves nothing")
	}
}
