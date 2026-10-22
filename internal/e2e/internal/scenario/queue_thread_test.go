package scenario

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"testing"
)

func queueThreadFrame(t *testing.T, conv, text string, id uint64, status string) protocol.Envelope {
	t.Helper()
	content, err := json.Marshal(map[string]any{"text": text, "queued_msg_id": id})
	if err != nil {
		t.Fatal(err)
	}
	item := protocol.ThreadItem{ID: 70, Rev: 70, Kind: "user_message", Status: status, Active: status == "queued", Shown: true, Content: content}
	if status == "delivered" {
		item.Order = 80
		item.Rev = 81
	}
	raw, err := json.Marshal(protocol.ThreadItemAddedPayload{ConversationID: conv, Epoch: "epoch", Version: item.Rev, Item: item})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Envelope{Type: protocol.TypeThreadItemAdded, Payload: raw}
}
