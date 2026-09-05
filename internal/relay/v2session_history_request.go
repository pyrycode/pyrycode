package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound conversation-HISTORY interception (#2116): the
// handler behind dispatchAppFrame's TypeRequestHistory case, the four wire codes
// it mints, and the byte budgeting § Conversation history assigns here. It is the
// join between four landed pieces that had no caller between them —
// protocol.TypeRequestHistory (#2113, vocabulary only), history.Store.Page
// (#2112, uncalled outside its own package tests) behind the HistoryPage seam,
// and the entries #2114 and #2115 append.
//
// LIKE ITS RETRIEVAL SIBLING IT IMPORTS NOTHING FROM THE PRODUCER. internal/relay
// does not import internal/history and this file does not change that: the seam
// carries protocol types and an outcome discriminant, and the log's sentinels are
// classified on the cmd/pyry side, the way historyAppendFailure already classifies
// them for the append path.
//
// WHERE IT RUNS. On the conn's appFrameWorker, off the Run goroutine, reached from
// appFrameWorker's appFrameHistoryRequest arm — the third frame type to need that
// route after the two attachment legs, and for the same reason: answering one
// request opens log segments off disk, decodes them and marshals a page of up to
// the application-envelope cap. That is the same work #1491 had to move off Run
// for the debug bundle, and the stall it prevents is the cross-conn head-of-line
// one #965 removed.
//
// ONE EMISSION ROUTE, WHICH IS WHERE IT DIVERGES FROM #2054. A page is a single
// envelope rather than a stream, so there is no Push leg: the page and every
// reject alike go through forwardToRun, where Run seals them under s.send.
// Emitting either directly from this goroutine would be a concurrent Encrypt on
// the single-owner send CipherState — a nonce reuse. The retrieval handler needs
// two routes and a paragraph explaining why both are correct; this one needs
// neither, and the simplification is worth naming so nobody adds a second.
//
// SERIALISATION IS THE CONCURRENCY BOUND. The worker is strictly FIFO and there is
// one per conn, so a conn has at most one history request in flight; a second
// waits behind the first rather than doubling the segment reads in play. That is
// not an invariant this file maintains — it is the worker's shape — but it IS the
// reason no per-verb concurrency limit is minted here, which § Conversation
// history leaves receiver-configured and unpublished.

const (
	// defaultHistoryPageEntries is the page size the daemon substitutes for a
	// client limit of 0 — sent as zero or omitted, which decode identically.
	//
	// THE SUBSTITUTION MUST HAPPEN ABOVE THE LOG. history.Store.Page refuses a
	// limit below 1 with ErrInvalidPageSize, so passing an omitted key through as
	// a literal 0 would turn every client that leaves the field out into an error
	// — which is why protocol.RequestHistoryPayload.Limit assigns the zero a
	// meaning rather than leaving it to the handler's taste.
	//
	// 50 matches the ask in the committed testdata/request_history.json fixture,
	// so the published example and the daemon's own default describe one page
	// size rather than two.
	defaultHistoryPageEntries = 50

	// maxHistoryPageEntries is the largest page this handler will ASK the log for,
	// whatever the client requested.
	//
	// IT IS NOT A SECOND COPY OF history.MaxPageEntries. That ceiling (4096) bounds
	// a COUNT and belongs to the log; this one is derived from the envelope cap and
	// belongs here, because § Conversation history assigns the byte budgeting to
	// this ticket. THE ARITHMETIC: an entry's four always-present keys cost about
	// 58 bytes at their absolute minimum — {"id":1,"type":"a","payload":0,
	// "ts":"2026-09-05T00:00:00Z"} — so no more than 65519/48 ≈ 1365 entries can
	// ever serialise inside one page, whatever they contain. Asking for 4096 is
	// therefore work that is GUARANTEED to be discarded by the budget loop below.
	//
	// IT IS A RESOURCE BOUND, NOT TIDINESS. MaxSegmentBytes bounds one stored
	// entry line at 1 MiB, so a paired but hostile limit of 4096 makes the daemon
	// open segments and hold decoded entries measured in hundreds of megabytes
	// before one byte reaches the wire. 1024 cuts that fourfold and costs nothing
	// real, since no page that large is emittable. The published count ceiling
	// stays 4096 and stays the log's; this sits under it, and § Page size already
	// permits returning fewer entries than asked to fit the cap.
	maxHistoryPageEntries = 1024

	// maxAppEnvelopeBytes is the v2 application-envelope cap
	// (docs/protocol-mobile.md § Application-envelope size cap): a transport frame
	// is one Noise transport message of 65535 bytes including the 16-byte AEAD
	// tag, leaving 65519 for the decrypted envelope. An envelope over it is
	// REJECTED BY THE TRANSPORT with message.too_long rather than truncated, which
	// is why a page is budgeted before it is emitted rather than after.
	//
	// The repo carried this number only in comments until this ticket; it is
	// spelled once here because this is the first outbound path that has to
	// COMPUTE against it rather than merely stay under it by construction.
	maxAppEnvelopeBytes = 65519

	// maxHistoryPageFitAttempts bounds the shortening loop.
	//
	// TERMINATION DOES NOT DEPEND ON IT: historyFitLimit returns a value strictly
	// below the entry count it was handed, so the limit decreases every iteration
	// and the loop emits at one entry regardless. This cap exists for the ONE case
	// that can defeat a computed fit — an append landing between two asks with an
	// empty cursor, where the window is anchored at the newest entry and the entry
	// set therefore changes underneath. It is a deterministic backstop on a
	// deterministic computation, not a second estimate.
	maxHistoryPageFitAttempts = 3
)

// Static reply messages, one per wire code this path can answer with. All five
// are fixed constants and NONE is derived from an error value, an id, a cursor or
// a path: internal/history's errors are free of payload bytes by construction but
// DO format absolute filesystem paths ("open segment %q", "resolve log directory
// %q"), and both strings the frame carries are client-supplied.
const (
	msgHistoryConversationNotFound = "conversation not found"
	msgHistoryInvalidRequest       = "history request rejected"
	msgHistoryInvalidPageSize      = "history page size is not valid"
	msgHistoryInvalidCursor        = "history cursor is not valid"
	msgHistoryUnavailable          = "conversation history is unavailable"
)

// The five answers, spelled once, reusing attachmentReject's shape so a code
// cannot be paired with the wrong retryable flag — the field a client actually
// branches on. Values rather than a map so each is addressable by name from the
// tests that pin the four codes this ticket minted.
//
// FOUR ARE PERMANENT FOR THE REQUEST AS SENT and one is not: every cause behind
// history.unavailable — a corrupt segment, an I/O error, an unwired log — can
// clear without the client changing anything, and none of the other four can.
var (
	rejectHistoryConversationNotFound = attachmentReject{protocol.CodeConversationNotFound, msgHistoryConversationNotFound, false}
	rejectHistoryInvalidRequest       = attachmentReject{protocol.CodeHistoryInvalidRequest, msgHistoryInvalidRequest, false}
	rejectHistoryInvalidPageSize      = attachmentReject{protocol.CodeHistoryInvalidPageSize, msgHistoryInvalidPageSize, false}
	rejectHistoryInvalidCursor        = attachmentReject{protocol.CodeHistoryInvalidCursor, msgHistoryInvalidCursor, false}
	rejectHistoryUnavailable          = attachmentReject{protocol.CodeHistoryUnavailable, msgHistoryUnavailable, true}
)

// handleRequestHistory answers one inbound request_history with a page of the
// named conversation's past entries or with one coded reject. Intercepted in
// dispatchAppFrame before dispatch.Route, exactly like handleRequestAttachment,
// and routed to this conn's appFrameWorker rather than handled inline on Run (see
// the file header).
//
// It takes the plaintext rather than the already-probed Envelope because the
// worker receives bytes: the frame is decoded here for the second and last time.
//
// ORDER IS THE DESIGN, and the ordering — not merely the presence of the checks —
// is what carries the security property:
//
//  1. A nil HistoryPage makes the frame INERT — but still CONSUMED, so it no
//     longer draws dispatch.Route's unknown-type reply. Mirrors the nil
//     AttachmentResolve guard and buys the same property: an unwired daemon
//     performs zero parsing of remote-authored bytes.
//  2. A PAYLOAD DECODE FAILURE IS REJECTED, NOT TOLERATED.
//     protocol.RequestHistoryPayload's own block assigns that obligation here, and
//     handleRequestSnapshot's tolerance does NOT transfer: its zero value fails
//     the next gate anyway, where this one's empty conversation id would reach a
//     path join, and filepath.Join(dir, "") is dir — the log ROOT rather than an
//     error. NOTHING about the failure is echoed or logged: encoding/json quotes
//     offending input into its error string and those bytes are remote-authored.
//  3. THE MEMBERSHIP GATE FIRES BEFORE THE ID BECOMES A PATH COMPONENT. That is
//     history.Store.Page's stated precondition and this handler is the caller that
//     discharges it. KnownConversation rather than a session router: a router
//     additionally refuses a known conversation with NO BOUND SESSION, which is
//     precisely the reopened conversation this verb exists to serve, and would
//     answer it the retryable server.binary_offline. A nil seam refuses
//     everything, the only fail-safe reading. It covers the non-canonical id too,
//     with no shape check of its own — the registry holds canonical ids only.
//  4. A NEGATIVE limit IS REFUSED, ZERO IS NOT. Zero asks the daemon to choose.
//  5. The page, budgeted to the envelope cap, or the outcome's reject.
//
// THE ANSWER IS DISTINGUISHABLE, UNLIKE #2054's, and that is a decision rather
// than an oversight. handleRequestAttachment merges an unknown conversation into
// attachment.not_found because a distinguishable answer would rebuild the
// path-existence oracle its merge exists to prevent. Three things make that
// reasoning not transfer: there is no second id to protect here, § Conversation
// history's published Rejects list the conversation condition SEPARATELY from the
// cursor's merged answer, and handleRequestSnapshot already answers an unknown or
// foreign conversation distinguishably. The one merge on this path is the
// cursor's three causes, and it is structural — they arrive as one outcome, so
// this site CANNOT branch on what it must not distinguish.
//
// SECURITY — the never-log rule, which differs per field rather than applying
// uniformly. The CURSOR IS NEVER LOGGED ON ANY ARM: nothing above internal/history
// validates its shape, history.cursorRefusal already declines to echo one for that
// reason, and raw it is the log-injection shape § Attachments forbids for a
// filename. The CONVERSATION ID IS LOGGED ONLY PAST STEP 3, where membership has
// established it is registry-canonical — § Conversation history's "loggable only
// after their shape is validated". ENTRY BYTES REACH THE LOGGER ON NO ARM: the
// served record carries counts, flags and lengths only. And no error out of the
// seam is available to log even if a future edit wanted to — the outcome
// discriminant is all that crosses, which is the other half of the bargain the
// cmd/pyry classifier makes.
func (m *V2SessionManager) handleRequestHistory(ctx context.Context, s *V2Session, plaintext []byte) {
	if m.cfg.HistoryPage == nil {
		// Step 1.
		m.cfg.Logger.Debug("relay: v2 request_history inert; no history log wired",
			"event", "v2.history.request.inert",
			"conn_id", s.connID)
		return
	}

	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		// Unreachable: dispatchAppFrame decoded these same bytes to match the
		// type before enqueuing them. Reply nothing — there is no envelope id
		// left to correlate a reply to. NEVER echo err.
		m.cfg.Logger.Warn("relay: v2 request_history envelope did not decode",
			"event", "v2.history.request.envelope_err",
			"conn_id", s.connID)
		return
	}

	var req protocol.RequestHistoryPayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		// Step 2. NEVER echo err or any payload byte, and no conversation id
		// either: the decode is what failed, so a partially-populated id would
		// attribute a refusal to a conversation nobody asked for.
		m.rejectHistoryRequest(ctx, s, env.ID, rejectHistoryInvalidRequest, "payload did not decode", "")
		return
	}

	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(req.ConversationID) {
		// Step 3. The requested id is NOT logged — nothing has shape-validated it,
		// and membership answering false is precisely the case where it may be an
		// arbitrary client string.
		m.rejectHistoryRequest(ctx, s, env.ID, rejectHistoryConversationNotFound, "conversation is not one this daemon hosts", "")
		return
	}

	if req.Limit < 0 {
		// Step 4. The id is loggable from here on; the limit is not logged at all,
		// because the reason constant already says which refusal this is and the
		// value carries nothing else an operator needs.
		m.rejectHistoryRequest(ctx, s, env.ID, rejectHistoryInvalidPageSize, "page size is negative", req.ConversationID)
		return
	}

	m.serveHistoryPage(ctx, s, env.ID, req)
}

