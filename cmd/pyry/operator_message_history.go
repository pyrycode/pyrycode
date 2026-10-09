package main

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// newOperatorMessageHistory records safe client text at stream placement or
// confirmation, then publishes the same legacy payload exactly once. Composed
// delivery bytes and host attachment paths are absent from QueuedMessage.
func newOperatorMessageHistory(store *history.Store, push func(operatorMessage), place *sendNowPlacement, logger *slog.Logger, source ...history.SessionProvenance) msgqueue.DeliveredFunc {
	var sends *queuedSendHistory
	if place != nil {
		sends = place.sendHistory
	}
	return operatorMessageHistory(store, push, place, logger, sends, source...)
}

func operatorMessageHistory(store *history.Store, push func(operatorMessage), place *sendNowPlacement, logger *slog.Logger, sends *queuedSendHistory, source ...history.SessionProvenance) msgqueue.DeliveredFunc {
	return func(convID string, msg msgqueue.QueuedMessage) {
		captured := place.takeSource(convID, msg.ID)
		if len(source) != 0 {
			captured = source[0]
		}
		payload, err := json.Marshal(protocol.MessagePayload{
			ConversationID: convID,
			MessageID:      msg.MessageID,
			QueuedMsgID:    msg.ID,
			Role:           "user",
			Text:           msg.Text,
			AttachmentIDs:  msg.AttachmentIDs,
			DeviceName:     msg.DeviceName,
			ClientVersion:  msg.ClientVersion,
			ClientSentAt:   formatClientSentAt(msg.ClientSentAt),
			SentNow:        msg.SentNow,
		})
		if err != nil {
			logger.Debug("relay: operator-message history drop; payload marshal",
				"event", "operator_message.marshal_err",
				"conversation_id", convID)
			return
		}
		commit := func() {
			ref := sends.beforeDelivery(convID, msg.ID)
			ts := time.Now().UTC()
			historyEntryID := appendConversationHistory(store, logger, "operator_message.history_append_err",
				convID, protocol.TypeMessage, payload, ts, captured)
			sends.delivered(convID, msg.ID, ref, historyEntryID, ts, captured)
			if push != nil {
				push(operatorMessage{convID: convID, payload: payload, ts: ts, historyEntryID: historyEntryID})
			}
		}
		place.attach(convID, msg.ID, commit)
	}
}

// formatClientSentAt renders the client's parsed tap time for the stored entry
// (#2704) as UTC RFC 3339, or "" — which omits the key — when the client sent
// none the handler could parse.
func formatClientSentAt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
