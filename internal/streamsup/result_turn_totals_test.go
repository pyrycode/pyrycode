package streamsup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// #2260 — the `result` line's duration, turn count and running cost carried on
// turn_end. The line-minting helper is stopLineWith, shared with the #2223 file
// beside this one rather than duplicated: both need raw key FRAGMENTS spliced in
// verbatim, because several rows below feed shapes no Go struct can hold — a
// duration_ms that is a string, a num_turns that is an object.
//
// totalsFragments spells the four keys the ordinary way, so a row that means to
// vary ONE of them says so by overriding that one rather than by restating four.
func totalsFragments(durationMS, durationAPIMS, numTurns, cost string) []string {
	return []string{
		`"duration_ms":` + durationMS,
		`"duration_api_ms":` + durationAPIMS,
		`"num_turns":` + numTurns,
		`"total_cost_usd":` + cost,
	}
}

// TestParser_ResultTurnTotals_Distinguishes is AC 1: four numbers that reach a
// client as nothing at all today must come out mutually distinguishable and
// UNDIFFERENCED.
//
// Every value on the row is distinct and none is derivable from another by any
// arithmetic the daemon could plausibly do — the cost is not a scaling of either
// duration, and no duration is a multiple of the turn count. A producer that
// crossed two keys, read one off another, returned a constant, or subtracted a
// previous line's running total would redden here rather than pass three fields
// out of four.
func TestParser_ResultTurnTotals_Distinguishes(t *testing.T) {
	t.Parallel()
	line := stopLineWith("success", totalsFragments("3718", "4463", "2", "0.028285")...)
	end := turnEndFrom(t, parseOneLine(t, line))

	if end.DurationMS != 3718 {
		t.Errorf("DurationMS: got %d, want %d", end.DurationMS, 3718)
	}
	if end.DurationAPIMS != 4463 {
		t.Errorf("DurationAPIMS: got %d, want %d", end.DurationAPIMS, 4463)
	}
	if end.NumTurns != 2 {
		t.Errorf("NumTurns: got %d, want %d", end.NumTurns, 2)
	}
	if end.CostUSDTotal != 0.028285 {
		t.Errorf("CostUSDTotal: got %v, want %v", end.CostUSDTotal, 0.028285)
	}
	// The turn boundary and its classification are asserted at the same call as the
	// four new fields, which is the cheapest place to catch a producer that bought
	// them by moving something that already worked.
	if end.Reason != turnevent.TurnEndReasonEndTurn {
		t.Errorf("Reason: got %q, want %q", end.Reason, turnevent.TurnEndReasonEndTurn)
	}
	if end.Outcome != "success" {
		t.Errorf("Outcome: got %q, want %q", end.Outcome, "success")
	}
}

// TestParser_ResultTurnTotals_NegativeAndLargeValuesPassThrough is the no-clamp
// posture measured rather than asserted in a doc comment: RateLimitedPayload's
// rule, which the ticket names, and specifically the absence of the
// duration_api_ms <= duration_ms consistency check that would reject 53 of the 57
// observed lines.
//
// The api-under-duration row is the one that matters. It is the SHAPE the four
// field names invite a reader to enforce, and it is a shape claude really sends —
// initialize_control_v2.1.239_after_completed_turn.json's first line reports
// duration_ms 6607 against duration_api_ms 4105 — so a well-meant ordering check
// would silently drop a real reading.
func TestParser_ResultTurnTotals_NegativeAndLargeValuesPassThrough(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		fragment []string
		want     turnevent.TurnEnd
	}{
		{
			name:     "duration_api_ms under duration_ms, as claude really sends",
			fragment: totalsFragments("6607", "4105", "1", "0.0220174"),
			want:     turnevent.TurnEnd{DurationMS: 6607, DurationAPIMS: 4105, NumTurns: 1, CostUSDTotal: 0.0220174},
		},
		{
			// Nothing observed sends a negative, and nothing rejects one either. A
			// signed int is what makes the reading OBSERVABLE rather than wrapped
			// into an enormous positive duration, which is the whole reason the
			// decode target is signed.
			name:     "negatives are published as claude sent them",
			fragment: totalsFragments("-1", "-2", "-3", "-0.5"),
			want:     turnevent.TurnEnd{DurationMS: -1, DurationAPIMS: -2, NumTurns: -3, CostUSDTotal: -0.5},
		},
		{
			// The frame-growth bound is over the TYPE's range, not over claude's
			// input length, so the worst case is reachable from a well-formed line
			// and is only ~20 bytes per int.
			name:     "int64 extremes decode rather than saturate",
			fragment: totalsFragments("9223372036854775807", "-9223372036854775808", "9223372036854775807", "1e308"),
			want: turnevent.TurnEnd{
				DurationMS: 9223372036854775807, DurationAPIMS: -9223372036854775808,
				NumTurns: 9223372036854775807, CostUSDTotal: 1e308,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			end := turnEndFrom(t, parseOneLine(t, stopLineWith("success", tc.fragment...)))
			if end.DurationMS != tc.want.DurationMS || end.DurationAPIMS != tc.want.DurationAPIMS ||
				end.NumTurns != tc.want.NumTurns || end.CostUSDTotal != tc.want.CostUSDTotal {
				t.Errorf("got {%d %d %d %v}, want {%d %d %d %v}",
					end.DurationMS, end.DurationAPIMS, end.NumTurns, end.CostUSDTotal,
					tc.want.DurationMS, tc.want.DurationAPIMS, tc.want.NumTurns, tc.want.CostUSDTotal)
			}
		})
	}
}

