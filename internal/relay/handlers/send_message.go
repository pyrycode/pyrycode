package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgSendMessageMalformed is the user-facing message emitted in the
// protocol.malformed error payload when SendMessagePayload cannot be
// JSON-decoded. The decode-error text is NOT echoed back (it could
// reflect attacker-controlled payload bytes); only this static string.
const msgSendMessageMalformed = "malformed send_message payload"

// msgConversationNotFound is the user-facing message emitted in the
// conversation.not_found error payload when the supervisor's
// ValidateConversation refuses the inbound conversation_id.
const msgConversationNotFound = "conversation not found"

// msgServerBinaryOffline is the user-facing message emitted in the
// server.binary_offline error payload when the conversation exists but has no
// live bound session at enqueue time (empty CurrentSessionID, or the bound id
// is no longer in the pool). Retryable: the phone re-issues once the session is
// (re)bound.
const msgServerBinaryOffline = "server binary offline"

// msgSendMessageBacklogFull is the user-facing message emitted in the
// server.binary_busy error payload when the conversation's inbound backlog is at
// its per-conversation cap (msgqueue.Enqueue returned 0). Retryable: the phone
// re-issues once the backlog drains. Static string only — never echoes
// payload.Text (untrusted, phone-controlled) — mirroring register_push_token.go's
// msgBinaryBusy.
const msgSendMessageBacklogFull = "server busy; message backlog full, retry"

// msgSendMessageTooManyAttachments is the user-facing message emitted in the
// protocol.malformed error payload when attachment_ids carries more than
// protocol.MaxAttachmentIDsPerMessage elements. It names the bound (a PUBLISHED
// producer-side contract a client already knows) and nothing about the frame's
// own ids.
const msgSendMessageTooManyAttachments = "too many attachment ids; at most 32 per message"

// msgSendMessageAttachmentNotFound is the user-facing message emitted in the
// attachment.not_found error payload when a named attachment id does not resolve
// under this message's own conversation.
//
// STATIC, and it MUST NOT say which of the named ids failed. A per-id answer
// turns one send_message into a batch existence-probe for up to
// protocol.MaxAttachmentIDsPerMessage ids and rebuilds the path-existence oracle
// that code's deliberate merge exists to prevent (docs/protocol-mobile.md
// § Error codes). It never echoes an id, a count, or a resolved path.
const msgSendMessageAttachmentNotFound = "attachment not found"

// attachmentPromptHeader introduces the daemon-authored block naming a message's
// attachments. It is appended AFTER the user's own text so a message naming none
// is an identity — see composeAttachmentPrompt.
const attachmentPromptHeader = "Attached files (use the Read tool to view each):"

// clearCommand is the ONE message text the daemon does not deliver (#2456): a
// send_message whose first whitespace-delimited token is exactly this runs the
// conversation reset instead of reaching claude. One fixed token, matched
// case-sensitively and in first position only — see isClearCommand for why it must
// stay that and nothing broader.
const clearCommand = "/clear"

// TurnWriter is the minimal per-conversation write-surface that the inbound
// delivery seam drives. *sessions.Session satisfies it via one-line
// passthroughs to Session.Activate and Supervisor.WriteUserTurn. The interface
// lives in this package so handlers/ stays free of internal/sessions and
// internal/supervisor imports.
//
// Since #721 the send_message handler no longer drives a TurnWriter directly —
// it enqueues, and the daemon's msgqueue drain (cmd/pyry.newInboundDeliver)
// resolves and writes one message at a time. SessionRouter.Route still returns
// a TurnWriter so the handler can validate the binding before enqueue.
//
// Activate is called before WriteUserTurn so an idle-evicted bootstrap
// session lazily respawns claude on the next delivery rather than dropping it
// silently (#396). On an already-active session Activate is a near-no-op (two
// non-blocking channel receives).
type TurnWriter interface {
	Activate(ctx context.Context) error
	WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
}

