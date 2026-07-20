// Package supervisor runs Claude Code under process supervision: spawn in a
// PTY, stream I/O transparently to the controlling terminal, and restart the
// child with exponential backoff when it exits.
//
// Phase 0 is deliberately narrow — it replaces the current tmux + bash
// restart loop. Future phases will add a control socket, multi-session
// routing, and in-process integrations for Channels, knowledge capture,
// memsearch, and crons.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/turncommit"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
	"golang.org/x/term"
)

// goroutineDrainTimeout caps how long runOnce waits for the I/O bridge
// goroutines after the child exits and the bridges have been closed. Both
// should drain promptly; the timeout is a safety net.
const goroutineDrainTimeout = 100 * time.Millisecond

// transcriptConfirmTimeout bounds the post-delivery wait for the resolved claude
// transcript to grow on the growth-confirm path (Config.ResolveTranscript set).
// Generous on purpose: claude appends the user turn to its JSONL at commit time,
// so a real commit shows growth within ~1-2s; the margin covers a slow
// --continue resume drain. A too-tight value risks a false negative (turn
// committed late) → phone retry → duplicate turn. Tuning knob, not a contract;
// bump if a slow-resume false negative is ever observed. Always capped further by
// the caller's ctx (the handler's 30s deliver budget).
const transcriptConfirmTimeout = 10 * time.Second

// transcriptConfirmPoll is the growth poll interval — matches tui-driver's
// promptCommitPoll.
const transcriptConfirmPoll = 150 * time.Millisecond

// ErrNoLiveSession is returned by WriteUserTurn when no claude session is
// registered at delivery time — the supervisor is between runOnce iterations,
// has not yet spawned, or is mid-restart. Formerly this was a silent drop
// (return nil); it is now a loud failure so the relay send_message handler
// reports it to the phone instead of acking a turn that never reached claude.
var ErrNoLiveSession = errors.New("supervisor: no live session")

// ErrTurnNotCommitted is returned by WriteUserTurn when DeliverPrompt
// delivered the turn but could not confirm a commit (DeliverResult.Committed
// == false) after its bounded recovery — the turn may still be wedged. Like
// ErrNoLiveSession it maps to a loud failure rather than a false ack.
var ErrTurnNotCommitted = errors.New("supervisor: turn not committed")

// ErrTrustModalPending is returned by the delivery path when claude's startup
// trust-folder modal is up: the queued turn is deliberately NOT delivered into
// the consent gate. Retryable — the caller (msgqueue) holds the head and
// retries; a valid remote accept (Supervisor.AcceptTrust) clears the modal and a
// later attempt delivers. #1014 keys its typed session error + give-up exemption
// off it (errors.Is).
var ErrTrustModalPending = errors.New("supervisor: trust modal pending")

// Phase describes the supervisor's current lifecycle state.
type Phase string

const (
	PhaseStarting Phase = "starting" // before the first child has been spawned
	PhaseRunning  Phase = "running"  // a child process is alive
	PhaseBackoff  Phase = "backoff"  // waiting before the next restart
	PhaseStopped  Phase = "stopped"  // Run has returned
)

// State is a snapshot of the supervisor's runtime state. Returned by
// (*Supervisor).State for the control plane.
type State struct {
	Phase        Phase         // current lifecycle phase
	ChildPID     int           // PID of the running child, or 0 when none
	StartedAt    time.Time     // when the supervisor entered Run
	RestartCount int           // number of times the child has exited
	LastUptime   time.Duration // uptime of the most recent child, zero if first run
	NextBackoff  time.Duration // delay scheduled before the next spawn, zero when running
}

