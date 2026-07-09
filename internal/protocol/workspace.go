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
