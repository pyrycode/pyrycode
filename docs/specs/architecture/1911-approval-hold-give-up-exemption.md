# #1911 — Stop abandoning a message queued behind a turn waiting on a person

Gate `msgqueue.Config.Pending` on `streamApprovalBridge.ApprovalParked`, so the
delivery hold's give-up bound exempts a person's deciding time and nothing else.

## Files to read first

Read these before writing anything. The list is the turn-1 data load; every
symbol named here is one the change either edits or depends on.

- `cmd/pyry/main.go` → `newInboundDeliver` — the delivery seam, its three-way
  placement argument, and the exact `fmt.Errorf` wrap the hold error gets today.
  The hold branch is the one statement this ticket edits inside it.
- `cmd/pyry/main.go` → `streamTurnHoldTimeout` — the constant whose doc block
  carries the "There is deliberately NO `Pending` analogue" paragraph. That
  paragraph is false once this lands and must be rewritten in this change.
- `cmd/pyry/main.go` → `mcpApprovalTimeout` — its "Why ten and not more"
  paragraph names #1911 as where the abandonment is tracked. Also false after
  this change; the constant's VALUE stays at ten (that is #1912's call).
- `cmd/pyry/main.go` → the `msgqueue.Config` literal built by `msgqueue.New`
  (search for `queueChanges` / `giveUps` / `blocked`, the three seams built just
  above it) — the `Pending`-left-unset comment, and the `Deliver:` line this
  change wraps. Note the construction order that forces late binding: `queue` is
  built here, the `relayWiring` literal below it.
- `internal/msgqueue/queue.go` → `PendingFunc`, `Config` (the `Pending` field),
  and `drain` — the exemption's exact semantics: `drain` checks `ctx.Err()`,
  then `err == nil`, then `dropped`, THEN `pending`. That ordering is what makes
  AC-4 structural rather than new code. `PendingFunc`'s doc names
  `supervisor.ErrTrustModalPending` as what the composition root wires; that
  sentence becomes false here.
- `internal/msgqueue/queue.go` → `giveUp`, `advanceLocked`, `Remove`,
  `commitGate` — how a head is abandoned, and the `draining && !committing`
  window the hold sits inside.
- `cmd/pyry/stream_turn_busy.go` → `waitIdleForDelivery` — its three return
  values (`nil`, `context.DeadlineExceeded`, `context.Canceled`) and what each
  means. The whole design keys off which of the three can reach the exemption.
- `cmd/pyry/modal_resolve_v2.go` → `ApprovalParked` — the report this consumes:
  resolved on read, a conjunction, negative in PTY mode, negative once `retire`
  runs. Read its doc block in full; AC-3 rests on it and adds no new edge.
- `cmd/pyry/modal_resolve_v2.go` → the `toolCallInFlight` field of
  `streamApprovalBridge` — the late-bound-after-construction shape this change
  copies, including its written argument for why the field is set at one wiring
  site instead of taken as a constructor parameter.
- `cmd/pyry/relay.go` → `relayWiring` (the struct) and the `w.approvals != nil`
  branch inside `startRelayV2` that assigns `bridge.toolCallInFlight` — the one
  production site this change adds a line to. There is exactly one `relayWiring{}`
  literal in the tree.
- `cmd/pyry/inbound_deliver_test.go` → `holdTestTracker`, `endHeldTurn`,
  `waitBacklog`, `assertNoWriteWithin`, `funcWriter`, `commitClaimingWriter`,
  `recvStringWithin`, `inboundTestLogger` — every helper the new tests need
  already exists here. Do not write new fixtures.
- `cmd/pyry/inbound_deliver_test.go` → `TestInboundDeliver_StreamHold_NeverEndingTurnGivesUp`
  and `TestInboundDeliver_StreamHold_HeldMessageIsDroppable` — the first is
  MODIFIED by this ticket (AC-2), the second is the template for AC-4's new test.
- `cmd/pyry/modal_resolve_v2_test.go` → `auditLogger` — the Debug-level,
  buffer-backed logger AC-5 asserts against. Same package; reuse it rather than
  building another. A capture above Debug would make the AC-5 assertion vacuous.
