package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/supervisor"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// modalKeystroker routes one abstract modal-resolution keystroke to the live
// claude session. *supervisor.Supervisor satisfies all three (#726), so the
// existing production wiring keeps compiling. Cancel needs only SendEsc; the
// gated answer arm adds Answer (permission options) and AcceptTrust (trust
// proceed).
type modalKeystroker interface {
	SendEsc() error
	Answer(choice string) error
	AcceptTrust() error
}

// noopKeystroker is the stream-json bootstrap's modal keystroker: there is no PTY
// to dismiss a modal on, so every actuation is a tolerated no-op. In stream mode a
// permission approval is a permbridge-parked completer resolved through the verdict
// arm (streamApprovalBridge.ResolveStream, #1080), not an on-screen PTY modal; its
// fail-closed deny is the permbridge completer's own deny-on-timeout (#1103). This
// type routes NO keystroke and NEVER touches the permbridge, so it cannot resolve
// an approval to allow. It is modalKeystroker's second implementer, alongside
// *supervisor.Supervisor (#726). #1131.
type noopKeystroker struct{}

func (noopKeystroker) SendEsc() error      { return nil }
func (noopKeystroker) Answer(string) error { return nil }
func (noopKeystroker) AcceptTrust() error  { return nil }

// modalKeystrokerOrNoop returns sup as the modal keystroker when it is a live
// *supervisor.Supervisor (the PTY path), and a noopKeystroker when sup is nil — the
// stream-json bootstrap path, where Session.Supervisor() returns a typed-nil pointer
// (#1077). It is the fourth and final typed-nil w.sup reader guarded, mirroring the
// sibling screenSnapshotterOrNil (#1101).
//
// It takes the CONCRETE *supervisor.Supervisor (not modalKeystroker) so the == nil
// test runs BEFORE boxing: assigning the typed-nil straight into the interface would
// leave a non-nil interface holding a nil pointer, and ResolveCancel / ResolveTimeout
// call kb.SendEsc() UNCONDITIONALLY, so that nil pointer would be dereferenced inside
// sendModalKey → daemon panic. Unlike screenSnapshotterOrNil we must return a NON-NIL
// no-op, not a genuine nil interface: the resolver has no nil-kb arm, and a nil-
// interface method call panics just the same. No-op on the PTY path: a non-nil sup
// passes straight through. #1131.
func modalKeystrokerOrNoop(sup *supervisor.Supervisor) modalKeystroker {
	if sup == nil {
		return noopKeystroker{}
	}
	return sup
}

// modalResolverV2 is the cmd/pyry implementation of relay.ModalResolver: it
// consumes an outstanding modal from the daemon-singleton registry, routes the
// resolving keystroke through the supervisor safe-answer seam, and writes the
// forensic audit record. It is the composition-root binding that lets
// internal/relay stay free of internal/{supervisor,modalbridge,audit} imports.
//
// Both methods run on the v2 manager's single Run dispatch goroutine (the relay
// calls them from dispatchAppFrame). The registry's own mutex is the only
// synchronisation; the supervisor seam and audit sink are themselves safe to
// call from any goroutine.
//
// SECURITY: no modal body/prompt/title and no payload bytes are ever logged; the
// audit entry carries only non-secret identity (device hash/label) + the opaque
// modal_id + outcome/source.
type modalResolverV2 struct {
	reg    *modalbridge.Registry
	kb     modalKeystroker
	logger *slog.Logger

	// activeConv resolves the conversation id to stamp on a folder-not-trusted
	// session_error when a trust modal is denied/timed out (#1014). It is the same
	// follow-active cursor the modal producer resolves its target from
	// (activeConversation.CurrentConversation). nil ⇒ emit disabled.
	activeConv func() string
	// notifyBlocked routes a folder-not-trusted session_error into the shared
	// give-up → session_error frame path (#1008): it is main.go's `blocked`
	// closure (a non-blocking, drop-on-full send into the giveUps channel the
	// sessionErrorEmitterV2 drains). nil ⇒ emit disabled. Both fields are set only
	// at the single production site (relay.go); the 18 test constructions leave
	// them nil, keeping the pre-#1014 behaviour and foreground/v1 inert. #1014.
	notifyBlocked func(convID, reason string)

	// streamApprovals resolves a stream-json permission approval (a
	// permbridge-parked completer, #1103) by modalID: the parallel VERDICT arm to
	// the keystroke arm below. ResolveAnswer dispatches to it when the answered
	// modalID is a stream approval, otherwise routes the tui keystroke. nil ⇒ no
	// stream approvals wired (foreground / v1 / pre-#1080), so ResolveAnswer always
	// routes the keystroke arm. Set at the production site (relay.go) like
	// activeConv/notifyBlocked; the 18 test constructions leave it nil. #1080.
	streamApprovals streamApprovalResolver
}

