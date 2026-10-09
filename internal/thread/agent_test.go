package thread

import (
	"encoding/json"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/history"
	"strings"
	"testing"
)

func testAgent(t *testing.T, f *Fold, status string, active bool, ended uint64) Item {
	t.Helper()
	var agents []Item
	for _, item := range f.Items() {
		if item.Kind == "agent" {
			agents = append(agents, item)
		}
	}
	if len(agents) != 1 {
		t.Fatalf("agents: %#v", agents)
	}
	item := agents[0]
	var data struct {
		Ended uint64 `json:"ended_order"`
	}
	if !decode(item.Content, &data) {
		t.Fatal("invalid content")
	}
	if item.Status != status || item.Active != active || data.Ended != ended || item.EndedOrder != ended {
		t.Fatalf("agent: %#v content=%s", item, item.Content)
	}
	return item
}
func testAgentCall(id uint64) history.Entry {
	return testMain(id, "tool_use", `,"tool_use_id":"c","name":"Agent","input":{"prompt":"original"}`)
}
func testTask(id uint64, typ, extra string) history.Entry {
	return testEntry(id, typ, `{"task_id":"task"`+extra+`}`)
}
func testDurable(id uint64, typ, extra string) history.Entry {
	return testEntry(id, typ, `{"conversation_id":"a","lifetime_id":"11111111-1111-4111-8111-111111111111","occurred_at":"2026-10-09T00:00:00Z"`+extra+`}`)
}
func TestAgentLifecyclePermutations(t *testing.T) {
	t.Parallel()
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			entries := []history.Entry{testAgentCall(1)}
			reports := []history.Entry{testMain(0, "tool_result", `,"tool_use_id":"c","is_error":false,"result_summary":"launch"`), testTask(0, "background_task_started", `,"tool_call_id":"c","description":"work"`), testTask(0, "background_task_updated", `,"status":"completed","summary":"finished work"`)}
			var ended uint64
			for _, n := range order {
				e := reports[n]
				e.ID = uint64(len(entries) + 1)
				entries = append(entries, e)
				if n == 2 {
					ended = e.ID
				}
			}
			f := testMainReplay(t, entries)
			item := testAgent(t, f, "finished", false, ended)
			testContent(t, item, "input", map[string]any{"prompt": "original"})
			testContent(t, item, "task_id", "task")
			var result map[string]any
			if !decode(item.Content, &result) || result["result"] == nil {
				t.Fatal("lost launch result")
			}
		})
	}
	f := testMainReplay(t, []history.Entry{testMain(1, "tool_result", `,"tool_use_id":"c","is_error":true`), testAgentCall(2)})
	testAgent(t, f, "failed", false, 2)
	f = New("a")
	testFeed(t, f, testAgentCall(1), testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false`))
	testAgent(t, f, "finished", false, 2)
	testFeed(t, f, testTask(3, "background_task_started", `,"tool_call_id":"c"`))
	testAgent(t, f, "running", true, 0)
}
func TestAgentFirstFinal(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed", "stopped", "gone", "ended_with_session", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := testMainReplay(t, []history.Entry{testAgentCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testTask(3, "background_task_updated", `,"status":"`+status+`"`), testTask(4, "background_task_updated", `,"status":"failed"`)})
			want := status
			if want == "completed" {
				want = "finished"
			}
			item := testAgent(t, f, want, false, 3)
			rev := item.Rev
			testFeed(t, f, testTask(5, "background_task_updated", `,"status":"`+status+`"`))
			if testAgent(t, f, want, false, 3).Rev != rev {
				t.Fatal("redundant report revised item")
			}
		})
	}
	f := testMainReplay(t, []history.Entry{testAgentCall(1), testMain(2, "tool_denied", `,"tool_use_id":"c","reason":"denied"`), testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`), testTask(4, "background_task_started", `,"tool_call_id":"c"`)})
	testAgent(t, f, "denied", false, 2)
}
func TestAgentTaskEnrichment(t *testing.T) {
	t.Parallel()
	f := New("a")
	testFeed(t, f, testAgentCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testTask(3, "background_task_updated", `,"status":"stopping"`))
	testAgent(t, f, "stopping", true, 0)
	testFeed(t, f, testTask(4, "background_task_updated", `,"summary":"new summary","patch":"{\"status\":\"stopped\"}"`), testTask(5, "background_task_progress", `,"description":"progress"`), testEntry(6, "background_task_roster", `{"tasks":[]}`), testMain(7, "turn_end", `,"stop_reason":"end_turn"`))
	item := testAgent(t, f, "stopping", true, 0)
	if item.Rev != 4 {
		t.Fatalf("progress revised item: %#v", item)
	}
	testFeed(t, f, testTask(8, "background_task_updated", `,"status":"stopped"`))
	testAgent(t, f, "stopped", false, 8)
}
func TestAgentDurableCompanions(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`), testAgentCall(2), testDurable(3, "agent_call_result", `,"tool_call_id":"c","status":"completed"`), testMain(4, "tool_result", `,"tool_use_id":"c","is_error":false,"result_summary":"saved"`), testDurable(5, "background_task_observed", `,"task_id":"task"`), testDurable(6, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`), testTask(7, "background_task_started", `,"tool_call_id":"c"`), testDurable(8, "background_task_outcome", `,"task_id":"task","status":"completed"`), testTask(9, "background_task_updated", `,"status":"completed","summary":"real outcome"`)}
	f := testMainReplay(t, entries)
	item := testAgent(t, f, "finished", false, 8)
	if item.ID != 2 || item.Order != 2 || item.Rev != 9 {
		t.Fatalf("durable stole identity: %#v", item)
	}
	testContent(t, item, "task_report", map[string]any{"task_id": "task", "status": "completed", "summary": "real outcome"})
}

