# Idle Eviction + Lazy Respawn + Concurrent Active Cap

Per-session lifecycle that evicts claude processes (~zero RAM) and respawns them on demand. Two independent eviction triggers share one mechanism:

- **Idle eviction (1.2c-A):** per-session timer evicts after `IdleTimeout` of inactivity.
- **Concurrent active cap (1.2c-B):** at the spawn-path entry, if activating one more session would exceed `ActiveCap`, the LRU active peer is evicted first.

Both end at `Session.transitionTo(stateEvicted)` — same on-disk state change, same registry write, same broadcast. They differ only in *who picks the victim*: the idle timer picks itself; the cap policy picks the LRU peer.

Claude's identity lives in the JSONL on disk under `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`, not the running process — so an evicted session can park its JSONL, exit claude, and respawn `claude --session-id <uuid>` later with the prior conversation intact.

## Status

- **Phase 1.2c-A (#40):** lifecycle primitive. Per-session lifecycle goroutine, configurable `IdleTimeout`, `lifecycle_state` persisted to the registry, `Pool.Activate` / `Session.Activate` wake-on-attach in the control plane.
- **Phase 1.2c-B (#41):** concurrent active cap. `Config.ActiveCap`, `Session.Evict` public primitive, LRU victim selection at `Pool.Activate`'s spawn-path entry.
- **Phase 1.2c-B+ (#116):** `-pyry-active-cap=N` flag (default `0` = uncapped) wired through to `Config.ActiveCap` in `runSupervisor`. Shipped alongside the e2e tests that consume it; `cmd/pyry/main.go` also wires `*sessions.Pool` as the `control.Sessioner` so the existing `sessions.new` verb (#75) can mint sessions against the daemon at the binary boundary.
- **Phase 2.0 (#677 → #678 → #680):** per-conversation bind makes eviction load-bearing — RAM scales with *active* discussions, not total sessions. #677 eagerly mints + binds a dedicated session per `create_conversation`; #678 routes `send_message` through the bound session's cap-enforcing `Pool.Activate`; **#680 proves the consequence** at the binary boundary — a phone-created discussion's session idle-evicts, reactivates on the next turn, obeys `ActiveCap` with LRU victim selection, and never bleeds across discussions (see [§ Testing](#testing) and [codebase/680.md](../codebase/680.md)). Until per-conversation sessions actually routed through `Activate` (#678), eviction wasn't load-bearing because the only session was the bootstrap.

## Lifecycle states

Two states per `*Session`:

| State | Meaning |
|---|---|
| `active` | claude is (or should be) running. Supervisor is up; bridge is attachable. |
| `evicted` | claude exited cleanly. JSONL is frozen on disk. No process; ~zero RAM. |

Transitions:

| From | To | Trigger |
|---|---|---|
| `active` | `evicted` | Idle timer fires AND `attached == 0` |
| `evicted` | `active` | `Session.Activate(ctx)` called |
| any | (terminal) | Outer `ctx` cancelled (pyry shutdown) |

The supervisor's `*Bridge` and the underlying `*Supervisor` are reused across the active/evicted/active cycle — only the inner spawn ctx is recreated each active period. `Session.State()` in `evicted` reports `PhaseStopped` (faithful — the supervisor really isn't running).

## Configuration

```go
type Config struct {
    // ...
    IdleTimeout time.Duration  // 0 disables idle eviction
    ActiveCap   int            // <= 0 disables the concurrent active cap
}

// Per-session override.
type SessionConfig struct {
    // ...
    IdleTimeout time.Duration  // 0 inherits Config.IdleTimeout
}
```

CLI flags (`cmd/pyry/main.go`):

- `-pyry-idle-timeout` (default `0` / disabled; opt in with e.g. `30s`). Eviction is opt-in because the original `15m` default silently killed the supervised claude in daemon-mode (`install-service`) installs where the operator wants claude warm for plugin inbound — see #395. Operator escape hatch and smoke-test knob (`-pyry-idle-timeout 30s`).
- `-pyry-active-cap` (default `0` = uncapped). Negative values map to "unset" via `Pool.New`'s contract (`<=0` → uncapped); no validation in `runSupervisor`. Since Phase 2.0's per-conversation auto-mint workload landed (#677/#678), this is the load-bearing bound on host process/memory pressure from `create_conversation` spawns — set it (and/or `-pyry-idle-timeout`) in installs that accept remote conversation creation. Per-conversation participation under the cap is verified by #680's e2e; production operators still leave it at zero by default.

## `Session` surface

```go
func (s *Session) LifecycleState() lifecycleState
func (s *Session) Activate(ctx context.Context) error
func (s *Session) Evict(ctx context.Context) error
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
func (s *Session) Run(ctx context.Context) error
```

- **`Activate`** — moves an evicted session to `active`, blocking until the supervisor has started AND the post-transition registry persist has completed AND the supervisor has bound its PTY (or `ctx` cancels). When already active, returns immediately (both the wake channel and the supervisor's `ptmxReadyCh` are already closed). Idempotent under concurrent calls; safe from any goroutine. After Activate returns nil, callers can safely invoke `WriteUserTurn`/`Resize` and reach the live claude — the PTY-readiness wait closes the ~hundreds-of-ms gap between `transitionTo` closing `activeCh` and `runOnce` binding the new PTY master ([ADR 023](../decisions/023-activate-waits-pty-readiness.md), #396). See [§ Persist seam](#persist-seam) for the disk-consistency guarantee.
- **`Evict`** — moves an active session to `evicted`, blocking until the supervisor has stopped AND the post-transition persist has completed (or `ctx` cancels). When already evicted, returns immediately. Idempotent under concurrent calls; safe from any goroutine. **Force-eviction:** unlike the idle timer, `Evict` does *not* defer for `attached > 0`. Used by the cap policy at `Pool.Activate`'s spawn path; an attached caller will see EOF on its bridge.
- **`Attach`** — unchanged signature. Now bumps an `attached` counter under `lcMu`; the wrapper goroutine decrements on bridge `done`. While `attached > 0`, idle eviction is deferred (cap eviction is not).
- **`Run`** — rewritten as a loop over `runActive` / `runEvicted`, driving the state machine and persisting after every transition.

**Activate-before-Attach contract.** `bridge.Attach` on an evicted session would block on the pipe forever (no claude to drain it). Callers must Activate first. Two attach paths exist today and both Activate first with a 30s budget:

1. **Control plane** — `handleAttach` in `internal/control/server.go` (CLI `pyry attach`, since #40).
2. **Relay-routed `send_message`** — `handlers.SendMessage` in `internal/relay/handlers/send_message.go` (Discord/Telegram phone/plugin inbound, since #396). Without Activate, an inbound `send_message` against an idle-evicted session silently dropped through `Supervisor.WriteUserTurn`'s `ptmx == nil` discard branch — the failure mode that produced a 7.5h pyrybox outage on 2026-05-15. Since #678 the Activate target is the frame's **per-conversation bound session** (not the bootstrap), and it funnels through the cap-enforcing `Pool.Activate` (see [§ Relay-routed `send_message`](#relay-routed-send_message-396) and [conversation-session-binding.md § Routing](conversation-session-binding.md#routing-send_message-consumes-the-binding)).

A busted respawn surfaces as `attach: activate: <err>` on the control wire and as `protocol.CodeServerBinaryOffline` (`Retryable=true`) + `send_message.activate_failed` WARN log on the relay wire.

## `Pool` surface

```go
func (p *Pool) Activate(ctx context.Context, id SessionID) error
```

The single spawn-path entry. Resolves `id` and ensures the session is active, enforcing `Config.ActiveCap` along the way:

- **Cap unset (`activeCap <= 0`):** byte-identical to a thin `Session.Activate` wrapper. One early-return branch. No LRU bookkeeping cost on the hot path; `Pool.capMu` is never even taken.
- **Cap set:** `Pool.capMu` serialises the cap-check + victim eviction + new spawn so two concurrent `Activate`s can't both observe `active < cap` and both proceed. If the target is already active, `lastActiveAt` is bumped (LRU touch) and the call returns. Otherwise, when activating one more would exceed the cap, `pickLRUVictim` selects the oldest-`lastActiveAt` peer (excluding the target itself), `Session.Evict` runs synchronously, then `Session.Activate` proceeds.

`Pool.Run` remains the same shape — `bootstrap.Run(ctx)` plus the rotation watcher under `errgroup`. Multi-session fan-out is Phase 1.1's job.

## Activity definition

"Activity" = at least one client is currently attached (`attached > 0`). Tied to attach state, not per-byte counting through the bridge.

- While attached, the idle timer re-arms on fire (poll-with-grace) instead of evicting.
- On detach, the timer runs out the configured window and evicts.
- Real eviction may overshoot the configured timeout by up to one window — documented in the user-facing latency story.
- **The cap policy ignores `attached`.** Cap is a hard limit; a force-evicted attached session sees its bridge close. Phase 2.0 may add an attach-aware filter to the LRU candidate set.

`last_active_at` is bumped on every state transition (active↔evicted). The cap policy also bumps it (in-memory only, not persisted) on an `Activate` against an already-active session via `Session.touchLastActive` — keeps in-memory LRU ordering reflective of the most recent touch.

## Idle timer mechanism

Single `*time.Timer` per active period, owned by `runActive`:

```go
timer := time.NewTimer(s.idleTimeout)
defer timer.Stop()

for {
    select {
    case <-ctx.Done():        cancelSup(); drainSup(); return ctx.Err()
    case <-runErr:            // supervisor exited spontaneously → evict
    case <-timer.C:
        if s.attached > 0 { timer.Reset(s.idleTimeout); continue }
        cancelSup(); drainSup(); return nil
    case <-s.evictCh:
        // Cap-policy eviction: forced, regardless of attached count.
        cancelSup(); drainSup(); return nil
    }
}
```

When `idleTimeout == 0`, the timer is **never armed** — `timerCh` stays a nil channel that never selects. Idle eviction is genuinely off; the `evictCh` arm is unaffected (the cap policy can still drive an eviction).

## Concurrent active cap

Set `Config.ActiveCap` to bound the number of concurrently running claudes. `<= 0` is unset (uncapped); `>= 1` enforces.

```
                  Pool.capMu held across:
                  ┌─────────────────────────────────────────┐
  Pool.Activate ─>│ 1. Lookup target                        │
                  │ 2. activeCap <= 0? → Session.Activate    │
                  │ 3. Already active? → touchLastActive    │
                  │ 4. pickLRUVictim (under Pool.mu.RLock)   │
                  │ 5. victim.Evict(ctx)  ← persists to disk│
                  │ 6. Session.Activate(ctx)                 │
                  └─────────────────────────────────────────┘
```

Victim selection (`Pool.pickLRUVictim`):

1. Iterate `pool.sessions` under `Pool.mu.RLock`.
2. Read each `Session.lcState` and `lastActiveAt` under `lcMu`.
3. Skip non-active sessions; count active ones.
4. Skip the target itself (you cannot evict the session you are about to activate to make room for itself).
5. Of the rest, pick the oldest `lastActiveAt`.
6. Return `nil` if `active < activeCap` (cap doesn't bind) or no eligible peer exists.

O(n) in total session count. For pyry's expected scale (≤ 100s) this is cheap; an explicit ordered structure would defeat the "zero cost when unset" property.

**Pathological cases:**

- `cap=1` with one session, target is already active → `touchLastActive`, return. No eviction.
- `cap=1` with one session, target is inactive (no peer) → `pickLRUVictim` returns `nil`, target fills the slot.
- Eviction error (kill failed, registry write failed) → `Pool.Activate` returns `cap: evict lru victim <id>: <err>`. The new session does **not** spawn (cap would be exceeded). Caller treats this like any Activate failure.
- `Session.Activate` fails after a successful eviction → eviction is not rolled back. The pool now has one fewer active session than before. Acceptable: the LRU victim was going to be evicted anyway under sustained load, and rollback would require respawning a process we just killed.

## Concurrency model

Goroutines per `Session`:

1. **Lifecycle goroutine** (body of `Session.Run`) — owns state transitions, idle timer, supervisor lifecycle.
2. **Inner supervisor goroutine** (per active period) — wraps `s.sup.Run(subCtx)` and pipes the result to `runErr`. Drained at the end of each active period.
3. **Attach detach-watcher goroutines** — one per active attach; decrement `attached` when the bridge's done channel fires.

Mutexes:

- `Pool.capMu` — outermost lock, taken only by `Pool.Activate` when `activeCap > 0`. Serialises the cap-check + victim eviction + new spawn so concurrent `Activate`s can't both observe `active < cap` and both proceed. Never re-acquired by callees.
- `Pool.mu` — protects `pool.sessions`, `pool.bootstrap`, registry persistence.
- `Session.lcMu` — protects `lcState`, `attached`, `activeCh`, `evictedCh`, `lastActiveAt` (when read for the registry snapshot).
- `Supervisor.mu` — unchanged.

**Lock order: `Pool.capMu` → `Pool.mu` → `Session.lcMu`.** `transitionTo` releases `Session.lcMu` *before* calling `Pool.persist` (which then re-takes `lcMu` briefly inside `saveLocked` to read the snapshot), then re-acquires `lcMu` after persist returns to close the wake channel — sequential, not nested. `Session.Evict` is callable while `capMu` is held — its callback into `Pool.persist` takes `Pool.mu`, never `capMu`, so no re-entrancy. No reverse path; no deadlock.

Channels (per `Session`):

- `s.activateCh` (buffered 1) — `Activate` sends, `runEvicted` reads. Buffered so concurrent `Activate`s collapse without coordinating with the lifecycle goroutine's exact select position.
- `s.activeCh` (closed-on-active-AND-persisted) — broadcast wakeup to `Activate` waiters. **Closed iff `lcState == stateActive` AND the persist for that transition has completed.** `transitionTo(active)` closes it *after* `pool.persist` returns; `transitionTo(evicted)` replaces it with a fresh open channel up-front. `Activate` snapshots the channel under `lcMu` *before* waiting so a concurrent evict-replace doesn't drop the wakeup.
- `s.evictCh` (buffered 1) — `Evict` sends, `runActive` reads. Symmetric to `activateCh`.
- `s.evictedCh` (closed-on-evicted-AND-persisted) — broadcast wakeup to `Evict` waiters. Symmetric to `activeCh`: `transitionTo(evicted)` closes it *after* `pool.persist` returns; `transitionTo(active)` replaces it with a fresh open channel up-front.
- `runErr` (buffered 1, per active period) — supervisor exit value.

Shutdown sequence:

1. Outer `ctx` cancelled (SIGINT/SIGTERM).
2. Lifecycle goroutine in either `runActive` or `runEvicted` selects on `<-ctx.Done()`.
3. If active: cancel inner supervisor ctx, drain `<-runErr`, return `ctx.Err()`.
4. If evicted: return `ctx.Err()` immediately.
5. `Pool.Run` returns. `cmd/pyry` proceeds with control-server shutdown.

In-flight `Activate` callers during shutdown see `<-ctx.Done()` and return `ctx.Err()` — no deadlock.

## Registry schema delta

```go
type registryEntry struct {
    // ...
    LifecycleState string `json:"lifecycle_state,omitempty"` // "active" | "evicted"
}
```

- `omitempty` keeps the dominant `active` case off disk — preserves the idempotent-reload byte-stability property #34 paid for upfront.
- Old pyry binaries reading new files ignore the field (default `encoding/json` decoder; `DisallowUnknownFields` is not set).
- New pyry reading old files defaults the missing field to `"active"`.
- **Non-bootstrap sessions** warm-start in whatever state the registry says — including `evicted`. Lazy respawn on attach drives the `Activate` that wakes them.
- **The bootstrap session** ignores its persisted `lifecycle_state` and always warm-starts active. The bootstrap is the per-process auto-spawn invariant; nothing under non-TTY stdin (launchd, systemd, piped wrapper) calls `Pool.Activate` on it, so a persisted `evicted` would park `runEvicted` on `activateCh` forever. The carve-out lives at the load layer in `Pool.New`'s `pickBootstrap` arm — `lcState = stateActive` regardless of `entry.LifecycleState`. See [ADR 016](../decisions/016-bootstrap-ignores-persisted-lifecycle-state.md).

The string form (`"active"` / `"evicted"`) is the wire shape; the in-memory form is the `lifecycleState` enum. `parseLifecycleState` and `(lifecycleState).String()` bridge the two.

## Persist seam

Every state transition writes the registry through `Pool.persist`. The persist is sequenced **between** the in-memory state flip and the wake of `Activate`/`Evict` waiters, so a caller that sees the channel close is guaranteed to read post-transition state from disk:

```go
func (s *Session) transitionTo(newState lifecycleState) error {
    // 1. Flip lcState; allocate the *opposite-direction* wake channel for the
    //    next transition. The current direction's channel stays open here.
    s.lcMu.Lock()
    s.lcState = newState
    s.lastActiveAt = time.Now().UTC()
    switch newState {
    case stateActive:
        s.evictedCh = make(chan struct{}) // for the next Evict
    case stateEvicted:
        s.activeCh = make(chan struct{})  // for the next Activate
    }
    s.lcMu.Unlock()

    // 2. Persist with lcMu released — saveLocked re-takes per-session lcMu.
    var persistErr error
    if s.pool != nil {
        persistErr = s.pool.persist()
    }

    // 3. Wake waiters. lcMu re-acquired only to serialise the close against
    //    any concurrent Activate/Evict capturing the channel reference.
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

**Invariant:** `activeCh` is closed iff `lcState == stateActive` AND the persist for that transition has completed; `evictedCh` symmetrically. Cold-start initialisation in `Pool.New` / `Pool.Create` already matches this — a session that warm-starts in `stateActive` initialises `activeCh = closedChan()` (no transition pending, disk already consistent) and `evictedCh = make(chan struct{})`.

`Activate` / `Evict` always wait on the channel — there is no "already in target state" early return. When fully transitioned, the channel is already closed and the receive returns immediately; mid-transition (state flipped, persist still running), the receive correctly blocks until persist completes. The race surfaced by #155 (`sess.Evict(ctx)` returns; subsequent `loadRegistry` reads pre-eviction state) is structurally impossible under this contract — see [ADR 013](../decisions/013-evict-activate-persist-ordering.md).

**Lock ordering.** `lcMu` is released before `pool.persist` (which takes `Pool.mu` and re-acquires per-session `lcMu` inside `saveLocked`). The post-persist `lcMu.Lock(); close(...); lcMu.Unlock()` is sequential, not nested — no new lock-order edge. The two `lcMu` acquisitions don't coalesce with anything; they exist only to order the close against concurrent waiters capturing the channel reference.

**Failure posture.** On persist failure the wake channel still closes — a permanently-stuck waiter is a worse failure mode than a waiter that wakes to stale disk. The persist error propagates up `Run`, which treats it as fatal (`return fmt.Errorf("persist evicted: %w", err)`).

`saveLocked` reads each session's lifecycle state and `lastActiveAt` under `Session.lcMu` when building the registry snapshot. The lock order is the same `Pool.mu → Session.lcMu` enforced everywhere else in the package.

`RotateID` mutates `session.id` *without* taking `lcMu`. Today's only callers (startup reconciliation, fsnotify rotation watcher) run before any lifecycle goroutine begins observing the id, so no concurrent reader exists. `lastActiveAt` IS protected by `lcMu` and is taken briefly. Documented in the function comment.

## Attach-event integration

Two callers reach Activate before driving the supervisor — the control plane and the relay-routed `send_message` handler. Both use the same 30s budget so operator mental models for "how long before the binary gives up" stay uniform.

### Control plane

`internal/control` adds `Activate(ctx)` to its `Session` interface. `handleAttach` calls it before `Attach`:

```go
activateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if err := sess.Activate(activateCtx); err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: activate: %v", err)})
    return false
}
done, err := sess.Attach(conn, conn)
// ...
```

The 30s window caps the documented 2-15s respawn latency with safety margin. A busted respawn surfaces as a clean error rather than a hung attach.

`handleStatus` does **not** activate. Status on an evicted session reports the supervisor's `PhaseStopped`; that's faithful and avoids spurious wakeups from a poll. Wire-string drift on healthy daemons is unchanged: `pyry status` still reports `PhaseRunning` on a never-evicted session.

### Relay-routed `send_message` (#396)

`internal/relay/handlers/send_message.go` extends `TurnWriter` with `Activate(ctx) error` and runs it before `WriteUserTurn`:

```go
activateCtx, cancelActivate := context.WithTimeout(ctx, sendMessageActivateTimeout)
if err := w.Activate(activateCtx); err != nil {
    cancelActivate()
    if errors.Is(err, context.Canceled) && ctx.Err() != nil {
        return err
    }
    logger.Warn("relay: send_message activate failed",
        "event", "send_message.activate_failed", ...)
    return replyError(ctx, c, env, protocol.CodeServerBinaryOffline, msgServerBinaryOffline, true)
}
cancelActivate()
err := w.WriteUserTurn(p.ConversationID, []byte(p.Text))
```

`sendMessageActivateTimeout` is the same 30s the CLI attach path uses, deliberately. Activate timeout maps to `protocol.CodeServerBinaryOffline` with `Retryable=true`; `ctx.Canceled` (conn closing) propagates to the dispatcher's per-conn unwind.

**Since #678, `w` is the per-conversation bound session, not the bootstrap.** The handler resolves the frame's `ConversationID` → `CurrentSessionID` → session via a `handlers.SessionRouter` seam before this block, and `w` is a `cmd/pyry` `boundSession` adapter whose `Activate` calls `Pool.Activate(ctx, id)` rather than `Session.Activate` directly — so a re-activated per-conversation session is a full `ActiveCap` citizen (the bootstrap path used `Session.Activate` because the bootstrap is never cap-evicted). The two-phase block itself is byte-identical; only the source of `w` changed. See [conversation-session-binding.md § Routing](conversation-session-binding.md#routing-send_message-consumes-the-binding).

### Eviction cause record

`runActive` emits a WARN-level `session.idle_eviction` log line at the SIGKILL moment (#396), preceding the supervisor-level `claude exited` line. Operators no longer need to correlate the generic `signal: killed` against the configured idle window. Fields: `session_id`, `idle_timeout`, `bootstrap`. The cap-policy eviction path does not yet emit a sibling line — when that path next gets touched, mirror this pattern.

## Failure posture

| Failure | Handling |
|---|---|
| Registry save fails on transition | Returned from `Session.Run` → bubbles out of `Pool.Run` → `cmd/pyry` exits with error. Matches #34's fatal-on-save posture. |
| Supervisor exits spontaneously while active | Treated as evict trigger. Today the supervisor only returns on ctx cancel, so this branch is mostly defensive. |
| `Activate` ctx cancelled before active | Returns `ctx.Err()` to caller; the lifecycle goroutine still finishes its in-flight transition. |
| `Attach` on evicted session without prior Activate | `bridge.Attach` would block forever. Documented contract: control plane must Activate first. |

**JSONL durability.** Today inner-ctx-cancel triggers SIGKILL via `exec.CommandContext`. A truncated final line is theoretically possible, but claude's JSONL is line-delimited — readers skip incomplete entries on resume. The AC ("JSONL on disk is untouched") reads as "pyry doesn't delete or modify the file"; it doesn't require claude to flush gracefully. Graceful supervisor stop is tracked as an open question for a follow-up.

## Latency story

Respawn cost dominates the visible eviction overhead. Documented for operators in the `-pyry-idle-timeout` flag help and worth knowing here:

| Phase | Typical latency |
|---|---|
| `Activate` → supervisor starts | ~immediate (Go-level signalling) |
| claude binary → first PTY output (cold prompt cache) | 2-15s, dominated by conversation size + prompt cache state |

Real eviction may also overshoot the configured `IdleTimeout` by up to one window because of poll-with-grace.

## Testing

Reuses the `/bin/sleep` fake-claude pattern from `internal/sessions` — no new test infrastructure (see [lessons.md](../../lessons.md#test-helpers-across-packages)).

`internal/sessions/session_test.go` covers idle eviction firing, eviction deferral while attached, respawn via `Activate`, no-op `Activate` on active sessions, ctx-cancellation paths, shutdown from both states.

`internal/sessions/session_persist_test.go` (#169) covers the persist-before-wake ordering: `TestSession_EvictBlocksUntilPersisted` asserts `loadRegistry` immediately after `sess.Evict(ctx)` returns shows `lifecycle_state == "evicted"` (no poll); `TestSession_ActivateBlocksUntilPersisted` asserts the symmetric guarantee on a session that warm-starts in `stateEvicted`; a stress wrapper loops 20 evict↔activate transitions asserting disk consistency at each step. Designed to pass deterministically under `go test -race -count=20 ./internal/sessions/...`.

`internal/sessions/registry_test.go` covers `lifecycle_state` round-trip and backwards-compat (missing field defaults to `active`).

`internal/sessions/pool_test.go` covers the bootstrap warm-start contract (#202): `TestPool_BootstrapWarmStart_IgnoresPersistedEvicted` asserts that a registry entry with `lifecycle_state: "evicted"` loads as `stateActive` for the bootstrap, and `TestPool_BootstrapEvictedOnDisk_StartsClaudeOnWarmStart` runs `Pool.Run` against the seeded fixture with `/bin/sleep` as the supervised child and asserts `Phase: running` + non-zero `StartedAt` within 5s (verified to fail against the pre-fix v0.10.1 binary). Also covers the parity-when-disabled regression guard (`IdleTimeout: 0` runs for several seconds without transitions).

`internal/sessions/pool_cap_test.go` covers the cap policy: `ActiveCap == 0` parity (no enforcement, no LRU bookkeeping cost), cap-binds-evicts-LRU with three sessions, the `cap=1` single-session pathological case, and a race test driving N concurrent `Activate`s against `cap=1` to assert the active count never exceeds the cap. The race test uses Bridge mode (per-supervisor pipes) because foreground mode leaks one stdin-bound `io.Copy` goroutine per `runOnce` that contends on `os.Stdin`'s `fdMutex` under stress.

`internal/control/server_test.go` covers `handleAttach` calling `Activate` exactly once, and the `Activate`-error path surfacing as `attach: activate: <err>` on the wire.

`internal/e2e/idle_test.go` (build tag `e2e`, ticket #115) covers the binary-boundary integration: `TestE2E_IdleEviction_EvictsBootstrap` runs pyry with `-pyry-idle-timeout=1s` and asserts the bootstrap evicts (registry `lifecycle_state == "evicted"`, `pyry status` not reporting `Phase: running`); `TestE2E_IdleEviction_LazyRespawn` issues a raw `VerbAttach` over the control socket post-eviction and asserts the session returns to active and the supervisor reaches `Phase: running` while the conn is held. See [e2e-harness.md § Idle-Eviction + Lazy-Respawn Pattern](e2e-harness.md).

`internal/e2e/cap_test.go` (build tag `e2e`, ticket #116) covers the cap-policy binary-boundary gap and the cap+idle interleave: `TestE2E_ActiveCap_EvictsLRU` (cap=2, three `sessions.new` mints with 50ms gaps; asserts each new spawn cap-evicts the LRU peer); `TestE2E_ActiveCap_IdleInterleave` (cap=2 + idle=2s; asserts the cap-evict victim and a subsequent idle-evict of the surviving non-most-recent session interleave consistently). Both use a tiny shell-script `claude` stand-in (`Pool.Create` appends `--session-id <uuid>` to `ClaudeArgs`, which both BSD and GNU `sleep(1)` reject). See [e2e-harness.md § Active-Cap Eviction Pattern](e2e-harness.md).

`internal/e2e/per_conversation_eviction_test.go` (build tag `e2e`, ticket #680) closes Phase 2.0 by proving **phone-created per-conversation** sessions participate in this machinery (the bootstrap/`sessions.new` paths above never exercised the `create_conversation` mint→bind→route adapters). `TestE2E_PerConversation_IdleEvictsAndReactivates` (idle=2s, uncapped): two `create_conversation`s bind distinct sessions, both idle-evict, a `send_message` reactivates one (ack within 15s + registry back to `active`) while the other stays `evicted` (no cross-bleed). `TestE2E_PerConversation_CapEvictsCrossDiscussion` (cap=2, no idle): a sequence of `create_conversation`s (each a spawning `Pool.Activate`) drives LRU eviction — first the bootstrap, then a *per-conversation* peer (the security-sensitive cross-discussion eviction) — asserting only the deliberate victim transitions and the active count never exceeds the cap. Both use fakeclaude-TUI (the reactivation ack is gated on `WaitReady`+`DeliverPrompt` even on the nil-resolver path) via a file-local variadic-flag `startPerConvHarness` (a generalization of `respawn_after_eviction_test.go`'s `startEvictionHarness`). The shared-env-UUID fakeclaude JSONL means this tier asserts **lifecycle/routing scoping** (per-UUID registry transitions, reactivation target), not per-file JSONL content — content recall is realclaude's domain. See [codebase/680.md](../codebase/680.md).

## Manual smoke

```bash
go build -o pyry ./cmd/pyry
./pyry -pyry-idle-timeout 30s &           # short timeout for the smoke
# (in another shell) pyry attach, send a prompt, get a reply, detach
sleep 35
ps -p <claude-pid>                         # gone
ls ~/.claude/projects/<encoded-cwd>/*.jsonl # still there
jq '.sessions[0].lifecycle_state' ~/.pyry/pyry/sessions.json  # "evicted"
pyry attach                                # 2-15s pause, then prompt
# ask claude to recall earlier discussion → it references prior content
pyry stop
```

## References

- Tickets: [#40](https://github.com/pyrycode/pyrycode/issues/40), [#41](https://github.com/pyrycode/pyrycode/issues/41), [#116](https://github.com/pyrycode/pyrycode/issues/116), [#169](https://github.com/pyrycode/pyrycode/issues/169), [#202](https://github.com/pyrycode/pyrycode/issues/202), [#395](https://github.com/pyrycode/pyrycode/issues/395), [#396](https://github.com/pyrycode/pyrycode/issues/396), [#680](https://github.com/pyrycode/pyrycode/issues/680) (Phase 2.0 per-conversation participation, e2e)
- Specs: [`docs/specs/architecture/40-idle-eviction-lazy-respawn.md`](../../specs/architecture/40-idle-eviction-lazy-respawn.md), [`docs/specs/architecture/41-concurrent-active-cap-lru.md`](../../specs/architecture/41-concurrent-active-cap-lru.md), [`docs/specs/architecture/169-evict-activate-persist-ordering.md`](../../specs/architecture/169-evict-activate-persist-ordering.md), [`docs/specs/architecture/202-supervise-bootstrap-evicted-warm-start-hang.md`](../../specs/architecture/202-supervise-bootstrap-evicted-warm-start-hang.md), [`docs/specs/architecture/396-send-message-respawn.md`](../../specs/architecture/396-send-message-respawn.md)
- ADRs: [`005-idle-eviction-state-machine.md`](../decisions/005-idle-eviction-state-machine.md), [`006-concurrent-active-cap-lru.md`](../decisions/006-concurrent-active-cap-lru.md), [`013-evict-activate-persist-ordering.md`](../decisions/013-evict-activate-persist-ordering.md), [`016-bootstrap-ignores-persisted-lifecycle-state.md`](../decisions/016-bootstrap-ignores-persisted-lifecycle-state.md), [`023-activate-waits-pty-readiness.md`](../decisions/023-activate-waits-pty-readiness.md)
- Sibling docs: [`sessions-package.md`](sessions-package.md), [`sessions-registry.md`](sessions-registry.md), [`control-plane.md`](control-plane.md)
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md)
