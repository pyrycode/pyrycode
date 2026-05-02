# Spec — Pool.Remove core (terminate + registry remove)

Ticket: #94 (split from #64). Phase 1.1d-A1.

## Context

Phase 1.1's CLI session management needs a delete primitive that the
control-plane / CLI layer can call without touching processes or
`sessions.json` directly. `Pool` already owns the registry mutex, the
child-process map, and the disk-write discipline (`saveLocked`); the
delete operation belongs there.

This ticket lands the **core** primitive only: terminate the claude
process and remove the registry entry. The on-disk JSONL is **not
touched** — that disposition (archive / purge) is 64-A2 / #95. No CLI
verb yet (#65). No UUID-prefix resolution (#65). No bulk remove.

The shape is deliberately a sibling of `Pool.Rename` (typed mutator)
and `Pool.Create` (cap-aware spawn): a small surface, one entry point,
ride existing seams (`Session.Evict` for termination, `saveLocked` for
persistence).

## Design

### Public surface

One new method and one new exported sentinel on `internal/sessions`:

```go
// ErrCannotRemoveBootstrap is returned by Pool.Remove for the bootstrap
// session. The bootstrap is a per-process invariant, not an operator
// resource — removing it would leave the pool in a state Pool.Lookup("")
// can't satisfy. Matchable via errors.Is.
var ErrCannotRemoveBootstrap = errors.New("sessions: cannot remove bootstrap session")

// Remove terminates the named session's claude process (if running) and
// drops its registry entry. The on-disk JSONL is NOT touched — disposition
// (archive / purge) is the caller's concern (see #95).
//
// Returns ErrSessionNotFound for an unknown id, ErrCannotRemoveBootstrap
// for the bootstrap entry, or ctx.Err() if termination is cancelled.
// On any of those error paths, the in-memory pool, on-disk sessions.json,
// and the JSONL on disk are byte-identical to their prior state.
//
// Returns only after the child has exited (modulo ctx cancellation).
func (p *Pool) Remove(ctx context.Context, id SessionID) error
```

Signature picks `(ctx, id)` over the AC's bare `(id)` shape because
`Session.Evict` accepts a context for cancellation; passing one through
keeps the long-lived termination interruptible. Same shape as
`Pool.Activate(ctx, id)` and `Pool.Create(ctx, label)`.

### Sequence

```
Pool.Remove(ctx, id)
  ├─ p.mu.Lock()
  │   sess, ok := p.sessions[id]
  │   ! ok                → ErrSessionNotFound  (return; pool unchanged)
  │   sess.bootstrap      → ErrCannotRemoveBootstrap
  │   delete(p.sessions, id)
  │   if err := p.saveLocked(); err != nil:
  │       p.sessions[id] = sess          // rollback
  │       return err                      // disk + memory consistent
  │ p.mu.Unlock()
  │
  └─ sess.Evict(ctx)
        (blocks until child has exited, or ctx cancels)
```

### Why delete-then-evict rather than evict-then-delete

Three orderings were considered:

1. **Evict, delete, save** — but `Session.Evict`'s lifecycle goroutine
   calls `transitionTo`, which calls `Pool.persist`, which acquires
   `p.mu` (write). If `Pool.Remove` were holding `p.mu` across `Evict`,
   the goroutine deadlocks on `p.mu`. The cap-policy path (#41) has the
   same constraint and resolves it with `capMu` as the outer mutex.
2. **Evict-then-delete** (drop `p.mu` across `Evict`) — leaves a window
   where `Pool.Lookup(id)` returns the session but its child is dead;
   a concurrent `Pool.Activate(id)` would re-spawn and orphan a claude
   under an entry we're about to remove.
3. **Delete-then-evict (chosen)** — drop the entry from `p.sessions`
   *first*, persist, release `p.mu`, then call `sess.Evict`. From the
   moment `p.mu` is released, all `Lookup`/`Activate`/`Rename`/`List`
   paths see the session as gone. The lifecycle goroutine still
   transitions through `transitionTo → Pool.persist`; that persist
   reads `p.sessions` and writes a registry that already lacks the
   entry. The redundant write is harmless.

The deviation from the AC's literal "Pool.mu held across termination"
is intentional and follows the same pattern #41 established for cap
eviction: the AC's atomicity goal is met (no concurrent observer sees
a half-removed state), but the lock isn't actually held *during*
`Evict` — releasing it lets `transitionTo`'s persist callback
acquire `p.mu` normally.

### Save-failure rollback

If `saveLocked` fails after the in-memory delete, restore the entry to
`p.sessions` before unlocking and return the error verbatim. This
mirrors `Pool.Rename`'s rollback discipline: on-disk state and
in-memory state must agree at every observable point.

The temp-file + rename discipline in `saveRegistryLocked` makes
partial-write corruption unreachable, so this rollback is
belt-and-suspenders for the "destination dir vanished" / "ENOSPC"
class of failures. If we don't roll back, a subsequent `List` /
`Lookup` would return inconsistent results vs. the disk.

### Termination reuse

`Session.Evict` already drives the child's exit (cancel supervisor's
inner context → `exec.CommandContext` SIGKILLs → `cmd.Wait` returns →
`runActive` returns → `Run` calls `transitionTo(stateEvicted)` →
`evictedCh` closes → `Evict` returns).

