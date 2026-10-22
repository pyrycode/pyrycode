package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestOrdinaryQueuePlacement_OrderWithDelayedConfirmation(t *testing.T) {
	for _, releaseAfterEnd := range []bool{false, true} {
		for _, noEcho := range []bool{false, true} {
			t.Run(fmtPlacementCase(releaseAfterEnd, noEcho), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				sink := newStreamTurnSink(64, discardLogger())
				bcast := newChanBcast("phone")
				store := history.New(t.TempDir())
				active := &stubActiveSession{}
				active.set("s1")
				active.bind("s2", testConvIDB)
				busy := newTurnBusyTracker(active.get, discardLogger())
				place := newSendNowPlacement(ctx, active.get, busy.WaitIdle)
				sink.setEchoObserver(place.echo)
				cursor := &stubCursor{}
				cursor.set(testConvID)
				emitter := newInteractiveTurnEmitterV2(cursor, bcast, discardLogger())
				emitter.hist = store
				place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
				stopDrain := startStreamTurnDrainV2(ctx, sink, emitter, active.get, busy, discardLogger())
				defer func() { cancel(); stopDrain() }()
				callback := make(chan struct{})
				entered := make(chan struct{}, 1)
				wrote := make(chan string, 2)
				writer := funcWriter{write: func(_ context.Context, _ string, payload []byte) error {
					wrote <- string(payload)
					return nil
				}}
				deliver := newInboundDeliver(func(string) (handlers.TurnWriter, error) { return writer, nil }, busy, time.Second, place)
				record := newOperatorMessageHistory(store, place, discardLogger())
				q, err := msgqueue.New(msgqueue.Config{Deliver: deliver, OnDelivered: func(conv string, msg msgqueue.QueuedMessage) {
					entered <- struct{}{}
					select {
					case <-callback:
					case <-ctx.Done():
						return
					}
					record(conv, msg)
				}, Logger: discardLogger()})
				if err != nil {
					t.Fatal(err)
				}
				qDone := make(chan error, 1)
				go func() { qDone <- q.Run(ctx) }()
				defer func() { cancel(); <-qDone }()
				send := sink.sinkFor("s1")
				send(snOpener)
				collectEnvs(t, bcast.pushed, 1)
				id := q.EnqueueDelivery(testConvID, "duplicate-id", "safe user text", "composed /host/private prompt")
				send(snEnd)
				preceding := collectEnvs(t, bcast.pushed, 3)
				if preceding[1].Type != protocol.TypeTurnEnd {
					t.Fatal(envTypes(preceding))
				}
				select {
				case got := <-wrote:
					if got != "composed /host/private prompt" {
						t.Fatal(got)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("no write")
				}
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("no confirmation")
				}
				sink.sinkFor("s2")(echoOf("composed /host/private prompt"))
				if !noEcho {
					send(echoOf("composed /host/private prompt"))
				}
				send(turnevent.TextChunk{MessageID: "reply", Text: "answer"})
				if releaseAfterEnd || noEcho {
					send(snEnd)
				}
				want := 1
				if releaseAfterEnd || noEcho {
					want = 4
				}
				got := collectEnvs(t, bcast.pushed, want)
				users := testCanonicalUsers(t, store, testConvID)
				if len(users) != 1 || users[0].Summary != "safe user text" || users[0].Status != "delivered" || users[0].Order == 0 {
					t.Fatalf("placed canonical item: %+v", users)
				}
				var placed protocol.MessagePayload
				if json.Unmarshal(users[0].Content, &placed) != nil || placed.QueuedMsgID != id {
					t.Fatal("placement lost queue identity")
				}

				close(callback)
				send(echoOf("composed /host/private prompt"))
				if !releaseAfterEnd && !noEcho {
					send(snEnd)
					got = append(got, collectEnvs(t, bcast.pushed, 3)...)
				}
				// A barrier event in another conversation makes late callback/echo processing observable.
				sink.sinkFor("s2")(snOpener)
				collectEnvs(t, bcast.pushed, 1)
				entries, err := store.Page(testConvID, "", 100)
				if err != nil {
					t.Fatal(err)
				}
				replay, gap := emitter.ring.After(testConvID, *preceding[len(preceding)-1].EventID)
				if gap || len(replay) != len(got) {
					t.Fatalf("replay=%d live=%d gap=%v", len(replay), len(got), gap)
				}
				for i, e := range replay {
					if e.Type == protocol.TypeMessage || e.Type != got[i].Type || e.ID != *got[i].EventID || !e.TS.Equal(got[i].TS) || !bytes.Equal(e.Payload, got[i].Payload) {
						t.Fatal("raw user publication or replay mismatch")
					}
				}
				if len(testCanonicalUsers(t, store, testConvID)) != 1 {
					t.Fatal("late callback duplicated canonical item")
				}
				if len(entries.Entries) == 0 {
					t.Fatal("history lost")
				}

			})
		}
	}
}

func fmtPlacementCase(afterEnd, noEcho bool) string {
	if afterEnd {
		if noEcho {
			return "after_end/no_echo"
		}
		return "after_end/echo"
	}
	if noEcho {
		return "after_echo/no_echo"
	}
	return "after_echo/echo"
}

