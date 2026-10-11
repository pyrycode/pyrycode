package main

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type memoryReplyOrdinaryCall struct {
	group             memoryReplyChildKey
	created, launcher bool
	shell             *memoryReplyCallOwner
}

// Ordinary creation belongs to a turn generation, independently of call-scoped
// agent observations. A reused call cannot become a shell through a later name.
// The caller has already validated the main DTO and identity, not an agent DTO.
func (r *memoryReplyEvidence) ordinaryWork(e history.Entry, id memoryReplyIdentity, st *memoryReplyLifetime, child bool) {
	if id.Call == "" {
		return
	}
	group := r.childKey(e, "")
	var calls map[string]*memoryReplyOrdinaryCall
	if child {
		key := group
		key.turn = id.Turn
		if r.childCalls == nil {
			r.childCalls = make(map[memoryReplyChildKey]map[string]*memoryReplyOrdinaryCall)
		}
		if r.childCalls[key] == nil {
			r.childCalls[key] = make(map[string]*memoryReplyOrdinaryCall)
		}
		calls = r.childCalls[key]
		if calls[id.Call] == nil {
			mainKey := memoryReplyKey{source: group.source, tagged: group.tagged, scope: group.scope, turn: id.Turn}
			if mainKey.tagged {
				mainKey.scope = 0
			}
			if main := r.turns[mainKey]; main != nil {
				if call := main.calls[id.Call]; call != nil && call.group == group {
					calls[id.Call] = call
					delete(main.calls, id.Call)
				}
			}
		}
	} else {
		if st.calls == nil {
			st.calls = make(map[string]*memoryReplyOrdinaryCall)
		}
		calls = st.calls
	}
	if e.Type != "tool_use" && e.Type != "tool_result" && e.Type != "tool_denied" {
		return
	}
	call := calls[id.Call]
	if call == nil || (call.shell != nil && call.group != group && id.Opening == nil) {
		call = &memoryReplyOrdinaryCall{group: group}
		calls[id.Call] = call
	}
	if e.Type != "tool_use" || call.created {
		return
	}
	call.created = true
	var p protocol.ToolUsePayload
	_ = json.Unmarshal(e.Payload, &p)
	call.launcher = (p.Name == "Agent" || p.Name == "Task") && (e.Session == nil || e.Session.Kind == "claude")
	if (p.Name != "Bash" && p.Name != "local_bash") || (e.Session != nil && e.Session.Kind != "claude") {
		return
	}
	owner := r.callOwner(group, id.Call)
	if owner.created {
		return
	}
	owner.child, owner.created, owner.shell = id.Parent != "" || id.ParentCall != "", true, true
	call.shell = owner
	r.observe(e.ID, group, id.Call, "")
}

// Promoted shells retain the ordinary call so another launch in the same turn
// remains a reuse. Current uncreated and launcher slots follow the first saved
// lifetime; created non-shell calls retain their original creation scope.
func (r *memoryReplyEvidence) promoteOrdinaryCalls(old, key memoryReplyChildKey, moved map[string]bool) {
	if r.owners[old] == nil {
		return
	}
	update := func(calls map[string]*memoryReplyOrdinaryCall) {
		for id, call := range calls {
			if call.shell != nil && call.group == old && moved[id] {
				call.group = key
			}
		}
	}
	for _, st := range r.openings {
		update(st.calls)
	}
	for _, st := range r.turns {
		for _, call := range st.calls {
			if call.group == old && (!call.created || call.launcher) {
				call.group = key
			}
		}
	}
	for _, calls := range r.childCalls {
		update(calls)
	}
}