// streamApprovalResolver resolves a stream-json approval identified by modalID to
// an allow/deny verdict on claude's parked completer (#1080). handled=false ⇒
// modalID is not a stream approval → ResolveAnswer routes the tui keystroke arm.
// *streamApprovalBridge is the production implementer; declared at the consumer
// (CODING-STYLE) so ResolveAnswer's unit tests drive it without the real bridge.
type streamApprovalResolver interface {
	ResolveStream(modalID string, allow bool, denyReason string) (handled bool)
}

// reasonRemoteDeny is the fixed, content-free deny message a remote reject answer
// returns to claude on the stream-json path (#1080). A compile-time constant —
// never host-derived — so it leaks nothing back to claude, mirroring
// reasonFolderNotTrusted here and permbridge's own reasonTimeout.
const reasonRemoteDeny = "permission denied"

// newModalResolverV2 wires the resolver to the daemon-singleton outstanding-modal
// registry (the same instance #708 live-wires the producer/emitter into), the
// supervisor keystroke seam, and the daemon logger. The #1014 emit seams
// (activeConv/notifyBlocked) are nil-default fields set at the production site,
// NOT constructor params — the constructor has 18 test call sites, and nil
// disables the emit (the established "nil disables the optional seam" convention).
func newModalResolverV2(reg *modalbridge.Registry, kb modalKeystroker, logger *slog.Logger) *modalResolverV2 {
	return &modalResolverV2{reg: reg, kb: kb, logger: logger}
}

// Trust-modal wire class string (mirrors modalbridge's unexported classTrust,
// duplicated because it is unexported there — like optProceed/optExit below).
// Only a trust-class deny/timeout surfaces the folder-not-trusted session_error.
const classTrust = "trust"

// reasonFolderNotTrusted is the static, content-free reason carried on the
// folder-not-trusted session_error (#1014 AC-3). Being a compile-time constant it
// can never carry queued phone text, the modal body/prompt/title, or any secret —
// it rides the wire as SessionErrorPayload.Message on the #1008 emitter.
const reasonFolderNotTrusted = "folder not trusted"

// emitFolderNotTrusted surfaces a typed folder-not-trusted session_error for the
// active conversation when trust is refused (a remote deny or the deny-on-timeout),
// reusing the #1008 give-up → session_error frame path so the client sees a
// terminal CodeSessionBlocked instead of a silent retry loop (#1014 AC-2/3). It is
// a no-op when either seam is unset (the 18 test constructions and foreground/v1),
// and never touches the modal body — the reason is the static constant.
func (r *modalResolverV2) emitFolderNotTrusted() {
	if r.notifyBlocked == nil || r.activeConv == nil {
		return
	}
	r.notifyBlocked(r.activeConv(), reasonFolderNotTrusted)
}

