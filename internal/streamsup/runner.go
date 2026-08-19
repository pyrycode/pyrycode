// Package streamsup supervises a long-lived headless claude child in
// stream-json mode: it spawns claude with
// --input-format stream-json --output-format stream-json --verbose, holds the
// child's stdin open across many turns, restarts the child on crash with an
// exponential-backoff-with-stability-reset ladder, resumes with --resume <id>
// (reusing the same on-disk session id, no fork), and shuts down cleanly
// (SIGTERM → SIGKILL grace plus a reap of claude's detached descendant Bash
// process groups).
//
// It is the stream-json sibling of internal/supervisor (the PTY path). Unlike
// the PTY path it binds no transcript: there is deliberately NO transcript
// tailing, no fsnotify, and no <uuid>.jsonl path resolution anywhere in this
// package — that is the whole point of the stream-json path (it structurally
// removes the bind-latency race the PTY path fought). The only filesystem
// canonicalisation done here is resolving WorkDir before spawn (macOS
// /tmp → /private/tmp symlink hygiene).
//
// This slice owns the process lifecycle only. Turn I/O — the stdin envelope
// writer and the stdout→turnevent parser — and the turncommit/idle/stall gates
// are follow-on slices that build on the seams here (Stdin() and Config.Stdout).
//
// Dependency direction: the package must not import
// github.com/pyrycode/pyrycode/internal/supervisor (the PTY helper) nor any of
// the internal/agentrun subpackages (streamrunner, ptyrunner, …). It imports
// the internal/agentrun root package (ReapDescendantGroups, ResolveWorkdir,
// ExitErrIsBenign) — the shared parent, not a sibling. Verify with:
//
//	go list -deps ./internal/streamsup/... | grep pyrycode/internal/supervisor
//
// Expected output: empty.
package streamsup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// killGrace is the SIGTERM → SIGKILL grace window applied via exec.Cmd.WaitDelay
// when ctx is cancelled. Mirrors streamrunner's value.
const killGrace = 5 * time.Second

// Supervisor backoff defaults, matching internal/supervisor.
const (
	defaultBackoffInitial = 500 * time.Millisecond
	defaultBackoffMax     = 30 * time.Second
	defaultBackoffReset   = 60 * time.Second
)

// Config configures a Runner. Required fields are validated in New; zero values
// for optional fields fall through to the documented defaults.
type Config struct {
	// ClaudeBin is the resolved path to the claude executable. Required;
	// New surfaces a not-found error via exec.LookPath.
	ClaudeBin string

	// WorkDir is the child process's working directory. Required. New resolves
	// it via agentrun.ResolveWorkdir (macOS /tmp → /private/tmp, the #989
	// symlink hazard); the resolved path is what cmd.Dir is set to and is the
	// only path canonicalisation this package performs.
	WorkDir string

	// SessionID is the caller-minted claude session id. Required (non-empty
	// check only — the pool owns minting and shape validation). The first spawn
	// passes --session-id <SessionID>; every respawn passes --resume <SessionID>
	// (reattach, append, no fork), so the on-disk id stays stable across a
	// kill-and-restart.
	SessionID string

	// Args is the caller-supplied pass-through argv (e.g. --model <m>). streamsup
	// owns only the fixed stream-json prefix and the id flag; everything else is
	// the caller's, mirroring supervisor.buildClaudeArgs. New clones it.
	Args []string

	// Stdout receives the child's stdout. Optional; nil discards it (a plain
	// exec.Cmd with Stdout == nil sends the child's stdout to /dev/null, so an
	// unconsumed stream never blocks the child). #1088's turnevent parser is the
	// sink that plugs in here.
	Stdout io.Writer

	// Stderr receives the child's stderr. Optional; nil discards it.
	Stderr io.Writer

	// Env is appended to os.Environ() in the child process. Optional; production
	// leaves it nil. Tests use it to thread the fake-child wiring.
	Env []string

	// Logger is used for lifecycle diagnostics. Optional; nil falls back to
	// slog.Default().
	Logger *slog.Logger

	// BackoffInitial, BackoffMax, BackoffReset configure the restart ladder.
	// Zero falls back to the supervisor defaults (500ms / 30s / 60s).
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffReset   time.Duration

	// OnChildExit is called once per COMPLETED SUPERVISION ITERATION: after the
	// spawn attempt finishes and before Run decides what to do next (shut down,
	// relaunch immediately, or back off). Optional — nil-checked at the fire site
	// and left nil by tests that omit it — but NON-NIL on every production
	// construction path since #1210: the sole tree-wide caller of New installs a
	// per-runner turn-busy clear here, so a conversation whose child dies mid-turn
	// stops being reported busy. Exported, unlike onSpawn, because that consumer
	// lives outside this package.
	//
	// The cardinality is per iteration, NOT per live child: it also fires when
	// the spawn failed during setup and no claude process ever launched
	// (spawnAndWait's started == false). Deliberate — no child existed, so no
	// turn of this runner's can be open, and the intended consumer's clear is
	// idempotent. A caller that needs "a real child died" cannot get it here.
	//
	// It fires on every exit path: a crash (which falls through to the backoff
	// ladder), a deliberate restart (which relaunches immediately), and a
	// shutdown. The shutdown case works because the call sits ABOVE Run's
	// post-spawn ctx.Err() return, which itself sits above the "claude exited"
	// log — a parent cancel fires the callback before Run returns.
	//
	// It runs synchronously on the Run goroutine with NO Runner lock held, so it
	// may call any Runner method (Restart, RestartFresh, Interrupt, Stdin,
	// State), but it must NOT block: the restart ladder is stalled until it
	// returns, delaying the respawn. Slow work belongs on a channel or a
	// goroutine. It must not panic either — there is no recover here (matching
	// onSpawn), so a panic takes the supervise loop down with it.
	//
	// It fires strictly AFTER the child has exited: spawnAndWait returns only
	// past cmd.Wait or a pre-launch error, never while claude is still running,
	// and Stdin() already reports no live child by then. It is NOT a drain
	// barrier, though — it carries no data out of the child, and bytes the dead
	// child already wrote may still be in flight in a downstream sink when it
	// fires. A consumer needing ordering against those events must get it from
	// that sink, not from this callback.
	OnChildExit func()

	// onSpawn is an unexported test seam, called once per spawn after cmd.Start
	// and after the stdin handle is stored, with the child's pid. Nil in
	// production. Lets a test observe "child N is up, Stdin() is live" without
	// polling.
	onSpawn func(pid int)
}

