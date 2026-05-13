# `internal/relay` — binary side of the binary↔relay wire protocol

## What it is

The binary side of the binary↔relay wire protocol. Three surfaces:

1. **Outbound dial with server-id handshake** (`connection.go`, #248) — wraps `internal/transport.Client` (#247, the generic WSS primitive) with the v1 handshake state machine: builds the upgrade headers, sends `hello`, awaits `hello_ack` within 5 seconds, classifies WS close-code `4409` as terminal (server-id conflict), and exposes inbound frames as `protocol.RoutingEnvelope` values via `Frames()`. Knows nothing about per-envelope dispatch or supervisor lifecycle.
2. **Per-phone-conn first-frame token validation** (`auth.go`, #249) — a single pure function `AuthenticateFirstFrame` that returns a structured `AuthOutcome` (response envelope + close-or-keep signal) on top of `devices.Registry.Validate`. Carrier-agnostic with respect to how the token reached the binary. The relay-conn ticket that wires this into actual phone traffic is a future sibling.
3. **Per-envelope-type handlers** (`handlers/`, #250) — sibling sub-package (`internal/relay/handlers`) of pure functions, one per inbound phone-traffic envelope type. First inhabitant: `Handle` for `register_push_token`. Each handler is routing-envelope-in / routing-envelope-out, knows only payload semantics; the future dispatcher (in `internal/relay`) owns conn state, per-conn id allocation, and conn lifecycle. The sub-package depends on `internal/devices` + `internal/protocol` only — no import of `internal/relay`, keeping it cycle-free.

Wire-spec source-of-truth: `docs/protocol-mobile.md` § Authentication, § Connection lifecycle, § Worked example. When that document changes, this package changes.

## Surface

```go
package relay

type Config struct {
    ServerID      identity.ServerID // caller resolves via identity.LoadOrCreate
    RelayURL      string            // must be wss:// (ws:// accepted only when AllowInsecureScheme=true)
    BinaryVersion string
    Logger        *slog.Logger      // required

    // AllowInsecureScheme, when true, lets RelayURL use ws:// in addition
    // to wss://. Test-only seam for e2e suites pointing the daemon at an
    // httptest-hosted fakerelay over plaintext. Production callers leave
    // this false; cmd/pyry flips it only when the operator sets
    // PYRY_ALLOW_INSECURE_RELAY=1 (#301).
    AllowInsecureScheme bool
}

type Connection struct { /* opaque */ }

func Connect(ctx context.Context, cfg Config) (*Connection, error)

func (*Connection) Frames() <-chan protocol.RoutingEnvelope // closes on lifecycle exit
func (*Connection) Send(env protocol.RoutingEnvelope) error  // binary→relay outbound
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

- **`RelayURL` must be `wss://`** in production. Non-wss schemes are rejected as `ErrInvalidConfig` at `Connect` time. Server-id is sent in a request header; a `ws://` misconfiguration would disclose it in cleartext. Server-id is not a credential per `docs/protocol-mobile.md` § Security model Threat 2, but the cleartext-disclosure defense is cheap and structural.
- **`AllowInsecureScheme = true` is the explicit test-only opt-in** (#301). Relaxes the scheme check to accept `ws://` in addition to `wss://`. `cmd/pyry` flips it only when the operator sets `PYRY_ALLOW_INSECURE_RELAY=1` (env-gated; no flag, no config-file key). Production default stays `wss://`-only.
- **All four required `Config` fields must be set.** `ServerID` is caller-resolved via `internal/identity.LoadOrCreate` before `Connect` — the relay package never touches the on-disk store, keeping it free of pairing/storage concerns. `Logger` is required (nil → `ErrInvalidConfig`); structured slog only.

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
- `connectWithClient(ctx, cfg, client) *Connection` — unexported test seam that wraps a `*transport.Client` (typically wired to the `httptest` URL via a custom `dialFn`) and bypasses production's URL/scheme validation. Production callers use `Connect`. The newer `Config.AllowInsecureScheme` field (#301) is a *production-shaped* path through `Connect` for callers that just need `ws://` (e2e); two seams, two purposes — keep both.
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
- `TestConfig_Validation_TableDriven` — each missing required field; `ws://` / `http://` / unparseable schemes → `ErrInvalidConfig` (with `AllowInsecureScheme=false`).
- `TestConfig_AllowInsecureScheme` (#301) — pins that `ws://` passes `Connect` when `AllowInsecureScheme=true`; `Close` cancels the lifecycle before the bogus URL's async dial surfaces.

## Auth: per-conn first-frame validation (`auth.go`, #249)

Pure decision function. The binary owns the phone→relay→binary trust check per spec § Authentication; the relay forwards every phone frame to the binary in a `RoutingEnvelope`, and the binary calls `AuthenticateFirstFrame` on receipt of the first frame for a given `conn_id`.

```go
const (
    StatusUnauthorized websocket.StatusCode = 4401
    MsgInvalidToken                          = "device token not recognised; re-pair via pyry pair on the binary"
)

var ErrMalformedHelloFrame = errors.New("relay: malformed hello frame")

type AuthOutcome struct {
    Response  protocol.RoutingEnvelope
    CloseConn bool
}

func AuthenticateFirstFrame(
    env protocol.RoutingEnvelope,
    token string,
    reg *devices.Registry,
    serverID string,
    logger *slog.Logger,
) (AuthOutcome, error)
```

### Contract

| Input case | Outcome | `CloseConn` |
|---|---|---|
| `reg.Validate(token)` returns `true` | `Response` = routing-wrapped `Envelope{ID:1, Type:TypeHelloAck, InReplyTo:&helloID, Payload:HelloAckPayload{ProtocolVersion:"v1", ServerID, ConnID:env.ConnID}}` | `false` |
| `reg.Validate(token)` returns `false` (empty / never-paired / removed-after-pair) | `Response` = routing-wrapped `Envelope{ID:1, Type:TypeError, InReplyTo:&helloID, Payload:ErrorPayload{Code:CodeAuthInvalidToken, Message:MsgInvalidToken, Retryable:false}}` | `true` |
| `env.Frame` not JSON-decodable as `Envelope` | `AuthOutcome{}` + `ErrMalformedHelloFrame` | — |

The relay-conn caller writes `Response` first, then (if `CloseConn`) closes the phone WS with `StatusUnauthorized`. The outer envelope `ID` is fixed at `1` — the binary's first outbound frame on the phone's conn; the relay-conn layer allocates `2..N` for subsequent frames.

### Carrier-agnostic

The function never parses WS headers, never inspects a hello payload, and never reads `env.Frame`'s payload for a token field. It only reads `env.ConnID` (echoed back) and `env.Frame`'s outer envelope `id` (echoed into `in_reply_to`). The relay-conn ticket that wires this into phone traffic picks the wire mechanism — (a) extended routing envelope, (b) synthesized `connection_opened` control frame ahead of `hello`, or (c) amended `hello` payload — without touching this signature.

### Revoked-vs-invalid is one code in v1

`devices.Registry.Validate` is a two-state predicate (`(Device, bool)`); `Registry.Remove` deletes the row outright (no tombstone). A previously-paired device whose row was removed is indistinguishable from a never-paired device at the predicate boundary. Spec § Error codes line 535 already documents `auth.token_revoked` as having the same UX as `auth.invalid_token`, so emitting only `auth.invalid_token` is correct v1 behaviour. The `protocol.CodeAuthTokenRevoked` constant remains defined for a future tombstone primitive but is NOT emitted here.

### Logging discipline

| Event | Level | Fields |
|---|---|---|
| `relay: auth accept` | Info | `event=auth.accept`, `conn_id`, `device_name` |
| `relay: auth reject` | Warn | `event=auth.reject`, `conn_id`, `code=auth.invalid_token` |

Forbidden on both paths: `token`, the matched `TokenHash`, raw frame bytes. **No name on reject** — emitting "name X was not recognised" would let an attacker brute-forcing tokens enumerate paired-device names from binary logs.

### Concurrency

Stateless. No goroutines spawned. Safe for arbitrary concurrent invocations across distinct phone conns. The only shared state is `*devices.Registry`, whose mutex guards `Validate`'s read-and-bump critical section. `LastSeenAt` is bumped under `reg.mu` as part of `Validate` itself — the handler calls no further mutator. Persistence (`reg.Save`) is the supervisor's responsibility per `devices/auth.go`'s documented contract.

### Test surface

`internal/relay/auth_test.go` (six flat tests, stdlib only, reuses `testLogger(t)` from `connection_test.go`):

- `TestAuthenticateFirstFrame_ValidToken` — `hello_ack` shape + `LastSeenAt` bump.
- `TestAuthenticateFirstFrame_UnknownToken` — `auth.invalid_token` shape.
- `TestAuthenticateFirstFrame_RevokedTokenSameUX` — `reg.Remove` then call with the previously-paired token; byte-identical reject shape (locks spec line 535 equivalence).
- `TestAuthenticateFirstFrame_EmptyToken` — same reject shape (defends against a buggy relay-conn caller forwarding `""`).
- `TestAuthenticateFirstFrame_MalformedHelloFrame` — `errors.Is(err, ErrMalformedHelloFrame)`, zero-value outcome.
- `TestStatusUnauthorized_Value` — pins the `4401` constant.

`assertRejectOutcome` is the shared file-local helper for the three reject-shape tests; `makeHelloRouting(t, connID, helloID)` builds the input envelope; `pairedRegistry(t)` returns a one-device fixture plus the fixed initial `pastSeen` time for the bump-assertion.

## Handlers: per-envelope-type processors (`handlers/`, #250)

Sub-package `internal/relay/handlers`. Each handler is a pure function: routing envelope in, routing envelope out, plus side effects on the registries it is passed. The dispatcher (future, in `internal/relay`) owns conn state, per-conn id allocation, and conn lifecycle; handlers know only payload semantics.

First inhabitant: `register_push_token` (`register_push_token.go`).

```go
package handlers

var ErrMalformedFrame = errors.New("handlers: malformed register_push_token frame")

func Handle(
    routing      protocol.RoutingEnvelope,
    device       *devices.Device, // nil = unauth dispatcher bug
    reg          *devices.Registry,
    registryPath string,
    nextID       uint64,          // dispatcher-allocated per-conn id; auth used 1, this is ≥ 2
    logger       *slog.Logger,
) (protocol.RoutingEnvelope, error)
```

### Behavioural contract

| Input case | Response envelope | Side effect |
|---|---|---|
| `routing.Frame` or inner payload not JSON-decodable | `(RoutingEnvelope{}, ErrMalformedFrame)` — dispatcher owns the `protocol.malformed` response | none |
| `device == nil` (dispatcher routed an unauth conn here — bug) | `error`: `Code=auth.invalid_token`, `Retryable=false` | none |
| Payload `(Platform, Token, DeviceName)` equals snapshot `(Platform, PushToken, Name)` | `ack` | **none — does NOT call `UpdatePushRegistration`, does NOT call `Save` (the dedupe contract)** |
| `reg.UpdatePushRegistration` returns `false` (concurrent revoke between auth-accept and frame arrival) | `error`: `Code=auth.invalid_token`, `Retryable=false` (same UX as unauth) | none |
| `reg.Save` returns non-nil | `error`: `Code=server.binary_busy`, `Retryable=true`, `RetryAfterS=nil` | **in-memory IS mutated; disk is not** (phone retries; dedupe will succeed on retry) |
| Triple differs and Save succeeds | `ack` | in-memory + disk both updated |

### Dedupe is load-bearing

Spec § Phone background behaviour has the phone re-register on every WS connect (~100 bytes, self-heals registry drift). Without dedupe, every WS connect rewrites `devices.json` — flash wear and i/o churn for what's typically a no-op. The handler's "no write occurred" contract is structurally enforced: the dedupe branch returns the ack **before** calling `UpdatePushRegistration` or `Save`. Test pinpoints this via `errors.Is(os.Stat(path), fs.ErrNotExist)` with the file pre-state set to "never existed" — if dedupe were broken, Save would have created it.

### `Name` is part of the triple

The protocol's `device_name` makes the phone the source of truth for self-reported name (an iOS Settings rename should propagate). So the dedupe comparison is `(Platform, PushToken, Name)`, not just `(Platform, PushToken)`. The registry mutator (`devices.Registry.UpdatePushRegistration`) overwrites all three fields together — see [`features/devices-registry.md`](devices-registry.md).

### Save-failure leaves in-memory mutated

Documented post-condition, mirroring `Validate`'s `LastSeenAt` pattern: in-memory is the runtime source of truth; the next successful Save catches disk up. Test pins `reg.FindByTokenHash(...).PushToken == "new-fcm"` after the failed call.

### Sub-package isolation

`handlers` imports `internal/devices` and `internal/protocol`. **It does NOT import `internal/relay`.** This keeps the future dispatcher (in `internal/relay`) free to import `handlers` without a cycle. `auth.go` stays in `internal/relay` proper because it is the gate into the dispatcher, not a per-type handler — the dispatcher calls it directly during conn setup before any frame dispatch.

The `wrap(connID, inReplyTo, nextID, envType, payload)` helper inside `register_push_token.go` deliberately duplicates `relay.buildResponse` rather than importing across the sub-package boundary. Premature to lift into a shared `handlers/internal/wire` for a single handler; lift when a second handler lands.

### Logging discipline

| Event | Level | Fields |
|---|---|---|
| `relay: register_push_token write` | Info | `event=register_push_token.write`, `conn_id`, `device_name=payload.DeviceName`, `platform` |
| `relay: register_push_token dedupe` | Debug | `event=register_push_token.dedupe`, `conn_id`, `device_name=device.Name` |
| `relay: register_push_token save failed` | Warn | `event=register_push_token.save_failed`, `conn_id`, `device_name`, `err` |
| `relay: register_push_token device gone mid-conn` | Warn | `event=register_push_token.gone_mid_conn`, `conn_id`, `device_name` |
| `relay: register_push_token unauth` | Warn | `event=register_push_token.unauth`, `conn_id`, `code=auth.invalid_token` |

Push token (FCM/APNs registration id) is opaque infrastructure data, not a secret on par with the phone-side device token — but is still NOT logged (no operational signal worth the noise). Device-side token from auth is NEVER read or logged. Device name IS logged on every path that has one (write/dedupe/save-failed/gone-mid-conn) — the inverse of #249's reject-path discipline, because the handler runs post-auth: the caller has already cleared the auth gate, so there is nothing to enumerate. Unauth (`device == nil`) by definition has no name to log.

### Test surface

`internal/relay/handlers/register_push_token_test.go` — six flat tests, stdlib only, package `handlers`:

- `TestHandle_FirstTimeRegister_WritesAndAcks` — happy-path write + reload-and-verify.
- `TestHandle_ReregisterIdentical_NoWriteAndAcks` — dedupe spy: file deliberately not pre-Saved, asserts `errors.Is(os.Stat, fs.ErrNotExist)` after the call.
- `TestHandle_ReregisterChanged_WritesAndAcks` — pre-Save, change one field, content-based equality (not mtime — sidesteps CI filesystem mtime-resolution flakes).
- `TestHandle_SaveFailure_EmitsServerBinaryBusy` — regular file at `<tempdir>/blocker` makes `MkdirAll` fail on `<blocker>/devices.json`. Pins error code/retryable + in-memory-still-mutated post-condition.
- `TestHandle_UnauthenticatedConn_EmitsAuthInvalidTokenNoWrite` — `device=nil`; asserts `auth.invalid_token` shape + no file + unchanged registry.
- `TestHandle_MalformedFrame_ReturnsSentinel` — pins `errors.Is(err, ErrMalformedFrame)`.

`assertEnvelopeShape(t, resp, wantType)` is the file-local helper; returns the decoded envelope so error tests can pull the payload out for further assertions.

## Consumers and roadmap

- **Supervisor wiring** (#301): `cmd/pyry/main.go` + `cmd/pyry/relay.go` resolve the relay URL with precedence `-pyry-relay` > `PYRY_RELAY_URL` > `cfg.RelayURL` > `DefaultConfig`, load the server-id via `identity.LoadOrCreate(resolveServerIDPath(name))` (same on-disk file as `pyry pair`), call `relay.Connect`, and spawn one supervisor-owned goroutine that drains `Frames()` and reads `Wait()`. On `ErrServerIDConflict` the goroutine calls the shared `signal.NotifyContext` cancel, unwinding `pool.Run`; on any other terminal error it logs warn and exits without restart (transport-internal reconnect already absorbed all non-fatal closes); empty `relayURL` is the disabled-relay branch (info log, no goroutine). See [`codebase/301.md`](../codebase/301.md) for the full wiring + e2e harness extensions.
- **Outbound sending** (#307, landed): `(*Connection).Send(env protocol.RoutingEnvelope) error` marshals the routing envelope and forwards via `transport.Client.Send`. Caller wraps the inner `protocol.Envelope` in `RoutingEnvelope` (the dispatcher's `Conn.Send` does this from the inside). Returns `transport.ErrDisconnected` / `ErrNotConnected` / `ErrClosed` verbatim when the underlying conn is dropped — frames sent during a disconnected window are lost, which is consistent with v1 protocol semantics (reconnect re-runs `hello/hello_ack`, so per-conn state on the relay is implicitly the wrong frame of reference for retry). First consumer is `internal/dispatch` via the dispatcher's `Outbound()` forwarder in `cmd/pyry/relay.go`.
- **Relay-conn wiring** (future ticket): on receipt of the phone's first frame, extracts the token from the chosen carrier (extended routing envelope, synthesized `connection_opened`, or amended `hello`), calls `AuthenticateFirstFrame`, writes the returned `Response` back through the binary→relay leg, and (if `CloseConn`) closes the phone WS with `StatusUnauthorized`. Owns the `2..N` per-conn envelope-ID counter and caches the auth'd `*devices.Device` snapshot for forwarding to `handlers.Handle` and its siblings.
- **Per-message dispatch** (future ticket): consumes `Frames()`, branches on `Envelope.Type`, decodes the per-type payload (#256 catalog), and routes to the relevant handler in `internal/relay/handlers/` (e.g. `register_push_token`, future `send_message`, `list_conversations`, …).

## Dependencies

- `internal/transport` (#247) — generic WSS client. The additions #248 landed (`Config.FatalCloseCodes`, `Connected()`, `ErrDisconnected`, `ErrFatalClose`, `DropConn()`) are documented under [`features/transport-package.md`](transport-package.md).
- `internal/protocol` (#255 + #271) — `Envelope`, `RoutingEnvelope`, `TypeHello` / `TypeHelloAck` / `TypeError` constants, `CodeAuthInvalidToken`, `HelloServerPayload`, `HelloAckPayload`, `ErrorPayload`.
- `internal/devices` (#208 + #210) — `Registry.Validate(plain)` predicate (two-state, bumps `LastSeenAt` under `reg.mu`) consumed by `AuthenticateFirstFrame`. The plain→hash boundary lives in `devices.HashToken`; this package never hashes.
- `internal/identity` (#206 / #207) — `ServerID` newtype. `LoadOrCreate` is the caller's responsibility, not this package's.
- `github.com/coder/websocket` — only for the `StatusCode` type (typed-locally as `statusServerIDConflict` for 4409 and exported as `StatusUnauthorized` for 4401); consumers in `cmd/pyry` don't pull this transitively for headers alone.

## Out of scope

See [`codebase/248.md`](../codebase/248.md) § Out of scope for the deferred list.
