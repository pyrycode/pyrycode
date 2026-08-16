package selfcheck

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/streamrunner"
)

// Canned stream-json fixtures used across the self-check tests. Each is a
// single JSONL line (no trailing newline); the streamRun mock writes these to
// cfg.Stdout with newline separators.
const (
	// passLine: assistant entry with stop_reason "end_turn" and a single
	// text content block. Satisfies jsonl.Reader's deterministic end-of-
	// turn rule (stop_reason "end_turn" AND sum of text > 0).
	passLine = `{"type":"assistant","message":{"id":"msg_pass","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	// writeLine: assistant entry whose content carries a tool_use block
	// with name "Write" — normal LLM output that claude's runtime denies
	// between emission and execution. Post-#542 the detector is
	// execution-layer (os.Stat of the sentinel), so an emitted-but-denied
	// tool_use in the stream must NOT trip FAIL; this fixture pins that in
	// TestSelfCheck_ToolUseInStreamDoesNotFail.
	writeLine = `{"type":"assistant","message":{"id":"msg_write","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_1","name":"Write","input":{"file_path":"probe.txt","content":"hello"}}],"usage":{"input_tokens":5,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
)

// installSeams captures the production seam values, installs no-op
// replacements that the per-test body then overrides selectively, and
// restores the originals via t.Cleanup. Tests must NOT call t.Parallel —
// the seams are package-level.
func installSeams(t *testing.T) {
	t.Helper()
	origTrust := trustMark
	origSettings := settingsWrite
	origStream := streamRun
	t.Cleanup(func() {
		trustMark = origTrust
		settingsWrite = origSettings
		streamRun = origStream
	})
	// Default benign overrides; per-test bodies replace the ones they care
	// about. Ensures no test accidentally hits ~/.claude.json,
	// os.TempDir(), or the real stream runner.
	trustMark = func(workdir string) (string, error) { return workdir, nil }
	settingsWrite = func(allowed []string) (string, error) { return "/tmp/test-settings.json", nil }
	streamRun = func(ctx context.Context, cfg streamrunner.Config) error {
		t.Errorf("streamRun unexpectedly invoked; test should override it")
		return nil
	}
}

// baseConfig returns a Config wired with a short OverallTimeout and the
// minimal required fields. Per-test bodies layer in overrides.
func baseConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		ClaudeBin:      "/usr/bin/claude-fake",
		WorkDir:        t.TempDir(),
		OverallTimeout: 5 * time.Second,
	}
}

func TestSelfCheck_Pass(t *testing.T) {
	installSeams(t)
	streamRun = func(ctx context.Context, cfg streamrunner.Config) error {
		if _, err := io.WriteString(cfg.Stdout, passLine+"\n"); err != nil {
			return err
		}
		// Hold briefly so the watcher's reader actually consumes the
		// line before pw closes.
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}

	result, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if err != nil {
		t.Fatalf("SelfCheckDenyDefault: unexpected error: %v\nresult=%+v", err, result)
	}
	if result.SentinelWritten {
		t.Errorf("SentinelWritten = true, want false")
	}
	if !result.EndOfTurnObserved {
		t.Errorf("EndOfTurnObserved = false, want true")
	}
	if result.AssistantCount != 1 {
		t.Errorf("AssistantCount = %d, want 1", result.AssistantCount)
	}
	if result.SentinelPath != "" {
		t.Errorf("SentinelPath = %q, want \"\"", result.SentinelPath)
	}
}

