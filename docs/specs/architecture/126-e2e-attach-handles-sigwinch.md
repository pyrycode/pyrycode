---
ticket: 126
title: e2e pyry attach forwards SIGWINCH to claude
status: spec
size: XS
---

# Files to read first

- `internal/e2e/attach_pty_test.go` — full file (~170 lines). The site of every change in this ticket.
  - lines 49-101 — `TestHelperProcess` echo mode. The SIGWINCH watcher gets installed here, immediately after the `MakeRaw`+`PYRY_E2E_STARTED` bootstrap and before the stdin echo loop.
  - lines 122-128 — `tinyNonce` (reused as-is by no-op; nothing in this test needs nonces).
  - lines 138-171 — `readUntilContains` (reused verbatim by the new test; the new test searches for a literal `winsize rows=N cols=M\n` line, no nonce needed because the dimensions themselves are unique to this test).
- `internal/e2e/attach_pty.go` — full file (~275 lines). **No edits.** Confirms:
  - The harness exposes `Master *os.File` for tests to write/read against.
  - `StartAttach` already handles AC#4 (`t.Skip` on `pty.Open` failure, line 71-74).
  - `attachCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}` (line 121) makes the slave the attach client's controlling terminal — so SIGWINCH from `pty.Setsize(master, …)` lands on the attach client's process group, which is exactly what this test needs.
