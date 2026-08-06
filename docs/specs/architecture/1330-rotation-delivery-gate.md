# #1330 — Order inbound delivery against a `new_session` rotation's respawn

**Size: S.** 3 production files, ~95 lines of production Go, ~310 lines of test. Zero
call-site cascade (every production change is additive except one statement inside
`(*streamsup.Runner).WriteUserTurn`, whose signature is unchanged). Nearest analogue
#1209 measured 121 production + 248 test and shipped as one ticket
(`git show 3ef6f81 --stat`).

---

## Files to read first

Reading list, in order. Each entry says what to extract; do not read past it.

| Path | What to extract |
|---|---|
| `internal/streamsup/runner.go:154-211` | The three-mutex split. `mu` guards `stdin` ONLY; `restartMu` guards `args`/`iterCancel`/`sessionID`/`rotatePending`; `stateMu` guards `state`. The new gate goes under `mu`, and § Concurrency explains why it cannot go under `restartMu`. |
| `internal/streamsup/runner.go:257-285` | `Stdin()` and `WriteUserTurn`. `WriteUserTurn` is a one-line delegation `WriteTurn(ctx, r.Stdin(), payload)` — that line is the ONLY production statement this ticket rewrites. |
| `internal/streamsup/runner.go:351-410` | `RestartFresh` + `nextSpawnID`. Note that `RestartFresh` returns after `cancel()`, and the child dies asynchronously on the Run goroutine — the reason "release the gate when `RestartFresh` returns" is NOT sufficient. |
| `internal/streamsup/runner.go:588-628` | `spawnAndWait`'s `setStdin` (:588, immediately after `cmd.Start`) / `takeStdin` (:604) pair, and the two helpers at :613-628. `setStdin` is the gate's close point; `takeStdin` deliberately is not. |
| `internal/streamsup/envelope.go:139-152` | `WriteTurn`'s nil-writer contract: a nil `w` yields `ErrNoLiveChild` and writes nothing, and the nil check PRECEDES the turncommit claim. This is why the gate can be expressed as "hand `WriteTurn` a nil writer" and reuse an already-reviewed refusal path verbatim. |
| `cmd/pyry/main.go:1447-1477` | `startFreshRunner` — the dispatch this ticket edits, and its doc's load-bearing ordering claim (rotate-before-`RestartFresh` primes the watcher skip-set). That ordering is PRESERVED; the arm is inserted ahead of both. |
| `cmd/pyry/main.go:1586-1653` | `newInboundDeliver` — resolve → `Activate` → `waitIdleForDelivery` → `openForDelivery` → `WriteUserTurn`. Read the PLACEMENT paragraph. **This function is not modified.** |
| `cmd/pyry/streamsup_runner.go:14-54` | `streamRunner`, the `*streamsup.Runner` → `sessions.Runner` adapter, and the `Interrupt`/`RestartFresh` precedent for a concrete method reached by type assertion off the un-widened interface. The new forwarder is the third such method. |
| `cmd/pyry/inbound_deliver_rotation_test.go:61-259` | The `fakeChild` / `rotatingWriter` / `rotatingSessionOwner` fixture. Reuse it. Read :84-106 especially — the "resolves its target child at WRITE time" decision, and the "no refuse-to-write branch" decision this ticket must revisit. |
| `cmd/pyry/inbound_deliver_rotation_test.go:261-403` | `TestInboundDeliver_RotationReleasesHeldTurn_DeliversToFreshChild` — the 9-step shape the new test mirrors, and :281-293, the paragraph that names this ticket's window as deliberately undecided. |
| `cmd/pyry/inbound_deliver_rotation_test.go:405-452` | `TestInboundDeliver_RotationWithoutHold_WritesIntoThePreRotationChild`, the fixture-reachability control. It must stay green; § Testing says why it does. |
| `cmd/pyry/new_session_routing_test.go:12-46` | `restartFreshStub` / `startNewSessionStub` / `bothMethodsStub`. None of them will grow a `BeginRotation` method; § Design explains why the dispatch uses an OPTIONAL assertion so these stay green. |
| `internal/streamsup/runner_test.go:649-740` | `TestRunner_RestartFresh_RotatesThenResumesNewID` — the `helperRunCfg` / `onSpawn` / `spawnArgsRecorder` / `runInBackground` harness the streamsup-level tests reuse. `onSpawn` fires after `setStdin`, which is the hook the new tests need. |
| `internal/msgqueue/queue.go:120-160, :580-615` | `PendingFunc` and the two drain arms. `ErrNoLiveChild` matches no `Pending` classifier, so it takes the ERROR-RETURN arm: 1 Hz retry of the same head, give-up at 2 m. That is the retry ladder this fix relies on. |
| `internal/e2e/relay_v2_stream_new_session_test.go:62-72, :402-546` | The sink-tag divergence, the deadline argument (do not touch it), and the AC-1 guard at :496-546. **This file is not modified by this ticket.** |
| `docs/specs/architecture/1295-forced-rotation-delivery-ordering.md:518` | Open question 4, "the clear-before-`RestartFresh` window … not structurally excluded". This spec closes it. |

