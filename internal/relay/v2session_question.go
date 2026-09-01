package relay

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound question-control interception (#1984): the two
// handlers behind dispatchAppFrame's TypeQuestionAnswer / TypeQuestionRefused
// cases. It sits beside v2session_modal.go because the two families share a
// discipline, not an implementation — the QuestionResolver seam and its
// V2SessionConfig field live in v2session_seams.go with the other seams, and the
// outbound half of the question family (reconcileQuestions, #1979) is in
// v2session_questionreconcile.go.
//
// WHAT THIS SLICE DOES AND DOES NOT DO. It intercepts, decodes and hands off.
// It resolves nothing (#1985 does) and it broadcasts nothing: question_dismissed
// has a single broadcaster on the cmd/pyry side and a second one here would be a
// second arbiter of whether a batch was consumed.
//
// The interception is the whole observable change even with nothing wired behind
// the seam. Before it, neither type had a case in dispatchAppFrame's control
// switch — which has no default arm — so both fell through to dispatch.Route,
// where IsKnownAppType rejects them and the client received a sealed unknown-type
// error reply. Both are now consumed whether or not a resolver is wired, so that
// reply is gone.

// handleQuestionAnswer routes an inbound question_answer control frame to the
// QuestionResolver seam. Intercepted in dispatchAppFrame before dispatch.Route,
// exactly like handleModalAnswer, and runs on the manager's single Run dispatch
// goroutine — so the s.device / s.connID reads are lock-free under the package's
// single-owner invariant. There is no reply to the caller and no broadcast:
// question control is fire-and-forget here.
//
// The signature takes (s, env) and no ctx, mirroring handleDequeueMessage's
// established deviation from the (ctx, s, env) siblings: there is no Push, no
// forwardEnvelope and no other cancellable work, because the handler emits
// nothing.
//
// Order is load-bearing:
//
//  1. A nil QuestionResolver (foreground / v1 / pre-#1985) makes the frame INERT —
//     but still consumed. Mirrors handleModalCancel's nil guard, and buys a
//     property the modal one does not have: an unwired daemon performs zero
//     parsing of remote-authored bytes.
//  2. A DECODE FAILURE IS REJECTED, NOT TOLERATED, and this is the one place the
//     modal handlers are deliberately not copied. They do `_ = json.Unmarshal(...)`
//     and let a failure fall through as an empty modal_id, which is harmless for a
//     payload of flat strings. QuestionAnswerPayload carries a nested answers
//     array, and encoding/json populates the fields it read BEFORE the one that
//     failed — so tolerating the error can hand the seam a valid batch id with nil
//     answers, exactly the empty-but-successful answer QuestionAnswerPayload's own
//     doc block forbids. Rejecting means the seam is not called at all.
//  3. A payload that decodes CLEANLY but names an unknown batch is not this
//     handler's to judge: it goes to the seam, whose implementer stays the sole
//     arbiter. That covers a `null` payload and an all-empty object, both of which
//     decode without error into a zero-value payload — an empty batch id is an
//     unknown batch, not a decode failure, and rejecting it here would install a
//     second arbiter.
//
// NO AUTHORIZATION HAPPENS HERE — no interactive capability check and no
// per-device check — matching handleModalAnswer, which applies neither and leaves
// both to the resolver so the decision lives in one place. The connection's device
// is passed through and the handler stays free of the decision. What makes that
// fail-safe today is structural rather than argued: the seam is nil at every
// construction site, so no answer reaches an actuator, and #1986 installs the
// per-device gate before anything is wired (QuestionResolver's doc block carries
// that ordering obligation).
//
// SECURITY: the decode error is NEVER wrapped, logged or replied. encoding/json
// quotes offending input into its error string, those bytes are remote-authored,
// and nothing on this path strips terminal escape sequences —
// QuestionAnswerPayload's doc block states the rule and this is the handler it was
// written for. The reject record therefore carries no batch id either: the decode
// is what failed, so a partially-populated id would attribute refused bytes to a
// batch. On the paths where it IS trustworthy, question_batch_id is logged — that
// same doc block marks it and answer_token explicitly safe to log, so this handler
// neither invents a redaction rule nor assumes one exists. No answer value and no
// raw payload byte appears on any path.
func (m *V2SessionManager) handleQuestionAnswer(s *V2Session, env protocol.Envelope) {
	if m.cfg.QuestionResolver == nil {
		m.cfg.Logger.Debug("relay: v2 question_answer inert; no resolver wired",
			"event", "v2.question.answer.inert",
			"conn_id", s.connID)
		return
	}
	var p protocol.QuestionAnswerPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		// Rejected, not tolerated (see step 2). NEVER echo err or any payload
		// byte — not to the phone, not into this record.
		m.cfg.Logger.Warn("relay: v2 question_answer rejected; payload did not decode",
			"event", "v2.question.answer.decode_err",
			"conn_id", s.connID)
		return
	}

	if m.cfg.QuestionResolver.ResolveAnswer(p, s.device) {
		m.cfg.Logger.Info("relay: v2 question_answer resolved",
			"event", "v2.question.answer.resolved",
			"conn_id", s.connID,
			"question_batch_id", p.QuestionBatchID)
		return
	}
	// false ⇒ success of a valid request: an unknown or already-resolved batch.
	// Nothing changed, so emitting nothing is correct — no reply, no broadcast.
	// The discriminant is a diagnostic only; it MUST NOT drive a question_dismissed
	// (see QuestionResolver's doc block).
	m.cfg.Logger.Debug("relay: v2 question_answer no-op",
		"event", "v2.question.answer.noop",
		"conn_id", s.connID,
		"question_batch_id", p.QuestionBatchID)
}

// handleQuestionRefusal routes an inbound question_refused control frame through
// the same QuestionResolver seam — the operator declined to choose, so the batch
// resolves with no selection. Its own type rather than an answer with an empty
// array, mirroring modal_cancel beside modal_answer, so this routes on the frame's
// name rather than on a value's shape.
//
// See handleQuestionAnswer for the nil-resolver / reject-on-decode / never-echo /
// no-authorization discipline the two share in full. QuestionRefusedPayload
// carries no array, so the partial-population hazard is narrower here — but the
// rule is the frame family's, not the shape's, and one tolerant sibling would be
// the exception a later reader copies.
func (m *V2SessionManager) handleQuestionRefusal(s *V2Session, env protocol.Envelope) {
	if m.cfg.QuestionResolver == nil {
		m.cfg.Logger.Debug("relay: v2 question_refused inert; no resolver wired",
			"event", "v2.question.refusal.inert",
			"conn_id", s.connID)
		return
	}
	var p protocol.QuestionRefusedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		m.cfg.Logger.Warn("relay: v2 question_refused rejected; payload did not decode",
			"event", "v2.question.refusal.decode_err",
			"conn_id", s.connID)
		return
	}

	if m.cfg.QuestionResolver.ResolveRefusal(p, s.device) {
		m.cfg.Logger.Info("relay: v2 question_refused resolved",
			"event", "v2.question.refusal.resolved",
			"conn_id", s.connID,
			"question_batch_id", p.QuestionBatchID)
		return
	}
	m.cfg.Logger.Debug("relay: v2 question_refused no-op",
		"event", "v2.question.refusal.noop",
		"conn_id", s.connID,
		"question_batch_id", p.QuestionBatchID)
}
