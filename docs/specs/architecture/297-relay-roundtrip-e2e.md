# 297 — net/e2e: relay roundtrip (appendix-flow happy path)

## Files to read first

- `internal/e2e/relay_send_message_test.go` — closest existing pattern; copy the pair → seed `conversations.json` → `StartRotationWithRelay` → wait-for-binary-hello → phone dial → `hello`/`hello_ack`/`send_message`/`ack` scaffolding. Most of the new test mirrors this verbatim.
- `internal/e2e/relay_assistant_turn_test.go` — pattern for the step-5 `message` echo: `PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER` env + `asstTrigger` write + the drain-until-marker loop that ignores TUI prelude chunks.
- `internal/e2e/register_push_token_test.go` — pattern for the step-6 verb. Use this AC subset verbatim; don't re-check on-disk persistence here (that's already pinned on the dedicated test — this test stays an envelope-protocol test).
- `internal/e2e/internal/fakephone/fakephone.go:66-150` — `Dial`/`Send`/`Receive(timeout)` surface and `ErrReceiveTimeout` sentinel. Receive is one-shot and bounded; no polling.
- `internal/e2e/internal/fakerelay/fakerelay.go:150,591` — `URL()` and `LastBinaryHello(serverID)` are the two surfaces this test uses.
- `internal/e2e/harness.go:227,317,649` — `StartInWithEnv`, `StartRotationWithRelay`, `RunBareIn`. Use `StartRotationWithRelay` because step 5 needs a fakeclaude wired so the assistant-turn bridge has something to echo.
- `internal/protocol/handshake.go` — `HelloAckPayload`, `HelloClientPayload`, `AckPayload`.
- `internal/protocol/messaging.go` — `SendMessagePayload`, `MessagePayload`.
- `internal/protocol/conversations_read.go` — `ListConversationsPayload{}` (empty), `ConversationsPayload{Conversations []ConversationSummary}`.
- `internal/relay/handlers/list_conversations.go` — confirms the binary projects all rows and orders by `LastUsedAt` then `ID`. Used to predict what the test should see.
- `cmd/pyry/relay.go:132-148` — confirms `TypeListConversations`, `TypeRegisterPushToken`, `TypeSendMessage` are all registered on the per-conn dispatcher, and the assistant-turn bridge runs when a bridge is present (it is, via `StartRotationWithRelay`).
- `docs/PROJECT-MEMORY.md` § "`time.Time` round-trip discipline" — JSON marshal strips the monotonic clock; compare `time.Time` via `time.Time.Equal`, never `==` / `reflect.DeepEqual`.
- `docs/protocol-mobile.md:712-end` — the appendix the test drives.

## Context

The wire-protocol stack ships in five layers: payload types (`#271–#275`), transport (`#247`), binary↔relay handshake (`#248`), inbound-token check (`#249`), per-verb handlers (`#250`, `#312`, `#319`, `#323`), and the claude→message bridge (`#311`). Three existing e2e tests each pin one slice:

| Test | Pins |
|---|---|
| `TestRelay_Hello` | binary `hello` (step 2) |
| `TestRelay_SendMessage_AckAndPTYDelivery` | phone `hello` + `send_message` + PTY delivery (steps 3, 5a) |
| `TestRelay_AssistantTurn_BroadcastsMessageEnvelope` | `message` echo back to phone (step 5b) |
| `TestRelay_RegisterPushToken_AckAndPersists` | `register_push_token` + ack + on-disk persistence (step 6) |

What none of them cover: a **single connection** that walks every step of the appendix in order and asserts the cross-step invariants — monotonic `id` per conn over a mixed set of verbs, `in_reply_to` chaining across four request/response pairs, `conversation_id` survival from `send_message` request through assistant-message echo, `ts` round-trip on the wire.

This ticket fills that gap. Pure consumer test. Zero production-code changes. The dispatch table is already wired; the harness packages are already shipped.

## Design

### One new file

`internal/e2e/relay_roundtrip_test.go` — build tag `e2e`, one test function `TestRelay_Roundtrip_Appendix`. No helpers extracted; the existing helpers (`shortHome`, `relayTestLogger`, `readPersistedServerID`, `StartRotationWithRelay`, `decodePairPayload`, `mustJSON`) cover everything. Resist the urge to factor — there's exactly one consumer of this composition and a second one would be a different test in spirit anyway.

### Sequence

The test drives the appendix in order on a single phone connection. IDs in parentheses are phone-side request IDs; bracketed IDs are binary-side response IDs the test asserts on.

