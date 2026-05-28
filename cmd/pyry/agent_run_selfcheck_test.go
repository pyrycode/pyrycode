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
	const sentinelPath = "/tmp/pyry-self-check-XXXX/probe-sentinel.txt"
	selfCheckFn = func(ctx context.Context, cfg selfcheck.Config) (selfcheck.Result, error) {
		return selfcheck.Result{
				SentinelWritten: true,
				SentinelPath:    sentinelPath,
			},
			fmt.Errorf("%w: probe sentinel appeared at %s",
				selfcheck.ErrSentinelWritten, sentinelPath)
	}

	var stdout bytes.Buffer
	err := runAgentRun(&stdout, []string{"--self-check"})
	if !errors.Is(err, selfcheck.ErrSentinelWritten) {
		t.Fatalf("err = %v, want ErrSentinelWritten\nstdout=%q", err, stdout.String())
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "pyry agent-run --self-check: FAIL") {
		t.Errorf("stdout does not start with FAIL marker:\n%s", got)
	}
	if !strings.Contains(got, sentinelPath) {
		t.Errorf("stdout missing the sentinel path evidence %q:\n%s", sentinelPath, got)
	}
	// Required substrings: post-#542 the FAIL prose reports execution-layer
	// evidence (the sentinel file on disk) and extends the historical-
	// reference chain through #542. The LLM-layer artifacts ("Use Write to
	// create a file named probe.txt", "name":"Write") are gone. These MUST
	// be present.
	required := []string{
		`permissions.defaultMode: "dontAsk"`,
		`["Read"]`,
		"PTY",
		"appeared on disk",
		"#329",
		"#336",
		"#470",
		"#473",
		"#538",
		"#539",
		"#542",
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
