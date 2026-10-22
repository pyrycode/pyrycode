package scenario

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestQuestionBoundaryIsolation(t *testing.T) {
	entry := func(id uint64, conv, session, turn, parent, text string) history.Entry {
		raw, err := json.Marshal(protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: turn, ParentToolUseID: parent, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return history.Entry{ID: id, Type: protocol.TypeAssistantDelta, Payload: raw, Session: &history.SessionProvenance{Kind: "claude", SessionID: session}}
	}
	entries := []history.Entry{entry(1, "conv", "session", "parked", "", "pre-answer witness")}
	source := func(string) []history.Entry { return entries }
	after, pre := QuestionBoundary(t, source, "conv")
	if pre != "pre-answer witness" {
		t.Fatal("pre-answer prose was not checked")
	}
	entries = append(entries,
		entry(2, "conv", "old-session", "parked", "", "foreign session witness"),
		entry(3, "foreign", "session", "parked", "", "foreign conversation witness"),
		entry(4, "conv", "session", "other-turn", "", "predecessor witness"),
		entry(5, "conv", "session", "parked", "child", "child witness"),
		entry(6, "conv", "session", "parked", "", "fresh answer"),
	)
	for range 2 {
		var turn sourceTurn
		turn.feed(t, "conv", after("conv"))
		if turn.text.String() != "fresh answer" {
			t.Fatal("continuation mixed a boundary or producing identity")
		}
	}
}