1. **Mint a pairing token.** `RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")` → `decodePairPayload`. Same as the existing three relay tests.
2. **Seed `conversations.json`** with one row (fixed UUIDv4 `knownConvID`, `cwd=home`, `last_used_at` fixed). This row is what `list_conversations` must report back **and** the conversation `send_message` targets.
3. **Spawn the daemon** via `StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverCreatedTrigger, stdinLog, fr.URL()+"/v1/server", "PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER="+asstTrigger)`. Same trigger-path pattern as `relay_assistant_turn_test.go`.
4. **Wait for binary `hello`** via `fr.LastBinaryHello(serverID)` polling with a 5s deadline. (No assertions on the binary hello content here — `TestRelay_Hello` already pins shape.)
5. **Dial fakephone** with the minted token.
6. **Phone `hello` (id=1) → `hello_ack` [id=N₁]**. Assert `InReplyTo == 1`, `Type == hello_ack`. Record `N₁` into the binary-id ledger (see Invariants below).
7. **Phone `list_conversations` (id=2) → `conversations` [id=N₂]**. Assert `InReplyTo == 2`, `Type == conversations`. Decode payload; assert `len == 1` and `Conversations[0].ID == knownConvID`. Record `N₂`.
8. **Phone `send_message` (id=3) → `ack` [id=N₃]** with `ConversationID=knownConvID`, `MessageID=u-1`, `Text=knownUserText`. Assert `InReplyTo == 3`, `Type == ack`. Record `N₃`.
9. **Trigger the assistant turn.** `os.WriteFile(asstTrigger, []byte(knownAssistantText), 0o600)`. Then drain the phone with `phone.Receive(remaining)` in a deadline-bounded loop (mirror the prelude-skipping loop in `relay_assistant_turn_test.go`), accepting frames until one with `Type == message` and `Payload.Text` containing `knownAssistantText` arrives. This frame is `[id=N₄]`, `InReplyTo == nil`. Decode `MessagePayload`; assert `ConversationID == knownConvID` and `Role == "assistant"`. Record `N₄`.
10. **Phone `register_push_token` (id=4) → `ack` [id=N₅]**. Assert `InReplyTo == 4`, `Type == ack`. Record `N₅`. (No on-disk reload — that's `TestRelay_RegisterPushToken_AckAndPersists`'s job.)

### Invariants — checked at the end, not interleaved

Collect each received envelope (or its `ID`) into a slice during the run. After step 10:

- **Monotonic binary-side ids.** `N₁ < N₂ < N₃ < N₄ < N₅`, strictly. The dispatcher's per-conn counter starts at 1 and increments on every emitted frame regardless of trigger source (reply vs server-initiated). The existing per-slice tests pin `>= 1`, `>= 2`, `>= 3` separately; this is the first place a single test sees all five and can assert strict ordering.
- **`in_reply_to` chain.** For each of `(hello, list_conversations, send_message, register_push_token)`, the matching response has `InReplyTo != nil && *InReplyTo == request.ID`. The `message` echo has `InReplyTo == nil` (server-initiated). Already a per-slice convention; assert here as one chain.
- **`ts` round-trip.** For every received envelope, assert `!env.TS.IsZero()`, and assert `roundTripTS(env.TS).Equal(env.TS)` where `roundTripTS` marshals through `json.Marshal` + `json.Unmarshal` of a `struct { TS time.Time }`. The marshal strips the monotonic-clock reading; `Equal` is the only correct comparison. This is the AC's "compare with `time.Time.Equal` per project convention" — pinned in `PROJECT-MEMORY.md` (read it).
- **`conversation_id` stability.** The `send_message` request carried `knownConvID`; the `message` echo's `MessagePayload.ConversationID` must be `== knownConvID` byte-for-byte (string compare). `ack` carries no conversation_id (`AckPayload` is `struct{}` by spec) — bridged across that gap by `in_reply_to`.

### What the test does NOT do

- Reload `devices.json` after `register_push_token`. Pinned in `TestRelay_RegisterPushToken_AckAndPersists`.
- Assert the assistant text reaches fakeclaude's stdin. Pinned in `TestRelay_SendMessage_AckAndPTYDelivery`.
- Assert close-4401 / `auth.invalid_token`. Lives on `relay_auth_test.go` per the ticket's Out-of-Scope.
- Exercise multiple phones, reconnect, or backfill.
- Assert ordering of conversations (only one row seeded).

These are protocol-overlap concerns owned by adjacent tests. Keeping them out is what holds this test at ~150 LOC.

### Helpers

No new package-level helpers. The `roundTripTS` envelope-time round-trip is a closure or short helper inside the test file — ~6 lines, single caller. Don't extract.

## Concurrency model

The test drives a single phone conn synchronously. Send → Receive → assert, in order. No concurrent goroutines on the test side. The daemon's per-conn dispatcher goroutine and the assistant-turn bridge are exercised but the test interacts via the WS, not via goroutine signalling.

Step 9's drain loop is the only place where the test reads multiple frames in sequence without knowing the count: the supervisor's PTY may emit prelude chunks (TUI banner / clear) before the assistant marker chunk. The loop pattern is verbatim from `relay_assistant_turn_test.go:159-190`.

## Error handling

- `phone.Receive` deadlines: use 3s per receive on steps 6–8 and 10. Step 9's drain has an outer 5s budget across multiple `Receive` calls (because prelude chunks are unbounded in count but bounded in arrival rate).
- `phone.Receive` returning `ErrReceiveTimeout` at any deterministic step is `t.Fatalf` — these are not retries, they're protocol failures.
- `phone.Send` errors are `t.Fatalf` — no connection-level recovery; if the daemon dropped the conn, the test has failed.
- All cleanups via `t.Cleanup`: fakerelay close, phone close, harness stop. Same idiom as the existing relay tests.

## Testing strategy

Run the test under `-race` as required by the AC. CI already does this for the `e2e` build tag (`.github/workflows/ci.yml`).

Run it locally a few times to surface flakes from the step-9 drain. The 5s outer budget is generous; the existing assistant-turn test has been stable under it.

## Open questions

- None. The seam exists, the harness is shipped, and four sibling tests have already proven each step in isolation. If the test grows past ~200 LOC the architect underestimated; flag back via `needs-rework:po` per the ticket's own hedge.

## Out of scope (re-stated for the implementer)

- No new production code. If you find yourself touching anything under `cmd/`, `internal/dispatch/`, `internal/relay/`, or `internal/protocol/`, stop and flag.
- No new harness helpers in `internal/e2e/internal/`. If you find yourself touching fakephone, fakerelay, or fakeclaude, stop and flag.
- No changes to the existing four relay e2e tests — even if their setup looks duplicative, leave them alone. Each one is a focused regression for one slice; consolidating would erase coverage attribution.
