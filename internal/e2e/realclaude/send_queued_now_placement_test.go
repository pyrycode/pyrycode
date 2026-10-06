//go:build e2e_realclaude

package realclaude

// TestRealClaude_SendQueuedNowPlacement is the live gate for #2730: a message the
// operator queued behind a running turn and then pushed in with send_queued_now
// must appear on the wire where claude actually read it. Claude reads stdin
// input written mid-turn after the tool call that was running, so the operator's
// own `message` push has to sit after that call's tool_result and before the
// assistant text that follows it, not at the moment the daemon wrote stdin.
//
// It composes the running-turn seam (startStreamRunningTurnHarness, whose daemon
// runs with --dangerously-skip-permissions so no modal blocks the Bash call) with
// sealSendMessage, sealEnvelope and nextAttachReadEnvelope as the single in-order
// reader. The Bash command is the silent foreground loop runningTurnPrompt uses,
// not a bare `sleep`, because claude backgrounds a bare sleep and the turn would
// end before the send-now lands.
//
// Every envelope is recorded in arrival order in one log across all phases, and
// the assertions read positions in that log. No assertion pins claude's wording
// beyond the marker it was asked to include.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	sendNowMarker      = "SENDNOW-PLACEMENT-7Q"
	sendNowLoopSeconds = 25
	sendNowSettle      = 3 * time.Second
)

// sendNowFrame is one recorded envelope for the driving conversation.
type sendNowFrame struct {
	kind      string // tool_use, tool_result, delta, user_message, turn_end, queue_state, unrecognized
	toolName  string
	toolUseID string
	text      string
	queued    []protocol.QueuedItem
}

func TestRealClaude_SendQueuedNowPlacement(t *testing.T) {
	runParallel(t)
	h, convID := startStreamRunningTurnHarness(t)
	queuedText := "One more thing: include the exact word " + sendNowMarker + " in your final reply."

	var log []sendNowFrame
	// next reads one envelope, records it if it concerns convID, and returns the
	// recorded frame (kind "" for anything else). ok=false on the deadline.
	next := func(deadline time.Time) (sendNowFrame, bool) {
		t.Helper()
		env, ok := nextAttachReadEnvelope(t, h, deadline)
		if !ok {
			return sendNowFrame{}, false
		}
		f := classifySendNowFrame(t, env, convID)
		if f.kind != "" {
			log = append(log, f)
		}
		return f, true
	}

	// 1. One long foreground Bash call.
	sealSendMessage(t, h.phone, h.initSend, 2, convID, "m-2", fmt.Sprintf(
		"Use the Bash tool exactly once to run this command and wait for it to finish in the "+
			"foreground — do NOT run it in the background: for i in $(seq 1 %d); do sleep 1; done; echo slept. "+
			"When it finishes, reply in one short paragraph. run=%d", sendNowLoopSeconds, time.Now().UnixNano()))

	// 2. Wait for the Bash call to start.
	var bashID string
	deadline := time.Now().Add(perTurnReplyBudget)
	for bashID == "" {
		f, ok := next(deadline)
		if !ok {
			t.Fatalf("no Bash tool_use for %q within %s", convID, perTurnReplyBudget)
		}
		switch {
		case f.kind == "turn_end":
			t.Fatalf("the turn ended before any Bash tool_use; claude did not run the loop")
		case f.kind == "tool_use" && f.toolName == "Bash":
			bashID = f.toolUseID
		}
	}
	t.Logf("Bash tool_use %s started", bashID)

	// 3. Queue the marker message behind the busy turn and read its queued id.
	sealSendMessage(t, h.phone, h.initSend, 3, convID, "m-3", queuedText)
	var queuedID uint64
	deadline = time.Now().Add(30 * time.Second)
	for queuedID == 0 {
		f, ok := next(deadline)
		if !ok {
			t.Fatalf("no queue_state listing the marker message within 30s; the send was not queued")
		}
		if f.kind == "tool_result" && f.toolUseID == bashID {
			t.Fatalf("the Bash call finished before the message was queued; the send-now would land after it and prove nothing")
		}
		if f.kind == "queue_state" {
			for _, item := range f.queued {
				if item.Text == queuedText {
					queuedID = item.QueuedMsgID
				}
			}
		}
	}

	// 4. Send it now, while the Bash call is still running.
	sendQueuedNow(t, h, convID, queuedID)
	t.Logf("send_queued_now for queued id %d sent", queuedID)

	// 5. Collect until the turn ends, then settle for late frames.
	deadline = time.Now().Add(perTurnReplyBudget)
	for ended := false; !ended; {
		f, ok := next(deadline)
		if !ok {
			t.Fatalf("no turn_end for %q within %s after send_queued_now", convID, perTurnReplyBudget)
		}
		ended = f.kind == "turn_end"
	}
	settle := time.Now().Add(sendNowSettle)
	for {
		if _, ok := next(settle); !ok {
			break
		}
	}

	// 6. Assertions over arrival order.
	toolResultIdx, finalTextIdx, turnEnds := -1, -1, 0
	var userIdx []int
	var finalText strings.Builder
	var lastQueue []protocol.QueuedItem
	for i, f := range log {
		switch f.kind {
		case "tool_result":
			if f.toolUseID == bashID && toolResultIdx < 0 {
				toolResultIdx = i
			}
		case "delta":
			if toolResultIdx >= 0 {
				if finalTextIdx < 0 {
					finalTextIdx = i
				}
				finalText.WriteString(f.text)
			}
		case "user_message":
			if strings.Contains(f.text, sendNowMarker) {
				userIdx = append(userIdx, i)
				if f.text != queuedText {
					t.Errorf("user message push text = %q, want %q", f.text, queuedText)
				}
			}
		case "turn_end":
			turnEnds++
		case "queue_state":
			lastQueue = f.queued
		case "unrecognized":
			t.Errorf("an unrecognized_message reached the phone at log position %d; a replayed user echo leaked past the parser", i)
		}
	}
	if turnEnds != 1 {
		t.Errorf("turn_end count = %d, want exactly 1; the sent-now message started a turn of its own", turnEnds)
	}
	if toolResultIdx < 0 {
		t.Fatalf("no tool_result for Bash call %s was observed", bashID)
	}
	if finalTextIdx < 0 {
		t.Fatalf("no assistant text after the Bash tool_result")
	}
	if !strings.Contains(finalText.String(), sendNowMarker) {
		t.Errorf("final assistant text does not contain %q: %q", sendNowMarker, finalText.String())
	}
	if len(userIdx) != 1 {
		t.Fatalf("user message pushes carrying the marker = %d, want exactly 1", len(userIdx))
	}
	if userIdx[0] < toolResultIdx || userIdx[0] > finalTextIdx {
		t.Errorf("user message push at log position %d, want after the Bash tool_result (%d) and before the final assistant text (%d)",
			userIdx[0], toolResultIdx, finalTextIdx)
	}
	if len(lastQueue) != 0 {
		t.Errorf("last queue_state for %q lists %d entries, want none: %+v", convID, len(lastQueue), lastQueue)
	}
}

