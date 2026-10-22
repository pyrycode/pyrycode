package scenario

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestSourceTurnIdentity(t *testing.T) {
	entry := func(id uint64, session, kind, turn, parent, text string) history.Entry {
		p, err := json.Marshal(map[string]any{"conversation_id": "conv", "turn_id": turn, "parent_tool_use_id": parent, "text": text})
		if err != nil {
			t.Fatal(err)
		}
		return history.Entry{ID: id, Type: kind, Payload: p, Session: &history.SessionProvenance{Kind: "claude", SessionID: session}}
	}
	for _, tc := range []struct {
		name, endSession, endTurn, parent string
		complete                          bool
	}{
		{"owned", "new", "reused", "", true},
		{"old-session", "old", "reused", "", false},
		{"old-turn", "new", "old", "", false},
		{"child", "new", "reused", "child", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var turn sourceTurn
			turn.feed(t, "conv", []history.Entry{
				entry(4, "new", protocol.TypeAssistantDelta, "reused", "", "one"),
				entry(5, "new", protocol.TypeAssistantDelta, "child", "agent", "private child"),
				entry(6, tc.endSession, protocol.TypeTurnEnd, tc.endTurn, tc.parent, ""),
			})
			// Reading a snapshot twice cannot duplicate content.
			turn.feed(t, "conv", []history.Entry{entry(4, "new", protocol.TypeAssistantDelta, "reused", "", "one")})
			if turn.text.String() != "one" || turn.ended != tc.complete {
				t.Fatalf("text=%q ended=%t", turn.text.String(), turn.ended)
			}
		})
	}
}

func TestSourceWatermarkRejectsPredecessorIdle(t *testing.T) {
	const conv = "37340000-0000-4000-8000-000000000001"
	const other = "37340000-0000-4000-8000-000000000002"
	home := t.TempDir()
	store := history.New(filepath.Join(home, ".pyry", "test"))
	endTime := time.Now().UTC()
	appendTurn := func(id, session, text string) {
		t.Helper()
		for _, fact := range []struct {
			kind  string
			value any
		}{
			{protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: id, TurnID: "reused", Text: text}},
			{protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: id, TurnID: "reused", StopReason: "end_turn"}},
		} {
			raw, err := json.Marshal(fact.value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendWithMetadata(conversations.ConversationID(id), fact.kind, raw, endTime, history.Metadata{Session: &history.SessionProvenance{Kind: "claude", SessionID: session}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	appendTurn(conv, "predecessor", "old private reply")
	appendTurn(other, "successor", "other private reply")
	source := SourceSince(t, home, conv)
	reads := 0
	w := ObserveSource(t, home, Wire{
		Send: func(string, any) uint64 { appendTurn(conv, "successor", "fresh reply"); return 42 },
		Next: func(time.Time) protocol.Envelope {
			reads++
			ts := endTime
			if reads == 1 {
				ts = endTime.Add(-time.Second)
			}
			raw, err := json.Marshal(protocol.TurnStatePayload{ConversationID: conv, State: "idle"})
			if err != nil {
				t.Fatal(err)
			}
			return protocol.Envelope{Type: protocol.TypeTurnState, TS: ts, Payload: raw}
		},
	})
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: conv, MessageID: "successor"})
	text := CompletedTurn(t, w.Next, conv, time.Second, false, w.Source)
	if text != "fresh reply" || reads != 2 {
		t.Fatalf("text=%q wire reads=%d", text, reads)
	}
	for _, facts := range []SourceFacts{source, w.Source} {
		entries := facts(conv)
		if len(entries) != 2 || entries[0].ID != 3 || entries[0].Session.SessionID != "successor" {
			t.Fatal("source watermark mixed conversations or producing sessions")
		}
	}
}
