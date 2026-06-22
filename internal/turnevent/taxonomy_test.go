package turnevent

import (
	"reflect"
	"testing"
)

// The expected lists below are independent literals (raw ACP strings, not the
// package constants), so a typo'd constant is caught by the deep-equal. Each
// test asserts: exactness (canonical slice == expected ACP list, in order),
// a count guard, consistency (every canonical value reports Valid() == true),
// and a validity table (one in-taxonomy, one fabricated, the empty string).

func TestToolKind_Taxonomy(t *testing.T) {
	t.Parallel()

	want := []ToolKind{
		"read", "edit", "delete", "move",
		"search", "execute", "think", "fetch", "other",
	}
	if got := len(toolKinds); got != 9 {
		t.Fatalf("toolKinds length: got %d, want %d", got, 9)
	}
	if !reflect.DeepEqual(toolKinds, want) {
		t.Fatalf("toolKinds: got %v, want %v", toolKinds, want)
	}
	for _, k := range toolKinds {
		if !k.Valid() {
			t.Errorf("canonical %q reports Valid() == false", k)
		}
	}

	cases := []struct {
		name string
		kind ToolKind
		want bool
	}{
		{"in-taxonomy", ToolKindRead, true},
		{"fabricated", ToolKind("nope"), false},
		{"empty", ToolKind(""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.kind.Valid(); got != tc.want {
				t.Errorf("ToolKind(%q).Valid(): got %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestToolStatus_Taxonomy(t *testing.T) {
	t.Parallel()

	want := []ToolStatus{"pending", "in_progress", "completed", "failed"}
	if got := len(toolStatuses); got != 4 {
		t.Fatalf("toolStatuses length: got %d, want %d", got, 4)
	}
	if !reflect.DeepEqual(toolStatuses, want) {
		t.Fatalf("toolStatuses: got %v, want %v", toolStatuses, want)
	}
	for _, s := range toolStatuses {
		if !s.Valid() {
			t.Errorf("canonical %q reports Valid() == false", s)
		}
	}

	cases := []struct {
		name   string
		status ToolStatus
		want   bool
	}{
		{"in-taxonomy", ToolStatusInProgress, true},
		{"fabricated", ToolStatus("nope"), false},
		{"empty", ToolStatus(""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.status.Valid(); got != tc.want {
				t.Errorf("ToolStatus(%q).Valid(): got %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestTurnEndReason_Taxonomy(t *testing.T) {
	t.Parallel()

	want := []TurnEndReason{
		"end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled",
	}
	if got := len(turnEndReasons); got != 5 {
		t.Fatalf("turnEndReasons length: got %d, want %d", got, 5)
	}
	if !reflect.DeepEqual(turnEndReasons, want) {
		t.Fatalf("turnEndReasons: got %v, want %v", turnEndReasons, want)
	}
	for _, r := range turnEndReasons {
		if !r.Valid() {
			t.Errorf("canonical %q reports Valid() == false", r)
		}
	}

	cases := []struct {
		name   string
		reason TurnEndReason
		want   bool
	}{
		{"in-taxonomy", TurnEndReasonEndTurn, true},
		{"fabricated", TurnEndReason("nope"), false},
		{"empty", TurnEndReason(""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.reason.Valid(); got != tc.want {
				t.Errorf("TurnEndReason(%q).Valid(): got %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

func TestPermissionOptionKind_Taxonomy(t *testing.T) {
	t.Parallel()

	want := []PermissionOptionKind{
		"allow_once", "allow_always", "reject_once", "reject_always",
	}
	if got := len(permissionOptionKinds); got != 4 {
		t.Fatalf("permissionOptionKinds length: got %d, want %d", got, 4)
	}
	if !reflect.DeepEqual(permissionOptionKinds, want) {
		t.Fatalf("permissionOptionKinds: got %v, want %v", permissionOptionKinds, want)
	}
	for _, k := range permissionOptionKinds {
		if !k.Valid() {
			t.Errorf("canonical %q reports Valid() == false", k)
		}
	}

	cases := []struct {
		name string
		kind PermissionOptionKind
		want bool
	}{
		{"in-taxonomy", PermissionOptionKindAllowOnce, true},
		{"fabricated", PermissionOptionKind("nope"), false},
		{"empty", PermissionOptionKind(""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.kind.Valid(); got != tc.want {
				t.Errorf("PermissionOptionKind(%q).Valid(): got %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}
