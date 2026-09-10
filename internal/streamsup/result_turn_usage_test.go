package streamsup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func usageFragment(input, output, cacheRead, cacheCreation string) string {
	return `"usage":{"input_tokens":` + input +
		`,"output_tokens":` + output +
		`,"cache_read_input_tokens":` + cacheRead +
		`,"cache_creation_input_tokens":` + cacheCreation + `}`
}

func assertTurnUsage(t *testing.T, got turnevent.TurnEnd, input, output, cacheRead, cacheCreation int) {
	t.Helper()
	if got.InputTokens != input || got.OutputTokens != output ||
		got.CacheReadTokens != cacheRead || got.CacheCreationTokens != cacheCreation {
		t.Errorf("usage: got {%d %d %d %d}, want {%d %d %d %d}",
			got.InputTokens, got.OutputTokens, got.CacheReadTokens, got.CacheCreationTokens,
			input, output, cacheRead, cacheCreation)
	}
}

func lastTurnEnd(t *testing.T, events []turnevent.Event) turnevent.TurnEnd {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("event count: got 0, want a TurnEnd")
	}
	end, ok := events[len(events)-1].(turnevent.TurnEnd)
	if !ok {
		t.Fatalf("last event: got %T, want turnevent.TurnEnd", events[len(events)-1])
	}
	return end
}

func TestParser_ResultTurnUsage_DistinguishesAndPreservesSignedCounts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                    string
		input, output, cacheRead, cacheCreation int
	}{
		{name: "capture-shaped distinct values", input: 8, output: 1715, cacheRead: 196771, cacheCreation: 12211},
		{name: "negative values pass through", input: -1, output: -2, cacheRead: -3, cacheCreation: -4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fragment := usageFragment(
				jsonNumber(tc.input), jsonNumber(tc.output),
				jsonNumber(tc.cacheRead), jsonNumber(tc.cacheCreation),
			)
			end := turnEndFrom(t, parseOneLine(t, stopLineWith("success", fragment)))
			assertTurnUsage(t, end, tc.input, tc.output, tc.cacheRead, tc.cacheCreation)
			if end.Reason != turnevent.TurnEndReasonEndTurn || end.Outcome != "success" {
				t.Errorf("turn boundary: got reason=%q outcome=%q", end.Reason, end.Outcome)
			}
		})
	}
}

func jsonNumber(n int) string {
	return strconv.Itoa(n)
}

func TestParser_ResultTurnUsage_AbsentNullAndHostileShapes(t *testing.T) {
	t.Parallel()
	goodTotals := totalsFragments("3718", "4463", "2", "0.028285")
	for _, tc := range []struct {
		name  string
		usage string
		want  [4]int
	}{
		{name: "absent usage"},
		{name: "null usage", usage: `"usage":null`},
		{name: "numeric usage", usage: `"usage":7`},
		{name: "array usage", usage: `"usage":[]`},
		{name: "wrong-typed count", usage: usageFragment("8", `"many"`, "196771", "12211")},
		{name: "one absent count preserves siblings", usage: `"usage":{"input_tokens":8,"output_tokens":1715,"cache_read_input_tokens":196771}`, want: [4]int{8, 1715, 196771, 0}},
		{name: "one null count preserves siblings", usage: usageFragment("8", "1715", "null", "12211"), want: [4]int{8, 1715, 0, 12211}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fragments := append([]string{}, goodTotals...)
			fragments = append(fragments,
				`"is_error":true`, `"terminal_reason":"max_turns"`,
				`"modelUsage":{"claude-sonnet":{"contextWindow":1000000}}`,
				`"permission_denials":[{"tool_name":"Bash","tool_use_id":"tool-1"}]`)
			if tc.usage != "" {
				fragments = append(fragments, tc.usage)
			}
			events := parseOneLine(t, stopLineWith("error_max_turns", fragments...))
			end := lastTurnEnd(t, events)
			assertTurnUsage(t, end, tc.want[0], tc.want[1], tc.want[2], tc.want[3])
			if end.DurationMS != 3718 || end.DurationAPIMS != 4463 || end.NumTurns != 2 || end.CostUSDTotal != 0.028285 {
				t.Errorf("turn totals changed: got {%d %d %d %v}", end.DurationMS, end.DurationAPIMS, end.NumTurns, end.CostUSDTotal)
			}
			if !end.IsError || end.TerminalReason != "max_turns" || len(end.ModelWindows) != 1 {
				t.Errorf("sibling result shapes changed: %+v", end)
			}
			if len(events) != 2 {
				t.Errorf("events: got %d, want denial plus TurnEnd", len(events))
			} else if _, ok := events[0].(turnevent.ToolCallDenied); !ok {
				t.Errorf("first event: got %T, want turnevent.ToolCallDenied", events[0])
			}
		})
	}
}

