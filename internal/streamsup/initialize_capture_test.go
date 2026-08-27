package streamsup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// #1810 — the committed `initialize` control-response captures, readable from
// this package.
//
// The request half of this exchange is written HERE, by marshalInitializeEnvelope
// and WriteInitialize (#1689). The reply half was captured live against claude
// 2.1.239 — once unarmed (#1688) and once per arm of #1763's three-arm
// measurement — and committed under internal/e2e/realclaude/testdata/. Those
// files are only BYTES: the e2e_realclaude build tag belongs to that package's Go
// FILES, not to its testdata, which is the same argument capturePath's doc makes
// and the reason a decode proven against them can run inside `make check` instead
// of behind a gate that exits 0 with zero tests executed when there is no claude
// login.
//
// This file is deliberately NOT an append to capture_test.go. The two record
// shapes share nothing but the directory they live in: that one is a record of
// stdout LINES whose payload is a JSON string holding a whole line, this one is a
// record of a control EXCHANGE whose payload is a nested object. Keeping them in
// separate files is what stops a later reader reaching for droppedLineCapture's
// helpers on a control record — they would decode to zero values rather than
// fail.
//
// #1811, #1812, #1719 and #1809 are the decodes that ride this reader. It exists
// as its own slice so the provenance discipline below is written once instead of
// four times.

// initCaptureDir is the committed captures' directory, relative to this package.
// Reading across the module like this is capture_test.go's idiom, confirmed to
// resolve from here today.
const initCaptureDir = "../e2e/realclaude/testdata"

// initCaptureVersion is the ONE claude version these captures record, and it is
// hard-coded as a deliberate tripwire rather than discovered by glob. The decodes
// built on these bytes were proven against 2.1.239 and against nothing else, so a
// re-capture at a later version must break this package loudly — at the
// directory-coverage assertion in TestInitCaptureArms_CoverTheCaptureDirectory
// and at the name-versus-content binding in capturedInitialize — rather than
// silently re-point a proof at a models list nobody has looked at.
const initCaptureVersion = "2.1.239"

// The four committed arms. The empty one is #1688's unarmed base capture, which
// predates #1763's arm harness and carries no `arm` key at all; the other three
// are #1763's arm identifiers, spelled as its own initControlArms table spells
// them. initCaptureArmNoRequest is the control arm that sent NO initialize
// request, so its record is the committed fixture for the absent-payload case.
const (
	initCaptureArmBase               = ""
	initCaptureArmBeforeFirstTurn    = "before_first_turn"
	initCaptureArmAfterCompletedTurn = "after_completed_turn"
	initCaptureArmNoRequest          = "control_no_request"
)

// initCaptureArms is the closed set every reader here selects from, base first.
// A closed literal rather than a glob discovery: that package globs because it
// cannot know a live claude's version ahead of the run and needs something to
// reject an undeclared arm with, and neither applies to a package that only ever
// reads four committed files.
var initCaptureArms = []string{
	initCaptureArmBase,
	initCaptureArmBeforeFirstTurn,
	initCaptureArmAfterCompletedTurn,
	initCaptureArmNoRequest,
}

// initCaptureGlob matches every committed capture of this family, at any version.
// It is the coverage test's input and is deliberately version-blind, so a
// re-capture shows up there as an unexpected name rather than as no match at all.
const initCaptureGlob = "initialize_control_v*.json"

// initCaptureName mints the base name of one capture: the unarmed shape when arm
// is empty, the arm-carrying shape otherwise. The two formats are
// initControlFixtureName's and initControlArmFixtureName's.
//
// versionSlug is NOT ported. The producer slugs both tokens before formatting and
// every token committed today is a fixed point of that slug, so a plain format is
// byte-identical to what it wrote. A second copy of a slug is a second thing to
// drift; a token that is not slug-clean instead reddens the name-versus-content
// binding in capturedInitialize with both strings named, and teaching this namer
// is then the fix.
func initCaptureName(version, arm string) string {
	if arm == "" {
		return fmt.Sprintf("initialize_control_v%s.json", version)
	}
	return fmt.Sprintf("initialize_control_v%s_%s.json", version, arm)
}

// initCapturePath joins one arm's minted name under the capture directory. The
// reader mints its own path from package constants; no caller supplies one.
func initCapturePath(arm string) string {
	return filepath.Join(initCaptureDir, initCaptureName(initCaptureVersion, arm))
}

