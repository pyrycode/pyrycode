# ADR 008 — `Bridge.Resize` is the supervisor-side resize seam

**Status:** Accepted (ticket #136)

## Context

The control plane accepts `Cols`/`Rows` on `AttachPayload`, but until #136 the
supervisor had no API to apply them — the values were dropped. The wire
caveats in `internal/control/protocol.go` and `internal/supervisor/supervisor.go`
made this explicit. To honour handshake geometry (and to give the future
live-resize verb in #137 a place to land), the control server needs a typed
"resize the supervised PTY" call without reaching into supervisor internals.

The architect's open question: where should the seam live?

| Option | Shape |
|---|---|
| Method on `*supervisor.Bridge` | Resize travels the same `Session → Bridge` chain as `Attach`. |
| Method on `*supervisor.Supervisor` | New chain (`Session → Supervisor`) just for resize. |
| Callback wired by `Run` | `Config.OnResize func(rows, cols)` wired up at construction. |

`*Bridge` does not currently hold the `*os.File` for the PTY (that lives in
`runOnce`'s stack), so any of the three options requires plumbing the fd
somewhere new.

## Decision

The seam is `(*supervisor.Bridge).Resize(rows, cols uint16) error`, with a
matching `SetPTY(*os.File)` registrar that `runOnce` calls per iteration.
`*sessions.Session` exposes a one-line `Resize` that delegates to the bridge
(or returns `ErrAttachUnavailable` in foreground mode). `internal/control`'s
`Session` interface gains the same method; `handleAttach` calls it between
`Activate` and `Attach`.

## Rationale

Three reasons the bridge wins over the supervisor or a callback:

1. **The chain already exists.** Control reaches the supervisor through
   `Session → Bridge` for `Attach`. Putting `Resize` on the same chain costs
   one method per layer; a `Session → Supervisor` chain would mean a second
   parallel path for one method.
2. **Iteration scoping is already there.** `Bridge.BeginIteration` /
   `EndIteration` (ADR 007) bracket each `runOnce`. Registering and clearing
   the per-iteration `*os.File` reuses those hook points; `SetPTY(ptmx)` after
   `pty.Start` and `SetPTY(nil)` before `EndIteration` slot in alongside the
   existing setup.
3. **Foreground mode has no bridge by construction.** A resize from a
   non-existent control client is unreachable in foreground mode, and
   `winsize.go`'s SIGWINCH watcher already handles geometry there. No
   convergence of paths is needed; the seam being bridge-only is the
   correct shape.

## Consequences

- `*Bridge` now owns a leaf-only `ptyMu` mutex over a `*os.File`. Lock order
  is leaf-only (never held while acquiring `mu`, `cancelMu`, or `leftMu`),
  so deadlock risk is zero.
- `Resize` is held across the `pty.Setsize` ioctl (microseconds) so a racing
  `SetPTY(nil)` cannot swap the fd mid-call. The `EBADF` window between
  `ptmx.Close` and `SetPTY(nil)` is tightened by the same lock — a `Resize`
  that holds the mutex when `Close` runs returns `EBADF`, which the control
  handler logs at Warn but does **not** propagate to the client. The reverse
  ordering (`SetPTY(nil)` before `ptmx.Close`) was rejected because it would
  open a longer window where new resizes target the to-be-closed fd.
- `Session.Resize` does **not** touch `lcMu`, does not bump `lastActiveAt`,
  and does not interact with the active↔evicted state machine. It is a pure
  pass-through. Activity tracking is tied to attach state (`attached > 0`),
  not bytes-through-bridge — same shape as the rest of the bridge surface.
- Argument order is **rows-then-cols**, matching `pty.Winsize{Rows, Cols, …}`.
  The wire protocol's `AttachPayload` keeps cols-then-rows for back-compat;
  the boundary swap is a single-line concern in `handleAttach`.
- `int → uint16` clamping (`clampUint16`) is silent; a client sending
  dimensions over 65535 is buggy or hostile, and logging the clamp would
  amplify the noise without giving the operator anything actionable.
- Resize errors never fail the attach. `pty.Setsize` failure is logged and
  the attach proceeds; a stale window size is recoverable on the next
  keystroke (terminals re-render on input). A failed attach due to a
  transient `EBADF` would be much worse UX.
- The seam is reused by #137 for live SIGWINCH propagation: the wire-protocol
  resize message lands on top of `Bridge.Resize` without further supervisor
  changes.

## Related

- ADR 007 — bridge iteration boundaries (the `BeginIteration` / `EndIteration`
  hook points `SetPTY` slots into).
- `docs/knowledge/features/control-plane.md` § Attach: Handshake Geometry
  (#136) — the consumer side.
- `internal/supervisor/winsize.go` — foreground mode's SIGWINCH watcher
  (untouched by #136).
