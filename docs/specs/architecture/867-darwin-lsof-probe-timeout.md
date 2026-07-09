# Spec: bound the darwin lsof rotation probe with a timeout (#867)

## Files to read first

- `internal/sessions/rotation/probe_darwin.go:42-59` — `darwinProbe.OpenJSONL`; the sole
  production line to change. Note the current `exec.Command(...).Output()` (no ctx) and the
  exit-code-1 benign classification (lines 48-50) that must survive verbatim.
- `internal/agentrun/ptyrunner/reap.go:23-33` — **the precedent to mirror.** `reapPSTimeout`
  const (2s) + the `reapDescendantGroupsFn` package-var seam pattern (test swaps non-parallel,
  restores via `t.Cleanup`). Copy this shape.
- `internal/agentrun/ptyrunner/reap.go:92-96` — `descendantPGIDs`: the exact
  `exec.CommandContext(ctx, ...).Output()` idiom under a `context.WithTimeout`, no `WaitDelay`.
  Your `OpenJSONL` construction is byte-for-byte analogous.
- `internal/sessions/rotation/watcher.go:199-219` — `probeWithRetry`: proves AC 3. A probe
  `error` is caught into `lastErr` and the loop continues to the next retry delay — a timeout
  surfaces as an ordinary transient. No caller change needed.
- `internal/sessions/rotation/probe.go:12-20` — `Probe` interface contract: "Returns error only
  for unrecoverable probe failures … so the watcher skips and retries." A timeout is exactly such
  a transient failure; the contract already covers it.
- `internal/sessions/rotation/probe_darwin_test.go` — the existing darwin-only test file
  (`//go:build darwin`); add the new tests + `TestHelperProcess` here. Note: no
  `TestHelperProcess` exists in this package yet — you introduce the first one.
- `CODING-STYLE.md:67-75` — the `TestHelperProcess` re-exec pattern the tests use.

## Context

The darwin rotation probe (`darwinProbe.OpenJSONL`) shells out with
`exec.Command("lsof", ...).Output()` — no context, no timeout. `lsof` can hang (dead network
mounts, uninterruptible-sleep targets, loaded hosts). Three callers run the probe synchronously
on hot single-threaded paths:

- `snapshotUsage` on the v2 manager's single `Run` dispatch goroutine (once per `screen_snapshot`).
  A hung `lsof` freezes the entire phone surface — every conn, every verb — indefinitely.
- the turn-stream / modal-stream bootstrap target resolvers on the producer goroutine.
- the rotation watcher's `probeWithRetry` goroutine.

The repo already treats this hazard as real for its sibling: `reap.go` bounds its `ps` snapshot
with a 2s ctx timeout (`reapPSTimeout`) "so a hung ps cannot wedge teardown". This ticket applies
the identical discipline to the lsof probe. Darwin-only: the linux probe reads `/proc` directly
(`probe_linux.go`) and cannot hang on an external process.

## Design

One production file changes: `internal/sessions/rotation/probe_darwin.go`. Two package-level vars
are added (both darwin-build-only, both mirroring the reap seam), and `OpenJSONL` is rewritten to
run under a bounded context.

### Package-level declarations (contracts)

```go
// lsofProbeTimeout bounds the single lsof invocation so a hung lsof cannot wedge
// the v2 dispatch goroutine, a producer resolver, or the rotation watcher.
// Mirrors ptyrunner.reapPSTimeout. A var (not const) so probe tests can shrink
// the bound for a fast, deterministic timeout assertion.
var lsofProbeTimeout = 2 * time.Second

// newLsofCmd builds the lsof command. A package-var seam (mirroring
// ptyrunner.reapDescendantGroupsFn) so tests can point OpenJSONL at a fake slow
// or exit-1 binary without a real lsof or PATH manipulation. Production leaves it
// pointed here; tests swap it non-parallel and restore via t.Cleanup.
var newLsofCmd = func(ctx context.Context, pid int) *exec.Cmd {
    return exec.CommandContext(ctx, "lsof", "-nP", "-p", strconv.Itoa(pid), "-F", "fn")
}
```

Rationale for the seam choice (the one design call this ticket delegates): inject the **command
factory**, not a whole "run and return output" runner. This keeps the maximum surface under test —
the `context.WithTimeout` construction, both error classifications, and the parse loop all remain
production code exercised by a real subprocess that the real ctx kills. The seam replaces only
*which binary is exec'd*. This is the faithful mirror of the reap precedent, which injects the
reap function itself while leaving the ctx/timeout in production.

Rationale for `lsofProbeTimeout` being a `var`: the reap precedent used a `const` because reap's
tests swap the whole function and never exercise the ps timeout firing. AC 1 here **requires**
exercising the timeout firing, so the bound must be test-shrinkable to keep the test fast
(~100ms, not 2s) and to let it assert "returned well within the bound". A shrinkable-var timeout
is a standard Go test idiom and is the minimal change that makes AC 1 cheap and non-flaky.

### `OpenJSONL` rewrite (contract)

Signature is unchanged: `func (darwinProbe) OpenJSONL(pid int) (string, error)`. New body shape:

