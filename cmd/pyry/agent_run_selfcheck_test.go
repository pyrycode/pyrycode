package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/agentrun/selfcheck"
)

// selfCheckBashLine is the canned Bash tool_use assistant entry used as
// FAIL Evidence in the CLI-level tests. Same shape as the package-level
// fixture; duplicated to keep the test surface self-contained.
const selfCheckBashLine = `{"type":"assistant","message":{"id":"msg_bash","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"echo hello"}}],"usage":{"input_tokens":5,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

// installSelfCheckSeams captures the production seam values and restores
// them via t.Cleanup. Tests must NOT call t.Parallel — the seams are
// package-level.
func installSelfCheckSeams(t *testing.T) {
	t.Helper()
	origFn := selfCheckFn
	origVer := selfCheckGetVersion
	t.Cleanup(func() {
		selfCheckFn = origFn
		selfCheckGetVersion = origVer
	})
	selfCheckGetVersion = func(string) string { return "fake-claude 0.0.0" }
}

func TestRunAgentRunSelfCheck_PASS(t *testing.T) {
	installSelfCheckSeams(t)
	selfCheckFn = func(ctx context.Context, cfg selfcheck.Config) (selfcheck.Result, error) {
		return selfcheck.Result{EndOfTurnObserved: true, AssistantCount: 1}, nil
	}

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
	installSelfCheckSeams(t)
	selfCheckFn = func(ctx context.Context, cfg selfcheck.Config) (selfcheck.Result, error) {
		return selfcheck.Result{
				BashInvoked: true,
				Evidence:    []byte(selfCheckBashLine),
			},
			fmt.Errorf("%w: tool_use name=%q observed in assistant entry",
				selfcheck.ErrBashInvoked, "Bash")
	}

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
	// Required substrings: post-#473 the FAIL prose accurately names the
	// settings-file + permission-mode + PTY enforcement contract. These
	// MUST be present (the predecessor's forbidden-list pin is now
	// inverted).
	required := []string{
		`permissions.defaultMode: "dontAsk"`,
		`["Read"]`,
		"PTY",
		"#329",
		"#336",
		"#470",
		"#473",
	}
	for _, sub := range required {
		if !strings.Contains(got, sub) {
			t.Errorf("FAIL message missing required substring %q:\n%s", sub, got)
		}
	}
}

// TestRunAgentRun_SelfCheckShortCircuit verifies that --self-check
// bypasses parseAgentRunArgs: passing it alone (no required flags) must
// route to the self-check codepath rather than failing on
// `--prompt-file: required`.
func TestRunAgentRun_SelfCheckShortCircuit(t *testing.T) {
	installSelfCheckSeams(t)
	selfCheckFn = func(ctx context.Context, cfg selfcheck.Config) (selfcheck.Result, error) {
		return selfcheck.Result{EndOfTurnObserved: true, AssistantCount: 1}, nil
	}

	var stdout bytes.Buffer
	if err := runAgentRun(&stdout, []string{"--self-check"}); err != nil {
		t.Fatalf("--self-check alone failed: %v\nstdout=%q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "pyry agent-run --self-check:") {
		t.Errorf("stdout missing self-check marker, looks like parser ran:\n%s", stdout.String())
	}
}
