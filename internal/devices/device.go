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

	// Platform is the push notification platform for this device:
	// "fcm" (Android) or "apns" (iOS). Empty for devices that have
	// not registered a push token (e.g. CLI peers, or phones that
	// have not yet completed register_push_token). Matches the
	// contract on protocol.RegisterPushTokenPayload.Platform.
	Platform string `json:"platform,omitempty"`

	// PushToken is the opaque FCM / APNs device token used to wake
	// this device when it is offline. Empty for devices that have
	// not registered. Written by the register_push_token handler;
	// never marshalled across the wire (the wire form is
	// protocol.RegisterPushTokenPayload).
	PushToken string `json:"push_token,omitempty"`

	// AllowRemotePermissions authorizes THIS device to ANSWER a remote
	// permission / trust / destructive modal (ADR 025 § "Security model").
	// Default OFF (the zero value): an omitted/pre-field on-disk record
	// decodes to false = denied. Set only by
	// `pyry pair --allow-remote-permissions`; never set or carried over the
	// wire. Read off the authenticated *Device by the modal control loop
	// (#703) via dispatch.Conn.Auth(). Gating applies ONLY to answering a
	// permission-class modal; everything else a paired phone does is
	// ungated. omitempty keeps the secure-default (false) off disk, matching
	// the Platform/PushToken precedent.
	AllowRemotePermissions bool `json:"allow_remote_permissions,omitempty"`
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
