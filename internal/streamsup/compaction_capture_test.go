package streamsup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// compactionCapturePath is the committed real-claude capture of one live
// `/compact` turn (#2229), produced by internal/e2e/realclaude's
// TestRealClaude_CompactionCapture: two priming turns then `/compact`, with every
// line of the compact turn recorded upstream of the parser and the compaction ones
// marked by content.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// toolProgressCapturePath gives: the bytes are only BYTES, the e2e_realclaude
// build tag belongs to that package's Go files rather than to its testdata, and
// reading it from here is what keeps the measurement inside `make check` instead
// of behind an opt-in gate that SKIPS (exit 0) with no claude login.
//
// THIS IS A THIRD READER, NOT A GENERALISATION OF capturedLines OR
// capturedToolProgressLines. The first one's docblock forbids by name growing it a
// path parameter, because its is_capture assertion is what stops a hand-built
// payload file being swapped in behind the provenance checks. So this takes the
// same shape the second one did: its own package constants, no path parameter, and
// every provenance check written out below rather than borrowed. None of the three
// can decode another's record shape, and reaching for the wrong one yields zero
// values rather than a failure.
const compactionCapturePath = "../e2e/realclaude/testdata/compaction_v" +
	compactionCaptureVersion + ".json"

// compactionCaptureVersion is the claude release the capture was taken at,
// spliced into the path above rather than repeated so the filename cannot drift
// from the version this reader enforces. The producing side refuses to write under
// a mismatched name (ccapRecord.fixtureWorthy), so a claude upgrade is a loud
// instruction to re-capture and repin rather than a fixture that quietly measures
// another release.
const compactionCaptureVersion = "2.1.259"

// compactionPinnedShapes IS THE MEASUREMENT THIS TICKET COMMITS: the envelope each
// compaction line arrived on, spelled "type" when the line carried no subtype and
// "type/subtype" when it did. That distinction is the point — Parser.emitSystemSubtype
// enumerates five `system` subtypes and sends everything else to emitUnrecognized,
// so whether a compaction line is a new arm in that switch or a new top-level type
// decides the whole shape of #2227's and #2228's mapping.
//
// IT IS EMPTY ON PURPOSE UNTIL THE LIVE GATE HAS RUN, and it is what sequences this
// family. The fixture cannot exist before `make e2e-realclaude` produces it, which
// happens after verification, so a reader that asserted against bytes today would
// redden `make check` for every unrelated ticket. compactionReaderGate turns that
// into a state machine with exactly one legal skip: filling this slice is the
// commit that lands the fixture, and a fixture landing WITHOUT it fatals rather
// than passing quietly. AC 4's end state is reached by construction, not by
// remembering.
var compactionPinnedShapes = []string{}

// The four states of (fixture, pin). Only the first is a skip, and only on the leg
// before the live gate has ever run.
const (
	compactionGateSkip  = "skip"
	compactionGateRun   = "run"
	compactionGateFatal = "fatal"
)

// compactionReaderGate is pure so all four quadrants are proved on every run,
// including the leg where the fixture is still absent and the reader itself cannot
// assert anything.
func compactionReaderGate(fixtureExists, pinFilled bool) (action, reason string) {
	switch {
	case !fixtureExists && !pinFilled:
		return compactionGateSkip, "the capture has not been taken yet: `make e2e-realclaude` on an " +
			"authenticated machine runs TestRealClaude_CompactionCapture, which arms on this fixture's " +
			"absence and writes it in-repo. This is the ONLY state in which this reader may skip, and it " +
			"ends the moment the bytes and the pin land together"
	case !fixtureExists && pinFilled:
		return compactionGateFatal, "compactionPinnedShapes names the shapes a capture observed but " +
			compactionCapturePath + " is gone. Restore the fixture, or if the capture was deliberately " +
			"dropped, empty the pin in the same commit — a pin with no bytes behind it is a measurement " +
			"nothing supports"
	case fixtureExists && !pinFilled:
		return compactionGateFatal, "the capture at " + compactionCapturePath + " has landed but " +
			"compactionPinnedShapes is still empty, so nothing pins what it measured. Read the record's " +
			"compaction_shapes and write them here IN THE COMMIT THAT ADDS THE FIXTURE. Until then the " +
			"bytes are committed and unpinned, which is the state this gate exists to make impossible"
	default:
		return compactionGateRun, ""
	}
}

// compactionCapture is the slice of the record this reader needs. payload is a
// JSON STRING holding the whole line (payload_encoding says so per frame), not a
// nested object, so the bytes can be re-decoded as claude sent them.
type compactionCapture struct {
	IsCapture     bool   `json:"is_capture"`
	ClaudeVersion string `json:"claude_version"`
	Frames        []struct {
		Index           int      `json:"index"`
		Type            string   `json:"type"`
		Subtype         string   `json:"subtype"`
		Compaction      bool     `json:"compaction"`
		Markers         []string `json:"markers"`
		PayloadEncoding string   `json:"payload_encoding"`
		Payload         string   `json:"payload"`
	} `json:"frames"`
}