- `internal/control/attach_client.go:78-82, 196-232` — `startWinsizeWatcher` and the `Attach` wiring (#133, already merged). Confirms a SIGWINCH on the attach client process triggers `pty.GetsizeFull(os.Stdin)` → `SendResize(ctx, socketPath, sessionID, cols, rows)` to the daemon. **Critical:** the watcher's `read()` is `pty.GetsizeFull(os.Stdin)` against the attach client's stdin, which is the slave PTY. So when the test resizes the master, `GetsizeFull` returns the freshly-set dimensions, not stale ones.
- `internal/supervisor/bridge.go:261-271` — `Bridge.Resize`. Confirms the daemon-side path: server's `handleResize` (#137) → `Session.Resize` → `Bridge.Resize` → `pty.Setsize(b.ptmx, …)`. The supervisor's `ptmx` is the master end of the helper's PTY; `Setsize` raises SIGWINCH on the helper child.
- `internal/control/protocol.go:46-67` — `AttachPayload`. Confirms the handshake's `Cols=0, Rows=0` "sentinel" rule (no resize on zero dims). Load-bearing for the "what does the test see at startup?" analysis (see *Race scenarios*).
- `internal/supervisor/winsize.go` — full file (58 lines). Pattern reference for the helper's SIGWINCH watcher: `signal.Notify` + buffered chan(1) + goroutine + `pty.GetsizeFull(os.Stdin)` guarded by `term.IsTerminal`. The helper's watcher is the same shape minus the teardown plumbing (the helper exits on stdin EOF; signal.Stop is unnecessary).
- `internal/e2e/attach_restart_test.go` — full file. Two reusable patterns:
  - The `pty.Setsize` precedent is absent (no test uses it yet) — but the `regexp.MustCompile` + `readUntilContains` shape (lines 17, 40-42, 76-122) is the model for needle-matching against the master byte stream. The new test uses the simpler `readUntilContains([]byte(...))` since the literal `winsize rows=42 cols=117\n` is unambiguous.
  - Confirms the existing tests in this package match-by-substring (`bytes.Contains`); they will not flake on the *new* `winsize` lines this ticket introduces (see *Backwards compatibility*).
- `internal/control/resize_test.go:166-184` — `TestServer_Resize_ForegroundSessionSilent`. Pin: a Resize against a foreground session is silently swallowed. This test goes through bridge mode, not foreground, so the path is fully wired — but the spec acknowledges the foreground path here for completeness.
- `docs/lessons.md` § "PTY Testing" (lines 9-14) — the `t.Skip` discipline for non-TTY hosts. Already centralised in `StartAttach`; this ticket inherits it.
- `docs/specs/architecture/125-e2e-attach-pty-harness.md` (whole spec) — the immediately-prior architectural context. Read for shared vocabulary (`AttachHarness`, `readUntilContains`, "the slave is the attach client's controlling terminal").
- `docs/specs/architecture/133-attach-sigwinch-emitter.md` (whole spec) — the production-code counterpart that #126 covers. The watcher's behaviour in *that* spec's "Concurrency model" section (race-scenario table) is what *this* test exercises. No re-derivation needed.

# Context

`pyry attach`'s SIGWINCH→`SendResize`→`Bridge.Resize`→child-SIGWINCH chain landed in #133 (client) and #136 + #137 (daemon-side seam + wire). Coverage is unit-only:

- `internal/control/attach_winsize_test.go::TestStartWinsizeWatcher_SIGWINCHEmitsResize` pins SIGWINCH-to-`SendResize` at the watcher boundary with stub IO.
- `internal/control/resize_test.go::TestServer_Resize_AppliesToSeam` pins server-to-seam at the daemon boundary with a mock seam.

No test exercises the full path through compiled `pyry` and `pyry attach` binaries. This ticket lands the e2e cover: a real `pty.Setsize` on the harness's master fd, a real SIGWINCH delivered by the kernel to the attach client process group, and a real winsize observation by the supervised child via the supervisor's PTY slave. If any of the four halves regress (client watcher, wire, server applier, supervisor seam), the test fails.

This is the last of the four #57-derived attach e2e tickets (#125 round-trip, #127 detach, #128 restart, #126 SIGWINCH). It reuses the harness from #125 with no harness-side changes.

# Design

## Approach

**One file edit.** `internal/e2e/attach_pty_test.go` gains:

1. A SIGWINCH handler in `TestHelperProcess` echo mode that emits a deterministic `winsize rows=N cols=M\n` line on every signal.
2. A new test function `TestE2E_Attach_HandlesSIGWINCH` that calls `pty.Setsize` on the harness master, then waits for the new dimensions to appear in the master byte stream.

No harness changes. No new helper functions outside the test file. No new files.

## End-to-end trajectory

```
test                                     attach client (pyry)             daemon (pyry, bridge mode)             supervised helper (test binary, echo mode)
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
StartAttach(t, "")
  └─ harness up, attach client connected
  └─ helper child running, signal.Notify(SIGWINCH) installed
                                                                                                                  (initial geometry: handshake may
                                                                                                                   or may not fire Bridge.Resize
                                                                                                                   depending on slave default size)

pty.Setsize(a.Master,
   &pty.Winsize{Rows:42, Cols:117}) ───> kernel: TIOCSWINSZ on master
                                          delivers SIGWINCH to slave's
                                          foreground process group
                                          (= the attach client process
                                          via Setctty in StartAttach).

                                          startWinsizeWatcher's sigCh fires
                                          → read() = pty.GetsizeFull(os.Stdin)
                                            → (cols=117, rows=42, ok=true)
                                          → SendResize(ctx, sock, "", 117, 42)
                                                                          ─>   handleResize:
                                                                               cols=117, rows=42 → Session.Resize(42,117)
                                                                                                 → Bridge.Resize(42,117)
                                                                                                 → pty.Setsize(supervisor.ptmx, {42,117})
                                                                                                 ──────────────────────────────────────>   kernel: SIGWINCH to helper
                                                                                                                                          helper sigCh fires:
                                                                                                                                            pty.GetsizeFull(os.Stdin)
                                                                                                                                            → (rows=42, cols=117)
                                                                                                                                            → fmt.Fprintf(os.Stdout,
                                                                                                                                                "winsize rows=42 cols=117\n")
                                                                                                                                          bytes flow back through bridge
                                                                                                                                          to the attach client to the slave
                                                                                                                                          to the master.
readUntilContains(a.Master,
  []byte("winsize rows=42 cols=117\n"),
  5*time.Second)
  └─ matches → test passes
```

## File diff

### `internal/e2e/attach_pty_test.go` — extend `TestHelperProcess` echo mode

The single change to existing code is inside the `case "echo":` branch (currently at lines 55-96). Insert the SIGWINCH watcher between the `PYRY_E2E_STARTED` bootstrap line (current line 61) and the input loop (current line 63). Imports gain `os/signal`, `syscall`, and `github.com/creack/pty`.

```go
// TestHelperProcess godoc — extend the existing godoc with a paragraph on
// the new SIGWINCH behaviour. The existing godoc says "process exit on
// stdin EOF" etc.; add:
//
//   On every SIGWINCH the helper emits a deterministic line of the form
//   "winsize rows=N cols=M\n" (pty.Winsize field order, rows-first to
//   match Bridge.Resize / Session.Resize). TestE2E_Attach_HandlesSIGWINCH
//   uses this to observe live resize propagation. Other tests (round-trip,
//   detach, restart) match their own payloads via bytes.Contains, so the
//   extra line is harmless to them.

case "echo":
    if term.IsTerminal(int(os.Stdin.Fd())) {
        if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
            os.Exit(98)
        }
    }
    fmt.Fprintf(os.Stdout, "PYRY_E2E_STARTED pid=%d\n", os.Getpid())

    // SIGWINCH watcher. AC#3 of #126: a deterministic stdout marker per
    // signal so the test can match without races. Pattern mirrors
    // internal/supervisor/winsize.go: signal.Notify + buffered chan(1)
    // + goroutine + pty.GetsizeFull(os.Stdin) guarded by IsTerminal.
    //
    // No signal.Stop / no done channel: the helper exits on stdin EOF
    // (or os.Exit on __EXIT__), which tears down the goroutine
    // implicitly. The buffered chan(1) is the same coalescing posture
    // as the daemon-side watcher.
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGWINCH)
    go func() {
        for range sigCh {
            if !term.IsTerminal(int(os.Stdin.Fd())) {
                continue
            }
            size, err := pty.GetsizeFull(os.Stdin)
            if err != nil {
                continue
            }
            // Single Write per emission. *os.File serialises Write calls
            // at the FD level (poll.FD.fdmu), so this does not interleave
            // with the echo loop's os.Stdout.Write(line) calls.
            fmt.Fprintf(os.Stdout, "winsize rows=%d cols=%d\n", size.Rows, size.Cols)
        }
    }()

    // (existing input loop unchanged: __EXIT__ / __PID__ specials, line-
    //  buffered echo, exit on EOF.)
    buf := make([]byte, 4096)
    var line []byte
    for {
        n, err := os.Stdin.Read(buf)
        for i := 0; i < n; i++ {
            b := buf[i]
            if b == '\n' {
                switch string(line) {
                case "__EXIT__":
                    os.Exit(1)
                case "__PID__":
                    fmt.Fprintf(os.Stdout, "PYRY_E2E_STARTED pid=%d\n", os.Getpid())
                    line = line[:0]
                    continue
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

### `internal/e2e/attach_pty_test.go` — add `TestE2E_Attach_HandlesSIGWINCH`

Append after the existing `readUntilContains` (current line 171). Imports gain `github.com/creack/pty` (shared with the helper extension above).

```go
// TestE2E_Attach_HandlesSIGWINCH proves live SIGWINCH propagation through
// the full attach pipeline by resizing the harness's client-side master
// PTY and asserting the supervised child observes the new dimensions on
// stdout within a generous deadline.
//
// Path under test:
//   pty.Setsize(master) → kernel SIGWINCH on slave's process group (the
//   attach client) → startWinsizeWatcher → SendResize → server
//   handleResize → Session.Resize → Bridge.Resize → pty.Setsize on
//   supervisor's ptmx → kernel SIGWINCH on helper → helper emits
//   "winsize rows=42 cols=117\n".
//
// The PTY availability skip lives in StartAttach; this test does not
// re-probe.
func TestE2E_Attach_HandlesSIGWINCH(t *testing.T) {
    a := StartAttach(t, "")

    // Pick dimensions unlikely to match the slave's default initial size
    // (so the handshake's possible-initial-resize emission cannot collide
    // with the marker we're matching for).
    target := &pty.Winsize{Rows: 42, Cols: 117}
    if err := pty.Setsize(a.Master, target); err != nil {
        t.Fatalf("Setsize master: %v", err)
    }

    needle := []byte("winsize rows=42 cols=117\n")
    if err := readUntilContains(a.Master, needle, 5*time.Second); err != nil {
        t.Fatalf("did not observe new winsize: %v", err)
    }
}
```

That is the complete production code for this ticket.

## Why no new helpers

The existing `readUntilContains` is exactly the right shape: accumulate bytes, match a literal needle, deadline-bounded. The needle is a fixed string (`winsize rows=42 cols=117\n`) since the test owns both ends of the resize. No nonce or regex is required.

The harness exposes `Master`, `attachDone`, and `daemonOut/Err` already — nothing new to plumb.

## Why extend the existing helper rather than add a new mode

Per ticket body: *"Reuse that harness's stub-claude program rather than introducing a second one — extend it if SIGWINCH observation isn't already covered."*

A second mode (e.g. `GO_TEST_HELPER_MODE=echo-winsize`) would force `attach_pty.go::spawnAttachableDaemon` to grow a "which mode?" parameter, which inflates the change for no benefit:

- The new SIGWINCH emission is purely additive to the byte stream. The existing tests match their payloads via `bytes.Contains` (`#125` round-trip), via regex with a unique anchor (`#128` `PYRY_E2E_STARTED pid=N`), or via fixed sequences they wrote themselves (`#127` Ctrl-B d). None of them care about extra bytes between their own writes.
- The marker `winsize rows=N cols=M\n` cannot collide with any other helper output (echoed user input is opaque user bytes; `PYRY_E2E_STARTED` is its own anchor; `__EXIT__` exits before printing anything).

A single mode keeps the helper's surface area honest: one binary, one mode, behaviours composed.

## Concurrency model (helper)

Two goroutines now exist inside the helper's `case "echo":` branch:

1. **Main goroutine** (existing): `os.Stdin.Read` blocking on the supervisor's PTY slave; `os.Stdout.Write(line)` per `\n`-delimited line.
2. **NEW: SIGWINCH watcher goroutine**: `for range sigCh` → `pty.GetsizeFull(os.Stdin)` → `fmt.Fprintf(os.Stdout, …)`.

Both write `os.Stdout`. Go's `*os.File.Write` serialises concurrent calls at the FD level (`poll.FD.fdmu`), and `fmt.Fprintf` issues exactly one `Write` per call. Result: `winsize rows=N cols=M\n` lines are atomic relative to echo lines — no byte interleaving, no need for an explicit mutex.

The goroutine has no clean shutdown path (no `done` channel, no `signal.Stop`). Lifetime: until the helper process exits via `os.Exit(0)` on stdin EOF or `os.Exit(1)` on `__EXIT__`. Both terminate the goroutine implicitly. This is the same posture as the daemon's `watchWindowSize` would be if it weren't shared with iteration teardown — and the helper has no iterations.

`pty.GetsizeFull(os.Stdin)` reads the dimensions the kernel just stored; it does not race with the SIGWINCH delivery (the kernel sets size before raising the signal — same contract relied on by `internal/supervisor/winsize.go`).

## Race scenarios audited

| Race | Outcome |
|---|---|
| Test resizes master *before* helper's `signal.Notify` is fully registered | The `StartAttach` return path includes a 500ms grace where the attach client is allowed to fail handshake without flake (line 137-146 of `attach_pty.go`). By the time `StartAttach` returns, the helper has long since printed its `PYRY_E2E_STARTED` marker (which sequences after `signal.Notify`). The test's `pty.Setsize` happens after `StartAttach` returns, so the helper's watcher is wired. |
| Test resizes master *before* attach client's `startWinsizeWatcher` is fully registered | Same argument: by `StartAttach`'s return, the attach client has finished its JSON handshake (server replied OK, client dialed back) and `startWinsizeWatcher` runs immediately after the ack (lines 78-81 of `attach_client.go`). The `defer stopWinsize()` path is irrelevant for this analysis. The 500ms grace in `StartAttach` plus the Go scheduler's prompt goroutine launch makes this a non-issue in practice; if it ever flakes, add a brief `time.Sleep(50*time.Millisecond)` after `StartAttach` and *before* `pty.Setsize` rather than priming a probe. |
| Handshake's initial Bridge.Resize fires SIGWINCH on the helper, helper emits a winsize line with the slave's default dims | Possible. The slave's default size on macOS / Linux is implementation-defined (often 0×0 from `pty.Open`, in which case the handshake's zero-dim sentinel suppresses the resize entirely). If a non-zero default ever surfaces, the helper emits e.g. `winsize rows=24 cols=80\n` *before* the test's resize. **Harmless:** the test's needle (`winsize rows=42 cols=117\n`) does not collide with any sane terminal default; `readUntilContains` accumulates and matches the second emission. The test does not care which size came first. |
| Burst of two SIGWINCH on the helper (initial-bridge + test resize) | The buffered chan(1) coalesces. If both fire before the goroutine processes the first, the second collapses to one queued. On wake, `pty.GetsizeFull` returns the *current* (most recent) dimensions, which is the test's target. The single emission then matches the needle. The "missing initial" emission is fine — see above. |
| Burst of two SIGWINCH on the helper, one fires *during* `pty.GetsizeFull` | The goroutine reads the fresh dims for the first signal, emits, returns to select, reads the second signal, re-reads dims (same value), emits a duplicate `winsize rows=42 cols=117\n` line. `readUntilContains` matches the first. The duplicate is harmless. |
| Helper emits SIGWINCH line *while* echo loop is mid-line | Echo writes only on `\n`. SIGWINCH writes a complete `winsize …\n` line. Both are single `Write` calls, serialised by `poll.FD.fdmu`. No interleaving at the byte level. |
| Test exits before the SIGWINCH propagation completes | `readUntilContains`'s 5-second deadline bounds the wait. On timeout, `t.Fatalf` reports both bytes seen and the timeout — actionable. Cleanup closes Master, which unblocks the leftover reader goroutine. |
| Multiple `pty.Setsize` from concurrent tests | `t.Parallel()` is **not** used by this test (matching `attach_detach_test.go` and `attach_restart_test.go`). The harness owns its own daemon and PTY pair per test. No cross-test SIGWINCH leakage. |

