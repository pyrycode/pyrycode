package sessions

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// installedArgv normalises a raw SetSpawnArgs / Restart record the way recordArgv
// normalises a construction argv: it drops the argvRecorderTemplate prefix when
// present (helperPoolArgvRecorder's pools carry it, helperRestartPool's do not)
// and then the #943 "--settings <path>" pair, leaving the settings flags an
// assertion owns. stripMCPSettings fatals when the pair is missing, so every
// caller doubles as a #943 regression check on the install path.
//
// The record has to be read this way rather than through waitArgv: SetSpawnArgs
// on the double deliberately does not feed recordArgv, so waitArgv would return
// the stale construction argv instead of the installed one.
func installedArgv(t *testing.T, argv []string) []string {
	t.Helper()
	if len(argv) >= len(argvRecorderTemplate) && argv[0] == argvRecorderTemplate[0] {
		argv = argv[len(argvRecorderTemplate):]
	}
	return stripMCPSettings(t, argv)
}

// waitRunning blocks until the double reports a live child, so an in-band
// delivery is asserted against a genuinely running session rather than one still
// coming up.
func waitRunning(t *testing.T, runner *lifecycleRunner) {
	t.Helper()
	if !pollUntil(t, 5*time.Second, func() bool { return runner.State().Phase == PhaseRunning }) {
		t.Fatal("child never reached PhaseRunning")
	}
}

// TestInBandDeliverable pins the partition itself (#1581, redrawn by #1604):
// which updates reach the live child in-band — /model + /effort command text, and
// a bypass revocation as a control request — and which keep #842's restart. Total
// over the shapes the wire can produce, so the restart tests stay green on purpose
// rather than by luck.
//
// The YOLO rows are the #1604 split and read as a pair: a revoke goes in-band
// because claude accepts set_permission_mode default on a held-open stream, an
// enable keeps the restart because claude refuses the escalation, gating it on the
// launch argv (#1595).
func TestInBandDeliverable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		update SettingsUpdate
		want   bool
	}{
		{"model only", SettingsUpdate{Model: ptr("opus")}, true},
		{"effort only", SettingsUpdate{Effort: ptr("high")}, true},
		{"model and effort", SettingsUpdate{Model: ptr("opus"), Effort: ptr("high")}, true},
		{"model cleared to default", SettingsUpdate{Model: ptr("")}, false},
		{"effort cleared to default", SettingsUpdate{Effort: ptr("")}, false},
		{"one present field empty", SettingsUpdate{Model: ptr("opus"), Effort: ptr("")}, false},
		{"model with yolo grant", SettingsUpdate{Model: ptr("opus"), YOLO: ptr(true)}, false},
		// A present YOLO revoke goes in-band even when it equals the stored value:
		// the rule keys on the frame's own value, never on merged-vs-previous.
		{"model with yolo revoke", SettingsUpdate{Model: ptr("opus"), YOLO: ptr(false)}, true},
		{"yolo revoke only", SettingsUpdate{YOLO: ptr(false)}, true},
		{"yolo revoke with effort", SettingsUpdate{Effort: ptr("high"), YOLO: ptr(false)}, true},
		// The empty-value reject wins over the revoke: the respawn recomposes argv
		// from the merged settings, so it carries the revocation anyway.
		{"yolo revoke with cleared model", SettingsUpdate{Model: ptr(""), YOLO: ptr(false)}, false},
		{"yolo enable only", SettingsUpdate{YOLO: ptr(true)}, false},
		{"nothing present", SettingsUpdate{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := inBandDeliverable(tc.update); got != tc.want {
				t.Errorf("inBandDeliverable(%+v) = %v, want %v", tc.update, got, tc.want)
			}
		})
	}
}

// TestPool_DeliverSettingsInBand_EnableWritesNothing (#1604, AC #3's second
// half): deliverSettingsInBand refuses to write a permission-mode change for a
// YOLO *enable*, and refuses it AT THE DELIVERY SITE rather than relying on
// inBandDeliverable having rejected the frame first.
//
// It calls the unexported delivery directly with a shape the predicate never lets
// through, which is the only way to reach the enable-direction fail-safe — and so
// the only red available for a mutant that drops the `!*update.YOLO` conjunct.
// That guard is what makes the delivery independently correct instead of dependent
// on a caller-side invariant; without this test it would be untested defence.
//
// No runPoolInBackground: the delivery reads only p.log and the runner it is
// handed, so a live child would add nothing to observe.
func TestPool_DeliverSettingsInBand_EnableWritesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")

	pool := helperRestartPool(t, regPath, t.TempDir(), SessionSettings{})
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)

	pool.deliverSettingsInBand(id, runner, SettingsUpdate{YOLO: ptr(true)})

	if got := runner.revokeCount(); got != 0 {
		t.Errorf("a YOLO enable wrote %d permission-mode change(s) in-band, want 0", got)
	}
	if got := runner.userTurns(); len(got) != 0 {
		t.Errorf("a YOLO-only update invented a command: %q", got)
	}
}

