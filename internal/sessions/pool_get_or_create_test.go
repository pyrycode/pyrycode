package sessions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestValidID covers the canonical-shape validator next to NewID. Cases pin
// each rejection rule independently so a future relaxation surfaces as a
// localized failure.
func TestValidID(t *testing.T) {
	t.Parallel()

	canonical, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"canonical from NewID", string(canonical), true},
		{"hand-rolled canonical", "11111111-2222-4333-8444-555555555555", true},
		{"variant 0x9", "11111111-2222-4333-9444-555555555555", true},
		{"variant 0xa", "11111111-2222-4333-a444-555555555555", true},
		{"variant 0xb", "11111111-2222-4333-b444-555555555555", true},
		{"too short", "11111111-2222-4333-8444-55555555555", false},
		{"too long", "11111111-2222-4333-8444-5555555555555", false},
		{"missing dash 8", "111111112-222-4333-8444-555555555555", false},
		{"missing dash 13", "11111111-22222-333-8444-555555555555", false},
		{"missing dash 18", "11111111-2222-43338-444-555555555555", false},
		{"missing dash 23", "11111111-2222-4333-84445-55555555555", false},
		{"non-hex char", "zzzzzzzz-2222-4333-8444-555555555555", false},
		{"uppercase rejected", "AAAAAAAA-2222-4333-8444-555555555555", false},
		{"version-3 nibble", "11111111-2222-3333-8444-555555555555", false},
		{"version-5 nibble", "11111111-2222-5333-8444-555555555555", false},
		{"variant 0xc rejected", "11111111-2222-4333-c444-555555555555", false},
		{"variant 0x0 rejected", "11111111-2222-4333-0444-555555555555", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidID(tc.in); got != tc.want {
				t.Errorf("ValidID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestPool_GetOrCreate_Take_ReturnsExisting: when id is already registered,
// GetOrCreate is a constant-time short-circuit. The caller's label argument
// is dropped — the take path does not touch the existing label.
func TestPool_GetOrCreate_Take_ReturnsExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	if !pollUntil(t, 2*time.Second, func() bool {
		return pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never spawned")
	}

	id, err := pool.Create(ctx, "x")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := pool.GetOrCreate(ctx, id, "y")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if got != id {
		t.Errorf("GetOrCreate id = %q, want %q", got, id)
	}

	// Snapshot still has bootstrap + the original session — no duplicate.
	snap := pool.Snapshot()
	if len(snap) != 2 {
		t.Errorf("Snapshot len = %d, want 2 (bootstrap + take target)", len(snap))
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 2 {
		t.Fatalf("registry sessions = %+v, want 2", reg)
	}
	for _, e := range reg.Sessions {
		if e.ID == id && e.Label != "x" {
			t.Errorf("take-path mutated label: got %q, want %q (input label dropped)", e.Label, "x")
		}
	}
}

// TestPool_GetOrCreate_Create_Persists: caller-supplied id flows verbatim
// into the registry; the new session is supervised and spawns a real child.
func TestPool_GetOrCreate_Create_Persists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	got, err := pool.GetOrCreate(ctx, target, "claudian-chat-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if got != target {
		t.Errorf("returned id = %q, want %q (caller-supplied)", got, target)
	}

	sess, err := pool.Lookup(target)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.State().ChildPID > 0
	}) {
		t.Fatalf("new session never spawned a child; state=%+v", sess.State())
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 2 {
		t.Fatalf("registry sessions = %+v, want 2", reg)
	}
	var found bool
	for _, e := range reg.Sessions {
		if e.ID == target {
			found = true
			if e.Label != "claudian-chat-1" {
				t.Errorf("on-disk label = %q, want %q", e.Label, "claudian-chat-1")
			}
			if e.Bootstrap {
				t.Errorf("new entry has bootstrap=true, want false")
			}
		}
	}
	if !found {
		t.Errorf("new entry %q missing from registry", target)
	}
}

// TestPool_GetOrCreate_PersistsPostDetach: AC#1 — the created session
// persists past disconnect. Evict the new session and verify the registry
// keeps the entry (with lifecycleState = evicted).
func TestPool_GetOrCreate_PersistsPostDetach(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	if _, err := pool.GetOrCreate(ctx, target, "label"); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	sess, err := pool.Lookup(target)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		return sess.LifecycleState() == stateActive && sess.State().ChildPID > 0
	}) {
		t.Fatal("session never reached active+spawned")
	}

	if err := sess.Evict(ctx); err != nil {
		t.Fatalf("Evict: %v", err)
	}

	if !pollUntil(t, 5*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatal("session did not transition to evicted")
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	var found bool
	for _, e := range reg.Sessions {
		if e.ID == target {
			found = true
			if e.LifecycleState != "evicted" {
				t.Errorf("on-disk lifecycleState = %q, want %q", e.LifecycleState, "evicted")
			}
		}
	}
	if !found {
		t.Errorf("entry %q missing from registry post-disconnect", target)
	}
}

// TestPool_GetOrCreate_InvalidID: empty / malformed / non-UUIDv4 ids return
// ErrInvalidSessionID and leave Pool state unchanged.
func TestPool_GetOrCreate_InvalidID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	cases := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"non-canonical short", "abc"},
		{"version-3 UUID", "11111111-2222-3333-8444-555555555555"},
		{"uppercase", "AAAAAAAA-2222-4333-8444-555555555555"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := pool.GetOrCreate(ctx, SessionID(tc.id), "")
			if !errors.Is(err, ErrInvalidSessionID) {
				t.Errorf("err = %v, want ErrInvalidSessionID", err)
			}
			if got != "" {
				t.Errorf("returned id = %q, want empty", got)
			}
		})
	}

	// Bootstrap unchanged: still 1 entry in Snapshot.
	if got := len(pool.Snapshot()); got != 1 {
		t.Errorf("Snapshot len = %d, want 1 (bootstrap only)", got)
	}
}

