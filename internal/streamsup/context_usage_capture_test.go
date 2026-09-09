package streamsup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// #2287 — the hermetic half of the `get_context_usage` control-request capture.
//
// The live half is internal/e2e/realclaude's TestRealClaude_ContextUsageCapture: it
// drives one child through a completed turn, writes a get_context_usage request at
// each `detail` value, and commits the record. This file is what reads those bytes
// back inside `make check` and pins the response shape they carry.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// compactionCapturePath gives: the bytes are only BYTES, the e2e_realclaude build
// tag belongs to that package's GO FILES rather than to its testdata, and reading
// it from here is what keeps the measurement inside `make check` instead of behind
// an opt-in gate that SKIPS (exit 0) with no claude login. A pin living in that
// package would never be compiled by the standard gate at all.
//
// THIS IS ANOTHER READER, NOT A GENERALISATION of capturedLines,
// capturedToolProgressLines or compactionCapture. Each of those docblocks forbids
// growing a path parameter, because the is_capture assertion is what stops a
// hand-built payload file being swapped in behind the provenance checks. So this
// takes the same shape: its own package constants, no path parameter, and every
// provenance check written out below rather than borrowed. None of them can decode
// another's record shape, and reaching for the wrong one yields zero values rather
// than a failure.
//
// This package is the pin's home because it is the CONSUMER: #2288's production
// writer lands in envelope.go beside WriteInitialize, and it decodes the shape this
// file pins.

// contextUsageCaptureGlob matches the committed capture at ANY claude version.
//
// A GLOB RATHER THAN A VERSION-SPLICED CONSTANT, which is a deliberate departure
// from compactionCapturePath and follows arcapFixturePath's reasoning instead. That
// constant is right when the fixture is a reader's input and a claude upgrade should
// be a loud instruction to re-capture. HERE THE RECORD IS THE DELIVERABLE: a claude
// bump landing between the moment this constant was authored and the moment the live
// gate runs would refuse the promotion outright and turn the whole ticket's output
// into nothing committed. So the producing side names the file from the version
// `claude --version` actually printed, and this side matches any of them.
//
// The version is not unpinned by that, only relocated: the record carries its own
// claude_version and the pin below is read against the file that names it, so a
// reader can always say which release a shape was measured on.
//
// Relative because `go test` runs in the package source directory. filepath.Glob
// returns no error for a pattern that matches nothing — only for a malformed one —
// so an absent fixture arrives here as an empty slice rather than as an error.
const contextUsageCaptureGlob = "../e2e/realclaude/testdata/context_usage_v*.json"

// contextUsagePinnedShapes IS THE MEASUREMENT THIS TICKET COMMITS: one entry per arm
// the capture drove, spelled "<arm>/<subtype>" when that arm's request was answered
// and "<arm>/unanswered" when it was not.
//
// BOTH SPELLINGS ARE REAL RESULTS, and that is the whole reason the vocabulary has
// two shapes rather than one. Whether the CLI's stdin control channel answers
// get_context_usage AT ALL is the unknown this capture exists to settle — the stem
// appears nowhere else in this tree — so "summary/unanswered" is a finding about
// claude, not a failed run, and #2288 designs against it exactly as it would against
// a subtype. A reader that treated an unanswered arm as an absence would delete the
// measurement.
//
// IT IS EMPTY ON PURPOSE UNTIL THE LIVE GATE HAS RUN. The fixture cannot exist
// before `make e2e-realclaude` produces it, which happens after verification, so a
// reader asserting against bytes any earlier would redden `make check` for every
// unrelated ticket. contextUsageReaderGate turns that into a state machine with
// exactly one legal skip: filling this slice is the commit that lands the fixture,
// and a fixture landing WITHOUT it fatals rather than passing quietly.
//
// Fill it from the record's own per-arm response_subtype and answered fields — and
// note that the reader below does NOT trust those labels when it checks the pin. It
// re-derives each shape from the arm's own recorded response bytes, so committing
// the record cannot turn this measurement into an agreement with itself.
var contextUsagePinnedShapes = []string{}

// The four states of (fixture, pin). Only the first is a skip, and only on the leg
// before the live gate has ever run.
const (
	contextUsageGateSkip  = "skip"
	contextUsageGateRun   = "run"
	contextUsageGateFatal = "fatal"
)

