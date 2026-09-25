package main

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// agentVocabularyDouble is the persisted store as the adapter sees it, holding
// both agents' entries (#2629), and counting every read so a test can prove the
// vocabulary was never consulted.
type agentVocabularyDouble struct {
	list  turnevent.ModelList
	have  bool
	codex []turnevent.ModelOption
	reads atomic.Int32
}

func (d *agentVocabularyDouble) ModelList() (turnevent.ModelList, bool) {
	d.reads.Add(1)
	return d.list, d.have
}

func (d *agentVocabularyDouble) CodexModels() []turnevent.ModelOption {
	d.reads.Add(1)
	return d.codex
}

// claudeLevels is the five levels claude advertises for its richer models. The
// settings suites that predate #2629 arm their Claude entries with it, since an
// entry advertising no levels now accepts no effort.
var claudeLevels = []string{"low", "medium", "high", "xhigh", "max"}

// claudeEntries is the bootstrap's Claude list: sonnet advertises all five of
// claude's levels, opus three, haiku none.
var claudeEntries = turnevent.ModelList{Models: []turnevent.ModelOption{
	{Value: "sonnet", EffortLevels: claudeLevels},
	{Value: "opus", EffortLevels: []string{"low", "medium", "high"}},
	{Value: "haiku"},
}}

// codexEntries is Codex's families: luna advertises Codex's higher levels,
// sol two of the ordinary ones.
var codexEntries = []turnevent.ModelOption{
	{Value: "luna", ResolvedModel: "gpt-6-luna", EffortLevels: []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
	{Value: "sol", ResolvedModel: "gpt-6-sol", EffortLevels: []string{"low", "medium"}},
}

// newAgentVocabularyAdapter builds a real pool whose live bootstrap runs claude
// with claudeEntries armed on its hold, and whose one dormant entry carries
// dormantFields (a harness key among them, for a Codex session).
func newAgentVocabularyAdapter(t *testing.T, dormantFields string, store *agentVocabularyDouble) (settingsUpdaterAdapter, *sessions.Pool) {
	t.Helper()
	pool, plan := newDormantWritePool(t, dormantFields)
	plan.arm(dormantWriteBootID, claudeEntries)
	return settingsUpdaterAdapter{p: pool, saved: store}, pool
}

func TestValidateEffortVocabulary(t *testing.T) {
	t.Parallel()

	cut := turnevent.ModelList{Models: []turnevent.ModelOption{
		{Value: "opus", EffortLevels: []string{"low"}, TruncatedFields: []string{"effort_levels"}},
		{Value: "opu", EffortLevels: []string{"low"}, TruncatedFields: []string{"value"}},
	}}
	codex := turnevent.ModelList{Models: codexEntries}
	claude := sessions.HarnessClaude
	tests := []struct {
		name    string
		harness string
		list    turnevent.ModelList
		have    bool
		model   string
		effort  string
		want    error
	}{
		{name: "empty effort clears", harness: claude, list: claudeEntries, have: true, model: "haiku", effort: ""},
		{name: "advertised level", harness: claude, list: claudeEntries, have: true, model: "sonnet", effort: "max"},
		{name: "level the entry does not advertise", harness: claude, list: claudeEntries, have: true, model: "opus", effort: "max", want: relay.ErrEffortNotOffered},
		{name: "entry advertising no levels accepts none", harness: claude, list: claudeEntries, have: true, model: "haiku", effort: "low", want: relay.ErrEffortNotOffered},
		{name: "codex ultra advertised", harness: harnessCodex, list: codex, have: true, model: "luna", effort: "ultra"},
		{name: "codex ultra not advertised", harness: harnessCodex, list: codex, have: true, model: "sol", effort: "ultra", want: relay.ErrEffortNotOffered},
		{name: "empty model falls back", harness: claude, list: claudeEntries, have: true, model: "", effort: "max"},
		{name: "empty model falls back, unknown refused", harness: claude, list: claudeEntries, have: true, model: "", effort: "ultra", want: relay.ErrEffortNotOffered},
		{name: "no vocabulary falls back", harness: claude, model: "sonnet", effort: "medium"},
		{name: "no vocabulary refuses outside set", harness: claude, model: "sonnet", effort: "ultra", want: relay.ErrEffortNotOffered},
		{name: "cut levels fall back", harness: claude, list: cut, have: true, model: "opus", effort: "max"},
		{name: "cut value is not the entry", harness: claude, list: cut, have: true, model: "opu", effort: "high"},
		// #2666: a Codex model with no entry accepts only what every held
		// family advertises (luna and sol share low and medium), never
		// Claude's five, and nothing while no family is held.
		{name: "codex unlisted model, common level", harness: harnessCodex, list: codex, have: true, model: "gpt-5.6-sol", effort: "medium"},
		{name: "codex unlisted model, one family's level refused", harness: harnessCodex, list: codex, have: true, model: "gpt-5.6-sol", effort: "xhigh", want: relay.ErrEffortNotOffered},
		{name: "codex unlisted model, max refused", harness: harnessCodex, list: codex, have: true, model: "gpt-5.6-sol", effort: "max", want: relay.ErrEffortNotOffered},
		{name: "codex empty model, common level", harness: harnessCodex, list: codex, have: true, model: "", effort: "low"},
		{name: "codex empty model, max refused", harness: harnessCodex, list: codex, have: true, model: "", effort: "max", want: relay.ErrEffortNotOffered},
		{name: "codex no families refuses every level", harness: harnessCodex, model: "luna", effort: "low", want: relay.ErrEffortNotOffered},
		{name: "codex no families still clears", harness: harnessCodex, model: "luna", effort: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateEffortVocabulary(tt.harness, tt.list, tt.have, tt.model, tt.effort); !errors.Is(err, tt.want) {
				t.Errorf("validateEffortVocabulary(%q, %q, %q) = %v, want %v", tt.harness, tt.model, tt.effort, err, tt.want)
			}
		})
	}
}

