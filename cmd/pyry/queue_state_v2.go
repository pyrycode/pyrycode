package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// queueStateQueueSize bounds the buffered hand-off between msgqueue's off-lock
// change seam (OnChange) and the emitter's Run goroutine. Backlog changes are
// rare and human-paced (a phone enqueues a turn, a drain advances, a dequeue
// removes); 16 absorbs a burst, and drop-on-full bounds memory while never
// stalling a drain goroutine (msgqueue's ChangeFunc MUST-NOT-BLOCK contract).
// Drop-on-full is safe because the seam is edge-triggered and Snapshot re-reads
// current state — a dropped notification is recovered by the next change.
const queueStateQueueSize = 16

// queueStateEmitterV2 fans a queue_state v2 envelope to every open INTERACTIVE
// conn when a conversation's inbound backlog changes (#722). It mirrors
// sessionTransitionEmitterV2: a buffered `in` channel decouples msgqueue's
// off-lock OnChange seam (a non-blocking send, per ChangeFunc's MUST-NOT-BLOCK
// contract) from the Run goroutine that performs the blocking ActiveConns
// snapshot and the per-conn Push. This decoupling is load-bearing: OnChange
// fires from a MIX of goroutines (the enqueue/remove callers run on the
// manager's Run goroutine; each drain runs on its own goroutine), so neither an
// inline ActiveConns (deadlocks the manager) nor an inline m.sessions read
// (valid only on Run, not on a drain) is correct for all callers. The single
// dedicated Run goroutine — neither the manager's Run goroutine nor a drain —
// makes ActiveConns safe and nextID single-goroutine.
//
// The interactive-only capability filter is the delivery gate — a phone that
// never negotiated interactive receives only the coarse v1 fan-out, never this
// event.
//
// SECURITY: the queued `text` is untrusted, phone-originated content. It flows
// Snapshot → toQueueStatePayload → Push as opaque transit and is NEVER logged
// at any level. Every log line carries only content-free discriminants —
// `event`, `conversation_id` (a non-secret routing id), `conn_id`, `env_id`,
// and Push's transport-sentinel `err`. The marshaled payload bytes and
// err.Error() on the marshal path are never logged (encoding/json can quote
// input bytes into its error). Same discipline as the three sibling emitters
// and msgqueue itself.
type queueStateEmitterV2 struct {
	// in is the shared hand-off channel; the OnChange seam (queueStateNotify)
	// sends convIDs in, Run receives them. The channel is the only state shared
	// across goroutines — channels are concurrency-safe.
	in <-chan string
	// snapshot is bound to queue.Snapshot at construction; read-only on Run.
	snapshot func(convID string) []msgqueue.QueuedMessage
	logger   *slog.Logger

	// nextID is the per-conn envelope-ID counter (mirrors the sibling emitters).
	// Read/written only on the single Run goroutine (broadcast is serial) — no
	// atomic needed. EventID is left nil: queue_state does not join the #647/#649
	// reconnect-replay ring (idempotent full-state; only the latest backlog
	// matters), and is a never-drop control event automatically (pushQueue.enqueue
	// evicts only TypeAssistantDelta).
	nextID uint64
}

// newQueueStateEmitterV2 constructs an emitter over the shared hand-off channel
// `in` and the queue's Snapshot. It is built AFTER queue.New (so Snapshot is
// bound) but the channel is created BEFORE queue.New and shared with the
// OnChange seam — that breaks the chicken-and-egg without any late-bound field.
// Run must be called once on a goroutine before notifications are delivered.
func newQueueStateEmitterV2(in <-chan string, snapshot func(convID string) []msgqueue.QueuedMessage, logger *slog.Logger) *queueStateEmitterV2 {
	return &queueStateEmitterV2{
		in:       in,
		snapshot: snapshot,
		logger:   logger,
	}
}

// queueStateNotify builds the msgqueue.ChangeFunc seam: a closure that does a
// non-blocking buffered send of convID with drop-on-full + a content-free Warn.
// It NEVER blocks, satisfying ChangeFunc's MUST-NOT-BLOCK contract on the drain
// path, and is safe for concurrent invocation (the enqueue caller, each drain,
// and the remove caller all fire it) because a channel send is concurrency-safe.
// It captures the channel, not the emitter, so the emitter is never read off the
// constructing goroutine. A dropped notification is recovered by the next change
// (Snapshot re-reads current state — only the final state matters).
func queueStateNotify(ch chan<- string, logger *slog.Logger) msgqueue.ChangeFunc {
	return func(convID string) {
		select {
		case ch <- convID:
		default:
			logger.Warn("relay: queue-state change queue full; dropping notification",
				"event", "queue_state.queue_full",
				"conversation_id", convID)
		}
	}
}

