//go:build e2e_realclaude

package realclaude

// Per-dispatcher-role smoke tests. Five named top-level tests (one per role:
// po, architect, developer, code-review, documentation) rather than a table
// loop so the nightly board surfaces five independent pass/fail signals; a
// regression in any single role's (allowed-tools, system-prompt) wiring is
// then attributable on sight. The five system prompts and user prompts also
// genuinely differ per role.
//
// The system prompts inlined below are deliberately *stand-ins* — 3-5 lines
// naming the role and pointing at its tool surface. They are NOT verbatim
// copies of `agents/<role>/CLAUDE.md`. The goal is to exercise pyry's
// agent-run wiring with dispatcher-shaped inputs, not to validate operator
// prompts (which are large, frequently revised, and would make this suite
// both expensive and falsely sensitive to prompt-edit churn).
//
// `Agent` is appended for architect and code-review only, mirroring the
// dispatcher's `needsAgent` membership check.

import (
	"testing"
)

// dispatcherBaseTools mirrors the `baseTools` literal at
// agents/dispatcher/src/dispatch.ts:1183 (in the sibling agent-dispatcher
// repo). If a future renumber moves that line, update both the cite and
// the contents below. A drift between this list and the dispatcher's
// goes silent until a role test starts producing surprising tool denials.
var dispatcherBaseTools = []string{
	"Bash",
	"Read",
	"Write",
	"Edit",
	"Glob",
	"Grep",
	"TodoWrite",
	"mcp__qmd__query",
	"mcp__qmd__get",
	"mcp__qmd__multi_get",
	"mcp__qmd__status",
	"mcp__context7__resolve-library-id",
	"mcp__context7__query-docs",
	"mcp__codegraph__codegraph_search",
	"mcp__codegraph__codegraph_callers",
	"mcp__codegraph__codegraph_callees",
	"mcp__codegraph__codegraph_impact",
	"mcp__codegraph__codegraph_node",
	"mcp__codegraph__codegraph_context",
	"mcp__codegraph__codegraph_files",
	"mcp__codegraph__codegraph_status",
}

// dispatcherAllowedToolsForRole returns the dispatcher's per-role allowed-
// tools list: baseTools for po/developer/documentation, baseTools+Agent for
// architect/code-review. Mirrors agents/dispatcher/src/dispatch.ts:1184-1186.
// An unknown role is a programmer error at the only five call sites in this
// file, so we panic rather than thread an `ok` return through every caller.
func dispatcherAllowedToolsForRole(role string) []string {
	switch role {
	case "po", "developer", "documentation":
		out := make([]string, len(dispatcherBaseTools))
		copy(out, dispatcherBaseTools)
		return out
	case "architect", "code-review":
		out := make([]string, 0, len(dispatcherBaseTools)+1)
		out = append(out, dispatcherBaseTools...)
		out = append(out, "Agent")
		return out
	default:
		panic("dispatcherAllowedToolsForRole: unknown role: " + role)
	}
}

// runRoleSmokeTest runs `pyry agent-run` with role-shaped inputs and asserts
// the realistic shape: zero exit, non-empty session id, trailer with no
// denials and num_turns >= 1, and a tail-side assistant event with
// EndOfTurn=true and TextChars > 0. Each of the five top-level tests
// delegates here so a single bug in the shape of the assertions is caught
// once. The pass/fail signal still surfaces per role because each test is a
// distinct top-level function.
func runRoleSmokeTest(t *testing.T, role, systemPrompt, userPrompt string) {
	t.Helper()
	workdir := WithWorktreeAuthenticated(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       userPrompt,
		SystemPrompt: systemPrompt,
		AllowedTools: dispatcherAllowedToolsForRole(role),
		MaxTurns:     4,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0\nstderr:\n%s", result.ExitCode, truncate(result.Stderr))
	}
	if result.SessionID == "" {
		t.Fatalf("SessionID is empty: no system/init envelope found in stdout\nstdout:\n%s", truncate(result.Stdout))
	}

	trailer, err := parseResultTrailer(result.Stdout)
	if err != nil {
		t.Fatalf("parseResultTrailer: %v\nstdout:\n%s", err, truncate(result.Stdout))
	}
	if trailer.PermissionDenials != nil && len(*trailer.PermissionDenials) != 0 {
		t.Fatalf("trailer.PermissionDenials = %d entries, want 0\nstderr:\n%s",
			len(*trailer.PermissionDenials), truncate(result.Stderr))
	}
	if trailer.NumTurns < 1 {
		t.Fatalf("trailer.NumTurns = %d, want >= 1\nstdout:\n%s", trailer.NumTurns, truncate(result.Stdout))
	}

	events := ReadJSONL(t, workdir, result.SessionID)
	jsonlPath := jsonlPathFor(workdir, result.SessionID)
	var last *JSONLEntry
	assistantCount := 0
	for i := range events {
		if events[i].Kind == "assistant" {
			assistantCount++
			last = &events[i]
		}
	}
	if last == nil {
		t.Fatalf("no assistant event found in JSONL\npath: %s\nstderr:\n%s", jsonlPath, truncate(result.Stderr))
	}
	if !last.EndOfTurn || last.TextChars <= 0 {
		t.Fatalf("last assistant event: EndOfTurn=%t TextChars=%d, want EndOfTurn=true and TextChars>0\npath: %s\nassistant events seen: %d",
			last.EndOfTurn, last.TextChars, jsonlPath, assistantCount)
	}
}

// truncate caps a stdout/stderr capture at 1024 bytes for embedding in
// t.Fatalf messages. Matches the pattern in tool_loop_test.go and
// allowed_tools_enforcement_test.go.
func truncate(b []byte) string {
	const max = 1024
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "... (truncated)"
}

func TestRealClaude_PO_RoleLoop(t *testing.T) {
	runRoleSmokeTest(t, "po",
		"You are a Pyrycode product-owner agent. You refine issue bodies. Use Read/Edit/qmd/codegraph for research.",
		"Rewrite this one-line ticket title in active voice. Reply with just the rewritten title: 'The CLI is crashed by malformed flags.'")
}

func TestRealClaude_Architect_RoleLoop(t *testing.T) {
	runRoleSmokeTest(t, "architect",
		"You are a Pyrycode architect agent. You produce implementation specs. Use Read/Grep/codegraph; delegate via Agent.",
		"Produce a five-line implementation sketch for: a Go function that returns the SHA-256 of stdin. Reply with the sketch only.")
}

func TestRealClaude_Developer_RoleLoop(t *testing.T) {
	runRoleSmokeTest(t, "developer",
		"You are a Pyrycode developer agent. You implement specs. Use Read/Edit/Bash to write and verify Go code.",
		"Implement a Go function `Add(a, b int) int` that returns a+b. Reply with the function only, no commentary.")
}

func TestRealClaude_CodeReview_RoleLoop(t *testing.T) {
	runRoleSmokeTest(t, "code-review",
		"You are a Pyrycode code-review agent. You review patches for correctness. Use Read/Grep/Agent for cross-file lookups.",
		"Review this three-line patch for obvious issues. Reply with one short paragraph: `func Add(a, b int) int { return a - b }`.")
}

func TestRealClaude_Documentation_RoleLoop(t *testing.T) {
	runRoleSmokeTest(t, "documentation",
		"You are a Pyrycode documentation agent. You summarize changes. Use Read/qmd to extract context.",
		"Summarize this one-line commit message in one sentence: 'fix(supervisor): handle SIGWINCH.'")
}
