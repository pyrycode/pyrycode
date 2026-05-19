package trust

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

func writeJSON(t *testing.T, path string, root any, mode fs.FileMode) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return buf.Bytes()
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return root
}

func TestMarkWorkdirTrusted_CreatesFileWhenMissing(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()
	wantRealpath, err := agentrun.ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", wd, err)
	}

	gotRealpath, err := markWorkdirTrustedIn(home, wd)
	if err != nil {
		t.Fatalf("markWorkdirTrustedIn: %v", err)
	}
	if gotRealpath != wantRealpath {
		t.Fatalf("realpath = %q, want %q", gotRealpath, wantRealpath)
	}

	dataPath := filepath.Join(home, ".claude.json")
	info, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("Stat %s: %v", dataPath, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want %o", got, 0o600)
	}

	root := readJSON(t, dataPath)
	projects, ok := root["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects type %T, want map[string]any", root["projects"])
	}
	if len(projects) != 1 {
		t.Fatalf("projects len = %d, want 1", len(projects))
	}
	entry, ok := projects[wantRealpath].(map[string]any)
	if !ok {
		t.Fatalf("projects[%q] type %T, want map[string]any", wantRealpath, projects[wantRealpath])
	}
	if got := entry["hasTrustDialogAccepted"]; got != true {
		t.Fatalf("hasTrustDialogAccepted = %v, want true", got)
	}
}

func TestMarkWorkdirTrusted_AddsToExistingFileWithoutProjects(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()
	realpath, err := agentrun.ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}

	dataPath := filepath.Join(home, ".claude.json")
	pre := map[string]any{
		"userID":    "abc",
		"telemetry": map[string]any{"enabled": false},
	}
	writeJSON(t, dataPath, pre, 0o600)

	if _, err := markWorkdirTrustedIn(home, wd); err != nil {
		t.Fatalf("markWorkdirTrustedIn: %v", err)
	}

	root := readJSON(t, dataPath)
	if got := root["userID"]; got != "abc" {
		t.Errorf("userID = %v, want abc", got)
	}
	tel, ok := root["telemetry"].(map[string]any)
	if !ok || tel["enabled"] != false {
		t.Errorf("telemetry = %v, want {enabled: false}", root["telemetry"])
	}
	projects, ok := root["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects type %T, want map[string]any", root["projects"])
	}
	entry, ok := projects[realpath].(map[string]any)
	if !ok {
		t.Fatalf("projects[%q] type %T", realpath, projects[realpath])
	}
	if entry["hasTrustDialogAccepted"] != true {
		t.Errorf("hasTrustDialogAccepted = %v, want true", entry["hasTrustDialogAccepted"])
	}
}

func TestMarkWorkdirTrusted_PreservesSiblingProjects(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()
	realpath, err := agentrun.ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}

	dataPath := filepath.Join(home, ".claude.json")
	pre := map[string]any{
		"projects": map[string]any{
			"/some/other/path": map[string]any{
				"hasTrustDialogAccepted": false,
				"extra":                  "field",
			},
		},
	}
	writeJSON(t, dataPath, pre, 0o600)

	if _, err := markWorkdirTrustedIn(home, wd); err != nil {
		t.Fatalf("markWorkdirTrustedIn: %v", err)
	}

	root := readJSON(t, dataPath)
	projects, _ := root["projects"].(map[string]any)
	if len(projects) != 2 {
		t.Fatalf("projects len = %d, want 2", len(projects))
	}
	other, ok := projects["/some/other/path"].(map[string]any)
	if !ok {
		t.Fatalf("sibling entry type %T", projects["/some/other/path"])
	}
	if other["hasTrustDialogAccepted"] != false {
		t.Errorf("sibling hasTrustDialogAccepted = %v, want false", other["hasTrustDialogAccepted"])
	}
	if other["extra"] != "field" {
		t.Errorf("sibling extra = %v, want \"field\"", other["extra"])
	}
	target, ok := projects[realpath].(map[string]any)
	if !ok {
		t.Fatalf("target entry type %T", projects[realpath])
	}
	if target["hasTrustDialogAccepted"] != true {
		t.Errorf("target hasTrustDialogAccepted = %v, want true", target["hasTrustDialogAccepted"])
	}
}

func TestMarkWorkdirTrusted_IdempotentPreservesExtraEntryFields(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()
	realpath, err := agentrun.ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}

	dataPath := filepath.Join(home, ".claude.json")
	pre := map[string]any{
		"projects": map[string]any{
			realpath: map[string]any{
				"hasTrustDialogAccepted": true,
				"mcpServers": map[string]any{
					"example": "value",
				},
			},
		},
	}
	bytesA := writeJSON(t, dataPath, pre, 0o600)

	if _, err := markWorkdirTrustedIn(home, wd); err != nil {
		t.Fatalf("markWorkdirTrustedIn: %v", err)
	}

	bytesB, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("read B: %v", err)
	}

	if !bytes.Equal(bytesA, bytesB) {
		t.Fatalf("idempotency violated\nA:\n%s\nB:\n%s", bytesA, bytesB)
	}
}

