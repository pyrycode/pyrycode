---
ticket: 128
title: e2e attach client survives a claude restart
status: spec
size: XS
---

# Files to read first

- `internal/e2e/attach_pty_test.go` — full file (~120 lines). The home of `TestHelperProcess` (the "stub-claude" the ticket asks us to extend) and the round-trip test from #125 you mirror. Extract:
  - L18-49 — `TestHelperProcess` + the `echo` mode body. **You extend this in place** — emit a startup marker, scan for `__EXIT__\n`, exit non-zero on the trigger.
  - L51-68 — `TestE2E_Attach_RoundTripsBytes`. Shape of the new test mirrors this one: `StartAttach(t, "")`, `Master.Write`, `readUntilContains`.
  - L70-119 — `tinyNonce` and `readUntilContains`. Both reused verbatim by the new test. `readUntilContains` is the right primitive for "wait for these bytes back" with a deadline; the startup-marker reader follows the same shape with regex matching instead of literal contains.
- `internal/e2e/attach_pty.go` — full file (~265 lines). The harness from #125 (extended in #127). You add **no new fields, no new methods** — the new test consumes existing surface (`a.Master`, `a.attachDone`, `a.attachCmd`) directly. Worth re-reading:
  - L28-53 — `AttachHarness` struct shape; the test reaches into `attachDone` / `attachCmd` for the liveness assertion (same package, no exports needed).
  - L66-142 — `StartAttach`. Confirms the helper inherits `GO_TEST_HELPER_PROCESS=1` + `GO_TEST_HELPER_MODE=echo` via `spawnAttachableDaemon`'s helper-env wiring; the supervised child re-execs the test binary on every spawn → every respawn re-runs `TestHelperProcess`'s `echo` arm afresh, getting a new PID and a fresh emit of the startup marker.
  - L209-229 — `WaitDetach`. Pattern for "block on `attachDone` with timeout"; the inverse "non-blocking is-attach-alive check" the new test does inline.
- `internal/supervisor/supervisor.go:148-213` — `Run` loop. Confirms the respawn semantics: after `runOnce` returns (child exited), the loop transitions to `PhaseBackoff`, sleeps `delay`, then re-enters `runOnce` with a fresh `pty.Start` and a new child PID. Bridge mode (L246-268) re-runs the two `io.Copy` goroutines on the new ptmx; `s.cfg.Bridge` persists across iterations.
- `internal/supervisor/supervisor.go:226-268` — `runOnce` bridge path. Confirms `cmd.Env = append(os.Environ(), s.cfg.helperEnv...)` re-applies on every iteration, so the supervised helper sees `GO_TEST_HELPER_PROCESS=1` again on respawn (no re-wiring needed in the test).
- `internal/supervisor/backoff.go` — full file (46 lines). `BackoffInitial = 500ms` default; `next` doubles per iteration. The first respawn delay is exactly 500ms — the AC#2 budget of ≥5s is one order of magnitude of headroom.
- `internal/supervisor/bridge.go:27-82` — `Bridge` shape. Single `io.Pipe` persists across child restarts. Writes from the attach client during the backoff window block on `pipeR` until the next `runOnce` resumes the `io.Copy(ptmx, bridge)` goroutine — no data loss, no special handling needed in the test.
- `docs/lessons.md` — re-read these three sections; they shape constraints in the design:
  - § "Daemon env flows through to the supervised child via supervisor.runOnce" — explains why bridge mode is NOT raw-mode and why the helper must `MakeRaw` itself (the existing `echo` arm already does).
  - § "PTY master fds on darwin do not support SetReadDeadline" — the new startup-marker reader cannot use `SetReadDeadline`; reuse `readUntilContains`'s caller-side timeout pattern.
  - § "PTY master backpressure stalls slave-side process exit" — irrelevant to this ticket because the supervisor's bridge keeps draining the master throughout (the `io.Copy(bridge, ptmx)` goroutine on the daemon side is the drain). Noted to confirm we are not re-introducing the #127 footgun.

# Context

