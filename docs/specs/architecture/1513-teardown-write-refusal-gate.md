# #1513 — Arm the write-refusal gate on eviction and settings-restart teardowns

## Files read

- `internal/streamsup/runner.go` → `Runner.mu` (the charter doc: why one acquisition, not two),
  `Runner.rotating`, `Runner.rotateGen`, `Runner.armFreshSeq`, `Runner.childGeneration` — the four
  fields the existing gate is built from, and the counter this ticket's release rule keys on.
- `internal/streamsup/runner.go` → `BeginRotation`, `turnTarget`, `WriteUserTurn`, `setStdin`,
  `takeStdin` — the arm, the read, the refusal, the release, and the teardown that deliberately does
  *not* release. The new gate slots into each of these five.
- `internal/streamsup/envelope.go` → `ErrNoLiveChild`, `WriteTurn` — the retryable sentinel and the
  zero-bytes contract the new refusal reuses verbatim.
- `internal/sessions/runner.go` → `Runner` interface, and the doc on `SetSpawnPermissionMode` /
  `SetPermissionMode` — the recorded argument for why a method whose consumer is inside
  `internal/sessions` goes ON the seam rather than behind a type assertion. AC4 is that argument
  applied again.
- `internal/sessions/session.go` → `Session.runActive` (its four select arms), `beginEvict`,
  `Session.Evict` — the two eviction arms that arm, and the two (`ctx.Done`, `runErr`) that the
  ticket puts out of scope.
- `internal/sessions/pool.go` → `Pool.UpdateSettings`, `inBandDeliverable` — the branch split, and
  the one live `Restart` caller that remains below it since #2066.
- `cmd/pyry/streamsup_runner.go` → `streamRunner` and its `SetSpawnPermissionMode` /
  `SetPermissionMode` delegates — the shape each new delegate copies, plus
  `var _ sessions.Runner = streamRunner{}`, which is what turns AC4 into a build failure.
- `internal/sessions/runner_test.go` → `fakeRunner`, `lifecycleRunner` (its per-method record slices
  and their `r.mu`-guarded readers) — the doubles AC3's ordering assertion is built on.
- `internal/sessions/session_evict_race_test.go` → `raceRunner`;
  `cmd/pyry/inbound_deliver_rotation_test.go` → `baseRunner`;
  `cmd/pyry/session_router_test.go` → `stubRunner` — the remaining three doubles a widened seam
  touches, one line each.
- `internal/streamsup/runner_test.go` → `TestRunner_BeginRotation_CrashRespawnLeavesTheGateArmed`
  and the `Restart`-respawn subtest beside it, plus `helperRunCfg`, `runInBackground`, `waitSpawn`,
  `waitReadyMarkers`, `waitForContains` — the live-child harness AC1 and AC2 reuse, and the exact
  precedent for driving an *equal*-`freshSeq` bind on purpose.
- `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md`
  § "Rotation-delivery gate (#1330)" — the recorded contract this ticket extends; the documentation
  stage folds the new arm in under that heading.

## Context

`internal/streamsup` has one write-refusal gate, armed by `BeginRotation` and read by `turnTarget`
under the same `Runner.mu` acquisition that reads the stdin handle. It exists because a stream write
into a doomed child's stdin pipe returns nil, msgqueue reads nil as a confirmed commit and drops the
queue head, and the message dies in the pipe unread. #1330 armed it on the `new_session` rotation
path. Three other deliberate-kill paths never got it:

- both eviction arms of `Session.runActive` — the idle-timer arm and the `evictCh` cap/force arm,
  which `Pool.Remove` also drives via `Session.Evict`;
- `Pool.UpdateSettings`' restart branch, which calls `sup.Restart` on a live child.