// Config controls a Supervisor instance.
type Config struct {
	// ClaudeBin is the path to the claude binary. Defaults to "claude" (found on PATH).
	ClaudeBin string

	// WorkDir is the working directory for the claude child process.
	// Empty means the supervisor's current directory.
	WorkDir string

	// ResumeLast causes restarts after the first run to pass --continue to
	// claude. Claude resumes the most recent session for the working directory,
	// so conversation history survives supervisor restarts and crashes.
	//
	// We use --continue rather than --resume <id> so that if the user runs
	// /clear inside claude (which rotates the session ID on disk), the next
	// restart picks up the post-clear session rather than reattaching to the
	// orphaned pre-clear one. Bare --resume would open claude's interactive
	// session picker — usable interactively but wrong for an unattended
	// supervisor restart.
	ResumeLast bool

	// ResolveSessionID, when non-nil, is called at the start of every spawn.
	// A non-empty return appends "--session-id <id>" to the claude args and
	// suppresses --continue (the two are mutually exclusive). Resolved fresh
	// each spawn so a /clear id rotation is picked up on the next restart.
	// Nil preserves the ResumeLast/--continue behaviour (per-caller sessions,
	// foreground tests). Used by the daemon's bootstrap session (#839) to
	// resume its OWN deterministic id rather than the most-recent session in a
	// shared sessions dir.
	ResolveSessionID func() string

	// SessionID is the caller-minted session id, set eagerly at construction.
	// The PTY supervisor DELIBERATELY IGNORES it — it resolves its own id lazily
	// via ResolveSessionID every spawn (so a /clear rotation is picked up). This
	// field exists only so an alternative RunnerFactory (the stream-json path,
	// #1109) can read a construction-safe, non-empty id from the same
	// supervisor.Config it is handed; streamsup requires its id at construction.
	// Because the supervisor never reads it, the PTY spawn path stays
	// byte-identical whether or not it is set — that is the rollback guarantee.
	// Construction-fixed: it does NOT mirror a /clear rotation (see spec #1108).
	SessionID string

	// ClaudeArgs are forwarded to the claude binary as positional arguments.
	ClaudeArgs []string

	// Bridge, if non-nil, mediates PTY I/O instead of bridging to the
	// supervisor's own stdin/stdout. Used in service mode (no controlling
	// terminal): an attaching client (e.g. `pyry attach`) can take over
	// the bridge to interact with the child. When nil, the supervisor
	// runs in foreground mode and bridges PTY I/O directly to its own
	// stdin/stdout — current behavior.
	Bridge *Bridge

	// Logger is used for structured logging.
	Logger *slog.Logger

	// Backoff parameters. Zero values use sensible defaults.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffReset   time.Duration

	// ValidateConversation, if non-nil, is invoked by WriteUserTurn before
	// any state mutation or PTY write. A non-nil return is propagated
	// verbatim — production wiring returns conversations.ErrConversationNotFound
	// for unknown ids; the supervisor stays decoupled from that package by
	// receiving the sentinel through the closure. When nil, WriteUserTurn
	// skips validation (test ergonomics).
	ValidateConversation func(id string) error

	// ResolveTranscript, when non-nil, resolves the newest claude transcript for
	// the supervised workdir: its absolute path and current byte size. ("", 0,
	// nil) means no transcript exists yet (valid — a fresh session that has
	// written no turn). A non-nil error means the dir is unreadable.
	//
	// When set, deliverViaSession uses transcript *growth* as a deterministic
	// commit signal instead of trusting DeliverResult.Committed (tui-driver's
	// chip heuristic, which false-acks short single-line turns lost to a
	// --continue restart race). When nil, the #594 Committed-based behaviour is
	// preserved (foreground mode, tests). Modelled on ValidateConversation:
	// optional, nil-safe, the production-vs-test seam.
	ResolveTranscript func(ctx context.Context) (path string, size int64, err error)

	// RecordDir, when non-empty, records each interactive-session spawn to a
	// unique 0600 .cast file under this directory via tui-driver's
	// SpawnOpts.RecordTo (#802). Empty (the default) attaches no recorder and
	// leaves the spawn byte-identical to the pre-recording behaviour. The
	// directory is created 0700 on demand; tui-driver owns the file (0600
	// O_EXCL, header, close-on-Close). Only the daemon's bootstrap session
	// sets this; per-caller sessions leave it empty.
	RecordDir string

	// helperEnv is extra environment variables appended to the child process
	// environment. Used only in tests (TestHelperProcess pattern).
	helperEnv []string
}

// Supervisor owns a single Claude Code child process and restarts it on exit.
type Supervisor struct {
	cfg Config
	log *slog.Logger

	mu    sync.Mutex
	state State

	// convMu guards currentConvID. Leaf-only; never held while acquiring
	// sessMu or mu.
	convMu        sync.Mutex
	currentConvID string

	// sessMu guards sess and sessReadyCh. Leaf-only; never held while
	// acquiring convMu or mu. setSession (called from runOnce) and
	// WriteUserTurn (called from arbitrary handler goroutines) serialize
	// through this lock.
	sessMu sync.Mutex
	sess   *tuidriver.Session
	// sessReadyCh is closed by setSession when a non-nil Session is
	// registered; freshened (re-opened) by setSession(nil) so subsequent
	// WaitForPTY waiters block again until the next runOnce iteration hosts a
	// new Session. WaitForPTY captures the channel reference under sessMu then
	// awaits it unlocked — same pattern as Session.activeCh.
	sessReadyCh chan struct{}

	// deliverFn delivers a captured user turn through a live Session: ready-gate
	// (WaitReady) → commit-confirm + corrupted-paste recovery (DeliverPrompt) →
	// Committed check. Set once in New to (*Supervisor).deliverViaSession and
	// overridden only in tests (the same unexported-injection seam as
	// helperEnv). Immutable post-New, so WriteUserTurn reads it lock-free. The
	// seam keeps every WriteUserTurn branch unit-testable without a live claude:
	// it returns an already-classified error/success, so no claude-screen
	// literal (idle prompt, spinner, pasted-text chip) leaks into pyrycode — all
	// that knowledge stays inside tui-driver.
	deliverFn func(ctx context.Context, sess *tuidriver.Session, payload []byte) error

	// keystrokeFn actuates one abstract modal keystroke against a captured live
	// Session. Set once in New to sendModalKeystroke; overridden only in tests —
	// the same unexported-injection seam as deliverFn — because the real
	// tui-driver calls nil-deref the PTY on a zero-value Session, so verb
	// dispatch cannot otherwise be unit-tested without a live claude. Immutable
	// post-New, so the modal methods read it lock-free.
	keystrokeFn func(sess *tuidriver.Session, k modalKey, choice string) error

	// settingsWarningFn reports whether claude's informational Settings Warning
	// startup dialog is on the captured Session's screen. Set once in New to
	// detectSettingsWarning; overridden only in tests — the same
	// unexported-injection seam as keystrokeFn — because the real detector renders
	// the live snapshot and nil-derefs a zero-value Session's PTY. Immutable
	// post-New, so waitReadyAutoContinue reads it lock-free.
	settingsWarningFn func(sess *tuidriver.Session) bool

	// restartMu guards the live-restart state below (#842). Leaf-only: Restart
	// releases it before calling the captured cancel, and Run's liveArgs/
	// setIterCancel take only this lock. Never nested with mu/sessMu/convMu — so
	// no lock-order edge is added to the Pool.mu → Session.lcMu discipline the
	// sessions layer relies on.
	restartMu sync.Mutex
	// claudeArgs is the live spawn arg list. Initialised in New from
	// cfg.ClaudeArgs and read (via liveArgs) at the top of each Run iteration
	// instead of cfg.ClaudeArgs, so Restart can swap the argv a running child
	// relaunches with. When Restart is never called this is a clone of
	// cfg.ClaudeArgs and the spawn is byte-identical to the pre-#842 behaviour.
	claudeArgs []string
	// iterCancel cancels the current Run iteration's derived ctx; set at
	// iteration start, cleared (nil) after runOnce returns. Restart calls it to
	// force the running child to exit so the restart loop relaunches. nil means
	// no live iteration to interrupt (between iterations, in backoff, or Run not
	// executing).
	iterCancel context.CancelFunc
	// restartCh (buffered 1) carries the "a deliberate restart was requested"
	// hint. Restart sends on it; Run consumes it either in the post-runOnce
	// drain (skip backoff) or the backoff-wait select (interrupt the wait) —
	// exactly once per token. Allocated once in New.
	restartCh chan struct{}
}

