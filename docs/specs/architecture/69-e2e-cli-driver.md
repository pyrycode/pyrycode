# Spec — E2E harness: CLI driver + first feature-flavoured e2e (#69)

## Context

#68 delivered the spawn + cleanup primitive (`internal/e2e.Harness`, `Start(t)`,
build-tag isolated under `//go:build e2e`). It exposes `SocketPath`, `HomeDir`,
`PID`, `Stdout`, `Stderr`, and proves the daemon comes up and shuts down. What
it doesn't yet expose is a way to *talk* to the daemon — every consumer would
have to re-implement `exec.Command(pyryBin, "status", "-pyry-socket="+h.SocketPath, ...)`,
and the question of which binary to use (the cached one from `ensurePyryBuilt`)
isn't addressable from outside the package.

This ticket layers a generic CLI driver onto the existing `*Harness` and
ships one concrete e2e test (`pyry status` round-trip) so:

1. Phase 1.1's session-verb tickets (#52, #54, #55, #56) have a working
   pattern to copy.
2. The daemon-is-actually-responsive contract (beyond "socket is dialable")
   is verified end-to-end.

The supervised "claude" is `/bin/sleep infinity` (chosen by #68 so the
readiness gate doesn't depend on a real claude binary), so this ticket's
test must pick a verb that talks **only** to the daemon's control socket
— not one that interrogates child state. `pyry status` qualifies (it just
asks the daemon for its in-memory phase + restart count); `pyry version`
doesn't even dial the socket (it short-circuits in `main.go:144`), so it
wouldn't exercise the socket plumbing the harness sells.

Split from #51.

## Design

### Surface area added

```go
// (in internal/e2e/harness.go, all under //go:build e2e)

// RunResult is the outcome of a CLI invocation against the harness's daemon.
// All three fields are populated regardless of exit code; an erroring command
// still has its captured Stdout/Stderr available for assertion.
type RunResult struct {
    ExitCode int
    Stdout   []byte
    Stderr   []byte
}

// Run invokes the cached pyry binary with `<verb> -pyry-socket=<h.SocketPath> <args...>`,
// waits for it to exit, and returns its captured streams. The harness's
// socket path is auto-injected so callers don't thread it through.
//
// The verb is positional because pyry dispatches subcommands on os.Args[1]
// (see cmd/pyry/main.go:144) — flags must come *after* the verb.
//
// Fails the test (t.Fatalf) on exec failure (binary not found, fork error)
// or if the command runs longer than 10s. A non-zero exit code is *not* a
// test failure here — the caller asserts on RunResult.ExitCode.
func (h *Harness) Run(t *testing.T, verb string, args ...string) RunResult
```

Two new exported names: `RunResult` and `(*Harness).Run`. No new files. No
new package-level state. No new options struct.

### Method body shape

```go
func (h *Harness) Run(t *testing.T, verb string, args ...string) RunResult {
    t.Helper()
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    full := append([]string{verb, "-pyry-socket=" + h.SocketPath}, args...)
    cmd := exec.CommandContext(ctx, binPath, full...)
    cmd.Env = childEnv(h.HomeDir)

    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr

    err := cmd.Run()
    if ctx.Err() == context.DeadlineExceeded {
        t.Fatalf("e2e: pyry %s timed out after 10s\nstdout:\n%s\nstderr:\n%s",
            verb, stdout.String(), stderr.String())
    }

    var exitCode int
    switch e := err.(type) {
    case nil:
        exitCode = 0
    case *exec.ExitError:
        exitCode = e.ExitCode()
    default:
        t.Fatalf("e2e: pyry %s exec failed: %v", verb, err)
    }

    return RunResult{ExitCode: exitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}
```

Three deliberate choices:

1. **`binPath` is the package-level var set by `ensurePyryBuilt`.** That
   helper has already run by the time anyone holds a `*Harness` (it's
   called inside `Start` before `cmd.Start`), so reading `binPath`
   directly is safe — `sync.Once` guarantees the write happens-before
   any post-`Start` read. No need to plumb the path through `Harness`.

2. **`childEnv(h.HomeDir)` reused verbatim.** The CLI client doesn't
   *need* `HOME` redirection (we override the socket path explicitly
   with `-pyry-socket=`, which trumps `-pyry-name`/`PYRY_NAME` in
   `resolveSocketPath`), but reusing the same env keeps client and
   server symmetric and defends against a future `pyry` subcommand
   that incidentally reads from `~/`. Stripping `PYRY_NAME` is the
   load-bearing part — without it, a `PYRY_NAME=foo` from the
   operator's shell would be ignored here (because of `-pyry-socket`)
   but could matter for any future verb that resolves an instance by
   name independently of the socket path.

3. **`exec.CommandContext` with a 10s timeout.** `pyry status` itself
   uses a 5s socket-dial timeout (`runStatus` → `control.Status`); 10s
   on the wrapper gives the network a comfortable margin without
   letting a hung daemon stall a test indefinitely. `t.Fatalf` on
   timeout because a CLI verb that exceeds its own internal deadline
   indicates daemon unresponsiveness, which no caller can do anything
   useful with.

### Why `RunResult` (struct), not the tuple

The AC explicitly leaves this to architect's discretion. The struct wins on:

- **Future-proof.** If any consumer ever needs `Duration`, `Combined`
  (interleaved), or `OOMed bool`, those land as new fields with no call-site
  churn. A 3-tuple has no headroom.
- **Argument order at call sites.** `if r := h.Run(t, "status"); r.ExitCode != 0 { ... }`
  reads more naturally than juggling positional `code, out, errOut := ...`.
- **Naming the data.** `[]byte` for stdout vs stderr is order-sensitive in
  a tuple; named fields prevent the obvious mix-up.

Cost: 4 extra lines for the type declaration. Worth it.

### Why `Run(t, verb, args...)` and not `Run(t, args...)`

Pyry dispatches subcommands by string-matching `os.Args[1]` (`main.go:144`).
The verb cannot be a flag and must come first. Encoding that into the type
signature (`verb string, args ...string`) prevents the obvious footgun of
calling `h.Run(t, "-pyry-socket=other", "status")` and getting confusing
"unknown flag" output from the global `flag` package's default behaviour.

The downside — `h.Run(t, "version")` looks slightly redundant (no flags) —
is trivial. The signature is honest about the contract.

### Auto-injection ordering

Positional layout passed to `exec.Command`:

```
[binPath]
"status"                          // verb (caller-provided)
"-pyry-socket=" + h.SocketPath    // injected
<caller's args...>
```

The verb comes first because `os.Args[1]` dispatch requires it. The socket
flag goes second so caller args can append cleanly. Caller can pass
`-pyry-socket=somewhere-else` themselves; Go's `flag` package takes the
*last* value, so caller-override is naturally available without any
special-case logic in the harness. (Documented as "advanced use" in the
doc comment? No — overrides are obvious from the auto-inject placement;
don't pre-document things.)

### Doc-comment update (`Package e2e`)

The current doc comment in `harness.go` shows only the bare smoke pattern
(implicitly — `go test -tags=e2e ./internal/e2e/...`). The AC requires a
copy-pasteable usage example for the CLI driver. Replace the existing
package doc with:

```go
// Package e2e provides a test harness that spawns pyry as a real daemon
// in an isolated temp HOME, blocks until the control socket is dialable,
// and tears it down reliably on test cleanup.
//
// The package is build-tag isolated; default `go test ./...` does not
// compile it. Invoke with:
//
//	go test -tags=e2e ./internal/e2e/...
//
// Set PYRY_E2E_BIN to a pre-built pyry binary to skip the per-test-process
// `go build`.
//
// Typical usage — spawn a daemon and drive a CLI verb against it:
//
//	func TestStatusReportsRunning(t *testing.T) {
//	    h := e2e.Start(t)
//
//	    r := h.Run(t, "status")
//	    if r.ExitCode != 0 {
//	        t.Fatalf("pyry status exit=%d stderr=%s", r.ExitCode, r.Stderr)
//	    }
//	    if !bytes.Contains(r.Stdout, []byte("Phase:")) {
//	        t.Fatalf("status output missing Phase: line: %s", r.Stdout)
//	    }
//	}
//
// h.Run auto-injects -pyry-socket=<h.SocketPath> after the verb so callers
// don't thread it through. Exit code, stdout, and stderr are all available
// on the returned RunResult regardless of success.
```

### Concrete e2e test

Add to `harness_test.go`:

```go
// TestStatus_E2E spawns the daemon and exercises the pyry status verb
// end-to-end against its control socket. Asserts on a stable substring
// rather than exact whitespace so a future status-formatting change
// doesn't break the test.
func TestStatus_E2E(t *testing.T) {
    h := Start(t)

    r := h.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
            r.ExitCode, r.Stdout, r.Stderr)
    }
    if !bytes.Contains(r.Stdout, []byte("Phase:")) {
        t.Errorf("status stdout missing %q line:\n%s", "Phase:", r.Stdout)
    }
}
```

`"Phase:"` is the only substring asserted — it's the leading literal in
`runStatus`'s output (`fmt.Printf("Phase:         %s\n", resp.Phase)`,
`main.go:367`) and is stable across phase values, restart counts, and
any future field additions. Asserting on the value itself
(`PhaseRunning`, etc.) would couple the test to claude-child startup
timing, which is exactly what `/bin/sleep infinity` was chosen to avoid
— the daemon is up, the socket answers, the status verb round-trips.

## Concurrency model

Unchanged from #68. The new `Run` method is synchronous: caller goroutine
calls `cmd.Run()`, waits, returns. The 10s timeout context is the only
asynchrony, and `cancel()` is deferred. No new goroutines, no new shared
state.

## Error handling

| Failure | Response |
|---|---|
| `cmd.Run` returns `*exec.ExitError` | Return `RunResult` with non-zero `ExitCode`; not a test failure |
| `cmd.Run` returns any other error | `t.Fatalf` (exec/fork failure — caller can't recover) |
| 10s deadline expires | `t.Fatalf` with stdout + stderr (daemon-side hang) |
| `cmd.Run` returns nil | `RunResult` with `ExitCode = 0` |

The asymmetry between "non-zero exit" (returned, not fatal) and "exec
failed" (fatal) is intentional: a non-zero exit is *data the test
asserts on*; a fork failure is infrastructure breaking, with no useful
recovery in test code.

## Testing strategy

Two assertions cover the AC:

1. **`TestStatus_E2E`** (new) — asserts `Run` works end-to-end against
   a live daemon, exit code 0, stable substring in stdout. Validates
   the AC's "one concrete e2e test" + "tight enough to fail meaningfully
   on regression but loose enough not to over-fit current output
   formatting."

2. **`TestHarness_Smoke`** + **`TestHarness_NoLeakOnFatal`** (existing,
   from #68) keep passing unchanged. They exercise the spawn/teardown
   primitive that `Run` builds on; no need for a separate "Run cleans up
   correctly" test because `Run` is synchronous and stateless — every
   exec it issues is fully resolved before it returns.

No test for the timeout path. Constructing a daemon that hangs `pyry
status` for >10s requires either a real claude that doesn't respond
or test-only socket injection — both significantly more invasive than
the safety net buys us. The 10s `context.DeadlineExceeded` branch is
defensive; per "evidence-based fix selection" we don't ship a regression
test for a failure mode that has not been observed.

CI: untouched. The existing `go test ./...` job remains unaware of
`-tags=e2e`. Wiring an e2e CI lane is a separate ticket (still
out-of-scope per #68's deferral list).

## Open questions

None resolved by sketching that affect implementation.

- **PYRY_E2E_BIN race.** If two tests in the same process run in parallel
  and call `Start(t)` for the first time, `sync.Once` serialises the
  build correctly. The added `Run` method reads `binPath` after `Start`
  has returned, so `sync.Once`'s happens-before guarantee covers this.
  No new race surface.

- **Why no `Run` overload that passes stdin.** No verb in the current CLI
  reads stdin (`status`, `stop`, `logs`, `attach`, `install-service` are
  all flag/socket driven). Add it when a verb that reads stdin lands.

- **Why no `Run`-without-socket variant for `pyry version`.** Only `version`
  doesn't need the socket, and `pyry version -pyry-socket=X` happily ignores
  the flag (it returns before parsing it). Auto-injection is harmless for
  the one verb that doesn't use it; no overload needed.

## Out of scope

- `Harness.Status()`, `Harness.Stop()`, `Harness.Attach()` typed wrappers
  (#52, #54, #55, #56 will introduce these per-verb if they're useful).
- An `Option`s/`WithFoo` constructor pattern.
- Stdin plumbing on `Run` (no current verb needs it).
- CI wiring (`make e2e`, GitHub Actions matrix) — same deferral as #68.
- Race-mode harness build (PYRY_E2E_BIN built without `-race`) — same
  deferral as #68.
