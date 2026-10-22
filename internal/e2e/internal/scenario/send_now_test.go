package scenario

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestSendNowPlacementControls(t *testing.T) {
	for _, name := range []string{"valid", "delivery-enrichment", "foreign-frames", "subagent-frames", "distinct-item-cleared", "early-end", "result-before-queue", "zero-queue-id", "missing-result", "foreign-result", "user-before-result", "user-after-text", "duplicate-user", "wrong-user-text", "missing-marker", "foreign-end", "late-extra-end", "late-duplicate-user", "nonempty-queue", "distinct-queued-add", "distinct-queued-catch-up", "distinct-queued-patch", "unrecognized"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSendNowPlacementHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_SEND_NOW_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			valid := name == "valid" || name == "delivery-enrichment" || name == "foreign-frames" || name == "subagent-frames" || name == "distinct-item-cleared"
			if ctx.Err() != nil || (err == nil) != valid {
				t.Fatalf("placement evidence acceptance err=%v output=%s", err, out)
			}
		})
	}
}

func TestSendNowPlacementHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_SEND_NOW_CONTROL")
	if name == "" {
		return
	}
	frame := func(kind string, value any) protocol.Envelope {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	use := frame(protocol.TypeToolUse, protocol.ToolUsePayload{ConversationID: "conv", Name: "Bash", ToolUseID: "bash"})
	queued := queueThreadFrame(t, "conv", SendNowQueuedText, 7, "queued")
	result := frame(protocol.TypeToolResult, protocol.ToolResultPayload{ConversationID: "conv", ToolUseID: "bash"})
	user := queueThreadFrame(t, "conv", SendNowQueuedText, 0, "delivered")
	delta := frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: "conv", Text: SendNowMarker})
	end := frame(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: "conv"})
	empty := queueThreadFrame(t, "conv", SendNowQueuedText, 0, "delivered")
	frames := []protocol.Envelope{use, queued, result, user, delta, end, empty}
	var initialItems []protocol.ThreadItem
	var distinct protocol.ThreadItemAddedPayload
	if err := json.Unmarshal(queueThreadFrame(t, "conv", "another queued message", 8, "queued").Payload, &distinct); err != nil {
		t.Fatal(err)
	}
	distinct.Item.ID, distinct.Item.Rev = 71, 1
	switch name {
	case "distinct-queued-add":
		frames = append(frames, frame(protocol.TypeThreadItemAdded, distinct))
	case "distinct-queued-catch-up":
		initialItems = []protocol.ThreadItem{distinct.Item}
	case "distinct-queued-patch", "distinct-item-cleared":
		initial := distinct.Item
		status, active := "queued", true
		if name == "distinct-queued-patch" {
			initial.Status, initial.Active = "dropped", false
		} else {
			status, active = "dropped", false
		}
		initialItems = []protocol.ThreadItem{initial}
		frames = append(frames, frame(protocol.TypeThreadItemChanged, protocol.ThreadItemChangedPayload{
			ConversationID: "conv", Epoch: "epoch", ItemID: 71, BaseRev: 1, Rev: 2,
			Changes: map[string]json.RawMessage{"status": mustQueueJSON(t, status), "active": mustQueueJSON(t, active)},
		}))
	case "delivery-enrichment":
		var p protocol.ThreadItemAddedPayload
		if err := json.Unmarshal(empty.Payload, &p); err != nil {
			t.Fatal(err)
		}
		p.Item.Rev = 90
		frames[6] = frame(protocol.TypeThreadItemAdded, p)
	case "early-end":
		frames[0] = end
	case "result-before-queue":
		frames = []protocol.Envelope{use, result, queued, user, delta, end, empty}
	case "zero-queue-id":
		frames[1] = queueThreadFrame(t, "conv", SendNowQueuedText, 0, "queued")
	case "missing-result":
		frames = []protocol.Envelope{use, queued, user, delta, end, empty}
	case "foreign-result":
		frames[2] = frame(protocol.TypeToolResult, protocol.ToolResultPayload{ConversationID: "other", ToolUseID: "bash"})
	case "user-before-result":
		frames[2], frames[3] = user, result
	case "user-after-text":
		frames[3], frames[4] = delta, user
	case "duplicate-user":
		frames = []protocol.Envelope{use, queued, result, user, frame(protocol.TypeMessage, protocol.MessagePayload{ConversationID: "conv", Role: "user", Text: SendNowQueuedText}), delta, end, empty}
	case "wrong-user-text":
		frames[3] = queueThreadFrame(t, "conv", SendNowQueuedText+" altered", 0, "delivered")
	case "missing-marker":
		frames[4] = frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: "conv", Text: "unrelated reply"})
	case "foreign-end":
		frames[5] = frame(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: "other"})
	case "late-extra-end":
		frames = append(frames, end)
	case "late-duplicate-user":
		frames = append(frames, frame(protocol.TypeMessage, protocol.MessagePayload{ConversationID: "conv", Role: "user", Text: SendNowQueuedText}))
	case "nonempty-queue":
		var p protocol.ThreadItemAddedPayload
		if err := json.Unmarshal(queued.Payload, &p); err != nil {
			t.Fatal(err)
		}
		p.Item.Rev = 90
		frames[6] = frame(protocol.TypeThreadItemAdded, p)
	case "unrecognized":
		frames = append(frames, frame(protocol.TypeUnrecognizedMessage, struct{}{}))
	case "foreign-frames":
		frames = append([]protocol.Envelope{frame(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: "other"})}, frames...)
	case "subagent-frames":
		frames = append(frames, frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: "conv", ParentToolUseID: "agent", Text: "not main text"}))
	}
	sends := 0
	wire := Wire{Send: func(kind string, payload any) uint64 {
		if kind == protocol.TypeThreadCatchUp {
			id := uint64(100)
			env := frame(protocol.TypeThreadItems, protocol.ThreadItemsPayload{ConversationID: "conv", Final: true, Items: initialItems})
			env.InReplyTo = &id
			frames = append([]protocol.Envelope{env}, frames...)
			return 100
		}
		sends++
		switch sends {
		case 1, 2:
			p, ok := payload.(protocol.SendMessagePayload)
			want := SendNowPrompt("control")
			if sends == 2 {
				want = SendNowQueuedText
			}
			if kind != protocol.TypeSendMessage || !ok || p.ConversationID != "conv" || p.Text != want {
				t.Fatal("placement trigger or queued text changed")
			}
		case 3:
			p, ok := payload.(protocol.SendQueuedNowPayload)
			if kind != protocol.TypeSendQueuedNow || !ok || p.ConversationID != "conv" || p.QueuedMsgID != 7 || string(p.AcceptedItemID) != "70" {
				t.Fatal("send-now did not use the surfaced queued identity")
			}
		default:
			t.Fatal("unexpected extra action")
		}
		return uint64(sends)
	}, Next: func(deadline time.Time) protocol.Envelope {
		if len(frames) == 0 {
			return protocol.Envelope{}
		}
		env := frames[0]
		frames = frames[1:]
		return env
	}}
	SendNowPlacement(t, wire, "conv", SendNowPrompt("control"), time.Second, SendNowSettle)
	if sends != 3 || len(frames) != 0 {
		t.Fatal("actions or late-frame evidence were omitted")
	}
}

func mustQueueJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
