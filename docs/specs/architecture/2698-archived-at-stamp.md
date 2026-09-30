# #2698 — Record when a conversation was archived and report it in the list

## Files read

- `internal/conversations/conversation.go` → `Conversation.IsArchived` — the omitempty "absent key decodes as the default, no migration" contract the new `ArchivedAt` field follows.
- `internal/conversations/registry.go` → `Registry.SetArchived` — the single-field mutator that gains the stamp; its doc comment promises "exactly one field" and must be rewritten.
- `internal/relay/handlers/archive_conversation.go` → `ConversationArchiver`, `ArchiveConversation` — the one production caller of `SetArchived`; serves both verbs.
- `internal/relay/handlers/list_conversations.go` → `ListConversationsWithAgents` — the only `ConversationSummary` construction site.
- `internal/protocol/conversations_read.go` → `ConversationSummary` — `WorkspaceLabel`'s "pointer, no omitempty, explicit null" discipline is the model for `archived_at`.
- `internal/protocol/conversations_read_test.go` → `TestConversationsPayload_RoundTrip` — its byte comparison re-marshals the envelope whose payload is a `json.RawMessage`, so it cannot see a new struct field; only decoded assertions prove the key is carried.
- `internal/conversations/registry_test.go` → `TestRegistry_SetArchived_*`, `TestRegistry_Save_ActiveOmitsArchivedKey` — call sites to update and the byte-stability pattern to mirror.
- `cmd/pyry/workspace_seed_test.go` → `TestSeedDefaultWorkspace_MarkedRegistryLeftAlone` — one more `SetArchived` call site.
- `internal/relay/handlers/archive_conversation_test.go` → `newArchiveConvReg`, `newArchiveConvConn` — real-registry handler test scaffolding to reuse.

No in-flight feature branch touches these files.

## Context

The owner decided both clients order the Archive screen newest-archived first. The daemon records no archive time today; `last_message_ts` is a copy of `LastUsedAt`. Manual archive (`archive_conversation`) is the only path that sets `IsArchived`; the idle sweep hard-deletes and is out of scope. Rows archived before this change have no stamp and report `null`; clients fall back to `last_used_at`.

## Design

**Record.** `Conversation.ArchivedAt *time.Time` tagged `json:"archived_at,omitempty"`. Nil means "not archived, or archived before #2698". A registry with no newly archived rows re-serializes byte-identically.

**Mutator.** `Registry.SetArchived(id ConversationID, archived bool, now time.Time) bool`. Under `r.mu`, on hit:

- `archived && !IsArchived` → `IsArchived = true`, `ArchivedAt = &now.UTC()`.
- `archived && IsArchived` → no change. The original stamp is kept; a legacy archived row with nil stays nil (re-archiving must not invent a time that would reorder it as newest).
- `!archived` → `IsArchived = false`, `ArchivedAt = nil`.

Now it touches exactly the two archive fields, which move together under one lock so they cannot disagree. Caller passes `now`, following the sweep's convention.

**Handler.** `ConversationArchiver.SetArchived` gains the `now time.Time` parameter; `ArchiveConversation` passes `time.Now()`. Reply payload (`conversation_updated`) unchanged. Logging unchanged — ids only, the stamp is never logged.

**Wire.** `ConversationSummary.ArchivedAt *time.Time` tagged `json:"archived_at"` (no omitempty: always serialized, `null` when nil), placed after `IsMuted`. `ListConversationsWithAgents` projects `conv.ArchivedAt`. Row order and all other fields unchanged.

**Fixture.** `internal/protocol/testdata/conversations.json` gets three rows: stamped archived, legacy archived with `"archived_at":null`, active with `"archived_at":null`.

## Concurrency model

No new goroutines. The flag and stamp are written together inside `SetArchived`'s existing critical section. `Get`/`List` already return copies; the `*time.Time` is freshly allocated per archive and never mutated in place, so sharing the pointer across copies is safe.

## Error handling

No new failure modes. Miss still returns false and mutates nothing.

## Testing strategy

- `internal/conversations/registry_test.go`:
  - `SetArchived` hit: archive stamps `ArchivedAt == now` (UTC), other fields untouched; re-archive with a later `now` keeps the original stamp; unarchive clears to nil.
  - Legacy literal with an archived row lacking `archived_at` loads with `ArchivedAt == nil`; Save writes no `archived_at` key and Save→Load→Save is a fixed point.
  - Round-trip: a stamped row survives Save→Load.
- `internal/protocol/conversations_read_test.go`: decoded assertions for the three fixture rows (stamp parsed, two nils); marshal of a nil-stamp `ConversationSummary` contains `"archived_at":null`.
- `internal/relay/handlers/archive_conversation_test.go`: new test driving the real `ArchiveConversation` and `ListConversationsWithAgents` over a real `Registry`: archive → list reads a stamp in `[before, after]`; re-archive → list reads the same stamp; `Load` from the saved file → list reads the stamp; unarchive → list reads explicit `null` (raw key present).
- Existing `SetArchived` call sites updated for the new signature.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md`, the `conversations` row of the message table (the one describing `workspace_label` and `is_muted`) must document `archived_at` — RFC3339 UTC, set by `archive_conversation`, kept on re-archive, cleared by `unarchive_conversation`; nullable but never omitted; `null` for active rows and rows archived before this change; clients order the Archive screen by it, falling back to `last_used_at` when `null`; `conversation_updated` does not carry it.

## Open questions

- Re-archiving a legacy archived row (nil stamp): keep nil, per "keeps its original stamp". Resolved in Design.
