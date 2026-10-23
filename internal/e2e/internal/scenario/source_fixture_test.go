package scenario

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// testSourceWire separates neutral payload fixtures from the wire under test.
// Only supported live frames reach Next; content fixtures become retained facts.
func testSourceWire(t *testing.T, w Wire, completeIdle ...bool) Wire {
	t.Helper()
	var entries []history.Entry
	var mark uint64
	turn := 1
	ended := false
	mainTurn := ""
	appendFact := func(env protocol.Envelope) {
		var p map[string]any
		if json.Unmarshal(env.Payload, &p) != nil {
			t.Fatal("invalid source fixture")
		}
		if p["turn_id"] == nil || p["turn_id"] == "" {
			p["turn_id"] = fmt.Sprint(turn)
			if env.Type == protocol.TypeTurnEnd && mainTurn != "" {
				p["turn_id"] = mainTurn
			}
		}
		if env.Type == protocol.TypeAssistantDelta && (p["parent_tool_use_id"] == nil || p["parent_tool_use_id"] == "") {
			mainTurn, _ = p["turn_id"].(string)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, history.Entry{ID: uint64(len(entries) + 1), Type: env.Type, Payload: raw, Session: &history.SessionProvenance{Kind: "claude", SessionID: "fixture"}})
	}
	send, next := w.Send, w.Next
	w.Send = func(kind string, value any) uint64 {
		if kind == protocol.TypeSendMessage {
			mark = uint64(len(entries))
			turn++
			ended = false
			mainTurn = ""
		}
		return send(kind, value)
	}
	w.Source = func(conv string) []history.Entry { return entries[mark:] }
	w.Next = func(deadline time.Time) protocol.Envelope {
		env := next(deadline)
		switch env.Type {
		case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied:
			appendFact(env)
			return protocol.Envelope{}
		case protocol.TypeTurnEnd:
			appendFact(env)
			ended = true
			var p protocol.TurnEndPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid terminal fixture")
			}
			raw, err := json.Marshal(protocol.TurnStatePayload{ConversationID: p.ConversationID, State: "idle"})
			if err != nil {
				t.Fatal(err)
			}
			return protocol.Envelope{Type: protocol.TypeTurnState, SessionID: json.RawMessage(`"fixture"`), Payload: raw}
		case protocol.TypeTurnState:
			// Neutral fixtures declare the same owner as their retained facts.
			// Preserve explicit foreign or invalid ownership for negative controls.
			if len(env.SessionID) == 0 {
				env.SessionID = json.RawMessage(`"fixture"`)
			}
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid live fixture")
			}
			if p.State == "idle" && !ended && (len(completeIdle) == 0 || completeIdle[0]) {
				raw, err := json.Marshal(protocol.TurnEndPayload{ConversationID: p.ConversationID, StopReason: "end_turn"})
				if err != nil {
					t.Fatal(err)
				}
				appendFact(protocol.Envelope{Type: protocol.TypeTurnEnd, Payload: raw})
				ended = true
			}
		}
		return env
	}
	return w
}
