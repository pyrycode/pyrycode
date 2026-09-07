package protocol

import "time"

// ListConversationsPayload is the body of a list_conversations frame
// (docs/protocol-mobile.md § list_conversations). Phone → binary. The
// payload is empty by spec; the type exists so the dispatcher can decode
// into a concrete value rather than a json.RawMessage.
type ListConversationsPayload struct{}

// ConversationsPayload is the body of a conversations frame
// (docs/protocol-mobile.md § conversations). Binary → phone, sent in
// reply to a list_conversations request. Order of the Conversations
// slice is preserved from the wire — the binary is the source of truth
// for ordering (e.g. most-recently-used first); this type does not
// reorder.
type ConversationsPayload struct {
	Conversations []ConversationSummary `json:"conversations"`
}

// ConversationSummary is one row of a ConversationsPayload
// (docs/protocol-mobile.md § conversations). Name is a pointer because
// the spec admits null on the wire (an unnamed scratch conversation),
// and is intentionally not tagged json:",omitempty": the spec example
// shows "name": null explicitly, and omitempty on a nil pointer would
// drop the key entirely, breaking byte-equivalent round-trip.
type ConversationSummary struct {
	ID         string  `json:"id"`
	Name       *string `json:"name"`
	IsPromoted bool    `json:"is_promoted"`
	// IsArchived is the conversation's durable archived flag. Always
	// serialized (no omitempty, unlike the on-disk Conversation.IsArchived):
	// a client partitions active vs. archived and counts each side, so it must
	// read the flag on active rows too, where the value is false.
	IsArchived bool   `json:"is_archived"`
	Cwd        string `json:"cwd"`
	// WorkspaceLabel is the operator-set display name stored for the workspace
	// at this row's own Cwd (#2208), so a client renders the chosen name without
	// a second read verb. It belongs to the workspace, not to this conversation:
	// N rows sharing one Cwd carry one label, and a row in another workspace
	// carries its own.
	//
	// A pointer without omitempty, the same discipline as Name's above, but here
	// the absent omitempty is a client-visible contract rather than only a
	// round-trip property: a workspace with no stored label must serialize an
	// explicit null, and the consuming clients fail closed on a missing key the
	// way they already do for a missing cwd. Nil is "no label stored"; a non-nil
	// pointer to "" is the distinct explicitly-empty label the registry admits.
	WorkspaceLabel *string   `json:"workspace_label"`
	LastMessageTS  time.Time `json:"last_message_ts"`
	LastUsedAt     time.Time `json:"last_used_at"`
}
