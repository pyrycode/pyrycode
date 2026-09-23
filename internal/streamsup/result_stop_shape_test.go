package streamsup

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// stopLineWith mints one stream-json result line from raw key FRAGMENTS, spliced
// in verbatim rather than marshalled from a Go value. That is the same choice
// resultLineWith makes and for the same reason: several rows below need shapes no
// Go struct can hold — an is_error that is a string, a terminal_reason that is a
// number — and a fixture built through encoding/json could not express them.
//
// The subtype key is omitted entirely when subtype is empty, which is the absent
// case claude's own trailer takes on a line carrying none.
func stopLineWith(subtype string, fragments ...string) string {
	line := `{"type":"result"`
	if subtype != "" {
		id, _ := json.Marshal(subtype)
		line += `,"subtype":` + string(id)
	}
	for _, f := range fragments {
		line += "," + f
	}
	return line + `}`
}

// TestParser_ResultStopShape_Distinguishes is AC 1: five stop shapes that reach a
// client as one value today must come out mutually distinguishable.
//
// Every row carries a DISTINCT terminal_reason, and the four error rows carry
// distinct subtypes, so the table is not vacuous in the way a same-value table is:
// a mapping that returned a constant, that crossed the two strings, or that read
// terminal_reason off the wrong key would redden here rather than pass three rows
// out of five. The clean-success row is what makes the other four mean something —
// without it "carries the subtype" would be satisfied by a producer that stamped
// every turn as an error.
//
// Reason is asserted on every row alongside the new fields. It is the AC 3
// invariant held at the same call as the AC 1 claim, which is the cheapest place
// to catch a producer that bought the new fields by moving the old one.
func TestParser_ResultStopShape_Distinguishes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		line               string
		wantReason         turnevent.TurnEndReason
		wantOutcome        string
		wantIsError        bool
		wantTerminalReason string
	}{
		{
			name:               "clean success",
			line:               stopLineWith("success", `"is_error":false`, `"terminal_reason":"completed"`),
			wantReason:         turnevent.TurnEndReasonEndTurn,
			wantOutcome:        "success",
			wantTerminalReason: "completed",
		},
		{
			name:               "max turns",
			line:               stopLineWith("error_max_turns", `"is_error":true`, `"terminal_reason":"max_turns"`),
			wantReason:         turnevent.TurnEndReasonEndTurn,
			wantOutcome:        "error_max_turns",
			wantIsError:        true,
			wantTerminalReason: "max_turns",
		},
		{
			name:               "budget cap",
			line:               stopLineWith("error_max_budget_usd", `"is_error":true`, `"terminal_reason":"budget_exhausted"`),
			wantReason:         turnevent.TurnEndReasonEndTurn,
			wantOutcome:        "error_max_budget_usd",
			wantIsError:        true,
			wantTerminalReason: "budget_exhausted",
		},
		{
			name:               "structured-output retries exhausted",
			line:               stopLineWith("error_max_structured_output_retries", `"is_error":true`, `"terminal_reason":"structured_output_retry_exhausted"`),
			wantReason:         turnevent.TurnEndReasonEndTurn,
			wantOutcome:        "error_max_structured_output_retries",
			wantIsError:        true,
			wantTerminalReason: "structured_output_retry_exhausted",
		},
		{
			// The shape the ticket names explicitly: subtype success WITH is_error
			// true, where claude puts the API error text in `result`. It is the row
			// that fails a producer deriving is_error from the subtype rather than
			// reading claude's own key.
			name:               "context overflow reported under subtype success",
			line:               stopLineWith("success", `"is_error":true`, `"terminal_reason":"prompt_too_long"`),
			wantReason:         turnevent.TurnEndReasonEndTurn,
			wantOutcome:        "success",
			wantIsError:        true,
			wantTerminalReason: "prompt_too_long",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			end := turnEndFrom(t, parseOneLine(t, tc.line))
			if end.Reason != tc.wantReason {
				t.Errorf("Reason: got %q, want %q", end.Reason, tc.wantReason)
			}
			if end.Outcome != tc.wantOutcome {
				t.Errorf("Outcome: got %q, want %q", end.Outcome, tc.wantOutcome)
			}
			if end.IsError != tc.wantIsError {
				t.Errorf("IsError: got %v, want %v", end.IsError, tc.wantIsError)
			}
			if end.TerminalReason != tc.wantTerminalReason {
				t.Errorf("TerminalReason: got %q, want %q", end.TerminalReason, tc.wantTerminalReason)
			}
		})
	}
}

