# 453 — `internal/relay`: v2 re-key responder swap on open conn (`handleRekeyInit` + atomic CipherState swap + peer-static continuity check)

## Files to read first

Each entry says **what to extract**, so the developer's turn-1 data load is complete.

- `internal/relay/v2session.go:317-499` — `handleNoiseInit`. Two changes: (1) the top-of-function `if s.state != V2StateAwaitingInit` gate becomes a `switch s.state` that routes `V2StateOpen` to the new `handleRekeyInit`; the existing `awaitingInit`-only body stays in place inside the `case V2StateAwaitingInit:` arm. The `handshakeComplete` and `default` arms keep the existing 4421 reject path verbatim (a `noise_init` arriving while we hold uncommitted CipherStates is the same state-machine violation it is today). (2) **Nothing else in `handleNoiseInit` moves.** The peer-static capture at line 364 (`s.peerStatic = s.resp.PeerStatic()`) already landed in #452 and is the source the re-key check reads from.
- `internal/relay/v2session.go:67-99` — `V2Session` struct. `peerStatic []byte` already exists (added in #452); read its SECURITY doc-comment — the lifetime contract ("a successful re-key MUST NOT overwrite this value") is load-bearing for the new code.
- `internal/relay/v2session.go:160-175` — package-level concurrency comment on `V2SessionManager` ("Run is the only goroutine the manager owns"). The atomic-swap invariant the new code relies on is *structural*, not lock-based — the spec amends this comment block to name `s.send` / `s.recv` ownership and the field-reassignment zeroisation choice.
- `internal/relay/v2session.go:560-580` — `handleNoiseMsg`'s `V2StateOpen` branch. **No code change in this slice.** Re-read it because AC #4 (old-key frame after swap → 4421) is *verification only* — that branch is the inherited tampered-frame teardown the new test asserts against the post-swap state.
- `internal/relay/v2session.go:730-751` — `closeWith`. Re-used unchanged on every re-key failure path. The session-removal half (`delete(m.sessions, s.connID)` on close) is what makes AC #4's "session absent" assertion hold without new cleanup code.
- `internal/relay/v2session.go:716-728` — `marshalInnerFrameV2`. Re-used to wrap the new responder's outbound `noise_resp`; same shape as initial.
- `internal/noise/noise.go:106-141` — `(*Responder).PeerStatic()`, `WriteResp`. Re-key constructs a fresh `Responder` per re-run (`NewResponder` + `ReadInit` + `WriteResp`) and consumes the same `(send, recv)` pair as initial; the symmetric-collapse at lines 136-140 is the load-bearing detail that makes the swap a one-liner.
- `internal/relay/v2session_test.go:60-69, 217-256, 740-828` — test helpers: `silentLogger`, `genV2Keypair`, `startManager` / `waitForEnvelopes` / `wrapInnerFrame` / `decodeRespFrame` / `decodeNoiseMsg`, `openSession` struct, `driveToOpen`, `sealAppFrame`, `decryptAppFrame`. The three new tests reuse these verbatim — do not duplicate.
- `internal/relay/v2session_test.go:409-464` — `TestV2Session_NoiseInitAfterOpen_4421`. **Delete this test.** It pins the behaviour this slice intentionally changes (second `noise_init` in `open` → 4421 close). Replacement coverage is the three new tests below.
- `internal/relay/v2session_test.go:830-915` — `TestV2Session_OpenState_EncryptedRoundTrip` and its setup. The re-key happy-path test mirrors this shape (drive to open, do a round-trip, but with a re-key in between).
- `docs/protocol-mobile.md` § Re-key (lines around 203-236) — re-key is a full IK re-run, phone-initiated; the re-key `noise_init`'s early-data is **empty**; atomic switchover; no `rekey_ack` envelope; old-key frames after switchover fail AEAD.
- `docs/protocol-mobile.md` § Threat #3 (around lines 483-505) — "no impersonation succeeds" residual-risk claim. The peer-static continuity check this slice adds is the implementation of that claim on the re-key path.
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md` § Re-key policy — per-binary static key on the responder; cryptographic-not-policy mitigation for the relay-operator surface.
- `docs/knowledge/codebase/446.md` — `closeWith` cleanup pattern reuse + the AEAD-failure teardown branch that AC #4 inherits verbatim. The "reuse `closeWith` rather than adding a parallel cleanup path" pattern applies here too.
- `docs/knowledge/codebase/452.md` — predecessor slice (peer-static capture); the field this slice reads and the lifetime contract it must honour.
- `docs/knowledge/codebase/454.md` — sibling slice (`rekey_request` control-envelope discriminator). Independent — neither calls the other. Re-key is the IK re-run; `rekey_request` is a nudge envelope.
- `docs/specs/architecture/449-v2-rekey-responder.md` on `feature/449` (orphan branch, closed `NOT_PLANNED`) — the parent super-slice's spec. Contains the design notes for the responder swap *verbatim*; this slice ships exactly that design with the peer-static capture (#452) and `rekey_request` discriminator (#454) carved off. **Lift the design decisions verbatim; do not re-derive.** Same orphan-spec pattern as #452 / #454.

## Context

`#452` (peer-static capture) and `#454` (`rekey_request` discriminator) both landed already. Looking at `internal/relay/v2session.go` today:

- `V2Session.peerStatic []byte` is captured in `handleNoiseInit` at line 364 after `ReadInit` succeeds.
- `dispatchAppFrame` already routes `TypeRekeyRequest` envelopes to `handleRekeyRequest` (logs-only, no transport action).
- The `noise_init`-on-open path is **still rejected at 4421** by lines 321-331 of `handleNoiseInit` — exactly the behaviour this slice changes.

This slice closes the loop. When a `noise_init` arrives for a `conn_id` already in `V2StateOpen`, route to a new `handleRekeyInit` that:

1. Runs IK responder again against the binary's static key (`NewResponder` → `ReadInit` → `WriteResp`).
2. Enforces peer-static continuity: `bytes.Equal(newResp.PeerStatic(), s.peerStatic)`; mismatch closes at 4426.
3. Atomically swaps `(s.send, s.recv)` with the freshly-derived pair in a single tuple assignment on the single dispatch goroutine.
4. Drops the old `*CipherState` pointers from the struct; Go's GC reclaims the underlying memory. No explicit `Wipe()` (would touch #433's surface — deferred).

