package streamsup

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// assistantLineWith mints one stream-json assistant line from raw key FRAGMENTS,
// spliced in verbatim rather than marshalled from a Go value — stopLineWith's
// choice and for its reason: several rows below need shapes no Go struct can hold
// (an `error` that is a number, one that is null).
//
// THE FIXTURE IS SYNTHESIZED, not taken from a committed capture, and that is a
// deliberate departure from the discipline systemTaskStartedLine and resultLine
// both state. It cannot be otherwise: rate limits, overload, auth failure and
// billing errors are not provokable on demand, so no live run can be made to emit
// one, and a live test for them would skip — reporting success while proving
// nothing. The shape's authority is the published SDK type definition
// (@anthropic-ai/claude-agent-sdk@0.3.263, sdk.d.ts) plus the Claude Code headless
// docs, where `error` is declared a sibling of `message` on SDKAssistantMessage.
// No testdata/ directory is minted here for the same reason: it would divide these
// fixtures from the committed captures under internal/e2e/realclaude/testdata.
func assistantLineWith(fragments ...string) string {
	line := `{"type":"assistant"`
	for _, f := range fragments {
		line += "," + f
	}
	return line + `}`
}

// oneTextBlock is the ordinary assistant message body — one mappable block, so a
// line carrying it emits a TextChunk. Rows that need the block-less case pass
// emptyMessage or omit the message key entirely.
const (
	oneTextBlock = `"message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}]}`
	emptyMessage = `"message":{"id":"m1","role":"assistant","content":[]}`
	cleanResult  = `{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}`
)

// parseLines runs several lines through ONE parser and returns everything it
// emitted, in order. Unlike parseOneLine it deliberately shares the parser across
// the lines: this ticket's whole subject is state that outlives a line, so a
// fresh-parser-per-line helper could not express any of these rows.
func parseLines(t *testing.T, lines ...string) []turnevent.Event {
	t.Helper()
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.Default())
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}
	return events
}

// turnEndsFrom returns every TurnEnd in order, so a multi-turn row can assert one
// turn's category against the next one's without indexing into a mixed slice.
func turnEndsFrom(t *testing.T, events []turnevent.Event) []turnevent.TurnEnd {
	t.Helper()
	var out []turnevent.TurnEnd
	for _, ev := range events {
		if end, ok := ev.(turnevent.TurnEnd); ok {
			out = append(out, end)
		}
	}
	return out
}

// TestParser_AssistantErrorCategory_ReachesTurnEnd is AC 1: a wrapper-level error on
// an assistant line comes out on that turn's turn_end, and a turn with no such line
// reports empty.
//
// Every row carries a DISTINCT category so the table is not vacuous the way a
// same-value table is — a producer that stamped a constant, or that read the wrong
// key, reddens here rather than passing four rows out of six. The two empty rows are
// what make the other four mean something: without them "carries the error" would be
// satisfied by a producer that categorised every turn.
func TestParser_AssistantErrorCategory_ReachesTurnEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// assistant is the assistant line, or "" for a turn that has none at all.
		assistant string
		want      string
	}{
		{
			name:      "rate limited",
			assistant: assistantLineWith(`"error":"rate_limit"`, oneTextBlock),
			want:      "rate_limit",
		},
		{
			name:      "overloaded",
			assistant: assistantLineWith(`"error":"overloaded"`, oneTextBlock),
			want:      "overloaded",
		},
		{
			name:      "account rejected",
			assistant: assistantLineWith(`"error":"account_on_hold"`, oneTextBlock),
			want:      "account_on_hold",
		},
		{
			name:      "authentication failed",
			assistant: assistantLineWith(`"error":"authentication_failed"`, oneTextBlock),
			want:      "authentication_failed",
		},
		{
			name:      "clean assistant line reports empty",
			assistant: assistantLineWith(oneTextBlock),
			want:      "",
		},
		{
			name:      "turn with no assistant line at all reports empty",
			assistant: "",
			want:      "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := []string{cleanResult}
			if tc.assistant != "" {
				lines = []string{tc.assistant, cleanResult}
			}
			ends := turnEndsFrom(t, parseLines(t, lines...))
			if len(ends) != 1 {
				t.Fatalf("turn_end count: got %d, want 1: %#v", len(ends), ends)
			}
			if ends[0].ErrorCategory != tc.want {
				t.Errorf("ErrorCategory: got %q, want %q", ends[0].ErrorCategory, tc.want)
			}
		})
	}
}

