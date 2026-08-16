package sessions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// argvRecorderTemplate stands in for the shell script these tests used to spawn.
// The old arrangement launched `sh -c 'printf %s "$@" > argv.txt; ...'` through a
// real terminal supervisor and read the file back, so "what argv did the pool
// compose" was answered by starting a process and doing filesystem IO.
//
// Since #1348 deleted that supervisor there is no child to ask, and the question
// is answered one layer up instead: the recorder factory below captures the argv
// at the moment the pool hands it to a runner. Same assertion, more directly
// made, no process and no polling. The three template tokens are kept because
// the pool appends after them and the tests assert on the appended tail, so the
// shape of the slice has to match what it always was.
var argvRecorderTemplate = []string{"-c", "recorder", "--"}

// argvRecords maps a runner's working directory to the argv it was constructed
// with. Keyed by directory because that is what the assertions already had in
// hand, and every test uses its own t.TempDir(), so two parallel pools cannot
// collide.
var argvRecords = struct {
	mu   sync.Mutex
	byWD map[string][]string
}{byWD: map[string][]string{}}

// recordingRunnerFactory captures the composed argv, then returns the same
// lifecycle double every other pool test uses.
func recordingRunnerFactory(cfg RunnerConfig) (Runner, error) {
	recordArgv(cfg.WorkDir, cfg.ClaudeArgs)
	recordSessionID(cfg.WorkDir, cfg.SessionID)
	return &lifecycleRunner{workDir: cfg.WorkDir, sessionID: cfg.SessionID}, nil
}

// recordArgv stores the argv most recently composed for a working directory.
// Both construction and Restart feed it, because a live settings change
// recomposes argv and restarts rather than rebuilding the runner — the old
// recorder saw that as the respawned child rewriting its argv file.
func recordArgv(workDir string, argv []string) {
	// Construction hands over template + appended; a restart hands over just the
	// recomposed tail. Normalise here so readers always see the appended tokens,
	// which is what the old shell recorder saw via "$@".
	if len(argv) >= len(argvRecorderTemplate) && argv[0] == argvRecorderTemplate[0] {
		argv = argv[len(argvRecorderTemplate):]
	}
	argvRecords.mu.Lock()
	defer argvRecords.mu.Unlock()
	argvRecords.byWD[workDir] = append([]string(nil), argv...)
}

// sessionIDRecords maps a working directory to the session id the pool handed
// the runner at construction.
//
// The pinned bootstrap id used to reach claude as a "--session-id <id>" argv
// flag, emitted at spawn time by a resolver on the terminal supervisor. The
// stream runner takes the id as a field and manages session identity itself —
// it strips session-id flags out of argv on the way past. So the id is still
// pinned and still asserted; it is just no longer a flag, and a test looking for
// one would be checking a mechanism rather than the guarantee.
var sessionIDRecords = struct {
	mu   sync.Mutex
	byWD map[string]string
}{byWD: map[string]string{}}

// ranRecords maps a working directory to the session id of a runner that
// actually reached Run there.
var ranRecords = struct {
	mu   sync.Mutex
	byWD map[string]string
}{byWD: map[string]string{}}

func recordRan(workDir, id string) {
	ranRecords.mu.Lock()
	defer ranRecords.mu.Unlock()
	ranRecords.byWD[workDir] = id
}

func recordSessionID(workDir, id string) {
	sessionIDRecords.mu.Lock()
	defer sessionIDRecords.mu.Unlock()
	sessionIDRecords.byWD[workDir] = id
}

// waitSessionID blocks until a runner has been constructed for dir and returns
// the session id it was given.
func waitSessionID(t *testing.T, dir string) string {
	t.Helper()
	var id string
	if !pollUntil(t, 5*time.Second, func() bool {
		sessionIDRecords.mu.Lock()
		defer sessionIDRecords.mu.Unlock()
		v, ok := sessionIDRecords.byWD[dir]
		if !ok {
			return false
		}
		id = v
		return true
	}) {
		t.Fatalf("no runner was ever constructed with workdir %q", dir)
	}
	return id
}

// helperPoolArgvRecorder builds a Pool whose template child records its own
// appended argv via argvRecorderScript. tplWorkDir is the bootstrap child's cwd
// (and every minted child's cwd when no per-session spawnDir is supplied). An
// optional claudeSessionsDir wires Config.ClaudeSessionsDir so the #1164
// resume-vs-create probe fires against transcripts placed there; omit it (the
// common case) to leave the dir unset, keeping the byte-identical --session-id
// create path (resume=false).
func helperPoolArgvRecorder(t *testing.T, registryPath, tplWorkDir string, claudeSessionsDir ...string) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	sessionsDir := ""
	if len(claudeSessionsDir) > 0 {
		sessionsDir = claudeSessionsDir[0]
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		RunnerFactory: recordingRunnerFactory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     argvRecorderTemplate,
			WorkDir:        tplWorkDir,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger:            logger,
		RegistryPath:      registryPath,
		ClaudeSessionsDir: sessionsDir,
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// waitArgvRaw blocks until the recorder in dir has finished (the `done` sentinel
// exists), then returns the recorded argv tokens verbatim, INCLUDING the #943
// "--settings <path>" pair every interactive spawn now carries. Tests asserting
// only the model/effort/session-id flags call waitArgv (which strips the pair);
// the #943 tests that need to see the pair call waitArgvRaw directly. A nil slice
// means the child appended nothing (byte-identical baseline).
func waitArgvRaw(t *testing.T, dir string) []string {
	t.Helper()
	var argv []string
	if !pollUntil(t, 5*time.Second, func() bool {
		argvRecords.mu.Lock()
		defer argvRecords.mu.Unlock()
		a, ok := argvRecords.byWD[dir]
		if !ok {
			return false
		}
		argv = a
		return true
	}) {
		t.Fatalf("no runner was ever constructed with workdir %q", dir)
	}
	if len(argv) == 0 {
		return nil
	}
	return argv
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
	// #839's pinned id is now a field on the handover, not a trailing argv flag.
	if got, want := waitSessionID(t, tplWorkDir), string(pool.BootstrapID()); got != want {
		t.Errorf("bootstrap session id = %q, want %q", got, want)
	}
}

// TestPool_BootstrapColdStart_SpawnsWithSessionID (#839): a cold-start bootstrap
// (no persisted settings) launches claude with exactly --session-id <bootID> and
// nothing else — the deterministic resume that replaced the old empty/--continue
// baseline (#833 AC #5, superseded here).
func TestPool_BootstrapColdStart_SpawnsWithSessionID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	runPoolInBackground(t, pool)

	// The pinned id reaches the runner as a field rather than an argv flag since
	// #1348; the stream runner strips session-id flags out of argv and owns
	// session identity itself. The guarantee under test — a cold-start bootstrap
	// is pinned to its own persisted id (#839) — is unchanged.
	if got, want := waitSessionID(t, tplWorkDir), string(pool.BootstrapID()); got != want {
		t.Errorf("cold-start bootstrap session id = %q, want %q", got, want)
	}
	if got := waitArgv(t, tplWorkDir); len(got) != 0 {
		t.Errorf("cold-start bootstrap argv = %v, want no appended flags", got)
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
		RunnerFactory: testRunnerFactory,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath:  regPath,
		Bootstrap:     SessionConfig{ClaudeBin: "/bin/sleep"},
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
		RunnerFactory: testRunnerFactory,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath:  regPath,
		Bootstrap:     SessionConfig{ClaudeBin: "/bin/sleep"},
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
