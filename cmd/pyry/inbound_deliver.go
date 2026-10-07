package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// inboundActivateTimeout caps the drain's per-attempt wait for an idle-evicted
// session to respawn its child. It kept the budget of the CLI attach verb (#396,
// removed in #1348): a wedged respawn surfaces as a delivery error so the drain
// retries the FIFO head rather than blocking forever inside Activate. Unlike the
// removed #594 deliver timeout, it does NOT bound the drain's turn-end pacing,
// which may run for a whole claude turn. That pacing block sits in FRONT of
// WriteUserTurn — the write returns as soon as the envelope is in the child's
// stdin pipe — and it is bounded separately, by streamTurnHoldTimeout (#1199).
// The terminal path #1348 deleted kept it INSIDE WriteUserTurn instead. A tuning
// knob, not a contract.
const inboundActivateTimeout = 30 * time.Second

// streamTurnHoldTimeout bounds ONE delivery attempt's wait for the conversation's
// running turn to end on the stream-json path (#1199). Unused when the turn-busy
// tracker is nil, which the composition root never passes since #1348; only a
// test construction of newInboundDeliver reaches that arm.
//
// The arithmetic, against msgqueue's drain: a turn that never ends fails attempt 1
// after this window, which starts the give-up streak (elapsed ≈ 0 <
// defaultGiveUpAfter, 2m), sleeps defaultRetryInterval (1s) and retries; attempt 2
// fails with elapsed ≈ this window ≥ the bound, so the drain gives up, fires
// OnGiveUp, and the head surfaces as a typed session_error / CodeSessionBlocked
// (#1000/#1008) instead of holding the conversation forever. Worst case ≈ 2× this
// value, so a client-visible bound of about half an hour.
//
// The trade-off the value encodes: give-up ABANDONS the head, so any finite bound
// trades "a wedged turn is reported late" against "a message queued behind a
// genuinely long agentic turn is thrown away". 15 minutes sits above any
// interactive turn observed to date while keeping the client-visible bound inside
// the half hour.
//
// There IS now a Pending analogue, and it is GATED (#1911): approvalHoldPending
// exempts an attempt from the give-up bound only when this hold timed out while an
// approval was parked on a person for that conversation. What it exempts is the
// PERSON'S deciding time, not the turn. The unconditional exemption this doc used
// to refuse resets the give-up streak forever, which a human decision may
// legitimately need and a running turn must not — that is what would make the bound
// unsatisfiable; per-conversation gating is what keeps it satisfiable, and the
// arithmetic above stands unchanged for a turn making no progress with nobody being
// asked. For THAT case the better discriminator is still staleness (no turn event
// for N minutes) rather than duration, but that needs a per-conversation timestamp
// the tracker deliberately does not hold (#1201); it is a separate ticket if
// production ever surfaces a session_error for a turn that was legitimately
// progressing. A tuning knob, not a contract.
const streamTurnHoldTimeout = 15 * time.Minute

// mcpApprovalTimeout is the DEFAULT human-approval window handed to the
// pending-approval registry (permbridge.Register) for every VerbMCPApprove
// request. Since #1932 wired the daemon's liveness report into that registry —
// which startRelayV2 does whenever a relay is configured, and startRelay skips
// along with the whole leg when one is not — the window is a re-check interval
// rather than a hard deadline: the registry's own
// timer asks the report at every expiry and re-arms this SAME window while the
// approval is still answerable, denying the request and deleting the entry only
// once the answer comes back no. The bound is therefore one window from the daemon
// OBSERVING the last answerer go away, which is not the moment it went — a vanished
// phone stays in the daemon's active set until internal/relay's idle sweep notices
// it, and docs/knowledge/features/permbridge-package.md carries what that costs.
// It is a default, not the value — envApprovalTimeout overrides it, and
// approvalTimeout is the accessor every consumer actually calls.
//
// Why ten and not two: waiting is not the unsafe state. The tool does not run
// while the approval is outstanding, so denying early prevents nothing that
// remaining pending was not already preventing. The number is about how long a
// person may reasonably take to reach a phone, not about safety.
//
// Why ten and not more: this constant no longer answers that. It used to be held
// deliberately clear of streamTurnHoldTimeout, which bounds the delivery hold,
// because a give-up there ABANDONED whatever message was queued behind the waiting
// turn — so raising this window past that hold traded a prompt that gives up too
// early for a message that silently disappears. #1911 removed the abandonment:
// approvalHoldPending exempts a hold from the give-up bound for exactly as long as
// an approval is parked on a person, so a queued message now waits out the decision
// instead of vanishing. Ten stays because it is what a person reaching a phone
// plausibly needs, not because the hold pins it. A tuning knob, not a contract.
const mcpApprovalTimeout = 10 * time.Minute

