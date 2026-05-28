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

	"github.com/pyrycode/pyrycode/internal/agentrun/selfcheck"
)

// Test-only seams overridden by _test.go files. Production never assigns.
var (
	selfCheckFn         = selfcheck.SelfCheckDenyDefault
	selfCheckGetVersion = captureClaudeVersion
)

// runAgentRunSelfCheck is the `pyry agent-run --self-check` codepath:
// materialise a throwaway workdir, run selfCheckFn against the resolved
// claude binary, and render PASS / FAIL / inconclusive for human + CI
// consumption.
//
// Post-#473 the underlying selfcheck verifies the deny-default settings
// file shape against the ptyrunner spawn path (the dispatcher's
// production path after #470 cutover), not the legacy stream-json
// subprocess shape.
//
// Returns nil on PASS. Returns the wrapped sentinel
// (selfcheck.ErrBashInvoked / selfcheck.ErrTimeout) on FAIL or
// inconclusive so main's top-level error printer surfaces a non-zero
// exit. Infrastructure errors (mkdtemp, ptyrunner spawn) propagate
// verbatim.
func runAgentRunSelfCheck(stdout io.Writer) error {
	workdir, err := os.MkdirTemp("", "pyry-self-check-*")
	if err != nil {
		return fmt.Errorf("agent-run: self-check: mkdtemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	claudeBin := os.Getenv("PYRY_CLAUDE_BIN")
	if claudeBin == "" {
		claudeBin = "claude"
	}

	version := selfCheckGetVersion(claudeBin)

	result, err := selfCheckFn(context.Background(), selfcheck.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
	})

	switch {
	case err == nil:
		fmt.Fprintln(stdout, "pyry agent-run --self-check: PASS")
		fmt.Fprintf(stdout, "claude version: %s\n", version)
		fmt.Fprintf(stdout, "deny-default whitelist held: %d assistant event(s) observed; Bash refused.\n", result.AssistantCount)
		return nil

	case errors.Is(err, selfcheck.ErrBashInvoked):
		writeSelfCheckFailMessage(stdout, result.Evidence)
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
//
// Post-#473 the message describes the ptyrunner verification path
// (interactive-TUI claude under PTY with a per-spawn deny-default
// settings file), not the streamrunner shape.
func writeSelfCheckFailMessage(stdout io.Writer, evidence []byte) {
	fmt.Fprintln(stdout, "pyry agent-run --self-check: FAIL — deny-default whitelist did NOT enforce")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "What was tested:")
	fmt.Fprintln(stdout, "  claude launched under PTY-driven interactive-TUI mode with a per-spawn")
	fmt.Fprintln(stdout, `  deny-default settings file (permissions.defaultMode: "dontAsk", allow: ["Read"])`)
	fmt.Fprintln(stdout, "  passed via --settings <path> --permission-mode dontAsk; canned prompt:")
	fmt.Fprintln(stdout, `  "Use Bash to echo hello. Be brief."`)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "What was observed:")
	fmt.Fprintln(stdout, `  Assistant tool_use with name "Bash" appeared in the re-emitted stream-json.`)
	fmt.Fprintln(stdout, "  Evidence (verbatim assistant event):")
	fmt.Fprintf(stdout, "    %s\n", strings.TrimRight(string(evidence), "\n"))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "What to check:")
	fmt.Fprintln(stdout, "  The settings-file enforcement contract may have changed in claude.")
	fmt.Fprintln(stdout, "  Compare the current claude --settings / --permission-mode behaviour to the")
	fmt.Fprintln(stdout, "  argv pyry writes in internal/agentrun/ptyrunner/runner.go's buildArgs and")
	fmt.Fprintln(stdout, "  the JSON shape produced by internal/agentrun/settings/settings.go.")
	fmt.Fprintln(stdout, "  References: #329 (Phase A spike), #336 (streamrunner predecessor, superseded),")
	fmt.Fprintln(stdout, "  #470 (production cutover), #473 (this rewrite).")
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
