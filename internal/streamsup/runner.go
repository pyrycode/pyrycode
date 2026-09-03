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
	"github.com/pyrycode/pyrycode/internal/transcript"
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
	// check only — the pool owns minting and shape validation). With no
	// ClaudeSessionsDir the first spawn passes --session-id <SessionID> and every
	// respawn passes --resume <SessionID> (reattach, append, no fork); with one
	// set, the by-id transcript probe picks the form per spawn instead (see
	// useCreateForm). Either way the SAME id is passed, so the on-disk id stays
	// stable across a kill-and-restart.
	SessionID string

	// ClaudeSessionsDir is the directory containing claude's <uuid>.jsonl files
	// for this WorkDir. Empty disables the by-id transcript probe, and every
	// spawn's id flag falls back to the Run loop's firstRun latch — byte-identical
	// to pre-#1630 argv. Name and semantics mirror sessions.Config.ClaudeSessionsDir
	// deliberately, so the two read as one concept.
	//
	// It is NEVER derived from WorkDir inside this package.
	// sessions.DefaultClaudeSessionsDir maps a workdir into the real
	// $HOME/.claude/projects/<encoded>, so deriving it here would point every unit
	// test — whose WorkDir is a t.TempDir() — at the developer's actual home. The
	// directory only ever arrives through this field.
	ClaudeSessionsDir string

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

	// RequestInitializeOnSpawn asks each spawned child, exactly once, to report
	// what the session knows about itself — the model list and the slash-command
	// list — by writing one initialize control_request onto its held-open stdin
	// (see RequestInitialize). Optional; false (the zero value) keeps a runner
	// byte-identical to pre-#1839 behaviour, which is what leaves every
	// construction site other than the interactive daemon's mapStreamsupConfig
	// untouched. Set it only there: the ask is that daemon's policy, not every
	// runner's behaviour.
	//
	// CARDINALITY IS PER SPAWN, and that is the whole of how "exactly once per
	// child" is enforced — there is no counter and no per-child bookkeeping, and
	// none is wanted: spawnAndWait runs once per child, so firing there is once per
	// child by construction, and the count does not grow with the turns that child
	// serves. The trap this rules out is the child's system/init line, which looks
	// like the per-child signal and is not — emitModelAnnounced's doc measures it
	// firing once per TURN, so a trigger there would ask on every turn while
	// staying green in any single-turn test.
	//
	// Unlike OnChildExit it fires only when a child ACTUALLY LAUNCHED: the call
	// sits below cmd.Start, so a spawn-setup failure never reaches it. That is the
	// opposite cardinality to OnChildExit's per-supervision-iteration one, and the
	// two fields sit next to each other, so the contrast is stated rather than left
	// to be inferred.
	//
	// The ask is BEST-EFFORT AND ITS ERROR IS ABSORBED — one Debug record, then the
	// spawn continues. It never becomes spawnAndWait's waitErr, so it cannot enter
	// the backoff ladder, restart a child or fail a spawn.
	//
	// It reads Stdin(), NOT the rotation-gated turnTarget, so an ask is not refused
	// while a new_session rotation is armed and one can land on a child
	// RestartFresh is about to kill. Deliberate rather than tolerated: that ask
	// commits nothing and no queue head rides on it, whereas gating it would leave
	// a child whose rotation was ABORTED (BeginRotation's disarm path) permanently
	// unasked — the state the per-replacement ask exists to prevent.
	RequestInitializeOnSpawn bool

	// SpawnPermissionMode is the session's stored permission posture, written to
	// every spawned child as a set_permission_mode control request before any user
	// turn can reach it (#2064). Optional; the zero value writes nothing and arms
	// nothing, which is what leaves every construction site other than the
	// interactive daemon's mapStreamsupConfig byte-identical.
	//
	// It is the CONSTRUCTION-TIME seed only. The live value is r.spawnMode, which
	// SetSpawnPermissionMode replaces when the session's stored posture changes —
	// Pool.UpdateSettings rebuilds no runner, so a value read from this field at
	// spawn time would go stale and could assert a posture the operator has already
	// changed. Same relationship SessionID has with r.sessionID.
	//
	// bypassPermissions is REFUSED here by the same non-membership that refuses it in
	// WritePermissionMode, so a bypass session is sent nothing and its gate is never
	// armed: its turns flow exactly as they do today. That is the intended reading and
	// not a gap — a gate no write can ever release is a bricked session, not a
	// fail-closed one. Re-granting bypass stays on the respawn path.
	//
	// Setting it REQUIRES PostureGate; New refuses the pairing otherwise. A posture
	// written with nothing correlating its ack is a line the daemon believes in and
	// has not confirmed, which is the shape #2065 cannot tolerate.
	SpawnPermissionMode string

	// PostureGate is the ack signal the spawn-time posture write is held against
	// (#2064): armed with the write's locally-minted request_id before the child's
	// stdin is published, released only by that id's success control_response, and
	// consulted by every WriteUserTurn in between. Optional; nil gates nothing.
	//
	// It is minted by the PARSER — (*Parser).PostureGate — because the parser is
	// built before streamsup.New (newStreamRunnerFactory installs it as Config.Stdout)
	// and the release side is the parser's. Minting it there is what makes it
	// impossible to bind the two halves to different gates, which is
	// newSessionParser's argument for minting a parser and its holds together.
	PostureGate *PostureGate

	// onSpawn is an unexported test seam, called once per spawn after cmd.Start
	// and after the stdin handle is stored, with the child's pid. Nil in
	// production. Lets a test observe "child N is up, Stdin() is live" without
	// polling.
	//
	// It fires strictly AFTER the RequestInitializeOnSpawn ask, so when it fires
	// that line is already in the child's pipe. Production cannot tell (the seam is
	// nil there), but it is what makes a test that counts asks deterministic
	// without polling the child's echo.
	onSpawn func(pid int)
}

