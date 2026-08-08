package turnbridge

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// outbound.go is the mirror of mapper.go: where mapEvent maps a tui-driver
// Event INTO the neutral turnevent.Event model, MapEvent maps that model OUT to
// the v2 interactive wire payloads (#607). It is a pure value-to-value adapter
// — no I/O, no state, no envelope-ID minting, no clock read, no sealing. Every
// one of those belongs to the consumer (the turn-lifecycle integration slice);
// keeping them out is what makes this table-testable and isolates it from the
// lifecycle state machine. See cmd/pyry/assistant_turn_v2.go for the consumer
// shape that wraps a payload into an Envelope.

// TurnContext is the per-event turn addressing the consumer supplies to
// MapEvent. The adapter never derives these — which conversation / turn / seq
// applies to a given event is a turn-lifecycle decision owned by the consumer.
type TurnContext struct {
	ConversationID string
	TurnID         string
	// Seq is the per-turn assistant-delta ordering counter. It is consumed
	// ONLY by the TextChunk -> assistant_delta mapping and ignored for every
	// other event kind; the consumer advances it.
	Seq int
}

// TurnState is the coarse turn-lifecycle state BuildTurnState shapes into a
// turn_state payload. String-backed so the call site is enum-safe; the wire
// field itself stays a plain string (#607).
type TurnState string

const (
	StateThinking   TurnState = "thinking"
	StateResponding TurnState = "responding"
	StateIdle       TurnState = "idle"
)

// maxSummaryLen bounds the input/result précis to a single line of at most this
// many runes. A phone-display bound, not a wire constraint (the envelope cap is
// far larger); tunable if the mobile view wants a different cap.
const maxSummaryLen = 200

// MapEvent maps one neutral turnevent.Event plus explicit turn context to the
// matching v2 interactive wire payload and its envelope type discriminant.
//
// ok is false for events with no wire representation: ThoughtChunk (ADR 025 —
// #607 defines no thought-text envelope; thinking surfaces as a turn_state
// transition the consumer drives via BuildTurnState, and the thought text is
// NOT forwarded) and any nil/unknown Event. The consumer drops + debug-logs
// those. Pure; safe on a zero-value Event.
//
// payload is one of the protocol.*Payload value structs, or nil when ok is
// false. It is any because the payloads share no marker interface; the
// consumer json.Marshals it directly (same path as protocol.MessagePayload).
// The consumer owns the envelope ID, TS, marshal, and seal — none happen here.
func MapEvent(ev turnevent.Event, tc TurnContext) (typ string, payload any, ok bool) {
	switch e := ev.(type) {
	case turnevent.TextChunk:
		return protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			Seq:            tc.Seq,
			Text:           e.Text,
		}, true
	case turnevent.ToolStart:
		return protocol.TypeToolUse, protocol.ToolUsePayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			ToolUseID:      e.ToolCallID,
			Name:           e.Title,
			InputSummary:   inputSummary(e.RawInput),
		}, true
	case turnevent.ToolUpdate:
		return protocol.TypeToolResult, protocol.ToolResultPayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			ToolUseID:      e.ToolCallID,
			IsError:        e.Status == turnevent.ToolStatusFailed,
			ResultSummary:  resultSummary(e.Content),
		}, true
	case turnevent.TurnEnd:
		return protocol.TypeTurnEnd, protocol.TurnEndPayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			StopReason:     string(e.Reason),
		}, true
	case turnevent.Stall:
		// A stall carries conversation identity only — it is not turn-scoped and
		// not a delta, so tc.TurnID and tc.Seq are ignored (as BuildTurnState
		// ignores them).
		return protocol.TypeStall, protocol.StallPayload{
			ConversationID: tc.ConversationID,
		}, true
	case turnevent.ApiRetry:
		// Like Stall, a status peer carries conversation identity only —
		// tc.TurnID and tc.Seq are ignored (not turn-scoped, not a delta). The
		// counter rides across only as the two bounded ints.
		return protocol.TypeApiRetry, protocol.ApiRetryPayload{
			ConversationID: tc.ConversationID,
			Active:         e.Active,
			Current:        e.Current,
			Total:          e.Total,
		}, true
	case turnevent.Compacting:
		return protocol.TypeCompacting, protocol.CompactingPayload{
			ConversationID: tc.ConversationID,
			Active:         e.Active,
		}, true
	case turnevent.Unrecognized:
		// Conversation identity only, like Stall and Compacting above: tc.TurnID
		// and tc.Seq are ignored. This one is not merely "not turn-scoped" — an
		// unrecognized message has no turn we can HONESTLY attribute it to, since
		// we could not parse it well enough to know what it belongs to. Raw is
		// already truncated by the producer; this adapter is pure and re-caps
		// nothing.
		return protocol.TypeUnrecognizedMessage, protocol.UnrecognizedMessagePayload{
			ConversationID: tc.ConversationID,
			Site:           string(e.Site),
			MessageType:    e.Kind,
			Raw:            e.Raw,
			Truncated:      e.Truncated,
		}, true
	case turnevent.BackgroundTaskStarted:
		// Conversation identity only, like the status peers above: tc.TurnID and
		// tc.Seq are ignored, and the payload has no field for either. A
		// background task OUTLIVES the turn that spawned it — that is the whole
		// #1240 point — so attributing it to a turn would be a claim we cannot
		// honestly make. Every string crosses verbatim: the producer bounded them
		// at construction (streamsup's maxTaskFieldID / maxTaskDescription) and
		// this adapter is pure and re-caps nothing. TruncatedFields rides along
		// because a payload that dropped it would present claude's cut text to a
		// phone as complete.
		return protocol.TypeBackgroundTaskStarted, protocol.BackgroundTaskStartedPayload{
			ConversationID:  tc.ConversationID,
			TaskID:          e.TaskID,
			ToolCallID:      e.ToolCallID,
			Description:     e.Description,
			TaskType:        e.TaskType,
			TruncatedFields: e.TruncatedFields,
		}, true
	case turnevent.BackgroundTaskUpdated:
		// The peer of BackgroundTaskStarted, same posture: not turn-scoped, so
		// tc.TurnID and tc.Seq are ignored. Patch crosses WHOLE and unparsed —
		// nothing here reads, validates, or re-serialises it, because a mapping
		// that enumerated known patch keys would silently discard every key
		// claude ships next.
		return protocol.TypeBackgroundTaskUpdated, protocol.BackgroundTaskUpdatedPayload{
			ConversationID:  tc.ConversationID,
			TaskID:          e.TaskID,
			Patch:           e.Patch,
			TruncatedFields: e.TruncatedFields,
		}, true
	case turnevent.BackgroundTaskRoster:
		// The aggregate peer, same posture again: not turn-scoped, tc.TurnID and
		// tc.Seq ignored.
		//
		// A nil Tasks is FORWARDED, not filtered: an empty roster says nothing is
		// alive, which is precisely the reassurance #1240's symptom needs, so
		// suppressing it would delete the payoff of the whole feature. The nil is
		// passed straight through and BackgroundTaskRosterPayload.MarshalJSON
		// (#1393) normalises it to "tasks":[] on the wire — deliberately not
		// pre-allocated here, which would produce the same bytes while hiding the
		// normalisation that type owns.
		//
		// DroppedTasks is the roster's ONLY truncation report — it is not called
		// truncated_fields and the payload has no top-level field of that name —
		// so losing it would tell a phone that a capped roster is the whole
		// roster.
		var tasks []protocol.BackgroundTask
		for _, t := range e.Tasks {
			tasks = append(tasks, protocol.BackgroundTask{
				TaskID:          t.TaskID,
				TaskType:        t.TaskType,
				Description:     t.Description,
				TruncatedFields: t.TruncatedFields,
			})
		}
		return protocol.TypeBackgroundTaskRoster, protocol.BackgroundTaskRosterPayload{
			ConversationID: tc.ConversationID,
			Tasks:          tasks,
			DroppedTasks:   e.DroppedTasks,
		}, true
	default:
		// ThoughtChunk and nil/unknown drop (see doc comment).
		return "", nil, false
	}
}

