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
	// SetPermissionMode switches the LIVE child's permission posture without
	// killing it, by writing one set_permission_mode control request carrying mode
	// on the stream the daemon already holds open (#2042 widened #1603's writer;
	// #1595 and #2041 measured the switch live). It is the generalisation of
	// #1604's revoke-only RevokeBypass, which this interface carried until #2043
	// rewrote Pool.deliverSettingsInBand to send an operator-chosen mode: the same
	// line, the same no-respawn delivery, with the mode as a parameter. Leaving
	// both on the seam would have let one revocation emit two identical control
	// requests, since the posture derivation makes a yolo:false update carry a
	// non-bypass mode of its own. (*streamsup.Runner) keeps its own RevokeBypass
	// and its package-level coverage; nothing outside that package calls it.
	//
	// mode is refused unless it is in the runner's closed allow-list — today
	// "default", "acceptEdits", "plan", "auto" and "dontAsk". The escalating mode is
	// refused by NON-MEMBERSHIP rather than by a deny-list entry, so it names no
	// literal and refuses every unanticipated spelling with it; re-granting bypass
	// stays on the respawn path, where claude gates it on the launch argv.
	//
	// The refusal is a distinct, PERMANENT error, and a caller must not treat it as
	// the retryable no-live-child error: the two are errors.Is-distinguishable and a
	// refused mode can never succeed on retry. Neither error carries the rejected
	// mode string, so a caller may log them verbatim — #833 keeps settings values
	// out of the daemon log and a permission mode is a settings value.
	//
	// The allow-list is a VOCABULARY gate, not an authorisation one: it answers
	// whether claude will parse the mode, never whether this caller may change this
	// session's posture. Three of its members loosen a child launched in the
	// daemon's default approval posture, and claude accepts them in-band. A caller
	// taking a mode from a wire frame owns that decision itself.
	//
	// It is ON this interface for SetSpawnArgs' reason exactly — its consumer sits
	// inside internal/sessions, where a structural assertion would fail open: an
	// unmatched arm is a silent no-op, leaving the child in the wrong posture
	// while UpdateSettings reports success, which is the exact regression the
	// in-band delivery exists to prevent. The interface method makes a runner that
	// cannot switch posture a build failure instead.
	//
	// Returns the runner's retryable no-live-child error when nothing is bound.
	// Safe from any goroutine.
	SetPermissionMode(mode string) error
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
