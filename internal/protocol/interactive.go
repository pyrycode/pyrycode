package protocol

// Interactive v2 event payloads. These are additive, capability-gated
// application events sent binary → phone when the phone has advertised the
// "interactive" capability at handshake (docs/protocol-mobile.md
// § Interactive events). They are the mobile adapter's wire representation
// of internal/turnevent's neutral turn-event model; the mapping from
// internal events to these envelopes, the push, and the capability
// intersection live in the consumer (#608), NOT here. This file is wire
// vocabulary only: pure structs and their (de)serialization.
//
// No field carries omitempty: every field is always present on the wire so
// the testdata fixtures pin the full shape and boundary values like seq:0
// and is_error:false do not silently vanish.

// TurnStatePayload is the body of an Envelope whose Type == TypeTurnState
// (docs/protocol-mobile.md § turn_state). Binary → phone direction; signals
// a coarse change in the turn's lifecycle. State is one of "thinking",
// "responding", "idle"; it stays a plain string (not a named enum),
// matching the MessagePayload.Role precedent. The exact internal
// event → state mapping is #608's.
type TurnStatePayload struct {
	ConversationID string `json:"conversation_id"`
	State          string `json:"state"`
}

// AssistantDeltaPayload is the body of an Envelope whose Type ==
// TypeAssistantDelta (docs/protocol-mobile.md § assistant_delta). Binary →
// phone direction; an incremental, coalesced chunk of assistant text. Seq
// is a per-turn, non-negative delta-ordering counter that resets each turn
// (distinct from the session-monotonic Envelope.ID).
type AssistantDeltaPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	Seq            int    `json:"seq"`
	Text           string `json:"text"`
}

// ToolUsePayload is the body of an Envelope whose Type == TypeToolUse
// (docs/protocol-mobile.md § tool_use). Binary → phone direction; announces
// a tool invocation. InputSummary is a human-readable précis of the tool
// input, not the raw input.
type ToolUsePayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	ToolUseID      string `json:"tool_use_id"`
	Name           string `json:"name"`
	InputSummary   string `json:"input_summary"`
}

// ToolResultPayload is the body of an Envelope whose Type == TypeToolResult
// (docs/protocol-mobile.md § tool_result). Binary → phone direction;
// reports the outcome of the tool_use with the matching ToolUseID.
// ResultSummary is a human-readable précis of the result, not the raw
// output.
type ToolResultPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	ToolUseID      string `json:"tool_use_id"`
	IsError        bool   `json:"is_error"`
	ResultSummary  string `json:"result_summary"`
}

// TurnEndPayload is the body of an Envelope whose Type == TypeTurnEnd
// (docs/protocol-mobile.md § turn_end). Binary → phone direction; marks the
// end of an assistant turn. StopReason carries the
// internal/turnevent.TurnEndReason string values verbatim ("end_turn",
// "max_tokens", "max_turn_requests", "refusal", "cancelled"); it stays a
// plain string because internal/protocol is a stdlib-only leaf data package
// and does NOT import internal/turnevent — #608 produces the field via
// string(turnevent.TurnEnd.Reason). The wire-value/taxonomy alignment is
// documented, not enforced by a shared type.
type TurnEndPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	StopReason     string `json:"stop_reason"`
}

// StallPayload is the body of an Envelope whose Type == TypeStall
// (docs/protocol-mobile.md § stall). Binary → phone direction; the wire form
// of the internal-only turnevent.Stall onset marker. It carries conversation
// identity only — like turn_state, a stall is a coarse conversation-level
// signal, not turn-scoped, so there is no turn_id; and it is onset-only, so
// there is no clearing field (the phone self-clears on the next turn
// activity). The bridge (#608) supplies ConversationID because the internal
// Stall marker carries none.
type StallPayload struct {
	ConversationID string `json:"conversation_id"`
}
