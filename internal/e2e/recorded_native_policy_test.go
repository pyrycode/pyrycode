//go:build e2e

package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordedNativeInvocationPolicy(t *testing.T) {
	for _, mode := range []string{"capture", "replay"} {
		for _, args := range [][]string{nil, {"-p", "prompt", "--output-format", "json"}, {"--input-format", "text"}} {
			home := t.TempDir()
			c := tapeProcessConfig{Scenario: "native-suggestions", Mode: mode, Home: home, Claim: filepath.Join(home, "claim"), Receipt: filepath.Join(home, "receipt"), Claude: filepath.Join(home, "must-not-execute")}
			if _, err := runClaudeTapeProcess(c, args); !errors.Is(err, errTapeNativePrintRefused) {
				t.Fatal("native non-stream invocation did not fail closed before admission")
			}
			rows, err := os.ReadDir(home)
			if err != nil || len(rows) != 0 {
				t.Fatal("native rejection wrote stream claim, receipt or output")
			}
		}
	}
	if !nativeStreamArgs([]string{"--output-format", "stream-json", "--input-format", "stream-json"}) {
		t.Fatal("stream producer rejected")
	}
	if nativeStreamArgs([]string{"-p", "--input-format stream-json"}) {
		t.Fatal("prompt text became producer capability")
	}
}
