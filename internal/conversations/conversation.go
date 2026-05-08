// Package conversations defines the Phase 3 Conversation entity: a long-lived
// thread that owns a sequence of underlying claude sessions and carries
// presentation metadata (name, promoted/unpromoted state).
//
// This package is intentionally I/O-free. Persistence (sessions.json-style
// registry on disk) lands in #217.
package conversations

import "time"

// ConversationID is a per-conversation identifier. Distinct from
// sessions.SessionID so that a value of one type cannot be silently passed
// where the other is expected — a Conversation carries both its own ID and a
// CurrentSessionID, and confusing them is the most plausible bug in the
// upcoming registry/API code.
//
// The empty ConversationID ("") is the unset sentinel. Format conventions
// (UUIDv4 vs. other) are not fixed here; #217 owns the generator and the
// validity predicate.
type ConversationID string

// Conversation is the on-disk and in-memory shape of a Phase 3 conversation.
// Field tags are snake_case to match the existing sessions registry style
// (internal/sessions/registry.go).
//
// Field ordering is preserved exactly as the AC requires; do not re-order to
// optimize struct padding — the JSON encoding is the source of truth and
// reviewer diff stability matters more than a few padding bytes per record.
type Conversation struct {
	// ID is the conversation's stable identifier. Always present.
	ID ConversationID `json:"id"`

	// Name is the user-visible display name. A pointer so that "absent"
	// (nil — the user has never named this conversation) is distinguishable
	// from "explicitly empty" (non-nil pointer to ""). Unpromoted
	// conversations typically leave this nil; promoted conversations
	// (channels) usually carry a name.
	Name *string `json:"name,omitempty"`

	// Cwd is the absolute working directory captured at conversation
	// creation time. Always present; never updated after creation.
	Cwd string `json:"cwd"`

	// CurrentSessionID is the underlying claude session this conversation
	// currently points at. Empty string when no session is bound (e.g., a
	// freshly created conversation that has not yet been started, or one
	// whose session has been archived). Empty values are omitted from the
	// JSON output.
	CurrentSessionID string `json:"current_session_id,omitempty"`

	// SessionHistory is the ordered list of prior session IDs that this
	// conversation has pointed at, in chronological (oldest-first) order.
	// The most recently retired session sits at the tail
	// (SessionHistory[len-1]); rotation appends in place
	// (append(SessionHistory, prevID)). Chronological ordering is chosen
	// because it matches the natural append pattern and avoids O(n) shifts
	// on every rotation; presentation layers that want newest-first can
	// reverse on read. An empty/nil slice is omitted from JSON output.
	SessionHistory []string `json:"session_history,omitempty"`

	// IsPromoted distinguishes the two conversation modes:
	//   false — discussion (ephemeral, eligible for auto-archive)
	//   true  — channel    (long-lived, named, exempt from auto-archive)
	// Always serialized; the field is meaningful in both states and the
	// unpromoted default ("discussion") must be explicit on disk.
	IsPromoted bool `json:"is_promoted"`

	// LastUsedAt is bumped whenever the conversation has user activity.
	// Used by "recently active" sorts and by the auto-archive predicate
	// (#219). Always present.
	LastUsedAt time.Time `json:"last_used_at"`
}
