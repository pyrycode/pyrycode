// Package turnevent defines the neutral, daemon-owned outbound turn-event
// model for Phase 2 structured streaming (EPIC #596).
//
// These are pure value types — no transport, no I/O, standard library only.
// They are the stable internal contract that the event-stream bridge (#608)
// maps tui-driver Events() INTO and that the v2 wire types (#607) map OUT of.
// The mobile wire (now) and the future pyry acp adapter (#600) are thin
// adapters over this one model: it is shaped ~90% like ACP so the ACP adapter
// is near pass-through, but it is owned by us — so churn in the external ACP
// spec stays inside the ACP adapter and never reaches the daemon core or the
// mobile wire. Same containment logic the tui-driver substrate seal applies to
// claude's screen.
//
// This is the outbound turn-event core only. No Events() draining and no
// envelope mapping live here. Inbound commands (Prompt, Cancel, …) and
// internal-only events (BusyState, Stall, …) are out of scope and get a home
// in a later ticket.
package turnevent

import "encoding/json"

// Event is the sealed sum type of outbound turn events: TextChunk,
// ThoughtChunk, ToolStart, ToolUpdate, TurnEnd. The unexported marker keeps the
// variant set closed to this package, so external ACP-spec churn cannot inject
// a variant. The bridge (#608) ranges a stream of Event and the wire adapter
// (#607) type-switches to map each kind.
type Event interface{ isTurnEvent() }

// TextChunk is incremental assistant text, grouped by message.
type TextChunk struct {
	MessageID string
	Text      string
}

// ThoughtChunk is streaming reasoning ("thinking") text, grouped by message.
type ThoughtChunk struct {
	MessageID string
	Text      string
}

// ToolStart announces a new tool invocation.
//
// RawInput is opaque pass-through tool input the model never parses or mutates;
// it is carried as json.RawMessage (undecoded bytes) precisely because that
// does not force a parse — consumers decode it on their own terms.
type ToolStart struct {
	ToolCallID string
	Title      string
	Kind       ToolKind
	RawInput   json.RawMessage
	Locations  []Location
}

// ToolUpdate carries changed fields of an existing tool call. Content may be
// nil for a status-only update.
type ToolUpdate struct {
	ToolCallID string
	Status     ToolStatus
	Content    ToolContent
}

// TurnEnd marks the end of a claude turn, carrying the reason only.
//
// ACP models end-of-turn as the stopReason return value of session/prompt, not
// as an event; converting TurnEnd back into that RPC return is the ACP
// adapter's job, not this model's. Here we just carry the reason.
type TurnEnd struct {
	Reason TurnEndReason
}

// Location is a file a tool call touches (ACP tool-call location). Line is
// 1-based; 0 means unspecified.
type Location struct {
	Path string
	Line int
}

// The events are pure value types, so each marker is implemented on a value
// receiver: TextChunk{}, not only &TextChunk{}, satisfies Event.
func (TextChunk) isTurnEvent()    {}
func (ThoughtChunk) isTurnEvent() {}
func (ToolStart) isTurnEvent()    {}
func (ToolUpdate) isTurnEvent()   {}
func (TurnEnd) isTurnEvent()      {}

var (
	_ Event = TextChunk{}
	_ Event = ThoughtChunk{}
	_ Event = ToolStart{}
	_ Event = ToolUpdate{}
	_ Event = TurnEnd{}
)
