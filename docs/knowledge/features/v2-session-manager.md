# `internal/relay` V2 session manager — Noise_IK handshake + token gating

The fourth surface of `internal/relay` (alongside the v1 outbound dial in `connection.go`, the v1 first-frame auth gate in `auth.go`, and the per-envelope-type handlers under `handlers/`). Adds the binary-side per-`conn_id` state machine that completes a [Mobile Protocol v2](../../protocol-mobile.md) Noise_IK handshake, validates the device-token piggybacked in IK message 1 early-data, and refuses every out-of-state inner frame at the WS-close layer. No application dispatch — `noise_msg` frames that reach the `open` state are dropped silently in this slice; the follow-up (#446) lands the handler-chain wiring.

**Wire role:** the responder half of [`internal/noise`](noise-package.md)'s `Responder` / `WriteResp` API, parameterised with the binary's static X25519 private key, the device registry, and an outbound `RoutingEnvelope` forwarder.

**Production wiring:** **not yet wired** into `cmd/pyry/relay.go`. The daemon still runs the v1 `internal/dispatch.Dispatcher` against `/v1/server`. The v2 manager is reachable only through test wiring today; cutover is a follow-up slice that also depends on the open-state handler surface (#446) and the pre-flight release-flag gate ([#436](../codebase/436.md)).

## Surface

```go
package relay

// WS close codes (the wire-spec values in docs/protocol-mobile.md § Error codes).
// 4401 (StatusUnauthorized) lives in auth.go and is reused unchanged.
const (
    StatusProtocolMismatch websocket.StatusCode = 4421 // state-machine / discriminator violation
    StatusHandshakeFailure websocket.StatusCode = 4426 // Noise_IK failure before CipherStates exist
)

type V2SessionState int

const (
    V2StateAwaitingInit V2SessionState = iota
    V2StateHandshakeComplete
    V2StateOpen
    V2StateClosed
)

type V2Session struct { /* unexported fields: connID, state, resp, send, recv */ }

func (s *V2Session) State() V2SessionState

type V2SessionConfig struct {
    Frames     <-chan protocol.RoutingEnvelope         // required; closes ⇒ Run returns nil
    Outbound   func(protocol.RoutingEnvelope) error    // required; production passes (*Connection).Send
    StaticPriv []byte                                  // required; must be noise.KeyLen (32) bytes
    Devices    *devices.Registry                       // required; token-validation predicate
    ServerID   string                                  // required; surfaced into hello_ack
    Logger     *slog.Logger                            // required (panic if nil)
}

type V2SessionManager struct { /* unexported */ }

func NewV2SessionManager(cfg V2SessionConfig) (*V2SessionManager, error)
func (m *V2SessionManager) Run(ctx context.Context) error
```

`NewV2SessionManager` panics on missing `Frames` or `Logger` (programmer errors, same posture as `internal/dispatch.New`); returns a wrapped error on missing `Outbound` / `Devices` / `ServerID` or on wrong-length `StaticPriv` (caller-facing config bugs). `Run` blocks until `Frames` closes (returns `nil`) or `ctx` is cancelled (returns `ctx.Err()`); every per-conn session is dropped on return.

## Wire types (`internal/protocol/v2envelope.go`)

```go
const V2Version = 2

const (
    TypeNoiseInit = "noise_init"
    TypeNoiseResp = "noise_resp"
    TypeNoiseMsg  = "noise_msg"
)

type InnerFrameV2 struct {
    Version int    `json:"v"`
    Type    string `json:"type"`
    Data    string `json:"data"` // base64.StdEncoding, padded; ≤ 65535 bytes decoded
}
```

Pure data type — the manager owns shape-checking. The 65535-byte cap on decoded `Data` is the Noise framework's per-message limit (`docs/protocol-mobile.md` § Wire shapes); enforced at the JSON-decode boundary so oversized payloads never reach `Responder.ReadInit`.

The `Token string \`json:"token,omitempty"\`` field appended to `protocol.HelloClientPayload` is the in-band carrier of the device-pairing token under v2 (`docs/protocol-mobile.md` § Authentication line 420). `omitempty` keeps v1 round-trip byte-identical for existing fixtures and tests. The v1 routing-envelope `RoutingEnvelope.Token` field is NOT removed in this slice — the v1 dispatcher still consumes it; v2's manager deliberately ignores `RoutingEnvelope.Token` per spec line 600.

## State machine

```
                    +-------------------+
                    | V2StateAwaitingInit |    (created lazily on first frame for a conn_id)
                    +-------------------+
                              |
                  noise_init  | (run handshake)
                              v
                    +---------------------+
                    | V2StateHandshakeCompl |  (CipherStates live; token not yet validated)
                    +---------------------+
                              |
                       token  | (Validate hit)
                          OK  v
                    +-----------+
                    | V2StateOpen |          (handler dispatch lands in #446; no-op drop today)
                    +-----------+

  Any rejection at any state → emit a single RoutingEnvelope carrying the
  optional sealed error frame plus the CloseCode, transition to V2StateClosed,
  delete the session from the manager's map.
```

The `handshakeComplete` substate is observably distinct from `open` even though both can be set inside the same `noise_init` handler — the externally-controlled `state` field exists so the gating test pins the "handler chain unreachable from `handshakeComplete`" invariant deterministically (AC #4) and so any future refactor that splits the dispatch loop cannot silently remove the invariant.

### Transition table

| Inbound on conn_id | `awaitingInit` | `handshakeComplete` | `open` | `closed` |
|---|---|---|---|---|
| `noise_init` | run handshake (below) | close(4421), → closed | close(4421), → closed | drop |
| `noise_resp` (phone is never the writer) | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |
| `noise_msg`, decrypt succeeds | close(4421), → closed (no CipherStates yet) | sealed `auth.invalid_token` + close(4401), → closed | drop (open-state dispatch deferred) | drop |
| `noise_msg`, decrypt fails / no CipherStates | close(4421), → closed | close(4421), → closed | drop | drop |
| Unknown `type` / bad `v` / malformed JSON / oversized `data` | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |

### `noise_init` happy-and-failure path

1. JSON-decode inner frame, validate `Version == 2` and `Type == "noise_init"`, base64-decode `Data` (size-cap 65535).
2. Lazy-construct `noise.Responder` from `cfg.StaticPriv` (one per session).
3. `Responder.ReadInit(data)` → early-data bytes. **On err: close(4426)** (MAC fail / wrong static pubkey / malformed IK message 1; no CipherStates exist, no AEAD-sealed error possible).
4. Decode early-data as a `protocol.Envelope`; require `Type == "hello"`. On decode failure or wrong type: **close(4421)** (handshake-layer protocol violation; CipherStates still don't exist).
5. Decode `Envelope.Payload` as `protocol.HelloClientPayload`. On decode failure: close(4421).
6. Marshal `HelloAckPayload{ProtocolVersion: "v2", ServerID: cfg.ServerID, ConnID: env.ConnID}` into an `Envelope{ID: 1, Type: hello_ack, InReplyTo: &hello.ID}`.
7. `Responder.WriteResp(ackEnvJSON)` → response bytes + `(send, recv)` CipherStates. **On err: close(4426)** (practically unreachable under correct flynn/noise; defensive).
8. Persist `send` / `recv` on the session. **State → `V2StateHandshakeComplete`** (the externally-observable substate, even though step 9 immediately advances or rejects).
9. Marshal an `InnerFrameV2{Type: noise_resp, Data: base64(respMsg)}`.
10. `cfg.Devices.Validate(hello.Token)`:
    - **Hit**: emit `RoutingEnvelope{ConnID, Frame: noiseRespFrame}` via `Outbound`, **state → `V2StateOpen`**. Log `v2.handshake.accept` with `conn_id` + `device_name`.
    - **Miss**: emit `noise_resp` first (so the AEAD channel exists on the wire), then AEAD-seal an `Envelope{Type: error, Payload: ErrorPayload{Code: auth.invalid_token, Message: MsgInvalidToken, Retryable: false}}` under `send`, wrap as an `InnerFrameV2{Type: noise_msg, Data: base64(ciphertext)}`, and emit one `RoutingEnvelope{ConnID, Frame: noiseMsgFrame, CloseCode: 4401}`. **State → `V2StateClosed`**, session deleted. Log `v2.handshake.reject.invalid_token` with `conn_id` only (NO device-name on reject — anti-enumeration, mirrors `auth.go:129-132`).

The AEAD-error-then-close path emits the error frame AND the close code in a **single** outbound routing envelope — atomic at the wire layer. This is what guarantees the spec's MUST: the phone observes the error envelope *before* the WS close (`docs/protocol-mobile.md` § Failure modes line 436). Two-call sequencing (`Send` then `CloseConn`) would race the relay's output paths.

### `noise_msg` in `V2StateHandshakeComplete` — the gating row

Today the natural inbound-frame flow never reaches this cell: state transitions through `handshakeComplete` atomically inside the `noise_init` handler. The cell exists for two reasons:

- **Future-proofing.** If a later slice introduces deferred token validation (e.g. network-backed registry lookup), `handshakeComplete` becomes observable to incoming frames between handshake completion and validation completion. The transition row defines the behaviour for that future world today.
- **Same-package unit-test verifiability.** A gating test in `v2session_test.go` directly assigns `s.state = V2StateHandshakeComplete` and injects a hand-rolled `(send, recv)` pair to drive this row deterministically — the **structural** proof that the handler chain is unreachable from `handshakeComplete`. AC #4's load-bearing invariant.

The implementation tries AEAD-decrypt-then-decode-as-`Envelope`. Decrypt failure → close(4421). Decrypt success → seal `auth.invalid_token` under the live `send` CipherState, emit + close(4401), regardless of envelope type. The handler chain in `internal/relay/handlers/` is **not** reached.

## Concurrency

**One goroutine.** `Run` is the only goroutine the manager owns. It reads `cfg.Frames`, looks up (or lazily creates) `m.sessions[env.ConnID]`, and processes the frame synchronously. `m.sessions` is mutated exclusively by `Run`; no mutex.

`V2Session` carries no lock. The package contract is "one goroutine per `conn_id` mutates the session"; in this slice that goroutine is `Run` itself. flynn/noise's `CipherState` carries a mutable 64-bit nonce counter; concurrent access would be UB — the serialisation point IS the lock.

Intentionally simpler than [`internal/dispatch.Dispatcher`](dispatch-package.md), which spins one goroutine per `conn_id` to absorb handler-side latency. v2 in this slice runs no handlers — every frame is a synchronous Noise call (~100 µs) or sub-µs JSON decode. Single-goroutine fan-in is correct and obviously safe; per-conn fan-out is a #446 concern once the open-state handler surface lands.

`V2Session.State()` is a plain field read. Safe today because no cross-goroutine reads exist. Once a broadcast layer or handler-side goroutines appear, the accessor will need `atomic.Int32` or a small mutex — not pre-emptively refactored.

## Security and log discipline

Mirrors v1's `internal/relay/auth.go` posture. The implementation MUST adhere; CR checks each rule against the diff.

- **MUST NOT log at any level**: `HelloClientPayload.Token`, `cfg.StaticPriv`, raw `RoutingEnvelope.Frame` bytes, AEAD ciphertext bytes (the `Data` field of any `noise_msg`), plaintext envelope payload bytes (post-AEAD-decrypt), base64-encoded forms of any of the above. The same MUST applies to `slog` fields, error wrapping (`fmt.Errorf("foo: %w", err)` where `err` accidentally carries the secret), and `panic` strings.
- **MUST log (operator-actionable) on ACCEPT**: event class `v2.handshake.accept`, `conn_id`, `device_name`. Plain low-cardinality string fields only.
- **MUST log (operator-actionable) on REJECT**: event class (`v2.handshake.reject.invalid_token` / `v2.handshake.reject.ik_failure` / `v2.state.reject`), `conn_id`, `close_code`. **NO `device_name`** even when the early-data carried one — anti-enumeration of paired-device names from binary logs.

`V2SessionConfig.StaticPriv` is the binary's 32-byte X25519 static private key. The doc-comment on the field declares it MUST NOT be logged, wrapped into an error message, or emitted on any wire surface — [`internal/keys`](keys-package.md) and [`internal/noise`](noise-package.md) document the same contract for the same bytes.

The AEAD-sealed error envelope on the 4401 path emits a static `MsgInvalidToken` string and a fixed `CodeAuthInvalidToken` code; no attacker-influenced content is echoed. Close-only paths (4421 / 4426) emit no envelope at all — no leakage surface.

## Test surface

### Same-package unit tests (`internal/relay/v2session_test.go`, no WS)

Each test constructs a `V2SessionManager` with an in-memory `outbound` recorder (mutex-guarded slice; goroutine-safe) and a `devices.Registry` built inline.

- `TestV2Session_HappyPath` — paired-device `hello` in early-data → state advances to `V2StateOpen`; `noise_resp` envelope on `Outbound` carries hello_ack; CipherStates non-nil; no close-code emitted.
- `TestV2Session_BadToken_AEADErrorThen4401` — unknown-token hello → exactly one outbound envelope with `CloseCode == 4401`, frame is a `noise_msg`-wrapped AEAD-sealed error envelope; the initiator side decrypts the wrapped envelope and the test asserts `Code == auth.invalid_token`. State = closed.
- `TestV2Session_IKReject_4426` — `noise_init` carrying random bytes (no real IK message 1) → exactly one outbound envelope with `CloseCode == 4426`, no Frame body. State = closed.
- `TestV2Session_NoiseInitAfterOpen_4421` — drive happy-path to `open`, then feed a second `noise_init` → outbound has the original `noise_resp` + a separate `CloseCode=4421` envelope. State = closed.
- `TestV2Session_Gating_NoiseMsgInHandshakeComplete_4401` — directly assign `s.state = V2StateHandshakeComplete`, `s.send/recv = <CipherStates from a real adjacent handshake>`, feed a `noise_msg` whose plaintext is a non-hello envelope. Asserts: exactly one outbound envelope with `CloseCode == 4401`, frame is AEAD-sealed `error{auth.invalid_token}`. **Structurally proves the "handler chain unreachable from handshakeComplete" invariant** — the regression guard for any future refactor that might add a v2→handler edge.
- `TestV2Session_OutOfStateRejections` — table-driven over the remaining cells: malformed JSON / unknown `Type` / bad `v` / unexpected `noise_resp` → 4421 in each state.
- `TestNewV2SessionManager_ConfigValidation` — panics on nil `Frames` / nil `Logger`; wrapped errors on nil `Outbound` / nil `Devices` / empty `ServerID` / wrong-length `StaticPriv`.

### E2E (`internal/e2e/relay_v2_handshake_test.go`, build tag `e2e`)

Spins up `fakerelay` (now with both `/v1/server` and `/v2/server`), wires `relay.Connect` + `V2SessionManager` inline (no daemon — `cmd/pyry/relay.go` still wires the v1 dispatcher), dials a `fakephone` against `/v1/client` (unchanged routing wire under v2), and drives a Noise_IK handshake from the phone side.

- `testV2HappyPath` — paired device → phone observes a `noise_resp` frame, decrypts hello_ack, then no further traffic.
- `testV2BadToken` — unpaired device → phone reads the AEAD-sealed `auth.invalid_token` `noise_msg`, then `Read` errors with `LastCloseStatus() == 4401`.
- `testV2IKReject` — phone sends an invalid noise_init (random bytes, no real IK message 1) → phone's next read errors with close code 4426. No prior frame from binary.

The gating-invariant test is unit-test-shape only — the e2e suite covers the natural inbound flows.

## Fakerelay / fakephone harness additions

- **`fakerelay.New` registers `/v2/server`** alongside `/v1/server`, sharing the existing `handleBinary` handler — the relay-side wire (binary↔relay routing envelope) is unchanged in v2. Phone-side `/v2/client` is NOT registered; tests connect the phone on `/v1/client`. The fakerelay's `binaryRecvPump` now treats `json.RawMessage` that marshals to the literal token `"null"` as "no frame to forward", matching the production relay's close-only envelope contract — without this, the close-only 4421/4426 paths would attempt to forward a `null` frame to the phone.
- **`fakephone.Client.SendBytes(data []byte)` / `ReceiveBytes(timeout)`** are byte-oriented siblings to `Send(env)` / `Receive(timeout)`. The wire shape inside `RoutingEnvelope.Frame` under v2 is an `InnerFrameV2` (not a `protocol.Envelope`), so the test driver builds the v2 frame as raw bytes and bypasses the `Envelope` marshal/unmarshal. `Send` / `Receive` delegate to the byte-oriented variants for the v1 case so v1 behaviour is unchanged.

## Out of scope (this slice)

- `noise_msg` application dispatch in the `open` state — follow-up slice (#446) wires the `internal/relay/handlers/` chain to v2.
- Encrypted echo round-trip e2e and tampered-`noise_msg` AEAD-failure → 4421 teardown — follow-up slice (#446; no dispatch path exists here to fail).
- Re-key timer + `rekey_request` handling — #435.
- Pre-flight release-flag gate — `pyry pair preflight` already lands (#436); the actual v2 cutover gate is in a later slice.
- Production wiring of `V2SessionManager` into `cmd/pyry/relay.go` — daemon path still runs the v1 dispatcher. Cutover decision lives with #446 (handlers) or a sibling.
- Per-phone-conn 10s handshake timeout — requires a relay→binary "phone connected" signal that does not exist in the v2 wire today. Tracked for a future protocol amendment + binary slice.
- `V2Session` cleanup on phone-initiated WS close — same root cause; relay→binary "phone disconnected" forward signal does not exist. State entries linger until the binary↔relay leg recycles.

## Dependencies

- [`internal/noise`](noise-package.md) (#433) — `Responder`, `ReadInit`, `WriteResp`, `CipherState`, `KeyLen`. The wrapper's empty-AD-at-the-type-system invariant flows through to every AEAD operation here.
- [`internal/devices`](devices-package.md) — `Registry.Validate(plain)` predicate (two-state, bumps `LastSeenAt` under `reg.mu`).
- [`internal/protocol`](protocol-package.md) — `Envelope`, `RoutingEnvelope`, `HelloClientPayload`, `HelloAckPayload`, `ErrorPayload`, the new `InnerFrameV2`, `V2Version`, `TypeNoise*` constants, and the new `Token` field on `HelloClientPayload`.
- [`github.com/coder/websocket`](relay-package.md#dependencies) — only for the `StatusCode` type aliasing the two new exported close codes.

## Related

- [`docs/specs/architecture/445-v2-inner-frame-handshake.md`](../../specs/architecture/445-v2-inner-frame-handshake.md) — architect spec (transition table + AC reconciliation + security review).
- [`docs/protocol-mobile.md`](../../protocol-mobile.md) §§ Authentication, Wire shapes, Failure modes, Error codes — wire-format source of truth.
- [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) — Mobile Protocol v2 (Noise_IK) parent decision.
- [`codebase/433.md`](../codebase/433.md) — `internal/noise` wrapper; the responder API this manager consumes.
- [`codebase/445.md`](../codebase/445.md) — per-ticket implementation notes.
- [`features/relay-package.md`](relay-package.md) — the v1 surfaces of `internal/relay`.
