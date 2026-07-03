# Spec — Cancel a removed session's lifecycle goroutine on `Pool.Remove` (#775)

## Files to read first

- `internal/sessions/pool.go:613-643` — `Pool.Remove`: the stale lifecycle-after-Remove comment (`613-617`) and the delete→save→dispose→Evict body. **This is where the close-on-remove signal is fired, and the comment to rewrite.**
- `internal/sessions/session.go:269-324` — `Session.Run`: the active↔evicted loop. The `stateEvicted` branch gains the removal check.
- `internal/sessions/session.go:412-422` — `runEvicted`: the park on `{ctx, activateCh}`. Gains the `removedCh` select case.
- `internal/sessions/session.go:333-410` — `runActive`: read to confirm it is **left unchanged** (why: `Evict` already drives active→evicted deterministically; see Concurrency model).
- `internal/sessions/session.go:64-97` — `Session` struct: where the new `removedCh` field lands, next to `activateCh`/`evictCh`. Note the lcMu-guarded swap-channels vs. the new write-once channel distinction.
- `internal/sessions/session.go:230-257` — `Session.Evict`: the primitive `Remove` already calls; confirms evictedCh close is the unblock signal that must not be short-circuited.
- `internal/sessions/pool.go:423-436` — bootstrap `Session` literal (construction site #1): add `removedCh` init.
- `internal/sessions/pool.go:1056-1072` — `buildSession` `Session` literal (construction site #2): add `removedCh` init.
- `internal/sessions/pool.go:887-906` — `supervise` + `g.Go(sess.Run(gctx))`: confirms every `Run` executes under the pool's **shared** errgroup ctx (`gctx`). The removed `Run` MUST return `nil`, never `ctx.Err()`.
- `internal/sessions/session.go:281-295` — the non-fatal-persist warning block: the canonical statement of "returning an error here tears down every session + the relay leg." The removal path must respect the same rule.
- `internal/sessions/pool_remove_test.go:1-75` + `internal/sessions/pool_create_test.go:54-81` — `runPoolInBackground` helper and existing Remove-test scaffolding to reuse.
- `internal/sessions/session_test.go:149` — `pollUntil(t, timeout, fn)` helper (already in the test package) for the poll-until-settle in the leak test.
- `internal/relay/v2session_test.go:2091-2137` — reference goroutine-leak test shape (`before := runtime.NumGoroutine()` … `runtime.GC()` … `after`); and `internal/e2e/internal/fakerelay/fakerelay_test.go:334-379` — baseline + poll-until-`≤baseline+jitter` shape.

## Context

`Pool.Remove` deletes the session's map entry, persists, and calls `sess.Evict(ctx)` to terminate the child. But the session's lifecycle goroutine — the body of `Session.Run` — then loops into `runEvicted` and parks on `activateCh` / `ctx.Done()`. The session is no longer reachable via `Pool.sessions`, so nothing can ever signal `activateCh`, and `ctx` here is the pool's **shared** errgroup context (`gctx`), which cancels only at full pool shutdown. The goroutine and everything it captures (`*Session`, `*supervisor.Supervisor`, `*supervisor.Bridge`, logger) therefore leak for the daemon's lifetime — one orphan per create+remove cycle.

`#94` documented this as a bounded cost, acceptable for a handful of operator-driven removes. On a long-lived daemon that churns many create+remove cycles it is an unbounded leak. This ticket makes the removed goroutine exit promptly.

The one hazard is the errgroup: every `Run` executes as `g.Go(func() error { return sess.Run(gctx) })`. If a removed `Run` returns `context.Canceled`, the errgroup treats it as failure, cancels `gctx`, and tears down **every** other session plus the relay leg — the exact blast radius the non-fatal persist handling (`session.go:281-295`) exists to avoid. The fix must return `nil` on removal and reserve `ctx.Err()` for genuine pool shutdown.

## Design

Add a per-session **write-once broadcast channel** `removedCh`, closed by `Remove`, that `runEvicted` selects on and that `Run` treats as a clean (`nil`) exit.

### Why the channel seam, not a per-session `context.WithCancel(gctx)`

The ticket floats two seams. Choose the **close-on-remove channel**:

1. **No construction-ordering race.** A per-session cancel must be derived from `gctx`, which is only known when `Run(gctx)` is called — so the `CancelFunc` can't be stored at construction, and a `Remove` that lands before `Run`'s first line would read a nil cancel. `removedCh` is allocated at construction (always non-nil before `Run` is scheduled); a `Remove` that lands early still works because the closed channel is observed the instant `Run` reaches `runEvicted`.
2. **Deterministic shutdown-vs-removal discrimination.** `gctx.Done()` → return `ctx.Err()` (propagate, pool shutdown); `removedCh` closed → return `nil`. No "which context fired?" ambiguity that a shared-parent cancel introduces.
3. **Matches the ticket's race note.** A *closed* channel (level-triggered broadcast) survives the `runActive`→`runEvicted` transition window, where a one-shot send could be lost.

### Session struct (`session.go:64-97`)

Add one field next to the existing lifecycle channels:

```go
// removedCh is closed exactly once by Pool.Remove, after the registry-remove
// commits. A closed removedCh tells the lifecycle goroutine to exit its Run
// loop cleanly (return nil) instead of re-parking in runEvicted. Write-once:
// allocated at construction, never reallocated (unlike activeCh/evictedCh,
// which swap under lcMu). Readers select/read it WITHOUT lcMu; a nil channel
// is a valid "never removed" state (used by the non-removable bootstrap and
// by any test-constructed Session).
removedCh chan struct{}
```

- `nil` is a safe "never removed" value: a nil channel is a never-ready select case, and `isRemoved()`'s non-blocking read defaults to `false`. So test literals that hand-build a `Session` need no change.

### `runEvicted` (`session.go:412-422`)

Add one select case; unchanged otherwise:

```go
case <-s.removedCh:
    return nil // removal: Run's isRemoved() check turns this into a clean exit
```

`removedCh` closed and `activateCh`/`ctx` firing concurrently → Go picks one; either way `Run`'s subsequent `isRemoved()` check catches the removal, so **removal always wins over a racing Activate** (a removed session can never resurrect — a latent-bug improvement over today, where a racing Activate could re-park→re-activate a removed session).

### `Session.Run` — `stateEvicted` branch only (`session.go:308-321`)

Insert one guard before the re-activate transition:

```go
case stateEvicted:
    if err := s.runEvicted(ctx); err != nil {
        return err // ctx.Err() — genuine pool shutdown, propagate
    }
    if s.isRemoved() {
        return nil // removed: exit the lifecycle goroutine, no errgroup error
    }
    if err := s.transitionTo(stateActive); err != nil { /* existing non-fatal warn */ }
```

Add the helper:

```go
// isRemoved reports whether Pool.Remove has closed removedCh. Non-blocking;
// no lcMu (removedCh is write-once). Deterministic: removedCh never reopens.
func (s *Session) isRemoved() bool // select { case <-s.removedCh: true; default: false }
```

`runActive` and the `stateActive` branch are **left unchanged** — see Concurrency model for why.

### `Pool.Remove` (`pool.go:618-643`)

Close `removedCh` after the registry-remove commits (past the `saveLocked` rollback branch) and before `Evict`:

```go
disposeErr := p.disposeJSONLLocked(id, opts.JSONL)
p.mu.Unlock()

close(sess.removedCh) // signal the lifecycle goroutine to exit; see ordering note
evictErr := sess.Evict(ctx)
```

- **Must be past the rollback point.** On `saveLocked` failure `Remove` rolls back (`p.sessions[id] = sess`) and returns while still holding `p.mu`; that early return never reaches the close, so a rolled-back, still-registered session keeps its `Run` goroutine alive. Correct.
- **Off-lock, no lcMu.** Closing a write-once channel needs no lock; the `runEvicted` reader observes the close without holding lcMu. Placed next to the already-off-lock `Evict` call (Remove releases `p.mu` before `Evict` to avoid the `Session.transitionTo → Pool.persist → p.mu` re-acquire deadlock).
- **Single-close is structural.** `Remove` is single-shot per id: a second `Remove` for the same id finds no `p.sessions[id]` under `p.mu` and returns `ErrSessionNotFound` before the close. No `sync.Once` / guard needed (evidence-based: no observed double-close; the map-delete-under-`p.mu` guard already enforces it).

### Construction sites

Add `removedCh: make(chan struct{})` to both `Session` literals: bootstrap (`pool.go:423-436`) and `buildSession` (`pool.go:1056-1072`). The bootstrap's is never closed (`ErrCannotRemoveBootstrap`) — a harmless always-open channel; allocate it for uniformity.

### Comment rewrite (`pool.go:613-617`)

Replace the "survives until pool shutdown / bounded resource cost — see #94" paragraph with the new contract: the lifecycle goroutine exits promptly when `Remove` closes `removedCh` — `runEvicted` observes the close and `Run` returns `nil` (never through the shared errgroup). This is a **code-comment** edit (in-scope). The evergreen feature-doc note (`docs/knowledge/features/sessions-package.md:390`) is **not** a developer deliverable — documentation phase updates it post-merge.

## Concurrency model

The subtle invariant: **`removedCh` is watched only in `runEvicted`, never in `runActive`.**

`Remove` always calls `sess.Evict(ctx)`, which deterministically drives an active session to evicted: `Evict` sends `evictCh` → `runActive`'s `case <-s.evictCh` fires → `cancelSup` + `drainSup` (SIGKILL the child, join the supervisor goroutine) → returns `(ReasonEviction, nil)` → `Run` runs `transitionTo(stateEvicted)`, which **closes `evictedCh`** and thereby unblocks the `Evict` call inside `Remove`. Only then does `Run` loop into `runEvicted`, where the (already-closed) `removedCh` is observed and `Run` returns `nil`.

If `runActive` *also* selected on `removedCh`, a select race could pick `removedCh` and return **before** `transitionTo(stateEvicted)` closed `evictedCh` — leaving `Remove`'s `Evict` call blocked on `evictedCh` until the caller's ctx expires (a hang). Keeping `removedCh` out of `runActive` keeps the child-termination path untouched and lets the removal be caught one step later, in the park state.

Two removal shapes, both funnel through the new `runEvicted` case:
- **Remove an active session** (the churn case): `Evict` drives active→evicted (as above), `Run` loops into `runEvicted` → `removedCh` closed → exit.
- **Remove an already-evicted session** (e.g. idle-evicted first): `Run` is already parked in `runEvicted`; `close(removedCh)` wakes the select directly. `Evict` on an already-evicted session sends no `evictCh` and returns immediately (its `evictedCh` is already closed in `stateEvicted`). No hang.

Lock discipline unchanged: `removedCh` is write-once and touched without lcMu, so it introduces no new lock ordering. The existing `Pool.mu → Session.lcMu` and `Pool.capMu → Pool.mu → Session.lcMu` orders are untouched.

## Error handling

- Removed `Run` returns **`nil`** — never `context.Canceled` — so the shared errgroup does not cancel `gctx` or tear down siblings/relay (AC-2). `ctx.Err()` is still returned only from the genuine `case <-ctx.Done()` paths (pool shutdown).
- `saveLocked` failure inside `Remove` rolls back and returns before the close — the rolled-back session's goroutine keeps running (correct: it is still registered).
- `Evict` error (ctx cancelled mid-termination) is returned by `Remove` as today; the close already happened, so the goroutine still exits once it reaches `runEvicted`. Ordering of close-before-Evict is correctness-safe because the close is level-triggered.

## Testing strategy

Add to `internal/sessions/pool_remove_test.go` (same-package, stdlib only).

**Primary (AC-3) — goroutine-count returns to baseline across create+remove churn:**
- **Not** `t.Parallel()` — `runtime.NumGoroutine()` is process-global.
- Build a pool with a real child (reuse `helperPoolCreate` + `/bin/sleep`-style child as existing Remove tests do), `runPoolInBackground`, wait for bootstrap active.
- Warm up with one create+remove cycle, then `runtime.GC()` + settle and record `baseline := runtime.NumGoroutine()` (first cycle allocates steady-state goroutines; measure baseline after it).
- Loop N (≈20) cycles: `Create` → `pollUntil` child spawned + `LifecycleState()==stateActive` → `Remove` → `pollUntil` `Lookup` returns `ErrSessionNotFound`.
- After the loop, poll-until-settle: `runtime.GC()`/`runtime.Gosched()` then `pollUntil` `NumGoroutine() <= baseline + jitter` (jitter ≈ 2–3; child SIGKILL + `Wait` + goroutine teardown is async, so a fixed sleep is flaky — poll). Assert no linear growth (final count must **not** be ≈ `baseline + N`). Reference shapes: `v2session_test.go:2091-2137`, `fakerelay_test.go:334-379`.

**Targeted (AC-1) — the parked-then-removed path:** Remove a session that is currently **evicted** and assert its goroutine exits. Simplest deterministic form: create → wait active → `Remove` while active (drives evicted then removed) and assert the removed goroutine no longer contributes to the count; optionally a variant that idle-evicts first (non-zero `IdleTimeout`) to exercise the "closed `removedCh` wakes a parked `runEvicted`" branch directly. Keep idle-timeout variants generous/poll-based to avoid timing flake.

**AC-2 — no collateral teardown:** Create two sessions (plus bootstrap); `Remove` one; assert (a) the other session's child is still alive (`State().ChildPID > 0`) and `LifecycleState()==stateActive`, (b) the bootstrap is still alive, and (c) `pool.Run` has **not** returned (the background goroutine from `runPoolInBackground` is still blocked — no errgroup propagation). This is the direct assertion that the removed `Run` returned `nil`, not an error.

The existing `TestPool_Remove_HappyPath` and sibling Remove tests should continue to pass unchanged (child exits, registry entry gone, JSONL untouched).

## Open questions

- **Eviction notification on Remove (pre-existing, out of scope).** Removing an *active* session drives `runActive`→`ReasonEviction`→`transitionTo(stateEvicted)`→`notifyTransition(ReasonEviction)` — i.e. a `session.evicted` transition still fires for a session that is being *deleted*, not idle-evicted. This is **existing behavior today** (Remove already calls Evict); this fix preserves it deliberately (suppressing it race-free would require `runActive` to learn about `removedCh`, reintroducing the `Evict`-hang hazard, and changing the outbound event stream as a side effect of a leak fix). Whether a removed session should emit an id-stable eviction event (cf. eviction-is-id-stable/no-rebind semantics) is a separate question — file a follow-up if it proves wrong; do not change it here.
- **Redundant post-remove registry write (pre-existing).** The active→evicted `transitionTo` on the remove path calls `pool.persist()`, writing the registry once more (already without the removed session). Harmless and pre-existing; not addressed here.
