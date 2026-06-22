package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

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
// All fields except reg are single-goroutine (nextID is unguarded): Handle is
// designed to run only on the producer's single Run goroutine (the deferred live
// wiring, #708, feeds it). reg is the one piece a second goroutine touches
// (#717's relay dispatch), and it carries its own mutex.
//
// SECURITY: the modal body (title/prompt/screenText) is application content and
// is NEVER logged at any level. Logs carry only content-free discriminants —
// event, class (a closed wire set), conn_id, env_id — and Push's transport-
// sentinel err.
type interactiveModalEmitterV2 struct {
	reg    *modalbridge.Registry
	bcast  interactiveBroadcaster
	logger *slog.Logger

	// nextID is the session-monotonic per-conn envelope-ID counter, mirroring
	// interactiveTurnEmitterV2.nextID. Written only on the single Handle
	// goroutine; nextID++ runs per conn per envelope.
	nextID uint64
}

// newInteractiveModalEmitterV2 constructs a surfacer wired to a daemon-singleton
// registry (the same instance #717 wires into dispatchAppFrame) and the
// capability-aware broadcaster.
func newInteractiveModalEmitterV2(reg *modalbridge.Registry, bcast interactiveBroadcaster, logger *slog.Logger) *interactiveModalEmitterV2 {
	return &interactiveModalEmitterV2{reg: reg, bcast: bcast, logger: logger}
}

// Handle drives the surfacer one tui-driver event at a time (the single-goroutine
// entry, mirroring interactiveTurnEmitterV2.Handle). screenText is the active
// session's screen already rendered to plain text (ANSI/OSC-free, ADR-025 seal;
// the deferred live wiring feeds Supervisor.ScreenSnapshot). Not safe for
// concurrent use.
//
// On an EventKindPtyModalShown carrying a permission/trust class it builds the
// PermissionRequest, records it (minting the one-time modal_id), and pushes a
// modal_shown envelope to every interactive-capable conn. Every other event —
// and every non-permission/trust class — is a no-op (AC1).
func (e *interactiveModalEmitterV2) Handle(ctx context.Context, ev tuidriver.Event, screenText string) {
	if ev.Kind != tuidriver.EventKindPtyModalShown {
		return // not our event
	}

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

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		// Defensive: ModalShownPayload is a closed string/[]struct and cannot
		// fail to marshal in practice. Never echo the payload or err.Error().
		e.logger.Debug("relay: modal surface drop; payload marshal",
			"event", "interactive_modal.marshal_err",
			"class", class)
		return
	}

	// One timestamp per logical event, shared by every conn. EventID is left
	// nil: a modal_shown is a control event, not part of the turn-event replay
	// ring (forwardEnvelope's dedup never touches EventID==nil envelopes).
	ts := time.Now().UTC()
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate — modal_shown rides the interactive capability (#607)
		}
		e.nextID++
		env := protocol.Envelope{
			ID:      e.nextID,
			Type:    protocol.TypeModalShown,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: modal surface push dropped",
				"event", "interactive_modal.push_err",
				"conn_id", c.ConnID,
				"env_id", e.nextID,
				"class", class,
				"err", err)
		}
	}
}
