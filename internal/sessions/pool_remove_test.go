package sessions

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestPool_Remove_HappyPath: Create a session, wait for its child to spawn,
// drop a stub JSONL on disk, then Remove the session. The child must exit,
// the registry entry must be gone in-memory and on disk, and the JSONL must
// be byte-identical (Remove does not touch on-disk JSONL).
func TestPool_Remove_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	claudeDir := t.TempDir()
	pool := helperPoolCreate(t, regPath, 0)
	pool.claudeSessionsDir = claudeDir
	ctx, _ := runPoolInBackground(t, pool)

	if !pollUntil(t, 5*time.Second, func() bool {
		return pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never spawned")
	}

	id, err := pool.Create(ctx, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(new): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return sess.State().ChildPID > 0 && sess.LifecycleState() == stateActive
	}) {
		t.Fatalf("new session never reached active+spawned; state=%+v lc=%v",
			sess.State(), sess.LifecycleState())
	}
	pid := sess.State().ChildPID

	// Drop a stub JSONL on disk; Remove must NOT touch it.
	jsonlPath := filepath.Join(claudeDir, string(id)+".jsonl")
	jsonlContent := []byte("stub jsonl content\n")
	if err := os.WriteFile(jsonlPath, jsonlContent, 0o600); err != nil {
		t.Fatalf("write stub jsonl: %v", err)
	}

	if err := pool.Remove(ctx, id, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := pool.Lookup(id); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(removed) err = %v, want ErrSessionNotFound", err)
	}
	for _, info := range pool.List() {
		if info.ID == id {
			t.Errorf("List still contains removed id %q", id)
		}
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Fatalf("registry sessions = %+v, want 1 (bootstrap only)", reg)
	}
	if reg.Sessions[0].ID == id {
		t.Errorf("registry on disk still has removed id %q", id)
	}

	if got := sess.LifecycleState(); got != stateEvicted {
		t.Errorf("removed session LifecycleState = %v, want stateEvicted", got)
	}
	if got := sess.State().ChildPID; got != 0 {
		t.Errorf("removed session ChildPID = %d, want 0", got)
	}
	// Belt-and-suspenders: zero-signal probe to confirm the child PID is
	// no longer alive (the supervisor has set ChildPID=0, but the kernel
	// reaping is what we actually care about).
	if pid > 0 {
		if !pollUntil(t, 2*time.Second, func() bool {
			return !processAlive(pid)
		}) {
			t.Errorf("child pid %d still alive after Remove", pid)
		}
	}

	gotJSONL, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl after Remove: %v", err)
	}
	if !bytes.Equal(gotJSONL, jsonlContent) {
		t.Errorf("jsonl mutated by Remove:\nbefore=%q\nafter =%q", jsonlContent, gotJSONL)
	}
}

// TestPool_Remove_Bootstrap_Rejected: Removing the bootstrap returns
// ErrCannotRemoveBootstrap. Registry, in-memory list, and on-disk JSONL
// must be byte-identical.
func TestPool_Remove_Bootstrap_Rejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	claudeDir := t.TempDir()
	pool := helperPoolPersistent(t, regPath)
	pool.claudeSessionsDir = claudeDir

	bootstrapID := pool.Default().ID()
	jsonlPath := filepath.Join(claudeDir, string(bootstrapID)+".jsonl")
	jsonlContent := []byte("bootstrap jsonl\n")
	if err := os.WriteFile(jsonlPath, jsonlContent, 0o600); err != nil {
		t.Fatalf("write bootstrap jsonl: %v", err)
	}

	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeList := pool.List()

	err = pool.Remove(context.Background(), bootstrapID, RemoveOptions{})
	if !errors.Is(err, ErrCannotRemoveBootstrap) {
		t.Errorf("Remove(bootstrap) err = %v, want ErrCannotRemoveBootstrap", err)
	}

	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Errorf("registry bytes changed on rejected Remove:\nbefore=%s\nafter =%s", beforeBytes, afterBytes)
	}
	afterStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed on rejected Remove: before=%v after=%v",
			beforeStat.ModTime(), afterStat.ModTime())
	}

	afterList := pool.List()
	if !reflect.DeepEqual(beforeList, afterList) {
		t.Errorf("List output changed on rejected Remove:\nbefore=%+v\nafter =%+v", beforeList, afterList)
	}

	gotJSONL, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if !bytes.Equal(gotJSONL, jsonlContent) {
		t.Errorf("jsonl mutated on rejected Remove")
	}
}

