# 242 — `conv`: `RunSweepLoop` ticker helper

## Files to read first

- `internal/conversations/sweep.go` — `Sweep(reg *Registry, now time.Time) int`. The pure primitive this ticket wraps. The loop calls it once per tick with `time.Now()`; nothing else changes about its contract.
- `internal/conversations/registry.go:72-116` — `(*Registry).Save(path string) error`. The atomic-write recipe and the error-wrap shape (`registry: <step>: %w`) the ERROR-log assertion will see. Save can fail at MkdirAll / CreateTemp / Chmod / Encode / Sync / Close / Rename — any wrapped error counts as a "Save error" for the non-fatal path.
- `internal/conversations/archive.go:1-25` — `archiveIdleThreshold = 30 * 24 * time.Hour` plus the `ShouldArchive` predicate. `SweepInterval = time.Hour` is justified relative to this threshold (one missed tick is harmless; the entry archives an hour later).
- `internal/conversations/sweep_test.go:11-29` — the `seedSpec`/`mk` test fixture pattern (`time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)` literal `now`, formatted-int UUIDs `%08d-2222-...`, `idleDays`+`isPromoted`+`Cwd` fields). Loop tests reuse this idiom directly — **do not invent a new fixture style.**
- `internal/sessions/rotation/watcher.go:117-140` — `Watcher.Run(ctx) error` is the canonical "blocking ticker-shaped goroutine" in the project. Same `for { select { case <-ctx.Done(): ...; case <-... } }` skeleton applies here. The one delta: this AC says *return nil on ctx cancellation*, where the watcher returns `ctx.Err()`. Honour this AC's wording — see § Concurrency model below for why.
- `internal/sessions/rotation/watcher.go:142-194` — `handleCreate`: a single-tick handler factored out from the loop body so it's unit-testable without driving the loop. Same factoring rationale applies to `sweepOnce` here.
- `internal/sessions/pool.go:707-760` — `(*Pool).Run` and how it composes `g, gctx := errgroup.WithContext(ctx)` + `g.Go(func() error { return w.Run(gctx) })`. This is exactly the call site sibling #243 will mirror for `RunSweepLoop`.
- `internal/sessions/session_test.go:130-135`, `internal/sessions/pool_test.go:31-37` — the `slog.New(slog.NewTextHandler(io.Discard, nil))` test-logger pattern. Reuse it (or a `bytes.Buffer` handler when a test asserts on emitted records).
- `docs/specs/architecture/237-conv-sweep-primitive.md` — the immediate predecessor spec; pins what `Sweep` does and does not do (no Save, no logging, no clock). This ticket layers exactly the missing pieces above it.

## Context

