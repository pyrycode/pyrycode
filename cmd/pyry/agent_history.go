package main

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	historyAgentObserved = "agent_call_observed"
	historyAgentResult   = "agent_call_result"
	historyAgentDenied   = "agent_call_denied"
	historyTaskObserved  = "background_task_observed"
	historyTaskLinked    = "background_task_linked"
	historyTaskOutcome   = "background_task_outcome"
)

// agentHistoryFact supplies joins for the existing mapped reports, without
// copying their prose. IDs join only within a conversation and minted producer
// lifetime; daemon-local incarnation counters are not durable identities.
type agentHistoryFact struct {
	ConversationID   string    `json:"conversation_id"`
	LifetimeID       string    `json:"lifetime_id"`
	ToolCallID       string    `json:"tool_call_id,omitempty"`
	ParentToolCallID string    `json:"parent_tool_call_id,omitempty"`
	Tool             string    `json:"tool,omitempty"`
	TaskID           string    `json:"task_id,omitempty"`
	Status           string    `json:"status,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func agentHistoryType(typ string) bool {
	switch typ {
	case historyAgentObserved, historyAgentResult, historyAgentDenied,
		historyTaskObserved, historyTaskLinked, historyTaskOutcome:
		return true
	default:
		return false
	}
}

func validAgentHistoryFact(convID, typ string, raw json.RawMessage) bool {
	var p agentHistoryFact
	if json.Unmarshal(raw, &p) != nil || p.ConversationID != convID ||
		!conversations.ValidID(p.LifetimeID) || p.OccurredAt.IsZero() {
		return false
	}
	switch typ {
	case historyAgentObserved:
		return p.ToolCallID != "" && (p.Tool == "Agent" || p.Tool == "Task")
	case historyAgentResult:
		return p.ToolCallID != "" && (p.Status == string(turnevent.ToolStatusCompleted) || p.Status == string(turnevent.ToolStatusFailed))
	case historyAgentDenied:
		return p.ToolCallID != "" && p.Status == "denied"
	case historyTaskObserved:
		return p.TaskID != ""
	case historyTaskLinked:
		return p.TaskID != "" && p.ToolCallID != ""
	case historyTaskOutcome:
		return p.TaskID != "" && p.Status != ""
	default:
		return false
	}
}

func (e *interactiveTurnEmitterV2) recordAgentFact(ctx context.Context, typ string, p agentHistoryFact) {
	e.flushDelta(ctx)
	if e.agentLifetime == "" {
		id, err := conversations.NewID()
		if err != nil {
			e.logger.Warn("history: agent lifetime mint failed", "event", "agent_history.rand_err")
			return
		}
		e.agentLifetime = string(id)
	}
	p.ConversationID, p.LifetimeID, p.OccurredAt = e.conversationID, e.agentLifetime, time.Now().UTC()
	raw, err := json.Marshal(p)
	if err != nil {
		e.logger.Warn("history: agent fact marshal failed", "event", "agent_history.marshal_err")
		return
	}
	id := appendConversationHistory(e.hist, e.logger, "agent_history.append_err", p.ConversationID, typ, raw, p.OccurredAt, e.source)
	if receipt, ok := legacyRuntimeReceipt(p.ConversationID, typ, raw, id, p.OccurredAt); ok {
		publishLegacyHistory(ctx, e.bcast, e.ring, e.logger, &e.nextID, p.ConversationID, protocol.TypeBanner, receipt, p.OccurredAt, id)
	}
}

// observeAgentHistory never changes main-turn or child routing state. A call
// result establishes a foreground outcome unless a complete task link names
// that call in this lifetime. Linked results are launch reports, regardless of
// whether the link arrived before or after the result; only task outcomes end
// background work. Denials remain independent terminal evidence.
func (e *interactiveTurnEmitterV2) observeAgentHistory(ctx context.Context, ev turnevent.Event) {
	if !e.runtimeFacts || e.source.Kind != "claude" {
		return
	}
	switch v := ev.(type) {
	case turnevent.ToolStart:
		if v.ToolCallID == "" || (v.Title != "Agent" && v.Title != "Task") {
			return
		}
		p := agentHistoryFact{ToolCallID: v.ToolCallID, ParentToolCallID: v.ParentToolCallID, Tool: v.Title}
		e.recordAgentFact(ctx, historyAgentObserved, p)
	case turnevent.ToolUpdate:
		if _, observed := e.launcherTurns[v.ToolCallID]; !observed || (v.Status != turnevent.ToolStatusCompleted && v.Status != turnevent.ToolStatusFailed) {
			return
		}
		p := agentHistoryFact{ToolCallID: v.ToolCallID, ParentToolCallID: v.ParentToolCallID, Status: string(v.Status)}
		e.recordAgentFact(ctx, historyAgentResult, p)
	case turnevent.ToolCallDenied:
		if v.ToolCallID == "" || slices.Contains(v.DroppedFields, "tool_call_id") || slices.Contains(v.TruncatedFields, "tool_call_id") {
			return
		}
		if _, observed := e.launcherTurns[v.ToolCallID]; !observed {
			return
		}
		p := agentHistoryFact{ToolCallID: v.ToolCallID, Status: "denied"}
		e.recordAgentFact(ctx, historyAgentDenied, p)
	case turnevent.BackgroundTaskStarted:
		e.observeTaskIdentity(ctx, v.TaskID, v.ToolCallID, v.TruncatedFields)
	case turnevent.BackgroundTaskUpdated:
		if e.observeTaskIdentity(ctx, v.TaskID, "", v.TruncatedFields) && v.Status != "" {
			e.recordAgentFact(ctx, historyTaskOutcome, agentHistoryFact{TaskID: v.TaskID, Status: v.Status})
		}
	case turnevent.BackgroundTaskProgress:
		e.observeTaskIdentity(ctx, v.TaskID, "", v.TruncatedFields)
	case turnevent.BackgroundTaskRoster:
		for _, row := range v.Tasks {
			e.observeTaskIdentity(ctx, row.TaskID, row.ToolCallID, row.TruncatedFields)
		}
	}
}

func (e *interactiveTurnEmitterV2) observeTaskIdentity(ctx context.Context, taskID, callID string, truncated []string) bool {
	if taskID == "" || slices.Contains(truncated, "task_id") {
		return false
	}
	p := agentHistoryFact{TaskID: taskID}
	e.recordAgentFact(ctx, historyTaskObserved, p)
	if callID != "" && !slices.Contains(truncated, "tool_call_id") {
		p.ToolCallID = callID
		e.recordAgentFact(ctx, historyTaskLinked, p)
	}
	return true
}
