# Spec #527 — agentrun: benign-teardown-error predicate (`ExitErrIsBenign`)

Filter four WARN call sites in `internal/agentrun/{ptyrunner,budget,streamrunner}` through a shared predicate so the OS-level "process already gone" responses to our own SIGTERM/SIGKILL/close stop showing up as failures on every routine `max_turns` exhaustion.

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:281-285` — the `defer sess.Close()` block whose `cerr` is the misclassified WARN. The fix wraps `cerr` in the predicate before the existing `logger.Warn(...)`.
- `internal/agentrun/budget/budget.go:127-142` — `OnEvent` arming + `Terminate()` call; the `logger.Warn("budget: terminate failed", …)` at line 137 is one of the four sites.
- `internal/agentrun/budget/budget.go:178-193` — `killAfterGrace`; the `logger.Warn("budget: kill failed", "err", err)` at line 191 is the second budget site.
- `internal/agentrun/streamrunner/runner.go:160-175` — the synchronous stdin write + close pair. The `logger.Warn("streamrunner: stdin close failed", "err", err)` at line 167 is the fourth site. The sibling `stdin write failed` at line 164 stays a WARN — that is a mid-write failure, not a teardown response, and is explicitly out of scope per the issue body.
- `internal/agentrun/workdir.go:1-8` — confirms the parent `package agentrun` already exists and is non-empty. The new predicate file (`exitclass.go`) goes in this package; its sibling subpackages import it.
- `internal/agentrun/budget/budget_test.go:295-334` — `TestTerminateError_DoesNotBlockKill` and `TestKillError_IsLogged` are the existing tests that exercise the two budget WARN sites. Both pass non-benign errors (`errors.New("simulated …")`) so neither needs changing — confirm by re-reading. They also document the slog `Level: LevelWarn` + `syncWriter` capture pattern the new regression tests reuse.
- `internal/agentrun/ptyrunner/runner_test.go:28-55` and `internal/agentrun/ptyrunner/helper_test.go` — `helperRunCfg` + `TestPtyRunnerHelperProcess` is the established fake-claude pattern (helper re-execs `os.Args[0]` with env vars). The new ptyrunner regression test reuses this scaffold with a new helper mode that responds to SIGTERM with `os.Exit(143)`.

## Context

`pyry agent-run --max-turns N` emits one WARN per terminated run today:

```
INFO  budget: max turns reached, sending SIGTERM count=1 max_turns=1
WARN  ptyrunner: close failed err="exit status 143"
```

`exit status 143` = `128 + SIGTERM(15)`. Claude exited from the SIGTERM the budget watcher itself sent. The teardown contract worked; the log frames the expected exit as a failure. `max-turns:90` exhaustion is the routine path for many tickets — once tui-driver runtime ships, this becomes a one-warning-per-run noise floor that drowns genuine WARN signal.

The same shape (an outer layer's expected exit code surfacing as an inner WARN) appears at three other sites. All four share root cause: an operation aimed at *getting the child gone* observes that the child is *already gone* — `syscall.ESRCH` on a kill, `syscall.EPIPE` on a closed pipe, `os.ErrClosed` on an already-closed fd, or `*exec.ExitError{ExitCode: 143|137}` from `Session.Close()` bubbling up our own signal.

Severity is cosmetic — the dispatcher reads `terminal_reason` from the trailer, not log noise — but the user-visible operator experience degrades meaningfully once tui-driver runtime is in production.

The 10 unrelated WARN/ERROR sites in `internal/agentrun/` (pre-run spawn, trust/MCP/network modal detection, watchdog, mid-write failures, grace-period elapsed) remain unchanged. The predicate is opt-in per call site.

## Design

### Predicate (`internal/agentrun/exitclass.go`)

Single new file in the parent `package agentrun`. The predicate is exported because the three consuming subpackages live below it.

Signature and contract:

```go
// ExitErrIsBenign reports whether err is the OS-level "process already
// gone" or "self-signalled exit" shape that surfaces during expected
// teardown of an agent-run child. Returns false for nil and for any
// other error. Wrapped errors are unwrapped via errors.Is / errors.As.
//
// Benign classes:
//   - syscall.ESRCH      (no such process — kill/signal raced child exit)
//   - syscall.EPIPE      (broken pipe — fd write after child exit)
//   - os.ErrClosed       (write/close after fd already closed)
//   - *exec.ExitError where ExitCode() == 143 (SIGTERM) or 137 (SIGKILL)
//   - *exec.ExitError where the child was signal-killed by SIGTERM or
//     SIGKILL (Signaled() with the matching signal — covers the case
//     where the child has no signal handler and ExitCode() returns -1).
func ExitErrIsBenign(err error) bool
```

Implementation notes (sketch, not full body):

- `if err == nil { return false }` first; nil is explicitly non-benign per AC.
- Three `errors.Is(err, syscall.X)` checks for `ESRCH`, `EPIPE`, `os.ErrClosed`. `errors.Is` traverses `*os.PathError` / `*os.SyscallError` wrappers automatically.
- `var exitErr *exec.ExitError; if errors.As(err, &exitErr) { … }` — inside, accept either `exitErr.ExitCode() == 143 || == 137`, or the signal-killed shape via `exitErr.Sys().(syscall.WaitStatus)` with `Signaled()` true and `Signal()` in `{syscall.SIGTERM, syscall.SIGKILL}`. The two checks are independent (a signal-killed process reports `ExitCode() == -1`, so we need both).
- Return `false` for any other shape.

The signal-killed branch goes beyond the literal AC ("ExitCode() is 143 or 137") but covers the same scenario the AC describes — claude killed by our SIGTERM/SIGKILL. Cost is ~6 LOC and one short comment; the alternative is a future bug where a signal-killed child without a 143/137 handler is misclassified.

### Call-site guards

Each of the four sites takes the same one-line transform: filter the returned error through `agentrun.ExitErrIsBenign` and skip (or downgrade) the existing WARN when the predicate is true. The original WARN must fire unchanged on non-benign errors.

The AC permits either "drop the log" or "emit at DEBUG". Default to **DEBUG** at every site — preserves observability for an operator who explicitly enables debug logging without contributing to the WARN noise floor. A bare drop is acceptable if the developer judges the per-site log redundant; both meet the AC.

The four sites, with the structural shape of the guard:

1. **`internal/agentrun/ptyrunner/runner.go:281-285`** — wrap the `if cerr != nil` body. Pseudocode shape:

   ```go
   if cerr := sess.Close(); cerr != nil {
       if agentrun.ExitErrIsBenign(cerr) {
           logger.Debug("ptyrunner: close: child already exited", "err", cerr)
       } else {
           logger.Warn("ptyrunner: close failed", "err", cerr)
       }
   }
   ```

   Requires a new import `"github.com/pyrycode/pyrycode/internal/agentrun"`. The ptyrunner package already imports the `budget` and `streamjson` subpackages of `internal/agentrun`, so the parent-package import sits naturally with them.

2. **`internal/agentrun/budget/budget.go:137`** — the same shape around the existing `Terminate` error log. Keep the structured `count` / `max_turns` fields on both branches:

   ```go
   if err := c.cfg.Terminate(); err != nil {
       if agentrun.ExitErrIsBenign(err) {
           c.cfg.Logger.Debug("budget: terminate: child already exited",
               slog.Int("count", count), slog.Int("max_turns", max), "err", err)
       } else {
           c.cfg.Logger.Warn("budget: terminate failed",
               slog.Int("count", count), slog.Int("max_turns", max), "err", err)
       }
   }
   ```

3. **`internal/agentrun/budget/budget.go:191`** — same shape around the `Kill` error log. Keep the `reason` field on both branches.

4. **`internal/agentrun/streamrunner/runner.go:167`** — same shape around the `stdin.Close()` WARN. **The sibling `stdin write failed` at line 164 MUST stay a WARN unchanged** — that is a mid-write failure, not a teardown response.

The budget package's new import is `"github.com/pyrycode/pyrycode/internal/agentrun"`. There is no import cycle: `internal/agentrun/workdir.go` does not import any subpackage.

### Why a shared predicate (not per-site duplicates)

- One definition site keeps the "what counts as benign?" question owned in one place. Adding a class later (e.g. a fifth `errors.Is` target) is a one-file edit, not a four-file edit drifting out of sync.
- The AC explicitly verifies single-predicate scope via `grep`. Per-site duplicates would fail the scope-verification AC.
- Tests for the predicate centralise in one file; per-site behaviour tests can lean on the predicate's own coverage and only assert "this site uses the predicate" via a captured-logger regression.

Alternatives considered and rejected:

- **Per-site inline `errors.Is` chains**: rejected — drift risk, four times the test surface, the AC's grep scope rules it out.
- **An outer-layer "we are shutting down" state flag**: rejected — requires every layer to subscribe to teardown state; the four sites are already self-contained in needing to recognise a benign error class.

## Concurrency model

No new goroutines. The predicate is a pure function over an `error` value — safe for concurrent use by construction. Each call site already runs on its existing goroutine:

- ptyrunner site fires in the `Run` goroutine's defer chain.
- budget `terminate` site fires in the goroutine calling `OnEvent` (typically the JSONL drain goroutine).
- budget `kill` site fires in the `time.AfterFunc` goroutine.
- streamrunner site fires in the `Run` goroutine after `cmd.Start()`.

The predicate has no shared state; concurrency is structurally fine.

## Error handling

The predicate never returns an error itself — it returns `bool`. Failure modes are limited to "called with an unexpected error shape", which collapses to `return false` (preserve existing WARN — the safe default).

Wrapping discipline at each call site stays as-is: the existing WARN messages already include `err` as a structured field, and the DEBUG (or dropped) variant uses the same field. No new error wrapping or transformation.

## Testing strategy

### Predicate unit tests (`internal/agentrun/exitclass_test.go`)

Table-driven, stdlib `testing` only, parallel. Cover the positive and negative cases the AC enumerates plus the signal-killed branch added by this spec.

Cases (each row asserts `ExitErrIsBenign(err) == want`):

**Positive (want true):**
- raw `syscall.ESRCH`
- raw `syscall.EPIPE`
- raw `os.ErrClosed`
- raw `*exec.ExitError` with `ExitCode() == 143` (constructed via a fake-child helper that calls `os.Exit(143)` and a single `cmd.Run()` capture, or via `syscall.WaitStatus` reflection — see `exec_test.go` patterns in the stdlib)
- raw `*exec.ExitError` with `ExitCode() == 137` (same shape, exit 137)
- a signal-killed `*exec.ExitError` (child loops with `signal.Ignore(SIGTERM)` is not portable to write here — alternative: drive via `cmd.Process.Signal(SIGKILL)` against a sleep child, assert `Signaled() && Signal() == SIGKILL`)
- each of the four positive cases above also wrapped via `fmt.Errorf("context: %w", inner)`

**Negative (want false):**
- `nil`
- `errors.New("plain")`
- `*exec.ExitError` with `ExitCode() == 1` (non-signal exit)
- `fmt.Errorf("wrap: %w", errors.New("plain"))`
- a wrapped non-benign error (e.g. a wrapped `errors.New("disk full")`)

If constructing a real `*exec.ExitError` with specific codes is awkward in unit tests, use a `TestHelperProcess` re-exec pattern (matches the existing `internal/agentrun/ptyrunner/helper_test.go` and `internal/agentrun/streamrunner/helper_test.go` shapes) inside the predicate's own test file — keeps construction realistic without depending on syscalls.

### Regression tests (call-site behaviour)

The AC asks for "a regression test" asserting zero WARN-or-higher records from the four sites on a `max_turns`-terminated run. The four sites span three packages and two top-level entry points (ptyrunner.Run vs streamrunner.Run), so split the regression into two scoped tests — both reuse the existing slog-capture pattern at `internal/agentrun/budget/budget_test.go:319` (`slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))` + `syncWriter`):

1. **`internal/agentrun/ptyrunner/runner_test.go` — `TestRun_MaxTurnsExhaustion_NoBenignWarns`**
   - Reuses `helperRunCfg` with `MaxTurns: 1` and a new helper mode (add to `helper_test.go`'s switch) that installs a SIGTERM handler exiting with code 143 and emits one assistant JSONL entry before blocking.
   - Wires a captured slog handler at `LevelWarn` into `cfg.Logger`.
   - Asserts the buffer contains zero records mentioning any of: `ptyrunner: close failed`, `budget: terminate failed`, `budget: kill failed`. Asserts `Run` returns nil (operator-shutdown collapse on the budget-triggered cancel) — equivalent to the existing `TestRun_CtxCancelDuringStream` post-conditions.
   - Covers sites 1, 2; covers site 3 only when the SIGTERM-handled exit is fast enough that the kill timer never fires. To exercise site 3, the helper mode optionally ignores SIGTERM (separate test or table case) so the grace timer fires SIGKILL into an already-exited shell wrapper — confirms `kill failed` does not surface either.

2. **`internal/agentrun/streamrunner/runner_test.go` — `TestRun_EarlyExitChild_NoBenignStdinCloseWarn`**
   - Reuses the existing streamrunner `helperRunCfg` shape (matches `internal/agentrun/streamrunner/runner_test.go` and `helper_test.go`).
   - New helper mode (or a flag on the existing one) that exits before reading the stdin envelope, forcing `EPIPE` on the subsequent `stdin.Close()`.
   - Wires a captured slog handler at `LevelWarn`. Asserts the buffer contains zero records mentioning `streamrunner: stdin close failed`. Asserts the `streamrunner: stdin write failed` WARN remains observable when the write fails first (separate sub-test or case to confirm the write-site WARN is not collateral damage).

Both tests use the synchronised-writer pattern already in `internal/agentrun/budget/budget_test.go:336-353` (`syncWriter`) — slog handlers may write concurrently from defers and timer goroutines.

### Existing tests that should keep passing without modification

- `TestTerminateError_DoesNotBlockKill` (budget) passes a non-benign `errors.New("simulated ESRCH")` — the *literal text* "simulated ESRCH" does NOT equal `syscall.ESRCH`, so the predicate returns false and the WARN still fires. The test asserts the Kill behaviour, not the log shape, so it remains valid.
- `TestKillError_IsLogged` (budget) passes a non-benign `errors.New("simulated kill failure")` — same: predicate returns false, WARN fires, assertion passes.

If the developer chooses to keep a positive assertion that the WARN suppression works end-to-end at the budget level (rather than driving it through ptyrunner), add a sibling test that passes `syscall.ESRCH` as the `Terminate` return — confirms the predicate path fires.

### Scope-verification check (mechanical, per AC)

The final AC asks for `grep -rn 'exitErrIsBenign' internal/agentrun/` to return only the definition + four call sites. The exported name is `ExitErrIsBenign` (case-sensitive; the AC's lowercase is informal — see *Open questions* below). The developer runs the case-insensitive equivalent as part of the implementation sanity check:

```
grep -rni 'exiterrisbenign' internal/agentrun/
```

Expected: 5 hits — one in `exitclass.go` (definition), one in `exitclass_test.go` test-file imports (acceptable; the test file references the predicate by name), one each in `ptyrunner/runner.go`, `budget/budget.go` (twice — the two budget sites), and `streamrunner/runner.go`. Six hits total once you count the budget twice. The intent of the AC is "the predicate is referenced from exactly the documented production call sites" — test-file references count separately and are fine.

## Open questions

- **Exported vs unexported predicate name.** The AC writes `exitErrIsBenign` (lowercase first letter = unexported in Go). Unexported requires the predicate to live in each subpackage as a duplicate, contradicting the AC's "predicate is referenced from exactly the 4 documented call sites and no others" (single shared predicate). Resolution: name it `ExitErrIsBenign` (exported) in `internal/agentrun/`. The AC's casing is informal; the structural requirement (single predicate, four call sites) is what binds.
- **Drop vs DEBUG.** Spec recommends DEBUG at all four sites for observability. Drop is permitted by the AC. Developer picks per site if a DEBUG line is genuinely redundant; default to DEBUG.
- **Signal-killed `*exec.ExitError` branch.** The AC enumerates `ExitCode() == 143 || 137`. The spec adds a `Signaled() + Signal() in {SIGTERM, SIGKILL}` branch as a defensive companion — covers signal-killed processes that report `ExitCode() == -1`. If the developer concludes this is unobserved-failure-mode territory (violates "Evidence-Based Fix Selection" in pipeline principles), drop the branch and ship only the literal-AC `ExitCode()` check. The cost of including it is ~6 LOC and one comment; the cost of omitting it is a future identical-shape bug if claude's signal-handling changes.

## Scope check (self-verification)

Production source files this spec prescribes new or modified content for:

1. `internal/agentrun/exitclass.go` — new
2. `internal/agentrun/ptyrunner/runner.go` — modify (one-site guard)
3. `internal/agentrun/budget/budget.go` — modify (two-site guard)
4. `internal/agentrun/streamrunner/runner.go` — modify (one-site guard)

Count: **4 production source files** (under the ≥ 5 forced-split threshold).

New files total: 2 (`exitclass.go` + `exitclass_test.go`) — under the "more than 3 new files" red line.

Total LOC estimate (production + tests + regression + spec edits):
- Predicate: ~30 LOC.
- Predicate tests: ~90 LOC (table-driven, includes wrapped + signal-killed cases).
- 4 call-site guards: ~24 LOC total (~6 LOC per site with the if/else split).
- 2 regression tests (ptyrunner + streamrunner): ~100 LOC combined, including helper mode wiring.

Estimated total: **~250 LOC** (within the 600-LOC red line). Sized correctly at S.