// Run drains the hand-off channel until ctx is cancelled, broadcasting a
// queue_state for each changed conversation. bcast is a goroutine-local
// parameter (supplied at start, when mgr exists) — no stored broadcaster field,
// no race. Mirrors sessionTransitionEmitterV2.Run.
func (e *queueStateEmitterV2) Run(ctx context.Context, bcast interactiveBroadcaster) {
	for {
		select {
		case <-ctx.Done():
			return
		case convID, ok := <-e.in:
			if !ok {
				return
			}
			e.broadcast(ctx, bcast, convID)
		}
	}
}

// broadcast snapshots ONLY convID's backlog, maps it to the wire payload,
// marshals it once, then fans a queue_state envelope to every currently-open
// INTERACTIVE conn. Snapshotting the single named conversation is the
// confidentiality boundary (AC-3): another conversation's text can never enter
// this payload — convID is threaded as one variable from OnChange → snapshot →
// conversation_id, so payload and items cannot desync. A per-conn Push error is
// logged at DEBUG and the loop continues — a dropped conn must not abort the
// others (it re-syncs on reconnect); ctx-cancel mid-fan-out returns early.
func (e *queueStateEmitterV2) broadcast(ctx context.Context, bcast interactiveBroadcaster, convID string) {
	payload := toQueueStatePayload(convID, e.snapshot(convID))
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		// QueueStatePayload is a closed struct of strings/ints/time and cannot
		// fail to marshal in practice. Defensive — never echo the payload or
		// err.Error() (the untrusted text could be quoted into the error).
		e.logger.Debug("relay: queue-state drop; payload marshal",
			"event", "queue_state.marshal_err",
			"conversation_id", convID)
		return
	}

	// Fresh snapshot per change: a phone that opened its session since the last
	// event is included here; one that dropped is absent, or surfaces as a Push
	// error below.
	ts := time.Now().UTC()
	for _, c := range bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate — non-interactive conns never see the structured stream
		}
		e.nextID++
		env := protocol.Envelope{
			ID:      e.nextID,
			Type:    protocol.TypeQueueState,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: queue-state push dropped",
				"event", "queue_state.push_err",
				"conversation_id", convID,
				"conn_id", c.ConnID,
				"env_id", e.nextID,
				"err", err)
		}
	}
}

// toQueueStatePayload maps a conversation's msgqueue backlog onto the protocol
// wire payload — the pure, unit-testable seam (mirrors toWirePayload). Each
// QueuedMessage{ID,Text,TS} becomes a QueuedItem{QueuedMsgID,Text,TS},
// preserving FIFO order. Queued is initialised to a non-nil zero-length slice so
// an empty/unknown backlog (Snapshot → nil) marshals to [], not null (AC-1;
// protocol note messaging.go:173-176 — the leaf type cannot force non-nil).
func toQueueStatePayload(convID string, items []msgqueue.QueuedMessage) protocol.QueueStatePayload {
	queued := make([]protocol.QueuedItem, 0, len(items))
	for _, m := range items {
		queued = append(queued, protocol.QueuedItem{
			QueuedMsgID: m.ID,
			Text:        m.Text,
			TS:          m.TS,
		})
	}
	return protocol.QueueStatePayload{
		ConversationID: convID,
		Queued:         queued,
	}
}

// startQueueStateStreamV2 starts the pre-built emitter's Run goroutine over
// bcast and returns a cleanup that waits for Run to exit on ctx-cancel. Mirrors
// startSessionTransitionStreamV2, except qse is pre-built (the emitter must
// exist at queue.New time for the shared OnChange channel; see newQueueState-
// EmitterV2) rather than constructed here.
//
// Cleanup does NOT close `in`: a late OnChange send racing teardown drops
// harmlessly into the open-but-unread buffer (a non-blocking send to a full
// channel just drops). Same rationale as startSessionTransitionStreamV2: rely on
// ctx cancellation to drain Run.
func startQueueStateStreamV2(ctx context.Context, qse *queueStateEmitterV2, bcast interactiveBroadcaster) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		qse.Run(ctx, bcast)
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
