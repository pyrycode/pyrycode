package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestConversationAgentSwitch_RebindsAndRetainsHistory(t *testing.T) {
	pool, plan := newDormantWritePool(t, "")
	plan.arm(dormantWriteBootID, claudeEntries)
	var transitions []sessions.SessionTransition
	pool.SetTransitionObserver(func(t sessions.SessionTransition) { transitions = append(transitions, t) })
	runPoolReady(t, pool)
	oldID, err := pool.MintWith("conv-1", "", protocol.AgentClaude, sessions.SessionSettings{Model: "opus", Effort: "low", PermissionMode: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-1", Cwd: "", CurrentSessionID: string(oldID), SessionHistory: []string{"earlier"}})
	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	sw := conversationAgentSwitcher{pool: pool, conversations: reg, registryPath: path, reset: &conversationReset{}}
	newID, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if newID == "" || newID == oldID {
		t.Fatalf("new ID = %q, old = %q", newID, oldID)
	}
	got, _ := reg.Get("conv-1")
	if got.CurrentSessionID != string(newID) || len(got.SessionHistory) != 2 || got.SessionHistory[1] != string(oldID) {
		t.Fatalf("conversation = %+v", got)
	}
	if h, err := pool.HarnessFor(newID); err != nil || h != protocol.AgentCodex {
		t.Fatalf("harness = %q, %v", h, err)
	}
	if _, err := pool.HarnessFor(oldID); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Fatalf("old session remains: %v", err)
	}
	if len(transitions) != 1 || transitions[0].Reason != sessions.ReasonClear || transitions[0].PreviousID != oldID || transitions[0].NewID != newID {
		t.Fatalf("transitions = %+v", transitions)
	}
	persisted, err := conversations.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := persisted.Get("conv-1")
	if saved.CurrentSessionID != string(newID) {
		t.Fatalf("saved binding = %+v", saved)
	}
}

func dormantSwitchFixture(t *testing.T, fields string) (*sessions.Pool, *conversations.Registry, conversationAgentSwitcher, string) {
	t.Helper()
	pool, plan := newDormantWritePool(t, fields)
	plan.arm(dormantWriteBootID, claudeEntries)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-1", CurrentSessionID: dormantWriteTargetID})
	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	return pool, reg, conversationAgentSwitcher{pool: pool, conversations: reg, registryPath: path, reset: &conversationReset{}}, path
}

func TestConversationAgentSwitch_DormantOldSession(t *testing.T) {
	pool, reg, sw, path := dormantSwitchFixture(t, `"model":"opus","effort":"low","permission_mode":"plan",`)
	runPoolReady(t, pool)
	newID, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.HarnessFor(dormantWriteTargetID); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Fatalf("dormant old entry remains: %v", err)
	}
	settings, err := pool.SettingsFor(newID)
	if err != nil || settings.Effort != "" || settings.PermissionMode != "plan" || settings.YOLO {
		t.Fatalf("new settings = %+v, %v", settings, err)
	}
	stored, err := conversations.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := stored.Get("conv-1")
	if row.CurrentSessionID != string(newID) || len(row.SessionHistory) != 1 || row.SessionHistory[0] != dormantWriteTargetID {
		t.Fatalf("persisted row = %+v", row)
	}
	if got, _ := reg.Get("conv-1"); got.CurrentSessionID != string(newID) {
		t.Fatalf("live row = %+v", got)
	}
}