// State returns a snapshot of the current supervisor state. Safe to call from
// any goroutine.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// WorkDir returns the working directory the supervised claude was spawned in
// (Config.WorkDir, immutable post-New, so no lock — unlike State which guards
// mutable state). Empty when the supervisor was built with no WorkDir (inherits
// the process cwd). The turn bridge derives a conversation's per-Cwd transcript
// directory from it: claude writes <session-id>.jsonl under
// ~/.claude/projects/<encoded-WorkDir>/, so this is the authoritative,
// byte-exact spawn cwd to encode (#686).
func (s *Supervisor) WorkDir() string {
	return s.cfg.WorkDir
}

func (s *Supervisor) updateState(fn func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.state)
}

// WriteUserTurn delivers a user-turn payload to the supervised claude child,
// tagged with the caller's conversation_id, and returns nil only when claude
// confirms the turn committed. It is the delivery path for untrusted,
// phone-originated turns (relay send_message → here → live claude); a turn that
// cannot be confirmed delivered is a loud failure, never a silent ack.
//
// Validation, when configured via Config.ValidateConversation, runs first.
// A non-nil validator result is returned verbatim — production wiring returns
// conversations.ErrConversationNotFound for unknown ids, which the handler maps
// to a wire-level refusal code. The cursor (CurrentConversation) is stamped to
// id before delivery on every validated call — including when delivery then
// fails — but is NOT mutated when validation refuses the id.
//
// Delivery captures the live Session under sessMu, releases the lock, then
// delivers on the captured pointer (WaitReady + DeliverPrompt can run for
// seconds; holding sessMu that long would block runOnce's teardown). A
// concurrent setSession(nil)+Close racing the captured pointer is safe: it
// lands in tui-driver's teardown-safe PTY-error path (no panic), so a session
// torn down mid-delivery surfaces here as a loud failure, never a crash or a
// false ack. This relaxes — without breaking — runOnce's setSession(nil)-
// before-Close ordering: a racing WriteUserTurn may now deliver against a
// closing session, but only into that error path.
//
// Failure modes — no live session (ErrNoLiveSession), claude not idle within
// the caller's ctx, an uncommitted/wedged turn (ErrTurnNotCommitted), or a PTY
// write error — all return non-nil, wrapped with the stable "supervisor: write
// user turn:" prefix. The underlying error is preserved for errors.Is checks
// (including context.DeadlineExceeded / context.Canceled through WaitReady).
func (s *Supervisor) WriteUserTurn(ctx context.Context, id string, payload []byte) error {
	if s.cfg.ValidateConversation != nil {
		if err := s.cfg.ValidateConversation(id); err != nil {
			return err
		}
	}

	s.convMu.Lock()
	s.currentConvID = id
	s.convMu.Unlock()

	s.sessMu.Lock()
	sess := s.sess
	s.sessMu.Unlock()

	if sess == nil {
		return fmt.Errorf("supervisor: write user turn: %w", ErrNoLiveSession)
	}
	if err := s.deliverFn(ctx, sess, payload); err != nil {
		return fmt.Errorf("supervisor: write user turn: %w", err)
	}
	return nil
}

// deliverViaSession is the production deliverFn. It gates on claude reaching
// idle (WaitReady), then delivers the turn and confirms the commit. The
// WaitReady idle gate is load-bearing: a blocking trust/network condition that
// prevents idle simply surfaces as a WaitReady ctx timeout → loud failure, so no
// trust/mcp/network policy branch is needed on this long-lived supervised path.
//
// Commit confirmation has two modes:
//
//   - Config.ResolveTranscript == nil (foreground / tests): trust
//     DeliverResult.Committed — DeliverPrompt's "no pasted-text chip ⇒
//     committed-but-slow" heuristic, sufficient with the idle gate in front.
//   - Config.ResolveTranscript != nil (the relay-driven --continue bootstrap):
//     ignore Committed and confirm via deterministic transcript *growth*. That
//     heuristic false-acks the short single-line first mobile turn when it is
//     lost to claude's ~7.5s --continue restart racing the send (#668): no chip
//     ever renders for a typed prompt, so DeliverPrompt reports a false commit.
//     Growth (the resolved JSONL got bigger, or a /clear-rotated newer file
//     appeared) is the only reliable signal; tui-driver's appearance-based
//     JSONLPath is not — under --continue the per-session JSONL already exists.
//
// JSONLPath in DeliverOpts stays empty in both modes: we own the growth signal
// now, and the supervisor holds no stable claude session UUID anyway (--continue,
// not --session-id; claude rotates the on-disk UUID on /clear). All claude-screen
// knowledge stays inside tui-driver; this method sees only classified
// errors, the Committed bool, and file sizes — never JSONL content.
func (s *Supervisor) deliverViaSession(ctx context.Context, sess *tuidriver.Session, payload []byte) error {
	deliver := func(ctx context.Context) (bool, error) {
		// The queue-driven delivery path carries a commit gate on ctx (#487). It is
		// called here — after WaitReady, before the write — to CLAIM the queued head
		// for writing. A false claim means the head was dropped during the idle-gate
		// wait, so abort without writing: a dropped message must never be typed into
		// claude. A nil gate (the non-queue paths, e.g. ACP) writes unconditionally.
		if gate := turncommit.From(ctx); gate != nil && !gate() {
			return false, turncommit.ErrDropped
		}
		res, err := sess.DeliverPrompt(ctx, tuidriver.DeliverOpts{
			Prompt: string(payload),
			Logger: s.log,
		})
		return res.Committed, err
	}

	if s.cfg.ResolveTranscript == nil {
		if err := waitReadyAutoContinue(ctx, s.readyDeps(sess)); err != nil {
			return fmt.Errorf("wait ready: %w", err)
		}
		committed, err := deliver(ctx)
		if err != nil {
			return err
		}
		if !committed {
			return ErrTurnNotCommitted
		}
		return nil
	}

	return confirmViaTranscriptGrowth(ctx, deliverGrowthDeps{
		waitReady: func(ctx context.Context) error {
			return waitReadyAutoContinue(ctx, s.readyDeps(sess))
		},
		deliver: deliver,
		resolve: s.cfg.ResolveTranscript,
		log:     s.log,
		timeout: transcriptConfirmTimeout,
		poll:    transcriptConfirmPoll,
	})
}

