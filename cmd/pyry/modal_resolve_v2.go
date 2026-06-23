package main

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
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
}

// newModalResolverV2 wires the resolver to the daemon-singleton outstanding-modal
// registry (the same instance #708 live-wires the producer/emitter into), the
// supervisor keystroke seam, and the daemon logger.
func newModalResolverV2(reg *modalbridge.Registry, kb modalKeystroker, logger *slog.Logger) *modalResolverV2 {
	return &modalResolverV2{reg: reg, kb: kb, logger: logger}
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

	// Keystroke is best-effort exactly like ResolveCancel: the modal is already
	// consumed and moot, so a keystroke error (no live session / teardown) is
	// Warn-logged with the supervisor sentinel and tolerated — the dismissal
	// must still broadcast and the audit must still be written. Aborting would
	// orphan a consumed modal.
	if err := r.routeAnswerKeystroke(verb, choice); err != nil {
		r.logger.Warn("relay: modal answer keystroke failed",
			"event", "modal_answer.keystroke_err",
			"modal_id", modalID,
			"err", err)
	}

	// AuthorizeRemotePermission (#702) splits allowed (true) from denied (false).
	// For an eligible device this reduces to outcome==OutcomeAllow, but calling
	// the primitive keeps the fail-closed conjunction in its single unit-tested
	// place (it re-checks eligibility — defense in depth).
	decision := audit.OutcomeDenied
	if devices.AuthorizeRemotePermission(dev, outcome) {
		decision = audit.OutcomeAllowed
	}
	r.auditAnswer(dev, modalID, out.Class, decision)

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
