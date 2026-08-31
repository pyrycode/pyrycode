# Attach Restart Pattern (`attach_restart_test.go`, #128)

`TestE2E_Attach_SurvivesClaudeRestart` is the load-bearing proof of
the supervisor's restart loop: an attached `pyry attach` client
remains usable across a supervised claude restart. The supervisor
re-binds the bridge to the new PTY, the attach client stays alive,
and bytes flow again. Builds on the PTY harness from #125 with two
small extensions to `TestHelperProcess`'s `echo` mode and one new
test file.

> **This ticket was billed as test-only — but the test surfaced a
> real production bug.** The supervisor's bridge input pump leaked
> across `runOnce` iterations and silently corrupted bytes typed
> during a restart. The post-restart payload arrived with a 5-byte
> prefix consumed by the leaked iteration-1 goroutine. Fix shipped
> in this ticket: replaced `io.Pipe` in `*supervisor.Bridge` with a
> `chan []byte` + per-iteration cancel signal
> (`BeginIteration` / `EndIteration`). See
> [ADR 007](../decisions/007-bridge-iteration-boundaries.md) and
> `lessons.md § Bridge input pump must be scoped per-iteration`.

### Helper extensions — startup marker, `__EXIT__`, `__PID__`

`TestHelperProcess`'s `echo` mode (#125) gains three behaviours:

1. **On startup, emit `PYRY_E2E_STARTED pid=<pid>\n` once** before
   reading any input. The supervisor re-execs the helper on each
   restart, so each iteration produces a fresh PID. Observing two
   distinct PIDs on the attach PTY is the test's proof of respawn.

2. **`__EXIT__\n` exits non-zero before echoing.** The supervisor
   sees `*exec.ExitError`, transitions to `PhaseBackoff` (500ms),
   and respawns. The trigger arrives as a complete line; line-buffered
   echo otherwise preserves byte-exact contract for #125's
   round-trip test.

3. **`__PID__\n` re-emits the startup marker without echoing the
   probe.** Necessary because the *first* startup marker races with
   the attach client connecting — bytes the helper writes before the
   bridge has an attached output sink are silently discarded by the
   bridge's `Write` discard-on-detach contract. The probe gives the
   test a deterministic way to capture pid1 *after* the attach is
   wired. Subsequent markers (post-restart pid2) don't need the
   probe — the attach is already bound by then.

The body is line-buffered; per-byte scan via `os.Stdin.Read` →
match `\n` → branch on `string(line)`. ~30 LOC replacing the
original two-line `io.Copy(os.Stdout, os.Stdin)`.

### `-pyry-resume=false` on the attach harness

The supervisor defaults to `ResumeLast=true`, prepending `--continue`
to the child's args on every respawn after the first. The e2e helper
is a Go test binary — `-test.run=TestHelperProcess --continue`
fails flag parsing. `spawnAttachableDaemon` now passes
`-pyry-resume=false` so the helper's argv is stable across restarts.
This also fixes a subtler bug for #125's round-trip test that never
manifested because that test never restarted the child.

### `os.MkdirTemp` HOME for sun_path budget

The test name `TestE2E_Attach_SurvivesClaudeRestart` plus
`<tempdir>/pyry.sock` overflows macOS's 104-byte `sun_path` limit
when `t.TempDir()` is used. `StartAttach` now allocates HOME via
`os.MkdirTemp("", "pyry-at-*")` + `t.Cleanup(os.RemoveAll)` —
same fix as #106's restart tests and #123's rotation primitive.
Existing `TestE2E_Attach_RoundTripsBytes` and #127's detach test
benefit transparently.

### Sequence

```
1. StartAttach(t, "")                       — daemon up, attach bound
2. Master.Write("__PID__\n")                — probe (post-attach-wired)
3. readStartupMarker(...) → pid1            — proves child A is up
4. Master.Write("pre-restart-<nonce>\n")
5. readUntilContains(payload1)              — pre-restart round-trip
6. Master.Write("__EXIT__\n")               — child A exits 1
7. readStartupMarker(...) → pid2 != pid1    — proves respawn (AC#3)
8. Master.Write("post-restart-<nonce>\n")
9. readUntilContains(payload2, 5s)          — bridge re-bind survives
10. select { case <-attachDone: fatal       — attach client still alive
            default: }
```

### `readStartupMarker` helper

Same shape as `readUntilContains` from #125 — caller-side timeout
via `time.After`, goroutine-per-read pattern, regex match against
the accumulated buffer. PTY master fds on darwin reject
`SetReadDeadline`, so the timeout is enforced in user space. Reader
goroutine left running on timeout is drained by the harness's
teardown closing `Master`.

### Acceptance-criteria mapping

| AC | Asserted by |
|---|---|
| AC#1 — pre-restart round-trip + exit trigger | steps 4–6 |
| AC#2 — post-restart round-trip ≤ 5s | step 9 (`readUntilContains` with `5*time.Second`) |
| AC#3 — respawn observed via distinct PIDs | step 7 (`pid2 != pid1`) |
| AC#4 — attach client still alive after respawn | step 10 (non-blocking select on `a.attachDone`) |
| AC#5 — skip on hosts without usable PTY | inherited from `StartAttach`'s `pty.Open` skip |

### What this test does not verify

- Backoff escalation across N consecutive crashes — this test forces
  exactly one restart.
- `BackoffReset` (60s stability window) — the test runs in <2s.
- A second concurrent attach during the respawn window — separate
  `ErrBridgeBusy` story.
- `__EXIT__` arriving as part of a longer chunk
  (e.g. `payload\n__EXIT__\n` in one `Write`). Not exercised by the
  AC; the helper's per-byte scan handles it correctly anyway.

### Production diff is **not** zero

| File | LOC | Change |
|---|---|---|
| `internal/supervisor/bridge.go` | +124 | replace `io.Pipe` with `chan []byte` + iteration cancel; add `BeginIteration` / `EndIteration` |
| `internal/supervisor/supervisor.go:runOnce` | +18 | bracket goroutines with `BeginIteration` / `EndIteration`; wait for both pumps to drain |
| `internal/e2e/attach_pty.go` | +12 | `os.MkdirTemp` HOME; `-pyry-resume=false` on the daemon spawn |
| `internal/e2e/attach_pty_test.go` | +58 | helper extensions (startup marker, `__EXIT__`, `__PID__`) |
| `internal/e2e/attach_restart_test.go` | +123 | new test + `readStartupMarker` helper |
| `docs/lessons.md` | +5 | bridge-input-pump-per-iteration entry |

`TestE2E_Attach_RoundTripsBytes` and `TestE2E_Attach_DetachesCleanly`
keep passing — line-buffered echo with the new control-line gate is
byte-equivalent to `io.Copy` for the payloads they send.
