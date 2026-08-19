package sessions

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// fakeRunner is a no-op Runner that never spawns a PTY, so it is safe on CI
// runners with no terminal. It stands in for *supervisor.Supervisor when a test
// supplies a Config.RunnerFactory, letting us prove the factory is invoked at
// every construction site without starting a real supervisor.
type fakeRunner struct{}

func (fakeRunner) State() State { return State{} }

func (fakeRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return nil
}

func (fakeRunner) WaitForPTY(ctx context.Context) error { return nil }

// Run blocks until ctx is cancelled so a fakeRunner driven through the session
// lifecycle looks "running" rather than exiting immediately. The seam tests
// below never start the lifecycle, so this body is dormant; it is written this
// way to keep fakeRunner a correct, reusable double.
func (fakeRunner) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func (fakeRunner) Restart(args []string) {}

func (fakeRunner) SetSpawnArgs(args []string) {}

func (fakeRunner) RevokeBypass() error { return nil }

// TestRunnerFactory_InvokedAtEveryConstructionSite covers AC-4: a non-nil
// Config.RunnerFactory is invoked in place of the default runner at BOTH construction
// sites — the bootstrap (Pool.New) and the per-session create (Pool.buildSession,
// shared by CreateIn and GetOrCreateIn) — proving the seam is genuinely threaded
// and not merely declared. It also proves (#1108) that each site exposes a
// construction-safe, non-empty RunnerConfig.SessionID equal to the id that
// site mints — the value a stream-json factory reads at construction.
//
// ClaudeBin points at a path that does not exist: the terminal supervisor did an
// exec.LookPath at construction, so if the factory were NOT wired the fallback to
// the terminal supervisor would fail LookPath and New would error. Success plus the call
// count together prove the factory replaced the default runner at each site.
func TestRunnerFactory_InvokedAtEveryConstructionSite(t *testing.T) {
	var calls int
	var seenSessionID []string // cfg.SessionID captured at each factory invocation
	factory := func(cfg RunnerConfig) (Runner, error) {
		calls++
		seenSessionID = append(seenSessionID, cfg.SessionID)
		return fakeRunner{}, nil
	}

	// Bootstrap construction site. Empty RegistryPath disables persistence and
	// empty ClaudeSessionsDir skips the transcript-resolver path, so New reaches
	// the newRunner call with no I/O beyond the per-session MCP settings file.
	p, err := New(Config{
		Bootstrap:     SessionConfig{ClaudeBin: "/nonexistent/claude-should-never-be-execd"},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		RunnerFactory: factory,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if calls != 1 {
		t.Fatalf("after New: factory invoked %d times, want 1 (bootstrap site)", calls)
	}
	// AC-4: the bootstrap site exposes the already-minted bootstrap id, non-empty
	// and equal to what BootstrapID() reports (nothing rotates it in this test).
	if got, want := seenSessionID[0], string(p.BootstrapID()); got == "" || got != want {
		t.Fatalf("bootstrap site cfg.SessionID = %q, want non-empty %q (== BootstrapID)", got, want)
	}

	// Create construction site. buildSession is the shared funnel for CreateIn
	// and GetOrCreateIn; calling it directly targets the second newRunner site
	// with no lifecycle goroutine to schedule.
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := p.buildSession(id, "label", "", SessionSettings{}); err != nil {
		t.Fatalf("buildSession: %v", err)
	}
	if calls != 2 {
		t.Fatalf("after buildSession: factory invoked %d times, want 2 (bootstrap + create sites)", calls)
	}
	// AC-4: the per-session site exposes the id parameter it was handed, non-empty.
	if got, want := seenSessionID[1], string(id); got == "" || got != want {
		t.Fatalf("per-session site cfg.SessionID = %q, want non-empty %q (== buildSession id)", got, want)
	}
}

// testRunnerFactory supplies the no-op runner above. Since #1348 a factory is
// mandatory on Config — the nil default used to build the terminal supervisor,
// which meant a test that named no factory silently spawned a real claude under
// a PTY. Several pool tests did exactly that and were slow and CI-hostile for
// the reason. Naming this explicitly at each call site is the point: a pool test
// that wants a real spawn now has to say so.
func testRunnerFactory(cfg RunnerConfig) (Runner, error) {
	return &lifecycleRunner{workDir: cfg.WorkDir}, nil
}

// lifecycleRunner is fakeRunner plus the two observable facts the pool's
// lifecycle tests assert on: a phase that moves starting → running, and a
// non-zero child pid once Run is in flight.
//
// Those tests used to get both by spawning /bin/sh under a real terminal
// supervisor and reading the actual child's pid. That coupled pool bookkeeping
// tests to process spawning, made them slow, and required a tty. The pid here is
// this process's own, which is deliberately a real live pid: anything asserting
// "there is a child" stays honest, and anything trying to signal it would be
// caught immediately rather than silently no-oping on a made-up number.
type lifecycleRunner struct {
	mu        sync.Mutex
	running   bool
	startedAt time.Time
	pid       int
	// workDir lets Restart record the recomposed argv against the same key the
	// construction-time recorder uses, so a restart is observable the way a
	// respawned child's argv used to be.
	workDir   string
	sessionID string
	// readyOnce/readyCh give WaitForPTY real readiness semantics: it must not
	// return until a child actually exists. Returning immediately made the
	// rotation-watcher test race, because it proceeded to plant a transcript
	// before the bootstrap had a pid for the watcher to attribute it against.
	readyOnce sync.Once
	readyCh   chan struct{}
	// restarts records the argv of every Restart call, in order. It replaces the
	// old arrangement where a real child wrote its own argv to a file and the test
	// read it back — the same assertion, one process fewer.
	restarts [][]string
	// setArgs records the argv of every SetSpawnArgs call, in order, and is kept
	// SEPARATE from restarts because the two are different events: an install
	// changes what the next spawn will run, a restart also ends the child running
	// now. It deliberately does not route through recordArgv either — that is the
	// restart-observability channel the spawn-argv tests read back, so an assertion
	// on an install must read setArgs (via spawnArgSets) and never waitArgv, which
	// would return the stale construction argv instead of timing out. #1580 left
	// this recording here for its first caller; #1581's in-band branch is it.
	setArgs [][]string
	// writes records the payload of every WriteUserTurn call, in order — the
	// in-band delivery channel #1581 introduced, whose commands are otherwise
	// invisible to a test. Recorded on EVERY path including no-live-child: it is
	// the production runner, not the pool, that refuses a write when no child is
	// bound, and the pool's contract is to attempt the write unconditionally.
	writes [][]byte
	// revokes counts every RevokeBypass call — the in-band bypass revocation
	// #1604 routes through the same branch. A COUNT, not a bool: an assertion has
	// to be able to tell "exactly one" from "two" and catch a double-send.
	// Recorded on EVERY call including no-live-child, for the same reason writes
	// is: the production runner, not the pool, is what refuses when no child is
	// bound, and the pool's contract is to attempt the revocation unconditionally.
	revokes int
}

func (r *lifecycleRunner) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return State{Phase: PhaseRunning, ChildPID: r.pid, StartedAt: r.startedAt}
	}
	return State{Phase: PhaseStarting}
}

