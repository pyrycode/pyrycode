# `sessions.json` Registry

On-disk persistence for `internal/sessions.Pool`. The registry stores per-pyry-name session identity (UUID) and metadata (label, created/last-active timestamps, bootstrap marker), so sessions outlive `pyry stop` + restart.

## Status

- **Phase 1.2a (#34):** registry introduced for the bootstrap entry. Cold start mints a UUID and writes the file; warm start reuses the persisted UUID without rewriting the file.
- **Phase 1.2b-A (#38):** `Pool.RotateID` now mutates the bootstrap entry's UUID when startup reconciliation detects a `/clear` rotation; persists via `saveLocked`. See [jsonl-reconciliation.md](jsonl-reconciliation.md).
- **Phase 1.2b-B (#39):** live-detection rotation watcher drives the same `RotateID` → `saveLocked` seam.
- **Phase 1.2c-A (#40):** `lifecycle_state` field added (`"active"` / `"evicted"`, `omitempty`). `last_active_at` now bumped on every state transition — the LRU follow-up consumes it for victim selection. See [idle-eviction.md](idle-eviction.md).
- **Phase 1.1a-A2 (#73):** `Pool.Create` writes new non-bootstrap entries via `saveLocked` with `bootstrap=false`. See [sessions-package.md § Pool.Create](sessions-package.md).
- **Phase 1.1c-A (#62):** `Pool.Rename(id, newLabel)` mutates the `label` field via `saveLocked`. Empty `newLabel` clears the on-disk value to `""`; non-empty values persist verbatim. See [sessions-package.md § Pool.Rename](sessions-package.md).
- **Phase 1.1+:** `Pool.Remove` plugs into the same `saveLocked` seam.

## Path

```
~/.pyry/<sanitized-name>/sessions.json
```

Lives as a sibling to the per-name socket `~/.pyry/<name>.sock`. Resolution is in `cmd/pyry/main.go::resolveRegistryPath` and reuses `sanitizeName` from socket-path resolution. Permissions: directory `0700`, file `0600`.

## Schema

```json
{
  "version": 1,
  "sessions": [
    {
      "id": "8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91",
      "label": "",
      "created_at": "2026-05-01T12:34:56.789123456Z",
      "last_active_at": "2026-05-01T12:34:56.789123456Z",
      "bootstrap": true,
      "lifecycle_state": "evicted"
    }
  ]
}
```

| Field | Type | 1.2a semantics |
|---|---|---|
| `version` | int | Forward-marker. 1.2a writes `1` and accepts any value on read. |
| `id` | string (UUIDv4) | The `SessionID`. |
| `label` | string | Operator-set label. `""` for the bootstrap entry until set; `Pool.Create` accepts an initial value, `Pool.Rename` (1.1c-A) mutates it. `Pool.List` substitutes the synthetic `"bootstrap"` for the bootstrap entry when the on-disk value is empty (read-side only — disk record is unchanged). |
| `created_at` | RFC3339Nano | Set once at session creation. |
| `last_active_at` | RFC3339Nano | Equal to `created_at` in 1.2a. Bumped on `RotateID` (1.2b-A) and on every lifecycle state transition (1.2c-A). |
| `bootstrap` | bool | Marks the entry resolved by `Pool.Lookup("")`. Omitted on disk when false (`omitempty`). |
| `lifecycle_state` | string | `"active"` or `"evicted"` (1.2c-A). Omitted on disk when `"active"` (`omitempty`) — preserves the dominant-case byte-stability. Missing field on read defaults to `"active"`. |

**Forward compatibility:** unknown top-level and per-session fields are tolerated on read (default `encoding/json` decoder; `DisallowUnknownFields` is *not* set). New fields land additively in later phases without breaking old pyry binaries.

## Atomic write

`saveRegistryLocked` writes to a temp file in the same directory, fsyncs, then renames into place:

```
os.CreateTemp(dir, ".sessions-*.json.tmp")
os.Chmod(tmp, 0o600)
json.NewEncoder(tmp).Encode(reg)
tmp.Sync()
tmp.Close()
os.Rename(tmp, path)   // commit point
```

`os.Rename` on the same filesystem is atomic on Linux ext4 and macOS APFS. SIGKILL between `CreateTemp` and `Rename` leaves the pre-existing target untouched and an orphan `.sessions-*.json.tmp` (cleaned up best-effort by `defer os.Remove(tmp)`). SIGKILL after `Rename` leaves the new file in place. **Partial JSON in the target file is unreachable.**

We do not also fsync the directory after rename — pyry's registry is operator-recoverable, not a database. Revisit if real-world corruption is observed.

## Load semantics

| Disk state | `loadRegistry` returns | `Pool.New` behaviour |
|---|---|---|
| File missing | `(nil, nil)` | Cold start: mint a fresh UUID and write the registry. |
| File present, empty | `(nil, nil)` | Cold start (same as missing). |
| File present, valid JSON | `(*registryFile, nil)` | Warm start: reuse the bootstrap entry's UUID/metadata. **No rewrite.** |
| File present, malformed JSON | `(nil, error)` | `Pool.New` returns the error. The error wraps as `pool init: sessions: load registry: registry: parse <path>: <unmarshal err>`, the daemon prints to stderr and exits non-zero, and the corrupt file is **not** rewritten — silent data loss is the worst-possible outcome and explicitly excluded. Operator must fix or remove the file. Pinned at the binary boundary by `TestE2E_Startup_CorruptRegistryFailsClean` (#111, see [e2e-harness.md § Failed-Start Pattern](e2e-harness.md)); the unit-level proof is `internal/sessions/registry_test.go`. |

The "no rewrite on warm start" property is what makes the AC's "writes only on state-changing operations" honest — and what `TestPool_New_WarmStartReusesUUID` asserts.

## Bootstrap marker

`bootstrap: true` is on disk so `Pool.Lookup("")` doesn't depend on file ordering. With the explicit marker, Phase 1.1's `pyry sessions rm <bootstrap-uuid>` has a clean question to answer — refuse, or promote another entry — instead of relying on "first entry in the array" as a load-bearing invariant.

## Concurrency

Single-writer per file. The per-pyry-name namespace already serializes to one pyry process via the socket file at `~/.pyry/<name>.sock`; the registry inherits that exclusion. No `flock`/`fcntl`.

`Pool.mu` (write) is held across `os.Rename` when `saveLocked` is called from a mutating Pool operation. The file is tiny and writes are infrequent (session create/rename/remove — not per-message activity), so disk-I/O-under-lock is acceptable. Phase 1.2c's `last_active_at` updates may need a different cadence; that is 1.2c's design problem.

## Manual smoke

### Restart preserves UUID

```bash
pyry &                                      # start in service mode
cat ~/.pyry/pyry/sessions.json              # capture the bootstrap UUID
pyry stop
pyry &                                      # restart
cat ~/.pyry/pyry/sessions.json              # same UUID expected
```

The automated proxy is `TestPool_New_WarmStartReusesUUID` in `internal/sessions/pool_test.go`.

### SIGKILL mid-write is safe

Phase 1.2a writes the registry exactly once at first start, so there is no recurring write to interrupt with SIGKILL. The rename-atomicity invariant is proved at the unit level by `TestSave_AtomicRenamePreservesOldFile`. The natural live SIGKILL target lands with Phase 1.1's `pyry sessions new` (a recurring write triggered by user action).

## References

- Ticket: [#34](https://github.com/pyrycode/pyrycode/issues/34)
- Locked design: [`docs/multi-session.md`](../../multi-session.md) (Phase 1.2 / "Locked decisions")
- Sibling feature doc: [`sessions-package.md`](sessions-package.md)
