# 029 — Daemon reloads the devices registry at handshake (read-through reconcile)

## Context

The daemon `devices.Load`s `~/.pyry/<name>/devices.json` **once at startup**
(`cmd/pyry/relay.go`) and validates every phone/desktop handshake against that
in-memory snapshot (`internal/relay/v2session.go` → `Registry.Validate`).
`pyry pair` runs as a **separate process**: it `Load`s the same file, `Add`s the
new device, and atomically rewrites it. A device paired *after* the daemon
started is on disk but absent from memory, so its first hello is rejected with an
invalid-token error until the service restarts — pairing was effectively
pair-then-restart (#782, root-caused on the mobile real-device run 2026-07-03).

Two constraints shaped the fix:

1. **Two writers, one file.** The daemon is *also* a writer of `devices.json`:
   `register_push_token` calls `Registry.Save` (`Validate` only advances
   `LastSeenAt` in memory, never persists). Both the daemon and `pyry pair`
   rewrite the whole file via atomic rename → **last-writer-wins on the entire
   file**. A stale-in-memory daemon that Saves would *erase* a device `pyry pair`
   just added.

2. **`Validate`'s security contract must not move** (empty-token short-circuit;
   never logs/returns token, hash, or name — [ADR 020](020-devices-registry-snapshot-then-write.md)
   sibling, `internal/devices/auth.go`).

## Decision

**Mechanism (a): read-through reload at handshake time, self-contained in the
daemon.** A new `(*Registry).Reload(path) error` reconciles the on-disk device
set into the in-memory registry under `r.mu` immediately before `Validate` at the
v2 handshake, and immediately before `Save` in `register_push_token`.

Reconciliation is a **read-modify-write keyed on `TokenHash`**, not a blind swap
of the in-memory slice:

- Device in **both** memory and disk → **keep the in-memory struct** (survivor):
  it carries the daemon's un-persisted `LastSeenAt` bump and any in-flight push
  registration. The daemon is the sole writer of those fields, so memory ≥ disk
  for them, and `pyry pair` has no verb that edits an existing device under a
  stable `TokenHash`.
- Device **only on disk** → adopt (newly paired — no in-memory runtime state).
- Device **only in memory** → drop (revoked via `pyry pair revoke`; reflecting
  unpair is a *free consequence* of the same mechanism).

Membership after `Reload` == disk's exact set → the accept set is never widened
beyond what is on disk. On malformed/unreadable disk `Reload` returns a wrapped
error and leaves memory **unchanged** (fail closed); callers proceed to
`Validate`/`Save` against the retained set.

## Rationale

1. **(a) over (b) "signal the daemon to reload."** A control socket exists
   (line-delimited JSON over a Unix socket), so signalling is not a dead end —
   but it would couple `pyry pair` to a *running* daemon. Pairing is
   daemon-independent today (it works whether or not the service is up); (b)
   would break that. (a) keeps the whole fix inside the daemon: no new IPC, no
   coupling, no schema change.

2. **Read-modify-write over blind-swap.** Reload-at-handshake *alone* fixes the
   restart bug but re-introduces it as **data loss** through the write path:
   memory `[A]`; `pyry pair` adds B → disk `[A,B]`; device A (live since startup)
   sends `register_push_token` *before B handshakes* → `Save` writes `[A]` → **B
   erased**. Reconciling disk into memory *before* the Save (`[A_push,B]`) is what
   closes that hole. The reload must therefore be applied at the daemon's read
   (handshake) **and** write (`register_push_token`) sites.

3. **Keep-survivor over adopt-disk.** For a single daemon, memory is authoritative
   for the only daemon-mutated fields (`LastSeenAt`, push registration). Adopting
   the disk struct on a hash match would clobber a just-bumped `LastSeenAt` with a
   stale value.

## Consequences

- **Pairing is a single step.** Start daemon → `pyry pair` → connect → accepted,
  no restart. Covers mobile today and the desktop client next (both speak Mobile
  Protocol v2).

- **Revoke reflects on the next handshake for free.** `pyry pair revoke` writes a
  valid `{"devices":[]}` (or a shorter list); the next `Reload` drops the
  absent-from-disk device. Live-conn revocation propagation stays out of scope
  (a separate, already-documented concern — reload reflects revoke/pair on the
  *next* handshake only).

