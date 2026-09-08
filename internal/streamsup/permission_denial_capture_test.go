package streamsup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// #2232 — the committed real-claude captures of claude REFUSING a tool call,
// readable from this package.
//
// The four records are #2189's bypass-reescalation probe arms, recorded upstream
// of the parser: each drives a turn that attempts a working-directory-violating
// Bash call under a different permission posture, so three of them carry denials
// and the fourth — the arm launched in bypass — denies nothing and is the control.
//
// It reads files under internal/e2e/realclaude/testdata/ for the reason
// compactionCapturePath gives: the bytes are only BYTES, the e2e_realclaude build
// tag belongs to that package's Go FILES rather than to its testdata, and reading
// them from here is what keeps the measurement inside `make check` instead of
// behind an opt-in gate that SKIPS (exit 0) with no claude login.
//
// THIS IS A FOURTH READER, NOT A GENERALISATION OF ANY OF THE THREE. capturedLines'
// docblock forbids by name growing it a path parameter, because its provenance
// assertions are what stop a hand-built payload file being swapped in behind them.
// So this takes the shape capturedInitialize took for the same problem: its own
// package constants, an ARM SELECTOR from a closed set rather than a path, and
// every provenance check written out below rather than borrowed. No caller can put
// an unchecked file behind these assertions, and none of the four readers can
// decode another's record shape — reaching for the wrong one yields zero values
// rather than a failure.
const denialCaptureDir = "../e2e/realclaude/testdata"

// denialCaptureVersion is the claude release these captures were taken at, spliced
// into every path below rather than repeated so a filename cannot drift from the
// version this reader enforces. It pins the FIXTURE FAMILY's release (2.1.239), not
// the current one: a re-capture at a later claude must break this package loudly
// rather than quietly measure a release nobody looked at.
const denialCaptureVersion = "2.1.239"

// The four committed arms, spelled as each record's own `arm` field spells them. A
// closed literal rather than a glob: this package only ever reads four committed
// files, and an undeclared arm is a caller mistake rather than a discovery.
const (
	denialArmControlDefault = "control_default"
	denialArmEnable         = "enable"
	denialArmReescalate     = "reescalate"
	denialArmControlBypass  = "control_bypass"
)

var denialCaptureArms = []string{
	denialArmControlDefault,
	denialArmEnable,
	denialArmReescalate,
	denialArmControlBypass,
}

// denialCapturePinnedCounts IS THE MEASUREMENT THIS TICKET COMMITS: how many
// system/permission_denied lines each arm's record holds, re-counted at f2a4fa1a.
//
// THE ZERO IS THE LOAD-BEARING ENTRY. control_bypass launched in bypass mode and
// denied nothing, so it is the control that stops every count here passing
// vacuously on a mapping that emits nothing at all — without it, an arm that
// silently dropped all seven denials would satisfy a "counts match" assertion only
// if the pins were also zero, and nothing would say they should not be.
var denialCapturePinnedCounts = map[string]int{
	denialArmControlDefault: 3,
	denialArmEnable:         3,
	denialArmReescalate:     1,
	denialArmControlBypass:  0,
}

// denialCapturePath mints one arm's path from package constants. The reader mints
// its own path; no caller supplies one.
func denialCapturePath(arm string) string {
	return filepath.Join(denialCaptureDir,
		fmt.Sprintf("bypass_reescalation_v%s_%s.json", denialCaptureVersion, arm))
}

// denialCaptureRecord is the slice of the probe's record this reader needs.
//
// stdout_events is []json.RawMessage, which is what makes a replay a replay: each
// entry keeps claude's own line bytes, so the parser is fed what claude wrote
// rather than a re-encoding of what some decoder made of it.
type denialCaptureRecord struct {
	Arm           string            `json:"arm"`
	ClaudeVersion string            `json:"claude_version"`
	StdoutEvents  []json.RawMessage `json:"stdout_events"`
}

