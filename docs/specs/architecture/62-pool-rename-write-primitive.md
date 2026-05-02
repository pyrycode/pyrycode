# Phase 1.1c-A — `Pool.Rename` write primitive

**Ticket:** [#62](https://github.com/pyrycode/pyrycode/issues/62)
**Size:** XS (one method, no new exported types, one test file).
**Scope:** `internal/sessions` only. No control-plane, no CLI, no wire protocol.

## Context

Phase 1.1c's `pyry sessions rename <id> <label>` CLI verb (ticket 47-B) needs a
typed Go API to mutate a session's label without touching `sessions.json`
directly. Today's `Pool` exposes `Lookup`/`Default`/`Snapshot`/`List` for reads
and `Create`/`RotateID`/`Activate` for state-changing flows that already write
the registry — but no method to edit a label. The matching wire/CLI surface in
47-B consumes this primitive; nothing in this ticket touches `internal/control`
or `cmd/pyry`.

The registry already persists a `label` field per entry
(`internal/sessions/registry.go:24`); the only thing missing is a typed,
pool-locked mutator that flows through the existing `saveLocked` write path so
on-disk state can never diverge from in-memory state.

## Design

### One new exported method, no new types

Add to `internal/sessions/pool.go`:

```go
// Rename updates the named session's label and persists the change to the
// registry. Empty newLabel is permitted and clears the on-disk label to "";
// Pool.List's synthetic "bootstrap" substitution continues to apply when the
// bootstrap's on-disk label is empty.
//
// Returns ErrSessionNotFound when id is not present in the pool. On the
// not-found path the in-memory registry and the on-disk sessions.json are
// byte-identical to their prior state — saveLocked is not invoked.
//
// Concurrency: takes p.mu (write) for the read-modify-write and holds it
// across the persisted file write, matching the RotateID/saveLocked
// invariant. Concurrent Pool.List/Lookup/Snapshot calls block on Pool.mu
// briefly; nothing else needs synchronisation.
//
// Lock order: Pool.mu (write). Does not take Session.lcMu — Session.label is
// guarded by Pool.mu (the only other readers are List and saveLocked, both
// under Pool.mu).
func (p *Pool) Rename(id SessionID, newLabel string) error
```

### Implementation sketch

```go
func (p *Pool) Rename(id SessionID, newLabel string) error {
    p.mu.Lock()
    defer p.mu.Unlock()
    sess, ok := p.sessions[id]
    if !ok {
        return ErrSessionNotFound
    }
    if sess.label == newLabel {
        // Same value — no in-memory change AND no disk write. Preserves the
        // AC's "byte-identical to prior state" guarantee for the trivial
        // case, and keeps the registry mtime stable for an idempotent rename.
        return nil
    }
    prev := sess.label
    sess.label = newLabel
    if err := p.saveLocked(); err != nil {
        sess.label = prev
        return err
    }
    return nil
}
```

The save-failure rollback is a small belt-and-suspenders: if `saveLocked`
returns an error, the on-disk file is untouched (the temp+rename discipline in
`saveRegistryLocked` makes the rename the commit point — partial writes are
unreachable). Reverting the in-memory label keeps in-memory and on-disk state
identical so a subsequent retry has consistent inputs and a `Lookup` after a
failed `Rename` doesn't return a label that isn't on disk.

### Why no `Session.lcMu`

`Session.label` is currently documented in `session.go` as "immutable post-New
from the lifecycle goroutine's perspective." That doc comment needs a small
update — Pool.Rename mutates it — but the underlying invariant is unchanged:
the lifecycle goroutine in `Session.Run` does not read `label`. The only
readers are:

- `Pool.List` — under `Pool.mu` (RLock)
- `Pool.saveLocked` — caller holds `Pool.mu` (write)

So `Pool.mu` is the correct guard for `label`, not `lcMu`. Rename takes
`Pool.mu` (write); the existing readers either share the read lock or hold the
write lock — no torn reads, no new lock-order edges.

The `RotateID` precedent applies directly: it also mutates a `Session` field
(`sess.id`) under `Pool.mu` (write) without taking `lcMu`, for the same
reason — `id` is read off-`lcMu` by every caller that already holds `Pool.mu`.

### Why hold `Pool.mu` across the disk write

The AC says it explicitly: "released only after the persisted file write
returns." This matches `RotateID` and every `Pool.persist`-from-`Session`
callback. Releasing the lock between the in-memory mutation and the disk write
would let a concurrent `Lookup` observe the new label while the disk still has
the old one — a window where in-memory and on-disk state disagree, exactly the
kind of "registry-only-entry-versus-truth" drift the existing locking
discipline rules out.

The cost is bounded: `saveLocked` is one `os.CreateTemp` + encode + fsync +
rename. Sub-millisecond on any reasonable disk; two orders of magnitude faster
than a `claude` spawn.

### Synthetic-bootstrap-label semantics

The AC has two related sub-clauses:

1. **Empty newLabel clears the on-disk label to `""`** — `List`'s synthetic
   `"bootstrap"` substitution (introduced in #60) continues to apply.
2. **Renaming bootstrap to a non-empty label** — that label is reflected
   verbatim by `List`; no synthetic substitution.

Both fall out of the implementation for free. `Rename` writes the verbatim
string through to disk; `List`'s substitution rule (`if s.bootstrap && label
== "" { label = "bootstrap" }`) applies the synthetic name only when the
on-disk value is empty. We do not branch on `sess.bootstrap` inside `Rename` —
the AC explicitly permits clearing the bootstrap label, and the `List`-side
substitution makes that operator-visible-default work correctly without
special-casing the writer.

### Doc-comment update on `Session.label`

`session.go:74-76` currently reads:

```go
// Persisted metadata. label/createdAt/bootstrap are immutable post-New
// from the lifecycle goroutine's perspective. lastActiveAt is bumped
// under lcMu on every state transition.
```

Update to:

```go
// Persisted metadata. createdAt and bootstrap are immutable post-New.
// label is immutable from the lifecycle goroutine's perspective but may be
// mutated by Pool.Rename under Pool.mu (write); other readers hold Pool.mu
// (RLock or Lock). lastActiveAt is bumped under lcMu on every state
// transition.
```

This is a doc-only change; one block comment, ~3 lines diff.

## Concurrency model

- **Lock order:** `Pool.mu` (write) only. No `Session.lcMu` involved.
  Identical to `RotateID`. No new lock-order edges.
- **Concurrent `Rename` + `List`:** standard RWMutex — `List`'s RLock either
  observes the pre-Rename label (consistent with the on-disk pre-state) or
  the post-Rename label (consistent with the on-disk post-state); never a
  torn read.
- **Concurrent `Rename` + `Rename` on the same id:** serialised by `Pool.mu`
  (write). Last writer wins, both writes persist (the second
  `saveLocked` runs against the post-first-write in-memory state).
- **Concurrent `Rename` + `Activate`/`Evict`:** the lifecycle goroutine never
  reads `Session.label`, so there is no race here regardless of locking.
  `Pool.persist` (called from `Session.transitionTo`) will serialise behind
  `Rename` on `Pool.mu` and write whatever the in-memory label is at that
  moment — which is exactly what should be on disk.
- **Concurrent `Rename` + `Create`:** `Create` takes `Pool.mu` (write) for the
  registration+persist couple, then releases. Either `Rename` runs before
  the new id exists (returns `ErrSessionNotFound` for that id, naturally) or
  after (rename works as normal).

## Error handling

Two error sources:

1. **Unknown id** — return `ErrSessionNotFound` (the existing sentinel from
   `pool.go:28`). No `saveLocked` invocation, no in-memory mutation; on-disk
   bytes unchanged. The AC's "byte-identical to the prior state" is a direct
   consequence of returning before any mutation.

2. **`saveLocked` failure** — return the wrapped error from `saveLocked`
   verbatim. The rollback (`sess.label = prev`) makes the in-memory state
   match the on-disk state (both at the prior value). Caller can retry. We
   do **not** wrap the error in a `Rename`-specific prefix — `saveLocked` /
   `saveRegistryLocked` already produce well-prefixed errors
   (`registry: …`); double-wrapping adds no information and breaks
   `errors.Is` symmetry with other persist sites (`RotateID`, `Create`,
   `Session.transitionTo` all return the save error verbatim).

No new sentinel errors. No new exported types.

## Testing strategy

New file `internal/sessions/pool_rename_test.go` (kept separate from
`pool_test.go` so the file scope is obvious; same package, no test seam
needed). Same shape as `pool_list_test.go` (#60).

Five tests, mapping 1:1 to the AC's test list:

1. **`TestPool_Rename_RoundTrip`** — `helperPoolPersistent(t, regPath)`,
   capture `pool.Default().ID()` (the bootstrap), call
   `pool.Rename(id, "main")` → assert `nil` error. Re-read the registry from
   disk via `loadRegistry(regPath)` and assert `pickBootstrap(reg).Label ==
   "main"`. Call `pool.List()` and assert `list[0].Label == "main"`
   (synthetic substitution suppressed because on-disk label is non-empty).
   Covers AC #1 first half + AC #2.

2. **`TestPool_Rename_EmptyClears`** — same setup, but first set the label to
   `"foo"`, then call `pool.Rename(id, "")`. Assert the on-disk label is
   `""` and `pool.List()[0].Label == "bootstrap"` (synthetic substitution
   resumes). Covers AC #1 second half.

3. **`TestPool_Rename_UnknownID`** — `helperPoolPersistent(t, regPath)`, read
   the on-disk file bytes via `os.ReadFile(regPath)` BEFORE the call, then
   call `pool.Rename(SessionID("00000000-0000-0000-0000-000000000000"), "x")`.
   Assert `errors.Is(err, sessions.ErrSessionNotFound)`. Re-read on-disk
   bytes and assert `bytes.Equal(before, after)` — byte-identical, the AC's
   exact word. Cover the in-memory side too: snapshot `pool.List()` before
   and after, assert deep-equal. Covers AC #3.

4. **`TestPool_Rename_RaceWithList`** — `helperPool(t, false)` (in-memory or
   tmpdir, either works). Spawn N (say, 16) goroutines: half call
   `pool.Rename(id, fmt.Sprintf("v%d", i))` in a loop; half call
   `pool.List()` and walk the result. Run for ~100 iterations each. Asserts
   nothing; the value is in `go test -race ./...` not firing. Covers AC #4.
   The harness is just `sync.WaitGroup` + `for` loops; no special structure.

5. **`TestPool_Rename_BootstrapPersistsAndShows`** — explicitly the bootstrap
   case from AC #2: `pool.Rename(bootstrapID, "primary")`, assert on-disk
   `Label == "primary"` AND `Bootstrap == true` (the disk record is still
   bootstrap-flagged), AND `pool.List()[0].Label == "primary"` (no
   synthetic substitution). Some overlap with #1; this one is named for the
   AC clause it covers and asserts the `Bootstrap` flag passthrough on disk
   that #1 doesn't bother with.

All five use existing helpers (`helperPool`, `helperPoolPersistent`). No new
test infrastructure required. `go test -race ./...` and `go vet ./...` pass
(AC #5).

## Files touched

- `internal/sessions/pool.go` — add `Rename` method. ~30 production lines
  including the doc comment.
- `internal/sessions/session.go` — update the `label/createdAt/bootstrap`
  doc-comment block to reflect that `label` is mutable via `Pool.Rename`.
  ~3 lines diff.
- `internal/sessions/pool_rename_test.go` — new file, ~150 test lines.

Total: 1 production file modified, 1 production file doc-touched, 1 test
file added, 0 files deleted, 0 consumer call sites to update (the wire/CLI
surface is 47-B's job).

Comfortably within XS bounds: ~30 production lines, 3 files, 0 new exported
types, 1 new exported method.

## Open questions

None worth deferring. Two judgment calls the developer may make freely:

- **Same-value short-circuit in `Rename`?** The sketch above checks
  `sess.label == newLabel` and returns early without writing. Pro: avoids a
  redundant disk write and keeps the registry mtime stable for an idempotent
  rename, which makes test assertions on "byte-identical" simpler. Con: one
  extra branch and a (tiny) divergence from `RotateID`'s "if oldID == newID
  return nil" pattern (which IS in `RotateID`). Recommend keep — it matches
  `RotateID(x, x)` precedent.
- **Validate `newLabel` (length cap, character class)?** No. The AC permits
  empty strings; nothing else is mentioned. Validation belongs at the CLI
  layer (47-B) where operator-input policy lives. The pool primitive should
  accept whatever string the caller hands it — same posture as `Create`'s
  label parameter (`Pool.Create(ctx, label string)` is similarly unvalidated).

## Out of scope (per ticket body)

- Control verb (47-B).
- CLI handler, argument parsing, UUID-prefix resolution (47-B).
- Any read-side surface beyond what's needed to assert AC (existing
  `Pool.List` from #60 covers reads).
- Renaming by prefix or partial id — strict full-`SessionID` only.
- Validation of `newLabel` (length, character class, uniqueness).
- A `Pool.Remove` write primitive — separate ticket if/when needed.
