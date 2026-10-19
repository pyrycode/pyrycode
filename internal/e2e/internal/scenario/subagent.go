package scenario

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func SubagentText(t *testing.T, w Wire, convID, prompt string, budget time.Duration) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "subagent-text", Text: prompt})

	deadline := time.Now().Add(budget)
	var agentID string
	lanes := map[string]*subagentTextLane{
		"": {},
	}
	for {
		env := w.Next(deadline)
		if env.Type == "" {
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
			t.Logf("foreground agent requested background=%t", p.Input["run_in_background"] == "true")
			agentID = p.ToolUseID
			lanes[agentID] = &subagentTextLane{}

		case protocol.TypeToolResult:
			var p protocol.ToolResultPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid subagent tool result")
			}
			if p.ConversationID == convID && p.ToolUseID == agentID {
				lower := strings.ToLower(p.ResultSummary)
				t.Logf("foreground agent result: error=%t summary_bytes=%d not_found=%t model=%t permission=%t agent_type=%t usage=%t", p.IsError, len(p.ResultSummary), strings.Contains(lower, "not found"), strings.Contains(lower, "model"), strings.Contains(lower, "permission"), strings.Contains(lower, "agent type"), strings.Contains(lower, "usage"))
			}

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
