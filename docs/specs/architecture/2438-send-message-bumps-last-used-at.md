# #2438 — `send_message` bumps `LastUsedAt` so the idle sweep spares conversations in use

## Files read

- `internal/relay/handlers/send_message.go` → `SendMessage` — the accept path, and the
  post-ack hook point the bump joins.
- `internal/relay/handlers/autoname.go` → `autoNameConversation`, `ConversationAutoNamer` —
  the registry-reaching pattern this change mirrors (one locked `Update`, then an eager
  `Save`, both after the ack). Its method set is the superset the new narrow interface is
  assignable from, which is why no constructor parameter changes.
- `internal/conversations/conversation.go` → `Conversation.LastUsedAt` — the field contract
  the code currently fails to honour: "bumped whenever the conversation has user activity".
- `internal/conversations/archive.go` → `ShouldArchive`, `archiveIdleThreshold` — the
  predicate AC 2 exercises. `now` is a parameter, not a package clock; that is why this
  ticket needs no clock seam of its own.
- `internal/conversations/registry.go` → `Registry.Update`, `Registry.Save` — the
  mutate-under-lock and atomic-persist primitives. Per ADR 022 the callback runs holding
  `mu`, so it must not re-enter the registry.
- `internal/relay/handlers/rename_conversation.go`, `set_system_prompt.go`,
  `change_workspace.go` → their `LastUsedAt: cv.LastUsedAt` carry-throughs — the
  metadata-edit rule this ticket must leave intact. Those verbs are edits, not uses.
- `internal/relay/handlers/send_message_test.go` → `TestSendMessage_AutoNamesUnnamedConversation`,
  `TestSendMessage_AcksBeforeAutoNaming`, `newAutoNameReg`, `autoNameSeedTime` — the fixtures
  the new tests reuse, and the one existing assertion this change reverses.
- `docs/knowledge/features/conversations-auto-archive.md` § "Out of scope" — records the
  deferral this ticket collects: *"Integration with `LastUsedAt` bumps — the future
  conversations API (rotate session, attach, send message) is what advances `LastUsedAt`;
  the predicate only reads it."* The send-message half lands here.
- `cmd/pyry/relay.go` → the sole `handlers.SendMessage` call site — `reg` and the resolved
  registry path are already wired, so this ticket has no consumer cascade.

## Context

`ShouldArchive` deletes an unpromoted, unarchived conversation once its `LastUsedAt` is 30
days old, and nothing on the messaging path ever advances that field. The only writers are
the creation stamps in `create_conversation.go` and `cmd/pyry/channel.go`; every other verb
carries the value through deliberately, because renaming, promoting, archiving and changing
a workspace are metadata edits rather than uses. The result is that a conversation in daily
use is swept 30 days after it was *created*. Sending a message is the one unambiguous use,
so that is where the bump belongs.

No ADR is warranted: this ticket discharges a deferral the auto-archive overview already
records, and introduces no new boundary.

## Design

One new step in `SendMessage`, one new narrow interface, both in `send_message.go`.

```go
// ConversationToucher is the registry write surface the last-used bump consumes.
type ConversationToucher interface {
	Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool
	Save(path string) error
}

func touchConversation(reg ConversationToucher, registryPath string, logger *slog.Logger, connID, conversationID string)
```

Behaviour of `touchConversation`: a nil `reg` returns (fail-closed, matching
`autoNameConversation`); otherwise one `Update` sets `cv.LastUsedAt = time.Now().UTC()`; a
missed row (`Update` reports false — deleted between enqueue and here) returns without
saving; otherwise `Save(registryPath)` persists, and only a failure is logged.

`SendMessage`'s signature does not change. It already takes `reg ConversationAutoNamer` and
`registryPath` for the naming step, and `ConversationAutoNamer`'s method set
(`Update`/`Save`/`WorkspaceLabel`) is a superset of `ConversationToucher`'s, so the existing
parameter passes straight through. Declaring the narrower interface anyway keeps each step's
stated contract honest: the auto-namer's doc says it writes `Name` only over a nil one, which
must not silently start meaning "and `LastUsedAt` too".

### Placement — after the ack, before the naming step

After the ack, for the reason `autoNameConversation` documents: the step costs an fsync, the
drain already owns the turn on another goroutine, and work done before the ack lets the
child's frames overtake it on the wire.

Before the naming step, so the `conversation_updated` record `autoNameConversation` snapshots
carries the *bumped* timestamp rather than a value that is stale the moment it is announced.
The snapshot line in that function reads the row under the lock and needs no edit; its
"carried through untouched" comment stays literally true of naming.

### Accepted, not routed

The ticket body says "after `router.Route` succeeds", but AC 1 says *accepted*, and the two
differ: a message can pass `Route` and still be refused for an unresolvable attachment or a
full backlog. The AC wins, and it also matches the hook point the body points at as the
model — naming is already reached only on an accepted message. The bump therefore sits on
the same side of the last reject branch, which makes "a rejected send does not bump"
structural rather than a guard.

