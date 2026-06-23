# `internal/relay` V2 session manager — Noise_IK handshake + open-state dispatch

The fourth surface of `internal/relay` (alongside the v1 outbound dial in `connection.go`, the v1 first-frame auth gate in `auth.go`, and the per-envelope-type handlers under `handlers/`). Adds the binary-side per-`conn_id` state machine that completes a [Mobile Protocol v2](../../protocol-mobile.md) Noise_IK handshake, validates the device-token piggybacked in IK message 1 early-data, negotiates the phone's advertised `interactive` capability against the daemon's authoritative supported set — echoing the intersection in `hello_ack` and recording the per-conn `interactive` flag (#626), dispatches `noise_msg` frames in the `open` state through the existing handler chain (#446), intercepts v2 control envelopes (`rekey_request`, #454) at the dispatch boundary, runs the responder side of a phone-initiated re-key with peer-static continuity and atomic CipherState swap (#453), arms a per-session 1-hour timer that emits an AEAD-sealed `rekey_request` envelope and tears the conn down at WS 4426 if the phone does not reply with a fresh `noise_init` within 30 s (#450), exposes a `Rekey(ctx, connID)` method that funnels operator-driven manual re-keys onto the same emit machinery with `payload.reason = "manual"` (#462), exposes a `Push(ctx, connID, env)` method for concurrency-safe **server-initiated** delivery of an unsolicited `noise_msg` to an addressed open session — non-blocking under relay backpressure via a per-session bounded buffer + droppable-delta drop policy (#571, made non-blocking #610), exposes a capability-aware `ActiveConns(ctx)` enumeration (and its `ActiveConnIDs(ctx)` `[]string` projection) that returns a concurrency-safe snapshot of every currently-open session paired with its negotiated `interactive` flag — the enumeration half of the fan-out primitive (#588, made capability-aware in #626), intercepts the inbound `request_snapshot` control envelope at the dispatch boundary and pushes a `screen_snapshot` carrying the current claude screen rendered to plain text back to the requester (#618, `security-sensitive`), serves mid-turn-reconnect replay from a late-bound event ring — a phone advertising `hello.last_event_id` is replayed the conversation's missed tail (or sent a `resync` marker) before the live stream resumes (#647, `security-sensitive`; the caught-up watermark is clamped to the conversation's newest retained id so an out-of-range / hostile `last_event_id` cannot suppress the live stream — #663, see [Reconnect replay](#reconnect-replay-647--hellolast_event_id--ring-replay--resync)), intercepts the inbound `modal_answer` / `modal_cancel` control envelopes at the dispatch boundary and — via a consumer-declared `ModalResolver` seam — resolves a `modal_cancel` (consume the outstanding modal, route the fail-safe ESC, audit) then fans a `modal_dismissed` broadcast to every interactive-capable conn, while `modal_answer` resolves **only from a per-device-gated device** — `option_id` validated against the surfaced modal, the safe-answer keystroke routed, the terminal decision audited — and, when **no** device answers within a bounded window, arms a daemon-global deny-on-timeout that safe-denies the modal (ESC), fans the same `modal_dismissed{timeout}` broadcast, and audits `denied_timeout` (#727 seam + #717 gated answer arm + #725 deny-on-timeout, `security-sensitive`, see [Inbound modal control](#inbound-modal-control-727717--deny-on-timeout-725--modalresolver-seam--modal_dismissed-broadcast)), and refuses every out-of-state inner frame or tampered AEAD payload at the WS-close layer.

**Wire role:** the responder half of [`internal/noise`](noise-package.md)'s `Responder` / `WriteResp` API, parameterised with the binary's static X25519 private key, the device registry, an outbound `RoutingEnvelope` forwarder, and an optional `dispatch.Handler` table for open-state application dispatch.

**Production wiring:** **wired into the daemon as of [#549](../codebase/549.md)**, behind the operator switch `PYRY_MOBILE_V2=1`. With the switch set, `cmd/pyry/relay.go:startRelay` builds the `V2SessionManager` over `conn.Frames()` (via the `startRelayV2` helper) instead of the v1 `internal/dispatch.Dispatcher`, registering the **same** `dispatch.Handler` values against `V2SessionConfig.Handlers` that the v1 path passes to `Dispatcher.Register` (four as of #666: `list_conversations` / `create_conversation` / `register_push_token` / `send_message`). The static key is loaded with the same `keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(name))` pair `pyry pair` uses, so the daemon's private key derives the public key the phone pinned at pairing. With the switch unset (the default), the v1 path is byte-for-byte unchanged. The cutover is a **hard switch, no soft fallback** ([ADR 024](../decisions/024-noise-ik-mobile-e2e.md)); `pyry pair preflight` ([#436](../codebase/436.md)) is the operator's pre-flip safety check, **not** the runtime selector. See [`codebase/549.md`](../codebase/549.md) § Cutover behaviour for the switch mechanism and the load-bearing "selection MUST NOT be driven by device count" constraint.

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

type V2Session struct { /* unexported fields: connID, state, resp, send, recv, device, peerStatic, interactive (#626) */ }

func (s *V2Session) State() V2SessionState

// ScreenSnapshotter renders the daemon's live claude screen to plain text
// (#618). *supervisor.Supervisor satisfies it; declared in the consumer so
// internal/relay depends on neither internal/supervisor nor tui-driver.
type ScreenSnapshotter interface {
    ScreenSnapshot() (text string, live bool) // live==false (text "") ⇒ no child attached
}

// ModalDismissal is the wire outcome+source the manager broadcasts after a
// resolver consumes an outstanding modal (#727). modal_id is held by the manager
// already, so it is not repeated here.
type ModalDismissal struct {
    Outcome string // e.g. "cancelled" (cancel); #717 uses the answered option_id
    Source  string // closed set {remote, local, timeout}; cancel ⇒ "remote"
}

// ModalResolver resolves an inbound modal control frame (or a deny-on-timeout)
// against the daemon's outstanding-modal state (#727, extended #725). Declared in
// the consumer (beside ScreenSnapshotter) so internal/relay imports neither
// internal/supervisor, internal/modalbridge, internal/audit, nor cmd/pyry; the
// cmd/pyry resolver satisfies it. *devices.Device crosses the seam (the per-conn
// s.device) on the cancel/answer arms. All three methods run on the manager's
// single Run dispatch goroutine.
type ModalResolver interface {
    // ResolveCancel consumes modalID (registry Resolve), routes a cancel/ESC
    // keystroke, audits outcome=cancelled, and returns the dismissal to
    // broadcast with ok=true. An unknown/already-resolved id ⇒ (zero, false):
    // no keystroke, no audit, no dismissal.
    ResolveCancel(modalID string, dev *devices.Device) (ModalDismissal, bool)
    // ResolveAnswer resolves an inbound modal_answer. #727 introduced it as a
    // deferred no-op; #717 fills the gated answer arm — gate-before-consume,
    // returns the dismissal (Outcome=answered option_id) on ok=true. The manager
    // already broadcasts on ok=true, so #717 touched no manager code.
    ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (ModalDismissal, bool)
    // ResolveTimeout safe-denies an unanswered modal whose deny-on-timeout window
    // elapsed (#725): consume modalID (registry Resolve) → fail-closed ESC →
    // audit outcome=denied_timeout/source=timeout with an EMPTY device (a timeout
    // has no answering device) → return the dismissal with ok=true. Takes no
    // device (the deny is unconditional, nothing to gate). An unknown/already-
    // resolved id (an answer/cancel won the race) ⇒ (zero, false): no keystroke,
    // no audit, no dismissal — the loser path.
    ResolveTimeout(modalID string) (ModalDismissal, bool)
}

type V2SessionConfig struct {
    Frames     <-chan protocol.RoutingEnvelope         // required; closes ⇒ Run returns nil
    Outbound   func(protocol.RoutingEnvelope) error    // required; production passes (*Connection).Send
    StaticPriv []byte                                  // required; must be noise.KeyLen (32) bytes
    Devices    *devices.Registry                       // required; token-validation predicate
    ServerID   string                                  // required; surfaced into hello_ack
    Logger     *slog.Logger                            // required (panic if nil)
    Handlers   map[string]dispatch.Handler             // optional; open-state envelope-type → handler
    Snapshotter       ScreenSnapshotter                // optional (#618); nil ⇒ request_snapshot → server.binary_offline
    KnownConversation func(conversationID string) bool // optional (#618); nil ⇒ request_snapshot → conversation.not_found
    ModalResolver     ModalResolver                    // optional (#727/#725); nil ⇒ modal_answer/modal_cancel + deny-on-timeout inert no-ops
}

type V2SessionManager struct { /* unexported */ }

func NewV2SessionManager(cfg V2SessionConfig) (*V2SessionManager, error)
func (m *V2SessionManager) Run(ctx context.Context) error

// Satisfies control.Rekeyer. Operator-driven manual re-key trigger (#462).
func (m *V2SessionManager) Rekey(ctx context.Context, connID string) error

// Concurrency-safe server-initiated push (#571; non-blocking under backpressure
// #610). Enqueues a caller-owned envelope onto the addressed session's bounded
// per-session buffer and returns immediately — NEVER blocks on the relay/send
// path. Run drains the buffer (drainOnce), sealing each envelope under s.send
// in order, so a slow/stalled relay can never wedge the calling producer. Under
// pressure the event-class drop policy runs pre-seal: assistant_delta drops
// oldest; control events never drop. Returns ErrConnNotFound when no open
// session has connID (a queue exists iff V2StateOpen); ctx.Err() only if ctx is
// already cancelled at entry. A drop is not an error (returns nil + debug-logs).
func (m *V2SessionManager) Push(ctx context.Context, connID string, env protocol.Envelope) error

// Mid-turn-reconnect replay source (#647), late-bound once during relay wiring
// after the interactive emitter (which owns the eventring) is built — a
// construction-time V2SessionConfig field is not buildable because the emitter
// and manager have a circular dependency (the ring does not exist when
// NewV2SessionManager runs). Stored under pushMu (the existing leaf lock). ring
// is the emitter's per-conversation event ring; currentConv resolves the
// conversation a reconnecting conn replays for (the cmd/pyry active-conversation
// cursor as of #687 — was the supervisor's #312 cursor, which #678 emptied for
// routed turns).
// nil ring or cursor (the setter never called, or the stream off) leaves replay
// disabled — a phone advertising last_event_id then just gets the live stream.
func (m *V2SessionManager) SetReplaySource(ring *eventring.Ring, currentConv func() string)

// ActiveConn is one open v2 session in the capability-aware enumeration (#626):
// its routing conn-id and the negotiated interactive-capability decision
// recorded at handshake. Holds only non-secret routing/decision data — never a
// *V2Session, CipherState, key, or plaintext.
type ActiveConn struct {
    ConnID      string
    Interactive bool
}

// Concurrency-safe snapshot of every session currently in V2StateOpen (#588,
// made capability-aware in #626), each paired with its negotiated interactive
// flag — the enumeration half of server-initiated fan-out. The future #596
// structured-stream fan-out calls this and selects interactive vs
// non-interactive conns on the Interactive flag, then Push on each conn-id.
// Safe to call from any goroutine other than the dispatch goroutine — funneled
// onto Run so m.sessions is never read concurrently with a map write. Result is
// an unordered set; sessions still handshaking / token-unvalidated are excluded
// (the same V2StateOpen gate Push enforces, so the negotiated flag of an
// un-authenticated peer is never observable). Returns nil on ctx cancellation or
// after Run has exited — no error, since a snapshot has no failure the caller
// can act on. Capability-aware v2 analog of v1's dispatch.Dispatcher.ActiveConns().
func (m *V2SessionManager) ActiveConns(ctx context.Context) []ActiveConn

// Thin projection over ActiveConns (dropping the interactive flag), preserved
// for the capability-agnostic #589 fan-out consumer. Signature and observable
// contract (unordered set, nil on ctx cancellation, non-nil-empty on an empty
// manager) unchanged.
func (m *V2SessionManager) ActiveConnIDs(ctx context.Context) []string

// Sentinels returned by Rekey and Push. ErrConnNotFound wraps
// control.ErrConnNotFound via %w so the slice A dispatcher's errors.Is
// mapping fires unchanged.
var (
    ErrConnNotFound   error // wraps control.ErrConnNotFound
    ErrSessionNotOpen error // session not in V2StateOpen (or, for Rekey, already awaiting a rekey reply)
)
```

`NewV2SessionManager` panics on missing `Frames` or `Logger` (programmer errors, same posture as `internal/dispatch.New`); returns a wrapped error on missing `Outbound` / `Devices` / `ServerID` or on wrong-length `StaticPriv` (caller-facing config bugs). `Handlers` is optional — nil or empty means every open-state envelope falls through to a sealed `protocol.unsupported` reply via [`dispatch.Route`](dispatch-package.md). `Snapshotter` and `KnownConversation` (#618) and `ModalResolver` (#727) are also **optional and unvalidated** — leaving them nil keeps the existing construction sites compiling unchanged; a nil `KnownConversation` rejects every `request_snapshot` as `conversation.not_found` and a nil `Snapshotter` reports `server.binary_offline`, so the snapshot feature is simply unavailable, never a crash; a nil `ModalResolver` makes both modal-control frames **and** an armed deny-on-timeout (#725) inert debug-logged no-ops (the modal bridge unwired — foreground, or pre-#708 before the producer is live). `Run` blocks until `Frames` closes (returns `nil`) or `ctx` is cancelled (returns `ctx.Err()`); every per-conn session is dropped on return.

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
2. **v2 control-envelope discriminator** (#454, extended #618/#727): `json.Unmarshal(plaintext, &probeEnv)`; on decode success a `switch probeEnv.Type` intercepts control envelopes before the application chain — `TypeRekeyRequest` → `handleRekeyRequest`, `TypeRequestSnapshot` → `handleRequestSnapshot` (#618), `TypeModalCancel` → `handleModalCancel`, `TypeModalAnswer` → `handleModalAnswer` (#727) — then returns. The application handler chain is NOT consulted for any of them. Decode failures (and any other type) deliberately fall through to step 3 so `dispatch.Route`'s malformed-envelope branch emits the sealed `protocol.malformed` reply established by #446. The probe is a re-decode (`dispatch.Route` decodes the same plaintext again); the cost is one small JSON parse per application frame, well below the per-frame AEAD cost.
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

`Push(ctx context.Context, connID string, env protocol.Envelope) error` is the missing primitive behind every **server-initiated** delivery to a phone — the assistant's reply to `send_message`, the #632 structured stream, and any future push. It seals a caller-owned envelope under the addressed session's send CipherState, wraps it as the existing `noise_msg` transport frame, and forwards it. **Introduced as a synchronous funnel in [#571](../codebase/571.md); rewritten non-blocking in [#610](../codebase/610.md)** so a slow/stalled relay can never wedge the calling producer (the [#633](../codebase/633.md) JSONL drainer) — closing the ADR-025 § Backpressure open risk (decisions/025 line 220).

**The #610 reconciliation — buffer in front, seal stays single-writer.** `Push` no longer round-trips through `Run`. It enqueues the (still **unsealed**) envelope onto a per-session bounded FIFO (`m.queues[connID]`, a `pushQueue` guarded by the leaf mutex `m.pushMu`) and returns immediately; a non-blocking `m.drainCh <- struct{}{}` (cap-1, coalescing) wakes `Run`. `Run`'s `drainOnce` arm pops **one** envelope per pass and seals it via `forwardEnvelope` (the renamed `handlePush`) under the single-owner-goroutine invariant — so the seal never leaves `Run`, the nonce counter is never raced, and one slow `Outbound` interleaves with (never monopolises) `Run`'s servicing of inbound frames / `ActiveConns` / wakes. The **seal stays single-writer**; only the *enqueue* moved off `Run`. **Drop-before-seal is load-bearing:** the Noise send nonce is strictly sequential, so the queue holds unsealed envelopes and the drop policy runs **pre-seal** — dropping a *sealed* frame would gap the phone's `recv` nonce → MAC failure → 4421 close. `pushMu` is a leaf lock (taken alone, never held across an `Encrypt`/`m.send`/channel op); the `m.queues` key set is mutated only on `Run` (created at `V2StateOpen` in `handleNoiseInit`, deleted in `closeWith`), so `queue-exists ⟺ V2StateOpen`.

**Drop policy** (`pushQueue.enqueue`, under `pushMu`; `assistant_delta` is the only droppable class — the coarse `message` and every control type are never-drop):

| state at enqueue | incoming | action |
|---|---|---|
| below `pushQueueCap` (256) | any | append |
| at cap, a delta queued | delta or control | evict the **oldest** queued delta, then append (AC#2 newest text retained; AC#3 control admitted by evicting a droppable, never by dropping control) |
| at cap, all control | delta | drop the incoming delta (loss-tolerant) |
| at cap, all control | control | admit past nominal cap (documented **soft overflow** — unreachable in practice; the phone cannot drive control volume) |

`enqueue` only removes existing entries and appends at the tail → the relative order of every survivor is preserved (AC#4); each drop bumps the per-session `dropped` counter (logged at debug, no app content).

**`Push` contract** (off-`Run`, non-blocking): `ctx.Err()` short-circuit at entry (the only remaining ctx dependency); else look up the queue under `pushMu` — `ErrConnNotFound` if absent — `enqueue`, signal `drainCh`, return `nil`. A drop is **not** an error. **Error-contract change (#610):** the public `Push` now collapses "session not open" into `ErrConnNotFound` (a not-open conn has no queue); `ErrSessionNotOpen` is no longer returned by `Push` (it stays a package sentinel for `Rekey` and the Run-side forward). The **`V2StateOpen` security gate moved to `forwardEnvelope`** on the drain side — a buffered push to a conn that closed/de-authed before drain is dropped there, never delivered to an un-authenticated peer. Both production callers ([#632](../codebase/632.md), [#589](../codebase/589.md)) only debug-log the error, so the collapse is invisible. `Push` stays a pure transport primitive: the caller owns `env` entirely (`Type`, `ID`, `TS`, `Payload`), and both the drop log and `forwardEnvelope`'s error log **MUST NOT** echo `env`, plaintext, ciphertext, or key bytes — only `conn_id`, the `dropped` count, and the `env.Type` class constant (the package's no-AEAD-bytes-in-logs discipline). `ErrConnNotFound` wraps `control.ErrConnNotFound` via `%w` so the wire-mapping `errors.Is` fires at both levels. See [`codebase/610.md`](../codebase/610.md) for the full design and [`codebase/571.md`](../codebase/571.md) for the original surface.

**AEAD-failure teardown** (tampered / replayed / truncated `noise_msg`): `s.recv.Decrypt` returns non-nil → log `v2.aead.fail` with `conn_id` + `close_code=4421` (NO error text — the underlying flynn/noise error may carry counter indices that aren't operator-actionable) → `closeWith(ctx, s, StatusProtocolMismatch, nil)`. `closeWith` emits a single close-only routing envelope and **deletes the session entry from `m.sessions`** — the next `noise_init` for the same `conn_id` lazy-creates a fresh `awaitingInit` with no carry-over CipherStates. The handler chain is structurally unreachable: the AEAD-decrypt branch returns before `dispatchAppFrame` is called.

**Why the outbound channel is not closed.** Closing on the sending side panics any goroutine the handler accidentally forked that retains the `*dispatch.Conn`. The drain is non-blocking (`select { case env := <-outbound: ...; default: return }`); a misbehaving handler that forks a sender after `dispatchAppFrame` returns writes into a leaked but capacity-bounded channel that the GC reclaims once the goroutine exits. This is the documented synchronous-handler assumption — handlers MUST be synchronous and MUST NOT retain `*dispatch.Conn` beyond the call.

The `device` field on `V2Session` is set exactly once in the handshake token-accept branch (right before state advances to `V2StateOpen`) and is surfaced through `*dispatch.Conn.Auth()`. Same lifetime as v1's `Conn.auth` slot — revocation of the device after handshake does NOT tear down the active conn; this matches the v1 posture and is intentional. Revocation propagation for active conns is tracked as a separate concern.

The `peerStatic` field on `V2Session` (#452) is similarly set exactly once — at step 3a above, before any branch that calls `closeWith`. A token failure (or any later handshake-layer failure) tears the session down via `closeWith` and `delete(m.sessions, s.connID)`, which drops the captured field along with the session entry — a failed handshake leaves no peerStatic to compare against on a future re-key. The field is identity-bearing (the public-static of the paired peer); the doc-comment pins the **MUST NOT log** discipline so a future log-line refactor cannot relax it on the "but it's public" instinct — emitting it makes the binary log a parallel device registry for anyone with log-read access.

### Concurrency-safe open-session enumeration (#588) — `ActiveConnIDs` method + `snapshot` funnel

`ActiveConnIDs(ctx context.Context) []string` is the **enumeration** half of server-initiated fan-out: [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) can address one open session by `conn_id`, but cannot discover *which* sessions are open. `ActiveConnIDs` returns a snapshot of the `conn_id`s of every session currently in `V2StateOpen`, so a producer goroutine can fan an unsolicited frame out to all connected phones by calling `ActiveConnIDs` then `Push` per returned id. It is the v2 analog of v1's `dispatch.Dispatcher.ActiveConns()`, and the missing piece [#571](../codebase/571.md) deferred — consumed by the [#589](../codebase/589.md) assistant-turn bridge. See [`codebase/588.md`](../codebase/588.md).

It is the structural twin of `Push`/`Rekey`, the **fourth** instance of the single-writer funnel: a new unbuffered `snapshot chan snapshotReq` field + a sixth `Run` select arm route each request onto the single dispatch goroutine, where the private `handleActiveConns` (renamed from `handleActiveConnIDs` in #626) reads `m.sessions` under the single-owner-goroutine invariant — serialised by `Run`'s `select` against every map write (lazy-create, `delete` in `closeWith`, handshake/re-key state transitions). `ActiveConns`/`ActiveConnIDs` themselves do only channel I/O on the **caller's** goroutine (a `select` send onto `m.snapshot`, then a `select` receive on the per-request `reply chan []ActiveConn`, both with `ctx.Done` escape arms returning `nil`); the seal/marshal steps a `Push` would run are simply absent — a snapshot touches no CipherState. No new lock, no new goroutine, no new wire shape, no new exported type beyond the method.

```go
// The reply was widened from chan []string to chan []ActiveConn in #626; the
// handler below appends the negotiated interactive flag, and ActiveConnIDs
// projects back to []string. See § Capability negotiation (#626).
type snapshotReq struct {
    reply chan []ActiveConn // cap=1 per request; Run's reply send is non-blocking
}

// In Run's select, beside the m.drainCh arm:
case req := <-m.snapshot:
    req.reply <- m.handleActiveConns()

// handleActiveConns, on the Run goroutine — the only site reading m.sessions:
out := make([]ActiveConn, 0, len(m.sessions))
for connID, s := range m.sessions {
    if s.state == V2StateOpen {
        out = append(out, ActiveConn{ConnID: connID, Interactive: s.interactive})
    }
}
return out
```

**The `s.state == V2StateOpen` filter is the load-bearing security gate** (`security-sensitive`). Only an open session has had its token validated (in `handleNoiseInit`'s accept branch); a `V2StateHandshakeComplete` session holds CipherStates but never passed the token check, and is excluded — identical to the gate [`forwardEnvelope`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) enforces on the push-drain side (#610), so a server push never reaches an un-authenticated peer. **Belt-and-suspenders, different fabric:** even if this filter regressed, the consumer `Push`es per returned id — a non-open conn has no queue (`ErrConnNotFound`), and a conn that closes between enqueue and drain is caught by `forwardEnvelope`'s `V2StateOpen` re-check before sealing — two deterministic, independent code-level checks (enumeration filter + `forwardEnvelope` gate), neither a stochastic agent rule. A `V2StateClosed` session cannot appear: `closeWith` already `delete`d it from the map.

**Returns `[]string`, not `([]string, error)`.** A snapshot has no failure mode the caller can act on; the only non-completion (ctx cancelled, or `Run` already exited with `Frames` closed and no receiver on `m.snapshot`) returns `nil` — equivalent to "no open sessions" for the broadcast consumer, which fans out to nobody this round and re-enumerates on the next assistant turn. `nil` and an empty non-nil slice are both `len 0` and interchangeable. The result is an **unordered set** (Go's randomized map-iteration order); the handler does not sort (no AC requires it; the broadcast consumer fans out order-independently — paying O(n log n) on the single dispatch goroutine would buy nothing). `handleActiveConns` emits no log line and reads no secret-bearing field — the return holds only non-secret conn-id routing keys + the negotiated `interactive` bool, handed only to the in-process consumer, never to a wire.

**Widened to a capability-aware enumeration in #626.** The reply now carries `[]ActiveConn` (conn-id + the negotiated `interactive` flag), and `ActiveConnIDs` is a thin `[]string` projection over it; see the next subsection.

### Inbound screen-snapshot handler (#618) — `handleRequestSnapshot` + the render/push seam

`request_snapshot` is a v2 **control** envelope (phone → binary), intercepted in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (the same boundary `rekey_request` uses), and answered with a `screen_snapshot` (binary → phone) carrying the current claude screen rendered to plain text. It backs ADR 025's always-available, parser-independent live-view escape hatch — the floor of the safe-degradation strategy. **`security-sensitive`**: the handler accepts an inbound frame from a non-trusted party over an internet-exposed relay and returns rendered screen content; ADR 025 § Security model (line 141) deliberately keeps read-only screen viewing **outside** the per-device permission gate, but the dispatch-and-return path is the surface the spec-stage security review audited (verdict PASS). See [`codebase/618.md`](../codebase/618.md).

Three seams, smallest-blast-radius first:

- **Supervisor render seam.** `(*supervisor.Supervisor).ScreenSnapshot() (text string, live bool)` captures the live `*tuidriver.Session` under `sessMu` (mirroring `WriteUserTurn`), then renders `tuidriver.Render(sess.Snapshot(), 0, 0)` **inside the seal** — the raw VT100 bytes are consumed in the same expression and never named in pyrycode, so no claude-screen literal enters the package (`cmd/substrate-guard` stays green). `sess == nil` ⇒ `("", false)`. `0,0` selects tui-driver's 120×40 default, matching the daemon PTY's allocation in headless mode (`resizeOnce` only fires for a TTY stdin), so the render is 1:1. Total — no error path; non-blocking (a pointer read + a bounded in-memory render).
- **Consumer-declared seam.** The relay reaches the supervisor through the one-method `ScreenSnapshotter` interface (declared in the relay) and the `KnownConversation func(string) bool` closure (production: a `conversations.Registry` membership check). The relay imports neither `internal/supervisor` nor `internal/conversations` — both seams are behaviours passed in via `V2SessionConfig`, same shape as `Config.ValidateConversation`.
- **The handler.** `handleRequestSnapshot(ctx, s, env)` runs on the single `Run` dispatch goroutine. Every branch pushes exactly one reply and returns (**AC #3** — never panics, hangs, or silently drops):

| Condition | Reply | Code | Retryable |
|---|---|---|---|
| Malformed payload / empty `conversation_id` | `error` | `conversation.not_found` | false |
| Unknown / foreign `conversation_id` (**AC #4**) | `error` | `conversation.not_found` | false |
| `KnownConversation == nil` (optional seam) | `error` | `conversation.not_found` | false |
| `Snapshotter == nil` (optional seam) | `error` | `server.binary_offline` | true |
| No live session (`live == false`, **AC #3**) | `error` | `server.binary_offline` | true |
| Happy path | `screen_snapshot{conversation_id, text, ts}` | — | — |

The `conversation_id` is validated by `KnownConversation` **before any render** (AC #4): an unknown/foreign id renders nothing. A JSON decode failure is tolerated — it leaves `ConversationID == ""`, which collapses into the not-found branch; the decode error is **never echoed**. Error replies carry only a **static** message constant (`msgSnapshotConvNotFound` / `msgSnapshotOffline`), never the decode-error text or the attacker-controlled raw `conversation_id`.

**Both success and error replies go through `m.forwardEnvelope`** (the renamed `handlePush`, [#610](../codebase/610.md)) — the single existing seal-and-forward path ([#571](../codebase/571.md)), no parallel send path. The public [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) is deliberately **NOT** called here: it would enqueue the reply onto the buffered push stream (subject to the drop policy and a deferred drain pass), whereas a snapshot reply is `InReplyTo`-correlated and must seal **immediately and in-line** on this same `Run` goroutine. The envelope `ID` is a fixed non-load-bearing `1` (mirrors `emitRekeyRequest`); the phone correlates on `InReplyTo`.

**Security / log discipline (specified + tested).** The rendered screen text is sensitive and is **NEVER** logged — mirrors the coarse bridge's chunk-bytes discipline (the v1 `assistant_turn.go`; the identical v2 emitter `assistant_turn_v2.go` was removed in [#699](../codebase/699.md)). The handler logs only `conn_id`, `conversation_id` (a non-sensitive UUID), and event names (`v2.snapshot.served`, and defensive `v2.snapshot.*_err` lines). `TestV2Session_OpenState_RequestSnapshot_NeverLogsScreenText` pins the invariant with a benign non-substrate sentinel; code-review should grep the handler for any log field carrying the rendered `text`.

**Concurrency.** No new goroutine, channel, or shutdown step — the handler runs only on `Run`. `ScreenSnapshot` takes `supervisor.sessMu` (leaf, pointer read) then a bounded in-memory render; `KnownConversation` takes a `conversations.Registry` RLock (leaf, bounded). Both are leaf locks in other packages, never nested with relay state. The TOCTOU window between `KnownConversation` and `ScreenSnapshot` is benign — a session that dies in the gap yields a deterministic `server.binary_offline`, not a crash or a stale render.

**Deferred (recorded in `codebase/618.md` § Out of scope):** per-conversation screen routing — this single-bootstrap-conversation phase validates *registry membership*, so any registered id renders the one live screen; the multi-conversation phase ([#596]+) MUST tighten the render to the named conversation's session. Also deferred: resized-foreground render dimensions, eager push on `stall_detected`, and request-flood rate-limiting.

### Inbound modal control (#727/#717) + deny-on-timeout (#725) — `ModalResolver` seam + `modal_dismissed` broadcast

`modal_answer` / `modal_cancel` are v2 **control** envelopes (phone → binary),
intercepted in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route`
(the same boundary `rekey_request` / `request_snapshot` use) — there is **no**
`dispatch.Route` handler. This is the **inbound** half of the daemon-side modal
bridge: the outbound half surfaces a modal to phones ([`modal_shown` + the
outstanding-modal registry](modalbridge-package.md), #716); this slice lets a
phone *resolve* it. The seam is the foundation #717 (gated `modal_answer`) and
#725 (deny-on-timeout) layer on; #727 proves it via `modal_cancel` (dismiss =
fail-safe deny). **`security-sensitive`**: an inbound untrusted frame mutates the
modal lifecycle and fans out a broadcast on the internet-exposed relay (spec-stage
security review, verdict PASS). See [`codebase/727.md`](../codebase/727.md).

Modal control is **fire-and-broadcast, not request/reply** — there is no reply to
the caller, so no decode error or attacker-controlled byte is ever echoed back.

- **`ModalResolver` consumer seam.** The relay declares the two-method interface
  (beside `ScreenSnapshotter`) and reaches the daemon's outstanding-modal state
  through it, so `internal/relay` imports neither `internal/supervisor`,
  `internal/modalbridge`, `internal/audit`, nor `cmd/pyry`. The `cmd/pyry`
  `modalResolverV2` (`cmd/pyry/modal_resolve_v2.go`) implements it: `ResolveCancel`
  does registry `Resolve` → supervisor `SendEsc` → `audit.Log({cancelled, remote})`;
  `ResolveAnswer` is the gated answer arm (#717 — `Lookup` → fail-closed gate →
  `option_id` classification → `Resolve` consume → safe-answer keystroke → audit).
  Wired in `cmd/pyry/relay.go`'s `startRelayV2`
  over the **daemon-singleton** `modalbridge.New()` registry (the same instance
  #708 live-wires the producer into).
- **`handleModalCancel`** — nil-resolver ⇒ debug-log + return (inert). Else decode
  `ModalCancelPayload` (a decode failure is tolerated → empty `modal_id` → the
  resolver's unknown-id no-op, never echoed), `ResolveCancel(modal_id, s.device)`;
  on `ok=false` return (unknown/already-resolved id → no keystroke, no audit, no
  broadcast — **AC-4**); on `ok=true` call `broadcastModalDismissed`.
- **`handleModalAnswer`** — symmetric to `handleModalCancel`. #727 shipped this
  arm with a deferred-no-op `ResolveAnswer` (always `ok=false`); #717 filled the
  gated answer arm in the resolver impl, so the broadcast line is now live: an
  authorized `modal_answer` returns `ok=true` with `Outcome` = the answered
  `option_id`, fanning the dismissal. The manager code is unchanged — #717 touched
  only `cmd/pyry/modal_resolve_v2.go` (see [`codebase/717.md`](../codebase/717.md)).
  An ungated / forged / stale answer still returns `ok=false` (no broadcast).
- **`broadcastModalDismissed(ctx, modalID, d)`** — the **load-bearing concurrency
  fact**: it fires from inside `dispatchAppFrame`, on the single `Run` goroutine,
  and **MUST NOT call `ActiveConns`** (which funnels its request *back* onto this
  same goroutine via `m.snapshot` → **deadlock**). Instead it reads `m.sessions`
  **directly** (the `handleActiveConns` pattern), filters `s.state == V2StateOpen
  && s.interactive` (the same #607 gate `modal_shown` rides), and `Push`es a
  `modal_dismissed{modal_id, outcome, source}` per conn. `Push` is
  `Run`-goroutine-safe — it touches only `m.queues` under `pushMu` and returns
  immediately (seal+forward on a later `Run` iteration via `drainOnce`), so the
  fan-out never blocks the dispatch goroutine. One shared `time.Now().UTC()`;
  envelope `ID: 1` is non-load-bearing (the phone correlates on `modal_id`;
  `modal_dismissed` is a control event, `EventID == nil`, never in the #647 ring,
  so no per-session counter is added to `V2Session`). A per-conn `Push` error
  (ctx teardown / `ErrConnNotFound` from a raced teardown) is debug-logged with
  the transport sentinel only and the fan-out continues — payload bytes are never
  logged.

The fan-out reaches *every* interactive conn, including ones that never saw this
modal's `modal_shown`; the payload carries only the opaque `modal_id` +
`cancelled`/`remote` (no modal body), so a conn with no matching outstanding modal
just ignores it. **`TestV2Session_ModalCancel_FanOut`** drives the cancel through
the real `Frames`/`Run` loop with three heads (two interactive, one not) — an
accidental `ActiveConns` call would hang it, making the test a *structural*
no-deadlock proof — and asserts the dismissal reaches both interactive heads and
neither the non-interactive one.

#### Deny-on-timeout (#725) — fail-closed safe-deny on an unanswered modal

The fail-closed safety net: if **no** authorized device answers within a bounded
window, the daemon **safe-denies** the modal rather than leave claude blocked
forever or risk a silent grant. It reuses `broadcastModalDismissed` unchanged and
adds a third `ModalResolver` arm (`ResolveTimeout`) plus a daemon-global timer
funnelled onto `Run`. **`security-sensitive`** (a timer on the permission surface;
spec-stage security review verdict PASS). See [`codebase/725.md`](../codebase/725.md).

Unlike `modal_answer`/`modal_cancel`, a timeout is **not** an inbound frame — it
originates internally and rides a new path:

- **Arm (off `Run`).** The producer surfacer (`interactiveModalEmitterV2.Handle`,
  cmd/pyry, live in #708) calls `(*V2SessionManager).ArmModalTimeout(ctx, modalID)`
  **immediately after `reg.Record`** — before the marshal/broadcast, so a modal that
  fails to marshal, or one surfaced to **zero** interactive conns, is still denied on
  the window (claude is blocked regardless of who is watching). `ArmModalTimeout` only
  calls `time.AfterFunc(modalDenyTimeout, cb)` and touches no `Run`-owned state, so it
  is safe off the `Run` goroutine. `modalDenyTimeout` is a package var (2 min default,
  test-overridable; ADR 025 specifies "a bounded window" but no number).
- **Funnel (`AfterFunc` callback → `Run`).** `cb` does
  `select { case m.modalTimeout <- modalID: case <-ctx.Done(): }` — the `armRekeyTimer`
  callback shape. `modalTimeout` is a **daemon-global** buffered (`wakeBufferSize`=16)
  channel keyed by `modal_id` (unlike `wake`, keyed by `*V2Session` — a modal is not
  bound to one conn). The `*time.Timer` is **deliberately discarded, never `Stop`ped**:
  the registry's one-shot `Resolve` is the idempotency gate, so a timer that fires after
  an answer/cancel already consumed the modal simply no-ops; an un-fired `AfterFunc`
  parks no goroutine, so leaving it un-`Stop`ped leaks nothing (avoids a
  `map[modalID]*time.Timer` + new lock for zero correctness gain).
- **Fire (on `Run`).** A new `Run` select arm
  `case modalID := <-m.modalTimeout: m.handleModalTimeout(runCtx, modalID)`.
  `handleModalTimeout` is a near-copy of `handleModalCancel`: nil-resolver ⇒ inert
  debug-log; else `ResolveTimeout(modalID)`; on `ok=false` return (already
  answered/cancelled — no keystroke, no audit, no broadcast); on `ok=true` call
  `broadcastModalDismissed` (the **same** fan-out, with `{denied_timeout, timeout}`).
- **`ResolveTimeout`** (cmd/pyry `modalResolverV2`) mirrors `ResolveCancel`: registry
  `Resolve` → best-effort `SendEsc` → `audit.Log({denied_timeout, timeout})` → return
  `{denied_timeout, timeout}, true`. Differences: **no device** (empty audit identity —
  the documented no-device-timeout case), and `denied_timeout`/`timeout` classification.

**Exactly-once is structural, not lock-defended.** Answer/cancel-resolution
(`handleModalAnswer`/`handleModalCancel`, via `m.cfg.Frames`) and timeout-resolution
(`handleModalTimeout`, via `m.modalTimeout`) are **both arms of the same `Run`
`select`** — serviced one at a time. Whichever `Run` services first consumes the modal
via the one-shot `Resolve`; the loser sees `ok=false` and no-ops. So an answer-vs-timeout
race cannot double-deny, double-broadcast, or double-audit; the registry mutex still
guards `Record` (surfacer goroutine) against `Resolve` (`Run`). The timeout leg **only
ever drives the deny keystroke**, never a grant — fail-closed by construction (ADR 025
§ Security model: "answered with the SAFE default (deny / ESC) … Never auto-grant").
**Production-inert until #708** live-wires the surfacer (nothing `Record`s a modal, so no
timer arms). `TestV2Session_ModalTimeout_FanOut` proves the off-`Run`-arm → on-`Run`-fire
crossing under `-race`.

### Capability negotiation on the handshake (#626) — `negotiateCapabilities` + `s.interactive` + capability-aware `ActiveConns`

The daemon-side **trust decision** [#607](../codebase/607.md) deferred. #607 landed the wire vocabulary (`CapabilityInteractive`, the `omitempty` `Capabilities []string` field on both `HelloClientPayload` and `HelloAckPayload`) but `handleNoiseInit` **decoded the phone's advertised capabilities and ignored them** — the `hello_ack` echoed nothing, the session recorded no capability state, and `ActiveConnIDs` returned every open conn with no capability filter. This slice closes that boundary; it is `security-sensitive` (the daemon deciding which internet-facing phones are *granted* the interactive capability) and is the enforcement half of ADR 025's deliberately split design: #607 = vocabulary (no trust), #626 = trust decision. See [`codebase/626.md`](../codebase/626.md).

**The authoritative supported set + the pure intersection.** The supported set is the daemon's own constant — **never** a mirror of the phone's claims:

```go
var supportedV2Capabilities = []string{protocol.CapabilityInteractive}

// advertised ∩ supportedV2Capabilities, in supported-set order. Iterates the
// SUPPORTED set (not the advertised one), so the result is a subset of supported
// by construction: dedups, drops the unsupported/spoofed, and yields nil for
// advertise-nothing / only-unsupported.
func negotiateCapabilities(advertised []string) []string {
    var out []string
    for _, name := range supportedV2Capabilities { // `name`, not `cap` (builtin)
        if slices.Contains(advertised, name) { out = append(out, name) }
    }
    return out
}
```

**Iterating the supported set (not the advertised set) is the security primitive** — "a spoofed capability can never be granted" is a structural property of the loop shape, deterministic, not a runtime guard a later refactor could bypass (Threat 1 / AC#3). A pure receiver-less function, directly table-testable for the whole negotiation matrix.

**Echo + record, single source of truth.** `negotiated := negotiateCapabilities(helloPayload.Capabilities)` is computed **before** the `hello_ack` literal, and `Capabilities: negotiated` is added to the existing `HelloAckPayload{…}`. The ack is sealed via `WriteResp` **before** the token check, so it is built on *every* handshake — `omitempty` keeps the key absent for a no-capability phone (v1 byte-stability, AC#5). The per-conn `interactive bool` (a new `V2Session` field beside `device`/`peerStatic`, same set-once / single-owner-goroutine discipline) is set in the token-OK tail, **between `s.device = &device` and `s.state = V2StateOpen`**, via `s.interactive = slices.Contains(negotiated, protocol.CapabilityInteractive)` — derived from the *same* `negotiated` slice the ack echoed, so the echoed capability and the recorded flag can never disagree.

**Fail-closed, two independent gates.** The flag is written **only** on the token-OK branch (after `Devices.Validate`, before `V2StateOpen`); every other path leaves it at its `false` zero value (advertise-nothing / `null` / `[]` / only-unsupported → `negotiated == nil` → `false`). And `handleActiveConns` filters on `V2StateOpen` — so even if the flag were mis-set, a non-open (un-authenticated) session is never enumerated, and the negotiated flag of an un-authenticated peer is never observable. Belt-and-suspenders of different fabric — two deterministic code-level gates, the same shape #588/#589 established. Re-key (`handleRekeyInit`) preserves `s.interactive` by never touching it, like `device`/`peerStatic`.

**Token-fail ack echo grants nothing.** On a token-fail handshake the `noise_resp` carries the ack (with `negotiated`) to a cryptographically-authenticated-but-unauthorized peer that is then closed at 4401 and deleted from `m.sessions`. The echo grants nothing (the session never opens, is never enumerated or pushed-to) and leaks nothing (`CapabilityInteractive` is public protocol vocabulary). Restructuring to build the ack after the token check would break the pinned "state → `HandshakeComplete` before token validation" invariant for zero security gain — accepted non-issue (spec § Security review, verdict PASS).

**`ActiveConns`/`ActiveConn` — the capability-aware enumeration.** The downstream consumer reads the negotiated flag via the [widened snapshot funnel](#concurrency-safe-open-session-enumeration-588--activeconnids-method--snapshot-funnel): `ActiveConns(ctx) []ActiveConn` returns each open conn paired with its `Interactive` flag, and `ActiveConnIDs` is a thin `[]string` projection (with an explicit `nil` in → `nil` out short-circuit that preserves #588's nil-on-cancel contract). The #596 structured-stream fan-out (since [#699](../codebase/699.md) the **single** v2 assistant-turn delivery path — #589's coarse `message` broadcast, re-targeted to non-interactive conns in [#634](../codebase/634.md), was deleted as dead code once the 2026-06-22 ADR 025 amendment made every phone `interactive`; the `Interactive` flag survives because the structured path still gates on it) consumes this: [`#632`](../codebase/632.md)'s `interactiveTurnEmitterV2` (`cmd/pyry/interactive_turn_v2.go`) snapshots `ActiveConns`, filters `Interactive == true` (the load-bearing capability gate, `if !c.Interactive { continue }`), then `Push`es a sealed structured envelope per conn-id — the emitter is built and unit-tested, with #633 wiring it to the live producer. [`#657`](../codebase/657.md)'s `sessionTransitionEmitterV2` (`cmd/pyry/session_transition_v2.go`) is the **second** interactive-only consumer of this exact primitive: it reuses the same `interactiveBroadcaster` interface and `if !c.Interactive { continue }` gate to fan a `session_transition` envelope (a `/clear` rotation or idle/cap eviction surfaced by [#659]'s pool observer) to interactive phones — but stamps **no** `EventID` (a session boundary is not a turn-stream event, so it does not join the #647 ring) and adds no manager-side path. The single-`Interactive`-bool shape is the right shape while `supportedV2Capabilities` has one member (YAGNI); a second capability is a deliberate, separately-reviewed change.

### Reconnect replay (#647) — `hello.last_event_id` → ring replay / resync

> **Note (#663).** #647 shipped (PR #651, merged 2026-06-08) with a code-review
> MUST FIX outstanding: the caught-up branch of `replayMissed` set the dedup
> watermark from the *untrusted* `last_event_id`, silently suppressing the live
> stream after a `/clear`-rotated reconnect or a hostile-large id.
> [#663](../codebase/663.md) resolved it — the caught-up watermark is now clamped
> to `min(afterID, NewestID(convID))`, so the behaviour below is the shipped
> guarantee. (Defect history: [codebase/647.md](../codebase/647.md) § Known issue.)

The inbound **consumer** of mid-turn replay (ADR 025 § Backpressure / replay). It
closes the loop opened by the [#646](../codebase/646.md) event ring (the replay
source) and [#649](../codebase/649.md)'s `event_id` on the outbound wire (the
position a phone learns). A phone
that reconnects mid-turn advertises the last durable `event_id` it saw as
`hello.last_event_id` (`HelloClientPayload.LastEventID *uint64`, omitempty); the
manager replays the missed tail on that conn **before** the live stream resumes,
or emits a `resync` marker if the position aged out of the bounded ring.

- **The replay source is late-bound, not a config field.** `emitter` ↔ `manager`
  is a construction cycle (the emitter takes the manager as its broadcaster; the
  replay path needs the emitter-owned `eventring.Ring`, created *inside* the
  emitter constructor — [#646](../codebase/646.md)). `SetReplaySource(ring, currentConv)` publishes
  the ring + the `func() string` conversation cursor to the manager once during
  wiring, after the emitter exists. As of [#687](../codebase/687.md) the cursor
  is the `cmd/pyry` active-conversation signal (`active.CurrentConversation`), not
  `sup.CurrentConversation` (the #312 bootstrap cursor) — #678 routes turns to
  bound-session supervisors, leaving the bootstrap cursor empty, so the replay
  path re-keys to the same active-conversation signal as the live emitter or it
  re-introduces the empty-cursor drop on the reconnect-replay path. Stored under
  the existing `pushMu` leaf lock; nil ⇒ replay disabled. One call site, in
  [`startInteractiveTurnStreamV2`](../codebase/633.md). (This is the inbound
  mirror of #646's "emitter-owns-the-ring retires the constructor cascade".)
  Note `cursor()` here runs on the **manager's** `Run` goroutine — a distinct
  goroutine from the live emitter's reader; the holder's mutex makes that safe.
- **`replayMissed` runs inline on `Run`, at the `handleNoiseInit` success tail.**
  After `noise_resp` is sent, `state == V2StateOpen`, and the push queue exists,
  the hook fires iff `helloPayload.LastEventID != nil`. It reads `(ring, cursor)`
  under `pushMu`, resolves `convID := cursor()` (returns early on nil source or
  empty cursor), then classifies via [`eventring.Ring.After`](eventring-package.md):
  - **replay** `(events, false)` → forward each event ascending via
    `forwardEnvelope` (the inline `handleRequestSnapshot` reply pattern, bypassing
    the buffered push stream), each carrying its original `EventID`, sealed under
    the fresh session keys. Because `Run` is single-threaded, the whole replay
    completes before `Run` services the live `drainCh` — the structural guarantee
    behind "before the live stream resumes".
  - **caught-up** `(nil, false)` → no replay frames; the watermark is clamped to
    `min(afterID, NewestID(convID))` (#663, read before `After`) so an
    out-of-range / hostile `last_event_id` cannot mute the subsequent live stream.
  - **gap** `(nil, true)` → `emitResync` forwards one `resync` marker
    (`TypeResync`, inline `{conversation_id}` payload, no `EventID`), never a
    partial gap-ful replay.
- **`replayThrough` per-conn watermark + `forwardEnvelope` guard.** A Run-owned
  `replayThrough uint64` on `V2Session` records the highest `event_id` delivered
  by replay; `forwardEnvelope` drops a live structured envelope whose
  `EventID <= replayThrough`, deterministically de-duplicating the transient
  replay/live overlap (a proven race, not speculative — see [codebase/647.md](../codebase/647.md)
  § Concurrency model). Envelopes with `EventID == nil` (snapshot, error, rekey,
  resync) are never dropped; conns that never advertised `last_event_id` keep
  `replayThrough == 0` and live ids are ≥ 1, so the guard is inert for them. The
  watermark is "different fabric" from the phone's own `event_id` dedup (defence
  in depth). The watermark is only ever set to a *real* ring id: the replay loop
  advances it per forwarded frame, and the caught-up branch clamps it to
  `min(afterID, NewestID(convID))` (#663) — a remote `last_event_id` beyond the
  conversation's id space can never raise it above the newest retained id.
- **Untrusted input.** `last_event_id` is range/shape-validated by the `*uint64`
  decode (a non-integer fails `HelloClientPayload` decode → existing 4421 close),
  bounded by `MaxEventsPerConversation`, and scoped to the daemon-resolved
  `convID` — a phone can never name another conversation (AC-5; pinned by the
  cursor→B / ring-holds-A test). The replay hook sits *after* Noise IK auth + the
  device-token check, so content is only ever served to an authenticated conn.

## Concurrency

**One owner goroutine + transient `time.AfterFunc` callbacks routed through a wake channel.** `Run` is the only goroutine the manager owns long-term. It reads `cfg.Frames`, looks up (or lazily creates) `m.sessions[env.ConnID]`, processes the frame synchronously, and ALSO pops `wakeSignal` values from a per-manager buffered channel `m.wake` and dispatches them via `handleWake`. `m.sessions` is mutated exclusively by `Run`; no mutex.

The #450 timer plumbing introduces transient `time.AfterFunc`-spawned goroutines (one per fire, never per session — `time.AfterFunc` only spawns when the timer fires). The callbacks DO NOT touch session state directly: they push a `wakeSignal{s, kind}` onto `m.wake` and exit. The single-owner-goroutine invariant for `s.send` / `s.recv` / `s.state` / `s.device` / `s.peerStatic` / `s.interactive` / `s.awaitingRekeyReply` / `s.rekeyTimer` / `s.rekeyReplyTimer` is structurally preserved — only `Run` reads or writes those fields. The callback closure selects on `(m.wake <- signal, <-runCtx.Done())` so a fired-but-undelivered wake on a shutting-down `Run` exits via the ctx branch without leaking; pinned by `TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak`. `Run` derives `runCtx, cancelRun := context.WithCancel(ctx); defer cancelRun()` so a `Frames`-channel-close exit (which doesn't cancel `ctx`) still cancels `runCtx` and unblocks any pending callback.

`V2Session` carries no lock. The package contract is "one goroutine per `conn_id` mutates the session"; today that goroutine is `Run` itself. flynn/noise's `CipherState` carries a mutable 64-bit nonce counter; concurrent access would be UB — the serialisation point IS the lock.

Intentionally simpler than [`internal/dispatch.Dispatcher`](dispatch-package.md), which spins one goroutine per `conn_id` to absorb handler-side latency. v2 runs handlers synchronously on the manager's single dispatch goroutine — a slow handler stalls dispatch for ALL `conn_id`s, not just the current one. (Since #721 `send_message` is no longer the worst case: it now enqueues non-blocking and acks, so its `Activate`/`WriteUserTurn` blocking moved off the dispatch goroutine onto the daemon's `msgqueue` drain — see [features/msgqueue-package.md](msgqueue-package.md).) This is deliberate for the size:S surface; per-conn fan-out (one goroutine per `conn_id` with a per-session mutex guarding `s.send` / `s.recv`) is the documented production-cutover follow-up and the priority concern before flipping `cmd/pyry/relay.go` to v2.

`V2Session.State()` is a plain field read. Safe today because no cross-goroutine reads exist. Both the push surface (#571, rewritten #610) and the #588 enumeration surface deliberately keep it that way: the #610 `Push` reads only `m.queues` under `pushMu` (never `s.state`), while `forwardEnvelope` reads `s.state` **on the `Run` goroutine** during the drain, and `handleActiveConns` reads `s.state` (and `s.interactive`, #626) **on the `Run` goroutine** (funneled through `m.snapshot`) — neither via a cross-goroutine `State()` call. So the broadcast/enumeration layer that this comment once anticipated (the [#589](../codebase/589.md) assistant-turn fan-out, built on #571's `Push` + #588's `ActiveConnIDs`) introduces **no** new reader of `s.state` off the owner goroutine, and `State()` still needs no `atomic.Int32`/mutex. Should a future slice read `s.state` directly from a producer goroutine *outside* the funnel, that accessor will need the atomic/mutex then — not pre-emptively refactored.

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

Server-initiated push (#571; backpressure + drop policy #610) — all `t.Parallel()`, all `-race`-clean; the `buildMessageEnvelope(t, id, text)` helper builds the binary→phone `message` envelope (always on the test goroutine — `t.Fatalf` from a child goroutine is unsafe):

- `TestV2Session_Push_InterleavedWithReply_DecryptsUnderRace` — fires a `Push` from a separate goroutine while feeding an inbound sealed request that triggers a `dispatchAppFrame` reply; the push drain and reply path contend for the single `Run` goroutine. Decrypts all three outbound frames (`noise_resp` + reply + push) in capture order under the phone's `initRecv` — a clean in-order decrypt is the nonce-integrity proof. The pushed frame decodes through the SAME `decryptAppFrame` path to a valid `TypeMessage` envelope (AC#1 + AC#4 — no new wire shape). Order between reply and push is nondeterministic (`Run`'s `select`); asserts presence, not order. **Passes unchanged across the #610 buffered-enqueue rewrite** (`waitForEnvelopes` polls; every seal still happens on `Run` in FIFO order).
- `TestV2Session_Push_ConcurrentWithReplies_NoNonceCorruption` — the stress version: N=8 concurrent pushes + M=8 in-flight request/reply dispatches; all N+M outbound frames decrypt in capture order with no AEAD failure (AC#2 — the drain serialises every `s.send.Encrypt` onto `Run`, nonce never reuses). Also passes unchanged post-#610 (8 pushes are well under `pushQueueCap`, so none drop).
- `TestV2Session_Push_UnknownConn_ErrConnNotFound_OtherSessionUnaffected` — push to a never-seen `conn_id` returns an error satisfying BOTH `errors.Is(err, relay.ErrConnNotFound)` AND `errors.Is(err, control.ErrConnNotFound)`; an unrelated open session's subsequent solicited round-trip still decrypts (AC#3 — no mutation of another session's state).
- `TestV2Session_Push_NotOpen_ReturnsErrConnNotFound` (renamed from `…ReturnsErrSessionNotOpen` in #610) — a white-box non-open session has no queue, so `Push` now returns `ErrConnNotFound`. The companion `TestV2Session_forwardEnvelope_NotOpen_GateRefuses` pins the drain-side `V2StateOpen` security gate that moved to `forwardEnvelope`. (Pre-#610 this asserted `ErrSessionNotOpen` via `handlePush`'s state check.)
- `TestV2Session_Push_ClosedSession_ReturnsErrConnNotFound` — drives an AEAD-failure 4421 teardown (flips a ciphertext byte) that deletes the session (and its queue), then asserts a push to that `conn_id` collapses into `ErrConnNotFound`.
- `TestV2Session_Push_CtxCancelled_ReturnsCtxErr` — a `Push` with an already-cancelled ctx returns `ctx.Err()` without blocking; `Push` checks `ctx.Err()` before consulting `m.queues` (#610), so a cancelled ctx short-circuits deterministically.

#610 backpressure tests (added): the drop policy is a pure unit surface — `TestPushQueue_Enqueue_*` (helpers `pqEnv` / `fillDeltas` / `assertQueue`) cover under-cap retention, drop-oldest-delta (AC#2), control-evicts-delta (AC#3), `message`-is-never-drop, control-never-dropped-when-deltas-present, order-preserved-across-drops (AC#4), and the all-control soft overflow. The end-to-end non-blocking guarantee (AC#1) is `TestV2Session_Push_NonBlockingUnderStall`: a stalling outbound double wedges the `Run` forward, every `Push` still returns within a tight deadline, the drop counter engages past `pushQueueCap`, and after release the survivors decrypt **in order** under the phone's `recv` state (proving drop-before-seal left no nonce gap). See [`codebase/610.md`](../codebase/610.md).

Capability negotiation (#626) — `buildHelloEarlyDataCaps` / `driveToOpenCaps` variants carry the advertised set without changing the `buildHelloEarlyData` (5 callers) / `driveToOpen` (30 callers) signatures; the handshake tests capture the hello_ack early-data (which `driveToOpen` discards) via `Initiator.ReadResp` → decode `Envelope` → `HelloAckPayload`:

- `TestNegotiateCapabilities` — table-driven AC#2/#3 matrix for the pure function: `[interactive]`→`[interactive]`; `[interactive, unsupported]`→`[interactive]` (drop); `[unsupported]`→`nil` (spoof); `nil`/`[]`→`nil` (advertise-nothing); `[interactive, interactive]`→`[interactive]` (dedup). `t.Parallel()`.
- `TestV2Session_Handshake_CapabilityNegotiation` — table-driven handshake-level: advertise `[interactive]` → ack echoes `[interactive]`; advertise nothing → ack has no `capabilities` key; spoof `[interactive, god-mode]` → ack echoes only `[interactive]`; `[god-mode]` only → ack has no capabilities.
- `TestV2Session_ActiveConns_MixedInteractive` — two open conns (one interactive, one not) → `ActiveConns` reports the correct flag per conn (white-box injection mirroring `TestV2Session_ActiveConnIDs_OpenOnly`).
- `TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated` — the **security** test: a phone advertising `[interactive]` but failing the token is closed at 4401 and never appears in `ActiveConns`; the negotiated flag is never observable for an unauthenticated peer.

The pre-existing `TestV2Session_ActiveConnIDs_*` suite (`OpenOnly`, `TornDownSessionAbsent`, `ConcurrentWithDispatch_RaceClean`, `EmptyManager`, `CtxCancelled_ReturnsNil`) passes **unchanged** — the `[]string` projection preserves #588's contract (AC#5).

Reconnect replay (#647) — `internal/relay/v2session_replay_test.go` (new): each drives a real Noise handshake whose hello carries `last_event_id` against a manager whose `SetReplaySource` was given a hand-populated `eventring.Ring` + a stub cursor, decrypts the forwarded frames, and asserts. `TestV2Session_Reconnect_ReplaysMissedTail` (3,4,5 ascending before any live frame), `…_CaughtUp_NoReplay`, `…_Gap_EmitsResync` (one `resync` with `conversation_id`), `…_AbsentLastEventID_NoReplay`, `…_ScopedToCursorConversation` (cursor→B, ring holds A → zero A events, AC-5), `…_ReplayDisabled_NoReplay` (nil ring), `…_OtherConnsUnaffected`, `…_ForwardEnvelope_ReplayWatermarkGuard` (drops `EventID ≤ replayThrough`, forwards above, never drops `EventID == nil`). The #647 caught-up/out-of-range tests asserted only *no replay frames* and missed that the live stream was muted afterward; **#663 closes that gap** — `…_OutOfRangeLastEventID_LiveStreamDelivered` (incl. `math.MaxUint64`), `…_ClearRotation_LiveStreamDelivered`, and `…_SameConversation_DedupPreserved` push a live frame after the caught-up handshake and assert **delivery** (see [codebase/663.md](../codebase/663.md)).

Inbound modal control (#727) — `internal/relay/v2session_modal_test.go` (new): a fake `ModalResolver` (records calls; canned `ModalDismissal{cancelled,remote}` with `ok=true` for the configured cancel id) + a conn-aware handshake helper (`openModalConn`) that stands up ≥2 interactive heads on one manager and recovers each conn's `noise_resp` from the shared `v2Recorder` by `ConnID`. `TestV2Session_ModalCancel_FanOut` (three heads — two interactive, one not; asserts `ResolveCancel` routed once with the right `modal_id` + per-conn device, `modal_dismissed{cancelled,remote}` decrypts at **both** interactive heads, **zero** noise_msg at the non-interactive one — AC-1/AC-2; running through the real `Frames`/`Run` loop is the structural no-deadlock proof), `…_ModalAnswer_NoOp` (routed through the seam, no dismissal — AC-3), `…_ModalCancel_UnknownID_NoOp` (resolver `ok=false` → no dismissal — AC-4), `…_ModalControl_NilResolver` (both frames inert debug-logged no-ops, session stays `V2StateOpen`). The resolver-side assertions (consume + keystroke + audit + no-body-leak) live in `cmd/pyry/modal_resolve_v2_test.go` (the relay can't import supervisor/audit/registry). See [codebase/727.md](../codebase/727.md).

Deny-on-timeout (#725) — same `v2session_modal_test.go` (`fakeModalResolver` extended with `ResolveTimeout`; `modalDenyTimeout` shrunk via save/restore): `TestV2Session_ModalTimeout_FanOut` arms a timeout, lets the window elapse with no answer, and asserts `ResolveTimeout` fired exactly once and `modal_dismissed{denied_timeout,timeout}` reaches both interactive heads but not the non-interactive one — the off-`Run`-arm (`AfterFunc`) → on-`Run`-fire (`handleModalTimeout`) crossing is the `-race` proof; `…_ModalTimeout_AlreadyResolved_NoBroadcast` (resolver `ok=false` ⇒ no dismissal — AC-2 loser path); `…_ModalTimeout_NilResolver` (armed timeout firing is inert, no panic). The resolver-side `ResolveTimeout` assertions (one ESC, single `denied_timeout` audit with **empty** device + no modal body, the already-consumed/unknown/keystroke-error paths) live in `cmd/pyry/modal_resolve_v2_test.go`; the surfacer arms exactly one timeout per surfaced modal in `cmd/pyry/interactive_modal_v2_test.go` (`fakeArmer`). See [codebase/725.md](../codebase/725.md).

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
- **Assistant-turn `message` fan-out to v2 phones — landed in [#589](../codebase/589.md).** The v2 assistant-turn bridge (`cmd/pyry/assistant_turn_v2.go`) taps the assistant/PTY output stream (the v2 analog of #311), calls [`ActiveConnIDs`](#concurrency-safe-open-session-enumeration-588--activeconnids-method--snapshot-funnel) then [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) per returned id, and is wired into `startRelayV2` under a `bridge != nil` foreground gate — so `Push` and `ActiveConnIDs` now have a production caller. The outbound envelope-ID policy is settled: the bridge mints `env.ID` from a caller-side monotonic counter, with `MessagePayload.MessageID` (a UUID) as the phone's dedup/ordering key. **Re-targeted in [#634](../codebase/634.md), then removed in [#699](../codebase/699.md):** #634 made the bridge consume `ActiveConns` and skip `c.Interactive` conns (coarse → non-interactive only), then #699 deleted `cmd/pyry/assistant_turn_v2.go` entirely — once the 2026-06-22 ADR 025 amendment made every phone `interactive`, its non-interactive delivery branch was unreachable. The structured stream (#632/#633) is now the **sole** v2 assistant-turn path, and keeps `Push` / `ActiveConns` as their production callers. **Live token streaming** remains the genuinely deferred piece (pyrycode-mobile#337). The out-of-scope v1 / dispatch-leg coarse bridge (`cmd/pyry/assistant_turn.go`) still produces `message`, so the wire constant stays.
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
- [`internal/protocol`](protocol-package.md) — `Envelope`, `RoutingEnvelope`, `HelloClientPayload`, `HelloAckPayload`, `ErrorPayload`, `InnerFrameV2`, `V2Version`, `TypeNoise*` constants, the `Token` field on `HelloClientPayload`, and (#618) `RequestSnapshotPayload` / `ScreenSnapshotPayload` + `TypeRequestSnapshot` / `TypeScreenSnapshot` / `CodeConversationNotFound` / `CodeServerBinaryOffline` (the #617 snapshot vocabulary), and (#727) `ModalCancelPayload` / `ModalAnswerPayload` / `ModalDismissedPayload` + `TypeModalCancel` / `TypeModalAnswer` / `TypeModalDismissed` (the #701 modal vocabulary).
- [`internal/eventring`](eventring-package.md) (#646, consumed #647) — the bounded per-conversation event ring; the manager reads `Ring.After(convID, afterID)` (self-synchronised) on the reconnect-replay path. Late-bound via `SetReplaySource`, never imported at construction.
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
- [`codebase/571.md`](../codebase/571.md) — concurrency-safe server-initiated push; the original `Push` method + `pushReq`/`push` channel + `handlePush`, the structural twin of the #462 rekey funnel. The primitive the [#589](../codebase/589.md) assistant-turn bridge consumes.
- [`codebase/610.md`](../codebase/610.md) — backpressure + droppable-delta drop policy (`security-sensitive`); the #571 synchronous funnel rewritten non-blocking. Removes `pushReq`/`push`, adds `pushMu`/`queues`/`drainCh` + the `pushQueue` bounded FIFO + `drainOnce`; renames `handlePush`→`forwardEnvelope`. Closes the ADR-025 line-220 open risk that a slow relay wedges the [#633](../codebase/633.md) producer.
- [`codebase/588.md`](../codebase/588.md) — concurrency-safe open-session enumeration; `ActiveConnIDs` method + `snapshotReq`/`snapshot` channel + `handleActiveConnIDs` (renamed to `handleActiveConns` in #626), the structural twin of the #571 push funnel (seal/marshal steps dropped). The enumeration half #571 deferred; with `Push` it completes the fan-out primitive the [#589](../codebase/589.md) bridge consumes.
- [`codebase/589.md`](../codebase/589.md) — the v2 assistant-turn bridge: the production consumer of `Push` + `ActiveConnIDs`, fanning finished assistant turns to every open v2 phone.
- [`codebase/618.md`](../codebase/618.md) — the inbound screen-snapshot handler (`security-sensitive`); `ScreenSnapshotter` interface, the two optional `V2SessionConfig` seams, the `dispatchAppFrame` snapshot arm, `handleRequestSnapshot` + `snapshotReplyError`, and the `(*supervisor.Supervisor).ScreenSnapshot` render seam. Reuses `forwardEnvelope` (renamed from `handlePush` in #610), never the public `Push`.
- [`codebase/617.md`](../codebase/617.md) — the screen-snapshot wire vocabulary (`request_snapshot` / `screen_snapshot` payloads + `Type` constants) #618 consumes.
- [`codebase/727.md`](../codebase/727.md) — the inbound modal-control interception (`security-sensitive`); the consumer-declared `ModalResolver` seam + `ModalDismissal`, the optional `V2SessionConfig.ModalResolver` field, the two `dispatchAppFrame` arms (`handleModalCancel` / `handleModalAnswer`), and the `broadcastModalDismissed` fan-out (reads `m.sessions` directly, never `ActiveConns` — deadlock). `modal_cancel` resolves (consume → ESC → audit → broadcast); `modal_answer` shipped a deferred no-op there, since filled by the #717 gated answer arm (see [`codebase/717.md`](../codebase/717.md)). The `cmd/pyry` `modalResolverV2` impl consumes the #716 registry (`Resolve`), the #726 `SendEsc` seam, and the #712 audit sink. See also [`features/modalbridge-package.md`](modalbridge-package.md) (the outbound `modal_shown` half).
- [`codebase/647.md`](../codebase/647.md) — the inbound mid-turn reconnect-replay consumer (`security-sensitive`); `SetReplaySource` (late-bound ring), `replayMissed`/`emitResync`, the `handleNoiseInit` hook, the `replayThrough` watermark + `forwardEnvelope` guard, and `HelloClientPayload.LastEventID` / `TypeResync`. Shipped with a caught-up-watermark MUST FIX outstanding, resolved by [`codebase/663.md`](../codebase/663.md) (clamp to `min(afterID, NewestID(convID))`). Consumes the [`codebase/646.md`](../codebase/646.md) ring + [`codebase/649.md`](../codebase/649.md) outbound `event_id`.
- [`codebase/626.md`](../codebase/626.md) — capability negotiation on the handshake (`security-sensitive`); `supportedV2Capabilities` + `negotiateCapabilities`, the `hello_ack` echo, the `s.interactive` flag, and the capability-aware `ActiveConns`/`ActiveConn` enumeration (`ActiveConnIDs` becomes a projection). The daemon-side trust decision [#607](../codebase/607.md) deferred.
- [`codebase/607.md`](../codebase/607.md) — the v2 interactive wire vocabulary (`CapabilityInteractive`, the `Capabilities []string` fields) that #626 enforces.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § Safe degradation (the parser-independent snapshot floor) and § Security model (line 141: read-only screen viewing outside the per-device permission gate).
- [`features/dispatch-package.md`](dispatch-package.md) — `Route` and `NewConn` (the production-allowed counterpart to `NewTestConn`).
- [`features/relay-package.md`](relay-package.md) — the v1 surfaces of `internal/relay`.
