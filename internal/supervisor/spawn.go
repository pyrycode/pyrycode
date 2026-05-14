package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// SpawnConfig is the minimum surface SpawnPTY needs. Mirrors the relevant
// subset of Config; intentionally separate so callers that do not want a
// full Supervisor (one-shot agent-run) do not pull in backoff/bridge/state
// fields.
type SpawnConfig struct {
	Bin     string       // executable path; required
	Args    []string     // argv (without argv[0])
	WorkDir string       // optional; empty means inherit
	Env     []string     // appended to os.Environ()
	Logger  *slog.Logger // optional; defaults to slog.Default()
}

// spawnWaitDelay bounds how long the runtime waits for the child to exit
// after ctx cancellation triggers SIGTERM before it sends SIGKILL.
const spawnWaitDelay = 5 * time.Second

// SpawnPTY launches Bin in a PTY using exec.CommandContext + pty.Start. On
// ctx cancel, SIGTERM is forwarded via cmd.Cancel; cmd.WaitDelay enforces a
// SIGKILL grace. Caller owns lifecycle — must Wait the *exec.Cmd and Close
// the returned *os.File.
//
// Callers that need different cancel/grace timings overwrite cmd.Cancel /
// cmd.WaitDelay on the returned *exec.Cmd before calling Wait.
func SpawnPTY(ctx context.Context, cfg SpawnConfig) (*exec.Cmd, *os.File, error) {
	cmd := exec.CommandContext(ctx, cfg.Bin, cfg.Args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = spawnWaitDelay

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("supervisor: pty start: %w", err)
	}
	return cmd, ptmx, nil
}
