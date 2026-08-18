# Spec #1199 — Hold a mid-turn send in the queue until the running turn ends (stream-json)

**Ticket:** [#1199](https://github.com/pyrycode/pyrycode/issues/1199) · **Size:** S · **Labels:** `size:s`, `bug`, `security-sensitive`, `needs-real-claude`

**Citations verified by content against `bd0f26b`** (the branch point). Re-verify by content, not by line, if a sibling lands first.

---

## Files to read first

Turn-1 data load. Read these before writing anything.

| Path + range | What to extract |
|---|---|
| `cmd/pyry/main.go:1534-1567` | `newInboundDeliver` — the seam this ticket changes. Note the existing `resolve → Activate(bounded) → WriteUserTurn(raw ctx)` order and the doc's stated reason for the raw ctx. |
| `cmd/pyry/main.go:1493-1500` | `inboundActivateTimeout` — the const style, doc shape, and "tuning knob, not a contract" posture the new `streamTurnHoldTimeout` mirrors. **Its doc's last clause becomes partly stale** and is rewritten by this ticket. |
| `cmd/pyry/main.go:1202-1216` | `sessionRouter.resolve` — the daemon-side validation (`convReg.Get` hit → non-empty `CurrentSessionID` → `pool.Lookup` hit) that every conversation id reaching the new mark has already passed. This is the security argument; read it, don't assume it. |
| `cmd/pyry/main.go:802-812` | `selectInteractiveRunner` call site. `streamSink != nil` ⟺ stream mode; this is where the tracker's construction lands. |
| `cmd/pyry/main.go:876-918` | The `msgqueue.New` literal: `Deliver`, `OnGiveUp`, `Pending`. AC3's give-up bound and the PTY-specific `Pending` escape hatch both live here. |
| `cmd/pyry/main.go:730` | `convReg` is loaded here — **before** `:809`, which is what makes the composition-root construction order work. |
| `cmd/pyry/main.go:984-1028` | The `relayWiring` literal; `streamSink: streamSink` at `:1027` is the sibling the new `busy:` field sits beside. |
| `cmd/pyry/stream_turn_busy.go` (whole file, 299 lines) | The tracker. In particular `:11-58` (the contract + three-feeds + SECURITY notes you must widen), `:205-229` `clearForSession` (the nil-receiver + resolve-outside-the-lock discipline the two new methods copy), `:238-257` `setBusy` (the single mutation primitive both new methods delegate to), `:264-298` `Busy` / `WaitIdle`. |
| `cmd/pyry/relay.go:700-749` | The `busy` local + its construction at `:747-748`, and the `:738-746` comment that is the upstream instruction addressed to this slice ("The consumer slice that consults it for delivery promotes it to a field, with the reader in the same diff"). |
| `cmd/pyry/relay.go:245-253` | `relayWiring.streamSink` — the field doc shape the new `busy` field mirrors. |
| `cmd/pyry/relay.go:765-782` | The unconditional session-transition wiring that also consumes `busy` (`:782`). It must keep compiling off the new field, and must still receive nil in PTY mode. |
| `cmd/pyry/relay.go:825-...` | `conversationForSession` — the resolve closure moves from relay.go to main.go; same package, no import change. |
| `internal/msgqueue/queue.go:87-92` | `DeliverFunc`'s contract: "MUST block while claude is busy … that blocking IS the drain's turn-end pacing." This ticket makes the stream path honour it. |
| `internal/msgqueue/queue.go:367-417` | `Remove` — "Removing the waiting head also cancels its in-flight delivery so the seam's idle-gate wait unblocks at once." AC2 rides this verbatim; nothing in msgqueue changes. |
| `internal/msgqueue/queue.go:419-445` | `commitGate` — "The delivery seam calls it once, **after the idle-gate wait and before the write**." This is the exact slot the new wait occupies, and why the head stays droppable throughout it. |
| `internal/msgqueue/queue.go:501-630` | The drain loop: `deliverCtx`/`deliverCancel`, the `dropped` computation, the `ctx.Err()`-first shutdown check, the `pending` branch, `firstFailedAt`, and `giveUp`. Work AC3's arithmetic against this code, not against the prose. |
| `internal/msgqueue/queue.go:69`, `:85` | `defaultRetryInterval = 1s`, `defaultGiveUpAfter = 2m` — the two constants AC3's worst-case bound is computed from. |
| `internal/streamsup/envelope.go:126-172` | `WriteTurn`: the `turncommit` claim sits *inside* it, after the nil-writer check. Confirms the new wait must be **outside** `WriteUserTurn`, upstream of the claim. |
| `internal/streamsup/runner.go:272-285` | `Runner.WriteUserTurn` — the one-line delegation that returns on write. The gap. Not edited by this ticket. |
| `cmd/pyry/inbound_deliver_test.go:30-60`, `:78-150` | `gatingWriter` and the real-`msgqueue.Queue`-wired harness. Reuse both; the two `newInboundDeliver(resolve)` call sites at `:88` and `:144` are the whole signature fan-out. |
| `internal/e2e/relay_v2_stream_queue_drain_test.go` (whole file) | The stream e2e to extend. `:33-48` is the repo's own record of the gap ("a `queue_state` depth of N is NOT reliably observable here") and **becomes false** — rewrite it. |
| `internal/e2e/relay_v2_dequeue_test.go:160-235` | The `queue_state` → read `queued_msg_id` → `dequeue_message` → updated `queue_state` sequence to mirror for AC2. **Do not edit this file** (AC4). |
| `docs/specs/architecture/1201-stream-turn-busy-tracker.md` | The tracker's origin spec — why membership-only, why no timestamp/count. Read before proposing any state addition. |

---

## Context

On `interactive_runner: stream-json` (live on the Mac daemon since 2026-07-24) a message sent while claude is mid-turn is enqueued correctly but never appears as a queued row, and cannot be dropped before it runs. On `pty` both work.

The cause is one gap. `msgqueue`'s package contract is that `DeliverFunc` blocks while claude is busy and the drain is paced entirely by that block (`queue.go:87-92`). The PTY implementation honours it — `supervisor.WriteUserTurn` gates on `waitReadyAutoContinue` before delivering, so the serial per-conversation drain parks for the whole turn and the backlog visibly holds. The stream implementation does not: `streamsup.Runner.WriteUserTurn` is `WriteTurn(ctx, r.Stdin(), payload)` (`runner.go:283-285`), which returns as soon as the envelope is in the child's stdin pipe. The drain empties as fast as it can write bytes.

Two consequences, which are the two halves of the regression:

- The `queue_state` producer fires on every enqueue and carries a real snapshot — but by the time it runs the backlog has usually already drained. The queued row is a race, not an absence.
- `WriteTurn` claims the `turncommit` gate (`envelope.go:161-163`) with nothing in front of it, so the window in which the head is `draining && !committing` — the only window in which `Remove` will drop it (`queue.go:367-417`) — is effectively zero. There is nothing to drop.

The signal needed to close the gap already exists and is fully wired: `turnBusyTracker` (`stream_turn_busy.go`), per-conversation, self-synchronised, with `Busy` and `WaitIdle` sitting unused. #1198's slice chain (#1201, #1202, #1206, #1207, #1209, #1210) closed the last feed and the file records the "no reachable sequence leaves a conversation reported busy forever" precondition as SATISFIED. This ticket is the consumer slice the tracker was built for, and the one `relay.go:738-746` names by role.

**Correction (#1496, documentation pass):** the SATISFIED claim depended on `TurnEnd` reliably reaching `observe`, which the fan-in's pre-#1496 class-blind drop-newest did not guarantee under saturation. #1496 reserves fan-in capacity for closing-class envelopes; the precondition this ticket's hold relies on now holds up to a documented, `Warn`-visible reserve headroom. See [`docs/knowledge/codebase/1496.md`](../../knowledge/codebase/1496.md).

**Why now:** `pyrycode-desktop`#483's 8/8 real-claude gate is blocked on this, and that dependency is cross-repo so it will not auto-surface here.

---

## Design

### The shape in one sentence

Put a bounded, nil-safe wait-for-idle in the inbound-delivery seam immediately before the write, and have the same seam mark the conversation mid-turn immediately before that write — so the next delivery on that conversation parks by program order, not by luck.

### Seam choice: `newInboundDeliver`, not `streamRunner.WriteUserTurn`

The ticket offered both. Take `newInboundDeliver` (`main.go:1553`).

- **The construction-order fork dissolves.** The stated obstacle was that the tracker does not exist when `newStreamRunnerFactory` is built at `main.go:809`. It does exist by `main.go:904` (`msgqueue.New`), and `convReg` — the only input the tracker's resolve closure needs — is loaded at `main.go:730`. So the tracker is constructed at the composition root *between* those two points and no factory signature changes. Choosing the runner seam instead would force a hoist plus three signature changes (`selectInteractiveRunner`, `newStreamRunnerFactory`, `streamRunner`).
- **Blast radius equals AC scope.** `newInboundDeliver` is reached only from the queue drain. The runner's `WriteUserTurn` is also the target of the ACP prompt path's `promptDeliverer` (`acp_prompt.go:31-32`, `:260`, resolved at `acp.go:134`), which carries a `promptDeliverTimeout` and no `turncommit` gate. Gating there would silently change a path no AC mentions.
- **AC4 is structurally free anyway.** The runner seam's claimed advantage ("PTY never enters this code") is matched here by making both new tracker methods nil-receiver no-ops — the discipline `clearForSession` already establishes (`stream_turn_busy.go:169-176`). In PTY mode `busy` is nil, both calls return immediately, and the deliver body is semantically byte-identical to today.
- **It is where AC3's reasoning lives.** The give-up bound (`GiveUpAfter`), `OnGiveUp`, and the PTY-specific `Pending` escape hatch are all in the same `msgqueue.Config` literal at `main.go:904-915`.

### Where the wait sits, and why exactly there

```
deliver(ctx, convID, payload):
    resolve(convID)                      ← unchanged; the daemon-side validation gate
    Activate(ctx + inboundActivateTimeout) ← unchanged; a wedged respawn stays a
                                             retryable error, not a 15-minute hold
    waitIdleForDelivery(ctx, convID, holdTimeout)   ← NEW
    openForDelivery(convID) → undo                  ← NEW
    WriteUserTurn(ctx, convID, payload)  ← inside it, WriteTurn claims turncommit
        on error: undo(); return err
    return nil
```

Three placement constraints, all load-bearing:

1. **Before `WriteUserTurn`**, therefore before `WriteTurn`'s `turncommit` claim. `msgqueue.commitGate`'s own doc (`queue.go:419-425`) says the seam calls the gate "after the idle-gate wait and before the write". Holding the wait outside the claim is what keeps the head `draining && !committing` for its whole duration, which is what makes `Remove` return true and cancel the wait (`queue.go:367-382`). AC2 is delivered by placement, not by new code.
2. **After `Activate`.** `Activate` is already bounded at 30s and idempotent; leaving it first means a wedged respawn still surfaces as a prompt retryable error rather than being masked behind a long hold.
3. **The mark is placed before the write, not after.** The tracker's ordinary opener feed is asynchronous (child → parser → sink → drain → `observe`). A mark placed *after* a successful write races that feed: on a fast child the turn's `TurnEnd` can land and clear before the marking statement runs, leaving a stale mark that no further event will clear. Marking first makes the ordering unconditional — no byte has reached the child, so no event for this turn can precede the mark. The cost is one undo on the write-error path, which `openForDelivery` returns rather than exposing a general clear.

### Determinism (the property AC1 actually asks for)

The drain is serial per conversation and `openForDelivery` runs on the drain goroutine inside the same `deliver` call that writes. So for messages *A* then *B* on one conversation:

> mark(A) **happens-before** `deliver(A)` returns **happens-before** `deliver(B)` starts **happens-before** *B*'s `waitIdleForDelivery` reads membership.

Same goroutine, program order. *B* parks. This is what turns today's race into a guarantee, and it is the reason the mark must live inside `deliver` rather than being inferred from the event stream. It holds regardless of how fast or slow the child is — including a child that has not yet started reading stdin.

### New surface on `turnBusyTracker` (`stream_turn_busy.go`)

Two unexported methods, both delegating to the existing `setBusy` / `WaitIdle` primitives. No new fields, no new synchronisation, no timestamp or count (the #1201 contract stands).

```go
// nil receiver → nil; empty conversation id → nil; otherwise a bounded WaitIdle.
func (t *turnBusyTracker) waitIdleForDelivery(ctx context.Context, conversationID string, timeout time.Duration) error

// nil receiver or empty conversation id → a no-op undo, nothing marked.
// Otherwise setBusy(conversationID, true). The returned undo closes the turn
// ONLY if this call actually opened it; if the conversation was already busy,
// the undo is a no-op.
func (t *turnBusyTracker) openForDelivery(conversationID string) (undo func())

// setBusy gains a return value reporting whether membership actually moved.
// Existing callers discard it; Go permits that with no edit at their call sites.
func (t *turnBusyTracker) setBusy(conversationID string, open bool) (changed bool)
```

The `changed` return is what makes the undo safe. Without it, an undo could clear a turn this delivery did not open — the conversation can already be busy at mark time if another feed's opener event landed in the gap between the wait returning nil and the mark (a `--resume` respawn replaying events is the plausible route). Deriving the same answer from a separate `Busy` read would reintroduce a TOCTOU; reporting it out of the one lock acquisition `setBusy` already takes does not. See § Security review [Concurrency].

`Busy` and `WaitIdle` keep their documented non-nil-safe contracts unchanged — the wiring hands the nil to `clearForSession` and to these two, and to nothing else. Returning the undo as a closure rather than exposing a second clear-by-conversation method is deliberate: the only way to obtain a clear is to have placed the matching open, so no future caller can reach "mark this conversation idle" by name.

Both methods refuse the empty key, preserving the `absent ≡ idle ≡ unknown ≡ unbound` collapse the type is built on.

### Doc updates inside `stream_turn_busy.go` (not optional — they are the ticket's record)

- `:11-18` — the tracker's opening sentence anticipates "the inbound-delivery path". That consumer now exists; say so and name it.
- `:27-39` — the THREE-FEEDS note becomes *three close feeds and one open feed besides `observe`*. Restate the no-forever-busy analysis under the new feed: a write-side open is placed only immediately before a write the same statement sequence performs; a failed write is undone in that sequence; a successful write starts a turn that closes through the `result`→`TurnEnd` feed, a pool teardown, or the child dying. The one residual — child alive, envelope consumed, turn never ends — is a *legitimately running turn*, bounded by `streamTurnHoldTimeout` + msgqueue's give-up, i.e. AC3. It is **not** the wedge class #1198 closed, and the KNOWN-GAP/SATISFIED record must stay intact rather than be edited to look like a new gap.
- `:53-58` — the SECURITY note's "the key is never taken from the wire" claim must be restated honestly for the new feed (see § Security review).

### Wiring

**`cmd/pyry/main.go`**

- New const beside `inboundActivateTimeout` (`:1500`):
  `streamTurnHoldTimeout = 15 * time.Minute`. Doc it in the same register: bounds **one** delivery attempt's wait for the conversation's running turn to end; unused when the tracker is nil; a tuning knob, not a contract; see § Error handling for the arithmetic.
- `inboundActivateTimeout`'s doc (`:1493-1500`) ends "it does NOT bound the `WriteUserTurn` that follows — that block is the drain's turn-end pacing". On the stream path the pacing block now sits *in front of* `WriteUserTurn` and *is* bounded. Amend that clause; do not delete it (it is still exactly right for PTY).
- After `selectInteractiveRunner` (`:809-812`), construct the tracker gated on `streamSink != nil`, with the resolve closure moved verbatim from `relay.go:747-748`:
  `newTurnBusyTracker(func(sid string) (string, bool) { return conversationForSession(convReg, sid) }, logger)`.
  Same package, so no import moves. Comment why the gate is `streamSink != nil` and not `cfg.InteractiveRunner == "stream-json"`: the former is `selectInteractiveRunner`'s own post-validation answer, so an unrecognised config value has already failed fast one line above.
- `Deliver: newInboundDeliver(router.resolve, turnBusy, streamTurnHoldTimeout)`.
- `busy: turnBusy` in the `relayWiring` literal, beside `streamSink:` (`:1027`).
- `newInboundDeliver` gains two parameters and the four statements above. Its doc gains a bullet for the hold, stating the placement constraints and that a nil tracker leaves the body semantically unchanged.

**`cmd/pyry/relay.go`**

- New `relayWiring` field beside `streamSink` (`:245-253`): `busy *turnBusyTracker`. Document that it is non-nil exactly when `streamSink` is non-nil (both minted at the composition root from the same condition), and that PTY leaves it nil so the composed `clearForSession` at `:782` stays a nil-receiver no-op.
- Delete the `busy` local (`:701-706`) and its construction (`:747-748`); read `w.busy` at `:749` and `:782`.
- Rewrite the `:731-746` comment. The "deliberately still a local … no DELIVERY path reads the signal" paragraph is now false in every clause. Replace it with the field's provenance and a pointer to the delivery reader — this is `relay.go:738-740`'s instruction being discharged, and leaving the old text would invert it exactly as #1207's split hygiene found elsewhere.

**Forward-reference sweep (do this, it is cheap):** `grep -rn '1199' --include='*.go' cmd internal` returns nothing today — the upstream reference is by *role*, not number. After the edits, re-grep for the role-shaped phrases too (`grep -rniE 'consumer slice|no DELIVERY path' cmd/pyry`) and make sure no surviving comment still claims the delivery consumer is absent.

### What does **not** change

`internal/msgqueue`, `internal/streamsup`, `internal/sessions`, `internal/supervisor`, `internal/relay`, and every PTY path. No wire-format change, no new frame, no new config key. The `dequeue_message` verb, `msgqueue.Remove`, `commitGate`, `deliverCancel`, the `queue_state` producer and the `session_error` producer are all consumed exactly as they stand.

---

## Concurrency model

No new goroutines. No new locks. The tracker's existing `mu` + close-and-replace generation channel is the only synchronisation touched, through `setBusy` and `WaitIdle` unchanged.

- **Who marks:** the msgqueue drain goroutine for that conversation, one at a time, inside `deliver`. Serial per conversation by construction (`queue.go:485-497` spawns at most one drain per conversation).
- **Who clears:** unchanged — the drain-fan-in goroutine on `TurnEnd` (`observe`), the pool lifecycle goroutine on a teardown transition, the stream-drain goroutine on the child-exit arm, and now the delivering goroutine's `undo` on a failed write. All four funnel through the single-lock-acquisition `setBusy`.
- **Who waits:** the same msgqueue drain goroutine, in `WaitIdle`'s check-and-subscribe loop. It holds no other lock while waiting — `resolve` and `Activate` have both returned, and `q.mu` is explicitly not held across `deliver` (`queue.go:504-506`).
- **Lock order:** unchanged. `waitIdleForDelivery` takes only `t.mu`, and never while holding `q.mu`. `openForDelivery` takes only `t.mu`. Neither calls `t.resolve`, so the `tracker.mu → convReg.mu` order the file warns about is not even reachable from the new methods.
- **Exit paths for the wait:** conversation goes idle (any of the four clears) → nil; head dropped → `Remove` fires `deliverCancel` → `context.Canceled`; daemon shutdown → parent ctx cancelled → `context.Canceled`, and the drain's `ctx.Err()`-first check (`queue.go:540-548`) makes shutdown win over every other outcome; `streamTurnHoldTimeout` elapsed → `context.DeadlineExceeded`. There is no path on which the wait returns without one of these four, so the drain goroutine cannot leak.

---

## Error handling

| Failure | Behaviour |
|---|---|
| Wait times out (`streamTurnHoldTimeout`) | Return `fmt.Errorf("stream turn hold: %w", err)`. `errors.Is(err, context.DeadlineExceeded)` holds. **Nothing was written** — the retry is a clean re-attempt, never a duplicate turn. |
| Head dropped during the wait | `deliverCancel` → `context.Canceled` → same wrap. The drain's `dropped` computation (`queue.go:534-538`) sees the head gone, advances with a fresh give-up clock, and does not retry or count it. Nothing written. |
| Daemon shutdown during the wait | `context.Canceled`; the drain's `ctx.Err()` check runs before the error is classified, so the head stays queued and the drain exits. |
| Write fails after the mark (`ErrNoLiveChild`, EPIPE, `turncommit.ErrDropped`) | `undo()` then return the error verbatim — unwrapped, so `msgqueue`'s existing `errors.Is` classification is untouched. The tracker is left exactly as it was before the attempt. |
| `resolve` / `Activate` fail | Unchanged; the new code is never reached and nothing is marked. |
| Tracker nil (PTY, or stream with the relay leg disabled) | Both calls are no-ops. |

**AC3's bound, computed against `queue.go:507-630`.** A conversation whose turn never ends: attempt 1 waits `streamTurnHoldTimeout` and fails, setting `firstFailedAt` (`elapsed ≈ 0 < GiveUpAfter`), sleeps `defaultRetryInterval` (1s), retries; attempt 2 waits again and fails with `elapsed ≈ streamTurnHoldTimeout ≥ GiveUpAfter` (2m) → `giveUp` → `OnGiveUp` → the #1008 `session_error` / `CodeSessionBlocked` frame, head abandoned, drain exits, remaining backlog preserved for the next enqueue. Worst case ≈ 2 × `streamTurnHoldTimeout` ≈ 30 minutes.

**Why 15 minutes, and why no `Pending` analogue.** The give-up path *abandons the head*. Any finite bound therefore trades "a wedged turn is reported late" against "a queued message behind a genuinely long agentic turn is thrown away". A `Pending` analogue (classifying the hold the way `ErrTrustModalPending` is classified at `main.go:908-913`) would reset the give-up streak forever and make AC3 unsatisfiable — the trust modal has that exemption because a *human* decision may legitimately take unbounded time; a running turn should not. 15 minutes sits above any interactive turn observed to date while keeping the client-visible bound inside the half hour. The obviously better discriminator — *staleness* (no turn event for N minutes) rather than *duration* — would require the tracker to store a per-conversation timestamp, which `stream_turn_busy.go:19-22` and #1201 deliberately forbid. Defer it: no long-turn abandonment has been observed, so do not build the defence now. Record the trade-off in the const's doc comment.

**Logging.** No new log call. The bounded-wait failure surfaces through msgqueue's existing `Warn` (`queue.go:592-597`), which carries `conversation_id`, `queued_msg_id`, `queued_at`, `err` and never the text. A held turn that completes normally logs nothing at all — a successful wait is not an event. Adding a per-hold log line would fire once per queued message on the daemon's hot path for no diagnostic gain; the `queue_state` frame is already the observable.

---

## Testing strategy

Fake tier is the primary oracle for AC1–AC4, per the ticket. AC5 is confirmation on top.

### `cmd/pyry/stream_turn_busy_test.go` (extend)

Scenarios, not test bodies:

- `openForDelivery` on a nil receiver returns a non-nil undo; calling that undo does not panic.
- `openForDelivery("")` marks nothing (`Busy("")` stays false) and its undo is inert.
- `openForDelivery(conv)` makes `Busy(conv)` true; its undo makes it false again and wakes a parked `WaitIdle`.
- `openForDelivery(conv)` followed by a `TurnEnd` through `observe` clears normally; a later `undo()` is a documented no-op on an already-idle conversation (assert no panic and no spurious broadcast side effect).
- `waitIdleForDelivery` returns nil immediately on a nil receiver, and on an empty conversation id.
- `waitIdleForDelivery` returns nil promptly once the conversation clears (open, park a waiter, clear, assert the return).
- `waitIdleForDelivery` returns an error satisfying `errors.Is(_, context.DeadlineExceeded)` when the conversation stays busy past a short injected timeout.
- `waitIdleForDelivery` returns `context.Canceled` when the parent ctx is cancelled while parked — the drop path's mechanism.
- **Undo is scoped to the open it made.** Mark the conversation busy through `observe` first, *then* call `openForDelivery(conv)` and run its undo: assert the conversation is still busy. Only a `setBusy`-reported `changed` can produce a live undo, so an implementation that unconditionally clears fails this.

### `cmd/pyry/inbound_deliver_test.go` (extend; reuse `gatingWriter` + the real `msgqueue.Queue` harness at `:78-150`)

- **AC4 — PTY unchanged.** The two existing tests keep their assertions; only the two `newInboundDeliver(resolve)` call sites gain `nil, streamTurnHoldTimeout`. Add one explicit case: with a nil tracker, two back-to-back enqueues both deliver with no hold.
- **AC1 — the hold engages, deterministically.** Tracker wired; enqueue *A*, let the writer return nil; enqueue *B* and *C*. Assert *B* is never written while the tracker reports busy and that `queue.Snapshot(conv)` still lists *B* and *C*. Then clear the tracker (via `observe` with a `TurnEnd`) and assert *B* is written, then *C*. Order asserted from the writer's recorded sequence.
- **AC2 — drop while held.** With *B* parked, `queue.Remove(conv, bID)` returns **true**, *B* is never written, and the drain advances to *C*. The `Remove`-returns-true assertion is the non-vacuous part: it is false today, because the head is `committing` almost immediately.
- **AC3 — bounded failure.** Build with a short injected hold timeout and a short `GiveUpAfter`; hold the tracker busy forever; assert the delivery error satisfies `errors.Is(_, context.DeadlineExceeded)`, the writer is **never** called (no duplicate-turn risk), and `OnGiveUp` fires.
- **Undo on write failure.** Writer returns an error → assert `Busy(conv)` is false afterwards, i.e. no stale mark survives a failed write.
- **Mark placement.** Writer returns nil → assert `Busy(conv)` is true. Assert from *inside* the writer stub that the conversation already reads busy when the write is entered — that is the mark-before-write ordering, and a mark-after-write implementation fails it.

### `internal/e2e/relay_v2_stream_queue_drain_test.go` (extend + rename)

Rename `TestRelayV2_StreamQueueDrainsInOrder` → `TestRelayV2_StreamMidTurnHoldDropAndDrainInOrder` and rewrite the `:33-48` doc block: the "queue_state depth of N is NOT reliably observable here" paragraph is the repo's record of the gap this ticket closes, and leaving it beside a contradicting test is worse than either editing or duplicating.

Extend to **four** sends (msg1–msg4) so the original three-way ordering assertion survives intact and msg4 becomes the dropped one:

1. Steps 1–2 unchanged in shape: four sends back-to-back while the child is held; collect all four acks before touching the trigger (the existing vacuity gate, widened to four).
2. **[AC1]** Drain frames until a `queue_state` for `knownConvID` whose entries contain msg2, msg3 **and** msg4 simultaneously; record msg4's `queued_msg_id`. Fatal on deadline naming the regression: the backlog drained on write, so the mid-turn hold never engaged. Non-vacuous because msg2 can only still be in the backlog if its delivery is parked — today it is written into the pipe microseconds after msg1.
3. **[AC2]** Send `dequeue_message` for msg4's id (mirror `relay_v2_dequeue_test.go:200-235`); wait for an updated `queue_state` whose entries exclude msg4 and still include msg2 and msg3.
4. Release the hold (`PYRY_FAKE_CLAUDE_STREAM_HOLD` trigger file), unchanged.
5. **[AC1 + AC2 terminal]** Collect assistant deltas: assert **exactly three**, carrying msg1/msg2/msg3 in submission order, that **no** delta contains msg4, and then a terminal `turn_state{idle}`. A fourth delta, or msg4 in any delta, is the sharpest failure message available — the dropped message reached claude.

Note for the developer: after the change the three turns run *sequentially* (each released by the previous turn's `TurnEnd`) rather than being buffered in the stdin pipe. With `fakeclaude` that is milliseconds; the existing 20s drain deadline is untouched.

**AC4 — do not open** `internal/e2e/relay_v2_queue_drain_test.go` or `internal/e2e/relay_v2_dequeue_test.go` except to read. Their passing unedited is the acceptance evidence.

### Gates

`go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test -race ./...`, and the e2e tag (`go test -race -tags e2e ./internal/e2e/ -run 'StreamMidTurnHold|QueueDrain|Dequeue'`). Report the e2e run explicitly — a `make` target that dies early silently skips its tail, and `ok <pkg> <time>` with no `-v` hides a skipped build-tagged package.

`gofmt -l` over the repo lists pre-existing offenders; scope the check to the files in this diff.

---

## Acceptance criteria (developer's deliverables)

1. `turnBusyTracker` gains `waitIdleForDelivery` and `openForDelivery` as specified — nil-receiver-safe, empty-key-safe, delegating to `setBusy` / `WaitIdle`, adding no fields and no new synchronisation. `setBusy` reports whether membership moved, and `openForDelivery`'s undo is a no-op when it did not. `Busy` and `WaitIdle` keep their existing contracts unedited.
2. The tracker's file-level doc records the new open feed and re-states the no-forever-busy analysis under it, keeping #1198's KNOWN-GAP/SATISFIED record intact, and restates the SECURITY note honestly for the delivery-supplied conversation id.
3. `newInboundDeliver` takes the tracker and the hold timeout, and performs the bounded wait then the mark, both between `Activate` and `WriteUserTurn`, with `undo()` on a write error. A nil tracker leaves behaviour semantically unchanged.
4. `streamTurnHoldTimeout = 15 * time.Minute` exists beside `inboundActivateTimeout`, documented with the give-up arithmetic and the no-`Pending`-analogue decision; `inboundActivateTimeout`'s stale clause is amended.
5. The tracker is constructed at the composition root gated on `streamSink != nil` and threaded to both consumers through the new `relayWiring.busy` field; `relay.go`'s local construction and its now-inverted `:731-746` comment are removed/rewritten.
6. Unit coverage per § Testing strategy, including the mark-before-write ordering assertion and the AC3 bounded-failure case.
7. The stream e2e is extended and renamed as specified, its stale `:33-48` rationale rewritten, and it proves the deterministic backlog, the drop, and that the dropped message never reaches claude.
8. `go vet`, `staticcheck`, `go test -race ./...`, and the e2e tag all pass; the PTY queue-drain and dequeue e2e files are unedited.

---

## Scope self-check

Production source files (non-test `.go`) this spec prescribes content for: `cmd/pyry/main.go`, `cmd/pyry/relay.go`, `cmd/pyry/stream_turn_busy.go` — **3**, below the 5-file gate. **0** new files. **0** new exported types. `newInboundDeliver`'s signature fan-out is **3** call sites (1 production, 2 test), below the 10-site line. Reject branches added to a state machine: **1** (the bounded-wait failure). Projected total written work ≈ 105 production + ≈ 350 test ≈ **455 lines**.

---

## Open questions

- **`streamTurnHoldTimeout`'s value is a judgement, not a derivation.** 15 minutes is chosen against no measured distribution of real turn durations. If production ever surfaces a `session_error` for a turn that was legitimately progressing, the fix is the staleness-based bound (last-event age), which needs a per-conversation timestamp on the tracker and therefore a #1201-contract amendment — a separate ticket, not a widening of this constant.
- **The relay leg disabled (`relayURL == ""`)** leaves the tracker constructed but never fed, because `startRelayV2` — and with it `startStreamTurnDrainV2` — never runs. This is inert rather than hazardous: the queue's only producer is the relay's `send_message` handler, so `deliver` is never called at all. Confirm that reading of `startRelay`'s early return while wiring, and if it turns out a non-relay producer can enqueue, raise it rather than papering over it.
- **`conversationForSession` is an O(conversations) scan per call**, unchanged by this ticket (the new methods do not call it). It stays within budget at the current arrival rate; a future slice that raises the rate wants a by-session-id read on the conversations registry.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the boundary needed restating rather than assuming. `turnBusyTracker`'s existing SECURITY note (`stream_turn_busy.go:53-58`) claims "the key is never taken from the wire" — true of `observe` and `clearForSession`, which key off a session id the daemon itself produced. `openForDelivery` breaks that literal wording: its key is the conversation id that arrived in a `send_message` payload. It does **not** break the property the wording protects, because that id passes two independent daemon-side gates before it can reach the mark — `router.Route` at enqueue (`send_message.go:145`: registry hit → non-empty `CurrentSessionID` → `pool.Lookup` hit, rejected synchronously otherwise, "the routing target is read from the server-stored registry row, never phone-writable") and `sessionRouter.resolve` again at delivery (`main.go:1202-1216`, the first statement of `deliver`). An unknown, unbound, or forged id returns before the mark. The mark therefore only ever describes the conversation the daemon is about to write to. **The spec requires that note be rewritten to say this** (AC2) rather than left asserting something the new feed makes false — a stale-and-inverted comment is the failure mode #1207 and #1210 both caught.
- **[Trust boundaries]** No finding — the child's influence is unchanged. The original invariant is "a hostile or confused child can only ever mark its OWN conversation busy." The new feed's key comes from the delivery target, which no child can influence at all, so the child's reachable set does not grow.
- **[Trust boundaries / existence oracle]** No finding. `Busy`'s doc treats its signature as the enforcement against a "does conversation X exist" oracle. `openForDelivery` inserts a key but exposes no new read: `Busy`/`WaitIdle` still collapse unknown/unbound/idle to the same answer through one map lookup, and no method enumerates the map or reaches the wire. Marking a conversation the caller is already authorized to write to reveals nothing the caller did not supply.
- **[Tokens, secrets, credentials]** Not applicable by design — the change handles no token, key, or credential, adds no persistence, and performs no comparison against a secret.
- **[File operations]** Not applicable by design — no path is constructed, opened, statted, or written. The change is a map membership bit and a context timeout, entirely in memory.
- **[Subprocess execution]** No finding, and the negative is worth pinning: the gate sits strictly *in front of* `WriteUserTurn` and never touches the payload bytes or the spawn args, so `marshalTurnEnvelope`'s single-physical-line injection resistance (`envelope.go:43-51`) — an untrusted prompt cannot forge a `result`, a `control_request`, or an approval on claude's stdin — is untouched. Checked separately for a **duplicate-write** path, since a retry that re-writes an already-delivered turn would be an integrity bug: the new failure (hold timeout) returns having written **zero** bytes, so its retry writes exactly once; the write-success path returns nil and the head advances; the write-failure path is today's retry semantics unchanged. No new duplicate path.
- **[Cryptographic primitives]** Not applicable by design — no randomness, no key material, no comparison of attacker-controlled values.
- **[Network & I/O — resource exhaustion]** SHOULD FIX, recorded, no code change. The hold shifts queued bytes out of the child's stdin pipe buffer and into the daemon's in-memory backlog for the duration of each turn, so `defaultMaxQueuedPerConversation` (100, `queue.go:85`) × the transport's 1 MiB frame ceiling — the bound #869 established — becomes *routinely reachable* on the stream path where it previously drained continuously. The bound's value does not change, `Enqueue` still rejects past it (reject, never drop) with a retryable reply, and this is precisely the posture the PTY path has held since #704. Code-review should know the reachability changed, not the bound. The cross-conversation multiplier (N conversations × the per-conversation cap) is #869's deliberate per-conversation scoping and stays out of scope here.
- **[Network & I/O — timeouts]** No finding. Every wait is bounded: `streamTurnHoldTimeout` on the hold, `inboundActivateTimeout` on the Activate in front of it, and the parent lifecycle ctx over both. No socket read, no new listener, no new deadline surface.
- **[Error messages, logs, telemetry]** No finding. The change adds **zero** log call sites. The only new error string is the static literal `"stream turn hold: "` wrapping a bare `context` error — no interpolation of phone-supplied data, no text, no id. The failure surfaces through msgqueue's pre-existing retry `Warn` (`queue.go:592-597`), which already carries `conversation_id`/`queued_msg_id`/`queued_at`/`err` and already fires on every `ErrNoLiveSession`; this ticket gives that identical line a new trigger, not a new field. Queued text remains unlogged at every level.
- **[Concurrency — lock ordering]** No finding. Both new methods take `t.mu` only, take it as a leaf, and are called with `q.mu` released (`queue.go:504-506` never holds it across `deliver`). Neither calls `t.resolve`, so the `tracker.mu → convReg.mu` order the file warns about is unreachable from the new surface.
- **[Concurrency — TOCTOU]** SHOULD FIX — **closed in this spec's design, not deferred.** `waitIdleForDelivery` returning nil and `openForDelivery` marking are two separate lock acquisitions, so a competing feed's opener (a `--resume` respawn replaying events is the plausible route) can land in the gap. A naive undo would then clear a turn this delivery never opened, reporting a live turn idle and letting the *next* message through unheld. Consequence is a transient recurrence of the bug being fixed — no confidentiality or integrity impact, self-correcting on the next opener event — which is why this is SHOULD and not MUST. It is nonetheless closed: `setBusy` now reports whether membership moved, out of the single lock acquisition it already takes, and the undo is live only when it did. Deriving the same answer from a separate `Busy` read would have reintroduced the very TOCTOU it is meant to close.
- **[Concurrency — shutdown]** No finding. The drain's `ctx.Err()` check runs before any error classification (`queue.go:540-548`), so shutdown wins over a hold, a timeout, and a drop alike; the head stays queued (the documented in-memory loss boundary) and the drain exits into `Run`'s `wg.Wait`.
- **[Concurrency — goroutine lifecycle]** No finding. No goroutine is spawned. The existing per-conversation drain goroutine lives longer while parked, but its exit set is exhaustive and unchanged in count: idle (any of four clears), drop (`deliverCancel`), shutdown (parent ctx), or `streamTurnHoldTimeout`. No path leaves the wait unbounded, so no drain can leak.
- **[Threat model — cross-conversation confidentiality]** No finding. Target selection is untouched: `sessionRouter.resolve` remains the sole chooser of the session a payload reaches, and the tracker's key is the same conversation id that delivery targets. Nothing in this change can route a message to a session other than its conversation's binding.
- **[Threat model — authorization surface of the drop]** No finding, recorded because the window widens. `dequeue_message` is capability-gated to interactive conns and shipped in #723; this ticket adds no verb, no gate, and no caller. It widens the interval in which the *head* is removable — which is #487's explicit intent ("that is the message a user queued behind a running turn precisely so they could still cancel it", `queue.go:373-379`). Any interactive paired device could already dequeue a conversation's non-head entries, so the authorized set does not grow.
- **[Threat model — self-inflicted denial of service]** No finding. An authorized phone can hold its own conversation by sending a turn that never ends, bounded at ~2 × `streamTurnHoldTimeout` by the give-up path into a typed `session_error`/`CodeSessionBlocked`. It cannot hold a conversation it is not authorized to write to (the mark is per-conversation and reachable only through the two resolve gates), and it cannot hold one indefinitely. AC3 is this finding's mitigation, stated as an acceptance criterion.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-25