func TestParser_ResultTurnUsage_IsolatedFromHostileSiblings(t *testing.T) {
	t.Parallel()
	good := usageFragment("8", "1715", "196771", "12211")
	for _, tc := range []struct {
		name    string
		sibling string
	}{
		{name: "hostile modelUsage", sibling: `"modelUsage":7`},
		{name: "hostile permission_denials", sibling: `"permission_denials":7`},
		{name: "hostile stop shape", sibling: `"is_error":"yes"`},
		{name: "hostile turn total", sibling: `"duration_ms":"slow"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			end := turnEndFrom(t, parseOneLine(t, stopLineWith("success", tc.sibling, good)))
			assertTurnUsage(t, end, 8, 1715, 196771, 12211)
		})
	}
}

func TestParser_ResultTurnUsage_CapturePin(t *testing.T) {
	t.Parallel()
	lines := capturedResultLines(t, turnTotalsCaptureName, turnTotalsCaptureVersion)
	want := [][4]int{{18, 197, 62410, 10034}, {18, 248, 73456, 318}, {18, 148, 74164, 704}}
	if len(lines) != len(want) {
		t.Fatalf("result lines: got %d, want %d", len(lines), len(want))
	}
	got := make([]turnevent.TurnEnd, 0, len(lines))
	for i, line := range lines {
		end := turnEndFrom(t, parseOneLine(t, string(line)))
		got = append(got, end)
		assertTurnUsage(t, end, want[i][0], want[i][1], want[i][2], want[i][3])
	}
	if !(got[0].OutputTokens < got[1].OutputTokens && got[1].OutputTokens > got[2].OutputTokens) ||
		!(got[0].CacheCreationTokens > got[1].CacheCreationTokens && got[1].CacheCreationTokens < got[2].CacheCreationTokens) {
		t.Errorf("per-turn counts must remain non-monotonic: got output {%d %d %d}, cache creation {%d %d %d}",
			got[0].OutputTokens, got[1].OutputTokens, got[2].OutputTokens,
			got[0].CacheCreationTokens, got[1].CacheCreationTokens, got[2].CacheCreationTokens)
	}
}

func TestParser_ResultTurnUsage_CapturePinMaxTurns(t *testing.T) {
	t.Parallel()
	for _, line := range capturedResultLines(t, turnTotalsMaxTurnsName, turnTotalsCaptureVersion) {
		end := turnEndFrom(t, parseOneLine(t, string(line)))
		if end.Outcome == "error_max_turns" {
			assertTurnUsage(t, end, 8, 1715, 196771, 12211)
			return
		}
	}
	t.Fatalf("%s carries no error_max_turns result line", turnTotalsMaxTurnsName)
}

func TestParser_ResultTurnUsage_CapturePinZeros(t *testing.T) {
	t.Parallel()
	path := filepath.Join(initCaptureDir, turnTotalsZeroName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading capture %s: %v", path, err)
	}
	var record struct {
		Frames []struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decoding capture %s: %v", path, err)
	}
	for _, frame := range record.Frames {
		if frame.Type == "result" {
			assertTurnUsage(t, turnEndFrom(t, parseOneLine(t, frame.Payload)), 0, 0, 0, 0)
			return
		}
	}
	t.Fatalf("%s carries no result frame", path)
}