// compactionShape spells one line's envelope the way the pin does. Derived here
// rather than imported from the probe: that package is behind a build tag this one
// does not carry, and re-deriving is also what makes the comparison a measurement
// rather than an agreement by construction.
func compactionShape(typ, subtype string) string {
	if subtype == "" {
		return typ
	}
	return typ + "/" + subtype
}

// TestRealClaudeCompactionCaptureShapesArePinned reads the committed capture and
// pins, for every compaction line in it, the top-level type it arrived as and the
// subtype it carried or the absence of one — AC 2.
//
// Every failure once the gate says run is t.Fatalf, never a skip: the capture is
// committed, so a missing or empty record is a broken premise rather than an
// unavailable resource. Zero compaction frames fatals here, one level below any
// future test that loops over them, so none of them can pass vacuously.
func TestRealClaudeCompactionCaptureShapesArePinned(t *testing.T) {
	raw, readErr := os.ReadFile(compactionCapturePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", compactionCapturePath, readErr)
	}
	switch action, reason := compactionReaderGate(exists, len(compactionPinnedShapes) > 0); action {
	case compactionGateSkip:
		t.Skipf("#2229: %s", reason)
	case compactionGateFatal:
		t.Fatalf("#2229: %s", reason)
	}

	var capture compactionCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", compactionCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, never a "+
			"hand-written payload; a guessed line carries whatever shape its author expected",
			compactionCapturePath)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on
	// the leading token; an "<unavailable: ...>" version fails it too, which is
	// correct — a capture that could not read the version it was taken at cannot
	// vouch for the release its filename claims.
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != compactionCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it. Re-capture at the "+
			"pinned version, or bump ccapFixtureVersion, compactionCaptureVersion and "+
			"compactionPinnedShapes together", compactionCapturePath, capture.ClaudeVersion,
			compactionCaptureVersion)
	}

	seen := map[string]bool{}
	for _, f := range capture.Frames {
		if !f.Compaction {
			continue
		}
		if len(f.Markers) == 0 {
			t.Fatalf("%s: frame %d is flagged compaction with an empty marker list; the record "+
				"contradicts itself and the flag cannot be trusted", compactionCapturePath, f.Index)
		}
		if f.PayloadEncoding != "json-string" {
			t.Fatalf("%s: frame %d payload_encoding = %q, want %q — a compaction line that was not "+
				"valid UTF-8 carries no readable payload, so its envelope cannot be re-derived",
				compactionCapturePath, f.Index, f.PayloadEncoding, "json-string")
		}
		// Re-derived from the line's OWN bytes rather than taken from the record's
		// labels. Those labels were written by the probe, and a reader that trusted
		// them would pin the probe's decoding rather than claude's line.
		var envelope struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal([]byte(f.Payload), &envelope); err != nil {
			t.Fatalf("%s: frame %d payload does not decode as JSON: %v", compactionCapturePath, f.Index, err)
		}
		if envelope.Type != f.Type || envelope.Subtype != f.Subtype {
			t.Fatalf("%s: frame %d is recorded as %q but its payload says %q — the record's envelope "+
				"labels disagree with the bytes they describe", compactionCapturePath, f.Index,
				compactionShape(f.Type, f.Subtype), compactionShape(envelope.Type, envelope.Subtype))
		}
		seen[compactionShape(envelope.Type, envelope.Subtype)] = true
	}
	if len(seen) == 0 {
		t.Fatalf("%s: the capture holds ZERO compaction lines. A record with none is vacuous — every "+
			"assertion built on it would pass without reading a byte claude sent, and #2227 and #2228 "+
			"both read this file", compactionCapturePath)
	}

	got := make([]string, 0, len(seen))
	for s := range seen {
		got = append(got, s)
	}
	sort.Strings(got)
	want := append([]string(nil), compactionPinnedShapes...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s: the compaction lines arrived on %v, but compactionPinnedShapes says %v.\n"+
			"If claude's shape genuinely changed, re-capture at the new version and repin both ends "+
			"together; a pin edited to match a fixture nobody re-read turns this measurement into an "+
			"agreement with itself", compactionCapturePath, got, want)
	}
}

