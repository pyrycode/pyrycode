package update

import (
	"slices"
	"testing"
)

func TestDetectRestartCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		probe RestartProbe
		want  []string
	}{
		{
			name:  "launchd_only",
			probe: RestartProbe{LaunchdPlistExists: true, UID: "501"},
			want:  []string{"launchctl", "kickstart", "-k", "gui/501/dev.pyrycode.pyry"},
		},
		{
			name:  "systemd_only",
			probe: RestartProbe{SystemdUnitExists: true},
			want:  []string{"systemctl", "--user", "restart", "pyry"},
		},
		{
			name:  "both_present_launchd_wins",
			probe: RestartProbe{LaunchdPlistExists: true, SystemdUnitExists: true, UID: "1000"},
			want:  []string{"launchctl", "kickstart", "-k", "gui/1000/dev.pyrycode.pyry"},
		},
		{
			name:  "neither_present",
			probe: RestartProbe{},
			want:  nil,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DetectRestartCommand(tc.probe)
			if !slices.Equal(got, tc.want) {
				t.Errorf("DetectRestartCommand(%+v) = %v, want %v", tc.probe, got, tc.want)
			}
		})
	}
}
