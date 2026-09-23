package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// modalResolverV2 is the cmd/pyry implementation of relay.ModalResolver: it
// consumes an outstanding modal from the daemon-singleton registry, actuates the
// resolution as a verdict on claude's parked stream-json approval, and writes the
// forensic audit record. It is the composition-root binding that lets
// internal/relay stay free of internal/{modalbridge,permbridge,audit} imports.
//
// Both methods run on the v2 manager's single Run dispatch goroutine (the relay
// calls them from dispatchAppFrame). The registry's own mutex is the only
// synchronisation; the stream-approval bridge and audit sink are themselves safe
// to call from any goroutine.
//
// SECURITY: no modal body/prompt/title and no payload bytes are ever logged; the
// audit entry carries only non-secret identity (device hash/label) + the opaque
// modal_id + outcome/source.
type modalResolverV2 struct {
	reg    *modalbridge.Registry
	logger *slog.Logger

	// streamApprovals resolves a stream-json permission approval (a
	// permbridge-parked completer, #1103) by modalID: the resolver's only
	// actuation. A modalID it does not handle — or every modalID when it is nil —
	// actuates nothing and is Warn-logged (#1546). Set after construction at the
	// production site (relay.go), once the bridge exists; most test constructions
	// leave it nil. #1080.
	streamApprovals streamApprovalResolver
}

// streamApprovalResolver reports whether a stream-json approval may be answered
// remotely, then resolves an eligible modalID to an allow/deny verdict on
// claude's parked completer. handled=false means modalID is not a stream approval,
// so the resolution actuates nothing. *streamApprovalBridge is the
// production implementer; the interface is declared at its consumer.
type streamApprovalResolver interface {
	RemoteAnswerable(modalID string) bool
	ResolveStream(modalID string, allow, alwaysAllow bool, denyReason string) (handled bool)
}

type streamModalRegistry interface {
	RecordWithContext(turnevent.PermissionRequest, string, string, modalbridge.PermissionContext) (protocol.ModalShownPayload, error)
	Resolve(string) (modalbridge.Outstanding, bool)
}

// reasonRemoteDeny is the fixed, content-free deny message a remote reject answer
// returns to claude on the stream-json path (#1080). A compile-time constant —
// never host-derived — so it leaks nothing back to claude, mirroring
// permbridge's own reasonTimeout.
const reasonRemoteDeny = "permission denied"

// newModalResolverV2 wires the resolver to the daemon-singleton outstanding-modal
// registry (the same instance #708 live-wires the producer/emitter into) and
// the daemon logger. streamApprovals is a
// nil-default field set at the production site, NOT a constructor param — the
// constructor has many test call sites, and nil disables the optional seam.
//
// The #1014 folder-not-trusted emit seams were removed deliberately (#1545): a
// trust modal only ever reached the registry through the terminal producer
// #1348 deleted, and trust is now settled by trustMark before every spawn, so
// no answer here can be a trust deny.
func newModalResolverV2(reg *modalbridge.Registry, logger *slog.Logger) *modalResolverV2 {
	return &modalResolverV2{reg: reg, logger: logger}
}

// ResolveCancel consumes the named modal and denies its parked stream approval.
// The registry Resolve is the single idempotency gate (AC #4): an unknown or
// already-consumed id returns (zero, false) before any actuation or audit, so a
// replayed/stale cancel never double-acts. A consumed modal that no stream
// approval handles actuates nothing and is Warn-logged, but the phone must still
// learn the dismissal (broadcast) and the forensic record must still exist
// (audit).
//
// The deny arm exists because a stream-json permission is a permbridge-parked
// completer, not an on-screen modal (#2416): without it a cancelled stream
// permission left claude blocked until permbridge's own timer fired,
// mcpApprovalTimeout at ten minutes.
//
// TWO GATES THE ANSWER ARM APPLIES ARE DELIBERATELY ABSENT HERE, and both look
// like omissions. RemoteAnswerable exists because an interaction-required
// permission cannot be ALLOWED by a one-tap remote answer; a cancel is only ever a
// deny, the fail-closed direction, so gating it would preserve exactly the
// parked-for-ten-minutes state this arm removes. And no per-device privilege is
// checked, as it never has been on this path: a cancel was a deny to claude on
// the deleted terminal path too (its escape key), so the stream path was brought
// level with it rather than granted anything new. reasonRemoteDeny is a compile-time constant,
// so nothing a client or the host authored reaches claude.
func (r *modalResolverV2) ResolveCancel(modalID string, dev *devices.Device) (relay.ModalDismissal, bool) {
	out, ok := r.reg.Resolve(modalID)
	if !ok {
		return relay.ModalDismissal{}, false
	}

	// STREAM (#2416): a permbridge-parked approval keyed by modalID resolves its
	// completer to deny. handled=true only when modalID is a stream approval. The
	// registry's only producer is the bridge's Surface, so a miss has never been
	// observed; it is logged, not defended (#1546). The modal is already consumed,
	// so the audit and dismissal below still happen — aborting would orphan it.
	handled := r.streamApprovals != nil && r.streamApprovals.ResolveStream(modalID, false, false, reasonRemoteDeny)
	if !handled {
		r.logUnrouted("relay: modal cancel reached no stream approval", "modal_cancel.unrouted", modalID)
	}

	var deviceHash, deviceLabel string
	if dev != nil {
		deviceHash = dev.TokenHash
		deviceLabel = dev.Name
	}
	audit.Log(r.logger, audit.Entry{
		DeviceHash:  deviceHash,
		DeviceLabel: deviceLabel,
		ModalID:     modalID,
		ModalClass:  out.Class,
		Outcome:     audit.OutcomeCancelled,
		Source:      audit.SourceRemote,
	})

	// One source vocabulary feeds both the wire dismissal and the audit entry
	// (audit.go's documented contract).
	return relay.ModalDismissal{
		Outcome: string(audit.OutcomeCancelled),
		Source:  string(audit.SourceRemote),
	}, true
}

// ResolveAnswer is the security-critical gated answer arm: it routes an
// internet-sourced modal_answer into claude's permission prompt ONLY from a
// gated device. The hard invariant is that nothing but a fully-authorized, valid
// answer may consume the modal or reach the parked approval — so the order is
// Lookup → fail-closed eligibility gate → option classification → consume →
// verdict → audit (gate before consume).
//
// answerToken is the client's idempotency key (uniqueness matters, secrecy does
// not — it is NOT authorization). The daemon's dedup is the modal_id one-shot
// Resolve below, which already collapses a replay to the unknown-id no-op (no
// second dismissal — exactly what AC #2 demands). A server-side token store that
// re-broadcast the prior result would VIOLATE "no second dismissal" and add
// unbounded state, so it is deliberately not built. The token is decoded (it
// arrives as a param) and otherwise unused, and never logged.
func (r *modalResolverV2) ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (relay.ModalDismissal, bool) {
	return r.ResolveAnswerWithAlwaysAllow(modalID, optionID, answerToken, false, dev)
}

