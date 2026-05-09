# `internal/conversations` auto-archive

Phase 3 auto-archive policy: unpromoted conversations idle for ≥30 days are eligible for archival; promoted channels are exempt regardless of idle time. Split into a pure predicate (`ShouldArchive`, #219) and a pure iterate-and-apply primitive (`Sweep`, #237) that the future daemon-side ticker calls. The daemon wiring (load registry, ticker goroutine, save) is a sibling ticket downstream — neither piece here does I/O.

## What it is

Two pure functions:

```go
func ShouldArchive(c Conversation, now time.Time) bool
func Sweep(reg *Registry, now time.Time) int
```

`ShouldArchive` returns `true` iff `!c.IsPromoted && now.Sub(c.LastUsedAt) >= 30*24*time.Hour`. `Sweep` iterates `reg.List()`, applies the predicate, calls `reg.Delete` on each match, and returns the count archived. No I/O, no clock — the caller passes `now`.

## Files

```
internal/conversations/
  archive.go          ShouldArchive + archiveIdleThreshold const (#219)
  archive_test.go     Table-driven boundary tests (#219)
  sweep.go            Sweep — pure iterate-and-apply primitive (#237)
  sweep_test.go       Table-driven seeded-registry tests (#237)
```

Stdlib only (`time`). No new package surface beyond two exported functions. `Registry.Delete` (also #237) is the mutation primitive `Sweep` calls — see [`features/conversations-registry.md` § Delete](conversations-registry.md).

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

`Sweep` does NOT call `reg.Save` — disk persistence is the daemon-wiring ticket's concern. Callers responsible for durability call `Save` themselves after `Sweep` returns.

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

## Out of scope

- Daemon-side ticker, load-on-tick, save-after-sweep, logging or metrics — separate downstream ticket. `Sweep` is a primitive, not an operator.
- Archive destination — Phase 3 archives by removing the row (history retention is a separate concern); a future ticket can add an `archived.json` sidecar if recoverable archive is asked for.
- Configurable threshold — exported knob deferred until a real ask.
- Integration with `LastUsedAt` bumps — the future conversations API (rotate session, attach, send message) is what advances `LastUsedAt`; the predicate only reads it.
- Clock interface / `Clock` type for injection — `now time.Time` is the injection point; tests pass deterministic literals, no fake clock needed.
- Map-based registry indexing for O(1) `Delete` — premature; the registry size and call frequency don't warrant it.

## Related

- [`features/conversations-package.md`](conversations-package.md) — the `Conversation` type, specifically the `IsPromoted` and `LastUsedAt` fields the predicate reads.
- [`features/conversations-registry.md`](conversations-registry.md) — the on-disk registry the sweep iterates; `Registry.Delete` is the mutation primitive `Sweep` calls.
- [`docs/specs/architecture/219-auto-archive-predicate.md`](../../specs/architecture/219-auto-archive-predicate.md) — architect's spec for the predicate (#219).
- [`docs/specs/architecture/237-conv-sweep-primitive.md`](../../specs/architecture/237-conv-sweep-primitive.md) — architect's spec for the sweep primitive (#237).
