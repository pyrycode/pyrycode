package memorysearch

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testInput(t *testing.T) Input {
	t.Helper()
	path := ""
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Agent: "claude", Workspace: workspace, Config: config.DefaultConfig(),
		Launch: LaunchEvidence{
			Agent: "claude", Workspace: workspace, PluginsKnown: true, Plugins: []Plugin{},
			ChildPATH: &path, ChildRunsAsDaemon: true, HostCLIsKnown: true, MCPStatus: &turnevent.MCPStatus{}, MCPStatusEffective: true,
		},
	}
}

func testResult(t *testing.T, in Input, want Availability, providers ...Provider) {
	t.Helper()
	got, err := Detect(in)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got.Availability != want || !slices.Equal(got.Providers, providers) {
		t.Errorf("Detect = %#v, want availability %q providers %#v", got, want, providers)
	}
}

func TestDetectBuiltins(t *testing.T) {
	t.Run("memsearch plugin", func(t *testing.T) {
		in := testInput(t)
		in.Launch.Plugins = []Plugin{{ID: "memsearch@search", Enabled: true}}
		testResult(t, in, Available, Provider{ID: "memsearch", DisplayName: "Memsearch", Installed: true, Enabled: true, Availability: Available})
	})
	t.Run("memsearch CLI on child PATH", func(t *testing.T) {
		in := testInput(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "memsearch"), []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
		in.Launch.ChildPATH = &dir
		testResult(t, in, Available, Provider{ID: "memsearch", DisplayName: "Memsearch", Installed: true, Enabled: true, Availability: Available})
	})
	t.Run("owned CLI without owner execute permission", func(t *testing.T) {
		in := testInput(t)
		dir := t.TempDir()
		candidate := filepath.Join(dir, "memsearch")
		if err := os.WriteFile(candidate, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(candidate, 0o010); err != nil {
			t.Fatal(err)
		}
		in.Launch.ChildPATH = &dir
		testResult(t, in, Unavailable, Provider{ID: "memsearch", DisplayName: "Memsearch", Installed: true, Availability: Unavailable})
	})
	t.Run("QMD CLI", func(t *testing.T) {
		in := testInput(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "qmd"), []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
		in.Launch.ChildPATH = &dir
		testResult(t, in, Available, Provider{ID: "qmd", DisplayName: "QMD", Installed: true, Enabled: true, Availability: Available})
	})
	t.Run("QMD effective MCP", func(t *testing.T) {
		in := testInput(t)
		in.Launch.MCPStatus = &turnevent.MCPStatus{Servers: []turnevent.MCPServerStatus{{Name: "qmd", Status: "connected"}}}
		testResult(t, in, Available, Provider{ID: "qmd", DisplayName: "QMD", Installed: true, Enabled: true, Availability: Available})
	})
	t.Run("Smart Connections bridge", func(t *testing.T) {
		in := testInput(t)
		in.Launch.MCPStatus = &turnevent.MCPStatus{Servers: []turnevent.MCPServerStatus{{Name: "smart-connections", Status: "connected"}}}
		testResult(t, in, Available, Provider{ID: "smart-connections", DisplayName: "Smart Connections", Installed: true, Enabled: true, Availability: Available})
	})
	t.Run("unrelated memory and Obsidian plugins", func(t *testing.T) {
		in := testInput(t)
		in.Launch.Plugins = []Plugin{{ID: "obsidian-smart-connections", Enabled: true}, {ID: "memory-tools", Enabled: true}}
		in.Launch.MCPStatus = &turnevent.MCPStatus{Servers: []turnevent.MCPServerStatus{{Name: "memory-helper", Status: "connected"}}}
		testResult(t, in, Absent)
	})
}

func TestDetectDeclarationsAndStates(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	t.Run("custom declaration", func(t *testing.T) {
		in := testInput(t)
		in.Config.MemorySearchProviders = []config.MemorySearchProvider{{ID: "my-search", DisplayName: "My Search", Agent: "claude", Workspace: in.Workspace, Enabled: boolPtr(true)}}
		testResult(t, in, Available, Provider{ID: "my-search", DisplayName: "My Search", Installed: true, Enabled: true, Availability: Available})
	})
	t.Run("Smart Connections scoped declaration", func(t *testing.T) {
		in := testInput(t)
		in.Config.MemorySearchProviders = []config.MemorySearchProvider{{ID: "smart-connections", DisplayName: "Smart Connections", Agent: "claude", Workspace: in.Workspace, Enabled: boolPtr(true)}}
		testResult(t, in, Available, Provider{ID: "smart-connections", DisplayName: "Smart Connections", Installed: true, Enabled: true, Availability: Available})
	})
	t.Run("disabled declaration", func(t *testing.T) {
		in := testInput(t)
		in.Config.MemorySearchProviders = []config.MemorySearchProvider{{ID: "my-search", DisplayName: "My Search", Agent: "claude", Workspace: in.Workspace, Enabled: boolPtr(false)}}
		testResult(t, in, Unavailable, Provider{ID: "my-search", DisplayName: "My Search", Installed: true, Availability: Unavailable})
	})
	t.Run("disabled plugin", func(t *testing.T) {
		in := testInput(t)
		in.Launch.Plugins = []Plugin{{ID: "memsearch", Enabled: false}}
		testResult(t, in, Unavailable, Provider{ID: "memsearch", DisplayName: "Memsearch", Installed: true, Availability: Unavailable})
	})
	t.Run("failed MCP", func(t *testing.T) {
		in := testInput(t)
		in.Launch.MCPStatus = &turnevent.MCPStatus{Servers: []turnevent.MCPServerStatus{{Name: "qmd", Status: "failed"}}}
		testResult(t, in, Unavailable, Provider{ID: "qmd", DisplayName: "QMD", Installed: true, Enabled: true, Availability: Unavailable})
	})
	t.Run("host-only CLI", func(t *testing.T) {
		in := testInput(t)
		in.Launch.HostCLIs = []string{"qmd"}
		testResult(t, in, Unknown, Provider{ID: "qmd", DisplayName: "QMD", Installed: true, Availability: Unknown})
	})
	t.Run("missing live MCP status", func(t *testing.T) {
		in := testInput(t)
		in.Launch.MCPStatus = nil
		testResult(t, in, Unknown)
	})
	t.Run("unchecked host installation", func(t *testing.T) {
		in := testInput(t)
		in.Launch.HostCLIsKnown = false
		testResult(t, in, Unknown)
	})
	t.Run("uncheckable child PATH", func(t *testing.T) {
		in := testInput(t)
		path := "relative-dir"
		in.Launch.ChildPATH = &path
		testResult(t, in, Unknown)
	})
	t.Run("child credentials unproven", func(t *testing.T) {
		in := testInput(t)
		in.Launch.ChildRunsAsDaemon = false
		testResult(t, in, Unknown)
	})
	t.Run("unreadable config", func(t *testing.T) {
		in := testInput(t)
		in.ConfigErr = errors.New("denied")
		testResult(t, in, Unknown)
	})
	t.Run("incomplete declaration", func(t *testing.T) {
		in := testInput(t)
		in.Config.MemorySearchProviders = []config.MemorySearchProvider{{ID: "my-search", DisplayName: "My Search", Agent: "claude", Workspace: in.Workspace}}
		testResult(t, in, Unknown)
	})
	t.Run("truncated MCP status", func(t *testing.T) {
		in := testInput(t)
		in.Launch.MCPStatus.DroppedServers = 1
		testResult(t, in, Unknown)
	})
	t.Run("available wins over incomplete evidence", func(t *testing.T) {
		in := testInput(t)
		in.ConfigErr = errors.New("denied")
		in.Launch.MCPStatus = nil
		in.Launch.Plugins = []Plugin{{ID: "memsearch", Enabled: true}}
		testResult(t, in, Available, Provider{ID: "memsearch", DisplayName: "Memsearch", Installed: true, Enabled: true, Availability: Available})
	})
}

func TestDetectIsolation(t *testing.T) {
	t.Run("other agent and workspace declaration", func(t *testing.T) {
		in := testInput(t)
		yes := true
		in.Config.MemorySearchProviders = []config.MemorySearchProvider{
			{ID: "other-agent", DisplayName: "Other Agent", Agent: "codex", Workspace: in.Workspace, Enabled: &yes},
			{ID: "other-workspace", DisplayName: "Other Workspace", Agent: "claude", Workspace: canonicalTempDir(t), Enabled: &yes},
		}
		testResult(t, in, Absent)
	})
	t.Run("unrelated missing declaration workspace", func(t *testing.T) {
		in := testInput(t)
		yes := true
		in.Config.MemorySearchProviders = []config.MemorySearchProvider{{ID: "other-workspace", DisplayName: "Other Workspace", Agent: "claude", Workspace: filepath.Join(in.Workspace, "missing"), Enabled: &yes}}
		testResult(t, in, Absent)
	})
	t.Run("excluded MCP status", func(t *testing.T) {
		in := testInput(t)
		in.Launch.MCPStatusEffective = false
		in.Launch.MCPStatus = &turnevent.MCPStatus{Servers: []turnevent.MCPServerStatus{{Name: "qmd", Status: "connected", Scope: "user"}}}
		testResult(t, in, Unknown)
	})
	t.Run("Codex home mismatch", func(t *testing.T) {
		in := testInput(t)
		in.Agent, in.Launch.Agent = "codex", "codex"
		in.CodexHome = canonicalTempDir(t)
		in.Launch.CodexHome = canonicalTempDir(t)
		in.Launch.Plugins = []Plugin{{ID: "memsearch", Enabled: true}}
		testResult(t, in, Unknown)
	})
	t.Run("Codex matching daemon home", func(t *testing.T) {
		in := testInput(t)
		in.Agent, in.Launch.Agent = "codex", "codex"
		in.CodexHome = canonicalTempDir(t)
		in.Launch.CodexHome = in.CodexHome
		in.Launch.Plugins = []Plugin{{ID: "memsearch", Enabled: true}}
		testResult(t, in, Available, Provider{ID: "memsearch", DisplayName: "Memsearch", Installed: true, Enabled: true, Availability: Available})
	})
	t.Run("other launch agent", func(t *testing.T) {
		in := testInput(t)
		in.Launch.Agent = "codex"
		in.Launch.Plugins = []Plugin{{ID: "memsearch", Enabled: true}}
		testResult(t, in, Unknown)
	})
	t.Run("excluded user MCP registration", func(t *testing.T) {
		in := testInput(t)
		in.Launch.MCPStatus = &turnevent.MCPStatus{Servers: []turnevent.MCPServerStatus{{Name: "qmd", Status: "connected", Scope: "user"}}}
		testResult(t, in, Absent)
	})
	t.Run("invalid selected workspace", func(t *testing.T) {
		in := testInput(t)
		in.Workspace = "relative"
		if _, err := Detect(in); err == nil {
			t.Fatal("Detect accepted relative workspace")
		}
	})
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
