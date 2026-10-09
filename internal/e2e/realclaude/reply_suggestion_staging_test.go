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
		{"zero output", "", "unusable output", 0},
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
			if !source.Started || source.ChildPID <= 0 || source.ReadKnown != (len(tc.output) > 0) || source.ForwardKnown != (len(tc.output) > 0) {
				t.Fatal("lost completed boundary witnesses")
			}
			if *source.ExitCode != wantExit || source.StdoutBytes != min(4097, len(tc.output)) {
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
		if strings.Contains(got, "private-") || (got == "daemon lifecycle: unknown") == tc.known {
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
	if err := os.WriteFile(path, []byte("{\"streams\":1}\n{\"calls\":1,\"pid\":42,\"start_ms\":1}\n{\"completed\":"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readSuggestSource(t, path); got != (suggestSource{Streams: 1, Calls: 1, PID: 42, StartMS: 1}) {
		t.Fatal("partial append lost completed source metadata or invented completion")
	}
}

func TestSuggestCLIFallbackIncomplete(t *testing.T) {
	for _, boundary := range []string{"spawn", "read", "forward"} {
		t.Run(boundary, func(t *testing.T) {
			output := "private-partial-output"
			if boundary == "spawn" {
				output = ""
			}
			evidence := installSuggestStandIn(t, output, 0, true)
			t.Setenv("GO_SUGGEST_READY", filepath.Join(t.TempDir(), "ready"))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "claude", "--print")
			cmd.Stdin = strings.NewReader("private-input-sentinel")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			var stdout lockedBuffer
			var stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if boundary == "read" {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				defer w.Close()
				// Fill the pipe before launch; the wrapper can read but cannot flush.
				fd := int(w.Fd())
				if err := syscall.SetNonblock(fd, true); err != nil {
					t.Fatal(err)
				}
				for {
					if _, err := syscall.Write(fd, make([]byte, 4096)); err != nil {
						if err != syscall.EAGAIN {
							t.Fatal(err)
						}
						break
					}
				}
				if err := syscall.SetNonblock(fd, false); err != nil {
					t.Fatal(err)
				}
				cmd.Stdout = w
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cancel()
				if cmd.ProcessState == nil {
					_ = cmd.Wait()
				}
			}()
			for {
				source := readSuggestSource(t, evidence)
				reached := source.Started
				if boundary == "read" {
					reached = source.ReadKnown
				}
				if boundary == "forward" {
					reached = source.ForwardKnown && stdout.String() == output
				}
				if reached {
					if group, err := syscall.Getpgid(source.ChildPID); err != nil || group != cmd.Process.Pid {
						t.Fatal("child escaped wrapper process group")
					}
					break
				}
				if ctx.Err() != nil {
					t.Fatal("boundary witness not reached")
				}
				time.Sleep(time.Millisecond)
			}
			if err := cmd.Cancel(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil || ctx.Err() != nil {
				t.Fatal("group termination failed")
			}
			source := readSuggestSource(t, evidence)
			if source.Calls != 1 || source.Completed != 0 || source.PID != cmd.Process.Pid || source.ChildPID <= 0 || !source.Started || source.ExitCode != nil || source.OutputObserved {
				t.Fatal("lost surviving spawn or invented completion")
			}
			if source.ReadKnown != (boundary != "spawn") || source.ForwardKnown != (boundary == "forward") {
				t.Fatal("incorrect read/forward witnesses")
			}
			if source.ReadKnown && source.ReadBytes != len(output) || source.ForwardKnown && source.ForwardBytes != len(output) {
				t.Fatal("incorrect prefix counts")
			}
			if boundary == "forward" && (stdout.String() != output || stderr.String() != "private-stderr-sentinel") {
				t.Fatal("changed exact I/O")
			}
			checkSuggestPrivacy(t, evidence, source)
		})
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

func TestSuggestLifecycleOutput(t *testing.T) {
	const base = "msg=reply_fallback.lifecycle pid=42 attempt_ms=9810 child_ms=9800 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=true own_deadline_elapsed=true group_cancel_requested=true wait_completed=true exit_observed=true exit_code=-1 exit_signal=9 "
	const output = "output_observed=true wait_ok=false wait_delay=false stdout_bytes=0 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=false stdout_result_ok=unknown stdout_text_ok=unknown"
	const unknownOutput = "stdout_bytes=unknown stdout_cap_exceeded=unknown stdout_utf8_ok=unknown stdout_json_ok=unknown stdout_result_ok=unknown stdout_text_ok=unknown"
	for _, tc := range []struct{ name, fields, want string }{
		{"zero", output, "stdout_bytes=0 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=false stdout_result_ok=unknown stdout_text_ok=unknown"},
		{"zero decoded", strings.Replace(output, "stdout_json_ok=false", "stdout_json_ok=true", 1), unknownOutput},
		{"zero successful predicates", strings.ReplaceAll(strings.Replace(output, "stdout_json_ok=false", "stdout_json_ok=true", 1), "=unknown", "=true"), unknownOutput},
		{"zero invalid utf8", strings.Replace(strings.Replace(output, "stdout_utf8_ok=true", "stdout_utf8_ok=false", 1), "stdout_json_ok=false", "stdout_json_ok=unknown", 1), unknownOutput},
		{"missing", "output_observed=true", "stdout_bytes=unknown"},
		{"partial predicates", strings.Replace(output, "stdout_utf8_ok=true ", "", 1), "stdout_bytes=unknown"},
		{"missing decode", strings.Replace(output, "stdout_json_ok=false ", "", 1), "stdout_bytes=unknown"},
		{"missing cap", strings.Replace(output, "stdout_cap_exceeded=false ", "", 1), "stdout_bytes=unknown"},
		{"contradictory cap", strings.Replace(output, "stdout_cap_exceeded=false", "stdout_cap_exceeded=true", 1), "stdout_bytes=unknown"},
		{"malformed", strings.Replace(output, "stdout_bytes=0", "stdout_bytes=private-output", 1), "stdout_bytes=unknown"},
		{"null", strings.Replace(output, "stdout_bytes=0", "stdout_bytes=<nil>", 1), "stdout_bytes=unknown"},
		{"negative", strings.Replace(output, "stdout_bytes=0", "stdout_bytes=-1", 1), "stdout_bytes=unknown"},
		{"above sentinel", strings.Replace(output, "stdout_bytes=0", "stdout_bytes=4098", 1), "stdout_bytes=unknown"},
		{"missing wait", strings.Replace(output, "wait_ok=false ", "", 1), "stdout_bytes=unknown"},
		{"contradictory wait", strings.Replace(strings.Replace(output, "wait_ok=false", "wait_ok=true", 1), "wait_delay=false", "wait_delay=true", 1), "wait_ok=unknown"},
		{"contradictory exit", strings.Replace(output, "wait_ok=false", "wait_ok=true", 1), "wait_ok=unknown"},
		{"nonboolean", strings.Replace(output, "stdout_utf8_ok=true", "stdout_utf8_ok=1", 1), "stdout_bytes=unknown"},
		{"invalid utf8 count", strings.Replace(strings.Replace(strings.Replace(output, "stdout_utf8_ok=true", "stdout_utf8_ok=false", 1), "stdout_json_ok=false", "stdout_json_ok=unknown", 1), "stdout_bytes=0", "stdout_bytes=1", 1), "stdout_bytes=1"},
		{"invalid utf8 decode", strings.Replace(strings.Replace(output, "stdout_utf8_ok=true", "stdout_utf8_ok=false", 1), "stdout_bytes=0", "stdout_bytes=1", 1), "stdout_json_ok=unknown"},
		{"missing result", strings.Replace(strings.Replace(strings.Replace(output, "stdout_json_ok=false", "stdout_json_ok=true", 1), "stdout_bytes=0", "stdout_bytes=25", 1), "stdout_result_ok=unknown ", "", 1), "stdout_result_ok=unknown"},
		{"decode gate", strings.ReplaceAll(output, "=unknown", "=true"), "stdout_result_ok=unknown"},
		{"valid", strings.ReplaceAll(strings.Replace(strings.Replace(output, "stdout_json_ok=false", "stdout_json_ok=true", 1), "stdout_bytes=0", "stdout_bytes=25", 1), "=unknown", "=true"), "stdout_text_ok=true"},
		{"saturated", strings.Replace(strings.Replace(output, "stdout_bytes=0", "stdout_bytes=4097", 1), "stdout_cap_exceeded=false", "stdout_cap_exceeded=true", 1), "stdout_bytes=4097"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestLifecycle(base+tc.fields+"\n", 42)
			if !strings.Contains(got, tc.want) || strings.Contains(got, "private-") {
				t.Fatal("incorrect safe output observation")
			}
		})
	}
	got := suggestLifecycle(strings.Replace(base, "wait_completed=true", "wait_completed=false", 1)+output+"\n", 42)
	if !strings.Contains(got, "stdout_bytes=unknown") || !strings.Contains(got, "exit_code=unknown") {
		t.Fatal("unobserved Wait invented output/exit")
	}
	completed := strings.Replace(strings.Replace(base, "exit_code=-1", "exit_code=0", 1), "exit_signal=9", "exit_signal=0", 1)
	for _, fields := range []string{
		strings.Replace(output, "wait_ok=false", "wait_ok=true", 1),
		strings.Replace(output, "wait_delay=false", "wait_delay=true", 1),
	} {
		if got := suggestLifecycle(completed+fields+"\n", 42); !strings.Contains(got, "stdout_bytes=0") || strings.Contains(got, "wait_ok=unknown") || strings.Contains(got, "wait_delay=unknown") {
			t.Fatal("lost successful Wait or WaitDelay snapshot")
		}
	}
	unknown := strings.Replace(strings.Replace(strings.Replace(strings.Replace(base, "wait_completed=true", "wait_completed=false", 1), "exit_observed=true", "exit_observed=false", 1), "exit_code=-1", "exit_code=unknown", 1), "exit_signal=9", "exit_signal=unknown", 1)
	if got := suggestLifecycle(unknown+output+"\n", 42); !strings.Contains(got, "exit_code=unknown") || !strings.Contains(got, "wait_ok=unknown") || !strings.Contains(got, "stdout_bytes=unknown") {
		t.Fatal("lost explicit unknown completion")
	}
}

func TestSuggestSourceDiagnosticApplicability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		utf8, json bool
		want       string
	}{
		{"invalid utf8", false, true, "json=unknown result_success=unknown text_valid=unknown"},
		{"failed decode", true, false, "json=false result_success=unknown text_valid=unknown"},
		{"decoded", true, true, "json=true result_success=false text_valid=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := suggestSource{OutputObserved: true, UTF8OK: tc.utf8, JSONOK: tc.json, TextOK: true}
			if got := source.diagnostic(false, 0, 0); !strings.Contains(got, tc.want) || strings.Contains(got, "private-") {
				t.Fatal("incorrect source predicate applicability")
			}
		})
	}
}

func TestSuggestSourceCapturePresence(t *testing.T) {
	if readSuggestSource(t, filepath.Join(t.TempDir(), "absent")) != (suggestSource{}) {
		t.Fatal("absent file invented evidence")
	}
	const start = "{\"calls\":1,\"pid\":42,\"start_ms\":1}\n"
	const capture = `{"completed":1,"pid":42,"child_pid":43,"elapsed_ms":1,"exit_code":0,"output_observed":true,"stdout_bytes":0,"utf8_ok":true,"json_ok":false,"result_ok":false,"text_ok":false}`
	for _, tc := range []struct {
		name, record string
		observed     bool
	}{
		{"explicit zero", capture, true},
		{"duplicate completion", capture + "\n" + capture, false},
		{"progress after completion", capture + "\n" + `{"progress":"read","pid":42,"child_pid":43,"bytes":1,"saturated":false}`, false},
		{"partial completion", `{"completed":1,"pid":42`, false},
		{"wrapper mismatch", strings.Replace(capture, `"pid":42`, `"pid":44`, 1), false},
		{"child mismatch", strings.Replace(capture, `"child_pid":43`, `"child_pid":44`, 1), false},
		{"missing pid", strings.Replace(capture, `"pid":42,`, "", 1), false},
		{"before spawn", "before-spawn", false},
		{"missing completion", strings.Replace(capture, `"completed":1,`, "", 1), false},
		{"missing elapsed", strings.Replace(capture, `"elapsed_ms":1,`, "", 1), false},
		{"negative elapsed", strings.Replace(capture, `"elapsed_ms":1`, `"elapsed_ms":-1`, 1), false},
		{"missing exit", strings.Replace(capture, `"exit_code":0,`, "", 1), false},
		{"null exit", strings.Replace(capture, `"exit_code":0`, `"exit_code":null`, 1), false},
		{"duplicate key", strings.Replace(capture, `"stdout_bytes":0`, `"stdout_bytes":1,"stdout_bytes":0`, 1), false},
		{"typed exit", strings.Replace(capture, `"exit_code":0`, `"exit_code":"private-error"`, 1), false},
		{"invalid zero utf8", strings.Replace(capture, `"utf8_ok":true`, `"utf8_ok":false`, 1), false},
		{"zero decoded", strings.Replace(capture, `"json_ok":false`, `"json_ok":true`, 1), false},
		{"read without forward", `{"progress":"read","pid":42,"child_pid":43,"bytes":1,"saturated":false}` + "\n" + strings.Replace(capture, `"stdout_bytes":0`, `"stdout_bytes":1`, 1), false},
		{"missing count", strings.Replace(capture, `"stdout_bytes":0,`, "", 1), false},
		{"null count", strings.Replace(capture, `"stdout_bytes":0`, `"stdout_bytes":null`, 1), false},
		{"missing predicate", strings.Replace(capture, `,"text_ok":false`, "", 1), false},
		{"null predicate", strings.Replace(capture, `"json_ok":false`, `"json_ok":null`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.jsonl")
			records := start + `{"progress":"spawn","pid":42,"child_pid":43}` + "\n" + tc.record + "\n"
			if tc.record == "before-spawn" {
				records = start + capture + "\n" + `{"progress":"spawn","pid":42,"child_pid":43}` + "\n"
			}
			if err := os.WriteFile(path, []byte(records), 0600); err != nil {
				t.Fatal(err)
			}
			got := readSuggestSource(t, path)
			if got.OutputObserved != tc.observed || (got.Completed == 1) != tc.observed || (got.ExitCode != nil) != tc.observed {
				t.Fatal("missing metadata became observed capture")
			}
			if !tc.observed && !strings.Contains(got.diagnostic(false, 0, 0), "output={unknown}") {
				t.Fatal("invented zero capture")
			}
		})
	}
}

func TestSuggestProgressMetadata(t *testing.T) {
	const start = "{\"calls\":1,\"pid\":42,\"start_ms\":1}\n"
	const spawn = `{"progress":"spawn","pid":42,"child_pid":43}` + "\n"
	const read = `{"progress":"read","pid":42,"child_pid":43,"bytes":4097,"saturated":true}` + "\n"
	for _, tc := range []struct {
		name, records    string
		known, ambiguous bool
	}{
		{"missing", "", false, false},
		{"spawn", spawn, false, false},
		{"saturated", spawn + read, true, false},
		{"null", spawn + strings.Replace(read, "4097", "null", 1), false, true},
		{"missing count", spawn + strings.Replace(read, `"bytes":4097,`, "", 1), false, true},
		{"malformed", spawn + strings.Replace(read, "4097", `"private-value"`, 1), false, true},
		{"partial", spawn + `{"progress":"read"`, false, false},
		{"partial after read", spawn + read + `{"progress":"forward"`, true, false},
		{"wrapper mismatch", spawn + strings.Replace(read, "42", "44", 1), false, true},
		{"child mismatch", spawn + strings.Replace(read, "43", "44", 1), false, true},
		{"contradictory cap", spawn + strings.Replace(read, "true", "false", 1), false, true},
		{"out of order", read, false, true},
		{"null stage", spawn + strings.Replace(read, `"progress":"read"`, `"progress":null`, 1), false, true},
		{"missing saturation", spawn + strings.Replace(read, `,"saturated":true`, "", 1), false, true},
		{"duplicate", spawn + read + read, false, true},
		{"duplicate key", spawn + strings.Replace(read, `"bytes":4097`, `"bytes":1,"bytes":4097`, 1), false, true},
		{"zero prefix", spawn + strings.Replace(strings.Replace(read, "4097", "0", 1), "true", "false", 1), false, true},
		{"above cap", spawn + strings.Replace(read, "4097", "4098", 1), false, true},
		{"forward before read", spawn + strings.Replace(read, `"read"`, `"forward"`, 1), false, true},
		{"duplicate spawn", spawn + spawn, false, true},
		{"repeated invocation", spawn + start + read, false, true},
		{"missing wrapper", spawn + strings.Replace(read, `"pid":42,`, "", 1), false, true},
		{"wrong stage type", spawn + strings.Replace(read, `"progress":"read"`, `"progress":true`, 1), false, true},
		{"same process", strings.Replace(spawn, "43", "42", 1), false, true},
		{"read bound", spawn + strings.Repeat(" ", 64*1024) + read, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.jsonl")
			if err := os.WriteFile(path, []byte(start+tc.records), 0600); err != nil {
				t.Fatal(err)
			}
			source := readSuggestSource(t, path)
			if source.ReadKnown != tc.known || source.ProgressAmbiguous != tc.ambiguous || (tc.name == "partial" && !source.Started) {
				t.Fatal("invented or lost progress")
			}
			if tc.known && source.ReadBytes != 4097 {
				t.Fatal("lost bounded prefix")
			}
			if strings.Contains(source.diagnostic(false, 0, 0), "private-") {
				t.Fatal("malformed input escaped into diagnostic")
			}
		})
	}
}

