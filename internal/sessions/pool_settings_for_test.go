package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// registerSessionWithSettings registers a non-bootstrap session carrying the
// given settings and returns its id. It is spawnMintedWithSettings' build-and-
// register step with the supervise/Activate tail dropped: a read test needs no
// lifecycle goroutine, and saveLocked is skipped too because SettingsFor never
// touches disk. Pool.New restores only the bootstrap entry from the registry, so
// a two-session fixture cannot be built by pre-writing two registry entries.
func registerSessionWithSettings(t *testing.T, pool *Pool, settings SessionSettings) SessionID {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	sess, err := pool.buildSession(id, "", t.TempDir(), settings)
	if err != nil {
		t.Fatalf("buildSession: %v", err)
	}
	pool.mu.Lock()
	pool.sessions[id] = sess
	pool.mu.Unlock()
	return id
}

// TestPool_SettingsFor_KnownID (AC-1): a session named by its own id reports
// exactly its persisted triple, with a nil error. Baseline row — green under
// every mutant the rows below target, which is why it cannot stand alone.
func TestPool_SettingsFor_KnownID(t *testing.T) {
	t.Parallel()
	want := SessionSettings{Model: "opus", Effort: "high", YOLO: true}
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolWithSettings(t, regPath, want)

	got, err := pool.SettingsFor(pool.BootstrapID())
	if err != nil {
		t.Fatalf("SettingsFor(bootstrap): err = %v, want nil", err)
	}
	if got != want {
		t.Errorf("SettingsFor(bootstrap): got %+v, want %+v", got, want)
	}
}

// TestPool_SettingsFor_PerSessionNotBootstrap (AC-1) is the ticket's whole
// point: a non-bootstrap session reports ITS settings, not the bootstrap's.
// Sole RED for an implementation that ignores its id argument and reads
// p.sessions[p.bootstrap] the way DefaultSettings does.
func TestPool_SettingsFor_PerSessionNotBootstrap(t *testing.T) {
	t.Parallel()
	bootWant := SessionSettings{Model: "opus", Effort: "high", YOLO: true}
	secondWant := SessionSettings{Model: "sonnet", Effort: "low", YOLO: false}
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolWithSettings(t, regPath, bootWant)
	secondID := registerSessionWithSettings(t, pool, secondWant)

	got, err := pool.SettingsFor(secondID)
	if err != nil {
		t.Fatalf("SettingsFor(second): err = %v, want nil", err)
	}
	if got != secondWant {
		t.Errorf("SettingsFor(second): got %+v, want %+v", got, secondWant)
	}

	boot, err := pool.SettingsFor(pool.BootstrapID())
	if err != nil {
		t.Fatalf("SettingsFor(bootstrap): err = %v, want nil", err)
	}
	if boot != bootWant {
		t.Errorf("SettingsFor(bootstrap): got %+v, want %+v", boot, bootWant)
	}
	if got == boot {
		t.Errorf("SettingsFor returned the same triple %+v for two sessions with different settings", got)
	}
}

// TestPool_SettingsFor_ZeroSettingsIsFound (AC-2, first half): a real session
// whose settings happen to be at their zero values is reported as FOUND — the
// zero value and a nil error. The value assertion alone proves nothing here, so
// the nil error is asserted explicitly. Pairs with the unknown-id row.
func TestPool_SettingsFor_ZeroSettingsIsFound(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolWithSettings(t, regPath, SessionSettings{})

	got, err := pool.SettingsFor(pool.BootstrapID())
	if err != nil {
		t.Fatalf("SettingsFor(bootstrap at defaults): err = %v, want nil (the session exists)", err)
	}
	if got != (SessionSettings{}) {
		t.Errorf("SettingsFor(bootstrap at defaults): got %+v, want zero value", got)
	}
}

// TestPool_SettingsFor_UnknownID (AC-2, second half): a well-formed id the pool
// does not know is ErrSessionNotFound plus the zero value. The fixture's
// bootstrap carries NON-zero settings on purpose, so this is also RED for an
// implementation that falls back to the bootstrap on a map miss.
func TestPool_SettingsFor_UnknownID(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolWithSettings(t, regPath, SessionSettings{Model: "opus", Effort: "high", YOLO: true})

	unknown := SessionID("00000000-0000-0000-0000-000000000000")
	got, err := pool.SettingsFor(unknown)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("SettingsFor(unknown): err = %v, want ErrSessionNotFound", err)
	}
	if got != (SessionSettings{}) {
		t.Errorf("SettingsFor(unknown): got %+v, want zero value", got)
	}
}

