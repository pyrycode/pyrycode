//go:build e2e_realclaude

package realclaude

// Large-tool-output regression sensor (#423). Orthogonal to #421's long-
// session test: that test fires when many short lines accumulate; this one
// fires when a single line on pyry's stream-json stdout exceeds 64 KiB.
// Today only permission_protocol_spike_test.go:133 extends a bufio.Scanner
// past the stdlib default; if that buffer extension is ever dropped — or if
// a similarly truncating scanner is wired into pyry's stream-json
// forwarding path — large tool output gets silently corrupted. This test
// drives a real claude session through a single Bash invocation producing
// ~80 KiB of stdout in one tool_result content block and pins:
//
//   - the result trailer reports subtype="success" and stop_reason="end_turn",
//   - the on-disk JSONL contains a tool_result content block whose raw
//     bytes exceed 70 KiB (headroom under the ~80 KiB target),
//   - pyry's stdout-forwarded tool_result content block has byte-equal
//     length to the on-disk content block (exact match — the emitter
//     re-emits ev.Raw verbatim per internal/agentrun/streamjson/emitter.go:149-150,
//     so any non-zero delta means a scanner truncated the forwarding path),
//   - the captured stderr does NOT contain bufio.Scanner's "token too long"
//     error text.
//
// Cost: ~$0.02 per `make e2e-realclaude` run on claude-haiku-4-5 / Effort:
// low (large tool_result inflates token count). Bounded.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

// largeToolOutputSystemPrompt steers the model toward a single literal
// Bash invocation. The "exactly as given" wording is load-bearing — if
// the model paraphrases the printf/tr pipeline it may emit fewer bytes
// and the 70 KiB threshold won't be reached.
const largeToolOutputSystemPrompt = "You are an e2e regression-guard test. " +
	"When asked to run a shell command, use the Bash tool exactly once. " +
	"Do not chain commands with && or ;. Do not modify or paraphrase the " +
	"command — run it exactly as given."

// largeToolOutputUserPrompt asks the model to run a deterministic command
// that emits 80,000 literal 'A' characters on one line. The command is
// chosen for determinism (no /dev/urandom noise that would churn fixtures
// if anyone later snapshots) and for clearing the 64 KiB stdlib bufio.Scanner
// cap by ~16 KiB while staying well under the 1 MiB extended cap.
const largeToolOutputUserPrompt = "Use the Bash tool exactly once to run this exact command and report when it completes:\n\n" +
	"printf '%80000s' '' | tr ' ' 'A'\n\n" +
	"Do not invoke any other tool. After the command completes, reply with one short sentence."

// TestRealClaude_LargeToolOutput_ExceedsDefaultScannerBuffer is the
// regression sensor described in #423. It is the only realclaude test that
// drives a tool_result content block past the 64 KiB stdlib bufio.Scanner
// default; if pyry's stream-json stdout forwarding path ever silently
// truncates a large line, this test fails.
func TestRealClaude_LargeToolOutput_ExceedsDefaultScannerBuffer(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       largeToolOutputUserPrompt,
		SystemPrompt: largeToolOutputSystemPrompt,
		AllowedTools: []string{"Bash"},
		MaxTurns:     2,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0\nstderr:\n%s", result.ExitCode, truncate(result.Stderr))
	}
	if result.SessionID == "" {
		t.Fatalf("SessionID is empty: no system/init envelope found in stdout\nstdout:\n%s", truncate(result.Stdout))
	}

	// Scan result.Stdout with a 1 MiB-capped scanner. The default 64 KiB
	// bufio.Scanner used by parseResultTrailer (tool_loop_test.go:211)
	// would silently exit at the ~80 KiB tool_result line earlier in the
	// stream, returning "no type:result line in stdout" as a false negative
	// that would mask the very regression this test exists to catch.
	// Mirrors the canonical extension pattern at
	// permission_protocol_spike_test.go:133.
	stdoutLines, err := scanLargeStdoutLines(result.Stdout)
	if err != nil {
		t.Fatalf("scanLargeStdoutLines: %v\nstdout:\n%s", err, truncate(result.Stdout))
	}

	trailer, err := findResultTrailer(stdoutLines)
	if err != nil {
		t.Fatalf("findResultTrailer: %v\nstdout:\n%s", err, truncate(result.Stdout))
	}
	if trailer.Subtype != "success" {
		t.Fatalf("trailer.Subtype = %q, want %q\nstdout:\n%s",
			trailer.Subtype, "success", truncate(result.Stdout))
	}
	if trailer.StopReason != "end_turn" {
		t.Fatalf("trailer.StopReason = %q, want %q\nstdout:\n%s",
			trailer.StopReason, "end_turn", truncate(result.Stdout))
	}

	events := ReadJSONL(t, workdir, result.SessionID)
	jsonlPath := jsonlPathFor(workdir, result.SessionID)

	bashToolUseID, jsonlContent, jsonlBlockIdx := findBashToolResultBlock(events)
	if bashToolUseID == "" {
		t.Fatalf("no Bash tool_use observed in JSONL assistant entries (model may have refused to "+
			"run the literal command; sharpen the system-prompt's \"exactly as given\" wording or "+
			"bump MaxTurns to 3)\npath: %s\nstdout:\n%s",
			jsonlPath, truncate(result.Stdout))
	}
	if jsonlContent == nil {
		t.Fatalf("Bash tool_use %s present but no matching tool_result content block in JSONL\n"+
			"path: %s\nstdout:\n%s",
			bashToolUseID, jsonlPath, truncate(result.Stdout))
	}
	if len(jsonlContent) <= 70_000 {
		t.Fatalf("JSONL tool_result content byte length = %d, want > 70000 (target ~80 KiB; model "+
			"may have paraphrased the printf/tr pipeline — inspect the JSONL for the actual command "+
			"and stdout it ran)\npath: %s\nblock idx: %d",
			len(jsonlContent), jsonlPath, jsonlBlockIdx)
	}

	stdoutContent := findToolResultContentByID(stdoutLines, bashToolUseID)
	if stdoutContent == nil {
		t.Fatalf("Bash tool_use %s present in JSONL but no matching tool_result block on stdout — "+
			"the watcher dropped the event entirely\npath: %s\nstdout:\n%s",
			bashToolUseID, jsonlPath, truncate(result.Stdout))
	}

	// Exact byte-length match. The emitter writes ev.Raw + '\n' verbatim
	// (internal/agentrun/streamjson/emitter.go:149-150), so the disk-side
	// and stdout-side bytes of any envelope are byte-identical. Any
	// non-zero delta means a scanner / truncator was wired into the
	// forwarding path — this is the regression sensor for #423.
	if len(stdoutContent) != len(jsonlContent) {
		t.Fatalf("tool_result content length mismatch: stdout=%d bytes, jsonl=%d bytes — this is the "+
			"regression sensor for #423; a scanner in pyry's stdout forwarding path has truncated "+
			"the tool_result content block (the emitter is contracted to re-emit ev.Raw verbatim, see "+
			"internal/agentrun/streamjson/emitter.go:149-150)\npath: %s\nblock idx: %d",
			len(stdoutContent), len(jsonlContent), jsonlPath, jsonlBlockIdx)
	}

	// Forward-defensive: the literal stdlib error text bufio.Scanner emits
	// when a single token exceeds its buffer. Mirrors long_session_test.go:135.
	if bytes.Contains(result.Stderr, []byte("bufio.Scanner: token too long")) {
		t.Fatalf("stderr contains \"bufio.Scanner: token too long\" — a Scanner in pyry's stdout/stderr "+
			"path hit a long claude line; bump its buffer to 1 MiB (see tool_loop_test.go:210 note)\n"+
			"stderr:\n%s", truncate(result.Stderr))
	}
}

