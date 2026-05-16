//go:build e2e_realclaude

package realclaude

// Positive-path counterpart to allowed_tools_enforcement_test.go. That
// test verifies Bash is *blocked* under --allowed-tools=Read; this one
// verifies that under --allowed-tools=Bash the model drives the full
// internal tool loop (assistant tool_use → user tool_result → final
// assistant text → end_turn) to completion. A regression here means
// claude's stream-json contract has shifted in a way that silently
// breaks pyry's tool-loop assumptions.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRealClaude_ToolLoopIntegrity asserts the full multi-turn tool
// loop is observable both on the on-disk JSONL (tool_use/tool_result
// correlation, subsequent assistant text) and on pyry's stdout result
// trailer (subtype/stop_reason/num_turns).
func TestRealClaude_ToolLoopIntegrity(t *testing.T) {
	workdir := WithWorktree(t)

	for _, seed := range []struct {
		name    string
		payload string
	}{
		{"hello.txt", "hello\n"},
		{"world.txt", "world\n"},
	} {
		if err := os.WriteFile(filepath.Join(workdir, seed.name), []byte(seed.payload), 0o600); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
	}

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "Use the Bash tool to run \"ls -1\" in the current directory, then tell me how many .txt files you see.",
		SystemPrompt: "You are an e2e regression-guard test. When asked to inspect the filesystem, use the Bash tool.",
		AllowedTools: []string{"Bash"},
		MaxTurns:     3,
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

	var bashToolUseID string
	var sawToolResult bool
	var sawFinalText bool

	for _, e := range events {
		switch e.Kind {
		case "assistant":
			blocks, err := parseContentBlocks(e.Raw)
			if err != nil {
				// Mirrors bashInvokedInRaw / selfcheck.go:283 — a single
				// malformed line must not turn a PASS into an inconclusive.
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
	if !sawFinalText {
		t.Fatalf("no subsequent assistant text block after tool_result; path: %s", jsonlPath)
	}

	trailer, err := parseResultTrailer(result.Stdout)
	if err != nil {
		stdout := result.Stdout
		suffix := ""
		if len(stdout) > 1024 {
			stdout = stdout[:1024]
			suffix = "... (truncated)"
		}
		t.Fatalf("parseResultTrailer: %v\nstdout:\n%s%s", err, stdout, suffix)
	}
	if trailer.Subtype != "success" {
		t.Fatalf("trailer.Subtype = %q, want %q", trailer.Subtype, "success")
	}
	if trailer.StopReason != "end_turn" {
		t.Fatalf("trailer.StopReason = %q, want %q", trailer.StopReason, "end_turn")
	}
	if trailer.NumTurns < 2 {
		t.Fatalf("trailer.NumTurns = %d, want >= 2", trailer.NumTurns)
	}
	if trailer.PermissionDenials != nil && len(*trailer.PermissionDenials) != 0 {
		t.Fatalf("trailer.PermissionDenials = %d entries, want 0", len(*trailer.PermissionDenials))
	}
}

// contentBlock names only the fields under assertion across assistant
// and user content blocks. JSON tags carry omitempty so the same struct
// decodes both tool_use, tool_result, and text blocks.
//
// Note: `tool_result` blocks carry their body in `content` (not `text`),
// which is either a string or an array of nested content blocks per
// claude's stream-json contract. RawMessage holds either shape so
// callers can check non-emptiness without binding to one form.
type contentBlock struct {
	Type      string          `json:"type"`
	Name      string          `json:"name,omitempty"`
	ID        string          `json:"id,omitempty"`
	Text      string          `json:"text,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// parseContentBlocks decodes the verbatim JSONL line into the
// message.content[] slice. Returns (nil, err) on malformed JSON;
// callers treat parse errors as a per-line skip.
func parseContentBlocks(raw json.RawMessage) ([]contentBlock, error) {
	var envelope struct {
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	return envelope.Message.Content, nil
}

// resultTrailer mirrors the subset of streamjson/emitter.go's `trailer`
// struct under assertion. PermissionDenials is pointer-typed so callers
// can distinguish "field absent" (nil) from "field present, empty"
// (non-nil, len 0). Today pyry's emitter never emits this field; the
// pointer is a forward-compat guard per the AC.
//
// IsError, TerminalReason, and Usage were added by #385 for the
// budget-guardrail tests (cache-hit + max-turns). They are omitempty-tagged
// so pre-#385 consumers that don't read them decode unchanged.
type resultTrailer struct {
	Type              string             `json:"type"`
	Subtype           string             `json:"subtype"`
	StopReason        string             `json:"stop_reason"`
	NumTurns          int                `json:"num_turns"`
	PermissionDenials *[]json.RawMessage `json:"permission_denials,omitempty"`
	IsError           bool               `json:"is_error,omitempty"`
	TerminalReason    string             `json:"terminal_reason,omitempty"`
	Usage             resultTrailerUsage `json:"usage,omitempty"`
}

type resultTrailerUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// parseResultTrailer scans stdout line by line for the first
// {"type":"result",...} envelope. If a future change pushes a stream-
// json line past bufio.Scanner's 64 KiB default, extend with
// scanner.Buffer(make([]byte, 1<<20), 1<<20).
func parseResultTrailer(stdout []byte) (*resultTrailer, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	for scanner.Scan() {
		var tr resultTrailer
		if err := json.Unmarshal(scanner.Bytes(), &tr); err != nil {
			continue
		}
		if tr.Type == "result" {
			return &tr, nil
		}
	}
	return nil, errors.New("no type:result line in stdout")
}
