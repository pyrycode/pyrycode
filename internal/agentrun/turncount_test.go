package agentrun

import "testing"

func TestIsNewLogicalTurn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		currentID string
		lastID    string
		want      bool
	}{
		{"first entry empty id", "", "", true},
		{"empty id after non-empty", "", "msg_A", true},
		{"first non-empty id", "msg_A", "", true},
		{"id changed", "msg_B", "msg_A", true},
		{"same id", "msg_A", "msg_A", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsNewLogicalTurn(tc.currentID, tc.lastID); got != tc.want {
				t.Fatalf("IsNewLogicalTurn(%q, %q) = %v, want %v", tc.currentID, tc.lastID, got, tc.want)
			}
		})
	}
}
