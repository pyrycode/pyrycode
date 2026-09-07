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

### Rename-workspace payloads (`workspace.go`, #2207)

Body of `rename_workspace` / `workspace_updated` (`docs/protocol-mobile.md` § Renaming a workspace). Appended to the same file — same domain. Reuses no existing type: `workspace_updated` is a fresh reply rather than the conversation family's `conversation_updated`, because a workspace has no row of its own (N conversations may share one `cwd`) — an un-upgraded client drops the new type as unknown instead of mis-rendering a `conversation_updated` it already understands.

```go
const MaxWorkspaceLabelBytes = 128

type RenameWorkspacePayload struct {
    Path  string  `json:"path"`
    Label *string `json:"label"`
}

type WorkspaceUpdatedPayload struct {
    Path  string  `json:"path"`
    Label *string `json:"label"`
}
```

- **Two structurally-identical structs rather than one shared type**, mirroring `CreateWorkspaceFolderPayload` / `WorkspaceFolderCreatedPayload`: each is free to move independently without dragging the other across the wire boundary.
- **`Label` is `*string` on both, and neither carries `omitempty`.** `null` is how the request says "clear," distinctly from `""` (rejected as blank by the handler); the reply must be able to echo that same explicit `null` rather than an absent key. See [`conversations-registry-crud.md`](conversations-registry-crud.md) § `WorkspaceLabel`/`SetWorkspaceLabel` for `MaxWorkspaceLabelBytes`'s value and where it was derived from (the list-shaped consumer, #2208, not this single field).
- **A `*string` reply field's `omitempty` hazard is only catchable by a fixture carrying the null.** `canonical` in `workspace_test.go` is `json.Compact`, so a golden round-trip is a byte comparison — but a *set* label marshals identically whether or not `omitempty` is present, so a set-value fixture cannot discriminate an accidental one. Only a fixture holding an explicit `"label":null` reddens, because a nil pointer under `omitempty` marshals to an absent key instead. `testdata/rename_workspace.json` carries a set label; `testdata/workspace_updated.json` deliberately carries the clear, for this reason — the two fixtures between them cover both states. The same split is worth copying for any future nullable reply field: put the null case in whichever fixture is the one that must prove it round-trips as `null`.
- **`WorkspaceUpdatedPayload.Path` is projected from the matched conversation's `Cwd`, never from the request**, even though the two are byte-equal by the match condition that gates the success path. The two values are provably identical, so this changes no byte on the wire — it changes where the bytes come from, turning "the echo is safe because the path matched" from an argued property into a structural one (a reject branch can therefore never reach this code path with a request-derived value at all, since every reject answers through the static-message `replyError` path instead). Flagged SHOULD FIX in #2207's security review; landed in the handler, not this layer, but the payload's field order documents the intent.
- Pure DTOs: no methods, no constructors, no `Validate()`. Same posture as the rest of the package.

Golden round-trip tests in `workspace_test.go` against `testdata/rename_workspace.json` / `testdata/workspace_updated.json`, asserting each field's value (distinct across the two fixtures) before the `canonical` byte comparison.