// TestPool_Remove_UnknownID: an unknown UUID returns ErrSessionNotFound;
// in-memory list and on-disk file are byte-identical to before.
func TestPool_Remove_UnknownID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	claudeDir := t.TempDir()
	pool := helperPoolPersistent(t, regPath)
	pool.claudeSessionsDir = claudeDir

	unknown := SessionID("00000000-0000-4000-8000-000000000000")
	jsonlPath := filepath.Join(claudeDir, string(unknown)+".jsonl")
	jsonlContent := []byte("orphan jsonl\n")
	if err := os.WriteFile(jsonlPath, jsonlContent, 0o600); err != nil {
		t.Fatalf("write orphan jsonl: %v", err)
	}

	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeList := pool.List()

	err = pool.Remove(context.Background(), unknown, RemoveOptions{})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Remove(unknown) err = %v, want ErrSessionNotFound", err)
	}

	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Errorf("registry bytes changed on unknown-id Remove")
	}
	afterStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed on unknown-id Remove")
	}

	afterList := pool.List()
	if !reflect.DeepEqual(beforeList, afterList) {
		t.Errorf("List output changed on unknown-id Remove:\nbefore=%+v\nafter =%+v", beforeList, afterList)
	}

	gotJSONL, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if !bytes.Equal(gotJSONL, jsonlContent) {
		t.Errorf("jsonl mutated on unknown-id Remove")
	}
}

// TestPool_Remove_RaceWithList: concurrent Create+Remove writers and List
// readers must be -race clean. The bootstrap is never the Remove target, so
// the test only exercises the non-bootstrap path. The assertion is "go test
// -race is silent and no errors logged."
func TestPool_Remove_RaceWithList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	if !pollUntil(t, 5*time.Second, func() bool {
		return pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never spawned")
	}

	const writers = 4
	const readers = 4
	const iters = 5

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				id, err := pool.Create(ctx, "")
				if err != nil {
					t.Errorf("Create: %v", err)
					return
				}
				if err := pool.Remove(ctx, id, RemoveOptions{}); err != nil {
					t.Errorf("Remove: %v", err)
					return
				}
			}
		}()
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters*4; j++ {
				for _, info := range pool.List() {
					_ = info.ID
				}
			}
		}()
	}
	wg.Wait()
}

// TestPool_Remove_TerminatesUncooperativeChild: the child traps SIGTERM/SIGINT
// to no-ops via /bin/sh, then sleeps 24h. Pool.Remove relies on
// exec.CommandContext's SIGKILL on ctx-cancel — SIGKILL is not catchable, so
// the child must die regardless of the trap. No real-time time.Sleep in the
// test body — the assertion is bounded by the budget ctx.
func TestPool_Remove_TerminatesUncooperativeChild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)

	// Replace the per-session template's args so Pool.Create spawns a child
	// that ignores cooperative signals. /bin/sh -c 'trap "" TERM INT HUP;
	// exec sleep 86400' wraps an uncatchable-by-design sleeper. The
	// trailing -- absorbs the --session-id <uuid> Pool.Create appends.
	pool.sessionTpl.ClaudeArgs = []string{"-c", `trap "" TERM INT HUP; exec sleep 86400`, "--"}

	ctx, _ := runPoolInBackground(t, pool)

	if !pollUntil(t, 5*time.Second, func() bool {
		return pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never spawned")
	}

	id, err := pool.Create(ctx, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(new): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return sess.State().ChildPID > 0
	}) {
		t.Fatal("uncooperative child never spawned")
	}
	pid := sess.State().ChildPID

	rmCtx, rmCancel := context.WithTimeout(ctx, 10*time.Second)
	defer rmCancel()

	start := time.Now()
	if err := pool.Remove(rmCtx, id, RemoveOptions{}); err != nil {
		t.Fatalf("Remove(uncooperative): %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Errorf("Remove blocked for the full ctx budget (%v); SIGKILL path may be broken", elapsed)
	}

	if _, err := pool.Lookup(id); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(removed) err = %v, want ErrSessionNotFound", err)
	}

	if !pollUntil(t, 2*time.Second, func() bool {
		return !processAlive(pid)
	}) {
		t.Errorf("child pid %d still alive after Remove on uncooperative child", pid)
	}
}

