package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound ON-DEMAND CONTEXT-USAGE interception (#2431): the
// handler behind dispatchAppFrame's TypeRequestContextUsage case and the one wire
// code it mints. It is the join between three landed pieces that had no caller
// between them — #2370's TypeContextUsage / ContextUsagePayload, #2371's
// turnbridge mapping from a claude reading to that payload, and #2430's
// streamsup.Runner.QueryContextUsage, which waits for the response carrying its own
// request's id so a reading handed to a waiting caller is not also republished as an
// unsolicited frame.
//
// ITS OWN FILE for v2session_modelrequest.go's stated reason, and with a sharper
// version of it here: the outbound context_usage frame already has a producer on the
// live turn lane, so a REQUEST handler living in a turn- or stream-named file would
// read as part of that cadence. The post-turn publication is the neighbour to read
// alongside it — same frame, same payload type, different trigger and a different
// detail.
//
// WHERE IT RUNS: ON THE ADDRESSED CONN'S appFrameWorker, not inline on Run. This is
// the one structural difference from handleRequestModelList, whose header states the
// division, and it follows from what answering costs. That verb does a registry
// lookup and copies ten model rows. This one may WAIT TWICE: first for an open turn
// to end, then for a round trip to claude. Either is unbounded in the way Run cannot
// tolerate — Run services every connection and every wake channel.
//
// THAT PLACEMENT IS A CONSTRAINT ON FUTURE EDITS, not just a description. The reply
// is sealed by Run under the single-owner send CipherState, so emitting from this
// worker would be a concurrent Encrypt — a NONCE REUSE, a real break rather than a
// race annoyance. Every emission below therefore goes through forwardToRun, and any
// future arm must too. The inverse also holds: if this verb were ever made cheap
// enough to move back onto Run, the emits must switch to forwardEnvelope in the SAME
// change.
//
// IT ADDS A PATH AND REMOVES NONE. The automatic post-turn context_usage publication
// is untouched; a conversation that already receives one receives exactly what it
// received before, at the same cadence and the same detail.

// Static reply messages, one per wire code this path can answer with. Both are fixed
// constants and NEITHER is derived from an error value, an id, or a reading — the
// payload this handler forwards is claude- and workspace-authored and untrusted, and
// nothing derived from it may reach a message a client reads.
const (
	msgContextUsageConversationNotFound = "conversation not found"
	msgContextUsageUnavailable          = "context usage is unavailable"
)

// The two answers, spelled once, reusing attachmentReject's shape so a code cannot be
// paired with the wrong retryable flag — the field a client actually branches on.
// Values rather than a map so each is addressable by name from the tests that pin
// them; the model-list and MCP-status reject sets are the precedent.
//
// ONE IS PERMANENT FOR THE REQUEST AS SENT AND ONE IS NOT. A conversation this daemon
// does not host stays unhosted however often the client asks. Every cause behind
// context_usage.unavailable — no bound session, no live child, a rotation in flight, a
// turn that outlived the query deadline, an unusable payload, or no daemon-side source
// wired — can clear without the client changing anything.
var (
	rejectContextUsageConversationNotFound = attachmentReject{protocol.CodeConversationNotFound, msgContextUsageConversationNotFound, false}
	rejectContextUsageUnavailable          = attachmentReject{protocol.CodeContextUsageUnavailable, msgContextUsageUnavailable, true}
)

// handleRequestContextUsage answers one inbound request_context_usage with the named
// conversation's current context breakdown or with one coded reject. It runs on the
// addressed conn's appFrameWorker (see the file header); dispatchAppFrame has already
// enforced the nil-seam and interactive gates on Run.
//
// It takes the plaintext rather than an already-probed Envelope, because it runs off
// Run: the worker receives bytes, so the frame is decoded once by the discriminator
// and again here. The inline-on-Run handlers take the Envelope for the mirrored
// reason.
//
// ORDER IS THE DESIGN, and the ordering — not merely the presence of the checks — is
// what carries the security property:
//
//  1. THE GATES ARE ALREADY DISCHARGED, ON RUN. A conn that did not negotiate
//     interactive, and a daemon with no seam wired, are fully inert — no decode, no
//     membership check, no seam call, NO REPLY — so neither can learn whether the
//     named conversation exists, whether a reading is available, or that this verb is
//     implemented at all. They live at the dispatch arm rather than here because a
//     frame that fails them must never reach the queue; this handler is therefore NOT
//     the enforcement point and must not grow a second copy of either check, which
//     would be a second thing to keep in agreement.
//  2. AN ENVELOPE DECODE FAILURE IS UNREACHABLE after dispatchAppFrame matched these
//     bytes. There is no trustworthy envelope id to correlate a reply on, and the
//     decoder error can quote remote-authored bytes, so log neither the id nor the
//     error.
//  3. A PAYLOAD DECODE FAILURE IS TOLERATED, NOT REJECTED, and the reason does NOT
//     generalise. It leaves ConversationID == "", which names no conversation —
//     handlers.CreateConversation mints every id from conversations.NewID — so it is
//     refused at step 4 without a third code. This is where the verb departs from
//     handleMCPStatusRequest, which answers protocol.malformed: that handler's
//     vocabulary already carried the code, and this verb's is deliberately two codes.
//     handleRequestHistory cannot do this at all, because there the empty id becomes a
//     path component where filepath.Join(dir, "") is the log ROOT rather than an
//     error; here it reaches a registry lookup and nothing else. NEVER echo or log the
//     error — encoding/json quotes offending input into its error string.
//  4. MEMBERSHIP SEPARATES THE TWO REFUSAL ARMS. KnownConversation answers "is this
//     conversation ours", so a false here is the permanent conversation.not_found and
//     a seam refusal PAST it is the retryable context_usage.unavailable. A nil
//     membership seam refuses everything, the only fail-safe reading. Membership
//     rather than a session router, for HistoryPage's stated reason: a router
//     additionally refuses a known conversation with no bound session, and the seam
//     below already refuses that case on its own terms.
//  5. RESOLVE, COMMA-OK HONOURED. The payload is read only on true. Writing
//     `payload, _ = …` would happen to work today only because the cmd/pyry producer
//     zeroes its refusal return — a property of that package, not of this contract —
//     and here it would be worse than elsewhere, since an all-zero ContextUsagePayload
//     is indistinguishable from a genuine empty context.
//  6. The reading, forwarded byte for byte.
//
// SECURITY — the never-log rule, which differs per field rather than applying
// uniformly. THE READING REACHES THE LOGGER ON NO ARM AND AT NO LEVEL. It is
// mixed-provenance (ContextUsagePayload's own doc): only ConversationID is
// daemon-authored, and every other string is claude- or workspace-authored, including
// ContextUsageMemoryFile.Path values off the operator's own filesystem. Not even an
// entry count is logged — a reading's size is derived from its content. THE
// CONVERSATION ID IS LOGGED ONLY PAST STEP 4, where membership has established it is
// registry-canonical and so cannot carry the control bytes a log injection needs; on
// step 4's own arm it is NOT logged, because membership answering false is precisely
// the case where it may be an arbitrary client string.
func (m *V2SessionManager) handleRequestContextUsage(ctx context.Context, s *V2Session, plaintext []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		// Step 2. Unreachable; content-free record, no reply.
		m.cfg.Logger.Warn("relay: v2 request_context_usage envelope did not decode",
			"event", "v2.context_usage.request.envelope_err",
			"conn_id", s.connID)
		return
	}

	var p protocol.RequestContextUsagePayload
	// Step 3. The error is deliberately discarded rather than checked: a failure
	// leaves ConversationID empty, which the membership gate below refuses. NEVER
	// echoed, NEVER logged.
	_ = json.Unmarshal(env.Payload, &p)

	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(p.ConversationID) {
		// Step 4. The requested id is NOT logged — nothing has shape-validated it.
		m.rejectContextUsageRequest(ctx, s, env.ID, rejectContextUsageConversationNotFound,
			"conversation is not one this daemon hosts", "")
		return
	}

	// Step 5. The id is loggable from here on. This call may wait for an open turn to
	// end and then for a child round trip; both bounds belong to the implementation,
	// and ctx is the worker's, so manager shutdown terminates them.
	payload, ok := m.cfg.ContextUsageFor(ctx, p.ConversationID)
	if !ok {
		m.rejectContextUsageRequest(ctx, s, env.ID, rejectContextUsageUnavailable,
			"no current reading is available for this conversation", p.ConversationID)
		return
	}

	m.emitContextUsageReply(ctx, s, env.ID, payload)
}

