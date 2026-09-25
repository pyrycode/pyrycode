package main

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// fallbackFive is the effort set a model with no usable entry accepts.
var fallbackFive = []string{"low", "medium", "high", "xhigh", "max"}

// #2646: each agent's capability list, read through the same adapter whose
// UpdateSettings enforces it.
func TestSettingsUpdaterAdapter_Capabilities(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		dormant string
		store   *agentVocabularyDouble
		model   string
		want    relay.AgentCapabilities
	}{
		{
			name:    "claude on opus",
			dormant: `"model":"opus",`,
			store:   &agentVocabularyDouble{codex: codexEntries},
			model:   "opus",
			want:    relay.AgentCapabilities{Interrupt: true, EffortLevels: []string{"low", "medium", "high"}, Models: []string{"sonnet", "opus", "haiku"}},
		},
		{
			name:    "claude on its default model",
			dormant: ``,
			store:   &agentVocabularyDouble{codex: codexEntries},
			model:   "",
			want:    relay.AgentCapabilities{Interrupt: true, EffortLevels: fallbackFive, Models: []string{"sonnet", "opus", "haiku"}},
		},
		{
			name:    "codex on luna",
			dormant: `"harness":"codex","model":"luna",`,
			store:   &agentVocabularyDouble{list: claudeEntries, have: true, codex: codexEntries},
			model:   "luna",
			want:    relay.AgentCapabilities{Interrupt: true, EffortLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, Models: []string{"luna", "sol"}},
		},
		{
			name:    "codex on an unlisted version",
			dormant: `"harness":"codex","model":"gpt-5.6-sol",`,
			store:   &agentVocabularyDouble{codex: codexEntries},
			model:   "gpt-5.6-sol",
			want:    relay.AgentCapabilities{Interrupt: true, EffortLevels: []string{"low", "medium"}, Models: []string{"luna", "sol"}},
		},
		{
			name:    "codex on its default model",
			dormant: `"harness":"codex",`,
			store:   &agentVocabularyDouble{codex: codexEntries},
			model:   "",
			want:    relay.AgentCapabilities{Interrupt: true, EffortLevels: []string{"low", "medium"}, Models: []string{"luna", "sol"}},
		},
		{
			name:    "codex with no vocabulary yet",
			dormant: `"harness":"codex","model":"luna",`,
			store:   &agentVocabularyDouble{list: claudeEntries, have: true},
			model:   "luna",
			want:    relay.AgentCapabilities{Interrupt: true, Models: []string{}},
		},
		{
			name:    "codex skips a cut value",
			dormant: `"harness":"codex","model":"luna",`,
			store: &agentVocabularyDouble{codex: []turnevent.ModelOption{
				codexEntries[0],
				{Value: "so", EffortLevels: []string{"low"}, TruncatedFields: []string{"value"}},
			}},
			model: "luna",
			want:  relay.AgentCapabilities{Interrupt: true, EffortLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, Models: []string{"luna"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			adapter, _ := newAgentVocabularyAdapter(t, tc.dormant, tc.store)
			got, ok := adapter.Capabilities(dormantWriteTargetID, tc.model)
			if !ok {
				t.Fatal("Capabilities refused a known session")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Capabilities = %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("unknown session", func(t *testing.T) {
		t.Parallel()
		adapter, _ := newAgentVocabularyAdapter(t, ``, &agentVocabularyDouble{codex: codexEntries})
		for _, id := range []string{"", "11111111-2222-3333-4444-555555555555"} {
			if got, ok := adapter.Capabilities(id, "opus"); ok {
				t.Errorf("Capabilities(%q) = %+v, true; want false", id, got)
			}
		}
	})
}

// #2646: for each agent, every option its list names is accepted by the same
// adapter's UpdateSettings — each model, and each effort the list names for that
// model — while an option outside the list gets today's refusal.
func TestSettingsUpdaterAdapter_CapabilitiesAreAccepted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		dormant        string
		unlistedModel  string
		unlistedEffort string // for the session's stored model
		storedModel    string
	}{
		{"claude", `"model":"opus",`, "luna", "max", "opus"},
		{"codex", `"harness":"codex","model":"sol",`, "sonnet", "ultra", "sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			adapter, _ := newAgentVocabularyAdapter(t, tc.dormant, &agentVocabularyDouble{codex: codexEntries})

			caps, ok := adapter.Capabilities(dormantWriteTargetID, tc.storedModel)
			if !ok || len(caps.Models) == 0 {
				t.Fatalf("Capabilities = %+v, %v; want a model list", caps, ok)
			}
			for _, model := range append([]string{""}, caps.Models...) {
				if model != "" {
					m := model
					if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &m}); err != nil {
						t.Errorf("listed model %q refused: %v", model, err)
					}
				}
				perModel, ok := adapter.Capabilities(dormantWriteTargetID, model)
				if !ok {
					t.Fatalf("Capabilities(%q) refused", model)
				}
				for _, effort := range perModel.EffortLevels {
					m, e := model, effort
					if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &m, Effort: &e}); err != nil {
						t.Errorf("listed model %q + listed effort %q refused: %v", model, effort, err)
					}
				}
			}

			m := tc.unlistedModel
			if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &m}); !errors.Is(err, relay.ErrModelNotOffered) {
				t.Errorf("unlisted model %q = %v, want ErrModelNotOffered", m, err)
			}
			stored, e := tc.storedModel, tc.unlistedEffort
			if listed, _ := adapter.Capabilities(dormantWriteTargetID, stored); slices.Contains(listed.EffortLevels, e) {
				t.Fatalf("fixture: effort %q is listed for %q", e, stored)
			}
			if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &stored, Effort: &e}); !errors.Is(err, relay.ErrEffortNotOffered) {
				t.Errorf("unlisted effort %q on %q = %v, want ErrEffortNotOffered", e, stored, err)
			}
		})
	}
}
