# 449 — `internal/relay`: v2 re-key responder path (noise_init on open conn → fresh IK + atomic CipherState swap)

## Files to read first

Each entry says **what to extract**, so the developer's turn-1 data load is complete.

- `docs/protocol-mobile.md:203-236` — **Re-key** section. Wire-level source of truth: re-key is a full IK re-run, phone-initiated; the re-key `noise_init`'s early-data is **empty**; atomic switchover; no `rekey_ack` envelope; old keys' frames after switchover fail AEAD and tear down the conn.
- `docs/protocol-mobile.md:483-505` — Threat #3 (relay operator MITM) and its residual-risk claim: *"no impersonation succeeds"*. The peer-static continuity check this spec adds (§ Design / Security) is the implementation of that claim on the re-key path. Without it, a relay MITM can re-key over an authenticated conn and assume the device snapshot.
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md:30,80,108-112` — ADR 024: re-key policy (1-hour timer + explicit `rekey_request`); per-binary static key on the responder; v2's relay-MITM mitigation is cryptographic-not-policy.
- `docs/specs/architecture/446-v2-noise-msg-application-dispatch.md` — predecessor spec. Re-use the close-codes (`StatusProtocolMismatch = 4421`, `StatusHandshakeFailure = 4426`), the `closeWith` cleanup pattern, the synchronous-handler invariant, the log policy. This slice's tampered-frame-after-rekey behaviour is **inherited from #446 entirely** — verification only, no new code path.
- `docs/knowledge/codebase/446.md` — implementation notes for #446's open-state dispatch. The "reuse `closeWith` rather than adding a parallel cleanup path" pattern repeats here; AC #2 is structural reuse of #446's AEAD-failure branch.
- `internal/relay/v2session.go:303-480` — `handleNoiseInit`. Two changes: (1) the top-of-function `if s.state != V2StateAwaitingInit` gate splits into a `switch s.state` that routes `V2StateOpen` to the new `handleRekeyInit`; (2) the existing body becomes `handleInitialHandshake` (mechanical rename, body unchanged). The token-accept branch (lines 444-479) gains one line: `s.peerStatic = s.resp.PeerStatic()` captured after `ReadInit` succeeds (added in step 3 of the initial handshake, NOT in the token-accept branch — the peer's static is known after `ReadInit`, before `WriteResp`).
- `internal/relay/v2session.go:67-85` — `V2Session` struct. Adds one field: `peerStatic []byte` (the initiator's static public key, captured at initial handshake; consulted at re-key to enforce peer continuity).
- `internal/relay/v2session.go:482-562` — `handleNoiseMsg`. Unchanged in this slice. The `V2StateOpen` branch calls `dispatchAppFrame`; the modification for control-envelope discrimination lives inside `dispatchAppFrame`, not here.
- `internal/relay/v2session.go:564-608` — `dispatchAppFrame`. One modification: before allocating the per-frame `outbound` channel and calling `dispatch.Route`, attempt to JSON-decode the plaintext as a `protocol.Envelope` and inspect `env.Type`. If `env.Type == protocol.TypeRekeyRequest`, branch to `handleRekeyRequest` and return. Otherwise (decode failure OR type is not `rekey_request`), fall through to the existing Route path unchanged. Decode failure deliberately falls through so Route's existing `protocol.malformed` reply path is preserved.
- `internal/relay/v2session.go:656-677` — `closeWith`. Unchanged in this slice; reused on re-key failure paths and on the inherited tampered-frame-after-rekey path.
- `internal/noise/noise.go:51-104` — `Responder` type, `NewResponder`, `ReadInit`, `WriteResp`. Re-key constructs a fresh `Responder` per re-run; same call sequence as initial. **Adds one new method**: `(*Responder).PeerStatic() []byte` that returns `r.hs.PeerStatic()` (callable only after `ReadInit` has succeeded — flynn's contract). The method returns a copy via `append([]byte(nil), r.hs.PeerStatic()...)` to keep flynn's internal slice from escaping to callers (mirrors the `StaticKeypair: ... Private: append(...)` defensive-copy pattern at line 80).
- `internal/protocol/codes.go:35-62` — envelope-type constants. Adds one constant: `TypeRekeyRequest = "rekey_request"`. **Do NOT add to `v1TypeSet` in `internal/protocol/envelope.go:101-118`** — `rekey_request` is a v2-only control envelope; if it leaked into `v1TypeSet`, `dispatch.Route` would treat it as a v1 application type and ask the handler chain to handle it, which is exactly what AC #3 prohibits.
- `internal/relay/v2session_test.go:407-462` — `TestV2Session_NoiseInitAfterOpen_4421`. **Must be removed.** The behaviour this test pins (second `noise_init` in open → 4421 close) is exactly what this slice intentionally changes. Replacement coverage lives in the new re-key tests; deletion keeps the test file honest about current behaviour.
- `internal/relay/v2session_test.go:736-826` — open-session test scaffolding (`openSession`, `driveToOpen`, `sealAppFrame`, `decryptAppFrame`). Reuse for re-key tests; do not duplicate.
- `internal/relay/v2session_test.go:828-1249` — open-state dispatch tests (#446). New tests mirror these patterns: `atomic.Bool` for handler-not-reached, `silentLogger()` for the default cases, custom log-capture helper for the unknown-reason WARN assertion.
- `flynn/noise@v1.1.0` — `HandshakeState.PeerStatic()` (callable after `ReadMessage` consumes IK message 1). Returns the peer's 32-byte static pubkey. The wrapper adds defensive-copy to prevent slice aliasing across the package boundary.

## Context

#446 landed application dispatch in `V2StateOpen` plus the tampered-frame teardown branch at 4421. Re-key handling was explicitly deferred (#445's and #446's "Out of scope" both cite #435/#449).

The ticket pulls a clean slice out of #435: **only the responder side** of phone-initiated re-key, plus classification of `rekey_request` as a control envelope. The binary-initiated re-key trigger (1-hour timer + emit), the 30s reply timeout, and the `pyry rekey <conn_id>` operator verb are siblings, not this slice.

Three load-bearing behaviours land here:

1. **Re-key responder.** A `noise_init` arriving on a conn that is already in `V2StateOpen` triggers a fresh IK responder run against the binary's static key. New `(send, recv)` `CipherState`s are derived. The dispatch goroutine atomically replaces `s.send` and `s.recv`; the old `CipherState` pointers are dropped (GC reclaims).
2. **Peer-pubkey continuity.** The new peer static (extracted via flynn's `HandshakeState.PeerStatic()` after `ReadInit`) is compared against `s.peerStatic` captured at initial handshake. Mismatch closes the conn at 4426. This implements the v2 threat-model claim that no impersonation succeeds through a relay MITM (`docs/protocol-mobile.md` Threat #3 § residual risk).
3. **Control envelope classification.** A decrypted application envelope with `type == "rekey_request"` is short-circuited at the dispatch boundary and never reaches `dispatch.Route`. `payload.reason` is validated against `{scheduled, manual, compromise}` — recognised values log at INFO, unknown values log at WARN and are tolerated. Receipt takes no transport action in this slice.

The AC #2 "frame sealed under OLD keys after the swap is rejected" behaviour is **inherited from #446 verbatim** — once `s.recv` points at the new `CipherState`, any old-key frame fails AEAD decrypt and hits the existing tampered-frame branch (`closeWith(StatusProtocolMismatch, nil)` + session cleanup). This slice's work for AC #2 is a test that pins the inherited behaviour against the post-swap state; no new code path.

Per the ticket § Out of scope: 1-hour timer, binary-initiated re-key emission, 30s reply timeout, operator verb, phone-side timer policy, and CipherState persistence across binary restarts stay deferred.

## Design

### Surface — additive

#### `internal/protocol/codes.go` (modified)

One new constant in the envelope-type block:

```go
// TypeRekeyRequest is the v2 control envelope (docs/protocol-mobile.md
// § Re-key) that either side may emit to nudge the peer toward initiating
// a re-key handshake. Receipt is informational; the actual re-key is a
// noise_init handshake re-run on the IK initiator's schedule.
//
// NOT a v1 application type — deliberately omitted from v1TypeSet
// (internal/protocol/envelope.go); the v2 manager intercepts it before
// dispatch.Route, so a leak into v1TypeSet would route it to the handler
// chain in violation of #449 AC #3.
TypeRekeyRequest = "rekey_request"
```

No other change to `internal/protocol`; no addition to `v1TypeSet`.

#### `internal/noise/noise.go` (modified)

One new method on `Responder`:

```go
// PeerStatic returns a copy of the initiator's static public key as
// learned from IK message 1. Callable only after ReadInit has returned
// nil; calling before is a programmer error and returns a zero-length
// slice (flynn/noise's documented contract is "an error to call before
// a handshake message containing a static key has been read"; the
// wrapper does not panic on misuse — callers that need stricter
// enforcement should track state in their own session struct).
//
// The returned slice is a fresh allocation; mutating it does not affect
// the underlying HandshakeState. Mirrors the defensive-copy posture of
// NewResponder's StaticKeypair.Private assignment.
func (r *Responder) PeerStatic() []byte
```

Body: `return append([]byte(nil), r.hs.PeerStatic()...)`.

No new field. No change to existing methods. No exposure of `Initiator.PeerStatic` (initiator already knows its peer static — it's the constructor argument).

#### `internal/relay/v2session.go` (modified)

```go
type V2Session struct {
    // ... existing fields unchanged ...

    // peerStatic is the initiator's 32-byte X25519 static public key
    // captured at initial handshake (after Responder.ReadInit succeeds).
    // Consulted by handleRekeyInit to enforce peer-pubkey continuity:
    // a re-key from a different static key (relay-MITM injection) is
    // rejected at 4426. Set exactly once in handleInitialHandshake
    // before WriteResp is called.
    //
    // SECURITY: the binary does not persist this value; it lives only
    // for the duration of the V2Session. A successful re-key does NOT
    // overwrite it — the value pins the original peer's identity for
    // the entire session lifetime.
    peerStatic []byte
}
```

```go
// handleNoiseInit routes a noise_init to either the initial handshake
// (awaitingInit → handshakeComplete → open) or the re-key responder
// (open → open with new CipherStates). A noise_init arriving in
// handshakeComplete is an out-of-state protocol violation.
func (m *V2SessionManager) handleNoiseInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
    switch s.state {
    case V2StateAwaitingInit:
        m.handleInitialHandshake(ctx, s, inner)
    case V2StateOpen:
        m.handleRekeyInit(ctx, s, inner)
    default: // V2StateHandshakeComplete (V2StateClosed is filtered earlier)
        m.cfg.Logger.Warn("relay: v2 state reject",
            "event", "v2.state.reject",
            "conn_id", s.connID,
            "close_code", int(StatusProtocolMismatch),
            "reason", "noise_init_in_handshake_complete")
        m.closeWith(ctx, s, StatusProtocolMismatch, nil)
    }
}
```

`handleInitialHandshake` is the existing body of `handleNoiseInit` minus the top-of-function `if s.state != V2StateAwaitingInit` gate, plus **one new line** capturing the peer's static after `ReadInit` succeeds (immediately following the `earlyData, err := s.resp.ReadInit(inner.Data)` block at v2session.go:335-345; before the early-data is JSON-unmarshalled into the hello envelope):

```go
// Capture the peer's static pub for re-key peer-continuity check.
s.peerStatic = s.resp.PeerStatic()
```

The rest of `handleInitialHandshake` (early-data hello decode → token validate → WriteResp → noise_resp emit → token-accept device capture → state advance to `open`) is unchanged.

#### `handleRekeyInit` — the new dispatch path

```go
// handleRekeyInit runs the responder side of a re-key handshake. Same
// shape as the initial handshake (NewResponder → ReadInit → WriteResp
// → emit noise_resp) but with three differences:
//   1. Early-data is empty on both directions (spec § Re-key line 212).
//      Hello validation and token re-check are skipped.
//   2. Peer-static continuity is enforced: the new peer's static must
//      match s.peerStatic. Mismatch → close 4426 (same code the initial
//      handshake uses for peer-static-related failure).
//   3. CipherState swap on success is atomic at the dispatch-goroutine
//      level: a single tuple assignment of (s.send, s.recv). State
//      stays V2StateOpen; s.device is preserved.
//
// Failure paths reuse the initial handshake's close-codes (4426) and
// closeWith. No new close codes (AC #4).
func (m *V2SessionManager) handleRekeyInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded)
```

Body (described as a sequence; the developer writes the imperative code):

1. **Construct responder.** `resp, err := noise.NewResponder(m.cfg.StaticPriv)`. On err: log `v2.handshake.reject.ik_failure` with `conn_id`, `close_code=4426`, `reason="rekey_responder_init_failed"`; `closeWith(StatusHandshakeFailure, nil)`; return. (Realistically unreachable — `StaticPriv` length validated at construction.)
2. **Consume IK message 1.** `_, err := resp.ReadInit(inner.Data)`. The returned early-data is discarded (spec § Re-key: empty early-data). On err: log `v2.handshake.reject.ik_failure` with `conn_id`, `close_code=4426`, `reason="rekey_read_init_failed"`; `closeWith(StatusHandshakeFailure, nil)`; return.
3. **Peer-static continuity check.** `if !bytes.Equal(resp.PeerStatic(), s.peerStatic)`: log `v2.handshake.reject.ik_failure` with `conn_id`, `close_code=4426`, `reason="rekey_peer_static_mismatch"`; `closeWith(StatusHandshakeFailure, nil)`; return. **Comparison is `bytes.Equal`**, which is variable-time but acceptable here: the comparison is between two attacker-known values (public keys are public by definition), so timing leakage carries no secret. Document this in a comment to forestall a "should be `subtle.ConstantTimeCompare`" review nit.
4. **Write IK message 2.** `respMsg, newSend, newRecv, err := resp.WriteResp(nil)` (empty early-data, mirroring the spec). On err: log `v2.handshake.reject.ik_failure` with `conn_id`, `close_code=4426`, `reason="rekey_write_resp_failed"`; `closeWith(StatusHandshakeFailure, nil)`; return.
5. **Marshal noise_resp.** `respFrame, err := marshalInnerFrameV2(protocol.TypeNoiseResp, respMsg)`. On err: log + closeWith as above with `reason="rekey_marshal_noise_resp"`.
6. **Atomic CipherState swap.** `s.send, s.recv = newSend, newRecv`. Single tuple assignment; the dispatch goroutine is the only writer (and only reader) of these fields, so atomicity is structurally guaranteed without a lock. The old `*CipherState` pointers are dropped from the struct; Go's GC reclaims the underlying memory. **Documented in the package comment**: "Old CipherStates are released by overwriting the field references on the dispatch goroutine; an explicit Wipe() on internal/noise.CipherState is not exposed (deferred — would require touching #433's surface). The single-owner-goroutine ownership of s.send/s.recv ensures no code path in the manager accesses the old state after the swap; the field-reassignment is the practical zeroisation."
7. **Log acceptance.** `m.cfg.Logger.Info("relay: v2 rekey accept", "event", "v2.rekey.accept", "conn_id", s.connID, "device_name", s.device.Name)`. (`s.device` is non-nil because re-key only runs in `V2StateOpen`, which is only entered after the token-accept branch sets `s.device`.)
8. **Emit noise_resp.** `m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})`.
9. **State stays `V2StateOpen`.** No state transition; `s.device` and `s.peerStatic` are unchanged.

The function is ≤55 LOC.

#### `dispatchAppFrame` — control-envelope discriminator

One new prefix to the existing function. The application-dispatch path (allocate outbound chan, call `dispatch.Route`, drain replies) is unchanged.

Sketch (contract, not implementation):

```go
// dispatchAppFrame ... (existing doc-comment).
//
// #449 adds a control-envelope discriminator ahead of the application
// dispatch path: a successfully-decoded envelope whose Type matches a
// known v2 control type (currently only TypeRekeyRequest) is handled
// inline and does NOT reach dispatch.Route. JSON-decode failures fall
// through to Route, which emits the existing sealed protocol.malformed
// reply — preserving #446's malformed-envelope path.
func (m *V2SessionManager) dispatchAppFrame(ctx context.Context, s *V2Session, plaintext []byte) {
    // Control-envelope check (#449).
    var probe protocol.Envelope
    if err := json.Unmarshal(plaintext, &probe); err == nil && probe.Type == protocol.TypeRekeyRequest {
        m.handleRekeyRequest(ctx, s, probe)
        return
    }
    // Existing application-dispatch path (#446) — unchanged.
    // outbound := make(...) ; conn := dispatch.NewConn(...) ; dispatch.Route(...) ; drain.
}
```

Two correctness notes:

- **`probe` is a separate local**, not reused as `env` inside the application-dispatch path, because `dispatch.Route` re-decodes the envelope from raw bytes and applies its own validation (`IsV1Compatible`, type-set check, malformed-envelope path). The probe decode is a cheap discriminator only — Route remains the single source of truth for v1-compatible application envelopes.
- **The probe decode does not consume CipherState bytes** and runs only on plaintext that has already passed `s.recv.Decrypt`. The cost is one `json.Unmarshal` per open-state frame; same magnitude as Route's own decode.

#### `handleRekeyRequest` — control envelope handling

```go
// handleRekeyRequest processes a decrypted rekey_request envelope. The
// binary is always the IK responder; the phone re-keys by sending
// noise_init directly. So receiving a rekey_request from the phone
// takes NO transport action in this slice — it is purely informational
// / forward-compat.
//
// payload.reason is validated against {scheduled, manual, compromise}:
// recognised values log at INFO under v2.rekey_request; unrecognised
// values log at WARN under v2.rekey_request.unknown_reason with the
// raw reason string echoed via a structured slog field (no string
// concatenation — slog handles attacker-controlled values safely).
//
// State stays V2StateOpen. No close; no envelope emitted.
func (m *V2SessionManager) handleRekeyRequest(ctx context.Context, s *V2Session, env protocol.Envelope)
```

Body sequence:

1. **Parse payload.** `var payload struct{ Reason string \`json:"reason"\` }; _ = json.Unmarshal(env.Payload, &payload)`. JSON-decode failure → `payload.Reason == ""`, which falls into the unknown-reason branch (treated as forward-compat).
2. **Switch on reason.** Recognised values (`"scheduled"`, `"manual"`, `"compromise"`) log at INFO with `event="v2.rekey_request"`, `conn_id=s.connID`, `reason=payload.Reason`. Default (unknown / empty) logs at WARN with `event="v2.rekey_request.unknown_reason"`, `conn_id=s.connID`, `reason=payload.Reason`.
3. **Return.** No transport action.

The function is ≤25 LOC.

### Concurrency — atomic swap invariant

The CipherState swap (`s.send, s.recv = newSend, newRecv`) executes on the manager's single dispatch goroutine. No other goroutine reads or writes these fields; no lock is needed; tuple-assignment is structurally atomic at the source level (compiler emits two stores, but no observer can interleave).

The peer-static continuity check (`bytes.Equal(resp.PeerStatic(), s.peerStatic)`) reads `s.peerStatic`, which was set in `handleInitialHandshake` (same goroutine) and never mutated again. No write-after-read race.

`s.resp` is the only field that becomes stale after re-key: it still points at the initial-handshake `Responder`. This is acceptable — `s.resp` is only consumed inside `handleInitialHandshake` and is dead state after first use; re-key uses a fresh local `Responder` and never touches `s.resp`. A follow-up cleanup that moves `s.resp` out of the struct entirely is **explicitly out of scope** (touches #445's surface without observable benefit).

**Head-of-line blocking during re-key.** The re-key handshake takes one X25519 derivation + AEAD setup (~100µs) plus one outbound noise_resp send. During this window the dispatch goroutine cannot service any other conn_id. Same posture as initial handshake — the single-goroutine fan-in posture from #445/#446 is unchanged. The per-conn fan-out follow-up that #446 names also covers this; tracked in v2-session-manager.md Open Q3.

### State-machine transition table — updated

The table from #446 changes the `noise_init` cell of the `open` column:

| Inbound on conn_id | `awaitingInit` | `handshakeComplete` | `open` (changes) | `closed` |
|---|---|---|---|---|
| `noise_init` | run handshake | close(4421), → closed | **re-key responder; swap CipherStates; state stays `open`** | drop |
| `noise_resp` | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |
| `noise_msg`, decrypts cleanly + Type `rekey_request` | n/a (no CipherStates) | n/a (no app envelope) | **handleRekeyRequest; no transport action; state stays `open`** | drop |
| `noise_msg`, decrypts cleanly + Type other | close(4421), → closed | sealed `auth.invalid_token` + close(4401), → closed | dispatch via handlers; sealed reply emitted; state stays `open` | drop |
| `noise_msg`, decrypt fails | close(4421), → closed | close(4421), → closed | close(4421), → closed (AEAD-failure teardown — inherited from #446) | drop |
| Unknown `type` / bad `v` / malformed | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |

The "decrypts cleanly" row splits into two cells in the `open` column to make the control-envelope branch explicit; the application-dispatch cell is unchanged from #446.

### Error handling

| Failure mode | Close code | AEAD-sealed error envelope? | Notes |
|---|---|---|---|
| Re-key `NewResponder` failure | 4426 | no | Realistically unreachable; static key validated at construction. `reason="rekey_responder_init_failed"`. |
| Re-key `ReadInit` MAC failure | 4426 | no | Phone presented an invalid IK message 1. `reason="rekey_read_init_failed"`. |
| **Re-key peer-static mismatch** | 4426 | no | **New finding.** The re-key initiator's static pub differs from the one captured at initial handshake. Indicates relay-MITM injection or a phone-side identity change. `reason="rekey_peer_static_mismatch"`. |
| Re-key `WriteResp` failure | 4426 | no | Realistically unreachable. `reason="rekey_write_resp_failed"`. |
| Re-key `marshalInnerFrameV2` failure | 4426 | no | Realistically unreachable. `reason="rekey_marshal_noise_resp"`. |
| Old-key `noise_msg` arriving after swap | 4421 | no | Inherited from #446's tampered-frame branch. No new code; verification only. |
| `rekey_request` with unknown `reason` | (no close) | no | WARN log only; tolerated for forward-compat (AC #3 wording). |
| `rekey_request` with empty payload / malformed JSON | (no close) | no | Falls into the unknown-reason WARN branch via `payload.Reason == ""`. |

**Key invariant:** every re-key failure mode closes at **4426** — the same code the initial handshake uses for IK-pattern failure. AC #4 is satisfied by mirroring the initial handshake's close-code posture verbatim. The test (`TestV2Session_OpenState_RekeyInit_ReadInitFails_4426`) pins the chosen code.

**Atomicity reuse.** Re-key failure paths use `closeWith(ctx, s, StatusHandshakeFailure, nil)` — close-only, no sealed error frame. Consistent with the initial handshake's IK-failure posture. The session entry is deleted from `m.sessions` by `closeWith`, so the next inbound frame on the same conn_id lazy-creates a fresh `V2StateAwaitingInit` (same structural cleanup pattern as #446's tampered-frame teardown).

### Log policy (security-load-bearing)

Extends the #445 / #446 posture. The implementation MUST adhere; code-review checks each rule against the diff.

- **MUST NOT log at any level:** AEAD plaintext, AEAD ciphertext, IK message bytes, base64 forms of any of the above, peer static pubkey bytes (`s.peerStatic` / `resp.PeerStatic()` raw), new or old CipherState internal state. Same MUST applies to slog fields and to error wrapping.
- **MUST log on re-key acceptance:** event class `v2.rekey.accept`, `conn_id`, `device_name`. Mirrors `v2.handshake.accept` from #445. The `device_name` is operator-actionable; it is the SAME device the conn was authenticated as at initial handshake (re-key preserves `s.device`).
- **MUST log on re-key failure:** event class `v2.handshake.reject.ik_failure`, `conn_id`, `close_code=4426`, `reason=<short snake-case key>` from the table above. NO error text from flynn/noise (may carry counter indices). NO peer-static bytes. NO device-name on the peer-static-mismatch branch (the re-key initiator's identity is unknown / hostile; logging the captured-at-initial-handshake device-name on a rejected re-key would create an anti-enumeration signal).
- **MUST log on `rekey_request` envelope:** INFO with event class `v2.rekey_request`, `conn_id`, `reason` (one of the recognised values). WARN with event class `v2.rekey_request.unknown_reason`, `conn_id`, `reason=<raw value>` for unrecognised values. The `reason` field carries attacker-influenced bytes — pass via structured slog field (not string concatenation) so slog's handler does the safe encoding.
- **MUST NOT log per open-state happy-path frame.** High-frequency message traffic stays out of the log channel — same posture as #446. The `v2.rekey.accept` and `v2.rekey_request` lines fire once per re-key and once per `rekey_request` respectively; both are low-frequency (1/hour expected re-key cadence).

### Testing strategy

Tests split between same-package unit (state-machine + handshake + dispatch glue, fast, no WS) and e2e (real WS via fakerelay). The unit-shape tests are the primary coverage; e2e adds end-to-end realism for the happy path only.

#### `internal/relay/v2session_test.go` — same-package unit

**Delete the existing `TestV2Session_NoiseInitAfterOpen_4421`** (lines 407-462). The behaviour it pins (second `noise_init` in open → 4421 close) is exactly what this slice changes. The replacement coverage is the re-key happy-path test below.

**New helper:** `sealRekeyRequest(t, cs, id, reason)` — AEAD-seals a `rekey_request` envelope with the given reason under `cs`, wraps as `noise_msg`, returns a `RoutingEnvelope`. Mirrors `sealAppFrame`. ~15 LOC.

**New helper:** `captureLogger(t)` — returns a `*slog.Logger` writing to a `bytes.Buffer` captured by the test, plus an accessor that returns the buffer contents as a string for substring assertion. Only used by the unknown-reason WARN test; other tests keep `silentLogger()`. ~10 LOC.

Each test below is described by its scenario; the developer writes the test in stdlib `testing` idiom matching the existing file.

- **`TestV2Session_OpenState_RekeyResponder_HappyPath`** — drive a paired-device handshake to open via `driveToOpen`. Construct a fresh `noise.Initiator` reusing the SAME `initPriv` from the initial handshake (the peer-continuity invariant requires the same static key). `WriteInit(nil)` (empty early-data per spec). Feed via `wrapInnerFrame`. Assert: ONE additional outbound envelope (count goes from 1 → 2), Frame is `noise_resp`, `CloseCode == 0`. Initiator-side: `ReadResp(respRaw)` succeeds, returns empty early-data, yields new `(initSend2, initRecv2)`. Round-trip verification: AEAD-seal an arbitrary `protocol.Envelope` (e.g. `TypeListConversations` with a stub handler) under `initSend2`, feed; expect a sealed reply that decrypts cleanly under `initRecv2`. Stop the manager and assert `mgr.sessions[v2TestConnID].state == V2StateOpen` and `mgr.sessions[v2TestConnID].device != nil` (snapshot preserved). Verifies AC #1 happy path AND the post-swap round-trip.
- **`TestV2Session_OpenState_RekeyResponder_OldKeyFrameRejected_4421`** — drive to open and re-key as above. Stash a ciphertext sealed under the OLD `initSend` (from before the re-key). After the re-key completes, feed the stale ciphertext as a `noise_msg`. Assert: ONE additional outbound envelope with `CloseCode == uint16(StatusProtocolMismatch)`, Frame nil. After stop, `mgr.sessions[v2TestConnID]` is absent. Verifies AC #2 — inherited tampered-frame branch fires against post-swap state. (This test exercises the same `closeWith` cleanup as #446's tampered-frame test, but on a re-keyed session — proving the inheritance holds across the swap.)
- **`TestV2Session_OpenState_RekeyResponder_DifferentPeerStatic_4426`** — drive to open with `initPriv` keypair A. Then construct a SECOND `noise.Initiator` with a DIFFERENT `initPriv` keypair B (`genV2Keypair(t)` again). `WriteInit(nil)` on initiator-B. Feed. Assert: ONE additional outbound envelope, `CloseCode == uint16(StatusHandshakeFailure)`, Frame nil. After stop, `mgr.sessions[v2TestConnID]` is absent (closeWith cleanup). Verifies the peer-static continuity check rejects a different-keypair re-key. **This is the security-load-bearing test** for the residual-risk-low claim of Threat #3.
- **`TestV2Session_OpenState_RekeyResponder_BadIKMessage_4426`** — drive to open. Feed a `noise_init` with random bytes as data (`make([]byte, 96)` filled by `crypto/rand`, mirroring `TestV2Session_IKReject_4426`). Assert: ONE additional outbound envelope, `CloseCode == uint16(StatusHandshakeFailure)`, Frame nil. Verifies AC #4 — re-key `ReadInit` failure uses the same close code as initial.
- **`TestV2Session_OpenState_RekeyRequest_NotRoutedToHandler`** — drive to open with a stub handler whose body sets `var handlerCalled atomic.Bool`. Use `sealRekeyRequest(t, sess.initSend, 42, "scheduled")` and feed. Assert: NO additional outbound envelope (count stays at 1). `handlerCalled.Load() == false`. `mgr.sessions[v2TestConnID].state == V2StateOpen`. Verifies AC #3 control-envelope classification (registered handler bypassed; no sealed reply emitted). Mirrors `TestV2Session_OpenState_TamperedNoiseMsg_4421`'s atomic.Bool pattern.
- **`TestV2Session_OpenState_RekeyRequest_UnknownReason_WarnNoClose`** — drive to open. Use `captureLogger(t)` instead of `silentLogger()`. Feed a `rekey_request` with `reason="surprise"`. Assert: NO outbound envelope produced beyond the initial `noise_resp`; the captured log buffer contains the substring `"v2.rekey_request.unknown_reason"` and `"reason=surprise"`. State stays `V2StateOpen`. Verifies AC #3 unknown-reason tolerance.
- **`TestV2Session_OpenState_RekeyRequest_ScheduledReason_InfoNoClose`** — companion: feed `reason="scheduled"`. Captured log contains `"v2.rekey_request"` event class with `reason=scheduled` and NOT `"unknown_reason"`. Pins the recognised-values branch.

#### `internal/dispatch/dispatch_test.go` — verify no behaviour change

No new tests. Existing tests are the regression guard for the unchanged Route surface. The control-envelope discriminator lives in `internal/relay`, not in `dispatch`.

#### `internal/e2e/relay_v2_handshake_test.go` (`//go:build e2e`)

One new subtest, added to the existing `TestRelayV2_Handshake` matrix:

- **`testV2RekeyResponderHappyPath`** — paired device, handshake to open. Phone-side: capture `initPriv` and the initial handshake's `(initSend, initRecv)`. Construct a NEW `noise.Initiator` with the SAME `initPriv` (peer continuity). `WriteInit(nil)`. Send as a `noise_init` inner frame. Read the noise_resp back; `ReadResp` returns new `(initSend2, initRecv2)`. AEAD-seal an arbitrary `list_conversations` envelope under `initSend2`, send. Read the reply; decrypt with `initRecv2`; assert envelope decode succeeds. Verifies the responder side of phone-initiated re-key end-to-end through fakerelay's WS path.

No e2e test for the peer-static-mismatch case — that's a unit-shape concern. No e2e test for the old-key-rejected case (same reasoning as #446: unit-shape pins the inherited branch on the post-swap state).

#### `internal/noise/noise_test.go`

One small test for the new `PeerStatic()` accessor:

- **`TestResponder_PeerStaticAfterReadInit`** — construct an Initiator with known static priv A and a Responder. Initiator `WriteInit([]byte("hello"))`. Responder `ReadInit(initMsg)`. Assert `bytes.Equal(responder.PeerStatic(), initiator.<pub-from-A>)`. Assert the returned slice is a defensive copy (mutate one byte; call `PeerStatic()` again; verify the new copy is unchanged).

### Wire-format and protocol changes

**None to the binary↔relay wire shape.** This slice operates entirely within the existing v2 inner-frame discriminator (`noise_init` / `noise_resp` / `noise_msg`) and consumes a v2-defined application envelope type (`rekey_request`) that the spec already names. The protocol document (`docs/protocol-mobile.md` § Re-key) is the spec; this slice is its implementation on the responder side.

### `cmd/pyry/relay.go` wiring

**Not modified in this slice.** Same posture as #446 — production daemon continues to wire the v1 dispatcher; v2 manager remains test-only. Production cutover lives in a separate slice gated on #436.

## Open questions

1. **Should re-key emit an INFO log line per acceptance?** Argues yes: parity with `v2.handshake.accept`, operationally useful to see re-key cadence in logs. Argues no: 1/hour cadence per conn × N conns could be noisy. **Decision: yes** — matches initial-handshake posture; operators expect parity. Per-conn cadence is bounded by the phone's 1-hour timer.
2. **Re-key rate limiting.** A misbehaving phone (or a relay-MITM that survives the peer-static check by reusing the original static — i.e., a leaked-Keystore scenario) could trigger excessive re-keys. The cost per re-key is ~100µs of crypto + one outbound frame. No rate limit in this slice. Acceptable because (a) the same DoS shape exists at initial handshake without rate limiting; (b) per-binary single-dispatch-goroutine fan-in naturally caps total re-key rate across all conns; (c) the per-conn fan-out follow-up (Open Q from #446) is the right place to add per-conn rate limits if needed. **Defer to a sibling defensive slice if observed.**
3. **Should `s.peerStatic` be cleared on session close?** Current design: the session entry is deleted by `closeWith` (via the map `delete`), so the `V2Session` struct becomes unreachable and `s.peerStatic` is GC-reclaimed with the struct. No explicit clear needed. **Resolved: no.**
4. **Phone-side static-key continuity from the binary's POV in v2.** This slice's peer-static check assumes the initiator presents the same static key across initial handshake and re-key — which is exactly what the phone Keystore-bound model guarantees. If a future protocol revision allows phones to rotate their own static (Keystore migration, Android backup-restore), this check breaks legitimate flows. **Out of scope.** Tracked in `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md` § Consequences as a v3 concern; flagging here for cross-reference.

## Scope self-check

Production source files modified or created (excluding tests, `*.md`, the spec):

1. `internal/protocol/codes.go` — modified (one new constant `TypeRekeyRequest`).
2. `internal/noise/noise.go` — modified (one new method `(*Responder).PeerStatic()`).
3. `internal/relay/v2session.go` — modified (one new field `V2Session.peerStatic`, refactor `handleNoiseInit` into a 2-arm switch, new `handleInitialHandshake` extracted body with one new line, new `handleRekeyInit`, new `handleRekeyRequest`, modify `dispatchAppFrame` for control-envelope discriminator).

Count: **3 production source files.** Well under the 5-file size:s ceiling.

New exported symbols:

- `protocol.TypeRekeyRequest` (constant)
- `noise.Responder.PeerStatic()` (method)

Total: **2 new exported symbols, 0 new exported types.** Well under the 5-type ceiling.

Production LOC estimate:

- `internal/protocol/codes.go`: +1 LOC (the constant) + ~5 LOC of doc-comment.
- `internal/noise/noise.go`: ~10 LOC (method + doc-comment).
- `internal/relay/v2session.go`: ~150 LOC (handleNoiseInit refactor: ~15 LOC of net diff; handleInitialHandshake: 0 LOC net (existing body, one new line); handleRekeyInit: ~55 LOC; handleRekeyRequest: ~25 LOC; dispatchAppFrame discriminator: ~10 LOC; V2Session field + doc: ~15 LOC; package-comment additions: ~20 LOC; per-reject log calls and helpers absorbed in the function bodies).

Production total: ~170 LOC. Within S (≤ ~400 lines total written work after tests).

Test LOC estimate: ~350 LOC (delete one test ~55 LOC; add seven unit tests at ~40-60 LOC each; add one noise unit test ~30 LOC; add one e2e subtest ~80 LOC; helpers ~40 LOC).

Total written work: ~520 LOC. Within S (the ~600 LOC ceiling for total written work; not a red line by itself).

Edit fan-out: zero. The new constant has no consumers yet. The new `PeerStatic()` method has exactly one caller (`handleRekeyInit`). The refactored `handleNoiseInit` has the same single caller (`handleFrame`'s `switch inner.Type` row). `dispatchAppFrame` is called from one place (`handleNoiseMsg`'s `V2StateOpen` branch).

Acceptance criteria: **5.** Within boundary.

Reject branches in the new state-machine logic (counted against the ≥10 red line): 5 in `handleRekeyInit` (NewResponder fail, ReadInit fail, peer-static mismatch, WriteResp fail, marshal fail) + 1 in `handleNoiseInit` (`handshakeComplete` rejection) + 1 in `handleRekeyRequest` (unknown reason WARN) = **7 branches**. Under 10.

Size: **S confirmed.**

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. Two boundaries are crossed by inbound bytes; both are existing:
  - `Responder.ReadInit(inner.Data)` — the IK AEAD boundary on the re-key path. Same primitive as initial handshake; same wrapper from `internal/noise` (#433). The re-key path adds the explicit peer-static continuity check immediately downstream, narrowing the "authenticated peer" boundary from "anyone with a valid IK message 1" to "the same peer as initial handshake". This implements the threat-model claim that "no impersonation succeeds" on the relay-MITM surface (Threat #3 § residual risk).
  - `s.recv.Decrypt(inner.Data)` in `dispatchAppFrame` — same AEAD boundary as #446; the control-envelope discriminator only inspects already-decrypted plaintext. No new untrusted-input source.
- **[Tokens, secrets, credentials]** No findings. The re-key handshake does NOT re-validate the device token (spec § Re-key: empty early-data). The `s.device` snapshot is preserved across re-key — same posture as #446's documented "device snapshot doesn't refresh after handshake" behaviour. Revocation propagation for active conns remains a separate ticket (v1 has the identical posture for `dispatch.Conn.auth`). **What this slice does add**: the peer-static continuity check structurally binds the post-rekey AEAD channel to the same peer identity that originally presented the validated token — without this check, a relay MITM could re-key over an authenticated session and inherit the device snapshot. The check is the implementation of the threat model's "no impersonation" claim.
- **[File operations]** N/A — this slice performs no file I/O.
- **[Subprocess / external execution]** N/A — no subprocess interaction.
- **[Cryptographic primitives]** No findings. All AEAD and DH primitives are inherited from `internal/noise` (#433, separately reviewed). The new `(*Responder).PeerStatic()` method exposes a public key (by definition, public information); returning a defensive copy prevents accidental aliasing of flynn's internal slice across the package boundary. The atomic CipherState swap is single-goroutine tuple assignment — no mixed-key window is observable. The old `*CipherState` pointers are dropped from the V2Session struct on swap; explicit Wipe() of the underlying key bytes is NOT exposed (would require touching #433's wrapper; the single-owner-goroutine ownership of s.send/s.recv ensures no code path accesses the old state after the swap, which is the practical zeroisation property — documented in the package comment). **`bytes.Equal` for the peer-static comparison is variable-time but acceptable**: both operands are public keys (the live peer's static pub from `resp.PeerStatic()` and the stored `s.peerStatic`), so timing leakage carries no secret. A comment in the comparison line forestalls a "should be `subtle.ConstantTimeCompare`" review nit.
- **[Network & I/O]** No findings, with one defensive assumption. Inbound `Data` size cap (65535 bytes decoded) is inherited from `decodeInnerFrameV2` (#445); the re-key noise_init flows through the same decoder. The `rekey_request` envelope's payload is JSON-decoded after AEAD-decrypt; the JSON decode is bounded by the same per-frame size cap. **Re-key rate limiting is NOT in this slice** — per Open Q 2, same posture as initial handshake; per-conn DoS exposure is bounded by the single dispatch goroutine's natural throughput cap. **Document as a deferred defensive concern; defer until observed.**
- **[Error messages, logs, telemetry]** No findings. The peer-static-mismatch branch deliberately omits `device_name` from its log fields (the re-key initiator's identity is unknown / hostile; logging the captured-at-initial-handshake device-name on a rejected re-key would create an anti-enumeration signal). Re-key acceptance logs `device_name` (operator-actionable). `rekey_request` envelope reason values are passed through structured slog fields (slog handles attacker-controlled values safely; no string-concatenation echo). No AEAD plaintext, ciphertext, IK message bytes, peer static raw bytes, or CipherState internal state appears in any log field at any level.
- **[Concurrency]** No findings. The CipherState swap is single-goroutine tuple assignment; no other goroutine reads/writes `s.send` / `s.recv`. The peer-static comparison reads `s.peerStatic`, set once in `handleInitialHandshake` (same goroutine) and never mutated again. No lock ordering issues — no new locks. `s.resp` is left dangling on re-key (set once in initial handshake, unused afterwards); same posture as the existing struct; cleanup is out of scope.
- **[Threat model alignment]** No findings.
  - **Threat #3 (relay operator MITM):** strengthened by the peer-static continuity check. Without it, a relay MITM that injects `noise_init` on an existing conn_id could re-key over an authenticated session and assume the device snapshot — the binary would treat the attacker's frames as the original phone's. With the check: re-key from a different static key is rejected at 4426; the original phone's session continues; no impersonation succeeds. **This slice implements the residual-risk-low claim** the threat model already makes; without it, the claim would be false on the re-key path.
  - **Threat #5 (compromised phone / leaked Keystore static):** unchanged from initial-handshake posture. A phone with a compromised static priv can re-pair, and can also re-key over an existing session (because the peer-static check passes — same key). The token-validation gate at initial handshake is the only line of defence against full phone compromise; this slice does not introduce a new compromise vector.
  - **Threat #6 (replay):** flynn's monotonic nonce on the new CipherStates resets to 0 after re-key (per Noise framework). Any replayed old-key frame fails AEAD on the new `s.recv` — inherited #446 branch. AC #2 covers this.
  - **Threat #7 (tampered frame):** same path as #446. Inherited.
  - **AC #3 invariant** ("`rekey_request` not routed through handler chain"): **structurally enforced** — the control-envelope discriminator returns BEFORE `dispatch.Route` is called; there is no code path from a `rekey_request` envelope to a handler invocation. Pinned by `TestV2Session_OpenState_RekeyRequest_NotRoutedToHandler`'s `atomic.Bool` side-effect flag.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-17
