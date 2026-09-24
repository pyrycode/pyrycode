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

// msgSetSystemPromptMalformed is the user-facing message emitted in the
// protocol.malformed error payload when SetSystemPromptPayload cannot be
// JSON-decoded — and also on the fail-closed default arm below. The decode-error
// text is NOT echoed back (it could reflect attacker-controlled payload bytes);
// only this static string.
const msgSetSystemPromptMalformed = "malformed set_system_prompt payload"

// msgSetSystemPromptTooLong is the user-facing message emitted in the
// protocol.malformed error payload when the supplied prompt exceeds
// conversations.MaxSystemPromptBytes. Non-retryable: re-issuing the same
// over-length value fails identically. Static — it names neither the value nor
// its length, so a reply cannot be used to binary-search the bound against a
// value the requester did not already hold.
const msgSetSystemPromptTooLong = "system prompt exceeds the maximum length"

// msgSetSystemPromptInvalidUTF8 is the user-facing message emitted in the
// protocol.malformed error payload when the registry refuses a prompt that is
// not valid UTF-8. Non-retryable. See the note on the branch itself: no wire
// payload can reach it, because encoding/json substitutes U+FFFD while decoding.
const msgSetSystemPromptInvalidUTF8 = "system prompt is not valid UTF-8"

// msgSetSystemPromptNotFound is the user-facing message emitted in the
// conversation.not_found error payload when conversation_id matches no row.
// Static — the requested id is never echoed on the wire.
const msgSetSystemPromptNotFound = "conversation not found"

// ConversationSystemPromptSetter is the minimal write surface this handler
// consumes from the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required. SetSystemPrompt is #2149's validating door
// (byte bound, UTF-8 validity, then existence, all under the registry lock); Get
// snapshots the post-write record for the reply, because unlike Update that door
// returns an error rather than the mutated record; Save eagerly persists.
//
// That this interface names NO session, pool or runner surface is load-bearing,
// not incidental — it is the structural half of "setting the prompt leaves a
// running session alone". The handler cannot restart, rotate, recompose the argv
// of, or interrupt a live child because it holds no seam through which any of
// those could be reached. Do not widen it. The value's route to a running
// session's NEXT start is #2150's refreshSystemPrompt, called from Pool.Activate,
// which re-reads exactly the registry field written here.
type ConversationSystemPromptSetter interface {
	SetSystemPrompt(id conversations.ConversationID, prompt *string) error
	Get(id conversations.ConversationID) (conversations.Conversation, bool)
	Save(path string) error
	// WorkspaceLabel supplies the reply's workspace_label (#2210). It is a fourth
	// registry door on a handler whose narrowness is itself asserted — see
	// TestSetSystemPrompt_TouchesNoSessionSurface, which pins the exact call
	// sequence. Still a conversations-registry read and still no session,
	// pool or runner seam.
	WorkspaceLabel(cwd string) (string, bool)
}