// Runner supervises the claude child lifecycle. Construct with New; drive with
// Run. The held-open stdin handle is exposed via Stdin.
type Runner struct {
	cfg     Config
	log     *slog.Logger
	workDir string // resolved absolute path (agentrun.ResolveWorkdir)

	// mu is a leaf mutex guarding WHICH CHILD, IF ANY, MAY RECEIVE A TURN: stdin
	// (the write end of the live child's StdinPipe) together with the rotation gate
	// (rotating + rotateGen). stdin is swapped at spawn/teardown by the Run
	// goroutine and read by Stdin() from #1088's writer goroutine, so every access
	// is serialised.
	//
	// The charter widened from "the stdin handle" to that broader statement at
	// #1330, deliberately: WriteUserTurn must answer "is a rotation in flight?" and
	// "which child's stdin?" as ONE question, because two acquisitions reintroduce
	// the defect at nanosecond width (read the flag false → a rotation arms → read
	// stdin → write into the doomed child). turnTarget answers both under this
	// single acquisition, so the race is structurally excluded rather than
	// narrowed; splitting it as a "simplification" reopens it silently. mu remains
	// a leaf — never held across a channel op, a log call or any other call-out.
	mu    sync.Mutex
	stdin io.WriteCloser

	// rotating reports that a new_session rotation is armed and no successor child
	// has bound yet. BeginRotation sets it strictly BEFORE the pool-side rotate()
	// re-keys the binding and fires its session_transition{clear}; setStdin clears
	// it when the fresh child binds. While it stands every WriteUserTurn refuses
	// with ErrNoLiveChild instead of writing into the child RestartFresh is about
	// to kill — the ~4 ms window on #1330's record, where the clear reaches clients
	// while the outgoing child is still alive and msgqueue reads the successful
	// write as a commit and drops the head.
	//
	// takeStdin deliberately does NOT clear it: the gate must survive the teardown,
	// which is the whole interval it exists for. Nor would releasing it when
	// RestartFresh returns suffice — that call returns after cancel(), while the
	// kill, cmd.Wait and the respawn all run later on the Run goroutine.
	//
	// Kept STRICTLY SEPARATE from rotatePending (restartMu), which the two fields
	// superficially resemble. rotatePending selects the next spawn's id FORM
	// (--session-id vs --resume); folding them together would leave an ABORTED
	// rotation's rotatePending set, so the next unrelated crash-respawn would spawn
	// --session-id <oldID> — first-run form, a fresh transcript — silently
	// discarding the conversation's history.
	rotating bool

	// rotateGen stamps each arm so a LOSING rotation's abort cannot disarm a
	// WINNER's gate. Two overlapping new_session frames are an ordinary shape, not
	// a contrivance (#1330's e2e re-sends the frame every ~250 ms): frame 2's
	// rotate() wins the re-key, frame 1's then fails ErrSessionNotFound
	// (`RotateForNewSession` — the ordinary outcome of losing that race)
	// and runs its abort, which unstamped would clear the arm frame 2 is holding
	// while frame 2's outgoing child is still alive. That reproduces the defect on
	// demand from two frames, and no test driving one rotation at a time can see
	// it. Monotonic and process-local: never persisted, never serialised, never
	// logged, and compared only against a value BeginRotation itself captured.
	rotateGen uint64

	// stateMu is a leaf mutex guarding state, the control-plane snapshot. The
	// Run goroutine writes it via updateState; State() reads it from any
	// goroutine. Kept separate from mu (a different concern with a different
	// access pattern: control-plane reader vs. Run-goroutine writer).
	stateMu sync.Mutex
	state   State

	// restartMu is a leaf mutex guarding the live-restart seam: args, iterCancel,
	// and the RestartFresh id-rotation pair (sessionID + rotatePending). The
	// streamsup analogue of supervisor's restartMu. args is the live spawn base
	// argv, swapped by Restart (with the kill) or SetSpawnArgs (without it), and
	// assigned in exactly one place — setArgsLocked; iterCancel is the current
	// spawn's cancel, so Restart/RestartFresh can force the child to exit. Kept
	// separate from mu and stateMu — the three swap/rotate entry points touch only
	// this mutex + restartCh (never a Pool lock), so the sessions layer can call
	// them after releasing Pool.mu with no lock-order concern.
	//
	// The charter is ONE ACQUISITION PER SPAWN SETUP (#1481): beginSpawn both reads
	// the spawn inputs (args + the id pair) and publishes iterCancel in a single
	// section, so a racing Restart/RestartFresh/SetSpawnArgs is serialised either
	// fully before it (the spawn observes the swap) or fully after it (it finds a
	// live cancel and tears that spawn down). Splitting the read from the publish
	// — which is what this loop did until #1481 — leaves a gap where iterCancel is
	// still nil from the previous iteration: the racer writes its rotation, cancels
	// NOTHING, and the spawn launches a live child under the pre-rotation id, for
	// that child's whole lifetime. Do not re-split it as a "simplification".
	restartMu  sync.Mutex
	args       []string
	iterCancel context.CancelFunc

	// sessionID is the live claude session id read by each spawn's buildArgs (via
	// beginSpawn). Seeded from cfg.SessionID in New; rotated to a new id by
	// RestartFresh. The mutable analogue of the immutable cfg.SessionID — that
	// field stays the construction-time input and New's non-empty validation
	// source; after construction the live id is r.sessionID.
	sessionID string

	// rotatePending is set by RestartFresh and consumed once by beginSpawn: it
	// re-arms first-run form so the next spawn uses --session-id <newID> (a fresh
	// transcript), not --resume <newID>. firstRun stays Run-goroutine-private;
	// rotatePending is the only cross-goroutine signal that re-arms it.
	rotatePending bool

	// restartCh (buffered 1) carries the "a deliberate restart was requested"
	// hint. Restart sends on it non-blockingly; Run consumes it either in the
	// post-spawn drain (skip backoff) or the backoff-wait select (interrupt the
	// wait). Allocated once in New. Coalescing rapid restarts to one relaunch with
	// the newest args is correct — see Restart.
	restartCh chan struct{}

	// controlSeq mints locally-unique correlation ids for every control line the
	// runner writes — Interrupt and RevokeBypass alike. ONE sequence, not one per
	// subtype: request_id must be unique across all in-flight control requests on
	// the stream, so two counters would both mint "1" and a future ack-correlator
	// could not tell an interrupt ack from a revocation ack. atomic.Uint64 because
	// either method may be called from a goroutine other than Run; the zero value
	// is ready, so New adds nothing.
	controlSeq atomic.Uint64
}

