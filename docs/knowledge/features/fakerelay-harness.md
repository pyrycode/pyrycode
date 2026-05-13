# Fake-Relay Harness

`internal/e2e/internal/fakerelay` is an in-process WebSocket server that
speaks the **routing half** of the mobile↔relay protocol
(`docs/protocol-mobile.md` § Authentication, § Routing envelope). It
exists so daemon-side e2e tests can exercise the full WS roundtrip —
binary ↔ relay ↔ phone — without depending on the `pyrycode-relay`
binary or live infrastructure.

Phase: #295 ships the package in isolation (no consumers wired). A
sibling ticket ships the fake-phone client; a third ticket consumes both
for the appendix-flow roundtrip test.

## Surface

```go
package fakerelay

func New(logger *slog.Logger) *Server   // returns running; nil logger panics
func (*Server) URL() string             // ws://127.0.0.1:NNNN — no trailing path
func (*Server) Close() error            // idempotent, always nil
```

Callers append `/v1/server` (binary upgrade) or `/v1/client` (phone
upgrade) to `URL()`. No `Config` struct yet — every test wants "boot it,
get a URL, close it." A future `NewWithConfig` can add overrides
non-breakingly.

## Endpoint contracts

### `GET /v1/server` — binary upgrades

Header: `x-pyrycode-server` = the claimed server-id.

| Condition | Response |
|---|---|
| Header empty | HTTP 400 |
| `server-id` already claimed (first-claim-wins) | HTTP 409 |
| Server is closing | HTTP 503 |
| OK | `101 Switching Protocols`, register in `binaries[serverID]` |

First-claim-wins **only while the first holder's conn is open** — there
is no grace period; the server-id is reusable the instant the binary
disconnects.

### `GET /v1/client` — phone upgrades

Headers (all required, non-empty): `x-pyrycode-server`,
`x-pyrycode-token`, `x-pyrycode-device-name`.

| Condition | Response |
|---|---|
| Any header empty | HTTP 400 |
| No binary bound to `server-id` | HTTP 503 |
| Server is closing | HTTP 503 |
| OK | `101`, assign `conn_id = "c-N"` (monotonic), register in `phones[connID]` |

The relay does NOT validate the token contents — the binary owns that
check per #249. The harness only checks non-emptiness.

### Rejection deviates from the production wire spec

Production rejections happen post-upgrade as WS close codes (`4409` /
`4404` / `4401`). The harness uses **pre-upgrade HTTP status** because
the status surfaces directly in `websocket.Dial`'s returned error, which
is simpler for consumer tests to assert on. Documented in the package
comment as a deliberate deviation.

## Routing rules (`docs/protocol-mobile.md` § Routing envelope)

```
phone → relay:    raw frame bytes
relay → binary:   {"conn_id": "c-N", "frame": <raw>}

binary → relay:   {"conn_id": "c-N", "frame": <raw>}
relay → phone:    raw frame bytes
```

- Wrap uses `protocol.RoutingEnvelope` from #255; `Frame json.RawMessage`
  splices byte-for-byte.
- Phone is expected to send well-formed JSON; the wrapper places `frame`
  as `RawMessage` and requires `json.Valid(data)` (a non-JSON phone
  frame tears the phone down with a Debug log).
- Malformed binary wrappers or unknown `conn_id`s are
  **Debug-logged-and-dropped** (not relay-shutdown) — surfaces as a
  missing receive in the consumer test rather than a relay-side shutdown
  that masks the cause.

## Concurrency model

```
httptest.Server (one accept goroutine)
  /v1/server → spawn serveBinary
  /v1/client → spawn servePhone

per accepted conn: two goroutines (recvPump + sendPump)
  serveBinary: cancel-on-first-return + drain-the-other + close(bc.done)
  servePhone:  cancel-on-first-return + drain-the-other + close(pc.done)
```

No ping-loop goroutine — `coder/websocket` auto-responds to pings on the
server side.

Lock discipline: only `s.mu`. Held only for map mutations + `connSeq`
increment + lookups. **Never held across `conn.Read`, `conn.Write`, or
channel sends.** No ordering graph.

`sendCh` is **unbuffered on both sides** — backpressure is the consumer
test's problem. A slow binary blocks phones writing to it; this is the
observable failure mode that the consumer test should drive.

Every routing-path channel-send watches three signals via `select`:
`peer.sendCh`, `ctx.Done()`, and `peer.done`. The `peer.done` arm is
load-bearing: without it, a phone sending to a binary whose recvPump has
already returned (sendPump being cancelled) would block forever and
defeat the `-race` no-leak invariant.

## Cleanup-cascades-on-binary-loss

When a binary's serve goroutine returns, the cleanup walks `phones` for
matching `serverID` and force-closes each. A phone whose binary is gone
cannot be routed anywhere — keeping it open would leak goroutines under
`go test -race`. This matches the production relay's behavior of
dropping phones whose binary went away.

## Shutdown sequence (`Server.Close`)

1. Under `s.mu`: set `closed=true`, snapshot every binaryConn +
   phoneConn, release.
2. Force-close each conn (`cancel()` + `conn.Close(NormalClosure)`) —
   unblocks the per-conn pumps' Read/Write.
3. `s.http.Close()` — waits for in-flight HTTP handlers to return.
4. Returns `nil`. Idempotent (the `closed` flag short-circuits a second
   call).

Each handler re-checks `s.closed` under `s.mu` AFTER `websocket.Accept`
so a handler racing `Close` aborts cleanly instead of installing a
doomed conn.

## What's NOT modeled (deliberate)

- **TLS termination.** Harness binds plain `ws://`.
- **30-second server-id release grace period.** AC pins immediate release.
- **Production WS close codes** (`4409`/`4404`/`4401`). Pre-upgrade HTTP
  status is the harness's substitution.
- **Token-contents validation.** Any non-empty token accepted.
- **`hello` / `hello_ack` envelope dispatch.** Routing seam only.
- **Persistence, rate limiting, throttling.** Maps live for the
  `Server`'s lifetime; no quotas.

## Layout

```
internal/e2e/internal/fakerelay/
  fakerelay.go        ~425 LOC, package fakerelay, no build tag
  fakerelay_test.go   ~400 LOC, no build tag (stdlib testing)
```

No build tag on either file: the package is `internal/e2e/internal/`-fenced
so only e2e-package code can import it, and a tiny library compiles
under `./...` without harm.

## Related

- Per-ticket implementation notes: [`codebase/295.md`](../codebase/295.md).
- Wire-spec source-of-truth: `docs/protocol-mobile.md` § Authentication,
  § Routing envelope, worked example.
- Library + lifecycle precedent:
  [`transport-package.md`](transport-package.md) (#247 — same
  `coder/websocket` dep, lifecycle-goroutine-before-conn-observable
  shape).
- Wire types: [`protocol-package.md`](protocol-package.md) (#255 —
  `RoutingEnvelope` is the wrap/unwrap shape).
- Sibling harness package:
  [`fakeclaude-binary.md`](fakeclaude-binary.md) (#122 — same
  `internal/e2e/internal/` placement convention, supervised-child
  stand-in).
- Consumer roadmap: sibling fake-phone client (separate ticket) and the
  consuming roundtrip e2e test (third ticket) — neither wired yet.
