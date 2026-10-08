package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testRuntimeRotate(t *testing.T, tag *streamSessionTag, path, newID string) {
	t.Helper()
	if path == "Rotate" {
		tag.Rotate(newID)
	} else if !tag.CompareAndSwap(tag.ID(), newID) {
		t.Fatal("routing swap refused")
	}
}

func TestRuntimeHistoryRoutingRegistrationRefusals(t *testing.T) {
	sink := newStreamTurnSink(8, discardLogger())
	destination := newStreamSessionTag("b")
	sink.sinkForSessionTag(destination, "codex")
	tag := newStreamSessionTag("a")
	produce := sink.sinkForSessionTag(tag, "claude")
	follower := newSessionResetFollower(tag, func(string, string) error { return sessions.ErrSessionIDTaken }, produce, discardLogger())
	follower.follow("b")
	if tag.ID() != "a" || sink.runtimeProducers["b"] != destination.incarnation.Load() {
		t.Fatalf("refused reset changed ownership: tag=%s destination=%d want=%d", tag.ID(), sink.runtimeProducers["b"], destination.incarnation.Load())
	}
	tag.Rotate("")
	if tag.CompareAndSwap("a", "") || tag.CompareAndSwap("stale", "c") || tag.ID() != "a" {
		t.Fatal("refused rotation changed the tag")
	}
	if len(sink.runtimeProducers) != 2 {
		t.Fatalf("refused rotation registered a source: %v", sink.runtimeProducers)
	}
}

func TestRuntimeHistoryRotationBeforeOutput(t *testing.T) {
	for _, path := range []string{"Rotate", "CompareAndSwap"} {
		for _, mode := range []string{"runtime", "legacy_zero"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := history.New(t.TempDir())
				sink := newStreamTurnSink(32, discardLogger())
				resolve := stubBusyResolve(map[string]string{"a": testConvID, "b": testConvID, "c": testConvID})
				busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
				e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
				e.hist = store
				installRuntimeHistory(sink, e, busy)
				trans := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, resolve, discardLogger())
				trans.hist, trans.runtimeSink, trans.runtimeContext = store, sink, ctx
				tag := newStreamSessionTag("a")
				produce := sink.sinkForTag(tag.ID, "claude")
				if mode == "runtime" {
					produce = sink.sinkForSessionTag(tag, "claude")
				}
				produce(turnevent.ThoughtChunk{})
				trans.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "b", PreviousAgent: "claude", NextAgent: "claude", Cause: sessions.CauseOperatorReset, Reason: sessions.ReasonClear})
				testRuntimeRotate(t, tag, path, "b")
				// Capture B before acceptance, then commit its next boundary. No B
				// output has yet registered a watermark or opened a main turn.
				captured, release, produced := make(chan struct{}), make(chan struct{}), make(chan struct{})
				delayed := sink.sinkForProducer(func() string {
					id := tag.ID()
					close(captured)
					select {
					case <-release:
					case <-ctx.Done():
					}
					return id
				}, "claude", tag.incarnation.Load)
				go func() { delayed(turnevent.TextChunk{Text: "late-B"}); close(produced) }()
				<-captured
				trans.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "b", NewID: "c", PreviousAgent: "claude", NextAgent: "claude", Cause: sessions.CauseOperatorReset, Reason: sessions.ReasonClear})
				testRuntimeRotate(t, tag, path, "c")
				close(release)
				<-produced
				produce(turnevent.TextChunk{Text: "successor-C"})
				produce(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
				fence := make(chan struct{})
				sink.dispatchPlacement(ctx, func() { close(fence) })
				cleanup := startStreamTurnDrainV2(ctx, sink, e, resolve, busy, discardLogger())
				defer func() { cancel(); cleanup() }()
				select {
				case <-fence:
				case <-time.After(5 * time.Second):
					t.Fatal("rotation fence timed out")
				}
				var openings, order []string
				for _, entry := range historyEntries(t, store, testConvID) {
					switch entry.Type {
					case historyTurnOpened:
						openings = append(openings, entry.Session.SessionID)
					case historySessionDivider:
						var p runtimeHistoryFact
						if err := json.Unmarshal(entry.Payload, &p); err != nil {
							t.Fatal(err)
						}
						order = append(order, p.PreviousSessionID+"->"+p.NewSessionID)
					case protocol.TypeAssistantDelta:
						var p protocol.AssistantDeltaPayload
						if err := json.Unmarshal(entry.Payload, &p); err != nil {
							t.Fatal(err)
						}
						order = append(order, p.Text)
						if p.Text == "late-B" && (entry.Session == nil || entry.Session.SessionID != "b") {
							t.Fatalf("late source lost: %+v", entry)
						}
					}
				}
				if strings.Join(openings, ",") != "a,c" || strings.Join(order, ",") != "a->b,b->c,late-B,successor-C" || busy.Busy(testConvID) {
					t.Fatalf("openings=%v order=%v busy=%v", openings, order, busy.Busy(testConvID))
				}
			})
		}
	}
}

