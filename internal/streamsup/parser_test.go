package streamsup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// discardLogger is a logger that drops everything — the parser's diagnostics are
// content-free and irrelevant to the tests' assertions.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// harnessNudgeFixture is claude's no-visible-output self-nudge as a string
// LITERAL, transcribed byte-exact from the #1247 capture (claude 2.1.220, the
// #1240 probe, 3 of 3).
//
// Deliberately NOT harnessNoOutputNudge: a fixture built from the constant it
// validates asserts nothing about the bytes — edit the constant and every test
// here follows it green. This literal is what makes such an edit go RED, which
// is the point of the suppression's whole test set. Every fixture below builds
// on this literal, never on the production constant.
const harnessNudgeFixture = "[Your previous response had no visible output. Please continue and produce a user-visible response.]"

// harnessNudgeDropMsg is the Debug message the drop site emits, as a literal for
// the same reason as harnessNudgeFixture.
const harnessNudgeDropMsg = "streamsup: dropping known harness user block"

// collectEvents feeds one complete line (a trailing newline is appended) to a
// fresh Parser and returns the events it emitted, in order.
func collectEvents(line string) []turnevent.Event {
	var got []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
	_, _ = p.Write([]byte(line + "\n"))
	return got
}

// TestParser_LineMapping is the per-line mapping table (AC2): each stdout
// stream-json line shape maps to the expected neutral turnevent.Event stream.
func TestParser_LineMapping(t *testing.T) {
	t.Parallel()
	// driftedNudge is the captured wording with its last word altered. Derived by
	// replacement rather than re-typed so the "one word differs" claim is
	// self-evident and cannot drift into a second transcription error; if the
	// replacement ever stopped matching, this case would collapse into the
	// silent-drop case above it and fail loudly.
	driftedNudge := strings.Replace(harnessNudgeFixture, "user-visible response.]", "user-visible reply.]", 1)
	tests := []struct {
		name string
		line string
		want []turnevent.Event
	}{
		{
			name: "assistant text",
			line: `{"type":"assistant","message":{"id":"msg-1","role":"assistant","content":[{"type":"text","text":"hello world"}]}}`,
			want: []turnevent.Event{turnevent.TextChunk{MessageID: "msg-1", Text: "hello world"}},
		},
		{
			name: "assistant thinking",
			line: `{"type":"assistant","message":{"id":"msg-2","role":"assistant","content":[{"type":"thinking","thinking":"pondering"}]}}`,
			want: []turnevent.Event{turnevent.ThoughtChunk{MessageID: "msg-2", Text: "pondering"}},
		},
		{
			name: "assistant tool_use",
			line: `{"type":"assistant","message":{"id":"msg-3","role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"Read","input":{"file_path":"/tmp/x"}}]}}`,
			want: []turnevent.Event{turnevent.ToolStart{
				ToolCallID: "tu-1",
				Title:      "Read",
				Kind:       turnevent.ToolKindRead,
				RawInput:   json.RawMessage(`{"file_path":"/tmp/x"}`),
			}},
		},
		{
			name: "assistant multi-block preserves order",
			line: `{"type":"assistant","message":{"id":"msg-4","role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"answer"},{"type":"tool_use","id":"tu-2","name":"Bash","input":{"command":"ls"}}]}}`,
			want: []turnevent.Event{
				turnevent.ThoughtChunk{MessageID: "msg-4", Text: "hmm"},
				turnevent.TextChunk{MessageID: "msg-4", Text: "answer"},
				turnevent.ToolStart{ToolCallID: "tu-2", Title: "Bash", Kind: turnevent.ToolKindExecute, RawInput: json.RawMessage(`{"command":"ls"}`)},
			},
		},
		{
			name: "user tool_result string content completed",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"ok","is_error":false}]}}`,
			want: []turnevent.Event{turnevent.ToolUpdate{ToolCallID: "tu-1", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "ok"}}},
		},
		{
			name: "user tool_result array content",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-2","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}],"is_error":false}]}}`,
			want: []turnevent.Event{turnevent.ToolUpdate{ToolCallID: "tu-2", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "line1line2"}}},
		},
		{
			name: "user tool_result is_error failed",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-3","content":"boom","is_error":true}]}}`,
			want: []turnevent.Event{turnevent.ToolUpdate{ToolCallID: "tu-3", Status: turnevent.ToolStatusFailed, Content: turnevent.TextContent{Text: "boom"}}},
		},
		{
			name: "user tool_result empty content is status-only",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-4","content":"","is_error":false}]}}`,
			want: []turnevent.Event{turnevent.ToolUpdate{ToolCallID: "tu-4", Status: turnevent.ToolStatusCompleted, Content: nil}},
		},
		{
			name: "result ends the turn",
			line: `{"type":"result","subtype":"success","session_id":"S"}`,
			want: []turnevent.Event{turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}},
		},
		{
			// The interrupt-terminated turn (spike T1, #1075): claude ends the
			// turn with subtype error_during_execution → cancelled, not end_turn.
			name: "result error_during_execution ends the turn as cancelled",
			line: `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"S"}`,
			want: []turnevent.Event{turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled}},
		},
		{
			// AC4: the mapping is scoped to error_during_execution only — a result
			// with no subtype keeps today's end_turn default.
			name: "result with no subtype defaults to end_turn",
			line: `{"type":"result","session_id":"S"}`,
			want: []turnevent.Event{turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}},
		},
		{
			// AC4: an unknown/other subtype is NOT a general subtype→reason table —
			// only error_during_execution maps to cancelled; everything else end_turn.
			name: "result unknown subtype defaults to end_turn",
			line: `{"type":"result","subtype":"max_tokens","session_id":"S"}`,
			want: []turnevent.Event{turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}},
		},
		{
			name: "system init is a no-op (per-turn marker, not session-open)",
			line: `{"type":"system","subtype":"init","session_id":"S"}`,
			want: nil,
		},
		{
			name: "system thinking_tokens tolerated",
			line: `{"type":"system","subtype":"thinking_tokens","tokens":42}`,
			want: nil,
		},
		{
			name: "system status tolerated",
			line: `{"type":"system","subtype":"status"}`,
			want: nil,
		},
		{
			name: "rate_limit_event tolerated",
			line: `{"type":"rate_limit_event","retry_after":10}`,
			want: nil,
		},
		{
			// Tier two: outside the measured known-ignored list, so it surfaces
			// rather than dying in a debug log the production daemon never prints.
			name: "unknown top-level type surfaces",
			line: `{"type":"some_future_event","payload":{}}`,
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedLineType,
				Kind: "some_future_event",
				Raw:  `{"type":"some_future_event","payload":{}}`,
			}},
		},
		{
			name: "unknown assistant block type surfaces",
			line: `{"type":"assistant","message":{"id":"msg-5","role":"assistant","content":[{"type":"redacted_thinking","data":"…"}]}}`,
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedAssistantBlock,
				Kind: "redacted_thinking",
				Raw:  `{"type":"redacted_thinking","data":"…"}`,
			}},
		},
		{
			// The prompt-echo shape. Slice 0 measured that claude does NOT echo the
			// delivered prompt back as user/text on this surface (it does on the
			// agent-run surface), so if one ever appears it is a real change and
			// must be visible.
			name: "unknown user block type surfaces",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"echoed prompt"}]}}`,
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  `{"type":"text","text":"echoed prompt"}`,
			}},
		},
		{
			// #1247 AC1: the one user/text block the parser drops in silence. It is
			// claude's harness prodding the model after a turn produced no visible
			// output (backgrounding a command is one way to get there) — not the
			// user's words and not the model's, so it must never be rendered as
			// either. Suppression, not mapping.
			name: "harness no-output nudge is dropped in silence",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + harnessNudgeFixture + `"}]}}`,
			want: nil,
		},
		{
			// #1247 AC3: the suppression is scoped to the BLOCK, not the message.
			// The nudge is first on purpose — a message-level guard or an early
			// return would also emit no Unrecognized, and only a sibling block that
			// still maps distinguishes "the nudge was suppressed" from "the message
			// was dropped".
			name: "harness nudge alongside a tool_result still emits the tool_result",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + harnessNudgeFixture + `"},{"type":"tool_result","tool_use_id":"tu-8","content":"ok","is_error":false}]}}`,
			want: []turnevent.Event{turnevent.ToolUpdate{ToolCallID: "tu-8", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "ok"}}},
		},
		{
			// Kills substring matching. The match is byte-exact equality, so text
			// that merely CONTAINS the nudge is a different payload and surfaces.
			name: "harness nudge embedded in longer text still surfaces",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"note: ` + harnessNudgeFixture + `"}]}}`,
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  `{"type":"text","text":"note: ` + harnessNudgeFixture + `"}`,
			}},
		},
		{
			// Kills prefix matching, and is the designed failure direction as an
			// executable assertion: the wording's stability across claude versions
			// is UNMEASURED (one version, one session, three occurrences), so drift
			// must bring the row back rather than stay silently swallowed.
			name: "drifted harness wording still surfaces",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + driftedNudge + `"}]}}`,
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  `{"type":"text","text":"` + driftedNudge + `"}`,
			}},
		},
		{
			// The trust boundary, pinned. A tool_result's payload is routinely
			// attacker-influenceable (it is whatever a file read, a command, or a
			// fetch returned) and decodes into streamBlock.Content, never into Text
			// — so a tool result that IS the nudge cannot reach the guard. Tripping
			// it needs control over the block's TYPE, not just over a string.
			name: "tool_result whose content is the nudge still maps",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-9","content":"` + harnessNudgeFixture + `","is_error":false}]}}`,
			want: []turnevent.Event{turnevent.ToolUpdate{
				ToolCallID: "tu-9",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: harnessNudgeFixture},
			}},
		},
		{
			// The suppression is scoped to emitUser. An assistant text block is
			// model speech; silencing it there would drop what the person came to
			// read, so the same bytes on that surface still map to TextChunk.
			name: "harness nudge text on the assistant surface still maps",
			line: `{"type":"assistant","message":{"id":"msg-9","role":"assistant","content":[{"type":"text","text":"` + harnessNudgeFixture + `"}]}}`,
			want: []turnevent.Event{turnevent.TextChunk{MessageID: "msg-9", Text: harnessNudgeFixture}},
		},
		{
			// A block that will not decode at all: no type is readable, so Kind is
			// empty. Previously this was skipped without recording anything.
			name: "undecodable assistant block surfaces with empty kind",
			line: `{"type":"assistant","message":{"id":"msg-6","role":"assistant","content":["not-an-object"]}}`,
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUndecodable,
				Kind: "",
				Raw:  `"not-an-object"`,
			}},
		},
		{
			// One malformed block must not cost the rest of the message.
			name: "undecodable block does not stop later blocks",
			line: `{"type":"assistant","message":{"id":"msg-7","role":"assistant","content":[42,{"type":"text","text":"still here"}]}}`,
			want: []turnevent.Event{
				turnevent.Unrecognized{Site: turnevent.UnrecognizedUndecodable, Kind: "", Raw: `42`},
				turnevent.TextChunk{MessageID: "msg-7", Text: "still here"},
			},
		},
		{
			// Unknown FIELDS on a known block type are not a gap in our mapping:
			// the block maps fine, so it stays silent. Only an unknown block TYPE
			// surfaces.
			name: "known block with unknown extra fields still maps",
			line: `{"type":"assistant","message":{"id":"msg-8","role":"assistant","content":[{"type":"text","text":"hi","brand_new_field":{"a":1}}]}}`,
			want: []turnevent.Event{turnevent.TextChunk{MessageID: "msg-8", Text: "hi"}},
		},
		{
			name: "assistant with nil message emits nothing",
			line: `{"type":"assistant"}`,
			want: nil,
		},
		{
			name: "user with nil message emits nothing",
			line: `{"type":"user"}`,
			want: nil,
		},
		{
			name: "malformed json surfaces as undecodable",
			line: `{"type":"assistant",`,
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUndecodable,
				Kind: "",
				Raw:  `{"type":"assistant",`,
			}},
		},
		{
			name: "blank line dropped",
			line: `   `,
			want: nil,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := collectEvents(tt.line)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("mapping mismatch:\n got  %#v\n want %#v", got, tt.want)
			}
		})
	}
}

