package selfcheck

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/ptyrunner"
)

// Canned stream-json fixtures used across the self-check tests. Each is a
// single JSONL line (no trailing newline); the ptyRun mock writes these to
// cfg.Stdout with newline separators.
const (
	// passLine: assistant entry with stop_reason "end_turn" and a single
	// text content block. Satisfies jsonl.Reader's deterministic end-of-
	// turn rule (stop_reason "end_turn" AND sum of text > 0). No tool_use
	// blocks means the probe-tool detector sees nothing.
	passLine = `{"type":"assistant","message":{"id":"msg_pass","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	// writeLine: assistant entry whose content carries a tool_use block
	// with name "Write". The detector matches on this; stop_reason
	// "tool_use" here mirrors what claude emits when it actually picks a
	// tool.
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
	origSession := newSessionID
	origPty := ptyRun
	t.Cleanup(func() {
		trustMark = origTrust
		settingsWrite = origSettings
		newSessionID = origSession
		ptyRun = origPty
	})
	// Default benign overrides; per-test bodies replace the ones they care
	// about. Ensures no test accidentally hits ~/.claude.json,
	// os.TempDir(), or the real ptyrunner.
	trustMark = func(workdir string) (string, error) { return workdir, nil }
	settingsWrite = func(allowed []string) (string, error) { return "/tmp/test-settings.json", nil }
	newSessionID = func() (string, error) { return "00000000-0000-4000-8000-000000000000", nil }
	ptyRun = func(ctx context.Context, cfg ptyrunner.Config) error {
		t.Errorf("ptyRun unexpectedly invoked; test should override it")
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
	ptyRun = func(ctx context.Context, cfg ptyrunner.Config) error {
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
	if result.ProbeToolInvoked {
		t.Errorf("ProbeToolInvoked = true, want false")
	}
	if !result.EndOfTurnObserved {
		t.Errorf("EndOfTurnObserved = false, want true")
	}
	if result.AssistantCount != 1 {
		t.Errorf("AssistantCount = %d, want 1", result.AssistantCount)
	}
	if result.Evidence != nil {
		t.Errorf("Evidence = %q, want nil", result.Evidence)
	}
}

// TestSelfCheck_PassesCanonicalAllowToPtyRunner pins the call-site contract
// that SelfCheckDenyDefault populates ptyrunner.Config.AllowedTools with the
// canonicalAllow constant. Regression net for the silent-drift pattern that
// produced bug #526: a required field was added to ptyrunner.Config (the
// runner.go:245 nil-check), but the selfcheck's Config literal was not
// updated, and no existing test in this package exercises the real Config
// contract — the ptyRun mock accepts anything.
func TestSelfCheck_PassesCanonicalAllowToPtyRunner(t *testing.T) {
	installSeams(t)
	var observedAllow []string
	ptyRun = func(ctx context.Context, cfg ptyrunner.Config) error {
		observedAllow = cfg.AllowedTools
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
		t.Fatalf("SelfCheckDenyDefault: unexpected error: %v", err)
	}

	if observedAllow == nil {
		t.Fatal("ptyrunner.Config.AllowedTools is nil; runner.go:245 nil-check would reject this Config")
	}
	if !reflect.DeepEqual(observedAllow, canonicalAllow) {
		t.Errorf("ptyrunner.Config.AllowedTools = %v, want %v (canonicalAllow)", observedAllow, canonicalAllow)
	}
}

func TestSelfCheck_ProbeToolInvoked(t *testing.T) {
	installSeams(t)
	ptyRun = func(ctx context.Context, cfg ptyrunner.Config) error {
		// Two-line fixture: probe-tool tool_use first, end_turn second.
		// The detector must trip on the first line; the second is
		// present so a regression where the detector misses the probe
		// tool and falls through to end-of-turn would surface as PASS,
		// not a hang.
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
	if !errors.Is(err, ErrProbeToolInvoked) {
		t.Fatalf("err = %v, want ErrProbeToolInvoked\nresult=%+v", err, result)
	}
	if !result.ProbeToolInvoked {
		t.Errorf("ProbeToolInvoked = false, want true")
	}
	if result.Evidence == nil {
		t.Fatalf("Evidence is nil, want the write line")
	}
	if !strings.Contains(string(result.Evidence), `"name":"Write"`) {
		t.Errorf("Evidence does not contain `\"name\":\"Write\"`: %q", result.Evidence)
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
	ptyRun = func(ctx context.Context, cfg ptyrunner.Config) error {
		// Block past the cfg.OverallTimeout. Real ptyrunner.Run collapses
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
	if result.ProbeToolInvoked {
		t.Errorf("ProbeToolInvoked = true, want false")
	}
}

