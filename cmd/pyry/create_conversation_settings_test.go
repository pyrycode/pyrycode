package main

import (
	"context"
	"errors"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestSessionMinter_Settings (#2665): create_conversation's model and effort are
// checked against the resolved agent's own entries by UpdateSettings' checks,
// and an accepted pair is the minted session's stored settings, each absent
// field keeping what a mint gives today. The bootstrap's operator defaults are
// opus / low; opus advertises low, medium and high, and Codex's two families
// have low and medium in common.
//
// A refused request names a spawn dir outside $HOME: the refusal it gets is the
// settings one, which proves the check ran before resolveSpawnDir could reject
// (or trust-mark) anything, and the pool gains no session.
func TestSessionMinter_Settings(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	cases := []struct {
		name       string
		agent      string
		families   bool // whether the store holds Codex's families
		model      *string
		effort     *string
		wantErr    error
		wantModel  string
		wantEffort string
	}{
		{name: "claude neither", agent: protocol.AgentClaude, wantModel: "opus", wantEffort: "low"},
		{name: "claude model only", agent: protocol.AgentClaude, model: str("sonnet"), wantModel: "sonnet", wantEffort: "low"},
		{name: "claude effort only", agent: protocol.AgentClaude, effort: str("medium"), wantModel: "opus", wantEffort: "medium"},
		{name: "claude effort not on default model", agent: protocol.AgentClaude, effort: str("max"), wantErr: relay.ErrEffortNotOffered},
		{name: "claude effort on requested model", agent: protocol.AgentClaude, model: str("sonnet"), effort: str("max"), wantModel: "sonnet", wantEffort: "max"},
		{name: "claude model not offered", agent: protocol.AgentClaude, model: str("gpt"), wantErr: relay.ErrModelNotOffered},
		{name: "claude explicit empty model", agent: protocol.AgentClaude, model: str(""), wantModel: "", wantEffort: "low"},
		{name: "codex neither", agent: protocol.AgentCodex, families: true},
		{name: "codex both", agent: protocol.AgentCodex, families: true, model: str("luna"), effort: str("ultra"), wantModel: "luna", wantEffort: "ultra"},
		{name: "codex effort common to families", agent: protocol.AgentCodex, families: true, effort: str("medium"), wantEffort: "medium"},
		{name: "codex effort not common", agent: protocol.AgentCodex, families: true, effort: str("ultra"), wantErr: relay.ErrEffortNotOffered},
		{name: "codex claude model", agent: protocol.AgentCodex, families: true, model: str("opus"), wantErr: relay.ErrModelNotOffered},
		{name: "codex no families", agent: protocol.AgentCodex, model: str("luna"), wantErr: relay.ErrModelVocabularyUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool, plan := newDormantWritePool(t, "")
			plan.arm(dormantWriteBootID, claudeEntries)
			opus, low := "opus", "low"
			if err := pool.UpdateSettings(dormantWriteBootID, sessions.SettingsUpdate{Model: &opus, Effort: &low}); err != nil {
				t.Fatalf("set operator defaults: %v", err)
			}
			runPoolReady(t, pool)
			store := &agentVocabularyDouble{}
			if tc.families {
				store.codex = codexEntries
			}
			minter := sessionMinter{p: pool, saved: store}

			spawnDir := ""
			if tc.wantErr != nil {
				spawnDir = "/"
			}
			id, _, err := minter.Create(context.Background(), "conv-1", spawnDir, tc.agent, handlers.CreateSettings{Model: tc.model, Effort: tc.effort})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Create err = %v, want %v", err, tc.wantErr)
				}
				if live := pool.List(); len(live) != 1 {
					t.Errorf("pool holds %d sessions after a refusal, want the bootstrap alone", len(live))
				}
				return
			}
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			got, err := pool.SettingsFor(sessions.SessionID(id))
			if err != nil {
				t.Fatalf("SettingsFor: %v", err)
			}
			if got.Model != tc.wantModel || got.Effort != tc.wantEffort || got.YOLO || got.PermissionMode != "" && got.PermissionMode != "default" {
				t.Errorf("minted settings = %+v, want model %q effort %q and no posture", got, tc.wantModel, tc.wantEffort)
			}
			if harness, err := pool.HarnessFor(sessions.SessionID(id)); err != nil || harness != tc.agent {
				t.Errorf("HarnessFor = %q, %v; want %q", harness, err, tc.agent)
			}
		})
	}
}
