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
  `message_id` added by #2092 — see § Security).
- Spec: [`specs/architecture/704-inbound-message-queue.md`](../../specs/architecture/704-inbound-message-queue.md).
- Ticket record: [codebase/704.md](../codebase/704.md).

## Why a new store

Before #721, `send_message` delivered **synchronously**: the handler called
`Supervisor.WriteUserTurn` (`internal/supervisor/supervisor.go:209-259`), whose
`WaitReady` idle-gate blocked the per-conn goroutine while claude was busy and
which failed (bounded by `sendMessageDeliverTimeout`, 30s) if claude stayed busy
past the cap (#594). So a message typed mid-turn either **blocked** the handler or
— on a long turn — **failed**, and concurrent messages **raced** across handler
goroutines with no defined order. `msgqueue` replaces that synchronous
request/response with **enqueue-then-drain**: `Enqueue` is non-blocking and returns
an id immediately; the drain delivers asynchronously through the same #594 reliable
path. #721 swapped the handler over to it (see [codebase/721.md](../codebase/721.md)).

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

// GiveUpFunc is the injected give-up-notification seam (#1000) — mirrors
// ChangeFunc exactly (never under q.mu, MUST NOT block, concurrency-safe, nil ⇒
// disabled). Fires once when the drain abandons a persistently-failing head,
// carrying the convID and a daemon-generated reason (elapsed retry window + the
// last delivery error) that NEVER contains the queued message text. Ships
// unwired (nil) in production this ticket; the wire vocabulary for the
// client-visible frame is #1007 (TypeSessionError/CodeSessionBlocked/
// SessionErrorPayload), and the blocked-by-#1007 sibling #1008 wires this seam
// to it, exactly as OnChange routes to the queue_state producer.
type GiveUpFunc func(convID, reason string)

// DeliveredFunc is the injected delivered-notification seam (#2115) — mirrors
// ChangeFunc/GiveUpFunc (never under q.mu, MUST NOT block, concurrency-safe,
// nil ⇒ disabled), fired once per head whose delivery was CONFIRMED. Unlike
// ChangeFunc it is not edge-triggered: the delivered item is gone from the
// backlog moments later, so it carries the item itself (the same QueuedMessage
// projection Snapshot/SnapshotAll build — no delivery field, see § Security).
type DeliveredFunc func(convID string, msg QueuedMessage)

// PendingFunc classifies a delivery error as a legitimate hold — the head is
// being deliberately withheld awaiting an external decision — rather than a
// delivery failure (#1014). While it returns true the drain retries WITHOUT
// counting the elapsed window toward GiveUpAfter and RESETS the give-up streak.
// It sees only the error, never the conversation, because msgqueue is a leaf
// that must not import the delivery seam's package — a conversation-scoped
// gating condition has to be folded into the error before it reaches here. MUST
// be pure and non-blocking (called on the drain path). nil ⇒ every non-nil
// delivery error counts (pre-#1014 behaviour).
type PendingFunc func(error) bool

// QueuedMessage is the engine-side projection of ADR 025's {queued_msg_id, text,
// ts} record (#719); the element Snapshot returns. TWO fields are untrusted,
// phone-originated content, not one: Text and MessageID (#2092) — never log
// either, only surface them to the authorized conversation. MessageID is the
// client's own id for the message (carried so a client can recognise a queued
// item as its own echo and draw it once), relayed verbatim, "" when the client
// sent none, never minted by the daemon; nothing reads it.
type QueuedMessage struct {
    ID        uint64
    MessageID string
    Text      string
    TS        time.Time
}

type Config struct {
    Deliver                  DeliverFunc   // required; New errors if nil
    RetryInterval            time.Duration // <= 0 ⇒ defaultRetryInterval (1s); poll cadence while claude is unavailable
    MaxQueuedPerConversation int           // #869: <= 0 ⇒ defaultMaxQueuedPerConversation (100); per-conv backlog cap
    OnChange                 ChangeFunc    // optional (#719); nil ⇒ change notification disabled
    GiveUpAfter              time.Duration // #1000: <= 0 ⇒ defaultGiveUpAfter (2m); per-head persistent-failure bound
    OnGiveUp                 GiveUpFunc    // optional (#1000); nil ⇒ give-up notification disabled (unwired in production)
    OnDelivered              DeliveredFunc // optional (#2115); nil ⇒ delivered notification disabled
    Pending                  PendingFunc   // optional (#1014); nil ⇒ no exemption from GiveUpAfter
    Logger                   *slog.Logger  // nil ⇒ slog.Default()
}

func New(cfg Config) (*Queue, error)                       // errors if cfg.Deliver == nil
func (q *Queue) Enqueue(convID, text string) uint64        // non-blocking; returns the stable per-conv id (>= 1), or 0 if the backlog is at cap (#869: reject, never drop)
func (q *Queue) EnqueueDelivery(convID, messageID, text, delivery string) uint64 // #2038 added delivery, #2092 added messageID: Enqueue in every respect except those two — text is what Snapshot/SnapshotAll (and so queue_state) reads back, delivery is the []byte the drain hands DeliverFunc, messageID is the client's own id for the message, stored and projected but read by nothing. Enqueue is EnqueueDelivery(convID, "", text, text) — the "" is the true value for a path that mints no client id, not a sentinel; it must NOT take q.mu before delegating (q.mu is not re-entrant — doing so hangs all ~108 existing Enqueue call sites, not just the new one)
func (q *Queue) Run(ctx context.Context) error             // lifecycle; blocks until ctx done, then joins all drains
func (q *Queue) Snapshot(convID string) []QueuedMessage    // #719: ordered copy of the backlog; unknown conv ⇒ nil
func (q *Queue) SnapshotAll() map[string][]QueuedMessage    // #878: every conv's backlog keyed by convID, omitting empty ones
func (q *Queue) Remove(convID string, id uint64) bool      // #719: drop a not-in-flight queued msg; true iff removed
```

`New` **returns an error** (does not panic) on a nil `Deliver` — the seam is
caller-supplied wiring, so a missing one is a wiring error to surface, not a
programmer-constant to panic on (contrast `eventring.New`'s bound, which *does*
panic). `RetryInterval`, `OnChange`, and `Logger` fall back to their defaults; a
nil `OnChange` is the supported "notification disabled" default (no validation,
unlike the required `Deliver`).

Internal shapes mirror ADR 025's record: `queued{id, text, delivery, ts}` (`delivery`
added #2038 — a plain value, not an "empty means fall back to text" sentinel, so
`drain` stays a single unconditional `[]byte(head.delivery)` with no branch on
which field a caller meant), and a
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

- **`Enqueue`** (under `mu`): get-or-create `convs[convID]`; **(#869) if the
  conversation's backlog is already at `Config.MaxQueuedPerConversation`
  (`len(c.items) >= q.max`, default 100), reject** — a pure early return: `0` is
  returned, no `nextID` bump, no append, no `notify`, no drain spawn, and the
  existing backlog (including the in-flight head) is left untouched. Otherwise
  assign `id = c.nextID; c.nextID++` (starts at 1); append `{id, text,
  time.Now()}`. If the lifecycle is running and no drain is already servicing the
  conversation, spawn one (`maybeSpawnDrainLocked`). Return `id`. Non-blocking;
  never waits on delivery.
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

An additive read/remove/notify layer the `queue_state` (#722) / `dequeue_message`
(#723) consumers need. No goroutine, no new lock; `Enqueue`/`Run`/stable-id
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
- **`SnapshotAll()` — every conversation's backlog, in one lock hold (#878).**
  `Snapshot` reads one named conversation; `convs` is private, so there was no way
  to enumerate the conversations holding a backlog at all. `SnapshotAll` takes
  `q.mu` for the whole read — a single consistent instant across every
  conversation, no TOCTOU between enumerating and reading one — and for each
  `convs` entry projects the items exactly as `Snapshot` does (value-copy,
  including the in-flight head), keyed into a fresh `map[string][]QueuedMessage`.
  It **skips any conversation whose `items` is empty**: `shrinkLocked` leaves a
  drained-but-retained `convQueue` in `convs` with `items == nil`, so without this
  filter a fully-drained conversation would still surface an entry. This filter
  *is* the connect-time queue reconcile's AC3 enforcement point — see
  [`v2-session-manager.md` § Connect-time queue reconcile](v2-session-manager.md#connect-time-queue-reconcile-878--outstandingqueues-seam--reconcilequeues).
  A pure read: mints no id, dequeues nothing. Built for, and so far only consumed
  by, the `cmd/pyry` `outstandingQueues` adapter that feeds `internal/relay`'s
  `OutstandingQueues` seam.
- **`Remove(convID, id)` — a waiting head is droppable; the advance is what keeps
  that safe.** `Enqueue` only ever appends to the tail, so `Remove` is the
  **first** op that can touch index 0. It refuses index 0 **iff** `c.committing`
  (narrowed from `c.draining` in #1085), decided under the same `q.mu` that sets
  and clears the flag, so the claim-versus-drop decision is atomic: either
  `commitGate` claims the head first and the drop no-ops, or the drop lands first
  and the gate refuses the write. `committing` is set only once the delivery seam
  is past the idle-gate wait and is actually writing, so a head that is merely
  **waiting** for claude to go idle (`draining && !committing`) is droppable **by
  design** (#487) — that is the message a user queued behind a running turn
  precisely so they could still cancel it — and dropping it cancels the in-flight
  delivery ctx so the seam's wait unblocks at once. Non-head removal (`idx >= 1`)
  is always safe: the drain only touches index 0 and holds a value copy of the
  head. Unknown conv / unknown / already-delivered id / committing head ⇒ `false`
  no-op (no panic, no reorder); surviving order preserved. So `dequeue_message`
  (the #723 handler, via the `QueueRemover` seam) **cannot cancel a write already
  in progress**, but can cancel anything before it.

  **Second caller since #2477: a conversation reset drops the whole backlog
  before its wrap-up turn**, one `Snapshot` + one `Remove` per queued id,
  precisely so `msgqueue`'s own drain cannot deliver a queued message into the
  idle window the wrap-up needs. It tolerates the same committing-head refusal
  `dequeue_message` does and for the identical reason — past that gate the
  message is already going into the outgoing child, so the reset's "drop the
  backlog" goal is already satisfied for that one id without `Remove` needing
  to be made total. `Remove`'s own change-notify republishes the emptied
  `queue_state`, so the reset needs no second publish of its own. See [Inbound
  new_session § The wrap-up
  turn](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#the-wrap-up-turn-and-the-replys-tense-2477).

  Head-drop safety does **not** come from this gate — it comes from
  `advanceLocked`, which drops the front only when the FIFO is non-empty **and**
  its front still carries the id the drain attempted (#1484). See § Concurrency
  model. Until #1484 the argument ran the other way (`draining == true` ⟹ no-op
  on the head ⟹ a blind `items[1:]` can never drop the wrong message); #1085
  retired the premise while the blind splice stayed, which is the bug #1484
  fixed.
- **`shrinkLocked` — shared backing-array hygiene.** The trailing "release at
  empty / compact when `cap > 2*len`" `switch` was lifted out of `advanceLocked`
  into `convQueue.shrinkLocked()`, now called by both `advanceLocked` (head drop)
  and `Remove` (mid-FIFO drop) — extracted, not duplicated, so a mid-FIFO removal
  doesn't leave a stale backing array. (`Remove`'s slice-delete leaves the freed
  `queued` value — holding the untrusted `text` — beyond the new `len` until
  `shrinkLocked` compacts, exactly as `advanceLocked`/`items[1:]` already does;
  consistent precedent, deliberately not zeroed.)
- **Change-notification fires after unlock, four sites.** A `notify(convID)`
  helper does the `onChange != nil` nil-check and is always called **after** `q.mu`
  is released, only on a real change: `Enqueue` (restructured from `defer
  q.mu.Unlock()` to explicit unlock + `notify`), the drain's delivery-advance
  (after `advanceLocked` drops a confirmed head — **not** on the empty-exit or
  delivery-error/retry paths), a successful `Remove` (no-ops fire nothing), and
  (#1000) `giveUp` — the abandoned head also leaves the backlog, so `notify`
  fires there too, keeping the wired `queue_state` producer's view correct after
  a give-up drop. Firing strictly after unlock is what makes a re-entrant
  `OnChange` (a consumer that calls back into `Snapshot`/`Remove`/`Enqueue`)
  re-acquire the lock cleanly and not deadlock against the goroutine that fired
  it. `OnChange` carries only `convID`; the seam is edge-triggered and the
  consumer coalesces by re-reading `Snapshot` (matches how the #647 reconnect
  path treats `eventring`).

## Bounded give-up on persistent delivery failure (#1000)

The drain's retry loop is deliberately lossless: a `Deliver` error retries the
**same** FIFO head after `RetryInterval`, forever, by design — that's what
bridges a claude-**child** respawn. Before #1000 that loop had no upper bound,
so a claude session parked at startup (an unanswerable dialog, a wedged
readiness gate, a network stall) looped on a head that never drained and never
failed, leaving the client with a message that neither ran nor errored. #1000
adds a **bounded** give-up while preserving losslessness for the case it exists
to bridge.

- **The bound is elapsed-time, not attempt-count or error-identity.** `Deliver`'s
  errors (`ErrNoLiveSession`, `ErrTurnNotCommitted`, PTY write errors) are
  **indistinguishable by type** between an ordinary respawn and a persistent
  wedge — the drain cannot tell "retry, this will clear" from "give up, this is
  stuck" by inspecting `err`. `Config.GiveUpAfter` (`<= 0` ⇒
  `defaultGiveUpAfter`, 2 minutes) is instead a per-head elapsed-wall-clock
  deadline, measured from the head's **first** consecutive delivery failure via
  a `drain`-local `firstFailedAt`. `defaultGiveUpAfter` is chosen to exceed
  `internal/supervisor`'s max backoff window with margin (`BackoffMax=30s`,
  `BackoffReset=60s`) — a transient failure spanning a full claude-child
  respawn/backoff cycle clears well inside the bound and never trips give-up.
- **Per-head, not per-session.** A successful delivery resets `firstFailedAt` to
  zero for the next head, so a wedge that clears for one message doesn't poison
  the bound for the next — each head gets a fresh give-up window.
- **Give-up drops one head and exits the drain, it does not skip to the next
  head — when there is still a head to abandon.** Because a startup wedge would
  fail every subsequent head identically, continuing would burn a full
  `GiveUpAfter` window per head and emit one give-up event per queued message for
  a single wedge. So when the front of the FIFO is still the head that failed,
  `giveUp` drops only that head (`advanceLocked`), clears `draining`, fires both
  seams off-lock (`notify(OnChange)` then `notifyGiveUp(OnGiveUp)`, in that order —
  clearing `draining` first means an observer can safely re-`Enqueue` without
  racing a still-`true` flag), and the drain goroutine **returns**. Any items
  still behind the dropped head stay queued; `draining == false` with
  `len(items) > 0` is exactly `maybeSpawnDrainLocked`'s respawn precondition, so
  the next `Enqueue` respawns the drain — the same lifecycle the pre-existing
  empty-exit path already uses.
- **A head dequeued inside the failure window is cancelled, not abandoned
  (#1484).** `Remove` can take a merely-waiting head at any point in the window,
  including after the drain's post-delivery `dropped` read. When it has, the
  id-checked `advanceLocked` drops **nothing**, and nothing is reported either:
  the advance decides **before** the `Warn`, so `OnGiveUp` never fires, no
  give-up line names that id, and the backlog behind the cancelled head is
  untouched. `draining` stays set and the drain **continues** with a fresh
  give-up clock — the same treatment the loop already gives a head dropped before
  it committed ("a clean cancellation, not a delivery failure"). The
  exit-don't-skip rule above bounds *abandonment*; here nothing was abandoned.
  `giveUp`'s bool return is welded to that: `true` ⟺ `draining` was cleared ⟺ the
  drain returns. Decoupling them would either strand the conversation (a live
  drain with `draining == false` lets `maybeSpawnDrainLocked` start a second one)
  or block respawn forever.
- **`GiveUpFunc` mirrors `ChangeFunc`'s seam contract** (never under `q.mu`, must
  not block, concurrency-safe, nil ⇒ disabled) and carries a daemon-generated
  `reason` (the elapsed window + `err.Error()`) that **never** contains
  `head.text` — extending the package's `NEVER log head.text` discipline (see §
  Error handling) to both the new give-up log line and the seam payload.
- **Shipped unwired, now live.** `OnGiveUp` was `nil` in production at this
  ticket's landing — same engine-first rhythm as the package's original #704
  landing (mechanism first, wired later). Split from #1001 into wire vocabulary
  (#1007 — `TypeSessionError`/`CodeSessionBlocked`/`SessionErrorPayload`, shipped
  unwired) and its blocked-by-#1007 producer sibling (#1008, shipped), which set
  `OnGiveUp` to a live `session_error` producer (`cmd/pyry/session_error_v2.go`)
  exactly as `OnChange` routes to the `queue_state` producer — a persistent
  delivery failure now surfaces as a typed, client-visible frame instead of a
  silently dropped head.

- **The bound is conditionally exempt, gated per conversation, not per error type
  (#1911).** `Config.Pending` was left unset from #1348 (which deleted the only
  producer of `#1014`'s `supervisor.ErrTrustModalPending`) until the stream path
  needed the same exemption for a head held behind an approval parked on a
  person. Because `PendingFunc` is blind to the conversation, the composition
  root cannot ask "is a person deciding on this conversation?" inside it — that
  question is answered at the delivery seam, which re-marks *only* the hold
  error (not every error the seam can return) before it reaches `Pending`. That
  precision is what keeps the bound satisfiable: exempting every delivery error
  during a parked approval would let a conversation that is separately and
  genuinely wedged (a failing `resolve`, a dead `Activate`) ride along on
  somebody else's decision time indefinitely. See
  `cmd/pyry`'s `approvalParkedReport`/`markApprovalHolds`/`approvalHoldPending`
  and [features/streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md).

See [codebase/1000.md](../codebase/1000.md), [codebase/1007.md](../codebase/1007.md),
[codebase/1008.md](../codebase/1008.md).

## Delivered notification (#2115)

A fourth optional seam beside `OnChange`/`OnGiveUp`/`Pending`, added so the
operator's own typed message could get a producer into the durable
per-conversation log ([history-package.md § Producers](history-package.md#producers-2114-2115))
without touching `DeliverFunc`. `DeliveredFunc(convID string, msg QueuedMessage)`
fires from the drain, after `q.mu` is released, once per **confirmed**
delivery — unconditional on `advanced`, unlike the neighbouring `q.notify`:
that guard is about the backlog, and once the write is confirmed the text has
reached claude's stdin and will be answered, so a `Remove` racing the commit
cancels nothing that already happened.

**Why `QueuedMessage` and not a bespoke parameter list.** `DeliverFunc`'s 64
`Deliver:` literals made a signature widening unsplittable, which forced an
additive seam — but the parameter *type* was still a choice, and reusing the
existing `Snapshot`/`SnapshotAll` projection rather than inventing
`func(convID, messageID, text string)` was the one that mattered. `QueuedMessage`
declares no `delivery` field, so this seam is *structurally* incapable of
handing a consumer the daemon-composed payload that may name an on-host path
(§ Security, `delivery`) — a consumer cannot leak it by forgetting a filter,
where an all-strings signature would have compiled just as well with `text`
and `messageID` transposed. **A projection type shaped to omit a field for one
consumer's sake is a leak barrier that composes to a new consumer for free —
reach for it before inventing a parameter list.**

**A negative-only test proves nothing until its fire site exists.** Built RED
in two steps (declare the seam, then wire the fire site), the give-up and
removed-before-commit assertions here were green from the moment the seam was
declared and stayed green with no fire site wired at all — nothing
distinguished "correctly silent" from "cannot possibly fire yet" except a
paired positive test's timeout. A suite of purely negative assertions gives no
RED signal; pair every "does not fire" case with a "does fire" case exercising
the same path.

`QueuedMessage.TS` on the delivered value is the **enqueue** timestamp, not the
confirmation time — a durable-record consumer wanting ordering must mint its
own (`internal/history`'s producers all hoist `time.Now().UTC()` at their own
commit point, never from this field).

## Concurrency model

- **Goroutines:** one `Run` goroutine (the daemon spawns it in `runSupervisor` and
  joins it via a buffered `qDone` channel after `pool.Run` returns, #721) + **one
  drain goroutine per active conversation**, spawned lazily
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
  **after** a confirmed commit, under the lock again — and it advances **by id**.
  A concurrent `Enqueue` can only append (FIFO tail), but a concurrent `Remove`
  **can** take the head, deliberately: a merely-waiting head is droppable for the
  whole idle-gate wait and for the drain's post-delivery bookkeeping (#487,
  #1085). So `advanceLocked(head.id)` drops the front only when the FIFO is
  non-empty **and** its front still carries the id the drain attempted — the same
  predicate `commitGate` uses to accept or refuse the write, now asked in the
  lock hold that actually splices. That makes check-and-splice atomic against
  `Remove` under the one leaf lock, **closing** the window rather than narrowing
  it: a `Remove` of the in-flight head turns the advance into a **no-op** instead
  of dropping the message queued behind it, or panicking on the empty slice the
  removal left (`shrinkLocked` sets `items = nil`). Both advance sites carry the
  check — the confirmed-delivery one and `giveUp`'s (#1484).
- **Lazy-spawn / exit-on-empty race** is closed under a single lock hold: the
  empty-check and the `draining = false` write are atomic w.r.t. a concurrent
  `Enqueue`, which either appends before the drain takes the lock (drain sees
  `len > 0`, keeps going) or after it released (sees `draining == false`, respawns).
  No interleave loses a message.
- **Shutdown:** parent ctx cancel → `deliver`'s `WaitReady` returns ctx error (or
  `sleepCtx` returns false) → each drain clears `draining` and returns → `wg.Done`
  → `Run`'s `wg.Wait` unblocks → `Run` returns `ctx.Err()`. No drain outlives `Run`.
  A delivery that confirms in the same instant shutdown begins can land in the
  window `drain` checks `ctx.Err()` **before** the confirmed-delivery branch —
  that head is left queued and fires neither `q.notify` nor (#2115)
  `OnDelivered`: the message reached claude's stdin but produces no downstream
  record. Inherited from #487/#1484's ordering, not introduced by any one seam;
  a consumer that persists what a seam reports (`internal/history`) is not
  gap-free across a daemon restart for this reason.

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
- **No silent drop.** Every message is either delivered (head advances), still
  queued (retry / awaiting drain), or (#1000) explicitly abandoned past the
  bounded give-up window — logged and, once wired, surfaced to the client via
  `OnGiveUp`, never dropped without a trace.

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
- **`delivery` (#2038) carries the same opaque-transit, never-logged discipline
  as `text`, and is structurally incapable of reaching a client.**
  `EnqueueDelivery` lets a caller enqueue a payload distinct from what a client
  reads back — `internal/relay/handlers.SendMessage` uses it to hand claude a
  composed prompt naming a stored attachment's on-host path while `text` stays
  the user's own words, which `docs/protocol-mobile.md` § Error codes forbids
  putting on the wire. `QueuedMessage` gains no `delivery` field, so
  `Snapshot`/`SnapshotAll` — and so `queue_state`, on both the enqueue push and
  the connect-time reconcile — are structurally incapable of projecting one; a
  future consumer that wants the path on the wire has to widen the exported
  type to get it, rather than merely forgetting a filter. `Enqueue` is
  `EnqueueDelivery(convID, text, text)`, so none of its ~108 existing call
  sites needed touching.
- **`messageID` (#2092) is untrusted like `text`, but travels the opposite
  direction on purpose.** It is the client's own id for the `send_message` that
  produced this record, stored verbatim and projected by both `Snapshot` and
  `SnapshotAll` so `queue_state` can carry it back out — deliberately echoed to
  every paired device, as the key a client merges its own optimistic echo on.
  Nothing in this package or its consumers reads it to route, authorize, match
  or dedupe; a colliding id across two devices is a client-local
  merge-attribution question (`docs/protocol-mobile.md` § Queue), not a daemon
  trust decision. This is the same shape #2038's `attachment_id` warned about: a
  doc comment that enumerated `Text` as *the* untrusted field went stale the
  moment a second untrusted field landed beside it, so `QueuedMessage`'s and
  `QueuedItem`'s comments now name both.
- **`convID` is a map key only.** Validating/resolving it to a real session is the
  **caller's** job, upstream of `Enqueue` (the `SessionRouter` / `ValidateConversation`
  in `send_message.go`). A hostile `convID` can at worst create an isolated FIFO that
  never drains — **never reach another conversation's session** (per-conversation maps
  + per-conversation drains are isolated by construction). No type-system signal is
  added, matching the `WriteUserTurn(ctx, id, payload)` convention where `id` is
  pre-validated by the caller.
- **No tokens/secrets/crypto/file/subprocess surface.** The `id` is a non-secret
  per-conversation counter, not a capability.
- **Inbound bound / backpressure — implemented (#869).** A phone flooding
  `send_message` while claude is persistently busy/wedged used to grow
  `convs[convID].items` without bound (an in-memory DoS; each queued message up
  to the transport's 1 MiB frame ceiling). #869 closes it at the single insertion
  point: `Config.MaxQueuedPerConversation` (`<= 0` ⇒ `defaultMaxQueuedPerConversation`,
  100) caps each conversation's not-yet-delivered backlog (`len(c.items)`,
  including the in-flight head). Past the cap, `Enqueue` **rejects** — returns `0`
  (never a valid id) without appending, consuming no id, notifying nothing,
  spawning no drain — **reject, never drop**, preserving losslessness (the mirror
  of ADR 025's "control never drops": inbound content gets "tell the sender," not
  silent-drop or drop-oldest). The `uint64` return arity is unchanged — `0` rides
  an already-impossible id value, so none of the ~45 pre-existing `Enqueue` call
  sites needed touching. The `send_message` handler maps `id == 0` to a retryable
  `protocol.CodeServerBinaryBusy` ("server.binary_busy") envelope and does not
  ack, so the phone re-issues once the backlog drains. Per-conversation only — N
  conversations can still hold up to N×cap; a daemon-wide ceiling is out of scope
  (matches `eventring`'s per-conversation `MaxEventsPerConversation` posture). See
  [codebase/869.md](../codebase/869.md).
- **`Snapshot` / `Remove` boundary crossings (#719) — convID trust is the
  consumer's job.** Both key by **caller-supplied `convID`**, and `Snapshot`
  *returns* the opaque `text` and (#2092) `messageID` (both will flow out to a
  phone via `queue_state`). Per-conversation maps give isolation-by-construction
  **once `convID` is trusted**; trusting it is #705's job (bind `convID` to the
  requesting phone's authorized conversation, exactly as `send_message` does).
  Cross-conversation **confidentiality** (`Snapshot` reading another conv's
  queued text) and **integrity** (`Remove` mutating another conv's FIFO) are
  prevented only there. The engine adds **zero** new log lines — `Snapshot`
  returns `text` and `messageID` as values, never logs either; the consumer must
  preserve the "never logged" discipline across the new exit for both. `Remove`
  never touches `nextID`, so the stable-id contract is preserved and a removed id
  is simply never reused. ADR 025 § Security model lists viewing/dequeuing as a
  paired phone's **ungated** capability (only answering permission-class modals
  is gated), so no permission gate is needed.
- **`SnapshotAll` boundary crossing (#878) — same posture as `Snapshot`, no new
  input trust decision.** `SnapshotAll` takes **no** caller-supplied parameter —
  it enumerates the engine's own `convs` keys, so there is no `convID` to trust or
  mistrust at this call. It **returns** the same opaque `text` and (#2092)
  `messageID` `Snapshot` does, fanned out to `internal/relay`'s connect-time
  reconcile ([#878](../codebase/878.md), `security-sensitive`) and from there to
  a possibly-untrusted v2 peer — the reconcile's own gates (Noise_IK auth +
  `interactive` capability + unicast addressing) are the trust boundary, not
  this engine. Zero new log lines; the never-logged discipline is unchanged for
  both fields.

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
├── delivered_test.go         #2115: fires once on confirmed delivery carrying text (never
│                             delivery), exactly one call across retries, no call on give-up,
│                             no call when the head is removed before commit, fires even when a
│                             Remove races a confirmed delivery (the test that pins "unconditional
│                             on advanced" against an `if advanced` regression)
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

Stdlib-only (`context`, `errors`, `fmt`, `log/slog`, `sync`, `time`); imports **no**
`internal/*` package — the delivery path arrives as the injected `DeliverFunc`,
the change-notification path as the injected `ChangeFunc`, and (#1000) the
give-up-notification path as the injected `GiveUpFunc`.

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
  protocol (`send_message` queued-by-daemon, the `{queued_msg_id, message_id, text, ts}`
  record — `message_id` added by #2092).
- [codebase/721.md](../codebase/721.md) — the **live wiring** ticket record: the
  `cmd/pyry` constructor, `newInboundDeliver` delivery seam, the `Route`/`resolve`
  split (the drain re-resolves without stamping the #687 cursor), and the
  enqueue-and-ack contract change in `send_message`.
- **Consumers:** **#722 landed the `queue_state` producer** ([codebase/722.md](../codebase/722.md))
  — `cmd/pyry`'s `queueStateEmitterV2` hooks `OnChange`, re-reads via `Snapshot`, and
  fans a per-conversation `queue_state` to interactive phones (the first live consumer
  of `OnChange` + the read side of `Snapshot`). **#723 landed the `dequeue_message`
  handler** ([codebase/723.md](../codebase/723.md)) — `internal/relay`'s
  `handleDequeueMessage` (via the consumer-declared `QueueRemover` seam) calls `Remove`
  on an interactive phone's inbound control frame, and its successful-removal `notify`
  drives the #722 producer to refresh `queue_state` (the **first live consumer of
  `Remove`**; the in-flight-head/unknown-id `false` no-op is what makes a hostile or
  stale id a safe no-op). Both #722/#723 split from the original #705 reporting/removal
  slice. **[#869](../codebase/869.md) landed the deferred inbound bound**: a
  per-conversation `MaxQueuedPerConversation` cap (default 100) at `Enqueue`,
  reject-never-drop, mapped by `send_message` to a retryable `server.binary_busy`
  reply. **[#878](../codebase/878.md) landed the connect-time reconcile enumeration
  seam**: `SnapshotAll` (every non-empty conversation's backlog in one lock hold),
  consumed by `cmd/pyry`'s `outstandingQueues` adapter and, through it,
  `internal/relay`'s `OutstandingQueues` seam — see
  [`v2-session-manager.md` § Connect-time queue reconcile](v2-session-manager.md#connect-time-queue-reconcile-878--outstandingqueues-seam--reconcilequeues).
  **[#1000](../codebase/1000.md) landed the persistent-failure give-up bound**: a
  per-head elapsed deadline (`GiveUpAfter`, default 2m) that abandons a
  persistently-failing head instead of retrying it forever, plus an `OnGiveUp`
  seam mirroring `OnChange` — split from #991 as the not-security-sensitive
  engine-side half; ships unwired (nil) until the wire+producer sibling (#1001,
  itself re-split at its vocab seam into wire vocabulary #1007 — shipped,
  unwired — and producer+wiring #1008, security-sensitive, blocked-by-#1007)
  lands. Nothing remains deferred on this engine except that wiring.
- **[#1199](../codebase/1199.md) closed a `DeliverFunc`-contract gap on the stream-json runner,
  engine-side unchanged.** The package's contract — `DeliverFunc` "MUST block while claude is busy …
  that blocking IS the drain's turn-end pacing" (`queue.go:87-92`) — held for the PTY delivery seam
  (`supervisor.WriteUserTurn` gates on `waitReadyAutoContinue`) but not for stream-json:
  `streamsup.Runner.WriteUserTurn` returns as soon as the envelope is in the child's stdin pipe, so the
  drain emptied as fast as it could write and the queued-backlog UI / drop-before-drain control never
  appeared on that runner. `newInboundDeliver` now waits on `cmd/pyry`'s per-conversation
  `turnBusyTracker` (`waitIdleForDelivery`, bounded by `streamTurnHoldTimeout`) and marks the
  conversation busy (`openForDelivery`) both between `Activate` and the write — placed *before*
  `WriteTurn`'s `turncommit` claim, which is what keeps the head `draining && !committing`, i.e.
  droppable, for the whole wait. `Remove`, `commitGate`, and the give-up bound above are consumed
  exactly as they stand; nothing in this package changed. On PTY the tracker is nil and both calls are
  no-ops — `newInboundDeliver`'s body is semantically unchanged there.
- **#1911 gave `Config.Pending` its first live producer since #1348 removed
  `supervisor.ErrTrustModalPending`'s.** A head held by #1199's stream-path hold is exempted from
  `GiveUpAfter` for as long as a person is being asked to approve something on that conversation
  (`streamApprovalBridge.ApprovalParked`, #1919) — see § Bounded give-up above for the gating
  pattern. Engine-side unchanged apart from the doc comments on `PendingFunc` and `Config.Pending`,
  which no longer name the deleted trust-modal producer as the wiring.
- **#2115 added `OnDelivered`, the engine's fourth seam and the first
  consumed outside `cmd/pyry`'s wire producers.** `cmd/pyry`'s `newOperatorMessageHistory` hooks it
  to write the operator's own typed message into [`internal/history`](history-package.md)'s durable
  conversation log — the first consumer for which `newInboundDeliver` (the delivery seam itself)
  could not have worked, since it sees only the composed `delivery` payload and never `text`. See
  § Delivered notification above and
  [history-package.md § Producers](history-package.md#producers-2114-2115).
