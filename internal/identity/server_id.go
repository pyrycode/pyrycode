// Package identity owns typed identifiers that span subsystems — server-id
// today; potential future device-id, paired-device-id. Pure types and
// validation; no I/O.
package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
)

// ServerID is the public routing identifier for one pyrycode-binary instance.
// Canonical form is a UUIDv4 lowercase hex string (8-4-4-4-12, 36 chars).
// Surfaced on the wire in pairing payloads and in the relay handshake's
// x-pyrycode-server header.
//
// The empty ServerID ("") is the unset sentinel and is never a valid
// generated id. Construct ServerID values only via NewServerID or
// ParseServerID; do not convert raw strings to ServerID outside this package.
type ServerID string

// ErrInvalidServerID indicates a string failed canonical UUIDv4 validation.
// Errors returned from ParseServerID match this with errors.Is.
var ErrInvalidServerID = errors.New("identity: invalid server id")

// NewServerID returns a fresh UUIDv4-shaped ServerID drawn from crypto/rand.
//
// crypto/rand.Read is documented as infallible on supported platforms. If
// the system rng is unavailable we panic — silently degrading to a
// zero-entropy id would break the unguessability that the relay-routing
// security model depends on.
func NewServerID() ServerID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("identity: crypto/rand failed: %w", err))
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 4122
	return ServerID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

// ParseServerID validates that s is a canonical UUIDv4 string and returns it
// as a ServerID. Returns ErrInvalidServerID for empty or malformed input.
// Use this at every wire/disk boundary that accepts an externally-supplied
// server-id.
func ParseServerID(s string) (ServerID, error) {
	if !validUUIDv4(s) {
		return "", ErrInvalidServerID
	}
	return ServerID(s), nil
}

// validUUIDv4 reports whether s is a canonical UUIDv4 string: 36 chars,
// lowercase hex, dashes at positions 8/13/18/23, version-4 nibble at
// position 14, and RFC 4122 variant nibble (8/9/a/b) at position 19.
func validUUIDv4(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			if c != '4' {
				return false
			}
		case 19:
			if !(c == '8' || c == '9' || c == 'a' || c == 'b') {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}
