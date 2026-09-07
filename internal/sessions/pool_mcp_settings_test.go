package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitArgv is waitArgvRaw with the #943 "--settings <path>" and #2093
// "--append-system-prompt-file <path>" pairs stripped, so the argv assertions
// that predate each keep comparing only the flags they own. Every interactive
// spawn now carries both pairs; the tests in this file assert the settings pair's
// presence, placement, and file content directly via waitArgvRaw, and
// pool_system_prompt_test.go does the same for the prompt pair.
func waitArgv(t *testing.T, dir string) []string {
	t.Helper()
	return stripSystemPrompt(t, stripMCPSettings(t, waitArgvRaw(t, dir)))
}

// stripMCPSettings removes the adjacent "--settings <path>" pair that #943
// injects into every interactive spawn's base and returns the remaining tokens.
// It fatals if the pair is absent — post-#943 every spawn carries it — so the
// pre-#943 argv assertions double as a regression check that the flag is present.
func stripMCPSettings(t *testing.T, argv []string) []string {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--settings" {
			out := make([]string, 0, len(argv)-2)
			out = append(out, argv[:i]...)
			out = append(out, argv[i+2:]...)
			return out
		}
	}
	t.Fatalf("argv %v missing --settings <path> pair (#943)", argv)
	return nil
}

// settingsArgPath asserts argv carries exactly one adjacent "--settings <path>"
// pair and returns the path.
func settingsArgPath(t *testing.T, argv []string) string {
	t.Helper()
	idx := -1
	for i, a := range argv {
		if a == "--settings" {
			if idx != -1 {
				t.Fatalf("argv %v carries more than one --settings flag", argv)
			}
			idx = i
		}
	}
	if idx == -1 {
		t.Fatalf("argv %v missing --settings flag", argv)
	}
	if idx+1 >= len(argv) {
		t.Fatalf("argv %v has --settings with no path", argv)
	}
	return argv[idx+1]
}

