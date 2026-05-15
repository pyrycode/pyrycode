# ADR 023: `Session.Activate` waits for supervisor PTY readiness

**Status:** Accepted (2026-05-15, ticket [#396](https://github.com/pyrycode/pyrycode/issues/396))
**Phase:** 2.x (relay-routed inbound on idle-evicted bootstrap)
**Refines:** [ADR 005](005-idle-eviction-state-machine.md) (idle eviction state machine), [ADR 013](013-evict-activate-persist-ordering.md) (Evict/Activate wait for registry persist)

## Context

Through ADR 005 and ADR 013, `Session.Activate` returns when the lifecycle goroutine has flipped to `stateActive` AND the registry persist has completed (i.e. when `transitionTo` closes `activeCh`). That close happens **before** `Session.Run`'s next iteration schedules `runActive`, which starts `supervisor.Run` on a goroutine; `runOnce → pty.Start → setPTY(ptmx)` is the sequence that actually binds the PTY master. There is a real ~hundreds-of-ms window where `Activate` has returned but the supervisor's `ptmx` is still nil.

The control-plane attach path masked this race because `Bridge.Attach` queues bytes via `b.in` until `Bridge.SetPTY` is called — early bytes wait in the queue and flush when the PTY arrives. The relay-routed `send_message` path (`internal/relay/handlers/send_message.go`, [#322](https://github.com/pyrycode/pyrycode/issues/322)) writes through `*sessions.Session → Supervisor.WriteUserTurn`, which writes to `ptmx` directly under `ptmxMu`. There is no queue. `Supervisor.WriteUserTurn`'s discard-on-nil-ptmx branch (mirroring `Bridge.Write`'s discard-on-unattached semantics) silently returned `nil`, the dispatcher emitted `send_message.ack`, and the user-visible result was: ack delivered, no claude, no respawn, no log line tying the silence to eviction. Observed on pyrybox 2026-05-15 as a 7.5h Discord/Telegram outage.

The design space:

1. **Strengthen `Activate`'s contract: wait for PTY readiness at the tail.** Add a freshening `ptmxReadyCh` chan in `Supervisor`, expose `WaitForPTY(ctx)`; `Session.Activate` calls it after the existing `<-activeCh` block.
2. **Make `WriteUserTurn` itself wait until the PTY is bound.** Either accept a `context.Context` parameter (breaks the established `TurnWriter` shape and every caller signature) or embed an unbounded internal wait (no caller-facing budget; can hang forever on a busted respawn).
3. **Restructure `transitionTo` to defer the `activeCh` close until `runOnce → setPTY` has bound the PTY.** Couple lifecycle's `transitionTo` to supervisor's `runOnce` more tightly so `activeCh` close *is* the readiness signal.

## Decision

**Option 1: strengthen `Activate`'s contract. After Activate returns nil, the underlying supervisor has a bound PTY and `WriteUserTurn`/`Resize` will reach the live claude.**

The implementation is one new chan field, one new method, two added lines in `Activate`:

```go
// internal/supervisor/supervisor.go
type Supervisor struct {
    // ...
    ptmxMu sync.Mutex
    ptmx   *os.File
    // ptmxReadyCh is closed by setPTY when a non-nil PTY is registered;
    // freshened (re-opened) by setPTY(nil) so subsequent WaitForPTY waiters
    // block again until the next runOnce iteration binds a new PTY.
    ptmxReadyCh chan struct{}
}

func (s *Supervisor) WaitForPTY(ctx context.Context) error {
    s.ptmxMu.Lock()
    ch := s.ptmxReadyCh
    s.ptmxMu.Unlock()
    select {
    case <-ch:        return nil
    case <-ctx.Done(): return ctx.Err()
    }
}
```

`setPTY(non-nil)` closes the chan (idempotent if already closed); `setPTY(nil)` allocates a fresh open chan (idempotent if already open). The chan-snapshot-then-await pattern under the existing `ptmxMu` mirrors `Session.activeCh`.

`Session.Activate`'s body changes by two lines: the existing `<-ch` arm no longer returns nil directly; instead the function returns `s.sup.WaitForPTY(ctx)` after the wait. The state-flip / `activateCh` / `evictedCh` / `transitionTo` choreography is unchanged.

The new contract:

> After `Session.Activate(ctx)` returns nil, the session's supervisor has a bound PTY and `WriteUserTurn`/`Resize` will reach the live claude. `ctx` cancellation while waiting for PTY readiness returns `ctx.Err()`.

## Rationale

### Why option 1 over option 2 (`WriteUserTurn` waits internally)

`WriteUserTurn`'s current signature is `func(conversationID string, payload []byte) error` — no `ctx`. Threading a context through would touch every caller (the `TurnWriter` interface, the handler wiring, the `Session` passthrough, and any future inbound handler that gets added). Embedding an unbounded internal wait would lose the per-handler timeout discipline (`sendMessageActivateTimeout = 30s` for `send_message`, matched to the CLI attach path's budget) and silently turn a busted respawn into a hung handler.

`Activate` is already the named operation for "ensure the session is ready." Strengthening its contract is a single conceptual move; readers already reach for `Activate` when they want a writeable session. Future readiness gates (graceful-stop quiescence, JSONL flush, etc.) layer onto Activate the same way `WaitForPTY` does — strengthen the contract with a tail wait, leave the signalling primitive alone.

### Why option 1 over option 3 (defer `activeCh` close until PTY is bound)

Option 3 entangles the `#202` bootstrap warm-start invariant ([ADR 016](016-bootstrap-ignores-persisted-lifecycle-state.md)). Today's invariant is: a session warm-starting in `stateActive` initialises `activeCh = closedChan()` (no transition pending; disk already consistent). If `activeCh` close is gated on `runOnce → setPTY`, the cold-start initialisation no longer holds — `New` would have to either pre-bind a PTY (impossible; `runOnce` hasn't started) or expose `activeCh` in the not-yet-ready state on warm-start (defeats the warm-start invariant). The ripple touches every `Pool.New` / `Pool.Create` initialiser path.

Option 1 keeps the lifecycle/supervisor seams independent: `transitionTo` continues to signal "in-memory and disk agree" via `activeCh` close, the supervisor signals "PTY is bound" via `ptmxReadyCh` close, and `Activate` composes the two waits. The blast radius is three production files and ~50 prod-line diff (vs. option 3's pool/registry/initialisation surface).

### Why a freshening chan instead of a one-shot

`setPTY(nil)` happens at the start of every `runOnce` iteration boundary (graceful stop, idle eviction, crash respawn). A one-shot chan would leave `WaitForPTY` returning immediately on the second-and-subsequent activations — breaking the contract for any post-eviction respawn. The freshening pattern (close on non-nil, allocate fresh on nil) keeps the chan in lockstep with the actual PTY lifetime. Same shape as `Session.activeCh` / `evictedCh`'s freshening dance in `transitionTo`.

### Why no early-return in `WaitForPTY` even when `ptmx != nil`

The chan-snapshot-then-await already collapses the already-ready case to a non-blocking receive on a closed channel. Adding an explicit `if s.ptmx != nil { return nil }` would either need its own `ptmxMu` round-trip (no net savings) or read `ptmx` racily. The select-on-closed-chan idiom is the cleanest implementation of "fast path when ready, block when not."

### Why the locking invariant is unchanged

`ptmxMu` is leaf-only — never held while acquiring `convMu` or `mu`. `WaitForPTY` captures the chan reference under `ptmxMu`, then awaits unlocked. `setPTY` mutates `ptmx` and `ptmxReadyCh` under the same `ptmxMu`. No new lock; no change to lock order. No new writer paths to `lcState`/`activeCh`/`evictedCh` (the lifecycle goroutine continues to be the only writer); `setPTY` continues to be called only from `runOnce` (supervisor's goroutine).

## Consequences

- **Relay-routed `send_message` no longer silently drops on idle-evicted bootstrap.** `handlers.SendMessage` runs `Activate` with a 30s budget before `WriteUserTurn`; a busted respawn surfaces as `protocol.CodeServerBinaryOffline` with `Retryable=true` instead of a fake ack.
- **CLI attach path benefits too.** `handleAttach`'s existing `Activate` call gets the strengthened contract for free — removes a known foot-gun on the WriteUserTurn-first ordering that the streaming-cursor handler will eventually want.
- **Two non-blocking channel receives added to the steady-state hot path.** `activeCh` already closed + `ptmxReadyCh` already closed = negligible cost on an already-active session.
- **`WriteUserTurn`'s `ptmx == nil` discard branch stays as defence-in-depth.** Lifted from the primary failure surface to a backstop for callers that race past Activate (e.g. an evicted session that races mid-Activate). The branch's silent-return semantics are now a property of "you shouldn't be writing here" rather than the dominant failure mode.
- **`session.idle_eviction` WARN log line at SIGKILL time** is added in the same ticket — operator-facing signal that AC#3 required, paired with the strengthened Activate so the recovery is both automatic and visible.
- **Future inbound carriers reuse the pattern.** Any new handler that writes through `Supervisor.WriteUserTurn` calls `Session.Activate` first with a budget matched to the CLI attach path's 30s window. Mental model for "how long before the binary gives up" stays uniform across attach paths.

## Alternatives considered

- **`WriteUserTurn` accepts ctx and waits internally** — Touches every caller; loses per-handler budget. Rejected.
- **`WriteUserTurn` waits internally without ctx (unbounded)** — Hides a busted respawn behind a hung handler. Rejected.
- **Defer `activeCh` close until `setPTY` binds** — Entangles the `#202` bootstrap warm-start invariant; ripples into every initialiser. Rejected.
- **Add a separate `Session.WaitReady(ctx)` primitive distinct from `Activate`** — Two readiness operations to keep in sync at every caller. Rejected; strengthening the existing Activate contract is the single conceptual move.

## References

- Ticket: [#396](https://github.com/pyrycode/pyrycode/issues/396)
- Spec: [`docs/specs/architecture/396-send-message-respawn.md`](../../specs/architecture/396-send-message-respawn.md)
- Code: `internal/supervisor/supervisor.go` (`Supervisor.ptmxReadyCh`, `setPTY`, `WaitForPTY`, `New`), `internal/sessions/session.go` (`Session.Activate`, `runActive` idle-eviction log), `internal/relay/handlers/send_message.go` (`TurnWriter.Activate`, handler ordering)
- Surfaced by: pyrybox observation 2026-05-15 (7.5h Discord/Telegram silent outage)
- Related ADRs: [005](005-idle-eviction-state-machine.md), [013](013-evict-activate-persist-ordering.md), [016](016-bootstrap-ignores-persisted-lifecycle-state.md)
