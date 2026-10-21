// Package scenario contains test-only daemon assertions shared by live and recorded tests.
package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// SendNowPrompt keeps the foreground-loop instruction unchanged. Live callers
// supply a fresh run nonce; recorded callers use a stable request contract.
func SendNowPrompt(run string) string {
	return fmt.Sprintf("Use the Bash tool exactly once to run this command and wait for it to finish in the "+
		"foreground — do NOT run it in the background: for i in $(seq 1 %d); do sleep 1; done; echo slept. "+
		"When it finishes, reply in one short paragraph. run=%s", SendNowLoopSeconds, run)
}

const (
	SendNowMarker      = "SENDNOW-PLACEMENT-7Q"
	SendNowQueuedText  = "One more thing: include the exact word " + SendNowMarker + " in your final reply."
	SendNowLoopSeconds = 25
	SendNowSettle      = 3 * time.Second
)

// QueuePlacementFrame is one recorded envelope for the driving conversation.
type QueuePlacementFrame struct {
	Kind             string // tool_use, tool_result, delta, user_message, turn_end, queued_item, unrecognized
	ToolName         string
	ToolUseID        string
	Text             string
	QueuedID, ItemID uint64
	RequestID        uint64
	MessageID        string
	Status           string
	Active, Shown    bool
	Order            uint64
	SentNow          bool
}