// readyForDelivery classifies claude's readiness one level above the concrete
// Session (so it is unit-testable with a fake waitReady) and decides whether a
// queued turn may be delivered. It reads readiness only — it marks nothing
// trusted and sends no keystroke:
//
//   - a waitReady error is returned unwrapped; the caller owns the "wait ready:
//     %w" wrap (both delivery modes apply it);
//   - a pending startup trust modal (Readiness.TrustModal) yields
//     ErrTrustModalPending BEFORE any delivery, so the queued (untrusted) turn is
//     never typed into claude's last consent gate (#988). The msgqueue retry loop
//     holds the head; a valid remote accept via Supervisor.AcceptTrust clears the
//     modal and a later attempt delivers;
//   - otherwise (idle, no trust modal) it returns nil and delivery may proceed.
func readyForDelivery(ctx context.Context, waitReady func(context.Context) (tuidriver.Readiness, error)) error {
	readiness, err := waitReady(ctx)
	if err != nil {
		return err
	}
	if readiness.TrustModal {
		return ErrTrustModalPending
	}
	return nil
}

// deliverGrowthDeps are the seams confirmViaTranscriptGrowth drives.
// deliverViaSession wires the real Session methods + Config.ResolveTranscript;
// tests inject fakes to script readiness, delivery, and transcript-growth
// outcomes with no live claude and no claude-screen literal — the same
// "seam one level above the screen" pattern as deliverFn (#594). timeout/poll
// are fields rather than package vars so parallel -race tests can shrink them
// without sharing mutable global state.
type deliverGrowthDeps struct {
	waitReady func(ctx context.Context) error                                // wraps Session.WaitReady
	deliver   func(ctx context.Context) (committed bool, err error)          // wraps Session.DeliverPrompt
	resolve   func(ctx context.Context) (path string, size int64, err error) // Config.ResolveTranscript
	log       *slog.Logger
	timeout   time.Duration // post-delivery growth-wait budget; defaults to transcriptConfirmTimeout
	poll      time.Duration // growth poll interval; defaults to transcriptConfirmPoll
}

// confirmViaTranscriptGrowth delivers a turn and returns nil only after it
// observes the resolved claude transcript grow past a pre-delivery baseline —
// the deterministic commit signal that replaces tui-driver's stochastic chip
// heuristic on the supervised-bootstrap path (#668). No growth within the
// bounded window → ErrTurnNotCommitted (a loud, retryable failure), never a
// false ack. Runs synchronously on the caller's goroutine; the poll is fully
// bounded by timeout and ctx.
func confirmViaTranscriptGrowth(ctx context.Context, d deliverGrowthDeps) error {
	log := d.log
	if log == nil {
		log = slog.Default()
	}
	timeout := d.timeout
	if timeout <= 0 {
		timeout = transcriptConfirmTimeout
	}
	poll := d.poll
	if poll <= 0 {
		poll = transcriptConfirmPoll
	}

	if err := d.waitReady(ctx); err != nil {
		return fmt.Errorf("wait ready: %w", err)
	}

	// Baseline is captured after WaitReady returns idle, so any JSONL writes
	// claude makes during the --continue resume are already counted and cannot be
	// mistaken for the user turn.
	basePath, baseSize, berr := d.resolve(ctx)
	if berr != nil {
		// No baseline → we cannot use growth. Fall back to the Committed
		// heuristic rather than manufacture a false negative that would drive a
		// duplicate send.
		log.Warn("supervisor: transcript baseline resolve failed; falling back to commit heuristic", "err", berr)
		committed, err := d.deliver(ctx)
		if err != nil {
			return err
		}
		if !committed {
			return ErrTurnNotCommitted
		}
		return nil
	}

	// Ignore Committed on the growth path — it is the unreliable heuristic we are
	// replacing. A delivery (PTY write) error is still loud.
	if _, err := d.deliver(ctx); err != nil {
		return err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return ErrTurnNotCommitted
		case <-ticker.C:
			newPath, newSize, err := d.resolve(ctx)
			if err == nil && grew(basePath, baseSize, newPath, newSize) {
				return nil
			}
		}
	}
}

