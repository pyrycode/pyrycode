package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSettings_EmptyInputReturnsErrorAndDoesNotWrite(t *testing.T) {
	// Cannot use t.Parallel() here — t.Setenv (below) forbids it. The
	// previous before/after-glob approach intended to coexist with parallel
	// siblings, but it raced anyway: parallel tests calling WriteSettings
	// with VALID input create *persistent* tempfiles by design (the caller
	// uses the path post-return), and those files landed in our `after`
	// glob unattributable to this call. CI surfaced the race; local runs
	// were timing-lucky. Fix: isolate TMPDIR per-test so the glob only sees
	// this test's own tempfiles. Trade-off: this test runs serially
	// relative to others in the package — acceptable, the test is O(ms).
	t.Setenv("TMPDIR", t.TempDir())

	cases := []struct {
		name  string
		input []string
	}{
		{"nil", nil},
		{"empty", []string{}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// No t.Parallel — see parent test's comment. Sub-tests must
			// also run serially because they share the parent's TMPDIR
			// and would race the same way against each other.

			// Snapshot the tempdir set before/after. With TMPDIR isolated
			// to t.TempDir() above, only this test's own WriteSettings
			// calls can put files in this glob — no cross-test contamination.
			pattern := filepath.Join(os.TempDir(), "pyry-agent-run-settings-*.json")
			before, err := filepath.Glob(pattern)
			if err != nil {
				t.Fatalf("Glob before: %v", err)
			}
			beforeSet := make(map[string]struct{}, len(before))
			for _, p := range before {
				beforeSet[p] = struct{}{}
			}

			path, err := WriteSettings(tc.input)
			if err == nil {
				_ = os.Remove(path)
				t.Fatalf("WriteSettings(%v) = nil error; want non-nil", tc.input)
			}
			if path != "" {
				t.Errorf("path = %q on error; want \"\"", path)
			}
			if !strings.Contains(err.Error(), "agentrun/settings: allowedTools required") {
				t.Errorf("err = %q; want it to contain %q", err, "agentrun/settings: allowedTools required")
			}

			after, err := filepath.Glob(pattern)
			if err != nil {
				t.Fatalf("Glob after: %v", err)
			}
			for _, p := range after {
				if _, ok := beforeSet[p]; !ok {
					t.Errorf("tempfile leaked: %q is in `after` but not `before`", p)
				}
			}
		})
	}
}

func TestWriteSettings_SingleToolGoldenBytes(t *testing.T) {
	t.Parallel()

	path, err := WriteSettings([]string{"Bash"})
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := []byte(`{"permissions":{"allow":["Bash"],"defaultMode":"dontAsk"}}` + "\n")
	if string(got) != string(want) {
		t.Fatalf("bytes mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestWriteSettings_PreservesOrderAndDuplicates(t *testing.T) {
	t.Parallel()

	input := []string{"Bash", "Read", "Bash", "Edit"}
	path, err := WriteSettings(input)
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := []byte(`{"permissions":{"allow":["Bash","Read","Bash","Edit"],"defaultMode":"dontAsk"}}` + "\n")
	if string(got) != string(want) {
		t.Fatalf("bytes mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestWriteSettings_RoundTripParseable(t *testing.T) {
	t.Parallel()

	input := []string{"Read", "Bash"}
	path, err := WriteSettings(input)
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var parsed struct {
		Permissions struct {
			Allow       []string `json:"allow"`
			DefaultMode string   `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got, want := len(parsed.Permissions.Allow), len(input); got != want {
		t.Fatalf("allow len = %d, want %d", got, want)
	}
	for i, tool := range input {
		if parsed.Permissions.Allow[i] != tool {
			t.Errorf("allow[%d] = %q, want %q", i, parsed.Permissions.Allow[i], tool)
		}
	}
	if parsed.Permissions.DefaultMode != "dontAsk" {
		t.Errorf("defaultMode = %q, want %q", parsed.Permissions.DefaultMode, "dontAsk")
	}
}

func TestWriteSettings_PathLocationPrefixSuffix(t *testing.T) {
	t.Parallel()

	path, err := WriteSettings([]string{"Read"})
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	defer os.Remove(path)

	gotDir := filepath.Clean(filepath.Dir(path))
	wantDir := filepath.Clean(os.TempDir())
	if gotDir != wantDir {
		t.Errorf("dir = %q, want %q", gotDir, wantDir)
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "pyry-agent-run-settings-") {
		t.Errorf("base = %q; want prefix %q", base, "pyry-agent-run-settings-")
	}
	if !strings.HasSuffix(path, ".json") {
		t.Errorf("path = %q; want .json suffix", path)
	}
}

func TestWriteSettings_PathIsAbsolute(t *testing.T) {
	t.Parallel()

	path, err := WriteSettings([]string{"Read"})
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	defer os.Remove(path)

	if !filepath.IsAbs(path) {
		t.Errorf("path = %q; want absolute", path)
	}
}
