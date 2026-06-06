//go:build e2e_realclaude

package realclaude

// Regression guard for the SIGTERM-mid-tool_use cleanup contract (#422):
// when pyry receives SIGTERM while real claude has a Bash subprocess
// in flight, these production invariants must hold:
//
//  1. Direct-child cleanup. pyry reaps the claude process it spawned — no
//     leftover claude after pyry exits. (claude's OWN Bash subprocesses run
//     in a separate descendant process group that pyry does not reap on
//     SIGTERM; that gap is tracked by #565 and is out of scope here. The
//     test reaps the leaked subprocess itself so it leaves nothing behind.)
//  2. JSONL consistency. The on-disk session JSONL ends at a complete
//     envelope boundary — no half-written trailing line that a future
//     --continue would choke on.
//  3. Bounded exit window. pyry exits within 5s of SIGTERM. A hang IS the
//     regression being guarded against.
//  4. SIGTERM landed mid-tool_use. A Bash tool_use envelope is on disk but
//     no matching tool_result was written before SIGTERM tore claude down.
//
// In-flight command (claude 2.1.158). The fixture runs `tail -f /dev/null`,
// a command that blocks forever as a single, pgrep-able process. An earlier
// `sleep 30` no longer works: claude 2.1.158's Bash tool refuses a standalone
// sleep ("Blocked: standalone sleep 30 ...") so no subprocess ever spawns
// (#563). run_in_background is also wrong — it returns immediately, so a
// tool_result lands and invariant 4 cannot hold. `tail -f /dev/null` keeps
// the tool_use open until SIGTERM, guaranteeing no tool_result with no
// timing race.
//
// Event-driven SIGTERM timing. The test does not guess when to signal. It
// waits for two real events: (a) the `tail` subprocess appears as a
// descendant of pyry, confirming the command is genuinely executing; (b) the
// Bash tool_use envelope is flushed to claude's on-disk session file (it lags
// the subprocess by a couple of seconds). Only then does it SIGTERM, so
// invariant 4's "tool_use present" precondition holds regardless of how fast
// or slow a given claude version is.
//
// Subprocess detection (claude 2.1.158). claude runs every Bash command in
// its own process group two levels below pyry, so `pgrep -g <pyry-pgid>`
// cannot see it. waitForBashSubprocess walks the process tree by parent
// instead, and returns the subprocess's process group so the test can reap
// the #565 leak in cleanup.
//
// Terminal-shape branch: the architect picked branch B (clean stream
// truncation at a complete envelope boundary). The on-disk JSONL is the
// session-state file claude uses for --continue, not a stream-json result
// stream — there is no evidence claude flushes a structured trailer line
// to this file on signal. Invariant 4 pins that shape: a Bash tool_use
// envelope is present but no matching tool_result envelope is written before
// SIGTERM tears claude down. If a future probe reveals branch A (a structured
// trailer line IS present), flip the tool_result assertion to "find a result
// envelope with subtype != success" — same surface, opposite sign — and
// record the observation in this comment.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// sigtermProcessName is the leaf process the fixture command spawns. The
// process-tree walk matches a descendant of pyry by this base name.
const sigtermProcessName = "tail"

// sigtermPrompt forces a single Bash call running `tail -f /dev/null`, a
// command that blocks forever as one `tail` process. It stays in flight (no
// tool_result) until SIGTERM lands, however long detection takes, so
// invariant 4 holds with no timing race. See the file header for why neither
// `sleep 30` nor run_in_background works on claude 2.1.158.
const sigtermPrompt = "Use the Bash tool to run `tail -f /dev/null`. Do nothing else."

