package sessions

import (
	"bytes"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// diskPermissionMode reads the bootstrap entry's RAW permission_mode value off
// disk — the field as serialised, not the posture settingsFromEntry derives from
// it. The distinction is the point at every call site below: "" is what both the
// default posture and the escalation write, and reading the derived value would
// hide which of the two the entry actually holds.
func diskPermissionMode(t *testing.T, regPath string) string {
	t.Helper()
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	e := pickBootstrap(reg)
	if e == nil {
		t.Fatalf("registry has no bootstrap entry: %+v", reg)
	}
	return e.PermissionMode
}

// failingModeRunner refuses every posture send, so the delivery site's
// fire-and-forget Info record actually fires. The double the pool tests use
// accepts them, which would leave the no-log assertion below vacuous.
type failingModeRunner struct{ fakeRunner }

func (failingModeRunner) SetPermissionMode(string) error {
	return errors.New("streamsup: no live child")
}

// TestPool_UpdateSettings_PermissionModeDerivation (AC #1) pins the pinned table:
// the posture's two stored fields move together, and an update naming either one
// derives both. Read as a whole rather than row by row — the interesting claim is
// that no row leaves the pair able to disagree.
//
// The third row is the one worth stating: a yolo:false must not drag an operator
// out of plan and into default as a side effect of naming a field it was not
// asked about. It carries a model change too, because a yolo:false against a
// non-bypass session changes nothing on its own and returns as a no-op before any
// of this.
func TestPool_UpdateSettings_PermissionModeDerivation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		stored   SessionSettings
		update   SettingsUpdate
		want     SessionSettings
		wantDisk string // the raw permission_mode value, "" when the key is absent
	}{
		{
			name:   "yolo enable becomes the escalation",
			stored: SessionSettings{},
			update: SettingsUpdate{YOLO: ptr(true)},
			want:   SessionSettings{YOLO: true, PermissionMode: permissionModeBypass},
		},
		{
			name:   "yolo revoke from the escalation lands in default",
			stored: SessionSettings{YOLO: true},
			update: SettingsUpdate{YOLO: ptr(false)},
			want:   SessionSettings{PermissionMode: permissionModeDefault},
		},
		{
			name:     "yolo revoke leaves a non-bypass mode alone",
			stored:   SessionSettings{Model: "sonnet", PermissionMode: "plan"},
			update:   SettingsUpdate{Model: ptr("opus"), YOLO: ptr(false)},
			want:     SessionSettings{Model: "opus", PermissionMode: "plan"},
			wantDisk: "plan",
		},
		{
			name:   "the escalation named as a mode sets yolo",
			stored: SessionSettings{PermissionMode: "plan"},
			update: SettingsUpdate{PermissionMode: ptr(permissionModeBypass)},
			want:   SessionSettings{YOLO: true, PermissionMode: permissionModeBypass},
		},
		{
			name:     "any other known mode clears yolo",
			stored:   SessionSettings{YOLO: true},
			update:   SettingsUpdate{PermissionMode: ptr("acceptEdits")},
			want:     SessionSettings{PermissionMode: "acceptEdits"},
			wantDisk: "acceptEdits",
		},
		{
			name:   "a mode and a yolo that agree derive one posture",
			stored: SessionSettings{},
			update: SettingsUpdate{PermissionMode: ptr(permissionModeBypass), YOLO: ptr(true)},
			want:   SessionSettings{YOLO: true, PermissionMode: permissionModeBypass},
		},
		{
			name:     "back to default from a stored mode",
			stored:   SessionSettings{PermissionMode: "dontAsk"},
			update:   SettingsUpdate{PermissionMode: ptr(permissionModeDefault)},
			want:     SessionSettings{PermissionMode: permissionModeDefault},
			wantDisk: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regPath := filepath.Join(t.TempDir(), "sessions.json")
			pool := helperRestartPool(t, regPath, t.TempDir(), tc.stored)
			id := pool.Default().ID()

			if err := pool.UpdateSettings(id, tc.update); err != nil {
				t.Fatalf("UpdateSettings: %v", err)
			}

			got, err := pool.SettingsFor(id)
			if err != nil {
				t.Fatalf("SettingsFor: %v", err)
			}
			if got != tc.want {
				t.Errorf("stored settings = %+v, want %+v", got, tc.want)
			}
			// The invariant, asserted independently of the row's own expectation
			// so a table typo cannot hide a pair that disagrees.
			if got.YOLO != (got.PermissionMode == permissionModeBypass) {
				t.Errorf("stored posture disagrees with itself: %+v", got)
			}
			if disk := diskPermissionMode(t, regPath); disk != tc.wantDisk {
				t.Errorf("on-disk permission_mode = %q, want %q", disk, tc.wantDisk)
			}
		})
	}
}