// contextUsageReaderGate is pure so all four quadrants are proved on every run,
// including the leg where the fixture is still absent and the readers themselves can
// assert nothing.
func contextUsageReaderGate(fixtureExists, pinFilled bool) (action, reason string) {
	switch {
	case !fixtureExists && !pinFilled:
		return contextUsageGateSkip, "the capture has not been taken yet: `make e2e-realclaude` on an " +
			"authenticated machine runs TestRealClaude_ContextUsageCapture, which arms on this fixture's " +
			"absence and writes it both in-repo and to an artifact directory outside every worktree. This " +
			"is the ONLY state in which this reader may skip, and it ends the moment the bytes and the " +
			"pin land together"
	case !fixtureExists && pinFilled:
		return contextUsageGateFatal, "contextUsagePinnedShapes names the shapes a capture observed but no " +
			"file matches " + contextUsageCaptureGlob + ". Restore the fixture, or if the capture was " +
			"deliberately dropped, empty the pin in the same commit — a pin with no bytes behind it is a " +
			"measurement nothing supports"
	case fixtureExists && !pinFilled:
		return contextUsageGateFatal, "a capture matching " + contextUsageCaptureGlob + " has landed but " +
			"contextUsagePinnedShapes is still empty, so nothing pins what it measured. Read the record's " +
			"arms — response_subtype where answered is true, unanswered where it is false — and write " +
			"them here IN THE COMMIT THAT ADDS THE FIXTURE. Until then the bytes are committed and " +
			"unpinned, which is the state this gate exists to make impossible"
	default:
		return contextUsageGateRun, ""
	}
}

// contextUsageCapture is the slice of the record this reader needs. The producing
// side writes many more fields — the whole daemon-vs-claude comparison among them —
// and this decodes only what the pin is about.
//
// ControlResponses holds the correlated response lines VERBATIM as claude wrote
// them, which is what lets the shape be re-derived rather than read off a label.
type contextUsageCapture struct {
	IsCapture     bool   `json:"is_capture"`
	ClaudeVersion string `json:"claude_version"`
	Arms          []struct {
		Arm              string            `json:"arm"`
		Detail           string            `json:"detail"`
		Answered         bool              `json:"answered"`
		ResponseSubtype  string            `json:"response_subtype"`
		ControlResponses []json.RawMessage `json:"control_responses"`
	} `json:"arms"`
}

// contextUsageShape spells one arm's observed result the way the pin does.
//
// Derived here rather than imported from the probe: that package is behind a build
// tag this one does not carry, and re-deriving is also what makes the comparison a
// measurement rather than an agreement by construction.
func contextUsageShape(arm, subtype string, answered bool) string {
	if !answered {
		return arm + "/unanswered"
	}
	return arm + "/" + subtype
}