// Enqueuer is the inbound backlog the send_message handler appends to instead
// of delivering synchronously. *msgqueue.Queue satisfies it. The interface is
// defined here, consumer-side, so handlers/ stays free of an internal/msgqueue
// import (mirrors SessionRouter and TurnWriter). Enqueue is non-blocking and
// returns the stable per-conversation id assigned to the message (>= 1), or 0 if
// the conversation's backlog is at capacity and the message was rejected (reject,
// never drop) — the handler maps a 0 to a retryable "backlog full" reply.
//
// Since #2038 the method is EnqueueDelivery rather than Enqueue: a message
// naming attachments is DELIVERED as a composed prompt naming their on-host paths
// while the text a client reads back stays the user's own words. Passing both
// halves here — rather than widening what is queued — is what keeps a host path
// off the wire on both queue_state arms without any consumer having to remember
// to strip it.
// Since #2092 it also takes the client's own messageID, relayed verbatim onto the
// queued record so a client can merge the queued row with the optimistic echo it
// already drew. The handler neither validates, normalises nor mints it — an empty
// id is legal and stays empty.
type Enqueuer interface {
	EnqueueDelivery(conversationID, messageID, text, delivery string) uint64
}

// AttachmentResolver resolves one attachment id named by a send_message to the
// on-host path of its stored file, confined to conversationID. *The* production
// implementation adapts attachments.ResolvePath; the interface lives here,
// consumer-side, so handlers/ stays free of an internal/attachments import
// (mirrors SessionRouter and Enqueuer).
//
// conversationID MUST be an id the caller has already validated against the
// registry binding — ResolvePath's precondition, which it cannot check itself and
// which carries the whole confinement property. SendMessage discharges it by
// calling SessionRouter.Route first, and only then resolving against that same
// id; see the ordering note there.
//
// COMMA-OK RATHER THAN AN ERROR, deliberately, and for a logging reason rather
// than an aesthetic one. ResolvePath answers exactly one sentinel for an unknown
// id, a non-canonical id and an id stored under a different conversation alike,
// so an error carries nothing a caller could branch on — but its shape-invalid
// refusal formats the RAW client-supplied id into its message, which is a
// log-injection value docs/protocol-mobile.md § Attachments forbids logging. The
// adapter is the only scope that holds that error, and it discards it, so the
// handler cannot log what it never receives.
//
// A nil AttachmentResolver refuses every id: a message naming attachments is
// answered attachment.not_found, and one naming none is unaffected. Fail-closed,
// so an unwired seam can never deliver a message with its attachments silently
// dropped.
type AttachmentResolver func(conversationID, attachmentID string) (path string, ok bool)

// SessionRouter resolves a send_message frame's conversation id to the write
// surface for that conversation's bound claude session (#678). Resolution is a
// pure in-memory lookup (no I/O, non-blocking) — the blocking activation
// happens in the returned TurnWriter's Activate, under the handler's existing
// budget. The interface lives in this package, returning a TurnWriter rather
// than any internal/sessions type, so handlers/ stays free of an
// internal/sessions import (mirrors SessionCreator in create_conversation.go).
//
// Failure mapping the handler relies on:
//   - unknown conversation → conversations.ErrConversationNotFound
//     (maps to conversation.not_found, not retryable)
//   - conversation has no bound session, or the bound session id is not in the
//     pool → any other non-nil error (maps to retryable server.binary_offline)
type SessionRouter interface {
	Route(conversationID string) (TurnWriter, error)
}

// ConversationResetter starts the conversation reset an intercepted "/clear"
// runs (#2456): the outgoing child's wrap-up turn, its handoff note, and the
// rotation onto a fresh session. cmd/pyry's activeSessionStarter is the
// production implementation, and it is the SAME value an inbound new_session
// reaches through relay.SessionStarter — which is the whole point of this seam.
// The two verbs give the same reset because they call one entry point, not
// because two implementations agree.
//
// Named for the role this handler puts it to (the package convention that gives
// us SessionRouter and ConversationToucher) while keeping the method name of the
// sealed surface — the same split relay.SessionStarter's own doc argues for. That
// name difference costs nothing at the wiring site: handlers/ imports neither
// internal/relay nor internal/sessions, and an interface value is assignable to
// any interface its method set covers, so cmd/pyry passes its existing
// relay.SessionStarter-typed field with no adapter (as it already does for
// ConversationToucher over the registry).
//
// THE PLAIN FORM, NOT relay.LateSessionStarter, and deliberately so. #2443's
// new_session.workspace_refused reply is the late form's entire reason, and it is
// correlated by in_reply_to against a new_session frame; answering a send_message
// with it would misreport which frame it describes. A "/clear" whose rotation
// cannot re-enter the conversation's recorded workspace therefore gets no error
// frame at all — see the intercept in SendMessage.
//
// conversationID MUST already be registry-validated by the caller. SendMessage
// discharges that by intercepting only BELOW SessionRouter.Route, so the id
// crossing this seam is a known registry key with a live binding — stricter than
// the raw client string relay's own new_session path hands the same entry point.
//
// Every outcome is BEST-EFFORT from this handler's side: an inert arm, a reset
// already in progress, and a failed rotation all mean the message is gone and the
// client has its ack, so there is nothing to roll back and no reply is owed. That
// is handleNewSession's posture for the same seam, and SendMessage keeps it.
type ConversationResetter interface {
	StartNewSession(conversationID string) error
}

