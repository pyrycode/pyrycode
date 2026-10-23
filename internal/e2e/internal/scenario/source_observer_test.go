package scenario

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestSourceObserverTerminalOrdering(t *testing.T) {
	now := time.Now().UTC()
	raw := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	entries := []history.Entry{{ID: 1, Type: protocol.TypeAssistantDelta, TS: now, Session: &history.SessionProvenance{Kind: "claude", SessionID: "new"}, Payload: raw(protocol.AssistantDeltaPayload{ConversationID: "conv", TurnID: "same", Text: "fresh"})}, {ID: 2, Type: protocol.TypeTurnEnd, TS: now, Session: &history.SessionProvenance{Kind: "claude", SessionID: "new"}, Payload: raw(protocol.TurnEndPayload{ConversationID: "conv", TurnID: "same", StopReason: "cancelled"})}}
	var kinds []string
	o := NewSourceObserver(t, t.TempDir())
	o.snapshot = func(string) []history.Entry { return nil }
	o.Watch("conv")
	o.snapshot = func(string) []history.Entry { return entries }
	o.OnEntry = func(e history.Entry) { kinds = append(kinds, e.Type) }
	for i, ts := range []time.Time{now.Add(-time.Second), now} {
		next := func(time.Time) protocol.Envelope {
			return protocol.Envelope{Type: protocol.TypeTurnState, TS: ts, Payload: raw(protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})}
		}
		o.Next(next, time.Now().Add(time.Second))
		if len(kinds) != i+1 {
			t.Fatalf("source observations=%v", kinds)
		}
	}
	if kinds[0] != protocol.TypeAssistantDelta || kinds[1] != protocol.TypeTurnEnd {
		t.Fatal(kinds)
	}
}

func TestSourceObserverQueuedPredecessorIdle(t *testing.T) {
	start := time.Now().UTC()
	raw := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	owner := &history.SessionProvenance{Kind: "claude", SessionID: "same-session"}
	entries := []history.Entry{
		{ID: 1, Type: "send_accepted", TS: start, Payload: raw(map[string]any{"conversation_id": "conv"})},
		{ID: 2, Type: "main_turn_opened", TS: start.Add(2 * time.Second), Session: owner, Payload: raw(map[string]any{"conversation_id": "conv", "turn_id": "successor", "occurred_at": start.Add(2 * time.Second)})},
		{ID: 3, Type: protocol.TypeAssistantDelta, TS: start.Add(4 * time.Second), Session: owner, Payload: raw(protocol.AssistantDeltaPayload{ConversationID: "conv", TurnID: "successor", Text: "fresh"})},
		{ID: 4, Type: protocol.TypeTurnEnd, TS: start.Add(5 * time.Second), Session: owner, Payload: raw(protocol.TurnEndPayload{ConversationID: "conv", TurnID: "successor"})},
	}
	o := NewSourceObserver(t, t.TempDir())
	o.snapshot = func(string) []history.Entry { return nil }
	o.Watch("conv")
	o.snapshot = func(string) []history.Entry { return entries }
	ends := 0
	o.OnEntry = func(e history.Entry) {
		if e.Type == protocol.TypeTurnEnd {
			ends++
		}
	}
	// The predecessor finishes after acceptance, and is read after successor
	// source completion. No successor terminal state is supplied.
	env := protocol.Envelope{Type: protocol.TypeTurnState, TS: start.Add(time.Second), SessionID: raw(owner.SessionID), Payload: raw(protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})}
	for range 2 {
		o.Next(func(time.Time) protocol.Envelope { return env }, time.Now().Add(time.Second))
	}
	if o.Ended("conv") || ends != 0 {
		t.Fatal("predecessor idle certified missing successor delivery")
	}
	// A matching terminal can precede the independently appended source content.
	env.TS = start.Add(3 * time.Second)
	o.Next(func(time.Time) protocol.Envelope { return env }, time.Now().Add(time.Second))
	if !o.Ended("conv") || ends != 1 {
		t.Fatal("owned terminal did not complete successor")
	}
}
