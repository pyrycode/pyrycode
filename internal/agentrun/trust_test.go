package agentrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestResolveWorkdir_DarwinRealpath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only: /var symlink")
	}
	wd := t.TempDir()
	got, err := ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", wd, err)
	}
	const want = "/private/var/"
	if len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("ResolveWorkdir(%q) = %q, want prefix %q", wd, got, want)
	}
}

func TestResolveWorkdir_AlreadyResolved(t *testing.T) {
	t.Parallel()
	// /tmp is a symlink to /private/tmp on macOS, so use a real created dir.
	wd := t.TempDir()
	resolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := ResolveWorkdir(resolved)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}
	if got != resolved {
		t.Fatalf("ResolveWorkdir(%q) = %q, want %q", resolved, got, resolved)
	}
}

func TestResolveWorkdir_RelativePath(t *testing.T) {
	t.Parallel()
	got, err := ResolveWorkdir(".")
	if err != nil {
		t.Fatalf("ResolveWorkdir(\".\"): %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("ResolveWorkdir(\".\") = %q, want absolute", got)
	}
}

func TestResolveWorkdir_MissingPath(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := ResolveWorkdir(missing)
	if err == nil {
		t.Fatalf("ResolveWorkdir(%q) = nil error; want non-nil", missing)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ResolveWorkdir(%q): error %v, want fs.ErrNotExist", missing, err)
	}
}

func TestEncodeProjectDir_DarwinRealpath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only: /var symlink")
	}
	wd := t.TempDir()
	got, err := EncodeProjectDir(wd)
	if err != nil {
		t.Fatalf("EncodeProjectDir(%q): %v", wd, err)
	}
	const want = "-private-var-folders-"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("EncodeProjectDir(%q) = %q, want prefix %q", wd, got, want)
	}
}

func TestEncodeProjectDir_LiteralSubstitution(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	resolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := EncodeProjectDir(resolved)
	if err != nil {
		t.Fatalf("EncodeProjectDir(%q): %v", resolved, err)
	}
	want := strings.NewReplacer("/", "-", ".", "-").Replace(resolved)
	if got != want {
		t.Fatalf("EncodeProjectDir(%q) = %q, want %q", resolved, got, want)
	}
}

func TestEncodeProjectDir_DotInPathSegment(t *testing.T) {
	t.Parallel()
	hidden := filepath.Join(t.TempDir(), ".hidden")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	got, err := EncodeProjectDir(hidden)
	if err != nil {
		t.Fatalf("EncodeProjectDir(%q): %v", hidden, err)
	}
	if !strings.HasSuffix(got, "--hidden") {
		t.Fatalf("EncodeProjectDir(%q) = %q, want suffix %q", hidden, got, "--hidden")
	}
}

func TestEncodeProjectDir_MissingPath(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := EncodeProjectDir(missing)
	if err == nil {
		t.Fatalf("EncodeProjectDir(%q) = nil error; want non-nil", missing)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("EncodeProjectDir(%q): error %v, want fs.ErrNotExist", missing, err)
	}
}

// readClaudeJSON reads the on-disk file and decodes with UseNumber so
// callers can assert against json.Number values verbatim.
func readClaudeJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		t.Fatalf("decode .claude.json: %v", err)
	}
	return root
}

func writeClaudeJSON(t *testing.T, home, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("pre-write .claude.json: %v", err)
	}
}

func mustResolve(t *testing.T, p string) string {
	t.Helper()
	r, err := ResolveWorkdir(p)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", p, err)
	}
	return r
}

func TestMarkWorkdirTrusted_MissingFileCreatesSkeleton(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	if err := MarkWorkdirTrusted(home, wd); err != nil {
		t.Fatalf("MarkWorkdirTrusted: %v", err)
	}

	dataPath := filepath.Join(home, ".claude.json")
	info, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("stat .claude.json: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 0600", got)
	}

	root := readClaudeJSON(t, home)
	projects, ok := root["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects field missing or wrong type: %T", root["projects"])
	}
	if len(projects) != 1 {
		t.Fatalf("projects len = %d, want 1", len(projects))
	}
	entry, ok := projects[mustResolve(t, wd)].(map[string]any)
	if !ok {
		t.Fatalf("entry for resolved wd missing or wrong type: %T", projects[mustResolve(t, wd)])
	}
	if got := entry["hasTrustDialogAccepted"]; got != true {
		t.Fatalf("hasTrustDialogAccepted = %v, want true", got)
	}
}

