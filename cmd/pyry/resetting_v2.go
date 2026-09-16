package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// resettingEmitterV2 fans a resetting v2 envelope to every open INTERACTIVE conn as
// a conversation reset moves through its phases (#2478, the producer for the frame
// #2453 declared). Without it a reset is invisible from a client's side: the screen
// pauses for however long the wrap-up turn and the respawn take, and nothing says
// which of the two is running or whether a handoff note was made.
//
// IT IS attachmentOfferEmitterV2'S SHAPE, NOT sessionErrorEmitterV2'S, and the
// choice is the whole design. The two nearest producers by class — queueStateEmitterV2
// and sessionErrorEmitterV2 — buffer onto a Run goroutine because their callers are
// msgqueue's OnChange and OnGiveUp seams, each under a MUST-NOT-BLOCK contract that
// a drop-on-full send is what satisfies. This producer's caller is
// activeSessionStarter.resetThenRotate, which already runs on its own goroutine
// precisely so a ninety-second wrap-up cannot block relay dispatch. So a queue buys
// nothing here, and it would COST something real: a drop-on-full send can drop the
// falling edge, and docs/protocol-mobile.md § resetting promises a client that every
// rising sequence ends in one, so it needs no timeout of its own to clear the
// indicator. A synchronous call cannot drop an edge and cannot reorder two.
//
// THE BROADCASTER IS LATE-BOUND, which is the one thing this does not inherit from
// that emitter. The fan-out surface exists only inside startRelayV2, and the
// activeSessionStarter literal that reaches this producer is built in main.go before
// it — the chicken-and-egg qse and sessionErr solve by taking bcast as a Run
// parameter. With no Run there is no parameter to take, so the emitter is pre-built
// and the broadcaster is published into it from the relay leg, the shape
// approvalParkedReport already uses.
//
// NO REPLAY RING AND NO DURABLE HISTORY. resetting is ephemeral status like
// queue_state and session_error, not a session boundary: there is no
// appendConversationHistory counterpart, no connect-time reconcile, and EventID
// stays nil. A phone that connects mid-reset learns nothing about it, which is the
// right answer — the falling edge is seconds away, and a rising edge replayed after
// the fact would pin an indicator open with nothing left to close it.
//
// SECURITY: the handoff note's content structurally cannot reach this wire or any
// record here. This type holds no note store, no reply text and no
// conversationReset — the same construction sessionErrorEmitterV2 uses when it holds
// no *msgqueue.Queue so it cannot reach queued text — and the outcome arrives as a
// bare bool. Every field on the payload is daemon-authored: conversation_id is the
// canonical id activeSessionStarter.start already resolved (never the raw client
// string), Active is a computed bool, and Phase and Handoff are constants selected
// from protocol's two closed sets, never interpolated. Every record carries only
// content-free discriminants — event, conversation_id, conn_id, env_id, and Push's
// transport sentinel as err — and the marshal arm never echoes the payload bytes or
// err.Error(). conversation_id is logged bare rather than through boundedConvID:
// that helper exists for the one arm where an arbitrary client string reaches a log
// call, and every arm past conversations.ValidID logs a string already known to be
// 36 bytes.
type resettingEmitterV2 struct {
	// ctx is the daemon context, captured at construction the way
	// attachmentOfferEmitterV2 captures its own: once cancelled, ActiveConns answers
	// empty and a racing Push returns its error, so a late edge fans out to nobody
	// rather than blocking teardown.
	ctx    context.Context
	logger *slog.Logger

	// mu is a LEAF guarding bcast and nextID, and it is never held across ActiveConns
	// or a Push — so it cannot nest inside the manager's own push lock and there is
	// no lock order to reason about. It is load-bearing rather than copied on both
	// counts: two conversations can reset at once, each on its own tail goroutine, so
	// the counter genuinely races (the reason attachmentOfferEmitterV2 carries one
	// where the Run-goroutine emitters do not); and guarding bcast with the mutex that
	// already exists costs one uncontended acquisition per edge — three per reset, on
	// a path an operator drives by hand — while removing a happens-before argument
	// that would otherwise have to be re-derived every time a new caller of
	// StartNewSession appears.
	mu sync.Mutex
	// bcast is nil before the relay leg attaches and after it detaches; both windows
	// are inert, as is a daemon that never starts the leg at all.
	bcast interactiveBroadcaster
	// nextID is the per-emitter envelope-ID counter, the shape every v2 emitter in
	// this package uses — V2SessionManager.Push never rewrites Envelope.ID, so each
	// producer numbers its own frames.
	nextID uint64
}

