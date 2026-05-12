# `internal/relay` — outbound dial with server-id handshake

## What it is

The binary side of the binary↔relay wire protocol. Wraps `internal/transport.Client` (#247, the generic WSS primitive) with the v1 handshake state machine: builds the upgrade headers, sends `hello`, awaits `hello_ack` within 5 seconds, classifies WS close-code `4409` as terminal (server-id conflict), and exposes inbound frames as `protocol.RoutingEnvelope` values via `Frames()`. Knows nothing about per-envelope dispatch, token validation, or supervisor lifecycle — those concerns layer above (#250 and the supervisor-wiring follow-up).

Wire-spec source-of-truth: `docs/protocol-mobile.md` § Authentication, § Connection lifecycle, § Worked example. When that document changes, this package changes.

## Surface

```go
package relay

type Config struct {
    ServerID      identity.ServerID // caller resolves via identity.LoadOrCreate
    RelayURL      string            // must be wss://
    BinaryVersion string
    Logger        *slog.Logger      // required
}

type Connection struct { /* opaque */ }

func Connect(ctx context.Context, cfg Config) (*Connection, error)

func (*Connection) Frames() <-chan protocol.RoutingEnvelope // closes on lifecycle exit
func (*Connection) Wait() error                              // blocks until exit
func (*Connection) Close() error                             // idempotent

var (
    ErrServerIDConflict = errors.New("relay: server-id conflict (close 4409)")
    ErrInvalidConfig    = errors.New("relay: invalid config")
)
```

Run pattern:

```go
conn, err := relay.Connect(ctx, relay.Config{
    ServerID:      sid,                      // identity.LoadOrCreate
    RelayURL:      cfg.RelayURL,             // from internal/config
    BinaryVersion: version,                  // build-time ldflag
    Logger:        log,
})
if err != nil {
    return fmt.Errorf("relay.Connect: %w", err) // ErrInvalidConfig
}
defer conn.Close()

go func() {
    for env := range conn.Frames() {
        dispatch(env) // future ticket
    }
}()

if err := conn.Wait(); err != nil {
    if errors.Is(err, relay.ErrServerIDConflict) {
        // another pyry holds this server-id; operator escalation, exit non-zero
        return err
    }
    // ctx.Err() or wrapped transport error
    return err
}
```

## Headers (locked at wire-spec)

Built inside `Connect` from `Config`; the caller does NOT supply them:

| Header | Value |
|---|---|
| `x-pyrycode-server` | `string(cfg.ServerID)` (UUIDv4 from `internal/identity`) |
| `x-pyrycode-version` | `cfg.BinaryVersion` |
| `user-agent` | `pyry/<cfg.BinaryVersion>` |

Source: `docs/protocol-mobile.md` § Authentication. The relay accepts on first-claim-wins; if the server-id is already claimed it closes with status `4409` and the package surfaces `ErrServerIDConflict` from `Wait()`.

## Handshake (one-shot per WS conn)

```
Connect()              → goroutine spawns
   │
   └── on every fresh transport conn (signalled by transport.Client.Connected()):
         │
         ├── send Envelope{ID: 1, Type: "hello", TS: now, Payload: HelloServerPayload{
         │        Role:             "server",
         │        ServerID:         cfg.ServerID,
         │        BinaryVersion:    cfg.BinaryVersion,
         │        ProtocolVersions: ["v1"],
         │   }}
         │
         ├── Receive(deadline = 5s)
         │     │
         │     ├── timeout / wrong type / malformed JSON → log WARN, DropConn(), reconnect
         │     ├── ErrDisconnected (conn dropped mid-handshake) → reconnect
         │     └── RoutingEnvelope wrapping Envelope{Type: "hello_ack"} → READY
         │
         └── forwardFrames(): Receive → unmarshal RoutingEnvelope → c.frames
                                │
                                └── any err → return; outer select catches next Connected → re-handshake
```

The 5-second `hello_ack` deadline is a wire-spec constant. `hello_ack` frames are ALWAYS wrapped in `RoutingEnvelope` (`conn_id: "-"`) per `docs/protocol-mobile.md` § Worked example — the decoder unwraps before checking `Envelope.Type`.

## Reconnect semantics

| Cause | Behaviour |
|---|---|
| Transport drop (`1011`, `1006`, network error) | Inherits `internal/transport`'s backoff (1s/2s/4s/8s/16s/30s cap ±20% jitter, reset after ≥60s uptime). On each fresh conn, re-runs the handshake. Frames flow on the SAME `Frames()` channel before and after reconnect — consumers see a contiguous in-order stream. |
| WS close `4409` (server-id conflict) | `Wait()` returns `ErrServerIDConflict`. NO reconnect. Operator escalation: another pyry holds the same server-id, or a stale connection on the relay side has not yet been reaped (relay's 30-second grace window). |
| `hello_ack` timeout / wrong type / malformed JSON | Logged WARN; `client.DropConn()` force-closes the live conn; transport reconnects via backoff; handshake retries. Persistent failure → backoff saturates at 30s — acceptable degraded behaviour. |
| Malformed JSON on a post-handshake frame | Logged WARN; frame dropped at the trust boundary; loop continues. Single bad frame does NOT tear the conn. |
| `ctx` cancelled / `Close()` called | Clean shutdown. `Frames()` closes; `Wait()` returns `ctx.Err()` or `nil`. |

## Error model

| Method | Returns |
|---|---|
| `Connect` | `nil` on success after sync validation; `ErrInvalidConfig` (wrapped, names the missing field or wrong scheme) on bad config. Never blocks — the `run` goroutine handles the dial. |
| `Frames` | `(env, ok)`. `ok=false` when the lifecycle exits. |
| `Wait` | `ErrServerIDConflict` (fatal 4409), `ctx.Err()` (graceful shutdown), `nil` (Close called), or a wrapped `transport` error (unexpected halt). |
| `Close` | Always `nil`. Idempotent. |

Sentinels are distinguished via `errors.Is`. `ErrServerIDConflict`'s string contains no dynamic content — no token / server-id leakage via error messages.

## Configuration constraints

- **`RelayURL` must be `wss://`.** Non-wss schemes are rejected as `ErrInvalidConfig` at `Connect` time. Server-id is sent in a request header; a `ws://` misconfiguration would disclose it in cleartext. Server-id is not a credential per `docs/protocol-mobile.md` § Security model Threat 2, but the cleartext-disclosure defense is cheap and structural.
- **All four `Config` fields are required.** `ServerID` is caller-resolved via `internal/identity.LoadOrCreate` before `Connect` — the relay package never touches the on-disk store, keeping it free of pairing/storage concerns. `Logger` is required (nil → `ErrInvalidConfig`); structured slog only.

## Logging discipline

| Event | Level | Fields |
|---|---|---|
| `relay: handshake complete` | Info | `server_id` |
| `relay: handshake failed; recycling conn` | Warn | `err` |
| `relay: malformed routing envelope; dropping` | Warn | `err` |
| `relay: forwardFrames exiting` | Debug | `err` |

Forbidden everywhere: `token`, `payload`, raw `frame` bytes, full `Headers` map (would leak `x-pyrycode-server` on every line; `server_id` is the operator-actionable subset). The transport's existing lifecycle logs (`transport: connected`, `transport: dial failed, backing off`, `transport: disconnected`) cover the conn lifecycle.

## Edge cases and gotchas

- **`Frames()` channel is unbuffered.** A slow consumer applies back-pressure all the way to the relay's send buffer. The dispatcher (future ticket) must keep `Frames()` drained continuously.
- **Frames are delivered post-handshake only.** The `hello_ack` is consumed inside `handshake()` and never reaches `Frames()`.
- **Reconnects are invisible to `Frames()` consumers.** The same channel persists across reconnects; a fresh `hello`/`hello_ack` handshake runs first, then frames resume on the new conn.
- **`Connect` is sync-validate, async-run.** It returns immediately after `Config` validation. The connection is NOT yet Ready; observe `Frames()` to consume post-handshake frames, or call `Wait()` to block on terminal classification.
- **Caller is responsible for `Close()`** during shutdown to release resources; `ctx` cancellation also drains the lifecycle.
- **Race between `Connected` signal and conn drop:** if the conn drops between the relay observing `Connected` and calling `Send(hello)`, `Send` returns `ErrNotConnected`, `handshake` returns an error, `DropConn` is a no-op, `run` loops and catches the next `Connected` or `transportErrCh`. No stuck state.

## Test surface

`internal/relay/connection_test.go` (~700 LOC, stdlib + `coder/websocket`):

- `newTestRelay(t)` — `httptest.NewServer` + `websocket.Accept` upgrader with hooks for: pre-emptive close with status, skip-ack, drop-after-hello, drop-after-ack, send-N-frames-after-ack, header introspection.
- `testLogger(t)` — discarding `slog.Logger`.
- `connectWithClient(ctx, cfg, client) *Connection` — unexported test seam that wraps a `*transport.Client` (typically wired to the `httptest` URL via a custom `dialFn`) and bypasses production's `wss://` validation. Production callers use `Connect`.
- `handshakeTimeout` is a package-level `var` so tests substitute a 200ms value via `t.Cleanup`. Same idiom as `internal/transport`'s test-substituted cadence fields.

Pinned behaviour:

- `TestConnect_HappyPath` — `hello` → `hello_ack` → Ready, full envelope shape on the wire matches the spec.
- `TestHeaders_Set` — relay introspects `x-pyrycode-server`, `x-pyrycode-version`, `user-agent: pyry/<version>`.
- `TestHandshake_AckTimeout` (NON-PARALLEL — mutates package-level `handshakeTimeout`) — 200ms substitute, handshake fails, `DropConn` fires, reconnect, success on attempt 2.
- `TestHandshake_UnexpectedFrame` — relay sends a non-ack frame first; same recycle.
- `TestServerIDConflict_FatalNoReconnect` — 4409 → `Wait()` returns `ErrServerIDConflict`; relay's `connCount` stays at 1.
- `TestTransportDropDuringHandshake` — relay reads `hello` then `CloseNow`; reconnect; success.
- `TestTransportDropPostHandshake_ReHandshakes` — proves post-reconnect frames flow on the SAME `Frames()` channel (pins the `ErrDisconnected` wedge fix in the transport additions; without it, `forwardFrames` would wedge in the previous-conn `Receive` and never let `run` observe the next `Connected`).
- `TestFrames_DeliversPostHandshakeInOrder` — three frames delivered in arrival order.
- `TestClose_ShutsDownCleanly` — `Close()` drains `Frames()` and `Wait()` returns; goroutines exit.
- `TestContextCancel_ShutsDownCleanly` — `cancel(ctx)` drains the lifecycle.
- `TestConfig_Validation_TableDriven` — each missing required field; `ws://` / `http://` / unparseable schemes → `ErrInvalidConfig`.

## Consumers and roadmap

- **Supervisor wiring** (next ticket): `cmd/pyry/main.go` constructs the `relay.Config`, calls `Connect`, fans `Frames()` to a dispatcher, watches `Wait()` for `ErrServerIDConflict` and exits non-zero so launchd/systemd decides whether to restart.
- **Outbound sending** (future ticket): adds `(*Connection).Send(env protocol.Envelope, connID string)` wrapping the envelope in `RoutingEnvelope` before handing to the transport — required for `register_push_token` (#275 payload, #250 handler) and binary-initiated frames (conversation updates, message echoes).
- **Per-message dispatch** (future ticket): consumes `Frames()`, branches on `Envelope.Type`, decodes the per-type payload (#256 catalog), and routes to the relevant subsystem (auth, sessions, conversations).

## Dependencies

- `internal/transport` (#247) — generic WSS client. The additions this ticket landed (`Config.FatalCloseCodes`, `Connected()`, `ErrDisconnected`, `ErrFatalClose`, `DropConn()`) are documented under [`features/transport-package.md`](transport-package.md).
- `internal/protocol` (#255 + #271) — `Envelope`, `RoutingEnvelope`, `TypeHello` / `TypeHelloAck` constants, `HelloServerPayload`.
- `internal/identity` (#206 / #207) — `ServerID` newtype. `LoadOrCreate` is the caller's responsibility, not this package's.
- `github.com/coder/websocket` — only for the `StatusCode` type (typed-locally as `statusServerIDConflict`); consumers in `cmd/pyry` don't pull this transitively for headers alone.

## Out of scope

See [`codebase/248.md`](../codebase/248.md) § Out of scope for the deferred list.
