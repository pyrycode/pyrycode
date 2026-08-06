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
	"sync/atomic"
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

// --- RestartFresh: rotate into a fresh session under a new id: AC1/AC2/AC4/AC5 --

// rotatedSessionID is the caller-supplied new session id a RestartFresh rotates
// to; distinct from testSessionID so the argv assertions can tell the fresh
// transcript apart from the pre-rotation session.
const rotatedSessionID = "99999999-8888-7777-6666-555555555555"

// idFlagCount returns how many id flags (--session-id / --resume) appear in args.
// Exactly one per spawn is the single-id-flag invariant (AC4/AC5(3)).
func idFlagCount(args []string) int {
	n := 0
	for _, a := range args {
		if a == "--session-id" || a == "--resume" {
			n++
		}
	}
	return n
}

// TestRunner_RestartFresh_RotatesThenResumesNewID proves the full fresh-restart
// sequence in one deterministic run: after RestartFresh the next spawn uses
// --session-id <newID> (a fresh transcript, AC1), and a subsequent crash-respawn
// --resumes the NEW id — never the old one (AC2 + the load-bearing subtlety the
// ticket warns about). Also asserts no double id-flag injection (AC5(3)).
//
// A sync.Once-guarded onSpawn rotates on exactly the first spawn. onSpawn fires
// on the Run goroutine before cmd.Wait, so the rotation is published before spawn
// 1 exits regardless of whether the crash or the RestartFresh cancel wins — the
// argv sequence is stable:
//
//	spawn 1: --session-id <old>   (establish the original id)
//	spawn 2: --session-id <new>   (fresh establish of the rotated id)
//	spawn 3: --resume     <new>   (crash-respawn resumes the rotated id)
func TestRunner_RestartFresh_RotatesThenResumesNewID(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "crash", out, stderr)
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
	rec := &spawnArgsRecorder{}
	cfg.Logger = slog.New(rec)
	var (
		r    *Runner
		once sync.Once
	)
	cfg.onSpawn = func(int) {
		once.Do(func() { r.RestartFresh(rotatedSessionID) })
	}

	var err error
	r, err = New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)

	// Wait for spawn 1 (old id), spawn 2 (rotated establish), spawn 3 (rotated
	// resume). Tiny backoff + a 20ms crash child reaches 3 spawns in well under a
	// second.
	deadline := time.Now().Add(5 * time.Second)
	for rec.count() < 3 {
		if time.Now().After(deadline) {
			cancel()
			join()
			t.Fatalf("saw only %d spawn(s), want ≥3", rec.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	join()

	spawns := rec.all()

	// spawn 1 establishes the original id with --session-id, no --resume.
	if got := idFlagValue(spawns[0], "--session-id"); got != testSessionID {
		t.Errorf("spawn 1 --session-id = %q, want %q:\n%v", got, testSessionID, spawns[0])
	}
	if slices.Contains(spawns[0], "--resume") {
		t.Errorf("spawn 1 unexpectedly carries --resume:\n%v", spawns[0])
	}

	// spawn 2 is the fresh establish of the rotated id: --session-id <new>, no
	// --resume — a fresh restart re-arms first-run form, it is not a resume.
	if got := idFlagValue(spawns[1], "--session-id"); got != rotatedSessionID {
		t.Errorf("spawn 2 --session-id = %q, want rotated %q (RestartFresh did not re-arm first-run form):\n%v",
			got, rotatedSessionID, spawns[1])
	}
	if slices.Contains(spawns[1], "--resume") {
		t.Errorf("spawn 2 unexpectedly carries --resume — a fresh restart is not a resume:\n%v", spawns[1])
	}

	// spawn 3 crash-respawns and resumes the ROTATED id — never the pre-rotation
	// id (the silent regression the ticket warns about).
	if got := idFlagValue(spawns[2], "--resume"); got != rotatedSessionID {
		t.Errorf("spawn 3 --resume = %q, want rotated %q (respawn regressed to the pre-rotation id):\n%v",
			got, rotatedSessionID, spawns[2])
	}

	for i, args := range spawns {
		// No double-inject: never both id flags on one spawn.
		if idFlagCount(args) != 1 {
			t.Errorf("spawn %d carries %d id flags, want exactly 1 (double-inject / missing flag):\n%v",
				i+1, idFlagCount(args), args)
		}
		// After the rotation (spawn 2 onward) the pre-rotation id must not resurface
		// anywhere in the argv — not as --resume <old>, not at all.
		if i >= 1 && slices.Contains(args, testSessionID) {
			t.Errorf("spawn %d references the pre-rotation id %q after the rotation:\n%v", i+1, testSessionID, args)
		}
	}
}

// TestRunner_RestartFresh_NoLiveChildIsSafe covers AC4: RestartFresh called
// before Run starts (no child to cancel, iterCancel is nil) must not panic and
// must land the rotated id on the very first spawn with exactly one id flag. The
// test completing is the no-panic proof.
func TestRunner_RestartFresh_NoLiveChildIsSafe(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	rec := &spawnArgsRecorder{}
	cfg.Logger = slog.New(rec)
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

	// Rotate with no child running: iterCancel is nil, so this is a publish-only
	// path (no cancel to fire, no panic).
	r.RestartFresh(rotatedSessionID)

	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}

	spawns := rec.all()
	if len(spawns) == 0 {
		t.Fatal("no spawn recorded")
	}
	first := spawns[0]
	if got := idFlagValue(first, "--session-id"); got != rotatedSessionID {
		t.Errorf("first spawn --session-id = %q, want pre-Run rotated %q:\n%v", got, rotatedSessionID, first)
	}
	if slices.Contains(first, "--resume") {
		t.Errorf("first spawn unexpectedly carries --resume:\n%v", first)
	}
	if n := idFlagCount(first); n != 1 {
		t.Errorf("first spawn carries %d id flags, want exactly 1 (no partial or duplicated injection):\n%v", n, first)
	}
}

// TestRunner_RestartFresh_EmptyIDIsNoOp keeps the deterministic empty-id guard
// from silently rotting: RestartFresh("") must not rotate the id and must not arm
// first-run form, so nextSpawnID still returns the original id with forceFirst
// false. The runner never spawns --session-id "".
func TestRunner_RestartFresh_EmptyIDIsNoOp(t *testing.T) {
	t.Parallel()
	cfg := Config{
		ClaudeBin: os.Args[0],
		WorkDir:   t.TempDir(),
		SessionID: testSessionID,
		Logger:    slog.New(&spawnArgsRecorder{}), // swallows the Warn record
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r.RestartFresh("")

	id, forceFirst := r.nextSpawnID()
	if id != testSessionID {
		t.Errorf("nextSpawnID id = %q after RestartFresh(%q), want unchanged %q", id, "", testSessionID)
	}
	if forceFirst {
		t.Errorf("nextSpawnID forceFirst = true after RestartFresh(%q), want false (no-op held)", "")
	}
}

// --- #1330: the rotation gate ------------------------------------------------
//
// These three cover the property at the layer that OWNS it. The cmd/pyry test
// (inbound_deliver_rotation_test.go) proves the DISPATCH arms a gate; it runs
// against a fixture gate and therefore cannot say anything about this one's
// single-acquisition check-and-capture, its survival across the teardown, or its
// generation stamp. Each of those is a distinct way to reopen #1330 silently.

// waitSpawn receives one spawn signal or fails the test.
func waitSpawn(t *testing.T, spawned <-chan struct{}, which string) {
	t.Helper()
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", which)
	}
}

// TestRunner_BeginRotation_RefusesTurnWhileChildIsLive is the core assertion: the
// gate refuses a turn while Stdin() is STILL NON-NIL. That non-vacuity guard is
// what distinguishes it from TestRunner_WriteUserTurn_NoLiveChild
// (interface_test.go) — without it an ErrNoLiveChild would prove only that some
// child was absent, which is the pre-existing refusal, not the new one. The live,
// doomed child is exactly the case #1330 exists for.
func TestRunner_BeginRotation_RefusesTurnWhileChildIsLive(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 4)
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

	// onSpawn fires AFTER setStdin (spawnAndWait), so this receive establishes
	// "child 1 is up and its stdin is bound" by program order, with no polling.
	waitSpawn(t, spawned, "the first spawn")
	if r.Stdin() == nil {
		t.Fatal("Stdin() is nil immediately after the spawn; every assertion below would collapse into the pre-existing no-live-child refusal")
	}

	r.BeginRotation()

	// The gate must not have touched the handle: it refuses turns for a child that
	// is still very much alive, which is the whole window #1330 closes.
	if r.Stdin() == nil {
		t.Fatal("Stdin() went nil when the rotation was armed; the gate must leave the handle alone (Interrupt still uses it) — the refusal below would then be vacuous")
	}
	if err := r.WriteUserTurn(context.Background(), "c1", []byte("gate-probe-armed")); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn with a rotation armed = %v, want ErrNoLiveChild; the turn was written into the child RestartFresh is about to kill", err)
	}
	// Zero bytes, not merely a non-nil error: a refusal that writes first and
	// errors afterwards would still hand the doomed child the turn.
	time.Sleep(150 * time.Millisecond)
	if got := out.String(); strings.Contains(got, "gate-probe-armed") {
		t.Errorf("the doomed child echoed the refused turn, so bytes reached it despite the ErrNoLiveChild; stdout:\n%s", got)
	}
}

