//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSuggestCLIProducerIsolation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		native bool
		stream bool
		want   string
	}{
		{"native rejects fallback", true, false, ""},
		{"native explicitly enables", true, true, "1|true"},
		{"fallback stream disables", false, true, "0|false"},
		{"fallback one-shot allowed", false, false, "0|true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fake := `#!/usr/bin/python3
import os, sys
print(os.environ.get("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION", "") + "|" + str("--prompt-suggestions" in sys.argv).lower())
`
			if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION", "0")
			installSuggestCLI(t, tc.native)
			args := []string{"--prompt-suggestions"}
			if tc.stream {
				args = append(args, "--input-format", "stream-json")
			}
			out, err := exec.Command("claude", args...).Output()
			if tc.want == "" {
				if err == nil || len(out) != 0 {
					t.Fatal("native wrapper allowed non-stream launch")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("producer setup = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSuggestCLISourceEvidence(t *testing.T) {
	dir := t.TempDir()
	// The stand-in proves only wrapper transparency and redaction. It never
	// participates in either live set/clear test.
	stdout := "{\"type\":\"assistant\",\"message\":{\"content\":\"private-generated-text\"}}\n" +
		"{\"type\":\"result\",\"result\":\"private-generated-text\"}\n" +
		"{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"allowed_warning\"}}\n" +
		"{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"private-status\"}}\n" +
		"{\"type\":\"prompt_suggestion\",\"suggestion\":\"private-generated-text\"}\n" +
		"{\"type\":\"prompt_suggestion\",\"suggestion\":null}\n" +
		"{\"type\":\"prompt_suggestion\",\"suggestion\":\"  \"}\n" +
		"private-malformed-frame\n"
	fake := "#!/usr/bin/python3\nimport sys\nsys.stdout.write(" + strconv.Quote(stdout) + ")\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "private-credential-sentinel")
	evidence := installSuggestCLI(t, true)
	out, err := exec.Command("claude", "--input-format", "stream-json", "--prompt-suggestions").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, []byte(stdout)) {
		t.Fatal("source observer changed stdout")
	}
	want := suggestSource{Streams: 1, Results: 1, Events: 3, Suggestions: 1, Bytes: len("private-generated-text"), Warnings: 1}
	if got := readSuggestSource(t, evidence); got != want {
		t.Fatalf("source metadata = %+v, want %+v", got, want)
	}
	blob, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("private-")) {
		t.Fatal("source evidence retained sensitive data")
	}
	info, err := os.Stat(evidence)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("source evidence must be private")
	}
}
