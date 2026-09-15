# Pool.Remove (1.1d-A1)

The typed delete primitive the future `pyry sessions rm` CLI verb (#65) calls instead of touching processes or `sessions.json` directly. One method, one new exported sentinel (`ErrCannotRemoveBootstrap`), no other type additions.

```go
var ErrCannotRemoveBootstrap = errors.New("sessions: cannot remove bootstrap session")
func (p *Pool) Remove(ctx context.Context, id SessionID) error
```

**Sequence — delete-then-evict.**

1. Take `Pool.mu` (write).
2. Resolve `id` in `p.sessions`. Unknown ⇒ release `Pool.mu`, return `ErrSessionNotFound` (in-memory + on-disk state byte-identical, no `saveLocked` call).
3. If `sess.bootstrap` ⇒ release `Pool.mu`, return `ErrCannotRemoveBootstrap` (bytes-identical, same as above).
4. `delete(p.sessions, id)`, then `delete(p.dormant, id)` (#2448), then `saveLocked()`. On save failure: restore `p.sessions[id] = sess`, release the lock, return the error verbatim. (Mirrors `Pool.Rename`'s rollback discipline.) The `p.dormant` delete has nothing to roll back: `id` was live, so `materialise` already retired any dormant entry for it when the session was registered, and `saveLocked` writes a live id from its `Session` either way — restoring `p.sessions[id]` alone restores the file byte-for-byte. The delete is here anyway because removal's finality is `Remove`'s own claim to make: without it, a future change to where the retire happens could resurrect a removed session on the next save.
5. Release `Pool.mu`.
6. Call `sess.Evict(ctx)`. Returns only after the child has exited (or `ctx` cancels). The on-disk JSONL is **not** touched — disposition (archive / purge) is 64-A2 / #95.

**Why delete-then-evict (not evict-then-delete).** Holding `Pool.mu` across `Session.Evict` deadlocks: the lifecycle goroutine's `transitionTo` calls `Pool.persist`, which reacquires `Pool.mu` (write). The cap-policy path (#41) hits the same constraint and uses `capMu` as the outer mutex; here the simpler resolution is to release `Pool.mu` after the in-memory delete commits — concurrent `Lookup` / `Activate` / `Rename` / `List` callers see the session as gone from that moment on, so there's no half-removed state for any observer to witness, and no risk of a re-spawn race against a session that's about to die.

**Why `ctx` (not the AC's bare `id` shape).** `Session.Evict` already accepts a context; passing one through keeps the (potentially long-lived) termination interruptible and matches `Pool.Activate(ctx, id)` / `Pool.Create(ctx, label)`.

**Bootstrap rejection is structural, not policy.** The bootstrap is the per-process invariant `Pool.Lookup("")` resolves to. Removing it would leave the pool in a state no caller can satisfy without an explicit re-bootstrap pass. The sentinel surface lets the CLI distinguish "operator targeted bootstrap" from "id not found" without string-matching error text.

**Termination reuse.** `Pool.Remove` does not re-implement SIGTERM/SIGKILL/grace logic. `Session.Evict` already drives the supervisor's child via `exec.CommandContext` cancel ⇒ SIGKILL ⇒ `cmd.Wait` returns. The supervisor today does not have a SIGTERM grace window — earlier docs that referenced one describe an aspiration, not the current behaviour. SIGKILL is uncatchable, so no fallback path is needed.

**Already-evicted sessions are a fast path.** If `sess` is already in `stateEvicted` (prior idle eviction, prior cap-policy eviction, or warm-started in evicted), `Session.Evict` is an immediate no-op — no second persist runs, the registry write in step 4 is the only mutation.

**Lifecycle goroutine after Remove (#775).** `Pool.Remove` closes the session's write-once `removedCh` — after the registry-remove commits (past the `saveLocked` rollback branch) and off `Pool.mu`, next to the already-off-lock `Evict` call. `Session.Evict` then drives an active session `active → evicted`; once `sess.Run`'s loop reaches `runEvicted` it observes the closed `removedCh` and returns **`nil`** — a clean exit, never `context.Canceled`, so the pool's **shared** errgroup (`gctx`) does not cancel and tear down any sibling session or the relay leg. The goroutine and everything it captures (`*Session`, `*supervisor.Supervisor`, `*supervisor.Bridge`, logger) are released at `Remove` time rather than surviving until pool shutdown — the #94 "bounded resource cost / one orphan per remove" note **no longer applies**. `removedCh` is watched **only** in `runEvicted`, never `runActive`: a select race there could return before `transitionTo(stateEvicted)` closes `evictedCh` and hang `Remove`'s `Evict` call. `Run`'s post-`runEvicted` `isRemoved()` re-check makes removal win over a racing `Activate`, so a removed session can never resurrect. The non-removable bootstrap allocates a `removedCh` that is never closed (`ErrCannotRemoveBootstrap`). See [codebase/775.md](../codebase/775.md).

**No `Session.lcMu` taken.** `Pool.Remove` does not read `lcState` / `lastActiveAt` / `attached`; the lifecycle goroutine continues to take `lcMu` inside `transitionTo` exactly as before. Lock-order graph (`Pool.capMu → Pool.mu → Session.lcMu`) is unchanged.

**Strict full-`SessionID` only.** Same posture as `Pool.Rename`. Empty-id is *not* the bootstrap shorthand it is in `Pool.Lookup` — empty falls through to the "not in map" branch and returns `ErrSessionNotFound`. Destructive operations require an explicit id.
