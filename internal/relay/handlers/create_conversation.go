package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgCreateConversationMalformed is the user-facing message emitted in the
// protocol.malformed error payload when CreateConversationPayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back (it could reflect
// attacker-controlled payload bytes); only this static string.
const msgCreateConversationMalformed = "malformed create_conversation payload"

// msgCreateConversationServerError is the user-facing message emitted in the
// server.binary_offline error payload when conversations.NewID fails (a system
// rng failure — effectively unreachable on crypto/rand). Retryable so the phone
// re-issues rather than hitting a dead end.
const msgCreateConversationServerError = "server error creating conversation"

// ConversationCreator is the minimal write surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required.
type ConversationCreator interface {
	Create(c conversations.Conversation)
	Save(path string) error
}

// CreateConversation returns a dispatch.Handler that processes a
// create_conversation frame from the phone: it mints a fresh conversation id,
// records a registry row with the effective cwd / promoted flag / name (server
// defaults applied when a field is null), eagerly persists the registry, and
// replies with a conversation_created envelope correlated via in_reply_to.
//
// reg is the conversations registry; registryPath is the canonical on-disk path
// passed to the eager Save; defaultCwd is the absolute cwd recorded when the
// payload's cwd is null; logger is the daemon's slog logger.
//
// SECURITY: the payload's cwd is phone-influenced but inert stored metadata —
// no code path in the current tree spawns a process, chdirs, or joins a
// filesystem path from conversation.Cwd; it is echoed back only to the
// operator's own paired devices. A future ticket that spawns a per-conversation
// claude session at conversation.Cwd MUST canonicalise + boundary-check the
// path before use — that spawn-consumer owns the validation, not this handler.
// The created id is server-minted (conversations.NewID), never phone-supplied,
// so a phone cannot choose, collide, or overwrite a row (Create appends).
func CreateConversation(reg ConversationCreator, registryPath, defaultCwd string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.CreateConversationPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: create_conversation malformed payload",
				"event", "create_conversation.malformed",
				"conn_id", c.ConnID(),
				"err", err)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgCreateConversationMalformed, false)
		}

		// Resolve the three nullable fields to effective values. cwd falls back
		// to the daemon's default workdir; name is a pointer passthrough (nil
		// stays nil — an unnamed scratch discussion); promoted defaults false.
		promoted := p.IsPromoted != nil && *p.IsPromoted
		cwd := defaultCwd
		if p.Cwd != nil {
			cwd = *p.Cwd
		}
		name := p.Name

		id, err := conversations.NewID()
		if err != nil {
			logger.Error("relay: create_conversation id generation failed",
				"event", "create_conversation.id_failed",
				"conn_id", c.ConnID(),
				"err", err)
			return replyError(ctx, c, env, protocol.CodeServerBinaryOffline, msgCreateConversationServerError, true)
		}

		now := time.Now().UTC()
		reg.Create(conversations.Conversation{
			ID:         id,
			Name:       name,
			Cwd:        cwd,
			IsPromoted: promoted,
			LastUsedAt: now,
		})

		// Eager best-effort persist so a freshly created conversation survives a
		// daemon restart. The sweep loop Saves lazily (only on a non-zero archive
		// tick), so without this the row would be absent on the next pyry start.
		// Save failure is non-fatal: the row is live in-memory and immediately
		// usable; durability is best-effort, exactly as RunSweepLoop treats its
		// own Save.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: create_conversation persist failed",
				"event", "create_conversation.persist_failed",
				"conn_id", c.ConnID(),
				"err", err)
		}

		payloadJSON, err := json.Marshal(protocol.ConversationCreatedPayload{
			ID:         string(id),
			IsPromoted: promoted,
			Cwd:        cwd,
			Name:       name,
			LastUsedAt: now,
		})
		if err != nil {
			return fmt.Errorf("marshal conversation_created payload: %w", err)
		}

		logger.Info("relay: create_conversation created",
			"event", "create_conversation.created",
			"conn_id", c.ConnID(),
			"conversation_id", string(id))
		return c.Reply(ctx, env, protocol.TypeConversationCreated, payloadJSON)
	}
}