// serveHistoryPage asks the log, budgets the answer against the envelope cap and
// emits it. Split from the gates above so the ordering there reads as one
// enumeration rather than trailing off into the budgeting loop.
//
// THE BUDGET IS ENFORCED BY RE-ASKING, NEVER BY TRUNCATING THE RETURNED SLICE.
// Truncating is the tempting one-liner and it is wrong: the page's Cursor names
// the position just before the OLDEST entry the log returned, so dropping entries
// from the tail while keeping that cursor makes the client's next ask skip exactly
// the dropped ones. That is a silent gap in the walk, and it is invisible to any
// test whose pages all fit.
//
// AtStart IS NEVER SYNTHESISED. It comes from the log's answer for whatever limit
// was finally asked, so a page shortened for bytes reports the log's own reading —
// which is what makes byte-driven truncation safe for a client at all, given that
// termination is AtStart and a short page means nothing.
func (m *V2SessionManager) serveHistoryPage(ctx context.Context, s *V2Session, inReplyTo uint64, req protocol.RequestHistoryPayload) {
	limit := effectiveHistoryLimit(req.Limit)
	for attempt := 1; ; attempt++ {
		// req.Cursor is passed BYTE FOR BYTE UNPARSED. history.parseCursor is the
		// only validator anywhere and nothing here may decode one.
		res := m.cfg.HistoryPage(req.ConversationID, req.Cursor, limit)
		switch res.Outcome {
		case HistoryPageBadCursor:
			// The one merged refusal: undecodable, foreign, and naming a position
			// not in this log arrive here identically. The cursor is not logged.
			m.rejectHistoryRequest(ctx, s, inReplyTo, rejectHistoryInvalidCursor, "cursor was refused by the log", req.ConversationID)
			return
		case HistoryPageUnavailable:
			m.rejectHistoryRequest(ctx, s, inReplyTo, rejectHistoryUnavailable, "log could not be read", req.ConversationID)
			return
		}

		payload, err := json.Marshal(protocol.HistoryPagePayload{
			Entries: res.Entries,
			Cursor:  res.Cursor,
			AtStart: res.AtStart,
		})
		if err != nil {
			// The entries' payloads are json.RawMessage that already round-tripped
			// through the log, and the rest is a closed struct; marshal cannot fail
			// in practice. NEVER echo err — it would quote entry bytes.
			m.cfg.Logger.Warn("relay: v2 history page marshal failed",
				"event", "v2.history.page_marshal",
				"conn_id", s.connID,
				"conversation_id", req.ConversationID,
				"entries", len(res.Entries))
			return
		}
		frame, err := m.historyFrame(s, inReplyTo, protocol.TypeHistoryPage, payload)
		if err != nil {
			return
		}

		if len(frame) <= maxAppEnvelopeBytes || len(res.Entries) <= 1 {
			if len(frame) > maxAppEnvelopeBytes {
				// The floor of the loop: one entry that no page can carry. Emitted
				// rather than refused, because refusing stalls the walk PERMANENTLY
				// — the client never receives the cursor, so it can never step past
				// the entry — while emitting yields the transport's own
				// message.too_long, a distinguishable answer, and the frame may well
				// be deliverable since this budget is measured against real
				// marshalled bytes. Lengths only; no entry content.
				m.cfg.Logger.Warn("relay: v2 history page exceeds the application-envelope cap",
					"event", "v2.history.oversized",
					"conn_id", s.connID,
					"conversation_id", req.ConversationID,
					"bytes", len(frame),
					"cap", maxAppEnvelopeBytes)
			}
			m.emitHistoryPage(ctx, s, inReplyTo, req.ConversationID, frame, res)
			return
		}

		next := historyFitLimit(res.Entries, len(frame)-maxAppEnvelopeBytes)
		if attempt >= maxHistoryPageFitAttempts {
			// See maxHistoryPageFitAttempts: only a concurrent append can defeat a
			// computed fit, and one entry always emits.
			next = 1
		}
		m.cfg.Logger.Debug("relay: v2 history page shortened to fit the envelope cap",
			"event", "v2.history.shortened",
			"conn_id", s.connID,
			"conversation_id", req.ConversationID,
			"bytes", len(frame),
			"from", len(res.Entries),
			"to", next)
		limit = next
	}
}

