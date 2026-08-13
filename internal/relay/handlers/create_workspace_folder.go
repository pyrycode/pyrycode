package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgCreateWorkspaceFolderMalformed is the user-facing message emitted in the
// protocol.malformed error payload when CreateWorkspaceFolderPayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back (it could reflect
// attacker-controlled payload bytes); only this static string.
const msgCreateWorkspaceFolderMalformed = "malformed create_workspace_folder payload"

// msgCreateWorkspaceFolderEmptyParent is the user-facing message emitted in the
// protocol.malformed error payload when the requested parent path is empty or
// whitespace-only. Non-retryable. The guard runs BEFORE resolve because an empty
// parent joins to the folder name alone, which confineWorkdirToHomeCreating then
// resolves against the daemon's process cwd (which may sit inside $HOME and PASS)
// — silently creating the folder under the daemon cwd instead of cleanly
// rejecting.
const msgCreateWorkspaceFolderEmptyParent = "workspace parent path must not be empty"

// msgCreateWorkspaceFolderBadName is the user-facing message emitted in the
// protocol.malformed error payload when the requested folder name is not a single
// clean path element (empty, absolute, contains a path separator, or contains
// ".."). Non-retryable. This is the deterministic guarantee that the new folder
// lands DIRECTLY under the supplied parent, never in a nested subpath (confinement
// alone does not give this: "sub/dir" stays inside $HOME yet is not directly under
// the parent). The name is never echoed — it is attacker-controlled.
const msgCreateWorkspaceFolderBadName = "workspace folder name must be a single path element"

// msgCreateWorkspaceFolderRejected is the user-facing message emitted in the
// protocol.malformed error payload when the resolver rejects the target (it
// escapes $HOME after symlink resolution, is unresolvable, or cannot be created).
// Non-retryable: re-issuing the same request fails identically. The message is
// static — it does NOT echo the parent, the name, or the confine error (which
// names the offending path).
const msgCreateWorkspaceFolderRejected = "workspace folder not allowed"

// ErrWorkspaceFolderRejected marks a deterministic rejection of the target folder
// (its resolved path escapes $HOME, is unresolvable, or cannot be created).
// WorkspaceFolderResolver implementations wrap every failure with it; the handler
// maps any non-nil resolver error to a non-retryable protocol.malformed reply,
// because re-issuing the same request fails identically (there is no transient
// failure mode — this verb does not spawn or mint). The sentinel lives in this
// consumer/mapper package (the cmd/pyry adapter wraps it, no import cycle),
// documents intent, and gives tests an errors.Is anchor; mirrors
// ErrWorkspaceRejected / ErrSpawnDirRejected.
var ErrWorkspaceFolderRejected = errors.New("workspace folder rejected")

// WorkspaceFolderResolver validates and creates the target folder under an
// untrusted parent path, returning the created folder's realpath confined to
// $HOME. It is injected at the cmd/pyry boundary (resolveWorkspaceFolder) so
// internal/relay/handlers stays free of cmd/pyry imports — the same seam
// change_workspace uses for WorkspaceResolver and create_conversation for
// SessionCreator. Any failure (target escapes $HOME after symlink resolution, is
// unresolvable, or cannot be created) is returned as an error wrapping
// ErrWorkspaceFolderRejected. On an escape the resolver's confiner creates
// nothing (the $HOME check runs before MkdirAll); on an already-existing folder
// it returns that folder's realpath (idempotent MkdirAll semantics).
type WorkspaceFolderResolver func(parent, name string) (created string, err error)

