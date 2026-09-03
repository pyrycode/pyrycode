package sessions

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// helperPoolMintBootstrap pre-writes a registry whose bootstrap entry carries
// the given settings, then warm-starts an argv-recording pool from that same
// path — the fixture recipe TestPool_BootstrapWarmStart_AppliesSettingsToArgv
// established. Every test in this file needs it, because "the operator's
// configuration" is only observable as what the bootstrap entry persisted.
func helperPoolMintBootstrap(t *testing.T, regPath, tplWorkDir string, settings SessionSettings) *Pool {
	t.Helper()
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
		}},
	}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}
	return helperPoolArgvRecorder(t, regPath, tplWorkDir)
}

// TestPool_CreateIn_InheritsOperatorSettings (AC 1): a session minted through
// the public CreateIn entry point spawns with the operator's configured model
// and effort, not claude's own defaults.
//
// Driven through CreateIn deliberately: spawnMintedWithSettings calls
// buildSession directly and would stay green even if the entry point were never
// changed.
func TestPool_CreateIn_InheritsOperatorSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolMintBootstrap(t, regPath, tplWorkDir, SessionSettings{Model: "opus", Effort: "high"})
	ctx, _ := runPoolInBackground(t, pool)

	id, err := pool.CreateIn(ctx, "", spawnDir)
	if err != nil {
		t.Fatalf("CreateIn: %v", err)
	}

	got := waitArgv(t, spawnDir)
	want := append([]string{"--session-id", string(id), "--model", "opus", "--effort", "high"},
		alwaysOnPosture(permissionModeDefault)...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("minted argv = %v, want %v", got, want)
	}
}

// TestPool_GetOrCreateIn_InheritsOperatorSettings (AC 1): the register path of
// the other public mint entry point inherits identically. GetOrCreateIn passes
// the settings into materialise; the take path never reaches buildSession, so
// this is the only leg that can inherit.
func TestPool_GetOrCreateIn_InheritsOperatorSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolMintBootstrap(t, regPath, tplWorkDir, SessionSettings{Model: "opus", Effort: "high"})
	ctx, _ := runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := pool.GetOrCreateIn(ctx, target, "", spawnDir); err != nil {
		t.Fatalf("GetOrCreateIn: %v", err)
	}

	got := waitArgv(t, spawnDir)
	want := append([]string{"--session-id", string(target), "--model", "opus", "--effort", "high"},
		alwaysOnPosture(permissionModeDefault)...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("minted argv = %v, want %v", got, want)
	}
}

// TestPool_MintedSession_NeverInheritsBypass (AC 2): the permission bypass is
// excluded from inheritance regardless of what the source settings hold. The
// bootstrap here HAS bypass enabled and its own argv is asserted to carry the
// flag, so the absence assertion on the minted argv is not vacuous.
func TestPool_MintedSession_NeverInheritsBypass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolMintBootstrap(t, regPath, tplWorkDir, SessionSettings{Model: "opus", Effort: "high", YOLO: true})
	ctx, _ := runPoolInBackground(t, pool)

	// The fixture proof: the bootstrap really did take the escalation. Since #2065
	// the FLAG is on every argv, so its presence proves nothing — what separates an
	// escalated session from a downgraded one is the absence of the --permission-mode
	// pair beside it, which claudeSettingsArgs suppresses only for a stored
	// bypassPermissions. That absence is what makes the minted assertion below
	// non-vacuous.
	bootArgv := waitArgv(t, tplWorkDir)
	if !slices.Contains(bootArgv, "--dangerously-skip-permissions") {
		t.Fatalf("bootstrap argv = %v, want it to carry --dangerously-skip-permissions", bootArgv)
	}
	if slices.Contains(bootArgv, "--permission-mode") {
		t.Fatalf("bootstrap argv = %v names a permission mode, so the yolo fixture never took: "+
			"an escalated session composes the flag ALONE", bootArgv)
	}

	id, err := pool.CreateIn(ctx, "", spawnDir)
	if err != nil {
		t.Fatalf("CreateIn: %v", err)
	}

	// The minted session inherits model and effort but NOT the posture, and since
	// #2065 "not inherited" is spelled as a downgrade rather than as an omission:
	// the minted child launches in bypass like every other, and carries
	// --permission-mode default, which is the posture the daemon writes it back to
	// before any turn reaches it. A tree that let the escalation be inherited would
	// compose the bare flag here, exactly as the bootstrap above does.
	got := waitArgv(t, spawnDir)
	want := append([]string{"--session-id", string(id), "--model", "opus", "--effort", "high"},
		alwaysOnPosture(permissionModeDefault)...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("minted argv = %v, want %v (the escalation must never be inherited)", got, want)
	}
}

