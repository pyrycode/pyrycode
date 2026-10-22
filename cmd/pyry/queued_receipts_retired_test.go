package main

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
)

func TestQueuedReceiptsRetired(t *testing.T) {
	store := history.New(t.TempDir())
	h := testSendHistory(store)
	for id := uint64(1); id <= 3; id++ {
		h.accepted(testConvID, msgqueue.QueuedMessage{ID: id, Text: "safe", TS: historyTS})
	}
	h.written(testConvID, msgqueue.QueuedMessage{ID: 1})
	operatorMessageHistory(store, nil, discardLogger(), h)(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "safe"})
	h.terminal(testConvID, msgqueue.QueuedMessage{ID: 2}, msgqueue.TerminalRemoved)
	accepted, err := readStartupSends(store, testConvID)
	if err != nil {
		t.Fatal(err)
	}
	closeStartupSends(store, discardLogger(), testConvID, accepted, historyTS)
	seen := make(map[string]bool)
	for _, entry := range historyEntries(t, store, testConvID) {
		if !queuedSendHistoryType(entry.Type) {
			continue
		}
		seen[entry.Type] = true
		if !validQueuedSendFact(testConvID, entry.Type, entry.Payload) {
			t.Fatalf("invalid retained source: %s", entry.Type)
		}
		if legacyHistoryType(entry.Type) {
			t.Errorf("queued source remains legacy eligible: %s", entry.Type)
		}
	}
	if len(seen) != 5 {
		raw, _ := json.Marshal(seen)
		t.Fatalf("missing source control: %s", raw)
	}
}

func TestQueuedSourceValidation(t *testing.T) {
	for _, typ := range []string{historySendAccepted, historySendWritten, historySendDelivered, historySendDropped, historySendLost} {
		t.Run(typ, func(t *testing.T) {
			reason := map[string]string{historySendWritten: "written", historySendDelivered: "delivered", historySendDropped: "removed", historySendLost: "daemon_restart"}[typ]
			p := queuedSendFact{ConversationID: testConvID, QueuedMessageID: 7, AcceptedAt: historyTS, OccurredAt: historyTS, AcceptedEntryID: 1, Reason: reason}
			if typ == historySendDelivered {
				p.DeliveryEntryID = 2
			}
			raw, _ := json.Marshal(p)
			if !validQueuedSendFact(testConvID, typ, raw) {
				t.Fatal("valid retained source rejected")
			}
			required := []string{"conversation_id", "queued_msg_id"}
			if typ == historySendAccepted {
				required = append(required, "accepted_at")
			} else {
				required = append(required, "accepted_entry_id", "occurred_at", "reason")
			}
			if typ == historySendDelivered {
				required = append(required, "delivery_entry_id")
			}
			for _, field := range required {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(raw, &fields)
				fields[field] = json.RawMessage("null")
				bad, _ := json.Marshal(fields)
				if validQueuedSendFact(testConvID, typ, bad) {
					t.Fatalf("invalid source accepted: %s", field)
				}
			}
		})
	}
}
