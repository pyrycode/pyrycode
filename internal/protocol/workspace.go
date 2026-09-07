package protocol

import "time"

// CreateWorkspaceFolderPayload is the body of a create_workspace_folder frame
// (docs/protocol-mobile.md § create_workspace_folder). Phone → binary. Both
// fields are spec-required and value-typed (mirroring PromoteConversationPayload):
//
//	Parent — the untrusted parent directory under which to create the folder
//	         (a "~"/"~/"-prefix anchors at the daemon's $HOME).
//	Name   — the new folder's single path element. It is untrusted and validated
//	         by the handler (no path separator, no "..", not absolute, non-empty)
//	         BEFORE it is joined under Parent, so the folder lands directly under
//	         Parent and never elsewhere.
//
// The daemon joins Parent + Name, confines the result to $HOME (symlink-resolved),
// and creates the directory. Deliberately NOT a reuse of any conversation payload:
// a workspace-folder create names no conversation and carries no id.
type CreateWorkspaceFolderPayload struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
}

// WorkspaceFolderCreatedPayload is the body of a workspace_folder_created frame
// (docs/protocol-mobile.md § workspace_folder_created). Binary → phone, sent in
// reply to a create_workspace_folder (in_reply_to). Path is the canonical
// (symlink-resolved) absolute path of the created folder, confined to $HOME.
type WorkspaceFolderCreatedPayload struct {
	Path string `json:"path"`
}

// RecentWorkspacesPayload is the body of a recent_workspaces frame
// (docs/protocol-mobile.md § recent_workspaces). Phone → binary. The payload is
// empty by spec; the type exists so the dispatcher can decode into a concrete
// value rather than a json.RawMessage (mirrors ListConversationsPayload).
type RecentWorkspacesPayload struct{}

// RecentWorkspacesListPayload is the body of a recent_workspaces_list frame
// (docs/protocol-mobile.md § recent_workspaces_list). Binary → phone, sent in
// reply to a recent_workspaces request. Ordering is the source of truth:
// entries are most-recent-first, deduped so each distinct workspace path
// appears exactly once. The slice is always non-nil so an empty result marshals
// as "workspaces":[] rather than null.
type RecentWorkspacesListPayload struct {
	Workspaces []RecentWorkspace `json:"workspaces"`
}

// RecentWorkspace is one row of a RecentWorkspacesListPayload
// (docs/protocol-mobile.md § recent_workspaces_list): one distinct workspace
// folder — its absolute path and the most-recent LastUsedAt across the
// conversations that share it. Neither field carries omitempty (reply-side
// discipline: the client reads both on every row to label and sort the list).
// path matches WorkspaceFolderCreatedPayload.Path; last_used_at matches
// Conversation.LastUsedAt / ConversationSummary.LastUsedAt.
type RecentWorkspace struct {
	Path       string    `json:"path"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// MaxWorkspaceLabelBytes bounds RenameWorkspacePayload.Label. UTF-8 BYTES, NOT
// RUNES, matching every other bound in this package. An over-bound label is
// REFUSED, never truncated, and the offending bytes stay out of the error reply.
//
// The value is NOT borrowed from MaxDeviceNameBytes even though it shares its
// number and its posture: a workspace label and a device name are unrelated
// fields, and a constant whose only justification is another constant rots the
// moment either field's use changes. Two native anchors:
//
// A label is one human-typed line rendered as a list row beside a folder path.
// 128 bytes is far past any hand-typed folder nickname while keeping one row
// unwrapped.
//
// AND IT IS PICKED AGAINST N LABELS IN ONE REPLY, NOT ONE LABEL IN ISOLATION,
// which is the arithmetic conversations-registry-crud.md § WorkspaceLabel /
// SetWorkspaceLabel explicitly deferred to this field. #2208 embeds one label per
// conversation in a list_conversations reply against the 65519-byte v2
// application-envelope cap. Worst-case JSON escaping is 6 bytes per source byte
// (a control byte becomes \u00XX), so 128 bytes costs at most 768 on the wire
// plus ~18 for its key — call it ~790 per labelled row on top of that row's
// existing cost, admitting on the order of 80 fully-escaped worst-case rows. A
// realistic ASCII label of ~30 bytes costs ~50. Recorded here so #2208 does not
// have to re-derive it; a larger bound chosen here would have moved that decision
// into a ticket that cannot revisit it.
//
// A LENGTH CEILING IS NOT A SAFETY PROPERTY — MaxDeviceNameBytes' warning
// transfers unchanged, and it is restated rather than cross-referenced because
// the ceiling is exactly what a later reader is most likely to mistake for
// containment. 128 bytes accommodates an ANSI escape run or a newline-injection
// payload several times over. The label is an opaque display string: the daemon
// stores and echoes it, never logs it and never interpolates it into an error
// message, and rendering it safely is the client's.
const MaxWorkspaceLabelBytes = 128

// RenameWorkspacePayload is the body of a rename_workspace frame
// (docs/protocol-mobile.md § Renaming a workspace). Phone → binary.
//
//	Path  — the workspace being named: the exact cwd string stored on one or more
//	        conversations. A LOOKUP KEY AND NOTHING MORE — the daemon compares it
//	        byte-for-byte against stored cwds and uses it as a map key. It is never
//	        resolved, joined, stat-ed, opened or passed to a process, and a path
//	        matching no conversation is refused with CodeWorkspaceNotFound.
//	Label — the operator-chosen display name, NULLABLE so the three states stay
//	        distinct: null (or an absent key) clears, a blank-after-trimming value
//	        is REFUSED, and any other string is stored verbatim — untrimmed — up to
//	        MaxWorkspaceLabelBytes.
//
// Neither field carries omitempty. On Label that is load-bearing rather than
// stylistic: omitempty would collapse the deliberate null into an absent key,
// and while both decode to nil, only the explicit null survives a byte-level
// round-trip against the reply fixture.
//
// The bound and the non-blank check are the HANDLER's, not this type's. There is
// no UnmarshalJSON here: MintPairingPayload's decode-time check argues in its own
// comment that it is a deliberate departure from this package's pure-DTO posture
// and "not a general licence", justified by that field having no downstream
// validator. This field has one, and keeping every malformed branch together in
// the handler is what lets each carry its own static message.
type RenameWorkspacePayload struct {
	Path  string  `json:"path"`
	Label *string `json:"label"`
}

// WorkspaceUpdatedPayload is the body of a workspace_updated frame
// (docs/protocol-mobile.md § Renaming a workspace). Binary → phone, sent in
// reply to a rename_workspace (in_reply_to). Path is the workspace's stored cwd
// and Label is the label now stored for it — null when cleared, which is why the
// field carries no omitempty (see RenameWorkspacePayload).
//
// A separate type from RenameWorkspacePayload rather than a reuse of it, the same
// call CreateWorkspaceFolderPayload / WorkspaceFolderCreatedPayload made: the two
// are structurally identical today, and each stays free to change without
// dragging the other across the wire boundary.
//
// This slice replies only to the requester. Fanning the change out to other
// connected clients is #2209, at which point this type gains a second, unsolicited
// producer the way TypeConversationUpdated has.
type WorkspaceUpdatedPayload struct {
	Path  string  `json:"path"`
	Label *string `json:"label"`
}
