package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound ON-DEMAND MODEL-LIST interception (#2125): the
// handler behind dispatchAppFrame's TypeRequestModelList case and the one wire
// code it mints. It is the join between two landed pieces that had no caller
// between them — protocol.TypeRequestModelList / RequestModelListPayload and the
// cmd/pyry resolver behind the ModelListFor seam, which since #2124 answers for a
// conversation with no bound session too.
//
// ITS OWN FILE for v2session_modelreconcile.go's stated reason, which applies with
// more force here: "modal" and "model" differ by one letter, and a model-list
// REQUEST handler living in a modal- or settings-named file is a readability
// hazard worth one file to avoid. The reconcile is the neighbour to read alongside
// it — same frame, same payload source, different trigger.
//
// WHERE IT RUNS: INLINE ON THE RUN DISPATCH GOROUTINE, unlike the three arms of
// dispatchAppFrame that hand off to the conn's appFrameWorker. Those three hash
// bytes, read files off disk, or marshal a page against the envelope cap; this one
// does a registry lookup, copies at most ten model rows and marshals one small
// envelope. Copying that plumbing would cost an appFrameJob kind and a worker
// route for work that needs neither.
//
// THAT PLACEMENT IS A CONSTRAINT ON FUTURE EDITS, not just a description. The
// reply is sealed by Run under the single-owner send CipherState, so emitting from
// any other goroutine would be a concurrent Encrypt — a NONCE REUSE, a real break
// rather than a race annoyance. If this arm is ever moved to enqueueAppFrame (as
// request_history was, for work this verb does not do), the emit must switch from
// forwardEnvelope to forwardToRun in the SAME change, and the ModelListFor seam
// must stay a bounded in-memory read until then.
//
// IT ADDS A PATH AND REMOVES NONE. The live interactive turn lane and the
// connect-time reconcileModelLists are untouched by this ticket; a conversation
// that already receives model_list receives exactly what it received before.

// Static reply messages, one per wire code this path can answer with. Both are
// fixed constants and NEITHER is derived from an error value, an id, or a model
// string — the payload this handler forwards is claude-authored and untrusted, and
// nothing derived from it may reach a message a client reads.
const (
	msgModelListConversationNotFound = "conversation not found"
	msgModelListUnavailable          = "model list is unavailable"
)

// The two answers, spelled once, reusing attachmentReject's shape so a code cannot
// be paired with the wrong retryable flag — the field a client actually branches
// on. Values rather than a map so each is addressable by name from the tests that
// pin them. handleRequestHistory's rejectHistory* set is the precedent.
//
// ONE IS PERMANENT FOR THE REQUEST AS SENT AND ONE IS NOT. A conversation this
// daemon does not host stays unhosted however often the client asks; every cause
// behind model_list.unavailable — a bootstrap child that has not yet answered its
// initialize ask, one constructed evicted, or no daemon-side source wired — can
// clear without the client changing anything.
var (
	rejectModelListConversationNotFound = attachmentReject{protocol.CodeConversationNotFound, msgModelListConversationNotFound, false}
	rejectModelListUnavailable          = attachmentReject{protocol.CodeModelListUnavailable, msgModelListUnavailable, true}
)

// handleRequestModelList answers one inbound request_model_list with the named
// conversation's model menu or with one coded reject. Intercepted in
// dispatchAppFrame before dispatch.Route, like handleRequestSessionSettings, and
// running inline on the Run goroutine (see the file header).
//
// It takes the already-probed Envelope rather than the plaintext, because it runs
// on Run: the frame is decoded once by the discriminator and its payload once
// here. The off-Run handlers take bytes only because their worker receives bytes.
//
// ORDER IS THE DESIGN, and the ordering — not merely the presence of the checks —
// is what carries the security property:
//
//  1. THE CAPABILITY GATE IS THE AUTHZ BOUNDARY. A conn that did not negotiate
//     interactive is fully inert — no decode, no membership check, no seam call, NO
//     REPLY — so it cannot learn whether the named conversation exists, whether a
//     menu is retained, or that this verb is implemented at all. Same posture as
//     handleRequestSessionSettings, and it MUST stay ahead of every step below.
//  2. A PAYLOAD DECODE FAILURE IS TOLERATED, NOT REJECTED, and the reason does NOT
//     generalise. It leaves ConversationID == "", which names no conversation —
//     handlers.CreateConversation mints every id from conversations.NewID — so it
//     is refused at step 3 without a third code. handleRequestHistory cannot do
//     this because there the empty id becomes a path component, where
//     filepath.Join(dir, "") is the log ROOT rather than an error; here it reaches
//     a registry lookup and nothing else. There is deliberately no separate
//     convID == "" pre-check: a second spelling of a check the gate below already
//     performs would close nothing. NEVER echo or log the error — encoding/json
//     quotes offending input into its error string and those bytes are
//     remote-authored.
//  3. MEMBERSHIP SEPARATES THE TWO REFUSAL ARMS. KnownConversation answers "is this
//     conversation ours", so a false here is the permanent conversation.not_found
//     and a resolver refusal PAST it is the retryable model_list.unavailable. A nil
//     seam refuses everything, the only fail-safe reading. Membership rather than a
//     session router, for the reason HistoryPage's block gives: a router
//     additionally refuses a known conversation with NO BOUND SESSION, which is
//     precisely the conversation this verb exists to serve.
//  4. RESOLVE, COMMA-OK HONOURED. A nil ModelListFor and a false comma-ok merge
//     into one retryable answer: both mean the daemon hosts this conversation and
//     has no menu to give, the client's repair is identical, and distinguishing
//     them would publish whether the host's model-list source is wired.
//  5. The menu, forwarded byte for byte.
//
// NEVER AN EMPTY models ARRAY STANDING IN FOR "UNKNOWN". turnevent.ModelList.Models
// is documented never-empty and the resolver's comma-ok is the only spelling of
// "nothing to send", so a refusal is an error frame and never a degraded
// model_list. That is where this verb departs from request_session_settings, whose
// all-zero reply is a real answer — a shape model_list cannot borrow.
//
// SECURITY — the never-log rule, which differs per field rather than applying
// uniformly. THE MODEL VALUES REACH THE LOGGER ON NO ARM AND AT NO LEVEL: they are
// claude-authored untrusted text (#833), and the resolver and the reconcile
// enumerator both enforce that structurally by having no logger at all. This
// handler is the first model-list path that carries one, so the property is a
// discipline here rather than a construction — the resolved payload appears in no
// log call below. THE CONVERSATION ID IS LOGGED ONLY PAST STEP 3, where membership
// has established it is registry-canonical and so cannot carry the control bytes a
// log injection needs; on step 3's own arm it is NOT logged, because membership
// answering false is precisely the case where it may be an arbitrary client string.
func (m *V2SessionManager) handleRequestModelList(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if !s.interactive {
		return // Step 1. Inert: no reply, no decode, no seam consulted.
	}

	var p protocol.RequestModelListPayload
	// Step 2. The error is deliberately discarded rather than checked: a failure
	// leaves ConversationID empty, which the membership gate below refuses. NEVER
	// echoed, NEVER logged.
	_ = json.Unmarshal(env.Payload, &p)

	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(p.ConversationID) {
		// Step 3. The requested id is NOT logged — nothing has shape-validated it.
		m.rejectModelListRequest(ctx, s, env.ID, rejectModelListConversationNotFound, "conversation is not one this daemon hosts", "")
		return
	}

	if m.cfg.ModelListFor == nil {
		// Step 4, the unwired half of the merge. The id is loggable from here on.
		m.rejectModelListRequest(ctx, s, env.ID, rejectModelListUnavailable, "no model-list source is wired", p.ConversationID)
		return
	}
	// The comma-ok is HONOURED, not discarded: ModelListFor's doc says a caller MUST
	// NOT read the payload on false, so it is assigned only on true. Writing
	// `payload, _ = …` would happen to work today only because the cmd/pyry producer
	// zeroes its refusal return — a property of that package, not of this contract.
	payload, ok := m.cfg.ModelListFor(p.ConversationID)
	if !ok {
		m.rejectModelListRequest(ctx, s, env.ID, rejectModelListUnavailable, "no vocabulary is retained for this conversation", p.ConversationID)
		return
	}

	m.emitModelListReply(ctx, s, env.ID, payload)
}

// emitModelListReply sends one resolved menu as a model_list correlated to the
// request. Split from the gates above so the ordering there reads as one
// enumeration rather than trailing off into the marshal.
//
// THE PAYLOAD CROSSES UNTOUCHED. It is not rebuilt, re-stamped or re-counted:
// ConversationID comes out of the daemon's own registry record inside the
// resolver, and DroppedModels rides through from the decode — never recomputed
// from len(Models) and never zeroed. The one legitimate reading of "the same
// payload the connect-time reconcile would send" is the same SOURCE, so forwarding
// is the implementation of that promise rather than an optimisation of it.
//
// EventID IS LEFT NIL, load-bearing exactly as it is on the reconcile: the frame
// never enters the #647 turn-event replay ring, so this path adds no
// per-conversation ring memory, and forwardEnvelope's last_event_id dedup stays
// inert for it, so a reply can never be dropped as already-seen. No turn is opened
// either — TypeModelList is not a turn-boundary type (frozen by #1704/#1705).
func (m *V2SessionManager) emitModelListReply(ctx context.Context, s *V2Session, inReplyTo uint64, p protocol.ModelListPayload) {
	body, err := json.Marshal(p)
	if err != nil {
		// ModelListPayload is a closed struct of string / []ModelOption / int whose
		// two custom MarshalJSONs delegate to json.Marshal over closed types, so
		// marshal cannot fail — no test can redden this. Defensive only, and it
		// answers NOTHING rather than inventing a reject the client could not act
		// on. NEVER echo err or the payload: either would quote model values into the
		// record.
		m.cfg.Logger.Warn("relay: v2 model_list reply marshal failed",
			"event", "v2.modellist.request.marshal_err",
			"conn_id", s.connID)
		return
	}

	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the client correlates on InReplyTo.
		Type:      protocol.TypeModelList,
		TS:        time.Now().UTC(),
		Payload:   body,
		InReplyTo: &inReplyTo,
	}
	// Content-free but for the routing id: conn_id and the registry-canonical
	// conversation_id. The model values are NEVER logged at any level, and neither
	// is the entry count — a menu's size is derived from its content. Debug, not
	// Info: this is a routine read that can fire on every chat open, the cadence
	// handleRequestSessionSettings cites.
	m.cfg.Logger.Debug("relay: v2 model list served",
		"event", "v2.modellist.request.served",
		"conn_id", s.connID,
		"conversation_id", p.ConversationID)
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		// Unreachable in practice: s is V2StateOpen on the dispatch goroutine.
		// Logged at debug and dropped — the package's outbound-drop posture.
		m.cfg.Logger.Debug("relay: v2 model_list reply push dropped",
			"event", "v2.modellist.request.push_err",
			"conn_id", s.connID,
			"err", err)
	}
}

