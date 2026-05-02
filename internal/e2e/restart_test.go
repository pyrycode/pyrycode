//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// registryEntry mirrors the on-disk shape used by internal/sessions. Defined
// locally because the production type is unexported. The schema is small and
// stable; if it grows a field, this struct grows the field too — the point of
// the test is the restart, not chasing schema drift.
type registryEntry struct {
	ID             string    `json:"id"`
	Label          string    `json:"label"`
	CreatedAt      time.Time `json:"created_at"`
	LastActiveAt   time.Time `json:"last_active_at"`
	Bootstrap      bool      `json:"bootstrap,omitempty"`
	LifecycleState string    `json:"lifecycle_state,omitempty"`
}

type registryFile struct {
	Version  int             `json:"version"`
	Sessions []registryEntry `json:"sessions"`
}

func TestE2E_Restart_PreservesActiveSessions(t *testing.T) {
	// os.MkdirTemp keeps the socket path under macOS's 104-byte sun_path
	// limit; t.TempDir() embeds the (long) test name and overflows.
	home, err := os.MkdirTemp("", "pyry-rs-*")
	if err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	regDir := filepath.Join(home, ".pyry", "test")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatalf("mkdir registry dir: %v", err)
	}
	regPath := filepath.Join(regDir, "sessions.json")

	now := time.Now().UTC().Truncate(time.Second)
	pre := registryFile{
		Version: 1,
		Sessions: []registryEntry{
			{
				ID:             "11111111-1111-4111-8111-111111111111",
				Label:          "",
				CreatedAt:      now,
				LastActiveAt:   now,
				Bootstrap:      true,
				LifecycleState: "active",
			},
			{
				ID:             "22222222-2222-4222-8222-222222222222",
				Label:          "second",
				CreatedAt:      now,
				LastActiveAt:   now,
				LifecycleState: "active",
			},
		},
	}
	writeRegistry(t, regPath, pre)

	h1 := StartIn(t, home)
	h1.Stop(t)

	if _, err := os.Stat(regPath); err != nil {
		t.Fatalf("registry file gone after first daemon Stop: %v", err)
	}

	h2 := StartIn(t, home)
	_ = h2 // ready-gate already passed inside StartIn; teardown via t.Cleanup.

	got := readRegistry(t, regPath)

	if got.Version != pre.Version {
		t.Errorf("registry version: got %d want %d", got.Version, pre.Version)
	}
	if len(got.Sessions) != len(pre.Sessions) {
		t.Fatalf("session count: got %d want %d\nfile:\n%s",
			len(got.Sessions), len(pre.Sessions), mustReadFile(t, regPath))
	}
	byID := make(map[string]registryEntry, len(got.Sessions))
	for _, e := range got.Sessions {
		byID[e.ID] = e
	}
	for _, want := range pre.Sessions {
		have, ok := byID[want.ID]
		if !ok {
			t.Errorf("session %s missing after restart", want.ID)
			continue
		}
		if have.LifecycleState != want.LifecycleState {
			t.Errorf("session %s lifecycle_state: got %q want %q",
				want.ID, have.LifecycleState, want.LifecycleState)
		}
		if have.Bootstrap != want.Bootstrap {
			t.Errorf("session %s bootstrap: got %v want %v",
				want.ID, have.Bootstrap, want.Bootstrap)
		}
	}
}

func writeRegistry(t *testing.T, path string, reg registryFile) {
	t.Helper()
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

func readRegistry(t *testing.T, path string) registryFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	var reg registryFile
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("parse registry: %v\nfile:\n%s", err, data)
	}
	return reg
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	return string(data)
}