// A model is checked against the entries of the session's own agent (#2629): a
// Codex family on a Codex session, a Claude alias on a Claude one, and each is
// refused as not offered on the other.
func TestSettingsUpdaterAdapter_ModelFollowsSessionAgent(t *testing.T) {
	t.Parallel()

	store := &agentVocabularyDouble{codex: codexEntries}
	adapter, pool := newAgentVocabularyAdapter(t, `"harness":"codex","model":"sol",`, store)

	for _, tc := range []struct {
		name  string
		id    string
		model string
		want  error
	}{
		{"codex family on codex session", dormantWriteTargetID, "luna", nil},
		{"claude alias on codex session", dormantWriteTargetID, "sonnet", relay.ErrModelNotOffered},
		{"codex family on claude session", dormantWriteBootID, "luna", relay.ErrModelNotOffered},
		{"claude alias on claude session", dormantWriteBootID, "sonnet", nil},
	} {
		model := tc.model
		if err := adapter.UpdateSettings(tc.id, relay.SettingsUpdate{Model: &model}); !errors.Is(err, tc.want) {
			t.Errorf("%s: UpdateSettings = %v, want %v", tc.name, err, tc.want)
		}
	}
	if got := dormantSettings(t, pool); got.Model != "luna" {
		t.Errorf("codex session model = %q, want %q — the refused alias must change nothing", got.Model, "luna")
	}
}

// A Codex session with no Codex entries held answers the unavailable reply, not
// not-offered: nothing can prove the family absent.
func TestSettingsUpdaterAdapter_CodexWithoutEntriesIsUnavailable(t *testing.T) {
	t.Parallel()

	adapter, _ := newAgentVocabularyAdapter(t, `"harness":"codex",`, &agentVocabularyDouble{list: claudeEntries, have: true})
	luna := "luna"
	if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &luna}); !errors.Is(err, relay.ErrModelVocabularyUnavailable) {
		t.Errorf("UpdateSettings = %v, want ErrModelVocabularyUnavailable", err)
	}
}