// newResettingEmitterV2 builds the producer over the daemon context. It is built in
// main.go, before the broadcaster exists; attach publishes that from the relay leg.
// Until then every edge is inert.
func newResettingEmitterV2(ctx context.Context, logger *slog.Logger) *resettingEmitterV2 {
	return &resettingEmitterV2{ctx: ctx, logger: logger}
}

// attach publishes the fan-out surface. Called once, from startRelayV2.
func (e *resettingEmitterV2) attach(bcast interactiveBroadcaster) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bcast = bcast
}

// detach makes the emitter inert again, so a fan-out cannot race a winding-down
// manager — what startRelayV2's cleanup asks of every producer it started.
// Idempotent, and a nil receiver is a no-op.
func (e *resettingEmitterV2) detach() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bcast = nil
}

// wrappingUp is the first rising edge: the wrap-up turn is running and the handoff
// is not resolved yet.
func (e *resettingEmitterV2) wrappingUp(convID string) {
	e.emit(convID, true, protocol.ResetPhaseWrappingUp, protocol.ResetHandoffPending)
}

// restarting is the PHASE CHANGE, not a second reset: the wrap-up turn is over and
// claude is being killed and respawned. It is where the handoff resolves, and the
// only place the bool the reset routine returns becomes a wire token.
//
// wrote is a bool rather than a protocol constant crossing out of session_reset.go
// deliberately: that file holds the reset's daemon-side coordination and imports no
// wire package, and everything that is not a completed WriteHandoffNote maps to
// skipped here — which is the closed set ResetHandoffSkipped's own doc describes,
// a reported outcome rather than a missing value.
func (e *resettingEmitterV2) restarting(convID string, wrote bool) {
	handoff := protocol.ResetHandoffSkipped
	if wrote {
		handoff = protocol.ResetHandoffWritten
	}
	e.emit(convID, true, protocol.ResetPhaseRestarting, handoff)
}

// done is the falling edge that closes every rising sequence, on success and on
// every error path. Phase and Handoff go out as the empty string rather than being
// omitted — the frame's standing no-omitempty rule, where the key is always on the
// wire and its zero value is the statement.
func (e *resettingEmitterV2) done(convID string) {
	e.emit(convID, false, "", "")
}

// emit marshals one payload and fans it to every currently-open INTERACTIVE conn.
//
// A nil receiver emits nothing, and that is a reachable state rather than a
// defensive check: activeSessionStarter is a constructor-less bag of injected seams
// built as a named-field literal, so an omitted resetting field is the PTY posture
// and the shape every pre-#2478 test literal in this package still gets for free.
//
// A per-conn Push error is logged at DEBUG and the loop continues — a dropped conn
// must not cost the others their frame, and there is no re-sync path to mention
// because the frame is live-only. ctx-cancel mid-fan-out returns early.
func (e *resettingEmitterV2) emit(convID string, active bool, phase, handoff string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	bcast := e.bcast
	e.mu.Unlock()
	if bcast == nil {
		return
	}

	payloadJSON, err := json.Marshal(protocol.ResettingPayload{
		ConversationID: convID,
		Active:         active,
		Phase:          phase,
		Handoff:        handoff,
	})
	if err != nil {
		// Defensive: ResettingPayload is a closed struct of three strings and a bool,
		// every one of them daemon-authored, and cannot fail to marshal in practice.
		// Never echo the payload or err.Error() — encoding/json quotes invalid input
		// bytes into its error.
		e.logger.Debug("relay: resetting drop; payload marshal",
			"event", "resetting.marshal_err",
			"conversation_id", convID)
		return
	}

	ctx := e.ctx
	ts := time.Now().UTC()
	for _, c := range bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the #607 capability gate — non-interactive conns never see the structured stream
		}
		e.mu.Lock()
		e.nextID++
		id := e.nextID
		e.mu.Unlock()
		env := protocol.Envelope{
			ID:      id,
			Type:    protocol.TypeResetting,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: resetting push dropped",
				"event", "resetting.push_err",
				"conversation_id", convID,
				"conn_id", c.ConnID,
				"env_id", id,
				"err", err)
		}
	}
}

// startResettingStreamV2 hands the pre-built emitter the relay leg's fan-out surface
// and returns an idempotent detach. It takes no ctx and starts no goroutine — the
// sibling start…StreamV2 functions need both for their Run loops and this producer
// has none, emitting synchronously on the reset tail goroutine instead.
func startResettingStreamV2(re *resettingEmitterV2, bcast interactiveBroadcaster) func() {
	re.attach(bcast)

	var cleanedUp bool
	return func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		re.detach()
	}
}
