package sessions

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// restartRecorderScript records all its args (one token per line) to argv.txt in
// its cwd, touches a `done` sentinel, then execs a long sleep so the child stays
// alive. Unlike `sh -c SCRIPT`, it is a standalone executable, so a leading
// --continue (prepended by ResumeLast on a restart) is just another positional
// arg it records rather than a shell option it rejects — which lets the bootstrap
// live-restart tests observe the resume flag (AC #2).
const restartRecorderScript = "#!/bin/sh\nprintf '%s\\n' \"$@\" > argv.txt\n: > done\nexec sleep 3600\n"

// helperRestartPool warm-starts a running Pool whose bootstrap child is the
// restart recorder (see restartRecorderScript). The bootstrap begins with the
// given settings and ResumeLast=true, so a settings-driven restart relaunches
// with --continue. tplWorkDir is the child's cwd, where it writes argv.txt/done.
func helperRestartPool(t *testing.T, regPath, tplWorkDir string, settings SessionSettings) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	script := filepath.Join(t.TempDir(), "recorder.sh")
	if err := os.WriteFile(script, []byte(restartRecorderScript), 0o755); err != nil {
		t.Fatalf("write recorder script: %v", err)
	}

	when := time.Now().UTC()
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:           SessionID("550e8400-e29b-41d4-a716-446655440000"),
			CreatedAt:    when,
			LastActiveAt: when,
			Bootstrap:    true,
			Model:        settings.Model,
			Effort:       settings.Effort,
			YOLO:         settings.YOLO,
			// Through permissionModeForDisk, so the pre-written entry has the shape
			// saveLocked would have produced: a mode-free literal writes no
			// permission_mode key and therefore reproduces the pre-#2043 on-disk
			// shape exactly.
			PermissionMode: permissionModeForDisk(settings),
		}},
	}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := New(Config{
		RunnerFactory: recordingRunnerFactory,
		Logger:        logger,
		RegistryPath:  regPath,
		Bootstrap: SessionConfig{
			ClaudeBin:      script,
			WorkDir:        tplWorkDir,
			ResumeLast:     true,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   time.Second,
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// clearRecording removes the recorder's argv.txt and done sentinel so a
// subsequent waitArgv observes the NEXT spawn (the restart) rather than the
// spawn that already ran. Safe: the recorded child has finished writing and is
// blocked in `exec sleep` by the time a test calls this.
func clearRecording(t *testing.T, dir string) {
	t.Helper()
	for _, f := range []string{"argv.txt", "done"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", f, err)
		}
	}
}

// doneAppears reports whether the recorder's done sentinel shows up in dir
// within timeout — i.e. whether a (re)spawn occurred.
//
// It cannot actually observe one under the runner double, and a NEW test must not
// reach for it. helperRestartPool passes restartRecorderScript as ClaudeBin, but
// recordingRunnerFactory ignores cfg.ClaudeBin and returns a lifecycleRunner whose
// Run execs /bin/sleep — nothing ever writes the sentinel, so this always returns
// false. Measured 2026-08-19: deleting UpdateSettings' no-op early return, so that
// a no-op update does call Restart, leaves TestPool_UpdateSettings_NoOp_NoRestart
// green while reddening TestPool_UpdateSettings_NoOpWritesNothing — the mutant is
// live and the assertion is simply blind. Read "no respawn" off the double's own
// Restart record instead, via runnerDouble (#1581).
func doneAppears(t *testing.T, dir string, timeout time.Duration) bool {
	t.Helper()
	return pollUntil(t, timeout, func() bool {
		_, err := os.Stat(filepath.Join(dir, "done"))
		return err == nil
	})
}

// mintEvicted builds + registers + supervises a minted session WITHOUT
// activating it, so it sits in stateEvicted with no live child — the state the
// evicted-session carve-out (§ Design) exercises.
func mintEvicted(t *testing.T, pool *Pool, spawnDir string, settings SessionSettings) SessionID {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	sess, err := pool.buildSession(id, "", spawnDir, settings)
	if err != nil {
		t.Fatalf("buildSession: %v", err)
	}
	pool.mu.Lock()
	pool.sessions[id] = sess
	if err := pool.saveLocked(); err != nil {
		pool.mu.Unlock()
		t.Fatalf("saveLocked: %v", err)
	}
	pool.mu.Unlock()
	pool.RegisterAllocatedUUID(id)
	if err := pool.supervise(sess); err != nil {
		t.Fatalf("supervise: %v", err)
	}
	return id
}

// TestPool_UpdateSettings_LiveRestart_Bootstrap (AC #1/#2/#3): a single
// UpdateSettings on the running bootstrap both persists the change and restarts
// its claude with the new argv, resuming via its deterministic --session-id
// (#839 retired --continue for the bootstrap).
//
// #1604 extends it rather than adding a second test, because the YOLO false→true
// this already flips IS the enable direction: claude gates the escalation on the
// launch argv and refuses the control request (#1595), so only the respawn under
// the recomposed argv can grant it. The restart-argv read below is one half of
// that pin; an empty permissionModes() is the other — no production path writes an
// ENABLE over the control channel, under any update, in either of the two
// spellings #2043 gave it.
func TestPool_UpdateSettings_LiveRestart_Bootstrap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)

	// First spawn: baseline settings. The deterministic id (#839) is a field on
	// the handover since #1348, checked separately.
	if got, want := waitArgv(t, tplWorkDir), append([]string{"--model", "sonnet"}, alwaysOnPosture(permissionModeDefault)...); !reflect.DeepEqual(got, want) {
		t.Fatalf("first spawn argv = %v, want %v", got, want)
	}
	if gotID := waitSessionID(t, tplWorkDir); gotID != string(id) {
		t.Fatalf("first spawn session id = %q, want %q", gotID, string(id))
	}
	clearRecording(t, tplWorkDir)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus"), Effort: ptr("high"), YOLO: ptr(true)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	// AC #1 + #2: relaunch resumes via --session-id (#839, not --continue) and
	// carries the new settings; the id is stable across the restart.
	got := waitArgv(t, tplWorkDir)
	want := []string{"--model", "opus", "--effort", "high", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restart argv = %v, want %v", got, want)
	}
	// #1604: the enable direction never travels over the control channel.
	if got := runner.permissionModes(); len(got) != 0 {
		t.Errorf("a YOLO enable wrote %v in-band, want no posture send at all", got)
	}
	// AC #3: the same call persisted the change.
	if disk := diskSettings(t, regPath); disk != (SessionSettings{Model: "opus", Effort: "high", YOLO: true}) {
		t.Errorf("on-disk settings = %+v, want opus/high/true", disk)
	}
}

