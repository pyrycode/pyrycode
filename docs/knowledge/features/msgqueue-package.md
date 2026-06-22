# `internal/msgqueue` — per-conversation inbound message backlog + drain engine

In-memory, daemon-resident FIFO backlog for phone-originated `send_message`
turns, with one serial drain goroutine per active conversation. It is the
**inbound counterpart of [`internal/eventring`](eventring-package.md)**: where
`eventring` buffers what the daemon pushes **out** to phones (the structured
event stream), `msgqueue` buffers what phones send **in** while claude is busy,
and releases it into the live claude session in order, one at a time, paced by
claude reaching idle / turn-end. Landed in #704 (EPIC #597 Phase 3 — interactive
modals/permissions/queue, ADR 025).

This slice ships the **engine only, unwired** — no package depends on it yet, the
same rhythm as the `turnbridge` producer (#606 shipped unwired, #616 wired it).
The live wiring into the `send_message` handler + the `cmd/pyry` constructor, the
`queue_state` / `dequeue_message` reporting/removal wire types, and any inbound
bound/backpressure policy are **separate slices** (#705 / the wiring slice).

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

## Exported surface (3 types)

```go
// DeliverFunc is the injected reliable-delivery seam — the shape of
// supervisor.WriteUserTurn. It MUST block while claude is busy (the WaitReady
// idle-gate) and return nil ONLY on a confirmed commit; that blocking IS the
// drain's turn-end pacing. A non-nil return ⇒ retry the same FIFO head.
type DeliverFunc func(ctx context.Context, convID string, payload []byte) error

type Config struct {
    Deliver       DeliverFunc   // required; New errors if nil
    RetryInterval time.Duration // <= 0 ⇒ defaultRetryInterval (1s); poll cadence while claude is unavailable
    Logger        *slog.Logger  // nil ⇒ slog.Default()
}

func New(cfg Config) (*Queue, error)                // errors if cfg.Deliver == nil
func (q *Queue) Enqueue(convID, text string) uint64 // non-blocking; returns the stable per-conv id (>= 1)
func (q *Queue) Run(ctx context.Context) error      // lifecycle; blocks until ctx done, then joins all drains
```

`New` **returns an error** (does not panic) on a nil `Deliver` — the seam is
caller-supplied wiring, so a missing one is a wiring error to surface, not a
programmer-constant to panic on (contrast `eventring.New`'s bound, which *does*
panic). `RetryInterval` and `Logger` fall back to their defaults.

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

## Concurrency model

- **Goroutines:** one `Run` goroutine (the daemon adds it to its errgroup in the
  wiring slice) + **one drain goroutine per active conversation**, spawned lazily
  and exiting when its FIFO empties.
- **Shared state:** a single `sync.Mutex` (`q.mu`) guards `convs`, each
  `convQueue`, `started`, `closed`, and `q.ctx`. **Leaf lock** — never held across
  the blocking `deliver` (which can block for a whole claude turn), never nested.
  This is the same "release before the seconds-long delivery" discipline
  `WriteUserTurn` itself uses.
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

## Files

```
internal/msgqueue/
├── queue.go        DeliverFunc, Config, Queue, queued, convQueue; New / Enqueue / Run;
│                   maybeSpawnDrainLocked, drain, advanceLocked, sleepCtx; defaultRetryInterval
└── queue_test.go   ordered one-at-a-time drain (in-flight counter fails >1), empty no-op,
                    per-conversation independence, idle-drains-promptly, lossless-retry/
                    respawn, stable independent ids, clean-shutdown-no-leak, New(nil) rejects
```

~285 LOC production (one new package) + ~368 LOC tests. Stdlib-only
(`context`, `errors`, `log/slog`, `sync`, `time`); imports **no** `internal/*`
package — the delivery path arrives as the injected `DeliverFunc`.

## Related

- [codebase/704.md](../codebase/704.md) — ticket record (patterns + lessons).
- [features/eventring-package.md](eventring-package.md) — the **outbound** sibling
  this mirrors (per-conversation in-memory store, daemon-resident, same restart
  boundary).
- `internal/supervisor` `WriteUserTurn` — the #594 reliable-delivery path whose
  shape `DeliverFunc` mirrors and whose `WaitReady` block *is* the drain's pacing.
- [features/turnbridge-package.md](turnbridge-package.md) — the "shipped unwired,
  injected-function-seam, `Config` + `New` + `Run`" template this engine follows.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § wire
  protocol (`send_message` queued-by-daemon, the `{queued_msg_id, text, ts}` record).
- **Consumers (deferred — none wired in #704):** the live `send_message` + `cmd/pyry`
  wiring slice and #705 (`queue_state` / `dequeue_message` reporting + removal types,
  and the inbound-bound decision).
