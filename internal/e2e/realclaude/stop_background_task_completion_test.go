//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRealClaudeStopBackgroundTaskCompletion requires terminal evidence while the
// rig still holds the FIFO. A control acceptance alone cannot satisfy this test.
func TestRealClaudeStopBackgroundTaskCompletion(t *testing.T) {
	runParallel(t)
	h, convID := startStreamRunningTurnHarness(t)
	fifo := filepath.Join(h.workdir, "stop-task.fifo")
	arrived, release := tpcapHoldFIFO(t, fifo)
	defer release()
	sealSendMessage(t, h.phone, h.initSend, 27960, convID, "stop-held-task", rafcapPrompt(fifo, time.Now().UnixNano()))
	var setup stopHeldTaskSetup
	var taskID string
	deadline := time.Now().Add(perTurnReplyBudget)
	for taskID == "" {
		env, ok := receiveEnvelope(t, h, time.Until(deadline))
		if !ok {
			t.Fatalf("held task setup timed out: %s fifo_arrived=%t", setup.diagnostic(), stopTaskFIFOArrived(arrived))
		}
		if err := setup.observe(env, convID, fifo); err != nil {
			t.Fatalf("held task setup: %v; %s fifo_arrived=%t", err, setup.diagnostic(), stopTaskFIFOArrived(arrived))
		}
		taskID = setup.taskID()
	}
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("held task never opened rig FIFO")
	}
	t.Logf("setup identified rig task: %s fifo_arrived=true", setup.diagnostic())
	// The hold is not released until after the completion assertion (or test teardown).
	payload, err := json.Marshal(protocol.StopBackgroundTaskPayload{ConversationID: convID, TaskID: taskID})
	if err != nil {
		t.Fatal("marshal stop")
	}
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: 27961, Type: protocol.TypeStopBackgroundTask, TS: time.Now().UTC(), Payload: payload})
	deadline = time.Now().Add(45 * time.Second)
	for {
		env, ok := receiveEnvelope(t, h, time.Until(deadline))
		if !ok {
			t.Fatal("stop acceptance did not produce task completion while FIFO held")
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatal("task stop refused")
		case protocol.TypeBackgroundTaskUpdated:
			var p protocol.BackgroundTaskUpdatedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal("decode terminal update")
			}
			if p.ConversationID == convID && p.TaskID == taskID && p.Status == "stopped" {
				t.Log("completion signal: background_task_updated status=stopped while FIFO held")
				return
			}
		case protocol.TypeBackgroundTaskRoster:
			var p protocol.BackgroundTaskRosterPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal("decode terminal roster")
			}
			if p.ConversationID == convID && !slices.ContainsFunc(p.Tasks, func(row protocol.BackgroundTask) bool { return row.TaskID == taskID }) {
				t.Log("completion signal: background_task_roster omits held task while FIFO held")
				return
			}
		}
	}
}

func stopTaskFIFOArrived(arrived <-chan struct{}) bool {
	select {
	case <-arrived:
		return true
	default:
		return false
	}
}

// stopHeldTaskSetup retains the latest roster because Claude can publish it
// before the start carrying the task-to-tool join. A start alone proves no roster.
type stopHeldTaskSetup struct {
	toolID                                           string
	start                                            protocol.BackgroundTaskStartedPayload
	tasks                                            []protocol.BackgroundTask
	rosters, rows, tools, starts, descriptionMatches int
}

func (s *stopHeldTaskSetup) observe(env protocol.Envelope, convID, fifo string) error {
	switch env.Type {
	case protocol.TypeError:
		return fmt.Errorf("request refused")
	case protocol.TypeToolUse:
		var p protocol.ToolUsePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return fmt.Errorf("decode tool use")
		}
		if p.ConversationID == convID {
			s.tools++
			if p.Name == "Bash" && p.ToolUseID != "" && p.Input["command"] == "cat "+fifo && p.Input["run_in_background"] == "true" {
				s.toolID = p.ToolUseID
			}
		}
	case protocol.TypeBackgroundTaskStarted:
		var p protocol.BackgroundTaskStartedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return fmt.Errorf("decode task start")
		}
		if p.ConversationID == convID {
			s.starts++
			if p.TaskType == "local_bash" && p.TaskID != "" && p.ToolCallID != "" && !slices.Contains(p.TruncatedFields, "task_id") && !slices.Contains(p.TruncatedFields, "tool_call_id") {
				s.start = p
			}
		}
	case protocol.TypeBackgroundTaskRoster:
		var p protocol.BackgroundTaskRosterPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return fmt.Errorf("decode task roster")
		}
		if p.ConversationID == convID {
			s.rosters++
			s.rows += len(p.Tasks)
			s.tasks = p.Tasks
			for _, row := range p.Tasks {
				if strings.Contains(row.Description, fifo) {
					s.descriptionMatches++
				}
			}
		}
	}
	return nil
}

func (s *stopHeldTaskSetup) taskID() string {
	var startedID string
	if s.toolID != "" && s.start.ToolCallID == s.toolID {
		startedID = s.start.TaskID
	}
	return stopHeldTaskID(s.tasks, s.toolID, startedID)
}

func (s *stopHeldTaskSetup) diagnostic() string {
	return fmt.Sprintf("tools=%d exact_background_tool=%t starts=%d start_joined=%t rosters=%d rows=%d description_matches=%d roster_joined=%t",
		s.tools, s.toolID != "", s.starts, s.toolID != "" && s.start.ToolCallID == s.toolID, s.rosters, s.rows, s.descriptionMatches, s.taskID() != "")
}

