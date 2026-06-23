package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// interactiveModalEmitterV2 is the outbound modal surfacer (ADR 025 § Modal,
// Phase 3): it turns a detected tui-driver permission/trust modal into a typed
// modal_shown envelope and fans it out to interactive-capable phones — never raw
// PTY bytes. It mirrors interactiveTurnEmitterV2's capability-gated fan-out and,
// like it, is a passive state machine: it spawns no goroutine and owns no queue.
// Its only synchronisation is the Registry's mutex.
//
// All fields except reg are single-goroutine (nextID, outstandingID, and
// outstandingClass are unguarded): Handle is designed to run only on the
// producer's single Run goroutine (the deferred live wiring, #708, feeds it).
// reg is the one piece a second goroutine touches (#717's relay dispatch, #706's
// cross-head Resolve race), and it carries its own mutex.
//
// SECURITY: the modal body (title/prompt/screenText) is application content and
// is NEVER logged at any level. Logs carry only content-free discriminants —
// event, class (a closed wire set), conn_id, env_id — and Push's transport-
// sentinel err.
type interactiveModalEmitterV2 struct {
	reg    *modalbridge.Registry
	bcast  interactiveBroadcaster
	armer  modalTimeoutArmer
	logger *slog.Logger

	// nextID is the session-monotonic per-conn envelope-ID counter, mirroring
	// interactiveTurnEmitterV2.nextID. Written only on the single Handle
	// goroutine; nextID++ runs per conn per envelope.
	nextID uint64

	// outstandingID / outstandingClass track the modal whose modal_shown this
	// emitter most recently surfaced, so a later EventKindPtyModalHidden can be
	// correlated back to its modal_id (Hidden carries only the class, #706).
	// Single-goroutine like nextID; "" / ModalClassUnknown when nothing is
	// outstanding.
	outstandingID    string
	outstandingClass tuidriver.ModalClass
}

// modalTimeoutArmer arms the daemon-side deny-on-timeout for a surfaced modal
// (#725). Defined here on the consumer side rather than widening the shared
// interactiveBroadcaster (which three emitters implement) — only this surfacer
// arms timeouts. *relay.V2SessionManager satisfies it structurally; #708 wires
// the live manager here.
type modalTimeoutArmer interface {
	ArmModalTimeout(ctx context.Context, modalID string)
}

// newInteractiveModalEmitterV2 constructs a surfacer wired to a daemon-singleton
// registry (the same instance #717 wires into dispatchAppFrame), the
// capability-aware broadcaster, and the deny-on-timeout armer (#725).
func newInteractiveModalEmitterV2(reg *modalbridge.Registry, bcast interactiveBroadcaster, armer modalTimeoutArmer, logger *slog.Logger) *interactiveModalEmitterV2 {
	return &interactiveModalEmitterV2{reg: reg, bcast: bcast, armer: armer, logger: logger}
}

// Handle drives the surfacer one tui-driver event at a time (the single-goroutine
// entry, mirroring interactiveTurnEmitterV2.Handle). screenText is the active
// session's screen already rendered to plain text (ANSI/OSC-free, ADR-025 seal;
// the deferred live wiring feeds Supervisor.ScreenSnapshot). Not safe for
// concurrent use.
//
// On an EventKindPtyModalShown carrying a permission/trust class it surfaces a
// modal_shown to every interactive-capable conn (handleModalShown). On an
// EventKindPtyModalHidden for the outstanding modal it resolves it through the
// shared registry and — only if this head won the cross-head race — broadcasts a
// modal_dismissed{local} (handleModalHidden, #706). Every other event — and
// every non-permission/trust class — is a no-op (AC1).
func (e *interactiveModalEmitterV2) Handle(ctx context.Context, ev tuidriver.Event, screenText string) {
	switch ev.Kind {
	case tuidriver.EventKindPtyModalShown:
		e.handleModalShown(ctx, ev, screenText)
	case tuidriver.EventKindPtyModalHidden:
		e.handleModalHidden(ctx, ev) // screenText unused: the body is already gone
	}
}

// handleModalShown surfaces a permission/trust modal: build the PermissionRequest,
// Record it (minting the one-time modal_id), arm the deny-on-timeout, track the
// id+class for a later Hidden (#706), and fan a modal_shown to every interactive
// conn. A non-permission/trust class is a no-op (AC1).
func (e *interactiveModalEmitterV2) handleModalShown(ctx context.Context, ev tuidriver.Event, screenText string) {
	req, class, ok := modalbridge.PermissionRequestForClass(ev.Modal, screenText)
	if !ok {
		// Non-permission/trust class (slash-picker, model-select, mcp, …): no
		// modal_shown (AC1). class string omitted — ev.Modal is the only fact.
		e.logger.Debug("relay: modal surface skip; non-permission class",
			"event", "interactive_modal.skip",
			"class", string(ev.Modal))
		return
	}

	payload, err := e.reg.Record(req, class)
	if err != nil {
		// crypto/rand failure — drop the modal; never push an id-less payload.
		// Never echo err detail beyond the sentinel; no payload/screen bytes.
		e.logger.Warn("relay: modal surface drop; id mint",
			"event", "interactive_modal.rand_err",
			"class", class)
		return
	}

	// Arm the fail-closed deny-on-timeout BEFORE the marshal/broadcast (#725):
	// the arm is unconditional on a successful Record, so a modal that records
	// but fails to marshal — or surfaces to zero interactive conns — is still
	// safe-denied on the window. claude is blocked on the prompt regardless of
	// who is watching, so it must be denied if nothing resolves it.
	e.armer.ArmModalTimeout(ctx, payload.ModalID)

	// Track the just-surfaced modal so a later EventKindPtyModalHidden (which
	// carries only the class) correlates back to this modal_id. Set here —
	// consistent with the registry entry and the armed timeout — even on the
	// defensive marshal-failure return below (#706).
	e.outstandingID = payload.ModalID
	e.outstandingClass = ev.Modal

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		// Defensive: ModalShownPayload is a closed string/[]struct and cannot
		// fail to marshal in practice. Never echo the payload or err.Error().
		e.logger.Debug("relay: modal surface drop; payload marshal",
			"event", "interactive_modal.marshal_err",
			"class", class)
		return
	}

	e.broadcastInteractive(ctx, protocol.TypeModalShown, payloadJSON, "interactive_modal.push_err")
}

