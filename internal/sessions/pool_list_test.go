package sessions

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestPool_List_BootstrapOnly: a fresh persistent pool with no extra sessions
// returns one entry. The synthetic "bootstrap" label is surfaced in the
// returned slice; the on-disk label remains empty.
func TestPool_List_BootstrapOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	list := pool.List()
	if len(list) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(list))
	}
	if !list[0].Bootstrap {
		t.Errorf("list[0].Bootstrap = false, want true")
	}
	if list[0].Label != "bootstrap" {
		t.Errorf("list[0].Label = %q, want %q (synthetic substitution)", list[0].Label, "bootstrap")
	}
	if list[0].ID != pool.Default().ID() {
		t.Errorf("list[0].ID = %q, want %q (Default)", list[0].ID, pool.Default().ID())
	}
	if list[0].LifecycleState != stateActive {
		t.Errorf("list[0].LifecycleState = %v, want stateActive", list[0].LifecycleState)
	}
	if list[0].LastActiveAt.IsZero() {
		t.Errorf("list[0].LastActiveAt is zero")
	}

	// Synthetic substitution must NOT have leaked to disk.
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg == nil || len(reg.Sessions) != 1 {
		t.Fatalf("registry = %+v, want one session", reg)
	}
	if reg.Sessions[0].Label != "" {
		t.Errorf("on-disk label = %q, want empty (synthetic substitution leaked)", reg.Sessions[0].Label)
	}

	// In-memory bootstrap label must also remain empty — substitution happens
	// only on the returned value.
	if pool.Default().label != "" {
		t.Errorf("in-memory bootstrap label = %q, want empty", pool.Default().label)
	}
}

// TestPool_List_OrderingByLastActive: with three sessions whose lastActiveAt
// values are distinct, List returns them in descending order. With a fourth
// session that ties with one of the existing entries, the tiebreak on
// SessionID is stable across calls.
func TestPool_List_OrderingByLastActive(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)

	t0, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	bumpLastActive(t, pool.Default(), t0)

	idA := SessionID("11111111-1111-4111-8111-111111111111")
	idB := SessionID("22222222-2222-4222-8222-222222222222")
	idC := SessionID("33333333-3333-4333-8333-333333333333") // ties with A
	addBareSession(t, pool, idA, t0.Add(1*time.Minute))
	addBareSession(t, pool, idB, t0.Add(2*time.Minute))

	// Three sessions, all distinct lastActiveAt: bootstrap=t0, A=t0+1m, B=t0+2m.
	list := pool.List()
	if len(list) != 3 {
		t.Fatalf("len(List()) = %d, want 3", len(list))
	}
	want := []SessionID{idB, idA, pool.Default().ID()}
	for i, w := range want {
		if list[i].ID != w {
			t.Errorf("list[%d].ID = %q, want %q (descending order)", i, list[i].ID, w)
		}
	}

	// Add a tied entry. The tiebreak is SessionID ascending — idA < idC, so
	// the order at index 1/2 should be idA, idC (within the t0+1m group).
	addBareSession(t, pool, idC, t0.Add(1*time.Minute))

	first := pool.List()
	second := pool.List()
	if len(first) != 4 {
		t.Fatalf("len(List()) = %d, want 4", len(first))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("ordering not stable across calls at index %d: first=%q second=%q",
				i, first[i].ID, second[i].ID)
		}
	}
	wantWithTie := []SessionID{idB, idA, idC, pool.Default().ID()}
	for i, w := range wantWithTie {
		if first[i].ID != w {
			t.Errorf("list[%d].ID = %q, want %q (tiebreak by SessionID ascending)",
				i, first[i].ID, w)
		}
	}
}