**The release trap.** `setStdin` disarms the rotation gate only for a spawn whose `freshSeq` snapshot
is *strictly greater* than `armFreshSeq` (#1482). A `Restart`-driven respawn, or a crash respawn off
the backoff ladder, reads *equal* and deliberately leaves the gate standing — that refusal is what
stops an unrelated respawn from clearing a rotation's arm. So reusing `rotating` for a teardown that
ends in exactly such a respawn would wedge the session: every turn refuses until some later
`RestartFresh` lands. A teardown gate needs a release that **any** successor child satisfies, and
`Runner.childGeneration` — bumped in both `setStdin` and `takeStdin` — is the counter that expresses
it.

This is a second gate with its own counter and its own threshold, not a widening of the first.

No ADR is warranted: this applies `Runner.mu`'s existing charter and #1482's arm/threshold shape to
three more callers rather than establishing a boundary.

## Design

### streamsup: the teardown gate

Two new fields on `Runner`, guarded by the existing `mu` and documented beside `rotating` and
`armFreshSeq`:

- `tearingDown bool` — a deliberate teardown is armed and no child has bound since.
- `armChildGen uint64` — the `childGeneration` value standing when the arm was placed; meaningful
  only while `tearingDown` is true.

One new exported method:

```go
// BeginTeardown arms the teardown gate ... Unlike BeginRotation it returns no
// disarm: every arm is self-clearing at the next bind, so a stray arm cannot wedge.
func (r *Runner) BeginTeardown()
```

It takes `mu` once and sets `tearingDown = true; armChildGen = r.childGeneration`. It needs neither
`restartMu` nor a `rotateGen`-style stamp — see "Why no abort, and no generation stamp" below — so
it is a single leaf acquisition with no call-out, safe from any goroutine with no Pool lock held.
That is the same property `BeginRotation`'s doc asserts, and it is what lets `internal/sessions` call
it from inside `runActive` and from `Pool.UpdateSettings` past its unlock.

**Release**, in `setStdin`, in the same acquisition that publishes the handle and bumps the counter,
directly beside the rotation gate's own release so the two thresholds read side by side:

```go
r.childGeneration++
if spawnFreshSeq > r.armFreshSeq { r.rotating = false }     // #1330/#1482, unchanged
if r.childGeneration > r.armChildGen { r.tearingDown = false } // #1513
```

Because `setStdin` bumps before comparing, *every* bind after the arm reads strictly greater — which
is exactly AC2's "any successor child satisfies it", including the `Restart`-driven and crash-respawn
binds the rotation gate's threshold refuses. `takeStdin` bumps the counter but does not test it, so
the teardown itself never releases the gate; that mirrors the reason `takeStdin` leaves `rotating`
alone — the teardown is the middle of the window, not its end.

**Read.** `turnTarget` must report *which* gate refused, because the two carry different operator
records. Rather than change its signature — it has eight in-package assertion sites that read the
boolean — the fuller answer moves into a new sibling and `turnTarget` becomes its boolean face:

```go
type turnGate uint8
const (gateOpen turnGate = iota; gateRotation; gateTeardown)

// turnTargetWithGate reports the writer a turn may be written to and WHICH gate,
// if any, refused — all under ONE r.mu acquisition (see mu's doc).
func (r *Runner) turnTargetWithGate() (w io.Writer, gate turnGate)

// turnTarget is its boolean face: callers that need only "may a turn be written?".
func (r *Runner) turnTarget() (w io.Writer, gated bool)
```

Every read still happens inside the single acquisition, so `mu`'s charter is preserved exactly: the
reason two acquisitions are forbidden is unchanged and untouched. `gateRotation` is checked first, so
a doubly-armed runner reports the rotation — the record an operator can act on, and the arm with the
stricter release.

`gateTeardown` is reported **only while a handle is bound**; with `stdin` nil the method falls
through to the pre-existing nil-handle path and returns `gateOpen` with a nil writer. The refusal is
byte-identical either way — nil writer, `ErrNoLiveChild`, zero bytes — so nothing is weakened: with
no child bound the gate has nothing to protect and the refusal is over-determined. What this avoids
is a log-volume regression the gate would otherwise introduce. An arm placed by an eviction that ends
in no respawn stands until the session is next activated, and reporting it unconditionally would turn
the silent no-live-child refusal of an evicted session into one Info record per delivery attempt for
as long as it sits evicted. #1330's record accepts that shape because a rotation's window always ends
in a respawn and a standing arm past it is an anomaly worth being loud about; an evicted session is
an ordinary resting state, not an anomaly. The rotation gate's own ordering and behaviour are left
byte-identical — this discrimination applies to the new gate only.

**Refuse.** `WriteUserTurn` switches on the gate and emits one record per cause, at Info, outside
every acquisition, with `session` as the only field — all four constraints copied verbatim from the
rotation record's doc (a slow slog handler must not block the Run goroutine's `setStdin`; a
`level=DEBUG` record anywhere in the daemon's stderr defeats #1330's e2e instrument guard; #833 keeps
settings values out of the log). The teardown record names its own cause: `"streamsup: turn refused;
session teardown in flight"`. The refusal itself is `WriteTurn`'s verbatim contract — nil target,
`ErrNoLiveChild`, zero bytes, no envelope construction — so no new classification enters the system,
which is the ticket's third out-of-scope item honoured in code.

