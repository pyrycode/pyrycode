//go:build e2e_realclaude

package realclaude

// Budget-guardrail tests. Two cost-relevant guarantees that the realclaude
// suite did not previously exercise end-to-end:
//
//  1. Anthropic prompt-cache alignment across `pyry agent-run` invocations
//     within the 1-hour TTL when (system-prompt, allowed-tools, model,
//     effort) are identical. A regression that breaks cache-key alignment
//     (dynamic content leaking into the system prompt, per-invocation tool
//     list churn) would silently multiply API spend.
//
//  2. `pyry agent-run --max-turns=N` actually caps a run at N turns against
//     the real `claude` CLI. Pinned unit-side at
//     internal/agentrun/streamjson/emitter_test.go:225 and
//     internal/agentrun/budget/budget_test.go; this is the missing
//     end-to-end pin against the real upstream.
//
// Both tests compose existing fixtures (WithWorktreeAuthenticated,
// RunPyryAgentRun, parseResultTrailer) — no edits to fixtures.go.

import (
	"testing"
)

// cacheHitSystemPrompt is sized to clear the implicit prompt-cache minimum
// (per Anthropic public docs as of 2026-05, ~2048 tokens on Haiku 4.5 — the
// built-in claude system prompt plus tool definitions typically pushes the
// prefix over that threshold on its own, but we add a few sentences of
// deterministic content for margin). The only load-bearing properties are
// (a) deterministic across both runs (single const, no dynamic content) and
// (b) discourages tool use and verbose replies. Do NOT include the literal
// date, run id, or any other dynamic value — that is exactly the regression
// class this test is designed to catch.
const cacheHitSystemPrompt = "You are a Pyrycode end-to-end regression-guard test fixture. " +
	"You exist solely to drive a single, minimal claude turn so the surrounding test " +
	"can assert on the resulting stream-json output. Reply with at most one short word; " +
	"do not elaborate; do not invoke any tools; do not ask clarifying questions. " +
	"The test that hosts you is named TestRealClaude_CacheHitWarmsAcrossRuns and lives " +
	"in internal/e2e/realclaude/budget_test.go. Its job is to verify the Anthropic " +
	"prompt cache hits across two back-to-back invocations with identical " +
	"system-prompt content."

const cacheHitUserPrompt = "Reply with a single word: ok."

// TestRealClaude_CacheHitWarmsAcrossRuns runs the same prompt twice with the
// same system prompt and asserts the second invocation's result trailer
// records cache_read_input_tokens > 0. The diagnostic check on the first
// run's cache_creation_input_tokens surfaces sub-threshold system prompts
// as a soft signal (t.Errorf) — the second run could legitimately hit a
// pre-existing cache, so the primary assertion is the second-run read.
func TestRealClaude_CacheHitWarmsAcrossRuns(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	opts := RunOpts{
		Workdir:      workdir,
		Prompt:       cacheHitUserPrompt,
		SystemPrompt: cacheHitSystemPrompt,
		AllowedTools: []string{"Read"},
		MaxTurns:     1,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	}

	first := RunPyryAgentRun(t, opts)
	if first.ExitCode != 0 {
		t.Fatalf("first run: ExitCode = %d, want 0\nstderr:\n%s", first.ExitCode, first.Stderr)
	}
	if first.SessionID == "" {
		t.Fatalf("first run: SessionID is empty\nstdout:\n%s", truncateStdout(first.Stdout))
	}
	firstTrailer, err := parseResultTrailer(first.Stdout)
	if err != nil {
		t.Fatalf("first run: parseResultTrailer: %v\nstdout:\n%s", err, truncateStdout(first.Stdout))
	}

	second := RunPyryAgentRun(t, opts)
	if second.ExitCode != 0 {
		t.Fatalf("second run: ExitCode = %d, want 0\nstderr:\n%s", second.ExitCode, second.Stderr)
	}
	if second.SessionID == "" {
		t.Fatalf("second run: SessionID is empty\nstdout:\n%s", truncateStdout(second.Stdout))
	}
	secondTrailer, err := parseResultTrailer(second.Stdout)
	if err != nil {
		t.Fatalf("second run: parseResultTrailer: %v\nstdout:\n%s", err, truncateStdout(second.Stdout))
	}

	// Primary assertion: the second run hit the prompt cache.
	if secondTrailer.Usage.CacheReadInputTokens <= 0 {
		t.Fatalf("second run: trailer.Usage.CacheReadInputTokens = %d, want > 0 "+
			"(cache did not warm across identical-prompt runs; check for dynamic content "+
			"in the system prompt or per-invocation tool-list churn)\n"+
			"first  usage: %+v\nsecond usage: %+v",
			secondTrailer.Usage.CacheReadInputTokens,
			firstTrailer.Usage, secondTrailer.Usage)
	}

	// Diagnostic (t.Errorf, not t.Fatalf): the first run paid the cache
	// creation cost. A 0 here with the primary assertion still passing
	// means the cache was already warm from an earlier test run — not a
	// regression. A 0 here with a 0 on the primary would mean the system
	// prompt is below the per-model implicit cache threshold; pad it.
	if firstTrailer.Usage.CacheCreationInputTokens <= 0 {
		t.Errorf("first run: trailer.Usage.CacheCreationInputTokens = %d, want > 0 "+
			"(soft signal: either the cache was pre-warmed by a prior nightly run, or "+
			"the system prompt is below the per-model implicit-cache threshold and "+
			"needs padding)\nfirst usage: %+v",
			firstTrailer.Usage.CacheCreationInputTokens, firstTrailer.Usage)
	}
}

