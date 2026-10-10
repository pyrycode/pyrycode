package protocol

import "encoding/json"

// ThreadItem is a full daemon-built row, shared by live updates and thread pages.
// ID and Kind never change. IDs, orders and revisions are history-entry numbers
// below 2^53; producers enforce that range. Unknown kinds and status words remain
// display data, with Summary supplying a plain fallback and Active stating whether
// the item can still change by itself.
type ThreadItem struct {
	ID   uint64 `json:"id"`
	Kind string `json:"kind"`
	// Order is absent for queued, dropped or lost sends until delivery supplies
	// its history entry order. Parent is absent for the main thread.
	Order      uint64 `json:"order,omitempty"`
	Rev        uint64 `json:"rev"`
	EndedOrder uint64 `json:"ended_order,omitempty"`
	// Session and Agent carry recorded attribution only, with no inferred default.
	// NoChild distinguishes positively no producing child from unknown provenance.
	Session string `json:"session,omitempty"`
	Agent   string `json:"agent,omitempty"`
	NoChild bool   `json:"no_child,omitempty"`
	Turn    string `json:"turn,omitempty"`
	Parent  uint64 `json:"parent,omitempty"`
	Status  string `json:"status"`
	Active  bool   `json:"active"`
	Shown   bool   `json:"shown"`
	Summary string `json:"summary"`
	Subtype string `json:"subtype,omitempty"`
	// Content retains kind-specific fields, including unknown fields, as inert
	// JSON. Clients must treat it as untrusted display data, never executable input.
	Content json.RawMessage `json:"content"`
}

// ThreadItemAddedPayload declares a new full item. It is not yet emitted.
// Version is the newest folded history entry; Epoch changes when item identities
// may no longer have the same meaning and clients must reload the conversation.
type ThreadItemAddedPayload struct {
	ConversationID string     `json:"conversation_id"`
	Epoch          string     `json:"epoch"`
	Version        uint64     `json:"version"`
	Item           ThreadItem `json:"item"`
}

// ThreadItemChangedPayload declares an in-place item update, not yet emitted.
// Clients apply it only when their item's revision equals BaseRev, otherwise
// requesting catch-up. Rev is the resulting revision; identity and kind stay fixed.
type ThreadItemChangedPayload struct {
	ConversationID string `json:"conversation_id"`
	Epoch          string `json:"epoch"`
	Version        uint64 `json:"version"`
	ItemID         uint64 `json:"item_id"`
	BaseRev        uint64 `json:"base_rev"`
	Rev            uint64 `json:"rev"`
	// Changes must be an object keyed by item field name, excluding id and kind.
	// Missing keys mean unchanged; supplied empty, false, zero or null values are
	// replacements. A content patch replaces the entire content value. Raw values
	// stay inert, including unknown fields; consumers own validation/application.
	Changes map[string]json.RawMessage `json:"changes"`
}

// ThreadTextAppendPayload declares a message text suffix, not yet emitted.
// Text is appended only when the item's revision equals BaseRev; Rev is the
// resulting revision. A mismatch requires catch-up rather than guessing.
type ThreadTextAppendPayload struct {
	ConversationID string `json:"conversation_id"`
	Epoch          string `json:"epoch"`
	Version        uint64 `json:"version"`
	ItemID         uint64 `json:"item_id"`
	BaseRev        uint64 `json:"base_rev"`
	Rev            uint64 `json:"rev"`
	Text           string `json:"text"`
}
