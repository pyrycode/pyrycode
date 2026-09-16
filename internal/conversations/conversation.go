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

	// Cwd is the absolute working directory recorded for the conversation,
	// captured at creation time. Always present. Updated by the change_workspace
	// verb (#823), which stores the $HOME-confined realpath of a client-chosen
	// target; the new folder takes effect on the conversation's next fresh
	// session spawn (conv.Cwd is deliberately decoupled from a running session's
	// captured spawn WorkDir, #685/#686).
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
	//   false — discussion (ephemeral, eligible for auto-archive unless IsArchived)
	//   true  — channel    (long-lived, named, exempt from auto-archive)
	// Always serialized; the field is meaningful in both states and the
	// unpromoted default ("discussion") must be explicit on disk.
	IsPromoted bool `json:"is_promoted"`

	// IsArchived is the durable manual-archive flag: true means the user
	// archived this conversation, false means it is active. Flipped by
	// Registry.SetArchived and consumed by the archive/unarchive verbs (#881)
	// and by ShouldArchive, which exempts archived rows from the idle sweep's
	// hard delete (#1488).
	//
	// omitempty is deliberate and, unlike IsPromoted, correct here: the
	// contract is "absent key decodes as active, with no migration step." A
	// pre-#880 on-disk row (no is_archived key) decodes to false = active, and
	// an active conversation re-serializes with the key omitted — so a registry
	// of all-active rows is byte-identical to its pre-#880 form. Only genuinely
	// archived rows gain "is_archived": true. Do not "fix" this to drop
	// omitempty for consistency with IsPromoted: their contracts are opposite.
	IsArchived bool `json:"is_archived,omitempty"`

	// SystemPrompt is the operator-set system prompt this conversation carries,
	// so two conversations on the same repository can be told to behave
	// differently. A pointer for the same reason as Name: nil is "absent" (no
	// prompt — the default), a non-nil pointer to "" is "explicitly empty". A
	// plain string with omitempty cannot express that split; both states would
	// serialize away and both would decode to "".
	//
	// omitempty carries the same contract as IsArchived's, for the same reason:
	// "an absent key decodes as no prompt, with no migration step." omitempty on
	// a pointer tests the pointer, not the pointee, so a nil prompt omits the key
	// while a non-nil pointer to "" still emits "system_prompt": "" — which is
	// what keeps the two states distinguishable on disk. A registry whose rows
	// all hold nil is therefore byte-identical to its pre-#2149 form.
	//
	// Written only by Registry.SetSystemPrompt, which bounds the value at
	// MaxSystemPromptBytes and refuses invalid UTF-8; nothing mints the
	// explicitly-empty state today. Read by the spawn path and the read-back
	// verb (#2150, #2152).
	SystemPrompt *string `json:"system_prompt,omitempty"`

	// LastContextUsage is the last context-window reading claude reported for
	// this conversation (#2460), so a client that reconnects, restarts, or opens
	// the conversation from a second device is shown the real numbers rather than
	// nothing or a transcript guess. nil means claude has never reported for this
	// conversation.
	//
	// A pointer for a reason adjacent to Name's and SystemPrompt's but not
	// identical: those need one to split "absent" from "explicitly empty", while
	// here a zero-valued reading is a FALSE reading — 0 tokens, 0%, the zero time
	// — that a client would render as fact. There is no explicitly-empty state
	// and nothing mints one: the only door, Registry.SetLastContextUsage, takes a
	// value rather than a pointer, so "clear it back to nil" is unreachable.
	//
	// omitempty carries the same contract as IsArchived's and SystemPrompt's: "an
	// absent key decodes as no reading, with no migration step." omitempty on a
	// pointer tests the pointer, so a registry whose rows all hold nil is
	// byte-identical to its pre-#2460 form.
	//
	// Written by both producers of a reading — the post-turn emitter arm (#2371)
	// and the on-demand request_context_usage flight (#2431) — through the one
	// setter. Last write wins; see SetLastContextUsage.
	LastContextUsage *ContextUsageReading `json:"last_context_usage,omitempty"`

	// PendingChannelPosts is the text posted into this channel that claude has not
	// been shown yet, oldest first (#2499). A `channel.post` records the content in
	// the durable log and pushes it to connected clients, but deliberately starts no
	// turn, so without this the operator's reply reaches claude with nothing before
	// it. The daemon's delivery seam carries these ahead of the next user turn it
	// writes and clears them once that write is confirmed.
	//
	// THIS IS THE ONE FIELD ON THIS RECORD THAT HOLDS CONVERSATION CONTENT, and it
	// is why the file's mode matters: Save already writes at 0600 through a temp +
	// rename, and SystemPrompt is already operator-authored content here, so the
	// row's sensitivity class is unchanged. Nothing in this package logs a record
	// field, so the text cannot leave the file by way of a log line.
	//
	// It is durable rather than in-memory because the whole point is a question
	// posted in the morning still being carried by a reply that evening, across a
	// daemon restart in between.
	//
	// omitempty carries the same contract as IsArchived's, SystemPrompt's and
	// LastContextUsage's: "an absent key decodes as nothing pending, with no
	// migration step." On a slice omitempty tests length, so a record whose last
	// entry was cleared omits the key exactly as one that never held a post does.
	//
	// Bounded by MaxPendingChannelPosts and MaxPendingChannelPostsBytes, enforced at
	// the one door — AppendPendingChannelPost. The bound refuses the NEWEST post
	// rather than evicting the oldest; that direction is load-bearing, and
	// ClearPendingChannelPosts records why.
	PendingChannelPosts []string `json:"pending_channel_posts,omitempty"`

	// LastUsedAt is bumped whenever the conversation has user activity.
	// Used by "recently active" sorts and by the auto-archive predicate
	// (#219). Always present.
	LastUsedAt time.Time `json:"last_used_at"`
}

// ContextUsageReading is the SUMMARY of one context-window reading: the headline
// numbers a reconnecting client needs, and nothing else.
//
// THE THREE INVENTORIES ARE DELIBERATELY ABSENT. The event this is projected from
// (turnevent.ContextUsage) also carries per-category, per-MCP-tool and
// per-memory-file breakdowns with their dropped counts. Those are not stored, for
// two independent reasons: they carry workspace memory-file paths and MCP server
// names that the frame's no-log rule keeps out of records, and a client coming
// back needs the headline numbers rather than a breakdown taken some turns ago.
// Do not "complete" this type by adding them.
//
// All five keys always emit. A fresh session genuinely reads zero tokens at zero
// percent, so omitempty on the inner fields would encode a fact as an absence;
// the outer pointer on Conversation.LastContextUsage already carries the
// never-reported state.
//
// The values are claude-authored and arrive bounded: streamsup's
// decodeContextUsage caps Model at its own string bound and is the single decoder
// BOTH lanes share, so nothing here re-bounds it.
type ContextUsageReading struct {
	// Model is the model claude reported the reading for.
	Model string `json:"model"`

	// TotalTokens is the context the conversation currently occupies.
	TotalTokens int `json:"total_tokens"`

	// MaxTokens is the window that context is measured against.
	MaxTokens int `json:"max_tokens"`

	// Percentage is claude's own rounded TotalTokens-of-MaxTokens figure, carried
	// rather than recomputed so a client sees the number claude reported.
	Percentage int `json:"percentage"`

	// AsOf is when the daemon recorded the reading, always UTC — normalised by
	// SetLastContextUsage, which is what makes the encoded form RFC 3339 with a Z
	// offset. time.Time marshals with whatever offset it carries, so the
	// normalisation lives at the one door rather than at each producer.
	AsOf time.Time `json:"as_of"`
}
