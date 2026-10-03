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
// It writes the OPERATOR's own turn, which no other producer can. Claude's echo
// of a delivered message reaches the daemon only as a digest (turnevent.UserEcho,
// #2730), never as text, and protocol.MessagePayload with role "user" has no other producer in the binary,
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
//
// push hands the same payload bytes and stamp to the live push (#2699), after the
// log append, so the wire and the log cannot differ. nil pushes nothing.
//
// A SEND-NOW MESSAGE COMMITS WHERE CLAUDE READ IT (#2730). Its payload is built
// here, from msg.Text as for every message, but the stamp, the append and the push
// are handed to place, which runs them when claude's echo of the write arrives, or
// when the turn goes idle without one. An ordinary message commits here, at the
// confirmed write, as before: claude reads it as the turn's opener, so the write
// is already where it sits. nil place commits every message here.
func newOperatorMessageHistory(store *history.Store, push func(operatorMessage), place *sendNowPlacement, logger *slog.Logger) msgqueue.DeliveredFunc {
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
			// The ids the send_message handler resolved (#2596), carried on the
			// queued record beside Text — never parsed out of the delivery payload.
			// nil for a message that named none, which omits the key.
			AttachmentIDs: msg.AttachmentIDs,
			// Who sent it (#2704), captured by the handler at enqueue: the pairing
			// record's name and the app version that connection's hello reported.
			// "" omits the key, so an older client's entry keeps today's bytes.
			DeviceName:    msg.DeviceName,
			ClientVersion: msg.ClientVersion,
			// The client's tap time, re-formatted HERE from the time the handler
			// parsed — never the client's own bytes.
			ClientSentAt: formatClientSentAt(msg.ClientSentAt),
		})
		if err != nil {
			// Defensive, matching both #2114 producers: MessagePayload is strings
			// and a string slice and cannot fail to marshal in practice. Never echo the payload
			// or err.Error() — encoding/json quotes invalid input bytes into its
			// error, which would put conversation content in a log line.
			logger.Debug("relay: operator-message history drop; payload marshal",
				"event", "operator_message.marshal_err",
				"conversation_id", convID)
			return
		}
		commit := func() {
			// Stamped at the commit — the confirmed write, or for a send-now message
			// claude's echo of it — not from msg.TS: that is the ENQUEUE time, and a
			// message can sit in the backlog for a long time, so an enqueue stamp
			// would place this entry behind ones carrying later timestamps and a
			// served page would read out of order. UTC matches what both #2114
			// producers hoist, so entries from all three are orderable by the field
			// the log stores.
			ts := time.Now().UTC()
			appendConversationHistory(store, logger, "operator_message.history_append_err",
				convID, protocol.TypeMessage, payload, ts)
			if push != nil {
				push(operatorMessage{convID: convID, payload: payload, ts: ts})
			}
		}
		if msg.SentNow {
			place.attach(convID, msg.ID, commit)
			return
		}
		commit()
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