// New validates the required fields, applies defaults, resolves WorkDir, and
// clones Args. It surfaces a not-found error when ClaudeBin is not on PATH and
// a wrapped fs.ErrNotExist when WorkDir does not exist (both fail fast).
func New(cfg Config) (*Runner, error) {
	if cfg.ClaudeBin == "" {
		return nil, errors.New("streamsup: ClaudeBin required")
	}
	if cfg.WorkDir == "" {
		return nil, errors.New("streamsup: WorkDir required")
	}
	if cfg.SessionID == "" {
		return nil, errors.New("streamsup: SessionID required")
	}
	if _, err := exec.LookPath(cfg.ClaudeBin); err != nil {
		return nil, fmt.Errorf("streamsup: claude binary not found: %w", err)
	}
	workDir, err := agentrun.ResolveWorkdir(cfg.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("streamsup: resolve workdir: %w", err)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.BackoffInitial == 0 {
		cfg.BackoffInitial = defaultBackoffInitial
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = defaultBackoffMax
	}
	if cfg.BackoffReset == 0 {
		cfg.BackoffReset = defaultBackoffReset
	}
	cfg.Args = slices.Clone(cfg.Args)
	return &Runner{
		cfg:       cfg,
		log:       cfg.Logger,
		workDir:   workDir,
		state:     State{Phase: PhaseStarting},
		args:      slices.Clone(cfg.Args),
		sessionID: cfg.SessionID,
		restartCh: make(chan struct{}, 1),
	}, nil
}

// Stdin returns the live child's stdin (the write end of its StdinPipe), or nil
// when no child is live (before the first spawn, between spawns, and mid-restart).
// The return type is io.Writer, not io.WriteCloser, so a consumer cannot close a
// handle the runner owns. This is the held-open-stdin seam; #1088's writer
// marshals a turn envelope and writes here, treating nil as a "no live child"
// refusal.
func (r *Runner) Stdin() io.Writer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stdin == nil {
		return nil
	}
	return r.stdin
}

// BeginRotation arms the rotation gate: from this call until the next child binds
// its stdin, every WriteUserTurn refuses with ErrNoLiveChild instead of writing
// into the child the accompanying RestartFresh is about to kill. It is called
// strictly BEFORE the pool-side rotate() that re-keys the binding and fires the
// session_transition{clear}, so no turn accepted on the strength of that clear can
// reach the outgoing child (#1330, AC1).
//
// It returns the DISARM for the caller's error path — startFreshRunner runs it
// when rotate() fails, because a gate left armed by a rotation that never happened
// would refuse every turn until the next respawn, a wedge the rotation never
// earned. The disarm is LIVE ONLY IF NO LATER ARM HAS LANDED (the rotateGen
// stamp), mirroring openForDelivery's undo in cmd/pyry: the only way to obtain a
// disarm is to have placed the matching arm, and a second arm retires the first
// one's. See rotateGen for the two-frame sequence that needs it.
//
// Like RestartFresh it drives only Runner-internal state — one leaf-mutex
// acquisition, no channel, no call-out — so the sessions layer can call it with no
// Pool lock held, which startFreshRunner does. Safe from any goroutine.
func (r *Runner) BeginRotation() (abort func()) {
	r.mu.Lock()
	r.rotating = true
	r.rotateGen++
	gen := r.rotateGen
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		if r.rotateGen == gen {
			r.rotating = false
		}
		r.mu.Unlock()
	}
}