// SetSystemPrompt returns a dispatch.Handler that processes a set_system_prompt
// frame from a paired client: it stores or clears a conversation's durable
// per-conversation system prompt — the operator-authored text every session that
// conversation spawns is given — eagerly persists the registry so the change
// survives a daemon restart, and replies with the reused conversation_updated
// record correlated via in_reply_to.
//
// Keyed by CONVERSATION, not by session, and deliberately so: a prompt must be
// settable with no session live and must outlive every session the conversation
// has. (The session-keyed sibling is set_session_settings, which reaches
// sessions.Pool.UpdateSettings and pairs with a restart. This verb does not, and
// must not.)
//
// The new value takes effect at the conversation's NEXT session start, not on a
// running child — the daemon stores the value and leaves the live session alone.
// That cost is real and is surfaced rather than hidden: an operator who edits the
// prompt and keeps typing sees no change. Telling them why is the read half's job
// (#2152) and the client's.
//
// reg is the conversations registry (the single writer — no reload-before-save);
// registryPath is the canonical on-disk path passed to the eager Save; logger is
// the daemon's slog logger.
//
// SECURITY (this verb is security-sensitive — it stores untrusted operator TEXT
// supplied by a network-paired party, text that becomes standing instructions to
// a claude child holding full tool permissions):
//
//   - Reachability is the authenticated, paired Noise session. A frame is
//     decrypted under the session's receive state before it reaches dispatch, so
//     only the paired phone arrives here — the same gate every conversation write
//     verb has, needing no new code. The interactive capability is NOT an inbound
//     gate (V2Session.interactive is read only in the outbound ActiveConns
//     fan-out), which is why change_workspace carries no such check either.
//
//   - No privilege is escalated: the same gate already permits send_message, i.e.
//     arbitrary user-role input to that same claude. What IS new is durability —
//     a prompt survives restarts, applies to every future session including ones
//     started from the host CLI, and appears in no message history. Making it
//     readable so an operator can see what is set is #2152.
//
//   - Validation is delegated whole to the registry door and re-implemented
//     nowhere. The handler does NO work before that call, which is what makes
//     "nothing is persisted on a reject" a property of the door rather than of
//     handler discipline.
//
//   - The reply cannot carry the prompt: ConversationUpdatedPayload has no such
//     field (see the note there). A projection type that lacks a field is a
//     stronger guarantee than a handler that declines to fill one in.
//
// Logging divergence from BOTH templates — keep it, do NOT "fix" it back by
// pattern-matching change_workspace or archive_conversation, which each log
// conversation_id as a structured field on their non-malformed branches. This
// handler logs conversation_id on NO branch, the success path included. Only
// conn_id (daemon-minted) and a per-branch event string appear. Never logged: the
// prompt, its length, the conversation id, the decode error (Go's json.Unmarshal
// errors can embed offending input bytes), and the registry sentinel — the
// sentinels are static and would be safe, but a distinct event per branch carries
// the same diagnostic signal with no per-sentinel judgment call. The single
// exception is persist_failed's err, a filesystem error naming the daemon's own
// registry path.
//
// The cost of that is visible and accepted: the applied record says a prompt
// changed and over which conn_id, but not which conversation.
func SetSystemPrompt(reg ConversationSystemPromptSetter, registryPath string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.SetSystemPromptPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			// Log conn_id ONLY. Neither err nor conversation_id is logged — both can
			// carry attacker payload bytes on a decode failure, and a type error
			// midway through a well-formed object leaves conversation_id populated
			// with supplied bytes while still returning an error.
			logger.Warn("relay: set_system_prompt malformed payload",
				"event", "set_system_prompt.malformed",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgSetSystemPromptMalformed, false)
		}

		// The one validating door: byte bound, UTF-8 validity, then existence, all
		// under the registry lock. p.SystemPrompt passes through as the tri-state it
		// decoded to — nil (or an absent key) clears, non-nil "" is the explicitly
		// empty state, non-nil text is stored verbatim. Passing the pointer straight
		// through is what makes the clear path go through the SAME validated door as
		// a set, which is the whole reason #2149's signature takes a *string.
		//
		// On any refusal the registry is left untouched, so nothing is persisted on
		// any reject path — and nothing above this line has done any work that would
		// need undoing.
		id := conversations.ConversationID(p.ConversationID)
		if err := reg.SetSystemPrompt(id, p.SystemPrompt); err != nil {
			code, msg, event := protocol.CodeProtocolMalformed, msgSetSystemPromptMalformed, "set_system_prompt.unknown_refusal"
			switch {
			case errors.Is(err, conversations.ErrSystemPromptTooLong):
				msg, event = msgSetSystemPromptTooLong, "set_system_prompt.too_long"
			case errors.Is(err, conversations.ErrSystemPromptInvalidUTF8):
				// Unreachable from the wire and kept anyway. encoding/json substitutes
				// U+FFFD for every invalid byte and every unpaired surrogate while
				// decoding a string, so the value handed to the door above is always
				// valid UTF-8 no matter what arrives. The registry's check is defence
				// in depth for its non-wire callers; mapping the sentinel here is free,
				// and NOT mapping it would make this a fall-through.
				// TestSetSystemPrompt_InvalidUTF8IsUnreachableFromTheWire pins the
				// unreachability so a later reader does not "fix" the missing wire case
				// with a payload that would quietly exercise the success path.
				msg, event = msgSetSystemPromptInvalidUTF8, "set_system_prompt.invalid_utf8"
			case errors.Is(err, conversations.ErrConversationNotFound):
				code, msg, event = protocol.CodeConversationNotFound, msgSetSystemPromptNotFound, "set_system_prompt.not_found"
			}
			// The default is the initialised triple above: any refusal this switch does
			// not recognise — a fourth sentinel a future registry change adds — becomes
			// a clean non-retryable protocol.malformed. Failing closed matters here
			// because the alternative is falling through to the snapshot-and-reply path
			// below and acking a write that never happened.
			logger.Warn("relay: set_system_prompt refused",
				"event", event,
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, code, msg, false)
		}

		// Snapshot the post-write record for the reply. SetSystemPrompt returns an
		// error rather than the mutated record (unlike Update, which hands rename and
		// change_workspace a snapshot inside its locked callback), so the reply needs
		// this separate read — the same shape archive_conversation uses with
		// SetArchived. The two ops are separately locked; the only interleaving with
		// an observable effect is a concurrent delete_conversation removing the row
		// between them, which surfaces as ok=false. Handle it as not_found: the write
		// committed but the record is gone, so conversation_updated has no source and
		// not_found is truthful for the row's current state.
		//
		// Folding both into one Update closure would close that gap and is rejected:
		// it would route the write around the validating door, and Update is
		// documented as deliberately unvalidated.
		cv, ok := reg.Get(id)
		if !ok {
			logger.Warn("relay: set_system_prompt refused",
				"event", "set_system_prompt.not_found",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgSetSystemPromptNotFound, false)
		}

		// Eager best-effort persist so the change survives a daemon restart. Save
		// failure is non-fatal: the in-memory value already happened and is what the
		// next spawn reads, exactly as create/rename/delete/archive treat their Save.
		// The logged err names the registry path (a filesystem error), not supplied
		// bytes — the one field on this handler safe to log.
		if err := reg.Save(registryPath); err != nil {
			logger.Error("relay: set_system_prompt persist failed",
				"event", "set_system_prompt.persist_failed",
				"conn_id", c.ConnID(),
				"err", err)
		}

		// Projected from the STORED record, never from the request: ID is cv.ID, so
		// the one request-derived value on the wire is registry-held state reached
		// only after the id matched an existing row. LastUsedAt is not bumped —
		// setting a prompt is a metadata edit, the same call the sibling verbs make.
		// cv shares the registry's SystemPrompt pointer (Get copies shallowly), which
		// would be a live aliasing hazard if this payload projected the field; it has
		// no such field and structurally cannot.
		payloadJSON, err := json.Marshal(protocol.ConversationUpdatedPayload{
			ID:         string(cv.ID),
			IsPromoted: cv.IsPromoted,
			IsArchived: cv.IsArchived,
			IsMuted:    cv.IsMuted,
			Name:       cv.Name,
			Cwd:        cv.Cwd,
			// The workspace's label, not the conversation's prompt: this is a
			// value the requester's client can already read off any
			// list_conversations row, which is exactly what the withheld prompt
			// is not.
			WorkspaceLabel: workspaceLabelFor(reg, cv.Cwd),
			LastUsedAt:     cv.LastUsedAt,
		})
		if err != nil {
			return fmt.Errorf("marshal conversation_updated payload: %w", err)
		}

		logger.Info("relay: set_system_prompt applied",
			"event", "set_system_prompt.applied",
			"conn_id", c.ConnID())
		return c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)
	}
}
