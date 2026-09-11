//go:build e2e_realclaude

package realclaude

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestInteractiveStreamForwardsSubagentText(t *testing.T) {
	h, convID := startStreamRunningTurnHarness(t)
	nonce := time.Now().UnixNano()
	sealSendMessage(t, h.phone, h.initSend, 2, convID, "m-subagent-2331",
		fmt.Sprintf("Use the Agent tool exactly once and wait for it in the foreground. Ask the agent to write exactly twelve numbered paragraphs about reliable process supervision, with at least two complete sentences per paragraph, and not to use tools. After it returns, write your own distinct twelve numbered paragraphs on the same topic, again with at least two complete sentences each. Do not use any other tools. run=%d", nonce))

	deadline := time.Now().Add(180 * time.Second)
	var agentID string
	lanes := map[string]*subagentTextLane{
		"": {},
	}
	for {
		env, ok := nextSubagentTextEnvelope(t, h, deadline)
		if !ok {
			t.Fatalf("turn did not reach terminal idle; Agent/Task id=%q main=%v child=%v",
				agentID, lanes[""], lanes[agentID])
		}
		switch env.Type {
		case protocol.TypeToolUse:
			var p protocol.ToolUsePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode tool_use payload: %v", err)
			}
			if p.ConversationID != convID || (p.Name != "Agent" && p.Name != "Task") {
				continue
			}
			if p.ParentToolUseID != "" {
				t.Fatalf("foreground %s call has parent_tool_use_id %q, want empty", p.Name, p.ParentToolUseID)
			}
			if p.ToolUseID == "" {
				t.Fatalf("foreground %s call has an empty tool_use_id", p.Name)
			}
			if agentID != "" {
				t.Fatalf("observed multiple foreground Agent/Task calls: %q and %q", agentID, p.ToolUseID)
			}
			agentID = p.ToolUseID
			lanes[agentID] = &subagentTextLane{}

		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID != convID || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if p.ParentToolUseID != "" && p.ParentToolUseID != agentID {
				t.Fatalf("assistant_delta parent_tool_use_id = %q before/mismatch with foreground Agent/Task id %q",
					p.ParentToolUseID, agentID)
			}
			lane := lanes[p.ParentToolUseID]
			if lane == nil {
				t.Fatalf("assistant_delta reached unregistered parent lane %q", p.ParentToolUseID)
			}
			lane.observe(t, p)

		case protocol.TypeUnrecognizedMessage:
			t.Fatalf("turn emitted unrecognized_message: %s", env.Payload)

		case protocol.TypeTurnState:
			var p protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if p.ConversationID != convID || p.State != "idle" {
				continue
			}
			assertSubagentTextLanes(t, agentID, lanes)
			return
		}
	}
}

type subagentTextLane struct {
	turnID string
	seqs   []int
}

func (l *subagentTextLane) observe(t *testing.T, p protocol.AssistantDeltaPayload) {
	t.Helper()
	if l.turnID == "" {
		if p.Seq != 0 {
			t.Fatalf("first delta for parent %q has seq %d, want 0", p.ParentToolUseID, p.Seq)
		}
		l.turnID = p.TurnID
	} else if p.TurnID != l.turnID {
		t.Fatalf("parent %q changed turn_id from %q to %q", p.ParentToolUseID, l.turnID, p.TurnID)
	}
	if p.Seq != len(l.seqs) {
		t.Fatalf("parent %q seq = %d, want lane-local %d", p.ParentToolUseID, p.Seq, len(l.seqs))
	}
	l.seqs = append(l.seqs, p.Seq)
}

func assertSubagentTextLanes(t *testing.T, agentID string, lanes map[string]*subagentTextLane) {
	t.Helper()
	if agentID == "" {
		t.Fatal("no foreground Agent/Task tool_use observed")
	}
	main, child := lanes[""], lanes[agentID]
	if len(main.seqs) < 2 || len(child.seqs) == 0 {
		t.Fatalf("lane deltas main=%v child=%v, want an advancing main lane and a non-empty child lane", main.seqs, child.seqs)
	}
	if main.turnID == "" || child.turnID == "" || main.turnID == child.turnID {
		t.Fatalf("lane turn ids main=%q child=%q, want distinct non-empty ids", main.turnID, child.turnID)
	}
}

func nextSubagentTextEnvelope(t *testing.T, h *perConvHarness, deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				return protocol.Envelope{}, false
			}
			t.Fatalf("phone receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return env, true
	}
}
