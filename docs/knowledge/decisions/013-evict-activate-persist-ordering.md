# ADR 013: `Session.Evict` / `Session.Activate` wait for registry persist

**Status:** Accepted (2026-05-07, ticket [#169](https://github.com/pyrycode/pyrycode/issues/169))
**Phase:** 1.3b (race fix surfaced by [#155](https://github.com/pyrycode/pyrycode/issues/155))
**Refines:** [ADR 005](005-idle-eviction-state-machine.md) (idle eviction state machine), [ADR 006](006-concurrent-active-cap-lru.md) (concurrent active cap)

## Context

Through ADR 005, every state transition wrote the registry through `Pool.persist`, but `transitionTo` closed the per-direction wake channel **before** calling `pool.persist`:

```go
s.lcMu.Lock()
s.lcState = newState
close(s.evictedCh) // wakes Evict's <-ch — UNDER lcMu
s.lcMu.Unlock()
return s.pool.persist() // disk write runs AFTER lcMu releases
```

`Session.Evict` and `Session.Activate` blocked on the channel close, so they woke the moment the in-memory flip was observable but **before** the registry had been persisted. A caller that performed `sess.Evict(ctx); reg, _ := loadRegistry(regPath)` could read pre-eviction state from disk. Phase 1.3b's `TestPool_GetOrCreate_PersistsPostDetach` (PR #166) tripped this race and was `t.Skip("blocked on #169")`.

The naive fix — move `pool.persist()` inside `transitionTo`'s `lcMu` critical section — re-introduces the lock-order violation the comment at the old `session.go:347-349` explicitly avoided: `pool.persist` → `saveLocked` re-acquires every session's `lcMu` when building the registry snapshot, deadlocking against the holder.

The design space had three options (per the ticket):

1. **Move `pool.persist()` inside `transitionTo`'s `lcMu` critical section.** Deadlocks via `saveLocked`'s per-session `lcMu` re-acquire. Rejected.
2. **Have `Evict` / `Activate` wait for persist completion.** Close the wake channel *after* `pool.persist` returns; waiters block until disk and memory agree.
3. **Serialise transitions through a goroutine** so the channel close happens only after the persist has been observed.

## Decision

**Option 2: close the per-direction wake channel AFTER `pool.persist` returns. Drop the early-return in `Activate` / `Evict`; always wait on the channel.**

`transitionTo` is restructured into three phases:

1. **Flip + allocate (under `lcMu`).** Set `lcState`, bump `lastActiveAt`, allocate a fresh open channel for the **opposite** direction (the next transition's wake channel). The current direction's channel is left open here.
2. **Persist (lock released).** Call `s.pool.persist()` with `lcMu` released so `saveLocked`'s per-session re-acquire is unblocked. Same lock-ordering rule the old code obeyed.
3. **Close (under `lcMu` again).** Re-acquire `lcMu` and close the current-direction wake channel. The acquisitions are sequential, not nested.

The new channel-state contract:

- **`activeCh` is closed iff `lcState == stateActive` AND the persist for that transition has completed.**
- **`evictedCh` is closed iff `lcState == stateEvicted` AND the persist for that transition has completed.**

Cold-start initialisation in `Pool.New` and `Pool.Create` already encodes this: a session that warm-starts in `stateActive` initialises `activeCh = closedChan()` (no transition pending; disk already consistent) and `evictedCh = make(chan struct{})`. No initialisation change needed.

`Activate` / `Evict` lose their early-return on "already in target state" and always wait on the channel:

```go
func (s *Session) Activate(ctx context.Context) error {
    s.lcMu.Lock()
    ch := s.activeCh
    if s.lcState != stateActive {
        select { case s.activateCh <- struct{}{}: default: }
    }
    s.lcMu.Unlock()
    select {
    case <-ch:        return nil
    case <-ctx.Done(): return ctx.Err()
    }
}
```

When fully transitioned, the channel is already closed and the receive returns immediately — same wall-clock cost as the previous no-op short-circuit. Mid-transition (state flipped, persist still running), the receive correctly blocks until persist completes.

## Rationale

### Why option 2 over option 3 (goroutine-serialised transitions)

The lifecycle goroutine (`Session.Run`) is already the **sole** caller of `transitionTo`. Transitions are already serialised by construction — option 3 would add a second goroutine that does nothing the existing one doesn't. Option 2 is a local reorder of three statements within the existing transition path; option 3 is a new control-flow primitive. Per the project's "simplicity first" principle, option 2 wins.

### Why drop the "already in target state" early-return

The wake channel is the load-bearing signal that disk and memory now agree. An early-return that bypasses the channel re-introduces the same race in a different shape: a caller who short-circuits on `lcState == stateActive` may have observed the in-memory flip from a *prior* `transitionTo` whose persist is still in flight (the lifecycle goroutine has flipped state but not yet closed `activeCh`). Always waiting on the channel collapses these cases — when fully consistent, the receive returns immediately; when not, it blocks for the few microseconds remaining until persist returns.

### Why close the wake channel even on persist failure

A permanently-stuck waiter is a worse failure mode than a waiter that wakes to stale disk. The persist error propagates up `Run`, which already treats it as fatal (`return fmt.Errorf("persist evicted: %w", err)`) — the operator sees the failure via the errgroup's first-error propagation, and the lifecycle goroutine terminates. Holding waiters hostage to a fatal-anyway condition adds nothing.

### Why repurpose `activeCh` / `evictedCh` instead of adding a separate `persistedCh`

The existing channel pair already carries the right lifetime (one per direction, per transition cycle), and the `closedChan()` initialisation in `Pool.New` / `Pool.Create` already encodes the warm-start case correctly. Adding a separate channel doubles the bookkeeping for no semantic gain. The contract change is invisible at the channel-shape level — it's only the timing of the close that moves.

### Why the second `lcMu` acquisition is safe

The two `lcMu` acquisitions in `transitionTo` are **sequential, not nested**. Between them, `pool.persist` takes `Pool.mu` and `saveLocked` re-takes per-session `lcMu` briefly — but at that point the first `lcMu` has been released. The lock order `Pool.mu → Session.lcMu` documented at the persist seam is preserved. The second `lcMu` acquisition is taken only after `pool.persist` returns and `Pool.mu` is released; it exists solely to order the `close()` against any concurrent `Activate` / `Evict` capturing the channel reference. No new lock-order edge.

## Consequences

- **Disk and memory agree at every `Activate` / `Evict` return point.** The race the ticket describes is structurally impossible. Callers can do `sess.Evict(ctx); reg, _ := loadRegistry(...)` and trust the result.
- **`Pool.GetOrCreate`'s post-detach assertion (#155, PR #166) holds.** When that PR rebases on `main`, its `t.Skip("blocked on #169")` line is removed and the test passes deterministically under `go test -race -count=20 ./internal/sessions/...`.
- **`Pool.Remove` (#94/#95) gets a free correctness boost.** Its post-delete `sess.Evict(ctx)` now waits for the eviction persist before returning. Remove's own ordering guarantees were already separate (its own `saveLocked` for the in-memory delete), but the bonus consistency is welcome.
- **No production callers see a behavioural difference on the happy path.** When fully transitioned, `Activate` / `Evict` return immediately just as before. The change is only visible mid-transition, where the old code returned early to inconsistent state.
- **`Pool.Activate`'s cap path is unchanged.** It calls `sess.LifecycleState()` to short-circuit on already-active and `sess.touchLastActive()` — both pure in-memory. No disk read; no race; no fix needed at this caller.
- **No new types, exports, or shared-state additions.** The fix is local to three methods of `Session`. Race detector remains clean.

## Alternatives considered

- **Move `pool.persist()` inside `transitionTo`'s `lcMu`** — Deadlocks via `saveLocked`. Rejected, documented at the call site.
- **Serialise transitions through a dedicated goroutine** — Overkill; the lifecycle goroutine already serialises.
- **Add a separate `persistedCh` per transition** — Doubles the channel bookkeeping with no semantic gain over repurposing the existing wake channels.
- **Keep the early-return; gate it on a separate "persist completed for this transition" generation counter** — Functionally equivalent to option 2 but with a hand-rolled signalling primitive instead of channel close. The channel already exists; reuse it.

## References

- Ticket: [#169](https://github.com/pyrycode/pyrycode/issues/169)
- Spec: [`docs/specs/architecture/169-evict-activate-persist-ordering.md`](../../specs/architecture/169-evict-activate-persist-ordering.md)
- Code: `internal/sessions/session.go` (`transitionTo`, `Activate`, `Evict`), `internal/sessions/session_persist_test.go` (regression + stress)
- Surfaced by: [#155](https://github.com/pyrycode/pyrycode/issues/155) `TestPool_GetOrCreate_PersistsPostDetach`
- Related ADRs: [005](005-idle-eviction-state-machine.md), [006](006-concurrent-active-cap-lru.md)
