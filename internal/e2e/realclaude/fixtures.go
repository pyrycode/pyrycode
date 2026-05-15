//go:build e2e_realclaude

package realclaude

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

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