// emitContextUsageReply sends one resolved reading as a context_usage correlated to
// the request. Split from the gates above so the ordering there reads as one
// enumeration rather than trailing off into the marshal.
//
// THE PAYLOAD CROSSES UNTOUCHED. It is not rebuilt, re-stamped or re-counted:
// ConversationID comes out of the daemon's own registry record inside the resolver,
// and the three dropped counts ride through from the mapping — never recomputed from
// len() and never zeroed. ContextUsagePayload's doc states the three counts are
// independent and not inferable; forwarding is the implementation of that promise
// rather than an optimisation of it.
//
// EventID IS LEFT NIL, load-bearing exactly as it is on the model-list reply: the
// frame never enters the #647 turn-event replay ring, so this path adds no
// per-conversation ring memory, and forwardEnvelope's last_event_id dedup stays inert
// for it, so a reply can never be dropped as already-seen. No turn is opened either —
// TypeContextUsage is not a turn-boundary type.
func (m *V2SessionManager) emitContextUsageReply(ctx context.Context, s *V2Session, inReplyTo uint64, p protocol.ContextUsagePayload) {
	body, err := json.Marshal(p)
	if err != nil {
		// ContextUsagePayload is a closed struct of strings, ints and three slices of
		// closed row types, whose MarshalJSON delegates to json.Marshal over an alias,
		// so marshal cannot fail — no test can redden this. Defensive only, and it
		// answers NOTHING rather than inventing a reject the client could not act on.
		// NEVER echo err or the payload: either would quote the reading into the record.
		m.cfg.Logger.Warn("relay: v2 context_usage reply marshal failed",
			"event", "v2.context_usage.request.marshal_err",
			"conn_id", s.connID)
		return
	}

	// Content-free but for the routing ids: conn_id and the registry-canonical
	// conversation_id. The reading is NEVER logged at any level, and neither is any
	// count derived from it. Debug, not Info: this can fire as fast as a finger moves.
	m.cfg.Logger.Debug("relay: v2 context usage served",
		"event", "v2.context_usage.request.served",
		"conn_id", s.connID,
		"conversation_id", p.ConversationID)

	if !m.forwardContextUsageReply(ctx, s, inReplyTo, protocol.TypeContextUsage, body) {
		m.cfg.Logger.Debug("relay: v2 context_usage reply dropped; session tearing down",
			"event", "v2.context_usage.request.reply_dropped",
			"conn_id", s.connID)
	}
}

