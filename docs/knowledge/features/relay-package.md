# `internal/relay` — binary side of the binary↔relay wire protocol

## What it is

The binary side of the binary↔relay wire protocol. Three surfaces:

1. **Outbound dial, established on WS upgrade** (`connection.go`, #248; binary↔relay handshake retired #582) — wraps `internal/transport.Client` (#247, the generic WSS primitive): builds the upgrade headers, treats the conn as established the moment the WS upgrade fires (the relay is content-blind — it registers the binary's server-id from the `x-pyrycode-server` header and sends no `hello_ack`), classifies WS close-code `4409` as terminal (server-id conflict), and exposes inbound frames as `protocol.RoutingEnvelope` values via `Frames()`. No relay-originated `hello`/`hello_ack` ceremony on this leg. Knows nothing about per-envelope dispatch or supervisor lifecycle.
2. **Per-phone-conn first-frame token validation** (`auth.go`, #249) — a single pure function `AuthenticateFirstFrame` that returns a structured `AuthOutcome` (response envelope + close-or-keep signal) on top of `devices.Registry.Validate`. Carrier-agnostic with respect to how the token reached the binary. The relay-conn ticket that wires this into actual phone traffic is a future sibling.
3. **Per-envelope-type handlers** (`handlers/`, #250, signature rewrite #319) — sibling sub-package (`internal/relay/handlers`) of `dispatch.Handler` closures, one per inbound phone-traffic envelope type. Inhabitants today: `ListConversations` (#303) for `list_conversations`, `RegisterPushToken` (#250 logic, #319 signature) for `register_push_token`, and `SendMessage` (#322 + Activate ordering #396) for `send_message`. Each is a factory: `func(deps...) dispatch.Handler` returning a closure with `func(ctx, *dispatch.Conn, protocol.Envelope) error`. The handler reads the authenticated device via `c.Auth()` (#318), allocates response ids via `c.NextID()` (called inside `c.Reply`), and never stamps `id`/`in_reply_to`/`ts` itself. The sub-package imports `internal/conversations` + `internal/devices` + `internal/dispatch` + `internal/protocol` only — no import of `internal/relay` or `internal/sessions` — keeping it cycle-free; the `send_message` handler depends on its own `handlers.TurnWriter` interface (`Activate` + `WriteUserTurn`) that `*sessions.Session` satisfies structurally. `SendMessage` runs `Activate` with a 30s budget before `WriteUserTurn` so an idle-evicted bootstrap session lazily respawns claude on the next inbound message instead of dropping it through the supervisor's `ptmx == nil` discard branch (#396, [features/idle-eviction.md](idle-eviction.md#relay-routed-send_message-396)); a busted respawn surfaces as `protocol.CodeServerBinaryOffline` with `Retryable=true`.

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
func (*Connection) CloseConn(connID string, code uint16) error // #308; close one phone conn
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

## Connection establishment (on WS upgrade)

The binary↔relay leg is **content-blind** — there is no application-layer `hello`/`hello_ack` ceremony on it (retired #582). The relay registers the binary's server-id from the `x-pyrycode-server` request header and claims the slot on WS upgrade; under v2 a `hello_ack` would be AEAD-sealed application data the relay holds no key for. So the conn is established the moment the upgrade fires, and `run()` goes straight to forwarding.

```
Connect()              → goroutine spawns
   │
   └── on every fresh transport conn (signalled by transport.Client.Connected()):
         │
         ├── log Info "relay: conn established" (server_id)
         │
         └── forwardFrames(): Receive → unmarshal RoutingEnvelope → c.frames
                                │
                                └── any err → return; outer select catches next Connected → re-enter forwardFrames
```

WS close-code `4409` (server-id conflict) is classified terminal independently of any frame exchange — it rides the transport's `FatalCloseCodes: [4409]` and surfaces as `ErrServerIDConflict` from `Wait()` (see Reconnect semantics).

> The *phone↔binary* `hello`/`hello_ack` is a different leg and survives — it is carried E2E-encrypted as Noise_IK early-data, relay-blind, and validated by the binary's `AuthenticateFirstFrame` (see § Auth below). Only the binary↔relay leg ceremony was retired.

## Reconnect semantics

| Cause | Behaviour |
|---|---|
| Transport drop (`1011`, `1006`, network error) | Inherits `internal/transport`'s backoff (1s/2s/4s/8s/16s/30s cap ±20% jitter, reset after ≥60s uptime). On each fresh conn, re-enters forwarding directly (no handshake). Frames flow on the SAME `Frames()` channel before and after reconnect — consumers see a contiguous in-order stream. |
| WS close `4409` (server-id conflict) | `Wait()` returns `ErrServerIDConflict`. NO reconnect. Operator escalation: another pyry holds the same server-id, or a stale connection on the relay side has not yet been reaped (relay's 30-second grace window). |
| Malformed JSON on an inbound frame | Logged WARN; frame dropped at the trust boundary; loop continues. Single bad frame does NOT tear the conn. |
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
- **`RelayURL` is treated as a *base* — the daemon appends `/v1/server` when it carries no meaningful path** (#631). The package owns the binary's endpoint knowledge: an unexported `resolveDialURL(raw, allowInsecure)` is the single home for relay-URL handling (parse → scheme-check → path-append), called inside `Connect` before the dial. If `u.Path == "" || u.Path == "/"` it sets `u.Path = "/v1/server"`; any operator-supplied path (`/v1/server`, `/v2/server`, `/custom`) passes through unchanged. `u.String()` reconstruction preserves host/port/query/userinfo (`wss://h/?x=1` → `wss://h/v1/server?x=1`). This mirrors the phone's `/v1/client` convention (`fakephone.Dial` does `baseURL+"/v1/client"`), so the single base `relay_url` in `~/.pyry/config.json` serves **both** `pyry pair` (uses it as a base for the QR) and the daemon (dials it) with **no `PYRY_RELAY_URL` override** required. The shipped default `DefaultConfig().RelayURL = "wss://relay.pyrycode.dev"` is itself a base URL — before #631 it silently 404-looped the daemon forever (the relay serves only `/v1/server`, `/v1/client`, `/healthz`). The `/v1/server` literal lives only here; `transport` stays protocol-agnostic. See [codebase/631.md](../codebase/631.md).
- **All four required `Config` fields must be set.** `ServerID` is caller-resolved via `internal/identity.LoadOrCreate` before `Connect` — the relay package never touches the on-disk store, keeping it free of pairing/storage concerns. `Logger` is required (nil → `ErrInvalidConfig`); structured slog only.

## Logging discipline

| Event | Level | Fields |
|---|---|---|
| `relay: conn established` | Info | `server_id` |
| `relay: malformed routing envelope; dropping` | Warn | `err` |
| `relay: forwardFrames exiting` | Debug | `err` |

Forbidden everywhere: `token`, `payload`, raw `frame` bytes, full `Headers` map (would leak `x-pyrycode-server` on every line; `server_id` is the operator-actionable subset). The transport's existing lifecycle logs (`transport: connected`, `transport: dial failed, backing off`, `transport: disconnected`) cover the conn lifecycle.

## Edge cases and gotchas

- **`Frames()` channel is unbuffered.** A slow consumer applies back-pressure all the way to the relay's send buffer. The dispatcher (future ticket) must keep `Frames()` drained continuously.
- **Reconnects are invisible to `Frames()` consumers.** The same channel persists across reconnects; frames resume on the new conn directly (no handshake).
- **`Connect` is sync-validate, async-run.** It returns immediately after `Config` validation. The connection is NOT yet established; observe `Frames()` to consume inbound frames, or call `Wait()` to block on terminal classification.
- **Caller is responsible for `Close()`** during shutdown to release resources; `ctx` cancellation also drains the lifecycle.
- **Race between `Connected` signal and conn drop:** if the conn drops right after the relay observes `Connected`, `forwardFrames`'s first `Receive` returns `ErrDisconnected`, `run` loops and catches the next `Connected` or `transportErrCh`. No stuck state.

## Test surface

`internal/relay/connection_test.go` (~700 LOC, stdlib + `coder/websocket`):

- `newTestRelay(t)` — `httptest.NewServer` + `websocket.Accept` upgrader for a **content-blind forwarder**: it registers each conn on upgrade (`ConnCount()` / `connectedCh` / header capture) and pumps `outboundFrames` to the binary; it never reads a hello or sends a `hello_ack`. Behaviors: `behaviorForward` (default — register + forward), `behaviorCloseImmediately4409` (close 4409 on accept), `behaviorDropOnConnect` (`CloseNow` right after upgrade → transport reconnects).
- `testLogger(t)` — discarding `slog.Logger`.
- `connectWithClient(ctx, cfg, client) *Connection` — unexported test seam that wraps a `*transport.Client` (typically wired to the `httptest` URL via a custom `dialFn`) and bypasses production's URL/scheme validation. Production callers use `Connect`. The newer `Config.AllowInsecureScheme` field (#301) is a *production-shaped* path through `Connect` for callers that just need `ws://` (e2e); two seams, two purposes — keep both.
- `waitConnCount(t, relay, n, timeout)` — readiness helper polling `relay.ConnCount() >= n`; replaced the `HelloEnv(0)` poll loops when the binary-sent hello was retired (#582).

Pinned behaviour:

- `TestConnect_ReachesForwardingNoAck` (#582) — against a relay that never sends a `hello_ack`, a frame pushed via `relay.outboundFrames` arrives on `c.Frames()` AND `ConnCount == 1` (no recycle); pins "reaches frame-forwarding, no ack, no recycle".
- `TestHeaders_Set` — relay introspects `x-pyrycode-server`, `x-pyrycode-version`, `user-agent: pyry/<version>`.
- `TestServerIDConflict_FatalNoReconnect` — 4409 → `Wait()` returns `ErrServerIDConflict`; relay's `connCount` stays at 1.
- `TestTransportDropOnConnect_Reconnects` (#582) — relay `CloseNow`s right after upgrade; transport reconnects (`ConnCount >= 2`).
- `TestTransportDropPostConnect_Reconnects` (#582) — proves post-reconnect frames flow on the SAME `Frames()` channel (pins the `ErrDisconnected` wedge fix in the transport additions; without it, `forwardFrames` would wedge in the previous-conn `Receive` and never let `run` observe the next `Connected`).
- `TestFrames_AfterConnect_InOrder` — three frames delivered in arrival order.
- `TestClose_ShutsDownCleanly` — `Close()` drains `Frames()` and `Wait()` returns; goroutines exit.
- `TestContextCancel_ShutsDownCleanly` — `cancel(ctx)` drains the lifecycle.
- `TestConfig_Validation_TableDriven` — each missing required field; `ws://` / `http://` / unparseable schemes → `ErrInvalidConfig` (with `AllowInsecureScheme=false`).
- `TestResolveDialURL` (#631) — table pinning the relay-URL handling: base / `"/"` → `/v1/server` appended; `/v1/server` / `/v2/server` / `/custom` passthrough (AC#4); query preserved on both append and passthrough; `ws://`/`http://` → wraps `ErrInvalidConfig` (message contains `"wss"`); `ws://` with `allowInsecure=true` → accepted + appended; `"://broken"` → wraps `ErrInvalidConfig` (message contains `"RelayURL parse"`).
- `TestConfig_AllowInsecureScheme` (#301) — pins that `ws://` passes `Connect` when `AllowInsecureScheme=true`; `Close` cancels the lifecycle before the bogus URL's async dial surfaces.
- `TestCloseConn_WireShape` (#308) — `CloseConn("c-7", 4401)` produces one outbound frame whose JSON has `conn_id=="c-7"`, `close_code==4401`, and no `frame` key.
- `TestCloseConn_PropagatesNotConnected` (#308) — pre-Connect state returns `transport.ErrNotConnected` verbatim.

## Close-conn surface (`CloseConn`, #308)

`(*Connection).CloseConn(connID string, code uint16) error` asks the relay to close the named phone conn with the given WS close code. Builds a close-only `RoutingEnvelope{ConnID, CloseCode}` (no `Frame`), marshals, and forwards via `transport.Client.Send`. Returns `transport.ErrNotConnected` / `ErrDisconnected` / `ErrClosed` verbatim — fire-and-forget at this layer; the per-conn close ack is implicit (no further inbound frames will arrive for `connID`).

The dispatcher's auth-reject path (#308) does NOT call `CloseConn`. Instead it publishes a single `RoutingEnvelope` with `Frame=<error>` AND `CloseCode=4401` onto `dispatch.Outbound()`; the existing forwarder's one `conn.Send(env)` is the atomic wire op. `CloseConn` is the surface reserved for direct callers that want close-without-payload (none today; the idle/inactivity sweep hinted at in #307's Open Questions is the anticipated future consumer).

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
    Device    *devices.Device // #318; accept-only — nil on reject and on the malformed early return
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

The outer envelope `ID` is fixed at `1` — the binary's first outbound frame on the phone's conn. #308 wires the caller: `internal/dispatch`'s `FirstFrameGate` (`cmd/pyry/relay.go:authGate`) invokes `AuthenticateFirstFrame`, maps `outcome.CloseConn` to WS close code `4401`, and publishes one routing envelope onto the dispatcher's outbound channel carrying **both** `Response.Frame` AND `CloseCode=4401` — so the error envelope and the close are atomic on the wire (no race between `Send` and a separate `CloseConn` call). On accept the dispatcher advances the per-conn id counter so the next handler reply gets `ID=2`.

#318 extends the accept path: `AuthOutcome.Device` is set to a pointer to a local `snapshot := device` copy of the value returned by `reg.Validate`. The gate closure in `cmd/pyry/relay.go` forwards this onto `dispatch.FirstFrameOutcome.Device`, which the dispatcher then stores into `*dispatch.Conn`'s per-conn auth slot via the unexported `setAuth` seam — strictly on the accept-and-continue branch. Downstream verb handlers read the matched device through `c.Auth()` and do NOT re-call `reg.Validate` on the second-and-later frames (the token only rides the first frame per `conn_id`). Reject and malformed paths leave `AuthOutcome.Device` nil structurally; see [codebase/318.md](../codebase/318.md).

### Carrier-agnostic

The function never parses WS headers, never inspects a hello payload, and never reads `env.Frame`'s payload for a token field. It only reads `env.ConnID` (echoed back) and `env.Frame`'s outer envelope `id` (echoed into `in_reply_to`). #308 picked option (a) from the three deferred mechanisms: the token rides `RoutingEnvelope.Token` populated by the relay from the phone's `x-pyrycode-token` header on the first frame per `conn_id`. Options (b) synthesized `connection_opened` control frame and (c) amended `hello` payload remain available for future protocol revisions without touching this signature.

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
#319 rewrote the original #250 pure handler against `dispatch.Handler`
and registered it in `cmd/pyry/relay.go` alongside
`list_conversations`. The pre-#319 `Handle` signature (routing-envelope
in/out, self-stamping `id`/`ts`/`in_reply_to`, sentinel
`ErrMalformedFrame`) is gone.

```go
package handlers

func RegisterPushToken(reg *devices.Registry, registryPath string,
                       logger *slog.Logger) dispatch.Handler
```

The returned closure has signature
`func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error`.
`reg`, `registryPath`, and `logger` are captured — no globals; the
third argument diverges from `ListConversations`'s two-arg shape
because this handler logs on every branch. The authenticated device is
read via `c.Auth()` (populated by the gate per #318); the dispatcher
allocates the response id via `c.NextID()` (called inside `c.Reply`)
and stamps `in_reply_to` from `env.ID` and `ts` from `time.Now().UTC()`.
The handler never touches those fields itself.

### Behavioural contract

| Input case | Response envelope | Side effect |
|---|---|---|
| `c.Auth() == nil` (dispatcher routed an unauth conn here — bug; defence-in-depth) | `error`: `Code=auth.invalid_token`, `Retryable=false` | none |
| `env.Payload` not JSON-decodable as `RegisterPushTokenPayload` | `error`: `Code=protocol.malformed`, `Retryable=false`, static message (decode-error text NOT echoed) | none |
| Payload `(Platform, Token, DeviceName)` equals snapshot `(Platform, PushToken, Name)` | `ack` | **none — does NOT call `UpdatePushRegistration`, does NOT call `Save` (the dedupe contract)** |
| `reg.UpdatePushRegistration` returns `false` (concurrent revoke between auth-accept and frame arrival) | `error`: `Code=auth.invalid_token`, `Retryable=false` (same UX as unauth) | none |
| `reg.Save` returns non-nil | `error`: `Code=server.binary_busy`, `Retryable=true`, `RetryAfterS=nil` | **in-memory IS mutated; disk is not** (phone retries; dedupe will succeed on retry) |
| Triple differs and Save succeeds | `ack` | in-memory + disk both updated |

The outer-frame malformed branch is gone from the contract — the
dispatcher's `handleOne` decodes `env protocol.Envelope` from the
routing frame before invoking the handler and replies
`protocol.malformed` upstream. Only the **inner-payload** decode
remains the handler's responsibility.

### Dedupe is load-bearing

Spec § Phone background behaviour has the phone re-register on every WS connect (~100 bytes, self-heals registry drift). Without dedupe, every WS connect rewrites `devices.json` — flash wear and i/o churn for what's typically a no-op. The handler's "no write occurred" contract is structurally enforced: the dedupe branch returns the ack **before** calling `UpdatePushRegistration` or `Save`. Test pinpoints this via `errors.Is(os.Stat(path), fs.ErrNotExist)` with the file pre-state set to "never existed" — if dedupe were broken, Save would have created it.

### `Name` is part of the triple

The protocol's `device_name` makes the phone the source of truth for self-reported name (an iOS Settings rename should propagate). So the dedupe comparison is `(Platform, PushToken, Name)`, not just `(Platform, PushToken)`. The registry mutator (`devices.Registry.UpdatePushRegistration`) overwrites all three fields together — see [`features/devices-registry.md`](devices-registry.md).

### Save-failure leaves in-memory mutated

Documented post-condition, mirroring `Validate`'s `LastSeenAt` pattern: in-memory is the runtime source of truth; the next successful Save catches disk up. Test pins `reg.FindByTokenHash(...).PushToken == "new-fcm"` after the failed call.

### Sub-package isolation

`handlers` imports `internal/devices`, `internal/dispatch`, and `internal/protocol`. **It does NOT import `internal/relay`.** The new `internal/dispatch` edge (added #319) is cycle-free because `internal/dispatch`'s only handler-direction dependency is the `Handler` function type, which `handlers` consumes structurally. `auth.go` stays in `internal/relay` proper because it is the gate into the dispatcher, not a per-type handler — the dispatcher calls it directly during conn setup before any frame dispatch.

The `wrap(connID, inReplyTo, nextID, envType, payload)` file-local helper from #250 is gone — `c.Reply(ctx, env, type, payloadJSON)` is the central stamping path now (#319). Two file-local helpers (`replyAck`, `replyError`) marshal the payload and call `c.Reply`.

### Logging discipline

| Event | Level | Fields |
|---|---|---|
| `relay: register_push_token write` | Info | `event=register_push_token.write`, `conn_id`, `device_name=payload.DeviceName`, `platform` |
| `relay: register_push_token dedupe` | Debug | `event=register_push_token.dedupe`, `conn_id`, `device_name=device.Name` |
| `relay: register_push_token save failed` | Warn | `event=register_push_token.save_failed`, `conn_id`, `device_name`, `err` |
| `relay: register_push_token device gone mid-conn` | Warn | `event=register_push_token.gone_mid_conn`, `conn_id`, `device_name` |
| `relay: register_push_token unauth` | Warn | `event=register_push_token.unauth`, `conn_id`, `code=auth.invalid_token` |

Push token (FCM/APNs registration id) is opaque infrastructure data, not a secret on par with the phone-side device token — but is still NOT logged (no operational signal worth the noise). Device-side token from auth is NEVER read or logged. Device name IS logged on every path that has one (write/dedupe/save-failed/gone-mid-conn) — the inverse of #249's reject-path discipline, because the handler runs post-auth: the caller has already cleared the auth gate, so there is nothing to enumerate. Unauth (`device == nil`) by definition has no name to log.

### Test surface (rewritten #319)

`internal/relay/handlers/register_push_token_test.go` — seven flat tests, stdlib only, package `handlers`, all under `-race`. The fixture uses `dispatch.NewTestConn(testConnID, out, dev)` to build a `*dispatch.Conn` with a buffered outbound channel the test drains; `_ = c.NextID()` is called once after construction so the first handler reply observes `id=2` (mirroring the gate's `hello_ack=1` accounting).

- `TestRegisterPushToken_FirstTimeRegister_WritesAndAcks` — happy-path write + reload via `devices.Load` and assert the triple.
- `TestRegisterPushToken_ReregisterIdentical_NoWriteAndAcks` — dedupe spy: file deliberately not pre-Saved, asserts `errors.Is(os.Stat, fs.ErrNotExist)` after the call.
- `TestRegisterPushToken_ReregisterChanged_WritesAndAcks` — pre-Save, change one field, content-equality post-call (sidesteps CI mtime-resolution flakes).
- `TestRegisterPushToken_GoneMidConn_EmitsAuthInvalidToken` — `dev`'s TokenHash absent from the registry forces `UpdatePushRegistration` to return false; asserts `auth.invalid_token`, `Retryable=false`.
- `TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy` — regular file at `<tempdir>/blocker` makes `MkdirAll` fail on `<blocker>/devices.json`. Pins error code/retryable + in-memory-still-mutated post-condition.
- `TestRegisterPushToken_UnauthenticatedConn_EmitsAuthInvalidTokenNoWrite` — `auth=nil`; asserts `auth.invalid_token` shape + no file + unchanged registry.
- `TestRegisterPushToken_MalformedPayload_EmitsProtocolMalformed` — `env.Payload = []byte("not-json")`; asserts `Code=protocol.malformed`, `Retryable=false`. New case; replaces the deleted `TestHandle_MalformedFrame_ReturnsSentinel` (the dispatcher owns the malformed-frame path now).

`newTestConn(t, dev)` and `makeRequest(t, payload)` are the file-local helpers; the `assertEnvelopeShape` / `equalRouting` / `makeRegisterRouting` helpers from #250 are gone with the sentinel.

#### e2e (`internal/e2e/register_push_token_test.go`, new #319)

Build tag `e2e`. `TestRelay_RegisterPushToken_AckAndPersists` pairs a device via `RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")`, decodes the plaintext token via `decodePairPayload`, boots the daemon against a `fakerelay` with `PYRY_ALLOW_INSECURE_RELAY=1`, dials a `fakephone` with the paired token, sends `hello` → expects `hello_ack` (`*InReplyTo == 1`), sends `register_push_token` with `ID: 2` → expects `ack` (`*InReplyTo == 2`, `env.ID >= 2` — strictly-greater leaves room for future dispatcher-side replies between `hello_ack` and this ack), then reloads `~/.pyry/test/devices.json` and asserts the `(Platform, PushToken, Name)` triple is persisted.

## Consumers and roadmap

- **Supervisor wiring** (#301): `cmd/pyry/main.go` + `cmd/pyry/relay.go` resolve the relay URL with precedence `-pyry-relay` > `PYRY_RELAY_URL` > `cfg.RelayURL` > `DefaultConfig`, load the server-id via `identity.LoadOrCreate(resolveServerIDPath(name))` (same on-disk file as `pyry pair`), call `relay.Connect`, and spawn one supervisor-owned goroutine that drains `Frames()` and reads `Wait()`. On `ErrServerIDConflict` the goroutine calls the shared `signal.NotifyContext` cancel, unwinding `pool.Run`; on any other terminal error it logs warn and exits without restart (transport-internal reconnect already absorbed all non-fatal closes); empty `relayURL` is the disabled-relay branch (info log, no goroutine). See [`codebase/301.md`](../codebase/301.md) for the full wiring + e2e harness extensions.
- **Outbound sending** (#307, landed): `(*Connection).Send(env protocol.RoutingEnvelope) error` marshals the routing envelope and forwards via `transport.Client.Send`. Caller wraps the inner `protocol.Envelope` in `RoutingEnvelope` (the dispatcher's `Conn.Send` does this from the inside). Returns `transport.ErrDisconnected` / `ErrNotConnected` / `ErrClosed` verbatim when the underlying conn is dropped — frames sent during a disconnected window are lost, which is consistent with the protocol's connection-lifecycle semantics (reconnect re-establishes the conn on WS upgrade, so per-conn state on the relay is implicitly the wrong frame of reference for retry). First consumer is `internal/dispatch` via the dispatcher's `Outbound()` forwarder in `cmd/pyry/relay.go`.
- **Relay-conn wiring** (#308 + #318, landed): the dispatcher's `FirstFrameGate` extracts the token from `RoutingEnvelope.Token` (relay-populated from the phone's `x-pyrycode-token` header on the first frame per `conn_id`), calls `AuthenticateFirstFrame`, and on reject publishes one routing envelope carrying both `Response.Frame` AND `CloseCode=4401` — the WS close is atomic with the error envelope. The dispatcher owns the `2..N` per-conn envelope-ID counter (#308) and stores the auth'd `*devices.Device` snapshot into `*dispatch.Conn`'s per-conn slot via the unexported `setAuth` seam (#318); downstream handlers read it via `c.Auth()` rather than re-validating.
- **Per-message dispatch** (`internal/dispatch`, #307+#308+#318): consumes `Frames()`, runs the optional `FirstFrame` gate, decodes the inner `protocol.Envelope` and branches on `Type`, routing to the registered handler in `internal/relay/handlers/`. `list_conversations` (#303), `register_push_token` (#319), and `send_message` (#322) are wired in `cmd/pyry/relay.go`'s `startRelay`; the rest of the #256 catalog (the assistant-turn-delivery half of `send_message` plus `backfill_since` / `create_conversation` / `promote_conversation` / `delete_conversation`) is deferred.

## Dependencies

- `internal/transport` (#247) — generic WSS client. The additions #248 landed (`Config.FatalCloseCodes`, `Connected()`, `ErrDisconnected`, `ErrFatalClose`, `DropConn()`) are documented under [`features/transport-package.md`](transport-package.md).
- `internal/protocol` (#255 + #271) — `Envelope`, `RoutingEnvelope`, `TypeHello` / `TypeHelloAck` / `TypeError` constants, `CodeAuthInvalidToken`, `HelloAckPayload`, `ErrorPayload`. (`HelloServerPayload` is no longer referenced by this package since #582 retired the binary↔relay handshake — it now has no consumer anywhere in the repo.)
- `internal/devices` (#208 + #210) — `Registry.Validate(plain)` predicate (two-state, bumps `LastSeenAt` under `reg.mu`) consumed by `AuthenticateFirstFrame`. The plain→hash boundary lives in `devices.HashToken`; this package never hashes.
- `internal/identity` (#206 / #207) — `ServerID` newtype. `LoadOrCreate` is the caller's responsibility, not this package's.
- `github.com/coder/websocket` — only for the `StatusCode` type (typed-locally as `statusServerIDConflict` for 4409 and exported as `StatusUnauthorized` for 4401); consumers in `cmd/pyry` don't pull this transitively for headers alone.

## Out of scope

See [`codebase/248.md`](../codebase/248.md) § Out of scope for the deferred list.