`Pool.Remove` does **not** re-implement any of this. It just calls
`Evict` with the caller's ctx. Termination is force-evict — does not
defer for `attached > 0`, same as the cap-policy path. Any attached
client receives EOF on its bridge, matching cap-policy semantics.

(Note: `exec.CommandContext` sends SIGKILL on cancel; there is no
SIGTERM grace in the supervisor today. Earlier docs referencing
"SIGTERM → grace → SIGKILL" describe an aspiration, not the current
behaviour. SIGKILL cannot be ignored, so no fallback path is needed —
see the test note below.)

### Already-evicted sessions

If `sess` is already in `stateEvicted` (prior idle timeout, prior
cap-policy eviction, or warm-start in evicted), `Session.Evict` is an
immediate no-op: the lifecycle goroutine doesn't transition (already
evicted), no redundant persist runs. `Pool.Remove`'s own `saveLocked`
(step 6) is the only persistence; correct.

### Lifecycle goroutine after Remove

After `Pool.Remove` returns, `sess.Run`'s loop transitions to
`runEvicted` and parks on `<-s.activateCh` and `<-ctx.Done()`. The
session is no longer reachable via `Pool.sessions`, so no caller can
signal `activateCh`. The goroutine survives until the pool's `runCtx`
cancels at pool shutdown (typically pyry exit).

This is a bounded resource cost: one orphan goroutine + ~kilobyte
`*Session` per `Remove` call per pool lifetime. For pyrycode's expected
workload (low-tens of sessions per pool, operator-driven removes),
this is operationally invisible. Per the project's evidence-based fix
selection: don't add a per-session terminate signal until observed.

A code comment on `Pool.Remove` documents this trade-off.

### Concurrency

Lock order is unchanged: `Pool.mu → Session.lcMu`. `Pool.Remove` does
not take `Session.lcMu` (it doesn't read `lcState`/`lastActiveAt`/
`attached`); the lifecycle goroutine continues to take `lcMu` inside
`transitionTo` exactly as before.

Concurrent operations:

| Concurrent caller | Outcome |
|---|---|
| `Pool.Lookup(id)` after `p.mu` released | `ErrSessionNotFound` |
| `Pool.Activate(ctx, id)` after `p.mu` released | `ErrSessionNotFound` (via Lookup) |
| `Pool.Rename(id, …)` after `p.mu` released | `ErrSessionNotFound` |
| `Pool.List` / `Snapshot` during `Evict` | does not see the removed session |
| `Pool.Remove(ctx, sameID)` racing | one wins the `p.mu` deletion; the other gets `ErrSessionNotFound` |
| `Pool.Remove(ctx, otherID)` | independent; both serialise through `p.mu` for their respective deletes |
| Cap-policy `pickLRUVictim` | iterates `p.sessions`; cannot pick a removed session |

The `p.mu` write held during step 1–6 briefly blocks `List` / `Lookup`
/ `Snapshot` readers, but the held window is just the in-memory
delete + the `saveLocked` disk write — same envelope as `Pool.Rename`
and `Pool.Create`'s persist phase, well-precedented.

`Session.Evict` is called *outside* `p.mu`, so its grace window does
not block other registry readers.

### What does NOT change

- `Session` struct: no new fields. No `terminate` channel, no per-session
  cancel func.
