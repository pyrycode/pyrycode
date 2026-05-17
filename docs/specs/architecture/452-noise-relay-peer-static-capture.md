# 452 — `internal/noise` + `internal/relay`: capture initiator peer-static at v2 IK handshake

## Files to read first

Each entry says **what to extract**, so the developer's turn-1 data load is complete.

- `internal/noise/noise.go:51-104` — `Responder` type, `NewResponder`, `ReadInit`. The new `PeerStatic()` method lands here as a sibling to `ReadInit`. Extract the defensive-copy idiom from line 80 (`append([]byte(nil), staticPriv...)`) — the same posture applies to the bytes flowing OUT of the wrapper.
- `internal/noise/noise.go:1-21` — package-level doc-comment with the SECURITY paragraph. The new method does not handle private-key bytes, but the field it surfaces is identity-bearing; the spec's Security section pins the "MUST NOT log peerStatic" extension to the existing key-bytes-MUST-NOT-log discipline.
- `internal/noise/noise_test.go:14-56` — `genKeypair` and `runHandshake` helpers. The two new noise tests reuse `genKeypair`; the post-handshake test uses a partial handshake (`NewResponder` + `ReadInit` only, no `WriteResp`) and does not need `runHandshake`.
- `internal/relay/v2session.go:67-91` — `V2Session` struct and `State()` accessor. The new field `peerStatic []byte` lands here in the same field block; no new accessor (the field is package-private and read by code in the same package only, in this slice via nothing — production reader lands in #453).
- `internal/relay/v2session.go:303-345` — `handleNoiseInit`, specifically the block from the top of the function through the `s.resp.ReadInit(inner.Data)` call. The capture-site assignment lands **immediately after** the `if err != nil` branch closes on the success path, before the early-data JSON unmarshal. Confirmed-line for the developer: insert after the `return` inside the `if err != nil` block (line 345 in the current file), before line 347's `var helloEnv protocol.Envelope`.
- `internal/relay/v2session_test.go:736-826` — `openSession` struct, `driveToOpen`, `sealAppFrame`, `decryptAppFrame`. The new v2session test reuses `driveToOpen` to reach `V2StateOpen` and then asserts on `os.mgr.sessions[v2TestConnID].peerStatic`. The helper today drops `initPub` from `genV2Keypair`; the new test captures both so it can compare against the captured field.
- `docs/protocol-mobile.md:483-505` — Threat #3 (relay-operator MITM) and the residual-risk claim *"no impersonation succeeds"*. The capture this slice ships is the prerequisite data exposure; the implementation of the no-impersonation claim is the re-key responder's `bytes.Equal` check, which lands in **#453**.
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md:80,108-112` — ADR 024: per-binary static key on the responder; v2's relay-MITM mitigation is cryptographic, not policy. Establishes why the per-session identity pin matters at all.
- **Orphan spec at `docs/specs/architecture/449-v2-rekey-responder.md` on `feature/449`** — the parent ticket's spec (closed `NOT_PLANNED`, branch unmerged). Contains the design notes for the accessor and field exactly as this slice needs them. The slice extracted from #449 is faithful to that design — the accessor's signature, the defensive-copy body, the field-lifetime contract, and the capture-site placement are all lifted verbatim. Do not re-derive; consult the orphan spec if a design rationale is unclear.
- `github.com/flynn/noise@v1.1.0/state.go:624-626` — `HandshakeState.PeerStatic()` body: `return s.rs`. Before IK message 1 is read, `s.rs` is `nil` (the field is set during the `re` token's DH decrypt inside `ReadMessage`). The wrapper's pre-`ReadInit` contract — "returns a zero-length slice, no panic" — follows directly from this implementation detail; the spec pins it as the wrapper's contract independently of flynn's internals so a future flynn upgrade that changes the field initialisation does not silently break callers.

## Context

Mobile Protocol v2 (`docs/protocol-mobile.md` § Threat #3) and ADR 024 require that an authenticated v2 session cannot be silently re-keyed by a different peer. The wire-level mechanism is a peer-static continuity check on every re-key: the new IK handshake's initiator static must equal the one captured at the initial handshake; mismatch closes the conn at 4426.

The IK handshake today (in #445/#446) runs `(*noise.Responder).ReadInit` which internally learns the initiator's static pub via flynn/noise's `HandshakeState.PeerStatic()`. The wrapper does not surface it and `V2Session` does not store it. Without that capture, the re-key responder slice (**#453**) has nothing to compare against.

This slice ships the **pure-data exposure only**:

1. A new accessor `(*noise.Responder).PeerStatic() []byte` that returns a defensive copy of `r.hs.PeerStatic()`.
2. A new `peerStatic []byte` field on `V2Session`.
3. One assignment in the existing initial-handshake path that captures `s.resp.PeerStatic()` after `ReadInit` succeeds and before any other use of the early-data payload.

**No production code path reads `peerStatic` in this slice.** The field exists so that #453's `handleRekeyInit` can call `bytes.Equal(newResp.PeerStatic(), s.peerStatic)`. Tests pin the accessor's contract and the capture site; nothing else changes on the wire.

The follow-on slice (#453) is **blocked by this slice** (PO recorded the `blockedBy` edge in the split-summary on #449). #454 (the `rekey_request` control-envelope discriminator) is independent and not blocked.

## Design

### Surface — additive

#### `internal/noise/noise.go` (modified)

One new method on `Responder`:

```go
// PeerStatic returns a copy of the initiator's 32-byte X25519 static
// public key as learned from IK message 1.
//
// Callable only after ReadInit has returned nil. flynn/noise's contract
// is "an error to call before a handshake message containing a static
// key has been read"; the wrapper does NOT panic on misuse. Before
// ReadInit has succeeded, the underlying flynn field is the zero value
// for []byte (nil), so this method returns a zero-length slice (its
// length is 0; bytes.Equal against any non-empty key returns false).
// Callers that need stricter enforcement should track handshake state
// in their own session struct.
//
// The returned slice is a fresh allocation; mutating it does not affect
// the underlying HandshakeState, and a subsequent call returns an
// independent copy. Mirrors the defensive-copy posture of NewResponder's
// StaticKeypair.Private assignment.
func (r *Responder) PeerStatic() []byte
```

Body — **exactly** `return append([]byte(nil), r.hs.PeerStatic()...)`. Single statement.

No new method on `Initiator`. The initiator already knows its peer's static — it is the constructor argument. Adding the symmetry would be dead code and would invite confusion about which side's value is "trusted".

No new field on `Responder`. No change to existing methods. No change to `cipherSuite`, no change to `Initiator`, no change to `CipherState`.

#### `internal/relay/v2session.go` (modified)

`V2Session` gains one field, declared in the same field block as `device`:

```go
type V2Session struct {
    // ... existing fields unchanged: connID, state, resp, send, recv, device ...

    // peerStatic is the initiator's 32-byte X25519 static public key
    // captured at the initial handshake (immediately after
    // Responder.ReadInit returns nil). The field is set exactly once
    // per V2Session and pins the original peer's identity for the
    // session's entire lifetime — a successful re-key (added in #453)
    // MUST NOT overwrite this value; the re-key responder reads
    // s.peerStatic and rejects mismatches at WS close code 4426.
    //
    // SECURITY: this is a public key (not a secret), but it is
    // identity-bearing. It MUST NOT appear in any logged field; the
    // package's no-key-in-logs discipline extends to per-session
    // identity pins. Not persisted to disk; lifetime is the V2Session.
    peerStatic []byte
}
```

The capture site is a single new line inside `handleNoiseInit`, placed **immediately after** the `if err != nil` branch closes on the `ReadInit` call's success path. Current v2session.go positions: the new line goes between line 345 (the closing `}` of `if err != nil { ... return }`) and line 347 (`var helloEnv protocol.Envelope`):

```go
earlyData, err := s.resp.ReadInit(inner.Data)
if err != nil {
    // ... existing reject branch unchanged ...
    return
}
// Capture the peer's static pub for the re-key responder's
// peer-continuity check (#453). PeerStatic returns a defensive copy;
// safe to store directly.
s.peerStatic = s.resp.PeerStatic()

