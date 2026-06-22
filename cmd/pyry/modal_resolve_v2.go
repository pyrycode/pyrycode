package main

import (
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// modalKeystroker routes one abstract modal-resolution keystroke to the live
// claude session. *supervisor.Supervisor satisfies it (#726). Cancel needs only
// SendEsc; #717 extends this interface with Answer/AcceptTrust for the gated
// answer arm.
type modalKeystroker interface {
	SendEsc() error
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

// ResolveAnswer is the deferred no-op answer arm (AC #3): it routes no keystroke,
// does not consume or mutate the modal, writes no audit entry, and returns
// (zero, false) so the manager broadcasts nothing. The escalating ALLOW path
// stays dead until #717 wires the per-device gate (#702) and replaces this body.
// A Debug log is not an audit entry, so AC #3 holds.
func (r *modalResolverV2) ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (relay.ModalDismissal, bool) {
	r.logger.Debug("relay: modal answer deferred (no-op until #717)",
		"event", "modal_answer.deferred",
		"modal_id", modalID)
	return relay.ModalDismissal{}, false
}
