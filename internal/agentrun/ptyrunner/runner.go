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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"

	"github.com/pyrycode/pyrycode/internal/agentrun"
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

	// ErrNetworkFailure fires when tui-driver's network-failure detector
	// (tuidriver.HasNetworkFailure) reports the claude API unreachable at idle.
	// The screen-literal anchor it keys on is owned by tui-driver, not named
	// here — pyrycode carries no claude-TUI substrate string.
	ErrNetworkFailure = errors.New("ptyrunner: network failure detected; claude API unreachable")
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
	// tuidriver.Session.DeliverPrompt. Required (must be non-empty; an empty
	// paste has no semantics in the TUI).
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

	// PromptCommitTimeout bounds how long each delivery attempt waits for a
	// freshly-delivered prompt to commit (spinner up or session JSONL written)
	// before treating the paste as corrupted/uncommitted and re-delivering.
	// Forwarded verbatim to tuidriver.DeliverPrompt as DeliverOpts.CommitTimeout;
	// zero picks tui-driver's DefaultPromptCommitTimeout. Tests set a short value
	// to keep the retry budget well inside the deadline.
	PromptCommitTimeout time.Duration

	// Logger is used for the small number of Warn-level diagnostics this
	// package emits (spawn / close / modal-detected). Optional; nil
	// falls back to slog.Default().
	Logger *slog.Logger
}

