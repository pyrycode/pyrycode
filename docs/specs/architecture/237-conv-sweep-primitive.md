# 237 — `conv`: in-memory auto-archive sweep primitive

## Files to read first

- `internal/conversations/registry.go:118-189` — existing `Create`/`Get`/`List`/`Update` methods. `Delete` mirrors their shape: mutex-guarded, no I/O, no validation, returns a single bool.
- `internal/conversations/registry.go:142-167` — `List` doc comment + body. Pins the "returns a copy" contract that the snapshot-safety test relies on.
- `internal/conversations/registry.go:34-37` — `Registry` struct (sole fields: `mu sync.Mutex`, `conversations []Conversation`). The slice is the only state to mutate.
- `internal/conversations/archive.go` — `ShouldArchive(c, now) bool` and the `archiveIdleThreshold` constant. `Sweep` is the predicate's only consumer in this ticket.
- `internal/conversations/archive_test.go` — table-driven shape and the `time.Date(...)` literal style for deterministic clocks. `TestSweep` reuses the same idiom.
- `internal/conversations/registry_test.go:136-194` — `TestRegistry_Get` table shape. `TestRegistry_Delete` mirrors it (hit / miss / delete-then-get).
- `internal/conversations/registry_test.go:393-413` — the existing "returned-slice-is-copy" subtest. Snapshot-safety for `Delete` extends the same invariant: a snapshot taken before `Delete` is unaffected by it.
- `internal/conversations/conversation.go` — `Conversation` field set (especially `ID`, `IsPromoted`, `LastUsedAt`) used by test fixtures.

## Context

Phase 3 of auto-archive. The predicate `ShouldArchive(c, now)` landed in #219; the daemon-side ticker + load/save wiring is a sibling ticket downstream. This ticket adds the two pure pieces that bridge them:

