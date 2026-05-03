package sessions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions/rotation"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// helperPool builds a Pool with a benign bootstrap config. None of the pool
// or session tests that use this call Run, so /bin/sleep is never spawned —
// it is only there to satisfy supervisor.New's exec.LookPath check.
//
// withBridge controls whether the bootstrap session has an attached bridge.
// The Attach-related session tests need one; the rest do not care.
func helperPool(t *testing.T, withBridge bool) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin: "/bin/sleep",
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if withBridge {
		cfg.Bootstrap.Bridge = supervisor.NewBridge(cfg.Logger)
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// helperPoolWithSleepArgs builds a Pool whose bootstrap session, when Run, will
// spawn `/bin/sleep 3600` — a long-lived benign child the test can tear down
// via context cancellation. Backoff is shortened so any unexpected restart
// path triggers quickly rather than hiding behind the 500ms default.
//
// Bridge is set so the supervisor uses service-mode I/O (per-supervisor
// pipes) instead of foreground-mode (shared os.Stdin / os.Stdout). Tests
// using foreground mode at scale (e.g. TestPool_Supervise_ConcurrentCalls_RaceClean
// running 33 concurrent supervisors) accumulate stranded io.Copy goroutines on
// os.Stdin's fdMutex and deadlock at shutdown. Production runs as a service
// (Bridge always non-nil); tests should match the production code path.
func helperPoolWithSleepArgs(t *testing.T) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			Bridge:         supervisor.NewBridge(logger),
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger: logger,
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// TestPool_New_BootstrapInstalled covers the constructor path: New must
// install exactly one bootstrap entry, reachable via Default(), with a valid
// canonical UUID id.
func TestPool_New_BootstrapInstalled(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	def := pool.Default()
	if def == nil {
		t.Fatal("pool.Default() returned nil")
	}
	id := string(def.ID())
	if len(id) != 36 {
		t.Errorf("len(default id) = %d, want 36 (id = %q)", len(id), id)
	}
	if !uuidPattern.MatchString(id) {
		t.Errorf("default id %q does not match canonical UUID pattern", id)
	}
}

// TestPool_LookupEmpty_ResolvesToDefault is the consumer-path AC: an empty
// SessionID resolves to the bootstrap entry. Phase 1.1's wire protocol will
// rely on this — Phase 1.0 doesn't call it from production code yet, but the
// behaviour must be in place for Child B.
func TestPool_LookupEmpty_ResolvesToDefault(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	got, err := pool.Lookup("")
	if err != nil {
		t.Fatalf("Lookup(\"\"): %v", err)
	}
	if got != pool.Default() {
		t.Errorf("Lookup(\"\") returned %p, want %p (Default)", got, pool.Default())
	}
}

// TestPool_LookupByID_ReturnsBootstrap verifies that a Lookup with the
// bootstrap session's own id returns it.
func TestPool_LookupByID_ReturnsBootstrap(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	def := pool.Default()
	got, err := pool.Lookup(def.ID())
	if err != nil {
		t.Fatalf("Lookup(%q): %v", def.ID(), err)
	}
	if got != def {
		t.Errorf("Lookup(%q) returned %p, want %p (Default)", def.ID(), got, def)
	}
}

// TestPool_LookupUnknown_ReturnsErrSessionNotFound verifies that a non-empty
// but unknown id returns ErrSessionNotFound, matchable via errors.Is.
func TestPool_LookupUnknown_ReturnsErrSessionNotFound(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	// Fabricate a syntactically-plausible id that is not the bootstrap.
	fake := SessionID("00000000-0000-4000-8000-000000000000")
	got, err := pool.Lookup(fake)
	if got != nil {
		t.Errorf("Lookup(unknown) returned non-nil session %p, want nil", got)
	}
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(unknown) err = %v, want ErrSessionNotFound", err)
	}
}

