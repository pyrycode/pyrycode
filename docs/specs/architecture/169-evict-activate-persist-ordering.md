# Spec: Session.Evict / Session.Activate must wait for registry persist

**Ticket:** [#169](https://github.com/pyrycode/pyrycode/issues/169) — bug(sessions): Session.Evict/Activate returns before pool.persist completes (race surfaced by #155)
**Size:** S
**Files:** `internal/sessions/session.go` (production), one new test file in `internal/sessions/` (regression).

## Files to read first

- `internal/sessions/session.go:65-98` — `Session` struct fields, in particular `lcMu`, `lcState`, `activeCh`, `evictedCh`, `activateCh`, `evictCh`. The fix only manipulates these.
- `internal/sessions/session.go:175-228` — current `Activate` / `Evict` bodies. These get rewritten.
- `internal/sessions/session.go:346-374` — current `transitionTo` body, including the lock-order comment. This gets rewritten.
- `internal/sessions/session.go:281-344` — `runActive` / `runEvicted`. Read for context: the lifecycle goroutine signals are unaffected, but the design relies on the loop being the *sole* caller of `transitionTo`.
- `internal/sessions/pool.go:316-325` — bootstrap channel initialisation in `New` (`activeCh`/`evictedCh` start as `closedChan()` for the matching state). The fix preserves this exactly.
- `internal/sessions/pool.go:849-854` — same channel initialisation in `Create` (sessions are created in `stateEvicted` with `evictedCh = closedChan()`, `activeCh = make`).
- `internal/sessions/pool.go:963-1002` — `saveLocked` and `persist`. `saveLocked` reads each session's `lcState` / `lastActiveAt` under that session's `lcMu`. The lock-order edge `Pool.mu (held by caller) → Session.lcMu` lives here.
- `internal/sessions/session_test.go:155-310` — existing lifecycle tests (`TestSession_IdleEvictionFires`, `TestSession_ActivateRespawns`, `TestSession_ActivateNoOpWhenActive`, `TestSession_ActivateCtxCancellation`). The regression test follows their shape (uses `helperPoolPersistent` + `runPoolInBackground` from `pool_create_test.go:59-83`, asserts via `loadRegistry`).
- `internal/sessions/pool_create_test.go:59-83` — `runPoolInBackground` helper used by the regression test.
- `internal/sessions/session_test.go:143` — `pollUntil` helper.
- `docs/lessons.md` — scan for entries on lifecycle / lcMu / persist; the current code's lock-order rule is documented at `session.go:347-349` and must continue to hold.

## Context

`internal/sessions/session.go:transitionTo` flips `lcState` and closes the per-direction "transition complete" channel (`evictedCh` on evict, `activeCh` on activate) under `lcMu`, then **releases `lcMu` and calls `s.pool.persist()` after the close**:

```go
s.lcMu.Lock()
s.lcState = newState
// ... swap channels ...
close(s.evictedCh) // wakes Evict's <-ch
s.lcMu.Unlock()
return s.pool.persist() // disk write happens AFTER waiters return
```

`Evict` and `Activate` block on the channel close, so they wake the moment the in-memory flip is observable but *before* the registry has been persisted. A caller that performs `sess.Evict(ctx); reg, _ := loadRegistry(regPath)` can read pre-eviction state from disk. Phase 1.3b's `TestPool_GetOrCreate_PersistsPostDetach` (`feature/155` PR #166, `internal/sessions/pool_get_or_create_test.go:175`) trips this race and is currently `t.Skip`'d on `#169`.

The lock-order constraint at `session.go:347-349` is real: `pool.persist` → `saveLocked` re-acquires every session's `lcMu`, so `transitionTo` must release `lcMu` before calling `persist`. The fix has to keep that constraint while also withholding the channel close until persist returns.

## Design

**Approach (option 2 from the ticket): close the per-direction channel AFTER `pool.persist` returns. Make `Activate`/`Evict` always wait on the channel — even on the "already in target state" early-return path — because the channel close is the load-bearing signal that disk and memory now agree.**

This rejects approach (1) (move persist inside `lcMu`) for the documented deadlock, and rejects approach (3) (serialise transitions through a goroutine) as overkill — the lifecycle goroutine is already the sole caller of `transitionTo`, so transitions are already serialised; we just need to delay the wake-up.

### New channel-state contract

`activeCh` and `evictedCh` keep their current shape but acquire a tighter invariant:

- **`activeCh` is closed iff `lcState == stateActive` AND the persist for that transition has completed.**
- **`evictedCh` is closed iff `lcState == stateEvicted` AND the persist for that transition has completed.**

This matches the existing cold-start initialisation in `Pool.New` (`session.go:319-325`) and `Pool.Create` (`pool.go:849-854`): a session that warm-starts in `stateActive` initialises `activeCh = closedChan()` (no transition pending, disk already consistent) and `evictedCh = make(chan struct{})` (open; will be closed when an evict transition is fully persisted). Mirror for `stateEvicted`. No initialisation change needed.

### Rewritten `transitionTo`

```go
func (s *Session) transitionTo(newState lifecycleState) error {
    s.lcMu.Lock()
    s.lcState = newState
    s.lastActiveAt = time.Now().UTC()
    // Allocate the channel for the *next* transition in the opposite
    // direction. The current direction's channel is left open here and
    // closed only after persist completes (below).
    switch newState {
    case stateActive:
        s.evictedCh = make(chan struct{})
    case stateEvicted:
        s.activeCh = make(chan struct{})
    }
    s.lcMu.Unlock()

    var persistErr error
    if s.pool != nil {
        persistErr = s.pool.persist()
    }

    // Wake waiters. Done under lcMu so the close is ordered against any
    // concurrent capture of the channel reference in Activate/Evict.
    s.lcMu.Lock()
    switch newState {
    case stateActive:
        close(s.activeCh)
    case stateEvicted:
        close(s.evictedCh)
    }
    s.lcMu.Unlock()

    return persistErr
}
```

Key points:

- The channel for the *next* transition (opposite direction) is allocated in the first critical section, exactly as today — its only readers are future `Activate`/`Evict` callers that arrive after the state flip, who must see a fresh open channel to wait on.
- The "current direction" channel close is moved to the second critical section, after `persist`. The state flip and the wake are now separated by the disk write.
- The `lcMu` re-acquire after persist follows the same lock-order rule the current code already obeys: `lcMu` is released before `pool.mu` is taken via `persist`; we only re-take `lcMu` *after* persist has returned and released `pool.mu`. No new lock-order edge.
- The persist error is returned even on a non-nil result. If persist fails, the channel still closes — a stuck-forever waiter is worse than a waiter that wakes to a session whose disk state is stale. The error propagates up `Run`, which returns it; the operator sees a fatal log. (This matches today's behaviour: `Run` already returns `fmt.Errorf("persist evicted: %w", err)` on transition failure — see `session.go:251-260`.)

### Rewritten `Activate`

```go
func (s *Session) Activate(ctx context.Context) error {
    s.lcMu.Lock()
    ch := s.activeCh
    if s.lcState != stateActive {
        select {
        case s.activateCh <- struct{}{}:
        default:
        }
    }
    s.lcMu.Unlock()

    select {
    case <-ch:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

`Evict` mirrors this against `evictedCh` / `evictCh` / `stateEvicted`.

The semantic change: the early-return "already in target state" branch is *gone*. Callers always wait on the channel. When the session is fully active and persisted (`activeCh` already closed), `<-ch` returns immediately — same behaviour as today's no-op short-circuit, with the same wall-clock cost (channel receive on a closed channel ≈ noop). When the session is mid-transition (state flipped, persist in flight), the caller correctly blocks until persist completes. Idempotent under concurrent calls: `activateCh` is buffered(1) so duplicate wake signals collapse, exactly as today.

Lifecycle invariant after the change: **after `Activate(ctx)` returns nil, both `lcState == stateActive` and the matching registry entry have been written to disk for the most recent transition into stateActive.** Same for `Evict`. The race the ticket describes is structurally impossible.

### What does NOT change

- `runActive` / `runEvicted` are untouched — they still return on `evictCh` / `activateCh` / ctx, and the `Run` loop still calls `transitionTo` after each one.
- `Pool.persist` / `saveLocked` are untouched. Lock order `Pool.mu → Session.lcMu` is preserved.
- `Pool.New` and `Pool.Create` channel initialisation are untouched — the contract above already matches what they install.
- `touchLastActive`, `Resize`, `Attach`, `LifecycleState`, `snapshotState` are untouched.
- `Pool.Activate`'s cap path (`pool.go:1017-1042`) is untouched. It calls `sess.LifecycleState()` to short-circuit on already-active, then `sess.touchLastActive()`. Both are pure in-memory operations; no disk read happens here, so no race exists at this caller. The fix is local to `Session`.
- `Pool.Remove` (`pool.go:496-521`) is untouched. It already evicts after persisting its own delete + saveLocked; the post-delete `sess.Evict(ctx)` now correctly waits for the *eviction* persist as a bonus, but Remove's own ordering guarantees were already separate.

## Concurrency model

- **One `Run` goroutine per session, sole caller of `transitionTo`.** Transitions are serialised by construction. The fix relies on this — no two transitions can interleave their persist windows.
- **Lock order:** `Pool.capMu → Pool.mu → Session.lcMu`, unchanged. `transitionTo` takes `lcMu` (briefly), releases, takes `Pool.mu` (via `persist`) which in turn takes each `Session.lcMu` briefly inside `saveLocked`, then releases `Pool.mu`, then re-takes its own `lcMu` briefly. No new edge; the documented rule at `session.go:347-349` (release `lcMu` before calling `pool.persist`) still holds — the fix only adds a second, *post-persist*, lcMu acquisition that respects the same rule.
- **No deadlock:** Approach (1) deadlocked because holding `lcMu` while calling `persist` would re-enter `lcMu` via `saveLocked`. The fix releases `lcMu` *before* `persist` (same as today) and only re-acquires `lcMu` after `persist` has returned (and released `Pool.mu`). The two `lcMu` acquisitions are sequential, not nested.
- **Concurrent `Evict`/`Activate` callers:** all capture the same channel reference under `lcMu` and all wake on its close. `evictCh` / `activateCh` are buffered(1), so duplicate wake signals to the lifecycle goroutine collapse — same as today.

## Error handling

- `transitionTo` returns the persist error verbatim, but always closes the wake channel first. Rationale: a wake-stuck waiter is a worse failure mode than a stale-disk wake. `Run` already treats persist failure as fatal (`return fmt.Errorf("persist evicted: %w", err)`), terminating the lifecycle goroutine — operator sees the failure via the errgroup's first-error propagation.
- No new failure modes for `Activate` / `Evict`. They still return `nil` on successful transition or `ctx.Err()` on cancellation. Callers that previously got `nil` and read stale disk now get `nil` and read consistent disk.

## Testing strategy

### Regression test (deterministic, race-detector-clean)

Add a new test in `internal/sessions/session_test.go` (or a new `session_persist_test.go` if the file is getting long — match conventions of the package) that proves the ordering invariant:

```go
// After Session.Evict returns, loadRegistry must show the post-evict state.
// Asserts the persist completes before Evict's wake.
func TestSession_EvictBlocksUntilPersisted(t *testing.T) {
    t.Parallel()
    dir := t.TempDir()
    regPath := filepath.Join(dir, "sessions.json")
    pool := helperPoolPersistent(t, regPath)
    ctx, _ := runPoolInBackground(t, pool)

    sess := pool.Default()
    if !pollUntil(t, 2*time.Second, func() bool {
        return sess.LifecycleState() == stateActive
    }) {
        t.Fatal("session never reached stateActive")
    }

    if err := sess.Evict(ctx); err != nil {
        t.Fatalf("Evict: %v", err)
    }

    // Immediate disk read — no poll. The fix's contract is "Evict returns
    // only after persist". A poll here would mask the race the fix exists
    // to prevent.
    reg, err := loadRegistry(regPath)
    if err != nil {
        t.Fatalf("loadRegistry: %v", err)
    }
    var found bool
    for _, e := range reg.Sessions {
        if e.ID == sess.ID() {
            found = true
            if e.LifecycleState != "evicted" {
                t.Errorf("on-disk lifecycleState = %q, want %q", e.LifecycleState, "evicted")
            }
        }
    }
    if !found {
        t.Errorf("entry %q missing from registry", sess.ID())
    }
}
```

Add a symmetric `TestSession_ActivateBlocksUntilPersisted` that warm-starts a session in `stateEvicted` (write the registry first, then construct the pool — see existing warm-reload tests around `pool_test.go:230-280` for the pattern), calls `Activate`, and asserts the on-disk `lifecycleState` is empty / "active" immediately after `Activate` returns. (`stateActive` is the default-omitempty case at `pool.go:986-988`, so the assertion is `e.LifecycleState == ""`.)

**Note: must use `helperPoolPersistent`** (whose bootstrap is `/bin/sleep`, never spawned) and **NOT** the cap-test helpers — `Run` spawns the supervisor on the bootstrap, and the regression test only needs the lifecycle / persist plumbing, not a real claude. The existing `TestSession_IdleEvictionFires` (`session_test.go:157`) is a precedent for "Run a real-ish bootstrap, then assert lifecycle state".

### Stress test (per AC#3)

Either:

- Tag the regression test with the `-count=20` invocation in CI (architect prefers this), OR
- Add a stress wrapper that loops 20 evict↔activate transitions and asserts disk consistency after each — this is the more honest test of the fix and is bounded enough to live alongside other tests. ~30 iterations under `-race` runs in well under a second.

The AC explicitly demands `go test -race -count=20 ./internal/sessions/...`. The regression test should pass deterministically under that invocation; if it doesn't, the fix is incomplete.

### Un-skip TestPool_GetOrCreate_PersistsPostDetach

The test lives on `feature/155` PR #166 in `internal/sessions/pool_get_or_create_test.go:175`. It is `t.Skip`'d on `#169`. **The file does not exist on `main` or on `feature/169`'s base.** Two scenarios:

1. **#155 merges before this fix.** When the developer rebases `feature/169` on the new `main`, `pool_get_or_create_test.go` exists with the skip. Remove the `t.Skip` line (line 176 in the linked file) and verify `go test -race -count=20 ./internal/sessions/...` passes.
2. **This fix merges first.** No action here. When PR #166 rebases on the new `main`, its author removes the skip in the rebase commit. The architect's regression test above already covers the same property; #166's test is a redundant-but-useful integration check.

Document scenario (2) in the PR description if it applies, so #166 reviewers know to drop the skip on rebase.

### Existing tests that must continue to pass

- `TestSession_IdleEvictionFires` (`session_test.go:157`) — idle timer drives `Run` into `transitionTo(stateEvicted)`. The poll on `LifecycleState() == stateEvicted` still works (the in-memory flip happens *before* persist; the test polls until the flip is observable, which is unchanged).
- `TestSession_ActivateRespawns` (`session_test.go:246`) — after `Evict`, calls `Activate`. Both still return after their respective persists; supervisor re-enters `PhaseRunning`.
- `TestSession_ActivateNoOpWhenActive` (`session_test.go:278`) — calls `Activate` on an active session. Fix changes the implementation (no early return), but the observable behaviour is identical: `<-activeCh` on a closed `activeCh` returns immediately.
- `TestSession_ActivateCtxCancellation` (`session_test.go:299`) — pre-cancelled ctx still wins the select.
- All cap-policy tests in `pool_cap_test.go`. The cap path's `sess.LifecycleState()` short-circuit and `touchLastActive()` are pure in-memory operations; no behaviour change.

## Open questions

None blocking. Two notes for the developer:

1. **Should the `lcMu.Lock(); close(...); lcMu.Unlock()` in the post-persist phase coalesce with anything?** No — `transitionTo` is the only writer and the close is single-shot per direction. The lock there exists only to serialise against any `Activate`/`Evict` capturing the channel reference; it's a pure memory-ordering nicety. Don't try to elide it.
2. **Why not a single `persistedCh` per transition instead of repurposing `activeCh` / `evictedCh`?** Because the existing channel pair already carries the right lifetime (one per direction, per transition cycle), and the `closedChan()` initialisation in `Pool.New` / `Pool.Create` already encodes the warm-start case correctly. Adding a separate channel doubles the bookkeeping for no semantic gain.

## Acceptance criteria mapping

- AC1 (post-`Evict` disk consistency): satisfied by the `transitionTo` reorder — `evictedCh` closes only after `persist` returns.
- AC2 (post-`Activate` disk consistency): symmetric — `activeCh` closes only after `persist` returns.
- AC3 (un-skip + `-race -count=20` passes): handled per the rebase rules above; the new architect-supplied regression test passes deterministically under the same invocation.
- AC4 (no lock-order violations): the rule at `session.go:347-349` still holds — `lcMu` is released before `pool.persist`, and the second `lcMu` acquisition is strictly after `persist` returns. No new lock-order edges.
- AC5 (`go test -race ./internal/sessions/...` and `go vet ./...` clean): no new types, no new exports, no shared-state additions; race detector's job is to catch any mistake in the implementation, not the design.