// ConversationToucher is the registry write surface the last-used bump consumes
// (#2438): one locked mutation, plus the eager persist that follows it.
//
// Deliberately NARROWER than ConversationAutoNamer rather than a reuse of it,
// even though the same value satisfies both and SendMessage hands its one
// registry parameter to each step — an interface value is assignable to any
// interface its method set covers, so the narrowing costs no adapter and no
// constructor argument. The separate name is what keeps the auto-namer's stated
// contract honest: that interface promises it writes Name only over a nil one,
// and a step writing LastUsedAt on every accepted message must not quietly
// arrive under that promise.
//
// Update's callback contract is the registry's own (ADR 022): it runs holding the
// registry mutex, so it must not call back into the registry, and it must not
// retain the *Conversation past return.
type ConversationToucher interface {
	Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool
	Save(path string) error
}

// SendMessage returns a dispatch.Handler that processes a send_message frame
// from the phone. router validates the frame's ConversationID against its bound
// claude session (#678) and stamps the active-conversation cursor (#687);
// queue is the daemon's inbound backlog the message is appended to; logger is
// the daemon's slog logger used for every branch's structured event.
//
// Since #721 the handler acks on ENQUEUE (accepted into the backlog), not on
// delivery-confirm. It validates the binding synchronously, enqueues
// non-blocking, and returns an ack; the daemon's msgqueue drain delivers the
// backlog one message at a time through the reliable WriteUserTurn path, paced
// by claude reaching idle (#704). This is the ADR 025 line 123 contract —
// send_message is unchanged at the wire level, queued by the daemon when claude
// is busy.
//
// The ack contract (which failures still produce an error reply vs are absorbed
// by the drain) is asymmetric by design:
//   - At enqueue we have a live phone to tell "retry", so a malformed payload, an
//     unknown conversation, and an unbound conversation are all rejected
//     synchronously, before any enqueue; a full backlog is rejected at the
//     enqueue point (Enqueue returns 0) with the same retryable shape.
//   - Once enqueued, the ack promised delivery, so a transient resolve/activate/
//     write failure is held and retried by the drain rather than surfaced (a
//     conversation that becomes unbound post-ack is retried, not dropped).
//
// SECURITY:
//   - payload.Text is treated as opaque transit content: it is stored in the
//     in-memory FIFO and reaches the supervised claude child's stdin verbatim
//     only at the drain's WriteUserTurn call. No transformation, no length cap
//     beyond the transport's WS read ceiling (1 MiB; see internal/transport).
//   - payload.Text is NEVER logged at any level. conversation_id and message_id
//     (phone-supplied opaque ids) plus the assigned queued_msg_id are logged on
//     the enqueue path; the binding-reject paths log conversation_id, and the
//     backlog-full reject logs conversation_id + message_id (no queued_msg_id —
//     nothing was queued).
//   - The phone supplies only the ConversationID lookup key and the Text; the
//     routing target (the bound session) is read from the server-stored registry
//     row, never phone-writable. An unbound conversation is rejected before
//     enqueue, so a turn is never silently routed to the shared bootstrap
//     session (#678 AC#4).
//   - Since #2159 a prefix of payload.Text can become the conversation's stored
//     display name, so it crosses into persisted state and onto a broadcast
//     frame. That is not a new class of value in that field — rename_conversation
//     already stores an arbitrary remote string as the name, unbounded — and the
//     derived title is bounded at 41 runes, so this path admits strictly less
//     than the field already did. It is still never logged: see
//     autoNameConversation, which explains why the title counts as the same
//     untrusted content the text is.
//
// Since #2159 it also takes the auto-naming seams: reg is the conversations
// registry (nil means no registry leg, which names nothing), registryPath is the
// canonical on-disk path passed to the eager Save, and announce fans the renamed
// row to every interactive client and may be nil. See autoNameConversation.
//
// Since #2438 reg and registryPath carry a second registry write as well: an
// accepted message stamps its conversation's LastUsedAt so the 30-day idle sweep
// spares a conversation in use. A nil registry therefore now names nothing AND
// stamps nothing. See touchConversation.
// Since #2456 it also takes reset, the conversation-reset seam an intercepted
// "/clear" runs instead of enqueuing. See the intercept below and
// ConversationResetter.
func SendMessage(router SessionRouter, queue Enqueuer, resolve AttachmentResolver, reg ConversationAutoNamer, registryPath string, announce ConversationAnnouncer, reset ConversationResetter, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.SendMessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: send_message malformed payload",
				"event", "send_message.malformed",
				"conn_id", c.ConnID(),
				"err", err)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgSendMessageMalformed, false)
		}

		// The published 32-id bound, enforced here for the first time (#2038):
		// internal/protocol declares it with no validator, and both it and
		// docs/protocol-mobile.md § Naming a message's attachments name whatever
		// first counts the list as the owner of this decision.
		//
		// REFUSE, never truncate. Truncating drops files a person attached and
		// gives the client no signal it happened, so the message ships looking
		// complete — the same reason the backlog cap rejects rather than evicting.
		// protocol.malformed rather than a minted code: 32 is a producer-side
		// contract a client knows BEFORE it sends, so a frame naming 33 is not a
		// conforming send_message, exactly as an ill-typed attachment_ids is not,
		// and that already answers this code one branch up. Not retryable —
		// resending the same frame reproduces it.
		//
		// COUNTED ON THE RAW ELEMENTS, before the dedup below, because the bound
		// is published as counting elements and not distinct ids. Counting after
		// the dedup would silently contradict that.
		//
		// Placed BEFORE Route: this is a pure frame-shape rule needing no state,
		// and Route has a side effect (it stamps the active-conversation cursor),
		// so a frame violating a published contract moves no daemon state and
		// learns nothing about whether the conversation it named exists.
		if len(p.AttachmentIDs) > protocol.MaxAttachmentIDsPerMessage {
			logger.Warn("relay: send_message names too many attachments",
				"event", "send_message.too_many_attachments",
				"conn_id", c.ConnID(),
				"attachment_count", len(p.AttachmentIDs))
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgSendMessageTooManyAttachments, false)
		}

		// Validate the conversation's binding BEFORE enqueue, and let Route stamp
		// the active-conversation cursor — this IS the phone-interaction moment
		// (#687), the same as the synchronous handler. The returned writer is
		// discarded: the drain re-resolves at delivery time because the binding
		// may change between enqueue and drain. An unbound conversation is
		// rejected here, never enqueued, so a turn is never routed to the shared
		// bootstrap session (#678 AC#4).
		if _, routeErr := router.Route(p.ConversationID); routeErr != nil {
			switch {
			case errors.Is(routeErr, conversations.ErrConversationNotFound):
				logger.Warn("relay: send_message unknown conversation",
					"event", "send_message.unknown_conversation",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID)
				return replyError(ctx, c, env, protocol.CodeConversationNotFound, msgConversationNotFound, false)
			default:
				// The conversation exists but has no live bound session (empty
				// CurrentSessionID, or the bound id is no longer in the pool).
				// Retryable so the phone re-issues after the session is (re)bound;
				// never falls through to the bootstrap — Route rejects an empty
				// binding before any Lookup (#678 AC#4).
				logger.Warn("relay: send_message no bound session",
					"event", "send_message.no_bound_session",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID,
					"err", routeErr)
				return replyError(ctx, c, env, protocol.CodeServerBinaryOffline, msgServerBinaryOffline, true)
			}
		}

		// THE ONE INTERCEPTED LITERAL (#2456). A "/clear" is not a message: it runs
		// the conversation reset — the wrap-up turn, the handoff note and the
		// rotation — instead of reaching claude, so the verb a client types and the
		// New session control it presses give the same thing. claude's own in-place
		// clear is no longer reachable from a client, which is the decision this
		// implements rather than a side effect of it.
		//
		// ITS POSITION IS THE DESIGN, on all three sides:
		//   - BELOW Route, so the id crossing the seam is registry-validated with a
		//     live binding and the follow-active cursor is stamped. That is also what
		//     leaves the unknown/unbound answers exactly as they were: those frames
		//     are rejected above and never reach the match.
		//   - ABOVE resolveAttachments, so a /clear's attachment ids are DROPPED —
		//     none is resolved, so no attachment.not_found can be answered for one and
		//     no directory is read on its behalf.
		//   - ABOVE the enqueue, so nothing is queued and no queue_state item is
		//     published for it. Intercepting after the enqueue would instead put the
		//     reset behind every message already in the backlog.
		// It also sits above touchConversation and autoNameConversation, so a /clear
		// never becomes the conversation's auto-name and stamps no LastUsedAt — the
		// new_session frame driving this same reset stamps nothing either, and the two
		// routes are meant to be one behaviour.
		//
		// The 32-id bound deliberately stays above Route: a /clear naming 33
		// attachment ids is still a non-conforming send_message.
		//
		// NOT INTERACTIVE-GATED, unlike the new_session that reaches the same entry
		// point. dispatch.Conn carries no such flag, and plumbing one here would be
		// new machinery for no authorization gain: pairing is the authorization
		// boundary, and docs/protocol-mobile.md § New session already publishes that
		// this verb is exempt from the per-device permission gate (#702). A
		// non-interactive client's /clear resets and simply does not see the
		// `resetting` frames it declined.
		if isClearCommand(p.Text) {
			// A count of nothing: the text is not logged here any more than anywhere
			// else on this handler, and the ids are not resolved, so there is nothing
			// to count. conversation_id is a validated registry key by this point.
			logger.Info("relay: send_message intercepted /clear; running the conversation reset",
				"event", "send_message.clear_intercepted",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"message_id", p.MessageID)

			// THE ACK GOES OUT FIRST, for the reason touchConversation records for its
			// own step: only the wrap-up arm of the reset moves itself onto another
			// goroutine, and every other arm — an inert conversation, a reset already
			// running, a synchronous rotation — resolves inline on THIS goroutine. A
			// reset started ahead of the ack would put that work between the frame and
			// its reply.
			ackErr := replyAck(ctx, c, env)

			switch {
			case reset == nil:
				// FAIL-CLOSED MEANS DROPPING IT, not delivering it. An unwired seam
				// (foreground / v1) must not hand the text to claude, because claude's
				// in-place clear being unreachable is the property this intercept
				// exists to establish — restoring it on the path where nothing is
				// wired would be the one regression it cannot tolerate.
				logger.Debug("relay: send_message dropped /clear; no conversation resetter wired",
					"event", "send_message.clear_no_resetter",
					"conn_id", c.ConnID(),
					"conversation_id", p.ConversationID)
			default:
				// BEST-EFFORT, exactly as handleNewSession treats the same seam. An
				// inert conversation, a reset already in progress and a rotation that
				// failed are indistinguishable from here and all mean the same thing:
				// the message is gone and the client has its ack, so there is nothing
				// to roll back and no reply is owed.
				//
				// The error is recorded verbatim, which is safe by construction rather
				// than by trust: RotatedWithoutWorkspaceError.Error() is a constant by
				// design, and the confinement error that produced a refusal is
				// discarded at its own site, so no phone-influenced workspace path can
				// reach this line.
				//
				// A *RotatedWithoutWorkspaceError is NOT answered to the client here.
				// That reply is correlated by in_reply_to against a new_session frame,
				// so answering a send_message with it would misreport which frame it
				// describes — which is also why this seam is the plain form and not
				// the late one.
				if err := reset.StartNewSession(p.ConversationID); err != nil {
					logger.Warn("relay: send_message /clear did not reset the conversation",
						"event", "send_message.clear_reset_failed",
						"conn_id", c.ConnID(),
						"conversation_id", p.ConversationID,
						"err", err)
				}
			}
			return ackErr
		}

		// Resolve the named attachments AFTER Route, never before, and against the
		// SAME p.ConversationID Route just validated — that ordering is what
		// discharges attachments.ResolvePath's precondition, which requires a
		// conversation the authenticated session is already on and which the
		// resolver cannot check for itself ("a caller that gets it wrong defeats
		// every check below"). Confinement then needs no test of its own: an
		// attachment is filed under whatever conversation the daemon's cursor named
		// when its last chunk landed, so an id from another conversation simply has
		// no directory beneath this one.
		//
		// One id failing refuses the WHOLE message, so a partially-attached turn
		// never reaches claude. Nothing is enqueued.
		paths, ok := resolveAttachments(resolve, p.ConversationID, p.AttachmentIDs)
		if !ok {
			// attachment_count only — never an id. The failing one may be
			// shape-invalid by definition, and even a valid one would rebuild by
			// log the oracle the static reply refuses to disclose.
			logger.Warn("relay: send_message names an unresolvable attachment",
				"event", "send_message.attachment_not_found",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"message_id", p.MessageID,
				"attachment_count", len(p.AttachmentIDs))
			return replyError(ctx, c, env, protocol.CodeAttachmentNotFound, msgSendMessageAttachmentNotFound, false)
		}

		// Enqueue is non-blocking: it appends to the conversation's in-memory FIFO
		// and returns the stable id immediately. The ack now means "accepted into
		// the backlog", not "delivered/committed". payload.Text is NEVER logged,
		// and neither is the composed prompt — it carries host paths.
		//
		// The two halves are what keeps #2038 AC 4 true: p.Text is what queue_state
		// reports back on both the enqueue push and the connect-time reconcile,
		// while the composed prompt goes only to claude.
		//
		// p.MessageID rides along verbatim (#2092) — the client's own id for this
		// message, which queue_state names on the item so the client can merge it
		// with its optimistic echo instead of drawing the message twice. It is
		// passed exactly as it arrived: nothing here validates, trims, normalises
		// or substitutes it, and "" stays "".
		id := queue.EnqueueDelivery(p.ConversationID, p.MessageID, p.Text, composeAttachmentPrompt(p.Text, paths))
		if id == 0 {
			// The conversation's backlog is at its per-conversation cap (#869).
			// Reject, never drop: nothing was enqueued and the existing backlog is
			// untouched. Surface a retryable server-busy so the phone re-issues
			// after the backlog drains. Log conversation_id + message_id only —
			// never payload.Text, and there is no queued_msg_id (nothing queued).
			logger.Warn("relay: send_message backlog full",
				"event", "send_message.backlog_full",
				"conn_id", c.ConnID(),
				"conversation_id", p.ConversationID,
				"message_id", p.MessageID)
			return replyError(ctx, c, env, protocol.CodeServerBinaryBusy, msgSendMessageBacklogFull, true)
		}
		logger.Info("relay: send_message enqueued",
			"event", "send_message.enqueued",
			"conn_id", c.ConnID(),
			"conversation_id", p.ConversationID,
			"message_id", p.MessageID,
			"queued_msg_id", id,
			// A count, never an id and never a path: how many attachments the
			// prompt names is operationally useful and discloses nothing.
			"attachment_count", len(paths))

		// THE ACK GOES OUT FIRST, and the auto-naming follows it (#2159). Acceptance
		// is established by the enqueue above, not by anything below: the message is
		// queued and the drain already owns it, so everything that follows is a side
		// effect on registry metadata. Making the sender's round-trip wait on an
		// fsync and on a broadcast aimed at OTHER clients would couple the hot path
		// to work the sender does not need — and measurably so, at the millisecond
		// scale a queued turn's delivery races in.
		//
		// The ack's error is carried past the naming rather than returned before it.
		// A conn whose ack write failed is a conn on its way out, and the message it
		// queued is still going to run, so its conversation still deserves its name.
		ackErr := replyAck(ctx, c, env)

		// The conversation was just used, so stamp it and keep the 30-day idle
		// sweep off it (#2438). Ahead of the naming step, so the record that step
		// announces carries the instant written here — see touchConversation.
		touchConversation(reg, registryPath, logger, c.ConnID(), p.ConversationID)

		// Auto-name the conversation from this message, if it is the first one and
		// the row is still unnamed. Reached only on an accepted message: every reject
		// branch above has already returned, which is what makes "a rejected send
		// writes no name" structural rather than a guard.
		//
		// p.Text, never the composed prompt: the prompt names on-host paths.
		//
		// It reports nothing and can fail nothing — see autoNameConversation.
		autoNameConversation(reg, registryPath, announce, logger, c.ConnID(), p.ConversationID, p.Text)

		return ackErr
	}
}

