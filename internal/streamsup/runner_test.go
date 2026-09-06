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
	"syscall"
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

// --- The by-id transcript probe decides the id flag: #1630 -------------------

// otherSessionID is an UNRELATED session's id, sitting in the same sessions
// directory. It is what makes the probe's by-id-ness observable rather than
// asserted: a directory scan would emit --resume <otherSessionID> where the
// runner's own transcript is absent, and a most-recent-by-mtime scan would name
// it even where the runner's own transcript is present.
const otherSessionID = "77777777-6666-5555-4444-333333333333"

// writeTranscript writes an empty <id>.jsonl fixture into dir with an explicit
// mtime. Contents are irrelevant — transcript.StatByID only stats — and the
// mtime is set rather than left to write order so the "newer <other-id>" rows
// below discriminate deterministically instead of racing a filesystem's
// timestamp granularity. The ".jsonl" suffix is spelled out because the fixture
// stands in for what claude itself writes on disk, not for one of our constants.
func writeTranscript(t *testing.T, dir, id string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write transcript fixture %s: %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes transcript fixture %s: %v", path, err)
	}
}

// TestUseCreateForm_ProbeDecidesIDFlag is the pure half of #1630: it composes
// useCreateForm with buildArgs over a fixture directory and compares the whole
// argv. With no directory the latch decides in both directions (the inert
// fall-back); with one supplied the probe decides OUTRIGHT, overriding the latch
// in both directions — which is the two defects, one row each: a latch saying
// "resume" against an absent transcript is #1655/#1656's crash-loop, and a latch
// saying "create" against a present one is ADR 032's refused first spawn.
//
// The two <other-id> rows are what discriminate a by-id probe from a directory
// scan. Every row additionally pins the absence of --continue, which is a
// REGRESSION PIN and not evidence: --continue has zero occurrences in this
// package (#839 removed it along with the adopt-by-mtime scan), so that clause
// cannot fail against a conforming implementation.
func TestUseCreateForm_ProbeDecidesIDFlag(t *testing.T) {
	t.Parallel()

	// Distinct, ordered mtimes so "newer" is a fact about the fixture rather
	// than about how fast the test wrote two files.
	var (
		older = time.Now().Add(-2 * time.Hour)
		newer = time.Now().Add(-1 * time.Hour)
	)
	base := []string{"--model", "sonnet"}

	tests := []struct {
		name string
		// dir returns the sessions directory to probe; "" means none was
		// supplied, which is the inert path.
		dir         func(t *testing.T) string
		id          string
		latchCreate bool
		wantFlag    string
	}{
		{
			name:        "no dir: a create latch falls back to --session-id",
			dir:         func(*testing.T) string { return "" },
			id:          testSessionID,
			latchCreate: true,
			wantFlag:    "--session-id",
		},
		{
			name:        "no dir: a resume latch falls back to --resume",
			dir:         func(*testing.T) string { return "" },
			id:          testSessionID,
			latchCreate: false,
			wantFlag:    "--resume",
		},
		{
			name: "transcript absent overrides a resume latch (the #1656 crash-loop)",
			dir:  func(t *testing.T) string { return t.TempDir() },
			// The latch says resume — a session that launched, ran no turn and
			// crashed. --resume against an id with no transcript exits 1 forever.
			id:          testSessionID,
			latchCreate: false,
			wantFlag:    "--session-id",
		},
		{
			name: "transcript present overrides a create latch (ADR 032's refused first spawn)",
			dir: func(t *testing.T) string {
				dir := t.TempDir()
				writeTranscript(t, dir, testSessionID, older)
				return dir
			},
			// The latch says create — a daemon restart, first spawn. claude
			// refuses --session-id against a transcript that already exists.
			id:          testSessionID,
			latchCreate: true,
			wantFlag:    "--resume",
		},
		{
			name: "only a NEWER unrelated transcript: still creates by id",
			dir: func(t *testing.T) string {
				dir := t.TempDir()
				writeTranscript(t, dir, otherSessionID, newer)
				return dir
			},
			id:          testSessionID,
			latchCreate: false,
			wantFlag:    "--session-id",
		},
		{
			name: "own transcript beside a NEWER unrelated one: still resumes by id",
			dir: func(t *testing.T) string {
				dir := t.TempDir()
				writeTranscript(t, dir, testSessionID, older)
				writeTranscript(t, dir, otherSessionID, newer)
				return dir
			},
			id: testSessionID,
			// Create, so this row pins the probe overriding the latch as well as
			// the newer neighbour losing to the by-id hit.
			latchCreate: true,
			wantFlag:    "--resume",
		},
		{
			name: "non-existent dir reads absent, never fails the spawn",
			dir: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "no-such-sessions-dir")
			},
			id:          testSessionID,
			latchCreate: false,
			wantFlag:    "--session-id",
		},
		{
			name: "an id ValidStem rejects reads absent, never fails the spawn",
			dir:  func(t *testing.T) string { return t.TempDir() },
			// Not a canonical UUID stem. New checks SessionID for non-emptiness
			// only, so such an id really does reach the probe.
			id:          "sess-abc",
			latchCreate: false,
			wantFlag:    "--session-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := tt.dir(t)

			got := buildArgs(base, useCreateForm(dir, tt.id, tt.latchCreate), tt.id)
			want := []string{
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--verbose",
				"--model", "sonnet",
				tt.wantFlag, tt.id,
			}
			if !slices.Equal(got, want) {
				t.Errorf("argv\n got  = %v\n want = %v", got, want)
			}
			// Neither the flag nor the id may ever name the unrelated session:
			// the id is always the one passed in, never one read out of the dir.
			if slices.Contains(got, otherSessionID) {
				t.Errorf("argv names the unrelated session %q — the probe scanned the directory:\n%v",
					otherSessionID, got)
			}
			// Regression pin, not evidence: #839 removed --continue with the
			// adopt-by-mtime scan, and no fall-back may reintroduce either.
			if slices.Contains(got, "--continue") {
				t.Errorf("argv carries --continue, removed by #839:\n%v", got)
			}
		})
	}
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

