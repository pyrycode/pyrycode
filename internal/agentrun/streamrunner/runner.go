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
// The package logs only diagnostic messages and never logs prompt bytes or
// any substring of the stream-json event stream. It tee-parses stdout for
// STRUCTURAL fields only — the top-level `type` of each event line, to drive
// the idle-stall watchdog (see watchdog.go) — and never inspects, retains, or
// logs event content.
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

	"github.com/pyrycode/pyrycode/internal/agentrun"
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
	// package emits (stdin write/close failures, idle-stall watchdog fire).
	// Optional; nil falls back to slog.Default().
	Logger *slog.Logger

	// IdleTimeout overrides the idle-stall watchdog threshold (the silence
	// budget while claude owes an assistant turn). Optional; zero or negative
	// falls back to the idleStall default. Production leaves it unset; tests
	// shrink it so a stall fires in milliseconds.
	IdleTimeout time.Duration
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
// An idle-stall watchdog (see watchdog.go) runs alongside: it tee-parses
// stdout for structural `type` fields and, if the stream sits silent past
// cfg.IdleTimeout *while claude owes an assistant turn*, kills claude and
// makes Run synthesise a distinct, retryable `idle_stall` result line on
// stdout (claude never emits its own `result` on a watchdog kill).
//
// Return value contract:
//
//   - nil on a clean (exit 0) child termination.
//   - nil on operator-shutdown teardown (parent ctx cancel) — success.
//   - nil on an idle-stall watchdog kill — the synthetic result line on
//     stdout is the signal; pyry exits clean and the dispatcher retries.
//   - *exec.ExitError on non-zero child exit not triggered by a cancel.
//   - a wrapped error from pre-Start setup (stdin pipe, spawn).
//
// On either cancel (operator shutdown of the parent ctx, or the watchdog
// cancelling its child ctx) the child receives SIGTERM and is given killGrace
// to exit before stdlib follows up with SIGKILL — wired via exec.Cmd.Cancel +
// exec.Cmd.WaitDelay.
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

	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = idleStall
	}
	runStart := time.Now()

	envelope, err := marshalEnvelope(cfg.PromptBytes)
	if err != nil {
		return fmt.Errorf("streamrunner: marshal envelope: %w", err)
	}

	// childCtx is distinct from the parent ctx so the watchdog can kill the
	// run (cancelChild) without it looking like operator shutdown — operator
	// SIGTERM/SIGINT cancels the parent ctx, which still propagates here and
	// stays a clean no-result teardown.
	childCtx, cancelChild := context.WithCancel(ctx)
	defer cancelChild()

	// Tee-parse stdout for structural `type` fields to drive the watchdog;
	// bytes pass through unchanged.
	parser := newStreamParser(cfg.Stdout, nil)

	cmd := exec.CommandContext(childCtx, cfg.ClaudeBin, cfg.Args...)
	cmd.Dir = cfg.WorkDir
	cmd.Stdout = parser
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

	wd := startWatchdog(childCtx, parser, idle, cancelChild, logger)

	// Single ~150-byte synchronous write; no goroutine needed. Failures
	// are logged-and-continued — the child's exit status is the
	// authoritative outcome. Identical pattern to drive.go's PTY writes.
	if _, err := stdin.Write(envelope); err != nil {
		logger.Warn("streamrunner: stdin write failed", "err", err)
	}
	if err := stdin.Close(); err != nil {
		if agentrun.ExitErrIsBenign(err) {
			logger.Debug("streamrunner: stdin close: child already exited", "err", err)
		} else {
			logger.Warn("streamrunner: stdin close failed", "err", err)
		}
	}

	waitErr := cmd.Wait()
	cancelChild() // stop the watchdog promptly on a normal exit
	wd.wait()     // join the goroutine; happens-before reading its state

	// Operator shutdown wins: a parent-ctx cancel is a clean no-result
	// teardown even if the watchdog also tripped in the same instant.
	if ctx.Err() != nil {
		return nil
	}
	// cmd.Wait() has returned, so the stdlib's stdout-forwarding goroutine is
	// done — Run is now the single writer to cfg.Stdout, so synthesising the
	// trailer here cannot race the passthrough.
	if wd.hasFired() {
		if !parser.hasSeenResult() {
			if err := writeIdleStallResult(cfg.Stdout, idle, runStart); err != nil {
				logger.Warn("streamrunner: write idle_stall result failed", "err", err)
			}
		}
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