// sendQueuedNow seals a send_queued_now control envelope for convID's queued
// entry id. The daemon sends no reply; the queue_state and the stream show the
// outcome. It mirrors the fake-claude suite's dequeue_message send.
func sendQueuedNow(t *testing.T, h *perConvHarness, convID string, id uint64) {
	t.Helper()
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      4,
		Type:    protocol.TypeSendQueuedNow,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendQueuedNowPayload{ConversationID: convID, QueuedMsgID: id}),
	})
}

// classifySendNowFrame maps an envelope to a recorded frame when it concerns
// convID. Subagent text and tool rows are skipped, since only the main thread's
// order is under test. An error envelope is fatal.
func classifySendNowFrame(t *testing.T, env protocol.Envelope, convID string) sendNowFrame {
	t.Helper()
	decode := func(v any) {
		if err := json.Unmarshal(env.Payload, v); err != nil {
			t.Fatalf("decode %s payload: %v", env.Type, err)
		}
	}
	switch env.Type {
	case protocol.TypeError:
		t.Fatalf("error envelope: %s", string(env.Payload))
	case protocol.TypeUnrecognizedMessage:
		return sendNowFrame{kind: "unrecognized"}
	case protocol.TypeToolUse:
		var p protocol.ToolUsePayload
		decode(&p)
		if p.ConversationID == convID && p.ParentToolUseID == "" {
			return sendNowFrame{kind: "tool_use", toolName: p.Name, toolUseID: p.ToolUseID}
		}
	case protocol.TypeToolResult:
		var p protocol.ToolResultPayload
		decode(&p)
		if p.ConversationID == convID && p.ParentToolUseID == "" {
			return sendNowFrame{kind: "tool_result", toolUseID: p.ToolUseID}
		}
	case protocol.TypeAssistantDelta:
		var p protocol.AssistantDeltaPayload
		decode(&p)
		if p.ConversationID == convID && p.ParentToolUseID == "" && strings.TrimSpace(p.Text) != "" {
			return sendNowFrame{kind: "delta", text: p.Text}
		}
	case protocol.TypeMessage:
		var p protocol.MessagePayload
		decode(&p)
		if p.ConversationID == convID && p.Role == "user" {
			return sendNowFrame{kind: "user_message", text: p.Text}
		}
	case protocol.TypeTurnEnd:
		var p protocol.TurnEndPayload
		decode(&p)
		if p.ConversationID == convID {
			return sendNowFrame{kind: "turn_end"}
		}
	case protocol.TypeQueueState:
		var p protocol.QueueStatePayload
		decode(&p)
		if p.ConversationID == convID {
			return sendNowFrame{kind: "queue_state", queued: p.Queued}
		}
	}
	return sendNowFrame{}
}
