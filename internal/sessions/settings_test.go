package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteMCPSettings_ShapeAndContent (AC #1/#2): the interactive-spawn settings
// writer emits exactly enough to suppress claude's MCP-enablement modal
// (enableAllProjectMcpServers:true) and NOTHING that changes the interactive
// session's permission posture — no permissions/allow/deny/defaultMode keys.
func TestWriteMCPSettings_ShapeAndContent(t *testing.T) {
	t.Parallel()

	path, err := writeMCPSettings()
	if err != nil {
		t.Fatalf("writeMCPSettings: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	if !filepath.IsAbs(path) {
		t.Errorf("writeMCPSettings path = %q, want absolute", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}

	// AC #1: enableAllProjectMcpServers is present and true.
	var got struct {
		EnableAllProjectMcpServers        bool `json:"enableAllProjectMcpServers"`
		SkipDangerousModePermissionPrompt bool `json:"skipDangerousModePermissionPrompt"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if !got.EnableAllProjectMcpServers {
		t.Errorf("enableAllProjectMcpServers = false, want true (JSON: %s)", raw)
	}

	// AC #1b: skipDangerousModePermissionPrompt is present and true, so a spawn
	// that carries --dangerously-skip-permissions (operator-enabled YOLO) does not
	// wedge on claude 2.1.199's Bypass Permissions warning dialog ("1. No, exit" /
	// "2. Yes, I accept" selection). The field is inert when the spawn does not
	// request bypass mode, so it is safe to write unconditionally.
	if !got.SkipDangerousModePermissionPrompt {
		t.Errorf("skipDangerousModePermissionPrompt = false, want true (JSON: %s)", raw)
	}

	// AC #2: no permission posture. The agent-run deny-default writer stamps a
	// "permissions" object with "defaultMode":"dontAsk"; none of those tokens may
	// appear here or normal interactive tool prompts break.
	for _, forbidden := range []string{"permissions", "defaultMode", "dontAsk", "allow", "deny"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("settings JSON %q must not contain %q (AC #2: no permission posture change)", raw, forbidden)
		}
	}
}

// TestWriteMCPSettings_DistinctPaths: each call writes its own file. The
// per-session discipline (one file per session, removed at that session's
// teardown) relies on distinct paths so removing one never orphans another.
func TestWriteMCPSettings_DistinctPaths(t *testing.T) {
	t.Parallel()

	a, err := writeMCPSettings()
	if err != nil {
		t.Fatalf("writeMCPSettings (a): %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(a) })

	b, err := writeMCPSettings()
	if err != nil {
		t.Fatalf("writeMCPSettings (b): %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(b) })

	if a == b {
		t.Errorf("writeMCPSettings returned the same path twice: %q", a)
	}
}
