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
// envelope mapping live here. Inbound commands (Prompt, Cancel, …) and the
// internal-only BusyState event are out of scope and get a home in a later
// ticket. The internal-only Stall event now lives here (mobile-sent,
// ACP-dropped); see its type doc.
package turnevent

import "encoding/json"

// Event is the sealed sum type of outbound turn events: TextChunk,
// ThoughtChunk, ToolStart, ToolUpdate, TurnEnd, the internal-only status
// peers Stall, ApiRetry, and Compacting, and the diagnostic marker
// Unrecognized. The unexported marker keeps the variant set closed to this
// package, so external ACP-spec churn cannot inject a variant. The bridge
// (#608) ranges a stream of Event and the wire adapter (#607) type-switches to
// map each kind.
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

// Stall is an internal-only onset marker: tui-driver raised a one-shot
// stall_detected signal (no payload, no clearing edge). It carries no fields —
// onset only, no "cleared" state, and (like every variant here) no
// conversation identity; the bridge injects that when mapping to the wire. The
// mobile adapter sends it as the wire "stall" event; the future ACP adapter
// (#600) drops it (no ACP equivalent).
type Stall struct{}

// ApiRetry is a PTY-derived status peer of Stall carrying claude's live
// API-error retry state. Active is the rising (true) / falling (false) edge;
// Current/Total are the parsed `attempt N/M` counter ({0,0} when the counter
// did not parse). Like every variant here it carries no conversation identity —
// the bridge injects it when mapping to the wire.
type ApiRetry struct {
	Active  bool
	Current int
	Total   int
}

// Compacting is a PTY-derived status peer of Stall: claude's auto-compaction
// banner. Banner-only (tui-driver streams no progress payload), so Active — the
// rising (true) / falling (false) edge — is the only field beyond the
// bridge-injected conversation id.
type Compacting struct{ Active bool }

// UnrecognizedSite names WHERE in the stream-json line mapping a payload was
// found that the parser has no mapping for. String-backed so the producer's call
// site is enum-safe and the value crosses the wire unchanged.
type UnrecognizedSite string

const (
	// UnrecognizedLineType is a top-level stream-json line whose `type` is
	// neither mapped nor on the measured known-ignored list.
	UnrecognizedLineType UnrecognizedSite = "line_type"
	// UnrecognizedAssistantBlock is an assistant message content block whose
	// `type` is not text, thinking, or tool_use.
	UnrecognizedAssistantBlock UnrecognizedSite = "assistant_block"
	// UnrecognizedUserBlock is a user message content block whose `type` is not
	// tool_result.
	UnrecognizedUserBlock UnrecognizedSite = "user_block"
	// UnrecognizedUndecodable is a line or block that failed to JSON-decode at
	// all. Kind is empty for this site — there is no type to report.
	UnrecognizedUndecodable UnrecognizedSite = "undecodable"
)

// Unrecognized is a diagnostic marker: the stream-json parser met a payload it
// has no mapping for and dropped it. It exists so genuinely unknown claude
// output becomes VISIBLE the moment it arrives, instead of vanishing into a
// debug log the production daemon does not print.
//
// It is deliberately NOT the parser's tolerate-and-drop path. Types we
// knowingly ignore (system/*, rate_limit_event) stay silent exactly as before;
// only output outside that measured set reaches here. A row per turn would make
// the feature worthless noise, so the known-ignored list is the whole design.
//
// Raw is a plain string, not json.RawMessage, because the producer truncates it
// at construction: a truncated blob is no longer valid JSON, so typing it as raw
// JSON would be a lie. Truncated says whether that happened. Like every variant
// here it carries no conversation identity — the bridge injects that.
type Unrecognized struct {
	// Site is where the drop happened.
	Site UnrecognizedSite
	// Kind is the message or block `type` that had no mapping. Empty when Site
	// is UnrecognizedUndecodable (nothing decoded, so no type was ever read).
	Kind string
	// Raw is the offending JSON, already truncated by the producer.
	Raw string
	// Truncated reports whether Raw was cut to fit the producer's cap.
	Truncated bool
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
func (Stall) isTurnEvent()        {}
func (ApiRetry) isTurnEvent()     {}
func (Compacting) isTurnEvent()   {}
func (Unrecognized) isTurnEvent() {}

var (
	_ Event = TextChunk{}
	_ Event = ThoughtChunk{}
	_ Event = ToolStart{}
	_ Event = ToolUpdate{}
	_ Event = TurnEnd{}
	_ Event = Stall{}
	_ Event = ApiRetry{}
	_ Event = Compacting{}
	_ Event = Unrecognized{}
)
