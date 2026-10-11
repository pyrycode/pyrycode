//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadinessEmulatorCaptureContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "queued_turn_readiness", "emulator-capture.json"))
	if err != nil || readinessHash(b) != "ef1be0314e5b101780e3707efea038c785ec5e62a22d98be09482a654f487db0" {
		t.Fatal("emulator handoff missing or changed without coupled reader")
	}
	var contract struct {
		RunID       string   `json:"run_id"`
		Selection   []string `json:"method_selection"`
		Recovery    []string `json:"recovery_paths"`
		Destination []string `json:"destination_paths"`
		Tree        string   `json:"mobile_merged_tree"`
	}
	if json.Unmarshal(b, &contract) != nil || len(contract.Selection) != 65 || len(contract.Recovery) != 8 || len(contract.Destination) != 8 || len(contract.Tree) != 40 || contract.RunID == "" {
		t.Fatal("emulator handoff incomplete")
	}
}
