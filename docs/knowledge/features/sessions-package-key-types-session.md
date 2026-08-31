# `Session`

```go
type Session struct { /* id, sup, bridge, log, lifecycle fields */ }

func (s *Session) ID() SessionID
func (s *Session) State() supervisor.State
func (s *Session) WriteUserTurn(conversationID string, payload []byte) error
func (s *Session) Supervisor() *supervisor.Supervisor // #311; type-asserted off Runner since #1077
func (s *Session) Bridge() *supervisor.Bridge         // #311; nil in foreground
func (s *Session) LifecycleState() lifecycleState
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
func (s *Session) Activate(ctx context.Context) error
func (s *Session) Run(ctx context.Context) error
```

One supervised claude instance plus the bridge that mediates its I/O in service mode. As of 1.2c-A each Session owns a lifecycle goroutine driving an `active ↔ evicted` state machine (see [idle-eviction.md](idle-eviction.md)).

- `State()` returns the supervisor's snapshot — same safe-from-any-goroutine contract. In `evicted`, the supervisor reports `PhaseStopped` (faithful — it really isn't running).
- `WriteUserTurn(conversationID, payload)` (#322) is a one-line passthrough to `(*supervisor.Supervisor).WriteUserTurn`. Consumed by the `send_message` handler via the `handlers.TurnWriter` interface — the interface is declared in `internal/relay/handlers` (not here) so the handler sub-package stays free of `internal/sessions` / `internal/supervisor` imports; `*Session` satisfies it structurally. No tests at this level (contract is exercised by `internal/supervisor`'s tests; a broken delegation fails to compile).
- `Supervisor()` / `Bridge()` (#311) return the underlying supervisor handle and I/O bridge. Consumed by the assistant-turn bridge wiring in `cmd/pyry` to read `CurrentConversation()` at broadcast time and register an output observer on `Bridge.Write`. `Bridge()` returns `nil` in foreground mode; callers must gate on it. Returned pointers are owned by the session — callers must not retain them past the session's lifetime.
- `LifecycleState()` returns the current lifecycle state under `lcMu`. Used by tests and (eventually) richer status payloads.
- `Attach` returns `ErrAttachUnavailable` when `bridge == nil` (foreground mode); otherwise delegates to `(*supervisor.Bridge).Attach`. `supervisor.ErrBridgeBusy` is propagated **verbatim** so callers' `errors.Is` checks keep working. Bumps `attached` under `lcMu`; the wrapper goroutine spawned here decrements on bridge `done`. **Contract:** callers must `Activate` first — `bridge.Attach` on an evicted session would block on the pipe forever.
- `Activate(ctx)` moves an evicted session to `active`, blocking until the supervisor has started (or `ctx` cancels). It has no early-return for "already active" — an already-active call still falls into the same wait, which is why an already-cancelled `ctx` needed its own entry guard (#1805, below) rather than being caught by a no-op short-circuit. Idempotent under concurrent calls. An already-cancelled or expired `ctx` returns `ctx.Err()` immediately, before any re-activation is requested — no `activateCh` signal, no supervisor start, regardless of the session's current state.
- `Run(ctx)` blocks until ctx cancellation, driving the lifecycle loop (`runActive` ↔ `runEvicted`). The supervisor is started on an inner ctx during active periods and drained when the ctx cancels.

The `log` field is written by the constructor but not read in 1.0 (no per-session log lines yet — see [parent ADR](../decisions/003-session-addressable-runtime.md)). Kept on the struct so 1.1 can attach without reshaping.