No new mutexes, no new shared state.

## Error handling

| Scenario | What happens | What the test reports |
|---|---|---|
| `pty.Setsize(a.Master, …)` returns an error | `t.Fatalf("Setsize master: %v", err)` immediately. | The test fails before the propagation chain runs. |
| `readUntilContains` times out | `t.Fatalf("did not observe new winsize: %v", err)` with the bytes seen. | Captures the failure mode (e.g. attach watcher didn't fire, daemon didn't apply, helper didn't emit). |
| Helper's `pty.GetsizeFull` errors mid-flight | Goroutine `continue`s; no emission for that signal. | Test times out → see above. Daemon stderr in the failure message points at the missing emission. |
| `t.Fatalf` after `StartAttach` returned | `t.Cleanup` runs the harness teardown — Master close, slave close, SIGTERM attach, SIGTERM daemon, socket remove. No leaked processes. |
| Attach client dies mid-test (between `StartAttach` and `pty.Setsize`) | `Setsize` may still succeed (kernel-level), but no SendResize fires, daemon never resizes, helper never emits → readUntilContains times out. Failure message includes daemon stderr. Same surface as a regression in #133. |

The test does not poll `attachDone` mid-test (unlike `attach_restart_test.go`'s AC#4) — the proof of the AC is the helper's emission, not attach-client liveness. If the attach client died, the deadline-bounded read fails and surfaces it.

# Backwards compatibility

Three existing tests run the same helper:

| Test | What it matches | Effect of new SIGWINCH emissions |
|---|---|---|
| `TestE2E_Attach_RoundTripsBytes` (#125) | `bytes.Contains` for `pyry-attach-roundtrip-<nonce>\n`. | Extra `winsize …\n` lines accumulate in the master stream; the substring match still finds the nonce. ✓ |
| `TestE2E_Attach_DetachesCleanly` (#127) | Drains master via background goroutine; never asserts on its contents. Asserts on `attachDone` exit code and `pyry status` output. | Extra winsize lines are drained silently. ✓ |
| `TestE2E_Attach_SurvivesClaudeRestart` (#128) | Regex `PYRY_E2E_STARTED pid=(\d+)\n` for PID detection; `bytes.Contains` for nonce-anchored payload round-trips. | `winsize rows=N cols=M\n` matches neither pattern. The accumulator's substring search ignores it. ✓ |

No flake amplification: the SIGWINCH goroutine only emits in response to a real signal. The other tests do not call `pty.Setsize` on master, and the supervisor's own `watchWindowSize` is dormant in bridge mode (its `resizeOnce` returns early on `!IsTerminal(os.Stdin)` — the daemon's stdin is `/dev/null`).

The helper's only on-startup chatter is `PYRY_E2E_STARTED pid=N\n`, unchanged. If the handshake's initial Bridge.Resize ever does fire (slave default ≠ 0×0), an extra `winsize …\n` line appears in the stream — still harmless to all three existing tests.

# Testing strategy

The single new e2e test, `TestE2E_Attach_HandlesSIGWINCH`, is itself the testing strategy. It is the canonical, end-to-end pin for the four-stage SIGWINCH chain.

### What the test pins

1. `pty.Setsize` on the harness master delivers SIGWINCH to the attach client process group.
2. The attach client's `startWinsizeWatcher` invokes `SendResize` with the freshly-read slave dimensions.
3. The daemon's server `handleResize` → `Session.Resize` → `Bridge.Resize` → `pty.Setsize` on the supervisor PTY.
4. The supervised child observes SIGWINCH and reads the new dimensions.

### What the test deliberately does not pin

- **Multiple SIGWINCH burst handling.** Covered by the unit test `TestStartWinsizeWatcher_SIGWINCHEmitsResize` (#133) and the channel-coalescing analysis there. Burst behaviour through the full chain is not load-bearing for any AC.
- **Detach-cancels-watcher.** Covered by the unit test `TestStartWinsizeWatcher_StopIsSynchronousAndLeakFree` (#133). Re-running it through the binary boundary adds no information.
- **Server clamps oversize dims.** Covered by `TestServer_Resize_ClampsOversizeDims` (#137). The chosen test dimensions (42×117) are well inside `uint16` bounds.
- **Foreground-mode silent resize.** Covered by `TestServer_Resize_ForegroundSessionSilent` (#137). The harness only runs bridge mode.

### Skip-on-no-PTY

`StartAttach` calls `pty.Open()` immediately and `t.Skip`s if it fails (line 71-74 of `attach_pty.go`). This satisfies AC#4 verbatim — the new test inherits the skip behaviour from the harness. No additional probe needed.

### Manual verification at implementation time

```
go test -tags e2e -race -count=10 -run TestE2E_Attach_HandlesSIGWINCH ./internal/e2e/...
go test -tags e2e -race -count=3  -run TestE2E_Attach                  ./internal/e2e/...
```

The second invocation re-runs all four `TestE2E_Attach_*` tests with the new helper extension to confirm backwards compatibility per the table above.

# Open questions

1. **Should the helper emit an initial `winsize …\n` at startup (alongside `PYRY_E2E_STARTED`) so tests can pin "child observed the handshake's initial geometry"?** No, not in this ticket. The handshake's geometry application is already pinned by `TestServer_AttachAppliesHandshakeGeometry` at the unit boundary. Re-running it through the binary would just duplicate that scaffold — and AC#3 of this ticket explicitly scopes the marker to "on SIGWINCH", not "on startup".

2. **Should the test choose dimensions that *match* the slave's default to verify "initial resize is suppressed when dims unchanged"?** No. That property lives at the protocol layer (`AttachPayload` zero-dim sentinel; `ResizePayload` zero-dim no-op) and is unit-pinned by `TestServer_Resize_ZeroDimNoOp`. A delta-based emission contract is out of scope here.

3. **Should `pty.Setsize(master, …)` be retried on `EINTR`?** Standard library `pty.Setsize` already wraps the ioctl; no retry needed in the test. If a future Go runtime change makes ioctls EINTR-prone, the fix is in the `pty` library.

# What this spec does not solve

- **Daemon-side foreground-mode SIGWINCH.** The harness only runs bridge mode (`cmd.Stdin` defaults to `/dev/null`). Foreground-mode SIGWINCH propagation is exercised by `internal/supervisor/winsize.go`'s in-tree unit test plus manual testing; it has no e2e cover and is out of scope here.
- **Real `claude` binary in the loop.** Same posture as #125 / #127 / #128 — the test binary is the supervised child; the production claude is not exercised by Pyrycode's test suite at any level.
- **Multi-attach SIGWINCH semantics.** The bridge rejects a second attacher (`ErrBridgeBusy`); concurrent attach clients are not a real configuration. No coverage gap.

# Out of scope

- Any code outside `internal/e2e/attach_pty_test.go`.
- Helper-binary refactor into a separate `package main`.
- Changes to `AttachHarness` API, `StartAttach`, `readUntilContains`, or `tinyNonce`.
- Documentation updates beyond the godoc on `TestHelperProcess` (the existing godoc paragraph at lines 18-48 picks up the new SIGWINCH behaviour in one new paragraph; no separate ADR or knowledge doc warranted for an e2e test extension).
