//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// runVerbIn is runVerb with a working directory. `pyry channel new` sends
// os.Getwd() as the workspace to create, so the directory the CLI runs FROM is
// the whole input under test — and Harness.Run cannot express it: runVerb never
// sets cmd.Dir, so the child inherits the test process's cwd, which is this
// package's source directory and lies outside the temp $HOME the daemon
// confines to.
//
// Kept here rather than added to harness.go on purpose: this is the only verb
// whose input is its own cwd, so a general RunIn on the harness would be a
// production-file change made for one caller.
func runVerbIn(t *testing.T, socket, home, dir, verb string, args ...string) RunResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	full := append([]string{verb, "-pyry-socket=" + socket}, args...)
	cmd := exec.CommandContext(ctx, binPath, full...)
	cmd.Env = childEnv(home)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("e2e: pyry %s timed out\nstdout:\n%s\nstderr:\n%s", verb, stdout.String(), stderr.String())
	}
	exitCode := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("e2e: pyry %s exec failed: %v", verb, err)
	}
	return RunResult{ExitCode: exitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

// convRow mirrors the fields of an on-disk conversations.json entry this suite
// asserts on. Declared locally rather than importing internal/conversations so
// the test reads the FILE's contract — a field renamed on the struct without a
// tag change must not silently pass here.
type convRow struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	Cwd              string  `json:"cwd"`
	CurrentSessionID string  `json:"current_session_id"`
	IsPromoted       bool    `json:"is_promoted"`
}

// waitForConversation polls the instance's conversations.json until a row with
// the given id appears, and returns it. The daemon persists eagerly inside the
// verb, so the row should be there by the time stdout is flushed; the poll
// covers filesystem visibility only.
func waitForConversation(t *testing.T, home, id string) convRow {
	t.Helper()
	path := filepath.Join(home, ".pyry", "test", "conversations.json")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			var file struct {
				Conversations []convRow `json:"conversations"`
			}
			if json.Unmarshal(raw, &file) == nil {
				for _, c := range file.Conversations {
					if c.ID == id {
						return c
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	raw, _ := os.ReadFile(path)
	t.Fatalf("conversation %s not in %s within 3s\nfile:\n%s", id, path, raw)
	return convRow{}
}

// projectDir creates a project folder under the daemon's $HOME and returns both
// the path and its symlink-resolved form.
//
// Both, because they differ where it matters: on macOS the temp home lives
// under /var/folders/… and /var is a symlink, so the daemon — which stores the
// canonical realpath — records something the test never handed it. Asserting
// against the un-resolved path passes on Linux and fails here.
func projectDir(t *testing.T, home, name string) (dir, real string) {
	t.Helper()
	dir = filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return dir, real
}

// TestChannelNew_E2E_CreatesChannelInCwd drives `pyry channel new` against a
// real daemon over its control socket and asserts the promoted row lands in
// conversations.json with the resolved cwd, the directory's base name, and a
// bound session id.
//
// This is the fake-daemon tier (fakeclaude via writeSleepClaude, no
// credentials, no network), so `make check` covers the verb.
func TestChannelNew_E2E_CreatesChannelInCwd(t *testing.T) {
	home, _ := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	proj, projReal := projectDir(t, home, "my-project")

	r := runVerbIn(t, h.SocketPath, home, proj, "channel", "new")
	if r.ExitCode != 0 {
		t.Fatalf("pyry channel new exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	if !canonicalUUIDLine.Match(r.Stdout) {
		t.Fatalf("stdout = %q, want a single canonical UUID + newline", r.Stdout)
	}
	id := string(bytes.TrimRight(r.Stdout, "\n"))

	got := waitForConversation(t, home, id)
	if !got.IsPromoted {
		t.Error("row is_promoted = false, want true — this verb creates a channel")
	}
	if got.Cwd != projReal {
		t.Errorf("row cwd = %q, want the resolved real path %q", got.Cwd, projReal)
	}
	if got.Name == nil || *got.Name != "my-project" {
		t.Errorf("row name = %v, want the directory's base name %q", got.Name, "my-project")
	}
	if got.CurrentSessionID == "" {
		t.Error("row current_session_id is empty, want a bound session as a client-created row has")
	}
}

// TestChannelNew_E2E_NameOverride pins that --name overrides the base-name
// default, matching `pyry sessions new`'s flag.
func TestChannelNew_E2E_NameOverride(t *testing.T) {
	home, _ := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	proj, projReal := projectDir(t, home, "api-server")

	r := runVerbIn(t, h.SocketPath, home, proj, "channel", "new", "--name", "Backend Work")
	if r.ExitCode != 0 {
		t.Fatalf("pyry channel new --name exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	id := string(bytes.TrimRight(r.Stdout, "\n"))

	got := waitForConversation(t, home, id)
	if got.Name == nil || *got.Name != "Backend Work" {
		t.Errorf("row name = %v, want the supplied label %q", got.Name, "Backend Work")
	}
	if got.Cwd != projReal {
		t.Errorf("row cwd = %q, want %q — --name must not affect the workspace", got.Cwd, projReal)
	}
}

// TestChannelNew_E2E_RefusesDirOutsideHome pins the acceptance criteria's
// refusal path against a real daemon: a directory outside the daemon's $HOME is
// rejected with a one-line stderr message that echoes neither the requested nor
// the resolved path, exit 1, and nothing persisted.
//
// The daemon's $HOME is the temp home; this test runs the CLI from a SEPARATE
// temp dir, which is a genuine escape from the daemon's perspective while still
// being a real, readable directory — so the refusal under test is the $HOME
// bound, not a missing path (which confineWorkdirToHomeCreating would create
// rather than refuse).
func TestChannelNew_E2E_RefusesDirOutsideHome(t *testing.T) {
	home, _ := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	outside, err := os.MkdirTemp("", "pyry-outside-*")
	if err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	outsideReal, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("EvalSymlinks(outside): %v", err)
	}
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}

	r := runVerbIn(t, h.SocketPath, home, outside, "channel", "new")
	if r.ExitCode != 1 {
		t.Fatalf("exit=%d, want 1\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	if len(bytes.TrimSpace(r.Stdout)) != 0 {
		t.Errorf("stdout = %q, want empty on the refusal path", r.Stdout)
	}
	if n := bytes.Count(bytes.TrimSpace(r.Stderr), []byte("\n")); n != 0 {
		t.Errorf("stderr spans %d newlines, want a single line:\n%s", n+1, r.Stderr)
	}
	for _, leak := range [][]byte{
		[]byte(outside), []byte(outsideReal), []byte(homeReal),
		[]byte("panic"), []byte("goroutine "),
	} {
		if bytes.Contains(r.Stderr, leak) {
			t.Errorf("stderr leaks %q:\n%s", leak, r.Stderr)
		}
	}

	// Nothing persisted: no conversations.json, or one with no rows.
	path := filepath.Join(home, ".pyry", "test", "conversations.json")
	if raw, err := os.ReadFile(path); err == nil {
		var file struct {
			Conversations []convRow `json:"conversations"`
		}
		if json.Unmarshal(raw, &file) == nil && len(file.Conversations) != 0 {
			t.Errorf("registry gained %d row(s) after a refusal:\n%s", len(file.Conversations), raw)
		}
	}
}
