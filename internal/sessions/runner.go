package sessions

import "context"

// Runner is the narrow interface internal/sessions dispatches through to drive a
// supervised claude child (Session.sup). It covers exactly the methods the
// session lifecycle invokes on that field — no more — so the Streamrunner
// Interactive work (T4/T7) can inject an alternative runner behind the same
// lifecycle without widening the seam.
//
// There is exactly ONE production implementation: cmd/pyry's streamRunner
// adapter, wrapping *streamsup.Runner. The adapter exists because Go has no
// covariant return on interface satisfaction and the concrete runner's State
// returns streamsup.State, not sessions.State — see streamRunner's own doc. The
// compile-time proof lives with it (var _ sessions.Runner = streamRunner{} in
// cmd/pyry); this file declares no assertion, because the type it would assert
// about is not in this package. The PTY supervisor this doc used to name is gone
// — #1348 deleted internal/supervisor — and the only other implementations are
// the package's test doubles.
//
// Config.RunnerFactory has no default: it is REQUIRED, and a nil factory is a
// construction error out of New (see RunnerFactory). There is no implicit
// implementation to fall back to.
//
// Consumers that need methods off the concrete runner (cmd/pyry's interrupt /
// new_session wiring: Interrupt, RestartFresh, BeginRotation) reach
// *streamsup.Runner by type assertion off Session.Runner(), which deliberately
// keeps returning this interface. Those methods stay off Runner — adding them
// would be speculative surface; see the delegate docs in cmd/pyry's
// streamsup_runner.go for each one's dispatch.
type Runner interface {
	State() State
	WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
	WaitForPTY(ctx context.Context) error
	Run(ctx context.Context) error
	Restart(args []string)
	// SetSpawnArgs installs the argv the runner's NEXT spawn will use WITHOUT
	// terminating a running child — Restart's swap half on its own (#1580), for a
	// caller that needs to change what the session runs next without ending what it
	// is running now. Declining to call Restart is not that path: it loses the swap
	// outright, so the next spawn re-execs the stale argv. Pool.UpdateSettings is
	// its one caller: the in-band branch (#1581) installs through it and delivers
	// the change as a /model or /effort command instead of a respawn.
	// See (*streamsup.Runner).SetSpawnArgs for the full contract —
	// notably that the argv is installed verbatim, with validation staying upstream
	// in Session.spawnArgs.
	SetSpawnArgs(args []string)
}

// RunnerFactory constructs a Runner from a RunnerConfig. It is the injection seam
// on Config and it is MANDATORY: a nil Config.RunnerFactory is a construction
// error ("sessions: Config.RunnerFactory is required") out of New, not a fall-back
// to some default implementation. The PTY default this doc used to describe went
// away with internal/supervisor (#1348); production supplies cmd/pyry's
// newStreamRunnerFactory, and the tests supply their own doubles.
//
// The factory is invoked at every construction site (bootstrap in Pool.New and
// per-session in Pool.buildSession). A returned error propagates through each
// site's own wrap — "sessions: bootstrap runner: %w" in New, "sessions: create
// runner: %w" in buildSession. Neither says "supervisor": the shared
// "sessions: … supervisor: %w" this doc used to quote went away with
// internal/supervisor (#1348), same as the claims above.
type RunnerFactory func(cfg RunnerConfig) (Runner, error)
