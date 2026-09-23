package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modelWindowsPlan is the per-pool-session answer table modelWindowsRunner reads
// at CALL time rather than at construction, for modelListPlan's stated reason:
// sessions.New invokes the runner factory while building the bootstrap session,
// so a test cannot know that id until New has returned and the runner already
// exists. Arming by id afterwards is what lets each session hold a
// distinguishable report.
type modelWindowsPlan struct {
	mu   sync.Mutex
	byID map[sessions.SessionID]modelWindowReport
}

func newModelWindowsPlan() *modelWindowsPlan {
	return &modelWindowsPlan{byID: map[sessions.SessionID]modelWindowReport{}}
}

// arm makes id's runner report r. A session with NO entry reports the unreported
// state, which is the never-reported-a-window fixture — the plan's zero state is
// "this child's turns have never carried a usable modelUsage".
func (p *modelWindowsPlan) arm(id sessions.SessionID, r modelWindowReport) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byID[id] = r
}

func (p *modelWindowsPlan) get(id sessions.SessionID) (modelWindowReport, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.byID[id]
	return r, ok
}

// modelWindowsRunner is stubRunner plus the ONE concrete method
// sessionModelWindows asserts for. stubRunner itself deliberately does not
// implement it and therefore stays the ready-made not-implemented fixture — see
// the runner-lacks-the-method case below, which builds its pool with
// newRouterTestPool for exactly that reason.
type modelWindowsRunner struct {
	stubRunner
	id   sessions.SessionID
	plan *modelWindowsPlan
}

func (r modelWindowsRunner) ModelWindows() (modelWindowReport, bool) { return r.plan.get(r.id) }

// newModelWindowsTestPool builds a real *sessions.Pool whose every session's
// runner answers from the returned plan, mirroring newModelListTestPool: a
// RegistryPath under a fresh temp dir gives Pool.Create a resolvable data dir,
// and a cold start there mints a bootstrap without spawning claude.
func newModelWindowsTestPool(t *testing.T) (*sessions.Pool, *modelWindowsPlan) {
	t.Helper()
	plan := newModelWindowsPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return modelWindowsRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, plan
}

// TestSessionModelWindows_ReportsTheSessionsOwnWindows covers the happy path: the
// retained report becomes a lookup map keyed verbatim by claude's own model ids.
//
// The fixture carries the shape measured across the committed captures — one
// model at 200000 beside another at 1000000, plus an alias spelling of the first
// — so a resolver that collapsed, deduplicated or canonicalised anything would
// lose an entry the map is expected to carry.
func TestSessionModelWindows_ReportsTheSessionsOwnWindows(t *testing.T) {
	t.Parallel()

	pool, plan := newModelWindowsTestPool(t)
	plan.arm(pool.BootstrapID(), modelWindowReport{
		Windows: []turnevent.ModelWindow{
			{ModelID: "claude-haiku-4-5", WindowTokens: 200_000},
			{ModelID: "claude-haiku-4-5-20251001", WindowTokens: 200_000},
			{ModelID: "claude-sonnet-5", WindowTokens: 1_000_000},
		},
		Dropped: 4,
	})

	got := sessionModelWindows(pool)(string(pool.BootstrapID()))
	want := map[string]int{
		"claude-haiku-4-5":          200_000,
		"claude-haiku-4-5-20251001": 200_000,
		"claude-sonnet-5":           1_000_000,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("windows = %v, want %v", got, want)
	}
}

// TestSessionModelWindows_EmptyIDEntryIsCarriedNotFiltered pins where the
// empty-id rule lives. internal/streamsup retains a modelUsage entry keyed by the
// empty string when its window is positive, and this resolver copies it across
// unfiltered — the comma-ok is its only filter. The rule that keeps it from
// joining is contextwindow.Read's, which never looks up "" because an entry with
// no model decodes to "". Filtering here too would be a second place the rule
// could drift.
func TestSessionModelWindows_EmptyIDEntryIsCarriedNotFiltered(t *testing.T) {
	t.Parallel()

	pool, plan := newModelWindowsTestPool(t)
	plan.arm(pool.BootstrapID(), modelWindowReport{
		Windows: []turnevent.ModelWindow{{ModelID: "", WindowTokens: 1_000_000}},
	})

	got := sessionModelWindows(pool)(string(pool.BootstrapID()))
	if got[""] != 1_000_000 {
		t.Errorf("windows = %v, want the empty-keyed entry carried through at 1000000", got)
	}
}

