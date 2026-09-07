package handlers

import (
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// maxAutoNameRunes bounds the text a derived title RETAINS, in runes and not in
// bytes (#2159). A truncated title is this many runes plus the one-rune ellipsis
// below, so 41 runes reach the wire at most — at 4 bytes per rune, three orders
// of magnitude under the application envelope's 65519-byte cap.
//
// Runes rather than bytes because this value is displayed, not stored in a
// fixed-width field: 40 CJK characters is a sensible sidebar title and 40 bytes
// of them is thirteen. A byte cut could also split a rune and put U+FFFD in a
// display name.
const maxAutoNameRunes = 40

// autoNameEllipsis marks a title that dropped something. One rune (U+2026), not
// three periods, so the marker costs one against the width a client renders.
const autoNameEllipsis = "…"

// ConversationAutoNamer is the minimal registry write surface the auto-naming
// step consumes. *conversations.Registry satisfies it structurally; no adapter
// required.
//
// Method-set-identical to ConversationRenamer and still declared separately,
// matching this package's one-narrow-interface-per-handler convention
// (ConversationPromoter, ConversationArchiver and ConversationDeleter already
// overlap each other). The difference that earns the second name is the contract
// stated here rather than the methods: this consumer writes Name ONLY over a nil
// one, which the rename interface must not claim.
//
// WorkspaceLabel supplies the pushed record's workspace_label (#2210) and MUST
// NOT be called from inside the Update callback: Update holds the registry's
// mutex for the callback's duration and this method takes that same
// non-reentrant lock, so a read there parks the goroutine forever holding the
// registry lock and stalls every registry consumer — a denial of service
// reachable by one ordinary send_message. autoNameConversation captures the cwd
// in the callback and reads the label after Update returns.
type ConversationAutoNamer interface {
	Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool
	Save(path string) error
	WorkspaceLabel(cwd string) (string, bool)
}

// ConversationAnnouncer fans a conversation_updated record, unsolicited, to
// every interactive-capable connected client. It is supplied by cmd/pyry, where
// #2156's emitter closes over the v2 session manager; this package cannot name
// that manager, so it takes this func instead. Its signature matches that
// emitter's announce exactly, so the wiring needs no adapting closure beyond a
// nil guard.
//
// IT TAKES NO excludeConnID, unlike WorkspaceAnnouncer, and the difference is
// what the requester already holds. rename_workspace answers its requester with
// the very record it broadcasts, so pushing it again would be a duplicate; a
// send_message is answered with an ack carrying no record at all, so excluding
// the sender would starve the one client that caused the change.
//
// It RETURNS NOTHING, which is the contract rather than an omission: nothing
// about the fan-out may fail the message the operator sent. A nil
// ConversationAnnouncer is valid and means "no fan-out" — the shape a caller
// with no relay leg takes.
type ConversationAnnouncer func(p protocol.ConversationUpdatedPayload)

// deriveConversationName cuts a short display title from a message's text
// (#2159). Pure and total: no I/O, no registry, no clock.
//
// The contract, in order:
//
//  1. Normalise — every run of whitespace, newlines included, collapses to one
//     space, and the ends are trimmed. strings.Fields splits on unicode.IsSpace,
//     which is exactly that rule.
//  2. A normalisation that is empty yields ("", false). An attachment-only
//     message is a real message and simply names nothing.
//  3. Within maxAutoNameRunes the normalisation IS the title, verbatim, with no
//     ellipsis. Exactly at the bound is inclusive — nothing was dropped.
//  4. Over it, whole words are taken while the accumulation stays within the
//     bound, and the ellipsis is appended. A first word that alone exceeds the
//     bound is cut mid-word at exactly the bound.
//
// The ellipsis is appended AFTER the cut, so the bound describes the retained
// text rather than the rendered string. That is why (3) is its own arm and not a
// formatting rule that happens to agree: the ellipsis marks a drop, so a title
// that dropped nothing must not carry one.
//
// The caller passes the message's own text and never composeAttachmentPrompt's
// output — that string names on-host paths, and a title cut from it would put a
// host filesystem path into a display name pushed to every paired client.
func deriveConversationName(text string) (string, bool) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return "", false
	}
	normalized := strings.Join(words, " ")
	if utf8.RuneCountInString(normalized) <= maxAutoNameRunes {
		return normalized, true
	}

	// Over the bound. Take whole words while the accumulation — the words plus
	// the single spaces joining them — stays within it.
	var b strings.Builder
	kept := 0
	for _, w := range words {
		next := kept + utf8.RuneCountInString(w)
		if kept > 0 {
			next++ // the joining space
		}
		if next > maxAutoNameRunes {
			break
		}
		if kept > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(w)
		kept = next
	}
	if kept == 0 {
		// The first word alone exceeds the bound, so no whole word fits. Cut that
		// word at the bound, on RUNE boundaries — a byte slice here would split a
		// multi-byte rune and emit U+FFFD.
		return string([]rune(normalized)[:maxAutoNameRunes]) + autoNameEllipsis, true
	}
	return b.String() + autoNameEllipsis, true
}

