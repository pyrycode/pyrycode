//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRealClaude_OrdinaryQueuedPlacement(t *testing.T) {
	runParallel(t)
	h, convID := startStreamRunningTurnHarness(t)
	sealSendMessage(t, h.phone, h.initSend, 2, convID, "ordinary-start", fmt.Sprintf(
		"Use Bash once in the foreground, never background: for i in $(seq 1 15); do sleep 1; done; echo done. Then reply briefly. run=%d", time.Now().UnixNano()))
	deadline := time.Now().Add(perTurnReplyBudget)
	for {
		env, ok := nextAttachReadEnvelope(t, h, deadline)
		if !ok {
			t.Fatal("no running Bash turn")
		}
		f := scenariohelpers.ClassifyQueuePlacementFrame(t, env, convID)
		if f.Kind == "turn_end" {
			t.Fatal("turn ended before Bash started")
		}
		if f.Kind == "tool_use" && f.ToolName == "Bash" {
			break
		}
	}
	text := "Reply with exactly ORDINARY-QUEUE-PLACEMENT-2820."
	sealSendMessage(t, h.phone, h.initSend, 3, convID, "ordinary-queued", text)
	var queuedID uint64
	var ends, users int
	var replyStarted bool
	deadline = time.Now().Add(2 * perTurnReplyBudget)
	for ends < 2 {
		env, ok := nextAttachReadEnvelope(t, h, deadline)
		if !ok {
			t.Fatal("queued reply did not end")
		}
		f := scenariohelpers.ClassifyQueuePlacementFrame(t, env, convID)
		if f.Kind == "queue_state" {
			for _, item := range f.Queued {
				if item.Text == text {
					queuedID = item.QueuedMsgID
				}
			}
		}
		if f.Kind == "turn_end" {
			ends++
			continue
		}
		if f.Kind == "user_message" && f.Text == text {
			users++
			var p protocol.MessagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if ends != 1 || replyStarted || p.QueuedMsgID == 0 || p.QueuedMsgID != queuedID || p.SentNow {
				t.Fatalf("ordinary message misplaced: ends=%d reply=%v queued=%d payload=%+v", ends, replyStarted, queuedID, p)
			}
		}
		if ends == 1 {
			interactive := f.Kind == "delta" || f.Kind == "tool_use" || f.Kind == "tool_result"
			if env.Type == protocol.TypeTurnState {
				var p protocol.TurnStatePayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatal(err)
				}
				interactive = p.ConversationID == convID && p.State != "idle"
			}
			if interactive {
				replyStarted = true
				if users != 1 {
					t.Fatal("reply frame preceded ordinary user message")
				}
			}
		}
	}
	if users != 1 || !replyStarted {
		t.Fatalf("users=%d reply started=%v", users, replyStarted)
	}
}