1. `ctx, cancel := context.WithTimeout(context.Background(), lsofProbeTimeout)`; `defer cancel()`.
2. `out, err := newLsofCmd(ctx, pid).Output()`.
3. On `err != nil`, classify in this order:
   - **timeout first:** `if ctx.Err() != nil { return "", fmt.Errorf("lsof probe timed out after %s: %w", lsofProbeTimeout, ctx.Err()) }`
   - **then the existing exit-1 benign path (verbatim):** `if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 { return "", nil }`
   - **else:** `return "", fmt.Errorf("lsof: %w", err)`.
4. On success: unchanged `parseLsofOutput` loop returning the first `.jsonl` name or `("", nil)`.

Ordering note (why the ctx.Err() check comes first): a context-timeout kills lsof with SIGKILL, so
`ProcessState.ExitCode()` returns `-1` — it would *not* be misclassified as exit-1 even without the
explicit check. The explicit `ctx.Err()` branch is nonetheless load-bearing for two reasons: (a) it
produces a clear, greppable timeout error instead of a bare signal-kill wrap, and (b) it is a
deterministic guard against any future platform quirk where a killed process surfaces a different
code. It is the "different fabric" deterministic net over the stochastic external command.

Imports: add `context` and `time`; keep `fmt`, `log/slog`, `os/exec`, `strconv`, `strings`.
No `WaitDelay` is set — lsof holds no inheritable pipe grandchildren, so `Output()` returns
promptly on kill (identical reasoning to reap's `descendantPGIDs`, which also omits `WaitDelay`).

## Concurrency model

No new goroutines. `context.WithTimeout` spawns os/exec's internal ctx-watcher goroutine, which
kills the process and is cleaned up by the deferred `cancel()`. The change is transparent to all
three callers: each still calls `OpenJSONL(pid)` synchronously and receives `(string, error)`. The
whole point is that the *caller's* goroutine (v2 dispatch, producer resolver, watcher) is now
guaranteed to unblock within `lsofProbeTimeout` instead of never.

## Error handling

| Condition | Result | Changed? |
|---|---|---|
| lsof exits 0, has a `.jsonl` fd | `(path, nil)` | no |
| lsof exits 0, no `.jsonl` fd | `("", nil)` | no |
| lsof exits 1 ("no files" / "process gone") | `("", nil)` | no — AC 2 |
| lsof exceeds `lsofProbeTimeout` | `("", <timeout error>)` — non-nil, within the bound | **new — AC 1** |
| lsof exits with any other non-1 code / spawn failure | `("", fmt.Errorf("lsof: %w", err))` | no |

The timeout error joins the existing `error` return channel; `probeWithRetry` (watcher.go:210-212)
already treats any probe error as `lastErr` + continue, and the v2 / resolver callers already
absorb probe errors as skip/transient. Hence AC 3: no caller change.

## Testing strategy

All tests go in `probe_darwin_test.go` (`//go:build darwin`). Use the `TestHelperProcess` re-exec
pattern (CODING-STYLE.md) — this package has none yet, so add one. The helper reads an env marker
and behaves as a fake lsof: mode `sleep` → sleep well past any test timeout (e.g. 10s); mode
`exit1` → `os.Exit(1)`. The test's `newLsofCmd` replacement builds
`exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")` with the mode set via
`cmd.Env` — threading `ctx` through `CommandContext` is what lets the timeout actually kill the
fake. Both replacement tests swap `newLsofCmd` (and, for the timeout test, `lsofProbeTimeout`)
non-parallel and restore via `t.Cleanup`, exactly as reap's tests do.

Scenarios (bullet form — developer writes the bodies in the project idiom):

- **Timeout fires within the bound (AC 1).** Shrink `lsofProbeTimeout` to ~100ms; point
  `newLsofCmd` at the `sleep` helper (would run 10s). Assert: `OpenJSONL` returns a non-nil error,
  and `time.Since(start) < 2s` (a generous ceiling — proves the bound fired and the test did not
  wait out the helper's 10s). Optionally assert `errors.Is(err, context.DeadlineExceeded)` via the
  wrapped `%w`.
- **Exit-1 stays benign (AC 2).** Leave the timeout at default; point `newLsofCmd` at the `exit1`
  helper. Assert `OpenJSONL(pid)` returns exactly `("", nil)` — no error. This is the first unit
  coverage of the exit-1 branch (previously only reachable via real lsof).
- **(Optional, cheap) success passthrough.** Point `newLsofCmd` at a helper that prints a canned
  `lsof -F fn` fixture (reuse `testdata/lsof_basic.txt`) and exits 0; assert the `.jsonl` path is
  returned. Confirms the seam did not disturb the happy path. Skip if it feels redundant with the
  existing `parseLsofOutput` tests.

The existing `parseLsofOutput` / `parseProcFD` / `noopProbe.Available` tests are untouched and
must still pass. `go test -race ./internal/sessions/rotation/` is the gate.

## Open questions

- **None blocking.** The seam-vs-PATH and const-vs-var calls are resolved above (command-factory
  seam + shrinkable timeout var). If the developer finds a same-package `TestHelperProcess` already
  landed by a sibling ticket, reuse it rather than adding a second — but as of this spec the
  package has none.