// capturedDenialLines reads one arm's committed record, asserts its provenance, and
// returns its stdout lines as claude's own bytes.
//
// The provenance checks live HERE, at the reader, so no caller can grow a second
// weaker copy of them. The parameter is an ARM SELECTOR from the closed
// denialCaptureArms set, never a path — the unknown-arm branch is that sentence
// made executable.
//
// The record's `arm` is checked against the arm requested, so a file whose name
// says one arm while its body says another fails here rather than being believed on
// its name. Every failure is t.Fatalf, never a skip: these captures are committed,
// so a missing or empty record is a broken premise rather than an unavailable
// resource, and a skip would report a deleted fixture as a green run.
func capturedDenialLines(t *testing.T, arm string) []json.RawMessage {
	t.Helper()

	known := false
	for _, candidate := range denialCaptureArms {
		if candidate == arm {
			known = true
			break
		}
	}
	if !known {
		t.Fatalf("unknown denial capture arm %q; the closed set is %v", arm, denialCaptureArms)
	}

	path := denialCapturePath(arm)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading capture %s: %v — the fixtures are committed, so this is a broken "+
			"premise rather than an unavailable resource", path, err)
	}
	var record denialCaptureRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decoding capture %s: %v", path, err)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on the
	// leading token; an "<unavailable: ...>" version fails it too, which is correct —
	// a capture that could not read the version it was taken at cannot vouch for the
	// release its filename claims.
	if got, _, _ := strings.Cut(record.ClaudeVersion, " "); got != denialCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it. Re-capture at "+
			"the pinned version, or bump denialCaptureVersion and denialCapturePinnedCounts together",
			path, record.ClaudeVersion, denialCaptureVersion)
	}
	if record.Arm != arm {
		t.Fatalf("%s: the record says arm %q but was read as %q — a file must not be believed on "+
			"its name when its body disagrees", path, record.Arm, arm)
	}
	if len(record.StdoutEvents) == 0 {
		t.Fatalf("%s: the record holds ZERO stdout lines. A replay over none is vacuous — every "+
			"assertion built on it would pass without reading a byte claude sent", path)
	}

	// COMPACTED, and the whitespace removed is the RECORDER'S rather than claude's.
	// The record is written by an indenting encoder, so each stored line comes back
	// spread over several physical lines; the parser is line-oriented and would read
	// each fragment as a line of its own, turning one denial into a dozen undecodable
	// rows. json.Compact removes insignificant whitespace only — key ORDER and every
	// value byte survive, which is what keeps this a replay of claude's line rather
	// than a re-encoding of somebody's decode of it.
	lines := make([]json.RawMessage, 0, len(record.StdoutEvents))
	for i, event := range record.StdoutEvents {
		var compact bytes.Buffer
		if err := json.Compact(&compact, event); err != nil {
			t.Fatalf("%s: stdout line %d is not valid JSON: %v", path, i, err)
		}
		lines = append(lines, json.RawMessage(compact.Bytes()))
	}
	return lines
}

// replayDenialCapture feeds every line of one arm through a FRESH parser each and
// returns the denials it emitted, beside the count of turnevent.Unrecognized events
// the same replay produced.
//
// A fresh parser per line is licensed by the documented turn-statelessness
// (maxTaskRosterEntries): the only cross-line state is the partial-line buffer,
// which a complete line never uses. It is also what makes each denial's mapping
// independent of every line before it.
//
// The unrecognized count is returned rather than asserted here so one caller can
// pin it as the thing this ticket CHANGED — see
// TestParser_DenialCaptureCostsNoUnrecognizedRow.
func replayDenialCapture(t *testing.T, arm string) (denials []turnevent.ToolCallDenied, unrecognized int) {
	t.Helper()
	for _, line := range capturedDenialLines(t, arm) {
		for _, ev := range collectEvents(string(line)) {
			switch e := ev.(type) {
			case turnevent.ToolCallDenied:
				denials = append(denials, e)
			case turnevent.Unrecognized:
				unrecognized++
			}
		}
	}
	return denials, unrecognized
}

// capturedDenialFields re-derives the three carried strings from ONE line's OWN
// bytes, plus claude's two identities. Derived here rather than taken from the
// probe's summary fields: a reader that trusted those would pin the probe's
// decoding rather than claude's line.
type capturedDenialFields struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
	UUID      string `json:"uuid"`
}

