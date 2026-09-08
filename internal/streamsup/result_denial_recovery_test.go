package streamsup

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// #2234 — the `result` line's permission_denials[] recovered as markers when no
// system/permission_denied line announced them.
//
// The lines here are HAND-BUILT, under the division of labour
// parser_permission_denied_test.go states: claude's own bytes are replayed in
// permission_denial_capture_test.go, which is where AC 2's direction is proven
// against the four committed arms. What no capture contains is the recovery ITSELF
// under this package's reach — the family that exercises it, bypass_approval_argv,
// has no reader here and a fifth one is out of scope — so AC 1 is necessarily
// authored, and the capture's measured three-key entry shape is what keeps these
// rows honest about the shape they are authored in.

// denialsResultLine mints one result line carrying the given permission_denials
// FRAGMENT verbatim, spliced as raw JSON rather than marshalled from a Go value.
// resultLineWith's reason applies here for a second field: the isolation rows below
// need shapes no Go type can hold — a bare number, an object, an array of numbers —
// and a fixture built through encoding/json could not express them. An empty
// fragment omits the key entirely, which is claude's shape on a turn that denied
// nothing.
//
// extra is spliced in the same way, so one row can carry a hostile modelUsage
// beside a valid denials array and assert that neither reaches the other.
func denialsResultLine(denials string, extra ...string) string {
	line := `{"type":"result","subtype":"success"`
	if denials != "" {
		line += `,"permission_denials":` + denials
	}
	for _, fragment := range extra {
		line += "," + fragment
	}
	return line + `}`
}

// denialEntries builds a well-formed permission_denials array from name/id pairs,
// in the order given. Order is the caller's because the ARRAY has one and the
// recovery preserves it — claude's order, truncated from the tail, never re-ranked.
func denialEntries(pairs ...string) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString(",")
		}
		name, _ := json.Marshal(pairs[i])
		id, _ := json.Marshal(pairs[i+1])
		fmt.Fprintf(&b, `{"tool_name":%s,"tool_use_id":%s}`, name, id)
	}
	b.WriteString("]")
	return b.String()
}

// feedLines runs every line through ONE parser, in order, and returns everything it
// emitted. A shared parser is the point rather than a convenience: the whole slice
// is about state carried from a permission_denied line to the result line that ends
// the same turn, and a fresh parser per line could not express it.
func feedLines(t *testing.T, lines ...string) []turnevent.Event {
	t.Helper()
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write(%s) err = %v, want nil", line, err)
		}
	}
	return events
}

// deniedEvents returns just the ToolCallDenied events, in emitted order.
func deniedEvents(events []turnevent.Event) []turnevent.ToolCallDenied {
	var out []turnevent.ToolCallDenied
	for _, ev := range events {
		if denial, ok := ev.(turnevent.ToolCallDenied); ok {
			out = append(out, denial)
		}
	}
	return out
}

// TestParser_RecoversDenialTheResultLineAloneReports is AC 1: an entry naming an id
// no permission_denied line reported produces one marker, and it goes out BEFORE
// the turn's end event.
//
// The ORDER is the acceptance criterion's real content, not a detail of it. A
// client closes the turn on turn_end, so a marker emitted after it arrives for a
// turn the client has already finished — which is the defect restored under a
// different name. The index comparison below is what makes that executable.
func TestParser_RecoversDenialTheResultLineAloneReports(t *testing.T) {
	t.Parallel()

	events := feedLines(t, denialsResultLine(denialEntries("Bash", "toolu_01Recovered")))

	denials := deniedEvents(events)
	if len(denials) != 1 {
		t.Fatalf("got %d ToolCallDenied events, want exactly 1 recovered from the result line", len(denials))
	}
	got := denials[0]
	if got.ToolName != "Bash" || got.ToolCallID != "toolu_01Recovered" {
		t.Errorf("recovered marker carries %q/%q, want claude's own %q/%q",
			got.ToolName, got.ToolCallID, "Bash", "toolu_01Recovered")
	}
	// AC 1's other half: the entry carries no prose and NONE IS SYNTHESIZED. A
	// recovered marker that invented a message would be the daemon speaking in
	// claude's voice about a denial claude described nowhere.
	if got.Message != "" || got.DecisionReasonType != "" || got.DecisionReason != "" {
		t.Errorf("recovered marker carries prose %q/%q/%q, want all three empty — the "+
			"result entry says nothing about why, and nothing may be synthesized",
			got.Message, got.DecisionReasonType, got.DecisionReason)
	}
	if got.TruncatedFields != nil || got.DroppedFields != nil {
		t.Errorf("recovered marker reports %v/%v, want nil/nil — nothing here is near a cap",
			got.TruncatedFields, got.DroppedFields)
	}

	markerAt, turnEndAt := -1, -1
	for i, ev := range events {
		switch ev.(type) {
		case turnevent.ToolCallDenied:
			markerAt = i
		case turnevent.TurnEnd:
			turnEndAt = i
		}
	}
	if turnEndAt < 0 {
		t.Fatalf("the result line emitted no TurnEnd; events = %#v", events)
	}
	if markerAt > turnEndAt {
		t.Errorf("the recovered marker landed at index %d, AFTER the TurnEnd at %d — a client "+
			"that closes the turn on that event never sees it", markerAt, turnEndAt)
	}
}

