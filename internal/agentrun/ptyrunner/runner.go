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
// This slice composes the pyry-side max-turns budget Counter (from
// internal/agentrun/budget) and the tui-driver watchdog Tracker
// (PTY-heartbeat + spinner-freeze) on top of the JSONL tail + stream-json
// emit from #478; the cmd/pyry/agent_run.go cutover is #470.
//
// The package logs only error messages and never logs PromptBytes content
// or any substring of the rolling TUI buffer — writers are opaque and the
// buffer is inspected only via tui-driver's pure-function detectors. The
// tuidriver.TailJSONL channel drain and the streamjson emitter inherit the
// same discipline — neither logs entry content; ptyrunner does not add any
// log call that would either. The wired budget Counter and the watchdog
// goroutine inherit the same discipline — the Counter logs only count +
// max_turns numerics and the watchdog goroutine logs only the
// tuidriver-generated watchdog error string; neither logs entry content.
//
// Dependency direction: the package must not import
// github.com/pyrycode/pyrycode/internal/supervisor (the PTY helper for the
// long-lived claude wrapper). The sibling agentrun subpackages (budget,
// streamjson) are wired here. Verify with:
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
	"sync"
	"syscall"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"

	"github.com/pyrycode/pyrycode/internal/agentrun/budget"
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

	// AllowedTools is the human-readable tool allowlist stamped into the
	// leading `system/init` envelope's `tools` field (via streamjson.New).
	// Required (non-nil; empty slice OK). The runtime enforcement is the
	// deny-default settings file at SettingsPath; this list is the
	// wire-shape mirror of those names, kept caller-synchronised at the
	// runAgentRunPty wiring site.
	AllowedTools []string

	// MaxTurns is the assistant-entry cap enforced by the pyry-side budget
	// Counter (internal/agentrun/budget). Required; must be > 0. The
	// interactive-TUI claude path intentionally omits --max-turns from argv
	// because interactive claude does not honor it; this field is the
	// load-bearing enforcement point. On budget hit the run is terminated
	// with ExitReasonMaxTurns set on the streamjson emitter.
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
	// home directory used to resolve the per-session JSONL path
	// (~/.claude/projects/<encoded-workdir>/<session-id>.jsonl).
	// Production callers leave it empty; os.UserHomeDir() is consulted in
	// that case. Tests use a t.TempDir() value so each test gets an
	// isolated ~/.claude/projects tree.
	HomeDir string

	// Env is appended to os.Environ() in the child process. Optional;
	// production callers leave it nil. Tests use it to thread
	// TestHelperProcess wiring. EnsureClaudeEnv (called after this
	// package wires cmd.Env) sets TERM=xterm-256color.
	Env []string

	// WatchdogTick is the cadence at which the watchdog goroutine polls
	// the rolling buffer + spinner state. Optional; zero defaults to 1
	// second (matches the tui-driver spike binaries). Tests typically set
	// 50ms to keep wall-clock low.
	WatchdogTick time.Duration

	// WatchdogTrackerOpts is forwarded verbatim to tuidriver.NewTracker.
	// Optional; zero values pick the tuidriver-package defaults
	// (PTYQuietLimit = 30s, SpinnerFreezeLimit = 30s). Tests use short
	// values (~200ms) to fire the watchdog within the test deadline.
	WatchdogTrackerOpts tuidriver.TrackerOpts

	// Logger is used for the small number of Warn-level diagnostics this
	// package emits (spawn / close / modal-detected). Optional; nil
	// falls back to slog.Default().
	Logger *slog.Logger
}