// TestPool_Revive_DoesNotInheritOperatorSettings (AC 3): a revived session
// still carries zero settings. Revive shares materialise with GetOrCreateIn, so
// this is what stops the mint-path change from being applied there
// unconditionally — reading mintSettings inside materialise reddens this test.
//
// The bootstrap's own argv is asserted in the same run so the test cannot pass
// by the fixture silently failing to take.
func TestPool_Revive_DoesNotInheritOperatorSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolMintBootstrap(t, regPath, tplWorkDir, SessionSettings{Model: "opus", Effort: "high"})
	ctx, _ := runPoolInBackground(t, pool)

	if got, want := waitArgv(t, tplWorkDir), append([]string{"--model", "opus", "--effort", "high"}, alwaysOnPosture(permissionModeDefault)...); !reflect.DeepEqual(got, want) {
		t.Fatalf("bootstrap argv = %v, want %v — the fixture never took", got, want)
	}

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	sess, err := pool.Revive(target, "conv-1", spawnDir)
	if err != nil {
		t.Fatalf("Revive: %v", err)
	}
	// Revive registers without spawning; Activate is what brings the child up.
	if err := pool.Activate(ctx, target); err != nil {
		t.Fatalf("Activate(revived): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool { return sess.State().ChildPID > 0 }) {
		t.Fatalf("revived session never spawned a child; state=%+v lc=%v", sess.State(), sess.LifecycleState())
	}

	// AC 1's revive row. A revived session inherits nothing, and since #2065 that
	// is spelled as a DOWNGRADE: the revived child launches in bypass like every
	// other and carries --permission-mode default, the posture the daemon writes it
	// back to. The mode pair is the assertion — a tree that left a revived child in
	// bypass would compose the bare flag, which is what an escalated session looks
	// like and is exactly the #1487 property that must not regress.
	got := waitArgv(t, spawnDir)
	want := append([]string{"--session-id", string(target)}, alwaysOnPosture(permissionModeDefault)...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("revived argv = %v, want %v — a revived session inherits nothing and is downgraded, never left in bypass (#1487)", got, want)
	}
}

// TestPool_CreateIn_NoConfiguration_ArgvByteIdentical (AC 4, first case): a
// bootstrap with empty model and empty effort mints an argv byte-identical to
// today's — no flag at all, not an empty-valued one.
func TestPool_CreateIn_NoConfiguration_ArgvByteIdentical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolMintBootstrap(t, regPath, tplWorkDir, SessionSettings{})
	ctx, _ := runPoolInBackground(t, pool)

	id, err := pool.CreateIn(ctx, "", spawnDir)
	if err != nil {
		t.Fatalf("CreateIn: %v", err)
	}

	got := waitArgv(t, spawnDir)
	want := append([]string{"--session-id", string(id)}, alwaysOnPosture(permissionModeDefault)...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("minted argv = %v, want %v", got, want)
	}
}

// TestPool_MintSettings_NoBootstrap_NoFlags (AC 4, second case): with no
// bootstrap to read from, inheritance contributes no argv tokens.
//
// Asserted on the composing helper rather than through a mint entry point
// because no pool that can mint has this shape: Pool.New always registers a
// bootstrap, so DefaultSettings only reports false for the bare &Pool{} literal
// TestPool_DefaultSettings_NoBootstrap uses — and that pool has a nil newRunner
// and no runGroup, so it can never reach buildSession.
func TestPool_MintSettings_NoBootstrap_NoFlags(t *testing.T) {
	t.Parallel()
	p := &Pool{}
	// Since #2065 "contributes no argv tokens" means "contributes nothing BEYOND the
	// escalation flag": inheritance still adds no --model and no --effort, which is
	// the property this pins. The flag is not inheritance.
	//
	// No --permission-mode pair, and that is faithful rather than a gap.
	// mintSettings returns a PRE-canonicalisation value — its two-field literal
	// leaves PermissionMode "" — and claudeSettingsArgs deliberately composes no
	// pair for a mode it cannot name, so the argv never claims a posture nothing
	// will write. buildSession runs canonicalSettings before composing, so the argv
	// a minted session actually spawns with does carry the pair;
	// TestPool_CreateIn_NoConfiguration_ArgvByteIdentical pins that, and
	// TestRunnerConfigPermissionModeIsAlwaysKnown pins that no construction path can
	// hand a runner an unnameable mode.
	if got, want := claudeSettingsArgs(p.mintSettings()), escalatedPosture(); !reflect.DeepEqual(got, want) {
		t.Errorf("claudeSettingsArgs(mintSettings()) with no bootstrap = %v, want %v", got, want)
	}
}
