package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/agentrun/ptyrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/settings"
	"github.com/pyrycode/pyrycode/internal/agentrun/streamrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/trust"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// Test-only seams overridden by _test.go files to inject failures at each
// call site of the ptyrunner default path without spawning real claude.
// Production never assigns to these.
var (
	trustMark     = trust.MarkWorkdirTrusted
	settingsWrite = settings.WriteSettings
	ptyRun        = ptyrunner.Run
	newSessionID  = sessions.NewID
)

// agentRunArgs is the parsed shape of `pyry agent-run`'s flag set. Field
// names are stable: sibling tickets (trust-merge, settings-file, spawn) read
// this struct without renames.
type agentRunArgs struct {
	promptFile       string
	systemPromptFile string
	allowedTools     []string
	maxTurns         int
	effort           string
	model            string
	workdir          string
	outputFormat     string
}

// agentRunUsageDescription is the body printed by `pyry agent-run --help`
// between the `Usage:` header and `fs.PrintDefaults()`. Extracted as a
// constant so a sibling test in agent_run_test.go can lock the prose
// against stale-disclaimer regressions (see #359).
const agentRunUsageDescription = `Drive a single supervised claude turn headlessly.

By default, spawns claude as an interactive-TUI process under a PTY
(the surface Anthropic's 2026-06-15 billing policy names as
subscription-eligible), pre-marks the workdir as trusted in
~/.claude.json, writes a per-spawn deny-default permissions JSON,
delivers the user prompt via a bracketed-paste sequence, tails
claude's session JSONL, and re-emits each event as stream-json on
stdout for the dispatcher to consume. --max-turns is enforced by
pyry (interactive claude does not honour it). --allowed-tools is the
load-bearing tool gate, written into the per-spawn settings file as
a deny-default allow-list.

Set PYRY_USE_STREAMJSON=1 to fall back to the legacy stream-json
subprocess path (claude -p with --output-format stream-json) for
billing-classification experimentation. The fallback is operator-facing
only; the dispatcher receives the same stream-json wire shape under
both modes.`

// validEfforts enumerates the accepted values for --effort. The spike
// (#329) froze this set; if the upstream claude CLI uses different names,
// file a follow-up rather than silently renaming here.
var validEfforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
	"max":    true,
}

