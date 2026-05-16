//go:build e2e_realclaude

package realclaude

// MCP server smoke tests for the four servers required by the dispatcher
// agents (qmd, context7, codegraph, plugin:figma:figma). Each test drives
// a haiku turn through `pyry agent-run` and asserts that the named MCP
// tool fires with a non-empty `tool_result`. Protocol drift on any of the
// four servers (renamed tool, changed parameter shape, broken stdio
// handshake) surfaces as a structured failure here rather than silently
// wedging a dispatched agent mid-run.
//
// These tests deliberately do NOT use WithWorktree / WithWorktreeAuthenticated:
// both pin $HOME to a tempdir, which disables claude's MCP server discovery
// (it reads `$HOME/.claude.json` and `$HOME/.claude/plugins/...`). The tests
// allocate a workdir via `t.TempDir()` and leave $HOME pointing at the outer
// operator's home so claude resolves the same MCP server set the operator
// has configured. The resulting JSONL session files end up under the outer
// `~/.claude/projects/<encoded-tempdir>/` — acceptable because (a) the
// encoded path is unique per run, (b) the suite is not fully hermetic
// today (WithWorktreeAuthenticated already leaks ANTHROPIC_API_KEY), and
// (c) CI runners have a controlled HOME.

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// figmaTestFileURL is a publicly accessible Figma Community file used as
// the argument to `mcp__plugin_figma_figma__get_metadata`. If this URL
// stops resolving, replace it with another stable public Figma file — the
// test asserts the MCP protocol round-trip, not any one file's content.
const figmaTestFileURL = "https://www.figma.com/community/file/1035203688168086460/material-3-design-kit"

// mcpSmokeSystemPrompt is the shared system prompt for the four MCP smoke
// tests. Keeps the model on a tight rail: call the named MCP tool once
// with the supplied parameters, do not improvise.
const mcpSmokeSystemPrompt = "You are an e2e regression-guard test. When asked to use an MCP tool, call exactly that tool once with the requested parameters, then briefly report the result."

func TestRealClaude_MCP_QMD(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skipf("TestRealClaude_MCP_QMD: ANTHROPIC_API_KEY is unset in the outer environment; this test calls the real Anthropic API. Export it (or rely on a CI secret) to run.")
	}
	if skip, reason := mcpServerHealthy(t, "qmd"); skip {
		t.Skipf("TestRealClaude_MCP_QMD: qmd MCP server unavailable: %s", reason)
	}

	workdir := t.TempDir()

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "Use the mcp__qmd__query tool to search the `second-brain` collection for the literal term `pyrycode`. After the call returns, briefly state how many results you got.",
		SystemPrompt: mcpSmokeSystemPrompt,
		AllowedTools: []string{"mcp__qmd__query", "Bash"},
		MaxTurns:     3,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	assertMCPToolUsed(t, workdir, result, "mcp__qmd__query")
}

func TestRealClaude_MCP_Context7(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skipf("TestRealClaude_MCP_Context7: ANTHROPIC_API_KEY is unset in the outer environment; this test calls the real Anthropic API. Export it (or rely on a CI secret) to run.")
	}
	if skip, reason := mcpServerHealthy(t, "plugin:context7:context7"); skip {
		t.Skipf("TestRealClaude_MCP_Context7: plugin:context7:context7 MCP server unavailable: %s", reason)
	}

	workdir := t.TempDir()

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "Use the mcp__plugin_context7_context7__resolve-library-id tool to resolve the library id for `react`. After the call returns, briefly report whether you got a result.",
		SystemPrompt: mcpSmokeSystemPrompt,
		AllowedTools: []string{"mcp__plugin_context7_context7__resolve-library-id", "Bash"},
		MaxTurns:     3,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	assertMCPToolUsed(t, workdir, result, "mcp__plugin_context7_context7__resolve-library-id")
}

