---
ticket: 290
title: TestServerIDConflict_FatalNoReconnect flake under -race -count=N
size: S
---

# #290 — `internal/relay`: harden the FatalCloseCodes path against in-flight Read cancellation

## Files to read first

- `internal/transport/wssclient.go:174-248` — `Connect` loop. The post-serve `FatalCloseCodes` check at lines 226-232 is the contract under stress. **The Dial-path check at lines 191-203 is a sibling; treat both as the surface area.**
- `internal/transport/wssclient.go:372-411` — `serve`. The close-status preference loop (405-410) was added by #288. The line that's load-bearing for #290 is the **`cancel()` at line 396**, between the first and second `<-errCh` reads. This is the timing window the bug lives in.
- `internal/transport/wssclient_test.go:707-779` — `racingCloseRelay` helper added by #288. Reuse this; do NOT add a parallel relay shape unless investigation proves the existing one can't reproduce.
- `internal/transport/wssclient_test.go:781-868` — `TestFatalCloseCodes_HaltsReconnect_RacingSendError`. Its design notes call out the `ctx.Err()` override issue and document it as an "open question" — this ticket converts that open question into a production fix.
- `internal/relay/connection.go:183-212` — `run()` loop. Observe that `Connected()` and `transportErrCh` are siblings in the same select; understand how `ConnCount > 1` propagates from a missed FatalCloseCodes match in `transport.Connect`.
- `internal/relay/connection_test.go:42-233` — `testRelay` + `behaviorCloseImmediately4409` (line 152-155). The handler closes with 4409 then returns; the deferred `conn.Close(StatusNormalClosure)` (line 149) follows. Read closely — the deferred-after-explicit-close pattern is one of the variables to rule in or out during investigation.
- `internal/relay/connection_test.go:410-434` — `TestServerIDConflict_FatalNoReconnect`. The failing assertion is `ConnCount = N, want 1`; `Wait()`'s ErrServerIDConflict assertion has been observed passing, so the failure is on the reconnect-count side, not the terminal-classification side.
- `docs/specs/architecture/288-close-frame-race-regression-test.md` § "Constraint 3 is critical and non-obvious" — already names the root cause in passing: `coder/websocket`'s `prepareRead.done()` deferred override replaces `closeReceivedErr` with `ctx.Err()` when ctx fires mid-Read. #290 acts on that knowledge.
- *(reference, do NOT read in full)* `coder/websocket@v1.8.13` `read.go:226-255` (`prepareRead` / `done`) — the library-internal source of the `ctx.Err()` override.

## Context

PR #289 (salvage of #248) shipped `internal/relay.Connection` with `FatalCloseCodes: [4409]` so a `statusServerIDConflict` close from the relay halts the reconnect loop with `ErrServerIDConflict`. CI's `-count=1` invocation passes; `go test -race -count=10 -timeout 120s ./internal/relay/` fails repeatedly on `TestServerIDConflict_FatalNoReconnect` with `ConnCount = 3/5/9/10, want 1`. The varying `ConnCount` rules out a flat counting bug — the transport IS reconnecting some fraction of the time, then eventually one reconnect's close *is* classified as 4409 and the test sees `ErrServerIDConflict` (which is why `Wait()`'s classification assertion still passes).

Three candidates were named in the ticket:

1. Test-side `ConnCount` counts handler invocations rather than unique accepts.
2. Transport-side `websocket.CloseStatus(serveErr) == -1` under contention, so the FatalCloseCodes check misses.
3. The `connectedCh` signaling added in #289 emits an additional signal that triggers an unintended retry path.

**Pre-investigation read of the candidates:**

