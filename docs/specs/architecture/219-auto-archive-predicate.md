# #219 — Auto-archive predicate

Pure predicate `ShouldArchive(c, now)` over the `Conversation` type. No I/O, no clock injection, no goroutines. Underpins the Phase 3 sweep ticket (#220) by isolating the archival rule from the timer/loop that will call it.

## Files to read first

- `internal/conversations/conversation.go:29-72` — `Conversation` struct, specifically the `IsPromoted` and `LastUsedAt` field semantics. The predicate reads these two fields only.
- `internal/conversations/conversation_test.go` — table-driven test idiom in this package (stdlib `testing`, struct-of-cases pattern, `time.Date(...)` for deterministic timestamps without monotonic component). Mirror the style.
- `CODING-STYLE.md` — gofmt, doc comment style, table-driven tests.
- `docs/specs/architecture/216-conversation-type.md` — context for why `IsPromoted` is the auto-archive exemption bit and why `LastUsedAt` is "always present" (no zero-value handling needed at this layer).

## Context

Phase 3 auto-archive policy: unpromoted conversations idle for ≥30 days are archived; promoted channels are exempt. Splitting the policy into a pure predicate (this ticket) and a sweep loop (#220) lets us unit-test the rule with plain `time.Time` literals — no fake clock, no test goroutines, no scheduler — and keeps the sweep code thin enough to test by exercising only its I/O contract.

Pure-function-with-injected-`now` is the established Go idiom for time-dependent rules; it is the same shape `internal/sessions` uses for similar "is X expired" checks.

## Design

### Package layout

```
internal/conversations/
  archive.go          New: ShouldArchive
  archive_test.go     New: table-driven tests
```

One new production file, one new test file. No changes to `conversation.go`, `registry.go`, or any consumer — there are no consumers in this ticket; #220 will wire it up.

### The predicate

```go
package conversations

import "time"

// archiveIdleThreshold is the inactivity window after which an unpromoted
// conversation becomes eligible for auto-archive. Promoted channels are
// exempt regardless of LastUsedAt.
const archiveIdleThreshold = 30 * 24 * time.Hour

// ShouldArchive reports whether c should be auto-archived as of now.
//
// A conversation archives iff it is unpromoted (a discussion, not a channel)
// AND its LastUsedAt is at least archiveIdleThreshold in the past. The
// boundary is inclusive: exactly 30 days idle archives.
//
// Pure function. No I/O, no clock — the caller passes now. The sweep loop
// (#220) is responsible for picking now (typically time.Now()) and for
// iterating the registry.
func ShouldArchive(c Conversation, now time.Time) bool {
    if c.IsPromoted {
        return false
    }
    return now.Sub(c.LastUsedAt) >= archiveIdleThreshold
}
```

### Decisions called out in the AC

1. **Threshold is a package-level `const`, not an exported parameter.** The AC pins `30*24*time.Hour`. Naming it (`archiveIdleThreshold`) makes the test assertions self-documenting and gives #220 a single point to reference if it ever wants to surface the value (e.g., in log messages). It stays unexported — no caller currently needs to read or override it; an exported knob would be premature.
2. **Boundary is `>=`, not `>`.** AC says "exactly 30 days before now archives (boundary inclusive)." Encoding this as `>=` is the only correct read.
3. **No zero-value guard on `LastUsedAt`.** Per #216, `LastUsedAt` is "always present"; treating a zero-value `LastUsedAt` as "ancient and therefore archive" is the correct fallback if it ever does occur in practice. Adding a guard would be defending against a failure mode that #216's invariants already rule out (Evidence-Based Fix Selection).
4. **`now` is a parameter, not `time.Now()`.** Explicit per "Technical Notes" in the ticket. Lets the sweep ticket inject the loop's tick time; lets tests use literal `time.Date(...)` values without a clock interface.

### Data flow

```
caller (sweep, #220) ──> ShouldArchive(c, now) ──> bool
```

That is the entire diagram. No state, no channels, no goroutines.

## Concurrency model

None. Pure function over a value type; safe to call from any goroutine.

## Error handling

None. The function cannot fail — it reads two fields and returns a bool.

## Testing strategy

`internal/conversations/archive_test.go`, table-driven, stdlib `testing` only. One test function `TestShouldArchive` with a row per acceptance-criteria case plus the obvious negative for promoted-but-also-idle.

Anchor `now` to a single deterministic value at the top of the test (e.g., `now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)`) and express each row's `LastUsedAt` as `now.Add(-d)` for the relevant `d`. This keeps the boundary arithmetic readable.

### Required rows

| Name | `IsPromoted` | `LastUsedAt` | Expected |
|---|---|---|---|
| promoted, very idle | true | `now.Add(-365 * 24 * time.Hour)` | false (AC: promoted never archives) |
| unpromoted, exactly 30 days idle | false | `now.Add(-30 * 24 * time.Hour)` | true (AC: boundary inclusive) |
| unpromoted, 29d23h idle | false | `now.Add(-(29*24*time.Hour + 23*time.Hour))` | false (AC: just under threshold) |

That is the full AC matrix. One additional row pulls its weight:

- **unpromoted, just over threshold** — `now.Add(-(30*24*time.Hour + time.Second))`, expected true. Pairs with the boundary-inclusive case to lock in the direction of the inequality (catches an accidental flip to `<=`).

Skip a "promoted with recent activity" row — it tests the same `IsPromoted` short-circuit as the "promoted, very idle" row and adds no signal.

Construct the `Conversation` literal inline per row (not a shared fixture) — only `IsPromoted` and `LastUsedAt` matter; the other fields can stay at zero values without affecting the predicate. This keeps each row readable on its own.

## Open questions

None.