// TestParser_AssistantErrorCategory_SurvivesABlocklessLine is the case that decides
// WHERE the value is read. emitAssistant returns early on a nil message and emits
// nothing for a message with no mappable blocks, so a producer that read the wrapper
// key inside that function would miss exactly the error-bearing line carrying no
// blocks — and an assistant line whose API call failed is precisely the line least
// likely to carry content.
//
// It asserts BOTH halves: the category lands, and the line still emits no event of
// its own, so reading the wrapper bought no new event on a path that had none.
func TestParser_AssistantErrorCategory_SurvivesABlocklessLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		assistant string
	}{
		{name: "empty content array", assistant: assistantLineWith(`"error":"rate_limit"`, emptyMessage)},
		{name: "no message key at all", assistant: assistantLineWith(`"error":"rate_limit"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := parseLines(t, tc.assistant, cleanResult)
			if len(events) != 1 {
				t.Fatalf("event count: got %d, want exactly 1 (the turn_end alone): %#v", len(events), events)
			}
			ends := turnEndsFrom(t, events)
			if len(ends) != 1 {
				t.Fatalf("turn_end count: got %d, want 1: %#v", len(ends), ends)
			}
			if ends[0].ErrorCategory != "rate_limit" {
				t.Errorf("ErrorCategory: got %q, want %q", ends[0].ErrorCategory, "rate_limit")
			}
		})
	}
}

// TestParser_AssistantErrorCategory_DoesNotCrossATurnBoundary is AC 2. Both result
// subtypes go through the one reset arm, so both are driven here: a producer that
// cleared only on `success` would leak a cancelled turn's category into the next
// turn and pass half this test.
//
// The third row is the one that catches a producer clearing on the ASSISTANT line
// instead of at the reset point — a second turn carrying no assistant line at all
// still has to report empty.
func TestParser_AssistantErrorCategory_DoesNotCrossATurnBoundary(t *testing.T) {
	t.Parallel()
	const cancelledResult = `{"type":"result","subtype":"error_during_execution"}`
	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{
			name: "clean turn follows a categorised one",
			lines: []string{
				assistantLineWith(`"error":"rate_limit"`, oneTextBlock), cleanResult,
				assistantLineWith(oneTextBlock), cleanResult,
			},
		},
		{
			name: "cancelled turn resets too",
			lines: []string{
				assistantLineWith(`"error":"rate_limit"`, oneTextBlock), cancelledResult,
				assistantLineWith(oneTextBlock), cleanResult,
			},
		},
		{
			name: "second turn carries no assistant line at all",
			lines: []string{
				assistantLineWith(`"error":"rate_limit"`, oneTextBlock), cleanResult,
				cleanResult,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ends := turnEndsFrom(t, parseLines(t, tc.lines...))
			if len(ends) != 2 {
				t.Fatalf("turn_end count: got %d, want 2: %#v", len(ends), ends)
			}
			if ends[0].ErrorCategory != "rate_limit" {
				t.Errorf("first turn ErrorCategory: got %q, want %q", ends[0].ErrorCategory, "rate_limit")
			}
			if ends[1].ErrorCategory != "" {
				t.Errorf("second turn ErrorCategory: got %q, want empty — a category crossed the boundary", ends[1].ErrorCategory)
			}
		})
	}
}

// TestParser_AssistantErrorCategory_LatchesOnEveryAssistantLine pins the LAST-WRITER
// -WINS rule, which is what narrows the residual a crashed turn would otherwise leave
// behind: every assistant line writes the field, an absent `error` key writing empty.
//
// The second row is the residual itself, driven the only way a unit test can — the
// same parser, a turn that never reaches its `result`, then a fresh turn. Without the
// latch that turn_end would carry the dead turn's category, which is a wrong claim
// rather than an early event. The row asserts the narrowing, not an elimination: a
// second turn emitting no assistant line before its result is still exposed, and
// TestParser_AssistantErrorCategory_ResidualSurvivesAResultlessTurn pins that
// honestly rather than leaving it implied.
func TestParser_AssistantErrorCategory_LatchesOnEveryAssistantLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{
			name: "a clean assistant line after an error-bearing one clears it",
			lines: []string{
				assistantLineWith(`"error":"rate_limit"`, oneTextBlock),
				assistantLineWith(oneTextBlock),
				cleanResult,
			},
		},
		{
			name: "a turn that never reached its result does not bleed into the next",
			lines: []string{
				assistantLineWith(`"error":"rate_limit"`, oneTextBlock),
				// No result line: the child died here and streamsup respawned under
				// the same parser.
				assistantLineWith(oneTextBlock),
				cleanResult,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ends := turnEndsFrom(t, parseLines(t, tc.lines...))
			if len(ends) != 1 {
				t.Fatalf("turn_end count: got %d, want 1: %#v", len(ends), ends)
			}
			if ends[0].ErrorCategory != "" {
				t.Errorf("ErrorCategory: got %q, want empty — the latch did not overwrite", ends[0].ErrorCategory)
			}
		})
	}
}

// TestParser_AssistantErrorCategory_ResidualSurvivesAResultlessTurn states the
// limitation the latch does NOT remove, so it is pinned as a decision rather than
// discovered later as a defect: a child that dies after an error-bearing line, whose
// next turn emits no assistant line before its result, still reports the dead turn's
// category.
//
// It is asserted rather than left unwritten because the alternative — a second reset
// point on a child-restart signal — was ruled out by the ticket, and an accepted
// limitation nobody wrote a test for is indistinguishable from one nobody noticed.
// If a later ticket adds that clear, this test is the one that must change, and its
// failure is the notice that the decision moved.
func TestParser_AssistantErrorCategory_ResidualSurvivesAResultlessTurn(t *testing.T) {
	t.Parallel()
	ends := turnEndsFrom(t, parseLines(t,
		assistantLineWith(`"error":"rate_limit"`, oneTextBlock),
		// The child died here; nothing else reaches the parser until the next turn's
		// own result, which carries no assistant line of its own to latch over.
		cleanResult,
	))
	if len(ends) != 1 {
		t.Fatalf("turn_end count: got %d, want 1: %#v", len(ends), ends)
	}
	if ends[0].ErrorCategory != "rate_limit" {
		t.Errorf("ErrorCategory: got %q, want %q — this is the accepted residual, not a pass",
			ends[0].ErrorCategory, "rate_limit")
	}
}

// TestParser_AssistantErrorCategory_CarriesUnknownValuesVerbatim is AC 3's first
// half: the set is OPEN, so a token claude's documented list does not name rides
// through as sent rather than being folded into "unknown" or rejected.
func TestParser_AssistantErrorCategory_CarriesUnknownValuesVerbatim(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"a_category_claude_has_not_shipped_yet",
		"Rate_Limit",
		"unknown",
		"max_output_tokens",
	} {
		t.Run(want, func(t *testing.T) {
			t.Parallel()
			ends := turnEndsFrom(t, parseLines(t,
				assistantLineWith(`"error":"`+want+`"`, oneTextBlock), cleanResult))
			if len(ends) != 1 {
				t.Fatalf("turn_end count: got %d, want 1: %#v", len(ends), ends)
			}
			if ends[0].ErrorCategory != want {
				t.Errorf("ErrorCategory: got %q, want %q carried verbatim", ends[0].ErrorCategory, want)
			}
		})
	}
}

// TestParser_AssistantErrorCategory_FieldCaps is AC 3's second half: the shared bound
// DROPS rather than cuts, so an over-long value arrives empty. The boundary rows sit
// either side of maxTurnEndStopField, which is what makes this a statement about the
// cap rather than about one arbitrary length.
//
// The malformed rows share the table because they share the answer: every shape this
// decode cannot read is one empty category, never a partial one and never a failure
// that disturbs the turn boundary.
func TestParser_AssistantErrorCategory_FieldCaps(t *testing.T) {
	t.Parallel()
	atCap := strings.Repeat("c", maxTurnEndStopField)
	for _, tc := range []struct {
		name     string
		fragment string
		want     string
	}{
		{name: "exactly at the cap is carried", fragment: `"error":"` + atCap + `"`, want: atCap},
		{name: "one byte past the cap drops", fragment: `"error":"` + atCap + `c"`, want: ""},
		{name: "a number is not a category", fragment: `"error":5`, want: ""},
		{name: "null is not a category", fragment: `"error":null`, want: ""},
		{name: "an object is not a category", fragment: `"error":{"type":"rate_limit"}`, want: ""},
		{name: "an empty string stays empty", fragment: `"error":""`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ends := turnEndsFrom(t, parseLines(t,
				assistantLineWith(tc.fragment, oneTextBlock), cleanResult))
			if len(ends) != 1 {
				t.Fatalf("turn_end count: got %d, want 1 — the decode disturbed the turn boundary: %#v", len(ends), ends)
			}
			if ends[0].ErrorCategory != tc.want {
				t.Errorf("ErrorCategory: got %q (len %d), want %q (len %d)",
					ends[0].ErrorCategory, len(ends[0].ErrorCategory), tc.want, len(tc.want))
			}
		})
	}
}

// TestParser_AssistantErrorCategory_IsLoggedContentFree is AC 3's third half: no
// claude-authored byte from this decode reaches a log line. It sweeps every record's
// message and every attribute value, on the CARRY path as well as the malformed one —
// the carry path is the half a drop-rung assertion could not see, and it is where a
// future "categorised turn" Debug would most plausibly be added.
func TestParser_AssistantErrorCategory_IsLoggedContentFree(t *testing.T) {
	t.Parallel()
	const (
		carriedMark    = "ae-carried-2224"
		overCapMark    = "ae-overcap-2224"
		malformedMark  = "ae-malformed-2224"
		undecodableTag = "ae-undecodable-2224"
	)
	rec := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(rec))
	lines := []string{
		assistantLineWith(`"error":"`+carriedMark+`"`, oneTextBlock),
		assistantLineWith(`"error":"`+overCapMark+strings.Repeat("x", maxTurnEndStopField)+`"`, oneTextBlock),
		assistantLineWith(`"error":{"nested":"`+malformedMark+`"}`, oneTextBlock),
		// Not valid JSON at all: the line takes consumeLine's undecodable path, which
		// must not start quoting the offending bytes either.
		`{"type":"assistant","error":"` + undecodableTag,
		cleanResult,
	}
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}
	for _, r := range rec.all() {
		for _, leak := range []string{carriedMark, overCapMark, malformedMark, undecodableTag} {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content (%q): %q", leak, r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content (%q); this decode logs nothing at all",
						r.msg, k, leak)
				}
			}
		}
	}
}

