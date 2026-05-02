# Idle Eviction + Lazy Respawn

Per-session lifecycle that evicts idle claude processes (~zero RAM) and respawns them on demand. Claude's identity lives in the JSONL on disk under `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`, not the running process — so an evicted session can park its JSONL, exit claude, and respawn `claude --session-id <uuid>` later with the prior conversation intact.

## Status

- **Phase 1.2c-A (#40):** primitive landed. Per-session lifecycle goroutine, configurable `IdleTimeout`, `lifecycle_state` persisted to the registry, `Pool.Activate` / `Session.Activate` wake-on-attach in the control plane.
- **Phase 1.2c-B (planned):** LRU concurrent-active cap layered on top. Consumes the `Activate` / `lifecycle_state` shape introduced here.
- **Phase 2.0:** first-message lazy bind makes eviction load-bearing — RAM scales with active conversations, not total sessions.

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
// Pool-level default. Inherited by SessionConfig when its IdleTimeout==0.
type Config struct {
    // ...
    IdleTimeout time.Duration  // 0 disables eviction
}

// Per-session override.
type SessionConfig struct {
    // ...
    IdleTimeout time.Duration  // 0 inherits Config.IdleTimeout
}
```

CLI flag (`cmd/pyry/main.go`): `-pyry-idle-timeout` (default `15m`). `0` disables eviction entirely. Production default: 15 minutes. Unit-test default: `0` (eviction off, parity with pre-1.2c behaviour).

The flag also exists as the operator escape hatch and the smoke-test knob (`-pyry-idle-timeout 30s`).

## `Session` surface

```go
func (s *Session) LifecycleState() lifecycleState
func (s *Session) Activate(ctx context.Context) error
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
func (s *Session) Run(ctx context.Context) error
```

- **`Activate`** — moves an evicted session to `active`, blocking until the supervisor has started (or `ctx` cancels). No-op when already active. Idempotent under concurrent calls; safe from any goroutine.
- **`Attach`** — unchanged signature. Now bumps an `attached` counter under `lcMu`; the wrapper goroutine decrements on bridge `done`. While `attached > 0`, idle eviction is deferred.
- **`Run`** — rewritten as a loop over `runActive` / `runEvicted`, driving the state machine and persisting after every transition.

**Activate-before-Attach contract.** `bridge.Attach` on an evicted session would block on the pipe forever (no claude to drain it). Callers must Activate first. The control plane is the only attach caller in pyry today and always Activates first (`handleAttach` in `internal/control/server.go`).

## `Pool` surface

```go
func (p *Pool) Activate(ctx context.Context, id SessionID) error
```

Thin wrapper: resolves `id` and calls `Session.Activate`. Symmetry with the rest of `Pool`'s surface; the future router gets a single entry point. `Pool.Run` remains the same shape — `bootstrap.Run(ctx)` plus the rotation watcher under `errgroup`. Multi-session fan-out is Phase 1.1's job.

## Activity definition

"Activity" = at least one client is currently attached (`attached > 0`). Tied to attach state, not per-byte counting through the bridge.

- While attached, the idle timer re-arms on fire (poll-with-grace) instead of evicting.
- On detach, the timer runs out the configured window and evicts.
- Real eviction may overshoot the configured timeout by up to one window — documented in the user-facing latency story.

`last_active_at` is bumped on every state transition (active↔evicted). That's the proxy the upcoming LRU follow-up consumes for victim selection.

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
    }
}
```

When `idleTimeout == 0`, the timer is **never armed** — `timerCh` stays a nil channel that never selects. Eviction is genuinely off.

## Concurrency model

Goroutines per `Session`:

1. **Lifecycle goroutine** (body of `Session.Run`) — owns state transitions, idle timer, supervisor lifecycle.
2. **Inner supervisor goroutine** (per active period) — wraps `s.sup.Run(subCtx)` and pipes the result to `runErr`. Drained at the end of each active period.
3. **Attach detach-watcher goroutines** — one per active attach; decrement `attached` when the bridge's done channel fires.

Mutexes:

- `Pool.mu` — protects `pool.sessions`, `pool.bootstrap`, registry persistence.
- `Session.lcMu` — protects `lcState`, `attached`, `activeCh`, `lastActiveAt` (when read for the registry snapshot).
- `Supervisor.mu` — unchanged.

**Lock order: `Pool.mu` → `Session.lcMu`.** `transitionTo` releases `Session.lcMu` *before* calling `Pool.persist` (which then re-takes `lcMu` briefly inside `saveLocked` to read the snapshot). No reverse path; no deadlock.

Channels:

- `s.activateCh` (buffered 1) — `Activate` sends, `runEvicted` reads. Buffered so concurrent `Activate`s collapse without coordinating with the lifecycle goroutine's exact select position.
- `s.activeCh` (closed-on-active) — broadcast wakeup to `Activate` waiters. `transitionTo(active)` closes it; `transitionTo(evicted)` replaces it with a fresh open channel. `Activate` snapshots the channel under `lcMu` *before* waiting so a concurrent evict-replace doesn't drop the wakeup.
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
- Bootstrap warm-starts in whatever state the registry says — including `evicted` if pyry was stopped while the session was evicted.

The string form (`"active"` / `"evicted"`) is the wire shape; the in-memory form is the `lifecycleState` enum. `parseLifecycleState` and `(lifecycleState).String()` bridge the two.

## Persist seam

Every state transition writes the registry through `Pool.persist`:

```go
func (s *Session) transitionTo(newState lifecycleState) error {
    s.lcMu.Lock()
    s.lcState = newState
    s.lastActiveAt = time.Now().UTC()
    if newState == stateActive { close(s.activeCh) } else { s.activeCh = make(chan struct{}) }
    s.lcMu.Unlock()
    return s.pool.persist()  // takes Pool.mu (write); saveLocked re-takes lcMu briefly
}
```

`saveLocked` reads each session's lifecycle state and `lastActiveAt` under `Session.lcMu` when building the registry snapshot. The lock order is the same `Pool.mu → Session.lcMu` enforced everywhere else in the package.

`RotateID` mutates `session.id` *without* taking `lcMu`. Today's only callers (startup reconciliation, fsnotify rotation watcher) run before any lifecycle goroutine begins observing the id, so no concurrent reader exists. `lastActiveAt` IS protected by `lcMu` and is taken briefly. Documented in the function comment.

## Control-plane integration

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

`internal/sessions/registry_test.go` covers `lifecycle_state` round-trip and backwards-compat (missing field defaults to `active`).

`internal/sessions/pool_test.go` covers bootstrap warm-starting in `evicted`, and the parity-when-disabled regression guard (`IdleTimeout: 0` runs for several seconds without transitions).

`internal/control/server_test.go` covers `handleAttach` calling `Activate` exactly once, and the `Activate`-error path surfacing as `attach: activate: <err>` on the wire.

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

- Ticket: [#40](https://github.com/pyrycode/pyrycode/issues/40)
- Spec: [`docs/specs/architecture/40-idle-eviction-lazy-respawn.md`](../../specs/architecture/40-idle-eviction-lazy-respawn.md)
- ADR: [`005-idle-eviction-state-machine.md`](../decisions/005-idle-eviction-state-machine.md)
- Sibling docs: [`sessions-package.md`](sessions-package.md), [`sessions-registry.md`](sessions-registry.md), [`control-plane.md`](control-plane.md)
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md)
