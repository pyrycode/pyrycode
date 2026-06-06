# Spec: ptyrunner reaps claude's descendant process groups on SIGTERM (#565)

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:288-360` — the spawn site (`exec.CommandContext` → `tuidriver.Spawn`) and the `defer sess.Close()` registration. The fix sets `cmd.Cancel`/`cmd.WaitDelay` between `EnsureClaudeEnv(cmd)` (line 294) and `tuidriver.Spawn` (line 344). **This is the only production wiring change.**
- `internal/agentrun/streamrunner/runner.go:181-184` — the exact `cmd.Cancel = SIGTERM` + `cmd.WaitDelay = killGrace` pattern to mirror, and `streamrunner/runner.go:41-44` for `killGrace = 5 * time.Second`.
- `internal/agentrun/ptyrunner/runner.go:450-466` — the budget `Terminate`/`Kill` hooks. Confirms the budget-hit teardown path is **separate** from operator-SIGTERM (it signals claude directly and does not cancel the parent ctx). Drives the "Open questions" scope note.
- `internal/supervisor/spawn.go:38-52` — `SpawnPTY` shows the same `cmd.Cancel`/`cmd.WaitDelay` shape already in the codebase (`spawnWaitDelay = 5s`); the precedent that this is the house pattern for graceful PTY teardown.
- `internal/e2e/realclaude/sigterm_mid_tool_use_test.go` — the verification test. Key regions: doc-comment carve-out at lines **11-13, 39-43**; `waitForBashSubprocess` returning `bashPGID` (lines 158, 348-362); the self-reap `t.Cleanup` at **167-169**; invariant-1 comment + assertion at **219-227**; the four post-exit invariants at 229-281. AC #3/#4 edit this file.
- `internal/agentrun/ptyrunner/helper_test.go` — existing `TestHelperProcess`/helper-mode scaffold (modes like `jsonl_exit143`); the unit test extends it with one new "spawn child in a fresh process group and block" mode rather than building fresh scaffolding.
- tui-driver `Session.Close()` (`github.com/pyrycode/tui-driver@v1.0.1/pkg/tuidriver/session.go:331-362`) — **read-only context.** Close already does SIGTERM → 3s grace → SIGKILL on claude. This 3s grace is the effective bounded-exit backstop and the reason `cmd.WaitDelay`'s exact value is non-binding (see Concurrency model).

## Context

`pyry agent-run` (ptyrunner, the default path) leaks claude's in-flight Bash subprocess when the operator sends SIGTERM. claude (2.1.158) runs every Bash command in a **detached process group two levels below pyry**:

```
pyry        (pid P, pgid P)
 └─ claude  (pid C, pgid C)        ← pyry's direct child; own group (pty.Start does setsid)
     └─ zsh -c eval '<cmd>'        (pid Z, pgid Z)   ← claude's detached spawn; NEW group
         └─ tail -f /dev/null      (pgid Z)
