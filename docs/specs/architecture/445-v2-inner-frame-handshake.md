# 445 — `internal/relay`: v2 inner-frame handshake + token gating (no app dispatch yet)

## Files to read first

These are the load-bearing reads for turn 1. Each entry says **what to extract**, so the developer doesn't waste turns rediscovering context.

- `docs/protocol-mobile.md:160-200` — handshake flow diagram (`noise_init` → `noise_resp`), token-validation-gating MUST clauses.
- `docs/protocol-mobile.md:240-285` — Inner-frame discriminator table (`noise_init` / `noise_resp` / `noise_msg`) + JSON wire shapes for `InnerFrameV2`. `data` is `base64.StdEncoding` (padded). Decoded length cap is 65535 bytes (the Noise framework's per-message limit).
- `docs/protocol-mobile.md:185` — the "token-validation gating" MUST clause: noise_msg between `handshakeComplete` and token-OK must be rejected with sealed `auth.invalid_token` + `4401`; handler chain MUST NOT be reachable from `handshakeComplete`.
- `docs/protocol-mobile.md:430-440` — ordering rule: send `noise_resp` first, then AEAD-sealed `error`, then `4401` close.
- `docs/protocol-mobile.md:447-460` — WS close-code table (4401 / 4421 / 4426).
- `internal/relay/connection.go:163-210` — `Frames()` / `Send(env)` / `CloseConn(connID, code)` — the **only** surface the new manager consumes from `relay.Connection`. `Send` and `CloseConn` already exist; do **not** add new methods.
- `internal/relay/auth.go:14-44` — `StatusUnauthorized = 4401`, `MsgInvalidToken`, exported-close-code style. Mirror this style for the two new constants.
- `internal/relay/auth.go:85-162` — `AuthenticateFirstFrame` (v1) + `buildResponse`. Same shape (input → outcome), useful pattern. Do **not** import or call from v2 code — the wire-token mechanism is different in v2.
- `internal/noise/noise.go:51-122` — `Responder` API: `NewResponder(staticPriv) → *Responder`, `ReadInit(initMsg) → (earlyData, err)`, `WriteResp(earlyData) → (respMsg, send, recv, err)`. **CipherStates are NOT goroutine-safe; per-conn-id serialisation is the only correctness guarantee.**
- `internal/noise/noise.go:184-215` — `CipherState.Encrypt(plaintext)` / `Decrypt(ciphertext)` — **no AD parameter**. Empty-AD invariant is structural in the type system.
- `internal/devices/auth.go:32-46` — `Registry.Validate(plain) → (Device, bool)` is the token-validation predicate. Empty `plain` returns `(Device{}, false)` without computing HashToken; safe to call with an empty token field.
- `internal/protocol/envelope.go:23-67` — `Envelope` (v1 application envelope, **unchanged in v2** — rides inside `noise_msg` and inside `noise_init`/`noise_resp` early-data) + `RoutingEnvelope` (the binary↔relay leg). `RoutingEnvelope.CloseCode != 0` is already the wire mechanism for "ask the relay to close this phone WS".
- `internal/protocol/handshake.go:14-25` — `HelloClientPayload`. Adds one optional field in this slice (see Design § Wire types).
- `internal/protocol/codes.go:9-30` — `CodeAuthInvalidToken` / `CodeProtocolMalformed` / `CodeProtocolUnknownType`. Reuse these — do not introduce v2-specific code constants in this slice.
- `internal/dispatch/dispatch.go:38-100` — **read for mental model only** of "per-conn-id dispatch loop". Do **not** modify. The v1 dispatcher continues to own the wire when `cmd/pyry/relay.go` is run; v2 state machine is *not* wired into that daemon path in this slice.
- `internal/e2e/internal/fakerelay/fakerelay.go:142-148` — handler-registration site. Add one `mux.HandleFunc("/v2/server", s.handleBinary)` here (no handler-body changes — the routing-envelope wire is unchanged in v2).
- `internal/e2e/internal/fakephone/fakephone.go:60-80` — `Dial(ctx, baseURL, serverID, token, deviceName)` and its `/v1/client` path. The phone side in tests reuses `/v1/client` unchanged (the relay-side wire is shared; v2 only changes the *inner* frame). The `token` arg becomes irrelevant for v2 (the binary ignores `RoutingEnvelope.Token` per spec) but `fakerelay` requires a non-empty header — pass any non-empty string.
- `internal/e2e/internal/fakephone/fakephone.go:120-160` — `LastCloseStatus()` accessor + `websocket.CloseStatus` capture pattern. Tests assert on this for the 4401 / 4426 expectations.
- `internal/e2e/relay_auth_test.go` — full e2e shape (fakerelay + fakephone + close-code assertion). This slice's e2e tests follow the same skeleton **but do not call `StartIn`**: they wire `relay.Connect` + `V2SessionManager` inline (see Design § Tests for why).

## Context

Mobile Protocol v2 (#430) wraps every binary↔phone application frame in a Noise_IK AEAD channel. The outer routing envelope (`{conn_id, frame, close_code?}`) is unchanged from v1, but the inner `frame` is replaced with a `{v, type, data}` discriminator where `type ∈ {noise_init, noise_resp, noise_msg}`. This slice is the binary-side handshake responder: per-`conn_id` state machine that completes Noise_IK, validates the device-token piggybacked in the `noise_init` early-data, and refuses out-of-state inner frames at the WS-close layer.

This slice does **not** ship the `noise_msg`-in-`open`-state application dispatch (#446 owns that). All paths added here either complete the handshake, ask the relay to close the phone WS, or both. The `internal/relay/handlers/` v1 handler chain is **never reached** from any new code path — that is the load-bearing invariant the gating test pins.

The slice depends on #433 (`internal/noise` wrapper landed at f9dbbe4) for the Noise_IK responder + CipherStates and on the existing `internal/devices` registry for token validation. The slice intentionally does **not** depend on `internal/dispatch` (v1 dispatcher) — the two consumers of `Frames()` cannot coexist on a single connection at runtime; this slice's manager is wired only in tests today, and the production daemon's v2 wiring is a follow-up (likely #446 or later, when the open-state handler surface exists).

## Design

### Wire types — `internal/protocol/v2envelope.go` (new file)

```go
const (
    TypeNoiseInit = "noise_init"
    TypeNoiseResp = "noise_resp"
    TypeNoiseMsg  = "noise_msg"
)

const V2Version = 2

type InnerFrameV2 struct {
    Version int    `json:"v"`
    Type    string `json:"type"`
    Data    string `json:"data"` // base64.StdEncoding, padded
}
```

The `internal/protocol` package is the canonical home for wire-format types — v1's `Envelope` lives there and the `Type*` constants for v1's envelope types live in `codes.go`. v2 follows the same pattern. The package stays pure data (no helpers, no validation methods); shape-checking happens at the manager.

### Wire types — `internal/protocol/handshake.go` (single-line modification)

Append `Token string \`json:"token,omitempty"\`` to `HelloClientPayload`. v1 round-trip stays byte-identical (omitempty + absent value), so existing v1 fixtures and `relay_auth_test.go` are unaffected. The field is the in-band carrier of the device-token under v2 (the spec moves the token from the WS-header / routing envelope into AEAD-sealed early-data — see `docs/protocol-mobile.md:338`).

The v1 routing-envelope `RoutingEnvelope.Token` field is NOT removed in this slice — the v1 dispatcher still consumes it. v2's state machine deliberately ignores `RoutingEnvelope.Token` (defensive: spec line 600 says binary MUST ignore in v2).

### Close codes — `internal/relay/v2session.go` (new file, top of file)

```go
const StatusProtocolMismatch  websocket.StatusCode = 4421  // state-machine / discriminator violations
const StatusHandshakeFailure  websocket.StatusCode = 4426  // Noise_IK failure (no AEAD channel exists)
```

Two new exported constants, parallel in style to `StatusUnauthorized = 4401` in `auth.go`. `StatusUnauthorized` (4401) is reused unchanged; no new constant for it. The `relay` package owns this constant set because the WS-close layer is the relay client's concern; the dispatcher / state machine maps protocol-level intent to these wire codes.

### State machine — `internal/relay/v2session.go`

```go
type V2SessionState int

const (
    V2StateAwaitingInit V2SessionState = iota
    V2StateHandshakeComplete
    V2StateOpen
    V2StateClosed
)
```

```go
type V2Session struct {
    connID string
    state  V2SessionState
    resp   *noise.Responder
    send   *noise.CipherState   // populated at handshakeComplete
    recv   *noise.CipherState   // populated at handshakeComplete
}

// State returns the externally-observable state. Required so the gating
// test pins the handshakeComplete→open invariant deterministically.
func (s *V2Session) State() V2SessionState
```

`V2Session` has **no** lock. The package contract is "one goroutine per conn_id mutates the session"; in this slice that goroutine is the manager's single dispatch loop (see § Concurrency). `flynn/noise`'s `CipherState` carries a mutable 64-bit nonce counter; concurrent access would be UB. The serialisation point IS the lock.

The `State()` accessor is a plain field read. Today it is called only from the same goroutine that writes; tomorrow if a broadcast layer reads it cross-goroutine, the accessor needs `atomic.Int32` or a small mutex — but that refactor lives in #446 or later (note in Open Questions). For now, `State()` exists primarily as a structural anchor for the test invariant.

### Manager — `internal/relay/v2session.go`

```go
type V2SessionConfig struct {
    Frames     <-chan protocol.RoutingEnvelope        // required
    Outbound   func(protocol.RoutingEnvelope) error    // required; production wires conn.Send
    StaticPriv []byte                                  // required; 32 bytes; binary's Noise static priv
    Devices    *devices.Registry                       // required
    ServerID   string                                  // required; surfaced into hello_ack payload
    Logger     *slog.Logger                            // required
}

type V2SessionManager struct {
    cfg      V2SessionConfig
    sessions map[string]*V2Session // mutated only by Run()
}

func NewV2SessionManager(cfg V2SessionConfig) *V2SessionManager
func (m *V2SessionManager) Run(ctx context.Context) error
```

`Outbound` is a function (not a channel) for two reasons: (1) the production wiring forwards to `(*relay.Connection).Send`, which is already function-shaped (and returns `transport.ErrDisconnected` / `ErrNotConnected` that the manager should log-and-drop, not pause on); (2) tests can substitute an in-memory recorder without standing up a channel. The contract: a non-nil error from `Outbound` is treated as "frame lost, conn now unhealthy" — log at debug, continue. This mirrors v1's `cmd/pyry/relay.go:163` forwarder-error handling.

`NewV2SessionManager` validates required fields (panic on missing Frames/Logger like `dispatch.New`; return error on `len(StaticPriv) != noise.KeyLen`). `Run` blocks until `Frames` closes or `ctx.Done()`. On return, every per-conn `*V2Session` is released; no goroutines outlive `Run`.

### Concurrency

**Single dispatch goroutine.** `Run` is the only goroutine the manager owns. It reads `cfg.Frames`, looks up `m.sessions[env.ConnID]` (creating the session lazily on the first frame for a conn_id, in `V2StateAwaitingInit`), and processes the frame synchronously. `m.sessions` is mutated exclusively by `Run`; no lock.

This is intentionally **simpler than `internal/dispatch.Dispatcher`** (which spins one goroutine per conn_id). The v1 dispatcher's per-conn goroutines exist to absorb handler-side latency without head-of-line-blocking the demux; v2 in this slice runs no handlers — every operation is a synchronous Noise call (~100 µs) or a synchronous JSON decode (sub-microsecond). Single-goroutine is correct and obviously safe; per-conn fan-out is a #446 concern once `noise_msg` application dispatch lands.

The ticket's note "each phone-conn-id can run on the existing single dispatch loop per conn-id … No extra locking" is satisfied by this design (the loop IS the serialisation point; locking is unnecessary because there is exactly one writer of session state).

**Lifecycle cleanup.** When the state machine transitions a session to `V2StateClosed` (either failure path), the entry is deleted from `m.sessions` immediately so the next frame on that conn_id is dropped silently at the demux. Phone-initiated WS closes are NOT visible to the binary in v2 today (the spec defines no relay→binary "phone closed" forward signal); state entries for phone-disconnected conns linger until the binary↔relay leg recycles. Bounded by the relay's connSeq lifetime — not a real leak, but tracked in Open Questions.

### State-machine transition table

The table is the contract. Each cell is `{response, new state}`. "→close(N)" is shorthand for `Outbound(RoutingEnvelope{ConnID, CloseCode: N})` (relay-side close request; works whether or not Frame is also set, per `connection.go:185-210`'s contract).

| Inbound on conn_id              | `awaitingInit`                  | `handshakeComplete`             | `open`                           | `closed` |
|---------------------------------|---------------------------------|---------------------------------|----------------------------------|----------|
| `noise_init`                    | run handshake (see below)       | →close(**4421**), **closed**    | →close(**4421**), **closed**     | drop     |
| `noise_resp`                    | →close(**4421**), **closed**    | →close(**4421**), **closed**    | →close(**4421**), **closed**     | drop     |
| `noise_msg`, decrypts cleanly to **non-`hello`** envelope | →close(**4421**), **closed** (no AEAD yet) | AEAD-seal `error{auth.invalid_token}` + →close(**4401**), **closed** | drop (no handler dispatch in this slice) | drop |
| `noise_msg`, decrypt fails / state lacks AEAD | →close(**4421**), **closed** | →close(**4421**), **closed** | drop (no handler) | drop |
| Unknown `type`, bad `v`, malformed JSON | →close(**4421**), **closed** | →close(**4421**), **closed** | →close(**4421**), **closed** | drop |

**`noise_init` handshake in `awaitingInit`** — the AC-load-bearing happy and failure paths:

1. JSON-decode `RoutingEnvelope.Frame` as `InnerFrameV2`. Validate `Version == V2Version` and `Type == TypeNoiseInit`; otherwise →close(4421).
2. `base64.StdEncoding.DecodeString(Data)`; size-cap at 65535 bytes; otherwise →close(4421).
3. Lazy-construct `noise.Responder` (one per conn, kept on `*V2Session`) if not already constructed.
4. `Responder.ReadInit(rawBytes)` → `earlyData, err`. **On err: →close(4426), closed.** (Spec § Failure modes: MAC fail / missing `server_static_pubkey` / malformed IK message 1 all surface here.)
5. Decode `earlyData` as `protocol.Envelope`. **On err OR on `env.Type != TypeHello`: take the handshakeComplete-non-hello path** (see step 7-failure-branch, but the AEAD channel does not yet exist, so → close(4421) instead, since there's no key to seal an error envelope under).

   Actually: re-read step 5 carefully. The spec mandates the noise_resp-first ordering ONLY for token-validation failure (where we *have* a hello envelope). If the early-data is not a hello envelope at all, the gating-table row "noise_msg in handshakeComplete decrypts to non-hello" applies — but this is the `noise_init` path, not `noise_msg`. We have not yet called `WriteResp` (CipherStates do not exist yet at step 5). Decision: a malformed-or-non-hello early-data is treated as a handshake-layer protocol violation → close(4421), state = closed. The AEAD-seal-error path is reserved for the case where we have CipherStates (i.e., after step 7).
6. Decode `env.Payload` as `protocol.HelloClientPayload`. On err: →close(4421).
7. Build `hello_ack` envelope (`protocol.HelloAckPayload{ProtocolVersion: "v2", ServerID: cfg.ServerID, ConnID: env.ConnID}`), marshal as `protocol.Envelope{ID:1, Type: TypeHelloAck, InReplyTo: &env.ID, ...}`, marshal to JSON.
8. `Responder.WriteResp(helloAckJSON)` → `respMsg, send, recv, err`. **On err: →close(4426), closed.** (Realistically unreachable under correct flynn/noise; included for completeness.)
9. Persist `send`, `recv` on the session. **State → `V2StateHandshakeComplete`**.
10. Emit `noise_resp`: marshal `InnerFrameV2{Version:2, Type:TypeNoiseResp, Data: base64.StdEncoding.EncodeToString(respMsg)}`, wrap as `RoutingEnvelope{ConnID, Frame}`, call `Outbound`.
11. **Token validation:** `cfg.Devices.Validate(hello.Token)`. On hit: **State → `V2StateOpen`**, return. On miss: take the **AEAD-error-then-close** branch:
    a. Build `error` envelope (`protocol.ErrorPayload{Code: CodeAuthInvalidToken, Message: MsgInvalidToken, Retryable: false}`), wrap as `protocol.Envelope{ID:2, Type: TypeError, ...}`, marshal to JSON.
    b. `send.Encrypt(envJSON)` → `aeadBytes, err`. On err: log warn, →close(4401) without the AEAD frame (best-effort).
    c. Marshal `InnerFrameV2{Version:2, Type: TypeNoiseMsg, Data: base64.StdEncoding.EncodeToString(aeadBytes)}`.
    d. Emit one `RoutingEnvelope{ConnID, Frame: <noise_msg JSON>, CloseCode: 4401}`. **The AEAD-sealed error envelope and the close request are emitted as a SINGLE routing envelope.** This is the existing v1 atomicity pattern (`cmd/pyry/relay.go:38` + `dispatch.go:498-512`'s `Response.CloseCode = outcome.Code`): one Send call, the phone observes the error frame and the close in order.
    e. **State → `V2StateClosed`**; delete the session from `m.sessions`.

Note that steps 9-10 set state=handshakeComplete BEFORE token validation in step 11. The handshakeComplete state is *observably distinct from open* even though the same goroutine performs both transitions back-to-back. This satisfies the AC: "The `handshakeComplete` substate is observably distinct from `open` even though both can be set inside the same `noise_init` handler — the externally-controlled `state` field exists so the gating test is deterministic and so any future refactor that splits the dispatch loop cannot silently remove the invariant."

### `noise_msg` in `V2StateHandshakeComplete`

This row of the table is the "gating" invariant. Today the natural inbound-frame flow never reaches this cell — state transitions through handshakeComplete atomically inside the noise_init handler. The cell exists for:

- **Future-proofing.** If #446 or later introduces deferred token validation (e.g. registry lookup over the network), `handshakeComplete` becomes observable to incoming frames between handshake completion and validation completion. This table row defines the behaviour for that future world today, structurally pinning the invariant.
- **Same-package unit-test verifiability.** A gating-invariant test in `internal/relay/v2session_test.go` (same-package, can directly assign `s.state = V2StateHandshakeComplete` and inject a hand-rolled CipherState pair) drives this row deterministically (see § Tests).

### Code-shape sketch

A single decoding helper handles the two AEAD-decrypt-then-Envelope-validate paths (early-data in `noise_init` and inbound `noise_msg`). Keeping the decoder in one place is the reason the gating invariant is structurally enforced — there is only one decode path to be wrong.

```go
// decryptAndDecodeNoiseMsg AEAD-decrypts the noise_msg payload and decodes
// the plaintext as a protocol.Envelope. Returns the envelope and nil on
// success; returns a sentinel-classified error otherwise. CipherState
// nonce is consumed even on decrypt success but envelope-decode failure
// — flynn/noise's counter cannot be rewound, and the next-frame
// behaviour assumes monotonic increment.
func decryptAndDecodeNoiseMsg(recv *noise.CipherState, data string) (protocol.Envelope, error)
```

Detailed AEAD/error-shape: handled by a `buildAndSealError(s *V2Session, code, message string, inReplyTo uint64) ([]byte, error)` helper that produces the `noise_msg`-wrapped AEAD-sealed error envelope JSON. Used only on the failure branch in step 11 above.

### Test seam — `internal/relay/v2session.go`

Tests live in `internal/relay/v2session_test.go` (same-package), so no exported test seam is required for state manipulation. The same-package test directly assigns `s.state`, `s.send`, `s.recv` to drive the gating row. No `// for tests only` exports.

For the e2e tests in `internal/e2e/`, the manager is constructed via `NewV2SessionManager` with a real `*relay.Connection`'s `Send` (or a recording stub) as `Outbound`. No special seam needed there either — the manager's surface is the natural test surface.

### Wiring — `cmd/pyry/relay.go`

**Not modified in this slice.** The production daemon continues to wire `internal/dispatch.Dispatcher` against the v1 inner-frame contract. The v2 state machine is reachable only through test wiring today. Production cutover is a follow-up — likely #446 (which lands `noise_msg`-in-open-state handler dispatch) or a sibling — and may involve `cmd/pyry/relay.go` selecting v1-vs-v2 based on URL path or a config flag; that decision is explicitly out of scope here. The ticket's "operator chooses the URL path" stance applies to the relay deployment, not to the binary's internal wiring decision.

### `fakerelay` — `/v2/server` route (1-line modification)

Add at `internal/e2e/internal/fakerelay/fakerelay.go:144`:

```go
mux.HandleFunc("/v2/server", s.handleBinary)
```

The relay-side wire (binary↔relay routing envelope) is unchanged in v2, so the handler is reused unmodified. The handler's binary-direct `hello` → `hello_ack` reply (`handleBinaryDirect`, line 440-491) still uses `ProtocolVersion: "v1"` in the synthesized hello_ack — that's the relay's response to the *binary's* hello (binary↔relay leg, which is unchanged from v1 per spec § Binary → relay). Do NOT touch handleBinaryDirect.

Phone-side `/v2/client` is NOT registered. Tests connect the phone on `/v1/client` (the routing wire is shared); the v2 inner-frame shape lives inside `RoutingEnvelope.Frame`, which fakerelay forwards opaquely. Phone-side endpoint introduction is the relay team's work and is explicitly out of scope (per the ticket § Out of scope).

## Error handling

| Failure mode                                     | Close code | AEAD-sealed error envelope sent? | Order on the wire |
|--------------------------------------------------|-----------|----------------------------------|-------------------|
| Token validation fails (`Devices.Validate` miss) | 4401      | yes (`auth.invalid_token`)       | error envelope + close in ONE routing envelope (atomic) |
| `Responder.ReadInit` rejects                     | 4426      | no (no key)                      | close only |
| `Responder.WriteResp` errors (defensive)         | 4426      | no                               | close only |
| Early-data decode fail / not `hello`             | 4421      | no                               | close only |
| `noise_msg` while `awaitingInit`                 | 4421      | no                               | close only |
| `noise_msg` decrypt fails in `handshakeComplete` | 4421      | no                               | close only |
| `noise_init` while `handshakeComplete` / `open`  | 4421      | no                               | close only |
| `noise_resp` from phone in any state             | 4421      | no                               | close only |
| Unknown `type`, bad `v`, malformed inner-frame JSON | 4421   | no                               | close only |
| Inner-frame `data` exceeds 65535 bytes decoded   | 4421      | no                               | close only |
| `Outbound` returns transport.ErrDisconnected     | n/a       | n/a                              | log at debug, drop frame, state stays unchanged; the relay leg's reconnect handles the rest |

**The atomicity claim** (token-failure path emits the error envelope and the close code in ONE `Outbound` call) is what guarantees the spec's ordering MUST: phone observes the error envelope *before* the WS close. This relies on `connection.go:177-183`'s `Send` writing one WS frame per call — once that WS frame is acknowledged, the relay processes the `CloseCode` and emits the close frame to the phone. Two-call sequencing (one Send + one CloseConn) would race the relay's two output paths.

## Testing strategy

Tests split between same-package unit (state-machine invariants, fast, no WS) and e2e (real WS via fakerelay).

### `internal/relay/v2session_test.go` (same-package unit)

Stdlib `testing` only; table-driven where useful. Each test constructs a `V2SessionManager` with an in-memory `Outbound` recorder (a slice appended under a test-private mutex; goroutine-safe by Run-then-stop pattern) and a real `internal/devices.Registry` built inline.

- **`TestV2Session_HappyPath`** — paired-device `hello` in early-data → state advances to `V2StateOpen`; `noise_resp` envelope on Outbound carries hello_ack; CipherStates non-nil; no close-code emitted.
- **`TestV2Session_BadToken_AEADErrorThen4401`** — paired-device-empty hello-with-unknown-token → exactly one Outbound envelope with `CloseCode == 4401`, frame is a `noise_msg`-wrapped AEAD-sealed error envelope, error envelope decrypts (via initiator-side CipherState) and has `code == auth.invalid_token`. State = closed.
- **`TestV2Session_IKReject_4426`** — `noise_init` carrying random bytes (no real IK message) → exactly one Outbound envelope with `CloseCode == 4426`, no Frame body required. State = closed.
- **`TestV2Session_NoiseInitAfterOpen_4421`** — drive happy-path to open, then feed a second `noise_init` → Outbound has the original hello_ack + a 4421 close envelope. State = closed.
- **`TestV2Session_Gating_NoiseMsgInHandshakeComplete_4401`** — directly assign `s.state = V2StateHandshakeComplete`, `s.send/recv = <CipherStates from a real adjacent handshake>`, feed a noise_msg whose plaintext is a non-hello envelope (e.g. `send_message`). Assert: exactly one Outbound envelope with `CloseCode == 4401`, frame is AEAD-sealed `error{auth.invalid_token}`. **Structurally proves the "handler chain unreachable from handshakeComplete" invariant** — there is no v2→handler edge in the code, and this test is the regression guard for any future refactor that might add one.
- **`TestV2Session_OutOfStateRejections`** — table-driven over the remaining (state, frame-type) cells: malformed JSON / unknown `Type` / bad `v` → 4421 in each state.

The unit tests do NOT use fakerelay (visibility-fenced behind `internal/e2e/internal/`). They drive the manager directly with in-memory channels.

### `internal/e2e/relay_v2_handshake_test.go` (`//go:build e2e`)

The full-WS tests. Each test:
1. Constructs fakerelay (now with both `/v1/server` and `/v2/server`).
2. Generates a fresh static keypair via `crypto/ecdh.X25519().GenerateKey(rand.Reader)`.
3. Builds an empty `devices.Registry` in-memory (or pre-populated for the happy-path test).
4. Calls `relay.Connect(ctx, relay.Config{RelayURL: fr.URL()+"/v2/server", AllowInsecureScheme: true, ...})`.
5. Constructs `relay.NewV2SessionManager(...)` with `Outbound: conn.Send`; spawns one goroutine running `mgr.Run(ctx)`.
6. Waits for the binary↔relay hello/hello_ack via `fr.LastBinaryHello(serverID)`.
7. Dials a phone via `fakephone.Dial(ctx, fr.URL(), serverID, "ignored-token", "phone-X")` (the token arg is irrelevant under v2 — fakerelay still requires a non-empty header).
8. Constructs an `internal/noise.Initiator` with a random phone-side priv and the binary's pub-key derived from its priv via `ecdh.X25519().NewPrivateKey(priv).PublicKey().Bytes()`.
9. Builds the early-data hello (containing the device-token in `HelloClientPayload.Token`) and calls `init.WriteInit(helloJSON)` → raw noise_init bytes.
10. Marshals `InnerFrameV2{Version:2, Type:TypeNoiseInit, Data: base64.StdEncoding.EncodeToString(noiseInitBytes)}` and sends via `phone.Send(envelope-wrapping-the-v2-frame-with-payload)`.
11. Asserts.

These tests do NOT spawn the pyry daemon — they wire `relay.Connect` + `V2SessionManager` inline in the test, because the daemon's `cmd/pyry/relay.go` still wires the v1 dispatcher. The pattern is "in-process binary" e2e, identical-in-spirit to but lighter-weight than the existing `StartIn`-spawning tests.

Scenarios covered (subset overlapping the unit tests intentionally — same scenarios validated through the real WS substrate):

- Happy path: paired device → phone observes a noise_resp frame, decrypts hello_ack, then no further traffic (the binary stays in open state, no handlers, nothing else happens).
- Bad token: unpaired device → phone reads the noise_msg frame, AEAD-decrypts as an `auth.invalid_token` error envelope, then `Read` errors with `websocket.CloseStatus → 4401` (via `fakephone.LastCloseStatus()`).
- IK reject: phone sends an invalid noise_init (e.g. wrong static pubkey for the responder) → phone's next Read errors with close code 4426. No prior frame from binary.

The gating-invariant test is unit-test-shape only; the e2e suite covers the natural inbound flows.

## Log policy (security-load-bearing)

Mirrors v1's `internal/relay/auth.go` posture. The implementation MUST adhere; code-review checks each rule against the diff.

- **MUST NOT log at any level:** `HelloClientPayload.Token`, `cfg.StaticPriv`, raw `RoutingEnvelope.Frame` bytes, AEAD ciphertext bytes (the `Data` field of any `noise_msg`), plaintext envelope payload bytes (post-AEAD-decrypt), base64-encoded forms of any of the above. The same MUST applies to `slog` fields, error wrapping (`fmt.Errorf("foo: %w", err)` where `err` accidentally carries the secret), and `panic` strings.
- **MUST log (operator-actionable) on ACCEPT:** event class (`v2.handshake.accept`), `conn_id`, `device_name` from the matched `*devices.Device` (matches v1 `auth.go:108-111`). Plain low-cardinality string fields only.
- **MUST log (operator-actionable) on REJECT:** event class (`v2.handshake.reject.invalid_token` / `v2.handshake.reject.ik_failure` / `v2.state.reject`), `conn_id`, `close_code`. **MUST NOT include `device_name`** even when the early-data carried one — anti-enumeration of paired-device names from binary logs (matches v1 `auth.go:129-132`).
- **Type-level forbid for `StaticPriv`:** the package doc-comment on `V2SessionConfig.StaticPriv` declares the field as "MUST NOT be logged, wrapped into an error message, or emitted on any wire surface". `internal/keys` and `internal/noise` already document the same contract for the same bytes; this spec extends it to the manager's holding site.

## Out-of-scope (deferred to follow-ups)

- **Per-phone-conn 10s handshake timeout** (`docs/protocol-mobile.md:366`, § Connection lifecycle: "expect a noise_init as the first frame within 10 seconds; otherwise close with 4421"). Enforcing this requires a relay→binary "phone connected" signal so the binary can start the timer; no such signal exists in the v2 wire today. Tracked for a future protocol amendment + binary slice. The state machine in this slice processes frames synchronously, so a phone that sends nothing causes no resource use; the bound is "relay-side WS-idle close", which the relay already enforces.
- **`V2Session` cleanup on phone-initiated WS close.** Same root cause — no relay→binary forwarded signal. See Open Question 1.
- **Production wiring of `V2SessionManager` into `cmd/pyry/relay.go`.** Open Question 2.

## Open questions

1. **Phone-WS-close cleanup.** Current relay→binary wire has no "phone disconnected" envelope; the binary cannot detect phone-initiated WS closes for a given conn_id. State entries linger in `m.sessions`. Bounded by per-binary-leg lifetime, but tracked as a deferred concern. Likely resolved when the spec adds a `conn_closed` synthetic envelope or when #446 / #435 introduce a registry trim path.
2. **Production wiring decision.** `cmd/pyry/relay.go` continues to use the v1 dispatcher. The cutover to v2 (and whether to keep v1 in any form for backward compat in operator-controlled deploys, or to do the hard cutover per spec § Hard cutover) is the next slice's call. The relevant inputs: #446 needs handler dispatch hooked up, and the spec's pre-flight gate (#436) gates the production flip.
3. **CipherState concurrent-read.** `V2Session.State()` is safe today because no cross-goroutine reads exist. Once #446 introduces broadcast or handler-side goroutines, the accessor will need `atomic.Int32` (small) — call out at #446 design time, do not pre-emptively refactor in this slice.

## Scope self-check

Production source files modified or created (excluding tests, `*.md`, the spec):

1. `internal/protocol/v2envelope.go` — new (~25 LOC)
2. `internal/protocol/handshake.go` — modified (+1 line, `Token` field on `HelloClientPayload`)
3. `internal/relay/v2session.go` — new (~150 LOC including comments)
4. `internal/e2e/internal/fakerelay/fakerelay.go` — modified (+1 line, route registration)

Count: **4 production source files**. Below the 5-file size:s ceiling. New exported symbols: 2 close-code constants + `V2Version` const + 3 `Type*` constants + `InnerFrameV2` struct + `V2SessionState` + 4 state-enum constants + `V2Session` + `V2SessionConfig` + `V2SessionManager` + 2 constructors/methods (`NewV2SessionManager`, `Run`) + accessor (`State()`). The constants don't count toward "new exported types" (they're consts, not types); the new exported types are `InnerFrameV2`, `V2SessionState`, `V2Session`, `V2SessionConfig`, `V2SessionManager` = **5 exported types**, at the size:s ceiling but not over. Acceptance criteria: 5. Within boundary.

Edit fan-out: 0 cascading consumer call-sites (all new types; the `HelloClientPayload.Token` field is additive omitempty — no consumer needs updating).

Size: **S** confirmed.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — single explicit boundary at `decryptAndDecodeNoiseMsg`'s AEAD-decrypt-then-decode pair; downstream state-machine code holds parsed `protocol.Envelope` only. The `RoutingEnvelope.Frame` byte boundary is the JSON-decode-to-`InnerFrameV2` site; nothing past that point handles raw inbound bytes.
- [Tokens] SHOULD FIX → FIXED (inline) — spec now enumerates log policy explicitly. Token, static private key, payload bytes, AEAD ciphertext, base64 forms thereof are all MUST-NOT-log at any level. The ACCEPT/REJECT logging asymmetry (`device_name` on ACCEPT only) mirrors v1 `internal/relay/auth.go:108-132`'s anti-enumeration posture.
- [File operations] N/A — slice does no file I/O. Static-key load is `internal/keys`'s domain (#438 + #439); device registry load is the daemon-startup path in `cmd/pyry/relay.go` (unchanged here).
- [Subprocess / external execution] N/A — none.
- [Cryptographic primitives] No findings — Noise_IK + ChaChaPoly + BLAKE2s are inherited from `internal/noise` (#433, security-reviewed at that ticket); empty-AD invariant is structurally enforced at the type system; nonce-counter management is `flynn/noise`'s 64-bit monotonic; token comparison uses `crypto/subtle.ConstantTimeCompare` inside `devices.Validate` (#208's contract); per-`conn_id` lazy `noise.Responder` construction prevents cross-session key reuse.
- [Network & I/O] No findings — `InnerFrameV2.Data` decoded-byte cap at 65535 (Noise framework limit, spec § Wire shapes) is enforced before any `Responder.ReadInit` call; outer `RoutingEnvelope` byte cap is inherited from `internal/transport`'s 1 MiB `SetReadLimit` ceiling. Per-phone-conn 10s handshake timeout is OUT OF SCOPE (no binary-side phone-connect signal exists in v2 wire) — explicitly documented under § Out-of-scope.
- [Error messages, logs, telemetry] SHOULD FIX → FIXED (inline) — see [Tokens]. The AEAD-sealed error envelope on the 4401 path emits a static `MsgInvalidToken` string and a fixed `CodeAuthInvalidToken` code; no attacker-influenced content is echoed. Close-only paths (4421 / 4426) emit no envelope at all — no leakage surface.
- [Concurrency] No findings — single dispatch goroutine owns all `*V2Session` mutation; `flynn/noise` CipherStates' non-goroutine-safety is satisfied structurally. Future cross-goroutine `State()` reads (#446 broadcast layer or similar) tracked in Open Question 3; today the race-detector would catch any premature attempt.
- [Threat model alignment] No findings — slice implements the Noise_IK responder half of Threat #3 (relay operator MITM, § Security model). A malicious relay cannot forge `noise_init` content because it lacks the binary's static private key; the binary detects forgery at `ReadInit`'s MAC step and closes with 4426. Token theft (Threat #4) is mitigated by AEAD-sealed in-band carriage of `hello.Token`; the relay never sees plaintext. Device-name enumeration (sub-threat of Threat #4) is mitigated by the ACCEPT-only `device_name` log rule.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-16

