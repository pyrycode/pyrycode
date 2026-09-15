package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// workDirRecorder captures the RunnerConfig.WorkDir the pool hands each runner,
// keyed by session id. The revive path's whole AC#3 claim is about which
// directory reaches that field, and reading it off the handover is both the
// earliest and the only observation point that does not require spawning claude.
type workDirRecorder struct {
	mu   sync.Mutex
	byID map[string]string
}

func (w *workDirRecorder) record(id, dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.byID == nil {
		w.byID = map[string]string{}
	}
	w.byID[id] = dir
}

func (w *workDirRecorder) get(id string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	dir, ok := w.byID[id]
	return dir, ok
}

// newRecordingPool builds a pool whose runners never spawn anything but record
// the workdir they were constructed with. registryPath may be shared across two
// pools to model a daemon restart.
func newRecordingPool(t *testing.T, registryPath, tplWorkDir string, rec *workDirRecorder) *sessions.Pool {
	t.Helper()
	pool, err := sessions.New(sessions.Config{
		Bootstrap: sessions.SessionConfig{
			ClaudeBin: os.Args[0],
			WorkDir:   tplWorkDir,
		},
		RegistryPath: registryPath,
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			rec.record(cfg.SessionID, cfg.WorkDir)
			return stubRunner{}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// runPoolReady starts pool.Run and blocks until Pool.Ready closes — i.e. until
// runGroup is wired, which is what Revive needs to avoid ErrPoolNotRunning. The
// in-package runPoolInBackground polls pool.mu directly and is unreachable from
// cmd/pyry, so readiness is read off the exported signal instead.
func runPoolReady(t *testing.T, pool *sessions.Pool) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("pool.Run did not exit within 15s after cancel")
		}
	})
	select {
	case <-pool.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("pool.Run did not become ready within 10s")
	}
	return ctx
}

