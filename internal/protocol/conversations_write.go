package protocol

import "time"

// CreateConversationPayload is the body of a create_conversation frame
// (docs/protocol-mobile.md § create_conversation). Phone → binary. All
// three fields are spec-optional — the binary fills server-side defaults
// when null is on the wire.
//
// Fields are pointers without omitempty so a nil value round-trips as
// JSON null (matching the spec example's "name": null / "cwd": null) and
// a pointer-to-zero round-trips as the zero scalar (matching the spec
// example's "is_promoted": false). omitempty on a nil pointer would drop
// the key entirely, breaking byte-equivalent round-trip.
type CreateConversationPayload struct {
	IsPromoted *bool   `json:"is_promoted"`
	Name       *string `json:"name"`
	Cwd        *string `json:"cwd"`
}

// ConversationCreatedPayload is the body of a conversation_created frame
// (docs/protocol-mobile.md § conversation_created). Binary → phone, sent
// in reply to a create_conversation. ID, IsPromoted, Cwd, LastUsedAt are
// spec-required and non-nilable. Name is a pointer because the spec
// example shows "name": null (an unnamed scratch conversation); see the
// rationale on CreateConversationPayload for why omitempty is omitted.
type ConversationCreatedPayload struct {
	ID         string    `json:"id"`
	IsPromoted bool      `json:"is_promoted"`
	Cwd        string    `json:"cwd"`
	Name       *string   `json:"name"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// PromoteConversationPayload is the body of a promote_conversation frame
// (docs/protocol-mobile.md § promote_conversation). Phone → binary. All
// three fields are spec-required: a promoted conversation must carry a
// name and an effective cwd, and the conversation_id must resolve to an
// existing row.
type PromoteConversationPayload struct {
	ConversationID string `json:"conversation_id"`
	Name           string `json:"name"`
	Cwd            string `json:"cwd"`
}

// ConversationUpdatedPayload is the body of a conversation_updated frame
// (docs/protocol-mobile.md § conversation_updated). Binary → phone,
// broadcast to all phones on this server-id. ID, IsPromoted, Cwd,
// LastUsedAt are required. Name is spec-optional (a previously unnamed
// conversation can be updated without acquiring a name) and is a pointer
// for the same round-trip reason given on CreateConversationPayload.
type ConversationUpdatedPayload struct {
	ID         string    `json:"id"`
	IsPromoted bool      `json:"is_promoted"`
	Name       *string   `json:"name"`
	Cwd        string    `json:"cwd"`
	LastUsedAt time.Time `json:"last_used_at"`
}
