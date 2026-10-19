package scenario

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"testing"
	"time"
)

const OrdinaryQueuedText = "Reply with exactly ORDINARY-QUEUE-PLACEMENT-2820."

type OrdinaryQueueConfig struct {
	ConversationID, Prompt string
	Budget                 time.Duration
	Checkpoint             func(uint64)
}

func OrdinaryQueuePlacement(t *testing.T, w Wire, c OrdinaryQueueConfig) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "ordinary-start", Text: c.Prompt})
	deadline := time.Now().Add(c.Budget)
	var bashID string
	for {
		env := w.Next(deadline)
		if env.Type == "" {
			t.Fatal("no running Bash turn")
		}
		f := ClassifyQueuePlacementFrame(t, env, c.ConversationID)
		if f.Kind == "turn_end" {
			t.Fatal("turn ended before Bash started")
		}
		if f.Kind == "tool_use" && f.ToolName == "Bash" {
			bashID = f.ToolUseID
			break
		}
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "ordinary-queued", Text: OrdinaryQueuedText})
	var queuedID uint64
	var ends, users int
	var replyStarted, checkpointed, bashFinished bool
	deadline = time.Now().Add(2 * c.Budget)
	for ends < 2 {
		env := w.Next(deadline)
		if env.Type == "" {
			t.Fatal("queued reply did not end")
		}
		f := ClassifyQueuePlacementFrame(t, env, c.ConversationID)
		if f.Kind == "tool_result" && f.ToolUseID == bashID {
			bashFinished = true
		}
		if f.Kind == "queue_state" {
			for _, item := range f.Queued {
				if item.Text == OrdinaryQueuedText {
					queuedID = item.QueuedMsgID
				}
			}
			if c.Checkpoint != nil && !checkpointed && queuedID != 0 {
				if ends != 0 || bashFinished {
					t.Fatal("queue observation followed the held turn's completion")
				}
				c.Checkpoint(queuedID)
				checkpointed = true
			}
		}
		if f.Kind == "turn_end" {
			ends++
			continue
		}
		if f.Kind == "user_message" && f.Text == OrdinaryQueuedText {
			users++
			var p protocol.MessagePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid ordinary user push")
			}
			if ends != 1 || replyStarted || p.QueuedMsgID == 0 || p.QueuedMsgID != queuedID || p.SentNow {
				t.Fatal("ordinary queued message misplaced")
			}
		}
		if ends == 1 {
			interactive := f.Kind == "delta" || f.Kind == "tool_use" || f.Kind == "tool_result"
			if env.Type == protocol.TypeTurnState {
				var p protocol.TurnStatePayload
				if json.Unmarshal(env.Payload, &p) != nil {
					t.Fatal("invalid ordinary reply state")
				}
				interactive = p.ConversationID == c.ConversationID && p.State != "idle"
			}
			if interactive {
				replyStarted = true
				if users != 1 {
					t.Fatal("reply frame preceded ordinary user message")
				}
			}
		}
	}
	if users != 1 || !replyStarted || (c.Checkpoint != nil && !checkpointed) {
		t.Fatal("ordinary queue lacked one user push, reply start or action checkpoint")
	}
}
