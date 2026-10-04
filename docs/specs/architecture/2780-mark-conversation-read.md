# #2780 — mark_conversation_read: advance the host read mark and fan it out

## Files read

- `internal/relay/handlers/set_conversation_muted.go` → `SetConversationMuted`: the analogue (decode, registry write, Save, read-back record, reply-then-announce). Its best-effort Save is NOT copied.
- `internal/relay/handlers/list_conversations.go` → `historyLatestReader`, the typed-nil `*history.Store` guard in `ListConversationsWithAgents`: the history seam reused here.
- `internal/history/log.go` → `Store.LatestEntryID`: newest durable entry id L, 0 for a missing/empty log; validates the id shape and directory containment itself.
- `internal/conversations/registry.go` → `Registry.Save`, `saveMu`/`mu` lock order (saveMu → mu), `switchSession`: the precedent for "mutate, persist the snapshot while both locks are held, revert on failure".
- `internal/conversations/conversation.go` → `Conversation.ReadUpTo` (#2779).
- `internal/relay/handlers/autoname.go` → `ConversationAnnouncer`; `cmd/pyry/relay.go` → `announceConversationHook` in `startRelayV2`: the existing fan-out, which already applies the interactive/multi-agent filtering.
- `internal/relay/handlers/register_push_token.go` → `replyError`.
- `internal/protocol/codes.go` (`TypeSetConversationMuted`, the error-code groups), `envelope.go` (`inboundAppTypeSet`), `compat_test.go` (three type lists, the type count, `TestErrorCode_Constants_MatchSpec`), `conversations_write.go` (`SetConversationMutedPayload`).
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`.

No other open feature branch touches these files.

## Context

#2779 stores `Conversation.ReadUpTo` and reports it with `latest_entry_id`. This slice adds the client write: one host-local operator read mark per conversation, raised by any client and pushed to the others so dots, pills and notifications agree. Unlike mute, a request that changes nothing must not push, and a failed save must not be acknowledged.

## Design

### Protocol

- `TypeMarkConversationRead = "mark_conversation_read"`, an `inboundAppTypeSet` member (phone → binary, `dispatch.Route`).
- `MarkConversationReadPayload{ConversationID string "conversation_id"; UpTo *uint64 "up_to"}`. The pointer makes a missing or null `up_to` detectable. `encoding/json` decodes a uint64 with `strconv.ParseUint(…, 10, 64)`, so negatives, fractions, exponents, strings, booleans and values over 2^64-1 all fail the decode. Every decode failure and a nil `UpTo` is `protocol.malformed`.
- New retryable code `CodeReadMarkUnavailable = "read_mark.unavailable"`: the advance could not be saved, nothing changed. No existing code fits (the refiner confirmed there is no generic retryable storage code).

### Registry mutator

`(*Registry).AdvanceReadUpTo(id, upTo, latest uint64, path string) (Conversation, bool, error)`:

- Takes `saveMu` then `mu` (the documented order), finds the row. A miss returns `ErrConversationNotFound`.
- `next = max(held, min(upTo, latest))`. When `next == held` it returns the row and `advanced=false` without saving.
- Otherwise sets `ReadUpTo = next` and writes `snapshotLocked()` with `writeRegistrySnapshot` while both locks are still held. If the write fails it restores the held value and returns the error, so no reader ever sees an advance that is not on disk, and a retry once saving works again advances and persists for real.
- Returns the stored row copy and `advanced=true`.

The unexported `advanceReadUpTo(…, persist)` takes the persist function, as `switchSession` does, so the revert can be unit-tested.

### Handler

`handlers.MarkConversationRead(reg ConversationReadMarker, hist historyLatestReader, registryPath string, announce ConversationAnnouncer, logger) dispatch.Handler`. `ConversationReadMarker` is `Get`, `AdvanceReadUpTo` and `WorkspaceLabel`.

1. Decode. Error or nil `up_to` → `protocol.malformed` (non-retryable).
2. `reg.Get(id)`. A miss, including an empty id → `conversation.not_found` (non-retryable). The registry is resolved before history is touched.
3. A nil (or typed-nil) history store, or a `LatestEntryID` error → `history.unavailable` (retryable).
4. `AdvanceReadUpTo`. `ErrConversationNotFound` (a concurrent delete) → `conversation.not_found`. Any other error → `read_mark.unavailable` (retryable).
5. Build `ConversationUpdatedPayload` from the stored row, as mute does. Reply with it, correlated. If `advanced` and announce is non-nil, announce it (no `in_reply_to`), even when the reply failed.

All refusals use static messages. Logs carry the event and conn id; the conversation id is logged only after the registry proved it.

### Wiring

In `startRelayV2`'s handlers map: `handlers.MarkConversationRead(w.convReg, w.hist, resolveConversationsRegistryPath(w.instanceName), announceConversationHook, logger)`. It takes no pool or session seam, so it cannot spawn, restart or interrupt anything.

## Concurrency model

No goroutines. Concurrent requests serialize on `saveMu` → `mu` inside `AdvanceReadUpTo`, so the max-with-held is atomic with its save and neither the held nor the persisted mark can regress. Other `Save` callers also take `saveMu` first, so an older snapshot cannot be renamed over the advance. L is read before the lock. A stale L only clamps lower, never past the real newest entry. Holding `mu` across the fsync briefly blocks registry readers, the same trade `switchSession` makes.

## Error handling

| Condition | Reply | Retryable | Side effects |
|---|---|---|---|
| undecodable / null / bad `up_to` | `protocol.malformed` | no | none |
| unknown or empty id | `conversation.not_found` | no | none |
| history unreadable or store nil | `history.unavailable` | yes | none |
| registry save failed | `read_mark.unavailable` | yes | in-memory mark reverted; no push |
| success, no change | `conversation_updated` reply | — | no save, no push |
| success, advanced | `conversation_updated` reply + push | — | saved |

## Testing strategy

- Registry (`internal/conversations`): table over (held, upTo, latest) for the clamp/monotonic formula incl. 0, equal, lower, over-large and max uint64. Advance survives `Load`. Persist failure reverts and a retry advances. Miss returns `ErrConversationNotFound`. A concurrent-advance test under `-race` ends at the max and on disk at the max.
- Handler (`internal/relay/handlers`), with a fake history and a recording announcer:
  - Advance: correlated reply with stored `read_up_to`, one push without `in_reply_to`, and the value on disk.
  - No-op (lower/equal, empty conversation at 0): the reply only, no push, no file written.
  - Clamp: over-large `up_to` stored as L.
  - Malformed table: missing, null, -1, 1.5, 1e2, `"5"`, true, 2^64. Not-found: unknown and empty id. All reply with static messages, with no mutation, file or push, and no payload marker in logs.
  - History error: retryable `history.unavailable`, no change. Not-found is checked before history (a failing history is not called for an unknown id).
  - Save failure: the registry path's parent is a regular file, so the reply is retryable `read_mark.unavailable` with no push and the mark unchanged. After removing the blocker, the same request persists and pushes.
  - Reply failure (a closed conn) still announces.
- Protocol: compat lists and count, error-code map. `relay_guard_test` `inboundTypes`.

## Open questions

- Should a no-op still re-save? No. Nothing changed, and the retry-after-failure case is covered by the revert.

## Documentation handoff

Pending for the documentation stage, in `docs/protocol-mobile.md`:

- Add the v2 type-table row and a request section for `mark_conversation_read`: required `conversation_id` and `up_to` (JSON integer, 0..2^64-1), the semantics `new = max(R, min(U, L))`, eager persistence, and the error outcomes `protocol.malformed`, `conversation.not_found`, `history.unavailable` (retryable) and the new retryable `read_mark.unavailable` for a failed save. Also the correlated `conversation_updated` reply on every success and the push without `in_reply_to` only on an actual advance.
- Add `read_mark.unavailable` to the error-code table.
- Update the `conversation_updated` row and section to name this reply and push producer, and update the producer counts it states ("three producers of its own", "all **eight** producers").
- Remove the "ships the read half only" sentences from the `conversations` row, the `conversation_updated` row and the history-log paragraph on the shared id space. Leave #2779's Changelog entry as written.
- Reaffirm `latest_entry_id > read_up_to` as the unread test, and one host-local operator mark per conversation.
- Add a Changelog entry.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The only untrusted input is the frame payload, decoded once in `MarkConversationRead` into `MarkConversationReadPayload`. `up_to` is a typed `*uint64`, so the range check is `encoding/json`'s `ParseUint`. `conversation_id` is first used as an exact-match key in `Registry.Get`. Only an id the registry proved reaches `history.Store.LatestEntryID`, which also re-checks `ValidID` and directory containment. An unknown id never names a history path.
- [Tokens] No findings. The verb carries no credentials; the session is already authenticated by the existing Noise transport.
- [File operations] No findings. The only write is the registry file through `writeRegistrySnapshot`: temp file at `0600`, parent `0700`, fsync, then rename. A crash mid-write leaves the old or the new mark, never a torn file. No path is built from request bytes.
- [Subprocesses] No findings. The handler holds no pool, runner or session seam, so it cannot spawn, restart or interrupt a child.
- [Cryptography] No findings. None is used here.
- [Network and I/O] No findings. Frame size is capped by the existing dispatch/relay envelope bound. A no-op does not save, so a client flooding the verb gets one cheap cursor read per frame (`LatestEntryID` opens no segment once the cursor is loaded). It cannot force an fsync, because the advances that do save are bounded by the history log's growth.
- [Errors, logs, telemetry] SHOULD FIX: every refusal must use a static message, and no refusal branch may log the decode error or the raw `conversation_id`, since encoding/json errors can quote input bytes. Tests assert that an injected payload marker is absent from replies and logs. The history and save errors may name local paths, so they go to the daemon log only, never onto the wire.
- [Concurrency] No findings. Lock order is `saveMu` → `mu`, the same as `Save` and `switchSession`. The max-with-held compare, the save and the revert happen while both are held, so concurrent requests cannot regress the mark and no reader sees an unsaved advance. No goroutines are started.
- [Threat model] By design (decided 2026-10-04): any paired, authenticated client can raise the host operator's mark for every device. That is the feature, not an escalation. A client cannot mark entries that do not exist yet, because the clamp is to L. Per-device read state is out of scope and has no ticket, because the product decision rejects it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
