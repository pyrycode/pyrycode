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
	if err != nil {
		t.Fatal(err)
	}
	var paths struct {
		Recovery    []string `json:"recovery_paths"`
		Destination []string `json:"destination_paths"`
	}
	c, err := readinessReadEmulatorContract()
	if err != nil {
		t.Fatal(err)
	}
	selection, _ := json.Marshal(c.Selection)
	if json.Unmarshal(b, &paths) != nil || len(paths.Recovery) != 8 || len(paths.Destination) != 8 || c.MobileTree != "8564ff1d87ee16944c1b99cd577449bd96b1534b" || c.TestHash != "f6bd70c18275c04bc83d33a66202171f53b86ccc7967a9f695ec59614652bfc8" || readinessHash(selection) != "67ab9afd7ecc1a56688085b00eca81ff433d62ab44553014433e87d907be6416" {
		t.Fatal("emulator source, selection or recovery contract changed")
	}
	for name, hash := range c.Support {
		b, err := os.ReadFile(name)
		if err != nil || readinessHash(b) != hash {
			t.Fatalf("prepared capture support changed: %s", name)
		}
	}
}
