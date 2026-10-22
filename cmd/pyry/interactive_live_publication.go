package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// publishLiveState delivers supported state after emit has excluded content.
// The drain owns the envelope counter; the ring synchronizes its own access.
func (e *interactiveTurnEmitterV2) publishLiveState(ctx context.Context, convID, typ string, payload json.RawMessage, ts time.Time, historyID *uint64) {
	var eventID *uint64
	if e.ring != nil {
		id := e.ring.AppendWithHistoryID(convID, typ, payload, ts, historyID)
		eventID = &id
	}
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue
		}
		e.nextID++
		env := protocol.Envelope{ID: e.nextID, Type: typ, TS: ts, Payload: payload, EventID: eventID, HistoryEntryID: historyID}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return
			}
			e.logger.Debug("relay: live state push dropped", "event", "interactive_turn.push_err",
				"conn_id", c.ConnID, "env_id", e.nextID, "conversation_id", convID, "err", err)
		}
	}
}
