package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgRenameConversationMalformed is the user-facing message emitted in the
// protocol.malformed error payload when RenameConversationPayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back (it could reflect
// attacker-controlled payload bytes); only this static string.
const msgRenameConversationMalformed = "malformed rename_conversation payload"

// msgRenameConversationEmptyName is the user-facing message emitted in the
// protocol.malformed error payload when the requested title is empty or
// whitespace-only. Non-retryable: re-issuing the same blank title fails
// identically. Mirrors the Registry.Promote empty-name refusal.
const msgRenameConversationEmptyName = "conversation name must not be empty"

// msgRenameConversationNotFound is the user-facing message emitted in the
// conversation.not_found error payload when conversation_id matches no row.
// Static — the requested id is never echoed on the wire.
const msgRenameConversationNotFound = "conversation not found"

// ConversationRenamer is the minimal write surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required.
type ConversationRenamer interface {
	Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool
	Save(path string) error
}

// RenameConversation returns a dispatch.Handler that processes a
// rename_conversation frame from a paired client: it sets an existing
// conversation's display name to the requested title, eagerly persists the
// registry so the rename survives a daemon restart, and replies with the
// reused conversation_updated record correlated via in_reply_to.
//
// reg is the conversations registry (the single writer — no reload-before-save);
// registryPath is the canonical on-disk path passed to the eager Save; logger
// is the daemon's slog logger.
//
// SECURITY: the two untrusted payload fields are contained. conversation_id is
// used ONLY as an exact-match registry key inside Update (a byte comparison, no
// path/argv construction — a miss is conversation.not_found, and a client can
// only name an id that already exists). The title is stored as an opaque display
// string and echoed only to the requester. All three reject branches reply with
// a fixed static string — no payload bytes (title, id, decode error) reach the
// wire — and the title is never logged (it is user content); only the non-secret
// conversation_id is logged, mirroring create_conversation.
func RenameConversation(reg ConversationRenamer, registryPath string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.RenameConversationPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: rename_conversation malformed payload",
				"event", "rename_conversation.malformed",
				"conn_id", c.ConnID(),
				"err", err)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgRenameConversationMalformed, false)
		}

		// Empty-title guard runs BEFORE Update, so a blank rename leaves the
		// stored name untouched (AC #5). Mirrors Registry.Promote: trim is used
		// only for the check; the stored value is the raw (untrimmed) title.
		if strings.TrimSpace(p.Name) == "" {
			logger.Warn("relay: rename_conversation empty name",
				"event", "rename_conversation.empty_name",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgRenameConversationEmptyName, false)
		}

		// Set the name and snapshot the post-mutation record inside the single
		// locked fn — capturing under the lock avoids a find-then-read TOCTOU and
		// a second locked read. title is a local; both the row's Name and the
		// reply's Name point at it (never at the *Conversation held by the slice,
		// which a future Create may reallocate).
		title := p.Name
		var updated protocol.ConversationUpdatedPayload
		hit := reg.Update(conversations.ConversationID(p.ConversationID), func(cv *conversations.Conversation) {
			cv.Name = &title
			updated = protocol.ConversationUpdatedPayload{
				ID:         string(cv.ID),
				IsPromoted: cv.IsPromoted,
				IsArchived: cv.IsArchived, // preserve archived state on the shared reply payload
				Name:       &title,
				Cwd:        cv.Cwd,
				LastUsedAt: cv.LastUsedAt, // rename is a metadata edit, not a "use" — not bumped
			}
		})
		if !hit {
			logger.Warn("relay: rename_conversation not found",
				"event", "rename_conversation.not_found",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgRenameConversationNotFound, false)
		}

		// Eager best-effort persist so the rename survives a daemon restart. Save
		// failure is non-fatal: the row is live in-memory and immediately usable;
		// durability is best-effort, exactly as create_conversation treats its own
		// Save.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: rename_conversation persist failed",
				"event", "rename_conversation.persist_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
		}

		payloadJSON, err := json.Marshal(updated)
		if err != nil {
			return fmt.Errorf("marshal conversation_updated payload: %w", err)
		}

		logger.Info("relay: rename_conversation renamed",
			"event", "rename_conversation.renamed",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID)
		return c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)
	}
}