State stays `V2StateOpen`. `s.device` and `s.peerStatic` are preserved unchanged. The re-key `noise_init`'s early-data is empty per spec; hello validation and token re-check do not run (they ran at initial handshake; the peer-static continuity check is the per-rekey identity gate).

AC #4 (old-key frame after swap → 4421 + cleanup) is **verification only** — the existing `handleNoiseMsg` open-state AEAD-failure branch from #446 already does this work. Once `s.recv` points at the new `CipherState`, any frame sealed under the old keys fails `s.recv.Decrypt` and lands in `closeWith(StatusProtocolMismatch, nil)` → session removal. No new code path; the test pins the inheritance against the post-swap state.

This slice is **blocked by #452** (peer-static capture) — that landed. Sibling #454 (`rekey_request` discriminator) is independent; neither slice references the other in code.

## Design

### Surface — modify `handleNoiseInit`, add `handleRekeyInit`

#### `handleNoiseInit` — top-of-function state switch

The existing top-of-function gate at lines 321-331 becomes a `switch s.state` that routes each state to its handler. The `awaitingInit` arm wraps the existing handshake body (current lines 333-499, unchanged); the new `open` arm calls `handleRekeyInit`; `handshakeComplete` and `default` keep the existing 4421 reject path.

```go
func (m *V2SessionManager) handleNoiseInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
    switch s.state {
    case V2StateAwaitingInit:
        // (existing handshake body lines 333-499, unchanged)
    case V2StateOpen:
        m.handleRekeyInit(ctx, s, inner)
    default: // V2StateHandshakeComplete; V2StateClosed is filtered earlier in handleFrame
        m.cfg.Logger.Warn("relay: v2 state reject",
            "event", "v2.state.reject",
            "conn_id", s.connID,
            "close_code", int(StatusProtocolMismatch),
            "reason", "noise_init_in_handshake_complete")
        m.closeWith(ctx, s, StatusProtocolMismatch, nil)
    }
}
```

Two correctness notes for the developer:

- The existing `awaitingInit` body has multiple early-`return` paths; wrap the entire block under the `case` arm. The `s.resp = resp` / `s.peerStatic = s.resp.PeerStatic()` / token-validation flow does not move.
- The current "noise_init_after_handshake" log reason (line 328) is replaced by the new `noise_init_in_handshake_complete` reason in the default arm. The open-state path no longer rejects, so the `noise_init_after_handshake` label is dropped entirely. Update the existing `TestV2Session_HandshakeCompleteRejects` (if any) to match — `grep` for the reason string to confirm no other test pins it.

#### `handleRekeyInit` — new function (~55 LOC)

