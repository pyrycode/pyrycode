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
}

// State returns a snapshot of the current supervisor state. Safe to call from
// any goroutine.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Supervisor) updateState(fn func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.state)
}

// WriteUserTurn delivers a user-turn payload to the supervised claude child,
// tagged with the caller's conversation_id. The cursor (CurrentConversation)
// is updated to id on every accepted call — including when no child is
// currently active (the bytes are dropped silently in that case, matching
// Bridge.Write's discard-on-unattached behaviour). The cursor is NOT mutated
// when validation refuses the id.
//
// Validation, when configured via Config.ValidateConversation, runs first.
// A non-nil validator result is returned verbatim — production wiring
// returns conversations.ErrConversationNotFound for unknown ids, which the
// handler maps to a wire-level refusal code.
//
// PTY write failures are wrapped with a stable "supervisor: write user
// turn:" prefix; the underlying error is preserved for errors.Is checks.
func (s *Supervisor) WriteUserTurn(id string, payload []byte) error {
	if s.cfg.ValidateConversation != nil {
		if err := s.cfg.ValidateConversation(id); err != nil {
			return err
		}
	}

	s.convMu.Lock()
	s.currentConvID = id
	s.convMu.Unlock()

	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	if s.sess == nil {
		return nil
	}
	if err := s.sess.AttachInput(payload); err != nil {
		return fmt.Errorf("supervisor: write user turn: %w", err)
	}
	return nil
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

// setSession registers (or clears, when sess is nil) the hosted Session for
// the current runOnce iteration. WriteUserTurn writes to it via AttachInput;
// setSession(nil) before the actual Close ensures an in-flight WriteUserTurn
// sees nil rather than a closing session. Mirrors Bridge.SetResizer.
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
	return &Supervisor{
		cfg:         cfg,
		log:         cfg.Logger,
		state:       State{Phase: PhaseStarting},
		sessReadyCh: make(chan struct{}),
	}, nil
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
