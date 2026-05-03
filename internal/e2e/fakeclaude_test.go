//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// uuidV4Re matches the canonical lowercase v4 UUID stem fake-claude emits
// for rotated jsonl files.
var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestE2E_StartRotation_PrimitiveWiresFakeClaude exercises the harness
// primitive end-to-end without touching pyry's rotation watcher: spawn
// pyry with fake-claude as the supervised child, observe the initial
// jsonl appear, drop the trigger, and observe a fresh <uuid>.jsonl
// appear in the same directory.
func TestE2E_StartRotation_PrimitiveWiresFakeClaude(t *testing.T) {
	// Short-prefix MkdirTemp keeps the pyry.sock path under macOS's 104-byte
	// sun_path limit; t.TempDir() embeds the (long) test name and overflows.
	home, err := os.MkdirTemp("", "pyry-fc-*")
	if err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	sessionsDir := filepath.Join(home, "fakesessions")
	initialUUID := "11111111-1111-4111-8111-111111111111"
	trigger := filepath.Join(home, "rotate.trigger")

	h := StartRotation(t, home, sessionsDir, initialUUID, trigger)

	if h.ClaudeSessionsDir != sessionsDir {
		t.Fatalf("ClaudeSessionsDir = %q, want %q", h.ClaudeSessionsDir, sessionsDir)
	}

	initialPath := filepath.Join(sessionsDir, initialUUID+".jsonl")
	waitForFile(t, initialPath, 5*time.Second, h.Stderr.String)

	if err := os.WriteFile(trigger, nil, 0o600); err != nil {
		t.Fatalf("write trigger: %v", err)
	}

	rotated := waitForRotatedJSONL(t, sessionsDir, initialUUID, 5*time.Second, h.Stderr.String)

	// Sanity: the rotated jsonl is non-empty (fake-claude writes "{}\n" to
	// each opened fd). Combined with #122's close-OLD-before-open-NEW order,
	// this implies the initial fd is no longer being written to.
	info, err := os.Stat(rotated)
	if err != nil {
		t.Fatalf("stat rotated %s: %v", rotated, err)
	}
	if info.Size() == 0 {
		t.Fatalf("rotated jsonl %s is empty; fake-claude payload missing", rotated)
	}
}

// waitForFile polls path with a deadline and exits via t.Fatalf with
// stderrFn() included on miss.
func waitForFile(t *testing.T, path string, deadline time.Duration, stderrFn func() string) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("file %s did not appear within %s\nstderr:\n%s", path, deadline, stderrFn())
}

// waitForRotatedJSONL polls sessionsDir for a *.jsonl whose stem is a v4
// UUID and is not initialUUID. Returns the absolute path on success.
func waitForRotatedJSONL(t *testing.T, sessionsDir, initialUUID string, deadline time.Duration, stderrFn func() string) string {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		entries, err := os.ReadDir(sessionsDir)
		if err == nil {
			for _, e := range entries {
				name := e.Name()
				if !strings.HasSuffix(name, ".jsonl") {
					continue
				}
				stem := strings.TrimSuffix(name, ".jsonl")
				if stem == initialUUID {
					continue
				}
				if uuidV4Re.MatchString(stem) {
					return filepath.Join(sessionsDir, name)
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no rotated <uuid>.jsonl appeared in %s within %s\nstderr:\n%s",
		sessionsDir, deadline, stderrFn())
	return ""
}
