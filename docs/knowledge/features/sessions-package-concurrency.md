# Concurrency

`sync.RWMutex` on `Pool.sessions`:

- `Lookup` and `Default` take the read lock.
- `Run` takes the read lock once briefly to grab the bootstrap pointer and `claudeSessionsDir`.
- Writers: `RotateID` (1.2b-A), `RotateForNewSession` (#1125; re-key via the shared `rekeyLocked` + skip-set register + save, one `Pool.mu` critical section), `RotateBootstrapForSelfHeal` (#1165; same shared `rekeyLocked` + save, but deliberately NO skip-set register — see [ADR 033](../decisions/033-supervisor-self-heal-dedicated-rotation-no-skip-set.md)), `RegisterAllocatedUUID` / `IsAllocated` mutations (1.2b-B), `persist` (1.2c-A; called from `Session.transitionTo`). Phase 1.1's `Pool.Add(SessionConfig)` plugs in the same way.

`sync.Mutex` on each `Session.lcMu` (1.2c-A): protects `lcState`, `attached`, `activeCh`, `lastActiveAt`, and (as of #866) `id`. **Lock order: `Pool.mu → Session.lcMu`**. `Session.transitionTo` releases `lcMu` *before* calling `Pool.persist` so `saveLocked`'s per-session re-acquire can't deadlock.

**`id` is a two-lock field (#866).** `RotateID` writes `sess.id = newID` under **both** `Pool.mu` (W, via its function-level `defer`) and `lcMu` — the write sits inside the same brief `lcMu` section `RotateID` already opens for `lastActiveAt`, so no new lock and no lock-order change. A read is race-clean while holding *either* lock: `Pool.mu`-holders (`List`, `ResolveID`, `Snapshot`, `saveLocked`) read `sess.id` directly; lifecycle-goroutine readers, which hold neither `Pool.mu` nor (before the read) `lcMu`, go through the unexported `(*Session).currentID()` helper (`sess.lcMu.Lock/Unlock`, return `id`). `Session.ID()` also routes through `currentID()` for the same reason, though it has zero production callers today. This replaced a stale invariant ("`RotateID` mutates `id` without `lcMu`; today's only callers run before any lifecycle goroutine begins observing it") that broke once #839 wired `RotateID` into the live fsnotify `/clear` watcher — that watcher goroutine runs concurrently with the per-session lifecycle goroutines and fires on every `/clear`, so the two lifecycle-goroutine reads of `sess.id` (the eviction-transition notify and the idle-eviction warn log) were a live, if latent, data race. See [codebase/866.md](../codebase/866.md).

**Known residual gap (non-blocking, #866 code review).** `Pool.Activate`'s LRU-eviction path reads `sess.id`/`victim.id` as call arguments to `pickLRUVictim` while holding only `capMu` — `Pool.mu` is acquired *inside* `pickLRUVictim`, after Go has already evaluated those arguments, so this specific read is technically unguarded by either lock. It is pre-existing (unchanged by #866), and nil-impact today (the torn value is only used to exclude an already-known-inactive target from victim candidates), but it means "every `Pool.mu`-holder reads `id` race-clean" is not quite exception-free. Tracked as a follow-up, not yet filed as its own ticket.

Goroutines introduced in this layer (1.2c-A):

1. **Per-Session lifecycle goroutine** — body of `Session.Run`, owns the `active ↔ evicted` state machine and idle timer.
2. **Per-active-period supervisor goroutine** — wraps `s.sup.Run(subCtx)` and pipes the result to `runErr`.
3. **Per-attach detach-watcher** — decrements `attached` when the bridge's done channel fires.

The PTY spawn / wait / backoff loop, the I/O bridge goroutines, and the SIGWINCH watcher all remain in their existing packages.