// effectiveHistoryLimit turns the client's claim into the count this daemon will
// ask the log for. The claim is NEVER used to size anything — the rule
// protocol.RequestHistoryPayload.Limit states — only to choose this number.
func effectiveHistoryLimit(asked int) int {
	if asked <= 0 {
		return defaultHistoryPageEntries
	}
	if asked > maxHistoryPageEntries {
		return maxHistoryPageEntries
	}
	return asked
}

// historyFitLimit reports how many of entries, newest-first, fit once overBy bytes
// have been shed from the page — the count to re-ask the log for.
//
// IT ALWAYS RETURNS STRICTLY FEWER THAN IT WAS HANDED, and at least one. That is
// what makes the caller's loop terminate without depending on its attempt cap: the
// limit falls every iteration and the caller emits unconditionally at one entry.
// The clamp is not defensive padding — a page can be over the cap because of
// overhead outside the entries array, where a purely size-driven fit would compute
// no reduction at all and spin.
//
// Sizes come from marshalling each entry rather than from an estimate, because
// escaping makes a byte count unpredictable from the decoded form: encoding/json
// escapes HTML by default, so a single '<' in a stored payload costs six bytes on
// the wire.
func historyFitLimit(entries []protocol.HistoryEntry, overBy int) int {
	budget := 0
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			break
		}
		budget += len(b) + 1 // the entry plus its separating comma
	}
	budget -= overBy

	used, fit := 0, 0
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			break
		}
		if used+len(b)+1 > budget {
			break
		}
		used += len(b) + 1
		fit++
	}

	if fit < 1 {
		fit = 1
	}
	if fit >= len(entries) {
		fit = len(entries) - 1
	}
	return fit
}

