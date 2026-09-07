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
// session (creator.Create) fails — e.g. the pool is not running or the registry
// save inside the pool fails. Retryable so the phone re-issues and gets a fresh
// conversation + session; the wrapped error is logged but never echoed. A
// failure to START claude no longer surfaces here (#2085): the child comes up on
// the conversation's first message, so a spawn failure is the msgqueue drain's
// retry-and-give-up path, exactly as it already is for an idle-evicted
// conversation being woken.
const msgCreateConversationMintFailed = "could not start conversation session"

// msgCreateConversationCwdRejected is the user-facing message emitted in the
// protocol.malformed error payload when the conversation's requested Cwd is
// rejected as a spawn workdir — it escapes $HOME after symlink resolution, or is
// unresolvable. Non-retryable: re-issuing the same Cwd fails identically. The
// message is static — it does NOT echo the path or ~/.claude.json (the wrapped
// confine error is logged but never sent on the wire).
const msgCreateConversationCwdRejected = "conversation working directory not allowed"

// ErrSpawnDirRejected marks a deterministic rejection of a conversation's
// requested spawn workdir (it escapes $HOME after symlink resolution, or is
// unresolvable). SessionCreator implementations wrap it; the handler maps it to
// a non-retryable protocol.malformed reply rather than a retryable
// server.binary_offline, because re-issuing the same Cwd fails identically. The
// sentinel lives in this consumer/mapper package (cmd/pyry wraps it, no import
// cycle); mirrors the errors.Is mapping convention pinned in PROJECT-MEMORY.
var ErrSpawnDirRejected = errors.New("conversation spawn directory rejected")

// createConversationMintTimeout caps the per-handler wait for creator.Create to
// mint and supervise the conversation's dedicated session.
//
// Be clear about what it buys today: nothing. It bounded a spawn — Pool
// activation blocked until claude's PTY came up — and since #2085 there is no
// spawn here. The one production SessionCreator (sessionMinter) now discards the
// ctx outright, because neither half of what it does can observe one:
// resolveSpawnDir takes no context.Context and Pool.Mint is ctx-free by design,
// and a deadline does not interrupt a blocking filesystem syscall. So a wedged
// disk pins the conn's app-frame worker (#965) with this budget exactly as it
// would without it. Do not read protection into it that is not there; if a real
// bound on that wedge is ever wanted it has to be built at the syscall, not
// here.
//
// It is kept only as the ctx-honouring contract SessionCreator advertises: the
// interface takes a context.Context, so an implementation that respects one
// (a future minter that talks to something remote, say) gets a bound rather than
// inheriting the conn's. Matches internal/control's session-create budget.
// A tuning knob, not a contract.
const createConversationMintTimeout = 30 * time.Second

// ConversationCreator is the minimal write surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required.
type ConversationCreator interface {
	Create(c conversations.Conversation)
	Save(path string) error
	// WorkspaceLabel supplies the reply's workspace_label (#2210). A freshly
	// created conversation inherits whatever name its workspace already carries,
	// so a client that creates into a labelled folder renders the label from this
	// reply rather than from its next list.
	WorkspaceLabel(cwd string) (string, bool)
}