1. `Registry.Delete(id) bool` — the mutation primitive the registry has been deferring (#217 explicitly punted deletion until a consumer needed it; #220's sweep is that consumer).
2. `Sweep(reg, now) int` — the iterate-and-apply composition: snapshot via `List`, filter via `ShouldArchive`, mutate via `Delete`, return the count.

Both are pure logic. No clock injection, no goroutines, no I/O — `time.Time` is passed in by the (future) daemon caller. The split keeps the rule (`ShouldArchive`) and the loop (`Sweep`) independently testable, mirroring the predicate/sweep separation that #219 set up.

## Design

### `Registry.Delete`

```go
// Delete removes the conversation whose ID equals id. Returns true on hit,
// false on miss. Mutex-guarded; safe for concurrent use alongside the other
// Registry methods.
//
// Delete does NOT call Save — disk persistence is the caller's concern,
// matching the Create / Update / Promote convention.
func (r *Registry) Delete(id ConversationID) bool {
    r.mu.Lock()
    defer r.mu.Unlock()
    for i := range r.conversations {
        if r.conversations[i].ID == id {
            r.conversations = append(r.conversations[:i], r.conversations[i+1:]...)
            return true
        }
    }
    return false
}
```

Notes:

- O(n) linear scan — same shape as `Get` and `Update`. The registry is a small in-memory list (tens, low hundreds at most); a map index would be premature.
- Slice-element-removal idiom (`append(s[:i], s[i+1:]...)`) is the stdlib-idiomatic single-element delete. Order-preserving; inexpensive at this size.
- The byte-exact `ID` comparison matches `Get`'s contract.
- Returns on first match. The registry does not enforce ID uniqueness on `Create`, but `Sweep` iterates a snapshot of `List` exactly once per entry, so a duplicated ID would still be visited (and deleted) twice.

### `Sweep`

New file `internal/conversations/sweep.go`:

```go
package conversations

import "time"

// Sweep removes every conversation in reg for which ShouldArchive(c, now)
// returns true. Returns the number of entries archived.
//
// Sweep operates on a snapshot of reg.List(): the underlying slice may be
// modified (by Delete) during iteration without affecting the snapshot the
// loop is walking.
//
// Sweep does NOT call reg.Save — disk persistence is the daemon-wiring
// ticket's concern. Callers responsible for durability must Save themselves
// after Sweep returns.
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

Notes on shape:

- Return type is `int`, not `(int, error)`. Body composes `List` (cannot fail), `ShouldArchive` (pure predicate), and `Delete` (cannot fail). An error slot would be permanently dead.
- The `if reg.Delete(c.ID)` check is defensive against a concurrent deletion between `List` and `Delete` (e.g., a future caller invoking `Delete` from another goroutine); `n` only increments on confirmed removal.
- No logging, no metrics, no clock — those belong to the daemon-wiring ticket. Sweep is a primitive, not an operator.

### Why a separate file (`sweep.go`)

`registry.go` is the registry's data-mutation surface; `archive.go` holds the predicate. `Sweep` composes both — placing it in either file would couple unrelated concerns (a registry-internal change wouldn't necessarily touch sweep semantics). A third file keeps the boundary clean and matches the existing `archive.go` precedent.

## Concurrency model

No new goroutines. `Sweep` runs on the caller's goroutine.

The only concurrency invariant that matters: `List` returns a copy, so the for-range loop in `Sweep` walks a snapshot detached from `r.conversations`. `Delete`'s mid-iteration mutation of the underlying slice cannot disturb the loop, even though both operations happen under the same `r.mu` (serialized, not concurrent).

If a future caller invokes `Sweep` concurrently with another writer (e.g., a daemon goroutine handling a new conversation create), the `r.mu` discipline is sufficient: each `List`, `Delete`, and other-writer call serializes through the same lock; no race, but no transactional guarantee either (an entry that becomes archive-eligible between `List` and the corresponding `Delete` is fine — it deletes successfully; an entry recreated under the same ID between `List` and `Delete` would be deleted by `Sweep` even though it's no longer archive-eligible, but `Create` does not enforce ID uniqueness, so this is not a regression of any documented contract).

## Error handling

- `Registry.Delete` cannot fail. No I/O, no validation. Miss returns `false`; that is not an error.
- `Sweep` cannot fail. No I/O.
- The daemon-wiring ticket downstream is responsible for `Save` errors and any retry / observability around them.

## Testing strategy

Two new test functions, both stdlib `testing` only, both table-driven where the shape benefits.

### `TestRegistry_Delete` (in `registry_test.go`)

Subtests covering:

- **hit** — seed 1 entry, `Delete(id)` → returns `true`, `List()` length is 0, `Get(id)` → `ok == false`.
- **miss-empty-registry** — empty `Registry`, `Delete(id)` → returns `false`.
- **miss-non-matching** — seed 1 entry with id `A`, `Delete(B)` → returns `false`, the `A` entry is untouched.
- **delete-then-get-returns-false** — seed 1, `Delete`, `Get` → `ok == false`.
- **preserves-order** — seed entries `A`, `B`, `C`; `Delete(B)` → `List()` returns `[A, C]` in that order. (Pins the order-preserving slice idiom against accidental swap-with-last optimisations.)
- **snapshot-safety** — seed 2 entries, take `snap := r.List()`, `Delete(snap[0].ID)`, assert `snap` is unchanged in length and element identity. Pins the documented "List returns a copy" contract from #217.
- **delete-twice-second-misses** — seed 1, `Delete(id)` → `true`, `Delete(id)` → `false`.

Use the existing test fixtures and helpers (`ptrTo`, `mustParseTime`, `Conversation{ID: ..., Cwd: ...}` literal style). UUIDs follow the `11111111-2222-4333-8444-555555555555` pattern already in the file.

### `TestSweep` (new file `internal/conversations/sweep_test.go`)

Single primary table. Each row seeds a `*Registry`, calls `Sweep(reg, now)`, asserts:

1. The returned count equals the expected `N`.
2. `len(reg.List())` equals `len(seed) - N`.
3. Every surviving entry passes `!ShouldArchive(c, now) || c.IsPromoted` (i.e., either still within the idle window or promoted).

Required rows (use `time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)` as `now` to match `archive_test.go`):

- **empty-registry** — 0 entries seeded → returns 0.
- **all-archivable** — N=3 unpromoted entries with `LastUsedAt = now.Add(-31 * 24 * time.Hour)` → returns 3, `List()` is empty.
- **none-archivable-fresh** — 2 unpromoted entries with `LastUsedAt = now.Add(-7 * 24 * time.Hour)` → returns 0, both survive.
- **none-archivable-promoted-but-idle** — 2 promoted entries with `LastUsedAt = now.Add(-365 * 24 * time.Hour)` → returns 0, both survive.
- **mixed** — N archivable + M fresh-unpromoted + K promoted-but-idle (use 2/2/2) → returns N, `List()` length is M+K, every survivor is either promoted or fresh.
- **boundary-exactly-30-days** — 1 unpromoted entry with `LastUsedAt = now.Add(-30 * 24 * time.Hour)` → returns 1 (matches `ShouldArchive`'s inclusive boundary asserted by `TestShouldArchive`'s "exactly 30 days idle" row).

Promoted-entry name field can be left as the zero `*string` (nil) — `ShouldArchive` short-circuits on `IsPromoted` before touching `Name`.

### What NOT to test

- Concurrent invocation of `Sweep`. The daemon-wiring ticket may add it; here `Sweep` runs on the caller's goroutine and the existing `TestRegistry_ConcurrentReadWrite` already exercises `Registry`'s mutex discipline.
- Persistence. `Sweep` doesn't call `Save`; no disk side-effects to verify. The doc comment is the contract.

## Open questions

None. The acceptance criteria are unambiguous and the surrounding code (registry methods, predicate, test fixtures) is fully landed.

## Out of scope (do not implement here)

- Daemon-side ticker goroutine, load-on-tick, save-after-sweep — separate ticket.
- Logging or metrics emitted by `Sweep` — same.
- A clock interface or `Clock` type for injection — `now time.Time` is the injection point; tests pass deterministic literals.
- Map-based registry indexing for O(1) Delete — premature; the registry size and call frequency don't warrant it.
