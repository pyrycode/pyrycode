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
// payload is one of the four protocol.*Payload value structs, or nil when ok is
// false. It is any because the four payloads share no marker interface; the
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
