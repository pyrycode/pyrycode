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

// RenameConversationPayload is the body of a rename_conversation frame
// (docs/protocol-mobile.md § rename_conversation). Phone → binary. Both
// fields are spec-required: a rename must name a target conversation and a
// new display title.
//
// Deliberately NOT a reuse of PromoteConversationPayload — promote also
// carries a required Cwd, which a rename neither has nor means. The reply
// reuses ConversationUpdatedPayload verbatim.
type RenameConversationPayload struct {
	ConversationID string `json:"conversation_id"`
	Name           string `json:"name"`
}

// DeleteConversationPayload is the body of a delete_conversation frame
// (docs/protocol-mobile.md § delete_conversation). Phone → binary. The one
// required field is the target conversation's id; deletion is permanent (hard
// delete — the reversible path is archive/unarchive). Mirrors
// PromoteConversationPayload / RenameConversationPayload's value-typed
// ConversationID.
type DeleteConversationPayload struct {
	ConversationID string `json:"conversation_id"`
}

// ConversationDeletedPayload is the body of a conversation_deleted frame
// (docs/protocol-mobile.md § conversation_deleted). Binary → phone, sent in
// reply to a delete_conversation. It carries only the deleted conversation's id
// (the id tag matches ConversationCreatedPayload / ConversationUpdatedPayload):
// the record no longer exists, so no name/cwd/last_used_at can be projected
// (contrast ConversationUpdatedPayload). The id is the one fact the ack must
// identify.
type ConversationDeletedPayload struct {
	ID string `json:"id"`
}

// ArchiveConversationPayload is the body of BOTH archive_conversation and
// unarchive_conversation frames (docs/protocol-mobile.md § archive). Phone →
// binary. The two verbs are a symmetric toggle of one durable flag — archive
// sets IsArchived, unarchive clears it — so one id-only payload serves both.
// The value-typed ConversationID mirrors DeleteConversationPayload /
// PromoteConversationPayload; deliberately NOT a reuse of
// DeleteConversationPayload (semantic coupling / false dependency).
type ArchiveConversationPayload struct {
	ConversationID string `json:"conversation_id"`
}

// ConversationUpdatedPayload is the body of a conversation_updated frame
// (docs/protocol-mobile.md § conversation_updated). Binary → phone,
// broadcast to all phones on this server-id. ID, IsPromoted, IsArchived, Cwd,
// LastUsedAt are required. Name is spec-optional (a previously unnamed
// conversation can be updated without acquiring a name) and is a pointer
// for the same round-trip reason given on CreateConversationPayload.
type ConversationUpdatedPayload struct {
	ID         string `json:"id"`
	IsPromoted bool   `json:"is_promoted"`
	// IsArchived is the conversation's durable archived flag. Always serialized
	// (no omitempty, unlike the on-disk Conversation.IsArchived): a client reads
	// the flag to partition active vs. archived, so it must read it on active
	// rows too, where the value is false — an absent key could not distinguish
	// "restored to active" from "old daemon." Placed right after IsPromoted to
	// mirror ConversationSummary and group the two state bools.
	IsArchived bool      `json:"is_archived"`
	Name       *string   `json:"name"`
	Cwd        string    `json:"cwd"`
	LastUsedAt time.Time `json:"last_used_at"`
}