func TestMarkWorkdirTrusted_PreservesSiblingProjects(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	writeClaudeJSON(t, home, `{
  "projects": {
    "/some/other/path": {
      "hasTrustDialogAccepted": false,
      "extra": "field"
    }
  }
}`)

	if err := MarkWorkdirTrusted(home, wd); err != nil {
		t.Fatalf("MarkWorkdirTrusted: %v", err)
	}

	root := readClaudeJSON(t, home)
	projects := root["projects"].(map[string]any)
	if len(projects) != 2 {
		t.Fatalf("projects len = %d, want 2; got %v", len(projects), projects)
	}
	sibling, ok := projects["/some/other/path"].(map[string]any)
	if !ok {
		t.Fatalf("sibling missing or wrong type")
	}
	if got := sibling["hasTrustDialogAccepted"]; got != false {
		t.Fatalf("sibling hasTrustDialogAccepted = %v, want false", got)
	}
	if got := sibling["extra"]; got != "field" {
		t.Fatalf("sibling extra = %v, want %q", got, "field")
	}
	target := projects[mustResolve(t, wd)].(map[string]any)
	if got := target["hasTrustDialogAccepted"]; got != true {
		t.Fatalf("target hasTrustDialogAccepted = %v, want true", got)
	}
}

func TestMarkWorkdirTrusted_PreservesFieldsWithinEntry(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()
	key := mustResolve(t, wd)

	pre := map[string]any{
		"projects": map[string]any{
			key: map[string]any{
				"hasTrustDialogAccepted": false,
				"foo":                    "bar",
				"mcpServers": map[string]any{
					"x": map[string]any{"command": "y"},
				},
			},
		},
	}
	b, _ := json.MarshalIndent(pre, "", "  ")
	writeClaudeJSON(t, home, string(b))

	if err := MarkWorkdirTrusted(home, wd); err != nil {
		t.Fatalf("MarkWorkdirTrusted: %v", err)
	}

	root := readClaudeJSON(t, home)
	entry := root["projects"].(map[string]any)[key].(map[string]any)
	if got := entry["hasTrustDialogAccepted"]; got != true {
		t.Fatalf("hasTrustDialogAccepted = %v, want true", got)
	}
	if got := entry["foo"]; got != "bar" {
		t.Fatalf("foo = %v, want %q", got, "bar")
	}
	mcp, ok := entry["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers wrong type: %T", entry["mcpServers"])
	}
	xEntry, ok := mcp["x"].(map[string]any)
	if !ok || xEntry["command"] != "y" {
		t.Fatalf("mcpServers.x.command lost; got %v", mcp["x"])
	}
}

func TestMarkWorkdirTrusted_PreservesTopLevelFields(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	writeClaudeJSON(t, home, `{
  "projects": {},
  "userID": "abc",
  "telemetry": {"enabled": false}
}`)

	if err := MarkWorkdirTrusted(home, wd); err != nil {
		t.Fatalf("MarkWorkdirTrusted: %v", err)
	}

	root := readClaudeJSON(t, home)
	if got := root["userID"]; got != "abc" {
		t.Fatalf("userID = %v, want %q", got, "abc")
	}
	tel, ok := root["telemetry"].(map[string]any)
	if !ok {
		t.Fatalf("telemetry wrong type: %T", root["telemetry"])
	}
	if got := tel["enabled"]; got != false {
		t.Fatalf("telemetry.enabled = %v, want false", got)
	}
}

