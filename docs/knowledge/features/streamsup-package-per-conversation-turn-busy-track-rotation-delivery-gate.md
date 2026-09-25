# Rotation-delivery gate (#1330)

Closes #1295's Open question 4 ("the clear-before-`RestartFresh` window … not structurally
excluded"). `startFreshRunner` ran `rotate()` — including `Pool.RotateForNewSession`'s
`ReasonClear` transition fan-out — to completion before calling `RestartFresh`, so the clear
reached clients, and released whatever the turn-busy tracker held, while the outgoing child
was still alive and the fresh one did not yet exist. A turn accepted in that ~4 ms window was
written into the doomed child; msgqueue reads a successful write as a commit and drops the
head, so the turn is gone — silently, since nothing errors.

**The invariant:** from `BeginRotation()` until the next child's stdin binds, `WriteUserTurn`
writes nothing and returns `ErrNoLiveChild`. Two new fields, `rotating bool` and
`rotateGen uint64`, live under the **same leaf mutex `mu` already guards `stdin` with** —
`mu`'s charter widened from "guards the stdin handle" to "guards which child, if any, may
receive a turn." That widening is the whole mechanism: `turnTarget() (w io.Writer, gated bool)`
answers "is a rotation armed?" and "which child's stdin?" as **one question under one
acquisition**. Two acquisitions — read the flag, then separately read `stdin` — reopen the
race at nanosecond width; a later "simplification" that splits them back apart would reintroduce
this ticket's defect invisibly.