// TestPool_UpdateSettings_RejectsPermissionMode (AC #1's rejection half): an
// unrecognised mode and a self-contradictory frame are both refused with nothing
// persisted and nothing delivered — which is what keeps an unrecognised value out
// of the registry, and therefore out of every argv composed from it later.
//
// The near misses are rows of their own: a wrong case and a stray space are
// exactly what a closed vocabulary buys over a deny-list, and "" is a row because
// SettingsUpdate treats it as a real value for Model and Effort.
func TestPool_UpdateSettings_RejectsPermissionMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		update  SettingsUpdate
		wantErr error
	}{
		{"unknown mode", SettingsUpdate{PermissionMode: ptr("yolo")}, ErrUnsupportedPermissionMode},
		{"empty mode", SettingsUpdate{PermissionMode: ptr("")}, ErrUnsupportedPermissionMode},
		{"wrong case", SettingsUpdate{PermissionMode: ptr("Plan")}, ErrUnsupportedPermissionMode},
		{"trailing space", SettingsUpdate{PermissionMode: ptr("plan ")}, ErrUnsupportedPermissionMode},
		{"a flag as a mode", SettingsUpdate{PermissionMode: ptr("--dangerously-skip-permissions")}, ErrUnsupportedPermissionMode},
		{"escalating mode with yolo false", SettingsUpdate{PermissionMode: ptr(permissionModeBypass), YOLO: ptr(false)}, ErrPermissionModeConflict},
		{"plain mode with yolo true", SettingsUpdate{PermissionMode: ptr("plan"), YOLO: ptr(true)}, ErrPermissionModeConflict},
		// The rejection wins over every other field in the frame, so a rejected
		// update cannot half-apply a model change.
		{"rejection beats a valid model", SettingsUpdate{Model: ptr("opus"), PermissionMode: ptr("nope")}, ErrUnsupportedPermissionMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regPath := filepath.Join(t.TempDir(), "sessions.json")
			stored := SessionSettings{Model: "sonnet", PermissionMode: "plan"}
			pool := helperRestartPool(t, regPath, t.TempDir(), stored)
			id := pool.Default().ID()
			runner := runnerDouble(t, pool, id)

			err := pool.UpdateSettings(id, tc.update)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("UpdateSettings = %v, want %v", err, tc.wantErr)
			}
			if got, _ := pool.SettingsFor(id); got != stored {
				t.Errorf("a rejected update changed the stored settings to %+v, want %+v", got, stored)
			}
			if disk := diskPermissionMode(t, regPath); disk != "plan" {
				t.Errorf("a rejected update wrote permission_mode %q to disk, want the stored %q", disk, "plan")
			}
			if got := runner.permissionModes(); len(got) != 0 {
				t.Errorf("a rejected update delivered %v to the child", got)
			}
			if got := runner.restartArgs(); len(got) != 0 {
				t.Errorf("a rejected update respawned the child: Restart%v", got)
			}
			if got := runner.spawnArgSets(); len(got) != 0 {
				t.Errorf("a rejected update installed an argv for the next spawn: %v", got)
			}
		})
	}
}

