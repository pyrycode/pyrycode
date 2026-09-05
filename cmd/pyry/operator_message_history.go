package main

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// newOperatorMessageHistory builds the third producer against the daemon's one
// durable conversation log (#2115), beside the two #2114 wired: the interactive
// chokepoint's emit and the session-transition broadcast. It is a closure
// factory in the shape of queueStateNotify and sessionErrorNotify.
//
// It writes the OPERATOR's own turn, which no other producer can. Claude never
// echoes the text back (the stream parser's emitUser maps only tool_result) and
// protocol.MessagePayload with role "user" has no other producer in the binary,
// so without this the served log is claude's half of the conversation with the
// questions missing.
//
// WHY THIS SEAM AND NOT THE DELIVERY ONE. msgqueue.DeliverFunc receives the
// delivered payload bytes, which for an attachment-bearing message (#2038) are a
// daemon-composed prompt naming an attachment's ON-HOST PATH.
// docs/protocol-mobile.md § Error codes forbids putting the daemon's layout on
// the wire, and this log is served to paired devices (#2116), so a producer at
// newInboundDeliver would write the one value that must never leave the host.
// OnDelivered carries a msgqueue.QueuedMessage instead — the projection that
// declares no delivery field — so the client-readable text is the only content
// reachable here, structurally rather than by a filter below.
//
// store may be nil; appendConversationHistory makes that a silent no-op, so the
// seam stays wired unconditionally and no branch is added here.
func newOperatorMessageHistory(store *history.Store, logger *slog.Logger) msgqueue.DeliveredFunc {
	return func(convID string, msg msgqueue.QueuedMessage) {
		payload, err := json.Marshal(protocol.MessagePayload{
			ConversationID: convID,
			MessageID:      msg.MessageID,
			// The role literal, matching internal/streamsup's envelope builder and
			// internal/agentrun/streamrunner's — the repo declares no roleUser
			// constant. AC 5: the shape a served page already decodes, so the
			// operator's own turn needs no second arm.
			Role: "user",
			// msg.Text, and never a value derived from the delivery payload — see
			// the note above. msg carries no other text-shaped field to reach for.
			Text: msg.Text,
		})
		if err != nil {
			// Defensive, matching both #2114 producers: MessagePayload is four
			// strings and cannot fail to marshal in practice. Never echo the payload
			// or err.Error() — encoding/json quotes invalid input bytes into its
			// error, which would put conversation content in a log line.
			logger.Debug("relay: operator-message history drop; payload marshal",
				"event", "operator_message.marshal_err",
				"conversation_id", convID)
			return
		}
		// Stamped HERE, at the confirmed write, not from msg.TS — that is the
		// ENQUEUE time, and a message can sit in the backlog for a long time, so an
		// enqueue stamp would place this entry behind ones carrying later
		// timestamps and a served page would read out of order. UTC matches what
		// both #2114 producers hoist, so entries from all three are orderable by
		// the field the log stores.
		appendConversationHistory(store, logger, "operator_message.history_append_err",
			convID, protocol.TypeMessage, payload, time.Now().UTC())
	}
}
