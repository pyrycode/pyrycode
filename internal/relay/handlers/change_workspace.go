package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgChangeWorkspaceMalformed is the user-facing message emitted in the
// protocol.malformed error payload when ChangeWorkspacePayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back (it could reflect
// attacker-controlled payload bytes); only this static string.
const msgChangeWorkspaceMalformed = "malformed change_workspace payload"

// msgChangeWorkspaceEmpty is the user-facing message emitted in the
// protocol.malformed error payload when the requested workspace path is empty
// or whitespace-only. Non-retryable: re-issuing the same blank target fails
// identically. The guard runs BEFORE confinement because an empty path resolves
// to the daemon's process cwd (which may sit inside $HOME and PASS), which would
// silently store the daemon's cwd instead of cleanly rejecting.
const msgChangeWorkspaceEmpty = "workspace path must not be empty"

// msgChangeWorkspaceRejected is the user-facing message emitted in the
// protocol.malformed error payload when the requested workspace path is rejected
// by confinement — it escapes $HOME after symlink resolution, or is
// unresolvable. Non-retryable: re-issuing the same path fails identically. The
// message is static — it does NOT echo the path (the confine error names it).
const msgChangeWorkspaceRejected = "workspace directory not allowed"

// msgChangeWorkspaceNotFound is the user-facing message emitted in the
// conversation.not_found error payload when conversation_id matches no row.
// Static — the requested id is never echoed on the wire.
const msgChangeWorkspaceNotFound = "conversation not found"

// ErrWorkspaceRejected marks a deterministic rejection of a change_workspace
// target path (it escapes $HOME after symlink resolution, or is unresolvable).
// WorkspaceResolver implementations wrap every failure with it; the handler maps
// any non-nil resolver error to a non-retryable protocol.malformed reply,
// because re-issuing the same path fails identically (there is no transient
// failure mode — this verb does not spawn or mint). The sentinel lives in this
// consumer/mapper package (the cmd/pyry adapter wraps it, no import cycle),
// documents intent, and gives tests an errors.Is anchor; mirrors
// ErrSpawnDirRejected.
var ErrWorkspaceRejected = errors.New("conversation workspace directory rejected")

// WorkspaceResolver validates and canonicalises an untrusted target workspace
// path, returning the realpath confined to $HOME. It is injected at the cmd/pyry
// boundary (resolveWorkspaceDir) so internal/relay/handlers stays free of
// cmd/pyry imports — the same seam create_conversation uses for SessionCreator.
// A path escaping $HOME after symlink resolution, or one that is unresolvable,
// is returned as an error wrapping ErrWorkspaceRejected. The resolver performs
// NO filesystem mutation (it uses the strict, non-creating confiner), so any
// reject leaves no partial state.
type WorkspaceResolver func(requested string) (resolved string, err error)

// ConversationWorkspaceUpdater is the minimal write surface this handler
// consumes from the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required.
type ConversationWorkspaceUpdater interface {
	Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool
	Save(path string) error
	// WorkspaceLabel supplies the reply's workspace_label (#2210) and MUST NOT be
	// called from inside the Update callback: Update holds the registry's mutex
	// for the callback's duration and this method takes the same non-reentrant
	// lock, so a read there deadlocks the daemon. The handler reads after Update
	// returns.
	WorkspaceLabel(cwd string) (string, bool)
}