- Candidate 1 is *very likely* false. The test relay's handler increments `r.connCount.Add(1)` after `websocket.Accept` returns (line 141 of `connection_test.go`); each successful WS upgrade is a fresh HTTP request from the transport's `Connect` loop. `ConnCount > 1` therefore means the transport *did* dial and accept multiple times — which means the FatalCloseCodes check missed on the prior attempts. Investigation should confirm but should not absorb significant time.
- Candidate 2 is *most likely* the cause and is named by inheritance from #288's spec. When `sendPump` or `pingLoop` errors first, `serve()` calls `cancel()` (line 396) BEFORE `recvPump`'s pending `conn.Read` surfaces the peer's `CloseError`. `coder/websocket`'s `prepareRead.done()` deferred override then replaces the `closeReceivedErr` with `ctx.Err()`, and `recvPump`'s return is a wrapped `context.Canceled` — no recognizable close status anywhere in `errs[]`. The post-serve check fails, Connect reconnects, `ConnCount++`.
- Candidate 3 is *very likely* false. `connectedCh` is buffered to 1 with drop-on-full semantics; multiple emissions get coalesced. Each Connect cycle emits at most once; `run()`'s observed `Connected()` signals are 1:1 with successful serves. Multiple `Connected()` reads only happen if `transport.Connect` reconnects — which is downstream of candidate 2.

The spec proceeds with candidate 2 as the working hypothesis. The investigation step MUST confirm before applying the production fix; if investigation falsifies it, the spec's later sections branch to the alternate fix shape.

## Hypothesis (working)

In `transport.serve` (lines 384-410):

```go
errCh := make(chan error, 3)
go func() { errCh <- c.recvPump(ctx, conn) }()
go func() { errCh <- c.sendPump(ctx, conn) }()
go func() { errCh <- c.pingLoop(ctx, conn) }()
...
errs = append(errs, <-errCh)
cancel()                              // ← line 396; race lives here
errs = append(errs, <-errCh, <-errCh)
```

When the peer closes with 4409:

- `recvPump` is parked inside `conn.Read(ctx)`. The library will surface a `*websocket.CloseError` once it parses the inbound close frame.
- `sendPump` may be parked in `select { ... }`, or — in the `TestServerIDConflict_FatalNoReconnect` shape specifically — about to enter `conn.Write` with the handshake's hello frame (relay accepts then closes BEFORE reading).
- `pingLoop` is parked on a ticker.

Two scheduler orderings:

| Order | First `errCh` arrival | After `cancel()`, recvPump returns | `errs[]` close-status |
|---|---|---|---|
| A (recvPump first) | recvPump's `CloseError(4409)` | (already returned) | status = 4409 at errs[0]. Connect halts. ✓ |
| B (sendPump first) | sendPump's `send: ... use of closed network connection` | `prepareRead.done()` overrides `closeReceivedErr` with `ctx.Err()` → recvPump returns wrapped `context.Canceled` | status = -1 across all 3 slots. Connect reconnects. ✗ |

Order B is the bug. Order A is the green path. Under `-race -count=N`, order B fires often enough to produce `ConnCount = 3..10`.

