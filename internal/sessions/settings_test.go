package sessions

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteMCPSettings_ShapeAndContent (AC #1/#2): the interactive-spawn settings
// writer emits exactly enough to suppress claude's MCP-enablement modal
// (enableAllProjectMcpServers:true) and NOTHING that changes the interactive
// session's permission posture — no permissions/allow/deny/defaultMode keys.
//
// Both branches of the writer are covered: the payload is a property of the
// file's content and must not depend on where the file lives (#1518).
func TestWriteMCPSettings_ShapeAndContent(t *testing.T) {
	t.Parallel()

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	tests := []struct {
		name         string
		registryPath string
	}{
		{name: "persistence disabled", registryPath: ""},
		{name: "data dir", registryPath: filepath.Join(t.TempDir(), "sessions.json")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, err := writeMCPSettings(tc.registryPath, id)
			if err != nil {
				t.Fatalf("writeMCPSettings: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })

			if !filepath.IsAbs(path) {
				t.Errorf("writeMCPSettings path = %q, want absolute", path)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read settings file: %v", err)
			}

			// AC #1: enableAllProjectMcpServers is present and true.
			var got struct {
				EnableAllProjectMcpServers        bool `json:"enableAllProjectMcpServers"`
				SkipDangerousModePermissionPrompt bool `json:"skipDangerousModePermissionPrompt"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal %q: %v", raw, err)
			}
			if !got.EnableAllProjectMcpServers {
				t.Errorf("enableAllProjectMcpServers = false, want true (JSON: %s)", raw)
			}

			// AC #1b: skipDangerousModePermissionPrompt is present and true, so a
			// spawn that carries --dangerously-skip-permissions (operator-enabled
			// YOLO) does not wedge on claude 2.1.199's Bypass Permissions warning
			// dialog ("1. No, exit" / "2. Yes, I accept" selection). The field is
			// inert when the spawn does not request bypass mode, so it is safe to
			// write unconditionally.
			if !got.SkipDangerousModePermissionPrompt {
				t.Errorf("skipDangerousModePermissionPrompt = false, want true (JSON: %s)", raw)
			}

			// AC #2: no permission posture. The agent-run deny-default writer stamps
			// a "permissions" object with "defaultMode":"dontAsk"; none of those
			// tokens may appear here or normal interactive tool prompts break.
			for _, forbidden := range []string{"permissions", "defaultMode", "dontAsk", "allow", "deny"} {
				if strings.Contains(string(raw), forbidden) {
					t.Errorf("settings JSON %q must not contain %q (AC #2: no permission posture change)", raw, forbidden)
				}
			}
		})
	}
}

// TestWriteMCPSettings_DistinctPaths: distinct sessions get distinct files. The
// per-session discipline (one file per session, removed at that session's
// teardown) relies on that so removing one never orphans another.
//
// The invariant is per SESSION, not per call (#1518). On the data-dir branch the
// name is derived from the id, so two calls for the same id deliberately land on
// one path — that is what bounds the on-disk set (see
// TestWriteMCPSettings_SameIDStablePath) — while two ids never collide. On the
// persistence-disabled branch the id is not consulted at all, so every call gets
// its own random temp name exactly as it did before.
func TestWriteMCPSettings_DistinctPaths(t *testing.T) {
	t.Parallel()

	idA, err := NewID()
	if err != nil {
		t.Fatalf("NewID (a): %v", err)
	}
	idB, err := NewID()
	if err != nil {
		t.Fatalf("NewID (b): %v", err)
	}

	t.Run("data dir, two ids", func(t *testing.T) {
		regPath := filepath.Join(t.TempDir(), "sessions.json")

		a, err := writeMCPSettings(regPath, idA)
		if err != nil {
			t.Fatalf("writeMCPSettings (a): %v", err)
		}
		b, err := writeMCPSettings(regPath, idB)
		if err != nil {
			t.Fatalf("writeMCPSettings (b): %v", err)
		}
		if a == b {
			t.Errorf("distinct session ids share a settings path: %q", a)
		}
	})

	t.Run("persistence disabled, two calls", func(t *testing.T) {
		a, err := writeMCPSettings("", idA)
		if err != nil {
			t.Fatalf("writeMCPSettings (a): %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(a) })

		b, err := writeMCPSettings("", idA)
		if err != nil {
			t.Fatalf("writeMCPSettings (b): %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(b) })

		if a == b {
			t.Errorf("writeMCPSettings returned the same path twice: %q", a)
		}
	})
}

// TestWriteMCPSettings_DataDirPathAndModes (#1518, AC #1/#3): with a registry path
// configured the file lands at <dataDir>/session-settings/<id>.json — an absolute
// path under the daemon's own data directory, not in an OS temp dir a reaper can
// clean out from under a live session. The subdirectory is created on demand at
// 0700 (cold start reaches this before the registry save has ever run) and the
// file itself is not group- or world-readable: anyone who can write it can hand
// claude hooks/permissions keys, i.e. code execution as the operator.
func TestWriteMCPSettings_DataDirPathAndModes(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	regPath := filepath.Join(dataDir, "sessions.json")
	settingsDir := filepath.Join(dataDir, "session-settings")

	// Cold start: neither sessions.json nor the settings subdirectory exists yet.
	if _, err := os.Stat(settingsDir); !os.IsNotExist(err) {
		t.Fatalf("test setup: %q already exists (err=%v)", settingsDir, err)
	}

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	path, err := writeMCPSettings(regPath, id)
	if err != nil {
		t.Fatalf("writeMCPSettings: %v", err)
	}

	if want := filepath.Join(settingsDir, string(id)+".json"); path != want {
		t.Errorf("writeMCPSettings path = %q, want %q", path, want)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("writeMCPSettings path = %q, want absolute", path)
	}

	di, err := os.Stat(settingsDir)
	if err != nil {
		t.Fatalf("stat settings dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("settings dir %q mode = %04o, want 0700", settingsDir, got)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat settings file: %v", err)
	}
	if got := fi.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("settings file %q mode = %04o, want no group/world bits", path, got)
	}
}

// TestWriteMCPSettings_SameIDStablePath (#1518, AC #4): the filename is a pure
// function of the session id, so a warm-started daemon rewrites the same file
// rather than adding one per restart. This is what makes the on-disk set bounded
// by session count with no startup sweeper; it reddens the moment the name goes
// back to a random suffix.
func TestWriteMCPSettings_SameIDStablePath(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	regPath := filepath.Join(dataDir, "sessions.json")
	settingsDir := filepath.Join(dataDir, "session-settings")

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	first, err := writeMCPSettings(regPath, id)
	if err != nil {
		t.Fatalf("writeMCPSettings (first): %v", err)
	}
	second, err := writeMCPSettings(regPath, id)
	if err != nil {
		t.Fatalf("writeMCPSettings (second): %v", err)
	}
	if first != second {
		t.Errorf("same id gave two paths: %q then %q", first, second)
	}

	// Every entry, not just the *.json ones: a second write must leave no scratch
	// file behind either.
	entries, err := os.ReadDir(settingsDir)
	if err != nil {
		t.Fatalf("readdir %q: %v", settingsDir, err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("settings dir holds %v after two writes for one id, want exactly one file", names)
	}

	// The rename left a complete, still-valid file.
	raw, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}
	var got mcpSettingsFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if !got.EnableAllProjectMcpServers || !got.SkipDangerousModePermissionPrompt {
		t.Errorf("settings after rewrite = %+v, want both fields true (%s)", got, raw)
	}
}

// TestWriteMCPSettings_RejectsNonCanonicalID (#1518): a warm-start bootstrap id
// comes straight out of the decoded registry with no shape check anywhere on that
// path, and after #1518 it names a file — so an id carrying a separator or a ".."
// segment would be a path-traversal primitive. The data-dir branch gates on
// ValidID and fails loudly, matching loadRegistry's "a malformed file is a hard
// error" posture; a silent fallback to a temp file would hide a corrupt registry.
func TestWriteMCPSettings_RejectsNonCanonicalID(t *testing.T) {
	t.Parallel()

	control, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "traversal", id: "../escape", wantErr: true},
		{name: "separator", id: "a/b", wantErr: true},
		{name: "empty", id: "", wantErr: true},
		{name: "not a uuid", id: "not-a-uuid", wantErr: true},
		// Control row: a rejection table whose every row errors proves nothing if
		// the writer is broken outright.
		{name: "canonical control", id: string(control)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dataDir := filepath.Join(parent, "data")
			regPath := filepath.Join(dataDir, "sessions.json")

			path, err := writeMCPSettings(regPath, SessionID(tc.id))
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("writeMCPSettings: %v", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Errorf("control row wrote no file at %q: %v", path, err)
				}
				return
			}

			if err == nil {
				t.Fatalf("writeMCPSettings(%q) = %q, nil; want an error", tc.id, path)
			}
			if path != "" {
				t.Errorf("writeMCPSettings(%q) returned path %q alongside its error, want \"\"", tc.id, path)
			}

			// Nothing created anywhere under the data dir's parent — a rejected id
			// must not even reach MkdirAll, let alone a write outside the data dir.
			var found []string
			if err := filepath.WalkDir(parent, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					found = append(found, p)
				}
				return nil
			}); err != nil {
				t.Fatalf("walk %q: %v", parent, err)
			}
			if len(found) != 0 {
				t.Errorf("rejected id %q left files behind: %v", tc.id, found)
			}
		})
	}
}

// TestWriteMCPSettings_RelativeRegistryPathIsAbsolute (#1518): resolveRegistryPath
// documents a CWD-relative fallback when $HOME is unresolvable, and a relative
// --settings value would be resolved by claude against the SESSION's spawn
// workdir rather than pyry's — pointing the child at a nonexistent file, which is
// exactly the #943 wedge this file exists to prevent. os.CreateTemp("") was
// absolute for free, so the relocation introduces the hazard and must close it.
//
// No t.Parallel: t.Chdir is incompatible with parallel tests.
func TestWriteMCPSettings_RelativeRegistryPathIsAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	path, err := writeMCPSettings(filepath.Join("data", "sessions.json"), id)
	if err != nil {
		t.Fatalf("writeMCPSettings: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("writeMCPSettings path = %q, want absolute", path)
	}
	if got, want := filepath.Base(path), string(id)+".json"; got != want {
		t.Errorf("settings file base = %q, want %q", got, want)
	}
	if got := filepath.Base(filepath.Dir(path)); got != "session-settings" {
		t.Errorf("settings file parent dir = %q, want %q", got, "session-settings")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("stat %q: %v", path, err)
	}
}