// TestPool_List_BootstrapLabelPassthrough: when the bootstrap entry on disk
// has a non-empty label, the synthetic "bootstrap" substitution does NOT
// clobber it.
func TestPool_List_BootstrapLabelPassthrough(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")

	id := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: id, Label: "main", CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	pool := helperPoolPersistent(t, regPath)
	list := pool.List()
	if len(list) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(list))
	}
	if list[0].Label != "main" {
		t.Errorf("list[0].Label = %q, want %q (operator-set label preserved)", list[0].Label, "main")
	}
	if !list[0].Bootstrap {
		t.Errorf("list[0].Bootstrap = false, want true")
	}
}

// TestPool_List_DeepCopy: mutating an element of the returned slice must not
// affect a subsequent List call. Guards the "deep enough copy" AC.
func TestPool_List_DeepCopy(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)

	first := pool.List()
	if len(first) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(first))
	}
	first[0].Label = "tampered"
	first[0].LastActiveAt = time.Time{}

	second := pool.List()
	if second[0].Label == "tampered" {
		t.Errorf("List returned a shared label string; mutation leaked")
	}
	if second[0].LastActiveAt.IsZero() {
		t.Errorf("List returned a shared LastActiveAt; mutation leaked")
	}
}

// TestPool_List_RaceClean: many concurrent List readers plus one goroutine
// invoking RotateID (which takes Pool.mu write) must be -race clean. The
// assertion is purely "go test -race is silent."
func TestPool_List_RaceClean(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}

	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool, err := New(Config{
		Bootstrap:    SessionConfig{ClaudeBin: "/bin/sleep"},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath: regPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const readers = 16
	const iters = 100

	stop := make(chan struct{})
	var readersWG sync.WaitGroup
	var mutatorWG sync.WaitGroup

	for i := 0; i < readers; i++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for j := 0; j < iters; j++ {
				_ = pool.List()
			}
		}()
	}

	// Mutator: alternately rotate the bootstrap id back and forth. RotateID
	// takes Pool.mu (write); List takes Pool.mu (read). This is the lock-
	// ordering surface we want -race to inspect.
	mutatorWG.Add(1)
	go func() {
		defer mutatorWG.Done()
		idA := pool.Default().ID()
		idB := SessionID("99999999-9999-4999-8999-999999999999")
		current := idA
		next := idB
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := pool.RotateID(current, next); err != nil {
				t.Errorf("RotateID(%q, %q): %v", current, next, err)
				return
			}
			current, next = next, current
		}
	}()

	// Wait for readers to finish their fixed iteration count, then signal
	// the mutator to stop and wait for it.
	readersWG.Wait()
	close(stop)
	mutatorWG.Wait()

	// Final List must still return exactly one entry — the rotation does
	// not change the cardinality.
	final := pool.List()
	if len(final) != 1 {
		t.Errorf("len(List()) = %d, want 1 after concurrent rotations", len(final))
	}

	// Registry on disk must not have been corrupted by the rotation churn.
	if _, err := os.Stat(regPath); err != nil {
		t.Errorf("registry stat: %v", err)
	}
}

// bumpLastActive sets the session's lastActiveAt under lcMu. Used to drive
// List ordering tests without poking transitionTo.
func bumpLastActive(t *testing.T, s *Session, when time.Time) {
	t.Helper()
	s.lcMu.Lock()
	s.lastActiveAt = when
	s.lcMu.Unlock()
}

// addBareSession registers a minimal *Session in pool.sessions under id with
// lastActiveAt set to when. The session has no supervisor, no bridge — it is
// only there to be enumerated by List.
func addBareSession(t *testing.T, pool *Pool, id SessionID, when time.Time) {
	t.Helper()
	sess := &Session{
		id:           id,
		log:          pool.log,
		createdAt:    when,
		lastActiveAt: when,
		pool:         pool,
		lcState:      stateActive,
		activeCh:     closedChan(),
		evictedCh:    make(chan struct{}),
		activateCh:   make(chan struct{}, 1),
		evictCh:      make(chan struct{}, 1),
	}
	pool.mu.Lock()
	pool.sessions[id] = sess
	pool.mu.Unlock()
}
