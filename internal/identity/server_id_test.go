package identity

import (
	"errors"
	"regexp"
	"testing"
)

// uuidv4Pattern enforces version + variant nibbles, tighter than the
// sessions package's pattern because ParseServerID enforces both.
var uuidv4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewServerID_Format(t *testing.T) {
	t.Parallel()
	id := NewServerID()
	if got := len(string(id)); got != 36 {
		t.Errorf("len(id) = %d, want 36 (id = %q)", got, id)
	}
	if !uuidv4Pattern.MatchString(string(id)) {
		t.Errorf("id %q does not match canonical UUIDv4 pattern", id)
	}
}

func TestNewServerID_Unique(t *testing.T) {
	t.Parallel()
	const n = 1000
	seen := make(map[ServerID]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewServerID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q at iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestParseServerID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"valid variant a", "550e8400-e29b-41d4-a716-446655440000", false},
		{"valid variant 9", "550e8400-e29b-41d4-9716-446655440000", false},
		{"valid variant b", "550e8400-e29b-41d4-b716-446655440000", false},
		{"valid variant 8", "550e8400-e29b-41d4-8716-446655440000", false},
		{"empty", "", true},
		{"too short prefix", "550e8400", true},
		{"one char short", "550e8400-e29b-41d4-a716-44665544000", true},
		{"one char long", "550e8400-e29b-41d4-a716-4466554400000", true},
		{"uppercase", "550E8400-E29B-41D4-A716-446655440000", true},
		{"wrong version v1", "550e8400-e29b-11d4-a716-446655440000", true},
		{"wrong variant 7", "550e8400-e29b-41d4-7716-446655440000", true},
		{"wrong variant c", "550e8400-e29b-41d4-c716-446655440000", true},
		{"non-hex char g", "550e8400-e29b-41d4-a716-44665544000g", true},
		{"missing dash", "550e8400e29b-41d4-a716-4466554400000", true},
		{"dash at wrong position", "550e840-0e29b-41d4-a716-446655440000", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseServerID(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidServerID) {
					t.Errorf("ParseServerID(%q) err = %v, want ErrInvalidServerID", tt.in, err)
				}
				if got != "" {
					t.Errorf("ParseServerID(%q) = %q, want empty on error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseServerID(%q) unexpected err: %v", tt.in, err)
			}
			if string(got) != tt.in {
				t.Errorf("ParseServerID(%q) = %q, want %q", tt.in, got, tt.in)
			}
		})
	}
}

func TestNewServerID_RoundTripsParseServerID(t *testing.T) {
	t.Parallel()
	id := NewServerID()
	got, err := ParseServerID(string(id))
	if err != nil {
		t.Fatalf("ParseServerID(%q): %v", id, err)
	}
	if got != id {
		t.Errorf("round-trip mismatch: got %q, want %q", got, id)
	}
}
