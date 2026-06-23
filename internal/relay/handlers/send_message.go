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

// msgServerBinaryOffline is the user-facing message emitted in the
// server.binary_offline error payload when the conversation exists but has no
// live bound session at enqueue time (empty CurrentSessionID, or the bound id
// is no longer in the pool). Retryable: the phone re-issues once the session is
// (re)bound.
const msgServerBinaryOffline = "server binary offline"

// TurnWriter is the minimal per-conversation write-surface that the inbound
// delivery seam drives. *sessions.Session satisfies it via one-line
// passthroughs to Session.Activate and Supervisor.WriteUserTurn. The interface
// lives in this package so handlers/ stays free of internal/sessions and
// internal/supervisor imports.
//
// Since #721 the send_message handler no longer drives a TurnWriter directly —
// it enqueues, and the daemon's msgqueue drain (cmd/pyry.newInboundDeliver)
// resolves and writes one message at a time. SessionRouter.Route still returns
// a TurnWriter so the handler can validate the binding before enqueue.
//
// Activate is called before WriteUserTurn so an idle-evicted bootstrap
// session lazily respawns claude on the next delivery rather than dropping it
// silently (#396). On an already-active session Activate is a near-no-op (two
// non-blocking channel receives).
type TurnWriter interface {
	Activate(ctx context.Context) error
	WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
}

// Enqueuer is the inbound backlog the send_message handler appends to instead
// of delivering synchronously. *msgqueue.Queue satisfies it. The interface is
// defined here, consumer-side, so handlers/ stays free of an internal/msgqueue
// import (mirrors SessionRouter and TurnWriter). Enqueue is non-blocking and
// returns the stable per-conversation id assigned to the message.
type Enqueuer interface {
	Enqueue(conversationID, text string) uint64
}

// SessionRouter resolves a send_message frame's conversation id to the write
// surface for that conversation's bound claude session (#678). Resolution is a
// pure in-memory lookup (no I/O, non-blocking) — the blocking activation
// happens in the returned TurnWriter's Activate, under the handler's existing
// budget. The interface lives in this package, returning a TurnWriter rather
// than any internal/sessions type, so handlers/ stays free of an
// internal/sessions import (mirrors SessionCreator in create_conversation.go).
//
// Failure mapping the handler relies on:
//   - unknown conversation → conversations.ErrConversationNotFound
//     (maps to conversation.not_found, not retryable)
//   - conversation has no bound session, or the bound session id is not in the
//     pool → any other non-nil error (maps to retryable server.binary_offline)
type SessionRouter interface {
	Route(conversationID string) (TurnWriter, error)
}

// SendMessage returns a dispatch.Handler that processes a send_message frame
// from the phone. router validates the frame's ConversationID against its bound
// claude session (#678) and stamps the active-conversation cursor (#687);
// queue is the daemon's inbound backlog the message is appended to; logger is
// the daemon's slog logger used for every branch's structured event.
//
// Since #721 the handler acks on ENQUEUE (accepted into the backlog), not on
// delivery-confirm. It validates the binding synchronously, enqueues
// non-blocking, and returns an ack; the daemon's msgqueue drain delivers the
// backlog one message at a time through the reliable WriteUserTurn path, paced
// by claude reaching idle (#704). This is the ADR 025 line 123 contract —
// send_message is unchanged at the wire level, queued by the daemon when claude
// is busy.
//
// The ack contract (which failures still produce an error reply vs are absorbed
// by the drain) is asymmetric by design:
//   - At enqueue we have a live phone to tell "retry", so a malformed payload, an
//     unknown conversation, and an unbound conversation are all rejected
//     synchronously, before any enqueue.
//   - Once enqueued, the ack promised delivery, so a transient resolve/activate/
//     write failure is held and retried by the drain rather than surfaced (a
//     conversation that becomes unbound post-ack is retried, not dropped).
//
// SECURITY:
//   - payload.Text is treated as opaque transit content: it is stored in the
//     in-memory FIFO and reaches the supervised claude child's stdin verbatim
//     only at the drain's WriteUserTurn call. No transformation, no length cap
//     beyond the transport's WS read ceiling (1 MiB; see internal/transport).
//   - payload.Text is NEVER logged at any level. conversation_id and message_id
//     (phone-supplied opaque ids) plus the assigned queued_msg_id are logged on
//     the enqueue path; conversation_id only on the reject paths.
//   - The phone supplies only the ConversationID lookup key and the Text; the
//     routing target (the bound session) is read from the server-stored registry
//     row, never phone-writable. An unbound conversation is rejected before
//     enqueue, so a turn is never silently routed to the shared bootstrap
//     session (#678 AC#4).
func SendMessage(router SessionRouter, queue Enqueuer, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.SendMessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: send_message malformed payload",
				"event", "send_message.malformed",
				"conn_id", c.ConnID(),
				"err", err)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgSendMessageMalformed, false)
		}

		// Validate the conversation's binding BEFORE enqueue, and let Route stamp
		// the active-conversation cursor — this IS the phone-interaction moment
		// (#687), the same as the synchronous handler. The returned writer is
		// discarded: the drain re-resolves at delivery time because the binding
		// may change between enqueue and drain. An unbound conversation is
		// rejected here, never enqueued, so a turn is never routed to the shared
		// bootstrap session (#678 AC#4).
		if _, routeErr := router.Route(p.ConversationID); routeErr != nil {
			switch {
			case errors.Is(routeErr, conversations.ErrConversationNotFound):
				logger.Warn("relay: send_message unknown conversation",
					"event", "send_message.unknown_conversation",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID)
				return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgConversationNotFound, false)
			default:
				// The conversation exists but has no live bound session (empty
				// CurrentSessionID, or the bound id is no longer in the pool).
				// Retryable so the phone re-issues after the session is (re)bound;
				// never falls through to the bootstrap — Route rejects an empty
				// binding before any Lookup (#678 AC#4).
				logger.Warn("relay: send_message no bound session",
					"event", "send_message.no_bound_session",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID,
					"err", routeErr)
				return replyError(ctx, c, env, protocol.CodeServerBinaryOffline, msgServerBinaryOffline, true)
			}
		}

		// Enqueue is non-blocking: it appends to the conversation's in-memory FIFO
		// and returns the stable id immediately. The ack now means "accepted into
		// the backlog", not "delivered/committed". payload.Text is NEVER logged.
		id := queue.Enqueue(p.ConversationID, p.Text)
		logger.Info("relay: send_message enqueued",
			"event", "send_message.enqueued",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID,
			"message_id", p.MessageID,
			"queued_msg_id", id)
		return replyAck(ctx, c, env)
	}
}
