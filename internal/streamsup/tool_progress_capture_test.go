package streamsup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// toolProgressCapturePath is the committed real-claude capture the tool_progress
// arm is proven against (#2089), produced by
// internal/e2e/realclaude's TestRealClaude_ToolProgressCapture: one live turn
// holding a FOREGROUND Bash call open long enough for claude to emit progress
// heartbeats, recorded upstream of the parser.
//
// It cannot ride the existing fixture. capturePath is pinned to
// dropped_lines_v2.1.220.json, whose 39 records contain no tool_progress line at
// all (verified 2026-09-06) — that capture backgrounded its Bash call and capped
// it below the heartbeat interval, so the frames were never produced.
//
// Like that file it lives under internal/e2e/realclaude/testdata/, and for the
// same reason: it is only BYTES, and the e2e_realclaude build tag belongs to that
// package's Go files rather than to its testdata. Reading it from here is what
// keeps the proof inside `make check` instead of behind an opt-in gate that SKIPS
// (exit 0) with no claude login, where a green suite would be no evidence at all.
const toolProgressCapturePath = "../e2e/realclaude/testdata/tool_progress_v" +
	toolProgressCaptureVersion + ".json"

// toolProgressCaptureVersion is the claude release the committed capture was
// taken from, and it is spliced into the path above rather than repeated, so the
// filename cannot drift from the version this reader enforces.
//
// The record carries the observed `claude --version` and the check below compares
// the two. Without it the version lives in the FILENAME ALONE — a string nothing
// reads — and a capture taken at a different claude could be committed under this
// name, silently turning the marker census into a measurement of some other
// release. The producing side refuses the same mismatch (tpcapRecord's
// fixtureWorthy), so a version bump is a loud instruction to re-capture and repin
// rather than a fixture that quietly says the wrong thing.
const toolProgressCaptureVersion = "2.1.259"

// toolProgressCapture is the slice of the record's shape this reader needs.
// payload is a JSON STRING holding the whole line (payload_encoding says so per
// frame), not a nested object, so the bytes go to the parser as-is.
type toolProgressCapture struct {
	IsCapture     bool   `json:"is_capture"`
	ClaudeVersion string `json:"claude_version"`
	Frames        []struct {
		Type            string `json:"type"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"frames"`
}

// capturedToolProgressLines returns every captured tool_progress line, in stream
// order, as the bytes claude put on the wire.
//
// THIS IS A SECOND READER, NOT A GENERALISATION OF capturedLines, AND THAT IS
// DELIBERATE. That function's docblock forbids by name growing it into a
// path-taking reader, because its is_capture assertion is what stops a
// hand-built payload file being swapped in behind the provenance checks, and its
// one file is a package constant precisely so no caller can choose the path.
// Widening it to a second file would have handed it the parameter that argument
// exists to refuse.
//
// So this takes #1810's shape instead — the exemption capturedLines' doc already
// names — and the discipline #1810 restated rather than imported is restated
// again here: the path is a package constant of this file's own, the reader takes
// NO path parameter, and every provenance check is written out below rather than
// borrowed. Nothing here is shared with either sibling reader, and nothing should
// be: none of the three can decode another's record shape, and reaching for the
// wrong one yields zero values rather than a failure.
//
// EVERY FAILURE HERE IS t.Fatalf, NEVER A SKIP, and the sibling readers' sentence
// now holds of this one too: the capture is committed, so a missing record is a
// broken premise rather than an unavailable resource. An earlier revision carried
// one fs.ErrNotExist skip while the fixture was still unproduced; it was deleted
// in the commit that landed the fixture, which is what its own comment said to do.
//
// Zero frames fatals here, so no caller can loop over an empty slice and pass
// vacuously — AC3's non-vacuity rule held at the reader, one level below every
// test that depends on it.
func capturedToolProgressLines(t *testing.T) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(toolProgressCapturePath)
	if err != nil {
		t.Fatalf("reading capture %s: %v", toolProgressCapturePath, err)
	}
	var capture toolProgressCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", toolProgressCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, "+
			"never a hand-written payload; a guessed line matches markers claude does not send",
			toolProgressCapturePath)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on
	// the leading token; an "<unavailable: ...>" version fails it too, which is
	// correct — a capture that could not read the version it was taken at cannot
	// vouch for the release its filename claims.
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != toolProgressCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this "+
			"capture measures, so a record from a different claude must not be read under it. "+
			"Re-capture at the pinned version, or bump toolProgressCaptureVersion and the census "+
			"in consumeToolProgress's docblock together",
			toolProgressCapturePath, capture.ClaudeVersion, toolProgressCaptureVersion)
	}
	var found [][]byte
	for i, frame := range capture.Frames {
		if frame.Type != "tool_progress" {
			t.Fatalf("%s: frame %d has type %q, want %q — this record holds ONE type by "+
				"construction, so a foreign type means the probe's filter changed",
				toolProgressCapturePath, i, frame.Type, "tool_progress")
		}
		if frame.PayloadEncoding != "json-string" {
			t.Fatalf("%s: frame %d payload_encoding = %q, want %q (the payload must be the whole "+
				"line as a JSON string)", toolProgressCapturePath, i, frame.PayloadEncoding, "json-string")
		}
		found = append(found, []byte(frame.Payload))
	}
	if len(found) == 0 {
		t.Fatalf("%s: got 0 captured tool_progress frames, want at least 1. A capture recording "+
			"none is vacuous: every assertion built on it would pass without reading a byte claude sent",
			toolProgressCapturePath)
	}
	return found
}