// TestResultTurnEndReason pins the pure subtype→reason mapping (AC3/AC4):
// error_during_execution → cancelled, every other subtype → end_turn.
func TestResultTurnEndReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		subtype string
		want    turnevent.TurnEndReason
	}{
		{"error_during_execution", turnevent.TurnEndReasonCancelled},
		{"success", turnevent.TurnEndReasonEndTurn},
		{"", turnevent.TurnEndReasonEndTurn},
		{"max_tokens", turnevent.TurnEndReasonEndTurn},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.subtype, func(t *testing.T) {
			t.Parallel()
			if got := resultTurnEndReason(tt.subtype); got != tt.want {
				t.Errorf("resultTurnEndReason(%q) = %q, want %q", tt.subtype, got, tt.want)
			}
		})
	}
}

// TestParser_LineBuffering (AC2): bytes split mid-line across Write calls
// reassemble; multiple complete lines in one Write emit in order; a partial
// exceeding maxBuf is dropped and scanning resumes at the next newline.
func TestParser_LineBuffering(t *testing.T) {
	t.Parallel()

	t.Run("split mid-line across writes", func(t *testing.T) {
		t.Parallel()
		var got []turnevent.Event
		p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
		_, _ = p.Write([]byte(`{"type":"res`))
		if len(got) != 0 {
			t.Fatalf("partial line emitted %d events, want 0", len(got))
		}
		_, _ = p.Write([]byte("ult\"}\n"))
		want := []turnevent.Event{turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("reassembled line: got %#v, want %#v", got, want)
		}
	})

	t.Run("multiple lines in one write emit in order", func(t *testing.T) {
		t.Parallel()
		var got []turnevent.Event
		p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
		_, _ = p.Write([]byte(`{"type":"assistant","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n" + `{"type":"result"}` + "\n"))
		want := []turnevent.Event{
			turnevent.TextChunk{MessageID: "m", Text: "hi"},
			turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("multi-line: got %#v, want %#v", got, want)
		}
	})

	t.Run("oversized partial dropped, scanning resumes at next newline", func(t *testing.T) {
		t.Parallel()
		var got []turnevent.Event
		p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
		p.maxBuf = 8
		// A 12-byte partial with no newline exceeds maxBuf: it is dropped, not
		// buffered unbounded and not left to corrupt the next line.
		_, _ = p.Write([]byte("AAAAAAAAAAAA"))
		if len(got) != 0 {
			t.Fatalf("oversized partial emitted %d events, want 0", len(got))
		}
		// A subsequent complete line still parses cleanly.
		_, _ = p.Write([]byte(`{"type":"result"}` + "\n"))
		want := []turnevent.Event{turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("after oversized-drop: got %#v, want %#v", got, want)
		}
	})
}

// TestParser_WriteReturnsFullLen: the parser is the terminal stdout sink, so it
// fully consumes what it is handed — a short return would wedge os/exec's copier.
func TestParser_WriteReturnsFullLen(t *testing.T) {
	t.Parallel()
	p := NewParser(func(turnevent.Event) {}, discardLogger())
	b := []byte(`{"type":"result"}` + "\n" + `{"type":"sys`)
	n, err := p.Write(b)
	if err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if n != len(b) {
		t.Fatalf("Write returned n=%d, want %d (full consume)", n, len(b))
	}
}

// TestParser_UnrecognizedTruncation pins the size cap. It is applied at
// CONSTRUCTION, so an oversized payload never enters the event stream, the push
// queue, or any log — the cap is the only thing between a pathological line and
// the 1 MiB transport frame ceiling.
func TestParser_UnrecognizedTruncation(t *testing.T) {
	t.Parallel()

	t.Run("oversized raw is cut and flagged", func(t *testing.T) {
		t.Parallel()
		// A line comfortably past the cap, of a type outside the ignored list.
		filler := strings.Repeat("x", maxUnrecognizedRaw*2)
		line := `{"type":"some_future_event","blob":"` + filler + `"}`

		got := collectEvents(line)
		if len(got) != 1 {
			t.Fatalf("event count: got %d, want 1", len(got))
		}
		ev, ok := got[0].(turnevent.Unrecognized)
		if !ok {
			t.Fatalf("event type: got %T, want turnevent.Unrecognized", got[0])
		}
		if !ev.Truncated {
			t.Errorf("Truncated: got false, want true")
		}
		if len(ev.Raw) > maxUnrecognizedRaw {
			t.Errorf("Raw length: got %d, want <= %d", len(ev.Raw), maxUnrecognizedRaw)
		}
		// The type is read before truncation, so it survives even when the body
		// does not — that is the field a reader triages on.
		if ev.Kind != "some_future_event" {
			t.Errorf("Kind: got %q, want %q", ev.Kind, "some_future_event")
		}
	})

	t.Run("at-cap raw is not flagged", func(t *testing.T) {
		t.Parallel()
		// Boundary: a payload of exactly maxUnrecognizedRaw bytes must not be
		// reported as truncated (the comparison is <=, not <).
		prefix := `{"type":"some_future_event","blob":"`
		suffix := `"}`
		filler := strings.Repeat("x", maxUnrecognizedRaw-len(prefix)-len(suffix))
		line := prefix + filler + suffix
		if len(line) != maxUnrecognizedRaw {
			t.Fatalf("test setup: line is %d bytes, want %d", len(line), maxUnrecognizedRaw)
		}

		got := collectEvents(line)
		if len(got) != 1 {
			t.Fatalf("event count: got %d, want 1", len(got))
		}
		ev := got[0].(turnevent.Unrecognized)
		if ev.Truncated {
			t.Errorf("Truncated: got true, want false at exactly the cap")
		}
		if ev.Raw != line {
			t.Errorf("Raw: got %d bytes, want the full %d", len(ev.Raw), len(line))
		}
	})

	t.Run("cut mid-rune yields valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// The cap is a BYTE slice, so it can land inside a multi-byte rune. The
		// value rides a JSON string field, where an invalid sequence would be
		// silently replaced downstream, so the producer scrubs it here instead.
		prefix := `{"type":"some_future_event","blob":"`
		// Pad so the cut lands one byte into a 3-byte rune.
		pad := strings.Repeat("x", maxUnrecognizedRaw-len(prefix)-1)
		line := prefix + pad + strings.Repeat("€", 8) + `"}`

		got := collectEvents(line)
		if len(got) != 1 {
			t.Fatalf("event count: got %d, want 1", len(got))
		}
		ev := got[0].(turnevent.Unrecognized)
		if !ev.Truncated {
			t.Fatalf("Truncated: got false, want true")
		}
		if !utf8.ValidString(ev.Raw) {
			t.Errorf("Raw is not valid UTF-8 after a mid-rune cut")
		}
	})
}

// TestParser_IgnoredLineTypesStaySilent is the noise guard, and it is the
// assertion that decides whether this feature is useful or annoying. system/init
// fires once per TURN and system/thinking_tokens roughly ten times per turn
// (slice 0 measurement), so if either surfaced, every conversation would grow a
// row per turn and the signal would be worthless.
//
// Since #1381 every row here is SYNTHESIZED, because that ticket moved the last
// captured system subtype out of this table and into its own mapping test. Do
// not re-point a row at the capture to compensate: capturedSystemLine requires
// exactly one matching record and thinking_tokens has 33, so it would t.Fatalf.
func TestParser_IgnoredLineTypesStaySilent(t *testing.T) {
	t.Parallel()
	lines := []struct {
		name string
		line string
	}{
		{"system/init", `{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5"}`},
		{"system/thinking_tokens", `{"type":"system","subtype":"thinking_tokens","tokens":128}`},
		{"system/status", `{"type":"system","subtype":"status","status":"ok"}`},
		// CORRECTED 2026-08-07 (#1380): `system` maps PER-SUBTYPE now, not
		// wholesale — emitSystemSubtype enumerates the mapped set. Every subtype it
		// does not match still stays silent, which is what this table asserts and
		// why the correction does not weaken it: system is claude's catch-all
		// namespace and its highest-rate emitter, so a new subtype must NOT become a
		// per-turn noise row. See ignoredLineTypes for the full statement.
		//
		// This row is the never-seen subtype, and it is deliberate rather than an
		// oversight: surfacing unknown system subtypes would reintroduce that noise
		// row and would break the live zero-unrecognized gate on the next claude
		// release that adds a chatty one.
		{"system/a_subtype_invented_next_year", `{"type":"system","subtype":"a_subtype_invented_next_year"}`},
		// task_notification is measured ABSENT on this surface — the capture's
		// expected_absent holds exactly it, and it was seen once on the HEADLESS
		// surface only. So it has no captured payload and this row is necessarily
		// synthesized; it invents no field structure beyond the subtype name.
		{"system/task_notification", `{"type":"system","subtype":"task_notification"}`},
		{"rate_limit_event", `{"type":"rate_limit_event","rate_limit":{"status":"allowed"}}`},
	}
	for _, tc := range lines {
		if got := collectEvents(tc.line); got != nil {
			t.Errorf("%s: line %s\n emitted %#v, want silence", tc.name, tc.line, got)
		}
	}
}

// taskStartedCapCheat records the cap values as LITERALS, deliberately not as
// maxTaskFieldID / maxTaskDescription / maxTaskPatch. Same rule as
// harnessNudgeFixture: a fixture built from the constant it validates asserts
// nothing about the number — halve the constant and every row below would follow
// it green. These literals are what make such an edit go RED.
//
// taskPatchCapFixture is a THIRD literal even though it currently equals
// taskDescriptionCapFixture, mirroring the production split: the two bound
// different fields for different reasons, and sharing a fixture would let a
// change to one silently retarget the other's proof.
//
// taskRosterEntriesCapFixture is the first fixture pinning a CARDINALITY rather
// than a byte length (#1381), and the same reasoning carries over unchanged: a
// count fixture written as maxTaskRosterEntries would follow the constant green
// if someone halved it, which is exactly the edit worth catching.
const (
	taskFieldIDCapFixture           = 256
	taskDescriptionCapFixture       = 4096
	taskPatchCapFixture             = 4096
	taskRosterEntriesCapFixture     = 8
	taskRosterDescriptionCapFixture = 512
)

// taskStartedLineFixture builds a system/task_started line from the four mapped
// keys. It invents NO field structure — the keys are exactly the capture's — and
// exists only to vary the VALUES, which is what the cap proof needs and what the
// capture cannot supply: every captured line is small (235 bytes for
// task_started, with a 9-byte description), so nothing in it approaches a cap.
func taskStartedLineFixture(t *testing.T, taskID, toolUseID, description, taskType string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"type":        "system",
		"subtype":     "task_started",
		"task_id":     taskID,
		"tool_use_id": toolUseID,
		"description": description,
		"task_type":   taskType,
	})
	if err != nil {
		t.Fatalf("marshalling task_started fixture: %v", err)
	}
	return string(b)
}

// taskStartedEvent drives one line through the shipped parser and returns the
// single BackgroundTaskStarted it must emit.
func taskStartedEvent(t *testing.T, line string) turnevent.BackgroundTaskStarted {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.BackgroundTaskStarted)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskStarted", got[0])
	}
	return ev
}

