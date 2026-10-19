//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
)

// Exercise the real helper loop rather than only the action state adapter.
func TestRecordedCheckpointProcessSeal(t *testing.T) {
	for _, name := range []string{"complete", "missing-action", "changed-action"} {
		t.Run(name, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(home, "work")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			c := tapeProcessConfig{Mode: "replay", Home: home, Scenario: "interrupt-thinking", CheckpointDir: filepath.Join(home, "actions"), Claim: filepath.Join(home, "claim"), Finish: filepath.Join(home, "finish"), Receipt: filepath.Join(home, "receipt"), Path: filepath.Join(home, "tape.json")}
			if err := claudetape.NewCheckpointDirectory(c.CheckpointDir); err != nil {
				t.Fatal(err)
			}
			marshal := func(v any) json.RawMessage {
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			settings, promptFile := filepath.Join(home, "settings.json"), filepath.Join(home, "system.txt")
			if err := os.WriteFile(settings, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(promptFile, []byte("synthetic system"), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--input-format", "stream-json", "--settings", settings, "--append-system-prompt-file", promptFile}

			input := marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": scenario.InitialThinkingPrompt("tape-interrupt-thinking")}}}})
			action := claudetape.ClientCheckpoint{Type: claudetape.ClientCheckpointDirection, Sequence: 1, Action: "interrupt_phase", ConversationID: tapeConversation, Phase: "thinking"}
			interrupt := json.RawMessage(`{"type":"control_request","request_id":"1","request":{"subtype":"interrupt"}}`)
			tape := &claudetape.Tape{Version: 2, Scenario: c.Scenario, Producer: claudetape.Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "synthetic process control", CapturedAt: "2026-10-19T00:00:00Z"}, Args: tapeArgs(args, c), Files: map[string]json.RawMessage{"$cwd": json.RawMessage(`"${PILOT_HOME}/work"`), "--settings": json.RawMessage(`{}`), "--append-system-prompt-file": marshal("synthetic system")}, Frames: []claudetape.Frame{{Direction: "stdin", Data: input}, {Direction: claudetape.ClientCheckpointDirection, Data: marshal(action)}, {Direction: "stdin", Data: interrupt}, {Direction: "stdout", Data: json.RawMessage(`{"type":"result","subtype":"error_during_execution"}`)}}}
			if err := tape.Save(c.Path); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(home, "config.json")
			if err := os.WriteFile(config, marshal(c), 0600); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestClaudeTapeProcess$", "--"}, args...)...)
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "PYRY_CLAUDE_TAPE_PROCESS="+config)
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if _, err := in.Write(append(input, '\n')); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "complete":
				if err := claudetape.PublishCheckpoint(c.CheckpointDir, action, 2*time.Second); err != nil {
					if raw, e := os.ReadFile(c.Receipt); e == nil {
						var r tapeReceipt
						if json.Unmarshal(raw, &r) == nil {
							t.Fatalf("checkpoint helper did not acknowledge: %s", r.Error)
						}
					}
					t.Fatal(err)
				}
				if _, err := in.Write(append(interrupt, '\n')); err != nil {
					t.Fatal(err)
				}
				scan := bufio.NewScanner(out)
				if !scan.Scan() || !strings.Contains(scan.Text(), "error_during_execution") {
					t.Fatal("held native result was not released")
				}
			case "changed-action":
				action.Phase = "responding"
				if err := claudetape.PublishCheckpoint(c.CheckpointDir, action, 150*time.Millisecond); err == nil {
					t.Fatal("mismatched action acknowledged")
				}
			}
			if err := os.WriteFile(c.Finish, nil, 0600); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if ctx.Err() != nil || (err == nil) != (name == "complete") {
				t.Fatalf("helper completion err=%v", err)
			}
			raw, err := os.ReadFile(c.Receipt)
			if err != nil {
				t.Fatal("missing explicit helper receipt")
			}
			var receipt tapeReceipt
			if json.Unmarshal(raw, &receipt) != nil || receipt.Success != (name == "complete") {
				t.Fatal("wrong helper receipt")
			}
			if name == "complete" {
				if _, err := os.Stat(c.CheckpointDir); !os.IsNotExist(err) {
					t.Fatal("successful helper did not revoke action pathname")
				}
			}
		})
	}
}