func TestConversationAgentSwitch_RefusalsLeaveBinding(t *testing.T) {
	bad := "unoffered"
	cases := []struct {
		name, convID, target, cwd string
		model, effort             *string
		unbound                   bool
		want                      error
	}{
		{name: "unknown", convID: "absent", target: protocol.AgentCodex, want: conversations.ErrConversationNotFound},
		{name: "unbound", convID: "conv-1", target: protocol.AgentCodex, unbound: true, want: conversations.ErrConversationNotFound},
		{name: "same agent", convID: "conv-1", target: protocol.AgentClaude, want: ErrAgentSwitchSameAgent},
		{name: "model", convID: "conv-1", target: protocol.AgentCodex, model: &bad, want: relay.ErrModelNotOffered},
		{name: "effort", convID: "conv-1", target: protocol.AgentCodex, effort: &bad, want: relay.ErrEffortNotOffered},
		{name: "workspace", convID: "conv-1", target: protocol.AgentCodex, cwd: "/", want: handlers.ErrSpawnDirRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, reg, sw, path := dormantSwitchFixture(t, `"model":"opus",`)
			var transitions []sessions.SessionTransition
			pool.SetTransitionObserver(func(t sessions.SessionTransition) { transitions = append(transitions, t) })
			store := &agentVocabularyDouble{codex: codexEntries}
			sw.saved = store
			if tc.unbound {
				reg.Update("conv-1", func(c *conversations.Conversation) { c.CurrentSessionID = "" })
			}
			if tc.cwd != "" {
				reg.Update("conv-1", func(c *conversations.Conversation) { c.Cwd = tc.cwd })
				if err := reg.Save(path); err != nil {
					t.Fatal(err)
				}
			}
			before := pool.List()
			id, err := sw.Switch(context.Background(), tc.convID, tc.target, tc.model, tc.effort)
			if id != "" || !errors.Is(err, tc.want) {
				t.Fatalf("Switch = %q, %v; want empty, %v", id, err, tc.want)
			}
			got, _ := reg.Get("conv-1")
			wantID := dormantWriteTargetID
			if tc.unbound {
				wantID = ""
			}
			if got.CurrentSessionID != wantID || len(got.SessionHistory) != 0 || len(pool.List()) != len(before) || len(transitions) != 0 {
				t.Fatalf("refusal mutated row/pool: %+v, %+v", got, pool.List())
			}
			if _, err := pool.HarnessFor(dormantWriteTargetID); err != nil {
				t.Fatalf("refusal removed old session: %v", err)
			}
		})
	}
}

func TestConversationAgentSwitch_MintErrorWithIDCleansUp(t *testing.T) {
	pool, reg, sw, _ := dormantSwitchFixture(t, "")
	// Pool.Run has not started. MintWith persists an ID and returns it with
	// ErrPoolNotRunning; Switch must remove that ID before answering failure.
	id, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, nil, nil)
	if id != "" || !errors.Is(err, sessions.ErrPoolNotRunning) {
		t.Fatalf("Switch = %q, %v", id, err)
	}
	if got, _ := reg.Get("conv-1"); got.CurrentSessionID != dormantWriteTargetID || len(got.SessionHistory) != 0 {
		t.Fatalf("binding changed: %+v", got)
	}
	if len(pool.List()) != 1 {
		t.Fatalf("mint residue: %+v", pool.List())
	}
	if _, err := pool.HarnessFor(dormantWriteTargetID); err != nil {
		t.Fatalf("mint failure removed old session: %v", err)
	}
}

func TestConversationAgentSwitch_ConversationSaveFailureRollsBack(t *testing.T) {
	pool, reg, sw, _ := dormantSwitchFixture(t, "")
	runPoolReady(t, pool)
	sw.registryPath = filepath.Join(t.TempDir(), "missing", "conversations.json")
	// A file at the parent path makes Save fail even though it creates dirs.
	if err := os.WriteFile(filepath.Dir(sw.registryPath), []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, nil, nil)
	if id != "" || err == nil {
		t.Fatalf("Switch = %q, %v", id, err)
	}
	if got, _ := reg.Get("conv-1"); got.CurrentSessionID != dormantWriteTargetID || len(got.SessionHistory) != 0 {
		t.Fatalf("binding changed: %+v", got)
	}
	if len(pool.List()) != 1 {
		t.Fatalf("mint residue: %+v", pool.List())
	}
}

