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
		// #2043's posture slot. The default mode and the zero value's empty mode
		// both append NOTHING: default IS claude's own default, so naming it would
		// change the argv every default session composes today, and that byte-for-
		// byte identity is the acceptance criterion.
		{"default mode appends nothing", SessionSettings{PermissionMode: permissionModeDefault}, nil},
		{"default mode beside a model", SessionSettings{Model: "opus", PermissionMode: permissionModeDefault}, []string{"--model", "opus"}},
		{"in-band mode appends the flag", SessionSettings{PermissionMode: "plan"}, []string{"--permission-mode", "plan"}},
		{"mode follows model and effort", SessionSettings{Model: "opus", Effort: "max", PermissionMode: "dontAsk"}, []string{"--model", "opus", "--effort", "max", "--permission-mode", "dontAsk"}},
		// The escalation keeps EXACTLY ONE spelling. A stored bypassPermissions is
		// the yolo bit's mode, and emitting it as a --permission-mode value would
		// give the fail-safe a second place to be enforced from.
		{"escalation is the skip flag alone", SessionSettings{YOLO: true, PermissionMode: permissionModeBypass}, []string{"--dangerously-skip-permissions"}},
		// A pair that disagrees is only reachable from a hand-built literal, never
		// from a Pool. The flag is derived from the YOLO bit alone, so no mode
		// string can compose a bypass child and no stored mode is emitted beside
		// the bypass flag.
		{"a mode cannot compose a bypass child", SessionSettings{PermissionMode: permissionModeBypass}, nil},
		{"yolo wins the posture slot over a stored mode", SessionSettings{YOLO: true, PermissionMode: "plan"}, []string{"--dangerously-skip-permissions"}},
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
