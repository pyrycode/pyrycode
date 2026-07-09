package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgDeleteConversationMalformed is the user-facing message emitted in the
// protocol.malformed error payload when DeleteConversationPayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back (it could reflect
// attacker-controlled payload bytes); only this static string.
const msgDeleteConversationMalformed = "malformed delete_conversation payload"

// msgDeleteConversationNotFound is the user-facing message emitted in the
// conversation.not_found error payload when conversation_id matches no row.
// Static — the requested id is never echoed on the wire.
const msgDeleteConversationNotFound = "conversation not found"

// ConversationDeleter is the minimal write surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required.
type ConversationDeleter interface {
	Delete(id conversations.ConversationID) bool
	Save(path string) error
}

// DeleteConversation returns a dispatch.Handler that processes a
// delete_conversation frame from a paired client: it PERMANENTLY removes an
// existing conversation from the registry (hard delete — the reversible path is
// archive/unarchive), eagerly persists the registry so the removal survives a
// daemon restart, and replies with a conversation_deleted acknowledgement
// carrying the deleted id, correlated via in_reply_to.
//
// reg is the conversations registry (the single writer — no reload-before-save);
// registryPath is the canonical on-disk path passed to the eager Save; logger
// is the daemon's slog logger.
//
// SECURITY: the one untrusted payload field, conversation_id, is used ONLY as an
// exact-match registry key inside Delete (a byte comparison, no path/argv
// construction — a miss is conversation.not_found, and a client can only name an
// id that already exists). Both reject branches reply with a fixed static string
// — no payload bytes (the supplied id, a decode-error fragment) reach the wire.
//
// Two deliberate divergences from the rename_conversation template, mandated by
// the spec's AC #5 ("no attacker-controlled payload bytes ... logged"): the
// malformed branch logs conn_id ONLY — not the decode err (Go's json.Unmarshal
// errors can embed offending input bytes) and not conversation_id (a decode
// failure leaves the struct at most partially populated, so p.ConversationID may
// hold raw attacker bytes). The not_found and success branches log the (now
// proven-real, or never-wire-reaching) conversation_id as a structured,
// handler-escaped slog field, consistent with the rename/create precedent.
func DeleteConversation(reg ConversationDeleter, registryPath string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.DeleteConversationPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			// Divergence from rename: log conn_id ONLY. Neither err nor
			// conversation_id is logged — both can carry attacker payload bytes
			// on a decode failure (AC #5).
			logger.Warn("relay: delete_conversation malformed payload",
				"event", "delete_conversation.malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgDeleteConversationMalformed, false)
		}

		// Hard delete: exact-match removal of the caller-named row. A miss
		// (no such id, including an already-deleted id) is conversation.not_found
		// and mutates nothing — idempotent-on-miss (AC #4).
		hit := reg.Delete(conversations.ConversationID(p.ConversationID))
		if !hit {
			logger.Warn("relay: delete_conversation not found",
				"event", "delete_conversation.not_found",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgDeleteConversationNotFound, false)
		}

		// Eager best-effort persist so the deletion survives a daemon restart.
		// Save failure is non-fatal: the row is already gone in-memory;
		// durability is best-effort, exactly as create/rename treat their Save.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: delete_conversation persist failed",
				"event", "delete_conversation.persist_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
		}

		payloadJSON, err := json.Marshal(protocol.ConversationDeletedPayload{ID: p.ConversationID})
		if err != nil {
			return fmt.Errorf("marshal conversation_deleted payload: %w", err)
		}

		logger.Info("relay: delete_conversation deleted",
			"event", "delete_conversation.deleted",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID)
		return c.Reply(ctx, env, protocol.TypeConversationDeleted, payloadJSON)
	}
}
