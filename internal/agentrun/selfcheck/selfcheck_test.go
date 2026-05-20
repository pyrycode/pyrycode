package selfcheck

import (
	"context"
	"errors"
	"io"
	"os"
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
	// blocks means the Bash detector sees nothing.
	passLine = `{"type":"assistant","message":{"id":"msg_pass","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	// bashLine: assistant entry whose content carries a tool_use block
	// with name "Bash". The detector matches on this; stop_reason
	// "tool_use" here mirrors what claude emits when it actually picks a
	// tool.
	bashLine = `{"type":"assistant","message":{"id":"msg_bash","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"echo hello"}}],"usage":{"input_tokens":5,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
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
	if result.BashInvoked {
		t.Errorf("BashInvoked = true, want false")
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

func TestSelfCheck_BashInvoked(t *testing.T) {
	installSeams(t)
	ptyRun = func(ctx context.Context, cfg ptyrunner.Config) error {
		// Two-line fixture: Bash tool_use first, end_turn second. The
		// detector must trip on the first line; the second is present so
		// a regression where the detector misses Bash and falls through
		// to end-of-turn would surface as PASS, not a hang.
		if _, err := io.WriteString(cfg.Stdout, bashLine+"\n"+passLine+"\n"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}

	result, err := SelfCheckDenyDefault(context.Background(), baseConfig(t))
	if !errors.Is(err, ErrBashInvoked) {
		t.Fatalf("err = %v, want ErrBashInvoked\nresult=%+v", err, result)
	}
	if !result.BashInvoked {
		t.Errorf("BashInvoked = false, want true")
	}
	if result.Evidence == nil {
		t.Fatalf("Evidence is nil, want the bash line")
	}
	if !strings.Contains(string(result.Evidence), `"name":"Bash"`) {
		t.Errorf("Evidence does not contain `\"name\":\"Bash\"`: %q", result.Evidence)
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
	if result.BashInvoked {
		t.Errorf("BashInvoked = true, want false")
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
	if result.BashInvoked {
		t.Errorf("BashInvoked = true, want false")
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

func TestBashInvokedInRaw(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{
			name: "Bash tool_use",
			raw:  bashLine,
			want: true,
		},
		{
			name: "Read tool_use is not Bash",
			raw:  `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`,
			want: false,
		},
		{
			name: "text only",
			raw:  passLine,
			want: false,
		},
		{
			name: "lowercase bash does not match",
			raw:  `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"bash","input":{}}]}}`,
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
			got, err := bashInvokedInRaw([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("bashInvokedInRaw: nil error, want decode error")
				}
				return
			}
			if err != nil {
				t.Fatalf("bashInvokedInRaw: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("bashInvokedInRaw = %v, want %v", got, tc.want)
			}
		})
	}
}
