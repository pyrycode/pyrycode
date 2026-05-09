# `internal/conversations` auto-archive

Phase 3 auto-archive policy: unpromoted conversations idle for ≥30 days are eligible for archival; promoted channels are exempt regardless of idle time. Three slices: a pure predicate (`ShouldArchive`, #219), a pure iterate-and-apply primitive (`Sweep`, #237), and the long-running ticker wrapper (`RunSweepLoop` + `sweepOnce` + `SweepInterval`, #242) that owns the tick / Save / log contract. The daemon wiring (load `conversations.json` at startup, plumb the `*Registry` into `Pool.Run`'s errgroup, choose the registry path) is sibling ticket #243.

## What it is

Two pure functions plus one long-running loop:

```go
func ShouldArchive(c Conversation, now time.Time) bool
func Sweep(reg *Registry, now time.Time) int

const SweepInterval = time.Hour
func RunSweepLoop(ctx context.Context, reg *Registry, path string, interval time.Duration, log *slog.Logger) error
```

`ShouldArchive` returns `true` iff `!c.IsPromoted && now.Sub(c.LastUsedAt) >= 30*24*time.Hour`. `Sweep` iterates `reg.List()`, applies the predicate, calls `reg.Delete` on each match, and returns the count archived. Neither does I/O.

`RunSweepLoop` ticks every `interval` (production: `SweepInterval = time.Hour`); on each tick it calls `Sweep(reg, time.Now())` and, only when the count is non-zero, `reg.Save(path)`. Successful Save logs at INFO with the count; failed Save logs at ERROR and the loop continues. Returns nil on `ctx.Done()`. Does not perform a final on-shutdown sweep. Designed to run as a single goroutine inside an errgroup.

## Files

```
internal/conversations/
  archive.go            ShouldArchive + archiveIdleThreshold const (#219)
  archive_test.go       Table-driven boundary tests (#219)
  sweep.go              Sweep — pure iterate-and-apply primitive (#237)
  sweep_test.go         Table-driven seeded-registry tests (#237)
  sweep_loop.go         RunSweepLoop + sweepOnce + SweepInterval (#242)
  sweep_loop_test.go    sweepOnce table + three loop-level tests (#242)
```

Stdlib only (`time`, `context`, `log/slog`). `Registry.Delete` (also #237) is the mutation primitive `Sweep` calls — see [`features/conversations-registry.md` § Delete](conversations-registry.md).

## How it works

Two-line body. Short-circuit on `IsPromoted`, then a single comparison:

```go
const archiveIdleThreshold = 30 * 24 * time.Hour

func ShouldArchive(c Conversation, now time.Time) bool {
    if c.IsPromoted {
        return false
    }
    return now.Sub(c.LastUsedAt) >= archiveIdleThreshold
}
```

Pure value semantics. Safe to call from any goroutine. Cannot fail — reads two fields, returns a bool.

`Sweep`'s body is a four-line composition:

```go
func Sweep(reg *Registry, now time.Time) int {
    n := 0
    for _, c := range reg.List() {
        if ShouldArchive(c, now) {
            if reg.Delete(c.ID) {
                n++
            }
        }
    }
    return n
}
```

The for-range walks the snapshot `List()` returns; `Delete`'s mutation of the underlying slice cannot disturb the loop because the snapshot is detached. The `if reg.Delete(...)` check is defensive against a hypothetical concurrent deletion between `List` and `Delete` — `n` only increments on confirmed removal. Returns `int`, not `(int, error)`: the body composes operations that cannot fail (`List`, `ShouldArchive`, `Delete`), so an error slot would be permanently dead. No goroutines spawned; runs on the caller's goroutine.

`Sweep` does NOT call `reg.Save` — durability is the loop's concern. Callers responsible for durability call `Save` themselves after `Sweep` returns; in production that caller is `sweepOnce`.

`RunSweepLoop`'s body is the canonical project ticker shape (mirrors `internal/sessions/rotation/watcher.go:Run`):

```go
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

`sweepOnce` is the single-tick body factored out so unit tests can drive Sweep + Save + log without spinning a ticker; the loop is then a thin contract over it (interval, ctx-cancellation, return value). Save errors are always non-fatal — a transient failure (e.g. disk briefly unwritable) must not bring down the daemon's errgroup. The next successful tick archives the same set again; idempotent.

## Decisions

### Threshold is an unexported package-level `const`

`archiveIdleThreshold = 30 * 24 * time.Hour`. Named (rather than inlined) so test assertions and any future log message in the sweep (#220) can reference one source of truth. Unexported because no caller currently needs to read or override it; an exported knob would be premature. The sweep can promote it to an exported var if a real configuration ask surfaces.

### Boundary is `>=`, not `>`

The AC pins "exactly 30 days idle archives (boundary inclusive)." Encoded as `>=`. The `unpromoted, just over threshold` test row pairs with the `exactly 30 days` row to lock in the direction of the inequality — an accidental flip to `<=` (or `<`) would fail one of the two.

### `now` is a parameter, not `time.Now()`

Pin the established Go idiom for time-dependent rules: take `now time.Time` as an argument; let the caller pick. The sweep loop (#220) injects its tick time; tests use literal `time.Date(...)` values without a clock interface. No fake clock, no test goroutines, no scheduler in the test path.

### No zero-value guard on `LastUsedAt`

Per #216, `LastUsedAt` is "always present" on a `Conversation` (it has no `omitempty` JSON tag and is bumped on every user activity by the future API layer). A zero-value `LastUsedAt` would make `ShouldArchive` return `true` (any unpromoted record older than 30 days archives, and `time.Time{}` is far older than any plausible `now`) — that is the correct fallback if the invariant ever breaks. Adding a guard would defend against a failure mode the type's invariants already rule out.

### Predicate / sweep / wiring split

Three slices: predicate (`ShouldArchive`, #219), sweep primitive (`Sweep`, #237), daemon wiring (ticker + load + save, future ticket). The first two are pure; the third does I/O. Each is testable in isolation — the rule is a four-row table-driven unit test with `time.Date(...)` literals; the sweep is a six-row table-driven test that seeds an in-memory `Registry` and asserts the count plus the survivors; the daemon wiring will be tested by exercising its I/O contract (load failure, save failure, tick interleave) without re-testing rule or sweep.

### `Sweep` returns `int`, not `(int, error)`

The body composes `List` (cannot fail), `ShouldArchive` (pure predicate), and `Delete` (cannot fail). An error slot would be permanently dead weight. The daemon-wiring ticket downstream is responsible for `Save` errors and any retry / observability around them — those are the operations that can actually fail.

### `Sweep` lives in its own file (`sweep.go`)

`registry.go` is the registry's data-mutation surface; `archive.go` holds the predicate. `Sweep` composes both — placing it in either file would couple unrelated concerns (a registry-internal change wouldn't necessarily touch sweep semantics). A third file keeps the boundary clean and matches the existing `archive.go` precedent.

### `Registry.Delete` is the consumer-driven addition

#217 explicitly deferred `Delete` until a real consumer surfaced ("conversations are not deleted in Phase 3 — they're archived via `IsPromoted` flips"). The auto-archive sweep is that consumer: archival is implemented as removal-from-registry. The two arrived together in #237 to keep the deletion semantics and the sweep semantics evolving in lockstep.

### `SweepInterval = time.Hour`, exported

Production tick interval is one hour, declared as the exported package-level constant `SweepInterval`. Justified relative to `archiveIdleThreshold = 30 * 24 * time.Hour`: missing one tick delays an archive by one hour, harmless. Exposed (rather than baked into a constructor) so the daemon-wiring caller in #243 can pass it explicitly to `RunSweepLoop` alongside the small interval the unit tests use. Not exposed as a flag or env var — the AC pins the value.

### `RunSweepLoop` returns `nil` on ctx cancellation, NOT `ctx.Err()`

AC requires nil. The errgroup contract says "the first goroutine to return a non-nil error wins"; returning `ctx.Err()` (== `context.Canceled` or `context.DeadlineExceeded`) on a clean shutdown would compete with whatever caused the cancellation in the first place, blurring the post-mortem signal. The `rotation.Watcher.Run` style of returning `ctx.Err()` predates auto-archive and is not changing here — both are valid errgroup citizens; the AC pins one for this loop.

### Save errors are non-fatal — log at ERROR, do not return

A failed `reg.Save` on a non-zero-count tick logs ERROR with the wrapped error and the count that *would have been* persisted, then continues. The next tick will retry. The disk file stays one hour stale; acceptable for an hourly loop. Stale-disk note for the wiring ticket: if Save fails on tick T and never recovers before the daemon exits, the in-memory state has the deletions but the on-disk file does not. On next startup the entries reappear in memory and re-archive on the first tick. Idempotent; no data loss; one extra archive log.

### No final on-shutdown sweep

AC explicit. Rationale is durability symmetry: a final on-shutdown sweep would persist deletions that wouldn't have happened until up to one hour later under steady-state operation; not worth the extra complexity (and the failure mode of a sweep racing the daemon's other shutdown work is more bug surface than the missed sweep is worth recovering).

### No-op tick short-circuits before `Save`

`if n == 0 { return }` in `sweepOnce` is the no-op contract: a tick that finds nothing to archive must not call `Save`. Without it, hourly fsyncs would churn on a fully-fresh registry indefinitely. Pinned by `TestSweepOnce/no-op-tick` and by `TestRunSweepLoop_NoOpDoesNotSave` (which sleeps through ≥10 ticks and asserts `os.Stat` returns `fs.ErrNotExist`).

### No clock interface; no `errgroup.Group` argument

`time.Now()` is invoked once per tick from `sweepOnce`, which is testable directly without driving the loop — `Sweep` itself takes `now` as a parameter (#219 / #237 chose this over a clock interface for the same reason). A `Clock` type for one `time.Now()` call would be over-engineered against existing precedent. And: `RunSweepLoop` is the body of one goroutine, not an orchestrator. The caller (sibling #243) owns the errgroup; this function only owns its single goroutine's work. Passing in an errgroup would invert the lifecycle.

### Logger required, not optional

No `if log == nil { log = slog.Default() }` guard. The package has no other code that synthesises a logger; making this function the one place that does would be inconsistent. Caller passes one; sibling #243 plumbs `Pool.Run`'s logger. Nil-logger panic is loud and fast.

### Log key conventions match `internal/sessions`

`"err"` for the wrapped error (mirrors `rotation/watcher.go`); `"count"` for the successful-archive integer; `"archived"` for the would-have-been-persisted integer in the ERROR record. Message strings are lowercase-prefixed with `"conversations: "` to match the package's `Save`-error wrap convention (`registry.go`) — gives operators one consistent grep target across log + wrapped-error text.

### `RunSweepLoop` lives in its own file (`sweep_loop.go`)

`sweep.go` holds the pure primitive (no I/O, no clock, no logging — that's its whole identity per #237). `sweep_loop.go` holds the I/O wrapper (clock, logger, conditional Save). Co-locating them in one file would erase the boundary that #237 deliberately drew. New file matches the `archive.go` / `sweep.go` precedent (one orthogonal concern per file in this package).

## Tests

### `TestShouldArchive` (`archive_test.go`)

Table-driven, stdlib only. One test function `TestShouldArchive` with four rows, all anchored to a single deterministic `now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)` and expressing each row's `LastUsedAt` as `now.Add(-d)` for the relevant `d`:

| Name | `IsPromoted` | `LastUsedAt` offset | Expected |
|---|---|---|---|
| promoted, very idle | true | `-365 days` | false |
| unpromoted, exactly 30 days idle | false | `-30 days` | true |
| unpromoted, 29d23h idle | false | `-(29d + 23h)` | false |
| unpromoted, just over threshold | false | `-(30 days + 1s)` | true |

The first row pins the `IsPromoted` short-circuit (a promoted record with arbitrarily ancient `LastUsedAt` does not archive). Rows 2–4 pin the boundary direction. No "promoted with recent activity" row — it tests the same short-circuit as row 1 and adds no signal. Each `Conversation` literal is constructed inline; only `IsPromoted` and `LastUsedAt` matter — other fields stay at zero values.

Use `time.Date(...)` (no monotonic-clock component) so `time.Time` arithmetic is deterministic across machines. See `lessons.md` § "JSON roundtrip strips monotonic-clock state" for the parallel rule on the persistence side.

### `TestSweep` (`sweep_test.go`)

Single primary table, one row per scenario. Each row seeds a `*Registry`, calls `Sweep(reg, now)`, asserts the returned count, the post-sweep `len(reg.List())`, and that every surviving entry passes `!ShouldArchive(c, now) || c.IsPromoted`. Anchored to the same `now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)` as the predicate test:

| Row | Seed | Expected count |
|---|---|---|
| empty-registry | 0 entries | 0 |
| all-archivable | 3 unpromoted, `LastUsedAt = now-31d` | 3 |
| none-archivable-fresh | 2 unpromoted, `LastUsedAt = now-7d` | 0 |
| none-archivable-promoted-but-idle | 2 promoted, `LastUsedAt = now-365d` | 0 |
| mixed | 2 archivable + 2 fresh-unpromoted + 2 promoted-but-idle | 2 |
| boundary-exactly-30-days | 1 unpromoted, `LastUsedAt = now-30d` | 1 |

The `boundary-exactly-30-days` row mirrors the predicate's "exactly 30 days idle" row at the sweep level — pins that the inclusive boundary survives the iterate-and-apply layer. The `none-archivable-promoted-but-idle` row pins that `Sweep` does not delete promoted records regardless of idle time. Promoted-entry name fields stay zero (`*string` nil) — `ShouldArchive` short-circuits on `IsPromoted` before touching `Name`.

Concurrent invocation is not tested here — `Sweep` runs on the caller's goroutine and `TestRegistry_ConcurrentReadWrite` already exercises `Registry`'s mutex discipline. Persistence is not tested either — `Sweep` does not call `Save`; the doc comment is the contract.

### `TestSweepOnce` (`sweep_loop_test.go`)

Drives `sweepOnce` directly. No ticker, no goroutine, no flake surface. Three subtests under one `t.Parallel()` parent:

| Subtest | Seed | Asserts |
|---|---|---|
| happy-path | 2 archivable + 1 fresh | `len(reg.List()) == 1`; on-disk file at `path` re-`Load`s to a single-entry registry; log buffer contains `level=INFO` and `count=2`. |
| no-op-tick | 1 fresh | `os.Stat(path)` returns `fs.ErrNotExist` (Save not called → file never created); log buffer is empty. |
| save-error-non-fatal | 1 archivable | Sweep happens (`len(reg.List()) == 0` — in-memory deletion is not rolled back); log buffer contains `level=ERROR` and `archived=1`. |

The Save-error injection is the **file-as-directory trick**: `os.WriteFile(tmpDir+"/notadir", …)` followed by passing `tmpDir+"/notadir/conversations.json"` as the path. `os.MkdirAll` inside `Registry.Save` returns ENOTDIR. Portable across Linux + macOS, self-cleaning via `t.TempDir()`, no special perms required. Chosen over `os.Chmod(tmpDir, 0o500)` (fragile under root and on some CI runners) and platform-specific device paths.

### `TestRunSweepLoop_TicksAndCancels` (`sweep_loop_test.go`)

The only test that drives a real ticker. Seed 2 archivable + 1 fresh. Spawn the loop with `interval = 5 * time.Millisecond`, captured logger, `t.TempDir()` path. Poll for the on-disk file's existence with a 2-second deadline — that's the "loop has actually ticked at least once" signal. Once the file exists, `cancel()`. Assert the loop returns `nil` within 1 second. Assert the file's contents (via `Load`) match the expected single survivor (`Cwd == "/fresh"`).

### `TestRunSweepLoop_NoOpDoesNotSave` (`sweep_loop_test.go`)

Seed 0 archivable. Spawn the loop with `interval = time.Millisecond`, discard logger. Sleep 50ms (≥ 10 ticks at 1ms). Cancel. Wait for goroutine to return nil. Assert `os.Stat(path)` returns `fs.ErrNotExist`. Pins the no-op-tick contract at the loop level — without this, a regression that called Save unconditionally would only be caught by the `sweepOnce` subtest above and could be papered over.

### `TestRunSweepLoop_SaveErrorContinues` (`sweep_loop_test.go`)

The "loop survived the Save failure" proof. Seed 1 archivable; use the file-as-directory broken path; spawn the loop with `interval = time.Millisecond` and a captured logger. Wait until ≥1 ERROR record appears (deadline 2s). Re-seed: `seedArchivables(reg, 1, 100)` adds another archivable while the loop is mid-run, so the next tick finds something to Sweep + Save (and Save fails again). Wait until ≥2 ERROR records appear. Cancel. Assert the loop returned nil; assert no `level=INFO` ever appeared (Save always failed). Two confirmed survivor-of-failure ticks; minimal flake surface.

The re-seed is load-bearing: after the first tick the registry is empty, so without re-seeding `Sweep` would return 0 forever and the `n == 0` short-circuit would silence subsequent ticks. The test would still observe one ERROR but couldn't distinguish "loop kept running" from "loop happened to crash silently after the first failure."

### Logger fixtures

`discardLogger()` returns a no-op `*slog.Logger` for tests that don't read logs. `captureLogger()` returns a logger writing to a `*syncBuffer` (mutex-wrapped `bytes.Buffer`) — `bytes.Buffer` is not safe for concurrent use, and the loop goroutine writes while the test reads. Text handler is sufficient: `level=INFO` / `level=ERROR` / `count=N` / `archived=N` are visible substrings. Both fixtures and the `seedArchivables` / `brokenSavePath` / `waitFor` / `countSubstring` helpers are co-located in the test file; not promoted (one consumer per package today).

## Out of scope

- Loading `conversations.json` at daemon startup — sibling #243, in `cmd/pyry/main.go`.
- Plumbing `*Registry` through `Pool.Run`'s errgroup or whatever supervision surface #243 picks — sibling #243.
- Choosing the registry path (likely `~/.pyry/conversations.json` per [`features/conversations-registry.md`](conversations-registry.md)) — sibling #243.
- e2e via the daemon — explicitly the sibling #243's territory.
- A configurable sweep interval flag or env var — AC pins `time.Hour` and "Don't expose it as a flag." If a real operator ask surfaces, layer a `SweepInterval var = time.Hour` later.
- Final on-shutdown sweep — AC explicit "does NOT perform any final on-shutdown sweep."
- Metrics emission (counter of archive runs, histogram of archived counts) — no Phase 3 metrics surface exists yet; defer until one does.
- `ConversationsRegistry`-shaped façade types or mockable interfaces — the function takes the concrete `*Registry`; the package owns both. An interface seam is premature.
- Archive destination — Phase 3 archives by removing the row (history retention is a separate concern); a future ticket can add an `archived.json` sidecar if recoverable archive is asked for.
- Configurable threshold — exported knob deferred until a real ask.
- Integration with `LastUsedAt` bumps — the future conversations API (rotate session, attach, send message) is what advances `LastUsedAt`; the predicate only reads it.
- Clock interface / `Clock` type for injection — `now time.Time` is the injection point; tests pass deterministic literals, no fake clock needed.
- Map-based registry indexing for O(1) `Delete` — premature; the registry size and call frequency don't warrant it.

## Related

- [`features/conversations-package.md`](conversations-package.md) — the `Conversation` type, specifically the `IsPromoted` and `LastUsedAt` fields the predicate reads.
- [`features/conversations-registry.md`](conversations-registry.md) — the on-disk registry the sweep iterates; `Registry.Delete` is the mutation primitive `Sweep` calls; `Registry.Save` is what `sweepOnce` calls.
- [`features/rotation-watcher.md`](rotation-watcher.md) — `Watcher.Run`, the canonical project ticker-shaped goroutine `RunSweepLoop` mirrors (one delta: this loop returns nil on ctx cancellation; the watcher returns `ctx.Err()`).
- [`docs/specs/architecture/219-auto-archive-predicate.md`](../../specs/architecture/219-auto-archive-predicate.md) — architect's spec for the predicate (#219).
- [`docs/specs/architecture/237-conv-sweep-primitive.md`](../../specs/architecture/237-conv-sweep-primitive.md) — architect's spec for the sweep primitive (#237).
- [`docs/specs/architecture/242-conv-sweep-loop.md`](../../specs/architecture/242-conv-sweep-loop.md) — architect's spec for the loop wrapper (#242).