// splitAllowedTools tokenises the --allowed-tools value, accepting either
// comma- or whitespace-separated forms (or any mix), trimming each token,
// and dropping empties. Empty input yields an empty slice; callers decide
// whether emptiness is a parse error.
func splitAllowedTools(raw string) []string {
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseAgentRunArgs parses and validates the flag set for `pyry agent-run`.
// Errors are wrapped to name the offending flag so the top-level prefix
// renders as `pyry: agent-run: --<flag>: <reason>`.
func parseAgentRunArgs(args []string) (agentRunArgs, error) {
	fs := flag.NewFlagSet("pyry agent-run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	promptFile := fs.String("prompt-file", "", "path to the user-prompt file (required)")
	systemPromptFile := fs.String("system-prompt-file", "", "path to the system-prompt file (required)")
	allowedTools := fs.String("allowed-tools", "", "comma- or space-separated tool allowlist (required)")
	maxTurns := fs.Int("max-turns", 0, "maximum claude turns for this run (>0, required)")
	effort := fs.String("effort", "", "thinking effort: low|medium|high|xhigh|max (required)")
	model := fs.String("model", "", "claude model identifier (required)")
	workdir := fs.String("workdir", "", "working directory for claude (must exist, required)")
	outputFormat := fs.String("output-format", "", "must be \"stream-json\" (required)")

	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: pyry agent-run [flags]")
		fmt.Fprintln(fs.Output(), agentRunUsageDescription)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return agentRunArgs{}, fmt.Errorf("agent-run: %w", err)
	}
	if fs.NArg() > 0 {
		return agentRunArgs{}, fmt.Errorf("agent-run: unexpected positional %q", fs.Arg(0))
	}

	parsed := agentRunArgs{
		promptFile:       strings.TrimSpace(*promptFile),
		systemPromptFile: strings.TrimSpace(*systemPromptFile),
		maxTurns:         *maxTurns,
		effort:           strings.TrimSpace(*effort),
		model:            strings.TrimSpace(*model),
		workdir:          strings.TrimSpace(*workdir),
		outputFormat:     strings.TrimSpace(*outputFormat),
	}

	if parsed.promptFile == "" {
		return agentRunArgs{}, fmt.Errorf("agent-run: --prompt-file: required")
	}
	if err := requireRegularFile(parsed.promptFile); err != nil {
		return agentRunArgs{}, fmt.Errorf("agent-run: --prompt-file: %w", err)
	}

	if parsed.systemPromptFile == "" {
		return agentRunArgs{}, fmt.Errorf("agent-run: --system-prompt-file: required")
	}
	if err := requireRegularFile(parsed.systemPromptFile); err != nil {
		return agentRunArgs{}, fmt.Errorf("agent-run: --system-prompt-file: %w", err)
	}

	tools := splitAllowedTools(*allowedTools)
	if len(tools) == 0 {
		return agentRunArgs{}, fmt.Errorf("agent-run: --allowed-tools: required, non-empty after split")
	}
	parsed.allowedTools = tools

	if parsed.maxTurns <= 0 {
		return agentRunArgs{}, fmt.Errorf("agent-run: --max-turns: must be > 0 (got %d)", parsed.maxTurns)
	}

	if parsed.effort == "" {
		return agentRunArgs{}, fmt.Errorf("agent-run: --effort: required")
	}
	if !validEfforts[parsed.effort] {
		return agentRunArgs{}, fmt.Errorf("agent-run: --effort: %q not in {low, medium, high, xhigh, max}", parsed.effort)
	}

	if parsed.model == "" {
		return agentRunArgs{}, fmt.Errorf("agent-run: --model: required")
	}

	if parsed.workdir == "" {
		return agentRunArgs{}, fmt.Errorf("agent-run: --workdir: required")
	}
	if err := requireDir(parsed.workdir); err != nil {
		return agentRunArgs{}, fmt.Errorf("agent-run: --workdir: %w", err)
	}

	if parsed.outputFormat == "" {
		return agentRunArgs{}, fmt.Errorf("agent-run: --output-format: required")
	}
	if parsed.outputFormat != "stream-json" {
		return agentRunArgs{}, fmt.Errorf("agent-run: --output-format: %q not supported (want \"stream-json\")", parsed.outputFormat)
	}

	return parsed, nil
}

// requireRegularFile asserts that path exists and refers to a regular file.
// Stat errors (ENOENT, EACCES, …) flow through verbatim; callers wrap with
// the flag-name prefix.
func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	return nil
}

// requireDir asserts that path exists and refers to a directory.
func requireDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: not a directory", path)
	}
	return nil
}

