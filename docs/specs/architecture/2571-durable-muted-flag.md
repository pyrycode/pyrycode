# #2571 — A conversation carries a durable muted flag on list rows and update records

Short plan: one bool field mirrored from `IsArchived` through storage, the list row and the update record. No new type, no new state machine, no new failure mode.

## Files read

- `internal/conversations/conversation.go` → `Conversation.IsArchived` — the on-disk template (omitempty: absent key decodes false, all-unmuted file stays byte-identical).
- `internal/protocol/conversations_read.go` → `ConversationSummary.IsArchived` — wire template, always serialized.
- `internal/protocol/conversations_write.go` → `ConversationUpdatedPayload.IsArchived` — wire template, always serialized.
- `internal/relay/handlers/list_conversations.go` → `ListConversations` — the only `ConversationSummary` producer.
- The seven `ConversationUpdatedPayload{` producers (grep outside tests at `370c4f67`, re-run before PR):
  `cmd/pyry/channel.go` (`pyry channel new` announce), and in `internal/relay/handlers/`: `RenameConversation`, `ArchiveConversation` (archive + unarchive share one builder), `PromoteConversation`, `SetSystemPrompt`, `Autoname`, `ChangeWorkspace`.
- `internal/conversations/registry_test.go` → `TestRegistry_SetArchived_RoundTrip`, `TestRegistry_Load_AbsentArchivedKeyDecodesActive`, `TestRegistry_Save_ActiveOmitsArchivedKey` — tests to mirror.
- `internal/relay/handlers/list_conversations_test.go` → `TestListConversations_SurfacesArchivedFlag`; `rename_conversation_test.go` → `newRenameConvReg`, `TestRenameConversation_Success_UpdatesReplyAndRow`.

## Change

Add `IsMuted bool` to `conversations.Conversation` directly after `IsArchived`, tagged `json:"is_muted,omitempty"` with a comment carrying the same "absent key decodes as not muted, no migration" contract. Add `IsMuted bool` tagged `json:"is_muted"` (no omitempty) directly after `IsArchived` on `protocol.ConversationSummary` and `protocol.ConversationUpdatedPayload`, each with an always-serialized comment: a client folding a record into its list in place must read `false` explicitly, or a rename would read as "unmuted". Each of the seven update-record producers and the list-row producer gains one `IsMuted: <conv>.IsMuted` fill, read from the same stored conversation snapshot the adjacent `IsArchived` fill reads. No registry setter (the sibling verb ticket adds it, modelled on `Registry.SetArchived`); no `ListFilter` field.

**Size overage, stated.** Production files touched: 11 (3 type files + 8 producer files), over the 5-file line. The refiner stated this on the estimate line. A split into "storage + list row" and "update record" would ship a flag that every rename silently clears on mobile, so the halves cannot be verified apart; per the floor rule, it stays one ticket. Total written work ~200 lines.

No overlapping in-flight branch touches these files (checked at plan time).

## Testing strategy

- `conversations`: save→reload keeps a muted row muted and an unmuted row unmuted; a hand-written file without `is_muted` loads as not muted; an all-unmuted registry serializes with no `is_muted` key.
- `protocol`: marshalling a zero `ConversationSummary` and a zero `ConversationUpdatedPayload` emits `"is_muted":false` (the always-serialized pin — the fixture round-trip tests cannot see struct fields because `Envelope.Payload` is raw bytes).
- `relay/handlers`: `list_conversations` reply marks the muted row `true`, the other `false`; renaming a conversation seeded `IsMuted: true` replies `is_muted: true`. The other six producers get the one-line fill but no per-producer test (AC names the rename reply as the pinned one).

## Documentation handoff (pending — documentation stage)

`docs/protocol-mobile.md`: document `is_muted` on the `conversations` row and the `conversation_updated` record in the message-type table rows that describe them. Always present from a daemon that knows it; a client reads an absent key as not muted, so a client paired with an older daemon keeps notifying.