// TestParser_TaskStartedMapsFromCapture is #1380's central assertion: the
// CAPTURED system/task_started line becomes one turnevent.BackgroundTaskStarted
// carrying the fields the capture shows.
//
// The field assertions are DERIVED from the capture's own payload rather than
// pinned to literals. The capture is redacted (session_id reads $SESSION_ID and
// description reads `cat $FIFO`), so a pinned expectation would be pinning the
// placeholder rather than a real value; a derived one still catches a field
// swap, which is the failure worth catching. task_type carries the one pinned
// literal, as a canary that the reader picked the right record at all.
func TestParser_TaskStartedMapsFromCapture(t *testing.T) {
	t.Parallel()
	line := capturedSystemLine(t, "task_started")

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}

	ev := taskStartedEvent(t, string(line))

	routes := []struct {
		field     string
		claudeKey string
		got       string
	}{
		{"TaskID", "task_id", ev.TaskID},
		{"ToolCallID", "tool_use_id", ev.ToolCallID},
		{"Description", "description", ev.Description},
		{"TaskType", "task_type", ev.TaskType},
	}
	for _, r := range routes {
		want, _ := payload[r.claudeKey].(string)
		if want == "" {
			t.Fatalf("the capture carries no string %q, so the routing of %s cannot be proven against it", r.claudeKey, r.field)
		}
		if r.got != want {
			t.Errorf("%s: got %q, want the capture's %s = %q", r.field, r.got, r.claudeKey, want)
		}
	}

	// The canary: proves the reader selected the task_started record rather than
	// some other system line that happens to decode into the same shape.
	if ev.TaskType != "local_bash" {
		t.Errorf("TaskType: got %q, want %q (the capture's one observed value)", ev.TaskType, "local_bash")
	}
	if ev.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — the captured line is far under every cap", ev.TruncatedFields)
	}

	// The two deliberate drops (see BackgroundTaskStarted's doc): claude's
	// session_id is NOT the daemon's conversation identity, and uuid has no
	// reader in the daemon.
	//
	// Swept by REFLECTION over every string field on the event rather than over
	// the four routes above, so a field added later to this event is covered
	// without anyone remembering to extend a list.
	// That is what makes the drop an enforced property rather than a comment. Both
	// values are read from the decoded payload rather than pinned, so the
	// assertion stays honest if the capture is ever re-taken unredacted.
	rv := reflect.ValueOf(ev)
	var swept int
	for _, key := range []string{"session_id", "uuid"} {
		v, _ := payload[key].(string)
		if v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() != reflect.String {
				continue
			}
			swept++
			if rv.Field(i).String() == v {
				t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
					rv.Type().Field(i).Name, key, v)
			}
		}
	}
	// The sweep visiting nothing would pass silently, which is the one way this
	// assertion could rot into decoration.
	if swept < len(routes)*2 {
		t.Errorf("the drop sweep visited %d string fields, want at least %d", swept, len(routes)*2)
	}
}

// TestParser_TaskStartedFieldCaps pins the construction-time bound. It is
// applied before the event reaches the sink, so an oversized payload never
// enters the event stream, a queue, or a log — the same ordering
// maxUnrecognizedRaw's cap has, and the only thing that makes the bound real
// rather than cosmetic.
//
// The capture proves the mapping and cannot prove the bound: every captured line
// is far under any cap worth setting. So these lines are SYNTHESIZED, which
// invents no field structure — the keys are the capture's, only the values are
// oversized.
//
// Exact cut lengths are asserted on ASCII fixtures only. The scrub uses an empty
// replacement (truncateRaw's precedent), so a mid-rune cut DELETES the partial
// rune and a multi-byte result is legitimately 1-3 bytes short of the cap; that
// row asserts validity and the ceiling instead.
func TestParser_TaskStartedFieldCaps(t *testing.T) {
	t.Parallel()

	const (
		smallID   = "bybi8g8i8"
		smallTool = "toolu_01ENMNP5P4d3pqjTg9LgCxgZ"
		smallDesc = "cat /tmp/fifo"
		smallType = "local_bash"
	)
	overID := strings.Repeat("i", taskFieldIDCapFixture+1)
	overTool := strings.Repeat("u", taskFieldIDCapFixture*2)
	overDesc := strings.Repeat("d", taskDescriptionCapFixture+1)
	overType := strings.Repeat("k", taskFieldIDCapFixture+64)

	tests := []struct {
		name                                     string
		taskID, toolUseID, description, taskType string
		wantCut                                  []string
		wantLenTaskID                            int
		wantLenToolCallID                        int
		wantLenDescription                       int
		wantLenTaskType                          int
	}{
		{
			name: "description over cap is cut and reported",
			// The report names the field that was cut, and the other three are both
			// intact and unnamed — a cut must not smear across the event.
			taskID: smallID, toolUseID: smallTool, description: overDesc, taskType: smallType,
			wantCut:            []string{"description"},
			wantLenTaskID:      len(smallID),
			wantLenToolCallID:  len(smallTool),
			wantLenDescription: taskDescriptionCapFixture,
			wantLenTaskType:    len(smallType),
		},
		{
			name:   "task_id over cap is cut and reported",
			taskID: overID, toolUseID: smallTool, description: smallDesc, taskType: smallType,
			wantCut:            []string{"task_id"},
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenToolCallID:  len(smallTool),
			wantLenDescription: len(smallDesc),
			wantLenTaskType:    len(smallType),
		},
		{
			// The row that pins the TRANSLATION: claude's key is tool_use_id, and the
			// report names the daemon's field, tool_call_id.
			name:   "tool_use_id over cap is reported as tool_call_id",
			taskID: smallID, toolUseID: overTool, description: smallDesc, taskType: smallType,
			wantCut:            []string{"tool_call_id"},
			wantLenTaskID:      len(smallID),
			wantLenToolCallID:  taskFieldIDCapFixture,
			wantLenDescription: len(smallDesc),
			wantLenTaskType:    len(smallType),
		},
		{
			name:   "task_type over cap is cut and reported",
			taskID: smallID, toolUseID: smallTool, description: smallDesc, taskType: overType,
			wantCut:            []string{"task_type"},
			wantLenTaskID:      len(smallID),
			wantLenToolCallID:  len(smallTool),
			wantLenDescription: len(smallDesc),
			wantLenTaskType:    taskFieldIDCapFixture,
		},
		{
			// Two at once: both named, in DECLARATION order (task_id before
			// description), which is what makes the value deterministic and pinnable.
			name:   "two fields over cap are both named in declaration order",
			taskID: overID, toolUseID: smallTool, description: overDesc, taskType: smallType,
			wantCut:            []string{"task_id", "description"},
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenToolCallID:  len(smallTool),
			wantLenDescription: taskDescriptionCapFixture,
			wantLenTaskType:    len(smallType),
		},
		{
			// The <= boundary: a field of exactly its cap is not truncated, and
			// nothing is reported.
			name:               "every field exactly at its cap is not truncated",
			taskID:             strings.Repeat("i", taskFieldIDCapFixture),
			toolUseID:          strings.Repeat("u", taskFieldIDCapFixture),
			description:        strings.Repeat("d", taskDescriptionCapFixture),
			taskType:           strings.Repeat("k", taskFieldIDCapFixture),
			wantCut:            nil,
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenToolCallID:  taskFieldIDCapFixture,
			wantLenDescription: taskDescriptionCapFixture,
			wantLenTaskType:    taskFieldIDCapFixture,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := taskStartedLineFixture(t, tc.taskID, tc.toolUseID, tc.description, tc.taskType)
			ev := taskStartedEvent(t, line)

			if !reflect.DeepEqual(ev.TruncatedFields, tc.wantCut) {
				t.Errorf("TruncatedFields: got %#v, want %#v", ev.TruncatedFields, tc.wantCut)
			}
			lens := []struct {
				field string
				got   int
				want  int
			}{
				{"TaskID", len(ev.TaskID), tc.wantLenTaskID},
				{"ToolCallID", len(ev.ToolCallID), tc.wantLenToolCallID},
				{"Description", len(ev.Description), tc.wantLenDescription},
				{"TaskType", len(ev.TaskType), tc.wantLenTaskType},
			}
			for _, l := range lens {
				if l.got != l.want {
					t.Errorf("len(%s): got %d, want %d", l.field, l.got, l.want)
				}
			}
		})
	}

	t.Run("a cut mid-rune yields valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// The cap is a BYTE slice, so it can land inside a multi-byte rune. The
		// value rides a JSON string field downstream, where an invalid sequence
		// would be silently replaced, so the producer scrubs it here instead.
		pad := strings.Repeat("d", taskDescriptionCapFixture-1)
		desc := pad + strings.Repeat("€", 8)
		line := taskStartedLineFixture(t, smallID, smallTool, desc, smallType)
		ev := taskStartedEvent(t, line)

		if !reflect.DeepEqual(ev.TruncatedFields, []string{"description"}) {
			t.Fatalf("TruncatedFields: got %#v, want [description]", ev.TruncatedFields)
		}
		if !utf8.ValidString(ev.Description) {
			t.Errorf("Description is not valid UTF-8 after a mid-rune cut")
		}
		// Short of the cap, not at it: the empty replacement DELETES the partial
		// rune rather than replacing it.
		if len(ev.Description) > taskDescriptionCapFixture {
			t.Errorf("len(Description): got %d, want <= %d", len(ev.Description), taskDescriptionCapFixture)
		}
	})
}

