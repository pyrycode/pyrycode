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
	"sync"
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

	// mu is a leaf mutex guarding stdin, the write end of the live child's
	// StdinPipe. It is swapped at spawn/teardown by the Run goroutine and read
	// by Stdin() from #1088's writer goroutine, so every access is serialised.
	mu    sync.Mutex
	stdin io.WriteCloser
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
		cfg:     cfg,
		log:     cfg.Logger,
		workDir: workDir,
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

// Run supervises the claude child until ctx is cancelled. Each iteration spawns
// claude, holds its stdin open, and waits for exit. On a crash it applies the
// exponential backoff before respawning with --resume; the backoff resets after
// a child that stayed up longer than BackoffReset. Shutdown is a parent-ctx
// cancel (not the child-exit error): Run returns ctx.Err() cleanly.
func (r *Runner) Run(ctx context.Context) error {
	bo := newBackoffTimer(r.cfg.BackoffInitial, r.cfg.BackoffMax, r.cfg.BackoffReset)
	firstRun := true

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		args := buildArgs(r.cfg.Args, firstRun, r.cfg.SessionID)
		r.log.Info("spawning claude", "args", args, "workdir", r.workDir)

		start := time.Now()
		waitErr := r.spawnAndWait(ctx, args)
		uptime := time.Since(start)

		// Shutdown is a parent-ctx cancel, NOT the child-exit error: a clean
		// graceful-shutdown return. (This slice has no live-restart seam like
		// supervisor.Restart, so the child-exit error only ever means a crash.)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if waitErr != nil {
			r.log.Warn("claude exited", "err", waitErr, "uptime", uptime)
		} else {
			r.log.Info("claude exited", "uptime", uptime)
		}

		firstRun = false
		delay := bo.next(uptime)
		r.log.Info("restarting after backoff", "delay", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// spawnAndWait spawns one claude child, stores its held-open stdin, and blocks
// until it exits. It returns the child's exit error (nil on clean exit, an
// *exec.ExitError on a crash, or a wrapped spawn-setup failure) — the Run loop
// distinguishes shutdown from crash via ctx.Err(), not this value.
func (r *Runner) spawnAndWait(ctx context.Context, args []string) error {
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
		return fmt.Errorf("streamsup: stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close() // best-effort: nothing consumed it, child never ran
		return fmt.Errorf("streamsup: start: %w", err)
	}

	r.setStdin(stdin)
	if r.cfg.onSpawn != nil {
		r.cfg.onSpawn(cmd.Process.Pid)
	}

	waitErr := cmd.Wait()

	// The child has exited (crash) or been torn down (cancel). Drop the handle
	// so Stdin() reports no live child, then close it best-effort — cmd.Wait
	// already closed the parent write end, so a broken-pipe / already-closed
	// error here is expected and benign (avoid a spurious WARN).
	if old := r.takeStdin(); old != nil {
		if cerr := old.Close(); cerr != nil && !agentrun.ExitErrIsBenign(cerr) {
			r.log.Warn("streamsup: stdin close failed", "err", cerr)
		}
	}

	return waitErr
}

// setStdin publishes the live child's stdin write end under the leaf mutex.
func (r *Runner) setStdin(w io.WriteCloser) {
	r.mu.Lock()
	r.stdin = w
	r.mu.Unlock()
}

// takeStdin clears the stored stdin handle and returns the previous value so
// the caller can close it outside the lock (a slow close never blocks Stdin()).
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
