package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestQueuedSendHandleValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"", true}, {"1", true}, {"18446744073709551615", true}, {"0", false}, {"null", false},
		{"-1", false}, {"1.5", false}, {"1.0", false}, {"1e0", false}, {`"7"`, false}, {"true", false}, {"{}", false}, {"18446744073709551616", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			field := ""
			if tc.value != "" {
				field = `,"queued_msg_id":` + tc.value
			}
			raw := json.RawMessage(`{"conversation_id":"` + testConvID + `","accepted_at":"2026-01-01T00:00:00Z"` + field + `}`)
			if validQueuedSendFact(testConvID, historySendAccepted, raw) != tc.valid {
				t.Fatalf("validation: %s", raw)
			}
		})
	}
	h := testSendHistory(history.New(t.TempDir()))
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 0, Text: "invalid", TS: time.Now()})
	if len(testAllSendEntries(t, h.store)) != 0 || len(h.refs) != 0 {
		t.Fatal("zero ID recorded")
	}
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: ^uint64(0), Text: "valid", TS: time.Now()})
	entries := testAllSendEntries(t, h.store)
	if len(entries) != 1 || !strings.Contains(string(entries[0].Payload), `"queued_msg_id":18446744073709551615`) {
		t.Fatal("producer lost uint64", entries)
	}
}

func TestQueuedSendHandleCaseVariants(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field string
		valid bool
	}{
		{``, true},
		{`,"QUEUED_MSG_ID":7`, true},
		{`,"queued_msg_id":7,"QUEUED_MSG_ID":7`, true},
		{`,"QUEUED_MSG_ID":7,"queued_msg_id":7`, true},
		{`,"queued_msg_id":7,"QUEUED_MSG_ID":8`, false},
		{`,"QUEUED_MSG_ID":8,"queued_msg_id":7`, false},
	} {
		t.Run(tc.field, func(t *testing.T) {
			raw := json.RawMessage(`{"conversation_id":"` + testConvID + `","accepted_at":"2026-01-01T00:00:00Z"` + tc.field + `}`)
			if validQueuedSendFact(testConvID, historySendAccepted, raw) != tc.valid {
				t.Fatalf("case variant validation: %s", raw)
			}
		})
	}
}

func testQueuedHandle(t *testing.T, item protocol.ThreadItem, id uint64, status string) uint64 {
	t.Helper()
	var content struct {
		ID uint64 `json:"queued_msg_id"`
	}
	if err := json.Unmarshal(item.Content, &content); err != nil {
		t.Fatal(err)
	}
	if item.ID != id || item.Status != status || item.Active != (status == "queued") || content.ID != 7 {
		t.Fatalf("queue identity: %+v %s", item, item.Content)
	}
	return content.ID
}