- `docs/knowledge/features/msgqueue-package.md` § "Bounded give-up on persistent
  delivery failure (#1000)" — why the bound is elapsed-time and per-head, and
  why give-up exits the drain instead of skipping. The exemption has to leave all
  of that intact.
- `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md`
  — the hold's placement argument in prose, plus its own now-false "Deliberately
  no `Pending`-style exemption" paragraph. **Read-only for you** — the
  documentation phase owns it; do not edit it.

## Context

A turn blocked on an unanswered approval keeps its conversation marked busy in
`turnBusyTracker`, so `waitIdleForDelivery` holds any message queued behind it.
That wait is bounded twice: by `streamTurnHoldTimeout` per attempt, and by
msgqueue's `GiveUpAfter` across attempts. The second bound **abandons the head** —
`giveUp` drops it and fires `OnGiveUp`, which the daemon routes to a typed
`session_error`. So a message queued while someone is deciding is thrown away
after roughly two hold windows, and the person is told the session is blocked
rather than being asked to hurry.

Today `mcpApprovalTimeout` (10 min) hides this: the approval denies itself well
before the hold expires, so the turn unblocks and the queued message lands. That
is why `mcpApprovalTimeout`'s own doc says it is "deliberately held clear of
`streamTurnHoldTimeout`". The moment an approval can outlive the hold — which is
exactly what #1912 does — the same wait turns a queued message into one that
silently disappears. This slice removes the abandonment first, so #1912 is not
trading a prompt that denies too eagerly for a message that vanishes.

`streamTurnHoldTimeout`'s doc already rejected the naive fix, and correctly: an
unconditional `Pending` "resets the give-up streak forever, which a HUMAN
decision may legitimately need and a running turn should not — it would make the
bound unsatisfiable." The missing piece was a way to tell the two apart. #1919
landed it: `streamApprovalBridge.ApprovalParked(conversationID)` answers "is a
person currently being asked about this conversation?" This slice is its first
consumer. Nothing here changes when or whether an approval denies.

**No ADR.** This is a gating condition on an exemption `msgqueue` already
implements and documents (`#1014`); the mechanism, its semantics, and its
trade-off are unchanged. The design decisions worth recording are local ones and
belong in the package overviews the documentation phase owns.

**Two knowledge docs carry claims this change falsifies.** The documentation
phase should fix both; they are named here rather than edited:
`docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md`
("Deliberately no `Pending`-style exemption"), and
`docs/knowledge/features/msgqueue-package.md`, whose give-up section describes a
bound with no conversation-scoped exemption.

## Design

### The shape, in one paragraph

`PendingFunc` is `func(error) bool`. It classifies the delivery error alone and
never sees a conversation, so the conversation-scoped question must be answered
**before the error leaves the delivery seam**. Therefore: the hold error is
marked with a sentinel so it is distinguishable from every other delivery
failure; a thin decorator around the `DeliverFunc` re-marks a hold error as
approval-held when the report says a person is being asked; and `Pending` is a
pure `errors.Is` against that second sentinel. Three small pieces, one late-bound
seam, and msgqueue is untouched apart from one false sentence in a doc comment.

### New code, all of it in `cmd/pyry/main.go` beside `newInboundDeliver`

Put it there rather than in a new file: `newInboundDeliver`,
`streamTurnHoldTimeout` and the `msgqueue.Config` literal are all in that file,
and the four pieces below only make sense read together with them.

**1. `approvalParkedReport`** — the late-bound holder for #1919's report.

```go
type approvalParkedReport struct {
    ask func(conversationID string) bool // nil until the relay wiring sets it
}
func (r *approvalParkedReport) set(ask func(conversationID string) bool)
func (r *approvalParkedReport) parked(conversationID string) bool
```

`set` and `parked` are both nil-receiver-safe, matching the nil-safety idiom the
seam it feeds already runs on (`waitIdleForDelivery`, `openForDelivery`). A nil
receiver, or an unset `ask`, makes `parked` report false for every conversation —
which is today's behaviour exactly, and the right answer in PTY mode, where
`ApprovalParked` is negative anyway because no tracker is wired.

Why late-bound at all: `msgqueue.New` runs in the composition root well before
`startRelayV2` builds the bridge, and the `relayWiring` literal is built after
the queue. The report cannot be handed to the queue at construction. This is the
same shape and the same reason as `streamApprovalBridge.toolCallInFlight`.

**2. Two sentinels.**

- `errStreamTurnHold` — "this attempt ended in the delivery hold, having written
  nothing." Produced by `newInboundDeliver`, and by nothing else.
- `errHeldForApproval` — "…and a person was being asked about this conversation."
  Produced by the decorator, consumed by `Pending`.

**3. `markApprovalHolds`** — a `DeliverFunc` → `DeliverFunc` decorator, a method
on `*approvalParkedReport`. Behaviour: call the inner seam; pass a nil error
straight through; pass a non-hold error straight through unwrapped; and only when
the error satisfies `errors.Is(err, errStreamTurnHold)` ask `parked(convID)`,
wrapping with `errHeldForApproval` when it answers true.

The two-step test order is load-bearing, for the reason `ApprovalParked`'s own
doc gives about `ApprovalAnswerable`: the sentinel test is a local comparison,
`parked` crosses two leaf locks. The common case — an ordinary delivery failure
with nobody being asked — must not pay the second.

**4. `approvalHoldPending(err error) bool`** — `errors.Is(err, errHeldForApproval)`.
A free function, not a method: it needs no receiver, and `PendingFunc`'s contract
demands it be pure and non-blocking. All of the work that could block runs on the
delivery path, which already blocks for whole turns; nothing new runs inside
msgqueue's classifier.

### Edits to existing code

**`newInboundDeliver` (`cmd/pyry/main.go`).** One statement. The hold branch's
wrap becomes a two-verb wrap carrying the sentinel:

```go
return fmt.Errorf("%w: %w", errStreamTurnHold, err)
```

with `errStreamTurnHold` spelled `"stream turn hold"`, so the rendered message is
byte-identical to today's and `errors.Is(err, context.DeadlineExceeded)` /
`context.Canceled` both still hold — the two properties the seam's own doc
promises the drain. Extend that doc comment by one sentence naming the sentinel
and its consumer. **No signature change**: the seam has 12 test call sites beside
its single production one, and widening it would put this ticket over the
call-site boundary for no gain.

**The `msgqueue.Config` literal (`cmd/pyry/main.go`).** Build the report next to
`queueChanges` / `giveUps` / `blocked` — those are already constructed ahead of
the literal for the same chicken-and-egg reason — then:

```go
Deliver: approvalParked.markApprovalHolds(newInboundDeliver(router.resolve, turnBusy, streamTurnHoldTimeout)),
Pending: approvalHoldPending,
```

Replace the `Pending`-left-unset comment. What it should now say: the exemption
is live and gated on `ApprovalParked`, not on a delivery-error type; #1348
removed the trust-modal producer and this is the exemption's second and only
current producer; and the gate is what keeps the bound satisfiable — a running
turn with nobody being asked still reaches give-up on today's schedule.

**`streamTurnHoldTimeout`'s doc (`cmd/pyry/main.go`).** Replace the "There is
deliberately NO `Pending` analogue" paragraph. Keep the arithmetic paragraph
above it verbatim — it is still exactly right for the unexempted case. The new
paragraph should record: the exemption now exists and is gated per conversation
on `ApprovalParked`; what it exempts is the person's deciding time, not the turn;
and the staleness-discriminator note (a per-conversation timestamp the tracker
deliberately does not hold, #1201) survives unchanged as the still-open answer
for a turn that is legitimately progressing with nobody being asked.

**`mcpApprovalTimeout`'s doc (`cmd/pyry/main.go`).** The "Why ten and not more"
paragraph says raising the window past the hold trades a prompt that gives up too
early for a message that silently disappears. That trade is gone. Rewrite the
paragraph to say the abandonment is fixed and the constant is no longer pinned
below `streamTurnHoldTimeout` by that argument. **Do not change the value** — ten
minutes stays; moving it is #1912's ticket.

**`PendingFunc`'s doc (`internal/msgqueue/queue.go`).** One sentence is now
false: "the composition root wires it as `errors.Is(err,
supervisor.ErrTrustModalPending)`". Replace it with what the composition root
actually wires (a predicate over a cmd/pyry-local sentinel that marks a delivery
held behind a parked human approval). Doc comment only — **no behaviour change in
this package, and no other edit to it.**

**`relayWiring` + `startRelayV2` (`cmd/pyry/relay.go`).** Add one field:

```go
// approvalParked is the late-bound seam carrying #1919's ApprovalParked report
// back to the delivery seam, which is built first. ...
approvalParked *approvalParkedReport
```

Set it inside the existing `if w.approvals != nil` branch, immediately beside the
`bridge.toolCallInFlight` assignment and under the same `if w.busy != nil` guard:
with no tracker the bridge answers negative for every conversation anyway, so
setting it there is exactly as informative and keeps the pair readable as one
knot. Populate the field at the single `relayWiring{}` literal.

### Data flow

```
send_message ──► msgqueue.Enqueue ──► drain (per conversation)
                                        │
                                        ├─► Deliver = markApprovalHolds(newInboundDeliver(...))
                                        │      │
                                        │      ├─ resolve ─► Activate ─► waitIdleForDelivery
                                        │      │                              │
                                        │      │            nil ──► openForDelivery ─► WriteUserTurn
                                        │      │            DeadlineExceeded / Canceled
                                        │      │                              │
                                        │      │                    %w errStreamTurnHold
                                        │      │                              │
                                        │      └─ parked(convID)? ──yes──► %w errHeldForApproval
                                        │                    │
                                        │                    no ──► unchanged error
                                        │
                                        └─► ctx.Err()? → err==nil? → dropped? → Pending(err)?
                                                                                    │
                                                            approvalHoldPending ──yes──► reset streak,
                                                                                         Debug, retry
                                                                                    │
                                                                                    no ──► give-up clock
```

`parked` resolves through `streamApprovalBridge.ApprovalParked` → the bridge's
`byModal` snapshot → `turnBusyTracker.ToolCallInFlight`. Both are read-on-demand;
nothing is stamped, and no new state is introduced anywhere.

### How each AC falls out

- **AC-1.** Attempt 1's hold expires after `streamTurnHoldTimeout`; the decorator
  marks it approval-held; `Pending` returns true; `drain` resets `firstFailedAt`
  and retries after `RetryInterval`. That loop repeats for as long as the person
  takes. `firstFailedAt` never accumulates, so `GiveUpAfter` is never reached and
  `giveUp` never runs — no head dropped, no `OnGiveUp`, no `session_error`. When
  the person answers, the turn resumes and ends, the next hold returns nil, and
  the message is written.
- **AC-2.** With nothing parked, `parked` answers false, the hold error is never
  re-marked, `approvalHoldPending` is false, and `drain` takes the identical path
  it takes today: `firstFailedAt` set on the first failure, `Warn`, give-up at
  the bound, `OnGiveUp` → the same typed `session_error`. The schedule is
  unchanged because nothing on that path changed.
- **AC-3.** `ApprovalParked` is resolved on read, and `retire` — the sole,
  unconditional deleter of the correlation, deferred on every terminal path the
  control server has — makes it negative. So the attempt after the approval is
  gone is not exempt, and `firstFailedAt` is stamped **at that attempt**: the
  ordinary bound runs from that point. No new negative edge, no counter, and
  nothing to reset. This AC is satisfied by the existing structure; it is pinned
  by test, not by new code.
- **AC-4.** `Remove` of a merely-waiting head cancels the in-flight delivery ctx,
  the hold returns `context.Canceled`, and `drain` evaluates **`dropped` before
  `pending`** — so the head is already out of the FIFO and the drain advances
  with a fresh clock no matter what the exemption said. The hold's placement
  before `WriteUserTurn` keeps the head `draining && !committing` for the whole
  wait, which is what makes it removable at all; the decorator sits outside that
  placement and cannot disturb it. Also structural, also pinned by test.
- **AC-5.** This change adds **zero** log calls. The only line the exemption path
  emits is msgqueue's existing Debug "delivery held (awaiting external decision)",
  which carries `conversation_id`, `queued_msg_id` and `queued_at` and no text.
  `ApprovalParked` logs nothing, by its own written decision. Nothing in the new
  code has access to approval content at all — the report is a bool.

### Alternatives rejected

- **Widen `newInboundDeliver` to take the report.** 12 test call sites plus one
  production; that is 13 simultaneous consumer edits against a boundary of 10,
  and the decorator gets the same result at zero.
- **Loop the hold inside the seam while parked, and never return an error.** It
  would work, but it re-implements msgqueue's pacing (retry interval, shutdown
  check, drop handling) outside msgqueue, and it hides the wait from the drain's
  Debug line and its give-up clock. The `Pending` seam exists for precisely this
  and is already tested (`internal/msgqueue/giveup_exempt_test.go`).
- **Exempt any delivery error while an approval is parked** (no
  `errStreamTurnHold`). Cheaper by a sentinel and a test, but it would also
  exempt a `resolve` or `Activate` failure — a genuinely wedged conversation —
  for the whole life of a parked approval. That is tolerable only while the
  approval deadline is short, and #1912 is the ticket that makes it long. The
  precision is the point.
- **Hang the report off `turnBusyTracker`** so the seam reaches it with no new
  parameter. Smaller, but it adds a reverse edge to a dependency #1917/#1919
  deliberately kept one-way (bridge → tracker), giving the two types a mutual
  reference resolved by late binding in both directions.

## Concurrency model

**No new goroutines.** Nothing here spawns, joins, or changes a lifecycle.

**No new synchronisation, and that is a claim to check rather than assume.**
`approvalParkedReport.ask` is written once at wiring time and read from each
conversation's drain goroutine. The happens-before chain that makes an unguarded
field safe: `set` runs inside `startRelayV2` before `go mgr.Run(ctx)` starts →
the manager's Run goroutine spawns the per-connection goroutines → the
`send_message` handler (`internal/relay/handlers`, the only production
`Enqueue` caller) calls `Enqueue` → `Enqueue` spawns the drain. Goroutine
creation is a happens-before edge at every link. This is the identical argument
`bridge.toolCallInFlight` is written under, in the same branch of the same
function, and `msgqueue.Run`'s own startup spawn cannot precede it because
`q.convs` is empty until an `Enqueue` lands.

**Lock discipline: nothing new nests.** `parked` is called from the delivery
seam holding no lock at all — after `waitIdleForDelivery` has returned, outside
`turncommit`'s claim, outside `q.mu`. `ApprovalParked` then takes the bridge's
leaf `mu`, releases it, and only then asks `ToolCallInFlight`, which takes the
tracker's leaf `mu`. No two locks are ever held at once on this path, so this
change establishes no lock order and cannot deadlock against the bridge → tracker
edge that already exists.

**The report is a level, not an edge, and a stale read is bounded.** The value
can change between the hold timing out and `parked` being asked. Both directions
are benign and neither loses a message: read-false-just-after-answered costs one
non-exempt attempt inside a 2-minute bound, and the following attempt's hold
returns nil and delivers; read-true-just-before-denied costs one extra exempt
attempt, after which the bound runs from the next failure. There is no transition
to miss because the consumer re-reads on every attempt.

**Shutdown.** `drain` checks `ctx.Err()` before both the `dropped` and `pending`
branches, so daemon shutdown wins over an exemption unconditionally. The head
stays queued — msgqueue's documented in-memory loss boundary — and `Run`'s
`wg.Wait` unblocks. This change adds nothing to that path.

## Error handling

| Path | Error | Classification | Outcome |
|---|---|---|---|
| `resolve` fails | verbatim | not a hold; never exempt | counts toward give-up |
| `Activate` fails / times out | verbatim | not a hold; never exempt | counts toward give-up |
| Hold times out, approval parked | `errHeldForApproval: stream turn hold: context deadline exceeded` | `Pending` true | streak reset, Debug, retry |
| Hold times out, nothing parked | `stream turn hold: context deadline exceeded` | `Pending` false | counts toward give-up (today's schedule) |
| Hold cancelled (head removed) | `…: context.Canceled` | `dropped` branch decides first | head already gone; drain advances, fresh clock |
| Hold cancelled (shutdown) | `…: context.Canceled` | `ctx.Err()` branch decides first | head stays queued, drain exits |
| `WriteUserTurn` fails | verbatim, unwrapped | not a hold; never exempt | counts toward give-up; `undo()` runs first |

Two invariants the developer must not break:

- **`WriteUserTurn`'s error stays verbatim.** Its doc says so and msgqueue
  classifies `ErrNoLiveSession` / `turncommit.ErrDropped` through it. The
  decorator only ever wraps an error already carrying `errStreamTurnHold`, which
  the write path cannot produce.
- **No guard for `context.Canceled` in the decorator.** It is tempting and it
  would be dead code: both cancellation sources are decided upstream of
  `pending`, by branches that already exist. Adding one would be a defence for a
  failure mode nobody has observed. AC-4's test is what pins that this is true.

## Testing strategy

All in `cmd/pyry/inbound_deliver_test.go`, reusing its existing helpers. **Four
new test functions and one modified. Do not add more** — the ACs are covered by
this list, and each additional queue-timing test costs a debugging cycle.

Every new test builds the queue in the **production shape** — `Deliver` wrapped
by `markApprovalHolds`, `Pending: approvalHoldPending` — with a stub report, not
a real bridge. Shrink `RetryInterval` / `GiveUpAfter` / the hold the way
`TestInboundDeliver_StreamHold_NeverEndingTurnGivesUp` already does; the
production arithmetic stays recorded on `streamTurnHoldTimeout`.

**Modified — `TestInboundDeliver_StreamHold_NeverEndingTurnGivesUp` (AC-2).**
Rewire its `msgqueue.Config` to the production shape with a report left unset, so
the existing assertion (a never-ending turn fires `OnGiveUp` for the
conversation) now proves it under the new wiring. Add a sentence to its doc
naming AC-2. This is the "unset report ⇒ never exempt" case too; it needs no
separate test.

**New — AC-1 + AC-5, `…_ApprovalHold_ParkedApprovalOutlastsGiveUpBound`.**
- Report answers true; tracker holds a turn that never ends; logger is
  `auditLogger`'s Debug-level buffer.
- Assert `OnGiveUp` does **not** fire for a window several multiples of
  `GiveUpAfter` — long enough that a non-exempt implementation gives up inside it
  and this test fails.
- Flip the report to false and end the turn; assert the queued text is written
  and the backlog empties (`waitBacklog`).
- Assert the captured log buffer contains neither the queued message text nor any
  tool-call id. The capture must be at Debug (`auditLogger`), or the assertion
  proves nothing about the Debug line the exemption path actually emits.

**New — AC-3, `…_ApprovalHold_ClearedApprovalRestartsTheBound`.**
- Report answers true; turn never ends. Assert no give-up past several
  `GiveUpAfter` windows (the "not before" half — without it the test passes on an
  implementation that never exempts anything).
- Flip the report to false with the turn **still** never ending. Assert
  `OnGiveUp` then fires, and the head is gone.
- The teeth: the give-up must arrive after the flip, not merely eventually.

**New — AC-4, `…_ApprovalHold_HeldHeadStaysDroppable`.** Clone the shape of
`TestInboundDeliver_StreamHold_HeldMessageIsDroppable` with the report answering
true and the production wiring: `m1` writes and marks busy, `m2`/`m3` queue,
`Remove(m2)` returns true, `waitBacklog` reports `[m3]`, `endHeldTurn` releases
`m3`, delivered order is `[m1 m3]`. Additionally assert `OnGiveUp` never fired —
that is what separates "dropped as a cancellation" from "abandoned".

**New — precision, `…_ApprovalHold_NonHoldErrorIsNeverExempt`.** Table-driven,
calling the wrapped seam directly (no queue), asserting `approvalHoldPending` on
the returned error:
- resolve returns an error, report true → not pending (a wedged conversation is
  not a person deciding);
- hold times out, report true → pending;
- hold times out, report false → not pending.

**Mutation checks the developer should run before calling this done** (no
worktree writes needed — `go test -overlay`):
- Delete the `errors.Is(err, errStreamTurnHold)` conjunct in the decorator → the
  precision test must go red.
- Make `parked` return true unconditionally → AC-2's modified test must go red.
- Make `parked` return false unconditionally → AC-1 and AC-3's first half must go
  red.
- Move `Pending` back to unset → AC-1 must go red.

`make check` must pass. No e2e tier is involved: every property here is
observable at the queue seam, and the fake-claude e2e has no approval that
outlives a hold to drive.

## Open questions

- **Should the exemption have an outer ceiling of its own?** Today it is bounded
  only by the approval's own lifetime, which is `mcpApprovalTimeout` (10 min) and
  which #1912 is about to change. If #1912 lands an approval that can be parked
  indefinitely while a client is connected, a queued message behind it is held
  indefinitely too — visibly, in the backlog, with the drop control live, which
  is the correct behaviour, but it is worth stating on #1912 rather than
  discovering. Not this ticket's call; do not add a ceiling here.
- **`ApprovalParked` answers per conversation, but a conversation could have more
  than one approval parked.** The report is a disjunction over parked ids, so it
  stays true until the last one clears. That is the behaviour AC-1 wants and
  needs no handling; noted so it is not read as an oversight.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The only new input this design consumes is
  a `bool` from `ApprovalParked`, resolved entirely from daemon-side state (the
  bridge's `byModal` correlation and `turnBusyTracker`'s membership). No wire
  value crosses a boundary here. The conversation id passed to `parked` is the
  same id the seam already carries, and it has passed two independent daemon-side
  gates before reaching this point — `router.Route` at enqueue and
  `sessionRouter.resolve` as `deliver`'s first statement — so an unknown,
  unbound, or forged id returns before the hold and never reaches `parked`. The
  boundary is where it already was; this change adds no second one.
- **[Trust boundaries — the report's own confinement]** No findings, and this one
  needed checking rather than assuming. `ApprovalParked` iterates **every** parked
  tool-use id, including other conversations', and asks the tracker about each
  under the asked-about conversation's key. If that lookup were keyed flat rather
  than nested per conversation, one conversation's parked approval would exempt
  another conversation's held message — a cross-conversation influence, and the
  exact defect class #1919's own review names. It is nested per conversation and
  #1919 pins it. This design relies on that property and adds no path around it.
- **[Tokens, secrets, credentials]** Not applicable by construction. No token,
  key, or credential is read, minted, stored, compared, or logged. Nothing in the
  new code touches `internal/keys`, `internal/noise`, or the device registry.
- **[File operations]** Not applicable. No path is constructed, no file opened,
  created, or removed. The change is entirely in-memory.
- **[Subprocess execution]** Not applicable. No `exec.Command`, no argv
  construction, no environment mutation. The queued payload's route to claude's
  stdin is unchanged — the change only affects *whether* a delivery attempt
  counts toward a bound.
- **[Cryptographic primitives]** Not applicable. No randomness, no comparison
  against a secret, no primitive selected.
- **[Network & I/O]** No findings. No socket is read or written and no cap is
  changed. Worth stating explicitly because a hold that never gives up sounds
  like an unbounded resource: it is not. The per-conversation backlog cap
  (`defaultMaxQueuedPerConversation`, 100) is `Enqueue`'s single insertion point
  and is untouched, so a flooding phone is still rejected at the cap whether or
  not an approval is parked. The exemption changes only which of the already-
  bounded, already-counted messages is eventually abandoned versus delivered — it
  can extend how long a bounded backlog is retained, not how large it grows.
- **[Error messages, logs, telemetry]** No findings, and this is AC-5. The change
  adds zero log calls. Both new sentinels are fixed daemon-authored strings with
  no interpolation, so the wrapped error text is `errHeldForApproval` +
  `errStreamTurnHold` + a `context` error and can carry neither queued text nor
  approval content by construction. The one line the exemption path reaches is
  msgqueue's existing content-free Debug. `giveUp`'s `reason` — the string that
  becomes a client-visible `session_error` — is unchanged and still built only
  from the elapsed window and the delivery error; the exemption means it is now
  reached *less* often, never with more in it. AC-5's assertion is against a
  Debug-level capture (`auditLogger`), so it is not vacuous.
- **[Concurrency]** No findings; see § Concurrency model for the full argument.
  The two hazards worth naming and why neither bites: (a) the unguarded
  late-bound field is safe on a happens-before chain that runs
  `set` → `go mgr.Run` → conn goroutine → `Enqueue` → `go drain`, with the only
  production `Enqueue` caller downstream of `mgr.Run`; (b) the tracker ⇄ bridge
  reference is not a lock cycle because `parked` is called holding no lock, and
  `ApprovalParked` releases the bridge's `mu` before asking the tracker.
- **[Availability — the category this ticket actually lives in]** SHOULD FIX,
  addressed in the design, restated here because it is the one real risk. An
  exemption that could be made permanently true by an attacker would make
  msgqueue's give-up bound unsatisfiable — the exact failure
  `streamTurnHoldTimeout`'s doc refused to accept. Three properties keep it
  bounded, and all three must survive review: the exemption is gated on a
  conjunction whose approval half is deleted unconditionally by `retire` on every
  terminal path; a held head remains removable for the whole wait (AC-4), so a
  person always retains the drop control; and the exemption is scoped to the hold
  error alone, so a wedged conversation with an approval parked still reaches
  give-up on the failure that actually wedged it. If a future change relaxes any
  of the three, the bound becomes unsatisfiable again.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security
  model's relevant threat here is a client influencing another conversation's
  delivery; addressed under trust boundaries above. Nothing on the wire changes:
  no new frame, no new field, no change to when `session_error` is emitted other
  than emitting it less often for the case it was wrong about.
- **[Out of scope]** Whether an approval may outlive the delivery hold at all is
  #1912's decision; this slice deliberately changes neither `mcpApprovalTimeout`
  nor any deny path.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