- `Session.Run`, `runActive`, `runEvicted`: untouched.
- `Pool.Run`, `Pool.supervise`: untouched.
- `Pool.persist`, `Pool.saveLocked`, `saveRegistryLocked`: untouched.
- Wire protocol, control plane, `cmd/pyry`: untouched (this ticket is
  internal to `internal/sessions`).
- Lock-order graph: untouched.

### Edge cases & error semantics

- `id == ""` — falls into the `! ok` branch (empty key not in map),
  returns `ErrSessionNotFound`. `Pool.Lookup`'s "empty resolves to
  bootstrap" rule does NOT apply to `Remove` — we want explicit ids
  for destructive operations.
- ctx cancelled before `sess.Evict` returns — `sess.Evict` returns
  `ctx.Err()`; `Pool.Remove` returns it verbatim. The entry is already
  deleted and persisted; the child may still be terminating. The
  lifecycle goroutine eventually completes its transition because the
  pool's `runCtx` is independent of the caller's ctx.
- ctx already cancelled at entry — pre-checks still run; `Evict`
  returns `ctx.Err()` immediately. Entry may end up deleted with the
  child terminating asynchronously, the same as the late-cancel case.
  Acceptable; the caller is signalling "stop now".
- Concurrent Remove + Activate of same id — pre-check window is the
  only race surface. The `p.mu` write held by Remove blocks Activate's
  `Pool.Lookup`-via-RLock until the delete commits. Once committed,
  Activate sees `ErrSessionNotFound`. No respawn race.

## Files touched

- `internal/sessions/pool.go`
  - `+` `ErrCannotRemoveBootstrap` sentinel (~3 lines incl. doc)
  - `+` `Pool.Remove` method (~30–35 lines incl. doc)
- `internal/sessions/pool_remove_test.go` *(new)*
  - Five tests as listed below.

No changes to `Session`, `cmd/pyry`, `internal/control`, or any other
package. No changes to `sessions.json` schema.

Total production code: ~35–40 lines. Test code: ~250–300 lines.

## Testing strategy

Tests follow the established `pool_rename_test.go` / `pool_create_test.go`
shape. Helpers `helperPoolCreate`, `runPoolInBackground`, `pollUntil`,
`uuidPattern` are reused.

### Tests

1. **`TestPool_Remove_HappyPath`** —
   `helperPoolCreate(t, regPath, 0)` → `runPoolInBackground` →
   `pool.Create(ctx, "")` → wait for `ChildPID > 0` and
   `LifecycleState == stateActive` → write a stub JSONL file at
   `<claudeSessionsDir>/<id>.jsonl` (any bytes) → `pool.Remove(ctx, id)`.
   Assert:
   - `Remove` returns nil.
   - `pool.Lookup(id)` returns `ErrSessionNotFound`.
   - `pool.List()` does not include the removed id.
   - `loadRegistry(regPath).Sessions` has length 1 (bootstrap only).
   - The captured `*Session` has `LifecycleState == stateEvicted` and
     `State().ChildPID == 0` (child has exited).
   - The stub JSONL file at `<id>.jsonl` is byte-identical to what we
     wrote (Remove did not touch it).

2. **`TestPool_Remove_Bootstrap_Rejected`** —
   `helperPoolPersistent(t, regPath)` → capture pre-state (registry
   bytes, `pool.List()`, child PID — though no child is running with
   helperPoolPersistent). Write a stub JSONL at `<bootstrapID>.jsonl`.
   Call `pool.Remove(ctx, bootstrapID)`. Assert:
   - Returns `ErrCannotRemoveBootstrap` (matchable via `errors.Is`).
   - Registry on disk byte-identical to pre-state.
   - `pool.List()` deep-equal to pre-state.
   - JSONL stub byte-identical.

3. **`TestPool_Remove_UnknownID`** —
   `helperPoolPersistent(t, regPath)` → capture pre-state → write a
   stub JSONL at `<unknown-uuid>.jsonl` → call
   `pool.Remove(ctx, "00000000-0000-4000-8000-000000000000")`. Assert:
   - Returns `ErrSessionNotFound`.
   - Registry bytes + `List()` + JSONL all byte-identical.

