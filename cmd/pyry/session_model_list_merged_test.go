package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// twoAgentVocabularyDouble is the vocabulary store holding either agent's entries,
// or both, or neither: Claude's through ModelList, Codex's through CodexModels. The
// pool the tests build is cold, so the store is the only source that answers and
// each row arms each agent independently.
type twoAgentVocabularyDouble struct {
	claude     turnevent.ModelList
	haveClaude bool
	codex      []turnevent.ModelOption
}

func (d twoAgentVocabularyDouble) ModelList() (turnevent.ModelList, bool) {
	return d.claude, d.haveClaude
}

func (d twoAgentVocabularyDouble) CodexModels() []turnevent.ModelOption { return d.codex }

// sentinelCodexModels is the store's Codex half as #2627 holds it: the family as
// Value, the newest version as ResolvedModel, its effort levels, no display name.
func sentinelCodexModels() []turnevent.ModelOption {
	return []turnevent.ModelOption{
		{Value: "ZZCODEXFAMONEZZ", ResolvedModel: "ZZCODEXRESONEZZ", EffortLevels: []string{"low", "high"}},
		{Value: "ZZCODEXFAMTWOZZ", ResolvedModel: "ZZCODEXRESTWOZZ"},
	}
}

// assertTaggedRows checks got against want's rows, each tagged with agent, its
// family as the row's own value, and — for Codex — the family as its display name.
func assertTaggedRows(t *testing.T, got []protocol.ModelOption, want []turnevent.ModelOption, agent string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s rows: got %d, want %d: %+v", agent, len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		wantDisplay := w.DisplayName
		if agent == protocol.AgentCodex {
			wantDisplay = w.Value
		}
		if g.Value != w.Value || g.ResolvedModel != w.ResolvedModel || g.DisplayName != wantDisplay {
			t.Errorf("%s row %d = %+v, want value %q resolved %q display %q", agent, i, g, w.Value, w.ResolvedModel, wantDisplay)
		}
		if !reflect.DeepEqual(g.EffortLevels, w.EffortLevels) || !reflect.DeepEqual(g.TruncatedFields, w.TruncatedFields) || g.SupportsAutoMode != w.SupportsAutoMode {
			t.Errorf("%s row %d = %+v, want the held row %+v carried through", agent, i, g, w)
		}
		if g.Agent != agent || g.Family != w.Value {
			t.Errorf("%s row %d: agent %q family %q, want %q and %q", agent, i, g.Agent, g.Family, agent, w.Value)
		}
	}
}

// #2651: both per-conn paths — the request_model_list seam and the connect-time
// reconcile — answer a multi_agent client with Claude's entries then Codex's, each
// tagged, and every other client with today's Claude-only list, over every
// combination of which agents hold entries.
func TestModelListSeams_MergeForMultiAgentClients(t *testing.T) {
	claude := sentinelModelList("MERGE")

	tests := []struct {
		name       string
		haveClaude bool
		haveCodex  bool
		multiAgent bool
		wantClaude bool
		wantCodex  bool
		wantNone   bool
	}{
		{name: "capable, both held", haveClaude: true, haveCodex: true, multiAgent: true, wantClaude: true, wantCodex: true},
		{name: "capable, no Codex held", haveClaude: true, multiAgent: true, wantClaude: true},
		{name: "capable, Codex only", haveCodex: true, multiAgent: true, wantCodex: true},
		{name: "capable, neither held", multiAgent: true, wantNone: true},
		{name: "old client, both held", haveClaude: true, haveCodex: true},
		{name: "old client, no Codex held", haveClaude: true},
		{name: "old client, Codex only", haveCodex: true, wantNone: true},
		{name: "old client, neither held", wantNone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, _ := newModelListTestPool(t)
			saved := twoAgentVocabularyDouble{haveClaude: tt.haveClaude}
			if tt.haveClaude {
				saved.claude = claude
			}
			if tt.haveCodex {
				saved.codex = sentinelCodexModels()
			}
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: "conv-merge", LastUsedAt: time.Now().UTC()})

			fromRequest, reqOK := modelListFor(reg, pool, saved)("conv-merge", tt.multiAgent)
			lists := retainedModelLists(reg, pool, saved)(tt.multiAgent)

			if tt.wantNone {
				if reqOK {
					t.Errorf("request answered %+v; want today's unavailable refusal", fromRequest)
				}
				if len(lists) != 0 {
					t.Errorf("reconcile sent %d model_list; want none", len(lists))
				}
				return
			}
			if !reqOK {
				t.Fatal("request refused; want a model_list")
			}
			if len(lists) != 1 {
				t.Fatalf("reconcile sent %d model_list, want 1", len(lists))
			}

			for path, got := range map[string]protocol.ModelListPayload{"request": fromRequest, "reconcile": lists[0]} {
				t.Run(path, func(t *testing.T) {
					if got.ConversationID != "conv-merge" {
						t.Errorf("ConversationID = %q, want conv-merge", got.ConversationID)
					}
					if len(got.Models) == 0 {
						t.Fatal("a model_list with an empty models array")
					}

					if !tt.multiAgent {
						// Byte-identical to today's answer: the resolver it always used,
						// and no tag key anywhere in the frame.
						want, _ := resolveBoundModelList(reg, pool, saved, "conv-merge")
						if !reflect.DeepEqual(got, want) {
							t.Errorf("old client's list = %+v, want today's %+v", got, want)
						}
						body, err := json.Marshal(got)
						if err != nil {
							t.Fatalf("marshal: %v", err)
						}
						if bytes.Contains(body, []byte(`"agent"`)) || bytes.Contains(body, []byte(`"family"`)) {
							t.Errorf("old client's frame carries a tag key: %s", body)
						}
						return
					}

					nClaude := 0
					if tt.wantClaude {
						nClaude = len(claude.Models)
						assertTaggedRows(t, got.Models[:nClaude], claude.Models, protocol.AgentClaude)
					}
					if tt.wantCodex {
						assertTaggedRows(t, got.Models[nClaude:], sentinelCodexModels(), protocol.AgentCodex)
					} else if len(got.Models) != nClaude {
						t.Errorf("got %d rows, want Claude's %d alone", len(got.Models), nClaude)
					}

					wantDropped := 0
					if tt.wantClaude {
						wantDropped = claude.DroppedModels
					}
					if got.DroppedModels != wantDropped {
						t.Errorf("DroppedModels = %d, want Claude's cut count %d", got.DroppedModels, wantDropped)
					}
				})
			}
		})
	}
}

// #2651: the merged list reads Claude's entries through the same three sources an
// older client's list does, so a bound session's own report still wins over the
// store — the merge adds Codex's half and changes nothing about which Claude list
// answers.
func TestMergedModelOptions_ClaudeHalfFollowsTheBoundSession(t *testing.T) {
	pool, plan := newModelListTestPool(t)
	own := sentinelModelList("BOUNDOWN")
	sess := pool.BootstrapID()
	plan.arm(sess, own)

	saved := twoAgentVocabularyDouble{claude: sentinelModelList("STORE"), haveClaude: true, codex: sentinelCodexModels()}
	models, dropped, ok := mergedModelOptions(pool, saved, string(sess))
	if !ok {
		t.Fatal("mergedModelOptions refused with both agents held")
	}
	assertTaggedRows(t, models[:len(own.Models)], own.Models, protocol.AgentClaude)
	assertTaggedRows(t, models[len(own.Models):], sentinelCodexModels(), protocol.AgentCodex)
	if dropped != own.DroppedModels {
		t.Errorf("dropped = %d, want the bound list's %d", dropped, own.DroppedModels)
	}
}
