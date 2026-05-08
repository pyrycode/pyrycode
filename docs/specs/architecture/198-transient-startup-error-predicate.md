# #198 — `isTransientStartupError` predicate

**Size:** XS (architect-confirmed). One new function (~10 LOC of body), one new
table-driven test file. No new exported API. No callers — the retry wrapper
that consumes this predicate lands in #199.

**Status:** ready for development.

## Files to read first

The developer's turn-1 data load. Each entry is paged in deliberately —
don't grep for them.

- `internal/control/client.go:248-285` — existing `request()` and `dial()`
  helpers. `dial()` wraps the underlying `net.Dialer.DialContext` error with
  `fmt.Errorf("dial %s: %w", socketPath, err)`. **Critical:** the predicate
  must traverse through that `%w` wrap, plus the inner `*net.OpError` ⇢
  `*os.PathError`/`*os.SyscallError` ⇢ `syscall.Errno` chain. `errors.Is`
  does this automatically; verify the test exercises the wrapped shape.
- `internal/control/client_test.go` — existing test layout in this package.
  Table-driven, stdlib `testing` only, no `testify`. Mirror the conventions
  here for `dial_test.go`.
- `internal/control/server.go:248-258` — comment notes that "ECONNREFUSED
  returns instantly" on a missing/closed unix-socket peer. Useful framing
  for the test docstrings; no code to extract.
- `CODING-STYLE.md` § "Errors" — confirms the project convention:
  `errors.Is` / `errors.As` only, never string-matching on `err.Error()`.
  AC #5 restates this; CODING-STYLE is the source.
- `docs/lessons.md:284` — unrelated `ENOENT` lesson (about installer
  pre-flight Stat). Skim once to confirm there is no prior predicate this
  ticket should consolidate with; there isn't.

That's the read budget. Don't expand it — the function is ten lines and a
table.

## Context