// TestPool_SettingsFor_EmptyIDNotFound (AC-3): the empty id is NOT special-cased
// to the bootstrap — it misses the map like any other unknown id. Sole RED for a
// Lookup-shaped `if id == "" { return bootstrap }` branch. The fixture choice is
// load-bearing: against a zero-value &Pool{} this row is vacuously green under
// both implementations, so the pool here has a bootstrap carrying settings that
// are distinguishable from the zero value.
func TestPool_SettingsFor_EmptyIDNotFound(t *testing.T) {
	t.Parallel()
	boot := SessionSettings{Model: "opus", Effort: "high", YOLO: true}
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolWithSettings(t, regPath, boot)

	got, err := pool.SettingsFor("")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf(`SettingsFor(""): err = %v, want ErrSessionNotFound`, err)
	}
	if got == boot {
		t.Errorf(`SettingsFor(""): returned the bootstrap's %+v — the empty id must not resolve to the bootstrap`, got)
	}
	if got != (SessionSettings{}) {
		t.Errorf(`SettingsFor(""): got %+v, want zero value`, got)
	}
}

// TestPool_SettingsFor_ReadWriteAgreeOnEmptyID encodes AC-3's stated rationale
// directly: if the two sides disagree on what "" means, a caller reads the
// bootstrap and writes nowhere. Goes RED if a later ticket special-cases "" on
// either side.
func TestPool_SettingsFor_ReadWriteAgreeOnEmptyID(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolWithSettings(t, regPath, SessionSettings{Model: "opus", Effort: "high", YOLO: true})

	if _, err := pool.SettingsFor(""); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf(`SettingsFor(""): err = %v, want ErrSessionNotFound`, err)
	}
	if err := pool.UpdateSettings("", SettingsUpdate{Model: ptr("haiku")}); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf(`UpdateSettings(""): err = %v, want ErrSessionNotFound`, err)
	}
}

// TestPool_SettingsFor_MutatesNothing (AC-4, second half): the read persists
// nothing and touches no map entry, across a known id, an unknown id and the
// empty id. Registry bytes and mtime plus Pool.List's snapshot must all be
// unchanged — the idiom TestPool_UpdateSettings_UnknownID uses. RED for any
// implementation that reaches saveLocked or writes p.sessions.
func TestPool_SettingsFor_MutatesNothing(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolWithSettings(t, regPath, SessionSettings{Model: "opus", Effort: "high", YOLO: true})

	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	beforeStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	beforeList := pool.List()

	if _, err := pool.SettingsFor(pool.BootstrapID()); err != nil {
		t.Fatalf("SettingsFor(bootstrap): err = %v, want nil", err)
	}
	if _, err := pool.SettingsFor(SessionID("00000000-0000-0000-0000-000000000000")); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SettingsFor(unknown): err = %v, want ErrSessionNotFound", err)
	}
	if _, err := pool.SettingsFor(""); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf(`SettingsFor(""): err = %v, want ErrSessionNotFound`, err)
	}

	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Errorf("registry bytes changed across reads:\nbefore=%s\nafter =%s", beforeBytes, afterBytes)
	}
	afterStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed across reads: before=%v after=%v",
			beforeStat.ModTime(), afterStat.ModTime())
	}
	if afterList := pool.List(); !reflect.DeepEqual(beforeList, afterList) {
		t.Errorf("List changed across reads:\nbefore=%+v\nafter =%+v", beforeList, afterList)
	}
}

// TestPool_SettingsFor_ConcurrentWithWriter (AC-4, first half) runs readers
// against concurrent UpdateSettings writers on one pool. It is the only row that
// can catch the hazard AC-4 names: an implementation delegating to
// DefaultSettings double-acquires p.mu.RLock, and Go's RWMutex is not reentrant,
// so it hangs as soon as a writer queues between the two acquisitions — which
// surfaces here as a test timeout rather than a failed assertion.
//
// Honest about what each half proves: the deadlock detection is PROBABILISTIC
// (it needs a writer to interleave between the two acquisitions), while the
// race-detector coverage of the settings field is deterministic under -race.
// UpdateSettings is safe on a non-Run pool with lifecycleRunner — the existing
// UpdateSettings tests rely on the same property.
func TestPool_SettingsFor_ConcurrentWithWriter(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolWithSettings(t, regPath, SessionSettings{Model: "opus", Effort: "high"})
	id := pool.BootstrapID()

	models := []string{"opus", "sonnet", "haiku"}
	const goroutines, iterations = 4, 50

	var wg sync.WaitGroup
	for w := 0; w < goroutines; w++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if _, err := pool.SettingsFor(id); err != nil {
					t.Errorf("SettingsFor(bootstrap) under concurrency: err = %v, want nil", err)
					return
				}
			}
		}()
		go func(base int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m := models[(base+i)%len(models)]
				if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr(m)}); err != nil {
					t.Errorf("UpdateSettings(bootstrap) under concurrency: err = %v, want nil", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
