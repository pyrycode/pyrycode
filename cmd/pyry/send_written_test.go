package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
)

func TestSendNowWrittenBeforeEcho(t *testing.T) {
	store := history.New(t.TempDir())
	h := testSendHistory(store)
	m := msgqueue.QueuedMessage{ID: 7, Text: "safe", TS: time.Now(), SentNow: true}
	h.accepted(testConvID, m)
	h.terminal(testConvID, m, msgqueue.TerminalDelivered)
	h.terminal(testConvID, m, msgqueue.TerminalDelivered)
	all := testAllSendEntries(t, store)
	if len(all) != 2 || all[1].Type != "send_written" {
		t.Fatalf("successful stdin write was not recorded once: %v", all)
	}
	p := testSendFact(t, all[1])
	if p.AcceptedEntryID != all[0].ID || p.DeliveryEntryID != 0 || p.Reason != "written" {
		t.Fatal("write claimed final placement")
	}
	pending, err := readStartupSends(store, testConvID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("unconsumed write lost startup ownership: %v", err)
	}
	closeStartupSends(store, discardLogger(), testConvID, pending, time.Now())
	all = testAllSendEntries(t, store)
	if all[len(all)-1].Type != historySendLost {
		t.Fatal("restart did not mark pending input lost")
	}
}

func TestSendNowWrittenQueueOutcome(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "written", true: "failed"}[failed], func(t *testing.T) {
			store := history.New(t.TempDir())
			h := testSendHistory(store)
			q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil },
				SendNow: func(context.Context, string, uint64, []byte) error {
					if failed {
						return errors.New("write refused")
					}
					return nil
				},
				OnAccepted: h.accepted, OnTerminal: h.terminal})
			if err != nil {
				t.Fatal(err)
			}
			id := q.EnqueueDelivery(testConvID, "app", "safe", "host prompt")
			if q.SendNow(testConvID, id) == failed {
				t.Fatal("queue write verdict changed")
			}
			want := 2
			if failed {
				want = 1
			}
			if len(testAllSendEntries(t, store)) != want {
				t.Fatal("write fact did not follow the real queue verdict")
			}
			if (len(q.Snapshot(testConvID)) == 1) != failed {
				t.Fatal("failed write lost backlog ownership")
			}
		})
	}
}
func TestSendNowWrittenRacesFinalPlacement(t *testing.T) {
	for range 50 {
		store := history.New(t.TempDir())
		h := testSendHistory(store)
		m := msgqueue.QueuedMessage{ID: 7, Text: "safe", TS: time.Now(), SentNow: true}
		h.accepted(testConvID, m)
		ref := h.beforeDelivery(testConvID, m.ID)
		at := time.Now()
		id, err := store.Append(conversations.ConversationID(testConvID), "message", []byte(`{"role":"user","text":"safe"}`), at)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); h.terminal(testConvID, m, msgqueue.TerminalDelivered) }()
		}
		wg.Add(1)
		go func() { defer wg.Done(); h.delivered(testConvID, m.ID, ref, &id, at, history.SessionProvenance{}) }()
		wg.Wait()
		h.terminal(testConvID, m, msgqueue.TerminalDelivered)
		written, delivered := 0, 0
		var writeID, deliveryID uint64
		for _, e := range testAllSendEntries(t, store) {
			if e.Type == historySendWritten {
				written++
				writeID = e.ID
			}
			if e.Type == historySendDelivered {
				delivered++
				deliveryID = e.ID
			}
		}
		if written > 1 || delivered != 1 || (written == 1 && writeID >= deliveryID) {
			t.Fatal("racing facts duplicated or regressed placement")
		}
	}
}