// TestRunner_BeginSpawn_FirstSpawnResumesExistingTranscript is the wiring pin
// for #1630: TestUseCreateForm_ProbeDecidesIDFlag proves the decision, this
// proves beginSpawn actually feeds that decision to buildArgs instead of passing
// its firstRun argument straight through. It calls beginSpawn with firstRun
// TRUE — the latch saying "create" — against a directory that already holds this
// session's transcript, which is a daemon restart: the argv must resume, because
// claude refuses --session-id against a live transcript (ADR 032).
//
// Constructs the Runner directly (as TestRunner_SpawnSetupFailureRetainsSessionID
// does) so no child process is involved and the argv is observed at the source.
func TestRunner_BeginSpawn_FirstSpawnResumesExistingTranscript(t *testing.T) {
	t.Parallel()
	sessionsDir := t.TempDir()
	writeTranscript(t, sessionsDir, testSessionID, time.Now())

	r := &Runner{
		cfg: Config{
			SessionID:         testSessionID,
			ClaudeSessionsDir: sessionsDir,
		},
		sessionID: testSessionID,
	}

	_, cancel, args, _, forceFirst, _, _ := r.beginSpawn(context.Background(), true)
	defer cancel()

	if forceFirst {
		t.Errorf("beginSpawn reported forceFirst with no rotation pending")
	}
	if got := idFlagValue(args, "--resume"); got != testSessionID {
		t.Errorf("first spawn --resume = %q, want %q — beginSpawn passed firstRun through "+
			"instead of consulting the probe:\n%v", got, testSessionID, args)
	}
	if slices.Contains(args, "--session-id") {
		t.Errorf("first spawn carries --session-id against an existing transcript, which claude "+
			"refuses (ADR 032):\n%v", args)
	}
	if got := idFlagCount(args); got != 1 {
		t.Errorf("first spawn carries %d id flags, want exactly 1:\n%v", got, args)
	}
}

// testSessionEnvVar is a name the production daemon does not use, so a runner that
// hardcoded PYRY_SESSION_ID instead of honouring Config.SessionIDEnvVar would fail
// the tests below rather than pass them by coincidence.
const testSessionEnvVar = "TEST_SESSION_ID"

// TestSpawnEnv covers the composer's obligations either side of the happy path: an
// unset name adds nothing at all, and the caller's Config.Env is never appended
// INTO. The second is not hypothetical — Config.Env belongs to the caller and is
// read by every spawn of the runner, so an in-place append into spare capacity is
// a write one spawn could observe in another.
func TestSpawnEnv(t *testing.T) {
	t.Parallel()

	t.Run("unset name adds nothing", func(t *testing.T) {
		if got := spawnEnv(nil, "", testSessionID); got != nil {
			t.Errorf("spawnEnv(nil, \"\", …) = %q, want nil — an unset name must leave cmd.Env inheriting implicitly", got)
		}
		base := []string{"A=1"}
		if got := spawnEnv(base, "", testSessionID); !slices.Equal(got, base) {
			t.Errorf("spawnEnv(base, \"\", …) = %q, want %q unchanged", got, base)
		}
	})

	t.Run("binds the id to the name", func(t *testing.T) {
		want := []string{testSessionEnvVar + "=" + testSessionID}
		if got := spawnEnv(nil, testSessionEnvVar, testSessionID); !slices.Equal(got, want) {
			t.Errorf("spawnEnv = %q, want %q", got, want)
		}
	})

	t.Run("never appends into the caller's slice", func(t *testing.T) {
		// Spare capacity is what makes this discriminating: append into a full slice
		// reallocates and hides the aliasing, so the fixture hands the composer room
		// to write in place if it is going to.
		base := make([]string, 1, 4)
		base[0] = "A=1"

		first := spawnEnv(base, testSessionEnvVar, "id-one")
		second := spawnEnv(base, testSessionEnvVar, "id-two")

		if want := []string{"A=1", testSessionEnvVar + "=id-one"}; !slices.Equal(first, want) {
			t.Errorf("the first spawn's env = %q, want %q — the second composition overwrote it", first, want)
		}
		if want := []string{"A=1", testSessionEnvVar + "=id-two"}; !slices.Equal(second, want) {
			t.Errorf("the second spawn's env = %q, want %q", second, want)
		}
		if len(base) != 1 {
			t.Errorf("the caller's slice grew to %q — the composer appended into it", base)
		}
	})
}

// TestRunner_BeginSpawn_EnvCarriesOwnLiveSessionID (#2169 AC-2) pins the identity
// the daemon puts on a claude child: each spawn's environment names the session
// THAT spawn is, and no other.
//
// TWO runners, not one, and that is the design of the test. A single-runner
// assertion is satisfied by a composer that hardcodes a constant and ignores the
// snapshot entirely — exactly the failure that matters here, since the pyry_files
// MCP server claude forks reads this variable to name where a handed-over file is
// filed, and an id naming the wrong live session misfiles it.
//
// Constructs the Runners directly (as TestRunner_BeginSpawn_FirstSpawnResumes-
// ExistingTranscript does) so no child process is involved and the environment is
// observed at the source that composes it.
func TestRunner_BeginSpawn_EnvCarriesOwnLiveSessionID(t *testing.T) {
	t.Parallel()

	// Canonical UUIDv4s, the shape sessions.NewID mints and sessions.ValidID gates:
	// the only values that reach this seam in production.
	const (
		idA = "11111111-1111-4111-8111-111111111111"
		idB = "22222222-2222-4222-8222-222222222222"
	)
	newRunner := func(id string) *Runner {
		return &Runner{
			cfg:       Config{SessionID: id, SessionIDEnvVar: testSessionEnvVar},
			log:       discardLogger(),
			sessionID: id,
		}
	}

	for _, tc := range []struct {
		name       string
		own, other string
	}{
		{"runner A", idA, idB},
		{"runner B", idB, idA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cancel, _, env, _, _, _ := newRunner(tc.own).beginSpawn(context.Background(), true)
			defer cancel()

			want := []string{testSessionEnvVar + "=" + tc.own}
			if !slices.Equal(env, want) {
				t.Fatalf("env = %q, want %q", env, want)
			}
			// Named separately from the equality above so a future env carrying a
			// second variable still fails on the claim that matters: this child must
			// not be able to name the other session.
			if slices.Contains(env, testSessionEnvVar+"="+tc.other) {
				t.Errorf("env carries the OTHER session's identity: %q", env)
			}
		})
	}
}

