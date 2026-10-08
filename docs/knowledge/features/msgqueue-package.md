# `internal/msgqueue` — per-conversation inbound message backlog + drain engine

In-memory, daemon-resident FIFO backlog for phone-originated `send_message`
turns, with one serial drain goroutine per active conversation, and a bounded,
reject-never-drop per-conversation backlog. It is the **inbound counterpart of
[`internal/eventring`](eventring-package.md)**: where `eventring` buffers what
the daemon pushes **out** to phones (the structured event stream), `msgqueue`
buffers what phones send **in** while claude is busy, and releases it into the
live claude session in order, one at a time, paced by claude reaching idle /
turn-end. Landed in #704 (EPIC #597 Phase 3 — interactive modals/permissions/
queue, ADR 025); the introspection / remove-by-id / change-notification API was
added additively in #719; the per-conversation backlog bound landed in
[#869](../codebase/869.md); the persistent-failure give-up bound landed in
[#1000](../codebase/1000.md); the delivered-notification seam feeding the
durable conversation log landed in #2115.

The package shipped **engine only, unwired** in #704 — the same rhythm as the
`turnbridge` producer (#606 shipped unwired, #616 wired it). [#721](../codebase/721.md)
**wired it live**: the daemon constructs one `Queue` in `cmd/pyry/runSupervisor`,
runs `Queue.Run(ctx)` under the daemon lifecycle, and `send_message` now
**enqueues-and-acks** instead of delivering synchronously (delivery seam =
`newInboundDeliver(router.resolve)`, the reliable `WriteUserTurn` path — extended in
[#1199](../codebase/1199.md) to `newInboundDeliver(router.resolve, turnBusy, streamTurnHoldTimeout)`,
see § Related). The
`queue_state` / `dequeue_message` reporting/removal wire types (#720) + their
handlers landed as #722/#723 (see § Related); the previously-deferred inbound
bound/backpressure policy landed as [#869](../codebase/869.md) once that live
wiring shipped. #719 added the engine-side primitives the #722/#723
reporting/removal handlers map onto (`Snapshot`, `Remove`, `OnChange`).

- Decision anchor: [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md)
  — `send_message` is "queued by the daemon when claude is busy" (line 123); each
  queued message is the `{queued_msg_id, message_id, text, ts}` record (line 118;
  `message_id` added by #2092 — see [§ Security](msgqueue-package-security.md)).
- Spec: [`specs/architecture/704-inbound-message-queue.md`](../../specs/architecture/704-inbound-message-queue.md).
- Ticket record: [codebase/704.md](../codebase/704.md).

## Topic map

| Document | Topics |
| --- | --- |
| [API](msgqueue-package-api.md) | Enqueue, identity, lifecycle callbacks, snapshots and removal. |
| [Confirmation and concurrency](msgqueue-package-lifecycle.md) | Safe delivery facts, acceptance ordering, outcome arbitration and shutdown. |
| [Give-up](msgqueue-package-give-up.md) | Per-head failure bounds and pending-hold exemptions. |
| [Send now](msgqueue-package-send-now.md) | Mid-turn delivery and refused-head re-insertion. |
| [Security](msgqueue-package-security.md) | Projection and caller trust boundaries. |
| [Integrations](msgqueue-package-related.md) | Consumers, related features and decisions. |

## Why a new store

Before #721, `send_message` delivered **synchronously**: the handler called
`Supervisor.WriteUserTurn`, whose
`WaitReady` idle-gate blocked the per-conn goroutine while claude was busy and
which failed (bounded by `sendMessageDeliverTimeout`, 30s) if claude stayed busy
past the cap (#594). So a message typed mid-turn either **blocked** the handler or
— on a long turn — **failed**, and concurrent messages **raced** across handler
goroutines with no defined order. `msgqueue` replaces that synchronous
request/response with **enqueue-then-drain**: `Enqueue` never waits for delivery;
the optional acceptance observer runs before it returns an id. The drain
delivers asynchronously through the same #594 reliable
path. #721 swapped the handler over to it (see [codebase/721.md](../codebase/721.md)).

## Exported surface

See [Exported surface](msgqueue-package-api.md#exported-surface) for
`EnqueueIdentified`, `DeviceID`, optional `OnAccepted`/`OnTerminal` and the
legacy APIs. Legacy enqueue callers carry empty device identity; identity
is metadata without authorization or deduplication.

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

- **`Enqueue`** (under `mu`): get-or-create `convs[convID]`; **(#869) if the
  conversation's backlog is already at `Config.MaxQueuedPerConversation`
  (`len(c.items) >= q.max`, default 100), reject** — a pure early return: `0` is
  returned, no `nextID` bump, no append, no `notify`, no drain spawn, and the
  existing backlog (including the in-flight head) is left untouched. Otherwise
  assign `id = c.nextID; c.nextID++` (starts at 1); append `{id, text,
  time.Now()}`. If the lifecycle is running and no drain is already servicing the
  conversation, spawn one (`maybeSpawnDrainLocked`). After unlocking, invoke
  `OnAccepted` if configured, complete acceptance and publish any deferred
  terminal/delivered callbacks, then notify `OnChange` and return `id`.
  Never waits on delivery; acceptance observation runs on the enqueue caller.
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

See [Introspection, removal, and change notification (#719)](msgqueue-package-api.md#introspection-removal-and-change-notification-719).

## Send-now delivery (#2729)

See [Send-now delivery](msgqueue-package-send-now.md): `Config.SendNow` writes
a queued message into the conversation's *running* turn instead of waiting
for idle — `Remove`'s cancel, now for acceleration. It shares `Remove`'s
take-the-head-out-of-the-FIFO safety argument rather than reimplementing it,
adds one delivered-projection field (`QueuedMessage.SentNow`) so an
`OnDelivered` consumer can tell the two deliveries apart, and the
`convQueue.headTaken` flag that keeps a refused head's own re-insertion from
being misread as a delivery failure that starts a give-up streak.

## Bounded give-up on persistent delivery failure (#1000)

See [Bounded give-up on persistent delivery failure (#1000)](msgqueue-package-give-up.md#bounded-give-up-on-persistent-delivery-failure-1000).

## Delivered notification (#2115)

See [Delivered notification (#2115)](msgqueue-package-lifecycle.md#delivered-notification-2115)
for copied safe projections and the delivered, removed and give-up terminal
outcomes. Acceptance completes before delivery observers; `OnDelivered`
precedes the delivered terminal callback. Confirmed delivery wins removal races.

## Concurrency model

See [Concurrency model](msgqueue-package-lifecycle.md#concurrency-model) for
off-lock callback scheduling and re-entry, atomic FIFO advance/outcome claims,
the drain join and the retained shutdown/confirmed-write observation gap.

## Error handling

- **`deliver` returns an error** (no live session during a child respawn →
  `ErrNoLiveSession`; wedged/uncommitted → `ErrTurnNotCommitted`; PTY write error):
  **retry the same head** after `RetryInterval`, leaving it at the FIFO head. This
  is **lossless**, and is exactly what makes "undelivered messages survive a claude
  **child** respawn and drain into the new child" — during the respawn window
  `WriteUserTurn` returns `ErrNoLiveSession` immediately; the retry bridges it.
  **No per-message delivery deadline** — a message is retried until delivered, the
  engine shuts down, or (#1000) the per-head elapsed bound (`GiveUpAfter`,
  default 2m — see § Bounded give-up) is exceeded, in which case the head is
  abandoned rather than retried forever. (Contrast the synchronous handler's 30s
  `sendMessageDeliverTimeout`, which exists only because the phone is blocked
  awaiting an ack; here the enqueue-ack is immediate and delivery is async.)
- **Long but healthy turn:** `WaitReady` *blocks* (it does not error), so this path
  does **not** hit the retry branch — the message simply waits, then delivers.
- **`text` is never logged at any level.** The drain's only logs (warn-on-delivery-
  error, and #1000's warn-on-give-up) carry `conversation_id`, the queued message
  `id`, the enqueue timestamp / elapsed window, and the error — **never** the text
  (mirrors `send_message.go`'s SECURITY discipline; the text is untrusted phone
  content bound for claude's stdin verbatim).
- **Resolved outcomes are explicit.** With lifecycle observation configured,
  confirmed delivery, explicit removal without delivery and actual give-up
  each report one message-specific terminal fact. Retry/hold leaves the
  acceptance unresolved; shutdown alone does too. Give-up remains logged and
  surfaced through the legacy `OnGiveUp` seam.

## Durability boundary (in scope vs out)

The backlog is **in-memory and keyed to the `pyry` daemon lifetime**, not the
child's (the same boundary as `eventring`):

- **Survives** a supervised claude-**child** respawn — the retry-the-same-head loop
  bridges the respawn window and drains into the new child.
- **Does not survive** a full daemon-process restart — purely in-memory, by design.
  Reconnect/resync covers that boundary. **No on-disk persistence** in this slice.

Queue ids are per conversation and daemon run, resetting after restart.
Acceptance and terminal facts supply the engine contract in
[ADR 042](../decisions/042-daemon-built-thread.md); the history writer (#2972)
owns durable linkage to its own acceptance records. Lifecycle wiring is #2971.

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

See [Security](msgqueue-package-security.md): the engine's stance on `text`,
`delivery`, `messageID`, `attachmentIDs` (#2596), `deviceName`/`clientVersion`/
`clientSentAt` (#2704), opaque `deviceID` (#2970), `convID`, and the `Snapshot`/`SnapshotAll`/`Remove`
boundary crossings (#719, #878), plus the inbound bound/backpressure (#869).

## Files

```
internal/msgqueue/
├── queue.go                  DeliverFunc, ChangeFunc, GiveUpFunc (#1000), DeliveredFunc (#2115),
│                             QueuedMessage, Config, Queue, queued, convQueue; New / Enqueue /
│                             Snapshot / SnapshotAll (#878) / Remove / Run; notify,
│                             notifyGiveUp (#1000), notifyDelivered (#2115),
│                             maybeSpawnDrainLocked, drain, giveUp (#1000), advanceLocked,
│                             shrinkLocked, sleepCtx; defaultRetryInterval,
│                             defaultMaxQueuedPerConversation (#869), defaultGiveUpAfter (#1000)
├── lifecycle.go              AcceptedFunc, TerminalFunc, TerminalOutcome; shared lifecycle
│                             state, acceptance completion and single-outcome publication
├── delivery_context.go       DeliveryMessage; safe copied lifecycle/delivery projection
├── lifecycle_test.go         acceptance ordering, identity/copy isolation, terminal outcomes,
│                             removal arbitration, send-now, retry and shutdown regressions
├── delivered_test.go         #2115: fires once on confirmed delivery carrying text (never
│                             delivery), exactly one call across retries, no call on give-up,
│                             no call when the head is removed before commit, fires even when a
│                             Remove races a confirmed delivery (the test that pins "unconditional
│                             on advanced" against an `if advanced` regression); #2596:
│                             AttachmentIDs rides the delivered projection in enqueued order,
│                             copied on entry (mutating the caller's slice after EnqueueAttached
│                             does not reach the record)
├── queue_test.go             #704: ordered one-at-a-time drain (in-flight counter fails >1),
│                             empty no-op, per-conversation independence, idle-drains-promptly,
│                             lossless-retry/respawn, stable independent ids,
│                             clean-shutdown-no-leak, New(nil) rejects (unmodified by #719);
│                             #878: SnapshotAll two-conversations, omits-drained-conversation,
│                             empty-queue, includes-in-flight-head, returns-value-copies,
│                             -race-with-enqueue-and-drain; #1000: gives-up after persistent
│                             failure, transient failure no give-up, default bound exceeds
│                             backoff window, no untrusted-content leak, clean teardown + respawn
└── queue_introspect_test.go  #719: snapshot-in-order, snapshot-is-a-copy, Remove drops
                              non-head / no-ops on in-flight-head|unknown|already-delivered,
                              OnChange fires on enqueue|advance|remove (no-op fires nothing),
                              OnChange fires without holding the lock (re-entrant), new-API
                              preserves #704 invariants
```

Uses stdlib plus `internal/turncommit` for the write-claim gate. Delivery and
observation consumers remain injected function seams; the queue does not import
the history writer or relay.

## Related

See [Related](msgqueue-package-related.md#related).
