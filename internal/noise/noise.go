// Package noise wraps github.com/flynn/noise to expose a narrow, opinionated
// surface for the Noise_IK_25519_ChaChaPoly_BLAKE2s cipher suite used by
// Mobile Protocol v2 (see docs/protocol-mobile.md § End-to-end encryption
// and docs/knowledge/decisions/024-noise-ik-mobile-e2e.md). The package owns
// the cipher-suite pin and the empty-associated-data invariant; callers
// (internal/relay) do not reach into github.com/flynn/noise directly.
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

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"

	flynnNoise "github.com/flynn/noise"
)

// KeyLen is the length in bytes of an X25519 static or public key.
// All key-shaped arguments to this package must be exactly this length.
const KeyLen = 32

// ErrInvalidKeyLength is returned (wrapped) by NewResponder and
// NewInitiator when a key argument is not exactly KeyLen bytes. Match
// with errors.Is.
var ErrInvalidKeyLength = errors.New("noise: invalid key length")

// cipherSuite pins the v2 cipher suite. The only place in the Go source
// where the suite name appears.
//
// Suite: Noise_IK_25519_ChaChaPoly_BLAKE2s.
var cipherSuite = flynnNoise.NewCipherSuite(
	flynnNoise.DH25519,
	flynnNoise.CipherChaChaPoly,
	flynnNoise.HashBLAKE2s,
)

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
func NewResponder(staticPriv []byte) (*Responder, error) {
	if len(staticPriv) != KeyLen {
		return nil, fmt.Errorf("noise: responder static key: %w", ErrInvalidKeyLength)
	}
	priv, err := ecdh.X25519().NewPrivateKey(staticPriv)
	if err != nil {
		return nil, fmt.Errorf("noise: derive public from static private: %w", err)
	}
	cfg := flynnNoise.Config{
		CipherSuite: cipherSuite,
		Random:      rand.Reader,
		Pattern:     flynnNoise.HandshakeIK,
		Initiator:   false,
		StaticKeypair: flynnNoise.DHKey{
			Private: append([]byte(nil), staticPriv...),
			Public:  priv.PublicKey().Bytes(),
		},
	}
	hs, err := flynnNoise.NewHandshakeState(cfg)
	if err != nil {
		return nil, fmt.Errorf("noise: new responder handshake state: %w", err)
	}
	return &Responder{hs: hs}, nil
}

// ReadInit consumes IK message 1 (the wire bytes of noise_init's decoded
// `data` field — raw bytes, not base64) and returns the initiator's
// early-data payload.
//
// On MAC failure, malformed message, or out-of-order call, returns
// (nil, wrapped error). The error wraps flynn/noise's underlying error
// and the wrapper adds no key material to the message.
func (r *Responder) ReadInit(initMsg []byte) ([]byte, error) {
	payload, _, _, err := r.hs.ReadMessage(nil, initMsg)
	if err != nil {
		return nil, fmt.Errorf("noise: responder ReadInit: %w", err)
	}
	return payload, nil
}

// PeerStatic returns a copy of the initiator's 32-byte X25519 static
// public key as learned from IK message 1. Callable only after ReadInit
// has returned nil; flynn/noise's documented contract is "an error to
// call before a handshake message containing a static key has been
// read". The wrapper does not panic on misuse — callers that need
// stricter enforcement should track state in their own session struct.
//
// The returned slice is a fresh allocation; mutating it does not affect
// the underlying HandshakeState. Mirrors NewResponder's defensive-copy
// posture for StaticKeypair.Private.
func (r *Responder) PeerStatic() []byte {
	return append([]byte(nil), r.hs.PeerStatic()...)
}

// WriteResp produces IK message 2, carrying earlyData as the early-data
// payload, and returns the paired CipherStates for the post-handshake
// transport. send is the CipherState this side (responder) uses to encrypt
// outbound frames; recv is the CipherState this side uses to decrypt
// inbound frames. earlyData may be nil or zero-length.
func (r *Responder) WriteResp(earlyData []byte) (respMsg []byte, send, recv *CipherState, err error) {
	msg, cs1, cs2, err := r.hs.WriteMessage(nil, earlyData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("noise: responder WriteResp: %w", err)
	}
	// flynn returns (cs1, cs2) where cs1 carries initiator→responder
	// traffic and cs2 carries responder→initiator traffic. For the
	// RESPONDER, that means cs1 = recv, cs2 = send. Swap to expose the
	// (send, recv) pair consistently for both sides.
	return msg, &CipherState{cs: cs2}, &CipherState{cs: cs1}, nil
}