// TestParser_AnnouncedDenialRecoversNoSecondMarker is AC 2: an id that already
// produced a marker from its own line produces no second one when the result line
// names it again.
//
// The captured arms carry BOTH shapes for every denial, so this is the ordinary
// path rather than an edge — without the suppression, every denial on a
// line-bearing posture would be reported twice.
func TestParser_AnnouncedDenialRecoversNoSecondMarker(t *testing.T) {
	t.Parallel()

	const id = "toolu_01Announced"
	line := denialLineFixture(t, map[string]any{
		"tool_name":            "Bash",
		"tool_use_id":          id,
		"message":              "sandbox refused this call",
		"decision_reason_type": "rule",
	})
	events := feedLines(t, line, denialsResultLine(denialEntries("Bash", id)))

	denials := deniedEvents(events)
	if len(denials) != 1 {
		t.Fatalf("got %d ToolCallDenied events, want exactly 1 — the line's, with the result "+
			"entry for the same id suppressed", len(denials))
	}
	// The surviving marker must be the LINE's, not a recovered one: claude's prose is
	// what a recovered marker cannot have, so this assertion also proves which of the
	// two paths won.
	if denials[0].Message != "sandbox refused this call" || denials[0].DecisionReasonType != "rule" {
		t.Errorf("the surviving marker carries %q/%q, want the LINE's prose — the recovery "+
			"appears to have displaced it rather than been suppressed by it",
			denials[0].Message, denials[0].DecisionReasonType)
	}
}

// TestParser_ResultDenialsCannotDisturbTheResultsOtherFields is AC 3: the denials
// array is read in an unmarshal independent of resultLine and resultStopLine, so
// none of the three can suppress either of the others and none can disturb
// segmentation.
//
// Three directions, each a row, because a single decode target would fail all three
// at once and any one of them passing alone proves nothing about the other two.
func TestParser_ResultDenialsCannotDisturbTheResultsOtherFields(t *testing.T) {
	t.Parallel()

	const id = "toolu_01Isolated"
	validDenials := denialEntries("Bash", id)

	tests := []struct {
		name        string
		line        string
		wantDenials int
		wantWindows int
		wantReason  string
	}{{
		// A modelUsage shape that fails resultLine's unmarshal must not cost the
		// denials the daemon could otherwise recover.
		name: "hostile modelUsage suppresses no denial",
		line: denialsResultLine(validDenials,
			`"modelUsage":[1,2,3]`, `"terminal_reason":"stopped"`),
		wantDenials: 1, wantWindows: 0, wantReason: "stopped",
	}, {
		// And the converse: a denials shape that fails ITS unmarshal must not blank
		// terminal_reason or evict a window claude reported.
		name: "hostile denials disturbs neither windows nor stop shape",
		line: denialsResultLine(`{"not":"an array"}`,
			`"modelUsage":{"sonnet":{"contextWindow":200000}}`, `"terminal_reason":"stopped"`),
		wantDenials: 0, wantWindows: 1, wantReason: "stopped",
	}, {
		// An array of hostile ELEMENTS is a third shape, and it fails the same way:
		// nothing recovered, everything else intact.
		name: "hostile denial elements disturb nothing",
		line: denialsResultLine(`[1,2,3]`,
			`"modelUsage":{"sonnet":{"contextWindow":200000}}`, `"terminal_reason":"stopped"`),
		wantDenials: 0, wantWindows: 1, wantReason: "stopped",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := feedLines(t, tc.line)

			if got := len(deniedEvents(events)); got != tc.wantDenials {
				t.Errorf("got %d ToolCallDenied events, want %d", got, tc.wantDenials)
			}
			// Segmentation is the third direction and the one no field assertion
			// reaches: exactly one turn boundary, whatever any of the three decodes did.
			var ends []turnevent.TurnEnd
			for _, ev := range events {
				if end, ok := ev.(turnevent.TurnEnd); ok {
					ends = append(ends, end)
				}
			}
			if len(ends) != 1 {
				t.Fatalf("got %d TurnEnd events, want exactly 1 — a decode reached segmentation", len(ends))
			}
			if ends[0].TerminalReason != tc.wantReason {
				t.Errorf("TerminalReason = %q, want %q", ends[0].TerminalReason, tc.wantReason)
			}
			if len(ends[0].ModelWindows) != tc.wantWindows {
				t.Errorf("got %d model windows, want %d", len(ends[0].ModelWindows), tc.wantWindows)
			}
		})
	}
}

