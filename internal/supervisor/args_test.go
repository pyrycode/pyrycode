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
		resume       bool
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
		{
			// #1164: an existing transcript for the pinned id reattaches via
			// --resume, suppressing both --session-id and --continue even when
			// continueLast would otherwise prepend --continue.
			name:         "session id with resume appends --resume and suppresses --continue",
			claudeArgs:   []string{"--channels", "plugin:discord"},
			firstRun:     false,
			continueLast: true,
			sessionID:    sid,
			resume:       true,
			want:         []string{"--channels", "plugin:discord", "--resume", sid},
		},
		{
			// #1164: the resume decision is independent of firstRun — a transcript
			// that already exists on the very first spawn (hard daemon restart)
			// still reattaches with --resume.
			name:         "session id with resume on first run appends --resume",
			claudeArgs:   nil,
			firstRun:     true,
			continueLast: false,
			sessionID:    sid,
			resume:       true,
			want:         []string{"--resume", sid},
		},
		{
			// #1164: resume is inert when there is no session id — the empty-id
			// path stays exactly the --continue logic regardless of the bit.
			name:         "empty session id ignores resume bit",
			claudeArgs:   []string{"--channels", "plugin:discord"},
			firstRun:     false,
			continueLast: true,
			sessionID:    "",
			resume:       true,
			want:         []string{"--continue", "--channels", "plugin:discord"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildClaudeArgs(tt.claudeArgs, tt.firstRun, tt.continueLast, tt.sessionID, tt.resume)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildClaudeArgs(%v, firstRun=%v, continueLast=%v, sessionID=%q, resume=%v) = %v, want %v",
					tt.claudeArgs, tt.firstRun, tt.continueLast, tt.sessionID, tt.resume, got, tt.want)
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

	// Every branch must leave the caller's slice untouched: the --continue
	// prepend path, the #839 --session-id append path, and the #1164 --resume
	// append path.
	_ = buildClaudeArgs(original, false, true, "", false)
	_ = buildClaudeArgs(original, false, true, "11111111-1111-4111-8111-111111111111", false)
	_ = buildClaudeArgs(original, false, true, "11111111-1111-4111-8111-111111111111", true)

	if !reflect.DeepEqual(original, snapshot) {
		t.Errorf("buildClaudeArgs mutated input: got %v, want %v", original, snapshot)
	}
}
