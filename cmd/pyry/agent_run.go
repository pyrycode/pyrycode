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
	"strings"
	"syscall"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/agentrun/settings"
	"github.com/pyrycode/pyrycode/internal/agentrun/streamrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/trust"
)

// Test-only seams overridden by _test.go files to inject failures at each
// call site without spawning real claude. Production never assigns to these.
//
// trustMark outlived the agent-run terminal path that introduced it: it is now
// reached from the daemon's workdir confinement and from the ACP lane, so it
// stays here as the shared seam rather than moving, keeping the override set in
// one place. The session-id seam went with the terminal path, which was its only
// caller — the stream runner lets claude mint its own.
var (
	trustMark     = trust.MarkWorkdirTrusted
	settingsWrite = settings.WriteSettingsWithDeny
)

// agentRunArgs is the parsed shape of `pyry agent-run`'s flag set. Field
// names are stable: sibling tickets (trust-merge, settings-file, spawn) read
// this struct without renames.
type agentRunArgs struct {
	promptFile       string
	systemPromptFile string
	allowedTools     []string
	disallowedTools  []string
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

Spawns claude as a stream-json subprocess (claude with
--input-format/--output-format stream-json, NOT claude -p/--print),
writes a per-spawn deny-default permissions JSON, delivers the user
prompt on stdin, and forwards claude's own stream-json on stdout for
the dispatcher to consume. --allowed-tools is written into the
per-spawn settings file as a deny-default allow-list, which is what
enforces it; the flag alone does not, and is defeated outright by
--dangerously-skip-permissions (see #1387). --max-turns is honoured by
claude itself on this surface, so pyry passes it straight through
rather than counting turns of its own.

Billing: this surface bills to the Keychain subscription. It passes
no print flag, and a live run on it reports a subscription five-hour
rate-limit window. Verified 2026-07-24, which retired the earlier
belief that only the PTY surface was subscription-eligible.

There is no second runner. The terminal-driving PTY path and its
PYRY_USE_STREAMJSON selector were removed in #1348; the variable is
ignored if still set.`

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
	disallowedTools := fs.String("disallowed-tools", "", "comma- or space-separated tool denylist (optional)")
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

	// --disallowed-tools is optional: an absent or empty value yields a nil
	// slice, which WriteSettingsWithDeny omits from the settings file (no
	// deny key). Tokenised identically to --allowed-tools.
	parsed.disallowedTools = splitAllowedTools(*disallowedTools)

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
// surface, then drive claude via the stream-json subprocess path, which is the
// only path since #1470 moved the default off the terminal-driving runner.
//
// Stdout contract: line-delimited stream-json events, claude's own stdout
// forwarded byte-for-byte.
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

	// The stream-json runner is the only path (#1470, ahead of the #1348
	// deletion). It used to be selected by PYRY_USE_STREAMJSON=1 against a
	// terminal-driving default, which meant an unconfigured run took the
	// terminal path — the one nothing has exercised since the 2026-07-25 fleet
	// switch and which produced zero turns when it was last tried live.
	//
	// PYRY_USE_STREAMJSON is deliberately NOT read any more, and setting it is
	// harmless. All five dispatcher forks carry it in their .env; making it a
	// no-op rather than an error means none of them needs editing, and the line
	// can be swept out of the forks whenever convenient rather than urgently.
	err = runAgentRunStreamRunner(ctx, stdout, parsed, claudeBin, promptBytes)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("agent-run: %w", err)
}

// runAgentRunStreamRunner drives one claude turn as a stream-json subprocess:
// it writes the per-spawn settings file that carries the tool boundary, then
// hands the argv to streamrunner.Run. It is the only agent-run path — the
// terminal-driving runner it once shared the verb with was deleted in #1348.
func runAgentRunStreamRunner(ctx context.Context, stdout io.Writer, parsed agentRunArgs, claudeBin string, promptBytes []byte) error {
	// Write the per-spawn deny-default settings file that carries this
	// command's whole tool boundary. Without it there is NO tool boundary at
	// all: `--dangerously-skip-permissions` defeats `--allowed-tools`,
	// measured 2026-08-08 (pyrycode#1387). Every agent dispatched between the
	// 2026-07-25 fleet switch and that fix ran unrestricted, which silently
	// returned architect-only web search and subagent spawning, the
	// deliberately-excluded Figma write tools, and the three human-only
	// tools, to every role.
	//
	// --disallowed-tools lands in permissions.deny in the same write: a tool
	// listed there leaves the model's surface, not denied at call time (#411).
	settingsPath, err := settingsWrite(parsed.allowedTools, parsed.disallowedTools)
	if err != nil {
		return fmt.Errorf("write per-spawn settings: %w", err)
	}
	defer func() { _ = os.Remove(settingsPath) }()

	return streamrunner.Run(ctx, streamrunner.Config{
		ClaudeBin:   claudeBin,
		WorkDir:     parsed.workdir,
		Args:        buildStreamRunnerClaudeArgs(parsed, true, "", settingsPath),
		PromptBytes: promptBytes,
		Stdout:      stdout,
		Stderr:      os.Stderr,
	})
}

// buildStreamRunnerClaudeArgs constructs the argv passed to `claude`
// (without argv[0]) for the stream-json subprocess pipeline, which is the
// only pipeline agent-run has. The resulting shape is pinned by
// TestBuildStreamRunnerClaudeArgs_Shape; change the argv and update that test.
//
// The permission slot (after `--verbose`, before `--append-system-prompt-file`)
// is the YOLO toggle, filled by permissionArgs(yolo, mcpConfigPath): yolo=true
// emits `--dangerously-skip-permissions`; yolo=false emits the
// permission-prompt-tool + mcp-config pair that routes every tool use through
// the daemon approval registry (see mcp_config.go). The sole production caller
// (runAgentRunStreamRunner) passes yolo=true — agent-run always runs YOLO — so
// the emitted argv is byte-for-byte unchanged from the pre-toggle form. The
// non-YOLO branch is exercised by unit tests here and wired to a live spawn
// downstream.
//
// Notes on individual flags:
//
//   - `--input-format stream-json` causes claude to read one user-turn
//     envelope from stdin.
//   - `--output-format stream-json --verbose` is the required pair to get
//     assistant message events on stdout under stream-json mode (without
//     `--verbose`, only the final `result` is emitted).
//   - `--dangerously-skip-permissions` (YOLO branch) removes the
//     workspace-trust dialog. It ALSO defeats `--allowed-tools`, which this
//     comment previously claimed was "the authoritative tool gate" on this
//     path. That was assumed and never measured, and it is false. Measured
//     2026-08-08 against claude directly, two cells, same model and prompt:
//     with `--dangerously-skip-permissions --allowed-tools Read` the agent
//     wrote the file; with `--allowed-tools Read` alone it refused. So for
//     two weeks after the 2026-07-25 fleet switch to this path, the
//     allowlist did nothing (pyrycode#1387).
//   - `--settings` carries the per-spawn deny-default permissions file and
//     IS the enforcement — the only enforcement agent-run has.
//     It survives `--dangerously-skip-permissions` where the command-line
//     allowlist does not. `--allowed-tools` is still passed, because it is
//     harmless and remains meaningful if the skip flag is ever dropped, but
//     it must not be relied on as the boundary.
//   - `--max-turns` is honoured in stream-json mode (interactive mode
//     ignored it) and bounds runaway-agent turn budget.
//   - `--allowed-tools` is comma-joined; `splitAllowedTools` already
//     normalised operator input into a clean slice at parse time.
func buildStreamRunnerClaudeArgs(parsed agentRunArgs, yolo bool, mcpConfigPath, settingsPath string) []string {
	// The shape itself lives in the streamrunner package so the permission
	// self-check builds the same argv this does. A self-check assembling its own
	// is a check on a spawn nobody performs, which is exactly how it ended up
	// attached to a runner production had already left (#1348). This function
	// stays because the non-yolo branch needs permissionArgs, which lives here.
	var perm []string
	if !yolo {
		perm = permissionArgs(false, mcpConfigPath)
	}
	return streamrunner.BuildClaudeArgs(streamrunner.ArgsParams{
		SystemPromptFile: parsed.systemPromptFile,
		Model:            parsed.model,
		Effort:           parsed.effort,
		MaxTurns:         parsed.maxTurns,
		AllowedTools:     parsed.allowedTools,
		SettingsPath:     settingsPath,
		PermissionArgs:   perm,
	})
}