// syncBuffer is a goroutine-safe bytes.Buffer. os/exec writes the child's
// stdout/stderr from a copier goroutine, and this test reads them while the
// child is still running (to learn the session id and surface stderr on
// mid-run fatals). A plain bytes.Buffer would be a data race under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// Bytes returns a copy so callers never read the underlying array while the
// copier goroutine mutates it.
func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buf.Bytes()
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

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

	var stdout, stderr syncBuffer
	cmd := spawnPyryAgentRun(t, bin, workdir, promptPath, systemPath, &stdout, &stderr)

	// Setpgid: true above makes pyry its own process-group leader (pgid ==
	// pid). pyryPid roots the process tree the detection walk descends.
	pyryPid := cmd.Process.Pid

	// Defense-in-depth cleanup: if any assertion below trips before the
	// salvage kills run, this still reaps pyry's own group at test end.
	// ESRCH on success is harmless.
	t.Cleanup(func() {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
	})

	// claude is pyry's single direct child; capture its pid so invariant 1
	// can confirm pyry reaps it after SIGTERM.
	claudePid := waitForDirectChild(t, pyryPid, 25*time.Second)
	if claudePid == 0 {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("pyry never spawned a claude child within 25s\nstderr:\n%s",
			truncate(stderr.Bytes()))
	}

	// Event (a): wait for the `tail -f /dev/null` subprocess to appear as a
	// descendant of pyry. claude 2.1.158 runs the command in its own
	// descendant process group, so the test walks the process tree (not
	// pyry's group) to find it, and records that group so the #565 orphan
	// can be reaped.
	bashPGID, found := waitForBashSubprocess(t, pyryPid, sigtermProcessName, 25*time.Second)
	if !found {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("claude never started the `tail -f /dev/null` subprocess within 25s, "+
			"cannot exercise SIGTERM mid-tool_use\nstderr:\n%s", truncate(stderr.Bytes()))
	}
	// Reap claude's leaked Bash subprocess (#565) at test end. pyry does not
	// kill claude's descendant group on SIGTERM, so without this the
	// `tail -f /dev/null` would linger past the run as an orphan.
	t.Cleanup(func() {
		_ = syscall.Kill(-bashPGID, syscall.SIGKILL)
	})

	// Event (b): wait until claude has flushed the Bash tool_use to its
	// on-disk session file. It lands a couple of seconds after the subprocess
	// starts; SIGTERM before the flush truncates the session file without it,
	// and invariant 4's "tool_use present" check needs it. `tail -f /dev/null`
	// never returns, so no tool_result is ever written, however long this
	// wait takes.
	sessionID := waitForSessionID(&stdout, 10*time.Second)
	if sessionID == "" {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("no system/init session_id on pyry stdout within 10s\nstderr:\n%s",
			truncate(stderr.Bytes()))
	}
	if !waitForBashToolUseOnDisk(t, workdir, sessionID, 15*time.Second) {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("claude never flushed the Bash tool_use to the session file within 15s "+
			"(session_id=%s); cannot assert SIGTERM landed mid-tool_use\nstderr:\n%s",
			sessionID, truncate(stderr.Bytes()))
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to pyry (pid=%d): %v", pyryPid, err)
	}

	// Bounded wait: the 5s budget is the production teardown contract.
	// Pyry's teardown is structurally bounded: SIGTERM → ctx cancel →
	// runner forwards SIGTERM to claude → cmd.Wait returns within the kill
	// grace (SIGKILL fallback via WaitDelay). Deadline expiry IS the
	// regression being guarded against — a hang at this point is not a flake.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		// Drain Wait so the buffer reads below are happens-after the
		// stdlib's internal forwarder writes; bounded so a wedged child
		// does not hang the suite.
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
		t.Fatalf("pyry did not exit within 5s of SIGTERM — hang regression "+
			"(teardown kill-grace contract)\nstderr:\n%s", truncate(stderr.Bytes()))
	}

	// Invariant 1: pyry reaps the claude process it spawned. claude's own
	// Bash subprocess runs in a separate descendant process group that pyry
	// does not reap on SIGTERM (#565); that gap is out of scope here and is
	// salvaged by the bashPGID cleanup registered above.
	if !waitForProcessGone(claudePid, 2*time.Second) {
		t.Fatalf("claude (pid=%d, pyry's direct child) still alive after pyry exit — "+
			"pyry did not reap the process it spawned\nstderr:\n%s",
			claudePid, truncate(stderr.Bytes()))
	}

	jsonlPath := jsonlPathFor(workdir, sessionID)
	jsonlBytes, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsonlPath, err)
	}

	// Invariant 2: no half-written line. ReadJSONL silently retains trailing
	// partial bytes (see internal/agentrun/jsonl/reader.go:188-262), so the
	// only way to surface a half-written tail is an explicit byte-tail check.
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

	// Invariant 4a: Bash tool_use present. We waited for it on disk above, so
	// a miss here means claude rewrote or truncated the session file during
	// teardown — not the expected branch-B shape.
	bashToolUseID, bashIdx := findBashToolUse(events)
	if bashToolUseID == "" {
		t.Fatalf("the Bash tool_use was flushed to %s before SIGTERM but is absent "+
			"after pyry exit — the session file was rewritten or truncated during "+
			"teardown", jsonlPath)
	}

	// Invariant 4b: matching tool_result absent (branch B). Failure here means
	// the Bash subprocess produced a result before SIGTERM tore claude down —
	// impossible for `tail -f /dev/null`, a command that never returns. If it
	// ever trips, the fixture command stopped blocking.
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
					"Bash returned before SIGTERM landed; the fixture command "+
					"(`tail -f /dev/null`) is supposed to block forever",
					bashToolUseID, jsonlPath)
			}
		}
	}
}

