package main

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	historyTurnOpened      = "main_turn_opened"
	historyToolInterrupted = "main_tool_interrupted"
	historyTurnInterrupted = "main_turn_interrupted"
	historySessionDivider  = "session_divider"
)

// runtimeHistoryFact contains identities and lifecycle outcomes, never thought
// text, tool inputs, prompts or inferred live-state readings.
type runtimeHistoryFact struct {
	ConversationID      string    `json:"conversation_id"`
	TurnID              string    `json:"turn_id,omitempty"`
	ToolCallID          string    `json:"tool_call_id,omitempty"`
	Tool                string    `json:"tool,omitempty"`
	Cause               string    `json:"cause,omitempty"`
	OccurredAt          time.Time `json:"occurred_at"`
	PreviousSessionID   string    `json:"previous_session_id,omitempty"`
	NewSessionID        string    `json:"new_session_id,omitempty"`
	PreviousAgent       string    `json:"previous_agent,omitempty"`
	NextAgent           string    `json:"next_agent,omitempty"`
	ResetHandoffOutcome *string   `json:"reset_handoff_outcome,omitempty"`
}

func runtimeDivider(t sessions.SessionTransition) (runtimeHistoryFact, bool) {
	if t.PreviousID == "" || t.PreviousID == t.NewID || t.ConversationID == "" {
		return runtimeHistoryFact{}, false
	}
	switch t.Cause {
	case sessions.CauseOperatorReset, sessions.CauseClaudeClear, sessions.CauseAgentSwitch,
		sessions.CauseRecovery, sessions.CauseWorkspaceChange, sessions.CauseIdleSleep, sessions.CauseCapacityEviction:
	default:
		return runtimeHistoryFact{}, false
	}
	p := runtimeHistoryFact{ConversationID: t.ConversationID, Cause: string(t.Cause), OccurredAt: t.OccurredAt,
		PreviousSessionID: string(t.PreviousID), NewSessionID: string(t.NewID), PreviousAgent: t.PreviousAgent, NextAgent: t.NextAgent}
	if t.Cause == sessions.CauseOperatorReset && t.ResetHandoffOutcome != nil && (*t.ResetHandoffOutcome == "written" || *t.ResetHandoffOutcome == "skipped") {
		outcome := *t.ResetHandoffOutcome
		p.ResetHandoffOutcome = &outcome
	}
	return p, true
}

func (e *interactiveTurnEmitterV2) recordRuntimeFact(typ string, p runtimeHistoryFact) {
	raw, err := json.Marshal(p)
	if err != nil {
		e.logger.Warn("history: runtime fact marshal failed", "event", "runtime_history.marshal_err")
		return
	}
	appendConversationHistory(e.hist, e.logger, "runtime_history.append_err", p.ConversationID, typ, raw, p.OccurredAt, e.source)
}

func runtimeSourceKey(convID, sessionID string) string { return convID + "\x00" + sessionID }

func (e *interactiveTurnEmitterV2) hasRuntimeTurn(convID string) bool {
	for _, st := range e.turns {
		if st.conversationID == convID && st.inTurn {
			return true
		}
	}
	return false
}

// closeRuntimeSource is the composable pre-divider closure point. It is called
// only by the stream drain, under the existing publication gate. An exit closes
// preceding work but does not seal the routing ID for a replacement child.
func (e *interactiveTurnEmitterV2) closeRuntimeSource(ctx context.Context, convID, sessionID, cause string, at time.Time, seal bool, exitEpoch uint64) {
	if seal {
		if e.runtimeSealed == nil {
			e.runtimeSealed = make(map[string]bool)
		}
		e.runtimeSealed[runtimeSourceKey(convID, sessionID)] = true
	}
	var keys []string
	for key, st := range e.turns {
		if st.conversationID == convID && (st.source.SessionID == sessionID || st.source.Kind == "") && (exitEpoch == 0 || st.runtimeEpoch < exitEpoch) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		e.convTurnState = e.turns[key]
		e.flushDelta(ctx)
		if e.inTurn {
			ids := make([]string, 0, len(e.runtimeTools))
			for id := range e.runtimeTools {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				e.recordRuntimeFact(historyToolInterrupted, runtimeHistoryFact{ConversationID: convID, TurnID: e.turnID, ToolCallID: id, Tool: e.runtimeTools[id], Cause: cause, OccurredAt: at})
			}
			e.recordRuntimeFact(historyTurnInterrupted, runtimeHistoryFact{ConversationID: convID, TurnID: e.turnID, Cause: cause, OccurredAt: at})
			e.transitionTo(ctx, convID, turnbridge.StateIdle)
			e.endTurn()
		}
		e.childLanes, e.launcherTurns, e.childToolTurns = nil, nil, nil
		e.releaseConversation(key)
	}
}

func (e *interactiveTurnEmitterV2) trackRuntimeTool(ev turnevent.Event) {
	if !e.runtimeFacts {
		return
	}
	switch v := ev.(type) {
	case turnevent.ToolStart:
		if v.ParentToolCallID == "" && v.Title != "Agent" && v.Title != "Task" {
			if e.runtimeTools == nil {
				e.runtimeTools = make(map[string]string)
			}
			e.runtimeTools[v.ToolCallID] = v.Title
		}
	case turnevent.ToolUpdate:
		if v.ParentToolCallID == "" && (v.Status == "" || v.Status == turnevent.ToolStatusCompleted || v.Status == turnevent.ToolStatusFailed) {
			delete(e.runtimeTools, v.ToolCallID)
		}
	case turnevent.ToolCallDenied:
		delete(e.runtimeTools, v.ToolCallID)
	}
}