// Run spawns claude under tui-driver with the argv shape buildArgs
// produces, waits for the TUI to reach idle and classifies the post-idle
// trust / mcp-failure / network-failure conditions via Session.WaitReady,
// submits cfg.PromptBytes via Session.DeliverPrompt, drains the per-session
// JSONL AND polls PTY-state transitions via tuidriver.Session.Events, re-emits
// each entry as stream-json on cfg.Stdout via streamjson.Emitter, and
// returns once end-of-turn fires or the context is cancelled.
//
// Return value contract:
//
//   - nil on a clean spawn → idle → deliver → events-to-end-of-turn cycle.
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
//   - fmt.Errorf("ptyrunner: deliver prompt: %w", err) on a DeliverPrompt
//     PTY-write failure.
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
//	cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close() → finalizeRecording()
//
// Each step is load-bearing: cancel() signals the watchdog goroutine
// (and the tuidriver.Session.Events merge goroutine) to exit; wg.Wait()
// drains the watchdog so no further SetExitReason(ExitReasonError) races
// with emitter.Close; counter.Stop() cancels the budget's SIGKILL grace
// timer; emitter.Close writes the `result` trailer to cfg.Stdout BEFORE
// sess.Close()'s SIGTERM races with claude's last PTY writes. The
// tuidriver.Session.Events channel is drained inline by Run's own
// goroutine, so wg tracks only the watchdog. finalizeRecording() (only
// registered when PYRY_RECORD_DIR is set) is the strict tail: it renames the
// .cast recording only after sess.Close() has joined tui-driver's PTY reader
// goroutine AND closed the RecordTo file (tui-driver owns that file end to
// end), so the rename cannot race a PTY write or an open handle. Re-ordering
// these defers silently breaks the invariant — keep them in this LIFO
// sequence.
//
// Run returns a named err so the recording-finalize defer can read it at fire
// time; the defer also reads the emitter's resolved ExitReason (the real wedge
// signal — Run returns nil on a watchdog-fire collapse) to tag -ok / -err by
// the run's true outcome. No existing defer assigns to err.
func Run(ctx context.Context, cfg Config) (err error) {
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

	// Operator-SIGTERM teardown. cmd.Cancel fires only when the ctx passed to
	// CommandContext is cancelled — operator SIGTERM/SIGINT — and never on
	// normal completion, budget-hit, or watchdog-fire (none of those cancel
	// the parent ctx), so those paths are byte-for-byte unchanged. At fire
	// time claude and its whole descendant tree are alive and not-yet-signalled,
	// so the reap walk is race-free. The reap SIGKILLs claude's detached Bash
	// subprocess group(s) — claude isolates every Bash command two levels below
	// pyry and does not reap that group even on a graceful SIGTERM (#565). The
	// trailing SIGTERM (vs os/exec's default immediate SIGKILL) then lets claude
	// flush its session JSONL cleanly before exit. Installed before Spawn so the
	// hook is in place when the os/exec watcher is armed at Start.
	cmd.Cancel = func() error {
		reapDescendantGroups(cmd.Process.Pid, logger)
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	// WaitDelay's exact value is non-binding — tui-driver Session.Close's
	// shutdown grace fires first and is the binding bounded-exit backstop;
	// killGrace only has to stay ≥ that grace. See reap.go.
	cmd.WaitDelay = killGrace

	// Opt-in TUI session flight recorder, gated on PYRY_RECORD_DIR. OFF by
	// default: an empty env var leaves recordTo "" and SpawnOpts{RecordTo: ""}
	// is byte-identical to the pre-#552 SpawnOpts{}.
	//
	// SECURITY: when enabled, tui-driver records EVERY PTY byte of the session
	// to a .cast file — the prompt, claude's output, AND all tool output, which
	// can include file contents and secrets. tui-driver opens it 0600 O_EXCL and
	// owns it; *.cast is gitignored, but the artifact is unencrypted by design
	// (it must stay `asciinema play`-able). Point PYRY_RECORD_DIR at a
	// non-synced, non-backed-up location such as ~/.local/share/pyry-recordings/
	// (sibling of ~/.local/share/pyry-artifacts/) — never inside the repo,
	// ~/.claude, or any Obsidian Sync / Time Machine path. The recording is a
	// separate opt-in artifact, NOT a log (see the package doc's
	// logging-discipline carve-out).
	//
	// ptyrunner owns only the path lifecycle: prune on startup, and the
	// post-Close -ok/-err rename. emitter is created later (after the idle/modal
	// gates), but the recording-finalize defer below must read its resolved
	// ExitReason at fire time to tag the .cast by the run's REAL outcome — so
	// declare it here, before that defer is registered. nil until assigned; the
	// defer nil-checks it.
	var emitter *streamjson.Emitter
	var recordTo string
	if dir := os.Getenv("PYRY_RECORD_DIR"); dir != "" {
		if merr := os.MkdirAll(dir, 0o700); merr != nil {
			return fmt.Errorf("ptyrunner: recording dir: %w", merr)
		}
		pruneOldRecordings(dir, logger)
		recordTo = recordingPath(dir, cfg.SessionID)
		// Register the finalize defer NOW — before `defer sess.Close()` below,
		// so LIFO runs it LAST: after sess.Close joins tui-driver's PTY reader
		// AND closes the recording file, and after emitter.Close has resolved
		// the exit reason. The closure reads the named err AND the emitter's
		// resolved ExitReason at fire time: Run returns nil on a watchdog-fired
		// wedge, so err alone would mis-tag it -ok — emitter.ExitReason()==
		// ExitReasonError is the real wedge signal, while a benign max-turns
		// stop (ExitReasonMaxTurns) stays -ok. emitter is nil on a pre-stream
		// early return: then a hard failure (trust / network modal) tags -err
		// via err, otherwise a header-only file -> -ok.
		defer func() {
			isErr := err != nil
			if emitter != nil && emitter.ExitReason() == streamjson.ExitReasonError {
				isErr = true
			}
			finalizeRecording(recordTo, isErr, logger)
		}()
	}

	sess, err := tuidriver.Spawn(cmd, tuidriver.SpawnOpts{RecordTo: recordTo})
	if err != nil {
		if isCtxErr(ctx, err) {
			return nil
		}
		logger.Warn("ptyrunner: spawn failed", "err", err)
		return fmt.Errorf("ptyrunner: spawn: %w", err)
	}
	defer func() {
		if cerr := sess.Close(); cerr != nil {
			if agentrun.ExitErrIsBenign(cerr) {
				logger.Debug("ptyrunner: close: child already exited", "err", cerr)
			} else {
				logger.Warn("ptyrunner: close failed", "err", cerr)
			}
		}
	}()

	// Wait for the TUI to reach idle, then apply policy to the readiness
	// classification. tui-driver detects (trust / mcp / network); ptyrunner
	// owns the policy: abort on trust, continue past an mcp banner, abort on
	// network. The mcp banner is non-fatal — an ambient MCP server failing must
	// not abort the run; streamrunner never aborted on it (parity), and treating
	// it as fatal regressed every spawn whose env had any offline MCP server
	// into error_during_execution/"no output".
	ready, werr := sess.WaitReady(ctx)
	if werr != nil {
		if isCtxErr(ctx, werr) {
			return nil
		}
		return fmt.Errorf("ptyrunner: wait idle: %w", werr)
	}
	if ready.TrustModal {
		logger.Warn("ptyrunner: trust modal detected")
		return ErrTrustModalDetected
	}
	if ready.McpFailure {
		logger.Warn("ptyrunner: mcp failure banner detected at idle — continuing (non-fatal)")
	}
	if ready.NetworkFailure {
		logger.Warn("ptyrunner: network failure detected")
		return ErrNetworkFailure
	}

	home := cfg.HomeDir
	if home == "" {
		h, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("ptyrunner: home dir: %w", herr)
		}
		home = h
	}
	jsonlPath := tuidriver.SessionJSONLPath(home, cfg.WorkDir, cfg.SessionID)

	// Deliver the prompt and confirm the turn committed, recovering from the
	// corrupted-paste wedge ("Mode B"). The deliver + commit-confirm + #227
	// no-re-deliver logic lives in tui-driver's DeliverPrompt; its decision
	// markers flow through cfg.Logger so the package's log discipline holds. A
	// residual wedge after retries surfaces downstream as num_turns=0 and the
	// dispatcher retries the ticket — DeliverPrompt's DeliverResult.Committed is
	// advisory, so Run falls through to the JSONL wait + watchdog regardless.
	if _, derr := sess.DeliverPrompt(ctx, tuidriver.DeliverOpts{
		Prompt:        string(cfg.PromptBytes),
		JSONLPath:     jsonlPath,
		CommitTimeout: cfg.PromptCommitTimeout,
		Logger:        logger,
	}); derr != nil {
		return fmt.Errorf("ptyrunner: deliver prompt: %w", derr)
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

	emitter, err = streamjson.New(streamjson.Config{
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

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runWatchdog(runCtx, sess, tracker, emitter, cancel, cfg.WatchdogTick, logger)
	}()
	defer wg.Wait()
	defer cancel()

	if werr := tuidriver.WaitForSessionJSONL(runCtx, jsonlPath); werr != nil {
		if isCtxErr(runCtx, werr) {
			return nil
		}
		return fmt.Errorf("ptyrunner: wait jsonl: %w", werr)
	}
	// tracker is the same watchdog Tracker constructed above (line 467);
	// v1.3.0's Events requires a non-nil *Tracker (nil panics on first deref).
	ch, err := sess.Events(runCtx, jsonlPath, 0, tracker)
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
			// Non-fatal (see the idle-time check above): keep consuming
			// events so the turn can complete despite an ambient MCP server
			// being offline.
			logger.Warn("ptyrunner: mcp failure banner detected — continuing (non-fatal)")
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
		"--permission-mode", "dontAsk",
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

// recordingMaxAge bounds how long a .cast flight recording is kept. On
// startup (only when PYRY_RECORD_DIR is set) recordings older than this are
// pruned so disk use stays bounded across runs.
const recordingMaxAge = 7 * 24 * time.Hour

// recordingPath returns the session-tagged temp .cast path in dir. tui-driver
// (via SpawnOpts.RecordTo) opens it 0600 O_EXCL, writes the asciinema header,
// and closes it on Session.Close; ptyrunner owns only the path lifecycle —
// prune, and the post-Close -ok/-err rename. The name is
// <sortable-UTC-stamp>-<sessionID>.cast so a crash / SIGKILL before the rename
// still leaves a session-identifiable file. The per-second UTC stamp plus the
// UUID session id make a collision otherwise impossible, and tui-driver's
// O_EXCL open defeats a create-time symlink/pre-create swap.
func recordingPath(dir, sessionID string) string {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	return filepath.Join(dir, stamp+"-"+sessionID+".cast")
}

// finalizeRecording renames the recording to annotate the run outcome inside
// the stem — <stem>-ok.cast when isErr is false, <stem>-err.cast when true.
// isErr reflects the run's REAL outcome (a hard Run error OR the emitter's
// resolved ExitReasonError wedge), NOT just Run's Go return — Run returns nil
// on a watchdog-fired wedge, so keying off the return alone mis-tagged every
// wedge -ok (fixed 2026-06-01). The suffix goes before the extension so the
// file stays a *.cast (required by both prune and `asciinema play`).
// Best-effort: a failed rename leaves the session-tagged temp in place — still
// a valid, replayable cast — Warn-logged with the path only, never any
// recording content.
//
// Runs as the strict tail of Run's defer-LIFO chain, after sess.Close() has
// joined tui-driver's PTY reader goroutine AND closed the recording file, so
// the rename cannot race a write or an open handle.
func finalizeRecording(tmpPath string, isErr bool, logger *slog.Logger) {
	outcome := "ok"
	if isErr {
		outcome = "err"
	}
	finalPath := strings.TrimSuffix(tmpPath, ".cast") + "-" + outcome + ".cast"
	if rerr := os.Rename(tmpPath, finalPath); rerr != nil {
		logger.Warn("ptyrunner: recording rename failed", "from", tmpPath, "to", finalPath, "err", rerr)
	}
}

// pruneOldRecordings deletes *.cast files in dir whose mtime is older than
// recordingMaxAge. Scoped strictly to dir's top level: filepath.Glob's "*"
// never matches a path separator, so it cannot recurse or escape dir, and only
// *.cast glob hits are candidates — never a subdirectory, never a non-.cast
// file. Best-effort housekeeping: glob/stat/remove errors are Warn-logged
// (path + err only) and never abort the run; a stale-cleanup hiccup must not
// block recording a fresh session. Called before the fresh file is created.
func pruneOldRecordings(dir string, logger *slog.Logger) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.cast"))
	if err != nil {
		logger.Warn("ptyrunner: recording prune glob failed", "dir", dir, "err", err)
		return
	}
	cutoff := time.Now().Add(-recordingMaxAge)
	for _, path := range matches {
		info, serr := os.Stat(path)
		if serr != nil {
			logger.Warn("ptyrunner: recording prune stat failed", "path", path, "err", serr)
			continue
		}
		if info.ModTime().Before(cutoff) {
			if rerr := os.Remove(path); rerr != nil {
				logger.Warn("ptyrunner: recording prune remove failed", "path", path, "err", rerr)
			}
		}
	}
}
