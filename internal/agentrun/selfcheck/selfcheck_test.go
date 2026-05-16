package selfcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Canned stream-json fixtures used across the self-check tests. Each is a
// single JSONL line (no trailing newline); the fake-claude helper writes
// these to stdout with newline separators.
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

// TestSelfCheckHelperProcess is the fake-claude entry point. The test
// binary is re-exec'd via a shell wrapper that drops the production claude
// argv (which the Go test binary's flag parser would reject) and routes
// here keyed by GO_SELFCHECK_HELPER=1 in the env.
//
// Env knobs:
//
//   - GO_SELFCHECK_HELPER_STDOUT: verbatim bytes to write to stdout
//     (typically one or more JSONL lines). Empty skips the write.
//   - GO_SELFCHECK_HELPER_LIFETIME: time.Duration the fake stays alive
//     after writing. Defaults to 200ms. Used by the timeout fixture to
//     hold the child past cfg.OverallTimeout without producing output.
func TestSelfCheckHelperProcess(t *testing.T) {
	if os.Getenv("GO_SELFCHECK_HELPER") != "1" {
		return
	}
	// Drain stdin so streamrunner's stdin envelope write doesn't block.
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()

	if out := os.Getenv("GO_SELFCHECK_HELPER_STDOUT"); out != "" {
		if _, err := os.Stdout.WriteString(out); err != nil {
			fmt.Fprintf(os.Stderr, "fake: write stdout: %v\n", err)
			os.Exit(98)
		}
		_ = os.Stdout.Sync()
	}

	lifetime := 200 * time.Millisecond
	if raw := os.Getenv("GO_SELFCHECK_HELPER_LIFETIME"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			lifetime = d
		}
	}
	time.Sleep(lifetime)
	os.Exit(0)
}

// selfCheckHelperWrapper writes a /bin/sh wrapper that drops the
// production claude argv (the Go test binary's flag parser rejects unknown
// flags like --input-format) and exec's the test binary in fake-claude
// mode. Env knobs are inherited through the exec.
func selfCheckHelperWrapper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude.sh")
	body := fmt.Sprintf(`#!/bin/sh
exec %q -test.run=^TestSelfCheckHelperProcess$
`, os.Args[0])
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	return path
}

// selfCheckSetup returns a Config wired to the helper wrapper with a
// short OverallTimeout. The caller sets cfg.Env to thread the helper's
// behaviour knobs.
func selfCheckSetup(t *testing.T) Config {
	t.Helper()
	return Config{
		ClaudeBin:      selfCheckHelperWrapper(t),
		WorkDir:        t.TempDir(),
		OverallTimeout: 5 * time.Second,
	}
}

func TestSelfCheck_Pass(t *testing.T) {
	cfg := selfCheckSetup(t)
	cfg.Env = []string{
		"GO_SELFCHECK_HELPER=1",
		"GO_SELFCHECK_HELPER_STDOUT=" + passLine + "\n",
		"GO_SELFCHECK_HELPER_LIFETIME=200ms",
	}

	result, err := SelfCheckDenyDefault(context.Background(), cfg)
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
	cfg := selfCheckSetup(t)
	cfg.Env = []string{
		"GO_SELFCHECK_HELPER=1",
		// Two-line fixture: Bash tool_use first, end_turn second. The
		// detector must trip on the first line; the second is present so
		// a regression where the detector misses Bash and falls through
		// to end-of-turn would surface as PASS, not a hang.
		"GO_SELFCHECK_HELPER_STDOUT=" + bashLine + "\n" + passLine + "\n",
		"GO_SELFCHECK_HELPER_LIFETIME=200ms",
	}

	result, err := SelfCheckDenyDefault(context.Background(), cfg)
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
	cfg := selfCheckSetup(t)
	cfg.OverallTimeout = 300 * time.Millisecond
	cfg.Env = []string{
		"GO_SELFCHECK_HELPER=1",
		// No stdout output. Fake stays alive past OverallTimeout so the
		// watcher never sees an end_turn nor a Bash invocation before
		// the deadline fires.
		"GO_SELFCHECK_HELPER_LIFETIME=2s",
	}

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
	cfg := selfCheckSetup(t)
	cfg.Env = []string{
		"GO_SELFCHECK_HELPER=1",
		"GO_SELFCHECK_HELPER_STDOUT=" + "{not valid json\n" + passLine + "\n",
		"GO_SELFCHECK_HELPER_LIFETIME=200ms",
	}

	result, err := SelfCheckDenyDefault(context.Background(), cfg)
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