// turnTarget reports the writer a turn may be written to — nil while a rotation is
// armed and nil when no child is live — together with whether the ROTATION GATE is
// what refused, under ONE r.mu acquisition. That single acquisition is the whole
// property: see mu's doc for why splitting the two reads reopens #1330's race at
// nanosecond width.
//
// The nil-handle branch returns an untyped nil rather than r.stdin, for the same
// reason Stdin() does: a typed-nil io.WriteCloser widened to io.Writer is not nil,
// and WriteTurn's no-live-child refusal keys on the interface being nil.
func (r *Runner) turnTarget() (w io.Writer, gated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rotating {
		return nil, true
	}
	if r.stdin == nil {
		return nil, false
	}
	return r.stdin, false
}

// WriteUserTurn writes the user envelope to the live child's stdin and claims
// the turncommit gate, wrapping the reviewed WriteTurn free function (#1088). It
// is the delivery path the session pool dispatches through (send_message →
// Session.WriteUserTurn → here). A nil target — no live child, or a rotation armed
// by BeginRotation (#1330) — yields ErrNoLiveChild without writing, the retryable
// no-live-child refusal; a gate deny yields turncommit.ErrDropped with zero bytes
// written. Both are WriteTurn's verbatim contract, so no new envelope construction
// is introduced.
//
// The rotation refusal deliberately reuses ErrNoLiveChild rather than minting a
// sentinel: that is already the retryable classification msgqueue and cmd/pyry
// agree on, and the e2e asserts as a contract check that stream WriteUserTurn
// returns only ErrNoLiveChild, turncommit.ErrDropped or nil
// (`TestRelayV2_StreamNewSessionRotatesAndRestartsFresh`). The discriminator an operator needs —
// "refused by the rotation gate" vs "no child yet" — is carried by the record
// below instead. Cost of refusing rather than blocking: the turn lands up to one
// msgqueue retry interval (1 s) later, against a give-up bound of 2 m.
//
// conversationID is accepted for interface conformance (sessions.Runner) and
// future outbound-cursor wiring (T4/T7); this slice does not yet track a cursor,
// so it is intentionally unused here — and it MUST NOT be logged: conversation ids
// are resolved daemon-side and stamped on the wire, never logged (cmd/pyry's
// stream_turn_busy.go, session_transition_v2.go), and streamsup logs none today.
// Neither may payload bytes be.
func (r *Runner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	w, gated := r.turnTarget()
	if gated {
		// Emitted OUTSIDE r.mu: a slow slog handler must never block the Run
		// goroutine's setStdin, which is the thing that ends this very window.
		//
		// INFO, NOT DEBUG, and the level is load-bearing rather than taste. #1330's
		// AC4 measures the fix against the e2e's AC-1 instrument guard, which greps
		// the daemon's WHOLE captured stderr for the literal "level=DEBUG"
		// (`TestRelayV2_StreamNewSessionRotatesAndRestartsFresh`) and which AC5 forbids
		// editing. A Debug record here fires on essentially every rotation, so it
		// would satisfy that guard from the fix's own diagnostic and turn the
		// measurement into a near-tautology — the false-green shape the ticket
		// spends a paragraph rejecting. Volume at Info is bounded and low: one
		// record per delivery attempt inside a window a respawn closes, and ~120
		// then a typed session_error in the pathological case where Run has already
		// returned — which is an anomaly worth being loud about. (Spec Open
		// question 1 left the level open on exactly this kind of evidence.)
		r.log.Info("streamsup: turn refused; new_session rotation in flight",
			"session", r.liveSessionID())
	}
	return WriteTurn(ctx, w, payload)
}

// Interrupt writes a single interrupt control_request line to the live child's
// stdin, ending the running turn (claude acks and emits a result with subtype
// error_during_execution, which the parser maps to a cancelled TurnEnd). The
// request_id is locally minted (not caller-supplied). When no child is live
// Stdin() is nil, so Interrupt returns the retryable ErrNoLiveChild without
// writing and without panicking — the safe no-op refusal.
//
// Interrupt is a concrete method on *Runner, deliberately NOT on sessions.Runner
// (#1077's placement rule: its dispatch lives in cmd/pyry, which can assert —
// unlike SetSpawnArgs (#1580) and RevokeBypass (#1604), whose consumer is inside
// internal/sessions and which are ON the interface for exactly that reason):
// #1121's interrupt routing reaches it via its own narrow interface or a type
// assertion. It mirrors how
// *supervisor.Supervisor encapsulates SendEsc (#726) without that method being on
// the interface. Safe from any goroutine.
func (r *Runner) Interrupt() error {
	return WriteInterrupt(r.Stdin(), r.nextControlID())
}