After `launchctl kickstart -k` (manual restart) or `pyry update`'s
auto-restart (v0.10.1, landed via #190), client commands like `pyry status`
race the daemon binding the control socket. During the ~100 ms – 2 s
window between "old daemon exited" and "new daemon listening", a fresh
`net.Dial("unix", socketPath)` call returns one of two error shapes:

1. **Socket file absent** — daemon process gone, hasn't yet recreated the
   file. Goes to the kernel as `connect(2)` against a non-existent path,
   surfaces as `*net.OpError{Err: *os.PathError{Err: syscall.ENOENT}}`.
2. **Socket file present, no listener accepting** — daemon process up,
   bind not complete yet, OR a stale socket file from a previous crashed
   daemon. Surfaces as `*net.OpError{Err: *os.SyscallError{Err:
   syscall.ECONNREFUSED}}`.

Both shapes mean *"transient — try again in a few ms"*. Every other dial
error (timeout, EOF, protocol-level) means *"the daemon answered and
something is wrong"* and must surface immediately — retrying would mask
real failures.

#198 ships only the classification predicate. #199 wires it into the
dial path with a bounded retry loop.

## Design

### Placement

New file: **`internal/control/dial.go`**.

Rationale: the existing `dial()` helper lives at the bottom of `client.go`
(client.go:273-285). We do *not* move it — that's a refactor cascade and
out of scope. The new file `dial.go` houses only the new predicate (and
will house future dial-related helpers from #199 etc.). The
file-name/function pairing matches the PO ticket's suggested layout and
gives the test file (`dial_test.go`) an obvious home.

### Function signature

```go
// isTransientStartupError reports whether err matches the dial-error shape
// produced when the daemon is mid-restart: either the unix socket file
// does not exist yet (syscall.ENOENT) or the file exists but the daemon
// has not yet begun accepting connections (syscall.ECONNREFUSED).
//
// Returns false for nil, for any other syscall, and for higher-level
// failures (timeouts, EOF, protocol errors). #199 wires this into a
// bounded retry loop in the client dial path.
func isTransientStartupError(err error) bool
```

Unexported: the predicate's only intended consumer is the retry wrapper
in the same package. Exporting now would be speculative API surface.

### Body

```go
return errors.Is(err, syscall.ENOENT) ||
    errors.Is(err, syscall.ECONNREFUSED)
```

`errors.Is` walks the unwrap chain through `fmt.Errorf("...: %w", ...)`,
through `*net.OpError.Unwrap()`, through `*os.PathError.Unwrap()` /
`*os.SyscallError.Unwrap()`, and matches at the leaf `syscall.Errno`.
`errors.Is(nil, x)` is `false` for any non-nil sentinel `x`, so the nil
case falls out for free — no separate guard needed.

No string matching, no type assertion. The body is two `errors.Is` calls
in an `||`. Resist the urge to add `errors.As` plumbing for `*net.OpError`
to "verify the shape" — `errors.Is` matching against the leaf `Errno` is
the contract that matters; the wrapping shape is incidental.

### Imports

```go
import (
    "errors"
    "syscall"
)
```

That's it. No `net`, no `os`, no `io` in the production file — the
predicate operates entirely through the `error` interface.

## Concurrency model

None. Pure function. No goroutines, no `context.Context`, no mutexes.
This is restated as AC #4 explicitly because some prior tickets
(see PROJECT-MEMORY) added `ctx` to functions that didn't need it; the
PO ticket has pre-rejected that in scope.

## Error handling

The predicate is itself part of the error-handling layer; it does not
return errors. Misclassification semantics:

- **False negative** (real transient error not recognised) — the future
  #199 retry loop won't kick in; the client surfaces the dial error
  immediately. Same behaviour as today (no retry). Acceptable.
- **False positive** (permanent error misclassified as transient) —
  #199 would retry a hopeless dial up to its bounded budget, then
  surface. Slightly worse UX (slower failure) but not incorrect. AC #3
  pins the predicate to *only* the two named errnos, so the false-
  positive surface is bounded to those two — and they are the right two
  for the supervisor-restart use case.

No new sentinel errors, no error wrapping, no propagation concerns. The
predicate is a leaf consumer of errors, not a producer.

## Testing strategy

### Layout

New file: **`internal/control/dial_test.go`**. Single
`TestIsTransientStartupError` function, table-driven.

### Cases

The table has six rows mapped 1:1 to the AC list. Each row carries
`name`, `err`, `want`.

**True (transient) cases:**

1. `*net.OpError` from a real `net.Dial("unix", <nonexistent>)` —
   constructs the wrapped `*os.PathError{Err: ENOENT}` shape that the
   production code path actually produces. Use `t.TempDir()` to get a
   fresh temp directory, then dial `filepath.Join(dir, "missing.sock")`
   without ever creating the file. AC #1 mandates this construction
   (not synthesis) so the test pins the real-world shape.
2. Synthetic `&net.OpError{Op: "dial", Net: "unix", Err:
   &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}`.
   AC #2 doesn't mandate live construction. Synthetic is portable
   (Linux/macOS unix-socket lifecycle differs in whether `Listener.Close`
   removes the socket file), faster, and tests the exact unwrap path
   the production code traverses.

**False (non-transient) cases:**

3. `nil` — predicate must return `false` for the no-error case.
4. `io.EOF` — common protocol-level error, no syscall in the chain.
5. Synthetic `&net.OpError{Op: "dial", Err: context.DeadlineExceeded}`
   — timeout shape. Maps AC #3's "wrapping a timeout".
6. Plain `errors.New("kaboom")` — string error with no unwrap chain;
   pins that the predicate doesn't false-positive on opaque errors.

Six rows total; each row asserts `got == tt.want` with a clear
`t.Errorf("isTransientStartupError(%v) = %v, want %v", tt.err, got,
tt.want)` failure message.

### What NOT to test

- Don't test that `errors.Is` works (it's stdlib).
- Don't test `*net.OpError` traversal independently (covered transitively
  by case 1 above).
- Don't add a "live ECONNREFUSED via short-lived listener" case. It's
  flaky across OSes (macOS leaves the socket file on `Listener.Close`,
  Linux removes it) and adds nothing the synthetic case doesn't cover.
  If the developer is tempted, the test for case 1 already proves
  `errors.Is` walks the wrapper chain end-to-end against a real kernel
  error.

## Open questions

None. The PO ticket is fully scoped, the seam is clean, the predicate
shape is forced by the AC. Developer should write the function, write
the table, ship.

## Acceptance check (for the developer)

Before pushing, walk down the AC list and tick each box mentally
against the table-driven test:

- [ ] AC#1 ENOENT via real `net.Dial` — case 1.
- [ ] AC#2 synthetic ECONNREFUSED — case 2.
- [ ] AC#3 nil / EOF / timeout — cases 3, 4, 5.
- [ ] AC#4 pure (no I/O, no goroutine, no ctx) — verify by inspection;
  imports should be only `errors` and `syscall`.
- [ ] AC#5 `errors.Is` traversal, no string match — verify by inspection.
- [ ] AC#6 table-driven — assert by file shape.

`go test -race ./internal/control/...` and `staticcheck ./...` must pass.
