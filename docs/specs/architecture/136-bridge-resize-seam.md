# #136 — Bridge.Resize seam + apply handshake Cols/Rows

## Files to read first

- `internal/control/protocol.go:39-64` — `AttachPayload` struct + the Phase-0 caveat that explains why `Cols/Rows` are dropped today. The "handshake" half of that caveat is the line being deleted; the "live SIGWINCH" half stays.
- `internal/supervisor/supervisor.go:236-280` — service-mode block in `runOnce`: where `ptmx` is allocated, where `BeginIteration`/`EndIteration` already bracket the iteration, and the lines 248-250 caveat to be rewritten.
- `internal/supervisor/bridge.go:45-140` — `Bridge` struct + iteration-scoped fields/methods (`iterCancel`, `BeginIteration`, `EndIteration`). The new PTY field and `SetPTY/Resize` methods follow the same `cancelMu`-style locking pattern.
- `internal/supervisor/winsize.go:40-57` — the existing `pty.Setsize` callsite. Reuse the same call shape (`pty.Setsize(f, &pty.Winsize{...})`) in the new `Bridge.Resize`.
- `internal/control/server.go:35-54` — the `Session` and `SessionResolver` interfaces. The `Session` interface gets one new method; `SessionResolver` is unchanged.
- `internal/control/server.go:347-411` — `handleAttach`. The new resize call lands between `Activate` (after PTY exists) and `Attach` (before bridge handoff), guarded by the zero-value check.
- `internal/control/server_test.go:20-76` — `fakeSession` and `fakeResolver`. `fakeSession` gets one new field (recorded resize calls) and a new method.
- `internal/control/attach_test.go:286-333` — `TestServer_AttachIgnoresGeometryToday`. Rename + rewrite to assert the spy seam IS invoked; add a sibling test for the zero-value no-op path.
- `internal/sessions/session.go:62-154` — `Session` struct + `Attach` method. The new `Resize` method mirrors `Attach`'s "delegate to bridge if non-nil, return sentinel otherwise" shape; reuse `ErrAttachUnavailable` rather than minting a new error.
- `internal/supervisor/bridge_test.go:1-100` — existing bridge tests. The new supervisor-side `Resize` test follows the same `NewBridge(nil)` + direct method-call pattern; uses `pty.Open()` like `internal/e2e/attach_pty.go:63-73`.

## Context

