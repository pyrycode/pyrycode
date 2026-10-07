package streamsup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// testSessionID is the caller-minted session id threaded through the
// integration tests; the runner treats it as opaque (non-empty check only).
const testSessionID = "11111111-2222-3333-4444-555555555555"

// safeBuffer is a mutex-guarded io.Writer + string reader. os/exec's stdout /
// stderr copier goroutines write into it while the test goroutine polls it, so
// every access is serialised.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// helperRunCfg returns a Config wired to run the test binary as a fake headless
// claude (see TestMain / helperChild). Tests override BackoffInitial, onSpawn,
// and writers as needed. The base Args are left empty: helperChild dispatches on
// the env, so the runner's fixed stream-json prefix + id flag are the whole argv.
func helperRunCfg(t *testing.T, mode string, stdout, stderr *safeBuffer, extraEnv ...string) Config {
	t.Helper()
	env := append([]string{
		"GO_STREAMSUP_HELPER=1",
		"GO_STREAMSUP_HELPER_MODE=" + mode,
	}, extraEnv...)
	return Config{
		ClaudeBin: os.Args[0],
		WorkDir:   t.TempDir(),
		SessionID: testSessionID,
		Stdout:    stdout,
		Stderr:    stderr,
		Env:       env,
	}
}

// runInBackground starts r.Run on a goroutine and returns a cancel func plus a
// join that waits for Run to return (failing the test if it hangs past 10s).
func runInBackground(t *testing.T, r *Runner) (cancel context.CancelFunc, join func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	join = func() error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return within 10s of cancel")
			return nil
		}
	}
	return cancel, join
}

// waitForContains polls b until it contains sub or the timeout elapses.
func waitForContains(t *testing.T, b *safeBuffer, sub string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), sub) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in output; got:\n%s", sub, b.String())
}

// --- New: validation --------------------------------------------------------

func TestNew_RequiredFields(t *testing.T) {
	t.Parallel()
	base := func() Config {
		return Config{ClaudeBin: os.Args[0], WorkDir: t.TempDir(), SessionID: testSessionID}
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty ClaudeBin", func(c *Config) { c.ClaudeBin = "" }},
		{"empty WorkDir", func(c *Config) { c.WorkDir = "" }},
		{"empty SessionID", func(c *Config) { c.SessionID = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			tt.mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Errorf("New with %s: got nil error, want non-nil", tt.name)
			}
		})
	}
}

func TestNew_WorkDirMustExist(t *testing.T) {
	t.Parallel()
	cfg := Config{
		ClaudeBin: os.Args[0],
		WorkDir:   filepath.Join(t.TempDir(), "does-not-exist"),
		SessionID: testSessionID,
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New with non-existent WorkDir: got nil error, want non-nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("New error = %v, want it to wrap fs.ErrNotExist", err)
	}
}

// --- Held-open stdin: AC2 ---------------------------------------------------

func TestRunner_HoldsStdinOpen(t *testing.T) {
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

	// The held-open handle must be live while the child runs.
	w := r.Stdin()
	if w == nil {
		t.Fatal("Stdin() is nil while the child is live — stdin was not held open")
	}
	if _, err := w.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write to held-open stdin: %v", err)
	}
	waitForContains(t, out, "ECHO:ping", 3*time.Second)

	// The child only writes GOT_EOF when its stdin reaches EOF; its absence
	// proves the runner did NOT close stdin after spawn.
	if strings.Contains(out.String(), "GOT_EOF") {
		t.Errorf("child observed stdin EOF — stdin must stay open across turns\nstdout:\n%s", out.String())
	}
}

// TestRunner_StdinNilBeforeSpawn asserts the accessor reports no live child
// before Run has spawned anything.
func TestRunner_StdinNilBeforeSpawn(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w := r.Stdin(); w != nil {
		t.Errorf("Stdin() = %v before Run, want nil", w)
	}
}

// --- Restart on crash: AC3 wiring -------------------------------------------

func TestRunner_RestartsOnCrash(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "crash", out, stderr)
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
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

	// ≥2 spawns proves the loop respawned after the crash.
	for i := 0; i < 2; i++ {
		select {
		case <-spawns:
		case <-time.After(5 * time.Second):
			t.Fatalf("saw only %d spawn(s), want ≥2 — loop did not restart on crash", i)
		}
	}
	cancel()

	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after teardown", err)
	}
}

