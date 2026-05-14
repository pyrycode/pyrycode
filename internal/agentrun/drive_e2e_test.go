package agentrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDriveE2E_RealClaudeProducesJSONL drives the real `claude` binary
// through a single user-turn and asserts that claude's session JSONL file
// materialises on disk within a budget, then cancels and confirms clean
// teardown within WaitDelay.
//
// Skipped under `go test -short` and when `claude` is not on PATH — this
// is a true integration test, not a unit test. Run locally with:
//
//	go test -race ./internal/agentrun/ -run TestDriveE2E_RealClaudeProducesJSONL
func TestDriveE2E_RealClaudeProducesJSONL(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("e2e: claude not on PATH: %v", err)
	}

	workdir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	// Trust the workdir so claude does not block on the workspace-trust
	// dialog. The defensive Enter inside Drive is best-effort; the real
	// belt is MarkWorkdirTrusted, just as agent-run does in production.
	if err := MarkWorkdirTrusted(home, workdir); err != nil {
		t.Fatalf("mark trusted: %v", err)
	}
	settingsPath, err := WriteSettings(workdir, []string{"Read"})
	if err != nil {
		t.Fatalf("write settings: %v", err)
	}

	// Claude encodes the realpath workdir into a project dir under
	// ~/.claude/projects, replacing '/', '.', and '_' with '-'.
	// (Empirically: '_' also gets converted — broader than the
	// lessons.md note which mentions only '/' and '.'.)
	encoded := resolved
	for _, ch := range []string{"/", ".", "_"} {
		encoded = strings.ReplaceAll(encoded, ch, "-")
	}
	jsonlDir := filepath.Join(home, ".claude", "projects", encoded)

	cfg := DriveConfig{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		Args: []string{
			"--settings", settingsPath,
			"--permission-mode", "default",
			"--model", "sonnet",
		},
		PromptBytes: []byte("Say hi in one short word. No tools, just text."),
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	driveDone := make(chan error, 1)
	go func() { driveDone <- Drive(ctx, cfg) }()

	// Poll for the encoded JSONL directory + at least one .jsonl file.
	deadline := time.Now().Add(30 * time.Second)
	var foundJSONL bool
	for time.Now().Before(deadline) {
		if entries, err := os.ReadDir(jsonlDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
					foundJSONL = true
					break
				}
			}
		}
		if foundJSONL {
			break
		}
		select {
		case err := <-driveDone:
			t.Fatalf("Drive returned before JSONL appeared: %v", err)
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !foundJSONL {
		t.Fatalf("no .jsonl file under %s within 30s", jsonlDir)
	}

	// Trigger teardown and assert Drive returns within the WaitDelay
	// budget (5s) + slack.
	cancel()
	select {
	case err := <-driveDone:
		if err != nil {
			t.Logf("Drive returned %v (acceptable on operator cancel)", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("Drive did not return within 7s of cancel")
	}
}
