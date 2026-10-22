package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestQueuedCanonicalCompletedTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}, {ConnID: "peer", Interactive: true, Thread: true}, {ConnID: "noninteractive"}}}}
	ring := eventring.New(64)
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }, OnAccepted: h.accepted, OnDelivered: operatorMessageHistory(store, nil, discardLogger(), h), OnTerminal: h.terminal, SendNow: func(context.Context, string, uint64, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	id := q.Enqueue(testConvID, "ping")
	queued := testCanonicalUsers(t, store, testConvID)
	if !q.SendNow(testConvID, id) {
		t.Fatal("send now refused")
	}
	e := newInteractiveTurnEmitterV2(nil, bcast, discardLogger())
	e.hist, e.ring = store, ring
	e.HandleFor(context.Background(), testConvID, turnevent.TextChunk{Text: "pong"}, history.SessionProvenance{})
	e.HandleFor(context.Background(), testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, history.SessionProvenance{})
	users := testCanonicalUsers(t, store, testConvID)
	if len(queued) != 1 || len(users) != 1 || users[0].ID != queued[0].ID || users[0].Status != "delivered" || users[0].Order == 0 {
		t.Fatal("canonical acceptance/delivery lost", users)
	}
	entries := historyEntries(t, store, testConvID)
	queuedIDs := map[uint64]bool{}
	for _, entry := range entries {
		if queuedSendHistoryType(entry.Type) || entry.Type == protocol.TypeMessage {
			queuedIDs[entry.ID] = true
		}
	}
	events, _ := ring.After(testConvID, 0)
	if len(events) == 0 || len(bcast.pushes) == 0 {
		t.Fatal("assistant traffic control absent")
	}
	for _, event := range events {
		if queuedIDs[event.HistoryEntryID] {
			t.Fatal("queued source reached newly appended replay")
		}
	}
	for _, push := range bcast.pushes {
		if push.env.HistoryEntryID != nil && queuedIDs[*push.env.HistoryEntryID] {
			t.Fatal("queued source reached app connection", push)
		}
	}
	if !reflect.DeepEqual(users, testCanonicalUsers(t, history.New(dir), testConvID)) {
		t.Fatal("reopened canonical state differs")
	}
}

func TestQueuedCanonicalStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "unresolved", TS: historyTS})
	queued := testCanonicalUsers(t, store, testConvID)
	accepted, err := readStartupSends(history.New(dir), testConvID)
	if err != nil {
		t.Fatal(err)
	}
	closeStartupSends(store, discardLogger(), testConvID, accepted, historyTS.Add(time.Second))
	for _, reader := range []*history.Store{store, history.New(dir)} {
		users := testCanonicalUsers(t, reader, testConvID)
		if len(users) != 1 || users[0].ID != queued[0].ID || users[0].Status != "lost" || !users[0].Shown || users[0].Active || users[0].Order != 0 {
			t.Fatal("restart loss must stay visible and undelivered", users)
		}
	}
}

func TestQueuedCanonicalSendNowDelayedConfirmation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	sink := newStreamTurnSink(8, discardLogger())
	place := newSendNowPlacement(ctx, func(string) (string, bool) { return testConvID, true }, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
	place.sendHistory = h
	place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
	entered, release, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
	record := operatorMessageHistory(store, place, discardLogger(), h)
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }, OnAccepted: h.accepted, OnTerminal: h.terminal,
		OnDelivered: func(c string, m msgqueue.QueuedMessage) { close(entered); <-release; record(c, m) },
		SendNow: func(ctx context.Context, c string, _ uint64, payload []byte) error {
			return place.write(ctx, c, payload, func() error { close(written); return nil })
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := q.EnqueueDelivery(testConvID, "app", "safe text", "host prompt")
	queued := testCanonicalUsers(t, store, testConvID)
	done := make(chan bool, 1)
	go func() { done <- q.SendNow(testConvID, id) }()
	<-written
	<-entered
	if len(historyEntries(t, store, testConvID)) != 1 {
		t.Fatal("write confirmation committed before placement")
	}
	place.echo("session", echoOf("host prompt"))
	raw := historyEntries(t, store, testConvID)
	if len(raw) != 3 {
		t.Fatal("echo did not commit user/outcome")
	}
	var user protocol.MessagePayload
	if json.Unmarshal(raw[1].Payload, &user) != nil || !user.SentNow {
		t.Fatal("send-now identity lost")
	}
	close(release)
	if !<-done || q.SendNow(testConvID, id) {
		t.Fatal("send-now queue behavior changed")
	}
	place.echo("session", echoOf("host prompt"))
	place.idle("session")
	if !reflect.DeepEqual(raw, historyEntries(t, store, testConvID)) {
		t.Fatal("confirmation/echo duplicated chronology")
	}
	users := testCanonicalUsers(t, store, testConvID)
	if len(users) != 1 || users[0].ID != queued[0].ID || users[0].Status != "delivered" || users[0].Order != raw[1].ID {
		t.Fatal("placement lost canonical identity/order", users)
	}
	if !reflect.DeepEqual(users, testCanonicalUsers(t, history.New(dir), testConvID)) {
		t.Fatal("reopened delayed placement differs")
	}
}

func TestQueuedCanonicalStorageFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, TS: historyTS})
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 2, TS: historyTS})
	before := testCanonicalUsers(t, store, testConvID)
	saved := dir + "-saved"
	if err := os.Rename(dir, saved); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(saved)
	if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	operatorMessageHistory(store, nil, discardLogger(), h)(testConvID, msgqueue.QueuedMessage{ID: 1, TS: historyTS, Text: "safe"})
	h.terminal(testConvID, msgqueue.QueuedMessage{ID: 2}, msgqueue.TerminalRemoved)
	if raw := historyEntries(t, history.New(saved), testConvID); len(raw) != 2 {
		t.Fatal("failed writes changed durable allocation")
	}
	if !reflect.DeepEqual(before, testCanonicalUsers(t, history.New(saved), testConvID)) {
		t.Fatal("failed writes fabricated delivery or drop")
	}
}
