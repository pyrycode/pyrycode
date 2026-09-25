package streamsup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// subagentPromptCapturePath is the committed real-claude capture of one turn that
// spawned one foreground general-purpose subagent under the daemon's OWN spawn
// flags, --forward-subagent-text included (#2657), produced by
// internal/e2e/realclaude's TestRealClaude_SubagentPromptCapture.
//
// A FIFTH READER, NOT parentCapturePath's. That capture was taken without the flag
// and pins the no-flag shape, where the delegated-prompt line does not exist; the
// two fixtures answer different questions and neither may be overwritten by the
// other. Read from here, not behind the e2e_realclaude tag, for the reason
// parentCapturePath gives: the bytes are only bytes, and reading them here keeps
// the measurement inside `make check`.
const subagentPromptCapturePath = "../e2e/realclaude/testdata/subagent_prompt_v" +
	subagentPromptCaptureVersion + ".json"

// subagentPromptCaptureVersion is spliced into the path so the filename cannot
// drift from the release the reader enforces; the producing side refuses to write
// under a mismatched name.
const subagentPromptCaptureVersion = "2.1.280"

// subagentPromptPinnedAgentID is the tool_use_id of the Agent call the captured
// turn made, and the parent_tool_use_id its delegated-prompt line carries.
//
// EMPTY ON PURPOSE UNTIL THE LIVE GATE HAS RUN, for parentPinnedAgentID's reason:
// the fixture is written by `make e2e-realclaude`, after verification, and filling
// this constant is the commit that lands it. subagentPromptReaderGate makes a
// fixture landing without it fatal.
const subagentPromptPinnedAgentID = ""

// subagentPromptReaderGate is parentReaderGate's state machine over (fixture,
// pin), with this capture's repair instructions. Same actions, one legal skip.
func subagentPromptReaderGate(fixtureExists, pinFilled bool) (action, reason string) {
	switch {
	case !fixtureExists && !pinFilled:
		return parentGateSkip, "the capture has not been taken yet: `make e2e-realclaude` on an " +
			"authenticated machine runs TestRealClaude_SubagentPromptCapture, which arms on this " +
			"fixture's absence and writes it in-repo. This is the ONLY state in which this reader may skip"
	case !fixtureExists && pinFilled:
		return parentGateFatal, "subagentPromptPinnedAgentID is set but " + subagentPromptCapturePath +
			" is gone. Restore the fixture, or empty the pin in the same commit that drops it"
	case fixtureExists && !pinFilled:
		return parentGateFatal, "the capture at " + subagentPromptCapturePath + " has landed but " +
			"subagentPromptPinnedAgentID is empty. Read the record's agent_tool_use_id and write it " +
			"here IN THE COMMIT THAT ADDS THE FIXTURE"
	default:
		return parentGateRun, ""
	}
}

// subagentPromptCapture is the slice of the record this reader needs.
type subagentPromptCapture struct {
	IsCapture      bool     `json:"is_capture"`
	ClaudeVersion  string   `json:"claude_version"`
	SpawnShape     []string `json:"spawn_shape"`
	AgentToolUseID string   `json:"agent_tool_use_id"`
	Frames         []struct {
		Index           int    `json:"index"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"frames"`
}

// readSubagentPromptCapture answers the gate and, when it says run, returns the
// provenance-checked record. Every failure past the gate fatals.
func readSubagentPromptCapture(t *testing.T) (subagentPromptCapture, bool) {
	t.Helper()
	raw, readErr := os.ReadFile(subagentPromptCapturePath)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", subagentPromptCapturePath, readErr)
	}
	switch action, reason := subagentPromptReaderGate(readErr == nil, subagentPromptPinnedAgentID != ""); action {
	case parentGateSkip:
		t.Skipf("#2657: %s", reason)
		return subagentPromptCapture{}, false
	case parentGateFatal:
		t.Fatalf("#2657: %s", reason)
		return subagentPromptCapture{}, false
	}

	var capture subagentPromptCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", subagentPromptCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this must be a genuine claude capture", subagentPromptCapturePath)
	}
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != subagentPromptCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — re-capture, or bump subagentPromptCaptureVersion "+
			"and the probe's version together", subagentPromptCapturePath, capture.ClaudeVersion,
			subagentPromptCaptureVersion)
	}
	// The whole premise: the line only exists under the flag the daemon spawns with.
	if !slices.Contains(capture.SpawnShape, "--forward-subagent-text") {
		t.Fatalf("%s: spawn_shape %v lacks --forward-subagent-text, so it cannot hold the line this "+
			"reader pins", subagentPromptCapturePath, capture.SpawnShape)
	}
	if capture.AgentToolUseID != subagentPromptPinnedAgentID {
		t.Fatalf("%s: agent_tool_use_id is %q but subagentPromptPinnedAgentID is %q",
			subagentPromptCapturePath, capture.AgentToolUseID, subagentPromptPinnedAgentID)
	}
	if len(capture.Frames) == 0 {
		t.Fatalf("%s: got 0 captured frames — a capture recording none proves nothing", subagentPromptCapturePath)
	}
	return capture, true
}

