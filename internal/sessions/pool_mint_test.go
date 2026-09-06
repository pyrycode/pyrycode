package sessions

import (
	"path/filepath"
	"testing"
	"time"
)

// TestPool_Mint_RegistersWithoutSpawning: Mint lands a fresh session in the
// pool, on disk, and in the evicted lifecycle state with NO claude child — the
// shape a conversation created but never messaged has since #2085. The absence
// is asserted over a window long enough that a spawning implementation would
// have raced past it (Pool.Create's own happy-path test proves a child appears
// well inside 2s on this runner).
func TestPool_Mint_RegistersWithoutSpawning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	runPoolInBackground(t, pool)

	id, err := pool.Mint("conv-1", "")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if len(id) != 36 || !uuidPattern.MatchString(string(id)) {
		t.Errorf("Mint id = %q, want canonical UUID", id)
	}

	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(minted id): %v", err)
	}
	if got := sess.LifecycleState(); got != stateEvicted {
		t.Errorf("LifecycleState = %v, want %v", got, stateEvicted)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pid := sess.State().ChildPID; pid > 0 {
			t.Fatalf("Mint spawned a child (pid=%d); it must register only — the child comes up on the first Activate", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Persisted, so the binding the create handler records points at a session
	// that survives a reload — the property the eager bind depends on.
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	found := false
	for _, e := range reg.Sessions {
		if e.ID == id {
			found = true
			if e.LifecycleState != stateEvicted.String() {
				t.Errorf("persisted lifecycle_state = %q, want %q", e.LifecycleState, stateEvicted.String())
			}
		}
	}
	if !found {
		t.Errorf("minted id %q absent from %s", id, regPath)
	}
}

// TestPool_Mint_ThenActivateSpawns: the minted session comes up on the caller's
// first Activate, through the ordinary lazy path an idle-evicted session takes.
// This is the other half of the contract — Mint that never spawns is only
// correct if Activate still does.
func TestPool_Mint_ThenActivateSpawns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	id, err := pool.Mint("conv-1", "")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(minted id): %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate(minted): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return sess.State().ChildPID > 0
	}) {
		t.Fatalf("minted session never spawned on Activate; state=%+v lc=%v", sess.State(), sess.LifecycleState())
	}
}

// TestPool_Activate_PrimesAllocatedUUID is the #2085 AC#4 regression: a
// conversation whose first message arrives long after the mint must still spawn
// without the rotation watcher reading its brand-new transcript as a /clear.
//
// The skip-set entry expires after allocatedTTL, so priming at mint time cannot
// survive an arbitrary wait — the entry has to be written by the thing that
// actually spawns. The test forces the expiry rather than waiting it out:
// allocatedTTL is shortened, the mint's window is allowed to lapse, and only
// then is Activate called. It fails against any implementation that primes at
// mint and not at the spawn.
func TestPool_Activate_PrimesAllocatedUUID(t *testing.T) {
	// Not parallel: mutates the package-level allocatedTTL, matching
	// TestPool_RegisterAllocatedUUID_Expires.
	prev := allocatedTTL
	allocatedTTL = 50 * time.Millisecond
	defer func() { allocatedTTL = prev }()

	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	id, err := pool.Mint("conv-1", "")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// Outlive any entry the mint may have written.
	time.Sleep(150 * time.Millisecond)

	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate(minted): %v", err)
	}
	if !pool.IsAllocated(id) {
		t.Errorf("IsAllocated(%q) = false after Activate, want true — the spawn must prime the "+
			"rotation skip-set, or the watcher reads the new transcript's CREATE as a /clear rotation", id)
	}
}

// TestPool_CreateIn_StillActivates pins the out-of-scope half of #2085: the
// control plane's `sessions new` verb reaches the pool through Pool.Create →
// CreateIn, and an operator asking for a session expects one. Only the relay's
// per-conversation mint defers.
func TestPool_CreateIn_StillActivates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	id, err := pool.CreateIn(ctx, "operator", "")
	if err != nil {
		t.Fatalf("CreateIn: %v", err)
	}
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(created id): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return sess.State().ChildPID > 0
	}) {
		t.Fatalf("CreateIn did not bring the session up; state=%+v lc=%v", sess.State(), sess.LifecycleState())
	}
}
