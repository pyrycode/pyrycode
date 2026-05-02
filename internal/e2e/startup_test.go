//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"testing"
)

func TestE2E_Startup_CorruptRegistryFailsClean(t *testing.T) {
	home, regPath := newRegistryHome(t)

	corrupt := []byte("{not valid json")
	if err := os.WriteFile(regPath, corrupt, 0o600); err != nil {
		t.Fatalf("seed corrupt registry: %v", err)
	}

	res := StartExpectingFailureIn(t, home)

	if res.ExitCode == 0 {
		t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
	}
	if !bytes.Contains(res.Stderr, []byte("registry")) {
		t.Errorf("stderr does not mention registry: %s", res.Stderr)
	}

	got, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry after failed start: %v", err)
	}
	if !bytes.Equal(got, corrupt) {
		t.Errorf("registry mutated by failed start:\nwant: %q\ngot:  %q", corrupt, got)
	}
}
