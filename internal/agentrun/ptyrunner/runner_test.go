package ptyrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

const testSessionID = "6fc6d062-1972-4457-9bfd-6b47c7e77e11"

// helperRunCfg returns a Config wired to TestPtyRunnerHelperProcess. Tests
// override PromptBytes / writers as needed. Required string fields get
// arbitrary non-empty test values — buildArgs only stringifies them, the
// fake-claude does not parse argv.
//
// When jsonlBody != "", a per-test fake home is wired through HomeDir +
// helper env so the helper writes the body to the encoded JSONL path the
// watcher reads.
func helperRunCfg(t *testing.T, mode string, stdout, stderr *bytes.Buffer, jsonlBody string) Config {
	t.Helper()
	home := t.TempDir()
	workdir := t.TempDir()
	encoded, err := agentrun.EncodeProjectDir(workdir)
	if err != nil {
		t.Fatalf("EncodeProjectDir: %v", err)
	}
	jsonlPath := filepath.Join(home, ".claude", "projects", encoded, testSessionID+".jsonl")
	env := []string{
		"GO_PTYRUNNER_HELPER=1",
		"GO_PTYRUNNER_HELPER_MODE=" + mode,
		"GO_PTYRUNNER_JSONL_PATH=" + jsonlPath,
		"GO_PTYRUNNER_JSONL_BODY=" + jsonlBody,
	}
	return Config{
		ClaudeBin:    os.Args[0],
		WorkDir:      workdir,
		SessionID:    testSessionID,
		SettingsPath: "/dev/null",
		SystemPrompt: "/dev/null",
		Model:        "test-model",
		Effort:       "low",
		PromptBytes:  []byte("hi"),
		Stdout:       stdout,
		Stderr:       stderr,
		HomeDir:      home,
		Env:          env,
	}
}

// happyPathBody is a single-line JSONL assistant entry whose stop_reason +
// non-empty content text triggers the deterministic end-of-turn signal.
const happyPathBody = `{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"test","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}` + "\n"

// noEotBody is a single-line tool_use assistant entry — assistant kind but
// no end_turn — so the watcher keeps draining and never returns nil.
const noEotBody = `{"type":"assistant","message":{"id":"msg_t","role":"assistant","model":"test","stop_reason":"tool_use","content":[{"type":"text","text":"calling tool"}]}}` + "\n"

// trailer is the shape the streamjson.Emitter writes as its final line.
type trailer struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	NumTurns       int    `json:"num_turns"`
	StopReason     string `json:"stop_reason"`
	SessionID      string `json:"session_id"`
	TerminalReason string `json:"terminal_reason"`
}

// parseTrailer decodes the last newline-terminated line in buf as a trailer.
func parseTrailer(t *testing.T, buf []byte) trailer {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(buf, "\n"), []byte("\n"))
	if len(lines) == 0 {
		t.Fatalf("parseTrailer: empty buffer")
	}
	var tr trailer
	if err := json.Unmarshal(lines[len(lines)-1], &tr); err != nil {
		t.Fatalf("parseTrailer: unmarshal %q: %v", lines[len(lines)-1], err)
	}
	return tr
}

// failingWriter is an io.Writer that always returns an error.
type failingWriter struct{}

func (failingWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("simulated pipe broken")
}

func TestRun_HappyPath_EmitsAndEndOfTurn(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "jsonl", &stdout, &stderr, happyPathBody)

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

	got := stdout.Bytes()
	if !bytes.HasPrefix(got, []byte(happyPathBody)) {
		t.Fatalf("stdout missing verbatim JSONL prefix:\n got  = %q\n want = %q", got, happyPathBody)
	}

	tr := parseTrailer(t, got)
	if tr.Type != "result" {
		t.Errorf("trailer Type = %q, want result", tr.Type)
	}
	if tr.Subtype != "success" {
		t.Errorf("trailer Subtype = %q, want success", tr.Subtype)
	}
	if tr.TerminalReason != "completed" {
		t.Errorf("trailer TerminalReason = %q, want completed", tr.TerminalReason)
	}
	if tr.IsError {
		t.Errorf("trailer IsError = true, want false")
	}
	if tr.NumTurns != 1 {
		t.Errorf("trailer NumTurns = %d, want 1", tr.NumTurns)
	}
	if tr.StopReason != "end_turn" {
		t.Errorf("trailer StopReason = %q, want end_turn", tr.StopReason)
	}
	if tr.SessionID != cfg.SessionID {
		t.Errorf("trailer SessionID = %q, want %q", tr.SessionID, cfg.SessionID)
	}
}

func TestRun_TrustModalDetected(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "trust", &stdout, &stderr, "")

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
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "mcp_failure", &stdout, &stderr, "")

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
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "network_failure", &stdout, &stderr, "")

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
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "slow_spawn", &stdout, &stderr, "")

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

// syncBuffer is a thread-safe wrapper around bytes.Buffer so the test
// goroutine can poll Stdout while the Run goroutine writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, b.buf.Len())
	copy(out, b.buf.Bytes())
	return out
}

func TestRun_CtxCancelDuringStream(t *testing.T) {
	t.Parallel()
	stdout := &syncBuffer{}
	var stderr bytes.Buffer

	// helperRunCfg sets Stdout to a *bytes.Buffer; rebuild the cfg with
	// our concurrent-safe wrapper because the parent's emitter writes
	// from the Run goroutine while the test polls.
	base := helperRunCfg(t, "jsonl", &bytes.Buffer{}, &stderr, noEotBody)
	base.Stdout = stdout

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, base) }()

	// Wait for the first emitted line — proves the watcher surfaced the
	// non-EOT assistant entry through Emit.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(stdout.Bytes(), []byte("\n")) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("\n")) {
		t.Fatal("did not observe first emitted line within 5s")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v, want nil (ctx-cancel collapse)", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not return within 8s after ctx-cancel")
	}

	tr := parseTrailer(t, stdout.Bytes())
	if tr.Subtype != "error_during_execution" {
		t.Errorf("trailer Subtype = %q, want error_during_execution", tr.Subtype)
	}
	if tr.TerminalReason != "" {
		t.Errorf("trailer TerminalReason = %q, want empty", tr.TerminalReason)
	}
	if !tr.IsError {
		t.Errorf("trailer IsError = false, want true")
	}
}

func TestRun_EmitErrorPropagation(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	base := helperRunCfg(t, "jsonl", &bytes.Buffer{}, &stderr, happyPathBody)
	base.Stdout = failingWriter{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, base) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run: got nil, want emit-error")
		}
		if !strings.Contains(err.Error(), "ptyrunner: emit:") {
			t.Errorf("err = %v, want prefix ptyrunner: emit:", err)
		}
		if !strings.Contains(err.Error(), "simulated pipe broken") {
			t.Errorf("err = %v, want wrapped simulated pipe broken", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
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
		{"no Stdout", func(c *Config) { c.Stdout = nil }, "Stdout required"},
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
				Stdout:       &bytes.Buffer{},
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
