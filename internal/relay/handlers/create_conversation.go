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

// msgCreateConversationMintFailed is the user-facing message emitted in the
// server.binary_offline error payload when minting the conversation's dedicated
// session (creator.Create) fails — e.g. the pool is not running, the activate
// budget elapses, or the registry save inside the pool fails. Retryable so the
// phone re-issues and gets a fresh conversation + session; the wrapped error is
// logged but never echoed.
const msgCreateConversationMintFailed = "could not start conversation session"

// createConversationMintTimeout caps the per-handler wait for creator.Create to
// mint, supervise, and activate the conversation's dedicated session. Pool
// activation blocks until claude's PTY is ready or ctx-cancel, so an unbounded
// ctx would pin the per-conn goroutine on a wedged spawn; the bound turns that
// into a retryable server.binary_offline. Matches sendMessageActivateTimeout and
// internal/control's session-create budget. A tuning knob, not a contract.
const createConversationMintTimeout = 30 * time.Second

// ConversationCreator is the minimal write surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required.
type ConversationCreator interface {
	Create(c conversations.Conversation)
	Save(path string) error
}

// SessionCreator is the minimal session-mint surface this handler consumes from
// the sessions pool: mint, supervise, and activate one dedicated claude session,
// returning its id. *sessions.Pool.Create has the shape (SessionID, error), so
// it is adapted at the cmd/pyry boundary (sessionMinter) rather than satisfying
// this directly — keeping handlers/ free of internal/sessions imports, mirroring
// TurnWriter.
type SessionCreator interface {
	Create(ctx context.Context, label string) (string, error)
}

// CreateConversation returns a dispatch.Handler that processes a
// create_conversation frame from the phone: it mints a fresh conversation id,
// mints and binds a dedicated claude session for it via the sessions pool,
// records a registry row carrying the bound session id plus the effective cwd /
// promoted flag / name (server defaults applied when a field is null), eagerly
// persists the registry, and replies with a conversation_created envelope
// correlated via in_reply_to.
//
// reg is the conversations registry; creator mints the per-conversation session;
// registryPath is the canonical on-disk path passed to the eager Save;
// defaultCwd is the absolute cwd recorded when the payload's cwd is null; logger
// is the daemon's slog logger.
//
// SECURITY: this handler is now a spawn-consumer — it mints a per-conversation
// claude session via creator.Create. That session spawns in the daemon's
// already-trust-marked shared workdir (the pool's buildSession uses tpl.WorkDir,
// = the trusted Bootstrap.WorkDir), never conversation.Cwd: the handler passes
// only (mintCtx, string(id)) to creator.Create, so the one phone-influenced
// field that could be a spawn input (conversation.Cwd) is structurally excluded
// and remains inert stored metadata. Canonicalising + boundary-checking
// Cwd-as-spawn-input is deferred to the per-conversation-workdir follow-up, which
// will make Cwd the spawn workdir. The session id reaching claude's argv
// (--session-id) is server-minted (sessions.NewID, crypto/rand); the created
// conversation id is server-minted (conversations.NewID), never phone-supplied,
// so a phone cannot choose, collide, or overwrite a row (Create appends).
func CreateConversation(reg ConversationCreator, creator SessionCreator, registryPath, defaultCwd string, logger *slog.Logger) dispatch.Handler {
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

		// Mint and bind a dedicated claude session for this conversation before
		// recording the row, so AC#1 holds: the persisted row points at a session
		// that exists in the pool. The label is the server-minted conversation id
		// (a session↔conversation breadcrumb in the session registry); it never
		// reaches claude's argv — buildSession uses only the SessionID for
		// --session-id. The 30s budget turns a wedged spawn into a retryable reply
		// rather than pinning the per-conn goroutine indefinitely.
		mintCtx, cancel := context.WithTimeout(ctx, createConversationMintTimeout)
		sessionID, err := creator.Create(mintCtx, string(id))
		cancel()
		if err != nil {
			// Any mint failure (pool not running, activate timeout, save failure,
			// ctx deadline) fails the whole create: returning before reg.Create
			// leaves no half-bound orphan row, and the phone retries onto a fresh
			// conversation + session.
			logger.Warn("relay: create_conversation session mint failed",
				"event", "create_conversation.session_mint_failed",
				"conn_id", c.ConnID(),
				"conversation_id", string(id),
				"err", err)
			return replyError(ctx, c, env, protocol.CodeServerBinaryOffline, msgCreateConversationMintFailed, true)
		}

		now := time.Now().UTC()
		reg.Create(conversations.Conversation{
			ID:               id,
			Name:             name,
			Cwd:              cwd,
			CurrentSessionID: sessionID,
			IsPromoted:       promoted,
			LastUsedAt:       now,
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
			"conversation_id", string(id),
			"session_id", sessionID)
		return c.Reply(ctx, env, protocol.TypeConversationCreated, payloadJSON)
	}
}
