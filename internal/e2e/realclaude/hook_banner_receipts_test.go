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
	mutatePayload := func(e *protocol.Envelope, key string, value any) {
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		p[key] = value
		e.Payload, _ = json.Marshal(p)
	}
	cases := []struct {
		name   string
		change func([]protocol.Envelope, []history.Entry) ([]protocol.Envelope, []history.Entry)
		valid  bool
	}{
		{"canonical queued delivery without receipts", nil, true},
		{"queued acceptance receipt", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			extra := f[2]
			extra.HistoryEntryID = &e[0].ID
			id := uint64(999)
			extra.EventID = &id
			return append(f, extra), e
		}, false},
		{"queued delivery receipt", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			extra := f[2]
			extra.HistoryEntryID = &e[2].ID
			id := uint64(999)
			extra.EventID = &id
			return append(f, extra), e
		}, false},
		{"duplicate", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			return append(f, f[0]), e
		}, false},
		{"missing", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			return append(f[:1], f[2:]...), e
		}, false},
		{"unexpected", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			mutatePayload(&f[2], "text", "unexpected")
			return f, e
		}, false},
		{"missing history ID", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			f[0].HistoryEntryID = nil
			return f, e
		}, false},
		{"zero replay ID", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			f[0].EventID = new(uint64)
			return f, e
		}, false},
		{"wrong timestamp", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			f[0].TS = f[0].TS.Add(time.Second)
			return f, e
		}, false},
		{"wrong raw fact", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			e[3].Type = "send_dropped"
			return f, e
		}, false},
		{"wrong delivery link", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			env := protocol.Envelope{Payload: e[2].Payload}
			mutatePayload(&env, "accepted_entry_id", 15)
			e[2].Payload = env.Payload
			return f, e
		}, false},
		{"unobserved acceptance with reused message ID", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			accepted := e[0]
			accepted.ID = 10
			e = append(e, accepted)
			env := protocol.Envelope{Payload: e[2].Payload}
			mutatePayload(&env, "accepted_entry_id", 10)
			e[2].Payload = env.Payload
			return f, e
		}, false},
		{"opening before ack", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			f[1], f[2] = f[2], f[1]
			return f, e
		}, false},
		{"opening after text", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			f[2], f[3] = f[3], f[2]
			return f, e
		}, false},
		{"refused text before refusal", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			return append([]protocol.Envelope{f[3]}, f...), e
		}, false},
		{"refused lifecycle", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			return append([]protocol.Envelope{f[4]}, f...), e
		}, false},
		{"missing refusal", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			mutatePayload(&f[0], "text", "")
			return f, e
		}, false},
		{"truncated refusal", func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
			mutatePayload(&f[0], "truncated", true)
			return f, e
		}, false},
	}
	for _, key := range []string{"conversation_id", "level", "text", "truncated", "stops_turn"} {
		for _, missing := range []bool{false, true} {
			cases = append(cases, struct {
				name   string
				change func([]protocol.Envelope, []history.Entry) ([]protocol.Envelope, []history.Entry)
				valid  bool
			}{name: key + map[bool]string{false: " null", true: " absent"}[missing], change: func(f []protocol.Envelope, e []history.Entry) ([]protocol.Envelope, []history.Entry) {
				var p map[string]json.RawMessage
				_ = json.Unmarshal(f[2].Payload, &p)
				if missing {
					delete(p, key)
				} else {
					p[key] = json.RawMessage("null")
				}
				f[2].Payload, _ = json.Marshal(p)
				return f, e
			}})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames, entries := testHookBannerWindow(t)
			if tc.change != nil {
				frames, entries = tc.change(frames, entries)
			}
			err := hookBannerCheckWindow("conv", 9, entries, frames)
			if (err == nil) != tc.valid {
				t.Fatalf("validation error = %v, want valid=%v", err, tc.valid)
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
	for i := range entries {
		entries[i].TS = at
	}
	var frames []protocol.Envelope
	for _, i := range []int{3, 7} {
		id, eventID := entries[i].ID, uint64(100+i)
		p := raw(protocol.BannerPayload{ConversationID: "conv", Level: "info"})
		if i == 3 {
			p = entries[i].Payload
		}
		frames = append(frames, protocol.Envelope{Type: protocol.TypeBanner, Payload: p, TS: at, HistoryEntryID: &id, EventID: &eventID})
	}
	reqID := uint64(9)
	frames = append(frames[:1], append([]protocol.Envelope{{Type: protocol.TypeAck, InReplyTo: &reqID}}, frames[1:]...)...)
	frames = append(frames,
		protocol.Envelope{Type: protocol.TypeAssistantDelta, Payload: raw(protocol.AssistantDeltaPayload{ConversationID: "conv", TurnID: "turn", Text: "reply"})},
		protocol.Envelope{Type: protocol.TypeTurnState, Payload: raw(protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})})
	return frames, entries
}
