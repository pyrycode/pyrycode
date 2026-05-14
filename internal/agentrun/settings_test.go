package agentrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteSettings_JSONShape pins the byte-for-byte output for the
// canonical inputs. Any indentation change or field-order reshuffle that
// would break the dispatcher's downstream `claude --settings` consumer
// or the boot-time schema self-check (#336) will trip this test.
func TestWriteSettings_JSONShape(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		want    string
	}{
		{
			name:    "two tools",
			allowed: []string{"Read", "Bash"},
			want:    `{"permissions":{"allow":["Read","Bash"],"defaultMode":"deny"}}` + "\n",
		},
		{
			name:    "single tool",
			allowed: []string{"Read"},
			want:    `{"permissions":{"allow":["Read"],"defaultMode":"deny"}}` + "\n",
		},
		{
			name:    "empty slice",
			allowed: []string{},
			want:    `{"permissions":{"allow":[],"defaultMode":"deny"}}` + "\n",
		},
		{
			name:    "nil slice normalises to empty",
			allowed: nil,
			want:    `{"permissions":{"allow":[],"defaultMode":"deny"}}` + "\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path, err := WriteSettings(dir, tc.allowed)
			if err != nil {
				t.Fatalf("WriteSettings: unexpected error: %v", err)
			}
			wantPath := filepath.Join(dir, SettingsFilename)
			if path != wantPath {
				t.Errorf("path = %q, want %q", path, wantPath)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("on-disk bytes:\n got  = %q\n want = %q", got, tc.want)
			}
		})
	}
}

// TestWriteSettings_Mode pins the on-disk permission bits (0o600) — matches
// the rest of the project's on-disk artefacts (registries, trust state).
func TestWriteSettings_Mode(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteSettings(dir, []string{"Read"})
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0o600", got)
	}
}

// TestWriteSettings_Overwrite covers an overwrite of an existing settings
// file from a prior invocation. Atomic rename guarantees no half-written
// state and no stray temp leftover.
func TestWriteSettings_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SettingsFilename)
	if err := os.WriteFile(path, []byte(`{"permissions":{"allow":["Stale"],"defaultMode":"allow"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	got, err := WriteSettings(dir, []string{"Read", "Bash"})
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	want := `{"permissions":{"allow":["Read","Bash"],"defaultMode":"deny"}}` + "\n"
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(bytes) != want {
		t.Errorf("on-disk bytes after overwrite:\n got  = %q\n want = %q", bytes, want)
	}

	// No stray .tmp leftovers.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("unexpected temp leftover: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("workdir contents = %v, want exactly [%s]", names, SettingsFilename)
	}
}

// TestWriteSettings_WorkdirMissing exercises the create-temp failure path —
// the helper does not pre-stat the workdir; the failure surface is the
// underlying os.CreateTemp call.
func TestWriteSettings_WorkdirMissing(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "nope")
	_, err := WriteSettings(workdir, []string{"Read"})
	if err == nil {
		t.Fatal("expected error for missing workdir, got nil")
	}
	if !strings.Contains(err.Error(), "agentrun: write settings:") {
		t.Errorf("error %q missing prefix %q", err.Error(), "agentrun: write settings:")
	}
}
