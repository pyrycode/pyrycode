# Phase 1.1b-A — `Pool.List` read primitive

**Ticket:** [#60](https://github.com/pyrycode/pyrycode/issues/60)
**Size:** XS (one method, one value type, one test file).
**Scope:** `internal/sessions` only. No control-plane, no CLI, no wire protocol.

## Context

Phase 1.1b's `pyry sessions list` CLI verb (ticket 46-B) needs a typed Go API
to enumerate every session pyry knows about — bootstrap and minted alike —
without reaching directly into `sessions.json`. Today's `Pool` only exposes
`Lookup`, `Default`, `Snapshot` (id+pid only, for the rotation watcher), and
the spawn/mutation primitives. There is no read-only accessor that returns the
operator-visible metadata: label, lifecycle state, last-active timestamp.

Adding that accessor is this ticket. The matching wire/CLI surface in 46-B
consumes it; nothing in this ticket touches `internal/control` or `cmd/pyry`.

## Design

### One new exported method, one new exported type

Add to `internal/sessions/pool.go`:

```go
// SessionInfo is one session's operator-visible metadata, returned by
// Pool.List. Field types are deep-copy-safe: SessionID and string are values,
// time.Time is a value, and lifecycleState is a uint8 enum. Mutating a
// SessionInfo does not affect Pool state or the on-disk registry.
type SessionInfo struct {
    ID           SessionID
    Label        string         // synthetic "bootstrap" substituted for the
                                // bootstrap entry when its on-disk label is
                                // empty; on-disk value is unchanged.
    LifecycleState lifecycleState
    LastActiveAt time.Time
    Bootstrap    bool           // true for the bootstrap entry; helps 46-B
                                // render a marker without re-checking IDs.
}

// List returns a snapshot of every session in the pool — bootstrap and minted
// alike — sorted by LastActiveAt descending (most recent first). Ties break
// on SessionID ascending for deterministic ordering. The returned slice and
// its elements are deep-copied: callers may mutate freely without affecting
// pool or registry state.
//
// The bootstrap entry's Label field is the synthetic string "bootstrap" when
// the on-disk label is empty; the on-disk registry entry is NOT mutated by
// this substitution. Non-empty bootstrap labels (operator-set) pass through
// verbatim.
//
// Read-only: this method does not bump LastActiveAt, transition lifecycle
// state, or persist anything. Safe for concurrent use; takes Pool.mu (read)
// and each Session.lcMu (write — the lock is sync.Mutex, not RWMutex)
// briefly.
//
// Lock order: Pool.mu (RLock) → Session.lcMu (Lock). Same order as
// Pool.saveLocked and Pool.pickLRUVictim — no new lock-order edges.
func (p *Pool) List() []SessionInfo
```

The bootstrap field is included in `SessionInfo` even though 46-B's first
renderer might not surface it: it costs one boolean per entry and removes the
"how do I know which one is the bootstrap" question for any consumer. (The
synthetic label `"bootstrap"` is a UX nicety, not a discriminator: an operator
can set `Label="bootstrap"` on a non-bootstrap entry, and the `Bootstrap` bool
disambiguates.)

### Why a new type and not reuse `SnapshotEntry`

`SnapshotEntry{ID, PID}` exists for the rotation watcher and carries only
primitives so the rotation package can consume snapshots without importing
`internal/sessions`. Adding label/lifecycle/last-active fields to it would
either bloat every rotation snapshot or push a `lifecycleState` enum into
`internal/sessions/rotation`'s import set. Two distinct types, one per
consumer, is cleaner than a shared shape.

### Implementation sketch

```go
func (p *Pool) List() []SessionInfo {
    p.mu.RLock()
    defer p.mu.RUnlock()

    out := make([]SessionInfo, 0, len(p.sessions))
    for _, s := range p.sessions {
        s.lcMu.Lock()
        state := s.lcState
        lastActive := s.lastActiveAt
        s.lcMu.Unlock()

        label := s.label
        if s.bootstrap && label == "" {
            label = "bootstrap"
        }

        out = append(out, SessionInfo{
            ID:             s.id,
            Label:          label,
            LifecycleState: state,
            LastActiveAt:   lastActive,
            Bootstrap:      s.bootstrap,
        })
    }
    sort.Slice(out, func(i, j int) bool {
        if !out[i].LastActiveAt.Equal(out[j].LastActiveAt) {
            return out[i].LastActiveAt.After(out[j].LastActiveAt) // desc
        }
        return out[i].ID < out[j].ID // asc tiebreak for determinism
    })
    return out
}
```

Note: `s.label`, `s.id`, and `s.bootstrap` are immutable post-`New` /
post-`RotateID` from the perspective of any reader holding `Pool.mu` — the
existing `RotateID` invariant ("today's only callers run before any lifecycle
goroutine begins observing the id") plus `Pool.mu`-held semantics make
reading them off-`lcMu` safe here.

`s.lcState` and `s.lastActiveAt` MUST be read under `lcMu` — the lifecycle
goroutine writes them under `lcMu` in `transitionTo` and `touchLastActive`.

### Sort order

Last-active descending matches the AC ("most recent first") and also matches
how an operator typically reads such a list: the session you just used is at
the top. Tiebreak on `SessionID` ascending makes the order deterministic
across calls — important for the test that asserts ordering on equal
timestamps (degenerate at runtime, normal in unit tests where time freezes).

`stdlib`-only: `sort.Slice` is fine. `slices.SortFunc` would also work and
is mildly preferable on Go 1.21+; either is acceptable. Pick `sort.Slice` to
match the existing `sortEntriesByCreatedAt` style in `registry.go`.

## Concurrency model

- **Lock order:** `Pool.mu` (read) → `Session.lcMu` (write — it's a plain
  `sync.Mutex`). Identical to `Pool.saveLocked` and `Pool.pickLRUVictim`.
  No new lock-order edges introduced.
- **Read-only:** no `lastActiveAt` bump, no state transition, no `persist()`.
  The AC's "registry fields unchanged on disk after the call" is a direct
  consequence of not calling `saveLocked`.
- **Concurrent List + transition:** a `List` call concurrent with a
  `transitionTo` either observes the pre-transition `lcState`/`lastActiveAt`
  or the post-transition values, never a torn read (each session's pair is
  read under one `lcMu` acquire). That is the existing `Pool.saveLocked`
  contract; nothing new.
- **Concurrent List + Create:** `Create` takes `Pool.mu` (write) for the
  registration; `List` takes `Pool.mu` (read). Standard RWMutex semantics —
  one of the two orderings is observed, neither corrupts the result.
- **Race-cleanliness:** trivially satisfied. The test below runs N goroutines
  doing `List` concurrently and asserts under `-race` that nothing fires.

## Error handling

`List` does not return an error. There is no I/O, no allocation that can
meaningfully fail, no context. An empty pool (impossible in practice — there
is always a bootstrap entry — but worth covering) yields a zero-length slice;
the AC's "empty pool yields a slice of length 1" reflects that bootstrap
always exists. `nil` is not returned in any path.

## Testing strategy

New file `internal/sessions/pool_list_test.go` (kept separate from
`pool_test.go` so the file scope is obvious; same package, no test seam
needed).

Four tests, table-light:

1. **`TestPool_List_BootstrapOnly`** — constructs a pool with no extra
   sessions, asserts `len(list) == 1`, asserts `list[0].Bootstrap == true`,
   asserts `list[0].Label == "bootstrap"` (since `helperPool` sets no
   bootstrap label). Reads the registry file back from disk and asserts the
   on-disk `label` is still empty (the AC's "on-disk label unchanged"
   guarantee). Uses `Config.RegistryPath = filepath.Join(t.TempDir(), "sessions.json")`
   so the load-back is real.

2. **`TestPool_List_OrderingByLastActive`** — constructs a pool, then
   directly mutates three sessions' `lastActiveAt` under `lcMu` to
   `t0`, `t0+1m`, `t0+2m` (no need to drive Activate/Evict — that's
   testing the lifecycle, not List). Calls `List`, asserts the slice is
   sorted by `LastActiveAt` descending. Adds a fourth session with
   `lastActiveAt == t0+1m` and asserts the tiebreak on `SessionID` is
   stable across two calls.

3. **`TestPool_List_BootstrapLabelPassthrough`** — constructs a pool whose
   bootstrap registry entry on disk has a non-empty label (write a
   `sessions.json` with `label: "main"` before `New`, warm-start). Asserts
   `list[0].Label == "main"` — the synthetic substitution does NOT clobber
   an explicit operator-set label.

4. **`TestPool_List_RaceClean`** — spawns N (say, 16) goroutines, each
   calling `List` 100 times in a loop, plus one goroutine calling
   `Pool.Create` to introduce mutation. Asserts under `go test -race ./...`
   that nothing fires. (This test only needs to *run* under `-race`; the
   harness for it is just `sync.WaitGroup` + a `for i := 0; i < N; i++` —
   no special structure.)

   Note: `Pool.Create` requires `Pool.Run` to be active (it uses the
   supervise seam). Tests that need to invoke Create must call `Pool.Run`
   in a goroutine first; the existing `pool_create_test.go` shows the
   pattern. If wiring Create into the race test gets fiddly, an acceptable
   simplification is to drop the Create goroutine and just race List
   against itself plus a goroutine calling `RotateID` (which only takes
   `Pool.mu` write — same lock ordering pressure, simpler harness).

All four tests use `helperPool` (already in `pool_test.go`). No new test
helpers required.

## Files touched

- `internal/sessions/pool.go` — add `SessionInfo` type, add `List()` method,
  add `sort` import if not already present (it's not — registry.go has it,
  pool.go does not). ~40 production lines.
- `internal/sessions/pool_list_test.go` — new file, ~150 test lines.

Total: 1 production file modified, 1 test file added, 0 files deleted, 0
consumer call sites to update (the wire/CLI surface is 46-B's job).

Well within XS bounds (≤100 lines production, ≤3 files, ≤5 new exported
types — this adds 1 type + 1 method).

## Open questions

None worth deferring. Two judgment calls the developer can make freely:

- **Sort: `sort.Slice` vs `slices.SortFunc`?** Either is fine. Match the
  surrounding style — `registry.go` uses `sort.SliceStable`, so `sort.Slice`
  fits. Stability is irrelevant here because the tiebreak on `SessionID`
  already makes the order total.
- **Race test mutator: `Create` vs `RotateID`?** Either exercises the
  lock-ordering surface. Pick whichever lands in fewer lines.

## Out of scope (per ticket body)

- Control verb (46-B).
- CLI handler, table renderer, `--json` flag (46-B).
- Any write/mutation of registry state.
- Streaming or push-based updates.
- Filtering or pagination — the pool is bounded at hundreds of sessions; a
  full snapshot is cheap.
