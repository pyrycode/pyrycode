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
	ID         string `json:"id"`
	IsPromoted bool   `json:"is_promoted"`
	Cwd        string `json:"cwd"`
	// WorkspaceLabel is the operator-set display name stored for the workspace at
	// this frame's own Cwd (#2210), carrying the same value and the same contract
	// ConversationSummary.WorkspaceLabel does on a list row — see the rationale
	// there. Nullable but never omitted: a workspace with no stored label
	// serializes an explicit null, so a client may treat a missing key as a
	// malformed frame rather than as an unlabelled workspace. Nil is "no label
	// stored"; a non-nil pointer to "" is the distinct explicitly-empty label the
	// registry admits, and presence must come from the accessor's second return
	// rather than from a label != "" comparison, which collapses the two.
	WorkspaceLabel *string   `json:"workspace_label"`
	Name           *string   `json:"name"`
	LastUsedAt     time.Time `json:"last_used_at"`
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

// SetConversationMutedPayload is the body of a set_conversation_muted frame
// (#2572). Phone → binary. It names a target conversation and the muted value to
// store for it: true mutes, false unmutes, through one verb.
//
// Muted is a pointer because the key is required: an absent key (or JSON null)
// decodes to nil and the handler rejects it as protocol.malformed. A plain bool
// would read a missing key as false and silently unmute. No omitempty, for the
// round-trip reason given on CreateConversationPayload.
//
// Deliberately NOT a reuse of ArchiveConversationPayload, per the
// semantic-coupling rationale the sibling payloads document. The reply reuses
// ConversationUpdatedPayload verbatim.
type SetConversationMutedPayload struct {
	ConversationID string `json:"conversation_id"`
	Muted          *bool  `json:"muted"`
}

// ChangeWorkspacePayload is the body of a change_workspace frame
// (docs/protocol-mobile.md § change_workspace). Phone → binary. Both fields are
// spec-required: a change_workspace must name a target conversation and a target
// workspace path.
//
// "Workspace" IS the conversation's cwd — this codebase has no separate
// workspace-id concept — so the target is a filesystem path (mirroring
// create/promote cwd), not an id. The path is untrusted (a network-paired
// party supplies it) and becomes the conversation's spawn workdir on its next
// fresh session, so the daemon confines it to $HOME (symlink-resolved) before
// storing the resolved realpath. The reply reuses ConversationUpdatedPayload
// verbatim (the record still exists and only cwd changed).
//
// Deliberately NOT a reuse of PromoteConversationPayload — promote also carries
// a required Name, which a workspace change neither has nor means (semantic
// coupling / false dependency, the rationale the sibling payloads document).
type ChangeWorkspacePayload struct {
	ConversationID string `json:"conversation_id"`
	Cwd            string `json:"cwd"`
}

// SetSystemPromptPayload is the body of a set_system_prompt frame
// (docs/protocol-mobile.md § set_system_prompt). Phone → binary. It names a
// target conversation and the durable per-conversation system prompt to store
// for it — the operator-authored text appended to every session that
// conversation spawns from then on.
//
// SystemPrompt is a pointer because the verb has to express THREE states, and a
// plain string could only express two:
//
//	null (or the key absent) — clear: the conversation returns to spawning with
//	                          the daemon constant alone, exactly as it does today
//	""                       — explicitly empty (a distinct stored state)
//	"<text>"                 — stored verbatim, after validation
//
// null and an absent key are indistinguishable after decode and both mean
// clear. The pointer maps 1:1 onto conversations.Registry.SetSystemPrompt's own
// *string argument, which is the single validating door for all three states —
// including clear, which is why the field is nullable rather than the clear path
// getting a verb of its own.
//
// No omitempty, matching CreateConversationPayload's rationale: omitempty on a
// nil pointer would drop the key entirely and break byte-equivalent round-trip.
//
// The byte bound (conversations.MaxSystemPromptBytes) and the UTF-8 check are
// enforced at the registry, not here — this type carries the value, it does not
// police it.
//
// Deliberately NOT a reuse of ChangeWorkspacePayload or
// ArchiveConversationPayload, per the semantic-coupling rationale the sibling
// payloads document. The reply reuses ConversationUpdatedPayload verbatim — see
// the note there on why that record gains no prompt field.
type SetSystemPromptPayload struct {
	ConversationID string  `json:"conversation_id"`
	SystemPrompt   *string `json:"system_prompt"`
}

// ConversationUpdatedPayload is the body of a conversation_updated frame
// (docs/protocol-mobile.md § conversation_updated). Binary → phone,
// broadcast to all phones on this server-id. ID, IsPromoted, IsArchived, Cwd,
// LastUsedAt are required. Name is spec-optional (a previously unnamed
// conversation can be updated without acquiring a name) and is a pointer
// for the same round-trip reason given on CreateConversationPayload.
//
// It deliberately carries NO system_prompt field (#2151), unlike rename /
// archive / change_workspace where this record happens to already hold what
// changed. Two reasons. Security: this frame is broadcast to all phones on this
// server-id, so hanging up to 8192 bytes of operator text on it would widen the
// audience for a value only the requester asked about — a projection type that
// lacks the field cannot leak it, which is a stronger guarantee than a handler
// that merely declines to fill it in. Scope: reading the prompt back is #2152,
// which deliberately does not echo the text either. set_system_prompt therefore
// replies with an unchanged-looking record that confirms the write without
// carrying the value.
type ConversationUpdatedPayload struct {
	ID         string `json:"id"`
	IsPromoted bool   `json:"is_promoted"`
	// IsArchived is the conversation's durable archived flag. Always serialized
	// (no omitempty, unlike the on-disk Conversation.IsArchived): a client reads
	// the flag to partition active vs. archived, so it must read it on active
	// rows too, where the value is false — an absent key could not distinguish
	// "restored to active" from "old daemon." Placed right after IsPromoted to
	// mirror ConversationSummary and group the two state bools.
	IsArchived bool `json:"is_archived"`
	// IsMuted is the conversation's durable mute-notifications flag (#2571),
	// read from the stored conversation by every producer. Always serialized
	// (no omitempty, unlike the on-disk Conversation.IsMuted): a client folds
	// this record into its list in place, so a record that dropped the key
	// would read as not muted and a rename would silently unmute the channel.
	IsMuted bool    `json:"is_muted"`
	Name    *string `json:"name"`
	Cwd     string  `json:"cwd"`
	// WorkspaceLabel is the operator-set display name stored for the workspace at
	// this frame's own Cwd (#2210), so a client patching a row in place from a
	// pushed frame renders the operator's chosen name without re-listing to find
	// it. Same value, same contract as ConversationSummary.WorkspaceLabel on a
	// list row; nullable but never omitted, and presence comes from the registry
	// accessor's second return, never from a label != "" comparison.
	//
	// That this record admits a workspace label while refusing a system prompt is
	// not a contradiction, and the difference is audience. A prompt reaches no
	// other read path — it is on neither list_conversations nor session_settings
	// — so hanging it on a frame broadcast to every phone on this server-id would
	// create disclosure. The label is already on every list_conversations row
	// (#2208), readable by any paired client on demand, so carrying it here
	// widens no audience: it changes only whether a client learns the value at
	// push time or at its next list. Size points the same way — one 128-byte
	// label on a single-row frame, against 8192 bytes of prompt times every row.
	//
	// The value is an opaque operator-supplied string echoed verbatim. Its
	// 128-byte bound is enforced on the write path and is a size limit, not a
	// safety property: rendering it safely stays the client's job.
	WorkspaceLabel *string   `json:"workspace_label"`
	LastUsedAt     time.Time `json:"last_used_at"`
}
