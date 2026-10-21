package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func legacyRuntimeFact(typ string) bool {
	switch typ {
	case historyTurnOpened, historyToolInterrupted, historyTurnInterrupted, historySessionDivider,
		historySendAccepted, historySendDelivered, historySendDropped, historySendLost, historyPromptAnswered:
		return true
	default:
		return agentHistoryType(typ)
	}
}

// legacyRuntimeReceipt accounts only validated, known runtime facts. Info banners
// are nonvisual and lifecycle-neutral in legacy mobile; their required fields
// must all be present. No runtime payload or metadata crosses this boundary.
func legacyRuntimeReceipt(convID, typ string, raw json.RawMessage, id *uint64, ts time.Time) (json.RawMessage, bool) {
	if !legacyRuntimeFact(typ) || id == nil || *id == 0 || ts.IsZero() {
		return nil, false
	}
	if typ == historyPromptAnswered {
		if !validPromptAnswerFact(convID, raw, ts) {
			return nil, false
		}
		payload, err := json.Marshal(protocol.BannerPayload{ConversationID: convID, Level: "info"})
		return payload, err == nil
	}
	if queuedSendHistoryType(typ) {
		if !validQueuedSendFact(convID, typ, raw) {
			return nil, false
		}
		payload, err := json.Marshal(protocol.BannerPayload{ConversationID: convID, Level: "info"})
		return payload, err == nil
	}
	if agentHistoryType(typ) {
		if !validAgentHistoryFact(convID, typ, raw) {
			return nil, false
		}
		payload, err := json.Marshal(protocol.BannerPayload{ConversationID: convID, Level: "info"})
		return payload, err == nil
	}
	var fact runtimeHistoryFact
	if json.Unmarshal(raw, &fact) != nil || fact.ConversationID != convID || fact.OccurredAt.IsZero() {
		return nil, false
	}
	switch typ {
	case historyTurnOpened, historyTurnInterrupted:
		if fact.TurnID == "" {
			return nil, false
		}
	case historyToolInterrupted:
		if fact.TurnID == "" || fact.ToolCallID == "" {
			return nil, false
		}
	case historySessionDivider:
		if fact.Cause == "" || (fact.PreviousSessionID == "" && fact.Cause != "daemon_restart") {
			return nil, false
		}
	}
	payload, err := json.Marshal(protocol.BannerPayload{ConversationID: convID, Level: "info"})
	return payload, err == nil
}

// publishLegacyHistory preserves the interactive gate and shares one durable
// identity between live recipients and the optional replay ring. Callers own
// their serial envelope counter; this function performs no durable append.
func publishLegacyHistory(ctx context.Context, bcast interactiveBroadcaster, ring *eventring.Ring, logger *slog.Logger, nextID *uint64, convID, typ string, payload json.RawMessage, ts time.Time, historyID *uint64) {
	var eventID *uint64
	if ring != nil {
		id := ring.AppendWithHistoryID(convID, typ, payload, ts, historyID)
		eventID = &id
	}
	for _, c := range bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue
		}
		(*nextID)++
		env := protocol.Envelope{ID: *nextID, Type: typ, TS: ts, Payload: payload, EventID: eventID, HistoryEntryID: historyID}
		if err := bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Debug("relay: interactive history push dropped", "event", "interactive_turn.push_err", "conn_id", c.ConnID, "env_id", *nextID, "conversation_id", convID, "err", err)
		}
	}
}
