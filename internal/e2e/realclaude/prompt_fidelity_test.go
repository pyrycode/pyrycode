//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// distinctivePrompt is an ASCII-only token unlikely to appear in any system
// prompt or claude response template. JSON-safe (no characters that need
// escaping), so the substring match against Event.Raw is deterministic.
const distinctivePrompt = "INTEGRATION_TEST_PROMPT_PROMPTFIDELITY_2N7Q4R8W"

// TestRealClaude_PromptFidelity is a regression guard for the pyry agent-run
// prompt-handoff path. It runs `pyry agent-run` against the real claude CLI
// with a distinctive prompt literal and asserts that the literal survives
// byte-for-byte into the first "user" entry of claude's JSONL session file.
//
// If anyone later re-introduces preprocessing, breaks the streamrunner
// envelope's text round-trip, or wires a path that bypasses streamrunner,
// the substring match fails and this test catches it before merge.
func TestRealClaude_PromptFidelity(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       distinctivePrompt,
		SystemPrompt: "You are an e2e regression-guard test. Reply with a single short word.",
		AllowedTools: []string{"Read"},
		MaxTurns:     1,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0\nstderr:\n%s", result.ExitCode, result.Stderr)
	}
	if result.SessionID == "" {
		stdout := result.Stdout
		suffix := ""
		if len(stdout) > 1024 {
			stdout = stdout[:1024]
			suffix = "... (truncated)"
		}
		t.Fatalf("SessionID is empty: no system/init envelope found in stdout\nstdout:\n%s%s", stdout, suffix)
	}

	events := ReadJSONL(t, workdir, result.SessionID)
	jsonlPath := jsonlPathFor(workdir, result.SessionID)

	for _, e := range events {
		if e.Kind == "user" {
			if !bytes.Contains(e.Raw, []byte(distinctivePrompt)) {
				t.Fatalf("first user entry does not contain prompt literal %q\npath: %s\nraw: %s",
					distinctivePrompt, jsonlPath, string(e.Raw))
			}
			return
		}
	}

	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	t.Fatalf("no user entry found in JSONL\npath: %s\nevent kinds: %s",
		jsonlPath, strings.Join(kinds, ","))
}

// jsonlPathFor recomputes the JSONL path for failure-message diagnostics.
// Returns a "<unresolved: ...>" sentinel on error rather than failing the
// test — the assertion has already failed; we just want the best path string
// we can produce.
func jsonlPathFor(workdir, sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "<unresolved home: " + err.Error() + ">"
	}
	enc, err := agentrun.EncodeProjectDir(workdir)
	if err != nil {
		return "<unresolved encoding: " + err.Error() + ">"
	}
	return filepath.Join(home, ".claude", "projects", enc, sessionID+".jsonl")
}
