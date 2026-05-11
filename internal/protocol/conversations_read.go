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
	ID            string    `json:"id"`
	Name          *string   `json:"name"`
	IsPromoted    bool      `json:"is_promoted"`
	Cwd           string    `json:"cwd"`
	LastMessageTS time.Time `json:"last_message_ts"`
	LastUsedAt    time.Time `json:"last_used_at"`
}
