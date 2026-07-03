package acpbridge

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestMapUpdate is the exhaustiveness assertion (AC-4): one case per sealed
// turnevent.Event variant plus the no-notification drops. It mirrors
// turnbridge.TestMapEventOutbound.
func TestMapUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ev         turnevent.Event
		wantUpdate any
		wantMsgID  string
		wantOK     bool
	}{
		{
			name:      "TextChunk -> agent_message_chunk, msg id out-of-band",
			ev:        turnevent.TextChunk{MessageID: "m1", Text: "hi"},
			wantMsgID: "m1",
			wantUpdate: AgentMessageChunk{
				SessionUpdate: SessionUpdateAgentMessageChunk,
				Content:       ContentBlock{Type: "text", Text: "hi"},
			},
			wantOK: true,
		},
		{
			// ThoughtChunk IS mapped here — the mobile divergence (turnbridge
			// drops it) does not apply to ACP.
			name:      "ThoughtChunk -> agent_thought_chunk (not dropped)",
			ev:        turnevent.ThoughtChunk{MessageID: "m9", Text: "thinking"},
			wantMsgID: "m9",
			wantUpdate: AgentThoughtChunk{
				SessionUpdate: SessionUpdateAgentThoughtChunk,
				Content:       ContentBlock{Type: "text", Text: "thinking"},
			},
			wantOK: true,
		},
		{
			name: "ToolStart -> tool_call with status pending",
			ev: turnevent.ToolStart{
				ToolCallID: "tool-1",
				Title:      "Bash",
				Kind:       turnevent.ToolKindExecute,
				RawInput:   json.RawMessage(`{"command":"ls"}`),
				Locations:  []turnevent.Location{{Path: "/tmp/x.go", Line: 3}},
			},
			wantMsgID: "",
			wantUpdate: ToolCall{
				SessionUpdate: SessionUpdateToolCall,
				ToolCallID:    "tool-1",
				Title:         "Bash",
				Kind:          "execute",
				Status:        "pending", // AC-2: ToolStart has no status; ACP default.
				RawInput:      json.RawMessage(`{"command":"ls"}`),
				Locations:     []ToolCallLocation{{Path: "/tmp/x.go", Line: 3}},
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate completed + text content",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-1",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: "done"},
			},
			wantUpdate: ToolCallUpdate{
				SessionUpdate: SessionUpdateToolCallUpdate,
				ToolCallID:    "tool-1",
				Status:        "completed",
				Content: []ToolCallContent{
					{Type: "content", Content: &ContentBlock{Type: "text", Text: "done"}},
				},
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate failed + nil content -> status-only, content omitted",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-2",
				Status:     turnevent.ToolStatusFailed,
				Content:    nil,
			},
			wantUpdate: ToolCallUpdate{
				SessionUpdate: SessionUpdateToolCallUpdate,
				ToolCallID:    "tool-2",
				Status:        "failed",
				Content:       nil,
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate + diff content",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-3",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.DiffContent{Path: "/tmp/x.go", OldText: "a", NewText: "b"},
			},
			wantUpdate: ToolCallUpdate{
				SessionUpdate: SessionUpdateToolCallUpdate,
				ToolCallID:    "tool-3",
				Status:        "completed",
				Content: []ToolCallContent{
					{Type: "diff", Path: "/tmp/x.go", OldText: "a", NewText: "b"},
				},
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate + terminal content",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-4",
				Status:     turnevent.ToolStatusInProgress,
				Content:    turnevent.TerminalContent{TerminalID: "term-9"},
			},
			wantUpdate: ToolCallUpdate{
				SessionUpdate: SessionUpdateToolCallUpdate,
				ToolCallID:    "tool-4",
				Status:        "in_progress",
				Content: []ToolCallContent{
					{Type: "terminal", TerminalID: "term-9"},
				},
			},
			wantOK: true,
		},
		// Drop cases: no session/update representation.
		{
			name: "TurnEnd -> no notification (stopReason is a session/prompt return)",
			ev:   turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
		},
		{
			name: "Stall -> no notification (internal-only, ACP has no home)",
			ev:   turnevent.Stall{},
		},
		{
			name: "nil Event -> no notification (zero-value safe)",
			ev:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			update, msgID, ok := MapUpdate(tt.ev)
			if ok != tt.wantOK {
				t.Fatalf("ok: got %v, want %v (update=%#v msgID=%q)", ok, tt.wantOK, update, msgID)
			}
			if !reflect.DeepEqual(update, tt.wantUpdate) {
				t.Fatalf("update:\n got %#v\nwant %#v", update, tt.wantUpdate)
			}
			if msgID != tt.wantMsgID {
				t.Fatalf("msgID: got %q, want %q", msgID, tt.wantMsgID)
			}
			// Loop invariant: a no-notification outcome carries no payload and
			// no message id.
			if !ok && (update != nil || msgID != "") {
				t.Fatalf("no-notification event must yield (nil, \"\"), got (%#v, %q)", update, msgID)
			}
		})
	}
}

