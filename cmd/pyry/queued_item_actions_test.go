package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	historylog "github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func testBoundQueue(t *testing.T, h *queuedSendHistory, first uint64) *msgqueue.Queue {
	t.Helper()
	q, err := msgqueue.New(msgqueue.Config{Logger: discardLogger(),
		Deliver: func(context.Context, string, []byte) error { return nil },
		SendNow: func(context.Context, string, uint64, []byte) error { return nil },
		OnAccepted: func(c string, m msgqueue.QueuedMessage) {
			if m.ID >= first {
				h.accepted(c, m)
			}
		},
		OnTerminal: func(c string, m msgqueue.QueuedMessage, o msgqueue.TerminalOutcome) {
			if m.ID >= first {
				h.terminal(c, m, o)
			}
		},
		OnDelivered: operatorMessageHistory(h.store, func(operatorMessage) {}, nil, discardLogger(), h),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.bindItem = q.BindAcceptedItem
	for id := uint64(1); id < first; id++ {
		q.Remove(testConvID, q.Enqueue(testConvID, "padding"))
	}
	return q
}

func testBoundAction(t *testing.T, w *switchWire, typ string, queueID, itemID uint64) {
	t.Helper()
	p := map[string]any{"conversation_id": testConvID, "queued_msg_id": queueID, "accepted_item_id": itemID}
	testDaemonThreadRequest(t, w, "phone", 40, typ, p)
	// A subsequent query on the same connection is the command completion barrier;
	// any unsolicited queue_state or command reply also fails the reply assertion.
	testDaemonThreadRequest(t, w, "phone", 41, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 10})
	testDaemonThreadReply(t, w, 41)
}

func TestBoundQueueActionsRestart(t *testing.T) {
	for _, typ := range []string{protocol.TypeDequeueMessage, protocol.TypeSendQueuedNow} {
		t.Run(typ, func(t *testing.T) {
			s, history, reg, store := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			for range 99 {
				testDaemonThreadAppend(t, history, "unknown", `{}`)
			}
			old := testBoundQueue(t, testSendHistory(history), 7)
			if old.Enqueue(testConvID, "old") != 7 {
				t.Fatal("old queue ID")
			}
			pending, err := readStartupSends(history, testConvID)
			if err != nil {
				t.Fatal(err)
			}
			closeStartupSends(history, discardLogger(), testConvID, pending, time.Now())
			dir, err := history.LogDir(testConvID)
			if err != nil {
				t.Fatal(err)
			}
			history = historylog.New(filepath.Dir(filepath.Dir(filepath.Dir(dir))))
			s.history = history
			if err := store.Shutdown(); err != nil {
				t.Fatal(err)
			}
			store = thread.NewStore(history)
			s.store = store
			for i := 101; i < 204; i++ {
				testDaemonThreadAppend(t, history, "unknown", `{}`)
			}
			h := testSendHistory(history) // new run has no old references
			q := testBoundQueue(t, h, 7)
			if q.Enqueue(testConvID, "new") != 7 {
				t.Fatal("new queue ID")
			}
			testDaemonThreadLoad(t, store, 205)
			items := store.Observe(testConvID).Items
			if len(items) != 2 {
				t.Fatal("restart items", items)
			}
			testQueuedHandle(t, daemonThreadItem(items[0]), 100, "lost")
			testQueuedHandle(t, daemonThreadItem(items[1]), 205, "queued")
			w, _ := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
				c.QueueRemover = q
				c.QueueSender = suggestionQueueSender{inner: q}
			})
			testDaemonThreadCursor(s)
			w.open("phone", false, protocol.CapabilityThread)
			testBoundAction(t, w, typ, 7, 100)
			if len(q.Snapshot(testConvID)) != 1 || len(h.refs) != 1 {
				t.Fatal("lost binding affected replacement")
			}
			testBoundAction(t, w, typ, 7, 205)
			if len(q.Snapshot(testConvID)) != 0 {
				t.Fatal("matching binding did not act")
			}
			entries := testAllSendEntries(t, history)
			before := len(entries)
			testBoundAction(t, w, typ, 7, 205)
			if len(testAllSendEntries(t, history)) != before {
				t.Fatal("retired binding emitted history")
			}
		})
	}
}

func TestQueueBindingBeforePublication(t *testing.T) {
	t.Parallel()
	h := testSendHistory(historylog.New(t.TempDir()))
	q := testBoundQueue(t, h, 1)
	called := false
	// Removal during the acceptance receipt defers its terminal callback; do not
	// recursively remove when the subsequent drop receipt is published.
	h.publish = func(m operatorMessage) {
		if called {
			return
		}
		called = true
		if m.historyEntryID == nil || !q.RemoveItem(testConvID, 1, *m.historyEntryID) {
			t.Error("published acceptance was not yet bound")
		}
	}
	q.Enqueue(testConvID, "queued")
	if !called || len(q.Snapshot(testConvID)) != 0 {
		t.Fatal("acceptance publication missing")
	}
	failed := testSendHistory(nil)
	failed.bindItem = func(string, uint64, uint64) { t.Error("failed append established binding") }
	failed.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, TS: time.Now()})
}

func TestBoundQueueActionsDevices(t *testing.T) {
	for _, typ := range []string{protocol.TypeDequeueMessage, protocol.TypeSendQueuedNow} {
		t.Run(typ, func(t *testing.T) {
			s, history, reg, store := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			q := testBoundQueue(t, testSendHistory(history), 1)
			q.EnqueueIdentified(testConvID, "shared", "one", "one", nil, "device-one", "", "", time.Time{})
			q.EnqueueIdentified(testConvID, "shared", "two", "two", nil, "device-two", "", "", time.Time{})
			testDaemonThreadLoad(t, store, 2)
			w, _ := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) { c.QueueRemover = q; c.QueueSender = q })
			testDaemonThreadCursor(s)
			w.open("phone", false, protocol.CapabilityThread)
			testBoundAction(t, w, typ, 2, 1)
			testBoundAction(t, w, typ, 1, 2)
			if len(q.Snapshot(testConvID)) != 2 {
				t.Fatal("shared message_id crossed item identity")
			}
			testBoundAction(t, w, typ, 2, 2)
			remaining := q.Snapshot(testConvID)
			if len(remaining) != 1 || remaining[0].DeviceID != "device-one" {
				t.Fatal("wrong device entry acted")
			}
			testBoundAction(t, w, typ, 1, 1)
			if len(q.Snapshot(testConvID)) != 0 {
				t.Fatal("own entry refused")
			}
		})
	}
}
