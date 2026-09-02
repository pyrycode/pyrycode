package main

import (
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
)

// questionActuator is the pair of daemon-side primitives a gated resolution
// delegates to. Declared at the consumer (CODING-STYLE) so the resolver's tests
// can drive it, exactly as streamApprovalResolver declares ResolveStream beside
// modalResolverV2; *streamApprovalBridge is the production implementer of both.
//
// Two methods rather than one, because the two arms carry different payloads and
// neither is derivable from the other: an answer needs the entries to assemble
// claude's verdict, and a refusal needs nothing but the batch id.
//
// NEITHER PRIMITIVE IS SELF-GUARDING, and both say so in their own doc blocks:
// possession of the batch id is treated as sufficient below this seam. What makes
// that safe is this file — the gate sits above both, and nothing else calls them
// from the wire.
type questionActuator interface {
	AnswerQuestion(batchID string, answers []protocol.QuestionAnswerEntry) (consumed bool)
	RefuseQuestion(batchID string) (consumed bool)
}

// classQuestion is the audit ModalClass value marking a record as a
// clarifying-question batch rather than a modal, so a forensic reader can tell the
// two families apart in one field. It is the reason a resolution audits at all
// where retireQuestion deliberately does not: that path reports a dismissal, which
// has no security decision behind it, and this one reports that a device was judged.
//
// The batch id rides audit.Entry's ModalID and this rides its ModalClass — the
// existing prompt-id / prompt-class pair — rather than a question-specific field
// being added to internal/audit. That would be a third production file in this
// slice and would move that package's shared no-leak test, and the two fields
// already carry exactly what a record needs to say WHICH batch and WHAT KIND.
const classQuestion = "question"

// questionResolverV2 is the cmd/pyry implementation of relay.QuestionResolver: it
// is the per-device authorization boundary for an inbound question_answer /
// question_refused, the record of every decision it makes, and nothing else. The
// batch's consume, claude's verdict and the single question_dismissed all belong to
// the primitives behind questionActuator, so this type adds no second broadcaster
// and no second arbiter of whether a batch was consumed.
//
// It is the composition-root binding that lets internal/relay stay free of
// internal/{questionbridge,audit} imports — modalResolverV2's role for the modal
// family, and this file sits beside it for that reason.
//
// Both methods run on the v2 manager's single Run dispatch goroutine (the relay
// calls them from handleQuestionAnswer / handleQuestionRefusal) and both return in
// bounded time: one registry read under the registry's own mutex, one pure
// predicate, one delegated call and at most one audit write. No lock is taken here,
// so this file establishes no lock ordering.
//
// SECURITY: no byte of the batch, the answer or the token is read for any purpose
// other than routing. The looked-up payload is DISCARDED — the resolver never holds
// question text — the answer entries are passed through untouched to the primitive
// that owns every rule about them, and the payload's AnswerToken is never read at
// all. The path emits no log record of its own; the audit entry is its only write.
type questionResolverV2 struct {
	// reg is the daemon-singleton store of surfaced-but-unretired batches (#1975),
	// read ONLY through Lookup. It is a constructor parameter rather than a
	// post-construction assignment because the composition root mints it before the
	// manager, where the bridge below cannot exist yet.
	reg *questionbridge.Registry

	// bridge actuates a gated resolution. Set AFTER CONSTRUCTION at the one
	// production site (relay.go), the shape modalResolverV2.streamApprovals uses
	// and for the same reason: the bridge is built after the manager because the
	// manager is its broadcaster, and the manager reads this resolver out of the
	// config it was constructed with.
	//
	// nil ⇒ BOTH ARMS ARE INERT, which is the whole of AC-5: on a daemon with no
	// stream-approval bridge (foreground / PTY) there is nothing to resolve with,
	// and a method call on a nil interface field would panic rather than no-op.
	bridge questionActuator

	logger *slog.Logger
}

// newQuestionResolverV2 wires the resolver to the daemon-singleton batch registry
// — the same instance the surfacer Records into and the connect-time reconcile
// Snapshots — and the daemon logger. The actuator is a nil-default field set at the
// production site, NOT a constructor param, for the reason its own doc block gives.
func newQuestionResolverV2(reg *questionbridge.Registry, logger *slog.Logger) *questionResolverV2 {
	return &questionResolverV2{reg: reg, logger: logger}
}

