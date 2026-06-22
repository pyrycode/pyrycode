# Spec #704 — per-conversation inbound message queue + drain on idle/turn-end

**Ticket:** #704 — feat(sessions): per-conversation message queue + drain on idle/turn-end
**Epic:** Phase 3 (#597) — interactive: modals, permissions, queue. ADR 025.
**Size:** S. New package `internal/msgqueue`, 1 production file, 3 exported types, 0 consumer call sites (shipped unwired).
**Security:** `security-sensitive` — see § Security review (appended after the spec-stage adversarial pass).

## Files to read first

- `internal/supervisor/supervisor.go:209-259` — `WriteUserTurn(ctx, id, payload) error`: the **shape the injected delivery seam mirrors**. Extract the reliable-delivery contract: `WaitReady` blocks while claude is busy (no error, just a block), commit-confirm, and the sentinels `ErrNoLiveSession` / `ErrTurnNotCommitted`. This is *the* pacing mechanism — the drain needs no separate turn-state detector.
- `internal/relay/handlers/send_message.go:46-202` — the **future wiring consumer** (out of scope here). Extract: the `TurnWriter` / `SessionRouter` consumer-declared seams; the `SECURITY:` block (line 84) — **`payload.Text` is NEVER logged**; the synchronous `sendMessageDeliverTimeout` (30s, line 29) request/response shape this queue’s async enqueue-then-drain *replaces*.
- `internal/eventring/ring.go` (whole file, ~208 LOC) — the **primitive to mirror**: per-conversation `map[string]*convRing`, a per-conversation `uint64` counter starting at 1, `New` panic-on-misconfig, package-doc style, payload-by-reference discipline, and the per-conversation memory-bound rationale (`MaxEventsPerConversation`). This is the outbound sibling; #704 is its inbound counterpart.
- `internal/turnbridge/producer.go:65-165` and `:320-331` — the **`Config` + `New(cfg) (*T, error)` + `Run(ctx) error` rhythm**, the injected-function-seam idiom, the "shipped unwired" pattern (#606 producer, #616 wired it — the explicit template this ticket cites), and the `sleepCtx(ctx, d) bool` ctx-aware backoff helper to copy for the drain’s retry wait.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:104-130` — wire-protocol section. Extract: `send_message` "Queued by the daemon when claude is busy" (line 123); the `{queued_msg_id, text, ts}` record (line 118); `queue_state` / `dequeue_message` are **#705’s**, not this slice’s (lines 118, 126); the § Backpressure note (line 130) bounds the *outbound* delta queue, **not** this inbound backlog — the inbound bound is the open question this spec’s security review resolves.
- `CODING-STYLE.md` § Concurrency + § Testing — channels-for-coordination / mutex-for-state, `context.Context` everywhere, table-driven stdlib-only tests, `go test -race`.

## Context

Today `send_message` delivers **synchronously**: the handler calls `WriteUserTurn`, whose `WaitReady` idle-gate blocks while claude is busy and which fails (bounded by `sendMessageDeliverTimeout`, 30s) if claude stays busy past the cap (#594). So a message typed mid-turn either blocks the per-conn handler goroutine or — on a long turn — fails. Concurrent messages race across handler goroutines with no defined order.

This slice introduces the per-conversation FIFO backlog + serial drain engine that fixes that: inbound messages are **enqueued non-blocking**, and a single drain per conversation delivers them **one at a time, in order**, through the existing #594 reliable-delivery path supplied as an **injected seam** — paced by claude reaching idle / turn-end. It is the **queue mechanism + drain engine only**, shipped self-contained with unit tests and a fake delivery seam (no live claude), the same rhythm as the `turnbridge` producer (#606 shipped unwired, #616 wired).

**Explicitly NOT in this slice** (per the ticket): the wire types `queue_state` / `dequeue_message` and the internal `QueueState` / `DropQueued` reporting/removal types (those are **#705**); and the live wiring into the `send_message` handler + the `cmd/pyry/main.go` constructor (a separate consumer slice — folding it in would trip the "new type **and** a `cmd/pyry/main.go` constructor" always-split and the `send_message` ack-contract change, see § Open questions).

## Design

### Package home — `internal/msgqueue`, sibling of `eventring`

A new flat package `internal/msgqueue` (CODING-STYLE: "flat packages preferred, one package per concern"). It is **not** nested under `internal/sessions`: the inbound backlog is its own concern — the exact mirror of `internal/eventring`, which is the *outbound* per-conversation in-memory store. Naming follows the wire vocabulary (`queued_msg_id`, `queue_state`). No package depends on it yet (unwired); it imports only stdlib (`context`, `log/slog`, `sync`, `time`). It does **not** import `internal/supervisor` — the delivery path arrives as an injected function, keeping the engine unit-testable with a fake.

### Exported surface (3 types)

```go
// DeliverFunc is the injected reliable-delivery seam — the shape of
// supervisor.WriteUserTurn. It MUST block while claude is busy (the WaitReady
// idle-gate) and return nil ONLY on a confirmed commit; that blocking IS the
// drain's turn-end pacing. A non-nil return means the turn was not delivered.
type DeliverFunc func(ctx context.Context, convID string, payload []byte) error

type Config struct {
    Deliver       DeliverFunc   // required; New errors if nil
    RetryInterval time.Duration // 0 ⇒ defaultRetryInterval; poll cadence while claude is unavailable
    Logger        *slog.Logger  // nil ⇒ slog.Default()
}

func New(cfg Config) (*Queue, error)                  // errors if cfg.Deliver == nil
func (q *Queue) Enqueue(convID, text string) uint64   // non-blocking; returns the stable per-conv id (≥1)
func (q *Queue) Run(ctx context.Context) error        // lifecycle; blocks until ctx done, then joins all drains
```

Internal (unexported) shapes:

```go
type queued    struct { id uint64; text string; ts time.Time }   // ADR-025 {queued_msg_id, text, ts}
type convQueue struct { items []queued; nextID uint64; draining bool }

type Queue struct {
    deliver DeliverFunc
    retry   time.Duration
    log     *slog.Logger

    mu      sync.Mutex
    convs   map[string]*convQueue
    ctx     context.Context  // set once by Run; the lifecycle ctx drains bind to
    started bool             // true once Run has set ctx
    wg      sync.WaitGroup   // joins drain goroutines on shutdown
}
```

`New` validates `Deliver != nil` (return error, do not panic — it is a caller-supplied seam, not a programmer-constant like `eventring.New`'s bound), defaults `RetryInterval` and `Logger`. `defaultRetryInterval` is a package const (~`1 * time.Second`), documented as the respawn-bridge poll cadence, a tuning knob not a contract.

### Data flow

```
send_message handler (future, #705/wiring)
        │  q.Enqueue(convID, text)        non-blocking, returns id
        ▼
  ┌──────────────────────────────────────────────┐
  │ Queue (msgqueue)                               │
  │   convs[convID]: FIFO []queued + nextID + draining
  │   one drain goroutine per ACTIVE conversation  │
  └──────────────────────────────────────────────┘
        │  per-conv drain: pop head → deliver(ctx, convID, text)
        ▼
  DeliverFunc  ==  Supervisor.WriteUserTurn (injected; #594 path)
        │  WaitReady (blocks until claude idle) → DeliverPrompt → commit-confirm
        ▼
     live claude child   (one in-flight delivery per conversation, in order)
```

**Why the drain needs no turn-state detector (drain trigger = option (a)).** The ticket offers two drain triggers. Option (a) — a serial drain loop that simply calls the delivery seam per message — is chosen. `WriteUserTurn`'s `WaitReady` gate already blocks while claude is busy and returns only when claude is idle (then commits). So:

- A message enqueued **mid-turn** → the drain pops it and calls `deliver`, which **blocks inside `WaitReady`** until the turn ends, then delivers. "Held until turn ends" falls out for free (AC #3).
- A message enqueued while claude is **idle** → `WaitReady` returns promptly, delivers promptly (AC #3).
- Serial loop ⇒ the next message’s `deliver` is not called until the previous returned (confirmed) ⇒ **never more than one in-flight per conversation** (AC #2).

Option (b) (an explicit `turnevent.TurnEnd` trigger from `turnbridge`) is **rejected**: it would add a second, redundant turn-state source and a cross-package signal subscription for a pacing the seam already encapsulates. (Belt-and-suspenders is not warranted — there is one honest pacing source, the seam itself; adding a second stochastic screen-sourced detector for a JSONL/idle-gated invariant would be different-fabric-for-its-own-sake with no observed failure to defend.)

### Lifecycle: lazy per-conversation drains, joined by `Run`

- **`Enqueue`** (under `mu`): get-or-create `convs[convID]`; assign `id = c.nextID; c.nextID++` (starts at 1); append `{id, text, time.Now()}`. If `started && !c.draining`, set `c.draining = true` and spawn the drain (capturing `q.ctx`). Unlock. Return `id`. Non-blocking; never waits for delivery (AC #1).
- **Per-conversation independence** (AC #1): each conversation gets its **own** drain goroutine, so a conversation whose `deliver` is blocked (claude busy) never blocks or reorders another conversation’s drain.
- **`Run(ctx)`**: under `mu`, set `q.ctx = ctx; q.started = true`, and spawn a drain for any conversation already holding items (covers `Enqueue`-before-`Run` — pending messages simply wait for `Run`, no panic, no lost wakeup). Unlock. Block on `<-ctx.Done()`, then `q.wg.Wait()`, then return `ctx.Err()` (matches `turnbridge.Producer.Run`’s errgroup-friendly shape).

The **drain loop** (contract sketch — the developer writes the body; lock acquisitions elided to one word):

```
drain(ctx, convID):                                   // wg.Add(1) at spawn; defer wg.Done()
  for {
    lock; c := convs[convID]
    if len(c.items) == 0 { c.draining = false; unlock; return }   // empty ⇒ exit; next Enqueue respawns
    head := c.items[0]; unlock                          // peek, don't pop — lossless until confirmed
    err := deliver(ctx, convID, []byte(head.text))      // blocks on WaitReady; ctx is the lifecycle ctx
    if ctx.Err() != nil { lock; c.draining = false; unlock; return }   // shutdown mid-deliver
    if err != nil {                                     // claude unavailable (respawn / wedged / PTY)
      log.Warn(convID, head.id, err)                    // NEVER log head.text (see § Error handling)
      if !sleepCtx(ctx, retry) { lock; c.draining=false; unlock; return }
      continue                                          // retry SAME head — lossless, respawn-survival
    }
    lock; c.items = c.items[1:]; unlock                 // confirmed ⇒ advance (see note on slice growth)
  }
```

The lazy-spawn race is closed by the empty-check and the `draining = false` write happening **under the same lock hold**: a concurrent `Enqueue` either appends before the drain takes the lock (drain sees `len > 0`, keeps going — no lost wakeup) or after the drain released it (`Enqueue` sees `draining == false`, respawns). No interleave loses a message.

`c.items = c.items[1:]` retains the backing array; over a long-lived conversation this slowly grows. The developer should re-slice into a fresh small slice when capacity dwarfs length (or use a head index) — note it, don't over-engineer; queues are expected shallow.

## Concurrency model

- **Goroutines:** one `Run` goroutine (the daemon adds it to its errgroup in the wiring slice) + **one drain goroutine per active conversation**, spawned lazily on `Enqueue`/`Run` and exiting when its FIFO empties. Idle conversations hold no goroutine.
- **Shared state:** a single `sync.Mutex` (`q.mu`) guards `convs`, each `convQueue`, `started`, and `q.ctx`. **Leaf lock** — never held across the `deliver` call (which can block for a whole claude turn) and never nested with any other lock. The drain peeks under the lock, releases it, delivers, then re-acquires to advance. This is the same "release before the seconds-long delivery" discipline `WriteUserTurn` itself uses (supervisor.go:248-250).
- **`q.ctx` visibility:** set once in `Run` under `mu` before any drain is spawned; each drain receives `ctx` as a parameter captured under the lock at spawn — no unsynchronised shared read.
- **Shutdown sequence:** parent ctx cancel → `deliver`’s `WaitReady` returns ctx error (or `sleepCtx` returns false) → each drain sets `draining = false` and returns → `wg.Done()` → `Run`’s `wg.Wait()` unblocks → `Run` returns. No drain goroutine outlives `Run` (clean under `-race`).
- **Goroutine-leak argument:** every drain exits on exactly one of three conditions — empty FIFO, `ctx` cancelled mid-deliver, or `ctx` cancelled mid-retry-sleep. All three are reachable and none depends on claude behaving; a wedged claude surfaces as `WaitReady` blocking, which still unblocks on ctx cancel.

## Error handling

- **`deliver` returns an error** (no live session during a child respawn → `ErrNoLiveSession`; wedged/uncommitted → `ErrTurnNotCommitted`; PTY write error): **retry the same head** after `RetryInterval`, leaving it at the FIFO head. This is **lossless** (AC #4) and is exactly what makes "undelivered messages survive a claude **child** respawn and drain into the new child" (ticket restart-semantics pin): during the ~500ms–30s respawn window `WriteUserTurn` returns `ErrNoLiveSession` immediately; the retry bridges it and delivers once the new child is live. **No per-message delivery deadline** — a message is retried until delivered or the engine shuts down. (Contrast the synchronous handler’s 30s `sendMessageDeliverTimeout`, which exists only because the phone is blocked awaiting an ack; here the enqueue-ack is immediate and delivery is async.)
- **Long but healthy turn:** `WaitReady` *blocks* (it does not error) for the whole turn, so this path does **not** hit the retry branch — the message simply waits, then delivers. The retry branch fires only on genuine delivery failure, not on a busy claude.
- **`payload.Text` is never logged.** The drain’s warn-on-error logs `conversation_id` and the queued message `id` only — never the text (mirrors `send_message.go`’s SECURITY discipline; the text is untrusted phone-originated content bound for claude’s stdin verbatim).
- **No silent drop.** Every message is either delivered (head advances) or still queued (retry / awaiting drain). The only loss boundary is a **full daemon-process restart** (in-memory backlog gone) — out of scope, same boundary as `eventring`; reconnect/resync covers it. No on-disk persistence in this slice.

## Testing strategy

Same-package `queue_test.go`, table/scenario-driven, stdlib `testing` only, `-race`-clean. The fake `DeliverFunc` is a test double that records call order + payloads and exposes a **gate** (a channel the test releases) to simulate "claude busy → turn ends", plus an **in-flight counter** that fails the test if it ever exceeds 1 for a conversation. Scenarios (RED → GREEN, no live claude):

- **Enqueue-during-in-flight-turn → ordered, one-at-a-time drain on turn-end (AC #2, #3, #5):** fake `deliver` blocks on its gate (simulating an in-flight turn). Enqueue `m1, m2, m3`. Assert `deliver` is called for `m1` only and `m2/m3` remain queued (one in-flight). Release the gate per message; assert delivery order == enqueue order and the in-flight counter never exceeds 1.
- **Empty-queue no-op (AC #4):** `Run` with no `Enqueue`; assert zero `deliver` calls and a clean `Run` return on ctx cancel.
- **Per-conversation independence (AC #1):** `convA`’s fake `deliver` blocks on its gate; `convB`’s succeeds. Assert `convB`’s messages all drain while `convA` is still blocked (one conversation’s backlog does not block another’s).
- **Idle-drains-promptly (AC #3):** with a non-blocking fake, an `Enqueue` is delivered without any external turn-end signal.
- **Lossless retry / respawn-survival (AC #4 + restart pin):** fake `deliver` returns `errFake` the first N calls then `nil`; assert the message is eventually delivered exactly once-to-success, never dropped, order preserved across the retries. Use a short `RetryInterval` in the test config.
- **Stable, independent ids (AC #1):** `Enqueue` returns monotonically increasing ids starting at 1 per conversation; two conversations have independent counters.
- **Clean shutdown / no leak:** cancel ctx while a drain is blocked in `deliver`; assert `Run` returns and (optionally) `runtime.NumGoroutine` settles — the fake’s `deliver` honors its ctx so `WaitReady`’s real cancellation behaviour is faithfully simulated.

## Open questions

- **Inbound bound / backpressure (the security-sensitive open question).** Resolved in § Security review: **deferred** (evidence-based), with the exact cheap deterministic mitigation pre-specified for the wiring slice. The engine’s single chokepoint (`Enqueue`) is where a future bound drops in.
- **`send_message` ack-contract change (owned by the wiring slice, #705/wiring).** Wiring this engine in changes the `send_message` ack from "delivered" to "accepted/queued" — delivery becomes async. ADR 025 line 123 already frames `send_message` as "queued by the daemon when claude is busy," so this is consistent, but the handler change + the `cmd/pyry/main.go` constructor + the reconciliation of the now-async ack is deliberately a **separate** slice (it trips the new-type-plus-`main.go`-constructor always-split). This spec does not touch `send_message.go`.
- **`RetryInterval` tuning.** Default ~1s is a starting point, not load-tested; the respawn window is the thing it bridges. One-line edit if it needs tuning.
- **Daemon-process-restart durability.** Out of scope (same in-memory boundary as `eventring`); resync covers it. No on-disk persistence here.

## Security review

**Verdict:** PASS

This pass was run adversarially against the spec above, assuming the spec has holes. The surface: the queue buffers untrusted, phone-originated `send_message` text and releases it into the live claude session; its ordering, loss-prevention, drain-pacing, and bounding are inbound message-dispatch policy on an internet-exposed surface.

**Findings:**

- **[Trust boundaries]** No MUST-FIX. The untrusted datum is `text` (phone-originated, reaches claude stdin verbatim). The engine treats `text` as **opaque transit bytes**: it is stored, never inspected, parsed, or used in a control decision, and is converted to `[]byte` only at the `deliver` call. The trust boundary is **not** in this package — `convID` validation/resolution is the caller’s job (the `SessionRouter`/`ValidateConversation` upstream in `send_message.go`, which rejects unknown/unbound conversations before any write). The engine uses `convID` solely as a map key; a hostile `convID` can at worst create an isolated FIFO that never drains to a real session (no cross-conversation leakage — per-conversation maps + per-conversation drains are isolated by construction). Documented: the engine **does not** validate `convID`; the wiring slice MUST keep resolution upstream of `Enqueue` (it already is). No type-system signal is added, matching the existing `WriteUserTurn(ctx, id, payload)` convention where `id` is pre-validated by the caller.
- **[Error messages, logs, telemetry]** No MUST-FIX, and a hard spec constraint: **`text` is MUST-NOT-log at any level.** The drain’s only log (warn-on-delivery-error) carries `conversation_id` + the queued `id` (a uint64 counter) only. This mirrors `send_message.go:84`’s SECURITY block ("`payload.Text` is NEVER logged"). The queued `id` is a non-secret per-conversation sequence number; logging it leaks nothing. No tokens, secrets, or payload bytes touch logs or error strings (the engine handles no credentials).
- **[Concurrency]** No MUST-FIX. Single leaf mutex, never held across the blocking `deliver`, never nested — no lock-ordering hazard. The lazy-spawn / exit-on-empty race is closed under a single lock hold (empty-check + `draining=false` atomic), argued in § Design. TOCTOU on the FIFO head: the drain **peeks** under the lock and only advances (`items[1:]`) **after** a confirmed commit, under the lock again — a concurrent `Enqueue` can only append (FIFO tail), never mutate the head, so the peeked head cannot be swapped out from under the in-flight delivery. Shutdown is deterministic: every drain exits on ctx-cancel via `WaitReady`/`sleepCtx`, joined by `Run`’s `WaitGroup` — no goroutine leak (argued per-exit-condition in § Concurrency). Mid-delivery process signal: the head stays queued (peek-not-pop), so a crash loses only in-memory state (the accepted daemon-restart boundary), never leaves a half-delivered durable artifact.
- **[Network & I/O — resource exhaustion / the flagged inbound bound]** SHOULD-FIX, **deferred (evidence-based)** — not a MUST-FIX gating this slice. The vector: a phone flooding `send_message` while claude is persistently busy/wedged grows `convs[convID].items` without bound (each message ≤ the transport’s 1 MiB WS read ceiling; see `send_message.go:87`), an in-memory DoS. **Mitigating context that lowers severity to SHOULD-FIX:** (1) `send_message` sits **behind the per-conn auth gate** (`devices.Registry` token validation, the first-frame gate in `internal/dispatch` / `internal/relay/auth.go`) — the flooder must be a **paired, authenticated device**, not an anonymous internet peer; (2) this is a **self-hosted, single-operator tool** (ADR 025 amendment 2026-06-22) — the realistic actor is the operator’s own buggy/compromised phone, not a third party; (3) **no deployed users; the failure has not been observed** (the ticket’s explicit framing and the pipeline’s Evidence-Based Fix Selection principle: do not ship a defense for an unobserved failure mode); (4) the drain’s continuous retry actively *shrinks* the backlog whenever claude accepts a turn, so accumulation requires a *sustained* busy/wedged claude concurrent with a flood. **Why not bound now:** a bound on the *inbound* queue cannot drop (that would violate the lossless AC #4) — it must **reject** new enqueues past a cap and surface a retryable wire error, which is `send_message` ack **policy** that belongs with the wire types (#705’s `queue_state`/`dequeue_message`) and the live `send_message` handler change (the wiring slice), not with this unwired primitive. Building enforcement here would impose an unproven policy on speculation and pre-empt the wire design. **Pre-specified cheap deterministic mitigation for the wiring slice (so the deferral is concrete, not hand-waving):** the single insertion point is `Enqueue` — add a `MaxQueuedPerConversation int` (0 ⇒ unbounded) to `Config`, change `Enqueue`’s signature to `(uint64, error)`, and return a new `ErrQueueFull` sentinel when `len(c.items) >= cap` **before** appending (reject, never drop — preserves lossless). The handler maps `ErrQueueFull` to a retryable error envelope. Cost: ~one comparison + one sentinel + one test. There are **zero** call sites today (unwired), so deferring the signature costs no churn — the first caller (the wiring slice) adopts whatever signature exists then. Tracked as the inbound-bound decision for the `send_message`-wiring / #705 work.
- **[Tokens, secrets, credentials]** N/A by design — the engine handles no tokens, keys, or credentials. The only data it holds is opaque message text + a non-secret counter id.
- **[File operations]** N/A by design — the engine is purely in-memory (the restart pin and § Error handling explicitly rule out on-disk persistence in this slice). No paths, no file modes, no TOCTOU surface.
- **[Subprocess / external command execution]** N/A by design — the engine spawns no subprocess and builds no command. Delivery to the claude child is entirely behind the injected `DeliverFunc` (the supervisor owns the PTY); the engine never touches `exec`.
- **[Cryptographic primitives]** N/A by design — no randomness, no comparison-to-secret, no crypto. The queued `id` is a plain monotonic counter, not a security token (it need not be unguessable; it is a per-conversation sequence number, not a capability).
- **[Threat model alignment]** ADR 025 § Security model concerns (remote-permission gating, modal nonces, replay) are **not** introduced by this slice — `send_message` is not a permission-class action and carries no nonce/idempotency contract today; double-send = double-delivery, identical to the current synchronous path (not a regression). The one ADR-relevant invariant this slice must not break — "untrusted phone input is held and released into the agent in order, losslessly, one at a time" — is exactly what the FIFO + serial drain + peek-not-pop guarantees. Modal/permission threats remain owned by their own #597 children.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-22
