package streamsup

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

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
				"--include-partial-messages",
				"--forward-subagent-text",
				"--replay-user-messages",
				"--prompt-suggestions",
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
				"--include-partial-messages",
				"--forward-subagent-text",
				"--replay-user-messages",
				"--prompt-suggestions",
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
				"--include-partial-messages",
				"--forward-subagent-text",
				"--replay-user-messages",
				"--prompt-suggestions",
				"--session-id", id,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildArgs(tt.base, tt.firstRun, id, true)
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
	_ = buildArgs(base, true, "id", true)
	_ = buildArgs(base, false, "id", false)
	if !slices.Equal(base, orig) {
		t.Errorf("buildArgs mutated base: got %v, want %v", base, orig)
	}
}

// TestBuildArgs_PromptSuggestionsOptOut pins #2831: the flag rides every
// persistent spawn unless the child environment sets claude's own switch to
// "false", and the spawn's last entry for it is the one that counts.
func TestBuildArgs_PromptSuggestionsOptOut(t *testing.T) {
	t.Parallel()
	const key = "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION="
	tests := []struct {
		name string
		env  []string
		want bool // flag present
	}{
		{"unset", []string{"HOME=/h"}, true},
		{"true", []string{key + "true"}, true},
		{"false", []string{key + "false"}, false},
		{"empty", []string{key}, true},
		{"later entry re-enables", []string{key + "false", key + "1"}, true},
		{"later entry disables", []string{key + "true", key + "false"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := buildArgs(nil, true, "id", !promptSuggestionsDisabled(tt.env))
			if got := slices.Contains(args, "--prompt-suggestions"); got != tt.want {
				t.Errorf("--prompt-suggestions present = %v, want %v (args %v)", got, tt.want, args)
			}
		})
	}
}

// TestBuildArgs_StableIDAcrossFirstAndResume is the pure half of AC4: the same
// id token appears after --session-id on the first spawn and after --resume on
// a respawn — byte-identical, so the on-disk id is reused (no fork).
func TestBuildArgs_StableIDAcrossFirstAndResume(t *testing.T) {
	t.Parallel()
	const id = "deadbeef-uuid"
	if got := idFlagValue(buildArgs(nil, true, id, true), "--session-id"); got != id {
		t.Errorf("first spawn --session-id = %q, want %q", got, id)
	}
	if got := idFlagValue(buildArgs(nil, false, id, true), "--resume"); got != id {
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

			got := buildArgs(base, useCreateForm(dir, tt.id, tt.latchCreate), tt.id, false)
			want := []string{
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--verbose",
				"--include-partial-messages",
				"--forward-subagent-text",
				"--replay-user-messages",
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

	// Both halves of each mutable/immutable pair are seeded, as this file's
	// hand-built literals already do for SessionID: New seeds the live fields from
	// the config, and a literal that skips New has to do it itself. Since #1475
	// beginSpawn's probe reads the live claudeSessionsDir, not cfg's, so a literal
	// setting only the config half would silently run no probe.
	r := &Runner{
		cfg: Config{
			SessionID:         testSessionID,
			ClaudeSessionsDir: sessionsDir,
		},
		sessionID:         testSessionID,
		claudeSessionsDir: sessionsDir,
	}

	_, cancel, _, args, _, _, forceFirst, _, _ := r.beginSpawn(context.Background(), true)
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
			_, cancel, _, _, env, _, _, _, _ := newRunner(tc.own).beginSpawn(context.Background(), true)
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

	_, cancel, _, args, env, _, forceFirst, _, _ := r.beginSpawn(context.Background(), false)
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
