package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPool_Revive_RegistersWithoutSpawning is the primitive's core claim: an id
// the pool has never seen becomes a live, Lookup-able, persisted entry with no
// claude child behind it — the shape a persisted-but-dropped session needs so
// the existing lazy-respawn path can take it from there (#1487).
func TestPool_Revive_RegistersWithoutSpawning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	sess, err := pool.Revive(target, "conv-1", "")
	if err != nil {
		t.Fatalf("Revive: %v", err)
	}
	if sess == nil {
		t.Fatal("Revive returned a nil session with a nil error")
	}
	if sess.ID() != target {
		t.Errorf("revived session id = %q, want %q", sess.ID(), target)
	}

	got, err := pool.Lookup(target)
	if err != nil {
		t.Fatalf("Lookup(revived id): %v", err)
	}
	if got != sess {
		t.Errorf("Lookup returned %p, want the revived session %p", got, sess)
	}

	// The whole point of the primitive: registered, not spawned. Activating is
	// the caller's separate step, so nothing may have a child yet.
	if lc := sess.LifecycleState(); lc != stateEvicted {
		t.Errorf("revived lifecycle state = %v, want %v", lc, stateEvicted)
	}
	if pid := sess.State().ChildPID; pid != 0 {
		t.Errorf("revived session has ChildPID %d, want 0 — Revive must not spawn", pid)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	var entry *registryEntry
	for i := range reg.Sessions {
		if reg.Sessions[i].ID == target {
			entry = &reg.Sessions[i]
		}
	}
	if entry == nil {
		t.Fatalf("revived entry %q missing from %q; entries = %+v", target, regPath, reg.Sessions)
	}
	if entry.Bootstrap {
		t.Errorf("revived entry has bootstrap=true, want false")
	}
	if entry.Label != "conv-1" {
		t.Errorf("revived entry label = %q, want %q", entry.Label, "conv-1")
	}
	if entry.LifecycleState != stateEvicted.String() {
		t.Errorf("revived entry lifecycle_state = %q, want %q", entry.LifecycleState, stateEvicted.String())
	}
}

// TestPool_Revive_ActivatesNormally is the AC#1 half that proves a revived
// session reaches a claude child: it is indistinguishable from an idle-evicted
// one, so the ordinary cap-aware Pool.Activate brings it up.
func TestPool_Revive_ActivatesNormally(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	sess, err := pool.Revive(target, "conv-1", "")
	if err != nil {
		t.Fatalf("Revive: %v", err)
	}

	if err := pool.Activate(ctx, target); err != nil {
		t.Fatalf("Activate(revived): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return sess.State().ChildPID > 0
	}) {
		t.Fatalf("revived session never spawned a child; state=%+v lc=%v", sess.State(), sess.LifecycleState())
	}
}

// TestPool_Revive_TakePath_ReturnsExisting: a revive racing (or following) a
// live session must hand back that session untouched rather than replacing it —
// the same take-path contract GetOrCreateIn documents, and the resolution for
// two conns reviving the same id concurrently.
func TestPool_Revive_TakePath_ReturnsExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	id, err := pool.Create(ctx, "original")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	existing, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	got, err := pool.Revive(id, "revived-label", "")
	if err != nil {
		t.Fatalf("Revive (take path): %v", err)
	}
	if got != existing {
		t.Errorf("Revive returned %p, want the existing session %p", got, existing)
	}
	if again, err := pool.Lookup(id); err != nil || again != existing {
		t.Errorf("Lookup after take-path revive = (%p, %v), want (%p, nil) — the entry must not be replaced", again, err, existing)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	for _, e := range reg.Sessions {
		if e.ID == id && e.Label != "original" {
			t.Errorf("take-path revive rewrote the label to %q, want %q dropped", e.Label, "revived-label")
		}
	}
}

// TestPool_Revive_SpawnsInGivenDir is AC#3's pool half: the spawnDir a caller
// hands Revive is the directory the revived child eventually runs in, not the
// shared template workdir. Mirrors TestPool_GetOrCreateIn_SpawnsInGivenDir's
// capture pattern, with the extra rung that no runner reaches Run until the
// caller Activates.
func TestPool_Revive_SpawnsInGivenDir(t *testing.T) {
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
	if _, err := pool.Revive(target, "", spawnDir); err != nil {
		t.Fatalf("Revive: %v", err)
	}

	// A spawn would land within the sub-second spawn path; 1s with no marker
	// confirms Revive alone started nothing.
	if pollUntil(t, 1*time.Second, func() bool {
		return markerExists(spawnDir, target)
	}) {
		t.Fatalf("Revive spawned a child in %q before any Activate", spawnDir)
	}

	if err := pool.Activate(ctx, target); err != nil {
		t.Fatalf("Activate(revived): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return markerExists(spawnDir, target)
	}) {
		t.Fatalf("revived child did not record cwd in spawnDir %q", spawnDir)
	}
	if markerExists(tplWorkDir, target) {
		t.Errorf("revived child ran in the template workdir %q; want spawnDir %q", tplWorkDir, spawnDir)
	}
}

// TestPool_Revive_EmptySpawnDir_UsesTemplateWorkDir pins the other leg: an empty
// spawnDir keeps today's behaviour — the shared template workdir.
func TestPool_Revive_EmptySpawnDir_UsesTemplateWorkDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolSpawnDir(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := pool.Revive(target, "", ""); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	if err := pool.Activate(ctx, target); err != nil {
		t.Fatalf("Activate(revived): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return markerExists(tplWorkDir, target)
	}) {
		t.Fatalf("revived child did not record cwd in template workdir %q", tplWorkDir)
	}
}

// TestPool_Revive_PoolNotRunning is the rollback path: with no lifecycle
// goroutine to drive it, a revive must leave neither an in-memory entry nor a
// disk one. The assertion is on the registry BYTES, not just map membership —
// the rollback re-persists, and a rollback that dropped the entry from memory
// but wrote a changed file would still corrupt the id's on-disk record.
func TestPool_Revive_PoolNotRunning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	// Deliberately do NOT call Run.

	before, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry after New: %v", err)
	}

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	sess, err := pool.Revive(target, "conv-1", "")
	if !errors.Is(err, ErrPoolNotRunning) {
		t.Fatalf("err = %v, want ErrPoolNotRunning", err)
	}
	if sess != nil {
		t.Errorf("Revive returned session %p on failure, want nil", sess)
	}
	if _, err := pool.Lookup(target); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup err = %v, want ErrSessionNotFound (in-memory rollback failed)", err)
	}

	after, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry after Revive: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("registry changed across a rolled-back revive:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestPool_Revive_InvalidID: a conversation whose CurrentSessionID is not a
// canonical UUIDv4 is rejected before any pool state is touched. This is the
// gate a malformed persisted binding hits.
func TestPool_Revive_InvalidID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	runPoolInBackground(t, pool)

	for _, id := range []SessionID{"", "session-not-in-pool", "550e8400e29b41d4a716446655440000"} {
		sess, err := pool.Revive(id, "conv-1", "")
		if !errors.Is(err, ErrInvalidSessionID) {
			t.Errorf("Revive(%q) err = %v, want ErrInvalidSessionID", id, err)
		}
		if sess != nil {
			t.Errorf("Revive(%q) returned session %p, want nil", id, sess)
		}
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if len(reg.Sessions) != 1 {
		t.Errorf("registry has %d entries, want 1 (bootstrap only)", len(reg.Sessions))
	}
}
