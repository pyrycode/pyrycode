# `devices.json` Registry

On-disk persistence for `internal/devices.Registry`. Stores the binary's paired-mobile-device list — token hash, operator-typed name, paired/last-seen timestamps — at `~/.pyry/<name>/devices.json`. Phase 3 storage primitive consumed by future `pyry pair` (mint), `pyry pair revoke <name>`, and the WS-handshake auth path.

Lives in the same `internal/devices` package as `Device`, `HashToken`, `VerifyToken` (#208) — no subpackage. Stdlib only (`encoding/json`, `os`, `path/filepath`, `sort`, `sync`).

## Status

- **Phase 3 foundation (#209):** mutex-guarded `Registry` + atomic save + load. Six exports: `Load`, `(*Registry).Save / Add / Remove / List / FindByTokenHash`.
- **Phase 3 foundation (#210):** `(*Registry).Validate` — the WS-perimeter auth predicate. Composes `HashToken` + `FindByTokenHash`-shaped scan + in-memory `LastSeenAt` advance. Seventh export. No consumers wired in this slice (the WS auth handler is a follow-up Phase-3 ticket).
- **Phase 3 foundation (#250):** `(*Registry).UpdatePushRegistration(tokenHash, platform, pushToken, name) bool` — the in-memory mutator for `Device.Platform` / `Device.PushToken` / `Device.Name` keyed by `TokenHash`. Returns `true` iff a matching row was found and mutated; caller chains `Save` for durability. Mutates the three fields under `r.mu`. Eighth export, consumed by `internal/relay/handlers.Handle` for the phone's `register_push_token` frame. `Name` is part of the mutation because the protocol's `device_name` makes the phone the source of truth for self-reported name (iOS Settings rename propagates).

## Surface

```go
type Registry struct { /* unexported */ }

func Load(path string) (*Registry, error)
func (r *Registry) Save(path string) error
func (r *Registry) Add(d Device)
func (r *Registry) Remove(name string) bool
func (r *Registry) List() []Device
func (r *Registry) FindByTokenHash(hash string) (Device, bool)
func (r *Registry) Validate(plain string) (Device, bool)
func (r *Registry) UpdatePushRegistration(tokenHash, platform, pushToken, name string) bool
```

`Registry` holds the in-memory device slice plus a guarding mutex. Construct via `Load` (cold-start mints empty; warm-start reads from disk); persist via `Save`. Methods are safe for concurrent use.

## Path

```
~/.pyry/<sanitized-name>/devices.json
```

The registry API is path-agnostic — `Load(path)` and `Save(path)` take any absolute path. Resolving `~/.pyry/<name>/devices.json` is the consumer's job (mirrors `internal/sessions`'s `loadRegistry(path)` / `saveRegistryLocked(path, reg)` discipline; the daemon-startup wiring ticket adds `resolveDevicesPath` next to `resolveRegistryPath`). Permissions: directory `0o700`, file `0o600`.

## Schema

```json
{
  "devices": [
    {
      "token_hash": "ba7816bf...",
      "name": "Juhana's Pixel 8",
      "paired_at": "2026-05-09T12:34:56.789Z",
      "last_seen_at": "2026-05-09T12:35:01.012Z"
    }
  ]
}
```

Envelope shape (`{"devices": [...]}`), not a bare top-level array. Reserves room for future top-level fields (schema version, push-token registration metadata per `protocol-mobile.md:495`) without breaking jq pipelines or stdlib decoder discipline. Same future-proofing rationale as the sessions registry's `{"sessions": [...]}` envelope.

No `version` field today (out of scope per AC; defer until first migration). `Device` JSON tags are pinned by [`features/devices-package.md`](devices-package.md).

## Atomic write

`Save` mirrors `internal/sessions/registry.go:saveRegistryLocked`:

```
os.MkdirAll(dir, 0o700)
os.CreateTemp(dir, ".devices-*.json.tmp")
defer os.Remove(tmp)
os.Chmod(tmp, 0o600)
json.NewEncoder(f).Encode(...)
f.Sync()
f.Close()
os.Rename(tmp, path)   // commit point
```

`os.Rename` on the same filesystem is atomic on Linux ext4 / macOS APFS. SIGKILL between `CreateTemp` and `Rename` leaves the pre-existing target untouched and an orphan `.devices-*.json.tmp` (cleaned up best-effort by `defer os.Remove(tmp)`). SIGKILL after `Rename` leaves the new file in place. Partial JSON in the target file is unreachable.

The `0o600` chmod is applied unconditionally before the encode even though `os.CreateTemp`'s default already creates with mode `0o600` — same belt-and-suspenders pattern as `saveRegistryLocked`, defends against a future umask-permissive env or stdlib behaviour change.

No parent-directory fsync (per `lessons.md` § "Atomic on-disk writes" — operator-recoverable JSON, ext4/APFS rename-entry update is durable enough; revisit if real-world corruption surfaces).

## Save concurrency: lock, snapshot, release, write

`Save` differs from `internal/sessions`'s hold-across-I/O in one load-bearing way:

```go
r.mu.Lock()
snapshot := append([]Device(nil), r.devices...)
r.mu.Unlock()
// sort + atomic write happen WITHOUT the lock held
```

The slice is shallow-copied under the lock; the file write happens after release. `List` and `FindByTokenHash` (the auth-path readers) are never blocked behind a Save's I/O syscalls. See [ADR 020](../decisions/020-devices-registry-snapshot-then-write.md) for the full rationale (auth path is the high-frequency reader; pairing is the rare writer).

Two concurrent `Save` calls on the same `*Registry` produce two complete temp files and two renames — `os.Rename` is atomic per call, the later rename wins. Each temp file is itself a complete encode; no torn write, no lost in-memory state at the snapshot boundary. Callers that need "Save once, then everyone observes the new state" call `Save` from a single goroutine (the pair command's goroutine; the auth path is read-only).

## Sort discipline

Snapshot is sorted by `PairedAt` ascending, tiebroken by `Name` byte-exact, before encode:

```go
sort.SliceStable(snapshot, func(i, j int) bool {
    if !snapshot[i].PairedAt.Equal(snapshot[j].PairedAt) {
        return snapshot[i].PairedAt.Before(snapshot[j].PairedAt)
    }
    return snapshot[i].Name < snapshot[j].Name
})
```

Two registries with the same logical content but different `Add` order produce byte-identical files (`TestRegistry_SaveStableOrdering` pins this). `time.Time.Equal` (not `==`) for the primary comparator — JSON roundtrip strips monotonic-clock state and `==` would treat otherwise-equal timestamps as unequal (see `lessons.md` § "JSON roundtrip strips monotonic-clock state").

Sort runs on the Save-side snapshot, not on the live in-memory slice — `Add` insertion order in memory is preserved (a future "most recently added first" UI is unaffected) while disk output stays deterministic.

## Load semantics

| Disk state | `Load` returns |
|---|---|
| File missing (`fs.ErrNotExist`) | `(empty *Registry, nil)` — cold start. |
| File present, zero bytes | `(empty *Registry, nil)` — same as missing. |
| File present, valid JSON | `(*Registry{devices: rf.Devices}, nil)`. |
| File present, malformed JSON | `(nil, fmt.Errorf("registry: parse %s: %w", path, err))`. |

Empty-file → empty-registry asymmetry vs. `internal/config.Load` (which surfaces empty as a parse error) is deliberate: `devices.json` is pyry-owned and zero bytes is a benign cold-start state; `config.json` is operator-owned and zero bytes is operator error.

The returned `*Registry` is independent of the on-disk file — subsequent `Save` calls re-encode from the in-memory slice; the file may be moved or deleted between `Load` and `Save` without affecting in-memory state.

## Lookup: linear scan with `==`

`FindByTokenHash` is byte-exact `==` over a linear scan, not `subtle.ConstantTimeCompare`. The constant-time concern is the plain↔hash boundary, owned by `devices.VerifyToken` (#208). Once the wire-presented plain has been hashed (deterministic SHA-256), comparing two 64-char hex strings is byte-exact — and any timing leak from `==` early-exit on a prefix mismatch reveals a public derivative (a hash the attacker could compute themselves), not a secret. See [`features/devices-package.md`](devices-package.md) and #208's security review for the full reasoning.

`Add` does not validate uniqueness. The pair-mint consumer (#TBD) is the single producer that reaches `Add` and validates against `List()` first if needed. `Remove` returns `true` iff a device with matching `Name` was found and removed — consumers can assert "the device I just revoked actually existed" before logging.

## `Validate` — the WS-perimeter auth predicate (#210)

`Validate(plain string) (Device, bool)` is the single auth-check entry point on the phone-WS path. The handler calls it once per inbound connection: `d, ok := reg.Validate(plain)`. Returns the matched `Device` and `true` on a hit, the zero `Device` and `false` on any miss (no device matches; `plain` is the empty string).

Body shape, in `internal/devices/auth.go`:

```go
func (r *Registry) Validate(plain string) (Device, bool) {
    if plain == "" {
        return Device{}, false
    }
    hash := HashToken(plain)
    r.mu.Lock()
    defer r.mu.Unlock()
    for i := range r.devices {
        if r.devices[i].TokenHash == hash {
            r.devices[i].LastSeenAt = time.Now()
            return r.devices[i], true
        }
    }
    return Device{}, false
}
```

Five points of structural discipline:

1. **Empty-plain early-out is first** — before `HashToken`, before the lock. The AC requires "no registry lookup on empty input"; this is the structural enforcement. Also defends (cheaply) the unreachable case of a `Device` persisted with `TokenHash == HashToken("")`.
2. **`HashToken` runs outside the lock.** SHA-256 over a short string is microseconds, but moving it outside the critical section keeps the lock held only for the scan-and-mutate window — important because the auth path is the high-frequency reader.
3. **Indexed loop (`for i := range r.devices`)** so the `LastSeenAt = time.Now()` assignment mutates the slice element in place. A value-loop (`for _, d := range r.devices`) would assign to a copy and the mutation would silently no-op.
4. **Mutation and snapshot both inside the lock.** The returned `Device` is a value-type copy taken before the deferred unlock fires, so callers see the just-written timestamp.
5. **Byte-exact `==` on `TokenHash`, not `subtle.ConstantTimeCompare`.** Constant-time at the plain↔hash boundary is owned by `HashToken`; once the wire plain has been hashed, comparing two 64-char hex strings is byte-exact (any timing leak reveals only a public derivative). Inherits #208 / #209 reasoning verbatim.

### What `Validate` does NOT do

- **No `Save`.** Disk persistence is the caller's concern. Validate runs on the WS hot path; an fsync per auth is a perf footgun. Future consumer schedules `Save` (periodic ticker / graceful-shutdown hook); the in-memory `LastSeenAt` is the source of truth for runtime decisions.
- **No `context.Context`, no `*slog.Logger`, no error path.** Body is hash + lock + scan + mutate + snapshot — microseconds at p99, never blocks. Auth-event logging (with `conn-id`, `remote-host`, attempt counter) is the WS handler's concern; the predicate is logger-free. AC pins the signature as `(Device, bool)`.
- **No rate limiting / lockout / observability.** Per-token attempt counters, IP-level lockout, structured auth metrics — all WS-handler concerns. The predicate is a leaf primitive.

### Concurrency

`Validate` takes `Registry.mu` exactly once across the scan + mutation + snapshot, releases on return. No new lock, no ordering, no callbacks, no re-entrance — the single-mutex contract from #209 is preserved.

Two concurrent `Validate` calls of the same token serialize on `mu`. The first writes `T1`; the second observes `T2 ≥ T1` (Go's `time.Now()` is monotonic per process) and writes `T2`. Final stored `LastSeenAt` is `T2` — the "monotonically-non-decreasing" invariant the AC names. A concurrent `Save` snapshots whatever value sits in memory at its lock-acquisition; a concurrent `Remove` between two `Validate` calls makes the second return `(Device{}, false)` cleanly (scan runs after the splice committed; no torn read).

### Why a method on `*Registry`

The mutation (`r.devices[i].LastSeenAt = ...`) is registry-side. A free function `Validate(r *Registry, plain string)` would either re-export `mu` / `devices` (encapsulation leak) or call an unexported helper for no abstraction win. Methods on the type that owns the state — same shape as `Add` / `Remove` / `List` / `FindByTokenHash`. Call-site reads as "ask the registry to validate."

### Tests

`internal/devices/auth_test.go`, same-package, table-driven, `t.Parallel()`, stdlib only.

- `TestRegistry_Validate_Hit` — valid token returns matching device; `LastSeenAt` advanced (asserted both on returned snapshot and via `List()` to pin the in-memory mutation); `PairedAt` unchanged.
- `TestRegistry_Validate_MissUnknown` — unknown token returns `(Device{}, false)`; `List()` shows no mutation.
- `TestRegistry_Validate_MissEmpty` — empty plain returns `(Device{}, false)`; no mutation. The "no registry lookup" half is enforced structurally by the early-out; the test asserts the observable consequence (no mutation), which is what consumers care about.
- `TestRegistry_Validate_EmptyRegistry` — defends against panic on a zero-init `*Registry`.
- `TestRegistry_Validate_ConcurrentSameToken` — race-detector probe (16 goroutines) plus monotonic-non-decreasing assertion: sort the per-goroutine observed `LastSeenAt` values and check each `>=` the previous; `final.After(when)` proves the structurally-correct lock didn't accidentally skip the mutation (e.g. value-receiver bug). Race detector catches a missing lock on the slice-element write.

## Tests

`internal/devices/registry_test.go`, same-package, table-driven, `t.Parallel()` everywhere, stdlib only.

- `TestRegistry_LoadMissingFile` — AC: missing → empty + nil error.
- `TestRegistry_LoadEmptyFile` — AC: zero bytes → empty + nil error.
- `TestRegistry_LoadMalformedJSON` — AC: malformed → wrapped `registry: parse` error, nil registry.
- `TestRegistry_AddSaveLoadRoundTrip` — AC: all four `Device` fields preserved across save/load (compares times with `time.Time.Equal`, never `==`).
- `TestRegistry_RemovePresent` / `TestRegistry_RemoveAbsent` — AC: returns true iff a device was removed.
- `TestRegistry_FindByTokenHash` — AC: hit + miss table (empty hash, non-matching, empty registry).
- `TestRegistry_SaveFilePermissions` — AC: parent dir mode `0o700`, file mode `0o600`. Skipped on Windows (POSIX semantics).
- `TestRegistry_SaveStableOrdering` — sort-before-encode produces byte-identical output across `Add` permutations.
- `TestRegistry_SaveAtomicRenamePreservesOldFile` — chmod-the-dir-readonly trick proves the pre-existing file survives a failed save unchanged. Skipped on Windows.
- `TestRegistry_ConcurrentReadWrite` — race-detector probe (8 goroutines, mixed `Add` / `List` / `FindByTokenHash`). Confirms the mutex is actually held — if any method drops the lock, `go test -race` flags the slice header / element accesses.

## Out of scope (deferred)

- **Schema versioning.** Per AC: defer until first migration. The envelope shape reserves the field; add it then, not now.
- **`pyry pair` (mint).** Sibling ticket — builds a `Device`, calls `Add` then `Save`.
- **`pyry pair revoke <name>`.** Sibling ticket — calls `Remove(name)` then `Save`.
- **WS handshake auth.** Phase 3 — calls `Load` once at daemon startup, then `Validate(presented)` per phone connect (#210 delivered the predicate; the handler that calls it is a sibling Phase-3 ticket).
- ~~**Per-device `last_seen_at` updates.**~~ Delivered by #210 (`Validate` advances `LastSeenAt` in memory on every hit). Disk persistence of the advanced value remains the auth handler's concern (periodic `Save` / graceful-shutdown hook); the predicate intentionally does not call `Save`.
- **Push-token registration metadata.** Future top-level field (per `protocol-mobile.md:495`); the envelope shape supports additive growth.
- **Encrypting `devices.json` at rest.** Defer — current threat model doesn't justify the operator UX cost.
- **Schema migration to a database.** Defer.

## Related

- [`features/devices-package.md`](devices-package.md) — `Device` struct + `HashToken` / `VerifyToken` (#208).
- [`features/sessions-registry.md`](sessions-registry.md) — the structural reference implementation (atomic write, envelope shape, forward compat).
- [`features/identity-package.md`](identity-package.md) — sibling Phase 3 foundation that owns `~/.pyry/server-id`.
- [`features/config-package.md`](config-package.md) — sibling Phase 3 foundation that owns `~/.pyry/config.json`.
- [ADR 020](../decisions/020-devices-registry-snapshot-then-write.md) — Save snapshots under lock, performs I/O outside.
- `internal/sessions/registry.go:saveRegistryLocked` — canonical atomic-rename recipe.
- `docs/protocol-mobile.md:62` — wire contract: binary stores `sha256(token)` in `devices.json`, never plaintext.
- `docs/protocol-mobile.md:618` — TOCTOU concern this registry's atomic-rename pattern structurally defends.
