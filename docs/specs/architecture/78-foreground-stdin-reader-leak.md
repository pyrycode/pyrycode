# Spec: Fix foreground-mode supervisor stdin reader leak (#78)

## Context

`internal/supervisor/supervisor.go:309-317` runs two `io.Copy` goroutines
to bridge user keystrokes to the claude PTY in foreground mode:

```go
go func() { _, err := io.Copy(ptmx, os.Stdin); done <- err }()
go func() { _, err := io.Copy(os.Stdout, ptmx); done <- err }()
```

When the child exits and `ptmx.Close()` runs, the *output* goroutine
unblocks immediately (PTY read returns EOF). The *input* goroutine is
blocked inside `os.Stdin.Read`, holding `os.Stdin`'s `fdMutex`. There is
no event that wakes it: stdin is still open at the OS level. It sits
stranded until the next byte the kernel delivers — which, in tests, is
never.

`runOnce` papers over this with a 100ms drain-one-of-two timeout
(`supervisor.go:324-327`) and `Run` then loops to spawn another child.
Each restart adds a stranded reader. Worse, every supervisor that runs
foreground and dies pins one more goroutine to `os.Stdin`'s `fdMutex`.
New `pty.Start` calls eventually deadlock waiting for that mutex.

The previous "we accept this for foreground because production uses
service mode" justification (`supervisor.go:284-286`) doesn't hold for
test fixtures that exercise foreground mode at scale. Observed failures
attributable to this leak:

- `TestPool_ActiveCap_RaceConcurrentActivate` deadlock (#41)
- `TestPool_Run_StartsWatcher` flake (#39)
- `TestPool_New_Reconciles_ColdStart_PicksNewestImmediately` flake (#73)
- ~10–15 wasted developer-agent turns per ticket on flake filtering

Sibling tests have already migrated to Bridge-mode fixtures as a
workaround (see `internal/sessions/pool_cap_test.go:23-26`,
`pool_create_test.go:27`). This ticket fixes the underlying behavior so
that workaround can be retired (out of scope for #78 itself).

Design sources:

- Ticket body, especially the three candidate approaches and the
  non-TTY/CI test-skip discussion
- `internal/supervisor/supervisor.go:228-329` — current `runOnce`
- `internal/supervisor/supervisor.go:248-270` — Bridge-mode branch
  (must be unchanged)
- `internal/supervisor/winsize.go` — uses `os.Stdin.Fd()` for
  terminal-size queries; not affected
- `cmd/pyry/main.go:283` — `term.IsTerminal(os.Stdin.Fd())` gate that
  excludes non-TTY foreground today

## Design

### Approach: separate `/dev/tty` fd for the input bridge

Of the three patterns in the ticket body:

- **Open `/dev/tty` separately** ✓ — picked
- Set stdin fd non-blocking + Go runtime poller — finicky restore
  semantics; rejected
- Read with deadline in a loop — adds keystroke latency, would risk the
  `<5ms` AC; rejected

Rationale: `term.MakeRaw(os.Stdin.Fd())` already early-returns on a
non-TTY (`supervisor.go:292-297`), so foreground mode is implicitly
TTY-only. Opening `/dev/tty` is a strict refinement: same underlying
device, fresh fd we *own*, closing it makes the in-flight `Read` return
with an error — `io.Copy` exits cleanly, goroutine drains, no more
`os.Stdin.fdMutex` contention.

The terminal mode (raw / cooked) is a property of the device, not the
fd. `term.MakeRaw(os.Stdin.Fd())` puts the controlling TTY into raw
mode; a separate fd from `os.Open("/dev/tty")` reads from the same
device with the same line discipline. No double-restore risk.

### New surface

No exported types, methods, or sentinels. One unexported helper, one
field rename / behaviour change inside `runOnce`. The change is
internal to `internal/supervisor`.

```go
// openTTYInput returns a reader for the controlling terminal. The
// returned ReadCloser is owned by the caller and must be Closed to
// unblock any in-flight Read (typically the input-bridge goroutine).
//
// On platforms or in environments where /dev/tty is unavailable
// (headless processes, certain containers), it returns the platform
// open error verbatim. Foreground mode is TTY-only by construction —
// callers may treat that error as "fall back to os.Stdin and accept
// the legacy leak" or as "skip the test", their choice.
func openTTYInput() (io.ReadCloser, error)
```

Implementation: `os.OpenFile("/dev/tty", os.O_RDONLY, 0)`. Single
liner; Linux + macOS only (the project's supported platforms — see
CLAUDE.md "Platforms: Linux + macOS").

### `runOnce` foreground branch — revised

```go
// Foreground mode: bridge directly to the supervisor's own terminal.

stdinFd := int(os.Stdin.Fd())
var restoreTerm func()
if term.IsTerminal(stdinFd) {
    oldState, err := term.MakeRaw(stdinFd)
    if err == nil {
        restoreTerm = func() { _ = term.Restore(stdinFd, oldState) }
    }
}
defer func() {
    if restoreTerm != nil {
        restoreTerm()
    }
}()

stopResize := s.watchWindowSize(ptmx)
defer stopResize()

// Open /dev/tty as a separate fd for the input bridge. When the
// child exits we Close this fd, the in-flight Read returns, and
// the input goroutine drains cleanly. Reading os.Stdin directly
// would leave the goroutine blocked on os.Stdin's fdMutex — see #78.
input, inputErr := openTTYInput()
if inputErr != nil {
    s.log.Debug("foreground: /dev/tty unavailable, falling back to os.Stdin",
        "err", inputErr)
    input = stdinFallback{} // no-op Close, reads from os.Stdin
}
defer func() { _ = input.Close() }()

done := make(chan error, 2)
go func() {
    _, err := io.Copy(ptmx, input)
    done <- err
}()
go func() {
    _, err := io.Copy(os.Stdout, ptmx)
    done <- err
}()

waitErr := cmd.Wait()
// Unblock both copy goroutines: ptmx.Close() drains the output
// goroutine; input.Close() drains the input goroutine.
_ = ptmx.Close()
_ = input.Close()

// Drain both. Under normal operation each returns within microseconds
// of its source closing; the timeout is a safety net.
for i := 0; i < 2; i++ {
    select {
    case <-done:
    case <-time.After(goroutineDrainTimeout):
        s.log.Warn("io bridge goroutine drain timeout")
    }
}
return waitErr
```

`stdinFallback` is a one-line type:

```go
type stdinFallback struct{}

func (stdinFallback) Read(p []byte) (int, error) { return os.Stdin.Read(p) }
func (stdinFallback) Close() error               { return nil }
```

We don't return `os.Stdin` directly because closing it would break the
process. The fallback path keeps the legacy leak behaviour but only
when `/dev/tty` is unavailable — an environment where foreground mode
was already not the production deployment. No behaviour regression.

### Comment cleanup (AC explicit)

The "We accept this for foreground mode rather than retrofitting a
cancellable stdin reader" block at `supervisor.go:272-287` is removed.
Replace with the short rationale embedded in the rewrite above
(2 lines: "Open /dev/tty as a separate fd... Reading os.Stdin directly
would leave the goroutine blocked on os.Stdin's fdMutex — see #78").

The `goroutineDrainTimeout` constant comment at `supervisor.go:26-32`
also drifts: it describes input-bridge stranding as the reason the
timeout exists. Rewrite to: "caps how long runOnce waits for the I/O
bridge goroutines after the child exits and the bridges have been
closed. Both should drain promptly; the timeout is a safety net."

### Bridge-mode branch — unchanged

The `s.cfg.Bridge != nil` branch at `supervisor.go:248-270` is not
touched. Bridge-mode tests (`bridge_test.go`) continue to pass. The AC
calls this out explicitly — "byte-for-byte unchanged in behavior."

### Drain semantics — change of contract

Today: drain *one of two* with a 100ms timeout (input goroutine is
abandoned). After the fix: drain *both* with the same per-iteration
timeout. Why: with the fix in place, the input goroutine reliably
exits, so we should wait for it. In the rare case it doesn't (the
fallback path, or a kernel oddity), the safety-net timeout still
bounds runOnce's return latency to ~200ms total in the worst case
(2 × `goroutineDrainTimeout`). Acceptable.

## Concurrency model

The fix touches only the foreground branch. All goroutines and shared
state outside that branch are unchanged.

| Goroutine | Lifetime | Wakeup signal |
|---|---|---|
| Output bridge (`io.Copy(stdout, ptmx)`) | per-runOnce | `ptmx.Close()` returns EOF |
| **Input bridge (`io.Copy(ptmx, input)`)** | **per-runOnce** | **`input.Close()` returns ErrClosed (was: stranded)** |
| `cmd.Wait` (implicit, internal to exec) | per-runOnce | child exit |
| SIGWINCH watcher (`watchWindowSize`) | per-runOnce | `stopResize()` closes done chan |

Lock order: no locks in this code path. `input.Close()` and
`ptmx.Close()` are independent fds; either order is fine. The chosen
order — `ptmx.Close()` first, then `input.Close()` — matches the
existing intent (output drains before input).

### Race notes

- **Race between `input.Close()` and the in-flight `Read`**:
  Go's `*os.File.Close` is safe to call concurrently with `Read`. The
  in-flight Read returns `*PathError` wrapping `os.ErrClosed` (or
  `syscall.EBADF` depending on platform). `io.Copy` propagates that to
  `done <- err`. No mutex involvement on `os.Stdin`; the new fd has
  its own internal `fdMutex`.

- **Race between two consecutive `runOnce` calls** (Run loop): each
  call opens its own `/dev/tty` fd in its own scope and closes it
  before returning. No fd is shared across iterations. The leak that
  caused #41 (one fd held forever, contention on its `fdMutex` from
  the next `pty.Start`) is structurally impossible with the fix
  because the fd is closed before the next iteration starts.

- **`cmd.Wait` ordering**: unchanged. `cmd.Wait` returns when the
  child has exited and its stdio has been reaped. We then close
  `ptmx` and `input` — both bridges drain. Under the existing code
  the sequence was identical except `os.Stdin` had no `Close`
  counterpart. Adding `input.Close()` is purely additive.

## Error handling

| Failure point | Behavior |
|---|---|
| `openTTYInput` returns ENXIO/ENOENT (no controlling TTY) | Log debug, fall back to `stdinFallback{}`. Foreground continues to work; legacy leak applies. Same as today. |
| `term.MakeRaw` fails | Already handled today: `restoreTerm` stays nil, no raw mode. Unchanged. |
| `input.Close()` returns error | Logged via the deferred `_ = input.Close()` — ignored. The fd is gone either way; the goroutine has already drained. |
| `io.Copy` from input returns error other than `ErrClosed` | Propagated to `done`; `runOnce` returns `waitErr` (the child's exit), not the copy error. Same as today. The copy error is incidental to child exit. |

No new error sentinels. No new public API surface. No new wrap
conventions. Existing logger fields (`err`, etc.) suffice.

## Testing strategy

All tests in `internal/supervisor/supervisor_test.go`. No new test
file.

### Required test

| Test | What it asserts |
|---|---|
| `TestSupervisor_Foreground_NoStdinReaderLeak` | After running multiple foreground-mode supervisor cycles, `runtime.NumGoroutine()` does not exceed the pre-test baseline by more than a small tolerance (allowance for stable runtime/test-framework goroutines). Skips with `t.Skipf("no controlling tty: %v", err)` when `/dev/tty` cannot be opened. |

Sketch:

```go
func TestSupervisor_Foreground_NoStdinReaderLeak(t *testing.T) {
    // Pre-flight: if /dev/tty is unavailable, the supervisor falls back
    // to os.Stdin and the leak persists by design (legacy fallback path).
    // Skip rather than fail.
    f, err := os.Open("/dev/tty")
    if err != nil {
        t.Skipf("no controlling tty: %v", err)
    }
    _ = f.Close()

    cfg := helperConfig("count_exits")
    countFile := t.TempDir() + "/count"
    cfg.helperEnv = append(cfg.helperEnv,
        "GO_TEST_HELPER_COUNT_FILE="+countFile,
        "GO_TEST_HELPER_MAX_EXITS=3",
    )

    sup, err := New(cfg)
    if err != nil {
        t.Fatalf("New: %v", err)
    }

    // Let any pending GC / runtime goroutines settle before snapshot.
    runtime.GC()
    time.Sleep(50 * time.Millisecond)
    pre := runtime.NumGoroutine()

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    go func() {
        // Cancel after enough time for ~3 child exits + restarts.
        time.Sleep(4 * time.Second)
        cancel()
    }()

    if err := sup.Run(ctx); err != nil && !isContextErr(err) {
        t.Fatalf("Run: %v", err)
    }

    // Allow goroutine teardown to complete. The drain timeout in
    // runOnce is bounded by goroutineDrainTimeout * 2 per cycle.
    time.Sleep(500 * time.Millisecond)
    runtime.GC()

    post := runtime.NumGoroutine()
    // Tolerance: 2 goroutines for any incidental runtime/test churn.
    // The leak under the old code was 3+ (one per child exit).
    if post > pre+2 {
        t.Errorf("goroutine leak: pre=%d, post=%d (delta=%d)", pre, post, post-pre)
    }
}
```

### Verification (CI rubric per AC)

```
go test -race -count=20 -run TestSupervisor_Foreground_NoStdinReaderLeak ./internal/supervisor/...
go test -race ./...
go vet ./...
staticcheck ./...
```

`-count=20` runs the test 20 times in the same binary. Each iteration
snapshots its own baseline with `runtime.GC` + sleep, so test-runner
churn doesn't compound across iterations. The relevant signal is
*delta within one iteration* not absolute count across the run.

The Bridge-mode tests (`bridge_test.go`) verify the AC "Bridge-mode
code path is byte-for-byte unchanged" — they pass without
modification.

### What this test does *not* cover

- Keystroke latency: the no-regression AC for "<5ms latency" is a
  property of the read path, which is byte-for-byte the same blocking
  Read on a TTY device — just on a different fd. No measurement test
  is added; the rationale (same kernel call, no polling) is
  sufficient. If a developer wants to manually verify, run `pyry`
  foreground and type — perceptually identical to before.
- Non-TTY foreground (the fallback path): documented as legacy /
  out-of-scope per the ticket. No test asserts the fallback's
  behaviour; the existing leak shape is preserved by design.

## Open questions

None. The ticket body picks the approach; this spec resolves the
fallback strategy and drain semantics.

The only judgment call deferred to the developer: the exact tolerance
in the leak test (`pre + 2` is the recommendation; if `-count=20`
shows benign drift up to +3 from runtime GC scheduler churn, +3 is
acceptable too — but if it exceeds 5 the test is failing for real,
not a tolerance issue).

## Out of scope (reminder)

Per the ticket body:

- Bridge-mode-fixture-migration follow-up for session tests (separate
  concern; #41/#73 fixtures stay as they are even after this lands)
- Any change to Bridge mode itself
- Non-TTY stdin handling (`echo X | pyry` style) — pyry foreground
  requires a TTY
- Generalising the input source as a Config field (not needed; the
  test relies on `os.Open("/dev/tty")` working in dev/CI-with-TTY,
  skipping otherwise)