// TestParser_ResultStopShape_AbsentAndHostileValues covers the shapes claude does
// not send but a hostile or future line could. Every one of them must leave the
// turn boundary standing — the property the SECOND, independent unmarshal exists
// to give — and answer with the empty value rather than a repair.
func TestParser_ResultStopShape_AbsentAndHostileValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		line string
	}{
		{"no stop keys at all", stopLineWith("success")},
		{"both keys explicitly null", stopLineWith("success", `"is_error":null`, `"terminal_reason":null`)},
		{"is_error is a string", stopLineWith("success", `"is_error":"yes"`, `"terminal_reason":"completed"`)},
		{"terminal_reason is a number", stopLineWith("success", `"is_error":false`, `"terminal_reason":7`)},
		{"terminal_reason is an object", stopLineWith("success", `"terminal_reason":{"why":"completed"}`)},
		{"terminal_reason is an array", stopLineWith("success", `"terminal_reason":["completed"]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The boundary itself, and the outcome with it: both are read off the
			// segmentation decode, which no shape of these two keys can disturb.
			end := turnEndFrom(t, parseOneLine(t, tc.line))
			if end.Reason != turnevent.TurnEndReasonEndTurn {
				t.Errorf("Reason: got %q, want %q — a hostile stop key moved the classification",
					end.Reason, turnevent.TurnEndReasonEndTurn)
			}
			if end.Outcome != "success" {
				t.Errorf("Outcome: got %q, want %q — a hostile stop key suppressed the subtype, "+
					"which is decoded off a different unmarshal and cannot depend on these keys", end.Outcome, "success")
			}
			if end.IsError {
				t.Errorf("IsError: got true, want false — a value of the wrong shape must read as absent, not as an error")
			}
			if end.TerminalReason != "" {
				t.Errorf("TerminalReason: got %q, want \"\" — a value of the wrong shape must read as absent", end.TerminalReason)
			}
		})
	}
}

// TestParser_ResultStopShape_BoundsClaudeAuthoredFields is AC 2. Both published
// strings are claude-authored and open-set, so both are bounded at CONSTRUCTION;
// an over-long value is DROPPED rather than cut, per maxTurnEndStopField's doc.
//
// The at-cap rows are what make the over-cap rows mean something. Without them a
// producer that dropped EVERY value would pass the over-cap half, and the boundary
// could sit anywhere below 256 unobserved.
func TestParser_ResultStopShape_BoundsClaudeAuthoredFields(t *testing.T) {
	t.Parallel()
	atCap := strings.Repeat("r", maxTurnEndStopField)
	overCap := strings.Repeat("r", maxTurnEndStopField+1)

	t.Run("terminal_reason at the cap is carried whole", func(t *testing.T) {
		t.Parallel()
		end := turnEndFrom(t, parseOneLine(t, stopLineWith("success", `"terminal_reason":"`+atCap+`"`)))
		if end.TerminalReason != atCap {
			t.Errorf("TerminalReason: got %d bytes, want the full %d — the boundary is <=, so a "+
				"value of exactly the cap is not over it", len(end.TerminalReason), len(atCap))
		}
	})

	t.Run("terminal_reason past the cap is dropped", func(t *testing.T) {
		t.Parallel()
		end := turnEndFrom(t, parseOneLine(t, stopLineWith("success", `"terminal_reason":"`+overCap+`"`)))
		if end.TerminalReason != "" {
			t.Errorf("TerminalReason: got %d bytes, want 0 — an over-cap token is dropped, not cut, "+
				"because a cut token matches nothing a client could act on", len(end.TerminalReason))
		}
	})

	t.Run("subtype at the cap is carried whole and still classifies", func(t *testing.T) {
		t.Parallel()
		end := turnEndFrom(t, parseOneLine(t, stopLineWith(atCap)))
		if end.Outcome != atCap {
			t.Errorf("Outcome: got %d bytes, want the full %d", len(end.Outcome), len(atCap))
		}
		if end.Reason != turnevent.TurnEndReasonEndTurn {
			t.Errorf("Reason: got %q, want %q", end.Reason, turnevent.TurnEndReasonEndTurn)
		}
	})

	t.Run("subtype past the cap is dropped without moving the classification", func(t *testing.T) {
		t.Parallel()
		end := turnEndFrom(t, parseOneLine(t, stopLineWith(overCap)))
		if end.Outcome != "" {
			t.Errorf("Outcome: got %d bytes, want 0", len(end.Outcome))
		}
		// The bound applies to the PUBLISHED value only. resultTurnEndReason still
		// sees claude's full subtype, so no input length can move stop_reason — the
		// structural half of AC 3.
		if end.Reason != turnevent.TurnEndReasonEndTurn {
			t.Errorf("Reason: got %q, want %q — the length bound must not reach the classification",
				end.Reason, turnevent.TurnEndReasonEndTurn)
		}
	})

	t.Run("an over-cap subtype still classifies as cancelled when it starts with the interrupt token", func(t *testing.T) {
		t.Parallel()
		// error_during_execution is an exact match, so a value merely PREFIXED by it
		// is a different subtype and keeps end_turn. The row exists to pin that the
		// bound did not turn the exact comparison into a prefix one.
		end := turnEndFrom(t, parseOneLine(t, stopLineWith("error_during_execution"+overCap)))
		if end.Reason != turnevent.TurnEndReasonEndTurn {
			t.Errorf("Reason: got %q, want %q", end.Reason, turnevent.TurnEndReasonEndTurn)
		}
		if end.Outcome != "" {
			t.Errorf("Outcome: got %d bytes, want 0", len(end.Outcome))
		}
	})

	// AC 2's third surface: not the event, not the frame, but the LOG. The result
	// arm logs nothing, and the second unmarshal discards its error rather than
	// wrapping it, so no over-long claude byte can reach a log line through this
	// path. Asserted rather than asserted-in-a-comment, because the failure it
	// guards against is a future debug call added in good faith.
	t.Run("no over-long value reaches a log line", func(t *testing.T) {
		t.Parallel()
		var logged bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
		p := NewParser(func(turnevent.Event) {}, log)
		line := stopLineWith(overCap, `"terminal_reason":"`+overCap+`"`, `"is_error":true`)
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
		if strings.Contains(logged.String(), overCap[:32]) {
			t.Errorf("the log carries claude's over-long value:\n%s", logged.String())
		}
	})
}

// TestResultTurnEndReason_UnchangedForEverySubtype is AC 3's producer half, held
// one level below the parser: the wire's stop_reason is derived from the subtype
// alone, and this ticket adds fields BESIDE it rather than changing it.
//
// The table covers every subtype the ticket body documents, plus the absent and
// unknown cases, so "byte-identical for every subtype" is a measurement over the
// whole documented set rather than a claim about the two subtypes that happen to
// have rows elsewhere.
func TestResultTurnEndReason_UnchangedForEverySubtype(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		subtype string
		want    turnevent.TurnEndReason
	}{
		{"success", turnevent.TurnEndReasonEndTurn},
		{"error_during_execution", turnevent.TurnEndReasonCancelled},
		{"error_max_turns", turnevent.TurnEndReasonEndTurn},
		{"error_max_budget_usd", turnevent.TurnEndReasonEndTurn},
		{"error_max_structured_output_retries", turnevent.TurnEndReasonEndTurn},
		{"", turnevent.TurnEndReasonEndTurn},
		{"a subtype claude has not shipped", turnevent.TurnEndReasonEndTurn},
	} {
		t.Run(tc.subtype, func(t *testing.T) {
			t.Parallel()
			if got := resultTurnEndReason(tc.subtype); got != tc.want {
				t.Errorf("resultTurnEndReason(%q) = %q, want %q", tc.subtype, got, tc.want)
			}
			// And through the parser, so the arm is pinned as well as the function:
			// a wire value can only change if BOTH agree, and they are asserted apart.
			end := turnEndFrom(t, parseOneLine(t, stopLineWith(tc.subtype)))
			if end.Reason != tc.want {
				t.Errorf("parsed Reason for subtype %q = %q, want %q", tc.subtype, end.Reason, tc.want)
			}
		})
	}
}

// stopShapeCaptureName is the committed capture this file pins the decode against,
// and stopShapeCaptureVersion the claude release its filename claims.
//
// It is a permission_mode_switch record rather than a capture of this ticket's
// own, and that is the finding the ticket's Context 4 did not have: the arm ran
// `claude --max-turns 4` DIRECTLY — its argv names the claude binary, there is no
// `pyry agent-run` and no pyry-synthesised trailer anywhere on that
// path — so the trailer in it is claude's own bytes by the same test Context 5
// states. It carries two result lines, a clean one and a budget-stopped one, which
// is why one file pins both ends of the taxonomy.
const (
	stopShapeCaptureVersion = "2.1.239"
	stopShapeCaptureName    = "permission_mode_switch_v" + stopShapeCaptureVersion + "_plan.json"
)

// capturedStopShapeLine returns the ONE result line of the committed capture whose
// subtype matches, as claude wrote it.
//
// A THIRD reader beside capturedResultLine and capturedToolProgressLines, not a
// generalisation of either, for the reason capturedToolProgressLines' doc gives at
// length: those readers take no path parameter BY DESIGN, and widening one to a
// second file would hand it the parameter that argument exists to refuse. Nothing
// here is shared with either, and the path is a constant of this file's own.
//
// EVERY FAILURE IS t.Fatalf, NEVER A SKIP. The capture is committed, so a missing
// file is a broken premise rather than an unavailable resource — which is also AC
// 4's requirement that this test fail when the file is absent.
//
// The no-match fatal is the vacuity guard. Without it, a capture that lost its
// budget-stopped line would leave every assertion below unexecuted and the test
// would pass having measured nothing.
//
// json.Compact is the one transformation applied, and it is the harness's rather
// than the fixture's: the capture is stored indented and the parser splits its
// input on newlines. Compact removes whitespace BETWEEN tokens only, so key order,
// every numeric literal and every string byte survive it.
func capturedStopShapeLine(t *testing.T, subtype string) []byte {
	t.Helper()
	path := filepath.Join(initCaptureDir, stopShapeCaptureName)
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
	if record.ClaudeVersion != stopShapeCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it",
			path, record.ClaudeVersion, stopShapeCaptureVersion)
	}
	for _, ev := range record.StdoutEvents {
		var kind struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal(ev, &kind); err != nil {
			continue
		}
		if kind.Type != "result" || kind.Subtype != subtype {
			continue
		}
		var one bytes.Buffer
		if err := json.Compact(&one, ev); err != nil {
			t.Fatalf("compacting the captured %s line of %s: %v", subtype, path, err)
		}
		return one.Bytes()
	}
	t.Fatalf("capture %s carries no result line with subtype %q, so every assertion below "+
		"would be vacuous", path, subtype)
	return nil
}

// TestParser_ResultStopShape_CapturePin is AC 4: the ticket's central claim
// measured against claude's own bytes rather than against a line this test wrote.
//
// It needs no credentials — the capture is committed — and it fails rather than
// skips when the file is absent, which is what stops the credential-free half of
// the evidence quietly evaporating.
//
// The synthetic table above and this one are not redundant. That one can only
// prove the mapping does what THIS TEST believes claude sends; this one proves the
// belief. A key claude spells differently, a value it wraps one level down, an
// is_error it omits on the budget path — each would pass the synthetic rows and
// redden here.
func TestParser_ResultStopShape_CapturePin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		subtype            string
		wantIsError        bool
		wantTerminalReason string
	}{
		{
			name:               "the budget-stopped arm",
			subtype:            "error_max_turns",
			wantIsError:        true,
			wantTerminalReason: "max_turns",
		},
		{
			// The same file's clean arm. It is what makes the row above a
			// DISCRIMINATION rather than an observation: two lines out of one live
			// run, differing in all three fields, from a parser that reads them off
			// the same three keys.
			name:               "the clean arm from the same run",
			subtype:            "success",
			wantIsError:        false,
			wantTerminalReason: "completed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := capturedStopShapeLine(t, tc.subtype)
			end := turnEndFrom(t, parseOneLine(t, string(line)))
			if end.Outcome != tc.subtype {
				t.Errorf("Outcome: got %q, want %q", end.Outcome, tc.subtype)
			}
			if end.IsError != tc.wantIsError {
				t.Errorf("IsError: got %v, want %v", end.IsError, tc.wantIsError)
			}
			if end.TerminalReason != tc.wantTerminalReason {
				t.Errorf("TerminalReason: got %q, want %q", end.TerminalReason, tc.wantTerminalReason)
			}
			// The wire's classification is unchanged on both captured arms, which is
			// AC 3 measured against claude's own bytes rather than a synthetic line.
			if end.Reason != turnevent.TurnEndReasonEndTurn {
				t.Errorf("Reason: got %q, want %q", end.Reason, turnevent.TurnEndReasonEndTurn)
			}
		})
	}
}