// CreateWorkspaceFolder returns a dispatch.Handler that processes a
// create_workspace_folder frame from a paired client (desktop Workspace Picker's
// Create-folder dialog; later mobile): it creates a new folder on the daemon host
// under a client-supplied parent path — confined to $HOME — and replies with the
// new workspace_folder_created record carrying the created folder's canonical
// absolute path, correlated via in_reply_to. It consumes NO conversations
// registry: it creates a directory and returns its path, nothing more — the
// leanest handler in the write-verb family. resolve validates + creates the
// target under $HOME; logger is the daemon's slog logger.
//
// SECURITY (this verb is security-sensitive — it WRITES TO THE HOST FILESYSTEM a
// directory named by two untrusted fields, `parent` and `name`, supplied by a
// network-paired party). Two independent, fail-closed, deterministic properties
// hold — belt-and-suspenders, DIFFERENT FABRIC (two distinct code checks, not one
// gate twice):
//
//	(1) The resolved target is confined to $HOME — confineWorkdirToHomeCreating
//	    (in the injected resolver) rejects any path escaping $HOME after symlink
//	    resolution, BEFORE any directory is created (path traversal / symlink
//	    escape).
//	(2) The new folder lands DIRECTLY under the supplied parent — the name-shape
//	    guard below rejects a name that is empty, absolute, contains a path
//	    separator, or contains "..". Confinement alone does NOT give this: a name
//	    like "sub/dir" stays inside $HOME yet is not directly under the parent.
//
// No-leak logging is the co-crux, and is UNIFORM here (simpler than
// change_workspace's per-branch divergence) because there is no safe-to-log
// structured field except conn_id: BOTH decoded fields (`parent`, `name`) are
// attacker-controlled path components, so neither may be logged. The invariant:
// NO log record this handler emits — on ANY branch, including success — ever
// contains `parent`, `name`, a joined/resolved path, or a decode/confine `err`.
// Only conn_id + a static `event` name. Two traps a future editor will hit by
// pattern-matching the siblings — do NOT "fix" them back:
//
//   - Malformed branch: do NOT log the decode `err` (`CreateConversation`
//     and rename_conversation do). Go's json.Unmarshal errors can embed the
//     offending input bytes.
//   - Rejected branch: do NOT log the confine `err` or the path
//     (`CreateConversation` logs the wrapped confine err, which NAMES the
//     offending path). AC #7.
//
// All four reject branches reply with a fixed static string; no supplied bytes
// (the parent, the name, the decode-error text) reach the wire.
func CreateWorkspaceFolder(resolve WorkspaceFolderResolver, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.CreateWorkspaceFolderPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			// Log conn_id ONLY — never the decode err (json.Unmarshal errors can
			// embed offending input bytes) and never the partially-decoded fields.
			logger.Warn("relay: create_workspace_folder malformed payload",
				"event", "create_workspace_folder.malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderMalformed, false)
		}

		// Empty-parent guard runs BEFORE resolve: an empty parent joins to the
		// name alone, which the confiner resolves against the daemon's process cwd
		// (which may sit inside $HOME and pass), silently creating the folder under
		// the daemon cwd rather than cleanly rejecting.
		if strings.TrimSpace(p.Parent) == "" {
			logger.Warn("relay: create_workspace_folder empty parent path",
				"event", "create_workspace_folder.empty_parent",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderEmptyParent, false)
		}

		// Name-shape guard (AC #3): the deterministic guarantee that the folder
		// lands directly under the parent, independent of the confiner's escape
		// check. Pure string validation — no filesystem access.
		if !validWorkspaceFolderName(p.Name) {
			logger.Warn("relay: create_workspace_folder bad folder name",
				"event", "create_workspace_folder.bad_name",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderBadName, false)
		}

		// Resolve joins the name under the parent, confines the result to $HOME,
		// and creates the folder. Any non-nil error is the deterministic,
		// non-retryable rejected branch — a bare err != nil check (NOT gated on
		// errors.Is ErrWorkspaceFolderRejected) is correct and gap-free: the
		// resolver's contract wraps every failure with the sentinel and every
		// failure is non-retryable. On escape the confiner creates nothing (AC #2);
		// on an existing folder it returns that folder's realpath (AC #5); `created`
		// is the canonical realpath (AC #4). Log conn_id ONLY — NEVER the err, NEVER
		// the path (the confine err names the offending path; AC #7).
		created, err := resolve(p.Parent, p.Name)
		if err != nil {
			logger.Warn("relay: create_workspace_folder rejected",
				"event", "create_workspace_folder.rejected",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderRejected, false)
		}

		payloadJSON, err := json.Marshal(protocol.WorkspaceFolderCreatedPayload{Path: created})
		if err != nil {
			return fmt.Errorf("marshal workspace_folder_created payload: %w", err)
		}

		// Success logs conn_id ONLY too — no created path (uniform no-path posture;
		// the path is returned on the wire to the requester, which is not a leak,
		// but the daemon log stays path-free).
		logger.Info("relay: create_workspace_folder created",
			"event", "create_workspace_folder.created",
			"conn_id", c.ConnID())
		return c.Reply(ctx, env, protocol.TypeWorkspaceFolderCreated, payloadJSON)
	}
}

// validWorkspaceFolderName reports whether name is a single clean path element
// safe to join directly under an untrusted parent: non-empty (after trimming
// whitespace), not absolute, no path separator, and no "..". This is the
// deterministic AC #3 guarantee that the created folder lands directly under the
// parent and never in a nested or escaping subpath. The daemon is Linux/macOS-only,
// so '/' is the separator; the ".." substring check is intentionally strict
// (rejecting e.g. "a..b") — a conservative, security-positive over-rejection of an
// exotic name is preferable to any parent-relative traversal slipping through.
func validWorkspaceFolderName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	if filepath.IsAbs(name) {
		return false
	}
	if strings.ContainsRune(name, '/') {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	return true
}
