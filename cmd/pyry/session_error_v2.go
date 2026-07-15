package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// sessionErrorQueueSize bounds the buffered hand-off between msgqueue's off-lock
// give-up seam (OnGiveUp, #1000) and the emitter's Run goroutine. Give-ups are
// far rarer than backlog changes — each needs >=2 min of persistent delivery
// failure per head (#1000's GiveUpAfter bound) — so 16 is effectively
// unreachable short of a simultaneous mass-wedge burst (N conversations all
// crossing the bound at once). Drop-on-full bounds memory while never stalling
// the drain (GiveUpFunc's MUST-NOT-BLOCK contract). Unlike queue_state a dropped
// give-up is NOT recovered by re-reading state (the reason is a one-shot edge),
// but the depth makes overflow unreachable and blocking would stall the drain's
// exit->respawn.
const sessionErrorQueueSize = 16

// giveUpNotice is the hand-off value the OnGiveUp seam sends to the emitter's Run
// goroutine. Unlike queue_state — whose channel carries a bare convID because the
// consumer re-reads current state via Snapshot — the give-up reason is a one-shot
// edge value that cannot be recovered by re-reading queue state (AC-4), so it
// rides the channel alongside the id. Same struct-channel shape as
// sessionTransitionEmitterV2.in.
type giveUpNotice struct{ convID, reason string }

// sessionErrorEmitterV2 fans a session_error v2 envelope to every open
// INTERACTIVE conn when the message queue gives up delivering a conversation's
// head (#1008, the msgqueue OnGiveUp seam #1000). It mirrors queueStateEmitterV2
// — a buffered `in` channel decouples msgqueue's off-lock give-up seam (a
// non-blocking send, per GiveUpFunc's MUST-NOT-BLOCK contract) from the single
// Run goroutine that performs the blocking ActiveConns snapshot and per-conn
// Push — MINUS the queue reference: this producer holds NO Snapshot and NO
// *msgqueue.Queue, so it CANNOT reach queued text. That structural absence is the
// AC-3 confidentiality guarantee (queued phone text is untrusted), enforced by
// construction rather than by discipline.
//
// The interactive-only capability filter is the delivery gate — a phone that
// never negotiated interactive receives only the coarse v1 fan-out, never this
// event.
//
// SECURITY: the only inputs are the daemon-resolved convID (a non-secret routing
// id) and the daemon-generated reason (#1000 builds it from the elapsed window
// and the delivery error; it NEVER carries head.text). The reason rides the wire
// as Message but is NEVER logged at any level; every log line carries only
// content-free discriminants — `event`, `conversation_id`, `conn_id`, `env_id`,
// and Push's transport-sentinel `err`. The marshaled payload bytes and
// err.Error() on the marshal path are never logged. Same discipline as the
// sibling emitters and msgqueue itself.
type sessionErrorEmitterV2 struct {
	// in is the shared hand-off channel; the OnGiveUp seam (sessionErrorNotify)
	// sends give-up notices in, Run receives them. The channel is the only state
	// shared across goroutines — channels are concurrency-safe. There is
	// deliberately NO snapshot/queue field (contrast queueStateEmitterV2): the
	// type system is the AC-3 no-queued-text guarantee.
	in     <-chan giveUpNotice
	logger *slog.Logger

	// nextID is the per-conn envelope-ID counter (mirrors the sibling emitters).
	// Read/written only on the single Run goroutine (broadcast is serial) — no
	// atomic needed. EventID is left nil: a give-up is a one-shot edge, so this
	// producer does NOT join the #647/#649 reconnect-replay ring and has NO
	// connect-time reconcile source — a phone that connects after a give-up fired
	// gets no replay (correct: #1000 respawns the session, so replaying a stale
	// "session blocked" would be wrong). TypeSessionError != TypeAssistantDelta,
	// so it is a never-drop control event in the push queue automatically.
	nextID uint64
}

// newSessionErrorEmitterV2 constructs an emitter over the shared hand-off channel
// `in`. The channel is created BEFORE msgqueue.New (so OnGiveUp can send to it)
// and shared with the seam builder — that breaks the chicken-and-egg without any
// late-bound field, mirroring newQueueStateEmitterV2. There is deliberately NO
// snapshot/queue parameter (the type system is the AC-3 guarantee). Run must be
// called once on a goroutine before notifications are delivered.
func newSessionErrorEmitterV2(in <-chan giveUpNotice, logger *slog.Logger) *sessionErrorEmitterV2 {
	return &sessionErrorEmitterV2{
		in:     in,
		logger: logger,
	}
}