```

pyry reaps claude (its direct child) but not group `Z`. On operator SIGTERM the `zsh`+`tail` group survives, reparented to init. For a command that never returns (`tail -f /dev/null`) the orphan is unbounded.

### Why the cheap fix does not work — measured, not assumed

The ticket offered two directions: **(a)** mirror streamrunner's SIGTERM grace so claude reaps its own child, or **(b)** track and kill claude's descendant groups. I ran the ticket's repro (`/tmp/sigterm_probe.sh`) against the current binary and against a throwaway build with direction (a) applied (`cmd.Cancel = SIGTERM` + `cmd.WaitDelay`). Both runs, live claude 2.1.158, default ptyrunner path:

| Build | claude (direct child) | `zsh`+`tail` group | pyry exit |
|---|---|---|---|
| current (os/exec default → SIGKILL claude) | reaped | **orphaned** | ~200 ms |
| (a) graceful SIGTERM + grace | reaped (clean, `num_turns:2`) | **still orphaned** | ~700 ms |

**Conclusion: direction (a) is insufficient.** claude, even given a graceful SIGTERM with grace, exits cleanly but does **not** kill the Bash subprocess it deliberately isolated into its own group. Relying on claude to clean up is a stochastic dependency that does not hold — so per *belt-and-suspenders means different fabric*, the guarantee must come from deterministic pyry code. This spec implements direction (b): **pyry walks claude's descendant tree at teardown and kills the orphan-able process groups itself.** Direction (a)'s graceful SIGTERM is still adopted — not for reaping, but so claude flushes its session JSONL cleanly (invariant 2) instead of dying mid-write under SIGKILL.

### The mechanism that makes the bug, and the chokepoint that fixes it

`runAgentRun` builds `ctx` via `signal.NotifyContext(ctx, SIGTERM, SIGINT)` (`cmd/pyry/agent_run.go:251`) and threads it to `ptyrunner.Run` → `exec.CommandContext(ctx, …)`. ptyrunner — unlike streamrunner — never overrides `cmd.Cancel`, so on operator ctx-cancel the os/exec watcher fires the **default `Kill()` (SIGKILL)** on claude immediately, preempting tui-driver's graceful `Close()`. Overriding `cmd.Cancel` both restores the graceful path **and** gives us the one teardown moment where **claude and its whole descendant tree are guaranteed alive and not-yet-signalled** — the race-free place to walk and reap.

## Design

Two parts, both confined to `internal/agentrun/ptyrunner`.

### Part 1 — new file `internal/agentrun/ptyrunner/reap.go`

A content-blind, best-effort descendant-process-group reaper. No new exported symbols.

```go
// reapDescendantGroups kills every process group that contains a descendant
// of rootPid, except the caller's own group, rootPid's own group, and the
// init/invalid group (pgid <= 1). Best-effort: enumeration or signal errors
// are logged at Warn (pids/pgids only) and do not propagate. Content-blind —
// it reads pid/ppid/pgid triples only, never process command lines.
func reapDescendantGroups(rootPid int, logger *slog.Logger)

// descendantPGIDs returns the distinct process-group ids of every transitive
// child of rootPid, discovered from a single `ps -axo pid=,ppid=,pgid=`
// snapshot. Bounded by a short context timeout so a hung ps cannot wedge
// teardown. Empty on enumeration failure.
func descendantPGIDs(ctx context.Context, rootPid int) (map[int]struct{}, error)
```

Behavior contract for `reapDescendantGroups`:
- Snapshot the process table once via `exec.CommandContext(ctxTimeout, "ps", "-axo", "pid=,ppid=,pgid=")` (verified portable on this darwin host and on Linux procps; `ctxTimeout` ≈ 2s). Parse each line into `(pid, ppid, pgid)`.
- BFS from `rootPid` over the `ppid → children` map to collect descendant pids, then collect their distinct `pgid`s.
- For each candidate pgid, **skip** it if `pgid <= 1`, `pgid == syscall.Getpgrp()` (pyry's own group — never suicide), or `pgid == rootPid` (claude is a session/group leader so its pgid == its pid; never kill claude's own group — `sess.Close()` owns claude's teardown). The self-group and `pgid<=1` guards are the load-bearing safety checks; getting them wrong SIGKILLs pyry or init.
- `syscall.Kill(-pgid, syscall.SIGKILL)` each surviving pgid (negative pid = whole group). SIGKILL, not SIGTERM-then-grace: these are abandoned tool commands whose output is already discarded (no `tool_result` will be sent), so there is no graceful-completion value and the AC wants a hard "no longer running" guarantee. `ESRCH` is benign (group already gone) — do not Warn on it; reuse the existing `agentrun.ExitErrIsBenign` notion if convenient, or just ignore `ESRCH`.
- Log one Info/Debug summary line with the count and the pgids reaped (numbers only — no command strings ever cross into a log line, preserving the package's logging discipline).

### Part 2 — wire it into `Run` (`runner.go`, ≈6 lines)

Immediately after `tuidriver.EnsureClaudeEnv(cmd)` (line 294) and **before** `tuidriver.Spawn(cmd, …)` (so the hook is installed before the os/exec watcher is armed at Start):

```go
cmd.Cancel = func() error {
    reapDescendantGroups(cmd.Process.Pid, logger) // kill claude's orphan-able Bash group(s)
    return cmd.Process.Signal(syscall.SIGTERM)     // then graceful SIGTERM (lets claude flush)
}
cmd.WaitDelay = killGrace // mirror streamrunner; sess.Close()'s 3s grace is the binding backstop
```

`syscall` and `time` are already imported. Reuse a 5s grace constant matching `streamrunner.killGrace` / `supervisor.spawnWaitDelay` (define a local `const killGrace = 5 * time.Second` in the ptyrunner package, or inline — the exact value is non-binding, see below).

### Why this is the whole change

- `cmd.Cancel` fires **only** when the ctx passed to `CommandContext` is cancelled — i.e. operator SIGTERM/SIGINT, exactly the AC scenario. It does **not** fire on normal completion, budget-hit, or watchdog-fire (none of those cancel the parent ctx), so those paths are byte-for-byte unchanged. Minimal blast radius.
- At `cmd.Cancel` time claude is alive and unsignalled, so the `ps` snapshot sees the live `pyry → claude → zsh → tail` tree. The walk is **race-free** — it is not a best-effort "hope the tree is still there at defer time" scan.
- claude's direct-child reap is unchanged in outcome (still gone after pyry exits) — now via graceful SIGTERM (+ `sess.Close` / `WaitDelay` SIGKILL backstop) instead of immediate SIGKILL.

### Data flow (operator SIGTERM)

```
operator SIGTERM → pyry → NotifyContext cancels ctx
        │
        ├─ os/exec watcher fires cmd.Cancel:
        │     reapDescendantGroups(claudePid):  ps snapshot → BFS → kill -Z (SIGKILL)   ← orphan gone
        │     SIGTERM → claude                                                          ← graceful
        │
        └─ Run event loop sees runCtx done → returns nil → defers LIFO →
              … → sess.Close(): SIGTERM claude, ≤3s grace, SIGKILL, close PTY           ← claude reaped
