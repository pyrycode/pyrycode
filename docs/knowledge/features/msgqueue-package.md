# `internal/msgqueue` — per-conversation inbound message backlog + drain engine

In-memory, daemon-resident FIFO backlog for phone-originated `send_message`
turns, with one serial drain goroutine per active conversation. It is the
**inbound counterpart of [`internal/eventring`](eventring-package.md)**: where
`eventring` buffers what the daemon pushes **out** to phones (the structured
event stream), `msgqueue` buffers what phones send **in** while claude is busy,
and releases it into the live claude session in order, one at a time, paced by
claude reaching idle / turn-end. Landed in #704 (EPIC #597 Phase 3 — interactive
modals/permissions/queue, ADR 025); the introspection / remove-by-id /
change-notification API was added additively in #719.

The package ships **engine only, unwired** — no package depends on it yet, the
same rhythm as the `turnbridge` producer (#606 shipped unwired, #616 wired it).
The live wiring into the `send_message` handler + the `cmd/pyry` constructor, the
`queue_state` / `dequeue_message` reporting/removal wire types, and any inbound
bound/backpressure policy are **separate slices** (#705 / the wiring slice). #719
added the engine-side primitives those consumers need (`Snapshot`, `Remove`,
`OnChange`) — still additive and unwired.

- Decision anchor: [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md)
  — `send_message` is "queued by the daemon when claude is busy" (line 123); each
  queued message is the `{queued_msg_id, text, ts}` record (line 118).
- Spec: [`specs/architecture/704-inbound-message-queue.md`](../../specs/architecture/704-inbound-message-queue.md).
- Ticket record: [codebase/704.md](../codebase/704.md).

## Why a new store

Today `send_message` delivers **synchronously**: the handler calls
`Supervisor.WriteUserTurn` (`internal/supervisor/supervisor.go:209-259`), whose
`WaitReady` idle-gate blocks the per-conn goroutine while claude is busy and which
fails (bounded by `sendMessageDeliverTimeout`, 30s) if claude stays busy past the
cap (#594). So a message typed mid-turn either **blocks** the handler or — on a
long turn — **fails**, and concurrent messages **race** across handler goroutines
with no defined order. `msgqueue` replaces that synchronous request/response with
**enqueue-then-drain**: `Enqueue` is non-blocking and returns an id immediately;
the drain delivers asynchronously through the same #594 reliable path.

## Exported surface

```go
// DeliverFunc is the injected reliable-delivery seam — the shape of
// supervisor.WriteUserTurn. It MUST block while claude is busy (the WaitReady
// idle-gate) and return nil ONLY on a confirmed commit; that blocking IS the
// drain's turn-end pacing. A non-nil return ⇒ retry the same FIFO head.
type DeliverFunc func(ctx context.Context, convID string, payload []byte) error

// ChangeFunc is the injected change-notification seam (#719) — same seam style
// as DeliverFunc. Fires, NEVER under q.mu, with the convID whose backlog
// changed (enqueue / delivery-advance / successful Remove). Carries only convID;
// the consumer re-reads via Snapshot (edge-triggered, coalescing is the
// consumer's choice). MUST NOT block, MUST be concurrency-safe. nil ⇒ disabled.
type ChangeFunc func(convID string)

// QueuedMessage is the engine-side projection of ADR 025's {queued_msg_id, text,
// ts} record (#719); the element Snapshot returns. Text is untrusted phone
// transit content — never log it, only surface it to the authorized conversation.
type QueuedMessage struct {
    ID   uint64
    Text string
    TS   time.Time
}

type Config struct {
    Deliver       DeliverFunc   // required; New errors if nil
    RetryInterval time.Duration // <= 0 ⇒ defaultRetryInterval (1s); poll cadence while claude is unavailable
    OnChange      ChangeFunc    // optional (#719); nil ⇒ change notification disabled
    Logger        *slog.Logger  // nil ⇒ slog.Default()
}

func New(cfg Config) (*Queue, error)                       // errors if cfg.Deliver == nil
func (q *Queue) Enqueue(convID, text string) uint64        // non-blocking; returns the stable per-conv id (>= 1)
func (q *Queue) Run(ctx context.Context) error             // lifecycle; blocks until ctx done, then joins all drains
func (q *Queue) Snapshot(convID string) []QueuedMessage    // #719: ordered copy of the backlog; unknown conv ⇒ nil
func (q *Queue) Remove(convID string, id uint64) bool      // #719: drop a not-in-flight queued msg; true iff removed
```

`New` **returns an error** (does not panic) on a nil `Deliver` — the seam is
caller-supplied wiring, so a missing one is a wiring error to surface, not a
programmer-constant to panic on (contrast `eventring.New`'s bound, which *does*
panic). `RetryInterval`, `OnChange`, and `Logger` fall back to their defaults; a
nil `OnChange` is the supported "notification disabled" default (no validation,
unlike the required `Deliver`).

Internal shapes mirror ADR 025's record: `queued{id, text, ts}`, and a
per-conversation `convQueue{items []queued, nextID uint64, draining bool}` held in
a `map[string]*convQueue` under a single `sync.Mutex`.

## Drain pacing — the seam *is* the turn-end signal (no detector built)

The ticket offered two drain triggers; the spec chose **option (a)**: a serial
drain loop that simply calls the delivery seam per message. `WriteUserTurn`'s
`WaitReady` gate already blocks while claude is busy and returns only when claude
is idle (then commits), so the queue needs **no separate turn-state detector**:

- A message enqueued **mid-turn** → the drain peeks it and calls `deliver`, which
  **blocks inside `WaitReady`** until the turn ends, then delivers. "Held until the
  turn ends" falls out for free.
- A message enqueued while claude is **idle** → `WaitReady` returns promptly,
  delivers promptly.
- The loop is **serial**: the next message's `deliver` is not called until the
  previous one returned (confirmed) ⇒ **never more than one in-flight delivery per
  conversation**.

Option (b) — an explicit `turnevent.TurnEnd` trigger from `turnbridge` — was
**rejected**: it would add a second, redundant turn-state source and a
cross-package subscription for pacing the seam already encapsulates. There is one
honest pacing source (the seam); a second screen-sourced detector for a JSONL/
idle-gated invariant would be different-fabric-for-its-own-sake with no observed
failure to defend.

## Lifecycle — lazy per-conversation drains, joined by `Run`

- **`Enqueue`** (under `mu`): get-or-create `convs[convID]`; assign `id =
  c.nextID; c.nextID++` (starts at 1); append `{id, text, time.Now()}`. If the
  lifecycle is running and no drain is already servicing the conversation, spawn
  one (`maybeSpawnDrainLocked`). Return `id`. Non-blocking; never waits on delivery.
- **Per-conversation independence:** each conversation gets its **own** drain
  goroutine, so a conversation whose `deliver` is blocked (claude busy) never
  blocks or reorders another conversation's drain. Idle conversations hold no
  goroutine.
- **`Run(ctx)`:** under `mu`, set `q.ctx = ctx; q.started = true` and spawn a drain
  for any conversation already holding a backlog (covers `Enqueue`-before-`Run`,
  no lost wakeup). Block on `<-ctx.Done()`, then set `q.closed = true` under `mu`,
  then `q.wg.Wait()`, then return `ctx.Err()` (errgroup-friendly, matches
  `turnbridge.Producer.Run`).
- **The drain loop** peeks the head under the lock, releases the lock, calls
  `deliver`, and advances (`items[1:]`) only after a confirmed commit — **peek,
  don't pop**. On `deliver` error it logs (never the text) and retries the **same
  head** after `RetryInterval`. It exits — clearing `draining` under the lock — on
  an empty FIFO (a later `Enqueue` respawns it) or on ctx-cancel.

### The `closed` flag — the shutdown-join happens-before (beyond the spec sketch)

The spec sketched only a `started` flag. The implementation adds a **`closed
bool`**, set under `q.mu` in `Run` *before* `wg.Wait()`, and checked by
`maybeSpawnDrainLocked` (which takes each spawn's `wg.Add(1)` under the same lock).
This makes every `wg.Add` **happen-before** `wg.Wait`: a late `Enqueue` either adds
before `closed` is observed (so before `Wait`) or sees `closed` and does not add at
all. A `ctx.Err()`-based gate would **not** give this happens-before — ctx
cancellation isn't serialized by the mutex — so the explicit flag is what makes the
shutdown join `-race`-clean.

## Introspection, removal, and change notification (#719)

An additive read/remove/notify layer the `queue_state` / `dequeue_message`
consumers (#705) need. No goroutine, no new lock; `Enqueue`/`Run`/stable-id
semantics untouched.

- **`Snapshot(convID)` — copy under the lock.** Takes `q.mu`, looks up the conv
  (`nil` ⇒ return `nil`, not an error), else projects each internal `queued` into
  a `QueuedMessage` value into a freshly allocated slice and returns it. Same
  snapshot-under-lock + copy-out idiom as the outbound sibling `eventring.After`,
  so the caller cannot mutate engine state through the result. The **whole**
  `items` slice is returned, **including the in-flight head**: an item is in the
  backlog until `advanceLocked` drops it on a confirmed commit, so "current
  `items`" maps exactly to "not-yet-delivered." The snapshot does **not** flag
  which entry is in-flight — `Remove` returning `false` is that signal.
- **`Remove(convID, id)` — the in-flight-head no-op is the load-bearing rule.**
  `advanceLocked` blindly drops index 0 (`items[1:]`) on the assumption it is
  still the just-delivered head; `Enqueue` only ever appends to the tail, so
  `Remove` is the **first** op that can touch index 0. It refuses index 0 **iff**
  `c.draining`, decided under the same `q.mu` the drain uses to set/clear
  `draining` and to peek/advance — making the check-and-splice atomic w.r.t. the
  drain. `draining == false` ⟺ no goroutine owns `items` (safe to drop index 0);
  `draining == true` ⟹ no-op on the head, so `advanceLocked` can never drop the
  wrong message. Non-head removal (`idx >= 1`) is always safe — the drain only
  touches index 0 and holds a value copy of the head. Unknown conv / unknown /
  already-delivered id / in-flight head ⇒ `false` no-op (no panic, no reorder);
  surviving order preserved. This is what guarantees `dequeue_message` **cannot
  cancel an in-flight delivery** (the #705 handler relies on it).
- **`shrinkLocked` — shared backing-array hygiene.** The trailing "release at
  empty / compact when `cap > 2*len`" `switch` was lifted out of `advanceLocked`
  into `convQueue.shrinkLocked()`, now called by both `advanceLocked` (head drop)
  and `Remove` (mid-FIFO drop) — extracted, not duplicated, so a mid-FIFO removal
  doesn't leave a stale backing array. (`Remove`'s slice-delete leaves the freed
  `queued` value — holding the untrusted `text` — beyond the new `len` until
  `shrinkLocked` compacts, exactly as `advanceLocked`/`items[1:]` already does;
  consistent precedent, deliberately not zeroed.)
- **Change-notification fires after unlock, three sites.** A `notify(convID)`
  helper does the `onChange != nil` nil-check and is always called **after** `q.mu`
  is released, only on a real change: `Enqueue` (restructured from `defer
  q.mu.Unlock()` to explicit unlock + `notify`), the drain's delivery-advance
  (after `advanceLocked` drops a confirmed head — **not** on the empty-exit or
  delivery-error/retry paths), and a successful `Remove` (no-ops fire nothing).
  Firing strictly after unlock is what makes a re-entrant `OnChange` (a consumer
  that calls back into `Snapshot`/`Remove`/`Enqueue`) re-acquire the lock cleanly
  and not deadlock against the goroutine that fired it. `OnChange` carries only
  `convID`; the seam is edge-triggered and the consumer coalesces by re-reading
  `Snapshot` (matches how the #647 reconnect path treats `eventring`).

## Concurrency model

- **Goroutines:** one `Run` goroutine (the daemon adds it to its errgroup in the
  wiring slice) + **one drain goroutine per active conversation**, spawned lazily
  and exiting when its FIFO empties. `Snapshot`/`Remove` add **no** goroutine —
  they run on the caller's goroutine; `notify` runs on whichever goroutine
  performed the mutation (Enqueue caller, a drain, or a Remove caller).
- **Shared state:** a single `sync.Mutex` (`q.mu`) guards `convs`, each
  `convQueue`, `started`, `closed`, and `q.ctx`. **Leaf lock** — never held across
  the blocking `deliver` (which can block for a whole claude turn), and (since
  #719) never held across `onChange` either, never nested. Both `deliver` and
  `onChange` are caller-supplied seams that could block or re-enter, so both are
  called lock-free. This is the same "release before the seconds-long delivery"
  discipline `WriteUserTurn` itself uses.
- **TOCTOU on the FIFO head:** the drain **peeks** under the lock and advances only
  **after** a confirmed commit, under the lock again. A concurrent `Enqueue` can
  only append (FIFO tail), never mutate the head, so the in-flight head can't be
  swapped out from under the delivery.
- **Lazy-spawn / exit-on-empty race** is closed under a single lock hold: the
  empty-check and the `draining = false` write are atomic w.r.t. a concurrent
  `Enqueue`, which either appends before the drain takes the lock (drain sees
  `len > 0`, keeps going) or after it released (sees `draining == false`, respawns).
  No interleave loses a message.
- **Shutdown:** parent ctx cancel → `deliver`'s `WaitReady` returns ctx error (or
  `sleepCtx` returns false) → each drain clears `draining` and returns → `wg.Done`
  → `Run`'s `wg.Wait` unblocks → `Run` returns `ctx.Err()`. No drain outlives `Run`.

## Error handling

- **`deliver` returns an error** (no live session during a child respawn →
  `ErrNoLiveSession`; wedged/uncommitted → `ErrTurnNotCommitted`; PTY write error):
  **retry the same head** after `RetryInterval`, leaving it at the FIFO head. This
  is **lossless**, and is exactly what makes "undelivered messages survive a claude
  **child** respawn and drain into the new child" — during the respawn window
  `WriteUserTurn` returns `ErrNoLiveSession` immediately; the retry bridges it.
  **No per-message delivery deadline** — a message is retried until delivered or the
  engine shuts down. (Contrast the synchronous handler's 30s
  `sendMessageDeliverTimeout`, which exists only because the phone is blocked
  awaiting an ack; here the enqueue-ack is immediate and delivery is async.)
- **Long but healthy turn:** `WaitReady` *blocks* (it does not error), so this path
  does **not** hit the retry branch — the message simply waits, then delivers.
- **`text` is never logged at any level.** The drain's only log (warn-on-delivery-
  error) carries `conversation_id`, the queued message `id`, the enqueue timestamp,
  and the error — **never** the text (mirrors `send_message.go`'s SECURITY
  discipline; the text is untrusted phone content bound for claude's stdin verbatim).
- **No silent drop.** Every message is either delivered (head advances) or still
  queued (retry / awaiting drain).

## Durability boundary (in scope vs out)

The backlog is **in-memory and keyed to the `pyry` daemon lifetime**, not the
child's (the same boundary as `eventring`):

- **Survives** a supervised claude-**child** respawn — the retry-the-same-head loop
  bridges the respawn window and drains into the new child.
- **Does not survive** a full daemon-process restart — purely in-memory, by design.
  Reconnect/resync covers that boundary. **No on-disk persistence** in this slice.

## Memory hygiene

`advanceLocked` releases the backing array when the FIFO empties (`items = nil`)
and compacts (`copy` into a fresh slice) when `cap > 2*len`, so a long-lived
conversation's slice doesn't retain an ever-growing backing array from past bursts.
Because `items[1:]` shrinks `cap` in lockstep with `len`, the compaction guard only
fires in the **tail of draining a large burst** — exactly when it matters; queues
are expected shallow, so it rarely fires at all.

The `convs` map itself is **not** evicted after a conversation fully drains
(`items` goes to `nil` but the `*convQueue` stays, preserving `nextID` for
id-stability) — deliberate, and mirrors `eventring`'s per-conversation map. The map
grows with **distinct** conversation ids over the daemon's lifetime, bounded by real
conversations for a single-operator tool.

## Security

The queue buffers untrusted, phone-originated `send_message` text and releases it
into the live claude session; ordering, loss-prevention, drain-pacing, and bounding
are inbound message-dispatch **policy** on an internet-exposed surface (`#704` is
`security-sensitive`). The engine's stance:

- **`text` is opaque transit.** Stored, never inspected, parsed, or used in a
  control decision; converted to `[]byte` only at the `deliver` call; **never
  logged** (above).
- **`convID` is a map key only.** Validating/resolving it to a real session is the
  **caller's** job, upstream of `Enqueue` (the `SessionRouter` / `ValidateConversation`
  in `send_message.go`). A hostile `convID` can at worst create an isolated FIFO that
  never drains — **never reach another conversation's session** (per-conversation maps
  + per-conversation drains are isolated by construction). No type-system signal is
  added, matching the `WriteUserTurn(ctx, id, payload)` convention where `id` is
  pre-validated by the caller.
- **No tokens/secrets/crypto/file/subprocess surface.** The `id` is a non-secret
  per-conversation counter, not a capability.
- **Inbound bound / backpressure — deferred (evidence-based).** A phone flooding
  `send_message` while claude is persistently busy/wedged grows `convs[convID].items`
  without bound (an in-memory DoS). Severity is **SHOULD-FIX, not MUST-FIX**:
  `send_message` sits behind the per-conn auth gate (the flooder is a *paired,
  authenticated* device), this is a self-hosted single-operator tool, the failure is
  **unobserved**, and the drain's retry actively shrinks the backlog whenever claude
  accepts a turn. A bound here cannot **drop** (that violates losslessness) — it must
  **reject** past a cap, which is `send_message` ack **policy** that belongs with the
  wire types / handler change. The single insertion point is pre-specified: add a
  `MaxQueuedPerConversation int` to `Config`, change `Enqueue` to `(uint64, error)`,
  return an `ErrQueueFull` sentinel before appending. Zero call sites today, so
  deferring the signature costs no churn — the wiring slice adopts whatever exists
  then.
- **`Snapshot` / `Remove` boundary crossings (#719) — convID trust is the
  consumer's job.** Both key by **caller-supplied `convID`**, and `Snapshot`
  *returns* the opaque `text` (it will flow out to a phone via `queue_state`).
  Per-conversation maps give isolation-by-construction **once `convID` is
  trusted**; trusting it is #705's job (bind `convID` to the requesting phone's
  authorized conversation, exactly as `send_message` does). Cross-conversation
  **confidentiality** (`Snapshot` reading another conv's queued text) and
  **integrity** (`Remove` mutating another conv's FIFO) are prevented only there.
  The engine adds **zero** new log lines — `Snapshot` returns `text` as a value,
  never logs it; the consumer must preserve the "`text` never logged" discipline
  across the new exit. `Remove` never touches `nextID`, so the stable-id contract
  is preserved and a removed id is simply never reused. ADR 025 § Security model
  lists viewing/dequeuing as a paired phone's **ungated** capability (only
  answering permission-class modals is gated), so no permission gate is needed.

## Files

```
internal/msgqueue/
├── queue.go                  DeliverFunc, ChangeFunc, QueuedMessage, Config, Queue, queued,
│                             convQueue; New / Enqueue / Snapshot / Remove / Run; notify,
│                             maybeSpawnDrainLocked, drain, advanceLocked, shrinkLocked,
│                             sleepCtx; defaultRetryInterval
├── queue_test.go             #704: ordered one-at-a-time drain (in-flight counter fails >1),
│                             empty no-op, per-conversation independence, idle-drains-promptly,
│                             lossless-retry/respawn, stable independent ids,
│                             clean-shutdown-no-leak, New(nil) rejects (unmodified by #719)
└── queue_introspect_test.go  #719: snapshot-in-order, snapshot-is-a-copy, Remove drops
                              non-head / no-ops on in-flight-head|unknown|already-delivered,
                              OnChange fires on enqueue|advance|remove (no-op fires nothing),
                              OnChange fires without holding the lock (re-entrant), new-API
                              preserves #704 invariants
```

Stdlib-only (`context`, `errors`, `log/slog`, `sync`, `time`); imports **no**
`internal/*` package — the delivery path arrives as the injected `DeliverFunc`,
the change-notification path as the injected `ChangeFunc`.

## Related

- [codebase/704.md](../codebase/704.md) — engine ticket record (patterns + lessons).
- [codebase/719.md](../codebase/719.md) — introspection / remove / change-notify
  ticket record (the in-flight-head no-op rule, fire-after-unlock).
- [features/eventring-package.md](eventring-package.md) — the **outbound** sibling
  this mirrors (per-conversation in-memory store, daemon-resident, same restart
  boundary).
- `internal/supervisor` `WriteUserTurn` — the #594 reliable-delivery path whose
  shape `DeliverFunc` mirrors and whose `WaitReady` block *is* the drain's pacing.
- [features/turnbridge-package.md](turnbridge-package.md) — the "shipped unwired,
  injected-function-seam, `Config` + `New` + `Run`" template this engine follows.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § wire
  protocol (`send_message` queued-by-daemon, the `{queued_msg_id, text, ts}` record).
- **Consumers (deferred — none wired in #704/#719):** the live `send_message` +
  `cmd/pyry` wiring slice and #705 (`queue_state` / `dequeue_message` reporting +
  removal handlers binding `convID` to the authorized phone, and the inbound-bound
  decision). #719 added the engine-side primitives (`Snapshot` / `Remove` /
  `OnChange`) those consumers map onto; the wire types and convID-trust binding
  remain theirs.
