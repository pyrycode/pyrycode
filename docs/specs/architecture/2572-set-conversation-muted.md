# #2572 — `set_conversation_muted` sets and clears a conversation's muted flag

## Files read

- `internal/relay/handlers/archive_conversation.go` → `ArchiveConversation`, `ConversationArchiver` — the template: single-field mutator, `Get` read-back, eager best-effort `Save`, `conversation_updated` reply, two static reject branches, malformed branch logs `conn_id` only.
- `internal/relay/handlers/archive_conversation_test.go` → `newArchiveConvReg`, `newArchiveConvConn`, `assertArchiveConvEnvelopeShape` — test scaffolding the new test file mirrors (own-prefixed copies, not shared).
- `internal/relay/handlers/autoname.go` → `ConversationAnnouncer` — the fan-out func type (no `excludeConnID`, returns nothing, nil means no fan-out). Reused as the new handler's announce parameter.
- `internal/relay/handlers/list_conversations.go` → `workspaceLabelFor`; `internal/relay/handlers/register_push_token.go` → `replyError` — shared helpers.
- `internal/conversations/registry.go` → `Registry.SetArchived` — shape `Registry.SetMuted` copies.
- `internal/conversations/conversation.go` → `Conversation.IsMuted` (#2571) — the durable field written here.
- `internal/protocol/codes.go` → `TypeArchiveConversation`; `internal/protocol/envelope.go` → `inboundAppTypeSet`; `internal/protocol/conversations_write.go` → `ArchiveConversationPayload`, `ConversationUpdatedPayload`.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `TestInboundAppTypeSet_CoversAllExportedTypeConstants` (length pinned at 26), `TestTypeConstants_V1V2Partition` — the three type lists.
- `cmd/pyry/relay.go` → `startRelayV2`'s `Handlers` map, the `TypeSendMessage` entry's nil-guarded closure over the named return `announceConversation`.
- `cmd/pyry/conversation_update_v2.go` → `conversationUpdateEmitterV2.announce` — the existing fan-out (interactive gate, own envelope counter, no `in_reply_to`).
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, the `TypeConversationUpdated` "reply+push" entry.

No in-flight feature branch touches these files.

## Context

#2571 made `IsMuted` durable and projected it as `is_muted` on list rows and every `conversation_updated`. Nothing writes it. This ticket adds the write verb. Mute lives on the host (decision 2026-09-24), so other clients must hear a change without re-listing — unlike archive, whose reply is not fanned out. The fan-out reuses #2156's `conversationUpdateEmitterV2` via the same nil-guarded closure `send_message` auto-naming already takes.

## Design

### Protocol (`internal/protocol`)

- `codes.go`: `TypeSetConversationMuted = "set_conversation_muted"` beside `TypeUnarchiveConversation`.
- `envelope.go`: add to `inboundAppTypeSet`.
- `conversations_write.go`: new payload

  ```go
  type SetConversationMutedPayload struct {
      ConversationID string `json:"conversation_id"`
      Muted          *bool  `json:"muted"`
  }
  ```

  `Muted` is a pointer so an absent key (and JSON `null`) decodes to nil and is rejected as malformed rather than read as `false` (AC 3). No omitempty — same round-trip rationale as `SetSystemPromptPayload`. Not a reuse of `ArchiveConversationPayload` (semantic-coupling rationale the siblings document).

### Registry (`internal/conversations`)

`func (r *Registry) SetMuted(id ConversationID, muted bool) bool` — identical shape to `SetArchived`: under `r.mu`, scan, set exactly `IsMuted`, return hit/miss; no `Save`.

### Handler (`internal/relay/handlers/set_conversation_muted.go`)

```go
type ConversationMuter interface {
    SetMuted(id conversations.ConversationID, muted bool) bool
    Get(id conversations.ConversationID) (conversations.Conversation, bool)
    Save(path string) error
    WorkspaceLabel(cwd string) (string, bool)
}
func SetConversationMuted(reg ConversationMuter, registryPath string, announce ConversationAnnouncer, logger *slog.Logger) dispatch.Handler
```

Flow:

1. `json.Unmarshal` into `SetConversationMutedPayload`. Decode error **or** `p.Muted == nil` → `protocol.malformed`, static message `"malformed set_conversation_muted payload"`, non-retryable; log `conn_id` only.
2. `reg.SetMuted(id, *p.Muted)`; miss → `conversation.not_found`, static `"conversation not found"` (reuse `msgArchiveConversationNotFound`? No — own const `msgMuteConversationNotFound` with the same text, so the two verbs' messages are not coupled). Log `conn_id` + `conversation_id` as a structured field (archive precedent: the id is a decoded string, never reaches the wire).
3. `reg.Get(id)`; a concurrent delete between the two → same not_found branch (archive precedent).
4. `reg.Save(registryPath)`; failure logged, non-fatal.
5. Build `ConversationUpdatedPayload` from the read-back `cv` (every field, `WorkspaceLabel` via `workspaceLabelFor`).
6. Reply via `c.Reply(ctx, env, TypeConversationUpdated, json)` (correlated), then call `announce(payload)` if non-nil (uncorrelated push to every interactive conn, requester included). The announce runs even when the reply errors (conn torn down) — the other clients must still hear; the reply error is returned after.

Idempotent: setting the current value hits, saves, replies and pushes the unchanged record — "changes nothing" in stored state.

No session is touched: the handler holds no pool/runner seam.

### Wiring (`cmd/pyry/relay.go`)

Hoist the existing `send_message` closure into a local `announceConversationHook` (declared just before `NewV2SessionManager`, beside `announceWorkspace`), pass it to both `handlers.SendMessage` and `handlers.SetConversationMuted`. One closure, no second fan-out loop. It reads the named return `announceConversation` at call time, so the post-construction assignment still reaches it.

### Guard (`cmd/pyry/relay_guard_test.go`)

`"TypeSetConversationMuted": "map-dispatched"` in `inboundTypes`; extend the `TypeConversationUpdated` comment's producer lists (comment only).

## Concurrency model

No goroutines. The handler runs on the dispatch goroutine. `SetMuted` is atomic under `r.mu`; `Get` is a separate lock, the only observable interleaving is a concurrent delete (handled as not_found). The announce is synchronous and bounded: `ActiveConns` snapshot + non-blocking `Push` (existing emitter contract).

## Error handling

| Branch | Code | Message | Saved | Pushed |
|---|---|---|---|---|
| decode error | `protocol.malformed` | `malformed set_conversation_muted payload` | no | no |
| `muted` absent / null | `protocol.malformed` | same | no | no |
| unknown id (incl. empty) | `conversation.not_found` | `conversation not found` | no | no |
| row deleted between SetMuted and Get | `conversation.not_found` | same | no | no |
| Save fails | — (success reply) | — | best-effort | yes |

All rejects `retryable: false`.

## Testing strategy

`internal/conversations/registry_test.go`:
- `SetMuted` hit sets then clears, every other field untouched; miss returns false and mutates nothing; round-trip through `Save`/`Load` keeps `IsMuted`.

`internal/relay/handlers/set_conversation_muted_test.go` (a recording announcer collects pushed payloads):
- mute: reply correlated (`in_reply_to`), `is_muted=true`, other fields unchanged; stored row muted; fresh `Load` from disk muted (survives restart); announcer called once with the same record.
- unmute on a muted row → `is_muted=false`, persisted, announced.
- idempotent: muting an already-muted row succeeds, row unchanged.
- malformed JSON, missing `muted` key, `muted: null`, unknown id: each → error reply with the exact static code/message, `retryable=false`; registry file not written (no file on disk / stored row unchanged); announcer not called; a sentinel payload string appears in neither reply bytes nor log buffer.
- nil announcer is tolerated.

`internal/protocol/compat_test.go`: add the constant to all three lists; bump the pinned length 26 → 27.

Verification: `go test -race ./internal/conversations/... ./internal/relay/handlers/... ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

- Reply-then-push vs push-then-reply ordering: chose reply first so the requester's correlated answer is the first frame it sees for its own request; clients fold by id so either order converges.

## Documentation handoff (pending — documentation stage)

`docs/protocol-mobile.md`:
- Add a v2 type-table row for `set_conversation_muted`: direction client → binary, payload `{"conversation_id": string, "muted": bool}`, `muted` required (absent/null is `protocol.malformed`), reply `conversation_updated` correlated by `in_reply_to`, same record pushed uncorrelated to every interactive conn (requester included).
- In the `conversation_updated` row, add `set_conversation_muted` to the verbs it answers and to its unsolicited push producers.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the one boundary is `json.Unmarshal` in `SetConversationMuted`. `conversation_id` is used only as an exact-match registry key inside `Registry.SetMuted` / `Registry.Get` (byte comparison; no path, argv or format construction). `muted` is a bool: after the nil check it can only be `true`/`false`. A client can only name an id that already exists; the miss branch is static.
- [Tokens] No findings — the verb carries no credential; it rides the already-authenticated Noise session and paired-device gate like every map-dispatched verb.
- [File operations] No findings — the only write is `Registry.Save` to the fixed `resolveConversationsRegistryPath` output; no payload byte reaches a path. Save's atomicity/mode are the existing registry contract, unchanged.
- [Subprocess] No findings — no exec; the handler holds no session seam, so muting cannot restart, spawn or interrupt anything (structural, not conventional).
- [Crypto] No findings — none used.
- [Network & I/O] No findings — frame size is bounded by the existing relay frame cap. Fan-out amplification: one request produces one push per interactive conn — the same bound `send_message` auto-naming already has; the pushed record is the registry read-back, never request bytes.
- [Error messages / logs] No findings — both reject replies are fixed constants. Malformed branch logs `conn_id` only (decode errors can quote input bytes; a partial decode can leave raw bytes in `p.ConversationID`). not_found logs the decoded id as a structured slog field (archive precedent; never on the wire). Tests assert a sentinel payload string reaches neither the reply nor the log on every reject branch.
- [Disclosure via fan-out] No findings — the pushed `ConversationUpdatedPayload` carries only fields every paired client already reads from `list_conversations` (the #2151 rationale excludes the system prompt by type). Pushing to the requester too is the documented `ConversationAnnouncer` contract.
- [Concurrency] No findings — `SetMuted` scans and mutates under one lock (no find-then-mutate window); the SetMuted/Get gap's only effect (concurrent delete) is handled as not_found.
- [Threat model] No findings — no new trust surface beyond one more boolean write verb inside the existing paired-device model.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24

## Revisions

- **2026-09-24, Phase B — not_found no longer logs `conversation_id`.** The Design (step 2) and the security review's log finding followed archive's precedent of logging the unknown id as a structured field. AC 3 says no byte of the payload reaches the log on any reject, and an unknown id is client-supplied bytes. Both not_found branches now log `conn_id` only, like the malformed branch; only the success branch (and the Save-failure log, reached only for a proven-real id) logs `conversation_id`. `TestSetConversationMuted_Rejects` asserts a payload marker is absent from the log on the unknown-id case.
- **Open question resolved:** reply first, then push; the push runs even if the reply errors, and the reply error is returned after it.