Phase 3 auto-archive ships in three slices: predicate (#219, landed), pure primitive (#237, landed — `Sweep` + `Registry.Delete`), and **the I/O wrapper** (this ticket plus sibling #243).

This ticket owns the loop body in isolation: the long-running function that owns the ticker, the conditional-Save-after-nonzero-Sweep semantics, the INFO-on-archive / ERROR-on-Save-failure logging contract, and the "ctx cancellation returns nil; no final on-shutdown sweep" termination contract. Sibling #243 owns the daemon-side wiring: loading `conversations.json` at startup, plumbing the `*Registry` into `Pool.Run`'s errgroup, choosing the registry path, and any e2e test that exercises the loop through a live daemon.

Splitting the loop semantics here from the daemon wiring there is the same predicate / sweep / wrapping decomposition #237 used. The unit tests in `internal/conversations` cover tick / Save / log behaviour deterministically with no daemon scaffolding. The wiring ticket then has nothing left to assert about loop semantics — it only asserts about its own composition.

## Design

### File layout

One new production file, one new test file. No edits to existing files.

```
internal/conversations/
  sweep_loop.go         (new, ~40 LOC)
  sweep_loop_test.go    (new, ~150 LOC)
```

`sweep.go` (the pure primitive) stays untouched. `registry.go` stays untouched. `archive.go` stays untouched.

### Exported surface

```go
package conversations

import (
    "context"
    "log/slog"
    "time"
)

// SweepInterval is the production tick interval for RunSweepLoop. One hour is
// safe relative to archiveIdleThreshold (30 days): missing one tick delays an
// archive by one hour, which is harmless. Exposed (rather than baked into a
// constructor) so the daemon-wiring caller can pass it explicitly into
// RunSweepLoop alongside the test interval the unit tests use.
const SweepInterval = time.Hour

// RunSweepLoop ticks every interval and applies Sweep + (conditional) Save to
// reg. On each tick it calls Sweep(reg, time.Now()). When the returned count is
// non-zero it calls reg.Save(path); a successful Save logs at INFO with the
// archived count, a failed Save logs at ERROR and the loop continues to the
// next tick. A zero-count tick does NOT call Save (avoids fsync churn on every
// hour with no changes).
//
// Designed to be run as a goroutine inside an errgroup (or equivalent)
// supervising it via context cancellation. Returns nil on ctx cancellation.
// Does NOT perform a final on-shutdown sweep — by design; the next pyry start
// will run the next tick within SweepInterval.
//
// Save errors are ALWAYS non-fatal: a transient failure (e.g. disk briefly
// unwritable) must not bring down the daemon's errgroup. Tick frequency is
// hourly, so a single missed Save loses at most one hour of archived-count
// persistence; the next successful tick archives the same set again.
//
// Caller responsibilities:
//   - reg has been Loaded by the caller before this loop starts.
//   - path is the same path the caller will Load from on the next pyry start.
//   - log is non-nil (the pool's logger or slog.Default()).
//   - interval > 0 (callers in production pass SweepInterval; tests pass small
//     deterministic values). interval <= 0 panics via time.NewTicker.
func RunSweepLoop(ctx context.Context, reg *Registry, path string, interval time.Duration, log *slog.Logger) error {
    t := time.NewTicker(interval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return nil
        case <-t.C:
            sweepOnce(reg, path, log)
        }
    }
}
```

### Unexported helper

```go
// sweepOnce performs a single tick: Sweep + (conditional) Save + log. Factored
// out of RunSweepLoop so unit tests can drive the tick body deterministically
// without spinning a ticker. The loop is then a thin contract over this body
// (interval, ctx-cancellation, return value).
func sweepOnce(reg *Registry, path string, log *slog.Logger) {
    n := Sweep(reg, time.Now())
    if n == 0 {
        return
    }
    if err := reg.Save(path); err != nil {
        log.Error("conversations: sweep save failed", "err", err, "archived", n)
        return
    }
    log.Info("conversations: archived idle conversations", "count", n)
}
```

Notes:

- **Return type is plain `int → void`**. Sweep cannot fail (#237 spec); the only fail point is `reg.Save`, and the AC says swallow + log. The helper has no error to return upward.
- **The `if n == 0` short-circuit is the no-op-tick contract.** Pinned by the (b) test row below — without it, hourly fsyncs would churn on a fully-fresh registry indefinitely.
- **Log key conventions match `internal/sessions`:** `"err"` for the wrapped error (mirrors `rotation/watcher.go:137,154,172,181,190`); `"count"` and `"archived"` for the integer payload. Message strings are lowercase-prefixed with `"conversations: "` to match the package's `Save`-error wrap convention (`registry.go:87,91,97,103,107,110,113`) — gives operators one consistent grep target across log + wrapped-error text.
- **Logger is required, not optional.** No `if log == nil { log = slog.Default() }` guard. The package has no other code that synthesises a logger; making this function the one place that does would be inconsistent. Caller passes one; sibling #243 already plumbs `Pool.Run`'s logger. Nil-logger panic is loud and fast.

### Call site sketch (sibling #243, NOT this ticket)

For the architect's own validation that the signature is callable: sibling #243 will end up adding something shaped like

```go
g.Go(func() error {
    return conversations.RunSweepLoop(gctx, convReg, convPath, conversations.SweepInterval, log)
})
```

inside `Pool.Run` (or a thin wrapper invoked from `cmd/pyry/main.go`). The signature pre-commits to this shape: `ctx` first, registry + path + interval as data, logger last — same ordering as `rotation.Config` fields and `sessions.Config`. Do not implement the call site here.

### Why no `errgroup.Group` argument and no internal `g.Go`

The function is the body of one goroutine, not an orchestrator. The caller (sibling #243) owns the errgroup; this function only owns its single goroutine's work. Passing in an errgroup would invert the lifecycle: the loop would have authority to spawn additional goroutines, which it has no need for. Keeping it a plain blocking function makes it composable with any supervision shape (errgroup, raw `go`, future test harness, etc.).

### Why no clock interface

`time.Now()` is invoked once per tick. The deterministic-clock case is already served by the AC's escape hatch (`runOnce`-style helper) — `sweepOnce` is testable directly and `Sweep` itself takes `now` as a parameter (#219 / #237 chose this over a clock interface for the same reason). A `Clock` type for one `time.Now()` call would be over-engineered against existing precedent.

### Why a separate file (`sweep_loop.go`)

`sweep.go` holds the pure primitive (no I/O, no clock, no logging — that's its whole identity per #237). This file holds the I/O wrapper (clock, logger, conditional Save). Co-locating them in one file would erase the boundary that #237 deliberately drew. New file matches `archive.go` / `sweep.go` precedent (one orthogonal concern per file in this package).

## Concurrency model

Single goroutine: the one the caller spawns to run `RunSweepLoop`. The loop owns its `time.Ticker` exclusively (constructed and stopped inside the function). No additional goroutines.

Synchronisation: none beyond what `Registry`'s mutex already provides. `Sweep` takes `r.mu` per `List` and per `Delete` call (snapshot under the mutex, mutate under the mutex). `reg.Save` takes `r.mu` briefly to copy the slice and then releases it before doing disk I/O. Every other registry method (`Create`, `Update`, `Promote`, etc.) running concurrently with the loop is already serialised through `r.mu`. The loop introduces no new lock; it composes existing primitives that are already concurrency-safe.

Shutdown: `ctx.Done()` is one of the two select arms. When it fires, the loop returns nil and the deferred `t.Stop()` releases the ticker. **No final sweep.** AC explicit, and the rationale is durability symmetry: a final on-shutdown sweep would persist archive deletions that wouldn't have happened until up to one hour later under steady-state operation; not worth the extra complexity (and the failure mode of a sweep racing the daemon's other shutdown work is more bug surface than the missed sweep is worth recovering).

**Why return `nil` rather than `ctx.Err()` on cancellation.** AC requires nil. The errgroup contract says "the first goroutine to return a non-nil error wins"; returning `ctx.Err()` (== `context.Canceled` or `context.DeadlineExceeded`) on a clean shutdown would compete with whatever caused the cancellation in the first place, blurring the post-mortem signal. The `rotation.Watcher.Run` style of returning `ctx.Err()` predates auto-archive and is not changing here. Both are valid errgroup citizens; the AC pins one.

## Error handling

Two categorical failures the loop body sees:

1. **`reg.Save` fails on a non-zero-count tick.** Log at ERROR with the wrapped error and the count that *would have been* persisted; do NOT return. The next tick will retry — `Sweep` on the second tick walks the (now smaller) registry, archives nothing this time, and the no-op short-circuit fires. The disk state stays one hour stale until the next archivable entry shows up; acceptable for an hourly loop.

   *Stale-disk note (out of scope, but worth flagging for the wiring ticket):* if Save fails on tick T and never recovers before the daemon exits, the in-memory state has the deletions but the on-disk file does not. On next startup, those entries reappear in memory and re-archive on the first tick. Idempotent. No data loss; one extra archive log.

2. **`ctx.Done()` fires.** Return nil. No Save, no log. (See § Concurrency above.)

Failures the loop body *cannot* see:

- `Sweep` cannot fail (#237).
- `time.NewTicker` panics on `interval <= 0`. Caller-error; the doc comment names the pre-condition. No defensive guard.
- `log.Info` / `log.Error` cannot fail (`*slog.Logger` swallows handler errors per stdlib contract).

## Testing strategy

All tests are stdlib `testing` only, table-driven where the row count justifies it. New file `internal/conversations/sweep_loop_test.go`. Five tests; the three required by the AC plus two small contract pins.

### Logger fixture

```go
import (
    "bytes"
    "io"
    "log/slog"
)

func discardLogger() *slog.Logger {
    return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// captureLogger returns a logger and a buffer the test can inspect for
// "msg=..." substrings. Text handler is sufficient — the AC asserts on level
// and presence of an archived-count, both of which are visible in TextHandler
// output ("level=INFO" / "level=ERROR" / "count=N" / "archived=N").
func captureLogger() (*slog.Logger, *bytes.Buffer) {
    var buf bytes.Buffer
    return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}
```

Both fixtures are co-located in the test file; do not promote to a package-level helper (one consumer).

### `TestSweepOnce` — primary behavioural coverage (table-driven)

Drives `sweepOnce` directly. No ticker, no goroutine, no flake surface. Three rows:

- **(a) happy path** — seed 2 archivable + 1 fresh. Run `sweepOnce(reg, path, log)` once. Assert: post-call `len(reg.List()) == 1`; the on-disk file at `path` parses via `Load(path)` to a `*Registry` whose `List()` returns the same single survivor; the captured log buffer contains `level=INFO` and `count=2`. (Loading via the package's own `Load` is the cleanest "what does the persisted file actually contain" assertion.)

- **(b) no-op tick** — seed 0 archivable. Run `sweepOnce(reg, path, log)`. Assert: `os.Stat(path)` returns `errors.Is(err, fs.ErrNotExist)` (Save was not called → file never created); the captured log buffer is empty (no INFO, no ERROR). Pins the no-op short-circuit.

- **(c) Save error is non-fatal** — seed 1 archivable. Construct `path` such that `Save` is guaranteed to fail: pre-create a regular file at the directory location the saver would `MkdirAll` (`os.MkdirAll` returns ENOTDIR on a path component that exists as a file), e.g. `tmpDir + "/notadir/conversations.json"` after `os.WriteFile(tmpDir+"/notadir", ...)`. Run `sweepOnce(reg, path, log)`. Assert: the call returns (does not panic); captured log buffer contains `level=ERROR` and `archived=1`; `len(reg.List()) == 0` (the in-memory deletion happened — Save failure does not roll back Sweep). The "loop continues" half of the contract is asserted at the loop level by `TestRunSweepLoop_SaveErrorContinues` below.

  Rationale for the file-as-directory failure injection over alternatives: read-only-dir (`os.Chmod(tmpDir, 0o500)`) is fragile under root and on some CI runners; pointing `path` into a non-existent root device path is platform-specific; the file-where-a-dir-should-be trick is portable across Linux + macOS and self-cleaning via `t.TempDir()`.

### `TestRunSweepLoop_TicksAndCancels`

Exercises the loop's two select arms. Seed 2 archivable, 1 fresh. Spawn the loop in a goroutine with `interval = 5 * time.Millisecond`, captured logger, `t.TempDir()` path, and `ctx, cancel := context.WithCancel(context.Background())`. Poll for the on-disk file's existence with a 2-second deadline (`os.Stat`, 1ms tick) — that's the "loop has actually ticked at least once" signal. Once the file exists, `cancel()`. Assert the loop's returned error is `nil` within a 1-second deadline (`select` on a `done chan error`). Assert the file's contents (via `Load`) match the expected single survivor.

This is the only test that drives a real ticker. The 2-second poll deadline is defence-in-depth against slow CI: an interval of 5ms means the first tick fires in 5ms in steady state; even at 100× CI overshoot the deadline is generous. Race-free: the goroutine writes the file under `r.mu` via `Save`, the test reads via `os.Stat` (no shared in-memory state).

### `TestRunSweepLoop_NoOpDoesNotSave`

Seed 0 archivable. Spawn the loop with `interval = 1 * time.Millisecond`, `t.TempDir()` path, discard logger. Sleep `50 * time.Millisecond` (≥ 10 ticks at 1ms — enough for several confirmed ticks under any realistic CI overshoot; the budget is short enough that a slow CI run still completes in under 5s of wall-clock total). Cancel. Wait for the goroutine to return nil. Assert `os.Stat(path)` returns `fs.ErrNotExist`. Pins the no-op-tick contract at the loop level — without this, a regression that called Save unconditionally would only be caught by `TestSweepOnce` row (b) and could be papered over.

### `TestRunSweepLoop_SaveErrorContinues`

Seed 1 archivable. Use the same file-as-directory trick from `TestSweepOnce` row (c) so Save fails on every tick. Spawn the loop with `interval = 1 * time.Millisecond`, the broken path, captured logger. Sleep `50 * time.Millisecond` (≥ 10 attempted ticks). Cancel. Wait for goroutine to return nil. Assert the captured log buffer contains *at least two* `level=ERROR` records (proves the loop kept running after the first failure rather than terminating). Assert the loop returned nil (clean ctx-cancellation exit, not error propagation).

After the first tick, the registry is empty (the in-memory delete happened). On the second tick, `Sweep` returns 0, `sweepOnce` short-circuits, no further ERROR is logged. **So the test must seed enough entries (e.g. 10) to keep the registry non-empty across multiple ticks, OR re-seed via a periodic helper.** Simpler: seed 10 archivable; assert the buffer contains ≥ 2 ERROR records and 0 INFO records. With 10 archivables, the first tick archives all 10 in-memory; from tick 2 onward Sweep returns 0 and ERRORs stop. So actually the test can only observe a single ERROR before the registry empties — pin the assertion at exactly 1 ERROR + 0 INFO + ≥ 1 successful subsequent (no-op) tick (which leaves no log trace by definition).

  *Restated cleanly:* assert exactly the sequence `{1 ERROR, then 0 logs for the remaining ticks, then loop returns nil on cancel}`. The "loop kept running after the failure" claim is proven by the loop returning nil on cancel rather than returning the Save error or panicking. The "kept running" can also be observed by a second seed: after the initial Sweep clears the registry, the test calls `reg.Create(...)` to add another archivable while the loop is mid-run, sleeps another 50ms, expects a second ERROR. This is the cleanest version — it proves the loop is still ticking after the first ERROR, not just "the loop didn't crash on the first failure."

  Recommended shape: seed 1 archivable, sleep, observe 1 ERROR, `reg.Create` another archivable, sleep, observe 2 ERROR records total. Cancel. Assert nil return. Two confirmed survivor-of-failure ticks; minimal flake surface.

### What NOT to test

- Production interval value (asserting `SweepInterval == time.Hour` is a tautology test — the constant is its own contract).
- `Sweep` correctness — pinned by `TestSweep` (#237).
- `Registry.Save` atomicity / fsync behaviour — pinned by registry tests (#217).
- `ShouldArchive` boundary semantics — pinned by `TestShouldArchive` (#219).
- `time.NewTicker` panic on `interval <= 0` — stdlib contract; testing it tests the stdlib.
- Concurrent invocation of `RunSweepLoop` against the same registry — out of scope; sibling #243 will spawn at most one. If a future ticket wants two loops on the same registry, it owns its own coverage.
- e2e via the daemon — explicitly the sibling #243's territory.

## Open questions

None. Every AC is satisfied by an unambiguous code path; every design choice is anchored in an existing precedent (rotation watcher loop shape, sessions logger conventions, sweep-primitive splitting rationale).

## Out of scope (do not implement here)

- Loading `conversations.json` at daemon startup (sibling #243, in `cmd/pyry/main.go`).
- Plumbing `*Registry` through `Pool.Run`'s errgroup or whatever supervision surface #243 picks (sibling #243).
- Choosing the registry path (sibling #243; likely `~/.pyry/conversations.json` per `docs/knowledge/features/conversations-registry.md`).
- A configurable sweep interval flag or env var. The AC pins `time.Hour` and "Don't expose it as a flag." If a real operator ask surfaces, layer a `SweepInterval var = time.Hour` later.
- A clock interface. `time.Now()` is the injection point; `sweepOnce` is testable directly.
- A final on-shutdown sweep. AC explicit "does NOT perform any final on-shutdown sweep."
- Metrics emission (counter of archive runs, histogram of archived counts). No Phase 3 metrics surface exists yet; defer until one does.
- `ConversationsRegistry`-shaped façade types or mockable interfaces. The function takes the concrete `*Registry`; the package owns both. An interface seam is premature.
