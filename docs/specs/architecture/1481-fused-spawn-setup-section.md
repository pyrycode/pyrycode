# #1481 — Fuse the spawn-id read and the iteration-cancel publish into one `restartMu` section

**Size: S.** One production file (`internal/streamsup/runner.go`), one test file
(`internal/streamsup/runner_test.go`), one evergreen doc
(`docs/knowledge/features/streamsup-package.md`). No new exported types, no interface change,
one production call site (`Run`).

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/streamsup/runner.go` → `Run` — the spawn-setup block at the top of the loop body. Extract: the exact statement order (`nextSpawnID` → `buildArgs(liveArgs(), …)` → `log.Info("spawning claude", …)` → `WithCancel` → `setIterCancel`) and the started-gated `firstRun` flip further down.
- `internal/streamsup/runner.go` → `nextSpawnID`, `liveArgs`, `setIterCancel` — the three `restartMu` accessors the fix subsumes. Extract: each takes `restartMu` itself, so a fused section **cannot call any of them** (Go mutexes are not reentrant).
- `internal/streamsup/runner.go` → `RestartFresh`, `Restart` — the two racers. Extract: both read `iterCancel` under `restartMu`, **release**, then call `cancel()` outside the lock. That release-before-cancel ordering is what keeps the fused section deadlock-free.
- `internal/streamsup/runner.go` → `buildArgs` — pure; it `append`s `base` into a freshly-`make`d slice and never retains or mutates it. Extract: that is why the fused section can pass `r.args` directly and `liveArgs`'s `slices.Clone` disappears rather than moving.
- `internal/streamsup/runner.go` → `spawnAndWait` — the `(started bool, waitErr error)` contract. Extract: `started == false` on any pre-launch failure, and the loop's `if started { firstRun = false }` gate that depends on it.
- `internal/streamsup/runner.go` → `Runner` — the `restartMu` field-group doc comment plus the `args`, `sessionID` and `rotatePending` field comments. Extract: the charter sentence to widen, and the three comments that name `liveArgs`/`nextSpawnID` by hand.
- `internal/streamsup/runner_test.go` → `spawnArgsRecorder` — an existing `slog.Handler` that captures the argv of every `"spawning claude"` record. Extract: it is the hook the regression test wraps; `Handle` runs **synchronously on the Run goroutine**.
- `internal/streamsup/runner_test.go` → `TestRunner_RestartFresh_RotatesThenResumesNewID` — the closest existing analogue (once-guarded rotation + recorder + argv assertions). Extract: its shape, its `idFlagValue` / `idFlagCount` helpers, and `rotatedSessionID` / `testSessionID`.
- `internal/streamsup/runner_test.go` → `TestRunner_RestartFresh_EmptyIDIsNoOp` — the **only** test-side caller of `nextSpawnID`. Extract: it must be retargeted when `nextSpawnID` is deleted.
- `internal/streamsup/runner_test.go` → `helperRunCfg`, `runInBackground` — harness reused verbatim.
- `internal/streamsup/helper_test.go` → `helperChild` — the fake-claude modes. Extract: `record_block` blocks until SIGTERM (a stable long-lived child); `crash` exits after 20 ms.
- `internal/sessions/pool.go` → `UpdateSettings` — the sole production `Restart(args)` caller. Extract: confirms no signature change reaches it, so this ticket has no cross-package fan-out.
- `docs/knowledge/features/streamsup-package.md` → § "Supervise loop (`Run`)", § "Fresh-restart under a new id (#1124)", § "Live-restart seam", and the `codebase/1124.md` line in the See-also list — the four sites naming the old shape.

## Context

`Run`'s spawn setup currently reads the rotation state and publishes the iteration cancel in
**two separate `restartMu` critical sections**, with a `buildArgs` allocation and a synchronous
log write between them. `iterCancel` is nil across that gap (the previous iteration cleared it),
so a `RestartFresh(newID)` landing there sets `sessionID`/`rotatePending`, reads `cancel == nil`,
**cancels nothing**, and returns — while the spawn proceeds with the id it read before the
rotation. The child then lives under the pre-rotation id for its whole lifetime, and the next
crash-respawn consumes the still-set `rotatePending` and spawns `--session-id <newID>` —
first-run form, a fresh transcript — discarding every turn since the rotation.

`Restart(args)` has the identical hole across the argv seam: the swap lands in `r.args` after
`buildArgs` has already captured the old argv, so a settings change waits for the child's next
natural death.

The ticket body has the full failure chain and the citation of the two-frame interleaving that
produces it on every run of #1330's e2e.

**Scope boundary.** `setStdin`'s unconditional `rotating` disarm is #1482's concern. This spec
must not touch `setStdin`, `rotating`, `rotateGen`, or `mu`.

## Design

### The shape

Collapse the whole spawn setup into **one** `restartMu` acquisition that both *reads* the
rotation state and *publishes* the cancel. Because `nextSpawnID` and `liveArgs` each take
`restartMu` and Go mutexes are not reentrant, the fused section cannot call them — the argv
build moves inside, and both accessors are deleted.

Add one unexported accessor:

```go
// beginSpawn captures one iteration's spawn inputs AND publishes that iteration's
// cancel in a SINGLE restartMu section, so a racing Restart/RestartFresh can never
// observe the post-read/pre-publish state that #1481 describes. Returns the derived
// iteration ctx, its cancel, the fully-built argv, and whether a fresh-restart
// request was consumed (the caller re-arms its private firstRun on true).
func (r *Runner) beginSpawn(ctx context.Context, firstRun bool) (
	iterCtx context.Context, cancel context.CancelFunc, args []string, forceFirst bool,
)
```

Body (contract sketch — the whole point is the single Lock/Unlock pair):

```go
iterCtx, cancel = context.WithCancel(ctx)   // deliberately BEFORE the lock: no nesting
r.restartMu.Lock()
defer r.restartMu.Unlock()
forceFirst = r.rotatePending
r.rotatePending = false
args = buildArgs(r.args, firstRun || forceFirst, r.sessionID)
r.iterCancel = cancel
return
```

Three properties the developer must preserve verbatim:

1. **`context.WithCancel` is constructed outside the section.** It takes no runner lock, so
   constructing it inside would also be correct, but keeping it out means `restartMu` is never
   held across the context package's own internal locking. The section holds exactly one
   pointer-store's worth of work plus a `buildArgs` allocation.
2. **No `slices.Clone` of `r.args`.** `buildArgs` copies `base` into a fresh slice and never
   retains it, so the clone `liveArgs` performed exists only because the slice used to escape
   the lock. It no longer does. (Read `buildArgs` and confirm before dropping it.)
3. **The `"spawning claude"` log stays in `Run`, AFTER the section.** Synchronous log I/O must
   not run under a leaf mutex. Keeping it *after* is what puts the racer on the branch-(b) side
   of the invariant — see § Test design.

### The call site

`Run`'s loop body becomes:

```go
iterCtx, cancel, args, forceFirst := r.beginSpawn(ctx, firstRun)
if forceFirst {
	firstRun = true
}
r.log.Info("spawning claude", "args", args, "workdir", r.workDir)