// TestPool_UpdateSettings_LiveRestart_Minted (AC #1/#2): a running minted session
// relaunches with the new argv, resuming via its baked --session-id (ResumeLast
// is false for minted sessions, so no --continue).
func TestPool_UpdateSettings_LiveRestart_Minted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	id := spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{Model: "sonnet"})

	if got, want := waitArgv(t, spawnDir), append([]string{"--session-id", string(id), "--model", "sonnet"}, alwaysOnPosture(permissionModeDefault)...); !reflect.DeepEqual(got, want) {
		t.Fatalf("first minted spawn argv = %v, want %v", got, want)
	}
	clearRecording(t, spawnDir)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus"), YOLO: ptr(true)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	got := waitArgv(t, spawnDir)
	want := []string{"--session-id", string(id), "--model", "opus", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restart minted argv = %v, want %v", got, want)
	}
}

// TestPool_UpdateSettings_NoOp_NoRestart: a no-op update (present value equals
// stored) does not disturb the running child — no relaunch.
func TestPool_UpdateSettings_NoOp_NoRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()

	waitArgv(t, tplWorkDir)
	clearRecording(t, tplWorkDir)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("sonnet")}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if doneAppears(t, tplWorkDir, 500*time.Millisecond) {
		t.Error("no-op UpdateSettings triggered a restart (done sentinel reappeared)")
	}
}

