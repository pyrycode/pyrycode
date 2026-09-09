package turnbridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestMapEventOutbound(t *testing.T) {
	t.Parallel()

	tc := TurnContext{ConversationID: "c1", TurnID: "t1", Seq: 7}

	// The model-announced sentinels. Mixed case is deliberate — see the verbatim
	// row below — and neither contains a character encoding/json escapes, matching
	// the discipline the rate-limited fixtures state. overCapModel is kept out of
	// the short row so that row's failure output stays legible.
	const modelSentinel = "QQ-Model-Sentinel-ZZ"
	overCapModel := "QQ-OverCap-Model-" + strings.Repeat("M", 300)

	// The model-list over-cap fixtures. Each is longer than EVERY bound a developer
	// could reach for — the producer's own (streamsup's maxModelResolved /
	// maxModelValue / maxModelDisplayName at 256, maxModelEffortLevel at 32) and
	// this file's maxSummaryLen (200) and maxResultSummaryRunes, neither of which is
	// applicable here — so a re-cap mutant at any of them goes red.
	overCapLevel := "QQ-OverCap-Level-" + strings.Repeat("L", 300)
	overCapValue := "QQ-OverCap-Value-" + strings.Repeat("V", 300)
	overCapDisplay := "QQ-OverCap-Display-" + strings.Repeat("D", 300)

	// The slash-command over-cap fixtures, the model-list ones' discipline applied
	// to this variant's own four bounded dimensions. Each string is longer than
	// EVERY bound a developer could reach for — the producer's own (streamsup's
	// maxSlashCommandName / maxSlashCommandArgumentHint / maxSlashCommandDescription
	// at 256, maxSlashCommandAlias at 64) and this file's maxSummaryLen (200) and
	// maxResultSummaryRunes, neither of which is applicable here — so a re-cap
	// mutant at any of them goes red. overCapAliasList is over the COUNT cap
	// (maxSlashCommandAliasCount, 8) as well as carrying over-cap elements, because
	// "aliases" is the one dimension a cut can shorten the LIST of and not only a
	// value of.
	//
	// overCapAliases is a FUNCTION rather than a slice so the row below can hand ev
	// and wantPayload separately-allocated values with equal contents — the
	// separate-slice-literals rule those two carry everywhere else in this table,
	// which a shared variable would quietly break.
	overCapCommandName := "QQ-OverCap-Name-" + strings.Repeat("N", 300)
	overCapCommandHint := "QQ-OverCap-Hint-" + strings.Repeat("H", 300)
	overCapCommandDescription := "QQ-OverCap-Description-" + strings.Repeat("D", 300)
	overCapAliases := func() []string {
		out := make([]string, 0, 9)
		for i := range 9 {
			out = append(out, "QQ-OverCap-Alias-"+strings.Repeat(string(rune('a'+i)), 100))
		}
		return out
	}

	// One over-cap result fixture, used by a failed row and a completed row that
	// differ ONLY in Status, so the pair pins the bound rather than the flag:
	// is_error does not change how much of a result reaches the wire (#1680 AC 3).
	// resultSummary cannot see is_error — the flag is derived here at the MapEvent
	// level — which is why this belongs in this table and not in TestResultSummary.
	overCapResult := strings.Repeat("r", maxResultSummaryRunes+100)
	cutResult := strings.Repeat("r", maxResultSummaryRunes) + "…"

	// FUNCTIONS rather than variables, for overCapAliases' reason two paragraphs up and
	// with more force: a pointer shared between ev and wantPayload would make
	// reflect.DeepEqual pass on the fact that both sides name one variable rather than
	// on the value claude sent. Separately-allocated pointers with equal contents is
	// this table's standing rule and the only form in which the pointee is checked.
	//
	// The sentinel is NEGATIVE on purpose, which is load-bearing rather than stylistic:
	// utilization is not a bounded fraction, so a clamp to 0..1, an abs() or a
	// percent-scaling rewrite goes red here rather than shipping.
	utilizationSentinel := func() *float64 { v := -7.25; return &v }
	zeroUtilization := func() *float64 { v := 0.0; return &v }

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
				Input: map[string]string{"command": "ls"},
			},
			wantOK: true,
		},
		{
			// #2191. The row above is the main-thread case and pins the field EMPTY,
			// which is what makes this pair non-vacuous: a mapping that dropped the
			// value passes that row alone, and one that stamped a constant passes this
			// row alone. The value crosses verbatim under a renamed key —
			// ParentToolCallID on the event, parent_tool_use_id on the wire, matching
			// the tool_use_id beside it — so this row also pins the rename.
			name: "ToolStart carries the spawning Agent call under the wire's name",
			ev: turnevent.ToolStart{
				ToolCallID:       "tool-9",
				ParentToolCallID: "toolu_01Agent",
				Title:            "Read",
				Kind:             turnevent.ToolKindRead,
				RawInput:         json.RawMessage(`{"file_path":"/tmp/x"}`),
			},
			tc:      tc,
			wantTyp: protocol.TypeToolUse,
			wantPayload: protocol.ToolUsePayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-9",
				ParentToolUseID: "toolu_01Agent",
				Name:            "Read", InputSummary: `{"file_path":"/tmp/x"}`,
				Input: map[string]string{"file_path": "/tmp/x"},
			},
			wantOK: true,
		},
		{
			// The result half of the same pair, and it must carry the SAME id so a
			// client can group the two rows it already joins on tool_use_id.
			name: "ToolUpdate carries the spawning Agent call under the wire's name",
			ev: turnevent.ToolUpdate{
				ToolCallID:       "tool-9",
				ParentToolCallID: "toolu_01Agent",
				Status:           turnevent.ToolStatusCompleted,
				Content:          turnevent.TextContent{Text: "ok"},
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-9",
				ParentToolUseID: "toolu_01Agent",
				IsError:         false, ResultSummary: "ok",
			},
			wantOK: true,
		},
		{
			// The id report token is the DAEMON's tool_call_id on the event and this
			// frame's tool_use_id on the wire; every other token already names a key
			// this frame carries and crosses untouched. Separate slice literals for ev
			// and wantPayload, this table's standing rule, so a rename that mutated in
			// place could not make both sides agree by aliasing.
			name: "ToolCallDenied -> tool_denied renames only the id report token",
			ev: turnevent.ToolCallDenied{
				ToolName:           "Bash",
				ToolCallID:         "tool-1",
				Message:            "requested permissions to use Bash",
				DecisionReasonType: "rule",
				DecisionReason:     "matches a deny rule",
				TruncatedFields:    []string{"message", "decision_reason"},
				DroppedFields:      []string{"tool_name", "tool_call_id", "decision_reason_type"},
			},
			tc:      tc,
			wantTyp: protocol.TypeToolDenied,
			wantPayload: protocol.ToolDeniedPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-1",
				ToolName: "Bash", DecisionReasonType: "rule",
				DecisionReason:  "matches a deny rule",
				Message:         "requested permissions to use Bash",
				TruncatedFields: []string{"message", "decision_reason"},
				DroppedFields:   []string{"tool_name", "tool_use_id", "decision_reason_type"},
			},
			wantOK: true,
		},
		{
			// Nothing cut and nothing dropped: both slices stay nil rather than
			// becoming empty non-nil ones. nil is what reaches the wire as null, and an
			// allocated [] would tell a phone claude's cut text is complete.
			name: "ToolCallDenied with nothing cut -> both report slices stay nil",
			ev: turnevent.ToolCallDenied{
				ToolName:   "Bash",
				ToolCallID: "tool-2",
				Message:    "requested permissions to use Bash",
			},
			tc:      tc,
			wantTyp: protocol.TypeToolDenied,
			wantPayload: protocol.ToolDeniedPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-2",
				ToolName: "Bash", Message: "requested permissions to use Bash",
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
			// #2024. ResultDetail maps straight through, uncapped: its producer
			// (streamsup's readLineCount) bounds it at construction, and it carries
			// no claude-supplied byte for a cap here to defend against.
			name: "ToolUpdate with a read's line count -> tool_result result_detail",
			ev: turnevent.ToolUpdate{
				ToolCallID:   "tool-6",
				Status:       turnevent.ToolStatusCompleted,
				Content:      turnevent.TextContent{Text: "file contents"},
				ResultDetail: "110 of 1676 lines",
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-6",
				IsError: false, ResultSummary: "file contents",
				ResultDetail: "110 of 1676 lines",
			},
			wantOK: true,
		},
		{
			// The common case by a wide margin: about 95% of calls are tools with
			// no meaningful count, and an empty ResultDetail must survive the
			// mapping as empty rather than acquiring a placeholder.
			name: "ToolUpdate with no count -> tool_result result_detail empty",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-7",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: "ok"},
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-7",
				IsError: false, ResultSummary: "ok",
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate failed -> over-cap error result truncated",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-5",
				Status:     turnevent.ToolStatusFailed,
				Content:    turnevent.TextContent{Text: overCapResult},
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-5",
				IsError: true, ResultSummary: cutResult,
			},
			wantOK: true,
		},
		{
			name: "ToolUpdate completed -> over-cap result truncated at the same bound",
			ev: turnevent.ToolUpdate{
				ToolCallID: "tool-6",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: overCapResult},
			},
			tc:      tc,
			wantTyp: protocol.TypeToolResult,
			wantPayload: protocol.ToolResultPayload{
				ConversationID: "c1", TurnID: "t1", ToolUseID: "tool-6",
				IsError: false, ResultSummary: cutResult,
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
			// #2223's stop shape reaches the payload, and stop_reason is STILL
			// "end_turn" on the same frame — the two rows above are the other half
			// of that claim, proving an event carrying no stop shape still produces
			// exactly today's payload.
			//
			// The three values are mutually distinct and none is derivable from
			// another: a budget stop whose subtype is error_max_turns, whose
			// terminal_reason is a different token, and whose is_error is true while
			// the wire's stop_reason stays end_turn. A mapper that crossed two
			// fields, or that derived is_error from the subtype, reddens here.
			name: "TurnEnd carries claude's stop shape beside an unchanged stop_reason",
			ev: turnevent.TurnEnd{
				Reason:         turnevent.TurnEndReasonEndTurn,
				Outcome:        "error_max_turns",
				IsError:        true,
				TerminalReason: "max_turns",
			},
			tc:      tc,
			wantTyp: protocol.TypeTurnEnd,
			wantPayload: protocol.TurnEndPayload{
				ConversationID: "c1", TurnID: "t1", StopReason: "end_turn",
				Outcome: "error_max_turns", IsError: true, TerminalReason: "max_turns",
			},
			wantOK: true,
		},
		{
			// #2224's category maps through, and it is the ONLY field set on this
			// row: the event carries no stop shape at all, so a mapper that folded
			// the category into one of #2223's fields — or that only populated it
			// when a stop shape was present — reddens here rather than passing on a
			// row where every field happened to be set.
			name: "TurnEnd carries the assistant-level error category",
			ev: turnevent.TurnEnd{
				Reason:        turnevent.TurnEndReasonEndTurn,
				ErrorCategory: "rate_limit",
			},
			tc:      tc,
			wantTyp: protocol.TypeTurnEnd,
			wantPayload: protocol.TurnEndPayload{
				ConversationID: "c1", TurnID: "t1", StopReason: "end_turn",
				ErrorCategory: "rate_limit",
			},
			wantOK: true,
		},
		{
			// The category is mapped UNCAPPED here, its producer having bounded it at
			// construction. A 512-byte value — twice maxTurnEndStopField, a length
			// streamsup could never emit — must therefore arrive intact rather than
			// dropped or cut, which is what proves the second bound really is absent
			// rather than merely untested.
			name: "TurnEnd error category is not re-bounded by the mapper",
			ev: turnevent.TurnEnd{
				Reason:        turnevent.TurnEndReasonEndTurn,
				ErrorCategory: strings.Repeat("c", 512),
			},
			tc:      tc,
			wantTyp: protocol.TypeTurnEnd,
			wantPayload: protocol.TurnEndPayload{
				ConversationID: "c1", TurnID: "t1", StopReason: "end_turn",
				ErrorCategory: strings.Repeat("c", 512),
			},
			wantOK: true,
		},
		{
			// #2260's four numbers map through, and they are the ONLY fields set on
			// this row for the category row's reason: a mapper that populated them
			// only alongside a stop shape would pass a row where everything happened
			// to be set. The values are claude's own from a committed capture and are
			// mutually non-derivable — no duration is a multiple of the turn count and
			// the cost scales neither — so a crossed assignment reddens.
			//
			// duration_api_ms EXCEEDS duration_ms here deliberately. That is the
			// ordinary case on this wire, and it is what a mapper "correcting" the
			// pair into a per-turn figure would have to break.
			name: "TurnEnd carries claude's turn totals undifferenced",
			ev: turnevent.TurnEnd{
				Reason:        turnevent.TurnEndReasonEndTurn,
				DurationMS:    24594,
				DurationAPIMS: 27064,
				NumTurns:      5,
				CostUSDTotal:  0.1608898,
			},
			tc:      tc,
			wantTyp: protocol.TypeTurnEnd,
			wantPayload: protocol.TurnEndPayload{
				ConversationID: "c1", TurnID: "t1", StopReason: "end_turn",
				DurationMS: 24594, DurationAPIMS: 27064, NumTurns: 5, CostUSDTotal: 0.1608898,
			},
			wantOK: true,
		},
		{
			// The zeros claude itself sends, carried as claude sent them. The row is
			// not a duplicate of the empty-event rows above: those reach zero because
			// nothing was set, this one because a REAL turn reported duration_api_ms 0
			// and num_turns 0 beside a non-zero duration and cost. A mapper that
			// treated a zero as "nothing to report" and suppressed the pair — or that
			// substituted the duration for the missing API figure — reddens here.
			name: "TurnEnd carries the zeros claude sends beside non-zero siblings",
			ev: turnevent.TurnEnd{
				Reason:       turnevent.TurnEndReasonEndTurn,
				DurationMS:   15617,
				CostUSDTotal: 0.0408803,
			},
			tc:      tc,
			wantTyp: protocol.TypeTurnEnd,
			wantPayload: protocol.TurnEndPayload{
				ConversationID: "c1", TurnID: "t1", StopReason: "end_turn",
				DurationMS: 15617, CostUSDTotal: 0.0408803,
			},
			wantOK: true,
		},
		{
			// The window fields are NOT published, and this is the row that keeps
			// that true now that the variant has publishable fields at all: an event
			// carrying windows produces a payload with no trace of them. Without it,
			// a future arm switching to an embedded struct would leak the pair and
			// nothing would say so.
			name: "TurnEnd model windows stay off the wire",
			ev: turnevent.TurnEnd{
				Reason:              turnevent.TurnEndReasonEndTurn,
				ModelWindows:        []turnevent.ModelWindow{{ModelID: "claude-haiku-4-5", WindowTokens: 200000}},
				DroppedModelWindows: 3,
			},
			tc:      tc,
			wantTyp: protocol.TypeTurnEnd,
			wantPayload: protocol.TurnEndPayload{
				ConversationID: "c1", TurnID: "t1", StopReason: "end_turn",
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
			name:    "Compacting cleared with no outcome -> compacting, conversation_id + active",
			ev:      turnevent.Compacting{Active: false},
			tc:      tc,
			wantTyp: protocol.TypeCompacting,
			wantPayload: protocol.CompactingPayload{
				ConversationID: "c1", Active: false,
			},
			wantOK: true,
		},
		{
			// #2236. The two strings ride across UNCAPPED here for the reason the
			// TurnEnd arm states beside them: streamsup bounds both at construction
			// (maxCompactField), and a second bound in this adapter would be a number
			// to keep in step with one that already holds.
			name: "Compacting cleared with claude's outcome -> compacting carries both strings",
			ev: turnevent.Compacting{
				Active:    false,
				Result:    "failed",
				ErrorText: "context window still over budget",
			},
			tc:      tc,
			wantTyp: protocol.TypeCompacting,
			wantPayload: protocol.CompactingPayload{
				ConversationID: "c1", Active: false,
				Result: "failed", ErrorText: "context window still over budget",
			},
			wantOK: true,
		},
		{
			// The rising edge's fields are empty at the producer, so this row proves
			// the ARM does not invent them — it maps what the event holds and no more.
			name:    "Compacting raised -> compacting, no outcome yet",
			ev:      turnevent.Compacting{Active: true},
			tc:      tc,
			wantTyp: protocol.TypeCompacting,
			wantPayload: protocol.CompactingPayload{
				ConversationID: "c1", Active: true,
			},
			wantOK: true,
		},
		{
			// #2237. A separate frame from compacting above rather than a wider one,
			// because claude states these values on a line that arrives AFTER the falling
			// edge has shipped. Like the status peers it carries conversation identity
			// only: tc's non-empty TurnID and non-zero Seq are ignored and the payload has
			// no field either could land in.
			name: "CompactionBoundary -> compaction_boundary, trigger and both counts verbatim",
			ev: turnevent.CompactionBoundary{
				Trigger:    "manual",
				PreTokens:  intPtr(23600),
				PostTokens: intPtr(2612),
			},
			tc:      tc,
			wantTyp: protocol.TypeCompactionBoundary,
			wantPayload: protocol.CompactionBoundaryPayload{
				ConversationID: "c1",
				Trigger:        "manual",
				PreTokens:      intPtr(23600),
				PostTokens:     intPtr(2612),
			},
			wantOK: true,
		},
		{
			// The row that would pass with an int field and fail the ticket. reflect.DeepEqual
			// on two *int distinguishes nil from a pointer to zero, so an arm that
			// defaulted an absent count — or a payload that declared plain ints — reddens
			// here. A trigger the producer dropped for length arrives as "" and is mapped
			// as "": this adapter neither re-bounds nor substitutes.
			name: "CompactionBoundary with an absent post count keeps it absent, not zero",
			ev: turnevent.CompactionBoundary{
				Trigger:   "",
				PreTokens: intPtr(23600),
			},
			tc:      tc,
			wantTyp: protocol.TypeCompactionBoundary,
			wantPayload: protocol.CompactionBoundaryPayload{
				ConversationID: "c1",
				Trigger:        "",
				PreTokens:      intPtr(23600),
				PostTokens:     nil,
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
			// The SECOND producing subtype's shape (#2245). The empty Patch is the
			// load-bearing half: a task_notification line carries no patch key and
			// the daemon synthesizes none, so an adapter that filled the field from
			// anything at all goes red here. Status and Summary carry values that
			// cannot be confused with each other or with TaskID, so a mapping that
			// crossed two wire keys does not pass on a symmetric fixture.
			name: "BackgroundTaskUpdated from task_notification -> terminal state, no patch",
			ev: turnevent.BackgroundTaskUpdated{
				TaskID:          "task-1",
				Status:          "completed",
				Summary:         "cat /tmp/fifo",
				TruncatedFields: []string{"summary"},
			},
			tc:      tc,
			wantTyp: protocol.TypeBackgroundTaskUpdated,
			wantPayload: protocol.BackgroundTaskUpdatedPayload{
				ConversationID:  "c1",
				TaskID:          "task-1",
				Status:          "completed",
				Summary:         "cat /tmp/fifo",
				TruncatedFields: []string{"summary"},
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
				Utilization:     utilizationSentinel(),
				TruncatedFields: []string{"tf-alpha-sentinel", "tf-beta-sentinel"},
			},
			tc:      tc,
			wantTyp: protocol.TypeRateLimited,
			wantPayload: protocol.RateLimitedPayload{
				ConversationID:  "c1",
				Status:          "qq-status-sentinel",
				LimitType:       "zz-limittype-sentinel",
				ResetsAt:        -1,
				Utilization:     utilizationSentinel(),
				TruncatedFields: []string{"tf-alpha-sentinel", "tf-beta-sentinel"},
			},
			wantOK: true,
		},
		{
			// The far-future instant crosses unreformatted, and the nil truncation
			// stays NIL: reflect.DeepEqual distinguishes a nil []string from an empty
			// one, so a mapper that allocated []string{} fails here as well as on the
			// bytes (TestMapEventRateLimitedTruncatedFieldsOnTheWire below).
			//
			// THE NIL Utilization IS THE SAME KIND OF CLAIM and is this row's second
			// job: nil means claude stated no reading, and a mapper that helpfully
			// substituted a zero would tell a phone the window is FRESH. DeepEqual
			// distinguishes a nil *float64 from a pointer to 0, so the pairing with the
			// explicit-zero row below is what pins the distinction — neither row alone
			// does, exactly as in streamsup's own absent-versus-zero table.
			name: "RateLimited far-future instant, nil truncation and nil utilization stay as claude left them",
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
			// The other half of that pair. An explicit zero is a READING — a fresh
			// window — and it must arrive as a pointer to 0 rather than collapsing into
			// the nil above. A plain float64 field on either type passes this row and
			// fails the one above it, which is why both are here.
			name: "RateLimited explicit zero utilization crosses as zero, not as absent",
			ev: turnevent.RateLimited{
				Status:      "qq-status-sentinel",
				LimitType:   "zz-limittype-sentinel",
				Utilization: zeroUtilization(),
			},
			tc:      tc,
			wantTyp: protocol.TypeRateLimited,
			wantPayload: protocol.RateLimitedPayload{
				ConversationID: "c1",
				Status:         "qq-status-sentinel",
				LimitType:      "zz-limittype-sentinel",
				Utilization:    zeroUtilization(),
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
		{
			// The sentinel is MIXED CASE on purpose, and that is the load-bearing
			// choice rather than a stylistic one: "no lowercasing" is a named property
			// of this value, and an all-lowercase sentinel (the shape the rate-limited
			// rows above use, correctly, for their own hazards) survives a mapper that
			// ran strings.ToLower. tc.ConversationID is already distinct from it, so a
			// mapping that swapped the two fields in either direction goes red here
			// without a second sentinel.
			name: "ModelAnnounced -> model_announced, every field verbatim",
			ev: turnevent.ModelAnnounced{
				Model:     modelSentinel,
				Truncated: true,
			},
			tc:      tc,
			wantTyp: protocol.TypeModelAnnounced,
			wantPayload: protocol.ModelAnnouncedPayload{
				ConversationID: "c1",
				Model:          modelSentinel,
				Truncated:      true,
			},
			wantOK: true,
		},
		{
			// The re-cap mutant's row. overCapModel is longer than BOTH bounds a
			// developer could reach for — the producer's maxModelField (256, where the
			// value was already bounded at construction) and this package's own
			// maxSummaryLen (200, which is the tool-précis cap and NOT applicable
			// here) — so a re-cap at either goes red rather than shipping. A short
			// sentinel survives both, which is why the verbatim row above cannot
			// double as this one.
			name: "ModelAnnounced over-cap model crosses uncut",
			ev: turnevent.ModelAnnounced{
				Model:     overCapModel,
				Truncated: true,
			},
			tc:      tc,
			wantTyp: protocol.TypeModelAnnounced,
			wantPayload: protocol.ModelAnnouncedPayload{
				ConversationID: "c1",
				Model:          overCapModel,
				Truncated:      true,
			},
			wantOK: true,
		},
		{
			// The "not turn-scoped" claim under TEST: tc carries a conspicuous TurnID
			// and a non-zero Seq, and the expected payload has no field either could
			// land in. Mirrors the RateLimited and ThinkingProgress rows above.
			name:    "ModelAnnounced ignores turn addressing (not turn-scoped)",
			ev:      turnevent.ModelAnnounced{Model: modelSentinel},
			tc:      TurnContext{ConversationID: "c1", TurnID: "t-must-not-appear", Seq: 42},
			wantTyp: protocol.TypeModelAnnounced,
			wantPayload: protocol.ModelAnnouncedPayload{
				ConversationID: "c1",
				Model:          modelSentinel,
			},
			wantOK: true,
		},
		{
			// Zero value maps rather than dropping — the absence of an empty-model
			// suppression branch, under test. The gate that decides whether the event
			// exists at all is the producer's (it does not emit on an empty model);
			// a second, differently-shaped filter here would silently diverge from it.
			// Also supplies the Truncated: false polarity the rows above do not.
			name:    "ModelAnnounced zero value maps rather than dropping",
			ev:      turnevent.ModelAnnounced{},
			tc:      tc,
			wantTyp: protocol.TypeModelAnnounced,
			wantPayload: protocol.ModelAnnouncedPayload{
				ConversationID: "c1",
			},
			wantOK: true,
		},
		{
			// Every field of every row, 1:1. Mixed-case sentinels on all three
			// strings for the ModelAnnounced verbatim row's reason — an
			// all-lowercase sentinel survives a mapper that ran strings.ToLower.
			// The effort levels are claude's MEASURED order (low, medium, high,
			// xhigh, max), which is neither alphabetical nor sorted, so a sort or a
			// canonicalisation goes red. SupportsAutoMode is true on one entry and
			// false on the other, so a flag defaulted in EITHER direction goes red.
			// DroppedModels is neither 0 nor len(Models), so a constant and a
			// recomputation from the payload's own row count are both caught. The
			// two entries differ, so a reversal or a re-sort of the outer slice is
			// caught as well.
			//
			// ev and wantPayload carry SEPARATE slice literals on purpose. Sharing
			// one backing array would let a mapper that sorted, deduped or filtered
			// IN PLACE mutate the expectation alongside the input and stay green —
			// and in production that same mutation would corrupt the model list
			// cmd/pyry's sessionModelHold retains, across two goroutines.
			name: "ModelList -> model_list, every field verbatim",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{
					{
						ResolvedModel:    "QQ-Resolved-Alpha-ZZ",
						Value:            "QQ-Value-Alpha-ZZ",
						DisplayName:      "QQ-Display-Alpha-ZZ",
						EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
						SupportsAutoMode: true,
						TruncatedFields:  []string{"resolved_model", "effort_levels"},
					},
					{
						ResolvedModel:    "QQ-Resolved-Beta-ZZ",
						Value:            "QQ-Value-Beta-ZZ",
						DisplayName:      "QQ-Display-Beta-ZZ",
						EffortLevels:     []string{"max", "low"},
						SupportsAutoMode: false,
						TruncatedFields:  []string{"display_name"},
					},
				},
				DroppedModels: 3,
			},
			tc:      tc,
			wantTyp: protocol.TypeModelList,
			wantPayload: protocol.ModelListPayload{
				ConversationID: "c1",
				Models: []protocol.ModelOption{
					{
						ResolvedModel:    "QQ-Resolved-Alpha-ZZ",
						Value:            "QQ-Value-Alpha-ZZ",
						DisplayName:      "QQ-Display-Alpha-ZZ",
						EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
						SupportsAutoMode: true,
						TruncatedFields:  []string{"resolved_model", "effort_levels"},
					},
					{
						ResolvedModel:    "QQ-Resolved-Beta-ZZ",
						Value:            "QQ-Value-Beta-ZZ",
						DisplayName:      "QQ-Display-Beta-ZZ",
						EffortLevels:     []string{"max", "low"},
						SupportsAutoMode: false,
						TruncatedFields:  []string{"display_name"},
					},
				},
				DroppedModels: 3,
			},
			wantOK: true,
		},
		{
			// A row that had nothing cut keeps a NIL TruncatedFields, and a row
			// whose effort menu claude omitted keeps a nil EffortLevels: an
			// allocating mapper goes red here because reflect.DeepEqual reports
			// false for nil against []string{}, whatever slices.Equal would say for
			// the same pair. The byte test below pins the same two nils where a
			// phone sees them — and there they mean OPPOSITE things.
			name: "ModelList row with nothing cut keeps nil slices",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{
					{
						ResolvedModel: "QQ-Resolved-Bare-ZZ",
						Value:         "QQ-Value-Bare-ZZ",
						DisplayName:   "QQ-Display-Bare-ZZ",
					},
				},
			},
			tc:      tc,
			wantTyp: protocol.TypeModelList,
			wantPayload: protocol.ModelListPayload{
				ConversationID: "c1",
				Models: []protocol.ModelOption{
					{
						ResolvedModel: "QQ-Resolved-Bare-ZZ",
						Value:         "QQ-Value-Bare-ZZ",
						DisplayName:   "QQ-Display-Bare-ZZ",
					},
				},
			},
			wantOK: true,
		},
		{
			// The re-cap mutant's row, the ModelAnnounced over-cap row's discipline
			// applied to all four of this variant's bounded text dimensions at once.
			// The producer bounded every one of them AT CONSTRUCTION, so a second
			// cap here — at the producer's bound or at either of this file's own,
			// which are not applicable — goes red rather than shipping.
			name: "ModelList over-cap strings and levels cross uncut",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{
					{
						ResolvedModel:   overCapModel,
						Value:           overCapValue,
						DisplayName:     overCapDisplay,
						EffortLevels:    []string{overCapLevel},
						TruncatedFields: []string{"resolved_model", "value", "display_name", "effort_levels"},
					},
				},
			},
			tc:      tc,
			wantTyp: protocol.TypeModelList,
			wantPayload: protocol.ModelListPayload{
				ConversationID: "c1",
				Models: []protocol.ModelOption{
					{
						ResolvedModel:   overCapModel,
						Value:           overCapValue,
						DisplayName:     overCapDisplay,
						EffortLevels:    []string{overCapLevel},
						TruncatedFields: []string{"resolved_model", "value", "display_name", "effort_levels"},
					},
				},
			},
			wantOK: true,
		},
		{
			// The "not turn-scoped" claim under test: tc carries a conspicuous
			// TurnID and a non-zero Seq, and the expected payload has no field
			// either could land in. Mirrors the ModelAnnounced row above.
			name: "ModelList ignores turn addressing (not turn-scoped)",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{Value: "QQ-Value-Alpha-ZZ"}},
			},
			tc:      TurnContext{ConversationID: "c1", TurnID: "t-must-not-appear", Seq: 42},
			wantTyp: protocol.TypeModelList,
			wantPayload: protocol.ModelListPayload{
				ConversationID: "c1",
				Models:         []protocol.ModelOption{{Value: "QQ-Value-Alpha-ZZ"}},
			},
			wantOK: true,
		},
		{
			// Zero value maps rather than dropping — the absence of an empty-Models
			// suppression branch, under test. The gate that decides whether the
			// event exists at all is the producer's (turnevent.ModelList's Models
			// documents it as never empty); a second, differently-shaped filter here
			// would silently diverge from it. The nil Models stays nil at the struct
			// level, ModelListPayload.MarshalJSON owning nil→[] on the wire.
			name:    "ModelList zero value maps rather than dropping",
			ev:      turnevent.ModelList{},
			tc:      tc,
			wantTyp: protocol.TypeModelList,
			wantPayload: protocol.ModelListPayload{
				ConversationID: "c1",
			},
			wantOK: true,
		},
		{
			// Every field of every row, 1:1 — the ModelList row above's discipline
			// applied to the OTHER inventory the same initialize reply carries.
			// Mixed-case sentinels on all three strings and on both aliases, so a
			// mapper that ran strings.ToLower goes red. The aliases are claude's own
			// order, deliberately NOT alphabetical, so a sort or a dedupe is caught.
			// The two entries differ in every dimension — one carries aliases and a
			// cut report, the other neither — so a reversal or a re-sort of the outer
			// slice is caught, and so is a mapper that copied row 0 twice.
			// DroppedCommands is neither 0 nor len(Commands), so a constant and a
			// recomputation from the payload's own row count are both caught.
			//
			// ev and wantPayload carry SEPARATE slice literals for the ModelList row's
			// reason, and here it is the rule AC 4 states rather than a precaution:
			// the arm shares Aliases and TruncatedFields backing arrays with the event
			// BY DESIGN, so an in-place sort or dedupe would mutate the expectation
			// alongside the input and stay green — while in production it would
			// corrupt the list #2005 reads on a relay-leg goroutine.
			name: "SlashCommandList -> slash_command_list, every field verbatim",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{
					{
						Name:            "QQ-Name-Alpha-ZZ",
						ArgumentHint:    "QQ-Hint-Alpha-ZZ",
						Description:     "QQ-Description-Alpha-ZZ",
						Aliases:         []string{"QQ-Reset-ZZ", "QQ-New-ZZ"},
						TruncatedFields: []string{"description", "aliases"},
					},
					{
						Name:         "QQ-Name-Beta-ZZ",
						ArgumentHint: "QQ-Hint-Beta-ZZ",
						Description:  "QQ-Description-Beta-ZZ",
					},
				},
				DroppedCommands: 3,
			},
			tc:      tc,
			wantTyp: protocol.TypeSlashCommandList,
			wantPayload: protocol.SlashCommandListPayload{
				ConversationID: "c1",
				Commands: []protocol.SlashCommand{
					{
						Name:            "QQ-Name-Alpha-ZZ",
						ArgumentHint:    "QQ-Hint-Alpha-ZZ",
						Description:     "QQ-Description-Alpha-ZZ",
						Aliases:         []string{"QQ-Reset-ZZ", "QQ-New-ZZ"},
						TruncatedFields: []string{"description", "aliases"},
					},
					{
						Name:         "QQ-Name-Beta-ZZ",
						ArgumentHint: "QQ-Hint-Beta-ZZ",
						Description:  "QQ-Description-Beta-ZZ",
					},
				},
				DroppedCommands: 3,
			},
			wantOK: true,
		},
		{
			// AC 2 at the struct level, on BOTH of the entry's list fields at once.
			// An allocating mapper goes red here because reflect.DeepEqual reports
			// false for nil against []string{}, whatever slices.Equal would say for
			// the same pair — the trap turnevent.SlashCommand.Aliases' doc names. The
			// byte test below pins the same two nils where a phone sees them, and
			// there they mean OPPOSITE things.
			name: "SlashCommandList row with nothing cut keeps nil slices",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{
					{
						Name:         "QQ-Name-Bare-ZZ",
						ArgumentHint: "QQ-Hint-Bare-ZZ",
						Description:  "QQ-Description-Bare-ZZ",
					},
				},
			},
			tc:      tc,
			wantTyp: protocol.TypeSlashCommandList,
			wantPayload: protocol.SlashCommandListPayload{
				ConversationID: "c1",
				Commands: []protocol.SlashCommand{
					{
						Name:         "QQ-Name-Bare-ZZ",
						ArgumentHint: "QQ-Hint-Bare-ZZ",
						Description:  "QQ-Description-Bare-ZZ",
					},
				},
			},
			wantOK: true,
		},
		{
			// AC 3's re-cap mutant, the ModelList over-cap row's discipline applied to
			// all four of this variant's bounded text dimensions at once. Every
			// fixture is longer than EVERY bound a developer could reach for — the
			// producer's own (streamsup's maxSlashCommandName /
			// maxSlashCommandArgumentHint / maxSlashCommandDescription at 256,
			// maxSlashCommandAlias at 64) and this file's maxSummaryLen (200) and
			// maxResultSummaryRunes, neither of which is applicable here. The alias
			// COUNT is over maxSlashCommandAliasCount (8) as well, so a re-cap of the
			// list's length and not just its elements goes red too.
			name: "SlashCommandList over-cap strings and aliases cross uncut",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{
					{
						Name:            overCapCommandName,
						ArgumentHint:    overCapCommandHint,
						Description:     overCapCommandDescription,
						Aliases:         overCapAliases(),
						TruncatedFields: []string{"name", "argument_hint", "description", "aliases"},
					},
				},
			},
			tc:      tc,
			wantTyp: protocol.TypeSlashCommandList,
			wantPayload: protocol.SlashCommandListPayload{
				ConversationID: "c1",
				Commands: []protocol.SlashCommand{
					{
						Name:            overCapCommandName,
						ArgumentHint:    overCapCommandHint,
						Description:     overCapCommandDescription,
						Aliases:         overCapAliases(),
						TruncatedFields: []string{"name", "argument_hint", "description", "aliases"},
					},
				},
			},
			wantOK: true,
		},
		{
			// The "not turn-scoped" claim under test: tc carries a conspicuous TurnID
			// and a non-zero Seq, and the expected payload has no field either could
			// land in. Mirrors the ModelList row above.
			name: "SlashCommandList ignores turn addressing (not turn-scoped)",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{Name: "QQ-Name-Alpha-ZZ"}},
			},
			tc:      TurnContext{ConversationID: "c1", TurnID: "t-must-not-appear", Seq: 42},
			wantTyp: protocol.TypeSlashCommandList,
			wantPayload: protocol.SlashCommandListPayload{
				ConversationID: "c1",
				Commands:       []protocol.SlashCommand{{Name: "QQ-Name-Alpha-ZZ"}},
			},
			wantOK: true,
		},
		{
			// Zero value maps rather than dropping — the absence of an empty-Commands
			// suppression branch, under test. The gate that decides whether the event
			// exists at all is the producer's (streamsup's emitSlashCommandList
			// suppresses the empty list); a second, differently-shaped filter here
			// would silently diverge from it. AC 2's nil half at the struct level: the
			// nil Commands stays NIL rather than becoming an allocated empty slice,
			// SlashCommandListPayload.MarshalJSON owning nil→[] on the wire.
			name:    "SlashCommandList zero value maps rather than dropping",
			ev:      turnevent.SlashCommandList{},
			tc:      tc,
			wantTyp: protocol.TypeSlashCommandList,
			wantPayload: protocol.SlashCommandListPayload{
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

	// Fresh allocations per row, the sibling table's rule, and the two values are the
	// absent-versus-zero pair's wire halves: 0 must encode as 0 and never as null,
	// while the negative one proves the encoder is handed claude's number rather than
	// something clamped on the way.
	zeroWireUtilization := func() *float64 { v := 0.0; return &v }
	negativeWireUtilization := func() *float64 { v := -7.25; return &v }

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
				// An unreported utilization reaches the wire as null TOO, and the
				// forbidden form below is the whole point: "utilization":0 would tell a
				// phone the window is fresh when claude said nothing about it. An
				// omitempty added to the field later also reddens here, because the key
				// would vanish rather than carry null.
				`"utilization":null`,
			},
			notWant: []string{`"truncated_fields":[]`, `"utilization":0`},
		},
		{
			// The reading's own row, and both needles are full "key":value pairs per this
			// test's standing rule. The value is negative so a clamp to 0..1 or an abs()
			// goes red on the bytes as well as on the struct, and the explicit-zero needle
			// is forbidden here for the same reason the null row forbids it: the two are
			// different facts and exactly one of them is true of any given frame.
			name: "an explicit zero reading reaches the wire as 0, and a negative one verbatim",
			ev: turnevent.RateLimited{
				Status:      "qq-status-sentinel",
				LimitType:   "zz-limittype-sentinel",
				Utilization: zeroWireUtilization(),
			},
			want: []string{
				`"utilization":0`,
				`"conversation_id":"cc-conv-sentinel"`,
				`"status":"qq-status-sentinel"`,
			},
			notWant: []string{`"utilization":null`},
		},
		{
			name: "a negative reading crosses to the wire unclamped",
			ev: turnevent.RateLimited{
				Status:      "qq-status-sentinel",
				LimitType:   "zz-limittype-sentinel",
				Utilization: negativeWireUtilization(),
			},
			want:    []string{`"utilization":-7.25`},
			notWant: []string{`"utilization":null`, `"utilization":0`, `"utilization":7.25`},
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

// model_list carries BOTH nil polarities, in adjacent rows of one table, and
// copying either onto the other is the realistic mistake this test exists to
// catch:
//
//   - "models" and "effort_levels" want [] and forbid null. ModelListPayload and
//     ModelOption each own a MarshalJSON that normalises its own nil, for two
//     DIFFERENT reasons (read them there) — the mapping's job is only to reach
//     them with the nil intact.
//   - "truncated_fields" wants null and forbids []. ModelOption.MarshalJSON
//     deliberately EXEMPTS it, so nothing normalises it afterwards:
//     nothing-was-cut is an ABSENCE, and a mapper that allocated an empty slice —
//     or appended into a fresh one — would emit [] and tell a phone that claude's
//     cut text is complete.
//
// So the polarity is per FIELD here, not per test, and the neighbouring rows of
// this table disagree on purpose. TestMapEventBackgroundTaskRosterEmptyTasksOnTheWire
// and TestMapEventRateLimitedTruncatedFieldsOnTheWire are the two halves of that
// split living in separate tests; this one holds both at once.
//
// The assertion runs on json.Marshal of the value MapEvent RETURNED, never on a
// test-built payload. With TWO MarshalJSON methods in play, a hand-built payload
// would prove the marshallers work and say nothing whatever about whether the
// mapping reached them with the nils intact.
//
// Needles are always the full "key":"value" or "key":[…] pair, never a bare
// value — a bare-value needle passes against a mapping that swapped two
// same-typed neighbours, and five of this row's six fields are strings, so that
// hazard is acute. No sentinel here contains a digit, so the turn-addressing row
// can forbid a bare seq without a false positive.
func TestMapEventModelListOnTheWire(t *testing.T) {
	t.Parallel()

	const convSentinel = "cc-conv-sentinel"
	tc := TurnContext{ConversationID: convSentinel, TurnID: "t-alpha", Seq: 7}

	// claude's MEASURED order, neither alphabetical nor sorted (#1827), beside what
	// sorting it would produce — so the ordering row forbids the exact bytes a
	// canonicalising mapper would emit.
	claudeOrder := `"effort_levels":["low","medium","high","xhigh","max"]`
	sortedOrder := `"effort_levels":["high","low","max","medium","xhigh"]`

	tests := []struct {
		name    string
		ev      turnevent.ModelList
		tc      TurnContext
		want    []string
		notWant []string
	}{
		{
			name: "nil models reaches the wire as []",
			ev:   turnevent.ModelList{},
			want: []string{
				`"models":[]`,
				`"dropped_models":0`,
				`"conversation_id":"` + convSentinel + `"`,
			},
			notWant: []string{`"models":null`},
		},
		{
			// The control: [] is not what the mapping emits for everything, so the
			// nil row above passes for the right reason.
			name: "populated list carries the entry",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{
					ResolvedModel: "qq-resolved-sentinel",
					Value:         "qq-value-sentinel",
					DisplayName:   "qq-display-sentinel",
				}},
			},
			want: []string{
				`"resolved_model":"qq-resolved-sentinel"`,
				`"value":"qq-value-sentinel"`,
				`"display_name":"qq-display-sentinel"`,
				`"supports_auto_mode":false`,
			},
			notWant: []string{`"models":[]`},
		},
		{
			// AC 4. A row that had nothing cut reaches the wire as null, never [].
			name: "row with nothing cut reaches the wire as null",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{Value: "qq-value-sentinel"}},
			},
			want:    []string{`"truncated_fields":null`},
			notWant: []string{`"truncated_fields":[]`},
		},
		{
			// AC 3, first isolating row: EXACTLY ONE name, and it is one of the two
			// the type's doc used to omit. A row carrying a second name would pin
			// neither — a whitelist mutant keeping only ("value", "display_name")
			// would stay green against an over-determined fixture.
			name: "only resolved_model cut survives the mapping",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{
					Value:           "qq-value-sentinel",
					TruncatedFields: []string{"resolved_model"},
				}},
			},
			want:    []string{`"truncated_fields":["resolved_model"]`},
			notWant: []string{`"truncated_fields":null`, `"truncated_fields":[]`},
		},
		{
			// AC 3, second isolating row: the other name the doc omitted, alone.
			name: "only effort_levels cut survives the mapping",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{
					Value:           "qq-value-sentinel",
					TruncatedFields: []string{"effort_levels"},
				}},
			},
			want:    []string{`"truncated_fields":["effort_levels"]`},
			notWant: []string{`"truncated_fields":null`, `"truncated_fields":[]`},
		},
		{
			// All four the producer can record, in ITS order, as ONE needle — so
			// member ORDER is pinned and not merely membership.
			name: "all four cut names cross in producer order",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{
					Value:           "qq-value-sentinel",
					TruncatedFields: []string{"resolved_model", "value", "display_name", "effort_levels"},
				}},
			},
			want:    []string{`"truncated_fields":["resolved_model","value","display_name","effort_levels"]`},
			notWant: []string{`"truncated_fields":null`},
		},
		{
			// The OTHER polarity, one field up: a nil effort menu is a COLLAPSE, not
			// an absence, so it reaches the wire as [].
			name: "nil effort levels reach the wire as []",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{Value: "qq-value-sentinel"}},
			},
			want:    []string{`"effort_levels":[]`},
			notWant: []string{`"effort_levels":null`},
		},
		{
			name: "effort levels cross in claude's own order",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{
					Value:        "qq-value-sentinel",
					EffortLevels: []string{"low", "medium", "high", "xhigh", "max"},
				}},
			},
			want:    []string{claudeOrder},
			notWant: []string{sortedOrder},
		},
		{
			// AC 2. The drop count is the decode's, so the fixture's row count and
			// drop count differ: a constant 0 and a recomputation from len(models)
			// are each a separate notWant.
			name: "dropped_models is the decode's count, not the row count",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{
					{Value: "qq-value-alpha"},
					{Value: "qq-value-beta"},
				},
				DroppedModels: 3,
			},
			want:    []string{`"dropped_models":3`},
			notWant: []string{`"dropped_models":0`, `"dropped_models":2`},
		},
		{
			// AC 1's addressing half, checked where a phone would see it: a struct
			// comparison cannot see a turn id that arrived through an embedded field
			// or a marshaller. Both halves of the turn context are conspicuous.
			name: "no turn addressing reaches the wire",
			ev: turnevent.ModelList{
				Models: []turnevent.ModelOption{{Value: "qq-value-sentinel"}},
			},
			tc:      TurnContext{ConversationID: convSentinel, TurnID: "t-must-not-appear", Seq: 42},
			want:    []string{`"conversation_id":"` + convSentinel + `"`},
			notWant: []string{"t-must-not-appear", "42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			evTC := tt.tc
			if evTC.ConversationID == "" {
				evTC = tc
			}
			typ, payload, ok := MapEvent(tt.ev, evTC)
			if !ok {
				t.Fatal("the mapping suppressed a model list; it must be forwarded")
			}
			if typ != protocol.TypeModelList {
				t.Fatalf("typ: got %q, want %q", typ, protocol.TypeModelList)
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

// slash_command_list carries BOTH nil polarities in adjacent rows of one table,
// exactly as model_list does, and it carries them on ONE struct rather than
// across two: protocol.SlashCommand owns both Aliases and TruncatedFields, and
// its MarshalJSON normalises the first while deliberately exempting the second.
//
//   - "commands" and "aliases" want [] and forbid null. SlashCommandListPayload
//     and SlashCommand each own a MarshalJSON that normalises its own nil, for
//     two DIFFERENT reasons (read them there — the payload's is that [] is a
//     positive statement, the entry's is that claude never sends an empty alias
//     array at all, so absent and empty are one reading). The mapping's job is
//     only to reach them with the nil intact.
//   - "truncated_fields" wants null and forbids []. SlashCommand.MarshalJSON
//     deliberately EXEMPTS it, so nothing normalises it afterwards:
//     nothing-was-cut is an ABSENCE, and a mapper that helpfully allocated an
//     empty slice — or appended into a fresh one — would emit [] and tell a
//     client that claude's cut text is complete.
//
// So the polarity is per FIELD, not per test, and the neighbouring rows of this
// table disagree on purpose: a reader who "fixes" whichever of the two they meet
// second breaks the other.
//
// The assertion runs on json.Marshal of the value MapEvent RETURNED, never on a
// test-built payload. With TWO MarshalJSON methods in play, a hand-built payload
// would prove the marshallers work and say nothing whatever about whether the
// mapping reached them with the nils intact.
//
// Needles are always the full "key":"value" or "key":[…] pair, never a bare value
// — a bare-value needle passes against a mapping that swapped two same-typed
// neighbours, and THREE of this row's four carried fields are strings, so that
// hazard is acute. No sentinel here contains a digit, so the turn-addressing row
// can forbid a bare seq without a false positive.
func TestMapEventSlashCommandListOnTheWire(t *testing.T) {
	t.Parallel()

	const convSentinel = "cc-conv-sentinel"
	tc := TurnContext{ConversationID: convSentinel, TurnID: "t-alpha", Seq: 7}

	// claude's own order for `clear`'s two published aliases, beside what sorting
	// them would produce — so the ordering row forbids the exact bytes a
	// canonicalising mapper would emit. Prefixed so neither is a bare word another
	// row could match by accident.
	claudeAliasOrder := `"aliases":["qq-reset","qq-new"]`
	sortedAliasOrder := `"aliases":["qq-new","qq-reset"]`

	tests := []struct {
		name    string
		ev      turnevent.SlashCommandList
		tc      TurnContext
		want    []string
		notWant []string
	}{
		{
			name: "nil commands reaches the wire as []",
			ev:   turnevent.SlashCommandList{},
			want: []string{
				`"commands":[]`,
				`"dropped_commands":0`,
				`"conversation_id":"` + convSentinel + `"`,
			},
			notWant: []string{`"commands":null`},
		},
		{
			// The control: [] is not what the mapping emits for everything, so the
			// nil row above passes for the right reason.
			name: "populated list carries the entry",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{
					Name:         "qq-name-sentinel",
					ArgumentHint: "qq-hint-sentinel",
					Description:  "qq-description-sentinel",
				}},
			},
			want: []string{
				`"name":"qq-name-sentinel"`,
				`"argument_hint":"qq-hint-sentinel"`,
				`"description":"qq-description-sentinel"`,
			},
			notWant: []string{`"commands":[]`},
		},
		{
			// AC 2. A row that had nothing cut reaches the wire as null, never [].
			name: "row with nothing cut reaches the wire as null",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{Name: "qq-name-sentinel"}},
			},
			want:    []string{`"truncated_fields":null`},
			notWant: []string{`"truncated_fields":[]`},
		},
		{
			// The OTHER polarity, one field up: a nil alias list is a COLLAPSE, not
			// an absence, so it reaches the wire as []. Adjacent to the row above on
			// purpose — the two disagree, and both are right.
			name: "nil aliases reach the wire as []",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{Name: "qq-name-sentinel"}},
			},
			want:    []string{`"aliases":[]`},
			notWant: []string{`"aliases":null`},
		},
		{
			// AC 3. Aliases are what make a consumer's grey-out correct, and their
			// order is claude's: the desktop Actions menu's own reset entry is an
			// ALIAS of clear rather than a command name, so a path that dropped or
			// re-ordered them greys out a command that works.
			name: "aliases cross in claude's own order",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{
					Name:    "qq-name-sentinel",
					Aliases: []string{"qq-reset", "qq-new"},
				}},
			},
			want:    []string{claudeAliasOrder},
			notWant: []string{sortedAliasOrder},
		},
		{
			// AC 2, first isolating row: EXACTLY ONE name. A row carrying a second
			// would pin neither — a whitelist mutant keeping only ("name",
			// "description") would stay green against an over-determined fixture.
			// "argument_hint" is the sharpest single name to isolate because it is
			// the only one of the four that is NOT byte-identical to claude's own key
			// (argumentHint), so a mapper translating names rather than copying them
			// goes red here and nowhere else.
			name: "only argument_hint cut survives the mapping",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{
					Name:            "qq-name-sentinel",
					TruncatedFields: []string{"argument_hint"},
				}},
			},
			want:    []string{`"truncated_fields":["argument_hint"]`},
			notWant: []string{`"truncated_fields":null`, `"truncated_fields":[]`},
		},
		{
			// AC 2, second isolating row: "aliases", the one name that can mean two
			// different cuts (a string in the list shortened, or the list itself
			// shortened) and says the same thing either way. Alone, so a whitelist
			// mutant dropping it cannot hide behind a neighbour.
			name: "only aliases cut survives the mapping",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{
					Name:            "qq-name-sentinel",
					TruncatedFields: []string{"aliases"},
				}},
			},
			want:    []string{`"truncated_fields":["aliases"]`},
			notWant: []string{`"truncated_fields":null`, `"truncated_fields":[]`},
		},
		{
			// All four the producer can record, in ITS order, as ONE needle — so
			// member ORDER is pinned and not merely membership.
			name: "all four cut names cross in producer order",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{
					Name:            "qq-name-sentinel",
					TruncatedFields: []string{"name", "argument_hint", "description", "aliases"},
				}},
			},
			want:    []string{`"truncated_fields":["name","argument_hint","description","aliases"]`},
			notWant: []string{`"truncated_fields":null`},
		},
		{
			// AC 2. The drop count is the decode's, so the fixture's row count and
			// drop count differ: a constant 0 and a recomputation from len(commands)
			// are each a separate notWant.
			name: "dropped_commands is the decode's count, not the row count",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{
					{Name: "qq-name-alpha"},
					{Name: "qq-name-beta"},
				},
				DroppedCommands: 3,
			},
			want:    []string{`"dropped_commands":3`},
			notWant: []string{`"dropped_commands":0`, `"dropped_commands":2`},
		},
		{
			// AC 1's addressing half, checked where a client would see it: a struct
			// comparison cannot see a turn id that arrived through an embedded field
			// or a marshaller. Both halves of the turn context are conspicuous.
			name: "no turn addressing reaches the wire",
			ev: turnevent.SlashCommandList{
				Commands: []turnevent.SlashCommand{{Name: "qq-name-sentinel"}},
			},
			tc:      TurnContext{ConversationID: convSentinel, TurnID: "t-must-not-appear", Seq: 42},
			want:    []string{`"conversation_id":"` + convSentinel + `"`},
			notWant: []string{"t-must-not-appear", "42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			evTC := tt.tc
			if evTC.ConversationID == "" {
				evTC = tc
			}
			typ, payload, ok := MapEvent(tt.ev, evTC)
			if !ok {
				t.Fatal("the mapping suppressed a slash-command list; it must be forwarded")
			}
			if typ != protocol.TypeSlashCommandList {
				t.Fatalf("typ: got %q, want %q", typ, protocol.TypeSlashCommandList)
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

// AC 4's read-only half, which the two tests above cannot see between them. Both
// compare the mapping's OUTPUT; this one asserts what the mapping did to its
// INPUT.
//
// A DeepEqual against an independently-built twin catches an arm that wrote
// through e.Commands[i] — an in-place sort, dedupe, filter, or a "normalise the
// nils while we're here". The event is passed BY VALUE and its struct is copied,
// but Commands is a slice header, so such a write reaches the caller's backing
// array; and every output-comparing test in this file would still pass, the
// mutation landing in the input and the expectation alike. The twin is built from
// separate literals for exactly that reason: a snapshot taken by assigning the
// event would share those same arrays and make the comparison vacuous. In
// production that write would corrupt the list #2005 reads on a relay-leg
// goroutine.
//
// AC 4's OTHER half — the payload's outer slice is freshly allocated rather than
// the event's own — is deliberately NOT asserted here, because no assertion could
// fail. protocol.SlashCommand and turnevent.SlashCommand are distinct types, so
// assigning the event's slice across does not typecheck and every construction
// that does allocates. A write-through-the-payload check would read as a pin and
// be green against every possible arm, which is worse than no check at all.
//
// Nor is it asserted that the inner slices are copied. They are not: Aliases and
// TruncatedFields cross as the slice headers they are and go on sharing backing
// arrays with the event BY DESIGN. The contract is carry-never-mutate-through,
// not copy, and a test demanding distinct inner arrays would forbid the design.
func TestMapEventSlashCommandListDoesNotMutateTheEvent(t *testing.T) {
	t.Parallel()

	ev := turnevent.SlashCommandList{
		Commands: []turnevent.SlashCommand{
			{
				Name:            "qq-name-alpha",
				ArgumentHint:    "qq-hint-alpha",
				Description:     "qq-description-alpha",
				Aliases:         []string{"qq-reset", "qq-new"},
				TruncatedFields: []string{"description"},
			},
			{Name: "qq-name-beta"},
		},
		DroppedCommands: 3,
	}
	twin := turnevent.SlashCommandList{
		Commands: []turnevent.SlashCommand{
			{
				Name:            "qq-name-alpha",
				ArgumentHint:    "qq-hint-alpha",
				Description:     "qq-description-alpha",
				Aliases:         []string{"qq-reset", "qq-new"},
				TruncatedFields: []string{"description"},
			},
			{Name: "qq-name-beta"},
		},
		DroppedCommands: 3,
	}

	_, payload, ok := MapEvent(ev, TurnContext{ConversationID: "c1"})
	if !ok {
		t.Fatal("the mapping suppressed a slash-command list; it must be forwarded")
	}
	if _, isList := payload.(protocol.SlashCommandListPayload); !isList {
		t.Fatalf("payload: got %T, want protocol.SlashCommandListPayload", payload)
	}
	if !reflect.DeepEqual(ev, twin) {
		t.Fatalf("the mapping wrote to the event it was handed:\n got %#v\nwant %#v", ev, twin)
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

// TestInputFields pins the extraction #1678 exists for. The rows are chosen,
// not padded: an empty json.RawMessage{} is deliberately absent because the nil
// row already covers the len(raw)==0 branch, a bare JSON number is absent
// because the array row already covers "does not unmarshal into a map", and a
// >maxInputFields input is absent because a 17-entry literal costs more to read
// than it proves — protocol's TestToolUsePayload_FitV2EnvelopeCap fills to
// exactly that many.
func TestInputFields(t *testing.T) {
	t.Parallel()

	// Over the per-value cap, so the truncation rows exercise the cut rather
	// than the pass-through.
	bulk := strings.Repeat("a", maxInputValueRunes+500)
	cappedBulk := strings.Repeat("a", maxInputValueRunes) + "…"
	longKey := strings.Repeat("k", maxInputKeyRunes+1)

	tests := []struct {
		name string
		raw  json.RawMessage
		want map[string]string
	}{
		{"nil yields no fields", nil, nil},
		{"empty object yields no fields", json.RawMessage(`{}`), nil},
		{"non-object yields no fields", json.RawMessage(`["a","b"]`), nil},
		{"invalid json yields no fields", json.RawMessage(`{not json`), nil},
		{
			name: "plain object crosses verbatim",
			raw:  json.RawMessage(`{"command":"ls -la","description":"list files"}`),
			want: map[string]string{"command": "ls -la", "description": "list files"},
		},
		{
			// A string value arrives DECODED, not re-quoted: the newline is a
			// newline and the quote is a quote, which is what makes a path a path.
			name: "string value arrives decoded",
			raw:  json.RawMessage(`{"text":"a\nb\"c"}`),
			want: map[string]string{"text": "a\nb\"c"},
		},
		{
			name: "non-string values arrive as compact json",
			raw:  json.RawMessage(`{"n":1.5,"b":true,"arr":[1, 2],"obj":{ "x" : 1 },"nul":null}`),
			want: map[string]string{
				"n": "1.5", "b": "true", "arr": "[1,2]", "obj": `{"x":1}`, "nul": "null",
			},
		},
		{
			name: "oversized value cut at the cap with an ellipsis",
			raw:  json.RawMessage(`{"content":"` + bulk + `"}`),
			want: map[string]string{"content": cappedBulk},
		},
		{
			name: "multibyte value cut on a rune boundary",
			raw:  json.RawMessage(`{"content":"` + strings.Repeat("日", maxInputValueRunes+100) + `"}`),
			want: map[string]string{"content": strings.Repeat("日", maxInputValueRunes) + "…"},
		},
		{
			// The key is DROPPED, never truncated: a cut key is a false claim
			// about the input's field name. Its siblings are unaffected.
			name: "over-long key drops its entry only",
			raw:  json.RawMessage(`{"` + longKey + `":"v","file_path":"/tmp/x.go"}`),
			want: map[string]string{"file_path": "/tmp/x.go"},
		},
		{
			// The regression this ticket exists for, and the row that proves
			// shortest-first: three bulk values exhaust maxInputTotalRunes, and
			// file_path survives INTACT because it is admitted before them.
			// Sorted-key order would spend the budget on a_bulk and b_bulk and
			// never reach file_path at all.
			name: "budget binds: short identifying field survives, bulk tail drops",
			raw: json.RawMessage(`{"a_bulk":"` + bulk + `","b_bulk":"` + bulk +
				`","c_bulk":"` + bulk + `","file_path":"/tmp/x.go"}`),
			want: map[string]string{
				"file_path": "/tmp/x.go",
				"a_bulk":    cappedBulk,
				"b_bulk":    cappedBulk,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := inputFields(tt.raw)
			// The nil is load-bearing, not cosmetic: the bridge must hand
			// protocol a nil so ToolUsePayload.MarshalJSON owns the {} rather
			// than the bridge pre-allocating one and hiding the decision.
			if tt.want == nil && got != nil {
				t.Fatalf("inputFields: got %#v, want a nil map", got)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("inputFields:\n got %#v\nwant %#v", got, tt.want)
			}
			for k, v := range got {
				if n := utf8.RuneCountInString(k); n > maxInputKeyRunes {
					t.Fatalf("key %q is %d runes, over the %d cap", k, n, maxInputKeyRunes)
				}
				if !utf8.ValidString(v) {
					t.Fatalf("value for %q is not valid UTF-8: %q", k, v)
				}
			}
		})
	}
}

// An input with nothing to send reaches the wire as "input":{}, never
// "input":null — the polarity is decided in protocol
// (ToolUsePayload.MarshalJSON) and this test proves the bridge reaches it with
// the nil intact.
//
// The assertion runs on json.Marshal of the value MapEvent RETURNED, not on a
// payload the test built, for TestMapEventBackgroundTaskRosterEmptyTasksOnTheWire's
// reason: a test-constructed payload would only prove protocol's MarshalJSON
// works, not that the mapping reached it without pre-allocating an empty map of
// its own.
//
// Pinning the marshalled BYTES rather than the decoded value is the point of the
// test: decoding "input":{} and decoding "input":null both yield an empty map to
// a Go caller, so a value-level assertion would pass in both polarities.
func TestMapEventToolUseEmptyInputOnTheWire(t *testing.T) {
	t.Parallel()

	tc := TurnContext{ConversationID: "c1", TurnID: "t1", Seq: 7}

	tests := []struct {
		name    string
		raw     json.RawMessage
		want    []string
		notWant []string
	}{
		{
			name:    "absent input",
			raw:     nil,
			want:    []string{`"input":{}`},
			notWant: []string{`"input":null`},
		},
		{
			name:    "empty object input",
			raw:     json.RawMessage(`{}`),
			want:    []string{`"input":{}`},
			notWant: []string{`"input":null`},
		},
		{
			name:    "non-object input",
			raw:     json.RawMessage(`["a","b"]`),
			want:    []string{`"input":{}`},
			notWant: []string{`"input":null`},
		},
		{
			// The control: {} is not what the marshaller emits for everything, so
			// the three empty rows above pass for the right reason. input_summary
			// rides along in the same needle set because AC 1 keeps it populated
			// and unchanged beside the new map.
			name: "populated input carries its fields",
			raw:  json.RawMessage(`{"command":"ls"}`),
			want: []string{
				`"input":{"command":"ls"}`,
				`"input_summary":"{\"command\":\"ls\"}"`,
			},
			notWant: []string{`"input":{}`, `"input":null`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ, payload, ok := MapEvent(turnevent.ToolStart{
				ToolCallID: "tool-1", Title: "Bash", RawInput: tt.raw,
			}, tc)
			if !ok {
				t.Fatal("ToolStart was suppressed by the mapping; it must be forwarded")
			}
			if typ != protocol.TypeToolUse {
				t.Fatalf("typ: got %q, want %q", typ, protocol.TypeToolUse)
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

func TestResultSummary(t *testing.T) {
	t.Parallel()

	// Expressed against the constant, never a bare literal: the row exercises the
	// cut, so it has to move with the bound rather than pin a number (#1680). The
	// multibyte fixture is the rune-safety guard on the COMPOSITION at the new
	// bound — truncate is unmodified and TestTruncate already covers it directly.
	long := strings.Repeat("b", maxResultSummaryRunes+300)
	longMultibyte := strings.Repeat("日", maxResultSummaryRunes+10)

	tests := []struct {
		name string
		in   turnevent.ToolContent
		want string
	}{
		{"nil -> empty", nil, ""},
		{"text verbatim", turnevent.TextContent{Text: "done"}, "done"},
		{"text truncated", turnevent.TextContent{Text: long}, strings.Repeat("b", maxResultSummaryRunes) + "…"},
		{"multibyte text cut on a rune boundary", turnevent.TextContent{Text: longMultibyte}, strings.Repeat("日", maxResultSummaryRunes) + "…"},
		{"diff -> path", turnevent.DiffContent{Path: "/tmp/x.go"}, "/tmp/x.go"},
		{"terminal -> reference", turnevent.TerminalContent{TerminalID: "term-9"}, "terminal term-9"},
	}

	// head keeps a failure legible: the over-cap fixtures are 10000+ runes and
	// dumping two of them whole buries the mismatch it is meant to show.
	head := func(s string) string {
		r := []rune(s)
		if len(r) <= 40 {
			return string(r)
		}
		return string(r[:40]) + "…"
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resultSummary(tt.in)
			if got != tt.want {
				t.Fatalf("resultSummary: got %d runes %q, want %d runes %q",
					utf8.RuneCountInString(got), head(got), utf8.RuneCountInString(tt.want), head(tt.want))
			}
			if !utf8.ValidString(got) {
				t.Fatalf("resultSummary produced invalid UTF-8: %q", head(got))
			}
		})
	}
}

// maxV2AppEnvelope is the Mobile Protocol v2 application-envelope size cap
// (docs/protocol-mobile.md § Application-envelope size cap). Test-local on
// purpose, and the reason NEEDED AMENDING when #2002 put a frame bound in the
// production file. The old one — "nothing in internal/turnbridge enforces the cap
// — the transport does, so a package-level constant here would imply an
// enforcement this package does not perform" — is now false for one payload:
// maxSlashCommandListBytes enforces a frame bound on slash_command_list.
//
// It stays test-local anyway, and the distinction is the point rather than a
// technicality. What outbound.go enforces is a BUDGET FOR ONE FIELD of one
// payload, a different number derived from this one; THIS cap is still enforced by
// the transport alone, and no other payload this file maps is bounded against it
// here. A production constant carrying 65519 would say the package checks
// envelopes, which it does not — maxDeltaTextBytes' "it bounds the INPUT text, not
// the envelope — do not 'correct' it towards 65519" is the same rule stated from
// the other side.
const maxV2AppEnvelope = 65519

// TestToolResultPayload_FitV2EnvelopeCap drives the REAL resultSummary with the
// largest result in the measured corpus and proves the serialised envelope fits
// under the v2 application-envelope cap. Per-field caps do not compose into an
// envelope guarantee on their own, so this is measured rather than argued — the
// statement protocol's TestToolUsePayload_FitV2EnvelopeCap makes, and this is
// that test's shape with one deliberate difference: it lives HERE, in the
// producer's package, so that maxResultSummaryRunes is what the measurement
// stands on. A hand-built payload over in protocol cannot call the unexported
// resultSummary at all and would stay green with the constant raised to 16000 —
// the exact shape that let #1678's maxInputFields ship with no test standing on
// it (docs/knowledge/features/turnbridge-package.md).
//
// The fill is '<', not 'a': encoding/json has SetEscapeHTML on by default, so one
// such rune costs six bytes on the wire and an 'a' fill measures 10309 B where
// this measures 60000-odd — it would pass a cap that is 47% over. Here '<' is the
// REALISTIC case rather than the contrived one, which is more than the precedent
// tests can say: a tool result is raw command output or file contents, and
// reading a TSX or HTML file is an ordinary '<'-dense result.
//
// The three identity fields are filled hostilely because nothing bounds them —
// conversation_id and turn_id are daemon-supplied, tool_use_id is claude's value
// verbatim — so the guarantee is an assumption worth stating rather than an
// enforced cap. 64 runes is roughly 11x the longest observed.
func TestToolResultPayload_FitV2EnvelopeCap(t *testing.T) {
	t.Parallel()

	fill := func(n int) string { return strings.Repeat("<", n) }

	// 64525 is the largest tool result in the measured corpus (#1680). The
	// producer applies no cap of its own, so resultSummary is what cuts it.
	summary := resultSummary(turnevent.TextContent{Text: fill(64525)})
	if n := utf8.RuneCountInString(summary); n != maxResultSummaryRunes+1 {
		t.Fatalf("precondition: summary is %d runes, want %d (the cap plus one ellipsis)", n, maxResultSummaryRunes+1)
	}

	// ResultDetail at ITS producer's worst case (#2024), so the measurement
	// covers the field rather than assuming it is small. streamsup's
	// toolResultDetail composes five forms; the longest is still the READ's, two
	// non-negative int64s as 19 digits + " of " + 19 digits + " lines" = 48 bytes.
	// That number is maxResultDetailBytes over in streamsup, repeated here because
	// it is unexported there — slashFillRow repeats the producer's caps for the
	// same reason and names the same failure mode to watch for, a worst case that
	// is no longer the producer's.
	//
	// Unlike ResultSummary above, this is NOT cut by anything in this package: the
	// bound is over int64's RANGE rather than over claude's input length, which is
	// what makes an absurd line count unable to grow the frame.
	//
	// THE FILL IS ALL-ASCII BECAUSE THE LONGEST FORM IS, NOT BECAUSE THE PRODUCER
	// IS. Before #2025 those were the same statement; that ticket's edit and write
	// forms emit U+2212 and U+00B7, so the alphabet is now ASCII plus those two.
	// The read form remains the longest of the five and happens to be the
	// all-ASCII one, and encoding/json escapes no byte of any form, so 48 bytes
	// here is still 48 bytes on the wire. streamsup's
	// TestToolResultDetail_OtherFormsAreShorter is what keeps the "longest of
	// five" half true.
	detail := "9223372036854775806 of 9223372036854775807 lines"
	if len(detail) != 48 {
		t.Fatalf("precondition: detail fill is %d B, want the producer's 48-byte worst case", len(detail))
	}

	// IsError is explicitly false because that is the worst case: "false" costs
	// one byte more on the wire than "true", and the field is never omitted.
	body, err := json.Marshal(protocol.ToolResultPayload{
		ConversationID: fill(64),
		TurnID:         fill(64),
		ToolUseID:      fill(64),
		IsError:        false,
		ResultSummary:  summary,
		ResultDetail:   detail,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	// Worst-case envelope too: max-uint64 ids and a populated EventID, so the
	// outer frame costs as much as it ever can.
	eventID := ^uint64(0)
	out, err := json.Marshal(protocol.Envelope{
		ID:      ^uint64(0),
		Type:    protocol.TypeToolResult,
		TS:      time.Date(2026, 8, 21, 10, 33, 18, 0, time.UTC),
		Payload: body,
		EventID: &eventID,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	t.Logf("tool_result at the result cap: %d B, %.1f%% of the %d-byte v2 application-envelope cap",
		len(out), float64(len(out))/float64(maxV2AppEnvelope)*100, maxV2AppEnvelope)
	if len(out) >= maxV2AppEnvelope {
		t.Errorf("serialised envelope: got %d B, want < %d B", len(out), maxV2AppEnvelope)
	}
}

// slashFillRow builds one worst-case slash-command row at the PRODUCER's own
// per-field caps: streamsup's maxSlashCommandName, maxSlashCommandArgumentHint
// and maxSlashCommandDescription at 256 bytes each, maxSlashCommandAlias at 64
// with maxSlashCommandAliasCount aliases, and a TruncatedFields naming all four
// wire names protocol.SlashCommand's doc enumerates. Those numbers are repeated
// here rather than imported because they are unexported in another package; if
// one of them moves, this fixture states a worst case that is no longer the
// producer's, which is the failure mode to watch for.
//
// The fill is '<' for TestToolResultPayload_FitV2EnvelopeCap's reason:
// encoding/json has SetEscapeHTML on by default, so one such byte costs six on
// the wire. An 'a' fill would measure a sixth of this and pass a budget it has no
// right to. Here it is not even the contrived case — a workspace author's command
// description is prose, and prose in a repository contains markup.
func slashFillRow() turnevent.SlashCommand {
	fill := func(n int) string { return strings.Repeat("<", n) }
	aliases := make([]string, 8)
	for i := range aliases {
		aliases[i] = fill(64)
	}
	return turnevent.SlashCommand{
		Name:            fill(256),
		ArgumentHint:    fill(256),
		Description:     fill(256),
		Aliases:         aliases,
		TruncatedFields: []string{"name", "argument_hint", "description", "aliases"},
	}
}

// mappedSlashRow is the row the arm builds for one event entry, with no cut
// applied — the value a prefix assertion compares against.
func mappedSlashRow(c turnevent.SlashCommand) protocol.SlashCommand {
	return protocol.SlashCommand{
		Name:            c.Name,
		ArgumentHint:    c.ArgumentHint,
		Description:     c.Description,
		Aliases:         c.Aliases,
		TruncatedFields: c.TruncatedFields,
	}
}

// slashCommandsBytes measures what the `commands` array costs on the wire, which
// is what maxSlashCommandListBytes budgets. It marshals the array on its own
// rather than the whole payload, so the number is comparable to the constant
// without the payload's other two keys folded in.
func slashCommandsBytes(t *testing.T, commands []protocol.SlashCommand) int {
	t.Helper()
	b, err := json.Marshal(commands)
	if err != nil {
		t.Fatalf("marshal commands: %v", err)
	}
	return len(b)
}

// TestMapEventSlashCommandListUnderBudgetIsUnchanged pins the half of the frame
// bound that is easy to lose: a list inside the budget maps to exactly the bytes
// the unbounded arm produced, with nothing cut and DroppedCommands carried
// through untouched.
//
// THE EQUALITY ALONE IS NOT A HEADROOM PROOF, which is why the precondition runs
// first. "The mapped payload equals the whole input" holds against a budget of
// 64000 exactly as well as against one ten times smaller — it says the cut did
// not fire on THIS fixture, never that the fixture has room above it. So the
// fixture's own measured size is asserted under the budget before the equality,
// with a message that names the fixture as the cause: a future row grown large
// enough to meet the budget then reddens HERE and says so, rather than surfacing
// as an unexplained inequality that reads like a mapping regression
// (docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md).
func TestMapEventSlashCommandListUnderBudgetIsUnchanged(t *testing.T) {
	t.Parallel()

	ev := turnevent.SlashCommandList{
		Commands: []turnevent.SlashCommand{
			{
				Name:            "qq-name-alpha",
				ArgumentHint:    "[qq-target]",
				Description:     "qq-description-alpha with <markup> and an em dash — raw",
				Aliases:         []string{"qq-reset", "qq-new"},
				TruncatedFields: []string{"description"},
			},
			{Name: "qq-name-beta"},
			{Name: "__qq-remote-workflow", Description: "qq-description-gamma"},
		},
		DroppedCommands: 4,
	}

	want := protocol.SlashCommandListPayload{
		ConversationID:  "c1",
		Commands:        []protocol.SlashCommand{mappedSlashRow(ev.Commands[0]), mappedSlashRow(ev.Commands[1]), mappedSlashRow(ev.Commands[2])},
		DroppedCommands: 4,
	}
	if n := slashCommandsBytes(t, want.Commands); n >= maxSlashCommandListBytes {
		t.Fatalf("precondition: this fixture's commands array serialises to %d B against the %d-byte budget, so it no longer exercises the nothing-is-cut path — the FIXTURE grew, not the mapping", n, maxSlashCommandListBytes)
	}

	_, payload, ok := MapEvent(ev, TurnContext{ConversationID: "c1"})
	if !ok {
		t.Fatal("the mapping suppressed a slash-command list; it must be forwarded")
	}
	got, isList := payload.(protocol.SlashCommandListPayload)
	if !isList {
		t.Fatalf("payload: got %T, want protocol.SlashCommandListPayload", payload)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("an under-budget list was altered:\n got %#v\nwant %#v", got, want)
	}

	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("an under-budget list did not map byte-identically:\n got %s\nwant %s", gotBytes, wantBytes)
	}
}

// TestMapEventSlashCommandListOverBudgetCutsFromTheTail drives the cut itself.
// Every row is at the producer's worst case, so the array is several times the
// budget and the cut must fire.
//
// The count fed in is maxSlashCommandListEntries, a DAEMON constant, and no
// assertion here names a length taken from a real claude: that count is workspace-
// and version-dependent by design, so a fixture pinned to one would break on a
// re-capture that changed nothing about this code.
func TestMapEventSlashCommandListOverBudgetCutsFromTheTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		droppedIn  int
		entryCount int
	}{
		{name: "nothing dropped by the producer", droppedIn: 0, entryCount: 128},
		{name: "producer already dropped some", droppedIn: 37, entryCount: 128},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			commands := make([]turnevent.SlashCommand, tt.entryCount)
			for i := range commands {
				commands[i] = slashFillRow()
			}
			ev := turnevent.SlashCommandList{Commands: commands, DroppedCommands: tt.droppedIn}

			_, payload, ok := MapEvent(ev, TurnContext{ConversationID: "c1"})
			if !ok {
				t.Fatal("the mapping suppressed a slash-command list; it must be forwarded")
			}
			got, isList := payload.(protocol.SlashCommandListPayload)
			if !isList {
				t.Fatalf("payload: got %T, want protocol.SlashCommandListPayload", payload)
			}

			if len(got.Commands) == 0 {
				t.Fatal("every entry was cut; one entry's worst case is far under the budget, so at least one must fit")
			}
			if len(got.Commands) >= len(ev.Commands) {
				t.Fatalf("nothing was cut: %d of %d entries kept, but the array is several times the %d-byte budget", len(got.Commands), len(ev.Commands), maxSlashCommandListBytes)
			}

			// The measured budget is the point: the retained array must actually
			// fit, not merely be shorter.
			if n := slashCommandsBytes(t, got.Commands); n > maxSlashCommandListBytes {
				t.Errorf("retained commands array: got %d B, want <= %d B", n, maxSlashCommandListBytes)
			}

			// Cut from the TAIL: what survives is a prefix of the input in
			// claude's own order, never a reordered or hole-punched selection.
			for i, row := range got.Commands {
				if want := mappedSlashRow(ev.Commands[i]); !reflect.DeepEqual(row, want) {
					t.Fatalf("entry %d is not the input's entry %d; the cut is not a tail cut", i, i)
				}
			}

			// ADDED TO, never replacing: the producer's count and this cut's sum,
			// so len(commands) + dropped_commands is still the list's true size.
			cutHere := len(ev.Commands) - len(got.Commands)
			if want := tt.droppedIn + cutHere; got.DroppedCommands != want {
				t.Fatalf("dropped_commands: got %d, want %d (%d from the producer + %d cut here)", got.DroppedCommands, want, tt.droppedIn, cutHere)
			}
			if want := len(ev.Commands) + tt.droppedIn; len(got.Commands)+got.DroppedCommands != want {
				t.Fatalf("len(commands) + dropped_commands: got %d, want %d (the list's true size after both cuts)", len(got.Commands)+got.DroppedCommands, want)
			}
		})
	}
}

// TestMapEventSlashCommandListCutEndsTheWalk pins the one property a list of
// uniformly-sized rows CANNOT pin: that a row which does not fit ENDS the walk
// rather than being skipped over in favour of a smaller row behind it.
//
// The fixture is deliberately decorrelated — worst-case rows first, small rows
// after — because with every row the same size, ending the walk and skipping the
// row produce the identical output, and an implementation that continued past the
// first over-budget row would pass the sibling test above unnoticed. That was
// MEASURED rather than assumed: mutating the cut's `break` to `continue` left the
// whole package green until this fixture existed.
//
// The distinction matters to a client, not just to the code. A skip produces a
// menu with a HOLE in claude's order, which a client diffing against the
// names-only slash_commands twin reads as a command that vanished; ending the walk
// produces a shortened menu, which is what dropped_commands then explains.
func TestMapEventSlashCommandListCutEndsTheWalk(t *testing.T) {
	t.Parallel()

	const bigRows = 10
	commands := make([]turnevent.SlashCommand, 0, bigRows+3)
	for i := 0; i < bigRows; i++ {
		commands = append(commands, slashFillRow())
	}
	// Three rows small enough to fit in whatever budget the big rows leave, so a
	// skipping implementation would admit them behind the row that did not fit.
	for _, name := range []string{"qq-tiny-alpha", "qq-tiny-beta", "qq-tiny-gamma"} {
		commands = append(commands, turnevent.SlashCommand{Name: name})
	}
	ev := turnevent.SlashCommandList{Commands: commands}

	_, payload, ok := MapEvent(ev, TurnContext{ConversationID: "c1"})
	if !ok {
		t.Fatal("the mapping suppressed a slash-command list; it must be forwarded")
	}
	got, isList := payload.(protocol.SlashCommandListPayload)
	if !isList {
		t.Fatalf("payload: got %T, want protocol.SlashCommandListPayload", payload)
	}

	if len(got.Commands) == 0 {
		t.Fatal("every entry was cut; the leading rows are one worst-case entry each and far under the budget, so several must fit")
	}
	// The prefix check runs BEFORE the count precondition on purpose: a skipping
	// implementation keeps MORE rows than a cutting one, so a count guard placed
	// first would swallow the failure and report it as a stale fixture.
	for i, row := range got.Commands {
		if want := mappedSlashRow(ev.Commands[i]); !reflect.DeepEqual(row, want) {
			t.Fatalf("entry %d is not the input's entry %d: a row behind the cut was admitted, so the walk SKIPPED rather than ENDED", i, i)
		}
	}
	if len(got.Commands) >= bigRows {
		t.Fatalf("precondition: %d of %d worst-case rows kept; this fixture only separates the two behaviours when the cut fires among them", len(got.Commands), bigRows)
	}
	if want := len(ev.Commands) - len(got.Commands); got.DroppedCommands != want {
		t.Fatalf("dropped_commands: got %d, want %d — every row from the cut onward is dropped, the small ones included", got.DroppedCommands, want)
	}
}

// TestSlashCommandListPayload_FitV2EnvelopeCap is the SUSPENDERS to
// maxSlashCommandListBytes' belt, TestToolResultPayload_FitV2EnvelopeCap's shape
// applied to this frame: it drives the REAL MapEvent with the largest list the
// producer's caps permit and measures the serialised envelope rather than arguing
// about it. maxSlashCommandListBytes is the conservative constant; this is the
// deterministic per-frame measurement that catches it being wrong. If this ever
// fails, LOWER the constant — never raise it.
//
// A per-field cap does not compose into an envelope guarantee, and for this frame
// it composes into the OPPOSITE: 128 entries at the 1280-byte per-entry term is
// 163,840 raw bytes before any escaping, so the unbounded arm's payload was
// several times the cap by construction.
//
// The conversation id is filled hostilely, TestToolResultPayload_FitV2EnvelopeCap's
// reason unchanged — it is daemon-supplied and nothing in this package bounds it,
// so the reserve outside `commands` is validated against 64 escaped runes rather
// than a 36-byte UUID.
func TestSlashCommandListPayload_FitV2EnvelopeCap(t *testing.T) {
	t.Parallel()

	commands := make([]turnevent.SlashCommand, 128)
	for i := range commands {
		commands[i] = slashFillRow()
	}
	_, payload, ok := MapEvent(
		turnevent.SlashCommandList{Commands: commands, DroppedCommands: 128},
		TurnContext{ConversationID: strings.Repeat("<", 64)},
	)
	if !ok {
		t.Fatal("the mapping suppressed a slash-command list; it must be forwarded")
	}
	got, isList := payload.(protocol.SlashCommandListPayload)
	if !isList {
		t.Fatalf("payload: got %T, want protocol.SlashCommandListPayload", payload)
	}
	if len(got.Commands) >= len(commands) {
		t.Fatalf("precondition: %d of %d entries survived, so this measurement no longer exercises the cut", len(got.Commands), len(commands))
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	// Worst-case envelope too: max-uint64 ids and a populated EventID, so the
	// outer frame costs as much as it ever can.
	eventID := ^uint64(0)
	out, err := json.Marshal(protocol.Envelope{
		ID:      ^uint64(0),
		Type:    protocol.TypeSlashCommandList,
		TS:      time.Date(2026, 8, 21, 10, 33, 18, 0, time.UTC),
		Payload: body,
		EventID: &eventID,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	t.Logf("slash_command_list at the frame budget: %d entries kept, commands array %d B, envelope %d B, %.1f%% of the %d-byte v2 application-envelope cap; reserve outside `commands` is %d B",
		len(got.Commands), slashCommandsBytes(t, got.Commands), len(out),
		float64(len(out))/float64(maxV2AppEnvelope)*100, maxV2AppEnvelope,
		len(out)-slashCommandsBytes(t, got.Commands))
	if len(out) >= maxV2AppEnvelope {
		t.Errorf("serialised envelope: got %d B, want < %d B", len(out), maxV2AppEnvelope)
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

// TestMapEventTurnEnd_ModelWindowsStayOffTheWire asserts the claim #2101's design
// rests on rather than leaving it inferred from reading the arm: a TurnEnd
// carrying per-model context windows maps to exactly the payload one without them
// maps to.
//
// The property is structural — MapEvent's arm builds protocol.TurnEndPayload field
// by field rather than embedding the event — but "structural" is what a later edit
// silently undoes. A reader who adds a field to that literal to publish the window
// reddens this test, which is the point: publishing it is #2102's decision to make
// deliberately, with the envelope arithmetic neither cap states today, not one that
// arrives as a side effect.
//
// Both halves matter. The two payloads are compared to each other, so the row
// cannot pass by both being wrong in the same way; and the JSON encoding is
// compared too, because a field added with a `json:"-"` tag would leave the structs
// unequal while the wire stayed clean, and one added to an embedded struct would do
// the reverse.
func TestMapEventTurnEnd_ModelWindowsStayOffTheWire(t *testing.T) {
	t.Parallel()
	tc := TurnContext{ConversationID: "c1", TurnID: "t1", Seq: 0}
	bare := turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}
	loaded := turnevent.TurnEnd{
		Reason: turnevent.TurnEndReasonEndTurn,
		ModelWindows: []turnevent.ModelWindow{
			{ModelID: "claude-haiku-4-5-20251001", WindowTokens: 200000},
			{ModelID: "claude-sonnet-5", WindowTokens: 1000000},
		},
		DroppedModelWindows: 7,
	}

	bareTyp, barePayload, bareOK := MapEvent(bare, tc)
	gotTyp, gotPayload, gotOK := MapEvent(loaded, tc)
	if !bareOK || !gotOK {
		t.Fatalf("MapEvent ok: bare %v, loaded %v, want both true", bareOK, gotOK)
	}
	if gotTyp != bareTyp {
		t.Errorf("envelope type: got %q, want %q", gotTyp, bareTyp)
	}
	if !reflect.DeepEqual(gotPayload, barePayload) {
		t.Errorf("payload: got %#v, want %#v — a widened TurnEnd must not widen the "+
			"envelope; publishing the window is #2102's call", gotPayload, barePayload)
	}
	wantJSON, err := json.Marshal(barePayload)
	if err != nil {
		t.Fatalf("marshalling the bare payload: %v", err)
	}
	gotJSON, err := json.Marshal(gotPayload)
	if err != nil {
		t.Fatalf("marshalling the loaded payload: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("payload JSON: got %s, want %s", gotJSON, wantJSON)
	}
	// Vacuity guard: sentinels that WOULD appear if the arm ever embedded the event.
	for _, leak := range []string{"claude-sonnet-5", "1000000", "ModelWindows", "\"7\"", ":7"} {
		if strings.Contains(string(gotJSON), leak) {
			t.Errorf("payload JSON carries %q: %s", leak, gotJSON)
		}
	}
}

// intPtr is the count-pointer constructor #2237's rows need. A helper rather than a
// per-row local because the whole point of the pointers is nil-versus-pointer-to-zero,
// and `&v` over a loop variable is exactly how that distinction gets written wrong.
func intPtr(v int) *int { return &v }

// TestMapEvent_CompactionBoundaryCarriesNothingElse is #2237's AC 2 at the wire: the
// mapped frame's key set is exactly four, so nothing from claude's compact_metadata
// can reach a client through this adapter even if a later edit widens the event.
//
// It asserts the KEY SET rather than searching for particular uuids, which is the
// stronger of the two and the one this layer can make. streamsup's replay test greps
// the captured identifiers out of the event; here the event is a Go struct that
// cannot hold one, so the only failure mode left is a FIELD being added — and an
// allowlist stated as "these four and no others" is what catches that, where a
// substring search over values would not.
func TestMapEvent_CompactionBoundaryCarriesNothingElse(t *testing.T) {
	t.Parallel()

	_, payload, ok := MapEvent(turnevent.CompactionBoundary{
		Trigger:    "manual",
		PreTokens:  intPtr(23600),
		PostTokens: intPtr(2612),
	}, TurnContext{ConversationID: "c1", TurnID: "t1", Seq: 7})
	if !ok {
		t.Fatal("MapEvent refused a CompactionBoundary; the arm must map every one of them")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling the payload: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("re-decoding the payload: %v", err)
	}
	want := map[string]bool{
		"conversation_id": true, "trigger": true, "pre_tokens": true, "post_tokens": true,
	}
	for k := range keys {
		if !want[k] {
			t.Errorf("payload carries key %q, which is not on the allowlist. claude's "+
				"compact_metadata also holds three uuids naming entries in the operator's own "+
				"transcript, and this frame publishes a trigger and two counts and nothing else", k)
		}
	}
	for k := range want {
		if _, present := keys[k]; !present {
			t.Errorf("payload is missing key %q; every key is always present on this file's "+
				"frames, which is what lets the testdata fixtures pin the full shape", k)
		}
	}
	// turn_id and seq are not merely absent from the output — the payload has no field
	// for either, so a non-empty TurnID and non-zero Seq above cannot leak.
	for _, forbidden := range []string{"turn_id", "seq"} {
		if _, present := keys[forbidden]; present {
			t.Errorf("payload carries %q; a compaction boundary is a mark in the conversation's "+
				"history and may arrive with no turn open at all", forbidden)
		}
	}
}

// TestMapEvent_ToolDeniedDoesNotMutateTheEvent pins the property the report-token rename
// makes newly reachable in this file. This is the first arm that rewrites slice CONTENTS
// rather than only reading them, and the same event is also observed by cmd/pyry's
// history-append path and by the remaining eventKind call sites — so a rename applied in
// place, or a payload sharing the producer's backing array, would let this arm's output
// become visible to them as a corrupted report.
//
// TestMapEventSlashCommandListDoesNotMutateTheEvent is the pattern; the failure it guards
// against here is different in kind, because that arm only ever re-slices.
func TestMapEvent_ToolDeniedDoesNotMutateTheEvent(t *testing.T) {
	t.Parallel()

	ev := turnevent.ToolCallDenied{
		ToolName:           "Bash",
		ToolCallID:         "tool-1",
		Message:            "requested permissions to use Bash",
		DecisionReasonType: "rule",
		TruncatedFields:    []string{"message"},
		DroppedFields:      []string{"tool_call_id", "decision_reason_type"},
	}
	before := turnevent.ToolCallDenied{
		ToolName:           "Bash",
		ToolCallID:         "tool-1",
		Message:            "requested permissions to use Bash",
		DecisionReasonType: "rule",
		TruncatedFields:    []string{"message"},
		DroppedFields:      []string{"tool_call_id", "decision_reason_type"},
	}

	_, payload, ok := MapEvent(ev, TurnContext{ConversationID: "c1", TurnID: "t1"})
	if !ok {
		t.Fatal("MapEvent refused a ToolCallDenied; the arm must map every one of them")
	}
	if !reflect.DeepEqual(ev, before) {
		t.Errorf("MapEvent mutated the event: got %+v, want %+v", ev, before)
	}

	// DeepEqual alone would pass on a rename that happened to write the same value, so
	// the aliasing itself is checked: writing through the payload's slice must not be
	// visible on the event's.
	p, isDenied := payload.(protocol.ToolDeniedPayload)
	if !isDenied {
		t.Fatalf("payload is %T, want protocol.ToolDeniedPayload", payload)
	}
	if len(p.DroppedFields) > 0 {
		p.DroppedFields[0] = "QQ-Sentinel-ZZ"
	}
	if len(p.TruncatedFields) > 0 {
		p.TruncatedFields[0] = "QQ-Sentinel-ZZ"
	}
	if !reflect.DeepEqual(ev, before) {
		t.Errorf("the payload's report slices alias the event's: writing through one "+
			"changed the other. got %+v, want %+v", ev, before)
	}
}

// TestMapEvent_ToolDeniedCarriesNothingElse pins the frame's key set, including that the
// two report keys are always present. seq is absent because the payload has no field for
// it; turn_id IS present, unlike the conversation-scoped status peers, because a denial
// belongs to the turn that made the call it names.
func TestMapEvent_ToolDeniedCarriesNothingElse(t *testing.T) {
	t.Parallel()

	_, payload, ok := MapEvent(turnevent.ToolCallDenied{
		ToolName:   "Bash",
		ToolCallID: "tool-1",
	}, TurnContext{ConversationID: "c1", TurnID: "t1", Seq: 7})
	if !ok {
		t.Fatal("MapEvent refused a ToolCallDenied; the arm must map every one of them")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling the payload: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("re-decoding the payload: %v", err)
	}
	want := map[string]bool{
		"conversation_id": true, "turn_id": true, "tool_use_id": true, "tool_name": true,
		"decision_reason_type": true, "decision_reason": true, "message": true,
		"truncated_fields": true, "dropped_fields": true,
	}
	for k := range keys {
		if !want[k] {
			t.Errorf("payload carries key %q, which is not on the allowlist", k)
		}
	}
	for k := range want {
		if _, present := keys[k]; !present {
			t.Errorf("payload is missing key %q; every key is always present on this "+
				"file's frames, which is what lets the testdata fixtures pin the full shape", k)
		}
	}
	if _, present := keys["seq"]; present {
		t.Error("payload carries \"seq\"; only assistant_delta is delta-ordered")
	}
	// Nothing was cut or dropped, so both reports must be a literal null rather than
	// an empty array. [] would say claude's text is complete when it is merely uncut.
	for _, report := range []string{"truncated_fields", "dropped_fields"} {
		if got := string(keys[report]); got != "null" {
			t.Errorf("%s: got %s, want null — an allocated [] and a nil report are "+
				"different statements to a client", report, got)
		}
	}
}