// initCaptureRecord is the slice of the capture's top-level shape this reader and
// its callers use. The tags are copied verbatim from initControlFixtureRecord,
// which is the AUTHORITATIVE field set for this record and sits behind
// e2e_realclaude, so it can be read but not imported.
//
// This is therefore a SECOND shape, and that type's own doc names the risk
// exactly: a parallel struct is how a field rename lands as a silent zero value.
// The risk is accepted with the surface minimised to eight fields — every one of
// them in the twenty-two-key subset all four records share, so none of them is a
// field #1688's base capture is simply missing.
//
// Deliberately NOT restated: control_request_sent, which is a raw `null` on the
// no-request arm rather than a zero length and whose question control_request_id
// already answers as a plain string; models_entry_fields, stdout_events,
// turn_boundaries, redaction, credential_scan_applied; and every send_point /
// after_send_point field, which the base capture does not carry at all.
type initCaptureRecord struct {
	ClaudeVersion string `json:"claude_version"`
	Arm           string `json:"arm"`

	ControlRequestID                string            `json:"control_request_id"`
	ControlResponses                []json.RawMessage `json:"control_responses"`
	ControlResponseSubtype          string            `json:"control_response_subtype"`
	ControlResponseRequestIDMatched bool              `json:"control_response_request_id_matched"`

	ModelsPresent bool `json:"models_present"`
	ModelsCount   int  `json:"models_count"`
}

// initCaptureResponse decodes ONE entry of control_responses: the control_response
// line as claude put it on the wire, whose `response` is the outer wrapper
// carrying subtype and request_id, whose own `response` is the initialize payload.
//
// pending_permission_requests and pending_user_dialog_requests are on the wrapper
// in two of the three responding captures and absent in the third, so naming them
// here would commit this package to a key set claude does not always send. Only
// subtype, request_id and response are on all three.
type initCaptureResponse struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
	} `json:"response"`
}