func TestAgentIdentityScopes(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"operator_reset", "idle_sleep", "daemon_restart"} {
		t.Run(boundary, func(t *testing.T) {
			entries := []history.Entry{testAgentCall(1), testDivider(2, boundary, "s", "n", "claude", "claude"), testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`)}
			f := testMainReplay(t, entries)
			want, active, ended := "running", true, uint64(0)
			if boundary == "daemon_restart" {
				want, active, ended = "finished", false, 3
			}
			testAgent(t, f, want, active, ended)
		})
	}
	entries := []history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`), testAgentCall(2), testDurable(3, "agent_call_result", `,"tool_call_id":"c","status":"completed"`), testMain(4, "tool_result", `,"tool_use_id":"c","is_error":false`), testDurable(5, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`), testAgentCall(6)}
	entries[4].Payload = []byte(strings.ReplaceAll(string(entries[4].Payload), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"))
	f := testMainReplay(t, entries)
	testMainItem(t, f, 2, 4, "agent", "finished", false)
	testMainItem(t, f, 6, 6, "agent", "running", true)
	for _, kind := range []string{"codex", "none", "unknown"} {
		entries := []history.Entry{testAgentCall(1), testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false`)}
		for i := range entries {
			entries[i].Session = testSource(kind, "s")
		}
		f := testMainReplay(t, entries)
		for _, item := range f.Items() {
			if item.Kind == "agent" {
				t.Fatal("non-Claude agent")
			}
		}
		if kind == "codex" {
			testMainItem(t, f, 1, 2, "tool_call", "done", false)
		}
	}
	// A report from another recorded source cannot end the launch.
	entries = []history.Entry{testAgentCall(1), testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false`)}
	entries[0].Session = testSource("claude", "one")
	entries[1].Session = testSource("claude", "two")
	testAgent(t, testMainReplay(t, entries), "running", true, 0)
}
func TestAgentRecoveryReferences(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"1", "null", "0", "99999999999999999", "4", "2", "99"} {
		t.Run(ref, func(t *testing.T) {
			ending := testEntry(4, "agent_ended_with_session", `{"conversation_id":"a","tool_call_id":"c","call_observed_entry_id":`+ref+`,"cause":"daemon_restart","occurred_at":"2026-10-09T00:00:00Z"}`)
			f := testMainReplay(t, []history.Entry{testAgentCall(1), testDivider(2, "operator_reset", "old", "new", "claude", "claude"), testAgentCall(3), ending})
			if len(f.Items()) != 3 {
				t.Fatal(f.Items())
			}
			first := f.Items()[0]
			second := f.Items()[2]
			if ref == "1" {
				if first.Status != "ended_with_session" || first.EndedOrder != 4 {
					t.Fatal(first)
				}
			} else if !first.Active {
				t.Fatal(first)
			}
			if !second.Active {
				t.Fatal("reference closed reused call")
			}
		})
	}
	for _, extra := range []string{`,"tool_call_id":"wrong"`, `,"tool_call_id":"c","lifetime_id":"22222222-2222-4222-8222-222222222222"`} {
		f := testMainReplay(t, []history.Entry{testAgentCall(1), testEntry(2, "agent_ended_with_session", `{"conversation_id":"a","call_observed_entry_id":1,"cause":"reset","occurred_at":"2026-10-09T00:00:00Z"`+extra+`}`)})
		testAgent(t, f, "running", true, 0)
	}
	// Task-only recovery must reach a call via its saved link, across a boundary.
	f := testMainReplay(t, []history.Entry{testAgentCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testDivider(3, "operator_reset", "s", "n", "claude", "claude"), testEntry(4, "agent_ended_with_session", `{"conversation_id":"a","task_id":"task","task_observed_entry_id":2,"cause":"idle_sleep","occurred_at":"2026-10-09T00:00:00Z"}`)})
	item := testAgent(t, f, "ended_with_session", false, 4)
	var raw map[string]any
	_ = json.Unmarshal(item.Content, &raw)
	if raw["ending"].(map[string]any)["cause"] != "idle_sleep" {
		t.Fatal("lost cause")
	}
}
func TestAgentInvalidFacts(t *testing.T) {
	t.Parallel()
	f := New("a")
	e := testAgentCall(1)
	e.Payload = []byte(strings.Replace(string(e.Payload), `"input":`, `"parent_tool_use_id":"parent","input":`, 1))
	e.Shown = new(bool)
	testFeed(t, f, e)
	e.Payload[2] = 'x'
	original := testAgent(t, f, "running", true, 0)
	if original.Shown || original.Turn != "t" {
		t.Fatal(original)
	}
	testContent(t, original, "parent_tool_use_id", "parent")
	original.Content[2] = 'x'
	testContent(t, f.Items()[0], "input", map[string]any{"prompt": "original"})
	for i, raw := range []string{`{"tool_use_id":"c","is_error":null}`, `{"tool_use_id":"c","is_error":false,"conversation_id":"foreign"}`, `{"tool_use_id":"c","is_error":false,"truncated_fields":["tool_use_id"]}`} {
		testFeed(t, f, testEntry(uint64(i+2), "tool_result", raw))
	}
	testAgent(t, f, "running", true, 0)
	if f.Version() != 4 {
		t.Fatal(f.Version())
	}
	// A truncated call link retains task evidence for a later complete link.
	f = testMainReplay(t, []history.Entry{testAgentCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c","truncated_fields":["tool_call_id"]`), testTask(3, "background_task_updated", `,"status":"completed"`), testTask(4, "background_task_started", `,"tool_call_id":"c"`)})
	testAgent(t, f, "finished", false, 3)
}

