package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestQueuedLegacyReceipts_CompletedTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	ome, bcast, ring := testQueuedReceiptEmitter(h)
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }, OnAccepted: h.accepted, OnDelivered: operatorMessageHistory(store, nil, discardLogger(), h), OnTerminal: h.terminal, SendNow: func(context.Context, string, uint64, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	id := q.Enqueue(testConvID, "ping")
	if !q.SendNow(testConvID, id) {
		t.Fatal("send now refused")
	}
	ome.broadcast(context.Background(), bcast, ring)
	e := newInteractiveTurnEmitterV2(nil, bcast, discardLogger())
	e.hist, e.ring = store, ring
	e.HandleFor(context.Background(), testConvID, turnevent.TextChunk{Text: "pong"}, history.SessionProvenance{})
	e.HandleFor(context.Background(), testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, history.SessionProvenance{})
	ome.broadcast(context.Background(), bcast, ring)
	testQueuedReceiptIdentity(t, store, bcast, ring)
	raw := historyEntries(t, store, testConvID)
	for _, reader := range []*history.Store{store, history.New(dir)} {
		if latest, err := reader.LatestEntryID(conversations.ConversationID(testConvID)); err != nil || latest != raw[len(raw)-1].ID {
			t.Fatalf("raw durable target=%d,%v", latest, err)
		}
		if !reflect.DeepEqual(historyEntries(t, reader, testConvID), raw) {
			t.Fatal("raw facts/metadata changed")
		}
	}
}

func TestQueuedLegacyReceipts_Validation(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{historySendAccepted, historySendDelivered, historySendDropped, historySendLost} {
		reason := map[string]string{historySendDelivered: "delivered", historySendDropped: "removed", historySendLost: "daemon_restart"}[typ]
		p := queuedSendFact{ConversationID: testConvID, AcceptedAt: historyTS, OccurredAt: historyTS, AcceptedEntryID: 1, DeliveryEntryID: 2, Reason: reason}
		raw, _ := json.Marshal(p)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		required := []string{"conversation_id"}
		if typ == historySendAccepted {
			required = append(required, "accepted_at")
		} else {
			required = append(required, "occurred_at", "accepted_entry_id", "reason")
		}
		if typ == historySendDelivered {
			required = append(required, "delivery_entry_id")
		}
		id := uint64(1)
		if _, ok := legacyRuntimeReceipt(testConvID, typ, raw, &id, historyTS); !ok {
			t.Fatalf("valid optional-free %s rejected", typ)
		}
		for _, field := range required {
			for _, value := range []any{nil, "", float64(0), "omit"} {
				mutated := make(map[string]any)
				for k, v := range fields {
					mutated[k] = v
				}
				mutated[field] = value
				if value == "omit" {
					delete(mutated, field)
				}
				bytes, _ := json.Marshal(mutated)
				if _, ok := legacyRuntimeReceipt(testConvID, typ, bytes, &id, historyTS); ok {
					t.Fatalf("%s accepted invalid %s=%v", typ, field, value)
				}
			}
		}
		for _, bad := range []string{`{}`, `null`, `{"conversation_id":"wrong"}`, `{"reason":"unsupported"}`} {
			if _, ok := legacyRuntimeReceipt(testConvID, typ, json.RawMessage(bad), &id, historyTS); ok {
				t.Fatal("bad fact accepted")
			}
		}
		for _, identity := range []*uint64{nil, new(uint64)} {
			if _, ok := legacyRuntimeReceipt(testConvID, typ, raw, identity, historyTS); ok {
				t.Fatal("unidentified fact accepted")
			}
		}
		if _, ok := legacyRuntimeReceipt(testConvID, typ, raw, &id, time.Time{}); ok {
			t.Fatal("zero timestamp accepted")
		}
		fields["reason"] = "unsupported"
		raw, _ = json.Marshal(fields)
		if typ != historySendAccepted {
			if _, ok := legacyRuntimeReceipt(testConvID, typ, raw, &id, historyTS); ok {
				t.Fatal("unsupported reason accepted")
			}
		}
	}
}

func testQueuedReceiptEmitter(h *queuedSendHistory) (*operatorMessageEmitterV2, *fakeInteractiveBcast, *eventring.Ring) {
	ome := newOperatorMessageEmitterV2(discardLogger())
	h.publish = ome.notifyReceipt
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}, {ConnID: "peer", Interactive: true, Thread: true}, {ConnID: "noninteractive"}}}}
	return ome, bcast, eventring.New(64)
}

