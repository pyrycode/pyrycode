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
		return queueThreadFrame(t, owner, text, id, "queued")
	}
	end := frame(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: "conv", StopReason: "end_turn"})
	w := Wire{Next: func(deadline time.Time) protocol.Envelope {
		if len(rows) > 0 {
			env := rows[0]
			rows = rows[1:]
			if env.Type == protocol.TypeThreadItemAdded {
				var p protocol.ThreadItemAddedPayload
				_ = json.Unmarshal(env.Payload, &p)
				var content struct {
					Text string `json:"text"`
					ID   uint64 `json:"queued_msg_id"`
				}
				_ = json.Unmarshal(p.Item.Content, &content)
				observed = p.ConversationID == "conv" && content.Text == OrdinaryQueuedText && content.ID != 0
			}
			return env
		}
		if remaining := time.Until(deadline); remaining > 0 {
			time.Sleep(remaining)
		}
		return protocol.Envelope{}
	}, Send: func(kind string, p any) uint64 {
		if kind == protocol.TypeThreadCatchUp {
			id := uint64(100)
			env := frame(protocol.TypeThreadItems, protocol.ThreadItemsPayload{ConversationID: "conv", Final: true})
			env.InReplyTo = &id
			rows = append([]protocol.Envelope{env}, rows...)
			return 100
		}
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
			user := queueThreadFrame(t, "conv", OrdinaryQueuedText, 0, "delivered")
			var push protocol.ThreadItemAddedPayload
			if err := json.Unmarshal(user.Payload, &push); err != nil {
				t.Fatal(err)
			}
			if name == "wrong-push-id" {
				push.Item.ID = 71
			}
			if name == "sent-now" {
				push.Item.Content = json.RawMessage(`{"text":"` + OrdinaryQueuedText + `","sent_now":true}`)
			}
			user = frame(protocol.TypeThreadItemAdded, push)
			reply := frame(protocol.TypeThreadItemAdded, protocol.ThreadItemAddedPayload{ConversationID: "conv", Item: protocol.ThreadItem{ID: 100, Rev: 100, Order: 100, Kind: "assistant_message", Status: "running", Active: true, Shown: true, Content: json.RawMessage(`{"text":"reply"}`)}})
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
				push.Item.ID = 71
				rows = append(rows, frame(protocol.TypeThreadItemAdded, push))
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