// TestCompactionReaderGateHasExactlyOneLegalSkip proves the four quadrants of the
// sequencing gate, and it runs on every leg including the one where the fixture
// does not exist yet — which is the leg where the reader above can assert nothing
// and this is the only non-vacuous coverage in the file.
//
// The third row is the one the whole design turns on. #1763 spent real tokens on a
// live gate that ran green and landed none of the three artifacts its acceptance
// criteria asked for; here the equivalent miss is bytes that land with nothing
// pinning what they measured, and a gate that passed in that state would hide it
// exactly as well.
func TestCompactionReaderGateHasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		fixtureExists bool
		pinFilled     bool
		want          string
	}{
		{"before the live gate has run: the one legal skip", false, false, compactionGateSkip},
		{"a pin whose bytes are gone", false, true, compactionGateFatal},
		{"bytes committed with nothing pinning them", true, false, compactionGateFatal},
		{"the steady state", true, true, compactionGateRun},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, reason := compactionReaderGate(tc.fixtureExists, tc.pinFilled)
			if action != tc.want {
				t.Errorf("compactionReaderGate(%v, %v) = %q, want %q", tc.fixtureExists, tc.pinFilled,
					action, tc.want)
			}
			if action == compactionGateRun && reason != "" {
				t.Errorf("the run state named a reason %q; nothing reads it", reason)
			}
			if action != compactionGateRun && reason == "" {
				t.Errorf("%s named no reason; the skip or fatal message would say nothing", action)
			}
		})
	}
}

// TestCompactionFixtureReplayReachesBothEdges is #2227's AC 3: the committed
// capture's own bytes, fed back through a real Parser, reach the same two edges the
// hand-authored table in parser_compacting_test.go asserts.
//
// IT SKIPS TODAY, and on the one legal quadrant compactionReaderGate already
// defines rather than on a branch of its own. #2229's capture fired against claude
// 2.1.259 — outcome=fired, shapes=[system/compact_boundary system/status] — but the
// run was the dispatcher's gate-only real-claude lap, which verifies from a detached
// worktree and never runs `git add`, so an in-repo fixture write was discarded with
// the worktree exactly as a tempdir write would have been. The assertion is written
// anyway and arms the moment an operator commits those bytes; nothing about it has
// to be remembered later, which is the whole value of putting it here now.
//
// WHAT IT ADDS OVER ITS SIBLING ABOVE is the mapping rather than the envelope. That
// test pins the type/subtype each compaction line ARRIVED on; this one pins what the
// shipped parser DOES with them, which is the claim a hand-authored line cannot
// make on its own — the literals in parser_compacting_test.go encode this author's
// reading of the capture, and only claude's own bytes can falsify it.
func TestCompactionFixtureReplayReachesBothEdges(t *testing.T) {
	raw, readErr := os.ReadFile(compactionCapturePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", compactionCapturePath, readErr)
	}
	switch action, reason := compactionReaderGate(exists, len(compactionPinnedShapes) > 0); action {
	case compactionGateSkip:
		t.Skipf("#2227: %s", reason)
	case compactionGateFatal:
		t.Fatalf("#2227: %s", reason)
	}

	var capture compactionCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", compactionCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — replaying a hand-written payload would prove only "+
			"that the mapping agrees with its author", compactionCapturePath)
	}

	// EVERY frame in stream order, not the compaction-flagged ones. The record's
	// flag is the probe's content classifier, and feeding the parser only what that
	// classifier picked would let a line it missed — the one most likely to matter —
	// go unreplayed. One parser across the whole turn, because the edge pair is
	// cross-line state.
	var got []string
	p := NewParser(func(ev turnevent.Event) { got = append(got, compactingTrace(ev)) },
		discardLogger())
	replayed := 0
	for _, f := range capture.Frames {
		if f.PayloadEncoding != "json-string" || f.Payload == "" {
			continue
		}
		replayed++
		if _, err := p.Write([]byte(f.Payload + "\n")); err != nil {
			t.Fatalf("%s: replaying frame %d: %v", compactionCapturePath, f.Index, err)
		}
	}
	if replayed == 0 {
		t.Fatalf("%s: replayed zero frames out of %d; a capture whose payloads cannot be fed back "+
			"proves nothing", compactionCapturePath, len(capture.Frames))
	}

	var rising, falling, unrecognized int
	for _, tr := range got {
		switch tr {
		case "compacting:true":
			rising++
		case "compacting:false":
			falling++
		case "unrecognized":
			unrecognized++
		}
	}
	if rising != 1 || falling != 1 {
		t.Fatalf("%s: replaying %d frame(s) produced %d compacting:true and %d compacting:false, "+
			"want exactly 1 of each. claude's own bytes disagree with the hand-authored literals "+
			"in parser_compacting_test.go, and the bytes win — read the capture's frames and fix "+
			"the mapping, not this assertion.\n  trace: %v",
			compactionCapturePath, replayed, rising, falling, got)
	}
	if unrecognized != 0 {
		t.Fatalf("%s: replaying the captured turn produced %d unrecognized_message frame(s), want "+
			"0 — some line of a real compacting turn reaches a client as a noise row",
			compactionCapturePath, unrecognized)
	}
}
