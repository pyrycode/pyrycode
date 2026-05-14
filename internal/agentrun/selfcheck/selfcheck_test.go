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

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// Canned JSONL fixtures used across the self-check tests. Kept on a
// single line each so the fake-claude writer can dump them verbatim into
// the session JSONL file the watcher tails.
const (
	// passLine: assistant entry with stop_reason "end_turn" and a single
	// text content block. Satisfies the deterministic end-of-turn rule
	// (stop_reason "end_turn" AND sum text length > 0). No tool_use
	// blocks means the bash detector sees nothing.
	passLine = `{"type":"assistant","message":{"id":"msg_pass","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	// bashLine: assistant entry whose content carries a tool_use block
	// with name "Bash". The detector matches on this; stop_reason
	// "tool_use" here mirrors what claude emits when it actually picks a
	// tool.
	bashLine = `{"type":"assistant","message":{"id":"msg_bash","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"echo hello"}}],"usage":{"input_tokens":5,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
)

// TestSelfCheckHelperProcess is the fake-claude entry point. The test
// binary re-execs itself with GO_SELF_CHECK_HELPER=1 (set in the parent
// test's Config.Env, propagated through SpawnPTY into the wrapper script
// and on into the re-exec'd test binary). Reads:
//
//   - GO_SELF_CHECK_HELPER_SESSION_ID: the --session-id value extracted
//     from the production argv by the wrapper script.
//   - GO_SELF_CHECK_HELPER_JSONL: verbatim bytes to write into
//     $HOME/.claude/projects/<encoded-cwd>/<sid>.jsonl. Empty skips the
//     write (used by the timeout fixture).
//   - GO_SELF_CHECK_HELPER_LIFETIME: time.Duration the fake stays alive
//     after writing. Defaults to 1s.
func TestSelfCheckHelperProcess(t *testing.T) {
	if os.Getenv("GO_SELF_CHECK_HELPER") != "1" {
		return
	}

	sid := os.Getenv("GO_SELF_CHECK_HELPER_SESSION_ID")
	if sid == "" {
		fmt.Fprintln(os.Stderr, "fake: missing GO_SELF_CHECK_HELPER_SESSION_ID")
		os.Exit(99)
	}

	if content := os.Getenv("GO_SELF_CHECK_HELPER_JSONL"); content != "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake: getwd: %v\n", err)
			os.Exit(98)
		}
		encoded, err := agentrun.EncodeProjectDir(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake: encode: %v\n", err)
			os.Exit(98)
		}
		home := os.Getenv("HOME")
		if home == "" {
			fmt.Fprintln(os.Stderr, "fake: empty HOME")
			os.Exit(98)
		}
		dir := filepath.Join(home, ".claude", "projects", encoded)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "fake: mkdir: %v\n", err)
			os.Exit(97)
		}
		path := filepath.Join(dir, sid+".jsonl")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "fake: write: %v\n", err)
			os.Exit(96)
		}
	}

	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
	lifetime := 1 * time.Second
	if raw := os.Getenv("GO_SELF_CHECK_HELPER_LIFETIME"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			lifetime = d
		}
	}
	time.Sleep(lifetime)
	os.Exit(0)
}

// selfCheckHelperWrapper writes a /bin/sh wrapper that drops the
// production claude argv (the Go test binary's flag parser would reject
// it), extracts --session-id, re-exports it as
// GO_SELF_CHECK_HELPER_SESSION_ID, and exec's the test binary in
// fake-claude mode. Mirrors the pattern in cmd/pyry/agent_run_test.go's
// configureFakeClaude.
func selfCheckHelperWrapper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude.sh")
	body := fmt.Sprintf(`#!/bin/sh
SID=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session-id) SID="$2"; shift 2 ;;
    *) shift ;;
  esac
done
export GO_SELF_CHECK_HELPER_SESSION_ID="$SID"
exec %q -test.run=^TestSelfCheckHelperProcess$
`, os.Args[0])
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	return path
}

