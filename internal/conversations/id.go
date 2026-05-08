package conversations

import (
	"crypto/rand"
	"fmt"
)

// NewID returns a fresh UUIDv4-shaped ConversationID, drawn from crypto/rand.
// Returns an error only when the system rng fails.
func NewID() (ConversationID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 4122
	return ConversationID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])), nil
}

// ValidID reports whether s is a canonical UUIDv4 string of the shape NewID
// produces: 36 chars, lowercase hex, dashes at positions 8/13/18/23, version-4
// nibble (0x4_) at position 14, and RFC 4122 variant (0x8/0x9/0xa/0xb) at
// position 19. Empty input returns false.
func ValidID(s string) bool {
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
