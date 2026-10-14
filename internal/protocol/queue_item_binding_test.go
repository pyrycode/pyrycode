package protocol

import (
	"encoding/json"
	"testing"
)

func TestQueueActionItemBindingWire(t *testing.T) {
	t.Parallel()
	for _, item := range []json.RawMessage{nil, json.RawMessage("205"), json.RawMessage("null")} {
		for _, payload := range []any{
			DequeueMessagePayload{ConversationID: "conv", QueuedMsgID: 7, AcceptedItemID: item},
			SendQueuedNowPayload{ConversationID: "conv", QueuedMsgID: 7, AcceptedItemID: item},
		} {
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			want := `{"conversation_id":"conv","queued_msg_id":7`
			if item != nil {
				want += `,"accepted_item_id":` + string(item)
			}
			want += `}`
			if string(raw) != want {
				t.Fatalf("wire: %s, want %s", raw, want)
			}
			var drop DequeueMessagePayload
			var send SendQueuedNowPayload
			if json.Unmarshal(raw, &drop) != nil || json.Unmarshal(raw, &send) != nil || string(drop.AcceptedItemID) != string(item) || string(send.AcceptedItemID) != string(item) {
				t.Fatal("binding presence changed on decode")
			}
		}
	}
}
