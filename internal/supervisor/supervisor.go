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
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

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

	// Logger is used for structured logging.
	Logger *slog.Logger

	// Backoff parameters. Zero values use sensible defaults.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffReset   time.Duration

	// helperEnv is extra environment variables appended to the child process
	// environment. Used only in tests (TestHelperProcess pattern).
	helperEnv []string
}

// Supervisor owns a single Claude Code child process and restarts it on exit.
type Supervisor struct {
	cfg Config
	log *slog.Logger
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
	return &Supervisor{cfg: cfg, log: cfg.Logger}, nil
}

// Run supervises the claude child until ctx is cancelled. Each iteration spawns
// claude in a PTY, streams I/O, and waits for exit. On exit it applies
// exponential backoff before respawning. The backoff counter resets if a child
// stayed up longer than Config.BackoffReset.
func (s *Supervisor) Run(ctx context.Context) error {
	bo := newBackoffTimer(s.cfg.BackoffInitial, s.cfg.BackoffMax, s.cfg.BackoffReset)
	firstRun := true

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		args := buildClaudeArgs(s.cfg.ClaudeArgs, firstRun, s.cfg.ResumeLast)

		start := time.Now()
		s.log.Info("spawning claude", "args", args, "workdir", s.cfg.WorkDir)
		err := s.runOnce(ctx, args)
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

// runOnce spawns claude in a PTY, bridges it to the controlling terminal,
// and returns when the child exits or ctx is cancelled.
func (s *Supervisor) runOnce(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, s.cfg.ClaudeBin, args...)
	if s.cfg.WorkDir != "" {
		cmd.Dir = s.cfg.WorkDir
	}
	cmd.Env = append(os.Environ(), s.cfg.helperEnv...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty start: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

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

	// Keep the PTY window size in sync with the real terminal.
	stopResize := s.watchWindowSize(ptmx)
	defer stopResize()

	// Bridge stdin/stdout.
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(ptmx, os.Stdin)
		done <- err
	}()
	go func() {
		_, err := io.Copy(os.Stdout, ptmx)
		done <- err
	}()

	waitErr := cmd.Wait()
	// After the child exits, unblock the copy goroutines.
	_ = ptmx.Close()
	// Drain at least one of the copy results to avoid leaking a goroutine for
	// a read that will never complete (stdin).
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
	}
	return waitErr
}
