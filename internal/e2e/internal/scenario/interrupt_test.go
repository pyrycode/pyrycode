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

func TestInterruptControls(t *testing.T) {
	for _, name := range []string{"running-valid", "thinking-valid", "thinking-foreign-start-valid", "thinking-progress-after-start-valid", "running-no-start", "thinking-no-start", "thinking-wrong-state", "thinking-early-progress", "thinking-early-text", "thinking-early-tool", "thinking-early-end", "running-natural-end", "running-foreign-end", "running-parser-gap", "thinking-parser-gap", "thinking-changed-state", "thinking-text", "thinking-tool", "thinking-result", "running-health-no-text", "running-health-no-idle", "running-health-foreign", "running-health-gap"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptControlHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_INTERRUPT_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || (err == nil) != strings.HasSuffix(name, "-valid") {
				t.Fatalf("interrupt acceptance err=%v output=%s", err, out)
			}
		})
	}
}
func TestInterruptControlHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_INTERRUPT_CONTROL")
	if name == "" {
		return
	}
	thinking := strings.HasPrefix(name, "thinking-")
	frame := func(kind string, p any) protocol.Envelope {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	state := func(owner, value string) protocol.Envelope {
		return frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: owner, State: value})
	}
	end := func(owner, reason string) protocol.Envelope {
		return frame(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: owner, StopReason: reason})
	}
	delta := func(owner string) protocol.Envelope {
		return frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: owner, Text: "reply"})
	}
	var queue []protocol.Envelope
	sequence := uint64(1)
	checkpointed := 0
	w := Wire{Next: func(deadline time.Time) protocol.Envelope {
		if len(queue) > 0 {
			env := queue[0]
			queue = queue[1:]
			return env
		}
		if remaining := time.Until(deadline); remaining > 0 {
			time.Sleep(remaining)
		}
		return protocol.Envelope{}
	}, Send: func(kind string, p any) uint64 {
		sequence++
		switch sequence {
		case 2:
			message, ok := p.(protocol.SendMessagePayload)
			if kind != protocol.TypeSendMessage || !ok || message.ConversationID != "conv" || message.Text != "trigger" {
				t.Fatal("interrupt trigger changed")
			}
			phase := "responding"
			if thinking {
				phase = "thinking"
			}
			if strings.HasSuffix(name, "no-start") {
				break
			}
			if name == "thinking-foreign-start-valid" {
				queue = append(queue, state("other", "responding"))
			}
			switch name {
			case "thinking-wrong-state":
				queue = append(queue, state("conv", "responding"))
			case "thinking-early-progress":
				queue = append(queue, frame(protocol.TypeThinkingProgress, map[string]string{"conversation_id": "conv"}))
			case "thinking-early-text":
				queue = append(queue, delta("conv"))
			case "thinking-early-tool":
				queue = append(queue, frame(protocol.TypeToolUse, protocol.ToolUsePayload{ConversationID: "conv"}))
			case "thinking-early-end":
				queue = append(queue, end("conv", "end_turn"))
			}
			queue = append(queue, state("conv", phase))
		case 3:
			if checkpointed != 1 {
				t.Fatal("interrupt preceded validated phase checkpoint")
			}
			if kind != protocol.TypeInterrupt {
				t.Fatal("interrupt action missing")
			}
			if thinking {
				payload, ok := p.(protocol.InterruptPayload)
				if !ok || payload.ConversationID != "conv" {
					t.Fatal("thinking interrupt lost target")
				}
			} else if p != nil {
				t.Fatal("running interrupt stopped using active conversation")
			}
			switch name {
			case "running-natural-end":
				queue = append(queue, end("conv", "end_turn"))
			case "running-foreign-end":
				queue = append(queue, end("other", "cancelled"))
			case "running-parser-gap", "thinking-parser-gap":
				queue = append(queue, frame(protocol.TypeUnrecognizedMessage, protocol.UnrecognizedMessagePayload{}))
			case "thinking-changed-state":
				queue = append(queue, state("conv", "responding"))
			case "thinking-text":
				queue = append(queue, delta("conv"))
			case "thinking-tool":
				queue = append(queue, frame(protocol.TypeToolUse, protocol.ToolUsePayload{ConversationID: "conv"}))
			case "thinking-result":
				queue = append(queue, frame(protocol.TypeToolResult, protocol.ToolResultPayload{ConversationID: "conv"}))
			case "thinking-progress-after-start-valid":
				queue = append(queue, frame(protocol.TypeThinkingProgress, map[string]string{"conversation_id": "conv"}))
			}
			if name != "running-foreign-end" {
				queue = append(queue, end("conv", "cancelled"))
			}
		case 4:
			message, ok := p.(protocol.SendMessagePayload)
			if thinking || kind != protocol.TypeSendMessage || !ok || message.ConversationID != "conv" || message.Text != "health" {
				t.Fatal("post-interrupt health changed")
			}
			if name == "running-health-gap" {
				queue = append(queue, frame(protocol.TypeUnrecognizedMessage, protocol.UnrecognizedMessagePayload{}))
			}
			owner := "conv"
			if name == "running-health-foreign" {
				owner = "other"
			}
			if name != "running-health-no-text" {
				queue = append(queue, delta(owner))
			}
			if name != "running-health-no-idle" {
				queue = append(queue, state("conv", "idle"))
			}
		default:
			t.Fatal("unexpected additional interrupt action")
		}
		return sequence
	}}
	InterruptTurn(t, w, InterruptConfig{ConversationID: "conv", Prompt: "trigger", HealthPrompt: "health", InitialThinking: thinking, Budget: 40 * time.Millisecond, Checkpoint: func(phase string) {
		want := "responding"
		if thinking {
			want = "thinking"
		}
		if phase != want || sequence != 2 {
			t.Fatal("wrong observed interrupt phase")
		}
		checkpointed++
	}})
	if (thinking && sequence != 3) || (!thinking && sequence != 4) {
		t.Fatal("wrong action count")
	}
}
