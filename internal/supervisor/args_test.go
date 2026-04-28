package supervisor

import (
	"reflect"
	"testing"
)

func TestBuildClaudeArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		claudeArgs     []string
		firstRun       bool
		continueLast   bool
		want           []string
	}{
		{
			name:         "first run with no claude args yields no claude args",
			claudeArgs:   nil,
			firstRun:     true,
			continueLast: true,
			want:         nil,
		},
		{
			name:         "first run preserves user args verbatim",
			claudeArgs:   []string{"--channels", "plugin:discord"},
			firstRun:     true,
			continueLast: true,
			want:         []string{"--channels", "plugin:discord"},
		},
		{
			name:         "subsequent run with continue prepends --continue",
			claudeArgs:   []string{},
			firstRun:     false,
			continueLast: true,
			want:         []string{"--continue"},
		},
		{
			name:         "subsequent run preserves user args after --continue",
			claudeArgs:   []string{"--channels", "plugin:discord"},
			firstRun:     false,
			continueLast: true,
			want:         []string{"--continue", "--channels", "plugin:discord"},
		},
		{
			name:         "continueLast=false never adds --continue",
			claudeArgs:   []string{"--channels", "plugin:discord"},
			firstRun:     false,
			continueLast: false,
			want:         []string{"--channels", "plugin:discord"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildClaudeArgs(tt.claudeArgs, tt.firstRun, tt.continueLast)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildClaudeArgs(%v, firstRun=%v, continueLast=%v) = %v, want %v",
					tt.claudeArgs, tt.firstRun, tt.continueLast, got, tt.want)
			}
		})
	}
}

// TestBuildClaudeArgs_DoesNotMutate confirms the helper never aliases the
// caller's slice. If buildClaudeArgs returned a slice that shared the backing
// array, prepending --continue could clobber data in subsequent calls — the
// kind of bug append-aliasing tends to cause when a `cap > len` slice is
// reused across iterations of the supervisor loop.
func TestBuildClaudeArgs_DoesNotMutate(t *testing.T) {
	t.Parallel()

	original := []string{"--channels", "plugin:discord"}
	snapshot := append([]string(nil), original...)

	_ = buildClaudeArgs(original, false, true)

	if !reflect.DeepEqual(original, snapshot) {
		t.Errorf("buildClaudeArgs mutated input: got %v, want %v", original, snapshot)
	}
}
