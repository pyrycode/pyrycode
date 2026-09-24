# Conversations-write payloads (#274, + `RenameConversationPayload` #820, + `DeleteConversationPayload` / `ConversationDeletedPayload` #822, + `ArchiveConversationPayload` #881, + `ChangeWorkspacePayload` #823, + `SetSystemPromptPayload` #2151)

Bodies of the conversation create/promote/rename/delete/archive/change-workspace/set-system-prompt lifecycle (`docs/protocol-mobile.md` § Message types → `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated` / `rename_conversation` / `delete_conversation` / `conversation_deleted` / `archive_conversation` / `unarchive_conversation` / `change_workspace` / `set_system_prompt`). Phone → binary: `create_conversation`, `promote_conversation`, `rename_conversation`, `delete_conversation`, `archive_conversation`, `unarchive_conversation`, `change_workspace`, `set_system_prompt`. Binary → phone: `conversation_created` (reply, rides `in_reply_to`), `conversation_updated` (reply to `promote_conversation`, `rename_conversation`, `archive_conversation`, `unarchive_conversation`, `change_workspace`, and `set_system_prompt`, rides `in_reply_to` and carries no live fan-out on any of those six — #820's rename, #881's archive/unarchive, #823's change_workspace, #2151's set_system_prompt all reply to the requester only; **plus, since #2156, one genuinely unsolicited producer**: a host-side `pyry channel new` create pushes the same frame, with no `in_reply_to`, to every interactive-capable conn — see [control-plane.md § Fanning `channel.new` out](control-plane.md#fanning-channelnew-out-conversation_updated-as-an-unsolicited-push-2156)), `conversation_deleted` (reply to `delete_conversation`, rides `in_reply_to`, requester only — no live fan-out, see [codebase/822.md](../codebase/822.md)).

```go
type CreateConversationPayload struct {
    IsPromoted *bool   `json:"is_promoted"` // *T + no omitempty (see WHY comment on the struct)
    Name       *string `json:"name"`
    Cwd        *string `json:"cwd"`
}

type ConversationCreatedPayload struct {
    ID             string    `json:"id"`
    IsPromoted     bool      `json:"is_promoted"`
    Cwd            string    `json:"cwd"`
    WorkspaceLabel *string   `json:"workspace_label"` // #2210 — see WorkspaceLabel note below
    Name           *string   `json:"name"`
    LastUsedAt     time.Time `json:"last_used_at"`
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
    ID             string    `json:"id"`
    IsPromoted     bool      `json:"is_promoted"`
    IsArchived     bool      `json:"is_archived"` // #881 — always serialized, no omitempty (see below)
    IsMuted        bool      `json:"is_muted"`    // #2571 — always serialized, no omitempty (see below)
    Name           *string   `json:"name"`
    Cwd            string    `json:"cwd"`
    WorkspaceLabel *string   `json:"workspace_label"` // #2210 — see WorkspaceLabel note below
    LastUsedAt     time.Time `json:"last_used_at"`
}

// SetSystemPromptPayload (#2151). SystemPrompt is *string, not string — nil
// clears, non-nil "" is a distinct explicitly-empty state, non-nil text is
// stored verbatim — mapping 1:1 onto Registry.SetSystemPrompt's argument. A
// string field could not express "clear" as anything other than "".
// Deliberately NOT a reuse of ArchiveConversationPayload/ChangeWorkspacePayload
// despite the shared ConversationID field (semantic coupling / false
// dependency, this file's standing rule).
type SetSystemPromptPayload struct {
    ConversationID string  `json:"conversation_id"`
    SystemPrompt   *string `json:"system_prompt"`
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
- **`SetSystemPromptPayload` (#2151) is the group's second field-omitting reply: `ConversationUpdatedPayload` gains no `system_prompt` field for it.** Every other verb in this file that reuses `conversation_updated` has a field on that struct for what it changed (`Name`, `Cwd`, `IsArchived`); this one deliberately does not, even though up to 8192 bytes would fit. The record is documented as broadcast to every phone on the server-id, and the requester is the only party who asked about this value — a field would widen that audience for free. A projection type that omits a field is a structural leak barrier: the reply *cannot* carry the prompt, not merely doesn't today. Reading the value back is a distinct verb, `request_system_prompt` / `system_prompt` (#2152, see [`protocol-package-types-system-prompt-payloads.md`](protocol-package-types-system-prompt-payloads.md)), and it also never echoes the prompt into a broadcast type — its reply is unicast, correlated by `InReplyTo`.
- **A JSON payload can never carry invalid UTF-8 into `SystemPrompt`, which makes one of the three registry sentinels unreachable through this handler.** `encoding/json` substitutes U+FFFD for both an invalid byte and an unpaired surrogate escape while decoding a Go string — measured against `"bad \xed\xa0\x80 bytes"`, `"bad \ud800 bytes"`, and a bare `"\xff\xfe"`, all three decode to valid UTF-8. `Registry.SetSystemPrompt`'s `ErrSystemPromptInvalidUTF8` sentinel therefore exists for the registry's other callers (`Update`, a future CLI) but no wire-driven test of this payload can trigger it — the coercion happens before validation, so what gets length-checked is what gets stored, and the round-trip guarantee is unaffected. Worth knowing before writing a "hostile bytes over the wire" test against any `*string` field this package decodes: the wire can't produce the failure case at all, and a test expecting a reject there will fail by landing on the success path instead.
- **`ConversationUpdatedPayload.IsArchived` (#881) has no `omitempty`, unlike the on-disk `Conversation.IsArchived`.** A client must read the flag on active rows too (value `false`) to partition active vs. archived state — an absent key couldn't distinguish "restored to active" from "old daemon." Placed immediately after `IsPromoted` to mirror `ConversationSummary`'s field order (#880) and group the two state bools; the fixture (`testdata/conversation_updated.json`) and both producers (`rename_conversation.go`, `archive_conversation.go`) were updated in lockstep so an archived conversation renamed still reports `is_archived: true` correctly.
- **`IsMuted` (#2571) mirrors `IsArchived` field for field: no `omitempty`, placed directly after it, filled by all seven `conversation_updated` producers (`cmd/pyry/channel.go`'s `pyry channel new` push and the six `internal/relay/handlers` verbs) from the same stored-conversation snapshot each already reads for `IsArchived`.** No registry setter shipped with this field — the flag is always `false` in production until the sibling write-verb ticket lands; tests set `IsMuted: true` directly on a registry conversation. Mobile folds this record into its list in place (`ConversationListProjection.upsertConversation`), so a record that silently dropped the key would read as not-muted and a bare rename would un-mute the conversation on the wire without anyone asking for that.
- **`WorkspaceLabel` (#2210) is on both `ConversationCreatedPayload` and every `conversation_updated` producer — reply and the #2156 unsolicited push alike — and is deliberately not the `system_prompt` precedent it might look like.** Same `*string`, no-`omitempty`, second-return-derives-presence discipline as `ConversationSummary.WorkspaceLabel`, positioned after `Cwd` on both structs to match. `SetSystemPromptPayload` above shows the opposite call: `conversation_updated` gains no field for it because that reply broadcasts to every phone on the server-id and the prompt is reachable from no other read path, so a field would widen who can learn it. The label doesn't carry that risk — #2208 already put it on every `list_conversations` row available to the same audience these two frames reach, so filling it here only changes *when* a client learns a value it may already hold, never *who* can. Two producers (`change_workspace`, `rename_conversation`) build their payload inside `conversations.Registry.Update`'s callback and must read the label *after* `Update` returns, never inside it — see [`conversations-registry-crud.md`](conversations-registry-crud.md) § `WorkspaceLabel`.
- **`ConversationUpdatedPayload` (#2156) gained a second producer shape without gaining a field.** The struct is unchanged; what changed is that one producer — a host-side `pyry channel new` create — populates it from a `reg.Get` read-back and pushes it with no `in_reply_to`, instead of replying to a request. Its six fields are a strict subset of `ConversationSummary`'s seven, so the push is not a disclosure widening: any conn that can receive it could already pull the same row via `list_conversations`. See [control-plane.md § Fanning `channel.new` out](control-plane.md#fanning-channelnew-out-conversation_updated-as-an-unsolicited-push-2156).
- **`conversation_created.json` is the only fixture in the slice carrying `in_reply_to`** (`in_reply_to: 4`, matching the `create_conversation` frame at id 4). The test pins `env.InReplyTo != nil && *env.InReplyTo == 4`.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to #275, #272, #273. Required-field validation, name uniqueness, ID resolution, broadcast fan-out — all dispatcher / registry concerns.

Golden round-trip tests in `conversations_write_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against the matching fixture. Flat test functions (no table-driven; `TestRenameConversationPayload_RoundTrip` #820, `TestDeleteConversationPayload_RoundTrip` / `TestConversationDeletedPayload_RoundTrip` #822, and two `ArchiveConversationPayload` round-trips — one per envelope fixture (`archive_conversation.json` / `unarchive_conversation.json`), differing only in `type` — #881 added alongside the original four), each follows the sibling-slice template. **`ChangeWorkspacePayload` (#823) breaks this pattern** — no `TestChangeWorkspacePayload_RoundTrip` and no `testdata/change_workspace.json` fixture were added; its JSON shape is exercised only indirectly, via `json.Unmarshal` inside `internal/relay/handlers/change_workspace_test.go`. See [codebase/823.md](../codebase/823.md) § Lessons learned.

**A field added to `Envelope.Payload`'s underlying struct is invisible to these round-trip tests, `WorkspaceLabel` (#2210) included** — `Envelope.Payload` is `json.RawMessage`, so re-marshalling an envelope writes the fixture's original payload bytes back regardless of what the struct gained. `conversation_created.json` / `conversation_updated.json` gained the key anyway (one a real label, one `null`, so both states have a hand-written example), but what actually proves the field is on the wire is the decoded per-field assertions these tests already make, extended to cover it — the same trap and the same fix as `ConversationSummary.WorkspaceLabel` in [`protocol-package-types-conversations-read-payloads.md`](protocol-package-types-conversations-read-payloads.md), whose raw-bytes null-vs-omitted assertion is the pattern to copy for any nullable-but-mandatory field added here.

**A required (non-pointer) `bool` field with no `omitempty`, `IsMuted` (#2571) included, needs a fix the fixture-editing trick above cannot supply, because a decoded assertion can't tell the two failure modes apart either.** A `bool` zero value is `false` whether the key was omitted or serialized as `false` — so unlike `Name` / `WorkspaceLabel`'s nil-vs-dropped-null distinction, there is no decoded state that only the "present" branch produces, and no per-field assertion (decoded or fixture-based) can catch an `omitempty` tag that should not be there. `TestIsMuted_AlwaysSerialized` (`conversations_write_test.go`) instead marshals a zero-value `ConversationSummary{}` and `ConversationUpdatedPayload{}` directly — bypassing `Envelope` entirely — and asserts the raw output contains the literal `"is_muted":false`. Any future required-bool field on either struct needs the same direct-marshal test, not a fixture or round-trip one.