// historyFrame seals nothing and sends nothing: it builds the outbound envelope
// and marshals it, so a caller can MEASURE the frame it is about to emit against
// the envelope cap and shorten before committing to it.
//
// Separated from the emit for that reason alone. Every other reply helper in this
// package marshals and forwards in one step, which is right when the answer's size
// is fixed by construction and wrong here, where it is the thing being decided.
// The reject path shares it so the correlation, the non-load-bearing envelope id
// and the UTC stamp are decided once for both answers.
func (m *V2SessionManager) historyFrame(s *V2Session, inReplyTo uint64, typ string, payload json.RawMessage) ([]byte, error) {
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      typ,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &inReplyTo,
	}
	frame, err := json.Marshal(reply)
	if err != nil {
		// payload is already valid JSON and the rest is a closed struct; marshal
		// cannot fail in practice. NEVER echo err — it would quote entry bytes.
		m.cfg.Logger.Warn("relay: v2 history reply envelope marshal failed",
			"event", "v2.history.envelope_marshal",
			"conn_id", s.connID,
			"reply_type", typ)
		return nil, err
	}
	return frame, nil
}

// emitHistoryPage hands one measured page to Run for sealing and records that it
// went. The record carries counts, a flag and a length — never an entry's type,
// timestamp or payload, which are conversation content and belong only in the log
// the page came from.
func (m *V2SessionManager) emitHistoryPage(ctx context.Context, s *V2Session, inReplyTo uint64, conversationID string, frame []byte, res HistoryPageResult) {
	if !m.forwardToRun(ctx, s, protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame}) {
		m.cfg.Logger.Debug("relay: v2 history page dropped; session tearing down",
			"event", "v2.history.reply_dropped",
			"conn_id", s.connID)
		return
	}
	m.cfg.Logger.Info("relay: v2 history page served",
		"event", "v2.history.served",
		"conn_id", s.connID,
		"conversation_id", conversationID,
		"entries", len(res.Entries),
		"at_start", res.AtStart,
		"bytes", len(frame),
		"in_reply_to", inReplyTo)
}

