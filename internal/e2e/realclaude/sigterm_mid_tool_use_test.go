//go:build e2e_realclaude

package realclaude

// Regression guard for the SIGTERM-mid-tool_use cleanup contract (#422):
// when pyry receives SIGTERM while real claude has a Bash subprocess
// in flight, three production invariants must hold:
//
//  1. Subprocess cleanup. The Bash subprocess (running `sleep 30`) is not
//     left behind as an orphan inside pyry's process group.
//  2. JSONL consistency. The on-disk session JSONL ends at a complete
//     envelope boundary — no half-written trailing line that a future
//     --continue would choke on.
//  3. Bounded exit window. pyry exits within 5s of SIGTERM (the
//     streamrunner.killGrace contract). A hang IS the regression being
//     guarded against.
//
// Terminal-shape branch: the architect picked branch B (clean stream
// truncation at a complete envelope boundary). The on-disk JSONL is the
// session-state file claude uses for --continue, not a stream-json result
// stream — there is no evidence claude flushes a structured trailer line
// to this file on signal. Assertion (5) below pins that shape: a Bash
// tool_use envelope is present but no matching tool_result envelope is
// written before SIGTERM tears claude down. If a future probe reveals
// branch A (a structured trailer line IS present), flip the tool_result
// assertion to "find a result envelope with subtype != success" — same
// surface, opposite sign — and record the observation in this comment.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sigtermSystemPrompt steers haiku toward a single Bash invocation so a
// tool_use is guaranteed in flight when the test sends SIGTERM. Mirrors
// the anti-chain wording in longSessionSystemPrompt.
const sigtermSystemPrompt = "You are an e2e regression-guard test. " +
	"When asked to run a shell command, use the Bash tool exactly once, " +
	"run the command verbatim, do NOT chain commands with && or ;, do NOT " +
	"comment, and do NOT do anything else."

// sigtermPrompt forces a single Bash call running `sleep 30`. The 30-second
// sleep is far longer than the time to detect the subprocess plus the 5s
// post-SIGTERM grace, so the Bash subprocess is still alive and its in-flight
// tool_use still open when the signal lands.
const sigtermPrompt = "Use the Bash tool to run `sleep 30`. Do nothing else."

// TestRealClaude_SigtermMidToolUse is the regression sensor described in
// #422. It is the only realclaude test that sends SIGTERM mid-run.
func TestRealClaude_SigtermMidToolUse(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	promptPath := filepath.Join(workdir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(sigtermPrompt), 0o600); err != nil {
		t.Fatalf("write %s: %v", promptPath, err)
	}
	systemPath := filepath.Join(workdir, "system.txt")
	if err := os.WriteFile(systemPath, []byte(sigtermSystemPrompt), 0o600); err != nil {
		t.Fatalf("write %s: %v", systemPath, err)
	}

	bin := ensurePyryBuilt(t)

	var stdout, stderr bytes.Buffer
	cmd := spawnPyryAgentRun(t, bin, workdir, promptPath, systemPath, &stdout, &stderr)

	// Setpgid: true above makes pgid == pid; the pgrep -g check below
	// walks this pgid to catch reparented orphans.
	pgid := cmd.Process.Pid

	// Defense-in-depth cleanup: if any assertion below trips before the
	// orphan check's salvage kill runs, this still reaps the group at
	// test end. ESRCH on success is harmless.
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})

	// Interrupt the moment claude's shell command is actually running,
	// not after a fixed wall-clock guess. Wait for the `sleep 30`
	// subprocess to appear in pyry's process group, then send the signal.
	// This holds no matter how fast or slow a given claude version starts.
	// A fixed timer does not. The 30s sleep keeps the subprocess in flight
	// long after detection, so SIGTERM still lands mid-tool_use. This uses
	// the same pgid + pgrep lookup as the orphan check below, so it
	// observes exactly the subprocess whose cleanup invariant #1 guards.
	if !waitForBashSubprocess(t, pgid, 25*time.Second) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Fatalf("claude never started the `sleep 30` subprocess within 25s, "+
			"cannot exercise SIGTERM mid-tool_use\nstderr:\n%s", truncate(stderr.Bytes()))
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to pyry (pid=%d): %v", cmd.Process.Pid, err)
	}

	// Bounded wait: the 5s budget is the production contract
	// (streamrunner.killGrace). Pyry's teardown is structurally bounded
	// by it: SIGTERM → ctx cancel → streamrunner forwards SIGTERM to
	// claude → cmd.Wait returns within killGrace (SIGKILL fallback via
	// WaitDelay). Deadline expiry IS the regression being guarded
	// against — a hang at this point is not a flake.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		// Drain Wait so the bytes.Buffer reads below are happens-after
		// the stdlib's internal forwarder writes; bounded so a wedged
		// child does not hang the suite.
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
		t.Fatalf("pyry did not exit within 5s of SIGTERM — hang regression "+
			"(streamrunner.killGrace contract)\nstderr:\n%s", truncate(stderr.Bytes()))
	}

	// Orphan check: process-group membership is preserved across
	// reparenting, so pgrep -g <pgid> catches a leaked Bash subprocess
	// even after pyry exits and the orphan reparents to init/launchd.
	// pgrep -P (direct children) would miss this case.
	orphans := processesInProcessGroup(t, pgid)
	if len(orphans) > 0 {
		// Salvage so the orphaned `sleep 30` does not linger past the
		// test run. Log-and-ignore on Kill error per spec.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Fatalf("orphaned subprocesses remain in pgid %d after pyry exit: %v\n"+
			"stderr:\n%s", pgid, orphans, truncate(stderr.Bytes()))
	}

	sessionID := parseInitSessionID(stdout.Bytes())
	if sessionID == "" {
		t.Fatalf("no system/init envelope found in pyry stdout, pyry may have "+
			"exited before claude emitted system/init\nstdout:\n%s",
			truncate(stdout.Bytes()))
	}

	jsonlPath := jsonlPathFor(workdir, sessionID)
	jsonlBytes, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsonlPath, err)
	}

	// No half-written line. ReadJSONL silently retains trailing partial
	// bytes (see internal/agentrun/jsonl/reader.go:188-262), so the only
	// way to surface a half-written tail is an explicit byte-tail check.
	if len(jsonlBytes) == 0 {
		t.Fatalf("jsonl %s is empty (claude wrote no events before SIGTERM)", jsonlPath)
	}
	if jsonlBytes[len(jsonlBytes)-1] != '\n' {
		lastNL := bytes.LastIndexByte(jsonlBytes, '\n')
		trailing := len(jsonlBytes) - lastNL - 1
		t.Fatalf("jsonl %s does not end with a newline — half-written "+
			"trailing line of %d bytes (last newline at index %d, file size %d)",
			jsonlPath, trailing, lastNL, len(jsonlBytes))
	}

	events := ReadJSONL(t, workdir, sessionID)

	// Bash tool_use present. Because we waited for the `sleep 30`
	// subprocess before signalling, a miss here means the tool_use line
	// was not flushed to the session file before SIGTERM truncated it,
	// not that the signal raced ahead of the tool call.
	var bashToolUseID string
	bashIdx := -1
	for i, e := range events {
		if e.Kind != "assistant" {
			continue
		}
		blocks, err := parseContentBlocks(e.Raw)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name == "Bash" && b.ID != "" {
				bashToolUseID = b.ID
				bashIdx = i
				break
			}
		}
		if bashToolUseID != "" {
			break
		}
	}
	if bashToolUseID == "" {
		t.Fatalf("the `sleep 30` subprocess was observed running but no Bash "+
			"tool_use envelope is in %s, the tool_use line was not flushed to "+
			"the session file before SIGTERM truncated it", jsonlPath)
	}

	// Matching tool_result absent (branch B). Failure here means the
	// Bash subprocess completed before SIGTERM tore claude down — the
	// test setup is broken (either the pre-SIGTERM sleep is too long or
	// the Bash command finished too fast, which is impossible for
	// `sleep 30` within an 8s total window).
	for _, e := range events[bashIdx+1:] {
		if e.Kind != "user" {
			continue
		}
		blocks, err := parseContentBlocks(e.Raw)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_result" && b.ToolUseID == bashToolUseID {
				t.Fatalf("matching tool_result on disk for Bash tool_use_id=%s in %s — "+
					"Bash completed before SIGTERM landed; reduce the pre-SIGTERM "+
					"sleep or extend the Bash command duration",
					bashToolUseID, jsonlPath)
			}
		}
	}
}

