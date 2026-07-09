# Spec #864 — ptyrunner: reap claude's descendant process groups on budget-hit and watchdog teardowns

## Files to read first

- `internal/agentrun/ptyrunner/reap.go` (whole file, ~125 LoC) — `reapDescendantGroups(rootPid, logger)` (the helper you wire, **unchanged**) and `descendantPGIDs`. Note the three load-bearing guards (`pgid<=1`, `pgid==Getpgrp()`, `pgid==rootPid`) and that the walk enumerates *descendants of `rootPid`* — a dead claude reparents its Bash group to init, so the walk only sees the orphan while claude is **still alive**. This is why ordering matters below.
- `internal/agentrun/ptyrunner/runner.go:294-320` — `cmd` construction, the existing `cmd.Cancel` reap hook (operator-SIGTERM path), and `cmd.WaitDelay`. The reap-before-SIGTERM ordering here is the pattern the budget path must mirror.
- `internal/agentrun/ptyrunner/runner.go:370-386` — `tuidriver.Spawn` success check and the `defer sess.Close()` registration. `cmd.Process` is non-nil only after this point; the new trailing reap defer registers immediately after this defer.
- `internal/agentrun/ptyrunner/runner.go:440-502` — the defer-LIFO teardown chain (`runCtx`/`cancel`, emitter, counter, watchdog goroutine, `wg.Wait`). The budget `Terminate` hook is at ~476-493; `runWatchdog` is spawned at ~497-500. Read the LIFO doc comment at 467-470 — you will extend it to mention the reap.
- `internal/agentrun/ptyrunner/watchdog.go` (whole file, 39 LoC) — `runWatchdog` cancels `runCtx` and does **not** signal claude; claude is reached only later via `sess.Close()`. This is why a trailing defer (not an in-`runWatchdog` reap) is the chokepoint for the watchdog path.
- `internal/agentrun/budget/budget.go:106-166` — `Counter.OnEvent` calls `cfg.Terminate()` **synchronously** on the caller's goroutine (the Run event loop, `runner.go:554`). Confirms the in-hook reap runs on the Run goroutine, and claude is alive (just finished a turn) when `Terminate` fires.
- `internal/agentrun/ptyrunner/runner_test.go:50-77` — `helperRunCfg` harness. `:569-598` — `TestRun_MaxTurnsExhaustion_NoBenignWarns` (mode `jsonl_exit143`, `MaxTurns:1`, the budget-hit harness to clone). `:600-633` — `TestRun_WatchdogFires` (mode `jsonl`, `WatchdogTick`, the watchdog harness to clone). `:22-38` — `loggerSyncWriter`, the mutex-guarded test-double pattern to mirror for the reap recorder.
- `internal/agentrun/ptyrunner/reap_test.go:26-102` — `TestReapDescendantGroups` and its four subtests (`ReapsDescendantGroupSparesCaller`, `NoDescendantsIsNoOp`, `ExcludesRootOwnGroup`, `ReapsGrandchildGroupSparesRoot`). These already assert the three guards (AC-4). **They stay unchanged** — reap.go is unchanged — and continuing to pass *is* AC-4's evidence. Do not add new guard tests.
- `docs/knowledge/codebase/565.md` § "Out of scope / known same-shape gaps" — this ticket is the documented promotion of the deferred budget/watchdog gap. Confirms the one-line reap drop-in and the "reap before self-signal" requirement.

## Context

#565 made ptyrunner reap claude's detached Bash process groups but scoped the reap to the operator-cancel path only (`cmd.Cancel`), which fires *only* when the parent ctx passed to `CommandContext` is cancelled (operator SIGTERM/SIGINT). The two other teardown paths that kill claude mid-work never cancel the parent ctx, so their reap never runs and the Bash group they leave behind reparents to init unbounded:

- **Budget hit** — the budget `Counter`'s `Terminate` hook (`runner.go:478`) cancels `runCtx` and SIGTERMs claude directly.
- **Watchdog fire** — `runWatchdog` (`watchdog.go`) cancels `runCtx`; teardown then reaches claude via the deferred `sess.Close()` SIGTERM.

The wedge corpus shows most abnormal teardowns kill claude mid tool-loop (last JSONL entry `tool_use`) — exactly the in-flight-Bash moment — so the highest-frequency abnormal path is the one that currently leaks. This is the anticipated follow-up to #565, not a regression.

