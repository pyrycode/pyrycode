package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func sendNowTracker(grace time.Duration) *turnBusyTracker {
	return newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger(), withSendNowGrace(grace))
}

var (
	snOpener = turnevent.TextChunk{MessageID: "m1", Text: "hello"}
	snEnd    = turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}
)

// The seam writes only into a running Claude turn, and every refusal writes
// nothing.
func TestSendNowDeliver_WritesOnlyIntoARunningClaudeTurn(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("no live child")
	cases := []struct {
		name     string
		claude   bool
		busy     bool
		resolve  error
		write    error
		want     error
		wantSent bool
	}{
		{"running claude turn writes", true, true, nil, nil, nil, true},
		{"idle turn refuses", true, false, nil, nil, errSendNowIdle, false},
		{"codex session refuses", false, true, nil, nil, errSendNowNotClaude, false},
		{"unresolvable conversation refuses", true, true, errNoBoundSession, nil, errNoBoundSession, false},
		{"failed write is returned", true, true, nil, writeErr, writeErr, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := sendNowTracker(time.Hour)
			if tc.busy {
				tr.observe("sess-a", snOpener)
			}
			var sent []string
			w := funcWriter{write: func(_ context.Context, _ string, payload []byte) error {
				sent = append(sent, string(payload))
				return tc.write
			}}
			resolve := func(string) (handlers.TurnWriter, error) {
				if tc.resolve != nil {
					return nil, tc.resolve
				}
				return w, nil
			}
			deliver := newSendNowDeliver(resolve, func(string) bool { return tc.claude }, tr)

			err := deliver(context.Background(), testConvID, []byte("now please"))
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got := len(sent) == 1; got != tc.wantSent {
				t.Fatalf("wrote %q, want a write: %v", sent, tc.wantSent)
			}
			if tc.write != nil {
				// The undo took the carry back: a turn end now closes at once.
				tr.observe("sess-a", snEnd)
				if tr.Busy(testConvID) {
					t.Error("failed write left a carry that held the turn end open")
				}
			}
		})
	}
}

// AC4: a send-now write that lands as the turn ends keeps the conversation busy
// through that turn end, until the second turn the write opened ends.
func TestTurnBusy_SendNowCarry_SecondTurnHoldsUntilItsOwnEnd(t *testing.T) {
	t.Parallel()
	tr := sendNowTracker(time.Hour)
	tr.observe("sess-a", snOpener)
	ok, _ := tr.openForSendNow(testConvID)
	if !ok {
		t.Fatal("openForSendNow on a running turn = false")
	}

	tr.observe("sess-a", snEnd) // turn 1 ends after the write
	if !tr.Busy(testConvID) {
		t.Fatal("turn end after a send-now write released the conversation")
	}
	tr.observe("sess-a", snOpener) // the write's own turn opens
	tr.observe("sess-a", snEnd)    // and ends
	if tr.Busy(testConvID) {
		t.Fatal("the write's own turn end did not close the conversation")
	}
}

// When claude folded the write into the running turn, no second turn comes and
// the grace closes the mark.
func TestTurnBusy_SendNowCarry_FoldedWriteClosesAfterGrace(t *testing.T) {
	t.Parallel()
	tr := sendNowTracker(20 * time.Millisecond)
	tr.observe("sess-a", snOpener)
	if ok, _ := tr.openForSendNow(testConvID); !ok {
		t.Fatal("openForSendNow on a running turn = false")
	}
	tr.observe("sess-a", snEnd)
	if !tr.Busy(testConvID) {
		t.Fatal("turn end released the conversation before the grace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.WaitIdle(ctx, testConvID); err != nil {
		t.Fatalf("grace never closed the folded turn: %v", err)
	}
}

// An idle conversation refuses the carry; a teardown close drops it.
func TestTurnBusy_SendNowCarry_IdleRefusesAndTeardownClears(t *testing.T) {
	t.Parallel()
	tr := sendNowTracker(time.Hour)
	if ok, _ := tr.openForSendNow(testConvID); ok {
		t.Fatal("openForSendNow on an idle conversation = true")
	}
	var nilTracker *turnBusyTracker
	if ok, _ := nilTracker.openForSendNow(testConvID); ok {
		t.Fatal("openForSendNow on a nil tracker = true")
	}

	tr.observe("sess-a", snOpener)
	tr.openForSendNow(testConvID)
	tr.clearForSession("sess-a")
	if tr.Busy(testConvID) {
		t.Fatal("teardown did not close the conversation")
	}
	// A fresh turn after the teardown closes on its own end: no stale carry.
	tr.observe("sess-a", snOpener)
	tr.observe("sess-a", snEnd)
	if tr.Busy(testConvID) {
		t.Fatal("a carry survived the teardown and held a later turn open")
	}
}

// AC3: a send-now delivery clears none of the pending channel posts the waiting
// head's composition recorded; the head's own confirmation still does.
func TestChannelCarry_SendNowDeliveryClearsNothing(t *testing.T) {
	t.Parallel()
	carry, reg, _ := newTestCarry(t)
	carry.record(carryTestConvID, "What are you avoiding?")

	deliver := carry.carryPending(func(context.Context, string, []byte) error { return nil })
	if err := deliver(context.Background(), carryTestConvID, []byte("head")); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	carry.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{Text: "sent now", SentNow: true})
	if got := reg.PendingChannelPosts(carryTestConvID); len(got) != 1 {
		t.Fatalf("pending after a send-now delivery = %q, want the post kept", got)
	}

	carry.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{Text: "head"})
	if got := reg.PendingChannelPosts(carryTestConvID); got != nil {
		t.Fatalf("pending after the head's own delivery = %q, want nothing", got)
	}
}
