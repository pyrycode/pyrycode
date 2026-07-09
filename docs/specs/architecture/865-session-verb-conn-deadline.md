# Spec #865 — Session verbs lose their response after 5s: extend the conn write deadline before the long op

**Ticket:** [#865](https://github.com/pyrycode/pyrycode/issues/865) · **Size:** XS · **Security-sensitive:** no (local Unix control socket; deadline/timing plumbing only, no auth/token/crypto/untrusted-input surface)

## Files to read first

- `internal/control/server.go:447-518` — `handshakeTimeout` const + `handle` dispatch. `handle` sets the 5s handshake deadline (line 470) and calls the two session handlers (lines 504, 506). This is the only place the const is consumed and the only two call sites of the handlers (confirmed via `codegraph_impact`).
- `internal/control/server.go:546-621` — `handleSessionsNew` (558) and `handleSessionsRm` (592): the two handlers to fix. Note `handleSessionsNew`'s comment at 549-552 ("Reusing the conn's handshake deadline would race the 5s handshake timer") — now stale; must be updated.
- `internal/control/server.go:795-860` — `handleAttach`: the shape to mirror. It receives `conn net.Conn`, then at line 854 does `_ = conn.SetDeadline(time.Time{})` before its long-running op. We follow the same "thread conn, reset the deadline before the long op" pattern, but **extend** (not clear) the deadline — see Design.
- `internal/control/server.go:198-278` — `Server` struct + `NewServer`. Note NewServer's signature is frozen across a ~34-call-site fan-out (the `SetRekeyer` doc-comment at 279-303 explains why post-construction overrides are used instead of new constructor params). The injectable-handshake seam (below) uses a per-`Server` field defaulted in NewServer, not a new param.
- `internal/control/client.go:282-303` — `request`: client sets its conn deadline from `ctx.Deadline()` (the 30s ctx), falling back to `DialTimeout` (5s) **only** when ctx has no deadline. Since `runSessionsNew`/`runSessionsRm` pass a 30s ctx, the client waits ~30s. **Confirms server-only:** the client is not the one dropping the response. No client change.
- `cmd/pyry/main.go:1416-1431` (`runSessionsNew`) and `1553-1575` (`runSessionsRm`) — client passes a 30s ctx. Read-only context; do not modify.
- `internal/control/sessions_new_test.go:18-174` — `fakeSessioner` double (`Create`/`Remove` ignore ctx today) and `startServerWithSessioner` helper. Both are extended by this ticket (add an op-delay to the fake; add a handshake-override helper variant).
- `internal/control/attach_create_if_missing_test.go:42-97` — `startServerWithResolverAndSessioner`: precedent for a helper that threads a sessioner and returns `(sock, stop)`.

## Context

`pyry sessions new` and `pyry sessions rm` report failure for a mutation that actually succeeded. The control server (`handle`) applies a 5s handshake deadline to every conn via `conn.SetDeadline(...)`. The two session handlers widen an **internal** 30s ctx for the `Pool.Create` / `Pool.Remove` call (documented claude-spawn latency is 2–15s) but never touch the conn deadline that the final `enc.Encode(Response{...})` response write rides on.

Once the op runs past 5s, the conn deadline has already fired: `enc.Encode` returns a deadline error that is silently discarded (`_ =`), and the client's read gets EOF. Result: the CLI prints an error, but the session **was** created (an operator-visible orphan) or **was** removed. `handleAttach` already avoids this by resetting the conn deadline before its long op; the two session handlers don't because they only receive the encoder, not the conn.

## Root cause (one line)

The conn **write** deadline (5s handshake) is left stale while the handler budgets 30s for the op; the response write fails silently after 5s.

## Design

Package touched: `internal/control` only. Production file: `internal/control/server.go`.

### 1. Thread `conn` into the two handlers, extend the deadline before the long op

Change the two handler signatures to receive the conn (mirroring `handleAttach`):

- `handleSessionsNew(conn net.Conn, enc *json.Encoder, payload *SessionsPayload)`
- `handleSessionsRm(conn net.Conn, enc *json.Encoder, payload *SessionsPayload)`

Update the two call sites in `handle` (server.go:504, 506) to pass `conn`.

In each handler, **after** the cheap validation (nil-sessioner guard; and in `rm`, the empty-id and policy-parse guards — those write their error responses well within the handshake window and don't need the extend) and immediately **before** the `s.sessioner.Create/Remove` call, reset the conn deadline:

```go
// Extend the conn write deadline past the op budget before the long
// call. The handler ctx (sessionOpTimeout) stays the binding budget;
// this is a backstop so a genuinely stuck response write still has an
// upper bound. Best-effort like handleAttach — a SetDeadline error on a
// broken conn surfaces on the Encode below.
_ = conn.SetDeadline(time.Now().Add(sessionOpTimeout + sessionOpConnGrace))
```

**Extend, do not clear.** `handleAttach` clears the deadline entirely (`time.Time{}`) because it streams indefinitely; a one-shot session verb has a bounded op, so it gets a bounded deadline strictly greater than the ctx budget. This keeps a stuck write from hanging the per-conn goroutine forever.

Introduce two package consts (dedup the `30*time.Second` literal currently duplicated across both handlers, and name the invariant):

- `const sessionOpTimeout = 30 * time.Second` — the handler ctx budget. Replace both `context.WithTimeout(context.Background(), 30*time.Second)` literals with this. (Leave `handleAttach`'s own 30s activation timeout untouched — different concern.)
- `const sessionOpConnGrace = 5 * time.Second` — margin the conn deadline sits above the ctx budget, so the ctx (not the conn) is the binding budget on the normal path.

Update `handleSessionsNew`'s stale comment (server.go:549-552): it currently claims the conn deadline is deliberately left alone. Replace with a note that the deadline is extended before the long op, mirroring `handleAttach`.

**AC3 is preserved for free:** the request is fully decoded in `handle` before either handler runs, so the extend happens strictly after the handshake read completes. The handshake deadline still bounds the initial request read; a client that sends nothing is still timed out. No ordering care needed.

### 2. Make the handshake timeout injectable (test seam — enables a fast, non-vacuous test)

A genuine regression test must have the op **outlast the pre-fix deadline** (otherwise it passes on buggy code — vacuous). Pre-fix, that deadline is the 5s handshake, so a socket-level test would need a >5s sleep for each of the three ACs (~15s of wall-clock). Instead, make the handshake timeout a per-`Server` field so tests can shrink it and keep the op sub-second.

- Rename the existing `const handshakeTimeout = 5 * time.Second` → `const defaultHandshakeTimeout = 5 * time.Second` (avoids a field/const name clash and reads clearly). Update its doc comment reference.
- Add an unexported `Server` field: `handshakeTimeout time.Duration`.
- `NewServer` sets `handshakeTimeout: defaultHandshakeTimeout` in the struct literal — production behaviour unchanged.
- `handle` reads `s.handshakeTimeout` instead of the const (server.go:470).

This follows the established `SetRekeyer` pattern (post-construction override so NewServer's frozen signature is untouched). Same-package tests set the field directly before `Serve` starts — no exported setter, no data race (the field is only read per-conn inside the Serve goroutine, which hasn't launched yet).

## Concurrency model

Unchanged. Each conn is handled in its own goroutine by `handle`; the session handlers are one-shot (decode → op → encode → close). The deadline is OS-level wall-clock on the Unix socket. The only new wrinkle is that the response write now has 35s of headroom instead of 5s. The test-seam field is written once before `Serve` launches and read-only thereafter, so no synchronization is required.

## Error handling

- `conn.SetDeadline` is best-effort (`_ =`), matching `handleAttach`. If it fails (broken conn), the subsequent `enc.Encode` surfaces the failure the same way it does today.
- The op ctx (`sessionOpTimeout`, 30s) remains the binding budget: a `Create`/`Remove` that genuinely exceeds 30s is cancelled via ctx and returns an error that is encoded and delivered (the conn deadline, at 35s, still has 5s of headroom to carry that error response out).
- No new error types, no new wire fields, no change to the `ErrorCode` mapping.

## Testing strategy

All new tests live in `internal/control` (same-package), table-free where a single scenario suffices, `t.Parallel()`. Extend two existing test doubles:

**Fake extension** — add `opDelay time.Duration` to `fakeSessioner` (`sessions_new_test.go:24`). At the top of `Create` and `Remove`, `if f.opDelay > 0 { time.Sleep(f.opDelay) }` before recording/returning. Each test owns its own fake, so no cross-test interference.

**Helper extension** — extract the body of `startServerWithSessioner` into `startServerWithSessionerHandshake(t, resolver, sessioner, handshake time.Duration)`; when `handshake > 0`, set `srv.handshakeTimeout = handshake` between `NewServer` and `go srv.Serve(ctx)`. `startServerWithSessioner` becomes a one-line delegate passing `0` (keep the default). Existing call sites unchanged.

Suggested constants for the timing tests: handshake override `200ms`, `opDelay 500ms`. Rationale: `500ms > 200ms` (comfortable margin under `-race`, so the op reliably outlasts the pre-fix deadline → non-vacuous) and `500ms ≪ 35s` (well under the extended deadline → passes after the fix).

Scenarios:

- **AC1 — `sessions.new` op > handshake still delivers the UUID.** Server with handshake=200ms; `fakeSessioner{returnID: <uuid>, opDelay: 500ms}`. Client calls `sessions.new`. Assert the response carries the minted UUID (no error, no EOF). *Non-vacuous check:* on unfixed `main` (no extend) the write fails at 200ms and the client gets EOF/error — this test fails there and passes after the fix.
- **AC2 — `sessions.rm` op > handshake still delivers the OK ack.** Same shape; `fakeSessioner{opDelay: 500ms}` returning nil. Client calls `sessions.rm` with a valid id. Assert `resp.OK == true` (no error, no EOF).
- **AC3 — slow client still timed out (handshake read bound unchanged).** Server with handshake=200ms. Dial a raw conn, send **no** request. Assert the client's read does **not** succeed as an OK/valid response and returns promptly (well under the 30s op budget) — either an error response or EOF is acceptable. Note for the implementer: because the handshake deadline has already expired by the time the server tries to encode its `decode request:` error, that error write may itself fail, so the client commonly sees EOF rather than the error string — assert on "not a success / read returns quickly", not on a specific error message.

Existing tests: no signature changes needed for the handler *call sites* (tests dial the socket; they don't call `handleSessionsNew`/`handleSessionsRm` directly — confirmed via grep). The const rename and the `NewServer` literal change compile against all existing call sites unchanged.

## Open questions

None blocking. One judgment call already made: **extend to `sessionOpTimeout + sessionOpConnGrace` (35s) rather than clear to `time.Time{}`** — chosen per the ticket's guidance so the ctx stays the binding budget and a stuck write keeps an upper bound. If the developer finds clearing simpler and the reviewer agrees, the AC outcomes are identical; the extend is the recommended shape.
