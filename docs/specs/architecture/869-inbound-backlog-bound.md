# Spec #869 — bound the per-conversation inbound backlog; reject-with-retryable on overflow

**Ticket:** #869 — msgqueue: bound the per-conversation inbound backlog and reject-with-retryable on overflow (deferred bound was never ticketed)
**Epic:** Phase 3 (#597) — interactive: modals, permissions, queue. ADR 025.
**Size:** S. Two production files (`internal/msgqueue/queue.go`, `internal/relay/handlers/send_message.go`) + their tests. **Zero new exported types**, **zero consumer-call-site cascade** (the `uint64` return arity is preserved — see § Design / reject signal). `cmd/pyry/main.go` needs **no change** (the new `Config` field is additive and defaults).
**Security:** `security-sensitive` — see § Security review (appended after the spec-stage adversarial pass). This ticket *implements* the exact mitigation #704's security review pre-specified and deliberately deferred (evidence-based) until the live `send_message` wiring (#721/#722/#723) landed. That wiring has shipped, so the deferral is now ready to build.

## Files to read first

- `internal/msgqueue/queue.go:57-63` — `defaultRetryInterval` const: **the exact posture the cap const mirrors** — a package const default that `Config` can override, "a tuning knob, not a contract." The cap follows this posture verbatim.
- `internal/msgqueue/queue.go:85-166` — `Config` + `New`: where `MaxQueuedPerConversation int` slots in and where its `<= 0 ⇒ defaultMaxQueuedPerConversation` fallback goes, mirroring `RetryInterval`'s `<= 0 ⇒ defaultRetryInterval` (lines 151-154). Extract: additive field ⇒ existing callers compile unchanged.
- `internal/msgqueue/queue.go:168-191` — `Enqueue`: **the single insertion point** the package doc names. Extract the current order (get-or-create `c` → assign `id = nextID; nextID++` → append → maybe-spawn → notify → return `id`); the cap check inserts *before* the id-assign/append and returns `0` without bumping `nextID`, without notifying, without spawning a drain.
- `internal/msgqueue/queue.go:193-216` — `Snapshot`: confirms the backlog = `c.items` **including the in-flight (draining) head** (an item leaves only on `advanceLocked`). So `len(c.items)` is the correct — and Snapshot-consistent — basis for the cap comparison. Do **not** try to exclude the in-flight head from the count.
- `internal/msgqueue/queue.go:1-46` (package doc) — the SECURITY block ("`text` … NEVER logged … convID is used solely as a map key") the reject path must keep. The package-doc line "The single insertion point for a future bound is Enqueue" is the thing this ticket fulfills; update it to past tense.
- `internal/relay/handlers/send_message.go:52-59` — the `Enqueuer` interface (consumer-side, so handlers/ stays free of an `internal/msgqueue` import). **Signature is unchanged** (`Enqueue(conversationID, text string) uint64`); its doc gains the `0 ⇒ rejected` contract.
- `internal/relay/handlers/send_message.go:114-166` — the `SendMessage` handler: the enqueue-then-ack tail (lines 155-165) is where the `id == 0` reject branch inserts. Extract the existing reject-path shape (log a structured `send_message.*` event with `conn_id` + `conversation_id`, then `replyError(..., retryable)`) and the SECURITY comment (lines 101-113) — `payload.Text` is NEVER logged.
- `internal/relay/handlers/register_push_token.go:21-25` and `:104-108` — the canonical **reuse target** for the reject reply: `msgBinaryBusy` static-string const + `replyError(ctx, c, env, protocol.CodeServerBinaryBusy, msgBinaryBusy, true)`. Mirror this idiom for a new static message const local to `send_message.go`.
- `internal/protocol/codes.go:17-19` — `CodeServerBinaryOffline` / `CodeServerBinaryBusy = "server.binary_busy"`. The reject **reuses `CodeServerBinaryBusy`** (no new code minted — see § Design / error code).
- `internal/relay/handlers/send_message_test.go:86-99` — the `fakeEnqueuer` double (records calls, returns `nextID++`). Extend it with a one-field "reject" mode so `Enqueue` returns `0`.
- `internal/relay/handlers/send_message_test.go:255-336` — the two existing "rejected-before-enqueue" scenarios (`TestSendMessage_UnknownConversation_…`, `TestSendMessage_NoBoundSession_…`): the exact template for the new cap-reject scenario (assert error envelope code + retryable + `replyAck` NOT sent + no `payload.Text` in logs). Note the difference: the cap-reject asserts `Enqueue` *was* called once and returned `0`, not that it was never called.
- `internal/msgqueue/queue_test.go` — the same-package scenario harness with the recording/gated fake `DeliverFunc`. Reuse it for the "accepted messages still drain in order after a rejection" assertion.
- `docs/specs/architecture/704-inbound-message-queue.md` § Security review, ¶ "Network & I/O — resource exhaustion" — **the design anchor**: the pre-specified `MaxQueuedPerConversation` + named cap const + reject-before-append + retryable-envelope mitigation this ticket implements.

## Context

The inbound backlog is unbounded. `msgqueue.Queue.Enqueue` appends without any cap; the `send_message` handler acks every enqueue. A paired phone with a retry loop or an app bug can grow `convs[convID].items` without limit — worst case exactly when the daemon is least healthy (claude wedged, the drain retrying the head at `defaultRetryInterval` forever while the backlog behind it grows). Each queued message can be up to the transport's 1 MiB frame ceiling — an in-memory DoS on a self-hosted daemon.

The outbound side (`eventring`, `pushQueue`) is already bounded with class-aware drop policies. The inbound side has no equivalent. This ticket closes that asymmetry, implementing the mitigation #704's security review flagged as the security-sensitive open question and deferred until the live wiring landed (it has: #721/#722/#723).

**The bound must reject, never drop.** Inbound turns are *user content*; the mirror of ADR 025's "control never drops" is **"tell the sender."** Overflow rejects the new enqueue and surfaces a *retryable* error so the phone re-issues later. Dropping the new message (silent) or evicting the oldest (drop-oldest) would both violate the losslessness the whole engine is built around (#704 AC #4). This is the different-fabric deterministic safety net (a cheap `len` comparison) mirroring the already-bounded outbound side — not a speculative defense.

## Design

The change is two edits plus their tests. Both edits are additive; neither changes an existing signature.

### 1. `msgqueue` — cap at `Enqueue` (the single insertion point)

**Cap as a `defaultRetryInterval`-posture knob.** Add a package const and a `Config` field, wired exactly like `RetryInterval`:

```go
// defaultMaxQueuedPerConversation caps a single conversation's in-memory inbound
// backlog. A phone flooding send_message while claude is wedged is rejected past
// this many not-yet-delivered messages (reject, never drop). A tuning knob, not a
// contract — same posture as defaultRetryInterval; not load-tested.
const defaultMaxQueuedPerConversation = 100

// Config gains:
//   // MaxQueuedPerConversation caps each conversation's not-yet-delivered
//   // backlog. <= 0 ⇒ defaultMaxQueuedPerConversation. A per-conversation bound,
//   // independent across conversations.
//   MaxQueuedPerConversation int
```

`New` resolves the field the same way it resolves `RetryInterval`: `if cfg.MaxQueuedPerConversation <= 0 { max = defaultMaxQueuedPerConversation }`, stored on a new `Queue.max int` field. Because the fallback lives in `New`, `cmd/pyry/main.go:823`'s `msgqueue.New(Config{Deliver, OnChange, Logger})` leaves the field unset and transparently gets 100 — **no cmd/pyry change**. Tests set `MaxQueuedPerConversation: 2` for cheap, non-vacuous cap firing (the same reason `RetryInterval` is overridable).

**Reject signal — return `0`, preserve `uint64` arity.** `Enqueue`'s contract becomes: returns the assigned id `≥ 1` on success (unchanged), returns **`0`** without appending when the conversation's backlog is at capacity. This is the ticket-endorsed choice: ids are `≥ 1` today, so `0` is an unambiguous, never-valid-as-an-id reject sentinel, and keeping the `uint64` return **avoids the ~45-test-call-site cascade** a `(uint64, error)` change would force (the `Enqueuer` interface and all existing `id := q.Enqueue(...)` sites stay byte-identical). The `0 ⇒ rejected` contract is documented on **both** sides — `msgqueue.Enqueue`'s doc and the handler-side `Enqueuer` interface doc — so no cross-package const is needed and the "handlers/ imports no `internal/msgqueue`" discipline is preserved.

**Reject check placement** (contract sketch — the developer writes the body; lock words elided):

```
Enqueue(convID, text) uint64:
  lock
  c := get-or-create convs[convID]              // brand-new conv: empty, nextID=1
  if q.max > 0 && len(c.items) >= q.max {        // AT capacity — reject
    unlock; return 0                             // no nextID bump, no append, no notify, no drain spawn
  }
  id := c.nextID; c.nextID++
  append {id, text, now}; maybeSpawnDrainLocked(convID, c)
  unlock
  notify(convID); return id
```

Correctness points the developer must preserve:

- **Count basis is `len(c.items)`** — the whole in-memory backlog *including the in-flight (draining) head* (peek-not-pop keeps the head in `items` until `advanceLocked`). This is the memory-DoS surface and is exactly what `Snapshot` reports, so the cap, `Snapshot`, and `queue_state` all agree on "backlog size."
- **Reject is a pure early return**: it must not bump `nextID` (a rejected message consumes no id ⇒ accepted ids stay dense), must not append, must not `notify` (no backlog change), and must not spawn a drain.
- **A brand-new conversation always accepts its first message**: its freshly created `c` has `len(items) == 0 < max` (max ≥ 1 always in practice), so creation-then-check never rejects a first enqueue. No orphan-empty-`convQueue` concern (a rejected enqueue only ever hits an *already-full*, already-present `c`).
- **`q.max <= 0` means unbounded** (guards a caller who explicitly sets 0; production never does — the default is 100). Keep the `q.max > 0 &&` guard so the sentinel path is dead in the unbounded config.
- **`Remove` (dequeue_message) frees a slot naturally**: after a `Remove`, `len(c.items)` shrinks, so a subsequent `Enqueue` succeeds again. No special-casing.

### 2. `send_message` handler — map reject → retryable envelope

The enqueue-then-ack tail gains an `id == 0` branch (contract sketch):

```
id := queue.Enqueue(p.ConversationID, p.Text)
if id == 0 {                                     // backlog full — reject, do NOT ack
  logger.Warn("relay: send_message backlog full",
      "event", "send_message.backlog_full",
      "conn_id", c.ConnID(),
      "conversation_id", p.ConversationID,
      "message_id", p.MessageID)                 // NEVER payload.Text
  return replyError(ctx, c, env, protocol.CodeServerBinaryBusy, msgSendMessageBacklogFull, true)
}
logger.Info("relay: send_message enqueued", ...) // unchanged
return replyAck(ctx, c, env)
```

- **Error code — reuse `CodeServerBinaryBusy`** (`"server.binary_busy"`), not a new `queue.full`. The AC calls for "the existing server-busy retryable shape"; from the phone's side the action is identical to every other retryable reject (re-issue later), and "the server can't accept this turn right now" is exactly what a busy backlog means. Minting a distinct code would add a `codes.go` edit + a `compat_test.go`/wire-doc consideration for marginal telemetry gain, against Simplicity First and the AC's explicit wording. *(Alternative considered and rejected — see § Open questions.)*
- **New static message const** `msgSendMessageBacklogFull` in `send_message.go`, mirroring `register_push_token.go`'s `msgBinaryBusy`. Static string only (e.g. "server busy; backlog full, retry"); the decode-safe discipline — never echo attacker-controlled bytes — is trivially met since the reject text is a constant.
- **Reject log carries `conn_id`, `conversation_id`, `message_id` (phone-supplied opaque id) — never `payload.Text`, never a `queued_msg_id` (there is none; the message was rejected).** This mirrors the handler's existing reject-path logging (the `send_message.no_bound_session` / `send_message.unknown_conversation` events) and honors the SECURITY block.
- **Ordering:** the reject branch is *after* the synchronous binding validation (malformed → `router.Route`) and *at* the enqueue point — a message that fails binding validation is still rejected before any enqueue (unchanged); only a *bound, routable* message that overflows a full backlog hits this new branch.

### Data flow (delta)

```
send_message handler
   │ router.Route(convID)                 validate binding (unchanged)
   │ id := queue.Enqueue(convID, text)
   ▼
 msgqueue.Enqueue
   │ len(c.items) >= max ?  ── yes ──▶ return 0  ──▶ handler: replyError(server.binary_busy, retryable=true); NO ack
   │            └─ no ──▶ append, return id ≥ 1  ──▶ handler: replyAck (unchanged)
```

## Concurrency model

No new goroutines, no new locks, no lifecycle change. The cap check executes under the **existing `q.mu`** hold in `Enqueue`, in the same critical section that already reads/mutates `c.items` and `c.nextID` — so the `len(c.items)` read is race-free by construction and the reject decision is atomic with respect to the drain's peek/advance (both take `q.mu`). The reject path releases `q.mu` and returns without touching `wg`, `ctx`, or any drain. `Snapshot`, `Remove`, `Run`, `drain`, and `maybeSpawnDrainLocked` are untouched.

## Error handling

- **Overflow is the only new failure mode**, and it is deterministic (a `len` comparison), not an error value — surfaced as the `0` sentinel, mapped by the handler to a retryable envelope. No new sentinel error type.
- **Reject preserves the existing backlog exactly** (AC "reject, never drop"): the early return mutates nothing, so the already-queued messages — including the in-flight head — are untouched and continue draining in enqueue order. There is no eviction path.
- **Below the cap, every existing failure/retry/loss path is unchanged**: the drain's retry-same-head, the daemon-restart in-memory loss boundary, and the `payload.Text`-never-logged discipline all carry over verbatim.
- **`payload.Text` MUST-NOT-log** on the reject path (as everywhere): the new warn logs ids and `conn_id` only.

## Testing strategy

Both packages already have the harness; the new tests are extensions, not new files. Table/scenario-driven, stdlib `testing` only, `-race`-clean.

**`internal/msgqueue/queue_test.go`** (construct with `MaxQueuedPerConversation: 2` for cheap firing; no `Run` needed to *fill* the backlog since `Enqueue` is non-blocking and independent of the drain):

- **Cap enforced at Enqueue (AC #1).** Fill conversation A to the cap (2 accepted enqueues → ids 1, 2). A third `Enqueue` returns `0`; below the cap it returned `≥ 1`. Assert the returned ids are `1, 2, 0`.
- **Reject, never drop; oldest not evicted (AC #2, #5).** After the rejected third enqueue, `Snapshot(A)` still returns exactly the two accepted messages (ids 1, 2) in order — the backlog is untouched and the rejected message never appears. Then start `Run` with a recording fake `DeliverFunc`; assert the two accepted messages drain in enqueue order and the rejected one is never delivered.
- **Cap is per conversation (AC #4).** With A filled to the cap and rejecting, an `Enqueue` on conversation B returns `≥ 1` (independent counter, independent FIFO).
- **`nextID` is not consumed by a reject.** After a reject at the head, `Remove` one queued (non-head) message to free a slot, then `Enqueue` again succeeds and its id continues the dense sequence (no gap introduced by the rejected attempt).
- **Unbounded config (`MaxQueuedPerConversation: 0` ⇒ default 100, or an explicit large value).** Existing scenarios that enqueue a handful of messages still behave exactly as today (regression guard that the default path never rejects small backlogs). *(The 45 existing `Enqueue` call sites are untouched and continue to pass unchanged — this is the correctness evidence for the zero-cascade claim.)*

**`internal/relay/handlers/send_message_test.go`** (extend `fakeEnqueuer` with a mode that returns `0`):

- **Handler surfaces a retryable error on reject (AC #3).** Router resolves OK; the fake `Enqueuer` returns `0`. Assert the handler (a) called `Enqueue` exactly once, (b) replied an **error** envelope with `code == protocol.CodeServerBinaryBusy` and `retryable == true`, (c) did **not** send an `ack`, and (d) logged no `payload.Text` (assert via a capturing logger that the reject event carries `conversation_id`/`message_id` but the message text string does not appear in any logged field). Mirror `TestSendMessage_NoBoundSession_RejectedBeforeEnqueue`'s envelope assertions.
- **Below the cap still acks (AC #3, regression).** The existing `TestSendMessage_AckOnEnqueue` already covers the `id ≥ 1 ⇒ ack` path; confirm it still passes unchanged (the fake returns `≥ 1` in its default mode).

## Open questions

- **Distinct `queue.full` wire code — considered, deferred.** A dedicated code (`server.queue_full` or `queue.full`) would give the operator finer telemetry ("backlog full" vs "binary busy"). Rejected for this slice: the AC specifies "the existing server-busy retryable shape," the phone's behavior is identical (retryable re-issue), and a new code costs a `codes.go` + `compat_test.go` + wire-doc edit for no functional gain. If backlog-full telemetry is later wanted, minting the code is a one-line follow-up that does not change this slice's handler branch (only the const it passes to `replyError`).
- **Cap value (100).** A starting point, same posture as `defaultRetryInterval`'s 1s — not load-tested. One-line edit if it needs tuning; overridable per-`Config` without further code change.
- **Global (cross-conversation) memory bound.** Out of scope. This bounds *per conversation*; N conversations can hold up to N×cap. Matches `eventring`'s per-conversation `MaxEventsPerConversation` posture and the ticket's "cap is per conversation" AC. A daemon-wide ceiling, if ever needed, is a separate decision.

## Security review

**Verdict:** PASS

Run adversarially against the spec above, assuming it has holes. The surface: this ticket adds a **reject/drop-policy gate** on the inbound, untrusted, phone-originated `send_message` content surface and changes `send_message` dispatch on the internet-exposed relay. It is precisely the inbound-bound decision #704's security review flagged and deferred (evidence-based) until the live wiring landed — that wiring has shipped, so this is the completion of a labeled, parked mitigation, not a premature or speculative defense.

**Findings:**

- **[Trust boundaries]** No MUST-FIX. The gate decision (`q.max > 0 && len(c.items) >= q.max`) uses only server-owned values — `q.max` (server config, default 100) and `len(c.items)` (server-tracked) — never attacker-controlled bytes. `text` remains opaque transit (stored on accept, never inspected; not touched at all on reject). `convID` is still only a map key; the handler's `router.Route` validates/resolves the binding **before** `Enqueue`, so a hostile `convID` is rejected upstream and, even if it reached `Enqueue`, could at worst fill its **own** isolated per-conversation FIFO to the cap and be rejected — no cross-conversation reach (per-conversation `convQueue` by construction). The gate strengthens the inbound trust boundary; it introduces no new crossing. Constraint pinned: the reject branch must stay downstream of `router.Route` (it is — at the enqueue point).
- **[Error messages, logs, telemetry]** No MUST-FIX; a hard constraint restated. The reject log carries `conn_id`, `conversation_id`, and `message_id` (phone-supplied opaque id) **only** — never `payload.Text`, never a `queued_msg_id` (none is assigned to a rejected message). The error envelope's user-facing message is a **static const** (`msgSendMessageBacklogFull`), never echoing attacker-controlled bytes (mirrors `send_message.go`'s malformed-path discipline and `register_push_token.go`'s `msgBinaryBusy`). This directly guards the "sec branch cloned from a non-sec template silently drops error context or leaks content" failure mode: the reject branch neither drops needed context (it logs conv/message ids + a distinct `send_message.backlog_full` event) nor leaks the text. Verified against the handler's existing reject events.
- **[Concurrency]** No MUST-FIX. The cap check executes under the **existing** `q.mu` hold, in the same critical section that reads/mutates `c.items`/`c.nextID` — no new lock, no new goroutine, no lock-ordering change. Read-then-append is one lock hold, so there is no TOCTOU on `len(c.items)`. The cap is a **hard ceiling**: concurrent `Enqueue`s serialize on `q.mu` (the second sees `len == max` and rejects, so two racers cannot both append past the cap), and a concurrent drain-advance only *reduces* `len` under the same lock — the backlog is never observed above `max` after any `Enqueue` returns. Reject releases `q.mu` and returns without touching `wg`, `ctx`, or any drain.
- **[Network & I/O — resource exhaustion / the flagged inbound bound]** **RESOLVED** — this ticket *is* the mitigation #704 deferred. The flagged vector ("a phone flooding `send_message` while claude is persistently busy/wedged grows `convs[convID].items` without bound, each message ≤ the 1 MiB transport frame ceiling — in-memory DoS") is directly closed: a single conversation's backlog is now hard-bounded at `max` (100) × ≤1 MiB = ≤100 MiB, and overflow is a cheap O(1) `len` comparison + a static reply that stores nothing — so a phone retrying the *rejected* message in a tight loop causes no memory growth and no unbounded work (the reject path is itself DoS-resistant). Two residuals, noted honestly, neither a gate: (1) the bound is **per conversation**, so N conversations hold up to N×cap — a daemon-wide ceiling is out of scope (matches `eventring`'s per-conversation posture; amplifying it requires also flooding the separately-gated `create_conversation` surface, a different vector); (2) the cap is **count-based, not byte-based** — deliberate, matching the ticket's "~100 messages" specification and simplest-deterministic-bound posture, and sufficient because the upstream 1 MiB per-frame ceiling already bounds each element.
- **[Tokens, secrets, credentials]** N/A by design. The gate touches no token, key, or credential. The `0` reject sentinel is a control-flow value, not a capability or guessable id (ids are non-secret per-conversation counters; `0` is simply the never-assigned value).
- **[File operations]** N/A by design. The reject path is purely in-memory (an early return before append) plus a wire reply. No paths, modes, or TOCTOU surface.
- **[Subprocess / external command execution]** N/A by design. A rejected enqueue returns before the append and **never reaches the delivery seam** — nothing is delivered to the claude child, no `exec`, no command construction.
- **[Cryptographic primitives]** N/A by design. No randomness, no comparison-to-secret. The `len >= max` comparison is timing-insensitive (both operands non-secret; nothing leaks via timing).
- **[Threat model alignment]** ADR 025's "control never drops" has as its inbound-user-content mirror **"tell the sender"** — implemented exactly: reject + *retryable* envelope, never silent-drop, never drop-oldest. No nonce/idempotency contract is introduced (`send_message` has none; double-send = double-enqueue as today; a rejected send is retried, so idempotency is unchanged). The invariant this slice must not break — "untrusted phone input is held and released in order, losslessly, one at a time" — is preserved: rejected messages never entered the backlog, so no *accepted* message is ever lost or evicted to make room, and accepted messages still drain FIFO one-at-a-time. A legitimate phone recovers automatically on the retryable reject; a flooding phone is throttled deterministically. This is the different-fabric (deterministic-code) safety net mirroring the already-deterministic outbound bounds (`eventring`/`pushQueue`).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-10
