package sessions

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// fakeRunner is a no-op Runner that never spawns a PTY, so it is safe on CI
// runners with no terminal. It stands in for *supervisor.Supervisor when a test
// supplies a Config.RunnerFactory, letting us prove the factory is invoked at
// every construction site without starting a real supervisor.
type fakeRunner struct{}

func (fakeRunner) State() supervisor.State { return supervisor.State{} }

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

// TestRunnerFactory_InvokedAtEveryConstructionSite covers AC-4: a non-nil
// Config.RunnerFactory is invoked in place of supervisor.New at BOTH construction
// sites — the bootstrap (Pool.New) and the per-session create (Pool.buildSession,
// shared by CreateIn and GetOrCreateIn) — proving the seam is genuinely threaded
// and not merely declared. It also proves (#1108) that each site exposes a
// construction-safe, non-empty supervisor.Config.SessionID equal to the id that
// site mints — the value a stream-json factory reads at construction.
//
// ClaudeBin points at a path that does not exist: supervisor.New does an
// exec.LookPath at construction, so if the factory were NOT wired the fallback to
// supervisor.New would fail LookPath and New would error. Success plus the call
// count together prove the factory replaced supervisor.New at each site.
func TestRunnerFactory_InvokedAtEveryConstructionSite(t *testing.T) {
	var calls int
	var seenSessionID []string // cfg.SessionID captured at each factory invocation
	factory := func(cfg supervisor.Config) (Runner, error) {
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