// scanLargeStdoutLines scans stdout with a 1 MiB-capped bufio.Scanner.
// The default 64 KiB cap is below the ~80 KiB tool_result line this test
// expects, so the default scanner would silently exit at the long line.
// The 1 MiB cap mirrors permission_protocol_spike_test.go:133. Each
// returned line is a fresh copy so callers can retain it past subsequent
// reads. scanner.Err() is propagated so a future buffer-exhaustion regression
// (a line above 1 MiB) fails loudly rather than silently truncating.
func scanLargeStdoutLines(stdout []byte) ([]json.RawMessage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []json.RawMessage
	for scanner.Scan() {
		line := make([]byte, len(scanner.Bytes()))
		copy(line, scanner.Bytes())
		lines = append(lines, json.RawMessage(line))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// findResultTrailer walks pre-scanned stdout lines for the first
// {"type":"result",...} envelope. Mirrors parseResultTrailer's logic but
// operates on lines already scanned with a 1 MiB-capped buffer so the
// long tool_result line earlier in the stream doesn't truncate the walk.
func findResultTrailer(lines []json.RawMessage) (*resultTrailer, error) {
	for _, line := range lines {
		var tr resultTrailer
		if err := json.Unmarshal(line, &tr); err != nil {
			continue
		}
		if tr.Type == "result" {
			return &tr, nil
		}
	}
	return nil, errors.New("no type:result line in stdout")
}

// findBashToolResultBlock walks JSONL events for the first Bash tool_use
// in an assistant entry, then for the first user entry carrying a matching
// tool_result. Returns the raw Content bytes (json.RawMessage — may be a
// JSON string or an array of nested content blocks per claude's stream-json
// contract) and the block index within the user envelope's content array.
// Returns (toolUseID, nil, -1) if the tool_use was seen but no matching
// tool_result was found; returns ("", nil, -1) if no Bash tool_use was
// seen at all. Per-line parse errors are skipped silently (matches the
// per-line skip pattern in tool_loop_test.go:75-80).
func findBashToolResultBlock(events []JSONLEntry) (toolUseID string, content json.RawMessage, blockIdx int) {
	var bashID string
	for _, e := range events {
		blocks, err := parseContentBlocks(e.Raw)
		if err != nil {
			continue
		}
		switch e.Kind {
		case "assistant":
			if bashID == "" {
				for _, b := range blocks {
					if b.Type == "tool_use" && b.Name == "Bash" && b.ID != "" {
						bashID = b.ID
						break
					}
				}
			}
		case "user":
			if bashID == "" {
				continue
			}
			for i, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID == bashID {
					return bashID, b.Content, i
				}
			}
		}
	}
	return bashID, nil, -1
}

// findToolResultContentByID walks pre-scanned stdout lines for the first
// tool_result content block whose tool_use_id matches toolUseID. Returns
// nil if no match. Per-line parse errors are skipped silently — user
// envelopes carrying a plain text content (a string, not an array) fail
// parseContentBlocks's array-decode and are correctly ignored.
func findToolResultContentByID(lines []json.RawMessage, toolUseID string) json.RawMessage {
	for _, line := range lines {
		blocks, err := parseContentBlocks(line)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_result" && b.ToolUseID == toolUseID {
				return b.Content
			}
		}
	}
	return nil
}