// --- Parser partial line dropped at child exit (#1503) ----------------------

// TestRunner_DropsParserPartialLineAtChildExit: a child that dies mid-line must
// not leave its fragment in the shared Parser for the respawned child's first
// line to splice onto. Driven through Run rather than the Parser alone, so it
// proves spawnAndWait makes the call, not just that the method exists.
func TestRunner_DropsParserPartialLineAtChildExit(t *testing.T) {
	t.Parallel()
	var (
		mu     sync.Mutex
		events []turnevent.Event
	)
	parser := NewParser(func(ev turnevent.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}, discardLogger())
	snapshot := func() []turnevent.Event {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(events)
	}

	argvFile := filepath.Join(t.TempDir(), "argv")
	cfg := helperRunCfg(t, "partial_then_line", nil, &safeBuffer{}, "GO_STREAMSUP_HELPER_ARGV_FILE="+argvFile)
	cfg.Stdout = parser
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)

	// The respawned child's line is the only thing either outcome emits: a clean
	// TurnEnd with the fix, one Unrecognized carrying the splice without it.
	deadline := time.Now().Add(5 * time.Second)
	for len(snapshot()) == 0 {
		if time.Now().After(deadline) {
			cancel()
			join()
			t.Fatal("no event within 5s of Run — the respawned child's line never parsed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	join()

	want := []turnevent.Event{turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}}
	if got := snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v — the dead child's partial spliced onto its successor's first line", got, want)
	}
}

// --- Resume id stable across restart: AC4 -----------------------------------

func TestRunner_ResumeIDStableAcrossRestart(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	argvFile := filepath.Join(t.TempDir(), "argv")
	cfg := helperRunCfg(t, "crash", out, stderr, "GO_STREAMSUP_HELPER_ARGV_FILE="+argvFile)
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
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

	// Wait for the third spawn: once spawn 3 has started, spawns 1 and 2 have
	// both exited (a child exits before the loop respawns), so both have
	// flushed their argv line to the capture file.
	for i := 0; i < 3; i++ {
		select {
		case <-spawns:
		case <-time.After(5 * time.Second):
			t.Fatalf("saw only %d spawn(s), want ≥3", i)
		}
	}
	cancel()
	join()

	data, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv capture: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("want ≥2 captured argv lines, got %d:\n%s", len(lines), data)
	}
	first, second := lines[0], lines[1]

	// Spawn 1 establishes the id with --session-id; spawn 2 reattaches with
	// --resume carrying the SAME id — proving the on-disk id is reused, no fork.
	if !strings.Contains(first, "--session-id "+testSessionID) {
		t.Errorf("spawn 1 argv missing --session-id %s:\n%s", testSessionID, first)
	}
	if strings.Contains(first, "--resume") {
		t.Errorf("spawn 1 argv unexpectedly carries --resume:\n%s", first)
	}
	if !strings.Contains(second, "--resume "+testSessionID) {
		t.Errorf("spawn 2 argv missing --resume %s:\n%s", testSessionID, second)
	}
	if strings.Contains(second, "--session-id") {
		t.Errorf("spawn 2 argv unexpectedly carries --session-id:\n%s", second)
	}
}

// --- Spawn-setup failure retains --session-id: AC4 regression ---------------

// spawnArgsRecorder is a slog.Handler that captures the argv of every
// "spawning claude" log record the Run loop emits (runner.go logs args just
// before each spawn attempt). It lets a test observe the argv of spawns that
// never launch a child — the spawn-SETUP-failure path, where no fake child runs
// to record its own os.Args. Mutex-guarded: os/exec forwarder goroutines are
// irrelevant here, but Run and the test goroutine both touch it.
type spawnArgsRecorder struct {
	mu     sync.Mutex
	spawns [][]string
}

func (h *spawnArgsRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *spawnArgsRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "spawning claude" {
		return nil
	}
	var args []string
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "args" {
			if v, ok := a.Value.Any().([]string); ok {
				args = slices.Clone(v)
			}
			return false
		}
		return true
	})
	h.mu.Lock()
	h.spawns = append(h.spawns, args)
	h.mu.Unlock()
	return nil
}

func (h *spawnArgsRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *spawnArgsRecorder) WithGroup(string) slog.Handler      { return h }

func (h *spawnArgsRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.spawns)
}

