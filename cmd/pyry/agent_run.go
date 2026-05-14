package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/agentrun"
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
// surface, emit the per-spawn deny-default settings file, and print its
// resolved path on stdout behind the stable `settings-file: ` marker so the
// dispatcher (sibling #332) can scrape it. The marker line is the sole
// stdout contract — no other line is printed on success.
func runAgentRun(args []string) error {
	parsed, err := parseAgentRunArgs(args)
	if err != nil {
		return err
	}
	path, err := agentrun.WriteSettings(parsed.workdir, parsed.allowedTools)
	if err != nil {
		return fmt.Errorf("agent-run: %w", err)
	}
	fmt.Printf("settings-file: %s\n", path)
	return nil
}