// sessionErrorNotify builds the msgqueue.GiveUpFunc seam: a closure that does a
// non-blocking buffered send of the give-up notice with drop-on-full + a
// content-free Warn (event + conversation_id ONLY — never reason). It NEVER
// blocks, satisfying GiveUpFunc's MUST-NOT-BLOCK contract on the drain path, and
// is safe for concurrent invocation (a channel send is concurrency-safe). It
// captures the channel, not the emitter, so the emitter is never read off the
// constructing goroutine. Unlike queue_state a dropped give-up is NOT recovered
// (the reason is a one-shot edge, not re-derivable from queue state) — but the
// buffer makes overflow unreachable short of a simultaneous mass-wedge, and
// blocking would stall the drain's exit->respawn.
func sessionErrorNotify(ch chan<- giveUpNotice, logger *slog.Logger) msgqueue.GiveUpFunc {
	return func(convID, reason string) {
		select {
		case ch <- giveUpNotice{convID: convID, reason: reason}:
		default:
			logger.Warn("relay: session-error give-up queue full; dropping notification",
				"event", "session_error.queue_full",
				"conversation_id", convID)
		}
	}
}

// Run drains the hand-off channel until ctx is cancelled or in is closed,
// broadcasting one session_error per give-up notice (AC-5: emits once per
// give-up, no busy-loop). bcast is a goroutine-local parameter (supplied at
// start, when mgr exists) — no stored broadcaster field, no race. Mirrors
// queueStateEmitterV2.Run.
func (e *sessionErrorEmitterV2) Run(ctx context.Context, bcast interactiveBroadcaster) {
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-e.in:
			if !ok {
				return
			}
			e.broadcast(ctx, bcast, n)
		}
	}
}

// broadcast builds the session_error payload from the give-up notice, marshals it
// once, then fans a session_error envelope to every currently-open INTERACTIVE
// conn. Its only inputs are the notice's daemon-resolved convID and
// daemon-generated reason; the terminal Code is the fixed CodeSessionBlocked
// constant this producer stamps (the seam carries no code field) — distinct from
// the transient CodeServerBinaryBusy so a client cannot read the frame as "retry
// shortly" (AC-2). The producer holds no queue handle, so no queued text can
// enter the payload (AC-3). A per-conn Push error is logged at DEBUG and the loop
// continues — a dropped conn must not abort the others (it misses this one-shot
// edge; no replay); ctx-cancel mid-fan-out returns early.
func (e *sessionErrorEmitterV2) broadcast(ctx context.Context, bcast interactiveBroadcaster, n giveUpNotice) {
	payload := protocol.SessionErrorPayload{
		ConversationID: n.convID,
		Code:           protocol.CodeSessionBlocked,
		Message:        n.reason,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		// SessionErrorPayload is a closed struct of three strings and cannot fail
		// to marshal in practice. Defensive — never echo the payload or err.Error()
		// (encoding/json can quote input bytes into its error).
		e.logger.Debug("relay: session-error drop; payload marshal",
			"event", "session_error.marshal_err",
			"conversation_id", n.convID)
		return
	}

	ts := time.Now().UTC()
	for _, c := range bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate — non-interactive conns never see the structured stream
		}
		e.nextID++
		env := protocol.Envelope{
			ID:      e.nextID,
			Type:    protocol.TypeSessionError,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: session-error push dropped",
				"event", "session_error.push_err",
				"conversation_id", n.convID,
				"conn_id", c.ConnID,
				"env_id", e.nextID,
				"err", err)
		}
	}
}

// startSessionErrorStreamV2 starts the pre-built emitter's Run goroutine over
// bcast and returns an idempotent cleanup that waits for Run to exit on
// ctx-cancel. Mirrors startQueueStateStreamV2: the emitter is pre-built (it must
// exist at msgqueue.New time for the shared OnGiveUp channel; see
// newSessionErrorEmitterV2) rather than constructed here.
//
// Cleanup does NOT close `in`: a late OnGiveUp send racing teardown drops
// harmlessly into the open-but-unread buffer (a non-blocking send to a full
// channel just drops). Same rationale as startQueueStateStreamV2: rely on ctx
// cancellation to drain Run.
func startSessionErrorStreamV2(ctx context.Context, see *sessionErrorEmitterV2, bcast interactiveBroadcaster) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		see.Run(ctx, bcast)
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
