# Resize: Live SIGWINCH Watcher (#133)

The client-side producer for `VerbResize`. Lives next to `Attach` in
`attach_client.go` (no new file, no exported type) and runs only for the
lifetime of an attach. Closes the live-resize loop end-to-end:

```
terminal resize → SIGWINCH on the client → startWinsizeWatcher
  → SendResize on a fresh control conn → handleResize → Session.Resize
  → Bridge.Resize → pty.Setsize → child redraws
```

### Wiring inside `Attach`

`Attach` installs the watcher *after* the handshake ack succeeds and tears
it down via `defer` before `Attach` returns:

```go
if !resp.OK {
    return errors.New("control: attach ack missing")
}

stopWinsize := startWinsizeWatcher(ctx, readTerminalSize, func(ctx context.Context, cols, rows int) error {
    return SendResize(ctx, socketPath, sessionID, cols, rows)
})
defer stopWinsize()
```

Two ordering rules:

- **Install after the handshake.** A resize emitted before the server has
  bound a session would race; deferring installation to after the ack
  removes that window structurally.
- **`defer stopWinsize()` runs before `defer conn.Close()`.** Both defers
  exist; LIFO order tears down the SIGWINCH watcher first, then the attach
  conn. This avoids a SIGWINCH firing into a half-closed conn — and since
  resize uses its own conn anyway, the order is also operationally
  irrelevant; documenting it just pins intent.

### `startWinsizeWatcher` — small helper, dependency-injected I/O

```go
type terminalSizeReader func() (cols, rows int, ok bool)

func startWinsizeWatcher(
    ctx context.Context,
    read terminalSizeReader,
    send func(ctx context.Context, cols, rows int) error,
) (stop func())
```

Pure orchestration: signal in, callback out. The `read` and `send`
parameters keep `os` and the network out of the watcher's body; production
wires them to `pty.GetsizeFull(os.Stdin)` (via `readTerminalSize`) and
`SendResize`. Tests inject stubs.

The watcher does **not** prime an initial size at startup — initial
geometry flows through the handshake `AttachPayload` (AC#2 of #133). The
window between handshake and the first SIGWINCH is microseconds; priming
would push a duplicate resize in the common case where geometry didn't
change.

### Synchronous teardown is the load-bearing guarantee

`stop` does three things in order:

1. `signal.Stop(sigCh)` — unsubscribe from SIGWINCH delivery.
2. `close(done)` — wake the watcher goroutine's `select`.
3. `<-gone` — block until the goroutine has actually exited.

Step 3 makes teardown *synchronous*, not *eventual*. No goroutine or
signal subscription outlives the `defer stopWinsize()` call site. This is
what makes "no signal handler or goroutine leaks across attach/detach
cycles" (AC#3) true structurally rather than as a guarantee that needs a
retry budget.

Importantly, if `SendResize` is in flight when `stop` fires, the goroutine
hasn't reached its `select` yet — `<-gone` waits out the in-flight call.
Detach cannot race against a half-completed resize.

### Production reader uses `os.Stdin` directly

```go
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
```

`os.Stdin` is used directly rather than wrapping `os.Stdin.Fd()` in a
fresh `*os.File` — the same convention `internal/supervisor/winsize.go`
follows. Wrapping a raw fd in a fresh `*os.File` makes the wrapper
finalizable; if it's GC'd while the underlying fd is in use, the
finalizer-driven `close(2)` plus a subsequent `open(2)` of any fd can race
into a reused fd, with `pty.Setsize` then targeting the wrong file.
Using `os.Stdin` directly inherits the runtime-managed lifecycle of the
process's actual stdin.

### Best-effort posture, no logging

`SendResize` errors are silently dropped: transient daemon hiccup, ctx
cancelled mid-flight, server-side `Error` string from a stale id —
all fall through the `_ = send(...)` line. The next SIGWINCH retries.
This matches `SendResize`'s own godoc and the server's posture
(`handleResize` returns OK even when the seam errors).

The watcher logs nothing. The package is library-level; `Attach`'s caller
(`cmd/pyry/main.go`) sets the logging conventions, and SendResize errors
are uninteresting in practice (either "daemon went away" — already
surfaced by the attach conn dropping — or "transient socket hiccup" — the
next SIGWINCH corrects it).

### Burst coalescing

A user dragging the window corner produces a flurry of SIGWINCH. The
buffered-cap-1 `sigCh` collapses any signals that arrive while the
goroutine is mid-`SendResize` to *one* queued event. After the in-flight
`SendResize` returns, the goroutine reads the queued one and emits a
fresh resize with the *current* size (re-read at signal time, not stale).
The server serialises under `Bridge.ptyMu`; last-write-wins on the
kernel `ioctl(TIOCSWINSZ)`. Same coalescing shape as the daemon-side
`supervisor/winsize.go`.

### Concurrency and the attach conn

The watcher does **not** touch the attach `conn`. Each SIGWINCH is its
own short-lived dial via `SendResize`. So three goroutines exist inside
`Attach` after handshake — output copier, input pump (the calling
goroutine itself, in `copyWithEscape`), and the watcher — and they share
no mutable state. A malformed resize, daemon-side seam error, or
transient dial failure lives entirely on the resize conn and never
disturbs the byte stream on the attach conn.

### Tests

`internal/control/attach_winsize_test.go` (build tag `//go:build unix`,
since `syscall.SIGWINCH` is unix-only and the project excludes Windows):

- `TestStartWinsizeWatcher_SIGWINCHEmitsResize` — hand-rolled `net.Listen`
  server (mirrors `TestSendResize_RoundTrip`), stub `read` returns
  `(120, 40, true)`, `syscall.Kill(syscall.Getpid(), syscall.SIGWINCH)`
  delivers the signal, server records the decoded `Request{Verb:
  VerbResize, Resize: &ResizePayload{Cols:120, Rows:40, SessionID:"abc"}}`.
- `TestStartWinsizeWatcher_StopIsSynchronousAndLeakFree` — 50 iterations
  of `startWinsizeWatcher` + `Kill SIGWINCH` + `stop` + `cancel`; samples
  `runtime.NumGoroutine()` before and after with a small slack.
  Per-iteration leakage would dwarf any test-noise budget.

Both tests run **without** `t.Parallel()` because `syscall.Kill(SIGWINCH)`
is process-wide; any concurrent test subscribing to SIGWINCH would race.
Audit confirms no other test in `internal/control` does so today; if a
future test adds a peer subscriber, it must coordinate.

The wire-shape SIGWINCH→`SendResize` transformation is the primary AC of #133; structural integration with `pty.GetsizeFull` and `os.Stdin` is
covered by the existing `supervisor/winsize.go` patterns and is not
re-exercised here. End-to-end coverage (real PTY, real attach, real
resize) lives in #126.
