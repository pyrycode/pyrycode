// Package keys owns the binary's persistent X25519 static keypair used by
// Mobile Protocol v2 (Noise_IK). Each pyrycode daemon holds one keypair per
// daemon-name, persisted at ~/.pyry/<daemon-name>/static_key.json. The pure
// type, validator, and key generator live alongside the I/O wrapper that
// mints and loads them on first run.
//
// SECURITY: the returned StaticKey.PrivateKey() bytes are the X25519 static
// secret. Never log them, never wrap them into error context, never emit
// them on a wire. Compromise of these bytes lets any holder impersonate
// this binary to every paired phone.
package keys

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
)

// ErrInvalidDaemonName is returned (wrapped) when the daemonName argument
// fails the package's allowlist validator. Match with errors.Is.
var ErrInvalidDaemonName = errors.New("keys: invalid daemon name")

// ErrCorruptKeyFile is returned (wrapped) when an existing static_key.json
// fails to decode, fails schema validation, or whose public_key disagrees
// with the public point recomputed from private_key. Match with errors.Is.
var ErrCorruptKeyFile = errors.New("keys: corrupt static key file")

// StaticKey is the binary's persistent X25519 keypair for Mobile Protocol v2.
// The raw 32-byte private scalar and public point are accessed via the
// PrivateKey and PublicKey methods, which return copies by value so callers
// cannot mutate package-internal state.
type StaticKey struct {
	priv [32]byte
	pub  [32]byte
}

// PrivateKey returns the raw 32-byte X25519 private scalar by value.
//
// SECURITY: callers MUST NOT log, wrap-into-error, or otherwise emit the
// returned bytes. Compromise of these bytes lets any holder impersonate
// this binary to every paired phone.
func (k *StaticKey) PrivateKey() [32]byte {
	return k.priv
}

// PublicKey returns the raw 32-byte X25519 public point by value. Safe to
// emit on the wire (this half is published in the QR pairing payload).
func (k *StaticKey) PublicKey() [32]byte {
	return k.pub
}

// validDaemonName reports whether s is a valid daemon-name path component
// for the keystore. The allowlist is restrictive by design: an operator-
// supplied name appears in a filesystem path under ~/.pyry/<daemon-name>/
// and must not be able to redirect path construction outside that
// directory.
//
// Rules: length 1..64; every byte in [a-z0-9_-]; first byte must not be
// '-' (argv-injection shape). All other inputs (including '.', '..', '/',
// '\\', uppercase, whitespace, NUL, multi-byte UTF-8) are rejected. The
// scan returns on the first violation; no regex.
func validDaemonName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	if s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// newStaticKey mints a fresh X25519 keypair from crypto/rand via the stdlib
// crypto/ecdh package. The returned bytes are bit-for-bit wire-compatible
// with flynn/noise's DHKey{Private, Public [32]byte}; the Noise wrapper
// consumes them as raw 32-byte arrays.
//
// crypto/rand is documented as infallible on supported platforms. If the
// system rng is unavailable we panic — silently degrading to a zero-entropy
// key would break the entire Noise_IK authentication model.
func newStaticKey() *StaticKey {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(fmt.Errorf("keys: crypto/rand failed: %w", err))
	}
	var sk StaticKey
	copy(sk.priv[:], priv.Bytes())
	copy(sk.pub[:], priv.PublicKey().Bytes())
	return &sk
}