// TestSelfCheck_MalformedAssistantLineSkipped pins the resilience
// contract inherited from jsonl.Reader: one malformed line in the stream
// is logged + skipped, does not poison subsequent events, and does not
// turn a PASS into an inconclusive.
func TestSelfCheck_MalformedAssistantLineSkipped(t *testing.T) {
	installSeams(t)
	ptyRun = func(ctx context.Context, cfg ptyrunner.Config) error {
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
	if result.ProbeToolInvoked {
		t.Errorf("ProbeToolInvoked = true, want false")
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

func TestSelfCheck_SessionIDFailure(t *testing.T) {
	installSeams(t)
	newSessionID = func() (string, error) {
		return "", errors.New("session id failed")
	}

	_, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if err == nil {
		t.Fatal("SelfCheckDenyDefault: nil error, want session-id failure")
	}
	if !strings.Contains(err.Error(), "mint session id") {
		t.Errorf("err = %q, want substring %q", err.Error(), "mint session id")
	}
	if !strings.Contains(err.Error(), "session id failed") {
		t.Errorf("err = %q, want underlying error %q", err.Error(), "session id failed")
	}
}

// TestSelfCheck_SettingsCleanedOnLaterFailure pins the defer-ordering
// invariant: `defer os.Remove(settingsPath)` is registered AFTER
// settingsWrite succeeds and BEFORE newSessionID is called, so any
// failure past the settings write still cleans up the tempfile.
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
	newSessionID = func() (string, error) {
		return "", errors.New("forced session-id failure")
	}

	_, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if err == nil {
		t.Fatal("SelfCheckDenyDefault: nil error, want session-id failure")
	}
	if observedPath == "" {
		t.Fatal("settingsWrite mock never recorded a path; cannot assert cleanup")
	}
	if _, statErr := os.Stat(observedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("settings tempfile %q not cleaned up; stat err = %v", observedPath, statErr)
	}
}

func TestSelfCheck_PtyRunnerError(t *testing.T) {
	installSeams(t)
	ptyRun = func(ctx context.Context, cfg ptyrunner.Config) error {
		return ptyrunner.ErrTrustModalDetected
	}

	result, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if !errors.Is(err, ptyrunner.ErrTrustModalDetected) {
		t.Fatalf("err = %v, want errors.Is(err, ptyrunner.ErrTrustModalDetected)\nresult=%+v", err, result)
	}
}

func TestProbeToolInvokedInRaw(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{
			name: "Write tool_use",
			raw:  writeLine,
			want: true,
		},
		{
			name: "Read tool_use is not Write",
			raw:  `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`,
			want: false,
		},
		{
			name: "text only",
			raw:  passLine,
			want: false,
		},
		{
			name: "lowercase write does not match",
			raw:  `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"write","input":{}}]}}`,
			want: false,
		},
		{
			name: "tool_use without name field",
			raw:  `{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}`,
			want: false,
		},
		{
			name:    "invalid json surfaces decode error",
			raw:     `{not json`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := probeToolInvokedInRaw([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("probeToolInvokedInRaw: nil error, want decode error")
				}
				return
			}
			if err != nil {
				t.Fatalf("probeToolInvokedInRaw: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("probeToolInvokedInRaw = %v, want %v", got, tc.want)
			}
		})
	}
}