// delegatedPromptLine is what the reader decodes off each captured line to find
// the delegated prompt from the line's OWN bytes rather than the record's labels.
type delegatedPromptLine struct {
	Type            string          `json:"type"`
	ParentToolUseID json.RawMessage `json:"parent_tool_use_id"`
	Message         *struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	} `json:"message"`
}

// harnessFlagKeys are the line-level flags claude stamps on lines it authored or
// replayed (see the census in harnessNoOutputNudge's docblock). The delegated
// prompt carrying none of them is what makes the parent-id trigger necessary.
var harnessFlagKeys = []string{"isSynthetic", "isReplay", "isMeta", "isCompactSummary"}

// TestRealClaudeSubagentPromptCaptureDropsTheDelegatedPrompt is AC 2: replayed
// through ONE parser in record order, the delegated-prompt line emits no
// Unrecognized, while the subagent's tool frames still carry the Agent call's id.
func TestRealClaudeSubagentPromptCaptureDropsTheDelegatedPrompt(t *testing.T) {
	capture, ok := readSubagentPromptCapture(t)
	if !ok {
		return
	}

	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
	perFrame := make([][]turnevent.Event, len(capture.Frames))
	var promptFrames []int
	for i, frame := range capture.Frames {
		if frame.PayloadEncoding != "json-string" {
			t.Fatalf("frame %d payload_encoding = %q, want %q", frame.Index, frame.PayloadEncoding, "json-string")
		}
		before := len(events)
		if _, err := p.Write(append([]byte(frame.Payload), '\n')); err != nil {
			t.Fatalf("frame %d: Write err = %v, want nil", frame.Index, err)
		}
		perFrame[i] = events[before:]

		var line delegatedPromptLine
		if json.Unmarshal([]byte(frame.Payload), &line) != nil || line.Type != "user" || line.Message == nil {
			continue
		}
		if parentToolUseID(line.ParentToolUseID) != subagentPromptPinnedAgentID {
			continue
		}
		for _, b := range line.Message.Content {
			if b.Type == "text" {
				promptFrames = append(promptFrames, i)
				break
			}
		}
	}

	if len(promptFrames) != 1 {
		t.Fatalf("got %d user lines carrying a text block under parent %q, want exactly 1 (the "+
			"delegated prompt)", len(promptFrames), subagentPromptPinnedAgentID)
	}
	i := promptFrames[0]
	var line delegatedPromptLine
	var keys map[string]json.RawMessage
	_ = json.Unmarshal([]byte(capture.Frames[i].Payload), &line)
	_ = json.Unmarshal([]byte(capture.Frames[i].Payload), &keys)
	for _, b := range line.Message.Content {
		if b.Type != "text" {
			t.Errorf("frame %d: delegated-prompt line carries a %q block, want text blocks only",
				capture.Frames[i].Index, b.Type)
		}
	}
	// No harness flag: if one were set, an existing trigger would already take the
	// line and the parent-id trigger would be unjustified. Flag present but false
	// is still "no flag" for the parser, which reads these by value.
	for _, k := range harnessFlagKeys {
		if v, ok := keys[k]; ok && string(v) != "false" {
			t.Errorf("frame %d: delegated-prompt line carries %s=%s — re-key the trigger on the flag",
				capture.Frames[i].Index, k, v)
		}
	}
	for _, ev := range perFrame[i] {
		if u, ok := ev.(turnevent.Unrecognized); ok {
			t.Errorf("frame %d: delegated-prompt line emitted Unrecognized{Site: %q, Kind: %q}, want none",
				capture.Frames[i].Index, u.Site, u.Kind)
		}
	}

	// The drop must not take the subagent's own work with it. Two reads is the
	// probe's staging, so two of each.
	var starts, updates int
	for _, ev := range events {
		switch e := ev.(type) {
		case turnevent.ToolStart:
			if e.ParentToolCallID == subagentPromptPinnedAgentID {
				starts++
			}
		case turnevent.ToolUpdate:
			if e.ParentToolCallID == subagentPromptPinnedAgentID {
				updates++
			}
		}
	}
	if starts < 2 || updates < 2 {
		t.Errorf("got %d ToolStart and %d ToolUpdate under parent %q, want at least 2 of each",
			starts, updates, subagentPromptPinnedAgentID)
	}
}

