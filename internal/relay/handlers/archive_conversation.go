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

// msgArchiveConversationNotFound is the user-facing message emitted in the
// conversation.not_found error payload when conversation_id matches no row
// (either verb). Static — the requested id is never echoed on the wire.
const msgArchiveConversationNotFound = "conversation not found"

// ConversationArchiver is the minimal write surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required. SetArchived flips exactly the durable
// IsArchived flag (#880's deterministic single-field mutator); Get snapshots
// the post-flip record for the reply; Save eagerly persists.
type ConversationArchiver interface {
	SetArchived(id conversations.ConversationID, archived bool) bool
	Get(id conversations.ConversationID) (conversations.Conversation, bool)
	Save(path string) error
	// WorkspaceLabel supplies the reply's workspace_label (#2210). Archiving a
	// conversation does not un-name its folder, so the flip and the restore both
	// report the label exactly as an untouched row would.
	WorkspaceLabel(cwd string) (string, bool)
}

// ArchiveConversation returns a dispatch.Handler that processes an
// archive_conversation or unarchive_conversation frame from a paired client —
// a symmetric toggle of one durable flag selected by archived. It flips the
// named conversation's IsArchived flag, eagerly persists the registry so the
// change survives a daemon restart, and replies with the reused
// conversation_updated record (reflecting the new archived state) correlated
// via in_reply_to. archived=true archives; archived=false restores. The verb is
// idempotent: toggling an already-archived (or already-active) row still Saves
// and still replies with the unchanged state.
//
// reg is the conversations registry (the single writer — no reload-before-save);
// registryPath is the canonical on-disk path passed to the eager Save; logger
// is the daemon's slog logger. archived is baked into the returned handler, so
// the same factory registered twice yields the two verbs.
//
// SECURITY (mirrors delete_conversation, per the security-sensitive label): the
// one untrusted payload field, conversation_id, is used ONLY as an exact-match
// registry key inside SetArchived and Get (a byte comparison, no path/argv
// construction — a miss is conversation.not_found, and a client can only name an
// id that already exists). Both reject branches reply with a fixed static string
// — no payload bytes (the supplied id, a decode-error fragment) reach the wire.
//
// Two deliberate divergences from the rename_conversation template — keep them,
// do not "fix" them back: (1) the flip goes through SetArchived + a follow-up
// Get rather than an Update closure, honoring #880's single-field mutator
// (structurally cannot touch another field); (2) the malformed branch logs
// conn_id ONLY — not the decode err (Go's json.Unmarshal errors can embed
// offending input bytes) and not conversation_id (a decode failure leaves the
// struct at most partially populated, so p.ConversationID may hold raw attacker
// bytes). The not_found and success branches log the (never-wire-reaching, or
// proven-real) conversation_id as a structured slog field. The verb label in the
// malformed message is env.Type, a trusted dispatch-validated constant (the
// dispatcher matched it against the registered key to route here), never
// attacker bytes.
func ArchiveConversation(reg ConversationArchiver, registryPath string, logger *slog.Logger, archived bool) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.ArchiveConversationPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			// Divergence from rename: log conn_id ONLY. Neither err nor
			// conversation_id is logged — both can carry attacker payload bytes
			// on a decode failure.
			logger.Warn("relay: "+env.Type+" malformed payload",
				"event", env.Type+".malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, "malformed "+env.Type+" payload", false)
		}

		// Flip exactly the archived flag via #880's single-field mutator. A miss
		// (no such id, including a garbage/empty conversation_id) is
		// conversation.not_found and mutates nothing (AC #3).
		id := conversations.ConversationID(p.ConversationID)
		if !reg.SetArchived(id, archived) {
			logger.Warn("relay: "+env.Type+" not found",
				"event", env.Type+".not_found",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgArchiveConversationNotFound, false)
		}

		// Snapshot the post-flip record for the reply. SetArchived and Get are
		// two separately-locked ops; the only interleaving with an observable
		// effect is a concurrent delete_conversation removing the row between
		// them, which surfaces here as ok=false. Handle it as not_found: the flip
		// committed but the record is gone, so conversation_updated has no source
		// and not_found is truthful for the row's current state.
		cv, ok := reg.Get(id)
		if !ok {
			logger.Warn("relay: "+env.Type+" not found",
				"event", env.Type+".not_found",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgArchiveConversationNotFound, false)
		}

		// Eager best-effort persist so the change survives a daemon restart. Save
		// failure is non-fatal: the in-memory flip already happened and is
		// immediately usable; durability is best-effort, exactly as
		// create/rename/delete treat their Save.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: "+env.Type+" persist failed",
				"event", env.Type+".persist_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
		}

		payloadJSON, err := json.Marshal(protocol.ConversationUpdatedPayload{
			ID:             string(cv.ID),
			IsPromoted:     cv.IsPromoted,
			IsArchived:     cv.IsArchived,
			IsMuted:        cv.IsMuted,
			Name:           cv.Name,
			Cwd:            cv.Cwd,
			WorkspaceLabel: workspaceLabelFor(reg, cv.Cwd),
			LastUsedAt:     cv.LastUsedAt,
		})
		if err != nil {
			return fmt.Errorf("marshal conversation_updated payload: %w", err)
		}

		logger.Info("relay: "+env.Type+" applied",
			"event", env.Type+".applied",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID,
			"is_archived", archived)
		return c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)
	}
}