### Why no abort, and no generation stamp

`BeginRotation` needs both because its gate can outlive a rotation that never happened: a losing
frame's `rotate()` fails, and without the disarm every turn refuses until the next `RestartFresh`.
Neither hazard exists here. The teardown gate releases at the *next bind of any child*, so a stray
arm costs at most the turns inside one respawn and then clears itself; and both teardown callers are
unconditional — `cancelSup` always runs on the arms that reach it, and `Runner.Restart` returns
nothing and cannot fail. Two overlapping teardown arms are also harmless for the same reason: the
later arm's threshold is the higher one, and one bind satisfies both. Adding a stamp and an abort
would be machinery for a failure mode this gate's release rule structurally excludes.

### sessions: the seam

`BeginTeardown()` goes ON the `sessions.Runner` interface — AC4 — for the argument
`SetSpawnPermissionMode` and `SetPermissionMode` already carry on that same interface: the consumer
is inside `internal/sessions`, which must not import `internal/streamsup` (`Pool.deliverSettingsInBand`'s
doc states the inversion), and a structural assertion there fails **open**. An unmatched arm would be
a silent no-op leaving the teardown ungated while everything reports success — the exact failure this
ticket closes. The interface method makes a runner that cannot arm a build failure instead.

Six implementations gain it: `streamRunner` (delegating to `(*streamsup.Runner).BeginTeardown`),
and the five doubles `baseRunner`, `stubRunner`, `raceRunner`, `fakeRunner`, `lifecycleRunner`.

### sessions: the three arm sites

`Session.runActive`, idle-timer arm and `evictCh` arm — `s.sup.BeginTeardown()` placed immediately
**above** `s.beginEvict(ReasonEviction)`, hence above `cancelSup`. The AC's "before `cancelSup`" is
satisfied either side of `beginEvict`; above it is strictly stronger and costs nothing, for #1330's
own placement rule — `beginEvict` fires `session_transition{eviction}` to clients, and the arm
belongs before anything that publishes the teardown, not between the publication and the kill.
`BeginTeardown` takes one leaf mutex and makes no call-out, so it disturbs nothing in #1186's
signal-then-flip ordering.

The `ctx.Done` arm (daemon shutdown) and the `runErr` arm (child already gone) are untouched, per the
ticket's out-of-scope list.

`Pool.UpdateSettings` — `sup.BeginTeardown()` immediately above `sup.Restart(newArgs)`, inside the
restart branch only. Not above the branch split: the in-band branch kills nothing, and arming there
would refuse turns for a delivery that never tears a child down. It sits past `p.mu.Unlock()`, where
the two `SetSpawn*` calls already sit, so no Pool lock is held across it.

## Concurrency model

No new goroutines, no new channels, no new mutex. The gate's state lives entirely under the existing
`Runner.mu`, a leaf never held across a channel op, a log call or another object's lock — both new
fields obey that unchanged. `BeginTeardown` takes `mu` exactly once and never nests, so it cannot
participate in a lock-ordering hazard; `BeginRotation`'s documented `restartMu → mu` sequence is not
extended, because this arm reads no `restartMu` state.

The arm's three call sites hold no Pool lock: the two in `runActive` run on the session's own
lifecycle goroutine (`s.lcMu` is released before both), and `Pool.UpdateSettings`' runs after
`p.mu.Unlock()`.

Arm and release are ordered by `mu` alone. A bind concurrent with an arm serialises one way or the
other, and both orders are safe: arm-then-bind releases at that bind (correct — the bind is after the
arm), bind-then-arm leaves the gate standing for the next one.

## Error handling

`BeginTeardown` returns nothing and cannot fail: it mutates two Runner-internal fields under a leaf
mutex. There is no error path to handle at any of the three call sites, and none of them gains a
branch.

