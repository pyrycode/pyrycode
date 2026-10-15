package thread

import (
	"bytes"
	"cmp"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type childKey struct {
	group        agentKey
	parent, turn string
}
type childCallKey struct {
	group      agentKey
	turn, call string
}
type childCall struct {
	parent string
	call   *mainCall
}
type childState struct {
	group                      agentKey
	parent                     string
	base, previous             Item
	baseRecipe, previousRecipe contentRecipe
	restored                   bool
	legacyEnding               *legacyChildEnding
}

type legacyChildEnding struct{ base, raw, content json.RawMessage }

type endingReference struct{ item, revision uint64 }

type childParentKey struct {
	group agentKey
	call  string
}

// childWorkCounts measures inspected candidates, including unchanged items.
// It is enabled only by package tests on their own serialized fold.
type childWorkCounts struct {
	registered, restored, baseCopies, expanded, edges, resolved int
}

// childGroup pins mapped evidence to the saved producer lifetime or legacy scope.
func (f *Fold) childGroup(e history.Entry) agentKey {
	key := agentKey{scope: f.legacyScope}
	if e.Session != nil {
		key.source, key.tagged = *e.Session, true
	}
	source := key
	source.scope = 0
	key.lifetime = f.agentLifetimes[source]
	if key.lifetime != "" {
		key.scope = 0
	}
	return key
}
func (f *Fold) childWork(e history.Entry) bool {
	id, summary, status, shown, valid := mainDecode(e, f.ingestCounts)
	if !valid {
		return false
	}
	switch e.Type {
	case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd:
	default:
		return false
	}
	group := f.childGroup(e)
	parent := id.ParentCall
	if parent == "" {
		parent = id.Parent
	}
	ck := childCallKey{group, id.TurnID, id.ToolUseID}
	saved := f.childCalls[ck]
	f.countPublicWork(1)
	// Reports without parent evidence may belong to a later child creation.
	// Their original group and first terminal survive main-turn replacement.
	if saved == nil && f.childReports[ck] == nil && (e.Type == protocol.TypeToolResult || e.Type == protocol.TypeToolDenied) {
		field := "result"
		if e.Type == protocol.TypeToolDenied {
			field = "denial"
		}
		f.childReports[ck] = f.outcome(e, status, field)
	}
	if parent == "" && id.ToolUseID != "" {
		f.countPublicWork(1)
		if g := f.agentGroups[group]; g != nil {
			f.countPublicWork(1)
			if c := g.calls[id.ToolUseID]; c != nil {
				parent = c.parent
			}
		}
	}
	if parent == "" && saved != nil {
		parent = saved.parent
	}
	if parent == "" {
		return false
	}
	key := childKey{group, parent, id.TurnID}
	st := f.childTurns[key]
	if st == nil {
		st = &mainTurn{key: mainKey{source: group.source, tagged: group.tagged, scope: group.scope, turn: id.TurnID}, text: -1, calls: make(map[string]*mainCall)}
		f.childTurns[key] = st
	}
	if id.ToolUseID != "" {
		if saved == nil {
			call := &mainCall{group: group, item: -1, terminal: st.end}
			// An early unparented report can enrich the later attributed creation.
			mainKey := st.key
			if mainKey.tagged {
				mainKey.scope = 0
			}
			if main := f.turns[mainKey]; main != nil && main.calls[id.ToolUseID] != nil && main.calls[id.ToolUseID].group == group {
				call = main.calls[id.ToolUseID]
				delete(main.calls, id.ToolUseID)
			}
			if call.item < 0 {
				// A main-turn ending cannot close an uncreated child call.
				call.terminal = st.end
				if report := f.childReports[ck]; report != nil && (call.terminal == nil || report.id < call.terminal.id) {
					call.terminal = report
				}
			}
			saved = &childCall{parent, call}
			f.childCalls[ck] = saved
			delete(f.childReports, ck)
		}
		if saved.parent != parent {
			if old := f.childTurns[childKey{group, saved.parent, id.TurnID}]; old != nil {
				delete(old.calls, id.ToolUseID)
			}
		}
		saved.parent = parent
		st.calls[id.ToolUseID] = saved.call
		if saved.call.item >= 0 {
			f.saveChild(saved.call.item, group, parent)
		}
	}
	before := len(f.items)
	f.foldWork(e, id, summary, status, shown, st, true)
	for index := before; index < len(f.items); index++ {
		f.countPublicWork(1)
		f.saveChild(index, group, parent)
	}
	if id.ToolUseID != "" && saved.call.item >= 0 {
		f.saveChild(saved.call.item, group, parent)
	}
	return true
}
func (f *Fold) saveChild(index int, group agentKey, parent string) {
	f.markAffected(index)
	f.countPublicWork(1) // child-state lookup, including first registration
	if f.childCounts != nil {
		f.childCounts.registered++
	}
	state := f.children[index]
	if state == nil {
		f.countPublicWork(3) // intrinsic item, state installation and dirty maintenance
		if f.childCounts != nil {
			f.childCounts.baseCopies++
		}
		// A first registration is already intrinsic, including mutations made
		// later in this entry before its first derived resolution.
		r := f.recipes[f.items[index].ID]
		state = &childState{base: f.items[index], previous: f.items[index], baseRecipe: r, previousRecipe: r, restored: true}
		f.children[index] = state
	} else {
		f.restoreChild(index)
		f.countPublicWork(2) // old parent edge lookup/removal
		delete(f.childEdges[childParentKey{state.group, state.parent}], index)
	}
	state.group, state.parent = group, parent
	key := childParentKey{group, parent}
	f.countPublicWork(4) // parent edge lookup/installation, membership and dirty set
	if f.childEdges[key] == nil {
		f.childEdges[key] = make(map[int]struct{})
	}
	f.childEdges[key][index] = struct{}{}
	f.childDirty[index] = struct{}{}
}

// Intrinsic state survives provisional ancestor endings. Reports received while
// inactive still update that state, so a later launch link can undo only closure.
func (f *Fold) restoreChild(index int) {
	f.countPublicWork(1) // include unchanged/non-child skips at mutation hooks
	state := f.children[index]
	if state == nil || state.restored {
		return
	}
	if f.childCounts != nil {
		f.childCounts.restored++
	}
	state.previous = f.items[index]
	state.previousRecipe = f.recipes[state.previous.ID]
	f.countPublicWork(3) // stored item, intrinsic restoration and dirty maintenance
	f.items[index] = state.base
	if f.source != nil {
		f.recipes[state.base.ID] = state.baseRecipe
	}
	f.items[index].Parent = state.previous.Parent
	f.items[index].Rev = state.previous.Rev
	state.restored = true
	f.childDirty[index] = struct{}{}
}

// Agent recomputation may run for an entire group; only actual changes enqueue
// that agent's own dependents. Intrinsic finals need no cached derived ending.
func (f *Fold) scheduleAgentChildren(index int) {
	f.countPublicWork(2) // agent identity and direct dependent set
	for child := range f.childEdges[f.childAgents[index]] {
		f.countPublicWork(2) // inspected dependent and dirty membership maintenance
		if f.childCounts != nil {
			f.childCounts.edges++
		}
		f.childDirty[child] = struct{}{}
	}
}

func (f *Fold) resolveChildren(rev uint64) {
	if len(f.childDirty) == 0 {
		return
	}
	indices := make([]int, 0, len(f.childDirty))
	f.countIngestion(len(f.childDirty) + 1)
	for index := range f.childDirty {
		f.countPublicWork(1)
		indices = append(indices, index)
	}
	for i := 0; i < len(indices); i++ {
		f.countPublicWork(2) // inspected candidate and agent identity lookup
		if f.childCounts != nil {
			f.childCounts.expanded++
		}
		key, agent := f.childAgents[indices[i]]
		if !agent {
			continue
		}
		f.countPublicWork(1) // dependent-set lookup, including empty sets
		for index := range f.childEdges[key] {
			f.countPublicWork(2) // inspected edge and deduplication lookup
			if f.childCounts != nil {
				f.childCounts.edges++
			}
			if _, exists := f.childDirty[index]; !exists {
				f.countPublicWork(1)
				f.childDirty[index] = struct{}{}
				if len(indices) == cap(indices) {
					f.countIngestion(3*len(indices) + 1)
				}
				indices = append(indices, index)
			}
		}
	}
	// Storage is creating-ID ordered. Older-only edges make this a topological walk.
	slices.SortFunc(indices, func(a, b int) int {
		f.countPublicWork(2)
		return a - b
	})
	for _, index := range indices {
		// Reachability may change even when this row's final fields do not.
		f.markAffected(index)
		if f.childCounts != nil {
			f.childCounts.resolved++
		}
		state := f.children[index]
		f.countPublicWork(2) // child state and stored item
		var ending endingReference
		if state != nil {
			f.restoreChild(index)
			if f.childCounts != nil {
				f.childCounts.baseCopies++
			}
			state.base = f.items[index]
			state.baseRecipe = f.recipes[state.base.ID]
		}
		item := &f.items[index]
		if state != nil {
			parentID := uint64(0)
			if f.childCounts != nil {
				f.childCounts.edges++
			}
			f.countPublicWork(1) // scoped parent group, including missing groups
			if g := f.agentGroups[state.group]; g != nil {
				f.countPublicWork(1) // scoped parent call
				if c := g.calls[state.parent]; c != nil && c.item >= 0 {
					f.countPublicWork(1) // parent item, including rejected kind/order
					parent := &f.items[c.item]
					if parent.Kind == "agent" && parent.ID < item.ID {
						parentID = parent.ID
						f.countPublicWork(1)
						ending = f.childEndings[c.item]
						if parent.EndedOrder != 0 {
							ending = endingReference{item: parent.ID, revision: parent.Rev}
						}
					}
				}
			}
			item.Parent = parentID
			if ending.item != 0 && item.Active {
				item.Active = false
				if item.Kind != "agent" {
					item.Status = "interrupted"
				}
				if f.source != nil {
					f.referenceField(index, "parent_ending", recipeField{item: ending.item, revision: ending.revision})
				} else {
					if state.legacyEnding == nil {
						state.legacyEnding = &legacyChildEnding{}
					}
					cache := state.legacyEnding
					ancestor, _ := slices.BinarySearchFunc(f.items, ending.item, func(item Item, id uint64) int {
						return cmp.Compare(item.ID, id)
					})
					raw := f.items[ancestor].Content
					f.countIngestion(2)
					// Identical intrinsic storage and ending need no repeated serialization,
					// including empty deltas before the first nonempty append.
					sameBase := len(item.Content) == len(cache.base) && len(item.Content) != 0 && &item.Content[0] == &cache.base[0]
					f.countIngestion(min(len(raw), len(cache.raw)) + 1)
					if f.texts[index] == nil && sameBase && bytes.Equal(raw, cache.raw) {
						item.Content = cache.content
					} else {
						base := item.Content
						f.setContent(index, "parent_ending", raw)
						cache.base, cache.raw, cache.content = base, raw, item.Content
					}
				}
			}
			previous, current := state.previous, *item
			previous.Rev, current.Rev = 0, 0
			f.countIngestion(20)
			if f.appendedText != index && len(previous.Content) == len(current.Content) && len(current.Content) != 0 && &previous.Content[0] != &current.Content[0] {
				f.countIngestion(len(current.Content))
			}
			if f.appendedText == index || !reflect.DeepEqual(previous, current) || !equalRecipe(state.previousRecipe, f.recipes[item.ID]) {
				item.Rev = max(item.ID, rev)
			} else {
				item.Rev = state.previous.Rev
			}
			state.base.Parent, state.base.Rev = item.Parent, item.Rev
			state.previous = *item
			state.previousRecipe = f.recipes[item.ID]
			state.restored = false
		}
		if item.Kind == "agent" {
			if item.EndedOrder == 0 && state != nil && !item.Active {
				f.childEndings[index] = ending
			} else {
				delete(f.childEndings, index)
			}
		}
	}
	f.countPublicWork(len(f.childDirty))
	// The next discovery must not iterate capacity retained from a former peak.
	f.childDirty = make(map[int]struct{})
	f.countIngestion(1)
}
