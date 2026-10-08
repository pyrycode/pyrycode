# Message queue API and backlog observation

Part of [`internal/msgqueue`](msgqueue-package.md).

## Exported surface

```go
type DeliverFunc func(ctx context.Context, convID string, payload []byte) error
type ChangeFunc func(convID string)
type GiveUpFunc func(convID, reason string)
type DeliveredFunc func(convID string, msg QueuedMessage)
type AcceptedFunc func(convID string, msg QueuedMessage)
type TerminalFunc func(convID string, msg QueuedMessage, outcome TerminalOutcome)
type PendingFunc func(error) bool
type SendNowFunc func(ctx context.Context, convID string, id uint64, payload []byte) error

type TerminalOutcome string
const (
    TerminalDelivered TerminalOutcome = "delivered"
    TerminalRemoved   TerminalOutcome = "removed"
    TerminalGiveUp    TerminalOutcome = "give_up"
)

type QueuedMessage struct {
    ID            uint64
    MessageID     string
    Text          string
    TS            time.Time
    AttachmentIDs []string
    DeviceID      string
    DeviceName    string
    ClientVersion string
    ClientSentAt  time.Time
    SentNow       bool
}

type Config struct {
    Deliver                  DeliverFunc   // required
    RetryInterval            time.Duration // <= 0: 1s
    MaxQueuedPerConversation int           // <= 0: 100
    OnChange                 ChangeFunc    // optional
    GiveUpAfter              time.Duration // <= 0: 2m
    OnGiveUp                 GiveUpFunc    // optional
    OnDelivered              DeliveredFunc // optional
    OnAccepted               AcceptedFunc  // optional
    OnTerminal               TerminalFunc  // optional
    Pending                  PendingFunc   // optional
    SendNow                  SendNowFunc   // optional
    Logger                   *slog.Logger  // nil: slog.Default()
}

func New(cfg Config) (*Queue, error)
func (q *Queue) Enqueue(convID, text string) uint64
func (q *Queue) EnqueueDelivery(convID, messageID, text, delivery string) uint64
func (q *Queue) EnqueueAttached(convID, messageID, text, delivery string, attachmentIDs []string) uint64
func (q *Queue) EnqueueSent(convID, messageID, text, delivery string, attachmentIDs []string, deviceName, clientVersion string, clientSentAt time.Time) uint64
func (q *Queue) EnqueueIdentified(convID, messageID, text, delivery string, attachmentIDs []string, deviceID, deviceName, clientVersion string, clientSentAt time.Time) uint64
func (q *Queue) Snapshot(convID string) []QueuedMessage
func (q *Queue) SnapshotAll() map[string][]QueuedMessage
func (q *Queue) Remove(convID string, id uint64) bool
func (q *Queue) SendNow(convID string, id uint64) bool
func (q *Queue) Run(ctx context.Context) error
func DeliveryMessage(ctx context.Context) (QueuedMessage, bool)
```

`New` returns an error on nil `Deliver`; every observer is optional and nil
independently disables it. `Deliver` must wait while Claude is busy and return
nil only on confirmed delivery: that blocking provides FIFO pacing. Errors
retry the same head. `Pending` classifies legitimate holds and resets the
per-head give-up streak; nil exempts no errors. Nil `SendNow` makes that operation
return false. See [give-up](msgqueue-package-give-up.md) and
[send-now](msgqueue-package-send-now.md) for those contracts.