// helperPoolPersistent builds a Pool with persistence enabled. The bootstrap
// supervisor target is /bin/sleep, never spawned (these tests don't call Run).
func helperPoolPersistent(t *testing.T, registryPath string) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	pool, err := New(Config{
		Bootstrap:    SessionConfig{ClaudeBin: "/bin/sleep"},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath: registryPath,
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// TestPool_New_ColdStartCreatesRegistry: with a non-existent registry path,
// New mints a UUID, writes the file, and the file contains exactly one
// bootstrap entry whose ID matches the in-memory default session.
func TestPool_New_ColdStartCreatesRegistry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pyry", "sessions.json")
	pool := helperPoolPersistent(t, path)

	def := pool.Default()
	if def == nil {
		t.Fatal("Default() = nil")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("registry not written: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("registry mode = %o, want 0600", mode)
	}

	reg, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Fatalf("registry = %+v, want one session", reg)
	}
	if reg.Sessions[0].ID != def.ID() {
		t.Errorf("registry id = %q, want %q", reg.Sessions[0].ID, def.ID())
	}
	if !reg.Sessions[0].Bootstrap {
		t.Errorf("registry entry not marked bootstrap")
	}
	if reg.Sessions[0].CreatedAt.IsZero() {
		t.Errorf("created_at is zero")
	}
}

// TestPool_New_WarmStartReusesUUID: a pre-existing registry's UUID survives
// reload, and the on-disk file is not rewritten (warm start is not a
// state-changing operation).
func TestPool_New_WarmStartReusesUUID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	knownID := SessionID("8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")

	if err := saveRegistryLocked(path, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: knownID, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded: %v", err)
	}
	beforeStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat seeded: %v", err)
	}

	pool := helperPoolPersistent(t, path)
	if got := pool.Default().ID(); got != knownID {
		t.Errorf("Default().ID() = %q, want %q", got, knownID)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("registry rewritten on warm start:\nbefore = %s\nafter  = %s", before, after)
	}
	afterStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed on warm start: before=%v after=%v", beforeStat.ModTime(), afterStat.ModTime())
	}
}

// TestPool_New_IdempotentReload: two cold-then-warm sequences yield the same
// default ID, and the file remains unchanged across reloads.
func TestPool_New_IdempotentReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	first := helperPoolPersistent(t, path)
	id1 := first.Default().ID()
	bytes1, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after first: %v", err)
	}

	second := helperPoolPersistent(t, path)
	id2 := second.Default().ID()
	bytes2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second: %v", err)
	}

	if id1 != id2 {
		t.Errorf("ids drifted: %q vs %q", id1, id2)
	}
	if string(bytes1) != string(bytes2) {
		t.Errorf("file content drifted across reloads")
	}
}

// TestPool_New_PersistenceDisabled_NoFile: empty RegistryPath leaves the
// TempDir untouched.
func TestPool_New_PersistenceDisabled_NoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = helperPool(t, false) // empty RegistryPath via the existing helper

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("TempDir not empty: %v", names)
	}
}

// helperPoolReconciling builds a Pool with both registry and claude sessions
// dir wired. Bootstrap target is /bin/sleep, never spawned.
func helperPoolReconciling(t *testing.T, registryPath, claudeSessionsDir string) (*Pool, error) {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	return New(Config{
		Bootstrap:         SessionConfig{ClaudeBin: "/bin/sleep"},
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath:      registryPath,
		ClaudeSessionsDir: claudeSessionsDir,
	})
}

// TestPool_New_Reconciles_RotatesToNewest: registry seeded with bootstrap A,
// claude dir contains <B>.jsonl newer than <A>.jsonl. Pool's default ID
// becomes B, registry on disk records B, A's JSONL is preserved untouched.
func TestPool_New_Reconciles_RotatesToNewest(t *testing.T) {
	t.Parallel()
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	claudeDir := t.TempDir()

	idA := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	idB := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: idA, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	now := time.Now()
	pathA := filepath.Join(claudeDir, string(idA)+".jsonl")
	pathB := filepath.Join(claudeDir, string(idB)+".jsonl")
	if err := os.WriteFile(pathA, []byte("a-content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pathA, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("b-content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pathB, now, now); err != nil {
		t.Fatal(err)
	}
	bytesABefore, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatal(err)
	}

	pool, err := helperPoolReconciling(t, regPath, claudeDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := pool.Default().ID(); got != idB {
		t.Errorf("Default().ID() = %q, want %q (rotated)", got, idB)
	}
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Fatalf("registry = %+v, want one session", reg)
	}
	if reg.Sessions[0].ID != idB {
		t.Errorf("registry id = %q, want %q", reg.Sessions[0].ID, idB)
	}
	if !reg.Sessions[0].Bootstrap {
		t.Errorf("rotated entry lost bootstrap flag")
	}
	// Pre-rotation JSONL must remain untouched.
	bytesAAfter, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("pre-rotation jsonl was deleted: %v", err)
	}
	if string(bytesABefore) != string(bytesAAfter) {
		t.Errorf("pre-rotation jsonl content changed")
	}
}

