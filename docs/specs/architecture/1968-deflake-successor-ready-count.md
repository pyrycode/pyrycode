# #1968 — deflake the successor-attribution READY count

## Files read

- `internal/streamsup/runner_test.go` → `TestRunner_BeginRotation_UnauthorisedBindLeavesTheGateArmed`,
  `waitSpawn`, `waitForContains`, `safeBuffer`, `helperRunCfg`, `runInBackground` — the failing
  test and every barrier it currently holds.
- `internal/streamsup/helper_test.go` → `helperChild`, its `echo_lines` arm — where the `READY`
  marker is written, and by whom.
- `internal/streamsup/runner.go` → `spawnAndWait` (the `cmd.Start` → `setStdin` → `onSpawn` →
  `cmd.Wait` sequence), `Restart`, `RestartFresh`, `setStdin`, `takeStdin` — what `onSpawn`
  actually orders, and how a child is killed.
- `internal/agentrun/reap.go` → `ReapDescendantGroups`, `descendantPGIDs` — `cmd.Cancel` shells
  out to `ps` before the SIGTERM. This is the accidental, unbounded grace window that lets a
  doomed child usually reach its marker, and the reason the flake is load-sensitive.
- `docs/knowledge/features/streamsup-package-firing-the-ask-at-spawn-time.md` § "Test-writing
  lesson" — this package has already recorded this exact failure class ("waiting on `onSpawn`'s
  signal proves the daemon **wrote** the line, not that the child **read** it … the count reads 1
  where 2 was expected, intermittently"), and already fixed it the same way: wait for the child's
  own output before killing it. The fix below is that convention applied to the marker itself.
- `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md`
  — the `#1330`/`#1482` invariant the test protects, so the repair cannot weaken it.

## Context

`TestRunner_BeginRotation_UnauthorisedBindLeavesTheGateArmed/the_successor_bind_drops_the_gate`
fails intermittently in `make check` under full-suite parallel load, on `main`. The reported run
saw 2 `READY` lines where the test wants 3. The gate behaviour under test was correct — the
successor's echo did arrive — so what raced is the assertion's precondition, not the invariant.

No ADR is warranted: this is a test-ordering repair inside one file.

### Diagnosis — which write was absent, and why nothing orders it

`echo_lines` writes its marker as the first statement of the child's own `main`. Reaching it
costs a full `exec` + dynamic link + Go runtime start in a fresh process. The parent, meanwhile,
signals `onSpawn` from `spawnAndWait` immediately after `cmd.Start` returns — before the child
has run a single instruction of its own. So `onSpawn` orders the child's **existence**, never its
first write.

Between that signal and the kill there is exactly one thing standing: `cmd.Cancel` runs
`ReapDescendantGroups`, which forks `ps -axo pid=,ppid=,pgid=` and parses the whole process
table before delivering SIGTERM. That is an accidental grace window — unbounded, unsynchronised,
and nothing the test asked for. It is wide enough that the child usually wins. Under full-suite
load both sides slow down by different factors and the child sometimes loses. `cmd.Wait` drains
whatever the child *wrote*, so a child killed before its write loses the marker **permanently**
for that run, not merely late; this is why the failure surfaces as a short count and never as a
slow one.

Two of the three writes are exposed, and each is independently sufficient to produce the reported
message:

- **Child 1's marker.** `waitSpawn(…, "the first spawn")` returns on `onSpawn`; `BeginRotation()`
  and the `t.Run` handoff follow; then `Restart(nil)` cancels the iteration and the reap+SIGTERM
  path kills it. No barrier anywhere touches the child's stdout.
- **Child 2's marker.** `waitSpawn(…, "the Restart respawn")` returns on `onSpawn`; the three row-(a)
  assertions follow (`Stdin()`, `turnTarget()`, and a `WriteUserTurn` that the gate *refuses*, so
  nothing is written to the child and nothing comes back); then row (b)'s `RestartFresh` kills it.
  Again nothing touches stdout. Row (a)'s refusal is specifically not a barrier: `ErrNoLiveChild`
  is produced entirely inside the runner.

**Child 3's marker is already ordered** and needs no repair: `echo_lines` writes `READY` before it
enters its scan loop, both writes cross the same pipe, and one copier goroutine appends them to
`out` in order — so `waitForContains(out, successorTurnProbe)` cannot succeed unless child 3's
marker is already in the buffer.

The captured artifact cannot say *which* of child 1 and child 2 lost in that particular run: both
emit the identical literal, the count is the only signal, and children are serial so the surviving
marker's position in `out` is the same either way. That ambiguity is not a gap in the diagnosis —
both writes are unordered, both are independently fatal, and the repair must therefore order
both. § Testing strategy forces each one separately and confirms each alone reproduces the
reported failure.

## Design

Test-only. No production file changes: the runner's behaviour is correct and the ticket's
production-side escape hatch does not fire.