start := time.Now()
started, waitErr := r.spawnAndWait(iterCtx, args)
cancel()
r.clearIterCancel()
uptime := time.Since(start)
```

Note the `:=` here declares four **new** loop-body locals; `firstRun` is the outer function's
local and must stay assigned via the `if forceFirst` block, never redeclared by a `:=` that
would shadow it and silently break the started-gated flip. That is the reason `beginSpawn`
returns `forceFirst` rather than a new `firstRun`.

### Narrowing `setIterCancel` → `clearIterCancel`

After the fix there is exactly one site that may publish a non-nil `iterCancel`: `beginSpawn`.
Replace `setIterCancel(c context.CancelFunc)` with:

```go
// clearIterCancel drops the finished iteration's cancel under restartMu. It takes no
// argument on purpose: publishing a non-nil cancel from anywhere but beginSpawn's
// section is the #1481 defect, so the API is narrowed until it cannot express it.
func (r *Runner) clearIterCancel()
```

Deterministic API narrowing, not another comment — a future re-split has to add a parameter
back before it can reintroduce the window. `setIterCancel` has exactly two callers today, both
in `Run`; nothing in the test files uses it.

### Deletions

- `liveArgs` — zero callers after the change. It **must** go: staticcheck's `unused` (part of
  `make check`) fails on an unexported method with no callers.
- `nextSpawnID` — zero production callers; its one test caller is retargeted (§ Test design).
  Deleting it rather than leaving it alive for its own test avoids the half-deletion-behind-
  test-usage shape that `staticcheck` cannot see.

### Why this closes both criteria

Let *S* be `beginSpawn`'s critical section. A racer takes `restartMu` exactly once and is
therefore serialised against *S*:

- **Racer before *S*.** It writes `sessionID`/`rotatePending` (or `r.args`) and reads whatever
  cancel the *previous* iteration left — nil, or a completed iteration's. *S* then reads the
  rotated id / swapped argv. **Branch (a): the spawn observes the rotation.** For `RestartFresh`
  that means `forceFirst` is true, so the argv is `--session-id <newID>` in first-run form.
- **Racer after *S*.** It reads the cancel *S* just published, and cancels the iteration ctx.
  **Branch (b): the spawn's iteration is torn down.** If the racer wins before `cmd.Start`,
  `exec.Cmd.Start` sees `c.ctx.Done()` already closed and returns `ctx.Err()` **without forking
  and without invoking `cmd.Cancel`** (verified in go1.26.2's `os/exec`, the `if c.ctx != nil {
  select { case <-c.ctx.Done(): return c.ctx.Err() … } }` guard, which sits above the fork and
  above any `cmd.Process` deref — so the reap-then-SIGTERM `cmd.Cancel` closure never runs with
  a nil `Process`). If it wins after `Start`, `cmd.Cancel` fires and the child is torn down.
  Either way the loop falls through to `drainRestart` and relaunches under the rotated
  id / swapped argv.

There is no third position: `restartMu` admits the racer either before or after *S*, and both
outcomes satisfy the invariant. The forbidden state — a live child under a pre-rotation id with
no live iteration cancel — required the racer to land *between* two sections, and there is now
only one.

### The `started == false` relay (branch (b), pre-`Start`)

`spawnAndWait` returns `(false, fmt.Errorf("streamsup: start: %w", ctx.Err()))`. Run then:

- calls `OnChildExit` (per-iteration contract, unchanged — it fires even when no child launched);
- finds the **parent** `ctx.Err()` nil, so it does not return;
- **leaves `firstRun` untouched** (`if started` gate), which is exactly right: `--session-id
  <newID>` never ran, so the rotated session is still unestablished and the retry must use
  first-run form again;
- `drainRestart()` sees the racer's `restartCh` token → `continue`, **no backoff, no
  `RestartCount++`**.

The next iteration's `beginSpawn` consumes `rotatePending` and builds `--session-id <newID>`.
This relay is the branch the fix newly depends on and § Test design asserts each link of it.

## Concurrency model

No new goroutines, no new channels, no new mutex. Lock inventory is unchanged: `mu` (stdin +
rotation gate), `stateMu` (control-plane snapshot), `restartMu` (live-restart seam).

Lock-order argument for the fused section:

- `beginSpawn` holds only `restartMu`. It calls `buildArgs` (pure) and stores a pointer. No
  channel op, no log call, no call-out. `restartMu` remains a leaf.
- `context.WithCancel` runs before the acquisition, so `restartMu` is never held while the
  context package takes a parent `cancelCtx`'s internal mutex.
- `Restart` / `RestartFresh` keep their release-before-`cancel()` ordering, so no path holds
  `restartMu` while a cancel propagates.
- `restartMu` is still never nested with `mu` or `stateMu`, and `Pool`/`Session` locks are still
  never reachable from it.

Critical-section width grows by one `buildArgs` allocation (a `make` + two `append`s over a
short argv). The section still contains no I/O and no blocking op.

## Error handling

| Failure | Behaviour |
|---|---|
| Racer lands before *S* | Rotation observed by the spawn being set up (branch (a)). No error path. |
| Racer lands after *S*, before `cmd.Start` | `Start` returns `ctx.Err()`; `started == false`; wrapped as `streamsup: start: …`; logged at Warn by the existing `"claude exited"` line; `firstRun` retained; immediate relaunch via `drainRestart`. |
| Racer lands after `cmd.Start` | Unchanged from today: `cmd.Cancel` reaps + SIGTERMs, `cmd.Wait` returns, `takeStdin` closes the handle, loop relaunches. |
| Parent ctx cancelled during setup | Unchanged: `WithCancel` yields an already-done child ctx, `Start` returns `ctx.Err()`, and the post-spawn `if ctx.Err() != nil` returns it. |
| `RestartFresh("")` | Unchanged Warn no-op; never reaches `restartMu`. |

No new error values, no new sentinel, nothing new crosses a package boundary.

## Testing strategy

`make check` must stay green (`go vet` + `-race` + staticcheck + substrate-guard + fake-daemon
e2e). Existing `internal/streamsup` tests must pass unmodified except the one retarget below.

### The deterministic hook

The window this ticket closes contains exactly one synchronous, interceptable statement: the
`"spawning claude"` log write. `Config.Logger` is an existing injection point and
`slog.Handler.Handle` runs **synchronously on the Run goroutine**, so a handler that fires a
rotation on that record lands the racer at a *provably* fixed point in the statement order —
no `time.Sleep`, no repetition, no window calibration.

- **On the mutant** (pre-fix shape) that point is strictly *inside* the window: the id was read
  by `nextSpawnID`, and `setIterCancel` has not run. The racer reads `iterCancel == nil`, misses
  both, and the spawn launches a live child under the pre-rotation id.
- **On the shipped tree** the log sits *after* `beginSpawn`, so the racer reads a live cancel →
  branch (b).

Add a small test-only wrapper (≈12 lines) around the existing `spawnArgsRecorder`: it delegates
`Handle` to the recorder, then, on a `"spawning claude"` record, fires a supplied hook exactly
once (`sync.Once`). Everything else (`Enabled`, `WithAttrs`, `WithGroup`, `count`, `all`)
promotes from the embedded recorder.

### Correlating "which children actually launched"

`cfg.onSpawn` fires on the Run goroutine after `cmd.Start` and after `setStdin`, and strictly
*after* the same iteration's `"spawning claude"` record was appended to the recorder. Run is
single-goroutine, so inside `onSpawn` the **last** entry of `rec.all()` is this child's argv.
That gives a race-free `launched [][]string` with no polling and no file. Under `-race` both
sides are mutex-guarded.

### Test 1 — `RestartFresh` racing spawn setup (AC1, AC3)

Scenario, as bullets; the developer writes the Go in the file's idiom:

- `helperRunCfg(t, "record_block", …)` — a long-lived child, so the launched child is stable and
  the outcome does not depend on crash-respawn churn.
- `cfg.Logger` = the wrapping handler; its hook calls `r.RestartFresh(rotatedSessionID)` once.
- `cfg.onSpawn` appends the last recorded argv to `launched` and signals a buffered channel.
- Wait for the first `onSpawn` signal (bounded by a generous failure deadline — a timeout as a
  *failure bound*, not a calibration), then `cancel()` + `join()`.

Assertions:

1. **Invariant (branch-agnostic, AC1).** `launched` is non-empty, and **every** entry carries
   `--session-id <rotatedSessionID>` and contains `testSessionID` nowhere. This is the criterion's
   forbidden outcome stated positively: no child ever ran under the pre-rotation id.
2. **Branch (b) mechanism (pins the prescribed statement order).** `rec.count() == 2` while
   `len(launched) == 1` — spawn 1 was attempted and launched nothing. If a later change moved the
   log ahead of `beginSpawn`, the racer would take branch (a) and this assertion is what flags it;
   assertion 1 would still pass, which is correct, since AC1 permits both branches.
3. **`started == false` did not advance `firstRun`.** Spawn 2 uses `--session-id`, not `--resume`
   (reuse `idFlagValue` / `idFlagCount`; exactly one id flag per spawn).
4. **No backoff was taken.** `r.State().RestartCount == 0` — the immediate-relaunch path
   (`drainRestart` → `continue`) skips the only site that increments it.

Mutant behaviour: the racer misses both, spawn 1's child launches under `testSessionID` and
blocks, `onSpawn` fires for it → assertion 1 fails immediately (no timeout wait), and 2 and 3
fail alongside it.

### Test 2 — `Restart(args)` racing spawn setup (AC2)

Same shape, one substitution: the hook calls `r.Restart([]string{"--model", "sonnet-1481-test"})`
once. `helperChild` dispatches on env, not argv, so an extra pass-through flag is inert.

Assertions:

1. **Invariant (AC2).** Every entry of `launched` contains `--model` followed by
   `sonnet-1481-test`. No child ever ran under the pre-swap argv.
2. `rec.count() == 2`, `len(launched) == 1`, `r.State().RestartCount == 0` — the same branch-(b)
   relay as Test 1.
3. Exactly one id flag on each recorded spawn (`--session-id`, since `started == false` retains
   first-run form).

Mutant behaviour: `buildArgs` captured the old argv before the log, so spawn 1's child launches
without `--model` and blocks → assertion 1 fails.

### Test 3 — retarget `TestRunner_RestartFresh_EmptyIDIsNoOp`

Its `nextSpawnID` call is deleted. Replace with `beginSpawn(context.Background(), true)`; call
the returned `cancel` (defer it); assert `forceFirst == false` and that `args` carries
`--session-id <testSessionID>` with no `--resume`. Strictly a stronger assertion than before —
it checks the argv the spawn would actually use. Not a discriminating test; green on both trees.

### The mutation runs (AC4)

Both trees are run and both results go in the PR. No worktree writes:

```bash
BASE=$(git merge-base origin/main HEAD)
MUT=$(mktemp -d)
git show "$BASE:internal/streamsup/runner.go" > "$MUT/runner.go"
# Append a beginSpawn SHIM so the new test file compiles against the pre-fix runner.
# It reproduces the split shape exactly: nextSpawnID (acquisition 1), released, then
# setIterCancel (acquisition 2).
cat >> "$MUT/runner.go" <<'EOF'
func (r *Runner) beginSpawn(ctx context.Context, firstRun bool) (context.Context, context.CancelFunc, []string, bool) {
	sessionID, forceFirst := r.nextSpawnID()
	if forceFirst {
		firstRun = true
	}
	args := buildArgs(r.liveArgs(), firstRun, sessionID)
	iterCtx, cancel := context.WithCancel(ctx)
	r.setIterCancel(cancel)
	return iterCtx, cancel, args, forceFirst
}
EOF
printf '{"Replace":{"%s/internal/streamsup/runner.go":"%s/runner.go"}}\n' "$PWD" "$MUT" > "$MUT/overlay.json"