func TestSuggestPrefixCorrelation(t *testing.T) {
	const start = `{"calls":1,"pid":42,"start_ms":1}` + "\n" + `{"progress":"spawn","pid":42,"child_pid":43}` + "\n"
	for _, n := range []int{1, 4096, 4097} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			count := strconv.Itoa(n)
			read := `{"progress":"read","pid":42,"child_pid":43,"bytes":` + count + `,"saturated":` + strconv.FormatBool(n == 4097) + `}` + "\n"
			forward := strings.Replace(read, `"read"`, `"forward"`, 1)
			capture := `{"completed":1,"pid":42,"child_pid":43,"elapsed_ms":1,"exit_code":0,"output_observed":true,"stdout_bytes":` + count + `,"utf8_ok":true,"json_ok":false,"result_ok":false,"text_ok":false}` + "\n"
			for _, tc := range []struct {
				name, records    string
				completed, known bool
			}{
				{"prefix", start + read + forward, false, true},
				{"partial completion", start + read + forward + `{"completed":`, false, true},
				{"complete", start + read + forward + capture, true, true},
				{"smaller capture", start + read + forward + strings.Replace(capture, `"stdout_bytes":`+count, `"stdout_bytes":0`, 1), false, false},
				{"duplicate forward", start + read + forward + forward, false, false},
				{"forward mismatch", start + read + strings.Replace(forward, `"child_pid":43`, `"child_pid":44`, 1), false, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "source.jsonl")
					if err := os.WriteFile(path, []byte(tc.records), 0600); err != nil {
						t.Fatal(err)
					}
					got := readSuggestSource(t, path)
					if got.ReadKnown != tc.known || got.ForwardKnown != tc.known || got.OutputObserved != tc.completed || (got.Completed == 1) != tc.completed {
						t.Fatal("incorrect correlated prefix/completion")
					}
					if tc.known && (got.ReadBytes != n || got.ForwardBytes != n || !strings.Contains(got.diagnostic(false, 0, 0), "bytes="+count+" saturated="+strconv.FormatBool(n == 4097))) {
						t.Fatal("lost prefix or saturation")
					}
					if !tc.completed && (!strings.Contains(got.diagnostic(false, 0, 0), "exit=unknown output={unknown}") || got.ExitCode != nil) {
						t.Fatal("prefix invented completion")
					}
				})
			}
		})
	}
}
