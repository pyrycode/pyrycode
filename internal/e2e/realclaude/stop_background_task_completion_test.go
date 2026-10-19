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
	hold := newStopHoldOperation(t, h.workdir, fifo)
	reader := newStopTaskReader(t, h)
	sealSendMessage(t, h.phone, h.initSend, 27960, convID, "stop-held-task", hold.prompt())
	setup := stopHeldTaskSetup{hold: hold}
	var taskID string
	deadline := time.Now().Add(perTurnReplyBudget)
	for taskID == "" {
		env, ok := reader.receive(t, min(100*time.Millisecond, time.Until(deadline)))
		if !ok {
			if time.Now().After(deadline) {
				t.Fatalf("held task setup timed out: %s fifo_arrived=%t", setup.diagnostic(), stopTaskFIFOArrived(arrived))
			}
		} else if err := setup.observe(env, convID, fifo); err != nil {
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
		env, ok := reader.receive(t, time.Until(deadline))
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
	hold                                             *stopHoldOperation
	operationConflict                                bool
	toolIDs                                          map[string]bool
	startJoins                                       []protocol.BackgroundTaskStartedPayload
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
		}
		matches := p.Input["command"] == "cat "+fifo && p.ConversationID == convID
		if s.hold != nil {
			matches = s.hold.matches(p.Input["command"])
			// An unsupported use cannot lend physical evidence to a later claim.
			if p.Name == "Bash" && !matches && s.hold.mentioned(p.Input["command"]) {
				s.operationConflict = true
			}
		}
		eligible := p.ConversationID == convID && p.ToolUseID != "" && p.Input["run_in_background"] == "true"
		if p.Name == "Bash" && matches && (s.hold != nil || eligible) {
			if s.toolIDs == nil {
				s.toolIDs = make(map[string]bool)
			}
			if previous, seen := s.toolIDs[p.ToolUseID]; seen {
				eligible = eligible && previous
			}
			s.toolIDs[p.ToolUseID] = eligible
		}
	case protocol.TypeBackgroundTaskStarted:
		var p protocol.BackgroundTaskStartedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return fmt.Errorf("decode task start")
		}
		if p.ConversationID == convID {
			s.starts++
			if p.TaskType == "local_bash" && p.TaskID != "" && p.ToolCallID != "" && !slices.Contains(p.TruncatedFields, "task_id") && !slices.Contains(p.TruncatedFields, "tool_call_id") {
				s.startJoins = append(s.startJoins, p)
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
	toolID := s.uniqueToolID()
	if s.operationConflict || (s.hold != nil && !s.hold.executed()) {
		return ""
	}
	var startedID string
	for _, start := range s.startJoins {
		if toolID != "" && start.ToolCallID == toolID {
			if startedID != "" && startedID != start.TaskID {
				return ""
			}
			startedID = start.TaskID
		}
	}
	id := stopHeldTaskID(s.tasks, toolID, startedID)
	for _, start := range s.startJoins {
		if id != "" && start.TaskID == id && start.ToolCallID != toolID {
			return ""
		}
	}
	return id
}

func (s *stopHeldTaskSetup) uniqueToolID() string {
	if len(s.toolIDs) == 1 {
		for id, eligible := range s.toolIDs {
			if eligible {
				return id
			}
		}
	}
	return ""
}

func (s *stopHeldTaskSetup) diagnostic() string {
	return fmt.Sprintf("tools=%d operation_tools=%d operation_conflict=%t unique_background_tool=%t execution_witness=%t starts=%d rosters=%d rows=%d description_matches=%d roster_joined=%t",
		s.tools, len(s.toolIDs), s.operationConflict, s.uniqueToolID() != "", s.hold != nil && s.hold.executed(), s.starts, s.rosters, s.rows, s.descriptionMatches, s.taskID() != "")
}

func stopHeldTaskID(tasks []protocol.BackgroundTask, toolID, startedID string) string {
	if toolID == "" {
		return ""
	}
	var taskID string
	for _, row := range tasks {
		if row.TaskID == "" || row.TaskType != "local_bash" || slices.Contains(row.TruncatedFields, "task_id") {
			continue
		}
		if row.TaskID == startedID || (row.ToolCallID == toolID && !slices.Contains(row.TruncatedFields, "tool_call_id")) {
			if row.ToolCallID != "" && !slices.Contains(row.TruncatedFields, "tool_call_id") && row.ToolCallID != toolID {
				return ""
			}
			if taskID != "" && taskID != row.TaskID {
				return ""
			}
			taskID = row.TaskID
		}
	}
	for _, row := range tasks {
		if taskID != "" && row.TaskID == taskID && row.ToolCallID != "" && row.ToolCallID != toolID && !slices.Contains(row.TruncatedFields, "tool_call_id") {
			return ""
		}
	}
	if startedID != "" && taskID != startedID {
		return ""
	}
	return taskID
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
		{"start and roster disagree on task", protocol.BackgroundTask{TaskID: "other", TaskType: "local_bash", ToolCallID: "tool"}, "tool", "held", ""},
		{"unrelated task with rig path in prose", protocol.BackgroundTask{TaskID: "other", TaskType: "local_bash", ToolCallID: "other-tool", Description: fifo}, "tool", "held", ""},
		{"missing tool identity", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", Description: fifo}, "", "held", ""},
		{"missing task id", protocol.BackgroundTask{TaskType: "local_bash", ToolCallID: "tool"}, "tool", "", ""},
		{"truncated task id", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", ToolCallID: "tool", TruncatedFields: []string{"task_id"}}, "tool", "held", ""},
		{"truncated tool id", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", ToolCallID: "tool", TruncatedFields: []string{"tool_call_id"}}, "tool", "", ""},
		{"valid start replaces truncated roster tool key", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", ToolCallID: "partial", TruncatedFields: []string{"tool_call_id"}}, "tool", "held", "held"},
		{"wrong task type", protocol.BackgroundTask{TaskID: "held", TaskType: "agent", ToolCallID: "tool"}, "tool", "held", ""},
		{"truncated description is irrelevant", protocol.BackgroundTask{TaskID: "held", TaskType: "local_bash", Description: "cat /ri", TruncatedFields: []string{"description"}}, "tool", "held", "held"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stopHeldTaskID([]protocol.BackgroundTask{tc.row}, tc.toolID, tc.startedID); got != tc.want {
				t.Errorf("task identity matched=%t; want matched=%t", got != "", tc.want != "")
			}
		})
	}
	t.Run("competing roster identities", func(t *testing.T) {
		rows := []protocol.BackgroundTask{
			{TaskID: "held", TaskType: "local_bash", ToolCallID: "tool"},
			{TaskID: "other", TaskType: "local_bash", ToolCallID: "tool"},
		}
		if stopHeldTaskID(rows, "tool", "") != "" {
			t.Fatal("ambiguous roster selected a task")
		}
	})
	t.Run("contradictory roster join", func(t *testing.T) {
		rows := []protocol.BackgroundTask{{TaskID: "held", TaskType: "local_bash", ToolCallID: "other"}}
		if stopHeldTaskID(rows, "tool", "held") != "" {
			t.Fatal("start overrode contradictory roster tool identity")
		}
	})
	t.Run("duplicate task with competing tool", func(t *testing.T) {
		rows := []protocol.BackgroundTask{
			{TaskID: "held", TaskType: "local_bash", ToolCallID: "tool"},
			{TaskID: "held", TaskType: "local_bash", ToolCallID: "other"},
		}
		if stopHeldTaskID(rows, "tool", "") != "" {
			t.Fatal("contradictory duplicate row selected a task")
		}
	})
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
		{"roster tool start", "rts", nil, true},
		{"roster start tool", "rst", nil, true},
		{"start tool roster", "str", nil, true},
		{"duplicate observations", "ttrrss", nil, true},
		{"unrelated start cannot erase join", "trsw", nil, true},
		{"competing tool identities", "trsu", nil, false},
		{"competing start identities", "trsv", nil, false},
		{"competing start tools", "trsx", nil, false},
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
		{"non Bash tool", "trs", func(p *protocol.ToolUsePayload, _ *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.Name = "Read"
		}, false},
		{"missing tool id", "trs", func(p *protocol.ToolUsePayload, _ *protocol.BackgroundTaskStartedPayload, _ *protocol.BackgroundTaskRosterPayload) {
			p.ToolUseID = ""
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
			otherTool := p
			otherTool.ToolUseID = "other-tool"
			frames['u'] = envelope(protocol.TypeToolUse, otherTool)
			otherStart := s
			otherStart.TaskID = "other-task"
			frames['v'] = envelope(protocol.TypeBackgroundTaskStarted, otherStart)
			otherStart.ToolCallID = "other-tool"
			frames['w'] = envelope(protocol.TypeBackgroundTaskStarted, otherStart)
			otherStart.TaskID = s.TaskID
			frames['x'] = envelope(protocol.TypeBackgroundTaskStarted, otherStart)
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