// grew reports whether the newest transcript advanced past the pre-delivery
// baseline: a larger file (turn appended) or a different newest file (/clear
// rotation). A still-empty resolution (newPath == "") is not growth.
func grew(basePath string, baseSize int64, newPath string, newSize int64) bool {
	return newPath != "" && (newPath != basePath || newSize > baseSize)
}

// CurrentConversation returns the most recently written conversation_id, or
// "" when no WriteUserTurn has been accepted yet. Safe for concurrent use.
// Survives child restarts; the cursor is in-memory state on the supervisor,
// not tied to a particular runOnce iteration.
func (s *Supervisor) CurrentConversation() string {
	s.convMu.Lock()
	defer s.convMu.Unlock()
	return s.currentConvID
}

// ScreenSnapshot renders the current claude screen to plain text. live is false
// when no claude child is attached (between restarts, mid-spawn, or idle-
// evicted); text is "" then. Safe for concurrent use and non-blocking: it
// captures the live Session under sessMu (a pointer read), releases the lock,
// then does a bounded in-memory VT100 render — no I/O, no channel ops — so it
// never wedges the caller. It backs the relay's on-demand request_snapshot
// (ADR 025 § Safe degradation), the parser-independent live-view escape hatch.
//
// SECURITY: the raw bytes from sess.Snapshot() are consumed by tuidriver.Render
// in the same expression and are never named or stored in pyrycode, so no
// claude-screen literal enters this package (cmd/substrate-guard stays green).
// The render runs inside the tui-driver seal; pyrycode forwards only the opaque
// rendered text. 0,0 selects tui-driver's default grid, matching the daemon
// PTY's allocation, so the render is 1:1 in headless mode.
func (s *Supervisor) ScreenSnapshot() (text string, live bool) {
	s.sessMu.Lock()
	sess := s.sess
	s.sessMu.Unlock()
	if sess == nil {
		return "", false
	}
	return tuidriver.Render(sess.Snapshot(), 0, 0), true
}

// Session returns the currently-hosted tui-driver Session, or nil when no
// claude child is attached (between restarts, mid-spawn, or idle-evicted).
// Safe for concurrent use; captures the pointer under sessMu, mirroring
// ScreenSnapshot's capture. The event-stream producer (internal/turnbridge)
// subscribes to the returned session's Events() stream.
//
// SECURITY: returning *tuidriver.Session does not breach the substrate seal.
// The supervisor already holds and uses this pointer; the producer only calls
// sess.Events() (typed events) — never MirrorOutput()/Snapshot() (raw bytes) —
// so no claude-screen literal enters pyrycode (cmd/substrate-guard stays green).
func (s *Supervisor) Session() *tuidriver.Session {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	return s.sess
}

// setSession registers (or clears, when sess is nil) the hosted Session for
// the current runOnce iteration. setSession(nil) before the actual Close means
// a WriteUserTurn that captures the pointer afterwards sees nil and fails loud.
// Note the relaxation since #594: WriteUserTurn captures the Session under
// sessMu then releases the lock before delivering, so a WriteUserTurn that
// captured the pointer just before this clear may still deliver against a
// closing session — but only into tui-driver's teardown-safe PTY-error path
// (no panic), which surfaces as a loud failure, never a crash or a false ack.
// Mirrors Bridge.SetResizer.
//
// sessReadyCh choreography: setSession(non-nil) closes the readiness channel
// (idempotent — close is a no-op when already closed); setSession(nil)
// allocates a fresh open channel (idempotent — leaves the channel alone
// when it is already open). WaitForPTY captures the chan reference under
// sessMu and awaits it unlocked.
func (s *Supervisor) setSession(sess *tuidriver.Session) {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	s.sess = sess
	if sess != nil {
		select {
		case <-s.sessReadyCh:
			// already closed
		default:
			close(s.sessReadyCh)
		}
		return
	}
	select {
	case <-s.sessReadyCh:
		s.sessReadyCh = make(chan struct{})
	default:
		// already open
	}
}