// capturedDenialLinesOnly re-derives which lines of an arm are denials from each
// line's own envelope, so the count below is a measurement of claude's bytes rather
// than an agreement with the pin it is compared against.
func capturedDenialLinesOnly(t *testing.T, arm string) []capturedDenialFields {
	t.Helper()
	var out []capturedDenialFields
	for _, line := range capturedDenialLines(t, arm) {
		var envelope struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatalf("%s: a captured line does not decode as JSON: %v", denialCapturePath(arm), err)
		}
		if envelope.Type != "system" || envelope.Subtype != "permission_denied" {
			continue
		}
		var fields capturedDenialFields
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatalf("%s: a captured denial line does not decode: %v", denialCapturePath(arm), err)
		}
		out = append(out, fields)
	}
	return out
}

// TestParser_DenialCaptureMapsEveryCapturedLine replays all four committed arms and
// pins one turnevent.ToolCallDenied per captured denial, carrying claude's own
// tool_name, tool_use_id and message — AC 1.
//
// Each arm's count is compared against BOTH the pin and a re-derivation from the
// lines' own bytes, which are three independent numbers rather than two: a pin
// edited to match a replay nobody re-read would still have to agree with claude's
// envelopes, and the control_bypass row's zero keeps the whole table from passing on
// a mapping that emits nothing.
func TestParser_DenialCaptureMapsEveryCapturedLine(t *testing.T) {
	t.Parallel()

	total := 0
	for _, arm := range denialCaptureArms {
		t.Run(arm, func(t *testing.T) {
			t.Parallel()
			want := denialCapturePinnedCounts[arm]
			captured := capturedDenialLinesOnly(t, arm)
			if len(captured) != want {
				t.Fatalf("%s: the record holds %d permission_denied lines, but "+
					"denialCapturePinnedCounts says %d. If claude's shape genuinely changed, "+
					"re-capture and repin together; a pin edited to match a fixture nobody re-read "+
					"turns this into an agreement with itself", denialCapturePath(arm), len(captured), want)
			}

			denials, _ := replayDenialCapture(t, arm)
			if len(denials) != want {
				t.Fatalf("%s: replaying the record produced %d ToolCallDenied events, want %d — "+
					"one per captured denial line", denialCapturePath(arm), len(denials), want)
			}
			for i, got := range denials {
				line := captured[i]
				if got.ToolName != line.ToolName || got.ToolCallID != line.ToolUseID ||
					got.Message != line.Message {
					t.Errorf("%s: denial %d carried %q/%q/%d bytes, want claude's own "+
						"%q/%q/%d bytes", denialCapturePath(arm), i, got.ToolName, got.ToolCallID,
						len(got.Message), line.ToolName, line.ToolUseID, len(line.Message))
				}
				// Nothing in the captures is anywhere near a cap, so both reports must be
				// nil. A cut or a drop here would mean a cap moved under the observation.
				if got.TruncatedFields != nil || got.DroppedFields != nil {
					t.Errorf("%s: denial %d reports %v/%v, want nil/nil — no captured value is "+
						"near its cap", denialCapturePath(arm), i, got.TruncatedFields, got.DroppedFields)
				}
			}
		})
		total += denialCapturePinnedCounts[arm]
	}
	if total != 7 {
		t.Errorf("the four pinned counts total %d, want 7 — the ticket's measurement", total)
	}
}

// TestParser_DenialCaptureYieldsEmptyDecisionReasons pins the ABSENCE of
// decision_reason_type and decision_reason across all seven captured denials — AC 3.
//
// The point is that absence is recorded as an OBSERVATION rather than assumed away.
// Both fields are declared on SDKPermissionDeniedMessage in
// @anthropic-ai/claude-agent-sdk@0.3.263 and the consumer wants them, so the decode
// carries them; claude 2.1.239 sent neither on any of these seven working-directory
// refusals. Because the fixtures are verbatim line bytes, that absence is claude's
// and not a recorder's — and because both fields are also absent from the two
// report slices, a later reader can tell it from a value the daemon emptied.
func TestParser_DenialCaptureYieldsEmptyDecisionReasons(t *testing.T) {
	t.Parallel()

	seen := 0
	for _, arm := range denialCaptureArms {
		denials, _ := replayDenialCapture(t, arm)
		for i, got := range denials {
			seen++
			if got.DecisionReasonType != "" || got.DecisionReason != "" {
				t.Errorf("%s: denial %d yielded decision reasons %q/%q, want both empty — no "+
					"captured line carries either key. If a re-capture at a later claude does "+
					"carry them, that is a documentation change and this pin moves with it",
					denialCapturePath(arm), i, got.DecisionReasonType, got.DecisionReason)
			}
		}
	}
	if seen != 7 {
		t.Fatalf("swept %d denials, want 7 — a sweep over fewer proves less than it claims", seen)
	}
}

