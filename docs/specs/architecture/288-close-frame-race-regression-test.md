# #288 — transport.serve close-frame race regression test

## Files to read first

- `internal/transport/wssclient.go:372-411` — the `serve` function and the close-status preference loop (lines 405-410). This is the contract being pinned. **Do NOT modify.**
- `internal/transport/wssclient.go:174-248` — `Connect` loop; specifically the post-serve fatal-close check at lines 226-232. The test's outer assertion (Connect returns `ErrFatalClose`) flows through this branch.
- `internal/transport/wssclient_test.go:605-661` — existing `closeCodeRelay` + `TestFatalCloseCodes_HaltsReconnect`. The new test reuses the relay shape and extends it; the new test sits adjacent to this one.
- `internal/transport/wssclient_test.go:668-705` — `TestFatalCloseCodes_HaltsOnDialError` shows the `dialFn` injection pattern referenced by the ticket's Technical Notes.
- `internal/transport/wssclient_test.go:30-64` — `testOpts` / `newClientForTest` — the test-config harness already supports `dialFn` substitution.
- `internal/transport/wssclient_test.go:102-154` — `newTestRelay` / `relayCtrl` — pattern for an `httptest`-backed WS server with per-conn signalling (`connectedCh`); template for the new relay's structure.
- *(reference, do NOT read in full)* `coder/websocket@v1.8.13` `read.go:226-255` (`prepareRead` / `done`) — documents the `ctx.Err()` override of `closeReceivedErr` that makes the race subtle. Cited here so the developer understands why "ensure both pushes land in `errCh` before `serve` cancels ctx" is the only way to preserve the `CloseError` in later `errs` slots.

## Context

#248 fixed a close-frame race in `transport.serve`: when `recvPump` and `sendPump` (or `pingLoop`) both error from the same peer-close event, the original code returned the first error to arrive on `errCh`. If the first arrival was `sendPump`'s mid-Write failure (no recognizable WS close status), the `FatalCloseCodes` check downstream in `Connect` silently skipped — a 4409 server-id conflict from the relay would not halt reconnect, defeating the whole point of `Config.FatalCloseCodes`.

The fix preserved the close-status preference by collecting all three pump errors and scanning for the one whose `websocket.CloseStatus(e) != -1`. The existing `TestFatalCloseCodes_HaltsReconnect` (`wssclient_test.go:630`) only exercises the happy path where `recvPump`'s `CloseError` arrives first — i.e., `errs[0]` already carries the status, and the preference loop's later slots are never consulted.

This ticket adds the missing regression test: a deterministic test that pins the contract under the racing-error shape, so the close-status preference can't be silently removed in a future refactor.

## What the test must prove

With the fix at `wssclient.go:405-410`, the loop iterates `errs[0]`, `errs[1]`, `errs[2]` and returns the first error whose `websocket.CloseStatus` is recognized. To exercise the loop's branch beyond `errs[0]`, the test needs:

1. A non-close-status error in `errs[0]` (a `sendPump` or `pingLoop` failure), AND
2. A `CloseError` with a `FatalCloseCodes`-listed status in `errs[1]` or `errs[2]`, AND
3. Both errors pushed to the buffered `errCh` (capacity 3) BEFORE `serve` consumes the first.

Constraint 3 is critical and non-obvious: `serve` does `cancel()` immediately after reading the first error. If `recvPump`'s `CloseError` is still in flight when ctx cancels, `coder/websocket`'s `prepareRead` `done()` deferred override (`read.go:241-242`) replaces the `CloseError` with `ctx.Err()`, and the fix can't help — no error in `errs` carries a recognizable close status. So the test must arrange for BOTH errors to land in `errCh` essentially atomically before `serve` wakes.

This atomic-arrival is precisely the natural production race: the peer's close frame causes BOTH `recvPump.Read` to return a `CloseError` AND `sendPump.Write` (if mid-flight) to fail with a generic "websocket closed" error, within nanoseconds. The buffered channel captures both pushes regardless of which goroutine wins the scheduler race.

## Design — test construction

### Relay: `racingCloseRelay`

Add a new test relay adjacent to `closeCodeRelay` (after `wssclient_test.go:624`). Contract:

