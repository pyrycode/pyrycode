# 035. Revive dropped sessions lazily at first touch, sourced from `conv.Cwd` — not registry rehydration at startup

## Status

Accepted (#1487)

## Context

`Pool.New` materialises exactly one `*Session` from `sessions.json` — the bootstrap. `pickBootstrap` is the only reader of `reg.Sessions`, so every session minted per-conversation (`Pool.CreateIn`, the `create_conversation` path) is parsed and then discarded on a warm start, and the first post-restart `saveLocked` erases it from disk permanently. A conversation's `CurrentSessionID` round-trips correctly through the *conversations* registry, so after a restart a healthy binding points at a session the pool no longer has: `sessionRouter.resolve` gets `ErrSessionNotFound` from `Lookup`, and `send_message` rejects with a retryable `server.binary_offline` before any enqueue. The thread is permanently dead. Spec #833 and [conversation-session-binding.md](../features/conversation-session-binding.md) both named this and deliberately scoped it out as "the Pool's existing session-lifecycle / startup-reconciliation concern" — this ADR is where it gets paid.

Two directions existed, with materially different footprints:

1. **Rehydrate `sessions.json` entries at `Pool.New`/`Run`.** The blocker: a minted session's spawn directory is phone-supplied, `$HOME`-confined at mint time, and **not persisted anywhere on the sessions side** — `registryEntry` has no cwd field, and `Session` never retains `spawnDir` after `buildSession` passes it into `RunnerConfig.WorkDir`. A rehydrate driven purely by `sessions.json` has no directory to spawn in and would silently fall back to the daemon template `WorkDir` — reviving the conversation into the *wrong project*, worse than today's loud rejection. Fixing that means adding a cwd field to `registryEntry` — a registry schema change, which the ticket flagged as an always-split pattern in the PO sizing guide. `GetOrCreateIn`/`materialise` also require `p.runGroup`, which is nil during `Pool.New` and only wired in `Pool.Run`, so registration and lifecycle-goroutine scheduling can't both happen at `New` anyway.
2. **Fall back in `sessionRouter.resolve` when `Lookup` misses**, re-minting against the conversation's bound id and its already-persisted `Cwd`. `cmd/pyry` already imports both `conversations` and `sessions`; `resolve` is already the single resolution authority for both `Route` and the drain. `conv.Cwd` is a `$HOME`-confined realpath, persisted since #685/#696 — no schema change needed.

## Decision

Direction 2. Two changes, two production files, no registry schema change:

- **`internal/sessions`**: `GetOrCreateIn`'s body — `ValidID` → `buildSession` → register-under-`p.mu` → `saveLocked` → skip-set → `runGroup` guard → `g.Go(sess.Run)` — is factored into an unexported `materialise(id, label, spawnDir) (*Session, took bool, error)`. `GetOrCreateIn` becomes `materialise` followed by `Activate` on the register path only (never on the take path — that split was previously an inline early return, now the `took` flag). A new exported `Pool.Revive(id, label, spawnDir) (*Session, error)` calls `materialise` and returns without ever calling `Activate`. **No `context.Context` parameter** — that absence is the API signal that `Revive` never spawns and never blocks.
- **`cmd/pyry`**: `sessionRouter.resolve` catches `Lookup`'s `ErrSessionNotFound`, re-runs `resolveSpawnDir(conv.Cwd)` — the *same* validator `sessionMinter.Create` uses at mint time, not a cached decision — and on success calls `Pool.Revive`. A rejected `Cwd` returns wrapping `handlers.ErrSpawnDirRejected` before any pool state is touched, i.e. the same wire outcome the conversation gets today.

The revived session lands at `stateEvicted` with an open `activeCh` and closed `evictedCh` — byte-for-byte the shape an idle-evicted session already has — so it respawns through the **existing** lazy-respawn path on the next `Pool.Activate`. This introduces no new lifecycle state and no new spawn path.

## Rationale

**Why re-validate `Cwd` at revive time instead of trusting the recorded value.** #696 adjudicated that a `Cwd` escaping `$HOME` after symlink resolution — including via a symlinked *ancestor* of a not-yet-existing path — must be rejected. That check ran at mint time only. A path valid when minted can become an escape before the restart (an attacker with write access under `$HOME` swaps a component for a symlink, then waits for the daemon to restart), and the revive is exactly the spawn site that would otherwise trust the stale check. Re-running `resolveSpawnDir` costs nothing extra — it's the same function, same call shape as the mint path — and closes that window with the same code, not a parallel one.

**Why the revived session carries zero `SessionSettings`.** `materialise` passes `SessionSettings{}` on both callers' paths. `registryEntry` persists `YOLO` (`--dangerously-skip-permissions`), and a phone can set it on its own minted session via the v2 settings verb. Before this ADR a restart silently dropped that session, so persisted YOLO on a minted session was inert; rehydration would make it live again across restarts. `registryEntry`'s fail-closed rationale ("neither absence nor corruption can ever enable bypass") was written when only the bootstrap was materialised, and #833's "persisted spawn settings must survive restart" argument was about *operator* intent on the bootstrap — it doesn't transfer to a phone-set flag on a minted session. A restart is a natural revocation point for a permission bypass, and re-granting is one settings verb away. This is free: it requires no code, only the decision not to thread persisted settings through `Revive`.

**Why lazy (first touch) rather than eager (at startup).** Reviving every bound conversation at daemon startup would spawn one claude per conversation on every restart. Lazy, on-touch revive keeps a restart cheap regardless of how many conversations are bound, matching the pool's existing idle-evict/reactivate cost model.

**Why this doesn't widen `resolve`'s non-blocking contract.** Since #721 `send_message` makes no blocking call, and `Route` sits on that path. `Revive`'s absent `context.Context` keeps that true — the revive branch adds filesystem syscalls (`EvalSymlinks`, possibly `MkdirAll`, the `trustMark` write, the registry persist) but never waits on a child process, and runs at most once per session per daemon lifetime; once it succeeds, `Lookup` hits and the steady-state path is byte-identical to today.

## Consequences

- **`registryEntry` gains no field; `Session` retains no new state.** The ticket's red line — "if the design needs a `registryEntry` field, split the ticket" — never trips.
- **Three residues, accepted rather than closed, because closing any of them needs direction 1:**
  - A conversation untouched since the restart is still erased from `sessions.json` by the next unrelated `saveLocked` (e.g. a bootstrap idle-eviction). Harmless under this design, because the revive sources id and cwd from the *conversations* registry, not from `sessions.json` — but it's a narrower guarantee than "a restart never erases a bound entry" read literally.
  - The `sessions.remove` control verb drops a session from the pool and `sessions.json` without clearing the owning conversation's `CurrentSessionID`, so the next touch re-registers the "removed" id. Distinguishing "removed" from "dropped by restart" needs persisted registry membership, which is direction 1's territory.
  - `buildSession` stamps a fresh `CreatedAt`, so a revived entry loses its original creation timestamp (cosmetic — only affects on-disk sort order).
- **A phone-set YOLO is lost from disk, not just made inert.** The next `saveLocked` after a revive rewrites that entry with the setting dropped. Deliberate (see Rationale); do not "fix" this by threading persisted settings into `Revive` without re-opening this ADR.
- **`GetOrCreateIn`'s take-path contract ("returns without activating") became a runtime branch (`took bool`) instead of a structural early return.** It is provably violable now, where it previously wasn't — see [codebase/1487.md](../codebase/1487.md)'s Lessons learned for the mutation that exposed this and the test it forced.
- **Escaping-`Cwd` rejections stay retryable.** A permanently-escaping `Cwd` produces the same retryable `server.binary_offline` a healthy-but-dropped binding gets today, so the phone can retry forever against a condition that will never clear. Matches existing behavior exactly; a dedicated non-retryable code is the named #672-family follow-up, not this ADR's concern.

## Related

- [codebase/1487.md](../codebase/1487.md) — implementation summary, lessons learned.
- [features/sessions-package.md § Reviving a dropped session](../features/sessions-package-key-types-reviving-a-dropped-session-pool-revive.md#reviving-a-dropped-session-poolrevive-1487) — `Pool.Revive` / `materialise` mechanism.
- [features/conversation-session-binding.md § Restart scope](../features/conversation-session-binding.md#edge-cases--limitations) — the deferral this ADR closes, and the residues.
- [ADR 030](030-plain-bool-failsafe-persisted-flag.md) — the persisted-`YOLO` fail-safe rationale this ADR extends to the revive path.
- Ticket: [#1487](https://github.com/pyrycode/pyrycode/issues/1487).
- Code: `internal/sessions/get_or_create.go` (`materialise`), `internal/sessions/revive.go` (`Pool.Revive`), `cmd/pyry/main.go` (`sessionRouter.resolve`, `sessionRouter.revive`).
