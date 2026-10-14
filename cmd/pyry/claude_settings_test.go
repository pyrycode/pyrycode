package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

const operatorSettings = `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"true"}]}]},"enableAllProjectMcpServers":false}`

func writeSettingsFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestNewClaudeSettings_Resolution(t *testing.T) {
	t.Parallel()
	instance := t.TempDir()
	instanceFile := filepath.Join(instance, claudeSettingsFileName)
	for _, tc := range []struct {
		name, flag, env string
		instanceFile    bool
		wantPath        string
		wantErr         string
	}{
		{name: "unset", wantPath: ""},
		{name: "flag wins", flag: "/flag.json", env: "/env.json", instanceFile: true, wantPath: "/flag.json"},
		{name: "env over instance file", env: "/env.json", instanceFile: true, wantPath: "/env.json"},
		{name: "instance file", instanceFile: true, wantPath: instanceFile},
		{name: "relative flag refused", flag: "settings.json", wantErr: "claude settings from flag -pyry-claude-settings: want one absolute path"},
		{name: "relative env refused", env: "settings.json", wantErr: "claude settings from env PYRY_CLAUDE_SETTINGS: want one absolute path"},
		{name: "control byte refused", flag: "/a\nb.json", wantErr: "want one absolute path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.instanceFile {
				dir = instance
				writeSettingsFixture(t, instanceFile, "{}", 0o600)
			}
			s, err := newClaudeSettings(tc.flag, tc.env, dir, discardLogger())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantPath == "" {
				if s != nil || s.composer() != nil {
					t.Fatalf("unset resolved to %+v", s)
				}
				return
			}
			if s.path != tc.wantPath {
				t.Fatalf("path = %q, want %q", s.path, tc.wantPath)
			}
		})
	}
}

func TestNewClaudeSettings_RemovesStaleComposedFiles(t *testing.T) {
	t.Parallel()
	instance := t.TempDir()
	stale := filepath.Join(instance, claudeSettingsComposedDir, "old.json")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSettingsFixture(t, stale, "{}", 0o600)
	if _, err := newClaudeSettings("", "", instance, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Fatalf("stale composed dir survives: %v", err)
	}
}

