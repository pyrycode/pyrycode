//go:build !e2e_realclaude

package realclaude

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// Synthetic shell controls are never retained as authenticated evidence.
func TestThreadShadowRawShellLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		facts        []struct{ typ, payload string }
	}{
		{"denied", "denied", []struct{ typ, payload string }{
			{"tool_denied", `{"turn_id":"turn","tool_use_id":"call","reason":"denied"}`},
			{"main_turn_interrupted", `{"turn_id":"turn","cause":"operator_reset","occurred_at":"2026-10-09T00:00:00Z"}`},
		}},
		{"linked_running", "running", []struct{ typ, payload string }{
			{"background_task_started", `{"task_id":"task","tool_call_id":"call"}`},
			{"tool_result", `{"turn_id":"turn","tool_use_id":"call","is_error":false,"result_summary":"launch"}`},
			{"turn_end", `{"turn_id":"turn","stop_reason":"end_turn"}`},
		}},
		{"linked_finished", "finished", []struct{ typ, payload string }{
			{"background_task_started", `{"task_id":"task","tool_call_id":"call"}`},
			{"tool_result", `{"turn_id":"turn","tool_use_id":"call","is_error":false,"result_summary":"launch"}`},
			{"background_task_updated", `{"task_id":"task","status":"completed","summary":"observed task content"}`},
			{"turn_end", `{"turn_id":"turn","stop_reason":"end_turn"}`},
		}},
		{"linked_companion", "finished", []struct{ typ, payload string }{
			{"background_task_started", `{"task_id":"task","tool_call_id":"call"}`},
			{"background_task_outcome", `{"lifetime_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","task_id":"task","status":"completed","occurred_at":"2026-10-09T00:00:00Z"}`},
			{"background_task_updated", `{"task_id":"task","status":"completed","summary":"observed task content"}`},
			{"tool_result", `{"turn_id":"turn","tool_use_id":"call","is_error":false,"result_summary":"launch after task completion"}`},
			{"background_task_updated", `{"task_id":"task","status":"completed","summary":"later duplicate must not replace the companion"}`},
		}},
		{"late_link", "finished", []struct{ typ, payload string }{
			{"tool_result", `{"turn_id":"turn","tool_use_id":"call","is_error":false,"result_summary":"launch"}`},
			{"turn_end", `{"turn_id":"turn","stop_reason":"end_turn"}`},
			{"background_task_updated", `{"task_id":"task","status":"completed","summary":"observed task content"}`},
			{"background_task_started", `{"task_id":"task","tool_call_id":"call"}`},
		}},
		{"linked_session_end", "ended_with_session", []struct{ typ, payload string }{
			{"background_task_started", `{"task_id":"task","tool_call_id":"call"}`},
			{"main_tool_interrupted", `{"turn_id":"turn","tool_call_id":"call","cause":"operator_reset","occurred_at":"2026-10-09T00:00:00Z"}`},
			{"main_turn_interrupted", `{"turn_id":"turn","cause":"operator_reset","occurred_at":"2026-10-09T00:00:00Z"}`},
			{"agent_ended_with_session", `{"tool_call_id":"call","call_observed_entry_id":1,"cause":"operator_reset","occurred_at":"2026-10-09T00:00:00Z"}`},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := shadowHistory{Conversation: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
			facts := append([]struct{ typ, payload string }{{"tool_use", `{"turn_id":"turn","tool_use_id":"call","name":"Bash","input":{"command":"controlled"}}`}}, tc.facts...)
			for i, fact := range facts {
				var fields map[string]any
				if err := json.Unmarshal([]byte(fact.payload), &fields); err != nil {
					t.Fatal(err)
				}
				fields["conversation_id"] = h.Conversation
				raw, _ := json.Marshal(fields)
				h.Entries = append(h.Entries, history.Entry{ID: uint64(i + 1), Type: fact.typ, Payload: raw, Session: &history.SessionProvenance{SessionID: "session", Kind: "claude"}})
			}
			fold := thread.New(h.Conversation)
			if err := fold.Feed(h.Entries); err != nil {
				t.Fatal(err)
			}
			cp := shadowCheckpoint{Version: fold.Version(), Items: fold.Items()}
			if cp.Items[0].Status != tc.status {
				t.Fatal("synthetic control did not reach specified shell state")
			}
			if err := shadowRawRows(h, cp); err != nil {
				t.Fatal(err)
			}
			if tc.name != "denied" {
				for i := range h.Entries {
					if h.Entries[i].Type != "background_task_started" {
						continue
					}
					original := h.Entries[i].Session
					h.Entries[i].Session = &history.SessionProvenance{SessionID: "foreign", Kind: "claude"}
					if shadowRawRows(h, cp) == nil {
						t.Fatal("foreign-source task link accepted")
					}
					h.Entries[i].Session = original
				}
			}
			cp.Items[0].Active = !cp.Items[0].Active
			if shadowRawRows(h, cp) == nil {
				t.Fatal("changed shell active state accepted")
			}
			cp.Items[0].Active = !cp.Items[0].Active
			if tc.name != "denied" {
				cp.Items[0].EndedOrder++
				if shadowRawRows(h, cp) == nil {
					t.Fatal("changed shell terminal order accepted")
				}
				cp.Items[0].EndedOrder--
			}
			cp.Items[0].Status = "wrong"
			if shadowRawRows(h, cp) == nil {
				t.Fatal("changed shell status accepted")
			}
			cp.Items[0].Status = tc.status
			var content map[string]json.RawMessage
			_ = json.Unmarshal(cp.Items[0].Content, &content)
			for _, field := range []string{"task_id", "task_link", "result", "task_report", "ending", "denial"} {
				original, ok := content[field]
				if !ok {
					continue
				}
				content[field] = json.RawMessage(`{}`)
				cp.Items[0].Content, _ = json.Marshal(content)
				if shadowRawRows(h, cp) == nil {
					t.Fatal("changed retained shell evidence accepted")
				}
				content[field] = original
			}
		})
	}
}