func TestQueuedSendHandleActions(t *testing.T) {
	for _, action := range []string{"drop", "send_now"} {
		t.Run(action, func(t *testing.T) {
			s, h, reg, store := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			for i := 0; i < 99; i++ {
				testDaemonThreadAppend(t, h, "unknown", `{}`)
			}
			sendHistory := testSendHistory(h)
			cfg := msgqueue.Config{Logger: discardLogger(), Deliver: func(context.Context, string, []byte) error { return nil }, SendNow: func(_ context.Context, c string, id uint64, _ []byte) error {
				if c != testConvID || id != 7 {
					t.Errorf("wrong queue target: %s %d", c, id)
				}
				return nil
			}}
			cfg.OnDelivered = operatorMessageHistory(h, func(operatorMessage) {}, nil, discardLogger(), sendHistory)
			q, err := msgqueue.New(msgqueue.Config{Logger: cfg.Logger, Deliver: cfg.Deliver, SendNow: cfg.SendNow, OnDelivered: cfg.OnDelivered, OnTerminal: func(c string, m msgqueue.QueuedMessage, o msgqueue.TerminalOutcome) {
				if m.ID == 7 {
					sendHistory.terminal(c, m, o)
				}
			}, OnAccepted: func(c string, m msgqueue.QueuedMessage) {
				if m.ID == 7 {
					sendHistory.accepted(c, m)
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 6; i++ {
				id := q.Enqueue(testConvID, "padding")
				q.Remove(testConvID, id)
			}
			if q.Enqueue(testConvID, "queued") != 7 {
				t.Fatal("queue ID")
			}
			testDaemonThreadLoad(t, store, 100)
			w, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) { c.QueueRemover = q; c.QueueSender = q })
			w.open("phone", false, protocol.CapabilityThread)
			cursor := &shadowConversation{}
			s.publisher.publish(t.Context(), testConvID, cursor)
			if err := mgr.FlushPushes(t.Context()); err != nil {
				t.Fatal(err)
			}
			_, env := w.read()
			var added protocol.ThreadItemAddedPayload
			if env.Type != protocol.TypeThreadItemAdded || json.Unmarshal(env.Payload, &added) != nil {
				t.Fatal("live acceptance", env)
			}
			testQueuedHandle(t, added.Item, 100, "queued")
			// No send acknowledgement is delivered; explicit catch-up recovers acceptance.
			testDaemonThreadRequest(t, w, "phone", 40, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 10})
			got, typ := testDaemonThreadReply(t, w, 40)
			if typ != protocol.TypeThreadItems || len(got.Items) != 1 {
				t.Fatal("catch-up", typ, got)
			}
			handle := testQueuedHandle(t, got.Items[0], 100, "queued")
			if action == "drop" {
				testDaemonThreadRequest(t, w, "phone", 41, protocol.TypeDequeueMessage, protocol.DequeueMessagePayload{ConversationID: testConvID, QueuedMsgID: handle})
			} else {
				testDaemonThreadRequest(t, w, "phone", 41, protocol.TypeSendQueuedNow, protocol.SendQueuedNowPayload{ConversationID: testConvID, QueuedMsgID: handle})
			}
			version := uint64(101)
			status := "dropped"
			if action == "send_now" {
				version = 102
				status = "delivered"
			}
			testDaemonThreadLoad(t, store, version)
			if len(q.Snapshot(testConvID)) != 0 {
				t.Fatal("queue target not consumed")
			}
			snap := store.Observe(testConvID)
			testQueuedHandle(t, daemonThreadItem(snap.Items[0]), 100, status)
			if action == "send_now" && snap.Items[0].Order != 101 {
				t.Fatal("delivery changed placement")
			}
			changes, err := store.Changes(t.Context(), testConvID, cursor.threadEpoch, cursor.threadVersion)
			if err != nil {
				t.Fatal(err)
			}
			s.publisher.publish(t.Context(), testConvID, cursor)
			if err := mgr.FlushPushes(t.Context()); err != nil {
				t.Fatal(err)
			}
			peer := "phone"
			if changes.BaselineRequired {
				closed := w.await()
				if action != "send_now" || closed.ConnID != "phone" || closed.CloseCode != uint16(relay.StatusQueueOverflow) {
					t.Fatal("unexpected retirement", closed)
				}
				w.open("again", false, protocol.CapabilityThread)
				peer = "again"
				testDaemonThreadRequest(t, w, peer, 43, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 10})
				recovered, typ := testDaemonThreadReply(t, w, 43, peer)
				if typ != protocol.TypeThreadItems || len(recovered.Items) != 1 {
					t.Fatal("retirement catch-up", recovered, typ)
				}
				testQueuedHandle(t, recovered.Items[0], 100, status)
			} else {
				_, env = w.read()
				var changed protocol.ThreadItemChangedPayload
				if env.Type != protocol.TypeThreadItemChanged || json.Unmarshal(env.Payload, &changed) != nil || changed.ItemID != 100 {
					t.Fatal("live terminal revision", env)
				}
				if string(changed.Changes["active"]) != "false" || string(changed.Changes["status"]) != `"`+status+`"` {
					t.Fatal("terminal action remained active", changed)
				}
				if content, present := changed.Changes["content"]; present {
					added.Item.Content = content
				}
				added.Item.Status, added.Item.Active = status, false
				testQueuedHandle(t, added.Item, 100, status)
			}
			if action == "send_now" {
				testDaemonThreadRequest(t, w, peer, 42, protocol.TypeThreadPage, protocol.ThreadPagePayload{ConversationID: testConvID, BeforeOrder: 103, Limit: 10})
				page, typ := testDaemonThreadReply(t, w, 42, peer)
				if typ != protocol.TypeThreadItems || len(page.Items) != 1 {
					t.Fatal("page", typ, page)
				}
				testQueuedHandle(t, page.Items[0], 100, status)
			}
		})
	}
}

func TestQueuedSendHandleQueries(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	sendHistory := testSendHistory(h)
	sendHistory.accepted(testConvID, msgqueue.QueuedMessage{ID: 7, Text: "before restart", TS: time.Now()})
	testDaemonThreadLoad(t, store, 1)
	if err := store.Shutdown(); err != nil {
		t.Fatal(err)
	}
	dir, err := h.LogDir(testConvID)
	if err != nil {
		t.Fatal(err)
	}
	h = history.New(filepath.Dir(filepath.Dir(filepath.Dir(dir))))
	s.history = h
	pending, err := readStartupSends(h, testConvID)
	if err != nil {
		t.Fatal(err)
	}
	closeStartupSends(h, discardLogger(), testConvID, pending, time.Now())
	sendHistory = testSendHistory(h)
	sendHistory.accepted(testConvID, msgqueue.QueuedMessage{ID: 7, Text: "reused", TS: time.Now()})
	next := thread.NewStore(h)
	s.store = next
	testDaemonThreadLoad(t, next, 3)
	w, _ := testDaemonThreadQueryWire(t, s, nil)
	w.open("phone", false, protocol.CapabilityThread)
	testDaemonThreadRequest(t, w, "phone", 50, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: next.Observe(testConvID).Epoch, Version: 1, Limit: 10})
	got, typ := testDaemonThreadReply(t, w, 50)
	if typ != protocol.TypeThreadItems {
		t.Fatal(typ)
	}
	// Catch-up from the missed terminal revision recovers loss and the replacement.
	testQueuedHandle(t, daemonThreadItem(next.Snapshot(testConvID).Items[0]), 1, "lost")
	found, lost := false, false
	for _, item := range got.Items {
		if item.ID == 1 {
			testQueuedHandle(t, item, 1, "lost")
			lost = true
		}
		if item.ID == 3 {
			testQueuedHandle(t, item, 3, "queued")
			found = true
		}
	}
	if !found || !lost {
		t.Fatal("reused acceptance absent:", got)
	}
}
