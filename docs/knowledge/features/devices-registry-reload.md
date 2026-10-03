# `Reload` — read-through reconcile at handshake (#782)

Split from [`devices-registry.md`](devices-registry.md) (the parent document). This child covers the on-startup-staleness fix: a device paired after the daemon started.

`Reload(path) error` lets a device paired via `pyry pair` **after** daemon startup
authenticate on its next handshake without a service restart. The daemon `Load`s
the registry once at startup and holds that snapshot for its lifetime, so before #782 a device added to `devices.json` by the separate `pyry pair` process was on
disk but absent from memory and rejected until restart. `Reload` reconciles the
on-disk set into memory, disk being authoritative for **membership**:

| Disk state | `Reload` returns | In-memory effect |
|---|---|---|
| Missing (ENOENT) | `nil` | membership → empty (disk says none) |
| Zero-byte | `nil` | membership → empty |
| Valid list (incl. `{"devices":[]}`) | `nil` | membership → disk's set (adds newly-paired, drops revoked) |
| Malformed JSON | wrapped error | **unchanged** (fail closed) |
| Other read error (EACCES/EIO/EISDIR) | wrapped error | **unchanged** (fail closed) |

### Reconciliation — keep-in-memory-survivor, keyed on `TokenHash`

The reconcile (pure helper `reconcileDevices(memory, disk)`) is a
**read-modify-write, not a blind swap**:

- A `TokenHash` present in **both** memory and disk → **keep the in-memory
  struct**. It carries the daemon's un-persisted `LastSeenAt` bump (from
  `Validate`) and any in-flight push registration; the daemon is the sole writer
  of those fields, so memory ≥ disk for them. `pyry pair` has no verb that edits
  an existing device under a stable `TokenHash` (revoke+re-pair mints a *new*
  hash → reconciles as drop-old + add-new).
- Disk-only → **adopt** the disk struct (newly paired — no in-memory runtime
  state).
- Memory-only → **drop** (revoked via `pyry pair revoke` — reflecting unpair on
  the next handshake is a free consequence of the mechanism).

Membership after `Reload` == disk's exact set, so the accept set is **never
widened** beyond what is on disk. Result order = disk order (irrelevant; `Save`
re-sorts, lookups scan linearly).

### Lock discipline

Mirrors `Save` (see [`devices-registry.md`](devices-registry.md) § Save concurrency and [ADR 020](../decisions/020-devices-registry-snapshot-then-write.md)):
the disk read via `readDevicesFile` happens **outside** `r.mu`; only the
reconcile-and-assign takes `r.mu`. `Reload` never nests locks and never calls back
into a locked path, so concurrent `Reload` / `Validate` / `Save` from different
goroutines serialise safely at the single `r.mu`. `Reload` runs **before**
`Validate`, never inside it — `Validate`'s security contract (empty-token
short-circuit; never logs/returns token, hash, or name) is untouched, and `Reload`
itself does no logging and no token/hash/name handling.

### Two-writer clobber guard

The daemon is *also* a writer of `devices.json` (`register_push_token` →
`UpdatePushRegistration` → `Save`; `Validate` never persists). Both the daemon and
`pyry pair` rewrite the whole file via atomic rename → last-writer-wins on the
entire file. A stale-in-memory daemon that `Save`s would erase a device `pyry
pair` just added. Reload-at-handshake **alone** does not fix this: A (live since
startup) can `register_push_token` before B ever handshakes, and its `Save` erases
B. So the reload is applied at **both** the daemon's read (v2 handshake, before
`Validate`) **and** write (`register_push_token`, before `Save`) sites. See [ADR
029](../decisions/029-devices-registry-reload-at-handshake.md) for the worked
scenario and the mechanism decision (read-through over signalling the daemon).

Consumers (`internal/relay`) fail closed on a `Reload` error: the v2 handshake
proceeds to `Validate` against the retained set (accept set not widened, loaded
devices not lost); `register_push_token` still `Save`s the known-good in-memory
state (self-heal). Both log `path` + a static reason only — **never** the wrapped
`err` (a `json.Unmarshal` error on a corrupt file can echo bytes carrying a
`token_hash`). See [`features/v2-session-manager.md`](v2-session-manager.md) for
the wiring.

