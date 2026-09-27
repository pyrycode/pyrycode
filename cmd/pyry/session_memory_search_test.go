package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type memorySearchTestRunner struct {
	stubRunner
	mu         sync.Mutex
	workspace  string
	path       string
	home       string
	generation uint64
	current    bool
	status     turnevent.MCPStatus
	statusOK   bool
	onQuery    func()
	queries    int
}

func (r *memorySearchTestRunner) MemorySearchLaunch() (string, *string, string, uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.current {
		return "", nil, "", 0, false
	}
	path := r.path
	return r.workspace, &path, r.home, r.generation, true
}

func (r *memorySearchTestRunner) QueryMCPStatus(context.Context) (turnevent.MCPStatus, bool) {
	r.mu.Lock()
	r.queries++
	onQuery := r.onQuery
	status, ok := r.status, r.statusOK
	r.mu.Unlock()
	if onQuery != nil {
		onQuery()
	}
	return status, ok
}

func memorySearchTestBinding(t *testing.T) (*conversations.Registry, *sessions.Pool, map[sessions.SessionID]*memorySearchTestRunner, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(base, "claude")
	codexDir := filepath.Join(base, "codex")
	for _, dir := range []string{claudeDir, codexDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	runners := make(map[sessions.SessionID]*memorySearchTestRunner)
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: claudeDir},
		RegistryPath: filepath.Join(base, "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			r := &memorySearchTestRunner{workspace: cfg.WorkDir, generation: 1, current: true, statusOK: true}
			if cfg.Harness == "codex" {
				r.home = base
			}
			runners[sessions.SessionID(cfg.SessionID)] = r
			return r, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := &conversations.Registry{}
	return reg, pool, runners, claudeDir, codexDir
}

func memorySearchBind(t *testing.T, reg *conversations.Registry, pool *sessions.Pool, convID, dir, agent string) sessions.SessionID {
	t.Helper()
	id, err := pool.MintAs(convID, dir, agent)
	if err != nil && err != sessions.ErrPoolNotRunning {
		t.Fatal(err)
	}
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(convID), CurrentSessionID: string(id), LastUsedAt: time.Now().UTC()})
	return id
}