// ResolveAnswerWithAlwaysAllow carries the optional session-grant choice from
// the v2 payload. ResolveAnswer remains the false/absent compatibility entry
// point for internal callers that do not supply the additive field.
func (r *modalResolverV2) ResolveAnswerWithAlwaysAllow(modalID, optionID, answerToken string, alwaysAllow bool, dev *devices.Device) (relay.ModalDismissal, bool) {
	_ = answerToken // see method doc: decoded, not used server-side, never logged.

	// Step 1: Lookup (read, no consume). Stale / unknown / already-resolved (a
	// replay or reorder of an answer whose modal_id was already consumed) all
	// miss here ⇒ no verdict, no mutation, no audit. This is AC #2's
	// idempotency / first-answer-wins.
	out, ok := r.reg.Lookup(modalID)
	if !ok {
		return relay.ModalDismissal{}, false
	}

	// Step 2: fail-closed eligibility gate, BEFORE classification and BEFORE
	// consume — the load-bearing ordering. A nil/unauthenticated device or an
	// unset opt-in bit denies; the modal is left outstanding (Lookup only) for a
	// legitimate local answer or the permission bridge's deny-on-timeout (#1103). Audited
	// denied_unauthorized with the (possibly empty) non-secret identity.
	if !dev.MayAnswerRemotePermission() {
		r.auditAnswer(dev, modalID, out.Class, audit.OutcomeDeniedUnauthorized)
		return relay.ModalDismissal{}, false
	}

	// Claude may mark a stdio permission ask as requiring tool-specific user
	// interaction that a one-tap remote modal cannot supply. This eligibility
	// check must precede classification and the modal one-shot: both allow and
	// deny options leave the modal and parked approval untouched for their
	// existing fail-closed timeout. A non-stream modal remains answerable.
	if r.streamApprovals != nil && !r.streamApprovals.RemoteAnswerable(modalID) {
		return relay.ModalDismissal{}, false
	}

	// Step 4: map option_id → outcome against THIS modal's surfaced options. A
	// forged or wrong-class option_id is not a locatable option ⇒ reject with no
	// verdict, no consume, no audit (no security decision was
	// made; it is a malformed client frame). Warn-logged with a length-bounded
	// option_id (it is attacker-controlled; slog JSON-escapes it).
	outcome, ok := classifyAnswer(out, optionID)
	if !ok {
		r.logger.Warn("relay: modal answer invalid option",
			"event", "modal_answer.invalid_option",
			"modal_id", modalID,
			"option_id", truncateForLog(optionID, 64))
		return relay.ModalDismissal{}, false
	}

	// Step 5/6: consume FIRST (commit idempotency), then actuate. The
	// defensive Resolve-miss (modal vanished between Lookup and Resolve) is
	// unreachable in practice — resolutions are serialized on the manager's Run
	// goroutine and the producer only adds — but is handled as a row-1 no-op.
	if _, ok := r.reg.Resolve(modalID); !ok {
		return relay.ModalDismissal{}, false
	}

	// AuthorizeRemotePermission (#702) splits allowed (true) from denied (false).
	// For an eligible device this reduces to outcome==OutcomeAllow, but calling
	// the primitive keeps the fail-closed conjunction in its single unit-tested
	// place (it re-checks eligibility — defense in depth). Computed once here: it
	// drives BOTH the stream verdict dispatch and the audit classification below.
	allow := devices.AuthorizeRemotePermission(dev, outcome)

	// Actuate the answer exactly like ResolveCancel (#1080): a permbridge-parked
	// approval keyed by modalID resolves its completer to allow/deny (echoing the
	// parked tool input on allow). ResolveStream reports handled=true only when
	// modalID is a stream approval; a miss actuates nothing and is Warn-logged
	// (#1546). The modal is already consumed, so the audit and dismissal still
	// happen — aborting would orphan it.
	handled := r.streamApprovals != nil && r.streamApprovals.ResolveStream(modalID, allow, alwaysAllow, reasonRemoteDeny)
	if !handled {
		r.logUnrouted("relay: modal answer reached no stream approval", "modal_answer.unrouted", modalID)
	}

	decision := audit.OutcomeDenied
	if allow {
		decision = audit.OutcomeAllowed
	}
	r.auditAnswer(dev, modalID, out.Class, decision)

	// The WIRE dismissal Outcome is the answered option_id (ModalDismissedPayload
	// contract), NOT the audit classification. Source is remote.
	return relay.ModalDismissal{Outcome: optionID, Source: string(audit.SourceRemote)}, true
}

// classifyAnswer locates optionID within o.Options and maps it to the grant
// outcome. ok=false if optionID is not a locatable option of THIS modal, or is
// not one of the four permission kinds (forged / unknown id, including the trust
// class's proceed/exit, which #1545 removed: trust is settled before spawn) —
// the caller rejects with no verdict, no consume, no audit. Membership in the
// surfaced o.Options is the gate: an id the modal never offered is rejected even
// when it names a valid kind.
func classifyAnswer(o modalbridge.Outstanding, optionID string) (outcome devices.RemotePermissionOutcome, ok bool) {
	if !slices.ContainsFunc(o.Options, func(opt protocol.ModalOption) bool { return opt.ID == optionID }) {
		return 0, false
	}
	switch optionID {
	case string(turnevent.PermissionOptionKindAllowOnce), string(turnevent.PermissionOptionKindAllowAlways):
		return devices.OutcomeAllow, true
	case string(turnevent.PermissionOptionKindRejectOnce), string(turnevent.PermissionOptionKindRejectAlways):
		return devices.OutcomeDeny, true
	default:
		return 0, false
	}
}

// logUnrouted records a consumed modal whose resolution no stream approval
// handled. SECURITY: it carries only the opaque modal_id — no modal body,
// prompt, title or option text.
func (r *modalResolverV2) logUnrouted(msg, event, modalID string) {
	r.logger.Warn(msg, "event", event, "modal_id", modalID)
}

// auditAnswer writes exactly one terminal-decision audit record for the answer
// arm, carrying only the non-secret device identity (empty for a nil device —
// the ResolveCancel pattern). modal_class comes from the step-1 Lookup; source
// is always remote.
func (r *modalResolverV2) auditAnswer(dev *devices.Device, modalID, class string, outcome audit.Outcome) {
	var deviceHash, deviceLabel string
	if dev != nil {
		deviceHash = dev.TokenHash
		deviceLabel = dev.Name
	}
	audit.Log(r.logger, audit.Entry{
		DeviceHash:  deviceHash,
		DeviceLabel: deviceLabel,
		ModalID:     modalID,
		ModalClass:  class,
		Outcome:     outcome,
		Source:      audit.SourceRemote,
	})
}

