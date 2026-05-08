# `devices.json` Registry

On-disk persistence for `internal/devices.Registry`. Stores the binary's paired-mobile-device list — token hash, operator-typed name, paired/last-seen timestamps — at `~/.pyry/<name>/devices.json`. Phase 3 storage primitive consumed by future `pyry pair` (mint), `pyry pair revoke <name>`, and the WS-handshake auth path.

Lives in the same `internal/devices` package as `Device`, `HashToken`, `VerifyToken` (#208) — no subpackage. Stdlib only (`encoding/json`, `os`, `path/filepath`, `sort`, `sync`).

## Status

- **Phase 3 foundation (#209):** mutex-guarded `Registry` + atomic save + load. Six exports: `Load`, `(*Registry).Save / Add / Remove / List / FindByTokenHash`. No consumers wired in this slice.

## Surface

```go
type Registry struct { /* unexported */ }

func Load(path string) (*Registry, error)
func (r *Registry) Save(path string) error
func (r *Registry) Add(d Device)
func (r *Registry) Remove(name string) bool
func (r *Registry) List() []Device
func (r *Registry) FindByTokenHash(hash string) (Device, bool)
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
- **WS handshake auth.** Phase 3 — calls `Load` once at daemon startup, then `FindByTokenHash(HashToken(presented))` per phone connect.
- **Per-device `last_seen_at` updates.** The field is persisted; updating it on each WS connect is the auth handler's concern, not this primitive's.
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
