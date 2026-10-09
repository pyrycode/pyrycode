package main

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// endAgentWork closes task identities before remaining calls. A linked result
// describes launch, while an unlinked result is a foreground outcome.
func (st *convTurnState) endAgentWork(cause string, at time.Time) []agentHistoryFact {
	var out []agentHistoryFact
	ids := make([]string, 0, len(st.agentTasks))
	for id := range st.agentTasks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		task := st.agentTasks[id]
		if task.ended || st.agentEnded[task.callID] {
			continue
		}
		p := st.agentCalls[task.callID]
		p.ToolCallID, p.TaskID, p.TaskObservedEntryID = task.callID, id, task.observedID
		p.ConversationID, p.LifetimeID, p.Cause, p.OccurredAt, p.Status = st.conversationID, st.agentLifetime, cause, at, ""
		out = append(out, p)
		task.ended = true
		if task.callID != "" {
			st.agentEnded[task.callID] = true
		}
	}
	ids = nil
	for id := range st.agentCalls {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := st.agentCalls[id]
		if st.agentEnded[id] {
			continue
		}
		st.agentEnded[id] = true
		if p.Status != "" {
			continue
		}
		p.ConversationID, p.LifetimeID, p.Cause, p.OccurredAt = st.conversationID, st.agentLifetime, cause, at
		out = append(out, p)
	}
	return out
}

func (e *interactiveTurnEmitterV2) closeAgentHistory(ctx context.Context, cause string, at time.Time) {
	if !e.runtimeFacts || e.source.Kind != "claude" {
		return
	}
	for _, p := range e.endAgentWork(cause, at) {
		raw, err := json.Marshal(p)
		if err != nil {
			e.logger.Warn("history: agent ending marshal failed", "event", "agent_history.marshal_err")
			continue
		}
		id := appendConversationHistory(e.hist, e.logger, "agent_history.append_err", p.ConversationID, historyAgentSessionEnded, raw, at, e.source)
		if receipt, ok := legacyRuntimeReceipt(p.ConversationID, historyAgentSessionEnded, raw, id, at); ok {
			publishLegacyHistory(ctx, e.bcast, e.ring, e.logger, &e.nextID, p.ConversationID, protocol.TypeBanner, receipt, at, id)
		}
	}
}