// WaitForPTY blocks until the supervisor has a live Session (registered by
// setSession on the next runOnce iteration), or ctx is cancelled. Returns nil
// on readiness, ctx.Err() on cancel. Safe from any goroutine; idempotent on
// an already-live Session (returns immediately).
//
// Session.Activate calls this at the tail of its waiting phase so callers
// that follow Activate with WriteUserTurn observe a live Session rather than
// the ~hundreds-of-ms gap between transitionTo closing activeCh and runOnce
// hosting the new Session.
func (s *Supervisor) WaitForPTY(ctx context.Context) error {
	s.sessMu.Lock()
	ch := s.sessReadyCh
	s.sessMu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// New constructs a Supervisor from a Config, applying defaults.
func New(cfg Config) (*Supervisor, error) {
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = "claude"
	}
	if _, err := exec.LookPath(cfg.ClaudeBin); err != nil {
		return nil, fmt.Errorf("claude binary not found: %w", err)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.BackoffInitial == 0 {
		cfg.BackoffInitial = 500 * time.Millisecond
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = 30 * time.Second
	}
	if cfg.BackoffReset == 0 {
		cfg.BackoffReset = 60 * time.Second
	}
	s := &Supervisor{
		cfg:         cfg,
		log:         cfg.Logger,
		state:       State{Phase: PhaseStarting},
		sessReadyCh: make(chan struct{}),
		claudeArgs:  slices.Clone(cfg.ClaudeArgs),
		restartCh:   make(chan struct{}, 1),
	}
	s.deliverFn = s.deliverViaSession
	s.keystrokeFn = sendModalKeystroke
	s.settingsWarningFn = detectSettingsWarning
	return s, nil
}

// Restart swaps the claude spawn args and, if a child is currently running,
// forces it to exit so the restart loop relaunches with the new args. When no
// child is running it only swaps the args — they take effect on the session's
// next spawn (e.g. the next Activate). Non-blocking, fire-and-forget: the
// supervisor's forever-retry loop guarantees the relaunch (§ crash recovery).
// Safe from any goroutine.
//
// It drives only supervisor-internal state (restartMu, a ctx cancel, a buffered
// channel); it never touches Pool.mu or Session.lcMu, so the sessions layer can
// call it after releasing Pool.mu without any lock-order concern (#842).
//
// The restartCh hint is always sent (non-blocking): a restart during a run
// makes the post-runOnce drain skip backoff, and a restart during backoff (no
// live child to cancel) breaks the backoff wait. Coalescing is correct — two
// rapid restarts overwrite claudeArgs with the newest value and collapse to the
// single buffered token, forcing one relaunch with the latest args.
func (s *Supervisor) Restart(args []string) {
	s.restartMu.Lock()
	s.claudeArgs = slices.Clone(args)
	cancel := s.iterCancel
	s.restartMu.Unlock()

	// Hint first, then kill, so Run's post-runOnce drain observes the token even
	// if the child exits the instant it is cancelled.
	select {
	case s.restartCh <- struct{}{}:
	default:
	}
	if cancel != nil {
		cancel()
	}
}

// liveArgs returns a clone of the live spawn args under restartMu. Run reads it
// (rather than cfg.ClaudeArgs) at the top of each iteration so a Restart-swapped
// argv takes effect on the next spawn.
func (s *Supervisor) liveArgs() []string {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return slices.Clone(s.claudeArgs)
}

// setIterCancel publishes (or clears, when c is nil) the current iteration's
// cancel func under restartMu so Restart can interrupt the running child.
func (s *Supervisor) setIterCancel(c context.CancelFunc) {
	s.restartMu.Lock()
	s.iterCancel = c
	s.restartMu.Unlock()
}

// Run supervises the claude child until ctx is cancelled. Each iteration spawns
// claude in a PTY, streams I/O, and waits for exit. On exit it applies
// exponential backoff before respawning. The backoff counter resets if a child
// stayed up longer than Config.BackoffReset.
//
// Each iteration runs on a derived ctx (iterCtx) so a Restart can cancel a
// single child without tearing Run down. Shutdown is therefore detected from
// the parent ctx (ctx.Err()), not from the child-exit error: an iterCtx-only
// cancel (a settings restart) falls through to relaunch, while a parent cancel
// returns. A deliberate restart skips backoff (it is not a crash).
func (s *Supervisor) Run(ctx context.Context) error {
	bo := newBackoffTimer(s.cfg.BackoffInitial, s.cfg.BackoffMax, s.cfg.BackoffReset)
	firstRun := true

	startedAt := time.Now()
	s.updateState(func(st *State) {
		st.Phase = PhaseStarting
		st.StartedAt = startedAt
	})
	defer s.updateState(func(st *State) {
		st.Phase = PhaseStopped
		st.ChildPID = 0
		st.NextBackoff = 0
	})

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		sessionID := ""
		if s.cfg.ResolveSessionID != nil {
			sessionID = s.cfg.ResolveSessionID()
		}
		args := buildClaudeArgs(s.liveArgs(), firstRun, s.cfg.ResumeLast, sessionID)

		start := time.Now()
		s.log.Info("spawning claude", "args", args, "workdir", s.cfg.WorkDir)
		onSpawn := func(pid int) {
			s.updateState(func(st *State) {
				st.Phase = PhaseRunning
				st.ChildPID = pid
				st.NextBackoff = 0
			})
		}
		iterCtx, cancel := context.WithCancel(ctx)
		s.setIterCancel(cancel)
		err := s.runOnce(iterCtx, args, onSpawn)
		cancel()
		s.setIterCancel(nil)
		uptime := time.Since(start)

		// Shutdown is a parent-ctx cancel, NOT any child-exit error: an
		// iterCtx-only cancel (a Restart kill) leaves ctx.Err() nil and falls
		// through to relaunch. Return value stays a context error for the
		// graceful-shutdown contract.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			s.log.Warn("claude exited", "err", err, "uptime", uptime)
		} else {
			s.log.Info("claude exited cleanly", "uptime", uptime)
		}

		firstRun = false

		// A deliberate restart is not a crash: skip backoff and relaunch
		// immediately with the swapped args.
		if s.drainRestart() {
			continue
		}

		delay := bo.next(uptime)

		s.updateState(func(st *State) {
			st.Phase = PhaseBackoff
			st.ChildPID = 0
			st.RestartCount++
			st.LastUptime = uptime
			st.NextBackoff = delay
		})

		s.log.Info("restarting after backoff", "delay", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		case <-s.restartCh:
			// A restart arrived while the child was already down and we were
			// waiting out the backoff — relaunch now with the swapped args.
		}
	}
}

// drainRestart non-blockingly consumes a pending deliberate-restart hint,
// reporting whether one was present. Used post-runOnce to decide whether to
// skip the crash backoff.
func (s *Supervisor) drainRestart() bool {
	select {
	case <-s.restartCh:
		return true
	default:
		return false
	}
}

// buildClaudeArgs builds claude's argument list for one spawn. When sessionID
// is non-empty it appends "--session-id <sessionID>" and does NOT prepend
// --continue (the two are mutually exclusive) — the deterministic resume the
// bootstrap session uses (#839). When sessionID is empty it prepends --continue
// on every spawn after the first, when continueLast is enabled. Pure function —
// no Supervisor state, easy to unit-test. Never mutates claudeArgs.
func buildClaudeArgs(claudeArgs []string, firstRun, continueLast bool, sessionID string) []string {
	args := append([]string(nil), claudeArgs...)
	if sessionID != "" {
		return append(args, "--session-id", sessionID)
	}
	if !firstRun && continueLast {
		args = append([]string{"--continue"}, args...)
	}
	return args
}

