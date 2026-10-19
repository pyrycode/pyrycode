package scenario

import (
	"encoding/json"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"testing"
	"time"
)

type InterruptConfig struct {
	ConversationID, Prompt, HealthPrompt string
	InitialThinking                      bool
	Budget                               time.Duration
	Checkpoint                           func(string)
}

func InitialThinkingPrompt(run string) string {
	return fmt.Sprintf("Do not use tools. Carefully prove or disprove this claim, checking the base case, induction step, and at least two possible counterexamples before writing any final answer: every positive integer can be written as a sum of distinct Fibonacci numbers with no consecutive Fibonacci numbers used. Take time to reason through all cases. run=%s", run)
}

// InterruptTurn preserves the original thinking/running milestone and first
// attributed cancelled end. Real provider cancellation and Bash effects stay live.
func InterruptTurn(t *testing.T, w Wire, c InterruptConfig) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "m-2", Text: c.Prompt})
	deadline := time.Now().Add(c.Budget)
	started := false
	for !started && time.Now().Before(deadline) {
		env := w.Next(deadline)
		if env.Type == protocol.TypeTurnState {
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid interrupt start state")
			}
			if p.ConversationID != c.ConversationID {
				continue
			}
			if c.InitialThinking {
				if p.State != "thinking" {
					t.Fatal("initial thinking was preceded by another state")
				}
				started = true
			} else {
				started = p.State == "responding"
			}
		} else if c.InitialThinking {
			switch env.Type {
			case protocol.TypeThinkingProgress, protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeTurnEnd:
				var p struct {
					ConversationID string `json:"conversation_id"`
				}
				if json.Unmarshal(env.Payload, &p) != nil {
					t.Fatal("invalid initial thinking frame")
				}
				if p.ConversationID == c.ConversationID {
					t.Fatal("initial thinking was preceded by progress, text, tool or terminal frame")
				}
			}
		}
	}
	if !started {
		t.Fatal("interrupt start milestone missing")
	}
	if c.Checkpoint != nil {
		phase := "responding"
		if c.InitialThinking {
			phase = "thinking"
		}
		c.Checkpoint(phase)
	}
	if c.InitialThinking {
		w.Send(protocol.TypeInterrupt, protocol.InterruptPayload{ConversationID: c.ConversationID})
	} else {
		w.Send(protocol.TypeInterrupt, nil)
	}
	WaitCancelledTurnEnd(t, w.Next, c.ConversationID, c.Budget, c.InitialThinking)
	if !c.InitialThinking {
		w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "m-4", Text: c.HealthPrompt})
		CompletedTurn(t, w.Next, c.ConversationID, c.Budget, false)
	}
}

func WaitCancelledTurnEnd(t *testing.T, next Next, convID string, budget time.Duration, initialThinking bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		env := next(deadline)
		if env.Type == protocol.TypeUnrecognizedMessage {
			t.Fatal("parser gap in interrupt window")
		}
		if initialThinking {
			switch env.Type {
			case protocol.TypeTurnState:
				var p protocol.TurnStatePayload
				if json.Unmarshal(env.Payload, &p) != nil {
					t.Fatal("invalid thinking cancellation state")
				}
				if p.ConversationID == convID && p.State != "thinking" {
					t.Fatal("initial thinking changed before cancelled end")
				}
			case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult:
				var p struct {
					ConversationID string `json:"conversation_id"`
				}
				if json.Unmarshal(env.Payload, &p) != nil {
					t.Fatal("invalid thinking cancellation frame")
				}
				if p.ConversationID == convID {
					t.Fatal("initial thinking produced text or tools before cancelled end")
				}
			}
		}
		if env.Type == protocol.TypeTurnEnd {
			var p protocol.TurnEndPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid interrupt terminal frame")
			}
			if p.ConversationID != convID {
				continue
			}
			if p.StopReason != "cancelled" {
				t.Fatal("interrupted turn ended without cancellation")
			}
			return
		}
	}
	t.Fatal("cancelled turn end missing")
}
