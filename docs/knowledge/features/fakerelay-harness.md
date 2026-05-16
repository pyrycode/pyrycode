# Fake-Relay Harness

`internal/e2e/internal/fakerelay` is an in-process WebSocket server that
speaks the **routing half** of the mobile↔relay protocol
(`docs/protocol-mobile.md` § Authentication, § Routing envelope). It
exists so daemon-side e2e tests can exercise the full WS roundtrip —
binary ↔ relay ↔ phone — without depending on the `pyrycode-relay`
binary or live infrastructure.

Phase: #295 ships the package in isolation (no consumers wired). #296
ships the sibling fake-phone client. #301 extends the harness with
binary-direct `hello`/`hello_ack` dispatch and a WS-4409 close mode so
the real `pyry` daemon can complete its binary↔relay handshake against
the harness; the e2e test in `internal/e2e/relay_test.go` is the first
production consumer.

## Surface

```go
package fakerelay

func New(logger *slog.Logger) *Server   // returns running; nil logger panics
func (*Server) URL() string             // ws://127.0.0.1:NNNN — no trailing path
func (*Server) Close() error            // idempotent, always nil

// e2e test hooks (#301)
func (*Server) RejectNextBinaryWith4409()                       // arm one-shot WS 4409
func (*Server) LastBinaryHello(serverID string) (protocol.Envelope, bool)
func (*Server) ForceCloseBinary(serverID string) bool           // close with 1011 (StatusInternalError)
func (*Server) WaitBinary(ctx context.Context, serverID string) bool  // (#371) block until s.binaries[serverID] is registered
```

`WaitBinary` exists because `websocket.Accept` writes the 101 response —
unblocking the test's `websocket.Dial` — *before* `handleBinary` finishes
inserting into `s.binaries` under `s.mu`. Tests that probe server-side
bookkeeping immediately after a raw dial (`ForceCloseBinary`,
`LastBinaryHello`, future probes) must synchronize on a positive signal
or race the handler under `-race`. Polls every 2 ms on a `time.Ticker`
under the caller's ctx; returns `true` on registration, `false` on ctx
expiry. `LastBinaryHello`-polling remains the right pattern for e2e
tests that already send a hello (the daemon does that on dial-out);
`WaitBinary` is the equivalent for unit tests that raw-dial without a
hello. See [`codebase/371.md`](../codebase/371.md).

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

**Exception (#301):** `RejectNextBinaryWith4409()` arms a one-shot
opt-in mode for the next `/v1/server` upgrade: accept the WS handshake,
then immediately close with `websocket.StatusCode(4409)` and reason
`"server-id already claimed"`. The flag clears after one use; subsequent
connects follow the normal HTTP-409 first-claim-wins path. Exists for
the `TestRelay_4409` e2e test that needs the production-shaped WS close
code rather than the HTTP-409 substitution.

### Binary-direct hello dispatch (#301)

When a binary sends an envelope **without** a `conn_id` in the outer
routing wrapper, `binaryRecvPump` decodes the raw bytes a second time as
`protocol.Envelope` and dispatches by `Type`. Today only
`protocol.TypeHello` is handled: capture under `s.mu` keyed by
`serverID` (read via `LastBinaryHello`), then reply with a
routing-wrapped `Envelope{Type:TypeHelloAck, InReplyTo:&helloID,
Payload:HelloAckPayload{ProtocolVersion:"v1", ServerID, ConnID:"-"}}`.
Other binary-direct types log at debug and drop — the dispatcher slice
takes over later. The routing-envelope path (frames with `conn_id`) is
unchanged.

## Routing rules (`docs/protocol-mobile.md` § Routing envelope)

```
phone → relay:    raw frame bytes
relay → binary:   {"conn_id": "c-N", "frame": <raw>, "token": "<phone-token>"?}

binary → relay:   {"conn_id": "c-N", "frame": <raw>, "close_code": <code>?}
relay → phone:    raw frame bytes  [then WS close(close_code) if non-zero]
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
- **Token injection (#308).** On the **first** phone→binary frame for
  each `conn_id`, the harness embeds the upgrade-time
  `x-pyrycode-token` value into `RoutingEnvelope.Token`; subsequent
  frames carry `Token: ""`. Captured via a per-conn `firstFrameSent`
  flag under `tokMu`.
- **`CloseCode` honor (#308).** On binary→phone frames with
  `env.CloseCode != 0`, the harness queues a `phoneSend{frame, closeCode}`
  tuple onto `pc.sendCh`; `phoneSendPump` writes `frame` first (so the
  phone observes the error envelope before the close) and then issues
  `pc.conn.Close(websocket.StatusCode(closeCode), "")`. Serialising the
  close through the same send pump that owns frame writes eliminates
  the race that a direct `conn.Close` at the `binaryRecvPump` call site
  would introduce against an in-flight write.

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
- **Production WS close codes** (`4404`/`4401`). Pre-upgrade HTTP
  status is the harness's substitution. (`4409` is supported as a
  one-shot opt-in via `RejectNextBinaryWith4409` — see above.)
- **Token-contents validation.** Any non-empty token accepted.
- **Binary-direct envelope dispatch beyond `hello`.** Other types log at
  debug and drop; the dispatcher slice consumes them.
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