// TestMapUpdate_WireShape locks the ACP camelCase field names — the actual wire
// contract. It marshals one update per mapped variant and compares against a
// golden JSON literal. Critically, it proves agent_message_chunk carries NO
// messageId field (the id is returned out-of-band, not on the ACP wire). Inputs
// contain no <>&, so default json.Marshal HTML-escaping is a no-op.
func TestMapUpdate_WireShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   turnevent.Event
		want string
	}{
		{
			name: "agent_message_chunk has no messageId field",
			ev:   turnevent.TextChunk{MessageID: "m1", Text: "hi"},
			want: `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`,
		},
		{
			name: "agent_thought_chunk",
			ev:   turnevent.ThoughtChunk{MessageID: "m9", Text: "thinking"},
			want: `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}`,
		},
		{
			name: "tool_call carries verbatim taxonomy strings and opaque rawInput",
			ev: turnevent.ToolStart{
				ToolCallID: "tool-1",
				Title:      "Bash",
				Kind:       turnevent.ToolKindExecute,
				RawInput:   json.RawMessage(`{"command":"ls"}`),
				Locations:  []turnevent.Location{{Path: "/tmp/x.go", Line: 3}},
			},
			want: `{"sessionUpdate":"tool_call","toolCallId":"tool-1","title":"Bash","kind":"execute","status":"pending","rawInput":{"command":"ls"},"locations":[{"path":"/tmp/x.go","line":3}]}`,
		},
		{
			name: "tool_call_update with text content",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-1",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: "done"},
			},
			want: `{"sessionUpdate":"tool_call_update","toolCallId":"tool-1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"done"}}]}`,
		},
		{
			name: "tool_call_update status-only omits content",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-2",
				Status:     turnevent.ToolStatusFailed,
				Content:    nil,
			},
			want: `{"sessionUpdate":"tool_call_update","toolCallId":"tool-2","status":"failed"}`,
		},
		{
			name: "tool_call_update with diff content",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-3",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.DiffContent{Path: "/tmp/x.go", OldText: "a", NewText: "b"},
			},
			want: `{"sessionUpdate":"tool_call_update","toolCallId":"tool-3","status":"completed","content":[{"type":"diff","path":"/tmp/x.go","oldText":"a","newText":"b"}]}`,
		},
		{
			name: "tool_call_update with terminal content",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-4",
				Status:     turnevent.ToolStatusInProgress,
				Content:    turnevent.TerminalContent{TerminalID: "term-9"},
			},
			want: `{"sessionUpdate":"tool_call_update","toolCallId":"tool-4","status":"in_progress","content":[{"type":"terminal","terminalId":"term-9"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			update, _, ok := MapUpdate(tt.ev)
			if !ok {
				t.Fatalf("MapUpdate(%#v): ok=false, want a payload", tt.ev)
			}
			b, err := json.Marshal(update)
			if err != nil {
				t.Fatalf("json.Marshal(%#v): %v", update, err)
			}
			if got := string(b); got != tt.want {
				t.Fatalf("wire shape:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}
