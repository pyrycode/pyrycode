package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// fakeClaudeAssistantLine is the single JSONL line the fake-claude binary
// writes to the watched session file. Shape: assistant entry with a single
// text content block and a usage object — satisfies jsonl.Reader's parser
// and the deterministic end-of-turn rule (stop_reason == "end_turn" AND
// sum text length > 0). Kept on one line so the file is one complete entry
// the watcher drains atomically.
const fakeClaudeAssistantLine = `{"type":"assistant","message":{"id":"msg_fake","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

// TestAgentRunFakeClaude is the fake-claude entry point used by the
// runAgentRun wiring tests. When PYRY_AGENT_RUN_FAKE=1 is set in the
// environment, the test binary re-exec'd as claude:
//
//  1. Optionally captures argv[1:] to GO_AGENT_RUN_FAKE_ARGS_FILE.
//  2. Writes a canned end-of-turn assistant JSONL line to
//     $HOME/.claude/projects/<encoded-cwd>/<sid>.jsonl, where <sid> is read
//     from --session-id in argv. This satisfies pyry's tail.Watcher so the
//     emitter's OnEndOfTurn fires and the run terminates cleanly.
//  3. Drains stdin into io.Discard.
//  4. Sleeps GO_AGENT_RUN_FAKE_LIFETIME (default 200ms) and exits 0.
func TestAgentRunFakeClaude(t *testing.T) {
	if os.Getenv("PYRY_AGENT_RUN_FAKE") != "1" {
		return
	}
	if path := os.Getenv("GO_AGENT_RUN_FAKE_ARGS_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "fake: write args: %v\n", err)
			os.Exit(99)
		}
	}

	if err := writeFakeClaudeJSONL(); err != nil {
		fmt.Fprintf(os.Stderr, "fake: write jsonl: %v\n", err)
		os.Exit(98)
	}

	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
	lifetime := 200 * time.Millisecond
	if raw := os.Getenv("GO_AGENT_RUN_FAKE_LIFETIME"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			lifetime = d
		}
	}
	time.Sleep(lifetime)
	os.Exit(0)
}

// writeFakeClaudeJSONL writes the canned end-of-turn assistant line to the
// session JSONL path the tail.Watcher expects: $HOME/.claude/projects/
// <agentrun.EncodeProjectDir(cwd)>/<sid>.jsonl. Resolves <sid> from
// --session-id in argv and <cwd> from os.Getwd (which equals the parent
// runAgentRun's --workdir, since Drive sets cmd.Dir to that).
func writeFakeClaudeJSONL() error {
	sid := os.Getenv("GO_AGENT_RUN_FAKE_SESSION_ID")
	if sid == "" {
		return fmt.Errorf("no GO_AGENT_RUN_FAKE_SESSION_ID in env")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	encoded, err := agentrun.EncodeProjectDir(cwd)
	if err != nil {
		return fmt.Errorf("encode cwd: %w", err)
	}
	home := os.Getenv("HOME")
	if home == "" {
		return fmt.Errorf("empty HOME")
	}
	dir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	return os.WriteFile(path, []byte(fakeClaudeAssistantLine+"\n"), 0o600)
}

// configureFakeClaude wires the test-only env knobs so a runAgentRun call
// spawns a shell wrapper that exec's the test binary in fake-claude mode,
// with ~ms-scale drive delays. Without this, runAgentRun would try to
// spawn the real `claude` from PATH and the drive would block for ~6
// seconds.
//
// A shell wrapper is required because buildClaudeArgs (the production
// argv builder) emits real claude flags like `--settings <path>` which the
// Go test binary's flag parser would reject. The wrapper drops the
// production argv on the floor and re-execs the test binary with a fixed
// `-test.run` pinned to TestAgentRunFakeClaude.
func configureFakeClaude(t *testing.T) {
	t.Helper()
	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "fake-claude.sh")
	// The wrapper drops the production argv (the test binary's flag parser
	// rejects unknown claude flags) but first extracts --session-id and
	// re-exports it so the fake can write the canned JSONL to the path
	// pyry's tail.Watcher is waiting for.
	body := fmt.Sprintf(`#!/bin/sh
SID=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session-id) SID="$2"; shift 2 ;;
    *) shift ;;
  esac