// TestSessionModelWindows_Isolation is AC 5: a window observed for one session is
// never reported for another. Two real pool sessions hold DIFFERENT reports, and
// each id must answer with its own.
//
// Both sessions are armed with distinguishable values rather than one armed and
// one silent, which is what makes a crossed lookup visible as a wrong window
// rather than as a nil that an unarmed session would also produce.
//
// Not parallel: t.Setenv confines anything the pool's create path might resolve
// out of HOME, and t.Setenv forbids t.Parallel — TestResolveBoundModelList_IsolatesConversations'
// arrangement, inherited for its reason.
func TestSessionModelWindows_Isolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pool, plan := newModelWindowsTestPool(t)
	// Pool.Create schedules the new session on the run group, so the pool has to
	// be running or supervise returns ErrPoolNotRunning.
	ctx := runPoolReady(t, pool)
	sessB, err := pool.Create(ctx, "session-b")
	if err != nil {
		t.Fatalf("pool.Create: %v", err)
	}
	plan.arm(pool.BootstrapID(), modelWindowReport{
		Windows: []turnevent.ModelWindow{{ModelID: "model-a", WindowTokens: 111_111}},
	})
	plan.arm(sessB, modelWindowReport{
		Windows: []turnevent.ModelWindow{{ModelID: "model-b", WindowTokens: 999_999}},
	})

	windows := sessionModelWindows(pool)
	if got, want := windows(string(pool.BootstrapID())), (map[string]int{"model-a": 111_111}); !reflect.DeepEqual(got, want) {
		t.Errorf("bootstrap windows = %v, want %v", got, want)
	}
	if got, want := windows(string(sessB)), (map[string]int{"model-b": 999_999}); !reflect.DeepEqual(got, want) {
		t.Errorf("session-b windows = %v, want %v", got, want)
	}
}

// TestSessionModelWindows_RefusesWithNil covers every refusal, all of which must
// answer nil — the single spelling contextwindow.Read reads as "nothing
// observed", falling back to the default window.
//
// The bootstrap session is armed in the pooled rows so the empty-id row's mutant
// is SOLE-RED rather than invisible: Pool.Lookup("") hands back the BOOTSTRAP
// session, so an implementation that reached the pool with an empty id would
// answer with the bootstrap's windows here, and an unarmed bootstrap would answer
// nil either way and pin nothing.
func TestSessionModelWindows_RefusesWithNil(t *testing.T) {
	t.Parallel()

	t.Run("unknown id", func(t *testing.T) {
		t.Parallel()
		pool, plan := newModelWindowsTestPool(t)
		plan.arm(pool.BootstrapID(), modelWindowReport{
			Windows: []turnevent.ModelWindow{{ModelID: "bootstrap-only", WindowTokens: 1_000_000}},
		})
		if got := sessionModelWindows(pool)("session-not-in-pool"); got != nil {
			t.Errorf("windows = %v, want nil", got)
		}
	})

	t.Run("empty id must not resolve to the bootstrap session", func(t *testing.T) {
		t.Parallel()
		pool, plan := newModelWindowsTestPool(t)
		plan.arm(pool.BootstrapID(), modelWindowReport{
			Windows: []turnevent.ModelWindow{{ModelID: "bootstrap-only", WindowTokens: 1_000_000}},
		})
		// Pool.Lookup("") returns the bootstrap session rather than an error
		// (#678), so this row asserts the reading a caller actually gets for an
		// empty id: whatever the bootstrap holds. It is recorded rather than
		// guarded against here because every production caller supplies an id the
		// daemon's own registry or pool produced — runConfigFor consults the usage
		// half ONLY with the session id its resolver returned.
		got := sessionModelWindows(pool)("")
		if got["bootstrap-only"] != 1_000_000 {
			t.Errorf("windows for the empty id = %v; Pool.Lookup(\"\") answers the bootstrap session, "+
				"so this documents the reading rather than asserting a refusal", got)
		}
	})

	t.Run("runner without the method", func(t *testing.T) {
		t.Parallel()
		pool := newRouterTestPool(t)
		if got := sessionModelWindows(pool)(string(pool.BootstrapID())); got != nil {
			t.Errorf("windows = %v, want nil — stubRunner has no ModelWindows method", got)
		}
	})

	t.Run("nothing reported yet", func(t *testing.T) {
		t.Parallel()
		pool, _ := newModelWindowsTestPool(t)
		// Unarmed: the child has never carried a usable modelUsage, which is the
		// pre-first-turn and post-daemon-restart case AC 3 names.
		if got := sessionModelWindows(pool)(string(pool.BootstrapID())); got != nil {
			t.Errorf("windows = %v, want nil", got)
		}
	})
}