// PostureGate holds a session's user turns until the child it is armed against has
// CONFIRMED the permission posture the daemon wrote to it (#2064). It is the first
// thing in this package to READ a control ack; the three writers all write and stop.
//
// Its whole state is one string. armedID == "" is the OPEN state, so a gate nobody
// armed is open and an ungated runner behaves exactly as it did before this type
// existed. arm closes it against exactly one locally-minted request_id, release opens
// it only on an exact match, and ready reports open.
//
// A fresh arm OVERWRITES the previous id rather than clearing a separate flag, and
// that is not a compression — it is what makes "a replacement child is never released
// by its predecessor's ack" hold with no bookkeeping. cmd/pyry builds ONE parser per
// session, not per child, so a dead child's buffered stdout can still be parsed after
// its successor has armed; the mismatched id refuses it.
//
// WHAT IT PROVES, AND WHAT IT DOES NOT. A released gate means "claude acked the
// request this daemon minted". It does NOT mean "claude enforces that posture" — an
// echoed posture is the child's claim about itself, the scope boundary
// interactive_stream_inband_bypass_revoke_test.go already draws for the ack it reads.
// A child that lies about its posture defeats this gate, and is also a child that
// could simply ignore the mode; the gate buys protection against BENIGN failure — an
// unsupported mode, a dropped line, a child slow to come up — not against a hostile
// one. Read that limit before treating a released gate as an enforcement guarantee.
//
// Every method is NIL-RECEIVER-SAFE, following sessionModelHold.ModelList's
// precedent, so neither the runner's turn path nor the parser's ack path needs a nil
// branch. The mutex is a LEAF: it is never held across a log call, a write, or
// another lock, and no path in this package holds it together with Runner.mu or
// Runner.restartMu.
type PostureGate struct {
	mu sync.Mutex
	// armedID is the request_id whose success ack opens this gate, or "" when the
	// gate is open. Never logged and never serialised — it is a correlation token,
	// not a capability: the daemon hands it to the child in the request line itself,
	// so there is nothing for unpredictability to buy and nextControlID's short
	// counter keeps the emitted line under PIPE_BUF.
	armedID string
}

// arm closes the gate against requestID, retiring whatever id stood before it. Called
// once per spawn that writes a posture, strictly BEFORE setStdin publishes that
// child's stdin handle — see spawnAndWait for why that ordering is the whole of "no
// user turn reaches the child before the write".
func (g *PostureGate) arm(requestID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.armedID = requestID
	g.mu.Unlock()
}

