package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	if f := transitions[0]; f.Cause != sessions.CauseAgentSwitch || f.ConversationID != "conv-1" || f.PreviousAgent != protocol.AgentClaude || f.NextAgent != protocol.AgentCodex || !f.AgentSwitch || f.PreviousID != oldID || f.NewID != newID || f.ResetHandoffOutcome != nil {
		t.Fatalf("switch fact = %+v", f)
	}

	// Switch back to prove the facts name actual agents in both directions.
	backID, err := sw.Switch(context.Background(), "conv-1", protocol.AgentClaude, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 2 {
		t.Fatalf("return switch facts = %+v", transitions)
	}
	if f := transitions[1]; f.PreviousAgent != protocol.AgentCodex || f.NextAgent != protocol.AgentClaude || f.PreviousID != newID || f.NewID != backID || f.ConversationID != "conv-1" || f.Cause != sessions.CauseAgentSwitch {
		t.Fatalf("return switch fact = %+v", f)
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
	emitter := newResettingEmitterV2(context.Background(), slog.Default())
	emitter.attach(newResettingBcast(interactiveConns()...))
	return pool, reg, conversationAgentSwitcher{pool: pool, conversations: reg, registryPath: path, reset: &conversationReset{}, resetting: emitter}, path
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
			if got.CurrentSessionID != wantID || len(got.SessionHistory) != 0 || len(pool.List()) != len(before) || len(transitions) != 0 || len(sw.resetting.bcast.(*resettingBcast).recorded()) != 0 {
				t.Fatalf("refusal mutated row/pool: %+v, %+v", got, pool.List())
			}
			if _, err := pool.HarnessFor(dormantWriteTargetID); err != nil {
				t.Fatalf("refusal removed old session: %v", err)
			}
		})
	}
}

func TestConversationAgentSwitch_WorkspaceRefusalOmitsPaths(t *testing.T) {
	pool, reg, sw, _ := dormantSwitchFixture(t, "")
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	reg.Update("conv-1", func(c *conversations.Conversation) { c.Cwd = outside })
	id, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, nil, nil)
	if id != "" || !errors.Is(err, handlers.ErrSpawnDirRejected) {
		t.Fatalf("Switch = %q, %v; want path-free workspace refusal", id, err)
	}
	for _, path := range []string{home, outside} {
		if strings.Contains(err.Error(), path) {
			t.Fatalf("workspace refusal contains path %q: %v", path, err)
		}
	}
	if got, _ := reg.Get("conv-1"); got.CurrentSessionID != dormantWriteTargetID {
		t.Fatalf("workspace refusal changed binding: %+v", got)
	}
	if got := pool.List(); len(got) != 1 {
		t.Fatalf("workspace refusal changed sessions: %+v", got)
	}
}

func TestConversationAgentSwitch_MintErrorWithIDCleansUp(t *testing.T) {
	pool, reg, sw, _ := dormantSwitchFixture(t, "")
	defer assertSwitchSkippedSequence(t, sw.resetting)
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

func TestConversationAgentSwitch_MintFailureOmitsWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(home, "private-project")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := newModelListPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: home},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			if cfg.Harness == protocol.AgentCodex {
				return nil, &os.PathError{Op: "chdir", Path: cfg.WorkDir, Err: os.ErrNotExist}
			}
			return modelListRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runPoolReady(t, pool)
	oldID, err := pool.MintWith("conv-1", home, protocol.AgentClaude, sessions.SessionSettings{})
	if err != nil {
		t.Fatal(err)
	}
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-1", Cwd: workspace, CurrentSessionID: string(oldID)})
	sw := conversationAgentSwitcher{pool: pool, conversations: reg, registryPath: filepath.Join(t.TempDir(), "conversations.json"), reset: &conversationReset{}}
	var transitions []sessions.SessionTransition
	pool.SetTransitionObserver(func(transition sessions.SessionTransition) { transitions = append(transitions, transition) })
	id, err := sw.Switch(context.Background(), "conv-1", protocol.AgentCodex, nil, nil)
	if id != "" || !errors.Is(err, ErrAgentSwitchMintFailed) {
		t.Fatalf("Switch = %q, %v; want path-free mint failure", id, err)
	}
	if got := agentSwitchOutcome(id, err); got.State != relay.AgentSwitchFailed {
		t.Fatalf("construction outcome = %+v", got)
	}
	if strings.Contains(err.Error(), workspace) || strings.Contains(err.Error(), home) {
		t.Fatalf("mint failure contains workspace path: %v", err)
	}
	if got, _ := reg.Get("conv-1"); got.CurrentSessionID != string(oldID) || len(got.SessionHistory) != 0 {
		t.Fatalf("mint failure changed binding: %+v", got)
	}
	if got := pool.List(); len(got) != 2 {
		t.Fatalf("mint failure changed registry: %+v", got)
	}
	if len(transitions) != 0 {
		t.Fatalf("mint failure published transitions: %+v", transitions)
	}
}

