# Attach Detach Pattern (`attach_detach_test.go`, #127)

`TestE2E_Attach_DetachesCleanly` drives the documented `Ctrl-B d`
sequence (bytes `0x02 0x64`) into a live attach session and asserts
the triple invariant of clean detach: attach client exits 0, daemon
survives, supervised child still in `Phase: running`. Builds on the
PTY harness from #125 with two new methods on the existing
`*AttachHarness` and one shared-body refactor in `harness.go`.

### Methods added to `*AttachHarness`

- `WaitDetach(t, timeout) int` — blocks on `attachDone` (the channel
  the wait goroutine closes after `attachCmd.Wait()` returns), then
  reads `attachCmd.ProcessState.ExitCode()`. Channel close is the
  happens-before edge that makes `ProcessState` safe to read on the
  test goroutine. `t.Fatalf` on timeout (clean detach is near-instant
  in practice; the 5s budget is a safety net).
- `Run(t, verb, args...) RunResult` — mirror of `Harness.Run` against
  the attach harness's `SocketPath` + `HomeDir`. Used to drive
  `pyry status` against the same daemon the attach client is bound to.

### `runVerb` — shared body extracted from `Harness.Run`

Both methods needed the same body: `exec.CommandContext(ctx, binPath,
verb, "-pyry-socket="+socket, args...)`, `cmd.Env = childEnv(home)`,
stdout/stderr capture, `runTimeout` deadline, exit-code mapping. The
body moved into a package-private `runVerb(t, socket, home, verb,
args...) RunResult` free function; `Harness.Run` and
`AttachHarness.Run` are 2-line wrappers (`return runVerb(t,
h.SocketPath, h.HomeDir, verb, args...)`). Net effect: ~25 lines move
out of `Harness.Run`; behaviour for existing callers is unchanged.

The refactor is bounded — `Harness.Run` had no callers outside the
harness package, so the rename is private and `gofmt`-clean. A 25-line
duplication would have been acceptable for an XS ticket; extraction
won because the two methods diverge only in two field reads.

### Master-drain goroutine — load-bearing for the test

`pyry attach` writes `pyry: detached.` to its own stderr after
`copyWithEscape` returns on `Ctrl-B d`. With `cmd.Stderr = slave`
that write goes through the kernel PTY into the master buffer; if no
goroutine is reading the master, the buffer fills and the slave write
blocks — the attach client never returns from `runAttach` and
`cmd.Wait()` never fires. Symptom: `WaitDetach` hits its 5s deadline
even though `Ctrl-B d` was correctly recognised.

The fix lives in the test, not the harness: spawn a background
master-drain goroutine before writing the detach sequence and let it
ride until teardown closes `Master` and `Read` errors out. The #125
round-trip test got away without one because `readUntilContains`
consumed the master continuously through the assertion phase. See
`lessons.md § PTY master backpressure stalls slave-side process
exit`.

### Why a generous timeout, not a tight one

`WaitDetach`'s timeout is a safety net, not a steady-state
expectation. Steady-state detach is single-digit milliseconds (no I/O
between `Ctrl-B d` recognition and process exit). The 5s budget gives
1–2 orders of magnitude of headroom and lets a flaky CI scheduler
skate. A tight deadline would convert scheduler jitter into
intermittent failures without catching real regressions any earlier.

### Acceptance-criteria mapping

| AC | Asserted by |
|---|---|
| Daemon spawn + attach via PTY harness; detach bytes written to PTY | `StartAttach(t, "")` + `a.Master.Write([]byte{0x02, 0x64})` |
| Attach client exits 0 within ≥5s | `a.WaitDetach(t, 5*time.Second)` + exit-code check |
| Daemon alive after detach | `a.Run(t, "status")` exit 0 check |
| Supervised child in `Phase: running` | `bytes.Contains(r.Stdout, []byte("Phase:         running"))` (multi-space gap is significant — column-aligned status output, mirrors `idle_test.go`) |
| Skip cleanly on hosts without usable PTY | inherited from `StartAttach`'s `pty.Open` skip |

### What this test does not verify

- Detach against a non-bootstrap session (`StartAttach` accepts
  `sessionID` but the test passes `""`).
- Behaviour when the user holds `Ctrl-B` but never types `d` — that's
  a `control.Attach` unit-test concern, not e2e.
- Bridge-busy semantics on a second concurrent attach.

### Production diff is zero

`pyry attach`, `control.Attach`'s prefix-key state machine, and the
detach handshake all shipped pre-#127. Test diff: ~55 LOC for the new
test, ~32 LOC for the two `*AttachHarness` methods, ~12 LOC for the
`runVerb` extraction.
