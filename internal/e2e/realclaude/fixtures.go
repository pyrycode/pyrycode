//go:build e2e_realclaude

// Package realclaude hosts the e2e suite that exercises the real `claude`
// binary end-to-end. Fixture helpers compose over internal/agentrun (trust
// encoding) and internal/agentrun/jsonl (session-JSONL parsing); they do not
// reimplement either.
package realclaude

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

// Mode selects which CLI RunPyryAgentRun invokes. Zero value is invalid.
type Mode int

const (
	ModeUnset Mode = iota
	ModeAgentRun
	ModeClaudeP
)

// RunOpts mirrors the `pyry agent-run` flag surface in cmd/pyry/agent_run.go.
type RunOpts struct {
	Mode         Mode
	Workdir      string
	Prompt       string
	SystemPrompt string
	AllowedTools []string
	MaxTurns     int
	Effort       string
	Model        string
	SessionID    string
	ExtraEnv     []string
	Timeout      time.Duration
}

// RunResult holds the outcome of one RunPyryAgentRun call. Non-zero ExitCode
// is not a test failure; callers assert on it.
type RunResult struct {
	ExitCode  int
	SessionID string
	Stdout    []byte
	Stderr    []byte
}

// JSONLEntry re-exports jsonl.Event so callers need not import the parser.
type JSONLEntry = jsonl.Event

const defaultRunTimeout = 5 * time.Minute

var (
	pyryOnce sync.Once
	pyryBin  string
	pyryErr  error
)

// WithWorktree allocates a tmpdir and pins HOME to it so claude session
// writes and ReadJSONL's later os.UserHomeDir resolve to the same root.
func WithWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// RunPyryAgentRun invokes the configured CLI, captures stdout/stderr/exit,
// resolves the session ID, and returns. Build/spawn/timeout errors call
// t.Fatalf; a non-zero exit does NOT.
func RunPyryAgentRun(t *testing.T, opts RunOpts) RunResult {
	t.Helper()
	if err := validateRunOpts(&opts); err != nil {
		t.Fatalf("realclaude: RunPyryAgentRun: %v", err)
	}
	promptPath := filepath.Join(opts.Workdir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(opts.Prompt), 0o600); err != nil {
		t.Fatalf("realclaude: write prompt: %v", err)
	}
	sysPath := filepath.Join(opts.Workdir, "system.txt")
	if err := os.WriteFile(sysPath, []byte(opts.SystemPrompt), 0o600); err != nil {
		t.Fatalf("realclaude: write system prompt: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	var cmd *exec.Cmd
	switch opts.Mode {
	case ModeAgentRun:
		cmd = exec.CommandContext(ctx, ensurePyryBuilt(t), "agent-run",
			"--prompt-file="+promptPath,
			"--system-prompt-file="+sysPath,
			"--allowed-tools="+strings.Join(opts.AllowedTools, ","),
			fmt.Sprintf("--max-turns=%d", opts.MaxTurns),
			"--effort="+opts.Effort, "--model="+opts.Model,
			"--workdir="+opts.Workdir, "--output-format=stream-json")
	case ModeClaudeP:
		cmd = exec.CommandContext(ctx, "claude", "-p",
			"--output-format=stream-json",
			"--session-id="+opts.SessionID, "--model="+opts.Model)
		cmd.Stdin = bytes.NewReader([]byte(opts.Prompt))
		cmd.Dir = opts.Workdir
	}
	cmd.Env = append(os.Environ(), opts.ExtraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("realclaude: timed out after %s\nstdout:\n%s\nstderr:\n%s",
			opts.Timeout, stdout.Bytes(), stderr.Bytes())
	}
	res := RunResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
	} else if runErr != nil {
		t.Fatalf("realclaude: exec: %v\nstderr:\n%s", runErr, stderr.Bytes())
	}
	if opts.Mode == ModeAgentRun {
		res.SessionID = parseTrailerSessionID(res.Stdout)
	} else {
		res.SessionID = opts.SessionID
	}
	return res
}

// ReadJSONL opens <HOME>/.claude/projects/<encoded-workdir>/<sessionID>.jsonl
// and returns every Event the parser surfaces. Empty file → empty slice;
// missing file or parser error → t.Fatalf.
func ReadJSONL(t *testing.T, workdir, sessionID string) []JSONLEntry {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("realclaude: resolve HOME: %v", err)
	}
	enc, err := agentrun.EncodeProjectDir(workdir)
	if err != nil {
		t.Fatalf("realclaude: encode workdir: %v", err)
	}
	path := filepath.Join(home, ".claude", "projects", enc, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("realclaude: open %s: %v", path, err)
	}
	defer f.Close()
	r := jsonl.NewReader(f, jsonl.Config{})
	var out []JSONLEntry
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("realclaude: parse %s: %v", path, err)
		}
		out = append(out, ev)
	}
}

// validateRunOpts checks required fields and applies defaults. Extracted so
// the validation path is unit-testable without invoking a subprocess.
func validateRunOpts(opts *RunOpts) error {
	if opts.Mode == ModeUnset {
		return errors.New("RunOpts.Mode is required")
	}
	if opts.Workdir == "" {
		return errors.New("RunOpts.Workdir is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultRunTimeout
	}
	switch opts.Mode {
	case ModeAgentRun:
		if opts.MaxTurns <= 0 {
			return errors.New("RunOpts.MaxTurns must be > 0 for ModeAgentRun")
		}
		if opts.Effort == "" || opts.Model == "" || len(opts.AllowedTools) == 0 {
			return errors.New("RunOpts.Effort, Model, AllowedTools required for ModeAgentRun")
		}
	case ModeClaudeP:
		if opts.SessionID == "" {
			id, err := newUUIDv4()
			if err != nil {
				return fmt.Errorf("mint session id: %w", err)
			}
			opts.SessionID = id
		}
	default:
		return fmt.Errorf("unknown Mode %d", opts.Mode)
	}
	return nil
}

// parseTrailerSessionID returns the session_id of the streamjson trailer
// (`type:"result"`) emitted by `pyry agent-run`, or "" if absent.
func parseTrailerSessionID(stdout []byte) string {
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var head struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(line, &head); err == nil && head.Type == "result" {
			return head.SessionID
		}
	}
	return ""
}

// newUUIDv4 mirrors cmd/pyry/agent_run.go:newSessionUUID. Duplicated because
// cmd/pyry is package main.
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ensurePyryBuilt builds pyry once per test process; PYRY_E2E_BIN short-
// circuits to a pre-built path. Duplicated from internal/e2e/harness.go: the
// two e2e packages compile under disjoint build tags so a shared helper
// would need a third tag-set (see PROJECT-MEMORY: resist over-DRY).
func ensurePyryBuilt(t *testing.T) string {
	t.Helper()
	pyryOnce.Do(func() {
		if env := os.Getenv("PYRY_E2E_BIN"); env != "" {
			pyryBin = env
			return
		}
		dir, err := os.MkdirTemp("", "pyry-realclaude-*")
		if err != nil {
			pyryErr = err
			return
		}
		pyryBin = filepath.Join(dir, "pyry")
		out, err := exec.Command("go", "build", "-o", pyryBin,
			"github.com/pyrycode/pyrycode/cmd/pyry").CombinedOutput()
		if err != nil {
			pyryErr = fmt.Errorf("go build pyry: %w\n%s", err, out)
		}
	})
	if pyryErr != nil {
		t.Fatalf("realclaude: %v", pyryErr)
	}
	return pyryBin
}