// TestPool_UpdateSettings_InBandMode (AC #3): a mode in the in-band five reaches
// the live child through the seam's mode-carrying method with no respawn, and the
// argv the SAME update installs carries it, so a crash-respawn does not revert it.
func TestPool_UpdateSettings_InBandMode(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)
	waitRunning(t, runner)

	if err := pool.UpdateSettings(id, SettingsUpdate{PermissionMode: ptr("acceptEdits")}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	if got, want := runner.permissionModes(), []string{"acceptEdits"}; !reflect.DeepEqual(got, want) {
		t.Errorf("in-band posture sends = %v, want exactly %v", got, want)
	}
	if got := runner.restartArgs(); len(got) != 0 {
		t.Errorf("an in-band mode respawned the child: Restart%v", got)
	}
	if got := runner.userTurns(); len(got) != 0 {
		t.Errorf("a mode-only update invented a command: %q", got)
	}
	installs := runner.spawnArgSets()
	if len(installs) != 1 {
		t.Fatalf("SetSpawnArgs called %d times, want exactly 1: %v", len(installs), installs)
	}
	if got, want := installedArgv(t, installs[0]), alwaysOnPosture("acceptEdits"); !reflect.DeepEqual(got, want) {
		t.Errorf("installed argv = %v, want %v", got, want)
	}
}

// TestPool_UpdateSettings_RevokeKeepsStoredMode is the third table row's DELIVERY
// half: the value sent in-band is the posture that RESULTS, not the field the
// frame named. A yolo:false against a stored plan re-sends plan — the child is
// probably already in it, which is the redundancy this path already tolerates for
// an unchanged model re-sent alongside a new effort.
//
// It is the direct red for a delivery site that sends "default" whenever the
// frame carried a yolo:false, which would silently downgrade an operator's plan.
func TestPool_UpdateSettings_RevokeKeepsStoredMode(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet", PermissionMode: "plan"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)
	waitRunning(t, runner)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus"), YOLO: ptr(false)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	if got, want := runner.permissionModes(), []string{"plan"}; !reflect.DeepEqual(got, want) {
		t.Errorf("in-band posture sends = %v, want exactly %v", got, want)
	}
	if got, want := runner.modelRequests(), []string{"opus"}; !reflect.DeepEqual(got, want) {
		t.Errorf("delivered model requests = %q, want %q", got, want)
	}
	if got := runner.userTurns(); len(got) != 0 {
		t.Errorf("model update wrote user turns %q, want none", got)
	}
	installs := runner.spawnArgSets()
	if len(installs) != 1 {
		t.Fatalf("SetSpawnArgs called %d times, want exactly 1: %v", len(installs), installs)
	}
	want := append([]string{"--model", "opus"}, alwaysOnPosture("plan")...)
	if got := installedArgv(t, installs[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("installed argv = %v, want %v", got, want)
	}
}

// TestPool_UpdateSettings_BypassMode_MixedWithClearedValueTakesRestart is #2043's
// AC #4 rewritten for #2066. That ticket routed the escalation in band, so a
// mode-spelled enable ALONE no longer reaches the restart — see
// TestPool_UpdateSettings_InBand_Escalation_NoRespawn for the pin on that.
//
// What this covers is the surviving route and it is worth keeping: an escalation
// mixed with a Model cleared to "" takes the restart, because the empty-value
// reject outranks a posture change in the same frame, and the relaunched child
// still carries the escalation spelled by the skip-permissions flag ALONE, never as
// a --permission-mode value. That is the "no frame can lose a posture change by
// mixing" claim in inBandDeliverable's doc, asserted rather than argued.
func TestPool_UpdateSettings_BypassMode_MixedWithClearedValueTakesRestart(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)
	waitRunning(t, runner)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr(""), PermissionMode: ptr(permissionModeBypass)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	restarts := runner.restartArgs()
	if len(restarts) != 1 {
		t.Fatalf("Restart called %d times, want exactly 1: %v", len(restarts), restarts)
	}
	if got, want := installedArgv(t, restarts[0]), []string{"--dangerously-skip-permissions"}; !reflect.DeepEqual(got, want) {
		t.Errorf("restart argv = %v, want %v", got, want)
	}
	if got := runner.permissionModes(); len(got) != 0 {
		t.Errorf("the escalation travelled over the control channel: %v", got)
	}
	// The stored pair, and the on-disk shape: the escalation keeps ONE spelling,
	// the yolo key, so nothing on disk can contradict it.
	if got, _ := pool.SettingsFor(id); got != (SessionSettings{YOLO: true, PermissionMode: permissionModeBypass}) {
		t.Errorf("stored settings = %+v, want the escalation", got)
	}
	if disk := diskPermissionMode(t, regPath); disk != "" {
		t.Errorf("on-disk permission_mode = %q, want no key: yolo is the escalation's one spelling", disk)
	}
}

