package agentrun

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// Default sleep timings, lifted verbatim from the Phase A spike
// (/tmp/agent-run-spike/pty_drive.py). Exposed via DriveConfig so tests
// override them down to ~ms.
const (
	defaultTrustDialogDelay = 2500 * time.Millisecond
	defaultPromptDelay      = 3500 * time.Millisecond
)

// DriveConfig parameterises Drive. Callers build the full claude argv at
// the call site (see cmd/pyry/agent_run.go's buildClaudeArgs); Drive owns
// nothing about flag shape.
type DriveConfig struct {
	ClaudeBin string       // required; claude executable path
	WorkDir   string       // required; child cwd
	Args      []string     // full claude argv (without argv[0])
	Logger    *slog.Logger // optional; defaults to slog.Default()

	// Env is appended to os.Environ() in the spawned child. Tests use this
	// to thread TestHelperProcess wiring; production leaves it nil.
	Env []string

	// Timings exposed for tunability and unit-test ergonomics. Zero values
	// fall back to the spike-validated defaults.
	TrustDialogDelay time.Duration
	PromptDelay      time.Duration

	// PromptBytes is the user-turn text typed into the PTY after PromptDelay.
	// Drive appends a single "\r" after these bytes; callers MUST NOT
	// include a trailing CR. Operator-controlled; not sanitised — control
	// sequences here flow into claude's TUI parser. Trust boundary: pyry
	// trusts its operator-supplied prompt file the same way it trusts
	// --workdir.
	PromptBytes []byte
}

// Drive spawns claude with the agent-run argv, drives a single user-turn
// via the PTY, background-drains the PTY output (so claude does not block
// once its output buffer fills), and waits for the child to exit (typically
// via ctx cancel → SIGTERM → SIGKILL grace).
//
// Returns nil on a clean exit (or ctx-cancel-driven teardown — operator
// SIGTERM is success at this verb). *exec.ExitError on non-zero child exit
// that was NOT triggered by ctx cancellation.
func Drive(ctx context.Context, cfg DriveConfig) error {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	trustDelay := cfg.TrustDialogDelay
	if trustDelay == 0 {
		trustDelay = defaultTrustDialogDelay
	}
	promptDelay := cfg.PromptDelay
	if promptDelay == 0 {
		promptDelay = defaultPromptDelay
	}

	cmd, ptmx, err := supervisor.SpawnPTY(ctx, supervisor.SpawnConfig{
		Bin:     cfg.ClaudeBin,
		Args:    cfg.Args,
		WorkDir: cfg.WorkDir,
		Env:     cfg.Env,
		Logger:  logger,
	})
	if err != nil {
		return fmt.Errorf("agentrun: drive: spawn: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// Background-drain. Without this, claude blocks once its output buffer
	// fills (~64 KB on the kernel-side line discipline). Goroutine exits
	// when the deferred ptmx.Close above fails its Read.
	go func() {
		_, _ = io.Copy(io.Discard, ptmx)
	}()

	if !sleepOrCancel(ctx, trustDelay) {
		return waitAndMap(ctx, cmd)
	}
	if _, err := ptmx.Write([]byte{'\r'}); err != nil {
		logger.Warn("agentrun: drive: trust-dialog write failed", "err", err)
	}

	if !sleepOrCancel(ctx, promptDelay) {
		return waitAndMap(ctx, cmd)
	}
	// Single combined write to match the spike's byte-identical drive shape.
	payload := append(append([]byte(nil), cfg.PromptBytes...), '\r')
	if _, err := ptmx.Write(payload); err != nil {
		logger.Warn("agentrun: drive: prompt write failed", "err", err)
	}

	return waitAndMap(ctx, cmd)
}

// sleepOrCancel returns true if the full delay elapsed, false if ctx was
// cancelled first. Wakes promptly on cancel so the caller can skip the
// pending PTY write.
func sleepOrCancel(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// waitAndMap collapses the child's exit error to the verb-level contract:
// nil on clean exit or operator-driven ctx cancellation, the underlying
// error otherwise. ctx.Err() takes precedence — once the operator has
// asked for shutdown, any resulting signal-exit is success at this verb.
func waitAndMap(ctx context.Context, cmd *exec.Cmd) error {
	err := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return err
}
