# Pool.GetOrCreate (1.3b)

The take-or-create primitive Phase 1.3b's `pyry attach --create-if-missing <uuid>` consumes. SDK consumers (Claudian / `@anthropic-ai/claude-agent-sdk`) mint a UUIDv4 per chat upstream and pass it through; pyry must accept the SDK's id even when no session under that UUID is registered yet. Pairs `Pool.Lookup` and `Pool.Create` into one atomic call:

| Caller | API |
|---|---|
| Server-minted id (CLI `sessions new`) | `Pool.Create(ctx, label)` |
| Caller-supplied id (SDK `attach --create-if-missing`) | `Pool.GetOrCreate(ctx, id, label)` |

```go
var ErrInvalidSessionID = errors.New("sessions: invalid session id")

func (p *Pool) GetOrCreate(ctx context.Context, id SessionID, label string) (SessionID, error)
```

**Take-or-create over insert-or-error.** `GetOrCreate` returns the canonical `SessionID` whether the session was already registered or this call created it — same return contract for both paths. The handler treats both branches identically from the call site onward (`Lookup → Activate → Attach`). The insert-or-error alternative (`CreateWithID` returning `ErrIDInUse`) would force the handler to follow up with a `Lookup`, re-introducing a TOCTOU window that two SDK chats opening simultaneously could race through. See [ADR 014](../decisions/014-get-or-create-take-or-create.md).

**ValidID gate at the Pool boundary.** Empty / non-canonical-UUIDv4 ids return `ErrInvalidSessionID` before any Pool state is touched. The validator runs before the lock is taken, so concurrent calls with different ids contend only briefly through `p.mu`.

**Atomic registration — the load-bearing change.** Unlike `Pool.Create` (which releases `p.mu` between persist and supervise), `GetOrCreate` holds `p.mu` across **all five** of (since #1487 this whole sequence lives in the shared `materialise` core — see [§ `Pool.Revive`](#reviving-a-dropped-session-poolrevive-1487)):

1. Duplicate-id short-circuit (`if existing, ok := p.sessions[id]`).
2. Registry-map insert.
3. `saveLocked()` (registry persist; rolled back on failure via `delete(p.sessions, id)`).
4. `registerAllocatedUUIDLocked(id)` — primes the rotation watcher's skip-set so a concurrent watcher snapshot sees register + skip-set atomically.
5. `g.Go(func() error { return sess.Run(gctx) })` — schedules the lifecycle goroutine.

`g.Go` is non-blocking: the goroutine it spawns parks on `activateCh` / `runCtx.Done()` before doing any pool work. Holding `p.mu` across `g.Go` is therefore safe (no lock-order violations, no deadlock risk). `Activate(ctx)` happens **after** `p.mu` is released — `Activate` has its own (`capMu`, `lcMu`) discipline that would deadlock if held under `p.mu`.

**Why `g.Go` must run inside the critical section.** Without holding `p.mu` across the schedule, a concurrent `GetOrCreate(sameID)` caller could:

1. Acquire `p.mu`, see the registered entry under `if existing, ok := p.sessions[id]`, release `p.mu`, return id.
2. Call `sess.Activate(ctx)` — which sends on `activateCh` (buffered 1) and waits on `activeCh`.
3. Block 30s until ctx times out, because the winner's lifecycle goroutine has not been scheduled yet (and will never close `activeCh`).

The race detector cannot catch it; the failure mode is "long hangs at attach time on the loser." Holding `p.mu` across `g.Go` makes the schedule observable as part of the same critical section that registers the entry, so any same-id observer sees both atomically.

**Two concurrent same-id callers.** One wins the lock, registers, persists, schedules `sess.Run`, releases the lock, proceeds to `Activate`, returns id. The other acquires the lock, observes the now-registered session via the duplicate-id short-circuit, releases the lock, returns id (no error). Both then `Lookup → Activate → Attach`; `Activate` is idempotent (already active → LRU touch, no-op). Net result: exactly one registry entry, exactly one supervised child, exactly one rotation skip-set entry.

**Helper extraction shared with `Pool.Create`.** Two private helpers, both new in this ticket:

- `buildSession(id, label) (*Session, error)` — constructs the per-session supervisor + Session. Touches no Pool state. `Pool.Create` and `Pool.GetOrCreate` call it identically; the supervisor.Config + Session field shape lives in one place.
- `registerAllocatedUUIDLocked(id)` — the lock-held variant of `RegisterAllocatedUUID`. Caller MUST hold `p.mu` (write). The exported `RegisterAllocatedUUID` keeps its current "takes the lock" contract for `Pool.Create`'s caller.

`Pool.Create`'s body shrinks; behaviour is unchanged.

**Failure modes.**

| Failure point | Caller sees | On-disk state | In-memory state |
|---|---|---|---|
| `ValidID` rejects (empty / malformed) | `ErrInvalidSessionID`, `""` | unchanged | unchanged |
| `buildSession` (supervisor.New) | wrapped err, `""` | unchanged | unchanged |
| Take path (id already registered) | id, no error | unchanged | unchanged; caller's `label` silently dropped |
| `saveLocked` | err verbatim, `""` | unchanged | rolled back |
| `runGroup == nil` (Pool.Run not active) | `ErrPoolNotRunning`, `""` | rolled back (best-effort re-save) | rolled back |
| `Pool.Activate` | err verbatim, valid id | entry persisted | entry in map; lifecycle goroutine running |

The take-path's silent label drop is documented in the docstring. Today's only caller (`handleAttach`) passes `""`; if a future caller wants take-or-create-with-label-update, that's a separate primitive (`Rename` after `GetOrCreate`).

**Lock order — unchanged.** `Pool.mu (write) → Session.lcMu` (briefly inside `saveLocked`) → release → `Pool.Activate` (`capMu → Pool.mu (R) → Session.lcMu`). The `g.Go`-under-`p.mu` edge introduces no new ordering: `errgroup.Group.Go` takes its own internal mutex and the spawned goroutine's parking on `activateCh` does not touch `p.mu`.

**Tests** (in `internal/sessions/pool_get_or_create_test.go`): `TestValidID` (table — empty/short/long/wrong-dash/non-hex/v3/non-RFC-4122-variant + canonical-NewID-output); `TestPool_GetOrCreate_Take_ReturnsExisting` (existing label preserved on take); `TestPool_GetOrCreate_Create_Persists` (caller's id written verbatim, claude spawned); `TestPool_GetOrCreate_PersistsPostDetach` (AC #1 — registry survives evict; this test surfaced ticket #169's persist-ordering race against `-race`); `TestPool_GetOrCreate_InvalidID` (`errors.Is(err, ErrInvalidSessionID)` for empty/malformed/v3); `TestPool_GetOrCreate_PoolNotRunning` (`ErrPoolNotRunning`, registry rolled back); `TestPool_GetOrCreate_ConcurrentSameID` (AC #4 — N=8 goroutines racing on one id, exactly one registry entry, label is one of the inputs, `-race`-clean); `TestPool_GetOrCreate_HonorsCap` (cap=1; bootstrap evicted via `Pool.Activate`'s cap-aware path).