// TestPool_UpdateSettings_PersistFailure_NoRestart: when the persist fails the
// settings roll back and the running child is NOT restarted (no half-apply).
func TestPool_UpdateSettings_PersistFailure_NoRestart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("posix permission semantics required (not root)")
	}
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()

	waitArgv(t, tplWorkDir)
	clearRecording(t, tplWorkDir)

	// Read-only registry dir → saveLocked's temp-file create fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus")}); err == nil {
		t.Fatal("UpdateSettings on a read-only registry dir returned nil, want persist error")
	}

	if got, _ := pool.DefaultSettings(); got.Model != "sonnet" {
		t.Errorf("settings not rolled back on persist failure: Model=%q, want sonnet", got.Model)
	}
	if doneAppears(t, tplWorkDir, 500*time.Millisecond) {
		t.Error("persist-failing UpdateSettings triggered a restart (done reappeared)")
	}
}

// TestPool_UpdateSettings_Evicted_SwapOnly: on an evicted session UpdateSettings
// persists + swaps the spawn argv but spawns no child; the next Activate then
// launches with the swapped argv (closes the reused-supervisor gap noted in the
// spec).
func TestPool_UpdateSettings_Evicted_SwapOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	id := mintEvicted(t, pool, spawnDir, SessionSettings{Model: "sonnet"})

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus"), YOLO: ptr(true)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	// No live child to kill/relaunch while evicted.
	if doneAppears(t, spawnDir, 500*time.Millisecond) {
		t.Fatal("UpdateSettings on an evicted session spawned a child (should swap only)")
	}

	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	got := waitArgv(t, spawnDir)
	want := []string{"--session-id", string(id), "--model", "opus", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("post-activate argv = %v, want %v (swap must apply on next Activate)", got, want)
	}
}