// truncateForLog bounds an attacker-controlled string to n bytes before it is
// logged: slog JSON-escapes the value (no log-injection) and the payload is
// already capped by the transport AEAD frame, so this only stops a hostile gated
// device from padding a field to bloat the log. Backs up to a rune boundary so a
// multi-byte rune is never split (mirrors modalbridge.boundPrompt).
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// streamApprovalBridge joins the two composition scopes a stream-json permission
// prompt straddles (#1080): the claude-facing permbridge parked-approval store
// (keyed by claude's tool_use_id, #1103) and the client-facing modalbridge
// outstanding-modal store (keyed by a minted modal_id nonce, #716). It owns the
// modal_id ⇄ tool_use_id correlation neither registry holds and joins both
// directions:
//
//   - Surface (control-server handler goroutine): a parked approval becomes the
//     SAME permission modal_shown clients already answer, minted via modalbridge
//     so the 4-option / reject-once-default payload is byte-compatible by
//     construction (AC-1). Returns a retire closure the control server defers.
//     Since #1973 it first asks questionbridge whether the approval is claude's
//     clarifying-question batch, which surfaces as a question_shown instead and
//     retires as a question_dismissed — a different frame family, a different
//     registry and a different correlation map, joined here only because claude
//     parks both through the same mcp-approve bridge.
//   - ResolveStream (relay Run goroutine, via modalResolverV2.ResolveAnswer): a
//     client's modal_answer resolves the parked completer to allow/deny (AC-2).
//   - retire (control-server handler goroutine, post-Await): the guaranteed
//     cleanup + client-dismissal backstop for every terminal path (AC-3).
//
// That correlation exists for exactly as long as a request is parked on a person,
// which makes it the daemon's direct answer to "is this conversation waiting on a
// human?" — the question the delivery hold otherwise has to infer from elapsed
// silence. ApprovalParked reads it (any goroutine, #1919).
//
// SECURITY: the bridge NEVER logs req.Input, the tool_name, the modal
// prompt/title, or a deny message beyond the fixed reasonRemoteDeny constant.
// Only content-free discriminants (event, modal_id, conn_id, env_id, class) and
// the transport-sentinel Push err reach any log field — the same discipline the
// modal emitter and control server hold.
type streamApprovalBridge struct {
	perm       *permbridge.Registry   // claude-facing completer store (#1103)
	modal      streamModalRegistry    // client-facing modal store (#716)
	bcast      interactiveBroadcaster // *relay.V2SessionManager (ActiveConns/Push)
	activeConv func() string          // follow-active convID scoping (#1065)
	ctx        context.Context        // daemon ctx captured at construction, for broadcasts
	logger     *slog.Logger

	// toolCallInFlight answers turnBusyTracker's conversation-keyed membership
	// question (ToolCallInFlight, #1917) — "is this tool call in flight on this
	// conversation?" — which is the half of ApprovalParked the correlation below
	// cannot supply. It is SET AFTER CONSTRUCTION at the one production site
	// (relay.go), not taken as a seventh constructor parameter: the constructor has
	// 14 call sites and the resolver's streamApprovals field is the in-tree
	// precedent for keeping an optional dependency's blast radius at one site.
	//
	// nil ⇒ ApprovalParked reports negative for EVERY conversation, without calling
	// out. The bridge and the tracker have different non-nil discriminants — the
	// bridge is built under a non-nil approvals registry, which the composition root
	// mints unconditionally, while the tracker exists only alongside streamSink — so
	// in the daemon's PTY mode the bridge is live and holds no tracker, and
	// answering negative there is what leaves that mode semantically unchanged.
	//
	// That short-circuit lives HERE, deliberately, and is not a nil-receiver guard
	// on ToolCallInFlight: #1917 refused that guard on the record because it "would
	// be the first step toward a consumer silently reading false in PTY mode instead
	// of failing loudly". An absent DEPENDENCY answering negative and an absent
	// TRACKER failing loudly are different questions with different answers.
	//
	// Read without mu, like perm/modal/bcast/activeConv/ctx: written once at wiring
	// time before the manager's Run goroutine starts. mu guards byModal and nextID,
	// and nothing else. #1919.
	toolCallInFlight func(conversationID, toolCallID string) bool

	// questions is the daemon-singleton store of surfaced-but-unretired
	// clarifying-question batches (#1975). Set AFTER CONSTRUCTION at the one
	// production site (relay.go), for toolCallInFlight's reason: the constructor
	// has fifteen call sites and this file's precedent for an optional dependency
	// is the resolver's streamApprovals field.
	//
	// nil ⇒ EVERY approval takes the permission path, question or not, which is
	// exactly the pre-#1973 behaviour — so the fourteen test constructions, the
	// daemon's PTY mode and v1/foreground stay semantically unchanged without a
	// second discriminant. It is minted beside modalReg rather than here because
	// #1928's connect-time reconcile reads its Snapshot from a config assembled
	// before this bridge exists.
	//
	// Read without mu, like perm/modal/bcast/activeConv/ctx: written once at
	// wiring time before the manager's Run goroutine starts.
	questions *questionbridge.Registry

	// mu is a leaf lock guarding byModal + byQuestion + nextID ONLY: held around
	// O(1) map ops and the counter bump, never across modal.Record,
	// perm.Lookup/perm.Resolve, modal.Resolve, questions.Record/questions.Resolve,
	// or a Push — so bridge.mu → registry.mu never nests and there is no deadlock
	// order to reason about.
	mu      sync.Mutex
	byModal map[string]streamApprovalCorrelation // modalID → permission eligibility
	// byQuestion is the batch correlation, and it is a SECOND MAP rather than
	// another key space inside byModal for a load-bearing reason: ResolveStream
	// treats any byModal hit as "this id is a stream approval" and resolves the
	// parked completer, so a batch id living there would let a modal_answer naming
	// it allow claude's AskUserQuestion call with nobody having answered. Today
	// that is also gated one level up — ResolveAnswer looks the id up in
	// modalbridge first, and a batch is never recorded there — but the separate map
	// makes the property structural rather than a consequence of another registry's
	// contents. Both maps are read by ApprovalParked and ApprovalAnswerable, which
	// ask about a parked approval and do not care which surface raised it.
	byQuestion map[string]string // questionBatchID → toolUseID
	nextID     uint64            // per-bridge control-envelope counter
}

// streamApprovalCorrelation keeps the opaque registry key and the immutable
// remote-answer eligibility captured when a permission is surfaced. Questions
// use byQuestion instead, so their answer path never inherits this restriction.
type streamApprovalCorrelation struct {
	toolUseID               string
	requiresUserInteraction bool
}

// newStreamApprovalBridge constructs the bridge over the daemon-singleton
// permbridge registry (approvals), the daemon-singleton modalbridge registry (the
// same instance the modal emitter Records into and modalResolverV2 consumes), the
// v2 manager's interactive broadcaster, the follow-active conversation cursor, and
// the daemon ctx (captured for the broadcasts Surface/retire fire off the
// control-server handler goroutine, which carries no ctx of its own).
func newStreamApprovalBridge(perm *permbridge.Registry, modal *modalbridge.Registry, bcast interactiveBroadcaster, activeConv func() string, ctx context.Context, logger *slog.Logger) *streamApprovalBridge {
	return &streamApprovalBridge{
		perm:       perm,
		modal:      modal,
		bcast:      bcast,
		activeConv: activeConv,
		ctx:        ctx,
		logger:     logger,
		byModal:    make(map[string]streamApprovalCorrelation),
		byQuestion: make(map[string]string),
	}
}

// The question-dismissal vocabulary, and ONE sentinel covers the whole no-answer
// class deliberately (#1973). The retire closure the control server defers runs
// identically on all three no-answer terminal paths — the approval window
// elapsing, the caller disconnecting, the daemon shutting down — and carries
// nothing that tells them apart, so emitting modal_dismissed's `timeout` would
// name a cause that is wrong on two paths out of three. A client reads an
// unrecognised source as "resolved, cause unknown, never as an answer", which is
// the fail-closed reading docs/protocol-mobile.md § question_dismissed publishes.
//
// Compile-time constants, like reasonRemoteDeny, so neither can ever carry a
// claude-authored option label — the rule QuestionDismissedPayload.Outcome states
// positively, because options carry no id and claude's answer protocol selects by
// label, so an answer path reporting the chosen option reaches for that string
// first and would move the frame to the batch's trust tier.
const (
	outcomeQuestionUnanswered = "unanswered"
	sourceQuestionNoAnswer    = "no_answer"
)

// The refusal dismissal vocabulary (#1990), published beside the no-answer pair
// in docs/protocol-mobile.md § question_dismissed. Compile-time constants for
// that pair's reason: neither can ever carry a claude-authored option label.
//
// sourceQuestionRemote is a local constant rather than string(audit.SourceRemote)
// even though the two spell the same wire value. ResolveCancel derives its source
// from the audit vocabulary because one value has to feed both its wire dismissal
// and its audit entry; this path writes NO audit record (retireQuestion's doc
// block says why — audit.Entry is the modal vocabulary and a batch has neither a
// modal id nor a class), so there is no second consumer to keep in agreement, and
// the question family's dismissal vocabulary stays readable in one block.
const (
	outcomeQuestionRefused = "refused"
	sourceQuestionRemote   = "remote"
)