// TestRealClaude_MCP_CodeGraph requires the `codegraph` binary on PATH so
// the test can seed a `.codegraph/` index in the per-test workdir. Empty
// index → empty search results regardless of protocol health, so the test
// skips (not fails) when the indexer is absent or fails.
func TestRealClaude_MCP_CodeGraph(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skipf("TestRealClaude_MCP_CodeGraph: ANTHROPIC_API_KEY is unset in the outer environment; this test calls the real Anthropic API. Export it (or rely on a CI secret) to run.")
	}
	if skip, reason := mcpServerHealthy(t, "codegraph"); skip {
		t.Skipf("TestRealClaude_MCP_CodeGraph: codegraph MCP server unavailable: %s", reason)
	}

	workdir := t.TempDir()

	// Seed a tiny Go source file with a distinctive sentinel symbol so the
	// codegraph_search call below has a deterministic single-match target.
	const sentinel = "PyrycodeSeedSentinel384"
	seed := "package seed\n\nfunc " + sentinel + "() {}\n"
	if err := os.WriteFile(filepath.Join(workdir, "seed.go"), []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed.go: %v", err)
	}
	if _, err := exec.LookPath("codegraph"); err != nil {
		t.Skipf("TestRealClaude_MCP_CodeGraph: codegraph binary not on PATH: %v; install codegraph to run this test", err)
	}
	indexCtx, indexCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer indexCancel()
	indexCmd := exec.CommandContext(indexCtx, "codegraph", "index", ".")
	indexCmd.Dir = workdir
	if out, err := indexCmd.CombinedOutput(); err != nil {
		t.Skipf("TestRealClaude_MCP_CodeGraph: `codegraph index .` failed in workdir: %v\noutput:\n%s", err, truncate(out))
	}

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "Use the mcp__codegraph__codegraph_search tool to find the symbol `" + sentinel + "` in the current directory's index. After the call returns, briefly state whether you got a match.",
		SystemPrompt: mcpSmokeSystemPrompt,
		AllowedTools: []string{"mcp__codegraph__codegraph_search", "Bash"},
		MaxTurns:     3,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	assertMCPToolUsed(t, workdir, result, "mcp__codegraph__codegraph_search")
}

// TestRealClaude_MCP_Figma requires the operator's figma MCP server to be
// authenticated. `! Needs authentication` or `✗ Failed to connect` in
// `claude mcp list` skips the test cleanly. The pinned figmaTestFileURL
// points at a public Figma Community file; if it stops resolving, swap it
// for another stable public file — the test asserts the protocol round-
// trip, not any one file's content.
func TestRealClaude_MCP_Figma(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skipf("TestRealClaude_MCP_Figma: ANTHROPIC_API_KEY is unset in the outer environment; this test calls the real Anthropic API. Export it (or rely on a CI secret) to run.")
	}
	if skip, reason := mcpServerHealthy(t, "plugin:figma:figma"); skip {
		t.Skipf("TestRealClaude_MCP_Figma: plugin:figma:figma MCP server unavailable: %s", reason)
	}

	workdir := t.TempDir()

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "Use the mcp__plugin_figma_figma__get_metadata tool on the URL `" + figmaTestFileURL + "` and briefly tell me the file name.",
		SystemPrompt: mcpSmokeSystemPrompt,
		AllowedTools: []string{"mcp__plugin_figma_figma__get_metadata", "Bash"},
		MaxTurns:     3,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	assertMCPToolUsed(t, workdir, result, "mcp__plugin_figma_figma__get_metadata")
}

