package main

import (
	"context"
	"errors"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// errSendNowNotClaude refuses a send-now write to a session that is not Claude's:
// Codex's write path starts a new turn rather than feeding the running one.
var errSendNowNotClaude = errors.New("send-now: session does not take input mid-turn")

// errSendNowIdle refuses a send-now write when the conversation's turn is idle:
// the ordinary drain delivers the message, in FIFO order.
var errSendNowIdle = errors.New("send-now: turn is idle")

// newSendNowDeliver builds the msgqueue.SendNowFunc seam (#2729): it writes a
// queued message's payload into the conversation's running claude turn.
//
// It differs from newInboundDeliver in exactly the places the two jobs differ.
// It never waits for idle — an idle turn is a refusal, since the drain owns that
// case. It never Activates: a running turn has a live child, and a send-now that
// found none must fail and leave the message queued, not spawn one. It refuses a
// non-Claude session before resolving anything. And it is NOT wrapped in
// channelCarry.carryPending or markApprovalHolds: pending channel posts ride the
// waiting head's own delivery, and a send-now write is never a hold.
//
// The tracker's openForSendNow is the busy check, atomic with the carry that
// keeps the conversation busy through a turn end the write may outlive; its undo
// runs when the write fails. The write's own error is returned verbatim, and
// msgqueue puts the message back where it was.
//
// Nothing here logs. msgqueue.SendNow's caller logs content-free fields only.
func newSendNowDeliver(resolve func(string) (handlers.TurnWriter, error), isClaude func(string) bool, busy *turnBusyTracker) msgqueue.SendNowFunc {
	return func(ctx context.Context, convID string, payload []byte) error {
		if !isClaude(convID) {
			return errSendNowNotClaude
		}
		// The busy check comes before resolve, so an idle conversation never
		// reaches resolve's revive of a dropped session.
		ok, undo := busy.openForSendNow(convID)
		if !ok {
			return errSendNowIdle
		}
		w, err := resolve(convID)
		if err != nil {
			undo()
			return err
		}
		if err := w.WriteUserTurn(ctx, convID, payload); err != nil {
			undo()
			return err
		}
		return nil
	}
}

// isClaude reports whether conversationID is bound to a Claude session. An
// unknown or unbound conversation, or a session the pool does not know, is
// false. It reads the same binding resolve does, without resolving a writer.
func (r sessionRouter) isClaude(conversationID string) bool {
	conv, ok := r.convReg.Get(conversations.ConversationID(conversationID))
	if !ok || conv.CurrentSessionID == "" {
		return false
	}
	harness, err := r.pool.HarnessFor(sessions.SessionID(conv.CurrentSessionID))
	return err == nil && harness == sessions.HarnessClaude
}

// sendNowGrace bounds how long a turn end that follows a send-now write keeps the
// conversation busy, waiting for the second turn the write may have opened (the
// #2728 capture's no-tool and after-last-tool arms). When claude folded the write
// into the turn that just ended, no opener comes and the next queued message
// waits this long; when the write opened a second turn whose first event takes
// longer than this to stream, the drain can see a brief idle that is not real. A
// tuning knob, not a contract: claude streams a turn's first event within a few
// seconds of reading its input.
const sendNowGrace = 10 * time.Second

// scheduleCarryRelease releases the tracker's carried close of generation gen
// once after has passed. It lives here rather than in stream_turn_busy.go, which
// reads no clock (TestTurnBusyTracker_ImportsStayMinimal): the tracker owns the
// generation, this feature owns the bound. The timer fires once and holds no
// resource, so it needs no shutdown path; a release for a retired generation is
// a no-op.
func scheduleCarryRelease(t *turnBusyTracker, conversationID string, gen uint64, after time.Duration) {
	time.AfterFunc(after, func() { t.releaseCarried(conversationID, gen) })
}