// TestRunner_BeginRotation_FreshChildClearsTheGate covers the release half: the
// gate is armed until a SUCCESSOR child binds its stdin, and the next spawn is
// what clears it. Releasing on RestartFresh's return instead would narrow the race
// rather than exclude it (that call returns after cancel(); the kill, the Wait and
// the respawn all run later on the Run goroutine), so this asserts through a real
// respawn rather than through the call's return.
func TestRunner_BeginRotation_FreshChildClearsTheGate(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
	spawned := make(chan struct{}, 4)
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

	waitSpawn(t, spawned, "the first spawn")
	r.BeginRotation()
	if err := r.WriteUserTurn(context.Background(), "c1", []byte("gate-probe-armed")); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn with a rotation armed = %v, want ErrNoLiveChild", err)
	}

	// The rotation completes: the runner kills child 1 and respawns under the
	// rotated id. Receiving the second spawn signal means setStdin ran for the
	// fresh child, which is the gate's release point.
	r.RestartFresh(rotatedSessionID)
	waitSpawn(t, spawned, "the post-rotation spawn")

	if err := r.WriteUserTurn(context.Background(), "c1", []byte("gate-probe-fresh")); err != nil {
		t.Fatalf("WriteUserTurn after the fresh child bound = %v, want nil; the gate outlived the rotation and the conversation would refuse turns until the next respawn", err)
	}
	// The fresh child is the only one alive, so its echo proves the bytes landed
	// there and not in the pre-rotation child.
	waitForContains(t, out, "gate-probe-fresh", 5*time.Second)
	if got := out.String(); strings.Contains(got, "gate-probe-armed") {
		t.Errorf("the refused turn reached a child after all; stdout:\n%s", got)
	}
}

