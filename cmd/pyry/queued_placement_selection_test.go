package main

import (
	"context"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestQueuedPlacementSelectionFloor(t *testing.T) {
	for _, echo := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "identical_echo"}[echo], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolve := stubBusyResolve(map[string]string{"s": testConvID})
			sink := newStreamTurnSink(64, discardLogger())
			bin, _ := fakeExitingClaude(t, t.TempDir())
			runner := runFactoryRunner(t, sink, bin, "s")
			testShadowWait(t, func() bool { return runner.State().ChildPID != 0 })
			child := runner.(streamRunner).r.ChildTarget()
			store := history.New(t.TempDir())
			place := newSendNowPlacement(ctx, resolve, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
			place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
			commits := 0
			place.record = func(string, msgqueue.QueuedMessage, ...history.SessionProvenance) { commits++ }
			stale := turnevent.Event(snEnd)
			if echo {
				stale = echoOf("identical")
			}
			q, err := msgqueue.New(msgqueue.Config{SendNow: func(ctx context.Context, conv string, _ uint64, payload []byte) error {
				return place.writeTarget(ctx, conv, payload, func(ctx context.Context) error {
					// Accepted after placement registration but before stdin selection.
					sink.forwardEvent(streamTurnEnvelope{sessionID: "s", child: child, ev: stale})
					return runner.WriteUserTurn(ctx, conv, payload)
				})
			}, Deliver: func(context.Context, string, []byte) error { return nil }, Logger: discardLogger()})
			if err != nil {
				t.Fatal(err)
			}
			id := q.EnqueueDelivery(testConvID, "app", "safe", "identical")
			if !q.SendNow(testConvID, id) {
				t.Fatal("write refused")
			}
			place.mu.Lock()
			entry := place.managed[testConvID][id]
			place.mu.Unlock()
			busy := newTurnBusyTracker(resolve, discardLogger())
			emitter := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			emitter.hist = store
			installRuntimeHistory(sink, emitter, busy)
			stop := startStreamTurnDrainV2(ctx, sink, emitter, resolve, busy, discardLogger())
			defer func() { cancel(); stop() }()
			placementDrainBarrier(t, ctx, sink)
			sink.dispatchPlacement(ctx, func() { place.takeQueued(testConvID, entry) })
			placementDrainBarrier(t, ctx, sink)
			if commits != 0 {
				t.Fatal("pre-selection output confirmed new input")
			}
			sink.forwardEvent(streamTurnEnvelope{sessionID: "s", child: child, ev: stale})
			placementDrainBarrier(t, ctx, sink)
			sink.dispatchPlacement(ctx, func() { place.takeQueued(testConvID, entry) })
			placementDrainBarrier(t, ctx, sink)
			if commits != 1 {
				t.Fatalf("fresh evidence committed %d times", commits)
			}
		})
	}
}