- **The cross-process TOCTOU window is narrowed, not closed.** No shared lock
  spans `pyry pair` and the daemon (no file-lock mechanism — out of scope). A
  `Save` can still race a `pyry pair` add. ~~The *next* reload reconciles
  it.~~ **Wrong — see [Correction (2026-08-19, #1530)](#correction-2026-08-19-1530) below: `Reload`
  reconciles memory into disk, with disk authoritative for membership, so a
  `Save` that races out a `pyry pair` write erases it from disk permanently.
  Nothing resurrects it.** Same last-writer-wins baseline as before #782 — no
  new vulnerability, but the window is not self-healing.

- **One bounded JSON read per handshake.** On the single manager Run goroutine,
  gated on `DevicesPath != ""`, only on the post-IK path (the peer already
  completed an IK handshake to reach it). No meaningful new DoS surface.

- **New log sites must not leak secrets.** A `json.Unmarshal` error on a corrupt
  `devices.json` can echo file bytes that include a `token_hash`. The two
  reload-failure Warn lines therefore record `path` + a **static** reason only,
  **never** the wrapped `err` — `Validate`'s non-leakage contract extends to every
  new site the reload introduces.

- **ENOENT-after-startup → empty set (open item).** Mirrors `Load`; `revoke`
  writes a valid empty-list file (never ENOENT), so only manual deletion trips it.
  If "deleting devices.json silently unpairs everyone" proves surprising, a future
  refinement could fail-closed-keep on ENOENT-after-a-nonempty-load. Deferred — no
  observed failure, and it would diverge `Reload` from `Load`.

- **Future device-edit verb caveat.** Keep-survivor assumes an existing device's
  disk fields never change under a stable `TokenHash` (true today — revoke+re-pair
  mints a *new* hash). A future `pyry pair edit` must revisit whether disk or
  memory wins per field.

## Alternatives considered

- **(b) `pyry pair` signals the running daemon to reload.** Rejected — couples a
  daemon-independent operation to a running service and adds IPC for no benefit
  over the read-through read the daemon already does at handshake.

- **Blind-swap the in-memory slice to disk's contents.** Rejected — erases the
  `LastSeenAt` / push-registration state the daemon holds but has not yet
  persisted, and does nothing for the write-path clobber.

- **Reload only at the handshake (not at `register_push_token`).** Rejected —
  trades the restart bug for a data-loss bug (worked scenario above).

- **A file lock (flock) shared by `pyry pair` and the daemon.** Rejected — a new
  cross-process mechanism / schema concern for a window the reconcile already
  narrows to negligible; out of scope for an S-sized fix (Simplicity First).
  ~~Rejected.~~ **Revisited and adopted — see [Correction](#correction-2026-08-19-1530) below.**
  The premise (the reconcile narrows the window to negligible) does not hold:
  it narrows only the daemon's own read-then-write gap, not `pyry pair`'s much
  wider window, and the loss it leaves open is terminal, not negligible.

## Correction (2026-08-19, #1530)

Two claims in this ADR were wrong, both stemming from conflating what `Reload`
reconciles. `Reload` reconciles **memory into disk** — disk is authoritative
for membership (see § Decision above: disk-only devices are adopted,
memory-only devices are dropped). It does not reconcile disk *from* memory.

1. **"A `Save` can still race a `pyry pair` add; the next reload reconciles
   it"** (§ Consequences) is false. Once a daemon `Save` writes a snapshot
   that omits a device `pyry pair` just added, that device is gone **from
   disk**. Every subsequent `Reload` reads the file that no longer contains
   it — reconciling memory to an already-lossy disk state reconciles nothing
   back. The loss is silent and terminal, not self-healing.
2. **The flock rejection's premise** — that the reconcile already narrows the
   cross-process window to negligible — was wrong for the same reason, and
   understated the asymmetry: the daemon's own window is narrow (the
   microseconds between `Reload` and `Save` in `register_push_token`), but
   nothing narrows `pyry pair`'s side, whose window spans
   `identity.LoadOrCreate`, `keys.LoadOrCreate` (keypair mint + persist on
   cold start), and a CSPRNG read — bounded by file I/O and key generation,
   not microseconds. The race also costs more than a lost *add*: ordering
   entirely within the daemon's own narrow window can lose a **revoke**
   (`Reload` reads `[X]` → `pyry pair revoke` writes `[]` → `Save` writes
   `[X]` back, and the daemon's in-memory set never dropped X), which is the
   security-relevant direction.

#1530 shipped the primitive this ADR rejected: `internal/devices.WithLock`, a
sibling-file `flock(2)` critical section (`docs/knowledge/architecture` cite —
see [`codebase/1530.md`](../codebase/1530.md)). That slice ships the
primitive only; no caller here is rewired yet, so the race this correction
describes stays open until the `cmd/pyry` and daemon consumer slices land and
call `WithLock` around their existing `Load`/`Reload` → mutate → `Save`
sequences. This ADR's core decision — reload-at-handshake to fix the
pair-then-restart bug (#782) — is unaffected and still correct; only the
"self-healing window" framing around it was wrong.

See also the same correction applied to
[`features/devices-registry.md`](../features/devices-registry.md) §
*Two-writer clobber guard*.

## Related

- Ticket #782 — [`codebase/782.md`](../codebase/782.md).
- [ADR 020](020-devices-registry-snapshot-then-write.md) — Save snapshots under
  lock, writes outside; `Reload` mirrors that lock discipline (I/O off the lock,
  reconcile-and-assign under `r.mu`).
- [`features/devices-registry.md`](../features/devices-registry.md) § Reload —
  the primitive.
- [`features/v2-session-manager.md`](../features/v2-session-manager.md) —
  `V2SessionConfig.DevicesPath` + the handshake reload step.
- `internal/devices/auth.go` — `Validate`'s security contract the reload runs
  *before*, never inside.
