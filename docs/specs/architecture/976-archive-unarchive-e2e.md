# Spec — #976 e2e: archive/unarchive_conversation round-trip over the v2 wire

**Size:** S (test-only). One new file: `internal/e2e/relay_v2_archive_test.go`. Zero production
files touched — the handler and both registrations already ship and are wired.

**Not security-sensitive.** No `security-sensitive` label; this ticket adds no new design surface.
The handler `internal/relay/handlers/archive_conversation.go` already carries its own security
reasoning (untrusted `conversation_id` used only as an exact-match registry key, static reject
strings, no payload bytes on the wire). This ticket only *observes* that behaviour end-to-end.

## Files to read first

- `internal/e2e/relay_v2_rename_test.go` (whole file, ~309 lines) — **the template.** Copy its two-subtest
  structure verbatim: pair → seed `conversations.json` → spawn v2 daemon → `driveHandshakeToOpenDaemon`
  → seal request → decrypt reply → assert reply envelope → read registry back off disk. The
  `conversation_updated` reply decode + `in_reply_to` correlation + `ConversationUpdatedPayload.IsArchived`
  inspection at lines 123–154 is the exact reply shape this ticket toggles. The not-found subtest at
  186–308 is the exact error-path shape (AC #3).
- `internal/relay/handlers/archive_conversation.go` (whole file, ~135 lines) — the handler under test.
  Confirms: `SetArchived(id, archived)` flips the flag, `Get` snapshots the post-flip record, `Save`
  runs **before** `c.Reply` (so the disk write is complete by the time the reply lands — no polling),
  a miss on `SetArchived` replies `conversation.not_found` and Saves nothing, and `last_used_at` is
  never bumped (archive is a metadata edit, not a "use").
- `cmd/pyry/relay.go:410-411` — the two registrations. `TypeArchiveConversation` → `archived=true`,
  `TypeUnarchiveConversation` → `archived=false`, same factory. This test is a live guard on both lines:
  delete either and the corresponding frame falls through to the no-handler `protocol.unsupported` arm
  and the round-trip assertion fails.
- `internal/protocol/conversations_write.go:80-131` — `ArchiveConversationPayload{ConversationID string}`
  (id-only, serves both verbs) and `ConversationUpdatedPayload` (reply body; note `IsArchived bool`
  is always serialized — no omitempty — on the wire).
- `internal/protocol/codes.go:22,83,90` — `CodeConversationNotFound = "conversation.not_found"`,
  `TypeArchiveConversation`, `TypeUnarchiveConversation`.
- `internal/conversations/conversation.go:83` — on-disk `IsArchived bool json:"is_archived,omitempty"`.
  **Read-back caveat:** unlike the wire payload, the on-disk key IS omitempty — after unarchive the key
  is absent from `conversations.json`. Decode the read-back into a plain `bool` field; an absent key
  yields `false`. Assert the value, never key presence.

## Context

A 2026-07-15 full-repo review found eleven client-sendable v2 wire verbs whose only coverage is
package-level unit tests — no e2e at any tier (the #949 `promote_conversation` gap shape). This ticket
closes two of them: `archive_conversation` / `unarchive_conversation`. They are inverse toggles of one
durable `is_archived` flag through one shared handler (the `archived` bool is baked into each
registration), so a single archive → unarchive round-trip against one seeded row is the cohesive
scenario. Siblings #974 (rename) and #975 (delete) shipped the same-shaped tests; this is the third.

## Design

One new file `internal/e2e/relay_v2_archive_test.go`, build tag `e2e`, package `e2e`. One top-level
`TestRelayV2_Archive(t)` with two subtests, exactly mirroring `relay_v2_rename_test.go`:

- `v2_enabled_archive_unarchive_round_trip` (AC #1 + #2)
- `v2_enabled_archive_not_found` (AC #3)

Each subtest owns its own `pair` → seed → spawn (not a shared daemon), so the not-found subtest reads a
pristine, never-mutated row. All harness primitives already exist — **do not extract a shared session
helper** (per Technical Notes): `shortHome`, `RunBareIn` + `decodePairPayload`, `StartInWithEnv` with
`PYRY_ALLOW_INSECURE_RELAY=1` + `PYRY_MOBILE_V2=1`, `fakerelay.New(relayTestLogger())`,
`readPersistedServerID`, `waitBinaryHello`, `fakephone.Dial`, `driveHandshakeToOpenDaemon` (returns
`initSend, initRecv`), `initSend.Encrypt` + `sendNoiseMsg`, `readInnerFrame` + `decryptInnerEnvelope`,
`mustJSON`. Inline the pair → spawn → handshake block into each subtest exactly as the rename test does.

### Round-trip subtest — two frames over ONE encrypted channel

The defining contract of this ticket: after a single handshake, send **both** frames sequentially over
the **same** transport cipher states — do NOT re-handshake between them.

1. Seed one conversation row with `is_archived:false` and a *distinguishing* set of other fields so
   "the other fields are preserved" is a real check on both replies (mirrors rename's promoted+named
   seed): `is_promoted:true`, a `name`, `cwd` = `home`, `last_used_at` = a fixed seeded RFC3339 stamp.
2. Complete the handshake once → `initSend, initRecv`.
3. **Archive** (request id `A`): seal an `Envelope{Type: TypeArchiveConversation, Payload:
   ArchiveConversationPayload{ConversationID: convID}}`, send, read one inner frame, decrypt with
   `initRecv`. Assert (AC #1): `Type == TypeConversationUpdated`; `InReplyTo == &A`; decoded
   `ConversationUpdatedPayload.IsArchived == true`; `ID == convID`; and the other fields survive —
   `IsPromoted == true`, `Name` = seeded name, `Cwd == home`, `LastUsedAt.Equal(seededTime)` (compare
   with `.Equal`, never `==` — monotonic-clock discipline). Then read `conversations.json` back off disk
   and assert the row's `is_archived == true`.
4. **Unarchive** (request id `B`, `B != A`): on the **same** `initSend`/`initRecv`, seal
   `Envelope{Type: TypeUnarchiveConversation, Payload: ArchiveConversationPayload{ConversationID:
   convID}}`, send, read one inner frame, decrypt. Assert (AC #2): `Type == TypeConversationUpdated`;
   `InReplyTo == &B`; `IsArchived == false`; other fields still preserved (`IsPromoted == true`, `Name`,
   `Cwd`, `LastUsedAt.Equal(seededTime)`). Then read `conversations.json` back and assert the row's
   `is_archived == false` (the toggle reverses; the key is absent from disk under omitempty — decode
   into a `bool`, assert the value).

Nonces stay in lockstep because request/reply strictly alternate (`Encrypt` advances the send nonce,
`Decrypt` advances the receive nonce; send-A, recv-A, send-B, recv-B). The handler emits exactly one
correlated frame per request (`c.Reply`, no separate broadcast in the handler path) — read exactly one
frame after each send.

### Not-found subtest (AC #3)

Seed one row (`is_archived:false`, a keep-name, `cwd` = home) that must survive untouched. Send **one**
`archive_conversation` frame (request id `C`) for a *different, absent* `conversation_id`. Assert the
decrypted reply is `Type == TypeError`, `InReplyTo == &C`, decoded `ErrorPayload.Code ==
CodeConversationNotFound`. Then read `conversations.json` back and assert the seeded row is intact and
its `is_archived` is still `false` (the failed toggle mutated nothing — the handler replies before any
`Save`). Pin `archive_conversation` here (AC #3 allows either verb; one deterministic choice).

Use distinct UUIDs and distinct request ids across the two subtests (as rename uses `77…`/`66…`/`55…`
and reqIDs 51/52). Any fixed distinct values are fine.

## Concurrency model

Minimal. One phone, one daemon, one goroutine of test logic per subtest. Frames are strictly
sequential over a single Noise transport channel (send → read-reply → send → read-reply). No shared
mutable state between subtests (each owns its `home`, spawn, and seed). The only cross-goroutine
interaction is the daemon's Save-then-Reply ordering: `reg.Save` completes synchronously (atomic
tmp→fsync→rename) before `c.Reply` constructs the frame, so reading the disk after the reply lands is
race-free without any poll — the same guarantee `relay_v2_rename_test.go` relies on.

## Error handling

The one error path is the not-found subtest above. There are no new failure modes to design — the
handler's malformed-payload and persist-failure branches are not exercised by this test (well-formed
payloads, best-effort Save). Standard e2e cleanup via `t.Cleanup` for `fr.Close`, `h.Stop`,
`phone.Close`, exactly as the template registers them.

## Testing strategy

The test *is* the deliverable. AC → assertion mapping:

- **AC #1** — round-trip subtest, archive step: `conversation_updated` reply with `in_reply_to` = archive
  request id and payload `is_archived == true`; on-disk row `is_archived == true`.
- **AC #2** — round-trip subtest, unarchive step: `conversation_updated` reply with `in_reply_to` =
  unarchive request id and payload `is_archived == false`; on-disk row `is_archived == false`.
- **AC #3** — not-found subtest: `error` reply carrying `conversation.not_found`; `conversations.json`
  row unchanged.
- **AC #4** — every assertion reads only decrypted wire frames and `conversations.json` off disk; no
  fakephone/fakerelay internals inspected. Preservation checks (`is_promoted`, `name`, `cwd`,
  `last_used_at` unchanged) make the observable-only checks non-vacuous.
- **AC #5** — `make e2e` green with `-race`. Verify locally:
  `go test -race -tags e2e ./internal/e2e/ -run TestRelayV2_Archive`.

## Open questions

None. The wire contract, handler behaviour, registration sites, and harness primitives are all
confirmed against the shipped #974/#975 siblings. The developer copies `relay_v2_rename_test.go`,
swaps the verb types and payload to `ArchiveConversationPayload`, folds the two rename subtests into a
single round-trip subtest that sends both frames over one channel, and asserts `IsArchived` instead of
`Name`.
