# Spec: e2e `delete_conversation` over the v2 wire (#975)

**Ticket:** [#975](https://github.com/pyrycode/pyrycode/issues/975) — `test(e2e): delete_conversation over the v2 wire`
**Size:** S (confirmed — 1 new test file, 0 production files, 0 new exported types)
**Security-sensitive:** No (no label; test-only, exercises an already-shipped+reviewed handler, asserts observable behaviour — reply Type/Code + on-disk state — not an isolation property). Security-review step skipped per the label gate.
**Split from:** #960. Sibling of #974 (rename, merged) and #976 (archive/unarchive round-trip).

---

## Files to read first

The whole job is: copy the freshly-merged #974 rename test, apply the delete divergences in the table below. Read in this order.

- `internal/e2e/relay_v2_rename_test.go` (full, 308 lines) — **the primary template.** Copy its two-subtest shape verbatim: `pair → seed → spawn → handshake → seal → decrypt reply → read registry back`. Both the happy-path decode and the not-found error decode (`TypeError` + `InReplyTo` + `ErrorPayload.Code == CodeConversationNotFound`, lines 267–281) are already spelled out inline here — no need to hunt `relay_v2_daemon_test.go`.
- `internal/e2e/relay_v2_promote_test.go` (full, 168 lines) — the original single-verb precedent (#949). Read only if the rename test leaves a harness question; otherwise skip.
- `internal/relay/handlers/delete_conversation.go` (full, 105 lines) — the handler under test. Confirms: `Delete(id) bool` → hit ⇒ eager `Save` then reply `conversation_deleted{id}`; miss ⇒ `conversation.not_found` error with **no Save** (registry untouched). The reply carries only the id — no name/cwd/last_used_at (the record is gone).
- `internal/protocol/conversations_write.go:59–78` — `DeleteConversationPayload{ConversationID string}` (id only — **no `Name`, no `Cwd`**) and `ConversationDeletedPayload{ID string}` (json tag `id`).
- `internal/protocol/codes.go:22,63–75` — `CodeConversationNotFound = "conversation.not_found"`, `TypeDeleteConversation = "delete_conversation"`, `TypeConversationDeleted = "conversation_deleted"`.
- `cmd/pyry/relay.go:409` — the `protocol.TypeDeleteConversation: handlers.DeleteConversation(...)` registration. The RED-on-main guard: strip this line and the happy-path `want conversation_deleted` assertion fails (verb falls through to `protocol.unsupported`).
- `internal/conversations/registry.go:254` — `Delete(id) bool` semantics (returns false on a miss; hard-removes the row on a hit — no flag).

Harness primitives already exist; **do not extract a shared session helper** (Tech Notes): `RunBareIn`, `StartInWithEnv`, `driveHandshakeToOpenDaemon`, `decryptInnerEnvelope`, `readInnerFrame`, `sendNoiseMsg`, `shortHome`, `decodePairPayload`, `readPersistedServerID`, `waitBinaryHello`, `relayTestLogger`, `mustJSON`, plus `fakephone.Dial`, `fakerelay.New`.

---

## Context

A 2026-07-15 full-repo review found eleven client-sendable v2 verbs whose only coverage is package-level unit tests — no e2e at any tier. This is the #949 `promote_conversation` failure shape (surface exists, nothing exercises it end-to-end). `delete_conversation` is the hard-delete conversation-lifecycle verb in that gap. It is distinct from rename/promote in one wire-visible way: its success reply is `conversation_deleted` (carrying only the deleted id), not `conversation_updated`.

This ticket proves the verb at the daemon boundary: a paired phone completes the Noise_IK handshake against a real spawned daemon, round-trips `delete_conversation` over the encrypted channel, and the assertions land on the decrypted reply and the on-disk `conversations.json`.

---

## Design

**One new file:** `internal/e2e/relay_v2_delete_test.go`, build tag `e2e`, package `e2e`. Structure mirrors `relay_v2_rename_test.go` exactly:

```
func TestRelayV2_Delete(t *testing.T)                      // top-level, two t.Run subtests
  ├─ testV2DaemonDeleteRoundTrip(t)                        // AC #1, #2
  └─ testV2DaemonDeleteNotFound(t)                         // AC #3
```

**Two independent subtests, two daemon spawns** (not one shared daemon) — same rationale as #974: keeps the not-found subtest's seed pristine so "conversations.json unchanged" reads a never-mutated file. The ~40-line pair→spawn→handshake preamble is duplicated across the two subtests; that is the accepted e2e idiom here.

### Divergences from the rename template

| Aspect | Rename (`relay_v2_rename_test.go`) | Delete (this ticket) |
|---|---|---|
| Request type | `TypeRenameConversation` | `TypeDeleteConversation` |
| Request payload | `RenameConversationPayload{ConversationID, Name}` | `DeleteConversationPayload{ConversationID}` — **id only; no Name, no Cwd** |
| Success reply type | `TypeConversationUpdated` | `TypeConversationDeleted` |
| Success reply payload | `ConversationUpdatedPayload` (full projection: Name/Cwd/IsPromoted/IsArchived/LastUsedAt) | `ConversationDeletedPayload{ID}` — **decode this; assert `.ID == convID`. Nothing else to project** (the record is gone) |
| Happy-path on-disk assertion | row mutated in place (new name; IsPromoted/Cwd/LastUsedAt preserved) | **row is GONE** (hard delete, not flagged) — see two-row seed below |
| Not-found path | error `conversation.not_found`, seeded row untouched | **identical** — error `conversation.not_found`, seeded row untouched |

### Happy path — seed TWO rows (target + survivor)

The rename happy path seeds one row and asserts a specific field value; that can't pass on an empty file. Delete's happy assertion is "row gone" — asserting `len == 0` on a single-row seed would **pass vacuously** against a `Save`-serialization bug that truncates the file to an empty array. Seed a second, non-targeted "survivor" row so the assertion proves *selective* hard-delete and that the on-disk file is intact:

- Seed `conversations.json` with two rows: `targetConvID` (the one to delete) and `survivorConvID` (a bystander with its own name).
- After the round-trip, read the registry back and assert **`len(onDisk.Conversations) == 1`**, the remaining row's `id == survivorConvID` with its name intact, and `targetConvID` is absent. This is the "removed from the registry, not flagged" evidence (AC #2): a soft-delete-that-flags bug leaves 2 rows; a wipe-everything / truncate bug leaves 0; only correct selective hard-delete leaves exactly the survivor.

This is the same non-vacuous-assertion discipline #974 used with `is_promoted:true` — make the happy assertion prove the specific behaviour, not a superset.

### Reply-decode contract (happy path)

```
reply := decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)
// assert reply.Type == protocol.TypeConversationDeleted
// assert reply.InReplyTo != nil && *reply.InReplyTo == reqID
// json.Unmarshal(reply.Payload, &protocol.ConversationDeletedPayload) → assert .ID == targetConvID
```

No `time.Time` handling — delete carries no `LastUsedAt`, so the `.Equal` fragility that rename dealt with does not arise here.

### Not-found path

Byte-for-byte the rename not-found subtest with the type/payload swapped:

- Seed one row with `seededConvID`; send `DeleteConversationPayload{ConversationID: absentConvID}` (a different, absent id).
- Assert reply `Type == protocol.TypeError`, `InReplyTo` correlates to `reqID`, decoded `ErrorPayload.Code == protocol.CodeConversationNotFound`.
- Read the registry back: assert `len == 1` and the seeded row (`id`, `name`) is unchanged — the handler replies before any `Save`, so the file must be pristine (AC #3).

### Request-envelope skeleton (both subtests)

Identical to the rename request marshal; only Type and Payload change. Use distinct `reqID` constants per subtest (rename used 51/52 — pick fresh values e.g. 61/62) and distinct UUIDs so a copy-paste bleed between subtests is obvious.

```
protocol.Envelope{ID: reqID, Type: protocol.TypeDeleteConversation, TS: time.Now().UTC(),
    Payload: mustJSON(t, protocol.DeleteConversationPayload{ConversationID: <id>})}
→ initSend.Encrypt → sendNoiseMsg
```

---

## Concurrency model

None introduced. The test drives the existing single spawned-daemon + single fakephone flow synchronously (send frame → block on `readInnerFrame` with a 3s deadline → assert). Cleanup via `t.Cleanup` (fakerelay `Close`, harness `Stop`, phone `Close`), exactly as the templates. `-race` clean because the test owns no shared mutable state across goroutines.

---

## Error handling (test failure modes)

- **Handshake / spawn flake** — inherited from the harness; `driveHandshakeToOpenDaemon` and `waitBinaryHello` fatal on failure with diagnostics. Nothing new to handle.
- **Reply timeout** — `readInnerFrame(t, phone, 3*time.Second)` fatals if the daemon never replies (e.g. registration stripped → verb unsupported → the daemon still replies, but with `TypeError/protocol.unsupported`, so the Type assertion catches it rather than a hang). 3s matches the templates.
- **On-disk decode** — use a minimal anonymous struct (`id`, `name` fields only) as the rename test does; `json.Unmarshal` fatal on malformed. Do not decode the full registry type.

---

## Testing strategy

This ticket *is* the test. Verification:

- `make e2e` green with `-race` (AC #5).
- **RED-on-main check** (state in the top-of-file doc comment, as rename did): removing the `cmd/pyry/relay.go:409` registration makes the happy-path `want conversation_deleted` assertion fail — the verb falls through to `protocol.unsupported`. The test is a live guard on that registration.
- **Non-vacuity:** the two-row happy seed guarantees `len == 1 && survivor intact` cannot pass on a truncated/empty file or a soft-delete-that-flags bug.

---

## Open questions

None. All primitives exist and are exercised by the merged rename/promote tests; the type shapes are confirmed above. The only judgement call — two-row vs one-row happy seed — is resolved in favour of two rows (non-vacuous "row gone" assertion) in the Design section.

---

## Acceptance criteria (unchanged from ticket)

1. Seed a conversation row, send `delete_conversation` over the v2 wire, assert the decrypted reply is `conversation_deleted` with `in_reply_to` correlating to the request id and payload `id` equal to the deleted conversation id.
2. The happy-path test reads `conversations.json` back and asserts the row is gone (hard delete — removed, not flagged).
3. Error-path test sends `delete_conversation` for an absent id, asserts the reply is an `error` envelope carrying `conversation.not_found`, and `conversations.json` is unchanged (no row removed).
4. Assertions are on observable behaviour only — decrypted wire frames and files on disk; no fake internals inspected.
5. `make e2e` green with `-race`.
