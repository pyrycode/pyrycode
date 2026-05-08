package conversations

import (
	"regexp"
	"strings"
	"testing"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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
	if !ValidID(string(id)) {
		t.Errorf("ValidID(%q) = false, want true", id)
	}
}

func TestNewID_Unique(t *testing.T) {
	t.Parallel()
	const n = 1000
	seen := make(map[ConversationID]struct{}, n)
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

func TestValidID(t *testing.T) {
	t.Parallel()
	const valid = "11111111-2222-4333-8444-555555555555"

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"too-short", "11111111-2222-4333-8444-55555555555", false},
		{"too-long", valid + "x", false},
		{"wrong-dashes", "111111112-222-4333-8444-555555555555", false},
		{"wrong-version-nibble", "11111111-2222-3333-8444-555555555555", false},
		{"wrong-variant-nibble", "11111111-2222-4333-7444-555555555555", false},
		{"valid-v4", valid, true},
		{"valid-v4-variant-9", "11111111-2222-4333-9444-555555555555", true},
		{"valid-v4-variant-a", "11111111-2222-4333-a444-555555555555", true},
		{"valid-v4-variant-b", "11111111-2222-4333-b444-555555555555", true},
		{"all-uppercase", strings.ToUpper("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), false},
		{"non-hex-char", "1111111g-2222-4333-8444-555555555555", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidID(tc.in); got != tc.want {
				t.Errorf("ValidID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
