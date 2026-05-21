//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

const testSessionID = "00000000-0000-0000-0000-000000000001"

func TestWithWorktree_ReturnsExistingHomeIsolatedDir(t *testing.T) {
	dir := WithWorktree(t)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}

	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if got != dir {
		t.Fatalf("UserHomeDir = %q, want %q", got, dir)
	}

	// Confirm HOME pin is restored when a subtest exits — t.Setenv
	// guarantees per-test cleanup ordering.
	outerHome := os.Getenv("HOME")
	t.Run("nested", func(t *testing.T) {
		inner := WithWorktree(t)
		if os.Getenv("HOME") != inner {
			t.Fatalf("HOME = %q, want %q (subtest)", os.Getenv("HOME"), inner)
		}
	})
	if os.Getenv("HOME") != outerHome {
		t.Fatalf("HOME = %q after subtest, want %q", os.Getenv("HOME"), outerHome)
	}
}

// TestWithWorktreeAuthenticated_RealAssistant exercises the fixture end-to-end:
// with ANTHROPIC_API_KEY present in the outer env, a minimal `pyry agent-run`
// invocation produces a JSONL fixture with at least one real assistant event.
// When the credential is absent the helper itself calls t.Skip, so the suite
// stays green on contributor machines without API keys.
func TestWithWorktreeAuthenticated_RealAssistant(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      workdir,
		Prompt:       "Reply with the single word 'pong' and nothing else.",
		SystemPrompt: "You are a minimal e2e authentication probe. Keep replies under 10 words.",
		AllowedTools: []string{"Read"},
		MaxTurns:     1,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})

	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0\nstderr:\n%s", result.ExitCode, truncate(result.Stderr))
	}
	if result.SessionID == "" {
		t.Fatalf("SessionID is empty: no system/init envelope found in stdout\nstdout:\n%s", truncate(result.Stdout))
	}

	// Belt-and-suspenders against the #383 failure mode: the structural
	// assertions below would already fail on a synthetic-auth envelope
	// (ExitCode != 0, no EndOfTurn text), but the substring check produces
	// a clearer diagnostic when auth is the culprit. Stdout is capped at
	// 1 KiB via truncate to bound any future leakage surface; today
	// claude's stream-json output does not echo the API key.
	if bytes.Contains(result.Stdout, []byte(`"model":"<synthetic>"`)) {
		t.Fatalf("stdout contains synthetic-model marker (auth failed?)\nstdout:\n%s", truncate(result.Stdout))
	}
	if bytes.Contains(result.Stdout, []byte(`"error":"authentication_failed"`)) {
		t.Fatalf("stdout contains authentication_failed marker\nstdout:\n%s", truncate(result.Stdout))
	}

	events := ReadJSONL(t, workdir, result.SessionID)
	jsonlPath := jsonlPathFor(workdir, result.SessionID)
	for _, ev := range events {
		if ev.Kind == "assistant" && ev.EndOfTurn && ev.TextChars > 0 {
			return
		}
	}
	t.Fatalf("no assistant event with EndOfTurn=true and TextChars>0 found in JSONL\npath: %s\nstderr:\n%s",
		jsonlPath, truncate(result.Stderr))
}

// TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet
// re-execs the test binary with both ANTHROPIC_API_KEY and
// CLAUDE_CODE_OAUTH_TOKEN cleared. t.Skipf ends the calling goroutine via
// runtime.Goexit() with no in-process return value, so asserting on the
// skip-message text requires the outer/inner subprocess pattern. Mirrors
// TestRunPyryAgentRun_Timeout.
func TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet(t *testing.T) {
	if os.Getenv("PYRY_REALCLAUDE_AUTH_SKIP_INNER") == "1" {
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
		WithWorktreeAuthenticated(t)
		t.Fatalf("WithWorktreeAuthenticated returned without skipping; want t.Skip when both creds unset")
		return
	}
	cmd := exec.Command(os.Args[0],
		"-test.run=^TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet$",
		"-test.v")
	cmd.Env = append(os.Environ(), "PYRY_REALCLAUDE_AUTH_SKIP_INNER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inner test exited non-zero (t.Skip should be success): %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("--- SKIP: TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet")) {
		t.Fatalf("inner test did not skip; output:\n%s", out)
	}
	wants := []string{
		"ANTHROPIC_API_KEY",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"security find-generic-password -s 'Claude Code-credentials' -w",
		"jq -r '.claudeAiOauth.accessToken'",
	}
	for _, w := range wants {
		if !bytes.Contains(out, []byte(w)) {
			t.Fatalf("skip message missing required substring %q\noutput:\n%s", w, out)
		}
	}
}