// spawnPyryAgentRun constructs and starts the same argv as RunPyryAgentRun
// but does NOT wait for completion — the caller needs PID access to send
// SIGTERM mid-run. Setpgid: true places pyry in its own process group so
// the defense-in-depth cleanup can reap it. Per the ticket's "Technical
// Notes" and resilience_test.go's precedent, this helper is file-local;
// promote to fixtures.go only when a second test needs the same shape.
func spawnPyryAgentRun(t *testing.T, bin, workdir, promptPath, systemPath string, stdoutBuf, stderrBuf *syncBuffer) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin,
		"agent-run",
		"--prompt-file="+promptPath,
		"--system-prompt-file="+systemPath,
		"--allowed-tools=Bash",
		// A generous ceiling, not a sync point. The test interrupts as
		// soon as the `tail -f /dev/null` subprocess appears, so this only
		// has to be high enough that claude reaches the single Bash call. A
		// tight cap of 2 was fine on older claude but newer versions spend a
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

// waitForDirectChild blocks until pyry has spawned its claude child (pyry's
// single direct child) or until timeout, returning the child's pid (0 on
// timeout). pgrep -P lists direct children only.
func waitForDirectChild(t *testing.T, pyryPid int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("pgrep", "-P", strconv.Itoa(pyryPid)).Output()
		if err == nil {
			for _, f := range strings.Fields(string(out)) {
				if pid, convErr := strconv.Atoi(f); convErr == nil {
					return pid
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0
}

// waitForBashSubprocess blocks until claude's in-flight shell command (the
// leaf process whose base name is `name`, e.g. "tail") appears as a
// DESCENDANT of pyry, or until timeout. It replaces a fixed pre-SIGTERM
// sleep: the caller signals the moment the command is genuinely running.
// claude 2.1.158 runs each Bash command in its own process group two levels
// below pyry, so a `pgrep -g <pyry-pgid>` cannot see it; this walks the
// process tree by parent instead. Returns the subprocess's process group id
// (so the caller can reap the #565 leak) and whether it was found.
func waitForBashSubprocess(t *testing.T, pyryPid int, name string, timeout time.Duration) (int, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, pid := range descendantsOf(pyryPid) {
			if leafCommandName(pid) == name {
				if pgid := pgidOf(pid); pgid > 0 {
					return pgid, true
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, false
}

// descendantsOf returns every transitive child pid of root via a breadth-
// first walk over `pgrep -P`. pgrep exit code 1 (no children) is the normal
// leaf case and is skipped silently. Bounded by the live process tree, which
// is shallow here (pyry → claude → shell → command).
func descendantsOf(root int) []int {
	var all []int
	queue := []int{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		out, err := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(out)) {
			if pid, convErr := strconv.Atoi(f); convErr == nil {
				all = append(all, pid)
				queue = append(queue, pid)
			}
		}
	}
	return all
}

// leafCommandName returns the base name of a process's executable (argv[0]
// with any directory stripped), e.g. "tail" for `tail -f /dev/null` and
// "zsh" for `/bin/zsh -c ...`. Used to pick the leaf Bash command out of the
// process tree, not the shell wrapper that runs it. Empty on lookup failure.
func leafCommandName(pid int) string {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// pgidOf returns a process's process-group id, or 0 on lookup failure.
func pgidOf(pid int) int {
	out, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return pgid
}

// waitForProcessGone returns true once pid is no longer alive, polling up to
// timeout. syscall.Kill with signal 0 probes liveness without delivering a
// signal: a non-nil error (ESRCH) means the process is gone.
func waitForProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForSessionID polls pyry's stdout for the system/init session_id, up to
// timeout. Returns "" if none appears.
func waitForSessionID(stdout *syncBuffer, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if id := parseInitSessionID(stdout.Bytes()); id != "" {
			return id
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
}

// waitForBashToolUseOnDisk polls claude's on-disk session JSONL until the
// Bash tool_use envelope is flushed (it lags the subprocess by a couple of
// seconds), or until timeout. Uses the same parse as the final assertion so
// the wait and the assertion agree.
func waitForBashToolUseOnDisk(t *testing.T, workdir, sessionID string, timeout time.Duration) bool {
	t.Helper()
	path := jsonlPathFor(workdir, sessionID)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			if id, _ := findBashToolUse(ReadJSONL(t, workdir, sessionID)); id != "" {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// findBashToolUse returns the id and event index of the first Bash tool_use
// in events, or ("", -1) if none is present.
func findBashToolUse(events []JSONLEntry) (string, int) {
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
				return b.ID, i
			}
		}
	}
	return "", -1
}
