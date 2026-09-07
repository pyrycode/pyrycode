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
	return stripSystemPrompt(t, stripMCPSettings(t, argv))
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

// TestInBandDeliverable pins the partition itself (#1581, redrawn by #1604 and
// again by #2066): which updates reach the live child in-band — /model + /effort
// command text, and any STORABLE posture as a control request — and which keep
// #842's restart. Total over the shapes the wire can produce, so the restart tests
// stay green on purpose rather than by luck.
//
// The posture rows no longer split on direction, and that is #2066's whole change.
// A revoke and an ESCALATION both go in-band: claude gated the escalation on the
// launch argv, #2065 put that flag on every argv, and #2060 measured claude
// accepting the re-escalation on such a child at 2.1.239. What survives is the
// split on VOCABULARY — a mode this daemon cannot store is refused by
// non-membership in permissionModeKnown, which is why the garbage rows below read
// false without the predicate naming any of them, and why the escalation's two
// spellings had to be opened together (internal/relay's validPermissionMode
// refuses the mode string, so the wire can only ever send the bit).
//
// The empty-Model and empty-Effort rejects still outrank a posture change in the
// same frame, escalation included, and lose nothing: the restart recomposes argv
// from the merged settings, so the respawn carries the posture anyway.
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
		{"model with yolo grant", SettingsUpdate{Model: ptr("opus"), YOLO: ptr(true)}, true},
		// A present YOLO revoke goes in-band even when it equals the stored value:
		// the rule keys on the frame's own value, never on merged-vs-previous.
		{"model with yolo revoke", SettingsUpdate{Model: ptr("opus"), YOLO: ptr(false)}, true},
		{"yolo revoke only", SettingsUpdate{YOLO: ptr(false)}, true},
		{"yolo revoke with effort", SettingsUpdate{Effort: ptr("high"), YOLO: ptr(false)}, true},
		// The empty-value reject wins over the revoke: the respawn recomposes argv
		// from the merged settings, so it carries the revocation anyway.
		{"yolo revoke with cleared model", SettingsUpdate{Model: ptr(""), YOLO: ptr(false)}, false},
		{"yolo enable only", SettingsUpdate{YOLO: ptr(true)}, true},
		// The empty-value reject outranks an ENABLE too, in both spellings. These
		// two rows are the only surviving route from an escalation to the restart
		// branch, and they are what keeps Pool.UpdateSettings' Restart call alive.
		{"yolo enable with cleared model", SettingsUpdate{Model: ptr(""), YOLO: ptr(true)}, false},
		{"yolo enable with cleared effort", SettingsUpdate{Effort: ptr(""), YOLO: ptr(true)}, false},
		{"nothing present", SettingsUpdate{}, false},
		// #2043's posture rows, widened by #2066. Every mode this daemon can STORE
		// goes in-band — the five plus the escalation; every unanticipated spelling
		// is refused by NON-MEMBERSHIP in the same clause, which is why the garbage
		// rows read false without the predicate naming any of them.
		{"mode default", SettingsUpdate{PermissionMode: ptr(permissionModeDefault)}, true},
		{"mode acceptEdits", SettingsUpdate{PermissionMode: ptr("acceptEdits")}, true},
		{"mode plan", SettingsUpdate{PermissionMode: ptr("plan")}, true},
		{"mode auto", SettingsUpdate{PermissionMode: ptr("auto")}, true},
		{"mode dontAsk", SettingsUpdate{PermissionMode: ptr("dontAsk")}, true},
		{"mode with model and effort", SettingsUpdate{Model: ptr("opus"), Effort: ptr("high"), PermissionMode: ptr("plan")}, true},
		{"escalation as a mode", SettingsUpdate{PermissionMode: ptr(permissionModeBypass)}, true},
		{"escalation as a mode beside a model", SettingsUpdate{Model: ptr("opus"), PermissionMode: ptr(permissionModeBypass)}, true},
		{"escalation as a mode with cleared model", SettingsUpdate{Model: ptr(""), PermissionMode: ptr(permissionModeBypass)}, false},
		{"unknown mode", SettingsUpdate{PermissionMode: ptr("Plan")}, false},
		{"empty mode", SettingsUpdate{PermissionMode: ptr("")}, false},
		// The empty-value reject still wins over a posture change, and loses
		// nothing: the respawn recomposes argv from the merged settings.
		{"mode with cleared model", SettingsUpdate{Model: ptr(""), PermissionMode: ptr("plan")}, false},
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

