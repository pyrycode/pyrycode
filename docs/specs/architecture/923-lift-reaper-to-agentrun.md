# Spec #923 — Lift the claude descendant-group reaper into `internal/agentrun`

**Ticket:** [#923](https://github.com/pyrycode/pyrycode/issues/923) — *Lift claude descendant-group reaper into internal/agentrun (no behaviour change)*
**Size:** S · **Security-sensitive:** no (process-lifecycle/reliability; no untrusted input, network, or crypto surface — reaping claude's own local detached Bash groups)
**Parent:** split from #915 (this is the *lift* child; #924 is the *wire* child, blocked-by #923).

## Summary

Pure relocation, **no behaviour change** for the PTY runner. Move the package-local reaper
(`reapDescendantGroups` + its enumeration helper `descendantPGIDs` + `reapPSTimeout`) up from
`internal/agentrun/ptyrunner` into the shared `internal/agentrun` package, beside `ExitErrIsBenign`
(the teardown-classification helper both runners already share). Export **only** the entry point as
`ReapDescendantGroups(rootPid int, logger *slog.Logger)`; the enumeration helper relocates alongside
it but **stays unexported** (it has no consumer outside the reaper — exporting a private impl helper
needlessly widens the shared package's API). The stream-json runner (#924) will consume the exported
function directly, since its package doc forbids importing sibling `agentrun` subpackages.

The PTY runner's three teardown call sites are already insulated behind the `reapDescendantGroupsFn`
package-var seam. The seam **stays in `ptyrunner`** and is simply re-pointed at the lifted function,
so `runner.go` and its seam-swap unit tests are untouched.

## Files to read first

- `internal/agentrun/ptyrunner/reap.go:1-133` — **the source of the move.** `reapDescendantGroups`
  (52), `descendantPGIDs` (92), consts `killGrace` (21), `reapPSTimeout` (25), seam var
  `reapDescendantGroupsFn` (33). Extract: the two functions + `reapPSTimeout` relocate verbatim;
  `killGrace` and the seam **stay** (see Design). Preserve the three load-bearing guards
  (`pgid<=1`, `pgid==self`, `pgid==rootPid`) exactly.
- `internal/agentrun/exitclass.go:1-45` — the **target package** (`package agentrun`) and the sibling
  helper `ExitErrIsBenign`. Extract: package name + import idiom; the reaper lands in a new sibling
  file, not appended here.
- `internal/agentrun/exitclass_test.go:17-40` — the package's **single** `TestMain` + `runExitHelper`.
  Extract: dispatch shape — it keys on `os.Getenv("GO_EXITCLASS_HELPER") != ""` with **no**
  `GO_..._HELPER=1` gate. The reap-mode dispatch branch folds in here.
- `internal/agentrun/ptyrunner/reap_test.go:1-198` — the reap unit tests (4 subtests) + the test-side
  fixtures (`startReapHelper`, `waitReport`, `processAlive`, `waitGroupGone`, `reapHelperOpts`,
  `reapHelper`). Extract: all of it relocates; the 4 `reapDescendantGroups(...)` calls become
  `ReapDescendantGroups(...)`.
- `internal/agentrun/ptyrunner/helper_test.go:79-87` (reap-mode dispatch in `runHelper`) and
  `:278-342` (fixture-side `runReapHelper`, `blockUntilKilled`, `spawnGrandchildAndBlock`). Extract:
  these move to `agentrun`; the env keys rename (see Design); note the grandchild-spawn `append`
  pattern relies on `os/exec`'s env-dedup-keeps-last — preserve verbatim, only rename keys.
- `internal/agentrun/ptyrunner/runner.go:302-320, 388-398, 490-504` — the **three** seam call sites
  (operator-cancel `cmd.Cancel`; post-spawn `defer`; budget `Terminate` hook). Extract: all route
  through `reapDescendantGroupsFn` — **no edits here**; they keep working once the seam is re-pointed.
- `internal/agentrun/ptyrunner/runner_test.go:641-693` — the seam-swap doubles (`reapRecorder.record`,
  `swapReapSeam`, `assertReapedLivePid`). Extract: `record(rootPid int, _ *slog.Logger)` must stay
  signature-compatible with `agentrun.ReapDescendantGroups` — it is; **no edits here**.
- `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:116-292` — the SIGTERM-mid-tool e2e
  (`-tags e2e_realclaude`). Extract: it asserts **process/group liveness** (`waitForProcessGone`,
  `waitForGroupGone`), never a log string — so the `ptyrunner:`→`agentrun:` log-prefix neutralisation
  is safe. No edits; may be env-blocked (see [[known-test-flakes]] — realclaude PTY).

## Context

#565 built `reapDescendantGroups`/`descendantPGIDs` inside `ptyrunner` and deliberately kept them
package-local. #915 found the stream-json runner has the same #565 parity gap (claude leaks detached
Bash process groups on SIGTERM), but streamrunner cannot import a sibling `agentrun` subpackage. The
fix must lift the reaper to the shared parent. This ticket is the lift only; the actual streamrunner
wire — and the empirical premise repro (does headless `claude -p` leak groups on SIGTERM?) — lands in
#924. If #924's premise fails, the lift here still stands as a harmless relocation.

## Design

### Production changes (2 files)

**New: `internal/agentrun/reap.go`** (`package agentrun`)

Relocate verbatim, adjusting only name + log prefix:

- `func ReapDescendantGroups(rootPid int, logger *slog.Logger)` — exported. Body byte-identical to
  today's `reapDescendantGroups` except the two `logger.Warn` and one `logger.Info` message strings
  change prefix `ptyrunner:` → `agentrun:` (Technical Note: "neutralise it for the shared package").
  The three guards (`pgid <= 1 || pgid == self || pgid == rootPid`) and the ESRCH-benign skip are
  load-bearing — preserve exactly. Content-blind: reads pid/ppid/pgid triples only, logs pgids/counts
  only, never command strings.
- `func descendantPGIDs(ctx context.Context, rootPid int) (map[int]struct{}, error)` — **unexported**,
  relocated verbatim. Only caller is `ReapDescendantGroups`.
- `const reapPSTimeout = 2 * time.Second` — relocates (only the reaper uses it).

Imports: `context, errors, log/slog, os/exec, strconv, strings, syscall, time`.

**Modified: `internal/agentrun/ptyrunner/reap.go`** — after the move, this file retains only:

- `const killGrace = 5 * time.Second` — **stays.** It is a distinct SIGTERM-grace const consumed by
  `runner.go:320` (`cmd.WaitDelay = killGrace`); streamrunner and the e2e harness carry their own
  same-named consts. Do **not** let it ride into `agentrun` — that collides. Its doc comment's
  `See reap.go` back-reference stays valid (same file).
- `var reapDescendantGroupsFn = agentrun.ReapDescendantGroups` — the seam, **re-pointed** at the
  lifted function. Type inference keeps its `func(int, *slog.Logger)` shape; the three runner call
  sites and the seam-swap tests bind to it unchanged.

Import set shrinks to `time` + `github.com/pyrycode/pyrycode/internal/agentrun` (the latter already
imported by `runner.go:55`, so the dependency is not new to the package). All of
`context/errors/log/slog/os/exec/strconv/strings/syscall` drop from this file.

`runner.go` is **untouched** — the seam re-point is entirely inside `reap.go`.

### Test changes (4 files)

**New: `internal/agentrun/reap_test.go`** (`package agentrun`) — relocate `TestReapDescendantGroups`
(4 subtests) plus **all** reap fixture machinery, consolidated from the two ptyrunner test files:

- Test-side (from `ptyrunner/reap_test.go`): `startReapHelper`, `waitReport`, `processAlive`,
  `waitGroupGone`, types `reapHelperOpts`/`reapHelper`. The 4 subtest bodies change
  `reapDescendantGroups(...)` → `ReapDescendantGroups(...)`.
- Fixture-side (from `ptyrunner/helper_test.go:278-342`): `runReapHelper`, `blockUntilKilled`,
  `spawnGrandchildAndBlock`.
- **Env-key rename** (package-neutralise): `GO_PTYRUNNER_REAP_MODE` → `GO_AGENTRUN_REAP_MODE`,
  `GO_PTYRUNNER_REAP_REPORT` → `GO_AGENTRUN_REAP_REPORT`. **Drop** the `GO_PTYRUNNER_HELPER=1` gate
  from the two spawn sites (`startReapHelper`, `spawnGrandchildAndBlock`) — `agentrun`'s dispatch keys
  purely on the mode var being non-empty (matching the `GO_EXITCLASS_HELPER` pattern), so the extra
  gate is dead. The grandchild spawn keeps its `append(os.Environ(), "GO_AGENTRUN_REAP_MODE=leaf")`
  order: `os/exec` dedups env keeping the **last** occurrence, so `leaf` overrides the parent's
  inherited `parent_fresh`. Do not reorder or "simplify" this.

**Modified: `internal/agentrun/exitclass_test.go`** — the package's single `TestMain` gains a
reap-mode dispatch branch beside the existing exit-helper branch:

```go
if role := os.Getenv("GO_AGENTRUN_REAP_MODE"); role != "" {
    runReapHelper(role)   // defined in reap_test.go, same package
    return
}
```

Order relative to the `GO_EXITCLASS_HELPER` branch is irrelevant: the two env sets are disjoint (exit
tests never set the reap var and vice-versa). `runReapHelper` is visible across `_test.go` files in
the same package.

**Modified: `internal/agentrun/ptyrunner/helper_test.go`** — remove the reap-mode dispatch branch
(`:83-87`) from `runHelper`, and delete `runReapHelper`, `blockUntilKilled`, `spawnGrandchildAndBlock`
(`:278-342`). The fake-claude modes and `writeSessionJSONLBody` are untouched (they serve the runner
tests, not the reap tests).

**Deleted: `internal/agentrun/ptyrunner/reap_test.go`** — its contents relocate to
`agentrun/reap_test.go`.

## Concurrency model

Unchanged. `ReapDescendantGroups` is best-effort and stateless: it reads its two arguments, shells
out to one `ps` snapshot (bounded by `reapPSTimeout`), and issues `SIGKILL`s. Safe on the os/exec
watcher goroutine and the budget hook goroutine, exactly as before. No new goroutines, channels, or
shared state introduced by the move.

## Error handling

Unchanged and load-bearing — preserve verbatim:

- Enumeration failure (`ps` error/timeout) → `logger.Warn` (root_pid + err), return; claude still gets
  its SIGTERM regardless.
- Per-group `SIGKILL` ESRCH (group already exited in the teardown window) → skipped silently (benign).
- Other kill error → `logger.Warn` (pgid + err), continue to the next group.
- The three spare-guards prevent SIGKILLing pyry's own group, rootPid's (claude's) own group, or init.

## Testing strategy

The relocated tests are the deliverable's own proof — no new test logic is authored, only moved:

- **Unit (relocated).** `TestReapDescendantGroups`' 4 subtests build real process trees under the
  `agentrun` TestMain dispatch and assert the guards: spares the caller's own group, spares a
  same-group grandchild (rootPid's own group), no-ops with no descendants, and SIGKILLs a fresh
  detached descendant group while sparing rootPid. Must pass under `go test -race` from
  `internal/agentrun`.
- **Unit (undisturbed, ptyrunner).** `TestRun_MaxTurnsExhaustion_ReapsDescendantGroups` and
  `TestRun_WatchdogFires_ReapsDescendantGroups` swap the ptyrunner-local `reapDescendantGroupsFn`
  seam via `swapReapSeam` and assert the three call sites fire. They keep passing because the seam
  and its double are unchanged; only the seam's default target moved.
- **Race/count stability.** Run `go test -race -count=5 ./internal/agentrun/ ./internal/agentrun/ptyrunner/`.
  Rationale: two reap subtests reap at `os.Getpid()`, sweeping every fresh-group descendant of the
  test process. Confirm they don't collide with `TestExitErrIsBenign`'s helper subprocesses. They
  cannot: those exit-helpers are spawned **without** `Setpgid` (they stay in the test's own group, so
  the `pgid==self` guard spares them) and complete synchronously in `TestExitErrIsBenign`'s sequential
  setup before any reap runs. The `-count=5` run confirms this empirically.
- **E2E (undisturbed).** `TestRealClaude_SigtermMidToolUse` (`-tags e2e_realclaude`) asserts full
  subtree teardown via process/group liveness, not log strings — must stay green. May be env-blocked
  on this runner (see [[known-test-flakes]]: realclaude PTY / claude-version mismatch); if it can't
  run, note that attribution rather than treating a non-run as red.
- **No new module dependency;** `go vet ./...` + `staticcheck ./...` clean.

## Open questions

- **Log prefix target.** Spec picks `agentrun:` (matches the package the code now lives in; parallels
  the existing `ptyrunner:` convention). Nothing asserts on the string, so any neutral prefix is
  acceptable — this is the developer's call if `agentrun:` reads awkwardly. Whatever is chosen must
  stay content-blind (pgids/counts only).
- **File placement.** Spec puts the reaper in a **new** `internal/agentrun/reap.go` rather than
  appending to `exitclass.go`, mirroring the ptyrunner split (`runner.go` vs `reap.go`) and keeping
  the reaper's wider import set out of `exitclass.go`. "Colocated with `ExitErrIsBenign`" (AC1) is
  satisfied by same-package siblinghood. Developer may append to `exitclass.go` instead if preferred,
  but the separate file is cleaner.

## Scope self-check

Production source files with new/modified content: **2** (`internal/agentrun/reap.go` new,
`internal/agentrun/ptyrunner/reap.go` modified). `runner.go` untouched. Under the ≥5 gate. Edit
fan-out: `codegraph_impact reapDescendantGroups` = 4 symbols, all co-located; production call sites
insulated by the seam (1-line re-point). No split.
