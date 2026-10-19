//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackgroundReplyOperation(t *testing.T) {
	for _, spelling := range []string{"unquoted", "single", "double", "single spaces", "double spaces", "mention"} {
		t.Run(spelling, func(t *testing.T) {
			work := t.TempDir()
			if strings.Contains(spelling, "spaces") {
				work = filepath.Join(work, "with spaces")
			}
			rig := newBackgroundReplyOperation(t, work)
			command := rig.path
			if strings.HasPrefix(spelling, "single") {
				command = "'" + command + "'"
			} else if strings.HasPrefix(spelling, "double") {
				command = `"` + command + `"`
			} else if spelling == "mention" {
				command = "printf '%s' '" + command + "'"
			}
			input, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_use_id": "held-tool", "tool_input": map[string]any{"command": command, "timeout": 20000}})
			hook := exec.Command(rig.hook)
			hook.Stdin = strings.NewReader(string(input))
			out, err := hook.Output()
			if err != nil {
				t.Fatal("hook failed")
			}
			var response struct {
				Output struct {
					Input struct {
						Command string `json:"command"`
						Timeout int    `json:"timeout"`
					} `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if json.Unmarshal(out, &response) != nil || response.Output.Input.Timeout != 20000 {
				t.Fatal("hook lost tool input")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", response.Output.Input.Command)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			t.Cleanup(func() {
				rig.release()
				cancel()
				if !waited {
					_ = cmd.Wait()
				}
			})
			if spelling == "mention" {
				if err := cmd.Wait(); err != nil {
					t.Fatal(err)
				}
				waited = true
				if rig.witness() != nil {
					t.Fatal("mention created physical witness")
				}
				return
			}
			var witness *backgroundReplyExecution
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				witness = rig.witness()
				if witness != nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if witness == nil || witness.Tool != "held-tool" || witness.Operation != rig.operation || !rig.held(witness) {
				t.Fatal("missing correlated physical hold")
			}
			frames := []string{`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`, `{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"hold","role":"assistant","content":[{"type":"tool_use","id":"held-tool","name":"Bash"}]}}`, `{"type":"result","subtype":"success"}`}
			l, m, _, s, _, p := backgroundReplyBoundary(backgroundReplyTestLines(frames), "token", witness)
			if l != 0 || m != 2 || s != 1 || p != "agent" {
				t.Fatal("physical execution lost Agent/Bash identity")
			}
			operation := rig.operation
			rig.operation = "wrong-operation"
			if rig.witness() != nil {
				t.Fatal("wrong operation accepted")
			}
			rig.operation = operation
			rig.release()
			if err := cmd.Wait(); err != nil {
				t.Fatal("hold did not finish")
			}
			waited = true
			if rig.held(witness) {
				t.Fatal("exited hold still accepted")
			}
			// A second invocation must leave ambiguity rather than overwrite proof.
			if err := exec.CommandContext(ctx, "sh", "-c", response.Output.Input.Command).Run(); err != nil {
				t.Fatal(err)
			}
			if rig.witness() != nil {
				t.Fatal("repeated execution remained unambiguous")
			}
		})
	}
}
