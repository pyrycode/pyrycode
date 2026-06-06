//go:build e2e_realclaude

package realclaude

// Long-running session JSONL append-integrity regression guard. Existing
// realclaude tests cap at MaxTurns ∈ {1,2,3,4}; the ≥10-turn append path
// through claude's session JSONL is unexercised end-to-end. This test
// drives a real claude session through ≥10 single-command Bash turns and
// asserts that:
//
//   - pyry's result trailer reports num_turns >= 10,
//   - the on-disk JSONL contains ≥10 assistant entries that parse cleanly
//     (the append path under test); end-of-turn markers are NOT counted —
//     in a tool-use sequence claude emits ~1 end_turn (the final summary),
//     not one per turn, so the old ">=10 EndOfTurn" bullet mismodeled the
//     protocol and could not pass on any claude version,
//   - the captured stderr does NOT contain bufio.Scanner's "token too long"
//     error text — a forward-defensive tripwire for the day someone wires a
//     stdlib Scanner into pyry's stdout/stderr path without bumping its
//     buffer.
//
// Cost budget: ~$0.05–$0.10 per `make e2e-realclaude` run on
// claude-haiku-4-5 / Effort: low. Bounded; in line with budget_test.go.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// longSessionSystemPrompt steers the model toward sequential single-command
// Bash invocations so the run consumes one assistant turn per step. Mirrors
// the anti-chain wording in budget_test.go's maxTurnsSystemPrompt.
const longSessionSystemPrompt = "You are an e2e regression-guard test. " +
	"When asked to run shell commands, use the Bash tool one command at a time, " +
	"wait for each result before continuing, do NOT combine commands, and do NOT " +
	"chain commands with && or ;."

// longSessionUserPrompt enumerates ten distinct single-command Bash operations
// against the seeded numbers.txt. The final "summarize" sentence nudges the
// model to emit one more assistant end_turn text block, giving the last-event
// assertion something to land on. If a future haiku revision collapses the
// list into fewer turns despite the anti-chain steering, bump the operation
// count or strengthen the steering — do NOT lower the NumTurns >= 10
// threshold (that defeats the test's purpose).
const longSessionUserPrompt = "Use the Bash tool ten times in sequence to run these commands one at " +
	"a time against numbers.txt, each in its own separate Bash call, waiting for each result before " +
	"issuing the next:\n" +
	"1. wc -l numbers.txt\n" +
	"2. head -n 3 numbers.txt\n" +
	"3. tail -n 3 numbers.txt\n" +
	"4. sort numbers.txt\n" +
	"5. uniq numbers.txt\n" +
	"6. cat numbers.txt\n" +
	"7. grep 5 numbers.txt\n" +
	"8. wc -c numbers.txt\n" +
	"9. awk '{s+=$1} END {print s}' numbers.txt\n" +
	"10. ls -l numbers.txt\n" +
	"Do NOT combine these into a single command. Do NOT use && or ; to chain them. " +
	"After all ten results, summarize what you saw in one short sentence."

// TestRealClaude_LongSessionJSONLIntegrity is the regression sensor described
// in #421. It is the only ≥10-turn realclaude test in the suite; if the
// upstream model starts collapsing turns or pyry's stdout/stderr handling
// silently downgrades a Scanner buffer, this test fails.
func TestRealClaude_LongSessionJSONLIntegrity(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	numbersPath := filepath.Join(workdir, "numbers.txt")
	if err := os.WriteFile(numbersPath, []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", numbersPath, err)
	}

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       longSessionUserPrompt,
		SystemPrompt: longSessionSystemPrompt,
		AllowedTools: []string{"Bash"},
		MaxTurns:     12,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
		Timeout:      10 * time.Minute,
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
	if trailer.NumTurns < 10 {
		t.Fatalf("trailer.NumTurns = %d, want >= 10 (if the model is collapsing turns, expand the "+
			"prompt or strengthen anti-chain steering — do NOT lower the threshold)\nstdout:\n%s",
			trailer.NumTurns, truncate(result.Stdout))
	}

	events := ReadJSONL(t, workdir, result.SessionID)
	jsonlPath := jsonlPathFor(workdir, result.SessionID)

	assistantCount := 0
	for i := range events {
		if events[i].Kind == "assistant" {
			assistantCount++
		}
	}
	// This test guards the >=10-turn append path: >=10 assistant entries
	// written to and read back from the on-disk session JSONL with no parse
	// error (ReadJSONL fatals on any malformed line, so a clean return here
	// already proves append integrity). We deliberately do NOT count EndOfTurn
	// (stop_reason=="end_turn") markers. In claude's tool-use protocol a
	// tool-calling assistant message stops with "tool_use"; only a final
	// yield-to-user message stops with "end_turn". A ten-command sequence
	// therefore yields ~1 end_turn (the summary), or 0 when the turn budget is
	// spent on tool calls before a summary is reached — never one per command.
	// The previous ">=10 EndOfTurn" assertion mismodeled the protocol and could
	// not pass on any claude version; the trailer's NumTurns>=10 check above is
	// the correct turn-count signal.
	if assistantCount < 10 {
		t.Fatalf("assistant entry count = %d, want >= 10 (the >=10-turn JSONL append path is the "+
			"regression surface this test guards)\npath: %s", assistantCount, jsonlPath)
	}

	// Forward-defensive tripwire: the literal stdlib error text bufio.Scanner
	// emits when a single token exceeds its buffer. Today pyry's production
	// stdout/stderr path has no Scanner that could fire this; the assertion
	// catches the day someone introduces one without bumping the buffer.
	if bytes.Contains(result.Stderr, []byte("bufio.Scanner: token too long")) {
		t.Fatalf("stderr contains \"bufio.Scanner: token too long\" — a Scanner in pyry's stdout/stderr "+
			"path hit a long claude line; bump its buffer to 1 MiB (see tool_loop_test.go:210 note)\n"+
			"stderr:\n%s", truncate(result.Stderr))
	}
}
