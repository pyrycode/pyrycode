# Send-now carry (#2729) — `openForSendNow` + `carry`/`carried` + `scheduleCarryRelease`

A send-now write ([msgqueue-package-send-now.md](msgqueue-package-send-now.md))
lands inside a turn that may end moments later. [The #2728
capture](e2e-realclaude-mid-turn-user-capture-test-go.md) measured what claude
does next: a message written during a running tool call folds into the
turn that is already ending, but one written while a text-only answer
streams, or just after the turn's last tool result, opens a **second** turn
of its own. Since #2730 the interactive spawn runs with
`--replay-user-messages`, and claude does echo the write back through the
fan-in — but only to `sendNowPlacement` ([history-package.md §
Producers](history-package-producers.md#producers-2114-2115)), which uses the echo to
place the operator-message push, never to this tracker. `turnBusyTracker`
stays echo-blind by design: `openForSendNow`'s carry and
`scheduleCarryRelease`'s grace below are what keep the conversation read
**busy** for the whole uncertain window regardless — if the drain reads idle
too early, it releases the next queued message into a turn that is not
actually over.

## `openForSendNow` — the atomic busy-check-and-carry

```go
func (t *turnBusyTracker) openForSendNow(conversationID string) (ok bool, undo func())
```

Returns `false` (recording nothing) when the conversation is not currently
busy — the caller's signal to write nothing at all and leave the message to
the idle drain, which is exactly `errSendNowIdle`'s trigger in
`cmd/pyry/send_now.go`. When the conversation **is** busy, it increments
`carry[conversationID]` (a count, not a bool — see below) under the same
lock acquisition as the busy check, so a `TurnEnd` landing immediately after
this call is guaranteed to observe the carry rather than racing it. The
returned `undo` decrements the count, for a write that goes on to fail; once
a close has already consumed the carry, `undo` finds nothing to take back
and the grace below is left to run its course.

## `observeMark` — a close with an outstanding carry does not close

`observe`'s existing open/close classification feeds into `observeMark`
rather than `applyBusyLocked` directly. An **opener** still clears any
`carried` mark outright — the conversation's next turn is visibly open, and
that turn's own `TurnEnd` will close it normally. A **closer** arriving while
`carry[conversationID] > 0` is intercepted: it does not call
`applyBusyLocked` at all. Instead it consumes the carry (`delete(carry,
conversationID)`), sweeps `inflight` for that conversation (whatever tool
call was running ended with the turn), mints a fresh generation
(`carryGen++`), records it in `carried[conversationID]`, and — **after
releasing `t.mu`** — calls `scheduleCarryRelease`. Every other close reaches
`applyBusyLocked` exactly as before #2729.

## Why the timer lives in `cmd/pyry/send_now.go`, not `stream_turn_busy.go`

`TestTurnBusyTracker_ImportsStayMinimal` pins `stream_turn_busy.go` against
importing `time` for a clock read — membership there moves only on an
event, by design, so the file stays a pure function of the fan-in. A folded
write is the one case with no further event to move on, so the release has
to be a **bound** instead, and the bound is this feature's, not the
tracker's general contract. `scheduleCarryRelease` and `sendNowGrace` (10s,
"a tuning knob, not a contract") therefore live in `send_now.go`:

```go
func scheduleCarryRelease(t *turnBusyTracker, conversationID string, gen uint64, after time.Duration) {
    time.AfterFunc(after, func() { t.releaseCarried(conversationID, gen) })
}
```

`releaseCarried` closes the mark only if `carried[conversationID]` still
equals the generation it was handed — any opener, any other close, or a
later carried close has already retired that generation, so a stale timer
firing late is a no-op by construction and can never close a turn it did
not hold. The timer fires once and holds no resource, so it needs no
shutdown path.

**Three outcomes, by the capture's own shapes:**

- **Fold** — claude answered the send-now write inside the turn that was
  already ending. No opener ever arrives; the grace timer fires after
  `sendNowGrace` and closes the mark. The next queued message waits up to
  10s longer than it otherwise would have.
- **Second turn, fast** — claude opens a new turn before the grace fires.
  The opener clears `carried` outright; that turn's own `TurnEnd` closes the
  conversation normally, exactly as any other turn would.
- **Second turn, slow opener** — the grace fires before the second turn's
  first event is parsed. The mark closes early, and a message released by
  the drain in that gap would race the real turn. `sendNowGrace`'s value is
  the bet that claude streams a turn's first event within a few seconds of
  reading its input; #2729 left this an open question rather than a proven
  bound.

Pinned by `TestTurnBusy_SendNowCarry_FoldedWriteClosesAfterGrace`,
`TestTurnBusy_SendNowCarry_SecondTurnHoldsUntilItsOwnEnd`, and
`TestTurnBusy_SendNowCarry_IdleRefusesAndTeardownClears`
(`cmd/pyry/send_now_test.go`).

## Every other close still sweeps the carry state with the mark

`applyBusyLocked`'s close branch deletes `carry[conversationID]` and
`carried[conversationID]` alongside `busy` and `inflight`. Teardown, the
exit-lane clear, a failed write's `undo`, and the grace release itself all
funnel through this one close path, so none of them can leave orphaned
carry bookkeeping behind for a conversation that is no longer tracked at
all.

## The `OnDelivered` consumer that must not see a send-now delivery as the waiting head's own

A send-now write bypasses `channelCarry.carryPending` entirely (see
[control-plane.md § Carrying a posted channel message into claude's next
turn](control-plane-channel-post-carry.md#carrying-a-posted-channel-message-into-claudes-next-turn-2499)) —
it is not the composition of the backlog's waiting head, so it must not
consume that head's composed channel-post count when `OnDelivered` fires.
`QueuedMessage.SentNow` is the field that lets `channelCarry.clearDelivered`
tell the two deliveries apart and return immediately on a send-now one. See
[msgqueue-package-send-now.md](msgqueue-package-send-now.md) for the engine
side of that same guarantee. Pinned by
`TestChannelCarry_SendNowDeliveryClearsNothing`
(`cmd/pyry/send_now_test.go`).
