// Package ptyrunner spawns claude as an interactive TUI under a PTY (via
// github.com/pyrycode/tui-driver), waits for the TUI to reach idle, submits
// one user prompt through a bracketed-paste sequence, tails the per-session
// JSONL output, re-emits each event as stream-json on cfg.Stdout, and tears
// the session down cleanly after the deterministic end-of-turn fires.
//
// `pyry agent-run` will use ptyrunner (post-#470 cutover) to drive claude on
// the interactive surface Anthropic's 2026-06-15 billing policy explicitly
// names as subscription-eligible, replacing the stream-json subprocess path
// in internal/agentrun/streamrunner. The package owns the spawn, the
// idle-wait, the modal/banner safety check, the prompt write, the JSONL
// tail + stream-json emit, and the shutdown — nothing else.
//
// This slice wires the JSONL tail and stream-json emit on top of the
// scaffolding from #471. The pyry-side max-turns budget and the watchdog
// goroutine land in a follow-up slice on top of this one; the
// cmd/pyry/agent_run.go cutover is #470.
//
// The package logs only error messages and never logs PromptBytes content
// or any substring of the rolling TUI buffer — writers are opaque and the
// buffer is inspected only via tui-driver's pure-function detectors. The
// wired jsonl/tail watcher and streamjson emitter inherit the same
// discipline — neither logs Event content; ptyrunner does not add any log
// call that would either.
//
// Dependency direction: the package must not import
// github.com/pyrycode/pyrycode/internal/supervisor (the PTY helper for the
// long-lived claude wrapper). The sibling agentrun subpackages (jsonl,
// jsonl/tail, streamjson) are wired here. Verify with:
//
//	go list -deps ./internal/agentrun/ptyrunner/... | grep pyrycode/internal/supervisor
//
// Expected output: empty.
package ptyrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"

	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl/tail"
	"github.com/pyrycode/pyrycode/internal/agentrun/streamjson"
)

// Sentinel-shape errors surfaced when a modal or failure banner is detected
// at idle. Each one names the failing detector and points to the
// remediation; consumers (#470's cmd cutover) match with errors.Is to
// distinguish the trust-modal case from the others for surfacing a hint.
var (
	// ErrTrustModalDetected fires when the trust-folder modal renders at
	// idle. Remediation lives in #469 (MarkWorkdirTrusted pre-write).
	ErrTrustModalDetected = errors.New("ptyrunner: trust-folder modal detected; pre-write trust via #469's MarkWorkdirTrusted before invoking Run")

	// ErrMcpFailureBanner fires when claude's "N MCP server failed"
	// status banner is present at idle.
	ErrMcpFailureBanner = errors.New("ptyrunner: MCP failure banner detected; check claude's MCP server config")

	// ErrNetworkFailure fires when the FailedToOpenSocket anchor is
	// present at idle (claude API unreachable).
	ErrNetworkFailure = errors.New("ptyrunner: network failure detected (FailedToOpenSocket); claude API unreachable")
)

// Config configures Run. Required fields are validated at entry; optional
// fields are documented below.
type Config struct {
	// ClaudeBin is the resolved path to the claude executable. Required.
	ClaudeBin string

	// WorkDir is the child process's working directory. Required.
	WorkDir string

	// SessionID is the pyry-minted UUID passed to claude --session-id.
	// Required.
	SessionID string

	// SettingsPath is the path to the deny-default settings JSON produced
	// by #469. Passed via claude --settings. Required.
	SettingsPath string

	// SystemPrompt is the path to the system-prompt file passed via
	// claude --append-system-prompt-file. Required.
	SystemPrompt string

	// Model is the model identifier passed via claude --model. Required.
	Model string

	// Effort is the reasoning-effort selector passed via claude --effort.
	// Required.
	Effort string

	// MaxTurns is declared for forward-compatibility with the follow-up
	// slice on top of #478 (pyry-side budget counter); NOT consumed in this
	// slice. The interactive-TUI claude path intentionally omits
	// --max-turns from argv (the follow-up slice enforces the cap in pyry
	// itself).
	MaxTurns int

	// PromptBytes is the user-turn prompt text submitted via
	// tuidriver.Session.WritePrompt — NOT raw Write. Required (must be
	// non-empty; an empty paste has no semantics in the TUI).
	//
	// PromptBytes content MUST NOT appear in any log line or in any
	// wrapped error message — the package's logging discipline forbids
	// it.
	PromptBytes []byte

	// Stdout is where the streamjson.Emitter writes per-event stream-json
	// lines and the trailing `type:"result"` trailer. Required.
	// Production callers pass os.Stdout; tests pass a bytes.Buffer or a
	// failing writer.
	Stdout io.Writer

	// Stderr receives the child's stderr. Required. (The interactive-TUI
	// claude writes stderr separately from the PTY-mirrored stdout —
	// tui-driver does not multiplex them.)
	Stderr io.Writer

	// HomeDir is an optional test seam. When non-empty, it overrides the
	// home directory used by the JSONL watcher
	// (~/.claude/projects/<encoded-workdir>/). Production callers leave it
	// empty; the watcher consults os.UserHomeDir() in that case. Tests
	// use a t.TempDir() value so each test gets an isolated
	// ~/.claude/projects tree.
	HomeDir string

	// Env is appended to os.Environ() in the child process. Optional;
	// production callers leave it nil. Tests use it to thread
	// TestHelperProcess wiring. EnsureClaudeEnv (called after this
	// package wires cmd.Env) sets TERM=xterm-256color.
	Env []string

	// Logger is used for the small number of Warn-level diagnostics this
	// package emits (spawn / close / modal-detected). Optional; nil
	// falls back to slog.Default().
	Logger *slog.Logger
}