**Correction (2026-08-19, #1530): this "guard" narrows only the daemon's own
window, not the cross-process race.** Applying `Reload` at both the read and
write sites closes the *within-daemon* ordering hole (#782's original bug),
but it does nothing for `pyry pair`'s side of the race: `Reload` reconciles
memory *from* disk, so once a `Save` has written a snapshot that omits a
device `pyry pair` just added, that device is gone from disk permanently —
no later reload brings it back. [ADR 029's correction](../decisions/029-devices-registry-reload-at-handshake.md#correction-2026-08-19-1530)
has the full analysis, including the revoke-direction case (a revoked device
can keep authenticating if a `register_push_token` interleaves between the
revoke's write and the daemon's read of it). #1530 added
`internal/devices.WithLock`, a cross-process `flock(2)` primitive sized to
close this — see [`codebase/1530.md`](../codebase/1530.md).

**Update (2026-09-07, #1528): the primitive has its first caller, but the
three writers named above still don't use it.** `recordRedemption` (the
redemption-clear writer, see [`devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `ClearRedeemBy`) is a fourth
`devices.json` writer, lock-compliant from birth — it is presently the only
production caller of `WithLock` in the tree. `pyry pair` (mint), `pyry pair
revoke`, and `register_push_token` are unaffected and still race each other
exactly as described above; #1531/#1532 are the tickets that retrofit them.
`recordRedemption` is the pattern to copy when they do: `WithLock` around a
`Reload` → mutate → `Save` region, reloading *inside* the lock rather than
only before it, since the lock excludes a concurrent commit but does nothing
about one that already landed between the pre-lock reload and the
acquisition.

**Update (2026-09-07, #1531): `pyry pair` (mint) and `pyry pair revoke` are
lock-compliant too, closing CLI-vs-CLI in both directions.** `register_push_token`
is now the one writer left unlocked (#1532), so the headline daemon-vs-CLI race
is still open — a live daemon's stale `Save` can still erase a device `pyry
pair` just added, or resurrect one `pyry pair revoke` just removed. What #1531
closes: a `pyry pair` racing a `pyry pair revoke` can no longer resurrect the
revoked device (the security-relevant direction), and CLI-vs-`recordRedemption`
now serializes on the same sidecar. See [`features/pyry-pair-command.md`](pyry-pair-command.md)
for the retrofit's operation-order and concurrency detail.

**Update (2026-09-15, #1532): every `devices.json` writer now holds the
lock, closing the headline daemon-vs-CLI race in both directions.**
`RegisterPushToken` (`internal/relay/handlers/register_push_token.go`) wraps its
reconcile, its `UpdatePushRegistration` mutation and its `Save` in one
`devices.WithLock` region, acquired before the reconcile's read — the reload is
run *inside* the region rather than only before it, for the same reason
`recordRedemption`'s is: the lock excludes a writer from committing *during* the
region but says nothing about one that already committed between this conn's
handshake reload and this acquisition. A `pyry pair` that commits while the
handler is parked on the lock now survives the handler's `Save`; a device `pyry
pair revoke` removed out from under this conn is no longer resurrected by it.

The reconcile also moved to run **before** the mutation, which buys the revoke
direction for free — the same ordering `ClearRedeemBy` already gets. A device
the in-region reload drops (because it was revoked mid-conn) makes
`UpdatePushRegistration` report no match, so nothing is saved and the frame is
refused on the existing non-retryable `auth.invalid_token` rather than acked.
That reordering is the one deliberate reply change in this slice; every other
reply is byte-unchanged. The handler's dedupe and three display-safety guards
(#2219) stay ahead of the region and take no lock, so a deduped or refused frame
still creates no lock sidecar.

A lock the handler cannot acquire within its bounded `pushRegistryLockWait`
(250ms, matching `redemptionLockWait` and `pairLockWait`'s sibling shape) surfaces
on the existing retryable `server.binary_busy` reply rather than skipping to an
unlocked write, with a log event (`register_push_token.lock_busy`) distinct from
an in-region `Save` failure so an operator can tell "another writer held it" from
"the disk write failed" — a distinction that costs nothing at the reply layer,
since both map to the same retryable refusal the phone cannot act on
differently. See [`features/relay-package-handlers.md`](relay-package-handlers.md)
for the handler's full contract and log table.

Retrofitting mint surfaced the general trap in this pattern, worth naming for
the next `WithLock` caller: **wrapping the existing `Save` in `WithLock` is not
the same as moving the `Load` inside it.** A build that only locks around
`Save` passes every "refuses when busy" test, because it genuinely does refuse
when busy — the stale-snapshot bug it leaves in place only shows up under an
interleaving assertion, not a contention one. See [`devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § "Testing a best-effort,
lock-guarded persist" for that test shape.

### Tests

`internal/devices/registry_test.go`, mirroring the existing table + race-probe
idiom:

- `TestReload` — added-adopted, removed-dropped, missing→empty, empty-list→empty,
  malformed→fail-closed-no-loss, unreadable→fail-closed-no-loss (points `path` at
  a directory for a deterministic `EISDIR`, avoiding chmod races).
- `TestReload_PreservesInMemoryLastSeenAt` — pins keep-survivor: a
  `Validate`-bumped `LastSeenAt` survives a `Reload` whose disk record still shows
  the older value.
- `TestReload_ConcurrentReloadValidate` — `-race` probe (concurrent
  `Reload`/`Validate`/`List`).
