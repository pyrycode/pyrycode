package main

import (
	"testing"
	"time"
)

// TestApprovalTimeout asserts the PYRY_APPROVAL_TIMEOUT override and the fail-closed
// default: an unset/empty/unparseable value yields the 2-minute mcpApprovalTimeout
// (production byte-identical when absent), a valid duration overrides it. The e2e
// (#1139) relies on the "2s" override to shrink the daemon's fail-closed timer, and on
// the default being unchanged so the allow/deny cases' generous window still applies.
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
}
