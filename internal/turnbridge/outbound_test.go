package turnbridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestMapEventOutbound(t *testing.T) {
	t.Parallel()

	tc := TurnContext{ConversationID: "c1", TurnID: "t1", Seq: 7}

	tests := []struct {
		name        string
		ev          turnevent.Event
		tc          TurnContext
		wantTyp     string
		wantPayload any
		wantOK      bool
	}{
		{
			name:    "TextChunk -> assistant_delta",
			ev:      turnevent.TextChunk{MessageID: "m1", Text: "hi there"},
			tc:      tc,
			wantTyp: protocol.TypeAssistantDelta,
			wantPayload: protocol.AssistantDeltaPayload{
				ConversationID: "c1", TurnID: "t1", Seq: 7, Text: "hi there",
			},
			wantOK: true,
		},
		{
			name:    "TextChunk seq 0 boundary reaches the wire",
			ev:      turnevent.TextChunk{Text: "first"},
			tc:      TurnContext{ConversationID: "c1", TurnID: "t1", Seq: 0},
			wantTyp: protocol.TypeAssistantDelta,
			wantPayload: protocol.AssistantDeltaPayload{
				ConversationID: "c1", TurnID: "t1", Seq: 0, Text: "first",
			},
			wantOK: true,
		},
		{
			name: "ToolStart -> tool_use with input summary",
			ev: turnevent.ToolStart{
				ToolCallID: "tool-1",
				Title:      "Bash",
				Kind:       turnevent.ToolKindExecute,
				RawInput:   json.RawMessage(`{"command":"ls"}`),
			},
			tc:      tc,
			wantTyp: protocol.TypeToolUse,
			wantPayload: protocol.ToolUsePayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-1",
				Name: "Bash", InputSummary: `{"command":"ls"}`,
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate failed -> tool_result is_error true",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-1",
				Status:     turnevent.ToolStatusFailed,
				Content:    turnevent.TextContent{Text: "boom"},
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-1",
				IsError: true, ResultSummary: "boom",
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate completed -> tool_result is_error false",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-2",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: "all good"},
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-2",
				IsError: false, ResultSummary: "all good",
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate in_progress -> is_error false",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-3",
				Status:     turnevent.ToolStatusInProgress,
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-3",
				IsError: false, ResultSummary: "",
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate status-only (nil content) -> empty result summary",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-4",
				Status:     turnevent.ToolStatusCompleted,
				Content:    nil,
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-4",
				IsError: false, ResultSummary: "",
			},
			wantOK: true,
		},
		{
			name:    "TurnEnd end_turn -> turn_end",
			ev:      turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
			tc:      tc,
			wantTyp: protocol.TypeTurnEnd,
			wantPayload: protocol.TurnEndPayload{
				ConversationID: "c1", TurnID: "t1", StopReason: "end_turn",
			},
			wantOK: true,
		},
		{
			name:    "TurnEnd cancelled -> stop_reason verbatim",
			ev:      turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled},
			tc:      tc,
			wantTyp: protocol.TypeTurnEnd,
			wantPayload: protocol.TurnEndPayload{
				ConversationID: "c1", TurnID: "t1", StopReason: "cancelled",
			},
			wantOK: true,
		},
		// Drop cases: ThoughtChunk (ADR 025 — text not forwarded) and the
		// zero/nil Event.
		{
			name: "ThoughtChunk dropped, no thought text forwarded",
			ev:   turnevent.ThoughtChunk{MessageID: "m9", Text: "secret reasoning"},
			tc:   tc,
		},
		{
			name: "nil Event dropped (zero-value safe)",
			ev:   nil,
			tc:   tc,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ, payload, ok := MapEvent(tt.ev, tt.tc)
			if ok != tt.wantOK {
				t.Fatalf("ok: got %v, want %v (typ=%q payload=%#v)", ok, tt.wantOK, typ, payload)
			}
			if typ != tt.wantTyp {
				t.Fatalf("typ: got %q, want %q", typ, tt.wantTyp)
			}
			if !reflect.DeepEqual(payload, tt.wantPayload) {
				t.Fatalf("payload:\n got %#v\nwant %#v", payload, tt.wantPayload)
			}
			if !ok && payload != nil {
				t.Fatalf("dropped event must yield nil payload, got %#v", payload)
			}
		})
	}
}

func TestBuildTurnState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state TurnState
		want  string
	}{
		{"thinking", StateThinking, "thinking"},
		{"responding", StateResponding, "responding"},
		{"idle", StateIdle, "idle"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ, payload := BuildTurnState("c1", tt.state)
			if typ != protocol.TypeTurnState {
				t.Fatalf("typ: got %q, want %q", typ, protocol.TypeTurnState)
			}
			want := protocol.TurnStatePayload{ConversationID: "c1", State: tt.want}
			if payload != want {
				t.Fatalf("payload: got %#v, want %#v", payload, want)
			}
		})
	}
}

func TestInputSummary(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 300)

	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"nil", nil, ""},
		{"empty", json.RawMessage{}, ""},
		{"compacts whitespace to one line", json.RawMessage(`{ "command" : "ls" }`), `{"command":"ls"}`},
		{"invalid json yields empty", json.RawMessage(`{not json`), ""},
		{
			name: "oversized truncated with ellipsis",
			raw:  json.RawMessage(`{"k":"` + long + `"}`),
			want: `{"k":"` + strings.Repeat("a", maxSummaryLen-6) + "…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := inputSummary(tt.raw); got != tt.want {
				t.Fatalf("inputSummary(%q): got %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestResultSummary(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("b", 300)

	tests := []struct {
		name string
		in   turnevent.ToolContent
		want string
	}{
		{"nil -> empty", nil, ""},
		{"text verbatim", turnevent.TextContent{Text: "done"}, "done"},
		{"text truncated", turnevent.TextContent{Text: long}, strings.Repeat("b", maxSummaryLen) + "…"},
		{"diff -> path", turnevent.DiffContent{Path: "/tmp/x.go"}, "/tmp/x.go"},
		{"terminal -> reference", turnevent.TerminalContent{TerminalID: "term-9"}, "terminal term-9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resultSummary(tt.in); got != tt.want {
				t.Fatalf("resultSummary(%#v): got %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"under bound unchanged", "abc", 5, "abc"},
		{"at bound unchanged", "abcde", 5, "abcde"},
		{"over bound cut plus ellipsis", "abcdef", 5, "abcde…"},
		{"multibyte cut on rune boundary", strings.Repeat("日", 10), 3, "日日日…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tt.s, tt.max)
			if got != tt.want {
				t.Fatalf("truncate(%q, %d): got %q, want %q", tt.s, tt.max, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q, %d) produced invalid UTF-8: %q", tt.s, tt.max, got)
			}
		})
	}
}