var helloEnv protocol.Envelope
// ... rest of handleNoiseInit unchanged ...
```

Three correctness invariants the developer must preserve:

1. **The assignment is unconditional on the success path of `ReadInit`.** It precedes the early-data JSON unmarshal, the hello-type check, the token validation, and `WriteResp`. Reasoning: by the time `ReadInit` returns nil, flynn has already MAC-verified and decrypted the initiator's static; the value is authentic regardless of whether the subsequent steps accept the hello. We pin the identity for the connection at the earliest authenticated point.
2. **The assignment runs before any branch that calls `closeWith`.** A token failure later in the function tears the session down via `closeWith` and `delete(m.sessions, s.connID)` — the captured field is dropped along with the session entry, which is correct (a failed handshake leaves no peerStatic to compare against on a future re-key).
3. **No other site in this slice writes `s.peerStatic`.** The capture site is the only writer; the field is unread by any production code path in this slice. (#453 will read it from `handleRekeyInit`.)

No change to `handleNoiseInit`'s other branches. No change to `handleNoiseMsg`, `dispatchAppFrame`, `sealError`, `marshalInnerFrameV2`, `closeWith`, or `send`.

### Why this slice is data-only

The AC explicitly forbids a behaviour-observable change on the wire. The split from #449 was structured so that #452 ships the inert capture and #453 wires the comparison; this lets the security-review surface for #452 reduce to "does the capture happen at the right point, under the right defensive-copy posture" without entangling re-key state-machine semantics.

A consequence: the field is **set but never read** in production within this slice. `go vet` and `staticcheck` do not warn on unused struct fields, so no suppression is needed. The test in `v2session_test.go` reads the field via white-box access (test in the same package as production), which prevents an "unused" warning across `go test -race`.

### Why no panic on pre-`ReadInit` call

The accessor's pre-`ReadInit` contract is "no panic, returns zero-length slice". The alternative (panic, or return `(nil, ErrHandshakeIncomplete)`) was considered and rejected:

- A panic would propagate to the dispatch goroutine and terminate the v2 manager — a hostile peer that can race a `PeerStatic` call before `ReadInit` returns would have a trivial denial-of-service primitive against the entire binary's mobile relay path. (In this slice, no production code path calls `PeerStatic` before `ReadInit`; in future slices, the same property must hold, but the wrapper's defence-in-depth posture is to tolerate misuse rather than convert it to a crash.)
- Adding an error return changes the call-site shape from `s.peerStatic = s.resp.PeerStatic()` to `if ps, err := s.resp.PeerStatic(); err == nil { s.peerStatic = ps }`, which (a) bloats the capture site and (b) regresses readability for a contract that is structurally maintained by the call order.

The zero-length return matches flynn's own behaviour (`s.rs` is `nil` before `ReadMessage` populates it), so the wrapper is documenting flynn's contract rather than adding a new one. The test below pins this so a flynn upgrade that changes the field's initial value would surface as a test failure.

## Testing strategy

Three new tests across two files. No new helpers; the existing `genKeypair` (noise) and `driveToOpen` / `openSession` / `genV2Keypair` (relay) helpers cover the setup.

### `internal/noise/noise_test.go`

**Test 1: `TestResponder_PeerStatic_AfterReadInit_MatchesInitiatorStatic`**

Scenario: build a `Responder` with a fresh responder static; build an `Initiator` with a fresh initiator static; drive `Initiator.WriteInit` → `Responder.ReadInit`. Assert:

- `responder.PeerStatic()` is non-empty and equals the initiator's static **public** key (derived from the initiator's static private via `ecdh.X25519().NewPrivateKey(initPriv).PublicKey().Bytes()` — same idiom as `NewInitiator`).
- The returned slice is a **fresh allocation**: capture two calls (`a := responder.PeerStatic()` then `b := responder.PeerStatic()`), mutate `a[0] ^= 0xff`, assert `b[0]` is unchanged. Pins the defensive-copy contract.
- A third call after the mutation still returns the unmutated value — confirms flynn's internal state is not aliased through the returned slice.

**Test 2: `TestResponder_PeerStatic_BeforeReadInit_ReturnsZeroLength`**

Scenario: construct a `Responder` via `NewResponder` and call `PeerStatic()` immediately, without `ReadInit`. Assert:

- The call does not panic (the test runner is enough; no `recover` needed because a panic fails the test directly).
- The returned slice has `len() == 0`. Use `len(got) != 0` rather than `got != nil` — both nil and `[]byte{}` are valid zero-length returns per the documented contract.

No tampered-message variant for this test surface — the existing `TestReadInit_RejectsTamperedMessage` / `TestReadInit_RejectsTruncatedMessage` cover the case where `ReadInit` fails (the developer does NOT need to add a `PeerStatic` assertion to those tests; on `ReadInit` failure the caller is contractually forbidden from trusting any subsequent state, including `PeerStatic`).

### `internal/relay/v2session_test.go`

**Test 3: `TestV2Session_InitialHandshake_CapturesPeerStatic`**

Scenario: drive a paired-device handshake to `V2StateOpen` via `driveToOpen`, then white-box-assert on the captured field. The test lives in package `relay` (same package as production), so direct access to `mgr.sessions[v2TestConnID].peerStatic` is permitted.

Bullet-pointed scenario for the developer:

- `respPriv, respPub := genV2Keypair(t)` — responder side.
- `initPriv, initPub := genV2Keypair(t)` — **both** values captured (existing helpers discard `initPub`; this test needs it for the comparison).
- Stand up `V2SessionConfig` with `respPriv`, a paired-token devices registry, `silentLogger`, a frames channel, and a `v2Recorder`.
- Call `driveToOpen(...)` to reach `V2StateOpen`. Assert `os.mgr` is non-nil and the handshake produced exactly one envelope (driveToOpen already enforces both; this test inherits).
- Look up the session: `sess := os.mgr.sessions[v2TestConnID]`. Assert `sess != nil`.
- Assert `bytes.Equal(sess.peerStatic, initPub)` returns true. On failure, `t.Errorf("peerStatic mismatch: got %x, want %x", sess.peerStatic, initPub)`.
- Assert `len(sess.peerStatic) == noise.KeyLen` (defence against an empty-slice regression that would silently pass a `bytes.Equal(nil, nil)` if the capture never ran).
- Call `os.stop()` (deferred) for clean shutdown.

The test is ~25-30 LOC. No new helpers; uses `driveToOpen`, `genV2Keypair`, `silentLogger`, `v2PairedRegistry`, `v2TestConnID`, `v2TestToken` from the existing v2session_test scaffolding.

### Tests that MUST NOT be added in this slice

- A peer-static **continuity check** test (mismatch → 4426) belongs to #453, not here. There is no production code in this slice that compares `peerStatic` against anything.
- A test that asserts `peerStatic` survives a re-key belongs to #453 — there is no re-key code path in this slice.
- A `PeerStatic` test on `Initiator` does not exist because no such accessor exists; do not add one.

## Concurrency model

Unchanged from #446. The field `peerStatic` is mutated exclusively by the `V2SessionManager.Run` goroutine (single dispatch loop; "the loop is the lock", per the package's existing comment). Reads in #453 happen on the same goroutine. No new shared state, no new locks, no new atomics. The field's lifetime is the `V2Session`'s; on `closeWith` the session is deleted from the manager's map and the struct (including the slice) becomes unreachable and GC-collectable.

## Error handling

Three failure modes the developer might consider; none warrant new branches in this slice.

1. **`PeerStatic` returns a zero-length slice on the success path.** Cannot happen post-`ReadInit` under flynn's IK pattern — IK message 1 always carries the encrypted static, and `ReadMessage` writes `s.rs` before returning nil. The wrapper's documented contract leaves this case undefined for `ReadInit`-success callers; in practice the bytes are always there. No assertion in production; the noise test that derives `initPub` from `initPriv` and compares pins the value transitively.
2. **`ReadInit` returns a non-nil error.** Already handled by the existing reject branch (close 4426). The new assignment never runs on this path because the existing `return` precedes it. ✓
3. **`flynn/noise` upgrade changes `PeerStatic` semantics.** Caught at the noise unit-test layer (Test 1's mutation-independence assertion exercises both the post-handshake value and the copy posture; Test 2 exercises the pre-handshake contract). An upgrade that broke either contract would fail tests in CI.

## Open questions

None. The orphan spec on `feature/449` resolved every design decision this slice needs; nothing was deferred to implementation.

## Out of scope

Mirrors the ticket § Out of scope verbatim. Listed here for completeness:

- The re-key responder path itself (`handleRekeyInit`, atomic `CipherState` swap, peer-static continuity check, AEAD-mismatch teardown verification) — **#453**, blocked by this slice.
- The `rekey_request` control-envelope discriminator and its handler chain branch — **#454**, independent.
- Initiator-side exposure of peer static — initiator already knows its peer static (constructor argument); no accessor needed.
- Persistence of `peerStatic` across binary restarts — out of scope for v2; ADR 024 § Re-key policy specifies in-memory only.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the new accessor exposes the post-`ReadInit` authenticated peer-static value. `ReadInit` is the explicit boundary (authenticates IK message 1 via DH + MAC); the wrapper publishes the already-trusted value via a defensive copy. The capture site in `handleNoiseInit` lives strictly on the success branch of `ReadInit`, so untrusted-to-trusted promotion is gated by the existing crypto check.
- [Tokens, secrets, credentials] No findings — the captured value is a public key, not a secret. The binary's static private key is not touched by this slice; the existing SECURITY discipline in `internal/noise/noise.go` and `V2SessionConfig.StaticPriv` is unaffected.
- [File operations] No findings — no file I/O introduced.
- [Subprocess] No findings — no exec.
- [Cryptographic primitives] No findings — no new primitive is introduced. The defensive copy (`append([]byte(nil), …)`) prevents slice aliasing between flynn's internal `HandshakeState.rs` and `V2Session.peerStatic`. A caller that mutates the returned slice cannot corrupt flynn's state nor poison a future `bytes.Equal` comparison; this is pinned by Test 1's mutation-independence assertion. Use of `bytes.Equal` for the future continuity check (lands in #453, out of scope here) is acceptable: both operands are attacker-known public values, so variable-time leakage carries no secret. Documenting that property in #453 prevents a "should be `subtle.ConstantTimeCompare`" review nit on a non-issue.
- [Network & I/O] No findings — no new I/O, no new size caps. The existing `maxNoisePayloadBytes` cap at the JSON-decode boundary continues to bound the input to `ReadInit`.
- [Error messages, logs, telemetry] SHOULD FIX — explicitly note in the field's doc-comment that `peerStatic` MUST NOT appear in any logged field. The value is a public key (not strictly secret) but identity-bearing, and the package's existing no-key-bytes-in-logs discipline (`internal/noise/noise.go` line 17-20) extends naturally to per-session identity pins. The spec body's field-comment block already includes this directive; code-review enforces. (Addressed by the spec.)
- [Concurrency] No findings — mutation and read both happen on the V2SessionManager's single dispatch goroutine; no shared-state race. Field lifetime is the `V2Session`'s.
- [Threat model alignment] No findings — this slice ships the data-capture half of Threat #3's residual-risk-claim implementation. The comparison half is named as out-of-scope and routed to #453, which is recorded as `blocked by #452` in the PO split-summary on #449.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-17