func TestConversationAgentSwitch_PostCommitRemovalErrorReportsNewID(t *testing.T) {
	pool, plan := newDormantWritePool(t, "")
	plan.arm(dormantWriteBootID, claudeEntries)
	var transitions []sessions.SessionTransition
	pool.SetTransitionObserver(func(t sessions.SessionTransition) { transitions = append(transitions, t) })
	poolCtx := runPoolReady(t, pool)
	oldID, err := pool.MintWith("conv-1", "", protocol.AgentClaude, sessions.SessionSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Activate(poolCtx, oldID); err != nil {
		t.Fatal(err)
	}
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-1", CurrentSessionID: string(oldID)})
	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sw := conversationAgentSwitcher{pool: pool, conversations: reg, registryPath: path, reset: &conversationReset{}}
	newID, err := sw.Switch(ctx, "conv-1", protocol.AgentCodex, nil, nil)
	if newID == "" || err == nil {
		t.Fatalf("Switch = %q, %v; want committed ID with removal error", newID, err)
	}
	got, _ := reg.Get("conv-1")
	if got.CurrentSessionID != string(newID) || len(got.SessionHistory) != 1 || len(transitions) != 1 {
		t.Fatalf("committed state = %+v; transitions = %+v", got, transitions)
	}
}

func TestConversationAgentSwitch_RequestedSettingsAndBypassRevocation(t *testing.T) {
	pool, reg, sw, _ := dormantSwitchFixture(t, `"model":"opus","effort":"high","yolo":true,`)
	sw.saved = &agentVocabularyDouble{codex: codexEntries}
	reg.Update("conv-1", func(c *conversations.Conversation) {
		name, prompt := "Channel", "Retain these instructions"
		c.Name, c.SystemPrompt = &name, &prompt
		c.SessionHistory = []string{"earlier"}
	})
	if err := reg.Save(sw.registryPath); err != nil {
		t.Fatal(err)
	}
	runPoolReady(t, pool)
	model, effort := "luna", "ultra"
	newID, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, &model, &effort)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := pool.SettingsFor(newID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != model || settings.Effort != effort || settings.YOLO || settings.PermissionMode != "default" {
		t.Fatalf("new settings = %+v", settings)
	}
	row, _ := reg.Get("conv-1")
	if row.Name == nil || *row.Name != "Channel" || row.SystemPrompt == nil || *row.SystemPrompt != "Retain these instructions" || len(row.SessionHistory) != 2 || row.SessionHistory[0] != "earlier" {
		t.Fatalf("conversation metadata changed: %+v", row)
	}
}

func TestConversationAgentSwitch_WorkspaceAndSessionRegistrySurviveRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(home, "project")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	plan := newModelListPlan()
	var newWorkDir string
	factory := func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		if cfg.Harness == protocol.AgentCodex {
			newWorkDir = cfg.WorkDir
		}
		return modelListRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
	}
	config := sessions.Config{Bootstrap: sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: home}, RegistryPath: regPath, RunnerFactory: factory}
	pool, err := sessions.New(config)
	if err != nil {
		t.Fatal(err)
	}
	runPoolReady(t, pool)
	oldID, err := pool.MintWith("conv-1", "", protocol.AgentClaude, sessions.SessionSettings{})
	if err != nil {
		t.Fatal(err)
	}
	convs := &conversations.Registry{}
	convs.Create(conversations.Conversation{ID: "conv-1", Cwd: workspace, CurrentSessionID: string(oldID)})
	convPath := filepath.Join(t.TempDir(), "conversations.json")
	if err := convs.Save(convPath); err != nil {
		t.Fatal(err)
	}
	sw := conversationAgentSwitcher{pool: pool, conversations: convs, registryPath: convPath, reset: &conversationReset{}}
	newID, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if newWorkDir != resolved {
		t.Fatalf("new runner workdir = %q, want %q", newWorkDir, resolved)
	}
	warm, err := sessions.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if harness, err := warm.HarnessFor(newID); err != nil || harness != protocol.AgentCodex {
		t.Fatalf("warm harness = %q, %v", harness, err)
	}
	if _, err := warm.HarnessFor(oldID); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Fatalf("warm old ID = %v", err)
	}
}

func TestConversationAgentSwitch_AbsentEffortRequiresTargetModelSupport(t *testing.T) {
	for _, tc := range []struct{ model, wantEffort string }{
		{"luna", "high"},
		{"sol", ""},
	} {
		t.Run(tc.model, func(t *testing.T) {
			pool, _, sw, _ := dormantSwitchFixture(t, `"effort":"high",`)
			sw.saved = &agentVocabularyDouble{codex: codexEntries}
			runPoolReady(t, pool)
			newID, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, &tc.model, nil)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := pool.SettingsFor(newID)
			if err != nil {
				t.Fatal(err)
			}
			if settings.Model != tc.model || settings.Effort != tc.wantEffort {
				t.Fatalf("settings = %+v, want model %q effort %q", settings, tc.model, tc.wantEffort)
			}
		})
	}
}
