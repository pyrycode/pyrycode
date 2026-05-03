---
ticket: 127
title: e2e attach detaches cleanly leaving daemon and child alive
status: spec
size: XS
---

# Files to read first

- `internal/e2e/attach_pty.go` — full file (~233 lines). The PTY harness from #125 you extend. Pay particular attention to:
  - L28-53 — `AttachHarness` struct shape; you add two methods, no new fields.
  - L66-142 — `StartAttach` body. The `attachCmd` and `attachDone` channel are already wired. `attachDone` closes when the attach client's `Wait()` returns; you call into this to assert clean detach.
  - L209-232 — `teardown`. `killSpawned` is idempotent; if the attach client has already exited cleanly, teardown becomes a no-op for that side. No teardown changes needed.
- `internal/e2e/harness.go:475-505` — `Harness.Run`. The body you mirror for `AttachHarness.Run`. Note `binPath` is a package var populated by `ensurePyryBuilt` (L106-141); both harnesses share the cache.
- `internal/e2e/harness.go:421-435` — `childEnv`. AttachHarness already calls this for daemon + attach spawn (`attach_pty.go:115`); the new `Run` method calls it the same way for the post-detach `pyry status`.
- `internal/e2e/idle_test.go:31-37, 88-92` — the `Phase:         running` substring assertion you mirror. Note the multi-space gap (column-aligned status output); `bytes.Contains` against `[]byte("Phase:         running")` matches the literal.
- `internal/e2e/attach_pty_test.go` — the round-trip test from #125 in the same package. Look at how `StartAttach(t, "")` is called and how `Master.Write` drives bytes into the PTY. The new test follows the same shape but writes the detach sequence instead of a payload.
- `cmd/pyry/main.go:440-474` — `runAttach`. Confirms `Ctrl-B d` is consumed by the attach client (specifically, by `control.Attach` underneath) and that on clean detach the function returns nil → exit 0. No production-code changes; this read is to confirm the contract the test asserts.

# Context

