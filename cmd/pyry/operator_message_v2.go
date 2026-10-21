package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// operatorMessageQueueSize bounds the hand-off between msgqueue's OnDelivered
// seam and the emitter's Run goroutine. Deliveries are human-paced, so 16
// absorbs any burst; drop-on-full keeps the drain goroutine from ever waiting on
// a push (queueStateQueueSize's reasoning).
const operatorMessageQueueSize = 16

// operatorMessage carries a committed user message or an inert queued receipt.
// The payload, timestamp and durable identity are shared by history and live
// publication. A user message can carry its linked outcome for ordered fan-out.
type operatorMessage struct {
	convID         string
	typ            string
	payload        json.RawMessage
	ts             time.Time
	historyEntryID *uint64
	receipt        *operatorMessage
}

// operatorMessageEmitterV2 pushes delivered user messages and queued receipts
// to every open interactive connection, including the sender. A buffered channel
// decouples unplaced queue confirmations from Run. Placed Claude messages
// call broadcast synchronously from the stream drain; mu serializes both paths.
//
// The event joins the replay ring, so a connection that
// reconnects with an earlier last_event_id gets the message ahead of the reply it
// started. The ring is born in the relay leg, long after the queue, so it reaches
// Run as a parameter at start rather than as a field set at construction. The
// ring has its own lock; interactiveTurnEmitterV2.emit is never called from here,
// because its per-conn counter belongs to the turn drain's goroutine.
//
// SECURITY: the payload carries operator text, message ids and attachment ids.
// It is opaque transit here and never logged; log lines carry only
// conversation_id, conn_id, env_id and Push's transport error.
type operatorMessageEmitterV2 struct {
	in     <-chan operatorMessage
	logger *slog.Logger

	// mu protects the producer counter and fan-out shared by Run and stream placement.
	mu     sync.Mutex
	nextID uint64
	// Queue producers never acquire mu, which can be held during peer delivery.
	pendingMu sync.Mutex
	pending   []operatorMessage
	wake      chan struct{}
}

// newOperatorMessageEmitterV2 builds the emitter over the channel the
// OnDelivered seam sends into. The channel exists before
// msgqueue.New; Run is started by the relay leg.
func newOperatorMessageEmitterV2(in <-chan operatorMessage, logger *slog.Logger) *operatorMessageEmitterV2 {
	return &operatorMessageEmitterV2{in: in, logger: logger, wake: make(chan struct{}, 1)}
}

func (e *operatorMessageEmitterV2) notifyReceipt(m operatorMessage) {
	e.pendingMu.Lock()
	if len(e.pending) == operatorMessageQueueSize {
		e.pendingMu.Unlock()
		e.logger.Warn("relay: queued receipt mailbox full", "event", "send_history.queue_full")
		return
	}
	e.pending = append(e.pending, m)
	e.pendingMu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// operatorMessageNotify builds the push hand-off newOperatorMessageHistory calls
// on each confirmed delivery: a non-blocking send that drops on a full channel
// with a content-free warning, so a slow or absent relay leg never holds up
// delivery. A dropped push remains in source history for thread recovery.
func operatorMessageNotify(ch chan<- operatorMessage, logger *slog.Logger) func(operatorMessage) {
	return func(m operatorMessage) {
		select {
		case ch <- m:
		default:
			logger.Warn("relay: operator-message push queue full; dropping push",
				"event", "operator_message.queue_full",
				"conversation_id", m.convID)
		}
	}
}

// Run drains the hand-off channel until ctx is cancelled. ring is nil in a wiring
// with no stream sink, and the push then goes out without an event_id.
func (e *operatorMessageEmitterV2) Run(ctx context.Context, bcast interactiveBroadcaster, ring *eventring.Ring) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
			e.broadcast(ctx, bcast, ring, operatorMessage{})
		case m, ok := <-e.in:
			if !ok {
				return
			}
			e.broadcast(ctx, bcast, ring, m)
		}
	}
}

// broadcast drains accepted/drop receipts before a user and its linked outcome.
// Each event enters replay once before interactive fan-out, sharing the original
// durable identity, payload and timestamp even with no connections open.
func (e *operatorMessageEmitterV2) broadcast(ctx context.Context, bcast interactiveBroadcaster, ring *eventring.Ring, m operatorMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingMu.Lock()
	pending := e.pending
	e.pending = nil
	e.pendingMu.Unlock()
	for _, receipt := range pending {
		e.publish(ctx, bcast, ring, receipt)
	}
	if m.convID != "" {
		e.publish(ctx, bcast, ring, m)
		if m.receipt != nil {
			e.publish(ctx, bcast, ring, *m.receipt)
		}
	}
}

// publish runs under mu; the user and its outcome form one ordered batch.
func (e *operatorMessageEmitterV2) publish(ctx context.Context, bcast interactiveBroadcaster, ring *eventring.Ring, m operatorMessage) {
	typ := m.typ
	if typ == "" {
		typ = protocol.TypeMessage
	}
	var eventID *uint64
	if ring != nil {
		id := ring.AppendWithHistoryID(m.convID, typ, m.payload, m.ts, m.historyEntryID)
		eventID = &id
	}
	for _, c := range bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate
		}
		e.nextID++
		env := protocol.Envelope{
			ID:             e.nextID,
			Type:           typ,
			TS:             m.ts,
			Payload:        m.payload,
			EventID:        eventID,
			HistoryEntryID: m.historyEntryID,
		}
		if err := bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: operator-message push dropped",
				"event", "operator_message.push_err",
				"conversation_id", m.convID,
				"conn_id", c.ConnID,
				"env_id", e.nextID,
				"err", err)
		}
	}
}

// startOperatorMessageStreamV2 starts the pre-built emitter's Run goroutine over
// bcast and ring, and returns an idempotent cleanup that waits for Run to exit on
// ctx-cancel. It does not close the channel: a late send racing teardown drops
// into the unread buffer (startQueueStateStreamV2's rationale).
func startOperatorMessageStreamV2(ctx context.Context, e *operatorMessageEmitterV2, bcast interactiveBroadcaster, ring *eventring.Ring) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx, bcast, ring)
	}()

	var cleanedUp bool
	return func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		<-done
	}
}
