// Package devices defines the on-disk Device type and the token
// hashing/verification primitives used by `pyry pair` and the mobile
// auth path.
//
// SECURITY: callers MUST NOT log a plain device-token, MUST NOT wrap
// a plain token into error context, and MUST NOT pass a plain token
// across log/slog fields. The plain token appears once at pairing
// (QR code, paste-fallback string) and once per WS-connect (the
// phone presents it for verification). Outside those two sites the
// only on-disk and in-memory representation is the SHA-256 hex hash.
package devices

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"time"
)

// Device is the on-disk shape for one paired device. Persisted by the
// sibling registry-CRUD ticket; never marshalled across the wire (the
// wire carries plain tokens once at handshake, then routing envelopes
// by server-id).
type Device struct {
	TokenHash  string    `json:"token_hash"`
	Name       string    `json:"name"`
	PairedAt   time.Time `json:"paired_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// HashToken returns the lowercase SHA-256 hex of plain. Output is
// always 64 hex characters (sha256.Size * 2). The same input always
// produces the same output (deterministic, no salt — see the package
// design doc for why bcrypt and per-token salt are intentionally
// rejected for 256-bit random tokens).
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// VerifyToken reports whether HashToken(plain) equals hash, in
// constant time relative to hash's length. Returns false for any
// hash whose length differs from the canonical 64-char hex (this
// includes the empty string and any malformed hex). Never panics;
// never logs; never returns the plain or hash in any error.
func VerifyToken(plain, hash string) bool {
	expected := HashToken(plain)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(hash)) == 1
}
