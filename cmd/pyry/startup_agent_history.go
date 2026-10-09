package main

import (
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type startupAgentKey struct {
	source   history.SessionProvenance
	lifetime string
	scope    int
}

// startupAgentReference addresses one identity in its original observation.
// The observation fixes legacy scope; a roster can observe several task IDs.
type startupAgentReference struct {
	source     history.SessionProvenance
	lifetime   string
	observedID uint64
	identity   string
}

// startupAgentEvidence retains only identities and outcomes from raw pages.
type startupAgentEvidence struct {
	agentHistoryFact
	ToolUseID string   `json:"tool_use_id"`
	Name      string   `json:"name"`
	Parent    string   `json:"parent_tool_use_id"`
	Truncated []string `json:"truncated_fields"`
	Dropped   []string `json:"dropped_fields"`
	Tasks     []struct {
		TaskID     string   `json:"task_id"`
		ToolCallID string   `json:"tool_call_id"`
		Truncated  []string `json:"truncated_fields"`
	} `json:"tasks"`
	id     uint64
	typ    string
	source history.SessionProvenance
}

func readStartupAgentWork(store *history.Store, convID conversations.ConversationID) ([]*convTurnState, error) {
	var evidence []startupAgentEvidence
	cursor := ""
	for {
		page, err := store.Page(convID, cursor, 128)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Entries {
			switch entry.Type {
			case protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeBackgroundTaskStarted, protocol.TypeBackgroundTaskUpdated, protocol.TypeBackgroundTaskProgress, protocol.TypeBackgroundTaskRoster, protocol.TypeSessionTransition, historySessionDivider:
			default:
				if !agentHistoryType(entry.Type) {
					continue
				}
			}
			var p startupAgentEvidence
			if json.Unmarshal(entry.Payload, &p) != nil || (p.ConversationID != "" && p.ConversationID != string(convID)) {
				continue
			}
			if agentHistoryType(entry.Type) && !validAgentHistoryFact(string(convID), entry.Type, entry.Payload) {
				continue
			}
			p.id, p.typ = entry.ID, entry.Type
			if entry.Session != nil {
				p.source = *entry.Session
			}
			if p.typ != protocol.TypeSessionTransition && p.typ != historySessionDivider && p.source.Kind != "" && p.source.Kind != "claude" {
				continue
			}
			evidence = append(evidence, p)
		}
		if page.AtStart {
			break
		}
		cursor = page.Cursor
	}
	groups := map[startupAgentKey]*convTurnState{}
	active := map[history.SessionProvenance]string{}
	closedCalls := map[startupAgentReference]bool{}
	closedTasks := map[startupAgentReference]bool{}
	scope := 0
	for i := len(evidence) - 1; i >= 0; i-- {
		p := evidence[i]
		if p.typ == protocol.TypeSessionTransition || p.typ == historySessionDivider {
			if p.Cause != "daemon_restart" {
				scope++
				clear(active)
			}
			continue
		}
		if p.typ == historyAgentSessionEnded {
			if p.CallObservedEntryID != 0 {
				closedCalls[startupAgentReference{p.source, p.LifetimeID, p.CallObservedEntryID, p.ToolCallID}] = true
			}
			if p.TaskObservedEntryID != 0 {
				closedTasks[startupAgentReference{p.source, p.LifetimeID, p.TaskObservedEntryID, p.TaskID}] = true
			}
			// Explicit references address original legacy observations, not this scope.
			if p.LifetimeID == "" {
				continue
			}
		} else if p.LifetimeID != "" {
			active[p.source] = p.LifetimeID
		}
		lifetime := p.LifetimeID
		if lifetime == "" {
			lifetime = active[p.source]
		}
		// Mapped reports duplicate durable facts in modern producer lifetimes.
		if lifetime != "" && !agentHistoryType(p.typ) {
			continue
		}
		key := startupAgentKey{source: p.source, lifetime: lifetime, scope: scope}
		if lifetime != "" {
			key.scope = 0
		}
		st := groups[key]
		if st == nil {
			st = &convTurnState{conversationID: string(convID), source: p.source, agentLifetime: lifetime, agentCalls: map[string]agentHistoryFact{}, agentTasks: map[string]*agentTaskHistory{}, agentEnded: map[string]bool{}}
			groups[key] = st
		}
		callID := p.ToolCallID
		if p.ToolUseID != "" {
			callID = p.ToolUseID
		}
		usable := func(id, field string) bool {
			return id != "" && !slices.Contains(p.Truncated, field) && !slices.Contains(p.Dropped, field)
		}
		switch p.typ {
		case historyAgentObserved, protocol.TypeToolUse:
			tool, parent := p.Tool, p.ParentToolCallID
			if p.typ == protocol.TypeToolUse {
				tool, parent = p.Name, p.Parent
			}
			if !usable(callID, "tool_use_id") || !usable(callID, "tool_call_id") || (tool != "Agent" && tool != "Task") {
				continue
			}
			call := st.agentCalls[callID]
			call.ToolCallID, call.Tool = callID, tool
			if parent != "" {
				call.ParentToolCallID = parent
			}
			if call.CallObservedEntryID == 0 {
				call.CallObservedEntryID = p.id
			}
			st.agentCalls[callID] = call
		case historyAgentResult, protocol.TypeToolResult:
			if !usable(callID, "tool_use_id") || !usable(callID, "tool_call_id") {
				continue
			}
			call := st.agentCalls[callID]
			call.Status = "reported"
			parent := p.ParentToolCallID
			if p.typ == protocol.TypeToolResult {
				parent = p.Parent
			}
			if parent != "" {
				call.ParentToolCallID = parent
			}
			st.agentCalls[callID] = call
		case historyAgentDenied, protocol.TypeToolDenied:
			if usable(callID, "tool_use_id") && usable(callID, "tool_call_id") {
				st.agentEnded[callID] = true
			}
		case historyAgentSessionEnded, historyTaskGone:
			if callID != "" {
				st.agentEnded[callID] = true
			}
			if p.TaskID != "" {
				task := startupObserveTask(st, p.TaskID, callID, p.id)
				task.ended = true
			}
		case historyTaskObserved, historyTaskLinked, historyTaskOutcome, protocol.TypeBackgroundTaskStarted, protocol.TypeBackgroundTaskUpdated, protocol.TypeBackgroundTaskProgress:
			if !usable(p.TaskID, "task_id") {
				continue
			}
			if !usable(callID, "tool_call_id") {
				callID = ""
			}
			task := startupObserveTask(st, p.TaskID, callID, p.id)
			if p.typ == historyTaskOutcome || (p.typ == protocol.TypeBackgroundTaskUpdated && p.Status != "") {
				task.ended = true
			}
		case protocol.TypeBackgroundTaskRoster:
			for _, row := range p.Tasks {
				if row.TaskID == "" || slices.Contains(row.Truncated, "task_id") {
					continue
				}
				call := row.ToolCallID
				if slices.Contains(row.Truncated, "tool_call_id") {
					call = ""
				}
				startupObserveTask(st, row.TaskID, call, p.id)
			}
		}
	}
	var out []*convTurnState
	for _, st := range groups {
		for id, call := range st.agentCalls {
			if call.Tool == "" {
				delete(st.agentCalls, id)
				continue
			}
			if closedCalls[startupAgentReference{st.source, st.agentLifetime, call.CallObservedEntryID, id}] {
				st.agentEnded[id] = true
			}
		}
		for id, task := range st.agentTasks {
			task.ended = task.ended || closedTasks[startupAgentReference{st.source, st.agentLifetime, task.observedID, id}]
			if task.ended && task.callID != "" {
				st.agentEnded[task.callID] = true
			}
		}
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b *convTurnState) int { return compareAgentOpening(a, b) })
	return out, nil
}

