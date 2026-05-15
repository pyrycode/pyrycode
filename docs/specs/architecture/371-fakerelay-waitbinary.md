# Spec: fakerelay `WaitBinary` synchronization for `TestForceCloseBinary`

Ticket: #371

## Files to read first

- `internal/e2e/internal/fakerelay/fakerelay.go:189-287` — `handleBinary`: the upgrade path. The race lives here: `websocket.Accept` (line 231) returns to the client after writing the 101 response, but registration `s.binaries[serverID] = bc` (line 260) happens in a later locked section. Extract: the exact ordering of Accept → mu.Lock → map insert.
- `internal/e2e/internal/fakerelay/fakerelay.go:587-596` — `LastBinaryHello`: existing fixture probe accessor. The new helper should match its locking style (single `s.mu.Lock(); defer s.mu.Unlock()`).
- `internal/e2e/internal/fakerelay/fakerelay.go:598-613` — `ForceCloseBinary`: the probe whose flake we are fixing. Do NOT modify its semantics; the `ghost` server-id branch must keep returning `false` immediately.
- `internal/e2e/internal/fakerelay/fakerelay_test.go:475-497` — `TestForceCloseBinary`: the failing test. The new helper call slots between `dialBinary` (line 481) and `ForceCloseBinary` (line 487).
- `internal/e2e/relay_test.go:145-158` — existing precedent: poll on `LastBinaryHello` to wait for binary readiness before calling `ForceCloseBinary`. The new `WaitBinary` helper is the same pattern, lifted to a reusable method so unit tests that haven't sent a hello can still synchronize.

## Context

`TestForceCloseBinary` flakes under CI's `-race` runner. The race is in the test-fixture's API expectations, not in production code:

1. Test calls `dialBinary(ctx, t, s, "alpha")`.
2. Server-side `handleBinary` runs `websocket.Accept` (`fakerelay.go:231`). Accept writes the 101 Switching Protocols response. The client unblocks from `websocket.Dial`.
3. Server-side handler then acquires `s.mu`, double-checks closed/claimed state, and finally inserts `s.binaries[serverID] = bc` (`fakerelay.go:260`).
4. Meanwhile the test, having returned from `dialBinary`, immediately calls `s.ForceCloseBinary("alpha")`, which reads `s.binaries[serverID]` (`fakerelay.go:606`).

Between steps 2 and 3 there is a window — small under nominal scheduling, but visible under `-race` on a contended CI worker — where the binary is "connected" from the client's perspective but not yet in the server's registry. `ForceCloseBinary` then returns `false`, the test fails.

The race is not in the WebSocket wire protocol or in any production code. The fakerelay package is test-only (`internal/e2e/internal/`). `ForceCloseBinary` is a fixture-only probe that observes server-internal bookkeeping. The fix belongs at the fixture layer.

The sibling e2e test `TestRelay_1011` (`internal/e2e/relay_test.go:132`) already avoids this race by polling `fakerelay.LastBinaryHello` (lines 145–154) before calling `ForceCloseBinary`. That polling presupposes the daemon has sent a hello envelope. The unit test `TestForceCloseBinary` raw-dials with no hello, so it has nothing analogous to poll on. This spec extracts the same polling pattern into a reusable helper.

## Design

### New API on `*fakerelay.Server`

Add one method to `internal/e2e/internal/fakerelay/fakerelay.go`, placed adjacent to `LastBinaryHello` (between `LastBinaryHello` and `ForceCloseBinary` is the natural slot):

```go
// WaitBinary blocks until a binary is registered for serverID, the
// context is done, or the server is Closed. Returns true if the binary
// became registered before ctx expired; false otherwise. e2e/unit
// tests call this between dialing a binary and probing server-side
// state (e.g. ForceCloseBinary) to close the upgrade→registration
// race: websocket.Accept unblocks the client before the server-side
// handler finishes inserting into s.binaries.
func (s *Server) WaitBinary(ctx context.Context, serverID string) bool
```

Behavior contract:

- On entry, checks the map under `s.mu`. If `serverID` is already registered, returns `true` immediately (no sleep).
- Otherwise polls every 2 ms (a `time.Ticker`, not `time.Sleep`, so context cancellation is responsive). Each tick re-checks the map under `s.mu`.
- Returns `true` the first tick the entry is present.
- Returns `false` when `ctx.Done()` fires before registration completes.
- If the server has been `Close`d, returns `false` (poll loop exits via ctx, since `httptest.Server` shutdown cascades to the test's deadline ctx, and Close itself does not abort foreign ctx — that's fine; callers always pass a deadline-bound ctx via `dialCtx`).

