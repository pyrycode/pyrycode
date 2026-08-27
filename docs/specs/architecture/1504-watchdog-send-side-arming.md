# Spec #1504 — `internal/streamsup`: the stall watchdog arms from the send side, not from construction

The #1094 watchdog was lifted from `streamrunner`, whose runner *is* the send side and so may
assume an owed turn the moment it is constructed. `streamsup`'s watchdog is constructed away from
the send side and cannot. This slice replaces the construction-time assumption with two explicit
lifecycle signals and re-anchors the five tests that encode the old premise.

**No production caller is added.** Verified at this branch's base: `codegraph_impact NewWatchdog`
returns `NewWatchdog` itself plus five tests, and the only non-test reference anywhere in the tree
is a prose mention in `internal/turnevent`'s `ThinkingProgress` doc comment. Composing `Writer()` into
`Config.Stdout` and deciding what a `stall(false)` does downstream stays with the still-unfiled
wiring slice.

---

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/watchdog.go` | `newStallTracker` | The defect: `awaiting: true` in the literal, and the doc sentence AC5 retires. |
| `internal/streamsup/watchdog.go` | `consumeLine` | The **only** existing writer of `awaiting`. The two new signals mirror its transition arms; read the `assistant`/`user`/`result` arms and the trailing comment naming the activity-only types. |
| `internal/streamsup/watchdog.go` | `stallTracker`, `snapshot` | The lock discipline: `mu` is a leaf, taken by the forwarder goroutine's `Write` and the poll goroutine's `snapshot`. Both new methods join that set; neither introduces a new lock. |
| `internal/streamsup/watchdog.go` | `shouldFire` | The pure predicate. **It does not change.** Everything in this slice moves its two inputs, not its logic. |
| `internal/streamsup/watchdog.go` | `Watchdog`, `Start` | Where the two exported methods land. `Watchdog`'s type doc carries the `DIVERGENCE from streamrunner` paragraph AC5 wants a second one written alongside. `Start`'s doc describes the edge-trigger latch the new signals interact with. |
| `internal/streamsup/watchdog_test.go` | `fakeClock`, `recvStall` | The two helpers every new test reuses. `fakeClock` is currently used only by tracker unit tests; this slice also uses it at the `Watchdog` level (see Testing strategy). |
| `internal/streamsup/watchdog_test.go` | `TestStallTracker_AwaitingTransitions`, `TestStallTracker_LineBuffering`, `TestWatchdog_GenuineIdleFires`, `TestWatchdog_PendingPermission_EmitNotKill`, `TestWatchdog_LatchReArms` | The five tests the ticket lists. Each needs a specific re-anchor, tabulated below — do not just delete assertions. |
| `internal/streamsup/runner.go` | `WriteUserTurn` | The send-side signal's future caller. Its error contract — `ErrNoLiveChild` (no live child, or a `BeginRotation` gate) and `turncommit.ErrDropped` (gate deny, zero bytes) — is what the new method's contract must name. |
| `internal/streamsup/runner.go` | `Run`, `spawnAndWait`, `takeStdin` | The exit side, and the evidence for the one-watchdog-per-runner decision below: `Config.Stdout` is fixed at `New` and the loop never re-wires it. |
| `internal/streamsup/parser_test.go` | `discardLogger` | The package's shared logger double — reuse it, don't mint a second. |
| `internal/agentrun/streamrunner/watchdog.go` | `newStreamParser` | The lifted original. Read it to see the premise being diverged from, so the new doc comment describes the divergence accurately rather than deleting the sentence. |
| `docs/knowledge/features/streamsup-package.md` | § "Idle/stall watchdog — receive-side, emit-not-kill (#1094)" | The evergreen description of the component. Also read, in the § "Two tiers" measurement list, the bullet recording that claude does **not** echo the delivered prompt back as a `user`/`text` line on this surface — that measurement is why a send-side signal is mandatory rather than optional. |

Do not edit `docs/knowledge/features/streamsup-package.md` or `docs/knowledge/codebase/1504.md`; the
documentation phase owns both.

---

## Context

`newStallTracker` starts `awaiting: true`. On the interactive surface the child spawns on
`Activate`/`RestartFresh` and emits `system`/`init` before any user envelope exists — a message may
not arrive for hours. `init` is activity-only and never clears `awaiting`, so `shouldFire` trips
240 s after spawn against a perfectly healthy idle session, and re-trips once per episode whenever
any later line re-arms the latch. The same false positive recurs after a crash-mid-turn respawn: the
abandoned turn leaves `awaiting=true` against a child that owes nothing.

Starting `awaiting: false` and stopping there is **not** the fix. Because claude does not echo the
delivered prompt back as a `user` line here (measured; recorded in the feature doc), the only stdout
lines that set `awaiting=true` are `user` lines — tool results and the #1247 harness nudge. A tracker
that started false with no send-side input would never arm before claude's first output, so the
wedge this component exists to catch — a turn that produces nothing at all — would go undetected
forever. The false positive would become a silent false negative.

So the fix is: **initial state false, plus two explicit lifecycle signals.** Both are facts the
wiring slice already holds.

---

## Design

### The two new signals

Two exported methods on `*Watchdog`, each forwarding to one unexported tracker method. No new type,
no new field on `WatchdogConfig`, no change to `shouldFire`.

```go
// UserTurnSent tells the watchdog a user turn's bytes reached the child's stdin.
func (w *Watchdog) UserTurnSent()

