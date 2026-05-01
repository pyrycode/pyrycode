# Architecture spec: Phase 1.2c-A — idle eviction + lazy respawn

Ticket: [#40](https://github.com/pyrycode/pyrycode/issues/40) (split from #36)
Phase: 1.2c-A
Size: M

## Context

A session's identity lives in the JSONL on disk under `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`, not in the running claude process. An idle session can park its JSONL on disk, exit claude (~zero RAM), and respawn `claude --session-id <uuid>` when a message arrives — claude reads the prior conversation and continues. This ticket introduces that lifecycle primitive.

Why now: Phase 2.0's first-message lazy bind will let Discord channels mint sessions automatically. Without eviction, RAM scales with total session count rather than active conversation count. This ticket gets the primitive in before that becomes load-bearing. A follow-up ticket (LRU concurrent-active cap) consumes the `Activate` shape introduced here.

The work touches one coherent state machine spread across `internal/sessions` (the lifecycle goroutine), `internal/control` (Activate-before-Attach), and `cmd/pyry` (idle-timeout flag).

## Design

### High level

Each `*Session` gains a per-session lifecycle goroutine that owns a two-state machine — `active` (supervisor running) ↔ `evicted` (no claude process; JSONL frozen on disk). `Session.Run` is rewritten from "delegate to `s.sup.Run`" to a loop that drives the state machine and starts/stops the embedded supervisor as the state demands.

`Pool.Run` is **not** restructured for #40. It still calls `bootstrap.Run(ctx)` directly. The new state machine lives entirely inside `Session.Run`. Multi-session fan-out is Phase 1.1's job and lands separately.

The control plane gains one call site: `handleAttach` invokes `Session.Activate(ctx)` before `Session.Attach(in, out)` so an evicted session is woken before the bridge is bound.

### Lifecycle states

```go
// internal/sessions/session.go (new)
type lifecycleState uint8

const (
    stateActive  lifecycleState = iota // claude is (or should be) running
    stateEvicted                       // claude exited; JSONL is on disk
)
```

Transitions:

| From | To | Trigger |
|---|---|---|
| `active` | `evicted` | Idle timer fires AND no client is attached |
| `evicted` | `active` | `Session.Activate(ctx)` called |
| `active` | (terminal) | Outer `ctx` cancelled (pyry shutdown) |
| `evicted` | (terminal) | Outer `ctx` cancelled (pyry shutdown) |

Persistence: `lifecycle_state` is added to `registryEntry`. Bootstrap session warm-starts in whatever state the registry says — including starting in `evicted` if pyry was stopped while the session was evicted. Cold start writes `active`.

Backwards-compat: missing field on read defaults to `active` (uses `omitempty` on write so old pyry binaries still load new files cleanly).

### `Session` additions

```go
// internal/sessions/session.go
type Session struct {
    // ... existing fields ...
    pool        *Pool          // back-pointer for persistence callbacks
    idleTimeout time.Duration  // 0 disables eviction (test default)

    lcMu       sync.Mutex
    lcState    lifecycleState
    attached   int            // count of currently-bound bridge clients
    activeCh   chan struct{}  // closed when state becomes active; reset on evict
    activateCh chan struct{}  // buffered(1); Activate sends, lifecycle reads in evicted
}

// LifecycleState returns a snapshot of the current state.
func (s *Session) LifecycleState() lifecycleState

// Activate moves the session to active state if currently evicted, blocking
// until the supervisor has started (or ctx is cancelled). No-op when already
// active. Safe to call from any goroutine; idempotent under concurrent calls.
func (s *Session) Activate(ctx context.Context) error
```

The `activateCh` is buffered(1) so `Activate` can send without coordinating with the lifecycle goroutine's exact position in the select. The lifecycle goroutine drains it during the evicted-state select.

The `activeCh` is the synchronization point Activate waits on. The lifecycle goroutine closes it when entering `active`, replaces it with a fresh open channel when entering `evicted`. `Activate` snapshots the channel under `lcMu` before waiting so a concurrent evict-replace doesn't drop the wakeup.

### `Session.Run` (the lifecycle loop)

```go
func (s *Session) Run(ctx context.Context) error {
    for {
        switch s.snapshotState() {
        case stateActive:
            if err := s.runActive(ctx); err != nil { // returns nil on idle-evict, ctx.Err on shutdown
                return err
            }
            if err := s.transitionTo(stateEvicted); err != nil {
                return fmt.Errorf("persist evicted: %w", err)
            }
        case stateEvicted:
            if err := s.runEvicted(ctx); err != nil { // returns nil on activate, ctx.Err on shutdown
                return err
            }
            if err := s.transitionTo(stateActive); err != nil {
                return fmt.Errorf("persist active: %w", err)
            }
        }
    }
}
```

`runActive` spawns the supervisor on an inner ctx, arms an idle timer, and selects on:

- `ctx.Done()` (outer shutdown) → cancel inner, drain supervisor, return `ctx.Err()`
- supervisor exit (clean or otherwise) → return nil; the loop transitions to evicted
- idle timer fires → if `attached > 0`, re-arm; else cancel inner, drain supervisor, return nil

`runEvicted` blocks on `s.activateCh` or `ctx.Done()` and returns. No supervisor is running.

The "drain supervisor" step is a `<-runErr` receive on a channel populated by a goroutine wrapping `s.sup.Run(subCtx)`. The supervisor's own ctx-cancel path is what shuts claude down (currently SIGKILL via `exec.CommandContext`).

**JSONL durability note.** SIGKILL during a JSONL write could in principle leave a truncated final line. Claude tolerates a malformed last line on resume (line-delimited JSON; readers skip incomplete entries). The AC ("JSONL on disk is untouched") is read as "pyry doesn't delete or modify the file"; it does not require claude itself to flush gracefully. A graceful-stop path for the supervisor is out of scope here and tracked as an open question.

### Idle timer

Implemented inside `runActive` with a single `*time.Timer`:

```go
timer := time.NewTimer(s.idleTimeout)
defer timer.Stop()

for {
    select {
    case <-ctx.Done():
        cancelSup(); <-runErr
        return ctx.Err()
    case <-runErr:
        return nil // supervisor exited spontaneously — uncommon; treat as evict
    case <-timer.C:
        s.lcMu.Lock()
        attached := s.attached
        s.lcMu.Unlock()
        if attached > 0 {
            timer.Reset(s.idleTimeout) // poll-with-grace; eviction defers up to one window
            continue
        }
        cancelSup(); <-runErr
        return nil
    }
}
```

**Activity definition.** "Activity" = at least one client is currently attached. While attached, eviction is deferred. On detach, eviction fires after `idleTimeout`. Rationale: today the only way for a user (or future router) to interact with a session is `pyry attach`; tying activity to attach state captures the intent and avoids per-byte counting reader plumbing through the bridge. The LRU follow-up consumes `last_active_at`, which is bumped on every state transition (active↔evicted) — that's a sufficient proxy for victim selection.

The poll-with-grace pattern (re-arm on fire when attached) means real eviction may overshoot the configured timeout by up to one window. Acceptable; documented in the user-facing docs note as part of the latency story.

When `s.idleTimeout == 0`, the timer is **never armed** — eviction is disabled. This is the unit-test default and the safety hatch if an operator hits a pathology in production. Production default is 15 minutes, set in `New()`.

### `Session.Attach` changes

`Attach` gains attach/detach bookkeeping under `lcMu`:

```go
func (s *Session) Attach(in io.Reader, out io.Writer) (<-chan struct{}, error) {
    if s.bridge == nil {
        return nil, ErrAttachUnavailable
    }
    s.lcMu.Lock()
    s.attached++
    s.lcMu.Unlock()

    done, err := s.bridge.Attach(in, out)
    if err != nil {
        s.lcMu.Lock()
        s.attached--
        s.lcMu.Unlock()
        return nil, err
    }

    wrapped := make(chan struct{})
    go func() {
        <-done
        s.lcMu.Lock()
        s.attached--
        s.lcMu.Unlock()
        close(wrapped)
    }()
    return wrapped, nil
}
```

The wrapped done channel preserves the existing public contract: callers `<-done` to know when the bridge ended. Bridge ownership of in/out is unchanged.

### `Pool` additions

`Config` and `SessionConfig` each gain an `IdleTimeout time.Duration`. Defaults applied in `New()`:
- `Config.IdleTimeout`: 15 minutes when zero (production default)
- `SessionConfig.IdleTimeout`: inherits `Config.IdleTimeout` when zero

The bootstrap session's idle timeout is `cfg.Bootstrap.IdleTimeout` after defaulting, set on the `*Session` at construction.

A single new `Pool` method handles persistence callbacks from the Session lifecycle goroutine:

```go
// persist takes Pool.mu (write) and writes the registry. Called by Session
// after a state transition. The lifecycle state is read from the Session
// itself by saveLocked when it builds the registry snapshot, so callers do
// not need to pass anything in.
func (p *Pool) persist() error {
    p.mu.Lock()
    defer p.mu.Unlock()
    return p.saveLocked()
}
```

`Pool.saveLocked` is updated to read each Session's lifecycle state under `Session.lcMu` when building the registry snapshot. Lock order: `Pool.mu` → `Session.lcMu`. This is consistent with the existing pattern (`RotateID` already takes `Pool.mu` and mutates Session fields directly; that path stays — `lastActiveAt` and `id` mutations there don't require `lcMu` because they happen with no concurrent Session lifecycle activity for that ID; document this invariant in `RotateID`'s comment).

`Pool.Activate(id)` is a thin wrapper for the control plane:

```go
func (p *Pool) Activate(ctx context.Context, id SessionID) error {
    sess, err := p.Lookup(id)
    if err != nil { return err }
    return sess.Activate(ctx)
}
```

Not strictly required (control plane can call `sess.Activate` directly through the resolver), but matches the package's public-API style and gives the future router a single entry point.

### `transitionTo` (the persist seam)

```go
func (s *Session) transitionTo(newState lifecycleState) error {
    s.lcMu.Lock()
    s.lcState = newState
    s.lastActiveAt = time.Now().UTC()
    if newState == stateActive {
        close(s.activeCh) // wake Activate waiters
    } else {
        s.activeCh = make(chan struct{})
    }
    s.lcMu.Unlock()
    return s.pool.persist()
}
```

Lock ordering: Session.lcMu released **before** Pool.mu acquired. `Pool.persist` → `Pool.saveLocked` → re-takes `Session.lcMu` briefly to read the snapshot. No deadlock.

### `internal/control/server.go` changes

The `Session` interface in `server.go` gains an `Activate` method:

```go
type Session interface {
    State() supervisor.State
    Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
    Activate(ctx context.Context) error // NEW
}
```

`*sessions.Session` satisfies it structurally (no producer-side change needed for the interface).

`handleAttach` calls Activate before Attach:

```go
sess, err := s.sessions.Lookup("")
if err != nil { /* existing error path */ }

// Wake the session if evicted. Bound by the per-conn ctx (which already had
// its handshake deadline cleared above).
activateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if err := sess.Activate(activateCtx); err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: activate: %v", err)})
    return false
}

done, err := sess.Attach(conn, conn)
// ... existing path ...
```

The 30s activate timeout caps the documented 2-15s respawn window with safety margin. If exceeded, the client sees a clean error.

`handleStatus` does NOT activate. Status on an evicted session reports the supervisor's `PhaseStopped` (the phase the supervisor sits in when not running). That's faithful — the session genuinely is not running. No wire-string drift expected for the byte-identical AC, since today's `pyry status` on a healthy daemon reports `PhaseRunning`; a status on a never-evicted session continues to report exactly that.

### `cmd/pyry/main.go` changes

Add a `-pyry-idle-timeout` flag (default `15m`), wire into `Config.IdleTimeout` and `SessionConfig.IdleTimeout`. The flag exists primarily to support the smoke test (using `30s`) and as an operator escape hatch.

### Registry schema delta

```go
type registryEntry struct {
    ID             SessionID `json:"id"`
    Label          string    `json:"label"`
    CreatedAt      time.Time `json:"created_at"`
    LastActiveAt   time.Time `json:"last_active_at"`
    Bootstrap      bool      `json:"bootstrap,omitempty"`
    LifecycleState string    `json:"lifecycle_state,omitempty"` // NEW: "active" | "evicted"
}
```

`omitempty` keeps the file clean when value is `"active"` (the dominant case); old pyry binaries reading new files ignore the field via the lenient JSON decoder. New pyry reading old files defaults the missing field to `"active"`.

The string form (`"active"` / `"evicted"`) is used on disk; the in-memory form (`lifecycleState` enum) lives in Go. A small `parseLifecycleState` / `String()` pair bridges them.

## Concurrency model

Goroutines per Session:
1. **Lifecycle goroutine** (the body of `Session.Run`) — owns state transitions, idle timer, supervisor lifecycle.
2. **Inner supervisor goroutine** (per active period) — wrapper that runs `s.sup.Run(subCtx)` and pipes the result to `runErr`. Drained at the end of each active period.
3. **Attach detach-watcher goroutines** — one per active attach, decrement `attached` when bridge done fires.

Mutexes:
- `Pool.mu` — protects `pool.sessions`, `pool.bootstrap`, registry persistence.
- `Session.lcMu` — protects `lcState`, `attached`, `activeCh`, `lastActiveAt` (when read for registry snapshot).
- `Supervisor.mu` — unchanged; protects `state`.

Lock order: **Pool.mu → Session.lcMu**. `transitionTo` releases `Session.lcMu` before calling `Pool.persist` to avoid the reverse path.

Channels:
- `s.activateCh` (buffered 1) — non-blocking signal from Activate to lifecycle goroutine.
- `s.activeCh` (closed-on-active) — broadcast wakeup to Activate waiters.
- `runErr` (buffered 1, per active period) — supervisor.Run's return value.

Shutdown sequence:
1. Outer ctx cancelled (SIGINT/SIGTERM).
2. Lifecycle goroutine in either `runActive` or `runEvicted` selects on `<-ctx.Done()`.
3. If active: cancels inner supervisor ctx, drains `<-runErr`, returns `ctx.Err()`.
4. If evicted: returns `ctx.Err()` immediately.
5. `Pool.Run` (today: `bootstrap.Run`) returns. `cmd/pyry` proceeds with control-server shutdown.

In-flight `Session.Attach` callers: their bridge `<-done` fires when their conn closes. The detach-watcher goroutine decrements `attached`. No leak.

In-flight `Session.Activate` callers during shutdown: their `<-ctx.Done()` fires (Activate takes a ctx); they return `ctx.Err()`. No deadlock.

## Error handling

| Failure | Handling |
|---|---|
| Registry save fails on transition | Returned from `Session.Run` → bubbles out of `Pool.Run` → `cmd/pyry` exits with error. Matches existing fatal-on-save posture from #34. |
| Supervisor.Run returns spontaneously (claude crashed, supervisor gave up) | Treat as evict trigger — transition to evicted state. The supervisor doesn't currently give up (retries forever per Open Question), so this branch is mostly defensive. |
| Activate ctx cancelled before active | Return `ctx.Err()` to caller; lifecycle goroutine still finishes the transition (Activate is fire-and-wait, not fire-and-control). |
| Attach on evicted session without prior Activate | `bridge.Attach` would block on input until claude eventually appears — but claude won't appear without Activate. The control plane is the only attach caller and it always Activates first. Document this contract on `Session.Attach`. |

## Testing strategy

Reuse the existing `/bin/sleep` fake-claude pattern from `internal/sessions` (see lessons.md "Test helpers across packages"). No new test infrastructure.

**`internal/sessions/session_test.go` additions:**

1. `TestSession_IdleEvictionFires` — short timeout (e.g. 200ms), `Run` in a goroutine, no Attach, expect transition to evicted (poll `LifecycleState`), claude process exits.
2. `TestSession_IdleEvictionDeferredWhileAttached` — short timeout, Attach synthetic in/out, verify state stays active for at least 2× timeout, then close the synthetic input → state goes evicted.
3. `TestSession_ActivateRespawns` — start session, evict, call `Activate(ctx)`, expect state active and supervisor's `State().Phase == PhaseRunning`.
4. `TestSession_ActivateNoOpWhenActive` — `Activate` on an active session returns immediately.
5. `TestSession_ActivateCtxCancellation` — Activate with already-cancelled ctx returns `ctx.Err()`.
6. `TestSession_ShutdownFromActive` and `TestSession_ShutdownFromEvicted` — outer ctx cancel returns `ctx.Err()` from `Run` cleanly.

**`internal/sessions/registry_test.go` additions:**

7. `TestRegistry_LifecycleStateRoundTrip` — write entry with `LifecycleState: "evicted"`, reload, verify field preserved.
8. `TestRegistry_LifecycleStateBackwardsCompat` — write file without the field (simulating old pyry), reload, verify defaults to "active".

**`internal/sessions/pool_test.go` additions:**

9. `TestPool_BootstrapWarmStartsEvicted` — pre-write registry with bootstrap entry `lifecycle_state: "evicted"`, `New()`, verify session's state is evicted at construction (no supervisor spawned).
10. `TestPool_ParityWhenIdleDisabled` — `IdleTimeout: 0`, run for several seconds, verify no transition occurs (regression guard for the AC's parity claim).

**`internal/control/server_test.go` additions:**

11. `TestServer_AttachActivatesEvictedSession` — fake Session whose Activate is a counter, Attach succeeds → counter incremented exactly once.
12. `TestServer_AttachActivateError` — fake Session whose Activate returns error → wire response surfaces `attach: activate: <err>`.

## Manual smoke (recorded for the PR description)

1. Build: `go build -o pyry ./cmd/pyry`
2. Run with short timeout: `./pyry -pyry-idle-timeout 30s` in foreground mode (or service mode + `pyry attach`).
3. Send a message: type a prompt to claude, get a reply.
4. Detach (Ctrl-B d in service mode) or just stop typing in foreground mode.
5. Wait 35s. Verify claude PID is gone: `ps -p <pid>` returns nothing. Verify JSONL exists: `ls ~/.claude/projects/<encoded-cwd>/*.jsonl`.
6. Verify registry: `cat ~/.pyry/pyry/sessions.json | jq '.sessions[0].lifecycle_state'` → `"evicted"`.
7. Reattach (or send another message in foreground): `pyry attach`. Expect 2-15s pause as claude respawns, then a prompt.
8. Ask claude to recall what was discussed earlier. Verify it references the prior conversation.
9. `pyry stop`; verify clean exit (`pyry status` errors with connection refused or similar).

## Open questions (for the dev to flag if they hit them)

1. **Supervisor graceful stop.** Today inner-ctx-cancel triggers SIGKILL via `exec.CommandContext`. JSONL final-line truncation is theoretically possible. Out of scope for #40 but noted for a follow-up if observed in practice.
2. **`pyry status` on evicted sessions.** Reports `PhaseStopped`, which is correct but not very informative. Could add a session-level lifecycle field to the status payload in a separate ticket. Wire-format change → not in #40.
3. **`pyry attach` UX during respawn.** The 2-15s pause is silent. The user sees nothing until claude's first PTY output. Could add a "waking session..." stderr message on the client side, but that's polish, not in scope.

## Why M, not split

I considered splitting at two seams:

**Seam A — primitive (introduce Activate/Evict + state machine) vs. policy (idle timer + persistence).** The primitive without persistence violates the AC ("registry records lifecycle_state as evicted"); the primitive without the timer is dead code that nobody calls; the timer without Activate is a one-way trip into evicted-and-stuck. Each child slice is incoherent on its own and would force the dev to invent placeholder consumers that the next ticket immediately rewrites.

**Seam B — in-memory state machine (everything except registry) vs. persistence.** The in-memory child is testable but doesn't deliver the AC; the persistence child is a 30-line addendum that doesn't meet the "child stands alone" bar. It would just be churn.

The work is genuinely one state machine with one persist seam and one external trigger (Activate). The line count (~140-160 production) is at the M ceiling but the dev's edit fan-out is tight (~5 files), the supervisor is unchanged, and the existing test infrastructure (sleep-as-fake-claude) is reused with no new scaffolding. M with the full surface in one ticket gives the dev one design to hold rather than two halves to coordinate.
