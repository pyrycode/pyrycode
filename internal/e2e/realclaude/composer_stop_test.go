//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// TestRealClaudeComposerStopPreservesBackgroundTask requires completion after
// release, rather than treating an interrupt ack or retained roster as survival.
func TestRealClaudeComposerStopPreservesBackgroundTask(t *testing.T) {
	h, convID := startStreamRunningTurnHarness(t)
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal("resolve claude after harness startup")
	}
	t.Logf("Composer Stop Claude version: %s", probeClaudeVersion(bin))
	background := filepath.Join(h.workdir, "composer-background.fifo")
	arrived, release := tpcapHoldFIFO(t, background)
	defer release()
	sealSendMessage(t, h.phone, h.initSend, 27750, convID, "composer-background", rafcapPrompt(background, time.Now().UnixNano()))
	var setup stopHeldTaskSetup
	ended := false
	deadline := time.Now().Add(perTurnReplyBudget)
	for setup.taskID() == "" || !ended {
		env := composerReceive(t, h, deadline)
		if err := setup.observe(env, convID, background); err != nil {
			t.Fatalf("background setup: %v; %s", err, setup.diagnostic())
		}
		if env.Type == protocol.TypeTurnEnd {
			var p protocol.TurnEndPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal("decode setup turn end")
			}
			if p.ConversationID == convID {
				if p.StopReason != "end_turn" {
					t.Fatalf("setup turn ended with %q", p.StopReason)
				}
				ended = true
			}
		}
	}
	composerRendezvous(t, arrived)
	composerRequireReader(t, background, fifoLiveReaderPresent)
	foreground := filepath.Join(h.workdir, "composer-foreground.fifo")
	foregroundArrived, releaseForeground := tpcapHoldFIFO(t, foreground)
	defer releaseForeground()
	sealSendMessage(t, h.phone, h.initSend, 27751, convID, "composer-foreground", composerForegroundPrompt(foreground))
	deadline = time.Now().Add(perTurnReplyBudget)
	for {
		env := composerReceive(t, h, deadline)
		if env.Type == protocol.TypeTurnEnd || env.Type == protocol.TypeError {
			t.Fatal("foreground turn ended or refused before held call")
		}
		if env.Type != protocol.TypeToolUse {
			continue
		}
		var p protocol.ToolUsePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal("decode foreground tool")
		}
		if p.ConversationID == convID && p.Name == "Bash" && p.Input["command"] == "cat "+foreground && p.Input["run_in_background"] != "true" {
			break
		}
	}
	composerRendezvous(t, foregroundArrived)
	composerRequireReader(t, foreground, fifoLiveReaderPresent)
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: 27752, Type: protocol.TypeInterrupt, TS: time.Now().UTC()})
	drainForCancelledTurnEnd(t, h.phone, h.initRecv, convID, perTurnReplyBudget, false)
	composerRequireReader(t, background, fifoLiveReaderPresent)
	release()
	deadline = time.Now().Add(perTurnReplyBudget)
	for {
		env := composerReceive(t, h, deadline)
		if env.Type != protocol.TypeBackgroundTaskUpdated {
			continue
		}
		var p protocol.BackgroundTaskUpdatedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal("decode background completion")
		}
		if p.ConversationID != convID || p.TaskID != setup.taskID() {
			continue
		}
		status := p.Status
		if status == "" && p.Patch != "" {
			var patch struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(p.Patch), &patch); err != nil {
				t.Fatal("decode background status patch")
			}
			status = patch.Status
		}
		switch status {
		case "completed":
			t.Log("cancelled foreground turn; held background task completed after rig release")
			return
		case "failed", "stopped":
			t.Fatalf("background task ended with %q after Composer Stop", status)
		}
	}
}