func (r *lifecycleRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	r.mu.Lock()
	r.writes = append(r.writes, append([]byte(nil), payload...))
	r.mu.Unlock()
	return nil
}

func (r *lifecycleRunner) ready() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readyCh == nil {
		r.readyCh = make(chan struct{})
	}
	return r.readyCh
}

func (r *lifecycleRunner) WaitForPTY(ctx context.Context) error {
	select {
	case <-r.ready():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run owns a real, harmless child for its whole life. A pure in-memory double
// was not enough: the pool's Remove tests assert that a child is actually gone
// afterwards, and reporting this process's own pid made them "pass" by finding
// the test binary alive. A real sleep is the smallest thing that keeps those
// assertions honest, and it is the only place in these tests that still needs a
// process at all.
func (r *lifecycleRunner) Run(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "/bin/sleep", "3600")
	if err := cmd.Start(); err != nil {
		return err
	}
	// Recorded HERE rather than at construction, because the pool builds a runner
	// speculatively on the get-or-create take path and throws it away when the id
	// already exists. The old spawn-dir tests observed a real child's cwd, so a
	// discarded runner left no trace; recording on Run keeps that distinction.
	recordRan(r.workDir, r.sessionID)
	ch := r.ready()
	r.readyOnce.Do(func() { close(ch) })
	r.mu.Lock()
	r.running = true
	r.startedAt = time.Now().UTC()
	r.pid = cmd.Process.Pid
	r.mu.Unlock()

	<-ctx.Done()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	r.mu.Lock()
	r.running = false
	r.pid = 0
	r.mu.Unlock()
	return ctx.Err()
}

func (r *lifecycleRunner) Restart(args []string) {
	r.mu.Lock()
	r.restarts = append(r.restarts, append([]string(nil), args...))
	wd := r.workDir
	r.mu.Unlock()
	recordArgv(wd, args)
}

func (r *lifecycleRunner) SetSpawnArgs(args []string) {
	r.mu.Lock()
	r.setArgs = append(r.setArgs, append([]string(nil), args...))
	r.mu.Unlock()
}

func (r *lifecycleRunner) RevokeBypass() error {
	r.mu.Lock()
	r.revokes++
	r.mu.Unlock()
	return nil
}

// restartArgs, spawnArgSets, userTurns and revokeCount are the read side of the
// four records above. Each takes r.mu and deep-copies, because the lifecycle
// goroutine writes while the test goroutine reads and every assertion must stay
// -race clean. userTurns converts to string on read so a delivery assertion reads
// as the command text it is.
func (r *lifecycleRunner) restartArgs() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneArgvRecords(r.restarts)
}

func (r *lifecycleRunner) spawnArgSets() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneArgvRecords(r.setArgs)
}

func (r *lifecycleRunner) userTurns() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.writes))
	for _, w := range r.writes {
		out = append(out, string(w))
	}
	return out
}

func (r *lifecycleRunner) revokeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revokes
}

func cloneArgvRecords(recs [][]string) [][]string {
	out := make([][]string, 0, len(recs))
	for _, rec := range recs {
		out = append(out, append([]string(nil), rec...))
	}
	return out
}

// runnerDouble returns the lifecycleRunner backing session id, so a test can read
// what the pool actually called on the supervisor. Prefer it over the `done`
// sentinel for "did a respawn happen?": doneAppears cannot observe one under this
// double (see its own doc), whereas restartArgs records every Restart call.
func runnerDouble(t *testing.T, pool *Pool, id SessionID) *lifecycleRunner {
	t.Helper()
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup(%s): %v", id, err)
	}
	r, ok := sess.Runner().(*lifecycleRunner)
	if !ok {
		t.Fatalf("session %s is backed by %T, want *lifecycleRunner", id, sess.Runner())
	}
	return r
}
