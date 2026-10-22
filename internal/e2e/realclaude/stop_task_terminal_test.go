//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// stopTaskTerminalItem requires the exact held call's canonical ending and the
// matching producer fact. A control response or roster omission alone is no proof.
func stopTaskTerminalItem(items []protocol.ThreadItem, entries []history.Entry, conv, call, task string, after uint64) bool {
	for _, item := range items {
		if item.Active || item.EndedOrder == 0 || item.Agent != "claude" || item.Session == "" || (item.Kind != "tool_call" && item.Kind != "agent") {
			continue
		}
		var content struct {
			Call string `json:"tool_use_id"`
			Task string `json:"task_id"`
		}
		if json.Unmarshal(item.Content, &content) != nil || content.Call != call || content.Task != task {
			continue
		}
		for _, entry := range entries {
			if entry.ID <= after || entry.ID != item.EndedOrder || entry.Session == nil || entry.Session.Kind != item.Agent || entry.Session.SessionID != item.Session {
				continue
			}
			if entry.Type != "background_task_outcome" && entry.Type != "background_task_gone" {
				continue
			}
			var fact struct {
				Conversation string `json:"conversation_id"`
				Task         string `json:"task_id"`
				Status       string `json:"status"`
			}
			if json.Unmarshal(entry.Payload, &fact) != nil || fact.Conversation != conv || fact.Task != task {
				continue
			}
			want := fact.Status
			if want == "completed" {
				want = "finished"
			}
			if want != "" && want != "stopping" && item.Status == want {
				return true
			}
		}
	}
	return false
}

func TestStopTaskTerminalItem(t *testing.T) {
	item := protocol.ThreadItem{ID: 1, Kind: "tool_call", Agent: "claude", Session: "source", Status: "stopped", EndedOrder: 3, Content: json.RawMessage(`{"tool_use_id":"call","task_id":"held"}`)}
	entry := history.Entry{ID: 3, Type: "background_task_outcome", Session: &history.SessionProvenance{Kind: "claude", SessionID: "source"}, Payload: json.RawMessage(`{"conversation_id":"conv","task_id":"held","status":"stopped"}`)}
	for _, tc := range []struct {
		name   string
		change func(*protocol.ThreadItem, *history.Entry)
		want   bool
	}{
		{"saved outcome", func(*protocol.ThreadItem, *history.Entry) {}, true},
		{"active acceptance", func(i *protocol.ThreadItem, _ *history.Entry) { i.Active = true }, false},
		{"wrong task", func(i *protocol.ThreadItem, _ *history.Entry) {
			i.Content = json.RawMessage(`{"tool_use_id":"call","task_id":"other"}`)
		}, false},
		{"wrong source", func(i *protocol.ThreadItem, _ *history.Entry) { i.Session = "other" }, false},
		{"wrong ending", func(i *protocol.ThreadItem, _ *history.Entry) { i.EndedOrder++ }, false},
		{"stale ending", func(i *protocol.ThreadItem, e *history.Entry) { i.EndedOrder = 2; e.ID = 2 }, false},
		{"roster alone", func(_ *protocol.ThreadItem, e *history.Entry) { e.Type = protocol.TypeBackgroundTaskRoster }, false},
		{"no producer final", func(_ *protocol.ThreadItem, e *history.Entry) { e.Payload = json.RawMessage(`{}`) }, false},
		{"saved gone", func(i *protocol.ThreadItem, e *history.Entry) {
			i.Status = "gone"
			e.Type = "background_task_gone"
			e.Payload = json.RawMessage(`{"conversation_id":"conv","task_id":"held","status":"gone"}`)
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, e := item, entry
			i.Content, e.Payload = append(json.RawMessage(nil), item.Content...), append(json.RawMessage(nil), entry.Payload...)
			if tc.change != nil {
				tc.change(&i, &e)
			}
			if got := stopTaskTerminalItem([]protocol.ThreadItem{i}, []history.Entry{e}, "conv", "call", "held", 2); got != tc.want {
				t.Fatalf("terminal evidence accepted=%t want=%t", got, tc.want)
			}
		})
	}
}