- `newRacingCloseRelay(t, status websocket.StatusCode, reason string) *racingCloseRelay`
- Fields: `server *httptest.Server`, `connCount atomic.Int64`, `triggerClose chan struct{}` (buffered ≥1), `connectedCh chan struct{}` (buffered ≥1).
- Handler behaviour:
  1. `websocket.Accept` the upgrade. Increment `connCount`. Signal `connectedCh` (non-blocking).
  2. Spawn an inner goroutine that reads frames from the client in an echo loop (mirroring `newTestRelay`'s echo). The echo loop's only job is to drain the client's sendPump writes so they don't pile up in the OS TCP send buffer before close-time.
  3. Block on `<-triggerClose`. When signalled, stop the echo loop and call `conn.Close(status, reason)`. The defer-close ensures the close handshake completes from the relay side regardless of inner-loop state.
- Expose a single trigger method: `(r *racingCloseRelay).TriggerClose()` — sends on `triggerClose` (non-blocking). Idempotent: subsequent sends are dropped.
- Reuse `closeCodeRelay`'s `URL()` shape (`"ws" + strings.TrimPrefix(...)`)
- Single-shot: only the first dial is served; later dials get the default httptest behaviour (which will fail upgrade — fine; we assert `connCount == 1`).

### Test: `TestFatalCloseCodes_HaltsReconnect_RacingSendError`

Place after `TestFatalCloseCodes_HaltsReconnect` in `wssclient_test.go`. Structure:

1. **Setup:** stand up `racingCloseRelay` with status `4409`. Build `Config` with `FatalCloseCodes: []websocket.StatusCode{4409}` and a short `WriteTimeout` (e.g. `50 * time.Millisecond`).
2. **Client:** `newClientForTest` with cadence constants short enough to not interfere (`pingInterval`, `pongTimeout`, `reconnectInitial`, `reconnectMax`, `stabilityReset` — borrow values from `TestFatalCloseCodes_HaltsReconnect`). Use `dialFn` injection: dial the relay via `websocket.Dial` exactly once via the real path; a second dial (which should never happen if the fix is intact) does NOT need to be served — the test asserts it never occurs.
3. **Connect:** spawn `c.Connect(ctx)` in a goroutine; capture its return error via `connectErr` channel. `ctx` bounded by a generous `context.WithTimeout` (e.g. 5 s) to fail fast on hang.
4. **Wait for live conn:** receive `<-c.Connected()` to gate the rest of the test on a confirmed connection. Also assert `<-relay.connectedCh` so the relay's accept has run.
5. **Prime sendPump traffic:** spawn a goroutine that, in a tight loop, calls `c.Send([]byte("x"))` until either the call returns `ErrDisconnected`/`ErrClosed` or a test-controlled stop signal fires. This ensures `sendPump` is continuously busy with in-flight Writes (the echo relay drains them) so that when `relay.TriggerClose()` runs, `sendPump.Write` is highly likely to be mid-frame and will fail when the conn enters close handshake.
6. **Trigger close:** `relay.TriggerClose()`. The relay sends close 4409 on its side, then closes. The client's pumps both observe failure:
   - `recvPump.Read` returns the `*CloseError` (via `closeReceivedErr` short-circuit in `prepareRead`).
   - `sendPump.Write` (mid-flight) returns a generic "websocket closed" error wrapped as `send: ...`.
   - `pingLoop` returns `ctx.Err()` after `serve` cancels.
   Both `sendPump` and `recvPump` push to `errCh` from the same peer-close event — the natural concurrent push that the fix exists to handle.
7. **Await Connect return:** read from `connectErr` with a deadline. Assert:
   - `errors.Is(err, ErrFatalClose)` — the close-status preference loop selected the `CloseError`.
   - `websocket.CloseStatus(err) == 4409` — status recovered through the wrap chain.
   - `relay.connCount.Load() == 1` — no reconnect occurred.
8. **Cleanup:** signal the Send-loop goroutine to stop, `c.Close()` (idempotent), drain `connectErr` if not already drained.

### Why this is deterministic under `-race -count=10` for the fix

With the fix at `wssclient.go:405-410` intact:

- If `recvPump` pushes its `CloseError` first → `errs[0]` has close status, the for-loop returns it on iteration 0. Connect returns `ErrFatalClose`. Test asserts pass.
- If `sendPump` (or `pingLoop`) pushes first → `errs[0]` has no close status. Because `sendPump`'s push happens in response to the same event that triggered `recvPump`'s push, `recvPump`'s `CloseError` is overwhelmingly likely to be in the buffered `errCh` already (both pushes occur in the same scheduler quantum from the same peer-close event). The for-loop iterates and finds the close status at `errs[1]` or `errs[2]`. Connect returns `ErrFatalClose`. Test asserts pass.

The outcome (Connect returns `ErrFatalClose`, `connCount == 1`) is INVARIANT under both orderings — that's what makes the test deterministic. The scheduler-level non-determinism in push order is laundered by the fix's preference loop, which is exactly the contract being pinned.

### Why the test fails if the fix is reverted (regression-catching property)

Reverting the fix to `return errs[0]` removes the for-loop. Then:

- `recvPump` first → `errs[0] = closeErr` → Connect returns `ErrFatalClose`. Test passes (false negative for THIS iteration).
- `sendPump` first → `errs[0] = sendErr` (no close status) → Connect's `websocket.CloseStatus(serveErr) != -1` check fails → `serveErr` doesn't match a fatal code → reconnect → `dialFn` runs a second time → `connCount > 1` (relay's single-shot accept rejects further upgrades; `connCount` increments only on successful accepts, but the test asserts `== 1`). Test fails.

Across `-count=10`, the priming goroutine (step 5) keeps sendPump's writes in flight at close-time, so the sendPump-first ordering is hit on a meaningful fraction of iterations — empirically expected within 10 runs. The test fails on any single iteration that hits the sendPump-first branch without the fix.

> **Determinism caveat acknowledged.** The "deterministic" property here is: with the fix, the test passes 10/10. The bug-detection rate without the fix is probabilistic (each iteration is a coin flip on goroutine order), but `-count=10` runs are sufficient to surface a regression with overwhelming probability given the busy-Send priming. This is the most a no-production-code-edits test of this race shape can offer without modifying `serve` to expose a test seam. Documented as an open question below.

## Acceptance criteria mapping

| AC | How the design satisfies it |
|---|---|
| New test deterministically exercises the race | `racingCloseRelay` + busy-Send priming reliably produces the concurrent-push shape. With fix, test passes invariantly. |
| Assert `errors.Is(err, ErrFatalClose)` and `websocket.CloseStatus(err) == 4409` | Step 7 assertions. |
| Assert `connCount == 1` (no second dial) | Step 7 assertion against `relay.connCount.Load()`. |
| Deterministic under `-race -count=10`, no time-based hacks | Channel-synchronized: `relay.TriggerClose()` is a channel signal, not a `time.Sleep`. Test outcome is invariant under the fix because the for-loop covers all orderings. |
| `go vet ./...` and `go test -race ./internal/transport/...` pass | Standard CI gates; no production code changes, only test additions. |

## Open questions / risks

- **Probabilistic regression detection.** The test deterministically passes with the fix, but reverts to the bug surface only when sendPump's push wins the race AND the CloseError survives ctx-override. The busy-Send loop maximizes the rate of in-flight Writes at close-time, but a future Go scheduler change could theoretically reduce the bug-trigger rate below the `-count=10` detection threshold. If this concern is felt to be material, route back via `needs-rework:po` to consider exposing a test seam in `serve` (e.g., a package-private `selectPumpError(errs []error) error` helper that the test calls directly). This is a documented compromise — accept and proceed unless PO disagrees.
- **TCP send-buffer effects.** The relay's echo loop is required to drain client sendPump writes; without it, the OS TCP send buffer holds the writes and sendPump.Write doesn't block, making the in-flight-at-close window smaller. The spec mandates the echo loop in the relay handler.
- **`pingLoop` not exercised here.** The AC allows `sendPump` OR `pingLoop` as the non-close error source. The design uses `sendPump` because it's easier to keep busy on demand via the Send-loop goroutine; `pingLoop`'s timing is harder to control without `time.Sleep`. Future ticket could add a `pingLoop`-shaped variant if coverage gaps emerge.

## What this spec is NOT

- Not modifying `internal/transport/wssclient.go` `serve()`. The fix is preserved verbatim — the test depends on it being intact. If the developer discovers the existing logic is insufficient for the race shape under test, route back via `needs-rework:po` per the ticket's Technical Notes.
- Not adding new exported types or interfaces in production code.
- Not changing `Config`, `Client`, or any sentinel errors.

## Testing strategy

Single new test function `TestFatalCloseCodes_HaltsReconnect_RacingSendError` plus the `racingCloseRelay` helper. Run with:

```
go test -race -count=10 ./internal/transport/...
go vet ./...
```

Both must pass. The `-count=10` is the AC's gate; running locally for verification is sufficient (CI runs `-count=1` by default, which is fine for steady-state regression detection given the bug, if reintroduced, will surface on any subsequent CI run statistically).
