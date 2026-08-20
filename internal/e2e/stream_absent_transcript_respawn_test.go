//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestE2E_StreamNeverEstablishedSession_RespawnsWithCreateForm is #1631's
// end-to-end proof: a stream-json session that launched but never established a
// transcript recovers on its next respawn instead of crash-looping the daemon
// forever on a widening backoff.
//
// The defect it pins: streamsup's firstRun latch flips on cmd.Start succeeding,
// not on a transcript appearing, so a session that ran no turn looks established
// while <id>.jsonl does not exist. Every later respawn emits --resume <id>,
// claude answers "No conversation found with session ID", exits non-zero, and
// the daemon retries forever. #1630 landed the by-id probe that fixes it and
// left it inert; this ticket supplies the directory, so the probe now reads
// "absent" and the respawn carries --session-id <id>, which succeeds.
//
// The reachability driven here is the crash respawn — a SIGKILL of the live
// child, with no operator, no phone and no eviction involved — because that is
// the arm that needs nothing else in the system to be true.
func TestE2E_StreamNeverEstablishedSession_RespawnsWithCreateForm(t *testing.T) {
	const bootstrapUUID = "16310000-0000-4000-8000-000000000001"

	home := shortHome(t)

	// Build both binaries BEFORE the $HOME override below. Both shell out to
	// `go build`, and go resolves its module cache from $HOME, so a build under
	// the isolated temp home re-downloads the whole module graph (and fails
	// offline). Both are sync.Once-guarded per test process, so spawnWith's own
	// ensurePyryBuilt call is a no-op by the time it runs.
	fakeBin := ensureFakeClaudeBuilt(t)
	ensurePyryBuilt(t)

	// Re-derive the directory the daemon will probe, from the same two exported
	// functions its own helper composes (cmd/pyry's streamClaudeSessionsDir, which
	// is unexported and in another package — hence this deliberate coupling). The
	// derivation is what makes the fixture below the daemon's REAL probe target
	// rather than an arbitrary path that would agree with it by luck.
	//
	// sessions.DefaultClaudeSessionsDir reads $HOME, and the daemon's $HOME is the
	// isolated temp home, so point this process at the same one for the length of
	// the derivation. It cannot disturb the daemon: childEnv strips HOME from the
	// inherited environment and re-appends home unconditionally.
	t.Setenv("HOME", home)
	resolved, err := agentrun.ResolveWorkdir(home)
	if err != nil {
		t.Fatalf("resolve daemon workdir %s: %v", home, err)
	}
	probeDir := sessions.DefaultClaudeSessionsDir(resolved)
	if probeDir == "" {
		t.Fatalf("derived an empty claude sessions dir for workdir %s — the probe would be inert and this test vacuous", resolved)
	}
	// probeDir is deliberately NOT created. Its emptiness — specifically the
	// absence of <bootstrapUUID>.jsonl under it — IS the fixture, and it is
	// faithful rather than contrived: stream-mode fakeclaude opens no <uuid>.jsonl
	// anywhere, which is the never-established session this test is about.

	writeStreamInteractiveConfig(t, home)
	seedBootstrapRegistry(t, home, bootstrapUUID)

	socket, cmd, stdout, stderr, doneCh := spawnWith(t, home, spawnOpts{
		claudeBin:  fakeBin,
		claudeArgs: []string{},
		extraEnv: []string{
			"PYRY_FAKE_CLAUDE_STREAM_JSON=1",
			// Opt in to the refusal real claude gives a --resume against an absent
			// transcript. Off by default, so no other test in this tier sees it.
			"PYRY_FAKE_CLAUDE_REJECT_ABSENT_RESUME=" + probeDir,
		},
	})
	h := &Harness{
		SocketPath: socket,
		HomeDir:    home,
		PID:        cmd.Process.Pid,
		Stdout:     stdout,
		Stderr:     stderr,
		cmd:        cmd,
		doneCh:     doneCh,
	}
	t.Cleanup(func() { h.teardown(t) })
	if err := h.waitForReady(); err != nil {
		t.Fatalf("e2e: %v", err)
	}

	// Spawn 1 is --session-id on both trees (fresh latch, absent transcript), so
	// the fake accepts it either way and this step discriminates nothing on its
	// own — it establishes the pid the kill targets.
	first := waitForRunnerStatus(t, h, 20*time.Second, "first child running",
		func(s *control.StatusPayload) bool { return s.Phase == "running" && s.ChildPID != 0 })

	killChild(t, first.ChildPID)

	// Generous: under -race a kill→respawn has been measured near a second.
	waitForRunnerStatus(t, h, 20*time.Second, "the killed child's exit to be counted",
		func(s *control.StatusPayload) bool { return s.RestartCount >= 1 })

	// Settle, THEN re-read. This is what makes the pre-change tree red rather
	// than transiently green: streamsup reports "running" the moment cmd.Start
	// succeeds, so a child that is about to be refused is indistinguishable from a
	// healthy one at a single sample. Against the 500ms→1s→2s ladder four seconds
	// is several more refusals, so a looping daemon has a RestartCount well past 1
	// by the time this wakes.
	time.Sleep(4 * time.Second)
	settled := statusOrFatal(t, h)
	if settled.RestartCount != 1 {
		t.Errorf("RestartCount = %d after settling, want 1 — the respawned child is exiting repeatedly, i.e. still looping on --resume\nstatus: %+v\ndaemon stderr:\n%s",
			settled.RestartCount, settled, stderrTail(h, 6000))
	}
	if settled.Phase != "running" || settled.ChildPID == 0 {
		t.Errorf("Phase/ChildPID = %q/%d after settling, want running with a live child\nstatus: %+v\ndaemon stderr:\n%s",
			settled.Phase, settled.ChildPID, settled, stderrTail(h, 6000))
	}
	if settled.ChildPID == first.ChildPID {
		t.Errorf("ChildPID = %d, same as the killed child — the respawn never happened", settled.ChildPID)
	}

	// Argv evidence, CONTROL FIRST. streamsup's Run logs the composed argv at Info
	// ("spawning claude") and the harness daemon runs at Info, so the create form
	// must be present; without asserting that, the no---resume assertion below
	// would be green on a log that never carried argv at all.
	logs := h.Stderr.String()
	if create := "--session-id " + bootstrapUUID; !strings.Contains(logs, create) {
		t.Fatalf("daemon log carries no %q — the argv evidence is missing, so the assertion below would prove nothing\ndaemon stderr:\n%s",
			create, stderrTail(h, 6000))
	}
	if strings.Contains(logs, "--resume") {
		t.Errorf("daemon log carries --resume; spawn 1 is --session-id on any tree, so this is a post-kill respawn resuming a session with no transcript — the loop\ndaemon stderr:\n%s",
			stderrTail(h, 6000))
	}
}