// TestParser_ResultTurnTotals_AbsentAndHostileValues is AC 2: an absent, null or
// non-numeric key yields the zero value and disturbs nothing else the line carries.
//
// The stop shape is asserted on EVERY row, not just the hostile ones. That is the
// half of AC 2 a per-field table would miss: the claim is not only that the new
// field reads zero but that #2223's and #2234's fields, and the turn boundary
// itself, are reached through decodes this one cannot touch.
func TestParser_ResultTurnTotals_AbsentAndHostileValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		fragments []string
		// wantZeroed is the whole four-tuple on rows where the decode fails as a
		// unit, which is this target's deliberate posture — see resultTurnTotalsLine.
		wantDurationMS int
		wantNumTurns   int
		wantCost       float64
	}{
		{
			name:      "no keys at all, which is what the fake-claude harness emits",
			fragments: nil,
		},
		{
			name:      "every key null",
			fragments: totalsFragments("null", "null", "null", "null"),
		},
		{
			// null against a scalar is the ONE absent-shaped value encoding/json
			// accepts rather than rejecting, so the siblings survive it. This row
			// proves the collapse is real and not an artifact of the whole decode
			// failing.
			name:           "one key null, the rest good",
			fragments:      totalsFragments("3718", "null", "2", "0.028285"),
			wantDurationMS: 3718,
			wantNumTurns:   2,
			wantCost:       0.028285,
		},
		{
			name:      "duration_ms is a string",
			fragments: totalsFragments(`"fast"`, "4463", "2", "0.028285"),
		},
		{
			name:      "num_turns is an object",
			fragments: totalsFragments("3718", "4463", "{}", "0.028285"),
		},
		{
			name:      "total_cost_usd is an array",
			fragments: totalsFragments("3718", "4463", "2", "[1,2]"),
		},
		{
			name:      "duration_api_ms is a bool",
			fragments: totalsFragments("3718", "true", "2", "0.028285"),
		},
		{
			// A number past float64's range. encoding/json REJECTS it rather than
			// producing +Inf, which is what makes json.Marshal downstream unable to
			// fail on a value this parser holds — there is no NaN or Inf literal in
			// JSON and no path that mints one.
			name:      "total_cost_usd is past float64's range",
			fragments: totalsFragments("3718", "4463", "2", "1e400"),
		},
		{
			// A fractional num_turns. Nothing observed sends one; it is here because
			// it is the shape most likely to appear if claude ever changes the key,
			// and the row pins that the answer is the fail-closed one rather than a
			// silent truncation to 2.
			name:      "num_turns is fractional",
			fragments: totalsFragments("3718", "4463", "2.5", "0.028285"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Every row carries the stop shape and a denials array so the assertions
			// below are about THIS decode failing alone rather than about a line that
			// had nothing else to lose.
			fragments := append([]string{`"is_error":true`, `"terminal_reason":"max_turns"`}, tc.fragments...)
			end := turnEndFrom(t, parseOneLine(t, stopLineWith("error_max_turns", fragments...)))

			if end.DurationMS != tc.wantDurationMS {
				t.Errorf("DurationMS: got %d, want %d", end.DurationMS, tc.wantDurationMS)
			}
			if end.NumTurns != tc.wantNumTurns {
				t.Errorf("NumTurns: got %d, want %d", end.NumTurns, tc.wantNumTurns)
			}
			if end.CostUSDTotal != tc.wantCost {
				t.Errorf("CostUSDTotal: got %v, want %v", end.CostUSDTotal, tc.wantCost)
			}
			// AC 2's second half: the turn boundary, its stop_reason, and the fields
			// #2223 and #2224 publish are untouched by anything this decode did.
			if end.Reason != turnevent.TurnEndReasonEndTurn {
				t.Errorf("Reason: got %q, want %q", end.Reason, turnevent.TurnEndReasonEndTurn)
			}
			if end.Outcome != "error_max_turns" {
				t.Errorf("Outcome: got %q, want %q", end.Outcome, "error_max_turns")
			}
			if !end.IsError {
				t.Errorf("IsError: got false, want true")
			}
			if end.TerminalReason != "max_turns" {
				t.Errorf("TerminalReason: got %q, want %q", end.TerminalReason, "max_turns")
			}
		})
	}
}

