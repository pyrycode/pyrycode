//go:build e2e_realclaude

package realclaude

// TestRealClaude_DoctorPoisoningRegression guards the contract that the
// per-spawn settings JSON pyry writes is one claude accepts at startup. A
// regression here means claude rejected the JSON, prepopulated its `/doctor`
// repair template into the user input buffer, and processed THAT instead of
// pyry's prompt — the #487 failure mode.
//
// Detector: the first `user` JSONL entry's content does NOT contain the
// verbatim opening of the `/doctor` template. A defence-in-depth assertion
// requires at least one `assistant` event in the JSONL; absence under a
// non-poisoned session indicates a different upstream failure mode this
// test is not designed to diagnose.
//
// Complements internal/agentrun/settings/settings_test.go — that pins the
// byte shape produced by WriteSettings; this pins that the resulting bytes
// survive contact with the real claude binary.

import (
	"encoding/json"
	"strings"
	"testing"
)

// doctorTemplateOpening is the verbatim opening of claude's `/doctor` repair
// template, observed in the #487 reproduction. If this substring appears in
// the first user entry, claude rejected the settings JSON at startup.
const doctorTemplateOpening = "Help me fix the issues reported by /doctor below."

func TestRealClaude_DoctorPoisoningRegression(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "Reply with the single word: pong",
		SystemPrompt: "You are a regression-guard test agent. Reply tersely.",
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

	var firstUserContent string
	var firstUserFound bool
	assistantCount := 0
	for _, e := range events {
		if e.Kind == "assistant" {
			assistantCount++
		}
		if e.Kind != "user" || firstUserFound {
			continue
		}
		content, ok := decodeUserContent(e.Raw)
		if !ok {
			// Mirror the bashInvokedInRaw policy at
			// allowed_tools_enforcement_test.go:74 — a malformed line
			// must not turn a PASS into an inconclusive. Skip silently.
			continue
		}
		firstUserContent = content
		firstUserFound = true
	}

	if firstUserFound && strings.Contains(firstUserContent, doctorTemplateOpening) {
		truncated := firstUserContent
		if len(truncated) > 512 {
			truncated = truncated[:512] + "... (truncated)"
		}
		t.Fatalf("first user JSONL entry contains /doctor repair template — "+
			"claude is rejecting the per-spawn settings JSON at startup — see #487.\n"+
			"path: %s\nuser content: %s",
			jsonlPath, truncated)
	}

	if assistantCount == 0 {
		t.Fatalf("no assistant events observed in JSONL — session did not produce a model reply; "+
			"this is not the #487 /doctor-poisoning signature, but indicates a different "+
			"upstream failure that masks the regression-guard's signal.\npath: %s", jsonlPath)
	}
}

// decodeUserContent extracts the user-entry content as a single string,
// coercing either of the two observed claude shapes:
//
//	"content": "string literal"
//	"content": [ {"type":"text", "text":"..."}, ... ]
//
// Returns (text, true) on success; (zero, false) when the line cannot be
// decoded or carries neither shape. Mirrors the silent-skip policy used by
// bashInvokedInRaw — one malformed line must not turn a PASS into an
// inconclusive.
func decodeUserContent(raw json.RawMessage) (string, bool) {
	var line struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		return "", false
	}
	if len(line.Message.Content) == 0 {
		return "", false
	}

	var asString string
	if err := json.Unmarshal(line.Message.Content, &asString); err == nil {
		return asString, true
	}

	var asList []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(line.Message.Content, &asList); err == nil {
		var sb strings.Builder
		for _, block := range asList {
			if block.Type == "text" && block.Text != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(block.Text)
			}
		}
		if sb.Len() == 0 {
			return "", false
		}
		return sb.String(), true
	}

	return "", false
}
