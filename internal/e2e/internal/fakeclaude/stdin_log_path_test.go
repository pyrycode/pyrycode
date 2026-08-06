package main

import (
	"path/filepath"
	"testing"
)

// TestStreamStdinLogPath pins the per-child derivation the stream stdin tee uses
// (#1331): the env value is a path STEM and each child appends to
// <stem>.<the session id its own argv was pinned to>, so the bootstrap child and
// the fresh post-rotation child write distinct files and a needle in one of them
// names the child that received it.
//
// Deliberately NOT a second copy of TestArgvSessionID's 17 parser rows — the parse
// is pinned there. What is pinned here is the derivation on top of it: which flag
// forms reach a file name, that a same-session respawn lands on the SAME file, and
// that a value the stem guard refuses cannot steer the resulting path anywhere.
// Intentionally UNTAGGED (no //go:build e2e), mirroring argv_session_id_test.go, so
// the standard `go test` gate exercises it without the e2e build tag.
func TestStreamStdinLogPath(t *testing.T) {
	t.Parallel()

	const (
		spawnID  = "11111111-1111-4111-8111-111111111111"
		resumeID = "66666666-6666-4666-8666-666666666666"
		stem     = "/tmp/e2e-1331/fakeclaude-stdin"
		// A stem that carries its own dot — the shape a caller who kept the old
		// ".log" habit would pass. The stem's dots are inert; only the appended
		// component is an id.
		dottedStem = "/tmp/e2e-1331/fakeclaude-stdin.log"
	)

	tests := []struct {
		name string
		stem string
		args []string
		want string
	}{
		{
			// The first spawn's form (streamsup.buildArgs, firstRun): the bootstrap
			// child and the post-rotation child both arrive this way, under
			// different ids, which is the whole separation.
			name: "--session-id names the file",
			stem: stem,
			args: []string{"--input-format", "stream-json", "--session-id", spawnID},
			want: "/tmp/e2e-1331/fakeclaude-stdin.11111111-1111-4111-8111-111111111111",
		},
		{
			// The respawn form. Same session ⟹ same file, deliberately: a
			// crash-respawn continues one child's evidence rather than splitting it,
			// which is why the tee still opens O_APPEND.
			name: "--resume lands on the same file as its --session-id spawn did",
			stem: stem,
			args: []string{"--input-format", "stream-json", "--resume", spawnID},
			want: "/tmp/e2e-1331/fakeclaude-stdin.11111111-1111-4111-8111-111111111111",
		},
		{
			name: "both flags present: the LAST one names the file",
			stem: stem,
			args: []string{"--session-id", spawnID, "--resume", resumeID},
			want: "/tmp/e2e-1331/fakeclaude-stdin.66666666-6666-4666-8666-666666666666",
		},
		{
			// Unreachable through the daemon (buildArgs always appends an id flag);
			// the child is named rather than silent, and the name cannot satisfy a
			// reader looking for a specific session's file.
			name: "no id flag falls back to the unattributed sentinel",
			stem: stem,
			args: []string{"--input-format", "stream-json", "--verbose"},
			want: "/tmp/e2e-1331/fakeclaude-stdin.unattributed",
		},
		{
			name: "empty argv falls back to the unattributed sentinel",
			stem: stem,
			args: nil,
			want: "/tmp/e2e-1331/fakeclaude-stdin.unattributed",
		},
		{
			// The security-relevant row: argvSessionID's stem guard refuses the value
			// BEFORE it can reach this concatenation, so the traversal never enters
			// the path at all. The subtest below also asserts the resulting path's
			// directory, which is the property the guard exists to protect.
			name: "a traversal value is refused, not spliced",
			stem: stem,
			args: []string{"--session-id", "../escape"},
			want: "/tmp/e2e-1331/fakeclaude-stdin.unattributed",
		},
		{
			name: "a separator value is refused, not spliced",
			stem: stem,
			args: []string{"--session-id", "a/b"},
			want: "/tmp/e2e-1331/fakeclaude-stdin.unattributed",
		},
		{
			name: "a dotted stem keeps its dots; the id is still the final component",
			stem: dottedStem,
			args: []string{"--session-id", spawnID},
			want: "/tmp/e2e-1331/fakeclaude-stdin.log.11111111-1111-4111-8111-111111111111",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := streamStdinLogPath(tc.stem, tc.args)
			if got != tc.want {
				t.Errorf("streamStdinLogPath(%q, %q) = %q, want %q", tc.stem, tc.args, got, tc.want)
			}
			// Universal, and the reason an argv value may be joined into a path at
			// all: whatever argv carried, the per-child log is a sibling of the stem.
			// A value that survived the guard would move this — "../escape" spliced
			// raw would put the file two directories up.
			if gotDir, wantDir := filepath.Dir(got), filepath.Dir(tc.stem); gotDir != wantDir {
				t.Errorf("streamStdinLogPath(%q, %q) escaped the stem's directory: got dir %q, want %q",
					tc.stem, tc.args, gotDir, wantDir)
			}
		})
	}
}
