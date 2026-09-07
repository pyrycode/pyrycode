package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// #839: the bootstrap session resumes its OWN deterministic id and never adopts
// a foreign session id from the shared ~/.claude/projects/<cwd>/ dir by mtime.
// These tests drive Pool.New / RotateID / BootstrapID directly (no live claude);
// the spawn-time "--session-id <id>" argv shape is covered by
// supervisor.TestBuildClaudeArgs, and the end-to-end spawn by the e2e suite.
// Together BootstrapID() returning the right id + buildClaudeArgs appending it
// cover "the spawned argv is --session-id <bootstrapID>".

// TestPool_New_DoesNotAdoptForeignSessionByMtime (AC-2/AC-3): a warm-started
// pool keeps its persisted bootstrap id even when a second claude has written a
// NEWER <uuid>.jsonl into the shared sessions dir. The bootstrap id is now pinned
// deterministically (from the persisted registry / --session-id, #839) and cannot
// rotate to a foreign uuid; the old startup adopt-by-mtime scan that would have
// done so is gone.
func TestPool_New_DoesNotAdoptForeignSessionByMtime(t *testing.T) {
	t.Parallel()
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	claudeDir := t.TempDir()

	own := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	foreign := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: own, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	// The daemon's own transcript plus a second claude's, written more recently.
	now := time.Now()
	ownPath := filepath.Join(claudeDir, string(own)+".jsonl")
	foreignPath := filepath.Join(claudeDir, string(foreign)+".jsonl")
	if err := os.WriteFile(ownPath, []byte("own\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(ownPath, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignPath, []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(foreignPath, now, now); err != nil {
		t.Fatal(err)
	}

	pool, err := helperPoolReconciling(t, regPath, claudeDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := pool.BootstrapID(); got != own {
		t.Errorf("BootstrapID() = %q, want %q (must not adopt the newer foreign uuid %q)", got, own, foreign)
	}
	// On-disk registry must still record the daemon's own id.
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 || reg.Sessions[0].ID != own {
		t.Fatalf("registry = %+v, want single bootstrap entry with id %q", reg, own)
	}
}

// TestPool_New_ColdStart_MintsOwnIdNotForeign (AC-3): a cold start (no registry)
// mints a fresh daemon-owned id and persists it; it does NOT adopt a foreign
// <uuid>.jsonl already present in the shared sessions dir. Pre-#839, cold start
// adopted the on-disk newest by mtime.
func TestPool_New_ColdStart_MintsOwnIdNotForeign(t *testing.T) {
	t.Parallel()
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	claudeDir := t.TempDir()

	foreign := SessionID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	if err := os.WriteFile(filepath.Join(claudeDir, string(foreign)+".jsonl"), []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pool, err := helperPoolReconciling(t, regPath, claudeDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := pool.BootstrapID()
	if got == foreign {
		t.Fatalf("BootstrapID() adopted the foreign on-disk uuid %q", foreign)
	}
	if !uuidPattern.MatchString(string(got)) {
		t.Errorf("BootstrapID() = %q, want a freshly-minted canonical UUID", got)
	}
	// Cold start persists the minted id, not the foreign one.
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 || reg.Sessions[0].ID != got {
		t.Fatalf("registry = %+v, want single bootstrap entry with the minted id %q", reg, got)
	}
}

// TestPool_BootstrapID_FollowsClearRotation (AC-4): a /clear rotates the on-disk
// id via RotateID (the rekey-and-persist seam every rotation path shares);
// BootstrapID then
// resolves the rotated id, so the next spawn's ResolveSessionID provider hands
// claude --session-id <rotated>, not the orphaned pre-/clear id.
func TestPool_BootstrapID_FollowsClearRotation(t *testing.T) {
	t.Parallel()
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	id0 := pool.BootstrapID()
	id1 := SessionID("99999999-9999-4999-8999-999999999999")
	if err := pool.RotateID(id0, id1); err != nil {
		t.Fatalf("RotateID: %v", err)
	}
	if got := pool.BootstrapID(); got != id1 {
		t.Errorf("BootstrapID() = %q, want %q (rotated id after /clear)", got, id1)
	}
}

// TestPool_BootstrapID_StableAndReused (AC-5): BootstrapID is invariant across
// repeated reads with no rotation, and a warm-started pool reuses the persisted
// id rather than re-minting one per restart. It changes only via RotateID
// (covered by TestPool_BootstrapID_FollowsClearRotation).
func TestPool_BootstrapID_StableAndReused(t *testing.T) {
	t.Parallel()

	// Stable across repeated reads with no RotateID.
	pool := helperPool(t, false)
	first := pool.BootstrapID()
	for i := 0; i < 5; i++ {
		if got := pool.BootstrapID(); got != first {
			t.Fatalf("read %d: BootstrapID() = %q, want stable %q", i, got, first)
		}
	}

	// Warm start reuses the persisted id (no re-mint per restart).
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "sessions.json")
	persisted := SessionID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: persisted, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	warm, err := helperPoolReconciling(t, regPath, "")
	if err != nil {
		t.Fatalf("New (warm): %v", err)
	}
	if got := warm.BootstrapID(); got != persisted {
		t.Errorf("warm-start BootstrapID() = %q, want persisted %q (must not re-mint)", got, persisted)
	}
}
