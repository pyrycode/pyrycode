//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSuggestCLIProducerIsolation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		native bool
		stream bool
		want   string
	}{
		{"native rejects fallback", true, false, ""},
		{"native explicitly enables", true, true, "1|true"},
		{"fallback stream disables", false, true, "0|false"},
		{"fallback one-shot allowed", false, false, "0|true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fake := `#!/usr/bin/python3
import os, sys
print(os.environ.get("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION", "") + "|" + str("--prompt-suggestions" in sys.argv).lower())
`
			if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION", "0")
			installSuggestCLI(t, tc.native)
			args := []string{"--prompt-suggestions"}
			if tc.stream {
				args = append(args, "--input-format", "stream-json")
			}
			out, err := exec.Command("claude", args...).Output()
			if tc.want == "" {
				if err == nil || len(out) != 0 {
					t.Fatal("native wrapper allowed non-stream launch")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("producer setup = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSuggestCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_SUGGEST_HELPER") != "1" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || string(input) != "private-input-sentinel" {
		os.Exit(29)
	}
	out, err := base64.StdEncoding.DecodeString(os.Getenv("GO_SUGGEST_OUTPUT"))
	if err != nil {
		os.Exit(30)
	}
	_, _ = os.Stderr.WriteString("private-stderr-sentinel")
	_, _ = os.Stdout.Write(out)
	if os.Getenv("GO_SUGGEST_HOLD") == "true" {
		if err := os.WriteFile(os.Getenv("GO_SUGGEST_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(31)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	code, _ := strconv.Atoi(os.Getenv("GO_SUGGEST_EXIT"))
	if code < 0 {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(code)
}

func installSuggestStandIn(t *testing.T, output string, exit int, hold bool) string {
	t.Helper()
	dir := t.TempDir()
	// Re-exec only the helper test; CLI arguments remain literal positional data.
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run '^TestSuggestCLIHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_SUGGEST_HELPER", "1")
	t.Setenv("GO_SUGGEST_OUTPUT", base64.StdEncoding.EncodeToString([]byte(output)))
	t.Setenv("GO_SUGGEST_EXIT", strconv.Itoa(exit))
	t.Setenv("GO_SUGGEST_HOLD", strconv.FormatBool(hold))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "private-credential-sentinel")
	t.Setenv("UNRELATED_PRIVATE_VALUE", "private-environment-sentinel")
	return installSuggestCLI(t, false)
}

func checkSuggestPrivacy(t *testing.T, evidence string, source suggestSource) {
	t.Helper()
	blob, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := source.diagnostic(false, 0, 0)
	if bytes.Contains(blob, []byte("private-")) || strings.Contains(diagnostic, "private-") {
		t.Fatal("evidence or diagnostic retained sensitive input/output/environment")
	}
	info, err := os.Stat(evidence)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("source evidence must be private")
	}
}

func TestSuggestCLIFallbackEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, output, stage string
		exit                int
	}{
		{"success", `{"result":"private-generated-text","subtype":"success","is_error":false}`, "usable output without wire set", 0},
		{"nonzero exit", `{"result":"private-generated-text"}`, "unsuccessful invocation", 7},
		{"signal exit", `{"result":"private-generated-text"}`, "unsuccessful invocation", -1},
		{"invalid utf8", "\xffprivate-generated-text", "unusable output", 0},
		{"malformed json", "private-raw-stdout", "unusable output", 0},
		{"error result", `{"result":"private-generated-text","is_error":true}`, "unusable output", 0},
		{"wrong subtype", `{"result":"private-generated-text","subtype":"private-subtype"}`, "unusable output", 0},
		{"wrong field type", `{"result":7}`, "unusable output", 0},
		{"wrong subtype type", `{"result":"private-generated-text","subtype":false}`, "unusable output", 0},
		{"duplicate field type", `{"result":7,"result":"private-generated-text"}`, "unusable output", 0},
		{"null preserves result", `{"result":"private-generated-text","result":null}`, "usable output without wire set", 0},
		{"null preserves error", `{"result":"private-generated-text","is_error":true,"is_error":null}`, "unusable output", 0},
		{"folded error field", `{"result":"private-generated-text","iſ_error":true}`, "unusable output", 0},
		{"empty", `{"result":"  "}`, "unusable output", 0},
		{"multiline", `{"result":"private-generated-text\nnext"}`, "unusable output", 0},
		{"separator", `{"result":"private-generated-text\u2028next"}`, "unusable output", 0},
		{"too many runes", `{"result":"` + strings.Repeat("x", 241) + `"}`, "unusable output", 0},
		{"control whitespace", `{"result":"\u001cprivate-generated-text"}`, "unusable output", 0},
		{"stdout cap", strings.Repeat(" ", 4097) + `{"result":"ok"}`, "unusable output", 0},
		{"trim and unicode", `{"result":" \t` + strings.Repeat("界", 240) + `\n"}`, "usable output without wire set", 0},
		{"escaped surrogate", `{"result":"private-generated-text\ud800"}`, "usable output without wire set", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := installSuggestStandIn(t, tc.output, tc.exit, false)
			cmd := exec.Command("claude", "--print", "--output-format", "json")
			cmd.Stdin = strings.NewReader("private-input-sentinel")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if (err == nil) != (tc.exit == 0) || stdout.String() != tc.output || stderr.String() != "private-stderr-sentinel" {
				t.Fatal("observer changed child input/output/exit behavior")
			}
			if tc.exit > 0 && cmd.ProcessState.ExitCode() != tc.exit || tc.exit < 0 && cmd.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
				t.Fatal("observer changed exit status or signal")
			}
			source := readSuggestSource(t, evidence)
			if source.Calls != 1 || source.Completed != 1 || source.PID != cmd.Process.Pid || source.StartMS == 0 || source.ElapsedMS < 0 || source.ExitCode == nil || !source.OutputObserved || source.stage(false) != tc.stage {
				t.Fatalf("unexpected invocation evidence: %s", source.diagnostic(false, 0, 0))
			}
			wantExit := tc.exit
			if wantExit < 0 {
				wantExit = -int(syscall.SIGTERM)
			}
			if *source.ExitCode != wantExit || source.StdoutBytes != len(tc.output) {
				t.Fatal("exit or stdout byte count does not match the observed child")
			}
			if tc.stage != "unusable output" && (!source.UTF8OK || !source.JSONOK || !source.ResultOK || !source.TextOK) {
				t.Fatal("valid returned output lacks production validation evidence")
			}
			checkSuggestPrivacy(t, evidence, source)
		})
	}
}

func TestSuggestSourceUncertainEvidence(t *testing.T) {
	const fields = "pid=42 attempt_ms=9810 child_ms=9800 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=true own_deadline_elapsed=true group_cancel_requested=true wait_completed=true exit_observed=true exit_code=-1 exit_signal=9"
	const prefix = "time=private-time level=INFO msg=reply_fallback.lifecycle "
	for _, tc := range []struct {
		stderr string
		pid    int
		known  bool
	}{
		{prefix + fields + " private=private-secret-sentinel\n", 42, true},
		{prefix + fields + "\n", 43, false},
		{prefix + strings.Replace(fields, "exit_code=-1", "exit_code=private-output", 1) + "\n", 42, false},
		{prefix + fields, 42, false}, // In-progress stderr record.
		{"private-raw-stderr\n", 42, false},
		{prefix + strings.Replace(fields, "exit_observed=true", "exit_observed=false", 1) + "\n", 42, true},
		{prefix + strings.Replace(fields, "parent_deadline=false", "parent_deadline=true", 1) + "\n", 42, true}, // Simultaneous deadlines remain visible.
	} {
		got := suggestLifecycle(tc.stderr, tc.pid)
		if strings.Contains(got, "private-") || strings.Contains(got, "unknown") == tc.known {
			t.Fatal("unsafe or incorrectly classified daemon record")
		}
		if tc.known && (!strings.Contains(got, "fallback_deadline=true") || !strings.Contains(got, "group_cancel_requested=true")) {
			t.Fatal("lost lifecycle observations")
		}
	}
	for _, tc := range []struct {
		source suggestSource
		want   string
	}{
		{suggestSource{Calls: 2, Completed: 2}, "unknown (ambiguous invocation evidence)"},
		{suggestSource{Calls: 1, Completed: 1}, "unknown (no observed child exit)"},
	} {
		if got := tc.source.stage(false); got != tc.want {
			t.Fatalf("stage = %q, want %q", got, tc.want)
		}
	}
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte("{\"streams\":1}\n{\"calls\":1,\"start_ms\":1}\n{\"completed\":"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readSuggestSource(t, path); got != (suggestSource{Streams: 1, Calls: 1, StartMS: 1}) {
		t.Fatal("partial append lost completed source metadata or invented completion")
	}
}

func TestSuggestCLIFallbackIncomplete(t *testing.T) {
	evidence := installSuggestStandIn(t, "private-partial-output", 0, true)
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("GO_SUGGEST_READY", ready)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "--print")
	cmd.Stdin = strings.NewReader("private-input-sentinel")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stdout lockedBuffer
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		if cmd.ProcessState == nil {
			_ = cmd.Wait() // Reap the process after an early failure too.
		}
	}()
	// Child readiness precedes wrapper forwarding. Wait for the forwarded bytes
	// in the synchronized capture before cancellation; both output pipes must
	// then close, otherwise Wait blocks until the context catches the leak.
	for {
		if _, err := os.Stat(ready); err == nil && len(stdout.String()) >= len("private-partial-output") {
			break
		}
		if ctx.Err() != nil {
			_ = cmd.Wait()
			t.Fatal("stand-in did not reach held invocation with forwarded output")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil || ctx.Err() != nil || stdout.String() != "private-partial-output" || stderr.String() != "private-stderr-sentinel" {
		t.Fatal("group termination failed or observer changed partial I/O")
	}
	source := readSuggestSource(t, evidence)
	if source.Calls != 1 || source.Completed != 0 || source.PID != cmd.Process.Pid || source.ExitCode != nil || source.OutputObserved || source.stage(false) != "incomplete invocation (exit/output unknown)" {
		t.Fatalf("unexpected incomplete evidence: %s", source.diagnostic(false, 0, 0))
	}
	checkSuggestPrivacy(t, evidence, source)
	if stage := (suggestSource{}).stage(false); stage != "no observed invocation (cause unknown)" {
		t.Fatal("missing invocation overstates cause")
	}
}

func TestSuggestCLISourceEvidence(t *testing.T) {
	dir := t.TempDir()
	// The stand-in proves only wrapper transparency and redaction. It never
	// participates in either live set/clear test.
	stdout := "{\"type\":\"assistant\",\"message\":{\"content\":\"private-generated-text\"}}\n" +
		"{\"type\":\"result\",\"result\":\"private-generated-text\"}\n" +
		"{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"allowed_warning\"}}\n" +
		"{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"private-status\"}}\n" +
		"{\"type\":\"prompt_suggestion\",\"suggestion\":\"private-generated-text\"}\n" +
		"{\"type\":\"prompt_suggestion\",\"suggestion\":null}\n" +
		"{\"type\":\"prompt_suggestion\",\"suggestion\":\"  \"}\n" +
		"private-malformed-frame\n"
	fake := "#!/usr/bin/python3\nimport sys\nsys.stdout.write(" + strconv.Quote(stdout) + ")\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "private-credential-sentinel")
	evidence := installSuggestCLI(t, true)
	out, err := exec.Command("claude", "--input-format", "stream-json", "--prompt-suggestions").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, []byte(stdout)) {
		t.Fatal("source observer changed stdout")
	}
	want := suggestSource{Streams: 1, Results: 1, Events: 3, Suggestions: 1, Bytes: len("private-generated-text"), Warnings: 1}
	if got := readSuggestSource(t, evidence); got != want {
		t.Fatalf("source metadata = %+v, want %+v", got, want)
	}
	blob, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("private-")) {
		t.Fatal("source evidence retained sensitive data")
	}
	info, err := os.Stat(evidence)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("source evidence must be private")
	}
}