// TestPool_UpdateSettings_InBand_ModelAndEffort (AC #1): a change whose present
// fields are a non-empty model and effort is delivered to the live child as
// command text on the stream the daemon already holds open, and that child is
// neither terminated nor respawned. Both halves are read off the double's own
// records — the delivered payloads and the Restart calls — never off the `done`
// sentinel, which cannot observe a respawn here (see doneAppears).
//
// It also covers AC #4's live-child half: the recomposed argv is installed for
// the next spawn, so the change survives a later crash-respawn or evict →
// Activate even though nothing was killed now.
func TestPool_UpdateSettings_InBand_ModelAndEffort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)
	waitRunning(t, runner)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus"), Effort: ptr("high")}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	// The command text is the measured form, and model precedes effort.
	if got, want := runner.userTurns(), []string{"/model opus", "/effort high"}; !reflect.DeepEqual(got, want) {
		t.Errorf("delivered turns = %q, want %q", got, want)
	}
	if restarts := runner.restartArgs(); len(restarts) != 0 {
		t.Errorf("in-band change respawned the child: Restart%v", restarts)
	}
	installs := runner.spawnArgSets()
	if len(installs) != 1 {
		t.Fatalf("SetSpawnArgs called %d times, want exactly 1: %v", len(installs), installs)
	}
	if got, want := installedArgv(t, installs[0]), []string{"--model", "opus", "--effort", "high"}; !reflect.DeepEqual(got, want) {
		t.Errorf("installed argv = %v, want %v", got, want)
	}
	if disk := diskSettings(t, regPath); disk != (SessionSettings{Model: "opus", Effort: "high"}) {
		t.Errorf("on-disk settings = %+v, want opus/high/false", disk)
	}
}

// TestPool_UpdateSettings_InBand_EvictedNoLiveChild (AC #4, the evicted half): a
// model-only change to a session with no live child reports no error to the
// client and still installs the new argv for the next spawn — today's
// evicted-session contract, unchanged by the mechanism swap. _Evicted_SwapOnly
// cannot cover this: its update carries YOLO, so it exercises the restart path.
//
// The write is attempted unconditionally rather than pre-checked. The pool cannot
// classify the failure anyway (internal/sessions must not import
// internal/streamsup), so "no live child" is the production runner's answer, not
// a branch here; the double accepts the write and records it.
//
// No post-Activate argv assertion: Activate reuses the existing runner, so
// recordArgv is never re-fed and waitArgv would return the stale construction
// argv. That an installed argv reaches the next spawn is proven cross-package by
// the two-spawn SetSpawnArgs test in internal/streamsup.
func TestPool_UpdateSettings_InBand_EvictedNoLiveChild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	runPoolInBackground(t, pool)

	id := mintEvicted(t, pool, spawnDir, SessionSettings{Model: "sonnet"})

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus")}); err != nil {
		t.Fatalf("UpdateSettings on an evicted session = %v, want nil (the client sees success)", err)
	}

	runner := runnerDouble(t, pool, id)
	if restarts := runner.restartArgs(); len(restarts) != 0 {
		t.Errorf("model-only update on an evicted session called Restart%v", restarts)
	}
	if got, want := runner.userTurns(), []string{"/model opus"}; !reflect.DeepEqual(got, want) {
		t.Errorf("delivered turns = %q, want %q", got, want)
	}
	installs := runner.spawnArgSets()
	if len(installs) != 1 {
		t.Fatalf("SetSpawnArgs called %d times, want exactly 1: %v", len(installs), installs)
	}
	want := []string{"--session-id", string(id), "--model", "opus"}
	if got := installedArgv(t, installs[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("installed argv = %v, want %v", got, want)
	}
}

// TestPool_UpdateSettings_ClearModel_KeepsRestart (AC #2's uncovered corner):
// clearing the model to "" means "run at claude's own default", which
// claudeSettingsArgs expresses by omitting the flag and which has no measured
// /model form — so it keeps the restart. Every test AC #2 names either carries a
// present YOLO or returns before the live-apply, so none of them exercises this.
func TestPool_UpdateSettings_ClearModel_KeepsRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)
	waitRunning(t, runner)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("")}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	restarts := runner.restartArgs()
	if len(restarts) != 1 {
		t.Fatalf("Restart called %d times, want exactly 1: %v", len(restarts), restarts)
	}
	if got := installedArgv(t, restarts[0]); len(got) != 0 {
		t.Errorf("restart argv = %v, want no settings flags at all", got)
	}
	if got := runner.userTurns(); len(got) != 0 {
		t.Errorf("clearing the model invented a command: %q", got)
	}
}