// handleModalHidden is the local resolution arm (ADR 025 §4 first-answer-wins):
// when the operator answers a modal at the local pyry attach TTY, claude's modal
// vanishes and tui-driver fires EventKindPtyModalHidden for the just-hidden
// class. This correlates that class back to the modal_id this emitter surfaced,
// Resolves it through the shared registry, and — only if this head wins the race
// against a remote answer/cancel (#717/#727) or the deny-on-timeout (#725) —
// audits the local resolution and broadcasts one modal_dismissed{source: local}.
//
// The daemon cannot observe WHICH option the operator picked locally (only that
// the modal vanished), so the dismissal carries a producer-defined sentinel
// outcome (dismissed_local), not an option_id; the phone uses it only to clear
// its prompt.
func (e *interactiveModalEmitterV2) handleModalHidden(ctx context.Context, ev tuidriver.Event) {
	if e.outstandingID == "" {
		return // nothing this emitter surfaced is outstanding (e.g. a Hidden for a non-permission modal we never recorded) — AC1's implicit "for the outstanding modal" gate
	}
	if ev.Modal != e.outstandingClass {
		// Defensive, unreachable under tui-driver's single-modal invariant (the
		// showing modal's class always matches the tracked one). If tui-driver
		// ever drifts, leave the tracking intact so the correct Hidden can still
		// resolve it. Content-free warn.
		e.logger.Warn("relay: modal hidden class mismatch",
			"event", "interactive_modal.hidden_class_mismatch",
			"hidden_class", string(ev.Modal),
			"outstanding_class", string(e.outstandingClass))
		return
	}

	// The modal is gone from the local screen regardless of who consumes the
	// registry entry — clear the tracking unconditionally once the class matches
	// so no return path leaks a stale id.
	id := e.outstandingID
	e.outstandingID = ""
	e.outstandingClass = tuidriver.ModalClassUnknown

	out, ok := e.reg.Resolve(id)
	if !ok {
		// First-answer-wins loser: a remote modal_answer/modal_cancel (#717/#727)
		// or the deny-on-timeout (#725) already consumed this modal_id. No audit,
		// no broadcast, no second modal_dismissed (AC2 / AC3-(b)).
		return
	}

	// Winner: exactly one audit record, then one modal_dismissed{local} to every
	// interactive conn. No answering device on a local TTY resolution, so the
	// audit identity is empty by construction (the no-device case ResolveTimeout
	// uses). One source vocabulary feeds both the wire dismissal and the audit.
	audit.Log(e.logger, audit.Entry{
		ModalID:    id,
		ModalClass: out.Class,
		Outcome:    audit.OutcomeDismissedLocal,
		Source:     audit.SourceLocal,
	})

	payloadJSON, err := json.Marshal(protocol.ModalDismissedPayload{
		ModalID: id,
		Outcome: string(audit.OutcomeDismissedLocal),
		Source:  string(audit.SourceLocal),
	})
	if err != nil {
		// Defensive: ModalDismissedPayload is a closed three-string struct and
		// cannot fail to marshal in practice. The modal is already Resolved +
		// audited, so the registry stays consistent; the missed phone re-syncs on
		// reconnect (mirrors broadcastModalDismissed). Never echo payload bytes.
		e.logger.Warn("relay: modal dismissed drop; payload marshal",
			"event", "interactive_modal.dismissed_marshal_err",
			"modal_id", id)
		return
	}

	e.broadcastInteractive(ctx, protocol.TypeModalDismissed, payloadJSON, "interactive_modal.dismissed_push_err")
}

// broadcastInteractive fans one control envelope of envType carrying payloadJSON
// to every interactive-capable conn — the shape modal_shown and modal_dismissed
// share: one shared timestamp, the #607 capability gate, a per-conn monotonic
// nextID, and a Push-error-tolerant loop (a torn-down conn re-syncs on reconnect).
// EventID is left nil: these are control events, not part of the turn-event
// replay ring (forwardEnvelope's dedup never touches EventID==nil envelopes). On
// ctx teardown it returns early. pushErrEvent tags the per-conn push-drop debug
// log. Producer-goroutine only (it advances the unguarded nextID).
func (e *interactiveModalEmitterV2) broadcastInteractive(ctx context.Context, envType string, payloadJSON []byte, pushErrEvent string) {
	ts := time.Now().UTC()
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate — v2 modal events ride the interactive capability (#607)
		}
		e.nextID++
		env := protocol.Envelope{
			ID:      e.nextID,
			Type:    envType,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: modal broadcast push dropped",
				"event", pushErrEvent,
				"conn_id", c.ConnID,
				"env_id", e.nextID,
				"err", err)
		}
	}
}
