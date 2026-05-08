# #199 — `dial` with brief retry on transient startup errors

**Size:** XS (architect-confirmed). One production helper added in
`internal/control/dial.go`, the existing `dial()` in `internal/control/client.go`
relocated alongside it (~12 lines moved, no behavior change for non-transient
errors). Three new table-driven test cases in `internal/control/dial_test.go`
against a fake dialer.

**Status:** ready for development.

**Depends on:** #198 (the predicate `isTransientStartupError`, already merged
into this branch).

## Files to read first

The developer's turn-1 data load. Each entry is paged in deliberately —
don't grep for them.

- `internal/control/client.go:248-285` — current `request()` and `dial()`.
  `dial()` calls `net.Dialer.DialContext` once and wraps the error as
  `fmt.Errorf("dial %s: %w", socketPath, err)`. **The error wrap shape is
  load-bearing for AC #2** (failure preserves "the same error message users
  get today, e.g. `dial unix .../pyry.sock: connect: no such file or
  directory`"). Keep the `fmt.Errorf` exactly as it is.
- `internal/control/dial.go` — current home of `isTransientStartupError`
  (#198). Two-import file (`errors`, `syscall`). The retry helper lands
  here; `dial()` moves here too so all dial-related helpers cohabit (the
  #198 spec already announced this file as the home for #199's helpers).
- `internal/control/dial_test.go` — current test layout for the predicate.
  Mirror its conventions: stdlib `testing` only, table-driven, tight
  failure messages.
- `internal/control/attach_client.go:53` and
  `internal/control/attach_stdio_client.go:34` — two additional callers
  of `dial()`. Confirm they are unchanged: same package, same function
  name, same signature. The PO ticket framed this as a "single-site wrap
  of the dial primitive" — this is what makes that true.
- `internal/control/client.go` — after relocating `dial`, the `net`
  import drops from this file. Verify `goimports`/`gofmt` cleans it.
- `docs/lessons.md` § "Aggregate sub-interfaces into a facade rather
  than threading new constructor parameters" — relevant *only* as a
  reminder that this ticket does NOT need to touch any of `NewServer`'s
  call sites; the seam is purely inside the `control` package's dial
  helpers. Skim once to confirm there's no facade work hiding in this
  ticket.

That's the read budget. The retry loop is twenty lines.

## Context

After `launchctl kickstart -k` (manual) or `pyry update`'s self-restart
(automatic, v0.10.1), client commands like `pyry status` race the daemon
binding the control socket. The 100ms–2s window between "old daemon
exited" and "new daemon listening" surfaces this user-facing error today:

```
pyry: status: dial /Users/<user>/.pyry/pyry.sock: dial unix /Users/<user>/.pyry/pyry.sock: connect: no such file or directory
```

#198 shipped the predicate. #199 wraps the existing `dial()` in a
bounded retry that consumes the predicate. Every client verb (`status`,
`sessions list`, `attach`, `attach --stdio`, ...) goes through `dial`
— wrapping it once covers all of them, including the two attach paths
that have their own callsites (`attach_client.go:53`,
`attach_stdio_client.go:34`).

Server-side stays untouched. The retry is an entirely client-side
behavior change.

## Design

### Placement

- **Move** `dial()` from `internal/control/client.go:273-285` to
  `internal/control/dial.go`. No body change in this step — same
  signature, same wrap message, same imports moving with it.
- **Add** `dialWithRetry(ctx, socketPath, fn, budget, interval)` to
  `internal/control/dial.go`. `dial()` becomes a one-line wrapper that
  calls `dialWithRetry` with the production constants and the real
  `net.Dialer.DialContext` primitive.
- **Add** an unexported `dialFunc` type in `internal/control/dial.go`
  (the test seam — fake dialers in `dial_test.go` satisfy it).

After this ticket, `internal/control/dial.go` contains:
`isTransientStartupError`, the `dialFunc` type, the production
constants, the production dial-fn (a small closure / package var
holding `net.Dialer.DialContext`), `dial`, `dialWithRetry`. That's
the full surface of dial-related helpers in the `control` package.

After this ticket, `internal/control/client.go`:
- No longer defines `dial`.
- Drops the `net` import (only `net.Conn` was referenced there, and
  only by `dial`).
- `request()` calls `dial(ctx, socketPath)` exactly as before.

### Function signatures

```go
// dialFunc is the underlying dial primitive — net.Dialer.DialContext
// in production, swappable in tests.
type dialFunc func(ctx context.Context, socketPath string) (net.Conn, error)

// dialWithRetry calls fn repeatedly while it returns a transient startup
// error (ENOENT / ECONNREFUSED, per isTransientStartupError), up to
// budget elapsed wall-clock time, polling every interval. The first
// attempt is immediate; subsequent attempts follow each interval. A
// non-transient error surfaces immediately; budget exhaustion preserves
// the most recent transient error. Visible at package scope so tests can
// drive it with a fake dialFunc and shorter timings.
func dialWithRetry(
    ctx context.Context,
    socketPath string,
    fn dialFunc,
    budget time.Duration,
    interval time.Duration,
) (net.Conn, error)

// dial is the one production caller — same signature it had pre-#199,
// so request() / handleAttach / attach-stdio paths are byte-identical.
func dial(ctx context.Context, socketPath string) (net.Conn, error)
```

`dialWithRetry` is unexported. The test file lives in the same package
(`package control`) so it has access without exporting.

### Constants

Two new package-level constants in `dial.go`:

```go
const (
    dialRetryBudget   = 1500 * time.Millisecond
    dialRetryInterval = 50 * time.Millisecond
)
```

These match AC #3 verbatim (≤1.5s wall-clock, 50ms interval, ~30
attempts). They are constants, not vars — production behavior is fixed;
tests parameterize via `dialWithRetry`'s arguments rather than mutating
package state.

### Body of `dial`

```go
func dial(ctx context.Context, socketPath string) (net.Conn, error) {
    return dialWithRetry(ctx, socketPath, netDialUnix, dialRetryBudget, dialRetryInterval)
}

// netDialUnix is the production dialFunc — kept as a named function so
// dialWithRetry's signature stays test-driven without a closure
// capturing a net.Dialer per call.
func netDialUnix(ctx context.Context, socketPath string) (net.Conn, error) {
    var d net.Dialer
    return d.DialContext(ctx, "unix", socketPath)
}
```

Note: the existing `dial()` had its own `context.WithTimeout(ctx,
DialTimeout)` fallback when the caller's ctx had no deadline. **Keep
that fallback** — relocate it into `dialWithRetry` (see below) so the
production `dial()` wrapper stays a one-liner. The default-deadline
behavior is unchanged from today.

### Body of `dialWithRetry`

```go
func dialWithRetry(
    ctx context.Context,
    socketPath string,
    fn dialFunc,
    budget time.Duration,
    interval time.Duration,
) (net.Conn, error) {
    if _, ok := ctx.Deadline(); !ok {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, DialTimeout)
        defer cancel()
    }

    deadline := time.Now().Add(budget)
    for {
        conn, err := fn(ctx, socketPath)
        if err == nil {
            return conn, nil
        }
        if !isTransientStartupError(err) {
            return nil, fmt.Errorf("dial %s: %w", socketPath, err)
        }
        if !time.Now().Before(deadline) {
            return nil, fmt.Errorf("dial %s: %w", socketPath, err)
        }

        timer := time.NewTimer(interval)
        select {
        case <-ctx.Done():
            timer.Stop()
            return nil, fmt.Errorf("dial %s: %w", socketPath, err)
        case <-timer.C:
        }
    }
}
```

Three exit doors, all preserving the original error via `fmt.Errorf
("dial %s: %w", socketPath, err)` — identical wrap to today's `dial()`,
which is what AC #2 ("same error message users get today") demands.

Loop shape notes:

- **First attempt is immediate.** No pre-sleep. The fast-path "daemon
  is up" stays one-syscall-fast (one `connect(2)` + return).
- **Deadline check sits between the predicate check and the sleep.** A
  transient error at t = budget exits with the wrapped error rather
  than sleeping again — that's how the budget is actually enforced.
  The `!time.Now().Before(deadline)` form (rather than `After`) handles
  the boundary tick correctly: at t = deadline exactly we exit.
- **`time.NewTimer` + `Stop()` on ctx cancel** rather than `time.After`
  — `time.After` leaks a timer per cancel. The leak is bounded
  (`interval` = 50ms) but harmless to fix.
- **`ctx.Done()` short-circuits the sleep, not the dial call.** A
  caller-side context cancel during the sleep returns immediately with
  the most recent transient error wrapped (not `ctx.Err()`); during a
  dial attempt the cancel propagates through `net.Dialer.DialContext`
  per the stdlib contract and returns from `fn` as a wrapped
  `context.Canceled` — that's not a transient startup error, so the
  next loop iteration takes the non-transient exit immediately.

### Imports

`dial.go` after this ticket:

```go
import (
    "context"
    "errors"
    "fmt"
    "net"
    "syscall"
    "time"
)
```

(`errors` and `syscall` were already there for the predicate. `context`,
`fmt`, `net`, `time` move in with `dial`.)

`client.go` loses its `net` import after `dial` moves out.

## Concurrency model

None new. `dialWithRetry` is a synchronous loop driven by
`time.NewTimer` and `ctx.Done()` — exactly the same shape as the
existing per-call dial logic, just iterated. No goroutines spawned, no
shared state, no mutexes.

The retry is per-`dial()`-call. Two concurrent `pyry status` invocations
each run their own retry loop independently — no rendezvous, no shared
counter. This matches the existing per-call model.

## Error handling

- **Non-transient error** (per `isTransientStartupError == false`):
  return immediately, wrapped as today. No retry. AC #4 explicit.
- **Transient error, budget remaining**: sleep `interval`, loop.
- **Transient error, budget exhausted**: return wrapped, exact same
  error message as today. AC #2 explicit.
- **`ctx` cancelled / deadline exceeded mid-sleep**: return wrapped
  most-recent-transient error. The caller already chose the deadline;
  honoring it ahead of the budget is correct.
- **`fn` returns `(nil, nil)`** (a buggy fake): treated as success and
  the nil conn is returned. The production primitive never does this;
  test fakes shouldn't either.

No new sentinel errors. No `errors.Is` checks beyond the predicate's own.
The error wrap site is a single line, replicated three times for the
three exit doors — keep them identical so a future grep on the wrap
string finds all three.

## Testing strategy

### Layout

Add three table-driven cases (or three separate `t.Run` blocks if a
single table doesn't fit cleanly) to `internal/control/dial_test.go`,
under a new `TestDialWithRetry` function. Keep the existing
`TestIsTransientStartupError` untouched.

The fake dialer is a small closure that counts calls and returns a
caller-provided sequence of errors (or `nil` for success). Sketch:

```go
type fakeDialer struct {
    calls int
    seq   []error // nil entry means "succeed and return a fake conn"
}

func (f *fakeDialer) dial(ctx context.Context, socketPath string) (net.Conn, error) {
    i := f.calls
    f.calls++
    if i >= len(f.seq) {
        return nil, syscall.ENOENT // default: keep returning ENOENT
    }
    if f.seq[i] == nil {
        // Successful "connection." A pipe is the cheapest stdlib net.Conn
        // — both ends close cleanly without a real listener.
        c1, c2 := net.Pipe()
        _ = c2.Close()
        return c1, nil
    }
    return nil, f.seq[i]
}
```

`net.Pipe` gives a valid `net.Conn` for the success-case assertion
without a listener. The test closes the returned conn before
returning.

### Cases

The three AC-mapped cases, plus the timing assertion AC #6 demands.

**1. Recovers after N transient failures.** Fake returns `ENOENT` N
times then succeeds. `dialWithRetry` is called with
`budget = 200ms, interval = 10ms` (NOT the production constants —
shorter so the test runs in <50ms instead of <1.5s). Pick `N = 3`.
Assertions:
- Returned conn is non-nil, error is nil.
- `f.calls == N + 1`.
- Elapsed wall-clock ≤ `N*interval + 50ms` slack. (AC #6 says "+~5ms";
  use 50ms in the test assertion — CI is loaded enough that 5ms is
  flaky and the AC's "~5ms" is the architect's intent for the loop's
  steady-state, not the assertion's wall-clock margin. Document the
  slack in a code comment so a future reader knows why it's not 5ms.)
- Don't forget to close the returned conn before exiting the test.

**2. Always-ENOENT exhausts the budget.** Fake always returns ENOENT.
`dialWithRetry` called with `budget = 100ms, interval = 10ms`.
Assertions:
- Returned conn is nil, error is non-nil.
- `errors.Is(err, syscall.ENOENT)` — the wrap chain still surfaces the
  original errno. (Pins AC #2's "same error message users get today".)
- Error string contains `"dial /fake/path:"` — pins the wrap prefix.
- Elapsed wall-clock in `[budget, budget + 50ms]`. Lower bound proves
  retries actually ran for the full budget; upper bound caps slack.

**3. Non-transient error fails immediately.** Fake returns `io.EOF` on
the first call. `dialWithRetry` called with the production constants
(`dialRetryBudget`, `dialRetryInterval`) — failure must be immediate
regardless of the budget, so it's safe to use the real values here
without slowing the test.
Assertions:
- Returned conn is nil, error is non-nil.
- `f.calls == 1` (no retry).
- `errors.Is(err, io.EOF)`.
- Error string contains `"dial /fake/path:"`.
- Elapsed ≤ 50ms (sanity).

### What NOT to test

- Don't test against a real `net.Dial` here. The retry behavior is
  driven by `dialFunc` injection; the real dial is exercised by every
  existing e2e test that calls `pyry status` etc. Out of scope per the
  PO ticket's "Integration coverage via existing `pyry status` e2e
  once both slices land — not needed in this ticket."
- Don't test that `time.Sleep` works. Don't test that `ctx.Done()`
  fires. Stdlib.
- Don't add a "context cancelled mid-loop" case unless the developer
  finds it irresistible — it's not in the AC and the production
  surface that triggers it (`pyry status -timeout=Xms` style) doesn't
  exist yet.

### Timing tolerance

Tests assert wall-clock elapsed against `time.Now()` deltas. Allow
50ms slack for case 1 and 2 — modest enough that a regression to "no
retry" or "wrong budget" still fails the test, generous enough that a
loaded CI doesn't flake. Document the slack with a `// +50ms slack
for CI scheduling` comment so a future reader knows why it isn't 5ms
or 1ms.

If the developer sees real flakes in CI under `-race`, raise the slack
to 100ms — `-race` slows everything ~2x and the dial loop with N=3
attempts at 10ms intervals is dangerously close to the noise floor.

## Open questions

None. Sizes XS, the seam is clear, the timings are deterministic
modulo CI slack, and the predicate from #198 does the heavy lifting.
Developer should write the move + helper + three tests and ship.

## Acceptance check (for the developer)

Walk down the AC list before pushing:

- [ ] AC#1 `pyry status` after `launchctl kickstart -k` succeeds —
  exercised in production by every client verb routing through `dial`;
  no e2e in this ticket.
- [ ] AC#2 always-ENOENT fails at ~1.5s with the original message —
  test case 2 + `errors.Is(err, syscall.ENOENT)` + wrap-prefix assertion.
- [ ] AC#3 ≤1.5s budget, 50ms interval, ~30 attempts — production
  constants `dialRetryBudget = 1500*time.Millisecond`,
  `dialRetryInterval = 50*time.Millisecond`.
- [ ] AC#4 retry only on `isTransientStartupError == true` — verify
  by inspection of `dialWithRetry`'s body; test case 3 pins
  non-transient = no retry.
- [ ] AC#5 client-side only, server unchanged — verify no edits under
  `internal/control/server.go` or any handler file.
- [ ] AC#6 unit test: ENOENT N times then success — test case 1.
- [ ] AC#7 unit test: always ENOENT → ~1.5s failure preserving message
  — test case 2.
- [ ] AC#8 unit test: non-transient (EOF) → immediate failure — test
  case 3.

`go test -race ./internal/control/...`, `go vet ./...`, and
`staticcheck ./...` must pass. The wider e2e suite is unchanged in
this ticket — if it fails, the retry has a bug.