// TestParser_ResultTurnTotals_IsolatedFromItsSiblings is the isolation property the
// Technical Notes call non-negotiable, measured in BOTH directions across all four
// decode targets on this one line.
//
// The reverse direction is the one a widened target would fail, and it is the
// reason this ticket added a fourth struct rather than four fields to an existing
// one: a hostile modelUsage or a hostile permission_denials must not zero four
// numbers that decoded perfectly well.
func TestParser_ResultTurnTotals_IsolatedFromItsSiblings(t *testing.T) {
	t.Parallel()
	good := strings.Join(totalsFragments("3718", "4463", "2", "0.028285"), ",")

	t.Run("a hostile modelUsage leaves the four numbers intact", func(t *testing.T) {
		t.Parallel()
		end := turnEndFrom(t, parseOneLine(t, stopLineWith("success",
			`"modelUsage":7`, good)))
		if end.DurationMS != 3718 || end.DurationAPIMS != 4463 || end.NumTurns != 2 || end.CostUSDTotal != 0.028285 {
			t.Errorf("numbers: got {%d %d %d %v}, want {3718 4463 2 0.028285}",
				end.DurationMS, end.DurationAPIMS, end.NumTurns, end.CostUSDTotal)
		}
		if len(end.ModelWindows) != 0 {
			t.Errorf("ModelWindows: got %v, want none — the hostile shape must fail ITS OWN decode", end.ModelWindows)
		}
	})

	t.Run("a hostile permission_denials leaves the four numbers intact", func(t *testing.T) {
		t.Parallel()
		// The fourth target's direction, and the one a widened denials struct would
		// fail. claude controls this key's ELEMENT shape, so a hostile array is a
		// value it can send rather than a hypothetical.
		end := turnEndFrom(t, parseOneLine(t, stopLineWith("success",
			`"permission_denials":7`, good)))
		if end.DurationMS != 3718 || end.DurationAPIMS != 4463 || end.NumTurns != 2 || end.CostUSDTotal != 0.028285 {
			t.Errorf("numbers: got {%d %d %d %v}, want {3718 4463 2 0.028285}",
				end.DurationMS, end.DurationAPIMS, end.NumTurns, end.CostUSDTotal)
		}
	})

	t.Run("a hostile is_error leaves the four numbers intact", func(t *testing.T) {
		t.Parallel()
		end := turnEndFrom(t, parseOneLine(t, stopLineWith("success",
			`"is_error":"yes"`, `"terminal_reason":"completed"`, good)))
		if end.DurationMS != 3718 || end.CostUSDTotal != 0.028285 {
			t.Errorf("numbers: got {%d %v}, want {3718 0.028285}", end.DurationMS, end.CostUSDTotal)
		}
		if end.TerminalReason != "" {
			t.Errorf("TerminalReason: got %q, want empty — the stop target fails as a unit", end.TerminalReason)
		}
	})

	t.Run("a hostile duration_ms leaves the windows and the stop shape intact", func(t *testing.T) {
		t.Parallel()
		end := turnEndFrom(t, parseOneLine(t, stopLineWith("success",
			`"modelUsage":`+modelUsageFragment("claude-sonnet-5", 200000),
			`"is_error":true`, `"terminal_reason":"prompt_too_long"`,
			`"duration_ms":"fast"`, `"total_cost_usd":0.028285`)))
		if end.DurationMS != 0 || end.CostUSDTotal != 0 {
			t.Errorf("numbers: got {%d %v}, want both zero", end.DurationMS, end.CostUSDTotal)
		}
		if len(end.ModelWindows) != 1 || end.ModelWindows[0].WindowTokens != 200000 {
			t.Errorf("ModelWindows: got %v, want one entry at 200000", end.ModelWindows)
		}
		if !end.IsError || end.TerminalReason != "prompt_too_long" {
			t.Errorf("stop shape: got {%v %q}, want {true prompt_too_long}", end.IsError, end.TerminalReason)
		}
	})

	t.Run("a hostile duration_ms leaves a recovered denial standing", func(t *testing.T) {
		t.Parallel()
		// turnEndFrom is deliberately NOT used here: the whole claim is that the
		// line emits a SECOND event beside the boundary, so a helper asserting
		// exactly one would make the row vacuous.
		events := parseOneLine(t, stopLineWith("success",
			`"permission_denials":`+denialEntries("Bash", "toolu_1"),
			`"duration_ms":{}`))
		if len(events) != 2 {
			t.Fatalf("event count: got %d, want a denial marker and a TurnEnd: %#v", len(events), events)
		}
		if _, ok := events[0].(turnevent.ToolCallDenied); !ok {
			t.Errorf("first event: got %T, want turnevent.ToolCallDenied — a failed number decode "+
				"must not suppress the denial the result line reports", events[0])
		}
		end, ok := events[1].(turnevent.TurnEnd)
		if !ok {
			t.Fatalf("second event: got %T, want turnevent.TurnEnd", events[1])
		}
		if end.DurationMS != 0 {
			t.Errorf("DurationMS: got %d, want 0", end.DurationMS)
		}
	})
}

