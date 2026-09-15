package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// helperPoolWarmStart pre-writes a registry holding a bootstrap entry plus the
// given non-bootstrap entries, then warm-starts an argv-recording pool from that
// same path, returning the pool and the bootstrap id.
//
// It is helperPoolMintBootstrap's recipe widened past one entry. That helper can
// only express what the bootstrap persisted, and this ticket's whole subject is
// what happens to the entries Pool.New does NOT materialise (#2448), so a fixture
// that cannot write a second entry cannot state the case.
//
// Entries with a zero CreatedAt are stamped one second apart, after the
// bootstrap's, so sortEntriesByCreatedAt gives the file a stable order the
// byte-level assertions below can rely on.
func helperPoolWarmStart(t *testing.T, regPath, tplWorkDir string, dormant ...registryEntry) (*Pool, SessionID) {
	t.Helper()
	when := time.Now().UTC()
	boot := SessionID("550e8400-e29b-41d4-a716-446655440000")
	entries := []registryEntry{{
		ID:           boot,
		CreatedAt:    when,
		LastActiveAt: when,
		Bootstrap:    true,
	}}
	for i, e := range dormant {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = when.Add(time.Duration(i+1) * time.Second)
			e.LastActiveAt = e.CreatedAt
		}
		entries = append(entries, e)
	}
	if err := saveRegistryLocked(regPath, &registryFile{Version: 1, Sessions: entries}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}
	return helperPoolArgvRecorder(t, regPath, tplWorkDir), boot
}

// helperDormantID mints a canonical id for a fixture entry — ValidID gates
// Revive, so a hand-written string would fail before reaching the code under
// test.
func helperDormantID(t *testing.T) SessionID {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}

// entryByID reads the registry at path and returns the entry for id, or nil.
func entryByID(t *testing.T, path string, id SessionID) *registryEntry {
	t.Helper()
	reg, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil {
		return nil
	}
	for i := range reg.Sessions {
		if reg.Sessions[i].ID == id {
			return &reg.Sessions[i]
		}
	}
	return nil
}

// TestPool_SaveLocked_KeepsUnmaterialisedEntries (AC 1): an entry loadRegistry
// returned that the pool did not materialise survives a later, unrelated save
// with its model and effort intact.
//
// The save is driven through Rename on the BOOTSTRAP, deliberately: it is an
// operation that has nothing to do with the dropped sessions, which is the shape
// of the defect — a bootstrap idle-eviction was enough to erase a conversation's
// record of the model it was set to.
func TestPool_SaveLocked_KeepsUnmaterialisedEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	configured, bare := helperDormantID(t), helperDormantID(t)

	pool, boot := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: configured, Label: "conv-1", Model: "opus", Effort: "high"},
		registryEntry{ID: bare, Label: "conv-2"},
	)

	if err := pool.Rename(boot, "renamed"); err != nil {
		t.Fatalf("Rename(bootstrap): %v", err)
	}

	got := entryByID(t, regPath, configured)
	if got == nil {
		t.Fatalf("entry %q erased by an unrelated save", configured)
	}
	if got.Model != "opus" || got.Effort != "high" {
		t.Errorf("entry model/effort = %q/%q, want %q/%q", got.Model, got.Effort, "opus", "high")
	}
	if got.Label != "conv-1" {
		t.Errorf("entry label = %q, want %q", got.Label, "conv-1")
	}
	if entryByID(t, regPath, bare) == nil {
		t.Errorf("entry %q erased by an unrelated save", bare)
	}
}

// TestPool_Remove_DropsEntryPermanently (AC 1, second half): a session the
// operator drops is gone from the file and does not come back on the next save.
// The guard against a dormant map that outlives the removal.
func TestPool_Remove_DropsEntryPermanently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	dropped := helperDormantID(t)

	pool, boot := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: dropped, Label: "conv-1", Model: "opus"},
	)
	ctx, _ := runPoolInBackground(t, pool)

	if _, err := pool.Revive(dropped, "conv-1", ""); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	if err := pool.Remove(ctx, dropped, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := entryByID(t, regPath, dropped); got != nil {
		t.Fatalf("removed entry still on disk: %+v", *got)
	}

	// A later unrelated save must not resurrect it.
	if err := pool.Rename(boot, "renamed"); err != nil {
		t.Fatalf("Rename(bootstrap): %v", err)
	}
	if got := entryByID(t, regPath, dropped); got != nil {
		t.Errorf("removed entry reappeared on the next save: %+v", *got)
	}
}

// TestPool_Revive_RestoresPersistedModelAndEffort (AC 2): a dropped session
// comes back with the model and effort its persisted entry carried.
func TestPool_Revive_RestoresPersistedModelAndEffort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "opus", Effort: "high"},
	)
	runPoolInBackground(t, pool)

	if _, err := pool.Revive(target, "conv-1", ""); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	got, err := pool.SettingsFor(target)
	if err != nil {
		t.Fatalf("SettingsFor(revived): %v", err)
	}
	if got.Model != "opus" || got.Effort != "high" {
		t.Errorf("revived settings model/effort = %q/%q, want %q/%q", got.Model, got.Effort, "opus", "high")
	}
}