---

## Context

`startFreshRunner` (`cmd/pyry/main.go:1462`) runs the rotation in two statements:

```
newID, err := rotate(oldID)      // Pool.RotateForNewSession: re-key, rebind, ReasonClear fan-out
v.RestartFresh(string(newID))    // rotate the runner id, then kill; respawn is asynchronous
```

Between them the outgoing child is **alive** and the conversation is **idle** — the
`ReasonClear` fan-out reached `turnBusyTracker.clearForSession` and released whatever
was held. A `send_message` accepted in that window resolves, passes
`waitIdleForDelivery` immediately, and writes into the child `RestartFresh` is about to
kill. `msgqueue` reads the successful write as a commit and drops the head, so the turn
is gone: the fresh child never receives it and never echoes it. The captured window is
~4 ms wide (`34.169` enqueue → `34.173` kill in the ticket's record).

The window does not end when `RestartFresh` returns. `RestartFresh` publishes the new id
and calls `cancel()`; the kill, `cmd.Wait`, and `takeStdin` all run later on the Run
goroutine. So any fix keyed to "`RestartFresh` has returned" narrows the race from
milliseconds to microseconds without excluding it — and AC-1 asks for exclusion, not
narrowing.

The e2e's ~6.1 s AC-1-guard red is this defect's detector, via the contrapositive the
guard's own determinism argument establishes (`:513-522`): M4 green with zero
`level=DEBUG` records means the fresh child never echoed turn #2, so M4's needle came
from the **outgoing** child through the shared stdin tee. The ~21 s M4 stall is a
different defect (#1298, silent park on a stale busy mark) and is **out of scope** —
see § Out of scope.

---

## Design

One gate, inside `*streamsup.Runner`, guarded by the same mutex that guards `stdin`.

### The invariant

> From the moment a rotation is armed until the next child's stdin is bound,
> `(*streamsup.Runner).WriteUserTurn` writes nothing and returns `ErrNoLiveChild`.

Armed strictly before `rotate()`; cleared by `setStdin`, i.e. by the fresh child
binding. Nothing in between opens it.

### Why the gate lives under `mu` and not `restartMu`

`WriteUserTurn` must answer "is a rotation in flight?" and "which child's stdin?" as one
question. Two lock acquisitions reintroduce the defect at nanosecond width: read the
flag (false) → a rotation arms → read `stdin` → write into the doomed child. Putting the
flag under `mu` — the mutex `stdin` already lives under — makes the check-and-capture a
single acquisition, so the answer is a snapshot of one consistent state and the race is
structurally excluded rather than narrowed.

The widening of `mu`'s documented charter is honest: it stops guarding "the stdin
handle" and starts guarding "which child, if any, may receive a turn". `mu` remains a
leaf lock, never held across a call-out. Update its doc comment (:161-165) to say so.

### `internal/streamsup/runner.go`

Two new fields on `Runner`, documented beside `stdin` under `mu`:

- `rotating bool` — a rotation is armed and no successor child has bound yet.
- `rotateGen uint64` — stamps each arm so a stale abort cannot disarm a later one.

Three method-level changes:

```go
// BeginRotation arms the rotation gate and returns the disarm for the caller's
// error path. Safe from any goroutine; touches only r.mu.
func (r *Runner) BeginRotation() (abort func())

// turnTarget reports the writer a turn may be written to — nil while a rotation
// is armed or no child is live — together with whether the rotation gate is what
// refused, under ONE r.mu acquisition.
func (r *Runner) turnTarget() (w io.Writer, gated bool)
```

- `BeginRotation` sets `rotating = true`, increments `rotateGen`, captures the value,
  and returns a closure that clears `rotating` only if `rotateGen` still equals the
  captured value. Modelled on `openForDelivery`'s undo
  (`cmd/pyry/stream_turn_busy.go:398-431`), which is likewise live only if this call
  actually placed the mark.

  **The generation stamp is load-bearing, not defensive padding, and the sequence that
  needs it is self-triggering.** Two `new_session` frames overlap (the e2e's own M2 loop
  re-sends every ~250 ms, so this is a shape the repo already produces):

  1. Frame 1 arms the gate, then calls `rotate(oldID)`.
  2. Frame 2 arms the gate and its `rotate(oldID)` wins, re-keying `oldID → newID`.
  3. Frame 1's `rotate` now fails `ErrSessionNotFound` — `RotateForNewSession` looks up
     `oldID` under `p.mu` and it is gone (`transition.go:120-123`). This is not a rare
     error; it is the *ordinary* outcome of losing that race, and its doc names the
     TOCTOU explicitly.
  4. Frame 1 runs its `abort()`.

  An unstamped `abort` clears the arm frame 2 is holding — while frame 2's outgoing
  child is still alive, because frame 2 has not reached `RestartFresh` yet. That
  reproduces the exact defect this ticket closes, on demand, from two frames. A
  developer who "simplifies away the counter" reopens it silently: no test that drives
  one rotation at a time can see it, which is why § Testing pins it.
- `turnTarget` returns `(nil, true)` while `rotating`, `(nil, false)` when `stdin` is
  nil, and `(r.stdin, false)` otherwise.
- `WriteUserTurn` becomes: call `turnTarget`; if `gated`, emit one `Debug` record
  **outside the lock** naming the refusal; then `return WriteTurn(ctx, w, payload)`
  unchanged. A nil `w` reaches `WriteTurn`'s existing nil branch, which returns
  `ErrNoLiveChild` without writing and without consuming the turncommit claim
  (`envelope.go:139-152`). No new error value, no new envelope construction, and
  `envelope.go` is not touched.
- `setStdin` clears `rotating`. `takeStdin` deliberately does not: the gate must survive
  the teardown, which is the whole interval it exists for.

`Stdin()` keeps today's semantics, so `Interrupt` and the roundtrip/runner tests are
unaffected. Interrupting during the ~ms rotation window is out of scope: it writes a
control line, not a turn, and no observed failure implicates it.

**Why refuse rather than block.** Blocking inside `WriteUserTurn` until the fresh child
binds would need a new wait/wakeup channel in the runner and would hold the drain
goroutine across a respawn. Refusing reuses the retry ladder that already exists and
already handles the immediately-following moment — between the kill and the fresh
child's `setStdin`, `Stdin()` is nil and `ErrNoLiveChild` is what production already
returns. The gate simply extends that same refusal backwards over the window where the
handle is non-nil but doomed. Cost: the turn lands up to one `defaultRetryInterval`
(1 s) later, against a give-up bound of 2 m.

### `cmd/pyry/streamsup_runner.go`

One forwarder, `func (a streamRunner) BeginRotation() func()`, documented as the third
concrete method reached by type assertion off the un-widened `sessions.Runner`
interface, after `Interrupt` (#1120) and `RestartFresh` (#1124).

### `cmd/pyry/main.go`

`startFreshRunner`'s `RestartFresh` arm gains an arm-then-rotate-then-restart sequence.
The existing rotate-before-`RestartFresh` order is **unchanged** — the arm is inserted
ahead of both, so the skip-set registration still precedes the spawn:

```go
case interface{ RestartFresh(string) }:
    abort := beginRotationOrNoop(r)
    newID, err := rotate(oldID)
    if err != nil {
        abort()
        return err
    }
    v.RestartFresh(string(newID))
    return nil
```

plus a small helper:

```go
// beginRotationOrNoop arms r's rotation gate when the runner has one and returns
// the disarm; a runner without the gate returns an inert disarm, leaving the
// dispatch shape unchanged.
func beginRotationOrNoop(r sessions.Runner) (abort func())
```

**Why an optional assertion instead of widening the type switch's case.** Widening the
case to `interface{ RestartFresh(string); BeginRotation() func() }` would silently
re-route `restartFreshStub` and `bothMethodsStub` (`new_session_routing_test.go:12-46`)
to the `default` inert arm, turning four existing subtests from assertions into
vacuities without a single failure. It would also change the documented
"`RestartFresh` is matched first" dispatch contract. The optional assertion leaves the
dispatch shape byte-identical and treats the gate as a capability, exactly as `Interrupt`
and `RestartFresh` are treated one layer up.

The PTY arm (`StartNewSession`) is untouched: `/clear` is a keystroke into a child that
keeps running, there is no teardown, and `supervisor.Supervisor` has no gate.

### Data flow across the window, after the fix

```
StartNewSession
  └─ startFreshRunner
       ├─ BeginRotation()                    ── gate ARMED ─────────────────┐
       ├─ rotate(oldID)                                                     │
       │    ├─ re-key pool + register skip-set + saveLocked                 │  every
       │    ├─ rebindConversation(old → new)                                │  WriteUserTurn
       │    └─ ReasonClear fan-out → clearForSession(newID) → tracker idle  │  in here
       └─ RestartFresh(newID)                                               │  returns
            ├─ sessionID = newID; rotatePending = true                      │  ErrNoLiveChild
            └─ cancel()  ─────────────────────────────────────────────┐     │
                                                                      │     │
Run goroutine: cmd.Wait returns → takeStdin  ─────────────────────────┘     │
Run goroutine: nextSpawnID → spawnAndWait → cmd.Start → setStdin  ── gate CLEARED ┘
                                                        (fresh child bound)

msgqueue drain: attempt N fails ErrNoLiveChild → 1 s → attempt N+1 → … → write lands
                                                                     in the fresh child
```

---

## Concurrency model

No new goroutines. Three participants touch the gate:

| Goroutine | Calls | Lock |
|---|---|---|
| relay handler (`handleNewSession` → `StartNewSession`) | `BeginRotation`, `abort` | `r.mu` |
| msgqueue per-conversation drain | `turnTarget` (via `WriteUserTurn`) | `r.mu` |
| runner `Run` | `setStdin` (clears), `takeStdin` (does not) | `r.mu` |

`r.mu` stays a leaf: `BeginRotation`, `abort`, and `turnTarget` take it, mutate or read
plain fields, and release it before anything else happens. Nothing is held across a
channel operation, a log call, or a call-out. `BeginRotation` therefore inherits
`RestartFresh`'s stated property — "touches only Runner-internal state, never a Pool
lock" — so the `sessions` layer's lock-order note (`runner.go:174-183`) still holds and
`startFreshRunner` may call it with no Pool lock held, which it does.

The refusal `Debug` record is emitted by `WriteUserTurn` after `turnTarget` has
released `r.mu`, so a slow handler cannot block the Run goroutine's `setStdin`.

Shutdown: none of the three participants outlives its existing owner, and the gate holds
no resource. If `Run` has returned (daemon teardown) the gate stays armed and deliveries
refuse — identical to today's behaviour when `stdin` is permanently nil, and it resolves
the same way, via msgqueue's 2 m give-up into a typed `session_error`. No new wedge
class.

---

## Error handling

| Failure | Behaviour | Why |
|---|---|---|
| Turn arrives while the gate is armed | `WriteUserTurn` returns `ErrNoLiveChild`, zero bytes written, turncommit claim not consumed | `WriteTurn`'s nil branch precedes the claim (`envelope.go:149-152`); msgqueue retries the same head at 1 Hz |
| `rotate()` fails after the arm (`ErrSessionNotFound`, mint failure) | `abort()` disarms, error propagates unchanged to `handleNewSession`, which Warn-logs and tolerates | Without the disarm the conversation refuses every turn until the next respawn — a wedge the rotation never earned |
| Two `new_session` frames overlap; the loser's `rotate` fails `ErrSessionNotFound` | The second `BeginRotation` bumped the generation, so the loser's `abort` is a no-op and the winner's gate stays armed | Losing the race IS how `rotate` fails here (`transition.go:120-123`); an unstamped abort would hand the defect back on demand — see § Design |
| Gate armed, runner already dead | Deliveries refuse until msgqueue gives up at 2 m → typed `session_error` | Same outcome as today's permanently-nil `Stdin()`; no new class |
| PTY runner | `beginRotationOrNoop` returns an inert disarm; nothing changes | `/clear` is a keystroke; the child is not torn down |
| `RestartFresh("")` (empty id guard) | Unreachable from this path — `rotate()` returns a minted id or an error, and the error path aborts before `RestartFresh` | Existing guard at `runner.go:369-372` unchanged |

The refusal is deliberately **not** given a new sentinel. `ErrNoLiveChild` is already the
retryable classification `msgqueue` and `cmd/pyry` agree on, and the e2e's own failure
message asserts as a contract check that stream `WriteUserTurn` returns "only
`ErrNoLiveChild`, `turncommit.ErrDropped` or nil"
(`relay_v2_stream_new_session_test.go:480`). A new sentinel would break that stated
contract and require classification wiring in two more files for no behavioural gain.
The discriminator an operator needs — "refused by the rotation gate" vs "no child yet" —
is carried by the `Debug` record instead.

---

## Testing strategy

Scenarios, not test bodies. Write them in the surrounding files' idiom.

### A. `cmd/pyry/inbound_deliver_rotation_test.go` — the AC-2 test (primary proof)

A third test, `TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild` (name
to taste), distinct from both existing tests. It drives the **real**
`startFreshRunner` — that is what makes it RED on `ac25ad8`, because the unmodified
`startFreshRunner` never arms the gate.

Fixture growth on `rotatingWriter`, all of it modelling production rather than asserting
it:

- A gate: `BeginRotation() func()` arming a `rotating` flag under the existing `mu`,
  with the same generation-stamped disarm, and `spawn` clearing it — the fixture twin of
  `setStdin`.
- `WriteUserTurn` consults the gate under the same `mu` acquisition as `current`, and
  returns `streamsup.ErrNoLiveChild` when gated, signalling on the existing `noChild`
  channel. This is the arm the ticket's Technical Notes flag: the type doc at :98-101
  ("deliberately NO refuse-to-write branch") must be **amended, not deleted** — the
  original reasoning still governs the *dead-child* case (refusing there would mask a
  misroute), while the gated case is a refusal production now genuinely makes.
- A `sessions.Runner`-shaped wrapper so `startFreshRunner` can dispatch to it, exposing
  `RestartFresh(string)` and `BeginRotation() func()`. `RestartFresh` performs the
  fixture's `beginRestart()` (teardown) — modelling that the kill follows the arm.

Steps, in **production order**:

1. Owner bound to `rotationOldSID`; writer with one live child; tracker; queue with
   `newInboundDeliver(resolve → writer, tracker, streamTurnHoldTimeout)` and
   `RetryInterval: 10ms`; `OnGiveUp` recorded as a finding, as the existing test does.
2. Open a pre-rotation turn via `tr.observe(...)`; assert `tr.Busy(testConvID)` — the
   non-vacuity guard.
3. `q.Enqueue(testConvID, "turn-two")`; receive `w.activated` — the drain is at the hold
   by program order, no sleep.
4. `assertNoWriteWithin(t, w.wrote, 50ms)` — the hold engaged.
5. Call the real `startFreshRunner(runnerFake, rotationOldSID, rotate)` where `rotate`
   is a closure that performs, **in production order**: `owner.rekey(old, new)` then
   `tr.clearForSession(rotationNewSID)`, then returns `rotationNewSID`. This is the
   whole point: the clear fires while the outgoing child is still live, exactly as
   `Pool.RotateForNewSession`'s fan-out does, and `startFreshRunner` — not the test —
   decides whether a gate was armed first.
6. Receive `w.noChild` — the woken delivery attempted a write and was refused. On the
   unmodified tree this receive times out because the write SUCCEEDED into the outgoing
   child; the failure message must say so and print `w.snapshot()`.
7. `w.spawn(rotationNewSID)` (models the fresh `setStdin`, clearing the fixture gate);
   receive `w.wrote` and assert it equals `rotationNewSID`.
8. Assert `w.stdinOf(rotationNewSID) == ["turn-two"]`, and iterate **every** child in
   `w.snapshot()` asserting no other holds `"turn-two"` — the misroute assertion, in the
   shape the existing test uses.
9. Assert `tr.Busy(testConvID)` (the pre-write mark stands) and that `OnGiveUp` never
   fired.

Do not call `t.Parallel()`, matching this file's stated reason (:54-59).

**Control:** `TestInboundDeliver_RotationWithoutHold_WritesIntoThePreRotationChild` must
stay green and must not be edited. No rotation is ever begun in it, so the fixture gate
is never armed and the unheld write still lands in the pre-rotation child. **Run it and
report the result** — the Technical Notes say check, don't assume.

**AC-3 demonstration:** run the new test on the unmodified tree
(`git stash` the production files, or `git worktree add` at `ac25ad8`), capture the RED
output, then re-run with the change and capture the green. Both outputs go in the PR
body.

### B. `internal/streamsup/runner_test.go` — the gate's own layer

The `cmd/pyry` test cannot prove the single-acquisition check-and-capture, because it
runs against the fixture's gate. Three scenarios, reusing `helperRunCfg` / `onSpawn` /
`spawnArgsRecorder` / `runInBackground`:

- **Gate refuses while a child is live.** Spawn (`echo_lines`), wait for `onSpawn`,
  assert `Stdin() != nil`, call `BeginRotation()`, then assert `WriteUserTurn` returns
  `ErrNoLiveChild` — with `Stdin()` still non-nil at that moment, which is the
  non-vacuity guard distinguishing this from `TestRunner_WriteUserTurn_NoLiveChild`
  (`interface_test.go:34-46`).
- **The next spawn clears it.** From the armed state, `RestartFresh(rotatedSessionID)`,
  wait for the second `onSpawn`, then assert `WriteUserTurn` returns nil.
- **`abort` is generation-stamped.** Arm twice, run the first arm's `abort`, assert
  `WriteUserTurn` still returns `ErrNoLiveChild`; then run the second `abort` and assert
  it returns nil. This one needs no child spawn if it asserts through `turnTarget`'s
  `gated` return directly (same-package test), which is the cheaper shape.

### C. Gates

`go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test -race ./...`, plus
`go test -race -count=50 -run '^TestInboundDeliver_' ./cmd/pyry/` — the determinism
budget the existing rotation tests are written against.

### D. AC-4 — the 20-run e2e sample

```
go test -race -tags e2e -count=1 -run '^TestRelayV2_StreamNewSessionRotatesAndRestartsFresh$' ./internal/e2e/
```

20 consecutive runs. **Zero** may fail at the AC-1 guard (`:538`, ~6.1 s). Baseline to
beat: 4 in 34. Record every run's outcome (green / AC-1 red / M4 red) with its wall
clock in the PR body — a bare "20 green" is not the record this ticket asked for.

### E. AC-5 — what must NOT change

`internal/e2e/relay_v2_stream_new_session_test.go` is **not modified**. Do not raise the
20 s M4 deadline, do not add a re-run wrapper, do not add a test-level retry. If a ~21 s
M4 stall (`:461`) appears in the 20-run sample, report it **on #1298** with the record's
stdin byte-pair, retry-Warn count and pending/holds count verbatim, and do not remedy it
here.

---

## Out of scope

- **The ~21 s M4 stall (#1298).** Its record — `99 bytes → 99 bytes, no growth`, `retry
  Warns: 0`, empty daemon-log window — is a silent park on a standing busy mark, not a
  failed write. Ordering delivery against the respawn does not clear a stale mark, so
  this fix is not expected to move that rate. AC-4 is scoped to the AC-1 guard for
  exactly this reason.
- **#1331 (per-child stdin tee attribution).** It would turn the AC-1 guard from an
  indirect detector into a direct M4 assertion. Not a prerequisite; the AC-2 unit test
  is the primary proof.
- **The drain's post-rotation sink-tag divergence** (`relay_v2_stream_new_session_test.go:62-72`).
  Pre-existing, separately flagged, and load-bearing for the AC-1 guard's determinism
  argument — fixing it here would invalidate the measurement AC-4 asks for.
- **`Interrupt` during the rotation window.** Writes a control line, not a turn; no
  observed failure implicates it.
- **`docs/knowledge/codebase/1330.md`.** Owned by the documentation phase, written from
  this spec plus the merged diff. Not a developer deliverable.

---

## Open questions

1. **Debug or Info for the refusal record?** Specified as `Debug`, matching
   `stream_turn_busy.go`'s skip diagnostics. If the 20-run sample shows the refusal
   firing more than a couple of times per rotation, the count is itself a finding and
   the level deserves revisiting — but not in this ticket.
2. **Should `Interrupt` share the gate?** Deliberately no (see § Out of scope). If a
   future record shows an interrupt landing in a doomed child, `turnTarget` is the
   single place that grows a second caller.
3. **Does the fix change M4's wall-clock?** It should add up to one 1 s retry to turn
   #2's arrival. Against the 20 s deadline that is invisible, but if the sample shows M4
   green times shifting by ~1 s, that is the expected signature and worth noting in the
   PR body rather than investigating.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the new surface consumes no untrusted data.
  `BeginRotation()`, its `abort()` and `turnTarget()` all take **zero arguments**; the
  gate is keyed by runner identity, which is process-local. The only phone-supplied thing
  that reaches this path is the *timing* of a `new_session` frame, and the conversation
  it acts on is resolved server-side (`activeSessionStarter.StartNewSession`,
  `main.go:1501-1511`: `a.currentConv()` → `resolveBound` → the `CurrentSessionID == ""`
  #678 isolation guard) before `startFreshRunner` is reached. Pre-existing and
  **not widened**: a phone can rotate whichever conversation the active-conversation
  cursor points at, which may be another device's — but that frame already kills that
  child via `RestartFresh` today, and arming the gate grants strictly less than the
  rotation it accompanies.
- **[Tokens, secrets, credentials]** Not applicable by design decision, stated rather
  than assumed: the gate is two words of process-local state (`rotating bool`,
  `rotateGen uint64`) that are never persisted, never serialised, never sent, and never
  compared against an attacker-supplied value. No token, key, or credential is created,
  read, stored, rotated, or revoked on this path.
- **[File operations]** Not applicable — the design adds no `os.*` call, no path
  construction, no file mode decision. The one adjacent on-disk write,
  `RotateForNewSession`'s `saveLocked` (`transition.go:126`), is unchanged and, per the
  Concurrency finding below, is **not** executed under the new lock.
- **[Subprocess / external command execution]** SHOULD FIX, addressed in-spec and
  flagged for code-review. The gate sits directly on the path that spawns `claude`, but
  must not influence argv: `rotating`/`rotateGen` live under `r.mu` while argv comes from
  `liveArgs()` + `nextSpawnID()` under `restartMu` (`runner.go:390-410`). The concrete
  hazard is a developer folding `rotating` into the existing `rotatePending` flag as a
  "simplification" — the two look interchangeable. They are not: an aborted rotation
  would leave `rotatePending` set, so the next unrelated crash-respawn spawns
  `--session-id <oldID>` (first-run form, fresh transcript) instead of
  `--resume <oldID>`, silently discarding the conversation's history. The fields MUST
  stay separate; `TestRunner_RestartFresh_EmptyIDIsNoOp` (`runner_test.go:794-818`) does
  not catch this, since it never aborts. Signal/kill handling (`cmd.Cancel`'s descendant
  reap + SIGTERM, `WaitDelay = killGrace`, `runner.go:559-570`) is untouched.
- **[Cryptographic primitives]** Not applicable, stated concretely rather than waved
  through: `rotateGen` is a monotonic generation counter, not a nonce, token, or id. It
  is compared only against a value captured by the same `BeginRotation` call, never
  leaves the process, and must never be logged or exposed. `crypto/rand` vs `math/rand`
  does not arise because nothing here is sampled. Wraparound-collision (a stale abort
  matching after 2^64 arms) is unreachable.
- **[Network & I/O]** No findings, and specifically no new amplification. The refusal
  performs zero I/O and zero allocation — it is strictly cheaper than the write it
  replaces — and rides msgqueue's pre-existing 1 Hz retry / 2 m give-up ladder
  (`msgqueue/queue.go:63-85`) rather than introducing a cadence. No socket, header,
  size cap, or timeout is added or changed. Log volume under a stuck gate is bounded by
  that same give-up: ~120 Debug records, then a typed `session_error`.
- **[Error messages, logs, telemetry]** SHOULD FIX — a live footgun the developer will
  walk straight into. The one new record is the refusal `Debug` in `WriteUserTurn`, and
  `WriteUserTurn`'s signature already carries a `conversationID` parameter that is
  currently unused and sitting right there (`runner.go:280-285`). It MUST NOT be logged:
  conversation ids are treated as sensitive routing keys — resolved daemon-side, stamped
  on the wire, never logged (`stream_turn_busy.go:258-266`,
  `session_transition_v2.go:38-47`) — and `streamsup` logs none today. Payload bytes are
  likewise forbidden. Permitted fields: the event discriminant plus, at most, the
  runner's own session id, which `streamsup` already logs (`runner.go:370`). Nothing
  from this path reaches the phone as a message: the refusal surfaces only as msgqueue's
  give-up → typed `session_error` / `CodeSessionBlocked`, a code without internal state.
- **[Concurrency]** Two findings, both structural.
  1. **The single-`r.mu` check-and-capture in `turnTarget` IS the security property.**
     Splitting the "is a rotation armed?" read from the `stdin` read into two
     acquisitions leaves the original defect present at nanosecond width — a narrowing,
     not the exclusion AC-1 asks for. A later "simplification" that splits them reopens
     it silently, which is why § Design states it as a constraint rather than a style
     note.
  2. **`r.mu` MUST NOT be held across `rotate()` or across the refusal's log call.**
     `BeginRotation` releases before `startFreshRunner` calls `rotate()`, whose
     `saveLocked` performs a full atomic write including fsync; holding `mu` across it
     would block the Run goroutine's `setStdin` for the duration of a disk write, and
     holding it across a slog handler would do the same for an unbounded one. `r.mu`
     stays a leaf, consistent with `runner.go:161-165` and with `RestartFresh`'s stated
     "never touches a Pool lock" property, so `startFreshRunner` may call
     `BeginRotation` off `Pool.mu` — which it does. No goroutine is spawned, so there is
     nothing to leak; if `Run` has already returned, an armed gate resolves through the
     existing 2 m give-up exactly as a permanently-nil `Stdin()` does today.
- **[Threat model alignment]** No new threat is reachable from
  `docs/protocol-mobile.md` § Security model (:1019): the design accepts no wire data,
  adds no parsing, and changes no crypto or transport, so threats 1–6 and 8 are
  untouched. The one relevant class is #7, denial of service (`severity: low-medium`,
  `mitigation: deferred`), and this ticket **improves** rather than widens it — a
  `new_session` flood already forces a respawn per frame today; the gate adds only a
  refusal window each such respawn already implies, cleared by the spawn it accompanies.
  The reachable abuse the pass did surface is the two-frame `abort` sequence in
  § Design, which the generation stamp closes; without the stamp it would be a MUST FIX
  (attacker-triggerable, deterministic, reproducible from two frames). Adjacent open
  work named as out of scope: #1298 (the silent-park M4 stall) and #1331 (per-child
  stdin tee attribution).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
