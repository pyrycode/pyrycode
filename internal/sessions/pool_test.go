package sessions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
func helperPoolWithSleepArgs(t *testing.T) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
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