// outcomeQuestionAnswered completes the family's outcome vocabulary (#1991),
// the third and last sentinel beside unanswered and refused. It shares
// sourceQuestionRemote with the refusal: both are a batch the operator resolved
// from a client, which is exactly what § question_dismissed's `remote`
// carry-over row publishes.
//
// It is a SENTINEL and not the chosen option, which is this frame's sharpest
// rule rather than a naming preference. QuestionOption carries no id and
// claude's answer protocol selects by LABEL, so the natural implementation of an
// answered dismissal reports that claude-authored string here — moving a frame
// whose published provenance reads daemon-asserted into the batch's trust tier,
// where a client would render it as trusted chrome. A client that wants the
// chosen label reads it from the batch it already holds.
const outcomeQuestionAnswered = "answered"

// answerVerdictInput is the updated tool input an answered batch hands back to
// claude: its own questions array, passed through, plus the operator's answers.
// The two keys are claude's whole contract for the reply
// (https://code.claude.com/docs/en/agent-sdk/user-input) — the questions array
// is required for tool processing, and answers is keyed by each question's TEXT
// with the chosen option's label as its value.
//
// Questions is a json.RawMessage BECAUSE THE PASSTHROUGH MUST BE CLAUDE'S OWN
// BYTES, spliced out of the parked tool input rather than re-marshalled from the
// daemon's wire-shaped batch. The two routes are indistinguishable to a shape
// check and differ by one key spelling: protocol.Question tags MultiSelect as
// multi_select where claude's tool input uses multiSelect, so a re-marshal hands
// claude a key it does not read — silently, since the call is allowed either
// way. questionbridge.Parse also drops keys it does not model, which would cut a
// field a later claude release adds.
//
// Answers values are `any` rather than []string because the shape is per
// question: a bare string for a single-select question, an array for a
// multiSelect one, matching claude's own examples for each kind.
type answerVerdictInput struct {
	Questions json.RawMessage `json:"questions"`
	Answers   map[string]any  `json:"answers"`
}

// reasonQuestionRefused is the deny message a refused batch returns to claude. A
// compile-time constant — never host-derived, never client-supplied — exactly
// like reasonRemoteDeny and permbridge's own reasonTimeout, so nothing an
// operator or a paired device authored can reach claude through it.
//
// IT IS AN INSTRUCTION, NOT A REASON STRING, and that is the point of the slice.
// A bare deny leaves claude free to answer its own question and carry on, which
// is precisely what the operator declined to let it do; the wording has to say
// that they want to discuss the question and that claude is to wait for their
// next message.
const reasonQuestionRefused = "The user declined to choose an option. They want to discuss this question before answering it: do not answer it yourself, do not assume an answer, and do not continue with the work it was blocking. Stop and wait for their next message."

// RefuseQuestion resolves the outstanding batch named by batchID as a refusal:
// claude's parked AskUserQuestion call is denied with the instruction above, and
// every interactive client is told the panel is dead. It reports whether it
// consumed the batch — a diagnostic for the caller's log record, never a
// broadcast trigger (relay.QuestionResolver's doc block), and deliberately not
// re-derivable by a second Lookup, which would reintroduce the race the one-shot
// exists to remove.
//
// IT APPLIES NO AUTHORIZATION AND IS NOT SELF-GUARDING. Possession of the batch
// id is treated as sufficient here, which is safe only because the per-device
// answer gate (#1986) sits above it and because nothing reaches this path from
// the wire until that gate exists — relay.QuestionResolver is unimplemented and
// V2SessionConfig.QuestionResolver is nil at every construction site. A wiring
// site that calls this without gating first lets any paired device refuse.
//
// THE ORDER OF THE FIRST THREE STEPS IS LOAD-BEARING IN BOTH DIRECTIONS:
//
//  1. Read the byQuestion correlation WITHOUT deleting it. retireQuestion is the
//     sole, unconditional deleter on every terminal path, and ResolveStream sets
//     the resolve-without-deleting precedent. A miss ends the call: an unknown
//     batch, or one whose retire already ran. That read is also why a nil
//     b.questions needs no guard — byQuestion is written only by surfaceQuestion,
//     which Surface reaches only under a non-nil registry, so a nil registry
//     implies an empty map and this step has already returned.
//  2. Consume the registry one-shot. Reading the correlation FIRST is what keeps
//     a refusal from winning the one-shot inside retireQuestion's window between
//     its delete and its own Resolve — two separate critical sections, by design
//     — and then finding no tool_use_id: claude would never be denied AND the
//     backstop, having lost the one-shot, would broadcast nothing, leaving every
//     client's panel up until the approval window elapsed.
//  3. Hand claude the verdict only AFTER that consume. The control server's
//     deferred retireQuestion runs the instant permbridge.Pending.Await returns,
//     so denying first would let the backstop win the one-shot and broadcast
//     `unanswered` for a batch the operator actually refused. ResolveAnswer keeps
//     the same shape — it consumes the modalbridge entry before ResolveStream.
//
// THE DISMISSAL FANS OUT ON ITS OWN GOROUTINE, the one deliberate deviation from
// retireQuestion's otherwise identical shape. broadcast calls bcast.ActiveConns,
// which funnels a request onto the relay manager's Run select — so a caller
// already on Run blocks there until the daemon ctx is cancelled, stalling the
// manager and every approval waiting on it (ApprovalAnswerable's doc block
// carries this file's record of the same hazard). retireQuestion is safe because
// it runs on a control-server handler goroutine; this primitive's caller is
// #1986's QuestionResolver implementation, which relay documents as running on
// Run and which cannot hand the work off itself — it owes its own caller this
// bool synchronously. Steps 1-3 therefore stay synchronous: the consume, the
// verdict and the return value are all settled before this returns, and only the
// fan-out is detached. That goroutine ends when the fan-out finishes, or at the
// latest when the daemon ctx is cancelled (ActiveConns returns nil on a done ctx
// and broadcast's Push loop returns early on teardown); at most one exists per
// consumed refusal, and a refusal requires a batch parked on a human, so a client
// cannot drive the count.
//
// SECURITY: the batch id is the only value that crosses in, and it is used solely
// as a key — never logged here, never rendered, never sent back toward claude.
// The path emits NO log record of its own: the relay handler already logs the
// outcome with the batch id (the one field internal/protocol marks safe), so a
// record here would duplicate it while its only new material — the batch body or
// the deny message — is exactly what must not be logged.
func (b *streamApprovalBridge) RefuseQuestion(batchID string) (consumed bool) {
	b.mu.Lock()
	toolUseID, ok := b.byQuestion[batchID]
	b.mu.Unlock()
	if !ok {
		return false
	}

	if _, ok := b.questions.Resolve(batchID); !ok {
		return false // the backstop, or an earlier refusal, already consumed it
	}

	// A Resolve miss here means permbridge already resolved this approval on its
	// own timer — nothing to do, and the client must still learn the panel is
	// dead, so the dismissal below is unconditional.
	b.perm.Resolve(toolUseID, permbridge.Deny(reasonQuestionRefused))

	go b.broadcast(protocol.TypeQuestionDismissed, protocol.QuestionDismissedPayload{
		QuestionBatchID: batchID,
		Outcome:         outcomeQuestionRefused,
		Source:          sourceQuestionRemote,
	}, "stream_question.refused_push_err")

	return true
}