// TestPool_New_Reconciles_NoRotationWhenMatch: registry's bootstrap matches
// the only on-disk JSONL. No rotation, registry bytes/mtime unchanged.
func TestPool_New_Reconciles_NoRotationWhenMatch(t *testing.T) {
	t.Parallel()
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	claudeDir := t.TempDir()

	id := SessionID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: id, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(claudeDir, string(id)+".jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	pool, err := helperPoolReconciling(t, regPath, claudeDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := pool.Default().ID(); got != id {
		t.Errorf("Default().ID() = %q, want %q", got, id)
	}
	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("registry rewritten on warm match:\nbefore=%s\nafter =%s", beforeBytes, afterBytes)
	}
	afterStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed: before=%v after=%v", beforeStat.ModTime(), afterStat.ModTime())
	}
}

// TestPool_New_Reconciles_MissingDir_ProceedsWithBootstrap: when the claude
// sessions dir does not exist, New succeeds and the seeded UUID is preserved.
func TestPool_New_Reconciles_MissingDir_ProceedsWithBootstrap(t *testing.T) {
	t.Parallel()
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	claudeDir := filepath.Join(t.TempDir(), "does-not-exist")

	id := SessionID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: id, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}

	pool, err := helperPoolReconciling(t, regPath, claudeDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := pool.Default().ID(); got != id {
		t.Errorf("Default().ID() = %q, want %q", got, id)
	}
	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("registry rewritten when claude dir missing")
	}
}

// TestPool_New_Reconciles_EmptyDir_NoOp: claude dir exists but contains no
// JSONLs. New succeeds, registry unchanged.
func TestPool_New_Reconciles_EmptyDir_NoOp(t *testing.T) {
	t.Parallel()
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	claudeDir := t.TempDir()

	id := SessionID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: id, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}

	pool, err := helperPoolReconciling(t, regPath, claudeDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := pool.Default().ID(); got != id {
		t.Errorf("Default().ID() = %q, want %q", got, id)
	}
	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("registry rewritten on empty claude dir")
	}
}

// TestPool_New_Reconciles_ColdStart_PicksNewestImmediately: cold start (no
// registry) with an existing JSONL on disk. After New, the registry's
// bootstrap entry is the on-disk UUID, not a freshly-minted one.
func TestPool_New_Reconciles_ColdStart_PicksNewestImmediately(t *testing.T) {
	t.Parallel()
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	claudeDir := t.TempDir()

	idX := SessionID("ffffffff-ffff-4fff-8fff-ffffffffffff")
	if err := os.WriteFile(filepath.Join(claudeDir, string(idX)+".jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	pool, err := helperPoolReconciling(t, regPath, claudeDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := pool.Default().ID(); got != idX {
		t.Errorf("Default().ID() = %q, want %q (cold start should adopt on-disk UUID)", got, idX)
	}
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 || reg.Sessions[0].ID != idX {
		t.Fatalf("registry = %+v, want single bootstrap entry with id %q", reg, idX)
	}
	if !reg.Sessions[0].Bootstrap {
		t.Errorf("cold-start rotated entry lost bootstrap flag")
	}
}

// TestPool_RotateID_HappyPath: rotate bootstrap id, assert map keys, bootstrap
// pointer, lastActiveAt advance, and registry on-disk reflect the new id.
func TestPool_RotateID_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	oldID := pool.Default().ID()
	oldLA := pool.Default().lastActiveAt

	newID := SessionID("99999999-9999-4999-8999-999999999999")
	if err := pool.RotateID(oldID, newID); err != nil {
		t.Fatalf("RotateID: %v", err)
	}

	if got := pool.Default().ID(); got != newID {
		t.Errorf("Default().ID() = %q, want %q", got, newID)
	}
	if _, err := pool.Lookup(oldID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(old) err = %v, want ErrSessionNotFound", err)
	}
	if got, err := pool.Lookup(newID); err != nil {
		t.Errorf("Lookup(new) err = %v, want nil", err)
	} else if got != pool.Default() {
		t.Errorf("Lookup(new) returned %p, want Default %p", got, pool.Default())
	}
	if !pool.Default().lastActiveAt.After(oldLA) {
		t.Errorf("lastActiveAt did not advance: old=%v new=%v", oldLA, pool.Default().lastActiveAt)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 || reg.Sessions[0].ID != newID {
		t.Fatalf("registry = %+v, want single entry with id %q", reg, newID)
	}
	if !reg.Sessions[0].Bootstrap {
		t.Errorf("rotated entry lost bootstrap flag on disk")
	}
}

// TestPool_RotateID_UnknownOldID: ErrSessionNotFound, no map mutation, no
// registry rewrite.
func TestPool_RotateID_UnknownOldID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	bootstrapID := pool.Default().ID()
	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}

	unknown := SessionID("00000000-0000-4000-8000-000000000000")
	target := SessionID("11111111-1111-4111-8111-111111111111")
	err = pool.RotateID(unknown, target)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("RotateID(unknown) err = %v, want ErrSessionNotFound", err)
	}

	if got := pool.Default().ID(); got != bootstrapID {
		t.Errorf("bootstrap id mutated after failed RotateID: got %q, want %q", got, bootstrapID)
	}
	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("registry rewritten on failed RotateID")
	}
	afterStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed on failed RotateID")
	}
}

