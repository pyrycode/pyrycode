package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type memorySearchTestRunner struct {
	stubRunner
	mu                 sync.Mutex
	workspace          string
	path               string
	home               string
	generation         uint64
	current            bool
	status             turnevent.MCPStatus
	statusOK           bool
	waitForQueryCancel bool
	onQuery            func()
	queries            int
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

func (r *memorySearchTestRunner) QueryMCPStatus(ctx context.Context) (turnevent.MCPStatus, bool) {
	r.mu.Lock()
	r.queries++
	onQuery := r.onQuery
	status, ok := r.status, r.statusOK
	wait := r.waitForQueryCancel
	r.mu.Unlock()
	if wait {
		<-ctx.Done()
		return turnevent.MCPStatus{}, false
	}
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

func TestMemorySearchFor_ProductionClaudeAdapter(t *testing.T) {
	workDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claudeBin := filepath.Join(workDir, "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexec sleep 3600\n"), 0700); err != nil {
		t.Fatal(err)
	}
	pool, err := sessions.New(sessions.Config{
		Bootstrap:     sessions.SessionConfig{ClaudeBin: claudeBin, WorkDir: workDir},
		RegistryPath:  filepath.Join(workDir, "sessions.json"),
		RunnerFactory: newStreamRunnerFactory(newStreamTurnSink(8, discardLogger()), "", nil, streamApprovalConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := &conversations.Registry{}
	id := memorySearchBind(t, reg, pool, "conv-production-claude", workDir, "claude")
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	runner := sess.Runner()
	launcher, ok := runner.(memorySearchLauncher)
	if !ok {
		t.Fatalf("production runner %T does not expose its child launch", runner)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("production runner did not stop")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if gotDir, _, _, _, live := launcher.MemorySearchLaunch(); live {
			if gotDir != workDir {
				t.Fatalf("child workspace = %q, want %q", gotDir, workDir)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("production child never exposed its launch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	configPath := filepath.Join(workDir, "config.json")
	memorySearchWriteConfig(t, configPath, memorySearchDeclaration("scoped-search", "claude", workDir, true))
	report, err := memorySearchFor(reg, pool, configPath)(context.Background(), "conv-production-claude", string(id))
	found := false
	for _, provider := range report.Providers {
		found = found || provider.ID == "scoped-search" && provider.Availability == "available"
	}
	if err != nil || report.Availability != "available" || !found {
		t.Fatalf("production Claude report = (%#v, %v)", report, err)
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

func TestMemorySearchFor_SettingsReplySurvivesDetectorError(t *testing.T) {
	reg, pool, runners, dir, _ := memorySearchTestBinding(t)
	id := memorySearchBind(t, reg, pool, "conv-settings", dir, "claude")
	model, effort := "claude-sonnet-test", "high"
	if err := pool.UpdateSettings(id, sessions.SettingsUpdate{Model: &model, Effort: &effort}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	memorySearchWriteConfig(t, configPath, memorySearchDeclaration("search", "claude", dir, true))
	// An invalid effective launch workspace makes Detect fail after the settings
	// resolver has found the ordinary saved fields.
	runners[id].mu.Lock()
	runners[id].workspace = "relative"
	runners[id].mu.Unlock()
	w := relayWiring{
		runSettings: func(convID string) (boundRunSettings, bool) {
			return resolveBoundRunSettings(reg, runSettingsPool{Pool: pool}, convID)
		},
		memorySearchFor: memorySearchFor(reg, pool, configPath),
	}
	if !strings.Contains(formattedGoFunc(t, "relay.go", "startRelayV2"), "MemorySearchFor: w.memorySearchFor") ||
		!strings.Contains(formattedGoFunc(t, "main.go", "runSupervisor"), "memorySearchFor: memorySearchFor(convReg, pool, resolveConfigPath())") {
		t.Fatal("production memory search provider is not attached to settings wiring")
	}

	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	phoneKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const token = "memory-search-settings-test-token"
	paired := &devices.Registry{}
	paired.Add(devices.Device{TokenHash: devices.HashToken(token), Name: "test phone", PairedAt: time.Now().UTC()})
	frames := make(chan protocol.RoutingEnvelope, 4)
	outbound := make(chan protocol.RoutingEnvelope, 8)
	mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{
		Frames:          frames,
		Outbound:        func(env protocol.RoutingEnvelope) error { outbound <- env; return nil },
		StaticPriv:      serverKey.Bytes(),
		Devices:         paired,
		ServerID:        string(identity.NewServerID()),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		RunConfigFor:    runConfigFor(w.runSettings, func(string) (int, int) { return 123, 456 }),
		MemorySearchFor: w.memorySearchFor,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Run(ctx) }()
	defer func() { cancel(); <-done }()

	initiator, err := noise.NewInitiator(phoneKey.Bytes(), serverKey.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	helloPayload, err := json.Marshal(protocol.HelloClientPayload{
		Role: "client", DeviceName: "test phone", ClientVersion: "v2-test",
		ProtocolVersions: []string{"v2"}, Token: token,
		Capabilities: []string{protocol.CapabilityInteractive},
	})
	if err != nil {
		t.Fatal(err)
	}
	hello, err := json.Marshal(protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: helloPayload})
	if err != nil {
		t.Fatal(err)
	}
	initMsg, err := initiator.WriteInit(hello)
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(kind string, data []byte) protocol.RoutingEnvelope {
		t.Helper()
		frame, err := json.Marshal(protocol.InnerFrameV2{Version: protocol.V2Version, Type: kind, Data: base64.StdEncoding.EncodeToString(data)})
		if err != nil {
			t.Fatal(err)
		}
		return protocol.RoutingEnvelope{ConnID: "settings-conn", Frame: frame}
	}
	await := func() protocol.InnerFrameV2 {
		t.Helper()
		select {
		case env := <-outbound:
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(env.Frame, &inner); err != nil {
				t.Fatal(err)
			}
			return inner
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for settings frame")
			return protocol.InnerFrameV2{}
		}
	}
	frames <- wrap(protocol.TypeNoiseInit, initMsg)
	response := await()
	if response.Type != protocol.TypeNoiseResp {
		t.Fatalf("handshake response type = %q", response.Type)
	}
	responseBytes, err := base64.StdEncoding.DecodeString(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	_, send, receive, err := initiator.ReadResp(responseBytes)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(protocol.Envelope{
		ID: 2, Type: protocol.TypeRequestSessionSettings, TS: time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"conv-settings"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := send.Encrypt(request)
	if err != nil {
		t.Fatal(err)
	}
	frames <- wrap(protocol.TypeNoiseMsg, sealed)
	replyFrame := await()
	if replyFrame.Type != protocol.TypeNoiseMsg {
		t.Fatalf("settings reply frame type = %q", replyFrame.Type)
	}
	encoded, err := base64.StdEncoding.DecodeString(replyFrame.Data)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := receive.Decrypt(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var reply protocol.Envelope
	if err := json.Unmarshal(plaintext, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Type != protocol.TypeSessionSettings || reply.InReplyTo == nil || *reply.InReplyTo != 2 {
		t.Fatalf("settings reply = %#v", reply)
	}
	var settings protocol.SessionSettingsPayload
	if err := json.Unmarshal(reply.Payload, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.SessionID != string(id) || settings.Model != model || settings.Effort != effort ||
		settings.UsedTokens != 123 || settings.WindowTokens != 456 {
		t.Errorf("saved settings changed on detector failure: %#v", settings)
	}
	if settings.MemorySearch == nil || settings.MemorySearch.Availability != "unknown" || len(settings.MemorySearch.Providers) != 0 {
		t.Errorf("detector failure memory_search = %#v, want present unknown", settings.MemorySearch)
	}

	// An unresponsive bound child must not hold the ordinary settings reply
	// until the connection's much longer request context expires.
	memorySearchWriteConfig(t, configPath)
	runners[id].mu.Lock()
	queriesBefore := runners[id].queries
	runners[id].workspace = dir
	runners[id].waitForQueryCancel = true
	runners[id].mu.Unlock()
	request, err = json.Marshal(protocol.Envelope{
		ID: 3, Type: protocol.TypeRequestSessionSettings, TS: time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"conv-settings"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err = send.Encrypt(request)
	if err != nil {
		t.Fatal(err)
	}
	frames <- wrap(protocol.TypeNoiseMsg, sealed)
	replyFrame = await()
	encoded, err = base64.StdEncoding.DecodeString(replyFrame.Data)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err = receive.Decrypt(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(plaintext, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Type != protocol.TypeSessionSettings || reply.InReplyTo == nil || *reply.InReplyTo != 3 {
		t.Fatalf("settings reply after unresponsive child = %#v", reply)
	}
	if err := json.Unmarshal(reply.Payload, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.SessionID != string(id) || settings.Model != model || settings.Effort != effort ||
		settings.UsedTokens != 123 || settings.WindowTokens != 456 ||
		settings.MemorySearch == nil || settings.MemorySearch.Availability != "unknown" ||
		len(settings.MemorySearch.Providers) != 0 {
		t.Errorf("settings after unresponsive child = %#v", settings)
	}
	runners[id].mu.Lock()
	queries := runners[id].queries
	runners[id].mu.Unlock()
	if queries != queriesBefore+1 {
		t.Errorf("unresponsive child queried %d times after %d prior queries, want one more", queries, queriesBefore)
	}
}
