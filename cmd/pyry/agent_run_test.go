package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