// sessionWriter adapts a *tuidriver.Session to io.Writer so the input pump
// can stay io.Copy(sessionWriter{sess}, src) — mirroring the old
// io.Copy(ptmx, src). Write forwards bytes to the session's raw-input seam
// (AttachInput → pty.Write) and reports the full slice written on success so
// io.Copy keeps draining.
type sessionWriter struct{ sess *tuidriver.Session }

func (w sessionWriter) Write(p []byte) (int, error) {
	if err := w.sess.AttachInput(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// spawnOpts builds the tui-driver SpawnOpts for one interactive-session spawn.
// recordDir == "" (the default / debug_capture OFF) yields exactly
// SpawnOpts{MirrorOutput: true} — RecordTo is the zero value "", byte-identical
// to the pre-recording behaviour and no recorder is attached. A non-empty
// recordDir attaches the cast recorder at a unique .cast path under it. Pure:
// no I/O, no side effects — the AC4 "assert the unset case explicitly" seam.
func spawnOpts(recordDir string, now time.Time) tuidriver.SpawnOpts {
	opts := tuidriver.SpawnOpts{MirrorOutput: true}
	if recordDir != "" {
		opts.RecordTo = recordingPath(recordDir, now)
	}
	return opts
}

// recordingPath returns a unique .cast path under dir stamped from now. tui-driver
// (via SpawnOpts.RecordTo) opens it 0600 O_EXCL, writes the asciinema header, and
// closes it on Session.Close; the supervisor owns only path selection. The
// nanosecond-precision UTC stamp is collision-free across the backoff restart loop
// (spawns are separated by fork/exec/PTY-alloc wall-clock ≥ BackoffInitial); the
// O_EXCL open is the deterministic backstop, self-healing via the next fresh stamp.
func recordingPath(dir string, now time.Time) string {
	return filepath.Join(dir, now.UTC().Format("20060102T150405.000000000Z")+".cast")
}

// claudeChildCmd assembles the exec.Cmd for the supervisor's claude child:
// workdir, base environment plus helperEnv, then tuidriver.EnsureClaudeEnv for
// substrate hygiene. EnsureClaudeEnv does two load-bearing things here:
//
//   - It strips the Claude Code nesting markers (CLAUDECODE,
//     CLAUDE_CODE_SESSION_ID, CLAUDE_CODE_CHILD_SESSION,
//     CLAUDE_CODE_ENTRYPOINT). When the daemon itself was started from inside
//     a Claude Code session — an operator terminal, a test harness, an e2e
//     script — those markers leak through os.Environ() into the child, and a
//     claude that inherits them treats itself as a nested child session and
//     SILENTLY writes no session transcript while answering turns normally.
//     The growth-confirm then never confirms, the drain re-sends the same
//     turn forever, and the turn bridge has no JSONL to tail. Verified live
//     on claude 2.1.199. The agent-run path (ptyrunner) has always scrubbed
//     via the same helper; this closes the interactive-path gap.
//
//   - It pins TERM=xterm-256color, the rendering tui-driver's detection
//     corpus is calibrated against. This REVERSES an earlier deliberate skip
//     ("would change claude's TUI rendering versus today's inherited TERM"):
//     the daemon's inherited TERM is absent under launchd and arbitrary under
//     a terminal, and every screen this supervisor reads (WaitReady, modal
//     classes, commit chips) is parsed by tui-driver, so the calibrated TERM
//     is the correct one.
//
// The TUIDRIVER_* arg-appending sides of EnsureClaudeEnv are opt-in env vars,
// unset in daemon contexts, so argv is unchanged.
func claudeChildCmd(ctx context.Context, bin string, args []string, workDir string, helperEnv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = append(os.Environ(), helperEnv...)
	return tuidriver.EnsureClaudeEnv(cmd)
}

// runOnce hosts claude through a tui-driver Session, bridges its I/O to the
// controlling terminal (or the configured Bridge in service mode), and returns
// when the child exits or ctx is cancelled. onSpawn, if non-nil, is called once
// with the child PID after the Session has been spawned.
func (s *Supervisor) runOnce(ctx context.Context, args []string, onSpawn func(pid int)) error {
	cmd := claudeChildCmd(ctx, s.cfg.ClaudeBin, args, s.cfg.WorkDir, s.cfg.helperEnv)

	// Host claude through a tui-driver Session. MirrorOutput is the only
	// output path now — the Session seals the PTY *os.File privately, so both
	// modes forward sess.MirrorOutput() to their output sink instead of
	// io.Copy'ing a raw master.
	//
	// Opt-in debug capture (#802): when cfg.RecordDir is set (operator flipped
	// the persisted debug_capture flag), attach tui-driver's cast recorder to
	// this one spawn. A MkdirAll failure degrades to no-capture — the safe
	// direction (no recording) that also keeps the daemon alive; never fail the
	// spawn over an unwritable debug sink. RecordDir empty → spawnOpts leaves
	// RecordTo "" → byte-identical to the pre-recording behaviour.
	recordDir := s.cfg.RecordDir
	if recordDir != "" {
		if err := os.MkdirAll(recordDir, 0o700); err != nil {
			s.log.Warn("debug_capture: recordings dir unavailable; continuing without capture", "dir", recordDir, "err", err)
			recordDir = ""
		}
	}
	sess, err := tuidriver.Spawn(cmd, spawnOpts(recordDir, time.Now()))
	if err != nil {
		return fmt.Errorf("spawn: %w", err)
	}

	if onSpawn != nil && cmd.Process != nil {
		onSpawn(cmd.Process.Pid)
	}

	if s.cfg.Bridge != nil {
		// Service mode: route I/O through the bridge so an attaching client
		// can take over interactively. No raw-mode setup and no server-side
		// SIGWINCH watcher — those belong to whichever client attaches and
		// apply to its own terminal. Handshake/live-resize geometry reaches
		// claude via Bridge.Resize → Session.Resize.
		//
		// BeginIteration/EndIteration scope the bridge's input cancel so the
		// input goroutine returns cleanly when the child exits, instead of
		// leaking and racing the next iteration's goroutine for queued
		// attach-client bytes. SetResizer(sess) registers the resize delegate;
		// SetResizer(nil) runs BEFORE EndIteration so an in-flight Resize sees
		// nil rather than a closing session.
		s.cfg.Bridge.BeginIteration()
		s.cfg.Bridge.SetResizer(sess)
		s.setSession(sess)
		done := make(chan error, 2)
		go func() {
			_, err := io.Copy(sessionWriter{sess}, s.cfg.Bridge)
			done <- err
		}()
		go func() {
			for chunk := range sess.MirrorOutput() {
				_, _ = s.cfg.Bridge.Write(chunk)
			}
			done <- nil
		}()

		waitErr := sess.Wait()
		// Clear registrations before closing the session so a racing
		// WriteUserTurn/Resize sees nil and drops/no-ops rather than touching
		// a closing session. EndIteration makes the bridge input pump return
		// EOF; sess.Close closes the PTY, which closes MirrorOutput and ends
		// the output pump (already drained/closed by the time Wait returns).
		s.setSession(nil)
		s.cfg.Bridge.SetResizer(nil)
		s.cfg.Bridge.EndIteration()
		_ = sess.Close()
		for i := 0; i < 2; i++ {
			select {
			case <-done:
			case <-time.After(goroutineDrainTimeout):
			}
		}
		return waitErr
	}

	// Foreground mode: bridge directly to the supervisor's own terminal.
	//
	// Register the Session for WriteUserTurn. setSession(nil) below runs
	// before sess.Close so a racing WriteUserTurn sees nil and drops.
	s.setSession(sess)

	// Put the controlling terminal into raw mode if it is a TTY so that
	// keystrokes pass through unmodified to the child.
	stdinFd := int(os.Stdin.Fd())
	var restoreTerm func()
	if term.IsTerminal(stdinFd) {
		oldState, err := term.MakeRaw(stdinFd)
		if err == nil {
			restoreTerm = func() { _ = term.Restore(stdinFd, oldState) }
		}
	}
	defer func() {
		if restoreTerm != nil {
			restoreTerm()
		}
	}()

	// Keep the PTY window size in sync with the real terminal via Session.Resize.
	stopResize := s.watchWindowSize(sess)
	defer stopResize()

	// Open /dev/tty as a separate fd for the input bridge. When the child
	// exits we Close this fd, the in-flight Read returns, and the input
	// goroutine drains cleanly. Reading os.Stdin directly would leave the
	// goroutine blocked on os.Stdin's fdMutex — see #78.
	input, inputErr := openTTYInput()
	if inputErr != nil {
		s.log.Debug("foreground: /dev/tty unavailable, falling back to os.Stdin",
			"err", inputErr)
		input = stdinFallback{}
	}
	defer func() { _ = input.Close() }()

	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(sessionWriter{sess}, input)
		done <- err
	}()
	go func() {
		for chunk := range sess.MirrorOutput() {
			_, _ = os.Stdout.Write(chunk)
		}
		done <- nil
	}()

	waitErr := sess.Wait()
	// Clear the session before closing so a racing WriteUserTurn drops;
	// input.Close() drains the input pump; sess.Close closes the PTY, which
	// closes MirrorOutput and ends the output pump (already drained/closed by
	// the time Wait returns).
	s.setSession(nil)
	_ = input.Close()
	_ = sess.Close()

	// Drain both. Under normal operation each returns within microseconds
	// of its source closing; the timeout is a safety net.
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(goroutineDrainTimeout):
			s.log.Warn("io bridge goroutine drain timeout")
		}
	}
	return waitErr
}

