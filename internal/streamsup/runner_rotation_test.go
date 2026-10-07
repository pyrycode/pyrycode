package streamsup

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

	_, cancel, _, args, _, _, forceFirst, _, _ := r.beginSpawn(context.Background(), true)
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

	_, cancel, _, args, _, _, forceFirst, _, _ := r.beginSpawn(context.Background(), true)
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

// adoptedSessionID is the id an announced reset names in the AdoptSessionID
// tests. Distinct from rotatedSessionID so a spawn's argv says on its face which
// path put the id there, and a canonical lowercase UUID stem because
// transcript.StatByID refuses anything else before it joins a path — an invalid
// stem would answer "absent" for a reason the fixture did not intend.
const adoptedSessionID = "abcdabcd-1234-4321-8765-0123456789ab"

// TestRunner_AdoptSessionID_InstallsIDWithoutRotationMachinery pins the whole of
// what adoption must NOT do (#2136 AC2) alongside the two ids it must leave
// alone (AC4 and the empty-id guard).
//
// The Runner is constructed directly, as TestRunner_SpawnSetupFailureRetainsSessionID
// does, so no child process is involved and every field is observed at the
// source. iterCancel is a recording func and restartCh is allocated empty
// because those two ARE the kill: RestartFresh's doc states these methods drive
// only restartMu, a ctx cancel and restartCh, so a cancel that never fired and a
// hint that was never sent is the complete statement of "the child is not
// killed" — stronger than watching a pid, which cannot distinguish "not killed"
// from "killed and respawned fast".
//
// The turnTarget assertion is AC2's "no turn is refused" at the consequence
// rather than the field: it is the seam WriteUserTurn consults, and a gated
// answer there is the ErrNoLiveChild every turn would get.
func TestRunner_AdoptSessionID_InstallsIDWithoutRotationMachinery(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		adopt  string
		wantID string
		why    string
	}{
		{
			name: "a different id", adopt: adoptedSessionID, wantID: adoptedSessionID,
			why: "the announced id is what the next spawn must resume",
		},
		{
			name: "the id already held", adopt: testSessionID, wantID: testSessionID,
			why: "adopting the id the runner already holds changes nothing",
		},
		{
			name: "the empty id", adopt: "", wantID: testSessionID,
			why: "the runner never emits an empty id, so New's non-empty contract holds here too",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var cancelled atomic.Bool
			r := &Runner{
				cfg:        Config{SessionID: testSessionID},
				log:        discardLogger(),
				sessionID:  testSessionID,
				restartCh:  make(chan struct{}, 1),
				iterCancel: func() { cancelled.Store(true) },
			}

			r.AdoptSessionID(tc.adopt)

			r.restartMu.Lock()
			gotID, gotPending, gotSeq := r.sessionID, r.rotatePending, r.freshSeq
			r.restartMu.Unlock()

			if gotID != tc.wantID {
				t.Errorf("sessionID = %q after AdoptSessionID(%q), want %q — %s",
					gotID, tc.adopt, tc.wantID, tc.why)
			}
			if gotPending {
				t.Error("rotatePending is set — adoption armed first-run form, so the next spawn would " +
					"establish a fresh transcript instead of resuming the adopted one")
			}
			if gotSeq != 0 {
				t.Errorf("freshSeq = %d, want 0 — adoption advanced the rotation gate's release "+
					"authorisation, which counts rotations that landed and adoption is not one", gotSeq)
			}
			if cancelled.Load() {
				t.Error("the iteration ctx was cancelled — adoption tore the live child down")
			}
			if n := len(r.restartCh); n != 0 {
				t.Errorf("restartCh holds %d hint(s), want 0 — adoption asked Run to relaunch", n)
			}
			if _, gated := r.turnTarget(); gated {
				t.Error("turnTarget reports gated — every turn arriving after an adoption would be " +
					"refused with ErrNoLiveChild")
			}

			_, cancel, _, args, _, _, forceFirst, _, _ := r.beginSpawn(context.Background(), false)
			defer cancel()
			if forceFirst {
				t.Error("beginSpawn reported forceFirst with no rotation pending")
			}
			if got := idFlagValue(args, "--resume"); got != tc.wantID {
				t.Errorf("next spawn --resume = %q, want %q — %s:\n%v", got, tc.wantID, tc.why, args)
			}
		})
	}
}

// TestRunner_AdoptSessionID_RespawnResumesAdoptedID is #2136 AC1 end to end: after
// the runner adopts an announced id, its next crash-respawn resumes THAT
// transcript and never the pre-reset one.
//
// The fixture holds a transcript for the ADOPTED id only, and that asymmetry is
// what makes the assertion ride useCreateForm's StatByID probe. With
// ClaudeSessionsDir left empty the probe is skipped and the resume form comes
// from the Run loop's firstRun latch instead — the same argv by a different
// route, proving nothing about the id the probe was handed. Staged the other way
// round (a transcript for the pre-reset id) spawn 2 would resume for the wrong
// reason on a runner that never adopted.
//
//	spawn 1: --session-id <pre-reset>  (no transcript for it ⇒ the probe says create)
//	spawn 2: --resume     <adopted>    (crash respawn, transcript staged ⇒ resume)
//
// A runner that ignored the adoption emits --session-id <pre-reset> for spawn 2,
// so the fixture discriminates on both the flag and the id.
func TestRunner_AdoptSessionID_RespawnResumesAdoptedID(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "crash", out, stderr)
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond

	sessionsDir := t.TempDir()
	writeTranscript(t, sessionsDir, adoptedSessionID, time.Now())
	cfg.ClaudeSessionsDir = sessionsDir

	rec := &spawnArgsRecorder{}
	cfg.Logger = slog.New(rec)
	var (
		r    *Runner
		once sync.Once
	)
	// Adoption kills nothing, so spawn 1's child lives out its own crash and the
	// respawn below is the child's own exit being answered — not a teardown this
	// call ordered.
	cfg.onSpawn = func(int) {
		once.Do(func() { r.AdoptSessionID(adoptedSessionID) })
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

	if got := idFlagValue(spawns[0], "--session-id"); got != testSessionID {
		t.Errorf("spawn 1 --session-id = %q, want %q:\n%v", got, testSessionID, spawns[0])
	}

	if got := idFlagValue(spawns[1], "--resume"); got != adoptedSessionID {
		t.Errorf("spawn 2 --resume = %q, want the adopted %q — the crash respawn resurrected the "+
			"conversation the announced reset cleared:\n%v", got, adoptedSessionID, spawns[1])
	}
	if slices.Contains(spawns[1], testSessionID) {
		t.Errorf("spawn 2 references the pre-reset id %q after the adoption:\n%v", testSessionID, spawns[1])
	}

	for i, args := range spawns[:2] {
		if got := idFlagCount(args); got != 1 {
			t.Errorf("spawn %d carries %d id flags, want exactly 1:\n%v", i+1, got, args)
		}
	}
}
