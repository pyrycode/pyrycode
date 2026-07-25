package main

import "testing"

// TestArgvSessionID pins the argv parse the stem knob (envSessionIDFromArgv)
// relies on: a minted spawn's "--session-id <id>" and a warm bootstrap spawn's
// "--resume <id>" (#1164) both name the transcript stem the daemon tails, the
// LAST occurrence wins (buildClaudeArgs appends the flag at the end of argv, so
// the spawn-time value beats anything a template contributed), and a value that
// could steer filepath.Join out of the sessions dir is refused so the caller
// falls back to PYRY_FAKE_CLAUDE_INITIAL_UUID. Intentionally UNTAGGED (no
// //go:build e2e), mirroring esc_detect_test.go, so the standard `go test` gate
// exercises the parser without the e2e build tag.
func TestArgvSessionID(t *testing.T) {
	t.Parallel()

	const (
		mintedID    = "11111111-1111-4111-8111-111111111111"
		bootstrapID = "66666666-6666-4666-8666-666666666666"
	)

	tests := []struct {
		name   string
		args   []string
		want   string
		wantOK bool
	}{
		{"empty argv", nil, "", false},
		{"no session flags", []string{"--settings", "/tmp/x.json"}, "", false},
		{"session-id only", []string{"--session-id", mintedID}, mintedID, true},
		{"resume only", []string{"--resume", bootstrapID}, bootstrapID, true},
		{
			"realistic minted argv",
			[]string{"--session-id", mintedID, "--settings", "/tmp/mcp-1.json"},
			mintedID, true,
		},
		{
			"realistic warm bootstrap argv",
			[]string{"--dangerously-skip-permissions", "--settings", "/tmp/mcp-0.json", "--resume", bootstrapID},
			bootstrapID, true,
		},
		{
			"both flags, last wins",
			[]string{"--session-id", mintedID, "--resume", bootstrapID},
			bootstrapID, true,
		},
		{
			"both flags, last wins (reverse order)",
			[]string{"--resume", bootstrapID, "--session-id", mintedID},
			mintedID, true,
		},
		{"flag is final token", []string{"--settings", "/tmp/x.json", "--session-id"}, "", false},
		// The = form is not emitted by either call site (sessions.buildSession and
		// supervisor.buildClaudeArgs both write two tokens), so it is deliberately
		// unhandled — this row pins that as a decision, not an oversight.
		{"equals form is not parsed", []string{"--session-id=" + mintedID}, "", false},
		// Stem guard: none of these may be adopted as a filename stem, because the
		// value reaches filepath.Join for both the transcript and the per-child
		// JSONL trigger path.
		{"traversal value", []string{"--session-id", "../escape"}, "", false},
		{"separator value", []string{"--session-id", "a/b"}, "", false},
		{"backslash value", []string{"--session-id", `a\b`}, "", false},
		{"dotted value", []string{"--session-id", "x.jsonl"}, "", false},
		{"empty value", []string{"--session-id", ""}, "", false},
		// A trailing bad value shadows an earlier good one — last-occurrence-wins is
		// resolved BEFORE the guard, so the fake falls back rather than silently
		// writing under a stale stem.
		{"last occurrence bad, no fallback to earlier", []string{"--session-id", mintedID, "--resume", "../escape"}, "", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := argvSessionID(tc.args)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("argvSessionID(%q) = (%q, %v), want (%q, %v)",
					tc.args, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
