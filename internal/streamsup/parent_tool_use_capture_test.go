package streamsup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// parentCapturePath is the committed real-claude capture of one live turn that
// fanned out to a subagent (#2191), produced by internal/e2e/realclaude's
// TestRealClaude_ParentToolUseCapture: one turn in which claude calls the Agent
// tool with a subagent that runs at least two tools, every line recorded upstream
// of the parser.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// compactionCapturePath and toolProgressCapturePath both give: the bytes are only
// BYTES, the e2e_realclaude build tag belongs to that package's Go files rather
// than to its testdata, and reading it from here is what keeps the measurement
// inside `make check` instead of behind an opt-in gate that SKIPS (exit 0) with no
// claude login.
//
// THIS IS A FOURTH READER, NOT A GENERALISATION OF ANY OF THE THREE. capturedLines'
// docblock forbids by name growing it a path parameter, because its is_capture
// assertion is what stops a hand-built payload file being swapped in behind the
// provenance checks. So this takes the shape its two successors took: its own
// package constants, no path parameter, and every provenance check written out
// below rather than borrowed. None of the four can decode another's record shape,
// and reaching for the wrong one yields zero values rather than a failure.
const parentCapturePath = "../e2e/realclaude/testdata/parent_tool_use_v" +
	parentCaptureVersion + ".json"

// parentCaptureVersion is the claude release the capture was taken at, spliced
// into the path above rather than repeated so the filename cannot drift from the
// version this reader enforces. The producing side refuses to write under a
// mismatched name (ptucRecord.fixtureWorthy), so a claude upgrade is a loud
// instruction to re-capture and repin rather than a fixture that quietly measures
// another release.
const parentCaptureVersion = "2.1.259"

// parentPinnedAgentID IS THE MEASUREMENT THIS TICKET COMMITS: the tool_use_id of
// the Agent call the captured turn made. Every frame the subagent produced names
// it, and the Agent call's own two frames name nothing — which is the whole claim
// AC 2 asks to be proven against live bytes rather than against a line someone
// wrote by hand.
//
// IT IS EMPTY ON PURPOSE UNTIL THE LIVE GATE HAS RUN. The fixture cannot exist
// before `make e2e-realclaude` produces it, which happens after verification, so a
// reader asserting against bytes any earlier would redden `make check` for every
// unrelated ticket. parentReaderGate turns that into a state machine with exactly
// one legal skip: filling this constant is the commit that lands the fixture, and a
// fixture landing WITHOUT it fatals rather than passing quietly. #2229's AC 4 end
// state is reached by construction rather than by remembering, and #2236 is the
// commit that had to reach it late.
//
// The value is read out of the record's own agent_tool_use_id, but every assertion
// below re-derives its subject from each line's OWN bytes rather than from the
// record's labels — so committing the record does not turn this into a measurement
// agreeing with itself.
const parentPinnedAgentID = "toolu_01LRXMtrx8W1mm14U6LywqAP"

// The four states of (fixture, pin). Only the first is a skip, and only on the leg
// before the live gate has ever run.
const (
	parentGateSkip  = "skip"
	parentGateRun   = "run"
	parentGateFatal = "fatal"
)

// parentReaderGate is pure so all four quadrants are proved on every run,
// including the leg where the fixture is still absent and the reader itself cannot
// assert anything. It is #2229's compactionReaderGate copied rather than imported:
// that one lives in a file this package already carries, but its states are keyed
// on a slice where this one is keyed on a string, and sharing would mean widening
// a gate that is exactly right for its own ticket.
func parentReaderGate(fixtureExists, pinFilled bool) (action, reason string) {
	switch {
	case !fixtureExists && !pinFilled:
		return parentGateSkip, "the capture has not been taken yet: `make e2e-realclaude` on an " +
			"authenticated machine runs TestRealClaude_ParentToolUseCapture, which arms on this " +
			"fixture's absence and writes it in-repo. This is the ONLY state in which this reader " +
			"may skip, and it ends the moment the bytes and the pin land together"
	case !fixtureExists && pinFilled:
		return parentGateFatal, "parentPinnedAgentID names an Agent call a capture observed but " +
			parentCapturePath + " is gone. Restore the fixture, or if the capture was deliberately " +
			"dropped, empty the pin in the same commit — a pin with no bytes behind it is a " +
			"measurement nothing supports"
	case fixtureExists && !pinFilled:
		return parentGateFatal, "the capture at " + parentCapturePath + " has landed but " +
			"parentPinnedAgentID is still empty, so nothing pins what it measured. Read the " +
			"record's agent_tool_use_id and write it here IN THE COMMIT THAT ADDS THE FIXTURE. " +
			"Until then the bytes are committed and unpinned, which is the state this gate exists " +
			"to make impossible"
	default:
		return parentGateRun, ""
	}
}