// RevokeBypass writes a single set_permission_mode control_request line to the
// live child's stdin, dropping its bypass posture WITHOUT killing it (#1595
// measured the drop live: success ack, next init reporting permissionMode
// default, and matching behaviour on the following turn, no respawn). The
// request_id is locally minted, and the mode is fixed in the writer — there is no
// enable direction on this surface, deliberately (see WriteBypassRevocation).
// When no child is live Stdin() is nil, so RevokeBypass returns the retryable
// ErrNoLiveChild without writing and without panicking.
//
// Unlike Interrupt it IS on sessions.Runner (#1604 widened the interface), and
// the contrast is the rule rather than an exception: Interrupt's dispatch lives in
// cmd/pyry, which can type-assert, whereas this method's consumer is
// Pool.UpdateSettings inside internal/sessions, with no such dispatch site. A
// structural assertion there would fail OPEN — its unmatched arm is a silent
// no-op, leaving the posture un-revoked while the update reports success — so the
// interface method makes a runner that cannot revoke a build failure instead.
// Stdin() releases r.mu before returning, so the potentially-blocking write never
// holds it. Safe from any goroutine.
func (r *Runner) RevokeBypass() error {
	return WriteBypassRevocation(r.Stdin(), r.nextControlID())
}

// nextControlID mints the next locally-unique control-request correlation id,
// shared by Interrupt and RevokeBypass. The atomic counter is unique within the
// runner's lifetime, which is all a future ack-correlator needs since each runner
// drives exactly one child stream; this slice does not read the control_response
// ack, so the id is write-only here.
func (r *Runner) nextControlID() string {
	return strconv.FormatUint(r.controlSeq.Add(1), 10)
}

// WaitForPTY returns cleanly: the stream-json path has no PTY to await. The
// session lifecycle calls it at the end of Activate so PTY-backed runners can
// block until their master is bound; on the stream path there is no such
// readiness gate, and the no-live-child window is handled per-turn by WriteTurn's
// retryable ErrNoLiveChild. Mirrors the seam that supervisor.WaitForPTY fills.
func (r *Runner) WaitForPTY(ctx context.Context) error { return nil }

// Restart swaps the live spawn base argv and, if a child is currently running,
// forces it to exit so the Run loop relaunches with the new args (resuming via
// --resume, since firstRun is already false after the first spawn). When no child
// is running it only swaps the args — they take effect on the next spawn.
// Non-blocking, fire-and-forget: the forever-retry Run loop guarantees the
// relaunch. Safe from any goroutine. Mirrors supervisor.Restart.
//
// It drives only Runner-internal state (restartMu, a ctx cancel, a buffered
// channel); it never touches Pool.mu or Session.lcMu, so the sessions layer can
// call it after releasing Pool.mu with no lock-order concern.
//
// The restartCh hint is always sent (non-blocking): a restart during a run makes
// the post-spawn drain skip backoff, and a restart during backoff (no live child
// to cancel) breaks the backoff wait. Coalescing is correct — two rapid restarts
// overwrite args with the newest value and collapse to the single buffered token,
// forcing one relaunch with the latest args.
//
// The argv install runs through setArgsLocked INSIDE this one section, not by
// calling SetSpawnArgs: the exported swap-only method takes restartMu itself, so
// calling it here would split Restart into two acquisitions and reopen the #1481
// window beginSpawn's doc closes. See SetSpawnArgs for the swap without the kill.
func (r *Runner) Restart(args []string) {
	r.restartMu.Lock()
	r.setArgsLocked(args)
	cancel := r.iterCancel
	r.restartMu.Unlock()

	// Hint first, then kill, so Run's post-spawn drain observes the token even if
	// the child exits the instant it is cancelled.
	select {
	case r.restartCh <- struct{}{}:
	default:
	}
	if cancel != nil {
		cancel()
	}
}

// SetSpawnArgs installs the base argv the NEXT spawn will use and leaves any live
// child running — it is Restart's swap half without the kill: no restartCh hint,
// no iterCancel call. It exists for a caller that needs to change what the session
// will run next without ending what it is running now; declining to call Restart
// is not that path, since it loses the swap outright and the next spawn (a
// crash-respawn, or an evict then Activate) silently re-execs the stale argv.
// Non-blocking, fire-and-forget, safe from any goroutine.
//
// The two do NOT converge when no child is live. Restart still sends its hint in
// that case, and the token is observable twice over: it satisfies Run's restartCh
// case in the backoff select, cutting an in-progress backoff wait short, and with
// no wait in flight it persists in the buffered channel so the NEXT child exit
// skips backoff entirely — RestartCount never increments and PhaseBackoff is never
// set. SetSpawnArgs sends nothing, so a runner already backing off stays backing
// off and the swap simply lands on the spawn that wait was going to make anyway.
//
// It takes restartMu exactly once and writes only args — never sessionID,
// rotatePending or iterCancel — so it preserves the #1481 single-acquisition
// property by construction, and cannot reach the forbidden state beginSpawn's doc
// names (that state is defined over the three fields it does not touch). Against a
// racing beginSpawn it serialises wholly before (that spawn observes the swap) or
// wholly after (that spawn keeps the old argv and the next one takes the new).
// Both are correct: the contract is the NEXT spawn, and it promises nothing about
// a spawn already in flight. Like Restart it drives only Runner-internal state and
// touches neither Pool.mu nor Session.lcMu, so the sessions layer can call it
// after releasing Pool.mu with no lock-order concern.
//
// The argv is installed VERBATIM — no validation, no shaping. That boundary is
// deliberate: composition and validation live upstream in the sessions layer
// (Session.spawnArgs, where claudeSettingsArgs enforces the yolo fail-safe in one
// place), and duplicating them here would give two places to keep in sync. Two
// consequences the caller owns, both inherited from Restart rather than introduced
// here: cmd/pyry's construction-time argv shaping is NOT reapplied on any
// post-construction install path (stripSessionIDFlags runs in mapStreamsupConfig
// and withApprovalArgs in newStreamRunnerFactory, both construction-only), and Run
// logs the composed argv at Info ("spawning claude"), so anything installed here
// reaches the daemon log.
//
// Must never be called with restartMu already held — the mutex is not reentrant.
// A caller already inside a section installs through setArgsLocked instead.
func (r *Runner) SetSpawnArgs(args []string) {
	r.restartMu.Lock()
	r.setArgsLocked(args)
	r.restartMu.Unlock()
}

