package ptyrunner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// helperRunCfg returns a Config wired to TestPtyRunnerHelperProcess. Tests
// override PromptBytes / writers as needed. Required string fields get
// arbitrary non-empty test values — buildArgs only stringifies them, the
// fake-claude does not parse argv.
func helperRunCfg(t *testing.T, mode string, stderr *bytes.Buffer, extraEnv ...string) Config {
	t.Helper()
	env := append([]string{
		"GO_PTYRUNNER_HELPER=1",
		"GO_PTYRUNNER_HELPER_MODE=" + mode,
	}, extraEnv...)
	return Config{
		ClaudeBin:    os.Args[0],
		WorkDir:      t.TempDir(),
		SessionID:    "00000000-0000-0000-0000-000000000000",
		SettingsPath: "/dev/null",
		SystemPrompt: "/dev/null",
		Model:        "test-model",
		Effort:       "low",
		PromptBytes:  []byte("hi"),
		Stderr:       stderr,
		Env:          env,
	}
}

func TestRun_HappyPath(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	cfg := helperRunCfg(t, "idle", &stderr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("Run took %v, want < 5s", elapsed)
	}
}

func TestRun_TrustModalDetected(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	cfg := helperRunCfg(t, "trust", &stderr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := Run(ctx, cfg)
	if err == nil {
		t.Fatal("Run: got nil, want trust-modal error")
	}
	if !errors.Is(err, ErrTrustModalDetected) {
		t.Fatalf("Run: err = %v, want errors.Is(err, ErrTrustModalDetected)", err)
	}
	if !strings.Contains(err.Error(), "#469's MarkWorkdirTrusted") {
		t.Errorf("err message missing #469 remediation hint: %q", err.Error())
	}
}

func TestRun_McpFailureDetected(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	cfg := helperRunCfg(t, "mcp_failure", &stderr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := Run(ctx, cfg)
	if err == nil {
		t.Fatal("Run: got nil, want MCP-failure error")
	}
	if !errors.Is(err, ErrMcpFailureBanner) {
		t.Fatalf("Run: err = %v, want errors.Is(err, ErrMcpFailureBanner)", err)
	}
	if !strings.Contains(err.Error(), "MCP failure banner") {
		t.Errorf("err message missing MCP-failure detector name: %q", err.Error())
	}
}

func TestRun_NetworkFailureDetected(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	cfg := helperRunCfg(t, "network_failure", &stderr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := Run(ctx, cfg)
	if err == nil {
		t.Fatal("Run: got nil, want network-failure error")
	}
	if !errors.Is(err, ErrNetworkFailure) {
		t.Fatalf("Run: err = %v, want errors.Is(err, ErrNetworkFailure)", err)
	}
	if !strings.Contains(err.Error(), "FailedToOpenSocket") {
		t.Errorf("err message missing FailedToOpenSocket anchor: %q", err.Error())
	}
}

func TestRun_CtxCancelDuringSpawn(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	cfg := helperRunCfg(t, "slow_spawn", &stderr)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run after ctx cancel: %v, want nil (operator-shutdown collapse)", err)
	}
	elapsed := time.Since(start)
	// SIGTERM grace 3s default + the 100ms-cancel + a slack budget.
	if elapsed > 8*time.Second {
		t.Errorf("Run took %v, want < 8s (Close grace likely fell through to SIGKILL)", elapsed)
	}
}

func TestBuildArgs(t *testing.T) {
	t.Parallel()
	got := buildArgs(Config{
		SessionID:    "sid-xyz",
		SettingsPath: "/tmp/s.json",
		SystemPrompt: "/tmp/sp.txt",
		Model:        "the-model",
		Effort:       "high",
	})
	want := []string{
		"--session-id", "sid-xyz",
		"--settings", "/tmp/s.json",
		"--permission-mode", "default",
		"--append-system-prompt-file", "/tmp/sp.txt",
		"--model", "the-model",
		"--effort", "high",
	}
	if len(got) != len(want) {
		t.Fatalf("buildArgs len = %d, want %d\n got = %q\nwant = %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("buildArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Forbidden flags must not appear.
	for _, forbidden := range []string{
		"--input-format", "--output-format", "--verbose",
		"--dangerously-skip-permissions", "--max-turns", "--allowed-tools",
	} {
		for _, a := range got {
			if a == forbidden {
				t.Errorf("buildArgs unexpectedly contains %q", forbidden)
			}
		}
	}
}

func TestRun_MissingRequiredFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no ClaudeBin", func(c *Config) { c.ClaudeBin = "" }, "ClaudeBin required"},
		{"no WorkDir", func(c *Config) { c.WorkDir = "" }, "WorkDir required"},
		{"no SessionID", func(c *Config) { c.SessionID = "" }, "SessionID required"},
		{"no SettingsPath", func(c *Config) { c.SettingsPath = "" }, "SettingsPath required"},
		{"no SystemPrompt", func(c *Config) { c.SystemPrompt = "" }, "SystemPrompt required"},
		{"no Model", func(c *Config) { c.Model = "" }, "Model required"},
		{"no Effort", func(c *Config) { c.Effort = "" }, "Effort required"},
		{"no PromptBytes", func(c *Config) { c.PromptBytes = nil }, "PromptBytes required"},
		{"no Stderr", func(c *Config) { c.Stderr = nil }, "Stderr required"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{
				ClaudeBin:    "/nonexistent/claude",
				WorkDir:      "/tmp",
				SessionID:    "sid",
				SettingsPath: "/dev/null",
				SystemPrompt: "/dev/null",
				Model:        "m",
				Effort:       "e",
				PromptBytes:  []byte("x"),
				Stderr:       &bytes.Buffer{},
			}
			tc.mutate(&cfg)
			err := Run(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run err = %v, want substring %q", err, tc.want)
			}
		})
	}
}
