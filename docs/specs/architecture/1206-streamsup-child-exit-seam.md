# #1206 — a per-child-exit seam on the streamsup runner

**Size:** S (confirmed; PO's estimate stands — see § Scope check)
**Labels:** `bug`, `security-sensitive`
**Split from:** #1203. Sibling: #1207 (the consumer; natively blocked-by this ticket).

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/streamsup/runner.go:395-492` | `Run`'s supervise loop in full. This is the whole design surface — the fire window is `:444`–`:450`, and every citation below indexes into this range. |
| `internal/streamsup/runner.go:104-115` | `Config`'s optional-field block, ending in the `onSpawn` unexported test seam. The new field goes here; `onSpawn`'s comment is the tone and density to match, **not** the visibility to copy. |
| `internal/streamsup/runner.go:494-564` | `spawnAndWait` — the `(started bool, waitErr error)` contract, the two pre-launch `return false, …` sites (`:533`, `:538`), the `onSpawn` invocation (`:547-549`), `cmd.Wait` (`:551`), and the `takeStdin` drop (`:557-561`) that makes `Stdin()` nil before the new callback runs. |
| `internal/streamsup/runner.go:176-218` | `New` — copies `cfg` wholesale into the Runner. Confirms the new field needs **no** validation, no default, and no `New` edit at all. |
| `internal/streamsup/state.go:10-14` | `PhaseStopped` = "Run has returned". The one-line proof that the existing lifecycle surface cannot express per-child exit (AC2's negative half). |
| `internal/streamsup/runner_test.go:41-92` | `helperRunCfg` / `runInBackground` / `waitForContains` — the harness every new test builds on. |
| `internal/streamsup/runner_test.go:299-332` | `TestRunner_RestartsOnCrash` — the crash-respawn shape to mirror, including the buffered-channel `onSpawn` counter idiom. |
| `internal/streamsup/runner_test.go:396-502` | `spawnArgsRecorder` + `TestRunner_SpawnSetupFailureRetainsSessionID` — the direct-`&Runner{}` construction that bypasses `New`'s `exec.LookPath`, and the recorder that counts spawn *attempts* (the only observer that sees iterations where no child launched). |
| `internal/streamsup/runner_test.go:504-539` | `TestRunner_TeardownSIGTERM` — the shutdown shape. |
| `internal/streamsup/runner_test.go:648-739` | `TestRunner_RestartFresh_RotatesThenResumesNewID` — the deliberate-restart shape and the `sync.Once`-guarded-callback idiom. |
| `internal/streamsup/helper_test.go:31-103` | The fake-child mode table. `crash` (`:77`), `echo_lines` (`:58`), and `record_block` (`:87` — never self-exits, so any exit is caller-caused) are the three modes this ticket needs. No new mode. |
| `cmd/pyry/streamsup_runner.go:155-175` | `mapStreamsupConfig` — the sole production `streamsup.Config` literal, named-field. Confirms the new field is nil in production with zero edits (AC5). |
| `cmd/pyry/stream_turn_busy.go:27-35` | The `KNOWN GAP` comment this seam exists to eventually close. **Read-only for this ticket** — it stays as-is (see § Out of scope). |

---

## Context

`internal/streamsup` has no way to tell anything outside the package that a claude child died. `Run` supervises one child at a time and respawns on the backoff ladder; the only lifecycle state that escapes is `PhaseStopped`, which means "`Run` has returned" (`state.go:14`) and fires exactly once, on permanent shutdown (`runner.go:415-419`). A crash-and-respawn is therefore invisible from outside: no pool transition (`RotateBootstrapForSelfHeal` deliberately fires none, `internal/sessions/pool.go:659-667`), no `TurnEnd` on the fan-in (the abandoned turn produces no `result` line), and the respawn's own `system`/`init` line is dropped by the turn-stateless parser (`parser.go:158-164`).

This slice ships **only the seam**, with no production consumer — the same order `Interrupt` (#1120 → #1121) and `RestartFresh` (#1124 → #1125) shipped in. #1207 wires the first consumer (a turn-busy clear) and is natively blocked-by this ticket.

---

## Design

### The seam

One new exported field on `Config`, in the optional block next to `onSpawn`:

```go
// (declaration only — full contract comment specified below)
OnChildExit func()
```

**Why an exported `Config` field.** `onSpawn` is unexported and settable only from in-package `_test.go` files, so mirroring it exactly fails AC1's out-of-package requirement. A `Config` field is also the project's stated DI convention (constructor arguments via the Config struct, `CLAUDE.md` § Go Architecture Patterns) and needs no `New` edit — `New` copies `cfg` wholesale (`runner.go:209-217`).

**Why `func()` with no arguments.** After a `RestartFresh` the runner's live id (`r.sessionID`, read via `nextSpawnID` at `:367`) diverges from `Config.SessionID`, but `Config.SessionID` is what tags this runner's events on the fan-in — so a runner-supplied id would invite the wrong one to be plumbed downstream. A bare `func()` keeps the identity decision at the wiring site and makes the callback carry literally no data out of the child.

**Rejected alternatives.** A channel field (needs buffering, lifetime, and close semantics, and a full buffer would either block `Run` or silently drop an exit); an observer interface (a 1-method interface for a fire-and-forget notification is heavier than the `onSpawn` precedent in the same struct); a new `Phase` value (`Phase` mirrors `supervisor.Phase` string-for-string per `state.go:5-7` — adding one cascades to the `cmd/pyry` adapter and the status builder, which AC5 forbids).

### The fire site

**Exactly one unconditional call, in the `:444`–`:450` window** — after `uptime := time.Since(start)` (`:444`) and above the post-spawn shutdown return (`:450-451`):

```
441  started, waitErr := r.spawnAndWait(iterCtx, args)
442  cancel()
443  r.setIterCancel(nil)
444  uptime := time.Since(start)
     ← the fire goes HERE (nil-checked, unconditional, one call)
450  if ctx.Err() != nil { return ctx.Err() }        // shutdown
454  if waitErr != nil { r.log.Warn("claude exited", …) }
470  if r.drainRestart() { continue }                 // deliberate restart
474  delay := bo.next(uptime)                         // crash → backoff ladder
```

`spawnAndWait` returns at `:441` and the loop only *then* branches on why the child is gone. One unconditional call above the first branch therefore covers all three exit paths without enumerating them, which is what keeps the production diff to one field plus one call.

**Two wrong anchors that the developer must not take** (both satisfy two of AC3's three paths and wedge on the third):

1. **The `claude exited` log at `:454-458`.** It sits *below* the shutdown return, so it never runs on a parent-ctx cancel. Anchoring there gives crash ✓, deliberate restart ✓, shutdown ✗.
2. **A `return ctx.Err()` site.** `Run` has three, and firing at all of them is worse than firing at none:
   - `:423` — top-of-loop guard, runs *before* any spawn. A fire there reports an exit for a child that never existed, on the first iteration.
   - `:451` — the post-exit shutdown return. **The only one in the window**, and it is already covered by the single `:444`–`:450` call.
   - `:486` — inside the backoff `select`, reached only *after* the `:441` spawn already returned and already fired. A fire there is a **second** fire for one child exit and breaks AC1's cardinality.

The single call in the window covers `:451` and needs no companion at the other two.

### Cardinality: per iteration, not per live child

The call is unconditional — it does **not** gate on `started`. So it also fires when the spawn failed during setup (`StdinPipe`/`cmd.Start` erroring, `:528-539`) and no claude process ever launched. This is AC4's chosen arm, and it is what keeps the diff to a single call site.

That choice constrains the doc comment (AC1's second sentence): the wording must be **"once per completed supervision iteration"**, never "once per child" or "once per child exit", because the latter is false on the setup-failure path. The field *name* stays `OnChildExit` — it names what the consumer cares about and reads correctly at the wiring site (`scfg.OnChildExit = func() { … }`) — and the comment carries the precision. This name/cardinality tension is deliberate, not an oversight.

Firing there is harmless for the intended consumer: no child existed, so no turn of this runner's can be open, and #1207's clear is idempotent. A future caller that needs "a real child died" cannot get it from this seam, and the comment must say so.

### The contract comment (required content)

The comment is the deliverable here as much as the code — it is the only thing #1207 will design against. It must state, in this package's existing density (compare `onSpawn` at `:110-114`, `Restart` at `:281-296`, `RestartFresh` at `:314-330`):

- **What.** Called once per **completed supervision iteration**, after the spawn attempt finishes and before `Run` decides what to do next (shut down / relaunch immediately / back off). Optional; nil in production — this slice ships the seam unwired, #1207 wires the first consumer.
- **Cardinality caveat.** Also fires when the spawn failed during setup and no claude process ever launched (`started == false` in `spawnAndWait`). Deliberate: no child existed so no turn can be open, and the intended consumer's clear is idempotent. A caller needing "a real child died" cannot get it from this seam.
- **Path coverage.** Fires on every exit path — crash (→ backoff ladder), deliberate restart (→ immediate relaunch), and shutdown. The shutdown case works because the call sits **above** `Run`'s post-spawn `ctx.Err()` return, so a parent-ctx cancel fires it before `Run` returns.
- **Goroutine + blocking.** Runs on the `Run` goroutine with **no Runner lock held** (`setIterCancel(nil)` at `:443` has already released `restartMu`), so it may call any `Runner` method — but it **must not block**: the restart ladder is stalled until it returns. Slow work belongs on a channel or a goroutine.
- **Liveness ordering.** Fires strictly **after** the child has exited — `spawnAndWait` returns only after `cmd.Wait` (`:551`) or a pre-launch error — never while claude is still running. `Stdin()` is already nil when it runs (`takeStdin` at `:557`).
- **Not a drain barrier.** It carries no data out of the child, and bytes the dead child already wrote may still be in flight in a downstream sink when it fires. A consumer that needs ordering against those events must get it from that sink, not from this callback. *(Load-bearing for #1207 — see § Open questions.)*
- **No panics.** It runs on the supervisor's own goroutine; a panic takes the daemon's supervise loop with it. No `recover` is added (matching `onSpawn`).

### What does not change

- `New` — no validation, no default, no edit. The field rides along in the wholesale `cfg` copy.
- `spawnAndWait` — untouched. The fire is deliberately *not* pushed down into it (see § Security review).
- `State` / `Phase` — untouched.
- `cmd/pyry` — **zero files touched.** `mapStreamsupConfig` is a named-field literal, so the new field is nil on the sole production construction path with no edit.
- Backoff timing, the `--resume` vs first-run `--session-id` spawn form, and the deliberate-restart-skips-backoff rule — all unchanged, asserted by the existing tests continuing to pass unmodified.

---

## Concurrency model

No new goroutines, no new channels, no new locks.

The callback is invoked synchronously on the `Run` goroutine. At the fire site no Runner mutex is held: `spawnAndWait` released `mu` before returning (`setStdin`/`takeStdin` are both lock-and-release), and `setIterCancel(nil)` at `:443` released `restartMu`. A callback may therefore call `Restart`, `RestartFresh`, `Interrupt`, `Stdin`, or `State` without deadlock — all of which are already documented safe from any goroutine.

The one hazard is a **blocking** callback: it stalls the restart ladder for as long as it blocks, delaying respawn. That is a contract statement on the callback, not something the runner can enforce, and the doc comment must say so explicitly.

---

## Error handling

The seam has no error channel by design: it is fire-and-forget notification, matching `onSpawn`. There is no failure mode inside the runner to handle — the only new failure the code can introduce is a nil deref, prevented by the `!= nil` guard, and a panic or a hang inside the caller's callback, both of which are the caller's contract to uphold (stated in the comment, no `recover`).

The exit *reason* is deliberately not passed. `Run` already distinguishes shutdown / restart / crash **below** the fire site, and re-deriving it above would mean duplicating that branch logic or moving the fire down past `:450` — the exact wedge AC3 exists to prevent.

---

## Testing strategy

All four tests go in `internal/streamsup/runner_test.go`, reusing the existing harness. No new helper-child mode. No e2e. **No existing test is modified** (AC5).

Counter shape: an `atomic.Int64` (or a mutex-guarded int) read *after* `join()` returns, which makes every assertion below exact rather than a poll. `runner_test.go` does not yet import `sync/atomic`; adding it is the only import change.

**1. Crash-respawn fires it (AC2).** Mirror `TestRunner_RestartsOnCrash` (`:299`): `crash` mode, `BackoffInitial` 1ms / `BackoffMax` 5ms, a buffered `spawns` channel on `onSpawn` and a buffered `exits` channel on the new field.
- Wait for ≥2 spawns and ≥2 exits, each with a 5s timeout.
- Then cancel and assert `Run` returns `context.Canceled`.
- Proves the seam fires on a self-crashing child repeatedly across the backoff ladder — which `PhaseStopped` (one fire, on shutdown only) structurally cannot.

**2. Shutdown fires it (AC3, the load-bearing one).** Mirror `TestRunner_TeardownSIGTERM` (`:506`) but with `echo_lines` mode (a child that blocks on held-open stdin and never self-exits, so exactly one iteration runs before cancel — `block_sigterm` would work too but deliberately burns the 5s SIGKILL grace).
- Sync on `onSpawn` so the child is up before cancelling — cancelling first would exit via the top-of-loop guard at `:423` and fire nothing, making the test vacuous.
- Cancel, `join()`, assert `Run` returned `context.Canceled`, then assert the exit count is **exactly 1**.
- Deterministic, not a poll: the fire is at `:445` and the return at `:451`, both on the `Run` goroutine, so `join()` returning is proof the fire already happened.
- **This is the test that kills the wrong anchors.** A developer who anchors on the `claude exited` log fails it with count 0; one who additionally fires at `:486` or `:423` fails it with count 2.

**3. Deliberate restart fires it (AC3).** `record_block` mode (`helper_test.go:87` — blocks until SIGTERM, never self-exits, so *every* exit in this test is caller-caused and the assertion has no self-exit noise).
- Sync on spawn 1 via `onSpawn`, call `r.Restart(nil)`, sync on spawn 2, then cancel and `join()`.
- Assert the exit count is **exactly 2**: one for the restart-killed child, one for the shutdown-killed child.
- Also confirms the fire happens *before* the `drainRestart` skip-backoff branch at `:470`, since a fire below it would be skipped on this path.

**4. Spawn-setup failure fires it, and the contract is pinned (AC4).** Mirror `TestRunner_SpawnSetupFailureRetainsSessionID` (`:454`): construct `&Runner{cfg: Config{ClaudeBin: <path that does not exist>, …, OnChildExit: …}, log: slog.New(rec), workDir: tmp}` directly, bypassing `New`'s `exec.LookPath`. Every spawn fails at `cmd.Start`, so no child ever launches.
- Wait for `rec.count() >= 2` spawn attempts, cancel, `join()`.
- Assert the exit count **equals** `rec.count()` — one fire per attempted iteration, including the ones where no child existed.
- The equality holds exactly at `join()` time: the "spawning claude" log (`:436`) and the fire (`:445`) are both on the `Run` goroutine with no return point between them, and every `Run` exit path either fires first (`:451`, `:486`) or hasn't logged yet (`:423`). If the implementation wrongly gated on `started`, this reads 0 and fails.
- The test's doc comment must name which arm of AC4 is pinned and point at the field's contract comment.

**5. Unwired-ness (AC5).** Not a Go test — three commands to run before commit, each against its **predicted** number, not eyeballed. All use `git grep` (tracked files only: `.claude/worktrees/` holds a stale checkout a repo-root `grep -rn .` would walk) and `-i` (a capitalisation variant of the spec's own identifier is exactly how #1201's grep under-counted):

```bash
# (a) every tracked mention of the identifier — expect EXACTLY 2 paths,
#     internal/streamsup/runner.go and internal/streamsup/runner_test.go
git grep -l -i -e 'onchildexit' -- cmd internal

# (b) non-test files mentioning it — expect EXACTLY 1
git grep -l -i -e 'onchildexit' -- cmd internal | grep -cv '_test\.go'

# (c) any mention outside the owning package — expect ZERO lines (exit status 1)
git grep -n -i -e 'onchildexit' -- cmd internal ':!internal/streamsup'
```

Baseline verified at `1d428ce`: `git grep -l -i -e 'onchildexit' -- cmd internal` returns **nothing today**, so no pre-existing hit inflates any count. Note the near-collision that a laxer pattern would catch: `internal/supervisor/supervisor_test.go:341` (`TestSupervisor_ChildExitsCleanly`) matches a bare `-i childexit` but **not** `onchildexit` — grep the full identifier.

Per #1201's lesson, pair the name-shaped checks with a **name-independent** one that catches a consumer regardless of what it is called:

```bash
# expect EXACTLY 3 paths: the spec .md, internal/streamsup/runner.go,
# internal/streamsup/runner_test.go — and NOTHING under cmd/
git diff --name-only main...HEAD
```

**Gate.** `make check` green, and `go test -race ./internal/streamsup/...` run with `-v` so skips are visible — `ok <pkg> Ns` hides skipped tests, and a skip is not a pass.

---

## Scope check

| Red line | Count | Verdict |
|---|---|---|
| New files | 0 | ✓ |
| Total written LOC (production + tests + spec) | ~30 prod (comment-dominated) + ~220 test + spec doc ≈ 280 | ✓ (< 600) |
| New exported types/interfaces | 0 (one exported *field*, no new type) | ✓ |
| Consumer call sites needing simultaneous update | 0 — every `streamsup.Config` literal is named-field (`cmd/pyry/streamsup_runner.go:158`; in-package `runner_test.go:51`, `:199`, `:223`, `:459`, `:799`) | ✓ |
| Acceptance criteria | 5 | ✓ |
| Distinct error/reject branches | 0 | ✓ |

Production source files (`*.go`, excluding `*_test.go`) with new or modified content: **1** — `internal/streamsup/runner.go`. Well under the ≥5 commit gate.

**Branch overlap:** none. `git fetch origin --prune` then a per-`origin/feature/*` diff against `origin/main` for `internal/streamsup/runner.go` and `internal/streamsup/runner_test.go` returned no hits (branch-based, so it sees in-flight work with no PR yet). `origin/feature/1203` — the pre-split parent — is exactly `main`.

**Size confirmed S**, not lowered to XS: the production diff is genuinely XS-shaped, but four helper-process exit-path tests with spawn/teardown synchronisation are ~220 LOC of the real turn cost, and the deliverable is a documented public-`Config` contract, not a trivial edit.

---

## Out of scope

- **The `#1203` comments in production code.** `git grep -n '#1203' -- '*.go'` returns `cmd/pyry/stream_turn_busy.go:31`, `:33`, and `cmd/pyry/relay.go:743`. #1203 is closed (split into this ticket and #1207), so those comments cite a closed ticket — **leave them**. The gap they describe is still genuinely open after this slice; it closes only when #1207 lands, and #1207 claims those edits. A `grep '#1203'` during this work is a false lead.
- **`docs/knowledge/`** — the `#1203` references in `features/streamsup-package.md`, `INDEX.md`, `codebase/1201.md`, `codebase/1202.md` are documentation-phase territory. So is `docs/knowledge/codebase/1206.md`, which documentation writes from this spec plus the merged diff — it is **not** a developer deliverable.
- **Any consumer.** Nothing in `cmd/` changes. The turn-busy clear, the identity decision (`Config.SessionID` vs the rotated live id), and the fan-in ordering question below are #1207's.

---

## Open questions

1. **Ordering against already-buffered events (for #1207, not this slice).** When the callback fires, `cmd.Wait` has returned — but events the dead child already wrote may still be queued in `streamTurnSink`'s buffered channel, undrained. A consumer that clears turn-busy synchronously in this callback can therefore be overtaken by a *later*-delivered `TextChunk` from the dead child, which would re-open the turn. #1207 must resolve this — most likely by riding the existing fan-in channel so FIFO orders the clear against those events, rather than clearing inline. This spec's only obligation is to state the non-guarantee on the seam's contract comment ("not a drain barrier") so #1207 designs against the truth. **No code in this slice.**
2. **Should a later consumer ever need the exit reason?** Deliberately not provided (see § Error handling). If #1207 or a successor needs it, the cheapest extension is a second field rather than widening this one's signature — the branch data lives below the fire site and re-deriving it above would reintroduce the AC3 wedge.

---

## Security review

**Verdict:** PASS

The label marks one question (per the ticket's own Technical Notes): *can the seam fire while the child is still alive?* Under #1207 this callback becomes a turn-busy clear, and under #1199 that clear releases a queued mid-turn send — so a fire that is not a real exit would release a send into a running turn.

**Findings:**

- **[Trust boundaries] No findings.** The seam moves **zero data**. `func()` with no parameters and no return value crosses the package boundary; the child's stdout continues to flow through the unchanged `Config.Stdout` → `Parser` path (`runner.go:506`), which this ticket does not touch. There is no new untrusted→trusted transition, and the "no data out of the child" property is enforced by the *type*, not by a check — the strongest available form. The identity a consumer keys on stays `Config.SessionID` at the wiring site, never the runner's rotated `r.sessionID` (`:150-155`), which is the whole reason the signature takes no arguments.

- **[Concurrency — the labelled question] No findings; the property is structural, not checked.** The fire site is above `:450` but strictly **below** `:441`, and `spawnAndWait` returns only from three places, all of which strictly post-date the child's death: `:533` (`StdinPipe` failed — no process ever created), `:538` (`cmd.Start` failed — no process ever created), and `:563` (after `cmd.Wait` at `:551` returned). `cmd.Wait` returns only once the process has exited; with `cmd.WaitDelay = killGrace` (`:523`) it may return early with `ErrWaitDelay` if the I/O pipes are still open, but that path too is entered only after process exit. There is no arrangement of these three returns under which claude is still running when the callback fires. **This is exactly what the review was asked to gate**, and it holds only because the fire is in `Run` above `:450` — a design that moved it *down inside* `spawnAndWait` (e.g. beside the `onSpawn` call at `:547`, or before `cmd.Wait`) would fire while the child is alive and is a MUST FIX if it appears in the implementation. Recorded as a code-review checkpoint: **verify the call is in `Run`, in the `:444`–`:450` window, and that `spawnAndWait` is unmodified.**

- **[Concurrency — locks and lifecycle] No findings.** No new goroutine, channel, or mutex, so no leak and no new lock-order edge. The callback runs with **no** Runner lock held (`mu` released inside `spawnAndWait`; `restartMu` released by `setIterCancel(nil)` at `:443`), so a consumer calling back into `Restart`/`RestartFresh`/`Interrupt`/`Stdin`/`State` cannot deadlock. The residual risk is availability, not integrity: a callback that blocks stalls the restart ladder for the duration. Stated as a contract in the doc comment; the runner cannot enforce it and adding a timeout/goroutine would be a defence for a failure mode never observed.

- **[Error messages / logs] No findings.** The seam adds no log line and no error value; the existing `claude exited` records at `:454-458` are unchanged, and the callback receives nothing it could leak.

- **[Subprocess execution] No findings.** No change to argv construction (`buildArgs`, `:593`), `cmd.Dir`, `cmd.Env`, `cmd.Cancel`'s reap-then-SIGTERM (`:519-522`), or `WaitDelay`. The teardown and descendant-reap behaviour is byte-identical, pinned by `TestRunner_TeardownSIGTERM` continuing to pass unmodified.

- **[File operations] N/A** — no path is constructed, opened, or written. **[Tokens/credentials] N/A** — none handled. **[Cryptographic primitives] N/A** — none used. **[Network & I/O] N/A** — no socket or listener in this package.

- **[Threat model alignment] Benign directions named explicitly.** The dangerous direction (fire too early, while the child lives) is foreclosed structurally above. The opposite directions are absorbed downstream and are why AC4 was free to take the unconditional arm: a fire with **no child** (spawn-setup failure) means no turn of this runner's can be open, and a **double** fire is idempotent under #1207's clear. Cross-runner interference is out of scope by construction — the callback is per-`Config`, so it can only ever notify the consumer that wired *that* runner.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-25