// TestPool_UpdateSettings_WarmStart_SpawnsUnderStoredMode (AC #2's spawn half): a
// session rebuilt out of the registry is spawned under its stored posture, from
// either shape of entry — one carrying a permission_mode key, and one written
// before #2043 whose yolo is all it has.
//
// The argv is read off the construction record, which is where a daemon restart
// would produce it: Pool.New composes it from the settings settingsFromEntry
// derived.
func TestPool_UpdateSettings_WarmStart_SpawnsUnderStoredMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stored SessionSettings
		want   []string
	}{
		{"a stored in-band mode", SessionSettings{PermissionMode: "plan"}, alwaysOnPosture("plan")},
		// helperRestartPool writes the entry through permissionModeForDisk, so
		// both rows below produce an entry with NO permission_mode key — exactly
		// the pre-#2043 on-disk shape.
		{"a pre-2043 entry with yolo", SessionSettings{YOLO: true}, escalatedPosture()},
		{"a pre-2043 entry without yolo", SessionSettings{Model: "sonnet"}, append([]string{"--model", "sonnet"}, alwaysOnPosture(permissionModeDefault)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regPath := filepath.Join(t.TempDir(), "sessions.json")
			tplWorkDir := t.TempDir()

			pool := helperRestartPool(t, regPath, tplWorkDir, tc.stored)
			runPoolInBackground(t, pool)

			if got := waitArgv(t, tplWorkDir); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("spawn argv = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPool_PermissionModeNeverLogged (AC #5): no permission-mode value reaches the
// daemon log — not in the delivery record, and not inside either rejection error
// that a caller might log verbatim.
//
// The delivery half uses a runner that REFUSES the send, because the record only
// fires on a failure; with the accepting double the buffer would be empty and the
// assertion vacuous. The "permission_mode" check is what proves the record fired
// at all, so an implementation that logs nothing cannot pass by silence.
//
// It deliberately does not drive a spawning pool: (*streamsup.Runner) logs the
// whole argv at Info on every spawn, which has always exposed --model and
// --effort values and now exposes --permission-mode identically. That record is
// pre-existing and out of this ticket's scope; scoping the buffer to the update
// path keeps this test honest about the paths #2043 owns.
func TestPool_PermissionModeNeverLogged(t *testing.T) {
	t.Parallel()
	const mode = "acceptEdits"

	var buf bytes.Buffer
	p := &Pool{log: slog.New(slog.NewTextHandler(&buf, nil))}
	p.deliverSettingsInBand(
		SessionID("550e8400-e29b-41d4-a716-446655440000"),
		failingModeRunner{},
		SettingsUpdate{PermissionMode: ptr(mode)},
		SessionSettings{PermissionMode: mode},
	)
	logged := buf.String()
	if !strings.Contains(logged, "permission_mode") {
		t.Fatalf("no delivery record was written at all: %q", logged)
	}
	if !strings.Contains(logged, "sessions.settings.delivery_err") {
		t.Errorf("delivery record lacks a stable event key: %q", logged)
	}
	if strings.Contains(logged, mode) {
		t.Errorf("the delivery record leaked the mode value: %q", logged)
	}

	for _, err := range []error{ErrUnsupportedPermissionMode, ErrPermissionModeConflict} {
		if strings.Contains(err.Error(), mode) || strings.Contains(err.Error(), "plan") {
			t.Errorf("%v echoes a mode value; a caller logging it verbatim would leak it", err)
		}
	}
	rejected := validatePermissionUpdate(SettingsUpdate{PermissionMode: ptr("hostile-value-42")})
	if rejected == nil {
		t.Fatal("validatePermissionUpdate accepted an unknown mode")
	}
	if strings.Contains(rejected.Error(), "hostile-value-42") {
		t.Errorf("the rejection echoed the caller's value: %q", rejected.Error())
	}
}

// TestPool_UpdateSettings_InstallsSpawnPostureOnBothBranches (#2064): the posture the
// runner asserts to every child it SPAWNS is kept in step with the stored one on both
// of UpdateSettings' branches, because neither branch rebuilds the runner.
//
// The RESTART row is the one that earns the placement above the branch split, and it
// is the sole red for the cheaper design that folds the install into
// deliverSettingsInBand: that path is never reached on the restart branch, so the
// runner would keep asserting the pre-change mode and a later crash-respawn would
// re-assert a posture the operator had already replaced. #2066 changed which update
// reaches that branch — a mode-spelled escalation used to, and now goes in band — so
// the row pairs the escalation with a Model cleared to "", which is what puts a frame
// on the restart today. The paired permissionModes() assertion is what keeps the two
// calls distinguishable: the restart branch installs WITHOUT writing anything to the
// live child, and the in-band branch does both.
func TestPool_UpdateSettings_InstallsSpawnPostureOnBothBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// start is the session's settings before the update. Only the restart row
		// needs a non-zero one, so its cleared-Model is a real change.
		start SessionSettings
		// update is applied to a session that starts at the default posture.
		update SettingsUpdate
		// wantSpawn is the posture every later spawn must assert afterwards.
		wantSpawn string
		// wantLive is what the LIVE child was told over the control channel.
		wantLive []string
	}{
		{
			name:      "in-band branch installs the new mode",
			update:    SettingsUpdate{PermissionMode: ptr("plan")},
			wantSpawn: "plan",
			wantLive:  []string{"plan"},
		},
		{
			name:      "in-band branch installs bypass and writes it (#2066)",
			update:    SettingsUpdate{PermissionMode: ptr(permissionModeBypass)},
			wantSpawn: permissionModeBypass,
			wantLive:  []string{permissionModeBypass},
		},
		{
			name:      "restart branch installs bypass without writing it",
			start:     SessionSettings{Model: "sonnet"},
			update:    SettingsUpdate{Model: ptr(""), PermissionMode: ptr(permissionModeBypass)},
			wantSpawn: permissionModeBypass,
			wantLive:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regPath := filepath.Join(t.TempDir(), "sessions.json")
			pool := helperRestartPool(t, regPath, t.TempDir(), tc.start)
			runPoolInBackground(t, pool)
			id := pool.Default().ID()
			runner := runnerDouble(t, pool, id)
			waitRunning(t, runner)

			if err := pool.UpdateSettings(id, tc.update); err != nil {
				t.Fatalf("UpdateSettings: %v", err)
			}

			// Exactly one install, carrying the MERGED posture — not a count of zero
			// (the branch was missed) and not two (an install duplicated per branch).
			installs := runner.spawnPermissionModes()
			if len(installs) != 1 || installs[0] != tc.wantSpawn {
				t.Fatalf("spawn posture installs = %v, want exactly [%s]", installs, tc.wantSpawn)
			}
			if got := runner.permissionModes(); !reflect.DeepEqual(got, tc.wantLive) {
				t.Errorf("live-child control writes = %v, want %v", got, tc.wantLive)
			}
		})
	}
}
