package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgPromoteConversationMalformed is the user-facing message emitted in the
// protocol.malformed error payload when PromoteConversationPayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back (it could reflect
// attacker-controlled payload bytes); only this static string.
const msgPromoteConversationMalformed = "malformed promote_conversation payload"

// msgPromoteConversationEmptyName is the user-facing message emitted in the
// protocol.malformed error payload when the requested channel name is empty or
// whitespace-only. Non-retryable: re-issuing the same blank name fails
// identically. Registry.Promote itself owns this refusal (ErrPromotionNameEmpty)
// — the handler does not duplicate the guard, only maps the sentinel.
const msgPromoteConversationEmptyName = "channel name must not be empty"

// msgPromoteConversationNotFound is the user-facing message emitted in the
// conversation.not_found error payload when conversation_id matches no row.
// Static — the requested id is never echoed on the wire.
const msgPromoteConversationNotFound = "conversation not found"

// msgPromoteConversationAlreadyPromoted is the user-facing message emitted in
// the conversation.already_promoted error payload when the target row is already
// a channel. Non-retryable: a replayed promote of an already-promoted row is
// refused idempotently (no nonce needed — same posture as rename).
const msgPromoteConversationAlreadyPromoted = "conversation already promoted"

// msgPromoteConversationNameInUse is the user-facing message emitted in the
// protocol.malformed error payload when another promoted conversation already
// carries the requested name. Client-fixable input error → malformed (no new
// wire code; mirrors rename's empty-name → malformed folding).
const msgPromoteConversationNameInUse = "channel name already in use"

// ConversationPromoter is the minimal registry surface this handler consumes:
// Promote flips the target row to a named channel, Get reads the promoted row
// back for the reply (Promote returns only an error, not the updated record),
// and Save eagerly persists. *conversations.Registry satisfies it structurally;
// no adapter required.
type ConversationPromoter interface {
	Promote(id conversations.ConversationID, name string) error
	Get(id conversations.ConversationID) (conversations.Conversation, bool)
	Save(path string) error
	// WorkspaceLabel supplies the reply's workspace_label (#2210), read under the
	// read-back row's own cwd — the payload's Cwd is not consumed here either.
	WorkspaceLabel(cwd string) (string, bool)
}

