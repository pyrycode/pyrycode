//go:build e2e

package e2e

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

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem.
var uuidStemPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestE2E_RotationWatcher_DetectsClear drives a real pyry daemon through one
// /clear-shaped JSONL rotation and asserts the registry's tracked id follows.
// Production code (watcher, real platform probe, RotateID, registry write
// path, reconcile) is exercised exactly as it ships; only the supervised
// child is replaced by the fakeclaude test binary.
//
// AC#3 from the ticket (independent observation via `pyry list`) is
// intentionally skipped: the verb does not exist (cmd/pyry/main.go has no
// `list` dispatch) and adding it is explicitly out of scope. The on-disk
// registry assertions below already exercise every link in the chain.
func TestE2E_RotationWatcher_DetectsClear(t *testing.T) {
	home, regPath := newRegistryHome(t)

	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}

	const initialUUID = "11111111-1111-4111-8111-111111111111"
	initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
	// Pre-create the initial jsonl BEFORE pyry starts so reconcileBootstrapOnNew
	// (synchronous inside Pool.New, before the control socket listens) sees it
	// as the most-recent jsonl and rotates the bootstrap entry to initialUUID
	// + persists. Without this, fakeclaude's first open races pyry startup.
	if err := os.WriteFile(initialJSONL, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write initial jsonl: %v", err)
	}

	trigger := filepath.Join(home, "rotate.trigger")

	h := StartRotation(t, home, sessionsDir, initialUUID, trigger)
	_ = h // teardown via t.Cleanup

	pre := waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)

	if err := os.WriteFile(trigger, nil, 0o600); err != nil {
		t.Fatalf("write trigger: %v", err)
	}

	post := waitForBootstrapIDChange(t, regPath, initialUUID, 5*time.Second)

	if !uuidStemPattern.MatchString(post.ID) {
		t.Errorf("post-rotation id %q does not match UUIDv4 stem pattern", post.ID)
	}
	if !post.LastActiveAt.After(pre.LastActiveAt) {
		t.Errorf("last_active_at did not advance: pre=%s post=%s",
			pre.LastActiveAt.Format(time.RFC3339Nano),
			post.LastActiveAt.Format(time.RFC3339Nano))
	}

	// Stable-state check: no background path reverts the pointer. 200ms is
	// ~2× the watcher's typical fsnotify-to-save latency; a spurious second
	// rotation would surface within this window.
	time.Sleep(200 * time.Millisecond)
	after := readBootstrap(t, regPath)
	if after.ID != post.ID {
		t.Errorf("bootstrap id reverted: post=%s after=%s\nfile:\n%s",
			post.ID, after.ID, mustReadFile(t, regPath))
	}
}

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

// waitForBootstrapIDChange polls regPath until the bootstrap entry's id is
// non-empty and != avoidID, or timeout elapses.
func waitForBootstrapIDChange(t *testing.T, regPath, avoidID string, timeout time.Duration) registryEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if e, ok := readBootstrapIfPresent(regPath); ok && e.ID != "" && e.ID != avoidID {
			return e
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("bootstrap id did not change away from %q within %s\nfile:\n%s",
		avoidID, timeout, mustReadFile(t, regPath))
	return registryEntry{}
}

// readBootstrap reads regPath and returns the bootstrap entry. Fatals if no
// bootstrap entry exists.
func readBootstrap(t *testing.T, regPath string) registryEntry {
	t.Helper()
	e, ok := readBootstrapIfPresent(regPath)
	if !ok {
		t.Fatalf("no bootstrap entry in registry\nfile:\n%s", mustReadFile(t, regPath))
	}
	return e
}

// readBootstrapIfPresent returns (entry, true) if the registry file is
// readable, parseable, and contains a bootstrap entry. Returns (_, false) on
// any of: file missing, parse error, no bootstrap entry. The poll helpers
// treat all three as "keep polling" rather than fataling — the file is
// written atomically by RotateID's saveLocked, but it may not exist yet at
// the very first poll iteration.
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