// rejectContextUsageRequest records one refusal and answers it, so the log line and
// the wire frame cannot drift apart into two edits. reason is a DAEMON-AUTHORED
// constant chosen by the call site — it is what keeps the causes merged behind
// context_usage.unavailable diagnosable to an operator while the wire answer stays
// exactly as coarse as it must be. conversationID is logged only where the caller has
// established it passed the membership gate; "" means the caller had no id it was
// allowed to name, and the field is then omitted rather than logged empty.
//
// rejectModelListRequest is the shape this copies, including that bargain.
func (m *V2SessionManager) rejectContextUsageRequest(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject, reason, conversationID string) {
	attrs := []any{
		"event", "v2.context_usage.request.refused",
		"conn_id", s.connID,
		"code", rej.code,
		"reason", reason,
	}
	if conversationID != "" {
		attrs = append(attrs, "conversation_id", conversationID)
	}
	m.cfg.Logger.Warn("relay: v2 request_context_usage refused", attrs...)

	body, err := json.Marshal(protocol.ErrorPayload{
		Code:      rej.code,
		Message:   rej.message,
		Retryable: rej.retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 context_usage error reply marshal failed",
			"event", "v2.context_usage.request.err_marshal",
			"conn_id", s.connID,
			"code", rej.code)
		return
	}
	if !m.forwardContextUsageReply(ctx, s, inReplyTo, protocol.TypeError, body) {
		m.cfg.Logger.Debug("relay: v2 context_usage reject dropped; session tearing down",
			"event", "v2.context_usage.request.reply_dropped",
			"conn_id", s.connID,
			"code", rej.code)
	}
}

// forwardContextUsageReply seals one reply back through Run. EVERY emission on this
// path — the reading and both rejects — goes through here, which is what keeps the
// send CipherState single-owner: this handler runs on the conn's appFrameWorker, and
// forwardToRun is the only way off it that does not Encrypt.
//
// Its own helper rather than a call into forwardMCPStatusReply, matching the package's
// posture that each reply-owing handler owns its emission helper — and it must be its
// own anyway, since that one names its log events for another verb and would misfile
// every record here.
func (m *V2SessionManager) forwardContextUsageReply(ctx context.Context, s *V2Session, inReplyTo uint64, typ string, payload json.RawMessage) bool {
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the client correlates on InReplyTo.
		Type:      typ,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &inReplyTo,
	}
	frame, err := json.Marshal(reply)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 context_usage reply envelope marshal failed",
			"event", "v2.context_usage.request.envelope_marshal",
			"conn_id", s.connID,
			"reply_type", typ)
		return false
	}
	return m.forwardToRun(ctx, s, protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
}
