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

	"github.com/pyrycode/pyrycode/internal/agentrun/selfcheck"
)

// Canned stream-json fixtures used by the CLI-level self-check tests.
// Same shape as internal/agentrun/selfcheck/selfcheck_test.go; duplicated
// (small) to keep the test surface self-contained.
const (
	selfCheckPassLine = `{"type":"assistant","message":{"id":"msg_pass","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	selfCheckBashLine = `{"type":"assistant","message":{"id":"msg_bash","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"echo hello"}}],"usage":{"input_tokens":5,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
)

// TestSelfCheckCLIFakeClaude is the fake-claude entry point for the CLI-
// level self-check tests. The runAgentRunSelfCheck verb resolves
// PYRY_CLAUDE_BIN; setting that to a shell wrapper written by
// configureSelfCheckFakeClaude routes spawned children here.
//
// The wrapper handles `--version` separately (short-circuited in shell
// before re-exec) so captureClaudeVersion produces a clean line.
//
// Env knobs:
//
//   - GO_SELF_CHECK_CLI_FAKE_STDOUT: verbatim bytes to write to stdout.
//   - GO_SELF_CHECK_CLI_FAKE_LIFETIME: time.Duration the fake stays alive
//     after writing. Defaults to 200ms.
func TestSelfCheckCLIFakeClaude(t *testing.T) {
	if os.Getenv("GO_SELF_CHECK_CLI_FAKE") != "1" {
		return
	}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()

	if out := os.Getenv("GO_SELF_CHECK_CLI_FAKE_STDOUT"); out != "" {
		if _, err := os.Stdout.WriteString(out); err != nil {
			fmt.Fprintf(os.Stderr, "fake: write stdout: %v\n", err)
			os.Exit(98)
		}
		_ = os.Stdout.Sync()
	}

	lifetime := 200 * time.Millisecond
	if raw := os.Getenv("GO_SELF_CHECK_CLI_FAKE_LIFETIME"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			lifetime = d
		}
	}
	time.Sleep(lifetime)
	os.Exit(0)
}

// configureSelfCheckFakeClaude writes a /bin/sh wrapper that drops the
// production claude argv (the Go test binary rejects unknown flags),
// short-circuits --version to a literal echo (so captureClaudeVersion
// produces a clean line), and otherwise exec's the test binary in
// TestSelfCheckCLIFakeClaude mode. Pins PYRY_CLAUDE_BIN +
// GO_SELF_CHECK_CLI_FAKE_STDOUT via t.Setenv so the wrapper inherits both.
func configureSelfCheckFakeClaude(t *testing.T, stdoutFixture string) {
	t.Helper()
	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "fake-claude.sh")
	body := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "fake-claude 0.0.0"
  exit 0
fi
exec %q -test.run=^TestSelfCheckCLIFakeClaude$
`, os.Args[0])
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake-claude script: %v", err)
	}
	t.Setenv("PYRY_CLAUDE_BIN", script)
	t.Setenv("GO_SELF_CHECK_CLI_FAKE", "1")
	t.Setenv("GO_SELF_CHECK_CLI_FAKE_STDOUT", stdoutFixture)
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
	if !strings.Contains(got, "#329") || !strings.Contains(got, "#336") || !strings.Contains(got, "#375") {
		t.Errorf("stdout missing #329 / #336 / #375 references:\n%s", got)
	}
	// Negative pins: operator-string AC requires no PTY / settings-file /
	// permissions.defaultMode terminology survives in the FAIL message.
	forbidden := []string{
		"permissions.defaultMode",
		".pyry-agent-run-settings.json",
		"per-spawn settings file",
		"PTY",
	}
	for _, sub := range forbidden {
		if strings.Contains(got, sub) {
			t.Errorf("FAIL message contains forbidden substring %q:\n%s", sub, got)
		}
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