// ResolveAnswer resolves an inbound question_answer: an operator's selections for a
// batch claude is blocked on. It reports whether it consumed an outstanding batch —
// a diagnostic for the relay handler's log record, never a broadcast trigger
// (relay.QuestionResolver's doc block).
//
// THE ORDER IS Lookup → gate → conjunction → consume, and it is the same ordering
// modalResolverV2.ResolveAnswer keeps for the same reason. Gating after the consume
// would burn the batch on an ineligible device's frame and leave claude blocked
// until the approval window elapsed, with the no-answer backstop having already
// lost the one-shot and so broadcasting nothing.
//
//  1. NO ACTUATOR ⇒ INERT, before anything else, so no registry content can route
//     around it and the nil interface field is never called.
//  2. LOOKUP, NEVER Resolve. A miss — an unknown batch, or one an earlier
//     resolution or the backstop already consumed — returns false with no verdict,
//     no broadcast and NO AUDIT: no security decision was made. That covers the
//     empty batch id a `null` payload decodes to, which the relay handler
//     deliberately forwards rather than judging itself. The payload is discarded.
//  3. FAIL-CLOSED ELIGIBILITY GATE. A nil device (no authenticated device on the
//     connection), an unauthenticated one and one whose opt-in bit is unset all
//     deny. The denial is audited denied_unauthorized with the (possibly empty)
//     non-secret identity, and the batch is LEFT OUTSTANDING — still answerable by
//     a legitimate device, and still covered by the no-answer backstop.
//  4. THE ALLOW CONJUNCTION, defence in depth. AuthorizeRemotePermission re-checks
//     eligibility alongside the outcome, which keeps the fail-closed conjunction in
//     its single unit-tested place; for a device that passed step 3 it reduces to
//     true, and it is the call that would keep denying correctly if that step were
//     ever moved. modalResolverV2.ResolveAnswer calls it at the same point for the
//     same reason.
//  5. DELEGATE. AnswerQuestion owns the consume, the verdict and the single
//     dismissal. A false return — the one-shot lost to a concurrent retire or
//     refusal, permbridge having resolved on its own timer, or entries it rejected
//     — is inert here for step 2's reason: nothing was consumed and nothing was
//     decided, so nothing is recorded. classifyAnswer's forged-option arm is the
//     in-tree precedent for exactly that shape.
//
// The entries are NOT examined here. Count, index range, duplicates and per-question
// shape are all answerVerdict's, which checks the count first so an arbitrarily long
// array is O(1) to reject and range-checks every index before the subscript that
// would otherwise panic. A second copy of those rules above the gate would be a
// second bound to keep in agreement with the batch.
//
// The check-then-act between the Lookup and the delegate is deliberately not atomic
// and is safe in both directions, AnswerQuestion's own argument verbatim: the
// registry one-shot is the single arbiter, so a concurrent retire or refusal either
// wins it (this returns false having resolved nothing) or loses it (it broadcasts
// nothing). The exploitable ordering is the reverse one, consume-then-gate.
//
// p.AnswerToken is the client's idempotency key and is NEVER READ — not compared,
// not stored, not logged. The daemon's dedup is the batch one-shot, and
// modalResolverV2.ResolveAnswer records why a server-side token store is
// deliberately not built: it would re-broadcast a prior result, breaking the
// single-dismissal property, while adding unbounded state.
func (r *questionResolverV2) ResolveAnswer(p protocol.QuestionAnswerPayload, dev *devices.Device) bool {
	if !r.admit(p.QuestionBatchID, dev) {
		return false
	}

	// Step 4. Unreachable given the gate above passed — both spell the same
	// fail-closed predicate for an allow outcome — and kept because the primitive
	// is where that conjunction is unit-tested, so a later edit to the gate cannot
	// silently turn this into an ungated allow.
	if !devices.AuthorizeRemotePermission(dev, devices.OutcomeAllow) {
		r.auditQuestion(dev, p.QuestionBatchID, audit.OutcomeDeniedUnauthorized)
		return false
	}

	if !r.bridge.AnswerQuestion(p.QuestionBatchID, p.Answers) {
		return false
	}

	r.auditQuestion(dev, p.QuestionBatchID, audit.OutcomeAllowed)
	return true
}