// registryHasSession reports whether sessions.json at path currently lists id.
// It reads the raw file rather than any pool accessor: AC#2 is a claim about
// what survives on DISK across a persist, which is precisely what the in-memory
// map cannot answer.
func registryHasSession(t *testing.T, path string, id sessions.SessionID) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	type entry struct {
		ID string `json:"id"`
	}
	var file struct {
		Sessions []entry `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %q: %v (raw = %s)", path, err, raw)
	}
	for _, e := range file.Sessions {
		if e.ID == string(id) {
			return true
		}
	}
	return false
}

// TestSessionRouter_Resolve_RevivesAfterDaemonRestart is the ticket's headline
// scenario (AC#1, AC#2, AC#3): a conversation bound to a MINTED session, a
// daemon restart that drops that session from the pool, and a send_message
// resolution that must now succeed instead of rejecting with a retryable
// server.binary_offline.
//
// The two pools share one sessions.json, which is what makes the restart real
// rather than simulated: pool B's warm start is driven by the file pool A wrote.
func TestSessionRouter_Resolve_RevivesAfterDaemonRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "projects", "app")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	projReal, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatalf("EvalSymlinks(proj): %v", err)
	}
	// trustMark returns its input here, so the recorded spawn workdir is the
	// confined realpath itself and the AC#3 comparison is against a real path.
	installRecordingTrustMark(t, projReal, nil)

	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	tplWorkDir := t.TempDir()

	// --- daemon lifetime A: mint a per-conversation session in proj ---
	recA := &workDirRecorder{}
	poolA := newRecordingPool(t, regPath, tplWorkDir, recA)
	ctxA := runPoolReady(t, poolA)
	id, err := poolA.CreateIn(ctxA, "conv-1", projReal)
	if err != nil {
		t.Fatalf("CreateIn: %v", err)
	}
	if !registryHasSession(t, regPath, id) {
		t.Fatalf("precondition: minted session %q not persisted by pool A", id)
	}

	// --- daemon lifetime B: warm start from the same registry ---
	recB := &workDirRecorder{}
	poolB := newRecordingPool(t, regPath, tplWorkDir, recB)
	runPoolReady(t, poolB)

	// The bug, stated as a precondition. Without this rung the test could pass
	// vacuously against a pool that never dropped the entry in the first place.
	if _, err := poolB.Lookup(id); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Fatalf("precondition: poolB.Lookup(%q) = %v, want ErrSessionNotFound (warm start must drop minted entries)", id, err)
	}
	// Dropped from the pool, but no longer dropped from disk: a state-changing
	// persist BEFORE the conversation is touched now rewrites sessions.json with
	// the minted entry still in it, because a warm start keeps what it did not
	// materialise and saveLocked writes those back beside the live sessions
	// (#2448). This rung inverted there — it previously asserted the erasure,
	// which is how a restart lost the session's model and effort. The AC#2
	// assertion below no longer rests on that erasure but on the handoff it
	// precedes: materialise retires the dormant entry as it takes the id live,
	// so a post-revive persist still carrying the id proves the live session
	// wrote it rather than the entry merely never having left.
	if err := poolB.Rename(poolB.BootstrapID(), "pre-touch"); err != nil {
		t.Fatalf("Rename (pre-touch persist): %v", err)
	}
	if !registryHasSession(t, regPath, id) {
		t.Fatalf("precondition: %q erased by a pre-touch persist; a warm start must keep the entries it did not materialise (#2448)", id)
	}

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-1",
		Cwd:              proj,
		CurrentSessionID: string(id),
		LastUsedAt:       time.Now().UTC(),
	})
	r := sessionRouter{pool: poolB, convReg: reg, active: &activeConversation{}}

	w, err := r.resolve("conv-1")
	if err != nil {
		t.Fatalf("resolve after restart: %v — want the bound session revived, not a reject", err)
	}
	b, ok := w.(boundSession)
	if !ok {
		t.Fatalf("resolve returned %T, want boundSession", w)
	}
	if b.id != id {
		t.Errorf("boundSession.id = %q, want the conversation's bound id %q", b.id, id)
	}

	// AC#1: the id is back in the pool, so the drain's Activate has a session
	// to bring up instead of an ErrSessionNotFound.
	sess, err := poolB.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup after revive: %v", err)
	}
	if sess != b.sess {
		t.Errorf("boundSession.sess = %p, want the pool's entry %p", b.sess, sess)
	}

	// AC#2: the next state-changing persist keeps the revived entry.
	if err := poolB.Rename(poolB.BootstrapID(), "post-touch"); err != nil {
		t.Fatalf("Rename (post-touch persist): %v", err)
	}
	if !registryHasSession(t, regPath, id) {
		if raw, readErr := os.ReadFile(regPath); readErr == nil {
			t.Fatalf("revived session %q erased by the next persist; registry = %s", id, raw)
		}
		t.Fatalf("revived session %q erased by the next persist", id)
	}

	// AC#3: the revived child's workdir is the conversation's directory, not
	// the daemon template WorkDir.
	gotDir, ok := recB.get(string(id))
	if !ok {
		t.Fatalf("no runner was constructed for revived session %q", id)
	}
	if gotDir != projReal {
		t.Errorf("revived runner WorkDir = %q, want the conversation's dir %q", gotDir, projReal)
	}
	if gotDir == tplWorkDir {
		t.Errorf("revived runner WorkDir is the daemon template workdir %q", tplWorkDir)
	}
}

// TestSessionRouter_Resolve_ReviveDefaultCwdMatchesTemplateWorkDir pins the
// convergence AC#3 quietly depends on. A conversation created with a null cwd
// RECORDS resolveDefaultCwd(workdir) but SPAWNS with an empty spawnDir, i.e. in
// the pool's template WorkDir. Reviving sources the directory from the recorded
// Cwd instead, so if those two ever diverged every default conversation would
// silently relocate on restart — splitting its claude JSONL across two
// ~/.claude/projects/<encoded-cwd>/ directories and losing the history.
//
// Both sides reduce to trustMark(EvalSymlinks(Abs(workdir))); this asserts on
// the pre-trust halves, which is where a change to either function would land.
func TestSessionRouter_Resolve_ReviveDefaultCwdMatchesTemplateWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workdir := filepath.Join(home, "proj")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// The daemon's own template WorkDir, as runSupervisor derives it.
	wantTplWorkDir, err := confineWorkdirToHome(workdir)
	if err != nil {
		t.Fatalf("confineWorkdirToHome: %v", err)
	}
	rec := installRecordingTrustMark(t, wantTplWorkDir, nil)

	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := newRecordingPool(t, regPath, wantTplWorkDir, &workDirRecorder{})
	runPoolReady(t, pool)

	id, err := sessions.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-default",
		Cwd:              resolveDefaultCwd(workdir), // what create_conversation records for a null cwd
		CurrentSessionID: string(id),
		LastUsedAt:       time.Now().UTC(),
	})
	r := sessionRouter{pool: pool, convReg: reg, active: &activeConversation{}}

	if _, err := r.resolve("conv-default"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("trustMark called %d times during a revive, want exactly 1", rec.calls)
	}
	if rec.gotWorkdir != wantTplWorkDir {
		t.Errorf("revive resolved a default conversation's Cwd to %q, want the template workdir %q — a default conversation must not relocate on restart", rec.gotWorkdir, wantTplWorkDir)
	}
}

// TestSessionRouter_Resolve_ReviveRejectsEscapingCwd is AC#4: the recorded Cwd
// is re-validated at revive time, not trusted. A path that was inside $HOME when
// the conversation was minted can be turned into an escape before the restart by
// anyone with write access under $HOME, and the revive is exactly where a
// mint-time-only check would have been believed (#696 must not regress).
func TestSessionRouter_Resolve_ReviveRejectsEscapingCwd(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	link := filepath.Join(home, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	rec := installRecordingTrustMark(t, "/unused", nil)

	regPath := filepath.Join(t.TempDir(), "sessions.json")
	runnerRec := &workDirRecorder{}
	pool := newRecordingPool(t, regPath, t.TempDir(), runnerRec)
	runPoolReady(t, pool)

	id, err := sessions.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-escaped",
		Cwd:              link,
		CurrentSessionID: string(id),
		LastUsedAt:       time.Now().UTC(),
	})
	r := sessionRouter{pool: pool, convReg: reg, active: &activeConversation{}}

	w, err := r.resolve("conv-escaped")
	if err == nil {
		t.Fatalf("resolve = (%v, nil), want the escaping Cwd rejected", w)
	}
	if !errors.Is(err, handlers.ErrSpawnDirRejected) {
		t.Errorf("error %v does not match ErrSpawnDirRejected", err)
	}
	if w != nil {
		t.Errorf("writer = %v, want nil — a rejected revive must never yield a write surface", w)
	}
	if rec.calls != 0 {
		t.Errorf("trustMark called %d times for an escaping Cwd, want 0", rec.calls)
	}
	if _, err := pool.Lookup(id); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Errorf("Lookup err = %v, want ErrSessionNotFound — a rejected revive must register nothing", err)
	}
	if dir, ok := runnerRec.get(string(id)); ok {
		t.Errorf("a runner was constructed for the rejected revive with workdir %q, want none", dir)
	}
}

// TestSessionRouter_Resolve_ReviveRejectsSymlinkedAncestorNotCreated is the
// second AC#4 rung and the sharper one: a not-yet-existing Cwd under a $HOME
// symlink pointing outside must be rejected AND must not be created. A revive
// that ran MkdirAll before the containment check would materialise a directory
// outside $HOME on every restart.
func TestSessionRouter_Resolve_ReviveRejectsSymlinkedAncestorNotCreated(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	link := filepath.Join(home, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	rec := installRecordingTrustMark(t, "/unused", nil)

	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := newRecordingPool(t, regPath, t.TempDir(), &workDirRecorder{})
	runPoolReady(t, pool)

	id, err := sessions.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-escaped-ancestor",
		Cwd:              filepath.Join(link, "scratch"),
		CurrentSessionID: string(id),
		LastUsedAt:       time.Now().UTC(),
	})
	r := sessionRouter{pool: pool, convReg: reg, active: &activeConversation{}}

	if _, err := r.resolve("conv-escaped-ancestor"); err == nil {
		t.Fatalf("resolve = nil error, want the symlinked-ancestor escape rejected")
	} else if !errors.Is(err, handlers.ErrSpawnDirRejected) {
		t.Errorf("error %v does not match ErrSpawnDirRejected", err)
	}
	if rec.calls != 0 {
		t.Errorf("trustMark called %d times for an escaping symlinked ancestor, want 0", rec.calls)
	}
	target := filepath.Join(outside, "scratch")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("escaping target %q was created (Stat err = %v), want never created", target, err)
	}
	if _, err := pool.Lookup(id); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Errorf("Lookup err = %v, want ErrSessionNotFound — a rejected revive must register nothing", err)
	}
}
