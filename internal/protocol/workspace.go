package protocol

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
