package streamsup

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// informationalCapturePath is the committed real-claude capture of one live turn
// whose UserPromptSubmit hook REFUSED the prompt (#2255), produced by
// internal/e2e/realclaude's TestRealClaude_OperatorSystemLinesCapture.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// compactionCapturePath gives: the bytes are only BYTES, the e2e_realclaude build
// tag belongs to that package's Go files rather than to its testdata, and reading it
// from here is what keeps the measurement inside `make check` instead of behind an
// opt-in gate that SKIPS (exit 0) with no claude login.
//
// A FURTHER READER, NOT A GENERALISATION OF ANY BEFORE IT — and no ordinal is
// claimed, because a count of the readers in this package is the kind of number that
// is stale within a ticket of being written. capturedLines' docblock forbids by name
// growing it a path parameter, since its is_capture assertion is what stops a
// hand-built payload file being swapped in behind the provenance checks; every reader
// since has restated that discipline rather than importing it, and so does this one.
// Its own path and version constants, no path parameter, every provenance check
// written out below. None of them can decode another's record shape, and reaching for
// the wrong one yields zero values rather than a failure.
//
// capturedStrings IS shared with the compaction reader, and that is not an exception
// to the paragraph above. What the rule protects is the RECORD DECODER and its
// provenance checks — the things that decide whether a file may be believed. That
// helper walks a payload string it is handed and decides nothing.
const informationalCapturePath = "../e2e/realclaude/testdata/operator_system_lines_v" +
	informationalCaptureVersion + ".json"

// informationalCaptureVersion is the claude release the capture was taken at,
// spliced into the path above rather than repeated so the filename cannot drift from
// the version this reader enforces. The producing side refuses to write under a
// mismatched name, so a claude upgrade is a loud instruction to re-capture and repin
// rather than a fixture that quietly measures another release.
const informationalCaptureVersion = "2.1.259"

// informationalSubtype is the quarry. Spelled as a literal rather than read off the
// parser's tables: this file measures what claude SENDS, and a comparison keyed on a
// production constant would agree with the matcher by construction.
const informationalSubtype = "informational"

// informationalPinnedKeys IS THE MEASUREMENT THIS TICKET COMMITS: the top-level key
// set claude's system/informational line arrived with, sorted. That set is what
// systemInformationalLine may declare from and nothing else — the family's rule,
// stated in systemTaskStartedLine's doc, is that the field set is exactly what the
// committed capture shows and nothing invented from a docs page.
//
// It is FILLED rather than empty, and this reader FATALS rather than skips on a
// missing fixture, which is where it parts company with its two nearest siblings. The
// sequencing gate they carry (compactionReaderGate) exists for a fixture that could
// not exist yet, because `make e2e-realclaude` had not run; here the bytes were
// committed by #2255 before this ticket opened. A skip quadrant would be a state this
// file can never legally be in, and one that would let the whole measurement pass
// vacuously if the fixture were ever deleted.
//
// Three of the seven are the envelope and the daemon's own mapped set; the other two,
// session_id and uuid, are the ones systemInformationalLine deliberately does not
// declare. A claude release that adds a key reddens this pin, which is the point:
// someone then decides whether it is payload or noise, rather than the daemon
// silently mapping the same three fields out of a wider line.
var informationalPinnedKeys = []string{
	"content", "level", "prevent_continuation", "session_id", "subtype", "type", "uuid",
}

// informationalUnmappedKeys are the two keys the captured line carries that the
// daemon must not read into any state — AC 2's second half. Named here so the leak
// sweep below can state what it is looking for, and read out of the LINE at run time
// rather than compared against values retyped into this file.
var informationalUnmappedKeys = []string{"session_id", "uuid"}

// informationalCapture is the slice of the record this reader needs. payload is a
// JSON STRING holding the whole line (payload_encoding says so per frame), not a
// nested object, so the bytes can be re-decoded as claude sent them.
type informationalCapture struct {
	IsCapture     bool   `json:"is_capture"`
	ClaudeVersion string `json:"claude_version"`
	Frames        []struct {
		Index           int    `json:"index"`
		Type            string `json:"type"`
		Subtype         string `json:"subtype"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"frames"`
}

// readInformationalCapture loads the committed record and runs every provenance
// check. Each failure is a Fatalf and never a skip: the capture is committed, so a
// missing, unreadable or unvouched record is a broken premise rather than an
// unavailable resource.
func readInformationalCapture(t *testing.T) informationalCapture {
	t.Helper()
	raw, err := os.ReadFile(informationalCapturePath)
	if err != nil {
		t.Fatalf("reading capture %s: %v — the fixture is committed, so its absence is a broken "+
			"premise. Restore it rather than weakening this reader; #2319's whole measurement is "+
			"that claude's own bytes reach the mapping inside `make check`", informationalCapturePath, err)
	}
	var capture informationalCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", informationalCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, never a "+
			"hand-written payload; a guessed line carries whatever shape its author expected",
			informationalCapturePath)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on the
	// leading token; an "<unavailable: ...>" version fails it too, which is correct — a
	// capture that could not read the version it was taken at cannot vouch for the
	// release its filename claims.
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != informationalCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it. Re-capture at "+
			"the pinned version, or bump the producing side's fixture version, "+
			"informationalCaptureVersion and informationalPinnedKeys together",
			informationalCapturePath, capture.ClaudeVersion, informationalCaptureVersion)
	}
	return capture
}

