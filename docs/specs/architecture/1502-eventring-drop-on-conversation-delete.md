# #1502 — eventring: drop a conversation's ring entry when it is removed

## Files read

- `internal/eventring/ring.go` → `Ring`, `convRing`, `Append`, `After`, `NewestID`: the map the leak lives in, the ring-wide `nextID` counter that must survive a removal (#2022), and `After`'s "unknown conversation" branch that a dropped entry falls into.
- `internal/conversations/registry.go` → `Registry`, `Delete`: the one method both removal paths go through, so the one place a removal observer covers both.
- `internal/conversations/sweep.go` → `Sweep`: the idle sweep calls `reg.Delete` per archivable row. `sweep_loop.go` → `RunSweepLoop`, started by `sessions.Pool.Run`.
- `internal/relay/handlers/delete_conversation.go` → `DeleteConversation`: calls `reg.Delete` via the `ConversationDeleter` interface. Malformed returns before `Delete` runs, and not_found is a `Delete` miss.
- `cmd/pyry/relay.go` → the stream-mode branch that builds `newInteractiveTurnEmitterV2` and calls `mgr.SetReplaySource(emitter.ring, …)`. This is the one place both the ring and `w.convReg` are in scope, and it runs at most once.
- `cmd/pyry/interactive_turn_v2.go` → `newInteractiveTurnEmitterV2`: owns the ring and has 86 call sites, so the constructor signature stays as it is.
- `docs/knowledge/features/conversations-registry.md` § Save concurrency: `Save` is called from several goroutines, and lock order is saveMu → mu. The observer must not add a lock edge.

## Context

`eventring.Ring` never forgets a conversation. Each removed conversation can keep up to 1024 events, which can be several MB of coalesced deltas, until the daemon restarts. Removal is always `Registry.Delete`, whether it comes from the `delete_conversation` handler or from the hourly idle sweep.

## Design

Three pieces, each small:

1. **`(*eventring.Ring).Drop(convID string)`** deletes `r.convs[convID]` under `r.mu`. `nextID` is untouched, so every later `Append`, including one for the dropped id, gets a higher id than any issued before. Dropping an unknown id is a no-op (a plain map `delete`). After a drop the conversation is indistinguishable from an unknown one: `NewestID` → 0, `After(id, 0)` → `(nil, false)`, and `After(id, n>0)` → `(nil, true)`, which is the documented gap for a cursor naming a removed conversation. `eventring` stays a leaf and gains no imports.

2. **A removal observer on `Registry`**: `SetOnDelete(fn func(id ConversationID))`, one slot, and nil clears it. `Delete` reads the slot under `r.mu` and calls it **after releasing `r.mu`**, only on a hit. It is called outside the lock so the observer can never create an r.mu → other-lock edge or re-enter the registry and deadlock. `Registry.Delete` is the single funnel for both removal paths, so one seam covers the handler and the sweep. Neither call site changes, and the handler's `ConversationDeleter` interface and `Sweep`'s signature stay as they are.

3. **Wiring in `cmd/pyry/relay.go`**: a helper `dropRingOnConversationDelete(reg *conversations.Registry, ring *eventring.Ring)` installs `func(id) { ring.Drop(string(id)) }` and does nothing when `reg` is nil. It is called in the stream-mode branch right after `SetReplaySource`, where the emitter's ring is created. It is a named helper so a `cmd/pyry` test can prove the production wiring without building the relay leg.

Data flow: `delete_conversation` handler or `Sweep` → `Registry.Delete` (hit) → observer → `Ring.Drop(convID)`.

## Concurrency model

No new goroutines. `Drop` takes `ring.mu`, and the observer runs on whichever goroutine called `Delete` (the v2 manager's handler goroutine or the sweep goroutine), after `r.mu` is released. There is a benign race: an in-flight emitter `Append` for the just-deleted conversation can land after `Drop` and recreate a small entry. That entry is bounded like any other and only arises if a turn is streaming at the moment of deletion. It is not worth a tombstone, because a tombstone set would itself grow without bound.

## Error handling

No new failure modes. `Drop` cannot fail, and the observer returns nothing. A registry `Save` failure after `Delete` is unchanged: the row is gone in memory, so dropping its ring entry is still correct.

## Testing strategy

- `internal/eventring/ring_test.go` — `TestDrop`: two conversations with events. Drop one, then check `NewestID` is 0, `After(id,0)` is `(nil,false)`, `After(id,oldID)` is a gap, and the other conversation's `After(0)` ids and `NewestID` are unchanged. A re-`Append` to the dropped id gets an id above every earlier id. Dropping an unknown id is a no-op that leaves the others unchanged. Concurrency is covered by extending nothing: `Drop` uses the same lock as the others, and `-race` runs the suite.
- `internal/conversations/registry_test.go` — `TestRegistry_OnDelete`: fires once with the id on a hit, does not fire on a miss, a nil observer is safe, and the observer can call back into the registry (e.g. `Get`) without deadlocking, which proves it runs outside `r.mu`.
- `internal/conversations/sweep_test.go` — `TestSweep_FiresOnDeleteForRemovedOnly`: the observer sees exactly the swept ids, and not the fresh or promoted ones it keeps (AC3 at the seam).
- `internal/relay/handlers/delete_conversation_test.go` — `TestDeleteConversation_OnDeleteObserver`: with a real registry and an observer recording ids, success records the id, and not_found and malformed record nothing (AC2 at the seam).
- `cmd/pyry/ring_removal_test.go` — `TestDropRingOnConversationDelete`: registry + ring wired by the helper. Deleting one conversation (including through `conversations.Sweep`) empties that ring entry and keeps the others, and a nil registry is a no-op.

## Documentation handoff

The ticket has no Documentation handoff section. Pending for the documentation stage: `docs/knowledge/features/eventring-package.md` should gain `Drop` in § Exported surface / § Ownership & wiring, and `docs/knowledge/features/conversations-registry.md` § CRUD should gain `SetOnDelete`.

## Open questions

- Is `w.convReg` ever nil in stream mode? The helper is nil-safe either way, so it doesn't matter.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The only untrusted input is the phone's `conversation_id` in `DeleteConversation`. It reaches the ring only through `Registry.Delete`, which fires the observer only on an exact-match hit. A client can therefore drop the replay state of a conversation it is already allowed to hard-delete, and of nothing else. Malformed and not_found requests never reach the observer (tested at the handler seam). The observer receives the stored row's `ConversationID`, so no raw wire bytes reach the ring as a key.
- [Trust boundaries] No findings on key agreement. The ring is keyed by `activeConversation.CurrentConversation()`, the same string as the registry's `ConversationID`. If they ever diverged, `Drop` would miss, leaving the pre-ticket leak and no wrong-conversation removal. `Drop` deletes one exact map key and can never touch another conversation (tested in `TestDrop`).
- [Tokens / credentials] Not applicable. No tokens, keys or secrets are created, stored or compared.
- [File operations] Not applicable. `Drop` and the observer are in-memory only. The registry `Save` after a delete is unchanged.
- [Subprocess] Not applicable. Nothing is executed.
- [Crypto] Not applicable. No crypto is used. The ring-wide id counter is deliberately not reset, so a phone's `last_event_id` / `replayThrough` watermark can never alias a reissued id (the #2022 property). `TestDrop` asserts that a re-`Append` after a drop gets a strictly higher id.
- [Network & I/O] No findings. A reconnecting phone whose cursor names a removed conversation gets `After` → gap=true, meaning a resync. That is the documented outcome and replays nothing. No new wire surface, and no new resource a peer can grow: every drop requires a real delete of an existing row.
- [Error messages / logs] Not applicable. No new log lines or error strings. The handler's existing no-echo rules are untouched.
- [Concurrency] No findings. The observer is called after `Registry.Delete` releases `r.mu`, so no new lock-order edge is created (saveMu → mu stays the only ordering). An observer that calls back into the registry cannot deadlock, and a test proves it. `Drop` takes only `ring.mu`, and concurrent drops from the sweep goroutine and the handler goroutine are serialized by it. No goroutines are added. There is one accepted benign race: an emitter `Append` in flight for the just-deleted conversation can recreate a small entry. That entry is bounded by `MaxEventsPerConversation` like any other and is visible only under that deleted id. A tombstone set was rejected because it would itself grow without bound.
- [Threat model] No findings. `docs/protocol-mobile.md` § Security model: the change narrows retained data, which shortens how long a deleted conversation's content stays in daemon memory. Out of scope: the durable history log (#2114) is a separate store, and the ticket excludes it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-23

Note: this section was appended in a second commit, after the plan commit. The label check was run after the first commit rather than before it. The design is unchanged by the review.
