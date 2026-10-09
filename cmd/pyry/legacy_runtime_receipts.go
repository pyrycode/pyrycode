package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func legacyRuntimeFact(typ string) bool {
	switch typ {
	case historyTurnOpened, historyToolInterrupted, historyTurnInterrupted, historySessionDivider:
		return true
	default:
		return false
	}
}

// legacyRuntimeReceipt accounts only validated, known runtime facts. Info banners
// are nonvisual and lifecycle-neutral in legacy mobile; their required fields
// must all be present. No runtime payload or metadata crosses this boundary.
func legacyRuntimeReceipt(convID, typ string, raw json.RawMessage, id *uint64, ts time.Time) (json.RawMessage, bool) {
	if !legacyRuntimeFact(typ) || id == nil || *id == 0 || ts.IsZero() {
		return nil, false
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

// legacyHistoryReader counts the legacy wire types, including validated runtime
// facts projected as banners, independently of raw visibility. Unsupported
// entries retain conservative visibility counting without certifying receipts.
// No raw entries, metadata or IDs are changed.
type legacyHistoryReader struct{ store *history.Store }

func (r legacyHistoryReader) LatestDisplayableEntryID(convID conversations.ConversationID) (uint64, error) {
	// Preserve nil-store errors and the raw reader's ID/containment validation.
	// An all-hidden log can still contain entries counted by legacy clients.
	latest, err := r.store.LatestEntryID(convID)
	if err != nil || latest == 0 {
		return latest, err
	}
	cursor := ""
	for {
		page, err := r.store.Page(convID, cursor, 128)
		if err != nil {
			return 0, err
		}
		for _, entry := range page.Entries {
			// These status types never contribute to the client's unread target,
			// even if a producer explicitly stored them as shown.
			switch entry.Type {
			case protocol.TypeTurnState, protocol.TypeStall, protocol.TypeApiRetry,
				protocol.TypeCompacting, protocol.TypeSessionTransition:
				continue
			}
			if legacyHistoryType(entry.Type) {
				return entry.ID, nil
			}
			if _, ok := legacyRuntimeReceipt(string(convID), entry.Type, entry.Payload, &entry.ID, entry.TS); ok {
				return entry.ID, nil
			}
			// Transport exclusion does not establish harmlessness. Unsupported
			// entries keep the raw visibility fallback and remain receipt holes.
			if entry.Shown == nil || *entry.Shown {
				return entry.ID, nil
			}
		}
		if page.AtStart {
			return 0, nil
		}
		cursor = page.Cursor
	}
}