// setArgsLocked installs the next spawn's base argv. Caller MUST hold restartMu.
// It is THE sole assignment to r.args after construction: Restart and SetSpawnArgs
// both install through it, which is what lets Restart keep its single restartMu
// acquisition (#1481) while there stays exactly one writer. Re-fusing the
// assignment back into a caller reintroduces the second writer this exists to
// prevent.
//
// The clone is load-bearing, not hygiene. Without it the caller keeps a live
// handle on the runner's spawn argv and can mutate it AFTER installation — a data
// race against beginSpawn's read, and a mutation that lands in the exec argv after
// whatever validation the caller performed. beginSpawn's "buildArgs is pure and
// copies base into a fresh slice, so passing r.args needs no clone" is about
// handing r.args OUT of the section; it does not license dropping the clone on the
// way IN.
//
// It makes no call-out. restartMu is a leaf: nothing under it may take another
// lock or do synchronous I/O, which is why Run logs "spawning claude" below
// beginSpawn's section rather than inside it. A diagnostic belongs in SetSpawnArgs
// after the unlock.
func (r *Runner) setArgsLocked(args []string) {
	r.args = slices.Clone(args)
}

// RestartFresh rotates the runner's persistent session id to newID and forces the
// next spawn to use --session-id <newID> (a fresh transcript, no fork),
// re-establishing first-run semantics. A subsequent crash-respawn then --resumes
// newID — never the pre-rotation id. If a child is live it is cancelled so Run
// relaunches immediately (skipping backoff, exactly like Restart); with no live
// child the rotation takes effect on the next spawn. Non-blocking,
// fire-and-forget, safe from any goroutine.
//
// An empty newID is a no-op (logged at Warn): the runner never emits
// --session-id "". This is a deterministic last-resort guard upholding New's
// non-empty contract — the validating boundary is the pool/routing layer (#1125),
// per the "caller-supplied id validation at the primitive boundary" convention.
//
// Unlike Restart it leaves r.args untouched: new_session rotates the id, not the
// model/flags — args stay owned by Restart/UpdateSettings. Like Restart it drives
// only Runner-internal state (restartMu, a ctx cancel, restartCh) and never a
// Pool lock, so the sessions layer can call it after releasing Pool.mu.
func (r *Runner) RestartFresh(newID string) {
	if newID == "" {
		r.log.Warn("streamsup: RestartFresh called with empty id; ignoring")
		return
	}
	r.restartMu.Lock()
	r.sessionID = newID
	r.rotatePending = true
	cancel := r.iterCancel
	r.restartMu.Unlock()

	// Hint first, then kill, so Run's post-spawn drain observes the token even if
	// the child exits the instant it is cancelled (identical ordering to Restart).
	select {
	case r.restartCh <- struct{}{}:
	default:
	}
	if cancel != nil {
		cancel()
	}
}

// beginSpawn captures one iteration's spawn inputs — the live (possibly
// Restart-swapped) base argv and the (possibly RestartFresh-rotated) id pair,
// consuming the fresh-restart request — AND publishes that iteration's cancel, in
// a SINGLE restartMu section. That single acquisition is the whole point (#1481):
// a racing Restart/RestartFresh takes restartMu exactly once, so it is serialised
// either fully before this section (the spawn observes its swap) or fully after it
// (it finds the just-published cancel and tears this spawn down). There is no
// third position, so the forbidden outcome — a live child under a pre-rotation id
// with no live iteration cancel — is unreachable rather than merely narrowed.
//
// SetSpawnArgs is the third racer on restartMu (#1580) and the weakest: one
// acquisition like the other two, and it writes args only — never sessionID,
// rotatePending or iterCancel — so whichever side of this section it lands on, the
// worst it can do is leave the swap for the following spawn.
//
// It reads the fields directly instead of calling accessors: restartMu is not
// reentrant, so any helper that takes it (as the now-deleted liveArgs and
// nextSpawnID accessors did) would deadlock here. buildArgs is pure and copies
// base into a fresh slice, so passing r.args needs no clone — the slice never
// escapes the section.
//
// forceFirst reports that a fresh-restart request was consumed; the caller re-arms
// its Run-goroutine-private firstRun on true. Returning it rather than a new
// firstRun keeps that local a plain assignment at the call site, where a := would
// shadow it and silently break the started-gated flip.
func (r *Runner) beginSpawn(ctx context.Context, firstRun bool) (
	iterCtx context.Context, cancel context.CancelFunc, args []string, forceFirst bool,
) {
	// Derived BEFORE the acquisition on purpose: context.WithCancel takes the
	// parent cancelCtx's own internal mutex, and restartMu must never be held
	// across another package's locking. It touches no Runner state, so nothing it
	// does can observe the pre-publish window.
	iterCtx, cancel = context.WithCancel(ctx)

	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	forceFirst = r.rotatePending
	r.rotatePending = false
	args = buildArgs(r.args, firstRun || forceFirst, r.sessionID)
	r.iterCancel = cancel
	return iterCtx, cancel, args, forceFirst
}