// informationalPayload returns the one captured system/informational line's bytes,
// selected by re-deriving each frame's envelope from its OWN payload rather than
// trusting the record's labels. Those labels were written by the probe, and a reader
// keyed on them would pin the probe's decoding rather than claude's line.
func informationalPayload(t *testing.T, capture informationalCapture) string {
	t.Helper()
	var found []string
	for _, f := range capture.Frames {
		if f.PayloadEncoding != "json-string" || f.Payload == "" {
			continue
		}
		var envelope struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal([]byte(f.Payload), &envelope); err != nil {
			t.Fatalf("%s: frame %d payload does not decode as JSON: %v",
				informationalCapturePath, f.Index, err)
		}
		if envelope.Type != f.Type || envelope.Subtype != f.Subtype {
			t.Fatalf("%s: frame %d is recorded as %q/%q but its payload says %q/%q — the record's "+
				"envelope labels disagree with the bytes they describe", informationalCapturePath,
				f.Index, f.Type, f.Subtype, envelope.Type, envelope.Subtype)
		}
		if envelope.Type == "system" && envelope.Subtype == informationalSubtype {
			found = append(found, f.Payload)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: the capture holds %d system/%s line(s), want exactly 1. A record with none "+
			"is vacuous — every assertion built on it would pass without reading a byte claude "+
			"sent — and more than one means this reader is choosing which to measure",
			informationalCapturePath, len(found), informationalSubtype)
	}
	return found[0]
}

// TestRealClaudeInformationalCaptureKeysArePinned pins the top-level key set the
// captured line arrived with, re-derived from its own bytes.
//
// It is what makes systemInformationalLine's "exactly what the capture shows"
// claim checkable rather than a sentence. A claude release that adds, renames or
// drops a key reddens here, one level below the replay below, so the mapping is
// re-decided by a person instead of quietly continuing to read three fields out of a
// line that changed shape.
func TestRealClaudeInformationalCaptureKeysArePinned(t *testing.T) {
	t.Parallel()

	payload := informationalPayload(t, readInformationalCapture(t))
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &keyed); err != nil {
		t.Fatalf("%s: the captured line does not decode as an object: %v", informationalCapturePath, err)
	}
	got := make([]string, 0, len(keyed))
	for k := range keyed {
		got = append(got, k)
	}
	sort.Strings(got)
	want := append([]string(nil), informationalPinnedKeys...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s: the informational line carries keys %v, but informationalPinnedKeys says %v.\n"+
			"If claude's shape genuinely changed, re-capture at the new version and repin both ends "+
			"together, then decide whether the difference is payload — a pin edited to match a "+
			"fixture nobody re-read turns this measurement into an agreement with itself",
			informationalCapturePath, got, want)
	}
}