func TestAgentEarlyTaskReportsAndChildLaunch(t *testing.T) {
	t.Parallel()
	f := testMainReplay(t, []history.Entry{testTask(1, "background_task_updated", `,"status":"completed","summary":"early"`), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testAgentCall(3)})
	item := testAgent(t, f, "finished", false, 3)
	if item.Rev != 3 {
		t.Fatal(item)
	}
	entries := []history.Entry{testMain(1, "assistant_delta", `,"text":"one"`), testMain(2, "tool_use", `,"tool_use_id":"c","name":"Task","parent_tool_use_id":"parent"`), testMain(3, "assistant_delta", `,"text":"two"`)}
	f = testMainReplay(t, entries)
	item = testAgent(t, f, "running", true, 0)
	testContent(t, item, "parent_tool_use_id", "parent")
	testContent(t, testMainItem(t, f, 1, 3, "assistant_message", "running", true), "text", "onetwo")
}
func TestAgentSavedGoneAndEnding(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"background_task_gone", "agent_ended_with_session"} {
		ending := `,"task_id":"task","tool_call_id":"c","status":"gone"`
		want := "gone"
		if typ == "agent_ended_with_session" {
			ending = `,"task_id":"task","tool_call_id":"c","cause":"capacity_eviction"`
			want = "ended_with_session"
		}
		entries := []history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`), testAgentCall(2), testDurable(3, "background_task_observed", `,"task_id":"task"`), testDurable(4, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`), testDurable(5, typ, ending), testTask(6, "background_task_updated", `,"status":"completed"`)}
		item := testAgent(t, testMainReplay(t, entries), want, false, 5)
		if typ == "agent_ended_with_session" {
			var content map[string]map[string]any
			_ = json.Unmarshal(item.Content, &content)
			if content["ending"]["cause"] != "capacity_eviction" {
				t.Fatal("lost saved cause")
			}
		}
	}
}

