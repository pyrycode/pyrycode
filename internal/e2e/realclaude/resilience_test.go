//go:build e2e_realclaude

package realclaude

// Protocol-resilience tests for the real `claude` binary. Each test pins
// a specific production failure mode to a structured exit signal (exit
// code, JSONL event, or stderr substring) — no test passes on "claude
// eventually returned something." A hang IS the failure mode being
// guarded against, so every test runs under a bounded timeout.
//
// Two tests (NonZeroExit, LargePrompt) drive claude through pyry via
// RunPyryAgentRun. Two tests (PrematureStdinClose, MalformedStreamJSON)
// exercise claude directly via exec.CommandContext so the test can
// control stdin bytes and observe claude's raw response shape; the
// local helpers at the bottom of this file own that surface.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealClaude_BashTool_NonZeroExit verifies that a non-zero Bash exit
// surfaces as a structured tool_result with is_error=true and that the
// surrounding turn still resolves with subtype="success" (graceful tool
// failure, not a pipe break). Same JSONL-walk shape as
// TestRealClaude_ToolLoopIntegrity; only the prompt + the is_error
// assertion differ.
func TestRealClaude_BashTool_NonZeroExit(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "Use the Bash tool to run \"ls /nonexistent-path-for-pyrycode-382\". Report what you see.",
		SystemPrompt: "You are an e2e regression-guard test. When asked to run a Bash command, run it once with the Bash tool and report the result.",
		AllowedTools: []string{"Bash"},
		MaxTurns:     3,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0\nstderr:\n%s", result.ExitCode, result.Stderr)
	}
	if result.SessionID == "" {
		t.Fatalf("SessionID is empty: no system/init envelope found in stdout\nstdout:\n%s", truncate(result.Stdout))
	}

	events := ReadJSONL(t, workdir, result.SessionID)
	jsonlPath := jsonlPathFor(workdir, result.SessionID)

	// Three independent observations, tracked separately so a failure
	// message names the actual missing event rather than a downstream
	// consequence of an earlier missing event:
	//   1. assistant tool_use(name=Bash) → bashToolUseID
	//   2. matching user tool_result(tool_use_id=bashToolUseID)
	//      → sawToolResult, toolResultIsErr, toolResultContent
	//   3. any subsequent assistant text block → sawFinalText
	// is_error is a documented stream-json field (verified against
	// internal/agentrun/jsonl/testdata/{clean,no_end_turn,double_end_turn}.jsonl,
	// which contain 19 `"is_error":...` occurrences from real claude runs).
	var bashToolUseID string
	var sawToolResult bool
	var toolResultIsErr bool
	var toolResultContent json.RawMessage
	var sawFinalText bool

	for _, e := range events {
		switch e.Kind {
		case "assistant":
			blocks, err := parseContentBlocks(e.Raw)
			if err != nil {
				continue
			}
			if bashToolUseID == "" {
				for _, b := range blocks {
					if b.Type == "tool_use" && b.Name == "Bash" && b.ID != "" {
						bashToolUseID = b.ID
						break
					}
				}
			} else if sawToolResult {
				for _, b := range blocks {
					if b.Type == "text" && b.Text != "" {
						sawFinalText = true
						break
					}
				}
			}
		case "user":
			if bashToolUseID == "" {
				continue
			}
			blocks, err := parseContentBlocks(e.Raw)
			if err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID == bashToolUseID {
					sawToolResult = true
					toolResultIsErr = b.IsError
					toolResultContent = b.Content
					break
				}
			}
		}
	}

	if bashToolUseID == "" {
		t.Fatalf("no Bash tool_use observed in assistant entries; path: %s", jsonlPath)
	}
	if !sawToolResult {
		t.Fatalf("Bash tool_use id %s present but no matching tool_result; path: %s", bashToolUseID, jsonlPath)
	}
	if !toolResultIsErr {
		t.Fatalf("Bash tool_result for id %s did not set is_error=true (content=%s); path: %s",
			bashToolUseID, string(toolResultContent), jsonlPath)
	}
	if len(toolResultContent) == 0 {
		t.Fatalf("Bash tool_result for id %s has empty content field; path: %s", bashToolUseID, jsonlPath)
	}
	if !sawFinalText {
		t.Fatalf("no subsequent assistant text block after errored tool_result; path: %s", jsonlPath)
	}

	trailer, err := parseResultTrailer(result.Stdout)
	if err != nil {
		t.Fatalf("parseResultTrailer: %v\nstdout:\n%s", err, truncate(result.Stdout))
	}
	// Per spec: subtype == "success" IS the structured contract ("graceful
	// tool failure, not pipe break"). Do NOT assert on StopReason — claude
	// may end on end_turn or tool_use_error and both satisfy the AC.
	if trailer.Subtype != "success" {
		t.Fatalf("trailer.Subtype = %q, want %q", trailer.Subtype, "success")
	}
}