// The two captures this file pins against, and the claude releases their filenames
// claim. They are read for opposite reasons: the first carries three result lines
// from ONE run, which is the whole per-turn/running-total discrimination; the
// second is the only capture in the corpus carrying the observed zeros, and is the
// only one stored in the `frames` shape.
const (
	turnTotalsCaptureVersion = "2.1.239"
	turnTotalsCaptureName    = "bypass_reescalation_v" + turnTotalsCaptureVersion + "_control_bypass.json"
	turnTotalsMaxTurnsName   = "permission_mode_switch_v" + turnTotalsCaptureVersion + "_plan.json"

	turnTotalsZeroVersion = "2.1.259"
	turnTotalsZeroName    = "compaction_v" + turnTotalsZeroVersion + ".json"
)

// capturedResultLines returns EVERY result line of one committed capture, in the
// order claude wrote them, as claude wrote them.
//
// It differs from capturedStopShapeLine beside it in returning all of them rather
// than the first of a subtype, and that is the point rather than a convenience: a
// running total is only observable ACROSS lines, so a helper that returned one
// would make the monotonicity assertions below unwritable.
//
// json.Compact is the one transformation applied, and it is the harness's rather
// than the fixture's: the capture is stored indented and the parser splits its
// input on newlines. Compact removes whitespace BETWEEN tokens only, so key order
// and every numeric literal survive it byte for byte — which matters here more than
// anywhere else in the family, since a round trip through map[string]any would
// re-render every one of these numbers.
func capturedResultLines(t *testing.T, name, wantVersion string) [][]byte {
	t.Helper()
	path := filepath.Join(initCaptureDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading capture %s: %v", path, err)
	}
	var record struct {
		ClaudeVersion string            `json:"claude_version"`
		StdoutEvents  []json.RawMessage `json:"stdout_events"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decoding capture %s: %v", path, err)
	}
	if record.ClaudeVersion != wantVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it",
			path, record.ClaudeVersion, wantVersion)
	}
	var lines [][]byte
	for _, ev := range record.StdoutEvents {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(ev, &kind); err != nil || kind.Type != "result" {
			continue
		}
		var one bytes.Buffer
		if err := json.Compact(&one, ev); err != nil {
			t.Fatalf("compacting a captured result line of %s: %v", path, err)
		}
		lines = append(lines, one.Bytes())
	}
	if len(lines) == 0 {
		t.Fatalf("capture %s carries no result line, so every assertion below would be vacuous", path)
	}
	return lines
}

// TestParser_ResultTurnTotals_CapturePin is AC 1 measured against claude's own
// bytes rather than against a line this file wrote.
//
// It needs no credentials — the captures are committed — and it FATALS rather than
// skips when one is absent, which is what stops the credential-free half of the
// evidence quietly evaporating.
//
// The synthetic table above and this one are not redundant. That one can only prove
// the mapping does what THIS TEST believes claude sends; this one proves the belief.
// A key claude spells differently, a value it wraps one level down, a num_turns it
// reports cumulatively — each would pass the synthetic rows and redden here.
//
// EVERY FLOAT LITERAL BELOW IS COPIED FROM THE CAPTURE BYTE FOR BYTE. Those trailing
// digits are not noise: 0.037524600000000005 is the shortest round-trip spelling of
// that float64, and 0.0375246 parses to a DIFFERENT one, so a tidied literal would
// fail this test for a reason that has nothing to do with the code under it.
func TestParser_ResultTurnTotals_CapturePin(t *testing.T) {
	t.Parallel()
	lines := capturedResultLines(t, turnTotalsCaptureName, turnTotalsCaptureVersion)
	if len(lines) != 3 {
		t.Fatalf("%s: got %d result lines, want the 3 this pin's monotonicity claims rest on",
			turnTotalsCaptureName, len(lines))
	}
	want := []turnevent.TurnEnd{
		{DurationMS: 3718, DurationAPIMS: 4463, NumTurns: 2, CostUSDTotal: 0.028285},
		{DurationMS: 3687, DurationAPIMS: 8097, NumTurns: 2, CostUSDTotal: 0.037524600000000005},
		{DurationMS: 2998, DurationAPIMS: 11048, NumTurns: 2, CostUSDTotal: 0.04710700000000001},
	}
	got := make([]turnevent.TurnEnd, 0, len(lines))
	for _, line := range lines {
		got = append(got, turnEndFrom(t, parseOneLine(t, string(line))))
	}
	for i := range want {
		if got[i].DurationMS != want[i].DurationMS || got[i].DurationAPIMS != want[i].DurationAPIMS ||
			got[i].NumTurns != want[i].NumTurns || got[i].CostUSDTotal != want[i].CostUSDTotal {
			t.Errorf("line %d: got {%d %d %d %v}, want {%d %d %d %v}", i,
				got[i].DurationMS, got[i].DurationAPIMS, got[i].NumTurns, got[i].CostUSDTotal,
				want[i].DurationMS, want[i].DurationAPIMS, want[i].NumTurns, want[i].CostUSDTotal)
		}
	}

	// The four readings, each stated as the property that DISTINGUISHES it from its
	// look-alike. These are the assertions docs/protocol-mobile.md's per-field
	// paragraph rests on, held against the bytes rather than against the prose — a
	// daemon that differenced either running total would pass every equality above
	// on line 0 and fail all three of these.
	if !(got[0].DurationAPIMS < got[1].DurationAPIMS && got[1].DurationAPIMS < got[2].DurationAPIMS) {
		t.Errorf("duration_api_ms must arrive as the RUNNING TOTAL claude sent, strictly rising: got %d, %d, %d",
			got[0].DurationAPIMS, got[1].DurationAPIMS, got[2].DurationAPIMS)
	}
	if !(got[0].CostUSDTotal < got[1].CostUSDTotal && got[1].CostUSDTotal < got[2].CostUSDTotal) {
		t.Errorf("total_cost_usd must arrive as the RUNNING TOTAL claude sent, strictly rising: got %v, %v, %v",
			got[0].CostUSDTotal, got[1].CostUSDTotal, got[2].CostUSDTotal)
	}
	// Non-monotonic, which is what makes it a per-turn reading rather than a total.
	if !(got[2].DurationMS < got[1].DurationMS) {
		t.Errorf("duration_ms is PER TURN and falls across this capture: got %d then %d",
			got[1].DurationMS, got[2].DurationMS)
	}
	// A cumulative counter would read 2, 4, 6 here. Holding at 2 is the evidence.
	if got[0].NumTurns != 2 || got[1].NumTurns != 2 || got[2].NumTurns != 2 {
		t.Errorf("num_turns is PER TURN and holds at 2 across this capture: got %d, %d, %d",
			got[0].NumTurns, got[1].NumTurns, got[2].NumTurns)
	}
	// duration_api_ms exceeding duration_ms on every line of this capture is the
	// observable tell the wire doc has to foreclose, and it is the norm rather than
	// an edge case (53 of the 57 lines in the corpus).
	for i := range got {
		if got[i].DurationAPIMS <= got[i].DurationMS {
			t.Errorf("line %d: duration_api_ms %d must exceed duration_ms %d on this capture",
				i, got[i].DurationAPIMS, got[i].DurationMS)
		}
	}
}

// TestParser_ResultTurnTotals_CapturePinMaxTurns is the second half of AC 1's
// evidence: a num_turns that is not 2, on a line whose subtype is not success.
//
// Without it every captured turn count this file reads is the same number, and
// "carries num_turns" would be satisfied by a producer that stamped 2 on every
// turn — the same vacuity the clean-arm row answers for #2223's stop shape.
func TestParser_ResultTurnTotals_CapturePinMaxTurns(t *testing.T) {
	t.Parallel()
	lines := capturedResultLines(t, turnTotalsMaxTurnsName, turnTotalsCaptureVersion)
	var end turnevent.TurnEnd
	var found bool
	for _, line := range lines {
		candidate := turnEndFrom(t, parseOneLine(t, string(line)))
		if candidate.Outcome == "error_max_turns" {
			end, found = candidate, true
			break
		}
	}
	if !found {
		t.Fatalf("%s carries no error_max_turns result line, so this pin would be vacuous", turnTotalsMaxTurnsName)
	}
	if end.NumTurns != 5 {
		t.Errorf("NumTurns: got %d, want 5", end.NumTurns)
	}
	if end.DurationMS != 24594 || end.DurationAPIMS != 27064 {
		t.Errorf("durations: got {%d %d}, want {24594 27064}", end.DurationMS, end.DurationAPIMS)
	}
	if end.CostUSDTotal != 0.1608898 {
		t.Errorf("CostUSDTotal: got %v, want %v", end.CostUSDTotal, 0.1608898)
	}
}

// TestParser_ResultTurnTotals_CapturePinZeros is AC 3's hardest claim held against
// bytes: a ZERO on this frame is a number claude sent, not a field the daemon could
// not read.
//
// compaction_v2.1.259.json is the only capture in the corpus carrying one, and it
// is stored UNLIKE the other 31 — its lines sit under `frames`, each line's JSON
// inside a string payload, where the rest use `stdout_events`. So capturedResultLines
// cannot see it, and a pin that reused that reader would report "no result line" or,
// worse, silently pass over an empty set. Reading the odd shape is what makes this
// evidence exist at all.
//
// The row is discriminating rather than incidental: duration_api_ms and num_turns
// read 0 on the SAME line where duration_ms is 15617 and the cost is non-zero, so a
// producer that zeroed the four as a unit — the fail-closed path every hostile row
// above exercises — cannot reach this state.
func TestParser_ResultTurnTotals_CapturePinZeros(t *testing.T) {
	t.Parallel()
	path := filepath.Join(initCaptureDir, turnTotalsZeroName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading capture %s: %v", path, err)
	}
	var record struct {
		ClaudeVersion string `json:"claude_version"`
		Frames        []struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decoding capture %s: %v", path, err)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on
	// the leading token — compactionCapture's rule for the same file.
	if got, _, _ := strings.Cut(record.ClaudeVersion, " "); got != turnTotalsZeroVersion {
		t.Fatalf("%s: claude_version is %q, want %q", path, record.ClaudeVersion, turnTotalsZeroVersion)
	}

	var found bool
	for _, f := range record.Frames {
		if f.Type != "result" {
			continue
		}
		found = true
		end := turnEndFrom(t, parseOneLine(t, f.Payload))
		if end.DurationAPIMS != 0 {
			t.Errorf("DurationAPIMS: got %d, want the 0 claude sent", end.DurationAPIMS)
		}
		if end.NumTurns != 0 {
			t.Errorf("NumTurns: got %d, want the 0 claude sent", end.NumTurns)
		}
		if end.DurationMS != 15617 {
			t.Errorf("DurationMS: got %d, want 15617 — the zeros beside it are claude's, "+
				"so the whole target must not have failed", end.DurationMS)
		}
		if end.CostUSDTotal != 0.0408803 {
			t.Errorf("CostUSDTotal: got %v, want %v", end.CostUSDTotal, 0.0408803)
		}
	}
	if !found {
		t.Fatalf("%s carries no result frame, so this pin would be vacuous", path)
	}
}