// ChildExited tells the watchdog the child it was watching is gone.
func (w *Watchdog) ChildExited()
```

Behaviour, one line each:

- `UserTurnSent` — sets `awaiting = true` **and** stamps `lastEvent = now()`.
- `ChildExited` — sets `awaiting = false` **and** stamps `lastEvent = now()`.

Both take `t.mu` for the duration, exactly as `feed` and `snapshot` do. `mu` stays a leaf: neither
method logs, calls out, or takes a second lock, so a caller may invoke either from any goroutine with
any of its own locks held — the property `Restart`/`SetSpawnArgs` already rely on elsewhere in the
package.

The unexported halves live next to `consumeLine`, because they are the same kind of thing: the third
and fourth writers of `awaiting`, expressed in the same shape.

### Why `UserTurnSent` must stamp `lastEvent`, not just set `awaiting`

This is load-bearing, not tidiness. An idle child emits nothing, so `lastEvent` can be hours stale by
the time a turn is finally sent. A `UserTurnSent` that set only `awaiting` would satisfy
`now - lastEvent > Idle` on the very next tick and fire immediately — a false positive strictly worse
than the one this ticket removes, because it fires on the exact turn a user just asked for. The stamp
is what makes the threshold mean "silent for `Idle` **since the turn was sent**". It carries its own
test row (see Testing strategy, T3).

### Why the send-side signal means *bytes actually reached the child*

`WriteUserTurn` has three outcomes and only one of them owes a turn:

| `WriteUserTurn` returns | Bytes written | Signal? |
|---|---|---|
| `nil` | the envelope | **yes** — claude owes the assistant turn |
| `ErrNoLiveChild` (no live child, or a `BeginRotation` gate) | zero | no |
| `turncommit.ErrDropped` (gate deny) | zero | no |

Arming on a refused or dropped turn reintroduces the same false stall by another route: nothing was
delivered, so nothing is owed, and the watchdog would fire 240 s later against a child that never saw
the turn. State this in `UserTurnSent`'s doc comment explicitly — name both sentinels — so the wiring
slice cannot get it wrong. The method takes no arguments and returns nothing precisely so the
decision stays at the one call site that can see the error value.

**Call it after a successful write, on the writing goroutine, before returning to the caller.** The
alternative (arm first, unwind on error) leaves an armed tracker behind any error path, which is the
failure being removed. Arming after the write opens a nanosecond-width reorder — claude's first
output could in principle land before the arm, flipping `awaiting` back to true after an `assistant`
line already cleared it. Bounded and self-healing: the arm is microseconds after the write syscall
against claude's tens-of-milliseconds first output, and the next `assistant`/`result` line clears it
regardless. Named in the doc comment, not mechanised — no observed failure justifies a sequence
number here.

Control requests do **not** arm. `Interrupt` and `RevokeBypass` write `control_request` lines whose
ack arrives in ~40 ms (#1075/#1595) and which are not user turns; no wedge has been observed on that
path. Say so in the doc comment so the wiring slice does not generalise "a write happened" into an arm.

### Why one watchdog per runner with a reset, not one per child

The ticket leaves this to the architect. It is settled by the code rather than by taste:
`Config.Stdout` is fixed when `New` builds the `Runner` and the supervise loop never re-wires it, so a
per-child watchdog would have no way to reach the new child's stdout — the fan-out `io.MultiWriter`
is composed once, for the runner's lifetime. Per-child construction would also multiply the poll
goroutine per spawn against a `Start` that the #1094 review already noted has no double-call guard.
One watchdog per runner, reset by `ChildExited`, keeps exactly one poll goroutine and exactly one
`Start`/`Wait` pair for the runner's life — the contract `Start` already documents.

`ChildExited` therefore means "everything the previous child owed is void", and the respawned child
starts from the same state a freshly-constructed tracker has: not awaiting, `lastEvent` fresh. That
is the whole of AC3.

### What is deliberately *not* reset on child exit

`ChildExited` does **not** drop the tracker's partial-line remainder. A dead child's trailing partial
does concatenate with the new child's first line, but the result is one unparseable line, which
`consumeLine` already treats as activity-only — it cannot flip `awaiting` to a wrong value, and the
activity stamp still lands. The cost is at most one line's `type` going unread, and the first line
after a spawn is `system`/`init`, which is activity-only anyway. Adding the drop would be a defence
against a failure mode that has never been observed, and the sibling question for the #1088 `Parser`
is explicitly still open in the feature doc's deferred list. Leave it there; do not add it here.

### Doc comments (AC5)

Two edits, both required:

1. **`newStallTracker`** — delete the sentence asserting the streamrunner premise ("the caller writes
   the opening user envelope to claude's stdin before the child produces any output"). Replace it
   with what is now true: the tracker starts **not** awaiting, and arms only on a `user` line from the
   child or an explicit `UserTurnSent`. Say why the initial value cannot be `true` here even though
   `streamrunner`'s `newStreamParser` sets it — the interactive child spawns before any turn exists.
2. **`Watchdog`** — add a second `DIVERGENCE from streamrunner` paragraph alongside the existing
   emit-not-kill one. Content: `streamrunner`'s runner *is* the send side, so it can assume an owed
   turn at construction; this watchdog is constructed away from the send side, so the owed-turn fact
   must be supplied by `UserTurnSent`/`ChildExited`. Note the asymmetry that makes this divergence
   unavoidable rather than stylistic — claude does not echo the prompt back on this surface, so the
   stdout stream alone cannot tell the watchdog a turn was sent.

Both new exported methods need their own doc comments carrying the contracts stated above.

---

## Concurrency model

No new goroutine, no new lock, no change to the poll loop.

| Goroutine | Touches |
|---|---|
| os/exec's stdout forwarder | `Write` → `feed` → `consumeLine` (takes `mu`) |
| the poll goroutine (`Start`) | `snapshot` (takes `mu`) |
| whichever goroutine calls `WriteUserTurn` | `UserTurnSent` (takes `mu`) — **new** |
| the `Run` goroutine | `ChildExited` (takes `mu`) — **new** |

Four writers of `awaiting` where there was one, all serialised by the same leaf mutex. `-race` over
the existing tests plus the new ones is the check; no ordering guarantee is offered or needed between
the two new signals and the forwarder, because every reachable interleaving is a state the tracker
already handles (the reorder window is named in the Design section and self-heals on the next line).

Shutdown is unchanged: `Start`'s ctx cancel ends the poll goroutine and `Wait` joins. Neither new
method blocks, so neither can delay teardown.

---

## Error handling

Neither method can fail, and neither returns an error — that is the point of putting the
delivered/refused decision at the call site rather than inside the watchdog. There are no new
failure modes and no new reject branches. Both methods on a `*Watchdog` built by `NewWatchdog`
always have a non-nil tracker, so neither can nil-deref.

The one recovery property worth stating: a wrongly-armed tracker is always self-healing. Any
`assistant` or `result` line clears `awaiting`, and `ChildExited` clears it unconditionally. The
worst outcome of a mis-signal is one spurious `OnStall(pending=false)` — which, by the emit-not-kill
divergence, kills nothing.

---

## Testing strategy

Stdlib `testing`, table-driven where the shape suits, `go test -race`. Reuse `discardLogger`,
`fakeClock` and `recvStall`; add no new helper unless a scenario genuinely needs one.

`WatchdogConfig.now` is unexported and the test file is in-package, so `Watchdog`-level tests can run
on `fakeClock` instead of a real short `Idle`. Use it for T3, where the assertion is about *when* the
threshold starts counting. The ticker still fires on the real clock; only the elapsed comparison
reads the fake, which is what makes "advance the fake clock, then wait for one real tick" work.

### New scenarios

- **T1 — AC1, silent child, nothing emitted.** Construct a watchdog, `Start`, write nothing at all,
  wait several idle windows. Assert `OnStall` is never called. Reddens against the constructor's
  `awaiting: true`.
- **T2 — AC1, child emitted `system`/`init` only.** Same, but write one `{"type":"system","subtype":"init"}`
  line before `Start`. Assert no fire. This is the ticket's literal failure scenario, and it is the
  inversion of what `TestWatchdog_GenuineIdleFires` asserts today.
- **T3 — AC2, the false-negative guard.** Construct, `Start`, call `UserTurnSent()`, keep the stream
  silent past the threshold, assert `OnStall` fires exactly once with `pending=false`. Reddens against
  a `UserTurnSent` stubbed to a no-op — i.e. against a "fix" that only flips the initial value.
- **T4 — AC2, the stamp.** On `fakeClock`: construct, `Start`, advance the fake clock well past `Idle`
  with no activity (no fire, since nothing is owed), then `UserTurnSent()`. Assert **no** fire for a
  span in which a stale `lastEvent` would have fired immediately; then advance past `Idle` again and
  assert the fire lands. Reddens against a `UserTurnSent` that sets `awaiting` without stamping
  `lastEvent`.
- **T5 — AC3, the exit reset.** `UserTurnSent()` (arm), then `ChildExited()`, then simulate the
  respawn by writing a `system`/`init` line, then sit silent past the threshold. Assert no fire.
  Then `UserTurnSent()` again and assert the fire does land — the reset must clear the owed turn
  without disabling the watchdog. Reddens against a `ChildExited` that is a no-op **and** against one
  that stamps `lastEvent` but leaves `awaiting` true.
- **T6 — tracker-level unit rows.** Extend `TestStallTracker_AwaitingTransitions` (or add a sibling)
  with the two new transitions read through `snapshot`: `turnSent` → `(true, now)`, `childExited` →
  `(false, now)`, each asserting the timestamp moved with the fake clock.

### Re-anchoring the five existing tests

The trap named in the ticket: the cheapest way to keep `make check` green is to not fix the bug.
Every row below must be *re-aimed*, not deleted, and none of the surviving assertions may become
vacuous.

| Test | Today | Re-anchor |
|---|---|---|
| `TestStallTracker_AwaitingTransitions` | Opens by asserting the tracker starts awaiting; then asserts `system` leaves `awaiting=true`, which is what gives the following `assistant`→false row its discriminating power. | Flip the opening assertion to **not** awaiting. Then call `turnSent()` to arm before the `system` row, so `system`-leaves-it-true and the `assistant`→false row that follows both still discriminate. Every later row (`user`→true, `assistant`→false, `tool_result`→true, `result`→false) is unchanged. |
| `TestStallTracker_LineBuffering` / "partial line does not transition" | Asserts the partial leaves `awaiting` true, then the completion flips it false. | Arm with `turnSent()` first, then assert the partial leaves it true and the completion flips it false. Without the arm both assertions read `false` and prove nothing. |
| `TestStallTracker_LineBuffering` / "multiple complete lines in one write" | `assistant` then `user` in one write; final state `true`. | Unchanged — the final `user`→true still discriminates from a false start. |
| `TestStallTracker_LineBuffering` / "oversized partial dropped" | Ends on `assistant`→`awaiting` false. | Arm with `turnSent()` before the recovery write, or the closing assertion is vacuous against a false start. |
| `TestWatchdog_GenuineIdleFires` | Fires after a lone `system`/`init` line — **the ticket's failure scenario, asserted as correct.** Misnamed. | This scenario becomes T2 (assert **no** fire). The genuine-idle-fires intent moves to T3, which arms via `UserTurnSent`. Rename accordingly so the name matches the scenario. |
| `TestWatchdog_PendingPermission_EmitNotKill` | Arms via a lone `system` line. | Arm via `UserTurnSent()` instead. Everything else — the `pending=true` flow-through, the exactly-once latch, the `ctx.Err() == nil` emit-not-kill check — is unchanged and is AC4's pin. |
| `TestWatchdog_LatchReArms` | Arms via a lone `system` line, fires, writes a `system` line to clear the latch, expects a re-fire. | Arm via `UserTurnSent()`. The latch mechanics are unchanged: `awaiting` stays true across the intervening `system` line, so activity clears the latch and the next window re-fires. |

`TestWatchdog_InFlightToolSilence_NoFire`, `TestStallTracker_ContentFree`,
`TestStallTracker_LastEventAdvances`, `TestShouldFire`, `TestWatchdog_CleanShutdown_NoLeak` and
`TestWatchdogTickFor` need **no** change and must stay green unmodified — they are AC4's regression
pins. Note that `TestWatchdog_InFlightToolSilence_NoFire` is now no-fire for two independent reasons
(the `assistant` line clears `awaiting`, and nothing armed it in the first place); leave it as-is,
its discriminating power against a type-blind watchdog is unaffected.

`make check` is the gate. No live claude is needed — every criterion is reachable through the
existing `now` seam.

---

## Open questions

1. **Method names.** `UserTurnSent`/`ChildExited` is the spec's choice: past-tense observer
   notifications, and `UserTurnSent` deliberately echoes `WriteUserTurn` so the one call site that may
   use it is obvious and a control-request write is obviously not it. If the developer finds a
   stronger house precedent for a `Note…`/`On…` prefix in this package, take it — the contract, not
   the spelling, is what this spec fixes.
2. **Does the wiring slice need a "turn refused" signal too?** No, and it should not get one from
   here: the refusal cases write zero bytes, so the correct action is to *not* call `UserTurnSent`,
   which needs no API. Recorded so the wiring slice does not invent a symmetric method.
3. **`docs/knowledge/features/streamsup-package.md` § "Idle/stall watchdog" goes stale** the moment
   this lands — it states the tracker's transition set without the two new signals, and the Public API
   block omits them. That is the documentation phase's edit, not the developer's; flagged here so it
   is not missed at merge.
