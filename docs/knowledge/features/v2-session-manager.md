# `internal/relay` V2 session manager — Noise_IK handshake + open-state dispatch

The fourth surface of `internal/relay` (alongside the v1 outbound dial in `connection.go`, the v1 first-frame auth gate in `auth.go`, and the per-envelope-type handlers under `handlers/`). Adds the binary-side per-`conn_id` state machine that completes a [Mobile Protocol v2](../../protocol-mobile.md) Noise_IK handshake, validates the device-token piggybacked in IK message 1 early-data, dispatches `noise_msg` frames in the `open` state through the existing handler chain (#446), intercepts v2 control envelopes (`rekey_request`, #454) at the dispatch boundary, runs the responder side of a phone-initiated re-key with peer-static continuity and atomic CipherState swap (#453), arms a per-session 1-hour timer that emits an AEAD-sealed `rekey_request` envelope and tears the conn down at WS 4426 if the phone does not reply with a fresh `noise_init` within 30 s (#450), exposes a `Rekey(ctx, connID)` method that funnels operator-driven manual re-keys onto the same emit machinery with `payload.reason = "manual"` (#462), exposes a `Push(ctx, connID, env)` method that funnels a concurrency-safe **server-initiated** delivery of an unsolicited `noise_msg` to an addressed open session (#571), exposes an `ActiveConnIDs(ctx)` method that returns a concurrency-safe snapshot of the `conn_id`s of every currently-open session — the enumeration half of the fan-out primitive (#588), and refuses every out-of-state inner frame or tampered AEAD payload at the WS-close layer.

**Wire role:** the responder half of [`internal/noise`](noise-package.md)'s `Responder` / `WriteResp` API, parameterised with the binary's static X25519 private key, the device registry, an outbound `RoutingEnvelope` forwarder, and an optional `dispatch.Handler` table for open-state application dispatch.

**Production wiring:** **wired into the daemon as of [#549](../codebase/549.md)**, behind the operator switch `PYRY_MOBILE_V2=1`. With the switch set, `cmd/pyry/relay.go:startRelay` builds the `V2SessionManager` over `conn.Frames()` (via the `startRelayV2` helper) instead of the v1 `internal/dispatch.Dispatcher`, registering the **same three** `dispatch.Handler` values against `V2SessionConfig.Handlers` that the v1 path passes to `Dispatcher.Register`. The static key is loaded with the same `keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(name))` pair `pyry pair` uses, so the daemon's private key derives the public key the phone pinned at pairing. With the switch unset (the default), the v1 path is byte-for-byte unchanged. The cutover is a **hard switch, no soft fallback** ([ADR 024](../decisions/024-noise-ik-mobile-e2e.md)); `pyry pair preflight` ([#436](../codebase/436.md)) is the operator's pre-flip safety check, **not** the runtime selector. See [`codebase/549.md`](../codebase/549.md) § Cutover behaviour for the switch mechanism and the load-bearing "selection MUST NOT be driven by device count" constraint.

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

// Satisfies control.Rekeyer. Operator-driven manual re-key trigger (#462).
func (m *V2SessionManager) Rekey(ctx context.Context, connID string) error

// Concurrency-safe server-initiated push (#571). Seals a caller-owned
// envelope under the addressed session's send CipherState, wraps it as a
// noise_msg, and forwards it to the phone. Safe to call from any goroutine
// other than the dispatch goroutine — funneled onto Run so s.send is never
// touched concurrently with an in-flight reply or a re-key swap.
func (m *V2SessionManager) Push(ctx context.Context, connID string, env protocol.Envelope) error

// Concurrency-safe snapshot of the conn IDs of every session currently in
// V2StateOpen (#588) — the enumeration half of server-initiated fan-out. The
// consumer (the v2 assistant-turn bridge, #589, in cmd/pyry/assistant_turn_v2.go)
// calls this, then Push on each returned id to broadcast one assistant-turn
// `message` envelope to every connected phone. Safe to call from any goroutine other than the
// dispatch goroutine — funneled onto Run so m.sessions is never read
// concurrently with a map write. Result is an unordered set; sessions still
// handshaking / token-unvalidated are excluded (the same V2StateOpen gate Push
// enforces). Returns nil on ctx cancellation or after Run has exited — no
// error, since a snapshot has no failure the caller can act on. v2 analog of
// v1's dispatch.Dispatcher.ActiveConns().
func (m *V2SessionManager) ActiveConnIDs(ctx context.Context) []string

// Sentinels returned by Rekey and Push. ErrConnNotFound wraps
// control.ErrConnNotFound via %w so the slice A dispatcher's errors.Is
// mapping fires unchanged.
var (
    ErrConnNotFound   error // wraps control.ErrConnNotFound
    ErrSessionNotOpen error // session not in V2StateOpen (or, for Rekey, already awaiting a rekey reply)
)
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

### Initiator-side scheduled re-key (#450) — 1-hour timer + `rekey_request` emit + 30 s reply timeout

Each `V2Session` that reaches `V2StateOpen` arms a 1-hour `*time.Timer` (`rekeyInterval`, package-var lowercase so tests substitute via `t.Cleanup`). On fire the timer's `time.AfterFunc` callback pushes a `wakeSignal{s, wakeRekeyEmit}` onto a per-manager buffered channel `m.wake` (cap `wakeBufferSize = 16`); the manager's `Run` loop pops the signal on a new third select arm and dispatches to `handleWake` → `emitRekeyRequest` on its own goroutine. The callback NEVER touches session state directly — the single-owner-goroutine invariant for `s.send` / `s.recv` / `s.state` is preserved by structurally routing every timer fire through the wake channel.

`Run` derives `runCtx, cancelRun := context.WithCancel(ctx); defer cancelRun()` and passes `runCtx` to every downstream handler (`handleFrame`, `handleWake`, the arming helpers, and transitively to `closeWith` / `handleNoiseInit` / `handleRekeyInit`). The arming callbacks select on `(m.wake <- signal, runCtx.Done())` so a fired-but-undelivered wake on a shutting-down `Run` exits via the ctx branch and leaves no goroutine behind. `Run`'s observable behaviour is unchanged — the return value is still `ctx.Err()` (so parent-cancel returns `context.Canceled` and `Frames`-close returns nil).

`emitRekeyRequest` mirrors `sealError`'s seal template: marshal `Envelope{ID: 1, Type: protocol.TypeRekeyRequest, TS: now, Payload: {"reason": "scheduled"}}` → `s.send.Encrypt` → `marshalInnerFrameV2(TypeNoiseMsg, ciphertext)` → `m.send(RoutingEnvelope{ConnID, Frame})`. Envelope `ID = 1` because there is no `rekey_ack` to correlate by `InReplyTo` per `docs/protocol-mobile.md` § Re-key (the next successful AEAD round-trip under the new keys is the implicit ack). `reason` is always `"scheduled"` in this slice; `"manual"` and `"compromise"` belong to the future `pyry rekey <conn_id>` verb (#451).

On successful emit, the manager sets `s.awaitingRekeyReply = true` and arms `s.rekeyReplyTimer = m.armRekeyReplyTimer(ctx, s)` (`rekeyReplyTimeout = 30 * time.Second`, same package-var posture). Three terminal branches:

- **Phone replies in time.** A fresh `noise_init` lands in `handleRekeyInit`, the IK handshake re-runs with the peer-static continuity check (#453), and on swap success the new `s.rekeyComplete(m, ctx)` hook fires at the success tail of `handleRekeyInit` (unconditional on the swap path — a spontaneous phone-initiated re-key that the binary did not request still re-bases the 1-hour cadence; clearing an unset `awaitingRekeyReply` is a no-op, stopping a nil `rekeyReplyTimer` is a no-op). `rekeyComplete` clears `awaitingRekeyReply`, stops + nils `rekeyReplyTimer`, stops the old `rekeyTimer`, and assigns `s.rekeyTimer = m.armRekeyTimer(ctx, s)` — the 1-hour cadence is re-based from the swap moment, not the previous emit moment.
- **Phone does not reply within 30 s.** `rekeyReplyTimer` fires, the callback pushes `wakeSignal{s, wakeRekeyReplyTimeout}` onto `m.wake`. `handleWake`'s `wakeRekeyReplyTimeout` arm checks `s.awaitingRekeyReply` (covering the swap-completed-before-timeout-fired race), emits a structured `noise.rekey_failed` WARN log line with `conn_id` + `close_code=4426` (NO `err=` field — anti-leakage of flynn-noise error text per the security review), and calls `closeWith(ctx, s, StatusHandshakeFailure, nil)`. The existing `closeWith` cleanup path runs: `s.state = V2StateClosed`, both per-session timers stopped + nilled, `delete(m.sessions, s.connID)`, single close-only outbound envelope emitted at WS 4426.
- **Conn closes for any other reason** (AEAD failure from [#446](../codebase/446.md), `noise_init` on an `awaitingInit` re-creation, manager shutdown). `closeWith` stops both per-session timers before the existing `delete(m.sessions, s.connID)`. `Stop()` is safe on a fired timer (returns false, no-op); the callback's `ctx.Done` arm is the load-bearing teardown (Run-derived `runCtx` cancels on Run exit, unblocking any pending callback's `m.wake` send).

**Per-session state additions on `V2Session`.** Three new fields, all owned by the dispatch goroutine: `rekeyTimer *time.Timer` (nil before initial open, nil after `closeWith`), `rekeyReplyTimer *time.Timer` (nil unless `awaitingRekeyReply` is true), `awaitingRekeyReply bool` (the canonical "are we awaiting a fresh noise_init" predicate — distinct from `rekeyReplyTimer != nil` because `Stop()` returns false on a fired timer whose callback may still be in flight, and the bool gives `handleWake`'s timeout arm a stable late-arrival predicate). No mutex — same single-owner-goroutine invariant as `s.send` / `s.recv` / `s.state` / `s.device` / `s.peerStatic`.

**Emit-side seal/marshal failures Warn-and-drop.** Same posture as `sealError`'s line 498-502: AEAD-seal failure on the emit side is realistically unreachable under correct flynn/noise; closing the conn over an internal seal failure would tear down a working session for a non-protocol error. Three distinct WARN events (`v2.rekey.emit.seal_failed`, `v2.rekey.emit.marshal_failed`, `v2.rekey.emit.skipped_already_awaiting`) carry `event` + `conn_id` only — no error text, no envelope bytes, no AEAD ciphertext. The `skipped_already_awaiting` case fires defensively if `emitRekeyRequest` is reached with `awaitingRekeyReply == true` (which should not happen — `rekeyTimer` is one-shot, only re-armed by `rekeyComplete`, which clears the bool first); surfacing it as Warn means operators see it if a future refactor introduces a re-emit bug.

**Wake delivery is blocking-send-plus-ctx-escape, not non-blocking-drop.** A dropped `wakeRekeyEmit` would cost the 1-hour cadence one beat and never re-arm (the next re-arm comes only via `rekeyComplete`); a dropped `wakeRekeyReplyTimeout` would park the session in `awaitingRekeyReply=true` forever. With `wakeBufferSize = 16` and rare fires, send-blocking is vanishingly rare; if it does happen, the callback waits microseconds for `Run` to drain one wake and exits cleanly.

**`rekeyInterval` and `rekeyReplyTimeout` are lowercase package vars, NOT exported constants.** The substitutability pattern matches `connection.go:43`'s `handshakeTimeout`; tests swap to sub-second values via `prev := rekeyInterval; rekeyInterval = 20*time.Millisecond; t.Cleanup(func() { rekeyInterval = prev })`. Production callers cannot override the 1-hour cadence — the value is a static program literal in production builds, which is the natural rate-limit against a misbehaving timer or a hostile config bump.

**No new ADR.** [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) § Re-key policy specifies the 1-hour cadence and the explicit `rekey_request` envelope; this slice implements the initiator half. No new close code is introduced — the reply-timeout teardown reuses `StatusHandshakeFailure` (4426), the same code the initial handshake and the re-key responder (#453) use for the IK-failure class.

### Operator-driven manual re-key (#462) — `Rekey` method satisfying `control.Rekeyer`

`*V2SessionManager` satisfies [`control.Rekeyer`](control-plane.md#rekey-v2-conn-re-key-trigger-seam-13d-1-459) (pinned by `var _ control.Rekeyer = (*V2SessionManager)(nil)`). The public method `Rekey(ctx context.Context, connID string) error` funnels each request onto Run's single dispatch goroutine via a new unbuffered `manualRekey chan manualRekeyReq` field; a fourth `Run` select arm dequeues the request and dispatches to a private `handleManualRekey` method that runs on the owner goroutine. The shape preserves the single-owner-goroutine invariant for `s.send` / `s.state` / `s.rekeyTimer` / `s.awaitingRekeyReply` — `Rekey` itself does only channel I/O on the caller's goroutine; every session-state read or write happens in `handleManualRekey` on `Run`'s goroutine. **No new lock, no new atomic, no new long-lived goroutine.**

```go
type manualRekeyReq struct {
    connID string
    reply  chan error // cap=1 per request; manager's send is non-blocking
}

// In V2SessionManager:
//   manualRekey chan manualRekeyReq  // unbuffered: backpressure is correct

// In Run's select:
case req := <-m.manualRekey:
    req.reply <- m.handleManualRekey(runCtx, req.connID)
```

The channel is unbuffered: backpressure is the right semantics — if `Run` is busy processing a frame, `Rekey` waits. A buffer would mislead the caller into thinking the request was accepted when in reality `Run` hadn't yet observed it. The caller's `ctx` is the escape arm in both `Rekey` select blocks (enqueue and reply). The per-request reply channel is `cap=1` so the manager's `req.reply <- err` send is non-blocking even if the caller's ctx fires between enqueue and reply. The `manualRekey` channel is NOT closed on `Run` exit (matches the existing posture for `m.cfg.Frames`); in-flight callers unblock via `ctx.Done`.

**Method-name divergence.** Ticket #462's AC text said `TriggerRekey(connID string) error`; the slice A `control.Rekeyer` interface declared `Rekey(ctx context.Context, connID string) error`. The interface signature wins because it lets `*V2SessionManager` satisfy `control.Rekeyer` directly with no adapter. The compile-time `var _ control.Rekeyer = (*V2SessionManager)(nil)` assertion pins the contract so a future refactor cannot drift the name back to the AC's informal label.

**`handleManualRekey` is the lookup + emit dispatch site.** Three reject branches, then the emit:

| Precondition | Returned sentinel |
| --- | --- |
| `m.sessions[connID]` miss | `ErrConnNotFound` (wraps `control.ErrConnNotFound`) |
| `s.state != V2StateOpen` | `ErrSessionNotOpen` |
| `s.awaitingRekeyReply` (a prior emit is in flight) | `ErrSessionNotOpen` |
| All checks pass | stop+nil `s.rekeyTimer`; call `emitRekeyRequest(ctx, s, "manual")`; return nil |

The `!= V2StateOpen` and `awaitingRekeyReply` branches return the SAME sentinel because from the operator's perspective both mean "the conn is not in a state where a manual rekey can be initiated." Distinguishing them externally would leak internal state-machine vocabulary into the operator surface with no actionable consequence. The collapse also imposes a natural per-conn rate limit: at most one manual rekey per `rekeyReplyTimeout` (30 s in production) — a second `Rekey` arriving within that window hits the awaiting-reply branch.

**`ErrConnNotFound` wraps `control.ErrConnNotFound` via `%w`.** Slice A's dispatcher uses `errors.Is(err, control.ErrConnNotFound)` (not `==`) to map to `ErrCodeConnNotFound = "conn_not_found"` on the wire. `%w` keeps that mapping firing without any further plumbing on either side. `ErrSessionNotOpen` has no wire-code analogue today — slice A defined no `ErrCodeSessionNotOpen`; the control dispatcher surfaces it through `Response.Error` verbatim with no `ErrorCode`. Sentinels live in `internal/relay` so the wire-mapping layer can import them without leaking relay-internal state-machine vocabulary into `internal/control`. The `relay → control` import is non-cyclic.

**Timer rebase.** `handleManualRekey` calls `s.rekeyTimer.Stop()` and sets `s.rekeyTimer = nil` BEFORE the emit. `Stop()`'s bool return is intentionally ignored — see "Stale-wake benign race" below. The natural [#453](../codebase/453.md) responder cycle on the phone's reply re-arms a fresh `rekeyTimer` via `rekeyComplete` from the swap moment, not the previous emit moment, so the next scheduled emit lands at T_swap + `rekeyInterval`, never at the original boundary. On reply-timeout the conn closes at WS 4426 and the session is removed entirely.

**`emitRekeyRequest` refactor.** The function signature gained a `reason string` parameter; the body deltas are mechanically minimal (the struct literal's `Reason:` field, the `Info` log's `reason` value). Call sites: `handleWake`'s `wakeRekeyEmit` arm passes `"scheduled"`; `handleManualRekey` passes `"manual"`. **One emit function, two callers — no parallel emit machinery.** The AEAD-seal posture, the awaiting-defensive skip, the marshal-failure WARN, the `awaitingRekeyReply = true` set, and the `armRekeyReplyTimer` call all run on both paths byte-identically. Mobile Protocol v2's `payload.reason = "manual"` is wire-pinned by `docs/protocol-mobile.md` § Re-key as *"operator-triggered via `pyry rekey <conn_id>`"*; the literal is the only semantic difference between the scheduled and manual emits.

**Stale-wake benign race.** Sequence: scheduled `rekeyTimer` fires at T=0, the `AfterFunc` callback pushes `wakeSignal{s, wakeRekeyEmit}` onto `m.wake` (cap 16). `Run` picks up the `manualRekey` arm first (Go's `select` is fair-random). `handleManualRekey` runs `Stop()` (returns false — fired), nils the timer, runs the manual emit (which sets `awaitingRekeyReply = true`). On a subsequent `Run` iteration the stale wake is processed: `handleWake` → `wakeRekeyEmit` arm → `emitRekeyRequest(ctx, s, "scheduled")` → the defensive `awaitingRekeyReply` check catches it and logs `v2.rekey.emit.skipped_already_awaiting`. **No double emit; one spurious WARN.** The defensive skip stays in `emitRekeyRequest` precisely so this race remains benign on the scheduled path; the manual path's explicit pre-check makes the defensive skip structurally unreachable from `handleManualRekey` (the explicit check fires first).

**AEAD-seal-failure posture: pause-not-close.** A sub-emit failure on the manual path leaves the conn with `rekeyTimer = nil` and `awaitingRekeyReply = false` — automatic scheduled re-keying is paused indefinitely until a phone-initiated re-key rebases the timer via `rekeyComplete`. Acceptable per the architect's security review: seal failures are realistically unreachable under correct flynn/noise (same posture as `sealError`); the remediation is operator-visible (`v2.rekey.emit.seal_failed` log line) and the operator can re-run `pyry rekey <conn_id>` once slice B2 lands. Re-arming the timer in the seal-failure branch would mask the underlying error AND introduce a code path the scheduled emit doesn't have — extra surface area for an unobserved failure mode.

**Control-socket wire-up of `Rekey` is out of scope.** `NewV2SessionManager` gained its first production caller in [#549](../codebase/549.md) (the `PYRY_MOBILE_V2=1` daemon cutover constructs the manager and drives `Run`), but #549 deliberately does **not** call `ctrlServer.SetRekeyer(mgr)` — that is a named non-goal. So the `Rekey` method is still reachable from `internal/relay` tests only until the control-socket wire-up lands in a separate ticket. The sibling slice B2 ships the `pyry rekey <conn_id>` operator verb in `cmd/pyry`; once both B2 and the `SetRekeyer` wire-up land on top of #549's manager construction, the verb is end-to-end functional.

### Concurrency-safe unsolicited push (#571) — `Push` method + `push` funnel

`Push(ctx context.Context, connID string, env protocol.Envelope) error` is the missing primitive behind every **server-initiated** delivery to a phone — the assistant's reply to `send_message` (today only an `ack`), and any future push. It seals a caller-owned envelope under the addressed session's send CipherState, wraps it as the existing `noise_msg` transport frame, and forwards it. It is the structural twin of [`Rekey`](#operator-driven-manual-re-key-462--rekey-method-satisfying-controlrekeyer): a new unbuffered `push chan pushReq` field + a fifth `Run` select arm funnel each request onto the single dispatch goroutine, so the lookup + seal-under-`s.send` + forward sequence runs under the single-owner-goroutine invariant. `Push` itself does only channel I/O on the caller's goroutine (a `select` send onto `m.push`, then a `select` receive on the per-request `reply chan error`, both with `ctx.Done` escape arms); every session-state read or write happens in the private `handlePush` on `Run`'s goroutine. **No new lock, no new goroutine, no new wire shape, no new exported type.**

**Why a funnel, not a mutex (the ticket's central decision).** `flynn/noise`'s `CipherState` carries a mutable 64-bit nonce counter and is **not** safe for concurrent use. A producer goroutine (the #589 assistant-turn bridge) that touched `s.send` directly would interleave with an in-flight `dispatchAppFrame` reply and reuse a nonce — an AEAD catastrophe. A mutex on `s.send` was rejected: it would contradict the package's documented no-mutex contract and have to be threaded through *every* `s.send.Encrypt` site (`dispatchAppFrame`, `emitRekeyRequest`, `sealError`, the re-key swap) — a cross-cutting refactor of the in-flight reply path. The funnel touches nothing existing: every `s.send.Encrypt` in the package already runs on `Run`, serialised by the `select`; the unbuffered `m.push` channel forces a cross-goroutine push to *wait its turn* rather than race the counter. It also composes with the re-key swap for free — `handleRekeyInit`'s `s.send, s.recv = newSend, newRecv` and `handlePush`'s `s.send.Encrypt` both run on `Run`, so a push seals fully under the old key or fully under the new, never a torn read.

**`handlePush` behaviour** (runs on the `Run` goroutine):

| Step | Result |
|---|---|
| `connID` not in `m.sessions` (unknown or torn-down — `closeWith` already `delete`d it) | return `ErrConnNotFound` |
| session exists, `s.state != V2StateOpen` | return `ErrSessionNotOpen` — **security gate**: a `handshakeComplete` session holds CipherStates but failed/skipped the token check; refusing the push keeps server output away from an un-authenticated peer |
| `json.Marshal(env)` fails | `fmt.Errorf("marshal push envelope: %w", err)` (defensive; a well-typed `message` envelope does not fail to marshal) |
| `s.send.Encrypt` / `marshalInnerFrameV2` fails | wrapped error, conn NOT closed (realistically unreachable under correct flynn/noise — same posture as `emitRekeyRequest`) |
| all pass | `m.send(RoutingEnvelope{ConnID, Frame})`; return `nil` |

A failed push never transitions the session out of `V2StateOpen` — best-effort delivery, exactly like `emitRekeyRequest`'s drop-and-stay-open posture. A transport-level drop (relay disconnected) is logged at debug inside `m.send` and returns `nil` (v1 reconnect semantics). The error paths return bare/wrapped errors and **MUST NOT** echo `env`, plaintext, ciphertext, or key bytes (the package's no-AEAD-bytes-in-logs discipline). The caller (the #589 assistant-turn bridge) owns `env` entirely (`Type`, `ID`, `TS`, `Payload`) and decides log level — `Push` is a pure transport primitive with no envelope-construction or validation policy. Both error sentinels are the same ones `Rekey` returns; `ErrConnNotFound` wraps `control.ErrConnNotFound` via `%w` so the wire-mapping `errors.Is` fires at both levels. See [`codebase/571.md`](../codebase/571.md).

**AEAD-failure teardown** (tampered / replayed / truncated `noise_msg`): `s.recv.Decrypt` returns non-nil → log `v2.aead.fail` with `conn_id` + `close_code=4421` (NO error text — the underlying flynn/noise error may carry counter indices that aren't operator-actionable) → `closeWith(ctx, s, StatusProtocolMismatch, nil)`. `closeWith` emits a single close-only routing envelope and **deletes the session entry from `m.sessions`** — the next `noise_init` for the same `conn_id` lazy-creates a fresh `awaitingInit` with no carry-over CipherStates. The handler chain is structurally unreachable: the AEAD-decrypt branch returns before `dispatchAppFrame` is called.

**Why the outbound channel is not closed.** Closing on the sending side panics any goroutine the handler accidentally forked that retains the `*dispatch.Conn`. The drain is non-blocking (`select { case env := <-outbound: ...; default: return }`); a misbehaving handler that forks a sender after `dispatchAppFrame` returns writes into a leaked but capacity-bounded channel that the GC reclaims once the goroutine exits. This is the documented synchronous-handler assumption — handlers MUST be synchronous and MUST NOT retain `*dispatch.Conn` beyond the call.

The `device` field on `V2Session` is set exactly once in the handshake token-accept branch (right before state advances to `V2StateOpen`) and is surfaced through `*dispatch.Conn.Auth()`. Same lifetime as v1's `Conn.auth` slot — revocation of the device after handshake does NOT tear down the active conn; this matches the v1 posture and is intentional. Revocation propagation for active conns is tracked as a separate concern.

The `peerStatic` field on `V2Session` (#452) is similarly set exactly once — at step 3a above, before any branch that calls `closeWith`. A token failure (or any later handshake-layer failure) tears the session down via `closeWith` and `delete(m.sessions, s.connID)`, which drops the captured field along with the session entry — a failed handshake leaves no peerStatic to compare against on a future re-key. The field is identity-bearing (the public-static of the paired peer); the doc-comment pins the **MUST NOT log** discipline so a future log-line refactor cannot relax it on the "but it's public" instinct — emitting it makes the binary log a parallel device registry for anyone with log-read access.

### Concurrency-safe open-session enumeration (#588) — `ActiveConnIDs` method + `snapshot` funnel

`ActiveConnIDs(ctx context.Context) []string` is the **enumeration** half of server-initiated fan-out: [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) can address one open session by `conn_id`, but cannot discover *which* sessions are open. `ActiveConnIDs` returns a snapshot of the `conn_id`s of every session currently in `V2StateOpen`, so a producer goroutine can fan an unsolicited frame out to all connected phones by calling `ActiveConnIDs` then `Push` per returned id. It is the v2 analog of v1's `dispatch.Dispatcher.ActiveConns()`, and the missing piece [#571](../codebase/571.md) deferred — consumed by the [#589](../codebase/589.md) assistant-turn bridge. See [`codebase/588.md`](../codebase/588.md).

It is the structural twin of `Push`/`Rekey`, the **fourth** instance of the single-writer funnel: a new unbuffered `snapshot chan snapshotReq` field + a sixth `Run` select arm route each request onto the single dispatch goroutine, where the private `handleActiveConnIDs` reads `m.sessions` under the single-owner-goroutine invariant — serialised by `Run`'s `select` against every map write (lazy-create, `delete` in `closeWith`, handshake/re-key state transitions). `ActiveConnIDs` itself does only channel I/O on the **caller's** goroutine (a `select` send onto `m.snapshot`, then a `select` receive on the per-request `reply chan []string`, both with `ctx.Done` escape arms returning `nil`); the seal/marshal steps a `Push` would run are simply absent — a snapshot touches no CipherState. No new lock, no new goroutine, no new wire shape, no new exported type beyond the method.

```go
type snapshotReq struct {
    reply chan []string // cap=1 per request; Run's reply send is non-blocking
}

// In Run's select, beside the m.push arm:
case req := <-m.snapshot:
    req.reply <- m.handleActiveConnIDs()

// handleActiveConnIDs, on the Run goroutine — the only site reading m.sessions:
out := make([]string, 0, len(m.sessions))
for connID, s := range m.sessions {
    if s.state == V2StateOpen { out = append(out, connID) }
}
return out
```

**The `s.state == V2StateOpen` filter is the load-bearing security gate** (`security-sensitive`). Only an open session has had its token validated (in `handleNoiseInit`'s accept branch); a `V2StateHandshakeComplete` session holds CipherStates but never passed the token check, and is excluded — identical to the gate [`handlePush`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) enforces, so a server push never reaches an un-authenticated peer. **Belt-and-suspenders, different fabric:** even if this filter regressed, the consumer calls `Push` per returned id and `Push` independently gates on `V2StateOpen` (`ErrSessionNotOpen` for a non-open session) — two deterministic, independent code-level checks in two methods (enumeration filter + `handlePush` gate), neither a stochastic agent rule. A `V2StateClosed` session cannot appear: `closeWith` already `delete`d it from the map.

**Returns `[]string`, not `([]string, error)`.** A snapshot has no failure mode the caller can act on; the only non-completion (ctx cancelled, or `Run` already exited with `Frames` closed and no receiver on `m.snapshot`) returns `nil` — equivalent to "no open sessions" for the broadcast consumer, which fans out to nobody this round and re-enumerates on the next assistant turn. `nil` and an empty non-nil slice are both `len 0` and interchangeable. The result is an **unordered set** (Go's randomized map-iteration order); the handler does not sort (no AC requires it; the broadcast consumer fans out order-independently — paying O(n log n) on the single dispatch goroutine would buy nothing). `handleActiveConnIDs` emits no log line and reads no secret-bearing field — the return holds only non-secret conn-id routing keys, handed only to the in-process consumer, never to a wire.

## Concurrency

**One owner goroutine + transient `time.AfterFunc` callbacks routed through a wake channel.** `Run` is the only goroutine the manager owns long-term. It reads `cfg.Frames`, looks up (or lazily creates) `m.sessions[env.ConnID]`, processes the frame synchronously, and ALSO pops `wakeSignal` values from a per-manager buffered channel `m.wake` and dispatches them via `handleWake`. `m.sessions` is mutated exclusively by `Run`; no mutex.

The #450 timer plumbing introduces transient `time.AfterFunc`-spawned goroutines (one per fire, never per session — `time.AfterFunc` only spawns when the timer fires). The callbacks DO NOT touch session state directly: they push a `wakeSignal{s, kind}` onto `m.wake` and exit. The single-owner-goroutine invariant for `s.send` / `s.recv` / `s.state` / `s.device` / `s.peerStatic` / `s.awaitingRekeyReply` / `s.rekeyTimer` / `s.rekeyReplyTimer` is structurally preserved — only `Run` reads or writes those fields. The callback closure selects on `(m.wake <- signal, <-runCtx.Done())` so a fired-but-undelivered wake on a shutting-down `Run` exits via the ctx branch without leaking; pinned by `TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak`. `Run` derives `runCtx, cancelRun := context.WithCancel(ctx); defer cancelRun()` so a `Frames`-channel-close exit (which doesn't cancel `ctx`) still cancels `runCtx` and unblocks any pending callback.

`V2Session` carries no lock. The package contract is "one goroutine per `conn_id` mutates the session"; today that goroutine is `Run` itself. flynn/noise's `CipherState` carries a mutable 64-bit nonce counter; concurrent access would be UB — the serialisation point IS the lock.

Intentionally simpler than [`internal/dispatch.Dispatcher`](dispatch-package.md), which spins one goroutine per `conn_id` to absorb handler-side latency. v2 runs handlers synchronously on the manager's single dispatch goroutine — a slow handler stalls dispatch for ALL `conn_id`s, not just the current one. The worst-case stall today is `send_message`'s 30 s `Activate` timeout. This is deliberate for the size:S surface; per-conn fan-out (one goroutine per `conn_id` with a per-session mutex guarding `s.send` / `s.recv`) is the documented production-cutover follow-up and the priority concern before flipping `cmd/pyry/relay.go` to v2.

`V2Session.State()` is a plain field read. Safe today because no cross-goroutine reads exist. Both the #571 push surface and the #588 enumeration surface deliberately keep it that way: `handlePush` reads `s.state` **on the `Run` goroutine** (funneled through `m.push`), and `handleActiveConnIDs` reads `s.state` **on the `Run` goroutine** (funneled through `m.snapshot`) — neither via a cross-goroutine `State()` call. So the broadcast/enumeration layer that this comment once anticipated (the [#589](../codebase/589.md) assistant-turn fan-out, built on #571's `Push` + #588's `ActiveConnIDs`) introduces **no** new reader of `s.state` off the owner goroutine, and `State()` still needs no `atomic.Int32`/mutex. Should a future slice read `s.state` directly from a producer goroutine *outside* the funnel, that accessor will need the atomic/mutex then — not pre-emptively refactored.

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

Re-key initiator — 1-hour timer + emit + 30 s reply timeout (#450):

- `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder` — joint coverage of AC #5 bullets 1 + 2. Substitutes `rekeyInterval = 20*time.Millisecond` / `rekeyReplyTimeout = 500*time.Millisecond`; drives to open; waits for the FIRST emit and decodes the inner envelope under `sess.initRecv` (`Type == protocol.TypeRekeyRequest`, `payload.reason == "scheduled"`); constructs a second `noise.Initiator` reusing the SAME `initPriv` and feeds a fresh `noise_init`; reads the rekey `noise_resp` via `initiator2.ReadResp` to derive `initRecv2`; waits for the SECOND emit and decodes under the post-swap `initRecv2`. State assertions after stop: `state == V2StateOpen`, `awaitingRekeyReply == true` (no second responder cycle ran).
- `TestV2Session_RekeyInitiator_ReplyTimeout_4426` — substitutes `rekeyReplyTimeout = 40*time.Millisecond`; uses `bufferLogger()`; drives to open; does NOT feed a noise_init reply; waits for the close envelope via the new `waitForOutboundCount(t, rec, n, deadline)` helper. Asserts: third outbound envelope has `CloseCode == uint16(StatusHandshakeFailure)` and nil Frame; `mgr.sessions[v2TestConnID]` absent after stop; log buffer contains `event=noise.rekey_failed`, `close_code=4426`, `conn_id=…`; the `noise.rekey_failed` line specifically does NOT contain `err=` (per-line substring check — anti-leakage of flynn-noise error text).
- `TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak` — two sub-tests: `close_via_manager_exit` (drive to open, stop immediately before the rekeyTimer fires) and `close_via_reply_timeout` (drive to open, wait for the close envelope after the reply-timeout, then stop). Both assert `runtime.NumGoroutine()` returns to within +1 of the pre-test baseline after `runtime.Gosched(); runtime.GC(); time.Sleep(20ms)`. Pins the goroutine-lifetime invariant for `time.AfterFunc` callbacks under the wake-channel design.

Operator-driven manual re-key (#462):

- `TestV2Session_RekeyManual_HappyPath_EmitsManualReason` — drives `driveToOpen`, calls `mgr.Rekey(ctx, v2TestConnID)`, waits for the second envelope (initial `noise_resp` + manual emit), AEAD-decrypts the second envelope under `sess.initRecv`, asserts `Type == protocol.TypeRekeyRequest` and `payload.reason == "manual"`, and asserts the log buffer contains `event=v2.rekey.emit` + `reason=manual` + `conn_id=<v2TestConnID>`. Substitutes long `rekeyInterval`/`rekeyReplyTimeout` (10 s each) so the scheduled boundary cannot fire during the test window.
- `TestV2Session_RekeyManual_UnknownConn_ReturnsErrConnNotFound` — manager with `Run` started but `m.sessions` empty; `mgr.Rekey(ctx, "this-conn-does-not-exist")` returns an error that satisfies BOTH `errors.Is(err, relay.ErrConnNotFound)` AND `errors.Is(err, control.ErrConnNotFound)` (the wire-mapping invariant — the `%w` wrap survives both levels). `rec.snapshot()` is empty (no outbound side-effect).
- `TestV2Session_RekeyManual_AlreadyAwaitingReply_ReturnsErrSessionNotOpen` — long `rekeyReplyTimeout` so the first emit's awaiting-reply window doesn't auto-close; first `Rekey` succeeds → wait for the manual emit to land in `rec` → second `Rekey` returns `errors.Is(err, relay.ErrSessionNotOpen)`. Asserts only ONE manual emit envelope is recorded (no double emit), session state remains `V2StateOpen`, and `s.awaitingRekeyReply` remains true.
- `TestV2Session_RekeyManual_RebasesScheduledTimer` — substitutes `rekeyInterval = 100ms` / `rekeyReplyTimeout = 1s`. Drives to open, immediately calls `Rekey`, waits for the manual emit at T ≈ small_delta and asserts `payload.reason == "manual"`. Drives a fresh responder cycle (same `initPriv` → peer-static continuity passes), captures `initRecv2`. **Original-boundary check**: at T ≈ rekeyInterval + jitter (130 ms after open — past the original scheduled boundary, well before T_rekeyComplete + rekeyInterval), asserts `rec.snapshot()` length is still 3 (manual + responder reply + nothing else). **New-boundary check**: at T_rekeyComplete + rekeyInterval + jitter, asserts a fourth envelope arrives whose payload decodes (under post-swap `initRecv2`) to `TypeRekeyRequest` with `reason == "scheduled"`. The original-boundary check is the load-bearing negative assertion — it directly pins "no stale scheduled emit lands between manual emit and the re-based scheduled boundary."

All four #462 tests are explicitly NOT `t.Parallel()` — they mutate package-level `rekeyInterval` / `rekeyReplyTimeout` vars (same posture as the other rekey tests).

Concurrency-safe unsolicited push (#571) — all `t.Parallel()`, all `-race`-clean; a new `buildMessageEnvelope(t, id, text)` helper builds the binary→phone `message` envelope (always on the test goroutine — `t.Fatalf` from a child goroutine is unsafe):

- `TestV2Session_Push_InterleavedWithReply_DecryptsUnderRace` — fires a `Push` from a separate goroutine while feeding an inbound sealed request that triggers a `dispatchAppFrame` reply; the push funnel and reply path contend for the single `Run` goroutine. Decrypts all three outbound frames (`noise_resp` + reply + push) in capture order under the phone's `initRecv` — a clean in-order decrypt is the nonce-integrity proof. The pushed frame decodes through the SAME `decryptAppFrame` path to a valid `TypeMessage` envelope (AC#1 + AC#4 — no new wire shape). Order between reply and push is nondeterministic (`Run`'s `select`); asserts presence, not order.
- `TestV2Session_Push_ConcurrentWithReplies_NoNonceCorruption` — the stress version: N=8 concurrent pushes + M=8 in-flight request/reply dispatches; all N+M outbound frames decrypt in capture order with no AEAD failure (AC#2 — the funnel serialises every `s.send.Encrypt` onto `Run`, nonce never reuses).
- `TestV2Session_Push_UnknownConn_ErrConnNotFound_OtherSessionUnaffected` — push to a never-seen `conn_id` returns an error satisfying BOTH `errors.Is(err, relay.ErrConnNotFound)` AND `errors.Is(err, control.ErrConnNotFound)`; an unrelated open session's subsequent solicited round-trip still decrypts (AC#3 — no mutation of another session's state).
- `TestV2Session_Push_NotOpen_ReturnsErrSessionNotOpen` — table-driven over `{awaiting_init, handshake_complete}`; white-box session injection with nil CipherStates (`handlePush` returns at the state check before touching `s.send`). Asserts `ErrSessionNotOpen` + zero outbound frames — the security gate against pushing to an un-authenticated peer.
- `TestV2Session_Push_ClosedSession_ReturnsErrConnNotFound` — drives an AEAD-failure 4421 teardown (flips a ciphertext byte) that deletes the session, then asserts a push to that `conn_id` collapses into `ErrConnNotFound`.
- `TestV2Session_Push_CtxCancelled_ReturnsCtxErr` — a `Push` with an already-cancelled ctx returns `ctx.Err()` without blocking; with no `Run` draining `m.push`, the `ctx.Done` arm is the only ready case (deterministic).

### E2E (`internal/e2e/relay_v2_handshake_test.go`, build tag `e2e`)

Spins up `fakerelay` (now with both `/v1/server` and `/v2/server`), wires `relay.Connect` + `V2SessionManager` **inline** (no daemon — this is the manager-in-isolation harness; the daemon-level wiring is covered separately by `relay_v2_daemon_test.go`, [#549](../codebase/549.md)), dials a `fakephone` against `/v1/client` (unchanged routing wire under v2), and drives a Noise_IK handshake from the phone side.

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

- **Production wiring of `V2SessionManager` into `cmd/pyry/relay.go`** — **landed in [#549](../codebase/549.md)** behind `PYRY_MOBILE_V2=1` (see the "Production wiring" line above). One aspect remains deferred: the per-conn fan-out below (the assistant-turn `message` fan-out landed in [#589](../codebase/589.md), next bullet).
- **Assistant-turn `message` fan-out to v2 phones — landed in [#589](../codebase/589.md).** The v2 assistant-turn bridge (`cmd/pyry/assistant_turn_v2.go`) taps the assistant/PTY output stream (the v2 analog of #311), calls [`ActiveConnIDs`](#concurrency-safe-open-session-enumeration-588--activeconnids-method--snapshot-funnel) then [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) per returned id, and is wired into `startRelayV2` under a `bridge != nil` foreground gate — so `Push` and `ActiveConnIDs` now have a production caller. The outbound envelope-ID policy is settled: the bridge mints `env.ID` from a caller-side monotonic counter, with `MessagePayload.MessageID` (a UUID) as the phone's dedup/ordering key. What genuinely remains deferred from this consumer is **live token streaming** (finished-message delivery only; pyrycode-mobile#337).
- **Per-conn fan-out for handler dispatch.** Open-state handler dispatch runs synchronously on the manager's single goroutine; a long-running handler stalls all conns. The follow-up spawns one goroutine per `conn_id` with a per-session mutex guarding `s.send` / `s.recv` (mirroring `dispatch.Dispatcher.runConn`). Priority concern before production cutover.
- Operator-facing `pyry rekey <conn_id>` verb — sibling slice B2 of #460 (re-split of #451). CLI surface for manual re-key; uses slice A's [`control.Rekey`](control-plane.md#client-helper) client helper to call `*V2SessionManager.Rekey` (shipped in #462), which reuses this slice's `emitRekeyRequest` plumbing with `payload.reason == "manual"`. The `"compromise"` value remains reserved for a future caller.
- Control-socket wiring of `(*V2SessionManager).Rekey` — still deferred. As of [#549](../codebase/549.md) the daemon constructs the manager and drives `Run` (so `NewV2SessionManager` now has a production caller), but it does **not** call `ctrlServer.SetRekeyer(mgr)`. Until that wire-up lands, the #462 `Rekey` method (hence `pyry rekey <conn_id>`) is reachable from `internal/relay` tests only.
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
- [`codebase/450.md`](../codebase/450.md) — v2 re-key initiator on binary side; 1-hour `rekeyTimer`, AEAD-sealed `rekey_request` emit under `s.send`, 30 s `rekeyReplyTimer`, `rekeyComplete` seam, wake-channel routing of `time.AfterFunc` callbacks.
- [`codebase/459.md`](../codebase/459.md) — `internal/control` rekey verb wire + `Rekeyer` interface + `control.Rekey` client helper + `control.ErrConnNotFound` sentinel; the wire contract that `(*V2SessionManager).Rekey` (#462) implements.
- [`codebase/462.md`](../codebase/462.md) — manager-side manual-rekey trigger; `Rekey` method + `manualRekey` channel + `handleManualRekey` + `emitRekeyRequest(reason)` refactor + `ErrConnNotFound`/`ErrSessionNotOpen` sentinels.
- [`codebase/549.md`](../codebase/549.md) — daemon cutover behind `PYRY_MOBILE_V2=1`; `NewV2SessionManager`'s first production caller.
- [`codebase/571.md`](../codebase/571.md) — concurrency-safe server-initiated push; `Push` method + `pushReq`/`push` channel + `handlePush`, the structural twin of the #462 rekey funnel. The primitive the [#589](../codebase/589.md) assistant-turn bridge consumes.
- [`codebase/588.md`](../codebase/588.md) — concurrency-safe open-session enumeration; `ActiveConnIDs` method + `snapshotReq`/`snapshot` channel + `handleActiveConnIDs`, the structural twin of the #571 push funnel (seal/marshal steps dropped). The enumeration half #571 deferred; with `Push` it completes the fan-out primitive the [#589](../codebase/589.md) bridge consumes.
- [`codebase/589.md`](../codebase/589.md) — the v2 assistant-turn bridge: the production consumer of `Push` + `ActiveConnIDs`, fanning finished assistant turns to every open v2 phone.
- [`features/dispatch-package.md`](dispatch-package.md) — `Route` and `NewConn` (the production-allowed counterpart to `NewTestConn`).
- [`features/relay-package.md`](relay-package.md) — the v1 surfaces of `internal/relay`.