// maxTurnsSystemPrompt steers the model toward sequential single-command
// Bash invocations so the run consumes one assistant turn per step.
const maxTurnsSystemPrompt = "You are an e2e regression-guard test. When asked to run shell commands, " +
	"use the Bash tool once per command and wait for each result before continuing."

// maxTurnsPrompt forces ≥5 distinct tool turns. If a future haiku revision
// is smart enough to fire all five `echo`s in one tool_use block (or
// otherwise complete in ≤2 turns naturally), the assertions
// `Subtype == "error_max_turns"` and `TerminalReason == "max_turns"` will
// fail loudly — the mitigation is to bump this prompt to force more turns
// (e.g. 8 sequential `read X.txt` calls with seeded file content the model
// must enumerate by name), not to weaken the assertion.
const maxTurnsPrompt = "Use the Bash tool five times in sequence to run these commands one at " +
	"a time, each in its own separate Bash call, waiting for each result before issuing " +
	"the next:\n" +
	"1. echo step-one\n" +
	"2. echo step-two\n" +
	"3. echo step-three\n" +
	"4. echo step-four\n" +
	"5. echo step-five\n" +
	"Do NOT combine these into a single command. Do NOT use && or ; to chain them. " +
	"After all five outputs, tell me which step numbers you saw."

// TestRealClaude_MaxTurnsHonored asserts that `pyry agent-run --max-turns=2`
// against a prompt requiring ≥5 turns terminates at the cap with the
// budget-exhaustion wire signal (subtype/terminal_reason/is_error), exact
// num_turns == 2, and stop_reason != "end_turn".
//
// ExitCode == 0 is correct here: `pyry agent-run` exits 0 on a successfully
// emitted result trailer regardless of trailer is_error. The budget
// exhaustion signal lives in the trailer fields, NOT the subprocess exit
// code.
func TestRealClaude_MaxTurnsHonored(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       maxTurnsPrompt,
		SystemPrompt: maxTurnsSystemPrompt,
		AllowedTools: []string{"Bash"},
		MaxTurns:     2,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0\nstderr:\n%s", result.ExitCode, result.Stderr)
	}
	if result.SessionID == "" {
		t.Fatalf("SessionID is empty\nstdout:\n%s", truncateStdout(result.Stdout))
	}

	trailer, err := parseResultTrailer(result.Stdout)
	if err != nil {
		t.Fatalf("parseResultTrailer: %v\nstdout:\n%s", err, truncateStdout(result.Stdout))
	}

	if trailer.Subtype != "error_max_turns" {
		t.Fatalf("trailer.Subtype = %q, want %q", trailer.Subtype, "error_max_turns")
	}
	if trailer.TerminalReason != "max_turns" {
		t.Fatalf("trailer.TerminalReason = %q, want %q", trailer.TerminalReason, "max_turns")
	}
	if trailer.NumTurns != 2 {
		t.Fatalf("trailer.NumTurns = %d, want 2 (exact — the budget caps at exactly --max-turns; "+
			"an off-by-one fires here)", trailer.NumTurns)
	}
	if trailer.StopReason == "end_turn" {
		t.Fatalf("trailer.StopReason = %q, want != %q (natural completion would indicate "+
			"the model collapsed the 5-step prompt into ≤2 turns; bump maxTurnsPrompt to "+
			"force more turns)", trailer.StopReason, "end_turn")
	}
	if !trailer.IsError {
		t.Fatalf("trailer.IsError = false, want true (wire-level budget-exhaustion signal)")
	}
}

// truncateStdout caps stdout in failure messages at 1 KiB so a large
// stream-json transcript does not bury the assertion. Mirrors the pattern
// at tool_loop_test.go:127.
func truncateStdout(stdout []byte) string {
	if len(stdout) > 1024 {
		return string(stdout[:1024]) + "... (truncated)"
	}
	return string(stdout)
}
