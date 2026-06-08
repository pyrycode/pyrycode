package turnbridge

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// entry builds a synthetic JSONLEntry: an envelope of the given type carrying a
// message {id, stop_reason, content[blocks]}, populating BOTH the typed Message
// (read by AssistantText / thinkingText) and RawLine (re-parsed by
// ParseToolUse / ParseToolResult) from the same blocks so the two never drift.
// RawLine is nil on a real synthetic entry by default — the parsers read it, so
// the test must populate it (the JSONLEntry contract).
func entry(t *testing.T, envType, msgID, stopReason string, blocks ...map[string]any) tuidriver.JSONLEntry {
	t.Helper()
	cbs := make([]tuidriver.ContentBlock, len(blocks))
	rawBlocks := make([]any, len(blocks))
	for i, b := range blocks {
		bt, _ := b["type"].(string)
		cbs[i] = tuidriver.ContentBlock{Type: bt, Raw: b}
		rawBlocks[i] = b
	}
	msg := map[string]any{"content": rawBlocks}
	if msgID != "" {
		msg["id"] = msgID
	}
	if stopReason != "" {
		msg["stop_reason"] = stopReason
	}
	line, err := json.Marshal(map[string]any{"type": envType, "message": msg})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	return tuidriver.JSONLEntry{
		Type: envType,
		Message: &tuidriver.EntryMessage{
			ID:         msgID,
			StopReason: stopReason,
			Content:    cbs,
		},
		RawLine: line,
	}
}

func jsonlEvent(e tuidriver.JSONLEntry) tuidriver.Event {
	return tuidriver.Event{Kind: tuidriver.EventKindJsonlEntry, Source: tuidriver.EventSourceJsonl, Entry: e}
}