// TestRunner_BeginSpawn_EnvTracksRotatedSessionID is #2169's rework pin: the
// identity on the child's environment is the id THAT SPAWN's argv carries, not the
// construction-time seed.
//
// The seed is the defect. Config.SessionID is construction-fixed — the field's own
// site in internal/sessions → Pool.New says it does not mirror a rotation — while
// RestartFresh rotates r.sessionID and respawns. Composing the variable from the
// seed one layer up left every successor child's environment naming the retired
// session while its argv named the live one, and the daemon refuses a retired id,
// so send_file failed closed from the first new_session onward for the rest of that
// session's life.
//
// Asserting that the argv and the environment AGREE, rather than merely that the
// environment changed, is what makes it a by-construction claim: both are derived
// from the single id beginSpawn snapshots under restartMu.
func TestRunner_BeginSpawn_EnvTracksRotatedSessionID(t *testing.T) {
	t.Parallel()

	const rotatedID = "33333333-3333-4333-8333-333333333333"
	r := &Runner{
		cfg:       Config{SessionID: testSessionID, SessionIDEnvVar: testSessionEnvVar},
		log:       discardLogger(),
		sessionID: testSessionID,
		restartCh: make(chan struct{}, 1),
	}

	r.RestartFresh(rotatedID)

	_, cancel, args, env, forceFirst, _, _ := r.beginSpawn(context.Background(), false)
	defer cancel()

	// Sanity on the fixture, not the claim: without a consumed rotation the rest of
	// the test would be asserting against the seed and passing for the wrong reason.
	if !forceFirst {
		t.Fatal("beginSpawn reported no pending rotation after RestartFresh — the fixture never rotated")
	}
	if got := idFlagValue(args, "--session-id"); got != rotatedID {
		t.Fatalf("argv --session-id = %q, want the rotated id %q:\n%v", got, rotatedID, args)
	}

	want := []string{testSessionEnvVar + "=" + rotatedID}
	if !slices.Equal(env, want) {
		t.Fatalf("env = %q, want %q — the environment names a different session than the argv does", env, want)
	}
	if slices.Contains(env, testSessionEnvVar+"="+testSessionID) {
		t.Errorf("env still names the retired session %q, which the daemon refuses: %q", testSessionID, env)
	}
}