// spawnPyryAgentRun constructs and starts the same argv as RunPyryAgentRun
// but does NOT wait for completion — the caller needs PID access to send
// SIGTERM mid-run. Setpgid: true places pyry in its own process group so
// the orphan check can walk the group. Per the ticket's "Technical Notes"
// and resilience_test.go's precedent, this helper is file-local; promote
// to fixtures.go only when a second test needs the same shape.
func spawnPyryAgentRun(t *testing.T, bin, workdir, promptPath, systemPath string, stdoutBuf, stderrBuf *bytes.Buffer) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin,
		"agent-run",
		"--prompt-file="+promptPath,
		"--system-prompt-file="+systemPath,
		"--allowed-tools=Bash",
		// A generous ceiling, not a sync point. The test interrupts as
		// soon as the `sleep 30` subprocess appears, so this only has to
		// be high enough that claude reaches the single Bash call. A tight
		// cap of 2 was fine on older claude but newer versions spend a
		// turn or two before the tool call, so 2 hit the cap before Bash
		// ran. Keep it loose enough to bound a runaway, not so tight it
		// races claude's pacing.
		"--max-turns=6",
		"--effort=low",
		"--model=claude-haiku-4-5",
		"--workdir="+workdir,
		"--output-format=stream-json",
	)
	cmd.Env = os.Environ()
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawnPyryAgentRun: start %s: %v", bin, err)
	}
	return cmd
}

// waitForBashSubprocess blocks until claude's in-flight shell command (the
// `sleep 30` subprocess) actually appears inside pyry's process group, or
// until timeout. It replaces a fixed pre-SIGTERM sleep: the caller signals
// the moment the command is genuinely running, so the test holds regardless
// of how fast or slow a given claude version starts a tool call. pgrep exit
// code 1 (no match yet) is the normal not-yet case; the loop just retries.
func waitForBashSubprocess(t *testing.T, pgid int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("pgrep", "-g", strconv.Itoa(pgid), "sleep").Output()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// processesInProcessGroup runs `pgrep -g <pgid>`, drops the pgid itself
// (defense against a transient kernel state where pyry's zombie pid is
// briefly visible), and returns the remaining PIDs. pgrep exit code 1
// with no output means no matches — the success case. Any other failure
// is structural and calls t.Fatalf.
func processesInProcessGroup(t *testing.T, pgid int) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-g", strconv.Itoa(pgid)).Output()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok && exitErr.ExitCode() == 1 {
			return nil
		}
		t.Fatalf("processesInProcessGroup: pgrep -g %d: %v (pgrep -g is "+
			"required for this test on macOS + Linux; install procps-ng on "+
			"Linux or rely on the stock pgrep on macOS)", pgid, err)
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		if pid == pgid {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}
