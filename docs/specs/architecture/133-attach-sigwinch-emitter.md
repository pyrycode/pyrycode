# #133 — `pyry attach` forwards SIGWINCH to daemon

## Files to read first

- `internal/control/attach_client.go` (whole file, ~140 lines) — the function being extended. The SIGWINCH watcher hooks in between the handshake ack (line 65) and `copyWithEscape` (line 83). The Phase-0 caveat at lines 25-27 is rewritten by AC#4.
- `internal/control/client.go:68-92` — `SendResize` signature and the "don't retry on transient failure; the next SIGWINCH re-emits" contract that this ticket consumes verbatim.
- `internal/control/protocol.go:32-79` — `VerbResize`, `ResizePayload`. Confirms the wire shape; nothing in this package changes here. The "deferred to #133" parenthetical at line 62 is informational only — it's already accurate.
- `internal/supervisor/winsize.go` (whole file, 58 lines) — the existing daemon-side SIGWINCH pattern in this codebase. The new client-side watcher mirrors its structure: `signal.Notify` + buffered chan(1) + `done`-driven goroutine + `signal.Stop` + close on teardown. Two deltas vs that file: (a) the client-side watcher does **not** prime once at startup (the handshake `AttachPayload` already covers initial sizing — AC#2), and (b) teardown is **synchronous** (stop blocks until the goroutine exits), which is the load-bearing part of AC#3 in this ticket.
- `internal/supervisor/winsize.go:40-57` — `resizeOnce`. Pin the `term.IsTerminal` guard idiom and the rationale for not wrapping `os.Stdin.Fd()` in a fresh `*os.File` (lessons.md / PROJECT-MEMORY 2026-05-02 — finalizer-induced fd reuse races). The new client helper reuses `os.Stdin` directly for the same reason.
- `internal/control/resize_test.go:207-288` — `TestSendResize_RoundTrip` and `TestSendResize_ServerError`. The pattern for the new SIGWINCH→resize unit test: hand-rolled `net.Listen`, server-goroutine that decodes the request, asserts on the decoded payload, encodes a `Response{OK: true}`. The new test reuses this shape; only the trigger differs (a real `syscall.Kill(os.Getpid(), syscall.SIGWINCH)` instead of a direct `SendResize` call).
- `internal/control/attach_test.go:746-864` — `TestAttach_ClientSendsSessionID` and `TestAttach_EmptySessionIDOmittedOnWire`. Same hand-rolled-listener pattern; confirms the wire-shape test idiom established for `Attach`.
- `cmd/pyry/main.go:445-474` — `runAttach`. No changes here; `socketPath` and `sessionID` are already in scope for `Attach` and flow through unchanged.
- `docs/specs/architecture/137-resize-wire-message.md` (whole spec) — what this ticket consumes upstream. The "Data flow" section ends with "Client (e.g. pyry attach SIGWINCH handler — landed by #133)" — that handler is what this ticket lands.
- `docs/specs/architecture/136-bridge-resize-seam.md` (Concurrency model section, ~30 lines) — confirms the server-side concurrency contract the SIGWINCH burst will exercise. Nothing changes here; it's load-bearing context for "is it OK if the user drags the window corner and we emit ten resizes in a row?" → yes, `Bridge.ptyMu` serialises and last-write-wins is the only meaningful semantic.

## Context

`Bridge.Resize` (#136) and the `VerbResize` wire+server-applier (#137) shipped together: the daemon already accepts a resize message on a fresh control connection, swaps cols/rows, clamps to `uint16`, and applies via the seam. The remaining gap is the **trigger** on the client side. `pyry attach` (`internal/control/attach_client.go`) installs no SIGWINCH handler today, so the supervised `claude` only ever sees the handshake's initial geometry; if the user resizes their terminal mid-session, the child renders against stale dimensions until they detach and reattach.

This ticket lands the SIGWINCH→`SendResize` bridge inside `Attach`. After it merges:

- The full live-resize loop works end-to-end: terminal resize → `SIGWINCH` on the client → `VerbResize` on the wire → `pty.Setsize` on the server → child redraws.
- The Phase-0 caveat at `internal/control/attach_client.go:25-27` is removed.
- `#126` (e2e attach-resize coverage) is unblocked — its prerequisite is "all four halves of live resize land in production code", which closes here.

## Design

### One helper, internal to the package, called from `Attach`

The watcher is small enough that it lives next to `Attach` in `attach_client.go`. No new file, no new exported type.

```go
// internal/control/attach_client.go (additions)

// terminalSizeReader reports the current TTY geometry (cols, rows). The
// bool is false when no terminal is attached or when the ioctl fails — in
// that case the watcher emits no resize, mirroring the daemon-side
// resizeOnce guard in internal/supervisor/winsize.go.
type terminalSizeReader func() (cols, rows int, ok bool)

// readTerminalSize is the production reader. It deliberately uses os.Stdin
// directly rather than wrapping a raw fd in a fresh *os.File — see
// supervisor/winsize.go:40-48 for the finalizer-induced fd reuse race that
// motivated the same convention there.
func readTerminalSize() (cols, rows int, ok bool) {
    if !term.IsTerminal(int(os.Stdin.Fd())) {
        return 0, 0, false
    }
    size, err := pty.GetsizeFull(os.Stdin)
    if err != nil {
        return 0, 0, false
    }
    return int(size.Cols), int(size.Rows), true
}

// startWinsizeWatcher installs a SIGWINCH handler. On each signal it reads
// the terminal size and emits a VerbResize via send. Returns a stop
// function that:
//
//  1. Calls signal.Stop on the SIGWINCH channel (so further signals are
//     no-ops to this handler — they may still be delivered elsewhere).
//  2. Closes done so the watcher goroutine breaks out of its select.
//  3. Blocks until the watcher goroutine has actually exited.
//
// Step 3 is the load-bearing guarantee: stop is synchronous. No goroutine
// or signal subscription outlives the call site's defer. This is what
// makes AC#3 ("no signal handler or goroutine leaks across attach/detach
// cycles") true *structurally* rather than *eventually*.
//
// The watcher does NOT prime an initial size at startup. Initial geometry
// flows through the handshake AttachPayload — AC#2 prohibits regressing
// that path.
func startWinsizeWatcher(
    ctx context.Context,
    read terminalSizeReader,
    send func(ctx context.Context, cols, rows int) error,
) (stop func()) {
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGWINCH)
    done := make(chan struct{})
    gone := make(chan struct{})

    go func() {
        defer close(gone)
        for {
            select {
            case <-sigCh:
                cols, rows, ok := read()
                if !ok {
                    continue
                }
                // Best-effort. SendResize errors (transient daemon
                // hiccup, ctx cancelled mid-flight) are silently
                // dropped; the next SIGWINCH retries. This matches the
                // server-side posture (handleResize returns OK even on
                // seam errors) and SendResize's own godoc.
                _ = send(ctx, cols, rows)
            case <-done:
                return
            }
        }
    }()

    return func() {
        signal.Stop(sigCh)
        close(done)
        <-gone
    }
}
```

### Wiring into `Attach`

Three new lines, sandwiched between the ack-success block and the bridge:

```go
// internal/control/attach_client.go — diff against current Attach

// ... (handshake encode + ack decode unchanged) ...

if !resp.OK {
    return errors.New("control: attach ack missing")
}

// NEW: live resize forwarding. Installed only after the handshake has
// succeeded (so resize messages can't reach the server before any session
// is bound) and torn down before Attach returns (synchronous — see
// startWinsizeWatcher).
stopWinsize := startWinsizeWatcher(ctx, readTerminalSize, func(ctx context.Context, cols, rows int) error {
    return SendResize(ctx, socketPath, sessionID, cols, rows)
})
defer stopWinsize()

// Connection is now in raw-bytes mode. Bridge to local terminal.
stdinFd := int(os.Stdin.Fd())
if term.IsTerminal(stdinFd) {
    // ... (unchanged) ...
}
// ... (output copy goroutine + copyWithEscape unchanged) ...
```

The `defer stopWinsize()` runs *before* the existing `defer conn.Close()` (lines later, but `defer` is LIFO). That ordering is deliberate: stop the SIGWINCH watcher *first*, then let conn.Close() unblock the output copy goroutine. Reverse order would let a SIGWINCH fire mid-conn-tear-down and trigger a SendResize against a half-closed daemon-side state — harmless (the new dial succeeds against the server, not the dying attach conn) but less tidy.

Actually the `defer conn.Close()` is at line 47, *before* the watcher. So `defer` LIFO order makes `stopWinsize` run first, then `conn.Close()`. That's the order we want; nothing to reorder.

### Caveat rewrite

| File:lines | Action |
|---|---|
| `internal/control/attach_client.go:25-27` | Drop the "Subsequent live resize events are not propagated in Phase 0 — detach and reattach to update." sentence. Replace with: "Subsequent live resize events are forwarded as `VerbResize` requests on fresh control connections (see `SendResize`) by an internal SIGWINCH watcher; the supervised child receives the new dimensions via the daemon's `Bridge.Resize` seam." |
| `internal/control/protocol.go:62` | The "(deferred to #133)" parenthetical is now stale — change to "by the SIGWINCH handler in pyry attach (`startWinsizeWatcher`)." Mechanical, not load-bearing. |

No other doc/spec touches.

### Why an internal helper, not a method on a struct

The package has no `attachClient` type today; `Attach` is a free function holding state on its stack. Introducing a struct just to host the watcher would inflate the change for no benefit — the watcher's lifetime is exactly Attach's stack frame, and there's nothing to share with other call sites. The helper takes its dependencies as parameters (the `read` function and the `send` closure) which keeps it directly unit-testable without the indirection of a method receiver.

### Why pass `read` and `send` as parameters

Two reasons:

1. **Testability.** A unit test that triggers a real SIGWINCH (`syscall.Kill(os.Getpid(), syscall.SIGWINCH)`) cannot rely on `pty.GetsizeFull(os.Stdin)` returning a real size in CI (no controlling terminal). The test injects a stub `read` that returns a fixed `(80, 24, true)` and a stub `send` that records the call.
2. **Keeps `os` and the network out of the watcher's body.** The watcher is pure orchestration: signal in, callback out. The IO ends are at the boundary, where production wires them to `pty.GetsizeFull` and `SendResize`. This is the same pattern used elsewhere in the codebase (e.g. supervisor's `Config` struct).

A package-level `var` indirection (the `time.Now`-replacement idiom) was considered and rejected — it couples test-time substitution to global state that other tests in the package could trip over. Function parameters are cleaner.

## Data flow

```
User resizes terminal
        │
        ▼
Kernel raises SIGWINCH on the controlling terminal
        │
        ▼
sigCh (in startWinsizeWatcher's goroutine) receives os.Signal
        │
        ▼
read() → (cols, rows, ok)
   │
   ├─ ok=false (no TTY / GetsizeFull errored) → continue
   │
   └─ ok=true
            │
            ▼
   send(ctx, cols, rows)
            │
            ▼
   SendResize(ctx, socketPath, sessionID, cols, rows)
            │
            ▼  fresh Unix-socket dial (independent of attach conn)
   ────────────────────────── network boundary ──────────────────────────
            │
            ▼
   Server.handle → case VerbResize → handleResize (#137)
            │
            ▼
   Session.Resize → Bridge.Resize → pty.Setsize → kernel raises SIGWINCH
   for the supervised child, child redraws against new dimensions.
```

The attach conn (held by `Attach`) is **not used** by any of this. Each SIGWINCH is its own short-lived dial. This mirrors `#137`'s design: malformed resize JSON, transient daemon hiccups, or seam errors all live entirely on the resize conn and never disturb the byte stream on the attach conn.

## Concurrency model

Three goroutines exist inside `Attach` after this ticket:

1. **Output copier** (existing): `go io.Copy(os.Stdout, conn)`. Lives until `conn.Close()`. Unchanged.
2. **Input pump** (existing, on the calling goroutine): `copyWithEscape(conn, os.Stdin)`. Returns nil on clean detach, error otherwise.
3. **NEW: SIGWINCH watcher**: `startWinsizeWatcher`'s inner goroutine. Lives until `stopWinsize()` is called (synchronous — closes `done`, then waits on `gone`).

The three are independent. The SIGWINCH watcher does not touch the attach `conn`; it dials a fresh Unix socket per event via `SendResize`. The output copier and input pump bridge byte streams; the SIGWINCH watcher exchanges JSON on a separate conn. No shared mutable state.

### Race scenarios audited

| Race | Outcome |
|---|---|
| SIGWINCH arrives before `signal.Notify` is fully registered | Linux signal delivery to `signal.Notify` happens-after the syscall returns; this is documented stdlib behaviour. The first SIGWINCH after registration is captured. The very-early window (before registration) is covered by the handshake `AttachPayload` — AC#2's "first SIGWINCH may arrive before the goroutine is fully wired" caveat is intentional, not a bug. |
| SIGWINCH arrives during `stopWinsize` between `signal.Stop` and the goroutine's `<-done` read | The buffered channel (cap 1) absorbs at most one signal; `signal.Stop` then unsubscribes; the goroutine's next select sees `<-done` and exits. The pending signal in `sigCh` is drained by GC — no resize is emitted because the goroutine took `<-done`. Acceptable: the pending resize was *concurrent* with detach and the user is no longer attached anyway. |
| `SendResize` is in flight when `stopWinsize` fires | `SendResize` is a synchronous function call from the watcher goroutine; if it's running, the goroutine hasn't reached its `select` yet. `stopWinsize` blocks on `<-gone`, which is closed only after `SendResize` returns. So `stopWinsize` waits out any in-flight SendResize. This is what makes teardown race-free — the watcher's exit is gated on its own work completing. |
| Burst of SIGWINCH (user dragging window corner) | Each signal triggers one `SendResize`. Buffered channel of cap 1 means two SIGWINCH arriving while the goroutine is mid-`SendResize` collapse to one queued — the second is coalesced. After `SendResize` returns, the goroutine reads the queued one and emits a fresh resize with the *current* size (re-read at signal time, not stale). Last-emitted-wins on the server (`Bridge.ptyMu` serialises). This is the correct behaviour for human-driven drags and matches `supervisor/winsize.go`'s coalescing. |
| `ctx` cancelled while watcher is alive | `SendResize(ctx, …)` propagates ctx; on cancellation it returns an error which the watcher silently drops. The watcher goroutine itself does **not** observe ctx — it observes `done`. This is deliberate: `Attach` always tears down the watcher via `defer stopWinsize()`, so `done` always fires; relying on ctx would add a second teardown path that could race with the deferred stop. |
| Two `Attach` calls concurrent in the same process | Each one installs its own `signal.Notify`. The `os/signal` package supports multiple subscriptions to the same signal; both watchers receive each SIGWINCH and both emit resizes. With distinct sessionIDs they target different sessions; with overlapping IDs the server processes them in arrival order under `Bridge.ptyMu` and last-wins. No new races. (In practice no real CLI invocation does this, but the property holds.) |

No new mutexes, no new shared state.

## Error handling

| Scenario | What the watcher does | What the user sees |
|---|---|---|
| `read()` returns `ok=false` (no TTY in the test env, ioctl error) | Skip the iteration; no SendResize. | No emission. The next SIGWINCH retries. |
| `SendResize` returns an error (daemon down, ctx cancelled, unknown session) | Silently drop. | The next SIGWINCH retries. |
| `SendResize` returns a server `Error` string (resolver failure on a stale ID) | Silently drop. | Same. |
| Goroutine panics (shouldn't happen — but if `read` or `send` panic) | Goroutine dies, `gone` is closed via the `defer close(gone)` recovery path. `stopWinsize` then unblocks. | A subsequent SIGWINCH is silently lost (no goroutine to receive it). The Attach session itself is unaffected. |

The watcher logs **nothing**. Reasons:

1. The package is library-level (`internal/control`); `Attach` is invoked from `cmd/pyry/main.go` which sets up its own logging conventions. Adding `slog` calls inside the watcher would imply a logger plumbed through `Attach`'s signature, which inflates the change.
2. SendResize errors are uninteresting in practice — they're either "daemon went away" (already surfaced when the attach conn drops) or "transient socket hiccup" (which the next SIGWINCH corrects). Logging them creates noise for no actionable signal.
3. Mirrors SendResize's own godoc: "Callers ... should not retry on transient failure; the next SIGWINCH will re-emit a fresh resize."

## Testing strategy

Two tests in a new file `internal/control/attach_winsize_test.go`. Build-tag the file `//go:build unix` since `syscall.SIGWINCH` is unix-only and the project explicitly excludes Windows (`CLAUDE.md` "Platforms: Linux + macOS").

| Test | What it pins |
|---|---|
| `TestStartWinsizeWatcher_SIGWINCHEmitsResize` | Stand up a hand-rolled `net.Listen` server (mirror `TestSendResize_RoundTrip`'s shape, `resize_test.go:207-252`). Call `startWinsizeWatcher(ctx, fakeRead, fakeSend)` where `fakeRead` returns a fixed `(120, 40, true)` and `fakeSend` is the production closure that calls `SendResize`. Send `syscall.Kill(os.Getpid(), syscall.SIGWINCH)`. Assert the server records one `Request{Verb: VerbResize, Resize: &ResizePayload{Cols:120, Rows:40}}`. |
| `TestStartWinsizeWatcher_StopIsSynchronousAndLeakFree` | Loop N=50 iterations: each iteration calls `startWinsizeWatcher(...)` with no-op `read`/`send`, fires one `syscall.Kill(os.Getpid(), syscall.SIGWINCH)` to exercise the live path, then calls `stop()`. Synchronous-stop guarantee means the goroutine has exited by the time `stop()` returns. Sample `runtime.NumGoroutine()` at three points: (a) before the loop, (b) during the loop's tail, (c) after the loop. The before/after delta must be ≤ 0 (any in-flight test goroutines would inflate the *during* sample but not the after sample). The "during" sample is informational. The structural guarantee is that the test cannot hang — if `stop()` were not synchronous, a goroutine leak would still be invisible to `NumGoroutine` flake-prone budgets, but the *test still detects it via cumulative growth*: 50 leaked goroutines is well outside any other-test noise. |

Both tests use `t.Parallel()`. The SIGWINCH delivery is `syscall.Kill(os.Getpid(), syscall.SIGWINCH)` — sends the signal to the test process itself, picked up by every active `signal.Notify(SIGWINCH)` subscriber in the process. With `t.Parallel()`, other tests running in parallel must not subscribe to SIGWINCH or they'd see the test's signal. Audit confirms no other test in `internal/control` does so. (If a future ticket adds a peer test that subscribes, it must not be `t.Parallel()` with this one — note in the test's godoc.)

The wire-shape SIGWINCH→`SendResize` transformation is the *primary* AC of this ticket; structural integration with `pty.GetsizeFull` and `os.Stdin` is covered by the existing `supervisor/winsize.go` patterns and is not re-exercised here.

### What we deliberately don't test

- **Full Attach()-with-real-PTY end-to-end.** The CLI driver (`internal/e2e`) is the right home for that, and #126 is the ticket. Re-running it here would duplicate that scaffold for no incremental coverage.
- **`syscall.Kill` in a non-unix environment.** The `//go:build unix` tag excludes Windows. The project doesn't target Windows.

### Re-running the existing handshake test

`TestServer_AttachAppliesHandshakeGeometry` (`attach_test.go:286-330`) is the canonical handshake-geometry pin. It must still pass unchanged after this ticket — verify with `go test -race ./internal/control/...` before commit. AC#2 is satisfied by *not modifying* the handshake encode in `Attach` (line 49-52 of attach_client.go is untouched).

## Open questions

1. **Should `stopWinsize` accept a deadline so a misbehaving SendResize cannot hang detach?** No. `SendResize` already honors ctx; the deferred call sets the same ctx as the watcher's. If ctx is cancelled before detach, SendResize returns within `DialTimeout` (5s). If a future regression makes SendResize hang past that, the fix is in `SendResize`, not in a wrapper deadline. Document on `startWinsizeWatcher`'s godoc.
2. **Should the watcher prime an initial resize after the handshake (in case the user's terminal changed size *between* `term.GetSize` in `runAttach` and the watcher's first SIGWINCH)?** No. The window is microseconds; the next real SIGWINCH covers it. Priming would also push a duplicate resize in the common case where geometry didn't change, which is wasted IPC.
3. **Should the watcher coalesce bursts more aggressively (e.g. a 50ms debounce)?** No, not in this ticket. The kernel `pty.Setsize` cost dominates, not IPC. If perceptible jank is observed in real usage, the right fix is server-side coalescing (one debounce per session) rather than client-side (each client invents its own latency budget). File a #200-series ticket if/when observed.
4. **Should `pyry attach` log "resize forwarded" at debug level on each SIGWINCH?** No, not in this ticket. Resize emission is a steady-state operation; logging it pollutes the operator's daemon log with one line per terminal resize. If a debug surface is wanted later, plumb it through `Server` (it already has a `log`); the client side stays silent.
