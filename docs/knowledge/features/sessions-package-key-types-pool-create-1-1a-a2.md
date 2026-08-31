# Pool.Create (1.1a-A2)

The user-facing primitive that ties together every existing seam — `NewID`, `saveLocked`, `RegisterAllocatedUUID`, `supervise`, `Activate` — to mint a fresh non-bootstrap session. One well-tested entry point so downstream callers (Phase 1.1a-B's `sessions.new` verb, future channel-driven auto-mint) don't re-derive the sequence.

```go
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error)
```

**Two unexported fields support it,** captured at `New()` time and read-only after:

- `sessionTpl SessionConfig` — shallow copy of `cfg.Bootstrap`. `Create` clones `ClaudeArgs`, appends `--session-id <uuid>`, sets `ResumeLast = false`, and (if `tpl.Bridge != nil`) mints a fresh `*supervisor.Bridge`. The bridge presence is the service-mode signal — sharing one bridge across N sessions would multiplex their I/O into a single client view.
- `idleTimeoutDefault time.Duration` — mirrors `Config.IdleTimeout`. `Create` applies the same fallback `New()` applies to the bootstrap (per-session zero → pool default).

**Sequence (in order — each step depends on the previous):**

1. `NewID()` — fresh UUIDv4
2. Build per-session `SessionConfig`: clone `ClaudeArgs`, append `--session-id <uuid>`, `ResumeLast=false`, fresh `Bridge` in service mode
3. `supervisor.New(supCfg)` — wrap on `sessions: create supervisor: %w` failure (no state mutated yet)
4. Build `*Session` in `stateEvicted` (label verbatim — empty preserved as empty; `bootstrap=false`; `createdAt=lastActiveAt=now`)
5. **Persist phase under `p.mu` (write):** insert into `p.sessions[id]`, call `saveLocked()`. On save failure, `delete(p.sessions, id)` rollback under the same lock and return `("", err)`. Lock released.
6. `RegisterAllocatedUUID(id)` — primes the rotation watcher's skip-set. Must fire before claude opens the JSONL or the CREATE looks like a `/clear` rotation. The 30s TTL is well clear of the sub-second spawn path.
7. `supervise(sess)` — schedules `sess.Run(gctx)` on the live errgroup. On `ErrPoolNotRunning`, return `(id, err)` — entry is on disk, no lifecycle goroutine.
8. `Activate(ctx, id)` — cap-aware. The new session is in `stateEvicted` so it doesn't count toward `active` for the cap pre-flight; `pickLRUVictim` excludes the target. On Activate failure, return `(id, err)`.

**Why persist *before* activate.** A save failure with claude already running leaves an unsupervised orphan: claude has opened its JSONL, started a conversation, and pyry has no on-disk record. The next pyry start won't reconcile it; the JSONL becomes a ghost. A registry-only entry that didn't activate is benign — same shape as a session that ran, idled out, and is now reattachable. A subsequent attach goes through `Pool.Activate` (the same primitive used here) and brings it up. See [lessons.md § Lock-order pitfalls when a callee persists](../../lessons.md#lock-order-pitfalls-when-a-callee-persists) for the lock-order discipline.

**Why register-allocated *after* persist, *before* activate.** `RegisterAllocatedUUID` has a 30s TTL window. It must fire before claude opens the JSONL (so the watcher's skip-set has the UUID when the CREATE fsnotify event lands). Doing it after persist (rather than before) makes the order robust to a slow registry write — TTL countdown starts from a known-recent moment.

**Why supervise *before* Activate.** `Session.Activate` sends on `activateCh` (buffered 1) then waits on `activeCh` until the lifecycle goroutine flips to active. If `sess.Run` isn't running yet, the buffered signal is held and `Activate` blocks until ctx cancels. So `supervise` (which schedules `sess.Run`) must precede `Activate`. If `supervise` returns `ErrPoolNotRunning`, bail before calling `Activate` — no goroutine to wake.

**Cap pre-flight via `Pool.Activate` directly (choice (a)).** Two viable shapes were considered: (a) call `Pool.Activate(ctx, id)` and let its existing cap path evict, or (b) run a cap pre-flight before registering. Choice (a): the new session IS in the pool and IS in `stateEvicted` — same shape as any other evicted session being reactivated. No duplicated cap logic, no new code path through `pickLRUVictim`, no transient inconsistent view from `Snapshot`/`Lookup` (which (b) would create).

**Failure-mode discriminator: id-or-empty.** The returned `SessionID` is the caller's signal:

| Failure point | Caller sees | On-disk state | In-memory state |
|---|---|---|---|
| `NewID` (rng) | wrapped err, `""` | unchanged | unchanged |
| `supervisor.New` | wrapped err, `""` | unchanged | unchanged |
| `saveLocked` | err verbatim, `""` | unchanged | rolled back |
| `supervise` | `ErrPoolNotRunning`, valid id | entry persisted | entry in map; no lifecycle goroutine |
| `Pool.Activate` | err verbatim (often `ctx.Err`), valid id | entry persisted | entry in map; lifecycle goroutine running; lcState may race to active |

Empty id ⇒ "nothing persisted, nothing to clean up." Non-empty id ⇒ "entry on disk, decide what to do (retry Activate, accept the eventual lifecycle, leave it for next pyry start)." Use `errors.Is(err, ErrPoolNotRunning)` to distinguish the not-running case from an Activate failure.

**ctx cancellation race — mid-flight only (narrowed, #1805).** `Session.Activate` now fails fast on an already-cancelled `ctx`: an entry guard reads `ctx.Err()` before taking `lcMu`, so a `ctx` that is cancelled or expired *at the call* returns immediately with no `activateCh` signal sent — for that case "Activate error → claude not running" **is** a hard invariant. The race described below still holds for a cancellation that lands *after* the call is already past the guard: if the caller cancels `ctx` after `supervise` succeeded but before `Activate` returns, `sess.Activate` may have already sent the buffered signal on `activateCh`. The lifecycle goroutine respects the *pool's* run-context (the errgroup's `gctx`), not the caller's, so the session may still spin up to active even though `Create` returns `(id, ctx.Err)`. Tests should not depend on the invariant for a `ctx` that was live when `Activate` was called and cancelled during the wait — only for one already dead at the call.

**Lock order — unchanged.** `Create` introduces no new ordering edges:

| Step | Locks | Order |
|---|---|---|
| Register + persist | `Pool.mu` (write) → `Session.lcMu` (briefly inside `saveLocked`) | `Pool.mu → Session.lcMu` ✓ |
| RegisterAllocatedUUID | `Pool.mu` (write) | trivially ✓ |
| supervise | `Pool.mu` (RLock) | trivially ✓ |
| Activate (cap path) | `capMu → Pool.mu` (RLock in pickLRU) → `Session.lcMu` | `capMu → Pool.mu → Session.lcMu` ✓ |

Critically: `Create` does NOT hold `p.mu` across `supervise`, `Activate`, or `RegisterAllocatedUUID`. The lock is taken only for the register+persist couple, then released. Concurrent `Create` calls each mint their own UUID via `crypto/rand` and serialise on `Pool.mu` for the persist couple; cap-path serialisation continues through `capMu` if `activeCap > 0`.

**Bridge: fresh per session in service mode.** `tpl.Bridge != nil` ⇒ `supervisor.NewBridge(p.log)` for the new session; `tpl.Bridge == nil` (foreground mode) ⇒ `nil`. Foreground-mode `Create` is operationally odd (the new session's output goes to its JSONL but has no live client) — not gated against, since the control verb path will only call `Create` in service mode.

**No new public types or sentinels.** `Pool.Create` is the only new exported name. `ErrPoolNotRunning` (from #72) is the only sentinel `Create` propagates.
