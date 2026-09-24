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

	// RedeemBy is the instant at which an UNREDEEMED pairing record stops
	// being acceptable. Stamped once at mint time by `pyry pair` as
	// PairedAt + RedemptionWindow; never rewritten afterwards. The zero
	// value means "no deadline" — what a record minted before this field
	// existed decodes to. RedeemBy says nothing about a device that has
	// already been redeemed: a redeemed device keeps authenticating past
	// it, which is why the name is not ExpiresAt.
	//
	// Validate enforces it (#1529): a record whose deadline is set and
	// already past refuses with ValidateWindowElapsed, and the v2 handshake
	// turns that into the same 4401 an unknown token gets. The zero value
	// authenticates forever, which is both the migration path for a record
	// predating the field and — since #1528's redemption clear — how an
	// already-redeemed device is represented.
	//
	// omitzero, not omitempty: encoding/json omits empty scalars, maps and
	// slices, never a struct, so omitempty on a time.Time is a silent
	// no-op that would write "0001-01-01T00:00:00Z" into every legacy
	// record on the next Save. omitzero consults time.Time's IsZero and
	// keeps the zero off disk, matching what the three fields above do for
	// their types.
	//
	// Downgrading to a binary predating this field re-Saves the record
	// without redeem_by, so the token stops expiring — it fails OPEN, back
	// to the pre-field behaviour. Deliberately not defended against in
	// code: swapping the operator's own binary needs local write access,
	// at which point this deadline is not the weakest link.
	RedeemBy time.Time `json:"redeem_by,omitzero"`

	// ClientVersion is the client_version this device reported in its most
	// recent accepted hello (#2577), stored raw so the operator can see in
	// `pyry pair list` whether any device still runs an old app. Written only
	// by the v2 handshake, and only after internal/sessions'
	// AdmitClientVersion has passed it: a value that gate refuses is stored as
	// "". It is still client-authored text — do not log it. omitempty keeps a
	// record with no version, including every record predating the field, free
	// of the key.
	ClientVersion string `json:"client_version,omitempty"`
}

// RedemptionWindow is how long a freshly minted pairing token stays
// redeemable: `pyry pair` stamps Device.RedeemBy at mint time plus this
// window. It is the only place the value appears — call sites and tests
// derive from it rather than restating the number.
const RedemptionWindow = 15 * time.Minute

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
