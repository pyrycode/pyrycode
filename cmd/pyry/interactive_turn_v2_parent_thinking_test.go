package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestInteractiveTurnEmitterV2_ParentThinkingPreservesLifecycle(t *testing.T) {
	t.Parallel()
	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{ParentToolCallID: "agent", Text: "private"},
		turnevent.ThinkingProgress{ParentToolCallID: "agent", EstimatedTokens: 123, EstimatedTokensDelta: 64},
	} {
		for _, open := range []bool{false, true} {
			t.Run(fmt.Sprintf("%T/open=%v", ev, open), func(t *testing.T) {
				cur := &stubCursor{}
				cur.set(testConvID)
				bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
				e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
				t.Cleanup(func() { e.flushTimer.Stop() })
				if open {
					e.HandleFor(context.Background(), testConvID, turnevent.TextChunk{MessageID: "main", Text: "buffered"})
				}
				id, phase, count := e.turnID, e.currentState, len(bcast.pushes)
				buffered := e.deltaBuf.String()
				e.HandleFor(context.Background(), testConvID, ev)
				if e.inTurn != open || e.turnID != id || e.currentState != phase || e.deltaBuf.String() != buffered {
					t.Fatalf("parent thinking changed lifecycle/buffer: inTurn=%v id=%q phase=%q buffer=%q", e.inTurn, e.turnID, e.currentState, e.deltaBuf.String())
				}
				if len(bcast.pushes) != count {
					t.Fatalf("parent thinking published frames: %v", pushTypes(bcast.pushes[count:]))
				}
			})
		}
	}
}

func TestTurnBusyTracker_ParentThinkingPreservesSendNow(t *testing.T) {
	t.Parallel()
	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{ParentToolCallID: "agent"},
		turnevent.ThinkingProgress{ParentToolCallID: "agent", EstimatedTokensDelta: 64},
	} {
		t.Run(fmt.Sprintf("%T", ev), func(t *testing.T) {
			tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess": testConvID}), discardLogger(), withSendNowGrace(time.Hour))
			tr.observe("sess", turnevent.TextChunk{})
			ok, undo := tr.openForSendNow(testConvID)
			if !ok {
				t.Fatal("send now refused open turn")
			}
			defer undo()
			tr.observe("sess", ev)
			tr.mu.Lock()
			carry := tr.carry[testConvID]
			tr.mu.Unlock()
			if carry != 1 || !tr.Busy(testConvID) {
				t.Fatal("parent thinking changed send-now carry")
			}
			tr.observe("sess", turnevent.TurnEnd{})
			tr.mu.Lock()
			gen, changed := tr.carried[testConvID], tr.changed
			tr.mu.Unlock()
			if gen == 0 {
				t.Fatal("no held-close generation")
			}
			tr.observe("sess", ev)
			tr.mu.Lock()
			after := tr.carried[testConvID]
			tr.mu.Unlock()
			if after != gen || !tr.Busy(testConvID) {
				t.Fatal("parent thinking retired release grace")
			}
			select {
			case <-changed:
				t.Fatal("parent thinking broadcast a change")
			default:
			}
			tr.releaseCarried(testConvID, gen)
			if tr.Busy(testConvID) {
				t.Fatal("preserved grace failed to release")
			}
		})
	}
}

func TestMainThinkingAfterTaskNotification(t *testing.T) {
	t.Parallel()
	for _, ev := range []turnevent.Event{turnevent.ThoughtChunk{}, turnevent.ThinkingProgress{EstimatedTokens: 64, EstimatedTokensDelta: 64}} {
		t.Run(fmt.Sprintf("%T", ev), func(t *testing.T) {
			p := newInitialThinkingPipeline(t)
			t.Cleanup(func() { p.emitter.flushTimer.Stop() })
			notification := turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"}
			p.busy.observe(initialThinkingSession, notification)
			p.emitter.HandleFor(context.Background(), testConvID, notification)
			if p.busy.Busy(testConvID) || p.emitter.inTurn {
				t.Fatal("notification opened main turn")
			}
			p.busy.observe(initialThinkingSession, ev)
			p.emitter.HandleFor(context.Background(), testConvID, ev)
			if !p.busy.Busy(testConvID) || !p.emitter.inTurn || p.emitter.currentState != "thinking" {
				t.Fatal("main thinking did not open notification-started turn")
			}
			states := pushesOfType(p.bcast.pushes, protocol.TypeTurnState)
			if len(states) != 1 {
				t.Fatalf("main thinking state count=%d", len(states))
			}
			p.busy.observe(initialThinkingSession, turnevent.TurnEnd{})
			p.emitter.HandleFor(context.Background(), testConvID, turnevent.TurnEnd{})
			if p.busy.Busy(testConvID) || p.emitter.inTurn {
				t.Fatal("main end did not close turn")
			}
		})
	}
}
