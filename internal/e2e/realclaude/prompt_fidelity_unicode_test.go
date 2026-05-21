//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"strings"
	"testing"
)

// unicodePrompt is a distinctive ASCII-framed payload containing non-ASCII
// UTF-8 sequences of varying widths:
//   - Finnish ä, ö, å (2-byte UTF-8)
//   - em-dash — (3-byte UTF-8)
//   - 🐍 (4-byte UTF-8, outside the BMP — the byte-slicing canary)
//
// Written as a literal double-quoted Go string (source file is UTF-8); not
// escaped via \uXXXX so we assert the bytes we typed survive end-to-end.
// The ASCII frame doubles as a sanity bound: if the prompt is dropped
// entirely the frame match also fails, distinguishing "prompt missing"
// from "prompt corrupted on a UTF-8 boundary".
const unicodePrompt = "INTEGRATION_TEST_PROMPT_UNICODEFIDELITY_K3M9P2X7 ä ö å — 🐍 INTEGRATION_TEST_PROMPT_UNICODEFIDELITY_END_R5T8V1Z4"

// TestRealClaude_PromptFidelity_Unicode is a regression guard for UTF-8 byte
// preservation through the pyry agent-run → streamrunner → JSONL path. A
// regression in JSON escape handling, re-encoding, or buffer slicing on a
// multi-byte UTF-8 boundary would land silently for Finnish-speaking users;
// this test asserts byte-identical preservation of a framed non-ASCII
// payload into the first "user" entry of claude's JSONL session file.
func TestRealClaude_PromptFidelity_Unicode(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       unicodePrompt,
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
			if !bytes.Contains(e.Raw, []byte(unicodePrompt)) {
				t.Fatalf("first user entry does not contain prompt literal %q\npath: %s\nraw: %s",
					unicodePrompt, jsonlPath, string(e.Raw))
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