func TestMarkWorkdirTrusted_MalformedJSONFails(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	dataPath := filepath.Join(home, ".claude.json")
	pre := []byte(`"not json"`)
	if err := os.WriteFile(dataPath, pre, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := markWorkdirTrustedIn(home, wd); err == nil {
		t.Fatalf("markWorkdirTrustedIn = nil error; want non-nil")
	}

	got, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, pre) {
		t.Fatalf("file was modified; got %q want %q", got, pre)
	}
}

func TestMarkWorkdirTrusted_WorkdirMissingReturnsError(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := markWorkdirTrustedIn(home, missing)
	if err == nil {
		t.Fatalf("markWorkdirTrustedIn = nil error; want non-nil")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want errors.Is fs.ErrNotExist", err)
	}

	dataPath := filepath.Join(home, ".claude.json")
	if _, err := os.Stat(dataPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("~/.claude.json should not exist; Stat err = %v", err)
	}
}

func TestMarkWorkdirTrusted_WorkdirSymlinkResolvesToRealpath(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(home, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	gotRealpath, err := markWorkdirTrustedIn(home, link)
	if err != nil {
		t.Fatalf("markWorkdirTrustedIn: %v", err)
	}

	wantRealpath, err := agentrun.ResolveWorkdir(target)
	if err != nil {
		t.Fatalf("ResolveWorkdir(target): %v", err)
	}
	if gotRealpath != wantRealpath {
		t.Fatalf("realpath = %q, want %q (NOT %q)", gotRealpath, wantRealpath, link)
	}

	dataPath := filepath.Join(home, ".claude.json")
	root := readJSON(t, dataPath)
	projects, ok := root["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects type %T", root["projects"])
	}
	if len(projects) != 1 {
		t.Fatalf("projects len = %d, want 1; got %v", len(projects), projects)
	}
	if _, ok := projects[wantRealpath]; !ok {
		t.Fatalf("projects missing realpath key %q; got %v", wantRealpath, projects)
	}
}

func TestMarkWorkdirTrusted_PreservesNumericPrecision(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	dataPath := filepath.Join(home, ".claude.json")
	pre := []byte(`{
  "lastLoginNanos": 1763123456789012345,
  "projects": {}
}
`)
	if err := os.WriteFile(dataPath, pre, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := markWorkdirTrustedIn(home, wd); err != nil {
		t.Fatalf("markWorkdirTrustedIn: %v", err)
	}

	root := readJSON(t, dataPath)
	num, ok := root["lastLoginNanos"].(json.Number)
	if !ok {
		t.Fatalf("lastLoginNanos type %T, want json.Number", root["lastLoginNanos"])
	}
	if num.String() != "1763123456789012345" {
		t.Fatalf("lastLoginNanos = %s, want 1763123456789012345", num.String())
	}
}

func TestMarkWorkdirTrusted_PreservesFileMode(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	dataPath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(dataPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(dataPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := markWorkdirTrustedIn(home, wd); err != nil {
		t.Fatalf("markWorkdirTrustedIn: %v", err)
	}

	info, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("file mode = %o, want %o", got, 0o644)
	}
}

func TestMarkWorkdirTrusted_ProjectsNotObjectFails(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()

	dataPath := filepath.Join(home, ".claude.json")
	pre := []byte(`{"projects": "not an object"}`)
	if err := os.WriteFile(dataPath, pre, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := markWorkdirTrustedIn(home, wd); err == nil {
		t.Fatalf("markWorkdirTrustedIn = nil error; want non-nil")
	}

	got, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, pre) {
		t.Fatalf("file was modified; got %q want %q", got, pre)
	}
}

func TestMarkWorkdirTrusted_EntryNotObjectFails(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	wd := t.TempDir()
	realpath, err := agentrun.ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}

	dataPath := filepath.Join(home, ".claude.json")
	pre := map[string]any{
		"projects": map[string]any{
			realpath: "not an object",
		},
	}
	bytesBefore := writeJSON(t, dataPath, pre, 0o600)

	if _, err := markWorkdirTrustedIn(home, wd); err == nil {
		t.Fatalf("markWorkdirTrustedIn = nil error; want non-nil")
	}

	got, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, bytesBefore) {
		t.Fatalf("file was modified")
	}
}

// TestMarkWorkdirTrusted_PublicSmoke pins the os.UserHomeDir plumbing for the
// exported wrapper. Cannot run in parallel because t.Setenv forbids parallel
// ancestors; the behavioural matrix is covered by markWorkdirTrustedIn tests.
func TestMarkWorkdirTrusted_PublicSmoke(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	wd := t.TempDir()

	gotRealpath, err := MarkWorkdirTrusted(wd)
	if err != nil {
		t.Fatalf("MarkWorkdirTrusted: %v", err)
	}
	wantRealpath, err := agentrun.ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}
	if gotRealpath != wantRealpath {
		t.Fatalf("realpath = %q, want %q", gotRealpath, wantRealpath)
	}

	dataPath := filepath.Join(tmp, ".claude.json")
	root := readJSON(t, dataPath)
	projects, ok := root["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects type %T", root["projects"])
	}
	entry, ok := projects[wantRealpath].(map[string]any)
	if !ok {
		t.Fatalf("entry type %T", projects[wantRealpath])
	}
	if entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("hasTrustDialogAccepted = %v, want true", entry["hasTrustDialogAccepted"])
	}
}
