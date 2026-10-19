//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const backgroundReplySyncInputLimit = 1 << 20

func backgroundReplySyncArgs(t *testing.T, work string) []string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal("locate synchronous Bash hook binary")
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	hook := filepath.Join(work, "sync-bash-hook")
	script := "#!/bin/sh\nexec env PYRY_REPLY_SYNC_HELPER=1 " + quote(bin) + " '-test.run=^TestBackgroundReplySyncHelperProcess$'\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal("write synchronous Bash hook")
	}
	settings, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": quote(hook)}}},
	}}})
	if err != nil {
		t.Fatal("encode synchronous Bash settings")
	}
	return append(append([]string(nil), ptucArgs...), "--settings", string(settings))
}

// Model-supplied command strings remain data; only execution mode is constrained.
func backgroundReplySyncHook(r io.Reader, w io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(r, backgroundReplySyncInputLimit+1))
	if err != nil || len(raw) > backgroundReplySyncInputLimit {
		return errors.New("invalid Bash hook input")
	}
	var in struct {
		Name  string                     `json:"tool_name"`
		Input map[string]json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Name != "Bash" || in.Input == nil {
		return errors.New("invalid Bash hook input")
	}
	var command string
	if json.Unmarshal(in.Input["command"], &command) != nil || command == "" {
		return errors.New("invalid Bash hook input")
	}
	in.Input["run_in_background"] = json.RawMessage(`false`)
	// Keep the held operation synchronous throughout the 180-second capture window.
	in.Input["timeout"] = json.RawMessage(`240000`)
	return json.NewEncoder(w).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "updatedInput": in.Input,
	}})
}

func TestBackgroundReplySyncHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_REPLY_SYNC_HELPER") != "1" {
		return
	}
	if backgroundReplySyncHook(os.Stdin, os.Stdout) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestBackgroundReplySyncHook(t *testing.T) {
	t.Parallel()
	work := filepath.Join(t.TempDir(), "rig ' $() ; space")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	args := backgroundReplySyncArgs(t, work)
	if !reflect.DeepEqual(args[:len(ptucArgs)], ptucArgs) || args[len(ptucArgs)] != "--settings" {
		t.Fatal("base model, permissions or Agent availability changed")
	}
	var settings struct {
		Hooks map[string][]struct {
			Matcher string                           `json:"matcher"`
			Hooks   []struct{ Type, Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal([]byte(args[len(ptucArgs)+1]), &settings) != nil || len(settings.Hooks) != 1 || len(settings.Hooks["PreToolUse"]) != 1 {
		t.Fatal("invalid Bash-only hook settings")
	}
	entry := settings.Hooks["PreToolUse"][0]
	if entry.Matcher != "Bash" || len(entry.Hooks) != 1 || entry.Hooks[0].Type != "command" {
		t.Fatal("hook would constrain Agent execution")
	}
	for _, mode := range []string{`true`, `false`, ``} {
		t.Run("background="+mode, func(t *testing.T) {
			input := map[string]any{"command": "rig-hold", "description": "rig-description"}
			if mode == "false" {
				input["timeout"] = 1
			}
			if mode != "" {
				input["run_in_background"] = mode == "true"
			}
			raw, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": input})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", entry.Hooks[0].Command)
			cmd.Stdin = bytes.NewReader(raw)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal("hook process failed")
			}
			var result struct {
				Hook struct {
					Event string         `json:"hookEventName"`
					Input map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if json.Unmarshal(out, &result) != nil || result.Hook.Event != "PreToolUse" {
				t.Fatal("invalid hook response")
			}
			if result.Hook.Input["run_in_background"] != false {
				t.Fatal("Bash can return an async-launch notice instead of its release token")
			}
			if result.Hook.Input["timeout"] != float64(240000) {
				t.Fatal("Bash timeout can expire before the capture window")
			}
			if result.Hook.Input["command"] != "rig-hold" || result.Hook.Input["description"] != "rig-description" || len(result.Hook.Input) != 4 {
				t.Fatal("hook changed command or unrelated input")
			}
		})
	}
}

func TestBackgroundReplySyncHookRejectsInput(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		`{`, `null`, `{}`, `{"tool_name":"Agent","tool_input":{"command":"rig"}}`,
		`{"tool_name":"Bash","tool_input":null}`, `{"tool_name":"Bash","tool_input":{}}`,
		`{"tool_name":"Bash","tool_input":{"command":null}}`,
		`{"tool_name":"Bash","tool_input":{"command":17}}`,
		`{"tool_name":"Bash","tool_input":{"command":""}}`,
		`{"tool_name":"Bash","tool_input":{"command":"rig"}} {}`,
		`{"tool_name":"Bash","tool_input":{"command":"rig","description":"` + strings.Repeat("x", backgroundReplySyncInputLimit) + `"}}`,
	} {
		var out bytes.Buffer
		if err := backgroundReplySyncHook(strings.NewReader(input), &out); err == nil || out.Len() != 0 || strings.Contains(err.Error(), input) {
			t.Fatal("invalid input was accepted or echoed")
		}
	}
}