// TestParser_TaskStartedDropIsLoggedContentFree is the package's standing rule
// applied to the new subtype: nothing derived from claude's output reaches a
// log. The content crosses the wire, not the log — emitUnrecognized's precedent,
// which records site, type and byte count only.
//
// The sweep over EVERY captured record is the load-bearing part. It catches the
// realistic way this rule gets broken later: someone appending "description",
// d.Description to a Debug line, on either the success path or the
// decode-failure path. Both are driven here, because the malformed line is the
// one whose handler is most tempted to explain itself.
func TestParser_TaskStartedDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	captured := capturedSystemLine(t, "task_started")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedDesc, _ := payload["description"].(string)
	if capturedDesc == "" {
		t.Fatalf("the capture carries no description, so the leak sweep would be vacuous")
	}

	// A task_started line the new shape cannot decode (task_id is a number), so
	// the decode-failure path runs. Its description is a distinctive literal, and
	// the whole point is that it must appear in no log record.
	const malformedDesc = "rm -rf /tmp/never-log-this-command"
	malformed := `{"type":"system","subtype":"task_started","task_id":42,` +
		`"tool_use_id":"toolu_x","description":"` + malformedDesc + `","task_type":"local_bash"}`

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if _, err := p.Write([]byte(malformed + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	// The malformed line is dropped, and NOT as an Unrecognized: keeping `system`
	// whole on ignoredLineTypes is what makes "no system line reaches the
	// unrecognized lane" structural, and that guarantee outranks surfacing a
	// malformed line of a subtype we already know.
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 (the captured line only) — %#v", len(events), events)
	}
	if _, ok := events[0].(turnevent.BackgroundTaskStarted); !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskStarted", events[0])
	}

	for _, r := range rec.all() {
		for _, leak := range []string{capturedDesc, malformedDesc} {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries a claude-derived description: %q", r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries a claude-derived description; this path logs the subtype only", r.msg, k)
				}
			}
		}
	}
}

// taskUpdatedLineFixture builds a system/task_updated line from the two mapped
// keys, taking the patch as RAW JSON so a test can hand it a shape or a size the
// capture does not contain. A nil patch omits the key entirely.
//
// It invents NO field structure: the LINE-level keys are exactly the capture's.
// It claims nothing about the patch's INTERNAL keys either, and that is the
// point rather than a loophole — the mapping carries the patch unparsed, so a
// synthesized multi-key patch asserts the opposite of an invented mapping: that
// keys the daemon knows nothing about survive.
func taskUpdatedLineFixture(t *testing.T, taskID string, patch json.RawMessage) string {
	t.Helper()
	line := map[string]any{
		"type":    "system",
		"subtype": "task_updated",
		"task_id": taskID,
	}
	if patch != nil {
		line["patch"] = patch
	}
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling task_updated fixture: %v", err)
	}
	return string(b)
}

// taskUpdatedEvent drives one line through the shipped parser and returns the
// single BackgroundTaskUpdated it must emit.
func taskUpdatedEvent(t *testing.T, line string) turnevent.BackgroundTaskUpdated {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.BackgroundTaskUpdated)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskUpdated", got[0])
	}
	return ev
}

// TestParser_TaskUpdatedMapsFromCapture is #1382's central assertion: the
// CAPTURED system/task_updated line becomes one turnevent.BackgroundTaskUpdated
// carrying task_id and patch.
//
// This line used to be a row in TestParser_IgnoredLineTypesStaySilent, asserting
// the opposite. Moving it here is the change; #1380 put it there deliberately
// because the subtype was genuinely still dropped between the two tickets.
//
// TaskID is DERIVED from the capture's own payload rather than pinned, for
// TestParser_TaskStartedMapsFromCapture's reason: the capture is redacted
// (session_id reads $SESSION_ID), so a pinned expectation risks pinning a
// placeholder, while a derived one still catches a field swap. Patch carries the
// one pinned literal, as a canary that the reader picked the right record at all.
func TestParser_TaskUpdatedMapsFromCapture(t *testing.T) {
	t.Parallel()
	line := capturedSystemLine(t, "task_updated")

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}

	ev := taskUpdatedEvent(t, string(line))

	routes := []struct {
		field     string
		claudeKey string
		got       string
	}{
		{"TaskID", "task_id", ev.TaskID},
	}
	for _, r := range routes {
		want, _ := payload[r.claudeKey].(string)
		if want == "" {
			t.Fatalf("the capture carries no string %q, so the routing of %s cannot be proven against it", r.claudeKey, r.field)
		}
		if r.got != want {
			t.Errorf("%s: got %q, want the capture's %s = %q", r.field, r.got, r.claudeKey, want)
		}
	}

	// Patch is asserted SEMANTICALLY — re-decoded and compared against the
	// payload's own patch — so the carriage claim does not rest on key ordering,
	// which is not ours to control.
	wantPatch, ok := payload["patch"]
	if !ok {
		t.Fatalf("the capture carries no patch, so the carriage assertion would be vacuous")
	}
	var gotPatch any
	if err := json.Unmarshal([]byte(ev.Patch), &gotPatch); err != nil {
		t.Fatalf("Patch %q does not re-decode as JSON: %v", ev.Patch, err)
	}
	if !reflect.DeepEqual(gotPatch, wantPatch) {
		t.Errorf("Patch: got %#v, want the capture's patch %#v", gotPatch, wantPatch)
	}

	// The canary: proves the reader selected the task_updated record rather than
	// some other system line that happens to decode into the same two-field shape.
	const wantPatchLiteral = `{"is_backgrounded":true}`
	if ev.Patch != wantPatchLiteral {
		t.Errorf("Patch: got %q, want %q (the capture's one observed patch)", ev.Patch, wantPatchLiteral)
	}
	if ev.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — the captured line is 170 bytes on the wire, far under every cap", ev.TruncatedFields)
	}

	// The two deliberate drops (see BackgroundTaskUpdated's doc): claude's
	// session_id is NOT the daemon's conversation identity, and uuid has no reader
	// in the daemon. Neither is even declared on systemTaskUpdatedLine, so this
	// sweep guards the event against a LATER field, which is the failure a fixed
	// list of field names stops catching the moment someone adds one.
	//
	// Swept by REFLECTION over every string field, inherited from
	// TestParser_TaskStartedMapsFromCapture. Both values are read from the decoded
	// payload rather than pinned, so the assertion stays honest if the capture is
	// ever re-taken unredacted.
	rv := reflect.ValueOf(ev)
	var swept int
	for _, key := range []string{"session_id", "uuid"} {
		v, _ := payload[key].(string)
		if v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() != reflect.String {
				continue
			}
			swept++
			if rv.Field(i).String() == v {
				t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
					rv.Type().Field(i).Name, key, v)
			}
		}
	}
	// The sweep visiting nothing would pass silently, which is the one way this
	// assertion could rot into decoration.
	//
	// The floor is a LITERAL, not #1380's len(routes)*2. That expression happened
	// to be exact there because BackgroundTaskStarted has as many routes as string
	// fields (4 and 4); here there is one route and two string fields, so
	// len(routes)*2 would be a floor of 2 against an actual 4 — it would pass with
	// half the sweep missing. 2 string fields x 2 dropped keys = 4. A later ticket
	// adding a field only raises the count; a field LEAVING string kind drops it
	// below the floor and goes red, which is exactly the weakening worth catching.
	const wantSwept = 2 * 2
	if swept < wantSwept {
		t.Errorf("the drop sweep visited %d string fields, want at least %d", swept, wantSwept)
	}
}