// TestRealClaude_PrematureStdinClose verifies claude exits cleanly within
// a bounded timeout when stdin is closed after the initial user-turn
// envelope is written — the exact pattern streamrunner uses in production.
// A regression where claude blocks waiting for a second envelope would
// trip the deadline assertion.
func TestRealClaude_PrematureStdinClose(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)
	bin := resolveClaudeBin(t)

	envelope, err := buildUserTurnEnvelope("Reply with a single short word.")
	if err != nil {
		t.Fatalf("buildUserTurnEnvelope: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	exitCode, stderr, runErr := runClaudeDirect(t, ctx, bin, workdir, envelope)

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("claude did not exit within 60s after stdin close — hang regression\nrunErr=%v\nstderr:\n%s",
			runErr, truncate(stderr))
	}
	if exitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (clean exit on stdin close)\nstderr:\n%s",
			exitCode, truncate(stderr))
	}
}

// TestRealClaude_MalformedStreamJSON verifies claude rejects garbage
// stdin with a non-zero exit and a non-empty diagnostic, rather than
// crashing silently or hanging. Bounded by 60s timeout.
func TestRealClaude_MalformedStreamJSON(t *testing.T) {
	workdir := WithWorktree(t)
	bin := resolveClaudeBin(t)

	garbage := []byte("this is not stream-json\n}}{{not json either\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	exitCode, stderr, runErr := runClaudeDirect(t, ctx, bin, workdir, garbage)

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("claude did not exit within 60s on malformed stdin — hang regression\nrunErr=%v\nstderr:\n%s",
			runErr, truncate(stderr))
	}
	if exitCode == 0 {
		t.Fatalf("ExitCode = 0, want non-zero (claude must reject malformed stream-json)\nstderr:\n%s",
			truncate(stderr))
	}
	if len(stderr) == 0 {
		t.Fatalf("stderr is empty; want a diagnostic on malformed stdin")
	}
	// Keyword-OR predicate per spec: do NOT pin a specific phrase (couples
	// the test to claude's prose). Match any of these case-insensitive
	// substrings to confirm a parse-shaped diagnostic surfaced.
	lower := strings.ToLower(string(stderr))
	keywords := []string{"json", "parse", "input", "format", "envelope"}
	matched := false
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("stderr contains no parse-shaped keyword (any of %v)\nstderr:\n%s",
			keywords, truncate(stderr))
	}
}

// TestRealClaude_LargePromptNearContextWindow sends ~52 KB of repeating
// text and asserts that claude EITHER processes it without an
// overflow-shaped stop_reason OR rejects it via a structured stream-json
// result trailer with a non-success subtype. The failure mode is
// "neither" — no result trailer at all (crash) or success with an
// overflow-shaped stop_reason (silent truncation).
func TestRealClaude_LargePromptNearContextWindow(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	// "All work and no play makes Jack a dull boy. " is 44 bytes;
	// 1200 repetitions = ~52 KB.
	prompt := strings.Repeat("All work and no play makes Jack a dull boy. ", 1200)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       prompt,
		SystemPrompt: "You are an e2e regression-guard test. Reply briefly.",
		AllowedTools: []string{"Read"},
		MaxTurns:     3,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	if result.SessionID == "" {
		t.Fatalf("SessionID is empty: no system/init envelope found in stdout\nstdout:\n%s",
			truncate(result.Stdout))
	}

	trailer, err := parseResultTrailer(result.Stdout)
	if err != nil {
		t.Fatalf("no result trailer in stdout (crash mid-stream is the failure mode being guarded against): %v\nstdout:\n%s",
			err, truncate(result.Stdout))
	}

	overflowReasons := map[string]struct{}{
		"context_overflow":   {},
		"max_context_length": {},
	}

	_, overflowShaped := overflowReasons[trailer.StopReason]
	branchA := trailer.Subtype == "success" && !overflowShaped
	branchB := trailer.Subtype != "success" && result.ExitCode != 0

	if !branchA && !branchB {
		t.Fatalf("large prompt produced neither (A) success-without-overflow nor (B) non-success-with-error-exit:\n"+
			"  trailer.Subtype=%q trailer.StopReason=%q result.ExitCode=%d\n"+
			"branch A requires: Subtype=\"success\" AND StopReason NOT in %v\n"+
			"branch B requires: Subtype != \"success\" AND ExitCode != 0\n"+
			"stdout:\n%s\nstderr:\n%s",
			trailer.Subtype, trailer.StopReason, result.ExitCode,
			[]string{"context_overflow", "max_context_length"},
			truncate(result.Stdout), truncate(result.Stderr))
	}
}

