# Delivery-seam consumer, mid-turn hold (#1199)

The first — and, as of this ticket, only — production reader of `Busy`/`WaitIdle`. It closes the actual
regression #1201 was built for: on `interactive_runner: stream-json`, `streamsup.Runner.WriteUserTurn`
returns as soon as the user-turn envelope is in the child's stdin pipe (`runner.go:283-285`), so
`internal/msgqueue`'s serial drain emptied as fast as it could write bytes instead of pacing on turn-end
the way `msgqueue.DeliverFunc`'s contract requires ("MUST block while claude is busy … that blocking IS
the drain's turn-end pacing", `msgqueue/queue.go:87-92`). The queued-backlog UI and the drop-before-drain
control were both regressed as a result — present on `pty` (which honours the contract via
`supervisor.WriteUserTurn`'s idle gate) and absent on `stream-json`.

Two new nil-receiver-safe, empty-key-safe methods on `turnBusyTracker`, both thin wrappers over the
existing primitives — no new fields, no new synchronisation:

```go
func (t *turnBusyTracker) waitIdleForDelivery(ctx context.Context, conversationID string, timeout time.Duration) error
func (t *turnBusyTracker) openForDelivery(conversationID string) (undo func())
```

`newInboundDeliver` (`cmd/pyry/main.go`, the `msgqueue.Config.Deliver` seam) calls both, in this exact
order, both between `Activate` and `WriteUserTurn`: `waitIdleForDelivery` first (bounded by the new
`streamTurnHoldTimeout`, 15 minutes), then `openForDelivery`, whose returned `undo` runs only if the
subsequent write fails. Two placement facts make this a **guarantee**, not a better race:

- **Before the write, therefore before `WriteTurn`'s `turncommit` claim.** `msgqueue.commitGate`'s own
  doc says the seam calls it "after the idle-gate wait and before the write" (`queue.go:419-425`) —
  holding the wait *outside* that claim is what keeps the queued head `draining && !committing` for the
  whole wait, the only window in which `msgqueue.Remove` drops it. The drop-before-drain control is
  delivered by placement, not by new removal logic.
- **The mark precedes the write, not follows it.** The tracker's ordinary opener feed (`observe`, fed
  from the parsed turn stream) is asynchronous; a mark placed after a successful write races it — on a
  fast child the turn's own `TurnEnd` can clear before the marking statement runs, leaving a stale mark
  nothing will ever clear. Marking first makes the ordering unconditional: no byte has reached the child
  yet, so no event for this turn can precede the mark.

**Determinism, the property the hold actually needs.** The msgqueue drain is serial per conversation, and
`openForDelivery` runs on that same drain goroutine inside the same `deliver` call that then writes. So
for messages *A* then *B* on one conversation: mark(A) happens-before `deliver(A)` returns happens-before
`deliver(B)` starts happens-before *B*'s `waitIdleForDelivery` reads membership — program order on one
goroutine, not a race against the child's speed.

**`setBusy` gained a `changed bool` return**, reported out of the single lock acquisition it already
takes. `openForDelivery`'s `undo` is live only when its own `setBusy` call actually moved membership —
guarding against a foreign opener (a `--resume` respawn replaying events is the plausible route) landing
in the gap between the wait returning nil and the mark; without the report, a naive undo could clear a
turn this delivery never opened and let the next message through unheld. Deriving the same answer from a
separate `Busy` read would reintroduce the TOCTOU this closes.

**`streamTurnHoldTimeout` (15 min, `main.go`, beside `inboundActivateTimeout`) bounds one delivery
attempt**, not the message — msgqueue's own retry (1s) and give-up (2m) bounds mean a turn that never
ends surfaces as a typed `session_error`/`CodeSessionBlocked` after ≈2× the timeout (≈30 min) rather than
holding the conversation forever. Deliberately no `Pending`-style exemption (contrast
`supervisor.ErrTrustModalPending`): that would reset the give-up streak forever, which a legitimate human
decision may need and a running turn should not.

**Trust boundary, restated honestly for this feed.** The tracker's SECURITY note previously claimed "the
key is never taken from the wire" — true of `observe`/`clearForSession`, which key off a daemon-resolved
session id. `openForDelivery`'s key is the conversation id from a `send_message` payload, which the
property still holds for, but for a narrower reason: that id passes two independent daemon-side gates
before it can reach the mark (`router.Route` at enqueue, `sessionRouter.resolve` again as `deliver`'s
first statement) — an unknown, unbound, or forged id returns before the mark, so the mark only ever
describes the conversation the daemon is about to write to, one the caller was already authorized to
write to.

PTY is unaffected: the tracker is nil there, both calls are no-ops, and `newInboundDeliver`'s body is
semantically identical to before #1199. See [codebase/1199.md](../codebase/1199.md).

**Forced-ordering test coverage across a `new_session` rotation (#1295).** The #1137 e2e had
failed twice at its M4 milestone with the same shape — rotation succeeds, the follow-up turn is
accepted, then zero bytes reach any child for the full 20 s deadline — consistent with a
delivery parked here (`waitIdleForDelivery`, bounded by `streamTurnHoldTimeout`, 15 min: far
outside the e2e's window, and silent while parked, matching the record). Rather than wait for
the ~1-in-N-per-week e2e to fire again, #1295 drives the ordering directly at this seam: park a
delivery in the hold, run the rotation underneath it (rekey the binding, tear the child down,
fire `clearForSession` keyed to the rotation's new session id), and check whether the clear is
what releases it. Forced 50/50 under `-race -count=50`, mutation-demonstrated — **the clear does
release the parked delivery**, and it lands in the post-rotation child and no other. That rules
out this seam as the M4 stall's cause on the ordering the test forces (pre-rotation turn events
and the transition arriving in order); it does **not** decide a straggler pre-rotation event
re-marking the conversation *after* the clear fires, which needs the drain's fan-in rather than
the seam alone and is filed as [#1298](https://github.com/pyrycode/pyrycode/issues/1298). See
[codebase/1295.md](../codebase/1295.md).