```

## Concurrency model

- `cmd.Cancel` runs on the os/exec watcher goroutine. It reads only `cmd.Process.Pid` (immutable after Start) and `logger` (concurrency-safe). It shells out to `ps` and issues `syscall.Kill`s — no shared mutable state, no locks needed.
- The reap is synchronous inside `cmd.Cancel` and bounded by the `ps` context timeout (~2s) plus a handful of `Kill` syscalls (sub-ms). It completes well before claude finishes its ~700 ms graceful exit, so it adds no meaningful teardown latency.
- **Bounded-exit contract (AC #2).** Two SIGKILL backstops exist after the SIGTERM: tui-driver `sess.Close()`'s 3s grace and os/exec's `cmd.WaitDelay`. `sess.Close()`'s 3s fires first and is the binding bound, so pyry exits within ~3s worst case (measured ~700 ms in the happy path) — inside the test's 5s window — regardless of the exact `cmd.WaitDelay`. Keep `WaitDelay ≥ 3s` so os/exec does not preempt `sess.Close`'s graceful path; 5s (streamrunner parity) is fine.

## Error handling

- **`ps` enumeration fails / times out** → `descendantPGIDs` returns empty + error; `reapDescendantGroups` logs Warn (no content) and returns. claude still gets the SIGTERM. Degrades to "claude reaped, descendants possibly leaked" only in the (extremely unlikely) core-tool-failure case. Acceptable best-effort; noted in Open questions.
- **`syscall.Kill(-pgid)` returns `ESRCH`** → benign (group already exited in the grace window); ignore, no Warn.
- **No Bash command in flight at SIGTERM** → walk finds no descendant groups (or only claude's own, which is excluded); reap is a no-op. Correct.
- **Self-protection** → `pgid == Getpgrp()`, `pgid == rootPid`, `pgid <= 1` are hard skips. These three guards are the difference between "kill the orphan" and "SIGKILL pyry or init." The unit test must assert the self-group guard explicitly.

## Testing strategy

### Unit test — `internal/agentrun/ptyrunner/reap_test.go` (CI-runnable, `go test -race`)

Extend `helper_test.go` with one new `TestHelperProcess` mode that spawns a grandchild in a **fresh process group** (`SysProcAttr{Setpgid: true}`) and blocks (e.g. reads stdin forever). Scenarios (bullet form — developer writes them in the project's table-driven idiom; liveness probed with `syscall.Kill(pid, 0)` / `Kill(-pgid, 0)` polled to a deadline, mirroring the e2e's `waitForProcessGone`):

- **Reaps a descendant group.** Spawn a child with `Setpgid: true` that blocks. Call `reapDescendantGroups(os.Getpid(), …)`. Assert the child's group is gone (`Kill(-childPgid, 0)` → `ESRCH`) within a short deadline.
- **Does not kill the caller's own group (suicide guard).** After the reap above, assert the test process itself is still alive (it must survive to run assertions) and that a sibling helper left in the **test's own** group (not a fresh group) is untouched.
- **No-op when there are no descendants.** Call `reapDescendantGroups(pidWithNoChildren, …)`; assert it returns without error and kills nothing.
- **`rootPid`'s own group is excluded.** A child left in the *same* group as `rootPid` (no `Setpgid`) is not killed (covers the `pgid == rootPid` guard).

This is the load-bearing CI net; the realclaude e2e covers the real multi-level claude tree but is build-tagged out of normal CI.

### Optional wiring assertion — `runner_test.go`

If cheap with the existing fake-claude seams, assert `cmd.Cancel`/`cmd.WaitDelay` are non-nil/set on the spawned cmd. Low value (the e2e is authoritative); skip if it requires new scaffolding.

### Strengthen the realclaude e2e — `sigterm_mid_tool_use_test.go` (AC #3 + #4)

- **Add the reap assertion (AC #3 core).** After the existing invariant-1 check (claude direct-child gone, line 223), add: the Bash subprocess group `bashPGID` (already captured by `waitForBashSubprocess`) is gone once pyry has exited — poll `syscall.Kill(-bashPGID, 0)` to `ESRCH` with a short deadline (a `waitForGroupGone(bashPGID, …)` sibling of `waitForProcessGone`, or reuse the latter on the `tail` pid). This is the assertion that fails on `main` today and passes after the fix.
- **Demote the self-reap to defense-in-depth (AC #3).** Keep the `t.Cleanup(func(){ syscall.Kill(-bashPGID, SIGKILL) })` at lines 167-169 but reframe its comment: it is now a belt-and-suspenders safety net for the case where the production reap regresses, not the mechanism under test. (The new positive assertion runs *before* this cleanup, so a regression is caught, not masked.)
- **Remove the carve-out (AC #3).** Rewrite the doc-comment so #565 is no longer "out of scope": delete the parenthetical at lines 11-13 ("claude's OWN Bash subprocesses … tracked by #565 and is out of scope here. The test reaps the leaked subprocess itself …"), update invariant 1's wording at 219-222, and update the "Subprocess detection" paragraph (39-43) so `bashPGID` is described as the group the test now *asserts reaped* (not merely reaps in cleanup). Invariant 1 becomes: pyry reaps the full claude subtree — direct child **and** the Bash subprocess group.
- **AC #4 — existing invariants unchanged.** Invariants 2 (JSONL ends at a complete envelope boundary), 4a (Bash `tool_use` present), 4b (no matching `tool_result`) keep their current assertions. The graceful SIGTERM (vs the prior SIGKILL) makes invariant 2 *more* robust, not less — claude now flushes before exiting.

## Open questions

- **`cmd.WaitDelay` value.** Recommended 5s for streamrunner/`spawnWaitDelay` parity; non-binding because `sess.Close()`'s 3s grace dominates. Developer may prefer to align it to 3s to make the single binding bound explicit. Either passes AC #2.
- **Budget-hit and watchdog-fire teardown paths.** These tear claude down without cancelling the parent ctx (budget `Terminate` signals claude directly; watchdog cancels only `runCtx`), so `cmd.Cancel` does not fire and they retain the same structural descendant leak. **Out of scope** per the AC (operator SIGTERM only) and *evidence-based fix selection* (unobserved). If a future ticket observes a budget/watchdog-time orphan, the same `reapDescendantGroups(cmd.Process.Pid, logger)` call can be added to the budget `Terminate` hook (`runner.go:452`) — a one-line addition reusing this helper. Flag this in the codebase doc as a known same-shape gap, not a regression.
- **`ps` as the enumeration mechanism.** Chosen for one portable implementation across Linux + macOS (no `//go:build` split, no cgo, no new dependency), consistent with the codebase's shell-out precedent (`internal/sessions/rotation/probe_darwin.go` shells to `lsof`; this test already shells to `ps`/`pgrep`). A `/proc`-based Linux fast path is not worth the platform split for a once-per-shutdown call. Revisit only if a hardened-PATH environment makes `ps` unavailable.
