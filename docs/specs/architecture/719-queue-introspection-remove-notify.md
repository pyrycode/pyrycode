# Spec #719 — msgqueue: queue introspection + remove-by-id + change-notification seam

**Ticket:** [#719](https://github.com/pyrycode/pyrycode/issues/719) — Phase 3 (epic #597), split from #705.
**Size:** S (additive API on one existing package; ~80 production LOC, 2 new exported types, 0 consumers).
**Security-sensitive:** yes — see § Security review (mandatory pass, appended after PASS).

## Files to read first

Turn-1 data load. Read these before writing any code; the design is a thin additive layer on top of them.

- `internal/msgqueue/queue.go:86-114` — `queued{id,text,ts}`, `convQueue{items,nextID,draining}`, `Queue{mu,convs,…}`. The exact fields the new accessors read/mutate; **`draining`** is the in-flight signal AC #2 hinges on.
- `internal/msgqueue/queue.go:140-160` — `Enqueue`: the append-then-`maybeSpawnDrainLocked` shape. You restructure its `defer q.mu.Unlock()` into an explicit unlock + post-unlock `notify` (AC #3 fire site #1).
- `internal/msgqueue/queue.go:211-272` — `drain` + `advanceLocked`: **peek `items[0]` → unlock → deliver → lock → `advanceLocked` (`items = items[1:]`)**. This is the invariant `Remove` must not break: `advanceLocked` blindly drops index 0 assuming it is still the just-delivered head. Fire site #3 (delivery-advance) goes right after the advance unlock.
- `internal/msgqueue/queue_test.go:21-117` — `fakeDeliver` (per-conv `gates` channel to pin a delivery "in flight", `entered`/`completed` channels, in-flight counter that fails if >1) + `recvWithin` + `equalStrings`. **Reuse all of it**; the new tests need the gate to hold the head in flight and a small `OnChange` recorder.
- `internal/eventring/ring.go:140-207` (`After`, `NewestID`) — the **sibling outbound store's** snapshot-under-lock + copy-out idiom (`out := make(...); copy(out, …)`). `Snapshot` mirrors this. Same package shape as msgqueue.
- `docs/knowledge/features/msgqueue-package.md` — the evergreen package reference: drain pacing, the `closed`-flag happens-before, the "leaf lock, never held across `deliver`" discipline, the `text`-never-logged + convID-is-a-key-only security stance. The new API must preserve every one of these.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:118,126` — the consumers that drive the shapes: `queue_state = {conversation_id, queued:[{queued_msg_id, text, ts}]}` and `dequeue_message = {conversation_id, queued_msg_id}`. `QueuedMessage{ID,Text,TS}` is the engine-side projection of that record; `Remove(convID, id)` is `dequeue_message`'s engine op. **No wire types in this slice.**

## Context

`internal/msgqueue` (#704) is write-and-drain only: `Enqueue` (append, returns a stable per-conversation id) and `Run` (serial drain through an injected `DeliverFunc`). There is no way to **observe** the backlog, **remove** a queued message, or **learn when the backlog changes**. The phone-facing `queue_state` / `dequeue_message` surface (#705 siblings) needs all three.

This slice adds them as an **additive API on the existing engine**, unit-tested in isolation, **no consumers wired** — the same rhythm #704 shipped the engine unwired. `Enqueue`/`Run` signatures and the stable-id semantics are untouched; the engine stays usable exactly as #704 left it.

The real cost is the **concurrency reconciliation**, not the data: the change-notification must coexist with the single serial drain goroutine and the queue mutex, and `Remove` must not be able to corrupt the drain's `advanceLocked` assumption. Both are resolved below.

## Design

### Exported surface (2 new types, 2 new methods, 1 new Config field)

```go
// QueuedMessage is the engine-side projection of ADR 025's {queued_msg_id, text, ts}
// record (the producer maps ID -> queued_msg_id). Text is untrusted phone content.
type QueuedMessage struct {
    ID   uint64
    Text string
    TS   time.Time
}

// ChangeFunc is the injected change-notification seam — mirrors the DeliverFunc seam
// style. It is invoked, never holding q.mu, with the conversation id whose backlog
// changed. nil disables notification. MUST NOT block and MUST be safe for concurrent
// invocation (it fires from the Enqueue caller, each drain goroutine, and the Remove
// caller). It carries only convID; the consumer re-reads via Snapshot (edge-triggered).
type ChangeFunc func(convID string)

// Config gains one optional field:
//   OnChange ChangeFunc // nil => notification disabled
```

- `func (q *Queue) Snapshot(convID string) []QueuedMessage` — returns the conversation's not-yet-confirmed-delivered FIFO as an **ordered copy**. Unknown conversation ⇒ `nil` (empty), **not** an error. The returned slice is freshly allocated; strings are immutable — callers cannot mutate engine state through it.
- `func (q *Queue) Remove(convID string, id uint64) bool` — drops a queued, **not-in-flight** message by id from its conversation's FIFO, preserving the surviving order. Returns `true` iff a message was removed. Unknown conversation, unknown id, already-delivered id, or the **in-flight (draining) head** ⇒ `false` no-op (no panic, no reorder).

`New` stores `cfg.OnChange` on the `Queue` (`onChange ChangeFunc` field); a nil `OnChange` is the disabled default, no validation needed (unlike `Deliver`, which stays required).

### `Snapshot` — copy under the lock

Lock, look up `convs[convID]`, `nil` ⇒ return `nil`. Else allocate `out := make([]QueuedMessage, len(c.items))`, project each `queued{id,text,ts}` into a `QueuedMessage{ID,Text,TS}`, return `out`. Same snapshot-under-lock + copy-out idiom as `eventring.After`. The **whole** `items` slice is returned, including the in-flight head: an item is in the snapshot iff it has not been **confirmed** delivered (the head mid-delivery is not-yet-confirmed and stays in `items` until `advanceLocked`), so "not-yet-delivered backlog" maps exactly to "current `items`". The snapshot does **not** flag which entry is in-flight — `Remove` returning `false` is how the #705 handler learns the head is non-removable.

### `Remove` — the in-flight-head no-op is the load-bearing rule

`advanceLocked` does `c.items = c.items[1:]` — it blindly drops **whatever is at index 0** when it runs, on the assumption that index 0 is still the message the drain just delivered (the drain holds a value-copy `head`, not an index). `Enqueue` only ever appends to the tail, so today index 0 is stable across the drain's unlock window. **`Remove` is the first operation that can touch index 0** — and removing the in-flight head would make `advanceLocked` drop the *next* message instead, silently losing an undelivered message. Hence:

```
Remove(convID, id):
  lock
  c = convs[convID]; if c == nil { unlock; return false }
  find i where c.items[i].id == id
    not found:            unlock; return false
    i == 0 && c.draining: unlock; return false      // in-flight head — no-op
    else: remove index i (append(items[:i], items[i+1:]...)); reuse advanceLocked's
          empty/compact hygiene; unlock; notify(convID); return true
```

Why this is exactly correct (pin in the design, assert in tests):

- **`draining` is the precise in-flight signal.** It is set `true` under `q.mu` *before* a drain is spawned and cleared `false` under `q.mu` only when the drain exits (empty FIFO or ctx-cancel). So `draining == true` ⟺ a goroutine owns this conversation and is delivering / about to deliver index 0; `draining == false` ⟺ **no goroutine touches `items`** (the pre-`Run` backlog case, or a fully-drained/cancelled conv). Removing index 0 when `!draining` is therefore safe; removing it when `draining` is the forbidden case.
- **Non-head removal (`i >= 1`) is always safe**, even while draining: the drain only ever reads/writes index 0 (peek + advance), and it holds a value-copy of the head, so shifting `items[i+1:]` left over index `i >= 1` cannot affect the in-flight delivery or the subsequent `advanceLocked`.
- **No data race.** All `items` access — drain peek, `advanceLocked`, `Snapshot`, `Remove` — happens under `q.mu`. The drain's only outside-the-lock work is `deliver(head)`, which reads a value copy. `Remove` never runs concurrently with `advanceLocked` on the same `items` (both hold `q.mu`).

For the practical #705 path (claude busy, head being retried), `draining` is `true`, so a `dequeue_message` targeting the head returns `false` and the handler acks "could not dequeue (in flight)"; any non-head queued message dequeues cleanly. This is precisely the AC-2 contract: `dequeue_message` cannot cancel an in-flight delivery.

`Remove` reuses `advanceLocked`'s tail hygiene (release the backing array at empty, compact when `cap > 2*len`) so a mid-FIFO removal doesn't leave a stale backing array — factor the trailing `switch` out of `advanceLocked` into a small `c.shrinkLocked()` helper that both `advanceLocked` and `Remove` call, or inline the equivalent; developer's call (keep it one helper, don't duplicate the switch).

### Change-notification — fire after unlock, three sites

`OnChange` carries only `convID`; the consumer re-reads current state via `Snapshot`. Edge-triggered, coalescing is the consumer's choice (matches how the #647 reconnect path treats `eventring`). A `notify` helper does the nil-check:

```
func (q *Queue) notify(convID string) { if q.onChange != nil { q.onChange(convID) } }
```

Fire sites — **always after `q.mu` is released**, only on a real change:

1. **Enqueue** — restructure `Enqueue` from `defer q.mu.Unlock()` to an explicit `q.mu.Unlock()` after `maybeSpawnDrainLocked`, then `q.notify(convID)`, then `return id`.
2. **Delivery-advance** — in `drain`, after the `c.advanceLocked(); q.mu.Unlock()` that drops a confirmed-delivered head, call `q.notify(convID)`. (Do **not** fire on the empty-exit path or on a delivery error/retry — those aren't backlog changes.)
3. **Removal** — in `Remove`, after the successful-removal unlock (shown above). No-op removals do **not** fire.

Firing strictly **after unlock** is what AC #3 means by "without holding the queue's internal lock (no re-entrancy deadlock with the drain goroutine)": a consumer callback that re-enters `Snapshot`/`Remove`/`Enqueue` re-acquires the lock cleanly and cannot deadlock against the goroutine that fired it.

## Concurrency model

- **Goroutines unchanged.** No new goroutine. The existing one-`Run`-goroutine + one-drain-per-active-conversation model is untouched. `Snapshot`/`Remove` run on the **caller's** goroutine (the future #705 handler goroutine); `notify` runs on whichever goroutine performed the mutation (Enqueue caller, a drain, or a Remove caller).
- **Single leaf lock preserved.** `q.mu` still guards `convs`, each `convQueue`, `started`, `closed`, `ctx`. `Snapshot` and `Remove` take it the same way `Enqueue` does and release before any `deliver`/`notify`. **`q.mu` is still never held across `deliver` and now also never held across `onChange`** — both are caller-supplied seams that could block or re-enter, so both are called lock-free. No new lock, no lock nesting, no lock-ordering question.
- **In-flight-head safety** is the `draining`-under-lock argument above: `Remove` cannot mutate the index the drain's `advanceLocked` depends on.
- **`onChange` concurrency contract.** Because `notify` fires from multiple goroutines, `OnChange` must be safe for concurrent invocation and must not block (a blocking `OnChange` on the drain path stalls that conversation's drain). Documented on `ChangeFunc`; the consumer (#705) owns its own synchronization. This is the inbound mirror of `eventring` being "the only shared object on the structured path, internally synchronised" — here the seam is the consumer's responsibility, signalled by the doc contract.
- **Shutdown unaffected.** The `closed`-flag happens-before join (#704) is unchanged; the new API adds no goroutine to join. A late `Remove`/`Snapshot` after ctx-cancel is harmless (it just reads/mutates the in-memory FIFO under the lock; no goroutine to coordinate with once drains have exited).

## Error handling

- **No new error returns.** `Snapshot` and `Remove` cannot fail in a way the caller must distinguish: an unknown conversation is an empty snapshot / a `false` no-op (per AC), not an error — matching the "unknown conversation returns an empty snapshot, not an error" requirement and msgqueue's existing non-erroring `Enqueue`.
- **`Remove`'s `bool`** is the only signal: `true` = removed, `false` = no-op (any reason). The reasons (not-found / already-delivered / in-flight head) are deliberately collapsed; #705 acks "dequeued" vs "not dequeued" and does not need to distinguish them (see Open questions).
- **`text` is never logged** by the new code, preserving #704's discipline. `Snapshot` *returns* `text` to the caller by design (`queue_state` carries it to the originating phone) — that is a return value, not a log. The engine adds no new log lines; `Remove`/`Snapshot`/`notify` log nothing.
- **No panics.** Every no-op path returns normally; `Remove` on a `nil` conv, empty FIFO, or absent id all return `false`.

## Testing strategy

`package msgqueue`, stdlib `testing`, `-race`, `t.Parallel()`, channel-synchronised (no sleeps). Reuse `fakeDeliver` (its per-conv `gates` channel pins a delivery in flight; `entered`/`completed` synchronise) and add a tiny mutex-guarded `OnChange` recorder (record convIDs, or a buffered channel of convID). RED → GREEN, no live claude. Existing #704 tests are **not edited** (AC #4 is satisfied by leaving them green with the new API present).

Scenarios (bullet inputs + expected behaviour; developer writes the bodies):

- **Snapshot reflects enqueues in order** (AC #1): enqueue `m1,m2,m3` to a gated conv (so nothing drains); `Snapshot` returns three `QueuedMessage` with ids `1,2,3`, texts/ts in order. `Snapshot` of an unknown conv ⇒ `len == 0`, nil-ish, no panic.
- **Snapshot is a copy** (AC #1): take a snapshot, mutate the returned slice's elements; a second snapshot is unaffected — engine state is not reachable through the returned slice.
- **Remove drops the right non-head entry, order preserved** (AC #2): enqueue `m1,m2,m3` (gated so all three sit queued, head in flight); `Remove(id of m2)` ⇒ `true`; snapshot ⇒ `[m1, m3]` in order.
- **Remove no-ops on the in-flight head** (AC #2): gate a delivery so the head is in flight (`draining==true`, observed via `entered`); `Remove(head id)` ⇒ `false`; snapshot still contains the head; release the gate; the head still delivers exactly once (drain's `advanceLocked` not corrupted) and remaining order is intact.
- **Remove no-ops on unknown id and unknown conv** (AC #2): `Remove("c", 999)` and `Remove("nope", 1)` ⇒ `false`, no panic, backlog unchanged.
- **Remove no-ops on an already-delivered id** (AC #2): let `m1` drain to completion (observed via `completed`), then `Remove(id of m1)` ⇒ `false`.
- **Change seam fires on enqueue, drain-advance, and removal** (AC #3): with `OnChange` recording convIDs — an `Enqueue` records `convID`; a confirmed delivery (gate released) records `convID`; a successful `Remove` records `convID`. A no-op `Remove` records **nothing**. Assert the affected convID each time.
- **Change seam fires without holding the lock** (AC #3): an `OnChange` that re-enters `Snapshot(convID)` (and/or `Remove`) completes without deadlock — proves notify is post-unlock.
- **#704 invariants still hold with the new API present** (AC #4): the existing ordered-drain / lossless-retry / per-conversation-independence / stable-id tests remain green unmodified (this is asserted by *not* touching them; optionally add one combined test that interleaves `Enqueue` + `Snapshot` + `Remove` and confirms drain order + max-in-flight==1 are unchanged).

Run `go test -race ./internal/msgqueue/ -count=10` for the concurrency surface, plus `go test -race ./...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

- **`Remove` reason granularity.** The collapsed `bool` is the simplest contract that satisfies AC #2 and lets #705 ack dequeued-vs-not. If #705's UX needs to distinguish "in-flight (try again)" from "already gone", a richer return (e.g. an enum or a second bool) can be added then — deferred on YAGNI, no observed need. Flag, don't build.
- **Snapshot in-flight flag.** The snapshot deliberately does not mark which entry is in-flight; `Remove`'s `false` is the discriminator. If #705 wants the phone to grey-out the in-flight head proactively, exposing an `InFlight bool` on `QueuedMessage` (true iff index 0 && draining) is a one-field follow-on — deferred, not in this slice.
- **`OnChange` coalescing.** Edge-triggered fan-out can fire several notifications in quick succession (enqueue then near-immediate drain-advance for an idle conv). The consumer (#705) coalesces (re-read current `Snapshot` once per wakeup); the engine does not buffer or debounce. Confirm this matches the #705 producer's expectations when it wires up.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] OUT OF SCOPE → #705.** This slice adds two new boundary crossings for untrusted, phone-originated data: `Snapshot` *returns* the opaque `text` (it will flow out to a phone via `queue_state`), and both `Snapshot` and `Remove` key by **caller-supplied `convID`**. The engine has no session context, so it cannot authorize convID — it extends #704's documented stance ("validating/resolving convID to a real session is the caller's job, upstream of `Enqueue`"). Cross-conversation **confidentiality** (`Snapshot` reading another conv's queued text) and **integrity** (`Remove` mutating another conv's FIFO) are prevented only by the #705 handler binding convID to the requesting phone's authorized conversation — exactly as `send_message` does today. Per-conversation maps give isolation-by-construction *once convID is trusted*; trusting it is #705's job. Named, not engine-fixable.
- **[Trust boundaries] SHOULD FIX (at consumer, #705).** `QueuedMessage.Text` is untrusted transit content. The #705 producer must (a) only emit `queue_state` for an authorized conversation (above), and (b) never log the text — preserving #704's "`text` never logged" discipline across the new exit. The engine documents `Text` as untrusted on the type; the consumer must honour it.
- **[Tokens, secrets] No findings.** No secret material. The `id` is a non-secret per-conversation monotonic counter, scoped by convID; a leaked id cannot act on another conversation (`Remove` requires the matching convID). `Remove` never touches `nextID`, so the stable-id contract (AC #4) is preserved and a removed id is simply never reused.
- **[File operations] No findings — N/A.** Purely in-memory; no paths, no disk, no symlinks.
- **[Subprocess] No findings — N/A.** The new accessors never reach `DeliverFunc`/claude stdin; no exec surface added.
- **[Cryptographic primitives] No findings — N/A.** No randomness; ids are a deterministic counter.
- **[Network & I/O] SHOULD FIX (at consumer, #705) + OUT OF SCOPE.** `Snapshot` allocates O(backlog) per call. It does not worsen the in-memory growth #704 deferred (it is read-only), but the #705 producer should call it at a bounded cadence — coalesced off the edge-triggered change seam — not in a hot poll loop. The inbound backlog bound itself (`MaxQueuedPerConversation` / `ErrQueueFull` at `Enqueue`) remains OUT OF SCOPE, pre-specified by #704 for the wiring slice; this slice adds no new unbounded surface.
- **[Error messages, logs] No findings.** The new code adds **zero** log lines and returns no errors that could leak state. `Snapshot` returns `text` as a value, never logs it. Code-review must confirm the developer added no debug log of the snapshot/removed id+text.
- **[Concurrency] No findings (core argument).** The only TOCTOU candidate is `Remove` vs the drain's `advanceLocked`: closed because the in-flight-head decision (`i == 0 && c.draining`) is read under the **same `q.mu`** the drain uses to set/clear `draining` and to peek/advance, so the check-and-splice is atomic w.r.t. the drain. `draining == false` ⟺ no goroutine owns the conversation, so removing index 0 then is safe; `draining == true` ⟹ no-op on index 0, so `advanceLocked` can never drop the wrong message. `notify` and `deliver` both fire strictly **after** unlock, so a re-entrant `OnChange` cannot deadlock against the goroutine that fired it. No new lock, no nesting, no new goroutine, no new leak. A blocking `OnChange` would stall a drain — a liveness footgun, not an exploit (the callback is wired by `cmd/pyry`, never phone-supplied); defused by the `ChangeFunc` "MUST NOT block, MUST be concurrency-safe" doc contract.
- **[Threat model alignment] No findings.** ADR 025 § Security model (line 143) explicitly lists viewing and dequeuing as a paired phone's **ungated** capability (only *answering permission-class modals* is gated), so `Snapshot`/`Remove` need no permission gate. The one binding threat — `dequeue_message` must not cancel an in-flight delivery (the #705 handler relies on it) — is enforced by the in-flight-head no-op (AC #2) and asserted in the test matrix.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-22
