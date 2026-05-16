# Spec — internal/noise: flynn/noise IK wrapper for handshake + AEAD transport (#433)

## Files to read first

The developer's turn-1 reading list. Lift these into context before writing any code.

- `internal/keys/static_key.go:43-65` — the `StaticKey` accessor contract that the responder side will consume. `PrivateKey()` returns `[32]byte` by value; convert to `[]byte` slice with `priv[:]` when handing to flynn's `DHKey{Private: ...}`. **The keys package is a hard dependency; do not re-implement key generation here.** The doc comment forbids logging the private bytes — that contract propagates to this wrapper (the package emits zero logs and the error paths must never include key bytes).
- `internal/keys/store.go:18-22` — the on-disk constants and naming (`algorithmName = "Noise_25519"`, `schemaVersion = 1`). Reference only — the wrapper does no I/O and consumes the raw 32-byte bytes returned by `StaticKey.PrivateKey()`.
- `internal/identity/server_id.go:1-85` — package-layout precedent for a small primitive package: pure type + small surface area in one file, no I/O sprawl. The wrapper follows this shape (one production file).
- `internal/pair/payload.go` — `internal/pair`'s package-doc cipher-suite naming style (see how the `Noise_25519` algorithm constant is named and documented). Mirror the same naming discipline in this package's doc comment.
- `docs/protocol-mobile.md:84-91` — § *Cipher suite*: `Noise_IK_25519_ChaChaPoly_BLAKE2s` named explicitly. **This is the only suite the package must support; reject any expansion.** The package-doc cipher-suite line in `internal/noise/noise.go` mirrors this verbatim.
- `docs/protocol-mobile.md:155-198` — § *Handshake* and § *Transport*: the IK message ordering (initiator writes IK msg 1 carrying early-data `hello`; responder reads, writes IK msg 2 carrying early-data `hello_ack`; both sides switch to transport mode). § *Transport* lines 195-201 are load-bearing for this wrapper's empty-AD design choice; mirror them in the package comment.
- `docs/protocol-mobile.md:280-298` — § *Wire shapes*: `data` field uses `base64.StdEncoding`. The wrapper itself does NOT do base64 (that lives in #434 at the routing-envelope layer); the wrapper consumes and produces raw bytes. Documented here so the developer doesn't accidentally pull base64 into the primitive.
- `docs/protocol-mobile.md:197` — *"Implementations MUST NOT pass a non-empty AD without a corresponding spec amendment."* This is enforced at the wrapper API: `CipherState.Encrypt(plaintext)` takes no AD parameter; there is no way to pass one without modifying the wrapper. The wrapper IS the enforcement point for this spec rule.
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md` § *Why this cipher suite* and § *Why per-binary, not per-phone, static keys on the binary side* — the rationale anchors. ADR-024 names `flynn/noise` as the chosen Go library and gives the threat model the wrapper sits inside.
- `go.mod` (top) — confirm the only new direct dependency added by this ticket is `github.com/flynn/noise`. No transitive deps from flynn outside the stdlib (flynn pulls in `golang.org/x/crypto` which is already an indirect dep here for the ChaCha20-Poly1305 / BLAKE2s impls; verify `go mod tidy` produces no surprises).

## Context

Mobile Protocol v2 (ADR-024, #430) replaces v1's plaintext-inside-WSS with end-to-end Noise_IK between phone and binary. Three siblings have already landed:

- **#438** — `internal/keys` primitive: `StaticKey` type, `LoadOrCreate`, X25519 generation.
- **#439** — `internal/keys` filesystem hardening: parent-dir `0700`, file `0600`, `O_NOFOLLOW` on read.
- **#432** — `internal/pair` extension: QR payload carries `server_static_pubkey` (raw 32-byte X25519 pub, base64) and a 64-bit BLAKE2s fingerprint.

This ticket lands the **Noise wrapper only**. The wrapper exposes initiator and responder state machines for the `Noise_IK_25519_ChaChaPoly_BLAKE2s` suite plus a `CipherState` that does post-handshake AEAD. It has **no consumers in this PR**: #434 (per-conn-id state machine + handshake routing in `internal/relay`) is the first caller, and #435 (re-key) is the second. The wrapper's narrow surface is load-bearing for those follow-ups — they should not be reaching into `flynn/noise` directly.

Why a wrapper rather than letting `internal/relay` import flynn directly:

1. **Single-point cipher-suite pin.** The string `Noise_IK_25519_ChaChaPoly_BLAKE2s` appears in exactly one place in the Go source (the package-level `cipherSuite` var). A future suite migration is one edit, not a grep-and-replace across `internal/relay` plus tests.
2. **Empty-AD invariant enforced at the type system.** `CipherState.Encrypt(plaintext)` has no AD parameter; callers cannot pass one even by accident. This is the enforcement of `docs/protocol-mobile.md:197` ("Implementations MUST NOT pass a non-empty AD without a corresponding spec amendment").
3. **Test fixture symmetry.** Mobile is the production initiator and the binary is the production responder, but both sides exist in Go so unit tests can run a full round-trip without a phone or a fake-phone harness. This is the same posture `internal/pair`'s tests take with the QR-render flow.

## Design

### Package location

`internal/noise/` (top-level under `internal/`).

The ticket body offers `internal/encryption/noise` as an alternative. Rejected — sibling packages (`internal/keys`, `internal/identity`, `internal/pair`, `internal/devices`) all live one level deep. Adding an `encryption/` intermediate for a single occupant breaks the established layout. If a second crypto package appears (e.g. an unrelated AEAD helper), it gets a peer top-level name; intermediates are not added retroactively.

### File layout

```
internal/noise/
  noise.go         All production code: cipher-suite constant, Responder,
                   Initiator, CipherState, NewResponder/NewInitiator,
                   ReadInit/WriteResp/WriteInit/ReadResp, Encrypt/Decrypt,
                   the X25519 pub-from-priv derivation helper, sentinels.
  noise_test.go    Round-trip + tamper + wrong-key + counter-mismatch tests.
```

Single production file. The ticket's ~120 LOC estimate fits comfortably; splitting into `responder.go` / `initiator.go` / `cipher.go` adds three-file overhead for no readability win (each "section" is ~30 LOC).

### Public API

```go
// Package noise wraps github.com/flynn/noise to expose a narrow, opinionated
// surface for the Noise_IK_25519_ChaChaPoly_BLAKE2s cipher suite used by
// Mobile Protocol v2 (see docs/protocol-mobile.md § End-to-end encryption
// and ADR-024). The package owns the cipher-suite pin and the empty-
// associated-data invariant; callers (internal/relay, #434) do not reach
// into github.com/flynn/noise directly.
//
// Empty associated-data is intentional. The v2 spec § Transport stipulates
// that the outer routing envelope (conn_id, frame) is plaintext and NOT
// bound to the AEAD; per-handshake key derivation isolates one session's
// ciphertext from another, and per-session nonce-counter discipline
// isolates frames within a session. CipherState.Encrypt and .Decrypt
// therefore take no AD parameter — there is no way to pass non-empty AD
// without modifying this package, which would itself require a spec
// amendment.
//
// SECURITY: this package consumes the binary's X25519 static private key
// (32 bytes) via NewResponder / NewInitiator. The package never logs and
// never wraps key bytes into error messages. Callers must uphold the same
// contract; see internal/keys for the on-disk side of the lifecycle.
package noise

// KeyLen is the length in bytes of an X25519 static or public key.
// All key-shaped arguments to this package must be exactly this length.
const KeyLen = 32

// ErrInvalidKeyLength is returned (wrapped) by NewResponder and
// NewInitiator when a key argument is not exactly KeyLen bytes. Match
// with errors.Is.
var ErrInvalidKeyLength = errors.New("noise: invalid key length")

// Responder runs the IK responder side of the handshake (reads message 1,
// writes message 2). After WriteResp returns, the handshake is complete
// and the returned CipherStates are used for transport.
//
// Methods on Responder are NOT safe for concurrent use. The expected call
// pattern is exactly ReadInit followed by WriteResp; calling out of order
// returns an error from flynn/noise wrapped with a "noise:" prefix.
type Responder struct {
    hs *flynnNoise.HandshakeState
}

// NewResponder constructs a Noise_IK responder configured with the
// binary's static X25519 private key. staticPriv must be exactly KeyLen
// bytes; the wrapper derives the matching public key via crypto/ecdh
// and supplies the full DHKey to flynn/noise.
//
// Returns (nil, ErrInvalidKeyLength-wrapped) if staticPriv is wrong-length.
// Returns (nil, wrapped error) if flynn's NewHandshakeState fails (which
// can happen on bad random source; production wires rand.Reader so this
// path is reached only in tests with a forced-failing Random).
func NewResponder(staticPriv []byte) (*Responder, error)

// ReadInit consumes IK message 1 (the wire bytes of noise_init's decoded
// `data` field — raw bytes, not base64) and returns the initiator's
// early-data payload (the v1-shaped `hello` envelope, JSON, UTF-8).
//
// On MAC failure, malformed message, or out-of-order call, returns
// (nil, wrapped error). The error wraps flynn/noise's underlying error
// and the wrapper adds no key material to the message.
func (r *Responder) ReadInit(initMsg []byte) (earlyData []byte, err error)

// WriteResp produces IK message 2, carrying earlyData (the v1-shaped
// `hello_ack` envelope, JSON, UTF-8) as the early-data payload, and
// returns the paired CipherStates for the post-handshake transport.
//
// send is the CipherState this side (responder) uses to encrypt outbound
// frames; recv is the CipherState this side uses to decrypt inbound
// frames. The wrapper hides flynn's k1/k2 ordering convention behind the
// send/recv naming. earlyData may be nil or zero-length.
func (r *Responder) WriteResp(earlyData []byte) (respMsg []byte, send, recv *CipherState, err error)

// Initiator runs the IK initiator side (writes message 1, reads message 2).
// Same concurrency contract as Responder.
type Initiator struct {
    hs *flynnNoise.HandshakeState
}

// NewInitiator constructs a Noise_IK initiator configured with the
// caller's static X25519 private key and the peer's (responder's) static
// X25519 public key. Both arguments must be exactly KeyLen bytes.
func NewInitiator(staticPriv, peerStaticPub []byte) (*Initiator, error)

// WriteInit produces IK message 1, carrying earlyData (the v1-shaped
// `hello` envelope, JSON, UTF-8) as the early-data payload.
func (i *Initiator) WriteInit(earlyData []byte) (initMsg []byte, err error)

// ReadResp consumes IK message 2, returns the responder's early-data
// payload, and returns the paired CipherStates. send/recv naming mirrors
// Responder.WriteResp — send is the initiator's encrypt-outbound state,
// recv is the initiator's decrypt-inbound state.
func (i *Initiator) ReadResp(respMsg []byte) (earlyData []byte, send, recv *CipherState, err error)

// CipherState wraps a *flynn/noise.CipherState. It is the post-handshake
// AEAD that encrypts and decrypts transport frames. Each CipherState
// carries a monotonic 64-bit counter; callers must use it strictly in
// order (every Encrypt produces the next counter; every Decrypt verifies
// against the next expected counter) and must not share a CipherState
// across goroutines.
//
// Encrypt and Decrypt are implemented as flynn's Encrypt(out=nil, ad=nil,
// plaintext) and Decrypt(out=nil, ad=nil, ciphertext); the empty
// associated-data is the spec-mandated v2 transport invariant.
type CipherState struct {
    cs *flynnNoise.CipherState
}

// Encrypt seals plaintext under the next nonce and returns ciphertext.
// On flynn/noise error (e.g. AEAD failure on a corrupt internal counter)
// returns (nil, wrapped error). Plaintext length must not exceed 65519
// bytes (Noise's 65535-byte transport-message limit minus the 16-byte
// AEAD tag); flynn rejects oversize plaintext with its own error which
// the wrapper forwards.
func (c *CipherState) Encrypt(plaintext []byte) (ciphertext []byte, err error)

// Decrypt opens ciphertext under the next expected nonce and returns
// plaintext. On MAC failure, counter mismatch, or any other AEAD error
// returns (nil, wrapped error).
func (c *CipherState) Decrypt(ciphertext []byte) (plaintext []byte, err error)
```

`flynnNoise` is the alias for the import `noise "github.com/flynn/noise"` — the import is aliased to avoid name collision with our own package name.

No other exported symbols. The wrapper does not re-export `HandshakeState`, `Config`, `HandshakePattern`, `CipherSuite`, `DHKey`, or any flynn type. A future migration off `flynn/noise` (called out as a possibility in the ticket's Technical Notes) touches only `noise.go`; no caller import path or type name has to change.

### Cipher suite

```go
// cipherSuite pins the v2 cipher suite. The only place in the Go source
// where the suite name string appears.
var cipherSuite = flynnNoise.NewCipherSuite(
    flynnNoise.DH25519,
    flynnNoise.CipherChaChaPoly,
    flynnNoise.HashBLAKE2s,
)
```

Documented in the package comment as `Noise_IK_25519_ChaChaPoly_BLAKE2s` and linked to `docs/protocol-mobile.md § End-to-end encryption`.

### Constructor wiring

`NewResponder(staticPriv)` shape (signature only — body is ~12 lines):

1. `if len(staticPriv) != KeyLen` → `fmt.Errorf("noise: responder static key: %w", ErrInvalidKeyLength)`.
2. Derive the public half: `priv, err := ecdh.X25519().NewPrivateKey(staticPriv)`; on error wrap as `"noise: derive public from static private: %w"`. The derived bytes are `priv.PublicKey().Bytes()`.
3. Build the flynn config:
   ```go
   cfg := flynnNoise.Config{
       CipherSuite:   cipherSuite,
       Random:        rand.Reader,                      // crypto/rand
       Pattern:       flynnNoise.HandshakeIK,
       Initiator:     false,
       StaticKeypair: flynnNoise.DHKey{Private: append([]byte(nil), staticPriv...), Public: pub},
   }
   ```
   `append([]byte(nil), ...)` defensively copies the caller's slice so a subsequent caller-side mutation (or zeroisation, in a hypothetical future StaticKey hardening) cannot corrupt the handshake state mid-flight.
4. `hs, err := flynnNoise.NewHandshakeState(cfg)`; wrap as `"noise: new responder handshake state: %w"`.
5. Return `&Responder{hs: hs}`.

`NewInitiator(staticPriv, peerStaticPub)` is the same shape with two key-length checks, `Initiator: true`, and an additional `PeerStatic: append([]byte(nil), peerStaticPub...)` field. The same defensive copy applies. Length check on `peerStaticPub` uses the same `ErrInvalidKeyLength` sentinel; the wrapping prefix names which key tripped.

`Random: rand.Reader` is explicit. flynn's default behaviour when `Random` is nil is documented but easy to miss; setting it explicitly removes one class of "did the default change?" question for future maintainers.

### Handshake methods

`Responder.ReadInit(initMsg)` body (~5 lines):

```go
payload, _, _, err := r.hs.ReadMessage(nil, initMsg)
if err != nil {
    return nil, fmt.Errorf("noise: responder ReadInit: %w", err)
}
return payload, nil
```

The two `*CipherState` returns are nil on the responder's first read (IK has two messages; CipherStates surface only on the side calling the LAST message — that's responder's WriteMessage, not its ReadMessage). Discarded explicitly via blank identifiers.

`Responder.WriteResp(earlyData)` body (~8 lines):

```go
msg, cs1, cs2, err := r.hs.WriteMessage(nil, earlyData)
if err != nil {
    return nil, nil, nil, fmt.Errorf("noise: responder WriteResp: %w", err)
}
// flynn returns (k1, k2) where k1 carries initiator→responder traffic and
// k2 carries responder→initiator traffic. For the RESPONDER, that means
// cs1 = recv (initiator's send arrives here), cs2 = send (responder's
// outbound). The wrapper swaps to expose (send, recv) consistently for
// both sides — see the matching Initiator.ReadResp for the non-swapped
// case.
return msg, &CipherState{cs: cs2}, &CipherState{cs: cs1}, nil
```

`Initiator.WriteInit(earlyData)` is the mirror — discards the two nil CipherStates from the first write.

`Initiator.ReadResp(respMsg)` body (~8 lines):

```go
payload, cs1, cs2, err := i.hs.ReadMessage(nil, respMsg)
if err != nil {
    return nil, nil, nil, fmt.Errorf("noise: initiator ReadResp: %w", err)
}
// For the INITIATOR, cs1 = send (initiator→responder is k1) and
// cs2 = recv (responder→initiator is k2). No swap.
return payload, &CipherState{cs: cs1}, &CipherState{cs: cs2}, nil
```

The swap-on-responder-only convention is the wrapper's most subtle invariant. It is verified by the round-trip test below (`TestRoundTrip_BothDirections`): the test calls `responder.send.Encrypt(x)` and asserts `initiator.recv.Decrypt(c) == x`. If the swap is wrong, the test fails on the very first decrypt. This is the structural anchor — there is no need for a comment-only assertion of the convention because the test pins it deterministically.

### CipherState methods

```go
func (c *CipherState) Encrypt(plaintext []byte) ([]byte, error) {
    out, err := c.cs.Encrypt(nil, nil, plaintext)
    if err != nil {
        return nil, fmt.Errorf("noise: encrypt: %w", err)
    }
    return out, nil
}

func (c *CipherState) Decrypt(ciphertext []byte) ([]byte, error) {
    out, err := c.cs.Decrypt(nil, nil, ciphertext)
    if err != nil {
        return nil, fmt.Errorf("noise: decrypt: %w", err)
    }
    return out, nil
}
```

The first `nil` is flynn's `out` parameter (output buffer to append to — `nil` means allocate a fresh slice). The second `nil` is the associated-data argument — the v2 spec mandate. No third option.

### Concurrency model

None within the package. No goroutines, no channels, no locks.

The expected use pattern (from #434):

- One `Responder` per `conn_id`, created on receipt of `noise_init`. ReadInit runs, WriteResp runs, both on the same dispatch goroutine. The Responder is then discarded; the two CipherStates live on for the conn's lifetime.
- The pair of CipherStates is held on the per-conn-id state struct. The send-CipherState is used by the dispatch goroutine to encrypt outbound responses; the recv-CipherState is used to decrypt inbound `noise_msg` frames. Because each conn_id has its own dispatch loop, no two goroutines touch the same CipherState. Re-key (#435) atomically swaps the pair on the same dispatch goroutine; no lock needed.

This contract is documented on each type. The wrapper does NOT add locking — adding it would mask programming errors at #434/#435.

### Error handling

| Sentinel | Returned when | Caller action |
|---|---|---|
| `ErrInvalidKeyLength` | `NewResponder` / `NewInitiator` got a key argument that is not exactly 32 bytes | Caller bug — fix the call site |
| Bare wrapped `fmt.Errorf` (no sentinel) | All flynn/noise errors (MAC failure, out-of-order handshake call, AEAD failure, counter exhaustion, oversize message, etc.) | Inspect by `err.Error()` if needed for logging; the wire-level response (e.g. close code 4426 for handshake failure) is the caller's (#434) decision |

Single sentinel keeps the surface small. Adding finer-grained sentinels for MAC-failure vs counter-mismatch is **deliberately deferred** — the caller (#434) closes the WS on any handshake or transport error with a single close-code mapping; it does not branch on the specific noise-layer reason. If a future caller needs to distinguish, the sentinels can be added without breaking the current ones.

Error message hygiene: no error message in this package includes plaintext, ciphertext, key bytes, or early-data payload. The wrapped flynn error message names the operation (e.g. `noise: MAC verification failed`) but never echoes the input bytes. A test pins this by asserting the error string does not contain the high-entropy probe bytes used in the tampered-message test (see § Testing strategy).

### Dependency wiring

`go.mod` additions:

- `require github.com/flynn/noise v1.1.0` (the latest stable release; pinned, not `latest`). The ticket calls this out as the version floor for v2 release.
- `go mod tidy` will pull `golang.org/x/crypto` from `// indirect` to direct (flynn imports `golang.org/x/crypto/chacha20poly1305` and `golang.org/x/crypto/blake2s` from x/crypto). The current `go.mod` already lists `golang.org/x/crypto v0.51.0` as indirect for unrelated reasons; whether `tidy` promotes it is automatic, do not hand-edit.
- No CGo. No platform-specific code paths.

Verify with `go mod tidy && go build ./... && go test -race ./internal/noise/...` post-merge into the dependency graph.

If `go mod tidy` produces any unexpected new direct dep beyond `flynn/noise`, that is a deviation from the ticket's "single new direct dependency" AC and should pause for review rather than be auto-committed.

## Testing strategy

Same-package (white-box) tests. Table-driven where applicable. `testing` stdlib only. No mocks — the test "phone" is a real `Initiator` constructed in the same test function. File: `internal/noise/noise_test.go`.

Bullet-pointed scenarios — developer writes idiomatic Go test code.

### Round-trip happy path

- **TestRoundTrip_HandshakeCompletes.** Generate a fresh responder static keypair via `crypto/ecdh.X25519().GenerateKey(rand.Reader)`. Construct `responder := NewResponder(respPriv)` and `initiator := NewInitiator(initPriv, respPub)`. Run the handshake: `initMsg, _ := initiator.WriteInit([]byte("hello-early"))`; `gotEarly, _ := responder.ReadInit(initMsg)`; assert `gotEarly == "hello-early"`. `respMsg, rSend, rRecv, _ := responder.WriteResp([]byte("hello-ack-early"))`; `gotAck, iSend, iRecv, _ := initiator.ReadResp(respMsg)`; assert `gotAck == "hello-ack-early"`. Assert all four returned CipherStates are non-nil.

- **TestRoundTrip_BothDirections.** Same setup as above. After handshake, exercise transport in both directions: `c1, _ := iSend.Encrypt([]byte("from initiator"))`; assert `rRecv.Decrypt(c1)` returns `"from initiator"`. `c2, _ := rSend.Encrypt([]byte("from responder"))`; assert `iRecv.Decrypt(c2)` returns `"from responder"`. **This test is the structural pin for the responder-side k1/k2 swap** — if `WriteResp` returned `(cs1, cs2)` instead of `(cs2, cs1)`, this test fails on the first Decrypt with a MAC error. Do not weaken or remove.

- **TestRoundTrip_ManyFrames.** Same setup, then loop 32 times sending alternating directions; assert each Decrypt round-trips. Smoke-test against counter-handling bugs in the wrapper (none expected — flynn owns the counter — but the test is cheap and pins the contract).

### Tamper detection

- **TestReadInit_RejectsTamperedMessage.** Run `initiator.WriteInit` to produce `initMsg`. Flip a single byte somewhere in the middle of `initMsg` (e.g. byte at index `len(initMsg)/2 ^= 0x01`). Call `responder.ReadInit(tampered)`; assert err is non-nil. Assert returned earlyData is nil. The exact error class is flynn's — the wrapper just forwards.

- **TestReadInit_RejectsTruncatedMessage.** Truncate `initMsg` to half its length; assert `responder.ReadInit` errors.

- **TestReadResp_RejectsTamperedMessage.** Symmetric: complete `WriteInit` and `ReadInit`, then run `responder.WriteResp` to get `respMsg`; flip a byte; assert `initiator.ReadResp(tampered)` errors.

### Wrong-key rejection

- **TestHandshake_RejectsWrongResponderStatic.** Generate **two** responder keypairs: `(R_priv, R_pub)` and `(F_priv, F_pub)`. Build the real responder with `R_priv`. Build the initiator with `peerStaticPub = F_pub` (the WRONG responder pub). Run `initiator.WriteInit(...)` — succeeds (this side has no way to know the pub is wrong; it just encrypts to F_pub). Run `responder.ReadInit(initMsg)` — assert err is non-nil. The MAC failure surfaces here because the responder reads with `R_priv` but the initiator's `es`/`ss` were derived against `F_pub`; DH outputs disagree; tag verification fails when flynn decrypts the encrypted-s field on message 1.

  **Note on the AC's wording.** The acceptance criterion says *"ReadResp errors on the initiator side after MAC failure."* The natural failure surface for a wrong-peer-static is the responder's `ReadInit`, because that is the first DH-derived decryption in the IK pattern. The responder never produces a `respMsg`, so the initiator's `ReadResp` is never reached. The test as specified above meets the spirit of the AC (handshake rejected, MAC failure observed) while accurately reflecting the IK message flow. If the developer prefers, they may additionally assert that calling `initiator.ReadResp([]byte{...arbitrary noise...})` errors — that exercise is acceptable but does not add coverage beyond `TestReadResp_RejectsTamperedMessage`.

- **TestNewInitiator_RejectsBadKeyLength.** Table-driven: `nil`, `[]byte{}`, 31 bytes, 33 bytes, 64 bytes. For each `(staticPriv, peerStaticPub)` combination where at least one is bad-length, assert `errors.Is(err, ErrInvalidKeyLength)` and returned initiator is nil. Cover the symmetric `NewResponder` rejection in a second matrix.

### Transport-counter behaviour

- **TestDecrypt_RejectsOutOfOrderFrame.** Complete a handshake; on the sender side, `iSend.Encrypt(p1) → c1`, `iSend.Encrypt(p2) → c2`. On the receiver side, call `rRecv.Decrypt(c2)` BEFORE `rRecv.Decrypt(c1)`. Assert error is non-nil (the AEAD tag on `c2` was computed against nonce 1; rRecv expects nonce 0; tag verification fails). Assert returned plaintext is nil. Per `docs/protocol-mobile.md:199` the wire does not carry the nonce, so out-of-order delivery is necessarily a MAC failure at the receiver — this test pins that property.

- **TestDecrypt_RejectsReplayedFrame.** Complete a handshake; `iSend.Encrypt(p1) → c1`; `rRecv.Decrypt(c1)` succeeds; immediately call `rRecv.Decrypt(c1)` again. Assert error is non-nil (the counter has advanced; the second decrypt expects nonce 1 but `c1` was sealed against nonce 0). Replay defence is structural in Noise; the test confirms the wrapper does not subvert it.

### Error-message hygiene

- **TestErrorMessages_DoNotLeakPlaintextOrKey.** Generate a unique high-entropy probe byte sequence (e.g. 16 bytes from `crypto/rand`). Use it as the plaintext to `iSend.Encrypt`. Tamper the resulting ciphertext (flip a byte). Call `rRecv.Decrypt(tampered)`; capture the returned error. Assert `!strings.Contains(err.Error(), hex.EncodeToString(probe))` and `!strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(probe))`. Single defensive assertion against future "helpful" error refactors that might pull plaintext into the message. Cheap, pins the contract.

### Tests deliberately NOT included

- **State-machine misuse (e.g. `ReadInit` called twice; `WriteResp` called before `ReadInit`).** flynn/noise enforces this with its own `errors.New("noise: unexpected call to ReadMessage should be WriteMessage")` etc. The wrapper forwards these errors verbatim; the wrapper's contract is "use in order or get an error from flynn," and that contract is already validated by `TestRoundTrip_HandshakeCompletes`. Adding redundant wrapper-side tests would just lock the test suite to flynn's exact error strings — brittle, no security or correctness coverage gain.
- **Empty-AD enforcement.** The wrapper API has no AD parameter; there is no way to pass non-empty AD without modifying `noise.go`. A test asserting `cs.Encrypt(p)` calls flynn with `ad=nil` would have to use reflection or a wrapped flynn fake — not justified for a property the type system already enforces.
- **Concurrency safety.** The contract is "not safe for concurrent use." A `-race` test that hammers a single CipherState from two goroutines would simply confirm flynn's non-thread-safety, not anything the wrapper does. Out of scope.

All tests pass `go vet`, `staticcheck`, and `go test -race`. No flakes — every test is deterministic given a fixed RNG; the use of `rand.Reader` is fine because the assertions never depend on specific key values, only on success/failure of the handshake + transport.

## Open questions

None. Every decision (package location, single-file layout, send/recv naming, defensive slice copy, error sentinel granularity, AD enforcement at the type system, out-of-order surface in the wrong-key test) is resolved inline. No clarification needed from PO or developer at implementation time.

## Out of scope (filed elsewhere or deferred)

- **Per-conn-id state machine + handshake routing** (`internal/relay`) — **#434**. Consumes this wrapper.
- **Re-key timer + `rekey_request` envelope** — **#435**. Consumes this wrapper (creates new Responder per re-key).
- **Application envelope (JSON `hello`/`hello_ack`) encoding into early-data bytes** — caller's concern. The wrapper accepts and returns raw `[]byte` for early-data; it does not parse JSON.
- **Base64 encoding of `data` field on the wire** — `internal/relay` layer (#434). The wrapper consumes and produces raw bytes; base64 lives at the routing-envelope JSON boundary.
- **Migration off `flynn/noise`** (to e.g. tailscale.com/control/noise or a from-scratch implementation) — narrow wrapper surface is the migration affordance. Not planned.
- **PSK support** (Noise IKpsk2 et al.) — not in v2's threat model (ADR-024). The `Config.PresharedKey` and related fields are intentionally not exposed.
- **Multi-suite negotiation** — v2 is one suite, hard cutover. ADR-024 § *Why hard cutover*.
- **CipherState zeroisation** — Go has no reliable zeroisation primitive; the per-handshake CipherStates live on the per-conn-id state for the conn's lifetime and are dropped by GC on disconnect. Mirrors `internal/keys`'s same Out-of-scope finding for the static private key.

## Security review

**Verdict:** PASS

The wrapper sits at the cryptographic boundary of Mobile Protocol v2. It is small, has one external dependency (`flynn/noise`), and most threats are upstream (caller wires the keys; spec defines the protocol) or inside flynn (the IK pattern + AEAD primitives). The findings below walk the categories in `agents/architect/security-review.md`.

**Findings:**

- **[Trust boundaries]**
  1. **Caller → wrapper at key arguments.** `NewResponder(staticPriv)` and `NewInitiator(staticPriv, peerStaticPub)` accept raw `[]byte`. Boundary defence: length check at exactly 32 bytes (rejected with `ErrInvalidKeyLength` before any flynn call). The wrapper defensively `append([]byte(nil), ...)` copies both keys into the `DHKey{Private, Public}` struct so a caller-side mutation after construction cannot reach into the live handshake state. The check + copy run before `flynn.NewHandshakeState`, so a malformed input cannot inject partially-initialised state into flynn's internals.
  2. **Wire → wrapper at handshake/transport bytes.** `ReadInit`, `ReadResp`, `Decrypt` accept raw `[]byte` from the wire (decoded from `data` field's base64). Boundary defence is delegated to flynn/noise: every received message is AEAD-verified (handshake messages via `EncryptAndHash`/`DecryptAndHash` in the symmetric state; transport via ChaChaPoly tag). MAC failure → flynn returns an error → wrapper wraps with `"noise:"` prefix → caller sees a non-nil error and decides on close-code (4426 for handshake, 4421 for transport per spec). No bytes from a failed verification reach the caller's plaintext path. **No length cap is enforced at the wrapper layer.** flynn enforces `MaxMsgLen` (65535) internally; oversize input is rejected by flynn with an error. The caller (#434) is responsible for any earlier WS-frame-size cap.
- **[Tokens, secrets, credentials]**
  - The wrapper handles the binary's static X25519 private key (32 bytes), the peer's static public key (32 bytes), and ephemeral keys (generated inside flynn per handshake from `rand.Reader`). All three classes never appear in logs (the package emits zero log calls; doc-comment forbids future additions) and never appear in error messages (the wrapper's wraps prefix flynn's error with `"noise:"` and add no fields; `TestErrorMessages_DoNotLeakPlaintextOrKey` pins this against future refactors using a probe-byte assertion).
  - The wrapper does NOT mint, persist, or rotate keys. Minting/persistence is `internal/keys`; rotation is v3.
  - The wrapper does NOT compare keys for equality (no `subtle.ConstantTimeCompare` is required because no comparison is performed). The handshake's `es`/`ss`/`se`/`ee` DH steps inside flynn use constant-time x25519 from `golang.org/x/crypto/curve25519`.
- **[File operations]** N/A. The package does no I/O.
- **[Subprocess / external command execution]** N/A. No subprocess, no env-var read, no shell-out.
- **[Cryptographic primitives]**
  - **Cipher suite:** `Noise_IK_25519_ChaChaPoly_BLAKE2s`. Standards-grade, multiple production precedents (Tailscale control protocol, WireGuard-adjacent). ADR-024 § *Why this cipher suite* documents the rationale. The wrapper pins the suite at exactly one source location (`var cipherSuite`).
  - **RNG:** `rand.Reader` (crypto/rand) explicitly wired into `Config.Random` for both responder and initiator. Ephemeral keys flow from this source. Production has no path that substitutes a different RNG.
  - **Key derivation:** Owned by flynn (HKDF-based via `symmetricState.Split`). The wrapper does not derive anything.
  - **Empty associated-data:** intentional and spec-mandated. Enforced at the type system (`CipherState.Encrypt` and `.Decrypt` take no AD parameter). The package comment explains the rationale (per-handshake key derivation + per-session nonce counter make outer-envelope binding redundant). Section reference in `docs/protocol-mobile.md:197` is the spec amendment gate.
  - **Constant-time comparison:** N/A at the wrapper layer; flynn's internal MAC verification uses `crypto/subtle`.
  - **Counter reuse / nonce management:** Owned by flynn's `CipherState`. The wrapper does not touch the counter. The replay test (`TestDecrypt_RejectsReplayedFrame`) confirms the structural property.
  - **In-memory zeroisation:** Not performed. Go does not give a reliable primitive. Same posture as `internal/keys`. The per-handshake CipherStates live on the per-conn-id state and are released to GC on disconnect; the static private key is held for the daemon's lifetime by design (one keypair per daemon, no rotation in v2).
- **[Network & I/O]** N/A. The wrapper consumes and produces `[]byte`. The `internal/relay` layer (#434) owns the network read/write.
- **[Error messages, logs, telemetry]**
  - Zero log calls. Doc-comment forbids future additions for key material.
  - Error messages are prefixed `"noise:"` and include the operation name (`ReadInit`, `WriteResp`, `Decrypt`, etc.) but never the input bytes or any derived state. The hygiene test pins this.
  - Telemetry: N/A.
- **[Concurrency]** Single-threaded contract. Each `Responder`, `Initiator`, and `CipherState` is owned by one goroutine. The caller (#434) maintains a single dispatch loop per conn_id, satisfying this contract structurally. The wrapper adds no locks — adding them would mask programming errors at the caller. The doc-comment on each type states the contract.
- **[Threat model alignment]**
  - **`docs/protocol-mobile.md` threat #1 (Relay operator MITM):** addressed structurally by encrypting the inner frame end-to-end. The wrapper IS the encryption — every transport frame between phone and binary flows through `CipherState.Encrypt`/`.Decrypt`. The empty-AD invariant is correct under the spec's reasoning (per-handshake keys + per-session counter discipline). The wrapper enforces it at the type system.
  - **`docs/protocol-mobile.md` threat #8 (Static-key compromise, severity: high):** out of scope for this ticket (the threat is owned by `internal/keys` for on-disk hardening and by v3 for rotation). The wrapper's contribution to the mitigation is the no-log/no-error-leak posture, pinned by `TestErrorMessages_DoNotLeakPlaintextOrKey`.
  - **`docs/protocol-mobile.md` § *Handshake failures* (4426 close on MAC failure):** the wrapper surfaces a non-nil error on MAC failure with no caller-actionable information beyond "handshake failed"; the caller (#434) maps to the close code. This is the right separation of concerns — the wrapper does not know about WS close codes.
  - **`docs/protocol-mobile.md` § *Wire shapes* (65519-byte application-envelope cap):** the cap lives at the application-envelope layer in #434, not in the wrapper. flynn's internal 65535-byte transport-message cap is the lower bound; oversize plaintext is rejected by flynn's `Encrypt`.
  - **ADR-024 § *Why per-binary, not per-phone, static keys*:** the wrapper is agnostic to this choice — it takes one private key per `NewResponder` call and one peer public per `NewInitiator` call. No assumption about per-phone vs per-binary leaks into the API.
- **[Dependency hygiene]**
  - `github.com/flynn/noise v1.1.0` is the only new direct dependency. Pinned. ADR-024 names it as the chosen library. Last release Feb 2024; pure Go, no CGo; ~1.2k stars; used by several Go projects in production. No supply-chain red flags.
  - `golang.org/x/crypto` is pulled transitively for ChaChaPoly and BLAKE2s primitives; already an indirect dep, may be promoted to direct by `go mod tidy`. No new transitive concern.
  - The ticket body says *"If the version moves between this ticket and #434/#435, that's a security-review trigger."* This is captured here — any future PR that bumps `github.com/flynn/noise` MUST re-run the security review pass on the consumers (#434/#435) and on this wrapper.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-16