`TestFatalCloseCodes_HaltsReconnect_RacingSendError` (#288) intentionally arranges atomic dual-arrival (recvPump pushes its `CloseError` to the buffered `errCh` BEFORE `serve` reads the first), so its outcome is invariant under both orderings. The handshake-shape test (`TestServerIDConflict_FatalNoReconnect`) does NOT arrange atomic dual-arrival — sendPump's mid-Write failure can land in `errCh` while recvPump is still inside `Read`. That's the structural difference that explains why #288's test passes and #290's flakes.

## Investigation protocol

The developer's first task is confirmation, not coding. Do it in this order; do not skip steps.

### Step 1 — local reproduction

Confirm the flake reproduces on the developer's machine before instrumenting:

```
go test -race -count=10 -timeout 120s ./internal/relay/ -run TestServerIDConflict_FatalNoReconnect
```

Expected: at least one failure with `ConnCount = N, want 1` for some `N > 1`. If 10 runs pass clean, escalate to `-count=50` before declaring "cannot reproduce". If 50 still passes clean, the flake is timing-bound to the original observer's machine — instrument anyway (step 2) and proceed with the hypothesis since the structural reading is unambiguous.

### Step 2 — instrument the FatalCloseCodes branches (throwaway logging)

Temporarily add diagnostic logging at the two `FatalCloseCodes` check sites in `internal/transport/wssclient.go`:

- After line 197 (Dial-error path), log `status` and the underlying error.
- After line 226 (post-serve path), log `status`, the formatted serveErr, AND the `errs[]` slice from `serve()` (requires hoisting `errs` out of `serve` and back through the return — alternatively, log from inside `serve` just before `return errs[0]` if no slot has a close status).

Run the failing invocation. Confirm:

- On failing iterations, log shows `status = -1` at the post-serve branch on the first N-1 attempts. (Hypothesis confirmed.)
- The accompanying `serveErr` is a wrapped `context.Canceled` or `use of closed network connection` (not a `*CloseError`).
- The `errs[]` slice on those iterations contains no element with `websocket.CloseStatus(e) != -1`. This is the smoking gun: even with #288's preference loop, NO error in `errs[]` carries the close status — the `CloseError` was clobbered by `ctx.Err()`.

If the logging contradicts the hypothesis — e.g., `status = 4409` is observed but Connect still reconnects, or `errs[]` contains a `CloseError` but the preference loop doesn't pick it — STOP and branch to § "Alternate root cause" below.

### Step 3 — sibling-test interaction check

```
go test -race -count=20 -run TestServerIDConflict_FatalNoReconnect ./internal/relay/
```

If the flake disappears under isolation, the cause is partly contention with sibling parallel tests in the package. The fix is the same (the underlying race is in `serve`), but the determinism story for the regression test changes — see § "Regression test" below.

Remove all instrumentation before proceeding to the fix.

## Design — production fix (if hypothesis confirmed)

Localised hardening of `transport.serve` so the close-status preference can't be defeated by the `ctx.Err()` override. The fix is a single-function change in `wssclient.go`.

### Rule (contract sketch — NOT to be pre-written as code)

In `serve`, between reading the first `errCh` arrival and calling `cancel()`:

- If `websocket.CloseStatus(firstErr) != -1` → already have the close status; cancel immediately as today.
- Else → grant a bounded grace window for one more `errCh` arrival, then cancel.

```
firstErr := <-errCh
errs := append(errs[:0], firstErr)
if websocket.CloseStatus(firstErr) == -1 {
    // grace-wait for recvPump's close-frame observation OR a brief deadline.
    select {
    case e := <-errCh:
        errs = append(errs, e)
    case <-time.After(closeFrameGrace):
    }
}
cancel()
// Drain remaining slots (1 or 2 depending on grace branch).
for len(errs) < 3 { errs = append(errs, <-errCh) }
// Existing preference loop, unchanged.
for _, e := range errs {
    if websocket.CloseStatus(e) != -1 { return e }
}
return errs[0]
```

Constants:

- Add a package-level constant `closeFrameGrace` near the existing constants block (lines 28-36). Value: `50 * time.Millisecond`. Justification: `coder/websocket`'s `Read` returns essentially instantly when the close frame is already buffered in the socket; the grace only stretches the disconnect path when no close frame is forthcoming (TCP reset, server crash), which is already a tear-down path where 50ms is irrelevant. Do NOT make this configurable via `Config` — it's a wire-internal timing constant.
- Tests substitute a shorter `closeFrameGrace` via the existing `testOpts` pattern in `newClientForTest` (see `wssclient_test.go:30-64`) — extend `testOpts` and `Client` with a `closeFrameGrace` field analogous to the existing cadence overrides. This is the only API surface change needed.

### What this does NOT change

- `FatalCloseCodes` semantics: still matches only WS close codes the library DOES classify. No semantic widening to "treat ANY unclassified close as the configured fatal code." That widening is a separate question and is flagged out-of-scope below.
- The Dial-path check at lines 191-203 stays as-is. The race is exclusive to the post-serve path because the Dial path doesn't have concurrent pumps fighting over ctx.
- `connectedCh`, `connDone`, the `setConn` sequencing, the `Send` / `Receive` semantics. Untouched.
- Public API surface. No new exported types, no `Config` additions, no sentinel errors.

### Why this is correct under both orderings

- Order A (recvPump first, has CloseError): the new branch skips the grace-wait (status != -1) and behaves exactly as today. Pass.
- Order B (sendPump or pingLoop first, no close status): grace-wait fires. recvPump's pending `conn.Read` typically returns within microseconds because the close frame is already buffered. `errCh` receives recvPump's `CloseError`. Preference loop picks it. Pass.
- Order B', degenerate case (recvPump truly hung — e.g. TCP black-hole): grace-wait expires after 50ms. Cancel proceeds as today; `recvPump` returns `ctx.Err()`. Preference loop finds no close status. Connect reconnects. **This matches today's behaviour for true black-hole drops.** The grace-wait does not introduce a new failure mode; it only widens the existing window for recvPump to surface a close frame that's actually inbound.

### What this fix does NOT cover (in-scope flag for follow-up)

If the relay sends close 4409 but the wire is severed before the close frame reaches the client (TCP RST, NIC drop), recvPump returns a generic IO error and the FatalCloseCodes check still misses. The PO's Technical Notes pointer explicitly asks whether non-close-frame disconnects should also be matched against `FatalCloseCodes` when a discriminator is configured (e.g. a header-derived `ServerID` discriminator the client could observe at Dial time). That is a **semantic widening of FatalCloseCodes**, not a close-frame visibility fix. Out of scope for #290. If investigation surfaces evidence that the production failure mode is TCP-level (not the in-flight Read cancellation), STOP and route back to PO with that finding. Do NOT expand scope inside #290.

## Design — regression test

Single new test, `TestFatalCloseCodes_HaltsReconnect_HandshakeShape`, placed adjacent to `TestFatalCloseCodes_HaltsReconnect_RacingSendError` in `internal/transport/wssclient_test.go`. Test characteristics:

- **Relay:** reuse `racingCloseRelay` (#288). Its single-accept + echo + on-demand close design is exactly what's needed; do not duplicate.
- **Client config:** `FatalCloseCodes: [4409]`, short `WriteTimeout`, short cadence constants. Substitute a shorter `closeFrameGrace` (e.g. `5 * time.Millisecond`) via the new `testOpts` field so the test runs quickly while still exercising the grace branch.
- **Sequence:**
  1. `c.Connect(ctx)` in a goroutine, capture err on a channel.
  2. Wait on `<-c.Connected()` and `<-relay.connectedCh`.
  3. **Force the sendPump-first ordering deterministically.** Two approaches; the test must pick one and only one:
     - **Approach A (preferred):** spawn a goroutine that calls `c.Send([]byte("x"))` repeatedly in a tight loop until a stop signal. After observing Connected(), trigger `relay.TriggerClose()`. The busy-Send keeps sendPump's `Write` mid-frame on a high fraction of iterations; combined with the test's `closeFrameGrace = 5ms` (vs. the prod default 50ms), `-count=10` reliably exercises the grace branch.
     - **Approach B (fallback if A is non-deterministic):** introduce a package-private `serveHooks` struct in `wssclient.go` with an optional `beforeCancel func()` callback that fires AFTER the first errCh arrival and BEFORE cancel(). The test installs a hook that asserts the first error was sendPump's mid-Write failure (no close status) before allowing cancel to proceed. This is a test seam. Prefer A; only use B if A produces flakes under `-count=10`.
- **Assertions:** identical to `TestFatalCloseCodes_HaltsReconnect_RacingSendError`:
  - `errors.Is(connectErr, ErrFatalClose)`
  - `websocket.CloseStatus(connectErr) == 4409`
  - `relay.connCount.Load() == 1`
- **Regression-catching property:** without the grace-wait fix, the sendPump-first race surfaces `connCount > 1` on a meaningful fraction of `-count=10` iterations (this is exactly the failure shape #290's reproducer reports). With the fix, the test passes deterministically because recvPump's CloseError lands in `errCh` during the grace window.

### Do NOT delete the relay-side test

`TestServerIDConflict_FatalNoReconnect` in `internal/relay/connection_test.go` stays as-is. The transport-level regression test pins the contract at the transport boundary; the relay-level test is now indirectly hardened by the transport fix. AC #5 (10 consecutive `-race -count=100` runs) is the empirical confirmation that the relay-level test no longer flakes.

### Single-test stress runs for the AC

The PO ticket asks for `go test -race -count=100 -timeout 300s ./internal/relay/` × 10 consecutive runs in the PR body. Run this AFTER the fix is in place. If it passes 10/10, AC #5 is satisfied. If any run fails, the fix is incomplete — return to step 2 of the investigation protocol and reassess.

## Alternate root cause (only if investigation refutes hypothesis)

If step 2 logging shows the FatalCloseCodes path is NOT being missed (status == 4409 reliably on every iteration) but `ConnCount` still > 1, the cause is downstream of the transport fatal-close return. Likely path:

- `c.client.Connect(ctx)` returned `ErrFatalClose`, but `c.classifyTransportErr` (`connection.go:295`) didn't classify it.
- OR `run()`'s select picked Connected() repeatedly before transportErrCh, and each handshake iteration redialled — but redialing requires `c.client.DropConn()` to spawn fresh dials, which it does NOT (DropConn just closes the live conn; it does not restart Connect).

If you reach this branch, you have evidence pointing at relay-side flow. STOP and route back to PO with the evidence; the fix may belong in a different package and the scope is no longer S. Do not silently swap files mid-spec.

## Out-of-scope (do not touch in this ticket)

- **Semantic widening of FatalCloseCodes** to match non-close-frame disconnects (TCP RST, NIC drop). Flag-back-to-PO territory.
- **Refactoring `serve`'s pump-error model** to a structured errgroup or similar. This ticket touches one branch in one function.
- **The double-close in `connection_test.go:148-150`** (deferred `Close(StatusNormalClosure)` after explicit `Close(4409)`). May be a stylistic smell but is not the cause of the flake — `coder/websocket.Close` is idempotent on a closed conn. Leave it.
- **Updating `docs/knowledge/architecture/system-overview.md`** to reflect the new grace-wait. Documentation phase owns shared docs (see architect CLAUDE.md). The fix's "why" goes in code comments and the PR body.

## Acceptance criteria mapping

| AC | How the design satisfies it |
|---|---|
| Root cause identified & documented in PR | Investigation protocol step 2 produces the logs that pin candidate 2 (`status = -1`, `errs[]` has no close status). Document the finding in the PR body verbatim. |
| Fix targets identified root cause only | Single-function change in `transport.serve`. No refactors of unrelated code; FatalCloseCodes semantics unchanged. |
| Production-side fix has deterministic regression test | `TestFatalCloseCodes_HaltsReconnect_HandshakeShape` per § "Regression test". |
| `go test -race -count=100 -timeout 300s ./internal/relay/` passes 10 consecutive runs | The grace-wait closes the race window for the sendPump-first ordering. Empirical verification at AC time. |
| No new flakes in `./internal/relay/` or `./internal/transport/` under same stress | Grace-wait only widens the existing disconnect window; it cannot introduce a new failure mode for paths that previously succeeded. Standard CI gates (`go vet`, `staticcheck`, `go test -race`) cover the broader package. |

## Open questions

- **Grace-wait constant value.** 50ms is a guess informed by close-frame-typical timing (microseconds when buffered; milliseconds under contention). If `-count=100 × 10` reveals 50ms is insufficient under heavy race contention, the right next step is NOT to bump the constant — it's to investigate why the close frame isn't buffered, because that points at a deeper issue. Document the failing iteration's behaviour and route back to PO.
- **Test-seam preference (Approach B in the regression test).** If Approach A in the regression test produces flakes, the cleaner long-term answer may be a `beforeCancel` hook in `serve`. Adding a test seam to production code is acceptable here because it's a no-op outside tests (nil-checked). Prefer A; fall back to B with a one-paragraph justification in the PR body.