// TestPool_RotateID_Idempotent: RotateID(x, x) is a no-op — same id, no
// registry rewrite, no lastActiveAt bump.
func TestPool_RotateID_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	id := pool.Default().ID()
	beforeLA := pool.Default().lastActiveAt
	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := pool.RotateID(id, id); err != nil {
		t.Errorf("RotateID(x,x) err = %v, want nil", err)
	}
	if got := pool.Default().ID(); got != id {
		t.Errorf("Default().ID() = %q, want %q", got, id)
	}
	if !pool.Default().lastActiveAt.Equal(beforeLA) {
		t.Errorf("lastActiveAt advanced on idempotent rotate: before=%v after=%v",
			beforeLA, pool.Default().lastActiveAt)
	}
	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("registry rewritten on idempotent RotateID")
	}
	afterStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed on idempotent RotateID")
	}
}

// TestPool_Snapshot_BootstrapNoChild: a fresh pool whose bootstrap session
// has not been Run yet snapshots as PID = 0.
func TestPool_Snapshot_BootstrapNoChild(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	snap := pool.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("len(snap) = %d, want 1", len(snap))
	}
	if snap[0].ID != pool.Default().ID() {
		t.Errorf("snap[0].ID = %q, want %q", snap[0].ID, pool.Default().ID())
	}
	if snap[0].PID != 0 {
		t.Errorf("snap[0].PID = %d, want 0 (no child Run)", snap[0].PID)
	}
}

// TestPool_RegisterAllocatedUUID_Consumed: registering then consulting the
// skip set returns true once and false thereafter.
func TestPool_RegisterAllocatedUUID_Consumed(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	id := SessionID("11111111-1111-4111-8111-111111111111")
	pool.RegisterAllocatedUUID(id)
	if !pool.IsAllocated(id) {
		t.Errorf("IsAllocated(%q) = false on first call, want true", id)
	}
	if pool.IsAllocated(id) {
		t.Errorf("IsAllocated(%q) = true on second call, want false (consumed)", id)
	}
}

// TestPool_RegisterAllocatedUUID_Expires: an entry past its TTL must report
// false even on the first IsAllocated call.
func TestPool_RegisterAllocatedUUID_Expires(t *testing.T) {
	prev := allocatedTTL
	allocatedTTL = 50 * time.Millisecond
	defer func() { allocatedTTL = prev }()

	pool := helperPool(t, false)
	id := SessionID("22222222-2222-4222-8222-222222222222")
	pool.RegisterAllocatedUUID(id)
	time.Sleep(120 * time.Millisecond)
	if pool.IsAllocated(id) {
		t.Errorf("IsAllocated(%q) = true after expiry, want false", id)
	}
}

