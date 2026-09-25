# #2617 — fakerelay `TestServerClose_NoGoroutineLeaks`: 10 s Close wait

## Files read

- `internal/e2e/internal/fakerelay/fakerelay_test.go` → `TestServerClose_NoGoroutineLeaks` — the 1 s `time.After` around `s.Close()`; the only file this ticket touches.
- `internal/e2e/internal/fakerelay/fakerelay.go` → `Server.Close`, `handleBinary` (the `s.closed` check at registration), `serveBinary` / `servePhone`, `binaryRecvPump` / `phoneRecvPump` — the close path audited below.
- `github.com/coder/websocket` v1.8.13 `close.go` → `Conn.Close`, `writeClose`, `waitCloseHandshake`, `waitGoroutines`; `conn.go` → `timeoutLoop` — the time limits on each conn close.
- Go stdlib `net/http/httptest` → `Server.Close` and its `StateHijacked` handling in `wrap` — what `s.http.Close()` waits for.

## Change

The test's wait for `Server.Close` goes from `time.After(time.Second)` to `time.After(10 * time.Second)`, with the fatal message updated to match. The goroutine-leak loop after it (its `baseline+2` comparison and 2 s poll) does not change. No production code changes.

Audit of `Server.Close` for an unbounded block (AC 1). No path found:

- It holds `s.mu` only to snapshot the conn maps, then releases it before any I/O.
- Each `conn.Close` is bounded by coder/websocket: `writeClose` has a 5 s context, and `waitCloseHandshake` has a 5 s context. Then `close()` closes `c.closed`, which ends `timeoutLoop`, so `waitGoroutines` returns at once. Its own 15 s cap is only a backstop.
- The fast path is the `cancel()` before each `conn.Close`. The recv pumps are blocked in `conn.Read(ctx)`, so `timeoutLoop` sees `readCtx.Done()` and calls `c.close()`. That unblocks the handshake as soon as the goroutine is scheduled. Under full-suite `-race` load, that scheduling delay is what exceeded 1 s.
- `s.http.Close()` (httptest) waits only for tracked conns. WebSocket conns are hijacked, and `wrap` forgets hijacked conns, so it does not wait on the WS handlers. A handler that registers after `closed` is set closes its own conn in its own goroutine, through the `s.closed` check in `handleBinary`/`handlePhone`.

10 s leaves room for scheduler delay on the fast path. It is below the worst case of a single handshake that times out (about 10 s). That case would need a server-side conn with no read in flight, and this test does not create one.

## Testing strategy

The test is its own proof: `go test -race -count=10 -run TestServerClose_NoGoroutineLeaks ./internal/e2e/internal/fakerelay/`. The flake depends on load and does not reproduce reliably on demand, so there is no RED step.

## Documentation handoff

None.