// ResolveRefusal resolves an inbound question_refused: the operator declined to
// choose, so the batch resolves with no selection and claude is denied with the
// daemon's own instruction to wait. Same comma-ok-shaped report and same no-op
// posture as ResolveAnswer, and the same admit ordering — read that method's doc
// block for the full argument; only the differences are stated here.
//
// IT HAS NO ALLOW CONJUNCTION, and that absence is deliberate rather than an
// omission. AuthorizeRemotePermission reads as "allow the tool call", so it is
// false for an ELIGIBLE device refusing — the intended outcome, not a gate failure
// — which makes it useless as a check on this arm. The eligibility gate inside
// admit is the same fail-closed predicate on both arms; only the conjunction, which
// exists to guard an allow, is answer-specific.
//
// A REFUSAL BY AN AUTHORISED OPERATOR AUDITS AS denied, NOT denied_unauthorized.
// The two are the difference this record exists to preserve: one says the operator
// decided, the other says the daemon refused to let a device decide. audit.Outcome
// already publishes exactly that split.
func (r *questionResolverV2) ResolveRefusal(p protocol.QuestionRefusedPayload, dev *devices.Device) bool {
	if !r.admit(p.QuestionBatchID, dev) {
		return false
	}

	if !r.bridge.RefuseQuestion(p.QuestionBatchID) {
		return false
	}

	r.auditQuestion(dev, p.QuestionBatchID, audit.OutcomeDenied)
	return true
}

// admit is steps 1-3 of both arms — no actuator, no outstanding batch, no eligible
// device — reporting whether the caller may proceed to actuation. It lives in one
// place so the two arms cannot drift and a later third arm inherits the ordering
// rather than re-deriving it; the pieces that are genuinely arm-specific (the allow
// conjunction, the delegate, the recorded outcome) stay in the arms.
//
// It writes the denied_unauthorized record itself, because that record IS the
// decision it makes: separating "deny" from "record the denial" across two
// functions is how a later edit ends up with one and not the other. The
// unknown-batch miss above it deliberately records nothing — no security decision
// was made there.
//
// The looked-up payload is discarded rather than returned: no caller needs the
// batch, and handing one back would put claude-authored question text in reach of a
// path whose whole discipline is that it holds none.
func (r *questionResolverV2) admit(batchID string, dev *devices.Device) bool {
	if r.bridge == nil {
		return false // foreground / PTY: nothing to resolve with, and nothing to decide
	}
	if _, ok := r.reg.Lookup(batchID); !ok {
		return false // unknown or already-resolved — no security decision, no record
	}
	if !dev.MayAnswerRemotePermission() {
		r.auditQuestion(dev, batchID, audit.OutcomeDeniedUnauthorized)
		return false
	}
	return true
}

// auditQuestion writes exactly one terminal-decision record for a question
// resolution, carrying only the non-secret device identity (empty for a nil device
// — auditAnswer's pattern, which this is the question-family twin of). The batch id
// rides ModalID and classQuestion rides ModalClass; source is always remote,
// because every frame this file judges arrived from a client.
//
// SECURITY: audit.Entry has no field that can hold a plain device token, a
// question, an answer value or an option label, and this call supplies none — the
// four values are the batch id, a compile-time class constant, the decided outcome
// and the fixed source.
func (r *questionResolverV2) auditQuestion(dev *devices.Device, batchID string, outcome audit.Outcome) {
	var deviceHash, deviceLabel string
	if dev != nil {
		deviceHash = dev.TokenHash
		deviceLabel = dev.Name
	}
	audit.Log(r.logger, audit.Entry{
		DeviceHash:  deviceHash,
		DeviceLabel: deviceLabel,
		ModalID:     batchID,
		ModalClass:  classQuestion,
		Outcome:     outcome,
		Source:      audit.SourceRemote,
	})
}
