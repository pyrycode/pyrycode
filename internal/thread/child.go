package thread

import (
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
	group          agentKey
	parent         string
	base, previous Item
	restored       bool
}

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
	id, summary, status, shown, valid := mainDecode(e)
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
	// Reports without parent evidence may belong to a later child creation.
	// Their original group and first terminal survive main-turn replacement.
	if saved == nil && f.childReports[ck] == nil && (e.Type == protocol.TypeToolResult || e.Type == protocol.TypeToolDenied) {
		field := "result"
		if e.Type == protocol.TypeToolDenied {
			field = "denial"
		}
		f.childReports[ck] = &mainOutcome{id: e.ID, status: status, field: field, raw: append(json.RawMessage(nil), e.Payload...)}
	}
	if parent == "" && id.ToolUseID != "" {
		if g := f.agentGroups[group]; g != nil {
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
		f.saveChild(index, group, parent)
	}
	if id.ToolUseID != "" && saved.call.item >= 0 {
		f.saveChild(saved.call.item, group, parent)
	}
	return true
}
func (f *Fold) saveChild(index int, group agentKey, parent string) {
	if f.childCounts != nil {
		f.childCounts.registered++
	}
	state := f.children[index]
	if state == nil {
		if f.childCounts != nil {
			f.childCounts.baseCopies++
		}
		// A first registration is already intrinsic, including mutations made
		// later in this entry before its first derived resolution.
		state = &childState{base: f.items[index], previous: f.items[index], restored: true}
		f.children[index] = state
	} else {
		f.restoreChild(index)
		delete(f.childEdges[childParentKey{state.group, state.parent}], index)
	}
	state.group, state.parent = group, parent
	key := childParentKey{group, parent}
	if f.childEdges[key] == nil {
		f.childEdges[key] = make(map[int]struct{})
	}
	f.childEdges[key][index] = struct{}{}
	f.childDirty[index] = struct{}{}
}

// Intrinsic state survives provisional ancestor endings. Reports received while
// inactive still update that state, so a later launch link can undo only closure.
func (f *Fold) restoreChild(index int) {
	state := f.children[index]
	if state == nil || state.restored {
		return
	}
	if f.childCounts != nil {
		f.childCounts.restored++
	}
	state.previous = f.items[index]
	f.items[index] = state.base
	f.items[index].Parent = state.previous.Parent
	f.items[index].Rev = state.previous.Rev
	state.restored = true
	f.childDirty[index] = struct{}{}
}

// Agent recomputation may run for an entire group; only actual changes enqueue
// that agent's own dependents. Intrinsic finals need no cached derived ending.
func (f *Fold) scheduleAgentChildren(index int) {
	for child := range f.childEdges[f.childAgents[index]] {
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
	for index := range f.childDirty {
		indices = append(indices, index)
	}
	for i := 0; i < len(indices); i++ {
		if f.childCounts != nil {
			f.childCounts.expanded++
		}
		key, agent := f.childAgents[indices[i]]
		if !agent {
			continue
		}
		for index := range f.childEdges[key] {
			if f.childCounts != nil {
				f.childCounts.edges++
			}
			if _, exists := f.childDirty[index]; !exists {
				f.childDirty[index] = struct{}{}
				indices = append(indices, index)
			}
		}
	}
	// Storage is creating-ID ordered. Older-only edges make this a topological walk.
	slices.Sort(indices)
	for _, index := range indices {
		if f.childCounts != nil {
			f.childCounts.resolved++
		}
		state := f.children[index]
		var ending json.RawMessage
		if state != nil {
			f.restoreChild(index)
			if f.childCounts != nil {
				f.childCounts.baseCopies++
			}
			state.base = f.items[index]
		}
		item := &f.items[index]
		if state != nil {
			parentID := uint64(0)
			if f.childCounts != nil {
				f.childCounts.edges++
			}
			if g := f.agentGroups[state.group]; g != nil {
				if c := g.calls[state.parent]; c != nil && c.item >= 0 && f.items[c.item].Kind == "agent" && f.items[c.item].ID < item.ID {
					parentID = f.items[c.item].ID
					ending = f.childEndings[c.item]
					if f.items[c.item].EndedOrder != 0 {
						ending = f.items[c.item].Content
					}
				}
			}
			item.Parent = parentID
			if ending != nil && item.Active {
				item.Active = false
				if item.Kind != "agent" {
					item.Status = "interrupted"
				}
				f.setContent(index, "parent_ending", ending)
			}
			previous, current := state.previous, *item
			previous.Rev, current.Rev = 0, 0
			if !reflect.DeepEqual(previous, current) {
				item.Rev = max(item.ID, rev)
			} else {
				item.Rev = state.previous.Rev
			}
			state.base.Parent, state.base.Rev = item.Parent, item.Rev
			state.previous = *item
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
	clear(f.childDirty)
}
