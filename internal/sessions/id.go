// Package sessions owns the set of supervised claude instances managed by one
// pyry process. Phase 1.0 constructs exactly one entry — the bootstrap
// session — at startup; Phase 1.1+ will add multi-session routing on top of
// the same Pool/Session shape.
package sessions

import (
	"crypto/rand"
	"fmt"
)

// SessionID is a per-session identifier. Locked design uses UUIDs (crypto/rand,
// 36-char canonical 8-4-4-4-12 form). Phase 1.0 generates one at startup for
// the bootstrap entry; the wire protocol does not carry it yet (Phase 1.1).
//
// The empty SessionID ("") is the unset sentinel and is never a valid
// generated ID. Pool.Lookup("") resolves to the default (bootstrap) entry.
type SessionID string

// NewID returns a fresh UUIDv4-shaped SessionID, drawn from crypto/rand.
// Returns an error only when the system rng fails — fatal at startup, same
// fail-fast semantics as today's supervisor.New errors.
func NewID() (SessionID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 4122
	return SessionID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])), nil
}
