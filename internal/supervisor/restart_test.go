package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// helperArgsAfterDoubleDash returns the tokens following the first "--" in the
// child's os.Args — the args the supervisor appended after the fixed
// -test.run=TestHelperProcess prefix. Used by the record_args helper mode.
func helperArgsAfterDoubleDash() []string {
	for i, a := range os.Args {
		if a == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

// recorderArgs builds a ClaudeArgs value that re-execs the test binary as the
// record_args helper, appending tail after the "--" separator so the child
// records exactly tail as its spawn argv.
func recorderArgs(tail ...string) []string {
	return append([]string{"-test.run=TestHelperProcess", "--"}, tail...)
}

// recorderConfig builds a Config whose child records each spawn's tail argv to
// argsFile (one line per spawn). exitAfter=true makes the child exit
// immediately after recording (exercises the backoff paths); false makes it
// block until killed (the swap / coalescing paths, where the child must stay
// alive to be restarted). initial is the first spawn's tail argv; keep it
// non-empty so spawns are countable. Bridge mode is used (no os.Stdin / raw
// terminal) to match the sessions recorder harness.
func recorderConfig(argsFile string, exitAfter bool, initial ...string) Config {
	cfg := helperConfig("record_args", "GO_TEST_HELPER_ARGS_FILE="+argsFile)
	if exitAfter {
		cfg.helperEnv = append(cfg.helperEnv, "GO_TEST_HELPER_EXIT=1")
	}
	cfg.ClaudeArgs = recorderArgs(initial...)
	cfg.Bridge = NewBridge(cfg.Logger)
	return cfg
}

// recordedArgs reads argsFile and returns one entry per spawn: the tail argv
// that spawn recorded. Safe to call while the child blocks — each spawn writes
// its line synchronously before blocking, and short appends are atomic.
func recordedArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read args file: %v", err)
	}
	s := strings.TrimRight(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// waitForSpawns polls argsFile until at least n spawn lines are present, then
// returns them. Fails the test on timeout.
func waitForSpawns(t *testing.T, path string, n int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got := recordedArgs(t, path)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("wanted >=%d spawns in %v, got %d: %v", n, timeout, len(got), got)
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runSupInBackground runs sup.Run on a goroutine and registers a cleanup that
// cancels the ctx and waits for Run to return.
func runSupInBackground(t *testing.T, sup *Supervisor) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not exit within 10s after cancel")
		}
	})
}

// bigBackoffConfig makes the backoff long enough that a "backoff skipped /
// interrupted" relaunch is unambiguously distinguishable from a "waited out the
// backoff" one.
func bigBackoffConfig(cfg Config) Config {
	cfg.BackoffInitial = 10 * time.Second
	cfg.BackoffMax = 10 * time.Second
	cfg.BackoffReset = time.Minute
	return cfg
}

