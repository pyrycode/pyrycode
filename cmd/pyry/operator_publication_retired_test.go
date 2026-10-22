package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestOperatorPublicationRetired(t *testing.T) {
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	e, b, ring := testQueuedReceiptEmitter(h)
	m := msgqueue.QueuedMessage{ID: 1, MessageID: "app", DeviceID: "device", DeviceName: "phone", ClientVersion: "1", TS: historyTS, Text: "safe text", AttachmentIDs: []string{"attachment"}}
	h.accepted(testConvID, m)
	queued := testCanonicalUsers(t, store, testConvID)
	if len(queued) != 1 || queued[0].Order != 0 || queued[0].Status != "queued" {
		t.Fatalf("accepted canonical item: %+v", queued)
	}
	operatorMessageHistory(store, nil, discardLogger(), h)(testConvID, m)
	e.broadcast(context.Background(), b, ring)
	if len(historyEntries(t, store, testConvID)) != 3 {
		t.Fatal("acceptance, delivery source and outcome must survive")
	}
	events, _ := ring.After(testConvID, 0)
	if len(events) != 2 || events[0].Type != protocol.TypeBanner || events[1].Type != protocol.TypeBanner {
		t.Fatalf("replay must contain only acceptance/delivered receipts: %v", events)
	}
	for _, p := range b.pushes {
		if p.env.Type == protocol.TypeMessage {
			t.Fatal("raw user message reached app fan-out")
		}
	}
	users := testCanonicalUsers(t, store, testConvID)
	if len(users) != 1 || users[0].ID != queued[0].ID || users[0].Status != "delivered" || users[0].Order == 0 {
		t.Fatalf("delivered canonical item: %+v", users)
	}
	var content map[string]any
	if json.Unmarshal(users[0].Content, &content) != nil || content["device_id"] != "device" || content["text"] != "safe text" {
		t.Fatal("canonical sender/content lost")
	}
	if !reflect.DeepEqual(users, testCanonicalUsers(t, history.New(dir), testConvID)) {
		t.Fatal("reopened canonical item differs")
	}
	if len(testCanonicalUsers(t, store, testConvIDB)) != 0 {
		t.Fatal("cross-conversation user")
	}
}

func testCanonicalUsers(t *testing.T, store *history.Store, conv string) []thread.Item {
	t.Helper()
	f := thread.New(conv)
	if err := f.Feed(historyEntries(t, store, conv)); err != nil {
		t.Fatal(err)
	}
	var users []thread.Item
	for _, item := range f.Items() {
		if item.Kind == "user_message" {
			users = append(users, item)
		}
	}
	return users
}

func TestOperatorReceiptWorkerJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := testSendHistory(history.New(t.TempDir()))
	e, _, ring := testQueuedReceiptEmitter(h)
	b := &testQueuedBlockedBroadcaster{entered: make(chan struct{}, 1), release: make(chan struct{})}
	stop := startOperatorMessageStreamV2(ctx, e, b, ring)
	defer func() { cancel(); stop() }()
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, TS: historyTS})
	awaitProvenanceSignal(t, b.entered)
	cancel()
	stop()
}

func TestOperatorReceiptMailboxBounded(t *testing.T) {
	var logs bytes.Buffer
	h := testSendHistory(history.New(t.TempDir()))
	e := newOperatorMessageEmitterV2(slog.New(slog.NewTextHandler(&logs, nil)))
	h.publish = e.notifyReceipt
	for id := uint64(1); id <= operatorMessageQueueSize+1; id++ {
		h.accepted(testConvID, msgqueue.QueuedMessage{ID: id, TS: historyTS, Text: opText, AttachmentIDs: []string{opHostPath}})
	}
	if len(e.pending) != operatorMessageQueueSize || !strings.Contains(logs.String(), "send_history.queue_full") {
		t.Fatal("receipt mailbox is not bounded")
	}
	for _, secret := range []string{opText, opHostPath} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("overflow log leaked send content")
		}
	}
}

func TestOperatorReceiptOptionalTransport(t *testing.T) {
	for _, noRing := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_peers", true: "no_ring"}[noRing], func(t *testing.T) {
			h := testSendHistory(history.New(t.TempDir()))
			e, b, ring := testQueuedReceiptEmitter(h)
			if noRing {
				ring = nil
			} else {
				b.snapshots = [][]relay.ActiveConn{{}}
			}
			h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, TS: historyTS})
			e.broadcast(context.Background(), b, ring)
			if noRing {
				if len(b.pushes) != 2 {
					t.Fatal("receipt recipients lost without ring")
				}
				for _, p := range b.pushes {
					if p.env.EventID != nil || p.env.HistoryEntryID == nil || *p.env.HistoryEntryID != 1 || p.env.Type != protocol.TypeBanner || !p.env.TS.Equal(historyTS) {
						t.Fatal("receipt identity without ring changed")
					}
				}
			} else {
				events, _ := ring.After(testConvID, 0)
				if len(events) != 1 || len(b.pushes) != 0 || events[0].HistoryEntryID != 1 {
					t.Fatal("receipt replay lost without peers")
				}
			}
		})
	}
}