// liveSessionID snapshots the live session id under restartMu, for a diagnostic
// that must name WHICH runner it came from (the daemon's logger is pool-wide, not
// per-session). It takes restartMu only, never mu, so it is safe to call from a
// WriteUserTurn that has already released mu — and must not be called with mu
// held, which would nest two leaf locks for a log field.
func (r *Runner) liveSessionID() string {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	return r.sessionID
}

// clearIterCancel drops the finished iteration's cancel under restartMu. It takes
// no argument on purpose: beginSpawn's section is the only place that may publish
// a non-nil cancel, since publishing it anywhere else is exactly the #1481 window,
// so the API is narrowed until it cannot express that. A future re-split has to
// add the parameter back before it can reintroduce the defect.
func (r *Runner) clearIterCancel() {
	r.restartMu.Lock()
	r.iterCancel = nil
	r.restartMu.Unlock()
}

// drainRestart non-blockingly consumes a pending deliberate-restart hint,
// reporting whether one was present. Used post-spawn to decide whether to skip
// the crash backoff (a restart is not a crash).
func (r *Runner) drainRestart() bool {
	select {
	case <-r.restartCh:
		return true
	default:
		return false
	}
}

// Run supervises the claude child until ctx is cancelled. Each iteration spawns
// claude, holds its stdin open, and waits for exit. On a crash it applies the
// exponential backoff before respawning with --resume; the backoff resets after
// a child that stayed up longer than BackoffReset.
//
// Each iteration runs on a derived ctx (iterCtx) so a Restart can cancel a
// single child without tearing Run down. Shutdown is therefore detected from the
// parent ctx (ctx.Err()), not from the child-exit error: an iterCtx-only cancel
// (a settings restart) falls through to relaunch with the swapped args, while a
// parent cancel returns ctx.Err() cleanly. A deliberate restart skips backoff (it
// is not a crash). Mirrors supervisor.Run.
func (r *Runner) Run(ctx context.Context) error {
	bo := newBackoffTimer(r.cfg.BackoffInitial, r.cfg.BackoffMax, r.cfg.BackoffReset)
	firstRun := true

	startedAt := time.Now()
	r.updateState(func(st *State) {
		st.Phase = PhaseStarting
		st.StartedAt = startedAt
	})
	defer r.updateState(func(st *State) {
		st.Phase = PhaseStopped
		st.ChildPID = 0
		st.NextBackoff = 0
	})

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// beginSpawn reads the live (possibly Restart-swapped) argv and the
		// (possibly rotated) id, consumes any pending RestartFresh request, and
		// publishes this iteration's cancel — all in ONE restartMu section, so a
		// racer is serialised wholly before or wholly after it and can never miss
		// both (#1481). forceFirst re-arms first-run form so the rotated id spawns
		// with --session-id (a fresh transcript). firstRun's flip-back to false on a
		// successful spawn (below) then makes the next respawn --resume the new id —
		// see the started-gated flip.
		iterCtx, cancel, args, forceFirst := r.beginSpawn(ctx, firstRun)
		if forceFirst {
			firstRun = true
		}
		// Logged AFTER the section: synchronous log I/O must never run under a leaf
		// mutex. It is also what puts a racer arriving here on the already-published
		// cancel's side of the invariant.
		r.log.Info("spawning claude", "args", args, "workdir", r.workDir)

		start := time.Now()
		started, waitErr := r.spawnAndWait(iterCtx, args)
		cancel()
		r.clearIterCancel()
		uptime := time.Since(start)

		// The child is gone and the loop has not yet branched on why, so this one
		// unconditional call covers every exit path — crash, deliberate restart,
		// and shutdown alike. It must stay ABOVE the ctx.Err() return below: that
		// return (and the "claude exited" log under it) is skipped on shutdown, so
		// a fire anchored there would be silent on exactly the path that most needs
		// it. See Config.OnChildExit for the full contract.
		if r.cfg.OnChildExit != nil {
			r.cfg.OnChildExit()
		}

		// Shutdown is a parent-ctx cancel, NOT any child-exit error: an
		// iterCtx-only cancel (a Restart kill) leaves ctx.Err() nil and falls
		// through to relaunch. Return value stays a context error for the
		// graceful-shutdown contract.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if waitErr != nil {
			r.log.Warn("claude exited", "err", waitErr, "uptime", uptime)
		} else {
			r.log.Info("claude exited", "uptime", uptime)
		}

		// Advance firstRun only once claude has actually launched. A
		// spawn-SETUP failure (started == false) never ran --session-id, so the
		// session is still unestablished on disk; the next attempt must retry
		// with --session-id, not --resume against an id that was never created.
		if started {
			firstRun = false
		}

		// A deliberate restart is not a crash: skip backoff and relaunch
		// immediately with the swapped args.
		if r.drainRestart() {
			continue
		}

		delay := bo.next(uptime)
		r.updateState(func(st *State) {
			st.Phase = PhaseBackoff
			st.ChildPID = 0
			st.RestartCount++
			st.LastUptime = uptime
			st.NextBackoff = delay
		})
		r.log.Info("restarting after backoff", "delay", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		case <-r.restartCh:
			// A restart arrived while the child was already down and we were
			// waiting out the backoff — relaunch now with the swapped args.
		}
	}
}