func (h *spawnArgsRecorder) all() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]string(nil), h.spawns...)
}

// TestRunner_SpawnSetupFailureRetainsSessionID guards the fix for the #1090
// review finding: a spawn-SETUP failure (cmd.Start never launches claude) must
// NOT advance firstRun. With ClaudeBin pointing at a non-existent binary, every
// spawn fails at Start, so the session is never established on disk — every
// retry must keep using --session-id. If firstRun flipped on the setup failure,
// the second attempt would switch to --resume against an id --session-id never
// created (a permanent, unrecoverable crash-loop). Constructs the Runner
// directly to bypass New's exec.LookPath check.
func TestRunner_SpawnSetupFailureRetainsSessionID(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	rec := &spawnArgsRecorder{}
	r := &Runner{
		cfg: Config{
			ClaudeBin:      filepath.Join(tmp, "no-such-claude-binary"),
			SessionID:      testSessionID,
			BackoffInitial: time.Millisecond,
			BackoffMax:     5 * time.Millisecond,
			BackoffReset:   time.Minute,
		},
		log:     slog.New(rec),
		workDir: tmp,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for ≥2 spawn attempts. The first fails at cmd.Start; if the runner
	// wrongly flipped firstRun on that setup failure, the second would use
	// --resume.
	deadline := time.Now().Add(5 * time.Second)
	for rec.count() < 2 {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("saw only %d spawn attempt(s), want ≥2", rec.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s of cancel")
	}

	for i, args := range rec.all() {
		if !slices.Contains(args, "--session-id") || slices.Contains(args, "--resume") {
			t.Errorf("spawn attempt %d argv = %v; after a spawn-setup failure the session is still "+
				"unestablished, so every retry must use --session-id, never --resume", i+1, args)
		}
	}
}

// --- Teardown SIGTERM + grace: AC5 ------------------------------------------

func TestRunner_TeardownSIGTERM(t *testing.T) {
	t.Parallel()
	for _, delayed := range []bool{false, true} {
		name := "immediate_start"
		if delayed {
			name = "delayed_start"
		}
		t.Run(name, func(t *testing.T) {
			out, stderr := &safeBuffer{}, &safeBuffer{}
			cfg := helperRunCfg(t, "block_sigterm", out, stderr)
			if delayed {
				// Delay exec beyond the former startup wait to exercise cancellation
				// while the child has yet to install its signal handler.
				cfg.ClaudeBin = filepath.Join(t.TempDir(), "slow-claude")
				cfg.Env = append(cfg.Env, "GO_STREAMSUP_TEST_BINARY="+os.Args[0])
				shim := "#!/bin/sh\nsleep 0.3\nexec \"$GO_STREAMSUP_TEST_BINARY\" \"$@\"\n"
				if err := os.WriteFile(cfg.ClaudeBin, []byte(shim), 0700); err != nil {
					t.Fatal(err)
				}
			}

			r, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			stopped := make(chan struct{})
			go func() {
				done <- r.Run(ctx)
				close(stopped)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-stopped:
				case <-time.After(10 * time.Second):
					t.Error("Run did not stop during cleanup")
				}
			})

			// READY is emitted after signal.Notify, so cancellation cannot race
			// the child's signal-handler installation even when startup is slow.
			waitForContains(t, out, "READY", 5*time.Second)
			start := time.Now()
			cancel()

			var runErr error
			select {
			case runErr = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return within 10s of cancel")
			}
			elapsed := time.Since(start)

			if !errors.Is(runErr, context.Canceled) {
				t.Errorf("Run returned %v, want context.Canceled", runErr)
			}
			// A handled SIGTERM exits promptly; falling through to the SIGKILL grace
			// window would push elapsed past killGrace (5s).
			if elapsed > 6*time.Second {
				t.Errorf("Run took %v; SIGTERM likely fell through to SIGKILL grace", elapsed)
			}
			if !strings.Contains(stderr.String(), "got SIGTERM") {
				t.Errorf("stderr missing %q; SIGTERM may not have reached the child\nstderr: %q",
					"got SIGTERM", stderr.String())
			}
		})
	}
}

// --- Teardown reaps descendant groups: AC5 ----------------------------------

