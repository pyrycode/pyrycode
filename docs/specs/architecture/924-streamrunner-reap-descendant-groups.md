# Spec #924 — streamrunner reaps claude's detached Bash process groups on teardown

**Ticket:** #924 (split from #915; wire-consumer child of the #923 reaper lift, which has landed).
**Size:** S. **Security-sensitive:** No (process-lifecycle/reliability; claude's OWN local Bash
groups; no untrusted input, no network surface, no crypto — same class as the hang-timeout work).

## Context

`agentrun.ReapDescendantGroups` (shipped by #923, `internal/agentrun/reap.go`) SIGKILLs claude's
detached descendant Bash process groups on teardown, sparing pyry's own group, claude's own group,
and init. The **ptyrunner** (default path) already consumes it via a `reapDescendantGroupsFn` seam
wired into `cmd.Cancel` (#565). The **streamrunner** (the `PYRY_USE_STREAMJSON=1` rollback path,
kept indefinitely per the #914/2026-05-19 decision) has **no equivalent** — its `cmd.Cancel` only
SIGTERMs claude itself. A dispatcher wall-clock SIGTERM landing mid-tool therefore leaves claude's
detached Bash group (`go test -race`, a Gradle daemon, an `npm` build) orphaned, reparented to init,
holding locks/ports/CPU unbounded.

This slice **consumes** the shared reaper — it does not re-implement the process walk. It is the
byte-for-byte streamrunner analogue of the ptyrunner wiring.

## Files to read first

- `internal/agentrun/streamrunner/runner.go:174-232` — the spawn + `cmd.Cancel`/`cmd.WaitDelay`
  block (line 183 is the existing `cmd.Cancel`); the single edit site. Note `childCtx` (167) wraps
  the operator `ctx`, and `cancelChild` is passed to the watchdog — this is why one reap in
  `cmd.Cancel` covers both teardowns.
- `internal/agentrun/ptyrunner/reap.go:18-25` — the `reapDescendantGroupsFn` seam var to mirror
  verbatim (drop ptyrunner's `killGrace` const — streamrunner already defines its own at
  `runner.go:44`).
- `internal/agentrun/ptyrunner/runner.go:302-320` — the parity wiring: reap-then-SIGTERM inside
  `cmd.Cancel`, and the comment explaining why it is race-free (claude + tree alive at fire time).
- `internal/agentrun/ptyrunner/runner_test.go:641-717` — `reapRecorder`, `swapReapSeam`,
  `assertReapedLivePid`, and `TestRun_MaxTurnsExhaustion_ReapsDescendantGroups`; the test doubles +
  assertion helper + one reap test to mirror.
- `internal/agentrun/streamrunner/runner_test.go:102-125` — `TestRun_CtxCancelMidRun`; the
  existing ctx-cancel harness the new reap test clones (same `sleep` helper mode, same cancel-mid-run
  shape) — the reap test adds the seam swap + assertion on top of it.
- `internal/agentrun/streamrunner/helper_test.go:36-55, 84-94` — `helperRunCfg` + the `sleep` fake-
  claude mode (installs a SIGTERM handler, prints `got SIGTERM`, exits 0) the new test reuses.
- `internal/agentrun/reap.go:35-73` — `ReapDescendantGroups` + the three load-bearing guards
  (`pgid<=1`, `pgid==self`, `pgid==rootPid`), so you can confirm the guards already spare
  pyry/claude/init and the streamrunner side adds nothing.
- `internal/e2e/realclaude/sigterm_mid_tool_use_test.go` (whole file) — the #565 real-claude
  premise/regression harness. It already passes `--output-format=stream-json`; the premise gate
  (AC#1) runs it with `PYRY_USE_STREAMJSON=1`. All its process-tree helpers (`waitForBashSubprocess`,
  `waitForGroupGone`, `descendantsOf`, …) are reusable as-is.
- `cmd/pyry/agent_run.go:266-293, 354-366` — `PYRY_USE_STREAMJSON=1` dispatch to
  `runAgentRunStreamRunner` + `buildStreamRunnerClaudeArgs` (forwards `--allowed-tools`, system
  prompt, `--max-turns`, `--model`, `--effort`), confirming the premise-gate harness triggers a real
  Bash tool call on the stream-json path.
- `docs/knowledge/codebase/565.md` — the reaper's design rationale and the "graceful SIGTERM does not
  make claude reap its own Bash group, 3/3" measurement the premise gate re-confirms.

## Design

### AC#1 — Premise gate (verify before implementing)

Confirm empirically that headless `claude -p` under the **stream-json** runner leaves a detached Bash
group orphaned on operator SIGTERM. Preferred instrument: the existing real-claude harness, run on
the stream-json path against the **current (pre-wire) code**.

Recipe:
1. Drive `TestRealClaude_SigtermMidToolUse` (or a throwaway sibling) with `PYRY_USE_STREAMJSON=1` in
   the pyry process env — set it in `spawnPyryAgentRun`'s `cmd.Env` (currently `os.Environ()`), so
   `pyry agent-run` dispatches to `runAgentRunStreamRunner`. Everything else (the `tail -f /dev/null`
   Bash forcing, event-driven SIGTERM timing, `waitForBashSubprocess` → `bashPGID`) transfers
   unchanged. Build tag `e2e_realclaude`; needs `ANTHROPIC_API_KEY`/OAuth (harness skips otherwise).
2. Against **pre-wire** code, the `waitForGroupGone(bashPGID)` invariant must **fail** — the Bash
   group survives pyry's exit. That failure IS the premise confirmation. Capture the surviving
   group's `ps -o pid,ppid,pgid -g <bashPGID>` (ppid reparented to 1) as evidence.
3. Record the evidence in a ticket comment.

**Decision:**
- Premise **holds** (group orphans) → implement the wire below.
- Premise **fails** (headless claude reaps its own group) → close #924 as moot with the evidence;
  no code ships. The #923 lift already landed harmlessly.
- Real claude **cannot** be driven in-environment (no key / claude not runnable) → do **not** silently
  skip. Document the structural basis in the ticket comment — the Bash-group detachment is claude's
  own `setpgid` isolation of each tool command, a property of how *claude* spawns children, not of
  the runner; the runner only controls how claude is *signalled*. #565 measured the orphan 3/3 on the
  interactive/PTY path, so it transfers to the headless stream-json path by construction — then
  proceed with the wire. The reap is harmless if the premise were wrong (an already-reaped/empty tree
  → ESRCH skipped, no-op), which bounds the downside of shipping on the structural argument.

### AC#2/#3 — Wire the reap into the streamrunner teardown

**New file `internal/agentrun/streamrunner/reap.go`** — one unexported seam var, mirroring
`ptyrunner/reap.go:18-25` (without `killGrace`, which already lives at `runner.go:44`):

```go
var reapDescendantGroupsFn = agentrun.ReapDescendantGroups
```

Contract: production points at the shared reaper (byte-identical to a direct call); the unit test
swaps it non-parallel and restores via `t.Cleanup`. Doc-comment it like ptyrunner's.

**Edit `runner.go:183`** — add the reap ahead of the existing SIGTERM:

```go
cmd.Cancel = func() error {
    reapDescendantGroupsFn(cmd.Process.Pid, logger)
    return cmd.Process.Signal(syscall.SIGTERM)
}
```

Design notes to carry in the comment:
- **One reap covers both teardowns.** `cmd.Cancel` fires when `childCtx` is done. `childCtx` is
  cancelled by (a) operator SIGTERM/SIGINT propagating through the parent `ctx`, and (b) the
  idle-stall watchdog's `cancelChild`. Both route through this single hook, so the reap is wired once
  and cannot be scoped to only one path — nor should it be (do **not** add a `ctx.Err()`
  discriminator; the watchdog-path reap is a harmless no-op).
- **Watchdog path is a harmless no-op in the common case.** The watchdog fires only while claude owes
  an assistant turn (`awaiting`), i.e. no tool in flight — so the descendant walk finds no live Bash
  group and reaps nothing. If a tool were somehow in flight, reaping it is beneficial, not harmful.
- **Race-free at fire time.** `cmd.Cancel` runs before claude is signalled, with claude and its whole
  descendant tree alive and un-signalled — same invariant ptyrunner relies on. `cmd.Process` is
  non-nil (the hook only fires post-`Start`; the existing `cmd.Process.Signal` already assumes this).
- **Never fires on clean exit.** On a normal child exit, `cancelChild()` runs only *after* `cmd.Wait()`
  returns (`runner.go:212`), by which point os/exec has already stopped its ctx watcher — so
  `cmd.Cancel` (and thus the reap) is not invoked. Identical firing semantics to the existing SIGTERM.
- The trailing SIGTERM (vs os/exec's default immediate SIGKILL) is unchanged — it still lets claude
  flush its session JSONL before exit.

### AC#5 — Dependency ban (already satisfied by construction)

`reap.go` imports `github.com/pyrycode/pyrycode/internal/agentrun` — the shared parent, **already**
imported by `runner.go:38` (for `agentrun.ExitErrIsBenign`). No new dependency edge, and no import of
a sibling `agentrun` subpackage. The package doc-comment's `go list -deps … | grep …/internal/supervisor`
guard (runner.go:17-23) stays green. Confirm with the existing check; nothing to add.

## Concurrency model

No new goroutines. The reap runs synchronously inside the os/exec ctx-watcher goroutine's invocation
of `cmd.Cancel`, bounded by the reaper's own 2s `ps` timeout (`reapPSTimeout`, internal to
`agentrun.ReapDescendantGroups`). The watchdog goroutine is untouched — it already calls `cancelChild`,
which now additionally triggers the reap via the shared hook.

## Error handling

Delegated entirely to `agentrun.ReapDescendantGroups`: best-effort and content-blind. Enumeration/kill
failures log at Warn (pgids/counts only, never command strings) and do not propagate; `ESRCH`
(group already exited in the teardown window) is skipped as benign. claude still receives its SIGTERM
regardless of reap outcome. streamrunner adds no error handling of its own.

## Testing strategy

**Required (AC#4) — deterministic unit seam test, CI-safe, no real claude.** Add to
`runner_test.go`, mirroring ptyrunner's doubles:

- Port `reapRecorder` (mutex-guarded; records each `rootPid`), `swapReapSeam(t, rec)` (points the
  package var at `rec.record`, restores via `t.Cleanup`), and `assertReapedLivePid(t, rec)`
  (asserts ≥1 call carried a plausible `pid > 1`) from `ptyrunner/runner_test.go:641-693`. If any
  are trivially identical, keep them file-local to streamrunner (no cross-package test import).
- New test `TestRun_CtxCancel_ReapsDescendantGroups` — clone `TestRun_CtxCancelMidRun`
  (`sleep` helper mode, cancel the parent ctx ~100ms in). Before `Run`, call `swapReapSeam`. Assert:
  (1) `Run` returns nil (operator-cancel teardown is a clean no-result exit), and
  (2) `assertReapedLivePid` — the seam fired with the live fake-claude pid, proving the reap wired
  into `cmd.Cancel` runs on the operator-SIGTERM teardown. **Non-parallel** (it mutates the package
  var); do NOT add `t.Parallel()`. Go completes non-parallel tests and their cleanups before parked
  parallel siblings resume, so the swap window never overlaps `TestRun_CtxCancelMidRun` et al.

This one operator-cancel test satisfies AC#4: both teardown paths share the identical `cmd.Cancel`
closure, so exercising the closure once proves the wiring for the watchdog path too (which is
independently covered by the existing watchdog tests establishing that it cancels `childCtx`).

**Not required — real-claude e2e regression guard.** See Open questions.

## Open questions

- **Commit a stream-json e2e regression guard?** The premise gate (AC#1) stands up the stream-json
  variant of `TestRealClaude_SigtermMidToolUse` anyway. Committing it as a permanent `e2e_realclaude`
  guard would give the stream-json path the same standing #565 gave the PTY path — at the cost of
  reusing/parameterising a known-flaky real-claude test (the SIGTERM realclaude test is a documented
  flake). **Recommendation:** leave it out of the required scope. The deterministic unit seam test is
  sufficient for AC#4 and CI-safe; add the e2e guard only if it falls out cheaply from the premise-gate
  work and the environment runs it reliably. Do not spend turn budget forcing it.
- **Premise-gate feasibility.** If real claude cannot run in the developer environment, follow the
  structural-fallback branch in AC#1 rather than blocking — the wire is harmless under a wrong
  premise, and #565's 3/3 measurement plus claude's runner-independent `setpgid` behavior carry the
  premise. Record the path taken (empirical vs structural) explicitly in the ticket comment.
