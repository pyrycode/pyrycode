package streamsup

import (
	"context"
	"encoding/json"
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
func TestParser_IgnoredLineTypesStaySilent(t *testing.T) {
	t.Parallel()
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5"}`,
		`{"type":"system","subtype":"thinking_tokens","tokens":128}`,
		`{"type":"system","subtype":"status","status":"ok"}`,
		// Ignoring `system` wholesale, not per-subtype, is deliberate: it is
		// claude's catch-all namespace and its highest-rate emitter, so a new
		// subtype must NOT become a per-turn noise row.
		`{"type":"system","subtype":"a_subtype_invented_next_year"}`,
		`{"type":"rate_limit_event","rate_limit":{"status":"allowed"}}`,
	}
	for _, line := range lines {
		if got := collectEvents(line); got != nil {
			t.Errorf("line %s\n emitted %#v, want silence", line, got)
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
