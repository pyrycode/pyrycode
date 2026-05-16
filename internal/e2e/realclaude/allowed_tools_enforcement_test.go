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
//
// The test also asserts the operator-visible denial signal (#420): when
// the gate holds, at least one of (assistant-text refusal keyword,
// structured `result` envelope with permission_denials or is_error) must
// be present, so a regression that silently no-ops on a denied call
// fails. The two assertions are co-located because they guard one
// contract against the same argv and the same model call.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
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

	// Gate held. Now assert the operator-visible signal (#420): a
	// regression that silently no-ops on the denied call would pass the
	// gate-held check above but leave the operator with no perceptible
	// signal. Either channel alone is sufficient — channel-agnostic by
	// design so a future claude that swaps text↔structured does not
	// break this test.
	textHit, assistantCount := assistantTextRefusalHit(events)
	structHit, stdoutLines := structuredDenialHit(result.Stdout)
	if !textHit && !structHit {
		t.Fatalf("permission gate held but produced no operator-visible signal: "+
			"assistant text contained none of %v across %d assistant entries; "+
			"stdout result envelope had permission_denials empty and is_error=false across %d lines.\npath: %s",
			denialKeywords, assistantCount, stdoutLines, jsonlPath)
	}
}

// denialKeywords is the lowercase substring set the text-channel
// detector accepts as evidence of an operator-visible refusal. Kept
// narrow on purpose: broad markers like "available" or "access" appear
// in non-refusal contexts. The disjunctive design (text OR structured)
// tolerates a future model that declines with outside-the-set wording
// AS LONG AS it still emits a structured signal.
var denialKeywords = []string{
	"cannot",
	"can't",
	"unable",
	"not allowed",
	"permission",
}

// assistantTextRefusalHit walks events, decoding each assistant entry's
// message.content[] and returning true on the first text block whose
// lowercased content contains any keyword in denialKeywords. Returns
// the number of assistant entries inspected alongside the hit so a
// failure message can quote the search width.
//
// JSON unmarshal errors skip the line silently (mirroring the existing
// bashInvokedInRaw loop's selfcheck.go:283 policy).
func assistantTextRefusalHit(events []JSONLEntry) (bool, int) {
	assistantCount := 0
	for _, e := range events {
		if e.Kind != "assistant" {
			continue
		}
		assistantCount++
		var line struct {
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(e.Raw, &line); err != nil {
			continue
		}
		for _, c := range line.Message.Content {
			if c.Type != "text" {
				continue
			}
			lower := strings.ToLower(c.Text)
			for _, kw := range denialKeywords {
				if strings.Contains(lower, kw) {
					return true, assistantCount
				}
			}
		}
	}
	return false, assistantCount
}

// structuredDenialHit scans stream-json stdout line-by-line for a
// top-level `result` envelope carrying either a non-empty
// permission_denials or is_error=true. Returns the number of lines
// scanned alongside the hit. Non-JSON and non-result lines are skipped
// silently. The detector accepts is_error as a fallback alongside the
// canonical permission_denials channel so a future claude release that
// routes denial through is_error + a subtype change still satisfies
// the contract.
func structuredDenialHit(stdout []byte) (bool, int) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	lines := 0
	for scanner.Scan() {
		lines++
		var env struct {
			Type              string            `json:"type"`
			IsError           bool              `json:"is_error"`
			PermissionDenials []json.RawMessage `json:"permission_denials"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue
		}
		if env.Type != "result" {
			continue
		}
		if len(env.PermissionDenials) > 0 || env.IsError {
			return true, lines
		}
	}
	return false, lines
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