// TestSelfCheck_SentinelWritten pins the FAIL mechanism after the layer
// swap: the verdict is the sentinel file on disk, not a tool_use block in
// the stream. The mock simulates a leaked boundary by writing the sentinel
// inside the spawn's workdir; even though end_turn is also observed, the
// stat-first verdict returns ErrSentinelWritten.
func TestSelfCheck_SentinelWritten(t *testing.T) {
	installSeams(t)
	cfg := baseConfig(t)
	wantSentinel := filepath.Join(cfg.WorkDir, probeSentinelName)
	streamRun = func(ctx context.Context, pcfg streamrunner.Config) error {
		// Boundary leaked: claude's runtime executed Write and the sentinel
		// landed on disk at the path the prompt named (pcfg.WorkDir is the
		// trust-marked realpath, identity-mocked to cfg.WorkDir).
		if err := os.WriteFile(filepath.Join(pcfg.WorkDir, probeSentinelName), []byte("hello"), 0o600); err != nil {
			return err
		}
		if _, err := io.WriteString(pcfg.Stdout, passLine+"\n"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}

	result, err := SelfCheckDenyDefault(context.Background(), cfg)
	if !errors.Is(err, ErrSentinelWritten) {
		t.Fatalf("err = %v, want ErrSentinelWritten\nresult=%+v", err, result)
	}
	if !result.SentinelWritten {
		t.Errorf("SentinelWritten = false, want true")
	}
	if result.SentinelPath != wantSentinel {
		t.Errorf("SentinelPath = %q, want %q", result.SentinelPath, wantSentinel)
	}
}

// TestSelfCheck_ToolUseInStreamDoesNotFail is the regression net for the
// whole ticket: an emitted-but-denied tool_use is normal LLM output (the
// model emits the Write block, claude's runtime denies execution, no file
// lands). Pre-#542 this false-FAILed; the execution-layer detector must
// PASS it.
func TestSelfCheck_ToolUseInStreamDoesNotFail(t *testing.T) {
	installSeams(t)
	streamRun = func(ctx context.Context, cfg streamrunner.Config) error {
		// Emit a Write tool_use then end_turn — but create NO file.
		if _, err := io.WriteString(cfg.Stdout, writeLine+"\n"+passLine+"\n"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}

	result, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if err != nil {
		t.Fatalf("SelfCheckDenyDefault: unexpected error: %v\nresult=%+v", err, result)
	}
	if result.SentinelWritten {
		t.Errorf("SentinelWritten = true, want false")
	}
	if !result.EndOfTurnObserved {
		t.Errorf("EndOfTurnObserved = false, want true")
	}
}

// TestProbeToolIsNotInAllowList pins the coupling invariant between the
// probe-tool and the allow list: canonicalProbeTool MUST NOT appear in
// canonicalAllow. A future code change that appends the probe-tool name
// to canonicalAllow would make PASS structurally unreachable (the
// allow-list mechanism would permit the probe tool) without any
// compile-time signal. This converts the doc-comment convention to a
// deterministic-fail check.
func TestProbeToolIsNotInAllowList(t *testing.T) {
	if slices.Contains(canonicalAllow, canonicalProbeTool) {
		t.Fatalf("canonicalProbeTool %q must NOT be in canonicalAllow %v — invariant violation",
			canonicalProbeTool, canonicalAllow)
	}
}

func TestSelfCheck_Timeout(t *testing.T) {
	installSeams(t)
	streamRun = func(ctx context.Context, cfg streamrunner.Config) error {
		// Block past the cfg.OverallTimeout. Real streamrunner.Run collapses
		// ctx-cancel to nil — mirror that contract.
		<-ctx.Done()
		return nil
	}

	cfg := baseConfig(t)
	cfg.OverallTimeout = 300 * time.Millisecond
	result, err := SelfCheckDenyDefault(context.Background(), cfg)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout\nresult=%+v", err, result)
	}
	if result.EndOfTurnObserved {
		t.Errorf("EndOfTurnObserved = true, want false")
	}
	if result.SentinelWritten {
		t.Errorf("SentinelWritten = true, want false")
	}
}

// TestSelfCheck_MalformedAssistantLineSkipped pins the resilience
// contract inherited from jsonl.Reader: one malformed line in the stream
// is logged + skipped, does not poison subsequent events, and does not
// turn a PASS into an inconclusive.
func TestSelfCheck_MalformedAssistantLineSkipped(t *testing.T) {
	installSeams(t)
	streamRun = func(ctx context.Context, cfg streamrunner.Config) error {
		if _, err := io.WriteString(cfg.Stdout, "{not valid json\n"+passLine+"\n"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}

	result, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if err != nil {
		t.Fatalf("SelfCheckDenyDefault: unexpected error: %v\nresult=%+v", err, result)
	}
	if !result.EndOfTurnObserved {
		t.Errorf("EndOfTurnObserved = false, want true (the valid line should have surfaced)")
	}
	if result.SentinelWritten {
		t.Errorf("SentinelWritten = true, want false")
	}
}

func TestSelfCheck_ConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Config)
		wantInErr string
	}{
		{
			name:      "empty ClaudeBin",
			mutate:    func(c *Config) { c.ClaudeBin = "" },
			wantInErr: "empty ClaudeBin",
		},
		{
			name:      "empty WorkDir",
			mutate:    func(c *Config) { c.WorkDir = "" },
			wantInErr: "empty WorkDir",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			installSeams(t)
			cfg := Config{
				ClaudeBin: "/bin/true",
				WorkDir:   t.TempDir(),
			}
			tc.mutate(&cfg)
			_, err := SelfCheckDenyDefault(context.Background(), cfg)
			if err == nil {
				t.Fatalf("SelfCheckDenyDefault: nil error, want one containing %q", tc.wantInErr)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantInErr)
			}
		})
	}
}

func TestSelfCheck_TrustMarkFailure(t *testing.T) {
	installSeams(t)
	trustMark = func(workdir string) (string, error) {
		return "", errors.New("trust write failed")
	}

	_, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if err == nil {
		t.Fatal("SelfCheckDenyDefault: nil error, want trust-mark failure")
	}
	if !strings.Contains(err.Error(), "mark workdir trusted") {
		t.Errorf("err = %q, want substring %q", err.Error(), "mark workdir trusted")
	}
	if !strings.Contains(err.Error(), "trust write failed") {
		t.Errorf("err = %q, want underlying error %q", err.Error(), "trust write failed")
	}
}