// TestWithWorktreeAuthenticated_OAuthTokenOnly_RepinsAndPreservesAbsentApiKey
// drives the OAuth-only branch in-process. No subprocess, no network. The
// token value is a synthetic literal — claude is never invoked here. We
// verify (a) the helper proceeds past the skip gate when only the OAuth
// token is set, (b) the token survives the WithWorktree HOME re-pin via
// t.Setenv, (c) the helper does NOT t.Setenv the absent
// ANTHROPIC_API_KEY (preserve original outer-env shape), and (d) the
// captured ~/.claude.json bytes are seeded verbatim into <tempHome>/.claude.json
// at mode 0o600 (#496 contract — interactive PTY claude needs this file to
// skip the onboarding theme picker).
func TestWithWorktreeAuthenticated_OAuthTokenOnly_RepinsAndPreservesAbsentApiKey(t *testing.T) {
	const token = "test-oauth-token-not-real"
	// Pre-seed a fake operator HOME with a recognisable .claude.json before
	// the fixture is invoked. The _marker field makes the verbatim-copy
	// assertion below diagnostic.
	opHome := t.TempDir()
	want := []byte(`{"hasCompletedOnboarding":true,"installMethod":"npm-global","_marker":"#496-test"}` + "\n")
	if err := os.WriteFile(filepath.Join(opHome, ".claude.json"), want, 0o600); err != nil {
		t.Fatalf("WriteFile fake operator .claude.json: %v", err)
	}
	t.Setenv("HOME", opHome)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", token)
	// Unset the API key entirely so we can distinguish absent from set-empty.
	if err := os.Unsetenv("ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("Unsetenv ANTHROPIC_API_KEY: %v", err)
	}

	dir := WithWorktreeAuthenticated(t)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if got := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); got != token {
		t.Fatalf("CLAUDE_CODE_OAUTH_TOKEN = %q, want %q", got, token)
	}
	if _, present := os.LookupEnv("ANTHROPIC_API_KEY"); present {
		t.Fatalf("ANTHROPIC_API_KEY present in env; helper must not Setenv an absent var")
	}
	if got := os.Getenv("HOME"); got != dir {
		t.Fatalf("HOME = %q, want %q", got, dir)
	}
	seeded := filepath.Join(dir, ".claude.json")
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("ReadFile seeded .claude.json: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("seeded .claude.json bytes differ\n got: %q\nwant: %q", got, want)
	}
	seededInfo, err := os.Stat(seeded)
	if err != nil {
		t.Fatalf("Stat seeded .claude.json: %v", err)
	}
	if mode := seededInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("seeded .claude.json mode = %#o, want %#o", mode, 0o600)
	}
}

// TestWithWorktreeAuthenticated_SkipsWhenOAuthSetButNoClaudeJSON re-execs
// the test binary with CLAUDE_CODE_OAUTH_TOKEN set but the operator HOME
// pinned to an empty tempdir (no .claude.json). t.Skipf ends the inner
// goroutine via runtime.Goexit() with no in-process return value, so
// asserting on the skip-message text requires the outer/inner subprocess
// pattern. Uses sentinel PYRY_REALCLAUDE_NOJSON_INNER=1 (distinct from
// PYRY_REALCLAUDE_AUTH_SKIP_INNER=1 because that sentinel clears BOTH env
// vars; this test needs the OAuth token SET) and falls through TestMain
// because GO_TEST_HELPER_PROCESS is unset.
func TestWithWorktreeAuthenticated_SkipsWhenOAuthSetButNoClaudeJSON(t *testing.T) {
	if os.Getenv("PYRY_REALCLAUDE_NOJSON_INNER") == "1" {
		opHome := t.TempDir()
		t.Setenv("HOME", opHome)
		t.Setenv("ANTHROPIC_API_KEY", "")
		if err := os.Unsetenv("ANTHROPIC_API_KEY"); err != nil {
			t.Fatalf("Unsetenv ANTHROPIC_API_KEY: %v", err)
		}
		t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token-not-real")
		WithWorktreeAuthenticated(t)
		t.Fatalf("WithWorktreeAuthenticated returned without skipping; want t.Skip when .claude.json missing")
		return
	}
	cmd := exec.Command(os.Args[0],
		"-test.run=^TestWithWorktreeAuthenticated_SkipsWhenOAuthSetButNoClaudeJSON$",
		"-test.v")
	cmd.Env = append(os.Environ(), "PYRY_REALCLAUDE_NOJSON_INNER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inner test exited non-zero (t.Skip should be success): %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("--- SKIP: TestWithWorktreeAuthenticated_SkipsWhenOAuthSetButNoClaudeJSON")) {
		t.Fatalf("inner test did not skip; output:\n%s", out)
	}
	wants := []string{
		"CLAUDE_CODE_OAUTH_TOKEN",
		".claude.json",
		"onboarding",
		"hasCompletedOnboarding=true",
		"claude",
	}
	for _, w := range wants {
		if !bytes.Contains(out, []byte(w)) {
			t.Fatalf("skip message missing required substring %q\noutput:\n%s", w, out)
		}
	}
}