// AnswerQuestion resolves the outstanding batch named by batchID as the
// operator's answer: claude's parked AskUserQuestion call is allowed, carrying an
// updated input that pairs claude's own questions array with what they chose, and
// every interactive client is told the panel is dead. It reports whether it
// consumed the batch — a diagnostic for the caller's log record, never a
// broadcast trigger (relay.QuestionResolver's doc block).
//
// It is AnswerQuestion and not ResolveAnswer because that name is already taken
// on this struct by the modal path, exactly as RefuseQuestion had to sidestep
// ResolveCancel. It takes the batch id and the entries rather than the whole
// protocol.QuestionAnswerPayload so it never holds an answer_token it has no
// business reading — questionbridge.Parse taking the pair rather than the
// permbridge.Request is the same argument. That token is idempotency and not
// authorization; the real dedup is the one-shot consume below.
//
// IT APPLIES NO AUTHORIZATION AND IS NOT SELF-GUARDING. Possession of the batch
// id is treated as sufficient here, which is safe only because the per-device
// answer gate (#1986) sits above it and because nothing reaches this path from
// the wire until that gate exists — relay.QuestionResolver is unimplemented and
// V2SessionConfig.QuestionResolver is nil at every construction site. A wiring
// site that calls this without gating first lets any paired device answer.
//
// RefuseQuestion is the template, and its ordering argument carries over intact —
// read the correlation without deleting it, consume the one-shot before handing
// claude the verdict — with ONE step it does not have:
//
//   - THE PARKED INPUT IS READ BEFORE THE CONSUME, AND ITS MISS IS A REAL
//     BRANCH. permbridge.Lookup missing means permbridge already resolved this
//     approval on its own timer, so there are no input bytes to pass through.
//     This returns having done NOTHING, because the control server's deferred
//     retireQuestion is about to run and owes every client its `unanswered`
//     dismissal: consuming first and dismissing without a verdict would silence
//     that backstop and leave claude denied by the timer with no client told
//     why. A Deny needs no input, which is why the refusal resolves
//     unconditionally and this cannot.
//   - THE VALIDATION READS THROUGH questionbridge.Lookup, NOT Resolve. A
//     rejected answer must leave the batch answerable for a corrected one
//     (answerVerdict states every rule), so the one-shot is consumed only once
//     the verdict is known good. Validating against Resolve would burn the batch
//     on a malformed frame.
//
// The check-then-act between that Lookup and the Resolve is deliberately not
// atomic and is safe in both directions: the one-shot is the single arbiter, so
// a concurrent retireQuestion or RefuseQuestion either wins it (this call
// returns false having resolved nothing) or loses it (it broadcasts nothing).
// Validating against a batch a concurrent caller then consumes costs only wasted
// work. The exploitable ordering is the reverse one, consume-then-validate.
//
// THE DISMISSAL FANS OUT ON ITS OWN GOROUTINE, RefuseQuestion's one deliberate
// deviation from retireQuestion, for its reason verbatim: broadcast calls
// bcast.ActiveConns, which funnels a request onto the relay manager's Run select,
// so a caller already on Run blocks there until the daemon ctx is cancelled. This
// primitive's caller is #1986's QuestionResolver implementation, which relay
// documents as running on Run and which cannot hand the work off itself — it owes
// its own caller this bool synchronously. Everything before the fan-out therefore
// stays synchronous: the validation, the consume, claude's verdict and the return
// value are all settled before this returns. That goroutine ends when the fan-out
// finishes, or at the latest when the daemon ctx is cancelled (ActiveConns returns
// nil on a done ctx and broadcast's Push loop returns early on teardown); at most
// one exists per consumed answer, and an answer requires a batch parked on a
// human, so a client cannot drive the count.
//
// SECURITY: the answers are remote-authored, and they reach claude's context —
// which grants nothing new, since a paired client can already put arbitrary text
// into the conversation with send_message, so an answer sits at exactly that
// trust tier (protocol.QuestionAnswerEntry's doc block). What must not happen is
// the other direction, and it is structural here: the path emits NO log record of
// its own, so there is no field for question text, an answer value or any other
// byte of the batch to leak into. The relay handler already logs the outcome with
// the batch id and the answer token, the two fields internal/protocol marks safe,
// so a record here would duplicate it while its only new material is exactly what
// may never be logged.
func (b *streamApprovalBridge) AnswerQuestion(batchID string, answers []protocol.QuestionAnswerEntry) (consumed bool) {
	b.mu.Lock()
	toolUseID, ok := b.byQuestion[batchID]
	b.mu.Unlock()
	if !ok {
		return false
	}

	// Before the consume: no input bytes means no verdict, and the backstop
	// still owes this batch its dismissal.
	req, ok := b.perm.Lookup(toolUseID)
	if !ok {
		return false
	}
	batch, ok := b.questions.Lookup(batchID)
	if !ok {
		return false
	}
	updated, ok := answerVerdict(req.Input, batch.Questions, answers)
	if !ok {
		return false
	}

	if _, ok := b.questions.Resolve(batchID); !ok {
		return false // the backstop, a refusal, or an earlier answer already consumed it
	}

	// A Resolve miss here means permbridge resolved this approval between the
	// Lookup above and now — the batch is consumed either way, and the client
	// must still learn the panel is dead, so the dismissal below is
	// unconditional.
	b.perm.Resolve(toolUseID, permbridge.Allow(updated))

	go b.broadcast(protocol.TypeQuestionDismissed, protocol.QuestionDismissedPayload{
		QuestionBatchID: batchID,
		Outcome:         outcomeQuestionAnswered,
		Source:          sourceQuestionRemote,
	}, "stream_question.answered_push_err")

	return true
}