// TestRunner_RestartFresh_ProbeDecidesPerSpawn is #1630's per-spawn pin: the id
// flag is decided on every spawn, not once at construction. The fixture is
// deliberately ASYMMETRIC — only the PRE-rotation id has a transcript — because
// that is the one arrangement that discriminates. A decision computed once would
// carry spawn 1's "present ⇒ resume" answer into the rotated id and emit
// --resume <rotated>; a fixture where both ids are absent passes under a
// per-spawn probe and a construction-time one alike, proving nothing.
//
//	spawn 1: --resume     <old>   (present ⇒ resume, on the FIRST spawn)
//	spawn 2: --session-id <new>   (rotated id, absent ⇒ create)
//
// Two spawns, not three: the fake child writes no transcript, so spawn 3 is also
// --session-id <new> — correct and convergent, but it asserts nothing further.
func TestRunner_RestartFresh_ProbeDecidesPerSpawn(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "crash", out, stderr)
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond

	// A directory the test owns, holding ONLY the pre-rotation id's transcript.
	sessionsDir := t.TempDir()
	writeTranscript(t, sessionsDir, testSessionID, time.Now())
	cfg.ClaudeSessionsDir = sessionsDir

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

	deadline := time.Now().Add(5 * time.Second)
	for rec.count() < 2 {
		if time.Now().After(deadline) {
			cancel()
			join()
			t.Fatalf("saw only %d spawn(s), want ≥2", rec.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	join()

	spawns := rec.all()

	// Spawn 1: the probe finds the pre-rotation transcript and resumes it, even
	// though the firstRun latch says create.
	if got := idFlagValue(spawns[0], "--resume"); got != testSessionID {
		t.Errorf("spawn 1 --resume = %q, want %q (the probe did not see the existing transcript):\n%v",
			got, testSessionID, spawns[0])
	}
	if slices.Contains(spawns[0], "--session-id") {
		t.Errorf("spawn 1 unexpectedly carries --session-id:\n%v", spawns[0])
	}

	// Spawn 2: the rotated id has no transcript, so this spawn creates. A
	// construction-time decision would have re-emitted spawn 1's resume answer.
	if got := idFlagValue(spawns[1], "--session-id"); got != rotatedSessionID {
		t.Errorf("spawn 2 --session-id = %q, want rotated %q — the flag was decided once, "+
			"not per spawn:\n%v", got, rotatedSessionID, spawns[1])
	}
	if slices.Contains(spawns[1], "--resume") {
		t.Errorf("spawn 2 unexpectedly carries --resume against an id with no transcript:\n%v", spawns[1])
	}
	if slices.Contains(spawns[1], testSessionID) {
		t.Errorf("spawn 2 references the pre-rotation id %q after the rotation:\n%v",
			testSessionID, spawns[1])
	}

	for i, args := range spawns[:2] {
		if got := idFlagCount(args); got != 1 {
			t.Errorf("spawn %d carries %d id flags, want exactly 1:\n%v", i+1, got, args)
		}
	}
}

// swapMarkerArg is the distinctive value SetSpawnArgs installs, so a spawn's argv
// says on its face which install it came from.
const swapMarkerArg = "swapmarker"

// TestRunner_SetSpawnArgs_InstallsWithoutKillingLiveChild proves both halves of
// the swap-only install against ONE live child (#1580):
//
//	(a) the install disturbs nothing — no further spawn is even attempted, and the
//	    child running before the call is the one still running after it;
//	(b) once that child ends, the next spawn re-execs with the newly installed argv.
//
// The two assertions in (a) are both load-bearing: the spawn count alone permits
// "no new spawn AND the old child died", and the pid alone permits a spawn attempt
// that failed setup. rec.count() records at the "spawning claude" log site, which
// fires BEFORE cmd.Start, so it observes attempts and not merely launches.
//
// The child in (b) is ended by signalling its pid directly rather than by calling
// anything on the Runner, which is what makes the new argv attributable to
// SetSpawnArgs alone: no other install path was invoked, so the marker can have
// come from nowhere else. (Restart is disqualified as the ending mechanism for
// exactly that reason — it installs argv itself.) Signalling a captured pid is
// sound only because record_block NEVER self-exits, so the pid cannot have been
// reaped and recycled onto an unrelated process before the signal lands; switching
// this test to a self-exiting mode (e.g. "crash") would silently turn the Kill
// into a signal aimed at whatever now owns that pid.
//
// Half (a)'s discriminator is the reap seam, NOT the settle window. A kill can
// only reach the child by cancelling the iteration ctx, and os/exec invokes
// cmd.Cancel on exactly that; reapDescendantGroupsFn fires first inside it,
// synchronously, before the SIGTERM. So a stub at that seam observes an attempted
// kill one goroutine wakeup after the offending call, whatever the child then does
// about the signal. Swapping it is also what keeps the count and pid assertions
// honest: the real reaper shells out to ps, which under -race stretched
// kill→respawn to ~1.03s and let a killing install slip through a 250ms settle
// with Phase still running and the pid unchanged (#1580 review). With the seam
// stubbed that ps is off the path, a killed child respawns in milliseconds, and
// the settle bounds a goroutine wakeup rather than a subprocess.
//
// The RestartCount assertion in (b) pins the OTHER forbidden kill-half effect, the
// restartCh hint, which no other assertion here can see: an install that leaked a
// token would kill nothing and still deliver the marker, so (a) and (b) both stay
// green, but drainRestart would then report true and Run would skip the backoff
// block — leaving the counter at 0 instead of 1 and silently suppressing the next
// crash-respawn's backoff. Asserting it is also why this test must NOT copy
// TestRunner_LiveRestart's RestartCount == 0: that holds because a Restart DOES
// send the hint, whereas an externally-ended child sends none, so Run takes the
// backoff path and the counter increments.
//
// Non-parallel: swapReapSeam mutates a package var.
func TestRunner_SetSpawnArgs_InstallsWithoutKillingLiveChild(t *testing.T) {
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "record_block", out, stderr)
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
	rec := &spawnArgsRecorder{}
	cfg.Logger = slog.New(rec)
	pids := make(chan int, 8)
	cfg.onSpawn = func(pid int) {
		select {
		case pids <- pid:
		default:
		}
	}

	reapRec := &reapRecorder{}
	swapReapSeam(t, reapRec)

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	// stop is idempotent so the deferred teardown (which covers every t.Fatalf
	// below) and the explicit one that inspects Run's error cannot both join —
	// the second join would block until runInBackground's own 10s deadline.
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		return join()
	}
	defer func() { _ = stop() }()

	var pid int
	select {
	case pid = <-pids:
	case <-time.After(5 * time.Second):
		t.Fatal("first child never spawned")
	}

	r.SetSpawnArgs([]string{"--model", swapMarkerArg})

	// Half (a). The window only has to cover the os/exec watcher goroutine waking
	// on a cancelled iteration ctx and calling cmd.Cancel — microseconds — because
	// the reap seam below is what an attempted kill trips. It is generous for the
	// count and pid assertions too: with the seam stubbed, a killed child respawns
	// at the 1ms backoff set above rather than behind the real reaper's ps.
	time.Sleep(250 * time.Millisecond)
	if calls := reapRec.calls(); len(calls) != 0 {
		t.Fatalf("reap seam fired %v after SetSpawnArgs — the install cancelled the iteration ctx, i.e. it killed the child",
			calls)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("saw %d spawn attempt(s) after SetSpawnArgs, want 1 — the install relaunched the child:\n%v",
			got, rec.all())
	}
	if got := r.State().ChildPID; got != pid {
		t.Fatalf("ChildPID = %d after SetSpawnArgs, want the pre-call child %d — the install did not leave it running",
			got, pid)
	}

	// Half (b). Nothing on the Runner is called from here to the next spawn.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM child %d: %v", pid, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for rec.count() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("saw only %d spawn(s) after the child was ended, want ≥2", rec.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// No hint was sent, so Run took the backoff path on the way to spawn 2 and
	// counted this as a crash-respawn. Reading it after the spawn is observed is
	// ordered, not racy: the increment happens in the backoff block Run must pass
	// through before beginSpawn.
	if got := r.State().RestartCount; got < 1 {
		t.Errorf("RestartCount = %d after the child was ended, want >=1 — SetSpawnArgs left a restartCh token, so the respawn skipped backoff", got)
	}
	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after teardown", err)
	}

	spawns := rec.all()
	if slices.Contains(spawns[0], swapMarkerArg) {
		t.Errorf("spawn 1 already carries the swap marker — it predates SetSpawnArgs:\n%v", spawns[0])
	}
	if !slices.Contains(spawns[1], swapMarkerArg) {
		t.Errorf("spawn 2 missing the installed --model %s — the swap did not reach the next spawn:\n%v",
			swapMarkerArg, spawns[1])
	}
	// The swap must not perturb the id form: this is an ordinary continuation of
	// the same session, so spawn 2 resumes rather than re-establishing.
	if got := idFlagValue(spawns[1], "--resume"); got != testSessionID {
		t.Errorf("spawn 2 --resume = %q, want %q:\n%v", got, testSessionID, spawns[1])
	}
	if slices.Contains(spawns[1], "--session-id") {
		t.Errorf("spawn 2 unexpectedly carries --session-id:\n%v", spawns[1])
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
// first-run form, so the next beginSpawn still builds --session-id <original> with
// forceFirst false. The runner never spawns --session-id "".
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

	_, cancel, args, _, forceFirst, _, _ := r.beginSpawn(context.Background(), true)
	defer cancel()
	if forceFirst {
		t.Errorf("beginSpawn forceFirst = true after RestartFresh(%q), want false (no-op held)", "")
	}
	if got := idFlagValue(args, "--session-id"); got != testSessionID {
		t.Errorf("beginSpawn argv --session-id = %q after RestartFresh(%q), want unchanged %q:\n%v",
			got, "", testSessionID, args)
	}
	if slices.Contains(args, "--resume") {
		t.Errorf("beginSpawn argv unexpectedly carries --resume:\n%v", args)
	}
}