// assertMCPSettingsFile asserts the file at path is the interactive MCP-enable
// settings file: parseable JSON with enableAllProjectMcpServers:true (AC #1) and
// no permission-posture tokens (AC #2).
func assertMCPSettingsFile(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings file %q: %v", path, err)
	}
	var got struct {
		EnableAllProjectMcpServers bool `json:"enableAllProjectMcpServers"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("settings file %q is not JSON: %v (%s)", path, err, raw)
	}
	if !got.EnableAllProjectMcpServers {
		t.Errorf("settings file %q: enableAllProjectMcpServers=false, want true (%s)", path, raw)
	}
	for _, forbidden := range []string{"permissions", "defaultMode", "dontAsk", "allow", "deny"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("settings file %q must not contain %q (AC #2): %s", path, forbidden, raw)
		}
	}
}

// TestPool_BootstrapSpawn_IncludesMCPSettings (AC #4/#1): the bootstrap claude's
// argv carries "--settings <path>" and the referenced file enables all project
// MCP servers, so claude's "N new MCP servers found" modal never renders.
func TestPool_BootstrapSpawn_IncludesMCPSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	runPoolInBackground(t, pool)

	argv := waitArgvRaw(t, tplWorkDir)
	assertMCPSettingsFile(t, settingsArgPath(t, argv))
}

// TestPool_MintedSpawn_IncludesMCPSettings (AC #4/#1): a minted session's argv
// carries the same "--settings <path>" pointing at an MCP-enable file.
func TestPool_MintedSpawn_IncludesMCPSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{})

	argv := waitArgvRaw(t, spawnDir)
	assertMCPSettingsFile(t, settingsArgPath(t, argv))
}

// TestPool_LiveRestartRecompose_KeepsMCPSettings (AC #3): a live settings-restart
// (#842) recomposes the argv from spawnBase; the "--settings <path>" element must
// survive the recompose pointing at the same, still-readable file.
func TestPool_LiveRestartRecompose_KeepsMCPSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()

	// First spawn: capture the --settings path and confirm the file is present.
	firstPath := settingsArgPath(t, waitArgvRaw(t, tplWorkDir))
	assertMCPSettingsFile(t, firstPath)
	clearRecording(t, tplWorkDir)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus")}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	// The restart's argv still carries --settings, at the SAME path, still readable.
	restartPath := settingsArgPath(t, waitArgvRaw(t, tplWorkDir))
	if restartPath != firstPath {
		t.Errorf("restart --settings path = %q, want stable %q", restartPath, firstPath)
	}
	assertMCPSettingsFile(t, restartPath)
}

// TestPool_Remove_CleansUpMintedSettingsFile (AC #3): removing a minted session
// deletes its per-session settings file (cleanup runs after the child is dead).
func TestPool_Remove_CleansUpMintedSettingsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	id := spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{})

	pool.mu.RLock()
	path := pool.sessions[id].settingsPath
	pool.mu.RUnlock()
	if path == "" {
		t.Fatal("minted session has empty settingsPath")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("settings file missing before Remove: %v", err)
	}

	if err := pool.Remove(ctx, id, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("settings file %q still present after Remove (err=%v), want removed", path, err)
	}
}

// TestPool_Run_CleansUpBootstrapSettingsFile (AC #3): the bootstrap file is never
// Remove-d (ErrCannotRemoveBootstrap); it lives for the daemon-process lifetime
// and is removed when Pool.Run returns on ctx cancel.
func TestPool_Run_CleansUpBootstrapSettingsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)

	pool.mu.RLock()
	path := pool.sessions[pool.bootstrap].settingsPath
	pool.mu.RUnlock()
	if path == "" {
		t.Fatal("bootstrap session has empty settingsPath")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("bootstrap settings file missing before Run: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()

	// Wait until the bootstrap has actually spawned before cancelling, so Run is
	// past the point where the cleanup defer is registered.
	waitArgvRaw(t, tplWorkDir)
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("pool.Run did not return within 15s after ctx cancel")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("bootstrap settings file %q still present after Run returned (err=%v), want removed", path, err)
	}
}

// helperPoolFakeRunner builds a Pool against registryPath whose runner factory
// returns the no-op fakeRunner, so New completes with no PTY, no child, and no
// lifecycle goroutine. The #1518 tests below need New's on-disk side effects
// only — they never run the pool.
func helperPoolFakeRunner(t *testing.T, registryPath string, factory RunnerFactory) (*Pool, error) {
	t.Helper()
	return New(Config{
		Bootstrap:     SessionConfig{ClaudeBin: "/nonexistent/claude-should-never-be-execd"},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath:  registryPath,
		RunnerFactory: factory,
	})
}

// settingsDirOf returns the per-session settings directory for a data dir.
func settingsDirOf(dataDir string) string {
	return filepath.Join(dataDir, "session-settings")
}

// assertSettingsDirEmpty asserts the per-session settings directory holds no
// entries at all — not even a scratch file. A still-absent directory counts as
// empty: a construction that failed before the write never created it.
func assertSettingsDirEmpty(t *testing.T, settingsDir string) {
	t.Helper()
	entries, err := os.ReadDir(settingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("readdir %q: %v", settingsDir, err)
	}
	if len(entries) == 0 {
		return
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Errorf("settings dir %q holds %v, want empty (a failed construction orphaned its settings file)", settingsDir, names)
}

// TestPool_BootstrapSpawn_SettingsFileUnderDataDir (#1518, AC #1): the bootstrap's
// --settings argv value — the immutable spawnBase element every backoff restart,
// evict reactivation, and #842 live restart re-execs with — points at
// <dataDir>/session-settings/<bootstrapID>.json, not into an OS temp dir where a
// multi-day reaper can delete it out from under a still-live session.
func TestPool_BootstrapSpawn_SettingsFileUnderDataDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	runPoolInBackground(t, pool)

	path := settingsArgPath(t, waitArgvRaw(t, tplWorkDir))
	if want := filepath.Join(settingsDirOf(dir), string(pool.BootstrapID())+".json"); path != want {
		t.Errorf("bootstrap --settings = %q, want %q", path, want)
	}
	assertMCPSettingsFile(t, path)
}

// TestPool_MintedSpawn_SettingsFileUnderDataDir (#1518, AC #1): a minted session's
// --settings argv value likewise lives under the data dir, keyed on its own id.
func TestPool_MintedSpawn_SettingsFileUnderDataDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	id := spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{})

	path := settingsArgPath(t, waitArgvRaw(t, spawnDir))
	if want := filepath.Join(settingsDirOf(dir), string(id)+".json"); path != want {
		t.Errorf("minted --settings = %q, want %q", path, want)
	}
	assertMCPSettingsFile(t, path)
}

// TestPool_RepeatedNew_SettingsFilesBoundedBySessions (#1518, AC #4): restarting
// the daemon against the same registry must not accumulate settings files. A warm
// start reuses the persisted bootstrap id, and the filename is derived from it, so
// restart N overwrites restart N-1's file. This is what makes the bound hold by
// construction rather than by a startup sweeper, and it goes red the moment the
// name reverts to a random suffix.
func TestPool_RepeatedNew_SettingsFilesBoundedBySessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")

	var bootstrapID SessionID
	for i := range 3 {
		pool, err := helperPoolFakeRunner(t, regPath, func(RunnerConfig) (Runner, error) {
			return fakeRunner{}, nil
		})
		if err != nil {
			t.Fatalf("New (start %d): %v", i, err)
		}
		if i == 0 {
			bootstrapID = pool.BootstrapID()
		} else if pool.BootstrapID() != bootstrapID {
			t.Fatalf("start %d warm-started onto id %q, want the persisted %q", i, pool.BootstrapID(), bootstrapID)
		}
	}

	// Glob *.json rather than counting directory entries: the atomic writer's
	// scratch pattern is ".settings-*.json.tmp", which must never be mistaken for
	// a session's settings file.
	settingsDir := settingsDirOf(dir)
	matches, err := filepath.Glob(filepath.Join(settingsDir, "*.json"))
	if err != nil {
		t.Fatalf("glob %q: %v", settingsDir, err)
	}
	if len(matches) != 1 {
		t.Errorf("after 3 daemon starts, settings dir holds %v, want exactly one file (one per session, not one per restart)", matches)
	}
}

// TestPool_New_RunnerFailure_RemovesSettingsFile (#1518, AC #5): when the bootstrap
// runner fails to construct after the settings file has been written, New removes
// the file before returning. In the OS temp dir the orphan was eventually reaped;
// in the data dir it would be permanent and would accumulate across every failed
// start.
func TestPool_New_RunnerFailure_RemovesSettingsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")

	if _, err := helperPoolFakeRunner(t, regPath, func(RunnerConfig) (Runner, error) {
		return nil, errors.New("runner factory refused")
	}); err == nil {
		t.Fatal("New succeeded, want the runner-factory error")
	}

	assertSettingsDirEmpty(t, settingsDirOf(dir))
}

// TestPool_BuildSession_RunnerFailure_RemovesSettingsFile (#1518, AC #5): the same
// at the second write site. The bootstrap's file is asserted still present in the
// same check, so this also proves the cleanup is scoped to the session that failed
// rather than wiping the directory.
func TestPool_BuildSession_RunnerFailure_RemovesSettingsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")

	pool, err := helperPoolFakeRunner(t, regPath, func(RunnerConfig) (Runner, error) {
		return fakeRunner{}, nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pool.mu.RLock()
	bootstrapPath := pool.sessions[pool.bootstrap].settingsPath
	pool.mu.RUnlock()
	if bootstrapPath == "" {
		t.Fatal("bootstrap session has empty settingsPath")
	}

	// Same-package swap of the pool's runner factory: nothing is running, so no
	// goroutine observes the field.
	pool.newRunner = func(RunnerConfig) (Runner, error) {
		return nil, errors.New("runner factory refused")
	}

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := pool.buildSession(id, "", "", SessionSettings{}); err == nil {
		t.Fatal("buildSession succeeded, want the runner error")
	}

	minted := filepath.Join(settingsDirOf(dir), string(id)+".json")
	if _, err := os.Stat(minted); !os.IsNotExist(err) {
		t.Errorf("settings file %q survived a failed buildSession (err=%v), want removed", minted, err)
	}
	if _, err := os.Stat(bootstrapPath); err != nil {
		t.Errorf("bootstrap settings file %q was collateral damage: %v", bootstrapPath, err)
	}
}
