package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

const switchConvID = "12345678-1234-4234-8234-123456789abc"

func relaySwitchFixture(t *testing.T) (*sessions.Pool, *conversations.Registry, conversationAgentSwitcher) {
	t.Helper()
	pool, reg, sw, _ := dormantSwitchFixture(t, `"model":"opus","effort":"high",`)
	row, _ := reg.Get("conv-1")
	reg.Delete("conv-1")
	row.ID = switchConvID
	reg.Create(row)
	sw.saved = &agentVocabularyDouble{list: claudeEntries, have: true, codex: codexEntries}
	if err := reg.Save(sw.registryPath); err != nil {
		t.Fatal(err)
	}
	return pool, reg, sw
}

func TestRelayAgentSwitchAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*conversations.Registry, *conversationAgentSwitcher, *protocol.SwitchAgentPayload)
		failure relay.AgentSwitchFailure
	}{
		{"invalid ID", func(_ *conversations.Registry, _ *conversationAgentSwitcher, p *protocol.SwitchAgentPayload) {
			p.ConversationID = "ZZPRIVATEIDZZ"
		}, relay.AgentSwitchInvalidRequest},
		{"missing", func(r *conversations.Registry, _ *conversationAgentSwitcher, _ *protocol.SwitchAgentPayload) {
			r.Delete(switchConvID)
		}, relay.AgentSwitchConversationNotFound},
		{"unbound", func(r *conversations.Registry, _ *conversationAgentSwitcher, _ *protocol.SwitchAgentPayload) {
			r.Update(switchConvID, func(c *conversations.Conversation) { c.CurrentSessionID = "" })
		}, relay.AgentSwitchConversationNotFound},
		{"missing binding", func(r *conversations.Registry, _ *conversationAgentSwitcher, _ *protocol.SwitchAgentPayload) {
			r.Update(switchConvID, func(c *conversations.Conversation) { c.CurrentSessionID = "missing" })
		}, relay.AgentSwitchConversationNotFound},
		{"same", func(_ *conversations.Registry, _ *conversationAgentSwitcher, p *protocol.SwitchAgentPayload) {
			p.Agent = "claude"
		}, relay.AgentSwitchInvalidRequest},
		{"model", func(_ *conversations.Registry, _ *conversationAgentSwitcher, p *protocol.SwitchAgentPayload) {
			p.Model = "ZZPRIVATEMODELZZ"
		}, relay.AgentSwitchModelNotOffered},
		{"effort", func(_ *conversations.Registry, _ *conversationAgentSwitcher, p *protocol.SwitchAgentPayload) {
			v := "ZZPRIVATEEFFORTZZ"
			p.Effort = &v
		}, relay.AgentSwitchEffortNotOffered},
		{"missing vocabulary", func(_ *conversations.Registry, s *conversationAgentSwitcher, p *protocol.SwitchAgentPayload) {
			s.saved = nil
			p.Model = "luna"
		}, relay.AgentSwitchVocabularyUnavailable},
		{"missing effort vocabulary", func(_ *conversations.Registry, s *conversationAgentSwitcher, p *protocol.SwitchAgentPayload) {
			s.saved = nil
			v := "high"
			p.Effort = &v
		}, relay.AgentSwitchVocabularyUnavailable},
		{"workspace", func(r *conversations.Registry, _ *conversationAgentSwitcher, _ *protocol.SwitchAgentPayload) {
			r.Update(switchConvID, func(c *conversations.Conversation) { c.Cwd = "/ZZPRIVATEPATHZZ" })
		}, relay.AgentSwitchWorkspaceRejected},
		{"ordinary reset exclusion", func(_ *conversations.Registry, s *conversationAgentSwitcher, _ *protocol.SwitchAgentPayload) {
			release, ok := s.reset.begin(switchConvID)
			if !ok {
				t.Fatal("fixture busy")
			}
			t.Cleanup(release)
		}, relay.AgentSwitchBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, reg, sw := relaySwitchFixture(t)
			p := protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "codex"}
			tc.mutate(reg, &sw, &p)
			got := (relayAgentSwitcher{switcher: sw}).SwitchAgent(context.Background(), p)
			if got != (relay.AgentSwitchOutcome{State: relay.AgentSwitchRefused, Failure: tc.failure}) {
				t.Fatalf("outcome = %+v", got)
			}
			if len(sw.resetting.bcast.(*resettingBcast).recorded()) != 0 || len(pool.List()) != 1 {
				t.Fatal("refusal started reset or mutated pool")
			}
		})
	}
}

func TestRelayAgentSwitchFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   sessions.SessionID
		err  error
		want relay.AgentSwitchState
	}{
		{"construction", "", fmt.Errorf("ZZPRIVATEPATHZZ: %w", ErrAgentSwitchMintFailed), relay.AgentSwitchFailed},
		{"persistence cleanup", "", errors.Join(ErrAgentSwitchUnavailable, conversations.ErrConversationNotFound), relay.AgentSwitchFailed},
		{"cleanup committed", "new", fmt.Errorf("ZZPRIVATEHANDOVERZZ: %w", ErrAgentSwitchCleanupFailed), relay.AgentSwitchCommitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agentSwitchOutcome(tc.id, tc.err)
			if got.State != tc.want || got.Failure != relay.AgentSwitchOtherFailure {
				t.Fatalf("outcome=%+v", got)
			}
		})
	}
}

func TestRelayAgentSwitchTemplateAndPublicationOrder(t *testing.T) {
	pool, reg, sw := relaySwitchFixture(t)
	runPoolReady(t, pool)
	// Claude's operator template must survive an empty wire model on the return switch.
	if err := pool.UpdateSettings(pool.BootstrapID(), sessions.SettingsUpdate{Model: switchString("opus")}); err != nil {
		t.Fatal(err)
	}
	var transitions []sessions.SessionTransition
	pool.SetTransitionObserver(func(tr sessions.SessionTransition) {
		edges := sw.resetting.bcast.(*resettingBcast).recorded()
		if len(edges) == 0 || edges[len(edges)-1].payload.Active {
			t.Error("transition preceded inactive reset status")
		}
		transitions = append(transitions, tr)
	})
	for _, p := range []protocol.SwitchAgentPayload{
		{ConversationID: switchConvID, Agent: "codex", Model: "luna"},
		{ConversationID: switchConvID, Agent: "claude", Model: "", Effort: switchString("")},
	} {
		got := (relayAgentSwitcher{switcher: sw}).SwitchAgent(context.Background(), p)
		if got.State != relay.AgentSwitchCommitted {
			t.Fatalf("outcome=%+v", got)
		}
		row, _ := reg.Get(switchConvID)
		settings, err := pool.SettingsFor(sessions.SessionID(row.CurrentSessionID))
		if err != nil {
			t.Fatal(err)
		}
		wantModel, wantEffort := "luna", "high"
		if p.Agent == "claude" {
			wantModel, wantEffort = "opus", ""
		}
		if settings.Model != wantModel || settings.Effort != wantEffort {
			t.Fatalf("settings=%+v", settings)
		}
		saved, err := conversations.Load(sw.registryPath)
		if err != nil {
			t.Fatal(err)
		}
		stored, _ := saved.Get(switchConvID)
		if stored.CurrentSessionID != row.CurrentSessionID {
			t.Fatal("binding not persisted")
		}
	}
	if len(transitions) != 2 {
		t.Fatalf("transitions=%+v", transitions)
	}
}

func TestRelayAgentSwitchPersistenceFailure(t *testing.T) {
	pool, reg, sw := relaySwitchFixture(t)
	runPoolReady(t, pool)
	sw.registryPath = filepath.Join(t.TempDir(), "ZZPRIVATEPATHZZ", "conversations.json")
	if err := os.WriteFile(filepath.Dir(sw.registryPath), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	got := (relayAgentSwitcher{switcher: sw}).SwitchAgent(context.Background(), protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "codex"})
	if got.State != relay.AgentSwitchFailed {
		t.Fatalf("outcome=%+v", got)
	}
	row, _ := reg.Get(switchConvID)
	if row.CurrentSessionID != dormantWriteTargetID {
		t.Fatal("lost old binding")
	}
	assertSwitchSkippedSequence(t, sw.resetting)
}

func switchString(s string) *string { return &s }
