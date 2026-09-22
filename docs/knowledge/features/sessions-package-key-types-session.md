# `Session`

```go
type Session struct { /* id, sup, log, lifecycle fields */ }

func (s *Session) ID() SessionID
func (s *Session) State() State
func (s *Session) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
func (s *Session) Runner() Runner // #1077; runner-type-specific calls (e.g. interrupt) type-switch on this
func (s *Session) LifecycleState() lifecycleState
func (s *Session) Activate(ctx context.Context) error
func (s *Session) Evict(ctx context.Context) error
func (s *Session) Run(ctx context.Context) error
```

One supervised claude instance. As of 1.2c-A each Session owns a lifecycle goroutine driving an `active ↔ evicted` state machine (see [idle-eviction.md](idle-eviction.md)).

- `State()` returns the supervisor's snapshot — same safe-from-any-goroutine contract. In `evicted`, the supervisor reports `PhaseStopped` (faithful — it really isn't running).
- `WriteUserTurn(ctx, conversationID, payload)` (#322) is a one-line passthrough to the underlying runner. Consumed by the `send_message` handler via the `handlers.TurnWriter` interface — the interface is declared in `internal/relay/handlers` (not here) so the handler sub-package stays free of `internal/sessions` imports; `*Session` satisfies it structurally. No tests at this level (contract is exercised by the runner's own tests; a broken delegation fails to compile).
- `LifecycleState()` returns the current lifecycle state under `lcMu`. Used by tests and (eventually) richer status payloads.
- `Activate(ctx)` moves an evicted session to `active`, blocking until the supervisor has started (or `ctx` cancels). It has no early-return for "already active" — an already-active call still falls into the same wait, which is why an already-cancelled `ctx` needed its own entry guard (#1805, below) rather than being caught by a no-op short-circuit. Idempotent under concurrent calls. An already-cancelled or expired `ctx` returns `ctx.Err()` immediately, before any re-activation is requested — no `activateCh` signal, no supervisor start, regardless of the session's current state.
- `Evict(ctx)` moves an active session to `evicted`; used by the cap-policy spawn path. Unlike the idle timer, it never defers for an open turn — the cap is a hard limit, and a running turn is killed with the child.
- `Run(ctx)` blocks until ctx cancellation, driving the lifecycle loop (`runActive` ↔ `runEvicted`). The supervisor is started on an inner ctx during active periods and drained when the ctx cancels.

The interactive attach/bridge path (`Attach`, `Supervisor()`/`Bridge()`, the per-attach `attached` counter this section used to describe) was deleted with the terminal runner in #1348; `*Session` no longer exposes it. `Runner()` (#1077) is the current escape hatch to the concrete runner for type-switched calls (e.g. `cmd/pyry`'s interrupt routing). Idle-eviction deferral, which `attached > 0` used to drive, now reads the optional `Config.TurnBusy` hook instead — see [idle-eviction.md](idle-eviction.md).

The `log` field is written by the constructor but not read in 1.0 (no per-session log lines yet — see [parent ADR](../decisions/003-session-addressable-runtime.md)). Kept on the struct so 1.1 can attach without reshaping.
