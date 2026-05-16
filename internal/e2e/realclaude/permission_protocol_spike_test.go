//go:build e2e_realclaude

package realclaude

// TestRealClaude_PermissionProtocol_Spike is the captured-trace test for
// #383. It invokes `claude` directly (NOT `pyry agent-run`) with
// `--permission-prompt-tool stdio` and writes every stdout stream-json
// line to a fixture under testdata/. The test PASSES regardless of
// whether a permission event fires — its purpose is to document
// observed behavior.
//
// Matrix sweep: the spike-runner reruns this test with each
// `--permission-mode` value by editing permissionMode below, then
// renames the resulting fixture (the filename embeds only the claude
// version) before the next run. See
// docs/knowledge/features/permission-protocol-spike.md.
//
// Divergence from pyry's canonical agent-run argv (cmd/pyry/agent_run.go
// buildClaudeArgs):
//   - NO `--dangerously-skip-permissions` (that flag suppresses gates).
//   - ADDS `--permission-prompt-tool stdio`.
//   - ADDS `--permission-mode <mode>`.
//   - OMITS `--append-system-prompt-file` and `--effort` to keep the
//     input surface minimal across reruns.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// permissionMode is the single --permission-mode the test runs. Edit and
// rerun to sweep the matrix; rename the prior fixture beforehand.
const permissionMode = "default"

// spikeTimeout caps the run. claude may block forever waiting for a
// response to a permission request (the spike does not respond), so the
// timeout is the bound that lets the test finish.
const spikeTimeout = 90 * time.Second

// stderrFixtureCap is the byte cap for embedded stderr in the fixture.
const stderrFixtureCap = 8 * 1024

func TestRealClaude_PermissionProtocol_Spike(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("claude binary not on PATH: %v", err)
	}
	// WithWorktreeAuthenticated — pinning HOME strips claude's local
	// credential store. The spike needs a real API response to capture
	// the permission protocol, so we re-pin ANTHROPIC_API_KEY from the
	// outer env. Skips cleanly when the key is unset.
	workdir := WithWorktreeAuthenticated(t)

	versionRaw, versionToken := captureClaudeVersion(t)

	argv := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--allowed-tools", "Read",
		"--permission-prompt-tool", "stdio",
		"--permission-mode", permissionMode,
		"--max-turns", "2",
		"--model", "claude-haiku-4-5",
	}

	// Pyry's streamrunner envelope shape — the one known to be accepted
	// by claude on stream-json input. Open question in the spec flagged
	// a simpler `{"type":"user","content":"..."}` fallback, but pyry's
	// production envelope is the safer baseline.
	stdinEnvelope := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{
					"type": "text",
					"text": "Use the Bash tool to run `ls -la` and report the result.",
				},
			},
		},
	}
	envelopeBytes, err := json.Marshal(stdinEnvelope)
	if err != nil {
		t.Fatalf("marshal stdin envelope: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), spikeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", argv...)
	cmd.Dir = workdir

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start claude: %v", err)
	}

	// Single reader goroutine; main goroutine writes envelope and waits.
	var (
		events    []json.RawMessage
		readerErr error
	)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(stdoutPipe)
		// 1 MiB cap — stream-json lines under --max-turns 2 are unlikely
		// to exceed this, but the default 64 KiB cap is too low for
		// model output and the bigger buffer is essentially free.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			events = append(events, json.RawMessage(line))
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			readerErr = err
		}
	}()

	if _, err := stdinPipe.Write(append(envelopeBytes, '\n')); err != nil {
		t.Logf("stdin write: %v (continuing — child exit is authoritative)", err)
	}
	if err := stdinPipe.Close(); err != nil {
		t.Logf("stdin close: %v (continuing)", err)
	}

	waitErr := cmd.Wait()
	<-readerDone
	duration := time.Since(start)

	deadlineTripped := errors.Is(ctx.Err(), context.DeadlineExceeded)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if readerErr != nil {
		t.Logf("stdout reader error: %v (events captured: %d)", readerErr, len(events))
	}

	// Structural failure: nothing captured AND deadline tripped.
	if len(events) == 0 && deadlineTripped {
		t.Fatalf("claude produced no stdout before timeout; nothing to capture\nstderr:\n%s\nwaitErr: %v",
			truncateString(stderrBuf.String(), stderrFixtureCap), waitErr)
	}

	fixturePath := writeFixture(t, fixtureRecord{
		ClaudeVersionRaw:       versionRaw,
		ClaudeVersion:          versionToken,
		Argv:                   append([]string{"claude"}, argv...),
		PermissionMode:         permissionMode,
		AllowedTools:           []string{"Read"},
		StdinEnvelopeSent:      json.RawMessage(envelopeBytes),
		StdoutEvents:           events,
		StderrCapture:          truncateString(stderrBuf.String(), stderrFixtureCap),
		ExitCode:               exitCode,
		ContextDeadlineTripped: deadlineTripped,
		DurationMs:             duration.Milliseconds(),
	}, versionToken)

	t.Logf("captured %d stdout event(s), exit=%d, deadline_tripped=%v, duration=%s",
		len(events), exitCode, deadlineTripped, duration)
	t.Logf("fixture written: %s", fixturePath)
}

type fixtureRecord struct {
	ClaudeVersionRaw       string            `json:"claude_version_raw"`
	ClaudeVersion          string            `json:"claude_version"`
	Argv                   []string          `json:"argv"`
	PermissionMode         string            `json:"permission_mode"`
	AllowedTools           []string          `json:"allowed_tools"`
	StdinEnvelopeSent      json.RawMessage   `json:"stdin_envelope_sent"`
	StdoutEvents           []json.RawMessage `json:"stdout_events"`
	StderrCapture          string            `json:"stderr_capture"`
	ExitCode               int               `json:"exit_code"`
	ContextDeadlineTripped bool              `json:"context_deadline_tripped"`
	DurationMs             int64             `json:"duration_ms"`
}

// captureClaudeVersion shells out to `claude --version` once, returning
// the trimmed full output and the leading whitespace-split token
// extracted from it. Falls back to the trimmed output if no whitespace
// is present.
func captureClaudeVersion(t *testing.T) (raw, token string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("claude --version: %v\noutput:\n%s", err, out)
	}
	raw = strings.TrimSpace(string(out))
	if fields := strings.Fields(raw); len(fields) > 0 {
		token = fields[0]
	} else {
		token = raw
	}
	return raw, token
}

var versionSlugSubst = regexp.MustCompile(`[^a-z0-9._-]+`)

func versionSlug(token string) string {
	slug := versionSlugSubst.ReplaceAllString(strings.ToLower(token), "_")
	if len(slug) > 32 {
		slug = slug[:32]
	}
	return slug
}

func writeFixture(t *testing.T, rec fixtureRecord, versionToken string) string {
	t.Helper()
	pkgDir := packageDir(t)
	dir := filepath.Join(pkgDir, "testdata")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("permission_protocol_v%s.json", versionSlug(versionToken)))
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatalf("write fixture tmp: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename fixture: %v", err)
	}
	return path
}

// packageDir returns the directory holding this test file. We avoid
// runtime.Caller-based discovery (which can resolve to a temp build
// path) by walking up from the current working directory — `go test`
// runs each package test in its own source directory.
func packageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