Tick interval rationale: 2 ms matches the order-of-magnitude granularity already used by `relay_test.go:150` (20 ms) but tighter because this is a unit-test inner loop, not an e2e wait for a daemon to dial out. The race window is microseconds; 2 ms is two-to-three orders of magnitude headroom. Total wall-time cost in the happy path is dominated by the very first lock check, which should hit on the first or second tick.

### Test change

Update `internal/e2e/internal/fakerelay/fakerelay_test.go:475-497` (`TestForceCloseBinary`):

- After the `t.Cleanup` registration of `bin.Close`, insert a `WaitBinary` call:
  ```go
  if !s.WaitBinary(ctx, "alpha") {
      t.Fatal("binary registration did not complete")
  }
  ```
- Leave the existing `ForceCloseBinary("alpha")` and ghost-case assertions unchanged.

The `ctx` already in scope from `dialCtx(t)` (line 478) is a 3-second deadline-bound context — appropriate budget.

### Do NOT change

- `ForceCloseBinary` itself. Its semantics (fast-return `false` for unknown server-ids) is correct and is asserted by the ghost-case branch at `fakerelay_test.go:494`. Wrapping it in a wait would either (a) slow the ghost case by the wait deadline or (b) require two variants. Neither is justified.
- `handleBinary`'s Accept-then-register order. Restructuring to pre-Accept registration would impose new ordering constraints on phone routing (the dispatcher at `fakerelay.go:536` would need to gate on a not-yet-existing "ready" channel to avoid forwarding to a nil `bc.conn`). That is a much larger change and unjustified — the race is in fixture API expectations, not in the WS protocol.
- Production code in `internal/transport`, `internal/supervisor`, or anywhere else. The flake is fixture-only.

### Concurrency

`WaitBinary` is read-only against `s.mu`-protected state, and the lock is released between ticks. It can run concurrently with all other Server operations. There is no shared mutable state added.

### Error handling

`WaitBinary` returns a `bool`, not an `error` — matching the style of `LastBinaryHello` (which also returns a bool, not an error). The single failure mode is "ctx expired before registration", and the caller already has the ctx; no wrapping needed.

## Testing strategy

### Acceptance gates (from ticket)

1. `go test -race -count=20 -run TestForceCloseBinary ./internal/e2e/internal/fakerelay/...` — 20 consecutive runs, all PASS.
2. `go test -race ./internal/e2e/internal/fakerelay/...` — full fakerelay test file passes, no regression.
3. `go test -race ./...` — full repo race test passes.

### New unit coverage

Add one new test to `fakerelay_test.go` (place it adjacent to `TestForceCloseBinary`):

- `TestWaitBinary` — single test function, two subtests via `t.Run`:
  - **happy path:** dial a binary, then call `WaitBinary(ctx, "alpha")`, assert `true`.
  - **timeout path:** without dialing, call `WaitBinary(ctxShort, "ghost")` where `ctxShort` has a 10 ms deadline, assert `false`. (No need to assert on elapsed time — the deadline-bound ctx makes the test deterministic regardless of scheduling jitter.)

The new test uses `t.Parallel()` like its siblings.

### What this fix does NOT prove

A passing 20-count race run reduces but does not zero the probability of a residual race elsewhere. If a *different* fakerelay test surfaces the same upgrade→registration timing — search for direct `s.binaries`-probing patterns in tests added after this ticket — the same fix applies. Not in scope here.

## Open questions

None. The fix is mechanical: one method added, one method call inserted, one new test.

## Commit message guidance

Per AC #1, the commit message must document:

- When `ForceCloseBinary` considers a binary "live" vs not: it considers a binary live iff `s.binaries[serverID]` is present at the moment of the lookup.
- Where the registration → ready → close lifecycle gap is: between `websocket.Accept` returning (which unblocks the client's `websocket.Dial`) and the subsequent `s.binaries[serverID] = bc` insertion under `s.mu`. `handleBinary` in `internal/e2e/internal/fakerelay/fakerelay.go:231-260` is the canonical reference.

A suggested subject line (developer may rephrase):

> `e2e/fakerelay: add WaitBinary to synchronize TestForceCloseBinary against upgrade→registration race (#371)`