One new test helper beside `waitSpawn` in `internal/streamsup/runner_test.go`:

- `waitReadyMarkers(t *testing.T, b *safeBuffer, n int)` — polls `b` until it holds at least `n`
  `READY` markers, fatalling after a fixed 5 s (matching `waitSpawn`'s own bound). Its doc names
  the hazard: `onSpawn` is not a write barrier, and a child killed before its marker never writes
  it. Contract: **call it before killing any child whose marker a later count assertion needs.**

Two call sites in `TestRunner_BeginRotation_UnauthorisedBindLeavesTheGateArmed`:

1. Parent level, between `waitSpawn(…, "the first spawn")` and `BeginRotation()` — `n = 1`.
2. Row (a), **immediately** after `waitSpawn(…, "the Restart respawn")` and **above** the three
   assertions — `n = 2`.

Placement (2) is load-bearing, not stylistic. Under the `#1482` mutant, row (a) fatals at its
`turnTarget` check; a barrier placed after that check would never run, child 2 would be killed by
row (b)'s `RestartFresh` at the same width as today, and row (b) would redden on the mutant —
destroying exactly the per-row discrimination this test exists to record.

The waits compose rather than merely repeat: site (1) guarantees the count is already 1 before
`Restart`, so site (2)'s `>= 2` can only be satisfied by child 2's own marker.

**The attribution assertion is untouched.** `strings.Count(out.String(), "READY") != 3` stays
exact. The barriers are preconditions establishing that the three markers the test drives exist;
they are not the attribution. A fourth spawn — a crash-respawn the test did not drive — still
pushes the count to 4 and still reddens the subtest.

## Concurrency model

Unchanged. `safeBuffer` already serialises the os/exec copier goroutines against the test
goroutine under its own mutex, and `waitReadyMarkers` reads only through `safeBuffer.String()`.
No new goroutine, no new shared state, no change to the runner's supervise loop, which stays
strictly serial: one child at a time, each child's `cmd.Wait` (and therefore its copier drain)
completing before the next `beginSpawn`. That serialisation is what makes a count over the whole
buffer meaningful in the first place.

## Error handling

`waitReadyMarkers` fatals on timeout with the observed count and the full buffer, so a future
regression reports the same evidence the current assertion does rather than a bare deadline. It
never reports success on a short count. The 5 s bound is generous against the millisecond-scale
child startup it waits on and matches the existing `waitSpawn` / `waitForContains` bounds in this
file, so it cannot turn a flake into a hang.

## Testing strategy

The tree carrying this bug already produced 20/20 and 5/5 greens, so a repeat-run green proves
nothing. Every claim below is settled by a forced interleaving or a mutant, run via
`go test -overlay=<abs-path>` so nothing is written into the worktree.

1. **Forced interleaving — both children.** Overlay `helper_test.go` to sleep before the
   `echo_lines` marker write, so both doomed children are guaranteed to lose the race. Pre-fix:
   `the successor bind drops the gate` reddens with a short count. Post-fix: passes.
2. **Forced interleaving — child 1 alone**, and **3. — child 2 alone.** Same overlay, with the
   sleep selected on the spawn's own argv (`--session-id <testSessionID>` is child 1,
   `--resume <testSessionID>` is child 2, `--session-id <rotatedSessionID>` is child 3). Each
   reproduces the reported `saw 2 READY lines, want 3` on the pre-fix tree and passes after the
   fix. This is what establishes that each write is independently unordered, and it is the
   evidence behind § Context's ambiguity claim.
3. **`#1482` discrimination survives.** Overlay `runner.go` to restore `setStdin`'s unconditional
   `r.rotating = false`, dropping the `spawnFreshSeq > r.armFreshSeq` authorisation. Required
   result on the fixed tree: `an unauthorised bind leaves the gate armed` FAILS while
   `the successor bind drops the gate` EXECUTES and PASSES.
4. **Exactness survives.** The assertion is left byte-identical apart from its comment, so a
   fourth marker still reddens by construction. Recorded as a structural argument, not a run.
5. Touched-scope gate: `go test -race ./internal/streamsup/...`, `go vet ./...`,
   `go build ./cmd/pyry`.

## Open questions

- **Which of child 1 / child 2 lost the race in the reported `make check` run?** Not answerable
  from the artifact, and it does not change the repair — both are unordered and both are fixed.
  Resolved by making the question moot rather than by answering it; § Testing strategy items 2–3
  show each is independently sufficient.
- **Should the forced-interleaving knob be committed as a permanent helper flag rather than run
  as an overlay?** Provisional answer: no. A default-off delay knob is never exercised by
  `make check`, so it would be dead surface that reads as coverage; the structural repair is the
  regression protection. Confirm during implementation that the overlay reproduces cleanly, and
  record the outcome under `## Revisions` if it does not.