// TestParser_TaskUpdatedCarriesPatchWhole proves the clause the capture cannot:
// that patch is carried WHOLE rather than key-enumerated.
//
// The captured patch has exactly one key, so an implementation that declared
// `struct{ IsBackgrounded bool }` and re-marshalled would satisfy every
// capture-derived assertion above. Whole-ness needs a second input — the same
// shape as the ticket's "the capture proves the mapping and cannot prove the
// bound" argument, one level down. These lines are SYNTHESIZED and invent no
// field structure: the line-level keys are the capture's, and the mapping claims
// nothing about the patch's internal keys, which is precisely what is being
// asserted here.
func TestParser_TaskUpdatedCarriesPatchWhole(t *testing.T) {
	t.Parallel()

	const taskID = "bybi8g8i8"

	t.Run("keys claude has never shown survive un-mapped", func(t *testing.T) {
		t.Parallel()
		// A string, a nested object and an array beside the one observed key. A
		// key-enumerating mapping drops all three and goes red here.
		raw := json.RawMessage(`{"is_backgrounded":true,"status":"running",` +
			`"detail":{"exit_code":0,"note":"still going"},"tags":["a","b"]}`)
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, taskID, raw))

		var got, want any
		if err := json.Unmarshal([]byte(ev.Patch), &got); err != nil {
			t.Fatalf("Patch %q does not re-decode as JSON: %v", ev.Patch, err)
		}
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatalf("decoding the fixture patch: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Patch: got %#v, want %#v — every key must survive, including the ones the daemon knows nothing about", got, want)
		}
		if ev.TruncatedFields != nil {
			t.Errorf("TruncatedFields: got %v, want nil", ev.TruncatedFields)
		}
	})

	t.Run("a large integer keeps its digits verbatim", func(t *testing.T) {
		t.Parallel()
		// The row that falsifies a map[string]any decode-and-re-marshal: that
		// implementation rounds every number through float64 and this id loses its
		// low digits. Byte-level, deliberately not DeepEqual, which would compare two
		// equally-rounded float64s and pass.
		const big = "12345678901234567890"
		raw := json.RawMessage(`{"revision":` + big + `}`)
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, taskID, raw))

		if !strings.Contains(ev.Patch, big) {
			t.Errorf("Patch: got %q, want it to carry %s verbatim", ev.Patch, big)
		}
	})

	t.Run("an absent patch lands empty and is not an error", func(t *testing.T) {
		t.Parallel()
		// Absence is claude's to choose; there is no captured negative case, so the
		// field lands empty rather than inventing a validation rule.
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, taskID, nil))

		if ev.Patch != "" {
			t.Errorf("Patch: got %q, want the empty string", ev.Patch)
		}
		if ev.TaskID != taskID {
			t.Errorf("TaskID: got %q, want %q — an absent patch must not disturb the rest", ev.TaskID, taskID)
		}
		if ev.TruncatedFields != nil {
			t.Errorf("TruncatedFields: got %v, want nil", ev.TruncatedFields)
		}
	})

	t.Run("a non-object patch is carried as its serialized text", func(t *testing.T) {
		t.Parallel()
		// Deliberate permissiveness: json.RawMessage takes any valid JSON value, and
		// the cap is what makes that safe rather than a shape check. One observation
		// of an object does not earn a validation rule.
		raw := json.RawMessage(`"backgrounded"`)
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, taskID, raw))

		if ev.Patch != string(raw) {
			t.Errorf("Patch: got %q, want %q", ev.Patch, string(raw))
		}
	})
}

// TestParser_TaskUpdatedFieldCaps pins the construction-time bound, mirroring
// TestParser_TaskStartedFieldCaps. It is applied before the event reaches the
// sink, so an oversized payload never enters the event stream, a queue, or a log.
//
// The capture proves the mapping and cannot prove the bound: the captured line
// is 170 bytes on the wire with a 24-byte patch, against caps of 256 and 4096.
// So these lines are SYNTHESIZED, which invents no field structure — the keys are
// the capture's, only the values are oversized.
func TestParser_TaskUpdatedFieldCaps(t *testing.T) {
	t.Parallel()

	const smallID = "bybi8g8i8"
	// A one-key patch object, split so the byte arithmetic below is visible: the
	// cut has to land INSIDE the string value for the typing proof to mean
	// anything.
	const patchOpen = `{"note":"`
	const patchClose = `"}`
	patchOf := func(value string) json.RawMessage {
		return json.RawMessage(patchOpen + value + patchClose)
	}
	const patchOverhead = len(patchOpen) + len(patchClose)

	smallPatch := patchOf("ok")
	// Comfortably past the cap, and long enough that the cut at taskPatchCapFixture
	// lands inside the string value rather than in the trailing `"}`.
	overPatch := patchOf(strings.Repeat("p", taskPatchCapFixture))
	atPatch := patchOf(strings.Repeat("p", taskPatchCapFixture-patchOverhead))
	overID := strings.Repeat("i", taskFieldIDCapFixture+1)

	tests := []struct {
		name          string
		taskID        string
		patch         json.RawMessage
		wantCut       []string
		wantLenTaskID int
		wantLenPatch  int
		// wantPatchValidJSON is the typing proof, asserted in BOTH directions: an
		// intact patch is valid JSON, and a CUT one is not — which is why Patch is a
		// string and not json.RawMessage. Typing a truncated object as raw JSON
		// would be a lie (turnevent.Unrecognized.Raw's precedent).
		wantPatchValidJSON bool
	}{
		{
			name: "patch over cap is cut and reported",
			// The report names the field that was cut, and task_id is both intact and
			// unnamed — a cut must not smear across the event.
			taskID: smallID, patch: overPatch,
			wantCut:            []string{"patch"},
			wantLenTaskID:      len(smallID),
			wantLenPatch:       taskPatchCapFixture,
			wantPatchValidJSON: false,
		},
		{
			name:   "task_id over cap is cut and reported",
			taskID: overID, patch: smallPatch,
			wantCut:            []string{"task_id"},
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenPatch:       len(smallPatch),
			wantPatchValidJSON: true,
		},
		{
			// Two at once: both named, in DECLARATION order (task_id before patch),
			// which is what makes the value deterministic and pinnable.
			name:   "two fields over cap are both named in declaration order",
			taskID: overID, patch: overPatch,
			wantCut:            []string{"task_id", "patch"},
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenPatch:       taskPatchCapFixture,
			wantPatchValidJSON: false,
		},
		{
			// The <= boundary: a field of exactly its cap is not truncated, and
			// nothing is reported.
			name:   "every field exactly at its cap is not truncated",
			taskID: strings.Repeat("i", taskFieldIDCapFixture), patch: atPatch,
			wantCut:            nil,
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenPatch:       taskPatchCapFixture,
			wantPatchValidJSON: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, tc.taskID, tc.patch))

			if !reflect.DeepEqual(ev.TruncatedFields, tc.wantCut) {
				t.Errorf("TruncatedFields: got %#v, want %#v", ev.TruncatedFields, tc.wantCut)
			}
			if len(ev.TaskID) != tc.wantLenTaskID {
				t.Errorf("len(TaskID): got %d, want %d", len(ev.TaskID), tc.wantLenTaskID)
			}
			if len(ev.Patch) != tc.wantLenPatch {
				t.Errorf("len(Patch): got %d, want %d", len(ev.Patch), tc.wantLenPatch)
			}
			if got := json.Valid([]byte(ev.Patch)); got != tc.wantPatchValidJSON {
				t.Errorf("json.Valid(Patch): got %v, want %v — a truncated patch is a STRING that is no longer valid JSON, which is why the field is not typed json.RawMessage",
					got, tc.wantPatchValidJSON)
			}
		})
	}

	t.Run("a cut mid-rune yields valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// The cap is a BYTE slice, so it can land inside a multi-byte rune. The value
		// rides a JSON string field downstream, where an invalid sequence would be
		// silently replaced, so the producer scrubs it here instead. The padding puts
		// the first multi-byte rune so its bytes straddle the cut.
		pad := strings.Repeat("d", taskPatchCapFixture-len(patchOpen)-1)
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, smallID, patchOf(pad+strings.Repeat("€", 8))))

		if !reflect.DeepEqual(ev.TruncatedFields, []string{"patch"}) {
			t.Fatalf("TruncatedFields: got %#v, want [patch]", ev.TruncatedFields)
		}
		if !utf8.ValidString(ev.Patch) {
			t.Errorf("Patch is not valid UTF-8 after a mid-rune cut")
		}
		// Short of the cap, not at it: the empty replacement DELETES the partial rune
		// rather than replacing it.
		if len(ev.Patch) > taskPatchCapFixture {
			t.Errorf("len(Patch): got %d, want <= %d", len(ev.Patch), taskPatchCapFixture)
		}
	})
}