// SendNowPlacement preserves the live queue/action and complete arrival-order
// assertions, including the late-frame window. Replay proves protocol order;
// actual Bash execution and elapsed provider time remain live obligations.
func SendNowPlacement(t *testing.T, w Wire, convID, prompt string, turnBudget, settleBudget time.Duration) {
	t.Helper()
	queuedText := SendNowQueuedText

	reader := newQueuePlacementReader(t, w, convID)
	var log []QueuePlacementFrame
	// next reads one envelope, records it if it concerns convID, and returns the
	// recorded frame (kind "" for anything else). ok=false on the deadline.
	next := func(deadline time.Time) (QueuePlacementFrame, bool) {
		t.Helper()
		f, ok := reader.next(deadline)
		if !ok {
			return QueuePlacementFrame{}, false
		}
		if f.Kind != "" {
			log = append(log, f)
		}
		return f, true
	}

	// 1. One long foreground Bash call.
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "m-2", Text: prompt})

	// 2. Wait for the Bash call to start.
	var bashID string
	deadline := time.Now().Add(turnBudget)
	for bashID == "" {
		f, ok := next(deadline)
		if !ok {
			t.Fatalf("no Bash tool_use for %q within %s", convID, turnBudget)
		}
		switch {
		case f.Kind == "turn_end":
			t.Fatalf("the turn ended before any Bash tool_use; claude did not run the loop")
		case f.Kind == "tool_use" && f.ToolName == "Bash":
			bashID = f.ToolUseID
		}
	}
	t.Logf("Bash tool_use %s started", bashID)

	// 3. Queue the marker message behind the busy turn and read its queued id.
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "m-3", Text: queuedText})
	var queuedID, acceptedID uint64
	deadline = time.Now().Add(30 * time.Second)
	for queuedID == 0 {
		f, ok := next(deadline)
		if !ok {
			t.Fatalf("no queued thread item carrying the marker message within 30s; the send was not queued")
		}
		if f.Kind == "tool_result" && f.ToolUseID == bashID {
			t.Fatalf("the Bash call finished before the message was queued; the send-now would land after it and prove nothing")
		}
		if f.Kind == "queued_item" && f.Text == queuedText {
			queuedID, acceptedID = f.QueuedID, f.ItemID
		}
	}

	// 4. Send it now, while the Bash call is still running.
	w.Send(protocol.TypeSendQueuedNow, protocol.SendQueuedNowPayload{ConversationID: convID, QueuedMsgID: queuedID, AcceptedItemID: json.RawMessage(fmt.Sprint(acceptedID))})
	t.Logf("send_queued_now for queued id %d sent", queuedID)

	// 5. Collect until the turn ends, then settle for late frames.
	deadline = time.Now().Add(turnBudget)
	for ended := false; !ended; {
		f, ok := next(deadline)
		if !ok {
			t.Fatalf("no turn_end for %q within %s after send_queued_now", convID, turnBudget)
		}
		ended = f.Kind == "turn_end"
	}
	settle := time.Now().Add(settleBudget)
	for {
		if _, ok := next(settle); !ok {
			break
		}
	}

	// 6. Assertions over arrival order.
	toolResultIdx, finalTextIdx, turnEnds := -1, -1, 0
	var userIdx []int
	var finalText strings.Builder
	terminal := false
	for i, f := range log {
		switch f.Kind {
		case "tool_result":
			if f.ToolUseID == bashID && toolResultIdx < 0 {
				toolResultIdx = i
			}
		case "delta":
			if toolResultIdx >= 0 {
				if finalTextIdx < 0 {
					finalTextIdx = i
				}
				finalText.WriteString(f.Text)
			}
		case "user_message":
			if strings.Contains(f.Text, SendNowMarker) {
				userIdx = append(userIdx, i)
				if f.Text != queuedText {
					t.Errorf("user message push text = %q, want %q", f.Text, queuedText)
				}
			}
		case "turn_end":
			turnEnds++
		}
		if f.ItemID == acceptedID {
			terminal = f.Status == "delivered"
		}
		switch f.Kind {
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
	if !strings.Contains(finalText.String(), SendNowMarker) {
		t.Errorf("final assistant text does not contain %q: %q", SendNowMarker, finalText.String())
	}
	if len(userIdx) != 1 {
		t.Fatalf("user message pushes carrying the marker = %d, want exactly 1", len(userIdx))
	}
	if userIdx[0] < toolResultIdx || userIdx[0] > finalTextIdx {
		t.Errorf("user message push at log position %d, want after the Bash tool_result (%d) and before the final assistant text (%d)",
			userIdx[0], toolResultIdx, finalTextIdx)
	}
	if !terminal {
		t.Error("queued item never became delivered")
	}
}

// ClassifyQueuePlacementFrame maps an envelope to a recorded frame when it concerns
// convID. Subagent text and tool rows are skipped, since only the main thread's
// order is under test. An error envelope is fatal.
func ClassifyQueuePlacementFrame(t *testing.T, env protocol.Envelope, convID string) QueuePlacementFrame {
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
		return QueuePlacementFrame{Kind: "unrecognized"}
	case protocol.TypeToolUse:
		var p protocol.ToolUsePayload
		decode(&p)
		if p.ConversationID == convID && p.ParentToolUseID == "" {
			return QueuePlacementFrame{Kind: "tool_use", ToolName: p.Name, ToolUseID: p.ToolUseID}
		}
	case protocol.TypeToolResult:
		var p protocol.ToolResultPayload
		decode(&p)
		if p.ConversationID == convID && p.ParentToolUseID == "" {
			return QueuePlacementFrame{Kind: "tool_result", ToolUseID: p.ToolUseID}
		}
	case protocol.TypeAssistantDelta:
		var p protocol.AssistantDeltaPayload
		decode(&p)
		if p.ConversationID == convID && p.ParentToolUseID == "" && strings.TrimSpace(p.Text) != "" {
			return QueuePlacementFrame{Kind: "delta", Text: p.Text}
		}
	case protocol.TypeMessage:
		var p protocol.MessagePayload
		decode(&p)
		if p.ConversationID == convID && p.Role == "user" {
			return QueuePlacementFrame{Kind: "user_message", Text: p.Text, QueuedID: p.QueuedMsgID, SentNow: p.SentNow}
		}
	case protocol.TypeTurnEnd:
		var p protocol.TurnEndPayload
		decode(&p)
		if p.ConversationID == convID {
			return QueuePlacementFrame{Kind: "turn_end"}
		}
	}
	return QueuePlacementFrame{}
}
