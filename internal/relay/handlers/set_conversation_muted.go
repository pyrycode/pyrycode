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

// msgMuteConversationNotFound is the static message of the
// conversation.not_found reply when conversation_id matches no row. The
// requested id is never echoed on the wire. Same text as archive's, kept as a
// constant of its own so the two verbs' messages are not coupled.
const msgMuteConversationNotFound = "conversation not found"

// msgMuteConversationMalformed is the static message of the protocol.malformed
// reply: a payload that does not decode, and one with no muted value.
const msgMuteConversationMalformed = "malformed " + protocol.TypeSetConversationMuted + " payload"

// ConversationMuter is the minimal write surface this handler consumes from the
// conversations registry. *conversations.Registry satisfies it structurally.
// SetMuted writes exactly the durable IsMuted flag; Get snapshots the stored
// record for the reply and the push; Save eagerly persists; WorkspaceLabel fills
// the record's workspace_label, which muting does not change.
type ConversationMuter interface {
	SetMuted(id conversations.ConversationID, muted bool) bool
	Get(id conversations.ConversationID) (conversations.Conversation, bool)
	Save(path string) error
	WorkspaceLabel(cwd string) (string, bool)
}

// SetConversationMuted returns a dispatch.Handler for a set_conversation_muted
// frame (#2572). It sets or clears the named conversation's IsMuted from the
// payload's required muted value, eagerly persists the registry so the value
// survives a daemon restart, replies to the requester with conversation_updated
// correlated via in_reply_to, and pushes the same record through announce to
// every interactive conn. The push is what makes a mute set on one client stop
// alerts on the others without a re-list; archive, the template, has no push.
// Sending the value the conversation already has succeeds and changes nothing.
//
// announce is the send_message auto-naming announcer (#2159) reused as is: it
// does not exclude the requester, which folds the pushed record by its payload
// id. A nil announce means no fan-out.
//
// Muting touches no session: the handler holds no pool or runner seam, so it
// cannot restart, spawn or interrupt anything.
//
// SECURITY (security-sensitive label): conversation_id is used only as an
// exact-match registry key inside SetMuted and Get; muted is a bool. Every
// reject replies with a fixed static message, and no reject branch logs a
// payload field: not the decode err (encoding/json can quote input bytes), not
// conversation_id (a partial decode can leave raw bytes in it, and an unknown id
// is still client-supplied bytes). Only the success branch logs the id, once the
// registry has proven it names a real row.
func SetConversationMuted(reg ConversationMuter, registryPath string, announce ConversationAnnouncer, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.SetConversationMutedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.Muted == nil {
			// A missing or null muted is malformed, not false: reading it as
			// false would silently unmute.
			logger.Warn("relay: set_conversation_muted malformed payload",
				"event", "set_conversation_muted.malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgMuteConversationMalformed, false)
		}
		muted := *p.Muted

		// A miss (no such id, including an empty one) mutates nothing.
		id := conversations.ConversationID(p.ConversationID)
		if !reg.SetMuted(id, muted) {
			logger.Warn("relay: set_conversation_muted not found",
				"event", "set_conversation_muted.not_found",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgMuteConversationNotFound, false)
		}

		// SetMuted and Get are separately locked. A concurrent
		// delete_conversation removing the row between them surfaces here as a
		// miss, and not_found is truthful for the row's current state.
		cv, ok := reg.Get(id)
		if !ok {
			logger.Warn("relay: set_conversation_muted not found",
				"event", "set_conversation_muted.not_found",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgMuteConversationNotFound, false)
		}

		// Best-effort persist, as archive treats its Save: the in-memory value is
		// already set and in use.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: set_conversation_muted persist failed",
				"event", "set_conversation_muted.persist_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
		}

		// The record is read back from the registry, never assembled from the
		// request, so the reply and the push carry only stored values.
		record := protocol.ConversationUpdatedPayload{
			ID:             string(cv.ID),
			IsPromoted:     cv.IsPromoted,
			IsArchived:     cv.IsArchived,
			IsMuted:        cv.IsMuted,
			Name:           cv.Name,
			Cwd:            cv.Cwd,
			WorkspaceLabel: workspaceLabelFor(reg, cv.Cwd),
			LastUsedAt:     cv.LastUsedAt,
		}
		payloadJSON, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal conversation_updated payload: %w", err)
		}

		logger.Info("relay: set_conversation_muted applied",
			"event", "set_conversation_muted.applied",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID,
			"is_muted", muted)

		// Reply first so the requester's correlated answer is the first frame it
		// sees for its own request. The push runs even when the reply fails (a
		// torn-down requester conn): the other clients must still hear.
		replyErr := c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)
		if announce != nil {
			announce(record)
		}
		return replyErr
	}
}