// TestPool_DeliverSettingsInBand_EnableWritesTheEscalation is the inverse of
// #1604's _EnableWritesNothing, which this replaces. That test pinned the delivery
// site's own bypass guard — "the second of three independent stops for the
// escalation" — and #2066 removes the stop, so the test that asserted it has to
// become the test that asserts its absence rather than be deleted: a delivery that
// silently wrote nothing for an escalation would ship this ticket as a green,
// silent no-op, and this is the red for exactly that.
//
// It calls the unexported delivery directly, so it reads the delivery site alone
// and does not depend on inBandDeliverable having routed the frame here. Both
// spellings are passed because the merged POSTURE is what the delivery reads, not
// the field the frame named: a mode-spelled enable and a YOLO-spelled one must
// come out as the same single send.
//
// EXACTLY ONE send, not at least one. The posture is two fields expressing one
// change, and the arithmetic is #2043's AC3: a frame naming both must not emit two
// identical control requests.
//
// No runPoolInBackground: the delivery reads only p.log and the runner it is
// handed, so a live child would add nothing to observe.
func TestPool_DeliverSettingsInBand_EnableWritesTheEscalation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		update SettingsUpdate
	}{
		{"yolo-spelled enable", SettingsUpdate{YOLO: ptr(true)}},
		{"mode-spelled enable", SettingsUpdate{PermissionMode: ptr(permissionModeBypass)}},
		{"both spellings in one frame", SettingsUpdate{YOLO: ptr(true), PermissionMode: ptr(permissionModeBypass)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			regPath := filepath.Join(dir, "sessions.json")

			pool := helperRestartPool(t, regPath, t.TempDir(), SessionSettings{})
			id := pool.Default().ID()
			runner := runnerDouble(t, pool, id)

			merged := SessionSettings{YOLO: true, PermissionMode: permissionModeBypass}
			pool.deliverSettingsInBand(id, runner, tc.update, merged)

			if got, want := runner.permissionModes(), []string{permissionModeBypass}; !reflect.DeepEqual(got, want) {
				t.Errorf("an escalation delivered %v in-band, want exactly %v", got, want)
			}
			if got := runner.userTurns(); len(got) != 0 {
				t.Errorf("a posture-only update invented a command: %q", got)
			}
		})
	}
}

