package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/selfcheck"
)

// runAgentRunSelfCheck is the `pyry agent-run --self-check` codepath:
// materialise a throwaway workdir + canonical deny-default settings file,
// run selfcheck.SelfCheckDenyDefault against the resolved claude binary,
// and render PASS / FAIL / inconclusive for human + CI consumption.
//
// Returns nil on PASS. Returns the wrapped sentinel
// (selfcheck.ErrBashInvoked / selfcheck.ErrTimeout) on FAIL or
// inconclusive so main's top-level error printer surfaces a non-zero
// exit. Infrastructure errors (mkdtemp, write, exec spawn) propagate
// verbatim.
func runAgentRunSelfCheck(stdout io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("agent-run: self-check: home dir: %w", err)
	}
	workdir, err := os.MkdirTemp("", "pyry-self-check-*")
	if err != nil {
		return fmt.Errorf("agent-run: self-check: mkdtemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	claudeBin := os.Getenv("PYRY_CLAUDE_BIN")
	if claudeBin == "" {
		claudeBin = "claude"
	}

	if _, err := agentrun.WriteSettings(workdir, []string{"Read"}); err != nil {
		return fmt.Errorf("agent-run: self-check: %w", err)
	}

	version := captureClaudeVersion(claudeBin)

	result, err := selfcheck.SelfCheckDenyDefault(context.Background(), selfcheck.Config{
		ClaudeBin:        claudeBin,
		HomeDir:          home,
		Workdir:          workdir,
		TrustDialogDelay: parseDurationEnv("PYRY_AGENT_RUN_TRUST_DELAY"),
		PromptDelay:      parseDurationEnv("PYRY_AGENT_RUN_PROMPT_DELAY"),
	})

	switch {
	case err == nil:
		fmt.Fprintln(stdout, "pyry agent-run --self-check: PASS")
		fmt.Fprintf(stdout, "claude version: %s\n", version)
		fmt.Fprintf(stdout, "deny-default whitelist held: %d assistant event(s) observed; Bash refused.\n", result.AssistantCount)
		return nil

	case errors.Is(err, selfcheck.ErrBashInvoked):
		writeSelfCheckFailMessage(stdout, workdir, result.Evidence)
		return err

	case errors.Is(err, selfcheck.ErrTimeout):
		fmt.Fprintln(stdout, "pyry agent-run --self-check: INCONCLUSIVE — overall timeout fired")
		fmt.Fprintf(stdout, "claude version: %s\n", version)
		fmt.Fprintln(stdout, "Neither an end-of-turn nor a Bash invocation was observed before the")
		fmt.Fprintln(stdout, "self-check's overall timeout. Treat as infrastructure failure (not a")
		fmt.Fprintln(stdout, "security failure); retry once before paging.")
		return err

	default:
		return err
	}
}

// writeSelfCheckFailMessage renders the multi-line FAIL operator-
// affordance to stdout. Pinned by TestRunAgentRunSelfCheck_FAIL —
// any rewording must update that test in lockstep.
func writeSelfCheckFailMessage(stdout io.Writer, workdir string, evidence []byte) {
	fmt.Fprintln(stdout, "pyry agent-run --self-check: FAIL — deny-default whitelist did NOT enforce")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "What was tested:")
	fmt.Fprintf(stdout, "  per-spawn settings file at %s/%s with\n", workdir, agentrun.SettingsFilename)
	fmt.Fprintln(stdout, `  permissions.defaultMode "deny" and permissions.allow ["Read"]; canned`)
	fmt.Fprintln(stdout, `  prompt: "Use Bash to echo hello. Be brief."`)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "What was observed:")
	fmt.Fprintln(stdout, `  Assistant tool_use with name "Bash" appeared in claude's JSONL output.`)
	fmt.Fprintln(stdout, "  Evidence (verbatim line from the session JSONL):")
	fmt.Fprintf(stdout, "    %s\n", strings.TrimRight(string(evidence), "\n"))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "What to check:")
	fmt.Fprintln(stdout, "  The permissions.defaultMode schema may have changed in claude. Compare")
	fmt.Fprintln(stdout, "  the current claude `--settings` schema docs to the shape pyry writes in")
	fmt.Fprintln(stdout, "  internal/agentrun/settings.go. References: #329 (Phase A spike) and #336")
	fmt.Fprintln(stdout, "  (this self-check).")
}

// captureClaudeVersion runs `claude --version` with a 5-second budget and
// returns the trimmed stdout. Best-effort: returns "<unavailable>" on any
// failure (binary not on PATH, non-zero exit, timeout). Self-check
// proceeds either way — the version line is operator affordance, not a
// correctness gate.
func captureClaudeVersion(claudeBin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, claudeBin, "--version").Output()
	if err != nil {
		return "<unavailable>"
	}
	return strings.TrimSpace(string(out))
}
