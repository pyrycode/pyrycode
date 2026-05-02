# ADR 006: Concurrent active cap with LRU eviction

**Status:** Accepted (2026-05-02, ticket [#41](https://github.com/pyrycode/pyrycode/issues/41))
**Phase:** 1.2c-B
**Builds on:** [ADR 005](005-idle-eviction-state-machine.md) (idle eviction state machine)

## Context

[ADR 005](005-idle-eviction-state-machine.md) gave every session an `active` ↔ `evicted` lifecycle. An idle timer per session evicts the running claude after N minutes of inactivity, and the next message respawns it lazily. Idle eviction alone bounds *steady-state* RAM, not *peak*.

A burst of activity across many sessions can still spike RAM: every session that gets a message stays active until its idle timer fires, regardless of how many other sessions are also active right now. Phase 2.0's first-message lazy bind (Discord channels minting sessions automatically) makes the spike easy to provoke — a noisy server can multiply session count overnight.

A concurrent-active cap closes the gap: a configurable upper bound on the number of running claudes, enforced at the spawn-path entry. When activating one more session would push the active count past the cap, the least-recently-active currently-active peer is evicted at that moment, before the new spawn proceeds.

The design space had three questions:

1. **Where is the cap enforced?** A check inside `Pool.Activate` (every spawn-path entry already lands here) versus a separate gate function callers must remember to invoke.
2. **What lock serialises the cap-check + victim-eviction + new-spawn sequence?** `Pool.mu` (held across the whole sequence) versus a dedicated outer mutex.
3. **How is the LRU victim selected?** Maintain an explicit ordered structure (heap, linked list) versus iterate `pool.sessions` and pick by `lastActiveAt`.

## Decision

1. **The cap is enforced inside `Pool.Activate`.** When `Config.ActiveCap <= 0` the function is byte-identical to Phase 1.2c-A — a thin wrapper around `Session.Activate` with a single early-return branch and no LRU bookkeeping cost on the hot path. When the cap is set, the cap-check + victim eviction + new spawn happen inline before delegating to `Session.Activate`.
2. **A dedicated `Pool.capMu` mutex serialises the cap-binding sequence.** Held only on the cap path (`activeCap > 0`); the uncapped path never touches it. Lock order: `capMu → Pool.mu → Session.lcMu`. `capMu` is the outermost lock and is never re-acquired by callees, so `Session.Evict`'s callback into `Pool.persist` (which takes `Pool.mu`) is deadlock-free.
3. **LRU victim selection iterates `pool.sessions`** under `p.mu.RLock`, reads each `Session.lcState` and `lastActiveAt` under `lcMu`, excludes the target from the candidate set, and picks the oldest `lastActiveAt` among the rest. O(n) in the active session count; for pyry's session counts (≤ 100s for the foreseeable future) the iteration is cheap and avoids a counter-vs-truth drift bug.
4. **`Session.Evict(ctx)` is promoted to a public primitive.** ADR 005 kept eviction internal to `runActive`'s select loop; ticket #41 needs an external trigger that's symmetric with `Activate`. The new public `Evict` is force-eviction — unlike the idle timer, it does **not** defer for `attached > 0`. The cap is a hard limit; an attached caller will see EOF on its bridge.

The two policies — idle (1.2c-A) and cap (1.2c-B) — compose because they share one mechanism. They differ only in *who picks the victim*:

| Trigger | Goroutine | Victim | Mechanism |
|---|---|---|---|
| Idle | per-session timer | itself | `runActive` cancels its own supervisor ctx |
| Cap | `Pool.Activate` caller | LRU active peer | `Session.Evict` |

Both end at `transitionTo(stateEvicted)` which writes the registry and broadcasts `evictedCh`.

## Rationale

### Cap check inside `Pool.Activate`, not a separate gate

`Pool.Activate` is the single spawn-path entry the lifecycle introduces. Every caller who needs a session running calls it (the control plane in `handleAttach`, the future router). Hooking the cap there means the cap can never be bypassed by accident. Idle-timer-driven eviction is *already* off the spawn path, so the only place a new claude starts running is the one place the cap check is gated on.

A separate `Pool.GateActivate` would split a single invariant ("active count ≤ cap at any spawn") across two functions and let a future caller forget the gate. Inlining the check costs ~10 lines and one branch.

### `Pool.capMu` over `Pool.mu` for the cap critical section

Holding `Pool.mu` (write) across the cap sequence was the spec's first sketch. Two problems showed up in implementation:

- **`Session.Evict` calls `Pool.persist` to write the registry.** `Pool.persist` re-takes `Pool.mu`. If `Pool.Activate` held `Pool.mu` (write) across the eviction call, `persist` would deadlock on its own re-entry.
- **Pool.mu is hot.** Lookup, snapshot, and the rotation watcher all read it. Holding it (write) across an eviction's bounded-but-non-trivial child-process work (SIGTERM → wait → SIGKILL, plus registry I/O) blocks unrelated reads.

A dedicated outer mutex (`capMu`) solves both: the cap-binding sequence serialises against itself, but `Pool.mu` is taken and released for the read-side iteration in `pickLRUVictim` and re-taken inside `persist` without re-entrancy. Lock order is documented: `capMu → Pool.mu → Session.lcMu`. `capMu` is never re-acquired by callees, so the order holds trivially.

The uncapped path (`activeCap <= 0`) returns before touching `capMu` at all. "Byte-identical when unset" includes "doesn't even take the cap mutex" — operators who don't set the cap pay zero coordination cost.

### Iterate over an ordered structure

For pyry's expected session counts (≤ 100s) the O(n) iteration is fine and the implementation is one for-loop. A heap or doubly-linked list would shave constant factors at the cost of:

- Maintaining the structure on every `lastActiveAt` bump (adds work to the hot path the cap was supposed to leave alone when unset).
- Risk of drift between the structure and the truth (the counter-vs-iteration class of bug).
- More code to test for the same observable behaviour.

The LRU structure is premature optimisation here. If the cap binds at high frequency in Phase 2.0+ and profiling flags the iteration, the helper signature already supports the swap — `pickLRUVictim` is one function with a clear contract.

### `Session.Evict` as a public primitive — and force-eviction

ADR 005 kept eviction internal to `runActive`. That was correct for #40: only the per-session timer needed to trigger it. With #41, an external caller (the cap path on `Pool.Activate`) needs to drive eviction synchronously. The natural shape mirrors `Activate`: a public method on `*Session` that signals the lifecycle goroutine and blocks until the transition completes (or `ctx` cancels).

The implementation pairs `evictCh` (buffered 1 signal, symmetric to `activateCh`) with `evictedCh` (closed-on-evicted broadcast, symmetric to `activeCh`). `runActive`'s select grew one `case <-s.evictCh` arm: cancel inner supervisor ctx, drain, return.

**Force-eviction is the load-bearing semantic difference from the idle path.** The idle timer's `case <-timer.C` defers eviction while `attached > 0` (re-arms with the full timeout). The cap path's `Session.Evict` does not — the cap is a hard limit. If the LRU victim has a live attach, its claude child is killed; the bridge sees EOF; the operator's terminal disconnects. This is *correct* but worth flagging:

> A future refinement could exclude attached sessions from the LRU candidate set. Not in scope for #41.

`Session.Evict` is also the contract that ADR 005 deferred to a follow-up. ADR 005 considered exposing `evictLocked` for an external caller; #41 is that follow-up and chose the public-method-with-channels shape (idiomatic to the rest of `Session`'s surface) over a lock-held variant (which would have leaked `Pool.mu`'s ordering across the package boundary).

## Consequences

### Positive

- Phase 2.0's auto-mint workload lands against a finished primitive — a noisy Discord server can't blow past the operator-configured RAM ceiling.
- The "byte-identical when unset" property keeps the existing single-CLI-user workflow regression-free: zero new bookkeeping, zero new mutex contention, one branch on the hot path.
- `Session.Evict` is a public primitive future code can call (e.g. an explicit `pyry sessions evict <id>` admin verb, if Phase 2 needs it).
- The cap policy and idle policy share `transitionTo(stateEvicted)`. New observers (metrics, audit log) hook one place and see both kinds of eviction.

### Negative / risks

- **Attached sessions are evictable under cap pressure.** A user actively typing into the LRU session will see their claude die. Mitigated by: (a) operators can leave the cap unset (default) until they actually need it, (b) the LRU heuristic strongly disfavours actively-used sessions because attach state bumps `lastActiveAt`, (c) Phase 2.0 may add an attach-aware filter. The hard-limit posture is the right default; surprise > silent-RAM-blowup.
- **`Session.Evict` drives a SIGKILL via `exec.CommandContext`** (same as the idle path). Truncated final JSONL line is theoretically possible; readers skip incomplete entries on resume. Same risk profile as ADR 005 — graceful supervisor stop is tracked as a follow-up.
- **`pickLRUVictim` is O(n)** in the total session count, called once per cap-binding `Activate`. Trivially within budget for ≤ 100s of sessions; revisit if profiling flags it.

### Neutral

- `Config.ActiveCap` is **not yet wired into `cmd/pyry`'s flag parsing.** The architect deferred CLI exposure to the same Phase 2.0 work that would actually need it. Test-only consumers exercise the AC; production use is gated on the CLI flag landing later.
- `lastActiveAt` is bumped on every state transition (already true since #40) AND on `touchLastActive` for an `Activate` against an already-active session. The latter is **not persisted** — the registry's `lastActiveAt` only flushes on transitions. In-memory LRU ordering reflects the touch; on-disk state does not.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Hold `Pool.mu` (write) across cap sequence | `Session.Evict` re-takes `Pool.mu` via `persist` → deadlock. Even without the deadlock, blocks unrelated reads for the eviction's bounded-but-non-trivial window. |
| Per-`Pool` LRU heap or doubly-linked list | Adds maintenance to every `lastActiveAt` bump (defeats the "zero cost when unset" property) and a class of drift bugs for an O(n) cost that doesn't matter at pyry's scale. |
| Separate `Pool.GateActivate` instead of inlining | Splits one invariant across two functions; future caller can forget the gate. |
| `Session.Evict` as a lock-held variant taking `Pool.mu` | Leaks `Pool.mu`'s ordering across the package boundary; couples `Session`'s API to its host's locking. The channel-based public method is idiomatic to the rest of `Session`'s surface. |
| Cap policy excludes attached sessions from victim set | Right call eventually; out of scope for #41. The hard-limit posture is the cleaner first cut and matches operator intent ("never exceed N running claudes, period"). |
| Wire `Config.ActiveCap` to a CLI flag now | No operator needs it before Phase 2.0; the CLI flag adds ~3 lines that can land in the same PR that exercises the cap in production. |

## References

- Ticket: [#41](https://github.com/pyrycode/pyrycode/issues/41)
- Spec: [`docs/specs/architecture/41-concurrent-active-cap-lru.md`](../../specs/architecture/41-concurrent-active-cap-lru.md)
- Feature doc: [`features/idle-eviction.md`](../features/idle-eviction.md) (covers both 1.2c-A and 1.2c-B)
- Builds on: [ADR 005](005-idle-eviction-state-machine.md)
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)