func TestReadJSONL_HappyPath(t *testing.T) {
	workdir := WithWorktree(t)
	writeFixtureLines(t, workdir, testSessionID,
		`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
	)

	events := ReadJSONL(t, workdir, testSessionID)

	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Kind != "assistant" || !events[0].EndOfTurn {
		t.Fatalf("events[0] = {Kind:%q, EndOfTurn:%v}, want {assistant, true}",
			events[0].Kind, events[0].EndOfTurn)
	}
	if events[1].Kind != "user" || events[1].EndOfTurn {
		t.Fatalf("events[1] = {Kind:%q, EndOfTurn:%v}, want {user, false}",
			events[1].Kind, events[1].EndOfTurn)
	}
}

func TestReadJSONL_EmptyFile(t *testing.T) {
	workdir := WithWorktree(t)
	writeFixtureLines(t, workdir, testSessionID)

	events := ReadJSONL(t, workdir, testSessionID)

	if len(events) != 0 {
		t.Fatalf("len(events) = %d, want 0", len(events))
	}
}

// Missing-file path is unit-tested via the private resolveAndOpenJSONL
// split so we can assert the returned error verbatim instead of trying
// to capture a t.Fatalf call.
func TestResolveAndOpenJSONL_MissingFile(t *testing.T) {
	workdir := WithWorktree(t)

	_, path, err := resolveAndOpenJSONL(workdir, testSessionID)
	if err == nil {
		t.Fatalf("resolveAndOpenJSONL: want error for missing file, got nil")
	}
	home, _ := os.UserHomeDir()
	enc, _ := agentrun.EncodeProjectDir(workdir)
	wantPath := filepath.Join(home, ".claude", "projects", enc, testSessionID+".jsonl")
	if path != wantPath {
		t.Fatalf("returned path = %q, want %q", path, wantPath)
	}
	if !strings.Contains(err.Error(), wantPath) {
		t.Fatalf("error %q does not contain resolved path %q", err.Error(), wantPath)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error %v is not os.ErrNotExist", err)
	}
}

func TestJSONLEntry_AliasCompiles(t *testing.T) {
	var _ jsonl.Event = JSONLEntry{}
}

// TestExtraEnvHasHelperProcessFlag exercises the recursion-guard predicate.
// Belt-and-suspenders against the 2026-05-16 fork-bomb pattern recurring.
func TestExtraEnvHasHelperProcessFlag(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want bool
	}{
		{"empty", nil, false},
		{"unrelated only", []string{"FOO=bar", "BAZ=qux"}, false},
		{"flag present", []string{"GO_TEST_HELPER_PROCESS=1"}, true},
		{"flag with neighbors", []string{"A=1", "GO_TEST_HELPER_PROCESS=1", "B=2"}, true},
		{"flag set to 0", []string{"GO_TEST_HELPER_PROCESS=0"}, false},
		{"flag empty value", []string{"GO_TEST_HELPER_PROCESS="}, false},
		{"prefix-only", []string{"GO_TEST_HELPER_PROCESS_EXTRA=1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extraEnvHasHelperProcessFlag(tc.env); got != tc.want {
				t.Fatalf("extraEnvHasHelperProcessFlag(%v) = %t, want %t", tc.env, got, tc.want)
			}
		})
	}
}

// TestMain branches on GO_TEST_HELPER_PROCESS so the test binary doubles as
// a fake `pyry` for tests that opt in via RunOpts.UseTestBinaryAsFakePyry.
//
// Previously this also set PYRY_E2E_BIN=os.Args[0] so ensurePyryBuilt
// short-circuited to the test binary. That pattern caused the 2026-05-16
// fork-bomb: tests that wanted *real* pyry (tool_loop, per_agent — added
// after this TestMain was written) inherited the self-referential env var,
// invoked the test binary as "pyry", whose TestMain fell through to
// m.Run() and recursed unboundedly. Each test that wants the fake-pyry
// pattern now opts in explicitly via the RunOpts field.
func TestMain(m *testing.M) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") == "1" {
		runFakePyry()
		return
	}
	os.Exit(m.Run())
}

func runFakePyry() {
	switch os.Getenv("PYRY_E2E_FAKE_MODE") {
	case "happy":
		fmt.Println(`{"type":"system","subtype":"init","cwd":"/tmp","tools":["Read"],"model":"claude-haiku-4-5","session_id":"` + fakeInitSessionID + `"}`)
		fmt.Println(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
		fmt.Println(`{"type":"result","session_id":"` + fakeInitSessionID + `"}`)
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "fake pyry: simulated failure before any stream-json")
		os.Exit(2)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "argv":
		for _, a := range os.Args {
			fmt.Fprintln(os.Stderr, a)
		}
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "fake pyry: unknown PYRY_E2E_FAKE_MODE %q\n", os.Getenv("PYRY_E2E_FAKE_MODE"))
	os.Exit(99)
}

const fakeInitSessionID = "11111111-1111-4111-8111-111111111111"

func fullRunOpts(workdir string) RunOpts {
	return RunOpts{
		Workdir:                 workdir,
		Prompt:                  "hello",
		SystemPrompt:            "you are a tester",
		AllowedTools:            []string{"Read"},
		MaxTurns:                1,
		Effort:                  "low",
		Model:                   "claude-haiku-4-5",
		ExtraEnv:                []string{"GO_TEST_HELPER_PROCESS=1"},
		UseTestBinaryAsFakePyry: true,
	}
}

func TestRunPyryAgentRun_HappyPath(t *testing.T) {
	workdir := WithWorktree(t)
	opts := fullRunOpts(workdir)
	opts.ExtraEnv = append(opts.ExtraEnv, "PYRY_E2E_FAKE_MODE=happy")

	result := RunPyryAgentRun(t, opts)

	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if result.SessionID != fakeInitSessionID {
		t.Fatalf("SessionID = %q, want %q", result.SessionID, fakeInitSessionID)
	}
	if !bytes.Contains(result.Stdout, []byte(`"subtype":"init"`)) {
		t.Fatalf("stdout missing init envelope:\n%s", result.Stdout)
	}
	if !bytes.Contains(result.Stdout, []byte(`"type":"result"`)) {
		t.Fatalf("stdout missing result trailer:\n%s", result.Stdout)
	}
}

func TestValidateRunOpts_Positive(t *testing.T) {
	if err := validateRunOpts(fullRunOpts("/tmp/x")); err != nil {
		t.Fatalf("validateRunOpts(full) = %v, want nil", err)
	}
}

func TestValidateRunOpts_Negative(t *testing.T) {
	cases := []struct {
		name      string
		mut       func(*RunOpts)
		wantField string
	}{
		{"workdir", func(o *RunOpts) { o.Workdir = "" }, "Workdir"},
		{"prompt", func(o *RunOpts) { o.Prompt = "" }, "Prompt"},
		{"system prompt", func(o *RunOpts) { o.SystemPrompt = "" }, "SystemPrompt"},
		{"allowed tools", func(o *RunOpts) { o.AllowedTools = nil }, "AllowedTools"},
		{"zero max turns", func(o *RunOpts) { o.MaxTurns = 0 }, "MaxTurns"},
		{"negative max turns", func(o *RunOpts) { o.MaxTurns = -1 }, "MaxTurns"},
		{"effort", func(o *RunOpts) { o.Effort = "" }, "Effort"},
		{"model", func(o *RunOpts) { o.Model = "" }, "Model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := fullRunOpts("/tmp/x")
			tc.mut(&opts)
			err := validateRunOpts(opts)
			if err == nil {
				t.Fatalf("validateRunOpts: want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("err %q does not name field %q", err.Error(), tc.wantField)
			}
		})
	}
}

func TestRunPyryAgentRun_InitEventAbsent(t *testing.T) {
	workdir := WithWorktree(t)
	opts := fullRunOpts(workdir)
	opts.ExtraEnv = append(opts.ExtraEnv, "PYRY_E2E_FAKE_MODE=fail")

	result := RunPyryAgentRun(t, opts)

	if result.ExitCode != 2 {
		t.Fatalf("ExitCode = %d, want 2 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if result.SessionID != "" {
		t.Fatalf("SessionID = %q, want empty", result.SessionID)
	}
}

// TestRunPyryAgentRun_Timeout re-execs itself so the inner branch's t.Fatalf
// is captured as a subprocess failure rather than killing the outer test.
func TestRunPyryAgentRun_Timeout(t *testing.T) {
	if os.Getenv("PYRY_REALCLAUDE_TIMEOUT_INNER") == "1" {
		workdir := WithWorktree(t)
		opts := fullRunOpts(workdir)
		opts.ExtraEnv = append(opts.ExtraEnv, "PYRY_E2E_FAKE_MODE=sleep")
		opts.Timeout = 100 * time.Millisecond
		RunPyryAgentRun(t, opts)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunPyryAgentRun_Timeout$", "-test.v")
	cmd.Env = append(os.Environ(), "PYRY_REALCLAUDE_TIMEOUT_INNER=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("inner test succeeded, want timeout failure:\n%s", out)
	}
	if !bytes.Contains(out, []byte("timed out")) {
		t.Fatalf("output missing 'timed out':\n%s", out)
	}
	if !bytes.Contains(out, []byte("stdout:")) || !bytes.Contains(out, []byte("stderr:")) {
		t.Fatalf("output missing stdout/stderr capture:\n%s", out)
	}
}

func TestRunPyryAgentRun_ArgvContract(t *testing.T) {
	workdir := WithWorktree(t)
	opts := fullRunOpts(workdir)
	opts.ExtraEnv = append(opts.ExtraEnv, "PYRY_E2E_FAKE_MODE=argv")
	opts.AllowedTools = []string{"Read", "Bash"}
	opts.MaxTurns = 3

	result := RunPyryAgentRun(t, opts)

	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
	want := []string{
		"agent-run",
		"--prompt-file=" + filepath.Join(workdir, "prompt.txt"),
		"--system-prompt-file=" + filepath.Join(workdir, "system.txt"),
		"--allowed-tools=Read,Bash",
		"--max-turns=3",
		"--effort=low",
		"--model=claude-haiku-4-5",
		"--workdir=" + workdir,
		"--output-format=stream-json",
	}
	for _, w := range want {
		if !bytes.Contains(result.Stderr, []byte(w)) {
			t.Fatalf("argv missing %q:\n%s", w, result.Stderr)
		}
	}
}

func TestParseInitSessionID(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"init line", `{"type":"system","subtype":"init","session_id":"abc"}` + "\n", "abc"},
		{"empty session id", `{"type":"system","subtype":"init","session_id":""}` + "\n", ""},
		{
			"non-init system line first",
			`{"type":"system","subtype":"warn","session_id":"skip"}` + "\n" +
				`{"type":"system","subtype":"init","session_id":"yes"}` + "\n",
			"yes",
		},
		{"result trailer only", `{"type":"result","session_id":"x"}` + "\n", ""},
		{
			"malformed before init",
			"not json\n" + `{"type":"system","subtype":"init","session_id":"ok"}` + "\n",
			"ok",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseInitSessionID([]byte(tc.input)); got != tc.want {
				t.Fatalf("parseInitSessionID = %q, want %q", got, tc.want)
			}
		})
	}
}

func writeFixtureLines(t *testing.T, workdir, sessionID string, lines ...string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	enc, err := agentrun.EncodeProjectDir(workdir)
	if err != nil {
		t.Fatalf("EncodeProjectDir: %v", err)
	}
	dir := filepath.Join(home, ".claude", "projects", enc)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	var body string
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}