func kindEvent(k tuidriver.EventKind) tuidriver.Event {
	return tuidriver.Event{Kind: k}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestMapEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     tuidriver.Event
		want   turnevent.Event
		wantOK bool
	}{
		{
			name:   "assistant text -> TextChunk",
			in:     jsonlEvent(entry(t, "assistant", "m1", "", map[string]any{"type": "text", "text": "hi there"})),
			want:   turnevent.TextChunk{MessageID: "m1", Text: "hi there"},
			wantOK: true,
		},
		{
			name:   "assistant thinking -> ThoughtChunk",
			in:     jsonlEvent(entry(t, "assistant", "m2", "", map[string]any{"type": "thinking", "thinking": "musing about it"})),
			want:   turnevent.ThoughtChunk{MessageID: "m2", Text: "musing about it"},
			wantOK: true,
		},
		{
			name: "assistant tool_use -> ToolStart",
			in: jsonlEvent(entry(t, "assistant", "m3", "", map[string]any{
				"type": "tool_use", "id": "tool-1", "name": "Bash",
				"input": map[string]any{"command": "ls"},
			})),
			want: turnevent.ToolStart{
				ToolCallID: "tool-1",
				Title:      "Bash",
				Kind:       turnevent.ToolKindExecute,
				RawInput:   mustMarshal(t, map[string]any{"command": "ls"}),
			},
			wantOK: true,
		},
		{
			name: "user tool_result ok -> ToolUpdate completed",
			in: jsonlEvent(entry(t, "user", "", "", map[string]any{
				"type": "tool_result", "tool_use_id": "tool-1", "is_error": false, "content": "all good",
			})),
			want: turnevent.ToolUpdate{
				ToolCallID: "tool-1",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: "all good"},
			},
			wantOK: true,
		},
		{
			name: "user tool_result error -> ToolUpdate failed",
			in: jsonlEvent(entry(t, "user", "", "", map[string]any{
				"type": "tool_result", "tool_use_id": "tool-2", "is_error": true, "content": "boom",
			})),
			want: turnevent.ToolUpdate{
				ToolCallID: "tool-2",
				Status:     turnevent.ToolStatusFailed,
				Content:    turnevent.TextContent{Text: "boom"},
			},
			wantOK: true,
		},
		{
			name: "user tool_result empty content -> status-only ToolUpdate",
			in: jsonlEvent(entry(t, "user", "", "", map[string]any{
				"type": "tool_result", "tool_use_id": "tool-3", "is_error": false,
			})),
			want: turnevent.ToolUpdate{
				ToolCallID: "tool-3",
				Status:     turnevent.ToolStatusCompleted,
				Content:    nil,
			},
			wantOK: true,
		},
		{
			name:   "end of turn -> TurnEnd",
			in:     kindEvent(tuidriver.EventKindJsonlEndOfTurn),
			want:   turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
			wantOK: true,
		},
		{
			name:   "stall detected -> Stall",
			in:     kindEvent(tuidriver.EventKindStallDetected),
			want:   turnevent.Stall{},
			wantOK: true,
		},
		// Drop cases: every PTY-state kind, unknown, and JSONL entries with no
		// representable content.
		{name: "drop unknown", in: kindEvent(tuidriver.EventKindUnknown)},
		{name: "drop pty idle", in: kindEvent(tuidriver.EventKindPtyIdle)},
		{name: "drop pty thinking", in: kindEvent(tuidriver.EventKindPtyThinking)},
		{name: "drop pty modal shown", in: kindEvent(tuidriver.EventKindPtyModalShown)},
		{name: "drop pty modal hidden", in: kindEvent(tuidriver.EventKindPtyModalHidden)},
		{name: "drop pty mcp failure shown", in: kindEvent(tuidriver.EventKindPtyMcpFailureShown)},
		{name: "drop pty mcp failure hidden", in: kindEvent(tuidriver.EventKindPtyMcpFailureHidden)},
		{name: "drop pty network failure shown", in: kindEvent(tuidriver.EventKindPtyNetworkFailureShown)},
		{name: "drop pty network failure hidden", in: kindEvent(tuidriver.EventKindPtyNetworkFailureHidden)},
		{
			name: "drop assistant with no representable block",
			in:   jsonlEvent(entry(t, "assistant", "m4", "", map[string]any{"type": "image", "source": "x"})),
		},
		{
			name: "drop user text (no tool_result)",
			in:   jsonlEvent(entry(t, "user", "", "", map[string]any{"type": "text", "text": "a cancel marker"})),
		},
		{
			name: "drop non-message envelope",
			in:   jsonlEvent(tuidriver.JSONLEntry{Type: "system"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := mapEvent(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok: got %v, want %v (got event %#v)", ok, tt.wantOK, got)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("event:\n got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestToolResultText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want string
	}{
		{"string", "plain output", "plain output"},
		{"nil", nil, ""},
		{
			name: "text blocks joined",
			in: []any{
				map[string]any{"type": "text", "text": "part1 "},
				map[string]any{"type": "text", "text": "part2"},
			},
			want: "part1 part2",
		},
		{
			name: "non-text blocks skipped",
			in: []any{
				map[string]any{"type": "image", "source": "x"},
				map[string]any{"type": "text", "text": "kept"},
			},
			want: "kept",
		},
		{"unexpected type", 42, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := toolResultText(tt.in); got != tt.want {
				t.Fatalf("toolResultText(%#v): got %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToolKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want turnevent.ToolKind
	}{
		{"Read", turnevent.ToolKindRead},
		{"Edit", turnevent.ToolKindEdit},
		{"Write", turnevent.ToolKindEdit},
		{"Bash", turnevent.ToolKindExecute},
		{"Grep", turnevent.ToolKindSearch},
		{"Glob", turnevent.ToolKindSearch},
		{"WebFetch", turnevent.ToolKindFetch},
		{"Task", turnevent.ToolKindThink},
		{"SomethingUnknown", turnevent.ToolKindOther},
		{"", turnevent.ToolKindOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := toolKind(tt.name); got != tt.want {
				t.Fatalf("toolKind(%q): got %q, want %q", tt.name, got, tt.want)
			}
			if !tt.want.Valid() {
				t.Fatalf("toolKind(%q) returned out-of-taxonomy %q", tt.name, tt.want)
			}
		})
	}
}

func TestRawInput(t *testing.T) {
	t.Parallel()

	if got := rawInput(nil); got != nil {
		t.Fatalf("rawInput(nil): got %q, want nil", got)
	}
	if got := rawInput(map[string]any{}); got != nil {
		t.Fatalf("rawInput(empty): got %q, want nil", got)
	}
	got := rawInput(map[string]any{"file_path": "/tmp/x"})
	if !json.Valid(got) {
		t.Fatalf("rawInput produced invalid JSON: %q", got)
	}
	var back map[string]any
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("unmarshal rawInput: %v", err)
	}
	if back["file_path"] != "/tmp/x" {
		t.Fatalf("rawInput round-trip: got %#v", back)
	}
}