// Run spawns claude under tui-driver with the argv shape buildArgs
// produces, waits for the TUI to reach idle, runs the trust / mcp-failure
// / network-failure detectors against the post-idle snapshot, submits
// cfg.PromptBytes via Session.WritePrompt, drains the per-session JSONL
// AND polls PTY-state transitions via tuidriver.Session.Events, re-emits
// each entry as stream-json on cfg.Stdout via streamjson.Emitter, and
// returns once end-of-turn fires or the context is cancelled.
//
// Return value contract:
//
//   - nil on a clean spawn → idle → WritePrompt → events-to-end-of-turn cycle.
//   - nil on operator-shutdown collapse: any ctx-cancel / deadline-exceeded
//     error from Spawn / WaitUntil / WaitForSessionJSONL / Session.Events,
//     and any in-flight emit failure observed during teardown, collapses to
//     nil when ctx.Err() is set (same contract streamrunner uses).
//   - errors.New("ptyrunner: <field> required") on missing required fields.
//   - fmt.Errorf("ptyrunner: spawn: %w", err) on Spawn failure.
//   - fmt.Errorf("ptyrunner: wait idle: %w", err) on a non-ctx WaitUntil
//     error (defensive — WaitUntil only returns ctx.Cause today).
//   - ErrTrustModalDetected / ErrMcpFailureBanner / ErrNetworkFailure on
//     the corresponding post-idle one-shot detection OR on a mid-run
//     EventKindPty{ModalShown(trust-folder),McpFailureShown,NetworkFailureShown}
//     transition surfaced on the Session.Events stream. The detection
//     cadence is tui-driver's DefaultPollInterval (50 ms).
//   - fmt.Errorf("ptyrunner: write prompt: %w", err) on WritePrompt
//     failure.
//   - fmt.Errorf("ptyrunner: emitter: %w", err) on streamjson.New failure
//     (defensive — Writer is validated upstream and SessionID is required).
//   - fmt.Errorf("ptyrunner: home dir: %w", err) when cfg.HomeDir is empty
//     and os.UserHomeDir() fails.
//   - fmt.Errorf("ptyrunner: wait jsonl: %w", err) on a non-ctx
//     WaitForSessionJSONL failure (e.g. permission denied on stat).
//   - fmt.Errorf("ptyrunner: events: %w", err) on Session.Events
//     synchronous open / seek failure (non-ctx).
//   - fmt.Errorf("ptyrunner: emit: %w", err) on the first sticky Emit
//     failure captured during the inline drain (e.g. broken pipe on
//     cfg.Stdout). Prioritised over a clean drain because the emit
//     failure is operator-actionable.
//
// Cleanup ordering — defer LIFO produces:
//
//	cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close()
//
// Each step is load-bearing: cancel() signals the watchdog goroutine
// (and the tuidriver.Session.Events merge goroutine) to exit; wg.Wait()
// drains the watchdog so no further SetExitReason(ExitReasonError) races
// with emitter.Close; counter.Stop() cancels the budget's SIGKILL grace
// timer; emitter.Close writes the `result` trailer to cfg.Stdout BEFORE
// sess.Close()'s SIGTERM races with claude's last PTY writes. The
// tuidriver.Session.Events channel is drained inline by Run's own
// goroutine, so wg tracks only the watchdog. Re-ordering these defers
// silently breaks the invariant — keep them in this LIFO sequence.
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
	if cfg.AllowedTools == nil {
		return errors.New("ptyrunner: AllowedTools required")
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
	if cfg.MaxTurns <= 0 {
		return errors.New("ptyrunner: MaxTurns required")
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

	// Single shared cancellation point: the budget Terminate hook AND the
	// watchdog goroutine both call cancel() on their respective trigger;
	// the tuidriver.TailJSONL goroutine closes its channel once runCtx
	// cancels, ending the inline drain below. Cancelling the parent ctx
	// (operator shutdown) also signals runCtx because runCtx is a child
	// of ctx.
	//
	// This first defer cancel() catches the early-return paths below
	// (emitter.New / budget.New / home-dir / wait-jsonl / tail-open
	// failure). A second defer cancel() is registered after the watchdog
	// goroutine spawn so the cleanup-order LIFO fires cancel() FIRST
	// (cancel is idempotent and safe for concurrent + repeated calls per
	// context docs).
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	emitter, err := streamjson.New(streamjson.Config{
		Writer:    cfg.Stdout,
		SessionID: cfg.SessionID,
		Cwd:       cfg.WorkDir,
		Tools:     cfg.AllowedTools,
		Model:     cfg.Model,
		Logger:    logger,
	})
	if err != nil {
		return fmt.Errorf("ptyrunner: emitter: %w", err)
	}
	// Defer-LIFO discipline: do NOT reorder the chain below. Fire order
	// (top runs first): cancel() → wg.Wait() → counter.Stop() →
	// emitter.Close() → sess.Close(). See package doc on Run for the
	// invariants each step protects.
	defer func() { _ = emitter.Close() }()

	tracker := tuidriver.NewTracker(cfg.WatchdogTrackerOpts)
	tracker.RecordTransition("prompt-written")

	counter, err := budget.New(budget.Config{
		MaxTurns: cfg.MaxTurns,
		Terminate: func() error {
			emitter.SetExitReason(streamjson.ExitReasonMaxTurns)
			tracker.RecordTransition("budget-hit")
			cancel()
			return cmd.Process.Signal(syscall.SIGTERM)
		},
		Kill: func() error {
			return cmd.Process.Signal(syscall.SIGKILL)
		},
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("ptyrunner: budget: %w", err)
	}
	defer counter.Stop()

	home := cfg.HomeDir
	if home == "" {
		h, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("ptyrunner: home dir: %w", herr)
		}
		home = h
	}
	jsonlPath := tuidriver.SessionJSONLPath(home, cfg.WorkDir, cfg.SessionID)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runWatchdog(runCtx, sess.Buffer, tracker, emitter, cancel, cfg.WatchdogTick, logger)
	}()
	defer wg.Wait()
	defer cancel()

	if werr := tuidriver.WaitForSessionJSONL(runCtx, jsonlPath); werr != nil {
		if isCtxErr(runCtx, werr) {
			return nil
		}
		return fmt.Errorf("ptyrunner: wait jsonl: %w", werr)
	}
	ch, err := sess.Events(runCtx, jsonlPath, 0)
	if err != nil {
		if isCtxErr(runCtx, err) {
			return nil
		}
		return fmt.Errorf("ptyrunner: events: %w", err)
	}

	var emitErr error
loop:
	for ev := range ch {
		switch ev.Kind {
		case tuidriver.EventKindPtyModalShown:
			if ev.Modal == tuidriver.ModalClassTrustFolder {
				logger.Warn("ptyrunner: trust modal detected")
				return ErrTrustModalDetected
			}
		case tuidriver.EventKindPtyMcpFailureShown:
			logger.Warn("ptyrunner: mcp failure banner detected")
			return ErrMcpFailureBanner
		case tuidriver.EventKindPtyNetworkFailureShown:
			logger.Warn("ptyrunner: network failure detected")
			return ErrNetworkFailure
		case tuidriver.EventKindJsonlEntry:
			if eerr := emitter.Emit(ev.Entry); eerr != nil && emitErr == nil {
				emitErr = eerr
			}
			counter.OnEvent(ev.Entry)
		case tuidriver.EventKindJsonlEndOfTurn:
			counter.OnEndOfTurn()
			break loop
		}
	}
	if runCtx.Err() != nil {
		return nil
	}
	if emitErr != nil {
		return fmt.Errorf("ptyrunner: emit: %w", emitErr)
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
