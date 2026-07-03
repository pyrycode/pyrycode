// Package acpbridge maps the neutral, daemon-owned turnevent model OUT to the
// Agent Client Protocol (ACP) session/update payloads. It is the ACP mirror of
// internal/turnbridge (which maps the same neutral events out to the mobile v2
// wire): same neutral source, different framing. ADR 027
// (docs/knowledge/decisions/027-acp-mapping.md) is the authoritative outbound
// mapping table this file implements.
//
// It is a pure value-to-value adapter — no transport, no I/O, no goroutine, no
// clock read, no state. Everything stateful (which session the update belongs
// to, holding the session/prompt call open for TurnEnd, writing Stall to
// stderr, grouping chunks into a message) is the streaming consumer's job
// (#750). Keeping those out is what makes MapUpdate table-testable and isolates
// it from the ACP session lifecycle.
//
// Import discipline: this package imports only internal/turnevent and
// encoding/json. It does NOT import internal/acp (the JSON-RPC transport floor,
// which is deliberately sealed to the standard library and knows no concrete
// ACP method) — the mapper never touches the transport — and it does NOT import
// internal/protocol (the mobile wire types), exactly as turnevent's package doc
// requires the two adapters to stay independent over one neutral model.
package acpbridge

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// session/update variant discriminants — the value of the ACP `sessionUpdate`
// field. These ARE the ACP wire strings (ADR 027 "ACP taxonomy reference").
const (
	SessionUpdateAgentMessageChunk = "agent_message_chunk"
	SessionUpdateAgentThoughtChunk = "agent_thought_chunk"
	SessionUpdateToolCall          = "tool_call"
	SessionUpdateToolCallUpdate    = "tool_call_update"
)

// MethodSessionUpdate is the JSON-RPC notification method the consumer (#750)
// sends carrying one of the payloads below. Defined here for the consumer's
// convenience; the {sessionId, update} params wrapper is the consumer's.
const MethodSessionUpdate = "session/update"

// AgentMessageChunk is the session/update payload for incremental assistant
// text. The neutral MessageID is NOT a field here — ACP has no messageId on the
// wire; MapUpdate returns it out-of-band for the consumer to group by.
type AgentMessageChunk struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       ContentBlock `json:"content"`
}

// AgentThoughtChunk is the session/update payload for streaming reasoning
// ("thinking") text. Kept as its own named type (not shared with
// AgentMessageChunk) so each call site matches the ADR taxonomy 1:1; the tiny
// duplication is deliberate.
type AgentThoughtChunk struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       ContentBlock `json:"content"`
}

// ContentBlock is an ACP content block. Text-only today: turnevent builds only
// text/diff/terminal content, and ACP image/resource blocks have no turnevent
// shape yet (ADR 027). New fields are added when the producer emits them.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolCall is the session/update payload announcing a new tool invocation. It
// carries no Content: a new invocation has no result yet — content first
// appears via a ToolCallUpdate.
type ToolCall struct {
	SessionUpdate string             `json:"sessionUpdate"`
	ToolCallID    string             `json:"toolCallId"`
	Title         string             `json:"title"`
	Kind          string             `json:"kind"`
	Status        string             `json:"status"`             // always "pending" from a ToolStart
	RawInput      json.RawMessage    `json:"rawInput,omitempty"` // opaque pass-through, never parsed here
	Locations     []ToolCallLocation `json:"locations,omitempty"`
}

// ToolCallUpdate is the session/update payload carrying the changed fields of an
// existing tool call. Status is omitempty (a degenerate empty-status update
// omits it) and Content is nil for a status-only update.
type ToolCallUpdate struct {
	SessionUpdate string            `json:"sessionUpdate"`
	ToolCallID    string            `json:"toolCallId"`
	Status        string            `json:"status,omitempty"`
	Content       []ToolCallContent `json:"content,omitempty"`
}

// ToolCallLocation is a file a tool call touches. Line is 1-based; 0 (unspecified)
// is omitted.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolCallContent is a flat tagged union over the ACP tool-call content shapes:
// "content" (a text block), "diff", or "terminal". Only the fields for the
// active Type are set; the rest are omitempty.
type ToolCallContent struct {
	Type       string        `json:"type"`
	Content    *ContentBlock `json:"content,omitempty"`    // Type == "content"
	Path       string        `json:"path,omitempty"`       // Type == "diff"
	OldText    string        `json:"oldText,omitempty"`    // Type == "diff"
	NewText    string        `json:"newText,omitempty"`    // Type == "diff"
	TerminalID string        `json:"terminalId,omitempty"` // Type == "terminal"
}