// answerVerdict validates a client's entries against the parked batch and, if
// they hold, assembles the updated tool input an allow carries back. It reports
// false for every rejection, and a rejection is TOTAL: nothing is assembled,
// which is what lets the caller consume the one-shot only on success.
//
// BOTH HALVES OF THE RESULT COME FROM COPIES THE DAEMON HOLDS, never from
// anything the client echoed back. input is claude's own parked bytes, whose
// questions VALUE is spliced through untouched (answerVerdictInput says why a
// re-marshal is wrong); questions is the parked batch, which supplies the text
// keying each answer. The two agree because Surface reaches surfaceQuestion only
// through a questionbridge.Parse that already succeeded on these exact bytes, and
// Parse rejects rather than truncates. That is also why the extraction below
// cannot fail — it is folded into the reject path rather than ignored, so the
// structurally unreachable case still cannot reach claude with a missing key.
//
// The rules, each of which rejects the whole answer:
//
//   - Entry count must equal the parked question count. It is checked FIRST, so
//     an arbitrarily long payload — protocol enforces no bound on the inbound
//     array — is O(1) to reject and per-frame work stays bounded by the batch's
//     1-4 questions. With the counts equal, "every index in range and none
//     repeated" gives exactly-once coverage by pigeonhole, which is the whole
//     purpose of seen.
//   - Every index must fall inside the batch. protocol carries QuestionIndex and
//     never range-checks it, so the check has to happen before the subscript;
//     skipping it is a panic rather than a wrong answer.
//   - A question with no values selects nothing, and more than one value for a
//     single-select question states two positions.
//
// NO VALUE IS COMPARED AGAINST THE OFFERED LABELS, deliberately: claude's
// contract permits free text anywhere and requires no value to be one of the
// labels, so a validator rejecting an unlisted value would reject a legal answer.
// Whether a value is emitted bare or as an array is decided from the parked
// question's MultiSelect and never from how many values arrived, so the shape
// matches claude's own examples per question kind.
//
// TWO QUESTIONS IN ONE BATCH CAN CARRY IDENTICAL TEXT, AND THAT COLLAPSES TO ONE
// answers KEY. There is deliberately no reject branch for it: keying by text is
// claude's own contract, so the daemon cannot do better than the shape allows,
// and rejecting would strand the operator with a batch they can never answer —
// strictly worse than the contract's own ambiguity. Nothing about it is
// client-controlled.
func answerVerdict(input json.RawMessage, questions []protocol.Question, answers []protocol.QuestionAnswerEntry) (json.RawMessage, bool) {
	if len(answers) != len(questions) {
		return nil, false
	}
	var parked struct {
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(input, &parked); err != nil || len(parked.Questions) == 0 {
		return nil, false
	}

	seen := make([]bool, len(questions))
	chosen := make(map[string]any, len(questions))
	for _, a := range answers {
		if a.QuestionIndex < 0 || a.QuestionIndex >= len(questions) || seen[a.QuestionIndex] {
			return nil, false
		}
		seen[a.QuestionIndex] = true

		q := questions[a.QuestionIndex]
		if len(a.Values) == 0 || (!q.MultiSelect && len(a.Values) > 1) {
			return nil, false
		}
		if q.MultiSelect {
			chosen[q.Text] = a.Values
		} else {
			chosen[q.Text] = a.Values[0]
		}
	}

	out, err := json.Marshal(answerVerdictInput{Questions: parked.Questions, Answers: chosen})
	if err != nil {
		return nil, false
	}
	return out, true
}

// Surface raises a permbridge-parked approval to interactive clients as the same
// permission modal_shown they already answer, and returns a retire closure the
// control server defers for guaranteed cleanup + dismissal (AC-1). The prompt body
// is req.ToolName (bounded by modalbridge.boundPrompt; non-secret — it names the
// tool being approved); the 4 permission options and the reject-once deny-default
// are class-fixed by PermissionRequestForClass, so the payload is byte-compatible
// with today's clients by construction. On modal.Record RNG failure it stores
// nothing, broadcasts nothing, and returns a no-op retire — claude then times out
// to deny via permbridge (fail-closed degrade, mirroring handleModalShown). Runs
// on the control-server handler goroutine (concurrent across approve requests).
//
// Since #1973 the FIRST question asked is whether this approval is claude's
// clarifying-question batch, because that surfaces as a different frame family
// entirely (surfaceQuestion). questionbridge.Parse is the single trust boundary
// between claude's tool input and the wire, and surfaceQuestion is reachable only
// from a Parse that returned ok, so no unbounded or malformed batch reaches a
// client. Parse deliberately does not distinguish "not the question tool" from
// "the question tool, rejected", and that costs nothing here: a batch nobody can
// render falls through to the permission modal below, which is the only prompt
// that still gets claude an allow/deny decision.
func (b *streamApprovalBridge) Surface(req permbridge.Request) (retire func()) {
	if b.questions != nil {
		if batch, ok := questionbridge.Parse(req.ToolName, req.Input); ok {
			return b.surfaceQuestion(batch, req.ToolUseID)
		}
	}

	permReq, wireClass, ok := modalbridge.PermissionRequestForClass(tuidriver.ModalClassPermission, req.ToolName)
	if !ok {
		// Unreachable: ModalClassPermission always maps. Defensive — never surface
		// an unbuilt modal; claude times out to deny (fail-closed).
		return func() {}
	}

	payload, err := b.modal.RecordWithContext(permReq, wireClass, b.activeConv(), modalbridge.PermissionContext{
		Reason:      req.DecisionReason,
		ReasonType:  req.DecisionReasonType,
		BlockedPath: req.BlockedPath,
		Description: req.Description,
		DefaultToNo: req.DefaultToNo,
		AlwaysAllow: req.AlwaysAllow,
	})
	if err != nil {
		// crypto/rand failure — drop the modal (no modal_shown, no correlation);
		// claude times out to deny. Never echo err detail; no payload/screen bytes.
		b.logger.Warn("relay: stream approval surface drop; id mint",
			"event", "stream_approval.rand_err")
		return func() {}
	}
	modalID := payload.ModalID

	b.mu.Lock()
	b.byModal[modalID] = streamApprovalCorrelation{
		toolUseID:               req.ToolUseID,
		requiresUserInteraction: req.RequiresUserInteraction,
	}
	b.mu.Unlock()

	b.broadcast(protocol.TypeModalShown, payload, "stream_approval.push_err")

	return func() { b.retire(modalID) }
}

// surfaceQuestion raises a parsed clarifying-question batch to interactive clients
// as one question_shown and returns the retire closure the control server defers
// (#1973). Surface's question arm, reachable only from a questionbridge.Parse that
// accepted the batch.
//
// THE BROADCAST PAYLOAD IS Record's RETURN VALUE, NEVER THE PARSED ONE. Parse
// leaves both ids zero because both are daemon-asserted, and Record is what mints
// the nonce, stamps the conversation id and hands back the stamped copy.
// Broadcasting the pre-record payload would ship an id-less frame that no
// dismissal can ever correlate to — cheap to write, and invisible until a client
// tries to clear the panel. Record also overwrites any id that did arrive on the
// payload, so nothing out of claude's tool input can become a routing key.
//
// On the Record RNG failure it stores nothing, broadcasts nothing and returns a
// no-op retire, mirroring Surface's modal.Record degrade: claude times out to deny.
// That also leaves the approval invisible to ApprovalAnswerable, so its deadline
// is not extended for a batch nobody has ever seen — the fail-closed direction.
//
// Runs on the control-server handler goroutine. SECURITY: the batch's four
// claude-authored strings reach the marshalled payload and NOTHING else; no log
// field here carries the question text, a header, an option label or description,
// the tool name, or any other byte of the parked input.
func (b *streamApprovalBridge) surfaceQuestion(batch protocol.QuestionShownPayload, toolUseID string) (retire func()) {
	stamped, err := b.questions.Record(batch, b.activeConv())
	if err != nil {
		// crypto/rand failure — drop the batch (no question_shown, no
		// correlation); claude times out to deny. Never echo err detail; no
		// payload bytes.
		b.logger.Warn("relay: stream question surface drop; batch id mint",
			"event", "stream_question.rand_err")
		return func() {}
	}
	batchID := stamped.QuestionBatchID

	b.mu.Lock()
	b.byQuestion[batchID] = toolUseID
	b.mu.Unlock()

	b.broadcast(protocol.TypeQuestionShown, stamped, "stream_question.push_err")

	return func() { b.retireQuestion(batchID) }
}

// retireQuestion is the guaranteed cleanup + dismissal backstop for a surfaced
// question batch, and the SINGLE ARBITER of the no-answer dismissal broadcast. It
// runs on EVERY Await return — the approval window elapsing, the caller
// disconnecting, the daemon shutting down, and the eventual answer (#1907) — in
// two steps with separate arbiters, mirroring retire:
//
//  1. Unconditionally delete the batchID→toolUseID correlation under mu. Sole
//     deleter, every terminal path, so byQuestion never leaks. Delete of an absent
//     key is a safe no-op.
//  2. questions.Resolve(batchID): a miss ⇒ the answer path already consumed and
//     dismissed this batch → no second dismissal. A hit ⇒ a no-answer path →
//     broadcast exactly one question_dismissed. The registry's read-and-delete is
//     one critical section, which is what makes "exactly one broadcaster per
//     outstanding question" structural rather than an agreement between this slice
//     and #1907.
//
// The delete runs BEFORE the Resolve, matching retire's order: the window between
// them reads "not parked" to ApprovalAnswerable, which is the fail-closed
// direction for a deadline, rather than "parked but already gone".
//
// NO AUDIT RECORD, unlike retire's. audit.Entry carries ModalID and ModalClass —
// the modal vocabulary. A batch has no class and its nonce is not a modal id, so
// an entry here would file a question under field names that lie about it.
//
// Runs on the control-server handler goroutine after Await.
func (b *streamApprovalBridge) retireQuestion(batchID string) {
	b.mu.Lock()
	delete(b.byQuestion, batchID)
	b.mu.Unlock()

	if _, ok := b.questions.Resolve(batchID); !ok {
		return // the answer path already consumed + broadcast this batch's dismissal
	}

	b.broadcast(protocol.TypeQuestionDismissed, protocol.QuestionDismissedPayload{
		QuestionBatchID: batchID,
		Outcome:         outcomeQuestionUnanswered,
		Source:          sourceQuestionNoAnswer,
	}, "stream_question.dismissed_push_err")
}

// parkedToolUseIDs snapshots every tool_use_id currently parked on a person,
// across BOTH surfaces: a clarifying question parks on a human exactly as a
// permission does, and ApprovalParked asks about a parked approval without caring
// which frame family raised it.
//
// SNAPSHOT UNDER mu, RELEASE, THEN ASK — the discipline ApprovalParked documents,
// and the reason mu stays a leaf lock. Bounded by the approvals concurrently
// parked on a human.
func (b *streamApprovalBridge) parkedToolUseIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.byModal)+len(b.byQuestion))
	for _, correlation := range b.byModal {
		out = append(out, correlation.toolUseID)
	}
	for _, toolUseID := range b.byQuestion {
		out = append(out, toolUseID)
	}
	return out
}