// capturedInitialize reads the committed initialize-control capture for one arm,
// asserts the file's own provenance, and returns its record beside the initialize
// PAYLOAD claude sent — the object nested under the outer response, not that
// wrapper and not the whole record. The payload is nil, with no failure, when the
// record says no response arrived: an arm that sent no request is readable as
// that FACT, which is what makes it the committed fixture for the absent case.
//
// It is json.RawMessage rather than a decoded type because each downstream ticket
// decodes a different slice of the payload — the same reason capturedLines hands
// back bytes.
//
// The provenance checks live HERE, at the reader, exactly as capturedLines' doc
// argues, so a second reader cannot grow a second weaker copy of them.
// capturedInitializePayload wraps this one with the payload-required rule and
// inherits every check rather than repeating it.
//
// This reader DOES take a parameter, which capturedLines' does not, so the
// guarantee that one gets from its package-constant path has to be held some
// other way here: the parameter is an ARM SELECTOR from the closed
// initCaptureArms set, never a path, and the reader mints the path itself from
// package constants. The unknown-arm branch below is that sentence made
// executable — no caller can put an unchecked file behind these assertions.
//
// Whether a response ARRIVED is decided by the captured bytes — a non-empty
// control_responses — and the record's own summary fields are checked AGAINST
// that rather than trusted. The arm identifier is not consulted either: a file
// whose name says "no request" while its body carries a response must fail here
// rather than be believed on its name.
//
// models_count and models_present are carried on the returned record and read by
// NOTHING in this reader. That is load-bearing rather than an omission: a reader
// that checked the payload's model count against the record's summary would make
// TestCapturedInitialize_ModelCountsMatchEachRecordsSummary unable to fail, and
// the fixtures' shape would be pinned by construction instead of by a test.
//
// Every failure is t.Fatalf, never a skip. The captures are committed, so a
// missing or unreadable record is a broken premise rather than an unavailable
// resource — a skip here would report a deleted fixture as a green run.
func capturedInitialize(t *testing.T, arm string) (*initCaptureRecord, json.RawMessage) {
	t.Helper()

	known := false
	for _, candidate := range initCaptureArms {
		if candidate == arm {
			known = true
			break
		}
	}
	if !known {
		t.Fatalf("arm %q is not one of initCaptureArms %q; this reader selects from a closed set "+
			"and mints its own path, so an arm it does not declare is a caller reaching for a file "+
			"these assertions were never proven against", arm, initCaptureArms)
	}

	path := initCapturePath(arm)
	base := filepath.Base(path)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading capture %s: %v", path, err)
	}
	rec := &initCaptureRecord{}
	if err := json.Unmarshal(raw, rec); err != nil {
		t.Fatalf("%s: decoding through initCaptureRecord: %v", base, err)
	}

	// Binds the file's NAME to its CONTENT in one comparison, which is
	// initControlDiscoverArms' idiom: a swapped file, a re-capture at a new claude
	// version, and one arm's bytes committed under another arm's name all land
	// here. For the base capture the arm half of the binding is an empty `arm`,
	// which is itself what distinguishes #1688's unarmed record from an arm record
	// renamed onto it.
	if want := initCaptureName(rec.ClaudeVersion, rec.Arm); base != want {
		t.Fatalf("%s: its record says claude_version %q arm %q, which initCaptureName mints as %q; "+
			"the file's name and its content disagree, so neither can be trusted to say which "+
			"capture these bytes are", base, rec.ClaudeVersion, rec.Arm, want)
	}

	// Accumulate every inconsistency and report them together, rather than failing
	// at the first: one run then says what is wrong with this file instead of
	// needing a fix-and-rerun per row. initControlDiscoverArms is the precedent.
	arrived := len(rec.ControlResponses) > 0
	var problems []string
	if arrived != (rec.ControlResponseSubtype != "") {
		problems = append(problems, fmt.Sprintf("%d control_responses but control_response_subtype %q",
			len(rec.ControlResponses), rec.ControlResponseSubtype))
	}
	if arrived != (rec.ControlRequestID != "") {
		problems = append(problems, fmt.Sprintf("%d control_responses but control_request_id %q",
			len(rec.ControlResponses), rec.ControlRequestID))
	}
	if arrived != rec.ControlResponseRequestIDMatched {
		problems = append(problems, fmt.Sprintf("%d control_responses but control_response_request_id_matched %v",
			len(rec.ControlResponses), rec.ControlResponseRequestIDMatched))
	}

	var entry initCaptureResponse
	switch {
	case !arrived:
	case len(rec.ControlResponses) != 1:
		// Two would make "the initialize payload" ambiguous, and silently taking the
		// first is the kind of choice that should be deliberate — capturedLine's
		// argument, for the same reason.
		problems = append(problems, fmt.Sprintf("%d control_responses, want exactly 1",
			len(rec.ControlResponses)))
	default:
		if err := json.Unmarshal(rec.ControlResponses[0], &entry); err != nil {
			problems = append(problems, fmt.Sprintf("decoding control_responses[0]: %v", err))
			break
		}
		if entry.Type != "control_response" {
			problems = append(problems, fmt.Sprintf("control_responses[0].type %q, want %q",
				entry.Type, "control_response"))
		}
		if entry.Response.Subtype != rec.ControlResponseSubtype {
			problems = append(problems, fmt.Sprintf("control_responses[0].response.subtype %q but "+
				"control_response_subtype %q", entry.Response.Subtype, rec.ControlResponseSubtype))
		}
		if entry.Response.RequestID != rec.ControlRequestID {
			problems = append(problems, fmt.Sprintf("control_responses[0].response.request_id %q but "+
				"control_request_id %q", entry.Response.RequestID, rec.ControlRequestID))
		}
	}
	if len(problems) > 0 {
		t.Fatalf("%s: the record contradicts itself, so no part of it can be trusted to say what "+
			"claude replied:\n  %s", base, strings.Join(problems, "\n  "))
	}

	if !arrived {
		return rec, nil
	}

	// An empty payload would let every downstream decode pass over nothing, so it
	// fails at the read. A non-object — a bare null, an array, a string — fails the
	// unmarshal here for the same reason.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry.Response.Response, &fields); err != nil {
		t.Fatalf("%s: control_responses[0].response.response is not a JSON object: %v", base, err)
	}
	if len(fields) == 0 {
		t.Fatalf("%s: control_responses[0].response.response is an empty object; a decode of nothing "+
			"passes for any expectation", base)
	}
	return rec, entry.Response.Response
}

// capturedInitializePayload is capturedInitialize with the absent case made
// fatal: the reader for callers that need a payload to decode, which is all four
// of #1811, #1812, #1719 and #1809. A caller that wants the absent case calls the
// wide reader and checks for nil.
func capturedInitializePayload(t *testing.T, arm string) json.RawMessage {
	t.Helper()
	rec, payload := capturedInitialize(t, arm)
	if payload == nil {
		t.Fatalf("%s: the record captured no initialize response (%d control_responses), so there is "+
			"no payload to decode; read it through capturedInitialize if the absent case is the subject",
			filepath.Base(initCapturePath(arm)), len(rec.ControlResponses))
	}
	return payload
}