done
export GO_AGENT_RUN_FAKE_SESSION_ID="$SID"
exec %q -test.run=^TestAgentRunFakeClaude$
`, os.Args[0])
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake-claude script: %v", err)
	}
	t.Setenv("PYRY_CLAUDE_BIN", script)
	t.Setenv("PYRY_AGENT_RUN_FAKE", "1")
	t.Setenv("PYRY_AGENT_RUN_TRUST_DELAY", "5ms")
	t.Setenv("PYRY_AGENT_RUN_PROMPT_DELAY", "5ms")
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

// TestRunAgentRun_EmitsSettingsFile drives runAgentRun end-to-end with a
// valid argv, captures stdout, and asserts the marker line appears as the
// first stdout line and the on-disk settings file is written. The marker
// line is the dispatcher's parse contract (sibling #332). Subsequent
// stdout lines — the stream-json passthrough and trailer — are validated
// in TestRunAgentRun_DrivesFakeClaude.
func TestRunAgentRun_EmitsSettingsFile(t *testing.T) {
	fx := newValidArgsFixture(t)
	configureFakeClaude(t)

	var stdout bytes.Buffer
	if err := runAgentRun(&stdout, fx.argv); err != nil {
		t.Fatalf("runAgentRun: %v", err)
	}

	wantPath := filepath.Join(fx.workdir, agentrun.SettingsFilename)
	wantLine := "settings-file: " + wantPath
	firstLine := strings.SplitN(stdout.String(), "\n", 2)[0]
	if firstLine != wantLine {
		t.Errorf("stdout first line:\n got  = %q\n want = %q", firstLine, wantLine)
	}

	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}
	wantJSON := `{"permissions":{"allow":["Read","Bash"],"defaultMode":"deny"}}` + "\n"
	if string(got) != wantJSON {
		t.Errorf("settings file content:\n got  = %q\n want = %q", got, wantJSON)
	}
}

// TestRunAgentRun_MarksWorkdirTrusted asserts that runAgentRun writes
// projects[realpath(workdir)].hasTrustDialogAccepted = true into
// $HOME/.claude.json so the supervised claude (#332) does not block on the
// workspace-trust TUI dialog at startup.
func TestRunAgentRun_MarksWorkdirTrusted(t *testing.T) {
	fx := newValidArgsFixture(t)
	configureFakeClaude(t)

	if err := runAgentRun(io.Discard, fx.argv); err != nil {
		t.Fatalf("runAgentRun: %v", err)
	}

	homeDir := os.Getenv("HOME")
	dataPath := filepath.Join(homeDir, ".claude.json")
	info, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("stat %s: %v", dataPath, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s: not a regular file", dataPath)
	}

	data, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("read %s: %v", dataPath, err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode %s: %v", dataPath, err)
	}

	wantKey, err := filepath.EvalSymlinks(fx.workdir)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", fx.workdir, err)
	}

	projects, ok := root["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects: not an object, got %T", root["projects"])
	}
	entry, ok := projects[wantKey].(map[string]any)
	if !ok {
		t.Fatalf("projects[%q]: not an object, got %T", wantKey, projects[wantKey])
	}
	got, ok := entry["hasTrustDialogAccepted"].(bool)
	if !ok {
		t.Fatalf("projects[%q].hasTrustDialogAccepted: not a bool, got %T", wantKey, entry["hasTrustDialogAccepted"])
	}
	if !got {
		t.Errorf("projects[%q].hasTrustDialogAccepted = false, want true", wantKey)
	}
}

// TestBuildClaudeArgs_Shape pins the production claude argv. Two
// assertions are load-bearing for the deny-default security model:
//
//   - `--permission-mode default` MUST appear so the settings file's
//     `defaultMode: deny` takes effect (anything else, notably acceptEdits
//     used in the upstream spike, silently overrides the file).
//   - `--allowedTools` MUST NOT appear. In interactive mode under the
//     settings layer it is additive and broadens the allow-list.
//
// The remaining flags are fixture-driven so a future flag addition forces
// an explicit test update.
func TestBuildClaudeArgs_Shape(t *testing.T) {
	const fixedSID = "deadbeef-dead-4ead-aead-deaddeadbeef"
	tests := []struct {
		name         string
		parsed       agentRunArgs
		settingsPath string
		sessionID    string
		want         []string
	}{
		{
			name: "canonical happy path",
			parsed: agentRunArgs{
				model:            "sonnet-4-6",
				systemPromptFile: "/tmp/sys.md",
				effort:           "medium",
				allowedTools:     []string{"Read", "Bash"},
			},
			settingsPath: "/tmp/.pyry-agent-run-settings.json",
			sessionID:    fixedSID,
			want: []string{
				"--settings", "/tmp/.pyry-agent-run-settings.json",
				"--permission-mode", "default",
				"--model", "sonnet-4-6",
				"--append-system-prompt-file", "/tmp/sys.md",
				"--effort", "medium",
				"--session-id", fixedSID,
			},
		},
		{
			name: "max effort",
			parsed: agentRunArgs{
				model:            "opus-4-7",
				systemPromptFile: "/tmp/x.md",
				effort:           "max",
				allowedTools:     []string{"Read"},
			},
			settingsPath: "/p.json",
			sessionID:    fixedSID,
			want: []string{
				"--settings", "/p.json",
				"--permission-mode", "default",
				"--model", "opus-4-7",
				"--append-system-prompt-file", "/tmp/x.md",
				"--effort", "max",
				"--session-id", fixedSID,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildClaudeArgs(tc.parsed, tc.settingsPath, tc.sessionID)
			if !slices.Equal(got, tc.want) {
				t.Errorf("buildClaudeArgs:\n got  = %v\n want = %v", got, tc.want)
			}
			// Security invariants — explicit, named assertions so a
			// drift in argv shape that happens to keep slices.Equal
			// passing still trips here.
			if idx := slices.Index(got, "--permission-mode"); idx < 0 || idx+1 >= len(got) || got[idx+1] != "default" {
				t.Errorf("missing `--permission-mode default` in %v", got)
			}
			if slices.Contains(got, "--allowedTools") || slices.Contains(got, "--allowed-tools") {
				t.Errorf("`--allowedTools` / `--allowed-tools` must NOT appear in %v", got)
			}
			if slices.Contains(got, "--max-turns") {
				t.Errorf("`--max-turns` must NOT appear in spawned argv (interactive claude ignores it): %v", got)
			}
			if slices.Contains(got, "--output-format") {
				t.Errorf("`--output-format` must NOT appear in spawned argv (stream-json is `-p`-mode only): %v", got)
			}
			if idx := slices.Index(got, "--session-id"); idx < 0 || idx+1 >= len(got) || got[idx+1] != tc.sessionID {
				t.Errorf("missing `--session-id %s` in %v", tc.sessionID, got)
			}
		})
	}
}

// TestBuildClaudeArgs_SessionIDShape pins that the session id minted by
// runAgentRun is a canonical UUIDv4 string by exercising newSessionUUID
// directly. Concrete shape: 36 chars, dashes at the standard positions,
// version-4 nibble at position 14, RFC 4122 variant at position 19.
func TestNewSessionUUID_Shape(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		got, err := newSessionUUID()
		if err != nil {
			t.Fatalf("newSessionUUID: %v", err)
		}
		if len(got) != 36 {
			t.Errorf("len = %d, want 36 (%q)", len(got), got)
		}
		if got[8] != '-' || got[13] != '-' || got[18] != '-' || got[23] != '-' {
			t.Errorf("dash positions wrong: %q", got)
		}
		if got[14] != '4' {
			t.Errorf("version nibble = %q, want '4'", got[14])
		}
		switch got[19] {
		case '8', '9', 'a', 'b':
		default:
			t.Errorf("variant nibble = %q, want one of 89ab", got[19])
		}
		if seen[got] {
			t.Errorf("collision: %q", got)
		}
		seen[got] = true
	}
}

// TestRunAgentRun_DrivesFakeClaude verifies the end-to-end wiring:
// runAgentRun reads the prompt file, builds the argv, spawns the (faked)
// claude, drives the PTY, watches the on-disk JSONL the fake writes,
// re-emits each event onto stdout, and composes a `type:"result"` trailer
// on end-of-turn. Asserts:
//
//  (a) the `settings-file:` marker line appears first;
//  (b) the assistant JSONL line the fake wrote appears byte-equivalent;
//  (c) the final line is a `type:"result"` trailer with subtype "success".
func TestRunAgentRun_DrivesFakeClaude(t *testing.T) {
	fx := newValidArgsFixture(t)
	configureFakeClaude(t)

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

	// Settings file must exist on disk — confirms we got past the
	// agentrun.WriteSettings step before spawning.
	settingsPath := filepath.Join(fx.workdir, agentrun.SettingsFilename)
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings file missing: %v", err)
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("stdout has %d lines, want >=3 (marker + assistant + trailer): %q", len(lines), stdout.String())
	}
	if !strings.HasPrefix(lines[0], "settings-file: ") {
		t.Errorf("stdout[0] = %q, want settings-file marker", lines[0])
	}
	// (b) the assistant line is somewhere between the marker and trailer.
	if !slices.Contains(lines, fakeClaudeAssistantLine) {
		t.Errorf("stdout missing assistant line %q\nfull stdout=%q", fakeClaudeAssistantLine, stdout.String())
	}
	// (c) the last line is a result trailer with subtype "success".
	var tr struct {
		Type           string `json:"type"`
		Subtype        string `json:"subtype"`
		TerminalReason string `json:"terminal_reason"`
		NumTurns       int    `json:"num_turns"`
		Usage          struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &tr); err != nil {
		t.Fatalf("decode trailer %q: %v", lines[len(lines)-1], err)
	}
	if tr.Type != "result" || tr.Subtype != "success" || tr.TerminalReason != "completed" {
		t.Errorf("trailer: %+v, want type=result subtype=success terminal_reason=completed", tr)
	}
	if tr.NumTurns != 1 {
		t.Errorf("trailer.num_turns = %d, want 1", tr.NumTurns)
	}
	if tr.Usage.InputTokens != 5 || tr.Usage.OutputTokens != 2 {
		t.Errorf("trailer.usage = %+v, want input=5 output=2 (matches fake canned line)", tr.Usage)
	}
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
