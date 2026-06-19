# Spec #689 — e2e fixtures bind their conversation to the bootstrap session (#678 regression)

**Ticket:** #689 — fix(e2e): #678 bound-session routing regressed 7 send_message/turn e2e tests (suite red on `main`)
**Size:** S (test + fixture only; **zero production-code change**)
**Family:** follows #673 (e2e fixture fix for #668's contract change)

---

## Files to read first

- `internal/e2e/relay_send_message_test.go:47-83` — the **canonical** seed + pre-create-JSONL + start pattern. The other 5 "binding-only" tests are structural copies of this. Extract: the `convJSON` literal shape (lines 51-53) and the computed `sessionsDir` + pre-created `<initialUUID>.jsonl` (lines 65-75).
- `internal/e2e/respawn_after_eviction_test.go:52-86` — eviction seed + `startEvictionHarness`; the AC#3 product-check site. Same shape as the canonical test.
- `internal/e2e/relay_two_phone_coarse_test.go:74-99` — **the one outlier**: `sessionsDir = tmp/claude-sessions` (NOT the daemon's computed dir) and **no** pre-created JSONL. Extract: why reconciliation never fires here today.
- `internal/e2e/relay_two_phone_structured_test.go:126-176` — coarse's sibling, already on the computed-dir + pre-create pattern (lines 141-158). Coarse should adopt exactly this shape.
- `cmd/pyry/main.go:652-698` — `errNoBoundSession`, `sessionRouter.Route`. The empty-`CurrentSessionID` guard at line 686 fires **before** any `pool.Lookup`; line 689-690 does `Lookup(SessionID(conv.CurrentSessionID))`. This is the contract the binding satisfies.
- `internal/relay/handlers/send_message.go:112-133` — Route-error → wire-code mapping: `ErrConversationNotFound` → `conversation.not_found` (not retryable); any other Route error (incl. `errNoBoundSession`) → retryable `server.binary_offline`. This is the failing-shape the tests observe.
- `internal/sessions/reconcile.go:118-148` — `reconcileBootstrapOnNew`: on `Pool.New`, rotates the bootstrap entry's id to the most-recent `<uuid>.jsonl` in the computed sessions dir via `RotateID`. **This is why the bound id must equal the test's pre-created `initialUUID`** — after reconciliation `Pool.Default().ID() == initialUUID`.
- `internal/conversations/conversation.go:44-49` — `CurrentSessionID string json:"current_session_id,omitempty"`. The field the fixture must populate; `omitempty` is why an unset field serializes to "" → `errNoBoundSession`.
- `internal/conversations/registry.go:46-62` (`Load`) and `:126-137` (`Get`) — `Load` runs **once** at startup (`cmd/pyry/main.go:520`); `Get` reads the in-memory slice. **No reload.** This is why the binding must be on disk *before* the daemon starts.
- `internal/e2e/harness.go:318` (`StartRotationWithRelay`) — where the new shared helper `seedBoundConversation` belongs (alongside the other e2e seed/wait helpers). No new imports (`os`, `filepath`, `testing` already present).

---

## Context

#678 (PR #683, merged `6830e08`) changed `send_message` from writing to the per-conn bootstrap surface to resolving the conversation's **bound** session via `sessionRouter.Route(conversationID)`, and **deliberately removed the bootstrap fallback** (its AC#4). `Route` rejects an empty `CurrentSessionID` before any pool `Lookup` (`cmd/pyry/main.go:686`), returning `errNoBoundSession`, which the handler maps to a retryable `server.binary_offline` (`send_message.go:121-132`).

Seven `internal/e2e` tests seed `conversations.json` **without** a binding (`current_session_id` empty), relying on the now-removed fallback. `internal/e2e` is not in `make check`, so #678 merged green while breaking it. **Bisect-confirmed** (per the ticket): tests pass at `6830e08^1`, fail at `6830e08`. Same family as #673.

This is **not a production bug**: production conversations are created via `create_conversation` (`handlers/create_conversation.go:144`), which mints a session and sets `CurrentSessionID` at creation time. Only the hand-seeded test fixtures skip the binding.

### The non-obvious constraint (corrects the ticket's Technical Note)

The ticket's Technical Note suggests *"a shared harness helper that **reads** the bootstrap id [from `sessions.json`] and seeds `current_session_id`."* That mechanism does **not** work, and the developer must not implement it:

1. The daemon loads `conversations.json` **once** at startup into an in-memory `*conversations.Registry` (`main.go:520`); `Route` reads that in-memory copy (`Registry.Get`). Writing `conversations.json` *after* the daemon starts has **no effect** on routing.
2. Therefore the binding must be present on disk **before** the daemon starts — but the bootstrap session id is only minted/reconciled *during* startup.

These two facts only reconcile because the bound id is **predictable**, not because it can be read back: `reconcileBootstrapOnNew` rotates the bootstrap entry's id to the most-recent `<uuid>.jsonl` in the daemon's computed sessions dir. Six of the seven tests pre-create exactly one `<initialUUID>.jsonl` in that dir before start, so after reconciliation **`Pool.Default().ID() == initialUUID`**. The fixture binds `current_session_id = initialUUID` — a compile-time constant the test already owns. No read-back, no `sessions.json` parsing.

**This was verified empirically during spec authoring** (binding `current_session_id = initialUUID` and running each representative test): `TestRelay_SendMessage_AckAndPTYDelivery` (green, 1.8s), `TestE2E_IdleEviction_RespawnsOnSendMessage` (green, 3.1s), and `TestTwoPhoneCoarse_NonInteractiveOnly` after alignment (green, 31.7s — the 30s is the pre-existing non-TUI `WaitReady` timeout, unchanged by this work).

---

## Design

### 1. Shared helper in `internal/e2e/harness.go`

Introduce one helper that writes the bound-conversation fixture, replacing the inlined `convJSON` literal in the seven affected tests. It centralizes the load-bearing invariant (bound id == reconciled bootstrap id == `initialUUID`) in one documented place so a future test edit can't silently re-break the binding.

```go
// seedBoundConversation writes conversations.json for the "test" instance with a
// single conversation row bound to boundSessionID. Binding is load-bearing under
// #678: sessionRouter.Route rejects an empty current_session_id before any pool
// Lookup, so an unbound row yields a retryable server.binary_offline instead of
// reaching WriteUserTurn. The daemon loads conversations.json once at startup
// (in-memory registry, no reload), so the row must exist BEFORE the daemon
// starts; boundSessionID MUST equal the bootstrap session's pool id, which
// reconcileBootstrapOnNew rotates to the most-recent <uuid>.jsonl in the computed
// sessions dir — i.e. the caller's pre-created initialUUID.
func seedBoundConversation(t *testing.T, home, convID, boundSessionID string)
```

- Behavior: writes `<home>/.pyry/test/conversations.json` (mode `0o600`) containing one row `{id: convID, cwd: home, current_session_id: boundSessionID, is_promoted: false, last_used_at: "2026-01-01T00:00:00Z"}`. `t.Fatalf` on write error. `t.Helper()`.
- The JSON shape is byte-identical to the existing inlined literals **plus** the `current_session_id` field — preserve field order so the diff reads as "added one field + extracted to helper".
- Do **not** migrate non-affected conversation-seeding tests to this helper (e.g. the v2 list-conversations / snapshot tests that never call `send_message`). Touch only what's necessary.

### 2. Per-test changes

Each affected test sends `send_message` to its `knownConvID`; bind that id to the session id reconciliation will mint (its `initialUUID`).

| Test | File | convID (bind) | boundSessionID (== initialUUID) | Change |
|------|------|---------------|----------------------------------|--------|
| `TestRelay_SendMessage_AckAndPTYDelivery` | `relay_send_message_test.go` | `333…3` | `444…4` | replace inline seed with `seedBoundConversation` |
| `TestRelay_Roundtrip_Appendix` | `relay_roundtrip_test.go` | `777…7` | `888…8` | replace inline seed |
| `TestRelay_AssistantTurn_BroadcastsMessageEnvelope` | `relay_assistant_turn_test.go` | `555…5` | `666…6` | replace inline seed |
| `TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope` | `relay_v2_daemon_test.go` | `888…8` | `999…9` | replace inline seed (this test fn only; ~line 440) |
| `TestTwoPhoneStructured_InteractiveReceivesStream` | `relay_two_phone_structured_test.go` | `555…5` | `444…4` | replace inline seed |
| `TestE2E_IdleEviction_RespawnsOnSendMessage` | `respawn_after_eviction_test.go` | `555…5` | `666…6` | replace inline seed |
| `TestTwoPhoneCoarse_NonInteractiveOnly` | `relay_two_phone_coarse_test.go` | `555…5` | `444…4` | seed **+ alignment** (see §3) |

(Use the full UUIDs already defined as `const`s in each test; the table abbreviates.)

> `relay_v2_daemon_test.go` contains other test functions (list-conversations, snapshot) that do **not** call `send_message` — leave them untouched. Only `TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope` needs the binding.

### 3. The coarse outlier — `relay_two_phone_coarse_test.go`

Unlike the other six, this test uses `sessionsDir = filepath.Join(tmp, "claude-sessions")` (a private temp dir handed to fakeclaude) and pre-creates **no** JSONL. The daemon's reconciliation scans the *computed* dir (`<home>/.claude/projects/encode(home)`), finds it empty, and never rotates — so the bootstrap id stays the random pool mint and `current_session_id = initialUUID` would not resolve.

Bring it onto the same pattern as its structured sibling (`relay_two_phone_structured_test.go:141-158`):
- Change `sessionsDir` to the computed path `filepath.Join(home, ".claude", "projects", encodeWorkdir(home))` + `os.MkdirAll(…, 0o700)`.
- Pre-create `<initialUUID>.jsonl` (`[]byte("{}\n")`, `0o600`) in that dir before `StartRotationWithRelay`.
- Then `seedBoundConversation(t, home, knownConvID, initialUUID)`.

`tmp` is still needed for `rotateTrigger`/`stdinLog`/`asstTrigger` — keep it; only `sessionsDir` moves. The coarse test never awaits the ack and asserts nothing about JSONL content (it is coarse, not structured), so moving the sessions dir is inert to its assertions; the only effect is making reconciliation set the bootstrap id to `initialUUID`. **Verified green** during spec authoring.

---

## Concurrency model

None. Pure on-disk fixture construction before daemon start; no goroutines, no shared state.

---

## Error handling

- The helper `t.Fatalf`s on `os.WriteFile` failure, matching the existing inlined pattern.
- **AC#3 product check (the one case that could escalate):** `TestE2E_IdleEviction_RespawnsOnSendMessage` exercises an idle-**evicted** bound session. The concern: does `Route` refuse an evicted-but-bound session instead of letting `Activate` respawn it? **Answer: no — verified green empirically.** An idle-evicted session stays in the pool parked in `runEvicted`, so `Route → pool.Lookup(boundID)` resolves it; `boundSession.Activate → pool.Activate` (`main.go:712-714`) respawns it; the inbound `send_message` yields `TypeAck`. The #396 silent-outage contract holds under bound-session routing. **No production regression — no escalation needed.** (Had this failed, the ticket's AC#4 directs splitting it out as a `security-sensitive` product-regression ticket rather than masking it; it did not fail.)

---

## Testing strategy

- **AC#1 is the gate:** run the **whole** suite, not the seven in isolation —
  `go test -tags e2e ./internal/e2e/... -count=1`. #674 greened one path while siblings re-broke the rest precisely because the full suite was never re-run. The suite must be fully green.
- Each affected test already asserts the post-binding behavior it needs (ack / message-envelope fan-out / respawn); the fixture change simply lets `Route` resolve so those assertions are reached. No new assertions required.
- Run `gofmt -l` / `go vet -tags e2e ./internal/e2e/...` over the touched files.
- Note: the coarse test legitimately takes ~31s (pre-existing non-TUI 30s `WaitReady` timeout); don't mistake it for a hang.

---

## Open questions

- **Helper vs. inline one-liner.** The spec prescribes a shared helper for the documentation/uniformity reasons above. If the developer finds a test whose `convJSON` diverges enough that the helper doesn't fit cleanly, inlining the `current_session_id` field for that one test (with a one-line comment pointing at the helper's doc) is an acceptable fallback — the binding value and the pre-start-on-disk requirement are what matter, not the call shape.
- **`make check` gate gap is out of scope.** The operator decision is "no CI" for the `-tags e2e` suite; do **not** add CI in this ticket. (Process note only: turn-delivery and session-binding changes should run the e2e suite before merge.)