func memorySearchWriteConfig(t *testing.T, path string, declarations ...config.MemorySearchProvider) {
	t.Helper()
	b, err := json.Marshal(config.Config{MemorySearchProviders: declarations})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func memorySearchDeclaration(id, agent, workspace string, enabled bool) config.MemorySearchProvider {
	return config.MemorySearchProvider{ID: id, DisplayName: id, Agent: agent, Workspace: workspace, Enabled: &enabled}
}

func TestMemorySearchFor_ScopesAndReloads(t *testing.T) {
	reg, pool, runners, claudeDir, codexDir := memorySearchTestBinding(t)
	claudeID := memorySearchBind(t, reg, pool, "conv-claude", claudeDir, "claude")
	codexID := memorySearchBind(t, reg, pool, "conv-codex", codexDir, "codex")
	configPath := filepath.Join(t.TempDir(), "config.json")
	memorySearchWriteConfig(t, configPath,
		memorySearchDeclaration("claude-search", "claude", claudeDir, true),
		memorySearchDeclaration("codex-search", "codex", codexDir, false))
	provider := memorySearchFor(reg, pool, configPath)
	assertReport := func(conv string, id sessions.SessionID, wantStatus, wantProvider string) {
		t.Helper()
		got, err := provider(context.Background(), conv, string(id))
		if err != nil {
			t.Fatal(err)
		}
		if got.Availability != wantStatus || len(got.Providers) != 1 || got.Providers[0].ID != wantProvider {
			t.Fatalf("%s report = %#v, want %s and %s only", conv, got, wantStatus, wantProvider)
		}
	}
	assertReport("conv-claude", claudeID, "available", "claude-search")
	assertReport("conv-codex", codexID, "unknown", "codex-search")
	ignored, err := provider(context.Background(), "conv-codex", string(codexID))
	if err != nil || !ignored.Providers[0].Installed || ignored.Providers[0].Enabled || ignored.Providers[0].Availability != "unavailable" {
		t.Fatalf("disabled install = (%#v,%v)", ignored, err)
	}
	if runners[claudeID].queries != 1 || runners[codexID].queries != 0 {
		t.Fatalf("MCP queries: Claude=%d Codex=%d", runners[claudeID].queries, runners[codexID].queries)
	}
	memorySearchWriteConfig(t, configPath, memorySearchDeclaration("replacement", "claude", claudeDir, true))
	assertReport("conv-claude", claudeID, "available", "replacement")
	got, err := provider(context.Background(), "conv-claude", string(codexID))
	if err != nil || got.Availability != "unknown" || len(got.Providers) != 0 {
		t.Fatalf("mismatched binding = (%#v,%v)", got, err)
	}
}

func TestMemorySearchFor_RejectsChangedBindingAndChild(t *testing.T) {
	reg, pool, runners, claudeDir, _ := memorySearchTestBinding(t)
	id := memorySearchBind(t, reg, pool, "conv-changing", claudeDir, "claude")
	other := memorySearchBind(t, reg, pool, "conv-other", claudeDir, "claude")
	configPath := filepath.Join(t.TempDir(), "config.json")
	memorySearchWriteConfig(t, configPath, memorySearchDeclaration("search", "claude", claudeDir, true))
	provider := memorySearchFor(reg, pool, configPath)
	for _, tc := range []struct {
		name   string
		change func()
	}{
		{"binding", func() {
			reg.Update("conv-changing", func(c *conversations.Conversation) { c.CurrentSessionID = string(other) })
		}},
		{"child", func() { runners[id].mu.Lock(); runners[id].generation++; runners[id].mu.Unlock() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg.Update("conv-changing", func(c *conversations.Conversation) { c.CurrentSessionID = string(id) })
			runners[id].mu.Lock()
			runners[id].onQuery = tc.change
			runners[id].mu.Unlock()
			got, err := provider(context.Background(), "conv-changing", string(id))
			if err != nil || got.Availability != "unknown" || len(got.Providers) != 0 {
				t.Fatalf("changed %s = (%#v,%v)", tc.name, got, err)
			}
		})
	}
	newWorkspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	memorySearchWriteConfig(t, configPath,
		memorySearchDeclaration("old-scope", "claude", claudeDir, true),
		memorySearchDeclaration("new-scope", "claude", newWorkspace, true))
	runners[id].mu.Lock()
	runners[id].workspace = newWorkspace
	runners[id].onQuery = nil
	runners[id].mu.Unlock()
	got, err := provider(context.Background(), "conv-changing", string(id))
	if err != nil || got.Availability != "available" || len(got.Providers) != 1 || got.Providers[0].ID != "new-scope" {
		t.Fatalf("replacement child scope = (%#v,%v)", got, err)
	}
	if runners[other].queries != 0 {
		t.Fatalf("other child queried %d times", runners[other].queries)
	}
}

func TestMemorySearchFor_IncompleteEvidenceAndDetectorError(t *testing.T) {
	reg, pool, runners, claudeDir, _ := memorySearchTestBinding(t)
	id := memorySearchBind(t, reg, pool, "conv-incomplete", claudeDir, "claude")
	configPath := filepath.Join(t.TempDir(), "config.json")
	provider := memorySearchFor(reg, pool, configPath)
	got, err := provider(context.Background(), "conv-incomplete", string(id))
	if err != nil || got.Availability != "unknown" {
		t.Fatalf("incomplete = (%#v,%v)", got, err)
	}
	if err := os.WriteFile(configPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	runners[id].mu.Lock()
	runners[id].status = turnevent.MCPStatus{Servers: []turnevent.MCPServerStatus{{Name: "qmd", Status: "connected", Scope: "local"}}}
	runners[id].mu.Unlock()
	got, err = provider(context.Background(), "conv-incomplete", string(id))
	if err != nil || got.Availability != "available" || len(got.Providers) != 1 || got.Providers[0].ID != "qmd" {
		t.Fatalf("usable MCP with unreadable config = (%#v,%v)", got, err)
	}
	runners[id].mu.Lock()
	runners[id].workspace = "relative"
	runners[id].mu.Unlock()
	got, err = provider(context.Background(), "conv-incomplete", string(id))
	if err != nil || got.Availability != "unknown" || len(got.Providers) != 0 {
		t.Fatalf("invalid launch = (%#v,%v)", got, err)
	}
}