// removeArchiveTestSetup brings up a pool with persistence and a claude
// sessions dir, spawns the bootstrap, creates a non-bootstrap session, and
// returns the bits the archive/purge tests need: ctx, the pool, the new
// session id, the live JSONL path, and the data-dir root pyry owns.
func removeArchiveTestSetup(t *testing.T) (ctx context.Context, pool *Pool, id SessionID, jsonlPath, dataDir string) {
	t.Helper()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	claudeDir := t.TempDir()
	pool = helperPoolCreate(t, regPath, 0)
	pool.claudeSessionsDir = claudeDir
	ctx, _ = runPoolInBackground(t, pool)

	if !pollUntil(t, 5*time.Second, func() bool {
		return pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never spawned")
	}

	var err error
	id, err = pool.Create(ctx, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(new): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return sess.State().ChildPID > 0 && sess.LifecycleState() == stateActive
	}) {
		t.Fatalf("new session never reached active+spawned; state=%+v lc=%v",
			sess.State(), sess.LifecycleState())
	}

	jsonlPath = filepath.Join(claudeDir, string(id)+".jsonl")
	dataDir = dir
	return ctx, pool, id, jsonlPath, dataDir
}

// TestPool_Remove_Archive_HappyPath: JSONLArchive moves the live JSONL into
// <dataDir>/archived-sessions/<uuid>.jsonl and the bytes match (proving
// it's the same file).
func TestPool_Remove_Archive_HappyPath(t *testing.T) {
	t.Parallel()
	ctx, pool, id, jsonlPath, dataDir := removeArchiveTestSetup(t)

	marker := []byte("marker-" + string(id))
	if err := os.WriteFile(jsonlPath, marker, 0o600); err != nil {
		t.Fatalf("write live jsonl: %v", err)
	}

	if err := pool.Remove(ctx, id, RemoveOptions{JSONL: JSONLArchive}); err != nil {
		t.Fatalf("Remove(JSONLArchive): %v", err)
	}

	if _, err := os.Stat(jsonlPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("live jsonl still present after archive: stat err = %v", err)
	}

	archivePath := filepath.Join(dataDir, "archived-sessions", string(id)+".jsonl")
	got, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archived jsonl: %v", err)
	}
	if !bytes.Equal(got, marker) {
		t.Errorf("archived bytes != live bytes: got=%q want=%q", got, marker)
	}

	reg, err := loadRegistry(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Fatalf("registry sessions = %+v, want 1 (bootstrap only)", reg)
	}
	if reg.Sessions[0].ID == id {
		t.Errorf("registry on disk still has removed id %q", id)
	}
}

