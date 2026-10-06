# Send-now delivery — `SendNowFunc` + `Queue.SendNow` (#2729)

Part of [`internal/msgqueue`](msgqueue-package.md). A phone's **Send now** writes a
named queued message into the conversation's **running** turn instead of waiting
for the drain to reach idle — the counterpart to `Remove`'s cancel, now for
acceleration rather than withdrawal.

```go
// SendNowFunc is the injected send-now seam: it writes a queued message's
// delivery payload into the conversation's RUNNING turn, without waiting for
// idle. nil ⇒ SendNow is inert.
type SendNowFunc func(ctx context.Context, convID string, id uint64, payload []byte) error

func (q *Queue) SendNow(convID string, id uint64) bool
```

`SendNowFunc` gained `id` in #2730: `Queue.SendNow` passes `m.id` straight
through. Legacy callers use it to join `place.expect` with `place.attach`
from `notifyDelivered`. Queue-backed writes now also carry the safe
`DeliveryMessage(ctx)` projection (#2820), so `place.write` prepares the
commit before writing and a later callback only acknowledges it. Ordinary
and send-now registration follows actual write order per conversation;
matching by client `message_id` would confuse duplicate ids and equal payloads.
See [history-package.md § Producers](history-package.md#producers-2114-2115).

`Config.SendNow` is the fourth optional caller-supplied seam beside
`OnChange`/`OnGiveUp`/`OnDelivered`/`Pending`; `nil` makes `SendNow` an
unconditional `false` no-op, matching the package's shipped-unwired-first
rhythm. `QueuedMessage` gained one field, `SentNow bool`, set only on the
delivery projection of a message `SendNow` writes — it is how a consumer that
also hangs off `OnDelivered` (the channel-carry clear, see
[control-plane.md § Carrying a posted channel message into claude's next
turn](control-plane-channel-post-carry.md#carrying-a-posted-channel-message-into-claudes-next-turn-2499))
tells a send-now delivery apart from an ordinary drain delivery of the same
head.

## Taking the message out of the FIFO is what makes every race already-solved

`SendNow(convID, id)` finds the item under `q.mu`, refuses the same two cases
`Remove` refuses (unknown id; the head while `commitGate` has marked it
`committing`), and otherwise **deletes it from `items` before releasing the
lock** — the message is gone from the backlog for the whole duration of the
write, not merely marked. That single fact is what settles every concurrency
question this feature raises using machinery the package already had:

- A concurrent `Remove` of the same id finds nothing — it can't double-take
  what `SendNow` already removed.
- Taking the **waiting** head cancels its in-flight delivery attempt
  (`c.deliverCancel`, the same cancel `Remove` fires) so the drain's
  `waitIdleForDelivery`-equivalent wait unblocks, and the drain treats the
  attempt as dropped — **exactly the `dequeue_message` path**, reused rather
  than re-implemented.
- The write itself (`q.sendNow(ctx, convID, []byte(m.delivery))`) runs with
  `q.mu` released, on the caller's goroutine, using `m.delivery` — the same
  composed-payload/client-text split `EnqueueDelivery` (#2038) established, so
  a send-now write carries the identical bytes an idle drain would have sent.

A seam error — the turn is idle, the session can't take input mid-turn, no
live child — puts the message back via `reinsert`, at the id-ordered position
it originally held (ids are monotonic, so "first item with a larger id" is
always the right slot), **except** it never lands in front of a head the
drain is actively `committing`, where it lands second instead. No `notify`
fires on this path: the backlog is back exactly as it was, so there is no
change to report. A drain that had exited on an empty FIFO is respawned.

On success: `notify` fires (the `queue_state` without this item), then
`notifyDelivered(convID, m, sentNow: true)` — the one call site that threads a
`true` through where every other caller of the shared `notifyDelivered`
passes `false`.

## `headTaken` — the drain must read a refused head's own re-insertion as a drop, not a failure

The gap a rework ticket closed (2026-10-03): a `SendNow` take of the **waiting
head** cancels that attempt, but `reinsert` can run **before** the drain
re-acquires `q.mu` after the cancelled write returns. If the seam error path
won, `reinsert` could put the message back **at the front** before the drain's
own post-attempt check — and the drain would then see its own head still at
index 0, read `err == context.Canceled`, and book a real delivery failure: a
`Warn`, a retry sleep, and `firstFailedAt` latched. One such event is harmless
on its own, but it starts a give-up streak that a later, unrelated transient
failure on a long turn could complete — turning a user's own **Send now** tap
into the trigger for abandoning their message.

`convQueue.headTaken` closes it. `SendNow` sets it (`c.headTaken = cancel !=
nil`) in the same locked section that takes the head, whenever the item it
took was sitting at index 0 with a delivery in flight. The drain's existing
drop check —

```go
dropped := len(c.items) == 0 || c.items[0].id != head.id || c.headTaken
```

— now reads `headTaken` as a third, independent reason the attempt is a clean
drop, **whether or not** a failed send-now has since put the head back at the
front. `headTaken` is cleared at the start of every attempt (so a later,
ordinary failure on the same head is never misread) and again right after the
drop check is evaluated (so it cannot leak into the next iteration). A
*successful* send-now needs no flag at all: `err == nil` is decided before the
drop check runs, so a confirmed write is never reachable through this path.

Pinned by `TestQueue_SendNow_RefusedHeadStartsNoGiveUpStreak`, which forces the
reinsert-before-redrain ordering the race depends on. The other engine-side
tests (`internal/msgqueue/send_now_test.go`) cover: taking a non-head item
mid-backlog; a seam error re-inserting a non-head item in place; the nil-seam
inert default; and the committing-head refusal shared with `Remove`.

## What this feature deliberately does not touch

- **No new log line.** `SendNow` logs nothing; the relay-side caller logs only
  `conn_id`/`conversation_id`/`queued_msg_id`, mirroring `dequeue_message`'s
  discipline — never the text, the delivery payload, or the client's message
  id.
- **No change to `commitGate`/`advanceLocked`.** Both already treat "my head
  is no longer at the front" as the uniform signal for "something else took
  it," which is precisely what a `SendNow` take looks like to them.
- **Not wrapped in the delivery-side decorators.** The daemon's `SendNow` seam
  (`cmd/pyry.newSendNowDeliver`) bypasses `channelCarry.carryPending` and
  `markApprovalHolds` on purpose — see
  [control-plane.md § Carrying a posted channel message into claude's next
  turn](control-plane-channel-post-carry.md#carrying-a-posted-channel-message-into-claudes-next-turn-2499)
  for why consuming the waiting head's composed channel-post count here would
  be a correctness bug, not a missed optimization.

See [`docs/protocol-mobile.md` § `send_queued_now`](../../protocol-mobile.md#send_queued_now)
for the wire contract this engine op serves, and
[v2-session-manager-state-machine-inbound-send-queued-now-queuesender-sea.md](v2-session-manager-state-machine-inbound-send-queued-now-queuesender-sea.md)
for the relay-side handler.
