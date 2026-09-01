package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the connect-time question-batch reconcile (#1979) — the fourth
// Mode B instance after reconcileModals (#877), reconcileQueues (#878) and
// reconcileModelLists (#1863). Its own file for the reason the model-list reconcile
// gives for its own: the test files were already one-per-reconcile, and a
// question reconcile living in a modal-named file is a readability hazard, the more
// so here because question_shown and modal_shown are deliberately separate frame
// families (#1962) that a shared file would invite conflating.

// reconcileQuestions unicasts the currently-outstanding clarifying-question batches
// to a freshly interactive-open conn (#1979) — the question twin of
// reconcileModals / reconcileQueues / reconcileModelLists. question_shown reaches a
// live conn only on the raise-time broadcast (#1973), which by construction reaches
// whoever is connected at the instant claude asks and no one else: the frame
// carries no event id, so it is not in the #647 turn-event replay ring, and there
// is no other path to it. A client that connects or reconnects afterwards therefore
// never learns the ask exists.
//
// The miss costs more than a plain miss, for the reason reconcileModals' doc block
// records: the daemon counts an approval answerable while ANY interactive conn is
// open, so a reconnected client that was never sent the batch re-arms the window at
// every expiry while being structurally unable to answer it. An outstanding batch
// is control state, which docs/protocol-mobile.md § Reconnect / Backfill semantics
// binds to a connect-time snapshot rather than a cursor backfill — the mechanism is
// chosen by the data class, not by how long the client was away.
//
// Run-goroutine only (called from handleNoiseInit's success tail), so
// s.interactive / s.connID are read lock-free under the package's single-owner
// invariant — that guarantee is the caller's, not this function's. It reaches no
// cmd/pyry producer state: the payloads come from a pure daemon-side read and the
// envelope ID is non-load-bearing (the client correlates on question_batch_id), so
// the send is entirely relay-side with a fixed ID — no cross-goroutine coupling, no
// lock added. m.Push takes pushMu internally and is the only lock on the path.
//
// EventID left nil is load-bearing twice, as in the three twins: it keeps the frame
// out of the turn-event replay ring (so this path adds no per-conversation ring
// memory), and it makes forwardEnvelope's last_event_id dedup inert for the frame.
// No turn is opened either, because nothing here touches turn state: the frame goes
// straight onto the conn's push queue via Push, never through forwardEnvelope, and
// TypeQuestionShown is not a turn-boundary type.
//
// Pure read, in both directions. The seam mints no nonce and retires no batch, so
// re-connecting re-sends the same set idempotently and answerability is unchanged —
// a re-sent question_batch_id stays answerable exactly once, governed by the
// registry's one-shot Resolve, which this path never calls. This path also mutates
// nothing it read: it marshals each payload and never writes through it.
//
// This path applies no bound of its own; the payloads arrive already bounded at
// parse time. See OutstandingQuestions, which also records why the per-batch bound
// does not bound the aggregate and where a cap would belong if one were wanted.
//
// SECURITY: a Question's Text and Header and a QuestionOption's Label and
// Description are claude-authored, untrusted text that crossed the subprocess trust
// boundary, and they are NEVER logged (#833). They are also forwarded UNSANITISED —
// no control-character or terminal-escape stripping happens here — which is
// Question's own documented decision and not an omission: the render boundary owing
// the sanitisation is the client's, and a silent transform on this path would
// present altered text to a client as claude's own. The marshal branch carries only
// content-free discriminants (event, conn_id, question_batch_id, conversation_id —
// non-secret correlation ids that already cross the wire, the posture
// reconcileModals keeps for modal_id) and deliberately does NOT echo err, because
// encoding/json quotes the input bytes and a question's text would land in the
// record. The push branch may echo err only because Push returns ctx.Err() or
// ErrConnNotFound and nothing else — an inherited contract, named here so a later
// change to Push's error values is visibly load-bearing. The success path logs
// nothing at all: a connect-time reconcile fires on every handshake, the
// routine-read cadence handleRequestSessionSettings cites when it logs conn_id and
// nothing else.
func (m *V2SessionManager) reconcileQuestions(ctx context.Context, s *V2Session) {
	// Capability gate (AC2) + the unwired/foreground opt-out. Mirrors the three
	// twins: s.interactive is the negotiated flag, a nil seam is the nil-resolver
	// posture the other optional control seams share.
	if !s.interactive || m.cfg.OutstandingQuestions == nil {
		return
	}
	outstanding := m.cfg.OutstandingQuestions()
	if len(outstanding) == 0 {
		return // nothing outstanding ⇒ nothing sent (AC2).
	}
	// One timestamp shared by the batch (matches the three twins).
	ts := time.Now().UTC()
	for _, p := range outstanding {
		payload, err := json.Marshal(p)
		if err != nil {
			// QuestionShownPayload is a closed struct of two strings + []Question,
			// Question is two strings + []QuestionOption + a bool, QuestionOption is two
			// strings, and both custom MarshalJSONs delegate to json.Marshal over those
			// closed types, so marshal cannot fail — no test can redden this. Defensive
			// only: NEVER echo err or the payload (either would quote a question's text).
			// Skip this one, keep sending the rest.
			m.cfg.Logger.Warn("relay: v2 question_shown reconcile marshal failed",
				"event", "v2.question.reconcile.marshal_err",
				"conn_id", s.connID,
				"question_batch_id", p.QuestionBatchID,
				"conversation_id", p.ConversationID)
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the client correlates on question_batch_id.
			Type:    protocol.TypeQuestionShown,
			TS:      ts,
			Payload: payload,
			// EventID left nil: a control snapshot, never part of the turn-event
			// replay ring (forwardEnvelope's dedup is inert for EventID == nil).
		}
		if err := m.Push(ctx, s.connID, env); err != nil {
			// ctx teardown ⇒ stop (the session is going away and the remaining
			// payloads have nowhere to land); any other sentinel (ErrConnNotFound is
			// unreachable — the push queue was created a few statements earlier in
			// this same handleNoiseInit on this same goroutine, which is why this is
			// Debug rather than Warn) ⇒ skip and continue. NEVER echo payload bytes.
			m.cfg.Logger.Debug("relay: v2 question_shown reconcile push dropped",
				"event", "v2.question.reconcile.push_err",
				"conn_id", s.connID,
				"err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}