// TestPool_GetOrCreate_PoolNotRunning: if Pool.Run has not started, the
// create path rolls back the in-memory entry and returns ErrPoolNotRunning.
// Same shape as Pool.Create's rollback for the same condition.
func TestPool_GetOrCreate_PoolNotRunning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	// Deliberately do NOT call Run.

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	got, err := pool.GetOrCreate(context.Background(), target, "")
	if !errors.Is(err, ErrPoolNotRunning) {
		t.Fatalf("err = %v, want ErrPoolNotRunning", err)
	}
	if got != "" {
		t.Errorf("returned id = %q, want empty (rollback)", got)
	}

	if _, err := pool.Lookup(target); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup err = %v, want ErrSessionNotFound (rollback failed)", err)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Errorf("registry has %d entries, want 1 (bootstrap only)", len(reg.Sessions))
	}
}

// TestPool_GetOrCreate_ConcurrentSameID is AC#4: N goroutines racing on the
// same UUID produce exactly one registry entry. All callers see the same
// canonical id and a nil error.
func TestPool_GetOrCreate_ConcurrentSameID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 0)
	ctx, _ := runPoolInBackground(t, pool)

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	const N = 8
	type result struct {
		id  SessionID
		err error
	}
	results := make([]result, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			label := fmt.Sprintf("g-%d", i)
			id, err := pool.GetOrCreate(ctx, target, label)
			results[i] = result{id: id, err: err}
		}()
	}
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d: err = %v, want nil", i, r.err)
		}
		if r.id != target {
			t.Errorf("goroutine %d: id = %q, want %q", i, r.id, target)
		}
	}

	snap := pool.Snapshot()
	if len(snap) != 2 {
		t.Errorf("Snapshot len = %d, want 2 (bootstrap + target); ids=%v", len(snap), snap)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 2 {
		t.Fatalf("registry sessions = %+v, want 2", reg)
	}
	// Label of the winner is one of the g-N strings.
	allowed := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		allowed[fmt.Sprintf("g-%d", i)] = struct{}{}
	}
	for _, e := range reg.Sessions {
		if e.ID == target {
			if _, ok := allowed[e.Label]; !ok {
				t.Errorf("winner label = %q, not in expected g-N set", e.Label)
			}
		}
	}
}

// TestPool_GetOrCreate_HonorsCap: ActiveCap=1; creating a new session under
// GetOrCreate evicts the bootstrap via the cap-aware Activate path. Proves
// the create branch reuses Pool.Activate's cap logic rather than spawning
// outside the cap.
func TestPool_GetOrCreate_HonorsCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolCreate(t, regPath, 1)
	ctx, _ := runPoolInBackground(t, pool)

	if !pollUntil(t, 5*time.Second, func() bool {
		return pool.Default().LifecycleState() == stateActive &&
			pool.Default().State().ChildPID > 0
	}) {
		t.Fatal("bootstrap never reached active")
	}

	target, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	if _, err := pool.GetOrCreate(ctx, target, ""); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	sess, err := pool.Lookup(target)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	if !pollUntil(t, 5*time.Second, func() bool {
		return pool.Default().LifecycleState() == stateEvicted &&
			sess.LifecycleState() == stateActive &&
			sess.State().ChildPID > 0 &&
			pool.Default().State().ChildPID == 0
	}) {
		t.Fatalf("cap=1 transitions did not settle: bootstrap=%v(pid=%d) new=%v(pid=%d)",
			pool.Default().LifecycleState(), pool.Default().State().ChildPID,
			sess.LifecycleState(), sess.State().ChildPID)
	}
}
