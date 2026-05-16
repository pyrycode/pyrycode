//go:build e2e_realclaude

package realclaude

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

// JSONLEntry aliases jsonl.Event so callers don't import the parser package.
type JSONLEntry = jsonl.Event

// WithWorktree returns a per-test temp directory and pins $HOME to it so
// both the in-test process and any subprocess resolve os.UserHomeDir()
// to the same root.
func WithWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// ReadJSONL parses <HOME>/.claude/projects/<EncodeProjectDir(workdir)>/<sessionID>.jsonl
// and returns every event. Empty file → empty slice; open or parse
// failures call t.Fatalf with the resolved path embedded.
func ReadJSONL(t *testing.T, workdir, sessionID string) []JSONLEntry {
	t.Helper()
	f, path, err := resolveAndOpenJSONL(workdir, sessionID)
	if err != nil {
		t.Fatalf("realclaude.ReadJSONL: %v", err)
	}
	defer f.Close()
	r := jsonl.NewReader(f, jsonl.Config{})
	var events []JSONLEntry
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("realclaude.ReadJSONL: parse %s: %v", path, err)
		}
		events = append(events, ev)
	}
}

// RunOpts mirrors `pyry agent-run`'s flag surface 1:1. A rename here or in
// cmd/pyry/agent_run.go is a spec violation by definition; the argv-contract
// test pins this in code.
type RunOpts struct {
	Workdir      string
	Prompt       string
	SystemPrompt string
	AllowedTools []string
	MaxTurns     int
	Effort       string
	Model        string
	ExtraEnv     []string
	Timeout      time.Duration
	// UseTestBinaryAsFakePyry, when true, makes RunPyryAgentRun exec the
	// current test binary (os.Args[0]) as if it were pyry. The test binary's
	// TestMain is expected to recognise GO_TEST_HELPER_PROCESS=1 in the
	// child env and route into runFakePyry() instead of running the test
	// suite. Callers MUST include "GO_TEST_HELPER_PROCESS=1" in ExtraEnv
	// when setting this flag — RunPyryAgentRun enforces this to prevent
	// the 2026-05-16 fork-bomb recurrence (unbounded test-binary spawn
	// when a self-referential PYRY_E2E_BIN was inherited by tests that
	// did not opt into fake-pyry mode).
	//
	// When false (default), RunPyryAgentRun goes through ensurePyryBuilt,
	// which builds real pyry from source (or honors PYRY_E2E_BIN if set
	// and non-self-referential).
	UseTestBinaryAsFakePyry bool
}

// RunResult is the synchronous return value of RunPyryAgentRun. A non-zero
// ExitCode is normal — callers assert on it themselves.
type RunResult struct {
	ExitCode  int
	SessionID string
	Stdout    []byte
	Stderr    []byte
}

// RunPyryAgentRun invokes `pyry agent-run` against opts.Workdir, writes
// prompt.txt and system.txt into the workdir, captures all three streams,
// and parses the session_id from the first stream-json system/init line.
// Structural failures (validation, build, exec start, timeout) call
// t.Fatalf; a non-zero subprocess exit is surfaced via RunResult.ExitCode.
func RunPyryAgentRun(t *testing.T, opts RunOpts) RunResult {
	t.Helper()
	if err := validateRunOpts(opts); err != nil {
		t.Fatalf("realclaude.RunPyryAgentRun: %v", err)
	}
	var bin string
	if opts.UseTestBinaryAsFakePyry {
		// Recursion guard — see comment on UseTestBinaryAsFakePyry. When
		// the child is the test binary itself, the child's TestMain must
		// route into runFakePyry() via GO_TEST_HELPER_PROCESS=1. Without
		// it, TestMain falls through to m.Run() and re-spawns itself,
		// producing the 2026-05-16 unbounded fork-bomb.
		if !extraEnvHasHelperProcessFlag(opts.ExtraEnv) {
			t.Fatalf("realclaude.RunPyryAgentRun: UseTestBinaryAsFakePyry=true requires " +
				"\"GO_TEST_HELPER_PROCESS=1\" in ExtraEnv; without it the child test binary " +
				"falls through to m.Run() and recurses unboundedly (fork-bomb, 2026-05-16)")
		}
		bin = os.Args[0]
	} else {
		bin = ensurePyryBuilt(t)
	}
	promptPath := filepath.Join(opts.Workdir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(opts.Prompt), 0o600); err != nil {
		t.Fatalf("realclaude.RunPyryAgentRun: write %s: %v", promptPath, err)
	}
	systemPath := filepath.Join(opts.Workdir, "system.txt")
	if err := os.WriteFile(systemPath, []byte(opts.SystemPrompt), 0o600); err != nil {
		t.Fatalf("realclaude.RunPyryAgentRun: write %s: %v", systemPath, err)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"agent-run",
		"--prompt-file="+promptPath,
		"--system-prompt-file="+systemPath,
		"--allowed-tools="+strings.Join(opts.AllowedTools, ","),
		"--max-turns="+strconv.Itoa(opts.MaxTurns),
		"--effort="+opts.Effort,
		"--model="+opts.Model,
		"--workdir="+opts.Workdir,
		"--output-format=stream-json",
	)
	cmd.Env = append(os.Environ(), opts.ExtraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("realclaude.RunPyryAgentRun: timed out after %s\nstdout:\n%s\nstderr:\n%s",
			timeout, stdout.Bytes(), stderr.Bytes())
	}
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		t.Fatalf("realclaude.RunPyryAgentRun: exec: %v", runErr)
	}
	return RunResult{
		ExitCode:  cmd.ProcessState.ExitCode(),
		SessionID: parseInitSessionID(stdout.Bytes()),
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
	}
}

