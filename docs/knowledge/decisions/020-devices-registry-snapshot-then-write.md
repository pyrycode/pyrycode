# 020 — `devices.Registry.Save` snapshots under lock, performs I/O outside

## Context

`internal/devices.Registry` (#209) persists paired-device entries to
`~/.pyry/<name>/devices.json` using the same atomic-rename recipe as
`internal/sessions/registry.go:saveRegistryLocked` (`MkdirAll(0o700)` →
`CreateTemp` → `Chmod(0o600)` → encode → `Sync` → `Close` → `Rename`).

The structural question for the registry's mutex discipline: when `Save`
writes the file, **does the registry hold its lock across the I/O**, or
**does it snapshot under the lock and release before writing**?

The reference implementation (`internal/sessions`) holds `Pool.mu` (write)
across `os.Rename` — the file is tiny, writes are infrequent, and the call
sites are session-create / rename / remove (operator-driven, not per-message).

`devices.Registry` consumers have a different shape:
- **`Save`** is called rarely — once per pairing (`pyry pair`) or revocation
  (`pyry pair revoke <name>`). Operator-driven.
- **`List` / `FindByTokenHash`** are called frequently — the WS-handshake
  auth path calls `FindByTokenHash(HashToken(presented))` once per phone
  connect.

Holding the lock across the file write would block the auth path's reads
behind a Save's syscall window (`MkdirAll` + `CreateTemp` + `Chmod` + encode
+ `Sync` + `Close` + `Rename`). Auth-path latency under a concurrent pairing
would be visible.

## Decision

`Save` takes the mutex, copies the device slice into a local snapshot,
releases the mutex, then performs sort + atomic-write on the snapshot:

```go
func (r *Registry) Save(path string) error {
    r.mu.Lock()
    snapshot := make([]Device, len(r.devices))
    copy(snapshot, r.devices)
    r.mu.Unlock()

    sort.SliceStable(snapshot, ...)

    // ... MkdirAll → CreateTemp → Chmod → Encode → Sync → Close → Rename ...
}
```

The snapshot is a shallow copy; `Device`'s fields are all value types
(`string`, `time.Time`), so the copy fully decouples the snapshot from any
later mutation under the lock.

## Rationale

1. **Auth-path readers are unblocked during Save.** `List` and
   `FindByTokenHash` re-acquire the mutex while Save's I/O is in flight.
   The auth path sees a consistent point-in-time view (either the
   pre-Save state or — after the snapshot copy returned — a state that
   may already include subsequent mutations); it never blocks.

2. **`Device` is a value type.** Shallow-copying the slice is sufficient.
   No field aliases the registry's storage — no pointers, no shared maps.
   This simplification is what makes the snapshot-then-release pattern
   strictly safe vs. e.g. `internal/sessions`'s `Session` (which holds a
   per-session mutex and a pointer back to the pool).

3. **Concurrent Save calls produce deterministic outcomes.** Two
   simultaneous `Save` calls each take the mutex, snapshot, release, write
   their own temp file, and rename. `os.Rename` is atomic per call; the
   later rename wins. No torn write, no partial JSON in the target. Each
   temp file is itself a complete encode of a consistent snapshot.

## Consequences

- **`Registry.mu` critical sections are short and bounded.** Lock-hold
  time is `O(len(devices))` for the slice copy — never an unbounded
  syscall window. Predictable contention behaviour as the device list
  grows (operator-controlled, expected single digits).

- **A concurrent `Add` between two Saves may land in zero, one, or both
  on disk.** This is the expected concurrent-writer model. Callers that
  need "Save sees all Adds up to T" serialize Save calls in a single
  goroutine. The pair-mint consumer is single-goroutine by construction;
  the auth path is read-only.

- **Snapshot must be deep enough.** Today, shallow is sufficient (all
  `Device` fields are value types). If a future field becomes a
  reference type (slice, map, pointer), the snapshot logic must deep-copy
  that field — otherwise the in-flight encode could see a torn value
  while concurrent `Add`/`Remove` mutates it. A `// SECURITY` comment in
  `Save` should be added at that point; for now, the value-type
  invariant is what makes the pattern correct.

- **Divergence from `internal/sessions` is a documented choice, not an
  oversight.** Future maintainers comparing the two registries will see
  different lock disciplines and might be tempted to "harmonize" them.
  This ADR is the answer: the difference is motivated by the auth-path
  read-frequency asymmetry, not a refactor opportunity.

## Alternatives considered

- **Hold `Registry.mu` across the I/O (mirror `internal/sessions`).**
  Rejected — blocks the auth path's reads behind every Save's syscall
  window. The sessions registry tolerates this because its readers are
  themselves operator-driven (CLI-listing, attach-resolution); the
  devices registry's readers run on every phone WS connect.

- **Separate `mu` (slice) and `saveMu` (file) mutexes.** Rejected —
  adds a second mutex with no extra guarantee. The file-level
  serialization the second mutex would enforce is already provided
  structurally: callers serialize `Save` in a single goroutine
  (the pair command); concurrent Save races at the filesystem layer
  have well-defined "later rename wins" semantics.

- **`sync.RWMutex` with readers taking RLock.** Rejected — `Add` /
  `Remove` (writers) are infrequent enough that the lock-acquire cost
  difference between `Mutex` and `RWMutex` doesn't matter, and
  `RWMutex` adds complexity (`RLock` / `RUnlock` discipline at every
  read site) for negligible gain. A simple `Mutex` with a snapshot is
  the smaller surface.

## Related

- Ticket #209 — devices.json registry CRUD.
- [`features/devices-registry.md`](../features/devices-registry.md) —
  feature documentation.
- `internal/sessions/registry.go:saveRegistryLocked` — the
  hold-across-I/O reference; comparison point.
- `docs/lessons.md` § "Atomic on-disk writes" — the rename-as-commit-point
  invariants this Save inherits.
- `docs/lessons.md` § "JSON roundtrip strips monotonic-clock state from
  `time.Time`" — informs the `time.Time.Equal` comparator in the Save
  sort.