// TestPool_UpdateSettings_YOLORevoke_DropsBypassInBand (security): flipping YOLO
// true→false drops the running child's bypass posture WITHOUT killing it, and
// installs a bypass-free argv for the next spawn.
//
// Renamed from _DropsBypassOnRestart, and retargeted with it (#1604): #1595
// measured that claude accepts a set_permission_mode control request carrying
// mode default on a held-open stream, so the revocation no longer needs a
// respawn to take effect now. The live assertion is NOT weakened by the move —
// it is the same "applies to the running child" property, read off the mechanism
// that now carries it (RevokeBypass) instead of off a relaunch argv. The
// next-spawn half is asserted alongside it, because it is what makes the
// revocation survive a crash-relaunch, a rotation or an eviction.
//
// The template is the sibling _YOLOAbsent_NoBypassInBand: every fact is read off
// the double's own records. waitArgv cannot be used past the first spawn here —
// lifecycleRunner.Restart is what feeds recordArgv, so with no restart it would
// return the stale construction argv or time out — and doneAppears is blind under
// this double either way (see its own doc).
func TestPool_UpdateSettings_YOLORevoke_DropsBypassInBand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{YOLO: true})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)
	waitRunning(t, runner)

	if got := waitArgv(t, tplWorkDir); !reflect.DeepEqual(got, []string{"--dangerously-skip-permissions"}) {
		t.Fatalf("first spawn argv = %v, want the bypass flag present", got)
	}
	if gotID := waitSessionID(t, tplWorkDir); gotID != string(id) {
		t.Fatalf("first spawn session id = %q, want %q", gotID, string(id))
	}

	if err := pool.UpdateSettings(id, SettingsUpdate{YOLO: ptr(false)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	// The live half, and AC #3's arithmetic: EXACTLY ONE posture send reached the
	// running child, carrying the mode the revocation derives. Two would be the
	// old revoke clause left standing beside the new mode clause — both fire on
	// this update — and zero would be that clause dropped with nothing put in its
	// place. Neither mutant survives this assertion.
	//
	// The send goes through the seam's mode-carrying method: #2042 introduced it
	// beside RevokeBypass so the delivered bytes stayed provably unchanged, and
	// #2043 collapsed the pair here, taking RevokeBypass off the Runner seam. The
	// wire bytes are still the ones #1595 measured — RevokeBypass was
	// SetPermissionMode("default") — so what changed is which method emits them.
	if got, want := runner.permissionModes(), []string{"default"}; !reflect.DeepEqual(got, want) {
		t.Errorf("in-band posture sends = %v, want exactly %v", got, want)
	}
	if restarts := runner.restartArgs(); len(restarts) != 0 {
		t.Errorf("a revoke respawned the child: Restart%v", restarts)
	}
	// The durable half: the next spawn carries no bypass flag either.
	installs := runner.spawnArgSets()
	if len(installs) != 1 {
		t.Fatalf("SetSpawnArgs called %d times, want exactly 1: %v", len(installs), installs)
	}
	// The durable half, in its post-#2065 spelling. A revoke can no longer be read as
	// "the flag is gone" — the flag is on every argv — so what is asserted is that the
	// next spawn is DOWNGRADED: it names an in-band mode beside the flag, where an
	// escalated session names none. A tree that lost the revocation composes the bare
	// flag here, which assertDowngraded reddens on.
	got := installedArgv(t, installs[0])
	assertDowngraded(t, "post-revoke installed argv", got)
	// Revoked YOLO with no model or effort leaves nothing beyond that suffix.
	if want := alwaysOnPosture(permissionModeDefault); !reflect.DeepEqual(got, want) {
		t.Errorf("post-revoke installed argv = %v, want %v", got, want)
	}
	if turns := runner.userTurns(); len(turns) != 0 {
		t.Errorf("a YOLO-only update invented a command: %q", turns)
	}
}

// TestPool_UpdateSettings_YOLOAbsent_NoBypassInBand (security): an update that
// changes only Model (YOLO omitted, stored false) can never recompose into a
// bypass child — the fail-safe survives the live-apply.
//
// Renamed from _NoBypassOnRestart, and rewritten with it: a model-only update is
// exactly the shape #1581 delivers in-band, so this asserts no respawn happened
// and reads the fail-safe off the argv the NEXT spawn will use rather than off the
// argv a respawn did use. The assertion is not weakened by that — it is the same
// argv, produced by the same Session.spawnArgs → claudeSettingsArgs path, checked
// one step earlier. What is dropped is the old #839 pinned-id check: with no
// respawn there is no second spawn for the id to survive into, so reading it back
// would only re-report the construction record. That id is asserted across a real
// restart by _LiveRestart_Bootstrap and _LiveRestart_Minted.
//
// #1604 must NOT drag this back to a respawn assertion: the model-only path it
// covers is exactly the one #1581 moved off the restart, and the argv it reads is
// the next-spawn install. The one thing #1604 adds is an empty permissionModes() —
// an update naming NEITHER posture field writes no posture change at all, which is
// the direct red for a delivery guard relaxed to send unconditionally. That guard
// is what keeps #2043's "one posture send per posture-carrying update" from
// becoming "one per update".
func TestPool_UpdateSettings_YOLOAbsent_NoBypassInBand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet", YOLO: false})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)
	waitRunning(t, runner)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus")}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	if restarts := runner.restartArgs(); len(restarts) != 0 {
		t.Errorf("model-only update respawned the child: Restart%v", restarts)
	}
	if got := runner.permissionModes(); len(got) != 0 {
		t.Errorf("an update naming no posture field wrote %v in-band, want none", got)
	}
	if got, want := runner.userTurns(), []string{"/model opus"}; !reflect.DeepEqual(got, want) {
		t.Errorf("delivered turns = %q, want %q", got, want)
	}
	installs := runner.spawnArgSets()
	if len(installs) != 1 {
		t.Fatalf("SetSpawnArgs called %d times, want exactly 1: %v", len(installs), installs)
	}
	// An absent YOLO must not ESCALATE the next spawn. Post-#2065 that is the mode
	// pair being present rather than the flag being absent — see assertDowngraded.
	got := installedArgv(t, installs[0])
	assertDowngraded(t, "absent-YOLO installed argv", got)
	if want := append([]string{"--model", "opus"}, alwaysOnPosture(permissionModeDefault)...); !reflect.DeepEqual(got, want) {
		t.Errorf("installed argv = %v, want %v", got, want)
	}
}
