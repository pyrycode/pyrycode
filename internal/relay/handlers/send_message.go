package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgSendMessageMalformed is the user-facing message emitted in the
// protocol.malformed error payload when SendMessagePayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back (it could
// reflect attacker-controlled payload bytes); only this static string.
const msgSendMessageMalformed = "malformed send_message payload"

// msgConversationNotFound is the user-facing message emitted in the
// conversation.not_found error payload when the supervisor's
// ValidateConversation refuses the inbound conversation_id.
const msgConversationNotFound = "conversation not found"

// TurnWriter is the minimal write-surface the send_message handler
// needs. *sessions.Session satisfies it via a one-line passthrough to
// Supervisor.WriteUserTurn. The interface lives in this package so
// handlers/ stays free of internal/sessions and internal/supervisor
// imports.
type TurnWriter interface {
	WriteUserTurn(conversationID string, payload []byte) error
}

// SendMessage returns a dispatch.Handler that processes a send_message
// frame from the phone. w is the per-conn write surface (the bootstrap
// session in production); logger is the daemon's slog logger used for
// every branch's structured event.
//
// SECURITY:
//   - payload.Text reaches the supervised claude child's stdin verbatim
//     via TurnWriter. No transformation, no length cap beyond the
//     transport's WS read ceiling (1 MiB; see internal/transport).
//   - payload.Text is NEVER logged at any level. conversation_id and
//     message_id (phone-supplied opaque ids) are logged on ack and
//     unknown-conversation paths only.
func SendMessage(w TurnWriter, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.SendMessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: send_message malformed payload",
				"event", "send_message.malformed",
				"conn_id", c.ConnID(),
				"err", err)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgSendMessageMalformed, false)
		}

		err := w.WriteUserTurn(p.ConversationID, []byte(p.Text))
		switch {
		case err == nil:
			logger.Info("relay: send_message ack",
				"event", "send_message.ack",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"message_id", p.MessageID)
			return replyAck(ctx, c, env)
		case errors.Is(err, conversations.ErrConversationNotFound):
			logger.Warn("relay: send_message unknown conversation",
				"event", "send_message.unknown_conversation",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgConversationNotFound, false)
		default:
			return err
		}
	}
}
