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
		{
			// Stall carries conversation_id only; tc's non-empty TurnID and
			// non-zero Seq are ignored (a stall is not turn-scoped, not a delta).
			// StallPayload has no turn_id field, so none can leak.
			name:    "Stall -> stall, conversation_id only",
			ev:      turnevent.Stall{},
			tc:      tc,
			wantTyp: protocol.TypeStall,
			wantPayload: protocol.StallPayload{
				ConversationID: "c1",
			},
			wantOK: true,
		},
		{
			// ApiRetry carries conversation_id + active + the parsed counter only;
			// tc's non-empty TurnID and non-zero Seq are ignored (a status peer is
			// not turn-scoped, not a delta). ApiRetryPayload has no turn_id field,
			// so none can leak.
			name:    "ApiRetry shown -> api_retry, conversation_id + counter, no turn_id",
			ev:      turnevent.ApiRetry{Active: true, Current: 3, Total: 10},
			tc:      tc,
			wantTyp: protocol.TypeApiRetry,
			wantPayload: protocol.ApiRetryPayload{
				ConversationID: "c1", Active: true, Current: 3, Total: 10,
			},
			wantOK: true,
		},
		{
			name:    "Compacting cleared -> compacting, conversation_id + active only",
			ev:      turnevent.Compacting{Active: false},
			tc:      tc,
			wantTyp: protocol.TypeCompacting,
			wantPayload: protocol.CompactingPayload{
				ConversationID: "c1", Active: false,
			},
			wantOK: true,
		},
		{
			name: "Unrecognized -> unrecognized_message, conversation identity only",
			ev: turnevent.Unrecognized{
				Site:      turnevent.UnrecognizedLineType,
				Kind:      "some_future_event",
				Raw:       `{"type":"some_future_event"}`,
				Truncated: false,
			},
			tc:      tc,
			wantTyp: protocol.TypeUnrecognizedMessage,
			// No turn_id and no seq: an unrecognized message has no turn we can
			// honestly attribute it to, so tc.TurnID/tc.Seq are ignored exactly as
			// they are for Stall and Compacting above.
			wantPayload: protocol.UnrecognizedMessagePayload{
				ConversationID: "c1",
				Site:           "line_type",
				MessageType:    "some_future_event",
				Raw:            `{"type":"some_future_event"}`,
				Truncated:      false,
			},
			wantOK: true,
		},
		{
			name: "Unrecognized undecodable carries empty message_type and the truncated flag",
			ev: turnevent.Unrecognized{
				Site:      turnevent.UnrecognizedUndecodable,
				Kind:      "",
				Raw:       `{"type":"assist`,
				Truncated: true,
			},
			tc:      tc,
			wantTyp: protocol.TypeUnrecognizedMessage,
			wantPayload: protocol.UnrecognizedMessagePayload{
				ConversationID: "c1",
				Site:           "undecodable",
				MessageType:    "",
				Raw:            `{"type":"assist`,
				Truncated:      true,
			},
			wantOK: true,
		},
		{
			// Like the status peers above, a background-task frame carries
			// conversation identity only: tc's non-empty TurnID and non-zero Seq are
			// ignored, and the payload has no turn_id/seq field for one to leak
			// into. Every claude-derived string crosses verbatim — the producer
			// bounded them at construction and this adapter re-caps nothing.
			name: "BackgroundTaskStarted -> background_task_started, every field verbatim",
			ev: turnevent.BackgroundTaskStarted{
				TaskID:          "task-1",
				ToolCallID:      "tool-9",
				Description:     "sleep 300 && echo done",
				TaskType:        "local_bash",
				TruncatedFields: []string{"description", "task_type"},
			},
			tc:      tc,
			wantTyp: protocol.TypeBackgroundTaskStarted,
			wantPayload: protocol.BackgroundTaskStartedPayload{
				ConversationID:  "c1",
				TaskID:          "task-1",
				ToolCallID:      "tool-9",
				Description:     "sleep 300 && echo done",
				TaskType:        "local_bash",
				TruncatedFields: []string{"description", "task_type"},
			},
			wantOK: true,
		},
		{
			name: "BackgroundTaskUpdated -> background_task_updated, patch whole and unparsed",
			ev: turnevent.BackgroundTaskUpdated{
				TaskID:          "task-1",
				Patch:           `{"is_backgrounded":true}`,
				TruncatedFields: []string{"patch"},
			},
			tc:      tc,
			wantTyp: protocol.TypeBackgroundTaskUpdated,
			wantPayload: protocol.BackgroundTaskUpdatedPayload{
				ConversationID:  "c1",
				TaskID:          "task-1",
				Patch:           `{"is_backgrounded":true}`,
				TruncatedFields: []string{"patch"},
			},
			wantOK: true,
		},
		{
			// The non-zero DroppedTasks is the load-bearing part of this row: it is
			// a truncation report NOT called truncated_fields, and the roster
			// deliberately has no top-level truncated_fields for a grep to land on.
			// A row carrying 0 would pass against a mapping that never reads the
			// field, and a phone would then be told a capped roster is the whole
			// roster.
			name: "BackgroundTaskRoster -> background_task_roster, entries in order + dropped_tasks",
			ev: turnevent.BackgroundTaskRoster{
				Tasks: []turnevent.BackgroundTask{
					{TaskID: "task-1", TaskType: "local_bash", Description: "sleep 300"},
					{TaskID: "task-2", TaskType: "local_bash", Description: "tail -f log"},
				},
				DroppedTasks: 3,
			},
			tc:      tc,
			wantTyp: protocol.TypeBackgroundTaskRoster,
			wantPayload: protocol.BackgroundTaskRosterPayload{
				ConversationID: "c1",
				Tasks: []protocol.BackgroundTask{
					{TaskID: "task-1", TaskType: "local_bash", Description: "sleep 300"},
					{TaskID: "task-2", TaskType: "local_bash", Description: "tail -f log"},
				},
				DroppedTasks: 3,
			},
			wantOK: true,
		},
		{
			// A row's truncation report rides THAT row: populated on the second
			// entry and nil on the first, neither hoisted to the top level nor
			// flattened across entries.
			name: "BackgroundTaskRoster per-entry truncated_fields rides its own entry",
			ev: turnevent.BackgroundTaskRoster{
				Tasks: []turnevent.BackgroundTask{
					{TaskID: "task-1", TaskType: "local_bash", Description: "short"},
					{TaskID: "task-2", TaskType: "local_bash", Description: "cut…", TruncatedFields: []string{"description"}},
				},
			},
			tc:      tc,
			wantTyp: protocol.TypeBackgroundTaskRoster,
			wantPayload: protocol.BackgroundTaskRosterPayload{
				ConversationID: "c1",
				Tasks: []protocol.BackgroundTask{
					{TaskID: "task-1", TaskType: "local_bash", Description: "short"},
					{TaskID: "task-2", TaskType: "local_bash", Description: "cut…", TruncatedFields: []string{"description"}},
				},
				DroppedTasks: 0,
			},
			wantOK: true,
		},
		{
			// Empty is forwarded, not treated as absent: a zero-value event still
			// maps, carrying the conversation id and empty strings.
			name:    "BackgroundTaskUpdated zero value maps rather than dropping",
			ev:      turnevent.BackgroundTaskUpdated{},
			tc:      tc,
			wantTyp: protocol.TypeBackgroundTaskUpdated,
			wantPayload: protocol.BackgroundTaskUpdatedPayload{
				ConversationID: "c1",
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

// An empty roster is a SIGNAL, not an absence: the mapping forwards it (ok is
// true — it suppresses nothing) and the bytes it produces carry "tasks":[],
// never "tasks":null. Suppressing or nulling it would delete the payoff of the
// whole feature, since "nothing is alive" is precisely the reassurance #1240's
// symptom needs.
//
// The assertion runs on json.Marshal of the value MapEvent RETURNED, not on a
// payload the test built: turnevent.BackgroundTaskRoster.Tasks is nil both for
// an empty roster and when claude omits the key, so a test-constructed payload
// would only prove protocol's MarshalJSON works, not that the mapping reached
// it with the nil intact.
func TestMapEventBackgroundTaskRosterEmptyTasksOnTheWire(t *testing.T) {
	t.Parallel()

	tc := TurnContext{ConversationID: "c1", TurnID: "t1", Seq: 7}

	tests := []struct {
		name    string
		ev      turnevent.BackgroundTaskRoster
		want    []string
		notWant []string
	}{
		{
			name:    "empty roster forwards as tasks:[]",
			ev:      turnevent.BackgroundTaskRoster{},
			want:    []string{`"tasks":[]`, `"dropped_tasks":0`},
			notWant: []string{`"tasks":null`},
		},
		{
			// The control: [] is not what the marshaller emits for everything, so
			// the empty case above passes for the right reason.
			name: "one-entry roster carries the entry",
			ev: turnevent.BackgroundTaskRoster{
				Tasks: []turnevent.BackgroundTask{
					{TaskID: "task-1", TaskType: "local_bash", Description: "sleep 300"},
				},
			},
			want:    []string{`"task_id":"task-1"`, `"description":"sleep 300"`},
			notWant: []string{`"tasks":[]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ, payload, ok := MapEvent(tt.ev, tc)
			if !ok {
				t.Fatal("empty/small roster was suppressed by the mapping; it must be forwarded")
			}
			if typ != protocol.TypeBackgroundTaskRoster {
				t.Fatalf("typ: got %q, want %q", typ, protocol.TypeBackgroundTaskRoster)
			}
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal mapped payload: %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(string(b), want) {
					t.Fatalf("mapped bytes missing %s:\n%s", want, b)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(string(b), notWant) {
					t.Fatalf("mapped bytes carry %s:\n%s", notWant, b)
				}
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