// touchConversation stamps a conversation's LastUsedAt with the instant its
// message was accepted, and persists it (#2438).
//
// Conversation.LastUsedAt is documented as bumped on user activity, and
// conversations.ShouldArchive reads it to decide — at 30 days idle — that nobody
// wants the conversation any more. Until this existed the only writers were the
// two creation stamps, so a conversation used every day was swept 30 days after
// it was CREATED. Sending a message is the one unambiguous use, which is why the
// bump lives here and why the sibling verbs that carry the field through —
// rename, promote, archive, set-system-prompt, change-workspace — still do.
// Those edit a conversation; this one uses it.
//
// ON ACCEPTANCE, NOT ON ROUTING. It is called after a non-zero EnqueueDelivery,
// which puts it past the last branch that can still refuse the message: an
// unresolvable attachment and a full backlog both pass Route and are rejected
// anyway. "A rejected send is not a use" is therefore structural, exactly as the
// naming step's equivalent rule is, rather than a guard someone must remember.
//
// It runs AFTER THE ACK and BEFORE the naming step. After the ack for the reason
// autoNameConversation gives at length: the step costs an fsync, and the drain is
// already delivering the turn on its own goroutine, so work done ahead of the ack
// lets the child's frames overtake it on the wire. Before the naming step so the
// conversation_updated record that step snapshots carries the timestamp just
// written rather than one that is stale the instant it is announced.
//
// It reports nothing and fails nothing. The sender has already been told the
// message was accepted, and no outcome here may change that.
func touchConversation(reg ConversationToucher, registryPath string, logger *slog.Logger, connID, conversationID string) {
	// A nil registry means "no registry leg wired" and stamps nothing, the same
	// fail-closed shape the naming step takes.
	if reg == nil {
		return
	}
	if !reg.Update(conversations.ConversationID(conversationID), func(cv *conversations.Conversation) {
		cv.LastUsedAt = time.Now().UTC()
	}) {
		// The row was deleted between the enqueue and here. Nothing was written, so
		// there is nothing to persist — and saving regardless could only produce a
		// spurious failure line about a write nobody asked for.
		return
	}

	// Eager persist, so the stamp survives a daemon restart: the registry is
	// reloaded from this file at startup, and a stale instant there is precisely
	// the bug this fixes. The sweep loop's own Save is lazy (it writes only on a
	// tick that archived something), so it cannot be relied on to carry this.
	//
	// WARN, and not the Error the naming step logs for its own failed Save. The
	// difference is what a miss costs: this value is self-healing — the in-memory
	// stamp is what the running daemon's sweep reads, and the next accepted message
	// re-stamps and re-persists — so a failure here is a degraded write rather than
	// a lost one. There is also no success line to pair a level with, because one
	// per accepted message would double this handler's log volume to tell an
	// operator nothing actionable. This is the only line the step emits.
	if err := reg.Save(registryPath); err != nil {
		logger.Warn("relay: send_message last-used persist failed",
			"event", "send_message.last_used_persist_failed",
			"conn_id", connID,
			"conversation_id", conversationID,
			"err", err)
	}
}