// initCaptureArmLabel names an arm for a subtest. The base capture's arm is the
// empty string, which t.Run would render as an index rather than a name.
func initCaptureArmLabel(arm string) string {
	if arm == "" {
		return "base"
	}
	return arm
}

// TestCapturedInitialize_ModelCountsMatchEachRecordsSummary reads all four
// committed captures through the reader and checks each one's payload against
// that record's OWN summary — the fixtures' shape pinned before #1811, #1812,
// #1719 and #1809 put decodes on top of it, and the reader's first caller.
//
// The three responding arms and the no-request arm are asserted as separate
// cases rather than folded into one count comparison. Against a nil payload
// `0 == 0` holds for a reader that returned nothing at all, so the absent case
// needs its own assertions, and the responding arms need the models_count > 0 row
// as the vacuity guard on theirs.
func TestCapturedInitialize_ModelCountsMatchEachRecordsSummary(t *testing.T) {
	t.Parallel()

	for _, arm := range initCaptureArms {
		t.Run(initCaptureArmLabel(arm), func(t *testing.T) {
			t.Parallel()

			rec, payload := capturedInitialize(t, arm)

			if arm == initCaptureArmNoRequest {
				if payload != nil {
					t.Fatalf("arm %q sent no initialize request, so its record must read back as an "+
						"absent payload; got %d bytes", arm, len(payload))
				}
				if rec.ModelsPresent {
					t.Errorf("models_present is true on the arm that sent no request")
				}
				if rec.ModelsCount != 0 {
					t.Errorf("models_count = %d on the arm that sent no request, want 0", rec.ModelsCount)
				}
				return
			}

			if payload == nil {
				t.Fatalf("arm %q captured an initialize response, so the reader must hand back its "+
					"payload; got nil", arm)
			}
			if !rec.ModelsPresent {
				t.Errorf("models_present is false on an arm whose record captured a response")
			}
			if rec.ModelsCount <= 0 {
				t.Fatalf("models_count = %d, want > 0; a zero summary would make the comparison below "+
					"hold for a payload carrying no models at all", rec.ModelsCount)
			}

			// The narrow reader is the one #1811, #1812, #1719 and #1809 call, so it
			// gets a caller here too: same file, same checks, and it must hand back
			// the same bytes rather than a second opinion about what claude sent.
			if wrapped := capturedInitializePayload(t, arm); !bytes.Equal(wrapped, payload) {
				t.Errorf("capturedInitializePayload returned %d payload bytes and capturedInitialize "+
					"returned %d; the two readers must agree", len(wrapped), len(payload))
			}

			var decoded struct {
				Models []json.RawMessage `json:"models"`
			}
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("decoding models out of the initialize payload: %v", err)
			}
			if len(decoded.Models) != rec.ModelsCount {
				t.Errorf("the payload carries %d model entries but the record's models_count says %d",
					len(decoded.Models), rec.ModelsCount)
			}
		})
	}
}

// TestInitCaptureArms_CoverTheCaptureDirectory asserts that the names
// initCaptureArms mints are EXACTLY the committed captures of this family. It is
// what makes the hard-coded initCaptureVersion safe: a fifth capture, or a
// re-capture at a new claude version, fails here with both sets printed rather
// than being quietly skipped by a table that names four files.
//
// The glob is version-blind on purpose — a version-pinned pattern would match
// nothing after a re-capture and report the miss as an empty directory.
func TestInitCaptureArms_CoverTheCaptureDirectory(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(filepath.Join(initCaptureDir, initCaptureGlob))
	if err != nil {
		t.Fatalf("glob %s in %s: %v", initCaptureGlob, initCaptureDir, err)
	}

	found := make([]string, 0, len(matches))
	for _, path := range matches {
		found = append(found, filepath.Base(path))
	}
	want := make([]string, 0, len(initCaptureArms))
	for _, arm := range initCaptureArms {
		want = append(want, initCaptureName(initCaptureVersion, arm))
	}
	sort.Strings(found)
	sort.Strings(want)

	if !slices.Equal(found, want) {
		t.Fatalf("the committed captures under %s and the names initCaptureArms mints disagree:\n"+
			"  on disk: %q\n  minted:  %q\nan unreadable capture is a proof nothing runs, and a "+
			"re-capture at a new claude version must be a deliberate edit here rather than a silent "+
			"skip", initCaptureDir, found, want)
	}
}
