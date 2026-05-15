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
	"time"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/agentrun/streamrunner"
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
		fmt.Fprintln(fs.Output(), "Drive a single supervised claude turn headlessly (scaffold only — no spawn yet).")
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
// surface, then spawn claude via streamrunner with the stream-json input/
// output formats. Claude's stdout (the canonical stream-json event stream,
// including its own `system init` and `result` events) is forwarded
// byte-for-byte to stdout; stderr is forwarded to os.Stderr.
//
// Stdout contract: claude's stdout, verbatim. The dispatcher's parser
// consumes this stream as if it were `claude -p --output-format stream-json`
// output.
func runAgentRun(stdout io.Writer, args []string) error {
	// --self-check is a sibling verb mode (#336): boot-time verification
	// that permissions.defaultMode "deny" in the per-spawn settings file
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

	err = streamrunner.Run(ctx, streamrunner.Config{
		ClaudeBin:   claudeBin,
		WorkDir:     parsed.workdir,
		Args:        buildClaudeArgs(parsed),
		PromptBytes: promptBytes,
		Stdout:      stdout,
		Stderr:      os.Stderr,
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("agent-run: %w", err)
}

// buildClaudeArgs constructs the argv passed to `claude` (without argv[0])
// for the stream-json subprocess pipeline.
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
func buildClaudeArgs(parsed agentRunArgs) []string {
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

// parseDurationEnv reads name as a time.Duration. Empty or unparseable
// values return zero, which selfcheck.Config interprets as "use the
// spike-validated production default". Production never sets these; the
// knobs exist so unit tests can compress sleeps to milliseconds. Used only
// by `pyry agent-run --self-check` (cmd/pyry/agent_run_selfcheck.go); the
// stream-json runtime path no longer has timing knobs.
func parseDurationEnv(name string) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return d
}