## Design

Confined to `internal/agentrun/ptyrunner`. **No changes to `reap.go`'s logic** (`reapDescendantGroups`/`descendantPGIDs` are already correct and content-blind); the helper is only wired into two additional sites. No new exported symbols, no new deps.

### The two teardown paths need different chokepoints

The reap must observe claude's descendant tree **while claude is still alive**, because `descendantPGIDs` walks descendants of claude's pid; once claude exits, the Bash group reparents to init (ppid=1) and the walk misses it. The two new paths differ in when claude gets signalled:

| Path | Who signals claude | Chokepoint | Why |
|------|--------------------|------------|-----|
| Operator cancel (existing) | `cmd.Cancel` (reap → SIGTERM) | `cmd.Cancel` (unchanged) | Reap already runs before its own SIGTERM. |
| **Budget hit** | the `Terminate` hook itself (SIGTERM) | **inside the `Terminate` hook, before its SIGTERM** | A trailing defer would run *after* the hook's SIGTERM — claude may already be exiting and the orphan reparented. Mirror #565's `cmd.Cancel` reap-before-signal ordering. |
| **Watchdog fire** (and normal end-of-turn) | nobody until `sess.Close()` | **a trailing `defer` reap, ordered before `sess.Close()`** | `runWatchdog` only cancels `runCtx`; claude stays wedged-but-alive until `sess.Close()`. A defer that fires before `sess.Close()` reaps a live tree. Same defer also covers a clean end-of-turn run whose claude launched a never-returning background command. |

### Test seam

Introduce a single package-var indirection in `reap.go` so unit tests can observe the reap firing on the budget/watchdog paths without standing up a real descendant process tree:

```go
// reapDescendantGroupsFn is the seam the teardown wiring calls so tests can
// observe the reap firing without a real process tree. Production points it at
// reapDescendantGroups; tests swap it (non-parallel) and restore via t.Cleanup.
var reapDescendantGroupsFn = reapDescendantGroups
```

**All three** reap call sites (existing `cmd.Cancel`, new budget hook, new trailing defer) call `reapDescendantGroupsFn(cmd.Process.Pid, logger)` rather than the function directly. Routing `cmd.Cancel` through the seam too is byte-identical in production (the var defaults to the real function) and keeps the indirection uniform — no partially-seamed path. This does not change any observable operator-path behaviour (AC-3).

### Wiring changes in `runner.go`

1. **Budget `Terminate` hook** (~478) — add the reap as the first statement, before `cancel()` and before the SIGTERM, so the guaranteed-live tree is reaped before any teardown begins:
   - `reapDescendantGroupsFn(cmd.Process.Pid, logger)` → then existing `SetExitReason` / `RecordTransition("budget-hit")` / `cancel()` / `return cmd.Process.Signal(syscall.SIGTERM)`. (Exact statement order among the existing three is unchanged; the reap slots in ahead of the SIGTERM — placing it first is cleanest.)

2. **Trailing reap defer** — register immediately after the existing `defer sess.Close()` (runner.go:378-386), so LIFO fires the reap **before** `sess.Close()`:
   - `defer func() { reapDescendantGroupsFn(cmd.Process.Pid, logger) }()`
   - `cmd.Process` is non-nil here (Spawn succeeded). Resulting fire order at teardown: `…→ wg.Wait → counter.Stop → emitter.Close → cancel → [reap] → sess.Close → recording-finalize`. On the watchdog path claude is unsignalled-but-alive at `[reap]` (nothing between `wg.Wait` and the reap touches claude), so the walk sees a live tree.

3. **`cmd.Cancel`** (313-316) — change its `reapDescendantGroups(...)` call to `reapDescendantGroupsFn(...)`. No other change.

