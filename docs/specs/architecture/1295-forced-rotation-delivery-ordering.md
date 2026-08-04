# 1295 — Force the queued-turn-across-`new_session`-rotation ordering deterministically

**Ticket:** [#1295](https://github.com/pyrycode/pyrycode/issues/1295) · **Size:** S · **Scope:** test-only (`cmd/pyry`), no production code · **Labels:** `bug`, `size:s`

---

## Files to read first

Turn-1 reading list. Each entry says what to extract; do not read past it.

| Path | What to extract |
|---|---|
| `cmd/pyry/main.go:1625-1660` | `newInboundDeliver` — **the seam under test.** The exact statement order: `resolve` → `Activate` → `waitIdleForDelivery` → `openForDelivery` → `WriteUserTurn` → `undo` on error. Note that `resolve` runs **once, before the hold**, so a parked delivery writes through a pre-rotation-resolved writer. |
| `cmd/pyry/main.go:1514-1552` | `inboundActivateTimeout` (30 s) and `streamTurnHoldTimeout` (15 min) doc comments — the two silent bounds the ticket's three-row table names, and the arithmetic against msgqueue's give-up. |
| `cmd/pyry/inbound_deliver_test.go` (whole file, 557 lines) | **The existing harness this ticket extends.** Reuse: `inboundTestLogger`, `recvStringWithin`, `recvErrWithin`, `assertNoWriteWithin`, `holdTestTracker`'s shape. Read `TestInboundDeliver_StreamHold_HoldsMidTurnSends` (:329) closely — its "the determinism is structural, not timing-based" argument is the one this ticket's test extends across a rotation. |
| `cmd/pyry/stream_turn_busy.go:349-395` | `waitIdleForDelivery` + `openForDelivery` — the arm being forced, and the "WHY THE HOLD IS DETERMINISTIC" paragraph. |
| `cmd/pyry/stream_turn_busy.go:200-250` | `clearForSession` — the release. It is **session-keyed**, resolved daemon-side, and skips silently at **Debug** when the session resolves to no conversation. |
| `cmd/pyry/session_transition_v2.go` — `transitionClearsTurn` + its doc | **Load-bearing and easy to get wrong: the clear is keyed to `NewSessionID`, not `PreviousID`.** For a `ReasonClear` transition that is the *post*-rotation id, and it only resolves because `notifyTransition` drives `rebindConversation` **ahead of** the observer fan-out. |
| `internal/sessions/transition.go:56-140` | `notifyTransition` → `rebindConversation` → observer, then `RotateForNewSession`. The ordering the fixture must mirror: re-key the binding, *then* fire the clear. |
| `cmd/pyry/main.go:1447-1478` | `startFreshRunner` — `rotate(oldID)` (which includes the whole transition fan-out **and the clear**) completes **before** `RestartFresh(newID)`. This gap is the window § Design discusses. |
| `cmd/pyry/relay.go:845-855` | `conversationForSession` — matches `CurrentSessionID` **or** `SessionHistory`. This is what the resolve fake models. |
| `internal/streamsup/envelope.go:13-18`, `:139-152`; `internal/streamsup/runner.go:263-285` | `ErrNoLiveChild` — returned by `WriteTurn` when `Runner.Stdin()` is nil (before first spawn, between spawns, **mid-restart**). Retryable, writes nothing. The fixture returns this sentinel verbatim. |
| `internal/msgqueue/queue.go:566-620` | The drain's outcome branches: `err == nil` → advance; `err != nil` → `firstFailedAt` set, 1 Hz `Warn`, `sleepCtx(ctx, q.retry)`, retry the **same head**. Note `Config.RetryInterval` / `GiveUpAfter` are injectable. |
| `cmd/pyry/interactive_turn_v2_test.go:25,45` · `cmd/pyry/stream_turn_busy_test.go:23` | `testConvID`, `discardLogger`, `stubBusyResolve` — existing package-level test helpers. `stubBusyResolve` takes a **static** map and is therefore *not* reusable here; the rotation needs a mutable one (§ Design). |
| `docs/knowledge/codebase/1137.md` | Background on the e2e milestone structure (M1–M4) the failure record uses. |

---

## Context

`TestRelayV2_StreamNewSessionRotatesAndRestartsFresh` has failed twice at M4 with the same
shape: rotation succeeds, the daemon **accepts** the follow-up turn (`ack #2 received=true`),
and then zero bytes reach any child for the full 20 s deadline. The e2e reproduces at roughly
1-in-N over weeks, so "run it more" is unbounded work. This ticket replaces sampling with a
**forced** ordering at the delivery seam, one layer below e2e.

The seam has three places a delivery can sit (ticket's Context table). All three are bounded;
two of the bounds (30 s, 15 min) exceed the test's 20 s window, so within that window a parked
delivery is *observationally* a permanent stall. The record's zero bytes rules out
"delivered, just late" and the transient-error reading, but does **not** separate
"parked in a silent bound" from "erroring persistently". #1296 separates those by capturing
the daemon log across the dead window. This ticket does not wait for that answer.

**Which arm this ticket forces: row 2, `busy.waitIdleForDelivery(ctx, convID, hold)`.**
It is the arm consistent with every feature of the record:

- it sits **after** `Route` resolved and the message was enqueued → `ack #2 received=true` holds;
- it sits **before** `WriteUserTurn` → zero bytes, exactly as the stdin log shows (99 → 99);
- its bound is `streamTurnHoldTimeout` = **15 min** → nothing expires inside a 20 s window;
- it emits **nothing** while parked. The error arm emits a 1 Hz `msgqueue: delivery failed,
  will retry` **Warn** at the daemon's default Info level; the record carries no such line.

Row 1 (`Activate`, 30 s) is also silent and also outlasts the window, but the pre-rotation turn
had already been delivered through this same conversation (M1 echoed it), so its session was
live and `Activate` on a live session is a no-op — a wedged respawn is not the shape here.
Row 3 returns at once on the stream path.

**What makes this forceable.** The seam is already fully injectable —
`newInboundDeliver(resolve, busy, hold)` takes all three as parameters, and
`msgqueue.Config.Deliver` is a `DeliverFunc`. `cmd/pyry/inbound_deliver_test.go` is the
existing live-wiring harness: a real `msgqueue.Queue` driving the real seam against fakes.
This ticket adds one file beside it. **No production code changes.**

---

## Design

### The ordering, stated once

> A turn is enqueued for a conversation whose bound session already has a **running turn**.
> The drain reaches the hold and parks — deterministically, by program order, not by timing.
> The `new_session` rotation then runs: the conversation's binding is re-keyed, the child is
> torn down, and the transition's `clearForSession` fires. **That clear is the only thing that
> releases the parked delivery.** The turn must land in the **post-rotation** child's stdin,
> and in no other child's.

Nothing in that sequence is ordered by a sleep. Each step's completion is observed through a
channel the fixture signals on.

### New file

`cmd/pyry/inbound_deliver_rotation_test.go` (package `main`). One file, no others.

### Fixture 1 — `rotatingWriter`: one runner across a rotation

Models the production indirection `boundSession.WriteUserTurn` → `*sessions.Session` →
`streamRunner` → `*streamsup.Runner.WriteUserTurn` → `WriteTurn(ctx, r.Stdin(), payload)`.

**The load-bearing modelling decision: the writer resolves its target child at _write_ time,
not at _resolve_ time.** That mirrors production exactly — `RestartFresh` rotates the runner's
internal id and re-spawns *in place*, so the `*streamsup.Runner` identity survives the rotation
while the child behind `Stdin()` does not. A fixture that snapshotted the child at resolve time
would make the assertion vacuous in the wrong direction; this one makes "did the turn reach the
post-rotation child?" a real question about the seam.

Contract (write the bodies in the file; these are the signatures and the one-line behaviours):

- `func (w *rotatingWriter) Activate(ctx context.Context) error` — non-blocking signal on
  `w.activated`, returns nil. Models `Pool.Activate` on an already-live session. **This signal
  is the test's barrier for "the drain has entered the delivery and is now at the hold"** —
  `Activate` is the statement immediately preceding `waitIdleForDelivery`, on the same
  goroutine, so receiving it establishes that ordering by program order.
- `func (w *rotatingWriter) WriteUserTurn(ctx context.Context, convID string, payload []byte) error`
  — snapshot the current child under `w.mu`. Nil child → non-blocking signal on `w.noChild`,
  return `streamsup.ErrNoLiveChild` verbatim. Non-nil → append `string(payload)` to **that
  child's** `stdin`, signal on `w.wrote`, return nil. It appends **even to a child marked
  dead**: that is the hazard AC1 names ("bytes into the dead child's pipe must not read as
  delivered") and it must be *observable*, not masked behind an error.
- `func (w *rotatingWriter) beginRestart()` — mark the current child dead, set current to nil.
  Models `RestartFresh`'s teardown half, during which `Runner.Stdin()` returns nil.
- `func (w *rotatingWriter) spawn(sessionID string) *fakeChild` — install a fresh live child as
  current and append it to the ordered `children` slice. Models the fresh spawn.
- `func (w *rotatingWriter) stdinOf(sessionID string) []string` and
  `func (w *rotatingWriter) childIDs() []string` — read accessors for the assertions.

`fakeChild` holds `{sessionID string; dead bool; stdin []string}`. Every child ever installed is
retained, so an assertion can name *which* child received the payload rather than only whether
someone did.

### Fixture 2 — `rotatingSessionOwner`: a mutable `conversationForSession`

`stubBusyResolve` takes a static map and cannot express a rotation. Replace it locally with a
tiny mutex-guarded set:

- `func (o *rotatingSessionOwner) resolve(sessionID string) (string, bool)` — returns
  `testConvID` for any owned id. Satisfies `newTurnBusyTracker`'s resolve parameter.
- `func (o *rotatingSessionOwner) rekey(oldID, newID string)` — **adds** `newID` and **keeps**
  `oldID`. That is `RebindSession`: `CurrentSessionID` moves to `newID` and `oldID` is appended
  to `SessionHistory`, and `conversationForSession` matches either. Keeping `oldID` is what
  keeps the child-exit lane's clear (keyed to the *construction-time* id) resolvable after the
  rotation; dropping it would make that lane a silent Debug-level skip.

### The forced sequence (primary test)

`TestInboundDeliver_RotationReleasesHeldTurn_DeliversToFreshChild`

| # | Test-goroutine action | Barrier before proceeding | Models |
|---|---|---|---|
| 1 | `owner.own(oldSID)`; `w.spawn(oldSID)`; build `tr := newTurnBusyTracker(owner.resolve, discardLogger())`; build the queue over `newInboundDeliver(func(string) (handlers.TurnWriter, error) { return w, nil }, tr, streamTurnHoldTimeout)`; `go q.Run(ctx)` | — | daemon at rest: conv bound to `oldSID`, child live |
| 2 | `tr.observe(oldSID, turnevent.TextChunk{...})`; **assert `tr.Busy(testConvID)`** | the assertion itself | M1 — the pre-rotation turn is running. The assert is the non-vacuity guard: without it the rest of the test proves nothing |
| 3 | `q.Enqueue(testConvID, "turn-two")` | `recvStringWithin`-style receive on `w.activated` | ack #2 — the turn is accepted and the drain has passed `resolve` + `Activate` |
| 4 | `assertNoWriteWithin(t, w.wrote, 50*time.Millisecond)` | the grace window | "not ready at the moment the drain attempts": parked in the hold, zero bytes |
| 5 | `owner.rekey(oldSID, newSID)` | — | `rebindConversation` / `RebindSession` |
| 6 | `w.beginRestart()` | — | `RestartFresh`'s teardown: old child dead, `Stdin()` nil |
| 7 | `tr.clearForSession(newSID)` | receive on `w.noChild` | the transition observer's clear — **keyed to the NEW id**, resolvable only because step 5 ran first. The parked delivery wakes here and finds no live child |
| 8 | `w.spawn(newSID)` | receive on `w.wrote` | the fresh child binds stdin; msgqueue's retry re-attempts the same head |
| 9 | assert delivery | — | see below |

Final assertions:

- the post-rotation child's stdin contains exactly `["turn-two"]`;
- **no other child's stdin contains `"turn-two"`** — iterate `w.children`, not just the
  pre-rotation one, so a future third child cannot hide a misroute;
- `tr.Busy(testConvID)` is true (the successful write opened the next turn — `openForDelivery`
  precedes the write and was not undone).

**Why steps 6 and 7 are in that order, and the honest cost.** Production runs them the other
way: `rotate()` fires the clear and returns, and only *then* does `startFreshRunner` call
`RestartFresh` (`main.go:1447-1478`). Left as production has it, whether the woken delivery
finds the doomed child still live or already nil is a **race** — and AC2 forbids deciding this
by luck. Pulling the teardown ahead of the clear removes the race while keeping the property
under test (the clear is still the sole release, and the fresh child still arrives strictly
after the wake). The consequence — that a parked turn can, in production, wake into a
still-live doomed child, write successfully, and be committed-and-dropped by msgqueue — is a
**distinct window this test deliberately does not decide.** It is recorded in § Open questions
as a follow-up candidate, and the recorded M4 failure argues against it being the M4 stall:
that path writes bytes, and the e2e's shared stdin log shows none.

### Control test — the fixture can deliver into the wrong child

`TestInboundDeliver_RotationWithoutHold_WritesIntoThePreRotationChild`

The same fixture and the same steps 1–3, but the seam is built with a **nil** tracker (PTY
mode, where the hold is a documented no-op). The delivery does not park; it writes immediately
into the still-live pre-rotation child. Assert exactly that.

This is not decoration. It makes the primary test's "no other child holds the payload"
assertion non-vacuous **permanently**, not only at mutation time: it proves the fixture has a
reachable path into the pre-rotation child, so the primary test's silence about that child is a
result, not a structural impossibility.

### `t.Parallel()` is deliberately omitted

Every other test in `inbound_deliver_test.go` calls it. These two must not, and the doc comment
must say why: AC2 is a **determinism** claim measured under `-count=50 -race`, and running 50
race-instrumented copies concurrently reintroduces machine load as a confound on the
`recvStringWithin` budgets. Serialised, the whole 50-iteration loop is ~5 s and every wait has
orders of magnitude of headroom.

### The `hold` value, and why

**Pass the production `streamTurnHoldTimeout` (15 min) verbatim.** The ticket flags this as a
verdict-changing knob, and the reasoning is:

- A **short** hold converts a stuck-busy defect from "silent park" into "error → 1 Hz retry →
  give-up at 2 min". Both fail a "the turn is delivered" assertion, but the second is a
  *different failure signature from the one on record* — the recorded M4 failure is silent.
  Asserting against a signature the field has never produced would weaken the whole exercise.
- The test never needs the bound to expire. The rotation's clear is the release; if the clear
  fails to release, the test fails on its own `recvStringWithin` budget (2 s) long before the
  production bound is reached. So the production value costs nothing in wall-clock and buys a
  faithful signature.

Set `msgqueue.Config.RetryInterval` to `10 * time.Millisecond` (the value the existing
never-ending-turn test uses) so step 8's retry is prompt. Leave `GiveUpAfter` at its default —
the test must never reach it, and a give-up firing would itself be a finding.

---

## Concurrency model

Three goroutines, no shared mutable state outside the two mutex-guarded fixtures.

| Goroutine | Owns | Synchronisation |
|---|---|---|
| test goroutine | the sequence: tracker feeds, `Enqueue`, rotation steps | channel receives on `w.activated` / `w.noChild` / `w.wrote` |
| `q.Run`'s per-conversation drain | one delivery attempt at a time through the real seam | `waitIdleForDelivery` blocks on the tracker's generation channel; unblocked only by `setBusy` moving membership |
| — | `rotatingWriter`, `rotatingSessionOwner` | plain `sync.Mutex`; leaf locks, never held across a channel op |

All fixture signal channels are **buffered** (cap 8) and sent to **non-blockingly**, mirroring
`exitFor`/`sinkFor`'s discipline: the fixture runs on the drain goroutine and must never wedge
it, and a dropped duplicate signal costs the test nothing (each barrier receives once).

**Where the determinism comes from**, stated so a reviewer can check it rather than trust it:

1. The busy mark (step 2) happens-before `Enqueue` (step 3) in test-goroutine program order.
2. `waitIdleForDelivery` re-checks membership and captures the generation channel under one
   lock acquisition, so it cannot miss a wakeup — and it *cannot return* while the mark stands.
3. The only `setBusy(conv, false)` in the whole test is step 7's `clearForSession`. There is no
   `TurnEnd`, no second tracker feed, and no eviction. So the release is unique and named.
4. `Activate` immediately precedes the hold on the drain goroutine, so receiving `w.activated`
   proves the drain is at the hold — no sleep required to "let it get there".

`ctx` is a `context.WithCancel(context.Background())` cancelled by `defer`; assert `q.Run`
returns `context.Canceled` at the end, as the existing tests do.

---

## Error handling

| Failure mode | Surface | Handling |
|---|---|---|
| the clear does not release the hold (the defect being hunted) | step 8's `recvStringWithin` on `w.wrote` times out at 2 s | `t.Fatalf` naming the arm: "the delivery is still parked in `waitIdleForDelivery` 2 s after the rotation's clear" |
| `clearForSession` resolves to no conversation | same timeout, but the cause is a fixture bug (step 5 omitted or `rekey` keyed wrong) | the failure message must name **both** possibilities; the production skip is Debug-level and invisible, so the test message is the only diagnostic |
| the write lands in the pre-rotation child | the "no other child" assertion | `t.Errorf` printing every child's `sessionID`, `dead` flag and stdin contents |
| msgqueue gives up | `OnGiveUp` fires | wire an `OnGiveUp` that `t.Errorf`s — reaching give-up means the retry ladder ran ~2 min of attempts and is itself a finding, not a timeout |
| `q.Run` returns early | `recvErrWithin` on the run channel | assert `context.Canceled` |

Every `t.Fatalf` message must name the *arm* and the *step*, not just "timed out" — this file's
whole purpose is to make a silent stall legible.

---

## Testing strategy

### AC2 — forced, not sampled

```bash
go test -race -count=50 -v \
  -run '^TestInboundDeliver_RotationReleasesHeldTurn_DeliversToFreshChild$' ./cmd/pyry/ \
  2>&1 | tee /tmp/1295-count50.txt
grep -c '^--- PASS' /tmp/1295-count50.txt   # expect 50
grep -c '^--- FAIL' /tmp/1295-count50.txt   # expect 0
tail -3 /tmp/1295-count50.txt               # expect: ok  github.com/pyrycode/pyrycode/cmd/pyry
```

**Both counts and the `tail` must be pasted verbatim** into the PR/issue. A `grep -c` that
returns `50` is only trustworthy alongside the `ok`/`FAIL` line — a build failure produces
`0`/`0` and reads as "no failures". Run the control test the same way.

### AC3 — the verdict is demonstrated

Run both mutations via `go test -overlay`, which executes a mutated **copy** from the scratchpad
and leaves the worktree byte-clean — a stronger guarantee than edit-then-revert, and it makes
AC5's "no production code modified" checkable by `git status` at any moment.

```bash
S=$(mktemp -d); cp cmd/pyry/main.go "$S/main_mut.go"
# …apply one mutation to $S/main_mut.go (see below)…
printf '{"Replace":{"%s/cmd/pyry/main.go":"%s"}}' "$PWD" "$S/main_mut.go" > "$S/overlay.json"
go test -race -overlay="$S/overlay.json" -v \
  -run '^TestInboundDeliver_RotationReleasesHeldTurn_DeliversToFreshChild$' ./cmd/pyry/
```

**M1 — misroute (removes the hold).** Delete the `waitIdleForDelivery` block from
`newInboundDeliver`. The delivery no longer parks; it writes at once into the still-live
pre-rotation child. Expected RED on the "no other child holds the payload" assertion. This is
the mutation that proves the test forces the **hold arm** specifically.

**M2 — drop (removes the write).** Replace the `WriteUserTurn` call and its error branch with
`return nil`, and silence the now-unused `undo` (`_ = busy.openForDelivery(convID)`). Expected
RED on "the post-rotation child received the turn". This proves the delivered-assertion is not
satisfiable by inaction.

Two rules for the mutation script, both learned the hard way:

- **Assert every textual substitution changed something** (`assert s.count(old) == 1` before
  `s.replace(...)`). A `.replace()` that matched nothing produces an unmutated binary that
  reports GREEN, which reads as "the test is weak" when it means "the mutation never applied".
- **Confirm each RED is an assertion failure, not a build failure.** Paste the `--- FAIL` block
  showing the assertion message. A mutated file that does not compile also "turns the test red"
  and proves nothing.

Revert is a no-op (nothing was written); `git status` must be clean apart from the new test file
and this spec.

### AC5 — no production code modified

```bash
git diff --name-only origin/main...HEAD
```
Every line must end in `_test.go` or start with `docs/`. Paste the output.

### Package regression

`go test -race ./cmd/pyry/` and `make check` must both stay green.

---

## Recording the outcome (AC4)

Both branches write to **this spec file** (`docs/specs/architecture/1295-…md`), appended as a
`## Outcome` section, plus a comment on the issue carrying the verbatim command output.
`docs/knowledge/codebase/1295.md` is **not** a deliverable of this ticket — the documentation
phase writes it from this spec plus the merged diff.

**If green** (the expected branch): the test merges as a permanent guard. The `## Outcome`
section records:

- **Eliminated:** "the rotation's `clearForSession` fails to release a delivery parked in
  `waitIdleForDelivery`, when the pre-rotation turn's events and the transition arrive in
  order" — forced 50/50, mutation-demonstrated.
- **Still open**, in the order this investigation ranks them:
  1. **A straggler pre-rotation event re-marks the conversation after the clear.** The tracker's
     opener feed is asynchronous (child → parser → sink → drain → `observe`). A `TextChunk`
     still buffered in the fan-in when the transition clear fires lands *after* it, and
     `oldSID` still resolves via `SessionHistory` — so it re-marks the conversation busy with a
     mark **no future event can clear**: the turn it belongs to is dead, its `TurnEnd` will
     never come, and both session-keyed clears have already fired. Result: a 15-minute silent
     park. This matches every feature of the M4 record. Deciding it needs the drain's fan-in
     (`startStreamTurnDrainV2` + `streamTurnSink` + tracker), not the seam alone.
  2. **A dropped exit envelope.** `exitFor` sends non-blockingly and drops on a full sink with
     `stream_turn.exit_sink_full` (Warn). A drop removes the second clear keyed to the
     construction-time id, widening (1)'s window. #1296's log capture will show the Warn if it
     fired.
  3. **The persistently-failing error arm** — not separable from a park by byte count alone;
     that is #1296's job (1 Hz `msgqueue: delivery failed, will retry` Warn vs. silence).
  4. **The clear-before-`RestartFresh` window** (§ Design) — a parked turn waking into a
     still-live doomed child. Argued *against* by the record (that path writes bytes; the shared
     stdin log showed none), but not structurally excluded.

**If red:** do **not** merge the test and do **not** attempt a fix. Record the exact ordering,
the command and its verbatim output in `## Outcome`; run the control demanded by AC3's red
branch (remove steps 5–8's rotation and show the same test passes, pinning the reproduction to
the ordering rather than to the fixture); file a follow-up ticket carrying the reproduction and
move the test to it. The PR then ships the write-up only.

---

## Open questions

1. **Which session id should the fixture's clear use?** The spec says `newSID`, matching
   `transitionClearsTurn`'s documented `NewSessionID` choice. If the developer finds that
   passing `oldSID` also releases (it should, via the `SessionHistory` match), that is worth one
   line in `## Outcome` — it is the redundancy `transitionClearsTurn`'s doc calls conditional on
   `notifyTransition` keeping the rebind ahead of the fan-out.
2. **Should the primary test also cover eviction?** `transitionClearsTurn` maps
   `ReasonEviction` to the mirrored previous id. Out of scope here — this ticket is about
   `new_session` — but if the fixture makes it a five-line variant, note it rather than write it.
3. **Straggler re-mark (Outcome item 1) as its own ticket.** It is the strongest surviving
   hypothesis and it is not forceable at this seam. It wants its own ticket sized against the
   drain's fan-in. File it from the write-up, not from this diff.
4. **`#1133`** (rotate the turn-event Parser tag on `RestartFresh`) is the same class of
   per-runner staleness and remains open. Related, not blocking.

---

## Scope guard

- One new file: `cmd/pyry/inbound_deliver_rotation_test.go`.
- Zero production files. Zero new exported identifiers.
- Do **not** touch `internal/e2e/relay_v2_stream_new_session_test.go` — #1296 owns it, including
  the stale "no periodic retry, so a missed wake-up is an unbounded stall" comment at ~:388-397.
- Do **not** raise a deadline, add a retry/re-run wrapper, or skip the e2e test (#1273's pin:
  auto-retry converts a possible permanent-stall defect into an invisible one).
- Do **not** fix anything, in either branch of AC4.
