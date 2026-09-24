//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seededRegistryFile is the part of conversations.json the #2569 seed writes.
type seededRegistryFile struct {
	Conversations   []convRow         `json:"conversations"`
	WorkspaceLabels map[string]string `json:"workspace_labels"`
	Seeded          bool              `json:"seeded"`
}

// TestWorkspaceSeed_E2E_FreshHostGetsGeneral covers #2569 AC1 against a real
// daemon: a fresh host ends up with one promoted General channel in the realpath
// of $HOME/pyry-workspace/default, that cwd labelled Default workspace, and the
// marker. The session registry holds the bootstrap and General's bound session
// and nothing else — a seed run before the pool is ready would leave an orphan
// minted session per start — and General's session was not spawned.
//
// The zero-byte conversations.json opts this daemon out of the harness's
// premarkWorkspaceSeeded; the daemon loads it as an empty, unseeded registry.
func TestWorkspaceSeed_E2E_FreshHostGetsGeneral(t *testing.T) {
	home, regPath := newRegistryHome(t)
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	if err := os.WriteFile(convPath, nil, 0o600); err != nil {
		t.Fatalf("write empty conversations.json: %v", err)
	}
	StartIn(t, home)

	var file seededRegistryFile
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(convPath)
		if err == nil && json.Unmarshal(raw, &file) == nil && file.Seeded {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !file.Seeded {
		raw, _ := os.ReadFile(convPath)
		t.Fatalf("no seed marker within 5s\nfile:\n%s", raw)
	}

	wantCwd, err := filepath.EvalSymlinks(filepath.Join(home, "pyry-workspace", "default"))
	if err != nil {
		t.Fatalf("seeded folder missing: %v", err)
	}
	if len(file.Conversations) != 1 {
		t.Fatalf("conversations = %d, want 1", len(file.Conversations))
	}
	general := file.Conversations[0]
	if !general.IsPromoted || general.Name == nil || *general.Name != "General" {
		t.Errorf("row promoted=%v name=%v, want a promoted General", general.IsPromoted, general.Name)
	}
	if general.Cwd != wantCwd {
		t.Errorf("cwd = %q, want %q", general.Cwd, wantCwd)
	}
	if got := file.WorkspaceLabels[general.Cwd]; got != "Default workspace" {
		t.Errorf("label under the stored cwd = %q, want Default workspace (labels %v)", got, file.WorkspaceLabels)
	}

	sessions := readRegistry(t, regPath).Sessions
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want bootstrap + General's\nfile:\n%s", len(sessions), mustReadFile(t, regPath))
	}
	for _, s := range sessions {
		if s.Bootstrap {
			continue
		}
		if s.ID != general.CurrentSessionID {
			t.Errorf("non-bootstrap session %s is not General's bound %s — an orphan", s.ID, general.CurrentSessionID)
		}
		if s.LifecycleState == "active" {
			t.Errorf("General's session is active, want it bound without a spawn")
		}
	}
}
