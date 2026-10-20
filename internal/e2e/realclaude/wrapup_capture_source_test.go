//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWrapUpCaptureSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = source
		raw, err := cmd.Output()
		if err != nil {
			t.Fatal("source fixture git operation failed")
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "--quiet")
	path := filepath.Join(source, "capture.json")
	if err := os.WriteFile(path, []byte("committed native fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "capture.json")
	git("-c", "user.name=Capture test", "-c", "user.email=capture@example.invalid", "commit", "--quiet", "-m", "fixture")
	revision := git("rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("preceding suite capture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "untracked"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "source")
	if err := prepareWrapUpCaptureSource(ctx, source, destination, revision); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(destination, "capture.json"))
	if err != nil || string(raw) != "committed native fixture" {
		t.Fatal("capture used mutable suite fixture")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", destination, "status", "--porcelain")
	if raw, err := cmd.Output(); err != nil || len(raw) != 0 {
		t.Fatal("capture source is dirty")
	}
	if _, err := os.Stat(filepath.Join(destination, "untracked")); !os.IsNotExist(err) {
		t.Fatal("copied untracked suite output")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "preceding suite capture" || git("status", "--porcelain") == "" {
		t.Fatal("changed original suite evidence")
	}
	for _, revision := range []string{"HEAD", strings.Repeat("f", 40)} {
		if prepareWrapUpCaptureSource(ctx, source, filepath.Join(t.TempDir(), "invalid"), revision) == nil {
			t.Fatal("accepted an invalid or unavailable source revision")
		}
	}
}

func TestWrapUpCaptureDriverEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, action, output, want string
		valid                      bool
	}{
		{"pass", "pass", "", "none", true},
		{"dirty", "fail", "commit source before daemon suite capture", "source_dirty", false},
		{"build", "fail", "cannot compile original pool test assertions", "helper_build", false},
		{"unknown", "fail", "private provider content", "unknown", false},
		{"skip", "skip", "private skip reason", "unknown", false},
		{"malformed", "invalid", "", "stream", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := wrapUpCaptureDriverEvidence{Failure: "none"}
			root := "TestRecordedClaude/clear-wrapup-skipped"
			for _, action := range []string{"run", tc.action} {
				raw, err := json.Marshal(map[string]string{"Test": root, "Action": action, "Output": tc.output})
				if err != nil {
					t.Fatal(err)
				}
				if action == "invalid" {
					raw = []byte("invalid json")
				}
				evidence.observe(raw)
			}
			if evidence.valid() != tc.valid || evidence.Failure != tc.want {
				t.Fatalf("unexpected driver result: %+v", evidence)
			}
			raw, err := json.Marshal(evidence)
			if err != nil || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "Output") {
				t.Fatal("retained arbitrary child output")
			}
			if tc.valid {
				evidence.observe([]byte(`{"Action":"fail","Test":"TestRecordedClaude"}`))
				if evidence.valid() {
					t.Fatal("accepted a failed outer audit")
				}
			}
		})
	}
}
