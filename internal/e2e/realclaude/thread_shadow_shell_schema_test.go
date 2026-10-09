package realclaude

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// shadowRawShell checks controlled task-linked shells against raw task evidence.
// A task link changes lifecycle ownership; a launch result cannot finish it.
func shadowRawShell(h shadowHistory, cp shadowCheckpoint, item thread.Item, source history.Entry, call protocol.ToolUsePayload, parentEnding json.RawMessage) (bool, error) {
	if (call.Name != "Bash" && call.Name != "local_bash") || (source.Session != nil && source.Session.Kind != "claude") {
		return false, nil
	}
	type fact struct {
		Task   string `json:"task_id"`
		Call   string `json:"tool_call_id"`
		Status string `json:"status"`
	}
	var task string
	var link history.Entry
	for _, entry := range h.Entries {
		if entry.ID > cp.Version {
			break
		}
		if !reflect.DeepEqual(entry.Session, source.Session) {
			continue
		}
		var p fact
		_ = json.Unmarshal(entry.Payload, &p)
		if (entry.Type == protocol.TypeBackgroundTaskStarted || entry.Type == "background_task_linked") && p.Call == call.ToolUseID && p.Task != "" {
			if task != "" && task != p.Task {
				return true, errors.New("controlled shell has ambiguous raw task links")
			}
			task = p.Task
			if link.ID == 0 || (link.Type == "background_task_linked" && entry.Type == protocol.TypeBackgroundTaskStarted) {
				link = entry
			}
		}
	}
	if task == "" {
		return false, nil
	}
	var content map[string]json.RawMessage
	_ = json.Unmarshal(item.Content, &content)
	equal := func(field string, raw json.RawMessage) bool {
		var got, want any
		return json.Unmarshal(content[field], &got) == nil && json.Unmarshal(raw, &want) == nil && reflect.DeepEqual(got, want)
	}
	var savedTask string
	_ = json.Unmarshal(content["task_id"], &savedTask)
	if savedTask != task || !equal("task_link", link.Payload) {
		return true, errors.New("shell task ownership differs from raw link")
	}
	status, active := "running", true
	var final history.Entry
	var finalField, finalStatus string
	companion := false
	var result history.Entry
	for _, entry := range h.Entries {
		if entry.ID > cp.Version {
			break
		}
		if !reflect.DeepEqual(entry.Session, source.Session) {
			continue
		}
		var p fact
		_ = json.Unmarshal(entry.Payload, &p)
		field := ""
		switch entry.Type {
		case protocol.TypeToolResult, protocol.TypeToolDenied:
			var report protocol.ToolResultPayload
			_ = json.Unmarshal(entry.Payload, &report)
			if report.ToolUseID != call.ToolUseID || report.TurnID != call.TurnID {
				continue
			}
			if entry.Type == protocol.TypeToolResult {
				if result.ID == 0 {
					result = entry
				}
				continue
			}
			field, p.Status = "denial", "denied"
		case protocol.TypeBackgroundTaskUpdated, "background_task_outcome":
			if p.Task != task {
				continue
			}
			field = "task_report"
		case "background_task_gone", "agent_ended_with_session":
			if p.Task != task && p.Call != call.ToolUseID {
				continue
			}
			field = "ending"
			if entry.Type == "agent_ended_with_session" {
				p.Status = "ended_with_session"
			}
		default:
			continue
		}
		if final.ID == 0 {
			if p.Status == "stopping" {
				status = p.Status
			} else if p.Status != "" {
				final, finalField, finalStatus = entry, field, p.Status
				status, active = p.Status, false
				if status == "completed" {
					status = "finished"
				}
			}
		} else if !companion && final.Type == "background_task_outcome" && entry.Type == protocol.TypeBackgroundTaskUpdated && p.Status == finalStatus {
			// The durable fact owns terminal order; its mapped companion supplies content.
			final.Payload = entry.Payload
			companion = true
		}
	}
	if result.ID != 0 && !equal("result", result.Payload) {
		return true, errors.New("shell launch result differs from raw report")
	}
	if active && parentEnding != nil {
		status, active = "interrupted", false
		if !equal("parent_ending", parentEnding) {
			return true, errors.New("child shell lost independently validated parent ending")
		}
	}
	if item.Status != status || item.Active != active || item.EndedOrder != final.ID || (final.ID != 0 && !equal(finalField, final.Payload)) {
		return true, fmt.Errorf("shell lifecycle differs from raw task outcome: creation=%d terminal=%d status_match=%t active_match=%t order_match=%t content_match=%t", item.ID, final.ID, item.Status == status, item.Active == active, item.EndedOrder == final.ID, final.ID == 0 || equal(finalField, final.Payload))
	}
	return true, nil
}