// TestSupervisor_Run_ArgsByteIdenticalWithoutRestart is the regression guard for
// the liveArgs/iterCtx refactor: with Restart never called, the child spawns
// with exactly Config.ClaudeArgs (liveArgs returns the cfg clone unchanged).
func TestSupervisor_Run_ArgsByteIdenticalWithoutRestart(t *testing.T) {
	t.Parallel()
	argsFile := filepath.Join(t.TempDir(), "args")
	sup, err := New(recorderConfig(argsFile, false, "baseline"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runSupInBackground(t, sup)

	got := waitForSpawns(t, argsFile, 1, 5*time.Second)
	if !reflect.DeepEqual(got, []string{"baseline"}) {
		t.Errorf("recorded spawns = %v, want [baseline] (byte-identical, no restart)", got)
	}
}

// TestSupervisor_Run_IgnoresSessionIDField is the AC-2 behaviour guard for #1108:
// the PTY supervisor DELIBERATELY IGNORES supervisor.Config.SessionID. With
// SessionID and ResolveSessionID set to DIFFERENT ids, the spawned child's argv
// carries the id ResolveSessionID resolved (--session-id <resolved>) and never
// the eager SessionID value — the byte-identical rollback guarantee, proven even
// when the two disagree. This is the belt-and-suspenders against a future edit
// accidentally wiring SessionID into the arg path; the structural half is
// compile-enforced (buildClaudeArgs takes no SessionID parameter, so the field
// cannot reach the arg builder).
func TestSupervisor_Run_IgnoresSessionIDField(t *testing.T) {
	t.Parallel()
	const (
		constructedID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc" // the ignored eager SessionID
		resolvedID    = "11111111-1111-4111-8111-111111111111" // what ResolveSessionID returns
	)
	argsFile := filepath.Join(t.TempDir(), "args")
	cfg := recorderConfig(argsFile, false)
	cfg.SessionID = constructedID
	cfg.ResolveSessionID = func() (string, bool) { return resolvedID, false }
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runSupInBackground(t, sup)

	got := waitForSpawns(t, argsFile, 1, 5*time.Second)
	if want := []string{"--session-id " + resolvedID}; !reflect.DeepEqual(got, want) {
		t.Errorf("recorded spawns = %v, want %v (PTY resolves via ResolveSessionID, ignores SessionID)", got, want)
	}
	if strings.Contains(got[0], constructedID) {
		t.Errorf("child argv %q leaked the ignored SessionID %q", got[0], constructedID)
	}
}

// TestSupervisor_Run_ResumeBitEmitsResumeFlag (#1164): a ResolveSessionID that
// reports resume=true makes the supervisor spawn "--resume <id>" (reattach to an
// existing transcript) and never "--session-id". This is the daemon-restart path
// that previously crash-looped — claude refuses --session-id when the pinned id's
// transcript already exists on disk, and the PTY buildClaudeArgs had no --resume
// branch to escape to. Proves the supervisor honours the resume bit end-to-end.
func TestSupervisor_Run_ResumeBitEmitsResumeFlag(t *testing.T) {
	t.Parallel()
	const resolvedID = "11111111-1111-4111-8111-111111111111"
	argsFile := filepath.Join(t.TempDir(), "args")
	cfg := recorderConfig(argsFile, false)
	cfg.ResolveSessionID = func() (string, bool) { return resolvedID, true }
	sup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runSupInBackground(t, sup)

	got := waitForSpawns(t, argsFile, 1, 5*time.Second)
	if want := []string{"--resume " + resolvedID}; !reflect.DeepEqual(got, want) {
		t.Errorf("recorded spawns = %v, want %v (resume bit → --resume)", got, want)
	}
	if strings.Contains(got[0], "--session-id") {
		t.Errorf("child argv %q used --session-id despite resume=true", got[0])
	}
}

// TestSupervisor_Restart_SwapsArgvAndSkipsBackoff (core mechanism): Restart on a
// running child swaps the spawn argv, kills the child, and relaunches promptly
// with the new args — skipping the (deliberately long) crash backoff.
func TestSupervisor_Restart_SwapsArgvAndSkipsBackoff(t *testing.T) {
	t.Parallel()
	argsFile := filepath.Join(t.TempDir(), "args")
	sup, err := New(bigBackoffConfig(recorderConfig(argsFile, false, "gen0")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runSupInBackground(t, sup)

	waitForSpawns(t, argsFile, 1, 5*time.Second)
	pid0 := sup.State().ChildPID
	if pid0 == 0 {
		t.Fatal("first child never reported a PID")
	}

	start := time.Now()
	sup.Restart(recorderArgs("gen1"))
	got := waitForSpawns(t, argsFile, 2, 5*time.Second)
	elapsed := time.Since(start)

	if got[1] != "gen1" {
		t.Errorf("second spawn argv = %q, want %q", got[1], "gen1")
	}
	if elapsed > 3*time.Second {
		t.Errorf("relaunch took %v with a 10s backoff — deliberate restart did not skip backoff", elapsed)
	}
	if !waitFor(t, 3*time.Second, func() bool { return sup.State().ChildPID != 0 && sup.State().ChildPID != pid0 }) {
		t.Errorf("child PID did not change after restart (was %d, now %d)", pid0, sup.State().ChildPID)
	}
}

// TestSupervisor_Restart_NoLiveChildSwapsOnly: Restart before Run (no live
// child) only swaps the args; the first spawn then uses them. No panic.
func TestSupervisor_Restart_NoLiveChildSwapsOnly(t *testing.T) {
	t.Parallel()
	argsFile := filepath.Join(t.TempDir(), "args")
	sup, err := New(recorderConfig(argsFile, false, "gen0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// No child running yet — swap only.
	sup.Restart(recorderArgs("swapped"))

	runSupInBackground(t, sup)
	got := waitForSpawns(t, argsFile, 1, 5*time.Second)
	if got[0] != "swapped" {
		t.Errorf("first spawn argv = %q, want %q (swap-only must apply on next spawn)", got[0], "swapped")
	}
}

// TestSupervisor_Restart_DuringBackoffInterrupts: a Restart arriving while the
// supervisor is waiting out a (long) crash backoff breaks the wait and
// relaunches promptly with the new args.
func TestSupervisor_Restart_DuringBackoffInterrupts(t *testing.T) {
	t.Parallel()
	argsFile := filepath.Join(t.TempDir(), "args")
	// exitAfter: the child records then exits, so the supervisor is in backoff
	// when we call Restart.
	sup, err := New(bigBackoffConfig(recorderConfig(argsFile, true, "gen0")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runSupInBackground(t, sup)

	waitForSpawns(t, argsFile, 1, 5*time.Second) // gen0 recorded, child exited → backoff

	start := time.Now()
	sup.Restart(recorderArgs("gen1"))
	got := waitForSpawns(t, argsFile, 2, 5*time.Second)
	elapsed := time.Since(start)

	if got[1] != "gen1" {
		t.Errorf("relaunch argv = %q, want %q", got[1], "gen1")
	}
	if elapsed > 3*time.Second {
		t.Errorf("relaunch took %v with a 10s backoff — Restart did not interrupt the backoff wait", elapsed)
	}
}

// TestSupervisor_Run_ShutdownReturnsContextError guards the moved shutdown
// check: cancelling the parent ctx while a child runs returns a context error
// and does NOT relaunch (a parent cancel is never mistaken for a restart kill).
func TestSupervisor_Run_ShutdownReturnsContextError(t *testing.T) {
	t.Parallel()
	argsFile := filepath.Join(t.TempDir(), "args")
	sup, err := New(recorderConfig(argsFile, false, "gen0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	waitForSpawns(t, argsFile, 1, 5*time.Second)
	cancel()

	select {
	case err := <-done:
		if !isContextErr(err) {
			t.Errorf("Run returned %v, want a context error on shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of parent cancel")
	}

	// A shutdown must not have relaunched: exactly the one spawn.
	if got := recordedArgs(t, argsFile); len(got) != 1 {
		t.Errorf("recorded %d spawns after shutdown, want 1 (no relaunch): %v", len(got), got)
	}
}

// TestSupervisor_Restart_Coalesces: two rapid Restart calls collapse to a single
// relaunch that uses the last args.
func TestSupervisor_Restart_Coalesces(t *testing.T) {
	t.Parallel()
	argsFile := filepath.Join(t.TempDir(), "args")
	sup, err := New(bigBackoffConfig(recorderConfig(argsFile, false, "gen0")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runSupInBackground(t, sup)

	waitForSpawns(t, argsFile, 1, 5*time.Second)

	sup.Restart(recorderArgs("gen1"))
	sup.Restart(recorderArgs("gen2"))

	got := waitForSpawns(t, argsFile, 2, 5*time.Second)
	if got[len(got)-1] != "gen2" {
		t.Errorf("final spawn argv = %q, want %q (last restart wins)", got[len(got)-1], "gen2")
	}

	// Give a spurious extra relaunch time to appear, then assert exactly one
	// relaunch happened (gen0 + one, not gen0 + two).
	time.Sleep(500 * time.Millisecond)
	if got := recordedArgs(t, argsFile); len(got) != 2 {
		t.Errorf("recorded %d spawns, want 2 (coalesced to one relaunch): %v", len(got), got)
	}
}

// waitFor polls fn until it returns true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
