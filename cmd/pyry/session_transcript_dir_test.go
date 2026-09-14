package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// transcriptDirRunner is stubRunner plus the ONE concrete method
// sessionTranscriptDir asserts for. It answers the WorkDir the pool built it
// with, standing in for production's streamClaudeSessionsDir(cfg.WorkDir)
// without needing a resolvable $HOME: what the resolver is asked to prove is
// that the answer comes from THIS session's runner, not what the derivation
// inside it computes — that half is streamsup_runner_test.go's.
//
// stubRunner itself deliberately does not implement the method and therefore
// stays the ready-made not-implemented fixture, which the runner-lacks-the-method
// case below reaches via newRouterTestPool.
type transcriptDirRunner struct {
	stubRunner
	dir string
}

func (r transcriptDirRunner) ClaudeSessionsDir() string { return r.dir }

// newTranscriptDirTestPool builds a real *sessions.Pool whose every session's
// runner answers that session's own spawn workdir, mirroring
// newModelWindowsTestPool: a RegistryPath under a fresh temp dir gives
// Pool.Create a resolvable data dir, and a cold start there mints a bootstrap
// without spawning claude.
func newTranscriptDirTestPool(t *testing.T, bootstrapWorkdir string) *sessions.Pool {
	t.Helper()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: bootstrapWorkdir},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return transcriptDirRunner{dir: cfg.WorkDir}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// TestSessionTranscriptDir_AnswersTheSessionsOwnFolder is the happy path: the
// folder reported for a session id is the one that session's own runner holds.
func TestSessionTranscriptDir_AnswersTheSessionsOwnFolder(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	pool := newTranscriptDirTestPool(t, workdir)

	if got := sessionTranscriptDir(pool, "/daemon/projects")(string(pool.BootstrapID())); got != workdir {
		t.Errorf("folder = %q, want %q", got, workdir)
	}
}

// TestSessionTranscriptDir_Isolation is #2423's whole point at the resolver's
// seam: two pool sessions built in two DIFFERENT working directories, neither
// of them the daemon's, each answering its own folder.
//
// Both sessions carry distinguishable directories rather than one carrying a
// directory and one nothing, which is what makes a crossed lookup visible as a
// wrong folder rather than as an empty answer an unbuilt session would also
// produce. The daemon's own directory is passed to the builder and must appear
// in NEITHER answer — a resolver that fell back to it on any path would report
// the pre-#2423 reading while looking wired.
//
// Not parallel: t.Setenv confines anything the pool's create path resolves out
// of HOME, and t.Setenv forbids t.Parallel — TestSessionModelWindows_Isolation's
// arrangement, inherited for its reason.
func TestSessionTranscriptDir_Isolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspaceA := filepath.Join(home, "workspace-a")
	workspaceB := filepath.Join(home, "workspace-b")
	for _, dir := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	pool := newTranscriptDirTestPool(t, filepath.Join(home, "daemon-workdir"))
	// Pool.CreateIn schedules the new session on the run group, so the pool has
	// to be running or supervise returns ErrPoolNotRunning.
	ctx := runPoolReady(t, pool)
	sessA, err := pool.CreateIn(ctx, "conv-a", workspaceA)
	if err != nil {
		t.Fatalf("pool.CreateIn(workspace-a): %v", err)
	}
	sessB, err := pool.CreateIn(ctx, "conv-b", workspaceB)
	if err != nil {
		t.Fatalf("pool.CreateIn(workspace-b): %v", err)
	}

	dirs := sessionTranscriptDir(pool, filepath.Join(home, "daemon-projects"))
	if got := dirs(string(sessA)); got != workspaceA {
		t.Errorf("conv-a folder = %q, want %q", got, workspaceA)
	}
	if got := dirs(string(sessB)); got != workspaceB {
		t.Errorf("conv-b folder = %q, want %q", got, workspaceB)
	}
}

// TestSessionTranscriptDir_RefusesWithEmpty covers every refusal, all of which
// must answer "" — the single spelling snapshotUsageFor reads as "do not stat",
// which is what keeps a relative <id>.jsonl from joining against the daemon's
// process directory.
func TestSessionTranscriptDir_RefusesWithEmpty(t *testing.T) {
	t.Parallel()

	t.Run("unknown id", func(t *testing.T) {
		t.Parallel()
		pool := newTranscriptDirTestPool(t, t.TempDir())
		if got := sessionTranscriptDir(pool, "/daemon/projects")("session-not-in-pool"); got != "" {
			t.Errorf("folder = %q, want \"\"", got)
		}
	})

	t.Run("runner without the method", func(t *testing.T) {
		t.Parallel()
		pool := newRouterTestPool(t)
		if got := sessionTranscriptDir(pool, "/daemon/projects")(string(pool.BootstrapID())); got != "" {
			t.Errorf("folder = %q, want \"\" — stubRunner has no ClaudeSessionsDir method", got)
		}
	})

	t.Run("runner that could not derive a folder", func(t *testing.T) {
		t.Parallel()
		// A runner whose own derivation degraded to "" — an unresolvable workdir
		// or no $HOME inside streamClaudeSessionsDir. The resolver forwards it
		// verbatim rather than substituting the daemon's directory.
		pool := newTranscriptDirTestPool(t, "")
		if got := sessionTranscriptDir(pool, "/daemon/projects")(string(pool.BootstrapID())); got != "" {
			t.Errorf("folder = %q, want \"\"", got)
		}
	})
}

// TestSessionTranscriptDir_NoDaemonDirectoryBuildsNoResolver is AC 5's build-time
// half: a daemon that cannot name its own sessions directory gets NO resolver, so
// snapshotUsageFor collapses to the nil seam and the handlers report zeros rather
// than a default window against a zero used count.
//
// Asserting on the returned value rather than on a call is the point — the
// nil-ness is structural, decided before any closure exists, which is
// bootstrapSnapshotUsage's and runConfigFor's own rule. A builder that instead
// returned a closure answering "" would make runConfigFor's usage half non-nil
// and change the wire shape.
func TestSessionTranscriptDir_NoDaemonDirectoryBuildsNoResolver(t *testing.T) {
	t.Parallel()

	if got := sessionTranscriptDir(newTranscriptDirTestPool(t, t.TempDir()), ""); got != nil {
		t.Error("sessionTranscriptDir(pool, \"\") returned a resolver, want nil")
	}
}
