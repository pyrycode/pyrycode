package sessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitArgv is waitArgvRaw with the #943 "--settings <path>" pair stripped, so the
// many pre-#943 argv assertions keep comparing only the flags they own. Every
// interactive spawn now carries the pair; the tests in this file assert its
// presence, placement, and file content directly via waitArgvRaw.
func waitArgv(t *testing.T, dir string) []string {
	t.Helper()
	return stripMCPSettings(t, waitArgvRaw(t, dir))
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
