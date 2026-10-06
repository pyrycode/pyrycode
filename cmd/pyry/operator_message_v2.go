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

// operatorMessage is one confirmed operator turn, handed from
// newOperatorMessageHistory to the live push (#2699). payload and ts are the
// very values the durable log entry was written with, so the log and the wire
// cannot differ.
type operatorMessage struct {
	convID         string
	payload        json.RawMessage
	ts             time.Time
	historyEntryID *uint64
}

// operatorMessageEmitterV2 pushes the operator's own delivered message, as a
// `message` envelope with role "user", to every open INTERACTIVE conn, the
// sender's included (#2699). It mirrors queueStateEmitterV2: a buffered channel
// decouples unplaced queue confirmations from Run. Placed Claude messages
// call broadcast synchronously from the stream drain; mu serializes both paths.
//
// Unlike queue_state the event joins the #647 replay ring, so a conn that
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
}

// newOperatorMessageEmitterV2 builds the emitter over the channel the
// OnDelivered seam sends into. Like queueChanges, the channel exists before
// msgqueue.New; Run is started by the relay leg.
func newOperatorMessageEmitterV2(in <-chan operatorMessage, logger *slog.Logger) *operatorMessageEmitterV2 {
	return &operatorMessageEmitterV2{in: in, logger: logger}
}

// operatorMessageNotify builds the push hand-off newOperatorMessageHistory calls
// on each confirmed delivery: a non-blocking send that drops on a full channel
// with a content-free warning, so a slow or absent relay leg never holds up
// delivery. A dropped push is still in the durable log, where request_history
// finds it.
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
		case m, ok := <-e.in:
			if !ok {
				return
			}
			e.broadcast(ctx, bcast, ring, m)
		}
	}
}

// broadcast appends the message to the replay ring once, before the fan-out, so
// it is retained with no conn open, then pushes one envelope per interactive
// conn sharing that one event id, payload and stamp — the shape of
// interactiveTurnEmitterV2.emit.
func (e *operatorMessageEmitterV2) broadcast(ctx context.Context, bcast interactiveBroadcaster, ring *eventring.Ring, m operatorMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var eventID *uint64
	if ring != nil {
		id := ring.Append(m.convID, protocol.TypeMessage, m.payload, m.ts)
		eventID = &id
	}
	for _, c := range bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate
		}
		e.nextID++
		env := protocol.Envelope{
			ID:             e.nextID,
			Type:           protocol.TypeMessage,
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