// extraEnvHasHelperProcessFlag returns true iff ExtraEnv contains a
// literal "GO_TEST_HELPER_PROCESS=1" entry. This is the discriminator
// that lets the child test binary route into runFakePyry() instead of
// re-running the test suite (and recursing).
func extraEnvHasHelperProcessFlag(env []string) bool {
	for _, kv := range env {
		if kv == "GO_TEST_HELPER_PROCESS=1" {
			return true
		}
	}
	return false
}

// validateRunOpts returns the first required-field violation. Exposed as a
// returned error so tests can assert on it without intercepting t.Fatalf.
// The --effort enum is NOT re-validated here; pyry rejects bad values and
// the helper surfaces that as a non-zero exit.
func validateRunOpts(opts RunOpts) error {
	switch {
	case opts.Workdir == "":
		return errors.New("RunOpts: Workdir: required")
	case opts.Prompt == "":
		return errors.New("RunOpts: Prompt: required")
	case opts.SystemPrompt == "":
		return errors.New("RunOpts: SystemPrompt: required")
	case len(opts.AllowedTools) == 0:
		return errors.New("RunOpts: AllowedTools: required, non-empty")
	case opts.MaxTurns <= 0:
		return fmt.Errorf("RunOpts: MaxTurns: must be > 0 (got %d)", opts.MaxTurns)
	case opts.Effort == "":
		return errors.New("RunOpts: Effort: required")
	case opts.Model == "":
		return errors.New("RunOpts: Model: required")
	}
	return nil
}

var (
	pyryBinOnce sync.Once
	pyryBinPath string
	pyryBinErr  error
)

// ensurePyryBuilt builds pyry once per test process. PYRY_E2E_BIN short-
// circuits to a pre-built binary. Duplicates internal/e2e/harness.go
// deliberately — disjoint build tags (e2e vs e2e_realclaude) block reuse.
func ensurePyryBuilt(t *testing.T) string {
	t.Helper()
	pyryBinOnce.Do(func() {
		if env := os.Getenv("PYRY_E2E_BIN"); env != "" {
			// Defense-in-depth: refuse a self-referential PYRY_E2E_BIN.
			// The 2026-05-16 fork-bomb was caused by TestMain auto-setting
			// PYRY_E2E_BIN=os.Args[0] (the test binary) and tests that
			// wanted real pyry inheriting the self-ref — they invoked the
			// test binary as "pyry", whose TestMain fell through to
			// m.Run() and recursed unboundedly. Use RunOpts.UseTestBinaryAsFakePyry
			// instead for explicit self-exec intent.
			envAbs, err1 := filepath.Abs(env)
			selfAbs, err2 := filepath.Abs(os.Args[0])
			if err1 == nil && err2 == nil && envAbs == selfAbs {
				pyryBinErr = fmt.Errorf("PYRY_E2E_BIN=%s points at the test binary itself; "+
					"refusing to use as pyry — set RunOpts.UseTestBinaryAsFakePyry=true "+
					"(plus GO_TEST_HELPER_PROCESS=1 in ExtraEnv) for explicit self-exec intent", env)
				return
			}
			pyryBinPath = env
			return
		}
		dir, err := os.MkdirTemp("", "pyry-realclaude-*")
		if err != nil {
			pyryBinErr = err
			return
		}
		pyryBinPath = filepath.Join(dir, "pyry")
		cmd := exec.Command("go", "build", "-o", pyryBinPath, "github.com/pyrycode/pyrycode/cmd/pyry")
		out, err := cmd.CombinedOutput()
		if err != nil {
			pyryBinErr = fmt.Errorf("go build pyry: %w\n%s", err, out)
		}
	})
	if pyryBinErr != nil {
		t.Fatalf("realclaude: %v", pyryBinErr)
	}
	return pyryBinPath
}

// parseInitSessionID scans stdout line-by-line and returns the session_id
// from the first decoded {"type":"system","subtype":"init",...} envelope
// with a non-empty session_id. Returns "" if none is found. Non-JSON and
// non-init lines are skipped silently.
func parseInitSessionID(stdout []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	for scanner.Scan() {
		var env struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue
		}
		if env.Type == "system" && env.Subtype == "init" && env.SessionID != "" {
			return env.SessionID
		}
	}
	return ""
}

// Split out so the missing-file path is testable as a returned error.
func resolveAndOpenJSONL(workdir, sessionID string) (*os.File, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("resolve HOME: %w", err)
	}
	enc, err := agentrun.EncodeProjectDir(workdir)
	if err != nil {
		return nil, "", fmt.Errorf("encode workdir %q: %w", workdir, err)
	}
	path := filepath.Join(home, ".claude", "projects", enc, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, path, fmt.Errorf("open %s: %w", path, err)
	}
	return f, path, nil
}
