//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestHookBannerRefusalPredicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    protocol.BannerPayload
		want bool
	}{
		{"receipt", protocol.BannerPayload{ConversationID: "conv", Level: "info"}, false},
		{"unrelated text", protocol.BannerPayload{ConversationID: "conv", Text: "notice"}, false},
		{"wrong conversation", protocol.BannerPayload{ConversationID: "other", Text: oslcapBlockReason}, false},
		{"refusal", protocol.BannerPayload{ConversationID: "conv", Text: oslcapBlockReason}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hookBannerIsRefusal(tc.p, "conv"); got != tc.want {
				t.Fatalf("refusal = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHookBannerReceiptWindow(t *testing.T) {
	for _, mode := range []string{"valid", "raw-banner", "raw-receipt", "missing-refusal", "missing-opening", "wrong-turn", "wrong-provenance", "phase-before-ack", "missing-ack", "wrong-delivery"} {
		t.Run(mode, func(t *testing.T) {
			frames, entries := testHookBannerWindow(t)
			switch mode {
			case "raw-banner", "raw-receipt":
				frames = append(frames, protocol.Envelope{Type: protocol.TypeBanner})
			case "missing-refusal":
				entries[3].Type = "unknown"
			case "missing-opening":
				entries[7].Type = "unknown"
			case "wrong-turn":
				entries[7].Payload = json.RawMessage(`{"conversation_id":"conv","turn_id":"wrong","occurred_at":"2026-10-11T01:00:00Z"}`)
			case "wrong-provenance":
				entries[8].Session = &history.SessionProvenance{Kind: "codex", SessionID: "other"}
			case "phase-before-ack":
				frames[0], frames[1] = frames[1], frames[0]
			case "missing-ack":
				frames = frames[1:]
			case "wrong-delivery":
				entries[2].Payload = json.RawMessage(`{"conversation_id":"conv","accepted_entry_id":15,"delivery_entry_id":12,"reason":"delivered"}`)
			}
			if err := hookBannerCheckWindow("conv", 9, entries, frames); (err == nil) != (mode == "valid") {
				t.Fatalf("window validation: %v", err)
			}
		})
	}
}

func testHookBannerWindow(t *testing.T) ([]protocol.Envelope, []history.Entry) {
	t.Helper()
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	raw := func(p any) json.RawMessage {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	fact := func(p map[string]any) json.RawMessage {
		p["conversation_id"] = "conv"
		return raw(p)
	}
	entries := []history.Entry{
		{ID: 11, Type: "send_accepted", Payload: fact(map[string]any{"message_id": "m-blocked", "accepted_at": at})},
		{ID: 12, Type: protocol.TypeMessage, Payload: raw(protocol.MessagePayload{ConversationID: "conv", MessageID: "m-blocked", Role: "user"})},
		{ID: 13, Type: "send_delivered", Payload: fact(map[string]any{"accepted_entry_id": 11, "delivery_entry_id": 12, "occurred_at": at, "reason": "delivered"})},
		{ID: 14, Type: protocol.TypeBanner, Payload: raw(protocol.BannerPayload{ConversationID: "conv", Text: oslcapBlockReason})},
		{ID: 15, Type: "send_accepted", Payload: fact(map[string]any{"message_id": "m-live", "accepted_at": at})},
		{ID: 16, Type: protocol.TypeMessage, Payload: raw(protocol.MessagePayload{ConversationID: "conv", MessageID: "m-live", Role: "user"})},
		{ID: 17, Type: "send_delivered", Payload: fact(map[string]any{"accepted_entry_id": 15, "delivery_entry_id": 16, "occurred_at": at, "reason": "delivered"})},
		{ID: 18, Type: "main_turn_opened", Payload: fact(map[string]any{"turn_id": "turn", "occurred_at": at})},
	}
	entries = append(entries, history.Entry{ID: 19, Type: protocol.TypeAssistantDelta, Payload: raw(protocol.AssistantDeltaPayload{ConversationID: "conv", TurnID: "turn", Text: "reply"})})
	for i := range entries {
		entries[i].TS = at
		entries[i].Session = &history.SessionProvenance{Kind: "claude", SessionID: "session"}
	}
	reqID := uint64(9)
	frames := []protocol.Envelope{
		{Type: protocol.TypeAck, TS: at, InReplyTo: &reqID},
		{Type: protocol.TypeTurnState, TS: at, Payload: raw(protocol.TurnStatePayload{ConversationID: "conv", State: "responding"})},
		{Type: protocol.TypeTurnState, TS: at, Payload: raw(protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})},
	}
	return frames, entries
}
