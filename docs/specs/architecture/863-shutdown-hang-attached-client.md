# Spec #863 — Daemon shutdown hangs while a control client is attached

**Ticket:** control: daemon shutdown hangs while a client is attached; `Serve` waits on a detach that never comes
**Size:** S · **Label:** bug · not security-sensitive (control Unix-socket lifecycle; no new untrusted surface)
**Production files touched:** `internal/control/server.go`, `internal/supervisor/bridge.go`, `internal/sessions/session.go` (3)

---

## Files to read first

- `internal/supervisor/bridge.go:220-263` — `Bridge.Attach`: the input-pump goroutine. The two block points (`in.Read` at 235, `b.in <- chunk` at 239) and the exit cleanup (252-260) that clears `attached`/`output`. This is the primary edit site.
- `internal/supervisor/bridge.go:53-97` — `Bridge` struct + `NewBridge`. Where the new terminal-shutdown signal field is declared and initialised. Note the existing per-iteration `iterCancel` (`cancelMu`-guarded, re-created by `BeginIteration`) — the new signal is **distinct** from it (terminal, never re-created).
- `internal/supervisor/bridge.go:139-162` — `BeginIteration`/`EndIteration`. Confirms `iterCancel` is per-iteration and closed on **every** child exit (including restarts), which is why it cannot carry the shutdown abort.
- `internal/control/server.go:355-394` — `Serve`: the `handleWG.Wait()` + `s.streamingWG.Wait()` that hangs. No code change here, but this is the symptom site.
- `internal/control/server.go:396-418` — `Close`: currently closes only the listener + socket, holding `s.mu` across the whole body (`defer s.mu.Unlock()`). Must be restructured to also close tracked streaming conns **after releasing `s.mu`**.
- `internal/control/server.go:762-875` — `handleAttach`: the handoff point (861-874) where the streaming goroutine is spawned on `streamingWG`. Conn registration is added here; the detach-watcher goroutine (868-873) is where deregistration is added.
- `internal/control/server.go:198-223` — `Server` struct: where the conn-set field is declared. `NewServer` (253-269) initialises it.
- `internal/sessions/session.go:239-263` — `Session.Attach`: the wrapper that delegates to `s.bridge.Attach` and closes `wrapped` when `bridgeDone` fires. Unchanged, but shows the `done` chain the fix relies on.
- `internal/sessions/session.go:360-421` — `Session.Run`: the active↔evicted loop. The `defer` bridge-shutdown hook goes at the **top of this function** (fires once on permanent termination). Read `runActive:457-521` to confirm `s.sup.Run` returns on **eviction** too — this is why the hook must NOT live in `supervisor.Run`.
- `internal/sessions/pool.go:1065-1083` — `supervise`: `g.Go(func() error { return sess.Run(gctx) })`. Proves `Session.Run` is invoked exactly once per session (bootstrap + minted), so a `defer` there fires exactly once, on shutdown (gctx cancel) or removal.
- `internal/control/attach_test.go:527-624` — `TestServer_StopWhileAttached`: the existing test that closes the client conn from the **test side** (613) and explicitly notes it cannot reproduce the production cascade. AC-4 requires this test still passes; AC-5 requires a new test that does NOT close the conn from the test side.
- `docs/lessons.md` § "Bridge input pump must be scoped per-iteration" — the per-iteration cancel machinery the send-abort extends (referenced by the ticket's technical notes).

---

## Context

When the daemon shuts down (SIGTERM, `pyry stop`, or the `pyry update` restart) while a control client is attached, the process never exits on its own. The `Bridge.Attach` input-pump goroutine loops `in.Read(conn)` and only exits when that read fails — but nothing server-side closes the conn, so an idle client's read blocks forever. `handleAttach`'s detach-watcher (tracked on `streamingWG`) blocks on `<-done`; `Serve` blocks on `streamingWG.Wait()`; `main.go` blocks on `<-ctrlDone`. The service manager escalates to SIGKILL — every stop-while-attached becomes a dirty exit.

There are **two** block points in the one pump goroutine, and they need **two** different unblock mechanisms:

1. **Read-parked** — pump blocked on `in.Read(conn)` (idle client). Unblocked by closing the conn.
2. **Send-parked** — pump blocked on `b.in <- chunk` (child in restart backoff, buffer full, supervisor not draining). Closing the conn does **not** unblock a channel send — the send needs its own cancellation.

## Design — abort the block point on the resource each layer owns

The fix is a clean three-layer split. Each layer aborts the parking spot that sits on a resource **it** owns; no layer reaches across the seam, so the frozen `Attach` signature (`(in, out) (done, err)`) is untouched — critical, because it is implemented/consumed across `Bridge`, `Session`, the control `Session` interface, and ~5 test files. Changing it would cascade ~15 call sites (the #29 failure mode).

| Layer | Owns | Block point it aborts | Trigger |
|-------|------|-----------------------|---------|
| `control.Server` | the `net.Conn` | `in.Read` (read-parked) | `Server.Close` closes every tracked streaming conn |
| `supervisor.Bridge` | `b.in` channel | `b.in <- chunk` (send-parked) | `Bridge.Shutdown` closes a terminal signal the send selects on |
| `sessions.Session` | the `Bridge` handle + shutdown-vs-eviction knowledge | (wires the two) | `Session.Run` `defer`s `Bridge.Shutdown` |

Both triggers fire on ctx cancel: `Serve`'s existing ctx-watcher calls `Close`; `pool.Run`'s errgroup cancel makes each `Session.Run` return, firing its deferred `Bridge.Shutdown`. Whichever spot the pump is parked at, the matching trigger unblocks it, the pump exits, the existing `done` chain fires, `streamingWG` drains, `Serve` returns.

### 1. `supervisor/bridge.go` — terminal send-abort

- **New field** on `Bridge`: a terminal shutdown signal channel (e.g. `shutdownCh chan struct{}`) plus a `sync.Once` (e.g. `shutdownOnce`). Allocated in `NewBridge`. **Distinct from `iterCancel`**: `iterCancel` is per-iteration and closed on every child exit (restarts included); `shutdownCh` is closed exactly once, at daemon/session termination, and never re-created. Using `iterCancel` for the send-abort would wrongly tear the attach down on every restart backoff.
- **New method** `func (b *Bridge) Shutdown()`: closes `shutdownCh` via the `Once` (idempotent, safe from any goroutine, no return value). Document it as terminal — "no further input will ever be drained; release a producer parked on the input channel." Prefer the name `Shutdown` over `Close` to avoid `io.Closer` (`Close() error`) connotations.
- **Modify the pump's send** (bridge.go:239) from a bare `b.in <- chunk` to a select against `shutdownCh`; on the shutdown arm, stop pumping and fall through to the **existing** cleanup (clear `output`/`attached`). Contract: the pump exits (so `doneCh` closes and `Attached()` reports false) whether it was parked on the send or on the read. The dropped chunk is acceptable — the daemon is terminating.
- Do **not** touch `Read`, `Write`, `BeginIteration`, `EndIteration`, or the resize seam.

Pump shape after the change (contract sketch, not final code):

```
for {
    n, rerr := in.Read(buf)          // unblocked by conn.Close (Server layer)
    if n > 0 {
        select {
        case b.in <- chunk:
        case <-b.shutdownCh:          // unblocked by Bridge.Shutdown (Session layer)
            break pump
        }
    }
    if rerr != nil { /* existing EOF-vs-error log */ break pump }
}
// existing cleanup: clear output/attached under b.mu
```

### 2. `sessions/session.go` — wire shutdown to the bridge

- At the **top of `Session.Run`**, add `defer` that calls `s.bridge.Shutdown()` guarded by `if s.bridge != nil` (foreground sessions have no bridge). `Session.Run` is scheduled once per session by `Pool.supervise` (`g.Go(sess.Run)`) and returns only on permanent termination — gctx cancel (shutdown) or `Pool.Remove` (removal). Eviction stays **inside** the loop (`runActive`→`runEvicted`), so the defer does **not** fire on eviction. This is the only layer that both holds the `Bridge` and can distinguish shutdown from eviction.
- No other change. `Session.Attach` and the `done`/`wrapped` chain are untouched.

**Why not `supervisor.Run`:** `runActive` runs `s.sup.Run(subCtx)` and cancels `subCtx` on eviction (session.go:478/511/516), so `supervisor.Run` returns on eviction. The `Supervisor` + `Bridge` persist across evict→reactivate, so a `Shutdown` there would poison the bridge for the next attach.

### 3. `control/server.go` — track and close streaming conns

- **New field** on `Server`: a streaming-conn set (e.g. `streamConns map[net.Conn]struct{}`), guarded by the existing `s.mu`. Initialise in `NewServer`.
- **Register at handoff** in `handleAttach` (at the streamingWG spawn, ~861): under `s.mu`, add `conn` to the set and read `s.closed` in the same critical section. Release the lock. If `s.closed` was already true (shutdown raced ahead of this handoff), close `conn` immediately — otherwise this freshly-attached, read-parked pump would never be unblocked (its conn was accepted before `Close` snapshotted, so `Close` won't close it). This closes the accept-vs-shutdown race deterministically.
- **Deregister in the detach-watcher** (the streamingWG goroutine, 868-873): after `<-done`, remove `conn` from the set **under `s.mu`**, then close `conn` **outside** the lock (as today). Double-close of a `net.Conn` is safe.
- **`Close` closes tracked conns**: restructure so the `net.Conn` closes happen **after** `s.mu` is released. Under `s.mu`: set `closed`, close `closedCh`, close the listener, remove the socket, and snapshot the streaming-conn set into a local slice. Release `s.mu`. Then range the snapshot and `_ = c.Close()` on each. Keep `Close` idempotent (the `s.closed` early-return stays).

Lock-order rule (ticket's noted hazard): **never hold `s.mu` across a `conn.Close()`.** Both `Close` and the detach-watcher take `s.mu` only to mutate the map, then close conns outside the lock. Consistency under `s.mu` between `closed` and the conn set is what makes the handoff race airtight: a handoff either lands in the set before `Close`'s critical section (Close closes it) or observes `closed == true` after (handoff closes it).

## Concurrency model

- **Goroutines unchanged in count/shape.** Still one pump per attach (bridge), one detach-watcher per attach (control, on `streamingWG`), one ctx-watcher (Serve), N session lifecycle goroutines (pool errgroup).
- **Shutdown sequence** (ctx cancel):
  - `Serve` ctx-watcher → `Server.Close` → closes listener (Accept fails) **and** every tracked streaming conn → read-parked pumps get read errors.
  - `pool.Run` errgroup cancel → each `Session.Run` returns → deferred `Bridge.Shutdown` → `shutdownCh` closed → send-parked pumps abort their send.
  - Either way each pump exits → cleanup clears `attached`/`output` → `doneCh` closes → `Session.Attach` wrapper decrements `attached`, closes `wrapped` → `handleAttach` detach-watcher wakes on `<-done`, closes conn, deregisters, `streamingWG.Done()`.
  - `Serve`'s `handleWG.Wait()` + `streamingWG.Wait()` return → `Serve` returns → `ctrlDone` fires → `main.go` proceeds to exit.
- **`main.go` ordering already guarantees the happens-before** (no change): `pool.Run(ctx)` returns only after every `Session.Run` returned (all `Bridge.Shutdown` executed), then `ctrl.Close()`, then `<-ctrlDone`. So by the time `main` waits on `ctrlDone`, both triggers have fired.
- **Locks:** the new conn set is guarded by the existing `s.mu` (no new mutex on the server). The bridge's `shutdownCh` is closed via `sync.Once` (no lock, close-once). `shutdownCh` is leaf-only — never held with `b.mu`/`cancelMu`/`leftMu`/`ptyMu`.

## Error handling / failure modes

- **Double conn close** (Server.Close + detach-watcher, or handoff-race close + Close): `net.Conn.Close` on an already-closed conn returns an error that is discarded (`_ =`). Harmless.
- **`Bridge.Shutdown` called more than once** (defensive; `Session.Run` runs once, but guard anyway): `sync.Once` makes repeat calls no-ops.
- **Send-parked chunk is dropped** on the shutdown arm: intended — the daemon is terminating and there is no drainer. Not a data path that matters at shutdown.
- **Attach after `Bridge.Shutdown`** (terminal session, should not happen — no session to attach to): the pump's first send aborts immediately; `Attached()` returns false. Acceptable terminal-state behaviour; not an AC.
- **Normal detach while send-parked** (client closes mid-send, not at shutdown): out of scope — no AC, unobserved, and only leaks one goroutine until shutdown (which now cleans it up). Do not add machinery for it (evidence-based fix selection).

## Testing strategy

Scenarios (developer writes them in the project's table-driven / stdlib idiom):

- **`bridge_test.go` — send-abort (AC-3, unit):** construct a `Bridge`, `BeginIteration`, `Attach` a reader that keeps yielding bytes, and do **not** drain `b.in`. Once the pump is parked on the send (buffer full — fill `inputChunkBufferSize`+1), call `Bridge.Shutdown()`; assert the pump exits within a bounded window (`Attached()` becomes false, and the `done` channel from `Attach` closes). This is the case "closing the conn alone does not cover."
- **`bridge_test.go` — `Shutdown` idempotent:** calling `Shutdown()` twice does not panic; a `Shutdown` with no attach in flight is a no-op.
- **`bridge_test.go` — restart does not abort attach (regression guard):** an attach survives a `EndIteration`/`BeginIteration` cycle (confirms `shutdownCh` is not `iterCancel`). Assert `Attached()` stays true across the iteration boundary and only clears after `Shutdown` or conn EOF.
- **`control` `attach_test.go` — production cascade (AC-5, the headline regression test):** stand up a `Server` (real `supervisor.NewBridge` via `sessionResolverWith(bridge.Attach)`, matching the existing tests), `Serve` on a goroutine, dial + attach a client, confirm `bridge.Attached()`, then **cancel the ctx** and assert `Serve` returns within a bounded window **without the test closing the client conn**. Drives the read-parked path through `Server.Close` closing the tracked conn.
- **`control` `attach_test.go` — AC-4 preserved:** `TestServer_StopWhileAttached` (which closes the conn from the test side) still passes unchanged; a normal client-initiated detach still fires `done` / clears `Attached()`.
- **`control` `server_test.go` (or attach_test.go) — handoff-vs-Close race:** after `Close`, an attach handshake that reaches handoff still tears down (the `wasClosed` branch closes the conn). Lower priority; include if cheap with the existing harness.
- **Session-level wiring (AC-2 end-to-end, if the sessions harness supports it cheaply):** the sessions package already drives `sess.Run(ctx)` with real bridges (`pool_cap_test.go:93`, `pool_test.go:826`). If a lightweight construction exists, assert that cancelling a running `Session`'s ctx results in `Bridge.Shutdown` having fired (e.g. a send-parked pump on that session's bridge unblocks). If it requires a live claude child, rely on the bridge-unit + control-regression coverage instead and note the gap.
- Run `go test -race ./internal/control/... ./internal/supervisor/... ./internal/sessions/...` — the shutdown paths are concurrency-sensitive; `-race` on the new conn-set access under `s.mu` and the `shutdownCh` close/select is the key check.

## Open questions

- **Bounded-window assertion value** for the regression tests: reuse the existing 2s poll deadline pattern from `TestServer_StopWhileAttached` (attach_test.go:616) for consistency. No new constant needed.
- **Method name** `Bridge.Shutdown` vs `Bridge.CloseInput`: `Shutdown` recommended (terminal, no error return, reads clearly against `BeginIteration`/`EndIteration`). Developer may pick `CloseInput` if it reads better at the `Session.Run` call site — cosmetic, not contractual.