// isClearCommand reports whether text's first whitespace-delimited token is
// exactly clearCommand — the one literal SendMessage intercepts (#2456).
//
// EXACT, CASE-SENSITIVE, FIRST TOKEN ONLY: never a prefix, never a table, never a
// case-insensitive compare. docs/protocol-mobile.md's stance that inbound message
// text is never a command vocabulary takes exactly one published exception here,
// and keeping it to one fixed token is what keeps that statement true — which is
// why the test table pins /compact, /model, /clearcache, /CLEAR and a /clear that
// is not the first token as ordinary messages.
//
// DELIBERATELY NOT strings.Fields. This predicate runs on EVERY send_message, and
// payload.Text is bounded only by the transport's 1 MiB WS read ceiling, so a
// field split would let a remote client buy one string header per token — some
// hundreds of thousands of allocations on a text of whitespace — for an answer
// that only ever needs the first token. TrimSpace returns a subslice rather than
// a copy, so the form below allocates nothing and is one linear scan strictly
// cheaper than the json.Unmarshal that already ran over the same bytes.
//
// The boundary check is what separates the command from a longer command sharing
// its spelling: "/clearcache" continues with 'c', not a space, so it is a message.
// unicode.IsSpace on both ends, matching what TrimSpace itself trims, so "a token"
// means one thing in both halves of the predicate.
func isClearCommand(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, clearCommand) {
		return false
	}
	rest := trimmed[len(clearCommand):]
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return unicode.IsSpace(r)
}

