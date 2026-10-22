//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	backgroundPrompt := rafcapPrompt(background, daemonRecordingNonce(t, time.Now().UnixNano()))
	declarePhysicalRecording(t, h.home, "composer-background", 1, backgroundPrompt)
	source := scenario.NewSourceObserver(t, h.home)
	source.Watch(convID)
	poll := scenario.PollingReceive(t, h.phone, h.initRecv)
	next := func(deadline time.Time) protocol.Envelope {
		env, ok := source.Next(poll, deadline)
		if !ok {
			t.Fatal("composer source/live deadline")
		}
		if env.Type == protocol.TypeError || env.Type == protocol.TypeUnrecognizedMessage {
			t.Fatal("composer live alarm")
		}
		return env
	}
	sealSendMessage(t, h.phone, h.initSend, 27750, convID, "composer-background", backgroundPrompt)
	var setup stopHeldTaskSetup
	ended := false
	source.OnEntry = func(entry history.Entry) {
		if err := setup.observeSource(entry, convID, background); err != nil {
			t.Fatal(err)
		}
		if entry.Type == protocol.TypeTurnEnd {
			var p protocol.TurnEndPayload
			if json.Unmarshal(entry.Payload, &p) != nil || p.StopReason != "end_turn" {
				t.Fatal("setup source reason")
			}
			ended = true
		}
	}
	deadline := time.Now().Add(perTurnReplyBudget)
	for setup.taskID() == "" || !ended {
		env := next(deadline)
		if err := setup.observe(env, convID, background); err != nil {
			t.Fatalf("background setup: %v; %s", err, setup.diagnostic())
		}

	}
	composerRendezvous(t, arrived)
	composerRequireReader(t, background, fifoLiveReaderPresent)
	foregroundCommand, foregroundStarted, releaseForeground := composerForegroundGate(t, h.workdir)
	defer releaseForeground()
	foregroundPrompt := composerForegroundPrompt(foregroundCommand, daemonRecordingNonce(t, time.Now().UnixNano()))
	declarePhysicalRecording(t, h.home, "composer-foreground", 2, foregroundPrompt)
	source.Watch(convID)
	heldCall := false
	source.OnEntry = func(entry history.Entry) {
		if entry.Type == protocol.TypeTurnEnd {
			t.Fatal("foreground ended before stop")
		}
		if entry.Type != protocol.TypeToolUse {
			return
		}
		var p protocol.ToolUsePayload
		if json.Unmarshal(entry.Payload, &p) != nil {
			t.Fatal("invalid foreground source")
		}
		heldCall = p.ConversationID == convID && p.ParentToolUseID == "" && p.Name == "Bash" && p.Input["command"] == foregroundCommand && p.Input["run_in_background"] != "true"
	}
	sealSendMessage(t, h.phone, h.initSend, 27751, convID, "composer-foreground", foregroundPrompt)
	deadline = time.Now().Add(perTurnReplyBudget)
	for !heldCall {
		next(deadline)
	}

	// The exact tool input above establishes identity, and the arrival file
	// witnesses actual shell execution. The command can finish only when the
	// rig creates its release file. Confirm its process is still live before Stop.
	composerFileRendezvous(t, foregroundStarted, nil)
	if err := composerForegroundHeld(foregroundStarted); err != nil {
		t.Fatalf("foreground not held before Composer Stop: %v", err)
	}
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: 27752, Type: protocol.TypeInterrupt, TS: time.Now().UTC()})
	cancelled := false
	source.OnEntry = func(entry history.Entry) {
		if entry.Type == protocol.TypeTurnEnd {
			var p protocol.TurnEndPayload
			if json.Unmarshal(entry.Payload, &p) != nil || p.StopReason != "cancelled" {
				t.Fatal("foreground cancellation reason")
			}
			cancelled = true
		}
	}
	deadline = time.Now().Add(perTurnReplyBudget)
	for !cancelled {
		next(deadline)
	}
	composerRequireReader(t, background, fifoLiveReaderPresent)
	release()
	deadline = time.Now().Add(perTurnReplyBudget)
	completed := false
	source.OnEntry = func(entry history.Entry) {
		if entry.Type != protocol.TypeBackgroundTaskUpdated {
			return
		}
		var p protocol.BackgroundTaskUpdatedPayload
		if err := json.Unmarshal(entry.Payload, &p); err != nil {
			t.Fatal("decode background completion")
		}
		if p.ConversationID != convID || p.TaskID != setup.taskID() {
			return
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
			completed = true
		case "failed", "stopped":
			t.Fatalf("background task ended with %q after Composer Stop", status)
		}
	}
	for !completed {
		next(deadline)
	}
	t.Log("cancelled foreground turn; held background task completed after rig release")
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

func composerForegroundPrompt(command string, nonce int64) string {
	return fmt.Sprintf("Use Bash in the foreground with timeout=120000 and run_in_background=false to run exactly: %s. Do not change the command. Wait for its result. run=%d", command, nonce)
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
	command = fmt.Sprintf(`printf '%%s\n' "$$" > %s; while [ ! -f %s ]; do sleep 0.1; done`, quote(started), quote(released))
	release = func() {
		if err := os.WriteFile(released, nil, 0o600); err != nil {
			t.Error("release foreground file gate")
		}
	}
	t.Cleanup(release)
	return command, started, release
}

// composerForegroundHeld rejects a released gate or a shell that exited after
// writing its arrival file. The exact command is matched on the client wire.
func composerForegroundHeld(started string) error {
	if _, err := os.Stat(filepath.Join(filepath.Dir(started), "foreground-released")); !os.IsNotExist(err) {
		return fmt.Errorf("foreground release is present or unreadable: %v", err)
	}
	raw, err := os.ReadFile(started)
	if err != nil {
		return fmt.Errorf("read foreground process: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("foreground arrival has no valid process id")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return fmt.Errorf("foreground shell is not live: %w", err)
	}
	status, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		return fmt.Errorf("inspect foreground shell with ps: %w", err)
	}
	if strings.TrimSpace(string(status)) == "" || strings.HasPrefix(strings.TrimSpace(string(status)), "Z") {
		return fmt.Errorf("foreground shell has empty or zombie status")
	}
	return nil
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
	raw, err := os.ReadFile(started)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != strconv.Itoa(cmd.Process.Pid) {
		t.Fatal("foreground arrival does not identify the held shell")
	}
	if err := composerForegroundHeld(started); err != nil {
		t.Fatal(err)
	}
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

func TestComposerForegroundGateDetectsPrematureExit(t *testing.T) {
	command, started, _ := composerForegroundGate(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait() // The shell is deliberately killed before rig release.
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	composerFileRendezvous(t, started, done)
	if err := composerForegroundHeld(started); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	if err := composerForegroundHeld(started); err == nil {
		t.Fatal("arrival from an exited foreground shell counted as held")
	}
}

func TestComposerForegroundHeldInspectionError(t *testing.T) {
	started := filepath.Join(t.TempDir(), "foreground-started")
	if err := os.WriteFile(started, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	err := composerForegroundHeld(started)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("inspection lost the ps execution cause: %v", err)
	}
	if !strings.Contains(err.Error(), "inspect foreground shell with ps") {
		t.Fatalf("inspection error has no operation context: %v", err)
	}
}