go test -overlay="$MUT/overlay.json" -race -count=1 \
  -run 'TestRunner_(RestartFreshRacesSpawnSetup|RestartRacesSpawnSetup|RestartFresh_EmptyIDIsNoOp)' \
  ./internal/streamsup/                                        # expect FAIL (RED)
go test -race -count=1 \
  -run 'TestRunner_(RestartFreshRacesSpawnSetup|RestartRacesSpawnSetup|RestartFresh_EmptyIDIsNoOp)' \
  ./internal/streamsup/                                        # expect ok (GREEN)
```

The mutant's `Run` loop is the base commit's verbatim — it never calls the shim; the shim exists
only so Test 3 compiles. The shim is unused by production code in the mutant, which is fine:
`go test` does not run staticcheck. **Record both invocations and their full output in the PR
body.** A green on the unmodified tree alone is not evidence.

Sanity check before believing a RED: confirm the mutant tree still *builds* (a compile error
also "fails"). `go vet -overlay="$MUT/overlay.json" ./internal/streamsup/` should be clean.

### Documentation (AC5)

Four sites in `docs/knowledge/features/streamsup-package.md`. The AC names three; the fourth is
the same class of rot (a symbol name that ceases to exist) and is one word.

1. **§ "Supervise loop (`Run`)" pseudo-code sketch.** Today it omits `nextSpawnID` entirely and
   still shows `buildArgs(liveArgs(), firstRun, SessionID)` — already stale before this ticket.
   Rewrite the three affected sketch lines to show `beginSpawn(ctx, firstRun)` returning
   `(iterCtx, cancel, args, forceFirst)`, the `forceFirst → firstRun = true` re-arm, the log
   *after* the section, and `cancel(); clearIterCancel()` on teardown. Follow the block with one
   sentence naming the atomicity and why it is load-bearing: the id/argv read and the
   `iterCancel` publish are one section so a racing `Restart`/`RestartFresh` is serialised either
   fully before (the spawn observes it) or fully after (it cancels the spawn) — never between.
2. **§ "Fresh-restart under a new id (#1124)".** The "`Run`'s spawn loop reads `nextSpawnID()` …"
   sentence. Replace the accessor name with `beginSpawn()` and widen the description: the section
   snapshots `(sessionID, rotatePending, args)`, clears `rotatePending`, builds the argv **and**
   publishes `iterCancel` — one acquisition, because splitting it is #1481.
3. **§ "Live-restart seam".** The "`args` (…, read via `liveArgs()`), `iterCancel` (…, published
   via `setIterCancel` each iteration)" clause. Replace with: `args` and the id pair are read, and
   `iterCancel` is published, by `beginSpawn` in one section; `clearIterCancel` drops it after the
   iteration ends. State that the no-argument shape of `clearIterCancel` is deliberate — the API
   cannot express a publish outside `beginSpawn`.
4. **See-also list**, the `codebase/1124.md` line: `(RestartFresh/nextSpawnID)` →
   `(RestartFresh/beginSpawn)`.

Leave `docs/specs/architecture/*.md` and `docs/knowledge/codebase/*.md` alone — those are frozen
per-ticket build artifacts describing the tree as of their own ticket. Leave
`docs/knowledge/architecture/system-overview.md` alone too: its `setIterCancel` mention is
`internal/supervisor`'s (the PTY path), which this ticket does not touch. Do **not** write
`docs/knowledge/codebase/1481.md` — the documentation phase owns it. Do not edit
`docs/knowledge/INDEX.md`.

Run `qmd update && qmd embed` after the doc edit.

## Open questions

1. **Should `beginSpawn` also return `sessionID`?** This spec says no — nothing outside the log
   line needs it, and the log prints `args`, which contains it. If the developer finds a
   consumer, adding a fifth return is fine; keeping `buildArgs` inside the section is not
   negotiable (reentrancy).
2. **`record_block` vs `crash` for the fake child.** This spec picks `record_block` because a
   long-lived child makes "which children actually launched" a stable set. `crash` would also
   discriminate (the mutant's stale-id child launches either way), but its 20 ms respawn churn
   makes `rec.count()` assertions racy. If `record_block`'s 30 s fallback proves awkward under
   `-race` on a loaded CI box, `block_sigterm` is the equivalent alternative.
3. **Does Test 1's assertion 2 over-constrain?** It pins the *prescribed* statement order (log
   after the section), not AC1, which permits both branches. Kept because branch (b) is the path
   the fix newly relies on and the technical notes ask for it explicitly. If a reviewer objects,
   the fix is to demote it to a comment — not to weaken assertion 1.