// statusOrFatal reads one control-plane status snapshot, which for the daemon's
// primary session is the bootstrap runner's live State (buildStatus).
func statusOrFatal(t *testing.T, h *Harness) *control.StatusPayload {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := control.Status(ctx, h.SocketPath)
	if err != nil {
		t.Fatalf("control.Status: %v\ndaemon stderr:\n%s", err, stderrTail(h, 4000))
	}
	return st
}

// waitForRunnerStatus polls the control plane until want accepts a snapshot, and
// fails with the last one plus the daemon's stderr tail when the budget expires.
func waitForRunnerStatus(t *testing.T, h *Harness, budget time.Duration, what string,
	want func(*control.StatusPayload) bool) *control.StatusPayload {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last *control.StatusPayload
	for time.Now().Before(deadline) {
		last = statusOrFatal(t, h)
		if want(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("daemon never reached %s within %s; last status = %+v\ndaemon stderr:\n%s",
		what, budget, last, stderrTail(h, 6000))
	return nil
}

// killChild SIGKILLs the supervised claude child directly — the crash the daemon
// must recover from, delivered without touching the daemon itself.
func killChild(t *testing.T, pid int) {
	t.Helper()
	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find supervised child pid=%d: %v", pid, err)
	}
	if err := p.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL supervised child pid=%d: %v", pid, err)
	}
}