func TestRuntimeHistoryEvictionBeforeOutput(t *testing.T) {
	for _, path := range []string{"Rotate", "CompareAndSwap"} {
		for _, cause := range []sessions.LifecycleCause{sessions.CauseIdleSleep, sessions.CauseCapacityEviction} {
			for _, stop := range []string{"child_exit", "confirmed_stop"} {
				t.Run(path+"/"+string(cause)+"/"+stop, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					store := history.New(t.TempDir())
					sink := newStreamTurnSink(32, discardLogger())
					resolve := stubBusyResolve(map[string]string{"a": testConvID, "b": testConvID})
					busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
					delivery := testDelivery(t, t.TempDir(), store, nil)
					delivery.bind(busy)
					e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
					e.hist = store
					installRuntimeHistory(sink, e, busy)
					trans := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, resolve, discardLogger())
					trans.hist, trans.runtimeSink, trans.runtimeContext = store, sink, ctx
					tag := newStreamSessionTag("a")
					produce := sink.sinkForSessionTag(tag, "claude")
					trans.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "b", PreviousAgent: "claude", NextAgent: "claude", Cause: sessions.CauseOperatorReset, Reason: sessions.ReasonClear})
					testRuntimeRotate(t, tag, path, "b")
					if path == "Rotate" {
						sink.exitForSessionTag(tag)() // retiring A, before B emits anything
					}
					trans.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "b", PreviousAgent: "claude", Cause: cause, Reason: sessions.ReasonEviction})
					// Shutdown joins parsed tails after announcing the eviction.
					produce(turnevent.ToolStart{ToolCallID: "unfinished", Title: "Read"})
					produce(turnevent.TextChunk{Text: "B-tail"})
					if stop == "child_exit" {
						sink.exitForSessionTag(tag)()
					} else {
						sink.runnerStopped("b")
					}
					fence := make(chan struct{})
					sink.dispatchPlacement(ctx, func() {
						newOperatorMessageHistory(store, nil, nil, discardLogger())(testConvID, msgqueue.QueuedMessage{Text: "operator-B"})
						close(fence)
					})
					testAccept(t, delivery, conversations.ConversationID(testConvID), "post-B")
					delivery.drain()
					if len(historyEntries(t, store, testConvID)) != 0 {
						t.Fatal("channel post passed pending boundary")
					}
					cleanup := startStreamTurnDrainV2(ctx, sink, e, resolve, busy, discardLogger())
					defer func() { cancel(); cleanup() }()
					select {
					case <-fence:
					case <-time.After(5 * time.Second):
						t.Fatal("eviction did not consume the actual producer stop")
					}
					delivery.drain()
					var order []string
					for _, entry := range historyEntries(t, store, testConvID) {
						switch entry.Type {
						case protocol.TypeAssistantDelta:
							var p protocol.AssistantDeltaPayload
							if err := json.Unmarshal(entry.Payload, &p); err != nil {
								t.Fatal(err)
							}
							order = append(order, p.Text)
						case protocol.TypeMessage:
							order = append(order, "operator-B")
						case historyToolInterrupted, historyTurnInterrupted, historySessionDivider:
							var p runtimeHistoryFact
							if err := json.Unmarshal(entry.Payload, &p); err != nil {
								t.Fatal(err)
							}
							if p.Cause == string(cause) {
								order = append(order, entry.Type)
								if entry.Session == nil || entry.Session.SessionID != "b" {
									t.Fatalf("closure lost source: %+v", entry)
								}
							}
						}
					}
					want := "B-tail," + historyToolInterrupted + "," + historyTurnInterrupted + "," + historySessionDivider + ",operator-B,post-B"
					if strings.Join(order, ",") != want || busy.Busy(testConvID) {
						t.Fatalf("order=%v want=%s busy=%v", order, want, busy.Busy(testConvID))
					}
				})
			}
		}
	}
}