func TestClaudeSettingsCompose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	operator := filepath.Join(dir, "daemon-settings.json")
	writeSettingsFixture(t, operator, operatorSettings, 0o644)
	s, err := newClaudeSettings(operator, "", filepath.Join(dir, "instance"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	// A launch with no --settings of its own gets the operator file itself.
	got, err := s.compose("")
	if err != nil || got != operator {
		t.Fatalf("compose(\"\") = %q, %v", got, err)
	}

	// A launch carrying the pool's per-session file gets one merged file.
	session := filepath.Join(dir, "session.json")
	writeSettingsFixture(t, session, `{"enableAllProjectMcpServers":true,"skipDangerousModePermissionPrompt":true}`, 0o600)
	merged, err := s.compose(session)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(merged) != s.outDir {
		t.Fatalf("merged file %q outside %q", merged, s.outDir)
	}
	info, err := os.Stat(merged)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("merged file mode: %v %v", info, err)
	}
	var obj map[string]any
	data, _ := os.ReadFile(merged)
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["enableAllProjectMcpServers"] != true || obj["skipDangerousModePermissionPrompt"] != true || obj["hooks"] == nil {
		t.Fatalf("merged = %s", data)
	}

	// The same session maps to the same file, and an edit is read on the next launch.
	writeSettingsFixture(t, operator, `{"env":{"A":"1"}}`, 0o644)
	again, err := s.compose(session)
	if err != nil || again != merged {
		t.Fatalf("recompose = %q, %v", again, err)
	}
	data, _ = os.ReadFile(again)
	if !strings.Contains(string(data), `"env"`) || strings.Contains(string(data), `"hooks"`) {
		t.Fatalf("edit not picked up: %s", data)
	}

	// Inline JSON as the launch's own value merges the same way.
	inline, err := s.compose(`{"model":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(inline)
	if !strings.Contains(string(data), `"model"`) || !strings.Contains(string(data), `"env"`) {
		t.Fatalf("inline merge: %s", data)
	}
}

func TestClaudeSettingsCompose_Refusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, path string)
		wantErr string
	}{
		{name: "missing", setup: func(*testing.T, string) {}, wantErr: "missing"},
		{name: "invalid JSON", setup: func(t *testing.T, p string) { writeSettingsFixture(t, p, `{"hooks":`, 0o600) }, wantErr: "not a JSON object"},
		{name: "array", setup: func(t *testing.T, p string) { writeSettingsFixture(t, p, `[]`, 0o600) }, wantErr: "not a JSON object"},
		{name: "null", setup: func(t *testing.T, p string) { writeSettingsFixture(t, p, `null`, 0o600) }, wantErr: "not a JSON object"},
		{name: "trailing data", setup: func(t *testing.T, p string) { writeSettingsFixture(t, p, `{} {}`, 0o600) }, wantErr: "not a JSON object"},
		{name: "invalid UTF-8", setup: func(t *testing.T, p string) { writeSettingsFixture(t, p, "{\"a\":\"\xff\"}", 0o600) }, wantErr: "not a JSON object"},
		{name: "group writable", setup: func(t *testing.T, p string) { writeSettingsFixture(t, p, `{}`, 0o664) }, wantErr: "group or other writable"},
		{name: "directory", setup: func(t *testing.T, p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "not a regular file"},
		{name: "fifo", setup: func(t *testing.T, p string) {
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Skip("mkfifo:", err)
			}
		}, wantErr: "not a regular file"},
		{name: "too large", setup: func(t *testing.T, p string) {
			writeSettingsFixture(t, p, `{"a":"`+strings.Repeat("x", maxClaudeSettingsBytes)+`"}`, 0o600)
		}, wantErr: "larger than"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "daemon-settings.json")
			tc.setup(t, path)
			s, err := newClaudeSettings(path, "", filepath.Join(dir, "instance"), discardLogger())
			if err != nil {
				t.Fatal(err)
			}
			for _, current := range []string{"", filepath.Join(dir, "session.json")} {
				_, err := s.compose(current)
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), path) {
					t.Fatalf("compose(%q) err = %v, want %q naming the file", current, err, tc.wantErr)
				}
			}
		})
	}
}

// fakeArgvClaude is a claude stand-in that records each launch's argv as one
// JSON line, together with the content of its --settings file, then crashes on
// its first launch and stays up on every later one.
func fakeArgvClaude(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "claude"), filepath.Join(dir, "argv.log")
	script := `#!/bin/sh
settings=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--settings" ]; then settings="$a"; fi
  prev="$a"
done
{ printf 'ARGS'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; printf 'SETTINGS '; cat "$settings"; printf '\n'; } >> "` + log + `"
if [ ! -e "` + dir + `/crashed" ]; then : > "` + dir + `/crashed"; exit 1; fi
exec sleep 3600
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestStreamRunnerFactory_AppliesDaemonSettingsOnEveryLaunch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	operator := filepath.Join(dir, "daemon-settings.json")
	writeSettingsFixture(t, operator, operatorSettings, 0o600)
	session := filepath.Join(dir, "session-settings.json")
	writeSettingsFixture(t, session, `{"enableAllProjectMcpServers":true}`, 0o600)
	s, err := newClaudeSettings(operator, "", filepath.Join(dir, "instance"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	bin, log := fakeArgvClaude(t)
	runner, err := newStreamRunnerFactory(newStreamTurnSink(64, discardLogger()), "", nil, streamApprovalConfig{settings: s.composer()})(sessions.RunnerConfig{
		ClaudeBin:      bin,
		WorkDir:        t.TempDir(),
		SessionID:      "session",
		ClaudeArgs:     []string{"--model", "m", "--session-id", "session", "--settings", session},
		Logger:         discardLogger(),
		BackoffInitial: 10 * time.Millisecond,
		BackoffMax:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	var lines []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log)
		lines = strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(lines) < 4 {
		t.Fatalf("want two launches, log: %q", lines)
	}
	// The relaunch after the crash goes through the same composition. Its id
	// flag form is the transcript probe's choice, which reads the real
	// ~/.claude/projects here, so the --resume form is asserted one layer down
	// in internal/streamsup's TestSettingsComposerEveryLaunch instead.
	for i := 0; i < 2; i++ {
		args, content := strings.Fields(lines[2*i])[1:], strings.TrimPrefix(lines[2*i+1], "SETTINGS ")
		if n := countFlag(args, "--settings"); n != 1 {
			t.Fatalf("launch %d carries %d --settings flags: %q", i, n, args)
		}
		if countFlag(args, "--session-id")+countFlag(args, "--resume") != 1 {
			t.Fatalf("launch %d lacks its id flag: %q", i, args)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(content), &obj); err != nil {
			t.Fatalf("launch %d settings not JSON: %q", i, content)
		}
		if _, ok := obj["hooks"]; !ok || string(obj["enableAllProjectMcpServers"]) != "true" {
			t.Fatalf("launch %d settings = %s, want operator hooks with the session's keys winning", i, content)
		}
	}
}

func TestStreamRunnerFactory_NoDaemonSettingsLeavesArgvAlone(t *testing.T) {
	t.Parallel()
	session := filepath.Join(t.TempDir(), "session-settings.json")
	writeSettingsFixture(t, session, `{"enableAllProjectMcpServers":true}`, 0o600)
	var s *claudeSettings
	bin, log := fakeArgvClaude(t)
	runner, err := newStreamRunnerFactory(newStreamTurnSink(64, discardLogger()), "", nil, streamApprovalConfig{settings: s.composer()})(sessions.RunnerConfig{
		ClaudeBin:  bin,
		WorkDir:    t.TempDir(),
		SessionID:  "session",
		ClaudeArgs: []string{"--settings", session},
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log)
		if line, _, ok := strings.Cut(string(data), "\n"); ok {
			args := strings.Fields(line)[1:]
			want := []string{"--settings", session}
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "--settings" && !reflect.DeepEqual(args[i:i+2], want) {
					t.Fatalf("argv changed: %q", args)
				}
			}
			if countFlag(args, "--settings") != 1 {
				t.Fatalf("argv changed: %q", args)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child never launched")
}

func countFlag(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag {
			n++
		}
	}
	return n
}

// A settings value that is not one absolute path is a configuration mistake and
// stops startup before the control socket opens, like a malformed account source.
func TestRunSupervisor_RefusesRelativeClaudeSettings(t *testing.T) {
	home := accountTestHome(t)
	t.Setenv(claudeSettingsEnv, "")
	socket := filepath.Join(home, "settings-refused.sock")
	stub := stubBinary(t)
	err := runSupervisor([]string{"-pyry-name", "settings-refused", "-pyry-socket", socket, "-pyry-workdir", home, "-pyry-codex", stub, "-pyry-claude", stub, "-" + claudeSettingsFlagName, "daemon-settings.json"})
	if err == nil || !strings.Contains(err.Error(), "flag -"+claudeSettingsFlagName) {
		t.Fatalf("err = %v, want a refusal naming the flag", err)
	}
	if _, statErr := os.Stat(socket); statErr == nil {
		t.Fatal("control socket opened despite a malformed settings value")
	}
}
