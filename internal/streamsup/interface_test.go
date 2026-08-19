package streamsup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turncommit"
)

// waitForState polls r.State() until pred holds or the timeout elapses, returning
// the matching snapshot. Lets a test observe a transient lifecycle phase (e.g.
// Backoff) without racing the Run goroutine's writes.
func waitForState(t *testing.T, r *Runner, pred func(State) bool, timeout time.Duration) State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := r.State(); pred(st) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state predicate; last = %+v", r.State())
	return State{}
}

// --- WriteUserTurn: AC2 ------------------------------------------------------

// TestRunner_WriteUserTurn_NoLiveChild: with no child spawned yet, Stdin() is nil
// and WriteUserTurn returns the retryable ErrNoLiveChild without writing —
// WriteTurn's verbatim no-live-child refusal, surfaced through the interface
// method.
func TestRunner_WriteUserTurn_NoLiveChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.WriteUserTurn(context.Background(), "c1", []byte("hello")); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn with no live child = %v, want ErrNoLiveChild", err)
	}
}

// TestRunner_WriteUserTurn_LiveChildDelivers: on a live child WriteUserTurn writes
// the user envelope onto the held-open stdin and returns nil; the fake child
// echoes the line back, proving the prompt reached it exactly once.
func TestRunner_WriteUserTurn_LiveChildDelivers(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	const marker = "write-user-turn-marker-AC2"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
		t.Fatalf("WriteUserTurn on a live child: %v", err)
	}
	// The child echoes each stdin line as ECHO:<line>; the envelope carries the
	// marker as its content text, so the echo proves the envelope was delivered.
	waitForContains(t, out, "ECHO:", 3*time.Second)
	if !strings.Contains(out.String(), marker) {
		t.Errorf("child output missing delivered marker %q:\n%s", marker, out.String())
	}
}

// TestRunner_WriteUserTurn_GateDropsWithoutWriting: a false turncommit claim
// means the queued head was dropped during the ready-wait, so WriteUserTurn
// surfaces turncommit.ErrDropped and writes ZERO bytes onto the child's stdin.
// The zero-bytes property is proven by a FIFO barrier: a second, un-gated turn
// carrying a sentinel is echoed by the child, and once that sentinel arrives the
// dropped marker's echo must be absent — the pipe is FIFO, so a written dropped
// line would have echoed before the sentinel.
func TestRunner_WriteUserTurn_GateDropsWithoutWriting(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	const dropped = "dropped-marker-AC2"
	const sentinel = "sentinel-marker-AC2"

	// A false gate drops the turn: ErrDropped, and nothing must reach the child.
	deniedCtx := turncommit.With(context.Background(), func() bool { return false })
	if err := r.WriteUserTurn(deniedCtx, "c1", []byte(dropped)); !errors.Is(err, turncommit.ErrDropped) {
		t.Fatalf("WriteUserTurn with a false gate = %v, want turncommit.ErrDropped", err)
	}

	// An un-gated turn delivers; its echo is the FIFO barrier.
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(sentinel)); err != nil {
		t.Fatalf("WriteUserTurn (sentinel): %v", err)
	}
	waitForContains(t, out, sentinel, 3*time.Second)
	if strings.Contains(out.String(), dropped) {
		t.Errorf("dropped turn reached the child — a false gate must write zero bytes\n%s", out.String())
	}
}

// --- Interrupt: AC1 + AC2 ----------------------------------------------------

// TestRunner_Interrupt_NoLiveChild: with no child spawned, Stdin() is nil and
// Interrupt returns the retryable ErrNoLiveChild without writing and without
// panicking — the safe no-op refusal (AC2). Mirrors WriteUserTurn's no-live-child
// contract.
func TestRunner_Interrupt_NoLiveChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Interrupt(); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("Interrupt with no live child = %v, want ErrNoLiveChild", err)
	}
}

// TestRunner_NextInterruptID_Monotonic: correlation ids are locally minted (not
// caller-supplied) and strictly increasing within the runner's lifetime, so a
// future ack-correlator can distinguish successive interrupts.
func TestRunner_NextInterruptID_Monotonic(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	first, second := r.nextInterruptID(), r.nextInterruptID()
	if first == second {
		t.Fatalf("nextInterruptID returned the same id twice: %q", first)
	}
	if first != "1" || second != "2" {
		t.Fatalf("nextInterruptID minted %q, %q, want 1, 2 (monotonic from zero value)", first, second)
	}
}

