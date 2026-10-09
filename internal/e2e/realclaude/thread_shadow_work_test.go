//go:build !e2e_realclaude

package realclaude

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// These in-memory mutations test the independent witness, never live artifacts.
func TestThreadShadowPairRejectsWorkEvidence(t *testing.T) {
	for cp := 1; cp < len(shadowChecks); cp++ {
		t.Run(shadowChecks[cp], func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				id    uint64
				field string
				value any
			}{
				{"agent_name", 6, "name", "changed"},
				{"agent_input", 6, "input", map[string]string{"prompt": "changed"}},
				{"agent_summary", 6, "input_summary", "changed"},
				{"agent_call", 6, "tool_use_id", "changed"},
				{"agent_turn", 6, "turn_id", "changed"},
				{"agent_result_missing", 6, "result", nil},
				{"agent_result_changed", 6, "result", map[string]string{"result_summary": "changed"}},
				{"agent_running", 6, "state", true},
				{"agent_status", 6, "status", "done"},
				{"agent_terminal_order", 6, "ended", uint64(7)},
				{"child_running", 7, "state", true},
				{"child_done", 7, "status", "done"},
				{"child_ending_missing", 7, "parent_ending", nil},
				{"tool_name", 2, "name", "changed"},
				{"tool_call", 2, "tool_use_id", "changed"},
				{"tool_summary", 2, "input_summary", "changed"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					h, e := testShadowUserPair(t)
					found := false
					for i := range e.Checkpoints[cp].Items {
						item := &e.Checkpoints[cp].Items[i]
						if item.ID != tc.id {
							continue
						}
						found = true
						switch tc.field {
						case "state":
							item.Status, item.Active = "running", true
						case "status":
							item.Status = tc.value.(string)
						case "ended":
							item.EndedOrder = tc.value.(uint64)
						default:
							testShadowSetContent(t, item, tc.field, tc.value)
						}
					}
					if !found {
						t.Fatal("synthetic mutation target missing")
					}
					if shadowValidatePair(h, e, false) == nil {
						t.Fatal("changed work evidence accepted")
					}
				})
			}
		})
	}
}

func testShadowSetContent(t *testing.T, item *thread.Item, field string, value any) {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal(item.Content, &content); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		delete(content, field)
	} else {
		content[field] = value
	}
	var err error
	item.Content, err = json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
}

func TestThreadShadowPairRejectsClosureDivider(t *testing.T) {
	for _, mutation := range []string{"missing", "content", "id", "order", "kind", "session", "agent", "no_child", "active", "shown"} {
		t.Run(mutation, func(t *testing.T) {
			h, e := testShadowUserPair(t)
			cp := &e.Checkpoints[3]
			found := false
			for i := range cp.Items {
				item := &cp.Items[i]
				if item.ID != cp.Version {
					continue
				}
				found = true
				switch mutation {
				case "missing":
					cp.Items = append(cp.Items[:i], cp.Items[i+1:]...)
				case "content":
					item.Content = json.RawMessage(`{"unrelated":true}`)
				case "id":
					item.ID--
				case "order":
					item.Order--
				case "kind":
					item.Kind = "turn_end"
				case "session":
					item.Session = "foreign"
				case "agent":
					item.Agent = "codex"
				case "no_child":
					item.NoChild = true
				case "active":
					item.Active = true
				case "shown":
					item.Shown = false
				}
				break
			}
			if !found {
				t.Fatal("synthetic divider missing")
			}
			if shadowValidatePair(h, e, false) == nil {
				t.Fatal("missing or changed closure divider accepted")
			}
		})
	}
}