// release opens the gate if requestID is the one it is armed against, and does
// nothing otherwise. Called from the parser goroutine for every SUCCESS
// control_response; a NAK, an undecodable line and a mismatched id never reach it or
// never match.
//
// There is deliberately NO requestID != "" guard. nextControlID formats an
// already-incremented uint64, so an armed gate's id is never empty and an ack
// carrying no request_id cannot match one; against an already-open gate the
// comparison succeeds and the assignment is a no-op. A guard here would defend a
// state that cannot exist.
func (g *PostureGate) release(requestID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.armedID == requestID {
		g.armedID = ""
	}
	g.mu.Unlock()
}

// ready reports whether turns may flow — that is, whether no unconfirmed posture is
// outstanding. Safe from any goroutine, and cheap enough to sit on the per-turn path.
func (g *PostureGate) ready() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armedID == ""
}

// Runner supervises the claude child lifecycle. Construct with New; drive with
// Run. The held-open stdin handle is exposed via Stdin.
type Runner struct {
	cfg     Config
	log     *slog.Logger
	workDir string // resolved absolute path (agentrun.ResolveWorkdir)

	// mu is a leaf mutex guarding WHICH CHILD, IF ANY, MAY RECEIVE A TURN: stdin
	// (the write end of the live child's StdinPipe) together with the rotation gate
	// (rotating + rotateGen + armFreshSeq). stdin is swapped at spawn/teardown by the Run
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

	// postureGate is cfg.PostureGate, hoisted so the turn path reads one field. Nil
	// is the ungated runner, and every method on the type is nil-receiver-safe, so no
	// call site branches on it.
	postureGate *PostureGate

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
	// A posture with nothing to correlate its ack would be written and then believed
	// on no evidence — the unenforced-write shape the gate exists to prevent — so the
	// misconfiguration fails LOUDLY at construction rather than degrading to a silent
	// ungated write at every spawn. The converse pairing is fine and is the ungated
	// default: a gate with no mode is never armed.
	if cfg.SpawnPermissionMode != "" && cfg.PostureGate == nil {
		return nil, errors.New("streamsup: SpawnPermissionMode requires PostureGate")
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
		cfg:         cfg,
		log:         cfg.Logger,
		workDir:     workDir,
		state:       State{Phase: PhaseStarting},
		args:        slices.Clone(cfg.Args),
		sessionID:   cfg.SessionID,
		spawnMode:   cfg.SpawnPermissionMode,
		postureGate: cfg.PostureGate,
		restartCh:   make(chan struct{}, 1),
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
// The arm also captures the RELEASE-side threshold (#1482): the freshSeq value
// standing when it is placed, against which setStdin measures each binding child.
// That is why this method takes TWO leaf mutexes where it once took one. They are
// taken SEQUENTIALLY AND NEVER NESTED — no path in this package holds two locks at
// once, and that is what keeps the ordering safe. Should nesting ever become
// unavoidable, the order is restartMu → mu, the sequence established here; a
// mu → restartMu nesting is the inversion, and recognisable as one.
//
// The restartMu read comes FIRST, and the mu section then publishes rotating,
// rotateGen and armFreshSeq together. Arming first and stamping second would leave
// the gate observably armed beside a retired arm's threshold, which a bind landing
// in between would compare against — a disarm-by-stale-comparison bug.
//
// Residual window, deliberately left open: between the restartMu read and the mu
// acquisition a DIFFERENT frame's RestartFresh could land and a spawn take its
// snapshot, and that spawn's later bind would clear the arm being placed here.
// Closing it needs both leaf mutexes held at once, which the two-mutex charter
// forbids (see mu and restartMu). It is unreachable from a single new_session frame
// — startFreshRunner calls BeginRotation and RestartFresh in program order on one
// goroutine — and needs two concurrent frames to interleave inside a few
// instructions. The two-frame gap in its documented shape (frame 1's respawn still
// pending when frame 2 arms) IS closed: frame 2's arm reads the already-bumped
// counter, so frame 1's successor snapshot is not strictly greater.
//
// Like RestartFresh it drives only Runner-internal state — leaf mutexes, no
// channel, no call-out — so the sessions layer can call it with no Pool lock held,
// which startFreshRunner does. Safe from any goroutine.
func (r *Runner) BeginRotation() (abort func()) {
	r.restartMu.Lock()
	seq := r.freshSeq
	r.restartMu.Unlock()

	r.mu.Lock()
	r.rotating = true
	r.rotateGen++
	r.armFreshSeq = seq
	gen := r.rotateGen
	r.mu.Unlock()

	// The disarm leaves armFreshSeq alone: a threshold behind a disarmed gate is
	// inert, and the next arm overwrites it.
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
	// The POSTURE gate is read SECOND, in its own acquisition, and the order is the
	// correctness argument rather than a detail (#2064). arm() strictly precedes
	// setStdin, so a handle observed live has already had its child's gate armed;
	// reading the gate first and the handle second would let child N's release admit
	// a turn into child N+1. Two acquisitions rather than one because mu is a leaf and
	// must never be held across another object's lock — and unlike the rotation gate
	// this needs no single-acquisition treatment: turnTarget's two reads answer one
	// question about the same instant, whereas this answer for a GIVEN handle can only
	// become more permissive.
	//
	// Refusing by nilling the target reuses WriteTurn's verbatim contract — the
	// retryable ErrNoLiveChild, ZERO BYTES written, no new envelope construction —
	// which is the same classification the rotation gate above reuses and the one
	// msgqueue and cmd/pyry already agree is worth retrying.
	if w != nil && !r.postureGate.ready() {
		// Outside every acquisition, and INFO not Debug, for the rotation record's
		// reasons verbatim: a slow slog handler must not block the Run goroutine's
		// setStdin, and a Debug record anywhere in the daemon's stderr defeats #1330's
		// e2e instrument guard. The session id is the only field — never the mode
		// (#833 keeps settings values out of the daemon log) and never the request id.
		r.log.Info("streamsup: turn refused; permission posture not yet confirmed by claude",
			"session", r.liveSessionID())
		w = nil
	}
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
// unlike SetSpawnArgs (#1580), RevokeBypass (#1604) and SetPermissionMode (#2042),
// whose consumer is inside
// internal/sessions and which are ON the interface for exactly that reason):
// #1121's interrupt routing reaches it via its own narrow interface or a type
// assertion. It mirrors how
// *supervisor.Supervisor encapsulates SendEsc (#726) without that method being on
// the interface. Safe from any goroutine.
func (r *Runner) Interrupt() error {
	return WriteInterrupt(r.Stdin(), r.nextControlID())
}

// SetPermissionMode writes a single set_permission_mode control_request line to
// the live child's stdin, switching its permission posture WITHOUT killing it.
// #1595 measured the default switch live against claude 2.1.220 and #2041
// measured acceptEdits, dontAsk, plan and auto against 2.1.239 — each acked
// success with the next turn's init.permissionMode echoing the new mode, no
// respawn. The request_id is locally minted from the counter Interrupt and
// RequestInitialize share.
//
// mode is refused unless it is in WritePermissionMode's closed allow-list, which
// is how the enable direction stays off this surface: bypassPermissions is refused
// by NON-MEMBERSHIP, so the literal never appears in production source and every
// unanticipated spelling is refused with it. Re-granting bypass stays on the
// respawn path. The refusal (ErrUnsupportedPermissionMode) is permanent and stays
// errors.Is-distinguishable from the retryable ErrNoLiveChild, which is what a
// caller gets when no child is live — Stdin() is nil then, so nothing is written
// and nothing panics.
//
// Like RevokeBypass and unlike Interrupt it IS on sessions.Runner, and the
// contrast is the rule rather than an exception: Interrupt's dispatch lives in
// cmd/pyry, which can type-assert, whereas this method's consumers sit inside
// internal/sessions, with no such dispatch site. A structural assertion there
// would fail OPEN — its unmatched arm is a silent no-op, leaving the child in the
// wrong posture while the update reports success — so the interface method makes a
// runner that cannot switch a build failure instead.
//
// Stdin() releases r.mu before returning, so the potentially-blocking write never
// holds it. Safe from any goroutine.
func (r *Runner) SetPermissionMode(mode string) error {
	return WritePermissionMode(r.Stdin(), r.nextControlID(), mode)
}

// RevokeBypass drops the live child's bypass posture by asking for the default
// mode. It is SetPermissionMode's named revoke shorthand, kept because it has a
// production caller — Pool.deliverSettingsInBand — whose delivered bytes must not
// change while the mode-carrying method is introduced beside it. #2043 rewrites
// that caller to send an operator-chosen mode, which is the slice where this
// method loses its last caller and can go: a method is deleted alongside the
// consumer that held it, not in the slice whose contract is that the consumer is
// untouched.
//
// It cannot name anything but permissionModeDefault, so the revocation line is
// byte-identical to the one #1595 measured and #1604 has been sending since. Its
// no-live-child contract is SetPermissionMode's, unchanged.
func (r *Runner) RevokeBypass() error {
	return r.SetPermissionMode(permissionModeDefault)
}

// SetSpawnPermissionMode installs the posture EVERY LATER SPAWN asserts, without
// touching the running child (#2064). It is SetPermissionMode's per-spawn twin and
// the contrast is the point: that method changes the live child's posture and writes
// nothing durable, this one changes what the next child is told and writes nothing at
// all.
//
// It exists because Pool.UpdateSettings REBUILDS NO RUNNER — both its branches install
// a recomposed argv onto the live runner and neither reconstructs it — so a posture
// read from the construction-time Config at spawn time goes stale the moment an
// operator changes the session's settings. A crash-respawn would then assert the
// posture the session had at daemon start, which can LOOSEN one the operator has since
// tightened. That is why the pool calls this on the line above its branch split, where
// its own comment already reads "Both branches install newArgs".
//
// Two cheaper shapes do not cover it, and are recorded so they are not re-proposed:
//
//   - Piggybacking the install on SetPermissionMode, which the in-band branch already
//     calls. It covers every mode-to-mode change but NOT an escalation to bypass,
//     which is not in-band-deliverable, takes the restart branch, and never reaches
//     that method — nor could it, since the allow-list refuses the escalation by
//     non-membership and naming it here would put the literal back into production
//     source #1603 deliberately emptied of it.
//   - Deriving the posture from the installed argv. Exact today and dead on arrival
//     under #2065, which takes the mode out of the launch argv entirely.
//
// mode is stored VERBATIM AND UNVALIDATED, including bypassPermissions and including
// the empty string. This is a normalisation-free install of an already-canonical
// stored value, not an operator-input path: the vocabulary gate that matters runs at
// the SPAWN, where permissionModeAllowed decides whether anything is written at all,
// and duplicating it here would be two copies of one defence rather than a second one.
//
// It takes restartMu alone — never mu, never a Pool lock — so the sessions layer can
// call it after releasing Pool.mu with no lock-order concern, exactly as SetSpawnArgs
// can. Safe from any goroutine; non-blocking.
func (r *Runner) SetSpawnPermissionMode(mode string) {
	r.restartMu.Lock()
	r.spawnMode = mode
	r.restartMu.Unlock()
}

// RequestInitialize writes a single initialize control_request line to the live
// child's stdin, asking it to report what the session knows about itself — the
// model list (identifiers, display names, supported reasoning-effort levels) and
// the slash-command list — without a respawn. The request_id is locally minted
// from the same counter Interrupt and RevokeBypass draw on. When no child is live
// Stdin() is nil, so RequestInitialize returns the retryable ErrNoLiveChild
// without writing and without panicking.
//
// It writes the line and stops: the control_response is NOT read here, exactly as
// RevokeBypass writes without reading its ack. The minted id is deliberately not
// returned either — the reader slice that correlates the ack is the first thing
// that needs it, and a return value with no reader is a seam with nothing on the
// far side of it.
//
// The name is RequestInitialize, not Initialize: on a type that already has New, a
// bare Initialize() reads as "initialize the runner", which is the opposite of what
// it does — it asks the CHILD to initialize.
//
// Like Interrupt and unlike RevokeBypass it is NOT on sessions.Runner, and the
// placement rule is the same one in both directions: the interface carries a
// method when its consumer sits inside internal/sessions, where a structural type
// assertion would fail open (RevokeBypass's does — Pool.UpdateSettings). This
// subtype has no consumer at all yet — the trigger lands with the publishing slice
// — so an interface method here would be a seam with nothing on the far side of
// it, and it would pull every fake runner under internal/sessions into the diff.
// Stdin() releases r.mu before returning, so the potentially-blocking write never
// holds it. Safe from any goroutine.
func (r *Runner) RequestInitialize() error {
	return WriteInitialize(r.Stdin(), r.nextControlID())
}

// nextControlID mints the next locally-unique control-request correlation id,
// shared by Interrupt, SetPermissionMode and RequestInitialize (RevokeBypass draws
// on it through SetPermissionMode, minting exactly one id per call, not two). The atomic counter is
// unique within the runner's lifetime — one sequence, not one per subtype, since
// request_id must be unique across all in-flight control requests on the stream
// rather than merely within one subtype. That is all a future ack-correlator
// needs since each runner
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
// The early return sits above the section below, so a no-op does NOT bump freshSeq:
// the rotation gate's release authorisation counts rotations that landed, and no
// production path pairs an arm with an empty-id call (startFreshRunner passes only
// rotate()'s success value, and RotateForNewSession returns ("", err) on every
// failure). If a pool bug ever minted an empty id the arm would outlive it until
// the next rotation — the conservative side, since the alternative authorises a
// spawn that succeeded no rotation at all.
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
	// Bumped in the SAME section as the id rotation, so beginSpawn's snapshot can
	// never skew against it: a spawn's section lands wholly before this one (its
	// snapshot is not the successor's) or wholly after (it is).
	r.freshSeq++
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
// base into a fresh slice, so passing r.args needs no clone — and handing that
// slice header OUT of the section is safe for the same reason setArgsLocked
// clones on the way IN: that clone makes setArgsLocked the sole writer of a
// backing array no installer ever mutates after publication, so a later install
// swaps the header and leaves this spawn's local pointing at the old, immutable
// array.
//
// The transcript probe and the argv assembly deliberately sit BELOW the unlock
// (#1630). restartMu is a leaf — nothing under it may take another lock or do
// synchronous I/O, the invariant setArgsLocked states — and useCreateForm's
// os.Stat is exactly that class of call-out; the mutex it would stall on a hung
// $HOME is the one liveSessionID takes on WriteUserTurn's diagnostic path.
// Moving them out does not re-split #1481's section: the forbidden outcome is a
// live child under a pre-rotation id with NO live iteration cancel, and the
// section still reads the spawn inputs and publishes iterCancel together, so a
// racer arriving after the unlock finds the published cancel and tears this
// spawn down. Only pure assembly moved, into a window that already exists (Run's
// "spawning claude" log and spawnAndWait's setup both run in it). The id the
// probe targets is the one snapshotted in the same section that consumed
// rotatePending, so the decision can never skew against a racing rotation.
//
// forceFirst reports that a fresh-restart request was consumed; the caller re-arms
// its Run-goroutine-private firstRun on true. Returning it rather than a new
// firstRun keeps that local a plain assignment at the call site, where a := would
// shadow it and silently break the started-gated flip.
//
// freshSeq is this spawn's snapshot of the landed-rotation counter, threaded
// through spawnAndWait to setStdin, where it authorises (or refuses) the rotation
// gate's release (#1482). It is a READ inside the section that already exists, not
// a second acquisition, so the one-acquisition-per-spawn-setup charter holds. The
// snapshot must be taken HERE, at spawn SETUP, and never re-read at bind time:
// the crash-respawn this closes runs its beginSpawn before RestartFresh but can
// reach setStdin after it, so a bind-time read would see the bumped value, disarm,
// and be killed moments later by the very rotation that bumped it. "Set up before
// that rotation's RestartFresh landed" is a statement about setup time, and only a
// setup-time snapshot carries it.
// spawnMode is this spawn's snapshot of the posture to assert, threaded through to
// spawnAndWait. It is a READ inside the section that already exists, not a second
// acquisition, so the one-acquisition-per-spawn-setup charter holds — the same
// treatment freshSeq gets above.
func (r *Runner) beginSpawn(ctx context.Context, firstRun bool) (
	iterCtx context.Context, cancel context.CancelFunc, args []string,
	forceFirst bool, freshSeq uint64, spawnMode string,
) {
	// Derived BEFORE the acquisition on purpose: context.WithCancel takes the
	// parent cancelCtx's own internal mutex, and restartMu must never be held
	// across another package's locking. It touches no Runner state, so nothing it
	// does can observe the pre-publish window.
	iterCtx, cancel = context.WithCancel(ctx)

	r.restartMu.Lock()
	forceFirst = r.rotatePending
	r.rotatePending = false
	base, id := r.args, r.sessionID
	freshSeq = r.freshSeq
	spawnMode = r.spawnMode
	r.iterCancel = cancel
	r.restartMu.Unlock()

	args = buildArgs(base, useCreateForm(r.cfg.ClaudeSessionsDir, id, firstRun || forceFirst), id)
	return iterCtx, cancel, args, forceFirst, freshSeq, spawnMode
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
		iterCtx, cancel, args, forceFirst, freshSeq, spawnMode := r.beginSpawn(ctx, firstRun)
		if forceFirst {
			firstRun = true
		}
		// Logged AFTER the section: synchronous log I/O must never run under a leaf
		// mutex. It is also what puts a racer arriving here on the already-published
		// cancel's side of the invariant.
		r.log.Info("spawning claude", "args", args, "workdir", r.workDir)

		start := time.Now()
		started, waitErr := r.spawnAndWait(iterCtx, args, freshSeq, spawnMode)
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
//
// freshSeq is beginSpawn's setup-time snapshot, carried through untouched and
// handed to setStdin as the rotation gate's release authorisation (#1482). Nothing
// here reads or interprets it.
func (r *Runner) spawnAndWait(ctx context.Context, args []string, freshSeq uint64, spawnMode string) (started bool, waitErr error) {
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

	// The posture gate is ARMED BEFORE setStdin, and that ordering is load-bearing:
	// setStdin publishes the live stdin handle and disarms the rotation gate in ONE
	// acquisition, so turns become writable at that instant. Arming after it would
	// leave a window in which a turn is delivered to a child whose posture the daemon
	// has not written, let alone confirmed. The id is minted here so the gate and the
	// line below carry the same one.
	//
	// Whether anything is WRITTEN at all is permissionModeAllowed's single decision. The
	// escalation fails it by NON-MEMBERSHIP, so a bypassPermissions session is sent
	// nothing AND its gate ends up open — its turns flow exactly as they do today,
	// which is the intended reading: a gate no write can ever release is a bricked
	// session, not a fail-closed one. The empty mode of an unconfigured runner fails it
	// the same way, which is what keeps every pre-#2064 construction site
	// byte-identical.
	//
	// THE ARM IS UNCONDITIONAL, and only the id it installs is conditional. Every spawn
	// must publish its OWN gate state, because the gate outlives the child: Restart
	// reuses this Runner and this PostureGate, so a branch that installs nothing leaves
	// the PREDECESSOR's id standing. That is not hypothetical — an un-acked default
	// child followed by an escalation to bypass takes exactly that path (the escalation
	// is not in-band-deliverable, so Pool.UpdateSettings restarts rather than rebuilds),
	// and the bypass child would then refuse every turn forever against an id nothing
	// can ever ack. arm("") installs the OPEN state, which is the same statement
	// armedID's doc already makes; nesting the call merely failed to reach the branch
	// that needs it.
	//
	// An argv-derived interlock — refuse whenever this spawn's args name
	// --dangerously-skip-permissions — was designed and REJECTED. It is exact on
	// today's argv and it would brick #2065, which launches every child in bypass and
	// delivers the posture in-band: an argv-keyed refusal would refuse every downgrade
	// it exists to deliver. The stored posture is kept accurate at its source instead,
	// by SetSpawnPermissionMode.
	postureID := ""
	if permissionModeAllowed(spawnMode) {
		postureID = r.nextControlID()
	}
	r.postureGate.arm(postureID)

	r.setStdin(stdin, freshSeq)
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
func (r *Runner) setStdin(w io.WriteCloser, spawnFreshSeq uint64) {
	r.mu.Lock()
	r.stdin = w
	if spawnFreshSeq > r.armFreshSeq {
		r.rotating = false
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
	r.mu.Unlock()
	return w
}

// useCreateForm reports whether this spawn's id flag should be --session-id
// (create) rather than --resume (reattach). Pure apart from a single os.Stat.
//
// With sessionsDir empty the probe is not consulted at all — no syscall is made
// — and latchCreate decides verbatim. That is what keeps a Runner constructed
// without the directory byte-identical to pre-#1630 argv, which is every
// production path until #1631 threads the directory through. With a directory
// supplied the probe decides OUTRIGHT and latchCreate is ignored, including on
// the FIRST spawn and including when a RestartFresh rotation re-armed first-run
// form: a confirmed by-id hit is the only answer that yields --resume, and every
// other answer — absent file, unreadable directory, an id ValidStem rejects —
// yields the create form. So the probe has no failure mode that can fail a
// spawn.
//
// The single rule is ADR 032's, carried into this package, and it closes two
// separate defects at once. A session that launched but ran no turn establishes
// no transcript (#1655), so the latch's true→false flip would have every respawn
// emit --resume against an id claude has no record of, which exits 1 (#1656) on
// a widening backoff forever; the probe reads absent and creates instead, and
// converges because a rejected --resume leaves NO STUB behind for the next probe
// to latch onto. And on a daemon restart the transcript survives, so the latch's
// first spawn emits --session-id against a live transcript, which claude refuses
// (ADR 032); the probe reads present and resumes. It also settles the
// rotated-id-collides-with-an-existing-transcript case as "resume" simply by
// falling out of the rule — unreachable from sessions.NewID's minting, never
// observed, and deliberately given no branch, flag or test of its own.
//
// Two things this must not become, both one edit away and both load-bearing.
// It probes via transcript.StatByID and never a hand-rolled
// filepath.Join(sessionsDir, id+ext) + os.Stat: StatByID runs its ValidStem gate
// BEFORE the join, and New checks only that SessionID is non-empty while
// RestartFresh re-checks nothing, so a non-canonical id reaches here and a
// hand-rolled join would turn it into an arbitrary-path existence oracle that
// flips the spawn's id flag. And every non-hit falls back to the create form,
// NEVER to a directory scan: transcript.Newest sits beside StatByID and answers
// a superficially similar question, but #839 deleted --continue and the
// adopt-by-mtime scan precisely to close the confused-deputy gap where a restart
// adopts a DIFFERENT claude's newer transcript out of the shared sessions dir.
func useCreateForm(sessionsDir, id string, latchCreate bool) bool {
	if sessionsDir == "" {
		return latchCreate
	}
	// The error is discarded because absence is the expected answer here, not a
	// failure: StatByID reports every miss as the zero Result, so !Found() already
	// covers the absent file, the unreadable directory and the invalid stem. A
	// separate err != nil arm would be a return site no fixture can reach on its
	// own.
	res, _ := transcript.StatByID(sessionsDir, id)
	return !res.Found()
}

// buildArgs assembles one spawn's argv: the fixed stream-json prefix, then the
// caller's base args, then the id flag. create picks that flag's form:
// --session-id <sessionID> establishes the on-disk transcript under a known id,
// --resume <sessionID> reattaches to one that already exists (append, no fork).
// The caller decides which, and in production that decision is useCreateForm's —
// a by-id transcript existence probe when Config.ClaudeSessionsDir is set, the
// Run loop's firstRun latch when it is not.
// Passing the SAME sessionID to both is why the on-disk id is stable across a
// kill-and-restart — plain --resume reuses the id and does not fork
// (--fork-session is the explicit, unused opt-in). Never emits -p/--print: the
// non-print choice is billing-tied and spike-verified (multi-turn, interrupt,
// resume, and the approval round-trip all work without it). Pure — no Runner
// state, and it never mutates base.
func buildArgs(base []string, create bool, sessionID string) []string {
	args := make([]string, 0, len(base)+7)
	args = append(args,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
	)
	args = append(args, base...)
	if create {
		return append(args, "--session-id", sessionID)
	}
	return append(args, "--resume", sessionID)
}
