# `internal/noise` — flynn/noise IK wrapper for handshake + AEAD transport

Owns the `Noise_IK_25519_ChaChaPoly_BLAKE2s` cipher-suite pin for [Mobile Protocol v2](../../protocol-mobile.md) and exposes a narrow API around [`github.com/flynn/noise`](https://github.com/flynn/noise) for the phone↔binary end-to-end encryption layer. Two roles (`Responder` for the binary, `Initiator` for the phone) run the IK handshake with caller-supplied early-data payloads carrying `hello` / `hello_ack`; both sides receive paired `CipherState`s for post-handshake AEAD. The empty-associated-data invariant from the v2 spec is enforced at the type system — there is no AD parameter to pass.

Consumers do not reach into `github.com/flynn/noise` directly; the wrapper is the single point through which the cipher-suite name, the key-length contract, and the AD invariant flow. A future migration off `flynn/noise` touches only this package.

## Surface

```go
const KeyLen = 32

var ErrInvalidKeyLength = errors.New("noise: invalid key length")

type Responder struct{ /* unexported */ }
type Initiator struct{ /* unexported */ }
type CipherState struct{ /* unexported */ }

func NewResponder(staticPriv []byte) (*Responder, error)
func (r *Responder) ReadInit(initMsg []byte) (earlyData []byte, err error)
func (r *Responder) PeerStatic() []byte
func (r *Responder) WriteResp(earlyData []byte) (respMsg []byte, send, recv *CipherState, err error)

func NewInitiator(staticPriv, peerStaticPub []byte) (*Initiator, error)
func (i *Initiator) WriteInit(earlyData []byte) (initMsg []byte, err error)
func (i *Initiator) ReadResp(respMsg []byte) (earlyData []byte, send, recv *CipherState, err error)

func (c *CipherState) Encrypt(plaintext []byte) (ciphertext []byte, err error)
func (c *CipherState) Decrypt(ciphertext []byte) (plaintext []byte, err error)
```

Twelve exports total. No `HandshakeState`, no `Config`, no `HandshakePattern`, no `CipherSuite`, no `DHKey` re-export — flynn types do not leak through the API.

## Cipher suite

`Noise_IK_25519_ChaChaPoly_BLAKE2s`. Pinned at exactly one location in the Go source:

```go
var cipherSuite = flynnNoise.NewCipherSuite(
    flynnNoise.DH25519,
    flynnNoise.CipherChaChaPoly,
    flynnNoise.HashBLAKE2s,
)
```

The human-readable name appears only in the package doc-comment. A future suite migration is a one-line edit + a doc-comment update — no caller code changes. The `flynnNoise` import alias on `github.com/flynn/noise` avoids name collision with this package.

## Handshake roles

### `Responder` (the binary)

```go
r, err := noise.NewResponder(staticPriv) // staticPriv = StaticKey.PrivateKey()[:]
// On receipt of IK message 1 (decoded from noise_init.data, raw bytes):
helloEarly, err := r.ReadInit(initMsg)
// Build hello_ack early-data, then produce IK message 2 + paired CipherStates:
respMsg, send, recv, err := r.WriteResp(ackEarly)
```

After `WriteResp` returns, the handshake is complete and the `Responder` is discarded. The two `*CipherState` values live on for the conn's lifetime (the per-`conn_id` state struct in #434). `send` encrypts outbound frames; `recv` decrypts inbound frames. `earlyData` for both `ReadInit` and `WriteResp` may be nil or zero-length.

### `Initiator` (the phone — Go reference impl for tests, Kotlin/Swift in production)

```go
i, err := noise.NewInitiator(staticPriv, peerStaticPub) // peerStaticPub = responder's pub from QR
// Build hello early-data, then produce IK message 1:
initMsg, err := i.WriteInit(helloEarly)
// On receipt of IK message 2 from the responder:
ackEarly, send, recv, err := i.ReadResp(respMsg)
```

Symmetric to `Responder`: after `ReadResp`, the `Initiator` is discarded and the two `CipherState`s carry transport. Production initiator is the phone (mobile-team's responsibility); the Go `Initiator` exists to enable full round-trip tests without a phone or fake-phone harness — the same posture `internal/pair`'s tests take for QR rendering.

## `send` / `recv` symmetry (the responder swap)

flynn's `WriteMessage` / `ReadMessage` return `(cs1, cs2)` where `cs1` always carries initiator→responder traffic and `cs2` always carries responder→initiator traffic. The asymmetry is per-role:

| Side | flynn's cs1 means | flynn's cs2 means |
|---|---|---|
| Initiator | `send` (outbound to responder) | `recv` (inbound from responder) |
| Responder | `recv` (inbound from initiator) | `send` (outbound to initiator) |

The wrapper collapses the asymmetry by swapping inside `Responder.WriteResp` and not swapping inside `Initiator.ReadResp`. Every caller, regardless of role, uses `send.Encrypt(...)` and `recv.Decrypt(...)` symmetrically — `(send, recv)` is the contract on both sides.

The swap is pinned structurally by `TestRoundTrip_BothDirections`: if `WriteResp` returned `(cs1, cs2)` instead of `(cs2, cs1)`, the very first cross-side `Decrypt` fails with a MAC error. There is no comment-only assertion of the convention — the test is the load-bearing pin.

## Empty associated-data, enforced at the type system

`CipherState.Encrypt(plaintext)` and `Decrypt(ciphertext)` have no AD parameter. Internally they call flynn's `Encrypt(nil, nil, plaintext)` and `Decrypt(nil, nil, ciphertext)` — the first `nil` is the output buffer (allocate fresh), the second is the AD.

This is the v2 spec's mandate from `docs/protocol-mobile.md:197`: *"Implementations MUST NOT pass a non-empty AD without a corresponding spec amendment."* Per-handshake key derivation isolates one session's ciphertext from another, and per-session nonce-counter discipline isolates frames within a session — the outer routing envelope (`conn_id`, `frame`) is plaintext and intentionally not AEAD-bound.

A caller cannot pass non-empty AD without editing `noise.go`, which would itself be a spec amendment. The wrapper IS the enforcement point.

## Peer-static exposure (responder only)

`(*Responder).PeerStatic() []byte` returns a defensive copy of the initiator's 32-byte X25519 static public key learned from IK message 1. Callable only after `ReadInit` returns nil; the wrapper does NOT panic on misuse — pre-`ReadInit` calls return a zero-length slice, matching flynn's `HandshakeState.PeerStatic()` zero-value behaviour. Body is one statement:

```go
return append([]byte(nil), r.hs.PeerStatic()...)
```

The defensive copy mirrors `NewResponder`'s `StaticKeypair.Private` posture — same idiom, symmetric for bytes flowing OUT as for bytes flowing IN. A caller that stashes the slice and later mutates it cannot corrupt flynn's internal state nor poison a downstream `bytes.Equal` comparison. Cost: 32 bytes per call.

**No `Initiator.PeerStatic` accessor exists.** The initiator already knows its peer's static — it is the constructor argument. Adding the symmetry would be dead code and invite confusion about which side's value is "trusted".

**Production consumer** (from [#452](../codebase/452.md)): [`internal/relay/V2Session.peerStatic`](v2-session-manager.md) captures the value at the initial Noise_IK handshake to pin the peer's identity for the session's lifetime; a future re-key responder (#453) will compare a fresh-handshake initiator's `PeerStatic()` against the pinned field and reject mismatches at WS close code 4426. The wrapper's contract: bytes returned post-`ReadInit` are authenticated (flynn has MAC-verified and DH-decrypted the initiator's static); bytes returned pre-`ReadInit` carry no semantics (zero-length).

The pre-`ReadInit` zero-length return is pinned by `TestResponder_PeerStatic_BeforeReadInit_ReturnsZeroLength` independently of flynn's internals — a future flynn upgrade that initialised `s.rs` to a non-nil placeholder would surface as a unit-test failure rather than a silent behavioural change. Callers that need stricter enforcement (e.g. "panic if I call this out of order") should track handshake state in their own session struct.

## Key arguments — `[]byte` of length `KeyLen` (32)

Both constructors length-check before any flynn call:

```go
if len(staticPriv) != KeyLen {
    return nil, fmt.Errorf("noise: responder static key: %w", ErrInvalidKeyLength)
}
```

Mismatch returns `(nil, wrapped ErrInvalidKeyLength)` — `errors.Is(err, ErrInvalidKeyLength)` is the caller's branch point. The wrapper derives the matching public via `crypto/ecdh`:

```go
priv, err := ecdh.X25519().NewPrivateKey(staticPriv)
// priv.PublicKey().Bytes() is the 32 bytes flynn needs for DHKey.Public
```

This matches `internal/keys.StaticKey.PrivateKey()`'s output (`internal/keys` itself uses `crypto/ecdh` for generation, so bytes round-trip bit-for-bit). The responder gets only `staticPriv`; the initiator additionally gets `peerStaticPub` (the responder's public, distributed via the QR pairing payload — see [#432](../codebase/432.md)).

### Defensive slice-copy of every key argument

Both constructors `append([]byte(nil), staticPriv...)` (and `peerStaticPub` on initiator) before handing to flynn's `DHKey`. A caller-side mutation after construction — or a hypothetical future `StaticKey` zeroisation — cannot corrupt the live handshake state. Cost: 32 bytes per key, paid once per handshake.

## Errors

| Sentinel | Returned when | Caller action |
|---|---|---|
| `ErrInvalidKeyLength` | `NewResponder` / `NewInitiator` got a key argument that is not exactly 32 bytes | Caller bug — fix the call site |
| Bare wrapped `fmt.Errorf` (no sentinel) | All flynn errors: MAC failure, malformed message, out-of-order call, AEAD failure, counter exhaustion, oversize message | Caller closes the WS with one close code per surface (4426 handshake, 4421 transport per spec); does not branch on the underlying reason |

Single sentinel keeps the surface small. Adding `ErrMACFailure` / `ErrCounterMismatch` would require the caller to branch on them; it does not. If a future caller needs the distinction, sentinels can be added without breaking the existing one.

### Error-message hygiene

No error message includes plaintext, ciphertext, key bytes, or early-data payload. Every wrapper error has the shape `"noise: <op>: <flynn message>"` where `<op>` is `ReadInit` / `WriteResp` / `WriteInit` / `ReadResp` / `encrypt` / `decrypt` and `<flynn message>` is flynn's own error string (which names the failure class but never echoes input bytes). The package emits **zero** `slog` calls.

Pinned by `TestErrorMessages_DoNotLeakPlaintextOrKey`: encrypts a 16-byte high-entropy `crypto/rand` probe, tampers the ciphertext, asserts the resulting error string contains neither the hex nor the base64 encoding of the probe. Cheap defensive assertion against future "helpful" error refactors.

## Concurrency

Not safe for concurrent use. Each `Responder`, `Initiator`, and `CipherState` is owned by one goroutine. flynn's `CipherState` carries a mutable 64-bit nonce counter; concurrent access would corrupt it.

The expected use pattern (from #434):

- One `Responder` per `conn_id`, created on receipt of `noise_init`. `ReadInit` runs, `WriteResp` runs, both on the same dispatch goroutine. The `Responder` is then discarded.
- The pair of `CipherState`s is held on the per-`conn_id` state struct. The dispatch goroutine for that conn is the sole writer; no two goroutines touch the same `CipherState`. Re-key (#435) atomically swaps the pair on the same dispatch goroutine; no lock needed.

The wrapper adds no locks — adding them would mask programming errors at the caller layer. Doc-comments on each type pin the contract.

## Why `flynn/noise` over alternatives

- **Pure Go, no CGo.** Drops into the existing build with no platform-conditional compilation.
- **Last release Feb 2024 (v1.1.0).** Pinned as the version floor for the v2 release; a future bump is a security-review trigger.
- **Used by Tailscale's control protocol** (`tailscale.com/control/noise`) with the same cipher suite v2 specifies — production precedent, including the `IK` pattern in particular.
- **`HandshakeState.WriteMessage(out, payload)` accepts early-data** directly, which is exactly the shape v2 needs for `hello` / `hello_ack` piggybacking. A library without payload-arg support would have required application-layer framing.

Picked in [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) § *Why this Go library*. The narrow wrapper surface is the migration affordance if a future library replacement becomes necessary.

## Tests

Same-package (white-box) tests in `internal/noise/noise_test.go`. Stdlib `testing` only. `t.Parallel()` everywhere except where a parent test must serialize. No mocks — the test "phone" is a real `Initiator` built in the same test.

Helpers:

- `genKeypair(t)` — fresh X25519 priv/pub via `ecdh.X25519().GenerateKey(rand.Reader)`.
- `runHandshake(t, initPriv, respPriv, respPub, helloEarly, ackEarly)` — runs a complete handshake, returns the four CipherStates + both early-data payloads. Centralises happy-path setup.

Round-trip happy path:

- `TestRoundTrip_HandshakeCompletes` — full handshake with non-trivial early-data on both sides; asserts payloads echo and all four CipherStates non-nil.
- `TestRoundTrip_BothDirections` — **structural pin for the responder-side cs1/cs2 swap.** Exercises transport both directions: `iSend.Encrypt(x)` then `rRecv.Decrypt(c) == x`, and the mirror. Do not weaken.
- `TestRoundTrip_ManyFrames` — 32-iteration loop alternating directions; smoke against counter-handling bugs.

Tamper detection:

- `TestReadInit_RejectsTamperedMessage` — flip one byte in the middle of IK message 1; assert `ReadInit` errors, returned earlyData nil.
- `TestReadInit_RejectsTruncatedMessage` — truncate IK message 1 to half-length; assert error.
- `TestReadResp_RejectsTamperedMessage` — flip one byte in IK message 2; assert `ReadResp` errors and all three return values (earlyData, send, recv) are nil.

Wrong-key rejection:

- `TestHandshake_RejectsWrongResponderStatic` — initiator builds with the **wrong** responder pub; responder's `ReadInit` observes MAC failure when decrypting the encrypted-`s` field of message 1 (DH outputs disagree). Test doc-comment cites the spec reconciliation: the AC says "initiator's `ReadResp` errors" but the natural failure surface in IK is `ReadInit` because the responder never produces a `respMsg`. See [`codebase/433.md`](../codebase/433.md) § *Lessons learned* for the reconciliation rationale.
- `TestNewResponder_RejectsBadKeyLength` — table-driven nil / empty / 31 / 33 / 64 bytes; asserts `errors.Is(err, ErrInvalidKeyLength)` and `nil` receiver.
- `TestNewInitiator_RejectsBadKeyLength` — same matrix across both args (priv-bad, pub-bad, both-bad).

Transport-counter behaviour:

- `TestDecrypt_RejectsOutOfOrderFrame` — encrypt p1 then p2, deliver p2 first; assert MAC failure (the wire carries no nonce; `rRecv` expects nonce 0 but p2's tag was computed against nonce 1).
- `TestDecrypt_RejectsReplayedFrame` — encrypt p1, decrypt p1 (succeeds), decrypt p1 again (fails — counter advanced).

Peer-static accessor (#452):

- `TestResponder_PeerStatic_AfterReadInit_MatchesInitiatorStatic` — drives a real handshake (`NewInitiator` + `WriteInit` → `NewResponder` + `ReadInit`); asserts `PeerStatic()` returns the initiator's pub. Two consecutive calls, mutate the first, assert the second is unchanged — pins the defensive-copy contract. Third call after mutation confirms flynn's internal state is not aliased through the returned slice.
- `TestResponder_PeerStatic_BeforeReadInit_ReturnsZeroLength` — `NewResponder` only; assert `len(PeerStatic()) == 0` and no panic. Pins the wrapper's pre-handshake contract independently of flynn's internals.

Error-message hygiene:

- `TestErrorMessages_DoNotLeakPlaintextOrKey` — described above.

Tests deliberately not included:

- **State-machine misuse** (`ReadInit` called twice; `WriteResp` before `ReadInit`) — flynn enforces this with its own errors; wrapper just forwards. Asserting the exact error strings would lock the test suite to flynn internals without adding security or correctness coverage.
- **Empty-AD enforcement** — the wrapper API has no AD parameter; the type system enforces it. A test asserting `cs.Encrypt(p)` calls flynn with `ad=nil` would require reflection or a wrapped fake.
- **Concurrency safety** — the contract is "not safe for concurrent use." A `-race` test would confirm flynn's non-thread-safety, not anything this wrapper does.

## Out of scope (deferred or in follow-up tickets)

- **Per-`conn_id` state machine + handshake routing** in `internal/relay` — **#434**. First production consumer.
- **Re-key state machine + `rekey_request` envelope** — **#435**. Second production consumer.
- **JSON encoding of `hello` / `hello_ack` payloads into early-data bytes** — caller's concern (likely #434). The wrapper accepts and returns raw `[]byte` for early-data.
- **Base64 encoding of the wire `data` field** — `internal/relay` layer (#434).
- **PSK support** (Noise `IKpsk2` et al.) — not in v2's threat model. `Config.PresharedKey` and friends intentionally not exposed.
- **Multi-suite negotiation** — v2 is one suite, hard cutover ([ADR 024](../decisions/024-noise-ik-mobile-e2e.md) § *Why hard cutover*).
- **`CipherState` in-memory zeroisation** — Go gives no reliable primitive; the per-handshake CipherStates live on the per-`conn_id` state for the conn's lifetime and are released to GC on disconnect. Same posture as `internal/keys` for the static private key.
- **Finer-grained transport sentinels** (`ErrMACFailure`, `ErrCounterMismatch`) — deferred until a caller actually needs to branch.
- **Migration off `flynn/noise`** — narrow wrapper surface is the migration affordance, not a plan.

## Related

- [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) — Mobile Protocol v2 (Noise_IK) parent decision; pinned `flynn/noise` as the chosen Go library.
- [`features/keys-package.md`](keys-package.md) — produces the `[32]byte` static private key the responder consumes via `StaticKey.PrivateKey()`.
- [`features/pair-package.md`](pair-package.md) — QR payload publishes the responder's static public key + 64-bit fingerprint; the phone-side initiator feeds the same bytes into `NewInitiator(_, peerStaticPub)`.
- [`codebase/433.md`](../codebase/433.md) — per-ticket implementation summary.
- [`codebase/452.md`](../codebase/452.md) — `(*Responder).PeerStatic()` accessor; defensive-copy posture symmetric to `NewResponder`.
- [`codebase/438.md`](../codebase/438.md) — `internal/keys` static keypair primitive.
- [`codebase/439.md`](../codebase/439.md) — `internal/keys` filesystem hardening (hard prerequisite for any production consumer of `LoadOrCreate`).
- [`codebase/432.md`](../codebase/432.md) — `internal/pair` QR payload extension carrying `server_static_pubkey` + fingerprint.
- [`docs/protocol-mobile.md`](../../protocol-mobile.md) § *End-to-end encryption*, § *Handshake*, § *Transport*, § *Wire shapes* — wire-format source of truth.
- [`docs/specs/architecture/433-noise-ik-wrapper.md`](../../specs/architecture/433-noise-ik-wrapper.md) — architect spec.
- [`github.com/flynn/noise`](https://github.com/flynn/noise) — upstream library, v1.1.0 pinned.