// resolveClaudeBin returns the path to the real claude binary. Honors
// PYRY_CLAUDE_BIN unless it points at the current test binary (defense
// against the 2026-05-16 fork-bomb pattern documented on
// RunOpts.UseTestBinaryAsFakePyry). Falls back to exec.LookPath("claude"),
// skipping the test with a named-variable message when not found —
// matches smoke_test.go's skip-with-context shape.
func resolveClaudeBin(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("PYRY_CLAUDE_BIN"); env != "" {
		envAbs, err1 := filepath.Abs(env)
		selfAbs, err2 := filepath.Abs(os.Args[0])
		if err1 == nil && err2 == nil && envAbs == selfAbs {
			t.Fatalf("resolveClaudeBin: PYRY_CLAUDE_BIN=%s points at the test binary itself; "+
				"refusing to use as claude (fork-bomb defense, 2026-05-16)", env)
		}
		return env
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("resolveClaudeBin: claude binary not on PATH: %v\nthis test requires the real claude CLI; install it or adjust PATH before running `make e2e-realclaude`", err)
	}
	return bin
}

// directClaudeArgs returns the argv used when invoking claude directly
// (sans pyry). Mirrors buildClaudeArgs in cmd/pyry/agent_run.go but
// omits --append-system-prompt-file because the direct-exec tests don't
// need a system prompt for the property they probe.
func directClaudeArgs() []string {
	return []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--model", "claude-haiku-4-5",
		"--effort", "low",
		"--max-turns", "3",
		"--allowed-tools", "Read",
	}
}

// runClaudeDirect spawns claude under ctx with directClaudeArgs(), writes
// stdinBytes, closes stdin, and returns the exit code plus captured
// stderr. Does NOT call t.Fatalf on non-zero exit or on ctx-cancel-driven
// kill — callers assert those themselves. The third return value is the
// raw error from cmd.Wait (useful for diagnostics when ctx fires).
//
// Stdout is captured (so it is drained and the child's pipe never
// backpressures) but not returned: neither caller asserts on response
// content. If a future raw-exec test needs the body, surface it then.
func runClaudeDirect(t *testing.T, ctx context.Context, bin, workdir string, stdinBytes []byte) (int, []byte, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, directClaudeArgs()...)
	cmd.Dir = workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("runClaudeDirect: stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("runClaudeDirect: start: %v", err)
	}
	if _, err := stdin.Write(stdinBytes); err != nil {
		// Mirror streamrunner's "log-and-continue" posture: the child's
		// exit status is the authoritative outcome. A write error here
		// (typically EPIPE wrapped in *os.PathError when claude has
		// already closed its end, e.g. Test 3's fast-reject path) is
		// diagnostic, not a test fault on its own.
		t.Logf("runClaudeDirect: stdin write: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Logf("runClaudeDirect: stdin close: %v", err)
	}

	waitErr := cmd.Wait()
	return cmd.ProcessState.ExitCode(), stderr.Bytes(), waitErr
}

// buildUserTurnEnvelope produces the single newline-terminated JSON line
// streamrunner writes to claude's stdin in production. Kept local to
// avoid importing internal/agentrun/streamrunner from a test helper.
func buildUserTurnEnvelope(prompt string) ([]byte, error) {
	type contentText struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role    string        `json:"role"`
		Content []contentText `json:"content"`
	}
	type envelope struct {
		Type    string  `json:"type"`
		Message message `json:"message"`
	}
	b, err := json.Marshal(envelope{
		Type: "user",
		Message: message{
			Role:    "user",
			Content: []contentText{{Type: "text", Text: prompt}},
		},
	})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

