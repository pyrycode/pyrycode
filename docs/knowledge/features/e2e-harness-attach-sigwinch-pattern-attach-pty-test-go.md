# Attach SIGWINCH Pattern (`attach_pty_test.go`, #126)

`TestE2E_Attach_HandlesSIGWINCH` proves the full live-resize chain
end-to-end at the binary boundary: a real `pty.Setsize` on the
harness's client-side master fd → kernel SIGWINCH on the attach
client process group → `startWinsizeWatcher` (#133) → `SendResize`
(#137 wire) → server `handleResize` → `Session.Resize` → `Bridge.Resize`
(#136 seam) → `pty.Setsize` on the supervisor's ptmx → kernel SIGWINCH
on the supervised child → child observes the new `(rows, cols)` and
emits a deterministic stdout marker.

If any of the four halves regresses (client watcher, wire, server
applier, supervisor seam), the test fails.

### No new harness API

Reuses #125's `AttachHarness` + `StartAttach(t, "")` as-is. The
harness exposes `Master *os.File` and the existing `readUntilContains`
helper does the matching. No new files, no new constructors — one
test function plus one helper extension.

### `TestHelperProcess` echo mode gains a SIGWINCH watcher

The supervised child (a `TestHelperProcess` re-exec in `echo` mode,
inherited from #125) installs a `signal.Notify(SIGWINCH)` watcher
immediately after the `MakeRaw` + `PYRY_E2E_STARTED` bootstrap. On
each signal it emits a single `winsize rows=N cols=M\n` line read
via `pty.GetsizeFull(os.Stdin)` — the supervisor's PTY slave is the
helper's stdin, so the kernel just stored the freshly applied
dimensions there.

Pattern mirrors `internal/supervisor/winsize.go`: `signal.Notify`
+ buffered `chan(1)` + goroutine + `pty.GetsizeFull(os.Stdin)`
guarded by `term.IsTerminal`. No teardown plumbing — the helper
exits on stdin EOF / `__EXIT__`, which terminates the goroutine
implicitly.

### Why extend the existing helper instead of adding a second mode

Per ticket: "reuse the harness's stub-claude program rather than
introducing a second one." The new `winsize …\n` line is purely
additive. Existing tests match their own payloads via:

| Sibling test | Match shape | Effect of new emissions |
|---|---|---|
| `TestE2E_Attach_RoundTripsBytes` (#125) | `bytes.Contains` for nonced payload | extra lines accumulate; substring search still hits ✓ |
| `TestE2E_Attach_DetachesCleanly` (#127) | drains master, asserts on `attachDone` + `pyry status` | extras are drained silently ✓ |
| `TestE2E_Attach_SurvivesClaudeRestart` (#128) | regex `PYRY_E2E_STARTED pid=(\d+)\n` + nonce-anchored bytes | `winsize …\n` matches neither ✓ |

A second mode would force `spawnAttachableDaemon` to grow a "which
mode?" parameter for no reuse benefit.

### Dimensions chosen to avoid handshake collision

`Rows: 42, Cols: 117` are deliberately distant from any sane terminal
default. If the handshake's initial `Bridge.Resize` ever fires (slave
default ≠ 0×0), the helper emits a winsize line for the default size
*before* the test's resize — `readUntilContains` accumulates and
matches the second emission. The test does not care which size came
first.

### Bursts coalesce into a single emission

Two SIGWINCH delivered before the watcher goroutine processes the
first collapse to one queued event in the buffered `chan(1)`. On
wake, `pty.GetsizeFull` returns the *current* (most recent)
dimensions. Two writes to `os.Stdout` from the watcher and the echo
loop are serialised at the FD level by `poll.FD.fdmu` — single
`Write` per `fmt.Fprintf` ⇒ no byte interleaving with echo lines.

### Acceptance-criteria mapping

| AC | Asserted by |
|---|---|
| AC#1 — `TestE2E_Attach_HandlesSIGWINCH` resizes harness master | `pty.Setsize(a.Master, &pty.Winsize{42, 117})` |
| AC#2 — child observes new dims within 2s deadline | `readUntilContains(a.Master, []byte("winsize rows=42 cols=117\n"), 5*time.Second)` (5s is the harness convention; well over the 2s minimum) |
| AC#3 — deterministic machine-readable marker | helper's `fmt.Fprintf(os.Stdout, "winsize rows=%d cols=%d\n", …)` |
| AC#4 — skip on hosts without usable PTY | inherited from `StartAttach`'s `pty.Open` skip |

### What this test does not verify

- **Multiple SIGWINCH burst handling.** Covered by the unit
  `TestStartWinsizeWatcher_SIGWINCHEmitsResize` (#133).
- **Detach-cancels-watcher.** Covered by
  `TestStartWinsizeWatcher_StopIsSynchronousAndLeakFree` (#133).
- **Server clamps oversize dims.** Covered by
  `TestServer_Resize_ClampsOversizeDims` (#137).
- **Foreground-mode silent resize.** Covered by
  `TestServer_Resize_ForegroundSessionSilent` (#137). The harness
  only runs bridge mode.

### Production diff is zero

The full chain shipped across #136 (`Bridge.Resize` seam), #137
(`VerbResize` wire + `handleResize` applier), and #133
(`startWinsizeWatcher` client emitter). #126 is the e2e cover that
proves the four halves are stitched together correctly through the
real `pyry` binary. Test diff: ~64 LOC, single file
(`attach_pty_test.go`).
