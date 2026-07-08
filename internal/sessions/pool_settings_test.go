package sessions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// argvRecorderScript writes its own positional argv (one token per line) to
// argv.txt in its cwd, then touches a `done` sentinel so a reader can wait for
// the write to complete before reading, then execs a long sleep so the child
// stays alive. Both writes use shell builtins only (no external mv), so PATH
// resolution can't flake. Because the shell is invoked as `sh -c SCRIPT --
// <appended args...>`, $0 is "--" and "$@" expands to exactly the tokens
// buildSession / New appended after the template — the argv we assert on.
const argvRecorderScript = `printf '%s\n' "$@" > argv.txt; : > done; exec sleep 3600`

// helperPoolArgvRecorder builds a Pool whose template child records its own
// appended argv via argvRecorderScript. tplWorkDir is the bootstrap child's cwd
// (and every minted child's cwd when no per-session spawnDir is supplied).
func helperPoolArgvRecorder(t *testing.T, registryPath, tplWorkDir string) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     []string{"-c", argvRecorderScript, "--"},
			WorkDir:        tplWorkDir,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
			Bridge:         supervisor.NewBridge(logger),
		},
		Logger:       logger,
		RegistryPath: registryPath,
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// waitArgv blocks until the recorder in dir has finished (the `done` sentinel
// exists), then returns the recorded argv tokens. A nil slice means the child
// appended nothing (byte-identical baseline).
func waitArgv(t *testing.T, dir string) []string {
	t.Helper()
	if !pollUntil(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dir, "done"))
		return err == nil
	}) {
		t.Fatalf("child never finished recording argv in %q", dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "argv.txt"))
	if err != nil {
		t.Fatalf("read argv.txt in %q: %v", dir, err)
	}
	return splitLines(string(data))
}

// splitLines returns the non-empty newline-separated tokens of s. The recorder
// prints one argv token per line; an empty argv yields a single blank line,
// which we drop to a nil slice (the byte-identical baseline).
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if tok := s[start:i]; tok != "" {
				out = append(out, tok)
			}
			start = i + 1
		}
	}
	if tail := s[start:]; tail != "" {
		out = append(out, tail)
	}
	return out
}

// spawnMintedWithSettings mirrors CreateIn's create sequence (build → register
// → persist → supervise → activate) but injects an explicit SessionSettings,
// exercising the minted spawn-argv path end-to-end. #826b will plumb settings
// through the public Create path; here we drive buildSession directly.
func spawnMintedWithSettings(t *testing.T, ctx context.Context, pool *Pool, spawnDir string, settings SessionSettings) SessionID {
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
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return id
}

// TestPool_BootstrapWarmStart_AppliesSettingsToArgv (AC #1/#3/#4): a registry
// whose bootstrap entry carries Model/Effort/YOLO is warm-started by New; the
// launched claude child's argv carries the corresponding flags.
func TestPool_BootstrapWarmStart_AppliesSettingsToArgv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	when := time.Now().UTC()
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:           SessionID("550e8400-e29b-41d4-a716-446655440000"),
			CreatedAt:    when,
			LastActiveAt: when,
			Bootstrap:    true,
			Model:        "opus",
			Effort:       "high",
			YOLO:         true,
		}},
	}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	runPoolInBackground(t, pool)

	got := waitArgv(t, tplWorkDir)
	want := []string{"--model", "opus", "--effort", "high", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bootstrap argv = %v, want %v", got, want)
	}
}

// TestPool_BootstrapColdStart_ArgvByteIdentical (AC #5): a cold-start bootstrap
// (no persisted settings) launches claude with NO extra flags — byte-identical
// to today.
func TestPool_BootstrapColdStart_ArgvByteIdentical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	runPoolInBackground(t, pool)

	if got := waitArgv(t, tplWorkDir); len(got) != 0 {
		t.Errorf("cold-start bootstrap argv = %v, want empty (byte-identical baseline)", got)
	}
}

// TestPool_MintedSession_AppliesSettingsToArgv (AC #3/#4): a minted session with
// non-zero settings launches claude with the settings flags after --session-id.
func TestPool_MintedSession_AppliesSettingsToArgv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	id := spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{
		Model: "opus", Effort: "high", YOLO: true,
	})

	got := waitArgv(t, spawnDir)
	want := []string{
		"--session-id", string(id),
		"--model", "opus",
		"--effort", "high",
		"--dangerously-skip-permissions",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("minted argv = %v, want %v", got, want)
	}
}

// TestPool_MintedSession_ZeroSettings_ArgvByteIdentical (AC #5): a minted
// session with zero settings launches claude with exactly --session-id <id> and
// nothing else — byte-identical to today's minted argv.
func TestPool_MintedSession_ZeroSettings_ArgvByteIdentical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	id := spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{})

	got := waitArgv(t, spawnDir)
	want := []string{"--session-id", string(id)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("minted zero-settings argv = %v, want %v", got, want)
	}
}

// TestPool_New_CorruptYOLO_FailsAndNoSpawn (AC #2, security): driving New with a
// registry whose yolo value is corrupt fails loudly at startup — no pool, no
// session, no claude spawned. Corruption can never enable bypass.
func TestPool_New_CorruptYOLO_FailsAndNoSpawn(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	raw := `{
      "version": 1,
      "sessions": [
        {
          "id": "550e8400-e29b-41d4-a716-446655440000",
          "created_at": "2026-05-01T12:34:56.789Z",
          "last_active_at": "2026-05-01T12:34:56.789Z",
          "bootstrap": true,
          "yolo": "maybe"
        }
      ]
    }`
	if err := os.WriteFile(regPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	pool, err := New(Config{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath: regPath,
		Bootstrap:    SessionConfig{ClaudeBin: "/bin/sleep"},
	})
	if err == nil {
		t.Fatalf("New with corrupt yolo = (pool %v, nil), want error", pool)
	}
	if pool != nil {
		t.Errorf("New returned non-nil pool on corrupt registry: %v", pool)
	}
}

// TestPool_BootstrapSettings_SurviveNewPersistReload (AC #1): warm-started
// settings flow entry → Session → saveLocked → disk unchanged (the in-memory
// round-trip, complementing the registry-serialization round-trip).
func TestPool_BootstrapSettings_SurviveNewPersistReload(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	when := time.Now().UTC()
	bootID := SessionID("550e8400-e29b-41d4-a716-446655440000")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: bootID, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
			Model: "opus", Effort: "high", YOLO: true,
		}},
	}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}

	pool, err := New(Config{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath: regPath,
		Bootstrap:    SessionConfig{ClaudeBin: "/bin/sleep"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Force a persist so settings flow Session → saveLocked → disk (New itself
	// does not re-persist on warm start).
	if err := pool.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	e := pickBootstrap(got)
	if e == nil {
		t.Fatal("no bootstrap entry after reload")
	}
	if e.Model != "opus" || e.Effort != "high" || !e.YOLO {
		t.Errorf("settings not preserved through persist: got Model=%q Effort=%q YOLO=%v, want opus/high/true",
			e.Model, e.Effort, e.YOLO)
	}
}