// autoNameConversation gives an as-yet-unnamed conversation a title cut from the
// message just accepted for it, persists it, and pushes the updated row to every
// interactive-capable client (#2159). Called by SendMessage after a non-zero
// EnqueueDelivery and before the ack, so only an ACCEPTED message names a
// conversation: every reject branch returns before this point, which is what
// makes "a rejected send writes no name" structural rather than a guard.
//
// It reports nothing and fails nothing. Auto-naming is a side effect of a
// message that was already accepted, so no outcome here may change the ack the
// caller is about to send.
//
// The ordering is load-bearing at two points:
//
//   - The nil-name check and the write are ONE locked mutation. A concurrent
//     rename_conversation therefore either lands entirely before this (the
//     callback sees a non-nil Name and declines) or entirely after (the
//     operator's explicit rename overwrites the derived title, which is the
//     precedence we want). There is no interleaving in which both write.
//   - WorkspaceLabel is read AFTER Update returns — see ConversationAutoNamer
//     for why calling it in the callback deadlocks the daemon.
//
// The title is captured in a local and the payload snapshotted inside the same
// callback. Neither the *Conversation nor a pointer into it is retained: the
// registry's slice may be reallocated by a later Create.
func autoNameConversation(reg ConversationAutoNamer, registryPath string, announce ConversationAnnouncer, logger *slog.Logger, connID, conversationID, text string) {
	// A nil registry means "no registry leg wired" and names nothing. Fail-closed
	// rather than fail-open: an unwired seam cannot half-name a conversation.
	if reg == nil {
		return
	}
	title, ok := deriveConversationName(text)
	if !ok {
		return // normalises to empty — an attachment-only message names nothing
	}

	// named distinguishes "the callback ran and wrote" from "the callback ran and
	// declined", which Update's own bool cannot: it reports whether the ROW was
	// found, and a found-but-already-named row must announce nothing.
	named := false
	var cwd string
	var updated protocol.ConversationUpdatedPayload
	hit := reg.Update(conversations.ConversationID(conversationID), func(cv *conversations.Conversation) {
		if cv.Name != nil {
			// Already named by create, by rename or by promote — or by this very
			// path on an earlier message. Never overwritten, so a chat is named
			// once and a later message never renames it.
			return
		}
		cv.Name = &title
		named = true
		cwd = cv.Cwd
		updated = protocol.ConversationUpdatedPayload{
			ID:         string(cv.ID),
			IsPromoted: cv.IsPromoted,
			IsArchived: cv.IsArchived, // preserved: naming is not un-archiving
			Name:       &title,
			Cwd:        cv.Cwd,
			// Auto-naming is a metadata write, not a "use" — the row's own value
			// is carried through untouched, as rename_conversation carries it.
			LastUsedAt: cv.LastUsedAt,
		}
	})
	if !hit || !named {
		// !hit: the row was deleted between the enqueue and here. Nothing to name,
		// and the message is already queued, which the drain owns.
		return
	}

	// Keyed by the cwd captured under the lock, so the label names the workspace
	// this very frame reports rather than one read a moment later.
	updated.WorkspaceLabel = workspaceLabelFor(reg, cwd)

	// Eager best-effort persist so the name survives a daemon restart, exactly as
	// rename_conversation treats its own Save. A failure is non-fatal: the name is
	// live in memory and is what every subsequent read sees.
	//
	// The failure is reported under the SAME event as the success, at Error and
	// with an err field, rather than as a second "…persist_failed" event. That
	// diverges from the five sibling registry writers deliberately: this ticket
	// publishes one new event, the event IS "this conversation was auto-named",
	// and the level plus the err field say whether the write also reached disk.
	// err is a filesystem error naming the daemon's own registry path — the one
	// value on this line safe to log.
	//
	// NEITHER LINE CARRIES THE TITLE. It is a prefix of payload.Text, which this
	// handler never logs at any level, so logging the title would log the same
	// untrusted user content under a different name.
	if err := reg.Save(registryPath); err != nil {
		logger.Error("relay: send_message auto-name persist failed",
			"event", "send_message.autonamed",
			"conn_id", connID,
			"conversation_id", conversationID,
			"err", err)
	} else {
		logger.Info("relay: send_message auto-named conversation",
			"event", "send_message.autonamed",
			"conn_id", connID,
			"conversation_id", conversationID)
	}

	// The fan-out, last and after both the write and the persist, so a broadcast
	// can never fail what already succeeded. The record is the row as it stood at
	// the instant the name landed; the envelope carries no in_reply_to, which is
	// what makes this a push rather than a reply — nothing solicited it.
	if announce != nil {
		announce(updated)
	}
}