// ChangeWorkspace returns a dispatch.Handler that processes a change_workspace
// frame from a paired client (desktop Workspace Picker; later mobile): it moves
// an existing conversation to a client-chosen workspace folder by setting its
// recorded Cwd to the $HOME-confined realpath of the target, eagerly persists
// the registry so the change survives a daemon restart, and replies with the
// reused conversation_updated record (carrying the new workspace) correlated via
// in_reply_to. "Workspace" IS the conversation's Cwd; this verb changes the
// RECORDED workspace only — the new folder takes effect on the conversation's
// next fresh session spawn (conv.Cwd is decoupled from a live session's captured
// spawn WorkDir, #685/#686) — and the reply goes only to the requester (other
// clients pick up the change on their next list_conversations, which reads the
// live registry with no list-handler change).
//
// reg is the conversations registry (the single writer — no reload-before-save);
// resolve confines the untrusted target path to $HOME; registryPath is the
// canonical on-disk path passed to the eager Save; logger is the daemon's slog
// logger.
//
// SECURITY (this verb is security-sensitive — the one material difference from
// its rename/delete siblings — because it stores an untrusted FILESYSTEM PATH
// supplied by a network-paired party): the target Cwd becomes the conversation's
// spawn workdir on its next fresh session, so it is confined to $HOME (rejecting
// any path escaping $HOME after symlink resolution, fail-closed) BEFORE it is
// stored, and the RESOLVED realpath — the value that was security-confined — is
// what is persisted, not the raw path (validate == store). All four reject
// branches reply with a fixed static string; no supplied bytes (the path, the
// id, the decode-error text) reach the wire.
//
// Two logging divergences from the create_conversation / rename_conversation
// templates — keep them, do NOT "fix" them back by pattern-matching create:
//
//	(1) Malformed branch (inherited from delete_conversation #822): log conn_id
//	    ONLY — not the decode err (Go's json.Unmarshal errors can embed offending
//	    input bytes) and not conversation_id (a decode failure leaves the struct
//	    at most partially populated, so p.ConversationID may hold raw attacker
//	    bytes).
//
//	(2) Confine-rejected branch (NEW to this verb, the load-bearing one):
//	    create_conversation.go logs the wrapped confine err on rejection — and
//	    that err NAMES the offending path. This handler must NOT: it logs conn_id
//	    + conversation_id only, never the confine err, never the path (AC #5 — no
//	    attacker-controlled bytes echoed to the daemon log). conversation_id is a
//	    decode-success, slog-escaped structured field here and is safe to log,
//	    consistent with delete's not_found branch.
func ChangeWorkspace(reg ConversationWorkspaceUpdater, resolve WorkspaceResolver, registryPath string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.ChangeWorkspacePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			// Divergence 1: log conn_id ONLY. Neither err nor conversation_id is
			// logged — both can carry attacker payload bytes on a decode failure.
			logger.Warn("relay: change_workspace malformed payload",
				"event", "change_workspace.malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgChangeWorkspaceMalformed, false)
		}

		// Empty-path guard runs BEFORE confine: confineWorkdirToHome("") resolves
		// to the daemon's process cwd (which may sit inside $HOME and pass), so an
		// empty target would silently become the daemon's cwd rather than a clean
		// reject (AC #4). conversation_id is a decoded structured field here, so
		// it is safe to log.
		if strings.TrimSpace(p.Cwd) == "" {
			logger.Warn("relay: change_workspace empty workspace path",
				"event", "change_workspace.empty",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgChangeWorkspaceEmpty, false)
		}

		// Confine the untrusted target to $HOME (fail-closed) before storing it.
		// Any non-nil error is the deterministic, non-retryable rejected branch —
		// do NOT gate on errors.Is(err, ErrWorkspaceRejected): the resolver's
		// contract wraps every failure with the sentinel and every failure is
		// non-retryable, so a bare err != nil is both correct and gap-free (it can
		// never fall through to a hang or a spurious success if that wrap-
		// everything contract is ever weakened). The resolver performs no
		// filesystem mutation (strict, non-creating confiner), so no earlier
		// failure leaves partial state.
		//
		// Divergence 2: log conn_id + conversation_id only — NEVER the confine err,
		// NEVER the path (the confine err names the offending path; AC #5).
		resolved, err := resolve(p.Cwd)
		if err != nil {
			logger.Warn("relay: change_workspace workspace rejected",
				"event", "change_workspace.rejected",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgChangeWorkspaceRejected, false)
		}

		// Set Cwd to the confined realpath and snapshot the post-mutation record
		// inside the single locked fn — capturing under the lock avoids a
		// find-then-read TOCTOU and a second locked read (mirrors rename). The
		// stored value is the RESOLVED realpath, not p.Cwd: the persisted value is
		// the one that was security-confined, and it is idempotent under the
		// re-confinement every downstream consumer applies (the next fresh spawn's
		// resolveSpawnDir, #686's transcript resolver). LastUsedAt is a metadata
		// edit — not bumped. Cwd/resolved is a value string and cv.Name is a
		// *string to an immutable heap string, so the snapshot is race-consistent
		// after the lock releases.
		var updated protocol.ConversationUpdatedPayload
		hit := reg.Update(conversations.ConversationID(p.ConversationID), func(cv *conversations.Conversation) {
			cv.Cwd = resolved
			updated = protocol.ConversationUpdatedPayload{
				ID:         string(cv.ID),
				IsPromoted: cv.IsPromoted,
				IsArchived: cv.IsArchived,
				Name:       cv.Name,
				Cwd:        resolved,
				LastUsedAt: cv.LastUsedAt,
			}
		})
		if !hit {
			logger.Warn("relay: change_workspace not found",
				"event", "change_workspace.not_found",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID)
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgChangeWorkspaceNotFound, false)
		}

		// The destination workspace's label, read HERE and not in the callback
		// above (#2210). Update holds the registry's mutex while it runs the
		// callback and WorkspaceLabel takes that same non-reentrant mutex, so
		// filling this field where the rest of the record is built would deadlock
		// the daemon on an ordinary change_workspace — every registry consumer
		// behind a lock nobody ever releases.
		//
		// Keyed on resolved, the confined realpath this handler stored and put on
		// the payload's Cwd, never on the request's p.Cwd. Labels are keyed
		// byte-exactly, so looking up the raw request path would report the
		// destination as unlabelled wherever the resolver is not an identity
		// function — which the real one never is.
		//
		// After the hit check, so a not-found reply performs no pointless read.
		// The snapshot and this read are two lock acquisitions rather than one: a
		// rename_workspace landing between them is reflected here, which is the
		// truthful current state, exactly as this file's sibling read-backs are.
		updated.WorkspaceLabel = workspaceLabelFor(reg, resolved)

		// Eager best-effort persist so the change survives a daemon restart. Save
		// failure is non-fatal: the in-memory change already happened and is
		// immediately usable; durability is best-effort, exactly as
		// create/rename/delete treat their Save. The logged err names the registry
		// path (a filesystem error), not attacker bytes — safe to log.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: change_workspace persist failed",
				"event", "change_workspace.persist_failed",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"err", err)
		}

		payloadJSON, err := json.Marshal(updated)
		if err != nil {
			return fmt.Errorf("marshal conversation_updated payload: %w", err)
		}

		logger.Info("relay: change_workspace changed",
			"event", "change_workspace.changed",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID)
		return c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)
	}
}
