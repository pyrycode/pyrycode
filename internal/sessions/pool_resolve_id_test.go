package sessions

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// TestPool_ResolveID_EmptyReturnsBootstrap: arg=="" resolves to the bootstrap
// session's id, same seam as Pool.Lookup("").
func TestPool_ResolveID_EmptyReturnsBootstrap(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)

	got, err := pool.ResolveID("")
	if err != nil {
		t.Fatalf("ResolveID(\"\"): %v", err)
	}
	want := pool.Default().ID()
	if got != want {
		t.Errorf("ResolveID(\"\") = %q, want %q (bootstrap id)", got, want)
	}
}

// TestPool_ResolveID_FullUUID: passing the bootstrap's full canonical UUID
// returns it unchanged.
func TestPool_ResolveID_FullUUID(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)

	id := pool.Default().ID()
	got, err := pool.ResolveID(string(id))
	if err != nil {
		t.Fatalf("ResolveID(%q): %v", id, err)
	}
	if got != id {
		t.Errorf("ResolveID(%q) = %q, want %q", id, got, id)
	}
}

// TestPool_ResolveID_UniquePrefix: a unique prefix (down to 1 char) resolves
// to the bootstrap id. AC: no minimum prefix length is enforced.
func TestPool_ResolveID_UniquePrefix(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	id := pool.Default().ID()

	for _, n := range []int{1, 4, 8, 16, 35} {
		prefix := string(id)[:n]
		got, err := pool.ResolveID(prefix)
		if err != nil {
			t.Errorf("ResolveID(%q) (n=%d): %v", prefix, n, err)
			continue
		}
		if got != id {
			t.Errorf("ResolveID(%q) (n=%d) = %q, want %q", prefix, n, got, id)
		}
	}
}

// TestPool_ResolveID_FullUUIDBeatsPrefix: when arg is an exact key, the
// short-circuit returns it even if the prefix scan would also find another
// session whose id has arg as a prefix. Constructed by inserting a second
// session whose id begins with the bootstrap id's full string.
func TestPool_ResolveID_FullUUIDBeatsPrefix(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)

	idA := pool.Default().ID()
	// idB starts with idA's full string. UUIDv4s are 36 chars so this is
	// synthetic — no real-world pool would contain such an entry. The point
	// is to exercise the short-circuit: passing idA must return idA even
	// though strings.HasPrefix(string(idB), string(idA)) is also true.
	idB := SessionID(string(idA) + "-extra")
	sessB := &Session{
		id:         idB,
		log:        pool.log,
		label:      "beta",
		pool:       pool,
		lcState:    stateActive,
		activeCh:   closedChan(),
		evictedCh:  make(chan struct{}),
		activateCh: make(chan struct{}, 1),
		evictCh:    make(chan struct{}, 1),
	}
	pool.mu.Lock()
	pool.sessions[idB] = sessB
	pool.mu.Unlock()

	got, err := pool.ResolveID(string(idA))
	if err != nil {
		t.Fatalf("ResolveID(%q): %v (expected idA to win via exact-match short-circuit)", idA, err)
	}
	if got != idA {
		t.Errorf("ResolveID(%q) = %q, want %q (exact match must win over prefix overlap)", idA, got, idA)
	}
}

// TestPool_ResolveID_AmbiguousPrefix: a prefix shared by two sessions returns
// ErrAmbiguousSessionID, and the wrapped message lists both `<uuid> (<label>)`
// pairs sorted by SessionID ascending.
func TestPool_ResolveID_AmbiguousPrefix(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)

	// Replace the bootstrap and add a second session, both sharing the
	// 8-char prefix "deadbeef". Synthetic non-UUID strings are fine — the
	// resolver only treats SessionIDs as opaque strings.
	bootID := pool.Default().ID()
	pool.mu.Lock()
	bootSess := pool.sessions[bootID]
	delete(pool.sessions, bootID)
	idA := SessionID("deadbeef-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	idB := SessionID("deadbeef-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	bootSess.id = idA
	// Leave bootstrap label empty so the synthetic-bootstrap-label
	// substitution path is exercised.
	bootSess.label = ""
	pool.sessions[idA] = bootSess
	pool.bootstrap = idA

	sessB := &Session{
		id:         idB,
		log:        pool.log,
		label:      "beta",
		pool:       pool,
		lcState:    stateActive,
		activeCh:   closedChan(),
		evictedCh:  make(chan struct{}),
		activateCh: make(chan struct{}, 1),
		evictCh:    make(chan struct{}, 1),
	}
	pool.sessions[idB] = sessB
	pool.mu.Unlock()

	got, err := pool.ResolveID("deadbeef")
	if got != "" {
		t.Errorf("ResolveID(ambiguous) returned id %q, want empty", got)
	}
	if !errors.Is(err, ErrAmbiguousSessionID) {
		t.Fatalf("ResolveID(ambiguous) err = %v, want errors.Is == ErrAmbiguousSessionID", err)
	}
	msg := err.Error()
	wantA := string(idA) + " (bootstrap)"
	wantB := string(idB) + " (beta)"
	if !strings.Contains(msg, wantA) {
		t.Errorf("err message missing %q; got: %q", wantA, msg)
	}
	if !strings.Contains(msg, wantB) {
		t.Errorf("err message missing %q; got: %q", wantB, msg)
	}
	// Sorted by SessionID ascending: idA ("deadbeef-aaaa…") < idB ("deadbeef-bbbb…").
	idxA := strings.Index(msg, wantA)
	idxB := strings.Index(msg, wantB)
	if idxA < 0 || idxB < 0 || idxA >= idxB {
		t.Errorf("expected %q before %q in err message: %q", wantA, wantB, msg)
	}
}

// TestPool_ResolveID_NoMatch: neither a full-UUID-shaped non-match nor an
// arbitrary unrelated string match anything in the pool — both return
// ErrSessionNotFound.
func TestPool_ResolveID_NoMatch(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)

	for _, arg := range []string{
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		"zzzz",
	} {
		got, err := pool.ResolveID(arg)
		if got != "" {
			t.Errorf("ResolveID(%q) returned id %q, want empty", arg, got)
		}
		if !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("ResolveID(%q) err = %v, want ErrSessionNotFound", arg, err)
		}
	}
}

// TestPool_ResolveID_RaceWithList: concurrent ResolveID + List goroutines must
// be -race clean. The assertion is purely "go test -race is silent."
func TestPool_ResolveID_RaceWithList(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	id := pool.Default().ID()
	prefix := string(id)[:8]

	const goroutines = 16
	const iters = 100

	var wg sync.WaitGroup
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if _, err := pool.ResolveID(prefix); err != nil {
					t.Errorf("ResolveID: %v", err)
					return
				}
			}
		}()
	}
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				for _, info := range pool.List() {
					_ = info.ID
				}
			}
		}()
	}
	wg.Wait()
}
