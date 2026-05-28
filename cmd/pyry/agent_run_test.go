package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/ptyrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/settings"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// Note: production claude under --output-format stream-json still writes a
// JSONL session file at ~/.claude/projects/<encoded-cwd>/<session-id>.jsonl.
// This fake does NOT produce that file — the verb no longer reads it (the
// JSONL-tail watcher was deleted in #391). Verification of claude's own
// JSONL emission is left to manual smoke against a real `claude` binary;
// see #375 for the self-check refactor that owns that surface.

// TestAgentRunStreamJSONFake is the fake-claude entry point used by the
// runAgentRun wiring tests. When PYRY_AGENT_RUN_FAKE=1 is set in the
// environment, the test binary re-exec'd as claude behaves per
// GO_AGENT_RUN_FAKE_MODE:
//
//   - "clean" (default): drain stdin to EOF, write a canned three-line
//     stream-json sequence to stdout (system init / assistant / result
//     success), exit 0.
//   - "exit1": drain stdin to EOF, exit 1.
//   - "sleep": drain stdin to EOF, install a SIGTERM handler that prints
//     "got SIGTERM" to stderr and exits 0; otherwise sleep 30s. Used by
//     the ctx-cancel path coverage in streamrunner; the verb-level test
//     here defers to streamrunner's own coverage.
//
// Optional captures:
//   - GO_AGENT_RUN_FAKE_ARGS_FILE: write argv[1:] (one per line) if set.
//   - GO_AGENT_RUN_FAKE_STDIN_FILE: write drained stdin bytes verbatim if set.
func TestAgentRunStreamJSONFake(t *testing.T) {
	if os.Getenv("PYRY_AGENT_RUN_FAKE") != "1" {
		return
	}
	if path := os.Getenv("GO_AGENT_RUN_FAKE_ARGS_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "fake: write args: %v\n", err)
			os.Exit(99)
		}
	}

	mode := os.Getenv("GO_AGENT_RUN_FAKE_MODE")
	if mode == "" {
		mode = "clean"
	}

	switch mode {
	case "clean":
		drainStdin(t)
		lines := []string{
			`{"type":"system","subtype":"init","session_id":"00000000-0000-4000-8000-000000000000"}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
			`{"type":"result","subtype":"success"}`,
		}
		for _, l := range lines {
			fmt.Fprintln(os.Stdout, l)
		}
		os.Exit(0)
	case "exit1":
		drainStdin(t)
		os.Exit(1)
	case "sleep":
		// SIGTERM handler is installed but the verb-level test no longer
		// exercises this mode; kept here to mirror streamrunner's helper
		// shape and avoid silently breaking future test additions.
		drainStdin(t)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "fake: unknown GO_AGENT_RUN_FAKE_MODE=%q\n", mode)
		os.Exit(99)
	}
}

func drainStdin(t *testing.T) {
	t.Helper()
	if path := os.Getenv("GO_AGENT_RUN_FAKE_STDIN_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake: open stdin capture: %v\n", err)
			os.Exit(99)
		}
		if _, err := io.Copy(f, os.Stdin); err != nil {
			fmt.Fprintf(os.Stderr, "fake: copy stdin: %v\n", err)
			_ = f.Close()
			os.Exit(99)
		}
		_ = f.Sync()
		_ = f.Close()
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// configureFakeClaude wires the test-only env knobs so a runAgentRun call
// spawns a shell wrapper that exec's the test binary in fake-claude mode.
// Without this, runAgentRun would try to spawn the real `claude` from PATH.
//
// A shell wrapper is required because buildStreamRunnerClaudeArgs emits
// real claude flags (e.g. `--input-format stream-json`) which the Go test
// binary's flag parser would reject. The wrapper drops the production argv
// on the floor and re-execs the test binary with a fixed `-test.run` pinned
// to TestAgentRunStreamJSONFake.
//
// Pins PYRY_USE_STREAMJSON=1 so callers exercise the streamrunner branch.
// The ptyrunner default path is exercised in tests via the package-level
// seams (trustMark / settingsWrite / ptyRun / newSessionID), not the fake.
func configureFakeClaude(t *testing.T) {
	t.Helper()
	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "fake-claude.sh")
	body := fmt.Sprintf(`#!/bin/sh
exec %q -test.run=^TestAgentRunStreamJSONFake$
`, os.Args[0])
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake-claude script: %v", err)
	}
	t.Setenv("PYRY_CLAUDE_BIN", script)
	t.Setenv("PYRY_AGENT_RUN_FAKE", "1")
	t.Setenv("PYRY_USE_STREAMJSON", "1")
}

// validArgsFixture builds a fully-valid argv for parseAgentRunArgs. Tests
// clone the slice and override individual flags to exercise error paths.
type validArgsFixture struct {
	promptFile       string
	systemPromptFile string
	workdir          string
	argv             []string
}

func newValidArgsFixture(t *testing.T) validArgsFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	promptPath := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	sysPath := filepath.Join(dir, "system.txt")
	if err := os.WriteFile(sysPath, []byte("system"), 0o644); err != nil {
		t.Fatalf("write system-prompt file: %v", err)
	}
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	return validArgsFixture{
		promptFile:       promptPath,
		systemPromptFile: sysPath,
		workdir:          workdir,
		argv: []string{
			"--prompt-file", promptPath,
			"--system-prompt-file", sysPath,
			"--allowed-tools", "Read,Bash",
			"--max-turns", "3",
			"--effort", "medium",
			"--model", "sonnet-4-6",
			"--workdir", workdir,
			"--output-format", "stream-json",
		},
	}
}

// argvWithout returns the fixture's argv with both the flag and its
// value removed, simulating an omitted required flag.
func (f validArgsFixture) argvWithout(flagName string) []string {
	out := make([]string, 0, len(f.argv))
	for i := 0; i < len(f.argv); i++ {
		if f.argv[i] == flagName {
			i++ // skip the value
			continue
		}
		out = append(out, f.argv[i])
	}
	return out
}

// argvReplacing returns the fixture's argv with the value for flagName
// replaced by newValue. Both flag and value must already be present.
func (f validArgsFixture) argvReplacing(flagName, newValue string) []string {
	out := slices.Clone(f.argv)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == flagName {
			out[i+1] = newValue
			return out
		}
	}
	return out
}

func TestParseAgentRunArgs_HappyPath(t *testing.T) {
	fx := newValidArgsFixture(t)
	got, err := parseAgentRunArgs(fx.argv)
	if err != nil {
		t.Fatalf("parseAgentRunArgs: unexpected error: %v", err)
	}
	if got.promptFile != fx.promptFile {
		t.Errorf("promptFile = %q, want %q", got.promptFile, fx.promptFile)
	}
	if got.systemPromptFile != fx.systemPromptFile {
		t.Errorf("systemPromptFile = %q, want %q", got.systemPromptFile, fx.systemPromptFile)
	}
	wantTools := []string{"Read", "Bash"}
	if !slices.Equal(got.allowedTools, wantTools) {
		t.Errorf("allowedTools = %v, want %v", got.allowedTools, wantTools)
	}
	if got.maxTurns != 3 {
		t.Errorf("maxTurns = %d, want 3", got.maxTurns)
	}
	if got.effort != "medium" {
		t.Errorf("effort = %q, want %q", got.effort, "medium")
	}
	if got.model != "sonnet-4-6" {
		t.Errorf("model = %q, want %q", got.model, "sonnet-4-6")
	}
	if got.workdir != fx.workdir {
		t.Errorf("workdir = %q, want %q", got.workdir, fx.workdir)
	}
	if got.outputFormat != "stream-json" {
		t.Errorf("outputFormat = %q, want %q", got.outputFormat, "stream-json")
	}
}

func TestParseAgentRunArgs_Errors(t *testing.T) {
	fx := newValidArgsFixture(t)
	missingDir := filepath.Join(fx.workdir, "does-not-exist")
	// A regular file used where --workdir expects a directory.
	plainFile := fx.promptFile
	// A directory used where --prompt-file expects a regular file.
	asDir := fx.workdir

	tests := []struct {
		name      string
		argv      []string
		wantInErr string
	}{
		{
			name:      "prompt-file missing",
			argv:      fx.argvWithout("--prompt-file"),
			wantInErr: "--prompt-file",
		},
		{
			name:      "prompt-file not found",
			argv:      fx.argvReplacing("--prompt-file", missingDir),
			wantInErr: "--prompt-file",
		},
		{
			name:      "prompt-file is a directory",
			argv:      fx.argvReplacing("--prompt-file", asDir),
			wantInErr: "--prompt-file",
		},
		{
			name:      "system-prompt-file missing",
			argv:      fx.argvWithout("--system-prompt-file"),
			wantInErr: "--system-prompt-file",
		},
		{
			name:      "system-prompt-file not found",
			argv:      fx.argvReplacing("--system-prompt-file", missingDir),
			wantInErr: "--system-prompt-file",
		},
		{
			name:      "allowed-tools missing",
			argv:      fx.argvWithout("--allowed-tools"),
			wantInErr: "--allowed-tools",
		},
		{
			name:      "allowed-tools empty after split",
			argv:      fx.argvReplacing("--allowed-tools", ", ,,"),
			wantInErr: "--allowed-tools",
		},
		{
			name:      "max-turns missing",
			argv:      fx.argvWithout("--max-turns"),
			wantInErr: "--max-turns",
		},
		{
			name:      "max-turns zero",
			argv:      fx.argvReplacing("--max-turns", "0"),
			wantInErr: "--max-turns",
		},
		{
			name:      "max-turns negative",
			argv:      fx.argvReplacing("--max-turns", "-1"),
			wantInErr: "--max-turns",
		},
		{
			name:      "effort missing",
			argv:      fx.argvWithout("--effort"),
			wantInErr: "--effort",
		},
		{
			name:      "effort bad value",
			argv:      fx.argvReplacing("--effort", "wat"),
			wantInErr: "--effort",
		},
		{
			name:      "model missing",
			argv:      fx.argvWithout("--model"),
			wantInErr: "--model",
		},
		{
			name:      "workdir missing",
			argv:      fx.argvWithout("--workdir"),
			wantInErr: "--workdir",
		},
		{
			name:      "workdir not found",
			argv:      fx.argvReplacing("--workdir", missingDir),
			wantInErr: "--workdir",
		},
		{
			name:      "workdir is a file",
			argv:      fx.argvReplacing("--workdir", plainFile),
			wantInErr: "--workdir",
		},
		{
			name:      "output-format missing",
			argv:      fx.argvWithout("--output-format"),
			wantInErr: "--output-format",
		},
		{
			name:      "output-format wrong value",
			argv:      fx.argvReplacing("--output-format", "json"),
			wantInErr: "--output-format",
		},
		{
			name:      "unexpected positional",
			argv:      append(slices.Clone(fx.argv), "leftover"),
			wantInErr: "unexpected positional",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAgentRunArgs(tc.argv)
			if err == nil {
				t.Fatalf("parseAgentRunArgs(%v) = nil error, want error containing %q", tc.argv, tc.wantInErr)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantInErr)
			}
		})
	}
}

func TestParseAgentRunArgs_EffortValidValues(t *testing.T) {
	fx := newValidArgsFixture(t)
	for _, v := range []string{"low", "medium", "high", "xhigh", "max"} {
		t.Run(v, func(t *testing.T) {
			got, err := parseAgentRunArgs(fx.argvReplacing("--effort", v))
			if err != nil {
				t.Fatalf("parseAgentRunArgs(--effort %s): unexpected error: %v", v, err)
			}
			if got.effort != v {
				t.Errorf("effort = %q, want %q", got.effort, v)
			}
		})
	}
}

func TestParseAgentRunArgs_AllowedToolsForms(t *testing.T) {
	fx := newValidArgsFixture(t)
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"comma", "Read,Bash", []string{"Read", "Bash"}},
		{"space", "Read Bash", []string{"Read", "Bash"}},
		{"mixed", "Read, Bash , Edit", []string{"Read", "Bash", "Edit"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAgentRunArgs(fx.argvReplacing("--allowed-tools", tc.raw))
			if err != nil {
				t.Fatalf("parseAgentRunArgs(--allowed-tools %q): unexpected error: %v", tc.raw, err)
			}
			if !slices.Equal(got.allowedTools, tc.want) {
				t.Errorf("allowedTools = %v, want %v", got.allowedTools, tc.want)
			}
		})
	}
}

// TestBuildStreamRunnerClaudeArgs_Shape pins the legacy claude argv under
// the stream-json subprocess pipeline (#391, renamed in #470 to reflect
// streamrunner-specific scope). The security invariants the old
// PTY/settings argv pinned (`--permission-mode dontAsk` MUST appear,
// `--allowedTools` MUST NOT appear) are inverted here: `--allowed-tools`
// IS the authoritative tool gate now, and `--dangerously-skip-permissions`
// replaces the settings file's deny-default + workspace-trust mark.
func TestBuildStreamRunnerClaudeArgs_Shape(t *testing.T) {
	tests := []struct {
		name   string
		parsed agentRunArgs
		want   []string
	}{
		{
			name: "canonical happy path",
			parsed: agentRunArgs{
				model:            "sonnet-4-6",
				systemPromptFile: "/tmp/sys.md",
				effort:           "medium",
				maxTurns:         3,
				allowedTools:     []string{"Read", "Bash"},
			},
			want: []string{
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--verbose",
				"--dangerously-skip-permissions",
				"--append-system-prompt-file", "/tmp/sys.md",
				"--model", "sonnet-4-6",
				"--effort", "medium",
				"--max-turns", "3",
				"--allowed-tools", "Read,Bash",
			},
		},
		{
			name: "max effort, single tool, larger turn budget",
			parsed: agentRunArgs{
				model:            "opus-4-7",
				systemPromptFile: "/tmp/x.md",
				effort:           "max",
				maxTurns:         12,
				allowedTools:     []string{"Read"},
			},
			want: []string{
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--verbose",
				"--dangerously-skip-permissions",
				"--append-system-prompt-file", "/tmp/x.md",
				"--model", "opus-4-7",
				"--effort", "max",
				"--max-turns", "12",
				"--allowed-tools", "Read",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildStreamRunnerClaudeArgs(tc.parsed)
			if !slices.Equal(got, tc.want) {
				t.Errorf("buildStreamRunnerClaudeArgs:\n got  = %v\n want = %v", got, tc.want)
			}

			// Named structural assertions so a re-ordering that happens to
			// preserve slices.Equal against a stale `want` still trips a
			// clear-named guard.
			if !slices.Contains(got, "--dangerously-skip-permissions") {
				t.Errorf("missing --dangerously-skip-permissions in %v", got)
			}
			if !nextValueEquals(got, "--input-format", "stream-json") {
				t.Errorf("missing `--input-format stream-json` in %v", got)
			}
			if !nextValueEquals(got, "--output-format", "stream-json") {
				t.Errorf("missing `--output-format stream-json` in %v", got)
			}
			if !slices.Contains(got, "--verbose") {
				t.Errorf("missing --verbose in %v", got)
			}
			wantTools := strings.Join(tc.parsed.allowedTools, ",")
			if !nextValueEquals(got, "--allowed-tools", wantTools) {
				t.Errorf("missing `--allowed-tools %s` in %v", wantTools, got)
			}
			wantMaxTurns := strconv.Itoa(tc.parsed.maxTurns)
			if !nextValueEquals(got, "--max-turns", wantMaxTurns) {
				t.Errorf("missing `--max-turns %s` in %v", wantMaxTurns, got)
			}

			// Negative pins: the load-bearing PTY/settings-mode flags must
			// never reappear under the stream-json pipeline.
			for _, banned := range []string{"--settings", "--permission-mode", "--session-id"} {
				if slices.Contains(got, banned) {
					t.Errorf("banned flag %q present in %v", banned, got)
				}
			}
		})
	}
}

func nextValueEquals(argv []string, flag, want string) bool {
	idx := slices.Index(argv, flag)
	if idx < 0 || idx+1 >= len(argv) {
		return false
	}
	return argv[idx+1] == want
}

// TestAgentRunUsageDescription locks the `pyry agent-run --help` body so a
// future stale-disclaimer regression fails CI (#359). The constant is the
// sole source of the description prose printed between the `Usage:` header
// and `fs.PrintDefaults()` by the `fs.Usage` callback, so asserting on it
// directly covers the same surface `--help` renders without needing to
// invoke the flag parser or redirect stderr.
func TestAgentRunUsageDescription(t *testing.T) {
	if strings.Contains(agentRunUsageDescription, "scaffold only") {
		t.Errorf("agentRunUsageDescription still contains stale %q disclaimer:\n%s",
			"scaffold only", agentRunUsageDescription)
	}
	// Load-bearing substrings: the prose cannot describe the current
	// runtime behaviour without `stream-json` (the wire shape both modes
	// emit), `--max-turns` / `--allowed-tools` (the two behavioural
	// anchors), `PYRY_USE_STREAMJSON` (the operator-facing rollback knob
	// added by #470), and `PTY` (the new-default disclosure).
	for _, want := range []string{"stream-json", "--max-turns", "--allowed-tools", "PYRY_USE_STREAMJSON", "PTY"} {
		if !strings.Contains(agentRunUsageDescription, want) {
			t.Errorf("agentRunUsageDescription missing required substring %q:\n%s",
				want, agentRunUsageDescription)
		}
	}
}

// TestRunAgentRun_StreamJSON_Clean verifies the end-to-end wiring: runAgentRun
// reads the prompt file, builds the argv, spawns the (faked) claude via
// streamrunner, and forwards claude's stdout verbatim. Asserts:
//
//   (a) the stream-json events (system init / assistant / result success)
//       appear on stdout in order;
//   (b) stdout does NOT start with a "settings-file: " marker line (the
//       marker was removed in #391; claude's `system init` event takes over);
//   (c) the verb does not write the per-spawn settings file or the
//       workspace-trust mark in ~/.claude.json;
//   (d) the prompt file's "hello" content is delivered as a stream-json
//       user-turn envelope on claude's stdin.
func TestRunAgentRun_StreamJSON_Clean(t *testing.T) {
	fx := newValidArgsFixture(t)
	configureFakeClaude(t)

	stdinCapture := filepath.Join(t.TempDir(), "stdin.json")
	t.Setenv("GO_AGENT_RUN_FAKE_STDIN_FILE", stdinCapture)

	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runAgentRun(&stdout, fx.argv) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentRun: %v\nstdout=%q", err, stdout.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("runAgentRun did not return within 10s\nstdout so far=%q", stdout.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("stdout has %d lines, want >=3 (system + assistant + result): %q", len(lines), stdout.String())
	}
	if strings.HasPrefix(lines[0], "settings-file: ") {
		t.Errorf("stdout[0] is a settings-file marker (removed in #391): %q", lines[0])
	}

	// Walk the lines, decoding each and asserting the type/subtype shape
	// in the order the fake emitted them.
	var (
		sawSystemInit bool
		sawAssistant  bool
		sawResultIdx  = -1
	)
	for i, line := range lines {
		if line == "" {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("stdout line %d not valid JSON: %v\nline=%q", i, err, line)
		}
		switch {
		case ev.Type == "system" && ev.Subtype == "init":
			sawSystemInit = true
		case ev.Type == "assistant":
			if !sawSystemInit {
				t.Errorf("assistant event appeared before system init (line %d)", i)
			}
			sawAssistant = true
		case ev.Type == "result" && ev.Subtype == "success":
			if !sawAssistant {
				t.Errorf("result event appeared before assistant (line %d)", i)
			}
			sawResultIdx = i
		}
	}
	if !sawSystemInit {
		t.Errorf("missing system init event in stdout: %q", stdout.String())
	}
	if !sawAssistant {
		t.Errorf("missing assistant event in stdout: %q", stdout.String())
	}
	if sawResultIdx < 0 {
		t.Errorf("missing result success event in stdout: %q", stdout.String())
	}

	// (c) negative filesystem assertions: the settings file and the
	// workspace-trust mark in ~/.claude.json must NOT be written by the verb.
	settingsPath := filepath.Join(fx.workdir, ".pyry-agent-run-settings.json")
	if _, err := os.Stat(settingsPath); err == nil {
		t.Errorf("verb wrote per-spawn settings file at %s; should be gone in #391", settingsPath)
	} else if !os.IsNotExist(err) {
		t.Errorf("stat settings file: %v", err)
	}
	trustPath := filepath.Join(os.Getenv("HOME"), ".claude.json")
	if _, err := os.Stat(trustPath); err == nil {
		t.Errorf("verb wrote workspace-trust mark at %s; should be gone in #391", trustPath)
	} else if !os.IsNotExist(err) {
		t.Errorf("stat trust file: %v", err)
	}

	// (d) stdin envelope round-trip: the prompt file's "hello" content was
	// JSON-wrapped and delivered. This confirms the verb is using
	// streamrunner.Run rather than a path that bypasses the envelope.
	captured, err := os.ReadFile(stdinCapture)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	var env struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimRight(captured, "\n"), &env); err != nil {
		t.Fatalf("unmarshal stdin envelope: %v\nraw=%q", err, captured)
	}
	if env.Type != "user" || env.Message.Role != "user" {
		t.Errorf("envelope shape: type=%q role=%q, want user/user", env.Type, env.Message.Role)
	}
	if len(env.Message.Content) != 1 || env.Message.Content[0].Type != "text" || env.Message.Content[0].Text != "hello" {
		t.Errorf("envelope content: %+v, want one text block with %q", env.Message.Content, "hello")
	}
}

// TestRunAgentRun_StreamJSON_NonZeroExit pins that a non-zero claude exit
// surfaces as an error wrapping *exec.ExitError with the verb's
// "agent-run: " prefix.
func TestRunAgentRun_StreamJSON_NonZeroExit(t *testing.T) {
	fx := newValidArgsFixture(t)
	configureFakeClaude(t)
	t.Setenv("GO_AGENT_RUN_FAKE_MODE", "exit1")

	err := runAgentRun(io.Discard, fx.argv)
	if err == nil {
		t.Fatal("runAgentRun: got nil, want non-nil from exit-1 child")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runAgentRun: err = %v (%T), want chain containing *exec.ExitError", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("ExitCode = %d, want 1", exitErr.ExitCode())
	}
	if !strings.HasPrefix(err.Error(), "agent-run: ") {
		t.Errorf("err message %q lacks `agent-run: ` prefix", err.Error())
	}
}

// Per spec: a verb-level ctx-cancel test was considered and rejected.
// The streamrunner package owns the SIGTERM/SIGKILL grace behaviour
// (see internal/agentrun/streamrunner.TestRun_CtxCancelMidRun); the
// verb's only ctx-cancel logic is one line (`return nil` if
// errors.Is(err, context.Canceled)`). Adding a verb-level cover would
// require either signal-context dependency injection or sending SIGTERM
// to the test process — both heavier than the bug they would prevent.

// installFakeSeams installs no-op success stubs for the four ptyrunner-path
// seams declared in agent_run.go (trustMark / settingsWrite / ptyRun /
// newSessionID) and registers cleanup so each is restored to its production
// value at test exit. Individual tests re-override any seam they need a
// specific behaviour from.
func installFakeSeams(t *testing.T) {
	t.Helper()
	origTrust := trustMark
	origSettings := settingsWrite
	origPty := ptyRun
	origSid := newSessionID
	t.Cleanup(func() {
		trustMark = origTrust
		settingsWrite = origSettings
		ptyRun = origPty
		newSessionID = origSid
	})
	trustMark = func(workdir string) (string, error) {
		return workdir, nil
	}
	settingsWrite = func(tools []string) (string, error) {
		f, err := os.CreateTemp("", "pyry-test-settings-*.json")
		if err != nil {
			return "", err
		}
		_ = f.Close()
		return f.Name(), nil
	}
	ptyRun = func(_ context.Context, _ ptyrunner.Config) error {
		return nil
	}
	newSessionID = sessions.NewID
}

// TestRunAgentRun_DispatchesToPtyRunnerByDefault verifies that with
// PYRY_USE_STREAMJSON unset, runAgentRun dispatches to the ptyrunner
// path and the captured ptyrunner.Config has every required field
// populated. Locks the default-branch wiring against future drift.
func TestRunAgentRun_DispatchesToPtyRunnerByDefault(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")
	t.Setenv("PYRY_CLAUDE_BIN", "/usr/local/bin/claude")

	var calls int
	var captured ptyrunner.Config
	ptyRun = func(_ context.Context, cfg ptyrunner.Config) error {
		calls++
		captured = cfg
		return nil
	}

	if err := runAgentRun(io.Discard, fx.argv); err != nil {
		t.Fatalf("runAgentRun: %v", err)
	}
	if calls != 1 {
		t.Fatalf("ptyRun called %d times, want 1", calls)
	}
	if captured.ClaudeBin == "" || captured.WorkDir == "" || captured.SessionID == "" ||
		captured.SettingsPath == "" || captured.SystemPrompt == "" || captured.Model == "" ||
		captured.Effort == "" || captured.AllowedTools == nil || captured.MaxTurns == 0 ||
		len(captured.PromptBytes) == 0 || captured.Stdout == nil || captured.Stderr == nil {
		t.Fatalf("ptyRun captured Config has unpopulated required fields: %+v", captured)
	}
}

// TestRunAgentRun_EnvSet1DispatchesToStreamRunner pins the rollback knob:
// PYRY_USE_STREAMJSON=1 routes through streamrunner (verified via the
// existing fake-claude clean path). If ptyrunner is reached, t.Fatal trips.
func TestRunAgentRun_EnvSet1DispatchesToStreamRunner(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	configureFakeClaude(t) // pins PYRY_USE_STREAMJSON=1
	ptyRun = func(_ context.Context, _ ptyrunner.Config) error {
		t.Fatal("ptyrunner called on streamrunner branch")
		return nil
	}

	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runAgentRun(&stdout, fx.argv) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentRun: %v\nstdout=%q", err, stdout.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("runAgentRun did not return within 10s\nstdout so far=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"type":"result"`) {
		t.Errorf("stdout missing result event from fake-claude clean path: %q", stdout.String())
	}
}

// TestRunAgentRun_EnvNon1ValueDispatchesToPtyRunner pins the predicate's
// strictness: only the exact string "1" selects the legacy path. Future
// contributors widening the truthy set ("true", "yes", "on", …) trip this.
func TestRunAgentRun_EnvNon1ValueDispatchesToPtyRunner(t *testing.T) {
	for _, val := range []string{"true", "yes", "on", "streamjson", "0", "false", ""} {
		t.Run(fmt.Sprintf("val=%q", val), func(t *testing.T) {
			fx := newValidArgsFixture(t)
			installFakeSeams(t)
			t.Setenv("PYRY_USE_STREAMJSON", val)

			var calls int
			ptyRun = func(_ context.Context, _ ptyrunner.Config) error {
				calls++
				return nil
			}
			if err := runAgentRun(io.Discard, fx.argv); err != nil {
				t.Fatalf("runAgentRun: %v", err)
			}
			if calls != 1 {
				t.Errorf("PYRY_USE_STREAMJSON=%q: ptyRun called %d times, want 1", val, calls)
			}
		})
	}
}

// TestRunAgentRun_PtyPath_TrustFailure_MentionsClaudeJson pins AC #3:
// a trust-write failure surfaces with `~/.claude.json` in the message
// (operator-actionable path is the load-bearing diagnostic).
func TestRunAgentRun_PtyPath_TrustFailure_MentionsClaudeJson(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")

	trustMark = func(_ string) (string, error) {
		return "", errors.New("simulated trust failure")
	}
	ptyRun = func(_ context.Context, _ ptyrunner.Config) error {
		t.Fatal("ptyrunner called after trust failure")
		return nil
	}

	err := runAgentRun(io.Discard, fx.argv)
	if err == nil {
		t.Fatal("runAgentRun: got nil, want error from trust failure")
	}
	got := err.Error()
	if !strings.Contains(got, "~/.claude.json") {
		t.Errorf("error %q missing `~/.claude.json`", got)
	}
	if !strings.HasPrefix(got, "agent-run: ") {
		t.Errorf("error %q missing `agent-run: ` prefix", got)
	}
}

// TestRunAgentRun_PtyPath_SettingsFailure_NamesSettingsStep pins AC #3:
// a settings-write failure surfaces with `settings` in the message.
func TestRunAgentRun_PtyPath_SettingsFailure_NamesSettingsStep(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")

	settingsWrite = func(_ []string) (string, error) {
		return "", errors.New("simulated settings failure")
	}
	ptyRun = func(_ context.Context, _ ptyrunner.Config) error {
		t.Fatal("ptyrunner called after settings failure")
		return nil
	}

	err := runAgentRun(io.Discard, fx.argv)
	if err == nil {
		t.Fatal("runAgentRun: got nil, want error from settings failure")
	}
	got := err.Error()
	if !strings.Contains(got, "settings") {
		t.Errorf("error %q missing `settings`", got)
	}
	if !strings.HasPrefix(got, "agent-run: ") {
		t.Errorf("error %q missing `agent-run: ` prefix", got)
	}
}

// TestRunAgentRun_PtyPath_PtyRunError_Wrapped pins AC #3: a ptyrunner.Run
// failure flows through the verb's `agent-run: ` prefix.
func TestRunAgentRun_PtyPath_PtyRunError_Wrapped(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")

	ptyRun = func(_ context.Context, _ ptyrunner.Config) error {
		return errors.New("simulated ptyrunner failure")
	}

	err := runAgentRun(io.Discard, fx.argv)
	if err == nil {
		t.Fatal("runAgentRun: got nil, want error from ptyrunner failure")
	}
	got := err.Error()
	if !strings.HasPrefix(got, "agent-run: ") {
		t.Errorf("error %q missing `agent-run: ` prefix", got)
	}
	if !strings.Contains(got, "simulated ptyrunner failure") {
		t.Errorf("error %q missing underlying ptyrunner message", got)
	}
}

// TestRunAgentRun_PtyPath_SettingsRemovedOnSuccess pins AC #2: the
// per-spawn settings tempfile is removed when the verb returns nil.
// Exercises the production settings.WriteSettings via a capture-wrapped
// seam (no replacement).
func TestRunAgentRun_PtyPath_SettingsRemovedOnSuccess(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")

	var capturedPath string
	settingsWrite = func(tools []string) (string, error) {
		p, err := settings.WriteSettings(tools)
		capturedPath = p
		return p, err
	}
	ptyRun = func(_ context.Context, _ ptyrunner.Config) error {
		// Sanity-check the tempfile exists at this moment (before defer fires).
		if _, err := os.Stat(capturedPath); err != nil {
			t.Errorf("settings file %q absent during ptyRun: %v", capturedPath, err)
		}
		return nil
	}

	if err := runAgentRun(io.Discard, fx.argv); err != nil {
		t.Fatalf("runAgentRun: %v", err)
	}
	if capturedPath == "" {
		t.Fatal("settingsWrite never called")
	}
	if _, err := os.Stat(capturedPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("settings file %q still exists after runAgentRun success: stat err=%v", capturedPath, err)
	}
}

// TestRunAgentRun_PtyPath_SettingsRemovedOnFailure pins AC #2: the
// per-spawn settings tempfile is removed even when ptyrunner fails.
func TestRunAgentRun_PtyPath_SettingsRemovedOnFailure(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")

	var capturedPath string
	settingsWrite = func(tools []string) (string, error) {
		p, err := settings.WriteSettings(tools)
		capturedPath = p
		return p, err
	}
	ptyRun = func(_ context.Context, _ ptyrunner.Config) error {
		return errors.New("boom")
	}

	if err := runAgentRun(io.Discard, fx.argv); err == nil {
		t.Fatal("runAgentRun: got nil, want error from ptyrunner")
	}
	if capturedPath == "" {
		t.Fatal("settingsWrite never called")
	}
	if _, err := os.Stat(capturedPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("settings file %q still exists after runAgentRun failure: stat err=%v", capturedPath, err)
	}
}

// TestRunAgentRun_PtyPath_WorkDirIsTrustResolvedRealpath pins the
// realpath-not-parsed-workdir contract: ptyrunner.Config.WorkDir equals
// the trust function's symlink-resolved return value, not parsed.workdir.
// Keeps claude's projects[<realpath>] key aligned with cmd.Dir.
func TestRunAgentRun_PtyPath_WorkDirIsTrustResolvedRealpath(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")

	const sentinel = "/sentinel/realpath"
	trustMark = func(_ string) (string, error) { return sentinel, nil }
	var captured ptyrunner.Config
	ptyRun = func(_ context.Context, cfg ptyrunner.Config) error {
		captured = cfg
		return nil
	}

	if err := runAgentRun(io.Discard, fx.argv); err != nil {
		t.Fatalf("runAgentRun: %v", err)
	}
	if captured.WorkDir != sentinel {
		t.Errorf("Config.WorkDir = %q, want sentinel realpath %q (must not be parsed.workdir %q)",
			captured.WorkDir, sentinel, fx.workdir)
	}
}

// TestRunAgentRun_PtyPath_SessionIDIsUUIDv4 pins the SessionID shape:
// canonical UUIDv4 per sessions.ValidID. Guards against a future "we'll
// just use a short hash" drift.
func TestRunAgentRun_PtyPath_SessionIDIsUUIDv4(t *testing.T) {
	fx := newValidArgsFixture(t)
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")

	var captured ptyrunner.Config
	ptyRun = func(_ context.Context, cfg ptyrunner.Config) error {
		captured = cfg
		return nil
	}

	if err := runAgentRun(io.Discard, fx.argv); err != nil {
		t.Fatalf("runAgentRun: %v", err)
	}
	if !sessions.ValidID(captured.SessionID) {
		t.Errorf("Config.SessionID = %q is not a canonical UUIDv4", captured.SessionID)
	}
}

// TestRunAgentRun_PtyPath_ConfigWiring asserts that each non-SessionID,
// non-WorkDir field of ptyrunner.Config round-trips byte-for-byte from
// the parsed args (covered in two fixtures). SessionID + WorkDir have
// dedicated pins above.
func TestRunAgentRun_PtyPath_ConfigWiring(t *testing.T) {
	type wantFields struct {
		model            string
		effort           string
		maxTurns         int
		allowedTools     []string
		systemPromptFile string
		promptBytes      string
	}
	tests := []struct {
		name string
		fx   func(t *testing.T) (validArgsFixture, wantFields)
	}{
		{
			name: "sonnet medium 3 turns",
			fx: func(t *testing.T) (validArgsFixture, wantFields) {
				f := newValidArgsFixture(t)
				return f, wantFields{
					model:            "sonnet-4-6",
					effort:           "medium",
					maxTurns:         3,
					allowedTools:     []string{"Read", "Bash"},
					systemPromptFile: f.systemPromptFile,
					promptBytes:      "hello",
				}
			},
		},
		{
			name: "opus max 12 turns",
			fx: func(t *testing.T) (validArgsFixture, wantFields) {
				f := newValidArgsFixture(t)
				// Rewrite prompt file content for round-trip verification.
				if err := os.WriteFile(f.promptFile, []byte("greetings"), 0o644); err != nil {
					t.Fatalf("rewrite prompt file: %v", err)
				}
				argv := slices.Clone(f.argv)
				for i := 0; i < len(argv)-1; i++ {
					switch argv[i] {
					case "--model":
						argv[i+1] = "opus-4-7"
					case "--effort":
						argv[i+1] = "max"
					case "--max-turns":
						argv[i+1] = "12"
					case "--allowed-tools":
						argv[i+1] = "Read"
					}
				}
				f.argv = argv
				return f, wantFields{
					model:            "opus-4-7",
					effort:           "max",
					maxTurns:         12,
					allowedTools:     []string{"Read"},
					systemPromptFile: f.systemPromptFile,
					promptBytes:      "greetings",
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx, want := tc.fx(t)
			installFakeSeams(t)
			t.Setenv("PYRY_USE_STREAMJSON", "")
			t.Setenv("PYRY_CLAUDE_BIN", "/usr/local/bin/claude-fixture")

			var capturedCfg ptyrunner.Config
			var capturedTools []string
			settingsWrite = func(tools []string) (string, error) {
				capturedTools = slices.Clone(tools)
				f, err := os.CreateTemp("", "pyry-test-settings-*.json")
				if err != nil {
					return "", err
				}
				_ = f.Close()
				return f.Name(), nil
			}
			ptyRun = func(_ context.Context, cfg ptyrunner.Config) error {
				capturedCfg = cfg
				return nil
			}

			var stdout bytes.Buffer
			if err := runAgentRun(&stdout, fx.argv); err != nil {
				t.Fatalf("runAgentRun: %v", err)
			}

			if capturedCfg.ClaudeBin != "/usr/local/bin/claude-fixture" {
				t.Errorf("ClaudeBin = %q, want %q", capturedCfg.ClaudeBin, "/usr/local/bin/claude-fixture")
			}
			if capturedCfg.SystemPrompt != want.systemPromptFile {
				t.Errorf("SystemPrompt = %q, want %q", capturedCfg.SystemPrompt, want.systemPromptFile)
			}
			if capturedCfg.Model != want.model {
				t.Errorf("Model = %q, want %q", capturedCfg.Model, want.model)
			}
			if capturedCfg.Effort != want.effort {
				t.Errorf("Effort = %q, want %q", capturedCfg.Effort, want.effort)
			}
			if capturedCfg.MaxTurns != want.maxTurns {
				t.Errorf("MaxTurns = %d, want %d", capturedCfg.MaxTurns, want.maxTurns)
			}
			if string(capturedCfg.PromptBytes) != want.promptBytes {
				t.Errorf("PromptBytes = %q, want %q", string(capturedCfg.PromptBytes), want.promptBytes)
			}
			if capturedCfg.Stdout != &stdout {
				t.Errorf("Stdout pointer mismatch: got %p, want %p", capturedCfg.Stdout, &stdout)
			}
			if capturedCfg.Stderr != os.Stderr {
				t.Errorf("Stderr = %v, want os.Stderr", capturedCfg.Stderr)
			}
			if capturedCfg.SettingsPath == "" {
				t.Errorf("SettingsPath empty in captured Config")
			}
			if !slices.Equal(capturedTools, want.allowedTools) {
				t.Errorf("settingsWrite received tools = %v, want %v", capturedTools, want.allowedTools)
			}
		})
	}
}

// TestRunAgentRun_PtyPath_AllowedToolsPassedToSettings pins the
// deny-default allowlist's load-bearing path: the slice handed to
// settings.WriteSettings equals parsed.allowedTools byte-for-byte.
func TestRunAgentRun_PtyPath_AllowedToolsPassedToSettings(t *testing.T) {
	fx := newValidArgsFixture(t)
	argv := fx.argvReplacing("--allowed-tools", "Read, Bash, Edit")
	installFakeSeams(t)
	t.Setenv("PYRY_USE_STREAMJSON", "")

	var captured []string
	settingsWrite = func(tools []string) (string, error) {
		captured = slices.Clone(tools)
		f, err := os.CreateTemp("", "pyry-test-settings-*.json")
		if err != nil {
			return "", err
		}
		_ = f.Close()
		return f.Name(), nil
	}

	if err := runAgentRun(io.Discard, argv); err != nil {
		t.Fatalf("runAgentRun: %v", err)
	}
	want := []string{"Read", "Bash", "Edit"}
	if !slices.Equal(captured, want) {
		t.Errorf("settingsWrite received tools = %v, want %v", captured, want)
	}
}

// TestRunAgentRun_RealClaude is env-gated: only runs when
// PYRY_E2E_REAL_CLAUDE=1. Covers the cmd→helpers→ptyrunner wiring at the
// cmd boundary against real claude in both default and fallback modes.
// The wire-shape byte-equivalence smoke lives in #482 at the ptyrunner
// boundary; this test asserts only that the verb produces dispatcher-
// recognisable stream-json events end-to-end.
func TestRunAgentRun_RealClaude(t *testing.T) {
	if os.Getenv("PYRY_E2E_REAL_CLAUDE") != "1" {
		t.Skip("set PYRY_E2E_REAL_CLAUDE=1 to run")
	}

	runSubtest := func(t *testing.T, useStreamJSON bool) {
		t.Helper()
		dir := t.TempDir()
		promptPath := filepath.Join(dir, "prompt.txt")
		if err := os.WriteFile(promptPath, []byte("Reply with only the literal word OK and nothing else."), 0o644); err != nil {
			t.Fatalf("write prompt: %v", err)
		}
		sysPath := filepath.Join(dir, "system.txt")
		if err := os.WriteFile(sysPath, []byte("Test system prompt."), 0o644); err != nil {
			t.Fatalf("write system: %v", err)
		}
		workdir := filepath.Join(dir, "work")
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			t.Fatalf("mkdir work: %v", err)
		}

		argv := []string{
			"--prompt-file", promptPath,
			"--system-prompt-file", sysPath,
			"--allowed-tools", "Read",
			"--max-turns", "1",
			"--effort", "low",
			"--model", "sonnet",
			"--workdir", workdir,
			"--output-format", "stream-json",
		}
		if useStreamJSON {
			t.Setenv("PYRY_USE_STREAMJSON", "1")
		} else {
			t.Setenv("PYRY_USE_STREAMJSON", "")
		}

		var stdout bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- runAgentRun(&stdout, argv) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runAgentRun: %v\nstdout=%q", err, stdout.String())
			}
		case <-time.After(90 * time.Second):
			t.Fatalf("runAgentRun did not return within 90s\nstdout so far=%q", stdout.String())
		}

		var sawSystem, sawAssistant, sawResult bool
		for _, line := range strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n") {
			if line == "" {
				continue
			}
			var ev struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			switch ev.Type {
			case "system":
				sawSystem = true
			case "assistant":
				sawAssistant = true
			case "result":
				sawResult = true
			}
		}
		if !sawSystem || !sawAssistant || !sawResult {
			t.Errorf("missing one of system/assistant/result events (system=%v assistant=%v result=%v):\n%s",
				sawSystem, sawAssistant, sawResult, stdout.String())
		}
	}

	t.Run("default_pty_path", func(t *testing.T) { runSubtest(t, false) })
	t.Run("fallback_streamrunner_path", func(t *testing.T) { runSubtest(t, true) })
}

func TestSplitAllowedTools(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"comma", "Read,Bash", []string{"Read", "Bash"}},
		{"space", "Read Bash", []string{"Read", "Bash"}},
		{"mixed", "Read, Bash , Edit", []string{"Read", "Bash", "Edit"}},
		{"empty", "", []string{}},
		{"separators only", ",,Read,,", []string{"Read"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitAllowedTools(tc.raw)
			if !slices.Equal(got, tc.want) {
				t.Errorf("splitAllowedTools(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
