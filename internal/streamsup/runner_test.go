package streamsup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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

// --- buildArgs: AC2 + AC4, pure ---------------------------------------------

func TestBuildArgs(t *testing.T) {
	t.Parallel()
	const id = "sess-abc"
	tests := []struct {
		name     string
		base     []string
		firstRun bool
		want     []string
	}{
		{
			name:     "first spawn establishes id with --session-id",
			base:     []string{"--model", "sonnet"},
			firstRun: true,
			want: []string{
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--verbose",
				"--model", "sonnet",
				"--session-id", id,
			},
		},
		{
			name:     "respawn reattaches with --resume",
			base:     []string{"--model", "sonnet"},
			firstRun: false,
			want: []string{
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--verbose",
				"--model", "sonnet",
				"--resume", id,
			},
		},
		{
			name:     "no base args",
			base:     nil,
			firstRun: true,
			want: []string{
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--verbose",
				"--session-id", id,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildArgs(tt.base, tt.firstRun, id)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildArgs()\n got  = %v\n want = %v", got, tt.want)
			}
			// The non-print choice is billing-tied and spike-verified: never
			// emit -p / --print.
			for _, a := range got {
				if a == "-p" || a == "--print" {
					t.Errorf("buildArgs emitted %q; the non-print choice is required", a)
				}
			}
		})
	}
}

func TestBuildArgs_DoesNotMutateBase(t *testing.T) {
	t.Parallel()
	base := []string{"--model", "sonnet"}
	orig := slices.Clone(base)
	_ = buildArgs(base, true, "id")
	_ = buildArgs(base, false, "id")
	if !slices.Equal(base, orig) {
		t.Errorf("buildArgs mutated base: got %v, want %v", base, orig)
	}
}

// TestBuildArgs_StableIDAcrossFirstAndResume is the pure half of AC4: the same
// id token appears after --session-id on the first spawn and after --resume on
// a respawn — byte-identical, so the on-disk id is reused (no fork).
func TestBuildArgs_StableIDAcrossFirstAndResume(t *testing.T) {
	t.Parallel()
	const id = "deadbeef-uuid"
	if got := idFlagValue(buildArgs(nil, true, id), "--session-id"); got != id {
		t.Errorf("first spawn --session-id = %q, want %q", got, id)
	}
	if got := idFlagValue(buildArgs(nil, false, id), "--resume"); got != id {
		t.Errorf("respawn --resume = %q, want %q", got, id)
	}
}

// idFlagValue returns the argv element immediately following flag, or "".
func idFlagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "block_sigterm", out, stderr)

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- r.Run(ctx) }()

	time.Sleep(150 * time.Millisecond) // let the child come up and block
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
