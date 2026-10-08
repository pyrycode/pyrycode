package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Static messages of mark_conversation_read's refusals. No request byte is ever
// echoed on the wire.
const (
	msgMarkReadMalformed       = "malformed " + protocol.TypeMarkConversationRead + " payload"
	msgMarkReadNotFound        = "conversation not found"
	msgMarkReadHistoryUnavail  = "conversation history is unavailable"
	msgMarkReadSaveUnavailable = "read mark could not be saved"
)

// ConversationReadMarker is the registry surface MarkConversationRead consumes.
// *conversations.Registry satisfies it structurally. AdvanceReadUpTo applies the
// clamped, monotonic advance and persists it itself; Get resolves the row before
// its history is read; WorkspaceLabel fills the record's workspace_label.
type ConversationReadMarker interface {
	Get(id conversations.ConversationID) (conversations.Conversation, bool)
	AdvanceReadUpTo(id conversations.ConversationID, upTo, latest uint64, path string) (conversations.Conversation, bool, error)
	WorkspaceLabel(cwd string) (string, bool)
}

// MarkConversationRead returns a dispatch.Handler for a mark_conversation_read
// frame (#2780). It raises the named conversation's host-local read mark to
// max(held, min(up_to, newest displayable durable history entry id)), so a mark
// can neither move back nor cover entries that do not exist yet. Every success replies with
// conversation_updated, correlated via in_reply_to and carrying the stored
// read_up_to. Only an actual advance is also pushed through announce, to every
// eligible client including the requester; a no-op pushes nothing.
//
// Unlike set_conversation_muted's best-effort Save, an advance that cannot be
// persisted is refused with the retryable read_mark.unavailable: the registry
// reverted it, so the same request advances afresh once saving works again.
//
// The handler holds no pool or runner seam, so it cannot spawn, restart or
// interrupt a session.
//
// SECURITY (security-sensitive label): conversation_id is first used as an
// exact-match registry key; only an id the registry has proven reaches the
// history store, which re-validates it before building a path. up_to decodes
// into a uint64, so encoding/json enforces the wire range. Every refusal sends
// a static message, and no refusal branch logs a payload field: not the decode
// err (encoding/json can quote input bytes), not an unproven conversation_id.
func MarkConversationRead(reg ConversationReadMarker, hist historyLatestReader, registryPath string, announce ConversationAnnouncer, logger *slog.Logger) dispatch.Handler {
	if store, ok := hist.(*history.Store); ok && store == nil {
		hist = nil
	}
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.MarkConversationReadPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.UpTo == nil {
			logger.Warn("relay: mark_conversation_read malformed payload",
				"event", "mark_conversation_read.malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgMarkReadMalformed, false)
		}

		id := conversations.ConversationID(p.ConversationID)
		notFound := func() error {
			logger.Warn("relay: mark_conversation_read not found",
				"event", "mark_conversation_read.not_found",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgMarkReadNotFound, false)
		}
		if _, ok := reg.Get(id); !ok {
			return notFound()
		}

		if hist == nil {
			return replyError(ctx, c, env, protocol.CodeHistoryUnavailable, msgMarkReadHistoryUnavail, true)
		}
		latest, err := hist.LatestDisplayableEntryID(id)
		if err != nil {
			logger.Error("relay: mark_conversation_read history read failed",
				"event", "mark_conversation_read.history_unavailable",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
			return replyError(ctx, c, env, protocol.CodeHistoryUnavailable, msgMarkReadHistoryUnavail, true)
		}

		cv, advanced, err := reg.AdvanceReadUpTo(id, *p.UpTo, latest, registryPath)
		if errors.Is(err, conversations.ErrConversationNotFound) {
			// Deleted between Get and the advance: not_found is truthful now.
			return notFound()
		}
		if err != nil {
			logger.Error("relay: mark_conversation_read persist failed",
				"event", "mark_conversation_read.persist_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
			return replyError(ctx, c, env, protocol.CodeReadMarkUnavailable, msgMarkReadSaveUnavailable, true)
		}

		// Read back from the registry, never assembled from the request.
		record := protocol.ConversationUpdatedPayload{
			ID:             string(cv.ID),
			IsPromoted:     cv.IsPromoted,
			IsArchived:     cv.IsArchived,
			IsMuted:        cv.IsMuted,
			ReadUpTo:       cv.ReadUpTo,
			Name:           cv.Name,
			Cwd:            cv.Cwd,
			WorkspaceLabel: workspaceLabelFor(reg, cv.Cwd),
			LastUsedAt:     cv.LastUsedAt,
		}
		payloadJSON, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal conversation_updated payload: %w", err)
		}

		logger.Info("relay: mark_conversation_read applied",
			"event", "mark_conversation_read.applied",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID,
			"read_up_to", cv.ReadUpTo,
			"advanced", advanced)

		// Reply first, as set_conversation_muted does; a failed reply (a torn-down
		// requester) must not keep the other clients from hearing an advance.
		replyErr := c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)
		if advanced && announce != nil {
			announce(record)
		}
		return replyErr
	}
}