// TestPool_RegisterAllocatedUUID_PrunesOnWrite: an expired entry is pruned
// when a fresh entry is registered.
func TestPool_RegisterAllocatedUUID_PrunesOnWrite(t *testing.T) {
	prev := allocatedTTL
	allocatedTTL = 50 * time.Millisecond
	defer func() { allocatedTTL = prev }()

	pool := helperPool(t, false)
	idA := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1")
	idB := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1")
	pool.RegisterAllocatedUUID(idA)
	time.Sleep(120 * time.Millisecond)
	pool.RegisterAllocatedUUID(idB)

	// A should have been pruned by the second Register's prune sweep.
	pool.mu.RLock()
	_, present := pool.allocated[idA]
	pool.mu.RUnlock()
	if present {
		t.Errorf("expired entry %q not pruned on subsequent register", idA)
	}
	if !pool.IsAllocated(idB) {
		t.Errorf("IsAllocated(%q) = false, want true", idB)
	}
}

// TestPool_Run_NoWatcherWhenDirEmpty: with ClaudeSessionsDir empty, Run
// supervises the bootstrap and exits cleanly on context cancellation, with
// no watcher constructed.
func TestPool_Run_NoWatcherWhenDirEmpty(t *testing.T) {
	t.Parallel()
	pool := helperPoolWithSleepArgs(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s after cancel")
	}
}

// TestPool_Run_StartsWatcher: with ClaudeSessionsDir set and a fake probe,
// writing a UUID-shaped JSONL during the watcher's window triggers RotateID
// before context cancellation.
func TestPool_Run_StartsWatcher(t *testing.T) {
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(t.TempDir(), "sessions.json")

	// Replace the platform probe factory with a fake that returns whatever
	// path was just created in `dir`. The path is captured by closure on
	// each OpenJSONL call by reading the dir.
	prevNewProbe := newProbe
	defer func() { newProbe = prevNewProbe }()
	newProbe = func(_ *slog.Logger) rotation.Probe {
		return &dirProbe{dir: dir}
	}

	pool, err := New(Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath:      regPath,
		ClaudeSessionsDir: dir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	oldID := pool.Default().ID()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()

	// Give the bootstrap supervisor a moment to spawn /bin/sleep so the
	// snapshot has a non-zero PID.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Default().State().ChildPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pool.Default().State().ChildPID == 0 {
		t.Fatal("bootstrap child never started")
	}

	newID := SessionID("8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91")
	if err := os.WriteFile(filepath.Join(dir, string(newID)+".jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Poll the on-disk registry — RotateID's saveLocked is the
	// synchronization point that makes the rotation observable from this
	// goroutine without racing with Session.id.
	deadline = time.Now().Add(2 * time.Second)
	rotated := false
	for time.Now().Before(deadline) {
		reg, err := loadRegistry(regPath)
		if err == nil && reg != nil && len(reg.Sessions) == 1 && reg.Sessions[0].ID == newID {
			rotated = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after cancel")
	}

	if !rotated {
		t.Fatalf("registry id never became %q (watcher did not rotate)", newID)
	}
	if oldID == newID {
		t.Fatal("test setup error: oldID == newID")
	}
}

// dirProbe always reports the most-recently-modified .jsonl in dir as PID's
// open file. Used by TestPool_Run_StartsWatcher to fake the per-PID FD probe
// without actually reading /proc or shelling out to lsof.
type dirProbe struct{ dir string }

func (p *dirProbe) OpenJSONL(int) (string, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return "", nil
	}
	var bestPath string
	var bestMT int64 = -1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".jsonl" {
			continue
		}
		full := filepath.Join(p.dir, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if mt := info.ModTime().UnixNano(); mt > bestMT {
			bestMT = mt
			bestPath = full
		}
	}
	return bestPath, nil
}

// TestPool_BootstrapWarmStartsEvicted: a registry with lifecycle_state
// "evicted" round-trips into a Session with stateEvicted at construction.
// No supervisor activity happens because Run is never called.
func TestPool_BootstrapWarmStartsEvicted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	id := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(path, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: id, CreatedAt: when, LastActiveAt: when,
			Bootstrap: true, LifecycleState: "evicted",
		}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pool := helperPoolPersistent(t, path)
	if got := pool.Default().LifecycleState(); got != stateEvicted {
		t.Errorf("LifecycleState = %v, want stateEvicted", got)
	}
}

// TestPool_ParityWhenIdleDisabled: with IdleTimeout 0, no transition occurs
// even after several timer windows. Regression guard for the AC's parity
// claim ("Phase 0 / 1.0 / 1.2a behaviour is byte-identical").
func TestPool_ParityWhenIdleDisabled(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			IdleTimeout:    0, // disabled
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	// Sleep a meaningful interval and confirm the state never flipped.
	time.Sleep(300 * time.Millisecond)
	if got := sess.LifecycleState(); got != stateActive {
		t.Errorf("LifecycleState after 300ms with idle disabled = %v, want stateActive", got)
	}
}

// TestPool_New_MalformedRegistryIsFatal: a corrupt sessions.json must surface
// a startup error rather than silently wiping the file.
func TestPool_New_MalformedRegistryIsFatal(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed malformed: %v", err)
	}
	pool, err := New(Config{
		Bootstrap:    SessionConfig{ClaudeBin: "/bin/sleep"},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath: path,
	})
	if err == nil {
		t.Fatalf("New(malformed registry) = %p, want error", pool)
	}
	// File must remain on disk for operator inspection.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("malformed registry was deleted: %v", err)
	}
}

