# ADR 007: Per-iteration cancel on the supervisor bridge's input path

**Status:** Accepted (2026-05-03, ticket [#128](https://github.com/pyrycode/pyrycode/issues/128))
**Phase:** test-infra (regression discovered while implementing the e2e attach-survives-restart proof)
**Affects:** `internal/supervisor/bridge.go`, `internal/supervisor/supervisor.go:runOnce`

## Context

`*supervisor.Bridge` is the seam that lets a `pyry attach` client take over the PTY interactively. A single `Bridge` instance persists across child restarts — `runOnce` re-runs each iteration with a fresh PTY pair, but the bridge (and any attached client) lives across that boundary. Phase 0's implementation backed the input path with `io.Pipe`:

```go
type Bridge struct {
    pipeR *io.PipeReader
    pipeW *io.PipeWriter
    // ...
}
func (b *Bridge) Read(p []byte) (int, error) { return b.pipeR.Read(p) }
```

`runOnce` launches two `io.Copy` goroutines per iteration:

```go
go io.Copy(ptmx, s.cfg.Bridge)   // input pump:  bridge → ptmx
go io.Copy(s.cfg.Bridge, ptmx)   // output pump: ptmx → bridge
```

When the child exits, `ptmx.Close()` unblocks the **output pump** (its `ptmx.Read` errors). The **input pump** has no symmetric trigger — `bridge.Read` blocks on the pipe until something arrives. The original code accepted this as a leak and waited "one of two with a 250ms timeout" before falling through to backoff:

```go
select {
case <-done:                      // first goroutine returned
case <-time.After(goroutineDrainTimeout):
}
return waitErr
```

Single-iteration runs (Phase 0 foreground) made this invisible. Once the supervisor's restart loop ran a second iteration with bytes in flight from an attached client, the consequences surfaced.

## The bug surfaced by #128

`TestE2E_Attach_SurvivesClaudeRestart` writes `__EXIT__\n` to crash child A, waits for child B's startup marker, then writes `post-restart-XXXXXXXX\n` and asserts the echo round-trips. Against the original bridge, the post-restart payload arrived as `restart-XXXXXXXX\n` — a **5-byte prefix consumed by the leaked input goroutine from iteration 1**.

The mechanism:

1. Iteration 1's input pump is blocked in `pipeR.Read`.
2. Child A exits; `cmd.Wait` returns.
3. `ptmx.Close()` unblocks the output pump; the select fires; `runOnce` returns. The input pump is still parked in `pipeR.Read`.
4. Backoff fires; iteration 2 begins. A new ptmx is opened. A **second** input goroutine is launched: `io.Copy(newPtmx, bridge)`.
5. The test writes `post-restart-XXXXXXXX\n`. The attach client forwards bytes to `pipeW`. Both goroutines are racing on `pipeR.Read`.
6. The leaked iteration-1 goroutine wins the first ~5 bytes (`post-`), copies them to the **closed** ptmx of iteration 1, sees write fail, returns. Bytes lost.
7. Iteration 2's goroutine reads the rest (`restart-XXXXXXXX\n`) and forwards it to the live ptmx. Helper echoes back the truncated string. Test assertion fails with a deterministic 5-byte gap.

The bug had been latent since the bridge was introduced: any restart while an attach client was actively writing could silently corrupt the input stream. No production telemetry exists for "characters typed during a restart" so the regression was never observed in the wild.

## Decision

Replace `io.Pipe` in `Bridge` with two structures that scope the input pump to a single `runOnce` iteration:

1. **A buffered `chan []byte`** (`b.in`) carrying input chunks from the attach goroutine to `Bridge.Read`. Buffer capacity = 64 chunks of 4 KiB; sized so a brief stall (e.g. mid-restart) doesn't push back on the attach client.
2. **A per-iteration cancel channel** (`b.iterCancel`) that `Bridge.Read` selects on alongside `b.in`. `EndIteration()` closes the cancel channel; `BeginIteration()` allocates a fresh one.

```go
func (b *Bridge) Read(p []byte) (int, error) {
    // ... drain leftover from a partial copy first ...
    select {
    case chunk := <-b.in:
        // ... copy + buffer overflow into b.leftover ...
        return n, nil
    case <-cancel:
        return 0, io.EOF
    }
}
```

`runOnce` brackets the per-iteration goroutines with `BeginIteration` / `EndIteration` and waits for **both** copy goroutines to drain (not "one-of-two with a timeout"):

```go
s.cfg.Bridge.BeginIteration()
done := make(chan error, 2)
go func() { _, err := io.Copy(ptmx, s.cfg.Bridge); done <- err }()
go func() { _, err := io.Copy(s.cfg.Bridge, ptmx); done <- err }()

waitErr := cmd.Wait()
_ = ptmx.Close()
s.cfg.Bridge.EndIteration()
for i := 0; i < 2; i++ {
    select {
    case <-done:
    case <-time.After(goroutineDrainTimeout):
    }
}
return waitErr
```

The attach goroutine in `Bridge.Attach` is unchanged in lifetime (still bound to the client connection's `in` reader EOF) — only its plumbing changes from `io.Copy(b.pipeW, in)` to a hand-rolled read-and-forward loop that pushes chunks onto `b.in`.

## Rationale

### Why the `chan []byte` over keeping `io.Pipe`

The bug is "the input pump can't be told to stop." `io.Pipe` only unblocks reads on `pipeW.Close()` — but the writer is the attach client's input, which is still alive (the client is meant to survive the restart). A separate signal channel is mandatory.

Once a signal channel is required, layering it over `io.Pipe` with a `select` is awkward — `pipeR.Read` doesn't compose with `select` without an extra goroutine to bridge it back to a channel, multiplying the complexity. A direct `chan []byte` collapses that: the attach goroutine writes chunks to the channel, `Bridge.Read` selects on the channel and the cancel signal, and the cancel becomes a first-class structural element rather than a workaround.

### Buffered channel + leftover slice over a synchronous handoff

`b.in` is buffered (capacity 64) so the attach client never pushes back during the brief input-pump stall between `EndIteration` and the next `BeginIteration`. A synchronous channel would block the attach goroutine on each send during that window; the test's `__EXIT__\n` line plus a fast-following payload could deadlock the next iteration before it starts.

The trade is a small per-bridge memory footprint (≤ 64 × 4 KiB = 256 KiB) and one extra buffer (`b.leftover`) for partial copies — when the caller's `p` is smaller than a queued chunk. Both costs are minor compared to the concurrency simplification.

### Bytes-in-flight safety: select non-determinism is the right semantic

The subtle case is **cancel and chunk arrive concurrently**. Go's `select` picks one ready case at random. Two outcomes:

- We pick `<-b.in`: chunk is delivered, returned to the caller. `EndIteration` will be observed on the next call.
- We pick `<-cancel`: `Read` returns EOF; the chunk **stays in the channel**. The next iteration's `BeginIteration → Read` consumes it.

Either way, no bytes are lost. The doc comment on `Bridge.Read` calls this out explicitly so a future reader doesn't "fix" it into a determinism check.

The only loss path would be if `BeginIteration` re-allocated `b.in` (clearing buffered chunks). It doesn't — only `iterCancel` is reset. `b.in` is allocated once in `NewBridge` and persists for the bridge's lifetime, exactly because it carries across iteration boundaries.

### Wait for **both** goroutines, not one-of-two

The original `select { case <-done: case <-time.After(...) }` returned after the first goroutine drained, leaving the second to leak. With the input pump now able to terminate cleanly via `EndIteration`, the for-loop drain becomes both correct (no leak) and bounded (per-iteration `goroutineDrainTimeout` per goroutine, same constant as before). The total worst-case wait is 2× `goroutineDrainTimeout`; in practice both goroutines drain in microseconds.

### `Write` discard-on-detach unchanged

The output path's "Write never returns an error" invariant (load-bearing for keeping the supervisor's PTY-drain goroutine alive when an attach client is mid-disconnect) is unchanged. This ADR is exclusively about the input path.

## Consequences

**Positive:**
- Attach round-trips survive arbitrarily many child restarts. The restart-loop is no longer a silent corruption surface for typed input.
- The supervisor's iteration boundary becomes explicit in the bridge's API. A future reader can see "when does the input pump go away?" by reading the bridge struct alone.
- Drain semantics are now symmetric: both goroutines exit on iteration end; `runOnce` waits for both.

**Neutral:**
- ~75 lines of Bridge code, replacing the ~3-line `io.Pipe` shim. The complexity moves from "implicit and broken under restart" to "explicit and correct."
- Two new public methods on `*Bridge` (`BeginIteration`, `EndIteration`). They're called in exactly one place (`runOnce`). Could be unexported once the test surface stabilises; left exported for now to keep `runOnce` readable without reaching across package boundaries.

**Negative:**
- The chan-based bridge has one more failure mode than the pipe: a wedged input pump (`b.in` full because `Bridge.Read` isn't draining) would block the attach goroutine on send. This is unreachable in practice — `Bridge.Read` is consumed by `io.Copy(ptmx, bridge)`, which is alive for the entire iteration — but a `select` with `default` to drop bytes could be added if a wedged ptmx ever became plausible.

## Alternatives considered

**Close the pipe on iteration end and re-create it on next.** Rejected: closes the *writer side* of the pipe, which means the attach goroutine's `io.Copy(b.pipeW, in)` returns an error. The attach goroutine clears `b.attached` on return; the next iteration starts with no attached client, even though the user's `pyry attach` socket is still open. Detaches the client across every restart — the exact failure this ticket was added to prevent.

**Cancel via a context propagated from `runOnce` into `Bridge.Read`.** Rejected: `io.Reader` doesn't take a context, and threading one through `io.Copy` requires a custom reader wrapper. Same shape as the chan-based design, with more boilerplate. The cancel channel is the simpler primitive.

**Leave the leak; document "don't restart while attached."** Rejected: the supervisor's whole reason to exist is to restart while attached. The product invariant the bug violates is the load-bearing one.

## Test coverage

`TestE2E_Attach_SurvivesClaudeRestart` (#128) exercises this end-to-end: writes a payload across a forced child restart, asserts the post-restart byte stream is byte-exact. Against the original bridge, the test fails reproducibly with a 5-byte truncation; against the new bridge, it passes with a generous (5s) deadline that is ~10× the observed steady-state latency.

Unit tests in `internal/supervisor/bridge_test.go` cover the plain Read/Write paths, `ErrBridgeBusy`, and the discard-on-detach contract. The iteration-boundary semantics are not unit-tested — the e2e test is the load-bearing assertion, and a unit test would have to reconstruct most of `runOnce`'s loop to be meaningful.

## Related

- Lessons: [§ Bridge input pump must be scoped per-iteration to survive child restart](../../lessons.md)
- Feature: [e2e-harness § Attach Restart Pattern](../features/e2e-harness.md)
- Code: `internal/supervisor/bridge.go`, `internal/supervisor/supervisor.go:246-280`
