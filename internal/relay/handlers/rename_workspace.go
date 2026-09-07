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

// msgRenameWorkspaceMalformed is the user-facing message emitted in the
// protocol.malformed error payload when RenameWorkspacePayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back — encoding/json quotes
// offending input into its error string, and those bytes are remote-authored;
// only this static string goes on the wire.
const msgRenameWorkspaceMalformed = "malformed rename_workspace payload"

// msgRenameWorkspaceBlankLabel is the user-facing message emitted in the
// protocol.malformed error payload when the supplied label is empty or
// whitespace-only. Non-retryable: re-issuing the same blank label fails
// identically. Static — the supplied bytes are never echoed. Clearing a label is
// said with a null, not with a blank string, which is why this is a refusal
// rather than a second spelling of the clear.
const msgRenameWorkspaceBlankLabel = "workspace label must not be empty"

// msgRenameWorkspaceLabelTooLong is the user-facing message emitted in the
// protocol.malformed error payload when the supplied label exceeds
// protocol.MaxWorkspaceLabelBytes. Non-retryable. Static — and it names neither
// the value nor its length, so a reply cannot be used to binary-search the bound
// against a value the requester did not already hold.
const msgRenameWorkspaceLabelTooLong = "workspace label exceeds the maximum length"

// msgRenameWorkspaceNotFound is the user-facing message emitted in the
// workspace.not_found error payload when the requested path matches no stored
// conversation's cwd. Static — the requested path is never echoed on the wire,
// which matters more here than on the sibling verbs: a path is a filesystem
// location on the daemon's host.
const msgRenameWorkspaceNotFound = "workspace not found"

// WorkspaceLabeler is the minimal surface this handler consumes from the
// conversations registry. *conversations.Registry satisfies it structurally; no
// adapter required. The List line is copied verbatim from RecentWorkspacesReader
// so the variadic signature keeps matching structurally.
//
// IT DELIBERATELY HAS NO WorkspaceLabel READ METHOD. The reply's label is the
// request's own value, which IS the stored value by SetWorkspaceLabel's
// verbatim-store contract, so no read-back is needed — and lacking the method
// makes it structurally impossible for this handler to reply with a label the
// requester did not itself supply. A projection that cannot reach a value is a
// stronger guarantee than a handler that declines to fetch one. Do not widen it.
type WorkspaceLabeler interface {
	List(filter ...conversations.ListFilter) []conversations.Conversation
	SetWorkspaceLabel(cwd string, label *string)
	Save(path string) error
}