Signature and contract:

```go
// handleRekeyInit runs the responder side of a phone-initiated re-key
// handshake. Same shape as the initial handshake (NewResponder → ReadInit
// → WriteResp → emit noise_resp) with three differences:
//
//  1. Early-data is empty on both directions (spec § Re-key). Hello
//     validation and token re-check are skipped — they ran at initial
//     handshake. The per-rekey identity gate is the peer-static
//     continuity check, not the token.
//  2. Peer-static continuity is enforced: the new initiator's static pub
//     (from resp.PeerStatic() after ReadInit) MUST equal s.peerStatic
//     captured at initial handshake. Mismatch closes at 4426, the same
//     code the initial handshake uses for IK-related failure (AC #2).
//  3. CipherState swap on success: a SINGLE tuple assignment
//     `s.send, s.recv = newSend, newRecv` on the manager's single
//     dispatch goroutine. No half-mixed state where one direction uses
//     new keys and the other uses old. State stays V2StateOpen; s.device
//     and s.peerStatic are preserved. The old *CipherState pointers are
//     dropped from the struct; Go's GC reclaims them. Explicit Wipe() of
//     the underlying key bytes is NOT exposed — would require touching
//     #433's surface, deferred.
//
// Failure paths reuse the initial handshake's close code (4426) and
// closeWith primitive. No new close code is introduced (AC #3).
func (m *V2SessionManager) handleRekeyInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded)
```

Body — described as a sequence; the developer writes the imperative code. The failure-mode → log-reason → close-code mapping is the spec's contract; the developer implements each branch as a structured slog `Warn` followed by `closeWith(ctx, s, StatusHandshakeFailure, nil)` and `return`.

