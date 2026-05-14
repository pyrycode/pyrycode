# #322 — `send_message`: dispatch.Handler + `TurnWriter` interface + dispatcher registration

Split from #313 (parent salvaged; reusable Files-to-read-first and wire-surface notes are at `docs/specs/architecture/313-send-message-handler.md` on `feature/313`). Scope here is **strictly inbound**: phone → `send_message` → supervisor write → synchronous `ack` (or refusal). Assistant-turn delivery is #311.

## Files to read first

- `internal/relay/handlers/register_push_token.go` (full file) — sibling handler this spec mirrors for file layout, factory shape, `replyAck`/`replyError` helper idiom, and logging posture. **Do NOT mirror its wider branch set** — `send_message` has fewer branches because the validation surface is upstream.
- `internal/relay/handlers/register_push_token_test.go:30-106` — `testLogger`, `newTestConn`, `makeRequest`, `assertEnvelopeShape` helpers. Copy the pattern (don't try to share — each handler test file keeps its own copies, see #319 spec). Use `protocol.TypeSendMessage` and `protocol.SendMessagePayload` shapes instead of register_push_token's.
- `internal/dispatch/dispatch.go:82-149` — `Conn.Auth()`, `Conn.NextID()`, `Conn.Reply(ctx, req, respType, payload)`. `Reply` (lines 141–149) is load-bearing: it stamps `id`, `in_reply_to`, `ts` so the handler never touches them. Mirror the existing handler's use of `Reply` for the wire envelope.
- `internal/dispatch/dispatch.go:95-110` — `NewTestConn(id, outbound, auth)` constructor (added in #319). Exact seam the unit tests use to drive a real `*Conn` without a full dispatcher.
- `internal/supervisor/supervisor.go:140-174` — `Supervisor.WriteUserTurn(id, payload) error`. Contract: when `ValidateConversation` is wired (pool's bootstrap path does this), unknown ids return `conversations.ErrConversationNotFound` verbatim; PTY write failures wrap with prefix `"supervisor: write user turn:"`.
- `internal/sessions/pool.go:350-362` — confirms the bootstrap session's `ValidateConversation` closure returns `conversations.ErrConversationNotFound` for unknown ids. This is the upstream guarantee the handler's sentinel mapping relies on.
- `internal/sessions/session.go:100-115` — existing one-line delegation pattern (`Session.State()` → `s.sup.State()`). The new `Session.WriteUserTurn` sits in the same delegation cluster.
- `internal/conversations/registry.go:25` — `ErrConversationNotFound` sentinel.
- `internal/protocol/codes.go:22` — `CodeConversationNotFound = "conversation.not_found"`. **Note the singular wire string**; the AC text reads `conversations.not_found` (plural) — see Open Questions §1. Use the existing constant.
- `internal/protocol/codes.go:44-46` — `TypeSendMessage`, `TypeAck`, `TypeError`.
- `internal/protocol/messaging.go:7-11` — `SendMessagePayload{ConversationID, MessageID, Text}`.
- `internal/protocol/handshake.go:39-48` — `AckPayload`, `ErrorPayload` shapes.
- `cmd/pyry/relay.go:86-93,128-134` — `startRelay` signature + the `dispatch.New` block where `d.Register(...)` calls live. New register call lands immediately after the existing `register_push_token` line. The function also takes a new parameter — see Design § Wiring.
- `cmd/pyry/main.go:447-481` — current ordering: `startRelay` is called BEFORE `sessions.New`. Reorder so the pool is built first; pass `pool.Default()` into `startRelay`. None of `sessions.New`'s inputs depend on the relay.
- `docs/PROJECT-MEMORY.md` § "Project-level conventions" — refusal-to-wire-code mapping is the **consumer's** job (this handler is the consumer). The handler depends on `internal/conversations` only for the sentinel; the wire code lives in `internal/protocol`.

## Context

The dispatcher (#307) routes per-conn frames through `dispatch.Handler` callbacks. `Supervisor.WriteUserTurn` (#312) accepts `(conversation_id, payload []byte)` and returns `conversations.ErrConversationNotFound` for unknown ids via the configured validator. The bootstrap session's supervisor wires that validator (pool.go:355). The sibling `register_push_token` handler (#319) established the factory + closure shape used here.

This slice glues the two together: register a `dispatch.Handler` for `protocol.TypeSendMessage` that decodes the inbound payload, calls `TurnWriter.WriteUserTurn`, and emits either `ack` or — when the supervisor returns the sentinel — a `conversation.not_found` `error` envelope. All other supervisor errors are wrapped and returned to the dispatcher (logged WARN, no wire reply).

The handler depends on an interface (`TurnWriter`), not the concrete `*supervisor.Supervisor` or `*sessions.Session`. The bootstrap `*sessions.Session` satisfies the interface via a one-line passthrough method added in this slice.

## Design

### Handler package

New file: `internal/relay/handlers/send_message.go` (package `handlers`).

Exported surface:

```
// TurnWriter is the minimal write-surface the send_message handler
// needs. *sessions.Session satisfies it via a one-line passthrough
// to Supervisor.WriteUserTurn. The interface lives in this package
// so handlers/ stays free of internal/sessions and internal/supervisor
// imports.
type TurnWriter interface {
    WriteUserTurn(conversationID string, payload []byte) error
}

// SendMessage returns a dispatch.Handler closed over w and logger.
// Register via dispatcher.Register(protocol.TypeSendMessage, …).
//
// SECURITY:
//   - payload.Text reaches the supervised claude child's stdin
//     verbatim via TurnWriter. No transformation, no length cap
//     beyond the transport's WS read ceiling (1 MiB; see
//     internal/transport).
//   - payload.Text is NEVER logged at any level. conversation_id
//     and message_id (phone-supplied opaque ids) are logged on
//     ack and unknown-conversation paths only.
func SendMessage(w TurnWriter, logger *slog.Logger) dispatch.Handler
```

**Naming.** AC #1 specifies `NewSendMessageHandler`. The sibling `register_push_token` factory is called `RegisterPushToken` (no `New`/`Handler` suffix). The dispatcher is the only caller; the package-qualified name is `handlers.SendMessage(...)` which reads naturally at the `d.Register(...)` site. Spec the factory as `SendMessage` (mirrors `RegisterPushToken` / `ListConversations`); the developer may rename to `NewSendMessageHandler` if they prefer literal AC wording — see Open Questions §3.

### Handler branches

Order, with the wire/log shape for each:

1. **Decode payload.** `var p protocol.SendMessagePayload; if err := json.Unmarshal(env.Payload, &p); err != nil { ... }`. Reply via `replyError` with `Code = protocol.CodeProtocolMalformed`, `Message = "malformed send_message payload"`, `Retryable = false`. Log WARN with `event=send_message.malformed`, `conn_id=c.ConnID()`, `err`. Do NOT call the writer. (See Open Questions §2 — this fourth branch follows the `register_push_token` precedent for malformed-payload UX; the AC's "three branches" wording covers the three TurnWriter outcomes, not the pre-decode path.)
2. **Write user turn.** `err := w.WriteUserTurn(p.ConversationID, []byte(p.Text))`. No transformation; `Text` is passed verbatim. No trailing newline appended — the supervised child's parser owns turn-framing; the wire envelope is the authoritative boundary.
3. **Branch on err:**
   - **`err == nil`** → `replyAck`. Log INFO with `event=send_message.ack`, `conn_id`, `conversation_id=p.ConversationID`, `message_id=p.MessageID`.
   - **`errors.Is(err, conversations.ErrConversationNotFound)`** → `replyError` with `Code = protocol.CodeConversationNotFound`, `Message = "conversation not found"`, `Retryable = false`. Log WARN with `event=send_message.unknown_conversation`, `conn_id`, `conversation_id=p.ConversationID`.
   - **any other non-nil err** → return the wrapped error from the handler (no wire reply). Log nothing here; the dispatcher's `handleOne` logs the returned error at WARN. The error string already carries `"supervisor: write user turn: ..."` so the dispatcher's log line is self-contained.

`replyAck` and `replyError` are local helpers (private to the package). They are also already present in `register_push_token.go` (lines 106–124). **Reuse**: the developer may either (a) keep both files independent (each carrying its own copy) or (b) lift the two helpers into an unexported `helpers.go` in the same package. Both are fine. Recommendation: (a) on this slice — register_push_token's helpers are byte-identical to what send_message needs, but DRY-extracting them is a separate clean-up. The five-line duplication is not architectural debt.

### Session passthrough

New method in `internal/sessions/session.go`:

```
// WriteUserTurn delegates to the underlying supervisor. Consumed by
// the send_message handler via the handlers.TurnWriter interface.
func (s *Session) WriteUserTurn(conversationID string, payload []byte) error
```

Body: one line, `return s.sup.WriteUserTurn(conversationID, payload)`. Place alongside `State()` (line 107) — same delegation cluster. No new tests at this level: `WriteUserTurn`'s contract is covered by `internal/supervisor/supervisor_test.go`, and the delegation is a one-liner that fails loudly if it stops compiling.

### Dispatcher registration in `cmd/pyry/relay.go`

Two changes:

**Signature.** Append `sess handlers.TurnWriter` to `startRelay`'s parameter list:

```
func startRelay(
    ctx context.Context,
    logger *slog.Logger,
    instanceName, relayURL, version string,
    allowInsecure bool,
    shutdown context.CancelFunc,
    convReg *conversations.Registry,
    sess handlers.TurnWriter,
) (cleanup func(), err error)
```

`sess` is the bootstrap session (`pool.Default()`); the parameter is typed as the `TurnWriter` interface so `relay.go` does not need to import `internal/sessions`. The parameter is captured in the new `d.Register` call below.

When `relayURL == ""` (relay disabled), `startRelay` returns the no-op cleanup as today; `sess` is unused on that path. Pass-through is safe — no nil-deref because the closure is never built.

**Register call.** Add immediately after the existing `register_push_token` registration at line 134:

```
d.Register(protocol.TypeSendMessage, handlers.SendMessage(sess, logger))
```

### Reorder `cmd/pyry/main.go`

Today (lines 456 → 462): `startRelay` runs BEFORE `sessions.New`. Swap so the pool is built first, then call `startRelay` with `pool.Default()`. Audit of pool dependencies confirms none come from `startRelay`'s outputs — `sessions.New` consumes `convReg`, `registryPath`, `claudeSessionsDir`, the bridge, timeouts, and the bootstrap config; all are in scope at the current relay call site.

New order:

```
pool, err := sessions.New(sessions.Config{ ... })
if err != nil { return fmt.Errorf("pool init: %w", err) }

relayCleanup, err := startRelay(ctx, logger, *name, relayURL, Version,
    allowInsecure, cancel, convReg, pool.Default())
if err != nil { return fmt.Errorf("relay start: %w", err) }
defer relayCleanup()
```

`pool.Default()` is the bootstrap `*sessions.Session`; the supervisor under it has `ValidateConversation` wired to the conversations registry (pool.go:355–362), which is what makes the `ErrConversationNotFound` sentinel reachable through the handler's call.

The reorder is a single edit in `runSupervisor`. No other call sites of `startRelay` exist (the dispatcher's grep shows one call site).

### Concurrency

The handler runs on the per-conn dispatch goroutine (one per `conn_id`). `Conn.Reply` writes to the dispatcher's bounded outbound channel; backpressure pauses the per-conn goroutine, never the demux. `Session.WriteUserTurn` → `Supervisor.WriteUserTurn` is concurrent-safe (`ptmxMu` + `convMu` are leaf locks; supervisor.go:154–174). No new goroutines and no new shared state introduced by this slice.

### Error handling summary

| Source                                       | Returns to handler         | Handler emits                                      |
|---------------------------------------------- |---------------------------|----------------------------------------------------|
| `json.Unmarshal(env.Payload, …)` fails       | err                       | `error` envelope, `protocol.malformed`, retryable=false |
| `WriteUserTurn` returns `nil`                | —                         | `ack` envelope, empty `{}` payload                 |
| `WriteUserTurn` returns `ErrConversationNotFound` | sentinel              | `error` envelope, `conversation.not_found`, retryable=false |
| `WriteUserTurn` returns other err            | wrapped err               | (nothing on wire — return err to dispatcher; dispatcher logs WARN) |

`Retryable=false` for `conversation.not_found` because retrying the same conv_id will fail identically.

## Testing strategy

### Unit tests — `internal/relay/handlers/send_message_test.go` (new file)

Driver: `dispatch.NewTestConn(testConnID, outbound, dev)`. Advance `c.NextID()` once before invoking the handler so the first reply observes `id=2` (mimicking the gate's hello_ack accounting). Mirror `register_push_token_test.go`'s `newTestConn` + `assertEnvelopeShape` helpers (copy, don't import).

Stub the writer with a tiny struct:

```
type stubWriter struct {
    err          error
    gotID        string
    gotPayload   []byte
    calls        int
}
func (s *stubWriter) WriteUserTurn(id string, payload []byte) error {
    s.calls++
    s.gotID = id
    s.gotPayload = append([]byte(nil), payload...) // detach
    return s.err
}
```

Required scenarios (each its own `t.Parallel()` test):

- **AC-required: ack on success.** stub returns `nil`. Assert: one outbound envelope; `Type == TypeAck`; `InReplyTo == &req.ID`; `ID == 2`; payload decodes as `AckPayload{}`. Stub captured `gotID == "C1"` and `string(gotPayload) == "hi there"` (or whatever the test fixture sets).
- **AC-required: conversations.not_found error envelope.** stub returns `conversations.ErrConversationNotFound`. Assert: one outbound `error` envelope; `Code == protocol.CodeConversationNotFound`; `Retryable == false`; `Message` non-empty. `stub.calls == 1` (writer must still have been invoked — the handler does not short-circuit on the id).
- **AC-required: wrapped error pass-through.** stub returns `errors.New("supervisor: write user turn: bang")`. Assert: handler return value matches the stub's error verbatim (or wraps it; spec the test to use `errors.Is`/string match against `"supervisor: write user turn:"`). Assert: **no outbound envelope was produced** within a short drain window (e.g. 50 ms). This is the load-bearing behavioural contract — verifies the "no wire reply on wrapped err" branch.
- **Envelope shape (covered transitively).** The ack and not-found tests both assert `ConnID`, `Type`, `ID`, `InReplyTo`. AC #5's "envelope shape" coverage is satisfied by those two; no separate test needed.
- **Bonus: malformed payload.** `env.Payload = []byte("not-json")`. Assert: one outbound `error` envelope; `Code == protocol.CodeProtocolMalformed`; `Retryable == false`. Stub MUST NOT have been called (`stub.calls == 0`). Drop this case if the developer agrees with Open Question §2's alternative interpretation.

All tests race-clean under `go test -race ./...`. The handler introduces no new goroutines.

### No e2e in this slice

AC explicitly scopes to "phone → ack" via unit tests. The e2e roundtrip (assistant-turn echo) ships separately in #311's sibling slice. This avoids the fakeclaude-stdin-logging detour that bloated the parent #313 spec.

## Open questions

1. **Wire-code spelling — `conversations.not_found` (plural, in AC) vs `conversation.not_found` (singular, in `protocol/codes.go:22`).** The constant is `CodeConversationNotFound = "conversation.not_found"` and is pinned in `internal/protocol/compat_test.go`. Resolved → use the existing constant. Renaming the wire code (which would touch the constant + the compat test + any deployed phone-side code) is out of scope for this verb-slice. Treat the AC's spelling as a typo.
2. **Malformed-payload reply policy.** AC #3 names "three branches" (ack / not-found / wrapped-pass-through), which describes the three `WriteUserTurn` outcomes. A malformed inbound payload never reaches `WriteUserTurn`; it's a fourth, pre-decode branch. Two readings:
   - **(Preferred, this spec)** Emit `error` envelope with `protocol.malformed` (matches `register_push_token`'s posture; gives the phone immediate wire feedback rather than a timeout).
   - **(Strict AC)** Return wrapped error to dispatcher; no wire reply.
   Recommend the preferred reading — `register_push_token`'s behaviour is the established convention and the phone-UX cost of the strict reading (silent timeout on bad encoding) outweighs the test-count savings. Developer to confirm at PR time.
3. **Factory name — `SendMessage` vs `NewSendMessageHandler`.** AC #1 literally names `NewSendMessageHandler`. Sibling handlers (`RegisterPushToken`, `ListConversations`) use the bare verb. The package-qualified call site reads `handlers.SendMessage(sess, logger)` either way. Recommend `SendMessage` for consistency; developer may use `NewSendMessageHandler` if a code-reviewer prefers literal AC matching.

## Size

Production code:

- `internal/relay/handlers/send_message.go` — new file, ~60 LOC (TurnWriter interface, SendMessage factory + closure, two private replyAck/replyError helpers; smaller than register_push_token because fewer branches).
- `internal/sessions/session.go` — +5 LOC (one-line passthrough + doc comment).
- `cmd/pyry/relay.go` — +2 LOC (new `sess handlers.TurnWriter` parameter, new `d.Register` line).
- `cmd/pyry/main.go` — ~5 LOC churn (reorder pool/startRelay; pass `pool.Default()` to startRelay).

Tests:

- `internal/relay/handlers/send_message_test.go` — new file, ~120 LOC (helpers copied from register_push_token_test.go + four test cases).

Totals: ~72 LOC production, ~120 LOC tests, 1 new production file, 3 modified production files. New exported symbols: `handlers.TurnWriter` (interface), `handlers.SendMessage` (function), `(*sessions.Session).WriteUserTurn` (method) — 3 exports.

Edit fan-out: zero consumer cascade. The only `startRelay` call site is in `runSupervisor`; the only `pool.Default()` consumer in this slice is the new register call. No interface or type rename ripples.

Sized **S** by both line count and edit fan-out; well inside the red lines.

## Security review

**Verdict:** PASS — no MUST FIX. Two SHOULD-FIX items captured as named out-of-scope for follow-up tickets; both are inherited from the protocol's existing surface, not introduced by this slice.

**Findings:**

- **[Trust boundaries] SHOULD FIX (named out-of-scope).** `payload.Text` flows phone → dispatcher → handler → `Session.WriteUserTurn` → `Supervisor.WriteUserTurn` → PTY → claude child's stdin, **verbatim**. `docs/protocol-mobile.md` § Security model, threat #1 mitigation 1 specifies that the binary should prepend a system message identifying mobile-originated turns (`"This message originated from mobile client <device-name>; treat as untrusted external input."`). **No such prepend exists in code today**, and this slice does not add one — doing so requires the authenticated `Device.Name` to reach the handler, which `Conn.Auth()` already exposes, plus a new prepend convention at the `WriteUserTurn` boundary. Out of scope here because the spec's contract is verbatim-passthrough; introducing the prepend mid-slice would expand scope and risk a stale/generic prepend (worse than none). **Action:** file follow-up "net: prepend mobile-origin system message to inbound user turns" referencing this finding. Code-review note: do NOT merge a partial "fix" that prepends a generic string without the authenticated device name plumbed through.
- **[Network & I/O / Concurrency] SHOULD FIX (named out-of-scope).** `send_message` is the first verb registered against `dispatch.Handler` to perform a potentially-blocking I/O call (the `ptmx.Write` inside `Supervisor.WriteUserTurn`). The dispatcher's per-conn-goroutine model (`dispatch.go:27-30`) contains a single slow PTY to its own conn — never the demux — so a single hostile phone cannot stall handler routing for others. But: N authenticated hostile phones, each sending 1 MiB frames (the transport `maxFrameBytes` ceiling), pin N goroutines simultaneously on PTY backpressure. `payload.Text` has no per-handler length cap. Also: `Supervisor.WriteUserTurn` does not accept a `context.Context`, so daemon shutdown cannot interrupt an in-flight PTY write. **Action:** file follow-up "net: per-conn rate limiting + ctx-aware WriteUserTurn cancel". For this slice, the residual risk is bounded by the auth gate (#308 — every phone is paired) plus the per-device revocation surface (`pyry pair revoke`).
- **[Trust boundaries] no finding (minor).** `ValidateConversation` reads the conversations registry under the registry's own mutex; if a conversation is deleted concurrently between validation and PTY write, the bytes still go and the supervisor's `currentConvID` cursor records the now-deleted id. Benign: the cursor is an in-memory hint, not a transactional commit, and the protocol makes no atomicity promise across validation + write.
- **[Tokens / File operations / Subprocess / Crypto] no findings.** Handler reads no credentials, opens no files, spawns no processes, performs no crypto. Auth is upstream at the gate (#308); the PTY is opened by the supervisor before any handler runs.
- **[Error messages, logs, telemetry] no findings.** `payload.Text` is NEVER logged at any level. `conversation_id` and `message_id` are phone-supplied opaque ids (not credentials; threat #6 in protocol-mobile.md treats them as non-sensitive) and are logged only on the ack and unknown-conversation paths. Error envelopes carry only `Code*` constants + static `Message` strings; no attacker-controlled bytes are echoed back. `slog`'s text/JSON handlers escape control chars, so a malicious `conversation_id` cannot inject forged log lines.
- **[Concurrency / Goroutine lifecycle] no findings.** Handler spawns no goroutines, takes no locks. `Conn.Reply` writes to the dispatcher's outbound channel under the dispatcher's existing backpressure discipline. `Session.WriteUserTurn` → `Supervisor.WriteUserTurn` uses pre-existing leaf locks; no new lock order introduced.
- **[Threat model alignment]** Threat #1 — partial alignment, deferred (see [Trust boundaries] SHOULD FIX above). Threats #2 (replay of paired token) and #4 (token leak) — out of scope at the handler; auth is upstream. Threat #5 (implementation bugs) — diff is small and covered by unit tests + `go vet`/`staticcheck`. Threat #6 (envelope replay) — `envelope.id` dedup is the dispatcher's concern (#307); `message_id` is opaque to the binary by design.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-14