Today the e2e suite has one test exercising `pyry attach` end-to-end: `TestE2E_Attach_RoundTripsBytes` (#125), which proves a single round-trip of bytes from the user's terminal to claude and back. There is no test asserting the *load-bearing* property of the supervisor's restart loop: when the supervised child exits and the supervisor respawns it, the attach client survives and the user's terminal session keeps working.

This is a real risk. A regression in the bridge's lifetime semantics (e.g. unintentionally tearing down the bridge when a child exits, half-closing the input pump on respawn, never re-binding the second `io.Copy(ptmx, bridge)`) would silently break the user's session — the attach client wouldn't crash, it would just stop seeing output. Unit tests in `internal/supervisor` cover the per-iteration mechanics; an e2e test is what catches "we wired the lifetimes wrong across iterations."

This ticket adds that test, building on the PTY harness from #125 (extended for `Run` in #127) and the existing `TestHelperProcess` "echo" stub. Per the ticket body, the existing stub is extended — not duplicated — with two minimal additions: a startup marker (so the test can distinguish "first child" from "second child") and a documented exit trigger (so the test can deterministically force a non-zero exit without racing).

Out of scope:
- Backoff escalation (the test asserts survival across a single restart, not N escalating ones).
- The supervisor's reset-on-stability path (`BackoffReset` is 60s; the test runs in <2s so it never trips).
- Concurrent-attach behaviour (covered separately by `ErrBridgeBusy` tests).

# Design

## Approach

Two small changes, both in `internal/e2e`:

1. **Extend `TestHelperProcess`'s `echo` mode** in `internal/e2e/attach_pty_test.go` with two behaviours:
   - On startup, emit a deterministic `PYRY_E2E_STARTED pid=<pid>\n` line so the test can observe each respawn distinctly and prove the new child has a different identity from the prior one (AC#3).
   - When stdin contains a complete `__EXIT__\n` line, exit with status 1 *before* echoing the trigger. Surfaces as `*exec.ExitError` to the supervisor → backoff → respawn (AC#1).
   - All other input continues to round-trip through `io.Copy`-equivalent line echo, preserving the contract `TestE2E_Attach_RoundTripsBytes` already relies on.

2. **Add one new e2e test**, `TestE2E_Attach_SurvivesClaudeRestart`, in a new file `internal/e2e/attach_restart_test.go` (build-tagged `e2e`). It uses `StartAttach`, drives a pre-restart round-trip, writes the exit trigger, observes the second startup marker, drives a post-restart round-trip, and asserts the attach client process is still alive.

No changes to `cmd/pyry`, `internal/control`, `internal/supervisor`, or `internal/e2e/harness.go`. No changes to `internal/e2e/attach_pty.go` (the harness from #125/#127 is reused as-is). No new exported types, no new packages.

The new test file is named `attach_restart_test.go` (not `restart_test.go`) because `internal/e2e/restart_test.go` already exists and tests *daemon* restart semantics (`TestE2E_Restart_PreservesActiveSessions` etc.). The two restart concepts are unrelated; keeping the names disjoint avoids confusion.

## TestHelperProcess `echo` mode — extended body

The existing arm:

```go
case "echo":
    if term.IsTerminal(int(os.Stdin.Fd())) {
        if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
            os.Exit(98)
        }
    }
    _, _ = io.Copy(os.Stdout, os.Stdin)
    os.Exit(0)
```

The replacement:

```go
case "echo":
    if term.IsTerminal(int(os.Stdin.Fd())) {
        if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
            os.Exit(98)
        }
    }
    // Startup marker: every spawn emits this exactly once before reading
    // any input. Restart-survival tests observe two distinct markers
    // (different PIDs) to prove the supervisor respawned us.
    fmt.Fprintf(os.Stdout, "PYRY_E2E_STARTED pid=%d\n", os.Getpid())

    // Line-buffered echo with a documented exit trigger. The trigger
    // must arrive as a complete line; on match we exit non-zero before
    // echoing it, so the supervisor sees the child crash and respawns.
    // Bytes that are not part of an __EXIT__ line round-trip via stdout
    // identically to io.Copy at line granularity.
    buf := make([]byte, 4096)
    var line []byte
    for {
        n, err := os.Stdin.Read(buf)
        for i := 0; i < n; i++ {
            b := buf[i]
            if b == '\n' {
                if string(line) == "__EXIT__" {
                    os.Exit(1)
                }
                line = append(line, b)
                if _, werr := os.Stdout.Write(line); werr != nil {
                    os.Exit(97)
                }
                line = line[:0]
            } else {
                line = append(line, b)
            }
        }
        if err != nil {
            // Flush any buffered partial line so a graceful EOF still
            // surfaces the in-flight bytes to the test.
            if len(line) > 0 {
                _, _ = os.Stdout.Write(line)
            }
            if err == io.EOF {
                os.Exit(0)
            }
            os.Exit(96)
        }
    }
```

Compatibility with #125's `TestE2E_Attach_RoundTripsBytes`:
- That test writes `pyry-attach-roundtrip-<nonce>\n` (a single line ending in `\n`) and reads back via `readUntilContains(payload)`. Line-buffered echo emits the line intact when `\n` arrives — identical observable behaviour.
- The new startup marker (`PYRY_E2E_STARTED pid=<pid>\n`) appears before the payload; `readUntilContains` slides past prefix bytes by design (it accumulates seen bytes and matches via `bytes.Contains`), so the extra prefix doesn't affect the existing assertion.
- Random nonce collision with `__EXIT__` is impossible (`tinyNonce` is 8 hex chars; the trigger string is `__EXIT__`).

Imports added to `attach_pty_test.go`: none. `fmt`, `os`, `io`, `term` are already imported.

## New test — `TestE2E_Attach_SurvivesClaudeRestart`

```go
//go:build e2e

package e2e

import (
    "fmt"
    "regexp"
    "syscall"
    "testing"
    "time"
)

// startupMarkerRe captures the PID emitted by TestHelperProcess's echo
// mode on every spawn. The supervisor re-execs the helper on each
// restart, producing a fresh PID per iteration; observing two distinct
// PIDs on the attach PTY is the test's proof of respawn (AC#3).
var startupMarkerRe = regexp.MustCompile(`PYRY_E2E_STARTED pid=(\d+)\n`)

// TestE2E_Attach_SurvivesClaudeRestart asserts the load-bearing
// invariant of the supervisor's restart loop: an attached `pyry attach`
// client remains usable across a supervised claude restart. The
// supervisor's bridge re-binds to the new PTY, the attach client stays
// alive, and bytes flow again.
//
// Sequence:
//   1. StartAttach — daemon up, attach client bound, helper running.
//   2. Read startup marker 1 (capture pid1) — proves first child is up.
//   3. Round-trip payload1 — proves pre-restart byte path.
//   4. Write __EXIT__\n — helper exits 1.
//   5. Read startup marker 2 (capture pid2 != pid1) — proves respawn (AC#3).
//   6. Round-trip payload2 within 5s — proves post-restart byte path
//      survived the bridge re-bind (AC#2).
//   7. Assert attach client process still alive (AC#4).
//
// PTY availability skip: inherited from StartAttach (AC#5).
func TestE2E_Attach_SurvivesClaudeRestart(t *testing.T) {
    a := StartAttach(t, "")

    pid1 := readStartupMarker(t, a.Master, 5*time.Second)

    payload1 := []byte("pre-restart-" + tinyNonce() + "\n")
    if _, err := a.Master.Write(payload1); err != nil {
        t.Fatalf("write payload1: %v", err)
    }
    if err := readUntilContains(a.Master, payload1, 5*time.Second); err != nil {
        t.Fatalf("pre-restart round-trip: %v", err)
    }

    if _, err := a.Master.Write([]byte("__EXIT__\n")); err != nil {
        t.Fatalf("write exit trigger: %v", err)
    }

    // Generous deadline: 500ms initial backoff + spawn + raw-mode setup
    // + first stdout flush. Default headroom is ~10x.
    pid2 := readStartupMarker(t, a.Master, 5*time.Second)
    if pid2 == pid1 {
        t.Fatalf("respawn produced same pid=%d; supervisor did not restart child", pid1)
    }

    payload2 := []byte("post-restart-" + tinyNonce() + "\n")
    if _, err := a.Master.Write(payload2); err != nil {
        t.Fatalf("write payload2: %v", err)
    }
    if err := readUntilContains(a.Master, payload2, 5*time.Second); err != nil {
        t.Fatalf("post-restart round-trip: %v", err)
    }

    // AC#4: the attach client must not have exited as a side effect of
    // its supervised child crashing and being respawned. Non-blocking
    // probe of the attachDone channel; if it has closed, the client
    // exited and we surface its exit code.
    select {
    case <-a.attachDone:
        exit := -1
        if a.attachCmd.ProcessState != nil {
            exit = a.attachCmd.ProcessState.ExitCode()
        }
        t.Fatalf("attach client exited unexpectedly (exit=%d) after child respawn", exit)
    default:
    }
}

// readStartupMarker reads from r until startupMarkerRe matches in the
// accumulated buffer or timeout elapses, returning the captured PID.
//
// PTY master fds on darwin reject SetReadDeadline (lessons.md
// § "PTY master fds on darwin"), so the timeout is enforced caller-side
// — same shape as readUntilContains. The reader goroutine left running
// on timeout is drained by the harness's teardown closing Master.
func readStartupMarker(t *testing.T, r interface {
    Read([]byte) (int, error)
}, total time.Duration) int {
    t.Helper()
    type readResult struct {
        buf []byte
        err error
    }
    ch := make(chan readResult, 1)
    var seen []byte

    read := func() {
        b := make([]byte, 4096)
        n, err := r.Read(b)
        ch <- readResult{buf: b[:n], err: err}
    }

    deadline := time.Now().Add(total)
    go read()
    for {
        select {
        case res := <-ch:
            if len(res.buf) > 0 {
                seen = append(seen, res.buf...)
                if m := startupMarkerRe.FindSubmatch(seen); m != nil {
                    pid, perr := strconvAtoi(string(m[1]))
                    if perr != nil {
                        t.Fatalf("parse pid %q: %v", m[1], perr)
                    }
                    return pid
                }
            }
            if res.err != nil {
                t.Fatalf("read startup marker: %v (seen %q)", res.err, seen)
            }
            go read()
        case <-time.After(time.Until(deadline)):
            t.Fatalf("startup marker not seen within %s; seen %d bytes: %q",
                total, len(seen), seen)
            return 0 // unreachable
        }
    }
}
```

Note: `strconvAtoi` is a thin wrapper to keep the import list minimal — in practice the spec uses `strconv.Atoi`. The `interface { Read([]byte) (int, error) }` shape is for documentation; the actual signature can take `*os.File` directly (matching `readUntilContains`). Pick whichever is simpler at implementation time.

Acceptance-criteria mapping:
- AC#1 — `StartAttach(t, "")`, payload1 round-trip, then `Master.Write([]byte("__EXIT__\n"))`.
- AC#2 — `readUntilContains(payload2, 5*time.Second)` after the second startup marker.
- AC#3 — `readStartupMarker` called twice, `pid2 != pid1` assertion.
- AC#4 — non-blocking select on `a.attachDone`.
- AC#5 — already satisfied by `StartAttach`'s `pty.Open` skip path; the new test inherits it.

# Concurrency model

No new persistent goroutines. The existing harness goroutines from #125 — daemon `Wait()` and attach client `Wait()` — are unchanged. The supervisor inside the daemon spawns a new pair of `io.Copy` goroutines on each `runOnce` iteration; that's existing behaviour, not something this ticket adds.

`readStartupMarker` reuses the goroutine-per-read pattern from `readUntilContains`: spin up a read goroutine, deliver result via a buffered(1) channel, time.After for the caller-side deadline. On timeout the in-flight reader is left blocked in `Read`; the harness's teardown closes Master, the kernel returns EIO, the goroutine drains and exits. Same lifecycle contract as `readUntilContains`, no new resource ownership concerns.

End-to-end byte path during the restart:

```
Test: Master.Write("__EXIT__\n")
  → kernel PTY → slave → attach client raw stdin → control.Attach client send
  → unix socket → daemon control.Server attach handler → bridge.pipeW
  → bridge.Read (consumed by supervisor's io.Copy(ptmx, bridge))
  → ptmx.Write → child stdin → helper detects "__EXIT__" line → os.Exit(1)
  → child exits → cmd.Wait returns *ExitError → runOnce returns
  → supervisor enters PhaseBackoff (500ms)
  → supervisor calls runOnce again → pty.Start → new child PID
  → helper emits "PYRY_E2E_STARTED pid=<pid2>\n"
  → ptmx.Read → bridge.Write → bridge.output (attached out)
  → unix socket → attach client → slave → kernel PTY → master
Test: readStartupMarker captures pid2 from the master read
```

Bridge backpressure during backoff: the `Bridge` is a single `io.Pipe` whose reader (`bridge.Read`) is consumed by the supervisor's `io.Copy(ptmx, bridge)` goroutine. When the child exits and that goroutine returns, no one is reading `pipeR` until the next `runOnce` re-enters. If the test wrote payload2 *during* the backoff window (it doesn't — `readStartupMarker` blocks until pid2 appears, which by definition is after the new child is up), the write would block on the pipe until the new reader arrived. No data loss, no special handling needed.

Ordering guarantee that AC#3 relies on:
- The supervised helper writes `PYRY_E2E_STARTED pid=<new>\n` *before* it reads any stdin.
- The supervisor's `io.Copy(bridge, ptmx)` goroutine is active before any further `io.Copy(ptmx, bridge)` work happens (both started together in `runOnce`).
- So by the time `readStartupMarker` returns pid2, the supervisor's reverse pump is alive and the next write will reach the new child.

# Error handling

| Failure mode | Handling |
|---|---|
| `pty.Open` fails (sandboxed CI) | `StartAttach` calls `t.Skip` (AC#5; existing behaviour from #125). |
| Write to `Master` fails | `t.Fatalf` immediately. Two-byte-to-30-byte writes cannot block under any practical PTY buffer size. |
| Pre-restart round-trip times out | `t.Fatalf` from `readUntilContains`'s timeout branch with the seen-bytes context. Indicates a regression in the #125 round-trip path; the new test fails in roughly the same shape that #125's test would. |
| Child fails to exit on `__EXIT__\n` | `readStartupMarker` for pid2 times out; `t.Fatalf` with seen-bytes context. Operator sees the helper output that arrived; root-cause is "trigger not detected" or "supervisor didn't respawn." |
| Supervisor doesn't respawn (e.g. fatal supervisor error) | Same: `readStartupMarker` for pid2 times out. The harness's `daemonErr` buffer is captured for teardown diagnostics. |
| Same PID on respawn (impossible in practice on POSIX, but defensive) | `t.Fatalf` with the duplicate PID. A clear failure mode, not a flake. |
| Post-restart round-trip times out | `t.Fatalf` from `readUntilContains`'s timeout branch. Indicates the bridge re-bind broke (the load-bearing property this test exists to defend). |
| Attach client exited despite respawn | non-blocking select on `a.attachDone`; `t.Fatalf` with the captured exit code. |
| Helper hits an unexpected stdin read error | exit 96 from helper. Surfaces as a non-zero exit on the next `cmd.Wait`; supervisor still respawns, so the test would still get a startup marker, but with a confusingly-different PID and possibly extra output. Unlikely with `MakeRaw`'d PTY input under normal conditions. Not worth defensive handling. |

No retries. The deadlines are 5s — one order of magnitude above observed steady-state. A timeout is a real signal, not a flake to mask.

# Testing strategy

The new e2e test is itself the verification — there is no separate unit test for the helper extension. The helper's `echo` mode body is exercised end-to-end by both `TestE2E_Attach_RoundTripsBytes` (line-buffered echo) and `TestE2E_Attach_SurvivesClaudeRestart` (startup marker + exit trigger). Together they cover the helper's full surface.

Local verification:

```bash
# New test, race detector on.
go test -tags=e2e -race -run TestE2E_Attach_SurvivesClaudeRestart ./internal/e2e/...

# All e2e tests in the package — confirms #125's round-trip and #127's
# detach test still pass under the extended echo mode.
go test -tags=e2e -race ./internal/e2e/...

# Soak for timing flakiness before merging.
go test -tags=e2e -race -count=20 -run TestE2E_Attach_SurvivesClaudeRestart ./internal/e2e/...
```

Expected outcomes:
- macOS dev hosts: green on all three commands.
- CI without a usable PTY: skip (StartAttach's `pty.Open` skip), reported as SKIP not FAIL.
- Linux CI with PTY: green; round-trip latencies are an order of magnitude under the 5s deadline.

The test must not lower `BackoffInitial` from its 500ms default — per the ticket's tech notes, prefer a generous deadline over reaching into backoff config. The 5s budget is comfortable headroom on the default.

# Open questions

- **None blocking.** The implementation surface is small: ~30 lines extending the helper, ~80 lines for the test + reader helper. Failure modes are well-understood. The contract under test (bridge re-bind across child restart) is the supervisor's documented Run-loop semantics in `internal/supervisor/supervisor.go:148-213`.

# Out of scope (for follow-ups)

- Asserting backoff escalation across N consecutive crashes (this test forces exactly one restart).
- Asserting `BackoffReset` behaviour — the test runs in <2s, well under the 60s reset window.
- A second concurrent attach during the respawn window (separate `ErrBridgeBusy` story).
- A test for `__EXIT__` arriving as part of a longer chunk (e.g. `payload\n__EXIT__\n` in one Write). Not exercised by the AC; the helper's per-byte scan handles it correctly anyway.