// resolveAttachments maps a message's named attachment ids to their on-host
// paths, confined to conversationID. It reports false if ANY named id does not
// resolve, having produced no paths — one refusal fails the whole message, so a
// turn is never delivered with some of its attachments silently missing.
//
// It DEDUPLICATES on first occurrence (#2038): a repeated id is resolved once and
// its path named once, at the position the client first listed it. Refusing a
// repeat would punish a client for something harmless, and naming a path twice
// would tell claude to read one file twice. Deduplicating also bounds the work to
// at most protocol.MaxAttachmentIDsPerMessage directory reads however the list is
// composed — but note the bound itself is counted by the CALLER on the raw
// elements, before this runs, because it is published as counting elements.
//
// The ids are compared as raw bytes. That is a pure equality against a map key —
// neither a path nor a log line — and it is deliberately not a normalising
// compare, which could merge two ids the resolver's own canonical-shape check
// treats differently.
//
// A nil resolve refuses every id, so an unwired seam fails closed. An empty list
// yields no paths and true, which is the identity case the composer relies on.
func resolveAttachments(resolve AttachmentResolver, conversationID string, ids []string) ([]string, bool) {
	if len(ids) == 0 {
		return nil, true
	}
	if resolve == nil {
		return nil, false
	}
	seen := make(map[string]struct{}, len(ids))
	paths := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		path, ok := resolve(conversationID, id)
		if !ok {
			return nil, false
		}
		paths = append(paths, path)
	}
	return paths, true
}

