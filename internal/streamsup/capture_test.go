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

// capturedSystemLine returns the one captured system line of the given subtype,
// as the bytes claude put on the wire.
//
// Every failure is t.Fatalf, never a skip: the capture is committed, so a
// missing record is a broken premise rather than an unavailable resource. The
// is_capture assertion is the provenance rule enforced AT THE READER — a
// hand-built payload file swapped in for this one fails here, before any mapping
// is read. Exactly one match is required because two would make "the captured
// line" ambiguous, and silently taking the first is the kind of choice that
// should be deliberate.
//
// Shared on purpose: #1381 (background_tasks_changed) and #1382 (task_updated)
// map the sibling subtypes out of the same file. CORRECTED 2026-08-07 (#1382):
// this used to add "and this package already drives both through the drop
// table", which stopped being true the moment task_updated's row moved out of
// TestParser_IgnoredLineTypesStaySilent and into its mapping test.
// background_tasks_changed is the one still driven through the drop table.
func capturedSystemLine(t *testing.T, subtype string) []byte {
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
	var found []string
	for _, rec := range capture.DroppedLines {
		if rec.Type != "system" || rec.Subtype != subtype {
			continue
		}
		if rec.PayloadEncoding != "json-string" {
			t.Fatalf("system/%s: payload_encoding = %q, want %q (the payload must be the whole line as a JSON string)",
				subtype, rec.PayloadEncoding, "json-string")
		}
		found = append(found, rec.Payload)
	}
	if len(found) != 1 {
		t.Fatalf("system/%s: got %d captured records in %s, want exactly 1", subtype, len(found), capturePath)
	}
	return []byte(found[0])
}