// TestRunner_RestartFresh_FiresOnSessionRotate covers the #1133 rotation
// notification per outcome: an accepted rotation fires the callback exactly once
// with the new id, the refused empty id fires it zero times (so a consumer's tag
// can never be emptied by a call the runner itself declined), and a nil callback
// is not a panic on either path.
//
// It asserts on the RUNNER, not on a consumer: what the callback is used for lives
// in cmd/pyry, and this package's obligation is only that it fires once per
// accepted rotation with the id that was accepted.
func TestRunner_RestartFresh_FiresOnSessionRotate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		newID    string
		wantFire []string
	}{
		{name: "accepted rotation fires once", newID: rotatedSessionID, wantFire: []string{rotatedSessionID}},
		{name: "refused empty id never fires", newID: "", wantFire: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// mu guards got: RestartFresh fires the callback on THIS goroutine, but the
			// runner is never Run here, so the lock is documentation of the contract
			// rather than a live race guard. -race keeps it honest either way.
			var mu sync.Mutex
			var got []string
			cfg := Config{
				ClaudeBin: os.Args[0],
				WorkDir:   t.TempDir(),
				SessionID: testSessionID,
				Logger:    slog.New(&spawnArgsRecorder{}), // swallows the empty-id Warn
				OnSessionRotate: func(newID string) {
					mu.Lock()
					defer mu.Unlock()
					got = append(got, newID)
				},
			}
			r, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			r.RestartFresh(tc.newID)

			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(got, tc.wantFire) {
				t.Fatalf("OnSessionRotate fired with %q, want %q", got, tc.wantFire)
			}
		})
	}
}

// TestRunner_RestartFresh_OnSessionRotateNilIsSafe pins the nil-check at the fire
// site. Every construction path other than newStreamRunnerFactory leaves the field
// nil, so a missing guard would panic on the first new_session of any other
// consumer — including every test in this package that omits the field.
func TestRunner_RestartFresh_OnSessionRotateNilIsSafe(t *testing.T) {
	t.Parallel()
	cfg := Config{
		ClaudeBin: os.Args[0],
		WorkDir:   t.TempDir(),
		SessionID: testSessionID,
		Logger:    slog.New(&spawnArgsRecorder{}),
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Both arms: the refused id returns above the fire site, the accepted one runs
	// through it. The test completing is the no-panic proof.
	r.RestartFresh("")
	r.RestartFresh(rotatedSessionID)
}

// TestRunner_RestartFresh_OnSessionRotateFiresBeforeNextSpawnID is the ordering
// pin the whole design rests on: the callback must observe the rotation BEFORE the
// runner hands the new id to a spawn. Fire it after the teardown cancel instead and
// the successor child's first events are tagged with the id the rotation replaced —
// which is the exact defect #1133 exists to remove, reintroduced on the other side
// of the rotation.
//
// beginSpawn stands in for "the successor spawn" because it is the single point
// that consumes the rotation (rotatePending) and yields the argv the child is
// launched with — reaching it at all means the runner considers the rotation
// spent, so a callback that has not fired by then is provably too late.
func TestRunner_RestartFresh_OnSessionRotateFiresBeforeNextSpawnID(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	firedWith := ""
	cfg := Config{
		ClaudeBin: os.Args[0],
		WorkDir:   t.TempDir(),
		SessionID: testSessionID,
		Logger:    slog.New(&spawnArgsRecorder{}),
		OnSessionRotate: func(newID string) {
			mu.Lock()
			defer mu.Unlock()
			firedWith = newID
		},
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r.RestartFresh(rotatedSessionID)

	mu.Lock()
	seen := firedWith
	mu.Unlock()
	if seen != rotatedSessionID {
		t.Fatalf("OnSessionRotate had not fired with %q before the next spawn was built (saw %q)",
			rotatedSessionID, seen)
	}

	_, cancel, args, _, forceFirst, _, _ := r.beginSpawn(context.Background(), true)
	defer cancel()
	if !forceFirst {
		t.Error("beginSpawn forceFirst = false after RestartFresh, want true (the rotation was not the one consumed)")
	}
	if got := idFlagValue(args, "--session-id"); got != rotatedSessionID {
		t.Errorf("beginSpawn argv --session-id = %q, want the rotated %q — the callback and the spawn disagree on the id:\n%v",
			got, rotatedSessionID, args)
	}
}

// --- #1481: a racer on the spawn-setup seam cannot miss both -----------------
//
// The window #1481 closed was the gap between "the spawn id was read" and "this
// iteration's cancel was published": a Restart/RestartFresh landing there wrote
// its rotation, read a nil cancel, cancelled nothing, and the spawn proceeded
// with the pre-rotation id — a live child under an id the pool had already
// abandoned. These two tests assert the invariant per racer.

// raceModelFlagValue is the pass-through argv token Restart swaps in mid-spawn-
// setup; helperChild dispatches on the env, not the argv, so an extra flag is
// inert to the fake claude and only has to be visible in the recorded argv.
const raceModelFlagValue = "sonnet-1481-test"

// spawnLogHook wraps spawnArgsRecorder and fires hook exactly once, on the
// "spawning claude" record. slog.Handler.Handle runs SYNCHRONOUSLY on the Run
// goroutine, so the hook lands a racer at a FIXED point in the spawn-setup
// statement order — no sleep, no repetition, no window calibration.
//
// On the pre-#1481 shape that point sits strictly inside the window (the id was
// read by the first restartMu acquisition; the cancel is published by a second
// one further down), so the racer reads a nil cancel and misses both. On the
// shipped tree the whole section has already run before the log, so the racer
// finds a live cancel and tears the spawn down — the branch the fix relies on.
type spawnLogHook struct {
	*spawnArgsRecorder
	once sync.Once
	hook func()
}

func (h *spawnLogHook) Handle(ctx context.Context, rec slog.Record) error {
	if err := h.spawnArgsRecorder.Handle(ctx, rec); err != nil {
		return err
	}
	if rec.Message == "spawning claude" {
		h.once.Do(h.hook)
	}
	return nil
}

// Overridden rather than promoted from the embedded recorder: the promoted
// versions return the INNER handler, which would silently drop the hook if a
// caller ever derived a logger via .With().
func (h *spawnLogHook) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *spawnLogHook) WithGroup(string) slog.Handler      { return h }

// launchRecorder answers "which spawn ATTEMPTS actually launched a child". Run
// is single-goroutine and logs "spawning claude" strictly before cmd.Start,
// while onSpawn fires strictly after it, so inside onSpawn the last argv the
// recorder captured is this child's. An attempt torn down before cmd.Start
// (spawnAndWait's started == false) leaves no entry — which is exactly the
// distinction the #1481 invariant is stated over.
type launchRecorder struct {
	rec     *spawnArgsRecorder
	mu      sync.Mutex
	argv    [][]string
	spawned chan struct{}
}

func newLaunchRecorder(rec *spawnArgsRecorder) *launchRecorder {
	return &launchRecorder{rec: rec, spawned: make(chan struct{}, 4)}
}

// onSpawn is the Config.onSpawn seam.
func (l *launchRecorder) onSpawn(int) {
	all := l.rec.all()
	l.mu.Lock()
	l.argv = append(l.argv, all[len(all)-1])
	l.mu.Unlock()
	select {
	case l.spawned <- struct{}{}:
	default:
	}
}

func (l *launchRecorder) all() [][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]string(nil), l.argv...)
}