Today the only e2e coverage of `pyry attach` is `TestE2E_Attach_RoundTripsBytes` (#125) — bytes flow terminal → daemon → child → back. There is no test asserting that the documented `Ctrl-B d` detach sequence cleanly disconnects the attach client without taking down the daemon or its supervised child.

That triple invariant — attach exits 0, daemon survives, supervised child survives — is the load-bearing property of detach. A regression in any one of the three (e.g. attach kills the daemon on detach, or the bridge half-closes and stalls the child) silently breaks user workflow. This ticket adds the missing test, building on the PTY harness primitive shipped in #125.

Out of scope: detach behaviour for non-bootstrap sessions, detach under contention (concurrent attach), keystroke unbinding (e.g. binding the prefix to something other than `Ctrl-B`).

# Design

## Approach

Three small pieces, all in `internal/e2e`:

1. Two new methods on `*AttachHarness` (`attach_pty.go`):
   - `WaitDetach(t, timeout)` — block until the attach client exits or the deadline elapses; return its exit code. Failing the test on timeout.
   - `Run(t, verb, args...)` — mirror of `Harness.Run` against the attach harness's daemon socket + HOME. Used by the test to invoke `pyry status`.
2. One new e2e test, `TestE2E_Attach_DetachesCleanly`, in a new file `internal/e2e/attach_detach_test.go` (build-tagged `e2e`). It uses `StartAttach`, writes `\x02d` into the PTY master, calls `WaitDetach`, then runs `pyry status` twice (daemon liveness + supervised child phase) via `AttachHarness.Run`.

No changes to `cmd/pyry`, no changes to `internal/control`, no changes to `internal/supervisor`. No new exported types. No new packages.

## Public surface (delta only)

```go
// In internal/e2e/attach_pty.go, methods on the existing AttachHarness:

// WaitDetach blocks until the attach client process exits or timeout
// elapses, then returns its exit code. Fails the test on timeout
// (the deadline is the AC#2 invariant — clean detach should be near-
// instant; a generous timeout here masks a hung detach).
//
// Safe to call exactly once after writing the detach sequence to Master.
// Subsequent calls return the same exit code (the attachDone channel
// stays closed; ProcessState is set).
func (a *AttachHarness) WaitDetach(t *testing.T, timeout time.Duration) int

// Run invokes the cached pyry binary against this harness's daemon
// socket with HOME=a.HomeDir and the verb's args appended. Mirrors
// Harness.Run — same auto-injection of -pyry-socket=, same RunResult
// shape, same timeout. Used by tests that need to drive a CLI verb
// against the same daemon the attach client is bound to.
func (a *AttachHarness) Run(t *testing.T, verb string, args ...string) RunResult
```

`RunResult` (already defined in `harness.go`) is reused verbatim — no second copy.

## Implementation notes

### Sharing Run with Harness

`Harness.Run` (harness.go:475-505) and the new `AttachHarness.Run` differ only in which struct fields they read (`SocketPath`, `HomeDir`). The body — `exec.CommandContext(ctx, binPath, ...)`, `childEnv(home)`, stdout/stderr capture, exit-code switch, deadline handling — is identical.

Extract the shared body into a package-private free function:

```go
// runVerb invokes the cached pyry binary against socket with the verb
// auto-injecting -pyry-socket=. Used by both Harness.Run and
// AttachHarness.Run.
func runVerb(t *testing.T, socket, home, verb string, args ...string) RunResult
```

Both `Harness.Run` and `AttachHarness.Run` become 3-line wrappers (`return runVerb(t, h.SocketPath, h.HomeDir, verb, args...)`). Net effect: ~25 lines move out of `Harness.Run`, both methods become trivial. No behaviour change for existing `Harness.Run` callers.

This refactor is bounded — `Harness.Run` has no other callers outside the harness package; the rename is private and `gofmt`-clean. Skip if grep reveals any external caller; duplicate the body instead. A 25-line duplication is acceptable for an XS ticket.

### WaitDetach implementation

`AttachHarness.attachDone` is already a `chan struct{}` closed by the goroutine running `attachCmd.Wait()` in `StartAttach` (attach_pty.go:120-124). After it closes, `attachCmd.ProcessState` is populated by Go's `exec` package and `.ExitCode()` is safe to read.

```go
func (a *AttachHarness) WaitDetach(t *testing.T, timeout time.Duration) int {
    t.Helper()
    select {
    case <-a.attachDone:
        if a.attachCmd.ProcessState == nil {
            t.Fatalf("attach process state nil after Wait")
        }
        return a.attachCmd.ProcessState.ExitCode()
    case <-time.After(timeout):
        t.Fatalf("attach client did not exit within %s after detach", timeout)
        return -1 // unreachable
    }
}
```

No new locking — `attachDone` is closed exactly once by the StartAttach goroutine, which is a memory-order barrier for `ProcessState`.

## Test design

```go
// internal/e2e/attach_detach_test.go
//go:build e2e

package e2e

import (
    "bytes"
    "testing"
    "time"
)

// TestE2E_Attach_DetachesCleanly writes the documented Ctrl-B d
// detach sequence into a live attach session and asserts:
//   1. the attach client exits 0 within a generous deadline,
//   2. the daemon survives (pyry status against the same socket
//      returns exit 0),
//   3. the supervised child is still in Phase: running.
//
// The PTY availability skip lives in StartAttach; this test does not
// re-probe.
func TestE2E_Attach_DetachesCleanly(t *testing.T) {
    a := StartAttach(t, "")

    // Detach sequence: Ctrl-B (0x02) then 'd' (0x64).
    if _, err := a.Master.Write([]byte{0x02, 0x64}); err != nil {
        t.Fatalf("write detach sequence: %v", err)
    }

    if exit := a.WaitDetach(t, 5*time.Second); exit != 0 {
        t.Fatalf("attach exit=%d, want 0", exit)
    }

    r := a.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("daemon dead after detach: pyry status exit=%d stderr=%s",
            r.ExitCode, r.Stderr)
    }
    if !bytes.Contains(r.Stdout, []byte("Phase:         running")) {
        t.Fatalf("supervised child not running after detach\nstdout:\n%s",
            r.Stdout)
    }
}
```

Acceptance-criteria mapping:
- AC#1 — `StartAttach(t, "")` + the two-byte write into `a.Master`.
- AC#2 — `WaitDetach(t, 5*time.Second)` + the exit-code assertion.
- AC#3 — `a.Run(t, "status")` and the `ExitCode != 0` check.
- AC#4 — the `Phase:         running` substring assertion (literal, multi-space).
- AC#5 — already satisfied by `StartAttach`'s `pty.Open` skip (attach_pty.go:71-74). The new test inherits it for free.

# Concurrency model

No new goroutines. The two existing background goroutines — daemon `Wait()` (attach_pty.go:179-183) and attach client `Wait()` (attach_pty.go:120-124) — are unchanged.

Synchronization for `WaitDetach`:
- `attachDone` close is the synchronization point. Channel close is a happens-before edge; reads of `attachCmd.ProcessState` after the receive on `attachDone` see the writes performed by `attachCmd.Wait()` in the closing goroutine.
- The `time.After` branch leaks no resources beyond the `time.Timer`, which the runtime GCs after firing.

Ordering for the test:
- Write detach bytes into `Master` → kernel PTY buffer.
- Slave-side reader in attach client (raw stdin) reads the bytes; `control.Attach` sees the prefix-key state machine match `Ctrl-B d`, returns nil from its bridge loop.
- `runAttach` writes the "detached." line and returns nil → process exits 0 → `attachCmd.Wait()` returns → `attachDone` closes.
- `WaitDetach` unblocks, reads ExitCode.
- `pyry status` runs against the still-alive daemon. The daemon's bridge unbind happens before the attach client returns (control.Attach side handshakes detach with the daemon before exiting), so by the time `pyry status` runs the supervisor is back to "no attached client" and the supervised child continues to run.

No timing assumptions beyond AC#2's 5s budget.

# Error handling

| Failure mode | Handling |
|---|---|
| `pty.Open` fails (sandboxed CI) | `StartAttach` calls `t.Skip` (already shipped in #125; AC#5). |
| Detach sequence write fails | `t.Fatalf` immediately. PTY master writes don't block under any practical buffer size for two bytes. |
| Attach client doesn't exit within 5s | `WaitDetach` calls `t.Fatalf`. The `t.Cleanup` registered by `StartAttach` still runs, killing both the hung attach client and the daemon. |
| Attach client exits with non-zero | `t.Fatalf` with the exit code. `daemonErr` buffer is not surfaced here (the daemon may still be alive); if a follow-up debug pass needs daemon stderr, the `pyry status` call will fail and surface it. |
| Daemon died despite detach | `pyry status` returns non-zero (socket dial fails). `t.Fatalf` includes stderr — operator sees the dial failure or any other diagnostic. |
| Supervised child crashed despite detach | `pyry status` returns 0 but the substring miss fires `t.Fatalf` with the full stdout. |
| `pyry status` hits the 10s `runTimeout` | `runVerb` `t.Fatalf`s with the timeout message. Existing behaviour from `Harness.Run`. |

No retry loops. The triple invariant is "true within a deadline"; if it fails, surface the failure and let teardown reclaim resources.

# Testing strategy

The test is itself the verification. No new unit tests for `WaitDetach` or `runVerb` — they're test infrastructure exercised by the e2e test that consumes them. Local verification:

```bash
go test -tags=e2e -race -run TestE2E_Attach_DetachesCleanly ./internal/e2e/...
go test -tags=e2e -race ./internal/e2e/...   # confirm no regression in #125's round-trip test
```

Expect: both green on macOS dev hosts. CI without a usable PTY hits the `StartAttach` skip path and reports SKIP, not FAIL.

Soak guidance: run the new test under `-count=20` locally to flush out any timing flakiness in the detach path before merging. The 5s budget should give 1-2 orders of magnitude of headroom on observed steady-state detach latency (single-digit ms).

# Open questions

- **None blocking.** The implementation surface is small and concrete; the failure modes are well-understood; the contract under test is documented in `cmd/pyry/main.go:468`.

# Out of scope (for follow-ups)

- Detach against a non-bootstrap session (`StartAttach` accepts `sessionID` but the test passes `""` — a follow-up could parameterize this once Pool exposes named sessions in the harness).
- Behaviour when the user holds `Ctrl-B` but never types `d` (prefix-timeout / passthrough). That's a `control.Attach` unit-test concern, not e2e.
- Bridge-busy semantics on a second concurrent attach. Tracked separately.