// composeAttachmentPrompt builds what claude receives: the user's own text,
// followed by a daemon-authored block naming each attachment's on-host path and
// directing claude to read them. The daemon puts the PATH in the prompt and
// claude opens it with its ordinary Read tool — the decision on record since
// 2026-05-16, taken because inlining the bytes would duplicate them into the
// JSONL transcript that the on-disk copy already holds.
//
// AN EMPTY PATH LIST IS THE IDENTITY. A message naming no attachments reaches
// claude byte-for-byte as it did before this function existed, which is why the
// empty case is an early return rather than a formatting rule that happens to
// produce the same bytes.
//
// The user's text is placed first and VERBATIM — never quoted, wrapped or
// escaped. Quoting would mangle a text containing the quote character and buys
// nothing, since the client already controls these bytes in full. The block goes
// last so the daemon's instruction is the most recent thing claude reads.
//
// One path per line, and no delimiter a path could contain. That holds because
// the only client-authored bytes in a resolved path are attachments.
// SanitizeFilename's output, whose allowlist is [a-zA-Z0-9_.-] and which
// guarantees exactly one path component carrying no '/' and no control
// character: a filename can neither add a line to this block nor escape the
// attachment directory in the rendered text.
func composeAttachmentPrompt(text string, paths []string) string {
	if len(paths) == 0 {
		return text
	}
	block := attachmentPromptHeader + "\n" + strings.Join(paths, "\n")
	if text == "" {
		return block
	}
	return text + "\n\n" + block
}