// SessionCreator is the minimal session-mint surface this handler consumes from
// the sessions pool: mint and supervise one dedicated claude session that will
// spawn in spawnDir, returning the session id. It does NOT start the child
// (#2085) — that happens on the conversation's first message, so that every
// per-session setting chosen before it is simply what the child launches with.
// It is adapted at the cmd/pyry boundary (sessionMinter) rather than satisfying
// *sessions.Pool directly — keeping handlers/ free of internal/sessions imports,
// mirroring TurnWriter.
//
// spawnDir == "" → the daemon's shared trusted workdir (default, unchanged). A
// non-empty spawnDir is the phone's *requested* working directory (the raw,
// untrusted conversation Cwd); the implementation validates it (confine to
// $HOME, symlink-resolve both sides) and trust-marks it before spawning. A
// requested dir that escapes $HOME is rejected with an error wrapping
// ErrSpawnDirRejected; the handler maps that to a non-retryable reply.
type SessionCreator interface {
	Create(ctx context.Context, label, spawnDir string) (string, error)
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
// SECURITY: this handler feeds the spawn path — it mints a per-conversation
// claude session via creator.Create, whose child will later spawn in the
// conversation's own (phone-influenced) Cwd rather than the daemon's shared
// workdir (#685, reversing the prior deferral). Since #2085 the mint no longer
// starts that child; the validation below is unchanged and still runs here, at
// mint time, and the accepted consequence of the wider validated-then-spawn
// window is recorded on sessionMinter.
// The phone's raw requested Cwd is forwarded verbatim as
// creator.Create's spawnDir; this handler does NO path handling and stays free of
// internal/sessions / cmd-layer imports. The cmd-layer adapter (sessionMinter →
// resolveSpawnDir) is the sole validator: it canonicalises + confines the Cwd to
// $HOME (rejecting any path that escapes after symlink resolution) and
// trust-marks the realpath before claude spawns, identical to the daemon's own
// bootstrap-workdir posture. A rejected Cwd surfaces as a non-retryable
// protocol.malformed reply (errors.Is ErrSpawnDirRejected) with no half-bound
// row; a null Cwd yields an empty spawnDir → the shared trusted workdir
// (unchanged). The session id reaching claude's argv (--session-id) is
// server-minted (sessions.NewID, crypto/rand); the created conversation id is
// server-minted (conversations.NewID), never phone-supplied, so a phone cannot
// choose, collide, or overwrite a row (Create appends).
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

		// spawnDir is the *raw* phone-requested working directory, read from the
		// nullable p.Cwd directly rather than the defaulted cwd above. Null → ""
		// so the pool spawns in the shared trusted workdir (AC#4, byte-identical
		// to today); a set Cwd is validated + trust-marked downstream at the
		// cmd-layer adapter before reaching the spawn. Keeping "where to spawn"
		// (spawnDir) separate from "what to record" (cwd) is what lets a default
		// conversation record defaultCwd yet still spawn in tpl.WorkDir.
		spawnDir := ""
		if p.Cwd != nil {
			spawnDir = *p.Cwd
		}

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
		// that exists in the pool. The bind stays EAGER (#2085 defers only the
		// spawn): set_session_settings is keyed on session_id and
		// request_session_settings answers all-zero for an empty CurrentSessionID,
		// so a conversation with no id yet could not be configured before its first
		// message — which is the whole point of deferring the spawn. The label is
		// the server-minted conversation id (a session↔conversation breadcrumb in
		// the session registry); it never reaches claude's argv — buildSession uses
		// only the SessionID for --session-id. The 30s budget binds only a
		// SessionCreator that honours a ctx, which the production one no longer
		// does — see createConversationMintTimeout for why it is inert here.
		mintCtx, cancel := context.WithTimeout(ctx, createConversationMintTimeout)
		sessionID, err := creator.Create(mintCtx, string(id), spawnDir)
		cancel()
		if err != nil {
			// A rejected Cwd (escapes $HOME / unresolvable) is deterministic:
			// re-issuing the same Cwd fails identically, so it maps to a
			// non-retryable protocol.malformed. The static message never echoes
			// the path; the wrapped confine error is logged only.
			if errors.Is(err, ErrSpawnDirRejected) {
				logger.Warn("relay: create_conversation spawn dir rejected",
					"event", "create_conversation.spawn_dir_rejected",
					"conn_id", c.ConnID(),
					"conversation_id", string(id),
					"err", err)
				return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgCreateConversationCwdRejected, false)
			}
			// Any other mint failure (pool not running, save failure, transient
			// trust-mark write error) is retryable. Returning before reg.Create
			// leaves no half-bound orphan row, and the phone retries onto a fresh
			// conversation + session. A mintCtx deadline is not in that list any
			// more: the production minter discards the ctx, so only a
			// ctx-honouring SessionCreator could return one — the arm stays a
			// catch-all rather than an enumeration.
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
			// Keyed on the same cwd the row was stored under and this frame
			// reports — the label belongs to the workspace, and a conversation
			// created into an already-named folder is named by it immediately.
			WorkspaceLabel: workspaceLabelFor(reg, cwd),
			Name:           name,
			LastUsedAt:     now,
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
