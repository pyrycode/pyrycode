package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	msgPinConversationMalformed   = "malformed " + protocol.TypeSetConversationPinned + " payload"
	msgPinConversationNotFound    = "conversation not found"
	msgPinConversationNotPromoted = "conversation not promoted"
)

// ConversationPinner is the registry surface for pinning. SetPinned owns atomic
// eligibility and mutation; Get supplies the stored snapshot for publication.
type ConversationPinner interface {
	SetPinned(id conversations.ConversationID, pinned bool, now time.Time) error
	Get(id conversations.ConversationID) (conversations.Conversation, bool)
	Save(path string) error
	WorkspaceLabel(cwd string) (string, bool)
}

// SetConversationPinned changes only a conversation's pin, eagerly saves even
// no-ops, then replies and announces the same stored record. Persistence is best
// effort; announcement still runs if the reply fails. A nil announce skips fan-out.
// Refusals expose neither request fields nor decoder or mutation error text.
func SetConversationPinned(reg ConversationPinner, registryPath string, announce ConversationAnnouncer, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.SetConversationPinnedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.Pinned == nil {
			logger.Warn("relay: set_conversation_pinned malformed payload",
				"event", "set_conversation_pinned.malformed", "conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgPinConversationMalformed, false)
		}

		id := conversations.ConversationID(p.ConversationID)
		var mutationErr error
		if id == "" {
			mutationErr = conversations.ErrConversationNotFound
		} else {
			mutationErr = reg.SetPinned(id, *p.Pinned, time.Now().UTC())
		}
		if mutationErr != nil {
			code, msg := protocol.CodeConversationNotFound, msgPinConversationNotFound
			switch {
			case errors.Is(mutationErr, conversations.ErrConversationNotFound):
			case errors.Is(mutationErr, conversations.ErrConversationNotPromoted):
				code, msg = protocol.CodeConversationNotPromoted, msgPinConversationNotPromoted
			default:
				return errors.New("set conversation pin: unexpected registry error")
			}
			logger.Warn("relay: set_conversation_pinned refused",
				"event", "set_conversation_pinned.refused", "conn_id", c.ConnID(), "code", code)
			return replyError(ctx, c, env, code, msg, false)
		}

		// A delete between the separately locked mutation and lookup means there
		// is no stored record to publish, matching SetConversationMuted.
		cv, ok := reg.Get(id)
		if !ok {
			logger.Warn("relay: set_conversation_pinned not found",
				"event", "set_conversation_pinned.not_found", "conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgPinConversationNotFound, false)
		}
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: set_conversation_pinned persist failed",
				"event", "set_conversation_pinned.persist_failed", "conn_id", c.ConnID(), "err", err)
		}

		record := protocol.ConversationUpdatedPayload{
			ID:             string(cv.ID),
			IsPromoted:     cv.IsPromoted,
			IsArchived:     cv.IsArchived,
			IsMuted:        cv.IsMuted,
			PinnedAt:       cv.PinnedAt,
			ReadUpTo:       cv.ReadUpTo,
			Name:           cv.Name,
			Cwd:            cv.Cwd,
			WorkspaceLabel: workspaceLabelFor(reg, cv.Cwd),
			LastUsedAt:     cv.LastUsedAt,
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal conversation_updated payload: %w", err)
		}
		logger.Info("relay: set_conversation_pinned applied",
			"event", "set_conversation_pinned.applied", "conn_id", c.ConnID(), "conversation_id", string(cv.ID))
		replyErr := c.Reply(ctx, env, protocol.TypeConversationUpdated, payload)
		if announce != nil {
			announce(record)
		}
		return replyErr
	}
}