4. **Comments** — extend the LIFO doc comment (467-470) and the `sess.Close` region to name the reap defer and warn against reordering it (it must stay registered right after `sess.Close` so it fires before Close's SIGTERM). Add a one-line comment at the budget hook noting the reap-before-self-SIGTERM ordering mirrors `cmd.Cancel`/#565.

### Redundant-reap behaviour (intended)

- On the **budget** path the trailing defer *also* fires later (claude likely dead by then → walk empty). Harmless — the in-hook reap already did the work with a live tree.
- On the **operator** path `cmd.Cancel` reaps the live tree; the trailing defer fires later (claude SIGTERM'd → walk empty). Harmless.
- Both are the AC-3 "reap runs twice, second is a no-op" case; `reapDescendantGroups` already tolerates `ESRCH` / already-exited groups.

## Concurrency model

No new goroutines. Reap call sites and the goroutines they run on:

- **Budget in-hook reap** — runs on the Run event-loop goroutine (`Counter.OnEvent` → `Terminate()` is synchronous, `budget.go:153`).
- **Trailing defer reap** — runs on the Run goroutine during deferred teardown, after `wg.Wait()` has joined the watchdog goroutine.
- **`cmd.Cancel` reap** — runs on the os/exec ctx-watcher goroutine (operator path only; not exercised by the budget/watchdog tests).

`reapDescendantGroups` is already documented safe to call from any goroutine (reads only its arguments, shells out to `ps`, issues kills — no shared state), so a concurrent operator-cancel + trailing-defer overlap in production is safe. On the budget path the two reaps are sequential on one goroutine.

Teardown-latency note: the trailing defer now runs a `ps` snapshot on **every** Run teardown (all paths), not only abnormal ones. Cost is one `ps` (~tens of ms, bounded by `reapPSTimeout`=2s) once per run — acceptable. On a clean run claude's descendant set is normally empty, so no kills and no `Info` log.

## Error handling

Unchanged from #565: the reap is best-effort and content-blind. Enumeration/kill failures log at `Warn` (pids/pgids only) and do not propagate; claude still gets its SIGTERM on every path regardless. `ESRCH` (group exited in the teardown window) is benign and skipped. The three guards (`pgid<=1`, own group, `rootPid` group) prevent SIGKILLing init, pyry's own group, or claude's own group.

## Testing strategy

`reap.go`'s guard tests (`reap_test.go`) are unchanged and their continued passing is AC-4. Two new unit tests in `runner_test.go`, each cloning the sibling behavioural test's harness. **Both must be non-parallel** (they mutate the `reapDescendantGroupsFn` package var). Go runs non-parallel tests to completion — including `t.Cleanup` restore — before parked `t.Parallel()` tests resume, so the swap window never overlaps the existing parallel `TestRun_*` tests; do not add `t.Parallel()` to these two.

Recorder double: mirror `loggerSyncWriter` — a mutex-guarded struct recording each call's `rootPid` (a slice or a count + last-pid). Swap `reapDescendantGroupsFn` to a closure that records then returns; restore the original via `t.Cleanup`.

- **`TestRun_MaxTurnsExhaustion_ReapsDescendantGroups`** — clone `helperRunCfg(t, "jsonl_exit143", …, noEotBody)`, `MaxTurns: 1`. Swap the seam. Run. Assert `Run` returns nil and the recorder captured **≥1** call with `rootPid > 1` (the in-hook reap fired on the budget-hit path; the trailing defer may add a second call — assert ≥1, not ==1). The test can't know claude's pid, so assert `pid > 1`, not an exact value.
- **`TestRun_WatchdogFires_ReapsDescendantGroups`** — clone the `TestRun_WatchdogFires` config (mode `jsonl`, `MaxTurns:10`, `WatchdogTick`/`WatchdogTrackerOpts`/`PromptCommitTimeout` as in the original). Swap the seam. Run. Assert `Run` returns nil and the recorder captured **≥1** call with `rootPid > 1` (the trailing defer fired on the watchdog-fire path).

Both assert the *wiring* fires with a plausible pid — not that a real tree is killed (that is `TestReapDescendantGroups`' job, unchanged). The fake-claude `jsonl`/`jsonl_exit143` helper spawns no descendant groups, so the swapped recorder-reap is the only observation needed.

Run `go test -race ./internal/agentrun/ptyrunner/...` and `go vet ./...`.

## Open questions

- **New sibling tests vs. modify the two existing tests in place.** The spec recommends *new* non-parallel tests so the existing behavioural assertions (trailer subtype, terminal-reason, no-benign-warns) stay parallel and untouched. Modifying the existing two in place (drop `t.Parallel()`, add the recorder) is also viable and slightly less code, but couples two concerns and de-parallelises two tests. Developer's call; new siblings preferred.
- **Whether to assert exact call count.** Spec says assert `≥1` because the trailing defer can add a redundant call on the budget path. If the developer wants a tighter assertion, count is a valid signal but couples the test to the redundant-reap detail; `≥1` is the robust choice.
