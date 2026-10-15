package codexsup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// captureModel identifies the model in live captures and offline fixture replays.
const captureModel = "gpt-6-luna"

// captureDir holds the live frames TestCaptureLive records. The translator
// tests replay them; #2609 reuses the tool items.
var captureDir = filepath.Join("testdata", "capture")

// isExactCommand reports whether a command approval asks for exactly want:
// one parsed action equal to want, run bare or through a login shell's -lc.
// A chained command parses as one action holding the whole chain, so it fails.
func isExactCommand(params json.RawMessage, want string) bool {
	var p struct {
		Command        string `json:"command"`
		CommandActions []struct {
			Command string `json:"command"`
		} `json:"commandActions"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.CommandActions) != 1 || p.CommandActions[0].Command != want {
		return false
	}
	return p.Command == want || strings.HasSuffix(p.Command, " -lc '"+want+"'")
}

// allUnder reports whether every path, relative paths taken from root, sits
// under root. An empty change set is not accepted.
func allUnder(root string, paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, resolved)
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p = filepath.Clean(p)
		under := false
		for _, r := range roots {
			if rel, err := filepath.Rel(r, p); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
				under = true
			}
		}
		if !under {
			return false
		}
	}
	return true
}

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// machineKeys name the machine rather than an account: remoteControl's
	// hostname and installation id.
	machineKeys = map[string]bool{"serverName": true, "installationId": true}
)

// TestCaptureFixturesScrubbed fails when a committed capture holds a local
// path, an email or a machine identity, so a recapture's scrub is checked as
// code rather than by a manual grep.
func TestCaptureFixturesScrubbed(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(captureDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no capture files: %v", err)
	}
	leaks := []string{"/private/", "/Users/", "-Users-", "/home/", "/var/folders/"}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(data)
		for _, l := range leaks {
			if strings.Contains(s, l) {
				t.Errorf("%s: contains local path fragment %q", f, l)
			}
		}
		for _, m := range emailPattern.FindAllString(s, -1) {
			if m != "redacted@example.invalid" {
				t.Errorf("%s: contains email %q", f, m)
			}
		}
		for k := range machineKeys {
			re := regexp.MustCompile(`"` + k + `":"([^"]*)"`)
			for _, m := range re.FindAllStringSubmatch(s, -1) {
				if m[1] != "redacted" {
					t.Errorf("%s: %s = %q, want redacted", f, k, m[1])
				}
			}
		}
	}
}

func TestCaptureApprovalPredicates(t *testing.T) {
	cmd := func(command, action string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"command": command, "commandActions": []map[string]string{{"command": action, "type": "unknown"}},
		})
		return raw
	}
	for _, tc := range []struct {
		name   string
		params json.RawMessage
		want   bool
	}{
		{"shell wrapped", cmd("/bin/zsh -lc 'touch accepted.txt'", "touch accepted.txt"), true},
		{"bare", cmd("touch accepted.txt", "touch accepted.txt"), true},
		{"chained", cmd("/bin/zsh -lc 'rm -rf x && touch accepted.txt'", "rm -rf x && touch accepted.txt"), false},
		{"action mismatch", cmd("/bin/zsh -lc 'touch accepted.txt'", "touch other.txt"), false},
		{"command mismatch", cmd("/bin/zsh -lc 'curl x'", "touch accepted.txt"), false},
	} {
		if got := isExactCommand(tc.params, "touch accepted.txt"); got != tc.want {
			t.Errorf("isExactCommand %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	root := t.TempDir()
	for _, tc := range []struct {
		name  string
		paths []string
		want  bool
	}{
		{"relative", []string{"notes.txt"}, true},
		{"absolute under", []string{filepath.Join(root, "sub", "notes.txt")}, true},
		{"escape", []string{"../notes.txt"}, false},
		{"root itself", []string{root}, false},
		{"outside", []string{"/etc/passwd"}, false},
		{"one outside", []string{"notes.txt", "/etc/passwd"}, false},
		{"empty", nil, false},
	} {
		if got := allUnder(root, tc.paths); got != tc.want {
			t.Errorf("allUnder %s = %v, want %v", tc.name, got, tc.want)
		}
	}
}
