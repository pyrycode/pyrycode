# Pool.List (1.1b-A)

The typed read primitive Phase 1.1b-B's `pyry sessions list` CLI verb (#46-B) calls instead of poking at `sessions.json` directly. One method, one new value type, no error path.

```go
type SessionInfo struct {
    ID             SessionID
    Label          string         // synthetic "bootstrap" substituted for the
                                  // bootstrap entry when its on-disk label is
                                  // empty; on-disk value is unchanged.
    LifecycleState lifecycleState
    LastActiveAt   time.Time
    Bootstrap      bool           // true for the bootstrap entry; lets
                                  // consumers disambiguate without re-checking IDs.
}

func (p *Pool) List() []SessionInfo
```

**Deep-copy by construction.** Every field is a value type (`SessionID`/`string`/`time.Time` are values; `lifecycleState` is a `uint8` enum). Mutating a `SessionInfo` cannot affect pool state or registry contents — no defensive cloning, no documentation contract that callers can violate.

**Sort: `LastActiveAt` desc, `SessionID` asc tiebreak.** Most-recent-first matches operator intuition (the session you just used is at the top). The id tiebreak makes ordering deterministic across calls — important for unit tests where time freezes; degenerate at runtime. Uses `sort.Slice` to match `registry.go`'s style.

**Bootstrap label substitution lives here, not in 46-B's renderer.** The wire payload is self-explanatory (every consumer gets `"bootstrap"` instead of the empty string for the unlabelled bootstrap entry) and the on-disk registry entry is **not** mutated. An operator-set bootstrap label passes through verbatim — `Bootstrap` (the bool) is the discriminator, not the label string.

**Why a new type, not extending `SnapshotEntry`.** `SnapshotEntry{ID, PID}` exists for the rotation watcher's closure-over-primitives boundary (`internal/sessions/rotation` cannot import `internal/sessions`). Adding `lifecycleState` to it would either bloat every rotation snapshot or push the enum into `rotation`'s import set. Two distinct types, one per consumer, is cleaner than a shared shape.

**Lock order — unchanged.** `Pool.mu` (RLock) → `Session.lcMu` (Lock). Identical to `Pool.saveLocked` and `Pool.pickLRUVictim`. The bootstrap flag, label, and id are immutable post-`New` / post-`RotateID` from any reader holding `Pool.mu`, so they're read off-`lcMu`; `lcState` and `lastActiveAt` MUST be read under `lcMu` (the lifecycle goroutine writes them under `lcMu` in `transitionTo` and `touchLastActive`). Each session's pair is read under one `lcMu` acquire — no torn reads.

**Read-only.** No `lastActiveAt` bump, no state transition, no `persist()` call. The AC's "registry fields unchanged on disk after the call" is a direct consequence of not calling `saveLocked`.

**Concurrent List:** standard RWMutex semantics — concurrent `List` callers don't contend with each other; concurrent `List + Create` (or `List + RotateID`) sees one ordering or the other, neither corrupts the result. Race-clean under `-race`.