**Identified and legacy enqueue (#2970).** `EnqueueIdentified` stores `deviceID`
verbatim as `DeviceID`, independently of the display name `DeviceName`.
Identity is opaque metadata; the queue neither authenticates nor authorizes it,
uses it for deduplication, nor logs it. Identity provenance is the relay's
responsibility (#2971). `EnqueueSent` delegates with an empty device id;
`EnqueueAttached`, `EnqueueDelivery` and `Enqueue` still delegate through it and
retain their signatures. All legacy callers therefore project empty identity.
The wrappers must not acquire `q.mu` before delegating: it is not re-entrant.

Every enqueue returns a monotonically increasing per-conversation id starting
at 1, or zero at capacity. Rejection appends nothing, consumes no id and invokes
neither acceptance nor change notification. With `OnAccepted` configured, every
successful enqueue through any API reports exactly one acceptance synchronously
on the enqueue caller before returning, even before `Run` starts. Enqueue never
waits for delivery, but observers must return promptly. Delivery can proceed
while acceptance runs; `OnDelivered` and `OnTerminal` for that message wait until
acceptance completes. See [confirmation and concurrency](msgqueue-package-lifecycle.md).

**Safe projections.** `OnAccepted`, `OnTerminal`, `OnDelivered` and
`DeliveryMessage(ctx)` carry the message's queue `ID`, opaque `DeviceID`, app
`MessageID`, client-readable `Text`, attachment ids, acceptance/enqueue time
`TS`, display name, app version and reported send time `ClientSentAt`.
Conversation identity is the observer's separate `convID` argument, or the write
seam's conversation argument for `DeliveryMessage`. `ClientSentAt` is the time
supplied by the caller (the relay parses it to UTC), zero when absent; `TS` is
never a delivery-confirmation timestamp. Successful send-now delivery and its
terminal projection set `SentNow`; acceptance leaves it false.

`Snapshot` and `SnapshotAll` preserve `DeviceID` alongside `ID`, `MessageID`,
`Text` and `TS`. Their existing narrower policy remains: attachment ids, display
name, client version, reported send time and `SentNow` stay at zero values.
`DeliveryMessage` returns false outside a queue-backed write context; delivery
decorators preserve the context to retain metadata.

None of these projections contains composed delivery bytes or resolved host
attachment paths. `text` is client-readable content; `delivery` is the separate
payload handed to the write seam, and is a plain value with no empty-string
fallback. `Enqueue` explicitly supplies text as both halves. Attachment ids are
cloned on entry and for each lifecycle/delivery observation, including repeated
`DeliveryMessage` reads. Mutating the input slice or one observer's projection
cannot change queue state or another observer's projection. Untrusted client
content and sender metadata remain absent from queue logs; see
[security](msgqueue-package-security.md).

**Notification compatibility.** `OnChange` remains a conversation-only,
edge-triggered notification: consumers re-read snapshots after enqueue, FIFO
advance, removal or give-up. `OnGiveUp` retains its conversation id and
content-free reason; `OnDelivered` retains its safe message parameter and once
per confirmed-delivery behavior. Lifecycle consumers can additionally observe
`OnTerminal`'s message-specific delivered, removed or give-up fact without
migrating any existing consumer. All observers run off-lock and can re-enter
`Snapshot`/`SnapshotAll`; they must be concurrency-safe and return promptly.

Queue ids identify an acceptance only within its conversation and daemon run;
they reset when the daemon restarts. The queue remains memory-only. A durable
history writer must link terminal facts to its own acceptance record instead of
treating the queue id as a global durable key. Production lifecycle wiring and
that writer are owned by #2971/#2972 under
[ADR 042](../decisions/042-daemon-built-thread.md).

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
  `items`" maps to the waiting FIFO (explicit removal and a send-now take also
  remove items). It preserves `DeviceID` but leaves the other sender/attachment
  fields zero as described above. The snapshot does **not** flag
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
  its front still carries the id the drain attempted (#1484). See
  [Concurrency model](msgqueue-package-lifecycle.md#concurrency-model).
  Until #1484 the argument ran the other way (`draining == true` ⟹ no-op
  on the head ⟹ a blind `items[1:]` can never drop the wrong message); #1085
  retired the premise while the blind splice stayed, which is the bug #1484
  fixed.

  A successful `Remove` immediately changes the backlog, but when an attempt is
  outstanding its terminal outcome waits for the result. Confirmed delivery
  wins, including without a gate claim; otherwise it resolves `TerminalRemoved`.
  Removal without an outstanding attempt resolves directly. See
  [lifecycle arbitration](msgqueue-package-lifecycle.md#delivered-notification-2115).
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
