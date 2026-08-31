# Client dial: transient-startup retry (#198 + #199)

`internal/control/dial.go` houses the dial-side surface for the control client: the `dial()` primitive every client verb routes through, the bounded `dialWithRetry` loop wrapping it, and the `isTransientStartupError` predicate that gates the retry. After #199, `client.go` no longer defines `dial` and drops its `net` import; all dial-related helpers cohabit in `dial.go`.

### Why retry exists

After `launchctl kickstart -k` (manual restart) or `pyry update`'s self-restart on v0.10.1 (automatic), client commands like `pyry status` race the daemon binding the control socket. The 100 ms – 2 s window between "old daemon exited" and "new daemon listening" used to surface as:

```
pyry: status: dial /Users/<user>/.pyry/pyry.sock: dial unix /Users/<user>/.pyry/pyry.sock: connect: no such file or directory
```

Verified manually 2026-05-08 during the v0.9.1 → v0.10.1 Mac upgrade. Wrapping `dial` once covers every client verb (`status`, `sessions list`, `attach`, `attach --stdio`, `sessions new`, `sessions rename`, ...) — including the two attach paths with their own callsites at `internal/control/attach_client.go:53` and `internal/control/attach_stdio_client.go:34`.

### Predicate (#198)

`isTransientStartupError(err error) bool` is a pure package-private classifier for the two dial-error shapes the daemon-restart race produces:

1. **Socket file absent** — `*net.OpError` ⇢ `*os.PathError{Err: syscall.ENOENT}`.
2. **Socket file present, no listener accepting** — `*net.OpError` ⇢ `*os.SyscallError{Err: syscall.ECONNREFUSED}` (also covers a stale socket file from a previously crashed daemon).

```go
return errors.Is(err, syscall.ENOENT) ||
    errors.Is(err, syscall.ECONNREFUSED)
```

`errors.Is` walks the unwrap chain through `dial`'s `fmt.Errorf("dial %s: %w", socketPath, err)`, through `*net.OpError.Unwrap`, through `*os.PathError.Unwrap` / `*os.SyscallError.Unwrap`, matching at the leaf `syscall.Errno`. The wire-shape (`*net.OpError`) is incidental — the contract is "did the unwrap chain reach one of the two named errnos?" `errors.Is(nil, sentinel)` is `false`, so the nil case falls out without a guard.

**Misclassification semantics.**
- *False negative* (real transient miss-classified as permanent): retry loop doesn't kick in; client surfaces the dial error immediately — same behaviour as pre-#199. Acceptable.
- *False positive* (permanent error miss-classified as transient): client retries up to the bounded budget then surfaces the wrapped error — slightly worse UX, not incorrect. The two-errno scope keeps the false-positive surface tight.

### Retry loop (#199)

```go
const (
    dialRetryBudget   = 1500 * time.Millisecond
    dialRetryInterval = 50 * time.Millisecond
)

type dialFunc func(ctx context.Context, socketPath string) (net.Conn, error)

func dial(ctx context.Context, socketPath string) (net.Conn, error) {
    return dialWithRetry(ctx, socketPath, netDialUnix, dialRetryBudget, dialRetryInterval)
}
```

`dialWithRetry(ctx, socketPath, fn, budget, interval)` calls `fn` repeatedly while it returns a transient startup error, polling every `interval` up to `budget` wall-clock time. The `dialFunc` parameter is the test seam — production passes `netDialUnix` (a thin `net.Dialer.DialContext("unix", …)` wrapper); `dial_test.go`'s `fakeDialer` satisfies it for the unit tests.

**Loop shape.**

- **First attempt is immediate.** No pre-sleep — the fast-path "daemon is up" stays one `connect(2)` + return.
- **Three exit doors, all preserving the original error** via `fmt.Errorf("dial %s: %w", socketPath, err)` — byte-identical to pre-#199's single-shot wrap. AC #2 ("same error message users get today") is structural, not asserted by string-match.
- **Deadline check sits between the predicate check and the sleep.** `!time.Now().Before(deadline)` (rather than `After`) handles the boundary tick — at `t = deadline` exactly we exit with the wrapped transient error rather than sleeping past the budget.
- **`time.NewTimer` + `Stop()` on ctx cancel**, not `time.After`. The leak is bounded (50 ms) but harmless to fix.
- **`ctx.Done()` short-circuits the inter-attempt sleep, not the dial call.** A caller-side context cancel during the sleep returns the most recent transient error wrapped (not `ctx.Err()`). During a dial attempt the cancel propagates through `net.Dialer.DialContext` per the stdlib contract and surfaces as a wrapped `context.Canceled` — *not* a transient startup error, so the next loop iteration takes the non-transient exit immediately.
- **Default-deadline fallback moved into `dialWithRetry`.** The `if _, ok := ctx.Deadline(); !ok { ctx, cancel = context.WithTimeout(ctx, DialTimeout) }` block that used to live in `dial()` now sits at the top of `dialWithRetry` so production `dial` stays a one-liner. Behaviour is unchanged for callers without a deadline.

### Sizing

1.5 s budget / 50 ms interval ≈ 30 attempts, sized for the launchctl-kickstart and `pyry update` self-restart windows observed in production. Constants over vars: tests parameterise via `dialWithRetry`'s arguments rather than mutating package state.

### Tests

`dial_test.go` keeps the existing six-row `TestIsTransientStartupError` (predicate, #198) and adds `TestDialWithRetry` (retry loop, #199) with three subtests driven by a shared `fakeDialer`:

- **Recovers after N transient failures.** `seq: [ENOENT, ENOENT, ENOENT, nil]` with `budget = 200 ms`, `interval = 10 ms`. Asserts non-nil conn, `f.calls == 4`, elapsed ≤ `N*interval + 50 ms`.
- **Always-transient exhausts budget.** Empty `seq` (defaults to ENOENT). Asserts `errors.Is(err, syscall.ENOENT)`, error string contains `"dial /fake/path:"`, elapsed ∈ `[budget, budget + 50 ms]` — lower bound proves retries actually ran.
- **Non-transient error fails immediately.** `seq: [io.EOF]` with production constants. Asserts `f.calls == 1`, `errors.Is(err, io.EOF)`, elapsed ≤ 50 ms (sanity).

`fakeDialer.dial` returns `net.Pipe`'s c1 (with c2 closed) on `nil` entries — `net.Pipe` is the cheapest stdlib `net.Conn` that doesn't need a listener. Tests close the returned conn before exiting. Timing slack is `+50 ms` for CI scheduling under `-race` (the architect's "+~5 ms" was the loop's steady-state intent, not the assertion margin); raise to 100 ms if real `-race` flakes appear.

**No e2e in this ticket.** The retry behaviour is driven by `dialFunc` injection; the real dial path is exercised by every existing `pyry status` / `pyry sessions list` e2e that already routes through `dial`. AC #1 (success after `launchctl kickstart -k`) is satisfied structurally — the wrapper covers all client verbs at one site.

See [`docs/specs/architecture/198-transient-startup-error-predicate.md`](../../specs/architecture/198-transient-startup-error-predicate.md) and [`docs/specs/architecture/199-dial-with-retry.md`](../../specs/architecture/199-dial-with-retry.md) for the architect's specs.
