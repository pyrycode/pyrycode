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
	"sync"
	"syscall"
	"time"

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
		res, err := sess.DeliverPrompt(ctx, tuidriver.DeliverOpts{
			Prompt: string(payload),
			Logger: s.log,
		})
		return res.Committed, err
	}

	if s.cfg.ResolveTranscript == nil {
		if _, err := sess.WaitReady(ctx); err != nil {
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
			_, err := sess.WaitReady(ctx)
			return err
		},
		deliver: deliver,
		resolve: s.cfg.ResolveTranscript,
		log:     s.log,
		timeout: transcriptConfirmTimeout,
		poll:    transcriptConfirmPoll,
	})
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
	}
	s.deliverFn = s.deliverViaSession
	s.keystrokeFn = sendModalKeystroke
	return s, nil
}

// Run supervises the claude child until ctx is cancelled. Each iteration spawns
// claude in a PTY, streams I/O, and waits for exit. On exit it applies
// exponential backoff before respawning. The backoff counter resets if a child
// stayed up longer than Config.BackoffReset.
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

		args := buildClaudeArgs(s.cfg.ClaudeArgs, firstRun, s.cfg.ResumeLast)

		start := time.Now()
		s.log.Info("spawning claude", "args", args, "workdir", s.cfg.WorkDir)
		onSpawn := func(pid int) {
			s.updateState(func(st *State) {
				st.Phase = PhaseRunning
				st.ChildPID = pid
				st.NextBackoff = 0
			})
		}
		err := s.runOnce(ctx, args, onSpawn)
		uptime := time.Since(start)

		switch {
		case errors.Is(err, context.Canceled):
			return err
		case err != nil:
			s.log.Warn("claude exited", "err", err, "uptime", uptime)
		default:
			s.log.Info("claude exited cleanly", "uptime", uptime)
		}

		firstRun = false
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
		}
	}
}

// buildClaudeArgs prepends --continue to claude's argument list on every spawn
// after the first, when ResumeLast is enabled. Pure function — no Supervisor
// state, easy to unit-test.
func buildClaudeArgs(claudeArgs []string, firstRun, continueLast bool) []string {
	args := append([]string(nil), claudeArgs...)
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

// runOnce hosts claude through a tui-driver Session, bridges its I/O to the
// controlling terminal (or the configured Bridge in service mode), and returns
// when the child exits or ctx is cancelled. onSpawn, if non-nil, is called once
// with the child PID after the Session has been spawned.
func (s *Supervisor) runOnce(ctx context.Context, args []string, onSpawn func(pid int)) error {
	cmd := exec.CommandContext(ctx, s.cfg.ClaudeBin, args...)
	if s.cfg.WorkDir != "" {
		cmd.Dir = s.cfg.WorkDir
	}
	cmd.Env = append(os.Environ(), s.cfg.helperEnv...)

	// Host claude through a tui-driver Session. MirrorOutput is the only
	// output path now — the Session seals the PTY *os.File privately, so both
	// modes forward sess.MirrorOutput() to their output sink instead of
	// io.Copy'ing a raw master. We deliberately do NOT call
	// tuidriver.EnsureClaudeEnv: it force-overrides TERM=xterm-256color, which
	// would change claude's TUI rendering versus today's inherited TERM. That
	// override exists for downstream screen parsing (#596), which this swap
	// does not do — behaviour-preservation decision.
	sess, err := tuidriver.Spawn(cmd, tuidriver.SpawnOpts{MirrorOutput: true})
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