// rejectModelListRequest records one refusal and answers it, so the log line and
// the wire frame cannot drift apart into two edits. reason is a DAEMON-AUTHORED
// constant chosen by the call site — it is what keeps the two merged causes behind
// model_list.unavailable diagnosable to an operator while the wire answer stays
// exactly as coarse as it must be. conversationID is logged only where the caller
// has established it passed the membership gate; "" means the caller had no id it
// was allowed to name, and the field is then omitted rather than logged empty.
//
// rejectHistoryRequest is the shape this copies, including that bargain.
func (m *V2SessionManager) rejectModelListRequest(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject, reason, conversationID string) {
	attrs := []any{
		"event", "v2.modellist.request.refused",
		"conn_id", s.connID,
		"code", rej.code,
		"reason", reason,
	}
	if conversationID != "" {
		attrs = append(attrs, "conversation_id", conversationID)
	}
	m.cfg.Logger.Warn("relay: v2 request_model_list refused", attrs...)
	m.modelListReplyError(ctx, s, inReplyTo, rej)
}

// modelListReplyError answers one refusal with a single TypeError envelope
// correlated to inReplyTo. rej supplies a STATIC message and the retryability
// docs/protocol-mobile.md § Error codes publishes for that code; no value derived
// from an error, an id, or a model string ever reaches this path.
//
// Its own helper rather than a call into historyReplyError or settingsReplyError,
// matching the package's stated posture that each reply-owing handler owns its
// error helper — and it must be its own anyway, since those two name their log
// events for other verbs and would misfile every refusal here.
func (m *V2SessionManager) modelListReplyError(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject) {
	body, err := json.Marshal(protocol.ErrorPayload{
		Code:      rej.code,
		Message:   rej.message,
		Retryable: rej.retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 model_list error reply marshal failed",
			"event", "v2.modellist.request.err_marshal",
			"conn_id", s.connID,
			"code", rej.code)
		return
	}
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   body,
		InReplyTo: &inReplyTo,
	}
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 model_list reject dropped; session tearing down",
			"event", "v2.modellist.request.reply_dropped",
			"conn_id", s.connID,
			"code", rej.code)
	}
}