// parentCapture is the slice of the record this reader needs. payload is a JSON
// STRING holding the whole line (payload_encoding says so per frame), not a nested
// object, so the bytes can be re-decoded as claude sent them.
type parentCapture struct {
	IsCapture      bool   `json:"is_capture"`
	ClaudeVersion  string `json:"claude_version"`
	AgentToolUseID string `json:"agent_tool_use_id"`
	Frames         []struct {
		Index           int    `json:"index"`
		Type            string `json:"type"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"frames"`
}

// readParentCapture answers the gate and, when it says run, returns the verified
// record. Every failure past the gate is t.Fatalf, never a skip: the capture is
// committed, so a missing or malformed record is a broken premise rather than an
// unavailable resource.
func readParentCapture(t *testing.T) (parentCapture, bool) {
	t.Helper()
	raw, readErr := os.ReadFile(parentCapturePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", parentCapturePath, readErr)
	}
	switch action, reason := parentReaderGate(exists, parentPinnedAgentID != ""); action {
	case parentGateSkip:
		t.Skipf("#2191: %s", reason)
		return parentCapture{}, false
	case parentGateFatal:
		t.Fatalf("#2191: %s", reason)
		return parentCapture{}, false
	}

	var capture parentCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", parentCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, never a "+
			"hand-written payload; a guessed line carries whatever shape its author expected",
			parentCapturePath)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on
	// the leading token; an "<unavailable: ...>" version fails it too, which is
	// correct — a capture that could not read the version it was taken at cannot
	// vouch for the release its filename claims.
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != parentCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it. Re-capture at "+
			"the pinned version, or bump parentCaptureVersion and re-run the probe together",
			parentCapturePath, capture.ClaudeVersion, parentCaptureVersion)
	}
	if capture.AgentToolUseID != parentPinnedAgentID {
		t.Fatalf("%s: agent_tool_use_id is %q but parentPinnedAgentID is %q — the pin must name the "+
			"Agent call the committed bytes actually made, or every assertion below measures a "+
			"different turn", parentCapturePath, capture.AgentToolUseID, parentPinnedAgentID)
	}
	if len(capture.Frames) == 0 {
		t.Fatalf("%s: got 0 captured frames. A capture recording none is vacuous: every assertion "+
			"built on it would pass without reading a byte claude sent", parentCapturePath)
	}
	return capture, true
}