func TestSelfCheck_SettingsWriteFailure(t *testing.T) {
	installSeams(t)
	settingsWrite = func(allowed []string) (string, error) {
		return "", errors.New("settings write failed")
	}

	_, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if err == nil {
		t.Fatal("SelfCheckDenyDefault: nil error, want settings-write failure")
	}
	if !strings.Contains(err.Error(), "write settings") {
		t.Errorf("err = %q, want substring %q", err.Error(), "write settings")
	}
	if !strings.Contains(err.Error(), "settings write failed") {
		t.Errorf("err = %q, want underlying error %q", err.Error(), "settings write failed")
	}
}

// TestSelfCheck_SettingsCleanedOnLaterFailure pins the defer-ordering
// invariant: `defer os.Remove(settingsPath)` is registered AFTER settingsWrite
// succeeds and BEFORE the spawn, so any failure past the settings write still
// cleans up the tempfile.
//
// The forced failure used to be a session-id mint, which was the only step
// between the two. That seam went with the terminal runner, so the spawn itself
// is now the nearest later step and stands in for it.
func TestSelfCheck_SettingsCleanedOnLaterFailure(t *testing.T) {
	installSeams(t)

	var observedPath string
	settingsWrite = func(allowed []string) (string, error) {
		f, err := os.CreateTemp(t.TempDir(), "test-settings-*.json")
		if err != nil {
			return "", err
		}
		_ = f.Close()
		observedPath = f.Name()
		return observedPath, nil
	}
	streamRun = func(ctx context.Context, cfg streamrunner.Config) error {
		return errors.New("forced spawn failure")
	}

	_, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if err == nil {
		t.Fatal("SelfCheckDenyDefault: nil error, want spawn failure")
	}
	if observedPath == "" {
		t.Fatal("settingsWrite mock never recorded a path; cannot assert cleanup")
	}
	if _, statErr := os.Stat(observedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("settings tempfile %q not cleaned up; stat err = %v", observedPath, statErr)
	}
}

// argValue returns the value following flag in a claude argv, or "" if absent.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestSelfCheck_PassesCanonicalAllowToSpawn replaces the field-level assertion
// that the terminal runner allowed. Under the stream runner the allowlist and
// the turn budget are argv, not struct fields, so this reads them back off the
// argv the check actually spawns with.
//
// It is the regression guard for bug #526, where a required input was added to
// the runner and the self-check's own call site was not updated, so the check
// silently spawned with a broken configuration. Nothing else pins it: the mock
// accepts any argv.
func TestSelfCheck_PassesCanonicalAllowToSpawn(t *testing.T) {
	installSeams(t)

	var observed []string
	streamRun = func(ctx context.Context, cfg streamrunner.Config) error {
		observed = append([]string(nil), cfg.Args...)
		// Emit a clean end-of-turn so the check completes normally; this test
		// is about the argv, not the verdict.
		if _, err := io.WriteString(cfg.Stdout, passLine+"\n"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}

	if _, err := SelfCheckDenyDefault(context.Background(), baseConfig(t)); err != nil {
		t.Fatalf("SelfCheckDenyDefault: %v", err)
	}
	if len(observed) == 0 {
		t.Fatal("streamRun never invoked, or spawned with an empty argv")
	}

	if got, want := argValue(observed, "--allowed-tools"), strings.Join(canonicalAllow, ","); got != want {
		t.Errorf("--allowed-tools = %q, want %q (canonicalAllow)", got, want)
	}
	// The settings file is the actual boundary this check exists to verify, so
	// a spawn without it would make PASS meaningless rather than merely weaker.
	if argValue(observed, "--settings") == "" {
		t.Error("--settings absent from the argv; the deny-default file IS the enforcement (#1387), so the check would pass vacuously")
	}
	// >= 2 so claude's runtime reaches the execute-or-deny step: turn 1 emits
	// the tool use, the runtime denies between turns, turn 2 acknowledges.
	turns, err := strconv.Atoi(argValue(observed, "--max-turns"))
	if err != nil {
		t.Fatalf("--max-turns not an integer in argv %v: %v", observed, err)
	}
	if turns < 2 {
		t.Errorf("--max-turns = %d, want >= 2 (must reach the execute-or-deny step)", turns)
	}
	// The mode that makes an unlisted tool a hard deny rather than a prompt.
	if got := argValue(observed, "--permission-mode"); got != "dontAsk" {
		t.Errorf("--permission-mode = %q, want \"dontAsk\"", got)
	}
}

// TestSelfCheck_SpawnError pins that a spawn failure propagates rather than
// being swallowed into an inconclusive result.
func TestSelfCheck_SpawnError(t *testing.T) {
	installSeams(t)

	sentinel := errors.New("spawn exploded")
	streamRun = func(ctx context.Context, cfg streamrunner.Config) error {
		return sentinel
	}

	_, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if !errors.Is(err, sentinel) {
		t.Errorf("SelfCheckDenyDefault err = %v, want it to wrap the spawn error", err)
	}
}