// ResolveCancel consumes the named modal and routes the fail-safe ESC dismiss.
// The registry Resolve is the single idempotency gate (AC #4): an unknown or
// already-consumed id returns (zero, false) before any keystroke or audit, so a
// replayed/stale cancel never double-acts. ESC actuation is best-effort — a
// keystroke error (no live session / teardown) is logged and tolerated: the
// modal is already consumed and moot, so the phone must still learn the
// dismissal (broadcast) and the forensic record must still exist (audit).
func (r *modalResolverV2) ResolveCancel(modalID string, dev *devices.Device) (relay.ModalDismissal, bool) {
	out, ok := r.reg.Resolve(modalID)
	if !ok {
		return relay.ModalDismissal{}, false
	}

	if err := r.kb.SendEsc(); err != nil {
		// Best-effort actuation: the modal is already consumed (idempotency
		// committed above), so do NOT abort the audit/broadcast — that would
		// orphan the consumed modal. err is a supervisor sentinel / transport
		// error, never a secret.
		r.logger.Warn("relay: modal cancel keystroke failed",
			"event", "modal_cancel.keystroke_err",
			"modal_id", modalID,
			"err", err)
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

// ResolveTimeout safe-denies an unanswered modal when its deny-on-timeout window
// elapses (#725). It mirrors ResolveCancel — consume (the single idempotency
// gate) → best-effort ESC → audit → return the dismissal — with two differences:
// it takes NO device (a timeout has no answering device, so the audit
// DeviceHash/DeviceLabel are empty, the documented no-device-timeout case), and
// outcome/source are denied_timeout/timeout instead of cancelled/remote. An
// unknown or already-consumed id (an answer/cancel won the race) returns
// (zero, false) before any keystroke or audit — the AC #2 loser path, identical
// to ResolveCancel's unknown-id no-op.
//
// ESC is the fail-closed deny for both modal classes (ADR 025 § Security model
// "answered with the SAFE default (deny / ESC)"): for a permission modal ESC
// dismisses the prompt (claude treats it as deny); for a trust modal ESC is the
// "exit" = deny option (classifyAnswer maps exit → verbEsc). It is the same
// keystroke cancel routes — only the audit classification differs.
//
// Runs on the manager's single Run dispatch goroutine (handleModalTimeout calls
// it). SECURITY: no modal body/prompt/title and no payload bytes are ever logged.
func (r *modalResolverV2) ResolveTimeout(modalID string) (relay.ModalDismissal, bool) {
	out, ok := r.reg.Resolve(modalID)
	if !ok {
		return relay.ModalDismissal{}, false
	}

	if err := r.kb.SendEsc(); err != nil {
		// Best-effort actuation: the modal is already consumed (idempotency
		// committed above), so do NOT abort the audit/broadcast — that would
		// orphan the consumed modal. err is a supervisor sentinel, never a secret.
		r.logger.Warn("relay: modal timeout keystroke failed",
			"event", "modal_timeout.keystroke_err",
			"modal_id", modalID,
			"err", err)
	}

	// No answering device on a timeout ⇒ the audit identity is empty by
	// construction (the no-device-timeout case audit.Entry documents).
	audit.Log(r.logger, audit.Entry{
		ModalID:    modalID,
		ModalClass: out.Class,
		Outcome:    audit.OutcomeDeniedTimeout,
		Source:     audit.SourceTimeout,
	})

	// A denied-on-timeout TRUST folder surfaces a typed session_error so the
	// client learns trust was refused instead of watching a silent retry loop
	// (#1014 AC-2/3). Strictly after the pre-built consume/ESC/audit; only the
	// trust class emits (a permission-class timeout does not).
	if out.Class == classTrust {
		r.emitFolderNotTrusted()
	}

	// One source vocabulary feeds both the wire dismissal and the audit entry.
	return relay.ModalDismissal{
		Outcome: string(audit.OutcomeDeniedTimeout),
		Source:  string(audit.SourceTimeout),
	}, true
}

// ResolveAnswer is the security-critical gated answer arm: it routes an
// internet-sourced modal_answer into claude's permission prompt ONLY from a
// gated device. The hard invariant is that nothing but a fully-authorized, valid
// answer may consume the modal or route a keystroke — so the order is Lookup →
// fail-closed eligibility gate → option classification → consume → keystroke →
// audit (gate before consume).
//
// answerToken is the client's idempotency key (uniqueness matters, secrecy does
// not — it is NOT authorization). The daemon's dedup is the modal_id one-shot
// Resolve below, which already collapses a replay to the unknown-id no-op (no
// second dismissal — exactly what AC #2 demands). A server-side token store that
// re-broadcast the prior result would VIOLATE "no second dismissal" and add
// unbounded state, so it is deliberately not built. The token is decoded (it
// arrives as a param) and otherwise unused, and never logged.
func (r *modalResolverV2) ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (relay.ModalDismissal, bool) {
	_ = answerToken // see method doc: decoded, not used server-side, never logged.

	// Step 1: Lookup (read, no consume). Stale / unknown / already-resolved (a
	// replay or reorder of an answer whose modal_id was already consumed) all
	// miss here ⇒ no keystroke, no mutation, no audit. This is AC #2's
	// idempotency / first-answer-wins.
	out, ok := r.reg.Lookup(modalID)
	if !ok {
		return relay.ModalDismissal{}, false
	}

	// Step 2: fail-closed eligibility gate, BEFORE classification and BEFORE
	// consume — the load-bearing ordering. A nil/unauthenticated device or an
	// unset opt-in bit denies; the modal is left outstanding (Lookup only) for a
	// legitimate local answer or the #725 deny-on-timeout. Audited
	// denied_unauthorized with the (possibly empty) non-secret identity.
	if !dev.MayAnswerRemotePermission() {
		r.auditAnswer(dev, modalID, out.Class, audit.OutcomeDeniedUnauthorized)
		return relay.ModalDismissal{}, false
	}

	// Step 3: map option_id → (outcome, keystroke) against THIS modal's surfaced
	// options. A forged or wrong-class option_id is not a locatable option ⇒
	// reject with no keystroke, no consume, no audit (no security decision was
	// made; it is a malformed client frame). Warn-logged with a length-bounded
	// option_id (it is attacker-controlled; slog JSON-escapes it).
	outcome, verb, choice, ok := classifyAnswer(out, optionID)
	if !ok {
		r.logger.Warn("relay: modal answer invalid option",
			"event", "modal_answer.invalid_option",
			"modal_id", modalID,
			"option_id", truncateForLog(optionID, 64))
		return relay.ModalDismissal{}, false
	}

	// Step 4/5: consume FIRST (commit idempotency), then route best-effort. The
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

	// Actuate the answer, best-effort exactly like ResolveCancel: the modal is
	// already consumed and moot, so an error is Warn-logged and tolerated — the
	// dismissal must still broadcast and the audit must still be written; aborting
	// would orphan a consumed modal. Two parallel arms:
	//   - STREAM (#1080): a permbridge-parked approval keyed by modalID resolves
	//     its completer to allow/deny (echoing the parked tool input on allow).
	//     ResolveStream reports handled=true only when modalID is a stream approval.
	//   - KEYSTROKE (tui): every other modalID — and every modal when no stream
	//     bridge is wired (foreground/v1, streamApprovals==nil) — routes the
	//     safe-answer keystroke into the on-screen modal (unchanged pre-#1080 path).
	handled := r.streamApprovals != nil && r.streamApprovals.ResolveStream(modalID, allow, reasonRemoteDeny)
	if !handled {
		if err := r.routeAnswerKeystroke(verb, choice); err != nil {
			r.logger.Warn("relay: modal answer keystroke failed",
				"event", "modal_answer.keystroke_err",
				"modal_id", modalID,
				"err", err)
		}
	}

	decision := audit.OutcomeDenied
	if allow {
		decision = audit.OutcomeAllowed
	}
	r.auditAnswer(dev, modalID, out.Class, decision)

	// A remote DENY of the trust folder (exit → OutcomeDeny) surfaces the same
	// typed session_error as the deny-on-timeout (#1014 AC-2/3). A trust proceed
	// (OutcomeAllow) does NOT emit — the held turn runs once trust clears — and
	// permission answers never emit (scoped to the trust class).
	if out.Class == classTrust && outcome == devices.OutcomeDeny {
		r.emitFolderNotTrusted()
	}

	// The WIRE dismissal Outcome is the answered option_id (ModalDismissedPayload
	// contract), NOT the audit classification. Source is remote.
	return relay.ModalDismissal{Outcome: optionID, Source: string(audit.SourceRemote)}, true
}

// answerVerb is the safe-answer keystroke an option_id maps to, one level up
// from the keystroker's verb methods (mirrors supervisor's unexported modalKey).
type answerVerb int

const (
	verbAnswer      answerVerb = iota // → kb.Answer(choice) (permission options)
	verbAcceptTrust                   // → kb.AcceptTrust()  (trust proceed)
	verbEsc                           // → kb.SendEsc()      (trust exit / dismiss)
)

// Trust-modal option ids on the wire (mirrors modalbridge's unexported
// optProceed/optExit, duplicated because they are unexported there): proceed →
// accept (allow), exit → ESC dismiss (deny).
const (
	optProceed = "proceed"
	optExit    = "exit"
)

// classifyAnswer locates optionID within o.Options and maps it to the grant
// outcome + the keystroke verb to actuate. ok=false if optionID is not a
// locatable option of THIS modal (forged / wrong-class / unknown id) — the
// caller rejects with no keystroke, no consume, no audit.
//
// Membership in the surfaced o.Options is the single source of truth: a
// permission option's keystroke is its 1-based position in claude's display
// order (the producer builds o.Options in that order, #716), so deriving the
// digit from the index keeps one source of truth and gives free membership
// validation. choice is unused for the trust verbs.
func classifyAnswer(o modalbridge.Outstanding, optionID string) (outcome devices.RemotePermissionOutcome, verb answerVerb, choice string, ok bool) {
	idx := slices.IndexFunc(o.Options, func(opt protocol.ModalOption) bool { return opt.ID == optionID })
	if idx < 0 {
		return 0, 0, "", false
	}
	switch optionID {
	case string(turnevent.PermissionOptionKindAllowOnce), string(turnevent.PermissionOptionKindAllowAlways):
		return devices.OutcomeAllow, verbAnswer, strconv.Itoa(idx + 1), true
	case string(turnevent.PermissionOptionKindRejectOnce), string(turnevent.PermissionOptionKindRejectAlways):
		return devices.OutcomeDeny, verbAnswer, strconv.Itoa(idx + 1), true
	case optProceed:
		return devices.OutcomeAllow, verbAcceptTrust, "", true
	case optExit:
		return devices.OutcomeDeny, verbEsc, "", true
	default:
		return 0, 0, "", false
	}
}

// routeAnswerKeystroke actuates the classified verb on the keystroker. The
// switch shape mirrors supervisor.sendModalKeystroke; choice is used only by
// verbAnswer. The default is unreachable from classifyAnswer (which returns only
// the three verbs with ok=true) — return loudly rather than panic per
// CODING-STYLE.
func (r *modalResolverV2) routeAnswerKeystroke(verb answerVerb, choice string) error {
	switch verb {
	case verbAnswer:
		return r.kb.Answer(choice)
	case verbAcceptTrust:
		return r.kb.AcceptTrust()
	case verbEsc:
		return r.kb.SendEsc()
	default:
		return fmt.Errorf("unknown answer verb %d", int(verb))
	}
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
//   - ResolveStream (relay Run goroutine, via modalResolverV2.ResolveAnswer): a
//     client's modal_answer resolves the parked completer to allow/deny (AC-2).
//   - retire (control-server handler goroutine, post-Await): the guaranteed
//     cleanup + client-dismissal backstop for every terminal path (AC-3).
//
// SECURITY: the bridge NEVER logs req.Input, the tool_name, the modal
// prompt/title, or a deny message beyond the fixed reasonRemoteDeny constant.
// Only content-free discriminants (event, modal_id, conn_id, env_id, class) and
// the transport-sentinel Push err reach any log field — the same discipline the
// modal emitter and control server hold.
type streamApprovalBridge struct {
	perm       *permbridge.Registry   // claude-facing completer store (#1103)
	modal      *modalbridge.Registry  // client-facing modal store (#716)
	bcast      interactiveBroadcaster // *relay.V2SessionManager (ActiveConns/Push)
	activeConv func() string          // follow-active convID scoping (#1065)
	ctx        context.Context        // daemon ctx captured at construction, for broadcasts
	logger     *slog.Logger

	// mu is a leaf lock guarding byModal + nextID ONLY: held around O(1) map ops
	// and the counter bump, never across modal.Record, perm.Lookup/perm.Resolve,
	// modal.Resolve, or a Push — so bridge.mu → registry.mu never nests and there
	// is no deadlock order to reason about.
	mu      sync.Mutex
	byModal map[string]string // modalID → toolUseID
	nextID  uint64            // per-bridge control-envelope counter
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
		byModal:    make(map[string]string),
	}
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
func (b *streamApprovalBridge) Surface(req permbridge.Request) (retire func()) {
	permReq, wireClass, ok := modalbridge.PermissionRequestForClass(tuidriver.ModalClassPermission, req.ToolName)
	if !ok {
		// Unreachable: ModalClassPermission always maps. Defensive — never surface
		// an unbuilt modal; claude times out to deny (fail-closed).
		return func() {}
	}

	payload, err := b.modal.Record(permReq, wireClass, b.activeConv())
	if err != nil {
		// crypto/rand failure — drop the modal (no modal_shown, no correlation);
		// claude times out to deny. Never echo err detail; no payload/screen bytes.
		b.logger.Warn("relay: stream approval surface drop; id mint",
			"event", "stream_approval.rand_err")
		return func() {}
	}
	modalID := payload.ModalID

	b.mu.Lock()
	b.byModal[modalID] = req.ToolUseID
	b.mu.Unlock()

	b.broadcast(protocol.TypeModalShown, payload, "stream_approval.push_err")

	return func() { b.retire(modalID) }
}

// ResolveStream is modalResolverV2.ResolveAnswer's stream (verdict) arm: resolve a
// stream-json approval identified by modalID to allow/deny on claude's parked
// completer (AC-2). handled=false ⇒ modalID is not a stream approval (absent from
// byModal) → the caller routes the tui keystroke arm. An allow echoes the parked
// tool Input byte-verbatim; a perm.Lookup miss on allow means permbridge already
// resolved (a raced timeout) — nothing to do, still fail-closed (claude denied).
// denyReason is the fixed content-free reasonRemoteDeny, never host-derived.
//
// It does NOT delete the correlation (retire is the sole, unconditional deleter)
// and does NOT consume the modalbridge entry (ResolveAnswer already consumed it
// before calling here, which gates a second ResolveStream for the same modalID).
// Runs on the relay manager's single Run goroutine.
func (b *streamApprovalBridge) ResolveStream(modalID string, allow bool, denyReason string) (handled bool) {
	b.mu.Lock()
	toolUseID, ok := b.byModal[modalID]
	b.mu.Unlock()
	if !ok {
		return false // not a stream approval — caller routes the keystroke arm
	}

	if allow {
		if req, ok := b.perm.Lookup(toolUseID); ok {
			b.perm.Resolve(toolUseID, permbridge.Allow(req.Input))
		}
	} else {
		b.perm.Resolve(toolUseID, permbridge.Deny(denyReason))
	}
	return true
}

// retire is the guaranteed cleanup + dismissal backstop the control server defers
// for a surfaced stream approval. It runs on EVERY Await return (answer, timeout,
// disconnect, shutdown) in two steps with separate arbiters:
//
//  1. Unconditionally delete the modalID→toolUseID correlation under mu. This is
//     the SOLE correlation deleter and it runs on every terminal path — including
//     the answer path, where step 2 no-ops — so byModal never leaks (the MUST-FIX
//     the no-correlation-leak test guards). Delete of an absent key is a safe
//     no-op, so racing a concurrent delete is irrelevant.
//  2. modal.Resolve(modalID): a miss ⇒ a modal_answer already consumed + dismissed
//     the modal → no second dismissal. A hit ⇒ the timeout/disconnect/shutdown
//     path (permbridge already denied claude via its own timer or watchApproveConn)
//     → write one content-free fail-closed audit record and broadcast one
//     modal_dismissed so no stale modal lingers (AC-3). The modalbridge one-shot is
//     the SINGLE arbiter of the dismissal broadcast: exactly one of {ResolveAnswer,
//     retire} broadcasts.
//
// Runs on the control-server handler goroutine after Await.
func (b *streamApprovalBridge) retire(modalID string) {
	b.mu.Lock()
	delete(b.byModal, modalID)
	b.mu.Unlock()

	out, ok := b.modal.Resolve(modalID)
	if !ok {
		return // a modal_answer already consumed + broadcast this modal's dismissal
	}

	// Timeout / disconnect / shutdown: no answering device, fail-closed deny — the
	// no-device deny-on-timeout vocabulary ResolveTimeout uses. Audit identity is
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