// An effort is accepted only when the model the session will run advertises it:
// the update's own model when it names one, else the stored one (#2629).
func TestSettingsUpdaterAdapter_EffortFollowsModel(t *testing.T) {
	t.Parallel()

	t.Run("codex", func(t *testing.T) {
		t.Parallel()
		adapter, pool := newAgentVocabularyAdapter(t, `"harness":"codex","model":"luna","effort":"low",`, &agentVocabularyDouble{codex: codexEntries})

		for _, effort := range []string{"xhigh", "max", "ultra"} {
			e := effort
			if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Effort: &e}); err != nil {
				t.Errorf("stored luna, effort %q: %v", effort, err)
			}
		}
		if got := dormantSettings(t, pool); got.Effort != "ultra" {
			t.Errorf("effort = %q, want ultra", got.Effort)
		}

		// The update's own model wins over the stored one, and a refusal changes
		// neither field.
		sol, ultra := "sol", "ultra"
		if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &sol, Effort: &ultra}); !errors.Is(err, relay.ErrEffortNotOffered) {
			t.Errorf("sol + ultra = %v, want ErrEffortNotOffered", err)
		}
		if got := dormantSettings(t, pool); got.Model != "luna" || got.Effort != "ultra" {
			t.Errorf("after refusal = %q/%q, want luna/ultra unchanged", got.Model, got.Effort)
		}

		// An empty model in the update has no entry, so effort falls back to
		// the levels every held family advertises (#2666).
		empty, maxLevel, medium := "", "max", "medium"
		for _, e := range []*string{&ultra, &maxLevel} {
			if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &empty, Effort: e}); !errors.Is(err, relay.ErrEffortNotOffered) {
				t.Errorf("empty model + %s = %v, want ErrEffortNotOffered", *e, err)
			}
		}
		if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &empty, Effort: &medium}); err != nil {
			t.Errorf("empty model + medium: %v", err)
		}

		// An empty effort still clears.
		clear := ""
		if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Effort: &clear}); err != nil {
			t.Errorf("clear effort: %v", err)
		}
		if got := dormantSettings(t, pool); got.Effort != "" {
			t.Errorf("effort after clear = %q, want empty", got.Effort)
		}
	})

	t.Run("codex stored version no longer listed", func(t *testing.T) {
		t.Parallel()
		adapter, _ := newAgentVocabularyAdapter(t, `"harness":"codex","model":"gpt-5.6-sol",`, &agentVocabularyDouble{codex: codexEntries})
		for _, effort := range []string{"ultra", "max", "xhigh"} {
			e := effort
			if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Effort: &e}); !errors.Is(err, relay.ErrEffortNotOffered) {
				t.Errorf("unlisted version + %s = %v, want ErrEffortNotOffered", effort, err)
			}
		}
		medium := "medium"
		if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Effort: &medium}); err != nil {
			t.Errorf("unlisted version + medium: %v", err)
		}
	})

	// #2666: no family held proves no level, so none is accepted; clearing
	// still is.
	t.Run("codex no vocabulary yet", func(t *testing.T) {
		t.Parallel()
		adapter, _ := newAgentVocabularyAdapter(t, `"harness":"codex","model":"luna","effort":"low",`, &agentVocabularyDouble{})
		for _, effort := range []string{"ultra", "max", "low"} {
			e := effort
			if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Effort: &e}); !errors.Is(err, relay.ErrEffortNotOffered) {
				t.Errorf("no vocabulary + %s = %v, want ErrEffortNotOffered", effort, err)
			}
		}
		clear := ""
		if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Effort: &clear}); err != nil {
			t.Errorf("no vocabulary, clear effort: %v", err)
		}
	})

	t.Run("claude", func(t *testing.T) {
		t.Parallel()
		adapter, pool := newAgentVocabularyAdapter(t, `"model":"opus",`, &agentVocabularyDouble{codex: codexEntries})

		// Dormant claude session on opus: max and ultra are refused, high accepted.
		maxLevel, ultra, high := "max", "ultra", "high"
		if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Effort: &maxLevel}); !errors.Is(err, relay.ErrEffortNotOffered) {
			t.Errorf("stored opus + max = %v, want ErrEffortNotOffered", err)
		}
		if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Effort: &high}); err != nil {
			t.Errorf("stored opus + high: %v", err)
		}
		sonnet := "sonnet"
		if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &sonnet, Effort: &maxLevel}); err != nil {
			t.Errorf("sonnet + max: %v", err)
		}
		if got := dormantSettings(t, pool); got.Model != "sonnet" || got.Effort != "max" {
			t.Errorf("dormant = %q/%q, want sonnet/max", got.Model, got.Effort)
		}

		// Live bootstrap on claude's default (no stored model): the fallback set.
		boot := dormantWriteBootID
		if err := adapter.UpdateSettings(boot, relay.SettingsUpdate{Effort: &maxLevel}); err != nil {
			t.Errorf("live default + max: %v", err)
		}
		if err := adapter.UpdateSettings(boot, relay.SettingsUpdate{Effort: &ultra}); !errors.Is(err, relay.ErrEffortNotOffered) {
			t.Errorf("live default + ultra = %v, want ErrEffortNotOffered", err)
		}
		haiku, low := "haiku", "low"
		if err := adapter.UpdateSettings(boot, relay.SettingsUpdate{Model: &haiku, Effort: &low}); !errors.Is(err, relay.ErrEffortNotOffered) {
			t.Errorf("haiku (no levels) + low = %v, want ErrEffortNotOffered", err)
		}
		if got, err := pool.SettingsFor(dormantWriteBootID); err != nil || got.Model != "" || got.Effort != "max" {
			t.Errorf("live settings = %+v, %v; want model empty, effort max", got, err)
		}
	})
}

// An unknown session id answers session-not-found before any vocabulary is read,
// for an effort-only frame as for a model.
func TestSettingsUpdaterAdapter_UnknownSessionReadsNoVocabulary(t *testing.T) {
	t.Parallel()

	store := &agentVocabularyDouble{list: claudeEntries, have: true, codex: codexEntries}
	adapter, _ := newAgentVocabularyAdapter(t, `"harness":"codex",`, store)
	unknown, err := sessions.NewID()
	if err != nil {
		t.Fatalf("sessions.NewID: %v", err)
	}
	ultra, luna := "ultra", "luna"
	for _, u := range []relay.SettingsUpdate{{Effort: &ultra}, {Model: &luna}, {Model: &luna, Effort: &ultra}} {
		for _, id := range []string{string(unknown), ""} {
			if err := adapter.UpdateSettings(id, u); !errors.Is(err, relay.ErrSessionUnknown) {
				t.Errorf("UpdateSettings(%q) = %v, want ErrSessionUnknown", id, err)
			}
		}
	}
	if n := store.reads.Load(); n != 0 {
		t.Errorf("vocabulary reads = %d, want 0 — an unknown id must not reach the vocabulary", n)
	}
}
