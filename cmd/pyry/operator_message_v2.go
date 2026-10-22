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

// operatorMessageQueueSize bounds the receipt mailbox and placement commands.
const operatorMessageQueueSize = 16

// operatorMessage carries a neutral receipt with its durable identity and timestamp.
type operatorMessage struct {
	convID         string
	typ            string
	payload        json.RawMessage
	ts             time.Time
	historyEntryID *uint64
}

// operatorMessageEmitterV2 publishes queued receipts to interactive connections
// and reconnect replay. Producers hand off through a bounded mailbox without
// waiting for peer delivery. Payloads are never logged.
type operatorMessageEmitterV2 struct {
	logger *slog.Logger

	// mu serializes publication and the producer counter.
	mu     sync.Mutex
	nextID uint64
	// Queue producers never acquire mu, which can be held during peer delivery.
	pendingMu sync.Mutex
	pending   []operatorMessage
	wake      chan struct{}
}

// newOperatorMessageEmitterV2 builds the receipt mailbox before the relay starts.
func newOperatorMessageEmitterV2(logger *slog.Logger) *operatorMessageEmitterV2 {
	return &operatorMessageEmitterV2{logger: logger, wake: make(chan struct{}, 1)}
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

// Run drains the receipt mailbox until ctx is cancelled. ring is nil in a wiring
// with no stream sink, and the push then goes out without an event_id.
func (e *operatorMessageEmitterV2) Run(ctx context.Context, bcast interactiveBroadcaster, ring *eventring.Ring) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
			e.broadcast(ctx, bcast, ring)
		}
	}
}

// broadcast drains receipts in acceptance/outcome order.
// Each event enters replay once before interactive fan-out, sharing the original
// durable identity, payload and timestamp even with no connections open.
func (e *operatorMessageEmitterV2) broadcast(ctx context.Context, bcast interactiveBroadcaster, ring *eventring.Ring) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingMu.Lock()
	pending := e.pending
	e.pending = nil
	e.pendingMu.Unlock()
	for _, receipt := range pending {
		e.publish(ctx, bcast, ring, receipt)
	}
}

// publish runs under mu with a validated neutral receipt.
func (e *operatorMessageEmitterV2) publish(ctx context.Context, bcast interactiveBroadcaster, ring *eventring.Ring, m operatorMessage) {
	typ := m.typ
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
// ctx-cancel. Late producers can only fill the bounded unread mailbox.
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