func TestOrdinaryQueuePlacement_IdenticalWritesInterleaveWithSendNow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newStreamTurnSink(64, discardLogger())
	bcast := newChanBcast("phone")
	store := history.New(t.TempDir())
	resolve := func(s string) (string, bool) {
		if s == "s1" {
			return testConvID, true
		}
		return testConvIDB, s == "s2"
	}
	busy := newTurnBusyTracker(resolve, discardLogger(), withSendNowGrace(time.Hour))
	place := newSendNowPlacement(ctx, resolve, busy.WaitIdle)
	place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
	sink.setEchoObserver(place.echo)
	emitter := newInteractiveTurnEmitterV2(&stubCursor{}, bcast, discardLogger())
	emitter.hist = store
	stop := startStreamTurnDrainV2(ctx, sink, emitter, resolve, busy, discardLogger())
	defer func() { cancel(); stop() }()
	writes := make(chan struct{}, 3)
	writer := funcWriter{write: func(context.Context, string, []byte) error { writes <- struct{}{}; return nil }}
	writerFor := func(string) (handlers.TurnWriter, error) { return writer, nil }
	record := newOperatorMessageHistory(store, place, discardLogger())
	held := make(chan struct{})
	entered := make(chan struct{}, 1)
	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(writerFor, busy, time.Second, place),
		SendNow: newSendNowDeliver(writerFor, func(string) bool { return true }, busy, place),
		OnDelivered: func(c string, m msgqueue.QueuedMessage) {
			if m.ID == 1 {
				entered <- struct{}{}
				select {
				case <-held:
				case <-ctx.Done():
					return
				}
			}
			record(c, m)
		}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitWrite := func() {
		t.Helper()
		select {
		case <-writes:
		case <-time.After(2 * time.Second):
			t.Fatal("no write")
		}
	}
	send := sink.sinkFor("s1")
	id1 := q.EnqueueDelivery(testConvID, "duplicate", "safe", "identical delivery")
	waitWrite()
	<-entered
	sink.sinkFor("s2")(echoOf("identical delivery"))
	sink.sinkFor("s2")(snOpener)
	if got := collectEnvs(t, bcast.pushed, 1); got[0].Type != protocol.TypeTurnState {
		t.Fatal("foreign echo placed the message")
	}
	send(echoOf("identical delivery"))
	send(snOpener)
	collectEnvs(t, bcast.pushed, 1)
	id2 := q.EnqueueDelivery(testConvID, "duplicate", "safe", "identical delivery")
	if !q.SendNow(testConvID, id2) {
		t.Fatal("send-now refused")
	}
	waitWrite()
	send(echoOf("identical delivery"))
	placementDrainBarrier(t, ctx, sink)
	id3 := q.EnqueueDelivery(testConvID, "duplicate", "safe", "identical delivery")
	close(held)
	send(snEnd)
	collectEnvs(t, bcast.pushed, 3)
	// The send-now carry opens a second turn; ordinary delivery still waits for it.
	send(snOpener)
	send(snEnd)
	collectEnvs(t, bcast.pushed, 4)
	waitWrite()
	send(echoOf("identical delivery"))
	send(snOpener)
	send(snEnd)
	collectEnvs(t, bcast.pushed, 4)
	users := testCanonicalUsers(t, store, testConvID)
	if len(users) != 3 {
		t.Fatalf("canonical users: %+v", users)
	}
	for i, item := range users {
		var p protocol.MessagePayload
		if json.Unmarshal(item.Content, &p) != nil || p.QueuedMsgID != []uint64{id1, id2, id3}[i] || p.MessageID != "duplicate" || p.SentNow != (i == 1) {
			t.Fatalf("wrong matched write: %+v", item)
		}
	}

	page, err := store.Page(testConvID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint64
	for _, entry := range page.Entries {
		if entry.Type == protocol.TypeMessage {
			var p protocol.MessagePayload
			json.Unmarshal(entry.Payload, &p)
			ids = append(ids, p.QueuedMsgID)
		}
	}
	if !slices.Equal(ids, []uint64{id3, id2, id1}) {
		t.Fatalf("history order=%v", ids)
	}
}

func TestOrdinaryQueuePlacement_FailedWriteRetriesOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newStreamTurnSink(32, discardLogger())
	resolve := func(string) (string, bool) { return testConvID, true }
	busy := newTurnBusyTracker(resolve, discardLogger())
	place := newSendNowPlacement(ctx, resolve, busy.WaitIdle)
	store := history.New(t.TempDir())
	place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
	sink.setEchoObserver(place.echo)
	bcast := newChanBcast("phone")
	emitter := newInteractiveTurnEmitterV2(&stubCursor{}, bcast, discardLogger())
	stop := startStreamTurnDrainV2(ctx, sink, emitter, resolve, busy, discardLogger())
	defer func() { cancel(); stop() }()
	var attempts int
	retry := make(chan struct{})
	wrote := make(chan struct{})
	writer := funcWriter{write: func(_ context.Context, _ string, _ []byte) error {
		attempts++
		if attempts == 1 {
			return errNoBoundSession
		}
		close(wrote)
		select {
		case <-retry:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}}
	record := newOperatorMessageHistory(store, place, discardLogger())
	q, err := msgqueue.New(msgqueue.Config{Deliver: newInboundDeliver(func(string) (handlers.TurnWriter, error) { return writer, nil }, busy, time.Second, place), OnDelivered: record, RetryInterval: time.Millisecond, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	defer func() { cancel(); <-done }()
	id := q.EnqueueDelivery(testConvID, "m", "safe", "composed")
	select {
	case <-wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not write")
	}
	if len(bcast.pushed) != 0 {
		t.Fatal("failed attempt emitted a record")
	}
	close(retry)
	sink.sinkFor("s1")(echoOf("composed"))
	sink.sinkFor("s1")(snOpener)
	sink.sinkFor("s1")(snEnd)
	got := collectEnvs(t, bcast.pushed, 4)
	for _, env := range got {
		if env.Type == protocol.TypeMessage {
			t.Fatal("raw user frame")
		}
	}
	users := testCanonicalUsers(t, store, testConvID)
	var p protocol.MessagePayload
	if len(users) != 1 || json.Unmarshal(users[0].Content, &p) != nil || p.QueuedMsgID != id {
		t.Fatal("retry lost canonical identity")
	}
}