// waitLaunch blocks until one child has launched. The timeout is a FAILURE
// BOUND, not a calibration: the interleaving is fixed by the log hook, so a
// healthy run reaches this in milliseconds and only a broken one waits.
func (l *launchRecorder) waitLaunch(t *testing.T) {
	t.Helper()
	select {
	case <-l.spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("no child launched within 5s")
	}
}

// TestRunner_RestartFreshRacesSpawnSetup lands a RestartFresh at the one point
// inside the old window that is synchronously interceptable, and asserts the
// #1481 invariant: no child ever runs under the pre-rotation id. The rotation is
// either observed by the spawn being set up (branch (a)) or cancels it (branch
// (b)); "live child under the old id AND no live iteration cancel" is
// unreachable.
//
// record_block keeps the launched child alive until SIGTERM, so the set of
// children that actually launched is stable rather than churning through
// crash-respawns.
func TestRunner_RestartFreshRacesSpawnSetup(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "record_block", out, stderr)
	rec := &spawnArgsRecorder{}
	lr := newLaunchRecorder(rec)
	cfg.onSpawn = lr.onSpawn
	var r *Runner
	cfg.Logger = slog.New(&spawnLogHook{
		spawnArgsRecorder: rec,
		hook:              func() { r.RestartFresh(rotatedSessionID) },
	})

	var err error
	r, err = New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	lr.waitLaunch(t)
	cancel()
	join()

	launched := lr.all()
	if len(launched) == 0 {
		t.Fatal("no child launched")
	}
	// The invariant, stated positively. Branch-agnostic: it holds under both
	// orderings AC1 permits, so it is the assertion that must never be weakened.
	for i, args := range launched {
		if got := idFlagValue(args, "--session-id"); got != rotatedSessionID {
			t.Errorf("launched child %d --session-id = %q, want rotated %q — a child launched under a "+
				"pre-rotation id while the rotation was neither observed nor cancelled (#1481):\n%v",
				i+1, got, rotatedSessionID, args)
		}
		if slices.Contains(args, testSessionID) {
			t.Errorf("launched child %d references the pre-rotation id %q:\n%v", i+1, testSessionID, args)
		}
		// Exactly one id flag, in --session-id form: started == false on the
		// cancelled attempt must not advance firstRun, so the rotated id is still
		// established rather than --resume'd against a session that never existed.
		if n := idFlagCount(args); n != 1 {
			t.Errorf("launched child %d carries %d id flags, want exactly 1:\n%v", i+1, n, args)
		}
	}

	// Branch (b)'s mechanism: the racer found the published cancel, so attempt 1
	// was torn down before cmd.Start and launched nothing, and attempt 2 is the
	// only live child. This pins the PRESCRIBED statement order (the log sits
	// after the fused section); AC1 itself permits branch (a), which the loop
	// above would still pass.
	if attempts, live := rec.count(), len(launched); attempts != 2 || live != 1 {
		t.Errorf("saw %d spawn attempt(s) and %d launched child(ren), want 2 and 1 — the racing "+
			"RestartFresh must cancel the iteration it raced, leaving that attempt started == false",
			attempts, live)
	}

	// The immediate-relaunch path (drainRestart → continue) skips the only site
	// that increments RestartCount: a cancelled spawn is a restart, not a crash.
	if got := r.State().RestartCount; got != 0 {
		t.Errorf("RestartCount = %d, want 0 (a racing restart must not take the crash backoff)", got)
	}
}