func TestThreadShadowRawAgentLifecycle(t *testing.T) {
	type fact struct{ typ, payload string }
	result := fact{"tool_result", `{"turn_id":"main","tool_use_id":"agent","is_error":false,"result_summary":"controlled launch"}`}
	link := fact{"background_task_started", `{"task_id":"task","tool_call_id":"agent"}`}
	completed := fact{"background_task_updated", `{"task_id":"task","status":"completed","summary":"controlled final"}`}
	mainEnd := fact{"turn_end", `{"turn_id":"main","stop_reason":"end_turn"}`}
	for _, tc := range []struct {
		name, status, child string
		facts               []fact
	}{
		{"unlinked_main_end", "running", "running", []fact{mainEnd}},
		{"foreground_finished", "finished", "interrupted", []fact{result, mainEnd}},
		{"foreground_failed", "failed", "interrupted", []fact{{"tool_result", `{"turn_id":"main","tool_use_id":"agent","is_error":true,"result_summary":"controlled failure"}`}}},
		{"denied", "denied", "interrupted", []fact{link, {"tool_denied", `{"turn_id":"main","tool_use_id":"agent","reason":"controlled denial"}`}}},
		{"linked_unrelated_main_end", "running", "running", []fact{link, result, mainEnd}},
		{"late_link_reopens", "running", "running", []fact{result, mainEnd, link}},
		{"linked_finished", "finished", "interrupted", []fact{link, result, completed, mainEnd}},
		{"linked_child_shell", "finished", "interrupted", []fact{{"background_task_started", `{"task_id":"child-task","tool_call_id":"child-tool"}`}, result, mainEnd}},
		{"nested_parent_closure", "finished", "running", []fact{{"assistant_delta", `{"turn_id":"nested","parent_tool_use_id":"child-tool","text":"controlled grandchild"}`}, result, mainEnd}},
		{"linked_stopping", "stopping", "running", []fact{link, result, {"background_task_updated", `{"task_id":"task","status":"stopping"}`}, mainEnd}},
		{"linked_stopped", "stopped", "interrupted", []fact{link, result, {"background_task_updated", `{"task_id":"task","status":"stopped"}`}, mainEnd}},
		{"linked_session_end", "ended_with_session", "interrupted", []fact{link, result, {"agent_ended_with_session", `{"tool_call_id":"agent","call_observed_entry_id":1,"cause":"operator_reset","occurred_at":"2026-10-09T00:00:00Z"}`}}},
		{"child_own_end", "running", "interrupted", []fact{link, result, {"turn_end", `{"turn_id":"child","parent_tool_use_id":"agent","stop_reason":"end_turn"}`}, mainEnd}},
		{"durable_companions", "finished", "interrupted", []fact{
			{"agent_call_result", `{"lifetime_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","tool_call_id":"agent","status":"completed","occurred_at":"2026-10-09T00:00:00Z"}`},
			result, link,
			{"background_task_outcome", `{"lifetime_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","task_id":"task","status":"completed","occurred_at":"2026-10-09T00:00:00Z"}`},
			completed, {"background_task_updated", `{"task_id":"task","status":"failed","summary":"later conflicting final"}`},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := shadowHistory{Conversation: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
			facts := append([]fact{
				{"agent_call_observed", `{"lifetime_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","tool_call_id":"agent","tool":"Agent","occurred_at":"2026-10-09T00:00:00Z"}`},
				{"tool_use", `{"turn_id":"main","tool_use_id":"agent","name":"Agent","input":{"prompt":"controlled"}}`},
				{"assistant_delta", `{"turn_id":"child","parent_tool_use_id":"agent","text":"controlled child"}`},
				{"tool_use", `{"turn_id":"child","parent_tool_use_id":"agent","tool_use_id":"child-tool","name":"Read","input":{"file_path":"$HOST_PATH"}}`},
			}, tc.facts...)
			if tc.name == "linked_child_shell" {
				facts[3].payload = `{"turn_id":"child","parent_tool_use_id":"agent","tool_use_id":"child-tool","name":"Bash","input":{"command":"controlled"}}`
			}
			if tc.name == "nested_parent_closure" {
				facts[3].payload = `{"turn_id":"child","parent_tool_use_id":"agent","tool_use_id":"child-tool","name":"Task","input":{"prompt":"controlled"}}`
			}
			for i, f := range facts {
				var p map[string]any
				if err := json.Unmarshal([]byte(f.payload), &p); err != nil {
					t.Fatal(err)
				}
				p["conversation_id"] = h.Conversation
				raw, _ := json.Marshal(p)
				h.Entries = append(h.Entries, history.Entry{ID: uint64(i + 1), Type: f.typ, Payload: raw, Session: &history.SessionProvenance{SessionID: "session", Kind: "claude"}})
			}
			fold := thread.New(h.Conversation)
			if err := fold.Feed(h.Entries); err != nil {
				t.Fatal(err)
			}
			cp := shadowCheckpoint{Version: fold.Version(), Items: fold.Items()}
			if len(cp.Items) < 3 || cp.Items[0].Status != tc.status || cp.Items[2].Status != tc.child {
				t.Fatal("synthetic control did not reach named parent/child states")
			}
			if err := shadowRawRows(h, cp); err != nil {
				t.Fatal(err)
			}
			for _, index := range []int{0, 1, 2} {
				original := cp.Items[index]
				cp.Items[index].Active = !original.Active
				if shadowRawRows(h, cp) == nil {
					t.Fatal("changed parent/child activity accepted")
				}
				cp.Items[index] = original
				cp.Items[index].Status = "unrelated"
				if shadowRawRows(h, cp) == nil {
					t.Fatal("changed parent/child status accepted")
				}
				cp.Items[index] = original
			}
			var content map[string]any
			_ = json.Unmarshal(cp.Items[0].Content, &content)
			for _, field := range []string{"result", "task_link", "task_report", "denial", "ending", "ended_order"} {
				if _, ok := content[field]; !ok {
					continue
				}
				original := cp.Items[0]
				testShadowSetContent(t, &cp.Items[0], field, nil)
				if shadowRawRows(h, cp) == nil {
					t.Fatal("missing raw agent evidence accepted")
				}
				cp.Items[0] = original
			}
		})
	}
}
