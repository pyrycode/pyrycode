//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// tempHome allocates a short-pathed temp dir for use as the daemon's $HOME.
// os.MkdirTemp (not t.TempDir) keeps <home>/pyry.sock under macOS's 104-byte
// sun_path limit; cleanup is registered with t.Cleanup.
func tempHome(t *testing.T, prefix string) string {
	t.Helper()
	home, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

// trustAccepted reads <home>/.claude.json and reports whether
// projects[key].hasTrustDialogAccepted is true.
func trustAccepted(t *testing.T, home, key string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	var root struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode .claude.json: %v", err)
	}
	entry, ok := root.Projects[key]
	if !ok {
		t.Fatalf("projects has no entry for %q; keys=%v", key, keysOf(root.Projects))
	}
	return entry.HasTrustDialogAccepted
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestE2E_Supervisor_PreMarksWorkdirTrusted covers AC-1/AC-2 on the real serve
// path: the daemon pre-marks its workdir's realpath trusted in ~/.claude.json
// before it becomes ready, so the supervised claude never wedges on the
// workspace-trust modal. The harness default (-pyry-workdir=home) is the
// confined workdir; its realpath is the key claude itself would resolve.
func TestE2E_Supervisor_PreMarksWorkdirTrusted(t *testing.T) {
	home := tempHome(t, "pyry-tr-")

	h := StartIn(t, home)
	defer h.Stop(t)

	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}
	if !trustAccepted(t, home, realHome) {
		t.Errorf("projects[%q].hasTrustDialogAccepted != true after ready", realHome)
	}
}

// TestE2E_Supervisor_MalformedClaudeJsonFailsFast covers AC-4: a malformed
// ~/.claude.json makes the trust pre-mark fail, and the daemon exits loudly at
// startup (naming the operation) rather than spinning a silent restart loop.
// The malformed file is left untouched (the mark is atomic: it fails before
// writing).
func TestE2E_Supervisor_MalformedClaudeJsonFailsFast(t *testing.T) {
	home := tempHome(t, "pyry-mj-")

	malformed := []byte("{not valid json")
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), malformed, 0o600); err != nil {
		t.Fatalf("seed malformed .claude.json: %v", err)
	}

	res := StartExpectingFailureIn(t, home)

	if res.ExitCode == 0 {
		t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
	}
	if !bytes.Contains(res.Stderr, []byte("mark workdir trusted")) {
		t.Errorf("stderr does not name the trust-mark step: %s", res.Stderr)
	}

	got, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json after failed start: %v", err)
	}
	if !bytes.Equal(got, malformed) {
		t.Errorf("malformed .claude.json mutated by failed start:\nwant: %q\ngot:  %q", malformed, got)
	}
}

// TestE2E_Supervisor_WorkdirOutsideHomeRejected covers AC-3: a workdir
// resolving outside $HOME is rejected at startup — never trusted, never
// launched. The error names the $HOME boundary; no ~/.claude.json is written.
func TestE2E_Supervisor_WorkdirOutsideHomeRejected(t *testing.T) {
	home := tempHome(t, "pyry-oh-")
	outside := tempHome(t, "pyry-out-") // a real dir, but not under home

	res := StartExpectingFailureIn(t, home, "-pyry-workdir="+outside)

	if res.ExitCode == 0 {
		t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
	}
	if !bytes.Contains(res.Stderr, []byte("outside the home directory")) {
		t.Errorf("stderr does not explain the $HOME boundary: %s", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
		t.Errorf("rejected workdir still wrote ~/.claude.json")
	}
}