// Run spawns claude under tui-driver with the argv shape buildArgs
// produces, waits for the TUI to reach idle, runs the trust / mcp-failure
// / network-failure detectors against the post-idle snapshot, submits
// cfg.PromptBytes via Session.WritePrompt, tails the per-session JSONL via
// jsonl/tail, re-emits each event as stream-json on cfg.Stdout via
// streamjson.Emitter, and returns once end-of-turn fires or the context is
// cancelled.
//
// Return value contract:
//
//   - nil on a clean spawn → idle → WritePrompt → tail-to-end-of-turn cycle.
//   - nil on operator-shutdown collapse: any ctx-cancel / deadline-exceeded
//     error from Spawn / WaitUntil / Watcher.Run, and any in-flight emit
//     failure observed during teardown, collapses to nil when ctx.Err() is
//     set (same contract streamrunner uses).
//   - errors.New("ptyrunner: <field> required") on missing required fields.
//   - fmt.Errorf("ptyrunner: spawn: %w", err) on Spawn failure.
//   - fmt.Errorf("ptyrunner: wait idle: %w", err) on a non-ctx WaitUntil
//     error (defensive — WaitUntil only returns ctx.Cause today).
//   - ErrTrustModalDetected / ErrMcpFailureBanner / ErrNetworkFailure on
//     the corresponding post-idle detection.
//   - fmt.Errorf("ptyrunner: write prompt: %w", err) on WritePrompt
//     failure.
//   - fmt.Errorf("ptyrunner: emitter: %w", err) on streamjson.New failure
//     (defensive — Writer is validated upstream and SessionID is required).
//   - fmt.Errorf("ptyrunner: tail: %w", err) on tail.New / Watcher.Run
//     failure (non-ctx I/O failure draining the JSONL file).
//   - fmt.Errorf("ptyrunner: emit: %w", err) on the first sticky Emit
//     failure captured during the watcher's drain (e.g. broken pipe on
//     cfg.Stdout). Prioritised over a non-ctx Watcher.Run error because
//     the emit failure is operator-actionable while the watcher likely
//     returned cleanly at EOT regardless.
//
// Cleanup ordering: emitter.Close (which writes the `result` trailer) runs
// BEFORE sess.Close (which SIGTERM→grace→SIGKILLs the claude child) via
// defer LIFO. That way the dispatcher receives a complete stream even if
// the child takes the full grace window to exit. emitter.Close's return
// value is discarded; sess.Close's non-nil error is logged at Warn and not
// surfaced.
func Run(ctx context.Context, cfg Config) error {
	if cfg.ClaudeBin == "" {
		return errors.New("ptyrunner: ClaudeBin required")
	}
	if cfg.WorkDir == "" {
		return errors.New("ptyrunner: WorkDir required")
	}
	if cfg.SessionID == "" {
		return errors.New("ptyrunner: SessionID required")
	}
	if cfg.SettingsPath == "" {
		return errors.New("ptyrunner: SettingsPath required")
	}
	if cfg.SystemPrompt == "" {
		return errors.New("ptyrunner: SystemPrompt required")
	}
	if cfg.Model == "" {
		return errors.New("ptyrunner: Model required")
	}
	if cfg.Effort == "" {
		return errors.New("ptyrunner: Effort required")
	}
	if len(cfg.PromptBytes) == 0 {
		return errors.New("ptyrunner: PromptBytes required")
	}
	if cfg.Stdout == nil {
		return errors.New("ptyrunner: Stdout required")
	}
	if cfg.Stderr == nil {
		return errors.New("ptyrunner: Stderr required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	cmd := exec.CommandContext(ctx, cfg.ClaudeBin, buildArgs(cfg)...)
	cmd.Dir = cfg.WorkDir
	cmd.Stderr = cfg.Stderr
	if cfg.Env != nil {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	tuidriver.EnsureClaudeEnv(cmd)

	sess, err := tuidriver.Spawn(cmd, tuidriver.SpawnOpts{})
	if err != nil {
		if isCtxErr(ctx, err) {
			return nil
		}
		logger.Warn("ptyrunner: spawn failed", "err", err)
		return fmt.Errorf("ptyrunner: spawn: %w", err)
	}
	defer func() {
		if cerr := sess.Close(); cerr != nil {
			logger.Warn("ptyrunner: close failed", "err", cerr)
		}
	}()

	if werr := tuidriver.WaitUntil(ctx, func() bool {
		return tuidriver.IsIdle(sess.Buffer.Snapshot())
	}); werr != nil {
		if isCtxErr(ctx, werr) {
			return nil
		}
		return fmt.Errorf("ptyrunner: wait idle: %w", werr)
	}

	snap := sess.Buffer.Snapshot()
	if tuidriver.HasTrustModal(snap) {
		logger.Warn("ptyrunner: trust modal detected")
		return ErrTrustModalDetected
	}
	if tuidriver.HasMcpFailureBanner(snap) {
		logger.Warn("ptyrunner: mcp failure banner detected")
		return ErrMcpFailureBanner
	}
	if tuidriver.HasNetworkFailure(snap) {
		logger.Warn("ptyrunner: network failure detected")
		return ErrNetworkFailure
	}

	if err := sess.WritePrompt(string(cfg.PromptBytes)); err != nil {
		return fmt.Errorf("ptyrunner: write prompt: %w", err)
	}

	emitter, err := streamjson.New(streamjson.Config{
		Writer:    cfg.Stdout,
		SessionID: cfg.SessionID,
		Logger:    logger,
	})
	if err != nil {
		return fmt.Errorf("ptyrunner: emitter: %w", err)
	}
	// Registered AFTER sess.Close's defer so LIFO ordering runs
	// emitter.Close FIRST (flushes the `result` trailer), then sess.Close
	// (SIGTERMs the claude child). emitter.Close's return is advisory:
	// if Emit was failing the dispatcher already sees a broken stream,
	// and if Emit was succeeding the trailer's ~300-byte write almost
	// certainly succeeds.
	defer func() { _ = emitter.Close() }()

	var emitErr error
	watcher, err := tail.New(tail.Config{
		Workdir:   cfg.WorkDir,
		SessionID: cfg.SessionID,
		HomeDir:   cfg.HomeDir,
		OnEvent: func(ev jsonl.Event) {
			if err := emitter.Emit(ev); err != nil && emitErr == nil {
				emitErr = err
			}
		},
		// OnEndOfTurn intentionally no-op: the emitter's internal
		// EndOfTurnSeen state is set inside Emit when ev.EndOfTurn
		// is true, so Close's default classification produces
		// ExitReasonCompletion automatically. The callback exists
		// only because tail.Config.OnEndOfTurn is required.
		OnEndOfTurn: func() {},
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("ptyrunner: tail: %w", err)
	}

	runErr := watcher.Run(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if emitErr != nil {
		return fmt.Errorf("ptyrunner: emit: %w", emitErr)
	}
	if runErr != nil {
		return fmt.Errorf("ptyrunner: tail: %w", runErr)
	}
	return nil
}

// buildArgs assembles the argv (excluding argv[0]) passed to claude. The
// interactive-TUI path keeps --session-id / --settings / --permission-mode
// / --append-system-prompt-file / --model / --effort and intentionally
// omits --input-format, --output-format, --verbose,
// --dangerously-skip-permissions, --max-turns, and --allowed-tools (the
// last two land in #472 and via the settings file produced by #469,
// respectively).
func buildArgs(cfg Config) []string {
	return []string{
		"--session-id", cfg.SessionID,
		"--settings", cfg.SettingsPath,
		"--permission-mode", "default",
		"--append-system-prompt-file", cfg.SystemPrompt,
		"--model", cfg.Model,
		"--effort", cfg.Effort,
	}
}

// isCtxErr reports whether err originates from ctx-cancel or
// deadline-exceeded — the operator-shutdown collapse. tuidriver.WaitUntil
// returns context.Cause(ctx) on cancellation, which may be the bare
// context.Canceled / context.DeadlineExceeded or a wrapped cause; either
// shape collapses to nil per the streamrunner contract.
func isCtxErr(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if ctx.Err() != nil {
		return true
	}
	return false
}