// TestRunner_BeginRotation_AbortIsGenerationStamped pins the counter that a
// "simplification" would delete. Two overlapping new_session frames are ordinary
// (#1330's e2e re-sends the frame every ~250 ms): the loser's rotate() fails
// ErrSessionNotFound and it runs its abort, which unstamped would disarm the
// WINNER's gate while the winner's outgoing child is still alive — reproducing the
// defect on demand from two frames, invisibly to any test driving one rotation at
// a time.
//
// It asserts through turnTarget's gated return rather than through a spawned
// child: the property is about the gate's own state, and Run is not needed to
// exercise it.
func TestRunner_BeginRotation_AbortIsGenerationStamped(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, gated := r.turnTarget(); gated {
		t.Fatal("turnTarget reports gated before any rotation was armed; the assertions below could not tell an arm from the initial state")
	}

	abortFirst := r.BeginRotation()  // frame 1 arms, then loses the re-key race
	abortSecond := r.BeginRotation() // frame 2 arms and wins
	if _, gated := r.turnTarget(); !gated {
		t.Fatal("turnTarget reports not-gated with two rotations armed")
	}

	abortFirst()
	if _, gated := r.turnTarget(); !gated {
		t.Fatal("the LOSING rotation's abort disarmed the WINNER's gate: turns would be written into the winner's outgoing child, which is #1330's defect reproduced from two frames")
	}

	abortSecond()
	if _, gated := r.turnTarget(); gated {
		t.Fatal("the winning rotation's own abort did not disarm the gate; a failed rotation would leave the conversation refusing every turn until the next respawn")
	}
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
