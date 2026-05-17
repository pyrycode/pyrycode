# `internal/relay` V2 session manager — Noise_IK handshake + open-state dispatch

The fourth surface of `internal/relay` (alongside the v1 outbound dial in `connection.go`, the v1 first-frame auth gate in `auth.go`, and the per-envelope-type handlers under `handlers/`). Adds the binary-side per-`conn_id` state machine that completes a [Mobile Protocol v2](../../protocol-mobile.md) Noise_IK handshake, validates the device-token piggybacked in IK message 1 early-data, dispatches `noise_msg` frames in the `open` state through the existing handler chain (#446), intercepts v2 control envelopes (`rekey_request`, #454) at the dispatch boundary, runs the responder side of a phone-initiated re-key with peer-static continuity and atomic CipherState swap (#453), and refuses every out-of-state inner frame or tampered AEAD payload at the WS-close layer.

**Wire role:** the responder half of [`internal/noise`](noise-package.md)'s `Responder` / `WriteResp` API, parameterised with the binary's static X25519 private key, the device registry, an outbound `RoutingEnvelope` forwarder, and an optional `dispatch.Handler` table for open-state application dispatch.

**Production wiring:** **not yet wired** into `cmd/pyry/relay.go`. The daemon still runs the v1 `internal/dispatch.Dispatcher` against `/v1/server`. The v2 manager is reachable only through test wiring today; the cutover follow-up re-wires `cmd/pyry/relay.go` to construct `V2SessionManager` instead of `Dispatcher` and registers the handler functions against `V2SessionConfig.Handlers` rather than `Dispatcher.Register`. Depends on the pre-flight release-flag gate ([#436](../codebase/436.md)).

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

type V2Session struct { /* unexported fields: connID, state, resp, send, recv, device, peerStatic */ }

func (s *V2Session) State() V2SessionState

type V2SessionConfig struct {
    Frames     <-chan protocol.RoutingEnvelope         // required; closes ⇒ Run returns nil
    Outbound   func(protocol.RoutingEnvelope) error    // required; production passes (*Connection).Send
    StaticPriv []byte                                  // required; must be noise.KeyLen (32) bytes
    Devices    *devices.Registry                       // required; token-validation predicate
    ServerID   string                                  // required; surfaced into hello_ack
    Logger     *slog.Logger                            // required (panic if nil)
    Handlers   map[string]dispatch.Handler             // optional; open-state envelope-type → handler
}

type V2SessionManager struct { /* unexported */ }

func NewV2SessionManager(cfg V2SessionConfig) (*V2SessionManager, error)
func (m *V2SessionManager) Run(ctx context.Context) error
```

`NewV2SessionManager` panics on missing `Frames` or `Logger` (programmer errors, same posture as `internal/dispatch.New`); returns a wrapped error on missing `Outbound` / `Devices` / `ServerID` or on wrong-length `StaticPriv` (caller-facing config bugs). `Handlers` is optional — nil or empty means every open-state envelope falls through to a sealed `protocol.unsupported` reply via [`dispatch.Route`](dispatch-package.md). `Run` blocks until `Frames` closes (returns `nil`) or `ctx` is cancelled (returns `ctx.Err()`); every per-conn session is dropped on return.

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
                    | V2StateOpen |          (noise_msg → dispatch.Route → AEAD-sealed reply)
                    +-----------+

  Any rejection at any state → emit a single RoutingEnvelope carrying the
  optional sealed error frame plus the CloseCode, transition to V2StateClosed,
  delete the session from the manager's map.
```

The `handshakeComplete` substate is observably distinct from `open` even though both can be set inside the same `noise_init` handler — the externally-controlled `state` field exists so the gating test pins the "handler chain unreachable from `handshakeComplete`" invariant deterministically (AC #4) and so any future refactor that splits the dispatch loop cannot silently remove the invariant.

### Transition table

| Inbound on conn_id | `awaitingInit` | `handshakeComplete` | `open` | `closed` |
|---|---|---|---|---|
| `noise_init` | run handshake (below) | close(4421), → closed (`reason=noise_init_in_handshake_complete`) | **re-key responder; peer-static check; CipherState swap; state stays `open` (#453)** | drop |
| `noise_resp` (phone is never the writer) | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |
| `noise_msg`, decrypt succeeds | close(4421), → closed (no CipherStates yet) | sealed `auth.invalid_token` + close(4401), → closed | **dispatch via handler chain; AEAD-sealed reply emitted; state stays `open`** | drop |
| `noise_msg`, decrypt fails / no CipherStates | close(4421), → closed | close(4421), → closed | **close(4421), → closed (AEAD-failure teardown; session entry dropped — also fires on stale-key frames after a #453 swap)** | drop |
| Unknown `type` / bad `v` / malformed JSON / oversized `data` | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |

### `noise_init` happy-and-failure path

1. JSON-decode inner frame, validate `Version == 2` and `Type == "noise_init"`, base64-decode `Data` (size-cap 65535).
2. Lazy-construct `noise.Responder` from `cfg.StaticPriv` (one per session).
3. `Responder.ReadInit(data)` → early-data bytes. **On err: close(4426)** (MAC fail / wrong static pubkey / malformed IK message 1; no CipherStates exist, no AEAD-sealed error possible).
3a. **Capture `s.peerStatic = s.resp.PeerStatic()`** (#452) — the initiator's static pub, pinned for the session's lifetime at the earliest authenticated point. By the time `ReadInit` returns nil, flynn has MAC-verified and DH-decrypted the static; the value is authentic regardless of whether subsequent steps accept the hello. Set exactly once per `V2Session`; a future re-key (added in #453) MUST NOT overwrite it. The field is inert in the current code (no production reader); #453's `handleRekeyInit` will compare a fresh-handshake initiator's `PeerStatic()` against it via `bytes.Equal` and reject mismatches at WS close code 4426.
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

### `noise_msg` in `V2StateOpen` — application dispatch and AEAD-failure teardown

The two `open`-row cells filled by [#446](../codebase/446.md).

**Happy path** (`dispatchAppFrame`):

1. `s.recv.Decrypt(inner.Data)` → plaintext envelope JSON. The handler chain is unreached on `Decrypt` failure (see below).
2. **v2 control-envelope discriminator** (#454): `json.Unmarshal(plaintext, &probeEnv)`; on decode success **and** `probeEnv.Type == protocol.TypeRekeyRequest`, call `handleRekeyRequest(ctx, s, probeEnv)` and return. The application handler chain is NOT consulted. Decode failures deliberately fall through to step 3 so `dispatch.Route`'s malformed-envelope branch emits the sealed `protocol.malformed` reply established by #446. The probe is a re-decode (`dispatch.Route` decodes the same plaintext again); the cost is one small JSON parse per application frame, well below the per-frame AEAD cost.
3. Allocate a per-frame `outbound chan protocol.RoutingEnvelope` (buffer 8 — `handlerOutboundBuf`) and a per-frame `*dispatch.Conn` via [`dispatch.NewConn`](dispatch-package.md), carrying the matched device snapshot captured in step 10 of the handshake.
4. `dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext)` — same error-envelope paths as v1 `Dispatcher.handleOne` (malformed JSON → sealed `protocol.malformed`; unsupported / unknown type / no handler → sealed `protocol.unsupported`-or-`unknown_type`; handler error → log WARN, no synthesised reply).
5. Drain `outbound` non-blockingly. For each captured reply: `s.send.Encrypt(reply.Frame)` → `marshalInnerFrameV2(TypeNoiseMsg, ciphertext)` → emit via `m.send` with `CloseCode: 0`. The reply's `CloseCode` is ignored — close intent is reserved for the manager's own close-with paths.
6. Return. State remains `V2StateOpen`.

**v2 `rekey_request` handler** (`handleRekeyRequest`, #454). The binary is always the IK responder per [ADR 024](../decisions/024-noise-ik-mobile-e2e.md); an inbound `rekey_request` takes **no transport action** — no close, no outbound frame, no state mutation. The phone re-keys by sending `noise_init` directly, not by signalling via `rekey_request`. The handler decodes `env.Payload` as `struct{ Reason string }` and emits a single structured log line:

- Recognised reasons (`scheduled`, `manual`, `compromise` — closed set from `docs/protocol-mobile.md` § Re-key): **INFO** with `event=v2.rekey.request.received`, `conn_id`, `reason`.
- Empty / unknown / non-string / JSON-decode-failure: **WARN** with the same field shape. Forward-compat is deliberate — mobile may add a `reason` value before the binary catches up.

A malformed inner payload does **not** emit a sealed `protocol.malformed` reply: the envelope itself took no transport action, so emitting an error reply for a broken control payload would be a surprising behaviour change. The handler runs on the manager's single dispatch goroutine (same as everything else in this file); no new concurrency invariant.

### `noise_init` in `V2StateOpen` — re-key responder swap (#453)

A `noise_init` arriving for a `conn_id` already in `V2StateOpen` is a phone-initiated re-key per `docs/protocol-mobile.md` § Re-key (the phone is the only IK initiator). The top of `handleNoiseInit` is now a `switch s.state` that routes the `open` arm to `handleRekeyInit`. The handler runs the IK responder again against `cfg.StaticPriv`:

1. `noise.NewResponder(cfg.StaticPriv)` (fresh `Responder` per re-run; the original `s.resp` is left dangling — dead state, cleanup deferred).
2. `Responder.ReadInit(inner.Data)` — re-key `noise_init` early-data is empty per spec; the returned slice is discarded. On err: close(4426) with `reason=rekey_read_init_failed`.
3. **Peer-static continuity check.** `bytes.Equal(resp.PeerStatic(), s.peerStatic)` — the new initiator's static MUST match the value captured at initial handshake (the [#452](../codebase/452.md) field). Mismatch closes at 4426 with `reason=rekey_peer_static_mismatch`; the reject log line deliberately omits `device_name` (anti-enumeration discipline — the re-key initiator's identity is unknown / hostile). `bytes.Equal` is intentionally variable-time-acceptable: both operands are public keys, so timing leakage carries no secret. A one-line code comment names this choice to forestall a "should be `subtle.ConstantTimeCompare`" review nit. **Hello validation and token re-check do NOT run on the re-key path** — they ran at initial handshake; the per-rekey identity gate is the peer-static check, not the token.
4. `Responder.WriteResp(nil)` (empty early-data per spec) → new `(send, recv)` CipherStates + response bytes. On err: close(4426) with `reason=rekey_write_resp_failed`.
5. Marshal `InnerFrameV2{Type: noise_resp, Data: base64(respMsg)}`. On err: close(4426) with `reason=rekey_marshal_noise_resp`.
6. **Atomic CipherState swap.** A single tuple assignment `s.send, s.recv = newSend, newRecv` on the manager's single dispatch goroutine. No half-mixed state where one direction uses new keys and the other uses old — the loop IS the lock for `flynn/noise`'s non-goroutine-safe `CipherState` (#433's contract); a tuple assignment on this goroutine cannot be observed half-applied by any other code path, so the spec's atomic-switchover requirement is structural. The old `*CipherState` pointers are dropped from the struct; Go's GC reclaims the underlying memory. **No explicit `Wipe()` of the key bytes is exposed** — would require touching #433's surface, deferred; the single-owner-goroutine invariant means no code path reads the old state after the swap, which is the practical zeroisation property.
7. Log `v2.rekey.accept` at INFO with `conn_id` + `device_name` (operator-actionable; SAME device as initial handshake by construction — `s.device` is preserved across re-key).
8. Emit the new `noise_resp` envelope. State stays `V2StateOpen`; `s.device` and `s.peerStatic` are preserved (the [#452](../codebase/452.md) lifetime contract — a successful re-key MUST NOT overwrite `s.peerStatic`).

**Failure-mode → close-code table.** Every re-key failure mode closes at **4426** — the SAME code the initial handshake uses for IK-pattern failure. No new close code is introduced. The five reject branches in `handleRekeyInit` (`rekey_responder_init_failed`, `rekey_read_init_failed`, `rekey_peer_static_mismatch`, `rekey_write_resp_failed`, `rekey_marshal_noise_resp`) each emit a `v2.handshake.reject.ik_failure` WARN line and call `closeWith(ctx, s, StatusHandshakeFailure, nil)`. `closeWith` removes the session entry from `m.sessions`, so the next inbound frame on the same `conn_id` lazy-creates a fresh `V2StateAwaitingInit` session.

**`noise_init` in `V2StateHandshakeComplete` still rejects at 4421.** A `noise_init` arriving while CipherStates are held but uncommitted is the same state-machine violation it was before #453 (the `default` arm of the `switch s.state` block, with `reason=noise_init_in_handshake_complete`).

**Old-key frames after the swap fail AEAD and tear down the conn.** Once `s.recv` points at the new CipherState, any frame sealed under the old keys fails `s.recv.Decrypt` and lands in the existing #446 tampered-frame branch: `closeWith(StatusProtocolMismatch /*4421*/, nil)` → session removal. **No new code path** — the inheritance is verified by `TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421`.

**Head-of-line blocking during re-key.** The re-key handshake costs roughly one X25519 derivation + AEAD setup (~100µs) plus one outbound `noise_resp` send. During this window the dispatch goroutine cannot service any other `conn_id` — same posture as the initial handshake. The per-conn fan-out follow-up that #446 names also covers this.

**AEAD-failure teardown** (tampered / replayed / truncated `noise_msg`): `s.recv.Decrypt` returns non-nil → log `v2.aead.fail` with `conn_id` + `close_code=4421` (NO error text — the underlying flynn/noise error may carry counter indices that aren't operator-actionable) → `closeWith(ctx, s, StatusProtocolMismatch, nil)`. `closeWith` emits a single close-only routing envelope and **deletes the session entry from `m.sessions`** — the next `noise_init` for the same `conn_id` lazy-creates a fresh `awaitingInit` with no carry-over CipherStates. The handler chain is structurally unreachable: the AEAD-decrypt branch returns before `dispatchAppFrame` is called.

**Why the outbound channel is not closed.** Closing on the sending side panics any goroutine the handler accidentally forked that retains the `*dispatch.Conn`. The drain is non-blocking (`select { case env := <-outbound: ...; default: return }`); a misbehaving handler that forks a sender after `dispatchAppFrame` returns writes into a leaked but capacity-bounded channel that the GC reclaims once the goroutine exits. This is the documented synchronous-handler assumption — handlers MUST be synchronous and MUST NOT retain `*dispatch.Conn` beyond the call.

The `device` field on `V2Session` is set exactly once in the handshake token-accept branch (right before state advances to `V2StateOpen`) and is surfaced through `*dispatch.Conn.Auth()`. Same lifetime as v1's `Conn.auth` slot — revocation of the device after handshake does NOT tear down the active conn; this matches the v1 posture and is intentional. Revocation propagation for active conns is tracked as a separate concern.

The `peerStatic` field on `V2Session` (#452) is similarly set exactly once — at step 3a above, before any branch that calls `closeWith`. A token failure (or any later handshake-layer failure) tears the session down via `closeWith` and `delete(m.sessions, s.connID)`, which drops the captured field along with the session entry — a failed handshake leaves no peerStatic to compare against on a future re-key. The field is identity-bearing (the public-static of the paired peer); the doc-comment pins the **MUST NOT log** discipline so a future log-line refactor cannot relax it on the "but it's public" instinct — emitting it makes the binary log a parallel device registry for anyone with log-read access.

## Concurrency

**One goroutine.** `Run` is the only goroutine the manager owns. It reads `cfg.Frames`, looks up (or lazily creates) `m.sessions[env.ConnID]`, and processes the frame synchronously. `m.sessions` is mutated exclusively by `Run`; no mutex.

`V2Session` carries no lock. The package contract is "one goroutine per `conn_id` mutates the session"; today that goroutine is `Run` itself. flynn/noise's `CipherState` carries a mutable 64-bit nonce counter; concurrent access would be UB — the serialisation point IS the lock.

Intentionally simpler than [`internal/dispatch.Dispatcher`](dispatch-package.md), which spins one goroutine per `conn_id` to absorb handler-side latency. v2 runs handlers synchronously on the manager's single dispatch goroutine — a slow handler stalls dispatch for ALL `conn_id`s, not just the current one. The worst-case stall today is `send_message`'s 30 s `Activate` timeout. This is deliberate for the size:S surface; per-conn fan-out (one goroutine per `conn_id` with a per-session mutex guarding `s.send` / `s.recv`) is the documented production-cutover follow-up and the priority concern before flipping `cmd/pyry/relay.go` to v2.

`V2Session.State()` is a plain field read. Safe today because no cross-goroutine reads exist. Once a broadcast layer or handler-side goroutines appear, the accessor will need `atomic.Int32` or a small mutex — not pre-emptively refactored.

## Security and log discipline

Mirrors v1's `internal/relay/auth.go` posture. The implementation MUST adhere; CR checks each rule against the diff.

- **MUST NOT log at any level**: `HelloClientPayload.Token`, `cfg.StaticPriv`, raw `RoutingEnvelope.Frame` bytes, AEAD ciphertext bytes (the `Data` field of any `noise_msg`), plaintext envelope payload bytes (post-AEAD-decrypt), handler reply envelope bytes (pre-encrypt), encrypted reply bytes (post-`s.send.Encrypt`), base64-encoded forms of any of the above, **`V2Session.peerStatic` bytes (#452)**. The same MUST applies to `slog` fields, error wrapping (`fmt.Errorf("foo: %w", err)` where `err` accidentally carries the secret), and `panic` strings. `peerStatic` is identity-bearing rather than secret, but the no-key-bytes-in-logs discipline extends to per-session identity pins — emitting it makes the binary log a parallel device registry.
- **MUST log (operator-actionable) on ACCEPT**: event class `v2.handshake.accept`, `conn_id`, `device_name`. Plain low-cardinality string fields only.
- **MUST log (operator-actionable) on REJECT**: event class (`v2.handshake.reject.invalid_token` / `v2.handshake.reject.ik_failure` / `v2.state.reject`), `conn_id`, `close_code`. **NO `device_name`** even when the early-data carried one — anti-enumeration of paired-device names from binary logs.
- **MUST log on open-state AEAD failure**: event class `v2.aead.fail`, `conn_id`, `close_code=4421`. **NO error text** from `s.recv.Decrypt` (the underlying flynn/noise error may carry counter indices that aren't operator-actionable). **NO envelope shape information** — a frame that didn't decrypt cannot be inspected.
- **No per-envelope log on the open-state happy path.** High-frequency message traffic would spam the log channel; existing v1 handler logs (`send_message.ack`, etc.) inherit their per-handler log policy and surface the per-envelope diagnostic instead.

`V2SessionConfig.StaticPriv` is the binary's 32-byte X25519 static private key. The doc-comment on the field declares it MUST NOT be logged, wrapped into an error message, or emitted on any wire surface — [`internal/keys`](keys-package.md) and [`internal/noise`](noise-package.md) document the same contract for the same bytes.

The AEAD-sealed error envelope on the 4401 path emits a static `MsgInvalidToken` string and a fixed `CodeAuthInvalidToken` code; no attacker-influenced content is echoed. Close-only paths (4421 / 4426) emit no envelope at all — no leakage surface.

## Test surface

### Same-package unit tests (`internal/relay/v2session_test.go`, no WS)

Each test constructs a `V2SessionManager` with an in-memory `outbound` recorder (mutex-guarded slice; goroutine-safe) and a `devices.Registry` built inline.

- `TestV2Session_HappyPath` — paired-device `hello` in early-data → state advances to `V2StateOpen`; `noise_resp` envelope on `Outbound` carries hello_ack; CipherStates non-nil; no close-code emitted.
- `TestV2Session_BadToken_AEADErrorThen4401` — unknown-token hello → exactly one outbound envelope with `CloseCode == 4401`, frame is a `noise_msg`-wrapped AEAD-sealed error envelope; the initiator side decrypts the wrapped envelope and the test asserts `Code == auth.invalid_token`. State = closed.
- `TestV2Session_IKReject_4426` — `noise_init` carrying random bytes (no real IK message 1) → exactly one outbound envelope with `CloseCode == 4426`, no Frame body. State = closed.
- (Removed in #453: `TestV2Session_NoiseInitAfterOpen_4421` pinned the behaviour that re-key responder #453 intentionally changes; replacement coverage is the three `TestV2Session_RekeyResponder_*` tests below.)
- `TestV2Session_Gating_NoiseMsgInHandshakeComplete_4401` — directly assign `s.state = V2StateHandshakeComplete`, `s.send/recv = <CipherStates from a real adjacent handshake>`, feed a `noise_msg` whose plaintext is a non-hello envelope. Asserts: exactly one outbound envelope with `CloseCode == 4401`, frame is AEAD-sealed `error{auth.invalid_token}`. **Structurally proves the "handler chain unreachable from handshakeComplete" invariant** — the regression guard for any future refactor that might add a v2→handler edge.
- `TestV2Session_OutOfStateRejections` — table-driven over the remaining cells: malformed JSON / unknown `Type` / bad `v` / unexpected `noise_resp` → 4421 in each state.
- `TestNewV2SessionManager_ConfigValidation` — panics on nil `Frames` / nil `Logger`; wrapped errors on nil `Outbound` / nil `Devices` / empty `ServerID` / wrong-length `StaticPriv`. `Handlers` is optional — no new validation case.

Open-state dispatch additions (#446):

- `TestV2Session_OpenState_EncryptedRoundTrip` — paired-device happy path through `dispatchAppFrame`. Stub handler keyed by `TypeListConversations` replies via `c.Reply`; phone-side decrypt of the captured `noise_msg` matches the handler's payload, `InReplyTo` echoes the request id, session state stays `V2StateOpen`.
- `TestV2Session_OpenState_TamperedNoiseMsg_4421` — flip one byte of a real ciphertext → exactly one outbound envelope with `CloseCode == 4421` and nil `Frame`, the registered handler's `atomic.Bool` flag stays false (handler chain structurally unreachable), and `mgr.sessions[v2TestConnID]` is absent (AC #3 — `closeWith` deletion).
- `TestV2Session_OpenState_FreshNoiseInitAfterAEADClose` — companion to the prior test. After 4421+cleanup, a second `noise_init` on the same `conn_id` completes a fresh handshake; a ciphertext sealed under the OLD `initSend` fails against the new session's `s.recv` (deterministic proof that the post-cleanup session is fresh `awaitingInit`-then-`open` with no carry-over CipherStates).
- `TestV2Session_OpenState_UnknownEnvelopeType_SealedUnsupportedReply` — open-state envelope with `Handlers = nil` → AEAD-sealed `Envelope{Type: TypeError, Payload.Code: CodeProtocolUnsupported}`. State stays `open`.
- `TestV2Session_OpenState_MalformedInnerEnvelope_SealedMalformedReply` — open-state envelope whose AEAD plaintext is raw garbage → AEAD-sealed `Envelope{Type: TypeError, Payload.Code: CodeProtocolMalformed}`. State stays `open`.
- `TestV2Session_OpenState_HandlerAuthDevice` — handler captures `c.Auth().Name` from inside the dispatch closure; asserts the matched-device snapshot captured during handshake (`s.device`) reaches the handler via `*dispatch.Conn.Auth()`.

Peer-static capture (#452):

- `TestV2Session_InitialHandshake_CapturesPeerStatic` — drives a paired-device handshake to `V2StateOpen` via `driveToOpen`, then white-box-asserts `mgr.sessions[v2TestConnID].peerStatic == initPub` and `len(...) == noise.KeyLen`. The length check is the regression guard against an empty-slice silently passing a future `bytes.Equal(nil, nil)` comparison if the capture site is skipped. Pins the capture invariant for the inert-in-this-slice field that #453 will read.

v2 control-envelope discriminator (#454):

- `TestV2Session_OpenState_RekeyRequest_Intercepted` — paired-device handshake to open → AEAD-sealed `{type: "rekey_request", payload: {reason: "scheduled"}}` → stub handler's `atomic.Bool` stays false (application chain unreachable), session stays `V2StateOpen`, no outbound close envelope. Structural proof that the probe in `dispatchAppFrame` runs before `dispatch.Route`.
- `TestV2Session_OpenState_RekeyRequest_UnknownReasonTolerated` — `{reason: "lunar-eclipse"}` → buffer-logger captures `level=WARN`, `event=v2.rekey.request.received`, `reason=lunar-eclipse`. Session stays open, no outbound frame. Forward-compat posture for unknown reasons.
- `TestV2Session_OpenState_RekeyRequest_RecognisedReasons` — table-driven over `{scheduled, manual, compromise}`; each subtest asserts `level=INFO`, the correct `reason` field, no close, no outbound frame, state stays open.

Re-key responder swap (#453):

- `TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys` — drives the paired-device handshake to open, constructs a SECOND `noise.Initiator` reusing the SAME `initPriv` (peer-continuity invariant), feeds a fresh `noise_init` with empty early-data, asserts the rekey `noise_resp` returns under `CloseCode == 0`, then AEAD-seals a `TypeListConversations` request under the NEW `initSend2` and asserts the reply decrypts cleanly under the NEW `initRecv2`. Post-stop assertions: `state == V2StateOpen`, `s.device.Name` preserved, `bytes.Equal(s.peerStatic, initPub)` (#452 lifetime contract honoured across re-key). Pins AC #1 + AC #5 — both directions of the swap are wired and the device + peer-static snapshots survive.
- `TestV2Session_RekeyResponder_DifferentPeerStatic_4426` — drives to open, then feeds a fresh `noise_init` from a DIFFERENT keypair. Exactly one additional outbound envelope with `CloseCode == 4426` and nil Frame; `mgr.sessions[v2TestConnID]` absent; log buffer contains `event=v2.handshake.reject.ik_failure` + `reason=rekey_peer_static_mismatch`; **the reject log line specifically does NOT contain `device_name`** (per-line substring check — a global check would false-positive on the initial `v2.handshake.accept`). **Security-load-bearing test** for the Threat #3 residual-risk claim.
- `TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421` — drives to open, stashes a ciphertext sealed under `sess.initSend` BEFORE the re-key, completes a successful re-key with the same static, then feeds the stashed stale frame. The inherited #446 tampered-frame branch fires against the post-swap `s.recv` → `CloseCode == 4421`, nil Frame, session removed. **No new code path** — verifies the inheritance at the new authenticated-state boundary.

### E2E (`internal/e2e/relay_v2_handshake_test.go`, build tag `e2e`)

Spins up `fakerelay` (now with both `/v1/server` and `/v2/server`), wires `relay.Connect` + `V2SessionManager` inline (no daemon — `cmd/pyry/relay.go` still wires the v1 dispatcher), dials a `fakephone` against `/v1/client` (unchanged routing wire under v2), and drives a Noise_IK handshake from the phone side.

- `testV2HappyPath` — paired device → phone observes a `noise_resp` frame, decrypts hello_ack, then no further traffic.
- `testV2BadToken` — unpaired device → phone reads the AEAD-sealed `auth.invalid_token` `noise_msg`, then `Read` errors with `LastCloseStatus() == 4401`.
- `testV2IKReject` — phone sends an invalid noise_init (random bytes, no real IK message 1) → phone's next read errors with close code 4426. No prior frame from binary.
- `testV2EncryptedEchoRoundTrip` (#446) — paired-device handshake to open with a stub handler registered against `TypeListConversations`; phone-side AEAD-seal request, read one inner frame back, decrypt with `initRecv`, assert the inner envelope's `Type`/`InReplyTo`/`Payload` match the handler's reply.
- `testV2TamperedNoiseMsg_4421` (#446) — phone sends a `noise_msg` with one byte flipped after handshake; phone observes `LastCloseStatus() == 4421`. The "fresh `noise_init` on the same `conn_id`" assertion lives in the unit test layer because `fakerelay` assigns a new `conn_id` per dial.

The gating-invariant test and the post-AEAD-failure fresh-handshake test are unit-shape only — the e2e suite covers the natural inbound flows.

## Fakerelay / fakephone harness additions

- **`fakerelay.New` registers `/v2/server`** alongside `/v1/server`, sharing the existing `handleBinary` handler — the relay-side wire (binary↔relay routing envelope) is unchanged in v2. Phone-side `/v2/client` is NOT registered; tests connect the phone on `/v1/client`. The fakerelay's `binaryRecvPump` now treats `json.RawMessage` that marshals to the literal token `"null"` as "no frame to forward", matching the production relay's close-only envelope contract — without this, the close-only 4421/4426 paths would attempt to forward a `null` frame to the phone.
- **`fakephone.Client.SendBytes(data []byte)` / `ReceiveBytes(timeout)`** are byte-oriented siblings to `Send(env)` / `Receive(timeout)`. The wire shape inside `RoutingEnvelope.Frame` under v2 is an `InnerFrameV2` (not a `protocol.Envelope`), so the test driver builds the v2 frame as raw bytes and bypasses the `Envelope` marshal/unmarshal. `Send` / `Receive` delegate to the byte-oriented variants for the v1 case so v1 behaviour is unchanged.

## Out of scope (deferred)

- **Production wiring of `V2SessionManager` into `cmd/pyry/relay.go`** — daemon path still runs the v1 dispatcher. Cutover re-wires the daemon to construct `V2SessionManager` instead of `Dispatcher` and registers production handlers against `V2SessionConfig.Handlers`. Gated by the pre-flight release-flag check ([#436](../codebase/436.md)).
- **Per-conn fan-out for handler dispatch.** Open-state handler dispatch runs synchronously on the manager's single goroutine; a long-running handler stalls all conns. The follow-up spawns one goroutine per `conn_id` with a per-session mutex guarding `s.send` / `s.recv` (mirroring `dispatch.Dispatcher.runConn`). Priority concern before production cutover.
- Re-key timer + scheduler that emits outbound `rekey_request` — #435 super-ticket. The responder-side discriminator landed in [#454](../codebase/454.md) (above); the responder swap landed in [#453](../codebase/453.md); the binary's own emit of `rekey_request` (using the same `protocol.TypeRekeyRequest` constant) is #450's future slice, along with the 1-hour timer and 30s reply timeout.
- Operator-facing `pyry rekey <conn_id>` verb — #451 (sibling of #450).
- Explicit `Wipe()` of old CipherState key bytes on re-key swap — would require touching #433's surface; deferred. The single-owner-goroutine invariant provides the practical zeroisation property (no code path observes the old state after the swap); documented in the `V2Session` package comment.
- `s.resp` reset to nil after handshake completes — the field is dead state after `WriteResp` returns and is unused by the re-key path (which constructs a fresh local `Responder`). Cleanup belongs in a `V2Session`-shape refactor, not in any re-key slice.
- `V2Session` cleanup on phone-initiated WS close — relay→binary "phone disconnected" forward signal does not exist on the v2 wire today. AEAD-failure teardown (this slice) IS the only binary-initiated cleanup path; phone-initiated reconnects still cannot trigger local cleanup. State entries linger until the binary↔relay leg recycles.
- Per-phone-conn 10s handshake timeout — requires a relay→binary "phone connected" signal that does not exist in the v2 wire today. Tracked for a future protocol amendment + binary slice.
- Revocation propagation to active conns — the device snapshot captured on `s.device` does not refresh after handshake; same posture as v1's `dispatch.Conn.auth`. Revocation tears down at the next WS recycle, not mid-conn.

## Dependencies

- [`internal/noise`](noise-package.md) (#433) — `Responder`, `ReadInit`, `WriteResp`, `CipherState`, `KeyLen`. The wrapper's empty-AD-at-the-type-system invariant flows through to every AEAD operation here.
- [`internal/devices`](devices-package.md) — `Registry.Validate(plain)` predicate (two-state, bumps `LastSeenAt` under `reg.mu`).
- [`internal/dispatch`](dispatch-package.md) — `Handler`, `Conn`, `NewConn`, `Route` (#446). The same handler-table dispatch primitives used by v1's `Dispatcher`, factored out so the v2 manager does not duplicate the malformed/unsupported/unknown-type error-envelope logic.
- [`internal/protocol`](protocol-package.md) — `Envelope`, `RoutingEnvelope`, `HelloClientPayload`, `HelloAckPayload`, `ErrorPayload`, `InnerFrameV2`, `V2Version`, `TypeNoise*` constants, and the `Token` field on `HelloClientPayload`.
- [`github.com/coder/websocket`](relay-package.md#dependencies) — only for the `StatusCode` type aliasing the two new exported close codes.

## Related

- [`docs/specs/architecture/445-v2-inner-frame-handshake.md`](../../specs/architecture/445-v2-inner-frame-handshake.md) — handshake spec (transition table + AC reconciliation + security review).
- [`docs/specs/architecture/446-v2-noise-msg-application-dispatch.md`](../../specs/architecture/446-v2-noise-msg-application-dispatch.md) — open-state dispatch + AEAD-failure teardown spec.
- [`docs/protocol-mobile.md`](../../protocol-mobile.md) §§ Authentication, Wire shapes, Failure modes, Error codes — wire-format source of truth.
- [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) — Mobile Protocol v2 (Noise_IK) parent decision.
- [`codebase/433.md`](../codebase/433.md) — `internal/noise` wrapper; the responder API this manager consumes.
- [`codebase/445.md`](../codebase/445.md) / [`codebase/446.md`](../codebase/446.md) — per-ticket implementation notes for the handshake and open-state slices.
- [`codebase/452.md`](../codebase/452.md) — `V2Session.peerStatic` capture at the initial IK handshake; pure-data exposure for the re-key responder's peer-continuity check.
- [`codebase/454.md`](../codebase/454.md) — v2 `rekey_request` control-envelope discriminator at the `dispatchAppFrame` seam; logs-only `handleRekeyRequest`.
- [`codebase/453.md`](../codebase/453.md) — v2 re-key responder swap on open conn; `handleRekeyInit`, peer-static continuity check, atomic CipherState swap.
- [`features/dispatch-package.md`](dispatch-package.md) — `Route` and `NewConn` (the production-allowed counterpart to `NewTestConn`).
- [`features/relay-package.md`](relay-package.md) — the v1 surfaces of `internal/relay`.