// runAgentRun implements `pyry agent-run`: parse and validate the full flag
// surface, then drive claude either via the interactive-TUI PTY path
// (default) or the stream-json subprocess path (when PYRY_USE_STREAMJSON=1).
// Both modes emit stream-json on stdout for the dispatcher.
//
// Stdout contract: line-delimited stream-json events. Under the streamrunner
// path it's claude's own stdout forwarded byte-for-byte; under the ptyrunner
// path it's the streamjson.Emitter's re-emit of claude's per-session JSONL.
// The dispatcher's parser is satisfied by either.
func runAgentRun(stdout io.Writer, args []string) error {
	// --self-check is a sibling verb mode (#336): boot-time verification
	// that permissions.defaultMode "dontAsk" in the per-spawn settings file
	// still enforces the whitelist. Recognised positionally so it
	// short-circuits before parseAgentRunArgs runs — the eight required
	// production flags do not apply to the diagnostic verb.
	if slices.Contains(args, "--self-check") {
		return runAgentRunSelfCheck(stdout)
	}
	parsed, err := parseAgentRunArgs(args)
	if err != nil {
		return err
	}

	promptBytes, err := os.ReadFile(parsed.promptFile)
	if err != nil {
		return fmt.Errorf("agent-run: read prompt-file: %w", err)
	}

	// Test-only knob: tests inject a fakeclaude path via PYRY_CLAUDE_BIN
	// without modifying the flag surface. Production never sets this.
	claudeBin := os.Getenv("PYRY_CLAUDE_BIN")
	if claudeBin == "" {
		claudeBin = "claude"
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// PYRY_USE_STREAMJSON=1 selects the legacy stream-json subprocess
	// rollback. Only the exact string "1" is truthy; any other value (or
	// unset) falls through to the ptyrunner default — pinned by
	// TestRunAgentRun_EnvNon1ValueDispatchesToPtyRunner so a future
	// contributor cannot quietly widen the truthy set.
	if os.Getenv("PYRY_USE_STREAMJSON") == "1" {
		err = runAgentRunStreamRunner(ctx, stdout, parsed, claudeBin, promptBytes)
	} else {
		err = runAgentRunPty(ctx, stdout, parsed, claudeBin, promptBytes)
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("agent-run: %w", err)
}

// runAgentRunStreamRunner is the legacy stream-json subprocess path,
// selected by PYRY_USE_STREAMJSON=1. Byte-equivalent to the pre-cutover
// runAgentRun body; preserved indefinitely for billing-classification
// comparison (operator decision 2026-05-19).
func runAgentRunStreamRunner(ctx context.Context, stdout io.Writer, parsed agentRunArgs, claudeBin string, promptBytes []byte) error {
	return streamrunner.Run(ctx, streamrunner.Config{
		ClaudeBin:   claudeBin,
		WorkDir:     parsed.workdir,
		Args:        buildStreamRunnerClaudeArgs(parsed),
		PromptBytes: promptBytes,
		Stdout:      stdout,
		Stderr:      os.Stderr,
	})
}

// runAgentRunPty is the default path: pre-mark workdir trust, write the
// per-spawn deny-default settings JSON, mint a fresh session UUID, and
// delegate to ptyrunner.Run. The settings tempfile is removed on every
// exit path via defer.
func runAgentRunPty(ctx context.Context, stdout io.Writer, parsed agentRunArgs, claudeBin string, promptBytes []byte) error {
	realpath, err := trustMark(parsed.workdir)
	if err != nil {
		return fmt.Errorf("mark workdir trusted in ~/.claude.json: %w", err)
	}

	settingsPath, err := settingsWrite(parsed.allowedTools)
	if err != nil {
		return fmt.Errorf("write per-spawn settings: %w", err)
	}
	defer func() { _ = os.Remove(settingsPath) }()

	sid, err := newSessionID()
	if err != nil {
		return fmt.Errorf("mint session id: %w", err)
	}

	return ptyRun(ctx, ptyrunner.Config{
		ClaudeBin:    claudeBin,
		WorkDir:      realpath,
		SessionID:    string(sid),
		SettingsPath: settingsPath,
		SystemPrompt: parsed.systemPromptFile,
		Model:        parsed.model,
		Effort:       parsed.effort,
		MaxTurns:     parsed.maxTurns,
		PromptBytes:  promptBytes,
		Stdout:       stdout,
		Stderr:       os.Stderr,
	})
}

// buildStreamRunnerClaudeArgs constructs the argv passed to `claude`
// (without argv[0]) for the stream-json subprocess pipeline selected by
// PYRY_USE_STREAMJSON=1. The ptyrunner default path owns its own argv
// inside internal/agentrun/ptyrunner; do not unify these shapes.
//
// Notes on individual flags:
//
//   - `--input-format stream-json` causes claude to read one user-turn
//     envelope from stdin.
//   - `--output-format stream-json --verbose` is the required pair to get
//     assistant message events on stdout under stream-json mode (without
//     `--verbose`, only the final `result` is emitted).
//   - `--dangerously-skip-permissions` removes the workspace-trust dialog
//     and the per-spawn settings file (replaced by `--allowed-tools` as the
//     authoritative tool gate). Acceptable in this verb because the
//     dispatcher is the sole caller and operates inside an isolated worktree;
//     the spawn's blast radius is bounded by `--allowed-tools`, not by the
//     trust dialog.
//   - `--max-turns` is honoured in stream-json mode (interactive mode
//     ignored it) and bounds runaway-agent turn budget.
//   - `--allowed-tools` is comma-joined; `splitAllowedTools` already
//     normalised operator input into a clean slice at parse time.
func buildStreamRunnerClaudeArgs(parsed agentRunArgs) []string {
	return []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--append-system-prompt-file", parsed.systemPromptFile,
		"--model", parsed.model,
		"--effort", parsed.effort,
		"--max-turns", strconv.Itoa(parsed.maxTurns),
		"--allowed-tools", strings.Join(parsed.allowedTools, ","),
	}
}