// envApprovalTimeout overrides the human-approval window (mcpApprovalTimeout). A
// plausibly-operational knob for tuning the window — the e2e (#1139, rebuilt under
// #1932) shrinks it to ~2s so both halves of the conditional bound are cheap to
// exercise. One arm keeps the answering phone connected and answers past more than
// one window, and nothing may deny in the meantime; the other ENDS that phone's
// session first, so nobody is left able to answer, and only then does the window
// deny. What the suite pins is a deny once nobody can answer it, and explicitly no
// deny while somebody still can.
const envApprovalTimeout = "PYRY_APPROVAL_TIMEOUT"

// approvalTimeout is the approval window handed to the pending-approval registry:
// mcpApprovalTimeout by default, overridable via PYRY_APPROVAL_TIMEOUT. An unset or
// unparseable value falls back to the default, so production behaviour is
// byte-identical when the env is absent. The DURATION stays the only knob, and it
// is the duration every arming uses, not just the first: permbridge.Register
// re-arms the value it was handed and never a different one. What that value feeds
// is the re-check interval the fail-closed core now applies while somebody can
// still answer — not a deadline it applies unconditionally.
func approvalTimeout() time.Duration {
	if v := os.Getenv(envApprovalTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return mcpApprovalTimeout
}

// approvalParkedReport is the late-bound holder for #1919's ApprovalParked report,
// carrying it from the relay wiring back to the delivery seam (#1911). The
// composition root builds msgqueue.New — and therefore the seam — well before
// startRelayV2 constructs the bridge that answers the report, and the relayWiring
// literal is built after the queue, so the report cannot be handed to the queue at
// construction. This is the same shape, for the same reason, as
// streamApprovalBridge.toolCallInFlight: one holder, set at one wiring site.
//
// ask is written exactly once, at wiring time, and read from each conversation's
// drain goroutine. It carries no mutex because goroutine creation supplies the
// happens-before edge on every link of the chain: set runs inside startRelayV2
// before the v2 manager's Run goroutine starts, that goroutine spawns the
// per-connection goroutines, the send_message handler on one of them calls
// msgqueue.Enqueue, and Enqueue spawns the drain that reads it. The queue's own Run
// goroutine cannot beat that: it holds no conversation until an Enqueue lands.
type approvalParkedReport struct {
	// ask is streamApprovalBridge.ApprovalParked; nil until the relay wiring sets it.
	ask func(conversationID string) bool
}

// set publishes the report. A nil receiver is a no-op, matching the nil-safety
// idiom of the seam this feeds (waitIdleForDelivery, openForDelivery).
func (r *approvalParkedReport) set(ask func(conversationID string) bool) {
	if r == nil {
		return
	}
	r.ask = ask
}

// parked reports whether a person is currently being asked about conversationID.
// A nil receiver or an unset ask answers false for every conversation — which is
// the behaviour before this exemption existed, and the right answer before the
// relay leg sets ask, or in a wiring with no turn-busy tracker, where
// ApprovalParked would be negative anyway.
func (r *approvalParkedReport) parked(conversationID string) bool {
	if r == nil || r.ask == nil {
		return false
	}
	return r.ask(conversationID)
}

// errStreamTurnHold marks a delivery attempt that ended in the stream-path
// mid-turn hold, having written nothing. newInboundDeliver's hold branch is its
// only producer and markApprovalHolds its only consumer. Its text keeps the
// rendered hold error byte-identical to the pre-#1911 wrap.
var errStreamTurnHold = errors.New("stream turn hold")

// errHeldForApproval marks a hold that happened while a person was being asked
// about the conversation (#1911). markApprovalHolds is its only producer and
// approvalHoldPending its only consumer. Like errStreamTurnHold it is a fixed
// daemon-authored string with no interpolation, so a wrapped hold error carries
// neither queued text nor approval content by construction.
var errHeldForApproval = errors.New("delivery held for a parked approval")

// markApprovalHolds decorates the delivery seam so msgqueue's Pending classifier
// can tell a person's deciding time apart from a wedged conversation. It exists
// because msgqueue.PendingFunc is func(error) bool: it classifies the delivery
// error alone and never sees a conversation, so the conversation-scoped question
// has to be answered here, before the error leaves the delivery seam.
//
// The two-step test order is load-bearing, for the reason ApprovalParked's own doc
// gives about its conjunction: the sentinel test is a local comparison while parked
// crosses two leaf locks, so the common case — an ordinary delivery failure with
// nobody being asked — must not pay the second.
//
// PRECISION IS THE POINT. Only a hold error is ever re-marked. A resolve or
// Activate failure is a genuinely wedged conversation and stays on the give-up
// clock even while an approval is parked, which is what keeps that clock
// satisfiable once an approval may outlive the hold. There is deliberately no
// context.Canceled guard here: both cancellation sources — a removed head and
// daemon shutdown — are decided by branches upstream of Pending in msgqueue's
// drain, so a guard would be dead code defending a failure mode nobody has seen.
func (r *approvalParkedReport) markApprovalHolds(deliver msgqueue.DeliverFunc) msgqueue.DeliverFunc {
	return func(ctx context.Context, convID string, payload []byte) error {
		err := deliver(ctx, convID, payload)
		if err == nil || !errors.Is(err, errStreamTurnHold) {
			return err
		}
		if !r.parked(convID) {
			return err
		}
		return fmt.Errorf("%w: %w", errHeldForApproval, err)
	}
}

// approvalHoldPending is the msgqueue.PendingFunc the composition root wires: it
// classifies a delivery held behind an approval parked on a person as a legitimate
// hold rather than a failure, so the give-up streak resets instead of counting.
//
// A free function rather than a method on the report: PendingFunc must be pure and
// non-blocking (msgqueue calls it on the drain path), and everything that could
// block has already run on the delivery path, which blocks for whole turns anyway.
func approvalHoldPending(err error) bool { return errors.Is(err, errHeldForApproval) }

// newInboundDeliver builds the msgqueue.DeliverFunc seam over the stamp-free
// resolve core. The engine (#704) calls it on a per-conversation drain
// goroutine, one delivery at a time. It:
//
//   - re-resolves the bound session per attempt (the binding may change between
//     enqueue and drain), returning any resolve error so the drain retries the
//     head (a conversation that becomes transiently unbound post-ack is held,
//     not dropped);
//   - Activates under a bounded budget so a wedged respawn becomes a retryable
//     error instead of a permanent block;
//   - HOLDS the delivery while the conversation's turn is running on the
//     stream-json path (#1199), then marks the conversation mid-turn, both
//     between Activate and the write — see the placement note below. A hold that
//     ends in an error is the sole producer of errStreamTurnHold, which is what
//     lets markApprovalHolds exempt a person's deciding time from the give-up
//     bound (#1911) without exempting a wedged conversation with it;
//   - writes the turn with the RAW lifecycle ctx — no deliver timeout, because
//     that blocking IS the drain's turn-end pacing (DeliverFunc must return nil
//     only on a confirmed commit, `defaultRetryInterval`).
//
// PLACEMENT of the hold, all three constraints load-bearing. It sits BEFORE
// WriteUserTurn, therefore before the turncommit claim streamsup.WriteTurn makes
// inside it: that is what keeps the queued head draining && !committing for the
// whole wait, which is the only window in which msgqueue.Remove drops it — so the
// drop-before-drain control is delivered by placement, not by new code (commitGate
// itself documents the seam as calling it "after the idle-gate wait and before the
// write"). It sits AFTER Activate, which is already bounded and idempotent, so a
// wedged respawn still surfaces as a prompt retryable error rather than being
// masked behind a long hold. And the MARK precedes the write rather than following
// it, because the tracker's ordinary opener feed is asynchronous and a fast child's
// TurnEnd could otherwise clear before the mark landed; openForDelivery's doc
// carries that argument, and returns the undo this body runs on a write error.
//
// A nil tracker makes both calls no-ops, leaving this body semantically identical
// to the pre-#1199 sequence. The composition root always passes one since #1348,
// so only test constructions reach that arm.
//
// It is built over router.resolve, NOT router.Route, so it never stamps the
// active-conversation cursor: the cursor stays single-writer (the routing-path
// goroutine via Route), preserving the #679/#687 follow-active invariant against
// a drain-time re-stamp. Taking resolve as a func value (not the struct) keeps
// the seam unit-testable with a fake resolve; busy and hold are taken the same way
// so the hold is exercisable with a short bound.
func newInboundDeliver(resolve func(string) (handlers.TurnWriter, error), busy *turnBusyTracker, hold time.Duration, placement ...*sendNowPlacement) msgqueue.DeliverFunc {
	var place *sendNowPlacement
	if len(placement) != 0 {
		place = placement[0]
	}
	return func(ctx context.Context, convID string, payload []byte) error {
		w, err := resolve(convID)
		if err != nil {
			return err
		}
		activateCtx, cancelActivate := context.WithTimeout(ctx, inboundActivateTimeout)
		err = w.Activate(activateCtx)
		cancelActivate()
		if err != nil {
			return err
		}
		// Wrapped, so a hold failure is legible in msgqueue's retry Warn; the wrap
		// keeps errors.Is(err, context.DeadlineExceeded) and context.Canceled true for
		// the drain's own classification. Nothing has been written at this point.
		// errStreamTurnHold rides the same wrap and this statement is its only
		// producer, so markApprovalHolds can tell a hold apart from every other
		// delivery failure without widening this seam; the rendered text is unchanged.
		if err := busy.waitIdleForDelivery(ctx, convID, hold); err != nil {
			return fmt.Errorf("%w: %w", errStreamTurnHold, err)
		}
		undo, finished, err := busy.beginDelivery(ctx, convID)
		if err != nil {
			return err
		}
		defer finished()
		write := func() error { return w.WriteUserTurn(ctx, convID, payload) }
		var writeErr error
		if place != nil && place.isClaude(convID) {
			writeErr = place.write(ctx, convID, payload, write)
		} else {
			writeErr = write()
		}
		if writeErr != nil {
			// Returned VERBATIM (unwrapped): msgqueue classifies ErrNoLiveSession,
			// ErrTrustModalPending and turncommit.ErrDropped by errors.Is, and the undo
			// leaves the tracker exactly as it was before this attempt.
			undo()
			return writeErr
		}
		return nil
	}
}