func TestMarkWorkdirTrusted_PreservesNumericPrecision(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	const bigNum = "1763123456789012345"
	writeClaudeJSON(t, home, `{
  "projects": {},
  "lastLoginNanos": `+bigNum+`
}`)

	if err := MarkWorkdirTrusted(home, wd); err != nil {
		t.Fatalf("MarkWorkdirTrusted: %v", err)
	}

	root := readClaudeJSON(t, home)
	n, ok := root["lastLoginNanos"].(json.Number)
	if !ok {
		t.Fatalf("lastLoginNanos type = %T, want json.Number", root["lastLoginNanos"])
	}
	if n.String() != bigNum {
		t.Fatalf("lastLoginNanos = %q, want %q", n.String(), bigNum)
	}
}

func TestMarkWorkdirTrusted_Idempotent(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	if err := MarkWorkdirTrusted(home, wd); err != nil {
		t.Fatalf("first call: %v", err)
	}
	a, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read a: %v", err)
	}
	if err := MarkWorkdirTrusted(home, wd); err != nil {
		t.Fatalf("second call: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("not byte-identical\nA: %s\nB: %s", a, b)
	}
}

func TestMarkWorkdirTrusted_ConcurrentSerializes(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd1 := t.TempDir()
	wd2 := t.TempDir()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errCh <- MarkWorkdirTrusted(home, wd1)
	}()
	go func() {
		defer wg.Done()
		errCh <- MarkWorkdirTrusted(home, wd2)
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent MarkWorkdirTrusted: %v", err)
		}
	}

	root := readClaudeJSON(t, home)
	projects, ok := root["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects wrong type: %T", root["projects"])
	}
	for _, wd := range []string{wd1, wd2} {
		entry, ok := projects[mustResolve(t, wd)].(map[string]any)
		if !ok {
			t.Fatalf("missing entry for %q in projects %v", wd, projects)
		}
		if got := entry["hasTrustDialogAccepted"]; got != true {
			t.Fatalf("entry[%q].hasTrustDialogAccepted = %v, want true", wd, got)
		}
	}
}

func TestMarkWorkdirTrusted_PreservesExistingFileMode(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	dataPath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(dataPath, []byte(`{"projects":{}}`), 0o644); err != nil {
		t.Fatalf("pre-write: %v", err)
	}

	if err := MarkWorkdirTrusted(home, wd); err != nil {
		t.Fatalf("MarkWorkdirTrusted: %v", err)
	}

	info, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 0644", got)
	}
}

func TestMarkWorkdirTrusted_MalformedJSONFailsLoudly(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	writeClaudeJSON(t, home, `not json`)
	pre, _ := os.ReadFile(filepath.Join(home, ".claude.json"))

	err := MarkWorkdirTrusted(home, wd)
	if err == nil {
		t.Fatalf("MarkWorkdirTrusted on malformed input: want error, got nil")
	}
	post, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !bytes.Equal(pre, post) {
		t.Fatalf("file changed despite error\nbefore: %s\nafter: %s", pre, post)
	}
}

func TestMarkWorkdirTrusted_ProjectsNotObjectFails(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	writeClaudeJSON(t, home, `{"projects": "not an object"}`)
	pre, _ := os.ReadFile(filepath.Join(home, ".claude.json"))

	err := MarkWorkdirTrusted(home, wd)
	if err == nil {
		t.Fatalf("want error when projects is a string; got nil")
	}
	post, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !bytes.Equal(pre, post) {
		t.Fatalf("file changed despite error")
	}
}

func TestMarkWorkdirTrusted_EntryNotObjectFails(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()
	key := mustResolve(t, wd)

	pre := map[string]any{
		"projects": map[string]any{
			key: "not an object",
		},
	}
	b, _ := json.MarshalIndent(pre, "", "  ")
	writeClaudeJSON(t, home, string(b))
	preBytes, _ := os.ReadFile(filepath.Join(home, ".claude.json"))

	err := MarkWorkdirTrusted(home, wd)
	if err == nil {
		t.Fatalf("want error when entry is a string; got nil")
	}
	post, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !bytes.Equal(preBytes, post) {
		t.Fatalf("file changed despite error")
	}
}