`BeginRotation() (abort func())` sets `rotating = true`, bumps `rotateGen`, and returns a
disarm that only takes effect if no later arm has landed — the same generation-stamped shape
as `openForDelivery`'s undo (#1199, above). This is load-bearing, not defensive padding: two
overlapping `new_session` frames are an ordinary sequence (the e2e's own retry loop re-sends
every ~250 ms), and the loser's `rotate()` ordinarily fails `ErrSessionNotFound`
(`sessions/transition.go:120-123`) and runs its `abort()`. Unstamped, that would clear the
*winner's* arm while the winner's outgoing child is still alive, reproducing the defect on
demand from two frames. `setStdin` publishes the new handle unconditionally and clears `rotating`
in that same acquisition only when the spawn is authorised (#1482, below); `takeStdin` deliberately
leaves it alone, since the teardown it performs is the
*middle* of the window the gate covers, not its end — releasing on `RestartFresh`'s return
would only narrow the race, since that call returns after `cancel()` while the kill, `cmd.Wait`,
and the respawn all still run later on the Run goroutine.

`startFreshRunner` (`cmd/pyry/main.go`) arms the gate by calling `r.BeginRotation()` directly —
`Interrupt`, `RestartFresh` and `BeginRotation` all moved onto `sessions.Runner` in #2592, so every
runner, production and test double alike, now offers all three as a build-time requirement rather
than an optional capability. Before #2592 the arm went through `beginRotationOrNoop`, an optional
type assertion that let a runner offering `RestartFresh` without a gate rotate ungated instead of
degenerating into an inert default; that fail-open shape is gone along with the assertion. The
existing rotate-before-`RestartFresh` order — originally load-bearing so the (now-retired, #2137)
rotation watcher would observe the id registered as freshly allocated before it saw the CREATE,
avoiding a double-rotation — is preserved on a surviving reason (#1330): `rotate()` fires the
`ReasonClear` transition fan-out, so without the gate armed first, a turn accepted in the window
between that fan-out and the fresh child existing would write into the doomed outgoing child and
be silently dropped. The arm is inserted ahead of both statements. `streamRunner.BeginRotation()`
forwards it, satisfying `sessions.Runner.BeginRotation` alongside `Interrupt` (#1120/#2592) and
`RestartFresh` (#1124/#2592).

**The refusal record is logged at `Info`, deliberately diverging from the spec's `Debug`.** The
e2e's stability guard greps the daemon's *whole* captured stderr for the literal
`level=DEBUG`; since the refusal fires on essentially every rotation, a `Debug` record would
have satisfied that guard from the fix's own diagnostic, turning a 20-run stability measurement
into a near-tautology. `Info` keeps the "refused by the gate" vs. "no child yet" discriminator
without touching that guard. It names only the event and the runner's own session id — never
the `conversationID` parameter (accepted for interface conformance, otherwise unused) and never
payload bytes.

**The release side is now authorised (#1482) — the two-frame gap this paragraph used to describe
is closed in its stated shape.** `setStdin` no longer clears `rotating` for any child that binds;
it clears only for a spawn set up after the `RestartFresh` that landed after the standing arm.
The authorisation is a monotonic threshold, not a one-shot token — a one-shot ("clear only if
this spawn consumed `rotatePending`") was rejected because a successor spawn that dies during
setup would spend it and wedge the gate armed forever. The shape: `freshSeq uint64` under
`restartMu` counts `RestartFresh` rotations that have landed; `RestartFresh` bumps it inside its
existing `restartMu` section (the empty-id early return stays above that section, so a no-op
call never bumps it). `BeginRotation` reads the current `freshSeq` under `restartMu` — **before**
taking `mu` — and captures it as `armFreshSeq` in the same `mu` acquisition that sets
`rotating = true` and bumps `rotateGen`; two sequential leaf acquisitions, never nested. Each
spawn snapshots `freshSeq` inside `beginSpawn`'s existing `restartMu` section (the same section #1481 fused the id read and `iterCancel` publish into) and carries it to `setStdin`, which clears
`rotating` iff the snapshot is strictly greater than `armFreshSeq`. Because `RestartFresh`'s bump
and `beginSpawn`'s snapshot are both sections of the same mutex, a spawn set up before the arm's
partner `RestartFresh` always reads a snapshot equal to `armFreshSeq` (refused) and a spawn set up
after always reads strictly greater (authorised) — this holds for a crash/backoff respawn of the
pre-rotation id and for a `Restart`-driven respawn alike, and it is exactly what closes the
originally-described interleaving: frame 2's arm captures the counter *after* frame 1's
`RestartFresh` has already bumped it, so frame 1's pending successor snapshots a value that is not
strictly greater and cannot clear frame 2's arm.

**What is still open, honestly.** The window between `BeginRotation`'s `restartMu` read and its
`mu` acquisition is not closed — a different frame's `RestartFresh` could land and a spawn take
its snapshot in those few instructions, and that spawn's later bind would then clear the arm
being placed. Closing it would require holding `mu` and `restartMu` at once, which this package's
never-nested leaf-mutex discipline (§ Concurrency model, below) forbids. It is unreachable from a
single `new_session` frame — `startFreshRunner` calls `BeginRotation` and `RestartFresh` in program order on one
goroutine — so it needs two concurrent frames interleaving inside a handful of instructions, and
no failure of this shape has been observed; left open per evidence-based fix selection rather
than closed speculatively. If evidence appears, the fix is a re-read-and-re-stamp under the arm's
own `rotateGen`, not a widening of the mutex discipline. See [codebase/1330.md](../codebase/1330.md)
for the original gate this paragraph's fix builds on.

**A second, separate gate covers deliberate teardowns outside rotation — eviction and
settings-restart (#1513).** `BeginRotation`'s gate only ever armed on the `new_session` path.
Both eviction arms of `sessions.Session.runActive` (the idle-timer arm and the `evictCh`
cap/force arm, which `Pool.Remove` also drives through `Session.Evict`) and
`Pool.UpdateSettings`' restart branch — immediately above `sup.Restart(newArgs)`, **inside**
that branch only, since the in-band branch beside it tears nothing down — killed a live child
with no gate armed at all: a write already past the delivery seam landed in the doomed child's
stdin pipe, returned nil, and msgqueue read nil as a confirmed commit and dropped the queue head
silently, same defect, different door.

The obvious fix — call the existing gate from these three sites — wedges the session instead of
fixing it. Every one of these teardowns *ends in* a `Restart`-driven or crash respawn, which is
exactly the bind whose `freshSeq` snapshot reads **equal** to `armFreshSeq` and which the
rotation gate's release rule (above) therefore refuses to clear. Arming `rotating` here would
leave every turn refused until some unrelated later `RestartFresh` happened to land. So `Runner`
carries a second flag, `tearingDown`, armed by the new `BeginTeardown()` together with its own
threshold `armChildGen` (the `childGeneration` value standing at arm time — the counter `setStdin`
and `takeStdin` already bump on every bind, since #2384). `setStdin` releases `tearingDown`
whenever the post-bump `childGeneration` reads strictly greater than `armChildGen` — and because
the bump happens before the comparison, **every** bind after the arm satisfies it, including the
equal-`freshSeq` binds the rotation gate refuses. `BeginTeardown` returns no disarm, unlike
`BeginRotation`: the gate is self-clearing at the next bind of any child, so a stray arm costs at
most one respawn's worth of refused turns rather than needing an abort to bound it.

`turnTarget`'s single boolean widened to `turnTargetWithGate` / `turnGate` (`gateOpen` /
`gateRotation` / `gateTeardown`) so `WriteUserTurn` can log a cause-specific record — still one
`r.mu` acquisition, the property this file's whole gate rests on. `gateRotation` is tested first
(the stricter release, the record worth an operator's attention), and `gateTeardown` is reported
only while a handle is bound: a stale `tearingDown` on an evicted session with nothing rebound is
an ordinary resting state, not an anomaly, and reporting it on every delivery attempt would be a
pure log-volume regression rather than a diagnostic gain — with no child bound the refusal falls
through to the pre-existing nil-handle path unconditionally.

`BeginTeardown()` sits on `sessions.Runner` itself, not behind a capability type-assertion, for
the reason this file's `Runner` interface section (see
[sessions-package's `Runner` interface](sessions-package-key-types-runner-interface-runnerfactory.md))
already applies to `SetSpawnPermissionMode`: the three arm sites live inside `internal/sessions`,
which must not import `internal/streamsup`, and a structural assertion there fails **open** — an
unmatched arm would silently leave a teardown ungated while every layer reported success, the
exact failure being closed. All six implementations (the production `streamRunner` adapter and
the five test doubles) gained the method as a build-time requirement rather than an optional hook.

**Two lessons that only show up once you try to test the ordering, not just the arming.** A test
built on `lifecycleRunner`'s existing per-method record slices can prove an arm *happened* but
never that it happened *before* the kill — "armed after the kill" reads identically to "armed
before it" on a set of independent slices. The ordering rows instead use a purpose-built recorder
(`internal/sessions/teardown_gate_test.go`'s `teardownRecorder`) that appends every call —
`BeginTeardown`, `Restart`, `Run`'s ctx-fired teardown — into one ordered log, because only a
shared log can express "first". And the first version of the idle-eviction row was itself flaky
by construction: it polled `Session.LifecycleState() == stateEvicted`, but `beginEvict` flips
that state and releases its lock **before** `runActive`'s idle arm reaches `cancelSup`, while the
recorder can only log the actual kill **after** `cancelSup` runs — so the wait condition was
ordered strictly earlier than the event the test asserted about, and a `pollUntil` sleep silently
stood in for a happens-after relation that did not exist. Green unloaded, red under CPU
contention. The fix was to poll the recorder's own log for the kill event instead of a published
state transition — the same edge its sibling forced-eviction row already had for free, since
`Session.Evict` blocks on the eviction's own completion channel. The general tell: if the
condition being waited on is published by a call that returns before the event under test, a
poll against it is a sleep with extra steps, not a synchronization point.

**Testing this kind of authorisation window: the vacuity guard and the arm placement both depend
on the harness mode, and a flat test function hides the per-row evidence a mutant needs (#1482).**
`WriteUserTurn` returns `ErrNoLiveChild` for both "gated" and "no child bound", so every row must
assert through `turnTarget`'s `gated` return, never `errors.Is(err, ErrNoLiveChild)` alone — but a
`Stdin() != nil` non-vacuity guard is only safe with an `echo_lines` child that is guaranteed
alive; adding it to a `crash`-mode row (whose 20 ms child may legitimately already be gone) trades
a real assertion for flakiness. Where the test arms the gate matters as much as what it asserts:
arming from the test goroutine after a `waitSpawn` receive reads naturally but races the
respawn — with a fast enough crash the successor can already have bound before the arm lands, so
the row passes on both the shipped and mutant trees and proves nothing. Arming inside the first
spawn's `onSpawn` (under a `sync.Once`) puts the arm strictly before the next `beginSpawn` by
program order on the single Run goroutine, which is what makes the row capable of failing at all.
Subtests, not a flat function, are what let a mixed-outcome mutant (row (a) reddens, row (b) stays
green) become visible per row — flattening them would also force the "refused bytes never reached
a child" check into a place with no happens-after edge to hang it from.