// BuildTurnState shapes a turn_state payload for the given conversation and
// target state. The consumer's lifecycle machine decides WHICH state applies
// (e.g. observing a ThoughtChunk means "thinking") and calls this; the adapter
// only shapes the payload. The return type is concrete (not any) because it is
// monomorphic — the consumer needs no type assertion.
func BuildTurnState(conversationID string, state TurnState) (typ string, payload protocol.TurnStatePayload) {
	return protocol.TypeTurnState, protocol.TurnStatePayload{
		ConversationID: conversationID,
		State:          string(state),
	}
}

// inputSummary derives a human-readable précis of a tool's opaque RawInput: the
// JSON compacted to a single line (insignificant whitespace stripped), then
// truncated to maxSummaryLen runes. Empty/nil input, or input that does not
// compact as JSON, yields "" — RawInput is best-effort/opaque (#606), so a
// malformed blob is a précis-less tool_use, not an error (mirrors rawInput's
// posture in mapper.go).
func inputSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	return truncate(buf.String(), maxSummaryLen)
}

// resultSummary derives a human-readable précis of a tool result's content,
// exhaustive over the sealed ToolContent sum type so a future producer variant
// cannot silently vanish. nil (the legal status-only ToolUpdate) yields "".
//
// The current inbound producer (mapper.go) only ever emits TextContent or nil;
// the Diff/Terminal renderings are unreachable today but handled (the type is
// sealed) and kept deliberately minimal until a producer (e.g. the ACP adapter
// #600) emits them, at which point the descriptor shape can be refined against a
// real consumer.
func resultSummary(c turnevent.ToolContent) string {
	switch v := c.(type) {
	case turnevent.TextContent:
		return truncate(v.Text, maxSummaryLen)
	case turnevent.DiffContent:
		return truncate(v.Path, maxSummaryLen)
	case turnevent.TerminalContent:
		return truncate("terminal "+v.TerminalID, maxSummaryLen)
	default:
		return ""
	}
}

// truncate returns s unchanged when it is at most max runes; otherwise it cuts
// at max runes and appends an ellipsis. Rune-aware (not byte-slicing) so
// multibyte text never splits mid-rune.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}
