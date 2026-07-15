package supervisor

import (
	"context"
	"strings"
	"testing"
)

// TestClaudeChildCmd_ScrubsNestingMarkers pins the child-env hygiene the
// interactive spawn path owes its claude child. When the daemon itself runs
// inside a Claude Code session (an operator terminal, a test harness, an e2e
// script), the parent session's nesting markers leak into os.Environ(). A
// claude child that inherits them treats itself as a nested child session and
// SILENTLY writes no session transcript while answering turns normally — so
// the growth-confirm never confirms, the drain re-sends the same turn forever,
// and the turn bridge has no JSONL to tail. Verified live against claude
// 2.1.199: with the markers present the turn completes on screen and zero
// transcript files exist; with them stripped the transcript appears instantly.
//
// The agent-run path has always scrubbed via tuidriver.EnsureClaudeEnv
// (ptyrunner/runner.go); this test pins the interactive supervisor path to the
// same substrate helper.
func TestClaudeChildCmd_ScrubsNestingMarkers(t *testing.T) {
	// Poison the process env exactly the way a parent Claude Code session does.
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "dee0ec69-6766-471b-ba11-41a26910dfea")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "sdk-ts")
	// Auth must survive the scrub.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token-value")

	cmd := claudeChildCmd(context.Background(), "claude", []string{"--session-id", "x"}, "/tmp", []string{"PYRY_HELPER=1"})

	var gotTERM, gotHelper, gotToken bool
	for _, kv := range cmd.Env {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_ENTRYPOINT":
			t.Errorf("child env leaks nesting marker %q", kv)
		case "TERM":
			if kv == "TERM=xterm-256color" {
				gotTERM = true
			}
		case "PYRY_HELPER":
			gotHelper = true
		case "CLAUDE_CODE_OAUTH_TOKEN":
			gotToken = true
		}
	}
	if !gotTERM {
		t.Errorf("child env missing pinned TERM=xterm-256color (tui-driver's calibrated rendering)")
	}
	if !gotHelper {
		t.Errorf("child env dropped helperEnv entry PYRY_HELPER=1")
	}
	if !gotToken {
		t.Errorf("child env dropped CLAUDE_CODE_OAUTH_TOKEN (auth must survive the scrub)")
	}
	if cmd.Dir != "/tmp" {
		t.Errorf("cmd.Dir = %q, want /tmp", cmd.Dir)
	}
}
