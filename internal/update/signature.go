package update

import (
	"crypto/ed25519"
	"errors"
	"fmt"
)

// ErrInvalidSignature is returned by VerifySignature when the detached
// signature does not verify against the supplied data and public key — or
// when the signature is not exactly ed25519.SignatureSize (64) bytes. A
// caller only ever branches "signed correctly vs not", so one sentinel
// covers both the wrong-length and non-verifying cases.
var ErrInvalidSignature = errors.New("checksums signature verification failed")

// ErrInvalidPublicKey is returned by VerifySignature when the supplied
// public key is not exactly ed25519.PublicKeySize (32) bytes. This guards
// against a malformed baked-in key constant so verification returns an error
// instead of ed25519.Verify panicking on a wrong-length key.
var ErrInvalidPublicKey = errors.New("invalid signing public key")

// VerifySignature returns nil iff sig is a valid Ed25519 signature over data
// under pub. It is a pure function: no I/O, no logging, no context.
//
// The signature is expected to be a raw 64-byte RFC 8032 Ed25519 signature
// over the exact bytes of data (for `pyry update`, the unmodified bytes of
// checksums.txt as served). There is no envelope, algorithm tag, or key id —
// a single algorithm and a single trust root means there is no
// algorithm-confusion or downgrade vector to defend against.
//
// Length is checked before ed25519.Verify so a malformed key or signature
// yields a sentinel error rather than a panic. The returned error is a clean
// sentinel-carrier: it never includes the signature or key bytes.
func VerifySignature(data, sig []byte, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key must be %d bytes, got %d: %w",
			ed25519.PublicKeySize, len(pub), ErrInvalidPublicKey)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("signature must be %d bytes, got %d: %w",
			ed25519.SignatureSize, len(sig), ErrInvalidSignature)
	}
	if !ed25519.Verify(pub, data, sig) {
		return ErrInvalidSignature
	}
	return nil
}