// reapRecorder is a mutex-guarded double for reapDescendantGroupsFn: it records
// the rootPid of every reap call so the ctx-cancel test can assert the wiring
// fired with a plausible pid — without a real descendant tree (that is
// agentrun.TestReapDescendantGroups' job). Mutex-guarded because cmd.Cancel
// fires from the os/exec ctx-watcher goroutine, not the test goroutine.
type reapRecorder struct {
	mu   sync.Mutex
	pids []int
}

func (r *reapRecorder) record(rootPid int, _ *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pids = append(r.pids, rootPid)
}

func (r *reapRecorder) calls() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.pids...)
}

// swapReapSeam points reapDescendantGroupsFn at rec.record for the test and
// restores the real reaper via t.Cleanup. Callers MUST be non-parallel: they
// mutate a package var. Go runs non-parallel tests — and their t.Cleanup
// restore — to completion before parked t.Parallel() tests resume, so the swap
// window never overlaps the parallel siblings.
func swapReapSeam(t *testing.T, rec *reapRecorder) {
	t.Helper()
	orig := reapDescendantGroupsFn
	reapDescendantGroupsFn = rec.record
	t.Cleanup(func() { reapDescendantGroupsFn = orig })
}

// assertReapedLivePid asserts the reap seam captured at least one call with a
// plausible pid (> 1, matching reap.go's pgid<=1 guard boundary). The test
// cannot know the fake-claude's real pid, so it asserts > 1.
func assertReapedLivePid(t *testing.T, rec *reapRecorder) {
	t.Helper()
	calls := rec.calls()
	if len(calls) == 0 {
		t.Fatal("reap seam never fired on the teardown path — reap wiring missing")
	}
	for _, pid := range calls {
		if pid > 1 {
			return
		}
	}
	t.Fatalf("reap seam fired %d time(s) but no call carried a plausible pid > 1: %v", len(calls), calls)
}

// TestRunner_TeardownReapsDescendantGroups asserts the operator-SIGTERM
// teardown reaps claude's detached descendant process groups. Non-parallel: it
// swaps the reapDescendantGroupsFn package var (see swapReapSeam).
func TestRunner_TeardownReapsDescendantGroups(t *testing.T) {
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "block_sigterm", out, stderr)

	rec := &reapRecorder{}
	swapReapSeam(t, rec)

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s of cancel")
	}
	assertReapedLivePid(t, rec)
}

// --- OnChildExit: the per-child-exit seam ------------------------------------

// newExitRecorder returns an OnChildExit callback plus an exact count reader and
// a buffered fire channel. The callback runs on the Run goroutine while the test
// goroutine reads the total, so the counter is atomic; the channel (non-blocking
// send, like the onSpawn counters above) lets a test wait for the Nth fire
// without polling.
func newExitRecorder() (fire func(), count func() int, fired <-chan struct{}) {
	var n atomic.Int64
	ch := make(chan struct{}, 32)
	return func() {
			n.Add(1)
			select {
			case ch <- struct{}{}:
			default:
			}
		},
		func() int { return int(n.Load()) },
		ch
}

// TestRunner_OnChildExit_FiresOnCrashRespawn covers the per-child cardinality: a
// crash followed by a backoff respawn fires OnChildExit again. PhaseStopped
// cannot express this — it fires once, on permanent shutdown (state.go) — so the
// seam is the only per-child signal. Mirrors TestRunner_RestartsOnCrash.
func TestRunner_OnChildExit_FiresOnCrashRespawn(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "crash", out, stderr)
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
	spawns := make(chan struct{}, 32)
	cfg.onSpawn = func(int) {
		select {
		case spawns <- struct{}{}:
		default:
		}
	}
	fire, _, exits := newExitRecorder()
	cfg.OnChildExit = fire

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)

	// ≥2 spawns proves the loop respawned after the crash; ≥2 exits proves the
	// seam fired for each of those children, not once for the whole Run.
	for i := 0; i < 2; i++ {
		select {
		case <-spawns:
		case <-time.After(5 * time.Second):
			t.Fatalf("saw only %d spawn(s), want ≥2 — loop did not restart on crash", i)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-exits:
		case <-time.After(5 * time.Second):
			t.Fatalf("saw only %d child-exit fire(s), want ≥2 — the seam is not per-child", i)
		}
	}
	cancel()

	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after teardown", err)
	}
}

