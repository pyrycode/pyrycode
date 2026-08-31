# Conversations-write payloads (#274, + `RenameConversationPayload` #820, + `DeleteConversationPayload` / `ConversationDeletedPayload` #822, + `ArchiveConversationPayload` #881, + `ChangeWorkspacePayload` #823)

Bodies of the conversation create/promote/rename/delete/archive/change-workspace lifecycle (`docs/protocol-mobile.md` § Message types → `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated` / `rename_conversation` / `delete_conversation` / `conversation_deleted` / `archive_conversation` / `unarchive_conversation` / `change_workspace`). Phone → binary: `create_conversation`, `promote_conversation`, `rename_conversation`, `delete_conversation`, `archive_conversation`, `unarchive_conversation`, `change_workspace`. Binary → phone: `conversation_created` (reply, rides `in_reply_to`), `conversation_updated` (reply to `promote_conversation`, `rename_conversation`, `archive_conversation`, `unarchive_conversation`, and `change_workspace`, rides `in_reply_to`; documented elsewhere as a broadcast type but every producer — #820's rename, #881's archive/unarchive, #823's change_workspace — replies only to the requester, no live fan-out), `conversation_deleted` (reply to `delete_conversation`, rides `in_reply_to`, requester only — no live fan-out, see [codebase/822.md](../codebase/822.md)).

```go
type CreateConversationPayload struct {
    IsPromoted *bool   `json:"is_promoted"` // *T + no omitempty (see WHY comment on the struct)
    Name       *string `json:"name"`
    Cwd        *string `json:"cwd"`
}

type ConversationCreatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    Cwd        string    `json:"cwd"`
    Name       *string   `json:"name"`
    LastUsedAt time.Time `json:"last_used_at"`
}

type PromoteConversationPayload struct {
    ConversationID string `json:"conversation_id"`
    Name           string `json:"name"`
    Cwd            string `json:"cwd"`
}

// RenameConversationPayload (#820). Deliberately NOT a reuse of
// PromoteConversationPayload — promote also carries a required Cwd, which a
// rename neither has nor means.
type RenameConversationPayload struct {
    ConversationID string `json:"conversation_id"`
    Name           string `json:"name"`
}

// DeleteConversationPayload (#822). Hard (permanent) delete — the reversible
// path is archive/unarchive (#880/#881), a distinct verb.
type DeleteConversationPayload struct {
    ConversationID string `json:"conversation_id"`
}

// ConversationDeletedPayload (#822). Carries only the deleted id — unlike
// ConversationUpdatedPayload, there is no surviving record to project
// name/cwd/last_used_at from.
type ConversationDeletedPayload struct {
    ID string `json:"id"`
}

// ArchiveConversationPayload (#881) is the body of BOTH archive_conversation
// and unarchive_conversation — a symmetric toggle of one durable flag, so one
// id-only payload serves both verbs. Deliberately NOT a reuse of
// DeleteConversationPayload despite the identical shape (semantic coupling /
// false dependency).
type ArchiveConversationPayload struct {
    ConversationID string `json:"conversation_id"`
}

// ChangeWorkspacePayload (#823). "Workspace" IS the conversation's cwd — this
// codebase has no separate workspace-id concept — so the target is an untrusted
// filesystem path, not an id. Deliberately NOT a reuse of
// PromoteConversationPayload — promote also carries a required Name, which a
// workspace change neither has nor means.
type ChangeWorkspacePayload struct {
    ConversationID string `json:"conversation_id"`
    Cwd            string `json:"cwd"`
}

type ConversationUpdatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    IsArchived bool      `json:"is_archived"` // #881 — always serialized, no omitempty (see below)
    Name       *string   `json:"name"`
    Cwd        string    `json:"cwd"`
    LastUsedAt time.Time `json:"last_used_at"`
}
```

- **`*T` WITHOUT `omitempty` for every spec-optional field whose example wire shows `null`** — `CreateConversationPayload.{IsPromoted, Name, Cwd}`, `ConversationCreatedPayload.Name`, `ConversationUpdatedPayload.Name`. Same discipline as `SessionTransitionPayload.WorkspaceCwd` (#656) and `ConversationSummary.Name` (#273). The rationale is documented once in detail on `CreateConversationPayload` and cross-referenced from the others; this is the only struct in the slice with three optional pointers in a row, so it's the natural home for the comment block. `omitempty` on a nil pointer would drop the key entirely and break byte-equivalent round-trip with the spec example.
- **`CreateConversationPayload.IsPromoted` is `*bool` — pointer-to-zero round-trips as the scalar.** Wire `false` survives as a pointer-to-false (NOT collapsed to nil); wire `null` would survive as nil. The test pins the pointer-to-false branch (spec example is `"is_promoted": false`); the wire-null branch is covered by `Name` / `Cwd` on the same struct, so the `*bool` shape is exercised end-to-end across the slice.
- **Field declaration order matches each spec example verbatim** — `_created` has `{ID, IsPromoted, Cwd, Name, LastUsedAt}`, `_updated` has `{ID, IsPromoted, Name, Cwd, LastUsedAt}` (note `Name` / `Cwd` swap). Go's `encoding/json` emits fields in declaration order; the byte-equal round-trip enforces the swap is correct.
- **`LastUsedAt` is `time.Time` (RFC3339Nano-on-the-wire envelope rule).** Spec example values (`"2026-05-08T10:34:01Z"` / `"2026-05-08T10:34:30Z"`) have no fractional seconds; `time.Time.MarshalJSON` emits RFC3339Nano which omits the fractional component when none is present, so the round-trip is byte-identical with no custom marshaller. Padding fixtures with `.000Z` would break it. Tests use `time.Time.Equal`, never `==`.
- **`PromoteConversationPayload` is the only fully-required struct in the slice.** All three fields (`ConversationID`, `Name`, `Cwd`) are non-pointer `string`, no `omitempty`. Promotion requires a name and an effective cwd, and the conversation_id must resolve to an existing row — semantic gates the dispatcher / registry (`Registry.Promote`'s `ErrPromotion*` sentinels) enforce.
- **`RenameConversationPayload` (#820) is fully-required, two fields, no pointers.** Both `ConversationID` and `Name` are non-pointer `string` — a rename must name a target and a new title; there's no optional-field case to distinguish (contrast `CreateConversationPayload`'s three nullable fields). Empty/whitespace-only `Name` and an unresolvable `ConversationID` are semantic gates the dispatch handler enforces (`internal/relay/handlers.RenameConversation`), not this layer — same posture as `PromoteConversationPayload`.
- **`DeleteConversationPayload` (#822) is one required field, no pointers** — mirrors `PromoteConversationPayload` / `RenameConversationPayload`'s value-typed `ConversationID`. An unresolvable id is not a decode-layer concern: the dispatch handler (`internal/relay/handlers.DeleteConversation`) treats a miss as `conversation.not_found`, not a payload-shape error.
- **`ConversationDeletedPayload` (#822) is the minimal ack — one `id` field.** Deliberately not a reuse of `ConversationUpdatedPayload`: post-delete there is no row left to source `name`/`cwd`/`last_used_at` from. See [codebase/822.md](../codebase/822.md) for why `conversation_updated` doesn't fit.
- **`ArchiveConversationPayload` (#881) is one required field, no pointers — shared by both verbs.** Same value-typed `ConversationID` shape as `DeleteConversationPayload`, kept as a distinct type rather than a reuse: archive/unarchive and delete are semantically unrelated (reversible vs. permanent), so sharing a type would be a false dependency. An unresolvable id is not a decode-layer concern — the dispatch handler (`internal/relay/handlers.ArchiveConversation`) treats a miss as `conversation.not_found`.
- **`ChangeWorkspacePayload` (#823) is two required fields, no pointers — the family's first payload carrying an untrusted filesystem path.** `Cwd` here is not a display string like `RenameConversationPayload.Name`; it becomes the conversation's spawn working directory, so the dispatch handler (`internal/relay/handlers.ChangeWorkspace`) confines it to `$HOME` (symlink-resolved) before storing — this is why the ticket carries `security-sensitive` where its rename/delete siblings do not. Empty/whitespace `Cwd` and a path that escapes `$HOME` are semantic gates the handler enforces, not this layer. See [codebase/823.md](../codebase/823.md).
- **`ConversationUpdatedPayload.IsArchived` (#881) has no `omitempty`, unlike the on-disk `Conversation.IsArchived`.** A client must read the flag on active rows too (value `false`) to partition active vs. archived state — an absent key couldn't distinguish "restored to active" from "old daemon." Placed immediately after `IsPromoted` to mirror `ConversationSummary`'s field order (#880) and group the two state bools; the fixture (`testdata/conversation_updated.json`) and both producers (`rename_conversation.go`, `archive_conversation.go`) were updated in lockstep so an archived conversation renamed still reports `is_archived: true` correctly.
- **`conversation_created.json` is the only fixture in the slice carrying `in_reply_to`** (`in_reply_to: 4`, matching the `create_conversation` frame at id 4). The test pins `env.InReplyTo != nil && *env.InReplyTo == 4`.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to #275, #272, #273. Required-field validation, name uniqueness, ID resolution, broadcast fan-out — all dispatcher / registry concerns.

Golden round-trip tests in `conversations_write_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against the matching fixture. Flat test functions (no table-driven; `TestRenameConversationPayload_RoundTrip` #820, `TestDeleteConversationPayload_RoundTrip` / `TestConversationDeletedPayload_RoundTrip` #822, and two `ArchiveConversationPayload` round-trips — one per envelope fixture (`archive_conversation.json` / `unarchive_conversation.json`), differing only in `type` — #881 added alongside the original four), each follows the sibling-slice template. **`ChangeWorkspacePayload` (#823) breaks this pattern** — no `TestChangeWorkspacePayload_RoundTrip` and no `testdata/change_workspace.json` fixture were added; its JSON shape is exercised only indirectly, via `json.Unmarshal` inside `internal/relay/handlers/change_workspace_test.go`. See [codebase/823.md](../codebase/823.md) § Lessons learned.