func testQueuedReceiptIdentity(t *testing.T, store *history.Store, bcast *fakeInteractiveBcast, ring *eventring.Ring) {
	t.Helper()
	page, _ := testLegacyReceipts(t, store)
	filtered := page[:0]
	for _, entry := range page {
		if entry.Type != protocol.TypeMessage {
			filtered = append(filtered, entry)
		}
	}
	page = filtered
	events, _ := ring.After(testConvID, 0)
	if len(events) != len(page) || len(bcast.pushes) != 2*len(events) {
		t.Fatalf("page/live/replay counts %d/%d/%d", len(page), len(bcast.pushes), len(events))
	}
	for i, event := range events {
		expected := page[len(page)-1-i]
		if event.HistoryEntryID != expected.ID || event.Type != expected.Type || !event.TS.Equal(expected.TS) || !bytes.Equal(event.Payload, expected.Payload) {
			t.Fatalf("replay identity/chronology at %d", i)
		}
		for _, p := range bcast.pushes[i*2 : i*2+2] {
			if p.connID == "noninteractive" || p.env.HistoryEntryID == nil || *p.env.HistoryEntryID != expected.ID || p.env.Type != expected.Type || !p.env.TS.Equal(expected.TS) || !bytes.Equal(p.env.Payload, expected.Payload) {
				t.Fatalf("live identity/gate at %d", i)
			}
		}
	}
}

func TestQueuedLegacyReceipts_Startup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	ome, bcast, ring := testQueuedReceiptEmitter(h)
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, TS: historyTS})
	ome.broadcast(context.Background(), bcast, ring)
	before, _ := ring.After(testConvID, 0)
	accepted, err := readStartupSends(history.New(dir), conversations.ConversationID(testConvID))
	if err != nil {
		t.Fatal(err)
	}
	closeStartupSends(history.New(dir), discardLogger(), testConvID, accepted, historyTS.Add(time.Second))
	ome.broadcast(context.Background(), bcast, ring)
	after, _ := ring.After(testConvID, 0)
	if !reflect.DeepEqual(before, after) || len(bcast.pushes) != 2 {
		t.Fatal("startup backfilled live/replay")
	}
	for _, reader := range []*history.Store{store, history.New(dir)} {
		page, _ := testLegacyReceipts(t, reader)
		if len(page) != 2 || page[0].Type != protocol.TypeBanner {
			t.Fatal("loss receipt absent")
		}
	}
}

type testQueuedBlockedBroadcaster struct{ entered, release chan struct{} }

func (b *testQueuedBlockedBroadcaster) ActiveConns(context.Context) []relay.ActiveConn {
	return []relay.ActiveConn{{ConnID: "phone", Interactive: true}}
}
func (b *testQueuedBlockedBroadcaster) Push(ctx context.Context, _ string, _ protocol.Envelope) error {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestQueuedLegacyReceipts_NonblockingPublication(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	h := testSendHistory(store)
	ome, _, ring := testQueuedReceiptEmitter(h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &testQueuedBlockedBroadcaster{entered: make(chan struct{}, 1), release: make(chan struct{})}
	stop := startOperatorMessageStreamV2(ctx, ome, b, ring)
	defer func() { cancel(); stop() }()
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 100, TS: historyTS})
	select {
	case <-b.entered:
	case <-time.After(time.Second):
		t.Fatal("peer did not stall")
	}
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }, OnAccepted: h.accepted, OnTerminal: h.terminal,
		OnDelivered: operatorMessageHistory(store, nil, discardLogger(), h),
		SendNow:     func(context.Context, string, uint64, []byte) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		id := q.Enqueue(testConvID, "safe")
		if !q.SendNow(testConvID, id) {
			t.Error("send now refused")
		}
		id = q.Enqueue(testConvID, "drop")
		q.Remove(testConvID, id)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue callback waited for peer")
	}
	close(b.release)
}

func TestQueuedLegacyReceipts_SendNowDelayedConfirmation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := history.New(t.TempDir())
	h := testSendHistory(store)
	ome, bcast, ring := testQueuedReceiptEmitter(h)
	sink := newStreamTurnSink(8, discardLogger())
	place := newSendNowPlacement(ctx, func(string) (string, bool) { return testConvID, true }, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
	place.sendHistory = h
	place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
	entered := make(chan struct{})
	release := make(chan struct{})
	written := make(chan struct{})
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
	done := make(chan bool, 1)
	go func() { done <- q.SendNow(testConvID, id) }()
	<-written
	<-entered
	if len(historyEntries(t, store, testConvID)) != 1 {
		t.Fatal("write confirmation committed before placement")
	}
	place.echo("session", echoOf("host prompt"))
	ome.broadcast(context.Background(), bcast, ring)
	testQueuedReceiptIdentity(t, store, bcast, ring)
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
	ome.broadcast(context.Background(), bcast, ring)
	testQueuedReceiptIdentity(t, store, bcast, ring)
	if !reflect.DeepEqual(raw, historyEntries(t, store, testConvID)) {
		t.Fatal("confirmation/echo duplicated chronology")
	}
}

func TestQueuedLegacyReceipts_StorageFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	ome, bcast, ring := testQueuedReceiptEmitter(h)
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, TS: historyTS})
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 2, TS: historyTS})
	ome.broadcast(context.Background(), bcast, ring)
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
	ome.broadcast(context.Background(), bcast, ring)
	events, _ := ring.After(testConvID, 0)
	if len(events) != 2 || len(bcast.pushes) != 4 {
		t.Fatal("failed delivery/drop minted a receipt")
	}

	if raw := historyEntries(t, history.New(saved), testConvID); len(raw) != 2 {
		t.Fatal("failed writes changed durable allocation")
	}
}