// TestRunner_Interrupt_LiveChildDelivers: on a live child Interrupt writes a
// single control_request line onto the held-open stdin (AC1); the echo_lines
// fake child echoes it back as ECHO:<line>, proving the exact interrupt envelope
// reached the child — type control_request, request.subtype interrupt, and a
// non-empty locally-minted request_id.
func TestRunner_Interrupt_LiveChildDelivers(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	if err := r.Interrupt(); err != nil {
		t.Fatalf("Interrupt on a live child: %v", err)
	}
	// The child echoes each stdin line as ECHO:<line>; locate the echoed
	// interrupt line, strip the prefix, and decode it.
	waitForContains(t, out, "ECHO:", 3*time.Second)
	echoed := findEchoedLine(t, out.String())
	var cr decodedControlRequest
	if err := json.Unmarshal([]byte(echoed), &cr); err != nil {
		t.Fatalf("echoed interrupt line did not decode: %v (%q)", err, echoed)
	}
	if cr.Type != "control_request" {
		t.Errorf("echoed interrupt type = %q, want control_request", cr.Type)
	}
	if cr.Request.Subtype != "interrupt" {
		t.Errorf("echoed interrupt request.subtype = %q, want interrupt", cr.Request.Subtype)
	}
	if cr.RequestID == "" {
		t.Error("echoed interrupt request_id is empty, want a locally-minted id")
	}
}

// findEchoedLine returns the first ECHO:-prefixed line's payload from the child's
// captured stdout, failing the test if none is present.
func findEchoedLine(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if after, ok := strings.CutPrefix(line, "ECHO:"); ok {
			return after
		}
	}
	t.Fatalf("no ECHO: line in child output:\n%s", output)
	return ""
}

// --- Live Restart: AC3 -------------------------------------------------------