The gate's only caller-visible effect is `WriteUserTurn` returning `ErrNoLiveChild` with zero bytes
written — the retryable classification msgqueue and `newInboundDeliver` already agree on. No new
sentinel is minted (the ticket's explicit third exclusion), so msgqueue's retry-and-give-up behaviour
and the e2e's contract check that stream `WriteUserTurn` returns only `ErrNoLiveChild`,
`turncommit.ErrDropped` or nil both hold unchanged.

**Residual, deliberately left open:** a write that passed `turnTargetWithGate` before the arm landed
is already in flight and the gate cannot recall it. #1330's arm carries the identical residual, and
closing it would need the mutex charter widened in exactly the way `Runner.mu`'s doc rejects. Out of
scope per the ticket.

## Testing strategy

**AC1 — the gate refuses on a live child** (`internal/streamsup`, live-child harness): spawn via
`helperRunCfg`/`runInBackground`, wait for the first spawn and its READY marker, `BeginTeardown()`,
then assert `WriteUserTurn` returns `ErrNoLiveChild` and that the probe string never appears in the
child's echoed stdout. Guarded by `Stdin() != nil` first, so the assertion cannot collapse into the
pre-existing no-live-child refusal and pass vacuously. Fails against `main`: `BeginTeardown` does not
exist there, and the write succeeds.

**AC2 — both release rules in one run** (`internal/streamsup`): arm *both* gates on the same runner,
drive one `r.Restart(nil)` respawn — the bind whose `freshSeq` snapshot reads equal to `armFreshSeq`,
the precedent for which is the `Restart`-respawn subtest beside
`TestRunner_BeginRotation_CrashRespawnLeavesTheGateArmed` — and assert on that single bind that the
teardown gate released while the rotation arm still stands. Read through `turnTargetWithGate`, whose
`turnGate` return distinguishes the two; a bare boolean could not tell "one gate cleared" from
"neither did".

**AC3 — arm precedes teardown on all three paths** (`internal/sessions`): `lifecycleRunner` gains an
ordered `events []string` log with an `r.mu`-guarded reader, appended by `BeginTeardown`, by
`Restart`, and by `Run` when its ctx fires. Three cases, one per path — idle timeout, `evictCh` via
`Pool.Remove`, and `Pool.UpdateSettings` with a Model cleared to `""` (the one update shape that
still reaches `Restart` since #2066) — each asserting the arm's index precedes the teardown's.
Ordering, not presence: a test that only counted arms would stay green with the arm placed after the
kill, which is the whole defect.

**AC4 — compile-time**: proven by `var _ sessions.Runner = streamRunner{}` in `cmd/pyry` plus the
five doubles. No new test; a missing implementation is a build failure, which is the AC's literal
demand. A `grep` for a type assertion on the arm path is not needed — there is no assertion to write,
because the method is on the interface.

Gate: `go test -race` on `internal/streamsup`, `internal/sessions` and `cmd/pyry`, plus `go vet ./...`
and `go build ./cmd/pyry`.

## Open questions

1. **Record level and wording for the teardown refusal.** Info with `session` as the only field, by
   inheritance from the rotation record's four documented constraints. To resolve at implementation:
   whether an idle eviction of a busy session makes this record noisy enough to want Debug — it
   cannot, because #1330's e2e instrument guard greps the whole daemon stderr for `level=DEBUG`, so
   Info is forced. Expect no change; record under `## Revisions` if the wording moves.
2. **Whether `turnTarget`'s boolean face survives review.** Keeping it spares eight in-package
   assertion sites a mechanical rewrite and keeps this ticket inside its call-site boundary. If the
   wrapper reads as a shim rather than a layering during implementation, the alternative is to widen
   `turnTarget` itself and update those eight — which is a sizing decision, so it would be recorded
   under `## Revisions` rather than taken silently.

## Documentation handoff

**Pending — owned by the documentation stage, not by this ticket.** Fold the teardown arm into
`docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md`,
under its existing "Rotation-delivery gate (#1330)" heading: name which teardowns arm the gate
(both eviction arms of `Session.runActive`, and `Pool.UpdateSettings`' restart branch) and which
release rule each arm answers to — `armFreshSeq`'s strictly-greater `freshSeq` for the rotation arm,
`armChildGen`'s strictly-greater `childGeneration` for the teardown arm, and why the second cannot
reuse the first. No file under `docs/knowledge/` is edited by this ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. `BeginTeardown` takes **no parameters**, so no operator- or
  wire-controlled value reaches the gate and there is no injection surface on the arm at all. The
  boundary-adjacent path is `Pool.UpdateSettings`, which *is* driven by an operator frame: an
  attacker-influenced input can therefore cause the gate to be armed, but arming can only ever cause
  a **refusal**, never a write, never a widened target, and never a change to which argv is exec'd.
  The gate moves no data across any boundary — it decides *whether* to write, never *what*; the
  payload reaching `WriteUserTurn` is validated upstream and is untouched here.
- **[Tokens, secrets, credentials]** No findings — none are handled. The adjacent hazard that is
  real: the new refusal record must carry `session` and nothing else. Specifically not the
  conversation id (resolved daemon-side and stamped on the wire, never logged), not payload bytes,
  and not the Model/Effort/permission-mode value that drove `UpdateSettings` onto the restart branch
  (#833 keeps settings values out of the daemon log). The design specifies `session` only; the
  verifier should check the emitted field set.
- **[File operations]** Not applicable by design decision: the change touches no filesystem path,
  creates no file and sets no mode. The one adjacency is `Pool.UpdateSettings`' `saveLocked` registry
  write, which the arm is placed strictly **after** — past both the persist and `p.mu.Unlock()` — so
  the atomic-write path and its failure rollback are unreachable from this change.
- **[Subprocess / external command execution]** No findings. The arm sits immediately above
  `cancelSup` and `sup.Restart`, so the adversarial question is whether it can **delay or skip** a
  kill. It cannot: `BeginTeardown` takes one leaf mutex, mutates two fields and returns — no channel
  op, no log call, no call-out, nothing that can block — and it adds no branch and no `return` above
  either teardown, so every path that killed a child before still kills it. It changes no argv and no
  signal handling.
- **[Cryptographic primitives]** Not applicable — no randomness, no keys, no comparisons against a
  secret. `childGeneration` is explicitly **not** an authorisation token: it is a monotonic
  process-local counter, never persisted, never serialised, never logged, and it gates only the
  *release* of a refusal. Wraparound would release a stale arm early, and needs ~9.2×10^18 spawns on
  a `uint64` bumped twice per spawn — unreachable, and the same argument `freshSeq` and `rotateGen`
  already stand on.
- **[Network & I/O]** No findings — no socket, no read loop, no size cap, no timeout, no TLS surface
  is touched. The remote-visible change is that a send racing a teardown now yields a retryable
  refusal instead of a false success, which is the **fail-closed** direction.
- **[Error messages, logs, telemetry]** SHOULD FIX, **resolved in the design above rather than
  deferred**: reporting `gateTeardown` unconditionally would turn an evicted session's silent
  no-live-child refusal into one Info record per delivery attempt for as long as it sits evicted —
  a log-volume regression, since an evicted session is an ordinary resting state rather than the
  anomaly #1330's equivalent record is argued against. The design now reports `gateTeardown` only
  while a handle is bound. Otherwise no findings: `ErrNoLiveChild` carries no session state, and the
  record's volume is bounded by msgqueue's 1 s retry interval inside a window a respawn closes.
- **[Concurrency]** No findings, and this is the category the change actually lives in. Lock
  ordering: `BeginTeardown` takes `mu` exactly once and **never nests**, so it cannot participate in
  an inversion, and `BeginRotation`'s documented `restartMu → mu` sequence is not extended because
  this arm reads no `restartMu` state. TOCTOU: arm, release and read all serialise on `mu`, and the
  read answers "which gate?" and "which handle?" in **one** acquisition — the property `Runner.mu`'s
  charter exists to protect, preserved by moving the fuller read inside `turnTargetWithGate` rather
  than splitting it. Two arms racing (a `runActive` eviction and an `UpdateSettings` restart) is
  harmless: the later arm carries the higher threshold and one bind satisfies both. Goroutine
  lifecycle: none spawned, none leaked. Shutdown mid-teardown: the gate is process-local and
  persists nothing, so no partial state can survive to the next start. **To verify in Phase B:** the
  `runActive` arm sites must sit after `s.lcMu.Unlock()` in the idle-timer arm — no Pool or session
  lock may be held across the call.
- **[Threat model alignment]** `docs/threat-model.md` does not exist; `docs/protocol-mobile.md`
  § Security model is relay-facing and this change is daemon-internal — no new frame, no new field,
  no new error code, no change to what the relay can observe. The one alignment worth naming is an
  **integrity improvement**: a remote sender can no longer be told "delivered" for a message that was
  dropped into a child the daemon was about to kill.
- **OUT OF SCOPE** — a sustained flood of settings-clear frames drives repeated `Restart` teardowns,
  and each window refuses turns; held up past msgqueue's 2 m give-up bound that becomes message loss
  by another door. This is a pre-existing property of `Pool.UpdateSettings` having no rate limit, not
  one this ticket introduces: today the same flood destroys in-flight turns *silently*, so the gate
  strictly improves the failure mode (loud and retried, rather than committed and lost). Rate-limiting
  `UpdateSettings` is its own ticket and is not filed here; the protocol spec records the same
  deferred posture for pairing-mint abuse ("revisit on observed abuse"), and no abuse has been
  observed here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15

## Revisions

### 2026-09-15 — implementation

**AC3's runner double.** The Testing strategy above proposed extending `lifecycleRunner` with an
ordered `events` log. Implemented instead as a new, self-contained `teardownRecorder` in
`internal/sessions/teardown_gate_test.go`, embedding `fakeRunner` for the method set it does not
exercise and overriding `BeginTeardown`, `Restart` and `Run`. Two reasons, both found while writing
it: `lifecycleRunner`'s records are per-method slices read by a dozen existing rows, and adding a
cross-cutting ordered log beside them invites a later reader to assert ordering against records that
cannot express it; and `lifecycleRunner.Run` spawns a real `/bin/sleep`, which these three rows do
not need. `lifecycleRunner` therefore takes a documented no-op `BeginTeardown` pointing at the
double that does record.

**A fourth row.** `TestPool_UpdateSettings_InBandBranchDoesNotArm` was added beyond the three the
plan named. The Design section's "not above the branch split" is a real constraint with no assertion
behind it otherwise: hoisting the arm above `inBandDeliverable`'s split leaves all three planned rows
green while every in-band settings change starts refusing turns for a teardown that never happens.

**Open question 1 — record level and wording: resolved as predicted.** Info, with `session` as the
only field, and its own message (`"streamsup: turn refused; session teardown in flight"`) rather than
sharing the rotation record, which would name a cause that did not happen. Debug is unavailable for
the reason the plan gave. No design change.

**Open question 2 — `turnTarget`'s boolean face: kept.** It reads as a layering rather than a shim in
place: `turnTargetWithGate` answers "which gate?", `turnTarget` answers "any gate?", every read stays
inside the one `mu` acquisition, and the eight existing in-package assertion sites are untouched.
Confirmed not to trip staticcheck's unused-code check. No sizing change.

### 2026-09-15 — rework (verifier FAIL, PR #2441)

No production behaviour changed; the design above stands unamended.

**The blocking finding — `TestSession_IdleEviction_ArmsTeardownGateBeforeKill` was flaky by
construction.** The row waited on `Session.LifecycleState() == stateEvicted`, but `beginEvict` flips
`lcState` and releases `lcMu` *before* `runActive`'s idle arm reaches `cancelSup`, while
`teardownRecorder.Run` can only append `evRunEnded` after it. The wait edge was therefore ordered
strictly **earlier** than the event the row asserts, leaving `pollUntil`'s 10 ms sleep standing in
for a happens-after relation: green unloaded, red under CPU contention with a log holding the arm and
no kill. The row now polls the recorder's own log for `evRunEnded`, which is the edge it actually
needs — the shape its sibling `TestSession_ForcedEviction_…` already had for free, since `Evict`
blocks on `evictedCh` and `endEvict` closes that after `drainSup`. `assertArmedBefore`'s `kill == -1`
branch is kept and becomes a genuine "the kill never happened" diagnostic rather than a race.

The Testing strategy above is unchanged in substance — the row proves the same ordering on the same
path; only the synchronisation it proves it through is corrected.

**Three documentation nits, no behaviour change.** `WriteUserTurn`'s two adjacent gate `if`s became a
`switch gate`, which states the mutual exclusion the three-valued type already implies. `turnTarget`'s
doc now says it is the test-facing face — Open Question 2 above resolved to keep it, but recorded the
reason without recording that `WriteUserTurn` taking `turnTargetWithGate` leaves it with no
production caller, which is what a later reader would otherwise go hunting for. `armChildGen`'s doc
now names the one residual its "ANY successor child" rule carries: a crash respawn landing between
the arm and the teardown beside it also releases the gate. Named rather than fixed, for the reason
the Design section rejects a `rotateGen`-style correlation.
