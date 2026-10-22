package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const NativeSuggestionClearPrompt = "Sounds good, thank you. Reply with a single short sentence."

// NativeSuggestionPrompts declares the bounded coding prefix used by live and recorded runs.
func NativeSuggestionPrompts(run string) []string {
	return []string{"Write a Go function that reverses a string. Reply in chat only, do not create files. Just the code, briefly. run=" + run,
		"Now add a unit test for it. Just the code.", "Now make it handle unicode correctly. Just the code.",
		"Now add a benchmark for it. Just the code.", "Now add a doc comment to the function. Just the code.", "Now add an example test for it. Just the code."}
}

type NativeSuggestionConfig struct {
	ConversationID     string
	Prompts            []string
	TurnBudget, Window time.Duration
	CheckNative        func()
}

func NativeSuggestions(t *testing.T, w Wire, c NativeSuggestionConfig) {
	t.Helper()
	sawDelta, idle := false, false
	turn := new(sourceTurn)
	var setRev, clearRev uint64
	observe := func(env protocol.Envelope) {
		turn.feed(t, c.ConversationID, sourceFacts(t, []SourceFacts{w.Source}, c.ConversationID))
		sawDelta = strings.TrimSpace(turn.text.String()) != ""
		switch env.Type {
		case protocol.TypeError:
			t.Fatal("native suggestion turn was refused")
		case protocol.TypeAssistantDelta:
			t.Fatal("retired suggestion content delivered on wire")
		case protocol.TypeTurnState:
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid suggestion turn state")
			}
			if turn.idle(t, env, c.ConversationID) && sawDelta {
				idle = true
			}
		case protocol.TypeReplySuggestion:
			var p struct {
				ConversationID string          `json:"conversation_id"`
				Revision       uint64          `json:"revision"`
				SuggestedReply json.RawMessage `json:"suggested_reply"`
			}
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid suggestion payload")
			}
			if p.ConversationID != c.ConversationID {
				return
			}
			if string(p.SuggestedReply) == "null" {
				if setRev != 0 && p.Revision > setRev {
					clearRev = p.Revision
				}
				return
			}
			var text string
			if json.Unmarshal(p.SuggestedReply, &text) != nil {
				t.Fatal("suggestion is neither explicit null nor a string")
			}
			if strings.TrimSpace(text) != "" && p.Revision > setRev {
				setRev = p.Revision
			}
		}
	}
	pump := func(budget time.Duration, done func() bool) bool {
		deadline := time.Now().Add(budget)
		for !done() && time.Now().Before(deadline) {
			env := w.Next(pollDeadline(deadline))
			observe(env)
		}
		return done()
	}
	for i, prompt := range c.Prompts {
		if setRev != 0 {
			break
		}
		sawDelta, idle = false, false
		turn = new(sourceTurn)
		w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: fmt.Sprintf("native-suggestion-%d", i+1), Text: prompt})
		if !pump(c.TurnBudget, func() bool { return idle }) {
			t.Fatal("native suggestion exchange did not complete")
		}
		pump(c.Window, func() bool { return setRev != 0 })
	}
	if setRev == 0 {
		t.Fatal("no native suggestion after completed exchanges")
	}
	c.CheckNative()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "suggestion-clear", Text: NativeSuggestionClearPrompt})
	if !pump(c.TurnBudget, func() bool { return clearRev != 0 }) {
		t.Fatal("native suggestion did not explicitly clear at a higher revision")
	}
}