// TestSubagentPromptReaderGateHasExactlyOneLegalSkip proves all four quadrants on
// every run, for TestParentReaderGateHasExactlyOneLegalSkip's reason.
func TestSubagentPromptReaderGateHasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fixture, pin  bool
		wantAction    string
		wantReasonHas string
	}{
		{false, false, parentGateSkip, "has not been taken yet"},
		{false, true, parentGateFatal, "Restore the fixture"},
		{true, false, parentGateFatal, "IN THE COMMIT THAT ADDS THE FIXTURE"},
		{true, true, parentGateRun, ""},
	}
	for _, tc := range tests {
		action, reason := subagentPromptReaderGate(tc.fixture, tc.pin)
		if action != tc.wantAction {
			t.Errorf("subagentPromptReaderGate(%v, %v) action = %q, want %q", tc.fixture, tc.pin, action, tc.wantAction)
		}
		if (tc.wantReasonHas == "") != (reason == "") || !strings.Contains(reason, tc.wantReasonHas) {
			t.Errorf("subagentPromptReaderGate(%v, %v) reason = %q, want it to contain %q",
				tc.fixture, tc.pin, reason, tc.wantReasonHas)
		}
	}
}

// delegatedPromptProbe stands in for a delegated prompt: distinctive, so the
// content-free sweep cannot match it by accident, and not any harness string, so
// a row that drops it can only have dropped it via the parent id.
const delegatedPromptProbe = "pyry-2657 delegated prompt probe: read alpha.txt then beta.txt"

// subagentUserLine builds a user line with parent as a raw JSON fragment ("" omits
// the key), so a row can supply a string, null, a non-string, or nothing.
func subagentUserLine(parent, blocks string) string {
	line := `{"type":"user","message":{"role":"user","content":[` + blocks + `]}`
	if parent != "" {
		line += `,"parent_tool_use_id":` + parent
	}
	return line + "}"
}

// TestParser_SubagentDelegatedPromptIsDropped is AC 3's matrix. Each row fails
// alone under a specific wrong rule: key on the key's presence and the null row
// reddens; widen past text and the image row reddens; scope to the message and
// the tool_result row loses its update.
func TestParser_SubagentDelegatedPromptIsDropped(t *testing.T) {
	t.Parallel()
	surfaced := []turnevent.Event{turnevent.Unrecognized{
		Site: turnevent.UnrecognizedUserBlock,
		Kind: "text",
		Raw:  textBlock(delegatedPromptProbe),
	}}
	tests := []struct {
		name string
		line string
		want []turnevent.Event
	}{
		{"subagent line's text block is dropped",
			subagentUserLine(`"toolu_agent"`, textBlock(delegatedPromptProbe)), nil},
		{"main-thread null parent surfaces",
			subagentUserLine(`null`, textBlock(delegatedPromptProbe)), surfaced},
		{"main-thread absent parent surfaces",
			subagentUserLine("", textBlock(delegatedPromptProbe)), surfaced},
		{"main-thread empty parent surfaces",
			subagentUserLine(`""`, textBlock(delegatedPromptProbe)), surfaced},
		{"a non-string parent reads as main-thread and surfaces",
			subagentUserLine(`7`, textBlock(delegatedPromptProbe)), surfaced},
		{"subagent line's unknown block type still surfaces",
			subagentUserLine(`"toolu_agent"`, `{"type":"image","text":"ignored"}`),
			[]turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "image",
				Raw:  `{"type":"image","text":"ignored"}`,
			}}},
		{"subagent line's tool_result still maps with its parent",
			subagentUserLine(`"toolu_agent"`, textBlock(delegatedPromptProbe)+","+toolResultBlock("tu-2657")),
			[]turnevent.Event{turnevent.ToolUpdate{
				ToolCallID:       "tu-2657",
				ParentToolCallID: "toolu_agent",
				Status:           turnevent.ToolStatusCompleted,
				Content:          turnevent.TextContent{Text: "x"},
			}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectEvents(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("events:\n got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// TestParser_SubagentPromptDropIsLoggedContentFree is AC 4: the same message and
// the same two attributes as the harness-block drop, and no byte of the prompt.
func TestParser_SubagentPromptDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	rec := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(rec))
	line := subagentUserLine(`"toolu_agent"`, textBlock(delegatedPromptProbe))
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	drops := rec.withMessage(harnessNudgeDropMsg)
	if len(drops) != 1 {
		t.Fatalf("records with message %q: got %d, want 1 (all records: %+v)", harnessNudgeDropMsg, len(drops), rec.all())
	}
	wantAttrs := map[string]string{
		"site": string(turnevent.UnrecognizedUserBlock),
		"type": "text",
	}
	if !reflect.DeepEqual(drops[0].attrs, wantAttrs) {
		t.Errorf("drop attrs: got %v, want exactly %v", drops[0].attrs, wantAttrs)
	}
	for _, r := range rec.all() {
		if strings.Contains(r.msg, delegatedPromptProbe) {
			t.Errorf("record message carries the prompt: %q", r.msg)
		}
		for k, v := range r.attrs {
			if strings.Contains(v, delegatedPromptProbe) {
				t.Errorf("record %q attr %q carries the prompt; the drop logs site and type only", r.msg, k)
			}
		}
	}
}