### What this does not do

- **No `conversation_updated` fan-out.** A broadcast per accepted message would be a push
  storm carrying nothing a client does not already learn from the message frames it is
  receiving, and neither AC asks for one. The record announced by an auto-name still carries
  the fresh timestamp, as above.
- **No clock seam.** `time.Now()` is read directly. `ShouldArchive` already takes `now` as a
  parameter, so the sweep property is testable by choosing the evaluation instant instead of
  the stamp instant.
- **No change to the sibling metadata verbs.** Rename, promote, archive, set-system-prompt and
  change-workspace keep carrying `LastUsedAt` through.

## Concurrency model

No new goroutines. The bump is one `Registry.Update`, which runs its callback under the
registry mutex (ADR 022); the callback touches only `cv.LastUsedAt`, calls nothing, and
retains no pointer into the slice. `Save` serialises on the registry's own `saveMu` and is
atomic (temp file → fsync → rename), so a concurrent naming `Save` cannot interleave a
half-written file, and the later snapshot renames later.

Two `Save` calls land on the first message of an unnamed conversation (one here, one from
naming) and one on every message after. That is accepted: messages arrive at human pace, and
collapsing the two would mean folding the bump into the naming callback, which cannot work —
naming returns early for an already-named row and for an attachment-only message, both of
which are still uses.

## Error handling

- Nil registry → no bump, no log. An unwired seam writes nothing.
- `Update` misses (row deleted post-enqueue) → return before `Save`; there is nothing to
  persist and a save would only risk a spurious failure line.
- `Save` fails → log at **Warn** with `conversation_id` and `err`, and continue. The bump is
  self-healing: the in-memory value is what the running daemon's sweep loop reads, and the
  next accepted message re-stamps and re-persists. The naming step logs its own save failure
  at Error because that line doubles as its one published success event; here there is no
  success line at all (one per message would double the hot path's log volume for no
  operational value), so Warn is the honest level for the only line this step emits.
- Nothing here can fail the message. The ack has already gone out; the bump is a side effect
  on registry metadata, exactly as naming is.

## Testing strategy

In `internal/relay/handlers/send_message_test.go`:

- **`TestSendMessage_BumpsLastUsedAtOnAcceptedSendOnly`** (AC 1), table-driven over one
  accepted row plus the four reject branches already enumerated for naming — unknown
  conversation, no bound session, too many attachments, backlog full. The accepted row seeds
  a *named* conversation so the naming step is a no-op and the bump is isolated; it asserts
  the stored `LastUsedAt` falls in the window bracketing the call and is persisted (reload via
  `conversations.Load` off the same path). Each reject row asserts the seed instant is
  unchanged, in memory and on disk.
- **`TestSendMessage_BumpSparesIdleConversationFromSweep`** (AC 2). Seeds a row at 31 days
  ago, asserts `conversations.ShouldArchive(seed, time.Now())` is **true** first so the case
  is non-vacuous, runs the handler, then asserts `ShouldArchive(row, time.Now().Add(24*time.Hour))`
  is false — the predicate evaluated a day after the accepted message.
- **Existing assertion reversed.** `TestSendMessage_AutoNamesUnnamedConversation` asserts the
  pushed record's `LastUsedAt` equals the seeded instant ("naming is not a use"). Naming
  still is not a use, but the send that triggered it now is, so the record must carry the
  bumped time. The assertion becomes a window check, and `autoNameSeedTime`'s doc comment is
  corrected to say what it now anchors.
- `TestSendMessage_AcksBeforeAutoNaming` needs no change: its double records the outbound
  queue depth on every `Update`, and the bump — like naming — runs after the ack, so the
  observed depth stays 1.

Gate: `go test -race ./internal/relay/handlers/... ./internal/conversations/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Open questions

- **Should an accepted send announce `conversation_updated` so a client's recency sort stays
  live?** Resolved at design time: no — out of scope above. The sort input a client holds goes
  stale only until its next `list_conversations`, and a per-message broadcast is a real cost.
  Left here as the question a follow-up would reopen if a client ever reports drift.
- **Does any e2e spec couple a send with a `last_used_at` assertion?** Checked before writing:
  the specs asserting that field (`relay_v2_recent_workspaces_test.go`,
  `relay_v2_change_workspace_test.go`, `conv_sweep_test.go`) do not send messages, and the
  specs that send messages do not assert it. Nothing to reconcile.

## Documentation handoff

The ticket carries no documentation acceptance criteria and names no doc path. One item is
pending for the documentation stage:

- `docs/knowledge/features/conversations-auto-archive.md` § "Out of scope" still lists
  *"Integration with `LastUsedAt` bumps — the future conversations API (rotate session,
  attach, send message) is what advances `LastUsedAt`"*. The send-message half is no longer
  deferred; the bullet needs narrowing to the arms that remain (rotate session, attach), and
  § "How it works" should name `send_message` as the writer that keeps a live conversation out
  of the sweep. **Pending — documentation stage.** Not edited here.
