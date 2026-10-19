package scenario

import (
	"context"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOrdinaryQueueControls(t *testing.T) {
	for _, name := range []string{"valid", "foreign-queue-valid", "live-no-checkpoint-valid", "no-bash", "foreign-queue", "zero-queue", "wrong-text", "wrong-push-id", "sent-now", "early-push", "duplicate-push", "early-reply", "no-reply", "late-queue", "ended-before-queue", "missing-second-end"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOrdinaryQueueControlHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_ORDINARY_QUEUE_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			valid := name == "valid" || strings.HasSuffix(name, "-valid")
			if ctx.Err() != nil || (err == nil) != valid {
				t.Fatalf("ordinary queue acceptance err=%v output=%s", err, out)
			}
		})
	}
}
func TestOrdinaryQueueControlHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_ORDINARY_QUEUE_CONTROL")
	if name == "" {
		return
	}
	frame := func(kind string, p any) protocol.Envelope {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	var rows []protocol.Envelope
	sequence := uint64(1)
	checkpointed := 0
	observed := false
	queue := func(owner, text string, id uint64) protocol.Envelope {
		return frame(protocol.TypeQueueState, protocol.QueueStatePayload{ConversationID: owner, Queued: []protocol.QueuedItem{{QueuedMsgID: id, Text: text}}})
	}
	end := frame(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: "conv", StopReason: "end_turn"})
	w := Wire{Next: func(deadline time.Time) protocol.Envelope {
		if len(rows) > 0 {
			env := rows[0]
			rows = rows[1:]
			if env.Type == protocol.TypeQueueState {
				var p protocol.QueueStatePayload
				_ = json.Unmarshal(env.Payload, &p)
				for _, item := range p.Queued {
					if p.ConversationID == "conv" && item.Text == OrdinaryQueuedText && item.QueuedMsgID != 0 {
						observed = true
					}
				}
			}
			return env
		}
		if remaining := time.Until(deadline); remaining > 0 {
			time.Sleep(remaining)
		}
		return protocol.Envelope{}
	}, Send: func(kind string, p any) uint64 {
		sequence++
		message, ok := p.(protocol.SendMessagePayload)
		if !ok || kind != protocol.TypeSendMessage || message.ConversationID != "conv" {
			t.Fatal("ordinary queue action changed")
		}
		switch sequence {
		case 2:
			if message.MessageID != "ordinary-start" || message.Text != "trigger" {
				t.Fatal("ordinary trigger changed")
			}
			if name == "no-bash" {
				rows = append(rows, end)
			} else {
				rows = append(rows, frame(protocol.TypeToolUse, protocol.ToolUsePayload{ConversationID: "conv", Name: "Bash", ToolUseID: "use"}))
			}
		case 3:
			if message.MessageID != "ordinary-queued" || message.Text != OrdinaryQueuedText {
				t.Fatal("queued input changed")
			}
			owner, text, id := "conv", OrdinaryQueuedText, uint64(9)
			if name == "foreign-queue" {
				owner = "other"
			}
			if name == "wrong-text" {
				text = "other"
			}
			if name == "zero-queue" {
				id = 0
			}
			toolResult := frame(protocol.TypeToolResult, protocol.ToolResultPayload{ConversationID: "conv", ToolUseID: "use"})
			if name == "late-queue" {
				rows = append(rows, toolResult)
			}
			if name == "ended-before-queue" {
				rows = append(rows, end)
			}
			if name == "foreign-queue-valid" {
				rows = append(rows, queue("other", text, id))
			}
			rows = append(rows, queue(owner, text, id))
			push := protocol.MessagePayload{ConversationID: "conv", Role: "user", Text: OrdinaryQueuedText, QueuedMsgID: 9}
			if name == "wrong-push-id" {
				push.QueuedMsgID = 10
			}
			if name == "sent-now" {
				push.SentNow = true
			}
			user := frame(protocol.TypeMessage, push)
			reply := frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "responding"})
			if name == "early-push" {
				rows = append(rows, user)
			}
			rows = append(rows, toolResult, end)
			if name == "early-reply" {
				rows = append(rows, reply)
			}
			if name != "early-push" {
				rows = append(rows, user)
			}
			if name == "duplicate-push" {
				rows = append(rows, user)
			}
			if name != "no-reply" && name != "early-reply" {
				rows = append(rows, reply)
			}
			if name != "missing-second-end" {
				rows = append(rows, end)
			}
		default:
			t.Fatal("unexpected ordinary queue action")
		}
		return sequence
	}}
	c := OrdinaryQueueConfig{ConversationID: "conv", Prompt: "trigger", Budget: 40 * time.Millisecond, Checkpoint: func(id uint64) {
		if id != 9 || !observed || sequence != 3 {
			t.Fatal("queue checkpoint preceded owned observation")
		}
		checkpointed++
	}}
	if name == "live-no-checkpoint-valid" {
		c.Checkpoint = nil
	}
	OrdinaryQueuePlacement(t, w, c)
	if sequence != 3 || (c.Checkpoint != nil && checkpointed != 1) {
		t.Fatal("wrong queue action count")
	}
}