// TestPool_Remove_Archive_AutoCreatesSubdir: with no archived-sessions dir
// pre-existing, archive must create it.
func TestPool_Remove_Archive_AutoCreatesSubdir(t *testing.T) {
	t.Parallel()
	ctx, pool, id, jsonlPath, dataDir := removeArchiveTestSetup(t)

	archiveDir := filepath.Join(dataDir, "archived-sessions")
	if err := os.RemoveAll(archiveDir); err != nil {
		t.Fatalf("pre-clean archive dir: %v", err)
	}

	marker := []byte("marker-" + string(id))
	if err := os.WriteFile(jsonlPath, marker, 0o600); err != nil {
		t.Fatalf("write live jsonl: %v", err)
	}

	if err := pool.Remove(ctx, id, RemoveOptions{JSONL: JSONLArchive}); err != nil {
		t.Fatalf("Remove(JSONLArchive): %v", err)
	}

	info, err := os.Stat(archiveDir)
	if err != nil {
		t.Fatalf("stat archive dir: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("archive path is not a directory: mode=%v", info.Mode())
	}

	got, err := os.ReadFile(filepath.Join(archiveDir, string(id)+".jsonl"))
	if err != nil {
		t.Fatalf("read archived jsonl: %v", err)
	}
	if !bytes.Equal(got, marker) {
		t.Errorf("archived bytes != marker: got=%q want=%q", got, marker)
	}
}

// TestPool_Remove_Archive_DestExists: when the archive destination is
// already present, archive must error (wrapping fs.ErrExist), the live
// JSONL must be untouched, the archive file must be untouched — and per
// the AC, the registry entry is still removed (disposition runs after
// persist).
func TestPool_Remove_Archive_DestExists(t *testing.T) {
	t.Parallel()
	ctx, pool, id, jsonlPath, dataDir := removeArchiveTestSetup(t)

	archiveDir := filepath.Join(dataDir, "archived-sessions")
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	archivePath := filepath.Join(archiveDir, string(id)+".jsonl")
	if err := os.WriteFile(archivePath, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed dst: %v", err)
	}
	if err := os.WriteFile(jsonlPath, []byte("new"), 0o600); err != nil {
		t.Fatalf("write live jsonl: %v", err)
	}

	err := pool.Remove(ctx, id, RemoveOptions{JSONL: JSONLArchive})
	if err == nil {
		t.Fatalf("Remove(JSONLArchive) want error, got nil")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("Remove err = %v, want errors.Is(err, fs.ErrExist)", err)
	}

	gotLive, readErr := os.ReadFile(jsonlPath)
	if readErr != nil {
		t.Fatalf("read live jsonl after dest-exists: %v", readErr)
	}
	if !bytes.Equal(gotLive, []byte("new")) {
		t.Errorf("live jsonl mutated by failed archive: got=%q", gotLive)
	}
	gotArchive, readErr := os.ReadFile(archivePath)
	if readErr != nil {
		t.Fatalf("read archive jsonl after dest-exists: %v", readErr)
	}
	if !bytes.Equal(gotArchive, []byte("old")) {
		t.Errorf("archive jsonl mutated by failed archive: got=%q", gotArchive)
	}

	if _, err := pool.Lookup(id); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(removed) err = %v, want ErrSessionNotFound (registry must be committed before disposition)", err)
	}
	reg, regErr := loadRegistry(filepath.Join(dataDir, "sessions.json"))
	if regErr != nil {
		t.Fatalf("loadRegistry: %v", regErr)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Fatalf("registry sessions = %+v, want 1 (bootstrap only)", reg)
	}
	if reg.Sessions[0].ID == id {
		t.Errorf("registry on disk still has removed id %q", id)
	}
}

// TestPool_Remove_Purge_HappyPath: JSONLPurge deletes the live JSONL and
// does NOT create the archived-sessions subdir.
func TestPool_Remove_Purge_HappyPath(t *testing.T) {
	t.Parallel()
	ctx, pool, id, jsonlPath, dataDir := removeArchiveTestSetup(t)

	marker := []byte("marker-" + string(id))
	if err := os.WriteFile(jsonlPath, marker, 0o600); err != nil {
		t.Fatalf("write live jsonl: %v", err)
	}

	if err := pool.Remove(ctx, id, RemoveOptions{JSONL: JSONLPurge}); err != nil {
		t.Fatalf("Remove(JSONLPurge): %v", err)
	}

	if _, err := os.Stat(jsonlPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("live jsonl still present after purge: stat err = %v", err)
	}
	archiveDir := filepath.Join(dataDir, "archived-sessions")
	if _, err := os.Stat(archiveDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("purge unexpectedly created archive dir: stat err = %v", err)
	}

	reg, err := loadRegistry(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Fatalf("registry sessions = %+v, want 1 (bootstrap only)", reg)
	}
}

// TestPool_Remove_Purge_AbsentNoop: with no JSONL on disk, JSONLPurge
// returns nil (per AC: "if the JSONL does not exist on disk, treat as
// success").
func TestPool_Remove_Purge_AbsentNoop(t *testing.T) {
	t.Parallel()
	ctx, pool, id, jsonlPath, dataDir := removeArchiveTestSetup(t)

	if _, err := os.Stat(jsonlPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("live jsonl unexpectedly present at start of purge-absent: stat err = %v", err)
	}

	if err := pool.Remove(ctx, id, RemoveOptions{JSONL: JSONLPurge}); err != nil {
		t.Fatalf("Remove(JSONLPurge, absent): %v", err)
	}

	reg, err := loadRegistry(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Fatalf("registry sessions = %+v, want 1 (bootstrap only)", reg)
	}
}

// processAlive reports whether pid is currently running. Uses the POSIX
// zero-signal probe (ESRCH if the process has been reaped). Mirrors
// internal/e2e/harness_test.go's helper of the same name; duplicated here
// to keep the sessions package's test surface self-contained.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
