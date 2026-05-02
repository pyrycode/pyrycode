//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestE2E_Startup_CorruptRegistryFailsClean(t *testing.T) {
	home, regPath := newRegistryHome(t)

	corrupt := []byte("{not valid json")
	if err := os.WriteFile(regPath, corrupt, 0o600); err != nil {
		t.Fatalf("seed corrupt registry: %v", err)
	}

	res := StartExpectingFailureIn(t, home)

	if res.ExitCode == 0 {
		t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
	}
	if !bytes.Contains(res.Stderr, []byte("registry")) {
		t.Errorf("stderr does not mention registry: %s", res.Stderr)
	}

	got, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry after failed start: %v", err)
	}
	if !bytes.Equal(got, corrupt) {
		t.Errorf("registry mutated by failed start:\nwant: %q\ngot:  %q", corrupt, got)
	}
}

func TestE2E_Startup_MissingClaudeProjectsDir(t *testing.T) {
	// os.MkdirTemp keeps the socket path under macOS's 104-byte sun_path
	// limit; t.TempDir() embeds the (long) test name and overflows.
	home, err := os.MkdirTemp("", "pyry-mp-*")
	if err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	claudeProjects := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(claudeProjects); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".claude/projects/ unexpectedly exists at %s (err=%v); test premise invalidated",
			claudeProjects, err)
	}

	h := StartIn(t, home)

	r := h.Run(t, "status")
	if r.ExitCode != 0 {
		t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	h.Stop(t)
}
