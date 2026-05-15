//go:build e2e_realclaude

package realclaude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
