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
// tailing, no fsnotify, and no <uuid>.jsonl path resolved for tailing or binding
// anywhere in this package — that is the whole point of the stream-json path (it
// structurally removes the bind-latency race the PTY path fought). The one
// transcript touch is a by-id EXISTENCE stat, at most one per spawn and only
// when Config.ClaudeSessionsDir is set: useCreateForm asks whether this
// session's own transcript is already on disk to pick the spawn's id flag, and
// reads no bytes from it. The only filesystem canonicalisation done here is
// resolving WorkDir before spawn (macOS /tmp → /private/tmp symlink hygiene).
//
// This slice owns the process lifecycle only. Turn I/O — the stdin envelope
// writer and the stdout→turnevent parser — and the turncommit/idle/stall gates
// are follow-on slices that build on the seams here (Stdin() and Config.Stdout).
//
// Dependency direction: the package must not import
// github.com/pyrycode/pyrycode/internal/supervisor (the PTY helper) nor any of
// the internal/agentrun subpackages (streamrunner, trust, …). It imports
// the internal/agentrun root package (ReapDescendantGroups, ExitErrIsBenign)
// for process helpers and internal/canonicalpath (Resolve) for filesystem-path
// resolution. Verify with:
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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/canonicalpath"
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

// Runner supervises the claude child lifecycle. Construct with New; drive with
// Run. The held-open stdin handle is exposed via Stdin.
type Runner struct {
	cfg Config
	log *slog.Logger

	// mu is a leaf mutex guarding WHICH CHILD, IF ANY, MAY RECEIVE A TURN: stdin
	// (the write end of the live child's StdinPipe) together with the rotation gate
	// (rotating + rotateGen + armFreshSeq) and the teardown gate (tearingDown +
	// armChildGen). stdin is swapped at spawn/teardown by the Run
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

	// mcpStatusEligible is the provenance verdict for stdin's exact child, derived
	// from the immutable argv snapshot that spawned it. childGeneration changes
	// whenever that binding changes, letting QueryMCPStatus register outside this
	// leaf lock and then reject a target that was replaced in the gap.
	mcpStatusEligible bool
	childGeneration   uint64
	childWorkspace    string
	childPATH         *string

	// rotating reports that a new_session rotation is armed and no successor child
	// has bound yet. BeginRotation sets it strictly BEFORE the pool-side rotate()
	// re-keys the binding and fires its session_transition{clear}; setStdin clears
	// it when an AUTHORISED child binds — the rotation's own successor or a later
	// spawn still, never an unrelated respawn of the pre-rotation id (see
	// armFreshSeq). While it stands every WriteUserTurn refuses
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

	// armFreshSeq is the freshSeq value BeginRotation observed when it placed the
	// STANDING arm, and is meaningful only while rotating is true. It authorises the
	// gate's RELEASE side, which until #1482 had no authorisation at all: setStdin
	// disarms only for a spawn whose own freshSeq snapshot is STRICTLY GREATER, i.e.
	// one set up after a RestartFresh that landed after this arm.
	//
	// The gap it covers is the whole of Pool.RotateForNewSession — mint, re-key,
	// register, an fsync'd atomic write, then the session_transition{clear} fan-out —
	// which runs between the arm and its partner RestartFresh. A child binding in
	// there (a crash-respawn of the pre-rotation id off the backoff ladder, or a
	// Restart-driven one) reads EQUAL and leaves the gate standing: it is not this
	// rotation's successor, it is the child RestartFresh is about to kill, and
	// clearing the gate for it hands a turn accepted on the strength of the
	// already-fired clear to a doomed child — #1330's silent loss through another
	// door.
	//
	// Written in the SAME mu acquisition that sets rotating, deliberately: arming
	// first and stamping second would leave a bind able to observe an armed gate
	// carrying a RETIRED arm's threshold and disarm by stale comparison.
	armFreshSeq uint64

	// tearingDown reports that a DELIBERATE TEARDOWN is armed and no child has bound
	// since. BeginTeardown sets it; the three callers are both eviction arms of
	// sessions.Session.runActive (idle timer, and the cap/force evictCh that
	// Pool.Remove also drives) and Pool.UpdateSettings' restart branch, each arming
	// strictly BEFORE the cancel or Restart that kills the child. While it stands
	// every WriteUserTurn refuses with ErrNoLiveChild rather than writing into that
	// child — #1330's loss, reached through a door #1330 did not close: the write
	// into a doomed child's pipe returns nil, msgqueue reads nil as a confirmed
	// commit and drops the queue head, and the message dies unread.
	//
	// SEPARATE FROM rotating, and the separation is the ticket (#1513) rather than
	// bookkeeping. The two gates refuse identically and differ entirely in their
	// RELEASE rule, because they are armed for opposite reasons: a rotation must
	// survive a Restart-driven or crash respawn (that is armFreshSeq's whole job),
	// while a teardown that ENDS IN exactly such a respawn must be released by it.
	// Arming rotating from these three callers would therefore wedge the session —
	// every turn refusing until some later RestartFresh landed — which is a worse
	// outcome than the loss it set out to fix.
	//
	// Like rotating it survives takeStdin: the teardown is the MIDDLE of the window
	// the gate covers, not its end.
	tearingDown bool

	// armChildGen is the childGeneration value standing when BeginTeardown placed the
	// arm, and is meaningful only while tearingDown is true. It is the RELEASE-side
	// threshold, and it is armFreshSeq's opposite number in shape and in effect:
	// setStdin releases the teardown gate for a binding child whose post-bump
	// childGeneration is STRICTLY GREATER, and since setStdin bumps that counter on
	// every bind, ANY successor child satisfies it. That is the property the arm
	// needs and the one armFreshSeq deliberately withholds.
	//
	// "ANY successor" is literal, and it carries one residual: a respawn the arm did
	// NOT cause — a crash respawn landing between BeginTeardown and the cancelSup or
	// Restart beside it — also releases the gate, reopening the window for the kill
	// that follows. That is a narrower instance of the residual mu's charter already
	// names, its odds are a crash inside two adjacent statements, and closing it means
	// correlating the arm with a specific teardown, i.e. rotateGen's shape — which is
	// the wedge this gate exists to avoid. Named, not fixed.
	//
	// Written in the SAME mu acquisition that sets tearingDown, for armFreshSeq's
	// reason verbatim: arming first and stamping second would leave a bind able to
	// observe an armed gate carrying a retired arm's threshold.
	//
	// Two overlapping teardown arms need no rotateGen-style stamp and no abort. The
	// later arm carries the higher threshold and one bind satisfies both, and a
	// stray arm cannot wedge anything because the next bind of ANY child clears it —
	// which is exactly what rotating's abort exists to compensate for and what this
	// release rule makes unnecessary. Monotonic and process-local: never persisted,
	// never serialised, never logged, and not an authorisation token — it gates only
	// the permissive direction, the release of a refusal.
	armChildGen uint64

	// stateMu is a leaf mutex guarding state, the control-plane snapshot. The
	// Run goroutine writes it via updateState; State() reads it from any
	// goroutine. Kept separate from mu (a different concern with a different
	// access pattern: control-plane reader vs. Run-goroutine writer).
	stateMu sync.Mutex
	state   State

	// restartMu is a leaf mutex guarding the live-restart seam: args, iterCancel,
	// and the RestartFresh id-rotation trio (sessionID + rotatePending + freshSeq). The
	// streamsup analogue of supervisor's restartMu. args is the live spawn base
	// argv, swapped by Restart (with the kill) or SetSpawnArgs (without it), and
	// assigned in exactly one place — setArgsLocked; iterCancel is the current
	// spawn's cancel, so Restart/RestartFresh can force the child to exit. Kept
	// separate from mu and stateMu — the three swap/rotate entry points touch only
	// this mutex + restartCh (never a Pool lock), so the sessions layer can call
	// them after releasing Pool.mu with no lock-order concern.
	//
	// AdoptSessionID (#2136) writes sessionID ALONE under this mutex — the trio's
	// first field without the other two — which is what makes following claude's own
	// announced reset distinguishable here from ordering a fresh restart.
	//
	// SetSpawnWorkDir (#1475) writes the directory PAIR (workDir +
	// claudeSessionsDir) and nothing else, which is what makes moving the next
	// spawn's working directory distinguishable from rotating its id — and what
	// keeps a spawn from ever observing a directory and a transcript folder that
	// name different places.
	//
	// The charter is ONE ACQUISITION PER SPAWN SETUP (#1481): beginSpawn both reads
	// the spawn inputs (args + the id pair) and publishes iterCancel in a single
	// section, so a racing Restart/RestartFresh/SetSpawnArgs/AdoptSessionID is serialised either
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
	// beginSpawn). Seeded from cfg.SessionID in New. It has TWO writers after that,
	// and they are not variants of one another: RestartFresh rotates it as part of a
	// daemon-ordered fresh restart, and AdoptSessionID installs it alone when claude
	// announces a reset the daemon is merely following (#2136). The mutable analogue
	// of the immutable cfg.SessionID — that field stays the construction-time input
	// and New's non-empty validation source; after construction the live id is
	// r.sessionID.
	sessionID string

	// spawnMode is the permission posture each spawn writes to its child (#2064).
	// Seeded from cfg.SpawnPermissionMode in New and replaced by
	// SetSpawnPermissionMode; the mutable analogue of that immutable field exactly as
	// sessionID is of cfg.SessionID.
	//
	// It lives under restartMu with the other spawn INPUTS rather than under mu with
	// the turn-target state, and the split is the same one args sits on: this value
	// decides what a spawn WRITES, not which child may receive a turn. beginSpawn
	// reads it in the section it already takes, so the one-acquisition-per-spawn-setup
	// charter holds with no second acquisition.
	spawnMode string

	// workDir is the directory each spawn chdirs into (spawnAndWait's cmd.Dir), a
	// resolved absolute path (canonicalpath.Resolve). Seeded from cfg.WorkDir in
	// New and replaced by SetSpawnWorkDir; the mutable analogue of that immutable
	// field exactly as sessionID is of cfg.SessionID.
	//
	// It became a spawn INPUT at #1475, when a new_session rotation gained the job
	// of bringing the successor up in the workspace the operator recorded on the
	// conversation. Before that it was a construction constant read live off the
	// Runner by both spawnAndWait and Run's "spawning claude" log; with a swapper
	// in play either read is a data race, so both now take beginSpawn's snapshot.
	//
	// claudeSessionsDir MOVES WITH IT and the pairing is not cosmetic — see that
	// field. The two are written in ONE restartMu section so no spawn can observe a
	// directory and a transcript folder that name different places.
	workDir string

	// claudeSessionsDir is the folder useCreateForm probes to decide --session-id
	// vs --resume for this spawn: the projects directory claude writes <id>.jsonl
	// into, which it names after the directory it resolved. Seeded from
	// cfg.ClaudeSessionsDir in New and replaced, always together with workDir, by
	// SetSpawnWorkDir. "" keeps Config's meaning: run no probe, latch on firstRun.
	//
	// WHY IT CANNOT BE LEFT BEHIND when workDir moves. The rotation spawn itself
	// would survive a stale value — forceFirst is set and the fresh id is absent
	// from either folder — but the successor's NEXT crash-respawn would probe the
	// pre-move folder, find the transcript absent because claude wrote it under the
	// new one, choose create form, and re-issue --session-id against a live
	// transcript, which claude refuses (ADR 032). A supplied directory makes
	// useCreateForm decide OUTRIGHT rather than latch, so that is a permanent
	// respawn loop rather than one wasted spawn.
	claudeSessionsDir string

	// postureGate is cfg.PostureGate, hoisted so the turn path reads one field. Nil
	// is the ungated runner, and every method on the type is nil-receiver-safe, so no
	// call site branches on it.
	postureGate *PostureGate

	// parser is non-nil when Config.Stdout is the Parser that consumes this runner's
	// child output. It binds request registration and per-spawn MCP status policy to
	// that exact parser.
	parser *Parser

	// rotatePending is set by RestartFresh and consumed once by beginSpawn: it
	// re-arms first-run form so the next spawn uses --session-id <newID> (a fresh
	// transcript), not --resume <newID>. firstRun stays Run-goroutine-private;
	// rotatePending is the only cross-goroutine signal that re-arms it.
	rotatePending bool

	// freshSeq counts the RestartFresh rotations that have LANDED. It is bumped
	// inside the same restartMu section that rotates sessionID and sets
	// rotatePending, so it is totally ordered with beginSpawn's snapshot of it and
	// no spawn can observe the rotation without the bump. RestartFresh's empty-id
	// early return sits ABOVE that section and therefore does not bump: the counter
	// measures rotations that happened, not calls that were made, and authorising a
	// spawn that succeeded no rotation is precisely the failure this closes.
	//
	// It carries the rotation gate's release-side authorisation (#1482): a spawn
	// snapshots it in beginSpawn and setStdin compares that snapshot against
	// armFreshSeq. A monotonic THRESHOLD, not a one-shot token — every later spawn
	// satisfies it and none consumes it. That is the whole reason the authorisation
	// does not ride on rotatePending/forceFirst, which beginSpawn consumes
	// unconditionally: a successor spawn that dies during setup (setStdin never
	// reached) would spend the token, every later child would bind unauthorised, and
	// the gate would wedge permanently — a conversation refusing every turn until
	// some unrelated later rotation, which is worse than the loss being fixed.
	//
	// Monotonic and process-local, like rotateGen: never persisted, never
	// serialised, never logged, and compared only against a value BeginRotation
	// itself captured. uint64 overflow is not a reachable concern.
	freshSeq uint64

	// restartCh (buffered 1) carries the "a deliberate restart was requested"
	// hint. Restart sends on it non-blockingly; Run consumes it either in the
	// post-spawn drain (skip backoff) or the backoff-wait select (interrupt the
	// wait). Allocated once in New. Coalescing rapid restarts to one relaunch with
	// the newest args is correct — see Restart.
	restartCh chan struct{}

	// controlSeq mints locally-unique correlation ids for every control line the
	// runner writes — Interrupt and SetPermissionMode alike. ONE sequence, not one per
	// subtype: request_id must be unique across all in-flight control requests on
	// the stream, so two counters would both mint "1" and an ack correlator could not
	// tell an interrupt ack from a permission-mode ack. atomic.Uint64 because
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
	// A posture with nothing to correlate its ack would be written and then believed
	// on no evidence — the unenforced-write shape the gate exists to prevent — so the
	// misconfiguration fails LOUDLY at construction rather than degrading to a silent
	// ungated write at every spawn. The converse pairing is fine and is the ungated
	// default: a gate with no mode is never armed.
	if cfg.SpawnPermissionMode != "" && cfg.PostureGate == nil {
		return nil, errors.New("streamsup: SpawnPermissionMode requires PostureGate")
	}
	workDir, err := canonicalpath.Resolve(cfg.WorkDir)
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
	parser := cfg.ControlParser
	if parser == nil {
		parser, _ = cfg.Stdout.(*Parser)
	}
	return &Runner{
		cfg:               cfg,
		log:               cfg.Logger,
		state:             State{Phase: PhaseStarting},
		args:              slices.Clone(cfg.Args),
		sessionID:         cfg.SessionID,
		spawnMode:         cfg.SpawnPermissionMode,
		workDir:           workDir,
		claudeSessionsDir: cfg.ClaudeSessionsDir,
		postureGate:       cfg.PostureGate,
		parser:            parser,
		restartCh:         make(chan struct{}, 1),
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

// ConfirmedPermissionMode reports the last permission mode claude confirmed for
// this runner's exact current child. The bool is false before a non-empty
// system/init report, after child exit, and when the runner is not wired to its
// concrete Parser. It never falls back to spawn intent, argv, or stored settings,
// and performs no control write or posture-gate transition.
func (r *Runner) ConfirmedPermissionMode() (string, bool) {
	if r.parser == nil {
		return "", false
	}
	return r.parser.confirmedPermissionMode()
}

// WaitForPTY returns cleanly: the stream-json path has no PTY to await. The
// session lifecycle calls it at the end of Activate so PTY-backed runners can
// block until their master is bound; on the stream path there is no such
// readiness gate, and the no-live-child window is handled per-turn by WriteTurn's
// retryable ErrNoLiveChild. Mirrors the seam that supervisor.WaitForPTY fills.
func (r *Runner) WaitForPTY(ctx context.Context) error { return nil }

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
	var episode crashEpisode
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
		iterCtx, cancel, id, args, env, workDir, forceFirst, freshSeq, spawnMode := r.beginSpawn(ctx, firstRun)
		if forceFirst {
			firstRun = true
		}
		// Logged AFTER the section: synchronous log I/O must never run under a leaf
		// mutex. It is also what puts a racer arriving here on the already-published
		// cancel's side of the invariant. The workdir is the SNAPSHOT, not r.workDir:
		// since #1475 the field is swappable, so a live read here would both race the
		// swapper and be able to name a directory other than the one this spawn is
		// about to enter.
		r.log.Info("spawning claude", "args", args, "workdir", workDir)

		start := time.Now()
		started, stderrTail, waitErr := r.spawnAndWait(iterCtx, args, env, workDir, freshSeq, spawnMode)
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

		// session is the id THIS child was spawned with, because the runner logger is
		// pool-wide and two crash-looping conversations must be told apart (#2723).
		// The stderr tail rides as a daemonLogOnly value: the daemon's own log output
		// renders it, and control.SlogTee keeps it out of the ring that `pyry logs`
		// and the debug bundle read. spawnAndWait returns it only for a child that
		// failed on its own, never for a restart or a shutdown kill.
		if waitErr != nil {
			attrs := []any{"session", id, "err", waitErr, "uptime", uptime}
			if stderrTail != "" {
				attrs = append(attrs, "stderr", daemonLogOnly(stderrTail))
			}
			r.log.Warn("claude exited", attrs...)
		} else {
			r.log.Info("claude exited", "session", id, "uptime", uptime)
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
		if episode.observe(uptime) && r.cfg.OnCrashLoop != nil {
			r.cfg.OnCrashLoop()
		}
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
// false when admission or spawn setup fails (token read / StdinPipe / cmd.Start).
// Claude never launched, so the session was never established; the caller must NOT advance
// firstRun — the next attempt has to retry with --session-id, not --resume
// against an id that --session-id never created. The Run loop distinguishes
// shutdown from crash via ctx.Err(), not the error value.
//
// freshSeq is beginSpawn's setup-time snapshot, carried through untouched and
// handed to setStdin as the rotation gate's release authorisation (#1482). Nothing
// here reads or interprets it.
//
// env is beginSpawn's composed child environment — Config.Env plus, when
// Config.SessionIDEnvVar is set, this spawn's live session id — and is likewise
// combined with the inherited environment. Optional token admission then replaces
// only the credential entry; all other snapshotted entries survive.
//
// workDir is beginSpawn's setup-time snapshot of the directory to chdir into,
// carried through as a parameter rather than read off the Runner (#1475): the
// field is swappable, and a live read here would race SetSpawnWorkDir and could
// enter a directory this spawn's argv and transcript probe were not built for.
//
// stderrTail is the end of the child's stderr (see stderrTail), returned only when
// the child failed ON ITS OWN: waitErr is non-nil and ctx — the iteration ctx that
// Restart, RestartFresh and shutdown all cancel before their kill — is still live.
// The check has to sit here, before Run's cancel() and drainRestart, because a
// deliberate kill also produces a non-nil waitErr.
func (r *Runner) spawnAndWait(ctx context.Context, args, env []string, workDir string, freshSeq uint64, spawnMode string) (started bool, stderrTail string, waitErr error) {
	childEnv, err := r.accountTokenEnv(ctx, append(os.Environ(), env...))
	if err != nil {
		return false, "", err
	}
	claudeBin, err := r.spawnClaudeBin()
	if err != nil {
		return false, "", err
	}
	cmd := exec.CommandContext(ctx, claudeBin, args...)
	cmd.Dir = workDir
	cmd.Stdout = r.cfg.Stdout
	cmd.Env = childEnv
	var childPATH *string
	for _, entry := range cmd.Environ() {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path := value
			childPATH = &path
		}
	}
	// Install the privacy decision before Start can create the stdout forwarder.
	// args is beginSpawn's immutable snapshot for this exact child, so a settings
	// change for the next spawn cannot retroactively alter this one.
	mcpEligible := mcpStatusEligible(args, r.cfg.MCPStatusConfigPath)
	if r.parser != nil {
		r.parser.beginMCPStatusChild(mcpEligible, r.RequestMCPStatus)
	}

	// Reap-then-SIGTERM fires only on ctx cancel (operator teardown): claude
	// isolates every Bash command into its own detached process group two levels
	// below pyry and does not reap it on a graceful SIGTERM (#565), so pyry reaps
	// it here — the streamsup analogue of streamrunner's teardown reap (#924). A
	// spontaneous crash never invokes cmd.Cancel (os/exec fires it only on ctx
	// cancel), matching the proven streamrunner behaviour; no
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
		if r.parser != nil {
			r.parser.endPermissionModeChild()
		}
		// A spawn-setup failure is treated as a crashed iteration by the loop
		// (backoff + retry), so a transient failure never kills the daemon.
		// started stays false: claude never launched.
		return false, "", fmt.Errorf("streamsup: stdin pipe: %w", err)
	}

	stderr := captureStderr(cmd, r.cfg.Stderr, r.log)
	if err := cmd.Start(); err != nil {
		stderr.started(false)
		_ = stdin.Close() // best-effort: nothing consumed it, child never ran
		if r.parser != nil {
			r.parser.endPermissionModeChild()
		}
		return false, "", fmt.Errorf("streamsup: start: %w", err)
	}
	stderr.started(true)

	// The posture gate is ARMED BEFORE setStdin, and that ordering is load-bearing:
	// setStdin publishes the live stdin handle and disarms the rotation gate in ONE
	// acquisition, so turns become writable at that instant. Arming after it would
	// leave a window in which a turn is delivered to a child whose posture the daemon
	// has not written, let alone confirmed. The id is minted here so the gate and the
	// line below carry the same one.
	//
	// Whether anything is WRITTEN at all is permissionModeSpawnWritable's single
	// decision — the writer's allow-list MINUS the escalation, and the subtraction is
	// #2066's. That ticket widened permissionModeAllowed so a live child can be
	// escalated in band, and this reader is the one that had to be scoped away from
	// the widening: a bypassPermissions session is still sent nothing AND its gate
	// still ends up open, so its turns flow exactly as they do today. That is the
	// intended reading — a gate no write can ever release is a bricked session, not a
	// fail-closed one — and it is why the spawn write was NOT extended alongside the
	// routing one: #2060 measured claude accepting the escalation on an ESTABLISHED
	// stream after two turns, and nothing has measured it as the FIRST control request
	// on a fresh child. The launch argv already asserts the escalation on every child
	// since #2065, so there is nothing here for the write to change anyway. The empty
	// mode of an unconfigured runner fails the predicate the same way, which is what
	// keeps every pre-#2064 construction site byte-identical.
	//
	// THE ARM IS UNCONDITIONAL, and only the id it installs is conditional. Every spawn
	// must publish its OWN gate state, because the gate outlives the child: Restart
	// reuses this Runner and this PostureGate, so a branch that installs nothing leaves
	// the PREDECESSOR's id standing. That is not hypothetical, and #2066 changed only
	// which sequence reaches it: an un-acked default child that is escalated IN BAND
	// (SetSpawnPermissionMode installs bypassPermissions as the spawn posture, no
	// respawn) and then CRASHES takes exactly that path, and the replacement bypass
	// child would refuse every turn forever against an id nothing can ever ack. Before
	// #2066 the same shape arrived through Pool.UpdateSettings' restart branch instead.
	// arm("") installs the OPEN state, which is the same statement armedID's doc
	// already makes; nesting the call merely failed to reach the branch that needs it.
	//
	// A SPAWN WHOSE BYPASS THE DAEMON DID NOT COMPOSE IS SENT NOTHING, whatever the
	// stored posture says. This is the interlock an earlier revision of this comment
	// recorded as "designed and REJECTED" on the grounds that it would brick #2065;
	// the live-claude gate then falsified the premise the rejection rested on, and
	// the reversal is recorded here rather than quietly applied.
	//
	// WHAT THE REJECTION ASSUMED: that the stored posture is kept accurate at its
	// source by SetSpawnPermissionMode, so a disagreement between argv and stored mode
	// could not arise. It can. Bypass reaches the argv from TWO entry points —
	// withApprovalArgs' doc in cmd/pyry names both — and only one of them is modelled
	// by the stored posture:
	//
	//   - sessions.claudeSettingsArgs. canonicalSettings pins PermissionMode to the
	//     escalation alongside a set YOLO bit, so permissionModeSpawnWritable already
	//     refuses THAT row by subtraction and the interlock is redundant on it.
	//     Since #2065 this composer appends the flag UNCONDITIONALLY, so it also
	//     produces the row where the stored posture is a real in-band mode and the
	//     child must be walked back to it — the downgrade #2065 exists for.
	//   - THE OPERATOR'S BOOTSTRAP PASS-THROUGH claude args, the shape main.go's own
	//     install-service example documents. These never touch SessionSettings, so the
	//     stored posture reads default while the child is launched in bypass — and
	//     asserting default at that child SILENTLY REVOKES the bypass the operator
	//     explicitly asked for, in-band and unanswerable.
	//
	// MEASURED, not reasoned: the #2064 live-claude gate reddened four
	// TestInteractiveStream* specs that pass on main. Each spawns through
	// spawnBootstrapDaemon's pass-through --dangerously-skip-permissions, and each
	// failed with real claude refusing a real tool — permission_denied on Bash and on
	// Write, and "I need permission to read that file" on Read — after this write put
	// the child in default.
	//
	// WHY THIS READS A CONFIG FIELD AND NOT THE ARGV. #2064 read
	// slices.Contains(args, bypassPermissionsFlag) here, and that was the right
	// signal while the flag's presence still MEANT something: the two entry points
	// above were the only ways it reached an argv. #2065 makes claudeSettingsArgs
	// append it to every argv, so the argv read answers true universally — no child
	// is ever sent its stored posture, every child stays in bypass, every gate stays
	// open, and the ticket ships as its own exact inverse, green and silent. The
	// distinction that survives is PROVENANCE: bypass the daemon composed itself,
	// which it may walk back, versus bypass an operator handed it, which it may not.
	// cfg.OperatorBypass carries exactly that, derived in internal/sessions from the
	// settings-free spawnBase — the one place the two are still separable.
	//
	// Losing per-spawn evaluation costs nothing here, and the reason is worth stating
	// because the field it replaces was deliberately per-spawn: the old value was a
	// property of the ASSEMBLED argv, which Restart(newArgs) can change, whereas
	// provenance is a property of the immutable spawnBase every recompose is built
	// from. See Config.OperatorBypass for the one constraint that keeps it true.
	postureID := ""
	if permissionModeSpawnWritable(spawnMode) && !r.cfg.OperatorBypass {
		postureID = r.nextControlID()
	}
	r.postureGate.arm(postureID)

	r.setStdin(stdin, freshSeq, mcpEligible)
	r.mu.Lock()
	r.childWorkspace = workDir
	r.childPATH = childPATH
	r.mu.Unlock()
	r.updateState(func(st *State) {
		st.Phase = PhaseRunning
		st.ChildPID = cmd.Process.Pid
		st.NextBackoff = 0
	})
	// One write per spawn, which is one per child, on RequestInitializeOnSpawn's shelf
	// and for its stated reasons — see that field's doc for why the placement below
	// cmd.Start is once-per-child by construction and why the child's per-turn init
	// line is the wrong trigger. It goes ABOVE the initialize ask so the round trip
	// that releases the gate starts first.
	//
	// The error is ABSORBED exactly as that ask's is: it must never become waitErr, or
	// a benign teardown race would enter the backoff ladder and restart a child. What
	// differs is the consequence — ABSORBING THE ERROR MUST NOT RELEASE THE GATE, and
	// that is structural rather than a rule to remember: nothing on this path touches
	// the gate, so a child whose write failed simply never has its posture confirmed
	// and its turns keep refusing until it is replaced. Debug for the ask's reasons,
	// and the record admits neither the mode nor the request id — WritePermissionMode
	// documents that no wrap carries the mode and that its vocabulary refusal is a bare
	// sentinel, which is what makes logging the error verbatim safe here.
	if postureID != "" {
		if err := WritePermissionMode(r.Stdin(), postureID, spawnMode); err != nil {
			r.log.Debug("streamsup: permission mode not delivered", "err", err)
		}
	}
	// One ask per spawn, which is one ask per child — see
	// Config.RequestInitializeOnSpawn for why no bookkeeping is needed and why the
	// child's per-turn init line is the wrong trigger. The error is absorbed
	// rather than returned: it must never reach waitErr, or a benign teardown race
	// would enter the backoff ladder and restart a child over an ask that commits
	// nothing. Debug, not Warn, for that same reason and to match
	// logControlResponse, which logs the reply half of this round trip at Debug;
	// the wrapped error is daemon-authored, and no request id and no payload are
	// admitted.
	if r.cfg.RequestInitializeOnSpawn {
		if err := r.RequestInitialize(); err != nil {
			r.log.Debug("streamsup: initialize ask not delivered", "err", err)
		}
	}

	if r.cfg.onSpawn != nil {
		r.cfg.onSpawn(cmd.Process.Pid)
	}

	waitErr = cmd.Wait()
	stderrTail = stderr.finish()
	if waitErr == nil || ctx.Err() != nil {
		stderrTail = ""
	}

	// The child has exited (crash) or been torn down (cancel). Drop the handle
	// so Stdin() reports no live child, then close it best-effort — cmd.Wait
	// already closed the parent write end, so a broken-pipe / already-closed
	// error here is expected and benign (avoid a spurious WARN).
	if old := r.takeStdin(); old != nil {
		if cerr := old.Close(); cerr != nil && !agentrun.ExitErrIsBenign(cerr) {
			r.log.Warn("streamsup: stdin close failed", "err", cerr)
		}
	}
	// Here and not in retireParserChild: that also runs from retireStdinGeneration
	// while the child may still be writing, and this is the one point where the
	// stdout copy is known to be finished (#1503).
	if r.parser != nil {
		r.parser.dropPartialLine()
	}

	return true, stderrTail, waitErr
}

// setStdin publishes the live child's stdin write end under the leaf mutex and,
// in the SAME acquisition, disarms the rotation gate (#1330) — but only for a child
// AUTHORISED to end that window (#1482). spawnFreshSeq is beginSpawn's setup-time
// snapshot of freshSeq; strictly greater than armFreshSeq means this spawn was set
// up after a RestartFresh that landed after the standing arm, so it is that
// rotation's successor or a later spawn still. A spawn set up before it reads EQUAL
// and leaves the gate standing — a crash-respawn off the backoff ladder or a
// Restart-driven one is not a successor, it is the child RestartFresh is about to
// kill, and releasing the gate for it hands a turn accepted on the strength of the
// already-fired session_transition{clear} to a doomed child.
//
// Pairing the release with the BIND is what makes "armed until the successor binds"
// exact — a gate released on RestartFresh's return would only narrow the race,
// since that call returns before the kill, the Wait and the respawn have happened.
//
// The handle is published UNCONDITIONALLY: refusing turns is the gate's job, and
// withholding the handle would break Interrupt, RevokeBypass and the teardown
// close. The check and the clear are one acquisition, so no TOCTOU is added.
//
// Nothing is logged here, and a diagnostic for a refused disarm must not be added:
// mu is a leaf, so a slow slog handler would stall the Run goroutine mid-spawn
// under it — the mirror of the hazard WriteUserTurn's gated record keeps outside
// its own acquisition — and a Debug record anywhere in the daemon's stderr defeats
// #1330's e2e instrument guard. Such a diagnostic belongs above the acquisition, in
// spawnAndWait, at Info.
func (r *Runner) setStdin(w io.WriteCloser, spawnFreshSeq uint64, mcpEligible bool) {
	r.mu.Lock()
	r.stdin = w
	r.mcpStatusEligible = mcpEligible
	r.childGeneration++
	r.childWorkspace = ""
	r.childPATH = nil
	if spawnFreshSeq > r.armFreshSeq {
		r.rotating = false
	}
	// The teardown gate's release, beside the rotation gate's so the two thresholds
	// read together (#1513). Same shape, different counter, and deliberately the
	// weaker authorisation: the bump above happens first, so EVERY bind after an arm
	// reads strictly greater and ends the window — including the Restart-driven and
	// crash respawns the line above refuses, which are precisely the successors a
	// teardown ends in.
	if r.childGeneration > r.armChildGen {
		r.tearingDown = false
	}
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
	r.mcpStatusEligible = false
	r.childGeneration++
	r.childWorkspace = ""
	r.childPATH = nil
	r.mu.Unlock()
	r.retireParserChild()
	return w
}

// retireStdinGeneration interrupts a write to one captured child without touching a
// replacement. Clearing the binding precedes Close so no later caller can select the
// handle being interrupted. The pipe close runs outside Runner.mu and unblocks the
// synchronous writer whose caller context fired this method.
func (r *Runner) retireStdinGeneration(generation uint64) {
	r.mu.Lock()
	if r.stdin == nil || r.childGeneration != generation {
		r.mu.Unlock()
		return
	}
	w := r.stdin
	r.stdin = nil
	r.mcpStatusEligible = false
	r.childGeneration++
	r.mu.Unlock()

	r.retireParserChild()
	_ = w.Close()
}

// retireParserChild fails every waiter owned by the departing child. It is called
// only after Runner.mu is released because every correlator carries its own mutex.
func (r *Runner) retireParserChild() {
	if r.parser == nil {
		return
	}
	r.parser.endPermissionModeChild()
	r.parser.failMCPStatusQueries()
	r.parser.failMCPActuations()
	r.parser.failContextUsageQueries()
	r.parser.failAppliedSettingsQueries()
}