// RenameWorkspace returns a dispatch.Handler that processes a rename_workspace
// frame from a paired client: it sets or clears the operator-chosen display name
// of a workspace, eagerly persists the registry so the change survives a daemon
// restart, and replies with a workspace_updated record correlated via
// in_reply_to.
//
// Keyed by WORKSPACE, not by conversation, and that is why the reply is a new
// type rather than the reused conversation_updated record change_workspace
// answers with: N conversations share one cwd, a workspace has no row of its own,
// and no conversation record could carry the change without naming one arbitrary
// member of that set.
//
// It replies to the REQUESTER ONLY. Fanning the change out to other connected
// clients is #2209 and is deliberately not waited on here — an operator who
// renames from one client and re-lists on another sees the new label once the
// read projection lands (#2208).
//
// reg is the conversations registry (the single writer — no reload-before-save);
// registryPath is the canonical on-disk path passed to the eager Save; logger is
// the daemon's slog logger.
//
// SECURITY (this verb is security-sensitive — it stores untrusted operator text
// supplied by a network-paired party, keyed by a path that party also supplies):
//
//   - Reachability is the authenticated, paired Noise session. A frame is
//     decrypted under the session's receive state before it reaches dispatch, so
//     only the paired phone arrives here — the same gate every conversation write
//     verb has, needing no new code. No interactive-capability gate, matching
//     set_system_prompt: that capability is read only in the outbound fan-out and
//     is not an inbound gate.
//
//   - The path is a LOOKUP KEY AND NOTHING MORE. It is compared byte-for-byte
//     against stored cwds and used as a map key; it is never resolved, joined,
//     stat-ed, opened, or passed to a process. The one filesystem operation this
//     handler performs, Save, takes the daemon-derived registryPath — no client
//     byte reaches it. And the reply's path is projected from the MATCHED ROW's
//     Cwd rather than from the request (see the success arm), so no request byte
//     reaches the wire on any branch, reject or success.
//
//   - The label is an opaque display string: stored verbatim, echoed only to the
//     requester, never logged, never interpolated into an error message, never a
//     path component or an argv element. Its byte bound is a size limit and NOT a
//     safety property — see protocol.MaxWorkspaceLabelBytes, which says so at
//     length. Validity is inherited rather than checked: encoding/json substitutes
//     U+FFFD for invalid bytes and unpaired surrogates while decoding into a Go
//     string, so the value handed to SetWorkspaceLabel is always valid UTF-8,
//     which is exactly the assumption that setter documents it relies on.
//
//   - The not-found refusal is the label map's ONLY CEILING, a containment
//     property rather than a UX nicety: a key is creatable only at a path that
//     byte-equals a stored cwd, so the key count is bounded by the number of
//     distinct cwds the daemon hosts. Relaxing it would remove that bound.
//
//   - All four reject branches reply with a fixed static string, so no payload
//     byte — path, label, or decode error — reaches the wire on a refusal, and
//     none of them stores anything.
//
// Logging follows set_system_prompt's strict posture rather than
// rename_conversation's, and the divergence is deliberate — do NOT "fix" it back
// by pattern-matching the sibling that logs its conversation_id. Every branch logs
// event and conn_id (daemon-minted) and nothing else. Never logged: the label
// (operator content), the path (a host filesystem path, and attacker-supplied on
// every reject branch), and the decode error. The single exception is
// persist_failed's err, a filesystem error naming the daemon's own registry path.
// The cost is visible and accepted: the record says a label changed and over
// which conn_id, but not for which workspace.
func RenameWorkspace(reg WorkspaceLabeler, registryPath string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.RenameWorkspacePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			// Neither err nor path is logged — both can carry attacker payload
			// bytes on a decode failure, and a type error midway through a
			// well-formed object leaves path populated with supplied bytes while
			// still returning an error.
			logger.Warn("relay: rename_workspace malformed payload",
				"event", "rename_workspace.malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgRenameWorkspaceMalformed, false)
		}

		// Both value guards are skipped when Label is nil: a clear carries no
		// value to validate, and dereferencing to trim it would panic. The clear
		// still falls through to the existence check below — the not-found refusal
		// is unqualified, so clearing a label at a path no conversation names is
		// refused exactly like setting one.
		//
		// VALIDATION PRECEDES THE EXISTENCE CHECK, mirroring rename_conversation's
		// empty-name guard running before Update. Cheap client-fault rejects answer
		// first, so a blank or oversized label aimed at a path that does not exist
		// reveals nothing about whether it exists.
		if p.Label != nil {
			// Trim is used for the CHECK ONLY; the stored value below is the raw,
			// untrimmed wire value. Same split rename_conversation makes for its
			// title, and Registry.Promote before it.
			if strings.TrimSpace(*p.Label) == "" {
				logger.Warn("relay: rename_workspace blank label",
					"event", "rename_workspace.blank_label",
					"conn_id", c.ConnID())
				return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgRenameWorkspaceBlankLabel, false)
			}
			// len() on a Go string counts BYTES, which is the bound's unit — see
			// protocol.MaxWorkspaceLabelBytes on why bytes and not runes. Refused,
			// never truncated: a silently shortened label is a different name than
			// the operator typed, and they would have no way to tell.
			if len(*p.Label) > protocol.MaxWorkspaceLabelBytes {
				logger.Warn("relay: rename_workspace label too long",
					"event", "rename_workspace.label_too_long",
					"conn_id", c.ConnID())
				return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgRenameWorkspaceLabelTooLong, false)
			}
		}

		// The cwd existence check. Registry.List is the only read surface over
		// conversation rows and ListFilter has no cwd field, so the check ranges
		// the UNFILTERED list — which includes archived rows by design, the same
		// call recent_workspaces makes: a workspace is a folder, and archiving a
		// conversation does not un-name its folder. List returns a copy with the
		// registry mutex taken internally, so this scan runs lock-free on data the
		// handler owns.
		//
		// Comparison is byte-exact, matching WorkspaceLabel's own documented
		// contract: two paths differing only in a trailing separator, a trailing
		// space, or case are distinct workspaces. Unlike recent_workspaces, a row
		// whose Cwd is blank is NOT skipped here — that handler EMITS rows and an
		// empty string is not a workspace worth listing, while this one MATCHES a
		// key, and #2208 will read the label back under that same conversation's
		// cwd whatever it is.
		matched := ""
		found := false
		for _, conv := range reg.List() {
			if conv.Cwd == p.Path {
				matched = conv.Cwd
				found = true
				break
			}
		}
		if !found {
			logger.Warn("relay: rename_workspace not found",
				"event", "rename_workspace.not_found",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeWorkspaceNotFound, msgRenameWorkspaceNotFound, false)
		}

		// The store. SetWorkspaceLabel is #2206's deliberately unvalidated door: it
		// takes the *string through as the tri-state it decoded to — nil (or an
		// absent key) clears so a later read reports absent rather than a present
		// empty string, and non-nil text is stored verbatim. Passing the pointer
		// straight through is what routes the clear through the SAME door as a set.
		// Every refusal above returned before reaching this line, so nothing is
		// stored on any reject path and nothing above it did work that would need
		// undoing.
		reg.SetWorkspaceLabel(matched, p.Label)

		// Eager best-effort persist so the change survives a daemon restart. Save
		// failure is non-fatal: the in-memory write already happened and is what
		// every subsequent read sees, exactly as create/rename/delete/archive treat
		// their own Save. The logged err names the daemon's registry path (a
		// filesystem error), the one field on this handler safe to log.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: rename_workspace persist failed",
				"event", "rename_workspace.persist_failed",
				"conn_id", c.ConnID(),
				"err", err)
		}

		// Path is projected from the MATCHED ROW, never from the request. The two
		// are byte-equal by the match condition, so this changes no byte on the
		// wire — it changes where the bytes come from, making "the only path this
		// verb publishes is one the daemon itself stored" structural rather than
		// argued. Same posture set_system_prompt states for its own reply.
		payloadJSON, err := json.Marshal(protocol.WorkspaceUpdatedPayload{
			Path:  matched,
			Label: p.Label,
		})
		if err != nil {
			return fmt.Errorf("marshal workspace_updated payload: %w", err)
		}

		logger.Info("relay: rename_workspace applied",
			"event", "rename_workspace.applied",
			"conn_id", c.ConnID())
		return c.Reply(ctx, env, protocol.TypeWorkspaceUpdated, payloadJSON)
	}
}
