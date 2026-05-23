# 518 — supervisor: fix `TestBridge_OutputObserver_NilSkipped` flake (and one sibling)

**Size:** XS (override PO's S; test-only, ~15 LOC diff, single file).

**Scope:** `internal/supervisor/bridge_test.go` only. No production change.

## Files to read first

- `internal/supervisor/bridge_test.go:97-125` — `TestBridge_OutputObserver_NilSkipped` as it stands today (the flaky test).
- `internal/supervisor/bridge_test.go:143-168` — `TestBridge_OutputForwardsWhenAttached`: identical shape (sibling that the audit AC asks us to fix too).
- `internal/supervisor/bridge.go:168-184` — `Bridge.Write` body. **Synchronous.** Acquires `b.mu`, snapshots `out := b.output`, releases, then writes to `out`. There is no async "output copy goroutine" to wait for.
- `internal/supervisor/bridge.go:209-252` — `Bridge.Attach`. Sets `b.output = out` synchronously, then spawns an input-pump goroutine that on input EOF acquires `b.mu` and clears `b.output = nil`. This is the goroutine the test races against.
- `internal/supervisor/bridge_test.go:30-57` — `TestBridge_WriteSwallowsAttachedWriteErrors` already uses the `io.Pipe()` pattern that this spec adopts; the new code mirrors it for consistency.

## Context

PR #517's QA flagged one failure of `TestBridge_OutputObserver_NilSkipped` (`bridge_test.go:117`, `got "", want "hi"`) that did not reproduce on retry. PR #517 touched none of `internal/supervisor/*`, so this is a pre-existing flake exposed by QA's single-sample comparison, not a regression. Filed against `main` to keep the QA gate honest.

## Root cause

The test calls `b.Attach(strings.NewReader(""), &out)` then synchronously calls `b.Write([]byte("hi"))`. Two goroutines race on `b.mu`:

| Goroutine | Step |
|---|---|
| Test | `b.Write` → acquires `b.mu`, snapshots `out := b.output`, releases, writes to `out`. |
| Attach's input pump | `in.Read(buf)` on `strings.NewReader("")` returns `(0, io.EOF)` immediately → goroutine exits its loop → acquires `b.mu`, sets `b.output = nil`, releases. |

If the input-pump cleanup wins, `b.Write` sees `b.output == nil` and discards the bytes per the documented "bytes lost, daemon stays alive" contract (`bridge.go:159-167`). `out.String() == ""` and the assertion fails.

This is purely a test-side race. **Production semantics are correct:** EOF on the attached input is supposed to detach the client and drop subsequent output. The bug is that the test gives `Attach` a reader that is already at EOF, then expects output forwarding to still work afterwards.

The PO body's "wait for the copy goroutine to flush" framing is slightly off — `Bridge.Write` has no async fan-out — but the fix shape it implies (a deterministic signal in place of relying on scheduling order) is the right shape. The deterministic signal here is **input-side EOF control**: keep the input pump alive across the assertion, then detach explicitly.

## Design

Replace `strings.NewReader("")` with `io.Pipe()` on the input side. The input pump blocks in `pr.Read(buf)` until the test calls `pw.Close()`, so `b.output` stays bound to the test's buffer for the entire assertion window. After assertions, close the pipe writer and wait on the `done` channel — that's the deterministic detach signal.

This is exactly the pattern `TestBridge_WriteSwallowsAttachedWriteErrors` already uses (`bridge_test.go:38-44`); we are normalising the two flaky siblings to it.

### Edits

Two functions in `internal/supervisor/bridge_test.go`. No new helpers — inlining the pipe pair keeps each test readable as a standalone case, and the pattern is already used in this file.

**`TestBridge_OutputObserver_NilSkipped`** (current `bridge_test.go:97-125`):

- Replace `strings.NewReader("")` with an `io.Pipe()` pair. Keep `&out bytes.Buffer` as before.
- After the existing assertions (the `if got := out.String(); ...` check at line 116 and the `SetOutputObserver(nil)` no-op check at line 121-124), close the pipe writer to trigger EOF on the input pump, then receive on `done` to confirm detach. Drop the existing `defer func() { <-done }()` — explicit teardown after `pw.Close()` is clearer and removes the `defer` indirection.
- The assertion at line 122 (`b.Write([]byte("!"))` after `SetOutputObserver(nil)`) still runs while attached, which is fine — the test's stated intent is "Write after nil-set still succeeds without error." That contract holds whether `b.output` is the buffer or nil.

**`TestBridge_OutputForwardsWhenAttached`** (current `bridge_test.go:143-168`):

- Same swap: `strings.NewReader("")` → `io.Pipe()`.
- The existing `select { case <-done: ... }` block at line 163-167 currently relies on the empty-reader EOF to fire `done`. After the swap, that empty-reader EOF no longer exists; replace the comment and trigger EOF explicitly by closing the pipe writer just before the `select`. Keep the `time.After(time.Second)` timeout — it's now guarding "did closing pw propagate through the input pump and close `done`," which is the deterministic signal we actually want to assert.

### Test plan

1. `go test -race -count=100 -run TestBridge_OutputObserver_NilSkipped ./internal/supervisor/` → 100/100 pass.
2. Same with `GOMAXPROCS=2` to exercise tighter scheduling.
3. `go test -race -count=100 -run TestBridge_OutputForwardsWhenAttached ./internal/supervisor/` → 100/100 pass (audit AC).
4. `go test -race ./internal/supervisor/` → green, no new race-detector findings.
5. `make check` → green.

The `-count=100` rule from AC#1 is the deterministic floor; if the fix is right, count is irrelevant. If `-count=100` still flakes, the diagnosis above is wrong — stop and re-diagnose rather than papering over with retries.

### Audit (AC#4)

Walked the rest of `bridge_test.go` for the same `Attach(immediate-EOF-reader, &out)` + synchronous `out.String()` assertion pattern:

| Test | Verdict |
|---|---|
| `TestBridge_WriteSwallowsAttachedWriteErrors` (line 30) | Already uses `io.Pipe()`. ✓ |
| `TestBridge_OutputObserver_InvokedOnWrite` (line 59) | No `Attach`; observer is synchronous from `Write`. ✓ |
| `TestBridge_OutputObserver_NilSkipped` (line 97) | **Has the bug — fix.** |
| `TestBridge_DiscardsWhenUnattached` (line 127) | No `Attach`. ✓ |
| `TestBridge_OutputForwardsWhenAttached` (line 143) | **Has the bug — fix.** |
| `TestBridge_InputFlowsToReader` (line 170) | Uses `strings.NewReader("greetings")`; the chunk is sent to `b.in` before EOF, and the test reads it. Race exists between EOF-cleanup and the test's read on `b.in`, but `b.Read` does not depend on `b.output`, so the race is benign. ✓ |
| `TestBridge_RejectsConcurrentAttach` (line 195) | First Attach uses `io.Pipe()`; the `strings.NewReader("")` calls are either rejected (`ErrBridgeBusy`) or only used to confirm the post-detach Attach succeeds — no Write follows. ✓ |
| `TestBridge_BlocksReadUntilAttached` (line 288) | Input race only; output is `&bytes.Buffer{}` but never read. ✓ |
| Resize tests (lines 228, 258, 269) | No `Attach`. ✓ |

Exactly two tests need the fix.

## What this spec does NOT change

- **No production change in `bridge.go`.** The Attach goroutine's "EOF on input ⇒ clear `b.output`" behaviour is correct; pyry depends on it for the daemon's detach path (`bridge.go:241-249`). Re-architecting that to expose a sync hook the test could wait on would change production semantics for a problem that is only present when the test feeds an immediately-closed reader.
- **No new helper or test-utility file.** Two inlined `io.Pipe()` calls; the file already uses the same pattern in one other test.
- **No `time.Sleep` anywhere.** AC#2 is explicit about this; the `time.After(time.Second)` timeout in `TestBridge_OutputForwardsWhenAttached` is a deadlock guard, not a wait-for-flush — it is allowed (it already exists in the file, e.g. line 165, line 306, line 315).

## Open questions

None. The fix is mechanical and the audit is bounded.
