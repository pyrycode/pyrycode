// Package streamrunner spawns claude with the stream-json input/output
// formats as a plain subprocess (no PTY), writes one user-turn JSON envelope
// to its stdin, forwards stdout/stderr to caller-supplied writers, and
// waits for the child to exit.
//
// `pyry agent-run` uses streamrunner to drive claude in
// `--input-format stream-json --output-format stream-json` mode from a
// single shot of stdin; the package owns the spawn, the stdin write, and
// the wait — nothing else.
//
// The package logs only error messages and never logs prompt bytes or any
// substring of the stream-json event stream — writers are opaque io.Writer
// values and the package does not parse what passes through them.
//
// Dependency direction: the package must not import
// github.com/pyrycode/pyrycode/internal/supervisor (the PTY helper) nor any
// of the other agentrun subpackages. Verify with:
//
//	go list -deps ./internal/agentrun/streamrunner/... | grep pyrycode/internal/supervisor
//
// Expected output: empty.
package streamrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// killGrace is the SIGTERM → SIGKILL grace window applied via
// exec.Cmd.WaitDelay when ctx is cancelled. Hardcoded per AC#2 ("no timing
// knobs"); mirrors the budget package's 5s default for symmetry.
const killGrace = 5 * time.Second

// Config configures Run. Required fields are validated at entry; zero
// values for optional fields fall through to documented defaults.
type Config struct {
	// ClaudeBin is the resolved path to the claude executable. Required.
	ClaudeBin string

	// WorkDir is the child process's working directory. Required.
	WorkDir string

	// Args is the full claude argv (excluding argv[0]). The caller
	// assembles flag shape — Run does not know about claude's flags.
	Args []string

	// PromptBytes is the user-turn prompt text. UTF-8; may contain quotes,
	// newlines, or control characters — Run JSON-encodes it, so embedded
	// metacharacters are safe. May be empty (a valid no-op turn).
	PromptBytes []byte

	// Stdout receives the child's stdout. Required.
	//
	// MUST NOT block — a writer that stalls will deadlock cmd.Wait()
	// because the stdlib's internal forwarder blocks on writing into it
	// while the child is still running.
	Stdout io.Writer

	// Stderr receives the child's stderr. Required. Same non-blocking
	// requirement as Stdout.
	Stderr io.Writer

	// Env is appended to os.Environ() in the child process. Optional;
	// production callers leave it nil. Tests use it to thread
	// TestHelperProcess wiring.
	Env []string

	// Logger is used for the small number of Warn-level diagnostics this
	// package emits (stdin write/close failures). Optional; nil falls
	// back to slog.Default().
	Logger *slog.Logger
}

// userTurn is the stream-json envelope written to claude's stdin. The shape
// matches the 2026-05-14 probe:
//
//	{"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}
type userTurn struct {
	Type    string          `json:"type"`
	Message userTurnMessage `json:"message"`
}

type userTurnMessage struct {
	Role    string                `json:"role"`
	Content []userTurnContentText `json:"content"`
}

type userTurnContentText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Run spawns claude with cfg.Args, writes one stream-json user-turn
// envelope to its stdin and closes stdin, forwards stdout/stderr to the
// configured writers, and waits for the child to exit.
//
// Return value contract:
//
//   - nil on a clean (exit 0) child termination.
//   - nil on ctx-cancel-driven teardown — operator shutdown is success.
//   - *exec.ExitError on non-zero child exit not triggered by ctx cancel.
//   - a wrapped error from pre-Start setup (stdin pipe, spawn).
//
// On ctx cancel, the child receives SIGTERM and is given killGrace to
// exit before stdlib follows up with SIGKILL. This is wired via
// exec.Cmd.Cancel + exec.Cmd.WaitDelay — no goroutine or timer is owned by
// this package.
func Run(ctx context.Context, cfg Config) error {
	if cfg.ClaudeBin == "" {
		return errors.New("streamrunner: ClaudeBin required")
	}
	if cfg.WorkDir == "" {
		return errors.New("streamrunner: WorkDir required")
	}
	if cfg.Stdout == nil {
		return errors.New("streamrunner: Stdout required")
	}
	if cfg.Stderr == nil {
		return errors.New("streamrunner: Stderr required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	envelope, err := marshalEnvelope(cfg.PromptBytes)
	if err != nil {
		return fmt.Errorf("streamrunner: marshal envelope: %w", err)
	}

	cmd := exec.CommandContext(ctx, cfg.ClaudeBin, cfg.Args...)
	cmd.Dir = cfg.WorkDir
	cmd.Stdout = cfg.Stdout
	cmd.Stderr = cfg.Stderr
	if cfg.Env != nil {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	// Override stdlib's default (SIGKILL) so claude gets a graceful
	// SIGTERM first; WaitDelay handles the SIGKILL fallback after grace.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = killGrace

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("streamrunner: stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("streamrunner: start: %w", err)
	}

	// Single ~150-byte synchronous write; no goroutine needed. Failures
	// are logged-and-continued — the child's exit status is the
	// authoritative outcome. Identical pattern to drive.go's PTY writes.
	if _, err := stdin.Write(envelope); err != nil {
		logger.Warn("streamrunner: stdin write failed", "err", err)
	}
	if err := stdin.Close(); err != nil {
		logger.Warn("streamrunner: stdin close failed", "err", err)
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return waitErr
}

// marshalEnvelope returns the single newline-terminated JSON line written
// to claude's stdin. The newline matches the probe's `echo '…' |` shape.
func marshalEnvelope(prompt []byte) ([]byte, error) {
	env := userTurn{
		Type: "user",
		Message: userTurnMessage{
			Role: "user",
			Content: []userTurnContentText{{
				Type: "text",
				Text: string(prompt),
			}},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