// TestPool_UpdateSettings_InBand_Escalation_NoRespawn is #2066 AC 1: enabling
// bypass on a running session no longer respawns it, proven by a spawn count that
// does not move, for BOTH spellings the daemon accepts.
//
// The Restart record is the instrument, not the `done` sentinel, for the reason
// runnerDouble's own doc gives: doneAppears cannot observe a respawn under this
// double, whereas restartArgs records every Restart call. Zero restarts and one
// delivered posture together are the proof — either alone is compatible with a
// silent no-op, which is the failure mode this ticket is most exposed to.
//
// The mode spelling is exercised HERE and not through the relay, and that is not a
// gap in coverage: internal/relay's validPermissionMode refuses bypassPermissions
// as a mode string on purpose, so Pool.UpdateSettings is the only surface on which
// both spellings exist and the only place the pair can be measured together.
//
// The installed argv is asserted too, because the in-band branch installs it and
// the restart branch is where an escalation used to compose one: an escalated
// session's argv is the flag ALONE, with no --permission-mode pair beside it, so a
// tree that regressed to composing the mode into the argv reddens here rather than
// at the next spawn.
func TestPool_UpdateSettings_InBand_Escalation_NoRespawn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		update SettingsUpdate
	}{
		{"yolo-spelled enable", SettingsUpdate{YOLO: ptr(true)}},
		{"mode-spelled enable", SettingsUpdate{PermissionMode: ptr(permissionModeBypass)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regPath := filepath.Join(t.TempDir(), "sessions.json")

			pool := helperRestartPool(t, regPath, t.TempDir(), SessionSettings{})
			runPoolInBackground(t, pool)
			id := pool.Default().ID()
			runner := runnerDouble(t, pool, id)
			waitRunning(t, runner)

			if err := pool.UpdateSettings(id, tc.update); err != nil {
				t.Fatalf("UpdateSettings(%+v): %v", tc.update, err)
			}

			if restarts := runner.restartArgs(); len(restarts) != 0 {
				t.Errorf("the escalation respawned the child: Restart%v", restarts)
			}
			if got, want := runner.permissionModes(), []string{permissionModeBypass}; !reflect.DeepEqual(got, want) {
				t.Errorf("delivered posture = %v, want exactly %v; a no-respawn that writes "+
					"nothing is this ticket shipped as a silent no-op", got, want)
			}
			if got := runner.userTurns(); len(got) != 0 {
				t.Errorf("a posture-only update invented a command: %q", got)
			}
			installs := runner.spawnArgSets()
			if len(installs) != 1 {
				t.Fatalf("SetSpawnArgs called %d times, want exactly 1: %v", len(installs), installs)
			}
			if got, want := installedArgv(t, installs[0]), escalatedPosture(); !reflect.DeepEqual(got, want) {
				t.Errorf("installed argv = %v, want %v — the escalation keeps exactly one spelling", got, want)
			}
			if got, want := runner.spawnPermissionModes(), []string{permissionModeBypass}; !reflect.DeepEqual(got, want) {
				t.Errorf("SetSpawnPermissionMode records = %v, want %v; a crash-respawn would "+
					"otherwise re-assert the pre-escalation posture", got, want)
			}
			// On disk the escalation has ONE spelling, the yolo key —
			// permissionModeForDisk maps the mode to "" — so the mode-spelled and
			// yolo-spelled enables must persist byte-identically. In memory the pair
			// invariant is what holds, and SettingsFor is where it is readable.
			if disk := diskSettings(t, regPath); disk != (SessionSettings{YOLO: true}) {
				t.Errorf("on-disk settings = %+v, want yolo:true and no permission_mode key", disk)
			}
			got, err := pool.SettingsFor(id)
			if err != nil {
				t.Fatalf("SettingsFor: %v", err)
			}
			if got != (SessionSettings{YOLO: true, PermissionMode: permissionModeBypass}) {
				t.Errorf("SettingsFor = %+v, want the escalated pair in step", got)
			}
		})
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
	if got, want := installedArgv(t, installs[0]), append([]string{"--model", "opus", "--effort", "high"}, alwaysOnPosture(permissionModeDefault)...); !reflect.DeepEqual(got, want) {
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
	want := append([]string{"--session-id", string(id), "--model", "opus"}, alwaysOnPosture(permissionModeDefault)...)
	if got := installedArgv(t, installs[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("installed argv = %v, want %v", got, want)
	}
}

// TestPool_UpdateSettings_ClearModel_KeepsRestart (AC #2's uncovered corner):
// clearing the model to "" means "run at claude's own default", which
// claudeSettingsArgs expresses by omitting the --model flag and which has no
// measured /model form — so it keeps the restart. (The posture suffix is
// unrelated and is on every argv since #2065; "no settings flags" below means no
// model and no effort.) Every test AC #2 names either carries a
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
	if got, want := installedArgv(t, restarts[0]), alwaysOnPosture(permissionModeDefault); !reflect.DeepEqual(got, want) {
		t.Errorf("restart argv = %v, want no model or effort flag, only the posture suffix %v", got, want)
	}
	if got := runner.userTurns(); len(got) != 0 {
		t.Errorf("clearing the model invented a command: %q", got)
	}
}