func startupObserveTask(st *convTurnState, taskID, callID string, id uint64) *agentTaskHistory {
	task := st.agentTasks[taskID]
	if task == nil {
		task = &agentTaskHistory{observedID: id}
		st.agentTasks[taskID] = task
	}
	if callID != "" {
		task.callID = callID
	}
	return task
}

func compareAgentOpening(a, b *convTurnState) int {
	first := func(st *convTurnState) uint64 {
		var id uint64
		for _, call := range st.agentCalls {
			if id == 0 || call.CallObservedEntryID < id {
				id = call.CallObservedEntryID
			}
		}
		for _, task := range st.agentTasks {
			if id == 0 || task.observedID < id {
				id = task.observedID
			}
		}
		return id
	}
	x, y := first(a), first(b)
	if x < y {
		return -1
	}
	if x > y {
		return 1
	}
	return 0
}

func closeStartupAgentWork(store *history.Store, logger *slog.Logger, work []*convTurnState, at time.Time) {
	for _, st := range work {
		for _, p := range st.endAgentWork("daemon_restart", at) {
			raw, err := json.Marshal(p)
			if err != nil {
				logger.Warn("history: startup fact marshal failed", "event", "startup_history.marshal_err")
				continue
			}
			appendConversationHistory(store, logger, "startup_history.append_err", p.ConversationID, historyAgentSessionEnded, raw, at, st.source)
		}
	}
}