// TestRunner_RestartRacesSpawnSetup is the argv half of the same invariant
// (AC2): a Restart landing on the spawn-setup seam either has its swapped argv
// picked up by the spawn being set up, or cancels it so the immediate relaunch
// picks it up. A settings swap never waits for the child's next natural death.
func TestRunner_RestartRacesSpawnSetup(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "record_block", out, stderr)
	rec := &spawnArgsRecorder{}
	lr := newLaunchRecorder(rec)
	cfg.onSpawn = lr.onSpawn
	var r *Runner
	cfg.Logger = slog.New(&spawnLogHook{
		spawnArgsRecorder: rec,
		hook:              func() { r.Restart([]string{"--model", raceModelFlagValue}) },
	})

	var err error
	r, err = New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	lr.waitLaunch(t)
	cancel()
	join()

	launched := lr.all()
	if len(launched) == 0 {
		t.Fatal("no child launched")
	}
	for i, args := range launched {
		if got := idFlagValue(args, "--model"); got != raceModelFlagValue {
			t.Errorf("launched child %d --model = %q, want %q — a child launched under the pre-swap "+
				"argv while the swap was neither observed nor cancelled (#1481):\n%v",
				i+1, got, raceModelFlagValue, args)
		}
		// Restart rotates flags, not identity: the id stays put, in first-run form
		// because the cancelled attempt never advanced firstRun.
		if got := idFlagValue(args, "--session-id"); got != testSessionID {
			t.Errorf("launched child %d --session-id = %q, want %q (Restart must not rotate the id):\n%v",
				i+1, got, testSessionID, args)
		}
		if n := idFlagCount(args); n != 1 {
			t.Errorf("launched child %d carries %d id flags, want exactly 1:\n%v", i+1, n, args)
		}
	}

	if attempts, live := rec.count(), len(launched); attempts != 2 || live != 1 {
		t.Errorf("saw %d spawn attempt(s) and %d launched child(ren), want 2 and 1 — the racing "+
			"Restart must cancel the iteration it raced, leaving that attempt started == false",
			attempts, live)
	}
	if got := r.State().RestartCount; got != 0 {
		t.Errorf("RestartCount = %d, want 0 (a racing restart must not take the crash backoff)", got)
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

// waitReadyMarkers blocks until b holds at least n echo_lines READY markers.
//
// onSpawn is NOT a write barrier: spawnAndWait signals it right after cmd.Start
// returns, before the child has run an instruction of its own, so it orders that
// child's EXISTENCE and never its first write. helperChild's echo_lines arm emits
// its marker only once a whole exec + Go runtime start has completed in the fresh
// process. The only thing standing between the signal and a Restart-driven kill is
// cmd.Cancel's reap, which forks ps and parses the process table first — an
// accidental grace window that is wide enough for the child to usually win
// (measured at ~17-22 ms of child uptime here) but is unbounded and unsynchronised,
// so under full-suite load the child sometimes loses. cmd.Wait drains only what the
// child actually WROTE, so a child killed before its marker never writes it at all:
// the loss is permanent for that run, and a later count reads short rather than
// late (#1968).
//
// So: call this before killing any child whose marker a later count assertion
// needs. Waiting for at least n keeps it a precondition rather than an assertion —
// the exact count that attributes an echo to a particular child stays at the call
// site that cares.
func waitReadyMarkers(t *testing.T, b *safeBuffer, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := strings.Count(b.String(), "READY"); got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d READY markers, saw %d; got:\n%s",
				n, strings.Count(b.String(), "READY"), b.String())
		}
		time.Sleep(10 * time.Millisecond)
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

// --- #1482: the release side is authorised -----------------------------------
//
// #1330 armed the gate and stamped the ARM's generation; the release side stayed
// unauthorised, so any child that bound ended a window it had no relationship to.
// These two rows pin the authorisation from both directions.

// Payload markers for the rows below. Distinct literals so the parent-level
// "the refused bytes never reached a child" assertion cannot match the successor's
// echo by accident.
const (
	unauthorisedTurnProbe = "gate-probe-unauthorised"
	successorTurnProbe    = "gate-probe-successor"
)

