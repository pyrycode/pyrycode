# Spec — `dequeue_message` inbound v2 control handler (#723)

**Ticket:** #723 — feat(bridge): `dequeue_message` handler — drop a queued message by id.
**Phase 3 (epic #597), capstone of the queued-backlog wire side.** Split from #705.
**Size:** S. **Label:** `security-sensitive` (inbound handler mutating daemon queue state from an untrusted phone frame — § Security review below is mandatory and was run on this spec).

## Files to read first

Generated from `codegraph_context` + the reads behind this design. Each entry is turn-1 data for the developer; read these before writing code.

- `internal/relay/v2session.go:1310-1369` — `dispatchAppFrame`: the v2 control-envelope discriminator switch (the interception point). Add the `TypeDequeueMessage` case here, beside `TypeInterrupt` / `TypeModalCancel`.
- `internal/relay/v2session.go:1677-1719` — `handleInterrupt`: **the closest structural template.** It gates on `s.interactive` *first*, then a nil-seam guard, then the one action. `handleDequeueMessage` mirrors this shape exactly. Note its doc comment's parenthetical "(dequeue is ungated, …)" is now stale — see § Design note.
- `internal/relay/v2session.go:1539-1571` — `handleModalCancel`: the "decode-tolerant, fire-and-no-reply, nil-resolver-inert" idiom for an inbound control frame that mutates daemon state via an injected seam. Same never-echo-payload discipline.
- `internal/relay/v2session.go:426-505` — `V2SessionConfig`: where the optional consumer-declared seams live (`Snapshotter`, `KnownConversation`, `ModalResolver`, `Interrupter`). Add the `QueueRemover` field here with the same nil-is-inert doc style.
- `internal/relay/v2session.go:367-424` — the consumer-declared interface block (`ScreenSnapshotter`, `Interrupter`, `ModalResolver`). Add `QueueRemover` here, same "declared in the consumer so internal/relay imports neither internal/msgqueue nor cmd/pyry" rationale.
- `internal/msgqueue/queue.go:218-263` — `Remove(convID string, id uint64) bool` + `notify`. **The engine op is already done.** Read the contract: `false` on unknown-conv / unknown-id / already-delivered / in-flight-head; `notify`(→`OnChange`) fires **only on success**. The `convID` parameter IS the mutation scope — `Remove(A,…)` provably touches only `A`'s FIFO (the security boundary).
- `internal/protocol/messaging.go:200-214` — `DequeueMessagePayload{ConversationID string, QueuedMsgID uint64}`. The wire payload to decode. (There is **no** `DropQueued` type — an earlier draft's name; ignore it.)
- `internal/protocol/codes.go:226` — `TypeDequeueMessage = "dequeue_message"` already exists, documented "intercepted pre-`dispatch.Route`". No protocol change needed.
- `cmd/pyry/relay.go:273-341` — `startRelayV2`: the `V2SessionConfig{…}` literal. Add `QueueRemover: queue`. The `queue` parameter (line 283) is widened from `handlers.Enqueuer` to `*msgqueue.Queue` — see § Wiring.
- `cmd/pyry/relay.go:89-107` + `cmd/pyry/main.go:788-805` — `startRelay`'s `queue` parameter (line 98) is the same widen; `main.go` already passes the concrete `*msgqueue.Queue` (`msgqueue.New`), so the call sites are unchanged.
- `cmd/pyry/queue_state_v2.go` (whole file, ~213 lines) — the #722 producer. **AC-4 is already satisfied by this:** it Run-loops on the `OnChange` hand-off channel and re-broadcasts `queue_state`. The handler does NOT emit `queue_state`; `Remove`'s `notify` drives it automatically.
- `internal/e2e/relay_v2_daemon_test.go:42-130` — the spawned-daemon v2 Noise harness (`driveHandshakeToOpenDaemon`, `waitBinaryHello`, seal via `initSend.Encrypt` / unseal via `initRecv.Decrypt`, `sendNoiseMsg`, `readInnerFrame`). The AC-5 backbone.
- `internal/e2e/relay_two_phone_structured_test.go:425-498` — `driveHandshakeToOpenDaemonInteractive` (interactive-capability handshake; asserts the `hello_ack` grants `interactive`). Reuse directly (same `package e2e`).
- `internal/e2e/harness.go:178-205` + `:371` — `StartIn` supervises `/bin/sleep 99999` as "claude" (never commits a turn ⇒ backlog persists = the deterministic "no live claude") and `seedBoundConversation(t, home, convID, boundSessionID)` (send_message requires a **bound** conversation or it rejects pre-enqueue).
- `internal/relay/handlers/send_message.go:50-58, 116-160` — `Enqueuer` (consumer interface) and the enqueue path: send_message validates the binding via `router.Route`, then `queue.Enqueue(convID, text)`. Confirms the queue is keyed by the **phone-asserted, Route-validated** `conversation_id` — the same id `dequeue_message` carries back.

## Context

ADR 025's mobile remote head lets a phone queue `send_message` turns into a daemon-resident FIFO (`internal/msgqueue`) while claude is busy; the #722 producer pushes a `queue_state` snapshot to interactive phones on every backlog change. This slice closes the loop: it lets the phone **cancel** a queued message before it drains, by wiring the already-designated `dequeue_message` inbound control frame to `msgqueue.Remove`.

Everything this handler depends on has landed: the wire types (`TypeDequeueMessage` / `DequeueMessagePayload`, #705), the engine op (`msgqueue.Remove`), and the `queue_state` producer over the `OnChange` seam (#722). The only genuinely-new surface is **an inbound capability gate** — today `s.interactive` gates *outbound* fan-out only; `dequeue_message` is the second inbound frame (after `interrupt`, #707) whose authorization IS the interactive capability.

## Design

Three production touch points; one new package-exported interface.

### 1. New consumer-declared seam — `internal/relay/v2session.go`

Add beside `Interrupter` / `ModalResolver` (≈ line 384), same "interface defined where consumed" rationale (CODING-STYLE §48):

```go
// QueueRemover drops a not-yet-drained queued message from a conversation's
// inbound backlog by id. *msgqueue.Queue satisfies it. Declared here (consumer
// side) so internal/relay imports neither internal/msgqueue nor cmd/pyry.
// Returns true iff a message was removed; an unknown/foreign conversation_id, an
// unknown or already-delivered id, or the in-flight (draining) head is a safe
// no-op (false). The conversationID arg IS the mutation scope: Remove(A,…)
// provably never touches conversation B's backlog.
type QueueRemover interface {
    Remove(conversationID string, queuedMsgID uint64) bool
}
```

Add the optional field to `V2SessionConfig` (≈ line 504), nil-is-inert doc matching `Interrupter`:

```go
// QueueRemover drops a queued message named by an inbound dequeue_message
// control frame (#723). Optional: nil ⇒ dequeue_message is inert (foreground /
// unwired). Production wires *msgqueue.Queue.
QueueRemover QueueRemover
```

### 2. Interception case — `dispatchAppFrame` switch (≈ line 1334)

One arm, beside `TypeInterrupt`:

```go
case protocol.TypeDequeueMessage:
    m.handleDequeueMessage(s, probeEnv)
    return
```

### 3. The handler — `handleDequeueMessage(s *V2Session, env protocol.Envelope)`

Signature is `(s, env)` — **no ctx** (no cancellable work: `Remove` takes none, there is no `forwardEnvelope`, and `queue_state` convergence is decoupled onto the #722 emitter goroutine). This deliberately mirrors `handleInterrupt`'s deviation from the `(ctx, s, env)` siblings; document it.

Behaviour, in this exact order (order is load-bearing — gate first):

1. **`if !s.interactive { return }`** — AC-3. A non-interactive conn's `dequeue_message` is inert: **no `Remove` call**, no mutation, no panic. This is the new inbound capability gate; read on the Run goroutine, lock-free (single-owner invariant).
2. **`if m.cfg.QueueRemover == nil { <debug-log inert>; return }`** — nil seam (foreground / pre-wire) inert, mirroring `handleInterrupt`'s nil-`Interrupter` guard.
3. **Decode, tolerantly:** `var p protocol.DequeueMessagePayload; _ = json.Unmarshal(env.Payload, &p)`. A decode failure leaves zero-value fields, which `Remove("", 0)` no-ops on. **Never** echo the decode error or any payload bytes back to the phone or into a log (`encoding/json` can quote attacker bytes into its error string — same discipline as `handleRequestSnapshot` / `handleModalCancel`).
4. **`removed := m.cfg.QueueRemover.Remove(p.ConversationID, p.QueuedMsgID)`** — the one-line engine call.
5. **Log only content-free discriminants:** on `removed`, INFO `event=v2.dequeue.removed` with `conn_id`, `conversation_id` (a non-secret routing id), `queued_msg_id`. On `!removed`, DEBUG `event=v2.dequeue.noop` (the AC-2 "success of a valid request" path). The handler never holds the queued *text* (`Remove` takes only an id), so there is nothing text-shaped to leak; still, never log `env.Payload`.
6. **No reply, no broadcast.** `dequeue_message` is fire-and-(automatic-`queue_state`), like `modal_cancel` / `interrupt`. AC-4's convergence is the `OnChange`→#722-producer path that `Remove`'s `notify` fires on success; on a `false` no-op nothing changed, so emitting nothing is correct. The handler MUST NOT call `Push` / `forwardEnvelope` / re-emit `queue_state` directly (AC-4 explicitly: "through the automatic `OnChange` path, not by re-emitting").

> **AC-2 framing:** `false` from `Remove` is **success of a valid request**, not an error. The handler treats unknown-id, already-delivered, and the in-flight head identically: a quiet no-op, never an error reply. The engine already encodes all four no-op cases (`queue.go:244`); the handler adds no second-guessing.

### Design note — reconcile the stale `handleInterrupt` comment

`handleInterrupt`'s doc (v2session.go ≈ line 1694) currently reads "(dequeue is ungated, modal_answer uses the per-device gate, modal_cancel a nonce)". Since #723, `dequeue_message` **does** gate on the interactive capability (it just carries no *per-device* / nonce gate). Update that parenthetical to say so, e.g. "(dequeue_message also gates on the interactive capability since #723 but carries no per-device gate; …)". Leaving it would contradict the new code. Keep the two `if !s.interactive { return }` guards **inline** — a one-line bare-capability check shared by two consumers does not warrant a helper abstraction (CODING-STYLE §50; over-DRY).

## Wiring — `cmd/pyry/relay.go`

The concrete `*msgqueue.Queue` (built at `main.go:788`) already flows to `startRelayV2` as a `handlers.Enqueuer` and satisfies the new `relay.QueueRemover` too (it has `Remove(string, uint64) bool`). Reach it by **widening the parameter type** — the composition root may hold concrete types:

- `startRelay` (line 98) and `startRelayV2` (line 283): `queue handlers.Enqueuer` → `queue *msgqueue.Queue`.
- Add import `"github.com/pyrycode/pyrycode/internal/msgqueue"` to `relay.go`.
- In the `V2SessionConfig{…}` literal (≈ line 337), add `QueueRemover: queue,`.
- `handlers.SendMessage(router, queue, logger)` is unchanged — `*msgqueue.Queue` still satisfies the `Enqueuer` parameter.

**No edit fan-out.** `startRelay`/`startRelayV2` each have exactly one caller (`main.go:805` / `relay.go:150`); `main.go` already passes the concrete `*msgqueue.Queue`. `codegraph`/grep confirm `handlers.Enqueuer` appears only as these two parameter types. The widen is local: 2 signatures + 1 import + 1 config line.

## Concurrency model

No new goroutine, no new lock. `handleDequeueMessage` runs on the manager's single Run dispatch goroutine (reached from `dispatchAppFrame` ← `handleNoiseMsg` ← `Run`), so the `s.interactive` read is lock-free under the package's single-owner invariant — identical to `handleInterrupt`. `msgqueue.Remove` takes `q.mu` internally and fires `notify`(→`queueStateNotify`) **after** releasing it (non-blocking buffered send, `queue.go:252-262`); that send hops to the #722 emitter's own Run goroutine, so the manager's Run goroutine never blocks on the `queue_state` fan-out. The in-flight-head/drain race is already resolved inside `Remove` (the `draining` flag is set/cleared under the same `q.mu` the drain peeks/advances under — `queue.go:238-247`); the handler inherits that guarantee and adds nothing to it.

## Error handling

| Condition | Behaviour |
|---|---|
| Non-interactive conn | No `Remove`, no mutation, no panic, no reply (AC-3). |
| nil `QueueRemover` (unwired) | Inert; DEBUG log; no panic. |
| Malformed `env.Payload` | Tolerated; zero-value fields → `Remove("",0)` no-op; never echo decode error/bytes. |
| Unknown / already-delivered / in-flight-head id | `Remove` → `false`; treated as success; DEBUG `v2.dequeue.noop`; no error reply (AC-2). |
| Foreign/hostile `conversation_id` | Passed verbatim to `Remove`; `Remove`'s `convID`-scoping confines the effect to that one FIFO (which only exists from a Route-validated `send_message`). Cannot touch another conversation's backlog (§ Security review). |
| Successful removal | `Remove` → `true`; INFO `v2.dequeue.removed`; `OnChange` fires → #722 pushes updated `queue_state` (AC-1, AC-4). |

The handler returns no error and owns no rollback: a removal either happened (and `notify` fired) or it didn't (no state touched).

## Security review (security-sensitive — run on this spec; verdict: PASS)

**Trust boundary.** `dequeue_message` is an AEAD-decrypted frame from a paired, Noise-authenticated phone (`handleNoiseMsg` `V2StateOpen` arm, `v2session.go:1273-1292`). `ConversationID` and `QueuedMsgID` are **untrusted phone input**.

**Threat: cross-conversation mutation** ("can a hostile `conversation_id` reorder/drop another conversation's backlog?"). **Mitigated structurally, by deterministic code — not by a stochastic gate.** `msgqueue.Remove(convID, id)` looks up `c := q.convs[convID]` and only ever mutates `c.items`; it never iterates other map entries (`queue.go:224-254`). So `Remove(A, …)` provably cannot touch conversation `B`. A FIFO for `A` exists only because a `send_message` for `A` passed `router.Route` validation at enqueue time (`send_message.go:131-160`). Therefore the worst a hostile id can do is (a) name a conversation with no backlog → `false` no-op, or (b) name one of the **operator's own** real conversations (all paired devices for a daemon share one conversation space — there is no foreign tenant within a daemon) → drop one of the operator's own queued messages. No privilege boundary is crossed.

**Decision — no `KnownConversation`/`Route` gate on `dequeue_message`.** Its siblings validate `conversation_id` for reasons this frame structurally lacks: `request_snapshot` *renders content* (leak risk → must verify the conversation), `send_message` *routes a delivery* (mis-route risk → must verify the binding). `dequeue_message` only mutates the named conversation's **own already-existing** backlog and returns **nothing** — the `convID`-scoping is self-validating. Adding a membership gate would defend an unobserved failure mode (Evidence-Based Fix Selection) **and** risk a false-negative: a conversation can hold a backlog while momentarily absent from the registry snapshot, which would silently drop a legitimate dequeue. The deterministic `Remove` scoping is the belt; a stochastic membership check would be a second cloth of the same weave (Belt-and-Suspenders → different fabric). This is a deliberate, reviewed choice — surfaced here so it is not mistaken for an omission.

**Threat: in-flight cancellation.** A `dequeue_message` naming the draining head must not cancel an in-flight delivery. `Remove` no-ops on `idx == 0 && c.draining` (`queue.go:244`), atomic w.r.t. the drain under `q.mu`. AC-2 covered by the engine; the handler adds nothing.

**Threat: capability bypass.** AC-3 requires the interactive gate. `if !s.interactive { return }` is the **first** statement, before any decode or `Remove`, so a non-interactive conn cannot mutate the queue even with a well-formed payload. Pinned by a unit test (below).

**Information disclosure.** The handler never returns content and never logs `env.Payload` or any queued text (it never holds text). Logs carry only `conn_id`, `conversation_id` (routing id), `queued_msg_id`. Matches the never-echo discipline of every sibling handler and `msgqueue` itself.

**Verdict: PASS.** No revision needed.

## Testing strategy

Stdlib `testing` only, table-driven, `-race` clean. Put unit tests in a **new** file `internal/relay/v2session_dequeue_test.go` (`package relay`, white-box) and the e2e in a **new** file `internal/e2e/relay_v2_dequeue_test.go` (`package e2e`) — new files avoid churn on the large shared test files.

### Unit (white-box, `package relay`) — drive `handleDequeueMessage` directly with a fake `QueueRemover`

Build a `fakeQueueRemover` recording each `(convID, id)` call and returning a programmable bool. Construct a minimal `V2SessionManager` (mirror the existing v2session tests' setup) and a `V2Session{state: V2StateOpen, interactive: …}`. Scenarios:

- **AC-1 success:** interactive session, remover returns `true` → `Remove` called exactly once with the decoded `(conversation_id, queued_msg_id)`; no panic; handler emits nothing on any send path.
- **AC-2 no-op-is-success:** interactive, remover returns `false` → `Remove` still called once with the given args; no panic; **no reply/Push emitted** (assert the manager's outbound is untouched).
- **AC-3 capability gate (the key new-surface assertion):** `interactive: false` → `Remove` **not called at all** (recorder empty); no panic.
- **nil seam inert:** `QueueRemover == nil` → no panic; no call; (optionally) DEBUG logged.
- **Decode tolerance:** `env.Payload` = malformed JSON → no panic; remover called with zero-value fields (or assert tolerated no-op); assert the raw payload bytes never appear in a captured `bytes.Buffer` logger.
- **Interception routing (one case):** a `TypeDequeueMessage` envelope through `dispatchAppFrame` reaches `handleDequeueMessage` (not `dispatch.Route`) — assert via a recording remover that the engine call happened and the dispatch handler table was not consulted.
- **AC-4 indirection:** with a remover whose `Remove` does NOT fire any `OnChange`, the handler itself pushes nothing — pinning "handler does not re-emit `queue_state` directly" (the producer is the only emitter).

### E2E (`package e2e`, build tag `e2e`) — AC-5 capstone

Backbone: the spawned-daemon v2 Noise harness (`relay_v2_daemon_test.go`) + the interactive handshake helper (`driveHandshakeToOpenDaemonInteractive`). Default `StartIn` supervises `/bin/sleep` as "claude" → **no turn ever commits → the backlog persists deterministically** (the ticket's "no live claude").

Scenario (bulleted, not pre-written):

1. Pair a phone; seed a **bound** conversation (`seedBoundConversation`) so `send_message` enqueues instead of rejecting pre-enqueue; start the daemon with `PYRY_MOBILE_V2=1` + insecure relay, sleep-claude (no committing claude).
2. Drive the **interactive** Noise handshake to open; keep the `initSend`/`initRecv` CipherStates.
3. Seal + send **two** `send_message` frames (`msg1`, `msg2`) for the bound conversation; read each `ack`. Observe a `queue_state` push whose `queued` lists both (FIFO order).
4. Seal + send `dequeue_message{conversation_id, queued_msg_id: msg2.id}` (the **non-head** message). Observe an updated `queue_state` whose `queued` is `[msg1]` only — `msg2` removed, order preserved (AC-1, AC-4, AC-5).
5. *(Optional negative, same test)* `dequeue_message{queued_msg_id: msg1.id}` (the in-flight draining head) → **no** further `queue_state` (no-op); assert no error envelope arrives within a short window (AC-2).

> **CRITICAL for the developer — the dequeued message must be the NON-head (`msg2`).** Under "no live claude" the first enqueued message (`msg1`) is the perpetually-draining head and is an **un-removable no-op** (`Remove` → `false`). A test that enqueues one message and dequeues it will see a silent no-op and fail AC-5. Enqueue ≥ 2 and dequeue a non-head. Also: `send_message` requires a bound conversation (`router.Route` must succeed) — seed the binding, or the frames reject with `server.binary_offline` before any enqueue.

## Open questions

- **e2e `queue_state` empty-list rendering.** The #722 producer emits `[]` (not `null`) for an empty backlog (`toQueueStatePayload`, `queue_state_v2.go:172`). AC-5 never reaches empty (msg1 always remains), so this does not bind here — but if a future test dequeues to empty, assert `[]`.
- **No `dequeue` ack.** This slice sends the phone no positive acknowledgement of a successful dequeue beyond the `queue_state` it will receive. That matches `modal_cancel`'s fire-and-broadcast posture and the ticket's AC set. If the mobile client later needs a correlated ack, that is a follow-up wire addition, out of scope here.