func stopHeldTaskID(tasks []protocol.BackgroundTask, toolID, startedID string) string {
	if toolID == "" {
		return ""
	}
	for _, row := range tasks {
		if row.TaskID == "" || row.TaskType != "local_bash" || slices.Contains(row.TruncatedFields, "task_id") {
			continue
		}
		if row.TaskID == startedID || (row.ToolCallID == toolID && !slices.Contains(row.TruncatedFields, "tool_call_id")) {
			return row.TaskID
		}
	}
	return ""
}

func TestStopHeldTaskID(t *testing.T) {
	const fifo = "/rig/stop-task.fifo"
	for _, tc := range []struct {
		name                    string
		row                     protocol.BackgroundTask
		toolID, startedID, want string
	}{
		{"summarized description joined by start", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", Description: "Hold FIFO"}, "tool", "held", "held"},
		{"enriched roster", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", ToolCallID: "tool"}, "tool", "", "held"},
		{"unrelated task with rig path in prose", protocol.BackgroundTask{TaskID: "other", TaskType: "local_bash", ToolCallID: "other-tool", Description: fifo}, "tool", "held", ""},
		{"missing tool identity", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", Description: fifo}, "", "held", ""},
		{"missing task id", protocol.BackgroundTask{TaskType: "local_bash", ToolCallID: "tool"}, "tool", "", ""},
		{"truncated task id", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", ToolCallID: "tool", TruncatedFields: []string{"task_id"}}, "tool", "held", ""},
		{"truncated tool id", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", ToolCallID: "tool", TruncatedFields: []string{"tool_call_id"}}, "tool", "", ""},
		{"wrong task type", protocol.BackgroundTask{TaskID: "held", TaskType: "agent", ToolCallID: "tool"}, "tool", "held", ""},
		{"truncated description is irrelevant", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", Description: "cat /ri", TruncatedFields: []string{"description"}}, "tool", "held", "held"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stopHeldTaskID([]protocol.BackgroundTask{tc.row}, tc.toolID, tc.startedID); got != tc.want {
				t.Errorf("task identity matched=%t; want matched=%t", got != "", tc.want != "")
			}
		})
	}
}

func TestStopHeldTaskSetup(t *testing.T) {
	const conv, fifo = "bound", "/rig/stop-task.fifo"
	tool := protocol.ToolUsePayload{ConversationID: conv, ToolUseID: "tool", Name: "Bash", Input: map[string]string{"command": "cat " + fifo, "run_in_background": "true"}}
	start := protocol.BackgroundTaskStartedPayload{ConversationID: conv, TaskID: "held", ToolCallID: "tool", TaskType: "local_bash", Description: "Hold FIFO"}
	roster := protocol.BackgroundTaskRosterPayload{ConversationID: conv, Tasks: []protocol.BackgroundTask{{TaskID: "held", TaskType: "local_bash", Description: "Hold FIFO"}}}
	envelope := func(typ string, payload any) protocol.Envelope {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: typ, Payload: raw}
	}
	for _, tc := range []struct {
		name   string
		order  string
		mutate func(*protocol.ToolUsePayload, *protocol.BackgroundTaskStartedPayload, *protocol.BackgroundTaskRosterPayload)
		want   bool
	}{
		{"roster before start as observed live", "trs", nil, true},
		{"start before roster", "tsr", nil, true},
		{"tool after task metadata", "srt", nil, true},
		{"start alone is not backgrounding", "ts", nil, false},
		{"roster without tool join", "tr", nil, false},
		{"task absent in latest roster", "trse", nil, false},
		{"wrong conversation tool", "trs", func(p *protocol.ToolUsePayload, _ *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.ConversationID = "other"
		}, false},
		{"wrong conversation start", "trs", func(_ *protocol.ToolUsePayload, p *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.ConversationID = "other"
		}, false},
		{"wrong conversation roster", "trs", func(_ *protocol.ToolUsePayload, _ *protocol.BackgroundTaskStartedPayload, p *protocol.BackgroundTaskRosterPayload) {
			p.ConversationID = "other"
		}, false},
		{"different command", "trs", func(p *protocol.ToolUsePayload, _ *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.Input["command"] = "cat /other/fifo"
		}, false},
		{"foreground command", "trs", func(p *protocol.ToolUsePayload, _ *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.Input["run_in_background"] = "false"
		}, false},
		{"unrelated task start", "trs", func(_ *protocol.ToolUsePayload, p *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.ToolCallID = "other"
		}, false},
		{"truncated start id", "trs", func(_ *protocol.ToolUsePayload, p *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.TruncatedFields = []string{"task_id"}
		}, false},
		{"truncated start tool id", "trs", func(_ *protocol.ToolUsePayload, p *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.TruncatedFields = []string{"tool_call_id"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, s, r := tool, start, roster
			p.Input = map[string]string{"command": "cat " + fifo, "run_in_background": "true"}
			if tc.mutate != nil {
				tc.mutate(&p, &s, &r)
			}
			frames := map[rune]protocol.Envelope{
				't': envelope(protocol.TypeToolUse, p),
				's': envelope(protocol.TypeBackgroundTaskStarted, s),
				'r': envelope(protocol.TypeBackgroundTaskRoster, r),
				'e': envelope(protocol.TypeBackgroundTaskRoster, protocol.BackgroundTaskRosterPayload{ConversationID: conv}),
			}
			var setup stopHeldTaskSetup
			for _, key := range tc.order {
				if err := setup.observe(frames[key], conv, fifo); err != nil {
					t.Fatal(err)
				}
			}
			if got := setup.taskID() != ""; got != tc.want {
				t.Fatalf("identified=%t want=%t; %s", got, tc.want, setup.diagnostic())
			}
			if strings.Contains(setup.diagnostic(), fifo) || strings.Contains(setup.diagnostic(), "Hold FIFO") {
				t.Fatal("diagnostic exposes payload content")
			}
		})
	}
}
