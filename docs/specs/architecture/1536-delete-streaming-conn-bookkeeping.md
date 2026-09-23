# #1536 — control: delete the streaming-conn bookkeeping

Short plan: a pure deletion, no new type, state or failure mode.

## Files read

- `internal/control/server.go` → `Server` (fields `streamingWG`, `streamConns`), `NewServer` (the `streamConns` initialiser), `Server.Serve` (the `s.streamingWG.Wait()` on the accept-error shutdown path), `Server.Close` (the snapshot-and-close block and its #863 handoff-race comment).
- `internal/control/server_test.go` → `fakeSession` (fields `attachFn`, `resizeCalls`, `resizeErr`; methods `Attach`, `Resize`), `resizeCall`, and the doc sentence about `attachFn`.

## Change

#1348 deleted `handleAttach`, the only writer of `streamConns` and the only caller of `streamingWG.Add`. Both fields go, with their comments, the `NewServer` map initialiser, the `streamingWG.Wait()` in `Serve`, and the block at the end of `Close` that snapshots and closes `streamConns`. `Close` keeps its lock discipline: it still unlocks `s.mu` before returning `firstErr`. Behaviour is unchanged — `Wait` on a zero `WaitGroup` returns immediately and the set was always empty. In the test file, `fakeSession` drops `Attach`, `Resize` and the fields only they used, plus `resizeCall`; the `Session` interface no longer requires either method. The `io` import remains in use elsewhere in the file. Comments elsewhere that cite `handleAttach` as a precedent are out of scope per the ticket.

## Testing strategy

No new logic, so no new test. The existing `internal/control` tests pass unedited; the compiler proves nothing still references the deleted symbols, and a grep for `streamConns`, `streamingWG`, `resizeCall` over `internal/control` returns nothing.

## Documentation handoff

None in the ticket.
