# `devices.json` Registry

On-disk persistence for `internal/devices.Registry`. Stores the binary's paired-mobile-device list — token hash, operator-typed name, paired/last-seen timestamps — at `~/.pyry/<name>/devices.json`. Phase 3 storage primitive consumed by future `pyry pair` (mint), `pyry pair revoke <name>`, and the WS-handshake auth path.

Lives in the same `internal/devices` package as `Device`, `HashToken`, `VerifyToken` (#208) — no subpackage. Stdlib only (`encoding/json`, `os`, `path/filepath`, `sort`, `sync`).

## Status

- **Phase 3 foundation (#209):** mutex-guarded `Registry` + atomic save + load. Six exports: `Load`, `(*Registry).Save / Add / Remove / List / FindByTokenHash`.
- **Phase 3 foundation (#210):** `(*Registry).Validate` — the WS-perimeter auth predicate. Composes `HashToken` + `FindByTokenHash`-shaped scan + in-memory `LastSeenAt` advance. Seventh export. No consumers wired in this slice (the WS auth handler is a follow-up Phase-3 ticket).
- **Phase 3 foundation (#250):** `(*Registry).UpdatePushRegistration(tokenHash, platform, pushToken, name) bool` — the in-memory mutator for `Device.Platform` / `Device.PushToken` / `Device.Name` keyed by `TokenHash`. Returns `true` iff a matching row was found and mutated; caller chains `Save` for durability. Mutates the three fields under `r.mu`. Eighth export, consumed by `internal/relay/handlers.Handle` for the phone's `register_push_token` frame. `Name` is part of the mutation because the protocol's `device_name` makes the phone the source of truth for self-reported name (iOS Settings rename propagates). **`Device.Name` is a remote-authored string, and its shape check lives one step upstream of this mutator, not inside it (#2219).** `RegisterPushToken` is `UpdatePushRegistration`'s only production caller, so the handler-side gate described in [`relay-package-handlers.md` § Display-safety gate](relay-package-handlers.md#display-safety-gate-on-device_name-and-platform-2219) covers every sink this field reaches (a daemon log, `pyry pair list`, `audit.Entry.DeviceLabel`) without this package needing its own check. `Registry` itself still enforces nothing about `Name`'s shape — a caller other than that handler would still be able to store a control character — and a name written before the gate existed is read back unchecked, which is why `Device.Name` is a remote-authored string with a gated *door*, not a display-safe type invariant.
- **Handshake reload (#782):** `(*Registry).Reload(path)` — reconciles the on-disk device set into the in-memory registry so a device paired via `pyry pair` after daemon startup authenticates on its next handshake without a restart (and a `pyry pair revoke` stops being accepted, for free). Ninth export, called by `internal/relay`'s v2 handshake (before `Validate`) and by the `register_push_token` handler (before `Save`, as a clobber guard). See [`devices-registry-reload.md`](devices-registry-reload.md) and [ADR 029](../decisions/029-devices-registry-reload-at-handshake.md).
- **Redemption clear (#1528):** `(*Registry).ClearRedeemBy(tokenHash string) bool` — zeroes `Device.RedeemBy` on the matching device under `r.mu`, returning true *iff* a row matched **and** its deadline was non-zero. Tenth export. The return value is what decides whether a `Save` is warranted, so idempotency is decided by the registry rather than re-derived at the call site. Consumed by `internal/relay`'s v2 handshake accept tail (`recordRedemption`) — the first `devices.json` writer built on `WithLock` from birth. See [`devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `ClearRedeemBy`.
- **Redemption enforcement (#1529):** `Validate`'s return widened from `(Device, bool)` to `(Device, ValidateResult)` — a third outcome, `ValidateWindowElapsed`, refuses a matched device whose `RedeemBy` is set and already past, distinct from `ValidateUnknownToken` so the v2 handshake can log the two apart while giving both the identical `4401` / `auth.invalid_token` wire shape. See [`devices-registry-validate.md`](devices-registry-validate.md).
- **Client version record (#2577):** `(*Registry).SetClientVersion(tokenHash, version string) bool` — sets `Device.ClientVersion` on the matching device under `r.mu`, returning true *iff* a row matched **and** the value changed. Eleventh export, same changed-only-return shape as `ClearRedeemBy` so the caller's `Save` decision lives in the registry. Does no filtering itself — the caller (`internal/relay`'s v2 handshake) admits through `sessions.AdmitClientVersion` before calling. Consumed by `recordClientVersion`, which runs immediately after `recordRedemption` on the same handshake accept tail. See [`devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `SetClientVersion`.
- **Install binding (#2734):** `Validate(plain string, peerStatic []byte) (Device, ValidateResult)` gains a second parameter and a third outcome, `ValidateKeyMismatch` — a matched device bound to a key other than the one presented. `(*Registry).BindStaticKey(tokenHash string, peerStatic []byte) BindResult` — the atomic bind-if-unbound decision, twelfth export. Both live in `internal/relay`'s handshake accept path, after the token and client-version gates; `recordStaticKey` persists a fresh bind the same best-effort way `recordClientVersion` does. See [`devices-registry-validate.md`](devices-registry-validate.md) and [`devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `BindStaticKey`.

## Surface

```go
type Registry struct { /* unexported */ }

func Load(path string) (*Registry, error)
func (r *Registry) Save(path string) error
func (r *Registry) Add(d Device)
func (r *Registry) Remove(name string) bool
func (r *Registry) List() []Device
func (r *Registry) FindByTokenHash(hash string) (Device, bool)
func (r *Registry) Validate(plain string, peerStatic []byte) (Device, ValidateResult)
func (r *Registry) UpdatePushRegistration(tokenHash, platform, pushToken, name string) bool
func (r *Registry) Reload(path string) error
func (r *Registry) ClearRedeemBy(tokenHash string) bool
func (r *Registry) SetClientVersion(tokenHash, version string) bool
func (r *Registry) BindStaticKey(tokenHash string, peerStatic []byte) BindResult
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
      "last_seen_at": "2026-05-09T12:35:01.012Z",
      "client_version": "pyrycode-mobile/1.4.0",
      "static_key": "a1b2c3..."
    }
  ]
}
```

Envelope shape (`{"devices": [...]}`), not a bare top-level array. Reserves room for future top-level fields (schema version, push-token registration metadata per `protocol-mobile.md:495`) without breaking jq pipelines or stdlib decoder discipline. Same future-proofing rationale as the sessions registry's `{"sessions": [...]}` envelope.

`client_version` (#2577) is `omitempty` — a record with no version, including every record predating the field, keeps the key off disk and decodes to `""`. It holds the raw `client_version` string the device's most recent accepted hello reported, admitted through `sessions.AdmitClientVersion` (the same rule `admitClient` applies to the retained session-prompt copy); a value the rule refuses is stored as `""`, not the rejected text. See [`devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `SetClientVersion` for the write path.

`static_key` (#2734) is `omitzero` — a record not yet bound, including every record predating the field, keeps the key off disk. It is the lowercase hex of the Noise_IK device-static public key the first accepted connection presented, written once by `BindStaticKey` and never rewritten except by `pyry pair revoke` deleting the whole row. See [`devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `BindStaticKey`.

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

The disk-read branch is shared with `Reload` (#782) via an unexported `readDevicesFile(path) ([]Device, error)` helper: `(nil, nil)` for ENOENT/zero-byte, `(nil, wrapped err)` for malformed/other-read-error, `(devs, nil)` for a valid list. `Load` constructs a fresh registry from it; `Reload` reconciles it into an existing one. The helper wraps `path` only, never the file bytes — a corrupt `devices.json` may embed a `token_hash`, so echoing its contents into an error (and thence a log) would leak it.

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

## Registry mutators — split into child documents

`Validate`, `Reload`, `ClearRedeemBy`, `SetClientVersion`, and `BindStaticKey` are documented in full in the children below, split out to stay under the per-document search cap:

| Document | Covers |
|---|---|
| [`devices-registry-validate.md`](devices-registry-validate.md) | `Validate` (#210, widened #1529, #2734) — the WS-perimeter token-and-key auth predicate |
| [`devices-registry-reload.md`](devices-registry-reload.md) | `Reload` (#782) — read-through reconcile at handshake, the two-writer clobber guard, and `WithLock`'s history |
| [`devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) | `ClearRedeemBy` (#1528), `SetClientVersion` (#2577), `BindStaticKey` (#2734) — the accept-tail mutators, plus the best-effort-lock-guarded-persist testing lessons |

## Out of scope (deferred)

- **Schema versioning.** Per AC: defer until first migration. The envelope shape reserves the field; add it then, not now.
- ~~**`pyry pair` (mint).**~~ Delivered — builds a `Device`, calls `Add` then `Save`, and (#1531) does so inside a `WithLock` region. See [`features/pyry-pair-command.md`](pyry-pair-command.md).
- ~~**`pyry pair revoke <name>`.**~~ Delivered — calls `Remove(name)` then `Save`, and (#1531) does so inside the same `WithLock` region. See [`features/pyry-pair-command.md`](pyry-pair-command.md).
- ~~**WS handshake auth.**~~ Delivered — the daemon `Load`s once at startup and `Validate(presented)`s per phone connect, and (as of #782) `Reload`s the on-disk set immediately before each v2 handshake's `Validate` so a device paired after startup is accepted without a restart. See [`devices-registry-reload.md`](devices-registry-reload.md) and [`features/v2-session-manager.md`](v2-session-manager.md).
- ~~**Per-device `last_seen_at` updates.**~~ Delivered by #210 (`Validate` advances `LastSeenAt` in memory on every hit). Disk persistence of the advanced value remains the auth handler's concern (periodic `Save` / graceful-shutdown hook); the predicate intentionally does not call `Save`.
- **Push-token registration metadata.** Future top-level field (per `protocol-mobile.md:495`); the envelope shape supports additive growth.
- **Encrypting `devices.json` at rest.** Defer — current threat model doesn't justify the operator UX cost.
- ~~**`Device.Name` display-safety at the `UpdatePushRegistration` write site.**~~ Delivered (#2219) — gated one step upstream, in `RegisterPushToken` (the mutator's only caller), not in `Registry` itself. See § "Phase 3 foundation (#250)" above and [`relay-package-handlers.md`](relay-package-handlers.md#display-safety-gate-on-device_name-and-platform-2219). A name stored before the gate existed is still read back unchecked, and `pyry pair --name` stays deliberately ungated — `Device.Name` has a gated write path, not a type-level display-safety invariant.
- **Schema migration to a database.** Defer.

## Related

- [`features/devices-package.md`](devices-package.md) — `Device` struct + `HashToken` / `VerifyToken` (#208).
- [`features/sessions-registry.md`](sessions-registry.md) — the structural reference implementation (atomic write, envelope shape, forward compat).
- [`features/identity-package.md`](identity-package.md) — sibling Phase 3 foundation that owns `~/.pyry/server-id`.
- [`features/config-package.md`](config-package.md) — sibling Phase 3 foundation that owns `~/.pyry/config.json`.
- [ADR 020](../decisions/020-devices-registry-snapshot-then-write.md) — Save snapshots under lock, performs I/O outside; `Reload` mirrors the lock discipline.
- [ADR 029](../decisions/029-devices-registry-reload-at-handshake.md) — read-through reload at handshake, keep-in-memory-survivor reconcile (#782).
- [ADR 041](../decisions/041-mobile-static-key-binding.md) — binding a pairing to its first install's Noise static key (#2734), and why the earlier "do not persist" decision was reversed.
- [`features/v2-session-manager.md`](v2-session-manager.md) — `V2SessionConfig.DevicesPath` + the handshake reload step.
- [`codebase/782.md`](../codebase/782.md) — the reload ticket.
- `internal/sessions/registry.go:saveRegistryLocked` — canonical atomic-rename recipe.
- `docs/protocol-mobile.md:62` — wire contract: binary stores `sha256(token)` in `devices.json`, never plaintext.
- `docs/protocol-mobile.md:618` — TOCTOU concern this registry's atomic-rename pattern structurally defends.
