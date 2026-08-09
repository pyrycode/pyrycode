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

// TestBackgroundTaskPayloadWireShape locks the wire shape of the three
// background-task payloads and the roster's entry type (#1401). It marshals each
// payload VALUE directly rather than driving MapUpdate: the three neutral
// variants have no MapUpdate arm until #1402, so there is no mapper to reach
// them through, which is also why they are absent from TestMapUpdate_WireShape.
//
// Every field in every row is distinct and non-zero (except row 4, whose point IS
// the zero values) and no value repeats across rows, so a renamed JSON tag, a
// dropped field, a field swap, a cross-row copy or a wrong discriminant fails a
// row. Inputs contain no <>&, so default json.Marshal HTML-escaping is a no-op.
func TestBackgroundTaskPayloadWireShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{
			name: "background_task_started carries the spawning toolCallId",
			payload: BackgroundTaskStarted{
				SessionUpdate:   SessionUpdateBackgroundTaskStarted,
				TaskID:          "task_01ABC",
				ToolCallID:      "toolu_01XYZ",
				Description:     "npm run build -- --watch",
				TaskType:        "local_bash",
				TruncatedFields: []string{"description"},
			},
			want: `{"sessionUpdate":"pyry/background_task_started","taskId":"task_01ABC","toolCallId":"toolu_01XYZ","description":"npm run build -- --watch","taskType":"local_bash","truncatedFields":["description"]}`,
		},
		{
			// Patch is deliberately a truncated, INVALID-JSON blob (the producer
			// cuts it at maxTaskPatch, and a cut object no longer parses). This
			// row is the guard on Patch being a plain string: retyped as
			// json.RawMessage, encoding/json rejects it and the whole payload
			// fails to marshal.
			name: "background_task_updated carries a truncated, unparseable patch",
			payload: BackgroundTaskUpdated{
				SessionUpdate:   SessionUpdateBackgroundTaskUpdated,
				TaskID:          "task_02DEF",
				Patch:           `{"is_backgrounded":tr`,
				TruncatedFields: []string{"patch"},
			},
			want: `{"sessionUpdate":"pyry/background_task_updated","taskId":"task_02DEF","patch":"{\"is_backgrounded\":tr","truncatedFields":["patch"]}`,
		},
		{
			// The second entry's taskType is a fixture value, not an observed
			// one — only local_bash has ever been captured. The field is a plain
			// string precisely so an unobserved kind rides through unchanged.
			name: "background_task_roster with entries reports droppedTasks",
			payload: BackgroundTaskRoster{
				SessionUpdate: SessionUpdateBackgroundTaskRoster,
				Tasks: []BackgroundTask{
					{
						TaskID:          "task_03GHI",
						TaskType:        "local_bash",
						Description:     "sleep 300",
						TruncatedFields: []string{"description"},
					},
					{
						TaskID:      "task_04JKL",
						TaskType:    "unobserved_kind",
						Description: "tail -f /var/log/app.log",
					},
				},
				DroppedTasks: 3,
			},
			want: `{"sessionUpdate":"pyry/background_task_roster","tasks":[{"taskId":"task_03GHI","taskType":"local_bash","description":"sleep 300","truncatedFields":["description"]},{"taskId":"task_04JKL","taskType":"unobserved_kind","description":"tail -f /var/log/app.log"}],"droppedTasks":3}`,
		},
		{
			// Tasks is left NIL on purpose: nil is the only thing the mapper will
			// ever hand this type for an empty roster (turnevent's Tasks is nil
			// both for an empty roster and when claude omits the key, never an
			// empty non-nil slice). A hand-built []BackgroundTask{} marshals to
			// [] with no device involved and would pin nothing. droppedTasks is
			// likewise left at 0, the positive "nothing was dropped".
			name: "empty background_task_roster marshals tasks as [] and keeps droppedTasks",
			payload: BackgroundTaskRoster{
				SessionUpdate: SessionUpdateBackgroundTaskRoster,
			},
			want: `{"sessionUpdate":"pyry/background_task_roster","tasks":[],"droppedTasks":0}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("json.Marshal(%#v): %v", tt.payload, err)
			}
			if got := string(b); got != tt.want {
				t.Fatalf("wire shape:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}
