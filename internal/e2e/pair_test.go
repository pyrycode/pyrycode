//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/pair"
)

// TestPair_E2E fulfills AC#6: invokes the built binary, captures stdout,
// decodes the payload via pair.Decode, looks up the resulting token hash
// in the registry on disk, and asserts the device is present with the
// expected Name. No daemon involved — the verb is one-shot CLI.
//
// HOME is pinned to a t.TempDir() via RunBareIn so the binary's
// ~/.pyry/<name>/ resolves under the test's isolated state. Default
// instance name "pyry" is used (no -pyry-name flag, no PYRY_NAME env);
// the registry path is therefore <home>/.pyry/pyry/devices.json.
func TestPair_E2E(t *testing.T) {
	t.Run("with explicit name", func(t *testing.T) {
		home := t.TempDir()
		r := RunBareIn(t, home, "pair", "--name=test-phone")
		if r.ExitCode != 0 {
			t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
				r.ExitCode, r.Stdout, r.Stderr)
		}

		payload := decodePairPayload(t, r.Stdout)

		registryPath := filepath.Join(home, ".pyry", "pyry", "devices.json")
		registry, err := devices.Load(registryPath)
		if err != nil {
			t.Fatalf("devices.Load(%q): %v", registryPath, err)
		}
		list := registry.List()
		if len(list) != 1 {
			t.Fatalf("registry has %d entries, want 1", len(list))
		}
		entry := list[0]
		if entry.Name != "test-phone" {
			t.Errorf("entry.Name=%q want %q", entry.Name, "test-phone")
		}
		if !devices.VerifyToken(payload.Token, entry.TokenHash) {
			t.Errorf("payload.Token does not hash to entry.TokenHash")
		}
		if payload.Server == "" {
			t.Error("payload.Server is empty")
		}
		if payload.Relay == "" {
			t.Error("payload.Relay is empty")
		}
	})

	t.Run("auto-name when --name omitted", func(t *testing.T) {
		home := t.TempDir()
		r := RunBareIn(t, home, "pair")
		if r.ExitCode != 0 {
			t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
				r.ExitCode, r.Stdout, r.Stderr)
		}

		payload := decodePairPayload(t, r.Stdout)

		registryPath := filepath.Join(home, ".pyry", "pyry", "devices.json")
		registry, err := devices.Load(registryPath)
		if err != nil {
			t.Fatalf("devices.Load(%q): %v", registryPath, err)
		}
		list := registry.List()
		if len(list) != 1 {
			t.Fatalf("registry has %d entries, want 1", len(list))
		}
		entry := list[0]
		want := "device-" + entry.TokenHash[:8]
		if entry.Name != want {
			t.Errorf("auto-name=%q want %q", entry.Name, want)
		}
		if !devices.VerifyToken(payload.Token, entry.TokenHash) {
			t.Errorf("payload.Token does not hash to entry.TokenHash")
		}
	})
}

// decodePairPayload finds the encoded payload in pair output and decodes
// it. Render writes the QR (UTF-8 half-blocks) followed by a blank line,
// the encoded string on its own line, and a one-line instruction.
// Pulling out the encoded line means scanning each line for one that
// pair.Decode accepts.
func decodePairPayload(t *testing.T, stdout []byte) pair.Payload {
	t.Helper()
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if p, err := pair.Decode(line); err == nil {
			return p
		}
	}
	t.Fatalf("no decodable pair payload found in stdout:\n%s", stdout)
	return pair.Payload{}
}