// PromoteConversation returns a dispatch.Handler that processes a
// promote_conversation frame ("Save as channel") from a paired client: it flips
// an existing scratch conversation to a named channel via Registry.Promote,
// eagerly persists the registry so the promotion survives a daemon restart, and
// replies with the reused conversation_updated record correlated via
// in_reply_to.
//
// reg is the conversations registry (the single writer — no reload-before-save);
// registryPath is the canonical on-disk path passed to the eager Save; logger is
// the daemon's slog logger.
//
// SECURITY (this verb carries the security-sensitive label — see the spec's
// § The Cwd decision and § Security review): PromoteConversationPayload carries a
// required Cwd that this handler deliberately does NOT consume. Promotion is a
// naming/persistence operation, not a relocation — the channel inherits the
// scratch conversation's existing (already $HOME-confined at create time) Cwd,
// and the payload Cwd reaches no filesystem operation, no registry write, and no
// spawn argument (Option B). It is read off the wire into the payload struct and
// discarded; the reply's Cwd is the row's pre-existing stored value, read back
// via Get. A future editor MUST NOT "wire up" p.Cwd without re-opening that
// decision and the security review — doing so would open the untrusted-path-as-
// workdir surface change_workspace guards (#823) that this verb, by design, does
// not have. The "Cwd ignored" unit test is the deterministic guard against that
// regression.
//
// The three untrusted payload fields are each contained: conversation_id is used
// ONLY as an exact-match registry key (a miss is conversation.not_found; a client
// can only name an id that already exists); name is stored as an opaque display
// string and echoed only to the requester (Registry.Promote enforces non-empty
// and cross-row name-uniqueness); cwd is discarded. All reject branches reply
// with a fixed static string — no supplied bytes (name, id, decode-error text)
// reach the wire — and the channel name is never logged (it is user content).
//
// Logging divergence from rename (adopt change_workspace's malformed discipline,
// do NOT "fix" it back): the malformed branch logs conn_id ONLY — not the decode
// err (Go's json.Unmarshal errors can embed offending input bytes) and not
// conversation_id (a decode failure may leave p.ConversationID holding raw
// attacker bytes). Every other branch runs post-decode, so conversation_id is a
// proven-decoded, slog-escaped structured field and is safe to log.
func PromoteConversation(reg ConversationPromoter, registryPath string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.PromoteConversationPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: promote_conversation malformed payload",
				"event", "promote_conversation.malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgPromoteConversationMalformed, false)
		}

		// Registry.Promote owns validation — empty/whitespace name, not-found,
		// already-promoted, and cross-row name-in-use — all under its lock, so no
		// pre-Promote guard is duplicated here (let the primitive own it). Map each
		// sentinel to its wire reply via errors.Is; no error falls through to
		// unsupported. Promote leaves the registry untouched on any refusal.
		if err := reg.Promote(conversations.ConversationID(p.ConversationID), p.Name); err != nil {
			switch {
			case errors.Is(err, conversations.ErrPromotionNameEmpty):
				logger.Warn("relay: promote_conversation empty name",
					"event", "promote_conversation.empty_name",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID)
				return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgPromoteConversationEmptyName, false)
			case errors.Is(err, conversations.ErrConversationNotFound):
				logger.Warn("relay: promote_conversation not found",
					"event", "promote_conversation.not_found",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID)
				return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgPromoteConversationNotFound, false)
			case errors.Is(err, conversations.ErrConversationAlreadyPromoted):
				logger.Warn("relay: promote_conversation already promoted",
					"event", "promote_conversation.already_promoted",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID)
				return replyError(ctx, c, env, protocol.CodeConversationAlreadyPromoted, msgPromoteConversationAlreadyPromoted, false)
			case errors.Is(err, conversations.ErrPromotionNameInUse):
				logger.Warn("relay: promote_conversation name in use",
					"event", "promote_conversation.name_in_use",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID)
				return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgPromoteConversationNameInUse, false)
			default:
				// Registry.Promote's contract returns only the four sentinels above.
				// A non-nil err outside that set is an unexpected internal fault:
				// surface it up rather than silently continuing to the success path
				// (never falls through to unsupported).
				return fmt.Errorf("promote conversation: %w", err)
			}
		}

		// Eager best-effort persist so the promotion survives a daemon restart.
		// Save failure is non-fatal (the in-memory promotion already happened and is
		// immediately usable); the logged err names the registry path — a filesystem
		// error, not attacker bytes — so it is safe to log. Mirrors rename/
		// change_workspace.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: promote_conversation persist failed",
				"event", "promote_conversation.persist_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
		}

		// Read the promoted row back for the reply — Promote returns only an error,
		// not the updated record. Promote→Get is two lock acquisitions, not atomic:
		// a concurrent delete of the same row between them yields a Get miss, which
		// is the truthful current state (conversation.not_found); any other
		// concurrent write is reflected verbatim in the read-back — also truthful
		// (spec § Concurrency model). Get returns the Conversation by value, and
		// got.Name points at the immutable heap string Promote stored, so the
		// snapshot is race-consistent with no extra copying.
		got, ok := reg.Get(conversations.ConversationID(p.ConversationID))
		if !ok {
			logger.Warn("relay: promote_conversation vanished before read-back",
				"event", "promote_conversation.not_found",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgPromoteConversationNotFound, false)
		}

		updated := protocol.ConversationUpdatedPayload{
			ID:         string(got.ID),
			IsPromoted: got.IsPromoted,
			IsArchived: got.IsArchived,
			IsMuted:    got.IsMuted,
			Name:       got.Name,
			Cwd:        got.Cwd, // the row's pre-existing (already-confined) cwd — payload Cwd is NOT consumed
			// Keyed on the same got.Cwd the line above sends, so the label always
			// names the workspace this frame reports rather than the one the
			// request happened to mention.
			WorkspaceLabel: workspaceLabelFor(reg, got.Cwd),
			LastUsedAt:     got.LastUsedAt,
		}
		payloadJSON, err := json.Marshal(updated)
		if err != nil {
			return fmt.Errorf("marshal conversation_updated payload: %w", err)
		}

		logger.Info("relay: promote_conversation promoted",
			"event", "promote_conversation.promoted",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID)
		return c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)
	}
}
