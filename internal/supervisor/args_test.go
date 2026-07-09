package supervisor

import (
	"reflect"
	"testing"
)

func TestBuildClaudeArgs(t *testing.T) {
	t.Parallel()

	const sid = "11111111-1111-4111-8111-111111111111"

	tests := []struct {
		name         string
		claudeArgs   []string
		firstRun     bool
		continueLast bool
		sessionID    string
		want         []string
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
		{
			// #839: a resolved session id appends --session-id and suppresses
			// --continue even when continueLast would otherwise prepend it.
			name:         "session id appends --session-id and suppresses --continue",
			claudeArgs:   []string{"--channels", "plugin:discord"},
			firstRun:     false,
			continueLast: true,
			sessionID:    sid,
			want:         []string{"--channels", "plugin:discord", "--session-id", sid},
		},
		{
			// #839 AC-1: deterministic from the very first spawn — the bootstrap
			// gets --session-id on firstRun too, never a --continue.
			name:         "session id on first run still appends --session-id",
			claudeArgs:   nil,
			firstRun:     true,
			continueLast: false,
			sessionID:    sid,
			want:         []string{"--session-id", sid},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildClaudeArgs(tt.claudeArgs, tt.firstRun, tt.continueLast, tt.sessionID)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildClaudeArgs(%v, firstRun=%v, continueLast=%v, sessionID=%q) = %v, want %v",
					tt.claudeArgs, tt.firstRun, tt.continueLast, tt.sessionID, got, tt.want)
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

	// Both branches must leave the caller's slice untouched: the --continue
	// prepend path and the #839 --session-id append path.
	_ = buildClaudeArgs(original, false, true, "")
	_ = buildClaudeArgs(original, false, true, "11111111-1111-4111-8111-111111111111")

	if !reflect.DeepEqual(original, snapshot) {
		t.Errorf("buildClaudeArgs mutated input: got %v, want %v", original, snapshot)
	}
}
