package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/selfcheck"
)

// Canned JSONL fixtures used by the CLI-level self-check tests. Same
// shape as internal/agentrun/selfcheck/selfcheck_test.go; duplicated
// (small) to keep the test surface self-contained and avoid an exported
// fixture surface in the helper package.
const (
	selfCheckPassLine = `{"type":"assistant","message":{"id":"msg_pass","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	selfCheckBashLine = `{"type":"assistant","message":{"id":"msg_bash","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"echo hello"}}],"usage":{"input_tokens":5,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
)

// TestSelfCheckCLIFakeClaude is the fake-claude entry point for the CLI-
// level self-check tests. The CLI wrapper (runAgentRunSelfCheck) does
// NOT receive a wrapper-script path from the test; it reads
// PYRY_CLAUDE_BIN from the env. Setting that to a wrapper script written
// by configureSelfCheckFakeClaude routes spawned children here.
//
// The fake mirrors TestSelfCheckHelperProcess in
// internal/agentrun/selfcheck/selfcheck_test.go: read --session-id from
// env, write the canned JSONL into the watched path, sleep briefly, exit.
func TestSelfCheckCLIFakeClaude(t *testing.T) {
	if os.Getenv("GO_SELF_CHECK_CLI_FAKE") != "1" {
		return
	}
	sid := os.Getenv("GO_SELF_CHECK_CLI_FAKE_SID")
	if sid == "" {
		fmt.Fprintln(os.Stderr, "fake: missing GO_SELF_CHECK_CLI_FAKE_SID")
		os.Exit(99)
	}
	content := os.Getenv("GO_SELF_CHECK_CLI_FAKE_JSONL")
	if content != "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake: getwd: %v\n", err)
			os.Exit(98)
		}
		encoded, err := agentrun.EncodeProjectDir(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake: encode: %v\n", err)
			os.Exit(98)
		}
		home := os.Getenv("HOME")
		if home == "" {
			fmt.Fprintln(os.Stderr, "fake: empty HOME")
			os.Exit(98)
		}
		dir := filepath.Join(home, ".claude", "projects", encoded)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "fake: mkdir: %v\n", err)
			os.Exit(97)
		}
		path := filepath.Join(dir, sid+".jsonl")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "fake: write: %v\n", err)
			os.Exit(96)
		}
	}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
}

// configureSelfCheckFakeClaude writes a /bin/sh wrapper that drops the
// production claude argv (the Go test binary rejects unknown flags),
// extracts --session-id, exports it via env, and exec's the test binary
// in TestSelfCheckCLIFakeClaude mode. Pins PYRY_CLAUDE_BIN and the
// self-check's GO_SELF_CHECK_CLI_FAKE_JSONL fixture via t.Setenv so the
// wrapper inherits both.
//
// Mirrors configureFakeClaude (agent_run_test.go) but routes to the
// self-check fake, not the production agent-run fake. The wrapper also
// short-circuits `--version` calls to a literal echo so
// captureClaudeVersion produces a clean line rather than triggering the
// fake's "missing --session-id" exit-99.
func configureSelfCheckFakeClaude(t *testing.T, jsonlContent string) {
	t.Helper()
	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "fake-claude.sh")
	body := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "fake-claude 0.0.0"
  exit 0
fi
SID=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session-id) SID="$2"; shift 2 ;;
    *) shift ;;
  esac
done
export GO_SELF_CHECK_CLI_FAKE_SID="$SID"
exec %q -test.run=^TestSelfCheckCLIFakeClaude$
`, os.Args[0])
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake-claude script: %v", err)
	}
	t.Setenv("PYRY_CLAUDE_BIN", script)
	t.Setenv("GO_SELF_CHECK_CLI_FAKE", "1")
	t.Setenv("GO_SELF_CHECK_CLI_FAKE_JSONL", jsonlContent)
	t.Setenv("PYRY_AGENT_RUN_TRUST_DELAY", "20ms")
	t.Setenv("PYRY_AGENT_RUN_PROMPT_DELAY", "20ms")
	t.Setenv("HOME", t.TempDir())
}

func TestRunAgentRunSelfCheck_PASS(t *testing.T) {
	configureSelfCheckFakeClaude(t, selfCheckPassLine+"\n")

	var stdout bytes.Buffer
	if err := runAgentRun(&stdout, []string{"--self-check"}); err != nil {
		t.Fatalf("runAgentRun: unexpected error: %v\nstdout=%q", err, stdout.String())
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "pyry agent-run --self-check: PASS\n") {
		t.Errorf("stdout does not start with PASS marker:\n%s", got)
	}
	if !strings.Contains(got, "claude version: fake-claude 0.0.0") {
		t.Errorf("stdout missing claude version line:\n%s", got)
	}
	if !strings.Contains(got, "deny-default whitelist held") {
		t.Errorf("stdout missing pass confirmation line:\n%s", got)
	}
}

func TestRunAgentRunSelfCheck_FAIL(t *testing.T) {
	configureSelfCheckFakeClaude(t, selfCheckBashLine+"\n")

	var stdout bytes.Buffer
	err := runAgentRun(&stdout, []string{"--self-check"})
	if !errors.Is(err, selfcheck.ErrBashInvoked) {
		t.Fatalf("err = %v, want ErrBashInvoked\nstdout=%q", err, stdout.String())
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "pyry agent-run --self-check: FAIL") {
		t.Errorf("stdout does not start with FAIL marker:\n%s", got)
	}
	if !strings.Contains(got, `"name":"Bash"`) {
		t.Errorf("stdout missing verbatim Evidence line with \"name\":\"Bash\":\n%s", got)
	}
	if !strings.Contains(got, "#329") || !strings.Contains(got, "#336") {
		t.Errorf("stdout missing #329 / #336 references:\n%s", got)
	}
}

// TestRunAgentRun_SelfCheckShortCircuit verifies that --self-check
// bypasses parseAgentRunArgs: passing it alone (no required flags) must
// route to the self-check codepath rather than failing on
// `--prompt-file: required`.
func TestRunAgentRun_SelfCheckShortCircuit(t *testing.T) {
	configureSelfCheckFakeClaude(t, selfCheckPassLine+"\n")

	var stdout bytes.Buffer
	if err := runAgentRun(&stdout, []string{"--self-check"}); err != nil {
		t.Fatalf("--self-check alone failed: %v\nstdout=%q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "pyry agent-run --self-check:") {
		t.Errorf("stdout missing self-check marker, looks like parser ran:\n%s", stdout.String())
	}
}