// assertMCPToolUsed asserts the shape every MCP smoke test demands:
//   - zero exit code and a non-empty SessionID,
//   - an assistant tool_use with `name == toolName` and a non-empty id,
//   - a matching user tool_result with the same tool_use_id and a non-
//     empty content field,
//   - the stream-json result trailer has no permission denials.
//
// Content text is server- and version-dependent (qmd snippets, figma file
// names, codegraph match shapes all vary across runs), so the assertion
// is len(content) > 0 — non-emptiness is sufficient to prove the protocol
// round-trip succeeded.
func assertMCPToolUsed(t *testing.T, workdir string, result RunResult, toolName string) {
	t.Helper()
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0\nstderr:\n%s", result.ExitCode, truncate(result.Stderr))
	}
	if result.SessionID == "" {
		t.Fatalf("SessionID is empty: no system/init envelope found in stdout\nstdout:\n%s", truncate(result.Stdout))
	}

	events := ReadJSONL(t, workdir, result.SessionID)
	jsonlPath := jsonlPathFor(workdir, result.SessionID)

	var toolUseID string
	var sawToolResult bool
	var toolResultContentLen int
	for _, e := range events {
		switch e.Kind {
		case "assistant":
			if toolUseID != "" {
				continue
			}
			blocks, err := parseContentBlocks(e.Raw)
			if err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_use" && b.Name == toolName && b.ID != "" {
					toolUseID = b.ID
					break
				}
			}
		case "user":
			if toolUseID == "" || sawToolResult {
				continue
			}
			blocks, err := parseContentBlocks(e.Raw)
			if err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID == toolUseID {
					sawToolResult = true
					toolResultContentLen = len(b.Content)
					break
				}
			}
		}
	}

	if toolUseID == "" {
		t.Fatalf("no %s tool_use observed in assistant entries; path: %s", toolName, jsonlPath)
	}
	if !sawToolResult {
		t.Fatalf("%s tool_use id %s present but no matching tool_result; path: %s", toolName, toolUseID, jsonlPath)
	}
	if toolResultContentLen == 0 {
		t.Fatalf("%s tool_result for id %s has empty content field; path: %s", toolName, toolUseID, jsonlPath)
	}

	trailer, err := parseResultTrailer(result.Stdout)
	if err != nil {
		t.Fatalf("parseResultTrailer: %v\nstdout:\n%s", err, truncate(result.Stdout))
	}
	if trailer.PermissionDenials != nil && len(*trailer.PermissionDenials) != 0 {
		t.Fatalf("trailer.PermissionDenials = %d entries, want 0", len(*trailer.PermissionDenials))
	}
}

// mcpServerHealthy runs `claude mcp list` once and inspects the output for
// `displayName: ... ✓ Connected`. Returns (false, "") when healthy;
// (true, reason) when the server is missing, failed, or needs auth —
// callers invoke t.Skipf with the reason so the test board surfaces a
// skip with operator-actionable context rather than a misattributed
// failure.
//
// The probe uses the PATH-resolved `claude` binary; PYRY_CLAUDE_BIN is
// intentionally NOT honored here (that variable is reserved for stubbed
// pyry tests and carries fork-bomb defenses elsewhere — see
// resilience_test.go:resolveClaudeBin). Bounded by 10s: if `claude mcp
// list` itself hangs because some unrelated server is stuck on startup,
// returns a skip with that reason rather than blocking the suite.
func mcpServerHealthy(t *testing.T, displayName string) (bool, string) {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		return true, "claude binary not on PATH: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "mcp", "list").CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return true, "claude mcp list timed out after 10s"
	}
	if err != nil {
		return true, "claude mcp list failed: " + err.Error()
	}
	prefix := displayName + ":"
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if strings.Contains(line, "✓ Connected") {
			return false, ""
		}
		// Found the server, but it is not connected. Surface the status
		// suffix (after the final " - ") so the skip message carries the
		// operator-facing reason ("! Needs authentication", "✗ Failed to
		// connect", etc.) rather than just "unhealthy".
		if idx := strings.LastIndex(line, " - "); idx >= 0 {
			return true, strings.TrimSpace(line[idx+3:])
		}
		return true, line
	}
	return true, "no entry matching " + prefix + " in claude mcp list output"
}