func TestAgentCompanionPendingFinals(t *testing.T) {
	t.Parallel()
	// A late link discards the foreground result and exposes the earliest task
	// ending, including one whose mapped companion arrived after a conflicting end.
	entries := []history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`), testDurable(2, "agent_call_result", `,"tool_call_id":"c","status":"completed"`), testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`), testDurable(4, "background_task_outcome", `,"task_id":"task","status":"failed"`), testDurable(5, "agent_ended_with_session", `,"tool_call_id":"c","cause":"daemon_restart"`), testTask(6, "background_task_updated", `,"status":"failed","summary":"failure detail"`), testAgentCall(7), testDurable(8, "background_task_linked", `,"tool_call_id":"c","task_id":"task"`)}
	f := testMainReplay(t, entries)
	item := testAgent(t, f, "failed", false, 7)
	if item.Rev != 8 {
		t.Fatal(item)
	}
	testContent(t, item, "task_report", map[string]any{"task_id": "task", "status": "failed", "summary": "failure detail"})
}

func TestAgentDurableParentAndSourceReferences(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent","parent_tool_call_id":"outer"`), testAgentCall(2), testDurable(3, "agent_ended_with_session", `,"tool_call_id":"c","call_observed_entry_id":1,"cause":"reset"`)}
	entries[0].Session = testSource("claude", "original")
	entries[1].Session = testSource("claude", "original")
	entries[2].Session = testSource("claude", "different")
	item := testAgent(t, testMainReplay(t, entries), "running", true, 0)
	testContent(t, item, "parent_tool_call_id", "outer")
	entries[2].Session = testSource("claude", "original")
	testAgent(t, testMainReplay(t, entries), "ended_with_session", false, 3)
}

func TestAgentRecoveryMismatchedLink(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testAgentCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testMain(3, "tool_use", `,"tool_use_id":"other","name":"Agent"`), testEntry(4, "agent_ended_with_session", `{"conversation_id":"a","tool_call_id":"other","task_id":"task","task_observed_entry_id":2,"cause":"reset","occurred_at":"2026-10-09T00:00:00Z"}`)}
	f := testMainReplay(t, entries)
	for _, item := range f.Items() {
		if !item.Active || item.Status != "running" {
			t.Fatal(item)
		}
	}
}
