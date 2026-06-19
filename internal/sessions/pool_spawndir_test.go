package sessions

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// helperPoolSpawnDir builds a Pool whose template (and therefore every
// child it spawns) runs a cwd-recorder: each child writes pwd to a marker
// file named after the --session-id uuid buildSession appends, then execs a
// long sleep so the child stays alive. The marker's *location* is the proof
// of the spawn directory — no production accessor for the child's cwd is
// added.
//
// Argument plumbing: buildSession appends "--session-id <uuid>" to the
// template ClaudeArgs, so inside `sh -c SCRIPT -- --session-id <uuid>` the
// positional params are $0="--", $1="--session-id", $2=<uuid>. The recorder
// writes cwd-$2.txt → cwd-<uuid>.txt into its own cwd. The bootstrap child is
// constructed without the append (pool.go New), so its $2 is empty and it
// writes cwd-.txt — distinct from any session's per-uuid marker, even when
// both share a directory.
//
// tplWorkDir is the shared template workdir (supervisor.Config.WorkDir when no
// per-session spawnDir is supplied). It must be writable so the recorder can
// create its marker.
func helperPoolSpawnDir(t *testing.T, registryPath, tplWorkDir string) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     []string{"-c", `pwd > "cwd-$2.txt"; exec sleep 3600`, "--"},
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

// markerExists reports whether the cwd-<id>.txt recorder marker exists at dir.
func markerExists(dir string, id SessionID) bool {
	_, err := os.Stat(filepath.Join(dir, "cwd-"+string(id)+".txt"))
	return err == nil
}

// TestPool_CreateIn_SpawnsInGivenDir: AC#1 — a session created with an
// explicit per-session spawn workdir runs its child in that directory, not
// the shared template workdir.
func TestPool_CreateIn_SpawnsInGivenDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolSpawnDir(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	id, err := pool.CreateIn(ctx, "", spawnDir)
	if err != nil {
		t.Fatalf("CreateIn: %v", err)
	}

	if !pollUntil(t, 5*time.Second, func() bool {
		return markerExists(spawnDir, id)
	}) {
		t.Fatalf("child did not record cwd in spawnDir %q", spawnDir)
	}
	if markerExists(tplWorkDir, id) {
		t.Errorf("child wrote marker in template workdir %q; want spawnDir %q", tplWorkDir, spawnDir)
	}
}

// TestPool_Create_SpawnsInTemplateWorkDir: AC#2 + AC#4 default leg — a session
// created via plain Create (no spawnDir) runs in the shared template workdir,
// exactly as today.
func TestPool_Create_SpawnsInTemplateWorkDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolSpawnDir(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	id, err := pool.Create(ctx, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The per-uuid marker distinguishes this session's recorder file from the
	// bootstrap's cwd-.txt in the same template workdir.
	if !pollUntil(t, 5*time.Second, func() bool {
		return markerExists(tplWorkDir, id)
	}) {
		t.Fatalf("child did not record cwd in template workdir %q", tplWorkDir)
	}
}

// TestPool_GetOrCreateIn_SpawnsInGivenDir: the GetOrCreate seam threads the
// per-session spawn workdir identically to CreateIn on the create path.
func TestPool_GetOrCreateIn_SpawnsInGivenDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolSpawnDir(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	if _, err := pool.GetOrCreateIn(ctx, target, "", spawnDir); err != nil {
		t.Fatalf("GetOrCreateIn: %v", err)
	}

	if !pollUntil(t, 5*time.Second, func() bool {
		return markerExists(spawnDir, target)
	}) {
		t.Fatalf("child did not record cwd in spawnDir %q", spawnDir)
	}
	if markerExists(tplWorkDir, target) {
		t.Errorf("child wrote marker in template workdir %q; want spawnDir %q", tplWorkDir, spawnDir)
	}
}

// TestPool_GetOrCreateIn_TakePath_IgnoresSpawnDir: on the take path (session
// already registered) spawnDir is ignored — the existing session keeps its own
// workdir and no new child is spawned, mirroring the documented label-drop.
func TestPool_GetOrCreateIn_TakePath_IgnoresSpawnDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	firstDir := t.TempDir()
	secondDir := t.TempDir()

	pool := helperPoolSpawnDir(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	// Create path: session spawns in firstDir.
	if _, err := pool.GetOrCreateIn(ctx, target, "", firstDir); err != nil {
		t.Fatalf("GetOrCreateIn (create): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return markerExists(firstDir, target)
	}) {
		t.Fatalf("child did not record cwd in firstDir %q", firstDir)
	}

	// Take path: same id, different spawnDir — must be ignored (no respawn).
	got, err := pool.GetOrCreateIn(ctx, target, "", secondDir)
	if err != nil {
		t.Fatalf("GetOrCreateIn (take): %v", err)
	}
	if got != target {
		t.Errorf("take-path id = %q, want %q", got, target)
	}
	// A spawn would land within the sub-second spawn path; 1s with no marker
	// confirms the take path spawned nothing in secondDir.
	if pollUntil(t, 1*time.Second, func() bool {
		return markerExists(secondDir, target)
	}) {
		t.Errorf("take path spawned a child in secondDir %q; want spawnDir ignored", secondDir)
	}
}
