//go:build e2e_realclaude

package realclaude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadinessRejectBeforePortablePublication(t *testing.T) {
	c, control := testReadinessEmulatorCapture(t)
	if err := readinessExportRuntime(filepath.Join(t.TempDir(), "control"), c.RunID, control, nil); err != nil {
		t.Fatalf("valid control rejected: %v", err)
	}
	for _, scenario := range []string{"model", "posture", "workspace role", "readback provenance", "source provenance", "clock provenance", "conclusion", "reply owner", "missing resources"} {
		t.Run(scenario, func(t *testing.T) {
			c, r := testReadinessEmulatorCapture(t)
			p := r.Setup["emulator_evidence"].(readinessEmulatorProvenance)
			private := "/tmp/private-configuration-3071"
			switch scenario {
			case "model":
				p.Settings[0].Model = private
			case "posture":
				p.Settings[0].PermissionMode = private
			case "workspace role":
				p.Settings[0].WorkspaceRole = private
			case "readback provenance":
				p.Settings[0].Provenance = private
			case "source provenance":
				p.Source = private
			case "clock provenance":
				p.ClockComparison = private
			case "conclusion":
				p.HostConclusion = private
			case "reply owner":
				r.Requests[1].OwnerOrdinal = 2
			case "missing resources":
				r.Resources = nil
			}
			r.Setup["emulator_evidence"] = p
			root := t.TempDir()
			for _, name := range []string{"destination", "recovery"} {
				dir := filepath.Join(root, name, c.RunID)
				// Configuration labels are intentionally absent from the substring deny
				// list: the typed semantic reader must reject them before publication.
				if err := readinessExportRuntime(dir, c.RunID, r, nil); err == nil {
					t.Fatalf("invalid %s accepted for %s", scenario, name)
				}
				if _, err := os.Lstat(dir); !os.IsNotExist(err) {
					t.Fatalf("rejected candidate published a %s run", name)
				}
			}
		})
	}
}

func TestReadinessPublicationPreservesExistingRun(t *testing.T) {
	c, r := testReadinessEmulatorCapture(t)
	dir := filepath.Join(t.TempDir(), c.RunID)
	if err := readinessExportRuntime(dir, c.RunID, r, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "setup.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.ClaudeVersion = "replacement"
	if err := readinessExportRuntime(dir, c.RunID, r, nil); err == nil {
		t.Fatal("existing evidence overwritten")
	}
	after, err := os.ReadFile(filepath.Join(dir, "setup.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("existing evidence changed")
	}
	if err := readinessValidateRuntime(dir); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessPublicationComplete(t *testing.T) {
	c, r := testReadinessEmulatorCapture(t)
	root := t.TempDir()
	dir := filepath.Join(root, c.RunID)
	if err := readinessExportRuntime(dir, c.RunID, r, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != c.RunID {
		t.Fatal("publication left staging directories")
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 8 {
		t.Fatal("publication is incomplete")
	}
	if err := readinessValidateRuntime(dir); err != nil {
		t.Fatal(err)
	}
}