// TestRealClaudeComposerStopClosedInputKillsHeldTask releases the foreground
// tool result only after closing stdin. The background FIFO stays held through
// normal Claude exit, so neither rig EOF nor parent cancellation can kill it.
func TestRealClaudeComposerStopClosedInputKillsHeldTask(t *testing.T) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("realclaude: claude not on PATH")
	}
	home := WithWorktreeAuthenticated(t)
	t.Logf("Composer Stop closed-input Claude version: %s", probeClaudeVersion(bin))
	workdir := filepath.Join(home, "composer-closed-input")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal("create closed-input workdir")
	}
	background := filepath.Join(workdir, "background.fifo")
	backgroundArrived, releaseBackground := tpcapHoldFIFO(t, background)
	defer releaseBackground()
	foregroundCommand, foregroundStarted, releaseForeground := composerForegroundGate(t, workdir)
	defer releaseForeground()
	ctx, cancel := context.WithTimeout(context.Background(), 2*perTurnReplyBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--model", "haiku", "--dangerously-skip-permissions")
	cmd.Dir = workdir
	// Match the supervisor's failure cleanup. Successful assertions below wait
	// for normal exit before cancelling, so the reaper cannot prove them for us.
	cmd.Cancel = func() error {
		agentrun.ReapDescendantGroups(cmd.Process.Pid, relayTestLogger())
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	recorder := newDropcapRecorder()
	cmd.Stdout = recorder
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("create closed-input pipe")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal("start closed-input claude")
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		_ = stdin.Close() // best-effort cleanup; successful runs already closed input
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("closed-input child failed to join")
		}
	}()
	if err := streamsup.WriteInitialize(stdin, "composer-closed-input"); err != nil {
		t.Fatal("write initialize")
	}
	prompt := fmt.Sprintf("First use Bash with run_in_background=true to run exactly: cat %s. Then use Bash in the foreground with timeout=120000 and run_in_background=false to run exactly: %s. Do not change either command. Wait only for the foreground result, then reply with one short word without checking or waiting for the background task. run=%d", background, foregroundCommand, time.Now().UnixNano())
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		t.Fatal("write closed-input turn")
	}
	composerRendezvous(t, backgroundArrived)
	composerFileRendezvous(t, foregroundStarted, done)
	deadline := time.Now().Add(10 * time.Second)
	for {
		lines, _ := recorder.snapshot()
		if composerHasBashCall(lines, background, true) && composerHasBashCommand(lines, foregroundCommand, false) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed-input calls did not match the rig's background/foreground inputs")
		}
		select {
		case <-done:
			t.Fatal("closed-input child exited during held-call setup")
		case <-time.After(20 * time.Millisecond):
		}
	}
	composerRequireReader(t, background, fifoLiveReaderPresent)
	if err := stdin.Close(); err != nil {
		t.Fatal("close input before held-result release")
	}
	// Let EOF reach Claude while the foreground call cannot produce its result.
	select {
	case <-done:
		t.Fatal("closed-input child exited before held-result release")
	case <-time.After(500 * time.Millisecond):
	}
	composerRequireReader(t, background, fifoLiveReaderPresent)
	releaseForeground()
	select {
	case <-done:
		if waitErr != nil || ctx.Err() != nil {
			t.Fatal("closed-input child failed instead of exiting normally")
		}
	case <-ctx.Done():
		t.Fatal("closed-input child did not exit after held-result release")
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		got := fifoLiveRead(background).Verdict
		if got == fifoLiveNoReader {
			break
		}
		if got != fifoLiveReaderPresent || time.Now().After(deadline) {
			t.Fatalf("closed-input background reader verdict=%s, want=%s", got, fifoLiveNoReader)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Log("closed input killed the held background task at foreground-result release")
}

func composerForegroundPrompt(fifo string) string {
	return fmt.Sprintf("Use Bash in the foreground with timeout=120000 and run_in_background=false to run exactly: cat %s. Do not add flags, redirections or other commands. Wait for its result. run=%d", fifo, time.Now().UnixNano())
}

func composerReceive(t *testing.T, h *perConvHarness, deadline time.Time) protocol.Envelope {
	t.Helper()
	env, ok := receiveEnvelope(t, h, time.Until(deadline))
	if !ok {
		t.Fatal("Composer Stop observation timed out")
	}
	if env.Type == protocol.TypeError || env.Type == protocol.TypeUnrecognizedMessage {
		t.Fatalf("Composer Stop received %s", env.Type)
	}
	return env
}

func composerRendezvous(t *testing.T, arrived <-chan struct{}) {
	t.Helper()
	select {
	case <-arrived:
	case <-time.After(perTurnReplyBudget):
		t.Fatal("Composer Stop rig FIFO never opened")
	}
}

func composerRequireReader(t *testing.T, fifo, want string) {
	t.Helper()
	if got := fifoLiveRead(fifo).Verdict; got != want {
		t.Fatalf("Composer Stop FIFO reader verdict=%s, want=%s", got, want)
	}
}

func composerHasBashCall(lines []dropcapCaptured, fifo string, background bool) bool {
	return composerHasBashCommand(lines, "cat "+fifo, background)
}

func composerHasBashCommand(lines []dropcapCaptured, command string, background bool) bool {
	for _, line := range lines {
		if line.Type != "assistant" {
			continue
		}
		var p struct {
			Message struct {
				Content []struct {
					Type, Name string
					Input      struct {
						Command         string `json:"command"`
						RunInBackground bool   `json:"run_in_background"`
					}
				}
			}
		}
		if err := json.Unmarshal(line.Raw, &p); err != nil {
			continue
		}
		for _, c := range p.Message.Content {
			if c.Type == "tool_use" && c.Name == "Bash" && c.Input.Command == command && c.Input.RunInBackground == background {
				return true
			}
		}
	}
	return false
}

// composerForegroundGate avoids Claude's foreground FIFO result stall. The
// arrival file witnesses execution; only the rig can create the release file.
func composerForegroundGate(t *testing.T, workdir string) (command, started string, release func()) {
	t.Helper()
	started = filepath.Join(workdir, "foreground-started")
	released := filepath.Join(workdir, "foreground-released")
	quote := func(path string) string { return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'" }
	command = fmt.Sprintf("printf ready > %s; while [ ! -f %s ]; do sleep 0.1; done", quote(started), quote(released))
	release = func() {
		if err := os.WriteFile(released, nil, 0o600); err != nil {
			t.Error("release foreground file gate")
		}
	}
	t.Cleanup(release)
	return command, started, release
}

func composerFileRendezvous(t *testing.T, started string, done <-chan struct{}) {
	t.Helper()
	deadline := time.NewTimer(perTurnReplyBudget)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(started); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal("inspect foreground arrival file")
		}
		select {
		case <-done:
			t.Fatal("child exited before foreground arrival")
		case <-deadline.C:
			t.Fatal("foreground file gate never opened")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestComposerHasBashCall(t *testing.T) {
	for _, tc := range []struct {
		name, raw        string
		background, want bool
	}{
		{"background rig call", `{"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /rig/fifo","run_in_background":true}}]}}`, true, true},
		{"foreground rig call", `{"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /rig/fifo","run_in_background":false}}]}}`, false, true},
		{"foreground is not background", `{"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /rig/fifo","run_in_background":false}}]}}`, true, false},
		{"background is not foreground", `{"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /rig/fifo","run_in_background":true}}]}}`, false, false},
		{"other fifo", `{"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /other/fifo","run_in_background":true}}]}}`, true, false},
		{"wrong tool", `{"message":{"content":[{"type":"tool_use","name":"Read","input":{"command":"cat /rig/fifo","run_in_background":true}}]}}`, true, false},
		{"text is not tool input", `{"message":{"content":[{"type":"text","name":"Bash","input":{"command":"cat /rig/fifo","run_in_background":true}}]}}`, true, false},
		{"malformed input", `{"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /rig/fifo","run_in_background":"true"}}]}}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := composerHasBashCall([]dropcapCaptured{{Type: "assistant", Raw: []byte(tc.raw)}}, "/rig/fifo", tc.background); got != tc.want {
				t.Fatalf("matched=%t, want=%t", got, tc.want)
			}
		})
	}
}

func TestComposerForegroundGate(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "space ' gate")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	command, started, release := composerForegroundGate(t, workdir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdout = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	composerFileRendezvous(t, started, done)
	select {
	case <-done:
		t.Fatal("foreground command exited before release")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	release() // Repeated release and cleanup must remain harmless.
	select {
	case <-done:
		if waitErr != nil || ctx.Err() != nil {
			t.Fatalf("foreground release did not produce normal exit: %v", waitErr)
		}
	case <-ctx.Done():
		t.Fatal("foreground command did not exit on release")
	}
}
