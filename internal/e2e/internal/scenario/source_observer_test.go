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