// RemoteAnswerable reports whether modalID is eligible for the remote
// permission-answer path. IDs outside the stream permission correlation are
// answerable here: the gate withholds only an interaction-required approval.
// A correlated interaction-required permission stays outstanding for the
// registry's fail-closed timeout instead. The lookup emits no log or audit record.
func (b *streamApprovalBridge) RemoteAnswerable(modalID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	correlation, ok := b.byModal[modalID]
	return !ok || !correlation.requiresUserInteraction
}

// ResolveStream is modalResolverV2.ResolveAnswerWithAlwaysAllow's stream
// (verdict) arm: resolve a
// stream-json approval identified by modalID to allow/deny on claude's parked
// completer (AC-2). handled=false ⇒ modalID is not a stream approval (absent from
// byModal) → the caller actuates nothing and Warn-logs it. An allow echoes the parked
// tool Input byte-verbatim and, when requested, derives session-only permission
// updates from the parked offer. A perm.Lookup miss on allow means permbridge
// already resolved (a raced timeout) — nothing to do, still fail-closed (claude denied).
// denyReason is the fixed content-free reasonRemoteDeny, never host-derived.
//
// It does NOT delete the correlation (retire is the sole, unconditional deleter)
// and does NOT consume the modalbridge entry (ResolveAnswer already consumed it
// before calling here, which gates a second ResolveStream for the same modalID).
// Runs on the relay manager's single Run goroutine.
func (b *streamApprovalBridge) ResolveStream(modalID string, allow, alwaysAllow bool, denyReason string) (handled bool) {
	b.mu.Lock()
	correlation, ok := b.byModal[modalID]
	b.mu.Unlock()
	if !ok {
		return false // not a stream approval — caller Warn-logs the miss
	}

	if allow {
		if req, ok := b.perm.Lookup(correlation.toolUseID); ok {
			verdict := permbridge.Allow(req.Input)
			if alwaysAllow {
				verdict = permbridge.AllowAlways(req.Input, req.AlwaysAllow)
			}
			b.perm.Resolve(correlation.toolUseID, verdict)
		}
	} else {
		b.perm.Resolve(correlation.toolUseID, permbridge.Deny(denyReason))
	}
	return true
}

// ApprovalParked reports whether conversationID currently has an approval parked
// awaiting a human decision (#1919). It is a CONJUNCTION across two membership
// sets — the correlation is parked here AND the call it names is in flight on the
// asked-about conversation — so either side going empty makes the answer negative,
// which is the fail-closed direction for a delivery hold.
//
// RESOLVED ON READ, never stamped when the approval parks. Stamping at Surface
// time would reintroduce a race: the ToolStart travels child → parser → sink →
// drain in-process while the approve travels claude → pyry mcp-approve → control
// socket, and nothing orders the two. Answering on read removes it outright, and
// lets retire — the sole correlation deleter, running on every terminal path —
// carry "reports negative again" with no counter and no fifth path to pin.
//
// The conversation key is deliberately NOT activeConv(): that cursor is set at
// enqueue by the session router, so a message enqueued for B while A sits parked
// moves it to B and would misattribute every approval A parks afterwards. Good
// enough for scoping a modal to a client's view; wrong for a report a delivery
// hold trusts.
//
// SNAPSHOT UNDER mu, RELEASE, THEN ASK — through parkedToolUseIDs, which covers
// BOTH surfaces because a parked clarifying question is parked on a human exactly
// as a permission is. mu is a leaf lock and ToolCallInFlight takes the tracker's
// own; asking under mu would establish this file's first nesting order, and one no
// other call site establishes — the shape that becomes a deadlock the day an edge
// appears the other way. The snapshot allocation is the price of keeping the lock
// a leaf, and both maps are bounded by the approvals concurrently parked on a
// human, each of which holds a control-socket connection blocked in Await.
//
// A retire or a Surface landing between the snapshot and the asks makes the answer
// one call stale, and that is CORRECT rather than tolerated: the truth is changing
// under any locking discipline, and a caller that read it under a global lock would
// still act on it after releasing. The report is a level, not an edge — its
// consumer re-reads it, so there is no transition to miss.
//
// MEMBERSHIP, mirroring Busy and ToolCallInFlight: THE SIGNATURE IS THE
// EXISTENCE-ORACLE ENFORCEMENT, not a runtime branch. Unknown, never-seen and
// empty values of either id reach one false through the same path — there is no
// id-specific branch here, because an empty conversation key is never inserted into
// the tracker (observe refuses an unresolvable session) and an empty tool-call id
// is never inserted either (setBusy refuses it), so both empties are answered by
// the identical lookups inside ToolCallInFlight. A later widening to (bool, error),
// or any variant handing back a conversation id, a modal id or a count, would
// reintroduce the oracle silently.
//
// It logs NOTHING. Every diagnostic worth emitting from here would carry either a
// conversation id — withheld as a routing key treated as sensitive, per
// clearForSession — or a tool_use id, so the correct count is zero.
//
// Callable from any goroutine: ResolveStream already reads byModal off the relay
// Run goroutine while Surface and retire mutate it from control-server handlers.
func (b *streamApprovalBridge) ApprovalParked(conversationID string) bool {
	if b.toolCallInFlight == nil {
		return false // no tracker wired (PTY mode) — see the field's doc block
	}

	parked := b.parkedToolUseIDs()

	// Asking about EVERY parked id, including ones belonging to other
	// conversations, leaks nothing about them: the tracker's retention is nested
	// per conversation, so another conversation's id is simply absent from this
	// conversation's inner set and answers false. That confinement is #1917's
	// property and the reason this iterate-and-ask shape is safe.
	for _, toolUseID := range parked {
		if b.toolCallInFlight(conversationID, toolUseID) {
			return true
		}
	}
	return false
}