// TestPool_Revive_RevokesPersistedPosture (AC 3): neither a persisted yolo:true
// nor a persisted non-default permission_mode reaches a revived session, while
// the model and effort beside them still do. The #1487 property, unchanged — a
// restart stays a revocation point for a permission bypass.
func TestPool_Revive_RevokesPersistedPosture(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		entry registryEntry
	}{
		{name: "persisted yolo", entry: registryEntry{Model: "opus", Effort: "high", YOLO: true}},
		{name: "persisted in-band mode", entry: registryEntry{Model: "opus", Effort: "high", PermissionMode: "acceptEdits"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			regPath := filepath.Join(dir, "sessions.json")
			target := helperDormantID(t)
			entry := tc.entry
			entry.ID = target
			entry.Label = "conv-1"

			pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(), entry)
			runPoolInBackground(t, pool)

			if _, err := pool.Revive(target, "conv-1", ""); err != nil {
				t.Fatalf("Revive: %v", err)
			}
			got, err := pool.SettingsFor(target)
			if err != nil {
				t.Fatalf("SettingsFor(revived): %v", err)
			}
			if got.YOLO {
				t.Errorf("revived session carries YOLO, want it revoked (#1487)")
			}
			if got.PermissionMode != permissionModeDefault {
				t.Errorf("revived permission mode = %q, want %q", got.PermissionMode, permissionModeDefault)
			}
			if got.Model != "opus" || got.Effort != "high" {
				t.Errorf("revived model/effort = %q/%q, want %q/%q — the posture is revoked, these are not",
					got.Model, got.Effort, "opus", "high")
			}
		})
	}
}

// TestPool_Revive_SpawnsWithRestoredSettings (AC 4): the revived session's first
// spawn carries the restored model and effort on its claude argv, through the
// existing claudeSettingsArgs composition and with no new spelling.
//
// The stored model is an EXACT id rather than a bare family, so the assertion
// pins #2447's rewrite: --model names the family alias of the stored value, not
// the stored bytes. A fixture storing "opus" would pass either way.
func TestPool_Revive_SpawnsWithRestoredSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, tplWorkDir,
		registryEntry{ID: target, Label: "conv-1", Model: "claude-opus-4-5-20251101", Effort: "high"},
	)
	ctx, _ := runPoolInBackground(t, pool)

	sess, err := pool.Revive(target, "conv-1", spawnDir)
	if err != nil {
		t.Fatalf("Revive: %v", err)
	}
	// Revive registers without spawning; Activate is what brings the child up.
	if err := pool.Activate(ctx, target); err != nil {
		t.Fatalf("Activate(revived): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool { return sess.State().ChildPID > 0 }) {
		t.Fatalf("revived session never spawned a child; state=%+v lc=%v", sess.State(), sess.LifecycleState())
	}

	got := waitArgv(t, spawnDir)
	want := append([]string{"--session-id", string(target), "--model", "opus", "--effort", "high"},
		alwaysOnPosture(permissionModeDefault)...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("revived argv = %v, want %v", got, want)
	}
}

// TestPool_Revive_NothingPersisted_ArgvUnchanged (AC 5): an entry carrying
// neither model nor effort, and an id with no persisted entry at all, both revive
// and spawn exactly as they do today — no flag at all, not an empty-valued one.
func TestPool_Revive_NothingPersisted_ArgvUnchanged(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		hasEntry   bool
		entryLabel string
	}{
		{name: "entry with no model or effort", hasEntry: true, entryLabel: "conv-1"},
		{name: "no persisted entry at all", hasEntry: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			regPath := filepath.Join(dir, "sessions.json")
			tplWorkDir := t.TempDir()
			spawnDir := t.TempDir()
			target := helperDormantID(t)

			var dormant []registryEntry
			if tc.hasEntry {
				dormant = append(dormant, registryEntry{ID: target, Label: tc.entryLabel})
			}
			pool, _ := helperPoolWarmStart(t, regPath, tplWorkDir, dormant...)
			ctx, _ := runPoolInBackground(t, pool)

			sess, err := pool.Revive(target, "conv-1", spawnDir)
			if err != nil {
				t.Fatalf("Revive: %v", err)
			}
			if err := pool.Activate(ctx, target); err != nil {
				t.Fatalf("Activate(revived): %v", err)
			}
			if !pollUntil(t, 5*time.Second, func() bool { return sess.State().ChildPID > 0 }) {
				t.Fatalf("revived session never spawned a child; state=%+v", sess.State())
			}

			got := waitArgv(t, spawnDir)
			want := append([]string{"--session-id", string(target)}, alwaysOnPosture(permissionModeDefault)...)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("revived argv = %v, want %v", got, want)
			}
		})
	}
}

// TestPool_Revive_PoolNotRunning_RestoresDormantEntry is
// TestPool_Revive_PoolNotRunning's byte-level claim against a DORMANT id, which
// is the case that can now actually change the file: materialise retires the
// entry before persisting, so a rolled-back revive that failed to put it back
// would erase the record it was reviving.
func TestPool_Revive_PoolNotRunning_RestoresDormantEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	target := helperDormantID(t)

	// Deliberately do NOT call Run.
	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "opus", Effort: "high"},
	)

	before, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry after New: %v", err)
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