1. **Construct responder.** `resp, err := noise.NewResponder(m.cfg.StaticPriv)`. On err: log `reason="rekey_responder_init_failed"`; close 4426; return. (Realistically unreachable — `StaticPriv` length is validated at `NewV2SessionManager`.)
2. **Consume IK message 1.** `_, err := resp.ReadInit(inner.Data)`. The returned early-data is discarded (spec § Re-key: empty early-data). On err: log `reason="rekey_read_init_failed"`; close 4426; return.
3. **Peer-static continuity check.** `if !bytes.Equal(resp.PeerStatic(), s.peerStatic)`: log `reason="rekey_peer_static_mismatch"`; close 4426; return. **`bytes.Equal` is intentional and variable-time-acceptable** — both operands are public keys (the live peer's pub from `resp.PeerStatic()` and the stored `s.peerStatic`), so timing leakage carries no secret. Add a one-line code comment naming this choice to forestall a "should be `subtle.ConstantTimeCompare`" review nit (the orphan #449 spec and #452.md both pre-empt this; document inline anyway).
4. **Write IK message 2.** `respMsg, newSend, newRecv, err := resp.WriteResp(nil)` (empty early-data, mirroring spec). On err: log `reason="rekey_write_resp_failed"`; close 4426; return.
5. **Marshal noise_resp.** `respFrame, err := marshalInnerFrameV2(protocol.TypeNoiseResp, respMsg)`. On err: log `reason="rekey_marshal_noise_resp"`; close 4426; return.
6. **Atomic CipherState swap.** `s.send, s.recv = newSend, newRecv`. Single tuple assignment; the dispatch goroutine is the sole writer and reader of these fields, so atomicity is structural without a lock. Old pointers are dropped from the struct.
7. **Log acceptance.** `m.cfg.Logger.Info("relay: v2 rekey accept", "event", "v2.rekey.accept", "conn_id", s.connID, "device_name", s.device.Name)`. `s.device` is non-nil because re-key only runs in `V2StateOpen`, which is only reached via the token-accept branch that sets `s.device`.
8. **Emit noise_resp.** `m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})`.
9. **No state transition.** State stays `V2StateOpen`. Do not assign to `s.state`. Do not touch `s.device`, `s.peerStatic`, or `s.resp` (the latter still points at the initial-handshake responder; it is dead state after first use, cleanup is out of scope).

Reject branches in `handleRekeyInit`: **5** (NewResponder, ReadInit, peer-static mismatch, WriteResp, marshal). Under the ≥10 red line. Each is a `Warn` + `closeWith` + `return`. Total ~55 LOC including the doc-comment and the five reject branches.

#### Package-level doc additions

Two short additions to the file-level comments:

1. **Update the `V2Session` struct comment** (lines 67-71) to name the single-goroutine ownership of `s.send` / `s.recv` explicitly:

   *"Mutation is serialised by the manager's single dispatch goroutine (the loop is the lock); there is no mutex because flynn/noise's CipherStates are not safe for concurrent use and the manager guarantees a single writer per conn_id. **The re-key responder (`handleRekeyInit`) atomically swaps `s.send` / `s.recv` in a single tuple assignment on this same goroutine; old `*CipherState` pointers are dropped from the struct and reclaimed by GC. No explicit `Wipe()` of the key bytes is exposed — the single-owner-goroutine invariant means no code path reads the old state after the swap, which is the practical zeroisation property.**"*

2. **Update the `V2SessionManager` package comment** (lines 161-171) to name the re-key atomic-swap invariant alongside the existing single-goroutine fan-in claim:

   *"...so single-goroutine fan-in is correct and obviously safe. **The re-key responder's CipherState swap (`s.send, s.recv = newSend, newRecv`) inherits this property: a single tuple assignment on this goroutine cannot be observed half-applied by any other code path, so the spec's atomic-switchover requirement is structural.**"*

Both additions are short paragraphs in existing comment blocks. No new files; no new doc-comment style.

### Concurrency — the atomic-swap invariant

The CipherState swap executes on the manager's single dispatch goroutine — the same one that consumes from `cfg.Frames`, holds `m.sessions`, and runs every handler call. No other goroutine reads or writes `s.send` / `s.recv`. No lock is needed; no `sync/atomic` is needed; tuple assignment in Go emits two stores but no observer can interleave because no other observer exists.

The peer-static comparison (`bytes.Equal(resp.PeerStatic(), s.peerStatic)`) reads `s.peerStatic`, which was set in `handleNoiseInit` (same goroutine, before `s.state` advanced to `V2StateOpen`) and never mutated again. No write-after-read race.

`s.resp` becomes stale after re-key: it still points at the initial-handshake `Responder`. This is acceptable — `s.resp` is only consumed inside the awaitingInit arm of `handleNoiseInit`; re-key uses a fresh local `Responder` and never touches `s.resp` afterwards. A follow-up cleanup that moves `s.resp` out of the struct is **explicitly out of scope** (touches #445's surface without observable benefit).

**Head-of-line blocking during re-key.** The re-key handshake costs roughly one X25519 derivation + AEAD setup (~100µs) plus one outbound `noise_resp` send. During this window the dispatch goroutine cannot service any other `conn_id`. Same posture as the initial handshake — single-goroutine fan-in from #445/#446 is unchanged. The per-conn fan-out follow-up that #446 names also covers this; tracked in `features/v2-session-manager.md` Open questions.

### State-machine transition table — the `noise_init`-in-`open` cell flips

Reproducing the table from #446 / #454 with **one cell changed**:

| Inbound on conn_id | `awaitingInit` | `handshakeComplete` | `open` | `closed` |
|---|---|---|---|---|
| `noise_init` | run handshake | close(4421), → closed | **re-key responder; swap CipherStates; state stays `open`** (was: close(4421), → closed) | drop |
| `noise_resp` | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |
| `noise_msg`, decrypts cleanly + Type `rekey_request` | n/a | n/a | handleRekeyRequest; logs-only; state stays `open` (#454) | drop |
| `noise_msg`, decrypts cleanly + Type other | close(4421), → closed | sealed `auth.invalid_token` + close(4401), → closed | dispatch via handlers; sealed reply; state stays `open` (#446) | drop |
| `noise_msg`, decrypt fails | close(4421), → closed | close(4421), → closed | close(4421), → closed (#446 tampered-frame branch — **applies post-swap unchanged**, AC #4) | drop |
| Unknown `type` / bad `v` / malformed | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |

### Error handling

| Failure mode | Close code | AEAD-sealed error envelope? | Notes |
|---|---|---|---|
| Re-key `NewResponder` failure | 4426 | no | Realistically unreachable; static key validated at construction. `reason="rekey_responder_init_failed"`. |
| Re-key `ReadInit` MAC/decrypt failure | 4426 | no | Phone presented an invalid IK message 1. `reason="rekey_read_init_failed"`. |
| **Re-key peer-static mismatch** | 4426 | no | **New finding.** The re-key initiator's static pub differs from `s.peerStatic`. Indicates relay-MITM injection or a phone-side identity change. `reason="rekey_peer_static_mismatch"`. |
| Re-key `WriteResp` failure | 4426 | no | Realistically unreachable. `reason="rekey_write_resp_failed"`. |
| Re-key `marshalInnerFrameV2` failure | 4426 | no | Realistically unreachable. `reason="rekey_marshal_noise_resp"`. |
| Old-key `noise_msg` arriving after swap | 4421 | no | **Inherited from #446's tampered-frame branch.** No new code; verification only. |

Every re-key failure mode closes at **4426** — the same code the initial handshake uses for IK-pattern failure (AC #3). The test (`TestV2Session_OpenState_RekeyResponder_DifferentPeerStatic_4426`) pins the chosen code for one failure branch.

**Atomicity reuse.** Re-key failure paths use `closeWith(ctx, s, StatusHandshakeFailure, nil)` — close-only, no sealed error frame, consistent with the initial handshake's IK-failure posture. The session entry is removed from `m.sessions` by `closeWith`, so the next inbound frame on the same `conn_id` lazy-creates a fresh `V2StateAwaitingInit` session (structural cleanup pattern from #446).

### Log policy (security-load-bearing)

Extends the #445 / #446 / #454 posture. Implementation MUST adhere; code-review checks each rule against the diff.

- **MUST NOT log at any level:** AEAD plaintext / ciphertext, IK message bytes, base64 forms thereof, peer-static pubkey bytes (raw `s.peerStatic` or `resp.PeerStatic()`), new or old CipherState internal state, the underlying `flynn/noise` error text from `ReadInit` / `WriteResp` failures (may carry counter indices or AEAD-tag positions — not operator-actionable).
- **MUST log on re-key acceptance:** event class `v2.rekey.accept`, `conn_id`, `device_name`. Mirrors `v2.handshake.accept` from #445. `device_name` is operator-actionable; re-key preserves `s.device`, so it is the SAME device the conn was authenticated as at initial handshake.
- **MUST log on re-key failure:** event class `v2.handshake.reject.ik_failure` (re-using the existing class — re-key failure is an IK-failure subclass), `conn_id`, `close_code=4426`, `reason=<snake-case key>` from the table above.
- **MUST NOT include `device_name` on the peer-static-mismatch branch.** The re-key initiator's identity is unknown / hostile; logging the captured-at-initial-handshake device-name on a rejected re-key would create an anti-enumeration signal (an attacker probing `conn_id`s could correlate close-event timing with device-name disclosure). Other re-key failure branches don't have a device-name field at all (the local `resp` doesn't carry one); the discipline is explicit only on the mismatch branch where `s.device.Name` is in scope.
- **MUST NOT log per open-state happy-path frame.** Unchanged from #446. `v2.rekey.accept` fires once per successful re-key — low frequency (1/hour expected).

### Testing strategy

Tests are unit-shape at the `(*V2SessionManager)` boundary, reusing `driveToOpen` / `openSession` / `sealAppFrame` / `decryptAppFrame` / `silentLogger` / `genV2Keypair` / `startManager` / `waitForEnvelopes` / `wrapInnerFrame` / `decodeRespFrame` / `decodeNoiseMsg` from `internal/relay/v2session_test.go`. No new helpers needed. No e2e — the e2e harness assigns a new `conn_id` per dial, which makes re-key untestable without invasive fakerelay extensions (same asymmetry rationale as #446 documented for the fresh-`noise_init`-after-tamper case).

**Removal.**

- **Delete `TestV2Session_NoiseInitAfterOpen_4421` (lines 409-464).** The behaviour it pins is exactly what this slice changes; replacement coverage is the three new tests below.

**New tests** — three new functions under a `// --- re-key responder tests (#453) ---` comment header, placed after the existing #446 open-state dispatch tests. The developer writes each one in the file's existing stdlib `testing` idiom (table-driven where natural, no testify, `t.Parallel()` per the file's convention).

- **`TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys`** (AC #1 + AC #5 happy path)
  - Setup: `driveToOpen` with `initPriv` keypair A. After driveToOpen returns, the manager's session is at `V2StateOpen` and the test holds the initiator's `initSend` / `initRecv` from initial handshake.
  - Construct a SECOND `noise.Initiator` reusing the SAME `initPriv` (peer-continuity invariant requires the same static key). `WriteInit(nil)` (empty early-data per spec).
  - Feed the new initMsg as a `noise_init` `RoutingEnvelope` via `wrapInnerFrame`.
  - Assert: ONE additional outbound envelope (the new `noise_resp`), `CloseCode == 0`, Frame non-nil. Decode the inner frame as `TypeNoiseResp`; on the initiator side, `initiator2.ReadResp(respRaw)` succeeds, returns empty early-data, yields fresh `(initSend2, initRecv2)`.
  - Round-trip verification under NEW keys: register a stub `dispatch.Handler` keyed by `protocol.TypeListConversations` that replies with a known payload (same pattern as `TestV2Session_OpenState_EncryptedRoundTrip`). AEAD-seal a `list_conversations` request envelope under `initSend2` (the NEW initiator-side send CipherState), feed it, expect ONE outbound envelope. Decrypt with `initRecv2`; assert the inner envelope's `Type` / `InReplyTo` / `Payload` match the stub's reply. Pins that BOTH new directions are wired and the swap is symmetric.
  - State assertion: stop the manager, then `mgr.sessions[v2TestConnID].state == V2StateOpen` and `mgr.sessions[v2TestConnID].device != nil` (snapshot preserved). Also assert `mgr.sessions[v2TestConnID].peerStatic` is unchanged from the pre-rekey value (lifetime contract from #452).

- **`TestV2Session_RekeyResponder_DifferentPeerStatic_4426`** (AC #2)
  - Setup: `driveToOpen` with `initPriv` keypair A.
  - Construct a SECOND `noise.Initiator` with a DIFFERENT keypair B (`genV2Keypair(t)` again). `WriteInit(nil)`.
  - Feed.
  - Assert: ONE additional outbound envelope, `CloseCode == uint16(StatusHandshakeFailure)` (4426), Frame nil. After stop, `mgr.sessions[v2TestConnID]` is absent from the map (closeWith cleanup).
  - Log assertion: replace `silentLogger()` with a `slog.New(slog.NewJSONHandler(buf, nil))` writing to a test-owned `bytes.Buffer` (write/read serialised by the manager's stop — the test reads the buffer only after `stop()` has returned, so no concurrent access). Assert the buffer contains a JSON line with `"event":"v2.handshake.reject.ik_failure"` and `"reason":"rekey_peer_static_mismatch"`. **Assert the buffer does NOT contain the substring `"device_name"`** on the reject line — pins the anti-enumeration discipline. This is the **security-load-bearing test** for the Threat #3 residual-risk claim.

- **`TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421`** (AC #4 — verification of inherited behaviour)
  - Setup: `driveToOpen` with `initPriv`. Capture `initSend` from the OPEN session (the test's `sess.initSend`) BEFORE the re-key — this is the stale CipherState the test will replay against.
  - Stash a ciphertext: `sealAppFrame(t, sess.initSend, someEnv)` where `someEnv` is any well-formed envelope. Do NOT feed it yet — re-key first.
  - Drive a re-key: SECOND `noise.Initiator` with SAME `initPriv`; `WriteInit(nil)`; feed; drain the `noise_resp` (count goes 1 → 2). At this point the manager's `s.recv` is the NEW CipherState; the test's stashed ciphertext was sealed under the OLD `s.send`'s counterpart key.
  - Feed the stashed routing envelope.
  - Assert: ONE additional outbound envelope (count goes 2 → 3), `CloseCode == uint16(StatusProtocolMismatch)` (4421), Frame nil. After stop, `mgr.sessions[v2TestConnID]` is absent. Pins AC #4 — the existing #446 tampered-frame branch fires against the post-swap `s.recv`, with no new code in this slice.

The `_4426` and `_4421` suffix convention mirrors existing test names in the file (`TestV2Session_OpenState_TamperedNoiseMsg_4421`, `TestV2Session_IKReject_4426`).

**No new test in `internal/noise/noise_test.go`** — `PeerStatic` is already tested in `internal/noise/noise_test.go` from #452. The re-key responder consumes the same accessor in the same way (post-`ReadInit`), so no new noise-package test is warranted.

**No new test in `internal/dispatch/dispatch_test.go`** — this slice makes no change to `internal/dispatch`.

**No e2e** — see the rationale at the top of this section.

### Wire-format and protocol changes

**None to the binary↔relay wire shape.** This slice operates entirely within the existing v2 inner-frame discriminator (`noise_init` / `noise_resp` / `noise_msg`). The re-key handshake reuses the same frame types as the initial handshake.

### `cmd/pyry/relay.go` wiring

**Not modified.** Same posture as #446 / #452 / #454 — production daemon continues to wire the v1 dispatcher; the v2 manager remains test-only until the production cutover slice (gated on #436).

## Open questions

1. **Should `s.resp` be reset to `nil` after `WriteResp` returns, to make the dangling-pointer property explicit?** Currently `s.resp` is set in awaitingInit, used through `WriteResp`, and then never touched again. The dangling pointer leaks ~1KB of HandshakeState memory for the session's lifetime. Re-key uses a fresh local `resp` and never touches `s.resp`. **Decision: defer.** This is a cleanup that belongs in a `V2Session`-shape refactor, not in a re-key slice. Filing as a follow-up under the v2-session-manager open-questions section is fine; not a blocker.

2. **Should the swap zeroise the old CipherStates' internal key material before dropping the pointers?** Today the GC reclaims the underlying memory once the field overwrite happens; an attacker with a process-memory read could observe the old keys in the heap until the GC runs. flynn's `CipherState` does not expose a `Zero()` method, and `internal/noise.CipherState` (#433) does not wrap one. Adding either is out of scope — would force a #433 surface change. The package comment names this trade-off explicitly. **Decision: defer.** The threat model (process-memory read) is well downstream of the relay-MITM surface this slice addresses; a future hardening slice can revisit.

3. **Should re-key rate-limiting be added in this slice?** A misbehaving phone or relay-MITM that survives the peer-static check (via the same static — e.g., leaked Keystore scenario) could trigger excessive re-keys. Per-re-key cost is ~100µs of crypto + one outbound frame. **Decision: no.** The same DoS shape exists at initial handshake without rate-limiting; the per-binary single-dispatch-goroutine fan-in naturally caps total re-key rate across all conns; the per-conn fan-out follow-up (Open Q from #446) is the right place to add per-conn rate limits if observed. Defer to a sibling defensive slice if observed in practice.

4. **Will logging on the WARN reject branch leak the close-code reason to network observers?** The log channel is operator-side (stderr / `journalctl`); the WS close code is the wire-side signal. They are separate channels; no leak. The reject reason is named only in the log; the wire-side signal is the bare `close_code=4426`. **Resolved: no concern.**

## Scope self-check

Production source files modified or created (excluding tests, `*.md`, the spec itself):

1. `internal/relay/v2session.go` — modified (top-of-function switch in `handleNoiseInit`; new `handleRekeyInit`; two short package-comment additions).

Count: **1 production source file.** Well under the 5-file size:s ceiling.

New exported symbols: **0.** `handleRekeyInit` is package-private. No new types.

Production LOC estimate:
- `handleNoiseInit` switch refactor: ~10 LOC net (current top-of-function gate is ~10 LOC; replaced by ~20 LOC of `switch`, net +10).
- `handleRekeyInit`: ~55 LOC (function body + 5 reject branches + doc-comment).
- Package-comment additions on `V2Session` and `V2SessionManager`: ~15 LOC (two short paragraphs).

Production total: ~80 LOC.

Test LOC estimate: delete `TestV2Session_NoiseInitAfterOpen_4421` (~55 LOC); add three new tests (~70 LOC each = ~210 LOC). Net test delta: +155 LOC.

Total written work: ~235 LOC. Well within S (~600 LOC ceiling).

Edit fan-out: **zero new consumer call sites.** `handleNoiseInit`'s signature is unchanged; its only caller (`handleFrame`'s `switch inner.Type` case) is unchanged. `handleRekeyInit` has exactly one caller (`handleNoiseInit`'s new `open` arm). No type signatures move; no cross-package edits.

Acceptance criteria: **5.** Within the 5-AC red line.

Reject branches in the new state-machine logic: **5** in `handleRekeyInit` (counted against the ≥10 red line). Under the cap.

Size: **S confirmed.** No split.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. Two boundaries are crossed by inbound bytes on the re-key path; both are existing:
  - `Responder.ReadInit(inner.Data)` on the new responder — same AEAD boundary primitive as the initial handshake, same wrapper from `internal/noise` (#433). The re-key path adds the explicit peer-static continuity check immediately downstream, narrowing the "authenticated peer" boundary from "anyone with a valid IK message 1 against the binary's static" to "the same peer as initial handshake". This is the implementation of the threat-model claim that "no impersonation succeeds" on the relay-operator surface (Threat #3 residual-risk).
  - `s.recv.Decrypt(inner.Data)` in `handleNoiseMsg`'s open branch — unchanged from #446; the new code does not touch the application-frame decrypt path. The post-swap `s.recv` is a CipherState derived from a fresh authenticated handshake against the same peer; no new untrusted-input source.
- **[Tokens, secrets, credentials]** No findings. The re-key handshake **does NOT re-validate the device token** (spec § Re-key: empty early-data). The `s.device` snapshot is preserved across re-key — same posture as #446's documented "device snapshot doesn't refresh after handshake" behaviour, and consistent with v1's `dispatch.Conn.auth` lifetime. Revocation propagation for active conns remains a separate ticket. **What this slice adds:** the peer-static continuity check structurally binds the post-re-key AEAD channel to the same peer identity that originally presented the validated token — without this check, a relay-operator MITM could re-key over an authenticated session and inherit the device snapshot. The check is the cryptographic implementation of the threat-model's "no impersonation" claim.
- **[File operations]** N/A — this slice performs no file I/O.
- **[Subprocess / external execution]** N/A — no subprocess interaction.
- **[Cryptographic primitives]** No findings. AEAD and DH primitives are inherited from `internal/noise` (#433, separately reviewed). The re-key handshake reuses `NewResponder` / `ReadInit` / `WriteResp` exactly as the initial handshake does — same cipher suite (`Noise_IK_25519_ChaChaPoly_BLAKE2s`), same defensive-copy posture on key bytes, same single-source pin. The atomic CipherState swap is single-goroutine tuple assignment — no mixed-key window is observable. The old `*CipherState` pointers are dropped from the V2Session struct on swap; explicit `Wipe()` of the underlying key bytes is NOT exposed (would require touching #433's wrapper; the single-owner-goroutine ownership of `s.send` / `s.recv` ensures no code path accesses the old state after the swap, which is the practical zeroisation property — documented in the package comment). **`bytes.Equal` for the peer-static comparison is variable-time but acceptable**: both operands are public keys (the live peer's static from `resp.PeerStatic()` and the stored `s.peerStatic`), so timing leakage carries no secret. A one-line code comment names this choice to forestall a "should be `subtle.ConstantTimeCompare`" review nit.
- **[Network & I/O]** No findings, with one defensive note. Inbound `Data` size cap (65535 bytes decoded) is inherited from `decodeInnerFrameV2` (#445); the re-key noise_init flows through the same decoder. **Re-key rate-limiting is NOT in this slice** — per Open Question 3, same posture as initial handshake; per-conn DoS exposure is bounded by the single dispatch goroutine's natural throughput cap. Document as a deferred defensive concern; defer until observed.
- **[Error messages, logs, telemetry]** No findings. The peer-static-mismatch branch deliberately omits `device_name` from its log fields (the re-key initiator's identity is unknown / hostile; logging the captured-at-initial-handshake device-name on a rejected re-key would create an anti-enumeration signal). Re-key acceptance DOES log `device_name` (operator-actionable; SAME device as initial handshake by construction). No AEAD plaintext, ciphertext, IK message bytes, peer-static raw bytes, CipherState internal state, or flynn-noise error text appears in any log field at any level. The `reason` field carries only spec-defined snake-case keys — no attacker-influenced bytes.
- **[Concurrency]** No findings. The CipherState swap is single-goroutine tuple assignment; no other goroutine reads/writes `s.send` / `s.recv`. The peer-static comparison reads `s.peerStatic`, set once in `handleNoiseInit` (same goroutine) and never mutated again. No lock-ordering issues — no new locks. `s.resp` is left dangling (set once in initial handshake, unused afterwards); same posture as the existing struct; cleanup deferred (Open Q 1).
- **[Threat model alignment]** No findings.
  - **Threat #3 (relay-operator MITM):** strengthened by the peer-static continuity check. Without it, a relay-MITM that injects `noise_init` on an existing `conn_id` would re-key over an authenticated session and assume the device snapshot — the binary would treat the attacker's frames as the original phone's. With the check: re-key from a different static is rejected at 4426; the original phone's session is torn down (closeWith cleanup); no impersonation succeeds. **This slice is the implementation of the residual-risk-low claim** the threat model already makes; without it, the claim would be false on the re-key path.
  - **Threat #5 (compromised phone / leaked Keystore static):** unchanged from initial-handshake posture. A phone with a compromised static private key can re-pair, and can also re-key over an existing session (because the peer-static check passes — same key). The token-validation gate at initial handshake is the only line of defence against full phone compromise; this slice does not introduce a new compromise vector.
  - **Threat #6 (replay):** flynn's monotonic nonce on the new CipherStates resets to 0 after re-key (Noise framework guarantee). Any replayed old-key frame fails AEAD on the new `s.recv` — inherited #446 branch. AC #4 covers this with `TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421`.
  - **Threat #7 (tampered frame):** same path as #446. Inherited.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-17
