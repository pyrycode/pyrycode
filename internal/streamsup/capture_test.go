package streamsup

import (
	"encoding/json"
	"os"
	"testing"
)

// capturePath is the committed real-claude capture the system-subtype mappings
// in this package are built from and proven against (#1260): claude 2.1.220,
// is_capture true, outcome fired, absence_claim_valid true, independently
// reproduced by an operator on 2026-08-06 against a tree 127 commits later with
// every structural count matching.
//
// It lives under internal/e2e/realclaude/testdata/, but it is only BYTES — the
// e2e_realclaude build tag belongs to that package's Go files, not to its
// testdata. Reading it from here is what keeps the mapping proof inside
// `make check` rather than behind an opt-in gate that SKIPS (exit 0) with no
// claude login, where a green suite would be no evidence at all.
const capturePath = "../e2e/realclaude/testdata/dropped_lines_v2.1.220.json"

// droppedLineCapture is the slice of the capture's shape these tests read.
// dropped_lines is an array of records; payload is a JSON STRING holding the
// whole line (payload_encoding says so per record), not a nested object, so the
// payload's bytes go to the parser as-is.
type droppedLineCapture struct {
	IsCapture    bool `json:"is_capture"`
	DroppedLines []struct {
		Type            string `json:"type"`
		Subtype         string `json:"subtype"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"dropped_lines"`
}

// capturedSystemLines returns EVERY captured system line of the given subtype,
// in stream order, as the bytes claude put on the wire.
//
// This is the package's one capture reader; capturedSystemLine wraps it with the
// exactly-one rule. The provenance checks live HERE, at the reader, rather than
// at the callers — the is_capture assertion means a hand-built payload file
// swapped in for this one fails before any mapping is read, and putting it in one
// place is what stops a second reader from growing a second, weaker copy of it.
// For the same reason the path is the capturePath package constant and this
// function takes NO path parameter: a plural reader is exactly the shape someone
// later generalizes into "read any capture file", and that generalization is what
// would put an unchecked file behind these assertions.
//
// Every failure is t.Fatalf, never a skip: the capture is committed, so a missing
// record is a broken premise rather than an unavailable resource. Zero matches
// fatals here, so no caller can loop over an empty slice and pass vacuously.
//
// Stream order is the capture's own record order, which #1385's rate-bound tests
// depend on: they drive the 33 thinking_tokens lines through ONE parser and the
// accumulator makes the result order-dependent.
func capturedSystemLines(t *testing.T, subtype string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("reading capture %s: %v", capturePath, err)
	}
	var capture droppedLineCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", capturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, "+
			"never a hand-written payload; a guessed line maps fields claude does not send", capturePath)
	}
	var found [][]byte
	for _, rec := range capture.DroppedLines {
		if rec.Type != "system" || rec.Subtype != subtype {
			continue
		}
		if rec.PayloadEncoding != "json-string" {
			t.Fatalf("system/%s: payload_encoding = %q, want %q (the payload must be the whole line as a JSON string)",
				subtype, rec.PayloadEncoding, "json-string")
		}
		found = append(found, []byte(rec.Payload))
	}
	if len(found) == 0 {
		t.Fatalf("system/%s: got 0 captured records in %s, want at least 1", subtype, capturePath)
	}
	return found
}

// capturedSystemLine returns the one captured system line of the given subtype,
// as the bytes claude put on the wire. Exactly one match is required because two
// would make "the captured line" ambiguous, and silently taking the first is the
// kind of choice that should be deliberate.
//
// Shared on purpose, and by three mapping tests: task_started (#1380),
// task_updated (#1382) and background_tasks_changed (#1381) all read their
// subtype out of this one file. CORRECTED 2026-08-08 (#1381): every statement
// this doc has carried about a caller still driving its line through the drop
// table is now spent — no captured system subtype remains on
// TestParser_IgnoredLineTypesStaySilent's table, which since #1381 drives only
// synthesized lines.
//
// The predicted fourth subtype arrived (#1385, thinking_tokens) and did NOT
// become a fourth caller of this function: it has 33 captured records, so the
// exactly-one rule fatals on it by design. It reads capturedSystemLines instead —
// which is why the singular reader is now a thin wrapper rather than the place
// the provenance checks live.
func capturedSystemLine(t *testing.T, subtype string) []byte {
	t.Helper()
	found := capturedSystemLines(t, subtype)
	if len(found) != 1 {
		t.Fatalf("system/%s: got %d captured records in %s, want exactly 1", subtype, len(found), capturePath)
	}
	return found[0]
}
