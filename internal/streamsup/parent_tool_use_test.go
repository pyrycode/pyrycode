package streamsup

import (
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// parentUserLineWith mints one stream-json `user` line from raw key FRAGMENTS,
// spliced in verbatim rather than marshalled from a Go value — assistantLineWith's
// choice and for its reason: several rows below need shapes no Go struct can hold
// (a parent_tool_use_id that is a number, an object, an array).
//
// THE FIXTURES IN THIS FILE ARE SYNTHESIZED, and unlike assistant_error_test.go's
// that is a STAGING statement rather than a permanent one. The shapes here ARE
// provokable — a turn that spawns a subagent produces them — and
// TestRealClaude_ParentToolUseCapture exists to provoke exactly that. What no live
// run can be made to produce on demand is a chosen NESTING DEPTH, which is why AC 3
// is proven against a synthesized line below and says so at that test. No
// internal/streamsup/testdata/ is minted for any of it: assistant_error_test.go
// states why, and the committed capture lives under
// internal/e2e/realclaude/testdata/ with its siblings.
func parentUserLineWith(fragments ...string) string {
	line := `{"type":"user"`
	for _, f := range fragments {
		line += "," + f
	}
	return line + `}`
}

// The two message bodies these rows map. Each carries exactly one mappable block,
// so a line built from it emits exactly one ToolStart or one ToolUpdate and no row
// below has to index past a neighbour.
const (
	parentToolUseBlock = `"message":{"id":"m1","role":"assistant","content":` +
		`[{"type":"tool_use","id":"toolu_child","name":"Read","input":{"file_path":"/tmp/x"}}]}`
	parentTextBlock = `"message":{"id":"m-text","role":"assistant","content":` +
		`[{"type":"text","text":"hello"}]}`
	parentToolResultBlock = `"message":{"role":"user","content":` +
		`[{"tool_use_id":"toolu_child","type":"tool_result","content":"ok"}]}`
	// The id an Agent call would carry, in claude's own shape.
	parentAgentID = "toolu_01AgentCallXXXXXXXXXXXXXX"
)

// toolStartFrom asserts the events hold exactly one ToolStart and returns it. Every
// assistant row goes through here: a decode that suppressed the block, or emitted a
// second event beside it, is a failure no per-field assertion below would catch.
func toolStartFrom(t *testing.T, events []turnevent.Event) turnevent.ToolStart {
	t.Helper()
	var found []turnevent.ToolStart
	for _, ev := range events {
		if ts, ok := ev.(turnevent.ToolStart); ok {
			found = append(found, ts)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d ToolStart events, want exactly 1 (all events: %#v)", len(found), events)
	}
	return found[0]
}

// toolUpdateFrom is toolStartFrom for the user side, and carries its reasoning.
func toolUpdateFrom(t *testing.T, events []turnevent.Event) turnevent.ToolUpdate {
	t.Helper()
	var found []turnevent.ToolUpdate
	for _, ev := range events {
		if tu, ok := ev.(turnevent.ToolUpdate); ok {
			found = append(found, tu)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d ToolUpdate events, want exactly 1 (all events: %#v)", len(found), events)
	}
	return found[0]
}

func textChunkFrom(t *testing.T, events []turnevent.Event) turnevent.TextChunk {
	t.Helper()
	var found []turnevent.TextChunk
	for _, ev := range events {
		if text, ok := ev.(turnevent.TextChunk); ok {
			found = append(found, text)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d TextChunk events, want exactly 1 (all events: %#v)", len(found), events)
	}
	return found[0]
}

func TestParser_ParentToolUseID_ReachesTextEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		parent string
		wantID string
	}{
		{name: "valid", parent: `"parent_tool_use_id":"toolu_01Case-Sensitive_ID"`, wantID: "toolu_01Case-Sensitive_ID"},
		{name: "absent"},
		{name: "null", parent: `"parent_tool_use_id":null`},
		{name: "non-string", parent: `"parent_tool_use_id":{"id":"toolu_wrong"}`},
		{name: "over cap", parent: `"parent_tool_use_id":"` + strings.Repeat("x", maxTaskFieldID+1) + `"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fragments := []string{parentTextBlock}
			if tc.parent != "" {
				fragments = append(fragments, tc.parent)
			}
			got := textChunkFrom(t, parseOneLine(t, assistantLineWith(fragments...)))
			if got.MessageID != "m-text" || got.Text != "hello" || got.ParentToolCallID != tc.wantID {
				t.Errorf("TextChunk = %+v, want MessageID %q, Text %q, ParentToolCallID %q",
					got, "m-text", "hello", tc.wantID)
			}
		})
	}
}

func TestParser_ParentToolUseID_TextReadsInnerDepthVerbatim(t *testing.T) {
	t.Parallel()
	const outer, inner = "toolu_01OuterAgent", "toolu_01InnerAgent"
	events := parseLines(t,
		assistantLineWith(parentTextBlock, `"parent_tool_use_id":"`+outer+`"`),
		assistantLineWith(parentTextBlock, `"parent_tool_use_id":"`+inner+`"`),
	)
	var got []string
	for _, ev := range events {
		if text, ok := ev.(turnevent.TextChunk); ok {
			got = append(got, text.ParentToolCallID)
		}
	}
	if len(got) != 2 || got[0] != outer || got[1] != inner {
		t.Fatalf("TextChunk parent ids = %q, want [%q %q]", got, outer, inner)
	}
}

// TestParser_ParentToolUseID_ReachesBothToolEvents is AC 2's hermetic half: the
// spawned-by id on an assistant line reaches its ToolStart and the one on a user
// line reaches its ToolUpdate, while a main-thread line carries an empty one.
//
// BOTH LINE TYPES IN ONE TABLE, and both directions of the value, because the pair
// is what makes the assertion non-vacuous. A producer that stamped a constant
// passes a table of only-populated rows; one that never read the key passes a table
// of only-null rows. Three distinct id values across the rows likewise: a producer
// that latched the first id it saw would pass a same-value table.
func TestParser_ParentToolUseID_ReachesBothToolEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		line     string
		wantID   string
		wantUser bool
	}{
		{
			name:   "assistant line spawned by an Agent call",
			line:   assistantLineWith(parentToolUseBlock, `"parent_tool_use_id":"`+parentAgentID+`"`),
			wantID: parentAgentID,
		},
		{
			name:   "assistant line on the main thread carries null",
			line:   assistantLineWith(parentToolUseBlock, `"parent_tool_use_id":null`),
			wantID: "",
		},
		{
			name:   "assistant line with the key absent entirely",
			line:   assistantLineWith(parentToolUseBlock),
			wantID: "",
		},
		{
			name:     "user line spawned by an Agent call",
			line:     parentUserLineWith(parentToolResultBlock, `"parent_tool_use_id":"toolu_01OtherAgent"`),
			wantID:   "toolu_01OtherAgent",
			wantUser: true,
		},
		{
			name:     "user line on the main thread carries null",
			line:     parentUserLineWith(parentToolResultBlock, `"parent_tool_use_id":null`),
			wantID:   "",
			wantUser: true,
		},
		{
			name:     "user line with the key absent entirely",
			line:     parentUserLineWith(parentToolResultBlock),
			wantID:   "",
			wantUser: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := parseOneLine(t, tc.line)
			if tc.wantUser {
				if got := toolUpdateFrom(t, events).ParentToolCallID; got != tc.wantID {
					t.Errorf("ToolUpdate.ParentToolCallID = %q, want %q", got, tc.wantID)
				}
				return
			}
			if got := toolStartFrom(t, events).ParentToolCallID; got != tc.wantID {
				t.Errorf("ToolStart.ParentToolCallID = %q, want %q", got, tc.wantID)
			}
		})
	}
}

// TestParser_ParentToolUseID_RejectsEveryNonStringShape drives the converter's whole
// reject set through the parser rather than through the function alone, so the rows
// prove what the EVENT carries and not merely what a helper returns.
//
// Each row also asserts the tool event still arrived. That is the half that matters:
// the safe answer to an unreadable value is "main thread", never "drop the row", and
// a decode that failed the whole line would satisfy an id-only assertion by emitting
// nothing at all.
func TestParser_ParentToolUseID_RejectsEveryNonStringShape(t *testing.T) {
	t.Parallel()
	shapes := []struct {
		name string
		raw  string
	}{
		{"number", `7`},
		{"float", `1.5`},
		{"bool", `true`},
		{"object", `{"id":"toolu_x"}`},
		{"array", `["toolu_x"]`},
		{"empty string", `""`},
	}
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			assistant := assistantLineWith(parentToolUseBlock, `"parent_tool_use_id":`+sh.raw)
			if got := toolStartFrom(t, parseOneLine(t, assistant)).ParentToolCallID; got != "" {
				t.Errorf("assistant: ParentToolCallID = %q, want %q", got, "")
			}
			user := parentUserLineWith(parentToolResultBlock, `"parent_tool_use_id":`+sh.raw)
			if got := toolUpdateFrom(t, parseOneLine(t, user)).ParentToolCallID; got != "" {
				t.Errorf("user: ParentToolCallID = %q, want %q", got, "")
			}
		})
	}
}

// TestParser_ParentToolUseID_BoundDropsRatherThanCuts pins maxTaskFieldID's boundary
// on this field, from both sides.
//
// THE OVER-CAP ROW ASSERTS EMPTY, NOT A PREFIX, and that is the whole judgement: this
// value is a JOIN KEY, so a cut id matches no tool_use_id while still looking like
// one, and a client joining on it could attach a row under the wrong parent. Empty
// means "main thread" — the pre-#2191 rendering, and an honest degradation. A cutting
// implementation passes an "is it shorter" assertion and fails this one.
func TestParser_ParentToolUseID_BoundDropsRatherThanCuts(t *testing.T) {
	t.Parallel()
	atCap := strings.Repeat("a", maxTaskFieldID)
	overCap := strings.Repeat("a", maxTaskFieldID+1)

	// The <= boundary, matching boundStopField's: a value of exactly the cap is
	// carried whole.
	line := assistantLineWith(parentToolUseBlock, `"parent_tool_use_id":"`+atCap+`"`)
	if got := toolStartFrom(t, parseOneLine(t, line)).ParentToolCallID; got != atCap {
		t.Errorf("at the cap: ParentToolCallID length = %d, want %d (value carried whole)",
			len(got), len(atCap))
	}
	line = parentUserLineWith(parentToolResultBlock, `"parent_tool_use_id":"`+overCap+`"`)
	if got := toolUpdateFrom(t, parseOneLine(t, line)).ParentToolCallID; got != "" {
		t.Errorf("over the cap: ParentToolCallID = %q (length %d), want %q — the value must be "+
			"DROPPED, not cut: a truncated join key matches no tool_use_id while still looking "+
			"like one", got, len(got), "")
	}
}

// TestParser_ParentToolUseID_ReadsInnerDepthVerbatim is AC 3.
//
// SYNTHESIZED ON PURPOSE, and this is the one row in the file for which that is
// permanent. A nesting depth is claude's own choice and is not provokable on demand,
// so a live test for it would SKIP while reporting success — assistantLineWith's
// stated rule. The property under test is the ABSENCE of code: the converter has no
// depth parameter, walks no ancestry and compares against no previously seen id, so
// a line naming an inner Agent call yields the inner id.
//
// Two lines through ONE parser, outer then inner. A shared parser is what makes the
// row mean something: an implementation that latched the first id it saw — the shape
// #2224's error category deliberately DOES have — would return the outer id for the
// second line and redden here, where a fresh parser per line could not tell the two
// designs apart.
func TestParser_ParentToolUseID_ReadsInnerDepthVerbatim(t *testing.T) {
	t.Parallel()
	const outer, inner = "toolu_01OuterAgent", "toolu_01InnerAgent"
	events := parseLines(t,
		assistantLineWith(parentToolUseBlock, `"parent_tool_use_id":"`+outer+`"`),
		assistantLineWith(parentToolUseBlock, `"parent_tool_use_id":"`+inner+`"`),
	)
	var got []string
	for _, ev := range events {
		if ts, ok := ev.(turnevent.ToolStart); ok {
			got = append(got, ts.ParentToolCallID)
		}
	}
	want := []string{outer, inner}
	if len(got) != len(want) {
		t.Fatalf("got %d ToolStart events, want %d (all events: %#v)", len(got), len(want), events)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ToolStart[%d].ParentToolCallID = %q, want %q — the value is read off the "+
				"line verbatim, so a line naming an INNER Agent call carries the inner id",
				i, got[i], want[i])
		}
	}
}

// TestParser_ParentToolUseID_LeavesTheAssistantErrorCategoryIntact proves the
// assistant side's separate decode target earned its second pass, in BOTH directions.
//
// This is assistantErrorLine's own argument turned into a test. Folding this key into
// that struct would put a third value behind one point of failure: a hostile `error`
// would blank the parent id, and a hostile parent id would blank the category. Two
// targets fail independently, so each row below asserts the OTHER value survived.
func TestParser_ParentToolUseID_LeavesTheAssistantErrorCategoryIntact(t *testing.T) {
	t.Parallel()

	// A hostile `error` (an object, where the target declares a string) must not cost
	// the parent id.
	line := assistantLineWith(parentToolUseBlock,
		`"parent_tool_use_id":"`+parentAgentID+`"`, `"error":{"type":"overloaded_error"}`)
	if got := toolStartFrom(t, parseOneLine(t, line)).ParentToolCallID; got != parentAgentID {
		t.Errorf("hostile error: ParentToolCallID = %q, want %q — an unreadable `error` must not "+
			"blank the parent id", got, parentAgentID)
	}

	// And a hostile parent id must not cost the error category, which reaches the wire
	// on the turn's turn_end.
	events := parseLines(t,
		assistantLineWith(parentToolUseBlock, `"parent_tool_use_id":{"nope":1}`, `"error":"rate_limit_error"`),
		cleanResult,
	)
	ends := turnEndsFrom(t, events)
	if len(ends) != 1 {
		t.Fatalf("got %d TurnEnd events, want 1", len(ends))
	}
	if ends[0].ErrorCategory != "rate_limit_error" {
		t.Errorf("hostile parent id: TurnEnd.ErrorCategory = %q, want %q — an unreadable "+
			"parent_tool_use_id must not blank the error category",
			ends[0].ErrorCategory, "rate_limit_error")
	}
}

// TestParser_ParentToolUseID_LeavesTheUserSidecarIntact is the regression the
// json.RawMessage field type exists to prevent, and it is the reason userLine was
// widened rather than joined by a third target.
//
// userLine's fields are chosen so the decode CANNOT FAIL: ToolUseResult is a
// RawMessage accepting any valid JSON value, and the struct's documented failure mode
// is "ul stays zero, the block surfaces". A `string` field for this key would break
// that — a parent_tool_use_id of `7` would fail the WHOLE userLine decode, zeroing
// IsSynthetic with it and resurrecting the harness-prose rows #2087 removed, up to
// and including the 87 KB skill body that ticket names. That is a disclosure
// regression reachable by a value claude controls, so it gets a test rather than a
// comment.
func TestParser_ParentToolUseID_LeavesTheUserSidecarIntact(t *testing.T) {
	t.Parallel()

	// A synthetic line whose parent id is hostile: the text block must STILL be
	// suppressed, so nothing but the tool_result maps.
	line := parentUserLineWith(
		`"message":{"role":"user","content":[`+
			`{"type":"text","text":"harness prose that must not reach the wire"},`+
			`{"tool_use_id":"toolu_child","type":"tool_result","content":"ok"}]}`,
		`"isSynthetic":true`, `"parent_tool_use_id":7`)
	events := parseOneLine(t, line)
	for _, ev := range events {
		if u, ok := ev.(turnevent.Unrecognized); ok {
			t.Errorf("an unreadable parent_tool_use_id resurrected a suppressed harness block: "+
				"Unrecognized{Site: %q, Kind: %q} — userLine's decode must not be failable",
				u.Site, u.Kind)
		}
	}
	if got := toolUpdateFrom(t, events).ParentToolCallID; got != "" {
		t.Errorf("ParentToolCallID = %q, want %q", got, "")
	}

	// And the sidecar's own ResultDetail must survive the same hostile value.
	line = parentUserLineWith(
		`"message":{"role":"user","content":[`+
			`{"tool_use_id":"toolu_child","type":"tool_result","content":"ok"}]}`,
		`"parent_tool_use_id":{"nope":1}`,
		`"tool_use_result":{"type":"text","file":{"filePath":"/tmp/x","content":"a\nb\n","numLines":2,"startLine":1,"totalLines":2}}`)
	if got := toolUpdateFrom(t, parseOneLine(t, line)).ResultDetail; got == "" {
		t.Errorf("ResultDetail = %q, want non-empty — an unreadable parent_tool_use_id must not "+
			"blank the tool_use_result sidecar", got)
	}
}

// TestParser_ParentToolUseID_IsNotFedByToolProgress is AC 2's second sentence, and
// the finding that shaped the whole design.
//
// On a tool_progress line the SAME KEY means something else: the tool call this
// progress frame belongs to. Each heartbeat's own tool_use_id is a synthetic
// `…-heartbeat-N` and its parent_tool_use_id is the real call's id — so a decoder
// that generalised the spawned-by reading to this line type would nest an ordinary
// Bash heartbeat underneath its own Bash row. This is userLine's
// tool_use_result/toolUseResult hazard arriving from the other direction: same key,
// two meanings.
//
// It is proven against the COMMITTED CAPTURE rather than a synthesized line, because
// the overload is a measured fact about claude's wire and a hand-written line would
// merely agree with whoever wrote it. capturedToolProgressLines fatals on zero
// frames, so this cannot pass vacuously.
func TestParser_ParentToolUseID_IsNotFedByToolProgress(t *testing.T) {
	t.Parallel()
	lines := capturedToolProgressLines(t)
	// Non-vacuity of the ROW as well as of the file: the guard means nothing unless at
	// least one captured frame actually carries a non-null value for the key.
	var carrying int
	for _, line := range lines {
		if strings.Contains(string(line), `"parent_tool_use_id":"`) {
			carrying++
		}
	}
	if carrying == 0 {
		t.Fatalf("none of the %d captured tool_progress frames carries a non-null "+
			"parent_tool_use_id, so this guard proves nothing. The overload it exists to pin is "+
			"gone from the capture — re-read consumeToolProgress's census before deleting it",
			len(lines))
	}
	for i, line := range lines {
		for _, ev := range parseOneLine(t, string(line)) {
			switch e := ev.(type) {
			case turnevent.ToolStart:
				t.Errorf("frame %d: a tool_progress line emitted ToolStart{ParentToolCallID: %q} — "+
					"this line type's parent_tool_use_id names the call the heartbeat BELONGS TO, "+
					"not a spawning Agent call, and must never feed the field", i, e.ParentToolCallID)
			case turnevent.ToolUpdate:
				t.Errorf("frame %d: a tool_progress line emitted ToolUpdate{ParentToolCallID: %q} — "+
					"see the ToolStart arm", i, e.ParentToolCallID)
			}
		}
	}
}