// spawnAndWait spawns one claude child, stores its held-open stdin, and blocks
// until it exits. It reports started — whether cmd.Start actually launched
// claude — alongside the child's exit error (nil on clean exit, an
// *exec.ExitError on a crash, or a wrapped spawn-setup failure). started is
// false when the spawn fails during setup (StdinPipe / cmd.Start): claude never
// launched, so the session was never established and the caller must NOT advance
// firstRun — the next attempt has to retry with --session-id, not --resume
// against an id that --session-id never created. The Run loop distinguishes
// shutdown from crash via ctx.Err(), not the error value.
func (r *Runner) spawnAndWait(ctx context.Context, args []string) (started bool, waitErr error) {
	cmd := exec.CommandContext(ctx, r.cfg.ClaudeBin, args...)
	cmd.Dir = r.workDir
	cmd.Stdout = r.cfg.Stdout
	cmd.Stderr = r.cfg.Stderr
	if r.cfg.Env != nil {
		cmd.Env = append(os.Environ(), r.cfg.Env...)
	}

	// Reap-then-SIGTERM fires only on ctx cancel (operator teardown): claude
	// isolates every Bash command into its own detached process group two levels
	// below pyry and does not reap it on a graceful SIGTERM (#565), so pyry reaps
	// it here — the streamsup analogue of streamrunner's teardown reap (#924). A
	// spontaneous crash never invokes cmd.Cancel (os/exec fires it only on ctx
	// cancel), matching the proven streamrunner/ptyrunner behaviour; no
	// speculative crash-path reaping.
	cmd.Cancel = func() error {
		reapDescendantGroupsFn(cmd.Process.Pid, r.log)
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = killGrace

	// Open stdin BEFORE Start and hold it open across the child's whole life:
	// #1088 writes turn envelopes onto this handle. This is the deliberate
	// inversion from streamrunner, which closes stdin after one turn.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		// A spawn-setup failure is treated as a crashed iteration by the loop
		// (backoff + retry), so a transient failure never kills the daemon.
		// started stays false: claude never launched.
		return false, fmt.Errorf("streamsup: stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close() // best-effort: nothing consumed it, child never ran
		return false, fmt.Errorf("streamsup: start: %w", err)
	}

	r.setStdin(stdin)
	r.updateState(func(st *State) {
		st.Phase = PhaseRunning
		st.ChildPID = cmd.Process.Pid
		st.NextBackoff = 0
	})
	if r.cfg.onSpawn != nil {
		r.cfg.onSpawn(cmd.Process.Pid)
	}

	waitErr = cmd.Wait()

	// The child has exited (crash) or been torn down (cancel). Drop the handle
	// so Stdin() reports no live child, then close it best-effort — cmd.Wait
	// already closed the parent write end, so a broken-pipe / already-closed
	// error here is expected and benign (avoid a spurious WARN).
	if old := r.takeStdin(); old != nil {
		if cerr := old.Close(); cerr != nil && !agentrun.ExitErrIsBenign(cerr) {
			r.log.Warn("streamsup: stdin close failed", "err", cerr)
		}
	}

	return true, waitErr
}

// setStdin publishes the live child's stdin write end under the leaf mutex and,
// in the SAME acquisition, disarms the rotation gate (#1330): a successor child is
// bound, so the window in which a turn could be written into a doomed child is
// over. Pairing the two here is what makes "armed until the successor binds"
// exact — a gate released on RestartFresh's return would only narrow the race,
// since that call returns before the kill, the Wait and the respawn have happened.
func (r *Runner) setStdin(w io.WriteCloser) {
	r.mu.Lock()
	r.stdin = w
	r.rotating = false
	r.mu.Unlock()
}

// takeStdin clears the stored stdin handle and returns the previous value so
// the caller can close it outside the lock (a slow close never blocks Stdin()).
// It deliberately leaves the rotation gate alone: the teardown is the MIDDLE of
// the window the gate covers, not its end (see the rotating field).
func (r *Runner) takeStdin() io.WriteCloser {
	r.mu.Lock()
	w := r.stdin
	r.stdin = nil
	r.mu.Unlock()
	return w
}

// buildArgs assembles one spawn's argv: the fixed stream-json prefix, then the
// caller's base args, then the id flag. On the first spawn the id flag is
// --session-id <sessionID> (establishes the on-disk transcript under a known
// id); on every respawn it is --resume <sessionID> (reattach, append, no fork).
// Passing the SAME sessionID to both is why the on-disk id is stable across a
// kill-and-restart — plain --resume reuses the id and does not fork
// (--fork-session is the explicit, unused opt-in). Never emits -p/--print: the
// non-print choice is billing-tied and spike-verified (multi-turn, interrupt,
// resume, and the approval round-trip all work without it). Pure — no Runner
// state, and it never mutates base.
func buildArgs(base []string, firstRun bool, sessionID string) []string {
	args := make([]string, 0, len(base)+7)
	args = append(args,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
	)
	args = append(args, base...)
	if firstRun {
		return append(args, "--session-id", sessionID)
	}
	return append(args, "--resume", sessionID)
}