// openTTYInput returns a reader for the controlling terminal. The returned
// ReadCloser is owned by the caller and must be Closed to unblock any
// in-flight Read (typically the input-bridge goroutine).
//
// /dev/tty is opened with O_NONBLOCK so the Go runtime poller mediates the
// Read. Without it, syscall.Read blocks in the kernel and Close on another
// goroutine has no way to interrupt it (POSIX close-during-read is a no-op
// for a blocking fd). With it, Read returns EAGAIN, the runtime parks the
// goroutine on the poller, and Close wakes it via runtime_pollUnblock —
// which is the whole reason we open /dev/tty rather than reusing os.Stdin.
//
// On platforms or in environments where /dev/tty is unavailable (headless
// processes, certain containers), it returns the platform open error
// verbatim. Foreground mode is TTY-only by construction.
func openTTYInput() (io.ReadCloser, error) {
	return os.OpenFile("/dev/tty", os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

// stdinFallback adapts os.Stdin to io.ReadCloser with a no-op Close. Used
// only when /dev/tty is unavailable; preserves the legacy stdin-bound
// goroutine leak in that environment rather than breaking the process by
// closing os.Stdin.
type stdinFallback struct{}

func (stdinFallback) Read(p []byte) (int, error) { return os.Stdin.Read(p) }
func (stdinFallback) Close() error               { return nil }