// TestRunner_OnChildExit_FiresOnShutdown covers the load-bearing exit path: a
// parent-ctx cancel fires the seam exactly once. Run's post-spawn shutdown
// return sits ABOVE the "claude exited" log, so a fire anchored on that log
// never runs here (count 0); a fire additionally placed at the top-of-loop guard
// or inside the backoff select double-counts (count 2). echo_lines blocks on the
// held-open stdin and never self-exits, so exactly one supervision iteration
// runs and the expected count is exact, not a floor.
//
// Deterministic, not a poll: the fire and Run's return are both on the Run
// goroutine with no wait between them, so join() returning is proof the fire has
// already happened.
func TestRunner_OnChildExit_FiresOnShutdown(t *testing.T) {
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
	fire, exitCount, _ := newExitRecorder()
	cfg.OnChildExit = fire

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)

	// Sync on the spawn before cancelling: cancelling first would leave Run at
	// the top-of-loop guard with no child ever spawned, making the count vacuous.
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	cancel()

	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
	if got := exitCount(); got != 1 {
		t.Errorf("OnChildExit fired %d time(s) on the shutdown path, want exactly 1 "+
			"(0 = the fire sits below Run's post-spawn ctx.Err() return; 2 = it also fires at a second site)", got)
	}
}

// TestRunner_OnChildExit_FiresOnDeliberateRestart covers the restart exit path,
// which leaves the loop at the drainRestart continue — above the backoff ladder —
// so a fire placed below that branch would be skipped here. record_block never
// self-exits, so every exit in this test is caller-caused: one child killed by
// Restart, one by the shutdown cancel — exactly 2.
func TestRunner_OnChildExit_FiresOnDeliberateRestart(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "record_block", out, stderr)
	spawns := make(chan struct{}, 32)
	cfg.onSpawn = func(int) {
		select {
		case spawns <- struct{}{}:
		default:
		}
	}
	fire, exitCount, _ := newExitRecorder()
	cfg.OnChildExit = fire

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)

	select {
	case <-spawns:
	case <-time.After(5 * time.Second):
		t.Fatal("first child never spawned")
	}
	r.Restart(nil)
	select {
	case <-spawns:
	case <-time.After(5 * time.Second):
		t.Fatal("Restart did not respawn the child")
	}
	cancel()

	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after teardown", err)
	}
	if got := exitCount(); got != 2 {
		t.Errorf("OnChildExit fired %d time(s), want exactly 2 "+
			"(one Restart-killed child, one shutdown-killed child)", got)
	}
}

// TestRunner_OnChildExit_FiresOnSpawnSetupFailure pins the arm the seam takes on
// the spawn-SETUP-failure case: the fire is UNCONDITIONAL, so it also fires when
// cmd.Start never launched claude (spawnAndWait reports started == false) and no
// child ever existed. That is the contract stated on Config.OnChildExit — "once
// per completed supervision iteration", not once per child. Mirrors
// TestRunner_SpawnSetupFailureRetainsSessionID's direct construction, which
// bypasses New's exec.LookPath so every spawn fails at Start.
//
// The equality is exact at join() time, not a race: the "spawning claude" log
// and the fire are both on the Run goroutine with no return point between them,
// and every Run exit path either fired already or has not logged yet. A fire
// wrongly gated on started reads 0 here.
func TestRunner_OnChildExit_FiresOnSpawnSetupFailure(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	rec := &spawnArgsRecorder{}
	fire, exitCount, _ := newExitRecorder()
	r := &Runner{
		cfg: Config{
			ClaudeBin:      filepath.Join(tmp, "no-such-claude-binary"),
			SessionID:      testSessionID,
			BackoffInitial: time.Millisecond,
			BackoffMax:     5 * time.Millisecond,
			BackoffReset:   time.Minute,
			OnChildExit:    fire,
		},
		log:     slog.New(rec),
		workDir: tmp,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for rec.count() < 2 {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("saw only %d spawn attempt(s), want ≥2", rec.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s of cancel")
	}

	attempts, fires := rec.count(), exitCount()
	if fires < 2 {
		t.Errorf("OnChildExit fired %d time(s) across %d spawn attempt(s), want ≥2", fires, attempts)
	}
	if fires != attempts {
		t.Errorf("OnChildExit fired %d time(s) across %d spawn attempt(s), want one fire per attempted "+
			"iteration — the seam is unconditional and fires even when no claude process launched", fires, attempts)
	}
}