// ApprovalAnswerable reports whether approvalID is still parked on a person,
// eligible for a remote answer, AND has at least one interactive-capable client
// connected to answer it.
// approvalID is claude's tool_use_id, which is ALSO permbridge's registry id. req
// is the immutable request from the exact registry generation whose timer is
// asking, so a stale same-ID surface cannot substitute its eligibility.
//
// All three parts of the conjunction are load-bearing. An approval can sit parked having
// never been surfaced to anybody: Surface's modal.Record failure path stores no
// correlation and broadcasts nothing, leaving claude to time out to deny. A
// connectivity-only report would call that approval answerable while no client has
// ever seen it; requiring both halves answers negative there, which is the
// fail-closed direction for a deny deadline. A surfaced interaction-required
// permission also answers negative before connectivity is consulted, because a
// connected phone still cannot provide its required tool-specific interaction.
//
// THE PARKED HALF IS CHECKED FIRST, and that ordering is not a flourish. ActiveConns
// is not a lock acquisition but a blocking round-trip onto the relay manager's Run
// goroutine, and the consumer sits on permbridge's fail-closed deny path — so the
// common negative, an approval whose correlation is already gone, must not pay a
// cross-goroutine hand-off to reach an answer it cannot change.
//
// The interactive gate is deliberately the SAME gate broadcast applies (#607): a
// conn that never negotiated the capability is never pushed a modal_shown, so it can
// never produce a modal_answer, and counting it would claim an answerer that
// structurally cannot answer. ActiveConns additionally excludes sessions still
// handshaking or token-unvalidated, so an un-authenticated peer is never counted
// either — inherited, not restated. It also returns nil once the daemon ctx is
// cancelled or Run has exited, which reads as "nobody connected": at teardown nobody
// can answer, so that is fail-closed too.
//
// SNAPSHOT UNDER mu, RELEASE, THEN ASK — the discipline broadcast already
// establishes in this file by taking mu INSIDE its ActiveConns loop, never around
// it. mu is a leaf lock; holding it across the hand-off would block every concurrent
// Surface, retire and ResolveStream for the manager's scheduling latency and would
// retire the leaf-lock property outright. The current request selects exactly one
// surface family, whose correlation is scanned under mu; a parked clarifying
// question remains answerable while an interaction-required permission does not.
// It releases mu before ActiveConns.
//
// NEVER CALL THIS FROM THE RELAY Run GOROUTINE. ActiveConns funnels its request onto
// Run and is documented safe only from other goroutines, so a call from Run
// deadlocks the manager: Run blocked sending to itself, no frames dispatched, no
// modals delivered, every parked approval left to its deadline. ResolveStream is a
// bridge method that already runs on Run, so proximity makes this an easy mistake.
// permbridge's time.AfterFunc timer goroutine — where the consumer sits — is not Run.
//
// A retire, a Surface or a disconnect landing between the scan and the enumeration
// makes the answer one call stale, and that is CORRECT rather than tolerated: the
// truth changes under any locking discipline, and a caller that read it under a
// global lock would still act on it after releasing. The report is a level, not an
// edge — its consumer re-reads it, so there is no transition to miss.
//
// MEMBERSHIP, mirroring Busy and ApprovalParked: THE SIGNATURE IS THE
// EXISTENCE-ORACLE ENFORCEMENT, not a runtime branch. Unknown, never-surfaced,
// already-resolved and empty approval ids reach one false through the same scan.
// There is no id-specific branch, and deliberately no `if approvalID == ""` guard: an
// empty correlation is never stored because permbridge.Register refuses an empty id
// before the control server can reach Surface, so that invariant belongs at the write
// side. A later widening to (bool, error), or any variant handing back a modal id, a
// conn id or a count, would reintroduce the oracle silently.
//
// There is also no b.bcast nil guard: broadcast already dereferences it
// unconditionally on the Surface and retire paths, so a bridge built without a
// broadcaster panics long before this report is reached, and a guard here would let a
// wiring bug read as "nobody can answer".
//
// It logs NOTHING, for ApprovalParked's reason: every diagnostic worth emitting from
// here would carry a tool_use id or a conn id.
//
// Callable from any goroutine except the relay Run goroutine.
func (b *streamApprovalBridge) ApprovalAnswerable(approvalID string, req permbridge.Request) bool {
	b.mu.Lock()
	parkedAndEligible := false
	if _, question := questionbridge.Parse(req.ToolName, req.Input); question {
		for _, toolUseID := range b.byQuestion {
			if toolUseID == approvalID {
				parkedAndEligible = true
				break
			}
		}
	} else if !req.RequiresUserInteraction {
		for _, correlation := range b.byModal {
			if correlation.toolUseID == approvalID {
				parkedAndEligible = true
				break
			}
		}
	}
	b.mu.Unlock()
	if !parkedAndEligible {
		return false // nobody is holding this approval — no round-trip needed
	}

	for _, c := range b.bcast.ActiveConns(b.ctx) {
		if c.Interactive {
			return true // the #607 capability gate, the same one broadcast applies
		}
	}
	return false
}

// retire is the guaranteed cleanup + dismissal backstop the control server defers
// for a surfaced stream approval. It runs on EVERY Await return (answer, timeout,
// disconnect, shutdown) in two steps with separate arbiters:
//
//  1. modal.Resolve(modalID): a miss ⇒ a modal_answer already consumed + dismissed
//     the modal → no second dismissal. A hit ⇒ the timeout/disconnect/shutdown
//     path (permbridge already denied claude via its own timer or watchApproveConn)
//     → write one content-free fail-closed audit record and broadcast one
//     modal_dismissed so no stale modal lingers (AC-3). The modalbridge one-shot is
//     the SINGLE arbiter of the dismissal broadcast: exactly one of {ResolveAnswer,
//     retire} broadcasts.
//  2. Unconditionally delete the modalID correlation under mu. Resolve comes first
//     so an interaction-required modal cannot lose its negative eligibility while
//     it is still available to a concurrent remote answer.
//
// Runs on the control-server handler goroutine after Await.
func (b *streamApprovalBridge) retire(modalID string) {
	out, ok := b.modal.Resolve(modalID)

	b.mu.Lock()
	delete(b.byModal, modalID)
	b.mu.Unlock()

	if !ok {
		return // a modal_answer already consumed + broadcast this modal's dismissal
	}

	// Timeout / disconnect / shutdown: no answering device, fail-closed deny — the
	// no-device denied_timeout/timeout audit vocabulary. Audit identity is
	// empty by construction; the reason is the modal class, never the modal body.
	audit.Log(b.logger, audit.Entry{
		ModalID:    modalID,
		ModalClass: out.Class,
		Outcome:    audit.OutcomeDeniedTimeout,
		Source:     audit.SourceTimeout,
	})
	b.broadcast(protocol.TypeModalDismissed, protocol.ModalDismissedPayload{
		ModalID: modalID,
		Outcome: string(audit.OutcomeDeniedTimeout),
		Source:  string(audit.SourceTimeout),
	}, "stream_approval.dismissed_push_err")
}

// broadcast marshals payload and fans one control envelope of envType to every
// interactive-capable conn — the shape modal_shown and modal_dismissed share:
// one shared timestamp, the #607 capability gate, a per-bridge monotonic nextID
// (mu-guarded so concurrent Surface/retire number race-free), and a
// Push-error-tolerant loop (a torn-down conn re-syncs on reconnect via
// modalbridge.Snapshot). On daemon ctx teardown it returns early. Runs on the
// control-server handler goroutine, mirroring interactiveModalEmitterV2's
// producer-goroutine fan-out. SECURITY: only the marshal-ready payload (opaque
// ids + the class-fixed option set) and content-free discriminants are emitted;
// a marshal/Push error is logged with the transport sentinel only, never bytes.
func (b *streamApprovalBridge) broadcast(envType string, payload any, pushErrEvent string) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		// Defensive: ModalShownPayload / ModalDismissedPayload are closed
		// string/[]struct types and cannot fail to marshal in practice. Never echo
		// payload bytes or err.Error().
		b.logger.Warn("relay: stream approval broadcast marshal failed",
			"event", "stream_approval.marshal_err")
		return
	}
	ctx := b.ctx
	ts := time.Now().UTC()
	for _, c := range b.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the #607 capability gate — v2 modal events ride interactive
		}
		b.mu.Lock()
		b.nextID++
		id := b.nextID
		b.mu.Unlock()
		env := protocol.Envelope{
			ID:      id,
			Type:    envType,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := b.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			b.logger.Debug("relay: stream approval broadcast push dropped",
				"event", pushErrEvent,
				"conn_id", c.ConnID,
				"env_id", id)
		}
	}
}
