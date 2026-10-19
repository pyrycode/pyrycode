//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Each control owns its shell process group, including a reader forked by Bash.
func stopHoldCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}

func stopSetupEnvelope(t *testing.T, typ string, payload any) protocol.Envelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("encode setup fixture")
	}
	return protocol.Envelope{Type: typ, Payload: raw}
}

func TestStopHoldOperation(t *testing.T) {
	for _, form := range []string{"bare", "single", "double", "single spaces", "double spaces", "exec", "semicolon"} {
		t.Run(form, func(t *testing.T) {
			dir := t.TempDir()
			if strings.Contains(form, "spaces") {
				dir = filepath.Join(dir, "space dir")
			}
			fifo := filepath.Join(t.TempDir(), "hold.fifo")
			arrived, release := tpcapHoldFIFO(t, fifo)
			hold := newStopHoldOperation(t, dir, fifo)
			command := hold.path
			switch form {
			case "single", "single spaces":
				command = stopShellQuote(hold.path)
			case "double", "double spaces":
				command = stopDoubleQuote(hold.path)
			case "exec":
				command = " exec " + stopShellQuote(hold.path)
			case "semicolon":
				command = stopShellQuote(hold.path) + " ; \n"
			}
			setup := stopHeldTaskSetup{hold: hold}
			tool := protocol.ToolUsePayload{ConversationID: "bound", ToolUseID: "tool", Name: "Bash", Input: map[string]string{"command": command, "run_in_background": "true"}}
			roster := protocol.BackgroundTaskRosterPayload{ConversationID: "bound", Tasks: []protocol.BackgroundTask{{TaskID: "held", ToolCallID: "tool", TaskType: "local_bash"}}}
			for _, env := range []protocol.Envelope{stopSetupEnvelope(t, protocol.TypeToolUse, tool), stopSetupEnvelope(t, protocol.TypeBackgroundTaskRoster, roster)} {
				if err := setup.observe(env, "bound", fifo); err != nil {
					t.Fatal(err)
				}
			}
			if setup.taskID() != "" {
				t.Fatal("command text without execution established identity")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := stopHoldCommand(ctx, command)
			if err := cmd.Start(); err != nil {
				t.Fatal("start hold control")
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("hold control cleanup timed out")
				}
			})
			select {
			case <-arrived:
			case <-ctx.Done():
				t.Fatal("hold control did not arrive")
			}
			if setup.taskID() != "held" {
				t.Fatalf("executed invocation unidentified: %s", setup.diagnostic())
			}
			for _, tc := range []struct {
				name, conv, background, command string
				want                            bool
			}{
				{"execution before metadata", "bound", "true", command, true},
				{"foreground", "bound", "false", command, false},
				{"wrong conversation", "other", "true", command, false},
				{"unrelated operation", "bound", "true", "true", false},
				{"path mentioned", "bound", "true", "echo " + stopShellQuote(hold.path), false},
				{"compound invocation", "bound", "true", "true; " + stopShellQuote(hold.path), false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					candidate := tool
					candidate.ConversationID = tc.conv
					candidate.Input = map[string]string{"command": tc.command, "run_in_background": tc.background}
					s := stopHeldTaskSetup{hold: hold}
					for _, env := range []protocol.Envelope{stopSetupEnvelope(t, protocol.TypeBackgroundTaskRoster, roster), stopSetupEnvelope(t, protocol.TypeToolUse, candidate)} {
						if err := s.observe(env, "bound", fifo); err != nil {
							t.Fatal(err)
						}
					}
					if (s.taskID() != "") != tc.want {
						t.Fatalf("attribution mismatch: %s", s.diagnostic())
					}
					// An ineligible invocation cannot lend its witness to another tool.
					if !tc.want && tc.name != "unrelated operation" {
						candidate = tool
						candidate.ToolUseID = "unexecuted-tool"
						if err := s.observe(stopSetupEnvelope(t, protocol.TypeToolUse, candidate), "bound", fifo); err != nil {
							t.Fatal(err)
						}
						rosterCopy := roster
						rosterCopy.Tasks = []protocol.BackgroundTask{{TaskID: "other", ToolCallID: candidate.ToolUseID, TaskType: "local_bash"}}
						if err := s.observe(stopSetupEnvelope(t, protocol.TypeBackgroundTaskRoster, rosterCopy), "bound", fifo); err != nil {
							t.Fatal(err)
						}
						if s.taskID() != "" {
							t.Fatal("borrowed execution evidence from ineligible invocation")
						}
					}
				})
			}
			select {
			case err := <-done:
				t.Fatalf("hold exited before release: success=%t", err == nil)
			default:
			}
			release()
			release()
			select {
			case err := <-done:
				// Leave a result for the cleanup wait as well.
				done <- err
				if err != nil {
					t.Fatal("released hold failed")
				}
			case <-ctx.Done():
				t.Fatal("released hold did not exit")
			}
			// A second real execution invalidates the single-invocation witness.
			second := stopHoldCommand(ctx, command)
			if err := second.Start(); err != nil {
				t.Fatal("start repeat execution")
			}
			defer func() { cancel(); _ = second.Wait() }()
			for hold.executed() && ctx.Err() == nil {
				time.Sleep(time.Millisecond)
			}
			if ctx.Err() != nil {
				t.Fatal("repeat execution not witnessed")
			}
			if setup.taskID() != "" {
				t.Fatal("repeated execution retained attribution")
			}
		})
	}
}

func TestStopHoldOperationRejectsMentions(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "hold.fifo")
	arrived, release := tpcapHoldFIFO(t, fifo)
	hold := newStopHoldOperation(t, t.TempDir(), fifo)
	for _, command := range []string{
		"echo " + stopShellQuote(hold.path),
		"# " + hold.path,
		"true; " + stopShellQuote(hold.path),
		stopShellQuote(hold.path) + " ignored",
		"cat " + stopShellQuote(fifo),
		"Please run " + stopShellQuote(hold.path),
		"echo token " + stopShellQuote(hold.path),
	} {
		if hold.matches(command) {
			t.Fatal("non-invocation text matched hold operation")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "bash", "-c", "echo "+stopShellQuote(hold.path)).Run(); err != nil {
		t.Fatal("mention control failed")
	}
	if hold.executed() {
		t.Fatal("mention produced execution evidence")
	}
	// FIFO arrival from another operation is insufficient physical evidence.
	cmd := exec.CommandContext(ctx, "cat", fifo)
	if err := cmd.Start(); err != nil {
		t.Fatal("start unrelated FIFO reader")
	}
	defer func() { cancel(); release(); _ = cmd.Wait() }()
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("unrelated reader did not arrive")
	}
	if hold.executed() {
		t.Fatal("FIFO arrival established operation execution")
	}
	s := stopHeldTaskSetup{hold: hold}
	tool := protocol.ToolUsePayload{ConversationID: "bound", ToolUseID: "tool", Name: "Bash", Input: map[string]string{"command": stopShellQuote(hold.path), "run_in_background": "true"}}
	roster := protocol.BackgroundTaskRosterPayload{ConversationID: "bound", Tasks: []protocol.BackgroundTask{{TaskID: "held", ToolCallID: "tool", TaskType: "local_bash"}}}
	for _, env := range []protocol.Envelope{stopSetupEnvelope(t, protocol.TypeToolUse, tool), stopSetupEnvelope(t, protocol.TypeBackgroundTaskRoster, roster)} {
		if err := s.observe(env, "bound", fifo); err != nil {
			t.Fatal(err)
		}
	}
	if s.taskID() != "" {
		t.Fatal("FIFO arrival and unexecuted tool text established identity")
	}
}
