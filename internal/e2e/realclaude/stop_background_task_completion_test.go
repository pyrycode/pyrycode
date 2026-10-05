//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
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
	h, convID := startStreamRunningTurnHarness(t)
	fifo := filepath.Join(h.workdir, "stop-task.fifo")
	arrived, release := tpcapHoldFIFO(t, fifo)
	defer release()
	sealSendMessage(t, h.phone, h.initSend, 27960, convID, "stop-held-task", rafcapPrompt(fifo, time.Now().UnixNano()))
	var taskID string
	deadline := time.Now().Add(perTurnReplyBudget)
	for taskID == "" {
		env, ok := receiveEnvelope(t, h, time.Until(deadline))
		if !ok {
			t.Fatal("held task absent from daemon roster")
		}
		if env.Type == protocol.TypeError {
			t.Fatal("held task setup refused")
		}
		if env.Type != protocol.TypeBackgroundTaskRoster {
			continue
		}
		var p protocol.BackgroundTaskRosterPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal("decode held roster")
		}
		if p.ConversationID != convID {
			continue
		}
		for _, row := range p.Tasks {
			if row.TaskID != "" && row.TaskType == "local_bash" && strings.Contains(row.Description, fifo) && !slices.Contains(row.TruncatedFields, "task_id") {
				taskID = row.TaskID
				break
			}
		}
	}
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("held task never opened rig FIFO")
	}
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
