package sessions

import (
	"reflect"
	"slices"
	"testing"
)

// TestClaudeSettingsArgs is the primary AC #3/#4/#5 assertion at the logic
// layer: model/effort map to their flags, YOLO-on appends the bypass flag,
// YOLO-off never does, and the zero value appends nothing (byte-identical argv).
func TestClaudeSettingsArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   SessionSettings
		want []string
	}{
		{"zero inherits template", SessionSettings{}, nil},
		{"model only", SessionSettings{Model: "sonnet"}, []string{"--model", "sonnet"}},
		{"effort only", SessionSettings{Effort: "high"}, []string{"--effort", "high"}},
		{"model and effort", SessionSettings{Model: "opus", Effort: "low"}, []string{"--model", "opus", "--effort", "low"}},
		{"yolo on appends bypass", SessionSettings{YOLO: true}, []string{"--dangerously-skip-permissions"}},
		{"yolo off appends nothing", SessionSettings{Model: "opus"}, []string{"--model", "opus"}},
		{"all three in deterministic order", SessionSettings{Model: "opus", Effort: "max", YOLO: true}, []string{"--model", "opus", "--effort", "max", "--dangerously-skip-permissions"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := claudeSettingsArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("claudeSettingsArgs(%+v) = %v, want %v", tc.in, got, tc.want)
			}
			// Security invariant: the bypass flag appears iff YOLO is set,
			// regardless of the other fields.
			hasBypass := slices.Contains(got, "--dangerously-skip-permissions")
			if hasBypass != tc.in.YOLO {
				t.Errorf("bypass flag present = %v, want %v (YOLO=%v)", hasBypass, tc.in.YOLO, tc.in.YOLO)
			}
		})
	}
}
