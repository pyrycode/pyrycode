# `internal/conversations` auto-archive

Phase 3 auto-archive policy: unpromoted conversations idle for ≥30 days are eligible for archival; promoted channels are exempt regardless of idle time. Split into a pure predicate (this doc, #219) and a sweep loop that calls it on a timer (#220, not yet built).

## What it is

A single pure function:

```go
func ShouldArchive(c Conversation, now time.Time) bool
```

Returns `true` iff `!c.IsPromoted && now.Sub(c.LastUsedAt) >= 30*24*time.Hour`. No I/O, no clock — the caller passes `now`. Lives in `internal/conversations/archive.go`.

## Files

```
internal/conversations/
  archive.go          ShouldArchive + archiveIdleThreshold const (#219)
  archive_test.go     Table-driven boundary tests (#219)
```

Stdlib only (`time`). No new package surface beyond the one exported function.

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

## Decisions

### Threshold is an unexported package-level `const`

`archiveIdleThreshold = 30 * 24 * time.Hour`. Named (rather than inlined) so test assertions and any future log message in the sweep (#220) can reference one source of truth. Unexported because no caller currently needs to read or override it; an exported knob would be premature. The sweep can promote it to an exported var if a real configuration ask surfaces.

### Boundary is `>=`, not `>`

The AC pins "exactly 30 days idle archives (boundary inclusive)." Encoded as `>=`. The `unpromoted, just over threshold` test row pairs with the `exactly 30 days` row to lock in the direction of the inequality — an accidental flip to `<=` (or `<`) would fail one of the two.

### `now` is a parameter, not `time.Now()`

Pin the established Go idiom for time-dependent rules: take `now time.Time` as an argument; let the caller pick. The sweep loop (#220) injects its tick time; tests use literal `time.Date(...)` values without a clock interface. No fake clock, no test goroutines, no scheduler in the test path.

### No zero-value guard on `LastUsedAt`

Per #216, `LastUsedAt` is "always present" on a `Conversation` (it has no `omitempty` JSON tag and is bumped on every user activity by the future API layer). A zero-value `LastUsedAt` would make `ShouldArchive` return `true` (any unpromoted record older than 30 days archives, and `time.Time{}` is far older than any plausible `now`) — that is the correct fallback if the invariant ever breaks. Adding a guard would defend against a failure mode the type's invariants already rule out.

### Predicate-only, no sweep, no consumers

Splitting the policy into a pure predicate (this ticket) and an I/O-bound sweep loop (#220) keeps each piece testable in isolation: the rule is a table-driven unit test with `time.Date(...)` literals; the sweep, when it lands, is tested by exercising its I/O contract (which records it asks to remove, what it does on `Save` failure) without needing to also re-test the rule. The predicate has zero consumers in this slice.

## Tests

`archive_test.go`, table-driven, stdlib only. One test function `TestShouldArchive` with four rows, all anchored to a single deterministic `now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)` and expressing each row's `LastUsedAt` as `now.Add(-d)` for the relevant `d`:

| Name | `IsPromoted` | `LastUsedAt` offset | Expected |
|---|---|---|---|
| promoted, very idle | true | `-365 days` | false |
| unpromoted, exactly 30 days idle | false | `-30 days` | true |
| unpromoted, 29d23h idle | false | `-(29d + 23h)` | false |
| unpromoted, just over threshold | false | `-(30 days + 1s)` | true |

The first row pins the `IsPromoted` short-circuit (a promoted record with arbitrarily ancient `LastUsedAt` does not archive). Rows 2–4 pin the boundary direction. No "promoted with recent activity" row — it tests the same short-circuit as row 1 and adds no signal. Each `Conversation` literal is constructed inline; only `IsPromoted` and `LastUsedAt` matter — other fields stay at zero values.

Use `time.Date(...)` (no monotonic-clock component) so `time.Time` arithmetic is deterministic across machines. See `lessons.md` § "JSON roundtrip strips monotonic-clock state" for the parallel rule on the persistence side.

## Out of scope

- Sweep loop / archive action — #220 owns iterating the registry, calling `ShouldArchive`, and removing or moving the matching records.
- Archive destination — Phase 3 archives by removing the row (history retention is a separate concern); a future ticket can add an `archived.json` sidecar if recoverable archive is asked for.
- Configurable threshold — exported knob deferred until a real ask.
- Integration with `LastUsedAt` bumps — the future conversations API (rotate session, attach, send message) is what advances `LastUsedAt`; the predicate only reads it.

## Related

- [`features/conversations-package.md`](conversations-package.md) — the `Conversation` type, specifically the `IsPromoted` and `LastUsedAt` fields the predicate reads.
- [`features/conversations-registry.md`](conversations-registry.md) — the on-disk registry the sweep (#220) will iterate.
- [`docs/specs/architecture/219-auto-archive-predicate.md`](../../specs/architecture/219-auto-archive-predicate.md) — architect's spec for this ticket.