// TestParser_TaskUpdatedDropIsLoggedContentFree is the package's standing rule
// applied to the second subtype: nothing derived from claude's output reaches a
// log. The content crosses the wire, not the log.
//
// The sweep over EVERY captured record is the load-bearing part, and patch is
// the most attractive thing to log while debugging a malformed line — which is
// why both the success and the decode-failure path are driven here.
func TestParser_TaskUpdatedDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	captured := capturedSystemLine(t, "task_updated")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedSession, _ := payload["session_id"].(string)
	if capturedSession == "" {
		t.Fatalf("the capture carries no session_id, so the leak sweep would be vacuous")
	}

	// A task_updated line the shape cannot decode (task_id is a number), so the
	// decode-failure path runs. Its patch carries a distinctive literal, and the
	// whole point is that it must appear in no log record.
	const malformedPatchValue = "rm -rf /tmp/never-log-this-patch"
	malformed := `{"type":"system","subtype":"task_updated","task_id":42,` +
		`"patch":{"command":"` + malformedPatchValue + `"}}`

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if _, err := p.Write([]byte(malformed + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	// The malformed line is dropped, and NOT as an Unrecognized: keeping `system`
	// whole on ignoredLineTypes is what makes "no system line reaches the
	// unrecognized lane" structural, and that guarantee outranks surfacing a
	// malformed line of a subtype we already know.
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 (the captured line only) — %#v", len(events), events)
	}
	ev, ok := events[0].(turnevent.BackgroundTaskUpdated)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskUpdated", events[0])
	}
	// The captured patch as the parser itself carried it, so the leak candidate is
	// derived from the shipped mapping rather than transcribed.
	if ev.Patch == "" {
		t.Fatalf("the mapped Patch is empty, so the leak sweep over it would be vacuous")
	}

	// The decode-failure path must actually have run, or the half of this sweep
	// that covers it asserts nothing.
	const dropMsg = "streamsup: dropping undecodable system line"
	var sawDrop bool
	for _, r := range rec.all() {
		if r.msg != dropMsg {
			continue
		}
		sawDrop = true
		if !reflect.DeepEqual(r.attrs, map[string]string{"subtype": "task_updated"}) {
			t.Errorf("the undecodable-drop record carries %#v, want the subtype keyword only", r.attrs)
		}
	}
	if !sawDrop {
		t.Fatalf("no %q record: the decode-failure path never ran, so its leak sweep is vacuous", dropMsg)
	}

	for _, r := range rec.all() {
		for _, leak := range []string{ev.Patch, malformedPatchValue, capturedSession} {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content: %q", r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content; this path logs the subtype only", r.msg, k)
				}
			}
		}
	}
}

// rosterEntryFixture is one synthesized tasks[] entry. Its three fields are
// exactly the per-entry keys the capture shows, and nothing invented.
type rosterEntryFixture struct {
	taskID      string
	taskType    string
	description string
}

// backgroundTaskRosterLineFixture builds a system/background_tasks_changed line.
// It invents NO field structure — the line-level key is the capture's `tasks`
// and the per-entry keys are the capture's three — and exists only to vary the
// COUNT and the VALUES, which is what the two-dimension bound proof needs and
// what the capture cannot supply: the captured line is 212 bytes carrying ONE
// entry with a 9-byte description, far under either bound.
//
// A nil entries slice omits the `tasks` key entirely; an empty non-nil slice
// emits `"tasks":[]`. Those are different inputs, and both are asserted.
func backgroundTaskRosterLineFixture(t *testing.T, entries []rosterEntryFixture) string {
	t.Helper()
	line := map[string]any{
		"type":    "system",
		"subtype": "background_tasks_changed",
	}
	if entries != nil {
		tasks := make([]map[string]string, 0, len(entries))
		for _, e := range entries {
			tasks = append(tasks, map[string]string{
				"task_id":     e.taskID,
				"task_type":   e.taskType,
				"description": e.description,
			})
		}
		line["tasks"] = tasks
	}
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling background_tasks_changed fixture: %v", err)
	}
	return string(b)
}

// rosterEntriesFixture builds n entries, each identifiable by its task_id so
// tail-truncation is PINNED rather than assumed.
func rosterEntriesFixture(n int) []rosterEntryFixture {
	out := make([]rosterEntryFixture, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, rosterEntryFixture{
			taskID:      fmt.Sprintf("task-%03d", i),
			taskType:    "local_bash",
			description: fmt.Sprintf("cat /tmp/fifo-%03d", i),
		})
	}
	return out
}

// backgroundTaskRosterEvent drives one line through the shipped parser and
// returns the single BackgroundTaskRoster it must emit.
func backgroundTaskRosterEvent(t *testing.T, line string) turnevent.BackgroundTaskRoster {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.BackgroundTaskRoster)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskRoster", got[0])
	}
	return ev
}

// TestParser_BackgroundTaskRosterMapsFromCapture is #1381's central assertion:
// the CAPTURED system/background_tasks_changed line becomes one
// turnevent.BackgroundTaskRoster carrying the tasks array's entries.
//
// This line used to be a row in TestParser_IgnoredLineTypesStaySilent, asserting
// the opposite. Moving it here is the change; #1380 put it there deliberately
// because the subtype was genuinely still dropped until this ticket, and it was
// the LAST captured system subtype on that table.
//
// The field assertions are DERIVED from the capture's own payload rather than
// pinned, for TestParser_TaskStartedMapsFromCapture's reason: the capture is
// redacted (session_id reads $SESSION_ID and the description reads `cat $FIFO`),
// so a pinned expectation risks pinning a placeholder, while a derived one still
// catches a field swap. task_type carries the one pinned literal, as a canary
// that the reader picked the right record at all.
func TestParser_BackgroundTaskRosterMapsFromCapture(t *testing.T) {
	t.Parallel()
	line := capturedSystemLine(t, "background_tasks_changed")

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	rawTasks, _ := payload["tasks"].([]any)
	if len(rawTasks) != 1 {
		t.Fatalf("the capture's tasks array holds %d entries, want 1", len(rawTasks))
	}
	entry, _ := rawTasks[0].(map[string]any)
	if entry == nil {
		t.Fatalf("the capture's tasks[0] is not an object, so no route can be proven against it")
	}

	ev := backgroundTaskRosterEvent(t, string(line))

	if len(ev.Tasks) != 1 || ev.DroppedTasks != 0 {
		t.Fatalf("roster shape: got %d tasks / %d dropped, want 1 / 0", len(ev.Tasks), ev.DroppedTasks)
	}
	task := ev.Tasks[0]

	routes := []struct {
		field     string
		claudeKey string
		got       string
	}{
		{"TaskID", "task_id", task.TaskID},
		{"TaskType", "task_type", task.TaskType},
		{"Description", "description", task.Description},
	}
	for _, r := range routes {
		want, _ := entry[r.claudeKey].(string)
		if want == "" {
			t.Fatalf("the capture's entry carries no string %q, so the routing of %s cannot be proven against it", r.claudeKey, r.field)
		}
		if r.got != want {
			t.Errorf("%s: got %q, want the capture's %s = %q", r.field, r.got, r.claudeKey, want)
		}
	}

	// The canary: proves the reader selected a background-task record at all
	// rather than some other system line that decodes into the same shape.
	if task.TaskType != "local_bash" {
		t.Errorf("TaskType: got %q, want %q (the capture's one observed value)", task.TaskType, "local_bash")
	}
	if task.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — the captured line is 212 bytes on the wire, far under every cap", task.TruncatedFields)
	}

	// The cross-record JOIN proof. All three captured records describe one task,
	// which is what makes the family docs' repeated "task_id is the join key"
	// claim falsifiable here rather than decorative — this is the only place it is
	// asserted at all.
	var started map[string]any
	if err := json.Unmarshal(capturedSystemLine(t, "task_started"), &started); err != nil {
		t.Fatalf("decoding the captured task_started payload: %v", err)
	}
	startedID, _ := started["task_id"].(string)
	if startedID == "" {
		t.Fatalf("the captured task_started carries no string task_id, so the join assertion would be vacuous")
	}
	if task.TaskID != startedID {
		t.Errorf("TaskID: the roster's %q does not join back to the captured task_started's %q", task.TaskID, startedID)
	}

	// The two deliberate drops (see BackgroundTaskRoster's doc): claude's
	// session_id is NOT the daemon's conversation identity, and uuid has no reader
	// in the daemon. Neither is even declared on systemBackgroundTasksLine, so
	// this sweep guards the event against a LATER field.
	//
	// The TRAVERSAL is what differs from the siblings' sweeps. Top-level
	// reflection visits ZERO string fields here, because every carried value lives
	// inside a []BackgroundTask — so this descends into a slice-of-struct field
	// and counts at both levels. Both values are read from the decoded payload
	// rather than pinned, so the assertion stays honest if the capture is ever
	// re-taken unredacted.
	var swept int
	var visit func(sv reflect.Value, key, v string)
	visit = func(sv reflect.Value, key, v string) {
		for i := 0; i < sv.NumField(); i++ {
			f := sv.Field(i)
			switch {
			case f.Kind() == reflect.String:
				swept++
				if f.String() == v {
					t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
						sv.Type().Field(i).Name, key, v)
				}
			case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.Struct:
				for j := 0; j < f.Len(); j++ {
					visit(f.Index(j), key, v)
				}
			}
		}
	}
	for _, key := range []string{"session_id", "uuid"} {
		v, _ := payload[key].(string)
		if v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
		visit(reflect.ValueOf(ev), key, v)
	}
	// The floor is a LITERAL, following #1382's correction rather than #1380's
	// len(routes)*2: 1 entry x 3 string fields x 2 dropped keys = 6. It is
	// load-bearing in a way neither sibling's was — a top-level-only sweep visits
	// 0 string fields on this event and would otherwise pass in SILENCE, so this
	// number is exactly the guard that catches "the reflection never descended".
	// Re-taking the capture with more entries only raises the count; a field
	// leaving string kind, or the descent regressing, drops below 6 and goes red.
	const wantSwept = 1 * 3 * 2
	if swept < wantSwept {
		t.Errorf("the drop sweep visited %d string fields, want at least %d — a sweep that never descended into Tasks visits none",
			swept, wantSwept)
	}
}