Today `internal/control` accepts `Cols`/`Rows` in `AttachPayload` but the supervisor has no API to apply them. Three places document the gap (`protocol.go`, `supervisor.go`, `attach_test.go`'s `TestServer_AttachIgnoresGeometryToday`). This ticket adds a typed resize seam — `Bridge.Resize(rows, cols uint16) error` — and uses it from the control server's attach handler to honor handshake geometry. The wire-protocol resize message and the live-resize applier are deferred to #137; the client-side SIGWINCH handler is deferred to #133.

## Design

### Where the seam lives

The seam is a method on `*Bridge`. Three reasons:

1. The control server already reaches the supervisor through the `Session` → `Bridge` chain (the `Attach` method does this). A resize seam on the same chain is the natural shape — no new ownership question, no callbacks to wire from `Run`.
2. `Bridge` already brackets each `runOnce` iteration via `BeginIteration`/`EndIteration`. The same pair of hook points naturally registers/clears the per-iteration `*os.File` for the PTY.
3. Foreground mode has no bridge by construction — a resize coming in from a non-existent control client is unreachable, and the existing SIGWINCH watcher in `winsize.go` handles geometry there. No code path needs to converge.

### Bridge changes

Add one new field and two new methods:

```go
type Bridge struct {
    // ... existing fields ...

    // ptyMu guards ptmx. Held briefly across pty.Setsize so a concurrent
    // SetPTY/ClearPTY can't swap the file mid-call.
    ptyMu sync.Mutex
    ptmx  *os.File // current PTY master, or nil between iterations
}

// SetPTY registers (or clears, when f is nil) the PTY master for the
// current runOnce iteration. Subsequent Resize calls target this fd.
// runOnce calls SetPTY(ptmx) after pty.Start succeeds and SetPTY(nil)
// before returning.
func (b *Bridge) SetPTY(f *os.File) { ... }

// Resize applies the given window size to the registered PTY master via
// pty.Setsize. Returns nil silently when no PTY is registered (between
// iterations or in foreground mode where no Bridge exists at all — though
// callers in that mode never get here). Errors from pty.Setsize are wrapped
// and returned for the caller to log; the control plane does not fail the
// attach on resize errors.
func (b *Bridge) Resize(rows, cols uint16) error { ... }
```

The `uint16` typing matches `pty.Winsize` (the underlying ioctl struct) — the boundary conversion from the wire `int` happens in the control server, not here.

The "no PTY registered → silent nil" branch handles a narrow race: between `EndIteration` (child exit) and the next `BeginIteration` (respawn), an in-flight resize from a control client targets nothing. Returning nil rather than an error avoids decorating the attach handler with timing-specific logging; the next attach from the same client picks up the next iteration's PTY anyway.

### Supervisor (runOnce) changes

In the service-mode block:

```go
if s.cfg.Bridge != nil {
    s.cfg.Bridge.BeginIteration()
    s.cfg.Bridge.SetPTY(ptmx)
    // ... existing input/output goroutine setup ...

    waitErr := cmd.Wait()
    _ = ptmx.Close()
    s.cfg.Bridge.SetPTY(nil)
    s.cfg.Bridge.EndIteration()
    // ... existing drain loop ...
}
```

`SetPTY(nil)` runs **before** `EndIteration` so a Resize that races with iteration teardown sees nil (silent no-op) rather than a closed fd. Keep it additive — no signature changes to `BeginIteration`/`EndIteration`.

### Sessions package changes

Add `Resize` to `*sessions.Session` mirroring `Attach`'s shape:

```go
// Resize applies the given window size to the session's PTY via the
// bridge. Returns ErrAttachUnavailable when the session has no bridge
// (foreground mode); the control plane's attach handler treats this as
// a no-op (log debug, continue), since foreground mode has its own
// SIGWINCH watcher.
func (s *Session) Resize(rows, cols uint16) error {
    if s.bridge == nil {
        return ErrAttachUnavailable
    }
    return s.bridge.Resize(rows, cols)
}
```

No lifecycle locking: `Resize` doesn't touch `lcMu`, doesn't bump `lastActiveAt`, doesn't interact with the active↔evicted state machine. It's a pass-through to a method whose own internal mutex serializes against the iteration boundary.

### Control package changes

Extend the `Session` interface:

```go
type Session interface {
    State() supervisor.State
    Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
    Activate(ctx context.Context) error
    Resize(rows, cols uint16) error // new
}
```

Two implementers: `*sessions.Session` (production) and `*fakeSession` (tests). No third party.

In `handleAttach`, between the existing `Activate` block and the `Attach` call:

```go
if err := sess.Activate(activateCtx); err != nil { /* unchanged */ }

// Apply handshake geometry. Zero in either dimension is the protocol
// "unknown / don't touch" sentinel — see AttachPayload omitempty tags.
// We narrow int → uint16 here; values > 65535 are clamped silently
// (a real terminal will never report dimensions that large).
if req.Attach != nil && req.Attach.Cols > 0 && req.Attach.Rows > 0 {
    cols := clampUint16(req.Attach.Cols)
    rows := clampUint16(req.Attach.Rows)
    if err := sess.Resize(rows, cols); err != nil &&
        !errors.Is(err, sessions.ErrAttachUnavailable) {
        s.log.Warn("control: attach geometry resize failed", "err", err, "rows", rows, "cols", cols)
    }
}

done, err := sess.Attach(conn, conn)
```

`req.Attach != nil` guard: existing handshake code reads `req.Attach.SessionID` only after verifying non-nil (see line 304-306). The same nil-check applies here — a malformed handshake without an `Attach` payload has no geometry to apply.

`clampUint16(int) uint16`: a tiny helper, package-private, returns `math.MaxUint16` for out-of-range positives. No need to error on it — pathological client sizes aren't worth a wire-level rejection.

`ErrAttachUnavailable` swallow: a foreground-mode session reaches `handleAttach` only as a missing-bridge error from `sess.Attach` itself (existing path). In practice the resize call to a no-bridge session is unreachable from a client that already received an OK ack, but the silent ignore keeps the contract robust.

### Argument order

`Resize(rows, cols uint16)` — rows-then-cols matches `pty.Winsize{Rows, Cols, ...}` field order and minimizes adapter friction at the `pty.Setsize` callsite.

### Caveat rewrites

| File:lines | Action |
|---|---|
| `internal/control/protocol.go:49-59` | Drop the "server discards `Cols/Rows`" sentence. Keep the live-resize / SIGWINCH paragraph (cleared by #137). |
| `internal/supervisor/supervisor.go:248-250` | Drop the "no setter" half. Keep "no SIGWINCH wiring on the server because that belongs to whichever client attaches" — that's still correct: this ticket adds handshake-time application, not live resizes. |
| `internal/control/attach_client.go:25-27` | Unchanged — cleared by #133. |

## Data flow

```
Client (pyry attach)                    Server                          Supervisor
─────────────────────                   ──────                          ──────────
Encode AttachPayload{
  Cols: 200, Rows: 50,
  SessionID: ""
}                       ──────────►  handle: Decode req
                                        │
                                        ▼
                                     handleAttach
                                        │
                                        ├──► ResolveID + Lookup
                                        │
                                        ├──► sess.Activate(ctx)  ───►  Session.Run loop transitions
                                        │                              to active; supervisor.Run
                                        │                              spawns claude in PTY;
                                        │                              Bridge.SetPTY(ptmx)
                                        │
                                        ├──► sess.Resize(50, 200) ──►  Bridge.Resize
                                        │                                   │
                                        │                                   ▼
                                        │                              pty.Setsize(ptmx,
                                        │                                &Winsize{Rows:50, Cols:200})
                                        │                                   │
                                        │                                   ▼
                                        │                              kernel: child sees
                                        │                              SIGWINCH + new dims
                                        │
                                        ├──► sess.Attach(conn, conn) ─► Bridge.Attach
                                        │
                                        ▼
                                     Encode Response{OK: true}
Decode ack ◄────────────────────────────┘
```

## Concurrency model

Three concurrent actors touch `Bridge.ptmx`:

1. **`runOnce`** (one per iteration; serialized by `Run`'s loop) — calls `SetPTY(ptmx)` then `SetPTY(nil)`.
2. **`handleAttach`** goroutines (one per client connection) — call `Bridge.Resize` indirectly via `Session.Resize`.
3. The supervisor's existing iteration teardown (`ptmx.Close`) — runs after `cmd.Wait` returns.

Locking: `Bridge.ptyMu` protects `Bridge.ptmx`. Held in `SetPTY` (write) and across the entire `pty.Setsize` call in `Resize` (read + use). This is critical because:

- A `Resize` that observes `ptmx != nil` and then races with `SetPTY(nil)` on a concurrent iteration teardown could call `pty.Setsize` on a closed fd, returning `EBADF`. With the mutex, the worst case is `Resize` either holds the lock and applies to the still-open ptmx (fine — `ptmx.Close` happens just before `SetPTY(nil)`, which then waits for the lock) or arrives after `SetPTY(nil)`, sees nil, returns silently.
- The lock ordering: `Bridge.ptyMu` is leaf-only (never held while acquiring `Bridge.mu`, `Bridge.cancelMu`, or `Bridge.leftMu`). No deadlock risk.

The `EBADF` window in `runOnce` between `ptmx.Close()` and `SetPTY(nil)` is tightened by the lock: if `Resize` already holds `ptyMu` when `ptmx.Close` runs, `pty.Setsize` returns `EBADF` and the control handler logs a warning. This is acceptable — it can only happen if a client's resize lands within microseconds of the child exiting. The alternative (close-after-clear) opens a longer window where new resizes target the to-be-closed fd; the current order is the safer trade.

## Error handling

| Scenario | Behavior |
|---|---|
| Either `Cols` or `Rows` is 0 in handshake | No `Resize` call. Silent. |
| `Cols`/`Rows` > 65535 | Clamped to `math.MaxUint16`. No error. |
| `Bridge.Resize` called with no PTY registered | Returns nil. Silent. |
| `pty.Setsize` returns an error (e.g. `EBADF` on a closed fd) | Wrapped + returned. `handleAttach` logs at Warn but continues — the attach itself proceeds. |
| `sess.Resize` returns `ErrAttachUnavailable` (foreground session) | Swallowed in `handleAttach`. Foreground mode has its own SIGWINCH watcher. |
| Resize succeeds but child has already exited | The next iteration's PTY starts at default (80×24); the next attach with the same handshake reapplies. Acceptable. |

No path fails the attach. The geometry is best-effort; a stale or wrong window size is recoverable on the user's next keystroke (terminals re-render on input). A failed attach because of a transient `EBADF` would be much worse UX.

## Testing strategy

### Control side (`internal/control/attach_test.go`)

**Replace** `TestServer_AttachIgnoresGeometryToday`:

1. `TestServer_AttachAppliesHandshakeGeometry` — `fakeSession` with a recorded resize history. Send handshake `Cols=200, Rows=50`. After `OK` ack, assert `fakeSession.resizeCalls == [{Rows:50, Cols:200}]`.
2. `TestServer_AttachZeroGeometryNoOp` — table-driven: `{Cols:0,Rows:50}`, `{Cols:200,Rows:0}`, `{Cols:0,Rows:0}`, missing `Attach` payload. For each, assert `len(fakeSession.resizeCalls) == 0` after `OK` ack.
3. `TestServer_AttachResizeErrorDoesNotFailAttach` — `fakeSession.Resize` returns a synthetic error. Send `Cols=80, Rows=24`. Assert `OK: true` ack still returned.

`fakeSession` extension (`server_test.go`):

```go
type fakeSession struct {
    // ... existing fields ...
    resizeCalls []resizeCall // protected by mu
    resizeErr   error
}

type resizeCall struct{ Rows, Cols uint16 }

func (f *fakeSession) Resize(rows, cols uint16) error {
    f.mu.Lock()
    f.resizeCalls = append(f.resizeCalls, resizeCall{Rows: rows, Cols: cols})
    err := f.resizeErr
    f.mu.Unlock()
    return err
}
```

### Supervisor side (`internal/supervisor/bridge_test.go`)

1. `TestBridge_ResizeAppliesToPTY` — `pty.Open()` to get a master/slave pair. `b := NewBridge(nil); b.SetPTY(master)`. Call `b.Resize(40, 100)`. Assert `pty.Getsize(master) == (40, 100)`. `t.Skip` when `pty.Open` is unavailable (matches `internal/e2e/attach_pty.go:73` pattern).
2. `TestBridge_ResizeNoPTYRegistered` — `b := NewBridge(nil)`. Call `b.Resize(40, 100)` without prior `SetPTY`. Assert err is nil.
3. `TestBridge_ResizeAfterClearPTY` — `b.SetPTY(master); b.SetPTY(nil); b.Resize(...)`. Assert err is nil.

### Sessions package

No new tests required. `Session.Resize` is a one-line delegator; if `Bridge.Resize` is covered and `Session.Attach`'s nil-bridge path is covered (it already is — `TestSession_Attach_NoBridge` at `session_test.go:35`), the delegation is implicitly covered. Adding a `TestSession_Resize_NoBridge` is optional; skip unless coverage gates demand it.

## Open questions

1. **Should `Resize` be exposed in `pyry status`?** Today the wire protocol has no place for "current PTY dimensions" in `StatusPayload`. Out of scope here; defer until #137 lands the live-resize message and we have a reason to expose it.
2. **Should `clampUint16` log when it clamps?** Probably not — a client sending `Cols > 65535` is buggy or hostile; logging gives a slow-DoS vector. Silent clamp is fine.
3. **Argument order — rows-then-cols vs cols-then-rows?** Picked rows-then-cols to match `pty.Winsize`. The wire protocol uses cols-then-rows in `AttachPayload` field order, which is jarring. Document the boundary swap in `handleAttach`'s call site (`sess.Resize(rows, cols)` — rows first) and in `Bridge.Resize`'s godoc. Resolution: stick with rows-then-cols in the seam to match `pty`. The wire field order is a separate concern.