// TestInformationalFixtureReplayPublishesTheBanner is AC 1: the committed capture's
// own bytes, fed back through a real Parser, produce one banner carrying the level,
// text and stops-turn flag that line actually states.
//
// WHAT IT ADDS OVER THE HAND-BUILT TABLE in parser_informational_banner_test.go is
// that the literals there encode this author's reading of the capture, and only
// claude's own bytes can falsify it. The record has the frame at events_emitted: 0 —
// the pre-mapping observation, taken when nothing in the daemon had an arm for this
// subtype — and this test is what changes that number.
//
// THE EXPECTED VALUES ARE RE-DERIVED from the captured line rather than retyped, so a
// re-capture at a new claude release moves the fixture and the expectation together;
// a comparison against constants this file also fed the parser would be an agreement
// with itself.
func TestInformationalFixtureReplayPublishesTheBanner(t *testing.T) {
	t.Parallel()

	capture := readInformationalCapture(t)
	payload := informationalPayload(t, capture)

	// EVERY frame in stream order, not the informational one alone. Feeding the parser
	// only the line this test is about would let a mapping that also fired on some
	// other frame go unnoticed, and the count below is what catches it.
	var banners []turnevent.Banner
	p := NewParser(func(ev turnevent.Event) {
		if b, ok := ev.(turnevent.Banner); ok {
			banners = append(banners, b)
		}
	}, discardLogger())
	replayed := 0
	for _, f := range capture.Frames {
		if f.PayloadEncoding != "json-string" || f.Payload == "" {
			continue
		}
		replayed++
		if _, err := p.Write([]byte(f.Payload + "\n")); err != nil {
			t.Fatalf("%s: replaying frame %d: %v", informationalCapturePath, f.Index, err)
		}
	}
	if replayed == 0 {
		t.Fatalf("%s: replayed zero frames out of %d; a capture whose payloads cannot be fed back "+
			"proves nothing", informationalCapturePath, len(capture.Frames))
	}
	if len(banners) != 1 {
		t.Fatalf("%s: replaying %d frame(s) produced %d banner(s), want exactly 1. The capture's "+
			"census is one system/%s line, so zero means claude's own bytes disagree with the "+
			"hand-authored literals in parser_informational_banner_test.go — and the bytes win",
			informationalCapturePath, replayed, len(banners), informationalSubtype)
	}

	var observed struct {
		Content             string `json:"content"`
		Level               string `json:"level"`
		PreventContinuation bool   `json:"prevent_continuation"`
	}
	if err := json.Unmarshal([]byte(payload), &observed); err != nil {
		t.Fatalf("%s: the captured informational line does not decode: %v", informationalCapturePath, err)
	}
	// Non-vacuity for the three comparisons below, asserted BEFORE them: a line whose
	// content and level were empty and whose flag was false would satisfy all three
	// while proving nothing about the mapping. The content check also states the
	// premise of the untruncated expectation — a captured line over the cap would
	// legitimately arrive cut, and this reader would then be comparing the wrong thing.
	if observed.Content == "" || observed.Level == "" || !observed.PreventContinuation {
		t.Fatalf("%s: the captured line states content=%q level=%q prevent_continuation=%v; a line "+
			"missing any of the three makes the comparisons below vacuous, so re-capture rather "+
			"than weakening them", informationalCapturePath, observed.Content, observed.Level,
			observed.PreventContinuation)
	}
	if len(observed.Content) > maxBannerText {
		t.Fatalf("%s: the captured content is %d bytes, past maxBannerText (%d). The mapping is "+
			"correct to cut it, but this test's untruncated expectation is not — split the "+
			"comparison rather than raising the cap to match a fixture",
			informationalCapturePath, len(observed.Content), maxBannerText)
	}

	got := banners[0]
	want := turnevent.Banner{
		Level:     observed.Level,
		Text:      observed.Content,
		StopsTurn: observed.PreventContinuation,
	}
	if got != want {
		t.Errorf("%s: banner = %+v, want %+v — the values claude actually stated",
			informationalCapturePath, got, want)
	}

	// AC 2's second half, proved against the REAL values rather than a sentinel: the two
	// keys the line carries and the daemon does not map must reach nothing. The sweep is
	// general rather than a two-field comparison, so a key a later claude adds is caught
	// by the same loop — the allowlist is what legitimately crosses (content, level) plus
	// claude's own envelope vocabulary, which names the line rather than the operator.
	rendered, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the mapped event: %v", err)
	}
	allowed := map[string]bool{
		observed.Content: true, observed.Level: true, "system": true, informationalSubtype: true,
	}
	swept := 0
	for _, s := range capturedStrings(t, payload) {
		if s == "" || allowed[s] {
			continue
		}
		swept++
		if strings.Contains(string(rendered), s) {
			t.Errorf("%s: the mapped banner carries %q, which is on claude's line but not on the "+
				"allowlist. claude's session identity is not the daemon's conversation identity and "+
				"nothing in the daemon reads the line's uuid, so neither may be declared on "+
				"systemInformationalLine", informationalCapturePath, s)
		}
	}
	// Non-vacuity for the sweep, and specifically for the two keys AC 2 names: a line
	// carrying only its published values would make the loop above compare nothing.
	if swept < len(informationalUnmappedKeys) {
		t.Fatalf("%s: the leak sweep compared %d string(s), fewer than the %d unmapped keys %v the "+
			"captured line is recorded as carrying. Either the fixture changed shape or the "+
			"allowlist swallowed the very values this check exists to look for",
			informationalCapturePath, swept, len(informationalUnmappedKeys), informationalUnmappedKeys)
	}
}