// helperDummySession builds a minimal *Session attached to pool that, when
// Run, will spawn `/bin/sleep 60` via its own supervisor. Not added to
// pool.sessions — it's only used to feed Pool.supervise. Backoff is shortened
// so a respawn after ctx cancellation triggers quickly rather than hiding.
func helperDummySession(t *testing.T, pool *Pool) *Session {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Bridge non-nil → service mode (per-supervisor pipes, no os.Stdin
	// contention). Foreground mode at scale is the deadlock path.
	sup, err := supervisor.New(supervisor.Config{
		ClaudeBin:      "/bin/sleep",
		ClaudeArgs:     []string{"60"},
		Bridge:         supervisor.NewBridge(logger),
		Logger:         logger,
		BackoffInitial: 10 * time.Millisecond,
		BackoffMax:     10 * time.Millisecond,
		BackoffReset:   1 * time.Second,
	})
	if err != nil {
		t.Fatalf("supervisor.New: %v", err)
	}
	now := time.Now().UTC()
	sess := &Session{
		id:           id,
		sup:          sup,
		log:          logger,
		createdAt:    now,
		lastActiveAt: now,
		pool:         pool,
		lcState:      stateActive,
		activeCh:     closedChan(),
		evictedCh:    make(chan struct{}),
		activateCh:   make(chan struct{}, 1),
		evictCh:      make(chan struct{}, 1),
	}
	return sess
}

// TestPool_Supervise_BeforeRun_ReturnsErrPoolNotRunning: the helper returns
// the sentinel before Pool.Run has wired the supervisor handle.
func TestPool_Supervise_BeforeRun_ReturnsErrPoolNotRunning(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	err := pool.supervise(pool.Default())
	if !errors.Is(err, ErrPoolNotRunning) {
		t.Errorf("supervise before Run = %v, want ErrPoolNotRunning", err)
	}
}

// TestPool_Supervise_AfterRunReturns_ReturnsErrPoolNotRunning: drive Pool.Run
// to return (cancel its ctx), then call supervise — the deferred clear must
// have zeroed the handle.
func TestPool_Supervise_AfterRunReturns_ReturnsErrPoolNotRunning(t *testing.T) {
	t.Parallel()
	pool := helperPoolWithSleepArgs(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Default().State().ChildPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pool.Default().State().ChildPID == 0 {
		cancel()
		<-done
		t.Fatal("bootstrap child never started")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s after cancel")
	}

	err := pool.supervise(pool.Default())
	if !errors.Is(err, ErrPoolNotRunning) {
		t.Errorf("supervise after Run returned = %v, want ErrPoolNotRunning", err)
	}
}

// TestPool_Supervise_ConcurrentCalls_RaceClean: while Pool.Run is active,
// fire N goroutines each calling supervise on its own dummy session. All
// return nil; `go test -race` is the assertion.
func TestPool_Supervise_ConcurrentCalls_RaceClean(t *testing.T) {
	t.Parallel()
	pool := helperPoolWithSleepArgs(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Default().State().ChildPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pool.Default().State().ChildPID == 0 {
		cancel()
		<-done
		t.Fatal("bootstrap child never started")
	}

	const n = 32
	dummies := make([]*Session, n)
	for i := range dummies {
		dummies[i] = helperDummySession(t, pool)
	}

	errs := make(chan error, n)
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < n; i++ {
		sess := dummies[i]
		go func() {
			start.Wait()
			errs <- pool.supervise(sess)
		}()
	}
	start.Done()
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("supervise #%d returned %v, want nil", i, err)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit within 5s after cancel")
	}
}
