# Spec #974 — e2e: `rename_conversation` over the v2 wire

**Ticket:** #974 (split from #960) · **Size:** S · **Security-sensitive:** no (test-only; observable-behaviour assertions, no new attack surface)

## Files to read first

- `internal/e2e/relay_v2_promote_test.go` (whole file, ~168 lines) — **the template.** Copy its pair → seed → spawn-daemon → Noise_IK handshake → seal-request → decrypt-reply → read-registry-back structure verbatim; swap the verb, payload, and assertions. Both this spec's subtests are this file with `promote` → `rename`.
- `internal/e2e/relay_v2_daemon_test.go:245-308` — the `roundTrip(reqID, convID)` closure (`seal → sendNoiseMsg → decryptInnerEnvelope(readInnerFrame(...))`, single frame) and the **error-reply decode** (`errReply.Type == TypeError`, `InReplyTo` correlation, `ErrorPayload.Code == CodeConversationNotFound`). The not-found subtest's assertions are lifted directly from lines 292-308.
- `internal/relay/handlers/rename_conversation.go` (whole file, 129 lines) — the handler under test. Confirms: sets `cv.Name = &title`; **preserves** `IsPromoted`, `IsArchived`, `Cwd`, `LastUsedAt` (rename does not bump last-used); on a registry miss replies `CodeConversationNotFound` **before** any `Save`, so the file is untouched on the error path; empty/blank title → `CodeProtocolMalformed` (out of scope for this ticket's ACs).
- `internal/protocol/conversations_write.go:46-57` — `RenameConversationPayload{ConversationID, Name}` (two fields, **no `Cwd`** — unlike promote). Lines 112-131 — `ConversationUpdatedPayload{ID, IsPromoted, IsArchived, Name *string, Cwd, LastUsedAt time.Time}`, the reused reply body.
- `internal/protocol/handshake.go:81-86` — `ErrorPayload{Code, Message, Retryable, RetryAfterS}`.
- `internal/protocol/codes.go:22,43,56,62` — `CodeConversationNotFound = "conversation.not_found"`, `TypeError`, `TypeConversationUpdated`, `TypeRenameConversation`.
- `internal/conversations/*.go` — the on-disk `Conversation` struct: confirm the JSON tags (`id`, `name` as `*string`, `cwd`, `is_promoted`, `is_archived`, `last_used_at`) before hand-authoring the seed JSON. The promote test's local `onDisk` decode struct (its lines 141-148) is the read-back template.
- `docs/PROJECT-MEMORY.md` § "time.Time round-trip discipline" — compare any `time.Time` that crossed the wire with `.Equal`, never `==` / `reflect.DeepEqual`.

## Context

A 2026-07-15 full-repo review found eleven client-sendable v2 wire verbs whose only coverage is package-level unit tests — the exact `promote_conversation` gap shape that let #949 ship a handler nothing exercised end-to-end. `rename_conversation` is one of four conversation-lifecycle verbs in that gap. This ticket proves its happy path and its not-found path **at the daemon boundary**: over the encrypted Noise v2 channel against a real spawned daemon, asserting decrypted wire frames and on-disk `conversations.json` state — never fake internals. Siblings (also split from #960) cover `delete_conversation` and the `archive`/`unarchive` round-trip; #963 is the realclaude capstone blocked by all of them.

## Design

**New file (the only deliverable):** `internal/e2e/relay_v2_rename_test.go`, build tag `//go:build e2e`, `package e2e`. No production code, no new exported symbols, no shared-helper extraction (Technical Notes: inline the setup — the promote test already proves every primitive works file-locally).

**Shape — two independent subtests, each with its own spawn + its own seed:**

```
func TestRelayV2_Rename(t *testing.T) {
    t.Run("v2_enabled_rename_conversation_round_trip", testV2DaemonRenameRoundTrip)
    t.Run("v2_enabled_rename_conversation_not_found",  testV2DaemonRenameNotFound)
}
```

Two spawns (not one shared daemon) is deliberate: it keeps the error-path "`conversations.json` unchanged" assertion unambiguous — the not-found subtest's seeded row is *pristine*, never mutated by a prior rename, so the read-back asserts the seeded name exactly. This mirrors `relay_v2_promote_test.go`'s one-function-per-scenario shape rather than `relay_v2_daemon_test.go`'s shared-daemon closure. The ~40 lines of pair/spawn/handshake setup duplicated across the two subtests is the accepted e2e idiom here; a file-local setup helper is *optional*, not required (do not block on it).

**Reused harness primitives (all exist; copy call-for-call from the promote test):** `shortHome`, `RunBareIn` (pair), `decodePairPayload`, `StartInWithEnv` (env `PYRY_ALLOW_INSECURE_RELAY=1`, `PYRY_MOBILE_V2=1`; flag `-pyry-relay=<fr.URL()>/v2/server`), `fakerelay.New` / `relayTestLogger`, `readPersistedServerID`, `waitBinaryHello`, `fakephone.Dial`, `driveHandshakeToOpenDaemon`, `sendNoiseMsg`, `readInnerFrame`, `decryptInnerEnvelope`, `mustJSON`. Seed path is `filepath.Join(home, ".pyry", "test", "conversations.json")` (instance name `test` from `pair -pyry-name=test`), written **after** pair, **before** `StartInWithEnv`, so the daemon loads it at boot — verbatim from the promote test.

### Wire contract

| | Request | Success reply | Error reply |
|---|---|---|---|
| Envelope `Type` | `TypeRenameConversation` | `TypeConversationUpdated` | `TypeError` |
| Payload type | `RenameConversationPayload{ConversationID, Name}` | `ConversationUpdatedPayload{ID, IsPromoted, IsArchived, Name *string, Cwd, LastUsedAt}` | `ErrorPayload{Code, …}` |
| Correlation | request `ID` | `InReplyTo == &reqID` | `InReplyTo == &reqID` |
| Discriminant | — | `Name` echoes requested title | `Code == CodeConversationNotFound` |

Note the payload divergence from promote: `RenameConversationPayload` has **no `Cwd`**. Do not copy promote's `Cwd: home` into the request.

### Seed shapes

**Happy path** — one row, deliberately **`is_promoted: true`** with a pre-existing name, so "unchanged" is a real check (a handler that hard-coded `false` or dropped the flag would be caught; seeding `false` and asserting `false` would not):

```
id           = "77777777-7777-4777-7777-777777777777"   // any fixed v4-shaped UUID
name         = "old-name"                                 // *string, non-nil
cwd          = home
is_promoted  = true
is_archived  = false
last_used_at = "2026-01-01T00:00:00Z"                     // fixed; parsed for the .Equal check
```
Rename to `"new-name"`.

**Error path** — one row that must survive untouched; the request targets a *different, absent* id:

```
seeded row:  id = "66666666-6666-4666-6666-666666666666", name = "keep-me", cwd = home, is_promoted = false
request id:  "55555555-5555-4555-5555-555555555555"       // NOT in the registry → miss
```

## Concurrency model

None introduced by the test. Single spawned daemon, single fakephone, one synchronous request → one reply per subtest. The `roundTrip` is strictly sequential: seal → `sendNoiseMsg` → `readInnerFrame` (3 s deadline) → `decryptInnerEnvelope`. The daemon's own goroutines are exercised as a black box. AC #5 (`make e2e` green under `-race`) is satisfied because the test adds no shared mutable state of its own; the `t.Cleanup` teardown order (phone close → daemon stop → fakerelay close) copies the promote test.

## Error handling (test-side)

Every harness step that can fail uses `t.Fatalf` with the same message idiom as the promote test (exit-code dumps for `pair`, decode errors, marshal errors). Assertion mismatches that should let the test continue use `t.Errorf`; ones that make later assertions meaningless (wrong reply `Type`, read-back failure) use `t.Fatalf`. The `readInnerFrame` 3 s deadline bounds a hung/no-reply daemon so the subtest fails loud instead of hanging the suite.

## Testing strategy

The file **is** the test. Assert observable behaviour only (AC #4) — decrypted envelopes and the on-disk file; never reach into `fakephone`/`fakerelay` state.

**`testV2DaemonRenameRoundTrip` (AC #1, #2):**
- Seed the happy-path row (above); pair, spawn, handshake.
- Seal + send `TypeRenameConversation` with `{ConversationID: "7777…", Name: "new-name"}`, `reqID` e.g. `51`.
- Decrypt the single reply frame. Assert: `Type == TypeConversationUpdated`; `InReplyTo != nil && *InReplyTo == reqID`; decoded `ConversationUpdatedPayload.ID == "7777…"`; `Name != nil && *Name == "new-name"`; `IsPromoted == true`; `IsArchived == false`; `Cwd == home`; **`LastUsedAt.Equal(seededTime)`** (parse `"2026-01-01T00:00:00Z"` once via `time.Parse(time.RFC3339, …)`; **never `==`**).
- Read `conversations.json` back off disk, decode with a local `onDisk` struct (promote-test pattern). Assert exactly one row; `Name == "new-name"`; `IsPromoted == true` (unchanged); `Cwd == home` (unchanged).

**`testV2DaemonRenameNotFound` (AC #3):**
- Seed the error-path row (`id = "6666…"`, `name = "keep-me"`); pair, spawn, handshake.
- Seal + send `TypeRenameConversation` with `{ConversationID: "5555…", Name: "whatever"}` (absent id), `reqID` e.g. `52`.
- Decrypt the reply. Assert: `Type == TypeError`; `InReplyTo != nil && *InReplyTo == reqID`; decoded `ErrorPayload.Code == CodeConversationNotFound`.
- Read `conversations.json` back. Assert the seeded row is intact — one row, `ID == "6666…"`, `Name == "keep-me"` (the failed rename mutated nothing). This is the "no row mutated" evidence.

**RED-on-regression check (why this test earns its keep):** if the handler's `TypeRenameConversation` registration at `cmd/pyry/relay.go:407` were removed, the verb would fall through to the no-handler `protocol.unsupported` arm and the happy-path `want conversation_updated` assertion would fail — the same failure shape the #949 gap review is closing. The test is a live guard on that registration, not just the handler body.

**Run:** `make e2e` (or `go test -race -tags e2e ./internal/e2e/ -run TestRelayV2_Rename`). Must be green under `-race` (AC #5).

## Open questions

- **Seeding `is_promoted: true` on load.** The registry `Load` unmarshals rows without enforcing cross-field invariants (a promoted row needs no special shape beyond the JSON tags), so seeding a promoted+named row is safe. If `Load` unexpectedly rejects or normalises it, fall back to `is_promoted: false` for the happy path and assert it stays `false` — the rename-persisted assertion (Name) still holds; only the flag-preservation strength weakens. Confirm against the `Conversation` struct + `Load` while writing the seed. Low risk.
- **On-disk `last_used_at` assertion.** Deliberately *not* asserted on the file (JSON string time-compare is fragile). The `time.Time.Equal` discipline is exercised on the decrypted reply's `LastUsedAt`, where it is a real `time.Time`. Sufficient for the Technical Note; do not add a string-time on-disk check.
