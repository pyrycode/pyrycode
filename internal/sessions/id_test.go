package sessions

import (
	"regexp"
	"testing"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestNewID_Format verifies that NewID returns canonical 36-char UUID-shaped
// strings: 8-4-4-4-12 lowercase hex separated by dashes.
func TestNewID_Format(t *testing.T) {
	t.Parallel()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if got := len(string(id)); got != 36 {
		t.Errorf("len(id) = %d, want 36 (id = %q)", got, id)
	}
	if !uuidPattern.MatchString(string(id)) {
		t.Errorf("id %q does not match canonical UUID pattern", id)
	}
}

// TestNewID_Unique generates a thousand IDs and verifies no duplicates — a
// smoke test that crypto/rand is wired correctly. A constant-zero rng would
// be caught immediately.
func TestNewID_Unique(t *testing.T) {
	t.Parallel()
	const n = 1000
	seen := make(map[SessionID]struct{}, n)
	for i := 0; i < n; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID iteration %d: %v", i, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q at iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}
