package ptyrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// loggerSyncWriter serialises Write calls for the slog test handler; slog
// handlers may write concurrently from defers / timer goroutines.
type loggerSyncWriter struct {
	mu sync.Mutex
	w  strings.Builder
}

func (s *loggerSyncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *loggerSyncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}

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
	jsonlPath := tuidriver.SessionJSONLPath(home, workdir, testSessionID)
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
		AllowedTools: []string{"Read"},
		MaxTurns:     5,
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

// failingWriter discards the first failAfter writes and then returns
// "simulated pipe broken" on every subsequent write. failAfter=0 means
// "fail every write".
type failingWriter struct {
	mu        sync.Mutex
	failAfter int
	writes    int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.writes <= f.failAfter {
		return len(p), nil
	}
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
	lines := bytes.Split(bytes.TrimRight(got, "\n"), []byte("\n"))
	if len(lines) < 3 {
		t.Fatalf("stdout has %d lines, want >=3 (init + assistant + trailer):\n%s", len(lines), got)
	}

	// Line 0: synthesised init envelope.
	var init struct {
		Type      string   `json:"type"`
		Subtype   string   `json:"subtype"`
		Cwd       string   `json:"cwd"`
		Tools     []string `json:"tools"`
		Model     string   `json:"model"`
		SessionID string   `json:"session_id"`
	}
	if err := json.Unmarshal(lines[0], &init); err != nil {
		t.Fatalf("decode init line %q: %v", lines[0], err)
	}
	if init.Type != "system" || init.Subtype != "init" {
		t.Errorf("init (Type,Subtype) = (%q,%q), want (system,init)", init.Type, init.Subtype)
	}
	if init.Cwd != cfg.WorkDir {
		t.Errorf("init Cwd = %q, want %q", init.Cwd, cfg.WorkDir)
	}
	if init.Model != cfg.Model {
		t.Errorf("init Model = %q, want %q", init.Model, cfg.Model)
	}
	if init.SessionID != cfg.SessionID {
		t.Errorf("init SessionID = %q, want %q", init.SessionID, cfg.SessionID)
	}
	if !reflect.DeepEqual(init.Tools, cfg.AllowedTools) {
		t.Errorf("init Tools = %v, want %v", init.Tools, cfg.AllowedTools)
	}

	// Line 1: verbatim JSONL assistant entry from the helper.
	if string(lines[1])+"\n" != happyPathBody {
		t.Errorf("emitted assistant line not byte-equivalent:\n got  = %q\n want = %q", lines[1], strings.TrimRight(happyPathBody, "\n"))
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

// TestRun_McpFailureNonFatal asserts an MCP-failure banner does NOT abort
// the run. A failing ambient MCP server (often one the agent does not even
// need) must not collapse a dispatched turn: streamrunner never aborted on
// this, and treating it as fatal regressed every spawn whose env had any
// offline MCP server into `error_during_execution`/"no output" (root-caused
// 2026-05-29 — the banner detector was designed to SURFACE failures to a
// host UI, not to abort the dispatcher path). The "mcp_failure" fixture
// renders only the banner and no end-of-turn, so Run proceeds past it and
// ends via ctx/watchdog. Contract: ErrMcpFailureBanner is never returned.
// The banner-then-complete happy path is covered against live claude by the
// agent-run repro harness.
func TestRun_McpFailureNonFatal(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "mcp_failure", &stdout, &stderr, "")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := Run(ctx, cfg); errors.Is(err, ErrMcpFailureBanner) {
		t.Fatalf("Run: MCP banner must be non-fatal, got ErrMcpFailureBanner: %v", err)
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

func TestRun_MidRun_ModalAndBannerDetection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		mode      string
		want      error
		substring string
	}{
		{
			name:      "trust modal mid-run",
			mode:      "mid_trust",
			want:      ErrTrustModalDetected,
			substring: "#469's MarkWorkdirTrusted",
		},
		// mcp failure banner mid-run is intentionally absent: an MCP banner
		// is non-fatal (see TestRun_McpFailureNonFatal). Trust-modal and
		// network-failure remain fatal mid-run.
		{
			name:      "network failure mid-run",
			mode:      "mid_network_failure",
			want:      ErrNetworkFailure,
			substring: "FailedToOpenSocket",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			cfg := helperRunCfg(t, tc.mode, &stdout, &stderr, "")

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := Run(ctx, cfg)
			if err == nil {
				t.Fatalf("Run: got nil, want %v", tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run: err = %v, want errors.Is(err, %v)", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.substring) {
				t.Errorf("err message missing %q substring: %q", tc.substring, err.Error())
			}
		})
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
	// failAfter=1 so streamjson.New's init write succeeds; the watcher's
	// first Emit call hits the failure and ptyrunner surfaces it.
	base.Stdout = &failingWriter{failAfter: 1}

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
		"--permission-mode", "dontAsk",
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

func TestRun_BudgetHitBeforeEndOfTurn(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "jsonl", &stdout, &stderr, noEotBody)
	cfg.MaxTurns = 1

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 8*time.Second {
		t.Errorf("Run took %v, want < 8s", elapsed)
	}

	got := stdout.Bytes()
	// Skip the leading synthesised init line; the noEotBody assistant
	// entry is the SECOND line on stdout (init + assistant + trailer).
	initEnd := bytes.IndexByte(got, '\n')
	if initEnd < 0 {
		t.Fatalf("stdout missing init line: %q", got)
	}
	afterInit := got[initEnd+1:]
	if !bytes.HasPrefix(afterInit, []byte(noEotBody)) {
		t.Fatalf("stdout missing verbatim JSONL after init:\n got  = %q\n want prefix = %q", afterInit, noEotBody)
	}

	tr := parseTrailer(t, got)
	if tr.Type != "result" {
		t.Errorf("trailer Type = %q, want result", tr.Type)
	}
	if tr.Subtype != "error_max_turns" {
		t.Errorf("trailer Subtype = %q, want error_max_turns", tr.Subtype)
	}
	if tr.TerminalReason != "max_turns" {
		t.Errorf("trailer TerminalReason = %q, want max_turns", tr.TerminalReason)
	}
	if !tr.IsError {
		t.Errorf("trailer IsError = false, want true")
	}
	if tr.NumTurns != 1 {
		t.Errorf("trailer NumTurns = %d, want 1", tr.NumTurns)
	}
	if tr.SessionID != cfg.SessionID {
		t.Errorf("trailer SessionID = %q, want %q", tr.SessionID, cfg.SessionID)
	}
}

func TestRun_MaxTurnsExhaustion_NoBenignWarns(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	// noEotBody is an assistant entry without end_turn — once MaxTurns is
	// reached, the budget Counter SIGTERMs the helper, which exits 143.
	cfg := helperRunCfg(t, "jsonl_exit143", &stdout, &stderr, noEotBody)
	cfg.MaxTurns = 1

	logBuf := &loggerSyncWriter{}
	cfg.Logger = slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := logBuf.String()
	for _, forbidden := range []string{
		"ptyrunner: close failed",
		"budget: terminate failed",
		"budget: kill failed",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("captured WARN includes %q (benign teardown error misclassified):\n%s",
				forbidden, out)
		}
	}
}

func TestRun_WatchdogFires(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "jsonl", &stdout, &stderr, "")
	cfg.MaxTurns = 10
	cfg.WatchdogTick = 50 * time.Millisecond
	cfg.WatchdogTrackerOpts = tuidriver.TrackerOpts{
		PTYQuietLimit:      200 * time.Millisecond,
		SpinnerFreezeLimit: 200 * time.Millisecond,
	}
	// Short prompt-commit window so the (intentionally) never-committing
	// fixture exhausts its re-delivery budget fast and reaches the watchdog
	// path well inside the deadline.
	cfg.PromptCommitTimeout = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v, want nil (watchdog-fire collapse)", err)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("Run took %v, want < 5s", elapsed)
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
		{"no AllowedTools", func(c *Config) { c.AllowedTools = nil }, "AllowedTools required"},
		{"no PromptBytes", func(c *Config) { c.PromptBytes = nil }, "PromptBytes required"},
		{"no Stdout", func(c *Config) { c.Stdout = nil }, "Stdout required"},
		{"no Stderr", func(c *Config) { c.Stderr = nil }, "Stderr required"},
		{"no MaxTurns", func(c *Config) { c.MaxTurns = 0 }, "MaxTurns required"},
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
				AllowedTools: []string{"Read"},
				MaxTurns:     1,
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
