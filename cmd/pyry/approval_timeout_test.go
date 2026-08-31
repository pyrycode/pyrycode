package main

import (
	"testing"
	"time"
)

// TestApprovalTimeout asserts the PYRY_APPROVAL_TIMEOUT override and the fail-closed
// default: an unset/empty/unparseable value yields the 10-minute mcpApprovalTimeout
// (production byte-identical when absent), a valid duration overrides it. The e2e
// (#1139) relies on the "2s" override to shrink the daemon's fail-closed timer; every
// one of its arms sets the window explicitly, so it is default-independent and this
// value can move without touching it.
func TestApprovalTimeout(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"empty falls back to default", "", mcpApprovalTimeout},
		{"invalid falls back to default", "not-a-duration", mcpApprovalTimeout},
		{"short override", "2s", 2 * time.Second},
		{"generous override", "30s", 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envApprovalTimeout, tc.env)
			if got := approvalTimeout(); got != tc.want {
				t.Errorf("approvalTimeout() with env %q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}

	// The fallback rows above compare against mcpApprovalTimeout symbolically —
	// they pin the ROUTING (unset/empty/unparseable reaches the default) and stay
	// green through any value regression. This pins the value itself, in the file
	// that owns the accessor: docs/guide.md § Environment knobs publishes this
	// number to users, so the two must not drift (#1909).
	t.Setenv(envApprovalTimeout, "")
	if got := approvalTimeout(); got != 10*time.Minute {
		t.Errorf("approvalTimeout() with the env absent = %v, want 10m (the published default)", got)
	}
}