func TestConversationAgentSwitch_ConversationSaveFailureRollsBack(t *testing.T) {
	pool, reg, sw, _ := dormantSwitchFixture(t, "")
	runPoolReady(t, pool)
	defer assertSwitchSkippedSequence(t, sw.resetting)
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

// gatedRunner is a modelListRunner whose Run, once cancelled, still waits for
// release. A teardown therefore cannot finish until the test opens the gate.
type gatedRunner struct {
	modelListRunner
	release <-chan struct{}
}

func (r gatedRunner) Run(ctx context.Context) error {
	<-ctx.Done()
	<-r.release
	return ctx.Err()
}

func TestConversationAgentSwitch_PostCommitRemovalErrorReportsNewID(t *testing.T) {
	// Session.Evict selects on evictedCh and ctx.Done(). With a runner that
	// returns on cancel, teardown can close evictedCh first and Remove then
	// reports nil at random. The gate keeps evictedCh open while Switch runs.
	release := make(chan struct{})
	plan := newModelListPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return gatedRunner{modelListRunner: modelListRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, release: release}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var transitions []sessions.SessionTransition
	pool.SetTransitionObserver(func(t sessions.SessionTransition) { transitions = append(transitions, t) })
	poolCtx := runPoolReady(t, pool)
	// Registered after runPoolReady, so it runs first: pool shutdown waits on
	// these runners.
	t.Cleanup(func() { close(release) })
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
	if got := agentSwitchOutcome(newID, err); got.State != relay.AgentSwitchCommitted {
		t.Fatalf("cleanup outcome = %+v", got)
	}
	got, _ := reg.Get("conv-1")
	if got.CurrentSessionID != string(newID) || len(got.SessionHistory) != 1 || len(transitions) != 1 {
		t.Fatalf("committed state = %+v; transitions = %+v", got, transitions)
	}
	if f := transitions[0]; f.Cause != sessions.CauseAgentSwitch || f.ConversationID != "conv-1" || f.PreviousAgent != protocol.AgentClaude || f.NextAgent != protocol.AgentCodex || !f.AgentSwitch || f.PreviousID != oldID || f.NewID != newID || f.ResetHandoffOutcome != nil {
		t.Fatalf("switch fact = %+v", f)
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

func assertSwitchSkippedSequence(t *testing.T, emitter *resettingEmitterV2) {
	t.Helper()
	edges := emitter.bcast.(*resettingBcast).recorded()
	if len(edges) != 3 || edges[0].payload.Phase != protocol.ResetPhaseWrappingUp || edges[0].payload.Handoff != protocol.ResetHandoffPending || edges[1].payload.Phase != protocol.ResetPhaseRestarting || edges[1].payload.Handoff != protocol.ResetHandoffSkipped || !edges[0].payload.Active || !edges[1].payload.Active || edges[2].payload.Active || edges[2].payload.Phase != "" || edges[2].payload.Handoff != "" {
		t.Fatalf("skipped reset sequence = %+v", edges)
	}
}