// TestParser_AssistantErrorCategory_LeavesTheStopShapeUnchanged is AC 4 held at the
// cheapest place to catch it: the four fields #2223 and #1075 publish must be
// byte-identical whether or not an error-bearing assistant line preceded the result.
// A producer that bought the new field by moving an old one reddens here.
func TestParser_AssistantErrorCategory_LeavesTheStopShapeUnchanged(t *testing.T) {
	t.Parallel()
	const overflowResult = `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"prompt_too_long"}`
	withError := turnEndsFrom(t, parseLines(t,
		assistantLineWith(`"error":"rate_limit"`, oneTextBlock), overflowResult))
	without := turnEndsFrom(t, parseLines(t,
		assistantLineWith(oneTextBlock), overflowResult))
	if len(withError) != 1 || len(without) != 1 {
		t.Fatalf("turn_end counts: got %d and %d, want 1 each", len(withError), len(without))
	}
	a, b := withError[0], without[0]
	if a.Reason != b.Reason || a.Reason != turnevent.TurnEndReasonEndTurn {
		t.Errorf("Reason: got %q with an error line and %q without, want %q both",
			a.Reason, b.Reason, turnevent.TurnEndReasonEndTurn)
	}
	if a.Outcome != b.Outcome || a.Outcome != "success" {
		t.Errorf("Outcome: got %q and %q, want \"success\" both", a.Outcome, b.Outcome)
	}
	if a.IsError != b.IsError || !a.IsError {
		t.Errorf("IsError: got %v and %v, want true both", a.IsError, b.IsError)
	}
	if a.TerminalReason != b.TerminalReason || a.TerminalReason != "prompt_too_long" {
		t.Errorf("TerminalReason: got %q and %q, want \"prompt_too_long\" both", a.TerminalReason, b.TerminalReason)
	}
	// The one field that must DIFFER, so the assertions above are not passing because
	// both parsers produced the same empty event.
	if a.ErrorCategory != "rate_limit" || b.ErrorCategory != "" {
		t.Errorf("ErrorCategory: got %q with an error line and %q without, want %q and empty",
			a.ErrorCategory, b.ErrorCategory, "rate_limit")
	}
}
