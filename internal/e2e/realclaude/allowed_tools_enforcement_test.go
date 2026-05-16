//go:build e2e_realclaude

package realclaude

// This ticket was originally framed as a `defaultMode ∈ {deny, default,
// dontAsk}` matrix; under post-#391 architecture the per-spawn settings
// file is gone and `--allowed-tools` is the sole enforcement
// configuration on the agent-run path, so the matrix collapsed to one
// row. The production contract being guarded here is: `pyry agent-run
// --allowed-tools X` MUST be a deny-by-default gate at the claude
// binary.
//
// Complements internal/agentrun/selfcheck — that probes the boot-time
// `--settings`/`defaultMode=deny` path; this probes the spawned
// agent-run `--allowed-tools` + `--dangerously-skip-permissions` path.

import (
	"encoding/json"
	"testing"
)

// TestRealClaude_AllowedToolsEnforcement runs `pyry agent-run` with a
// Bash-attractive prompt and a Read-only allowlist, then asserts that
// no assistant entry in the resulting JSONL session emits a Bash
// tool_use. A regression here means the claude-binary boundary stopped
// honoring `--allowed-tools`.
func TestRealClaude_AllowedToolsEnforcement(t *testing.T) {
	workdir := WithWorktree(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "List the files in the current working directory. Use the Bash tool to run `ls -la`.",
		SystemPrompt: "You are a regression-guard test agent. Use the tools you are given to satisfy the user.",
		AllowedTools: []string{"Read"},
		MaxTurns:     2,
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
		if e.Kind != "assistant" {
			continue
		}
		hit, err := bashInvokedInRaw(e.Raw)
		if err != nil {
			// Mirrors selfcheck.go:283 — a single malformed line must
			// not turn a PASS into an inconclusive. Skip silently; do
			// not log raw bytes.
			continue
		}
		if hit {
			t.Fatalf("Bash tool_use observed in JSONL despite --allowed-tools=Read — gate regression.\npath: %s", jsonlPath)
		}
	}
}

// bashInvokedInRaw mirrors internal/agentrun/selfcheck/selfcheck.go:284
// exactly. If selfcheck's shape changes (e.g. claude renames `tool_use`
// → `tool_invocation`), both must move in lockstep.
func bashInvokedInRaw(raw json.RawMessage) (bool, error) {
	var line struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		return false, err
	}
	for _, c := range line.Message.Content {
		if c.Type == "tool_use" && c.Name == "Bash" {
			return true, nil
		}
	}
	return false, nil
}