// contextUsageObservedSubtype reads the envelope subtype out of one recorded
// response line, searching the THREE PLACEMENTS a control_response is known to use:
// top level, under `response`, and under `response.response`.
//
// The third is not hypothetical padding. Parser.consumeLine's control_response arm
// records, as measured shape, that subtype and request_id arrive nested under
// `response` rather than at top level; the initialize capture then measured a reply
// nesting ONE LEVEL DEEPER for its payload. A reader that stopped at either of the
// first two levels would report a false absence, which is exactly what the first cut
// of that family's summariser did.
//
// The FIRST non-empty subtype across placements and across lines wins, so an arm
// whose lines carry none reports "" — and "" reaching the pin is caught by the
// caller rather than silently spelled as an arm with a trailing slash.
func contextUsageObservedSubtype(lines []json.RawMessage) string {
	for _, raw := range lines {
		var env struct {
			Subtype  string `json:"subtype"`
			Response struct {
				Subtype  string `json:"subtype"`
				Response struct {
					Subtype string `json:"subtype"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		for _, sub := range []string{env.Subtype, env.Response.Subtype, env.Response.Response.Subtype} {
			if sub != "" {
				return sub
			}
		}
	}
	return ""
}

// contextUsageLocate returns the single committed capture's path, whether one
// exists, and a fatal reason when the glob is ambiguous.
//
// AMBIGUITY REFUSES RATHER THAN GUESSING, and that is the deterministic answer as
// well as the safe one: two captures at two claude versions are two different
// measurements, and picking the lexically-first would silently pin whichever release
// sorted lower. The producing side names one file per version and never deletes an
// older one on its own, so this state is reachable by an ordinary re-capture — the
// resolution is to delete the stale fixture in the commit that repins.
func contextUsageLocate(t *testing.T) (path string, exists bool) {
	t.Helper()
	matches, err := filepath.Glob(contextUsageCaptureGlob)
	if err != nil {
		// Only a malformed pattern reaches here; a pattern matching nothing returns
		// an empty slice and a nil error. So this is a defect in the constant above,
		// not a missing fixture.
		t.Fatalf("#2287: the capture glob %q is malformed: %v", contextUsageCaptureGlob, err)
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		t.Fatalf("#2287: %d files match %s: %v. Two captures are two measurements at two claude "+
			"releases, and picking one would pin whichever release sorted lower. Delete the stale "+
			"fixture in the commit that repins contextUsagePinnedShapes",
			len(matches), contextUsageCaptureGlob, matches)
	}
	if len(matches) == 0 {
		return "", false
	}
	return matches[0], true
}

// contextUsageRead runs the gate, then decodes and provenance-checks the capture.
// It returns the decoded record only in the run state; a skip or a fatal never
// returns.
//
// Every failure once the gate says run is t.Fatalf, never a skip: the capture is
// committed, so a missing or malformed record is a broken premise rather than an
// unavailable resource.
func contextUsageRead(t *testing.T, ticket string) contextUsageCapture {
	t.Helper()

	path, exists := contextUsageLocate(t)
	switch action, reason := contextUsageReaderGate(exists, len(contextUsagePinnedShapes) > 0); action {
	case contextUsageGateSkip:
		t.Skipf("#%s: %s", ticket, reason)
	case contextUsageGateFatal:
		t.Fatalf("#%s: %s", ticket, reason)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		// The glob already matched this name, so a read failure here is a permission
		// or a race, not an absence — and an absence would have been the gate's to
		// report.
		t.Fatalf("#%s: reading capture %s: %v", ticket, path, err)
	}
	var capture contextUsageCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("#%s: decoding capture %s: %v", ticket, path, err)
	}
	if !capture.IsCapture {
		t.Fatalf("#%s: %s: is_capture is false — this file must be a genuine claude capture, never a "+
			"hand-written payload; a guessed response carries whatever shape its author expected, which "+
			"is precisely the reading this ticket spent a live gate lap to replace", ticket, path)
	}
	// `claude --version` prints "<version> (Claude Code)", so a record's version may
	// carry a trailing parenthesised product name. An "<unavailable: ...>" fails the
	// leading-digit check, which is correct: a capture that could not read the version
	// it was taken at cannot vouch for any release.
	token, _, _ := strings.Cut(strings.TrimSpace(capture.ClaudeVersion), " ")
	if token == "" || token[0] < '0' || token[0] > '9' {
		t.Fatalf("#%s: %s: claude_version is %q, which does not begin with a version token. A capture "+
			"that could not read the release it was taken at cannot vouch for one, and the pin below "+
			"would then describe a shape nobody can attribute", ticket, path, capture.ClaudeVersion)
	}
	if len(capture.Arms) == 0 {
		t.Fatalf("#%s: %s holds ZERO arms. A record with none is vacuous — every assertion built on it "+
			"would pass without reading a byte claude sent", ticket, path)
	}
	return capture
}

// TestRealClaudeContextUsageCaptureShapesArePinned is #2287's AC 5: the committed
// capture is read inside `make check` and the response shape it observed is pinned,
// per arm.
//
// THE SHAPE IS RE-DERIVED FROM EACH ARM'S OWN RECORDED RESPONSE BYTES, never taken
// from the record's response_subtype label. Those labels were written by the probe,
// and a reader that trusted them would pin the probe's decoding rather than claude's
// line — the two are different claims, and only the second one is a measurement. The
// label is still checked, one assertion below, precisely because a disagreement
// between the record's own description and the bytes it describes is a defect in the
// artifact worth failing on.
//
// An UNANSWERED arm is a pinned shape rather than a gap, for the reason
// contextUsagePinnedShapes states: whether the CLI answers this subtype at all is
// the unknown, so its silence is the finding.
func TestRealClaudeContextUsageCaptureShapesArePinned(t *testing.T) {
	capture := contextUsageRead(t, "2287")

	seen := make([]string, 0, len(capture.Arms))
	armNames := make(map[string]bool, len(capture.Arms))
	for _, arm := range capture.Arms {
		if arm.Arm == "" {
			t.Fatalf("%s: an arm carries an empty name, so its pinned shape would be an unattributable "+
				"\"/subtype\" and two such arms would collide into one", contextUsageCaptureGlob)
		}
		if armNames[arm.Arm] {
			t.Fatalf("%s: arm %q appears twice. The pin is a SET, so a duplicate silently collapses two "+
				"different measurements into one entry and the count assertion below cannot see it",
				contextUsageCaptureGlob, arm.Arm)
		}
		armNames[arm.Arm] = true

		observed := contextUsageObservedSubtype(arm.ControlResponses)

		// The record's own coherence, checked before the shape is derived from it.
		// answered:true with no decodable subtype anywhere in the recorded lines means
		// the record contradicts itself, and the derived shape would then be "<arm>/".
		if arm.Answered && observed == "" {
			t.Fatalf("%s: arm %q is recorded answered with %d response line(s), but no subtype decodes "+
				"out of them at any of the three placements. The record contradicts itself and the "+
				"pinned shape would be an empty subtype",
				contextUsageCaptureGlob, arm.Arm, len(arm.ControlResponses))
		}
		if !arm.Answered && len(arm.ControlResponses) > 0 {
			t.Fatalf("%s: arm %q is recorded UNANSWERED while carrying %d response line(s). Those two "+
				"facts cannot both hold: a correlated response is what answered means, so either the "+
				"correlation is wrong or the flag is",
				contextUsageCaptureGlob, arm.Arm, len(arm.ControlResponses))
		}
		if arm.Answered && observed != arm.ResponseSubtype {
			t.Fatalf("%s: arm %q is labelled response_subtype %q but its own bytes say %q — the "+
				"record's description disagrees with the lines it describes, so neither can be trusted",
				contextUsageCaptureGlob, arm.Arm, arm.ResponseSubtype, observed)
		}

		seen = append(seen, contextUsageShape(arm.Arm, observed, arm.Answered))
	}

	got := append([]string(nil), seen...)
	sort.Strings(got)
	want := append([]string(nil), contextUsagePinnedShapes...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s: the capture's arms observed %v, but contextUsagePinnedShapes says %v.\n"+
			"If claude's shape genuinely changed, re-capture at the new version and repin both ends "+
			"together; a pin edited to match a fixture nobody re-read turns this measurement into an "+
			"agreement with itself", contextUsageCaptureGlob, got, want)
	}
}

// TestContextUsageCaptureCoversBothDetailValues is #2287's other half of AC 1 read
// from this side: the committed record carries one arm per `detail` value, and the
// two are DISTINCT.
//
// It is separate from the pin above rather than folded into it, because the two
// catch different mutants and each is meant to be the sole red for its own. The pin
// compares a SET of shapes: a probe that sent "summary" twice, or that dropped the
// "full" arm entirely, produces a set the pin can be edited to match, and nothing
// there would notice. What this row cannot do is say which subtype came back —
// that is the pin's.
//
// The detail values are spelled here as literals rather than read from the record,
// which is the point: they are a SECOND, INDEPENDENT COPY of the two values the SDK
// declares, so a probe that quietly sent one value twice reddens here instead of
// pinning a shape nobody asked for.
func TestContextUsageCaptureCoversBothDetailValues(t *testing.T) {
	capture := contextUsageRead(t, "2287")

	want := map[string]bool{"summary": true, "full": true}
	got := make(map[string]int, len(capture.Arms))
	for _, arm := range capture.Arms {
		got[arm.Detail]++
	}
	for detail := range want {
		switch got[detail] {
		case 1:
			// The measured case.
		case 0:
			t.Errorf("%s: no arm carries detail %q. AC 1 asks for a request at EACH detail value, and "+
				"the two differ in what they cost claude to compute — summary answers from the last "+
				"response's usage while full counts each category through a token-count API — so a "+
				"record missing one settles nothing about the difference this capture exists to measure",
				contextUsageCaptureGlob, detail)
		default:
			t.Errorf("%s: %d arms carry detail %q, want exactly 1. Two arms at one detail value make "+
				"the shape pin describe a comparison that was never driven",
				contextUsageCaptureGlob, got[detail], detail)
		}
	}
	for detail, n := range got {
		if !want[detail] {
			t.Errorf("%s: %d arm(s) carry detail %q, which is neither of the two values the subtype "+
				"declares. Either the probe sent a third value or the record's detail column is not "+
				"what was written on the wire", contextUsageCaptureGlob, n, detail)
		}
	}
}

// TestContextUsageReaderGateHasExactlyOneLegalSkip proves the four quadrants of the
// sequencing gate, and it runs on every leg INCLUDING the one where the fixture does
// not exist yet — which is the leg where both readers above can assert nothing, and
// where this is the file's only non-vacuous coverage.
//
// The third row is the one the whole design turns on. #1763 spent real tokens on a
// live gate that ran green and landed none of the three artifacts its acceptance
// criteria asked for, and #2229 lost its fixture to a detached gate worktree while
// its own log said "commit it". Here the equivalent miss is bytes that land with
// nothing pinning what they measured, and a gate that passed in that state would
// hide it exactly as well.
func TestContextUsageReaderGateHasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		fixtureExists bool
		pinFilled     bool
		want          string
	}{
		{"before the live gate has run: the one legal skip", false, false, contextUsageGateSkip},
		{"a pin whose bytes are gone", false, true, contextUsageGateFatal},
		{"bytes committed with nothing pinning them", true, false, contextUsageGateFatal},
		{"the steady state", true, true, contextUsageGateRun},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, reason := contextUsageReaderGate(tc.fixtureExists, tc.pinFilled)
			if action != tc.want {
				t.Errorf("contextUsageReaderGate(%v, %v) = %q, want %q", tc.fixtureExists, tc.pinFilled,
					action, tc.want)
			}
			if action == contextUsageGateRun && reason != "" {
				t.Errorf("the run state named a reason %q; nothing reads it", reason)
			}
			if action != contextUsageGateRun && reason == "" {
				t.Errorf("%s named no reason; the skip or fatal message would say nothing", action)
			}
		})
	}
}

// TestContextUsageObservedSubtypeReadsAllThreePlacements is the targeted check on
// the one piece of logic here that is not a read of committed bytes. It runs on
// every leg, fixture or no fixture.
//
// The doubly-nested row is the load-bearing one and it is not hypothetical: a
// summariser reading only the first two levels reported a false absence against a
// live 2.1.239 reply, which is what the third placement was added for in the
// initialize family.
//
// The last two rows are what keep contextUsageShape's unanswered spelling honest:
// a line that decodes to no subtype at all, and a line that is not JSON, must both
// report "" rather than panicking or inventing one — the pin's caller turns "" on an
// answered arm into a fatal, and it can only do that if this returns it.
func TestContextUsageObservedSubtypeReadsAllThreePlacements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"top level", []string{`{"type":"control_response","subtype":"success"}`}, "success"},
		{"nested under response",
			[]string{`{"type":"control_response","response":{"subtype":"success"}}`}, "success"},
		{"nested under response.response, the deeper measured shape",
			[]string{`{"type":"control_response","response":{"response":{"subtype":"success"}}}`}, "success"},
		{"an error subtype is a shape like any other",
			[]string{`{"type":"control_response","response":{"subtype":"error","error":"unrecognized"}}`}, "error"},
		{"the first non-empty across lines wins",
			[]string{`{"type":"control_response"}`, `{"type":"control_response","response":{"subtype":"error"}}`},
			"error"},
		{"no placement carries a subtype",
			[]string{`{"type":"control_response","response":{"error":"unrecognized"}}`}, ""},
		{"a line that is not JSON is skipped, not fatal", []string{`not json at all`}, ""},
		{"no lines at all", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := make([]json.RawMessage, 0, len(tc.lines))
			for _, l := range tc.lines {
				lines = append(lines, json.RawMessage(l))
			}
			if got := contextUsageObservedSubtype(lines); got != tc.want {
				t.Errorf("#2287: contextUsageObservedSubtype = %q, want %q; this is what the pin "+
					"compares against, so a placement it cannot read is a false absence rather than "+
					"a measurement", got, tc.want)
			}
		})
	}
}

// TestContextUsageShapeSpellsAnUnansweredArm pins the two spellings the pin's
// vocabulary uses, and it is the sole red for a helper that spelled an unanswered
// arm as "<arm>/" — which would read as an answered arm carrying an empty subtype
// and would be indistinguishable in the pin from the record-contradiction the reader
// fatals on.
func TestContextUsageShapeSpellsAnUnansweredArm(t *testing.T) {
	t.Parallel()

	if got, want := contextUsageShape("summary", "success", true), "summary/success"; got != want {
		t.Errorf("#2287: an answered arm spells %q, want %q", got, want)
	}
	// The subtype is IGNORED when the arm went unanswered, not appended: a probe that
	// left a stale subtype on an unanswered arm must still pin as unanswered, because
	// the flag is the measurement and the subtype is derived from bytes that do not
	// exist.
	if got, want := contextUsageShape("full", "success", false), "full/unanswered"; got != want {
		t.Errorf("#2287: an unanswered arm spells %q, want %q — an unanswered send point is this "+
			"capture's whole reading, not a gap in it", got, want)
	}
}