// selfCheckSetup builds a homeDir + workdir pair with the canonical
// deny-default settings file already in place. Returns a partially-filled
// Config the caller customises (Env, OverallTimeout). t.Setenv pins HOME
// so the spawned child sees the same home as the parent.
func selfCheckSetup(t *testing.T) (homeDir, workdir string, cfg Config) {
	t.Helper()
	homeDir = t.TempDir()
	t.Setenv("HOME", homeDir)
	workdir = t.TempDir()
	if _, err := agentrun.WriteSettings(workdir, []string{"Read"}); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	cfg = Config{
		ClaudeBin:        selfCheckHelperWrapper(t),
		HomeDir:          homeDir,
		Workdir:          workdir,
		TrustDialogDelay: 20 * time.Millisecond,
		PromptDelay:      20 * time.Millisecond,
		OverallTimeout:   5 * time.Second,
	}
	return homeDir, workdir, cfg
}

func TestSelfCheck_Pass(t *testing.T) {
	_, _, cfg := selfCheckSetup(t)
	cfg.Env = []string{
		"GO_SELF_CHECK_HELPER=1",
		"GO_SELF_CHECK_HELPER_JSONL=" + passLine + "\n",
		"GO_SELF_CHECK_HELPER_LIFETIME=500ms",
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
	_, _, cfg := selfCheckSetup(t)
	cfg.Env = []string{
		"GO_SELF_CHECK_HELPER=1",
		// Two-line fixture: Bash tool_use first, end_turn second. The
		// detector must trip on the first line; the second is present so
		// a regression where the detector misses Bash and falls through
		// to end-of-turn would surface as PASS, not a hang.
		"GO_SELF_CHECK_HELPER_JSONL=" + bashLine + "\n" + passLine + "\n",
		"GO_SELF_CHECK_HELPER_LIFETIME=500ms",
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

// TestSelfCheck_BashInvokedUnderMisformattedSettings is the AC's
// "runtime enforcement, not file presence" verification. The settings
// file written to the workdir uses defaultMode "DENY" (uppercase) — a
// shape that, were the detector to inspect the file content, might be
// flagged as "deny is present". The detector is structural over JSONL:
// it sees the Bash tool_use the fake emits and still reports
// ErrBashInvoked.
func TestSelfCheck_BashInvokedUnderMisformattedSettings(t *testing.T) {
	_, workdir, cfg := selfCheckSetup(t)
	settingsPath := filepath.Join(workdir, agentrun.SettingsFilename)
	bogus := `{"permissions":{"allow":["Read"],"defaultMode":"DENY"}}` + "\n"
	if err := os.WriteFile(settingsPath, []byte(bogus), 0o600); err != nil {
		t.Fatalf("write bogus settings: %v", err)
	}

	cfg.Env = []string{
		"GO_SELF_CHECK_HELPER=1",
		"GO_SELF_CHECK_HELPER_JSONL=" + bashLine + "\n",
		"GO_SELF_CHECK_HELPER_LIFETIME=500ms",
	}

	result, err := SelfCheckDenyDefault(context.Background(), cfg)
	if !errors.Is(err, ErrBashInvoked) {
		t.Fatalf("err = %v, want ErrBashInvoked\nresult=%+v", err, result)
	}
	if !result.BashInvoked {
		t.Errorf("BashInvoked = false, want true")
	}
	if result.Evidence == nil {
		t.Errorf("Evidence is nil, want the bash line")
	}
}

func TestSelfCheck_Timeout(t *testing.T) {
	_, _, cfg := selfCheckSetup(t)
	cfg.OverallTimeout = 300 * time.Millisecond
	cfg.Env = []string{
		"GO_SELF_CHECK_HELPER=1",
		// No JSONL output. Fake stays alive past the OverallTimeout so
		// the watcher never sees an end_turn nor a Bash invocation.
		"GO_SELF_CHECK_HELPER_LIFETIME=2s",
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
	_, _, cfg := selfCheckSetup(t)
	cfg.Env = []string{
		"GO_SELF_CHECK_HELPER=1",
		"GO_SELF_CHECK_HELPER_JSONL=" + "{not valid json\n" + passLine + "\n",
		"GO_SELF_CHECK_HELPER_LIFETIME=500ms",
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
			name:      "empty HomeDir",
			mutate:    func(c *Config) { c.HomeDir = "" },
			wantInErr: "empty HomeDir",
		},
		{
			name:      "empty Workdir",
			mutate:    func(c *Config) { c.Workdir = "" },
			wantInErr: "empty Workdir",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				ClaudeBin: "/bin/true",
				HomeDir:   t.TempDir(),
				Workdir:   t.TempDir(),
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
