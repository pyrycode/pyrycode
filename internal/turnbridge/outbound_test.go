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
		{
			// The two readings differ, so a mapping that wired one field to both
			// wire keys goes red here rather than passing on a symmetric fixture.
			name:    "ThinkingProgress -> thinking_progress, both readings verbatim",
			ev:      turnevent.ThinkingProgress{EstimatedTokens: 184, EstimatedTokensDelta: 37},
			tc:      tc,
			wantTyp: protocol.TypeThinkingProgress,
			wantPayload: protocol.ThinkingProgressPayload{
				ConversationID: "c1", EstimatedTokens: 184, EstimatedTokensDelta: 37,
			},
			wantOK: true,
		},
		{
			// The "not turn-scoped" claim under TEST, not merely under comment: tc
			// here carries a non-empty TurnID and a non-zero Seq, and the expected
			// payload has no field either could land in. reflect.DeepEqual against
			// this literal is what makes it bite — a payload that grew a turn_id
			// field and populated it from tc would differ from the want value.
			// TestThinkingProgressPayloadHasNoTurnAddressing below covers the wire
			// bytes, which a struct comparison alone cannot.
			name:    "ThinkingProgress ignores turn addressing (not turn-scoped)",
			ev:      turnevent.ThinkingProgress{EstimatedTokens: 5, EstimatedTokensDelta: 5},
			tc:      TurnContext{ConversationID: "c1", TurnID: "t-must-not-appear", Seq: 42},
			wantTyp: protocol.TypeThinkingProgress,
			wantPayload: protocol.ThinkingProgressPayload{
				ConversationID: "c1", EstimatedTokens: 5, EstimatedTokensDelta: 5,
			},
			wantOK: true,
		},
		{
			// {0,0} is a legitimate reading, exactly as ApiRetry's {0,0} counter is
			// a legitimate "count unknown". No suppression branch: the producer's
			// rate bound already governs which lines earn an event, and a second,
			// differently-shaped filter here would silently diverge from it.
			name:    "ThinkingProgress zero value maps rather than dropping",
			ev:      turnevent.ThinkingProgress{},
			tc:      tc,
			wantTyp: protocol.TypeThinkingProgress,
			wantPayload: protocol.ThinkingProgressPayload{
				ConversationID: "c1",
			},
			wantOK: true,
		},
		{
			// Every string field carries a DISTINCT sentinel: status, limit_type and
			// conversation_id are same-typed neighbours, so placeholder repeats would
			// let a mapping that swapped two of them pass. ResetsAt is deliberately
			// not a plausible instant — a clamp, an abs() or an absent-means-now
			// rewrite all go red here rather than shipping.
			name: "RateLimited -> rate_limited, every field verbatim",
			ev: turnevent.RateLimited{
				Status:          "qq-status-sentinel",
				LimitType:       "zz-limittype-sentinel",
				ResetsAt:        -1,
				TruncatedFields: []string{"tf-alpha-sentinel", "tf-beta-sentinel"},
			},
			tc:      tc,
			wantTyp: protocol.TypeRateLimited,
			wantPayload: protocol.RateLimitedPayload{
				ConversationID:  "c1",
				Status:          "qq-status-sentinel",
				LimitType:       "zz-limittype-sentinel",
				ResetsAt:        -1,
				TruncatedFields: []string{"tf-alpha-sentinel", "tf-beta-sentinel"},
			},
			wantOK: true,
		},
		{
			// The far-future instant crosses unreformatted, and the nil truncation
			// stays NIL: reflect.DeepEqual distinguishes a nil []string from an empty
			// one, so a mapper that allocated []string{} fails here as well as on the
			// bytes (TestMapEventRateLimitedTruncatedFieldsOnTheWire below).
			name: "RateLimited far-future instant and nil truncation stay as claude left them",
			ev: turnevent.RateLimited{
				Status:    "qq-status-sentinel",
				LimitType: "zz-limittype-sentinel",
				ResetsAt:  4102444800,
			},
			tc:      tc,
			wantTyp: protocol.TypeRateLimited,
			wantPayload: protocol.RateLimitedPayload{
				ConversationID: "c1",
				Status:         "qq-status-sentinel",
				LimitType:      "zz-limittype-sentinel",
				ResetsAt:       4102444800,
			},
			wantOK: true,
		},
		{
			// The "not turn-scoped" claim under TEST: tc carries a conspicuous TurnID
			// and a non-zero Seq, and the expected payload has no field either could
			// land in. Mirrors the ThinkingProgress row above.
			name: "RateLimited ignores turn addressing (not turn-scoped)",
			ev: turnevent.RateLimited{
				Status:    "qq-status-sentinel",
				LimitType: "zz-limittype-sentinel",
			},
			tc:      TurnContext{ConversationID: "c1", TurnID: "t-must-not-appear", Seq: 42},
			wantTyp: protocol.TypeRateLimited,
			wantPayload: protocol.RateLimitedPayload{
				ConversationID: "c1",
				Status:         "qq-status-sentinel",
				LimitType:      "zz-limittype-sentinel",
			},
			wantOK: true,
		},
		{
			// Zero value maps rather than dropping. This is where an absent-means-now
			// rewrite of ResetsAt shows: 0 means claude did not report the instant,
			// not the epoch and not the current time. The gate that decides whether an
			// event exists at all is the producer's, not this adapter's.
			name:    "RateLimited zero value maps rather than dropping",
			ev:      turnevent.RateLimited{},
			tc:      tc,
			wantTyp: protocol.TypeRateLimited,
			wantPayload: protocol.RateLimitedPayload{
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

// truncated_fields is pinned in BOTH of its states, on the bytes, because the
// nil is the part that has to survive the mapping: RateLimitedPayload
// deliberately has no MarshalJSON (internal/protocol/interactive.go), so nothing
// normalises a nil afterwards — nothing-was-cut is an ABSENCE and must reach the
// wire as null. A mapper that allocated an empty slice would emit [] and tell a
// phone that claude's truncated text is complete, and nothing else in the tree
// would notice.
//
// The SHAPE is TestMapEventBackgroundTaskRosterEmptyTasksOnTheWire's above, with
// the assertion deliberately INVERTED: that test demands "tasks":[] and forbids
// "tasks":null, because BackgroundTaskRosterPayload owns a nil→[] MarshalJSON.
// Copying its polarity here would want "truncated_fields":[] — green against
// exactly the allocating mapper this test exists to catch.
//
// The assertion runs on json.Marshal of the value MapEvent RETURNED, never on a
// test-built payload. That discipline matters more here than in the roster test:
// with no MarshalJSON at all, a test-built payload proves nothing whatever about
// whether the mapping preserved the nil.
//
// Needles are the full "key":"value" pairs, never the bare values — a bare-value
// needle passes against a mapping that swapped two same-typed neighbours.
func TestMapEventRateLimitedTruncatedFieldsOnTheWire(t *testing.T) {
	t.Parallel()

	tc := TurnContext{ConversationID: "cc-conv-sentinel", TurnID: "t1", Seq: 7}

	tests := []struct {
		name    string
		ev      turnevent.RateLimited
		want    []string
		notWant []string
	}{
		{
			name: "nil truncation reaches the wire as null",
			ev: turnevent.RateLimited{
				Status:    "qq-status-sentinel",
				LimitType: "zz-limittype-sentinel",
				ResetsAt:  4102444800,
			},
			want: []string{
				`"truncated_fields":null`,
				`"conversation_id":"cc-conv-sentinel"`,
				`"status":"qq-status-sentinel"`,
				`"limit_type":"zz-limittype-sentinel"`,
				`"resets_at":4102444800`,
			},
			notWant: []string{`"truncated_fields":[]`},
		},
		{
			// The control: null is not what the mapping emits for everything, so the
			// nil row above passes for the right reason. Both members in ONE needle
			// pins member ORDER, not merely membership. The negative instant crosses
			// verbatim too, so a clamp or a reformat to RFC3339 goes red.
			name: "populated truncation crosses verbatim and in order",
			ev: turnevent.RateLimited{
				Status:          "qq-status-sentinel",
				LimitType:       "zz-limittype-sentinel",
				ResetsAt:        -1,
				TruncatedFields: []string{"tf-alpha-sentinel", "tf-beta-sentinel"},
			},
			want: []string{
				`"truncated_fields":["tf-alpha-sentinel","tf-beta-sentinel"]`,
				`"conversation_id":"cc-conv-sentinel"`,
				`"status":"qq-status-sentinel"`,
				`"limit_type":"zz-limittype-sentinel"`,
				`"resets_at":-1`,
			},
			notWant: []string{`"truncated_fields":null`, `"truncated_fields":[]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ, payload, ok := MapEvent(tt.ev, tc)
			if !ok {
				t.Fatal("the mapping suppressed a rate-limited event; it must be forwarded")
			}
			if typ != protocol.TypeRateLimited {
				t.Fatalf("typ: got %q, want %q", typ, protocol.TypeRateLimited)
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

// The frame is conversation-scoped, and the assertion runs on the BYTES
// json.Marshal produces from the value MapEvent returned — not on a struct
// comparison. A struct comparison cannot see a turn id that arrived through an
// embedded field or a marshaller, and "carries no turn_id" is a claim about what
// a phone decodes, so it is checked where a phone would see it. The turn context
// fed in is deliberately conspicuous: both its TurnID and its Seq would be
// greppable in the output if either leaked.
func TestMapEventThinkingProgressCarriesNoTurnAddressing(t *testing.T) {
	t.Parallel()

	typ, payload, ok := MapEvent(
		turnevent.ThinkingProgress{EstimatedTokens: 184, EstimatedTokensDelta: 37},
		TurnContext{ConversationID: "c1", TurnID: "t-must-not-appear", Seq: 42},
	)
	if !ok {
		t.Fatal("ThinkingProgress was dropped by the mapping; it must reach the wire")
	}
	if typ != protocol.TypeThinkingProgress {
		t.Fatalf("typ: got %q, want %q", typ, protocol.TypeThinkingProgress)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal mapped payload: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		`"conversation_id":"c1"`,
		`"estimated_tokens":184`,
		`"estimated_tokens_delta":37`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mapped bytes missing %s:\n%s", want, got)
		}
	}
	for _, notWant := range []string{`turn_id`, `t-must-not-appear`, `"seq"`, `42`} {
		if strings.Contains(got, notWant) {
			t.Errorf("mapped bytes carry turn addressing %q:\n%s", notWant, got)
		}
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