// TestParser_DenialRecoveryDoesNotSurviveTheTurn is AC 4: the per-turn record of
// which ids already emitted does not reach the next turn.
//
// Two turns through ONE parser, and the second turn names the SAME id the first
// announced. That is the residual's direction made executable rather than argued: a
// stale id does not produce a wrong marker, it SUPPRESSES a genuine one, so a test
// that used a fresh id in turn two would pass on a parser that never cleared the set
// at all.
func TestParser_DenialRecoveryDoesNotSurviveTheTurn(t *testing.T) {
	t.Parallel()

	const id = "toolu_01AcrossTheBoundary"
	announced := denialLineFixture(t, map[string]any{
		"tool_name": "Bash", "tool_use_id": id, "message": "turn one refused this",
	})

	events := feedLines(t,
		// Turn one: announced by its own line, then named again in the result. One
		// marker, the line's.
		announced,
		denialsResultLine(denialEntries("Bash", id)),
		// Turn two: the SAME id, in the result only. The set was cleared at turn one's
		// boundary, so this must recover.
		denialsResultLine(denialEntries("Bash", id)),
	)

	denials := deniedEvents(events)
	if len(denials) != 2 {
		t.Fatalf("got %d ToolCallDenied events across two turns, want 2 — turn one's line and "+
			"turn two's recovery. A residual id from turn one suppresses the second", len(denials))
	}
	if denials[0].Message != "turn one refused this" {
		t.Errorf("turn one's marker carries %q, want the line's prose", denials[0].Message)
	}
	if denials[1].Message != "" || denials[1].ToolCallID != id {
		t.Errorf("turn two's marker carries %q/%q, want the recovered shape: this id and no prose",
			denials[1].ToolCallID, denials[1].Message)
	}
}

// TestParser_ResultDenialsBounds covers what the daemon refuses to trust about an
// array claude controls: its length, the length of each value in it, and the size
// of the per-turn set that answers "already reported".
func TestParser_ResultDenialsBounds(t *testing.T) {
	t.Parallel()

	t.Run("an over-cap array is cut from the tail", func(t *testing.T) {
		t.Parallel()
		var pairs []string
		for i := 0; i < maxTurnDenials+3; i++ {
			pairs = append(pairs, "Bash", fmt.Sprintf("toolu_01Entry%02d", i))
		}
		denials := deniedEvents(feedLines(t, denialsResultLine(denialEntries(pairs...))))
		if len(denials) != maxTurnDenials {
			t.Fatalf("got %d markers, want maxTurnDenials (%d) — an array length claude "+
				"controls is not a count the daemon should trust", len(denials), maxTurnDenials)
		}
		// From the TAIL: claude's order is preserved because no ranking is invented.
		if denials[0].ToolCallID != "toolu_01Entry00" {
			t.Errorf("first surviving marker is %q, want the array's first entry — the cut "+
				"must come off the tail", denials[0].ToolCallID)
		}
	})

	t.Run("an entry with no usable id is dropped", func(t *testing.T) {
		t.Parallel()
		overCap := strings.Repeat("i", maxTaskFieldID+1)
		line := denialsResultLine(denialEntries(
			"Bash", "",
			"Bash", overCap,
			"Bash", "toolu_01Usable",
		))
		denials := deniedEvents(feedLines(t, line))
		if len(denials) != 1 {
			t.Fatalf("got %d markers, want 1 — an entry whose id is empty or over-cap joins to "+
				"nothing, and this marker carries no prose to stand on instead", len(denials))
		}
		if denials[0].ToolCallID != "toolu_01Usable" {
			t.Errorf("surviving marker carries id %q, want the one usable entry's", denials[0].ToolCallID)
		}
	})

	t.Run("an over-cap tool_name is dropped and reported", func(t *testing.T) {
		t.Parallel()
		line := denialsResultLine(denialEntries(
			strings.Repeat("n", maxTaskFieldID+1), "toolu_01LongName"))
		denials := deniedEvents(feedLines(t, line))
		if len(denials) != 1 {
			t.Fatalf("got %d markers, want 1 — an over-cap name costs the field, never the marker",
				len(denials))
		}
		if denials[0].ToolName != "" {
			t.Errorf("ToolName = %d bytes, want empty — a cut name matches no tool while still "+
				"looking like one", len(denials[0].ToolName))
		}
		if len(denials[0].DroppedFields) != 1 || denials[0].DroppedFields[0] != "tool_name" {
			t.Errorf("DroppedFields = %v, want [tool_name] — an emptied field the consumer cannot "+
				"otherwise tell from one claude never sent", denials[0].DroppedFields)
		}
	})

	t.Run("a full per-turn set suppresses recovery", func(t *testing.T) {
		t.Parallel()
		var lines []string
		for i := 0; i < maxTurnDenials; i++ {
			lines = append(lines, denialLineFixture(t, map[string]any{
				"tool_name": "Bash", "tool_use_id": fmt.Sprintf("toolu_01Announced%02d", i),
				"message": "refused",
			}))
		}
		// One more denial, in the result only. The set is at its cap, so it no longer
		// proves it holds every announced id — recovering would risk a SECOND marker for
		// a call already reported, so the recovery goes silent instead.
		lines = append(lines, denialsResultLine(denialEntries("Bash", "toolu_01Unannounced")))

		denials := deniedEvents(feedLines(t, lines...))
		if len(denials) != maxTurnDenials {
			t.Fatalf("got %d markers, want %d — the %d announced lines and nothing recovered "+
				"past a full set", len(denials), maxTurnDenials, maxTurnDenials)
		}
	})
}
