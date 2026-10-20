//go:build e2e_realclaude

package realclaude

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The original observer wraps our recorder rather than being bypassed by the
// recorded provider selection. Its Python program, counters and signal behavior
// stay unchanged; ordinary live roots still resolve the actual installed CLI.
func lifecycleRecordingSuggestObserver(t *testing.T, real, path string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	if c.Process.Scenario != "fallback-suggestion" || real != c.Wrapper || !filepath.IsAbs(path) || path == real {
		t.Fatal("foreign suggestion observer")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0700 {
		t.Fatal("invalid original suggestion observer")
	}
	t.Setenv("PYRY_LIFECYCLE_OBSERVED_CLI", path)
}
func lifecycleRecordingSuggestProvider(t *testing.T, c lifecycleRecordingConfig) string {
	t.Helper()
	path := os.Getenv("PYRY_LIFECYCLE_OBSERVED_CLI")
	actual, err := exec.LookPath("claude")
	if c.Process.Scenario != "fallback-suggestion" || err != nil || path != actual || path == c.Wrapper || !filepath.IsAbs(path) {
		t.Fatal("original suggestion observer is not installed")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0700 {
		t.Fatal("original suggestion observer is unavailable")
	}
	return path
}