// TestRealClaudeParentToolUseCaptureJoinsSubagentWorkToItsAgentCall is AC 2's live
// half: replayed hermetically from the committed fixture, the subagent's tool calls
// produce tool_use and tool_result frames whose parent names the Agent call, while
// the Agent call's own two frames carry an empty one.
//
// EVERY LINE GOES THROUGH ONE PARSER, in the capture's own record order, because
// that is how the daemon sees them and because a fresh parser per line could not
// catch a residual latched across lines — the bug emitAssistant's doc says would
// file a main-thread call under the last subagent that ran.
//
// The subject of each assertion is re-derived from the EMITTED EVENT, never from
// the record's labels: the parser is what decides which id a frame carries, so a
// reader trusting the probe's own bookkeeping would agree with it by construction.
func TestRealClaudeParentToolUseCaptureJoinsSubagentWorkToItsAgentCall(t *testing.T) {
	capture, ok := readParentCapture(t)
	if !ok {
		return
	}

	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.Default())
	for _, frame := range capture.Frames {
		if frame.PayloadEncoding != "json-string" {
			t.Fatalf("%s: frame %d payload_encoding = %q, want %q (the payload must be the whole "+
				"line as a JSON string)", parentCapturePath, frame.Index, frame.PayloadEncoding, "json-string")
		}
		if _, err := p.Write(append([]byte(frame.Payload), '\n')); err != nil {
			t.Fatalf("frame %d: Write err = %v, want nil", frame.Index, err)
		}
	}

	// The Agent call itself: its own tool_use and tool_result are MAIN-THREAD frames,
	// because claude made that call, not a subagent. This is the half that stops a
	// producer stamping every frame in the turn.
	var agentFrames, attributed int
	for _, ev := range events {
		switch e := ev.(type) {
		case turnevent.ToolStart:
			if e.ToolCallID == parentPinnedAgentID {
				agentFrames++
				if e.ParentToolCallID != "" {
					t.Errorf("the Agent call's own tool_use carries ParentToolCallID %q, want %q — "+
						"claude made that call, so it belongs to the main thread",
						e.ParentToolCallID, "")
				}
				continue
			}
			if e.ParentToolCallID == parentPinnedAgentID {
				attributed++
			}
		case turnevent.ToolUpdate:
			if e.ToolCallID == parentPinnedAgentID {
				agentFrames++
				if e.ParentToolCallID != "" {
					t.Errorf("the Agent call's own tool_result carries ParentToolCallID %q, want %q "+
						"— see the tool_use arm", e.ParentToolCallID, "")
				}
				continue
			}
			if e.ParentToolCallID == parentPinnedAgentID {
				attributed++
			}
		}
	}

	// Both counts are asserted, and each closes a way the other could pass alone. A
	// producer that never read the key leaves attributed at zero; one that stamped a
	// constant leaves the Agent call's own frames non-empty, which the loop above
	// reports. The minimum is the AC's own: a subagent running at least two tools
	// produces at least two tool_use frames and their two results.
	const wantAgentFrames, minAttributed = 2, 4
	if agentFrames != wantAgentFrames {
		t.Errorf("got %d frames for the Agent call %q, want %d (its tool_use and its tool_result) "+
			"— the pin names a call this replay did not produce both frames for",
			agentFrames, parentPinnedAgentID, wantAgentFrames)
	}
	if attributed < minAttributed {
		t.Errorf("got %d frames attributed to the Agent call %q, want at least %d. The capture is "+
			"supposed to hold a subagent that ran at least two tools; fewer means the fixture does "+
			"not prove the join it was taken for", attributed, parentPinnedAgentID, minAttributed)
	}
}

// TestParentReaderGateHasExactlyOneLegalSkip proves all four quadrants of the
// (fixture, pin) machine on every run, including the ones the reader above cannot
// reach in whatever state the tree is in when it runs.
//
// This is the test that makes the skip above safe to have. A gate whose skip arm is
// only exercised in the state it fires in is a gate nobody has read: the two fatal
// quadrants are precisely the ones that fire once, years apart, when someone deletes
// a fixture or lands one without its pin — and a run in the skip state would
// otherwise prove neither.
func TestParentReaderGateHasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		fixture, pin  bool
		wantAction    string
		wantReasonHas string
	}{
		{"neither yet: the one legal skip", false, false, parentGateSkip, "has not been taken yet"},
		{"pin with no bytes behind it", false, true, parentGateFatal, "Restore the fixture"},
		{"bytes with nothing pinning them", true, false, parentGateFatal, "IN THE COMMIT THAT ADDS THE FIXTURE"},
		{"both landed: assert", true, true, parentGateRun, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, reason := parentReaderGate(tc.fixture, tc.pin)
			if action != tc.wantAction {
				t.Fatalf("parentReaderGate(%v, %v) action = %q, want %q",
					tc.fixture, tc.pin, action, tc.wantAction)
			}
			if tc.wantReasonHas == "" {
				if reason != "" {
					t.Errorf("reason = %q, want empty — the run arm explains nothing", reason)
				}
				return
			}
			if !strings.Contains(reason, tc.wantReasonHas) {
				t.Errorf("reason = %q, want it to contain %q — the message is the repair "+
					"instruction, and a state nobody can act on is a state that stalls the pipeline",
					reason, tc.wantReasonHas)
			}
		})
	}
}