// MapUpdate maps one neutral turnevent.Event to its ACP session/update payload,
// or reports "no notification" (ok == false). It is the ACP mirror of
// turnbridge.MapEvent — pure, exhaustive over the sealed turnevent.Event, with
// no I/O, no goroutine and no clock read; safe on a zero-value / nil Event.
//
// update is one of the payload structs above, or nil when ok is false. It is
// any because the payloads share no marker interface; the consumer json.Marshals
// it directly under the session/update params. The variant discriminant rides
// INSIDE update as its sessionUpdate field, so there is no separate type return
// (unlike the mobile envelope). The method is always MethodSessionUpdate.
//
// msgID is the neutral MessageID returned OUT-OF-BAND (ACP has no messageId wire
// field): non-empty only for the two chunk variants, so the consumer can group
// chunks by message. All stateful grouping is the consumer's (#750).
//
// ok is false for the two events with no session/update representation — TurnEnd
// (it maps to the session/prompt stopReason return, divergence 1) and Stall
// (internal-only; the consumer writes it to stderr) — and for a nil/unknown
// Event. What to do with a no-notification outcome is the consumer's job.
func MapUpdate(ev turnevent.Event) (update any, msgID string, ok bool) {
	switch e := ev.(type) {
	case turnevent.TextChunk:
		return AgentMessageChunk{
			SessionUpdate: SessionUpdateAgentMessageChunk,
			Content:       ContentBlock{Type: "text", Text: e.Text},
		}, e.MessageID, true
	case turnevent.ThoughtChunk:
		return AgentThoughtChunk{
			SessionUpdate: SessionUpdateAgentThoughtChunk,
			Content:       ContentBlock{Type: "text", Text: e.Text},
		}, e.MessageID, true
	case turnevent.ToolStart:
		// Kind is emitted as the ACP taxonomy string verbatim (string(e.Kind)):
		// no translation table is needed because the turnevent.ToolKind values
		// already ARE the ACP strings (taxonomy.go), and "other" is already the
		// neutral fallback kind, so no unknown-kind coalescing happens here.
		// ToolStart has no status field, so a new tool_call is emitted with the
		// ACP default "pending" (turnevent.ToolStatusPending).
		return ToolCall{
			SessionUpdate: SessionUpdateToolCall,
			ToolCallID:    e.ToolCallID,
			Title:         e.Title,
			Kind:          string(e.Kind),
			Status:        string(turnevent.ToolStatusPending),
			RawInput:      e.RawInput,
			Locations:     mapLocations(e.Locations),
		}, "", true
	case turnevent.ToolUpdate:
		// Status is likewise the ACP taxonomy string verbatim (string(e.Status)):
		// turnevent.ToolStatus values ARE the ACP strings, so no table.
		return ToolCallUpdate{
			SessionUpdate: SessionUpdateToolCallUpdate,
			ToolCallID:    e.ToolCallID,
			Status:        string(e.Status),
			Content:       mapToolContent(e.Content),
		}, "", true
	case turnevent.TurnEnd:
		// Divergence 1: end-of-turn is the stopReason RETURN of session/prompt,
		// not a session/update notification. The consumer resolves the held
		// session/prompt call with string(e.Reason). Nothing to emit here.
		return nil, "", false
	case turnevent.Stall:
		// Internal-only onset marker; ACP has no home for it (ADR 027 outbound
		// table). The consumer surfaces it on stderr. Not a session/update.
		return nil, "", false
	default:
		// nil, or any impossible future variant of the sealed Event: no
		// notification. Kept explicit so a new producer variant surfaces as a
		// visible drop rather than silently vanishing (same posture as the
		// template's default arm).
		return nil, "", false
	}
}

// mapToolContent maps the sealed turnevent.ToolContent to the ACP tool-call
// content list, exhaustive over the sum type. A nil content (the legal
// status-only ToolUpdate) yields nil, so the payload's content field is omitted.
// The default arm is kept even though the type is sealed so a future producer
// variant cannot silently vanish (mirrors turnbridge.resultSummary).
func mapToolContent(c turnevent.ToolContent) []ToolCallContent {
	switch v := c.(type) {
	case turnevent.TextContent:
		return []ToolCallContent{{Type: "content", Content: &ContentBlock{Type: "text", Text: v.Text}}}
	case turnevent.DiffContent:
		return []ToolCallContent{{Type: "diff", Path: v.Path, OldText: v.OldText, NewText: v.NewText}}
	case turnevent.TerminalContent:
		return []ToolCallContent{{Type: "terminal", TerminalID: v.TerminalID}}
	default:
		return nil
	}
}

// mapLocations maps each turnevent.Location to its ACP wire shape, returning nil
// for an empty input so the payload's locations field is omitted.
func mapLocations(locs []turnevent.Location) []ToolCallLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]ToolCallLocation, len(locs))
	for i, l := range locs {
		out[i] = ToolCallLocation{Path: l.Path, Line: l.Line}
	}
	return out
}
