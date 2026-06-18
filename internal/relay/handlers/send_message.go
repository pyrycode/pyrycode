package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// sendMessageActivateTimeout caps the per-handler wait for an evicted
// session's supervisor to respawn and bind its PTY. Matches
// internal/control/server.go's 30s VerbAttach budget so an inbound
// send_message and a CLI `pyry attach` behave uniformly on a freshly
// evicted session. #396.
const sendMessageActivateTimeout = 30 * time.Second

// sendMessageDeliverTimeout caps the per-handler wait for WriteUserTurn's
// ready-gate + commit-confirm delivery. WaitReady blocks while claude is
// mid-turn, so an unbounded ctx would hang the per-conn goroutine on a long
// claude turn; the bound turns "claude busy/wedged past budget" into a
// retryable server.binary_offline reply. Matches sendMessageActivateTimeout so
// the two phases of one inbound message share a budget shape. A tuning knob,
// not a contract.
const sendMessageDeliverTimeout = 30 * time.Second

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
// server.binary_offline error payload when Activate fails to bring the
// session's supervisor online before sendMessageActivateTimeout elapses.
const msgServerBinaryOffline = "server binary offline"

// TurnWriter is the minimal write-surface the send_message handler
// needs. *sessions.Session satisfies it via one-line passthroughs to
// Session.Activate and Supervisor.WriteUserTurn. The interface lives in
// this package so handlers/ stays free of internal/sessions and
// internal/supervisor imports.
//
// Activate is called before WriteUserTurn so an idle-evicted bootstrap
// session lazily respawns claude on the next inbound message rather
// than dropping it silently (#396). On an already-active session
// Activate is a near-no-op (two non-blocking channel receives).
type TurnWriter interface {
	Activate(ctx context.Context) error
	WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
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

// SendMessage returns a dispatch.Handler that processes a send_message
// frame from the phone. router resolves the frame's ConversationID to the
// write surface for that conversation's bound claude session (#678); logger
// is the daemon's slog logger used for every branch's structured event.
//
// SECURITY:
//   - payload.Text reaches the supervised claude child's stdin verbatim
//     via TurnWriter. No transformation, no length cap beyond the
//     transport's WS read ceiling (1 MiB; see internal/transport).
//   - payload.Text is NEVER logged at any level. conversation_id and
//     message_id (phone-supplied opaque ids) are logged on ack and
//     unknown-conversation paths only.
//   - The phone supplies only the ConversationID lookup key and the Text; the
//     routing target (the bound session) is read from the server-stored
//     registry row, never phone-writable. An unbound conversation is rejected
//     before any pool Lookup, so a turn is never silently routed to the shared
//     bootstrap session (AC#4).
func SendMessage(router SessionRouter, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.SendMessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: send_message malformed payload",
				"event", "send_message.malformed",
				"conn_id", c.ConnID(),
				"err", err)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgSendMessageMalformed, false)
		}

		// Resolve the conversation's bound session before any Activate/write, so
		// the turn lands in that discussion's own claude rather than the shared
		// bootstrap (#678). Route is a pure in-memory lookup; the blocking
		// Activate/WriteUserTurn below run against the resolved surface under the
		// unchanged two-phase budgets.
		w, routeErr := router.Route(p.ConversationID)
		if routeErr != nil {
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
				// binding before any Lookup (AC#4).
				logger.Warn("relay: send_message no bound session",
					"event", "send_message.no_bound_session",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID,
					"err", routeErr)
				return replyError(ctx, c, env, protocol.CodeServerBinaryOffline, msgServerBinaryOffline, true)
			}
		}

		// Activate first so an idle-evicted bootstrap session respawns
		// claude before we attempt the PTY write (#396). The 30s budget
		// matches the CLI attach path; a busted respawn surfaces as an
		// explicit server.binary_offline reply rather than a silent drop.
		activateCtx, cancelActivate := context.WithTimeout(ctx, sendMessageActivateTimeout)
		if err := w.Activate(activateCtx); err != nil {
			cancelActivate()
			// ctx.Canceled propagates to the dispatcher's per-conn
			// unwind (the conn is closing). Activate timeout or any
			// other Activate failure is surfaced as a wire reply so
			// the caller learns the binary is offline rather than
			// waiting indefinitely for an ack that never comes.
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return err
			}
			logger.Warn("relay: send_message activate failed",
				"event", "send_message.activate_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
			return replyError(ctx, c, env, protocol.CodeServerBinaryOffline, msgServerBinaryOffline, true)
		}
		cancelActivate()

		// Bound the delivery: WriteUserTurn's ready-gate blocks while claude is
		// busy, so an unbounded ctx would hang the per-conn goroutine. A
		// deliver-timeout yields context.DeadlineExceeded (not Canceled), so it
		// correctly lands in the default → binary_offline arm below.
		deliverCtx, cancelDeliver := context.WithTimeout(ctx, sendMessageDeliverTimeout)
		err := w.WriteUserTurn(deliverCtx, p.ConversationID, []byte(p.Text))
		cancelDeliver()
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
		case errors.Is(err, context.Canceled) && ctx.Err() != nil:
			// The parent conn ctx is closing — propagate for the per-conn
			// unwind rather than emitting a doomed wire reply. Mirrors the
			// Activate-block check above. A deliver-timeout is
			// DeadlineExceeded, not Canceled, so it falls through to default.
			return err
		default:
			// Every other WriteUserTurn failure mode is transient — no live
			// session, claude not idle within budget, a wedged delivery, or a
			// PTY closing — so report a retryable binary_offline instead of a
			// false ack (Route does not reply on a bare handler error). The
			// supervisor's sentinels (ErrNoLiveSession, ErrTurnNotCommitted)
			// land here without handlers/ importing internal/supervisor.
			logger.Warn("relay: send_message delivery failed",
				"event", "send_message.delivery_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
			return replyError(ctx, c, env, protocol.CodeServerBinaryOffline, msgServerBinaryOffline, true)
		}
	}
}