// TestParser_DenialCaptureCarriesNeitherClaudeIdentity is AC 2's second half, run
// against the REAL values rather than a structural claim.
//
// Neither claude's session_id nor its uuid may reach any field of the event:
// session_id is claude's session identity and NOT the daemon's conversation
// identity, and uuid is a per-line message id nothing in the daemon reads. The
// guarantee is structural — neither key is declared on systemPermissionDeniedLine,
// so encoding/json discards both — and this sweep is what would catch a decode
// target that grew one. It takes each value from the line that CARRIED it, so it
// cannot pass by comparing against a string claude never sent.
func TestParser_DenialCaptureCarriesNeitherClaudeIdentity(t *testing.T) {
	t.Parallel()

	swept := 0
	for _, arm := range denialCaptureArms {
		captured := capturedDenialLinesOnly(t, arm)
		denials, _ := replayDenialCapture(t, arm)
		if len(captured) != len(denials) {
			t.Fatalf("%s: %d captured denials but %d events; the sweep below would compare "+
				"mismatched pairs", denialCapturePath(arm), len(captured), len(denials))
		}
		for i, got := range denials {
			if captured[i].SessionID == "" || captured[i].UUID == "" {
				t.Fatalf("%s: captured denial %d carries no session_id or no uuid, so this sweep "+
					"would compare against the empty string and pass on anything",
					denialCapturePath(arm), i)
			}
			for name, field := range map[string]string{
				"ToolName": got.ToolName, "ToolCallID": got.ToolCallID, "Message": got.Message,
				"DecisionReasonType": got.DecisionReasonType, "DecisionReason": got.DecisionReason,
			} {
				if strings.Contains(field, captured[i].SessionID) {
					t.Errorf("%s: denial %d field %s carries claude's session_id, which is NOT the "+
						"daemon's conversation identity", denialCapturePath(arm), i, name)
				}
				if strings.Contains(field, captured[i].UUID) {
					t.Errorf("%s: denial %d field %s carries claude's uuid, which nothing in the "+
						"daemon reads", denialCapturePath(arm), i, name)
				}
			}
			swept++
		}
	}
	if swept != 7 {
		t.Fatalf("swept %d denials, want 7 — a leak sweep over fewer compared less than it claims", swept)
	}
}

// TestParser_DenialCaptureCostsNoUnrecognizedRow pins what this ticket actually
// CHANGED on the captured bytes, and it is not what #2232's body predicted.
//
// The body reasoned that the line fell to the ignoredLineTypes branch's silent
// debug drop, so that "no unrecognized_message frame for the line" was already true
// and needed no criterion. It was not true. claude spells `message` as a STRING and
// streamLine declares that key as *streamMessage, so every denial failed the
// whole-line decode and reached emitUnrecognized — one surfaced noise row per denied
// tool call on the operator's timeline. consumePermissionDeniedLine is what makes
// the body's sentence true, and this test is the evidence: replaying all four arms
// now yields zero Unrecognized events.
//
// It is deliberately whole-record rather than denial-only. A gate that matched too
// widely would take OTHER undecodable lines away from the surfaced tier, and that
// loss is invisible to every other test here; asserting zero across every line of
// four real turns is what would catch it.
func TestParser_DenialCaptureCostsNoUnrecognizedRow(t *testing.T) {
	t.Parallel()

	for _, arm := range denialCaptureArms {
		t.Run(arm, func(t *testing.T) {
			t.Parallel()
			denials, unrecognized := replayDenialCapture(t, arm)
			if unrecognized != 0 {
				t.Errorf("%s: replaying the record produced %d turnevent.Unrecognized events, want 0",
					denialCapturePath(arm), unrecognized)
			}
			if len(denials) != denialCapturePinnedCounts[arm] {
				t.Errorf("%s: %d denials, want %d — a zero-unrecognized claim over a replay that "+
					"mapped the wrong number of lines says nothing",
					denialCapturePath(arm), len(denials), denialCapturePinnedCounts[arm])
			}
		})
	}
}