// TestParser_BackgroundTaskRosterBounds pins the construction-time bound in BOTH
// dimensions. It is applied before the event reaches the sink, so an oversized
// payload never enters the event stream, a queue, or a log.
//
// The capture proves the mapping and cannot prove either bound: one entry, 212
// bytes. So these lines are SYNTHESIZED, which invents no field structure — the
// keys are the capture's, only the count and the values are oversized.
//
// The count dimension is the one with no precedent in this package. A per-entry
// text cap alone leaves the event's total size a function of a number claude
// chooses, which is why both halves are asserted here and why the last subtest
// drives them at once.
func TestParser_BackgroundTaskRosterBounds(t *testing.T) {
	t.Parallel()

	const (
		smallID   = "bybi8g8i8"
		smallType = "local_bash"
		smallDesc = "cat /tmp/fifo"
	)

	t.Run("the entry count is bounded and the overflow is reported", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name        string
			entries     []rosterEntryFixture
			wantLen     int
			wantDropped int
		}{
			{
				name:    "one over the cap drops one",
				entries: rosterEntriesFixture(taskRosterEntriesCapFixture + 1),
				wantLen: taskRosterEntriesCapFixture, wantDropped: 1,
			},
			{
				// The <= boundary, matching truncateField's convention.
				name:    "exactly at the cap carries every entry",
				entries: rosterEntriesFixture(taskRosterEntriesCapFixture),
				wantLen: taskRosterEntriesCapFixture, wantDropped: 0,
			},
			{
				// The row that makes the report a COUNT rather than a flag: a flag
				// cannot tell 1 lost from 92, and the roster's true size is only
				// recoverable as len(Tasks) + DroppedTasks.
				name:    "a large roster reports how many were lost",
				entries: rosterEntriesFixture(100),
				wantLen: taskRosterEntriesCapFixture, wantDropped: 100 - taskRosterEntriesCapFixture,
			},
			{
				// An empty roster is MEANINGFUL — it says nothing is alive — so it is
				// still emitted. Dropping it would discard the most informative case.
				name:    "an empty tasks array still emits one event",
				entries: []rosterEntryFixture{},
				wantLen: 0, wantDropped: 0,
			},
			{
				// A DIFFERENT input from the empty array: the key is absent entirely.
				name:    "an absent tasks key still emits one event",
				entries: nil,
				wantLen: 0, wantDropped: 0,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				ev := backgroundTaskRosterEvent(t, backgroundTaskRosterLineFixture(t, tc.entries))

				if len(ev.Tasks) != tc.wantLen {
					t.Fatalf("len(Tasks): got %d, want %d", len(ev.Tasks), tc.wantLen)
				}
				if ev.DroppedTasks != tc.wantDropped {
					t.Errorf("DroppedTasks: got %d, want %d", ev.DroppedTasks, tc.wantDropped)
				}
				if tc.wantLen == 0 && ev.Tasks != nil {
					t.Errorf("Tasks: got %#v, want nil — never an empty non-nil slice", ev.Tasks)
				}
				// Truncation is from the TAIL, preserving claude's order: no ranking is
				// invented, because claude's ordering semantics are unobserved. Pinned
				// per entry rather than assumed from the count.
				for i := range ev.Tasks {
					if ev.Tasks[i].TaskID != tc.entries[i].taskID {
						t.Errorf("Tasks[%d].TaskID: got %q, want %q — the survivors are claude's first %d, in order",
							i, ev.Tasks[i].TaskID, tc.entries[i].taskID, tc.wantLen)
					}
				}
			})
		}
	})

	t.Run("each entry's text is bounded and the cut is reported on that entry", func(t *testing.T) {
		t.Parallel()
		overID := strings.Repeat("i", taskFieldIDCapFixture+1)
		overType := strings.Repeat("k", taskFieldIDCapFixture+64)
		overDesc := strings.Repeat("d", taskRosterDescriptionCapFixture+1)

		tests := []struct {
			name               string
			entry              rosterEntryFixture
			wantCut            []string
			wantLenTaskID      int
			wantLenTaskType    int
			wantLenDescription int
		}{
			{
				// The report names the field that was cut, and the other two are both
				// intact and unnamed — a cut must not smear across the entry.
				name:    "description over cap is cut and reported",
				entry:   rosterEntryFixture{taskID: smallID, taskType: smallType, description: overDesc},
				wantCut: []string{"description"}, wantLenTaskID: len(smallID),
				wantLenTaskType: len(smallType), wantLenDescription: taskRosterDescriptionCapFixture,
			},
			{
				name:    "task_id over cap is cut and reported",
				entry:   rosterEntryFixture{taskID: overID, taskType: smallType, description: smallDesc},
				wantCut: []string{"task_id"}, wantLenTaskID: taskFieldIDCapFixture,
				wantLenTaskType: len(smallType), wantLenDescription: len(smallDesc),
			},
			{
				name:    "task_type over cap is cut and reported",
				entry:   rosterEntryFixture{taskID: smallID, taskType: overType, description: smallDesc},
				wantCut: []string{"task_type"}, wantLenTaskID: len(smallID),
				wantLenTaskType: taskFieldIDCapFixture, wantLenDescription: len(smallDesc),
			},
			{
				// All three at once, named in DECLARATION order, which is what makes the
				// value deterministic and pinnable.
				name:    "every field over cap is named in declaration order",
				entry:   rosterEntryFixture{taskID: overID, taskType: overType, description: overDesc},
				wantCut: []string{"task_id", "task_type", "description"}, wantLenTaskID: taskFieldIDCapFixture,
				wantLenTaskType: taskFieldIDCapFixture, wantLenDescription: taskRosterDescriptionCapFixture,
			},
			{
				// The <= boundary: a field of exactly its cap is not truncated, and
				// nothing is reported.
				name: "every field exactly at its cap is not truncated",
				entry: rosterEntryFixture{
					taskID:      strings.Repeat("i", taskFieldIDCapFixture),
					taskType:    strings.Repeat("k", taskFieldIDCapFixture),
					description: strings.Repeat("d", taskRosterDescriptionCapFixture),
				},
				wantCut: nil, wantLenTaskID: taskFieldIDCapFixture,
				wantLenTaskType: taskFieldIDCapFixture, wantLenDescription: taskRosterDescriptionCapFixture,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				ev := backgroundTaskRosterEvent(t, backgroundTaskRosterLineFixture(t, []rosterEntryFixture{tc.entry}))

				if len(ev.Tasks) != 1 {
					t.Fatalf("len(Tasks): got %d, want 1", len(ev.Tasks))
				}
				if ev.DroppedTasks != 0 {
					t.Errorf("DroppedTasks: got %d, want 0 — a text cut must not report a count drop", ev.DroppedTasks)
				}
				task := ev.Tasks[0]
				if !reflect.DeepEqual(task.TruncatedFields, tc.wantCut) {
					t.Errorf("TruncatedFields: got %#v, want %#v", task.TruncatedFields, tc.wantCut)
				}
				lens := []struct {
					field string
					got   int
					want  int
				}{
					{"TaskID", len(task.TaskID), tc.wantLenTaskID},
					{"TaskType", len(task.TaskType), tc.wantLenTaskType},
					{"Description", len(task.Description), tc.wantLenDescription},
				}
				for _, l := range lens {
					if l.got != l.want {
						t.Errorf("len(%s): got %d, want %d", l.field, l.got, l.want)
					}
				}
			})
		}
	})

	t.Run("a cut mid-rune yields valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// The cap is a BYTE slice, so it can land inside a multi-byte rune. The
		// value rides a JSON string field downstream, where an invalid sequence
		// would be silently replaced, so the producer scrubs it here instead.
		desc := strings.Repeat("d", taskRosterDescriptionCapFixture-1) + strings.Repeat("€", 8)
		ev := backgroundTaskRosterEvent(t, backgroundTaskRosterLineFixture(t,
			[]rosterEntryFixture{{taskID: smallID, taskType: smallType, description: desc}}))

		if len(ev.Tasks) != 1 {
			t.Fatalf("len(Tasks): got %d, want 1", len(ev.Tasks))
		}
		task := ev.Tasks[0]
		if !reflect.DeepEqual(task.TruncatedFields, []string{"description"}) {
			t.Fatalf("TruncatedFields: got %#v, want [description]", task.TruncatedFields)
		}
		if !utf8.ValidString(task.Description) {
			t.Errorf("Description is not valid UTF-8 after a mid-rune cut")
		}
		// Short of the cap, not at it: the empty replacement DELETES the partial
		// rune rather than replacing it.
		if len(task.Description) > taskRosterDescriptionCapFixture {
			t.Errorf("len(Description): got %d, want <= %d", len(task.Description), taskRosterDescriptionCapFixture)
		}
	})

	t.Run("both dimensions cut at once and neither report smears into the other", func(t *testing.T) {
		t.Parallel()
		// The concrete evidence for "bounded in BOTH dimensions": an entry inside
		// the SURVIVING prefix carries its own text cut while the roster reports its
		// count drop, and the two reports are independent.
		entries := rosterEntriesFixture(taskRosterEntriesCapFixture + 1)
		entries[0].description = strings.Repeat("d", taskRosterDescriptionCapFixture+1)
		ev := backgroundTaskRosterEvent(t, backgroundTaskRosterLineFixture(t, entries))

		if len(ev.Tasks) != taskRosterEntriesCapFixture {
			t.Fatalf("len(Tasks): got %d, want %d", len(ev.Tasks), taskRosterEntriesCapFixture)
		}
		if ev.DroppedTasks != 1 {
			t.Errorf("DroppedTasks: got %d, want 1", ev.DroppedTasks)
		}
		if !reflect.DeepEqual(ev.Tasks[0].TruncatedFields, []string{"description"}) {
			t.Errorf("Tasks[0].TruncatedFields: got %#v, want [description]", ev.Tasks[0].TruncatedFields)
		}
		if len(ev.Tasks[0].Description) != taskRosterDescriptionCapFixture {
			t.Errorf("len(Tasks[0].Description): got %d, want %d", len(ev.Tasks[0].Description), taskRosterDescriptionCapFixture)
		}
		for i := 1; i < len(ev.Tasks); i++ {
			if ev.Tasks[i].TruncatedFields != nil {
				t.Errorf("Tasks[%d].TruncatedFields: got %#v, want nil — one entry's cut must not smear onto its neighbours",
					i, ev.Tasks[i].TruncatedFields)
			}
		}
	})
}

