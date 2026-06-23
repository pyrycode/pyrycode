# Spec #721 — route `send_message` through the inbound queue (daemon wiring)

**Ticket:** pyrycode/pyrycode#721 · Phase 3 (epic #597) · split from #705 · **`security-sensitive`**
**Size:** S — construct + run the #704 `msgqueue.Queue`, swap one handler from sync-deliver to enqueue. 3 production files, ~70 net-new production LOC, 1 new exported type, ≤3 call sites.

## Context

`internal/msgqueue.Queue` (#704) shipped the inbound-backlog engine but **unwired**: `msgqueue.New` is never called, nothing constructs the queue, and `send_message` still delivers synchronously via the `Route → Activate → WriteUserTurn` path in `internal/relay/handlers/send_message.go`. While claude is mid-turn the handler blocks the per-conn goroutine inside `WriteUserTurn`'s idle gate, and the turn **fails** (retryable `server.binary_offline`) once `sendMessageDeliverTimeout` (30s, #594) elapses. A phone that types during a long turn either blocks or loses the message.

This slice makes the queue live: construct one `msgqueue.Queue` in the daemon bootstrap, run it under the daemon lifecycle, and swap the `send_message` handler to **enqueue-and-ack**. The drain (one serial goroutine per conversation, #704) delivers the backlog one message at a time through the same reliable `WriteUserTurn` path, paced by claude reaching idle. ADR 025 line 123: `send_message` is "unchanged [at the wire level]. Queued by the daemon when claude is busy."

The single non-obvious design decision: **`sessionRouter.Route` has a side effect** — it stamps the `activeConversation` cursor (`r.active.set`), the #679/#687 follow-active signal that drives the structured turn stream. The synchronous handler called `Route` exactly once per send (at delivery time, which was also send time). With a deferred drain, naively re-resolving via `Route` inside the drain would re-stamp the cursor at *drain* time — moving the follow-active cursor based on drain order rather than phone-interaction order. The design factors a side-effect-free `resolve` core out of `Route` so the drain delivers without touching the cursor.

## Files to read first

- `internal/msgqueue/queue.go:65-191` — `DeliverFunc` contract (must block while busy, return nil only on commit), `Config`, `New` (errors if `Deliver` nil), `Enqueue` (non-blocking, returns stable id). The engine this slice wires.
- `internal/msgqueue/queue.go:269-363` — `Run` (binds lifecycle ctx, spawns drains for pre-existing backlog, joins on ctx.Done) and `drain` (peek-deliver-advance, retry-on-error after `retry`, leave-head-on-ctx-cancel). The behaviour `newInboundDeliver` must satisfy and the shutdown semantics `Run` already owns.
- `internal/relay/handlers/send_message.go` (whole, 203 lines) — the handler to swap. Preserve: the malformed/not_found/binary_offline error mapping, the `payload.Text`-never-logged discipline, the `SessionRouter`/`TurnWriter` interfaces. Remove: `Activate`/`WriteUserTurn` calls, both timeout constants, the delivery-result switch, the `context.Canceled` propagation arms (no blocking call remains).
- `cmd/pyry/main.go:843-985` — `sessionRouter.Route` (the `r.active.set` side effect to factor out), `errNoBoundSession`, `boundSession` (Activate→`Pool.Activate`, WriteUserTurn→session), `activeConversation` (`set`/`watch`/`CurrentConversation`). Read this to see *why* the drain must not stamp.
- `cmd/pyry/main.go:702-806` — `runSupervisor` lifecycle: the daemon `ctx`, the `ctrlDone` goroutine-and-join pattern (the model for running `Queue.Run`), `pool.Run(ctx)` (the blocking main loop), and the `startRelay` call site (line 770) where `router` is built inline and must become a named var.
- `cmd/pyry/relay.go:89-216` (`startRelay`, v1 leg, registration at :168) and `:271-333` (`startRelayV2`, v2 leg, registration at :312) — **both** `handlers.SendMessage(router, logger)` sites. Both thread the new queue param.
- `internal/relay/handlers/send_message_test.go:18-153` — `stubTurnWriter`, `stubSessionRouter`, `routeTo`, `newSendMsgConn`, `sendMsgRequest`, `assertSendMsgEnvelopeShape`. Reuse these; add a stub `Enqueuer`.
- `cmd/pyry/session_router_test.go:11-40` — `newRouterTestPool` + `TestSessionRouter_Route` harness (real pool + convReg). Extend with the `resolve`-vs-`Route` active-stamp case.
- `internal/msgqueue/queue_test.go:13-55` — `fakeDeliver`'s per-conversation gate + `entered`/`completed` channels. Mirror this sleepless busy/idle gate pattern in the cmd/pyry wiring test.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` — line 123 (`send_message` wire-unchanged, queued when busy) and line 130 (§Backpressure bounds the **outbound** push queue, **not** this inbound one — the security gap this spec surfaces).

## Design

### Package structure

No new packages. Three files change:

| File | Change |
|---|---|
| `internal/relay/handlers/send_message.go` | New `Enqueuer` interface (1 method). `SendMessage` signature gains a `queue Enqueuer` param. Body swaps sync-deliver → validate-then-enqueue-then-ack. Both timeout constants and the delivery switch removed. |
| `cmd/pyry/main.go` | Factor `resolve` core out of `sessionRouter.Route`. Add `newInboundDeliver`. In `runSupervisor`: name the `router` var, construct `msgqueue.New`, run `Queue.Run(ctx)` as a joined goroutine, thread `router`+`queue` into `startRelay`. New `inboundActivateTimeout` const. |
| `cmd/pyry/relay.go` | `startRelay` and `startRelayV2` each gain a `queue handlers.Enqueuer` param; forward it to both `handlers.SendMessage` registrations. |

### Key types / contracts

**`handlers.Enqueuer`** (new, consumer-defined in `handlers`, satisfied by `*msgqueue.Queue`):

```go
// Enqueuer is the inbound backlog the send_message handler appends to instead of
// delivering synchronously. *msgqueue.Queue satisfies it. Defined here so
// handlers/ stays free of an internal/msgqueue import (mirrors SessionRouter).
type Enqueuer interface {
    Enqueue(convID, text string) uint64
}
```

`*msgqueue.Queue.Enqueue(convID, text string) uint64` matches verbatim (`queue.go:173`).

**`SendMessage`** signature: `SendMessage(router SessionRouter, queue Enqueuer, logger *slog.Logger) dispatch.Handler`. Behaviour (replaces the two-phase delivery body):

1. Decode `SendMessagePayload` → on error, `protocol.malformed` (unchanged).
2. `_, err := router.Route(p.ConversationID)` — validate the binding is live **before** enqueue, and let `Route`'s `active.set` stamp the cursor (this IS the phone-interaction moment, same as today). The returned writer is **discarded**: the drain re-resolves at delivery time because the binding may change between enqueue and drain. Map `err` exactly as today — `conversations.ErrConversationNotFound` → `conversation.not_found` (not retryable); any other → `server.binary_offline` (retryable). On either, **do not enqueue**.
3. `id := queue.Enqueue(p.ConversationID, p.Text)` — non-blocking append.
4. `replyAck` and log `send_message.enqueued` with `conversation_id`, `message_id`, `queued_msg_id=id` (never `Text`).

**`sessionRouter.resolve`** (new unexported method — the side-effect-free core; `Route` becomes a thin wrapper):

```go
// resolve maps convID to its bound write surface WITHOUT stamping the active
// cursor. The empty-CurrentSessionID guard (#678 AC#4 isolation break) lives
// here, the single resolution authority. Route layers active.set onto it.
func (r sessionRouter) resolve(convID string) (handlers.TurnWriter, error)

func (r sessionRouter) Route(convID string) (handlers.TurnWriter, error) {
    w, err := r.resolve(convID)
    if err != nil { return nil, err }
    r.active.set(convID) // stamp only on success — unchanged from today
    return w, nil
}
```

`resolve` is the current `Route` body minus the `r.active.set` line. No behavioural change to `Route` (`SessionRouter` interface unchanged → zero ripple to #678 tests / the e2e `routeTo` helper / `seedBoundConversation`).

**`newInboundDeliver`** (new unexported func — the `DeliverFunc` seam, built over the stamp-free `resolve`):

```go
// newInboundDeliver builds the msgqueue delivery seam. It re-resolves the bound
// session per attempt (binding may change between enqueue and drain), activates
// it under a bounded budget (a wedged respawn → error → drain retries the head),
// then writes the turn UNBOUNDED — that block IS the drain's turn-end pacing.
func newInboundDeliver(resolve func(string) (handlers.TurnWriter, error)) msgqueue.DeliverFunc
```

Wired as `msgqueue.New(msgqueue.Config{Deliver: newInboundDeliver(router.resolve), Logger: logger})`. Taking the `router.resolve` method value (not the struct) makes the seam unit-testable with a fake resolve. Internals: `resolve(convID)` → on error return it (drain absorbs/retries); `Activate` under `context.WithTimeout(ctx, inboundActivateTimeout)`; `WriteUserTurn(ctx, …)` with the **raw lifecycle ctx** (no deliver timeout — the #594 30s cap is gone; the drain is allowed to block for a whole turn).

### Data flow

```
Phone ─send_message─> SendMessage handler
   ├─ Route(convID)            [validate binding + stamp active cursor]
   │    └─ reject ─> error reply (malformed / not_found / binary_offline)   [SYNC]
   └─ Enqueue(convID, text) ─> replyAck  "accepted into backlog"            [SYNC]

msgqueue.Queue.Run(ctx)                              [one daemon goroutine, joined on shutdown]
   └─ per-conv drain goroutine ── newInboundDeliver(router.resolve):
          resolve(convID)         [NO active stamp]
          Activate(ctx, 30s)      [bounded respawn of an idle-evicted session]
          WriteUserTurn(ctx, …)   [UNBOUNDED — blocks until claude idle+commit]
        err  ─> retry same head after 1s   [ABSORBED: logged warn, no wire reply]
        nil  ─> advance, deliver next
        ctx-cancel ─> leave head queued (in-mem loss boundary), exit
```

Independence across conversations and the serial single-in-flight-per-conversation invariant are the engine's (#704) — this slice does not re-implement them.

## Concurrency model

- **One** `Queue.Run(ctx)` goroutine, spawned in `runSupervisor` via the existing `ctrlDone` pattern: `qDone := make(chan error, 1); go func() { qDone <- queue.Run(ctx) }()`, joined with `<-qDone` after `pool.Run` returns (alongside `<-ctrlDone`). On `ctx` cancel `Run` stops spawning new drains and `wg.Wait`s the in-flight ones (queue.go:285-290) — a clean join, no leak.
- Drain goroutines are owned by the engine. `newInboundDeliver` runs on them and touches only already-concurrency-safe state: `convReg.Get`, `pool.Lookup`/`Activate`, `Session.WriteUserTurn`. **It does not touch `activeConversation`** — the cursor stays single-writer (the routing-path goroutine via `Route`), preserving the #679/#687 invariant.
- Construction order in `runSupervisor`: build `router` (named) → `msgqueue.New(... newInboundDeliver(router.resolve) ...)` → spawn `Queue.Run` goroutine → `startRelay(…, router, …, queue, …)` (registers the handler with the live queue). The queue must exist before the handler is registered; `Run` may start before or after registration (it spawns drains lazily on first enqueue and also sweeps any pre-existing backlog at Run time — no lost wakeup, queue.go:272-275).

## Error handling — the ack contract (AC#3)

**The ack now means "accepted into the backlog," not "delivered/committed."** Pinned table:

**Synchronous (handler, before/at enqueue) — these still produce an error reply:**

| Failure | Wire reply | Retryable | Enqueued? |
|---|---|---|---|
| Payload won't decode | `protocol.malformed` | no | no |
| `Route` → `ErrConversationNotFound` (unknown conversation) | `conversation.not_found` | no | no |
| `Route` → `errNoBoundSession` / `Pool.Lookup` miss (**unbound**, #678) | `server.binary_offline` | yes | no |
| `Route` OK → `Enqueue` | **`ack`** | — | yes |

**Asynchronous (drain, after ack) — absorbed, never replies:**

| Condition | Behaviour |
|---|---|
| `resolve` error at drain time (became unbound/deleted post-ack) | retry head after 1s, `warn` (convID + queued_msg_id + ts only) |
| `Activate` error (respawn wedged) | retry head after 1s |
| `WriteUserTurn` error (no live session / PTY error) | retry head after 1s |
| `WriteUserTurn` nil (committed) | advance, next |
| ctx cancelled (daemon shutdown) | leave head queued (lost on daemon-process restart; resync covers it), exit |

The unbound case is **asymmetric by design**: at enqueue we have a live phone to tell "retry" (synchronous reject, preserves #678's "unbound → error, not bootstrap"); post-ack we have promised delivery, so a transient unbind is held and retried rather than dropped. Document this asymmetry at the handler's `Route`-reject branch.

**Restart semantics (pin, don't build):** the backlog survives a claude **child** respawn (the drain retries the head until the new child is live — `ErrNoLiveSession` is the retry trigger, queue.go:57-62) and drains into it. A full **daemon-process** restart loses the in-memory backlog — same boundary as `eventring`; no on-disk persistence in this slice.

## Testing strategy (AC#4)

All sleepless; mirror `queue_test.go`'s gate + `entered`/`completed` channel pattern.

**`internal/relay/handlers/send_message_test.go`** — handler-level, fake `Enqueuer` (records `(convID, text)` calls, returns a stub id) + existing `stubSessionRouter`:

- ack-on-enqueue: a routable send replies `ack`; `Enqueue` called exactly once with `(sendMsgConvID, sendMsgText)`; no `WriteUserTurn` reached (the writer is discarded).
- unbound rejected before enqueue: `stubSessionRouter{err: errNoBoundSession-shape}` → `binary_offline` reply **and** `Enqueue` call count == 0. Repeat with `conversations.ErrConversationNotFound` → `conversation.not_found` + count 0.
- malformed payload → `protocol.malformed` + `Enqueue` count 0.
- two conversations: two sends to different convIDs each enqueue independently (fake records both `(convID, text)` pairs).
- **Remove** `TestSendMessage_ActivateTimeout_…`, `…_DeliveryFailure_…`, `…_DeliveryCtxCanceled_…` — those branches moved out of the handler into the drain.

**`cmd/pyry`** (new `inbound_deliver_test.go` or extend an existing file) — the live-wiring integration proof, real `msgqueue.Queue` + `newInboundDeliver(fakeResolve)` where `fakeResolve` returns a gating `handlers.TurnWriter` stub (its `WriteUserTurn` blocks on a per-turn gate = busy, returns nil on release = turn-end commit):

- enqueue-while-busy → ordered drain: gate closed, enqueue 3 → assert none delivered; open gate once per turn → assert deliveries arrive **in enqueue order, one at a time** (assert via the stub's `entered`/`completed`, no `maxInflight > 1`).
- idle drains promptly: gate open, enqueue 1 → delivered without further signal.

**`cmd/pyry/session_router_test.go`** — extend the real-pool harness:

- `resolve` does **not** stamp the cursor: a successful `resolve` leaves `active.CurrentConversation()` unchanged; a successful `Route` on the same router sets it. (Guards the #679/#687 invariant against a drain-time re-stamp regression.)

## Open questions

1. **Inbound queue bound / backpressure** — see Security review §6. Unbounded backlog growth from a flooding phone is real but unobserved (no deployed users); surfaced, not built. Recommend PO file a follow-up; the single insertion point is `Queue.Enqueue` (already documented at `queue.go:34-35`).
2. **Drain-time permanent unbind** — if a conversation is deleted while messages are queued, the head retries every 1s for the daemon's life. There is no conversation-delete verb today, so this is currently unreachable; the daemon-restart boundary bounds it. No action this slice; note for whoever adds conversation deletion.

## Security review

**Verdict:** PASS

**Findings:**

- **[1 Trust boundaries]** No MUST-FIX. The untrusted→trusted boundary is unchanged in location: phone-supplied `ConversationID` (lookup key) + `Text` (opaque transit) enter at the `send_message` handler. The routing target (bound session) is still read from the server-stored registry row, never phone-writable. The empty-binding guard (#678 AC#4) that stops an unbound conversation routing into the shared bootstrap moves into `resolve` but is **byte-identical** and remains the single resolution authority — both `Route` (handler validation) and `newInboundDeliver` (drain) go through it, so neither path can bypass it. `Text` stays opaque: stored in the FIFO, converted to `[]byte` only at the `WriteUserTurn` call, never inspected.
- **[6 Network & I/O — resource exhaustion] SHOULD FIX / OUT OF SCOPE.** This wiring makes the **unbounded inbound-queue growth** vector live: a paired phone flooding `send_message` during a long turn grows the per-conversation FIFO without bound (each frame is already ≤1 MiB by the transport WS read cap, but the message *count* is uncapped). ADR 025 §Backpressure (line 130) bounds the *outbound* push queue only. Threat actor is a **trusted-but-malicious/buggy paired device** (not anonymous internet); blast radius is daemon memory; no deployed users and the failure is unobserved. Per the pipeline's evidence-based-fix principle, **surfaced, not pre-built**: the single bound insertion point is `Queue.Enqueue` (`queue.go:34-35`). Recommend a PO follow-up ticket for an inbound bound/backpressure policy (sibling to the outbound bound). Out of scope for #721.
- **[7 Error messages, logs, telemetry]** No findings. `payload.Text` is never logged (handler discipline preserved); the new `send_message.enqueued` event logs `conversation_id`, `message_id`, `queued_msg_id` only. The drain's warn-on-retry logs `conversation_id`, `queued_msg_id`, `queued_at`, `err` — never the queued text (queue.go:339-344, already enforced by the engine). No error reply echoes attacker bytes (the static `msgSendMessageMalformed` string is reused).
- **[8 Concurrency]** No findings. One new `Queue.Run` goroutine, joined on `ctx`-cancel via `qDone` (no leak). Drain goroutines are engine-owned and join through `Run`'s `wg.Wait`. The critical correctness point: the drain delivers via `resolve`, which does **not** touch `activeConversation`, so the cursor stays single-writer (routing-path goroutine only) — the #679/#687 invariant is preserved, and the `resolve`-doesn't-stamp test guards it. No new shared mutable state, no new lock, no lock-ordering change.
- **[2 Tokens / 3 File ops / 4 Subprocess / 5 Crypto]** N/A — this slice adds no token/credential handling, no filesystem path construction (no on-disk persistence), no subprocess argument construction, and no cryptographic primitives. It threads an in-memory engine into an existing handler.
- **[9 Threat model alignment]** The relevant `protocol-mobile.md` threat — a malicious paired phone exhausting daemon resources via the inbound path — is the resource-exhaustion finding above: named, scoped out, with the follow-up owner (PO) and insertion point (`Enqueue`) identified.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-23