// waitArgvLines polls the argv capture file until it holds at least n complete
// (newline-terminated) records, and returns those records.
//
// onSpawn fires immediately after cmd.Start returns, so it proves only that the
// fork/exec succeeded — the child may not have executed a single line of Go yet.
// Every teardown here SIGTERMs the live child (Restart cancels the iteration
// ctx; so does cancelling Run's ctx), and a child still in runtime startup dies
// on the default SIGTERM disposition, before record_block writes its argv.
// Waiting on the record itself — the only artifact the assertions consume — is
// the happens-before edge onSpawn cannot supply.
//
// The deadline is a FAILURE BOUND, not a calibration: the write is the child's
// first act, so a healthy run reaches it in milliseconds and only a child that
// never records waits it out.
func waitArgvLines(t *testing.T, path string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var data []byte
	var recs []string
	for {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			// Count only complete records: whatever follows the final newline is
			// a torn write, not a recorded argv line.
			data = b
			recs = strings.Split(string(b), "\n")
			recs = recs[:len(recs)-1]
			if len(recs) >= n {
				return recs
			}
		case errors.Is(err, os.ErrNotExist):
			// record_block creates the file with O_CREATE on its first write, so
			// a missing file is the normal pre-write state: zero records so far.
			data, recs = nil, nil
		default:
			t.Fatalf("read argv capture %s: %v", path, err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out waiting for the child's argv record: want ≥%d complete line(s) in %s, have %d:\n%s",
				n, path, len(recs), data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunner_LiveRestart drives a deliberate Restart against a live child and
// asserts (a) the child is respawned with the swapped args and --resume (not
// --session-id), (b) Run's ctx is NOT cancelled by the restart (Run keeps
// looping — proven by cancelling it explicitly afterward and observing
// context.Canceled), and (c) RestartCount does not increment, because a
// deliberate restart is not a crash and skips the backoff path.
func TestRunner_LiveRestart(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	argvFile := filepath.Join(t.TempDir(), "argv")
	cfg := helperRunCfg(t, "record_block", out, stderr, "GO_STREAMSUP_HELPER_ARGV_FILE="+argvFile)
	spawns := make(chan struct{}, 32)
	cfg.onSpawn = func(int) {
		select {
		case spawns <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)

	// Wait for the first child (it records its argv, then blocks until SIGTERM —
	// it will not self-exit, so a second spawn can only come from Restart).
	select {
	case <-spawns:
	case <-time.After(5 * time.Second):
		t.Fatal("first child never spawned")
	}

	// Restart kills this child; its argv must be on disk before that happens.
	waitArgvLines(t, argvFile, 1)

	r.Restart([]string{"--model", "restart-marker"})

	// The restart must force a relaunch with the swapped args.
	select {
	case <-spawns:
	case <-time.After(5 * time.Second):
		t.Fatal("Restart did not respawn the child")
	}

	// Same window on spawn 2: the cancel() below kills it, so wait out its record
	// too. Both argv lines are in hand from here on.
	lines := waitArgvLines(t, argvFile, 2)

	// Run is still looping (Restart did not cancel it): cancelling now returns a
	// context error, proving Run outlived the restart.
	cancel()
	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after teardown", err)
	}

	// A deliberate restart is not a crash: RestartCount stays 0.
	if got := r.State().RestartCount; got != 0 {
		t.Errorf("RestartCount = %d after a deliberate restart, want 0 (not a crash)", got)
	}

	first, second := lines[0], lines[1]

	// Spawn 1 establishes the id with --session-id and carries no restart arg.
	if !strings.Contains(first, "--session-id "+testSessionID) {
		t.Errorf("spawn 1 argv missing --session-id %s:\n%s", testSessionID, first)
	}
	if strings.Contains(first, "restart-marker") {
		t.Errorf("spawn 1 argv unexpectedly carries the restart arg:\n%s", first)
	}
	// Spawn 2 resumes the SAME id and carries the swapped restart arg.
	if !strings.Contains(second, "--resume "+testSessionID) {
		t.Errorf("spawn 2 argv missing --resume %s (a live restart resumes, not forks):\n%s", testSessionID, second)
	}
	if strings.Contains(second, "--session-id") {
		t.Errorf("spawn 2 argv unexpectedly carries --session-id:\n%s", second)
	}
	if !strings.Contains(second, "--model restart-marker") {
		t.Errorf("spawn 2 argv missing the swapped --model restart-marker:\n%s", second)
	}
}

// --- State: AC4 --------------------------------------------------------------

// TestRunner_StatePhaseProgression asserts State() reflects the lifecycle:
// Running (with a live pid and a start time) while the child is up, then Stopped
// on ctx cancel.
func TestRunner_StatePhaseProgression(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Before Run, State reports the initial Starting phase.
	if got := r.State().Phase; got != PhaseStarting {
		t.Errorf("pre-Run Phase = %q, want %q", got, PhaseStarting)
	}

	cancel, join := runInBackground(t, r)
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}

	st := waitForState(t, r, func(s State) bool { return s.Phase == PhaseRunning }, 3*time.Second)
	if st.ChildPID <= 0 {
		t.Errorf("Running state ChildPID = %d, want > 0", st.ChildPID)
	}
	if st.StartedAt.IsZero() {
		t.Error("Running state StartedAt is zero, want the Run start time")
	}

	cancel()
	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
	final := r.State()
	if final.Phase != PhaseStopped {
		t.Errorf("post-cancel Phase = %q, want %q", final.Phase, PhaseStopped)
	}
	if final.ChildPID != 0 {
		t.Errorf("post-cancel ChildPID = %d, want 0", final.ChildPID)
	}
}

// TestRunner_StateBackoffOnCrash asserts a crash drives State() into Backoff with
// an incremented RestartCount and a scheduled NextBackoff. A generous
// BackoffInitial keeps the Backoff window wide enough to observe reliably.
func TestRunner_StateBackoffOnCrash(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "crash", out, stderr)
	cfg.BackoffInitial = 500 * time.Millisecond
	cfg.BackoffMax = 500 * time.Millisecond
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	st := waitForState(t, r, func(s State) bool { return s.Phase == PhaseBackoff }, 5*time.Second)
	if st.RestartCount < 1 {
		t.Errorf("Backoff state RestartCount = %d, want ≥1", st.RestartCount)
	}
	if st.NextBackoff <= 0 {
		t.Errorf("Backoff state NextBackoff = %v, want > 0", st.NextBackoff)
	}
	if st.ChildPID != 0 {
		t.Errorf("Backoff state ChildPID = %d, want 0", st.ChildPID)
	}
}

// --- WaitForPTY: AC5 ---------------------------------------------------------

// TestRunner_WaitForPTY: the stream path has no PTY, so WaitForPTY returns nil in
// every window — no live child, a live child, and even under a cancelled ctx (it
// is a bare return nil, no readiness gate).
func TestRunner_WaitForPTY(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := r.WaitForPTY(context.Background()); err != nil {
		t.Errorf("WaitForPTY before spawn = %v, want nil", err)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if err := r.WaitForPTY(cctx); err != nil {
		t.Errorf("WaitForPTY with cancelled ctx = %v, want nil", err)
	}

	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	if err := r.WaitForPTY(context.Background()); err != nil {
		t.Errorf("WaitForPTY with live child = %v, want nil", err)
	}
}
