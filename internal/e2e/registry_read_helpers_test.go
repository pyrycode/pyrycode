//go:build e2e

package e2e

// Registry-reading helpers shared by the interactive-stream e2e tests: the
// claude sessions-dir path derivation and the sessions.json bootstrap readers.
// These arrived with TestE2E_RotationWatcher_DetectsClear, whose file this was;
// #2137 retired the rotation watcher and deleted that test, and the helpers
// outlived it because four sibling tests in this package drive them.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// encodeWorkdir mirrors internal/sessions.encodeWorkdir (unexported). Replaces
// both '/' and '.' with '-'. The naive '/'→'-' rule is wrong: claude encodes
// dots too, so a worktree under "/Users/.../.pyrycode-worktrees" produces a
// doubled dash. Keep this in sync with the production helper.
func encodeWorkdir(workdir string) string {
	if workdir == "" {
		return ""
	}
	r := strings.NewReplacer("/", "-", ".", "-")
	return r.Replace(workdir)
}

// claudeSessionsDir mirrors sessions.DefaultClaudeSessionsDir for a daemon whose
// $HOME is home and whose workdir is also home (the harness default,
// -pyry-workdir=home): the base is home, and the workdir is symlink-RESOLVED
// before encoding. The resolution matches production (#989): claude writes its
// transcript under its resolved cwd, so the daemon resolves too, and on macOS a
// t.TempDir sits under /var/folders, a symlink to /private/var. Encoding the
// unresolved home here would point the fake's transcript at a folder the daemon
// never watches, and every interactive-stream test would time out. Resolution
// failure falls back to the literal path, the same shape the daemon uses.
func claudeSessionsDir(home string) string {
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		resolved = home
	}
	return filepath.Join(home, ".claude", "projects", encodeWorkdir(resolved))
}

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem.
var uuidStemPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// waitForBootstrapID polls regPath until the bootstrap entry's id equals want
// or timeout elapses. Fatals with the latest registry contents on timeout.
func waitForBootstrapID(t *testing.T, regPath, want string, timeout time.Duration) registryEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if e, ok := readBootstrapIfPresent(regPath); ok && e.ID == want {
			return e
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("bootstrap id did not reach %q within %s\nfile:\n%s",
		want, timeout, mustReadFile(t, regPath))
	return registryEntry{}
}

// readBootstrapIfPresent returns (entry, true) if the registry file is
// readable, parseable, and contains a bootstrap entry. Returns (_, false) on
// any of: file missing, parse error, no bootstrap entry. The poll helpers
// treat all three as "keep polling" rather than fataling — the file is
// written atomically by the pool's saveLocked, whichever path drove the write,
// but it may not exist yet at the very first poll iteration.
func readBootstrapIfPresent(regPath string) (registryEntry, bool) {
	data, err := os.ReadFile(regPath)
	if err != nil {
		return registryEntry{}, false
	}
	var reg registryFile
	if err := json.Unmarshal(data, &reg); err != nil {
		return registryEntry{}, false
	}
	for _, e := range reg.Sessions {
		if e.Bootstrap {
			return e, true
		}
	}
	return registryEntry{}, false
}
