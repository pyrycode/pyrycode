# Workspace-folder payloads (`workspace.go`, #887)

Body of `create_workspace_folder` / `workspace_folder_created` (`docs/protocol-mobile.md` § `create_workspace_folder`). A **new file**, not an addition to `conversations_write.go`: unlike every verb in the conversations-write slice above, this one names no conversation and carries no `conversation_id` — it creates a directory on the daemon host and returns its path.

```go
type CreateWorkspaceFolderPayload struct {
    Parent string `json:"parent"`
    Name   string `json:"name"`
}

type WorkspaceFolderCreatedPayload struct {
    Path string `json:"path"`
}
```

- **Both fields required, value-typed strings — no pointers, no `omitempty`.** Same discipline as `PromoteConversationPayload` / `RenameConversationPayload`: no optional-field branch to distinguish.
- **`Parent` is an untrusted directory path; `Name` is untrusted and must be a single clean path element** — no separator, no `..`, not absolute, non-empty. Confinement to `$HOME` (via the reused `confineWorkdirToHomeCreating`) and the name-shape check are two independent, deterministic gates enforced by the dispatch handler (`internal/relay/handlers.CreateWorkspaceFolder`), not this layer: confinement alone does not guarantee the folder lands *directly* under `Parent` (a name like `sub/dir` stays inside `$HOME` yet escapes that guarantee). See [codebase/887.md](../codebase/887.md).
- **`WorkspaceFolderCreatedPayload.Path` is the canonical (symlink-resolved) absolute path of the created folder** — a fresh reply type, not a reuse of `ConversationUpdatedPayload` (there is no conversation row to project `name`/`cwd`/`last_used_at` from). Sent `in_reply_to` the request, requester only, no broadcast.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Same posture as the rest of the package.

Golden round-trip tests in `workspace_test.go` (`TestCreateWorkspaceFolderPayload_RoundTrip`, `TestWorkspaceFolderCreatedPayload_RoundTrip`) against `testdata/create_workspace_folder.json` / `testdata/workspace_folder_created.json` — this slice does **not** repeat `ChangeWorkspacePayload`'s round-trip-test gap.

### Recent-workspaces payloads (`workspace.go`, #888)

Body of `recent_workspaces` / `recent_workspaces_list` (`docs/protocol-mobile.md` § `recent_workspaces`). Appended to the same file as the `create_workspace_folder` payloads above (same domain — workspace wire messages), not a new file.

```go
type RecentWorkspacesPayload struct{}

type RecentWorkspacesListPayload struct {
    Workspaces []RecentWorkspace `json:"workspaces"`
}

type RecentWorkspace struct {
    Path       string    `json:"path"`
    LastUsedAt time.Time `json:"last_used_at"`
}
```

- **`RecentWorkspacesPayload` is empty by spec** — like `ListConversationsPayload`, it exists only so the dispatcher decodes a concrete value instead of a `json.RawMessage`.
- **`RecentWorkspacesListPayload.Workspaces` is always non-nil** (`make([]RecentWorkspace, 0, n)` at the handler), so an empty result marshals as `"workspaces":[]`, never `null`.
- **Neither `RecentWorkspace` field carries `omitempty`** — reply-side discipline: the client reads `path` and `last_used_at` on every row. `path` matches `WorkspaceFolderCreatedPayload.Path`; `last_used_at` matches `Conversation.LastUsedAt` / `ConversationSummary.LastUsedAt`.
- **Ordering is the source of truth, not a client-side sort key.** Entries are most-recent-first by the max `LastUsedAt` across the conversations sharing that `Cwd`, deduped so each distinct path appears exactly once — computed by `internal/relay/handlers.RecentWorkspaces`, not this layer. See [codebase/888.md](../codebase/888.md).
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Same posture as the rest of the package.

Golden round-trip tests in `workspace_test.go` (`TestRecentWorkspacesPayload_RoundTrip`, `TestRecentWorkspacesListPayload_RoundTrip`) against `testdata/recent_workspaces.json` / `testdata/recent_workspaces_list.json`.