// rejectHistoryRequest records one refusal and answers it, so the log line and the
// wire frame cannot drift apart into two edits. reason is a DAEMON-AUTHORED
// constant chosen by the call site — it is what keeps the cursor's three merged
// causes and the gates' separate ones diagnosable to an operator while the wire
// answer stays exactly as coarse as it must be. conversationID is logged only
// where the caller has established it passed the membership gate; "" means the
// caller had no id it was allowed to name, and the field is then omitted rather
// than logged empty.
//
// THE CURSOR IS ABSENT FROM THIS SIGNATURE ON PURPOSE. There is no arm on which it
// may be logged, so it is not a parameter a future edit could pass "just for this
// one case".
func (m *V2SessionManager) rejectHistoryRequest(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject, reason, conversationID string) {
	attrs := []any{
		"event", "v2.history.request.refused",
		"conn_id", s.connID,
		"code", rej.code,
		"reason", reason,
	}
	if conversationID != "" {
		attrs = append(attrs, "conversation_id", conversationID)
	}
	m.cfg.Logger.Warn("relay: v2 request_history refused", attrs...)
	m.historyReplyError(ctx, s, inReplyTo, rej)
}

// historyReplyError answers one refusal with a single TypeError envelope
// correlated to inReplyTo. rej supplies a STATIC message and the retryability
// docs/protocol-mobile.md § Error codes publishes for that code; no value derived
// from an error, an id, a cursor or a path ever reaches this path.
//
// Its own helper rather than a call into attachmentReplyError, matching the
// package's stated posture that each reply-owing handler owns its error helper —
// and it must be its own anyway, since that one's log events are named for the
// attachment path and would misfile every refusal here.
func (m *V2SessionManager) historyReplyError(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject) {
	payload, err := json.Marshal(protocol.ErrorPayload{
		Code:      rej.code,
		Message:   rej.message,
		Retryable: rej.retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 history error reply marshal failed",
			"event", "v2.history.err_marshal",
			"conn_id", s.connID,
			"code", rej.code)
		return
	}
	frame, err := m.historyFrame(s, inReplyTo, protocol.TypeError, payload)
	if err != nil {
		return
	}
	if !m.forwardToRun(ctx, s, protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame}) {
		m.cfg.Logger.Debug("relay: v2 history reject dropped; session tearing down",
			"event", "v2.history.reply_dropped",
			"conn_id", s.connID,
			"code", rej.code)
	}
}