// Initiator runs the IK initiator side (writes message 1, reads message 2).
// Same concurrency contract as Responder.
type Initiator struct {
	hs *flynnNoise.HandshakeState
}

// NewInitiator constructs a Noise_IK initiator configured with the
// caller's static X25519 private key and the peer's (responder's) static
// X25519 public key. Both arguments must be exactly KeyLen bytes.
func NewInitiator(staticPriv, peerStaticPub []byte) (*Initiator, error) {
	if len(staticPriv) != KeyLen {
		return nil, fmt.Errorf("noise: initiator static key: %w", ErrInvalidKeyLength)
	}
	if len(peerStaticPub) != KeyLen {
		return nil, fmt.Errorf("noise: initiator peer static pub: %w", ErrInvalidKeyLength)
	}
	priv, err := ecdh.X25519().NewPrivateKey(staticPriv)
	if err != nil {
		return nil, fmt.Errorf("noise: derive public from static private: %w", err)
	}
	cfg := flynnNoise.Config{
		CipherSuite: cipherSuite,
		Random:      rand.Reader,
		Pattern:     flynnNoise.HandshakeIK,
		Initiator:   true,
		StaticKeypair: flynnNoise.DHKey{
			Private: append([]byte(nil), staticPriv...),
			Public:  priv.PublicKey().Bytes(),
		},
		PeerStatic: append([]byte(nil), peerStaticPub...),
	}
	hs, err := flynnNoise.NewHandshakeState(cfg)
	if err != nil {
		return nil, fmt.Errorf("noise: new initiator handshake state: %w", err)
	}
	return &Initiator{hs: hs}, nil
}

// WriteInit produces IK message 1, carrying earlyData as the early-data
// payload.
func (i *Initiator) WriteInit(earlyData []byte) ([]byte, error) {
	msg, _, _, err := i.hs.WriteMessage(nil, earlyData)
	if err != nil {
		return nil, fmt.Errorf("noise: initiator WriteInit: %w", err)
	}
	return msg, nil
}

// ReadResp consumes IK message 2, returns the responder's early-data
// payload, and returns the paired CipherStates. send is the initiator's
// encrypt-outbound state; recv is the initiator's decrypt-inbound state.
func (i *Initiator) ReadResp(respMsg []byte) (earlyData []byte, send, recv *CipherState, err error) {
	payload, cs1, cs2, err := i.hs.ReadMessage(nil, respMsg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("noise: initiator ReadResp: %w", err)
	}
	// For the INITIATOR, cs1 = send (initiator→responder is cs1) and
	// cs2 = recv (responder→initiator is cs2). No swap.
	return payload, &CipherState{cs: cs1}, &CipherState{cs: cs2}, nil
}

// CipherState wraps a *flynn/noise.CipherState. It is the post-handshake
// AEAD that encrypts and decrypts transport frames. Each CipherState
// carries a monotonic 64-bit counter; callers must use it strictly in
// order and must not share a CipherState across goroutines.
//
// Encrypt and Decrypt invoke flynn's Encrypt(out=nil, ad=nil, plaintext)
// and Decrypt(out=nil, ad=nil, ciphertext); the empty associated-data is
// the spec-mandated v2 transport invariant.
type CipherState struct {
	cs *flynnNoise.CipherState
}

// Encrypt seals plaintext under the next nonce and returns ciphertext.
// On flynn/noise error returns (nil, wrapped error).
func (c *CipherState) Encrypt(plaintext []byte) ([]byte, error) {
	out, err := c.cs.Encrypt(nil, nil, plaintext)
	if err != nil {
		return nil, fmt.Errorf("noise: encrypt: %w", err)
	}
	return out, nil
}

// Decrypt opens ciphertext under the next expected nonce and returns
// plaintext. On MAC failure, counter mismatch, or any other AEAD error
// returns (nil, wrapped error).
func (c *CipherState) Decrypt(ciphertext []byte) ([]byte, error) {
	out, err := c.cs.Decrypt(nil, nil, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("noise: decrypt: %w", err)
	}
	return out, nil
}
