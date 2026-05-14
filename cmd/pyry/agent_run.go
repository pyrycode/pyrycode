package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"golang.org/x/sync/errgroup"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl/tail"
	"github.com/pyrycode/pyrycode/internal/agentrun/streamjson"
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
// surface, mark the workspace trusted, emit the per-spawn deny-default
// settings file, print its resolved path on stdout, mint a session UUIDv4,
// spawn `claude` in a PTY under --session-id, watch the on-disk JSONL via
// internal/agentrun/jsonl/tail, and re-emit each watcher Event onto stdout
// as line-delimited stream-json. On run termination a final `type:"result"`
// trailer is composed and emitted.
//
// Stdout contract: the `settings-file: ` marker line, followed by the
// re-emitted stream-json event lines (one per watcher Event), followed by
// exactly one `type:"result"` trailer. The dispatcher's parser consumes
// this stream as if it were `claude -p --output-format stream-json` output.
func runAgentRun(stdout io.Writer, args []string) error {
	parsed, err := parseAgentRunArgs(args)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("agent-run: resolving home directory: %w", err)
	}
	if err := agentrun.MarkWorkdirTrusted(home, parsed.workdir); err != nil {
		return fmt.Errorf("agent-run: pre-populating workspace trust: %w", err)
	}
	settingsPath, err := agentrun.WriteSettings(parsed.workdir, parsed.allowedTools)
	if err != nil {
		return fmt.Errorf("agent-run: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "settings-file: %s\n", settingsPath); err != nil {
		return fmt.Errorf("agent-run: write settings-file marker: %w", err)
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

	sessionID, err := newSessionUUID()
	if err != nil {
		return fmt.Errorf("agent-run: mint session id: %w", err)
	}

	emitter, err := streamjson.New(streamjson.Config{
		Writer:    stdout,
		SessionID: sessionID,
	})
	if err != nil {
		return fmt.Errorf("agent-run: stream emitter: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	watcher, err := tail.New(tail.Config{
		Workdir:     parsed.workdir,
		SessionID:   sessionID,
		HomeDir:     home,
		OnEvent:     func(ev jsonl.Event) { _ = emitter.Emit(ev) },
		OnEndOfTurn: cancel,
	})
	if err != nil {
		return fmt.Errorf("agent-run: tail watcher: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return watcher.Run(gctx) })
	g.Go(func() error {
		return agentrun.Drive(gctx, agentrun.DriveConfig{
			ClaudeBin:        claudeBin,
			WorkDir:          parsed.workdir,
			Args:             buildClaudeArgs(parsed, settingsPath, sessionID),
			PromptBytes:      promptBytes,
			TrustDialogDelay: parseDurationEnv("PYRY_AGENT_RUN_TRUST_DELAY"),
			PromptDelay:      parseDurationEnv("PYRY_AGENT_RUN_PROMPT_DELAY"),
		})
	})
	runErr := g.Wait()

	classifyForEmitter(emitter, runErr)
	if cerr := emitter.Close(); cerr != nil {
		slog.Default().Warn("agent-run: stream trailer write failed", "err", cerr)
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return fmt.Errorf("agent-run: drive: %w", runErr)
		}
		if errors.Is(runErr, context.Canceled) {
			return nil
		}
		return fmt.Errorf("agent-run: drive: %w", runErr)
	}
	return nil
}

// classifyForEmitter translates the errgroup result into a streamjson
// ExitReason override. nil and context.Canceled are not overrides — the
// emitter's Close defaults already handle the EOT-observed vs not-observed
// split. Any other error (ExitError, watcher I/O failure, etc.) classifies
// as ExitReasonError.
func classifyForEmitter(em *streamjson.Emitter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	em.SetExitReason(streamjson.ExitReasonError)
}

// newSessionUUID returns a fresh UUIDv4-shaped string for use as claude's
// --session-id. Mirrors internal/conversations/id.go:NewID's pattern; not
// extracted because this is the sole agent-run call site.
func newSessionUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// parseDurationEnv reads name as a time.Duration. Empty or unparseable
// values return zero, which DriveConfig interprets as "use the
// spike-validated production default". Production never sets these; the
// knobs exist so unit tests can compress sleeps to milliseconds.
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

// buildClaudeArgs constructs the argv passed to `claude` (without argv[0]).
//
// Two security invariants are pinned by tests:
//
//   - `--permission-mode default` MUST appear. The per-spawn settings file
//     emitted by #339 has `defaultMode: "deny"`; the upstream spike used
//     `acceptEdits`, which silently overrides the file's default and
//     defeats the whitelist. The literal flag pair is load-bearing.
//   - `--allowedTools` MUST NOT appear. In interactive mode under the
//     settings layer, `--allowedTools` is additive and silently broadens
//     the allow-list. The settings file is the sole authority.
//
// `--max-turns` and `--output-format` are accepted at the pyry CLI surface
// (the dispatcher requires them for budget bookkeeping and intent
// declaration respectively) but are NOT propagated to interactive claude:
// claude's interactive mode does not honour `--max-turns`, and
// `--output-format stream-json` is a `-p`-mode-only flag.
func buildClaudeArgs(parsed agentRunArgs, settingsPath, sessionID string) []string {
	return []string{
		"--settings", settingsPath,
		"--permission-mode", "default",
		"--model", parsed.model,
		"--append-system-prompt-file", parsed.systemPromptFile,
		"--effort", parsed.effort,
		"--session-id", sessionID,
	}
}