// TestRunner_BeginRotation_UnauthorisedBindLeavesTheGateArmed drives both halves of
// the authorisation in ONE run: while a rotation stands, a child that binds from a
// spawn set up BEFORE that rotation's RestartFresh landed leaves the gate armed —
// it is not the successor, it is a child RestartFresh is about to kill — and the
// successor itself still brings the gate down.
//
// The subtests are load-bearing rather than cosmetic. Against a mutant restoring
// setStdin's unconditional clear, (a) fails while (b) still executes and passes,
// which is the per-row evidence the discrimination matrix needs; one flat function
// would collapse both into a single verdict.
//
// echo_lines stays alive until it is killed, so no crash-respawn can race the arm:
// the arm is strictly before every later beginSpawn by program order.
func TestRunner_BeginRotation_UnauthorisedBindLeavesTheGateArmed(t *testing.T) {
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

	// onSpawn fires AFTER setStdin, so every receive here is a happens-after edge on
	// that child's release point — no polling, no window calibration.
	waitSpawn(t, spawned, "the first spawn")
	// The marker needs its own barrier on top of that edge: Restart below kills this
	// child, and onSpawn does not order the child's own first write against the kill
	// — see waitReadyMarkers. Without this the marker is lost outright and (b)'s
	// count reads one short, intermittently and only under load (#1968).
	waitReadyMarkers(t, out, 1)
	r.BeginRotation()

	t.Run("an unauthorised bind leaves the gate armed", func(t *testing.T) {
		// Restart kills child 1 and relaunches at once (hint in hand, no backoff).
		// That respawn's beginSpawn runs while the gate stands and before any
		// RestartFresh, so its snapshot cannot exceed the arm's threshold: it is the
		// Restart-driven shape of "set up before the rotation's RestartFresh landed",
		// the same position the crash/backoff respawn occupies in the row below.
		r.Restart(nil)
		waitSpawn(t, spawned, "the Restart respawn")
		// ABOVE the assertions, not below them, and that placement is load-bearing:
		// under the #1482 mutant the first assertion fatals, so a barrier under it
		// would never run, (b)'s RestartFresh would kill this child at today's width,
		// and (b) would redden on the mutant — destroying the per-row discrimination
		// this test exists to record. The wait above already put the count at 1, so
		// only this child's own marker can satisfy 2.
		waitReadyMarkers(t, out, 2)

		if r.Stdin() == nil {
			t.Fatal("Stdin() is nil after the Restart respawn; both assertions below would collapse into the pre-existing no-live-child refusal, which passes with or without the authorisation")
		}
		if _, gated := r.turnTarget(); !gated {
			t.Fatal("an unrelated respawn disarmed the gate: a turn accepted on the strength of the already-fired session_transition{clear} would be written into the child RestartFresh is about to kill, msgqueue would read that successful write as a commit, and the turn would be lost silently")
		}
		if err := r.WriteUserTurn(context.Background(), "c1", []byte(unauthorisedTurnProbe)); !errors.Is(err, ErrNoLiveChild) {
			t.Fatalf("WriteUserTurn = %v, want ErrNoLiveChild; the caller-visible refusal is what keeps the turn queued for the successor", err)
		}
	})

	t.Run("the successor bind drops the gate", func(t *testing.T) {
		// The rotation lands. Child 3's beginSpawn therefore snapshots the bumped
		// counter — strictly greater than the arm's threshold — so it is the one
		// spawn authorised to end this window.
		r.RestartFresh(rotatedSessionID)
		waitSpawn(t, spawned, "the post-rotation spawn")

		if err := r.WriteUserTurn(context.Background(), "c1", []byte(successorTurnProbe)); err != nil {
			t.Fatalf("WriteUserTurn after the successor bound = %v, want nil; the gate outlived its own rotation and the conversation refuses every turn until some later one — a worse outcome than the loss the authorisation fixes", err)
		}
		waitForContains(t, out, successorTurnProbe, 5*time.Second)
		// Three children have existed and the first two are dead, so the echo above
		// can only be the successor's: the turn observably landed in the
		// post-rotation child, not in a pre-rotation one.
		//
		// This child needs no waitReadyMarkers of its own: echo_lines writes its
		// marker before entering the scan loop, both writes cross the same pipe, and
		// one copier appends them to out in order — so the echo cannot be here unless
		// the marker already is. The barriers above cover the two children whose
		// markers a kill could beat; the count stays EXACT, so a fourth spawn the test
		// never drove still reddens this row.
		if got := strings.Count(out.String(), "READY"); got != 3 {
			t.Errorf("saw %d READY lines, want 3 (first spawn, Restart respawn, successor); the echo cannot be attributed to the successor otherwise:\n%s", got, out.String())
		}
	})

	// Asserted at the parent, not inside (a): "the refused bytes never reached a
	// child" needs a happens-AFTER edge, and (b)'s successful echo is a
	// deterministic one — child 2's cmd.Wait returned before child 3 was spawned,
	// and Wait returns only once its stdout copier has drained. Placing it inside
	// (b) would redden (b) under the mutant, where it must stay green; a sleep
	// inside (a) would trade determinism for nothing.
	if got := out.String(); strings.Contains(got, unauthorisedTurnProbe) {
		t.Errorf("the refused turn reached a child after all; stdout:\n%s", got)
	}
}

// TestRunner_BeginRotation_CrashRespawnLeavesTheGateArmed is the filed interleaving
// itself: the child crashes, the backoff ladder respawns it under the PRE-ROTATION
// id inside the gap between BeginRotation and its partner RestartFresh (the whole of
// the pool's rotate — mint, re-key, register, an fsync'd atomic write, then the
// session_transition{clear} fan-out), and that respawn must not end the window.
//
// The arm is placed INSIDE the first spawn's onSpawn under a sync.Once — the shape
// TestRunner_RestartFresh_RotatesThenResumesNewID uses for its rotation — and not
// from the test goroutine after a waitSpawn. The crash child is gone in 20 ms, so a
// test-goroutine arm can land after the respawn has already bound, and the row would
// then be asserting about a bind that PREDATES the arm: green on both trees, proving
// nothing. onSpawn runs on the Run goroutine, so arming there puts the arm strictly
// before child 2's beginSpawn by program order.
func TestRunner_BeginRotation_CrashRespawnLeavesTheGateArmed(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "crash", &safeBuffer{}, &safeBuffer{})
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
	spawned := make(chan struct{}, 4)
	var (
		r    *Runner
		once sync.Once
	)
	cfg.onSpawn = func(int) {
		once.Do(func() { r.BeginRotation() })
		select {
		case spawned <- struct{}{}:
		default:
		}
	}

	var err error
	r, err = New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	waitSpawn(t, spawned, "the first spawn")
	waitSpawn(t, spawned, "the crash respawn")

	// Deliberately NOT guarded by Stdin() != nil: the 20 ms crash child may already
	// be gone, and gated is non-vacuous without it — under a mutant clearing
	// unconditionally, child 2's setStdin has run before this second receive, so the
	// read is deterministic in both directions.
	if _, gated := r.turnTarget(); !gated {
		t.Fatal("the crash/backoff respawn of the pre-rotation id disarmed the gate; a queued turn would then be accepted and written into a child RestartFresh is about to kill, and msgqueue would drop it as committed — #1330's loss reached through the backoff ladder")
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