// TestParser_BackgroundTaskRosterSynthesizesNoTerminalEvent proves the clause the
// capture cannot: the capturing probe deliberately ended its turn with the task
// still ALIVE, so nothing on record shows a task finishing.
//
// A task's disappearance from a later roster is the available finish signal, but
// that transition has never been observed, and the daemon does not report a
// finish it cannot detect. The second line is SYNTHESIZED and invents no field
// structure — the keys are the capture's, only the array contents differ.
func TestParser_BackgroundTaskRosterSynthesizesNoTerminalEvent(t *testing.T) {
	t.Parallel()

	// Both lines go through ONE parser, so a hypothetical cross-line roster diff
	// would have the state it needs to fire. Separate parsers would make the
	// assertion vacuous.
	drive := func(t *testing.T, second string) []turnevent.Event {
		t.Helper()
		var events []turnevent.Event
		p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
		captured := capturedSystemLine(t, "background_tasks_changed")
		if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
		if len(events) != 1 {
			t.Fatalf("the captured roster emitted %d events, want 1 — this test's premise", len(events))
		}
		before := len(events)
		if _, err := p.Write([]byte(second + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
		return events[before:]
	}

	tests := []struct {
		name   string
		second []rosterEntryFixture
	}{
		{
			name:   "the captured task is replaced by a different one",
			second: []rosterEntryFixture{{taskID: "z9kq2f1x0", taskType: "local_bash", description: "sleep 60"}},
		},
		{
			// The shape an implementation is most tempted to special-case into a
			// finish, which is why it is asserted rather than left to the first.
			name:   "the roster goes empty",
			second: []rosterEntryFixture{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := drive(t, backgroundTaskRosterLineFixture(t, tc.second))

			// Asserted on the WHOLE slice rather than on got[0]: a test that inspects
			// only the first event cannot see a finish appended beside it, which is
			// the exact failure this test exists to catch.
			if len(got) != 1 {
				t.Fatalf("the second roster emitted %d events, want exactly 1 — no finish/terminal event may accompany it (%#v)",
					len(got), got)
			}
			ev, ok := got[0].(turnevent.BackgroundTaskRoster)
			if !ok {
				t.Fatalf("event type: got %T, want turnevent.BackgroundTaskRoster", got[0])
			}
			// The array's contents are carried STRUCTURALLY; the daemon infers
			// nothing from the disappearance beyond what the snapshot already says.
			if len(ev.Tasks) != len(tc.second) {
				t.Errorf("len(Tasks): got %d, want %d", len(ev.Tasks), len(tc.second))
			}
			if ev.DroppedTasks != 0 {
				t.Errorf("DroppedTasks: got %d, want 0", ev.DroppedTasks)
			}
		})
	}
}

// TestParser_BackgroundTaskRosterDropIsLoggedContentFree is the package's
// standing rule applied to the third subtype: nothing derived from claude's
// output reaches a log. The content crosses the wire, not the log.
//
// A roster is the most attractive thing to dump while debugging a malformed line
// — it is a list, and lists read as diagnostics — which is why both the success
// and the decode-failure path are driven here. The ENTRY COUNT is covered too:
// it is derived from claude's output as much as the text is, and "just the
// length" is the leak a content-free rule is most often bent for. That half is
// closed by asserting the exact record set rather than by string matching, which
// no count could be searched for reliably.
func TestParser_BackgroundTaskRosterDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	captured := capturedSystemLine(t, "background_tasks_changed")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedSession, _ := payload["session_id"].(string)
	capturedUUID, _ := payload["uuid"].(string)
	if capturedSession == "" || capturedUUID == "" {
		t.Fatalf("the capture carries no string session_id/uuid, so the leak sweep would be vacuous")
	}

	// A line the shape cannot decode (tasks is an object, not an array), so the
	// decode-failure path runs. Its description is a distinctive literal, and the
	// whole point is that it must appear in no log record.
	const malformedDesc = "rm -rf /tmp/never-log-this-roster"
	malformed := `{"type":"system","subtype":"background_tasks_changed",` +
		`"tasks":{"task_id":"x","task_type":"local_bash","description":"` + malformedDesc + `"}}`

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if _, err := p.Write([]byte(malformed + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	// The malformed line is dropped, and NOT as an Unrecognized: keeping `system`
	// whole on ignoredLineTypes is what makes "no system line reaches the
	// unrecognized lane" structural, and that guarantee outranks surfacing a
	// malformed line of a subtype we already know.
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 (the captured line only) — %#v", len(events), events)
	}
	ev, ok := events[0].(turnevent.BackgroundTaskRoster)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskRoster", events[0])
	}
	// The description as the parser itself carried it, so the leak candidate is
	// derived from the shipped mapping rather than transcribed.
	if len(ev.Tasks) != 1 || ev.Tasks[0].Description == "" {
		t.Fatalf("the mapped roster carries no description, so the leak sweep over it would be vacuous")
	}

	// Exactly one record, and it is the decode-failure drop carrying the subtype
	// KEYWORD only. This is the assertion that closes the count leak: nothing else
	// is logged on either path, so no entry count can ride along.
	const dropMsg = "streamsup: dropping undecodable system line"
	all := rec.all()
	if len(all) != 1 {
		t.Fatalf("log records: got %d, want exactly 1 (the undecodable drop) — %+v", len(all), all)
	}
	if all[0].msg != dropMsg {
		t.Fatalf("record message: got %q, want %q — the decode-failure path never ran, so this sweep would be vacuous", all[0].msg, dropMsg)
	}
	if !reflect.DeepEqual(all[0].attrs, map[string]string{"subtype": "background_tasks_changed"}) {
		t.Errorf("the undecodable-drop record carries %#v, want the subtype keyword only", all[0].attrs)
	}

	for _, r := range all {
		for _, leak := range []string{ev.Tasks[0].Description, malformedDesc, capturedSession, capturedUUID} {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content: %q", r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content; this path logs the subtype only", r.msg, k)
				}
			}
		}
	}
}

// logRecorder is a slog.Handler that captures every record it is handed —
// message plus attr key/value pairs — so a test can assert both what IS logged
// and, the harder half, what is NOT. Mirrors runner_test.go's spawnArgsRecorder:
// four methods, Enabled always true so Debug records arrive, mutex-guarded.
//
// Unlike spawnArgsRecorder it filters nothing at capture time. Selection happens
// in the assertion (withMessage), which lets the "never the text" check sweep
// EVERY record the line produced rather than only the one expected — a leak into
// some other record on the same path would be caught too.
type logRecorder struct {
	mu      sync.Mutex
	records []capturedRecord
}

// capturedRecord is one slog record flattened to what the assertions read. Attr
// values are rendered with slog.Value.String() so the content check does not
// depend on the value's dynamic type.
type capturedRecord struct {
	msg   string
	attrs map[string]string
}

func (h *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	c := capturedRecord{msg: rec.Message, attrs: make(map[string]string)}
	rec.Attrs(func(a slog.Attr) bool {
		c.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, c)
	h.mu.Unlock()
	return nil
}

func (h *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logRecorder) WithGroup(string) slog.Handler      { return h }

func (h *logRecorder) all() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]capturedRecord(nil), h.records...)
}

// withMessage returns the captured records whose message is msg.
func (h *logRecorder) withMessage(msg string) []capturedRecord {
	var out []capturedRecord
	for _, rec := range h.all() {
		if rec.msg == msg {
			out = append(out, rec)
		}
	}
	return out
}

// TestParser_HarnessNudgeDropIsLoggedContentFree is AC4 of #1247: the drop is
// Debug-logged with the site and block type ONLY, never the text — the package's
// standing content-free logging rule, matching the existing known-ignored drop.
//
// The third assertion is the load-bearing one. The first two only describe what
// is present; "no captured attr value contains the nudge" is what catches the
// realistic way this rule gets broken later, someone appending "text",
// block.Text to the log line. The payload itself is harmless boilerplate — the
// rule is about the SITE, which would carry an arbitrary user/text block's
// content the day the guard is widened.
func TestParser_HarnessNudgeDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	rec := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(rec))

	line := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + harnessNudgeFixture + `"}]}}`
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
		for k, v := range r.attrs {
			if strings.Contains(v, harnessNudgeFixture) {
				t.Errorf("record %q attr %q carries the block text; the drop site logs site and type only", r.msg, k)
			}
		}
		if strings.Contains(r.msg, harnessNudgeFixture) {
			t.Errorf("record message carries the block text: %q", r.msg)
		}
	}
}

// TestParser_IgnoredLineTypesIsTheMeasuredSet pins the list itself. It is the
// one knob that decides whether the timeline row is signal or noise, so growing
// it must be a deliberate edit with a measurement behind it, never a drive-by.
func TestParser_IgnoredLineTypesIsTheMeasuredSet(t *testing.T) {
	t.Parallel()
	want := map[string]bool{"system": true, "rate_limit_event": true}
	if !reflect.DeepEqual(ignoredLineTypes, want) {
		t.Errorf("ignoredLineTypes: got %v, want %v\n"+
			"Growing this list hides a claude message type from every client. "+
			"Re-run the slice 0 measurement before changing it.", ignoredLineTypes, want)
	}
}
