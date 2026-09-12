//go:build e2e_liverelay

package liverelay

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLiveRelayPairingUsesFixture(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source test path")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "liverelay_test.go"))
	if err != nil {
		t.Fatalf("read liverelay source: %v", err)
	}

	text := string(source)
	if !strings.Contains(text, "paireddevice.Setup(") {
		t.Error("live-relay setup does not call paireddevice.Setup")
	}
	for _, forbidden := range []string{"runPyryPair", "decodePairPayload", "pair.Decode("} {
		if strings.Contains(text, forbidden) {
			t.Errorf("live-relay source retains CLI pairing dependency %q", forbidden)
		}
	}
}
