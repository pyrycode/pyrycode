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
	// its one caller: the in-band branch installs through it and delivers the live
	// change as set_model, /effort, or set_permission_mode instead of a respawn.
	// See (*streamsup.Runner).SetSpawnArgs for the full contract —
	// notably that the argv is installed verbatim, with validation staying upstream
	// in Session.spawnArgs.
	SetSpawnArgs(args []string)
	// SetModel switches the LIVE child's model without killing it and without
	// creating a user turn. The implementation writes one set_model control
	// request to the held-open stream. Model validation belongs to the caller's
	// trust boundary; errors must not echo the model value.
	//
	// It is on this interface because its consumer is deliverSettingsInBand. A
	// capability assertion there would fail open and report a persisted update as
	// live-applied even when the runner could not send it.
	SetModel(model string) error
	// SetSpawnPermissionMode installs the posture the runner's NEXT and every later
	// spawn asserts to its child in-band, without touching the running one (#2064).
	// It is SetSpawnArgs' posture twin and stands in the same relation to
	// SetPermissionMode below that SetSpawnArgs stands in to Restart: one changes
	// what runs next, the other changes what is running now.
	//
	// Pool.UpdateSettings is its one caller, and it calls it on the line ABOVE its
	// branch split — the single point both branches pass through — because neither
	// branch rebuilds the runner. Without it a runner keeps asserting the posture it
	// was CONSTRUCTED with, so a crash-respawn after an operator's change would
	// re-assert the startup posture and can LOOSEN one that was since tightened.
	//
	// The two shapes that look sufficient and are not. Piggybacking the install on
	// SetPermissionMode misses every update that takes the RESTART branch, which
	// never calls that method — since #2066 that is a Model or Effort cleared to ""
	// rather than an escalation, because the escalation now is in-band-deliverable
	// and does reach it; the miss moved, it did not go away, and a piggybacked
	// install would still let a crash-respawn re-assert a stale posture. Deriving the
	// posture from the installed argv was exact until #2065, which takes the mode out
	// of the launch argv entirely.
	//
	// mode is installed VERBATIM AND UNVALIDATED — bypassPermissions included, the
	// empty string included. This is not an operator-input path: callers pass an
	// already-canonical stored posture, and the vocabulary gate that matters runs at
	// the SPAWN, where a mode outside the runner's allow-list means nothing is written
	// and nothing is gated. It returns nothing and cannot fail; it is non-blocking and
	// takes no lock the sessions layer holds.
	//
	// It is ON this interface for SetPermissionMode's reason exactly, and the stakes
	// are higher here: its consumer sits inside internal/sessions, where a structural
	// assertion fails OPEN, and failing open means a respawned child silently
	// asserting a posture the operator has already changed.
	SetSpawnPermissionMode(mode string)
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
	// mode is refused unless it is in the runner's closed allow-list — since #2066
	// "default", "acceptEdits", "plan", "auto", "dontAsk" AND "bypassPermissions".
	// Re-granting bypass arrives HERE now rather than on the respawn path: claude used
	// to gate the escalation on the launch argv, #2065 put that flag on every argv,
	// and #2060 measured claude accepting the re-escalation on such a child at
	// 2.1.239. What the list still refuses, by NON-MEMBERSHIP rather than by a
	// deny-list entry, is every mode claude will not parse — near misses of the
	// escalation included.
	//
	// A SUCCESSFUL write retargets the runner's posture gate, so a posture the child
	// NAKs holds that session's turns until another in-band change retargets it. That
	// window has applied to every in-band posture change since #2064; the escalation
	// joined the class rather than creating it, and the operator's remedy — naming any
	// other mode — reaches this same method.
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
	// BeginTeardown arms the runner's write-refusal gate for a DELIBERATE KILL this
	// package is about to perform (#1513): from the call until the next child binds,
	// every WriteUserTurn is refused with the runner's retryable no-live-child error
	// instead of being written into the child that is about to die. Without it the
	// write lands in the doomed child's stdin pipe and RETURNS NIL, msgqueue reads
	// nil as a confirmed commit and drops the queue head, and the message dies
	// unread — no retry, no session_error, and queue_state reporting it delivered.
	//
	// Three callers, each arming strictly BEFORE the kill it pairs with and before
	// anything that publishes the teardown to clients: both eviction arms of
	// Session.runActive (the idle timer, and the cap/force evictCh that Pool.Remove
	// drives through Session.Evict), and Pool.UpdateSettings' restart branch — the
	// in-band branch beside it MUST NOT call this, since it tears no child down and
	// an arm there would refuse turns for a delivery that kills nothing.
	//
	// It is the deliberate-teardown twin of the runner's own new_session rotation
	// arm, NOT a second caller of it, and the difference is the release rule: a
	// rotation arm must survive a Restart-driven or crash respawn, while these
	// teardowns END IN exactly such a respawn and must be released by it. The
	// implementation owns that distinction; this seam only promises that any
	// successor child ends the window, so a stray arm cannot wedge a session.
	//
	// It returns nothing and cannot fail. It is non-blocking, takes no lock this
	// package holds, and makes no call-out — so it cannot delay the kill that
	// follows it — and it is safe from any goroutine.
	//
	// It is ON this interface for SetSpawnPermissionMode's reason exactly, and the
	// stakes are the same shape: its consumers sit inside internal/sessions, which
	// must not import internal/streamsup (see Pool.deliverSettingsInBand), and a
	// structural assertion there fails OPEN. An unmatched arm would be a silent
	// no-op leaving the teardown ungated while every layer reported success — which
	// is precisely the silent loss this method exists to close. The interface method
	// makes a runner that cannot arm a build failure instead.
	BeginTeardown()
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