4. **`TestPool_Remove_RaceWithList`** —
   `helperPoolCreate(t, regPath, 0)` → `runPoolInBackground` →
   spawn N goroutines (8 creators+removers, 8 List readers) for a
   bounded number of iterations. Creators repeatedly `Create` then
   `Remove` the freshly-minted id; readers repeatedly call `List`.
   The assertion is "go test -race is silent and no errors logged".
   The bootstrap is never removed (Create returns a fresh non-bootstrap
   id; Remove targets that id specifically). Same precedent as
   `TestPool_Rename_RaceWithList` and the cap-test race patterns.

5. **`TestPool_Remove_TerminatesUncooperativeChild`** —
   Uses the `TestHelperProcess` pattern (already established in
   `internal/supervisor/supervisor_test.go`). Build a `Config` whose
   `Bootstrap.ClaudeBin` is `os.Args[0]` with
   `ClaudeArgs=["-test.run=TestHelperProcess", "--"]` and
   `helperEnv=GO_TEST_HELPER_PROCESS=1, GO_TEST_HELPER_MODE=sleep,
   GO_TEST_HELPER_SLEEP=24h` so the helper child blocks for 24 hours
   ignoring any non-SIGKILL signal. (Or define a new `block_forever`
   mode that installs SIGTERM/SIGINT handlers as no-ops, reinforcing
   the "ignores cooperative signals" property — but `sleep 24h` is
   simpler and SIGKILL cuts it off regardless.)

   Create a session running this helper, wait for `ChildPID > 0`,
   capture the PID, call `Pool.Remove(ctx, id)` with a short bounded
   ctx (e.g. 10s timeout — generous safety margin; in practice
   SIGKILL → `cmd.Wait` returns within milliseconds). Assert:
   - Remove returns nil within the budget (no real-time `time.Sleep`
     in the test body — the assertion is about Remove's return time
     bounded by the ctx, not a fixed sleep).
   - The captured PID is no longer alive (POSIX zero-signal probe:
     `os.FindProcess(pid)` + `Process.Signal(syscall.Signal(0))` →
     non-nil error, same shape as `internal/e2e`'s `processAlive`).
   - Lookup returns `ErrSessionNotFound`.

   This satisfies the AC's "SIGKILL fallback when the child ignores
   SIGTERM" — the supervisor sends SIGKILL via `exec.CommandContext`,
   which terminates uncooperative children deterministically. The
   test exercises the path without leaning on `time.Sleep`.

### Coverage check

Maps to the AC test list:

| AC test requirement | Test |
|---|---|
| Successful remove of running session (process exits, registry entry gone, on-disk file reflects, JSONL still on disk) | #1 |
| Bootstrap-remove rejected (no process / registry / JSONL change) | #2 |
| Unknown-UUID error (no process / registry / JSONL change) | #3 |
| Race-clean concurrent remove + list | #4 |
| SIGKILL fallback when child ignores SIGTERM (TestHelperProcess pattern) | #5 |

## Quality gates

- `go test -race ./...` clean (existing CI gate).
- `go vet ./...` clean.
- `staticcheck ./...` clean.
- No new dependencies.
- `qmd update && qmd embed` after the knowledge doc note lands (step
  below).

## Knowledge capture (during implementation)

Append to `docs/knowledge/features/sessions-package.md` a new
`§ Pool.Remove` subsection mirroring the `§ Pool.Rename` and
`§ Pool.Create` ones — the chosen ordering (delete-then-evict), the
trade-off note (lifecycle goroutine survives until pool shutdown), the
sentinel surface, and the "no SessionMu touched" lock note. No new ADR
— this design follows established patterns (`Pool.Rename` for the
mutator shape; `Pool.Create` for the persist/rollback discipline; #41
for the "outer mutex separation" rationale, here resolved by holding
`p.mu` only during the cheap mutate phase).

PROJECT-MEMORY.md gets a one-paragraph entry under "Codebase (Phase
1.1d-A1, ticket #94)" summarising the surface and the
delete-then-evict invariant.

## Open questions

None — design is fully specified. Edge cases above cover the
ctx-cancellation and concurrent-caller cases the implementer would
otherwise have to invent answers for.

## Out of scope (reaffirmed)

- JSONL on-disk disposition (`RemoveOptions`, `JSONLPolicy`, archive,
  purge) → 64-A2 / #95.
- Control verb / CLI surface → #65.
- UUID-prefix resolution → #65.
- Bulk remove / TTL-based remove.
- Per-session terminate signal to eliminate the orphan goroutine —
  defer until evidence.
